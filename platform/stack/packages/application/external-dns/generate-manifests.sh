#!/bin/bash
set -e

# ExternalDNS controller. No web UI, so no HTTPRoute. The DNS provider is left at
# the chart default ("fake") in values.yaml so the controller installs cleanly on
# a local cluster; set a real provider + credentials per environment.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.21.1"

echo "# EXTERNAL-DNS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/external-dns/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/ --force-update
helm repo update external-dns
helm template --namespace adhar-system external-dns external-dns/external-dns -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
