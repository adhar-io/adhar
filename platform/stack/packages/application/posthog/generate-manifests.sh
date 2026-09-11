#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="30.46.0"

echo "# POSTHOG INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/posthog/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add posthog https://posthog.github.io/charts-clickhouse/ --force-update
helm repo update posthog   # only this repo: an unrelated stale repo entry must not abort generation
helm template --namespace adhar-system posthog posthog/posthog -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}
