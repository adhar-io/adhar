#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="3.9.0"

echo "# KYVERNO-POLICIES INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/kyverno-policies/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add kyverno https://kyverno.github.io/kyverno/ --force-update
helm repo update kyverno
# Chart >= 3.9.0 defaults policyType to ValidatingPolicy (CEL, policies.kyverno.io).
# Keep ClusterPolicy: the PolicyExceptions in manifests/exceptions/ and
# manifests/supply-chain.yaml are kyverno.io/v2beta1 objects that only bind to
# ClusterPolicy rules; switching engines requires migrating those first
# (https://kyverno.io/docs/guides/migration-to-cel/).
helm template --namespace adhar-system kyverno-policies kyverno/kyverno-policies -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true --set policyType=ClusterPolicy >>${INSTALL_YAML}