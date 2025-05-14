#!/bin/bash

# Update Crossplane manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)" # Ensure HACK_DIR is an absolute path
CROSSPLANE_VERSION=${1:-"1.12.0"} # Default to version 1.12.0 if not provided

# Ensure the hack directory exists
mkdir -p "$HACK_DIR"

# Use Helm to generate the Crossplane manifest including CRDs
helm repo add crossplane-stable https://charts.crossplane.io/stable
helm repo update crossplane-stable
helm template crossplane --namespace crossplane-system crossplane-stable/crossplane --version "$CROSSPLANE_VERSION" -f "$HACK_DIR/values.yaml" --output-dir "$HACK_DIR" > "$HACK_DIR/crossplane.yaml"

if [ -f "$HACK_DIR/crossplane.yaml" ]; then
    echo "Crossplane manifest with CRDs generated successfully."
else
    echo "Failed to generate Crossplane manifest with CRDs."
    exit 1
fi

echo "Crossplane manifest updated to version $CROSSPLANE_VERSION."