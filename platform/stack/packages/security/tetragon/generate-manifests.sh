#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.7.0"

echo "# TETRAGON INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/tetragon/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add cilium https://helm.cilium.io/ --force-update
helm repo update cilium
helm template --include-crds --namespace adhar-system tetragon cilium/tetragon -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
