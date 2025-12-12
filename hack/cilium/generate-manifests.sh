#!/bin/bash

# Update Cilium manifest using Helm
# This script generates Cilium manifests including Gateway API CRDs
# Gateway API is enabled by default and replaces NGINX Ingress
INSTALL_YAML="platform/controllers/adharplatform/resources/cilium/install.yaml"
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
CILIUM_VERSION="v1.18.4"
CILIUM_NAMESPACE="adhar-system"

# Use Helm to generate the Cilium manifest including CRDs (Gateway API CRDs included)
helm repo add cilium https://helm.cilium.io/
helm repo update cilium
helm template cilium cilium/cilium --namespace $CILIUM_NAMESPACE --version "$CILIUM_VERSION" --include-crds -f "$HACK_DIR/values.yaml" > "$INSTALL_YAML"

if [ -f "$INSTALL_YAML" ]; then
    echo "Cilium manifest with CRDs generated successfully."
else
    echo "Failed to generate Cilium manifest with CRDs."
    exit 1
fi

echo "Cilium manifest updated to version $CILIUM_VERSION."