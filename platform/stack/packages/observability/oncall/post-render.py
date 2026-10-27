#!/usr/bin/env python3
"""Edits the oncall chart (1.16.5) gets wrong, re-applied after every
`helm template` by generate-manifests.sh. Text-level and idempotent.

The chart's plugin-provisioning template hard-codes its own test fixture's
release name: the bundled Grafana is provisioned with
`onCallApiUrl: http://helm-testing-oncall-engine:8080`, a Service that exists in
no real install. The plugin then reports "Plugin not connected" (`lookup
helm-testing-oncall-engine … no such host`) and OnCall cannot be used at all
(2026-10-24). The engine Service is `<release>-engine`; the release is `oncall`.
The ConfigMap's own `helm-testing-…` name is harmless (the Deployment mounts it
by that same name) and is left alone.

And `grafanaUrl`: without it the plugin's backend calls Grafana's own API at
Grafana's PUBLIC root_url, which sits behind the oncall oauth2-proxy — so the
"connect" step received Keycloak's sign-in page and failed with `failed to
parse JSON response: invalid character '<'`. The plugin reads
`jsonData.grafanaUrl` first ("jsonData.grafanaUrl is not set" otherwise), so it
is pointed at the in-cluster Service, the same address the engine uses.

And the migration Job's NAME. values.yaml makes it an Argo CD Sync hook
(`migrate.annotations`, no TTL); outside the chart's own hook mode the chart
names it after the render time (`oncall-engine-migrate-2026-09-12-07-04-21`).
BeforeHookCreation finds the previous run BY NAME, so a name that changes on
every render would leave each old Job behind for good. The pod template's copy
of the name goes too, so a re-render diffs clean.
"""
import re
import sys

WRONG = "onCallApiUrl: http://helm-testing-oncall-engine:8080"
RIGHT = "onCallApiUrl: http://oncall-engine:8080"
GRAFANA_URL = "grafanaUrl: http://oncall-grafana.adhar-system.svc.cluster.local"

MIGRATE_JOB = "oncall-engine-migrate"
MIGRATE_RENDER_TIME_NAME = re.compile(MIGRATE_JOB + r"-\d{4}(?:-\d{2}){5}\b")
MIGRATE_SOURCE = "# Source: oncall/templates/engine/job-migrate.yaml"


def main(path):
    s = open(path).read()
    if WRONG in s:
        if s.count(WRONG) != 1:
            sys.exit(f"post-render: the plugin provisioning in {path} changed shape; re-check the chart")
        s = s.replace(WRONG, RIGHT)
    if RIGHT not in s:
        sys.exit(f"post-render: no onCallApiUrl in {path}; re-check the chart")
    if GRAFANA_URL not in s:
        line = next(l for l in s.splitlines() if l.strip() == RIGHT)
        indent = line[: len(line) - len(line.lstrip())]
        s = s.replace(line, line + "\n" + indent + GRAFANA_URL, 1)
    s = MIGRATE_RENDER_TIME_NAME.sub(MIGRATE_JOB, s)
    check_migrate_job(s, path)
    open(path, "w").write(s)


def check_migrate_job(s, path):
    """Fail the render rather than ship the migration Job as a tracked resource
    again: with a TTL and no hook it re-runs every few minutes forever."""
    docs = [d for d in s.split("\n---\n") if MIGRATE_SOURCE in d]
    if len(docs) != 1:
        sys.exit(f"post-render: expected one migrate Job in {path}, found {len(docs)}; re-check the chart")
    job = docs[0]
    for want in (
        f"  name: {MIGRATE_JOB}\n",
        "argocd.argoproj.io/hook: Sync\n",
        "argocd.argoproj.io/hook-delete-policy: BeforeHookCreation\n",
    ):
        if want not in job:
            sys.exit(f"post-render: the migrate Job lacks {want.strip()!r}; check `migrate:` in values.yaml")
    if "ttlSecondsAfterFinished" in job or "helm.sh/hook" in job:
        sys.exit("post-render: the migrate Job carries a TTL or a Helm hook again; check `migrate:` in values.yaml")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "manifests/install.yaml")
