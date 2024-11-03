#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v0.25.0"

echo "# HEADLAMP INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/headlamp/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add headlamp https://headlamp-k8s.github.io/headlamp/ --force-update
helm repo update
helm template --namespace adhar-system headlamp headlamp/headlamp -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}