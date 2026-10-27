"""Kubernetes folder: five dashboards in place of thirty-two.

  Cluster Overview   the cluster at a glance (kept uid adhar-k8s-overview) + the
                     kubernetes-mixin compute view per namespace + object inventory.
  Nodes & Capacity   requests vs allocatable vs usage (kept uid Hzyg1Dzmk) + every
                     node-exporter detail (CPU modes, memory, disks, filesystems,
                     network, PSI, conntrack) + pods per node + node autoscaling.
  Workloads & Pods   namespace -> workload -> pod drill-down (kept uid 6581e46e…, the
                     target of every pod link on the platform) + rollouts, jobs,
                     autoscaling (HPA / KEDA) and the pod's logs.
  Control Plane      API server, etcd, controller-manager, scheduler, kubelets and
                     metrics-server.
  Storage            PersistentVolumeClaims, volume usage, the node-local disks behind
                     them, attach limits and volume operations.
"""
from dashlib import *  # noqa: F401,F403
from dash_common import (CAD, KSM, UIDS, cluster_var, embed_mixin, go_process, links, pods_table, tags)
from dash_k8s_core import k8s_capacity, k8s_overview

K = KSM
NODE_NE = 'node_uname_info{nodename=~"$node"}'


def ne(expr, by="instance"):
    """A node-exporter expression (keeping `instance`) relabelled with the node name and
    filtered by $node: node-exporter reports IP:port, the cluster speaks node names."""
    return f'({expr}) * on (instance) group_left (nodename) {NODE_NE}'


# =============================================================================================
# Cluster Overview
# =============================================================================================

def overview():
    d = k8s_overview()
    d.title = "Kubernetes / Cluster Overview"
    d.description += (" Below the summary: the kubernetes-mixin compute and network view per namespace (formerly "
                      "'Compute Resources / Cluster') and the inventory of every object kind.")
    d.links = links("Kubernetes", ("Nodes & capacity", "k8s-nodes"), ("Workloads & pods", "k8s-workloads"),
                    ("Control plane", "k8s-control-plane"), ("Storage", "k8s-storage"))
    d.add_variables([cluster_var()])
    d.collapse = {"Cluster at a glance", "Requests vs real usage per node", "Unhealthy pods"}

    d.row("Object inventory")
    kinds = [("Deployments", "kube_deployment_created"), ("StatefulSets", "kube_statefulset_created"),
             ("DaemonSets", "kube_daemonset_created"), ("Jobs", "kube_job_created"),
             ("CronJobs", "kube_cronjob_created"), ("Services", "kube_service_created"),
             ("PVCs", "kube_persistentvolumeclaim_created"), ("ConfigMaps", "kube_configmap_created")]
    d.line(4, *[(stat(n, f"Number of {n} in the selected namespaces (kube-state-metrics).",
                      [f'count({m}{{{K}, namespace=~"$namespace"}}) or vector(0)'], color_mode="value"), 3)
                for n, m in kinds])
    d.line(8,
           (ts("Pods by phase", "Pods in each phase over time across the selected namespaces. A Pending band that "
               "does not shrink is a scheduling or image problem; Failed pods are usually finished Jobs.",
               [T(f'sum by (phase) (kube_pod_status_phase{{{K}, namespace=~"$namespace"}})', "{{phase}}")],
               stack=True, overrides=[ov_color("Running", GREEN), ov_color("Pending", AMBER), ov_color("Failed", RED),
                                      ov_color("Succeeded", BLUE), ov_color("Unknown", PURPLE)]), 8),
           (ts("Containers waiting, by reason", "Containers not running and why (CrashLoopBackOff, ImagePullBackOff, "
               "CreateContainerConfigError, ...). Anything here for more than a few minutes needs a look.",
               [T(f'sum by (reason) (kube_pod_container_status_waiting_reason{{{K}, namespace=~"$namespace"}}) > 0', "{{reason}}")],
               stack=True, decimals=0), 8),
           (table("Objects per namespace", "Workload objects per namespace. Click a namespace to open it in "
                  "Workloads & Pods.",
                  [f'count by (namespace) (kube_pod_info{{{K}, namespace=~"$namespace"}})',
                   f'count by (namespace) (kube_deployment_created{{{K}, namespace=~"$namespace"}})',
                   f'count by (namespace) (kube_statefulset_created{{{K}, namespace=~"$namespace"}})',
                   f'count by (namespace) (kube_persistentvolumeclaim_created{{{K}, namespace=~"$namespace"}})'],
                  [("namespace", "Namespace", None, {"links": [{"title": "Open namespace",
                    "url": f"/d/{UIDS['k8s-workloads']}?var-datasource=${{datasource}}&var-namespace=${{__data.fields.Namespace}}&${{__url_time_range}}"}]}),
                   ("Value #A", "Pods", "short", None), ("Value #B", "Deployments", "short", None),
                   ("Value #C", "StatefulSets", "short", None), ("Value #D", "PVCs", "short", None)],
                  sort=("Pods", True)), 8))

    embed_mixin(d, "k8s-resources-cluster", row="Compute, network & storage by namespace (kubernetes-mixin)")
    return d


# =============================================================================================
# Nodes & Capacity
# =============================================================================================

