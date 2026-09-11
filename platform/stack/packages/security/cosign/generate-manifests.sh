#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.10.7"
# Every platform package installs into the shared adhar-system namespace
# (ADR-0011); the package ships no kind:Namespace object (ADR-0011 invariant).
#
# Two names in this chart are NOT renameable -- the policy-controller binary
# hardcodes both (cmd/webhook/main.go: webhook.Options{ServiceName: "webhook",
# SecretName: "webhook-certs"}), so a manifest rename desynchronises the cert
# SANs / clientConfig and breaks admission TLS:
#   * Service/webhook -- injects WEBHOOK_PORT=tcp://... into every adhar-system
#     pod through service links. Components that parse *_PORT-shaped env vars
#     MUST set enableServiceLinks:false (crossplane already does) or set the
#     variable explicitly; that is the standing ADR-0011 rule.
#   * Secret/webhook-certs -- shared with any other knative-based webhook in the
#     namespace. tekton was moved off it (WEBHOOK_SECRET_NAME ->
#     tekton-webhook-certs, see application/tekton/generate-manifests.sh), but
#     kpack (the buildpack package) hardcodes it too and CANNOT be moved.
#     cosign and buildpack are therefore mutually exclusive while both live in
#     adhar-system -- see platform/stack/packages/CONFLICTS.md.
NAMESPACE="adhar-system"

echo "# COSIGN (SIGSTORE POLICY-CONTROLLER) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/cosign/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add sigstore https://sigstore.github.io/helm-charts --force-update
helm repo update sigstore
helm template --include-crds --namespace ${NAMESPACE} policy-controller sigstore/policy-controller -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
