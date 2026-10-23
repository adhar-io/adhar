"""Kubernetes folder: the cluster overview and nodes & capacity (hand-authored; the
other Kubernetes dashboards live in dash_kubernetes.py)."""
from dashlib import *  # noqa: F401,F403
from dash_common import UIDS, links

OVERVIEW_UID = UIDS["k8s-overview"]
CAPACITY_UID = UIDS["k8s-nodes"]
K8S_TAGS = ["adhar", "adhar-kubernetes", "kubernetes"]


def dash_links(*extra):
    return links("Kubernetes", *extra)

# =============================================================================================
# Kubernetes — cluster overview
# =============================================================================================

KSM_J = 'job="kube-state-metrics"'
CAD = 'job="kubelet", metrics_path="/metrics/cadvisor"'


def k8s_overview():
    N = 'node=~"$node"'
    NS = 'namespace=~"$namespace"'
    K = KSM_J
    d = Dash(OVERVIEW_UID, "Kubernetes / Cluster Overview",
             "The cluster at a glance: node health, requests vs allocatable vs real usage (the platform's recurring "
             "capacity problem is nodes full of memory REQUESTS while real usage is far lower), unhealthy pods with "
             "their reasons, rollouts, namespaces and quotas, storage, the control plane and the network. kube-state-"
             "metrics series are pinned to job=\"kube-state-metrics\" because OpenCost re-exports some of them.",
             K8S_TAGS,
             [ds_var(),
              qvar("node", "Node", f'label_values(kube_node_info{{{K}}}, node)'),
              qvar("namespace", "Namespace", f'label_values(kube_namespace_status_phase{{{K}}}, namespace)')],
             links=dash_links(("Capacity & requests", CAPACITY_UID),
                              ("Compute resources / cluster", "efa86fd1d0c121a26444b636a3f509a8")))

    def alloc(res):
        return f'sum(kube_node_status_allocatable{{{K}, {N}, resource="{res}"}})'

    def req(res):
        return (f'sum(kube_pod_container_resource_requests{{{K}, {N}, resource="{res}"}} '
                f'* on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase=~"Pending|Running"}} == 1))')

    d.row("Cluster at a glance")
    d.line(4,
           (stat("Nodes ready", "Ready nodes out of all nodes.",
                 [T(f'sum(kube_node_status_condition{{{K}, {N}, condition="Ready", status="true"}})', "ready"),
                  T(f'count(kube_node_info{{{K}, {N}}})', "total")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("Nodes with problems", "Nodes not Ready, cordoned, or reporting Memory/Disk/PID pressure.",
                 [f'count((sum by (node) (kube_node_status_condition{{{K}, {N}, condition="Ready", status!="true"}}) > 0) or '
                  f'(sum by (node) (kube_node_status_condition{{{K}, {N}, condition=~"MemoryPressure|DiskPressure|PIDPressure", status="true"}}) > 0) or '
                  f'(sum by (node) (kube_node_spec_unschedulable{{{K}, {N}}}) > 0)) or vector(0)'], th=OK_BAD), 3),
           (stat("Pods running", "Pods in phase Running.",
                 [f'sum(kube_pod_status_phase{{{K}, {NS}, phase="Running"}})'], color_mode="value", graph="area"), 3),
           (stat("Pods pending", "Pods waiting to be scheduled or to start. Persistently pending pods usually lack "
                 "requestable capacity (see the Capacity dashboard) or a volume.",
                 [f'sum(kube_pod_status_phase{{{K}, {NS}, phase="Pending"}})'], th=OK_WARN_BAD, graph="area"), 3),
           (stat("Pods failed / evicted", "Pods in phase Failed, including evicted ones still listed.",
                 [f'sum(kube_pod_status_phase{{{K}, {NS}, phase="Failed"}})'], th=OK_WARN_BAD), 3),
           (stat("Crash-looping containers", "Containers currently waiting in CrashLoopBackOff, ImagePullBackOff, "
                 "ErrImagePull or CreateContainerConfigError.",
                 [f'sum(kube_pod_container_status_waiting_reason{{{K}, {NS}, reason=~"CrashLoopBackOff|ImagePullBackOff|ErrImagePull|CreateContainerConfigError|CreateContainerError"}}) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Restarts (1h)", "Container restarts across the cluster in the last hour.",
                 [f'sum(increase(kube_pod_container_status_restarts_total{{{K}, {NS}}}[1h]))'], decimals=0,
                 th=steps((GREEN, None), (AMBER, 5), (RED, 30))), 3),
           (stat("OOMKilled (1h)", "Containers whose last termination was an OOMKill and which restarted in the last "
                 "hour — the working set hit the memory limit.",
                 [f'count((max by (namespace, pod, container) (kube_pod_container_status_last_terminated_reason{{{K}, {NS}, reason="OOMKilled"}}) == 1) '
                  f'and on (namespace, pod, container) (increase(kube_pod_container_status_restarts_total{{{K}, {NS}}}[1h]) > 0)) or vector(0)'],
                 th=OK_BAD), 3))
    d.line(6,
           (gauge("CPU requested", "CPU requested by running and pending pods as a share of allocatable CPU. The "
                  "scheduler refuses new pods once a node's requests reach 100%, whatever the real usage.",
                  [f'100 * {req("cpu")} / {alloc("cpu")}'], th=PCT_REQ), 4),
           (gauge("CPU used", "Real CPU usage (cAdvisor) as a share of allocatable CPU.",
                  [f'100 * sum(rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[$__rate_interval])) / {alloc("cpu")}']), 4),
           (gauge("Memory requested", "Memory requested by running and pending pods as a share of allocatable memory.",
                  [f'100 * {req("memory")} / {alloc("memory")}'], th=PCT_REQ), 4),
           (gauge("Memory used", "Real container working set as a share of allocatable memory. The gap to "
                  "'Memory requested' is memory reserved but unused.",
                  [f'100 * sum(container_memory_working_set_bytes{{{CAD}, container!="", {N}}}) / {alloc("memory")}']), 4),
           (gauge("Pods vs capacity", "Scheduled pods as a share of the nodes' pod capacity (maxPods).",
                  [f'100 * sum(kubelet_running_pods{{{N}}}) / {alloc("pods")}']), 4),
           (gauge("Control plane pods ready", "Ready share of the static control-plane pods (etcd, kube-apiserver, "
                  "kube-controller-manager, kube-scheduler) and the cloud-controller-manager in kube-system.",
                  [f'100 * sum(kube_pod_status_ready{{{K}, namespace="kube-system", condition="true", pod=~"(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*"}}) / '
                   f'count(kube_pod_info{{{K}, namespace="kube-system", pod=~"(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*"}})'],
                  th=steps((RED, None), (AMBER, 99), (GREEN, 100))), 4))

    d.row("Requests vs real usage per node")
    node_req = lambda res: (f'sum by (node) (kube_pod_container_resource_requests{{{K}, {N}, resource="{res}"}} '
                            f'* on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase=~"Pending|Running"}} == 1))')
    node_alloc = lambda res: f'sum by (node) (kube_node_status_allocatable{{{K}, {N}, resource="{res}"}})'
    d.line(9,
           (bargauge("Memory REQUESTED % of allocatable", "Per node. This is what the scheduler sees: a node at ~100% "
                     "accepts no pod with a memory request, even when its real usage (right) is low.",
                     [T(f'100 * {node_req("memory")} / {node_alloc("memory")}', "{{node}}")], th=PCT_REQ, max=110), 6),
           (bargauge("Memory USED % of allocatable", "Per node: container working set (cAdvisor) against allocatable.",
                     [T(f'100 * sum by (node) (container_memory_working_set_bytes{{{CAD}, container!="", {N}}}) / {node_alloc("memory")}', "{{node}}")]), 6),
           (bargauge("CPU REQUESTED % of allocatable", "Per node CPU requests of running and pending pods against "
                     "allocatable CPU.", [T(f'100 * {node_req("cpu")} / {node_alloc("cpu")}', "{{node}}")], th=PCT_REQ, max=110), 6),
           (bargauge("CPU USED % of allocatable", "Per node real CPU usage against allocatable.",
                     [T(f'100 * sum by (node) (rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[$__rate_interval])) / {node_alloc("cpu")}', "{{node}}")]), 6))
    d.line(9, (table(
        "Nodes", "Capacity per node: allocatable, requested and used for CPU and memory, pods, and conditions. "
        "'Req %' columns turn amber at 85% and red at 95% — the point where the scheduler stops placing pods.",
        [f'max by (node, kernel_version, kubelet_version, internal_ip) (kube_node_info{{{K}, {N}}})',
         f'max by (node) (kube_node_status_condition{{{K}, {N}, condition="Ready", status="true"}})',
         node_alloc("cpu"),
         f'100 * {node_req("cpu")} / {node_alloc("cpu")}',
         f'100 * sum by (node) (rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[5m])) / {node_alloc("cpu")}',
         node_alloc("memory"),
         f'100 * {node_req("memory")} / {node_alloc("memory")}',
         f'100 * sum by (node) (container_memory_working_set_bytes{{{CAD}, container!="", {N}}}) / {node_alloc("memory")}',
         f'sum by (node) (kubelet_running_pods{{{N}}})',
         f'sum by (node) (kube_node_status_condition{{{K}, {N}, condition=~"MemoryPressure|DiskPressure|PIDPressure", status="true"}})',
         f'max by (node) (kube_node_spec_unschedulable{{{K}, {N}}})'],
        [("node", "Node", None, {"links": [{"title": "Open node in Nodes & Capacity", "url": f"/d/{CAPACITY_UID}?var-datasource=${{datasource}}&var-node=${{__data.fields.node}}&${{__url_time_range}}"}]}),
         ("internal_ip", "IP", None, None), ("kubelet_version", "Kubelet", None, None),
         ("Value #B", "Ready", None, {"mappings": vmap((0, "NOT READY", RED), (1, "Ready", GREEN)), "cell": "color-background"}),
         ("Value #C", "CPU alloc", "cores", {"decimals": 2}),
         ("Value #D", "CPU req %", "percent", {"cell": "color-background", "th": PCT_REQ, "decimals": 0}),
         ("Value #E", "CPU used %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #F", "Mem alloc", "bytes", None),
         ("Value #G", "Mem req %", "percent", {"cell": "color-background", "th": PCT_REQ, "decimals": 0}),
         ("Value #H", "Mem used %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #I", "Pods", "short", None),
         ("Value #J", "Pressure", None, {"mappings": vmap((0, "none", GREEN)), "cell": "color-text", "th": OK_BAD}),
         ("Value #K", "Cordoned", None, {"mappings": vmap((0, "-", GREY), (1, "CORDONED", AMBER))})],
        sort=("Mem req %", True), hide=("Value #A", "kernel_version")), 24))
    d.line(8,
           (ts("Memory: allocatable vs requested vs used", "Cluster totals over time. Requests (amber) are what "
               "fills nodes; usage (green) is what the workloads actually consume.",
               [T(alloc("memory"), "allocatable"), T(req("memory"), "requested"),
                T(f'sum(container_memory_working_set_bytes{{{CAD}, container!="", {N}}})', "used (working set)")],
               unit="bytes", fill=10, overrides=[ov_dashed("allocatable", RED), ov_color("requested", "orange"), ov_color("used.*", GREEN)]), 12),
           (ts("CPU: allocatable vs requested vs used", "Cluster totals over time.",
               [T(alloc("cpu"), "allocatable"), T(req("cpu"), "requested"),
                T(f'sum(rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[$__rate_interval]))', "used")],
               unit="cores", fill=10, overrides=[ov_dashed("allocatable", RED), ov_color("requested", "orange"), ov_color("used", GREEN)]), 12))

    d.row("Unhealthy pods")
    d.line(9,
           (table("Pods not running", "Pods in phase Pending, Failed or Unknown, with the reason a container is "
                  "waiting when there is one.",
                  [f'max by (namespace, pod, phase) (kube_pod_status_phase{{{K}, {NS}, phase=~"Pending|Failed|Unknown"}} == 1)',
                   f'max by (namespace, pod, reason) (kube_pod_container_status_waiting_reason{{{K}, {NS}}} == 1)',
                   f'max by (namespace, pod) (time() - kube_pod_created{{{K}, {NS}}}) and on (namespace, pod) (kube_pod_status_phase{{{K}, {NS}, phase=~"Pending|Failed|Unknown"}} == 1)'],
                  [("namespace", "Namespace", None, None), ("pod", "Pod", None, {"links": pod_link()}),
                   ("phase", "Phase", None, {"cell": "color-text", "mappings": vmap(("Pending", "Pending", AMBER), ("Failed", "Failed", RED), ("Unknown", "Unknown", RED))}),
                   ("reason", "Waiting reason", None, None),
                   ("Value #C", "Age", "s", None)],
                  hide=("Value #A", "Value #B"), sort=("Age", True)), 12),
           (table("Top restarting containers (24h)", "Containers with the most restarts in the last 24 hours and the "
                  "reason of their last termination (OOMKilled = memory limit, Error = non-zero exit).",
                  [f'topk(20, sum by (namespace, pod, container) (increase(kube_pod_container_status_restarts_total{{{K}, {NS}}}[24h])) > 0)',
                   f'max by (namespace, pod, container, reason) (kube_pod_container_status_last_terminated_reason{{{K}, {NS}}} == 1)'],
                  [("namespace", "Namespace", None, None), ("pod", "Pod", None, {"links": pod_link()}),
                   ("container", "Container", None, None),
                   ("Value #A", "Restarts", "short", {"cell": "color-background", "th": steps((GREEN, None), (AMBER, 3), (RED, 10)), "decimals": 0}),
                   ("reason", "Last termination", None, {"cell": "color-text", "mappings": vmap(("OOMKilled", "OOMKilled", RED), ("Error", "Error", AMBER), ("Completed", "Completed", GREEN))})],
                  hide=("Value #B",), sort=("Restarts", True)), 12))
    d.line(8,
           (ts("Pods by phase", "Pods per phase over time.",
               [T(f'sum by (phase) (kube_pod_status_phase{{{K}, {NS}}})', "{{phase}}")], stack=True,
               overrides=[ov_color("Running", GREEN), ov_color("Pending", "orange"), ov_color("Failed", RED),
                          ov_color("Succeeded", BLUE)]), 8),
           (ts("Waiting containers by reason", "Containers that cannot start, by reason.",
               [T(f'sum by (reason) (kube_pod_container_status_waiting_reason{{{K}, {NS}}})', "{{reason}}")],
               draw="bars", decimals=0), 8),
           (ts("Restarts & OOM kills", "Container restarts and OOM events per interval across the cluster.",
               [T(f'sum(increase(kube_pod_container_status_restarts_total{{{K}, {NS}}}[$__rate_interval]))', "restarts"),
                T(f'sum(increase(container_oom_events_total{{{CAD}, {NS}}}[$__rate_interval]))', "OOM events")],
               draw="bars", decimals=0, overrides=[ov_color("OOM.*", RED)]), 8))

    d.row("Rollouts")
    d.line(8,
           (table("Deployments not fully available", "Deployments whose available replicas are below the desired "
                  "count, or with an update still rolling out.",
                  [f'max by (namespace, deployment) (kube_deployment_spec_replicas{{{K}, {NS}}})',
                   f'max by (namespace, deployment) (kube_deployment_status_replicas_available{{{K}, {NS}}})',
                   f'max by (namespace, deployment) (kube_deployment_status_replicas_unavailable{{{K}, {NS}}}) > 0 or '
                   f'max by (namespace, deployment) (kube_deployment_spec_replicas{{{K}, {NS}}} - kube_deployment_status_replicas_updated{{{K}, {NS}}}) > 0'],
                  [("namespace", "Namespace", None, None), ("deployment", "Deployment", None, None),
                   ("Value #A", "Desired", "short", None), ("Value #B", "Available", "short", None),
                   ("Value #C", "Unavailable / not updated", "short", {"cell": "color-background", "th": OK_BAD})],
                  sort=("Unavailable / not updated", True)), 8),
           (table("StatefulSets not ready", "StatefulSets with fewer ready replicas than desired.",
                  [f'max by (namespace, statefulset) (kube_statefulset_replicas{{{K}, {NS}}})',
                   f'max by (namespace, statefulset) (kube_statefulset_status_replicas_ready{{{K}, {NS}}})',
                   f'max by (namespace, statefulset) (kube_statefulset_replicas{{{K}, {NS}}} - kube_statefulset_status_replicas_ready{{{K}, {NS}}}) > 0'],
                  [("namespace", "Namespace", None, None), ("statefulset", "StatefulSet", None, None),
                   ("Value #A", "Desired", "short", None), ("Value #B", "Ready", "short", None),
                   ("Value #C", "Not ready", "short", {"cell": "color-background", "th": OK_BAD})],
                  sort=("Not ready", True)), 8),
           (table("DaemonSets & Jobs with problems", "DaemonSets with unavailable or misscheduled pods, and Jobs "
                  "that failed.",
                  [f'label_replace(max by (namespace, daemonset) (kube_daemonset_status_number_unavailable{{{K}, {NS}}} + kube_daemonset_status_number_misscheduled{{{K}, {NS}}}) > 0, "kind", "DaemonSet", "", "")',
                   f'label_replace(label_replace(max by (namespace, job_name) (kube_job_status_failed{{{K}, {NS}}}) > 0, "daemonset", "$1", "job_name", "(.*)"), "kind", "Job", "", "")'],
                  [("namespace", "Namespace", None, None), ("kind", "Kind", None, None), ("daemonset", "Name", None, None),
                   ("Value #A", "Unavailable", "short", {"cell": "color-background", "th": OK_BAD}),
                   ("Value #B", "Failed pods", "short", {"cell": "color-background", "th": OK_BAD})]), 8))

    d.row("Namespaces & quotas")
    d.line(9,
           (table("Namespaces: requests vs usage", "Per namespace: memory and CPU requested by running pods against "
                  "what they actually use. Low efficiency = reserved capacity nobody uses, which is what fills nodes.",
                  [f'sum by (namespace) (kube_pod_container_resource_requests{{{K}, {NS}, resource="memory"}} * on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase="Running"}} == 1))',
                   f'sum by (namespace) (container_memory_working_set_bytes{{{CAD}, container!="", {NS}}})',
                   f'100 * sum by (namespace) (container_memory_working_set_bytes{{{CAD}, container!="", {NS}}}) / sum by (namespace) (kube_pod_container_resource_requests{{{K}, {NS}, resource="memory"}} * on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase="Running"}} == 1))',
                   f'sum by (namespace) (kube_pod_container_resource_requests{{{K}, {NS}, resource="cpu"}} * on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase="Running"}} == 1))',
                   f'sum by (namespace) (rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {NS}}}[5m]))',
                   f'sum by (namespace) (kube_pod_status_phase{{{K}, {NS}, phase="Running"}})'],
                  [("namespace", "Namespace", None, None),
                   ("Value #A", "Mem requested", "bytes", None), ("Value #B", "Mem used", "bytes", None),
                   ("Value #C", "Mem efficiency", "percent", {"cell": "color-text", "th": steps((RED, None), (AMBER, 30), (GREEN, 60)), "decimals": 0}),
                   ("Value #D", "CPU requested", "cores", {"decimals": 2}), ("Value #E", "CPU used", "cores", {"decimals": 2}),
                   ("Value #F", "Pods", "short", None)],
                  sort=("Mem requested", True)), 12),
           (table("Resource quota usage", "Used vs hard for every ResourceQuota (the environment namespaces carry one). "
                  "At 100% new pods in that namespace are rejected at admission.",
                  [f'max by (namespace, resourcequota, resource) (kube_resourcequota{{{K}, {NS}, type="used"}})',
                   f'max by (namespace, resourcequota, resource) (kube_resourcequota{{{K}, {NS}, type="hard"}})',
                   f'100 * max by (namespace, resourcequota, resource) (kube_resourcequota{{{K}, {NS}, type="used"}}) / (max by (namespace, resourcequota, resource) (kube_resourcequota{{{K}, {NS}, type="hard"}}) > 0)'],
                  [("namespace", "Namespace", None, None), ("resourcequota", "Quota", None, None),
                   ("resource", "Resource", None, None), ("Value #A", "Used", "short", None),
                   ("Value #B", "Hard", "short", None),
                   ("Value #C", "Used %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100})],
                  sort=("Used %", True)), 12))

    d.row("Storage")
    d.line(8,
           (stat("PVCs by phase", "PersistentVolumeClaims by phase. A Pending claim keeps its pod Pending too.",
                 [T(f'sum by (phase) (kube_persistentvolumeclaim_status_phase{{{K}, {NS}}})', "{{phase}}")],
                 color_mode="value", text_mode="value_and_name", orientation="vertical"), 4),
           (table("Pending PVCs", "Claims not yet bound, with their storage class.",
                  [f'max by (namespace, persistentvolumeclaim) (kube_persistentvolumeclaim_status_phase{{{K}, {NS}, phase="Pending"}} == 1)',
                   f'max by (namespace, persistentvolumeclaim, storageclass) (kube_persistentvolumeclaim_info{{{K}, {NS}}})'],
                  [("namespace", "Namespace", None, None), ("persistentvolumeclaim", "Claim", None, None),
                   ("storageclass", "Storage class", None, None)], hide=("Value #A", "Value #B")), 8),
           (bargauge("Volume usage (CSI volumes)", "Used share of each volume that reports kubelet volume statistics. "
                     "Node-local adhar-local volumes (the default class) report none, so most platform volumes are "
                     "not listed — their services' own dashboards estimate usage instead.",
                     [T(f'topk(15, 100 * max by (namespace, persistentvolumeclaim) (kubelet_volume_stats_used_bytes{{{NS}}} / kubelet_volume_stats_capacity_bytes{{{NS}}}))', "{{namespace}}/{{persistentvolumeclaim}}")]), 6),
           (bargauge("Requested storage by namespace", "Sum of PVC storage requests per namespace (top 10).",
                     [T(f'topk(10, sum by (namespace) (kube_persistentvolumeclaim_resource_requests_storage_bytes{{{K}, {NS}}}))', "{{namespace}}")],
                     unit="bytes", th=NEUTRAL, min=0, max=None), 6))

    d.row("Control plane")
    d.line(8,
           (ts("API server requests by code", "Requests per second to kube-apiserver by response class.",
               [T('sum by (code_class) (label_replace(rate(apiserver_request_total{job="apiserver"}[$__rate_interval]), "code_class", "${1}xx", "code", "([0-9]).."))', "{{code_class}}")],
               unit="reqps", stack=True, overrides=[ov_color("2xx", GREEN), ov_color("4xx", "orange"), ov_color("5xx", RED)]), 8),
           (ts("API server latency p99 by verb", "99th percentile request latency (excluding WATCH/CONNECT). Seconds "
               "here precede leader-election failures of the controllers.",
               [T('histogram_quantile(0.99, sum by (verb, le) (rate(apiserver_request_duration_seconds_bucket{job="apiserver", verb!~"WATCH|CONNECT"}[$__rate_interval])))', "{{verb}}")],
               unit="s", th=steps((GREEN, None), (AMBER, 1), (RED, 4))), 8),
           (ts("etcd request latency p99 (from the API server)", "99th percentile latency of the API server's etcd "
               "requests by operation. etcd itself is not scraped on kubeadm nodes (it listens on localhost).",
               [T('histogram_quantile(0.99, sum by (operation, le) (rate(etcd_request_duration_seconds_bucket{job="apiserver"}[$__rate_interval])))', "{{operation}}")],
               unit="s"), 8))
    d.line(8,
           (ts("API server in-flight & queued", "Requests being served (mutating / read-only) and requests queued by "
               "API Priority and Fairness.",
               [T('sum by (request_kind) (apiserver_current_inflight_requests{job="apiserver"})', "{{request_kind}} in flight"),
                T('sum by (priority_level) (apiserver_flowcontrol_current_inqueue_requests{job="apiserver"})', "{{priority_level}} queued")]), 8),
           (table("Control-plane pods", "Static control-plane pods and the cloud-controller-manager: ready state and "
                  "restarts. Restarts of kube-controller-manager / kube-scheduler usually mean lost leader-election "
                  "leases on an overloaded control plane.",
                  [f'max by (pod, node) (kube_pod_info{{{K}, namespace="kube-system", pod=~"(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*"}})',
                   f'max by (pod) (kube_pod_status_ready{{{K}, namespace="kube-system", condition="true", pod=~"(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*"}})',
                   f'sum by (pod) (kube_pod_container_status_restarts_total{{{K}, namespace="kube-system", pod=~"(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*"}})',
                   f'sum by (pod) (increase(kube_pod_container_status_restarts_total{{{K}, namespace="kube-system", pod=~"(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*"}}[24h]))'],
                  [("pod", "Pod", None, None), ("node", "Node", None, None),
                   ("Value #B", "Ready", None, {"mappings": vmap((0, "NOT READY", RED), (1, "Ready", GREEN)), "cell": "color-background"}),
                   ("Value #C", "Restarts", "short", None),
                   ("Value #D", "Restarts 24h", "short", {"cell": "color-background", "th": OK_WARN_BAD, "decimals": 0})],
                  hide=("Value #A",)), 10),
           (ts("Control-plane CPU", "CPU used by the control-plane components. A two-core control plane pegged here "
               "is what makes controllers lose their leases.",
               [T(f'sum by (pod) (rate(container_cpu_usage_seconds_total{{{CAD}, namespace="kube-system", container!="", pod=~"(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*"}}[$__rate_interval]))', "{{pod}}")],
               unit="cores", stack=True), 6))

    d.row("Network & nodes")
    d.line(8,
           (ts("Node network", "Bytes received (above) and transmitted (below) per node on physical/cloud interfaces.",
               [T(f'sum by (instance) (rate(node_network_receive_bytes_total{{device!~"lo|veth.*|lxc.*|cilium.*|docker.*"}}[$__rate_interval]))', "{{instance}} rx"),
                T(f'sum by (instance) (rate(node_network_transmit_bytes_total{{device!~"lo|veth.*|lxc.*|cilium.*|docker.*"}}[$__rate_interval]))', "{{instance}} tx")],
               unit="Bps", overrides=[ov_negative(".* tx")]), 8),
           (ts("Top namespaces by traffic", "Pod network receive + transmit per namespace (top 10).",
               [T(f'topk(10, sum by (namespace) (rate(container_network_receive_bytes_total{{{CAD}, {NS}}}[$__rate_interval]) + rate(container_network_transmit_bytes_total{{{CAD}, {NS}}}[$__rate_interval])))', "{{namespace}}")],
               unit="Bps"), 8),
           (ts("Node memory pressure (PSI)", "Share of time tasks on each node were stalled waiting for memory "
               "(pressure stall information). Sustained values mean the node is really short of memory, not just "
               "of requests.",
               [T('rate(node_pressure_memory_waiting_seconds_total[$__rate_interval])', "{{instance}}")],
               unit="percentunit", th=steps((GREEN, None), (AMBER, 0.05), (RED, 0.2))), 8))
    return d


# =============================================================================================
# Kubernetes — capacity & requests
# =============================================================================================

def k8s_capacity():
    N = 'node=~"$node"'
    NS = 'namespace=~"$namespace"'
    K = KSM_J
    RUN = f'max by (namespace, pod) (kube_pod_status_phase{{{K}, phase=~"Pending|Running"}} == 1)'

    def req_by(by, res, sel=N):
        return (f'sum by ({by}) (kube_pod_container_resource_requests{{{K}, {sel}, resource="{res}"}} '
                f'* on (namespace, pod) group_left () {RUN})')

    def alloc_by(by, res):
        return f'sum by ({by}) (kube_node_status_allocatable{{{K}, {N}, resource="{res}"}})'

    # Nodes ordinary pods can land on: not tainted NoSchedule/NoExecute (the
    # control plane) and not cordoned.
    SCHED = (f'unless on (node) (max by (node) (kube_node_spec_taint{{{K}, effect=~"NoSchedule|NoExecute"}}) '
             f'or max by (node) (kube_node_spec_unschedulable{{{K}}} == 1))')

    def free_by_node(res):
        return f'({alloc_by("node", res)} - ({req_by("node", res)} or {alloc_by("node", res)} * 0)) {SCHED}'

    d = Dash(CAPACITY_UID, "Kubernetes / Capacity & Requests",
             "Where the cluster's capacity goes. The scheduler places pods by REQUESTS, not by usage, so a cluster "
             "can refuse pods while its nodes are mostly idle. This dashboard puts allocatable, requested and used "
             "side by side per node, per namespace and per workload, shows the largest pod that still fits, and "
             "lists what is pending because nothing fits.",
             K8S_TAGS + ["capacity"],
             [ds_var(),
              qvar("node", "Node", f'label_values(kube_node_info{{{K}}}, node)'),
              qvar("namespace", "Namespace", f'label_values(kube_namespace_status_phase{{{K}}}, namespace)')],
             links=dash_links(("Cluster overview", OVERVIEW_UID)))

    d.row("Capacity at a glance")
    d.line(4,
           (stat("Memory allocatable", "Allocatable memory on the selected nodes.",
                 [f'sum(kube_node_status_allocatable{{{K}, {N}, resource="memory"}})'], unit="bytes", color_mode="value"), 3),
           (stat("Memory requested", "Memory requested by running + pending pods on the selected nodes.",
                 [f'sum({req_by("node", "memory")})'], unit="bytes", color_mode="value"), 3),
           (stat("Memory used", "Real working set on the selected nodes.",
                 [f'sum(container_memory_working_set_bytes{{{CAD}, container!="", {N}}})'], unit="bytes", color_mode="value"), 3),
           (stat("Largest free memory", "Largest unrequested memory on any single schedulable node (control-plane, tainted and "
                 "cordoned nodes excluded) = the biggest memory request a new pod can have and still be scheduled.",
                 [f'max({free_by_node("memory")})'],
                 unit="bytes", th=steps((RED, None), (AMBER, 536870912), (GREEN, 2147483648))), 3),
           (stat("CPU allocatable", "Allocatable CPU on the selected nodes.",
                 [f'sum(kube_node_status_allocatable{{{K}, {N}, resource="cpu"}})'], unit="cores", color_mode="value", decimals=1), 3),
           (stat("CPU requested", "CPU requested by running + pending pods.",
                 [f'sum({req_by("node", "cpu")})'], unit="cores", color_mode="value", decimals=1), 3),
           (stat("CPU used", "Real CPU usage.",
                 [f'sum(rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[$__rate_interval]))'],
                 unit="cores", color_mode="value", decimals=1), 3),
           (stat("Largest free CPU", "Largest unrequested CPU on any single schedulable node (control-plane, tainted and "
                 "cordoned nodes excluded) = the biggest CPU request a new pod can have and still be scheduled.",
                 [f'max({free_by_node("cpu")})'],
                 unit="cores", decimals=2, th=steps((RED, None), (AMBER, 0.25), (GREEN, 1))), 3))
    d.line(4,
           (stat("Pods pending", "Pods not yet running.",
                 [f'sum(kube_pod_status_phase{{{K}, {NS}, phase="Pending"}})'], th=OK_WARN_BAD, graph="area"), 4),
           (stat("Unschedulable pods", "Pods the scheduler has marked Unschedulable (no node fits their requests, "
                 "affinity, taints or volume topology).",
                 [f'sum(kube_pod_status_unschedulable{{{K}, {NS}}}) or vector(0)'], th=OK_BAD, graph="area"), 4),
           (stat("Pods without memory request", "Running pods with no memory request on any container (BestEffort "
                 "for memory). They are invisible to the scheduler and the first to be OOM-killed under pressure.",
                 [f'count(max by (namespace, pod) (kube_pod_status_phase{{{K}, {NS}, phase="Running"}} == 1) unless on (namespace, pod) '
                  f'max by (namespace, pod) (kube_pod_container_resource_requests{{{K}, {NS}, resource="memory"}})) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 20))), 4),
           (stat("Memory request efficiency", "Used / requested memory across running pods. 40% means 60% of what "
                 "pods reserve sits idle.",
                 [f'100 * sum(container_memory_working_set_bytes{{{CAD}, container!="", {N}}}) / sum({req_by("node", "memory")})'],
                 unit="percent", th=steps((RED, None), (AMBER, 30), (GREEN, 60)), decimals=0), 4),
           (stat("CPU request efficiency", "Used / requested CPU.",
                 [f'100 * sum(rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[$__rate_interval])) / sum({req_by("node", "cpu")})'],
                 unit="percent", th=steps((RED, None), (AMBER, 20), (GREEN, 50)), decimals=0), 4),
           (stat("Nodes", "Nodes in the selection (watch this move as the node autoscaler adds and removes workers).",
                 [f'count(kube_node_info{{{K}, {N}}})'], color_mode="value", graph="area"), 4))

    d.row("Per node over time")
    d.line(8,
           (ts("Memory requested % per node", "Each node's memory requests as a share of allocatable. Lines at the "
               "red dashed band (95%) are nodes the scheduler can no longer use for memory-requesting pods.",
               [T(f'100 * {req_by("node", "memory")} / {alloc_by("node", "memory")}', "{{node}}")],
               unit="percent", th=PCT_REQ, soft_max=100), 12),
           (ts("Memory used % per node", "Each node's real container working set as a share of allocatable.",
               [T(f'100 * sum by (node) (container_memory_working_set_bytes{{{CAD}, container!="", {N}}}) / {alloc_by("node", "memory")}', "{{node}}")],
               unit="percent", th=PCT, soft_max=100), 12))
    d.line(8,
           (ts("CPU requested % per node", "Each node's CPU requests as a share of allocatable.",
               [T(f'100 * {req_by("node", "cpu")} / {alloc_by("node", "cpu")}', "{{node}}")],
               unit="percent", th=PCT_REQ, soft_max=100), 12),
           (ts("CPU used % per node", "Each node's real CPU usage as a share of allocatable.",
               [T(f'100 * sum by (node) (rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[$__rate_interval])) / {alloc_by("node", "cpu")}', "{{node}}")],
               unit="percent", th=PCT, soft_max=100), 12))
    d.line(8,
           (bargauge("Free memory per node (allocatable - requested)", "Memory a new pod could still request on each "
                     "schedulable node (control-plane, tainted and cordoned nodes excluded). The largest bar is the biggest memory request that can be scheduled right now.",
                     [T(free_by_node("memory"), "{{node}}")],
                     unit="bytes", th=steps((RED, None), (AMBER, 536870912), (GREEN, 2147483648)), min=0, max=None), 12),
           (bargauge("Free CPU per node (allocatable - requested)", "CPU a new pod could still request on each schedulable node.",
                     [T(free_by_node("cpu"), "{{node}}")],
                     unit="cores", decimals=2, th=steps((RED, None), (AMBER, 0.25), (GREEN, 1)), min=0, max=None), 12))

    d.row("Namespaces & workloads")
    d.line(9,
           (ts("Memory requested by namespace", "Stacked memory requests of running + pending pods per namespace "
               "(top 10), with allocatable dashed.",
               [T(f'topk(10, {req_by("namespace", "memory", NS)})', "{{namespace}}"),
                T(f'sum(kube_node_status_allocatable{{{K}, {N}, resource="memory"}})', "allocatable")],
               unit="bytes", stack=True, fill=40, overrides=[ov_dashed("allocatable", RED)]), 12),
           (table("Reclaimable memory by workload", "Containers (aggregated per namespace / pod owner) whose memory "
                  "request is far above what they use. Request minus usage is reserved memory nobody uses; trimming "
                  "the top of this list is the cheapest capacity on the platform.",
                  [f'topk(25, sum by (namespace, owner) (label_replace(kube_pod_container_resource_requests{{{K}, {NS}, resource="memory"}} * on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase="Running"}} == 1), "owner", "${1}${3}", "pod", "(.+?)(-[a-z0-9]{{8,10}})?-[a-z0-9]{{5}}$|(.+)-[0-9]+$")) '
                   f'- sum by (namespace, owner) (label_replace(container_memory_working_set_bytes{{{CAD}, container!="", {NS}}}, "owner", "${1}${3}", "pod", "(.+?)(-[a-z0-9]{{8,10}})?-[a-z0-9]{{5}}$|(.+)-[0-9]+$")))',
                   f'sum by (namespace, owner) (label_replace(kube_pod_container_resource_requests{{{K}, {NS}, resource="memory"}} * on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase="Running"}} == 1), "owner", "${1}${3}", "pod", "(.+?)(-[a-z0-9]{{8,10}})?-[a-z0-9]{{5}}$|(.+)-[0-9]+$"))',
                   f'sum by (namespace, owner) (label_replace(container_memory_working_set_bytes{{{CAD}, container!="", {NS}}}, "owner", "${1}${3}", "pod", "(.+?)(-[a-z0-9]{{8,10}})?-[a-z0-9]{{5}}$|(.+)-[0-9]+$"))'],
                  [("namespace", "Namespace", None, None), ("owner", "Workload", None, None),
                   ("Value #A", "Reclaimable", "bytes", {"cell": "color-background", "th": steps((GREEN, None), (AMBER, 268435456), (RED, 1073741824))}),
                   ("Value #B", "Requested", "bytes", None), ("Value #C", "Used", "bytes", None)],
                  sort=("Reclaimable", True)), 12))
    d.line(9,
           (table("Top memory requests by container", "Largest memory requests of running containers, with their "
                  "current usage and limit.",
                  [f'topk(25, max by (namespace, pod, container) (kube_pod_container_resource_requests{{{K}, {NS}, {N}, resource="memory"}} * on (namespace, pod) group_left () max by (namespace, pod) (kube_pod_status_phase{{{K}, phase="Running"}} == 1)))',
                   f'max by (namespace, pod, container) (container_memory_working_set_bytes{{{CAD}, container!="", {NS}}})',
                   f'max by (namespace, pod, container) (kube_pod_container_resource_limits{{{K}, {NS}, resource="memory"}})',
                   f'100 * max by (namespace, pod, container) (container_memory_working_set_bytes{{{CAD}, container!="", {NS}}}) / max by (namespace, pod, container) (kube_pod_container_resource_requests{{{K}, {NS}, resource="memory"}})'],
                  [("namespace", "Namespace", None, None), ("pod", "Pod", None, {"links": pod_link()}),
                   ("container", "Container", None, None),
                   ("Value #A", "Request", "bytes", None), ("Value #B", "Used", "bytes", None),
                   ("Value #C", "Limit", "bytes", None),
                   ("Value #D", "Used % of request", "percent", {"cell": "gauge", "th": steps((BLUE, None), (GREEN, 40), (AMBER, 100), (RED, 150)), "min": 0, "max": 150})],
                  sort=("Request", True), hide=()), 12),
           (table("Pending & unschedulable pods", "Pods not running, with what they ask for. Compare the requests "
                  "with 'Largest free memory / CPU' above to see whether anything could fit.",
                  [f'max by (namespace, pod) (kube_pod_status_phase{{{K}, {NS}, phase="Pending"}} == 1)',
                   f'sum by (namespace, pod) (kube_pod_container_resource_requests{{{K}, {NS}, resource="memory"}}) and on (namespace, pod) (kube_pod_status_phase{{{K}, {NS}, phase="Pending"}} == 1)',
                   f'sum by (namespace, pod) (kube_pod_container_resource_requests{{{K}, {NS}, resource="cpu"}}) and on (namespace, pod) (kube_pod_status_phase{{{K}, {NS}, phase="Pending"}} == 1)',
                   f'max by (namespace, pod) (kube_pod_status_unschedulable{{{K}, {NS}}})',
                   f'max by (namespace, pod) (time() - kube_pod_created{{{K}, {NS}}}) and on (namespace, pod) (kube_pod_status_phase{{{K}, {NS}, phase="Pending"}} == 1)'],
                  [("namespace", "Namespace", None, None), ("pod", "Pod", None, {"links": pod_link()}),
                   ("Value #B", "Mem request", "bytes", None), ("Value #C", "CPU request", "cores", {"decimals": 2}),
                   ("Value #D", "Unschedulable", None, {"mappings": vmap((1, "YES", RED)), "cell": "color-text", "novalue": "-"}),
                   ("Value #E", "Pending for", "s", None)],
                  hide=("Value #A",), sort=("Pending for", True)), 12))

    d.row("Trends")
    d.line(8,
           (ts("Pending pods & nodes", "Pending pods against the node count: the autoscaler should add a node soon "
               "after pods start pending for capacity.",
               [T(f'sum(kube_pod_status_phase{{{K}, {NS}, phase="Pending"}})', "pending pods"),
                T(f'sum(kube_pod_status_unschedulable{{{K}, {NS}}})', "unschedulable"),
                T(f'count(kube_node_info{{{K}}})', "nodes")],
               overrides=[{"matcher": {"id": "byName", "options": "nodes"},
                           "properties": [{"id": "custom.axisPlacement", "value": "right"},
                                          {"id": "custom.drawStyle", "value": "line"},
                                          {"id": "custom.lineStyle", "value": {"dash": [10, 10], "fill": "dash"}}]}]), 12),
           (ts("Request efficiency over time", "Used / requested for memory and CPU across running pods.",
               [T(f'100 * sum(container_memory_working_set_bytes{{{CAD}, container!="", {N}}}) / sum({req_by("node", "memory")})', "memory"),
                T(f'100 * sum(rate(container_cpu_usage_seconds_total{{{CAD}, container!="", {N}}}[$__rate_interval])) / sum({req_by("node", "cpu")})', "cpu")],
               unit="percent", soft_max=100), 12))
    return d


