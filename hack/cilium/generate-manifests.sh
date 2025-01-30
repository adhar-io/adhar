#!/bin/bash
set -e

INSTALL_YAML="pkg/controllers/localbuild/resources/cilium/k8s/install.yaml"
CILIUM_DIR="./hack/cilium"
CHART_VERSION="v1.16.4"

echo "# CILIUM INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'hack/cilium/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add cilium --force-update https://helm.cilium.io/
helm repo update
helm template cilium cilium/cilium -f ${CILIUM_DIR}/values.yaml --version ${CHART_VERSION} --namespace kube-system --set hubble.relay.enabled=true --set hubble.ui.enabled=true --include-crds >> ${INSTALL_YAML}
