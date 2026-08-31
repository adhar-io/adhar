#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.19.1"

echo "# HARBOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/harbor/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add harbor https://helm.goharbor.io --force-update
helm repo update
helm template --namespace adhar-system harbor harbor/harbor -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# Pin the harbor-core Service ClusterIP to a stable, known address.
#
# With internalTLS on, the kind node's containerd pulls kpack-built images whose
# tag host is harbor-core.adhar-system.svc.cluster.local — a name the node's
# resolver cannot resolve. The durable node config (kind provider) instead dials
# this fixed ClusterIP over HTTPS (the core cert carries it as an IP SAN). Pinning
# it means the address is known ahead of time, both to the cert minter and to the
# node's containerd registry config, with no runtime ClusterIP discovery.
HARBOR_CORE_CLUSTERIP="10.96.222.222"
python3 - "${INSTALL_YAML}" "${HARBOR_CORE_CLUSTERIP}" <<'PY'
import sys, re
path, cip = sys.argv[1], sys.argv[2]
docs = open(path).read().split('\n---\n')
out = []
for d in docs:
    if re.search(r'^kind:\s*Service\s*$', d, re.M) and re.search(r'^  name:\s*harbor-core\s*$', d, re.M):
        # insert clusterIP as the first line under spec:
        d = re.sub(r'(^spec:\s*\n)', r'\1  clusterIP: %s\n' % cip, d, count=1, flags=re.M)
    out.append(d)
open(path, 'w').write('\n---\n'.join(out))
print("pinned harbor-core clusterIP -> %s" % cip)
PY