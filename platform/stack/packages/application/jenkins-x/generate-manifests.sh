#!/bin/bash
set -e

# Lighthouse (Jenkins X webhook/ChatOps/Tekton-trigger layer, ADR-0018).
CHART_VERSION="1.33.1"
NAMESPACE="adhar-system"

cd "$(dirname "$0")"

helm repo add jenkins-x https://jenkins-x-charts.github.io/repo
helm repo update jenkins-x
helm template lighthouse jenkins-x/lighthouse \
  --version "${CHART_VERSION}" \
  --namespace "${NAMESPACE}" \
  -f values.yaml > manifests/install.yaml

echo "Lighthouse manifests generated (chart ${CHART_VERSION})."
