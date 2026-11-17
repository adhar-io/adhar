#!/bin/bash
set -e

# ExternalDNS controller. No web UI, so no HTTPRoute. The DNS provider is set to
# "inmemory" in values.yaml so the controller installs cleanly on a local cluster
# with no cloud creds; set a real provider + credentials per environment.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.21.1"

echo "# EXTERNAL-DNS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/external-dns/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/ --force-update
helm repo update external-dns
helm template --namespace adhar-system external-dns external-dns/external-dns -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# The Deployment is owned by manifests/deployment.yaml.tmpl (a stack template
# rendered per cluster with the platform's DNS provider, zone and credentials),
# so drop the chart's copy; keep the ServiceAccount/RBAC/Service it generates.
# Re-check deployment.yaml.tmpl against the chart's Deployment when bumping
# CHART_VERSION (image tag, probes, security context).
python3 - "${INSTALL_YAML}" <<'EOF'
import re, sys
p = sys.argv[1]
docs = open(p).read().split('\n---\n')
open(p, 'w').write('\n---\n'.join(d for d in docs if not re.search(r'^kind: Deployment$', d, re.M)))
EOF
