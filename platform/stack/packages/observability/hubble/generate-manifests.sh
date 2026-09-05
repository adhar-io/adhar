#!/usr/bin/env bash
set -euo pipefail
# Hubble relay + UI ship in the bootstrap Cilium install (a single Helm render so
# relay + the agent's Hubble server share one Cilium CA and their mTLS verifies).
# This GitOps package only exposes the Hubble UI through the Cilium Gateway, so
# there is nothing to render here — the HTTPRoute is checked in directly.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "Hubble UI HTTPRoute is ready at ${SCRIPT_DIR}/manifests/httproute.yaml (relay/UI come from the bootstrap Cilium install)."
