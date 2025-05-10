#!/bin/bash

# Update ArgoCD manifest using Helm
HACK_DIR="$(dirname \"$0\")"
ARGOCD_VERSION=${1:-"3.0.0"} # Default to version 3.0.0 if not provided

# Ensure the hack directory exists
mkdir -p "$HACK_DIR"

# Use Helm to generate the ArgoCD manifest including CRDs
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update
helm template argocd argo/argo-cd --version "$ARGOCD_VERSION" --include-crds --output-dir "$HACK_DIR" > "$HACK_DIR/argocd.yaml"

if [ -f "$HACK_DIR/argocd.yaml" ]; then
    echo "ArgoCD manifest with CRDs generated successfully."
else
    echo "Failed to generate ArgoCD manifest with CRDs."
    exit 1
fi

echo "ArgoCD manifest updated to version $ARGOCD_VERSION."