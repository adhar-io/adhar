#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="30.46.0"

echo "# POSTHOG INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/posthog/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add posthog https://posthog.github.io/charts-clickhouse/ --force-update
helm repo update posthog   # only this repo: an unrelated stale repo entry must not abort generation
helm template --namespace adhar-system posthog posthog/posthog -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

# PostHog's bin/migrate gates PostHog-Cloud-only steps on DEPLOYMENT. With the
# chart's "helm_local_ha" it runs setup_tasks_oauth (which aborts with
# "You must set OIDC_RSA_PRIVATE_KEY to use RSA algorithm") and
# schedule_temporal_workflows (no Temporal here). "hobby" is upstream's name for
# exactly this deployment shape.
#
# This is done by rewriting the chart's OWN entry rather than adding one through
# values `env`: the chart hardcodes DEPLOYMENT in its pod specs, so a second
# entry made every Deployment fail server-side apply with
# `duplicate entries for key [name="DEPLOYMENT"]`.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import re, sys
path = sys.argv[1]
src = open(path).read()
out, n = re.subn(r'(?m)^(\s*- name: DEPLOYMENT\n\s*value: )helm_local_ha\s*$', r'\1hobby', src)
open(path, 'w').write(out)
print(f"DEPLOYMENT rewritten to 'hobby' in {n} pod spec(s)")
dupes = [m for m in re.findall(r'(?m)^\s*- name: DEPLOYMENT$', out)]
print(f"DEPLOYMENT entries now: {len(dupes)}")
PYEOF
