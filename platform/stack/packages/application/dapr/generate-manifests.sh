#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.18.3"

echo "# DAPR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/dapr/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add dapr https://dapr.github.io/helm-charts/ --force-update
helm repo update
helm template --namespace adhar-system dapr dapr/dapr -f values.yaml --version ${CHART_VERSION} --include-crds --set crds.enabled=true >>${INSTALL_YAML}

# What the chart cannot be told: a single-member scheduler (its replica count
# AND its etcd peer list, which the template hard-codes to three) and the
# node-bound priority its node-local volume needs. Hand-editing the output is
# how the scheduler lost quorum on 2026-10-16 — the next render reverted half of
# the edit and nothing noticed. Keep last; it exits non-zero if the chart's
# shape changed under it. Pinned by TestDaprSchedulerIsASingleMemberEtcd.
python3 post-render.py "${INSTALL_YAML}"