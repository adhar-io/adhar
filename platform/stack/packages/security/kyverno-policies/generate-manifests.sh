#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v3.3.0"

echo "# KYVERNO-POLICIES INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/kyverno-policies/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add kyverno https://kyverno.github.io/kyverno/ --force-update
helm repo update
helm template --namespace kyverno kyverno-policies kyverno/kyverno-policies -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >>${INSTALL_YAML}