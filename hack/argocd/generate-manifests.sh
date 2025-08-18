#!/bin/bash

# Update ArgoCD manifest using Helm
MANIFEST_DIR="platform/controllers/adharplatform/resources/argocd" # Changed 'argocd' to 'argo-cd'
INSTALL_YAML="$MANIFEST_DIR/install.yaml"
INSTALL_HA_YAML="$MANIFEST_DIR/install-ha.yaml"
HACK_DIR="$(cd "$(dirname "$0")" && pwd)" # Ensure HACK_DIR is an absolute path
ARGOCD_VERSION="v8.3.0"
ARGOCD_NAMESPACE="adhar-system"

mkdir -p "$MANIFEST_DIR" # Ensure directory exists

# Use Helm to generate the ArgoCD manifest including CRDs
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update argo
helm template argo-cd argo/argo-cd --namespace $ARGOCD_NAMESPACE --version "$ARGOCD_VERSION" -f "$HACK_DIR/values.yaml" --include-crds > "$INSTALL_YAML"
helm template argo-cd argo/argo-cd --namespace $ARGOCD_NAMESPACE --version "$ARGOCD_VERSION" -f "$HACK_DIR/values-ha.yaml" --include-crds > "$INSTALL_HA_YAML"

if [ -f "$INSTALL_YAML" ]; then
    echo "ArgoCD manifest with CRDs generated successfully."
else
    echo "Failed to generate ArgoCD manifest with CRDs."
    exit 1
fi

echo "ArgoCD manifest updated to version $ARGOCD_VERSION in namespace $ARGOCD_NAMESPACE."