#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.17.9"

echo "# DAPR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/dapr/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add dapr https://dapr.github.io/helm-charts/ --force-update
helm repo update
helm template --namespace adhar-system dapr dapr/dapr -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >>${INSTALL_YAML}