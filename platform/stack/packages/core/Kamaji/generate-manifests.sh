#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.0.0"

echo "# KAMAJI INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/Kamaji/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add clastix https://clastix.github.io/charts --force-update
helm repo update clastix
helm template --namespace adhar-system kamaji clastix/kamaji -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

# The chart's CRDs (not templated by helm) hardcode the upstream namespace in
# the cert-manager CA-injection annotation and the conversion webhook service
# reference; rewrite them to the install namespace or cainjector never finds
# kamaji-serving-cert and every Kamaji webhook call fails.
sed -i.bak -e 's#cert-manager.io/inject-ca-from: kamaji-system/#cert-manager.io/inject-ca-from: adhar-system/#' \
  -e 's#^\(\s*\)namespace: kamaji-system$#\1namespace: adhar-system#' "${INSTALL_YAML}" && rm -f "${INSTALL_YAML}.bak"
