#!/usr/bin/env python3
"""Build the hand-authored Adhar dashboards (no reliable published grafana.com
source) into dashboards-custom/<name>.json. These reference the platform's fixed
datasource UIDs directly (prometheus/loki), so generate-dashboards.py packages
them into sidecar ConfigMaps unchanged.

Deterministic + consistent: one compact spec per dashboard, laid out on the 24-col
grid automatically, with brand-consistent stat/timeseries/logs panels.
"""
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "dashboards-custom")
os.makedirs(OUT, exist_ok=True)

PROM = {"type": "prometheus", "uid": "prometheus"}
LOKI = {"type": "loki", "uid": "loki"}


class Layout:
    """Auto-places panels down the 24-column grid."""
    def __init__(self):
        self.y = 0
        self.x = 0
        self.panels = []
        self._id = 0

    def nid(self):
        self._id += 1
        return self._id

    def row(self, title):
        if self.x != 0:
            self.y += 8
            self.x = 0
        self.panels.append({
            "id": self.nid(), "type": "row", "title": title, "collapsed": False,
            "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": [],
        })
        self.y += 1

    def _place(self, w, h):
        if self.x + w > 24:
            self.x = 0
            self.y += h
        gp = {"h": h, "w": w, "x": self.x, "y": self.y}
        self.x += w
        if self.x >= 24:
            self.x = 0
            self.y += h
        return gp

    def stat(self, title, expr, unit="short", w=6, h=4, thresholds=None, desc="", legend=""):
        gp = self._place(w, h)
        steps = thresholds or [{"color": "green", "value": None}]
        self.panels.append({
            "id": self.nid(), "type": "stat", "title": title, "description": desc,
            "datasource": PROM, "gridPos": gp,
            "targets": [{"datasource": PROM, "expr": expr, "legendFormat": legend, "refId": "A"}],
            "fieldConfig": {"defaults": {"unit": unit, "color": {"mode": "thresholds"},
                                          "thresholds": {"mode": "absolute", "steps": steps}},
                            "overrides": []},
            "options": {"colorMode": "value", "graphMode": "area", "justifyMode": "auto",
                        "textMode": "auto", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}},
        })

    def ts(self, title, targets, unit="short", w=12, h=8, desc="", stacking=False, fill=10):
        gp = self._place(w, h)
        tgs = [{"datasource": PROM, "expr": e, "legendFormat": l, "refId": chr(65 + i)}
               for i, (e, l) in enumerate(targets)]
        self.panels.append({
            "id": self.nid(), "type": "timeseries", "title": title, "description": desc,
            "datasource": PROM, "gridPos": gp, "targets": tgs,
            "fieldConfig": {"defaults": {"unit": unit, "custom": {
                "drawStyle": "line", "lineInterpolation": "smooth", "lineWidth": 2,
                "fillOpacity": fill, "showPoints": "never", "spanNulls": True,
                "stacking": {"mode": "normal" if stacking else "none", "group": "A"},
                "gradientMode": "opacity"}}, "overrides": []},
            "options": {"legend": {"showLegend": True, "placement": "bottom", "displayMode": "table",
                                   "calcs": ["lastNotNull", "max"]},
                        "tooltip": {"mode": "multi", "sort": "desc"}},
        })

    def logs(self, title, expr, w=24, h=8, desc=""):
        gp = self._place(w, h)
        self.panels.append({
            "id": self.nid(), "type": "logs", "title": title, "description": desc,
            "datasource": LOKI, "gridPos": gp,
            "targets": [{"datasource": LOKI, "expr": expr, "refId": "A"}],
            "options": {"showTime": True, "wrapLogMessage": True, "enableLogDetails": True,
                        "dedupStrategy": "none", "sortOrder": "Descending"},
        })


def dashboard(uid, title, tags, layout, ns_metric):
    return {
        "uid": uid, "title": title, "tags": ["adhar"] + tags, "editable": True,
        "schemaVersion": 39, "version": 1, "timezone": "", "refresh": "30s",
        "time": {"from": "now-6h", "to": "now"}, "graphTooltip": 1,
        "annotations": {"list": []}, "links": [],
        "templating": {"list": [
            {"name": "namespace", "type": "query", "datasource": PROM, "refresh": 2,
             "query": {"query": f"label_values({ns_metric}, namespace)", "refId": "ns"},
             "sort": 1, "includeAll": True, "allValue": ".*", "current": {"text": "All", "value": "$__all"},
             "label": "Namespace"},
            {"name": "pod", "type": "query", "datasource": PROM, "refresh": 2,
             "query": {"query": f"label_values({ns_metric}{{namespace=~\"$namespace\"}}, pod)", "refId": "pod"},
             "sort": 1, "includeAll": True, "allValue": ".*", "current": {"text": "All", "value": "$__all"},
             "label": "Pod"},
        ]},
        "panels": layout.panels,
    }


