#!/bin/bash
# Renders the llm-d router (standalone, agentgateway proxy) into manifests/install.yaml.
set -e
CHART_VERSION="v0.10.0"
helm pull oci://ghcr.io/llm-d/charts/llm-d-router-standalone --version ${CHART_VERSION} -d /tmp >/dev/null
tar xzf /tmp/llm-d-router-standalone-${CHART_VERSION}.tgz -C /tmp
{ sed -n '1,10p' manifests/install.yaml | grep '^#'; helm template llm-d /tmp/llm-d-router-standalone -n adhar-system -f values.yaml; } > manifests/install.yaml.new
mv manifests/install.yaml.new manifests/install.yaml
# The chart's EPP readiness probe times out after ONE second; on a loaded node
# that kills a healthy router (platform invariant: TestNoProbeShipsAOneSecondTimeout).
sed -i '' 's/^\([[:space:]]*\)timeoutSeconds: 1$/\1timeoutSeconds: 5/' manifests/install.yaml
echo "rendered llm-d router ${CHART_VERSION}"
