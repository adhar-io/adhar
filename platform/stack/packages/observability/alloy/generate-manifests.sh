#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.12.1"

echo "# ALLOY INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/alloy/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
helm template --namespace adhar-system alloy grafana/alloy -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
# The chart ships probes with timeoutSeconds: 1, which no package here may do
# (TestNoProbeShipsAOneSecondTimeout): a loaded node needs longer than a second
# to answer, and kubelet then kills a healthy agent. Raised on every render so a
# regeneration cannot silently revert it (it did, 2026-09-24).
python3 - "${INSTALL_YAML}" <<'PYEOF'
import sys, yaml
path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
raised = 0
for d in docs:
    if d.get("kind") not in ("Deployment", "StatefulSet", "DaemonSet"):
        continue
    spec = d["spec"]["template"]["spec"]
    for c in (spec.get("containers") or []) + (spec.get("initContainers") or []):
        for probe in ("livenessProbe", "readinessProbe", "startupProbe"):
            p = c.get(probe)
            if p and p.get("timeoutSeconds", 1) < 5:
                p["timeoutSeconds"] = 5; raised += 1
with open(path, "w") as fh:
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False)
print(f"probes: {raised} timeout(s) raised to 5s")
PYEOF
