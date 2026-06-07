#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.2.0"

echo "# PENPOT INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/penpot/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add penpot https://helm.penpot.app --force-update
helm repo update
helm template --namespace penpot penpot penpot/penpot -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# NOTE: manifests/dependencies.yaml (in-namespace Postgres + Valkey) and
# manifests/httproute.yaml are static and intentionally NOT regenerated here.
