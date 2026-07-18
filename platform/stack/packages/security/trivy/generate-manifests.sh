#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.33.1"

echo "# TRIVY OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/trivy/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add aqua https://aquasecurity.github.io/helm-charts/ --force-update
helm repo update aqua
helm template --include-crds --namespace adhar-system trivy-operator aqua/trivy-operator -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
