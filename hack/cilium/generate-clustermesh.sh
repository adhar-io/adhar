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

# A Cluster Mesh is defined by a shared CA, and Helm mints a fresh self-signed one
# on every render — so the clustermesh certificates must be pinned to the very CA
# baked into install.yaml (`cilium-ca`). Without this the apiserver presents a
# certificate no cilium agent trusts, and two Adhar clusters rendered at different
# times could never authenticate each other.
#
# clustermesh.config.enabled=true is likewise required, for the
# `clustermesh-remote-users` ConfigMap: the apiserver Deployment mounts it
# non-optionally, so without it the pod never leaves Init. The filter below keeps
# only `clustermesh-apiserver/` sources, which deliberately drops the two
# `clustermesh-config/` Secrets that same flag renders EMPTY (cilium-clustermesh,
# cilium-kvstoremesh): those hold the peer configuration `cilium clustermesh
# connect` writes, and re-applying an empty copy would tear the mesh down again.
INSTALL="$HACK_DIR/../../platform/controllers/adharplatform/resources/cilium/install.yaml"
CA_CRT="$(python3 "$HACK_DIR/read-cilium-ca.py" "$INSTALL" crt)"
CA_KEY="$(python3 "$HACK_DIR/read-cilium-ca.py" "$INSTALL" key)"
[ -n "$CA_CRT" ] && [ -n "$CA_KEY" ] || { echo "could not read cilium-ca from $INSTALL" >&2; exit 1; }

TMP="$(mktemp)"
helm template cilium cilium/cilium --namespace adhar-system --version "$CILIUM_VERSION" \
  -f "$HACK_DIR/values.yaml" \
  --set tls.ca.cert="$CA_CRT" \
  --set tls.ca.key="$CA_KEY" \
  --set clustermesh.useAPIServer=true \
  --set clustermesh.config.enabled=true \
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
