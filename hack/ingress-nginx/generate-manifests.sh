#!/bin/bash

# Update NGINX Ingress Controller manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)" # Ensure HACK_DIR is an absolute path
# It's good practice to use a specific version. Check for the latest stable version appropriate for your needs.
NGINX_VERSION="v4.12.2"

# Use Helm to generate the NGINX Ingress Controller manifest
# The official chart is typically from 'ingress-nginx'
HELM_REPO_NAME="ingress-nginx"
HELM_REPO_URL="https://kubernetes.github.io/ingress-nginx"
HELM_CHART_NAME="ingress-nginx/ingress-nginx"

helm repo add "$HELM_REPO_NAME" "$HELM_REPO_URL"
helm repo update "$HELM_REPO_NAME"

echo "Generating NGINX Ingress manifest version $NGINX_VERSION..."
helm template ingress-nginx "$HELM_CHART_NAME" \
  --version "$NGINX_VERSION" \
  --include-crds \
  -f "$HACK_DIR/values.yaml" > "$HACK_DIR/install.yaml"

if [ -f "$HACK_DIR/install.yaml" ]; then # Check if the file exists
    echo "NGINX Ingress manifest with CRDs generated successfully."
    # Remove the default template output file if helm template creates one (e.g. ingress-nginx/templates/...)
    rm -rf "$HACK_DIR/ingress-nginx"
else
    echo "Failed to generate NGINX Ingress manifest with CRDs."
    # Clean up empty install.yaml if it was created
    if [ -f "$HACK_DIR/install.yaml" ]; then
        rm "$HACK_DIR/install.yaml"
    fi
    exit 1
fi

echo "NGINX Ingress manifest updated to version $NGINX_VERSION."
