#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
# grafana/loki chart (Loki 3.x). The old loki-stack chart (Loki 2.6.1) is
# deprecated and lacks limits_config.volume_enabled, which Grafana's logs
# volume panel requires.
CHART_VERSION="7.2.0"

echo "# LOKI INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/loki-stack/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
helm template --namespace adhar-system loki grafana/loki -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
