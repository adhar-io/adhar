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

# ---------------------------------------------------------------------------
# PostHog's ClickHouse migrations call executable user-defined functions
# (JSONCleanPostHogEventProperties, aggregate_funnel, …) that live in the APP
# image as posthog/user_scripts/ (Go binaries + wrapper scripts + the
# latest_user_defined_function.xml that declares them). Upstream mounts that
# directory into ClickHouse with a compose volume; the chart's
# ClickHouseInstallation mounts nothing, so on a stock clickhouse-server the
# migration dies with
#   Code: 46. DB::Exception: Function with name `JSONCleanPostHogEventProperties` does not exist
# (seen on a fresh cluster, 2026-09-23). An init container copies the directory
# out of the same posthog image the migrate Job runs, into two emptyDirs the
# server reads: user_scripts_path (/var/lib/clickhouse/user_scripts/) and the
# config dir, where the stock config.xml loads *_function.*xml. Taking the files
# from the app image, not a pinned copy, keeps the UDF set in step with the
# migrations that call it.
python3 - ${INSTALL_YAML} <<'PYEOF_UDF'
import sys, yaml
path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]

app_image = None
for d in docs:
    if d.get("kind") == "Job" and d["metadata"]["name"].startswith("posthog-migrate"):
        app_image = d["spec"]["template"]["spec"]["containers"][0]["image"]
if not app_image:
    sys.exit("ERROR: could not read the app image off the posthog-migrate Job")

done = False
for d in docs:
    if d.get("kind") != "ClickHouseInstallation":
        continue
    for pt in d["spec"]["templates"]["podTemplates"]:
        spec = pt["spec"]
        spec.setdefault("volumes", []).extend([
            {"name": "user-scripts", "emptyDir": {}},
            {"name": "udf-config", "emptyDir": {}},
        ])
        spec["initContainers"] = [{
            "name": "install-udfs",
            "image": app_image,
            "command": ["/bin/sh", "-ec",
                "cp -R /code/posthog/user_scripts/. /udf/scripts/\n"
                "cp /code/posthog/user_scripts/latest_user_defined_function.xml /udf/config/user_defined_function.xml\n"
                "chmod -R a+rX /udf/scripts/*\n"
                "echo \"installed $(grep -c '<function>' /udf/config/user_defined_function.xml) UDF definitions\"\n"],
            "volumeMounts": [
                {"name": "user-scripts", "mountPath": "/udf/scripts"},
                {"name": "udf-config", "mountPath": "/udf/config"},
            ],
            "resources": {"requests": {"cpu": "50m", "memory": "64Mi"}, "limits": {"memory": "256Mi"}},
        }]
        for c in spec["containers"]:
            if c["name"] != "clickhouse":
                continue
            c.setdefault("volumeMounts", []).extend([
                {"name": "user-scripts", "mountPath": "/var/lib/clickhouse/user_scripts"},
                {"name": "udf-config", "mountPath": "/etc/clickhouse-server/user_defined_function.xml",
                 "subPath": "user_defined_function.xml"},
            ])
            done = True
if not done:
    sys.exit("ERROR: no clickhouse container found in the ClickHouseInstallation pod template")

with open(path, "w") as fh:
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False)
print(f"UDFs: ClickHouse now loads posthog/user_scripts from {app_image}")
PYEOF_UDF

# ---------------------------------------------------------------------------
# Two things the chart re-randomises or under-sizes on every render, kept
# stable here so a regeneration is reviewable:
#   * the Django SECRET_KEY the chart generates at template time. Regenerating
#     it on a running platform invalidates every session and signed token, so
#     the value already in git is carried forward when one exists.
#   * app probe timeouts. The chart's 2 s/5 s were killing healthy PostHog pods
#     on loaded nodes (see CLAUDE.md "probe timeouts must tolerate a loaded
#     node"); every app probe gets at least 15 s.
python3 - ${INSTALL_YAML} <<'PYEOF_STABLE'
import re, subprocess, sys, yaml
path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]

prev = subprocess.run(["git", "show", "HEAD:" + subprocess.run(["git", "ls-files", "--full-name", path], capture_output=True, text=True).stdout.strip()],
                      capture_output=True, text=True).stdout
m = re.search(r"^\s*posthog-secret:\s*(\S+)\s*$", prev, re.M)
carried = 0
if m:
    for d in docs:
        if d.get("kind") == "Secret" and "posthog-secret" in (d.get("data") or {}):
            d["data"]["posthog-secret"] = m.group(1); carried += 1

raised = 0
def visit(spec):
    global raised
    for c in (spec.get("containers") or []) + (spec.get("initContainers") or []):
        for probe in ("livenessProbe", "readinessProbe", "startupProbe"):
            p = c.get(probe)
            if not p:
                continue
            want = 15
            if p.get("timeoutSeconds", 1) < want:
                p["timeoutSeconds"] = want; raised += 1
for d in docs:
    if d.get("kind") in ("Deployment", "StatefulSet", "Job"):
        visit(d["spec"]["template"]["spec"])

with open(path, "w") as fh:
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False)
print(f"stable: SECRET_KEY carried forward from git ({carried} secret), {raised} probe timeout(s) raised")
PYEOF_STABLE
