#!/bin/bash
set -e

# Argo Rollouts controller + dashboard. The dashboard UI is exposed via the
# Cilium Gateway (manifests/httproute.yaml); the chart Ingress stays disabled.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.41.0"

echo "# ARGO ROLLOUTS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/argo-rollout/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add argo https://argoproj.github.io/argo-helm --force-update
helm repo update argo
helm template --namespace adhar-system argo-rollouts argo/argo-rollouts -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
