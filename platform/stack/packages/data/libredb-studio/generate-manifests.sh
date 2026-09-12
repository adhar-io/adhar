#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
# Chart 0.1.63 -> app 0.15.0 (ghcr.io/libredb/libredb-studio:0.15.0). The chart
# is published ONLY as an OCI artifact (oci://ghcr.io/libredb/charts/...);
# there is no https Helm repository to `helm repo add`.
CHART_VERSION="0.1.63"

echo "# LIBREDB STUDIO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/libredb-studio/generate-manifests.sh'" >>${INSTALL_YAML}

helm template --namespace adhar-system libredb-studio \
  oci://ghcr.io/libredb/charts/libredb-studio \
  --version ${CHART_VERSION} -f values.yaml >>${INSTALL_YAML}

# NOTE: manifests/platform-postgres.yaml (CNPG Cluster + credentials),
# manifests/datasources.yaml (ExternalSecrets mirroring every platform
# database credential), manifests/keycloak-client.yaml (OIDC client + client
# secret) and manifests/httproute.yaml are static and intentionally NOT
# regenerated here.
