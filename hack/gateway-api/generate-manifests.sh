#!/bin/bash
set -e

# Regenerates the embedded Gateway API CRD bundle used by the Cilium Gateway.
#
# Cilium's Gateway controller requires the EXPERIMENTAL channel: it indexes
# TLSRoute at gateway.networking.k8s.io/v1alpha2, which the standard channel
# ships but does NOT serve (served=false). Installing the experimental bundle
# fixes the cilium-operator crash:
#   "no matches for kind \"TLSRoute\" in version \"gateway.networking.k8s.io/v1alpha2\""
#
# We also strip the release's `safe-upgrades` ValidatingAdmissionPolicy + binding,
# which otherwise blocks applying experimental CRDs on top of standard ones.
#
# Keep GATEWAY_API_VERSION in sync with the Cilium version in hack/cilium.

GATEWAY_API_VERSION="v1.5.1"
OUT="platform/controllers/adharplatform/resources/gateway-api/crds.yaml"
URL="https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/experimental-install.yaml"

echo "Downloading Gateway API ${GATEWAY_API_VERSION} (experimental channel)..."
TMP="$(mktemp)"
curl -fsSL "$URL" -o "$TMP"

echo "Stripping the safe-upgrades ValidatingAdmissionPolicy (blocks experimental-on-standard)..."
python3 - "$TMP" "$OUT" <<'PY'
import sys
src = open(sys.argv[1]).read()
docs = src.split("\n---\n")
kept = [d for d in docs if not ("safe-upgrades.gateway.networking.k8s.io" in d and "kind: ValidatingAdmissionPolicy" in d)]
open(sys.argv[2], "w").write("\n---\n".join(kept))
print(f"  docs {len(docs)} -> {len(kept)}")
PY

rm -f "$TMP"
echo "Gateway API CRDs written to ${OUT} (version ${GATEWAY_API_VERSION})."
