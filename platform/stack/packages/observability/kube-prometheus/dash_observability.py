"""Observability folder: four dashboards in place of twenty-nine.

  Telemetry Pipeline        how signals get collected: Prometheus scraping, Alloy (metrics
                            remote_write, logs to Loki, OTLP), Faro (browser telemetry).
  Metrics, Logs & Traces    where signals are kept and alerted on: Prometheus TSDB and rules,
                            Alertmanager, Mimir, Loki, Tempo and Grafana.
  Cost                      OpenCost: what the cluster costs and who spends it (overview and
                            per-namespace in one).
  JVM Services              every Micrometer / JVM service (Keycloak, OpenMetadata, Kafka UI,
                            Tolgee, Nexus, Strimzi and the applications' Spring services).

Dropped: VictoriaMetrics (2), Pyroscope, Beyla and Fluent Bit (packages disabled in every
profile), the Node.js dashboard (nothing on the platform publishes prom-client metrics),
the duplicate Loki (5) / Tempo (4) / Mimir (3) mixin and community views, and the generic
Spring Boot / JVM community boards (JVM Services covers them).
"""
from dashlib import *  # noqa: F401,F403
from dash_common import (KSM, SYS, UIDS, component_logs, links, pods_ready_stat, pods_resources, pods_table,
                         tags, up_stat)

MIMIR = {"type": "prometheus", "uid": "mimir"}


def _mimir(panel):
    """A panel that reads Mimir (long-term / remote-written series) instead of $datasource."""
    panel["datasource"] = MIMIR
    return panel


# =============================================================================================
# Telemetry Pipeline
# =============================================================================================

