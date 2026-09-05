#!/usr/bin/env bash
set -euo pipefail
# Hubble Relay + UI, deployed as a GitOps package rather than baked into the
# bootstrap Cilium install — this keeps the hubble-relay/hubble-ui/-backend
# images OFF the critical path of `adhar up` (the "Cilium & Gateway" phase). The
# Cilium AGENT's Hubble server stays in the bootstrap install (hubble.enabled=true
# + hubble-peer/hubble-server-certs); the relay here connects to
# hubble-peer.adhar-system and the UI talks to the relay.
#
# Rendered from the same cilium chart + values as the bootstrap install, with
# relay/ui force-enabled, then filtered down to just the relay/ui resources.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFESTS_DIR="${SCRIPT_DIR}/manifests"
CILIUM_VALUES="${SCRIPT_DIR}/../../../../../hack/cilium/values.yaml"
CILIUM_VERSION="${CILIUM_VERSION:-1.20.0}"
INSTALL_YAML="${MANIFESTS_DIR}/install.yaml"

mkdir -p "${MANIFESTS_DIR}"
helm repo add cilium https://helm.cilium.io/ >/dev/null 2>&1 || true
helm repo update cilium >/dev/null 2>&1 || true

TMP="$(mktemp)"
helm template cilium cilium/cilium --namespace adhar-system --version "${CILIUM_VERSION}" \
  -f "${CILIUM_VALUES}" \
  --set hubble.relay.enabled=true --set hubble.ui.enabled=true > "${TMP}"

python3 - "${TMP}" "${INSTALL_YAML}" <<'PY'
import sys, yaml
src, out = sys.argv[1], sys.argv[2]
docs=[d for d in yaml.safe_load_all(open(src)) if d]
keep=[d for d in docs if ('hubble-relay' in d.get('metadata',{}).get('name','')
                          or 'hubble-ui' in d.get('metadata',{}).get('name',''))]
order={'ServiceAccount':0,'Secret':1,'ConfigMap':2,'ClusterRole':3,'ClusterRoleBinding':4,'Service':5,'Deployment':6}
keep.sort(key=lambda d: order.get(d.get('kind'),9))
with open(out,'w') as f:
    f.write("# HUBBLE RELAY + UI — moved off the bootstrap Cilium critical path so their\n")
    f.write("# images no longer block the 'Cilium & Gateway' phase of `adhar up`. The\n")
    f.write("# agent's Hubble server stays in the bootstrap install; relay connects to\n")
    f.write("# hubble-peer.adhar-system. Regenerate with generate-manifests.sh.\n")
    yaml.safe_dump_all(keep, f, default_flow_style=False, sort_keys=False)
print(f"Wrote {len(keep)} Hubble relay/UI resources to {out}")
PY
rm -f "${TMP}"
