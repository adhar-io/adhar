#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.0.3"

echo "# KARGO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/kargo/generate-manifests.sh'" >>${INSTALL_YAML}

helm template --namespace kargo kargo oci://ghcr.io/akuity/kargo-charts/kargo -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >>${INSTALL_YAML}