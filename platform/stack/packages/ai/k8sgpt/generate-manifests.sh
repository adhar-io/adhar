#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.2.29"

echo "# K8SGPT-OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/ai/k8sgpt/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add k8sgpt https://charts.k8sgpt.ai/ --force-update
helm repo update k8sgpt
helm template --include-crds --namespace adhar-system k8sgpt k8sgpt/k8sgpt-operator -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# The chart renders its ServiceMonitor at wave 0; on a first boot the
# monitoring.coreos.com CRDs may not exist yet (repo rule: monitoring CRs at
# wave 10). CRDs go first (wave -5) so the K8sGPT CR in k8sgpt.yaml never
# fails its dry-run against an unestablished CRD.
python3 - "${INSTALL_YAML}" <<'PY'
import sys, yaml
path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
n = 0
for d in docs:
    ann = d.setdefault("metadata", {}).setdefault("annotations", {}) or {}
    if d.get("kind") == "CustomResourceDefinition":
        ann["argocd.argoproj.io/sync-wave"] = "-5"; n += 1
    elif d.get("kind") == "ServiceMonitor":
        ann["argocd.argoproj.io/sync-wave"] = "10"; n += 1
    d["metadata"]["annotations"] = ann
with open(path, "w") as fh:
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False)
print(f"wave-annotated {n} object(s)")
PY
