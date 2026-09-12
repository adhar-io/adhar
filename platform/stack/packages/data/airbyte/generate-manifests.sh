#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.9.3"

echo "# AIRBYTE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/airbyte/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add airbyte https://airbytehq.github.io/helm-charts --force-update
helm repo update airbyte
helm template --include-crds --namespace adhar-system airbyte airbyte/airbyte -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# The chart bundles a private single-node MinIO (ADR-0011 says applications use
# the platform's object store, never a second copy). There is no
# `minio.enabled` toggle in chart 1.9.3 -- templates/minio.yaml is gated on
# `global.storage.type == "minio"`, and flipping that off would move Airbyte to
# the S3 provider entirely -- so `--set minio.enabled=false` does nothing and the
# objects have to be stripped here, the same way data/kubeflow strips its
# bundled MinIO and application/posthog its bundled Kafka/ZooKeeper.
#
# The bundled StatefulSet is pinned to minio/minio:RELEASE.2023-11-20T22-40-07Z,
# a tag that is no longer pullable; because it is a pre-install hook the
# ImagePullBackOff also wedged the whole ArgoCD sync ("waiting for completion of
# hook apps/StatefulSet/airbyte-minio").
#
# values.yaml already points global.storage.minio.endpoint at
# minio.adhar-system.svc.cluster.local:9000 and global.storage.secretName at the
# ESO-mirrored airbyte-platform-minio Secret, so nothing references the stripped
# objects afterwards. The dead literal MINIO_ACCESS_KEY_ID / MINIO_SECRET_ACCESS_KEY
# ("minio"/"minio123") the chart still writes into airbyte-airbyte-secrets are
# dropped too, so no placeholder object-store credential ships in git.
python3 - "${INSTALL_YAML}" <<'PYEOF_MINIO'
import sys, yaml
p = sys.argv[1]
DROP = {
    ("StatefulSet", "airbyte-minio"),           # bundled MinIO + its volumeClaimTemplate
    ("Service", "airbyte-minio-svc"),           # its headless Service
    ("Pod", "airbyte-minio-create-bucket"),     # the post-install `mc mb` hook
    ("PersistentVolumeClaim", "airbyte-minio-pv-claim"),
}
docs = [d for d in yaml.safe_load_all(open(p)) if d]
kept = []
for d in docs:
    if (d.get("kind"), (d.get("metadata") or {}).get("name")) in DROP:
        continue
    if d.get("kind") == "Secret" and (d.get("metadata") or {}).get("name") == "airbyte-airbyte-secrets":
        for k in ("MINIO_ACCESS_KEY_ID", "MINIO_SECRET_ACCESS_KEY"):
            (d.get("stringData") or {}).pop(k, None)
    kept.append(d)
with open(p, "w") as f:
    f.write("\n---\n".join(yaml.safe_dump(d, sort_keys=False) for d in kept))
print(f"stripped bundled MinIO: {len(docs) - len(kept)} resource(s)")

# Fail loudly if anything still points at the stripped subchart.
blob = open(p).read()
for needle in ("airbyte-minio-svc", "minio123"):
    if needle in blob:
        sys.exit(f"ERROR: {needle!r} still referenced in {p}")
PYEOF_MINIO

# The bootloader runs as a Helm pre-install/pre-upgrade hook with NO delete
# policy. ArgoCD's default for such a hook is before-hook-creation, so every
# sync must first DELETE the previous hook Pod and wait for it to go away:
#
#   Running  waiting for deletion of hook /Pod/airbyte-airbyte-bootloader
#
# Combined with selfHeal re-syncing while other resources are still converging,
# the sync restarted the multi-minute bootloader over and over and never reached
# the rest of the chart, leaving Airbyte Degraded indefinitely.
#
# HookSucceeded deletes the Pod once it has succeeded instead, so the next sync
# has nothing to wait for. The bootloader stays a PreSync hook and still runs
# before the rest of the release.
python3 - ${INSTALL_YAML} <<'PYEOF'
import sys, yaml

path = sys.argv[1]
docs = list(yaml.safe_load_all(open(path)))
patched = 0
for d in docs:
    if not d:
        continue
    # ONLY Pods and Jobs: those are the hooks that run to completion and can
    # therefore "succeed". Applying HookSucceeded to a Role or ServiceAccount
    # makes ArgoCD wait for a completion that can never happen --
    #   waiting for completion of hook rbac.../Role/airbyte-admin-role
    # which stalls the sync just as badly as the problem being fixed.
    if d.get("kind") not in ("Pod", "Job"):
        continue
    ann = (d.get("metadata") or {}).get("annotations") or {}
    if "helm.sh/hook" not in ann:
        continue
    if "helm.sh/hook-delete-policy" in ann or "argocd.argoproj.io/hook-delete-policy" in ann:
        continue
    ann["argocd.argoproj.io/hook-delete-policy"] = "HookSucceeded"
    d["metadata"]["annotations"] = ann
    patched += 1

with open(path, "w") as fh:
    yaml.safe_dump_all([d for d in docs if d], fh, default_flow_style=False, sort_keys=False)
print(f"set hook-delete-policy=HookSucceeded on {patched} hook(s) that declared none")
PYEOF
