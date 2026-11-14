#!/bin/bash
set -euo pipefail

# Acceptance A2.8 (docs/design/0025 §15, Phase 2): what Volcano costs the API
# server. Prints one Markdown table row for the package README:
#
#   ./measure-footprint.sh before    # Volcano not installed
#   ./measure-footprint.sh after     # Volcano Healthy for at least 15 minutes
#
# READ-ONLY. It runs `kubectl get` and `kubectl top` against the current context
# and nothing else; check that context names the cluster you mean (CLAUDE.md,
# "Kube context moves between clusters") before believing the numbers.
#
# What it reads, and why those:
#   kube-apiserver RSS   process_resident_memory_bytes from the API server's own
#                        /metrics. With more than one API server each scrape lands on
#                        one of them; run it on a single-master cluster (the canary)
#                        or repeat until every replica has answered.
#   CRD bytes            the JSON size of every CustomResourceDefinition, and of
#                        Volcano's alone (*.volcano.sh). The API server holds each
#                        CRD's schema in memory, so bytes are the honest proxy.
#   control plane        the control-plane node's memory from metrics-server, so
#                        "the delta fits the master" is a number, not a feeling.

phase="${1:-}"
if [[ "${phase}" != "before" && "${phase}" != "after" ]]; then
  echo "usage: $0 before|after" >&2
  exit 2
fi

context="$(kubectl config current-context)"
server="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
version="$(kubectl version -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"])')"

rss_bytes="$(kubectl get --raw /metrics | awk '$1 == "process_resident_memory_bytes" {printf "%.0f", $2}')"
heap_bytes="$(kubectl get --raw /metrics | awk '$1 == "go_memstats_heap_inuse_bytes" {printf "%.0f", $2}')"

crds_json="$(mktemp)"
trap 'rm -f "${crds_json}"' EXIT
kubectl get customresourcedefinitions -o json >"${crds_json}"
read -r crd_count crd_bytes volcano_count volcano_bytes < <(python3 - "${crds_json}" <<'PY'
import json, sys
items = json.load(open(sys.argv[1]))["items"]
size = lambda o: len(json.dumps(o, separators=(",", ":")))
volcano = [i for i in items if i["metadata"]["name"].endswith(".volcano.sh")]
print(len(items), sum(map(size, items)), len(volcano), sum(map(size, volcano)))
PY
)

cp_mem="$(kubectl top node -l node-role.kubernetes.io/control-plane --no-headers 2>/dev/null | awk '{print $4 " (" $5 ")"; exit}')"

mib() { awk -v b="$1" 'BEGIN {printf "%.0f MiB", b / 1048576}'; }
kib() { awk -v b="$1" 'BEGIN {printf "%.0f KiB", b / 1024}'; }

echo "| ${phase} | $(date -u +%Y-%m-%dT%H:%MZ) | ${context} (${server}) | ${version} | $(mib "${rss_bytes}") | $(mib "${heap_bytes:-0}") | ${crd_count} / $(kib "${crd_bytes}") | ${volcano_count} / $(kib "${volcano_bytes}") | ${cp_mem:-n/a} |"
