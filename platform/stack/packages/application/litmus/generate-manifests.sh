#!/bin/bash
set -e

# LitmusChaos — the platform's chaos-engineering engine, rendered from THREE
# upstream charts because Litmus 3.x splits into a control plane, an execution
# plane and a fault catalogue:
#
#   litmus            ChaosCenter: frontend + graphql server + auth server. The
#                     UI, the experiment history, the probes. -> install.yaml
#   litmus-core       chaos-operator + the ChaosEngine/ChaosExperiment/
#                     ChaosResult CRDs. Nothing runs without this: the operator
#                     is what turns a ChaosEngine into a running fault.
#                     -> core.yaml
#   kubernetes-chaos  the 29 Kubernetes ChaosExperiment definitions (pod-delete,
#                     pod-cpu-hog, node-drain, …). These are the faults a user
#                     picks from; without them ChaosCenter is an empty console.
#                     -> experiments.yaml
#
# Upstream's own install path is "apply ChaosCenter, then enrol a chaos
# infrastructure from the UI, which hands you a manifest with a token". That is
# a manual step at the end of an `adhar up`, so the execution plane is installed
# declaratively here instead and the platform ships ready to run a game day.
#
# MongoDB is NOT rendered from these charts — manifests/platform-mongodb.yaml
# provisions it through the platform's MongoDB operator. See values.yaml.

CHART_VERSION="3.30.0"
CORE_VERSION="3.31.1"
EXPERIMENTS_VERSION="3.31.0"

helm repo add litmuschaos https://litmuschaos.github.io/litmus-helm/ --force-update
helm repo update litmuschaos

render() {
  local out="$1" chart="$2" version="$3" wave="$4"; shift 4
  echo "# LITMUSCHAOS ${chart} RESOURCES" >"${out}"
  echo "# This file is auto-generated with 'platform/stack/packages/application/litmus/generate-manifests.sh'" >>"${out}"
  # --include-crds is mandatory for litmus-core: it ships the chaos CRDs in the
  # chart's crds/ directory, which `helm template` omits by default, and the
  # ChaosExperiments in experiments.yaml are instances of them.
  helm template --namespace adhar-system litmus "litmuschaos/${chart}" \
    --version "${version}" --include-crds "$@" >>"${out}"
  python3 - "${out}" "${wave}" <<'PYEOF'
import sys, yaml
path, wave = sys.argv[1], sys.argv[2]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
workloads = 0
for d in docs:
    # Ordering is explicit rather than left to Argo CD's kind heuristics: the
    # CRDs and the operator must be established before the 29 ChaosExperiment
    # instances, and the ChaosCenter must not start before its database.
    d.setdefault("metadata", {}).setdefault("annotations", {}).setdefault(
        "argocd.argoproj.io/sync-wave", wave)
    if d.get("kind") not in ("Deployment", "StatefulSet", "DaemonSet"):
        continue
    # One namespace for every package (ADR-0011) means Kubernetes injects a
    # <NAME>_PORT env var per Service, and the graphql server reads its
    # configuration from the environment; cosign's Service/webhook becomes
    # WEBHOOK_PORT=tcp://10.x.x.x:443, the shape that has crashed other packages
    # here (CONFLICTS.md). Litmus discovers nothing through service links.
    spec = d.get("spec", {}).get("template", {}).get("spec")
    if spec is not None and "enableServiceLinks" not in spec:
        spec["enableServiceLinks"] = False
        workloads += 1
with open(path, "w") as f:
    f.write("\n---\n".join(yaml.safe_dump(d, sort_keys=False) for d in docs))
print(f"{path}: wave {wave}, enableServiceLinks=false on {workloads} workload(s)")
PYEOF
}

# Wave 0: CRDs + the chaos operator. Everything else is an instance of these.
render manifests/core.yaml         litmus-core      "${CORE_VERSION}"        0
# Wave 2: the fault catalogue, after the CRDs are established.
render manifests/experiments.yaml  kubernetes-chaos "${EXPERIMENTS_VERSION}" 2
# Wave 3: ChaosCenter, after manifests/platform-mongodb.yaml (waves 0-1).
render manifests/install.yaml      litmus           "${CHART_VERSION}"       3 -f values.yaml
