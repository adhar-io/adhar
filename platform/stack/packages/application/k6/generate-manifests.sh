#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="4.6.0"

echo "# K6 INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/k6/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
# Chart >= 4.5 renders a kind:Namespace for the release namespace by default;
# packages must not ship Namespace objects (ArgoCD owns adhar-system).
helm template --namespace adhar-system k6 grafana/k6-operator -f values.yaml --version ${CHART_VERSION} --set namespace.create=false >>${INSTALL_YAML}