#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.39.0"

echo "# VICTORIA-METRICS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/victoria-metrics/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add victoria-metrics https://victoriametrics.github.io/helm-charts/ --force-update
helm repo update victoria-metrics
helm template --namespace adhar-system victoria-metrics victoria-metrics/victoria-metrics-single -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
