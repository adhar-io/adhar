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

# Long-lived resources must NOT be ArgoCD hooks at all.
#
# The chart annotates its ServiceAccount, Role, RoleBinding, env ConfigMap,
# secrets Secret, db Service and db StatefulSet with `helm.sh/hook: pre-install`.
# ArgoCD reads Helm hook annotations as ITS OWN hooks, and the default delete
# policy for a hook is before-hook-creation — so every sync first DELETES those
# objects and waits for them to disappear:
#
#   Running  waiting for deletion of hook rbac.../RoleBinding/airbyte-admin-binding
#
# which never resolves, because the same objects are also tracked as ordinary
# managed resources and get re-applied. Airbyte sat Degraded for 146 sync
# attempts on exactly that line.
#
# Under Helm these annotations only ordered the install; under ArgoCD the
# sync-wave ordering already does that, and these objects have to EXIST for the
# release's lifetime rather than be recreated per sync. So drop the hook
# annotations for everything that is not a Pod or Job (those are handled above,
# where HookSucceeded is correct because they run to completion).
python3 - ${INSTALL_YAML} <<'PYEOF_HOOKS'
import sys, yaml

path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
HOOK_KEYS = ("helm.sh/hook", "helm.sh/hook-weight", "helm.sh/hook-delete-policy")
cleaned = []
for d in docs:
    ann = (d.get("metadata") or {}).get("annotations") or {}
    if d.get("kind") not in ("Pod", "Job") and any(k in ann for k in HOOK_KEYS):
        for k in HOOK_KEYS:
            ann.pop(k, None)
        if ann:
            d["metadata"]["annotations"] = ann
        else:
            d["metadata"].pop("annotations", None)
        cleaned.append(f"{d.get('kind')}/{(d.get('metadata') or {}).get('name')}")

with open(path, "w") as fh:
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False)
print(f"removed helm hook annotations from {len(cleaned)} long-lived resource(s): {', '.join(cleaned)}")
PYEOF_HOOKS

# The bootloader must be WAVE-ORDERED, not a PreSync hook.
#
# The block above correctly stops the ServiceAccount/Role/RoleBinding being
# ArgoCD hooks -- they have to outlive a single sync. But the bootloader Pod is a
# Pod, so it kept `helm.sh/hook: pre-install`, and an ArgoCD PreSync hook runs
# BEFORE any ordinary resource is applied. The SA it runs as therefore does not
# exist yet:
#
#   pods "airbyte-airbyte-bootloader" is forbidden: error looking up service
#   account adhar-system/airbyte-admin: serviceaccount "airbyte-admin" not found
#
# ...retried forever, so Airbyte never installed at all (Missing, 57+ attempts).
# Under Helm this worked only because the SA was a pre-install hook too, i.e. in
# the same phase; removing the SA's hook annotation is what exposed it.
#
# So take the bootloader out of the hook phases entirely and let ArgoCD's own
# wave ordering sequence it, which is the mechanism that actually matches the
# requirement "after the SA, secrets and database; before the services":
#
#   wave 0  ServiceAccount, Role/Binding, Secret, ConfigMaps, Services, airbyte-db
#   wave 1  bootloader
#   wave 2  the seven Deployments
#
# Pod -> Job, because a bare Pod is the wrong primitive for a regular (non-hook)
# resource: it is immutable, so the next image bump fails to patch it and the
# sync breaks, and it would linger Completed in the desired state forever. A Job
# is mutable enough for ArgoCD to replace, reports Complete as Healthy, and is
# what a run-to-completion step should have been. backoffLimit is generous
# because the bootloader legitimately waits on the database.
python3 - ${INSTALL_YAML} <<'PYEOF_BOOTLOADER'
import sys, yaml

path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
HOOK_KEYS = ("helm.sh/hook", "helm.sh/hook-weight", "helm.sh/hook-delete-policy",
             "argocd.argoproj.io/hook", "argocd.argoproj.io/hook-delete-policy")

out, converted, waved = [], None, []
for d in docs:
    kind, name = d.get("kind"), (d.get("metadata") or {}).get("name")

    # The chart's `helm.sh/hook: test` connection-test Pod. Helm runs it only on
    # `helm test`; ArgoCD does not map a bare `test` hook, so it would be synced
    # as an ordinary Pod and tracked forever. A test fixture is not desired
    # state -- drop it, the same as the stripped MinIO hooks above.
    if kind == "Pod" and name == "airbyte-test-connection":
        continue

    if kind == "Pod" and name == "airbyte-airbyte-bootloader":
        meta = d["metadata"]
        ann = {k: v for k, v in (meta.get("annotations") or {}).items()
               if k not in HOOK_KEYS}
        ann["argocd.argoproj.io/sync-wave"] = "1"
        labels = dict(meta.get("labels") or {})
        pod_spec = d["spec"]
        pod_spec.setdefault("restartPolicy", "Never")
        out.append({
            "apiVersion": "batch/v1",
            "kind": "Job",
            "metadata": {"name": name, "namespace": meta.get("namespace"),
                         "labels": labels, "annotations": ann},
            "spec": {
                "backoffLimit": 10,
                "ttlSecondsAfterFinished": 600,
                "template": {"metadata": {"labels": labels}, "spec": pod_spec},
            },
        })
        converted = name
        continue

    if kind == "Deployment":
        meta = d["metadata"]
        ann = meta.get("annotations") or {}
        ann["argocd.argoproj.io/sync-wave"] = "2"
        meta["annotations"] = ann
        waved.append(name)

    out.append(d)

if not converted:
    sys.exit("ERROR: bootloader Pod not found -- did the chart rename it?")

with open(path, "w") as fh:
    yaml.safe_dump_all(out, fh, default_flow_style=False, sort_keys=False)
print(f"bootloader {converted}: Pod -> Job at wave 1; wave 2 on {len(waved)} Deployment(s)")

# Nothing may remain a PreSync hook: that phase runs before the ServiceAccount.
leftover = [f"{d['kind']}/{d['metadata']['name']}" for d in out
            if "pre-install" in ((d.get("metadata") or {}).get("annotations") or {}).get("helm.sh/hook", "")]
if leftover:
    sys.exit(f"ERROR: still PreSync hooks, will race the ServiceAccount: {leftover}")
PYEOF_BOOTLOADER
