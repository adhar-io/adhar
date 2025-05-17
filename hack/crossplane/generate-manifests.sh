#!/bin/bash

# Update Crossplane manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
CROSSPLANE_VERSION="v2.0.0-preview.1"
CROSSPLANE_NAMESPACE="adhar-system"

# Use Helm to generate the Crossplane manifest including CRDs
helm repo add crossplane-preview https://charts.crossplane.io/preview
helm repo update crossplane-preview
helm template crossplane --namespace $CROSSPLANE_NAMESPACE crossplane-preview/crossplane --version "$CROSSPLANE_VERSION" -f "$HACK_DIR/values.yaml" --include-crds > "$HACK_DIR/install.yaml"

if [ -f "$HACK_DIR/install.yaml" ]; then
    echo "Crossplane manifest with CRDs generated successfully."
else
    echo "Failed to generate Crossplane manifest with CRDs."
    exit 1
fi

echo "Crossplane manifest updated to version $CROSSPLANE_VERSION in namespace $CROSSPLANE_NAMESPACE."