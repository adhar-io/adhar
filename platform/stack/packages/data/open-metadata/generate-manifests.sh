#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.12.10"

echo "# OPEN-METADATA INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/open-metadata/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add open-metadata https://helm.open-metadata.org --force-update
helm repo update open-metadata
helm template --include-crds --namespace open-metadata openmetadata open-metadata/openmetadata -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
