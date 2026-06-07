#!/bin/bash
set -e

# Cloud Native Buildpacks for Kubernetes are provided by kpack. kpack has NO
# official Helm chart, so we vendor its upstream static release manifest. kpack
# installs into its own "kpack" namespace and exposes no web UI / Ingress, so no
# HTTPRoute is required. Image builds are driven by kpack CRDs
# (Image/Builder/ClusterStore/ClusterStack).

INSTALL_YAML="manifests/install.yaml"
KPACK_VERSION="v0.17.1"

echo "# BUILDPACK (kpack) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/buildpack/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# kpack ${KPACK_VERSION} (upstream release manifest)" >>${INSTALL_YAML}

curl -sSL "https://github.com/buildpacks-community/kpack/releases/download/${KPACK_VERSION}/release-${KPACK_VERSION#v}.yaml" >>${INSTALL_YAML}
