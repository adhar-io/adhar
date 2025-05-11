#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v5.0.15"

echo "# MINIO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/minio/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add minio https://charts.min.io --force-update
helm repo update
helm template --namespace minio minio minio/minio -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}