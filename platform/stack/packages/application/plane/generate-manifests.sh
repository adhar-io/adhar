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
