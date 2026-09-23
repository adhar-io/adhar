#!/bin/bash
set -e

# Policy Reporter, rendered from the upstream chart.
#
# WHY THIS SCRIPT EXISTS
# These manifests were previously hand-written, and drifted into a state that
# could not work: the core was pinned at policy-reporter:2.20.1 (which serves
# only the /v1 API) while the UI was policy-reporter-ui:2.4.0 (which discovers
# its data through /v2). The UI's source discovery 404'd, so `/api/config`
# reported `sources: []` and the dashboard rendered empty — with every component
# Running and healthy, and 11,952 results sitting in the core.
#
# Rendering from the chart pins a pair the upstream project actually ships
# together, so the two halves cannot drift apart again.
#
# The UI is exposed through the platform Gateway behind the Keycloak oauth2-proxy
# (manifests/dashboard-sso.yaml + httproute.yaml), which is why the chart's own
# ingress and auth stay off.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="3.10.0"

echo "# POLICY REPORTER INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/security/adhar-policy-reporter/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Chart policy-reporter/policy-reporter ${CHART_VERSION} — core + UI are pinned TOGETHER by the chart." >>${INSTALL_YAML}

helm repo add policy-reporter https://kyverno.github.io/policy-reporter --force-update
helm repo update policy-reporter
helm template --namespace adhar-system policy-reporter policy-reporter/policy-reporter \
  -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# Disable service links on every workload.
#
# One namespace for every package (ADR-0011) means Kubernetes injects a
# <NAME>_PORT env var per Service; cosign's Service/webhook becomes
# WEBHOOK_PORT=tcp://… and anything decoding env strictly breaks
# (platform/stack/packages/CONFLICTS.md).
python3 - "${INSTALL_YAML}" <<'PYEOF'
import sys, yaml
path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
patched = 0
for d in docs:
    if d.get("kind") not in ("Deployment", "StatefulSet", "DaemonSet"):
        continue
    spec = d.get("spec", {}).get("template", {}).get("spec")
    if spec is not None and "enableServiceLinks" not in spec:
        spec["enableServiceLinks"] = False
        patched += 1
with open(path, "w") as f:
    f.write("\n---\n".join(yaml.safe_dump(d, sort_keys=False) for d in docs))
print(f"enableServiceLinks=false on {patched} workload(s)")
PYEOF
