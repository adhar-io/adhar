#!/bin/bash

# Update ArgoCD manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)" # Ensure HACK_DIR is an absolute path
ARGOCD_VERSION="v8.0.3"

# Use Helm to generate the ArgoCD manifest including CRDs
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update argo
helm template argo-cd argo/argo-cd --version "$ARGOCD_VERSION" -f "$HACK_DIR/values.yaml" --include-crds > "$HACK_DIR/install.yaml"

if [ -f "$HACK_DIR/install.yaml" ]; then
    echo "ArgoCD manifest with CRDs generated successfully."
else
    echo "Failed to generate ArgoCD manifest with CRDs."
    exit 1
fi

echo "ArgoCD manifest updated to version $ARGOCD_VERSION."