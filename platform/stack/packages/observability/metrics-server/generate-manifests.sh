#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v3.12.2"

echo "# METRICS_SERVER INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/metrics-server/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server --force-update
helm repo update
helm template --namespace kube-system metrics-server metrics-server/metrics-server -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}