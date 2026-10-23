"""Shared pieces of the aggregated Adhar dashboards (one small set per Grafana folder).

  * UIDS: every dashboard this package ships, by role. Links between dashboards are
    written against these names, and the successors of retired dashboards keep the
    retired uid where people had bookmarked it (the pod dashboard every table links
    to, the cluster overview, capacity).
  * RETIRED: the uids of dashboards that were folded into another one -> where their
    content lives now. Vendored panels (the kubernetes-mixin) still carry links to the
    old uids; embed() rewrites them through this map so no link points at nothing.
  * Generic component sections: pods (ready / restarts / CPU / memory against their
    requests and limits), the Go process, and controller-runtime reconcilers. Most
    platform services are operators written with controller-runtime, so the same
    four panels answer "is it working, is it keeping up, what does it cost" for each.
"""
import json
import os
import re

from dashlib import (AMBER, BLUE, GREEN, GREY, LOKI, NEUTRAL, OK_BAD, OK_WARN_BAD, PCT, PROM, RED, E, T,
                     bargauge, heatmap, logs, ov_color, ov_dashed, ov_negative, pod_link, stat, steps,
                     table, timeline, ts, vmap)

HERE = os.path.dirname(os.path.abspath(__file__))
MIXIN = os.path.join(HERE, "dashboards-custom", "kubernetes-mixin")

# ---------------------------------------------------------------------------------------------
# Dashboard identities
# ---------------------------------------------------------------------------------------------

UIDS = {
    "platform": "adhar-platform-overview",
    # Kubernetes
    "k8s-overview": "adhar-k8s-overview",
    "k8s-nodes": "Hzyg1Dzmk",                                  # was Kubernetes / Capacity & Requests
    "k8s-workloads": "6581e46e4e5c7ba40a07646395ef7b23",       # was Compute Resources / Pod (every pod link)
    "k8s-control-plane": "adhar-k8s-control-plane",
    "k8s-storage": "adhar-k8s-storage",
    # Networking
    "net-cilium": "adhar-net-cilium",
    "net-edge": "adhar-net-edge",
    "net-traffic": "ff635a025bcfea7bc3dd4f508990a3e9",         # was Networking / Cluster
    # Data
    "data-postgres": "cloudnative-pg",
    "data-valkey": "adhar-valkey",
    "data-kafka": "adhar-kafka-cluster",
    "data-rabbitmq": "Kn5xm-gZk",
    "data-analytics": "adhar-data-analytics",
    # Delivery
    "gitops": "adhar-gitops",
    "ci": "adhar-ci",
    "dev-platform": "adhar-dev-platform",
    # Security
    "policy": "adhar-policy",
    "runtime-security": "adhar-runtime-security",
    "identity": "adhar-identity",
    # Observability
    "telemetry": "adhar-telemetry",
    "lgtm": "adhar-lgtm",
    "cost": "adhar-cost",
    "runtimes": "adhar-runtimes",
    # AI / Resilience
    "ai": "adhar-ai",
    "resilience": "adhar-resilience",
}

