#!/usr/bin/env python3
"""Builds the "Adhar Platform" front-page dashboard into
manifests/dashboard-adhar-platform.yaml.

This file IS the source of that dashboard. It used to be produced by a script
that lived outside the repository, so every fix was a hand-edit of a 300 KB
JSON blob; edit this instead and re-run it:

    python3 build-platform-dashboard.py

Rules the layout follows (pinned by platform_dashboard_test.go):
  * fixed datasource uids only — prometheus, mimir, loki, tempo — the ones
    manifests/extra-datasources.yaml and the chart provision;
  * every panel has a description, a unit and, where a value can be "bad",
    thresholds with the platform's green/amber/red;
  * the first two rows open, the rest collapsed, so the page loads fast and
    a collapsed row only queries when someone opens it;
  * active-pod recording rules (cluster:namespace:pod_*:active:*,
    node_namespace_pod_container:*) instead of raw cAdvisor/KSM series,
    which also count Completed pods and overstated commitment by 2x;
  * every query was checked against a live cluster (DigitalOcean, 2026-10-23).
    Panels for components a profile may not run end in `or vector(0)` or say
    in their description what feeds them, so they read 0 rather than break.
"""
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "manifests", "dashboard-adhar-platform.yaml")

PROM = {"type": "prometheus", "uid": "prometheus"}
MIMIR = {"type": "prometheus", "uid": "mimir"}
LOKI = {"type": "loki", "uid": "loki"}
# The custom applications' logs (dash_applications.py, Applications folder). Pinned
# first in the link bar and on the panels that make someone want the lines behind them.
APP_LOGS = {"title": "Application Logs", "url": "/d/adhar-app-logs?${__url_time_range}", "targetBlank": False}
TEMPO = {"type": "tempo", "uid": "tempo"}

GREEN, AMBER, RED, BLUE, GREY = "green", "#EAB839", "red", "#5794F2", "text"


def steps(*pairs):
    """steps((None,'green'),(80,'amber'),...) -> Grafana threshold steps."""
    return {"mode": "absolute", "steps": [{"color": c, "value": v} for v, c in pairs]}


OK_AT_ZERO = steps((None, GREEN), (1, RED))
WARN_AT_ONE = steps((None, GREEN), (1, AMBER), (5, RED))
UTIL = steps((None, GREEN), (70, AMBER), (85, RED))
UTIL_RATIO = steps((None, GREEN), (0.7, AMBER), (0.85, RED))
NEUTRAL = steps((None, BLUE))
HEALTH_RATIO = steps((None, RED), (0.95, AMBER), (0.999, GREEN))

# Core platform services, one line each in the availability timeline.
CORE_DEPLOYMENTS = [
    ("Argo CD server", "argo-cd-argocd-server"), ("Argo CD repo-server", "argo-cd-argocd-repo-server"),
    ("Gitea", "gitea"), ("Keycloak", "keycloak"), ("Harbor", "harbor-core"),
    ("Grafana", "prometheus-grafana"), ("Kyverno", "kyverno-admission-controller"),
    ("cert-manager", "cert-manager"), ("RustFS", "rustfs"), ("Mimir gateway", "mimir-gateway"),
    ("External Secrets", "external-secrets"), ("Crossplane", "crossplane"),
    ("Kargo", "kargo-controller"), ("Tekton", "adhar-system-controller"),
    ("Adhar Console", "adhar-console"), ("Adhar controller", "adhar-controller-manager"),
    ("Cilium operator", "cilium-operator"),
]
CORE_STATEFULSETS = [
    ("Argo CD controller", "argo-cd-argocd-application-controller"), ("Prometheus", "prometheus-prometheus-kube-prometheus-prometheus"),
    ("Loki", "loki"), ("Tempo", "tempo"), ("Mimir ingester", "mimir-ingester"), ("OpenBao", "openbao"),
    ("Alertmanager", "alertmanager-prometheus-kube-prometheus-alertmanager"),
]


