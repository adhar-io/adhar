#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.42.2"

echo "# TRINO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/trino/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add trino https://trinodb.github.io/charts --force-update
helm repo update trino
helm template --include-crds --namespace trino trino trino/trino -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