def write(name, dash):
    with open(os.path.join(OUT, name + ".json"), "w", encoding="utf-8") as f:
        json.dump(dash, f, indent=2)
    print(f"  wrote dashboards-custom/{name}.json ({len(dash['panels'])} panels)")


# ---------------------------------------------------------------- Alloy
l = Layout()
l.row("Overview — Grafana Alloy (telemetry collector)")
l.stat("Alloy version", "max(alloy_build_info)", "none", desc="Running Alloy build", legend="{{version}}")
l.stat("Running components", "sum(alloy_component_controller_running_components)", "short",
       thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}])
l.stat("Alloy pods up", 'sum(up{job=~".*alloy.*"})', "short",
       thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}])
l.stat("Component eval p99", "histogram_quantile(0.99, sum(rate(alloy_component_evaluation_seconds_bucket[5m])) by (le))", "s")
l.row("Pipelines")
l.ts("Prometheus remote_write — samples/s",
     [("sum(rate(prometheus_remote_storage_samples_total[5m]))", "sent"),
      ("sum(rate(prometheus_remote_storage_samples_failed_total[5m]))", "failed"),
      ("sum(rate(prometheus_remote_write_wal_samples_appended_total[5m]))", "wal appended")], "ops")
l.ts("Loki logs — bytes/s & drops",
     [("sum(rate(loki_write_sent_bytes_total[5m]))", "sent bytes/s"),
      ("sum(rate(loki_write_dropped_entries_total[5m]))", "dropped entries/s")], "Bps")
l.ts("OTLP traces — spans/s",
     [("sum(rate(otelcol_receiver_accepted_spans_total[5m]))", "accepted"),
      ("sum(rate(otelcol_exporter_sent_spans_total[5m]))", "sent"),
      ("sum(rate(otelcol_exporter_send_failed_spans_total[5m]))", "send failed")], "ops")
l.ts("Component evaluation latency (p50/p99)",
     [("histogram_quantile(0.5, sum(rate(alloy_component_evaluation_seconds_bucket[5m])) by (le))", "p50"),
      ("histogram_quantile(0.99, sum(rate(alloy_component_evaluation_seconds_bucket[5m])) by (le))", "p99")], "s")
l.row("Resources")
l.ts("CPU (cores)", [('sum(rate(alloy_resources_process_cpu_seconds_total[5m])) by (pod)', "{{pod}}")], "short")
l.ts("Memory (RSS)", [('sum(alloy_resources_process_resident_memory_bytes) by (pod)', "{{pod}}")], "bytes")
l.logs("Alloy logs", '{namespace=~"$namespace", pod=~"alloy.*"}')
write("alloy", dashboard("adhar-alloy", "Adhar / Alloy — Telemetry Collector", ["alloy", "collector"], l,
                         "alloy_component_controller_running_components"))

# ---------------------------------------------------------------- external-dns
l = Layout()
l.row("Overview — ExternalDNS")
l.stat("Registry endpoints", "sum(external_dns_registry_endpoints_total)", "short")
l.stat("Source endpoints", "sum(external_dns_source_endpoints_total)", "short")
l.stat("Verified records", "sum(external_dns_controller_verified_records)", "short")
l.stat("Last sync age", "time() - max(external_dns_controller_last_sync_timestamp_seconds)", "s",
       thresholds=[{"color": "green", "value": None}, {"color": "yellow", "value": 300}, {"color": "red", "value": 900}],
       desc="Seconds since ExternalDNS last synced records to the DNS provider")
l.row("Records & reconciliation")
l.ts("Managed endpoints",
     [("sum(external_dns_source_endpoints_total)", "source"),
      ("sum(external_dns_registry_endpoints_total)", "registry")], "short")
l.ts("Sync / reconcile age",
     [("time() - max(external_dns_controller_last_sync_timestamp_seconds)", "since last sync"),
      ("time() - max(external_dns_controller_last_reconcile_timestamp_seconds)", "since last reconcile")], "s")
