#!/bin/bash
set -e

# Chaos Mesh control plane + dashboard. The dashboard UI is exposed via the
# Cilium Gateway (manifests/httproute.yaml); the chart Ingress stays disabled.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="2.8.4"

echo "# CHAOS MESH INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/chaos-mesh/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add chaos-mesh https://charts.chaos-mesh.org --force-update
helm repo update chaos-mesh
# --include-crds is mandatory: chaos-mesh ships its CRDs in the chart's crds/
# directory, which `helm template` omits by default. Without them the render
# contained ZERO CustomResourceDefinitions, and chaos-controller-manager
# crash-looped forever on "if kind is a CRD, it should be installed before
# calling Start {kind: StressChaos.chaos-mesh.org}" — the controller cannot
# build a cache for a kind the API server has never heard of.
helm template --namespace adhar-system chaos-mesh chaos-mesh/chaos-mesh -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

# Disable service links on every chaos-mesh pod.
#
# Kubernetes injects a <NAME>_PORT env var for each Service in the namespace,
# and since ADR-0011 put every package in adhar-system, cosign's
# policy-controller contributes `Service/webhook` — which becomes
# WEBHOOK_PORT=tcp://10.x.x.x:443. chaos-dashboard parses its configuration
# with envconfig and reads WEBHOOK_PORT as an integer, so it panicked at
# start-up: "assigning WEBHOOK_PORT to WebhookPort: converting
# 'tcp://10.107.33.255:443' to type int". The chart exposes no
# enableServiceLinks value, and cosign hardcodes the Service name in its binary
# (CONFLICTS.md), so the consumer has to opt out of the legacy links it never
# wanted. Nothing in chaos-mesh discovers anything through them.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import sys, yaml
path = sys.argv[1]
docs = list(yaml.safe_load_all(open(path)))
patched = 0
for d in docs:
    if not d or d.get("kind") not in ("Deployment", "StatefulSet", "DaemonSet"):
        continue
    spec = d.get("spec", {}).get("template", {}).get("spec")
    if spec is not None and "enableServiceLinks" not in spec:
        spec["enableServiceLinks"] = False
        patched += 1
with open(path, "w") as f:
    f.write("\n---\n".join(yaml.safe_dump(d, sort_keys=False) for d in docs if d))
print(f"enableServiceLinks=false on {patched} workload(s)")
PYEOF
