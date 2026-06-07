#!/bin/bash
set -e

# Renders the Argo Workflows base install from the upstream namespace-install
# manifest. We vendor the upstream manifest (rather than the Helm chart) because
# the dev overlay (manifests/dev/kustomization.yaml) strategic-merge patches the
# upstream resource names ("workflow-controller-configmap" and "argo-server"),
# which the Helm chart prefixes with the release name. The dev overlay layers SSO
# config, the admin SA, the external secret and the Gateway API HTTPRoute on top.
#
# Gateway API only -- the upstream manifest ships NO Ingress; external routing is
# handled by manifests/dev/ingress.yaml (an HTTPRoute that strips /argo-workflows).

INSTALL_YAML="manifests/base/install.yaml"
APP_VERSION="v4.0.5"

echo "# ARGO WORKFLOWS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/argo-workflows/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Argo Workflows ${APP_VERSION} (upstream install.yaml)" >>${INSTALL_YAML}

curl -sSL "https://github.com/argoproj/argo-workflows/releases/download/${APP_VERSION}/install.yaml" >>${INSTALL_YAML}
