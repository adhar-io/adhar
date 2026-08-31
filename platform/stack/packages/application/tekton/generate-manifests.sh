#!/bin/bash
set -e

# Tekton ships release manifests (no official Helm chart). Pinned versions.
# Recent releases are published as GitHub release assets (the old GCS
# tekton-releases/previous/ bucket lags), so we pull from GitHub.
PIPELINE_VERSION="v1.16.0"
TRIGGERS_VERSION="v0.37.0"
DASHBOARD_VERSION="v0.71.0"   # LTS

INSTALL_YAML="manifests/install.yaml"

echo "# TEKTON INSTALL RESOURCES (pipelines ${PIPELINE_VERSION}, triggers ${TRIGGERS_VERSION}, dashboard ${DASHBOARD_VERSION} LTS)" > ${INSTALL_YAML}
echo "# Auto-generated with 'platform/stack/packages/application/tekton/generate-manifests.sh'" >> ${INSTALL_YAML}

for url in \
  "https://github.com/tektoncd/pipeline/releases/download/${PIPELINE_VERSION}/release.yaml" \
  "https://github.com/tektoncd/triggers/releases/download/${TRIGGERS_VERSION}/release.yaml" \
  "https://github.com/tektoncd/triggers/releases/download/${TRIGGERS_VERSION}/interceptors.yaml" \
  "https://github.com/tektoncd/dashboard/releases/download/${DASHBOARD_VERSION}/release-full.yaml"; do
  echo "---" >> ${INSTALL_YAML}
  curl -sSfL "$url" >> ${INSTALL_YAML}
done

# All platform packages deploy into adhar-system.
sed -i.bak 's/tekton-pipelines-resolvers/adhar-system/g; s/tekton-pipelines/adhar-system/g' ${INSTALL_YAML}
rm -f ${INSTALL_YAML}.bak
