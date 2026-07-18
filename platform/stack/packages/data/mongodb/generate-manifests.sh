#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="19.1.4"

echo "# MONGODB INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/mongodb/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add bitnami https://charts.bitnami.com/bitnami --force-update
helm repo update bitnami
helm template --include-crds --namespace adhar-system mongodb bitnami/mongodb -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
