#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.3.1"

echo "# OPEN CLUSTER MANAGEMENT (cluster-manager) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/open-cluster-management/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add ocm https://open-cluster-management.io/helm-charts --force-update
helm repo update ocm
helm template --namespace adhar-system cluster-manager ocm/cluster-manager -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
