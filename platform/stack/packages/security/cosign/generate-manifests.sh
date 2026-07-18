#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.10.6"

echo "# COSIGN (SIGSTORE POLICY-CONTROLLER) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/cosign/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add sigstore https://sigstore.github.io/helm-charts --force-update
helm repo update sigstore
helm template --include-crds --namespace adhar-system policy-controller sigstore/policy-controller -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
