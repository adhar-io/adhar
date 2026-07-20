#!/bin/bash
set -e

# Tekton ships release manifests (no official Helm chart). Pinned versions:
PIPELINE_VERSION="v0.65.0"
TRIGGERS_VERSION="v0.30.0"
DASHBOARD_VERSION="v0.52.0"

INSTALL_YAML="manifests/install.yaml"

echo "# TEKTON INSTALL RESOURCES (pipelines ${PIPELINE_VERSION}, triggers ${TRIGGERS_VERSION}, dashboard ${DASHBOARD_VERSION})" > ${INSTALL_YAML}
echo "# Auto-generated with 'platform/stack/packages/application/tekton/generate-manifests.sh'" >> ${INSTALL_YAML}

for url in \
  "https://storage.googleapis.com/tekton-releases/pipeline/previous/${PIPELINE_VERSION}/release.yaml" \
  "https://storage.googleapis.com/tekton-releases/triggers/previous/${TRIGGERS_VERSION}/release.yaml" \
  "https://storage.googleapis.com/tekton-releases/triggers/previous/${TRIGGERS_VERSION}/interceptors.yaml" \
  "https://storage.googleapis.com/tekton-releases/dashboard/previous/${DASHBOARD_VERSION}/release-full.yaml"; do
  echo "---" >> ${INSTALL_YAML}
  curl -sSfL "$url" >> ${INSTALL_YAML}
done

# All platform packages deploy into adhar-system.
sed -i.bak 's/tekton-pipelines-resolvers/adhar-system/g; s/tekton-pipelines/adhar-system/g' ${INSTALL_YAML}
rm -f ${INSTALL_YAML}.bak
