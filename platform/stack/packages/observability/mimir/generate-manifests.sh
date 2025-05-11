#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v5.6.0-weekly.314"

echo "# MIMIR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/mimir/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
helm template --namespace monitoring mimir grafana/mimir-distributed -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}