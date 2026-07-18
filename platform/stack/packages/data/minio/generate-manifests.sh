#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="5.4.0"

echo "# MINIO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/minio/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add minio https://charts.min.io --force-update
helm repo update minio
helm template --namespace adhar-system minio minio/minio -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}