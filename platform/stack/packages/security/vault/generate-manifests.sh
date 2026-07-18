#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.32.0"

echo "# VAULT INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/vault/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add hashicorp https://helm.releases.hashicorp.com --force-update
helm repo update hashicorp
helm template --include-crds --namespace adhar-system vault hashicorp/vault -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
