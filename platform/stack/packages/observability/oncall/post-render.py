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
"""
import sys

WRONG = "onCallApiUrl: http://helm-testing-oncall-engine:8080"
RIGHT = "onCallApiUrl: http://oncall-engine:8080"
GRAFANA_URL = "grafanaUrl: http://oncall-grafana.adhar-system.svc.cluster.local"


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
    open(path, "w").write(s)


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "manifests/install.yaml")
