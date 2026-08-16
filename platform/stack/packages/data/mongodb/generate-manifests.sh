#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.13.0"

echo "# MONGODB COMMUNITY OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/mongodb/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add mongodb https://mongodb.github.io/helm-charts --force-update
helm repo update mongodb
helm template --namespace adhar-system mongodb mongodb/community-operator -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
