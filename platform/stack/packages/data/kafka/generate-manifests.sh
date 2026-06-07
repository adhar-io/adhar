#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="32.4.3"

echo "# KAFKA INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/kafka/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add bitnami https://charts.bitnami.com/bitnami --force-update
helm repo update bitnami
helm template --include-crds --namespace kafka kafka bitnami/kafka -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
