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

# Wave-order ClickHouse -> prepare -> migrate.
#
# The chart gives the ClickHouseInstallation and the date-stamped migrate Job NO
# waves and NO hooks, so ArgoCD applies them together and `bin/migrate` races
# ClickHouse's readiness. Worse, PostHog's migration reads `system.crash_log`,
# which ClickHouse does not materialise until crash logging is configured, so
# migrate fails:
#
#   Unknown table expression identifier 'system.crash_log'
#
# ...hits its backoff limit, the sync fails, and posthog-web/worker time out
# waiting on a database that was never migrated.
#
# `clickhouse-prepare.yaml` creates those system tables, but it was a PostSync
# hook -- and PostSync only runs once the sync SUCCEEDS. It could therefore never
# run on the very failure it exists to fix: a deadlock where the repair is gated
# on the thing being repaired. It is a wave-1 ordinary resource now (see that
# file), which is why the migrate Job has to land after it.
#
# The Job name carries a template timestamp, so match on the prefix rather than
# a literal name.
python3 - ${INSTALL_YAML} <<'PYEOF_WAVES'
import sys, yaml

path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]

# The application Deployments -- the ones whose init container blocks until the
# database is migrated. Moving migrate to a later wave without moving THESE is a
# wave inversion: they sit at wave 0 stuck on `Init:1/2`, wave 0 therefore never
# reports healthy, ArgoCD never advances to the migration they are waiting for,
# and the sync fails forever on
#   Deployment "posthog-web" exceeded its progress deadline
#
# pgbouncer, redis and clickhouse-operator are deliberately NOT here: the
# migration itself needs them, so they belong in wave 0 with the databases.
APP_DEPLOYMENTS = {"posthog-web", "posthog-worker-default", "posthog-events"}

touched = []
for d in docs:
    kind, name = d.get("kind"), (d.get("metadata") or {}).get("name", "")
    wave = None
    if kind == "ClickHouseInstallation":
        wave = "0"          # the database itself, first
    elif kind == "Job" and name.startswith("posthog-migrate"):
        wave = "2"          # after posthog-clickhouse-prepare (wave 1)
    elif kind == "Deployment" and name in APP_DEPLOYMENTS:
        wave = "3"          # after the migration they block on
    if wave is None:
        continue
    ann = (d["metadata"].get("annotations") or {})
    ann["argocd.argoproj.io/sync-wave"] = wave
    d["metadata"]["annotations"] = ann
    touched.append(f"{kind}/{name}@{wave}")

if not any(t.startswith("Job/posthog-migrate") for t in touched):
    sys.exit("ERROR: no posthog-migrate Job found -- did the chart rename it?")

with open(path, "w") as fh:
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False)
print("wave-ordered: " + ", ".join(touched))
PYEOF_WAVES
