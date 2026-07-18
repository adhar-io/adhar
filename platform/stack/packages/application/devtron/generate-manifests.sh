#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.23.2"

echo "# DEVTRON INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/devtron/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add devtron https://helm.devtron.ai --force-update
helm repo update
helm template --namespace adhar-system devtron devtron/devtron-operator -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
