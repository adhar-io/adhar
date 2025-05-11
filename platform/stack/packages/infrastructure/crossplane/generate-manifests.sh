#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v1.15.0"

echo "# CROSSPLANE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/infrastructure/crossplane/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add crossplane https://charts.crossplane.io/stable --force-update
helm repo update
helm template --namespace crossplane-system crossplane crossplane/crossplane -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}