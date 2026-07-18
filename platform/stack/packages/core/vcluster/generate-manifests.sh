#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.34.1"

echo "# VCLUSTER INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/vcluster/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add loft https://charts.loft.sh --force-update
helm repo update loft
helm template --namespace adhar-system vcluster loft/vcluster -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
