#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="9.0.0"

echo "# FALCO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/falco/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add falcosecurity https://falcosecurity.github.io/charts --force-update
helm repo update falcosecurity
helm template --include-crds --namespace adhar-system falco falcosecurity/falco -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
