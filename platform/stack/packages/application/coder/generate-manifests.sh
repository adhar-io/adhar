#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.34.0"

echo "# CODER INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/coder/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add coder-v2 https://helm.coder.com/v2 --force-update
helm repo update
helm template --namespace coder coder coder-v2/coder -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