class Board:
    def __init__(self):
        self.panels = []
        self.row_panel = None
        self.y = 0
        self.x = 0
        self.row_h = 0
        self._id = 0

    def nid(self):
        self._id += 1
        return self._id

    # ---- layout -----------------------------------------------------------------
    def row(self, title, collapsed=False, desc=""):
        self._newline()
        row = {"id": self.nid(), "type": "row", "title": title, "collapsed": collapsed,
               "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": []}
        if desc:
            row["description"] = desc
        self.panels.append(row)
        self.row_panel = row if collapsed else None
        self.y += 1

    def _newline(self):
        if self.x:
            self.y += self.row_h
            self.x = 0
            self.row_h = 0

    def _place(self, w, h):
        if self.x + w > 24:
            self._newline()
        gp = {"h": h, "w": w, "x": self.x, "y": self.y}
        self.x += w
        self.row_h = max(self.row_h, h)
        if self.x >= 24:
            self._newline()
        return gp

    def _add(self, panel):
        (self.row_panel["panels"] if self.row_panel else self.panels).append(panel)
        return panel

    # ---- panel kinds ---------------------------------------------------------------
    def _base(self, kind, title, desc, w, h, ds):
        return {"id": self.nid(), "type": kind, "title": title, "description": desc,
                "datasource": ds, "gridPos": self._place(w, h)}

    @staticmethod
    def _targets(ds, exprs, instant=False, fmt=None):
        out = []
        for i, e in enumerate(exprs):
            expr, legend = (e if isinstance(e, tuple) else (e, ""))
            t = {"datasource": ds, "refId": chr(65 + i), "expr": expr, "legendFormat": legend or "__auto"}
            if instant:
                t["instant"] = True
                t["range"] = False
            if fmt:
                t["format"] = fmt
            out.append(t)
        return out

    def stat(self, title, expr, desc, unit="short", w=4, h=4, th=NEUTRAL, decimals=0,
             ds=PROM, graph=False, text_mode="value", mappings=None, color_mode=None):
        p = self._base("stat", title, desc, w, h, ds)
        # Rows can switch the default (the front row fills each tile with its
        # status colour; the rest of the dashboard keeps coloured values).
        color_mode = color_mode or getattr(self, "stat_color_mode", "value")
        p["targets"] = self._targets(ds, [expr], instant=not graph)
        p["fieldConfig"] = {"defaults": {"unit": unit, "decimals": decimals, "thresholds": th,
                                         "color": {"mode": "thresholds"}, "mappings": mappings or [],
                                         "noValue": "0"}, "overrides": []}
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "colorMode": color_mode, "graphMode": "area" if graph else "none",
                        "textMode": text_mode, "justifyMode": "center", "orientation": "auto",
                        "showPercentChange": False, "wideLayout": True}
        return self._add(p)

    def gauge(self, title, expr, desc, unit="percent", w=4, h=6, th=UTIL, mx=100, decimals=1):
        p = self._base("gauge", title, desc, w, h, PROM)
        p["targets"] = self._targets(PROM, [expr], instant=True)
        p["fieldConfig"] = {"defaults": {"unit": unit, "min": 0, "max": mx, "decimals": decimals,
                                         "thresholds": th, "color": {"mode": "thresholds"}}, "overrides": []}
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "showThresholdLabels": False, "showThresholdMarkers": True}
        return self._add(p)

    def ts(self, title, exprs, desc, unit="short", w=12, h=8, ds=PROM, stack=False, decimals=None,
           legend="list", mx=None, th=None, draw="line", overrides=None, mn=None):
        p = self._base("timeseries", title, desc, w, h, ds)
        p["targets"] = self._targets(ds, exprs)
        custom = {"drawStyle": draw, "lineWidth": 1, "fillOpacity": 12 if not stack else 35,
                  "gradientMode": "opacity", "showPoints": "never", "spanNulls": True,
                  "stacking": {"mode": "normal" if stack else "none", "group": "A"},
                  "axisSoftMin": 0 if mn is None else mn}
        if th:
            custom["thresholdsStyle"] = {"mode": "line+area" if False else "dashed"}
        d = {"unit": unit, "custom": custom, "color": {"mode": "palette-classic"}}
        if decimals is not None:
            d["decimals"] = decimals
        if mx is not None:
            d["max"] = mx
        if mn is not None:
            d["min"] = mn
        if th:
            d["thresholds"] = th
        p["fieldConfig"] = {"defaults": d, "overrides": overrides or []}
        legend_opts = {"showLegend": True, "displayMode": "list", "placement": "bottom", "calcs": []}
        if legend == "table":
            legend_opts = {"showLegend": True, "displayMode": "table", "placement": "right",
                           "calcs": ["lastNotNull", "max"], "sortBy": "Max", "sortDesc": True}
        elif legend == "hidden":
            legend_opts = {"showLegend": False, "displayMode": "list", "placement": "bottom", "calcs": []}
        p["options"] = {"legend": legend_opts, "tooltip": {"mode": "multi", "sort": "desc"}}
        return self._add(p)

    def bargauge(self, title, expr, desc, unit="percent", w=8, h=8, th=UTIL, mx=100, legend="", decimals=1):
        p = self._base("bargauge", title, desc, w, h, PROM)
        p["targets"] = self._targets(PROM, [(expr, legend)], instant=True)
        p["fieldConfig"] = {"defaults": {"unit": unit, "min": 0, "max": mx, "decimals": decimals,
                                         "thresholds": th, "color": {"mode": "thresholds"}}, "overrides": []}
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "orientation": "horizontal", "displayMode": "gradient", "showUnfilled": True,
                        "valueMode": "color", "namePlacement": "left", "sizing": "auto", "minVizHeight": 14,
                        "maxVizHeight": 22}
        return self._add(p)

    def table(self, title, queries, desc, w=12, h=8, rename=None, units=None, hide=None, sort=None,
              ds=PROM, th=None, cell=None):
        """queries: [(expr, column name)] — instant table queries merged on their labels."""
        p = self._base("table", title, desc, w, h, ds)
        targets = []
        for i, (expr, _) in enumerate(queries):
            targets.append({"datasource": ds, "refId": chr(65 + i), "expr": expr, "format": "table",
                            "instant": True, "range": False})
        p["targets"] = targets
        renames = {"Time": "", **(rename or {})}
        exclude = {"Time": True}
        for h_ in (hide or []):
            exclude[h_] = True
        if len(queries) == 1:
            renames["Value"] = queries[0][1]
        else:
            for i, (_, col) in enumerate(queries):
                renames[f"Value #{chr(65 + i)}"] = col
        p["transformations"] = ([{"id": "merge", "options": {}}] if len(queries) > 1 else []) + [
            {"id": "organize", "options": {"excludeByName": exclude, "renameByName": renames}}]
        overrides = []
        for col, (unit, th_, mode) in (units or {}).items():
            props = [{"id": "unit", "value": unit}]
            if th_:
                props.append({"id": "thresholds", "value": th_})
                props.append({"id": "custom.cellOptions", "value": {"type": mode or "color-text"}})
            overrides.append({"matcher": {"id": "byName", "options": col}, "properties": props})
        p["fieldConfig"] = {"defaults": {"custom": {"align": "auto", "filterable": True,
                                                    "cellOptions": {"type": "auto"}},
                                         "thresholds": th or NEUTRAL, "decimals": 2},
                            "overrides": overrides}
        p["options"] = {"showHeader": True, "cellHeight": "sm", "footer": {"show": False},
                        "sortBy": [{"displayName": sort[0], "desc": sort[1]}] if sort else []}
        return self._add(p)

    def state(self, title, expr, desc, w=24, h=8, legend="", mappings=None, th=None):
        p = self._base("state-timeline", title, desc, w, h, PROM)
        p["targets"] = self._targets(PROM, [(expr, legend)])
        p["fieldConfig"] = {"defaults": {"color": {"mode": "thresholds"}, "thresholds": th or steps((None, RED), (1, GREEN)),
                                         "mappings": mappings or [], "custom": {"fillOpacity": 75, "lineWidth": 0}},
                            "overrides": []}
        p["options"] = {"showValue": "never", "mergeValues": True, "alignValue": "left", "rowHeight": 0.85,
                        "legend": {"showLegend": False, "displayMode": "list", "placement": "bottom"},
                        "tooltip": {"mode": "single", "sort": "none"}}
        return self._add(p)

    def pie(self, title, expr, desc, legend, w=6, h=8, overrides=None):
        p = self._base("piechart", title, desc, w, h, PROM)
        p["targets"] = self._targets(PROM, [(expr, legend)], instant=True)
        p["fieldConfig"] = {"defaults": {"unit": "short", "color": {"mode": "palette-classic"}},
                            "overrides": overrides or []}
        p["options"] = {"pieType": "donut", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "legend": {"showLegend": True, "displayMode": "table", "placement": "right",
                                   "values": ["value", "percent"]},
                        "tooltip": {"mode": "single", "sort": "none"}, "displayLabels": []}
        return self._add(p)

    def logs(self, title, expr, desc, w=24, h=9):
        p = self._base("logs", title, desc, w, h, LOKI)
        p["targets"] = [{"datasource": LOKI, "refId": "A", "expr": expr, "queryType": "range"}]
        p["options"] = {"showTime": True, "wrapLogMessage": True, "prettifyLogMessage": False,
                        "enableLogDetails": True, "dedupStrategy": "signature", "sortOrder": "Descending"}
        return self._add(p)

    def text(self, content, w=24, h=3, title=""):
        p = {"id": self.nid(), "type": "text", "title": title, "gridPos": self._place(w, h),
             "options": {"mode": "markdown", "content": content}}
        return self._add(p)


def ratio5xx(by, sel, window):
    """5xx share that reads 0 (not "no data") for a service with traffic and no errors."""
    total = f'sum by ({by}) (rate(http_server_requests_seconds_count{{{sel}}}[{window}]))'
    errors = f'sum by ({by}) (rate(http_server_requests_seconds_count{{{sel},status=~"5.."}}[{window}]))'
    return f'({errors} or {total} * 0) / {total}'


def color_override(name, color):
    return {"matcher": {"id": "byName", "options": name},
            "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": color}}]}


