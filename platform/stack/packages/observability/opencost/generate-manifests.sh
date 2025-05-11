#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v1.38.1"

echo "# OPENCOST INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/opencost/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add opencost https://opencost.github.io/opencost-helm-chart --force-update
helm repo update
helm template --namespace monitoring opencost opencost/opencost -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}