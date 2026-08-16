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
# Hand-authored Adhar dashboards for components with no reliable published
# grafana.com dashboard (Alloy, external-dns, cosign, tetragon, kubescape,
# pyroscope, spark-operator). These reference datasources by their fixed UIDs
# (prometheus/loki/tempo/mimir/pyroscope) directly, so pin_datasources is a
# no-op on them. A source string "local:<file>" reads dashboards-custom/<file>.
CUSTOM_DIR = os.path.join(HERE, "dashboards-custom")

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
    ("spark-performance", 7890, "Application"),

    # ---------------------------------------------------------------------
    # Kubernetes control plane + detail (kubernetes-mixin family, 12114-12135)
    # Complements the dotdc Views set above with control-plane and
    # per-namespace/pod/workload compute + networking drill-downs.
    # ---------------------------------------------------------------------
    ("k8s-apiserver", 12116, "Platform"),
    ("k8s-kubelet", 12123, "Platform"),
    ("k8s-scheduler", 12130, "Platform"),
    ("k8s-controller-manager", 12122, "Platform"),
    ("k8s-proxy", 12129, "Platform"),
    ("k8s-etcd", 20330, "Platform"),
    ("k8s-persistent-volumes-detail", 12127, "Platform"),
    ("k8s-compute-cluster", 12114, "Platform"),
    ("k8s-compute-namespace-pods", 12117, "Platform"),
    ("k8s-compute-namespace-workloads", 12118, "Platform"),
    ("k8s-compute-node-pods", 12119, "Platform"),
    ("k8s-compute-pod", 12120, "Platform"),
    ("k8s-compute-workload", 12121, "Platform"),
    ("k8s-networking-cluster", 12124, "Platform"),
    ("k8s-networking-namespace-pods", 12125, "Platform"),
    ("k8s-networking-namespace-workload", 12126, "Platform"),
    ("k8s-pods-detail", 12128, "Platform"),
    ("k8s-statefulsets", 12131, "Platform"),
    ("k8s-use-cluster", 12135, "Platform"),
    ("k8s-cluster-autoscaler", 3831, "Platform"),
    ("k8s-capacity", 5228, "Platform"),
    ("k8s-workloads-metrics", 8588, "Platform"),
    ("kubernetes-events", 23100, "Platform"),

    # ---------------------------------------------------------------------
    # LGTM stack internals (Tempo / Loki / Mimir operational mixins)
    # ---------------------------------------------------------------------
    ("tempo-operational", "https://raw.githubusercontent.com/grafana/tempo/main/operations/tempo-mixin-compiled/dashboards/tempo-operational.json", "Platform"),
    ("tempo-reads", "https://raw.githubusercontent.com/grafana/tempo/main/operations/tempo-mixin-compiled/dashboards/tempo-reads.json", "Platform"),
    ("tempo-writes", "https://raw.githubusercontent.com/grafana/tempo/main/operations/tempo-mixin-compiled/dashboards/tempo-writes.json", "Platform"),
    ("tempo-resources", "https://raw.githubusercontent.com/grafana/tempo/main/operations/tempo-mixin-compiled/dashboards/tempo-resources.json", "Platform"),
    ("loki-writes", "https://raw.githubusercontent.com/grafana/loki/main/production/loki-mixin-compiled/dashboards/loki-writes.json", "Platform"),
    ("loki-reads", "https://raw.githubusercontent.com/grafana/loki/main/production/loki-mixin-compiled/dashboards/loki-reads.json", "Platform"),
    ("loki-operational", "https://raw.githubusercontent.com/grafana/loki/main/production/loki-mixin-compiled/dashboards/loki-operational.json", "Platform"),
    ("mimir-overview", 17607, "Platform"),
    ("mimir-overview-resources", 17606, "Platform"),
    ("mimir-overview-networking", 17605, "Platform"),

    # ---------------------------------------------------------------------
    # Security / policy / secrets
    # ---------------------------------------------------------------------
    ("trivy-operator", 17813, "Platform"),
    ("falco", 11914, "Platform"),
    ("external-secrets", "https://raw.githubusercontent.com/external-secrets/external-secrets/main/docs/snippets/dashboard.json", "Platform"),

    # ---------------------------------------------------------------------
    # Platform application services (infra/control-plane runtimes)
    # ---------------------------------------------------------------------
    ("crossplane", 24549, "Platform"),
    ("keda-operator", 22111, "Platform"),
    ("keda-scaled-object", 23951, "Platform"),
    ("velero", 11055, "Platform"),
    ("opencost", 22208, "Platform"),
    ("opencost-namespace", 22252, "Platform"),
    ("argo-workflows", 25113, "Platform"),
    ("argo-workflows-controller", 25112, "Platform"),
    ("tekton", 16559, "Platform"),
    ("dapr-system-services", "https://raw.githubusercontent.com/dapr/dapr/master/grafana/grafana-system-services-dashboard.json", "Platform"),
    ("dapr-sidecar", "https://raw.githubusercontent.com/dapr/dapr/master/grafana/grafana-sidecar-dashboard.json", "Platform"),
    ("jupyterhub", 5849, "Platform"),
    ("trino-cluster", 20208, "Platform"),
    ("trino-pod", 20207, "Platform"),
    ("opensearch", 15178, "Platform"),
    ("victoria-metrics", 10229, "Platform"),
    ("victoria-metrics-cluster", 11176, "Platform"),
    ("mongodb", 2583, "Platform"),
    ("mysql", 7362, "Platform"),

    # ---------------------------------------------------------------------
    # Hand-authored Adhar dashboards (no reliable published grafana.com source).
    # Source files live in dashboards-custom/; they reference datasource UIDs
    # (prometheus/loki/tempo/pyroscope) directly.
    # ---------------------------------------------------------------------
    ("alloy", "local:alloy.json", "Platform"),
    ("external-dns", "local:external-dns.json", "Platform"),
    ("cosign", "local:cosign.json", "Platform"),
    ("tetragon", "local:tetragon.json", "Platform"),
    ("kubescape", "local:kubescape.json", "Platform"),
    ("pyroscope", "local:pyroscope.json", "Platform"),
    ("spark-operator", "local:spark-operator.json", "Platform"),
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
    print(f"\n{len(ok)} dashboards written to {OUT_DIR}, {len(failed)} failed")
    if failed:
        for name, gid, err in failed:
            print(f"  - {name} (id {gid}): {err}", file=sys.stderr)


if __name__ == "__main__":
    main()
