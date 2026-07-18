#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.0.0"

echo "# KAMAJI INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/Kamaji/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add clastix https://clastix.github.io/charts --force-update
helm repo update clastix
helm template --namespace adhar-system kamaji clastix/kamaji -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
