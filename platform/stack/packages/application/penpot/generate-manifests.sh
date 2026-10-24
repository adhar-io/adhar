#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.9.0"

echo "# PENPOT INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/penpot/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add penpot https://helm.penpot.app --force-update
helm repo update
helm template --namespace adhar-system penpot penpot/penpot -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# Edits the chart cannot express (the backend waits for Keycloak). Keep last.
python3 post-render.py "${INSTALL_YAML}"

# NOTE: manifests/dependencies.yaml (in-namespace Postgres + Valkey) and
# manifests/httproute.yaml.tmpl are static and intentionally NOT regenerated here.