l.row("Errors")
l.ts("Error rate",
     [("sum(rate(external_dns_source_errors_total[5m]))", "source errors/s"),
      ("sum(rate(external_dns_registry_errors_total[5m]))", "registry errors/s")], "ops",
     desc="Any sustained error rate means DNS records are drifting from desired state")
write("external-dns", dashboard("adhar-external-dns", "Adhar / ExternalDNS", ["external-dns", "networking"], l,
                                "external_dns_registry_endpoints_total"))

# ---------------------------------------------------------------- cosign / policy-controller
l = Layout()
l.row("Overview — Sigstore policy-controller (cosign)")
l.stat("Reconcile rate", "sum(rate(controller_runtime_reconcile_total[5m]))", "ops")
l.stat("Reconcile errors", "sum(rate(controller_runtime_reconcile_errors_total[5m]))", "ops",
       thresholds=[{"color": "green", "value": None}, {"color": "yellow", "value": 0.01}, {"color": "red", "value": 0.1}])
l.stat("Workqueue depth", "sum(workqueue_depth)", "short",
       thresholds=[{"color": "green", "value": None}, {"color": "yellow", "value": 10}, {"color": "red", "value": 50}])
l.stat("Cert reads", "sum(certwatcher_read_certificate_total)", "short")
l.row("Reconcile")
l.ts("Reconcile by controller", [('sum(rate(controller_runtime_reconcile_total[5m])) by (controller)', "{{controller}}")], "ops")
l.ts("Reconcile latency (p50/p99)",
     [("histogram_quantile(0.5, sum(rate(controller_runtime_reconcile_time_seconds_bucket[5m])) by (le))", "p50"),
      ("histogram_quantile(0.99, sum(rate(controller_runtime_reconcile_time_seconds_bucket[5m])) by (le))", "p99")], "s")
l.row("Workqueue & certificates")
l.ts("Workqueue", [('sum(workqueue_depth) by (name)', "depth {{name}}"),
                   ('sum(rate(workqueue_adds_total[5m])) by (name)', "adds/s {{name}}")], "short")
l.ts("Certificate watcher",
     [("sum(rate(certwatcher_read_certificate_total[5m]))", "reads/s"),
      ("sum(rate(certwatcher_read_certificate_errors_total[5m]))", "read errors/s")], "ops")
write("cosign", dashboard("adhar-cosign", "Adhar / Cosign — Sigstore Policy Controller", ["cosign", "security"], l,
                          "controller_runtime_reconcile_total"))

# ---------------------------------------------------------------- spark-operator
l = Layout()
l.row("Overview — Spark Operator")
l.stat("Running apps", "sum(spark_application_running_count)", "short",
       thresholds=[{"color": "blue", "value": None}])
l.stat("Submitted (total)", "sum(spark_application_submit_count)", "short")
l.stat("Succeeded (total)", "sum(spark_application_success_count)", "short",
       thresholds=[{"color": "green", "value": None}])
l.stat("Failed (total)", "sum(spark_application_failure_count)", "short",
       thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}])
l.row("Applications")
l.ts("Application state (rate)",
     [("sum(rate(spark_application_submit_count[5m]))", "submitted/s"),
      ("sum(rate(spark_application_success_count[5m]))", "succeeded/s"),
      ("sum(rate(spark_application_failure_count[5m]))", "failed/s")], "ops")
l.ts("Running / pending applications",
     [("sum(spark_application_running_count)", "running"),
      ("sum(spark_application_pending_count)", "pending")], "short")
l.row("Executors")
l.ts("Executor state",
     [("sum(spark_executor_running_count)", "running"),
      ("sum(spark_executor_success_count)", "succeeded"),
      ("sum(spark_executor_failure_count)", "failed")], "short")
l.ts("Operator reconcile", [('sum(rate(controller_runtime_reconcile_total[5m]))', "reconciles/s"),
                            ('sum(rate(controller_runtime_reconcile_errors_total[5m]))', "errors/s")], "ops")
write("spark-operator", dashboard("adhar-spark-operator", "Adhar / Spark Operator", ["spark", "data"], l,
                                  "controller_runtime_reconcile_total"))

# ---------------------------------------------------------------- tetragon
# Metric names are tetragon 1.7.x's (verified against a live agent 2026-10-04):
# the ringbuf_* / missed_events_total family the first version of this panel set
# used does not exist there, so every panel read "No data".
l = Layout()
l.row("Overview — Tetragon (eBPF runtime security)")
l.stat("Events/s", "sum(rate(tetragon_events_total[5m]))", "ops")
l.stat("Policy events/s", "sum(rate(tetragon_policy_events_total[5m]))", "ops",
       desc="Matches of the platform's TracingPolicies (adhar-privilege-escalation, adhar-sensitive-files, adhar-agent-egress)")
