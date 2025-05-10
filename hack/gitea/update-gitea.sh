#!/bin/bash

# Update Git provider manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)" # Ensure HACK_DIR is an absolute path
GITEA_VERSION=${1:-"11.0.1"} # Default to version 11.0.1 if not provided

# Check the selected Git provider from the configuration
GIT_PROVIDER=${2:-"gitea"} # Default to gitea if not provided

# Ensure the hack directory exists
mkdir -p "$HACK_DIR"

if [ "$GIT_PROVIDER" == "gitlab" ]; then
    echo "Using GitLab as the Git provider."
    helm repo add gitlab https://charts.gitlab.io/
    helm repo update
    helm template gitlab gitlab/gitlab --version "$GITEA_VERSION" --include-crds --output-dir "$HACK_DIR" > "$HACK_DIR/gitlab.yaml"

    if [ -f "$HACK_DIR/gitlab.yaml" ]; then
        echo "GitLab manifest with CRDs generated successfully."
    else
        echo "Failed to generate GitLab manifest with CRDs."
        exit 1
    fi
else
    echo "Using Gitea as the Git provider."
    helm repo add gitea-charts https://dl.gitea.com/charts/
    helm repo update
    helm template gitea gitea-charts/gitea --version "$GITEA_VERSION" --include-crds --output-dir "$HACK_DIR" > "$HACK_DIR/gitea.yaml"

    if [ -f "$HACK_DIR/gitea.yaml" ]; then
        echo "Gitea manifest with CRDs generated successfully."
    else
        echo "Failed to generate Gitea manifest with CRDs."
        exit 1
    fi
fi

echo "Manifest updated for $GIT_PROVIDER to version $GITEA_VERSION."