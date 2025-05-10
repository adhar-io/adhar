#!/bin/bash

# Update Cilium manifest using Helm
HACK_DIR="$(dirname \"$0\")"
CILIUM_VERSION=${1:-"1.13.0"} # Default to version 1.13.0 if not provided

# Ensure the hack directory exists
mkdir -p "$HACK_DIR"

# Use Helm to generate the Cilium manifest including CRDs
helm repo add cilium https://helm.cilium.io/
helm repo update
helm template cilium cilium/cilium --version "$CILIUM_VERSION" --include-crds --output-dir "$HACK_DIR" > "$HACK_DIR/cilium.yaml"

if [ -f "$HACK_DIR/cilium.yaml" ]; then
    echo "Cilium manifest with CRDs generated successfully."
else
    echo "Failed to generate Cilium manifest with CRDs."
    exit 1
fi

echo "Cilium manifest updated to version $CILIUM_VERSION."