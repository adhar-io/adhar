#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.28.2"

echo "# CLOUDNATIVE-PG INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/cnpg/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add cnpg https://cloudnative-pg.github.io/charts --force-update
helm repo update cnpg
helm template --namespace cnpg-system cnpg cnpg/cloudnative-pg -f values.yaml --version ${CHART_VERSION} --kube-version 1.31.0 --include-crds >>${INSTALL_YAML}