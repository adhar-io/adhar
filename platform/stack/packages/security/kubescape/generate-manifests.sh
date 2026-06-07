#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.40.2"

echo "# KUBESCAPE OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/kubescape/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add kubescape https://kubescape.github.io/helm-charts/ --force-update
helm repo update kubescape
helm template --include-crds --namespace kubescape kubescape kubescape/kubescape-operator -f values.yaml --version ${CHART_VERSION} \
  --set clusterName=adhar \
  --set capabilities.continuousScan=enable >>${INSTALL_YAML}
