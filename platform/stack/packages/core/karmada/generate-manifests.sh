#!/bin/bash
set -e

# Karmada is published as a GitHub release asset, not from a Helm repository:
# https://karmada-io.github.io/charts returns 404. The chart tarball ships with
# every release, so the version here is the Karmada version.
INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.19.0"
CHART_URL="https://github.com/karmada-io/karmada/releases/download/v${CHART_VERSION}/karmada-chart-v${CHART_VERSION}.tgz"

echo "# KARMADA (multi-cluster control plane) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/karmada/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Karmada v${CHART_VERSION}" >>${INSTALL_YAML}

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
curl -fsSL -o "${TMP}/karmada-chart.tgz" "${CHART_URL}"

helm template --namespace adhar-system karmada "${TMP}/karmada-chart.tgz" \
  -f values.yaml --version "${CHART_VERSION}" --include-crds >>${INSTALL_YAML}