RETIRED = {
    "efa86fd1d0c121a26444b636a3f509a8": UIDS["k8s-overview"],     # Compute Resources / Cluster
    "85a562078cdf77779eaa1add43ccec1e": UIDS["k8s-workloads"],    # Compute Resources / Namespace (Pods)
    "a87fb0d919ec0ea5f6543124e16c42a5": UIDS["k8s-workloads"],    # Compute Resources / Namespace (Workloads)
    "a164a7f0339f99e89cea5cb47e9be617": UIDS["k8s-workloads"],    # Compute Resources / Workload
    "200ac8fdbfbb74b39aff88118e4d1c2c": UIDS["k8s-nodes"],        # Compute Resources / Node (Pods)
    "058020e04168bfdea0c52269cb699df2": UIDS["k8s-nodes"],        # Compute Resources / Nodes Overview
    "3e97d1d02672cdd0861f4c97c64f89b2": UIDS["k8s-nodes"],        # Node Exporter / USE Method / Cluster
    "919b92a8e8041bd567af9edab12c840c": UIDS["k8s-storage"],      # Persistent Volumes
    "09ec8aa1e996d6ffcd6817bbaff4db1b": UIDS["k8s-control-plane"],
    "72e0e05bef5099e5f049b05fdc429ed4": UIDS["k8s-control-plane"],
    "2e6b6a3b4bddf1427b3a55aa1311c656": UIDS["k8s-control-plane"],
    "3138fa155d5915769fbded898ac09fd9": UIDS["k8s-control-plane"],
    "8b7a8b326d7a6f1f04244066368c67af": UIDS["net-traffic"],      # Networking / Namespace (Pods)
    "bbb2a765a623ae38130206c7d94a160f": UIDS["net-traffic"],      # Networking / Namespace (Workload)
    "728bf77cc1166d2f3133bf25846876cc": UIDS["net-traffic"],      # Networking / Workload (never vendored)
    "7a18067ce943a40ae25454675c19ff5c": UIDS["net-traffic"],      # Networking / Pod (never vendored)
    "jwPKIsniz": UIDS["data-kafka"],                              # Kafka / Topics & Consumer Lag
    "j7MCpqZ7k": UIDS["data-rabbitmq"],                           # RabbitMQ-Stream
}

FOLDER_TAG = {
    "Kubernetes": "adhar-kubernetes", "Networking": "adhar-networking", "Data": "adhar-data",
    "Delivery": "adhar-delivery", "Security": "adhar-security", "Observability": "adhar-observability",
    "AI": "adhar-ai", "Resilience": "adhar-resilience",
}


def tags(folder, *extra):
    return ["adhar", FOLDER_TAG[folder]] + list(extra)


def links(folder, *extra):
    """The link bar every dashboard carries: the platform front page, the other
    dashboards of the same folder, every Adhar dashboard, plus direct links passed
    in as (title, uid-key-or-uid)."""
    out = [
        {"title": "Adhar Platform", "type": "link", "url": f"/d/{UIDS['platform']}", "icon": "dashboard",
         "asDropdown": False, "includeVars": False, "keepTime": True, "targetBlank": False, "tags": []},
        {"title": folder, "type": "dashboards", "tags": [FOLDER_TAG[folder]], "asDropdown": True,
         "includeVars": False, "keepTime": True, "targetBlank": False, "icon": "external link"},
        {"title": "All dashboards", "type": "dashboards", "tags": ["adhar"], "asDropdown": True,
         "includeVars": False, "keepTime": True, "targetBlank": False, "icon": "external link"},
    ]
    for title, uid in extra:
        out.append({"title": title, "type": "link", "url": f"/d/{UIDS.get(uid, uid)}", "asDropdown": False,
                    "includeVars": False, "keepTime": True, "targetBlank": False, "icon": "dashboard",
                    "tags": []})
    return out


# ---------------------------------------------------------------------------------------------
# kubernetes-mixin (vendored from prometheus-operator/kube-prometheus)
# ---------------------------------------------------------------------------------------------

