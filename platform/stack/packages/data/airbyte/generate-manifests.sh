#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.9.2"

echo "# AIRBYTE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/airbyte/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add airbyte https://airbytehq.github.io/helm-charts --force-update
helm repo update airbyte
helm template --include-crds --namespace adhar-system airbyte airbyte/airbyte -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
