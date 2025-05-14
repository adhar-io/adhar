#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v3.3.7"

echo "# JUPYTERHUB INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/jupyterhub/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add jupyterhub https://jupyterhub.github.io/helm-chart/ --force-update
helm repo update jupyterhub
helm template --include-crds --namespace jupyterhub jupyterhub jupyterhub/jupyterhub -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}