# Panel titles shared across the mixin -> what they mean. Anything not listed gets a
# description built from its query.
MIXIN_DESC = {
    "CPU Utilisation": "CPU actually used as a share of allocatable CPU.",
    "CPU Requests Commitment": "CPU requested by pods as a share of allocatable CPU — what the scheduler sees.",
    "CPU Limits Commitment": "CPU limits as a share of allocatable CPU (over 100% = overcommitted limits).",
    "Memory Utilisation": "Memory actually used as a share of allocatable memory.",
    "Memory Requests Commitment": "Memory requested by pods as a share of allocatable memory — what the scheduler sees.",
    "Memory Limits Commitment": "Memory limits as a share of allocatable memory (over 100% = overcommitted limits).",
    "CPU Usage": "CPU cores used (rate of container_cpu_usage_seconds_total).",
    "CPU usage": "CPU cores used.",
    "CPU Quota": "Per-object CPU usage against requests and limits.",
    "CPU Throttling": "Share of CFS periods in which the container was throttled at its CPU limit.",
    "Memory Usage": "Container memory (working set unless stated otherwise).",
    "Memory Usage (w/o cache)": "Working set: memory the kernel cannot reclaim — what OOM decisions use.",
    "Memory Usage (w/cache)": "Memory including page cache.",
    "Memory Usage (WSS)": "Working set size per container.",
    "Memory Quota": "Per-object memory usage against requests and limits.",
    "Memory": "Memory usage.",
    "Memory Requests by Namespace": "Memory requested per namespace.",
    "Current Network Usage": "Current receive / transmit bandwidth and packet rates.",
    "Current Status": "Current receive / transmit bandwidth and packet rates.",
    "Receive Bandwidth": "Bytes received per second.",
    "Transmit Bandwidth": "Bytes transmitted per second.",
    "Rate of Received Packets": "Packets received per second.",
    "Rate of Transmitted Packets": "Packets transmitted per second.",
    "Rate of Received Packets Dropped": "Received packets dropped per second.",
    "Rate of Transmitted Packets Dropped": "Transmitted packets dropped per second.",
    "Current Storage IO": "Current read / write IOPS and throughput.",
    "IOPS(Reads+Writes)": "Read + write operations per second.",
    "ThroughPut(Read+Write)": "Read + write bytes per second.",
    "IOPS (Pod)": "Read and write operations per second for the pod.",
    "IOPS (Containers)": "Read + write operations per second per container.",
    "ThroughPut (Pod)": "Read and write bytes per second for the pod.",
    "ThroughPut (Containers)": "Read + write bytes per second per container.",
    "Up": "Number of scrape targets up for this component.",
    "Work Queue Add Rate": "Items added to the controllers' work queues per second.",
    "Work Queue Depth": "Items waiting in the work queues. Steady growth means the controller cannot keep up.",
    "Work Queue Latency": "99th percentile time items wait in the queue.",
    "Kube API Request Rate": "Requests this component sends to the API server, by status class.",
    "Post Request Latency 99th Quantile": "99th percentile latency of POST requests to the API server.",
    "Get Request Latency 99th Quantile": "99th percentile latency of GET requests to the API server.",
    "Goroutines": "Number of goroutines per instance — a steady climb suggests a leak.",
    "Volume Space Usage": "Used vs free space of the volume (CSI volumes only — node-local volumes report nothing).",
    "Volume inodes Usage": "Used vs free inodes of the volume.",
    "Running Kubelets": "Kubelets reporting.",
    "Running Pods": "Pods the kubelet runs.",
    "Running Containers": "Containers by state.",
    "Actual Volume Count": "Volumes mounted, by plugin.",
    "Desired Volume Count": "Volumes the kubelet should mount, by plugin.",
    "Config Error Count": "Kubelet configuration errors.",
    "Operation Rate": "Kubelet runtime operations per second.",
    "Operation Error Rate": "Failed kubelet runtime operations per second.",
    "Operation Duration 99th quantile": "99th percentile duration of kubelet runtime operations.",
    "Pod Start Rate": "Pods started per second.",
    "Pod Start Duration": "99th percentile time to start a pod.",
    "Storage Operation Rate": "Volume operations per second.",
    "Storage Operation Error Rate": "Failed volume operations per second.",
    "Storage Operation Duration 99th quantile": "99th percentile volume operation duration.",
    "Cgroup manager operation rate": "Cgroup manager operations per second.",
    "Cgroup manager 99th quantile": "99th percentile cgroup manager operation duration.",
    "PLEG relist rate": "Pod lifecycle event generator relists per second.",
    "PLEG relist interval": "99th percentile interval between PLEG relists (over 3m marks the node NotReady).",
    "PLEG relist duration": "99th percentile PLEG relist duration.",
    "RPC rate": "Kubelet requests to the API server by status class.",
    "Request duration 99th quantile": "99th percentile latency of requests to the API server, by verb.",
    "Scheduling Rate": "Scheduling attempts per second.",
    "Scheduling latency 99th Quantile": "99th percentile end-to-end scheduling latency.",
}


