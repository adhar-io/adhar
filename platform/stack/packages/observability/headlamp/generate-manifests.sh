#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.44.0"

# NOTE: the full Keycloak OIDC + platform-CA + cold-boot wiring is FOLDED into
# values.yaml using the chart's native knobs (env / config.extraArgs /
# volumes / config.oidc.secret.create=false), so this plain `helm template`
# reproduces install.yaml faithfully — NO manual patching is required after
# regenerating. If you bump CHART_VERSION, re-verify the rendered deployment
# still carries the 4 OPTIONAL OIDC_* secretKeyRefs (cold-boot), the -oidc-*
# args including -oidc-ca-file and -oidc-callback-url, and the platform-ca
# volume/mount. See the header comment in values.yaml for the rationale.

echo "# HEADLAMP INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/headlamp/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add headlamp https://kubernetes-sigs.github.io/headlamp/ --force-update
helm repo update
helm template --namespace adhar-system headlamp headlamp/headlamp -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

