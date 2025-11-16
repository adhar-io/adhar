#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v0.18.3"

echo "# REDIS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/redis/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add ot-container-kit https://ot-container-kit.github.io/helm-charts --force-update
helm repo update ot-container-kit
helm template --namespace redis-operator redis ot-container-kit/redis-operator -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}