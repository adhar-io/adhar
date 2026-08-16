#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.58.0"

echo "# FLUENT-BIT INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/fluent-bit/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add fluent https://fluent.github.io/helm-charts --force-update
helm repo update fluent
# --api-versions monitoring.coreos.com/v1 so the ServiceMonitor (capability-gated
# in the chart) renders into the static manifest that ArgoCD applies; the CRD is
# provided by the kube-prometheus package at deploy time.
helm template --namespace adhar-system fluent-bit fluent/fluent-bit -f values.yaml --version ${CHART_VERSION} --include-crds --api-versions monitoring.coreos.com/v1 >>${INSTALL_YAML}
