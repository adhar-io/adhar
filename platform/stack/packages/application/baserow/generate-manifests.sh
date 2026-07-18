#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="7.5.0"

echo "# BASEROW INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/baserow/generate-manifests.sh'" >>${INSTALL_YAML}

# Community chart maintained by christianhuth. Bundles Bitnami PostgreSQL + Redis.
helm repo add christianhuth https://charts.christianhuth.de --force-update
helm repo update
helm template --namespace adhar-system baserow christianhuth/baserow -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
