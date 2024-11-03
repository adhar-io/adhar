#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v65.5.1"

echo "# KUBE-PROMETHEUS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/kube-prometheus/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
helm repo update
helm template --namespace monitoring prometheus prometheus-community/kube-prometheus-stack -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}