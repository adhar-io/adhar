#!/bin/bash

# Update Cilium manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
CILIUM_VERSION="1.13.0"
CILIUM_NAMESPACE="adhar-system"

# Use Helm to generate the Cilium manifest including CRDs
helm repo add cilium https://helm.cilium.io/
helm repo update cilium
helm template cilium cilium/cilium --namespace $CILIUM_NAMESPACE --version "$CILIUM_VERSION" --include-crds -f "$HACK_DIR/values.yaml" --output-dir "$HACK_DIR" > "$HACK_DIR/install.yaml"

if [ -f "$HACK_DIR/install.yaml" ]; then
    echo "Cilium manifest with CRDs generated successfully."
else
    echo "Failed to generate Cilium manifest with CRDs."
    exit 1
fi

echo "Cilium manifest updated to version $CILIUM_VERSION."