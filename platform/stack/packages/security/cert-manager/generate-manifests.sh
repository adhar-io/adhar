#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v1.15.3"

echo "# CERT MANAGER INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/cert-manager/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add jetstack https://charts.jetstack.io --force-update
helm repo update
helm template --namespace cert-manager cert-manager jetstack/cert-manager -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >>${INSTALL_YAML}