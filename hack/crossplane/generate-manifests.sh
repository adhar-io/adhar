#!/bin/bash

# Update Crossplane manifest using Helm
HACK_DIR="$(cd "$(dirname "$0")" && pwd)"
# Anchored to the script location so output lands in the same place regardless
# of the directory the script is invoked from.
INSTALL_YAML="$HACK_DIR/../../platform/controllers/adharplatform/resources/crossplane/install.yaml"
INSTALL_HA_YAML="$HACK_DIR/../../platform/controllers/adharplatform/resources/crossplane/install-ha.yaml"
CROSSPLANE_VERSION="v2.4.2"
CROSSPLANE_NAMESPACE="adhar-system"

# Use Helm to generate the Crossplane manifest including CRDs
helm repo add crossplane https://charts.crossplane.io/stable
helm repo update crossplane
helm template crossplane --namespace $CROSSPLANE_NAMESPACE crossplane/crossplane --version "$CROSSPLANE_VERSION" -f "$HACK_DIR/values.yaml" --include-crds > "$INSTALL_YAML"
helm template crossplane --namespace $CROSSPLANE_NAMESPACE crossplane/crossplane --version "$CROSSPLANE_VERSION" -f "$HACK_DIR/values-ha.yaml" --include-crds > "$INSTALL_HA_YAML"

if [ -f "$INSTALL_YAML" ]; then
    echo "Crossplane manifest with CRDs generated successfully."
else
    echo "Failed to generate Crossplane manifest with CRDs."
    exit 1
fi

echo "Crossplane manifest updated to version $CROSSPLANE_VERSION in namespace $CROSSPLANE_NAMESPACE."
# Re-apply the two things the rendered chart does not carry.
#
# Both were hand-edits of the generated files until 2026-10-09, when the first
# regeneration in months silently reverted them — the same trap the plane
# package had. A post-processing step is the only form that survives
# regeneration, which is why they are here and not in the output.
#
# 1. enableServiceLinks: false on both Deployments. Every platform component
#    shares the adhar-system namespace, so Kubernetes injects a <SVC>_PORT env
#    var for each Service in it. cosign's policy-controller ships a Service
#    named "webhook", which becomes WEBHOOK_PORT=tcp://<ip>:443 — and crossplane
#    reads that as its own --webhook-port flag and fails to start.
#
# 2. A header saying the resource limits are caps rather than reservations.
#
# Pinned by TestCrossplaneManifestsDisableServiceLinks.
python3 - "$INSTALL_YAML" "$INSTALL_HA_YAML" <<'PYEOF'
import re, sys

NOTE_CROSSPLANE = (
    "      # Platform components share the adhar-system namespace, so kube injects a\n"
    "      # <SVC>_PORT env var for every Service in it. cosign's policy-controller\n"
    "      # ships a Service named \"webhook\", yielding WEBHOOK_PORT=tcp://<ip>:443 —\n"
    "      # which crossplane parses as its own --webhook-port flag and fails to start.\n"
    "      enableServiceLinks: false\n")
NOTE_RBAC = (
    "      # See the note on the crossplane Deployment above: shared-namespace service\n"
    "      # link env vars collide with crossplane's own flag env vars.\n"
    "      enableServiceLinks: false\n")

HEADER = (
    "# NOTE: container resource *limits* below are caps, not reservations — the\n"
    "# requests stay small so a single-node Kind cluster is unaffected. Crossplane\n"
    "# was throttled and OOM-killed in a loop (29 restarts on a 10-node cloud\n"
    "# cluster) at cpu 200m / memory 512Mi once the cloud provider packages\n"
    "# registered their CRDs, so no package, XRD or composition ever reconciled.\n"
    "# The numbers live in hack/crossplane/values*.yaml; do not patch them here.\n")

for path in sys.argv[1:]:
    src = open(path).read()
    added = 0
    for sa, note in (("crossplane", NOTE_CROSSPLANE), ("rbac-manager", NOTE_RBAC)):
        anchor = f"      serviceAccountName: {sa}\n"
        if anchor not in src:
            raise SystemExit(f"{path}: no pod spec with serviceAccountName: {sa}; the chart's "
                             "deployment template changed and enableServiceLinks would be lost")
        if "enableServiceLinks" in src.split(anchor)[0][-400:]:
            continue
        src = src.replace(anchor, note + anchor, 1)
        added += 1
    if not src.startswith("# NOTE: container resource"):
        src = HEADER + src
    open(path, "w").write(src)
    print(f"  {path.split('/')[-1]}: re-applied enableServiceLinks on {added} Deployment(s)")
PYEOF
