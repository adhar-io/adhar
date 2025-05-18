#!/bin/bash

# Update GitLab manifest using Helm
INSTALL_YAML="platform/controllers/adharplatform/resources/gitlab/install.yaml"
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
GITLAB_VERSION="v9.4.1"

# Use Helm to generate the GitLab manifest including CRDs
helm repo add gitlab https://charts.gitlab.io/
helm repo update gitlab
helm template gitlab gitlab/gitlab-ce --version "$GITLAB_VERSION" --include-crds > "$INSTALL_YAML"

if [ -f "$INSTALL_YAML" ]; then
    echo "GitLab manifest with CRDs generated successfully."
else
    echo "Failed to generate GitLab manifest with CRDs."
    exit 1
fi

echo "GitLab manifest updated to version $GITLAB_VERSION."