def telemetry():
    AL = 'job="alloy"'
    FR = 'job="faro"'
    d = Dash(UIDS["telemetry"], "Observability / Telemetry Pipeline",
             "How metrics, logs and traces get from workloads to storage: Prometheus scrape targets, the Alloy "
             "DaemonSet (metrics remote_write to Mimir, pod logs to Loki, OTLP traces to Tempo) and the Faro "
             "receiver for browser telemetry. Verify a signal at its reader (Mimir / Loki / Tempo) — this "
             "dashboard shows where along the way it got lost. Replaces the Alloy, Faro and Prometheus-scrape "
             "parts of the Prometheus dashboards.",
             tags("Observability", "alloy", "faro", "prometheus"),
             [ds_var(), qvar("job", "Scrape job", 'label_values(up, job)', all_value=".+")],
             links=links("Observability", ("Metrics, logs & traces", "lgtm")),
             collapse=1)

    d.row("Pipeline at a glance")
    d.line(4,
           (stat("Targets down", "Prometheus scrape targets currently down (up == 0) for the selected jobs.",
                 ['count(up{job=~"$job"} == 0) or vector(0)'], th=steps((GREEN, None), (AMBER, 1), (RED, 5))), 3),
           (stat("Targets up", "Scrape targets up out of all targets for the selected jobs.",
                 [T('sum(up{job=~"$job"})', "up"), T('count(up{job=~"$job"})', "total")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (pods_ready_stat("Alloy collectors", "Alloy DaemonSet pods (one per node).", "alloy-[a-z0-9]{5}"), 3),
           (stat("Alloy config failing", "Collectors whose last configuration load failed (they keep running the "
                 "previous config).",
                 [f'count(alloy_config_last_load_successful{{{AL}}} == 0) or vector(0)'], th=OK_BAD), 3),
           (stat("Remote write failures / s", "Samples Alloy failed to send to Mimir (dropped after retries).",
                 [f'sum(rate(prometheus_remote_storage_samples_failed_total{{{AL}}}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (AMBER, 1), (RED, 100)), decimals=1), 3),
           (stat("Remote write lag", "How far behind Alloy's remote_write is (newest sample read vs newest sent), "
                 "worst collector. Minutes of lag = dashboards that read Mimir are stale.",
                 [f'max(prometheus_remote_storage_queue_highest_timestamp_seconds{{{AL}}} - prometheus_remote_storage_queue_highest_sent_timestamp_seconds{{{AL}}})'],
                 unit="s", th=steps((GREEN, None), (AMBER, 60), (RED, 300))), 3),
           (stat("Log lines dropped / s", "Log entries Alloy dropped on the way to Loki (rejected or retries "
                 "exhausted).",
                 [f'sum(rate(loki_write_dropped_entries_total{{{AL}}}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (AMBER, 0.1), (RED, 10)), decimals=1), 3),
           (stat("Faro events / s", "Browser events (logs, exceptions, measurements) received by the Faro "
                 "collector.",
                 [f'sum(rate(faro_receiver_events_total{{{FR}}}[5m])) or vector(0)'], unit="ops", color_mode="value",
                 graph="area", decimals=2), 3))

    d.row("Prometheus scraping")
    d.line(8,
           (table("Targets down", "Every scrape target that is down: job, namespace, pod and endpoint. The Prometheus "
                  "UI's /targets page has the scrape error.",
                  ['max by (job, namespace, pod, instance) (up{job=~"$job"} == 0)'],
                  [("job", "Job", None, None), ("namespace", "Namespace", None, None), ("pod", "Pod", None, None),
                   ("instance", "Instance", None, None)], sort=("Job", False), hide=("Value",)), 8),
           (bargauge("Samples per scrape (top 15 jobs)", "Samples each job exposes per scrape. A job jumping here "
                     "is a cardinality explosion (a label with unbounded values).",
                     [T('topk(15, sum by (job) (scrape_samples_scraped{job=~"$job"}))', "{{job}}")],
                     unit="short", min=0, max=None, th=steps((GREEN, None), (AMBER, 20000), (RED, 100000)), decimals=0), 8),
           (ts("Scrape duration (slowest 10 jobs)", "Longest scrape per job. Close to the scrape timeout (10s) means "
               "the target is about to start failing.",
               [T('topk(10, max by (job) (scrape_duration_seconds{job=~"$job"}))', "{{job}}")],
               unit="s", th=steps((GREEN, None), (AMBER, 5), (RED, 9))), 8))
    d.line(8,
           (ts("Targets up by job over time", "Healthy targets per job (top 15 jobs by target count).",
               [T('topk(15, sum by (job) (up{job=~"$job"}))', "{{job}}")], decimals=0), 8),
           (ts("New series per scrape", "Series created per scrape by job (top 10). Constant churn is a label that "
               "changes per scrape (pod hash, request id).",
               [T('topk(10, sum by (job) (scrape_series_added{job=~"$job"}))', "{{job}}")], decimals=0), 8),
           (ts("Discovery & target sync", "Targets discovered by service discovery and failed SD configurations.",
               [T('sum(prometheus_sd_discovered_targets)', "discovered"),
                T('sum(prometheus_sd_failed_configs)', "failed configs")],
               decimals=0, overrides=[ov_color("failed configs", RED)]), 8))

    d.row("Alloy: metrics (remote_write to Mimir)")
    d.line(8,
           (ts("Samples sent / failed / retried", "Samples per second Alloy ships to Mimir, and those that failed or "
               "were retried.",
               [T(f'sum(rate(prometheus_remote_storage_samples_total{{{AL}}}[$__rate_interval]))', "sent"),
                T(f'sum(rate(prometheus_remote_storage_samples_failed_total{{{AL}}}[$__rate_interval]))', "failed"),
                T(f'sum(rate(prometheus_remote_storage_samples_retried_total{{{AL}}}[$__rate_interval]))', "retried")],
               unit="ops", overrides=[ov_color("failed", RED), ov_color("retried", AMBER)]), 8),
           (ts("Pending samples & shards", "Samples queued for sending and the number of remote_write shards in use "
               "(Alloy adds shards when it falls behind).",
               [T(f'sum(prometheus_remote_storage_samples_pending{{{AL}}})', "pending"),
                T(f'sum(prometheus_remote_storage_shards{{{AL}}})', "shards")],
               overrides=[{"matcher": {"id": "byName", "options": "shards"},
                           "properties": [{"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Remote write lag per collector", "Seconds between the newest sample read and the newest sent, per "
               "Alloy pod.",
               [T(f'max by (pod) (prometheus_remote_storage_queue_highest_timestamp_seconds{{{AL}}} - prometheus_remote_storage_queue_highest_sent_timestamp_seconds{{{AL}}})', "{{pod}}")],
               unit="s", th=steps((GREEN, None), (AMBER, 60), (RED, 300))), 8))

    d.row("Alloy: logs (to Loki) & OTLP")
    d.line(8,
           (ts("Log entries sent / dropped", "Log lines per second Alloy sends to Loki and drops.",
               [T(f'sum(rate(loki_write_sent_entries_total{{{AL}}}[$__rate_interval]))', "sent"),
                T(f'sum(rate(loki_write_dropped_entries_total{{{AL}}}[$__rate_interval]))', "dropped")],
               unit="ops", overrides=[ov_color("dropped", RED)]), 8),
           (ts("Loki push latency p99 & bytes", "99th percentile push request latency to Loki and bytes sent per "
               "second.",
               [T(f'histogram_quantile(0.99, sum by (le) (rate(loki_write_request_duration_seconds_bucket{{{AL}}}[$__rate_interval])))', "p99 latency"),
                T(f'sum(rate(loki_write_sent_bytes_total{{{AL}}}[$__rate_interval]))', "bytes/s")],
               unit="s", overrides=[{"matcher": {"id": "byName", "options": "bytes/s"},
                                     "properties": [{"id": "unit", "value": "Bps"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("OTLP exporter queue", "Fill of the OpenTelemetry exporter queues (traces to Tempo, logs to Loki). "
               "A full queue drops data.",
               [T(f'sum by (pod) (otelcol_exporter_queue_size{{{AL}}}) / sum by (pod) (otelcol_exporter_queue_capacity{{{AL}}})', "{{pod}}"),
                T(f'sum(rate(otelcol_exporter_loki_entries_failed{{{AL}}}[$__rate_interval]))', "loki entries failed/s")],
               unit="percentunit", overrides=[{"matcher": {"id": "byName", "options": "loki entries failed/s"},
                                               "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"},
                                                              {"id": "color", "value": {"mode": "fixed", "fixedColor": RED}}]}]), 8))

    d.row("Alloy: collectors")
    d.line(8,
           (ts("Running components & evaluation", "Components each collector runs and their evaluation time p99.",
               [T(f'sum by (pod) (alloy_component_controller_running_components{{{AL}}})', "{{pod}} components"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(alloy_component_evaluation_seconds_bucket{{{AL}}}[$__rate_interval])))', "evaluation p99")],
               overrides=[{"matcher": {"id": "byName", "options": "evaluation p99"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Collector CPU & memory", "Alloy process CPU and resident memory per pod.",
               [T(f'sum by (pod) (rate(alloy_resources_process_cpu_seconds_total{{{AL}}}[$__rate_interval]))', "{{pod}} cpu"),
                T(f'sum by (pod) (alloy_resources_process_resident_memory_bytes{{{AL}}})', "{{pod}} rss")],
               unit="cores", overrides=[{"matcher": {"id": "byRegexp", "options": ".* rss"},
                                         "properties": [{"id": "unit", "value": "bytes"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Config reloads", "Configuration load failures and the time since the last successful load.",
               [T(f'sum(increase(alloy_config_load_failures_total{{{AL}}}[$__rate_interval]))', "load failures"),
                T(f'max(time() - alloy_config_last_load_success_timestamp_seconds{{{AL}}})', "since last good load")],
               overrides=[ov_color("load failures", RED),
                          {"matcher": {"id": "byName", "options": "since last good load"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("Faro (browser telemetry)")
    d.line(8,
           (ts("Browser signals", "Logs, exceptions, measurements (web vitals) and events per second received from "
               "browsers.",
               [T(f'sum(rate(faro_receiver_logs_total{{{FR}}}[$__rate_interval]))', "logs"),
                T(f'sum(rate(faro_receiver_exceptions_total{{{FR}}}[$__rate_interval]))', "exceptions"),
                T(f'sum(rate(faro_receiver_measurements_total{{{FR}}}[$__rate_interval]))', "measurements"),
                T(f'sum(rate(faro_receiver_events_total{{{FR}}}[$__rate_interval]))', "events")],
               unit="ops", overrides=[ov_color("exceptions", RED)]), 8),
           (ts("Faro collector CPU & memory", "The faro-alloy receiver's process CPU and memory.",
               [T(f'sum(rate(alloy_resources_process_cpu_seconds_total{{{FR}}}[$__rate_interval]))', "cpu"),
                T(f'sum(alloy_resources_process_resident_memory_bytes{{{FR}}})', "rss")],
               unit="cores", overrides=[{"matcher": {"id": "byName", "options": "rss"},
                                         "properties": [{"id": "unit", "value": "bytes"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Faro config & remote config", "Faro collector config load status and remote-config loads.",
               [T(f'max(alloy_config_last_load_successful{{{FR}}})', "config ok"),
                T(f'sum(increase(remotecfg_load_failures_total{{{FR}}}[$__rate_interval]))', "remote config failures")],
               decimals=0, overrides=[ov_color("remote config failures", RED)]), 8))
    d.line(9, (component_logs("Collector errors (Loki)", "Errors from the Alloy collectors and the Faro receiver.",
                              'namespace="adhar-system", pod=~"alloy-.+|faro-alloy-.+"', pattern='level=(error|warn)'), 24))
    return d


# =============================================================================================
# Metrics, Logs & Traces (LGTM backends)
# =============================================================================================

def lgtm():
    PR = 'job="prometheus-kube-prometheus-prometheus"'
    AM = 'job="prometheus-kube-prometheus-alertmanager"'
    MI = 'job=~"adhar-system/(distributor|ingester|querier|query-frontend|query-scheduler|store-gateway|compactor)"'
    LK = 'job="adhar-system/loki"'
    TP = 'job="adhar-system/tempo"'
    GF = 'job="prometheus-grafana"'
    d = Dash(UIDS["lgtm"], "Observability / Metrics, Logs & Traces",
             "Where the platform's signals are stored, queried and alerted on: Prometheus (scrape storage and rule "
             "evaluation), Alertmanager, Mimir (long-term metrics), Loki (logs), Tempo (traces) and Grafana itself. "
             "Replaces Prometheus, Alertmanager, Mimir (3), Loki (5), Tempo (4) and VictoriaMetrics (2).",
             tags("Observability", "prometheus", "mimir", "loki", "tempo"),
             [ds_var()],
             links=links("Observability", ("Telemetry pipeline", "telemetry")),
             collapse=1)

    d.row("Backends at a glance")
    d.line(4,
           (stat("Firing alerts", "Alerts firing right now (ALERTS), excluding the always-on Watchdog.",
                 ['count(ALERTS{alertstate="firing", alertname!~"Watchdog|InfoInhibitor"}) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 10))), 3),
           (stat("Notification failures / s", "Notifications Alertmanager failed to deliver (Console webhook, ...).",
                 [f'sum(rate(alertmanager_notifications_failed_total{{{AM}}}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (RED, 0.01)), decimals=2), 3),
           (stat("Prometheus series", "Series in Prometheus's head block.",
                 [f'max(prometheus_tsdb_head_series{{{PR}}})'], color_mode="value", graph="area"), 3),
           (stat("Rule failures / s", "Recording / alerting rule evaluations that failed.",
                 [f'sum(rate(prometheus_rule_evaluation_failures_total{{{PR}}}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (RED, 0.01)), decimals=2), 3),
           (stat("Mimir active series", "Active series held by Mimir's ingesters.",
                 [f'sum(cortex_ingester_active_series{{{MI}}})'], color_mode="value", graph="area"), 3),
           (stat("Mimir samples in / s", "Samples per second Mimir's distributors accept.",
                 [f'sum(rate(cortex_distributor_received_samples_total{{{MI}}}[5m]))'], unit="ops", color_mode="value",
                 graph="area"), 3),
           (stat("Loki lines / s", "Log lines per second Loki's distributor receives.",
                 [f'sum(rate(loki_distributor_lines_received_total{{{LK}}}[5m]))'], unit="ops", color_mode="value",
                 graph="area"), 3),
           (stat("Tempo spans / s", "Spans per second Tempo receives.",
                 [f'sum(rate(tempo_distributor_spans_received_total{{{TP}}}[5m]))'], unit="ops", color_mode="value",
                 graph="area"), 3))

    d.row("Prometheus")
    d.line(8,
           (ts("Head series & samples appended", "Series in the head block and samples appended per second.",
               [T(f'max(prometheus_tsdb_head_series{{{PR}}})', "series"),
                T(f'sum(rate(prometheus_tsdb_head_samples_appended_total{{{PR}}}[$__rate_interval]))', "samples/s")],
               overrides=[{"matcher": {"id": "byName", "options": "samples/s"},
                           "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Storage", "TSDB block bytes on disk against the size-based retention limit (dashed).",
               [T(f'max(prometheus_tsdb_storage_blocks_bytes{{{PR}}})', "blocks"),
                T(f'max(prometheus_tsdb_retention_limit_bytes{{{PR}}}) > 0', "retention limit")],
               unit="bytes", overrides=[ov_dashed("retention limit", RED)]), 8),
           (ts("Query latency p99", "99th percentile PromQL engine time by slice (queue, prepare, inner eval, "
               "result sort).",
               [T(f'max by (slice) (prometheus_engine_query_duration_seconds{{{PR}, quantile="0.99"}})', "{{slice}}")],
               unit="s"), 8))
    d.line(8,
           (ts("Rule groups: duration vs interval", "Slowest rule groups' last evaluation against their interval. "
               "Over the interval = missed evaluations.",
               [T(f'topk(10, max by (rule_group) (prometheus_rule_group_last_duration_seconds{{{PR}}}))', "{{rule_group}}")],
               unit="s"), 8),
           (ts("Rule evaluation failures & missed iterations", "Failed rule evaluations and missed group iterations "
               "per second.",
               [T(f'sum(rate(prometheus_rule_evaluation_failures_total{{{PR}}}[$__rate_interval]))', "failures"),
                T(f'sum(rate(prometheus_rule_group_iterations_missed_total{{{PR}}}[$__rate_interval]))', "missed iterations")],
               unit="ops", th=steps((GREEN, None), (RED, 0.01))), 8),
           (ts("Notifications to Alertmanager", "Alerts sent per second, errors and dropped notifications.",
               [T(f'sum(rate(prometheus_notifications_sent_total{{{PR}}}[$__rate_interval]))', "sent"),
                T(f'sum(rate(prometheus_notifications_errors_total{{{PR}}}[$__rate_interval]))', "errors"),
                T(f'sum(rate(prometheus_notifications_dropped_total{{{PR}}}[$__rate_interval]))', "dropped")],
               unit="ops", overrides=[ov_color("errors|dropped", RED)]), 8))

    d.row("Alertmanager & alerts")
    d.line(9,
           (table("Firing alerts", "Every alert firing now, by name, severity and namespace (the Console's "
                  "notification centre receives the same set).",
                  ['count by (alertname, severity, namespace) (ALERTS{alertstate="firing", alertname!~"Watchdog|InfoInhibitor"})'],
                  [("alertname", "Alert", None, None),
                   ("severity", "Severity", None, {"cell": "color-text", "mappings": vmap(
                       ("critical", "critical", RED), ("warning", "warning", AMBER), ("info", "info", BLUE))}),
                   ("namespace", "Namespace", None, None), ("Value", "Instances", "short", None)],
                  sort=("Severity", False)), 12),
           (ts("Alerts over time", "Firing and pending alerts over time by severity.",
               [T('count by (severity) (ALERTS{alertstate="firing", alertname!~"Watchdog|InfoInhibitor"})', "firing {{severity}}"),
                T('count(ALERTS{alertstate="pending"})', "pending")],
               decimals=0, overrides=[ov_color("firing critical", RED), ov_color("firing warning", AMBER), ov_dashed("pending")]), 12))
    d.line(8,
           (ts("Notifications by integration", "Notifications sent and failed per second by integration "
               "(webhook = the Console).",
               [T(f'sum by (integration) (rate(alertmanager_notifications_total{{{AM}}}[$__rate_interval]))', "{{integration}} sent"),
                T(f'sum by (integration) (rate(alertmanager_notifications_failed_total{{{AM}}}[$__rate_interval]))', "{{integration}} failed")],
               unit="ops", overrides=[ov_color(".* failed", RED)]), 8),
           (ts("Notification latency p99", "99th percentile time to deliver a notification by integration.",
               [T(f'histogram_quantile(0.99, sum by (le, integration) (rate(alertmanager_notification_latency_seconds_bucket{{{AM}}}[$__rate_interval])))', "{{integration}}")],
               unit="s"), 8),
           (ts("Alerts in Alertmanager & silences", "Alerts Alertmanager holds by state, and silences by state.",
               [T(f'max by (state) (alertmanager_alerts{{{AM}}})', "alerts {{state}}"),
                T(f'max by (state) (alertmanager_silences{{{AM}}})', "silences {{state}}")], decimals=0), 8))

    d.row("Mimir (long-term metrics)")
    d.line(8,
           (ts("Samples received & discarded", "Samples per second the distributors receive, and samples discarded "
               "by reason (limits, out of order, too old).",
               [T(f'sum(rate(cortex_distributor_received_samples_total{{{MI}}}[$__rate_interval]))', "received"),
                T(f'sum by (reason) (rate(cortex_discarded_samples_total{{{MI}}}[$__rate_interval]))', "discarded {{reason}}")],
               unit="ops", overrides=[ov_color("discarded.*", RED)]), 8),
           (ts("Ingester series", "In-memory and active series in the ingesters, and samples failing to ingest.",
               [T(f'sum(cortex_ingester_memory_series{{{MI}}})', "in memory"),
                T(f'sum(cortex_ingester_active_series{{{MI}}})', "active"),
                T(f'sum(rate(cortex_ingester_ingested_samples_failures_total{{{MI}}}[$__rate_interval]))', "ingest failures/s")],
               overrides=[ov_color("ingest failures/s", RED),
                          {"matcher": {"id": "byName", "options": "ingest failures/s"},
                           "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Request latency p99 by component", "99th percentile request latency per Mimir component (write path: "
               "distributor / ingester; read path: query-frontend / querier / store-gateway).",
               [T(f'histogram_quantile(0.99, sum by (le, job) (rate(cortex_request_duration_seconds_bucket{{{MI}}}[$__rate_interval])))', "{{job}}")],
               unit="s"), 8))
    d.line(8,
           (ts("Queries", "Queries in progress at the query-frontend and queries per second.",
               [T(f'sum(cortex_query_frontend_queries_in_progress{{{MI}}})', "in progress"),
                T(f'sum(rate(cortex_query_frontend_queries_total{{{MI}}}[$__rate_interval]))', "queries/s")],
               overrides=[{"matcher": {"id": "byName", "options": "queries/s"},
                           "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Compactor & object storage", "Hours since the compactor's last successful run, and object-storage "
               "operation failures (RustFS) per second.",
               [T(f'(time() - max(cortex_compactor_last_successful_run_timestamp_seconds{{{MI}}})) / 3600', "hours since compaction"),
                T(f'sum by (operation) (rate(thanos_objstore_bucket_operation_failures_total{{{MI}}}[$__rate_interval])) > 0', "{{operation}} failures/s")],
               overrides=[ov_color(".*failures/s", RED)]), 8),
           (ts("Mimir pods: memory", "Working-set memory per Mimir pod (ingesters hold every active series).",
               [T(f'sum by (pod) (container_memory_working_set_bytes{{job="kubelet", metrics_path="/metrics/cadvisor", {SYS}, pod=~"mimir-.+", container!=""}})', "{{pod}}")],
               unit="bytes"), 8))

    d.row("Loki (logs)")
    d.line(8,
           (ts("Ingest", "Log lines and bytes per second received by the distributor.",
               [T(f'sum(rate(loki_distributor_lines_received_total{{{LK}}}[$__rate_interval]))', "lines/s"),
                T(f'sum(rate(loki_distributor_bytes_received_total{{{LK}}}[$__rate_interval]))', "bytes/s")],
               unit="ops", overrides=[{"matcher": {"id": "byName", "options": "bytes/s"},
                                       "properties": [{"id": "unit", "value": "Bps"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Request latency p99: push vs query", "99th percentile latency of push and query routes.",
               [T(f'histogram_quantile(0.99, sum by (le) (rate(loki_request_duration_seconds_bucket{{{LK}, route=~"loki_api_v1_push|/logproto.Pusher/Push"}}[$__rate_interval])))', "push"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(loki_request_duration_seconds_bucket{{{LK}, route=~"loki_api_v1_query.*|loki_api_v1_series|loki_api_v1_label.*"}}[$__rate_interval])))', "query")],
               unit="s"), 8),
           (ts("Errors by route", "Loki requests per second answered 5xx (and 429 rate limits), by route.",
               [T(f'sum by (route, status_code) (rate(loki_request_duration_seconds_count{{{LK}, status_code=~"5..|429"}}[$__rate_interval]))', "{{route}} {{status_code}}")],
               unit="reqps", th=steps((GREEN, None), (RED, 0.01))), 8))
    d.line(8,
           (ts("Streams & chunks", "Streams in memory and chunks flushed to storage per second.",
               [T(f'sum(loki_ingester_memory_streams{{{LK}}})', "streams"),
                T(f'sum(rate(loki_ingester_chunks_flushed_total{{{LK}}}[$__rate_interval]))', "chunks flushed/s")],
               overrides=[{"matcher": {"id": "byName", "options": "chunks flushed/s"},
                           "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Retention & compaction", "Hours since the compactor last applied retention.",
               [T(f'(time() - max(loki_compactor_apply_retention_last_successful_run_timestamp_seconds{{{LK}}})) / 3600', "hours since retention")],
               th=steps((GREEN, None), (AMBER, 25), (RED, 49))), 8),
           (ts("Loki memory", "Working-set memory of the Loki pods.",
               [T(f'sum by (pod) (container_memory_working_set_bytes{{job="kubelet", metrics_path="/metrics/cadvisor", {SYS}, pod=~"loki-.+", container!=""}})', "{{pod}}")],
               unit="bytes"), 8))

    d.row("Tempo (traces)")
    d.line(8,
           (ts("Spans received & discarded", "Spans per second received, and discarded by reason (rate limits, "
               "trace too large, live traces limit).",
               [T(f'sum(rate(tempo_distributor_spans_received_total{{{TP}}}[$__rate_interval]))', "received"),
                T(f'sum by (reason) (rate(tempo_discarded_spans_total{{{TP}}}[$__rate_interval]))', "discarded {{reason}}")],
               unit="ops", overrides=[ov_color("discarded.*", RED)]), 8),
           (ts("Live traces & blocks", "Traces held in the ingester, blocks in the blocklist, and compaction "
               "backlog.",
               [T(f'sum(tempo_ingester_live_traces{{{TP}}})', "live traces"),
                T(f'max(tempodb_blocklist_length{{{TP}}})', "blocks"),
                T(f'max(tempodb_compaction_outstanding_blocks{{{TP}}})', "outstanding compaction")], decimals=0), 8),
           (ts("Request latency p99 by route", "99th percentile Tempo request latency (push vs trace lookup / "
               "search).",
               [T(f'topk(6, histogram_quantile(0.99, sum by (le, route) (rate(tempo_request_duration_seconds_bucket{{{TP}}}[$__rate_interval]))))', "{{route}}")],
               unit="s"), 8))
    d.line(8,
           (ts("Bytes received & flushed", "Bytes per second received and blocks flushed to the backend.",
               [T(f'sum(rate(tempo_distributor_bytes_received_total{{{TP}}}[$__rate_interval]))', "received"),
                T(f'sum(rate(tempo_ingester_blocks_flushed_total{{{TP}}}[$__rate_interval]))', "blocks flushed/s")],
               unit="Bps", overrides=[{"matcher": {"id": "byName", "options": "blocks flushed/s"},
                                       "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Compaction errors", "Compaction errors per second (backend unreachable or corrupt blocks).",
               [T(f'sum(rate(tempodb_compaction_errors_total{{{TP}}}[$__rate_interval]))', "errors")],
               unit="ops", th=steps((GREEN, None), (RED, 0.001))), 8),
           (_mimir(ts("Span metrics (generator, from Mimir)", "Calls per second the Tempo metrics generator derives "
               "from spans (traces_spanmetrics_calls_total). The generator remote-writes to Mimir, so this panel reads "
               "Mimir, not Prometheus.",
               [T('topk(10, sum by (service) (rate(traces_spanmetrics_calls_total[$__rate_interval])))', "{{service}}", ds=MIMIR)],
               unit="reqps")), 8))

    d.row("Grafana & pods")
    d.line(8,
           (ts("Grafana request latency p99", "99th percentile Grafana HTTP latency by handler (top 8).",
               [T(f'topk(8, histogram_quantile(0.99, sum by (le, handler) (rate(grafana_http_request_duration_seconds_bucket{{{GF}}}[$__rate_interval]))))', "{{handler}}")],
               unit="s"), 12),
           (stat("Dashboards", "Dashboards provisioned in Grafana.", [f'max(grafana_stat_totals_dashboard{{{GF}}})'],
                 color_mode="value"), 4),
           (pods_ready_stat("Backend pods", "Prometheus, Alertmanager, Grafana, Mimir, Loki and Tempo pods.",
                            "prometheus-.+|alertmanager-.+|mimir-.+|loki-.+|tempo-.+"), 4),
           (up_stat("Mimir components", "Mimir component scrape targets.",
                    "adhar-system/(distributor|ingester|querier|query-frontend|query-scheduler|store-gateway|compactor)"), 4))
    d.line(10, (pods_table("LGTM pods", "Every observability backend pod.",
                           "prometheus-prometheus-.+|alertmanager-.+|prometheus-grafana-.+|mimir-.+|loki-.+|tempo-.+"), 24))
    return d


# =============================================================================================
# Cost (OpenCost)
# =============================================================================================

def cost():
    OC = 'job="opencost"'
    NODE_CPU = f'on (node) group_left () avg by (node) (node_cpu_hourly_cost{{{OC}}})'
    NODE_RAM = f'on (node) group_left () avg by (node) (node_ram_hourly_cost{{{OC}}})'
    CPU_NS = f'sum by (namespace) (container_cpu_allocation{{{OC}, namespace=~"$namespace"}} * {NODE_CPU})'
    RAM_NS = f'sum by (namespace) (container_memory_allocation_bytes{{{OC}, namespace=~"$namespace"}} / 1073741824 * {NODE_RAM})'
    PV_NS = (f'sum by (namespace) (label_replace(kube_persistentvolumeclaim_info{{{KSM}, namespace=~"$namespace"}}, "persistentvolume", "$1", "volumename", "(.+)") '
             f'* on (persistentvolume) group_left () (avg by (persistentvolume) (pv_hourly_cost{{{OC}}}) * on (persistentvolume) group_left () '
             f'max by (persistentvolume) (kube_persistentvolume_capacity_bytes{{{KSM}}}) / 1073741824))')
    d = Dash(UIDS["cost"], "Observability / Cost",
             "What the cluster costs and who spends it, from OpenCost's list prices: nodes (CPU and RAM), persistent "
             "volumes and load balancers, allocated per namespace and workload, and what is reserved but idle. "
             "Values read 0 where OpenCost has no price for the cloud. Replaces OpenCost / Overview and OpenCost / "
             "Namespace.",
             tags("Observability", "cost", "opencost"),
             [ds_var(), qvar("namespace", "Namespace", f'label_values(container_cpu_allocation{{{OC}}}, namespace)')],
             links=links("Observability", ("Nodes & capacity", "k8s-nodes")),
             collapse=1)

    d.row("Cost at a glance")
    d.line(4,
           (stat("Cluster / hour", "Sum of every node's hourly price.", [f'sum(node_total_hourly_cost{{{OC}}})'],
                 unit="currencyUSD", color_mode="value", decimals=2), 3),
           (stat("Projected / month", "Hourly node cost x 730 hours.", [f'sum(node_total_hourly_cost{{{OC}}}) * 730'],
                 unit="currencyUSD", color_mode="value", decimals=0), 3),
           (stat("Storage / month", "Persistent volumes at their hourly price x 730.",
                 [f'sum(avg by (persistentvolume) (pv_hourly_cost{{{OC}}}) * on (persistentvolume) group_left () max by (persistentvolume) (kube_persistentvolume_capacity_bytes{{{KSM}}}) / 1073741824) * 730'],
                 unit="currencyUSD", color_mode="value", decimals=0), 3),
           (stat("Load balancers / month", "Cloud load balancers (the Gateway's) x 730.",
                 [f'sum(kubecost_load_balancer_cost{{{OC}}}) * 730 or vector(0)'], unit="currencyUSD", color_mode="value", decimals=0), 3),
           (stat("Nodes", "Nodes OpenCost prices.", [f'count(node_total_hourly_cost{{{OC}}})'], color_mode="value"), 3),
           (stat("Spot nodes", "Nodes OpenCost recognises as spot / preemptible.",
                 [f'sum(kubecost_node_is_spot{{{OC}}}) or vector(0)'], color_mode="value"), 3),
           (stat("Idle CPU (requested, unused)", "Cores reserved by requests but not used: what right-sizing "
                 "requests would give back.",
                 ['sum(cluster:namespace:pod_cpu:active:kube_pod_container_resource_requests) - sum(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_rate5m)'],
                 unit="none", decimals=1, color_mode="value"), 3),
           (stat("Idle memory (requested, unused)", "Memory reserved by requests but not in the working set.",
                 ['sum(cluster:namespace:pod_memory:active:kube_pod_container_resource_requests) - sum(node_namespace_pod_container:container_memory_working_set_bytes)'],
                 unit="bytes", decimals=1, color_mode="value"), 3))

    d.row("By namespace")
    d.line(10,
           (table("Monthly cost by namespace", "CPU, memory and storage cost per namespace (allocation x node / "
                  "volume price x 730 hours), plus its share of the total.",
                  [f'{CPU_NS} * 730', f'{RAM_NS} * 730', f'{PV_NS} * 730',
                   f'({CPU_NS} + {RAM_NS}) * 730'],
                  [("namespace", "Namespace", None, None), ("Value #A", "CPU / month", "currencyUSD", {"decimals": 2}),
                   ("Value #B", "Memory / month", "currencyUSD", {"decimals": 2}),
                   ("Value #C", "Storage / month", "currencyUSD", {"decimals": 2}),
                   ("Value #D", "Compute / month", "currencyUSD", {"decimals": 2, "cell": "gauge", "th": steps((GREEN, None), (AMBER, 100), (RED, 500)), "min": 0})],
                  sort=("Compute / month", True)), 12),
           (bargauge("Top namespaces (compute / month)", "The fifteen most expensive namespaces.",
                     [T(f'topk(15, ({CPU_NS} + {RAM_NS}) * 730)', "{{namespace}}")],
                     unit="currencyUSD", min=0, max=None, th=steps((GREEN, None), (AMBER, 100), (RED, 500)), decimals=0), 12))
    d.line(8,
           (ts("Hourly cost by namespace", "Compute cost per hour per namespace (top 10), stacked.",
               [T(f'topk(10, {CPU_NS} + {RAM_NS})', "{{namespace}}")], unit="currencyUSD", stack=True, decimals=3), 12),
           (bargauge("Top workloads (compute / month)", "The fifteen most expensive pods by allocation.",
                     [T(f'topk(15, (sum by (namespace, pod) (container_cpu_allocation{{{OC}, namespace=~"$namespace"}} * {NODE_CPU}) + '
                        f'sum by (namespace, pod) (container_memory_allocation_bytes{{{OC}, namespace=~"$namespace"}} / 1073741824 * {NODE_RAM})) * 730)', "{{namespace}}/{{pod}}")],
                     unit="currencyUSD", min=0, max=None, th=NEUTRAL, decimals=0), 12))

    d.row("Nodes & infrastructure")
    d.line(8,
           (table("Node prices", "Each node's instance type and hourly price (CPU / RAM / total).",
                  [f'max by (node, instance_type) (node_total_hourly_cost{{{OC}}})',
                   f'max by (node) (node_cpu_hourly_cost{{{OC}}})', f'max by (node) (node_ram_hourly_cost{{{OC}}})',
                   f'max by (node) (node_total_hourly_cost{{{OC}}}) * 730'],
                  [("node", "Node", None, None), ("instance_type", "Instance type", None, None),
                   ("Value #A", "Total / hour", "currencyUSD", {"decimals": 3}),
                   ("Value #B", "Per core / hour", "currencyUSD", {"decimals": 4}),
                   ("Value #C", "Per GiB / hour", "currencyUSD", {"decimals": 4}),
                   ("Value #D", "Per month", "currencyUSD", {"decimals": 0})],
                  sort=("Total / hour", True)), 12),
           (ts("Hourly cost by node", "Price per node per hour.",
               [T(f'sum by (node) (node_total_hourly_cost{{{OC}}})', "{{node}}")], unit="currencyUSD", stack=True, decimals=3), 12))
    d.line(8,
           (ts("Efficiency: CPU used vs allocated", "CPU actually used against CPU allocated (max of request and "
               "usage) cluster-wide. The gap is paid-for idle.",
               [T(f'sum(container_cpu_allocation{{{OC}}})', "allocated"),
                T('sum(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_rate5m)', "used")],
               unit="cores", overrides=[ov_dashed("allocated")]), 12),
           (ts("Efficiency: memory used vs allocated", "Memory actually used against memory allocated cluster-wide.",
               [T(f'sum(container_memory_allocation_bytes{{{OC}}})', "allocated"),
                T('sum(node_namespace_pod_container:container_memory_working_set_bytes)', "used")],
               unit="bytes", overrides=[ov_dashed("allocated")]), 12))
    return d


# =============================================================================================
# JVM services
# =============================================================================================

def jvm():
    S = 'namespace=~"$namespace", job=~"$job", pod=~"$pod"'
    d = Dash(UIDS["runtimes"], "Observability / JVM Services",
             "Every JVM service that publishes Micrometer metrics, platform or application: Keycloak, OpenMetadata, "
             "Kafka UI, Tolgee, the Strimzi operator and the applications' Spring Boot services (adhar-kit). "
             "HTTP RED, heap and GC, threads, connection pools and logs per service. An application's own "
             "dashboard (Applications folder) has the same panels scoped to it. Replaces the Spring Boot (2), JVM "
             "(Micrometer), Kafka Streams and Kafka UI dashboards.",
             tags("Observability", "jvm", "spring"),
             [ds_var(),
              qvar("namespace", "Namespace", 'label_values(jvm_memory_used_bytes, namespace)'),
              qvar("job", "Service", 'label_values(jvm_memory_used_bytes{namespace=~"$namespace"}, job)'),
              qvar("pod", "Pod", 'label_values(jvm_memory_used_bytes{namespace=~"$namespace", job=~"$job"}, pod)')],
             links=links("Observability", ("Telemetry pipeline", "telemetry")),
             collapse=1)

    d.row("Services at a glance")
    d.line(4,
           (stat("Services", "JVM services publishing Micrometer metrics in the selection.",
                 [f'count(count by (job) (jvm_memory_used_bytes{{{S}}}))'], color_mode="value"), 4),
           (stat("Instances", "JVM processes (pods) in the selection.",
                 [f'count(count by (namespace, pod) (jvm_memory_used_bytes{{{S}}}))'], color_mode="value"), 4),
           (stat("Requests / s", "HTTP requests per second served by the selected services.",
                 [f'sum(rate(http_server_requests_seconds_count{{{S}}}[5m]))'], unit="reqps", color_mode="value",
                 graph="area", decimals=1), 4),
           (stat("5xx ratio", "Share of HTTP responses that are server errors.",
                 [f'100 * (sum(rate(http_server_requests_seconds_count{{{S}, status=~"5.."}}[5m])) or vector(0)) / sum(rate(http_server_requests_seconds_count{{{S}}}[5m]))'],
                 unit="percent", th=steps((GREEN, None), (AMBER, 1), (RED, 5)), decimals=2), 4),
           (stat("Fullest heap", "Highest heap use of any instance. Near 100% the JVM spends its time in GC and "
                 "then dies with OutOfMemoryError.",
                 [f'max(100 * sum by (namespace, pod) (jvm_memory_used_bytes{{{S}, area="heap"}}) / sum by (namespace, pod) (jvm_memory_max_bytes{{{S}, area="heap"}} > 0))'],
                 unit="percent", th=PCT, decimals=0), 4),
           (stat("Worst GC time", "Highest share of time any instance spends in GC pauses.",
                 [f'max(sum by (namespace, pod) (rate(jvm_gc_pause_seconds_sum{{{S}}}[5m])))'],
                 unit="percentunit", th=steps((GREEN, None), (AMBER, 0.05), (RED, 0.15)), decimals=2), 4))
    d.line(9, (table(
        "JVM services", "One row per service instance: uptime, request rate, 5xx share, p99 latency, heap used, "
        "GC time and live threads.",
        [f'max by (namespace, job, pod) (process_uptime_seconds{{{S}}})',
         f'sum by (namespace, job, pod) (rate(http_server_requests_seconds_count{{{S}}}[5m]))',
         f'100 * (sum by (namespace, job, pod) (rate(http_server_requests_seconds_count{{{S}, status=~"5.."}}[5m])) or sum by (namespace, job, pod) (rate(http_server_requests_seconds_count{{{S}}}[5m])) * 0) / sum by (namespace, job, pod) (rate(http_server_requests_seconds_count{{{S}}}[5m]))',
         f'histogram_quantile(0.99, sum by (namespace, job, pod, le) (rate(http_server_requests_seconds_bucket{{{S}}}[5m])))',
         f'100 * sum by (namespace, job, pod) (jvm_memory_used_bytes{{{S}, area="heap"}}) / sum by (namespace, job, pod) (jvm_memory_max_bytes{{{S}, area="heap"}} > 0)',
         f'sum by (namespace, job, pod) (rate(jvm_gc_pause_seconds_sum{{{S}}}[5m]))',
         f'max by (namespace, job, pod) (jvm_threads_live_threads{{{S}}})'],
        [("namespace", "Namespace", None, None), ("job", "Service", None, None),
         ("pod", "Pod", None, {"links": pod_link(UIDS["k8s-workloads"])}),
         ("Value #A", "Uptime", "s", None), ("Value #B", "Req/s", "reqps", {"decimals": 2}),
         ("Value #C", "5xx %", "percent", {"decimals": 2, "cell": "color-text", "th": steps((GREEN, None), (AMBER, 1), (RED, 5))}),
         ("Value #D", "p99", "s", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 1), (RED, 3))}),
         ("Value #E", "Heap %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #F", "GC time", "percentunit", {"decimals": 3, "cell": "color-text", "th": steps((GREEN, None), (AMBER, 0.05), (RED, 0.15))}),
         ("Value #G", "Threads", "short", None)],
        sort=("Req/s", True)), 24))

    d.row("HTTP (RED)")
    d.line(8,
           (ts("Requests by status", "Requests per second by service and status class.",
               [T(f'sum by (job, status) (rate(http_server_requests_seconds_count{{{S}}}[$__rate_interval]))', "{{job}} {{status}}")],
               unit="reqps", overrides=[ov_color(".* 5..", RED), ov_color(".* 4..", "orange")]), 8),
           (ts("Latency p99 by service", "99th percentile server latency per service (Micrometer histograms; "
               "services without histogram buckets show nothing here — see the average).",
               [T(f'histogram_quantile(0.99, sum by (job, le) (rate(http_server_requests_seconds_bucket{{{S}}}[$__rate_interval])))', "{{job}} p99"),
                T(f'sum by (job) (rate(http_server_requests_seconds_sum{{{S}}}[$__rate_interval])) / sum by (job) (rate(http_server_requests_seconds_count{{{S}}}[$__rate_interval]))', "{{job}} avg")],
               unit="s", overrides=[ov_dashed(".* avg")]), 8),
           (bargauge("Slowest endpoints (avg)", "Mean latency of the ten slowest URI templates.",
                     [T(f'topk(10, sum by (job, uri) (rate(http_server_requests_seconds_sum{{{S}}}[5m])) / sum by (job, uri) (rate(http_server_requests_seconds_count{{{S}}}[5m]) > 0))', "{{job}} {{uri}}")],
                     unit="s", min=0, max=None, th=steps((GREEN, None), (AMBER, 0.5), (RED, 2)), decimals=3), 8))

    d.row("Memory & garbage collection")
    d.line(8,
           (ts("Heap used vs max", "Heap in use per instance against its maximum (dashed).",
               [T(f'sum by (pod) (jvm_memory_used_bytes{{{S}, area="heap"}})', "{{pod}} used"),
                T(f'sum by (pod) (jvm_memory_max_bytes{{{S}, area="heap"}} > 0)', "{{pod}} max")],
               unit="bytes", overrides=[ov_dashed(".* max", RED)]), 8),
           (ts("Memory pools", "Used bytes per memory pool (Eden, Old Gen, Metaspace, code cache ...).",
               [T(f'sum by (id) (jvm_memory_used_bytes{{{S}}})', "{{id}}")], unit="bytes", stack=True), 8),
           (ts("GC pause time & count", "Time spent in GC pauses per second, and pauses per second, by cause.",
               [T(f'sum by (pod) (rate(jvm_gc_pause_seconds_sum{{{S}}}[$__rate_interval]))', "{{pod}} pause s/s"),
                T(f'sum by (cause) (rate(jvm_gc_pause_seconds_count{{{S}}}[$__rate_interval]))', "{{cause}} pauses/s")],
               unit="percentunit", overrides=[{"matcher": {"id": "byRegexp", "options": ".*pauses/s"},
                                               "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(8,
           (ts("Allocation & promotion", "Bytes allocated and promoted to the old generation per second.",
               [T(f'sum by (pod) (rate(jvm_gc_memory_allocated_bytes_total{{{S}}}[$__rate_interval]))', "{{pod}} allocated"),
                T(f'sum by (pod) (rate(jvm_gc_memory_promoted_bytes_total{{{S}}}[$__rate_interval]))', "{{pod}} promoted")],
               unit="Bps", overrides=[ov_dashed(".* promoted")]), 8),
           (ts("Live data after GC", "Old-generation size after a full GC against its maximum: the real working "
               "set of the heap.",
               [T(f'sum by (pod) (jvm_gc_live_data_size_bytes{{{S}}})', "{{pod}} live"),
                T(f'sum by (pod) (jvm_gc_max_data_size_bytes{{{S}}})', "{{pod}} max")],
               unit="bytes", overrides=[ov_dashed(".* max", RED)]), 8),
           (ts("Buffers & classes", "Direct / mapped buffer memory and loaded classes.",
               [T(f'sum by (id) (jvm_buffer_memory_used_bytes{{{S}}})', "{{id}} buffers"),
                T(f'sum by (pod) (jvm_classes_loaded_classes{{{S}}})', "{{pod}} classes")],
               unit="bytes", overrides=[{"matcher": {"id": "byRegexp", "options": ".* classes"},
                                         "properties": [{"id": "unit", "value": "short"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("Threads, CPU & pools")
    d.line(8,
           (ts("Threads by state", "JVM threads by state. Many BLOCKED threads mean lock contention.",
               [T(f'sum by (state) (jvm_threads_states_threads{{{S}}})', "{{state}}")], stack=True, decimals=0,
               overrides=[ov_color("blocked", RED), ov_color("runnable", GREEN)]), 8),
           (ts("CPU usage", "Process CPU usage (share of the CPUs the JVM sees) and system load.",
               [T(f'max by (pod) (process_cpu_usage{{{S}}})', "{{pod}} process"),
                T(f'max by (pod) (system_cpu_usage{{{S}}})', "{{pod}} system")],
               unit="percentunit", overrides=[ov_dashed(".* system")]), 8),
           (ts("Database connection pools (HikariCP)", "Active, idle and pending connections per pool. Pending > 0 "
               "means requests wait for a database connection.",
               [T(f'sum by (pool) (hikaricp_connections_active{{{S}}})', "{{pool}} active"),
                T(f'sum by (pool) (hikaricp_connections_idle{{{S}}})', "{{pool}} idle"),
                T(f'sum by (pool) (hikaricp_connections_pending{{{S}}})', "{{pool}} pending")],
               decimals=0, overrides=[ov_color(".* pending", RED)]), 8))
    d.line(8,
           (ts("Log events by level", "Logback / Log4j events per second by level.",
               [T(f'sum by (level) (rate(logback_events_total{{{S}}}[$__rate_interval]))', "{{level}}")],
               unit="ops", overrides=[ov_color("error", RED), ov_color("warn", AMBER)]), 8),
           (ts("Kafka clients", "Records consumed / produced per second and consumer lag for services using the "
               "Kafka client (Micrometer kafka_* metrics).",
               [T(f'sum by (job) (rate(kafka_consumer_fetch_manager_records_consumed_total{{{S}}}[$__rate_interval]))', "{{job}} consumed"),
                T(f'sum by (job) (rate(kafka_producer_record_send_total{{{S}}}[$__rate_interval]))', "{{job}} produced"),
                T(f'max by (job) (kafka_consumer_fetch_manager_records_lag_max{{{S}}})', "{{job}} max lag")],
               unit="ops", overrides=[{"matcher": {"id": "byRegexp", "options": ".* max lag"},
                                       "properties": [{"id": "unit", "value": "short"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (logs("Errors (Loki)", "ERROR / WARN lines of the selected services' pods.",
                 '{namespace=~"$namespace", pod=~"$pod"} |~ "(?i)\\\\b(error|warn|exception)\\\\b"'), 8))
    return d


DASHBOARDS = [
    ("observability-telemetry", "Observability", telemetry),
    ("observability-lgtm", "Observability", lgtm),
    ("observability-cost", "Observability", cost),
    ("observability-jvm", "Observability", jvm),
]
