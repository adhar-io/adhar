#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v2.10.2"

echo "# LOKI-STACK INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/loki-stack/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
helm template --namespace monitoring loki grafana/loki-stack --set fluent-bit.enabled=true,promtail.enabled=false -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}