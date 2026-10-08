#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.8.0"

echo "# PLANE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/plane/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add plane https://helm.plane.so --force-update
helm repo update
helm template --namespace adhar-system plane plane/plane-ce -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# The chart stamps every pod template with `timestamp: "<helm template run time>"`
# to force a rollout on `helm upgrade`. Under GitOps that is pure churn -- and on
# the migrator Job it is fatal: plane-api-migrate-1 keeps its name across renders,
# a Job's spec.template is immutable, and ArgoCD then retries the update forever
# with `field is immutable`, wedging the whole Application. Pin it so a
# regeneration only shows real changes.
#
# Consequence: a change confined to plane-app-vars (WEB_URL / CORS_ALLOWED_ORIGINS)
# no longer rolls the pods by itself -- follow such a change with
#   kubectl -n adhar-system rollout restart deploy -l app.kubernetes.io/instance=plane
sed -i '' -E 's/^([[:space:]]*)timestamp: "[^"]*"$/\1timestamp: "pinned-by-generate-manifests"/' ${INSTALL_YAML} \
  || sed -i -E 's/^([[:space:]]*)timestamp: "[^"]*"$/\1timestamp: "pinned-by-generate-manifests"/' ${INSTALL_YAML}

# The migrator is a plain Job in the chart: it runs once, and if the database
# is not up yet on a fresh cluster it exhausts backoffLimit ("timed out waiting
# for the condition") and stays Failed forever — ArgoCD then reports the whole
# Application Degraded on every sync (2026-09-15 DO bring-up). Migrations are
# idempotent, so make it a Sync hook that is recreated on every sync: a failed
# run is retried on the next sync instead of wedging the app.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import re, sys
path = sys.argv[1]
src = open(path).read()
hook = ('  name: plane-api-migrate-1\n'
        '  annotations:\n'
        '    argocd.argoproj.io/hook: Sync\n'
        '    argocd.argoproj.io/hook-delete-policy: BeforeHookCreation\n')
out, n = re.subn(r'(?m)^  name: plane-api-migrate-1\n', hook, src, count=1)
open(path, 'w').write(out)
print(f"migrator Job marked as a Sync hook ({n})")
PYEOF

# Pin the bundled MinIO images to the registry the platform already pulls from.
#
# The chart ships `minio/minio:latest` and `minio/mc:latest` from Docker Hub.
# Both fail on a real cluster: the tags are unauthenticated Docker Hub pulls
# subject to rate limits, and plane-minio-wl-0 sat in ImagePullBackOff for hours
# (541 attempts) while the platform's OWN MinIO -- the same software, pulled as
# quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z -- started first time.
#
# Same images, a registry that answers, and a pinned release rather than a
# floating tag, which is what the supply-chain policy wants anyway.
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
src = open(path).read()
subs = {
    r'(?<![\w/.-])minio/minio:latest\b': 'quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z',
    r'(?<![\w/.-])minio/mc:latest\b': 'quay.io/minio/mc:RELEASE.2024-11-21T17-21-54Z',
}
total = 0
for pattern, replacement in subs.items():
    src, n = re.subn(pattern, replacement, src)
    print(f"  {pattern} -> {replacement} ({n})")
    total += n
open(path, 'w').write(src)
print(f"pinned {total} MinIO image reference(s) to quay.io")
PYEOF

# Use the platform's OWN plane-backend build, not the upstream one.
#
# adhar-io/plane is the fork the platform's Plane changes live in, and its CI
# publishes ghcr.io/adhar-io/plane-backend. The tag is the upstream line plus a
# build number (1.4.2-1 for v1.4.2), so it cannot come from the chart's
# `planeVersion` value — that one tag is shared by frontend, space, admin and
# live, which are NOT republished under adhar-io and must keep pointing at
# artifacts.plane.so. Hence a rewrite here rather than a values override, the
# same reason the MinIO images above are rewritten.
#
# Verified public and multi-arch (linux/amd64 + linux/arm64) before pinning:
#   curl -H "Authorization: Bearer $(ghcr token)" \
#     https://ghcr.io/v2/adhar-io/plane-backend/manifests/1.4.2-1
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
src = open(path).read()
src, n = re.subn(r'artifacts\.plane\.so/makeplane/plane-backend:\S+',
                 'ghcr.io/adhar-io/plane-backend:1.4.2-1', src)
open(path, 'w').write(src)
print(f"pointed {n} plane-backend reference(s) at ghcr.io/adhar-io")
PYEOF

# Every workload that reads `plane-doc-store-secrets` also reads the ESO-managed
# `plane-s3-credentials`.
#
# The chart renders the doc-store Secret with the bucket, endpoint, region and
# USE_MINIO=0, but with NO credentials: `env.aws_access_key` and
# `env.aws_secret_access_key` are deliberately left empty in values.yaml so the
# object store's root credential never enters git (see manifests/platform-storage.yaml).
# ArgoCD owns the chart's Secret, so the credentials cannot be merged into it —
# they arrive as a second envFrom entry, added here.
#
# Pinned by TestPlaneUsesThePlatformObjectStore: a regeneration that loses this
# step leaves every Plane workload without S3 credentials, and the symptom is an
# upload that fails at runtime rather than a pod that fails to start.
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
src = open(path).read()

# Match the envFrom entry for the chart's doc-store Secret, at whatever
# indentation the template used, and append a sibling entry for ours.
pattern = re.compile(
    r'(?P<indent>[ ]*)- secretRef:\n'
    r'(?P<inner>[ ]*)name:[ ]+plane-doc-store-secrets\n'
    r'(?P<optindent>[ ]*)optional: false\n')


def add_credentials(m):
    return (m.group(0)
            + f"{m.group('indent')}- secretRef:\n"
            + f"{m.group('inner')}name: plane-s3-credentials\n"
            + f"{m.group('optindent')}optional: false\n")


src, n = pattern.subn(add_credentials, src)
# Every workload that mounts the doc-store Secret must have been matched. The
# chart's indentation is not uniform (one entry writes `name:` with two spaces),
# and a stricter pattern silently skipped that workload — which is the failure
# this check exists to catch, since the result is a pod that starts fine and
# cannot write to the object store.
mounts = len(re.findall(r'[ ]+name:[ ]+plane-doc-store-secrets\n[ ]+optional:', src))
if n == 0 or n != mounts:
    raise SystemExit(f"matched {n} of {mounts} plane-doc-store-secrets envFrom entries: the chart's "
                     "secret wiring changed, and some Plane workloads would run without S3 credentials")
open(path, 'w').write(src)
print(f"added the platform S3 credentials to {n} workload(s)")
PYEOF

# Raise the chart's one-second probe timeout.
#
# The chart ships `timeoutSeconds: 1` on the API readiness probe. On a loaded
# node that kills a healthy pod — the platform rule is that no package may ship a
# one-second probe, enforced by TestNoProbeShipsAOneSecondTimeout.
#
# This used to be a hand-edit of the generated file, which meant the FIRST
# regeneration silently undid it (2026-10-08). Doing it here is the only form
# that survives.
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
src = open(path).read()
src, n = re.subn(r'(?m)^([ ]*)timeoutSeconds: 1$', r'\g<1>timeoutSeconds: 5', src)
open(path, 'w').write(src)
print(f"raised {n} one-second probe timeout(s) to 5s")
PYEOF
