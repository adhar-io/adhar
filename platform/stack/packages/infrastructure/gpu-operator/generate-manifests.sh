#!/bin/bash
# Renders the NVIDIA GPU Operator into manifests/install.yaml. Bump
# CHART_VERSION here and `version` in adhar-package.yaml together.
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v26.7.1"

echo "# GPU-OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/infrastructure/gpu-operator/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add nvidia https://helm.ngc.nvidia.com/nvidia --force-update
helm repo update nvidia
helm template --include-crds --namespace adhar-system gpu-operator nvidia/gpu-operator -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# Two fixes to the render:
#  * The chart ships three `helm.sh/hook` Jobs (pre-upgrade CRD upgrade,
#    pre-delete GPU-cluster cleanup, NFD post-delete prune). Under Argo CD
#    there is no Helm release, hook objects become Argo CD hooks, and a
#    privileged cleanup Job is not something a GitOps sync should run
#    implicitly. Dropped; CRDs are applied by the sync like any other object.
#  * Sync waves: the chart's CRDs (ClusterPolicy, NodeFeature*) before the
#    ClusterPolicy CR that uses them; the dcgm ServiceMonitor at 10 like every
#    other monitoring CR on the platform, so a first boot without the
#    Prometheus CRDs does not fail the sync.
python3 - "${INSTALL_YAML}" <<'PY'
import sys, yaml
path = sys.argv[1]
header = [line for line in open(path) if line.startswith("#")][:2]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
hooks = [d for d in docs if any(k.startswith("helm.sh/hook") for k in ((d.get("metadata") or {}).get("annotations") or {}))]
docs = [d for d in docs if d not in hooks]
n = 0
for d in docs:
    md = d.setdefault("metadata", {})
    ann = md.get("annotations") or {}
    if d.get("kind") == "CustomResourceDefinition":
        ann["argocd.argoproj.io/sync-wave"] = "-5"; n += 1
    elif d.get("kind") == "ServiceMonitor":
        ann["argocd.argoproj.io/sync-wave"] = "10"
        ann["argocd.argoproj.io/sync-options"] = "SkipDryRunOnMissingResource=true"; n += 1
    elif d.get("kind") == "ClusterPolicy":
        ann["argocd.argoproj.io/sync-wave"] = "5"
        ann["argocd.argoproj.io/sync-options"] = "SkipDryRunOnMissingResource=true"; n += 1
    if ann:
        md["annotations"] = ann
with open(path, "w") as fh:
    fh.writelines(header)
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False)
print(f"dropped {len(hooks)} helm hook object(s); wave-annotated {n} of {len(docs)}")
PY
echo "rendered gpu-operator ${CHART_VERSION}"
