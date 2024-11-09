#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v2.15.1"

echo "# KEDA INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/keda/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add kedacore https://kedacore.github.io/charts --force-update
helm repo update
helm template --namespace keda keda kedacore/keda -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >>${INSTALL_YAML}