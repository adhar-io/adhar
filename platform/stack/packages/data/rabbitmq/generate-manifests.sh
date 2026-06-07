#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="16.0.14"

echo "# RABBITMQ INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/rabbitmq/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add bitnami https://charts.bitnami.com/bitnami --force-update
helm repo update bitnami
helm template --include-crds --namespace rabbitmq rabbitmq bitnami/rabbitmq -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
