#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.5.0"

echo "# SPARK-OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/spark-operator/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add spark-operator https://kubeflow.github.io/spark-operator --force-update
helm repo update
helm template --include-crds --namespace adhar-system spark-operator spark-operator/spark-operator -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true  >>${INSTALL_YAML}