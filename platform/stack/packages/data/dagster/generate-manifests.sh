#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.13.8"

echo "# DAGSTER INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/dagster/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add dagster https://dagster-io.github.io/helm --force-update
helm repo update dagster
helm template --include-crds --namespace dagster dagster dagster/dagster -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
