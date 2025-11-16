#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v72.6.2"

echo "# KUBE-PROMETHEUS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/kube-prometheus/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
helm repo update prometheus-community
helm template --include-crds --namespace adhar-system prometheus prometheus-community/kube-prometheus-stack -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >> ${INSTALL_YAML}