#!/bin/bash
set -e

# Cloud Native Buildpacks for Kubernetes are provided by kpack. kpack has NO
# official Helm chart, so we vendor its upstream static release manifest and
# post-process it. kpack exposes no web UI / Ingress, so no HTTPRoute is
# required; image builds are driven by kpack CRDs
# (Image/Builder/ClusterStore/ClusterStack).
#
# Post-processing (all of it in the python step below, so `install.yaml` is
# reproducible from upstream):
#   1. Namespace -- everything moves from the upstream `kpack` namespace into
#      `kpack-system`. kpack is the ONE sanctioned exception to ADR-0011's
#      "every package in adhar-system": its webhook hardcodes
#      `Secret/webhook-certs` (cmd/webhook/main.go, webhook.Options{SecretName})
#      and so does sigstore's policy-controller (security/cosign); two knative
#      webhooks that fight over one Secret leave one of them without a valid
#      serving cert, and both packages are enabled in production. The vendored
#      `kind: Namespace` object is DROPPED (ArgoCD's CreateNamespace=true creates
#      it; upstream's carries pod-security enforce=restricted, which would block
#      the build pods).
#   2. Sharing adhar-system means kpack's generic knative config ConfigMaps
#      collide with tekton's (`config-logging`, `config-observability`), so they
#      are renamed AND the matching CONFIG_*_NAME env vars are repointed --
#      renaming the object alone would silently make kpack read tekton's config.
#   3. Secret/webhook-certs is NOT renamed: kpack hardcodes it in its binary
#      (cmd/webhook/main.go -- webhook.Options{SecretName: "webhook-certs"}), so
#      a renamed Secret is simply never populated and the webhook never gets a
#      serving cert. See CONFLICTS.md: this is why cosign (which hardcodes the
#      same Secret name) and buildpack are mutually exclusive in one namespace.
#   4. HTTPS-everywhere: the controller resolves and pushes the ClusterBuilder
#      image to Harbor (harbor-core.adhar-system.svc, TLS). It must trust the
#      platform `adhar` CA, projected into adhar-system as the `adhar-ca`
#      ConfigMap by the supply-chain package's CA-distribution hook. Go honours
#      SSL_CERT_DIR, so the CA directory is searched alongside the system store.

INSTALL_YAML="manifests/install.yaml"
KPACK_VERSION="v0.17.1"
RAW_YAML="$(mktemp)"
trap 'rm -f "${RAW_YAML}"' EXIT

curl -sSL "https://github.com/buildpacks-community/kpack/releases/download/${KPACK_VERSION}/release-${KPACK_VERSION#v}.yaml" >"${RAW_YAML}"

python3 - "${RAW_YAML}" "${INSTALL_YAML}" "${KPACK_VERSION}" <<'PYEOF'
import sys, yaml

raw, out, version = sys.argv[1], sys.argv[2], sys.argv[3]
UPSTREAM_NS = "kpack"
NS = "kpack-system"   # see header: the one package kept out of adhar-system
RENAMED_CONFIGMAPS = {
    "config-logging": "buildpack-config-logging",
    "config-observability": "buildpack-config-observability",
}
CA_CONFIGMAP = "adhar-ca"

docs = [d for d in yaml.safe_load_all(open(raw)) if d]
kept = []
for d in docs:
    if d.get("kind") == "Namespace":          # (1) never ship a Namespace
        continue
    md = d.setdefault("metadata", {})
    if md.get("namespace") == UPSTREAM_NS:
        md["namespace"] = NS
    if d.get("kind") == "ConfigMap" and md.get("name") in RENAMED_CONFIGMAPS:
        md["name"] = RENAMED_CONFIGMAPS[md["name"]]
    kept.append(d)

def walk(node):
    """Repoint every remaining reference to the upstream namespace (webhook and
    CRD-conversion clientConfig service refs live deep inside the specs)."""
    if isinstance(node, dict):
        if node.get("namespace") == UPSTREAM_NS:
            node["namespace"] = NS
        for v in node.values():
            walk(v)
    elif isinstance(node, list):
        for v in node:
            walk(v)

renamed_env = 0
ca_patched = 0
for d in kept:
    walk(d)
    if d.get("kind") != "Deployment":
        continue
    pod = d["spec"]["template"]["spec"]
    for c in pod.get("containers", []):
        for e in c.get("env", []):
            # (2) repoint the knative config ConfigMap names
            if e.get("name") in ("CONFIG_LOGGING_NAME", "CONFIG_OBSERVABILITY_NAME"):
                new = RENAMED_CONFIGMAPS.get(e.get("value"))
                if new:
                    e["value"] = new
                    renamed_env += 1
        # (4) adhar CA trust for the controller (it talks to Harbor over TLS)
        if d["metadata"]["name"] == "kpack-controller":
            env = c.setdefault("env", [])
            if not any(e.get("name") == "SSL_CERT_DIR" for e in env):
                env.append({"name": "SSL_CERT_DIR",
                            "value": f"/etc/ssl/certs:/etc/{CA_CONFIGMAP}"})
            mounts = c.setdefault("volumeMounts", [])
            if not any(m.get("name") == CA_CONFIGMAP for m in mounts):
                mounts.append({"name": CA_CONFIGMAP,
                               "mountPath": f"/etc/{CA_CONFIGMAP}",
                               "readOnly": True})
            volumes = pod.setdefault("volumes", [])
            if not any(v.get("name") == CA_CONFIGMAP for v in volumes):
                volumes.append({"name": CA_CONFIGMAP,
                                "configMap": {"name": CA_CONFIGMAP, "optional": True}})
            ca_patched += 1

with open(out, "w") as f:
    f.write("# BUILDPACK (kpack) INSTALL RESOURCES\n")
    f.write("# This file is auto-generated with 'platform/stack/packages/application/buildpack/generate-manifests.sh'\n")
    f.write(f"# kpack {version} (upstream release manifest), post-processed into kpack-system (ADR-0011 exception: hardcoded Secret/webhook-certs)\n")
    f.write("\n---\n".join(yaml.safe_dump(d, sort_keys=False) for d in kept))

print(f"dropped {len(docs) - len(kept)} Namespace object(s); "
      f"repointed {renamed_env} CONFIG_*_NAME env var(s); "
      f"patched adhar CA into {ca_patched} container(s)")
PYEOF
