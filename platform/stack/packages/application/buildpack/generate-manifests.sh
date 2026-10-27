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
#      `adhar-system`, like every platform package (ADR-0011). The vendored
#      `kind: Namespace` object is DROPPED (adhar-system exists before any
#      package syncs; upstream's carries pod-security enforce=restricted, which
#      would block the build pods).
#   2. Sharing adhar-system means kpack's generic knative config ConfigMaps
#      collide with tekton's (`config-logging`, `config-observability`), so they
#      are renamed AND the matching CONFIG_*_NAME env vars are repointed --
#      renaming the object alone would silently make kpack read tekton's config.
#   3. The webhook's names stay upstream's, and the step below refuses to write
#      a manifest that renames them. kpack's webhook binary hardcodes its Secret
#      (cmd/webhook/main.go -- webhook.Options{SecretName: "webhook-certs"}) and
#      runs knative.dev/pkg's webhook under the component name `webhook`, so its
#      leader-election leases are `webhook.*`. Neither can be configured: a
#      renamed Secret is never populated, and a Role scoped to the renamed one
#      forbids the binary from writing the Secret it actually uses -- the
#      2026-10-25 rename (`kpack-webhook-certs`, WEBHOOK_SECRET_NAME, which kpack
#      never reads) left kpack's admission with no certificate at all. So no
#      other knative-framework webhook may run in adhar-system with that Secret
#      or component name: sigstore's policy-controller (security/cosign) has
#      both, which is why cosign is disabled, and Knative Serving's webhook has
#      both, which is why Serving runs in knative-serving (CONFLICTS.md).
#   4. HTTPS-everywhere: the controller resolves and pushes the ClusterBuilder
#      image to Harbor (harbor-core.adhar-system.svc, TLS). It must trust the
#      platform `adhar` CA, projected into adhar-system as the `adhar-ca`
#      ConfigMap by the supply-chain package's CA-distribution hook. Go honours
#      SSL_CERT_DIR, so the CA directory is searched alongside the system store.
#   5. Resources sized for adhar-system rather than kpack's own namespace (the
#      platform's measurements, kept here so a regeneration cannot undo them):
#      - webhook: upstream's 50Mi/200Mi OOMKilled it 17 times in a row on a live
#        cluster (exit 137, CrashLoopBackOff, so kpack admission was down and
#        every Image/Build went unvalidated). Its informers cache and index the
#        whole namespace -- on a production profile ~370 pods, ~260 Secrets and
#        ~350 ConfigMaps, close to a megabyte of Secret data before decoding --
#        so its memory scales with the platform, not with kpack. 512Mi is
#        measured headroom over the ~70Mi it settles at; the CPU limit goes up
#        with it, because informer resync on a namespace this size is a burst
#        and 100m made startup look hung.
#      - controller: a reconciler (~100-200 MiB); builds run in their own pods,
#        so upstream's 1Gi request was 1Gi no build could use.

INSTALL_YAML="manifests/install.yaml"
KPACK_VERSION="v0.17.1"
RAW_YAML="$(mktemp)"
trap 'rm -f "${RAW_YAML}"' EXIT

curl -sSfL "https://github.com/buildpacks-community/kpack/releases/download/${KPACK_VERSION}/release-${KPACK_VERSION#v}.yaml" >"${RAW_YAML}"

python3 - "${RAW_YAML}" "${INSTALL_YAML}" "${KPACK_VERSION}" <<'PYEOF'
import sys, yaml

raw, out, version = sys.argv[1], sys.argv[2], sys.argv[3]
UPSTREAM_NS = "kpack"
NS = "adhar-system"
RENAMED_CONFIGMAPS = {
    "config-logging": "buildpack-config-logging",
    "config-observability": "buildpack-config-observability",
}
CA_CONFIGMAP = "adhar-ca"
# (5) per (Deployment, container); see the header for the measurements.
RESOURCES = {
    ("kpack-controller", "controller"): {
        "requests": {"cpu": "20m", "memory": "192Mi"},
        "limits": {"memory": "768Mi"},
    },
    ("kpack-webhook", "webhook"): {
        "requests": {"cpu": "10m", "memory": "128Mi"},
        "limits": {"cpu": "500m", "memory": "512Mi"},
    },
}
# (3) the names kpack's webhook binary hardcodes
WEBHOOK_SECRET = "webhook-certs"

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
resized = 0
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
        # (5) platform-sized resources
        sized = RESOURCES.get((d["metadata"]["name"], c.get("name")))
        if sized:
            c["resources"] = sized
            resized += 1

# (3) refuse to write a manifest whose webhook names differ from the ones the
# binary uses -- an upstream release that renames them needs CONFLICTS.md
# re-read, not a silent regeneration.
problems = []
secrets = {d["metadata"]["name"] for d in kept if d.get("kind") == "Secret"}
if WEBHOOK_SECRET not in secrets:
    problems.append(f"Secret/{WEBHOOK_SECRET} is missing (Secrets: {sorted(secrets)})")
granted = [n for d in kept if d.get("kind") == "Role"
           for r in d.get("rules", []) if "update" in r.get("verbs", [])
           for n in r.get("resourceNames", [])]
if WEBHOOK_SECRET not in granted:
    problems.append(f"no Role lets the webhook update Secret/{WEBHOOK_SECRET}")
for d in kept:
    if d.get("kind") != "Deployment":
        continue
    for c in d["spec"]["template"]["spec"].get("containers", []):
        if any(e.get("name") == "WEBHOOK_SECRET_NAME" for e in c.get("env", [])):
            problems.append(f"Deployment/{d['metadata']['name']} sets WEBHOOK_SECRET_NAME, which kpack never reads")
if resized != len(RESOURCES):
    problems.append(f"sized {resized} container(s), want {len(RESOURCES)}: {sorted(RESOURCES)}")
if problems:
    sys.exit("kpack post-processing failed:\n  " + "\n  ".join(problems))

with open(out, "w") as f:
    f.write("# BUILDPACK (kpack) INSTALL RESOURCES\n")
    f.write("# This file is auto-generated with 'platform/stack/packages/application/buildpack/generate-manifests.sh'\n")
    f.write(f"# kpack {version} (upstream release manifest), post-processed into adhar-system; the webhook keeps its hardcoded Secret/webhook-certs\n")
    f.write("\n---\n".join(yaml.safe_dump(d, sort_keys=False) for d in kept))

print(f"dropped {len(docs) - len(kept)} Namespace object(s); "
      f"repointed {renamed_env} CONFIG_*_NAME env var(s); "
      f"patched adhar CA into {ca_patched} container(s); "
      f"sized {resized} container(s)")
PYEOF
