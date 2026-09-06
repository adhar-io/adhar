#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.40.4"

echo "# KUBESCAPE OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/kubescape/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add kubescape https://kubescape.github.io/helm-charts/ --force-update
helm repo update kubescape
# ksNamespace must match the install namespace: the chart derives the storage
# APIService server certificate SANs (storage.<ksNamespace>.svc) from it, and
# with the default ("kubescape") the aggregated API fails TLS verification,
# which breaks discovery for every client (kubectl, ArgoCD) in the cluster.
helm template --include-crds --namespace adhar-system kubescape kubescape/kubescape-operator -f values.yaml --version ${CHART_VERSION} \
  --set clusterName=adhar \
  --set ksNamespace=adhar-system \
  --set capabilities.continuousScan=enable >>${INSTALL_YAML}