def mixin_desc(panel):
    title = panel.get("title", "")
    if title in MIXIN_DESC:
        return MIXIN_DESC[title]
    metrics = []
    for t in panel.get("targets", []) or []:
        for m in re.findall(r"[a-zA-Z_:][a-zA-Z0-9_:]*(?=\{|\[)", t.get("expr", "")):
            if m not in metrics and m not in ("sum", "rate", "irate", "max", "min", "avg", "by", "count"):
                metrics.append(m)
    src = (" Source: " + ", ".join(metrics[:4]) + ".") if metrics else ""
    return f"{title} (kubernetes-mixin).{src}"


def _fix_node_pod_drilldown(d):
    """Upstream bug: the per-pod tables on Node (Pods) group by pod only, yet their
    drill-down link passes var-namespace=$namespace — a variable that dashboard does
    not have — so every click opened the Pod dashboard on an empty namespace. Group
    by (namespace, pod), merge on both, and pass the row's own namespace."""
    for p in d.get("panels", []):
        trs = p.get("transformations") or []
        if not trs or trs[0].get("id") != "joinByField" or trs[0].get("options", {}).get("byField") != "pod":
            continue
        for t in p.get("targets", []):
            t["expr"] = t["expr"].replace(") by (pod)", ") by (namespace, pod)")
        trs[0] = {"id": "merge", "options": {}}
        for tr in trs:
            if tr.get("id") == "organize":
                tr["options"].setdefault("renameByName", {})["namespace"] = "Namespace"
                tr["options"].setdefault("indexByName", {}).update({"Namespace": 0, "Pod": 1})
        for o in p.get("fieldConfig", {}).get("overrides", []):
            for prop in o.get("properties", []):
                if prop.get("id") == "links":
                    for link in prop["value"]:
                        link["url"] = link["url"].replace("var-namespace=$namespace",
                                                          "var-namespace=${__data.fields.Namespace}")


def mixin(src):
    """A vendored kubernetes-mixin dashboard, with the node drill-down fixed."""
    with open(os.path.join(MIXIN, src + ".json")) as f:
        d = json.load(f)
    if src == "k8s-resources-node":
        _fix_node_pod_drilldown(d)
    return d


def embed_mixin(d, src, row=None, var_map=None, skip=()):
    d.embed(mixin(src), row=row, var_map=var_map, skip=skip, describe=mixin_desc, uid_map=RETIRED)


# The mixin's `cluster` picker: this platform runs one cluster per Prometheus and its
# series carry no `cluster` label, so it stays hidden and resolves to "" (which matches
# series without the label).
def cluster_var():
    return {"name": "cluster", "label": "Cluster", "type": "query", "datasource": PROM, "hide": 2,
            "definition": 'label_values(up{job="kube-state-metrics"}, cluster)',
            "query": {"query": 'label_values(up{job="kube-state-metrics"}, cluster)',
                      "refId": "PrometheusVariableQueryEditor-VariableQuery"},
            "refresh": 2, "regex": "", "sort": 1, "multi": False, "includeAll": False, "options": [],
            "current": {}}


# ---------------------------------------------------------------------------------------------
# Generic component sections
# ---------------------------------------------------------------------------------------------

KSM = 'job="kube-state-metrics"'
CAD = 'job="kubelet", metrics_path="/metrics/cadvisor"'
SYS = 'namespace="adhar-system"'


def up_stat(title, desc, job_re, extra=""):
    """'n / m up' for a set of scrape jobs; red when any target is down, grey 'not scraped'
    when nothing is."""
    sel = f'job=~"{job_re}"{extra}'
    return stat(title, desc + " Shows targets up / total; red when any target is down.",
                [T(f'sum(up{{{sel}}})', "up"), T(f'count(up{{{sel}}})', "total")],
                color_mode="value", text_mode="value_and_name", no_value="not scraped")


def down_stat(title, desc, job_re, extra=""):
    sel = f'job=~"{job_re}"{extra}'
    return stat(title, desc + " Counts scrape targets that are down; 'not scraped' means no target exists.",
                [f'count(up{{{sel}}} == 0) or (count(up{{{sel}}}) * 0)'], th=OK_BAD, no_value="not scraped")


