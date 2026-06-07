#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.5.1"

echo "# PLANE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/plane/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add plane https://helm.plane.so --force-update
helm repo update
helm template --namespace plane plane plane/plane-ce -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
