#!/bin/bash
set -e

# Tekton ships release manifests (no official Helm chart). Pinned versions:
PIPELINE_VERSION="v0.65.0"
TRIGGERS_VERSION="v0.30.0"
DASHBOARD_VERSION="v0.52.0"

INSTALL_YAML="manifests/install.yaml"

echo "# TEKTON INSTALL RESOURCES (pipelines ${PIPELINE_VERSION}, triggers ${TRIGGERS_VERSION}, dashboard ${DASHBOARD_VERSION})" > ${INSTALL_YAML}
echo "# Auto-generated with 'platform/stack/packages/application/tekton/generate-manifests.sh'" >> ${INSTALL_YAML}

for url in \
  "https://storage.googleapis.com/tekton-releases/pipeline/previous/${PIPELINE_VERSION}/release.yaml" \
  "https://storage.googleapis.com/tekton-releases/triggers/previous/${TRIGGERS_VERSION}/release.yaml" \
  "https://storage.googleapis.com/tekton-releases/triggers/previous/${TRIGGERS_VERSION}/interceptors.yaml" \
  "https://storage.googleapis.com/tekton-releases/dashboard/previous/${DASHBOARD_VERSION}/release-full.yaml"; do
  echo "---" >> ${INSTALL_YAML}
  curl -sSfL "$url" >> ${INSTALL_YAML}
done

# All platform packages deploy into adhar-system.
sed -i.bak 's/tekton-pipelines-resolvers/adhar-system/g; s/tekton-pipelines/adhar-system/g' ${INSTALL_YAML}
rm -f ${INSTALL_YAML}.bak

# Tekton's webhook cert Secret is called "webhook-certs" upstream, which collides
# with sigstore policy-controller (the cosign package) now that every package
# shares adhar-system (ADR-0011). policy-controller hardcodes that Secret name in
# its binary; Tekton takes it from the WEBHOOK_SECRET_NAME env var, so Tekton is
# the side that renames. Rewrites the Secret, the Role resourceNames entry and the
# env value -- but never the already-distinct "triggers-webhook-certs".
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys
p = sys.argv[1]
s = open(p).read()
s, n = re.subn(r'(?<![-\w])webhook-certs\b', 'tekton-webhook-certs', s)
open(p, 'w').write(s)
print(f"renamed Secret/webhook-certs -> Secret/tekton-webhook-certs ({n} reference(s))")
PYEOF