l.stat("Policies loaded", 'sum(tetragon_tracingpolicy_loaded{state="enabled"})', "short",
       thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 3}],
       desc="TracingPolicies attached and enabled; the platform baseline is 3")
l.stat("Export drops/s", "sum(rate(tetragon_export_ratelimit_events_dropped_total[5m]))", "ops",
       thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 0.001}],
       desc="Events the export rate limit discarded before Loki = audit-trail gaps; should be 0")
l.row("Process & policy events")
l.ts("Events by type", [('sum(rate(tetragon_events_total[5m])) by (type)', "{{type}}")], "ops", stacking=True)
l.ts("Policy events by policy", [('sum(rate(tetragon_policy_events_total[5m])) by (policy)', "{{policy}}")], "ops", stacking=True)
l.ts("Policy events by workload", [('sum(rate(tetragon_policy_events_total[5m])) by (namespace, workload)', "{{namespace}}/{{workload}}")], "ops")
l.ts("Exported to stdout/Loki", [("sum(rate(tetragon_events_exported_total[5m]))", "events/s"),
                                 ("sum(rate(tetragon_events_exported_bytes_total[5m]))", "bytes/s")], "short")
l.row("Agent health")
l.ts("Probe failures",
     [("sum(rate(tetragon_missed_link_probes_total[5m]))", "missed link probes/s"),
      ("sum(rate(tetragon_missed_prog_probes_total[5m]))", "missed prog probes/s"),
      ("sum(rate(tetragon_errors_total[5m]))", "errors/s")], "ops")
l.ts("Process cache",
     [("sum(tetragon_process_cache_size)", "size"),
      ("sum(rate(tetragon_process_cache_evictions_total[5m]))", "evicted/s"),
      ("sum(rate(tetragon_process_cache_misses_total[5m]))", "misses/s")], "short")
l.logs("Policy matches (exported events)", '{namespace=~"$namespace", app="tetragon", container="export-stdout"} |= "process_kprobe"',
       desc="Each line is one kernel-hook match with the process tree; filter further with |= \"adhar-agent-egress\" etc.")
write("tetragon", dashboard("adhar-tetragon", "Adhar / Tetragon — Runtime Security", ["tetragon", "security"], l,
                            "tetragon_events_total"))

# ---------------------------------------------------------------- falco
# Replaces grafana.com 11914, which reads `falco_events` from the retired
# falco-exporter. The platform's events come through Falcosidekick
# (falcosecurity_falcosidekick_falco_events_total) and the agent's own health
# counters (falcosecurity_falco_* / _scap_*) come from the DaemonSet's :8765.
l = Layout()
l.row("Overview — Falco (runtime threat detection)")
l.stat("Events/h", "sum(increase(falcosecurity_falcosidekick_falco_events_total[1h]))", "short")
l.stat("Critical+ events/24h", 'sum(increase(falcosecurity_falcosidekick_falco_events_total{priority_raw=~"critical|alert|emergency"}[24h]))', "short",
       thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}])
l.stat("Agent-workload events/24h", 'sum(increase(falcosecurity_falcosidekick_falco_events_total{rule=~"Shell spawned in agent workload|Agent workload connected outside the cluster"}[24h]))', "short",
       thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}],
       desc="The two adhar.io/agent rules in security/falco values.yaml")
l.stat("Falco nodes reporting", 'count(up{job="falco"} == 1)', "short",
       thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}])
l.row("What fired")
l.ts("Events by rule", [('sum(rate(falcosecurity_falcosidekick_falco_events_total[5m])) by (rule)', "{{rule}}")], "ops", stacking=True)
l.ts("Events by priority", [('sum(rate(falcosecurity_falcosidekick_falco_events_total[5m])) by (priority_raw)', "{{priority_raw}}")], "ops", stacking=True)
l.ts("Events by namespace", [('sum(rate(falcosecurity_falcosidekick_falco_events_total[5m])) by (k8s_ns_name)', "{{k8s_ns_name}}")], "ops")
l.ts("Fan-out (Falcosidekick outputs)", [('sum(rate(falcosecurity_falcosidekick_outputs_total[5m])) by (destination, status)', "{{destination}} {{status}}")], "ops",
     desc="Deliveries to Loki, Alertmanager and the UI; any status=error series means evidence is being lost")
