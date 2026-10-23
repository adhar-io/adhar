#!/usr/bin/env python3
"""Vendoring and packaging helpers for Grafana dashboards.

Every dashboard this package ships is BUILT, not downloaded: build-service-dashboards.py
(one small set per Grafana folder, dash_<folder>.py) and build-platform-dashboard.py
(the "Adhar Platform" front page). Until 2026-10-23 this script downloaded ~90
community dashboards from grafana.com; most showed "No data" here (written for other
exporters / label sets), several were duplicates of one another, and a dozen served
packages disabled in every profile, so they were replaced by the aggregated set.

What remains here:
  * configmap_yaml(): the sidecar ConfigMap shape every built dashboard is wrapped in
    (grafana_dashboard=1 label, grafana_folder annotation);
  * fetch() / pin_datasources(): to vendor a published dashboard as a SOURCE (e.g. into
    dashboards-custom/vendored/) for a builder to embed — never to ship it as is.

DASHBOARDS is deliberately empty; running this script writes nothing.
"""
import json
import os
import sys
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_DIR = os.path.join(HERE, "manifests")
# "local:<name>.json" entries whose folder is not "Platform" are written by
# build-service-dashboards.py (CloudNativePG, Valkey/Redis, Kafka, RabbitMQ,
# Kubernetes overview/capacity, and the kubernetes-mixin set vendored from
# kube-prometheus). Edit and re-run THAT script; this one only re-packages them.
# Hand-authored Adhar dashboards for components with no reliable published
# grafana.com dashboard (Alloy, external-dns, cosign, tetragon, kubescape,
# pyroscope, spark-operator). These reference datasources by their fixed UIDs
# (prometheus/loki/tempo/mimir/pyroscope) directly, so pin_datasources is a
# no-op on them. A source string "local:<file>" reads dashboards-custom/<file>.
CUSTOM_DIR = os.path.join(HERE, "dashboards-custom")

# name | source | folder — dashboards downloaded and shipped as is. Empty on purpose
# (see the module docstring); add a builder in dash_<folder>.py instead.
DASHBOARDS = []

# grafana.com input pluginId -> our provisioned datasource UID.
PLUGIN_UID = {
    "prometheus": "prometheus",
    "loki": "loki",
    "tempo": "tempo",
    "grafana-pyroscope-datasource": "pyroscope",
    "phlare": "pyroscope",
}
# Fallback for common input NAMES when __inputs is absent.
NAME_UID = {
    "DS_PROMETHEUS": "prometheus",
    "DS_LOKI": "loki",
    "DS_TEMPO": "tempo",
    "DS_MIMIR": "mimir",
    "DS_THANOS": "prometheus",
    "DS_PYROSCOPE": "pyroscope",
}


def fetch(source):
    """source: int grafana.com dashboard id, str "local:<file>" for an in-repo
    hand-authored dashboard, or str direct-download URL."""
    if isinstance(source, str) and source.startswith("local:"):
        with open(os.path.join(CUSTOM_DIR, source[len("local:"):]), encoding="utf-8") as f:
            return json.load(f)
    if isinstance(source, int):
        url = f"https://grafana.com/api/dashboards/{source}/revisions/latest/download"
    else:
        url = source
    req = urllib.request.Request(url, headers={"User-Agent": "adhar-dashboards"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode("utf-8"))


def pin_datasources(dash):
    """Replace ${DS_*} input placeholders with fixed datasource UIDs and strip
    the __inputs/__requires import metadata so the dashboard provisions cleanly."""
    uid_by_input = {}
    for inp in dash.get("__inputs", []) or []:
        if inp.get("type") == "datasource":
            uid_by_input[inp["name"]] = PLUGIN_UID.get(inp.get("pluginId", ""), "prometheus")
    s = json.dumps(dash)
    for name, uid in uid_by_input.items():
        s = s.replace("${%s}" % name, uid)
    # Fallback for placeholders not declared in __inputs.
    for name, uid in NAME_UID.items():
        s = s.replace("${%s}" % name, uid)
    dash = json.loads(s)
    dash.pop("__inputs", None)
    dash.pop("__requires", None)
    dash["id"] = None
    return dash


def configmap_yaml(name, folder, dash):
    body = json.dumps(dash, indent=2, ensure_ascii=False)
    indented = "\n".join("    " + line for line in body.splitlines())
    return (
        "apiVersion: v1\n"
        "kind: ConfigMap\n"
        "metadata:\n"
        f"  name: adhar-dashboard-{name}\n"
        "  namespace: adhar-system\n"
        "  labels:\n"
        '    grafana_dashboard: "1"\n'
        f"    app.kubernetes.io/part-of: adhar-observability\n"
        "  annotations:\n"
        f'    grafana_folder: "{folder}"\n'
        "data:\n"
        f"  {name}.json: |\n"
        f"{indented}\n"
    )


def main():
    os.makedirs(OUT_DIR, exist_ok=True)
    ok, failed = [], []
    for name, gid, folder in DASHBOARDS:
        try:
            dash = pin_datasources(fetch(gid))
            path = os.path.join(OUT_DIR, f"dashboard-{name}.yaml")
            with open(path, "w") as f:
                f.write(configmap_yaml(name, folder, dash))
            title = dash.get("title", "?")
            ok.append((name, gid, folder, title))
            print(f"OK    {folder:11} {name:24} (id {gid}) -> {title}")
        except Exception as e:  # noqa: BLE001 - report and continue
            failed.append((name, gid, str(e)))
            print(f"FAIL  {name:24} (id {gid}): {e}", file=sys.stderr)
    if not DASHBOARDS:
        print("nothing to download: every dashboard is built by build-service-dashboards.py / build-platform-dashboard.py")
    print(f"\n{len(ok)} dashboards written to {OUT_DIR}, {len(failed)} failed")
    if failed:
        for name, gid, err in failed:
            print(f"  - {name} (id {gid}): {err}", file=sys.stderr)


if __name__ == "__main__":
    main()
