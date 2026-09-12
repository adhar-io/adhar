#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
# Chart 0.29.4 ships OpenBao v2.6.2 (appVersion). The chart requires
# kubeVersion >= 1.30, which helm template cannot infer offline, hence
# --kube-version below.
CHART_VERSION="0.29.4"

echo "# OPENBAO INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/openbao/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add openbao https://openbao.github.io/openbao-helm --force-update
helm repo update openbao
helm template --include-crds --kube-version 1.32.0 --namespace adhar-system openbao openbao/openbao -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