l.row("Falco agent health")
l.ts("Kernel event drops", [('sum(rate(falcosecurity_scap_n_drops_total[5m])) by (hostname)', "{{hostname}}")], "ops",
     desc="Ring-buffer overflow = syscalls unobserved; should be flat at 0")
l.ts("Syscalls observed", [('sum(rate(falcosecurity_scap_n_evts_total[5m])) by (hostname)', "{{hostname}}")], "ops")
l.logs("Events (Loki, via Falcosidekick)", '{rule=~".+"} | json',
       desc="Every Falco event as JSON; labels rule / priority / source / k8s_ns_name / k8s_pod_name are indexed")
write("falco", dashboard("adhar-falco", "Adhar / Falco — Runtime Threats", ["falco", "security"], l,
                         "falcosecurity_falcosidekick_falco_events_total"))

# ---------------------------------------------------------------- kubescape
l = Layout()
l.row("Overview — Kubescape Operator (security posture)")
l.stat("Operator pods up", 'sum(up{job=~".*kubescape.*"})', "short",
       thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}])
l.stat("Reconcile rate", 'sum(rate(controller_runtime_reconcile_total{job=~".*kubescape.*"}[5m]))', "ops")
l.stat("Reconcile errors", 'sum(rate(controller_runtime_reconcile_errors_total{job=~".*kubescape.*"}[5m]))', "ops",
       thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 0.1}])
l.stat("Goroutines", 'sum(go_goroutines{job=~".*kubescape.*"})', "short")
l.row("Reconcile & work")
l.ts("Reconcile", [('sum(rate(controller_runtime_reconcile_total{job=~".*kubescape.*"}[5m])) by (controller)', "{{controller}}"),
                   ('sum(rate(controller_runtime_reconcile_errors_total{job=~".*kubescape.*"}[5m]))', "errors/s")], "ops",
     desc="Kubescape's scan controllers. Compliance scores are surfaced in the Kubescape UI / ConfigMaps; this dashboard covers operator health.")
l.ts("Workqueue depth", [('sum(workqueue_depth{job=~".*kubescape.*"}) by (name)', "{{name}}")], "short")
l.row("Resources")
l.ts("Memory (RSS)", [('sum(process_resident_memory_bytes{job=~".*kubescape.*"}) by (pod)', "{{pod}}")], "bytes")
l.ts("CPU (cores)", [('sum(rate(process_cpu_seconds_total{job=~".*kubescape.*"}[5m])) by (pod)', "{{pod}}")], "short")
l.logs("Kubescape logs", '{namespace=~"$namespace", pod=~"kubescape.*"}')
write("kubescape", dashboard("adhar-kubescape", "Adhar / Kubescape — Security Posture", ["kubescape", "security"], l,
                             "controller_runtime_reconcile_total"))

# ---------------------------------------------------------------- pyroscope
l = Layout()
l.row("Overview — Pyroscope (continuous profiling)")
l.stat("Pyroscope up", 'sum(up{job=~".*pyroscope.*"})', "short",
       thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}])
l.stat("Profiles received/s", "sum(rate(pyroscope_distributor_received_decompressed_bytes_sum[5m]))", "Bps")
l.stat("Ingester series", "sum(pyroscope_ingester_memory_series)", "short")
l.stat("Goroutines", 'sum(go_goroutines{job=~".*pyroscope.*"})', "short")
l.row("Ingestion")
l.ts("Distributor received",
     [("sum(rate(pyroscope_distributor_received_compressed_bytes_sum[5m]))", "compressed bytes/s"),
      ("sum(rate(pyroscope_distributor_received_samples_sum[5m]))", "samples/s")], "short",
     desc="Profiles arriving at the Pyroscope distributor")
l.ts("Ingester append & flush",
     [("sum(rate(pyroscope_ingester_appended_samples_sum[5m]))", "appended/s"),
      ("sum(pyroscope_ingester_memory_series)", "in-memory series")], "short")
l.row("Resources")
l.ts("Memory (RSS)", [('sum(process_resident_memory_bytes{job=~".*pyroscope.*"}) by (pod)', "{{pod}}")], "bytes")
l.ts("CPU (cores)", [('sum(rate(process_cpu_seconds_total{job=~".*pyroscope.*"}[5m])) by (pod)', "{{pod}}")], "short")
write("pyroscope", dashboard("adhar-pyroscope", "Adhar / Pyroscope — Continuous Profiling", ["pyroscope", "profiling"], l,
                             "go_goroutines"))

print("done: 8 custom dashboards built")
