#!/bin/bash
set -e

# OpenFunction control plane (FaaS framework). No web UI of its own, so no
# HTTPRoute -- function ingress is created per-function. Installs into the
# "openfunction" namespace.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.7.0"

echo "# OPEN-FUNCTION INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/open-function/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add openfunction https://openfunction.github.io/charts/ --force-update
helm repo update openfunction
helm template --namespace adhar-system openfunction openfunction/openfunction -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
