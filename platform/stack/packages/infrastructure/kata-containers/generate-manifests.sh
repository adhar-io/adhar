#!/bin/bash
# Renders the Kata Containers runtime installer (kata-deploy) into
# manifests/install.yaml. Bump CHART_VERSION here and `version`/`appVersion`
# in adhar-package.yaml together.
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="3.21.0"

echo "# KATA-CONTAINERS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/infrastructure/kata-containers/generate-manifests.sh'" >>${INSTALL_YAML}

helm pull oci://ghcr.io/kata-containers/kata-deploy-charts/kata-deploy --version ${CHART_VERSION} -d /tmp >/dev/null
rm -rf /tmp/kata-deploy && tar xzf /tmp/kata-deploy-${CHART_VERSION}.tgz -C /tmp
helm template --namespace adhar-system kata-deploy /tmp/kata-deploy -f values.yaml >>${INSTALL_YAML}

# The chart ships a `helm.sh/hook: post-delete` cleanup Job (plus its own
# ServiceAccount/ClusterRole/Binding) that uninstalls the runtime from every
# node when the Helm RELEASE is deleted. Under Argo CD there is no Helm
# release: a hook-annotated object becomes an Argo CD hook and a post-delete
# Job that needs hostPID + privileged is not something a GitOps sync should
# ever run implicitly. Drop every hook object; the DaemonSet's own lifecycle
# (its preStop runs `kata-deploy reset`) already cleans a node up when the
# pod is removed. Everything that remains is the installer: ServiceAccount,
# RBAC and the DaemonSet.
python3 - "${INSTALL_YAML}" <<'PY'
import sys, yaml
path = sys.argv[1]
header = [line for line in open(path) if line.startswith("#")][:2]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
kept, dropped = [], 0
for d in docs:
    ann = (d.get("metadata") or {}).get("annotations") or {}
    if any(k.startswith("helm.sh/hook") for k in ann):
        dropped += 1
        continue
    kept.append(d)
with open(path, "w") as fh:
    fh.writelines(header)
    yaml.safe_dump_all(kept, fh, default_flow_style=False, sort_keys=False)
print(f"kept {len(kept)} object(s), dropped {dropped} helm hook object(s)")
PY
echo "rendered kata-deploy ${CHART_VERSION}"
