#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.12.2"

echo "# LAKEFS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/lakefs/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add lakefs https://charts.lakefs.io --force-update
helm repo update lakefs
helm template --include-crds --namespace lakefs lakefs lakefs/lakefs -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
