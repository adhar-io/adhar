#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.3.0"

echo "# MYSQL-OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/mysql-operator/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add mysql-operator https://mysql.github.io/mysql-operator/ --force-update
helm repo update mysql-operator
helm template --namespace adhar-system mysql-operator mysql-operator/mysql-operator -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
