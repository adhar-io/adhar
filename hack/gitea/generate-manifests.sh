#!/bin/bash

# Update Git provider manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)" # Ensure HACK_DIR is an absolute path
GITEA_VERSION=${1:-"11.0.1"} # Default to version 11.0.1 if not provided

echo "Using Gitea as the Git provider."
helm repo add gitea-charts https://dl.gitea.com/charts/
helm repo update gitea-charts
helm template gitea gitea-charts/gitea --version "$GITEA_VERSION" -f "$HACK_DIR/values.yaml" > "$HACK_DIR/install.yaml"

if [ -f "$HACK_DIR/install.yaml" ]; then
    echo "Gitea manifest with CRDs generated successfully."
else
    echo "Failed to generate Gitea manifest with CRDs."
    exit 1
fi

echo "Manifest updated for Gitea to version $GITEA_VERSION."