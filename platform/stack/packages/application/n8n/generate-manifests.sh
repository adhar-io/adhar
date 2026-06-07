#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.0.1"

echo "# N8N INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/n8n/generate-manifests.sh'" >>${INSTALL_YAML}

# Community chart maintained by 8gears, published as an OCI artifact.
helm template --namespace n8n n8n oci://8gears.container-registry.com/library/n8n -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
