#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.10.0"

echo "# PROJECTSVELTOS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/sveltos/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add projectsveltos https://projectsveltos.github.io/helm-charts --force-update
helm repo update projectsveltos
helm template --namespace projectsveltos projectsveltos projectsveltos/projectsveltos -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
