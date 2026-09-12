#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
# agentgateway (Linux Foundation / Agentic AI Foundation, originally solo.io).
# Both charts are published only as OCI artifacts on cr.agentgateway.dev, so
# there is no `helm repo add`/`helm repo update` step here — `helm template`
# pulls the pinned version straight from the registry (same pattern as
# application/kargo and application/n8n).
#
# The CRDs ship as a SEPARATE chart (agentgateway-crds) and are NOT bundled in
# the main chart's crds/ directory, so `--include-crds` on the main chart alone
# renders nothing. Both charts are templated, CRDs first, so a single
# install.yaml applies in dependency order.
CHART_VERSION="v1.5.0"

echo "# AGENTGATEWAY INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/ai/agentgateway/generate-manifests.sh'" >>${INSTALL_YAML}

# 1) CRDs: AgentgatewayBackend, AgentgatewayPolicy, AgentgatewayParameters,
#    AgentgatewayModel. Rendered at sync-wave -5 (see the post-processing step)
#    so ArgoCD establishes them before any CR in this package is applied.
helm template --namespace adhar-system agentgateway-crds oci://cr.agentgateway.dev/charts/agentgateway-crds \
  --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

# 2) Control plane: the agentgateway controller (xDS server) that reconciles
#    Gateways of GatewayClass `agentgateway` into data-plane proxy Deployments.
#
#    NOTE ON GATEWAYCLASSES: the chart does NOT render a GatewayClass object.
#    The controller creates and owns `agentgateway`
#    (controllerName agentgateway.dev/agentgateway) at runtime. That is a
#    different class and a different controller from the platform's Cilium edge
#    (GatewayClass `adhar`, controller io.cilium/gateway-controller), so the two
#    coexist with nothing to rename: Cilium stays the single TLS-terminating
#    edge on nodePort 30080/30443, and agentgateway is an internal AI data plane
#    behind it. Keep BOTH — see manifests/httproute.yaml for the topology.
helm template --namespace adhar-system agentgateway oci://cr.agentgateway.dev/charts/agentgateway \
  -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

# Put the CRDs on an early sync-wave. ArgoCD applies CustomResourceDefinitions
# and the controller in one wave otherwise, and on a FIRST boot the
# AgentgatewayBackend/Policy CRs in this package would fail their dry-run
# against a not-yet-established CRD. Wave -5 matches the repo's first-boot
# ordering rule (CRDs before controllers before CRs).
python3 - "${INSTALL_YAML}" <<'PY'
import sys, re
path = sys.argv[1]
docs = open(path).read().split('\n---\n')
out, n = [], 0
for d in docs:
    if re.search(r'^kind:\s*CustomResourceDefinition\s*$', d, re.M):
        if 'argocd.argoproj.io/sync-wave' not in d:
            # Insert under the existing metadata.annotations block, or create one.
            if re.search(r'^  annotations:\s*$', d, re.M):
                d = re.sub(r'^(  annotations:\s*\n)', r'\1    argocd.argoproj.io/sync-wave: "-5"\n', d, count=1, flags=re.M)
            else:
                d = re.sub(r'^(metadata:\s*\n)', r'\1  annotations:\n    argocd.argoproj.io/sync-wave: "-5"\n', d, count=1, flags=re.M)
            n += 1
    out.append(d)
open(path, 'w').write('\n---\n'.join(out))
print("annotated %d CRDs with sync-wave -5" % n)
PY
