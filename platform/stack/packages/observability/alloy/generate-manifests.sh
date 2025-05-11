#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v0.9.2"

echo "# ALLOY INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/alloy/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
helm template --namespace monitoring alloy grafana/alloy -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}