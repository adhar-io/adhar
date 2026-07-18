#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.8.2"

echo "# FARO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/faro/generate-manifests.sh'" >>${INSTALL_YAML}

# Grafana Faro has no standalone Helm chart - it is a frontend (browser) RUM SDK
# whose telemetry is received by Grafana Alloy's faro.receiver component. This
# package therefore deploys a dedicated Alloy instance configured purely as a
# Faro collector (faro.receiver -> Loki/Tempo), exposed on port 12347.

helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update grafana
helm template --namespace adhar-system faro grafana/alloy -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
