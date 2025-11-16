#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v5.2.2"

echo "# VELERO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/velero/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add velero https://vmware-tanzu.github.io/helm-charts --force-update
helm repo update velero
helm template --namespace velero velero velero/velero -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
