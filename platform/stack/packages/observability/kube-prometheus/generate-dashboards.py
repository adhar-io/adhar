#!/usr/bin/env python3
"""Download curated Grafana dashboards from grafana.com and emit them as
in-repo sidecar ConfigMaps for the two-folder (Platform / Application) layout.

Each dashboard's datasource inputs are pinned to the platform's provisioned
datasource UIDs (prometheus / loki / tempo / mimir / pyroscope) so the JSON is
self-contained and provisions without prompting. Output goes to manifests/ as
one ConfigMap per dashboard (grafana_dashboard=1 label, grafana_folder
annotation). Runtime needs no egress — the JSON is committed to the repo.
"""
import json
import os
import sys
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_DIR = os.path.join(HERE, "manifests")

# name | source | folder
#   source is either an int grafana.com dashboard id, or a str direct-download
#   URL (for projects that publish their dashboard JSON outside grafana.com).
# Platform = hardware/OS, Kubernetes, networking, data stores, LGTM stack,
# security, and platform application services (ArgoCD/Gitea/Keycloak/Vault/...).
# Application = application-runtime dashboards (Spring Boot / Micrometer / JVM).
DASHBOARDS = [
    ("node-exporter-full", 1860, "Platform"),
    ("kubernetes-global", 15757, "Platform"),
    ("kubernetes-namespaces", 15758, "Platform"),
    ("kubernetes-nodes", 15759, "Platform"),
    ("kubernetes-pods", 15760, "Platform"),
    ("kube-state-metrics", 13332, "Platform"),
    ("cilium-agent", 16611, "Platform"),
    ("cilium-operator", 16612, "Platform"),
    ("cilium-hubble", 16613, "Platform"),
    ("coredns", 14981, "Platform"),
    ("cloudnativepg", 20417, "Platform"),
    # Networking / DNS / probing
    ("cilium-network-monitoring", 24056, "Platform"),
    ("blackbox-exporter", 13659, "Platform"),
    # Cluster / nodes (node-exporter-driven variants)
    ("kubernetes-nodes-classic", 8171, "Platform"),
    ("k8s-cluster-overview", 15661, "Platform"),
    # Alerting / LGTM
    ("alertmanager", 9578, "Platform"),
    ("loki-stack", 14055, "Platform"),
    # Data stores / messaging (platform services)
    ("redis-overview", 18345, "Platform"),
    ("redis-ha", 11835, "Platform"),
    ("rabbitmq-overview", 10991, "Platform"),
    ("rabbitmq-stream", 14798, "Platform"),
    # Platform application services
    ("keycloak-metrics", 10441, "Platform"),
    ("minio", "https://raw.githubusercontent.com/minio/minio/master/docs/metrics/prometheus/grafana/minio-dashboard.json", "Platform"),
    ("prometheus", 19105, "Platform"),
    ("loki", 13407, "Platform"),
    ("cert-manager", 11001, "Platform"),
    ("kyverno", 15987, "Platform"),
    ("argocd", 14584, "Platform"),
    ("gitea", 13192, "Platform"),
    ("keycloak", 17878, "Platform"),
    ("vault", 12904, "Platform"),
    ("harbor", 14075, "Platform"),
    ("kafka-cluster", 762, "Platform"),
    ("kafka-exporter", 7589, "Platform"),
    ("redis", 763, "Platform"),
    ("spring-boot-statistics", 10280, "Application"),
    ("spring-boot-apm", 12900, "Application"),
    ("jvm-micrometer", 4701, "Application"),
    ("kafka-streams", 13966, "Application"),
    ("nodejs", 11159, "Application"),
]

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
    """source: int grafana.com dashboard id, or str direct-download URL."""
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
    print(f"\n{len(ok)} dashboards written to {OUT_DIR}, {len(failed)} failed")
    if failed:
        for name, gid, err in failed:
            print(f"  - {name} (id {gid}): {err}", file=sys.stderr)


if __name__ == "__main__":
    main()
