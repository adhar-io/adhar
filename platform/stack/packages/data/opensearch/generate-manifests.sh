#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="3.6.0"
DASHBOARDS_VERSION="3.6.0"

echo "# OPENSEARCH INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/opensearch/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add opensearch https://opensearch-project.github.io/helm-charts/ --force-update
helm repo update opensearch

# OpenSearch cluster
helm template --include-crds --namespace opensearch opensearch opensearch/opensearch -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# OpenSearch Dashboards (UI)
echo "---" >>${INSTALL_YAML}
helm template --include-crds --namespace opensearch opensearch-dashboards opensearch/opensearch-dashboards -f values-dashboards.yaml --version ${DASHBOARDS_VERSION} >>${INSTALL_YAML}
