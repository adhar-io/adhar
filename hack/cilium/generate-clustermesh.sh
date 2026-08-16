#!/bin/bash
set -e
# Regenerate the opt-in Cilium Cluster Mesh apiserver manifest (roadmap P2.4).
#
# The default install.yaml (generate-manifests.sh) ships mesh-ready identity but
# NOT the clustermesh-apiserver pod, so a plain local `adhar up` stays single-node
# lean (ADR-0012). This script renders only the clustermesh-apiserver objects into
# a dedicated file that is applied on-demand when federating a second cluster.
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
OUT="$HACK_DIR/../../platform/controllers/adharplatform/resources/cilium/clustermesh.yaml"
CILIUM_VERSION="1.20.0"

helm repo add cilium https://helm.cilium.io/ >/dev/null 2>&1 || true
helm repo update cilium >/dev/null

TMP="$(mktemp)"
helm template cilium cilium/cilium --namespace adhar-system --version "$CILIUM_VERSION" \
  -f "$HACK_DIR/values.yaml" \
  --set clustermesh.useAPIServer=true \
  --set clustermesh.apiserver.service.type=ClusterIP \
  --set clustermesh.apiserver.tls.auto.enabled=true \
  --set clustermesh.apiserver.tls.auto.method=helm \
  > "$TMP"

python3 - "$TMP" "$OUT" <<'PY'
import sys, re
src, out = sys.argv[1], sys.argv[2]
docs = open(src).read().split('\n---\n')
keep = [d.strip('\n') for d in docs
        if (m := re.search(r'^# Source:\s*(\S+)', d, re.M))
        and 'clustermesh-apiserver/' in m.group(1) and d.strip()]
header = open(out).read().split('\n---\n', 1)[0] + '\n---\n'  # preserve existing header
open(out, 'w').write(header + ('\n---\n'.join(keep)) + '\n')
print(f"wrote {out}: {len(keep)} clustermesh-apiserver objects")
PY
echo "Cluster Mesh apiserver manifest regenerated ($CILIUM_VERSION)."
