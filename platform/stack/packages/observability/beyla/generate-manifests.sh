#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.16.7"

echo "# BEYLA INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/beyla/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update grafana
helm template --namespace adhar-system beyla grafana/beyla -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