def build():
    b = Board()
    NS = 'namespace=~"$namespace"'
    NODE = 'node=~"$node"'
    APPS_NS = 'namespace=~"$app_namespace"'
    # application!="" keeps Spring/Micrometer apps that set the common tag (every
    # adhar-kit app) and drops Keycloak, which emits the same metric name in
    # adhar-system without it.
    HTTP_FILTER = 'application!="",uri!~"/actuator.*"'

    # =========================================================== at a glance ====
    b.row("Platform at a glance", desc="Is the platform healthy right now? Every tile is red/amber only when something needs a person.")
    # The front row reads at a glance across a room: each tile's whole
    # background takes its threshold colour (green / amber / red).
    b.stat_color_mode = "background"
    b.stat("Nodes Ready", 'sum(kube_node_status_condition{condition="Ready",status="true"})',
           "Nodes whose kubelet reports Ready. Compare with Node count: a gap is a NotReady node.",
           th=steps((None, RED), (1, GREEN)))
    b.stat("NotReady Nodes", 'count(kube_node_status_condition{condition="Ready",status!="true"} == 1) or vector(0)',
           "Nodes reporting Ready=false or Unknown.", th=OK_AT_ZERO)
    b.stat("Apps Healthy", 'sum(argocd_app_info{health_status="Healthy"}) / count(argocd_app_info)',
           "Share of Argo CD Applications that are Healthy (platform packages + custom apps).",
           unit="percentunit", decimals=1, th=HEALTH_RATIO)
    b.stat("Apps Degraded", 'count(argocd_app_info{health_status=~"Degraded|Missing"}) or vector(0)',
           "Argo CD Applications in Degraded or Missing health. Details in GitOps & Delivery.", th=WARN_AT_ONE)
    b.stat("Apps Out of Sync", 'count(argocd_app_info{sync_status!="Synced"}) or vector(0)',
           "Applications whose live state differs from Git (OutOfSync or Unknown). A burst after a Gitea push is expected for ~10 min.",
           th=steps((None, GREEN), (1, AMBER), (10, RED)))
    b.stat("Alerts Firing", 'count(ALERTS{alertstate="firing",alertname!~"Watchdog|InfoInhibitor"}) or vector(0)',
           "Prometheus alerts currently firing (Watchdog/InfoInhibitor excluded). Open Alertmanager for routing.",
           th=steps((None, GREEN), (1, AMBER), (5, RED)))

    b.stat("Pending Pods", 'count(kube_pod_status_phase{phase="Pending"} == 1) or vector(0)',
           "Pods not yet running. Unschedulable ones are the node autoscaler's signal; see Workload Reliability.",
           th=steps((None, GREEN), (1, AMBER), (10, RED)))
    b.stat("CrashLooping", 'count(kube_pod_container_status_waiting_reason{reason="CrashLoopBackOff"} == 1) or vector(0)',
           "Containers in CrashLoopBackOff now.", th=WARN_AT_ONE)
    b.stat("OOMKilled (last exit)", 'count(kube_pod_container_status_last_terminated_reason{reason="OOMKilled"} == 1) or vector(0)',
           "Containers whose most recent termination was an OOM kill: raise the memory limit or find the leak.", th=WARN_AT_ONE)
    b.stat("Evicted Pods", 'count(kube_pod_status_reason{reason="Evicted"} == 1) or vector(0)',
           "Pods evicted by the kubelet (disk/memory pressure) and not yet garbage-collected.", th=WARN_AT_ONE)
    b.stat("Restarts (1h)", 'round(sum(increase(kube_pod_container_status_restarts_total[1h]))) or vector(0)',
           "Container restarts across the cluster in the last hour.", th=steps((None, GREEN), (5, AMBER), (25, RED)))
    b.stat("Scrape Targets Down", 'count(up == 0) or vector(0)',
           "Prometheus targets failing their scrape. A down target means a blind spot, not necessarily a down service.",
           th=WARN_AT_ONE)

    b.gauge("CPU Used", '100 * cluster:node_cpu:ratio_rate5m',
            "Actual CPU use across all nodes (node-exporter).", w=4)
    b.gauge("CPU Requested", '100 * sum(cluster:namespace:pod_cpu:active:kube_pod_container_resource_requests) / sum(max by (node) (kube_node_status_allocatable{resource="cpu"}))',
            "CPU requested by RUNNING pods over allocatable. This is what the scheduler sees: near 100% means new pods go Pending even when CPU Used is low.",
            w=4, th=steps((None, GREEN), (80, AMBER), (95, RED)))
    b.gauge("Memory Used", '100 * (1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes))',
            "Memory in use across nodes (MemTotal - MemAvailable).", w=4)
    b.gauge("Memory Requested", '100 * sum(cluster:namespace:pod_memory:active:kube_pod_container_resource_requests) / sum(max by (node) (kube_node_status_allocatable{resource="memory"}))',
            "Memory requested by running pods over allocatable.", w=4, th=steps((None, GREEN), (80, AMBER), (95, RED)))
    b.ts("Cluster Utilisation", [
        ('100 * cluster:node_cpu:ratio_rate5m', "CPU used"),
        ('100 * sum(cluster:namespace:pod_cpu:active:kube_pod_container_resource_requests) / sum(max by (node) (kube_node_status_allocatable{resource="cpu"}))', "CPU requested"),
        ('100 * (1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes))', "Memory used"),
        ('100 * sum(cluster:namespace:pod_memory:active:kube_pod_container_resource_requests) / sum(max by (node) (kube_node_status_allocatable{resource="memory"}))', "Memory requested"),
    ], "Used vs requested, as a share of allocatable. A large gap between requested and used is reservable capacity nobody uses.",
        unit="percent", w=8, h=6, mx=100, decimals=1)

    core = " or ".join(
        [f'label_replace(min(kube_deployment_status_replicas_available{{namespace="adhar-system",deployment="{d}"}} >= bool kube_deployment_spec_replicas{{namespace="adhar-system",deployment="{d}"}}), "component", "{n}", "", "")' for n, d in CORE_DEPLOYMENTS]
        + [f'label_replace(min(kube_statefulset_status_replicas_ready{{namespace="adhar-system",statefulset="{s}"}} >= bool kube_statefulset_replicas{{namespace="adhar-system",statefulset="{s}"}}), "component", "{n}", "", "")' for n, s in CORE_STATEFULSETS])
    b.state("Core Platform Services", core,
            "Green = every desired replica is available. Red = at least one replica missing. A service the profile does not run has no line.",
            h=10, legend="{{component}}",
            mappings=[{"type": "value", "options": {"0": {"text": "Degraded", "color": RED}, "1": {"text": "Available", "color": GREEN}}}])

    b.stat_color_mode = "value"
    # =========================================================== capacity ====
    b.row("Capacity & Nodes", desc="Requests vs usage per node: requests decide scheduling, usage decides performance.")
    b.stat("Nodes", 'count(kube_node_info)', "Nodes registered with the API server.", w=4)
    b.stat("CPU Allocatable", 'sum(max by (node) (kube_node_status_allocatable{resource="cpu"}))', "Schedulable CPU cores across all nodes.",
           unit="none", w=4, decimals=1)
    b.stat("Memory Allocatable", 'sum(max by (node) (kube_node_status_allocatable{resource="memory"}))', "Schedulable memory across all nodes.",
           unit="bytes", w=4, decimals=1)
    b.stat("Pod Slots Used", '100 * sum(kubelet_running_pods) / sum(max by (node) (kube_node_status_allocatable{resource="pods"}))',
           "Running pods over the nodes' maxPods.", unit="percent", w=4, decimals=1, th=UTIL)
    b.stat("Unschedulable Pods", 'count(kube_pod_status_unschedulable == 1) or vector(0)',
           "Pods the scheduler cannot place (PodScheduled=False/Unschedulable). The node autoscaler acts on these.", w=4, th=WARN_AT_ONE)
    b.stat("Worst Node Disk", 'max(100 * (1 - node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs|ramfs"} / node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs|ramfs"}))',
           "Fullest root filesystem. Platform volumes are node-local (adhar-local), so this is also PV headroom.",
           unit="percent", w=4, decimals=1, th=steps((None, GREEN), (75, AMBER), (85, RED)))

    pct_cell = ("percent", steps((None, GREEN), (70, AMBER), (85, RED)), "color-background")
    b.table("Node Requests vs Usage", [
        (f'max by (node) (kube_node_status_allocatable{{resource="cpu",{NODE}}})', "CPU alloc"),
        (f'sum by (node) (cluster:namespace:pod_cpu:active:kube_pod_container_resource_requests{{{NODE}}})', "CPU req"),
        (f'sum by (node) (node_namespace_pod_container:container_cpu_usage_seconds_total:sum_rate5m{{{NODE}}})', "CPU used"),
        (f'100 * sum by (node) (cluster:namespace:pod_cpu:active:kube_pod_container_resource_requests{{{NODE}}}) / max by (node) (kube_node_status_allocatable{{resource="cpu",{NODE}}})', "CPU req %"),
        (f'max by (node) (kube_node_status_allocatable{{resource="memory",{NODE}}})', "Mem alloc"),
        (f'sum by (node) (cluster:namespace:pod_memory:active:kube_pod_container_resource_requests{{{NODE}}})', "Mem req"),
        (f'sum by (node) (node_namespace_pod_container:container_memory_working_set_bytes{{{NODE}}})', "Mem used"),
        (f'100 * sum by (node) (cluster:namespace:pod_memory:active:kube_pod_container_resource_requests{{{NODE}}}) / max by (node) (kube_node_status_allocatable{{resource="memory",{NODE}}})', "Mem req %"),
        (f'sum by (node) (kubelet_running_pods{{{NODE}}})', "Pods"),
    ], "Per node: allocatable, what running pods REQUEST, and what they actually USE (5m). Requested % near 100 with low use = over-reserved; used near alloc = hot node.",
        w=24, h=9, rename={"node": "Node"},
        units={"CPU alloc": ("none", None, None), "CPU req": ("none", None, None), "CPU used": ("none", None, None),
               "CPU req %": pct_cell, "Mem alloc": ("bytes", None, None), "Mem req": ("bytes", None, None),
               "Mem used": ("bytes", None, None), "Mem req %": pct_cell, "Pods": ("none", None, None)},
        sort=("CPU req %", True))
    b.ts("CPU Utilisation by Node", [(f'100 * instance:node_cpu_utilisation:rate5m * on(instance) group_left(nodename) node_uname_info{{nodename=~"$node"}}', "{{nodename}}")],
         "CPU busy per node (node-exporter).", unit="percent", mx=100, decimals=1, legend="table")
    b.ts("Memory Utilisation by Node", [(f'100 * instance:node_memory_utilisation:ratio * on(instance) group_left(nodename) node_uname_info{{nodename=~"$node"}}', "{{nodename}}")],
         "Memory in use per node.", unit="percent", mx=100, decimals=1, legend="table")
    b.bargauge("Root Disk by Node", '100 * (1 - node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs|ramfs"} / node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs|ramfs"}) * on(instance) group_left(nodename) node_uname_info{nodename=~"$node"}',
               "Root filesystem usage per node.", w=8, legend="{{nodename}}", th=steps((None, GREEN), (75, AMBER), (85, RED)))
    b.state("Node Pressure Conditions", f'kube_node_status_condition{{condition=~"MemoryPressure|DiskPressure|PIDPressure|NetworkUnavailable",status="true",{NODE}}}',
            "Red when a node reports a pressure condition. Pressure evicts pods.", w=16, h=8, legend="{{node}} {{condition}}",
            th=steps((None, GREEN), (1, RED)),
            mappings=[{"type": "value", "options": {"0": {"text": "OK", "color": GREEN}, "1": {"text": "Pressure", "color": RED}}}])
    b.table("Top Pods by CPU", [(f'topk(10, sum by (namespace, pod) (node_namespace_pod_container:container_cpu_usage_seconds_total:sum_rate5m{{{NS}}}))', "CPU cores")],
            "The ten pods using the most CPU right now (5m rate).", w=12, h=8,
            units={"CPU cores": ("none", steps((None, GREEN), (1, AMBER), (2, RED)), "color-text")}, sort=("CPU cores", True))
    b.table("Top Pods by Memory", [(f'topk(10, sum by (namespace, pod) (node_namespace_pod_container:container_memory_working_set_bytes{{{NS}}}))', "Working set")],
            "The ten pods with the largest working set.", w=12, h=8, units={"Working set": ("bytes", None, None)}, sort=("Working set", True))

    # =========================================================== reliability ====
    b.row("Workload Reliability", collapsed=True, desc="Pods that are not doing their job, and why.")
    b.table("Pending / Unschedulable Pods", [
        (f'max by (namespace, pod) (kube_pod_status_phase{{phase="Pending",{NS}}} == 1)', "Pending"),
        (f'max by (namespace, pod) (kube_pod_status_unschedulable{{{NS}}})', "Unschedulable"),
    ], "Pods stuck in Pending. Unschedulable=1 means no node fits the request (capacity, taints, volume attach limit).",
        w=12, h=8)
    b.table("OOMKilled Containers", [(f'max by (namespace, pod, container) (kube_pod_container_status_last_terminated_reason{{reason="OOMKilled",{NS}}} == 1)', "OOMKilled")],
            "Containers whose last exit was an OOM kill.", w=12, h=8)
    b.ts("Restarts by Namespace", [(f'sum by (namespace) (increase(kube_pod_container_status_restarts_total{{{NS}}}[15m])) > 0', "{{namespace}}")],
         "Container restarts per namespace per 15 minutes.", draw="bars", legend="table", decimals=0)
    b.ts("Containers Waiting, by Reason", [(f'sum by (reason) (kube_pod_container_status_waiting_reason{{{NS}}} == 1)', "{{reason}}")],
         "Containers not running and why (CrashLoopBackOff, ImagePullBackOff, CreateContainerConfigError, ...).", decimals=0)
    b.ts("Kernel OOM Kills by Node", [('sum by (nodename) (increase(node_vmstat_oom_kill[15m]) * on(instance) group_left(nodename) node_uname_info{nodename=~"$node"}) > 0', "{{nodename}}")],
         "OOM kills counted by the kernel per node (includes processes outside Kubernetes limits).", w=8, draw="bars", decimals=0)
    b.ts("CPU Throttling by Namespace", [(f'100 * sum by (namespace) (rate(container_cpu_cfs_throttled_periods_total{{container!="",{NS}}}[5m])) / clamp_min(sum by (namespace) (rate(container_cpu_cfs_periods_total{{container!="",{NS}}}[5m])), 1)', "{{namespace}}")],
         "Share of CFS periods throttled: a CPU limit is being hit.", w=8, unit="percent", decimals=1, legend="table")
    b.ts("HPA: Current vs Desired Replicas", [
        (f'sum by (namespace, horizontalpodautoscaler) (kube_horizontalpodautoscaler_status_current_replicas{{{NS}}})', "{{namespace}}/{{horizontalpodautoscaler}} current"),
        (f'sum by (namespace, horizontalpodautoscaler) (kube_horizontalpodautoscaler_status_desired_replicas{{{NS}}})', "{{namespace}}/{{horizontalpodautoscaler}} desired"),
    ], "Horizontal autoscalers. Desired above current for long = the cluster cannot place the new pods.", w=8, decimals=0, legend="table")
    b.gauge("Deployment Readiness", f'100 * sum(kube_deployment_status_replicas_available{{{NS}}}) / clamp_min(sum(kube_deployment_spec_replicas{{{NS}}}), 1)',
            "Available over desired replicas across Deployments.", w=6, th=steps((None, RED), (90, AMBER), (99, GREEN)))
    b.gauge("StatefulSet Readiness", f'100 * sum(kube_statefulset_status_replicas_ready{{{NS}}}) / clamp_min(sum(kube_statefulset_replicas{{{NS}}}), 1)',
            "Ready over desired replicas across StatefulSets.", w=6, th=steps((None, RED), (90, AMBER), (99, GREEN)))
    b.stat("Failed Jobs", f'count(kube_job_status_failed{{{NS}}} > 0) or vector(0)', "Jobs with at least one failed pod.", w=6, h=6, th=WARN_AT_ONE)
    b.stat("Image Pull Errors", f'count(kube_pod_container_status_waiting_reason{{reason=~"ImagePullBackOff|ErrImagePull",{NS}}} == 1) or vector(0)',
           "Containers that cannot pull their image.", w=6, h=6, th=WARN_AT_ONE)

    # =========================================================== gitops ====
    b.row("GitOps & Delivery", collapsed=True, desc="Argo CD, Argo Rollouts, Kargo verification and Tekton pipelines.")
    b.stat("Applications", 'count(argocd_app_info)', "Argo CD Applications.", w=4)
    b.stat("Healthy", 'count(argocd_app_info{health_status="Healthy"}) or vector(0)', "Applications reporting Healthy.", w=4, th=steps((None, GREEN)))
    b.stat("Progressing", 'count(argocd_app_info{health_status="Progressing"}) or vector(0)', "Applications still rolling out.", w=4, th=steps((None, BLUE), (1, AMBER)))
    b.stat("Degraded / Missing", 'count(argocd_app_info{health_status=~"Degraded|Missing"}) or vector(0)', "Applications Degraded or Missing.", w=4, th=WARN_AT_ONE)
    b.stat("Out of Sync", 'count(argocd_app_info{sync_status!="Synced"}) or vector(0)', "Applications OutOfSync or Unknown.", w=4, th=steps((None, GREEN), (1, AMBER), (10, RED)))
    b.stat("Failed Syncs (range)", 'round(sum(increase(argocd_app_sync_total{phase=~"Failed|Error"}[$__range]))) or vector(0)',
           "Sync operations that failed in the selected time range.", w=4, th=WARN_AT_ONE)
    b.table("Applications Needing Attention", [
        ('max by (name, project, dest_namespace, health_status, sync_status) (argocd_app_info{health_status!="Healthy"} or argocd_app_info{sync_status!="Synced"})', "v")],
        "Every Application that is not Healthy AND Synced. Health and sync are columns; open the app in Argo CD from its name.",
        w=14, h=9, hide=["v"], rename={"name": "Application", "project": "Project", "dest_namespace": "Namespace",
                                       "health_status": "Health", "sync_status": "Sync"})
    b.pie("Applications by Health", 'count by (health_status) (argocd_app_info)', "Health distribution of all Applications.", "{{health_status}}",
          w=10, h=9, overrides=[color_override("Healthy", GREEN), color_override("Degraded", RED),
                                color_override("Progressing", BLUE), color_override("Missing", AMBER)])
    b.ts("Sync Operations by Outcome", [('sum by (phase) (increase(argocd_app_sync_total[$__rate_interval]))', "{{phase}}")],
         "Sync operations completed per interval, by phase.", draw="bars", stack=True, decimals=0,
         overrides=[color_override("Succeeded", GREEN), color_override("Failed", RED), color_override("Error", RED)])
    b.ts("Reconcile Duration (p50 / p95)", [
        ('histogram_quantile(0.5, sum by (le) (rate(argocd_app_reconcile_bucket[$__rate_interval])))', "p50"),
        ('histogram_quantile(0.95, sum by (le) (rate(argocd_app_reconcile_bucket[$__rate_interval])))', "p95")],
        "How long the application controller takes per reconcile. A rising p95 is the first sign of an overloaded control plane.",
        unit="s", decimals=2)
    b.table("Rollouts (custom applications)", [
        ('max by (namespace, name, phase, strategy) (rollout_info)', "v"),
        ('max by (namespace, name) (rollout_info_replicas_available)', "Available"),
        ('max by (namespace, name) (rollout_info_replicas_desired)', "Desired")],
        "Argo Rollouts of the custom applications in their environment namespaces: phase, strategy and replicas.",
        w=12, h=8, hide=["v"], rename={"namespace": "Env", "name": "Rollout", "phase": "Phase", "strategy": "Strategy"})
    b.ts("Kargo Verification (AnalysisRuns) by Phase", [('count by (phase) (analysis_run_info)', "{{phase}}")],
         "Kargo verifies each promotion with an Argo Rollouts AnalysisRun; Failed here means a promotion's verification failed. "
         "(Kargo itself publishes no Prometheus metrics for Stage health — open the Kargo UI for stage-level status.)",
         w=12, h=8, decimals=0, overrides=[color_override("Successful", GREEN), color_override("Failed", RED),
                                            color_override("Error", RED), color_override("Running", BLUE)])
    b.stat("Pipeline Success (range)", 'sum(increase(tekton_pipelines_controller_pipelinerun_total{status="success"}[$__range])) / clamp_min(sum(increase(tekton_pipelines_controller_pipelinerun_total[$__range])), 1)',
           "PipelineRuns that succeeded over all that finished in the selected range.", unit="percentunit", w=6, h=5, decimals=1,
           th=steps((None, RED), (0.8, AMBER), (0.95, GREEN)))
    b.stat("PipelineRuns Running", 'sum(tekton_pipelines_controller_running_pipelineruns) or vector(0)', "PipelineRuns in progress now.", w=6, h=5)
    b.stat("TaskRuns Throttled", 'sum(tekton_pipelines_controller_running_taskruns_throttled_by_node + tekton_pipelines_controller_running_taskruns_throttled_by_quota) or vector(0)',
           "TaskRuns waiting for node capacity or quota.", w=6, h=5, th=WARN_AT_ONE)
    b.stat("Pipeline p95 Duration", 'histogram_quantile(0.95, sum by (le) (rate(tekton_pipelines_controller_pipelinerun_duration_seconds_bucket[$__range])))',
           "95th percentile PipelineRun wall time over the range.", unit="s", w=6, h=5, decimals=0)
    b.table("Pipelines", [
        ('sum by (pipeline) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{status="success"}[$__range]))', "Succeeded"),
        ('sum by (pipeline) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{status="failed"}[$__range]))', "Failed"),
        ('sum by (pipeline) (rate(tekton_pipelines_controller_pipelinerun_duration_seconds_sum[$__range])) / sum by (pipeline) (rate(tekton_pipelines_controller_pipelinerun_duration_seconds_count[$__range]))', "Mean duration"),
    ], "Per pipeline over the selected range: runs succeeded / failed and mean duration.", w=24, h=7, rename={"pipeline": "Pipeline"},
        units={"Succeeded": ("none", None, None), "Failed": ("none", steps((None, GREEN), (1, RED)), "color-text"),
               "Mean duration": ("s", None, None)})

    # =========================================================== apps SLO ====
    b.row("Custom Applications (SLO)", collapsed=True,
          desc="Golden signals of every custom application, from the Micrometer metrics adhar-kit apps publish. Per-app detail lives in the Applications folder.")
    sel = f'{APPS_NS},{HTTP_FILTER}'
    slo = b.table("Service Level by Application", [
        (f'sum by (namespace, application) (rate(http_server_requests_seconds_count{{{sel}}}[$__range]))', "Req/s"),
        (ratio5xx("namespace, application", sel, "$__range"), "5xx ratio"),
        (f'1 - ({ratio5xx("namespace, application", sel, "$__range")}) / (1 - $slo)', "Error budget left"),
        (f'histogram_quantile(0.95, sum by (namespace, application, le) (rate(http_server_requests_seconds_bucket{{{sel}}}[$__range])))', "p95"),
    ], "Per application and environment over the selected range, excluding /actuator probes. Error budget left = 1 - (5xx ratio / (1 - SLO)); "
       "the SLO is the dashboard's $slo variable. Below 0 means the budget for the range is spent.",
        w=24, h=8, rename={"namespace": "Env", "application": "Application"},
        units={"Req/s": ("reqps", None, None),
               "5xx ratio": ("percentunit", steps((None, GREEN), (0.001, AMBER), (0.01, RED)), "color-text"),
               "Error budget left": ("percentunit", steps((None, RED), (0.25, AMBER), (0.5, GREEN)), "color-background"),
               "p95": ("s", steps((None, GREEN), (0.5, AMBER), (1, RED)), "color-text")})
    slo["links"] = [APP_LOGS]
    b.ts("Request Rate by Application", [(f'sum by (application, namespace) (rate(http_server_requests_seconds_count{{{sel}}}[$__rate_interval]))', "{{application}} ({{namespace}})")],
         "HTTP requests per second.", w=8, unit="reqps", decimals=2, legend="table")
    b.ts("5xx Ratio by Application", [(ratio5xx("application, namespace", sel, "$__rate_interval"), "{{application}} ({{namespace}})")],
         "Share of requests answered 5xx.", w=8, unit="percentunit", decimals=2, legend="table")
    b.ts("p95 Latency by Application", [(f'histogram_quantile(0.95, sum by (application, namespace, le) (rate(http_server_requests_seconds_bucket{{{sel}}}[$__rate_interval])))', "{{application}} ({{namespace}})")],
         "95th percentile server latency.", w=8, unit="s", decimals=3, legend="table")

    # =========================================================== control plane ====
    b.row("Kubernetes Control Plane", collapsed=True,
          desc="API server and etcd. On an overloaded master these degrade first and everything else follows (leader-election loss).")
    b.stat("API Availability (30d)", 'apiserver_request:availability30d{verb="all"}', "Recorded 30-day API server availability (kube-prometheus SLO rules).",
           unit="percentunit", w=4, decimals=3, th=steps((None, RED), (0.99, AMBER), (0.999, GREEN)))
    b.stat("API Error Burn (1h)", 'apiserver_request:burnrate1h', "1h error-budget burn rate of the API server. >14 pages.",
           w=4, decimals=2, th=steps((None, GREEN), (6, AMBER), (14, RED)))
    b.stat("API Requests / s", 'sum(rate(apiserver_request_total[5m]))', "Requests served by the API server.", unit="reqps", w=4, decimals=0)
    b.stat("API 5xx / s", 'sum(rate(apiserver_request_total{code=~"5.."}[5m])) or vector(0)', "Server errors from the API server.", unit="reqps", w=4, decimals=2,
           th=steps((None, GREEN), (0.1, AMBER), (1, RED)))
    b.stat("etcd DB Size", 'max(apiserver_storage_size_bytes)', "etcd database size as reported by the API server (default quota 2 GiB).",
           unit="bytes", w=4, decimals=1, th=steps((None, GREEN), (1.2e9, AMBER), (1.8e9, RED)))
    b.stat("Inflight Requests", 'sum(apiserver_current_inflight_requests)', "Requests being processed now (mutating + read-only).", w=4,
           th=steps((None, GREEN), (200, AMBER), (400, RED)))
    b.ts("API Request Latency p99 by Verb", [
        ('histogram_quantile(0.99, sum by (verb, le) (rate(apiserver_request_sli_duration_seconds_bucket{verb!~"WATCH|CONNECT"}[$__rate_interval])))', "{{verb}}")],
        "99th percentile API latency by verb (SLI histogram: excludes webhook and priority-queue time). Over 1 s = control plane struggling.",
        unit="s", decimals=3, th=steps((None, GREEN), (1, RED)))
    b.ts("API Requests by Code Class", [
        ('sum by (code_class) (label_replace(rate(apiserver_request_total[$__rate_interval]), "code_class", "${1}xx", "code", "(.).."))', "{{code_class}}")],
        "API traffic split by response class.", unit="reqps", decimals=1,
        overrides=[color_override("2xx", GREEN), color_override("4xx", AMBER), color_override("5xx", RED)])
    b.ts("etcd Request Latency p99 (from API server)", [
        ('histogram_quantile(0.99, sum by (operation, le) (rate(etcd_request_duration_seconds_bucket[$__rate_interval])))', "{{operation}}")],
        "API server → etcd latency by operation. etcd itself is not scraped on self-managed control planes; this is the client view.",
        unit="s", decimals=3, legend="table")
    b.ts("Controller Work Queues", [('topk(8, sum by (name) (workqueue_depth{job="apiserver"}))', "{{name}}")],
         "Deepest API server work queues. A queue that only grows is a stuck controller.", decimals=0, legend="table")

    # =========================================================== identity & edge ====
    b.row("Identity, Edge & Certificates", collapsed=True)
    b.stat("Keycloak", 'min(kube_statefulset_status_replicas_ready{namespace="adhar-system",statefulset="keycloak"} >= bool kube_statefulset_replicas{namespace="adhar-system",statefulset="keycloak"}) or min(kube_deployment_status_replicas_available{namespace="adhar-system",deployment="keycloak"} >= bool kube_deployment_spec_replicas{namespace="adhar-system",deployment="keycloak"})',
           "Keycloak (single sign-on) has all replicas available.", w=4, text_mode="value",
           mappings=[{"type": "value", "options": {"0": {"text": "DOWN", "color": RED}, "1": {"text": "UP", "color": GREEN}}}],
           th=steps((None, RED), (1, GREEN)), color_mode="background")
    b.stat("Logins Validated / min", 'sum(rate(keycloak_credentials_password_hashing_validations_total[5m])) * 60 or vector(0)',
           "Password validations per minute (Keycloak).", w=4, decimals=1)
    b.stat("Edge Requests / s", 'sum(rate(hubble_http_requests_total[5m])) or vector(0)', "HTTP requests seen by Hubble L7 visibility.", unit="reqps", w=4, decimals=1)
    b.stat("Edge 5xx Ratio", 'sum(rate(hubble_http_responses_total{status=~"5.."}[5m])) / clamp_min(sum(rate(hubble_http_responses_total[5m])), 1e-9) or vector(0)',
           "Share of HTTP responses that were 5xx (Hubble).", unit="percentunit", w=4, decimals=2, th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)))
    b.stat("Certificates Not Ready", 'count(certmanager_certificate_ready_status{condition="False"} == 1) or vector(0)',
           "cert-manager Certificates whose Ready condition is False.", w=4, th=WARN_AT_ONE)
    b.stat("Soonest Cert Expiry", 'min(certmanager_certificate_expiration_timestamp_seconds - time())',
           "Time until the first cert-manager certificate expires (renewal normally happens 30 days before).",
           unit="s", w=4, decimals=0, th=steps((None, RED), (7 * 86400, AMBER), (21 * 86400, GREEN)))
    b.table("Certificates", [
        ('max by (namespace, name) (certmanager_certificate_expiration_timestamp_seconds - time())', "Expires in"),
        ('max by (namespace, name) (certmanager_certificate_ready_status{condition="True"})', "Ready")],
        "Every cert-manager Certificate, soonest expiry first.", w=8, h=9, rename={"namespace": "Namespace", "name": "Certificate"},
        units={"Expires in": ("s", steps((None, RED), (7 * 86400, AMBER), (21 * 86400, GREEN)), "color-text"),
               "Ready": ("none", steps((None, RED), (1, GREEN)), "color-background")},
        sort=("Expires in", False))
    b.ts("Edge Responses by Status", [('sum by (status) (rate(hubble_http_responses_total[$__rate_interval]))', "{{status}}")],
         "HTTP responses by status code (Hubble).", w=8, h=9, unit="reqps", decimals=2, legend="table")
    b.ts("Edge Latency", [
        ('histogram_quantile(0.5, sum by (le) (rate(hubble_http_request_duration_seconds_bucket[$__rate_interval])))', "p50"),
        ('histogram_quantile(0.95, sum by (le) (rate(hubble_http_request_duration_seconds_bucket[$__rate_interval])))', "p95"),
        ('histogram_quantile(0.99, sum by (le) (rate(hubble_http_request_duration_seconds_bucket[$__rate_interval])))', "p99")],
        "Request latency observed by Hubble.", w=8, h=9, unit="s", decimals=3)
    b.ts("Cluster DNS by Response Code", [('sum by (rcode) (rate(coredns_dns_responses_total[$__rate_interval]))', "{{rcode}}")],
         "CoreDNS answers by rcode. SERVFAIL rising = upstream/DNS trouble.", w=12, h=7, unit="reqps", decimals=1,
         overrides=[color_override("NOERROR", GREEN), color_override("SERVFAIL", RED), color_override("NXDOMAIN", AMBER)])
    b.ts("External Secrets Sync Errors", [('sum by (name) (increase(externalsecret_sync_calls_error[$__rate_interval])) > 0', "{{name}}")],
         "ExternalSecrets failing to sync. A Secret that never appears blocks every app that reads it.", w=12, h=7, draw="bars", decimals=0, legend="table")

    # =========================================================== security ====
    b.row("Security & Policy", collapsed=True)
    b.stat("Policy Failures (range)", 'round(sum(increase(kyverno_policy_results_total{rule_result="fail"}[$__range]))) or vector(0)',
           "Kyverno rule evaluations that failed in the range (Audit policies report, Enforce policies block).", w=6, th=steps((None, GREEN), (1, AMBER), (100, RED)))
    b.stat("Policy Passes (range)", 'round(sum(increase(kyverno_policy_results_total{rule_result="pass"}[$__range]))) or vector(0)',
           "Kyverno rule evaluations that passed in the range.", w=6)
    b.stat("Admission Requests / s", 'sum(rate(kyverno_admission_requests_total[5m])) or vector(0)', "Requests through Kyverno's admission webhooks.",
           unit="reqps", w=6, decimals=1)
    b.stat("Runtime Alerts (range)", 'round(sum(increase(falcosecurity_falcosidekick_falco_events_total[$__range]))) or vector(0)',
           "Falco runtime-security events in the range.", w=6, th=steps((None, GREEN), (1, AMBER), (50, RED)))
    b.table("Policies by Failures", [('topk(15, sum by (policy_name, rule_name) (increase(kyverno_policy_results_total{rule_result="fail"}[$__range])) > 0)', "Failures")],
            "The policies and rules failing most often over the range.", w=12, h=9, rename={"policy_name": "Policy", "rule_name": "Rule"},
            units={"Failures": ("none", steps((None, GREEN), (1, AMBER), (100, RED)), "color-text")}, sort=("Failures", True))
    b.ts("Policy Results by Outcome", [('sum by (rule_result) (increase(kyverno_policy_results_total[$__rate_interval]))', "{{rule_result}}")],
         "Kyverno rule results per interval.", w=12, h=9, draw="bars", stack=True, decimals=0,
         overrides=[color_override("pass", GREEN), color_override("fail", RED), color_override("warn", AMBER), color_override("error", RED)])
    b.ts("Runtime Security Events by Priority", [('sum by (priority) (rate(falcosecurity_falcosidekick_falco_events_total[$__rate_interval]))', "{{priority}}")],
         "Falco events per second by priority.", w=12, h=7, unit="ops", decimals=2)
    b.ts("Kernel Events by Type", [('sum by (type) (rate(tetragon_events_total[$__rate_interval]))', "{{type}}")],
         "Tetragon eBPF events per second.", w=12, h=7, unit="ops", decimals=1, legend="table")

    # =========================================================== data ====
    b.row("Data Services", collapsed=True)
    b.stat("PostgreSQL Instances Up", 'sum(cnpg_collector_up) or vector(0)', "CloudNativePG instances whose metrics collector reports up.", w=4)
    b.stat("PostgreSQL Connections", 'sum(cnpg_backends_total) or vector(0)', "Backends across every CNPG cluster.", w=4)
    b.stat("Max Replication Lag", 'max(cnpg_pg_replication_lag) or vector(0)', "Worst replica lag across CNPG clusters.", unit="s", w=4, decimals=1,
           th=steps((None, GREEN), (10, AMBER), (60, RED)))
    b.stat("Cache Instances Up", 'sum(redis_up) or vector(0)', "Valkey/Redis instances exporting metrics.", w=4)
    b.stat("Last Backup Status", 'min(velero_backup_last_status) or vector(0)', "Velero: 1 when the last backup of every schedule succeeded.",
           w=4, mappings=[{"type": "value", "options": {"0": {"text": "FAILED", "color": RED}, "1": {"text": "OK", "color": GREEN}}}],
           th=steps((None, RED), (1, GREEN)), color_mode="background")
    b.stat("Worst PVC Usage", 'max(100 * (1 - kubelet_volume_stats_available_bytes / kubelet_volume_stats_capacity_bytes)) or vector(0)',
           "Fullest PersistentVolumeClaim.", unit="percent", w=4, decimals=1, th=steps((None, GREEN), (75, AMBER), (90, RED)))
    b.ts("PostgreSQL Database Size", [('sum by (namespace, cluster) (label_replace(cnpg_pg_database_size_bytes{datname!~"template.*|postgres"}, "cluster", "$1", "pod", "(.*)-[0-9]+"))', "{{namespace}}/{{cluster}}")],
         "Size of each CNPG cluster's databases.", unit="bytes", decimals=1, legend="table")
    b.ts("PostgreSQL Transactions", [
        ('sum(rate(cnpg_pg_stat_database_xact_commit[$__rate_interval]))', "commits"),
        ('sum(rate(cnpg_pg_stat_database_xact_rollback[$__rate_interval]))', "rollbacks")],
        "Transactions per second across all clusters.", unit="ops", decimals=1, overrides=[color_override("rollbacks", RED)])
    b.bargauge("Volume Usage (top 15)", f'topk(15, 100 * (1 - kubelet_volume_stats_available_bytes{{{NS}}} / kubelet_volume_stats_capacity_bytes{{{NS}}}))',
               "Fullest PersistentVolumeClaims.", w=12, h=9, legend="{{namespace}}/{{persistentvolumeclaim}}", th=steps((None, GREEN), (75, AMBER), (90, RED)))
    b.ts("Cache Activity", [
        ('sum(rate(redis_commands_processed_total[$__rate_interval]))', "commands/s"),
        ('sum(redis_connected_clients)', "clients")],
        "Valkey/Redis commands per second and connected clients.", w=12, h=9, decimals=0)

    # =========================================================== observability ====
    b.row("Logs, Traces & Telemetry Pipeline", collapsed=True,
          desc="Error rates from Loki (logs) and Tempo span metrics (traces, in Mimir), and the health of the pipelines that carry them.")
    errlog = b.ts("Error Log Lines by Namespace (Loki)", [
        (f'topk(10, sum by (namespace) (count_over_time({{{NS}}} | detected_level=~"error|fatal|critical" [$__auto])))', "{{namespace}}")],
        "Log lines Loki classified as error/fatal/critical (its detected_level, read from JSON/logfmt level fields), per namespace. "
        "Counts lines, not incidents. detected_level is ~25x cheaper than a line regex over every stream.",
        ds=LOKI, w=12, decimals=0, legend="table", draw="bars")
    errlog["links"] = [APP_LOGS]
    b.ts("Trace Error Ratio by Service (Tempo)", [
        ('sum by (service) (rate(traces_spanmetrics_calls_total{status_code="STATUS_CODE_ERROR"}[$__rate_interval])) / sum by (service) (rate(traces_spanmetrics_calls_total[$__rate_interval]))', "{{service}}")],
        "Share of spans with error status, per service, from Tempo's span-metrics generator (remote-written to Mimir).",
        ds=MIMIR, w=12, unit="percentunit", decimals=2, legend="table")
    b.ts("Traced Request Rate by Service (Tempo)", [
        ('sum by (service) (rate(traces_spanmetrics_calls_total{span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER"}[$__rate_interval]))', "{{service}}")],
        "Server/consumer spans per second per service (span metrics).", ds=MIMIR, w=12, unit="reqps", decimals=2, legend="table")
    b.ts("Traced p95 Latency by Service (Tempo)", [
        ('histogram_quantile(0.95, sum by (service, le) (rate(traces_spanmetrics_latency_bucket{span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER"}[$__rate_interval])))', "{{service}}")],
        "p95 server span duration per service (span metrics).", ds=MIMIR, w=12, unit="s", decimals=3, legend="table")
    b.stat("Active Series", 'sum(prometheus_tsdb_head_series)', "Series in Prometheus' head block. Cardinality drives memory.", w=4,
           th=steps((None, GREEN), (2e6, AMBER), (4e6, RED)))
    b.stat("Samples / s", 'sum(rate(prometheus_tsdb_head_samples_appended_total[5m]))', "Samples ingested by Prometheus.", unit="ops", w=4, decimals=0)
    b.stat("Log Lines / s", 'sum(rate(loki_distributor_lines_received_total[5m])) or vector(0)', "Lines received by Loki's distributor.", unit="ops", w=4, decimals=0)
    b.stat("Spans / s", 'sum(rate(tempo_distributor_spans_received_total[5m])) or vector(0)', "Spans received by Tempo.", unit="ops", w=4, decimals=1)
    b.stat("OTLP Spans Accepted / s", 'sum(rate(otelcol_receiver_accepted_spans_total[5m])) or vector(0)', "Spans accepted by the Alloy OTLP receivers (apps send here).",
           unit="ops", w=4, decimals=1)
    b.stat("Remote-write Failures / s", 'sum(rate(prometheus_remote_storage_samples_failed_total[5m])) or vector(0)',
           "Samples Prometheus failed to remote-write (if configured).", unit="ops", w=4, decimals=1, th=steps((None, GREEN), (0.01, RED)))
    b.ts("Scrape Targets Down by Job", [('count by (job) (up == 0)', "{{job}}")],
         "Down targets per job. A job here has a ServiceMonitor/PodMonitor that cannot be scraped.", w=12, decimals=0, legend="table", draw="bars")
    b.ts("Ingestion by Signal", [
        ('sum(rate(prometheus_tsdb_head_samples_appended_total[$__rate_interval]))', "metric samples/s"),
        ('sum(rate(loki_distributor_lines_received_total[$__rate_interval]))', "log lines/s"),
        ('sum(rate(tempo_distributor_spans_received_total[$__rate_interval]))', "spans/s")],
        "Throughput of the three telemetry pipelines.", w=12, unit="ops", decimals=0)

    # =========================================================== autoscaling ====
    b.row("Node Autoscaler", collapsed=True,
          desc="The Adhar node autoscaler (adhar-controller-manager) decides from Pending pods; its decisions are read from its log.")
    b.ts("Node Count", [('count(kube_node_info)', "nodes"), ('count(kube_node_info) - count(kube_node_role{role=~"control-plane|master"})', "workers")],
         "Registered nodes over time.", w=8, decimals=0)
    b.ts("Autoscaler Decisions (Loki)", [
        ('sum by (action) (count_over_time({namespace="adhar-system", app="adhar-controller-manager"} |= "autoscaling tick" | regexp "\\"action\\"=\\"(?P<action>[A-Za-z]+)\\"" | action != "None" [$__auto]))', "{{action}}")],
        "Non-trivial decisions per interval (ScaleUp / ScaleDown / Refused). None-ticks are omitted.", ds=LOKI, w=8, draw="bars", decimals=0,
        overrides=[color_override("ScaleUp", BLUE), color_override("ScaleDown", AMBER)])
    b.ts("Pending Pods vs Workers", [
        ('count(kube_pod_status_unschedulable == 1) or vector(0)', "unschedulable pods"),
        ('count(kube_node_info) - count(kube_node_role{role=~"control-plane|master"})', "workers")],
        "Unschedulable pods are what make the autoscaler add a worker.", w=8, decimals=0)
    b.logs("Scaling Actions", '{namespace="adhar-system", app="adhar-controller-manager"} |~ "scaling (up|down)|joined|retir|drain|refused|quota" != "autoscaling tick"',
           "Scale-up / scale-down / join / drain lines from the autoscaler. A scale-up with no node joining afterwards is the orphaned-VM case.", h=8)

    # =========================================================== cost ====
    b.row("Cost & Efficiency", collapsed=True, desc="OpenCost node pricing. Values are list prices; they read 0 where OpenCost has no price for the cloud.")
    b.stat("Cluster Cost / hour", 'sum(node_total_hourly_cost) or vector(0)', "Sum of node hourly prices.", unit="currencyUSD", w=6, decimals=2)
    b.stat("Projected / month", '(sum(node_total_hourly_cost) or vector(0)) * 730', "Hourly cost x 730.", unit="currencyUSD", w=6, decimals=0)
    b.stat("Idle CPU (requested, unused)", 'sum(cluster:namespace:pod_cpu:active:kube_pod_container_resource_requests) - sum(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_rate5m)',
           "Cores reserved by requests but not used. Right-size requests to give them back to the scheduler.", unit="none", w=6, decimals=1)
    b.stat("Idle Memory (requested, unused)", 'sum(cluster:namespace:pod_memory:active:kube_pod_container_resource_requests) - sum(node_namespace_pod_container:container_memory_working_set_bytes)',
           "Memory reserved by requests but not in the working set.", unit="bytes", w=6, decimals=1)
    b.ts("Hourly Cost by Node", [('sum by (instance) (node_total_hourly_cost)', "{{instance}}")], "Price per node per hour.", unit="currencyUSD", decimals=3, legend="table")
    b.table("Requests vs Usage by Namespace", [
        (f'sum by (namespace) (cluster:namespace:pod_cpu:active:kube_pod_container_resource_requests{{{NS}}})', "CPU req"),
        (f'sum by (namespace) (node_namespace_pod_container:container_cpu_usage_seconds_total:sum_rate5m{{{NS}}})', "CPU used"),
        (f'sum by (namespace) (cluster:namespace:pod_memory:active:kube_pod_container_resource_requests{{{NS}}})', "Mem req"),
        (f'sum by (namespace) (node_namespace_pod_container:container_memory_working_set_bytes{{{NS}}})', "Mem used")],
        "Reserved vs used per namespace.", w=12, rename={"namespace": "Namespace"},
        units={"CPU req": ("none", None, None), "CPU used": ("none", None, None), "Mem req": ("bytes", None, None), "Mem used": ("bytes", None, None)},
        sort=("CPU req", True))

    return b.panels


