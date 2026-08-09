#!/bin/bash

# Update Crossplane manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
# Anchored to the script location so output lands in the same place regardless
# of the directory the script is invoked from.
INSTALL_YAML="$HACK_DIR/../../platform/controllers/adharplatform/resources/crossplane/install.yaml"
INSTALL_HA_YAML="$HACK_DIR/../../platform/controllers/adharplatform/resources/crossplane/install-ha.yaml"
CROSSPLANE_VERSION="v2.3.1"
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