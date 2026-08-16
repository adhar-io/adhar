#!/bin/bash

# Update Git provider manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)" # Ensure HACK_DIR is an absolute path
# Anchored to the script location so output lands in the same place regardless
# of the directory the script is invoked from.
INSTALL_YAML="$HACK_DIR/../../platform/controllers/adharplatform/resources/gitea/install.yaml"
INSTALL_HA_YAML="$HACK_DIR/../../platform/controllers/adharplatform/resources/gitea/install-ha.yaml"
GITEA_VERSION=${1:-"12.7.0"} # gitea chart 12.7.0 packages Gitea app v1.27.0

echo "Using Gitea as the Git provider."
helm repo add gitea-charts https://dl.gitea.com/charts/
helm repo update gitea-charts
helm template gitea gitea-charts/gitea --namespace adhar-system --version "$GITEA_VERSION" -f "$HACK_DIR/values.yaml" > "$INSTALL_YAML"
helm template gitea gitea-charts/gitea --namespace adhar-system --version "$GITEA_VERSION" -f "$HACK_DIR/values-ha.yaml" > "$INSTALL_HA_YAML"

if [ -f "$INSTALL_YAML" ]; then
    echo "Gitea manifest with CRDs generated successfully."
else
    echo "Failed to generate Gitea manifest with CRDs."
    exit 1
fi

echo "Manifest updated for Gitea to version $GITEA_VERSION."