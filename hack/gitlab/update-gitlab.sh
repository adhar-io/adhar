#!/bin/bash

# Update GitLab manifest using Helm
HACK_DIR="$(dirname \"$0\")"
GITLAB_VERSION=${1:-"6.0.0"} # Default to version 6.0.0 if not provided

# Ensure the hack directory exists
mkdir -p "$HACK_DIR"

# Use Helm to generate the GitLab manifest including CRDs
helm repo add gitlab https://charts.gitlab.io/
helm repo update
helm template gitlab gitlab/gitlab --version "$GITLAB_VERSION" --include-crds --output-dir "$HACK_DIR" > "$HACK_DIR/gitlab.yaml"

if [ -f "$HACK_DIR/gitlab.yaml" ]; then
    echo "GitLab manifest with CRDs generated successfully."
else
    echo "Failed to generate GitLab manifest with CRDs."
    exit 1
fi

echo "GitLab manifest updated to version $GITLAB_VERSION."