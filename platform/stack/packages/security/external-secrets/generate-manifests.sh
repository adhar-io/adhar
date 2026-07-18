#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.5.0"

echo "# EXTERNAL SECRETS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/external-secrets/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add external-secrets --force-update https://charts.external-secrets.io
helm repo update external-secrets
helm template --namespace adhar-system external-secrets external-secrets/external-secrets -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}