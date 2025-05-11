#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v0.15.1"

echo "# TERRAFORM INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/infrastructure/terraform/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add terraform https://flux-iac.github.io/tofu-controller --force-update
helm repo update
helm template --namespace adhar-system tf-controller terraform/tf-controller -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}