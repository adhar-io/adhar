#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.12.10"

echo "# PIXIE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/pixie/generate-manifests.sh'" >>${INSTALL_YAML}

# Pixie (pixie/pixie-chart) deploys the Pixie Vizier eBPF data collector into the
# 'pl' namespace. Vizier connects to a Pixie Cloud control plane (the web UI lives
# in Pixie Cloud, not in-cluster, so no Gateway API HTTPRoute is needed).
#
# A Pixie Cloud deploy key is required for the Vizier to register. It is a
# per-user cloud credential and cannot be committed, so it is rendered empty
# here. Supply it before / after install, e.g.:
#   kubectl -n pl create secret generic pl-deploy-secrets \
#     --from-literal=deploy-key=<YOUR_PIXIE_DEPLOY_KEY> --dry-run=client -o yaml | kubectl apply -f -
# and set PL_CLUSTER_NAME in the pl-cloud-config ConfigMap.

helm repo add pixie https://pixie-helm-charts.storage.googleapis.com --force-update
helm repo update pixie
helm template --namespace pl pixie pixie/pixie-chart \
  --version ${CHART_VERSION} \
  --set deployKey="" \
  --set clusterName="adhar" \
  --set cloudAddr="withpixie.ai:443" >>${INSTALL_YAML}
