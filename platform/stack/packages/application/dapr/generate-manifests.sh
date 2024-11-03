#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v1.13.5"

echo "# DAPR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/harbor/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add dapr https://dapr.github.io/helm-charts/ --force-update
helm repo update
helm template --namespace dapr-system dapr dapr/dapr -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >>${INSTALL_YAML}