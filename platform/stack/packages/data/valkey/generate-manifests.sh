#!/bin/bash
set -e

# Hyperspike valkey-operator — a CRD-based operator (kind: Valkey) that manages
# Valkey cache/cluster instances. Distributed as a single release install bundle
# (the project's helm-charts repo currently ships an expired TLS cert), so we pin
# a release and vendor its install.yaml. Bump VERSION and re-run to update.
VERSION="v0.0.61"
INSTALL_YAML="manifests/install.yaml"

echo "# VALKEY OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/valkey/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Source: hyperspike/valkey-operator ${VERSION} release install bundle" >>${INSTALL_YAML}
curl -sSL "https://github.com/hyperspike/valkey-operator/releases/download/${VERSION}/install.yaml" >>${INSTALL_YAML}