def pods_ready_stat(title, desc, pod_re, ns=SYS):
    """Ready pods / pods for a component, from kube-state-metrics (works for every
    component, scraped or not)."""
    sel = f'{KSM}, {ns}, pod=~"{pod_re}"'
    return stat(title, desc + " Ready pods out of the component's pods (kube-state-metrics).",
                [T(f'sum(kube_pod_status_ready{{{sel}, condition="true"}} * on (namespace, pod) group_left () '
                   f'max by (namespace, pod) (kube_pod_status_phase{{{KSM}, phase="Running"}} == 1))', "ready"),
                 T(f'count(max by (namespace, pod) (kube_pod_status_phase{{{sel}, phase=~"Pending|Running"}} == 1))', "pods")],
                color_mode="value", text_mode="value_and_name", no_value="not deployed")


def pods_table(title, desc, pod_re, ns=SYS, h=None):
    """One row per pod of a component: phase-ready, restarts, CPU/memory against
    requests and limits, node. The pod name opens the Workloads & Pods dashboard."""
    sel = f'{ns}, pod=~"{pod_re}"'
    return table(
        title, desc + " One row per pod: readiness, restarts (total and 24h), CPU and memory against their "
        "requests and limits, and the node it runs on. Click a pod for its full resource view.",
        [f'max by (namespace, pod, node) (kube_pod_info{{{KSM}, {sel}}})',
         f'max by (namespace, pod) (kube_pod_status_ready{{{KSM}, {sel}, condition="true"}})',
         f'sum by (namespace, pod) (kube_pod_container_status_restarts_total{{{KSM}, {sel}}})',
         f'sum by (namespace, pod) (increase(kube_pod_container_status_restarts_total{{{KSM}, {sel}}}[24h]))',
         f'sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{{{CAD}, {sel}, container!=""}}[5m]))',
         f'sum by (namespace, pod) (kube_pod_container_resource_requests{{{KSM}, {sel}, resource="cpu"}})',
         f'sum by (namespace, pod) (container_memory_working_set_bytes{{{CAD}, {sel}, container!=""}})',
         f'100 * sum by (namespace, pod) (container_memory_working_set_bytes{{{CAD}, {sel}, container!=""}}) / '
         f'sum by (namespace, pod) (kube_pod_container_resource_limits{{{KSM}, {sel}, resource="memory"}})'],
        [("namespace", "Namespace", None, None),
         ("pod", "Pod", None, {"links": pod_link(UIDS["k8s-workloads"])}),
         ("node", "Node", None, None),
         ("Value #B", "Ready", None, {"mappings": vmap((0, "NOT READY", RED), (1, "Ready", GREEN)), "cell": "color-background"}),
         ("Value #C", "Restarts", "short", None),
         ("Value #D", "Restarts 24h", "short", {"cell": "color-background", "th": OK_WARN_BAD, "decimals": 0}),
         ("Value #E", "CPU", "cores", {"decimals": 3}),
         ("Value #F", "CPU request", "cores", {"decimals": 2}),
         ("Value #G", "Memory", "bytes", None),
         ("Value #H", "Mem % of limit", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100})],
        sort=("Pod", False), hide=("Value #A", "created_by_kind", "created_by_name", "host_ip", "pod_ip", "uid",
                                   "priority_class", "host_network"))