def nodes():
    d = k8s_capacity()
    d.title = "Kubernetes / Nodes & Capacity"
    d.description = (d.description + " Below the capacity view: every node's hardware and OS in node-exporter "
                     "detail (formerly Node Exporter Full, Kubernetes / Views / Nodes, Kubernetes Nodes, the USE "
                     "method and Nodes Overview), the pods on each node, and node autoscaling.")
    d.links = links("Kubernetes", ("Cluster overview", "k8s-overview"), ("Workloads & pods", "k8s-workloads"),
                    ("Storage", "k8s-storage"))
    d.add_variables([cluster_var()])
    d.collapse = {"Capacity at a glance", "Per node over time"}

    d.row("Nodes (node-exporter)")
    cores = 'count without (cpu, mode) (node_cpu_seconds_total{mode="idle"})'
    d.line(9, (table(
        "Nodes", "One row per node from node-exporter: kernel, cores, load per core, CPU busy, memory used, root "
        "filesystem used, disk I/O utilisation of the busiest device, network, uptime and pressure stall (PSI). "
        "Load per core over 1 or memory PSI above a few percent mean the node is genuinely saturated, not just "
        "fully requested.",
        [ne('max by (instance, release) (node_uname_info)'),
         ne(cores),
         ne(f'node_load5 / {cores}'),
         ne('100 * (1 - avg without (cpu) (rate(node_cpu_seconds_total{mode="idle"}[5m])))'),
         ne('100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)'),
         ne('100 * (1 - node_filesystem_avail_bytes{mountpoint="/", fstype!="rootfs"} / node_filesystem_size_bytes{mountpoint="/", fstype!="rootfs"})'),
         ne('100 * max without (device) (rate(node_disk_io_time_seconds_total{device!~"dm-.*|loop.*"}[5m]))'),
         ne('sum without (device) (rate(node_network_receive_bytes_total{device!~"lo|veth.*|lxc.*|cilium.*"}[5m]) + rate(node_network_transmit_bytes_total{device!~"lo|veth.*|lxc.*|cilium.*"}[5m]))'),
         ne('time() - node_boot_time_seconds'),
         ne('100 * rate(node_pressure_memory_waiting_seconds_total[5m])'),
         ne('100 * rate(node_pressure_cpu_waiting_seconds_total[5m])')],
        [("nodename", "Node", None, None), ("release", "Kernel", None, None),
         ("Value #B", "Cores", "short", None),
         ("Value #C", "Load5 / core", "short", {"decimals": 2, "cell": "color-background", "th": steps((GREEN, None), (AMBER, 1), (RED, 2))}),
         ("Value #D", "CPU busy", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #E", "Memory used", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #F", "Root FS used", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #G", "Disk busy", "percent", {"cell": "color-text", "th": PCT, "decimals": 0}),
         ("Value #H", "Network", "Bps", None),
         ("Value #I", "Uptime", "s", None),
         ("Value #J", "Mem PSI", "percent", {"decimals": 1, "cell": "color-text", "th": steps((GREEN, None), (AMBER, 5), (RED, 20))}),
         ("Value #K", "CPU PSI", "percent", {"decimals": 1, "cell": "color-text", "th": steps((GREEN, None), (AMBER, 20), (RED, 50))})],
        sort=("Node", False), hide=("instance", "Value #A")), 24))

    d.row("CPU & load")
    d.line(8,
           (ts("CPU busy by mode", "Share of all CPU time on the selected nodes spent in each mode (idle excluded). "
               "High iowait means processes wait for disk; high steal means the hypervisor is short of CPU.",
               [T(f'sum by (mode) (rate(node_cpu_seconds_total{{mode!="idle"}}[$__rate_interval]) * on (instance) group_left () {NODE_NE}) '
                  # one-to-many against the CPU count, not scalar(): scalar() of nothing is NaN
                  f'/ on () group_left () (sum(count without (cpu, mode) (node_cpu_seconds_total{{mode="idle"}}) * on (instance) group_left () {NODE_NE}) > 0)', "{{mode}}")],
               unit="percentunit", stack=True, overrides=[ov_color("iowait", RED), ov_color("steal", PURPLE)]), 8),
           (ts("CPU busy per node", "1 - idle per node.",
               [T(ne('1 - avg without (cpu) (rate(node_cpu_seconds_total{mode="idle"}[$__rate_interval]))'), "{{nodename}}")],
               unit="percentunit", max=1, th=steps((GREEN, None), (AMBER, 0.75), (RED, 0.9))), 8),
           (ts("Load average per core", "5-minute load divided by the node's cores. Over 1 means runnable work is "
               "queueing for CPU.",
               [T(ne(f'node_load5 / {cores}'), "{{nodename}}")], decimals=2,
               th=steps((GREEN, None), (AMBER, 1), (RED, 2))), 8))
    d.line(8,
           (ts("CPU pressure (PSI)", "Share of time at least one task waited for CPU (some) per node. The honest "
               "saturation signal: utilisation can read 70% while tasks queue.",
               [T(ne('rate(node_pressure_cpu_waiting_seconds_total[$__rate_interval])'), "{{nodename}}")],
               unit="percentunit", th=steps((GREEN, None), (AMBER, 0.2), (RED, 0.5))), 8),
           (ts("Context switches & interrupts", "Per second per node. A sudden jump with flat load is often a "
               "busy-looping process or a noisy NIC.",
               [T(ne('rate(node_context_switches_total[$__rate_interval])'), "{{nodename}} ctx"),
                T(ne('rate(node_intr_total[$__rate_interval])'), "{{nodename}} intr")], unit="ops"), 8),
           (ts("Processes", "Running and blocked (uninterruptible, usually on I/O) processes per node.",
               [T(ne('node_procs_running'), "{{nodename}} running"),
                T(ne('node_procs_blocked'), "{{nodename}} blocked")],
               decimals=0, overrides=[ov_color(".*blocked", RED)]), 8))

    d.row("Memory")
    d.line(8,
           (ts("Memory used per node", "1 - MemAvailable / MemTotal per node.",
               [T(ne('1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes'), "{{nodename}}")],
               unit="percentunit", max=1, th=steps((GREEN, None), (AMBER, 0.85), (RED, 0.95))), 8),
           (ts("Memory breakdown (selected nodes)", "Where the memory of the selected nodes goes: applications "
               "(anonymous), page cache, buffers, slab, and what is free.",
               [T(f'sum(node_memory_MemTotal_bytes * on (instance) group_left () {NODE_NE}) - sum((node_memory_MemFree_bytes + node_memory_Cached_bytes + node_memory_Buffers_bytes + node_memory_SReclaimable_bytes) * on (instance) group_left () {NODE_NE})', "used by processes"),
                T(f'sum(node_memory_Cached_bytes * on (instance) group_left () {NODE_NE})', "page cache"),
                T(f'sum(node_memory_Buffers_bytes * on (instance) group_left () {NODE_NE})', "buffers"),
                T(f'sum(node_memory_SReclaimable_bytes * on (instance) group_left () {NODE_NE})', "slab (reclaimable)"),
                T(f'sum(node_memory_MemFree_bytes * on (instance) group_left () {NODE_NE})', "free")],
               unit="bytes", stack=True, overrides=[ov_color("free", GREEN)]), 8),
           (ts("Memory pressure, page faults & OOM kills", "Memory PSI (share of time tasks stalled on memory), "
               "major page faults per second and kernel OOM kills (bars).",
               [T(ne('rate(node_pressure_memory_waiting_seconds_total[$__rate_interval])'), "{{nodename}} PSI"),
                T(ne('rate(node_vmstat_pgmajfault[$__rate_interval])'), "{{nodename}} major faults/s"),
                T(ne('increase(node_vmstat_oom_kill[$__rate_interval])'), "{{nodename}} OOM kills")],
               overrides=[{"matcher": {"id": "byRegexp", "options": ".*PSI"},
                           "properties": [{"id": "unit", "value": "percentunit"}]},
                          {"matcher": {"id": "byRegexp", "options": ".*(faults|OOM).*"},
                           "properties": [{"id": "custom.axisPlacement", "value": "right"}]},
                          ov_color(".*OOM kills", RED)]), 8))

    d.row("Disks & filesystems")
    dev = 'device!~"dm-.*|loop.*|sr.*"'
    d.line(8,
           (ts("Disk utilisation", "Share of time each device was busy serving I/O. Near 100% the disk is the "
               "bottleneck (databases, Kafka, image pulls).",
               [T(ne(f'rate(node_disk_io_time_seconds_total{{{dev}}}[$__rate_interval])'), "{{nodename}} {{device}}")],
               unit="percentunit", max=1, th=steps((GREEN, None), (AMBER, 0.6), (RED, 0.9))), 8),
           (ts("Disk throughput", "Bytes read (above) and written (below) per device.",
               [T(ne(f'rate(node_disk_read_bytes_total{{{dev}}}[$__rate_interval])'), "{{nodename}} {{device}} read"),
                T(ne(f'rate(node_disk_written_bytes_total{{{dev}}}[$__rate_interval])'), "{{nodename}} {{device}} write")],
               unit="Bps", overrides=[ov_negative(".* write")]), 8),
           (ts("Disk latency", "Average time per completed read and write. Network block storage typically sits at "
               "1-5 ms; tens of ms means the volume is throttled.",
               [T(ne(f'rate(node_disk_read_time_seconds_total{{{dev}}}[$__rate_interval]) / rate(node_disk_reads_completed_total{{{dev}}}[$__rate_interval])'), "{{nodename}} {{device}} read"),
                T(ne(f'rate(node_disk_write_time_seconds_total{{{dev}}}[$__rate_interval]) / rate(node_disk_writes_completed_total{{{dev}}}[$__rate_interval])'), "{{nodename}} {{device}} write")],
               unit="s"), 8))
    fs = 'fstype!~"tmpfs|overlay|squashfs|rootfs|vfat|nsfs", mountpoint!~"/boot.*"'
    d.line(8,
           (bargauge("Filesystem used", "Used share of every real filesystem per node. Node-local PersistentVolumes "
                     "(the adhar-local StorageClass) live on these filesystems, so a full root filesystem is also "
                     "full databases.",
                     [T(ne(f'100 * (1 - node_filesystem_avail_bytes{{{fs}}} / node_filesystem_size_bytes{{{fs}}})'), "{{nodename}} {{mountpoint}}")]), 8),
           (ts("IOPS", "Reads and writes completed per second per device.",
               [T(ne(f'rate(node_disk_reads_completed_total{{{dev}}}[$__rate_interval])'), "{{nodename}} {{device}} reads"),
                T(ne(f'rate(node_disk_writes_completed_total{{{dev}}}[$__rate_interval])'), "{{nodename}} {{device}} writes")],
               unit="iops", overrides=[ov_negative(".* writes")]), 8),
           (ts("Inodes & I/O pressure", "Inodes used on each root filesystem and I/O pressure stall (share of time "
               "tasks waited on I/O).",
               [T(ne('1 - node_filesystem_files_free{mountpoint="/"} / node_filesystem_files{mountpoint="/"}'), "{{nodename}} inodes"),
                T(ne('rate(node_pressure_io_waiting_seconds_total[$__rate_interval])'), "{{nodename}} IO PSI")],
               unit="percentunit"), 8))

    d.row("Network")
    nic = 'device!~"lo|veth.*|lxc.*|cilium.*|docker.*"'
    d.line(8,
           (ts("Network throughput", "Bytes received (above) and transmitted (below) on physical / cloud interfaces "
               "per node.",
               [T(ne(f'sum without (device) (rate(node_network_receive_bytes_total{{{nic}}}[$__rate_interval]))'), "{{nodename}} rx"),
                T(ne(f'sum without (device) (rate(node_network_transmit_bytes_total{{{nic}}}[$__rate_interval]))'), "{{nodename}} tx")],
               unit="Bps", overrides=[ov_negative(".* tx")]), 8),
           (ts("Errors & drops", "Receive / transmit errors and drops per second per node. Drops on a cloud NIC are "
               "usually the instance's packets-per-second allowance.",
               [T(ne(f'sum without (device) (rate(node_network_receive_errs_total{{{nic}}}[$__rate_interval]) + rate(node_network_transmit_errs_total{{{nic}}}[$__rate_interval]))'), "{{nodename}} errors"),
                T(ne(f'sum without (device) (rate(node_network_receive_drop_total{{{nic}}}[$__rate_interval]) + rate(node_network_transmit_drop_total{{{nic}}}[$__rate_interval]))'), "{{nodename}} drops")],
               unit="pps", th=steps((GREEN, None), (RED, 1))), 8),
           (ts("TCP: retransmits, connections, conntrack", "TCP segments retransmitted per second, established "
               "connections, and the conntrack table's fill level (full = new connections are dropped).",
               [T(ne('rate(node_netstat_Tcp_RetransSegs[$__rate_interval])'), "{{nodename}} retrans/s"),
                T(ne('node_netstat_Tcp_CurrEstab'), "{{nodename}} established"),
                T(ne('100 * node_nf_conntrack_entries / node_nf_conntrack_entries_limit'), "{{nodename}} conntrack %")],
               overrides=[{"matcher": {"id": "byRegexp", "options": ".*conntrack %"},
                           "properties": [{"id": "unit", "value": "percent"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("System")
    d.line(8,
           (ts("File descriptors", "Allocated file handles as a share of the kernel maximum per node.",
               [T(ne('node_filefd_allocated / node_filefd_maximum'), "{{nodename}}")], unit="percentunit",
               th=steps((GREEN, None), (AMBER, 0.7), (RED, 0.9))), 8),
           (ts("Clock offset", "Offset of the system clock from its time source (timex). TLS, etcd leases and "
               "token validation all break with a skewed clock.",
               [T(ne('node_timex_offset_seconds'), "{{nodename}}")], unit="s",
               th=steps((GREEN, None), (AMBER, 0.05), (RED, 0.5))), 8),
           (timeline("Node conditions", "Kubernetes node conditions (kubelet): Ready, and the pressure conditions "
                     "that make the kubelet evict pods.",
                     [T(f'max by (node) (kube_node_status_condition{{{K}, node=~"$node", condition="Ready", status="true"}})', "{{node}} Ready"),
                      T(f'max by (node) (kube_node_status_condition{{{K}, node=~"$node", condition=~"MemoryPressure|DiskPressure|PIDPressure", status="true"}})', "{{node}} pressure")],
                     vmap((0, "no", GREY), (1, "yes", GREEN))), 8))

    embed_mixin(d, "k8s-resources-node", row="Pods on the selected nodes (kubernetes-mixin)")

    d.row("Node autoscaling")
    d.line(8,
           (ts("Nodes and pending pods", "Node count (ready / total) against pods that cannot be scheduled. The Adhar "
               "node autoscaler adds a worker when pods stay Pending for capacity and removes the emptiest node when "
               "the cluster stays idle.",
               [T(f'sum(kube_node_status_condition{{{K}, condition="Ready", status="true"}})', "ready nodes"),
                T(f'count(kube_node_info{{{K}}})', "nodes"),
                T(f'sum(kube_pod_status_unschedulable{{{K}}}) or vector(0)', "unschedulable pods")],
               decimals=0, overrides=[ov_color("unschedulable pods", RED), ov_dashed("nodes")]), 12),
           (logs("Autoscaler decisions (Loki)", "What the Adhar node autoscaler decided and why: scale-up and "
                 "scale-down decisions, refusals by the cloud, and joins.",
                 '{namespace="adhar-system", app="adhar-controller-manager"} |~ "(?i)scal|worker node group|join"'), 12))
    return d


# =============================================================================================
# Workloads & Pods
# =============================================================================================

def workloads():
    NSX = 'namespace="$namespace"'
    v_ns = qvar("namespace", "Namespace", f'label_values(kube_namespace_status_phase{{{K}}}, namespace)',
                multi=False, include_all=False)
    v_ns["current"] = {"selected": True, "text": "adhar-system", "value": "adhar-system"}
    v_type = qvar("type", "Workload type", 'label_values(namespace_workload_pod:kube_pod_owner:relabel{namespace="$namespace"}, workload_type)',
                  multi=False, include_all=True, all_value=".+")
    v_wl = qvar("workload", "Workload", 'label_values(namespace_workload_pod:kube_pod_owner:relabel{namespace="$namespace", workload_type=~"$type"}, workload)',
                multi=False, include_all=False)
    v_pod = qvar("pod", "Pod", f'label_values(kube_pod_info{{{K}, namespace="$namespace"}}, pod)',
                 multi=False, include_all=False)
    d = Dash(UIDS["k8s-workloads"], "Kubernetes / Workloads & Pods",
             "Drill down namespace -> workload -> pod. Pick a namespace for its health, workloads and pods; click a "
             "workload or pod in the tables (or pick it above) and open its section below for the kubernetes-mixin "
             "compute, network and storage view. Also: rollouts of Deployments / StatefulSets / DaemonSets, Jobs and "
             "CronJobs, autoscaling (HPA and KEDA) and the pod's logs. Replaces the Compute Resources / Namespace, "
             "Workload and Pod dashboards, Kubernetes / Views / Namespaces and Pods, Kubernetes / Pods, StatefulSets "
             "and the KEDA dashboards; its uid is the old Pod dashboard's, which every pod link points at.",
             tags("Kubernetes", "kubernetes"),
             [ds_var(), cluster_var(), v_ns, v_type, v_wl, v_pod],
             links=links("Kubernetes", ("Cluster overview", "k8s-overview"), ("Nodes & capacity", "k8s-nodes")),
             collapse={"Namespace at a glance", "Workloads in the namespace", "Pods in the namespace"})

    d.row("Namespace at a glance")
    run = f'max by (namespace, pod) (kube_pod_status_phase{{{K}, {NSX}, phase=~"Pending|Running"}} == 1)'
    d.line(4,
           (stat("Pods running", "Pods in phase Running.",
                 [f'sum(kube_pod_status_phase{{{K}, {NSX}, phase="Running"}}) or vector(0)'], color_mode="value"), 3),
           (stat("Pods pending", "Pods not yet scheduled or still pulling images. Persistently above zero means no "
                 "node fits them or an image cannot be pulled.",
                 [f'sum(kube_pod_status_phase{{{K}, {NSX}, phase="Pending"}}) or vector(0)'], th=OK_WARN_BAD), 3),
           (stat("Containers not ready", "Running pods' containers that fail their readiness probe.",
                 [f'sum(kube_pod_container_status_ready{{{K}, {NSX}}} == 0 and on (namespace, pod) (kube_pod_status_phase{{{K}, phase="Running"}} == 1)) or vector(0)'],
                 th=OK_WARN_BAD), 3),
           (stat("Crash-looping / image errors", "Containers waiting in CrashLoopBackOff, ImagePullBackOff, "
                 "ErrImagePull or CreateContainerConfigError.",
                 [f'sum(kube_pod_container_status_waiting_reason{{{K}, {NSX}, reason=~"CrashLoopBackOff|ImagePullBackOff|ErrImagePull|CreateContainerConfigError"}}) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Restarts (24h)", "Container restarts in the namespace over the last 24 hours.",
                 [f'sum(increase(kube_pod_container_status_restarts_total{{{K}, {NSX}}}[24h])) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 10)), decimals=0), 3),
           (stat("OOM-killed containers", "Containers whose last termination was an OOM kill.",
                 [f'sum(kube_pod_container_status_last_terminated_reason{{{K}, {NSX}, reason="OOMKilled"}}) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("CPU used / requested", "CPU used by the namespace's containers as a share of what they request.",
                 [f'100 * sum(rate(container_cpu_usage_seconds_total{{{CAD}, {NSX}, container!=""}}[5m])) / '
                  f'sum(kube_pod_container_resource_requests{{{K}, {NSX}, resource="cpu"}} * on (namespace, pod) group_left () {run})'],
                 unit="percent", th=steps((BLUE, None), (GREEN, 30), (AMBER, 100), (RED, 150)), decimals=0), 3),
           (stat("Memory used / requested", "Working-set memory as a share of memory requests. Far below 100% means "
                 "requests (and therefore the nodes) are oversized.",
                 [f'100 * sum(container_memory_working_set_bytes{{{CAD}, {NSX}, container!=""}}) / '
                  f'sum(kube_pod_container_resource_requests{{{K}, {NSX}, resource="memory"}} * on (namespace, pod) group_left () {run})'],
                 unit="percent", th=steps((BLUE, None), (GREEN, 30), (AMBER, 100), (RED, 150)), decimals=0), 3))

    d.row("Workloads in the namespace")
    # topk: a pod owned through two levels (e.g. a Strimzi pod set) has two owner series.
    OWN = f'topk by (pod) (1, namespace_workload_pod:kube_pod_owner:relabel{{{NSX}}})'
    wl_link = [{"title": "Open workload",
                "url": f"/d/{UIDS['k8s-workloads']}?var-datasource=${{datasource}}&var-namespace=$namespace"
                       f"&var-type=${{__data.fields.Type}}&var-workload=${{__data.fields.Workload}}&${{__url_time_range}}"}]
    d.line(9, (table(
        "Workloads", "Every workload in the namespace (Deployments, StatefulSets, DaemonSets, Jobs, ...): pods, "
        "restarts, CPU and memory used against requests. Click a workload to select it, then open the 'Workload' "
        "section below.",
        [f'count by (workload, workload_type) ({OWN})',
         f'sum by (workload, workload_type) (sum by (pod) (increase(kube_pod_container_status_restarts_total{{{K}, {NSX}}}[24h])) * on (pod) group_left (workload, workload_type) {OWN})',
         f'sum by (workload, workload_type) (sum by (pod) (rate(container_cpu_usage_seconds_total{{{CAD}, {NSX}, container!=""}}[5m])) * on (pod) group_left (workload, workload_type) {OWN})',
         f'sum by (workload, workload_type) (sum by (pod) (kube_pod_container_resource_requests{{{K}, {NSX}, resource="cpu"}}) * on (pod) group_left (workload, workload_type) {OWN})',
         f'sum by (workload, workload_type) (sum by (pod) (container_memory_working_set_bytes{{{CAD}, {NSX}, container!=""}}) * on (pod) group_left (workload, workload_type) {OWN})',
         f'sum by (workload, workload_type) (sum by (pod) (kube_pod_container_resource_requests{{{K}, {NSX}, resource="memory"}}) * on (pod) group_left (workload, workload_type) {OWN})',
         f'100 * sum by (workload, workload_type) (sum by (pod) (container_memory_working_set_bytes{{{CAD}, {NSX}, container!=""}}) * on (pod) group_left (workload, workload_type) {OWN}) / '
         f'sum by (workload, workload_type) (sum by (pod) (kube_pod_container_resource_requests{{{K}, {NSX}, resource="memory"}}) * on (pod) group_left (workload, workload_type) {OWN})'],
        [("workload", "Workload", None, {"links": wl_link}), ("workload_type", "Type", None, None),
         ("Value #A", "Pods", "short", None),
         ("Value #B", "Restarts 24h", "short", {"cell": "color-background", "th": OK_WARN_BAD, "decimals": 0}),
         ("Value #C", "CPU", "cores", {"decimals": 3}), ("Value #D", "CPU request", "cores", {"decimals": 2}),
         ("Value #E", "Memory", "bytes", None), ("Value #F", "Memory request", "bytes", None),
         ("Value #G", "Mem % of request", "percent", {"cell": "gauge", "th": steps((BLUE, None), (GREEN, 30), (AMBER, 100), (RED, 150)), "min": 0, "max": 150})],
        sort=("Memory", True)), 24))

    d.row("Pods in the namespace")
    d.line(10, (pods_table("Pods", "Every pod in the namespace.", ".+", ns=NSX), 24))

    embed_mixin(d, "k8s-resources-namespace", row="Namespace compute, network & storage (kubernetes-mixin)",
                skip=("CPU Utilisation (from requests)", "CPU Utilisation (from limits)",
                      "Memory Utilisation (from requests)", "Memory Utilisation (from limits)"))
    embed_mixin(d, "k8s-resources-workloads-namespace", row="Workloads by type: $type (kubernetes-mixin)")
    embed_mixin(d, "k8s-resources-workload", row="Workload: $workload (kubernetes-mixin)")
    embed_mixin(d, "k8s-resources-pod", row="Pod: $pod (kubernetes-mixin)")

    d.row("Pod: $pod — status & logs")
    P = f'{NSX}, pod="$pod"'
    d.line(8,
           (timeline("Container readiness", "Readiness of each container of the selected pod over time.",
                     [T(f'max by (container) (kube_pod_container_status_ready{{{K}, {P}}})', "{{container}}")],
                     vmap((0, "not ready", RED), (1, "ready", GREEN))), 8),
           (table("Containers", "Containers of the selected pod: image, restarts, the reason of the last termination, "
                  "and requests / limits.",
                  [f'max by (container, image) (kube_pod_container_info{{{K}, {P}}})',
                   f'max by (container) (kube_pod_container_status_restarts_total{{{K}, {P}}})',
                   f'max by (container, reason) (kube_pod_container_status_last_terminated_reason{{{K}, {P}}} == 1)',
                   f'sum by (container) (kube_pod_container_resource_requests{{{K}, {P}, resource="cpu"}})',
                   f'sum by (container) (kube_pod_container_resource_limits{{{K}, {P}, resource="memory"}})'],
                  [("container", "Container", None, None), ("image", "Image", None, None),
                   ("Value #B", "Restarts", "short", {"cell": "color-text", "th": OK_WARN_BAD}),
                   ("reason", "Last termination", None, None),
                   ("Value #D", "CPU request", "cores", None), ("Value #E", "Memory limit", "bytes", None)],
                  hide=("Value #A", "Value #C")), 16))
    d.line(10, (logs("Logs of $pod (Loki)", "Every log line of the selected pod, newest first.",
                     '{namespace="$namespace", pod="$pod"}'), 24))

    d.row("Rollouts: Deployments, StatefulSets, DaemonSets")
    d.line(8,
           (table("Rollout status", "Each Deployment / StatefulSet / DaemonSet in the namespace: desired, up to date "
                  "and available replicas. Available below desired for long means the rollout is stuck (a failing "
                  "readiness probe, an image that does not pull, or no room on the nodes).",
                  [f'max by (deployment) (kube_deployment_spec_replicas{{{K}, {NSX}}})',
                   f'max by (deployment) (kube_deployment_status_replicas_updated{{{K}, {NSX}}})',
                   f'max by (deployment) (kube_deployment_status_replicas_available{{{K}, {NSX}}})',
                   f'max by (deployment) (kube_deployment_spec_replicas{{{K}, {NSX}}}) - max by (deployment) (kube_deployment_status_replicas_available{{{K}, {NSX}}})'],
                  [("deployment", "Deployment", None, None), ("Value #A", "Desired", "short", None),
                   ("Value #B", "Up to date", "short", None), ("Value #C", "Available", "short", None),
                   ("Value #D", "Missing", "short", {"cell": "color-background", "th": OK_BAD})],
                  sort=("Missing", True)), 8),
           (table("StatefulSets", "Desired vs ready replicas and whether the controller has observed the latest "
                  "spec (generation lag = an update still rolling).",
                  [f'max by (statefulset) (kube_statefulset_replicas{{{K}, {NSX}}})',
                   f'max by (statefulset) (kube_statefulset_status_replicas_ready{{{K}, {NSX}}})',
                   f'max by (statefulset) (kube_statefulset_status_replicas_updated{{{K}, {NSX}}})',
                   f'max by (statefulset) (kube_statefulset_metadata_generation{{{K}, {NSX}}}) - max by (statefulset) (kube_statefulset_status_observed_generation{{{K}, {NSX}}})'],
                  [("statefulset", "StatefulSet", None, None), ("Value #A", "Desired", "short", None),
                   ("Value #B", "Ready", "short", None), ("Value #C", "Updated", "short", None),
                   ("Value #D", "Generation lag", "short", {"cell": "color-background", "th": OK_BAD})]), 8),
           (table("DaemonSets", "Nodes that should run the DaemonSet vs nodes where it is ready and up to date.",
                  [f'max by (daemonset) (kube_daemonset_status_desired_number_scheduled{{{K}, {NSX}}})',
                   f'max by (daemonset) (kube_daemonset_status_number_ready{{{K}, {NSX}}})',
                   f'max by (daemonset) (kube_daemonset_status_updated_number_scheduled{{{K}, {NSX}}})',
                   f'max by (daemonset) (kube_daemonset_status_number_misscheduled{{{K}, {NSX}}})'],
                  [("daemonset", "DaemonSet", None, None), ("Value #A", "Desired", "short", None),
                   ("Value #B", "Ready", "short", None), ("Value #C", "Updated", "short", None),
                   ("Value #D", "Misscheduled", "short", {"cell": "color-background", "th": OK_BAD})]), 8))
    d.line(8,
           (ts("Unavailable replicas over time", "Desired minus available replicas per Deployment and StatefulSet. "
               "Spikes are rollouts; a plateau is a stuck rollout.",
               [T(f'(max by (deployment) (kube_deployment_spec_replicas{{{K}, {NSX}}}) - max by (deployment) (kube_deployment_status_replicas_available{{{K}, {NSX}}})) > 0', "{{deployment}}"),
                T(f'(max by (statefulset) (kube_statefulset_replicas{{{K}, {NSX}}}) - max by (statefulset) (kube_statefulset_status_replicas_ready{{{K}, {NSX}}})) > 0', "{{statefulset}}")],
               decimals=0, draw="bars"), 12),
           (ts("Pod starts & restarts", "Containers restarted per interval in the namespace, by pod (top 10).",
               [T(f'topk(10, sum by (pod) (increase(kube_pod_container_status_restarts_total{{{K}, {NSX}}}[$__rate_interval]))) > 0', "{{pod}}")],
               draw="bars", decimals=0), 12))

    d.row("Jobs & CronJobs")
    d.line(8,
           (table("Jobs", "Jobs in the namespace: succeeded / failed pods and how long ago they finished. Failed "
                  "jobs of hooks (Argo CD PostSync, bootstrap) show up here first.",
                  [f'max by (job_name) (kube_job_status_succeeded{{{K}, {NSX}}})',
                   f'max by (job_name) (kube_job_status_failed{{{K}, {NSX}}})',
                   f'max by (job_name) (kube_job_status_active{{{K}, {NSX}}})',
                   f'time() - max by (job_name) (kube_job_status_completion_time{{{K}, {NSX}}})'],
                  [("job_name", "Job", None, None), ("Value #A", "Succeeded", "short", None),
                   ("Value #B", "Failed", "short", {"cell": "color-background", "th": OK_BAD}),
                   ("Value #C", "Active", "short", None), ("Value #D", "Finished ago", "s", None)],
                  sort=("Failed", True)), 12),
           (table("CronJobs", "Each CronJob: when it last ran and last succeeded, whether it is suspended, and how "
                  "many runs are active.",
                  [f'time() - max by (cronjob) (kube_cronjob_status_last_schedule_time{{{K}, {NSX}}})',
                   f'time() - max by (cronjob) (kube_cronjob_status_last_successful_time{{{K}, {NSX}}})',
                   f'max by (cronjob) (kube_cronjob_spec_suspend{{{K}, {NSX}}})',
                   f'max by (cronjob) (kube_cronjob_status_active{{{K}, {NSX}}})'],
                  [("cronjob", "CronJob", None, None), ("Value #A", "Last run", "s", None),
                   ("Value #B", "Last success", "s", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 93600), (RED, 180000))}),
                   ("Value #C", "Suspended", None, {"mappings": vmap((0, "no", GREEN), (1, "SUSPENDED", AMBER))}),
                   ("Value #D", "Active", "short", None)]), 12))

    d.row("Autoscaling: HPA & KEDA")
    d.line(8,
           (table("HorizontalPodAutoscalers", "Every HPA in the namespace (KEDA ScaledObjects appear as keda-hpa-*): "
                  "min / current / desired / max replicas. Current at max means the workload wants more than it is "
                  "allowed.",
                  [f'max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_spec_min_replicas{{{K}, {NSX}}})',
                   f'max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_status_current_replicas{{{K}, {NSX}}})',
                   f'max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_status_desired_replicas{{{K}, {NSX}}})',
                   f'max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_spec_max_replicas{{{K}, {NSX}}})',
                   f'max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_status_current_replicas{{{K}, {NSX}}}) / max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_spec_max_replicas{{{K}, {NSX}}})'],
                  [("horizontalpodautoscaler", "HPA", None, None), ("Value #A", "Min", "short", None),
                   ("Value #B", "Current", "short", None), ("Value #C", "Desired", "short", None),
                   ("Value #D", "Max", "short", None),
                   ("Value #E", "At max", None, {"cell": "color-background", "mappings": [
                       {"type": "range", "options": {"from": 1, "to": 1e9, "result": {"text": "AT MAX", "color": RED, "index": 0}}},
                       {"type": "range", "options": {"from": -1e9, "to": 0.9999, "result": {"text": "headroom", "color": GREEN, "index": 1}}}]})]), 12),
           (ts("Replicas: current vs desired", "Current (solid) and desired (dashed) replicas per HPA.",
               [T(f'max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_status_current_replicas{{{K}, {NSX}}})', "{{horizontalpodautoscaler}}"),
                T(f'max by (horizontalpodautoscaler) (kube_horizontalpodautoscaler_status_desired_replicas{{{K}, {NSX}}})', "{{horizontalpodautoscaler}} desired")],
               decimals=0, overrides=[ov_dashed(".* desired")]), 12))
    d.line(8,
           (ts("KEDA scaler metric values", "The value each KEDA scaler reads from its source (queue length, lag, "
               "...) for ScaledObjects in this namespace. Needs the KEDA operator's own metrics endpoint scraped.",
               [T('sum by (scaledObject, scaler, metric) (keda_scaler_metrics_value{exported_namespace="$namespace"})', "{{scaledObject}} {{metric}}")]), 8),
           (timeline("KEDA scalers active", "Whether each scaler is active (its trigger condition holds).",
                     [T('max by (scaledObject, scaler) (keda_scaler_active{exported_namespace="$namespace"})', "{{scaledObject}} {{scaler}}")],
                     vmap((0, "idle", GREY), (1, "active", GREEN))), 8),
           (ts("KEDA errors / s", "Errors fetching scaler metrics and reconciling ScaledObjects.",
               [T('sum by (scaledObject) (rate(keda_scaler_detail_errors_total{exported_namespace="$namespace"}[$__rate_interval]))', "{{scaledObject}} scaler"),
                T('sum by (scaledObject) (rate(keda_scaled_object_errors_total{exported_namespace="$namespace"}[$__rate_interval]))', "{{scaledObject}} object")],
               unit="ops", th=steps((GREEN, None), (RED, 0.01))), 8))
    return d


# =============================================================================================
# Control Plane
# =============================================================================================

def control_plane():
    A = 'job="apiserver"'
    CP = '(etcd|kube-apiserver|kube-controller-manager|kube-scheduler|cloud-controller-manager).*'
    ETCD = 'job=~"kube-etcd|etcd"'   # never Dapr's embedded etcd (job="scheduler")
    # kube-controller-manager, kube-scheduler and etcd bind to localhost on kubeadm nodes and are not
    # scraped; their tiles read "not scraped" rather than pretending to be healthy.
    d = Dash(UIDS["k8s-control-plane"], "Kubernetes / Control Plane",
             "kube-apiserver (SLO availability, latency, APF queueing, objects), etcd, every kubelet, the "
             "control-plane pods and metrics-server. On kubeadm clusters etcd, the controller-manager and the "
             "scheduler listen on localhost and are not scraped: their tiles read 'not scraped', their pods' CPU and "
             "restarts are below, and the API server's own view of etcd (request latency, database size, objects) "
             "stands in. A two-core control plane "
             "pegged by the API server is what makes controllers lose their leader-election leases (CLAUDE.md).",
             tags("Kubernetes", "kubernetes", "control-plane"),
             [ds_var(), cluster_var(),
              qvar("apiserver_instance", "API server", 'label_values(up{job="apiserver"}, instance)', hide=2),
              qvar("kubelet_instance", "Kubelet", 'label_values(up{job="kubelet", metrics_path="/metrics"}, instance)')],
             links=links("Kubernetes", ("Cluster overview", "k8s-overview"), ("Nodes & capacity", "k8s-nodes")),
             collapse=1)

    d.row("Control plane at a glance")
    d.line(4,
           (stat("API server", "API server targets up.", [T(f'sum(up{{{A}}})', "up"), T(f'count(up{{{A}}})', "total")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("Requests / s", "Requests per second served by kube-apiserver.",
                 [f'sum(rate(apiserver_request_total{{{A}}}[5m]))'], unit="reqps", color_mode="value", graph="area"), 3),
           (stat("5xx ratio", "Share of API requests answered with a 5xx in the last 5 minutes.",
                 [f'100 * sum(rate(apiserver_request_total{{{A}, code=~"5.."}}[5m])) / sum(rate(apiserver_request_total{{{A}}}[5m]))'],
                 unit="percent", th=steps((GREEN, None), (AMBER, 1), (RED, 5)), decimals=2), 3),
           (stat("Read p99", "99th percentile latency of non-watch read requests (GET / LIST).",
                 [f'histogram_quantile(0.99, sum by (le) (rate(apiserver_request_duration_seconds_bucket{{{A}, verb=~"GET|LIST"}}[5m])))'],
                 unit="s", th=steps((GREEN, None), (AMBER, 1), (RED, 5))), 3),
           (stat("APF rejected / s", "Requests rejected by API Priority and Fairness (429). Clients then retry and "
                 "the load gets worse.",
                 [f'sum(rate(apiserver_flowcontrol_rejected_requests_total{{{A}}}[5m])) or vector(0)'],
                 unit="reqps", th=OK_BAD, decimals=2), 3),
           (stat("etcd p99 (via API server)", "99th percentile latency of the API server's requests to etcd.",
                 [f'histogram_quantile(0.99, sum by (le) (rate(etcd_request_duration_seconds_bucket{{{A}}}[5m])))'],
                 unit="s", th=steps((GREEN, None), (AMBER, 0.1), (RED, 0.5))), 3),
           (stat("etcd database", "Size of the etcd database as the API server reports it.",
                 [f'max(apiserver_storage_size_bytes{{{A}}})'], unit="bytes", color_mode="value",
                 th=steps((GREEN, None), (AMBER, 4e9), (RED, 7e9))), 3),
           (stat("Control-plane restarts (24h)", "Restarts of the static control-plane pods and the cloud "
                 "controller manager in the last 24 hours. Restarts of the controller-manager / scheduler mean lost "
                 "leader-election leases.",
                 [f'sum(increase(kube_pod_container_status_restarts_total{{{KSM}, namespace="kube-system", pod=~"{CP}"}}[24h])) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 5)), decimals=0), 3))
    d.line(4,
           (stat("Kubelets", "Kubelets up.", [T('sum(up{job="kubelet", metrics_path="/metrics"})', "up"),
                                              T('count(up{job="kubelet", metrics_path="/metrics"})', "total")],
                 color_mode="value", text_mode="value_and_name"), 4),
           (stat("etcd has leader", "Whether the etcd members report a leader (only when etcd is scraped).",
                 [f'min(etcd_server_has_leader{{{ETCD}}})'], mappings=vmap((0, "NO LEADER", RED), (1, "yes", GREEN)),
                 no_value="not scraped"), 4),
           (stat("Controller manager", "kube-controller-manager targets up (only when it is scraped).",
                 ['sum(up{job="kube-controller-manager"})'], color_mode="value", no_value="not scraped"), 4),
           (stat("Scheduler", "kube-scheduler targets up (only when it is scraped).",
                 ['sum(up{job="kube-scheduler"})'], color_mode="value", no_value="not scraped"), 4),
           (stat("Objects in etcd", "Objects stored, summed over every resource type.",
                 [f'sum(max by (resource) (apiserver_resource_objects{{{A}}}))'], color_mode="value", graph="area"), 4),
           (stat("metrics-server", "metrics-server up: kubectl top and every HPA depend on it.",
                 ['sum(up{job="metrics-server"})'], mappings=vmap((0, "DOWN", RED)), color_mode="value",
                 no_value="not scraped"), 4))

    d.row("API server: load, objects & clients")
    d.line(8,
           (ts("Requests by verb", "Requests per second by verb.",
               [T(f'sum by (verb) (rate(apiserver_request_total{{{A}}}[$__rate_interval]))', "{{verb}}")],
               unit="reqps", stack=True), 8),
           (ts("Top resources by request rate", "The ten resources clients ask about most. An operator listing a "
               "big resource in a loop shows up here.",
               [T(f'topk(10, sum by (resource) (rate(apiserver_request_total{{{A}, resource!=""}}[$__rate_interval])))', "{{resource}}")],
               unit="reqps"), 8),
           (ts("Requests by priority level (APF)", "Requests dispatched per API Priority and Fairness priority "
               "level: system (kubelets, controllers), workload-high / -low (operators), global-default (everything "
               "else). One level dominating shows which class of client is driving the load.",
               [T(f'sum by (priority_level) (rate(apiserver_flowcontrol_dispatched_requests_total{{{A}}}[$__rate_interval]))', "{{priority_level}}")],
               unit="reqps", stack=True), 8))
    d.line(8,
           (ts("In flight & queued (APF)", "Requests executing per priority level, and requests waiting in APF "
               "queues. Queueing on workload-low / global-default is the first sign of an overloaded API server.",
               [T(f'sum by (priority_level) (apiserver_flowcontrol_current_executing_requests{{{A}}})', "{{priority_level}} executing"),
                T(f'sum by (priority_level) (apiserver_flowcontrol_current_inqueue_requests{{{A}}}) > 0', "{{priority_level}} queued")]), 8),
           (bargauge("Largest object counts", "The resources with the most objects stored. Hundreds of CRDs and "
                     "thousands of objects are what make a LIST, and Argo CD's cluster cache, expensive.",
                     [T(f'topk(15, max by (resource) (apiserver_resource_objects{{{A}}}))', "{{resource}}")],
                     unit="short", min=0, max=None, th=NEUTRAL, decimals=0), 8),
           (ts("Watches & latency p99 by verb", "Open WATCH requests (long-running, right) and 99th percentile request "
               "latency by verb (left). Thousands of watches is normal: every controller watches what it owns.",
               [T(f'histogram_quantile(0.99, sum by (verb, le) (rate(apiserver_request_duration_seconds_bucket{{{A}, verb!~"WATCH|CONNECT"}}[$__rate_interval])))', "{{verb}} p99"),
                T(f'sum(apiserver_longrunning_requests{{{A}, verb="WATCH"}})', "watchers")],
               unit="s", overrides=[{"matcher": {"id": "byName", "options": "watchers"},
                                     "properties": [{"id": "unit", "value": "short"},
                                                    {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    embed_mixin(d, "apiserver", row="API server SLOs (kubernetes-mixin)", var_map={"instance": "apiserver_instance"})

    d.row("etcd (as the API server sees it)")
    d.line(8,
           (ts("etcd request latency p99 by operation", "99th percentile latency of the API server's etcd calls by "
               "operation. kubeadm's etcd listens on localhost and is not scraped, so this is the etcd view that is "
               "always available; above ~100 ms the disk under etcd is too slow or the node is CPU-starved.",
               [T(f'histogram_quantile(0.99, sum by (operation, le) (rate(etcd_request_duration_seconds_bucket{{{A}}}[$__rate_interval])))', "{{operation}}")],
               unit="s", th=steps((GREEN, None), (AMBER, 0.1), (RED, 0.5))), 8),
           (ts("etcd database size", "Database size reported by the API server (apiserver_storage_size_bytes). The "
               "default etcd quota is 2 GiB on kubeadm (8 GiB maximum); at the quota etcd goes read-only.",
               [T(f'max by (storage_cluster_id) (apiserver_storage_size_bytes{{{A}}})', "{{storage_cluster_id}}")],
               unit="bytes", th=steps((GREEN, None), (AMBER, 1.5e9), (RED, 2e9))), 8),
           (ts("Objects stored over time (top 10)", "Object counts of the ten largest resources. Steady growth of "
               "one kind (Events, ReplicaSets, PolicyReports, Tekton runs) is what grows etcd.",
               [T(f'topk(10, max by (resource) (apiserver_resource_objects{{{A}}}))', "{{resource}}")], decimals=0), 8))

    embed_mixin(d, "kubelet", row="Kubelets (kubernetes-mixin)", var_map={"instance": "kubelet_instance"})

    d.row("Control-plane pods")
    d.line(8, (pods_table("Control-plane pods", "Static control-plane pods and the cloud controller manager.",
                          CP, ns='namespace="kube-system"'), 16),
           (ts("Control-plane CPU", "CPU used by each control-plane component.",
               [T(f'sum by (pod) (rate(container_cpu_usage_seconds_total{{{CAD}, namespace="kube-system", container!="", pod=~"{CP}"}}[$__rate_interval]))', "{{pod}}")],
               unit="cores", stack=True), 8))

    d.row("metrics-server (resource metrics API)")
    MS = 'job="metrics-server"'
    d.line(8,
           (ts("Kubelet scrapes", "Scrapes of the kubelets' resource endpoints per second, by success. Failures "
               "leave `kubectl top` and every HPA without data for that node.",
               [T(f'sum by (success) (rate(metrics_server_kubelet_request_total{{{MS}}}[$__rate_interval]))', "success={{success}}")],
               unit="ops", overrides=[ov_color("success=false", RED)]), 6),
           (ts("Kubelet scrape latency p99", "99th percentile time to scrape one kubelet, per node.",
               [T(f'histogram_quantile(0.99, sum by (node, le) (rate(metrics_server_kubelet_request_duration_seconds_bucket{{{MS}}}[$__rate_interval])))', "{{node}}")],
               unit="s"), 6),
           (ts("Metric freshness p99", "Age of the metrics the API serves (scrape interval + latency).",
               [T(f'histogram_quantile(0.99, sum by (le) (rate(metrics_server_api_metric_freshness_seconds_bucket{{{MS}}}[$__rate_interval])))', "p99")],
               unit="s"), 6),
           (ts("Stored points", "Node and container points metrics-server holds in memory.",
               [T(f'sum by (type) (metrics_server_storage_points{{{MS}}})', "{{type}}")], decimals=0), 6))
    d.line(7, *[(p, 8) for p in go_process("metrics-server")])
    return d


# =============================================================================================
# Storage
# =============================================================================================

def storage():
    NSV = 'namespace=~"$namespace"'
    VS = 'job="kubelet", metrics_path="/metrics"'
    d = Dash(UIDS["k8s-storage"], "Kubernetes / Storage",
             "PersistentVolumeClaims and the disks behind them. Most platform volumes are node-local (the "
             "adhar-local StorageClass on cloud clusters, local-path on Kind): they report NO kubelet volume "
             "statistics, so their fill level is the node filesystem's, shown per node below. Block volumes "
             "(adhar-block) report their own usage. The attach budget per node (7-16 disks) is what limits "
             "block volumes, not CPU.",
             tags("Kubernetes", "kubernetes", "storage"),
             [ds_var(), cluster_var(),
              qvar("namespace", "Namespace", f'label_values(kube_persistentvolumeclaim_info{{{KSM}}}, namespace)'),
              qvar("storageclass", "StorageClass", f'label_values(kube_persistentvolumeclaim_info{{{KSM}}}, storageclass)'),
              qvar("node", "Node", f'label_values(kube_node_info{{{KSM}}}, node)')],
             links=links("Kubernetes", ("Nodes & capacity", "k8s-nodes"), ("Workloads & pods", "k8s-workloads")),
             collapse={"Storage at a glance", "Claims"})

    PVC = f'{KSM}, {NSV}'
    d.row("Storage at a glance")
    d.line(4,
           (stat("Claims bound", "PersistentVolumeClaims in phase Bound.",
                 [f'sum(kube_persistentvolumeclaim_status_phase{{{PVC}, phase="Bound"}}) or vector(0)'], color_mode="value"), 4),
           (stat("Claims pending", "Claims waiting for a volume: no provisioner, no capacity, or a zone / attach "
                 "limit. A Pending claim keeps its pod Pending.",
                 [f'sum(kube_persistentvolumeclaim_status_phase{{{PVC}, phase="Pending"}}) or vector(0)'], th=OK_BAD), 4),
           (stat("Claims lost", "Claims whose volume disappeared (phase Lost).",
                 [f'sum(kube_persistentvolumeclaim_status_phase{{{PVC}, phase="Lost"}}) or vector(0)'], th=OK_BAD), 4),
           (stat("Requested capacity", "Storage requested by all claims in the selection.",
                 [f'sum(kube_persistentvolumeclaim_resource_requests_storage_bytes{{{PVC}}})'], unit="bytes", color_mode="value"), 4),
           (stat("Volumes over 80%", "Volumes that report usage (block / CSI) and are more than 80% full.",
                 [f'count(kubelet_volume_stats_used_bytes{{{VS}, {NSV}}} / kubelet_volume_stats_capacity_bytes{{{VS}, {NSV}}} > 0.8) or vector(0)'],
                 th=OK_BAD), 4),
           (stat("Fullest node filesystem", "Root filesystem fill of the fullest node — where node-local volumes live.",
                 ['max(1 - node_filesystem_avail_bytes{mountpoint="/", fstype!="rootfs"} / node_filesystem_size_bytes{mountpoint="/", fstype!="rootfs"}) * 100'],
                 unit="percent", th=PCT, decimals=1), 4))

    d.row("Claims")
    info = f'kube_persistentvolumeclaim_info{{{KSM}, {NSV}, storageclass=~"$storageclass"}}'
    d.line(10, (table(
        "PersistentVolumeClaims", "Every claim: StorageClass, phase, requested size, and — for volumes that report "
        "it — used bytes and fill. The node column is where a node-local volume lives (and where its pod must run).",
        [f'max by (namespace, persistentvolumeclaim, storageclass, volumename) ({info})',
         f'max by (namespace, persistentvolumeclaim, phase) (kube_persistentvolumeclaim_status_phase{{{PVC}}} == 1) * on (namespace, persistentvolumeclaim) group_left () max by (namespace, persistentvolumeclaim) ({info})',
         f'max by (namespace, persistentvolumeclaim) (kube_persistentvolumeclaim_resource_requests_storage_bytes{{{PVC}}}) * on (namespace, persistentvolumeclaim) group_left () max by (namespace, persistentvolumeclaim) ({info})',
         f'max by (namespace, persistentvolumeclaim) (kubelet_volume_stats_used_bytes{{{VS}, {NSV}}})',
         f'100 * max by (namespace, persistentvolumeclaim) (kubelet_volume_stats_used_bytes{{{VS}, {NSV}}}) / max by (namespace, persistentvolumeclaim) (kubelet_volume_stats_capacity_bytes{{{VS}, {NSV}}})',
         f'max by (namespace, persistentvolumeclaim, node) (kubelet_volume_stats_capacity_bytes{{{VS}, {NSV}}})'],
        [("namespace", "Namespace", None, None), ("persistentvolumeclaim", "Claim", None, None),
         ("storageclass", "StorageClass", None, None), ("phase", "Phase", None, None),
         ("Value #C", "Requested", "bytes", None), ("Value #D", "Used", "bytes", {"novalue": "n/a"}),
         ("Value #E", "Used %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("node", "Node", None, None), ("volumename", "Volume", None, None)],
        sort=("Requested", True), hide=("Value #A", "Value #B", "Value #F")), 24))
    d.line(8,
           (bargauge("Requested capacity by StorageClass", "Storage requested per StorageClass.",
                     [T(f'sum by (storageclass) (kube_persistentvolumeclaim_resource_requests_storage_bytes{{{PVC}}} * on (namespace, persistentvolumeclaim) group_left (storageclass) {info})', "{{storageclass}}")],
                     unit="bytes", min=0, max=None, th=NEUTRAL, decimals=0), 8),
           (bargauge("Requested capacity by namespace", "Storage requested per namespace (top 15).",
                     [T(f'topk(15, sum by (namespace) (kube_persistentvolumeclaim_resource_requests_storage_bytes{{{PVC}}}))', "{{namespace}}")],
                     unit="bytes", min=0, max=None, th=NEUTRAL, decimals=0), 8),
           (ts("Claims by phase", "Claims per phase over time.",
               [T(f'sum by (phase) (kube_persistentvolumeclaim_status_phase{{{PVC}}})', "{{phase}}")],
               stack=True, decimals=0, overrides=[ov_color("Bound", GREEN), ov_color("Pending", AMBER), ov_color("Lost", RED)]), 8))

    d.row("Volume usage (volumes that report it)")
    d.line(8,
           (ts("Fullest volumes", "Fill of the ten fullest volumes that report kubelet volume statistics.",
               [T(f'topk(10, max by (namespace, persistentvolumeclaim) (kubelet_volume_stats_used_bytes{{{VS}, {NSV}}} / kubelet_volume_stats_capacity_bytes{{{VS}, {NSV}}}))', "{{namespace}}/{{persistentvolumeclaim}}")],
               unit="percentunit", max=1, th=steps((GREEN, None), (AMBER, 0.8), (RED, 0.9))), 12),
           (ts("Days until full (linear forecast)", "When each reporting volume fills at its growth rate over the "
               "last 6 hours. Only volumes that are growing are shown.",
               [T(f'max by (namespace, persistentvolumeclaim) (kubelet_volume_stats_available_bytes{{{VS}, {NSV}}}) / '
                  f'(-1 * max by (namespace, persistentvolumeclaim) (deriv(kubelet_volume_stats_available_bytes{{{VS}, {NSV}}}[6h])) > 0) / 86400', "{{namespace}}/{{persistentvolumeclaim}}")],
               unit="d", th=steps((RED, None), (AMBER, 7), (GREEN, 30))), 12))

    embed_mixin(d, "persistentvolumesusage", row="Volume detail (kubernetes-mixin)",
                var_map={"namespace": "volume_namespace"})
    d.add_variables([
        qvar("volume_namespace", "Volume namespace", 'label_values(kubelet_volume_stats_capacity_bytes{job="kubelet", metrics_path="/metrics"}, namespace)',
             multi=False, include_all=False, hide=0),
        qvar("volume", "Volume", 'label_values(kubelet_volume_stats_capacity_bytes{job="kubelet", metrics_path="/metrics", namespace="$volume_namespace"}, persistentvolumeclaim)',
             multi=False, include_all=False)])

    d.row("Node-local disks & attach limits")
    d.line(8,
           (bargauge("Node root filesystem used", "Where node-local volumes live. A full root filesystem means the "
                     "kubelet starts evicting pods (DiskPressure) and databases on that node stop writing.",
                     [T(ne('100 * (1 - node_filesystem_avail_bytes{mountpoint="/", fstype!="rootfs"} / node_filesystem_size_bytes{mountpoint="/", fstype!="rootfs"})'), "{{nodename}}")]), 8),
           (bargauge("Volumes mounted per node", "Volumes the kubelet has mounted per node (all plugins). Block "
                     "volumes count against the VM's attach limit (7 on a DigitalOcean droplet, 8-16 on Azure/AWS).",
                     [T(f'sum by (node) (volume_manager_total_volumes{{{VS}, node=~"$node", state="actual_state_of_world", plugin_name=~"kubernetes.io/csi.*"}})', "{{node}}")],
                     unit="short", min=0, max=16, th=steps((GREEN, None), (AMBER, 6), (RED, 7)), decimals=0), 8),
           (ts("Volume operations", "Kubelet / CSI volume operations per second and their 99th percentile duration. "
               "Slow attach / mount is a pod stuck in ContainerCreating.",
               [T(f'sum by (operation_name) (rate(storage_operation_duration_seconds_count{{{VS}, node=~"$node"}}[$__rate_interval]))', "{{operation_name}}"),
                T(f'histogram_quantile(0.99, sum by (operation_name, le) (rate(storage_operation_duration_seconds_bucket{{{VS}, node=~"$node"}}[$__rate_interval])))', "{{operation_name}} p99")],
               overrides=[{"matcher": {"id": "byRegexp", "options": ".* p99"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"},
                                          {"id": "custom.lineStyle", "value": {"dash": [10, 10], "fill": "dash"}}]}]), 8))
    d.line(8,
           (ts("Failed volume operations", "Volume operations that failed, by operation and plugin.",
               [T(f'sum by (operation_name, volume_plugin) (rate(storage_operation_duration_seconds_count{{{VS}, node=~"$node", status!="success"}}[$__rate_interval])) > 0', "{{operation_name}} {{volume_plugin}}")],
               unit="ops", th=steps((GREEN, None), (RED, 0.001))), 12),
           (ts("CSI operations p99", "99th percentile duration of CSI calls (attach, stage, publish) by method.",
               [T(f'histogram_quantile(0.99, sum by (method_name, le) (rate(csi_operations_seconds_bucket{{{VS}, node=~"$node"}}[$__rate_interval])))', "{{method_name}}")],
               unit="s"), 12))
    return d


DASHBOARDS = [
    ("k8s-cluster-overview", "Kubernetes", overview),
    ("k8s-nodes", "Kubernetes", nodes),
    ("k8s-workloads", "Kubernetes", workloads),
    ("k8s-control-plane", "Kubernetes", control_plane),
    ("k8s-storage", "Kubernetes", storage),
]
