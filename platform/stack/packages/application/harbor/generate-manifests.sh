#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v1.15.1"

echo "# HARBOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/harbor/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add harbor https://helm.goharbor.io --force-update
helm repo update
helm template --namespace harbor harbor harbor/harbor -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >>${INSTALL_YAML}