def pods_resources(pod_re, label="{{pod}}", ns=SYS):
    """(cpu, memory, restarts) time series for a component's pods, for d.line."""
    sel = f'{ns}, pod=~"{pod_re}"'
    return [
        ts("CPU by pod", "CPU cores used per pod, with the CPU request dashed. Above the request the pod competes "
           "for spare node CPU and is first to be throttled under contention.",
           [T(f'sum by (pod) (rate(container_cpu_usage_seconds_total{{{CAD}, {sel}, container!=""}}[$__rate_interval]))', label),
            T(f'sum by (pod) (kube_pod_container_resource_requests{{{KSM}, {sel}, resource="cpu"}})', label + " request")],
           unit="cores", overrides=[ov_dashed(".* request")]),
        ts("Memory by pod", "Working-set memory per pod with the memory limit dashed red. Reaching the limit is an "
           "OOM kill.",
           [T(f'sum by (pod) (container_memory_working_set_bytes{{{CAD}, {sel}, container!=""}})', label),
            T(f'sum by (pod) (kube_pod_container_resource_limits{{{KSM}, {sel}, resource="memory"}})', label + " limit")],
           unit="bytes", overrides=[ov_dashed(".* limit", RED)]),
        ts("Restarts & OOM kills", "Container restarts per interval (bars); a pod whose last termination was "
           "OOMKilled is drawn red.",
           [T(f'sum by (pod) (increase(kube_pod_container_status_restarts_total{{{KSM}, {sel}}}[$__rate_interval]))', label),
            T(f'sum by (pod) (kube_pod_container_status_last_terminated_reason{{{KSM}, {sel}, reason="OOMKilled"}})', label + " OOMKilled")],
           draw="bars", decimals=0, overrides=[ov_color(".*OOMKilled", RED)]),
    ]


def go_process(job_re, label="{{job}}"):
    """(cpu, memory, goroutines) for Go services scraped under job_re, for d.line."""
    sel = f'job=~"{job_re}"'
    return [
        ts("Process CPU", "CPU seconds per second the service's own process uses (process_cpu_seconds_total).",
           [T(f'sum by (job) (rate(process_cpu_seconds_total{{{sel}}}[$__rate_interval]))', label)], unit="cores"),
        ts("Process memory (RSS) & heap", "Resident memory of the process and the live Go heap. RSS far above heap "
           "is memory the runtime has not returned to the OS yet.",
           [T(f'sum by (job) (process_resident_memory_bytes{{{sel}}})', label + " rss"),
            T(f'sum by (job) (go_memstats_heap_inuse_bytes{{{sel}}})', label + " heap")],
           unit="bytes", overrides=[ov_dashed(".* heap")]),
        ts("Goroutines", "Goroutines per service. A line that only ever climbs is a leak (usually a watch or a "
           "connection that is never closed).",
           [T(f'sum by (job) (go_goroutines{{{sel}}})', label)], decimals=0),
    ]


def controller_runtime(job_re, what="controller"):
    """(reconciles by result, errors, queue depth, reconcile latency) for controller-runtime
    based operators scraped under job_re, for d.line."""
    sel = f'job=~"{job_re}"'
    return [
        ts("Reconciles / s by controller", f"Reconciliations per second per {what} controller, by result. "
           "'error' results are drawn red; a steady error rate means objects that never converge.",
           [T(f'sum by (controller, result) (rate(controller_runtime_reconcile_total{{{sel}}}[$__rate_interval]))', "{{controller}} {{result}}")],
           unit="ops", overrides=[ov_color(".* error", RED)]),
        ts("Reconcile errors / s", "controller_runtime_reconcile_errors_total per controller: reconciles that "
           "returned an error and were requeued with backoff.",
           [T(f'sum by (controller) (rate(controller_runtime_reconcile_errors_total{{{sel}}}[$__rate_interval])) > 0', "{{controller}}")],
           unit="ops", th=steps((GREEN, None), (RED, 0.01))),
        ts("Work queue depth", "Items waiting per work queue. Growth that does not drain means the controller "
           "cannot keep up (CPU throttling, slow API server, too few workers).",
           [T(f'sum by (name) (workqueue_depth{{{sel}}}) > 0', "{{name}}")], decimals=0),
        ts("Reconcile time p99", "99th percentile time one reconcile takes, per controller.",
           [T(f'histogram_quantile(0.99, sum by (controller, le) (rate(controller_runtime_reconcile_time_seconds_bucket{{{sel}}}[$__rate_interval])))', "{{controller}}")],
           unit="s"),
    ]


def component_logs(title, desc, selector, pattern='(?i)(error|fail|panic|fatal|warn)'):
    return logs(title, desc + " Filtered to warnings and errors; open Explore for the full stream.",
                f'{{{selector}}} |~ "{pattern}"')


__all__ = [n for n in dir() if not n.startswith("_")] + ["_fix_node_pod_drilldown"]
