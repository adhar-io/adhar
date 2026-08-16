#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.10.6"
# cosign gets its OWN namespace (not the shared adhar-system). This resolves two
# collisions documented in platform/stack/packages/CONFLICTS.md:
#   1. Secret/webhook-certs collided with tekton (whichever synced last owned the
#      cert; the other's admission webhook then failed TLS).
#   2. Service/webhook injected WEBHOOK_PORT into every adhar-system pod via
#      service-links, which crashlooped crossplane (--webhook-port parse error).
# A dedicated namespace removes both by construction. The ApplicationSet sets the
# Application destination namespace to cosign-system with CreateNamespace=true, so
# the package does NOT ship a kind:Namespace object (ADR-0011 invariant).
NAMESPACE="cosign-system"

echo "# COSIGN (SIGSTORE POLICY-CONTROLLER) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/cosign/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add sigstore https://sigstore.github.io/helm-charts --force-update
helm repo update sigstore
helm template --include-crds --namespace ${NAMESPACE} policy-controller sigstore/policy-controller -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
