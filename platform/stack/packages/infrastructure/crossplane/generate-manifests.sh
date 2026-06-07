#!/bin/bash
set -e

# =============================================================================
# REDUNDANCY NOTICE
# =============================================================================
# This GitOps Crossplane package is REDUNDANT with the Crossplane core that the
# Adhar BOOTSTRAP installs imperatively (see
# platform/controllers/adharplatform/resources/crossplane/, currently v2.3.1).
# Installing both would conflict on cluster-scoped resources (CRDs, RBAC, the
# crossplane-system Deployment, etc.).
#
# It is pinned to the SAME version as the bootstrap (v2.3.1) and kept ONLY for
# GitOps parity / declarative reference. Prefer the bootstrap-managed install.
# Do NOT enable both simultaneously for the same cluster.
# =============================================================================

INSTALL_YAML="manifests/install.yaml"
# Keep in lockstep with the bootstrap Crossplane version
# (platform/controllers/adharplatform/resources/crossplane/).
CHART_VERSION="2.3.1"

echo "# CROSSPLANE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/infrastructure/crossplane/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# REDUNDANT with the bootstrap Crossplane install; kept only for GitOps parity (see header of generate-manifests.sh)." >>${INSTALL_YAML}

helm repo add crossplane https://charts.crossplane.io/stable --force-update
helm repo update crossplane
helm template --namespace crossplane-system crossplane crossplane/crossplane -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
