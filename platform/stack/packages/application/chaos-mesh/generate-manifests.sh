#!/bin/bash
set -e

# Chaos Mesh control plane + dashboard. The dashboard UI is exposed via the
# Cilium Gateway (manifests/httproute.yaml); the chart Ingress stays disabled.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.8.2"

echo "# CHAOS MESH INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/chaos-mesh/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add chaos-mesh https://charts.chaos-mesh.org --force-update
helm repo update chaos-mesh
helm template --namespace adhar-system chaos-mesh chaos-mesh/chaos-mesh -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
