#!/bin/bash
set -e

# Argo Events controller + eventbus/sensor/eventsource controllers. No web UI,
# so no HTTPRoute is required.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.4.21"

echo "# ARGO EVENTS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/argo-events/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add argo https://argoproj.github.io/argo-helm --force-update
helm repo update argo
helm template --namespace argo-events argo-events argo/argo-events -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
