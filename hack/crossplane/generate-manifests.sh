#!/bin/bash

# Update Crossplane manifest using Helm
INSTALL_YAML="platform/controllers/adharplatform/resources/crossplane/install.yaml"
INSTALL_HA_YAML="platform/controllers/adharplatform/resources/crossplane/install-ha.yaml"
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
CROSSPLANE_VERSION="v2.1.0"
CROSSPLANE_NAMESPACE="adhar-system"

# Use Helm to generate the Crossplane manifest including CRDs
helm repo add crossplane https://charts.crossplane.io/stable
helm repo update crossplane
helm template crossplane --namespace $CROSSPLANE_NAMESPACE crossplane/crossplane --version "$CROSSPLANE_VERSION" -f "$HACK_DIR/values.yaml" --include-crds > "$INSTALL_YAML"
helm template crossplane --namespace $CROSSPLANE_NAMESPACE crossplane/crossplane --version "$CROSSPLANE_VERSION" -f "$HACK_DIR/values-ha.yaml" --include-crds > "$INSTALL_HA_YAML"

if [ -f "$INSTALL_YAML" ]; then
    echo "Crossplane manifest with CRDs generated successfully."
else
    echo "Failed to generate Crossplane manifest with CRDs."
    exit 1
fi

echo "Crossplane manifest updated to version $CROSSPLANE_VERSION in namespace $CROSSPLANE_NAMESPACE."