#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v1.11.0"

echo "# TEMPO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/tempo/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
helm template --namespace monitoring tempo grafana/tempo -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}