def dashboard():
    return {
        "annotations": {"list": [
            {"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"}, "enable": True, "hide": True,
             "iconColor": "rgba(0, 211, 255, 1)", "name": "Annotations & Alerts", "type": "dashboard"},
            {"datasource": PROM, "enable": True, "iconColor": "red", "name": "Argo CD sync failures",
             "expr": 'sum by (name) (increase(argocd_app_sync_total{phase=~"Failed|Error"}[2m])) > 0',
             "step": "2m", "titleFormat": "Sync failed", "textFormat": "{{name}}", "useValueForTime": False},
            {"datasource": LOKI, "enable": True, "iconColor": "blue", "name": "Node scaling",
             "expr": '{namespace="adhar-system", app="adhar-controller-manager"} |= "scaling" |~ "worker node group"',
             "titleFormat": "Node autoscaler", "textFormat": "{{__line__}}"},
        ]},
        "description": "The platform front page: health at a glance, capacity (requests vs usage per node), reliability, GitOps and delivery "
                       "(Argo CD, Rollouts, Kargo verification, Tekton), custom application SLOs, the control plane, identity/edge/certificates, "
                       "security, data, logs/traces and the node autoscaler. The only dashboard outside a Grafana folder; everything else is "
                       "filed by area, and each custom application has its own in the Applications folder. "
                       "Generated by build-platform-dashboard.py — edit that, not this file.",
        "editable": True,
        "fiscalYearStartMonth": 0,
        "graphTooltip": 1,
        # Three links, so the controls stay on one row beside the variables. One
        # dropdown per folder (seven of them, plus Adhar AI) wrapped the controls
        # onto a third row; "All dashboards" lists every Adhar dashboard and the
        # folder is in the breadcrumb (dash_common.links keeps the same three).
        "links": [
            # Pinned first: where custom-application owners find their logs.
            {"asDropdown": False, "icon": "doc", "includeVars": False, "keepTime": True, "tags": [], "targetBlank": False,
             "title": "Application Logs", "tooltip": "Logs of every custom application, by environment, application and component",
             "type": "link", "url": "/d/adhar-app-logs"},
            {"asDropdown": True, "icon": "external link", "includeVars": False, "keepTime": True, "tags": ["application"], "targetBlank": False,
             "title": "Applications", "tooltip": "One dashboard per custom application", "type": "dashboards", "url": ""},
            {"asDropdown": True, "icon": "external link", "includeVars": False, "keepTime": True, "tags": ["adhar"], "targetBlank": False,
             "title": "All dashboards", "tooltip": "Every Adhar dashboard", "type": "dashboards", "url": ""},
        ],
        "panels": build(),
        "refresh": "1m",
        "schemaVersion": 39,
        "tags": ["adhar", "platform", "overview"],
        "templating": {"list": [
            {"name": "namespace", "label": "Namespace", "type": "query", "datasource": PROM,
             "definition": "label_values(kube_namespace_status_phase, namespace)",
             "query": {"qryType": 1, "query": "label_values(kube_namespace_status_phase, namespace)", "refId": "var-namespace"},
             "includeAll": True, "multi": True, "allValue": ".+", "refresh": 2, "sort": 1,
             "current": {"text": ["All"], "value": ["$__all"]}, "options": [], "hide": 0,
             "description": "All = .+ (not .*): Loki refuses a selector whose only matcher also matches the empty string."},
            {"name": "node", "label": "Node", "type": "query", "datasource": PROM,
             "definition": "label_values(kube_node_info, node)",
             "query": {"qryType": 1, "query": "label_values(kube_node_info, node)", "refId": "var-node"},
             "includeAll": True, "multi": True, "allValue": ".*", "refresh": 2, "sort": 1,
             "current": {"text": ["All"], "value": ["$__all"]}, "options": [], "hide": 0},
            {"name": "app_namespace", "label": "App environment", "type": "query", "datasource": PROM,
             "definition": "label_values(http_server_requests_seconds_count{application!=\"\",namespace!=\"adhar-system\"}, namespace)",
             "query": {"qryType": 1, "query": "label_values(http_server_requests_seconds_count{application!=\"\",namespace!=\"adhar-system\"}, namespace)", "refId": "var-app_namespace"},
             "includeAll": True, "multi": True, "allValue": ".*", "refresh": 2, "sort": 1,
             "current": {"text": ["All"], "value": ["$__all"]}, "options": [], "hide": 0,
             "description": "Environment namespaces of the custom applications (Custom Applications row)."},
            {"name": "slo", "label": "Availability SLO", "type": "custom", "query": "0.99,0.995,0.999",
             "current": {"text": "0.995", "value": "0.995"}, "includeAll": False, "multi": False, "hide": 0,
             "options": [{"text": v, "value": v, "selected": v == "0.995"} for v in ("0.99", "0.995", "0.999")],
             "description": "Objective the error-budget column is computed against."},
        ]},
        "time": {"from": "now-6h", "to": "now"},
        "timepicker": {},
        "timezone": "browser",
        "title": "Adhar Platform",
        "uid": "adhar-platform-overview",
        "version": 2,
        "weekStart": "",
    }


HEADER = """# GENERATED by build-platform-dashboard.py — edit the script, then re-run it.
# The platform front page; the one dashboard deliberately left outside a Grafana
# folder (dashboards_test.go: TestExactlyOneDashboardSitsOutsideAFolder).
apiVersion: v1
kind: ConfigMap
metadata:
  name: adhar-dashboard-adhar-platform
  labels:
    grafana_dashboard: '1'
    app.kubernetes.io/part-of: adhar-observability
data:
  adhar-platform.json: |-
"""


def main():
    import sys
    body = json.dumps(dashboard(), indent=2)
    text = HEADER + "".join("    " + line + "\n" for line in body.splitlines())
    if "--check" in sys.argv:
        if open(OUT).read() != text:
            sys.exit(f"{OUT} is stale: run build-platform-dashboard.py")
        return
    with open(OUT, "w") as f:
        f.write(text)
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
