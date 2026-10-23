"""Data folder: five dashboards in place of twenty-one.

  PostgreSQL (CloudNativePG)       every CNPG cluster (kept uid cloudnative-pg)
  Valkey Cache                     the shared Valkey and every app cache (kept uid adhar-valkey);
                                   replaces Redis Overview and Redis HA
  Kafka (Strimzi)                  brokers + topics + consumer lag in one (kept uid
                                   adhar-kafka-cluster); replaces Kafka / Topics & Consumer Lag,
                                   Kafka Streams and the Kafka UI JVM board
  RabbitMQ                         brokers, queues and the stream protocol (kept uid Kn5xm-gZk);
                                   replaces RabbitMQ-Stream
  Search, Lakehouse & Analytics    OpenSearch, ClickHouse (PostHog), OpenMetadata, Trino and the
                                   data apps (RustFS, MongoDB, Neo4j, Airbyte, Dagster, Prefect,
                                   Metabase, MLflow, JupyterHub, Kubeflow)

MySQL, MinIO and Spark dashboards were dropped with their packages (disabled in every
profile); MongoDB's community dashboard had no panels at all.
"""
from dashlib import *  # noqa: F401,F403
from dash_common import (KSM, SYS, UIDS, component_logs, links, pods_ready_stat, pods_resources, pods_table,
                         tags, up_stat)
from dash_data_core import cloudnativepg, kafka_cluster, kafka_topics, rabbitmq, valkey


def postgres():
    d = cloudnativepg()
    d.title = "Data / PostgreSQL (CloudNativePG)"
    d.collapse = 1
    return d


def cache():
    d = valkey()
    d.title = "Data / Valkey Cache"
    d.description += " Replaces the Redis Overview and Redis HA community dashboards."
    d.collapse = 1
    return d


def kafka():
    d = kafka_cluster()
    d.title = "Data / Kafka (Strimzi)"
    d.description = ("Strimzi Kafka (KRaft): the platform's shared adhar-kafka and every composed application "
                     "cluster. Brokers and controller health, throughput, requests, replication, storage and JVM; then "
                     "topics, production and consumer lag per group from the kafka-exporter (formerly 'Kafka / Topics "
                     "& Consumer Lag').")
    d.merge(kafka_topics(), rename={"At a glance": "Topics & consumers at a glance"}, keep_first=True)
    d.collapse = {"Health at a glance", "Topics & consumers at a glance"}
    return d


def rabbit():
    d = rabbitmq()
    d.title = "Data / RabbitMQ"
    d.collapse = 1
    return d


# =============================================================================================
# Search, Lakehouse & Analytics
# =============================================================================================

DATA_APPS = ("rustfs.*|trino.*|mongodb.*|neo4j.*|airbyte.*|dagster.*|prefect.*|metabase.*|mlflow.*|"
             "hub-.*|jupyter-.*|libredb.*|kafka-ui.*|valkey-admin.*")


def analytics():
    OS = 'job="opensearch-nodes", cluster=~"$os_cluster"'
    CH = 'chi=~"$chi"'
    OM = 'job="openmetadata"'
    TR = 'job=~"coordinator|worker"'
    d = Dash(UIDS["data-analytics"], "Data / Search, Lakehouse & Analytics",
             "The analytical side of the data plane: OpenSearch (search and the platform's audit / log indices), "
             "ClickHouse behind PostHog, OpenMetadata (catalogue and lineage), Trino over the RustFS Iceberg "
             "lakehouse, and the data applications (RustFS, MongoDB, Neo4j, Airbyte, Dagster, Prefect, Metabase, "
             "MLflow, JupyterHub). Components that publish no metrics are covered by their pods' health and "
             "resources. Replaces the OpenSearch, PostHog / ClickHouse, OpenMetadata, Trino (cluster + pod) and "
             "JupyterHub dashboards.",
             tags("Data", "opensearch", "clickhouse", "trino", "lakehouse"),
             [ds_var(),
              qvar("os_cluster", "OpenSearch cluster", 'label_values(opensearch_cluster_status, cluster)'),
              qvar("os_node", "OpenSearch node", 'label_values(opensearch_jvm_uptime_seconds{cluster=~"$os_cluster"}, node)'),
              qvar("chi", "ClickHouse installation", 'label_values(chi_clickhouse_metric_Uptime, chi)')],
             links=links("Data", ("PostgreSQL", "data-postgres"), ("Kafka", "data-kafka")),
             collapse=1)
    ON = OS + ', node=~"$os_node"'

    d.row("Analytics at a glance")
    d.line(4,
           (stat("OpenSearch status", "Cluster health: green = every shard allocated, yellow = replicas "
                 "unassigned (data safe, redundancy lost), red = a primary is unassigned (data unavailable).",
                 [f'max(opensearch_cluster_status{{{OS}}})'], mappings=vmap((0, "GREEN", GREEN), (1, "YELLOW", AMBER), (2, "RED", RED)),
                 th=steps((GREEN, None), (AMBER, 1), (RED, 2)), no_value="not deployed"), 3),
           (stat("Unassigned shards", "OpenSearch shards that no node holds.",
                 [f'sum(opensearch_cluster_shards_number{{{OS}, type="unassigned"}})'], th=OK_BAD), 3),
           (stat("OpenSearch heap", "Highest JVM heap use across OpenSearch nodes. Above ~85% GC dominates and "
                 "circuit breakers start rejecting requests.",
                 [f'max(opensearch_jvm_mem_heap_used_percent{{{OS}}})'], unit="percent", th=PCT, decimals=0), 3),
           (stat("ClickHouse servers", "ClickHouse servers the Altinity operator's exporter can read (PostHog's "
                 "event store).",
                 [f'count(chi_clickhouse_metric_Uptime{{{CH}}})'], color_mode="value", no_value="not scraped"), 3),
           (up_stat("OpenMetadata", "OpenMetadata server.", "openmetadata"), 3),
           (pods_ready_stat("Trino", "Trino coordinator and workers.", "trino-.+"), 3),
           (pods_ready_stat("RustFS", "The platform's S3 object store and Iceberg catalogue.", "rustfs-.+"), 3),
           (stat("Data apps not ready", "Pods of the data applications (see the table below) that are not ready.",
                 [f'count(kube_pod_status_ready{{{KSM}, {SYS}, pod=~"{DATA_APPS}", condition="true"}} == 0 '
                  f'and on (namespace, pod) (kube_pod_status_phase{{{KSM}, phase="Running"}} == 1)) or vector(0)'],
                 th=OK_BAD), 3))

    d.row("OpenSearch")
    d.line(8,
           (timeline("Cluster status", "Health over time (green / yellow / red).",
                     [T(f'max by (cluster) (opensearch_cluster_status{{{OS}}})', "{{cluster}}")],
                     vmap((0, "green", GREEN), (1, "yellow", AMBER), (2, "red", RED))), 8),
           (ts("Shards by state", "Active, initializing, relocating and unassigned shards.",
               [T(f'sum by (type) (opensearch_cluster_shards_number{{{OS}}})', "{{type}}")], decimals=0,
               overrides=[ov_color("unassigned", RED), ov_color("initializing", AMBER)]), 8),
           (ts("Documents & store size", "Documents in all indices (primaries) and total store size.",
               [T(f'sum(opensearch_indices_doc_number{{{ON}}})', "documents"),
                T(f'sum(opensearch_indices_store_size_bytes{{{ON}}})', "store")],
               overrides=[{"matcher": {"id": "byName", "options": "store"},
                           "properties": [{"id": "unit", "value": "bytes"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(8,
           (ts("Indexing rate & latency", "Documents indexed per second and average time per document. Failed "
               "indexing operations are drawn red.",
               [T(f'sum(rate(opensearch_indices_indexing_index_count{{{ON}}}[$__rate_interval]))', "indexed/s"),
                T(f'sum(rate(opensearch_indices_indexing_index_failed_count{{{ON}}}[$__rate_interval]))', "failed/s"),
                T(f'sum(rate(opensearch_indices_indexing_index_time_seconds{{{ON}}}[$__rate_interval])) / sum(rate(opensearch_indices_indexing_index_count{{{ON}}}[$__rate_interval]))', "latency")],
               unit="ops", overrides=[ov_color("failed/s", RED),
                                      {"matcher": {"id": "byName", "options": "latency"},
                                       "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Search rate & latency", "Search queries per second and average query / fetch phase time.",
               [T(f'sum(rate(opensearch_indices_search_query_count{{{ON}}}[$__rate_interval]))', "queries/s"),
                T(f'sum(rate(opensearch_indices_search_query_time_seconds{{{ON}}}[$__rate_interval])) / sum(rate(opensearch_indices_search_query_count{{{ON}}}[$__rate_interval]))', "query latency"),
                T(f'sum(rate(opensearch_indices_search_fetch_time_seconds{{{ON}}}[$__rate_interval])) / sum(rate(opensearch_indices_search_fetch_count{{{ON}}}[$__rate_interval]))', "fetch latency")],
               unit="ops", overrides=[{"matcher": {"id": "byRegexp", "options": ".*latency"},
                                       "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Thread pool queues & rejections", "Tasks queued and rejected per thread pool. Rejections on write / "
               "search mean the node cannot keep up and clients get 429s.",
               [T(f'sum by (name) (opensearch_threadpool_tasks_number{{{ON}, type="queue"}}) > 0', "{{name}} queued"),
                T(f'sum by (name) (rate(opensearch_threadpool_tasks_number{{{ON}, type="rejected"}}[$__rate_interval])) > 0', "{{name}} rejected/s")],
               overrides=[ov_color(".*rejected.*", RED)]), 8))
    d.line(8,
           (ts("JVM heap & GC", "Heap used per node against its maximum (dashed), and GC time per second.",
               [T(f'max by (node) (opensearch_jvm_mem_heap_used_bytes{{{ON}}})', "{{node}} used"),
                T(f'max by (node) (opensearch_jvm_mem_heap_max_bytes{{{ON}}})', "{{node}} max"),
                T(f'sum by (node) (rate(opensearch_jvm_gc_collection_time_seconds{{{ON}}}[$__rate_interval]))', "{{node}} GC s/s")],
               unit="bytes", overrides=[ov_dashed(".* max", RED),
                                        {"matcher": {"id": "byRegexp", "options": ".*GC.*"},
                                         "properties": [{"id": "unit", "value": "percentunit"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Disk free vs watermarks", "Free disk per node as a share of its data path, against the high and "
               "flood-stage watermarks. At flood stage OpenSearch makes every index read-only.",
               [T(f'100 * min by (node) (opensearch_fs_total_available_bytes{{{ON}}} / opensearch_fs_total_total_bytes{{{ON}}})', "{{node}} free %"),
                T(f'100 - max(opensearch_cluster_routing_allocation_disk_watermark_high_pct{{{OS}}})', "high watermark"),
                T(f'100 - max(opensearch_cluster_routing_allocation_disk_watermark_flood_stage_pct{{{OS}}})', "flood stage")],
               unit="percent", overrides=[ov_dashed("high watermark", AMBER), ov_dashed("flood stage", RED)]), 8),
           (ts("Caches & circuit breakers", "Query / request cache hit ratio, and circuit breakers tripped (requests "
               "refused to protect the heap).",
               [T(f'sum(rate(opensearch_indices_querycache_hit_count{{{ON}}}[$__rate_interval])) / (sum(rate(opensearch_indices_querycache_hit_count{{{ON}}}[$__rate_interval])) + sum(rate(opensearch_indices_querycache_miss_number{{{ON}}}[$__rate_interval])))', "query cache hit"),
                T(f'sum(rate(opensearch_indices_requestcache_hit_count{{{ON}}}[$__rate_interval])) / (sum(rate(opensearch_indices_requestcache_hit_count{{{ON}}}[$__rate_interval])) + sum(rate(opensearch_indices_requestcache_miss_count{{{ON}}}[$__rate_interval])))', "request cache hit"),
                T(f'sum by (name) (increase(opensearch_circuitbreaker_tripped_count{{{ON}}}[$__rate_interval])) > 0', "{{name}} tripped")],
               unit="percentunit", overrides=[{"matcher": {"id": "byRegexp", "options": ".*tripped"},
                                               "properties": [{"id": "unit", "value": "short"}, {"id": "custom.drawStyle", "value": "bars"},
                                                              {"id": "custom.axisPlacement", "value": "right"},
                                                              {"id": "color", "value": {"mode": "fixed", "fixedColor": RED}}]}]), 8))
    d.line(8, (table("Largest indices", "Indices by store size: documents, size and shard / replica counts "
                     "(primaries context).",
                     [f'topk(20, sum by (index) (opensearch_index_store_size_bytes{{{OS}, context="total"}}))',
                      f'sum by (index) (opensearch_index_doc_number{{{OS}, context="primaries"}})',
                      f'max by (index) (opensearch_index_shards_number{{{OS}, type="active"}})',
                      f'max by (index) (opensearch_index_replicas_number{{{OS}}})'],
                     [("index", "Index", None, None), ("Value #A", "Store", "bytes", None),
                      ("Value #B", "Documents", "short", None), ("Value #C", "Active shards", "short", None),
                      ("Value #D", "Replicas", "short", None)],
                     sort=("Store", True)), 24))

    d.row("ClickHouse (PostHog)")
    d.line(8,
           (ts("Queries", "Running queries per server, and SELECT / INSERT queries started per second.",
               [T(f'sum by (hostname) (chi_clickhouse_metric_Query{{{CH}}})', "{{hostname}} running"),
                T(f'sum(rate(chi_clickhouse_event_SelectQuery{{{CH}}}[$__rate_interval]))', "select/s"),
                T(f'sum(rate(chi_clickhouse_event_InsertQuery{{{CH}}}[$__rate_interval]))', "insert/s")]), 8),
           (ts("Rows inserted & merged", "Rows written per second by inserts and by background merges.",
               [T(f'sum(rate(chi_clickhouse_event_InsertedRows{{{CH}}}[$__rate_interval]))', "inserted"),
                T(f'sum(rate(chi_clickhouse_event_MergedRows{{{CH}}}[$__rate_interval]))', "merged")], unit="ops"), 8),
           (ts("Parts pressure", "Highest part count in any partition. Over ~300 ClickHouse delays inserts, over "
               "~3000 it rejects them ('too many parts') — PostHog ingestion then backs up into Kafka.",
               [T(f'max by (hostname) (chi_clickhouse_metric_MaxPartCountForPartition{{{CH}}})', "{{hostname}}")],
               th=steps((GREEN, None), (AMBER, 300), (RED, 1000)), decimals=0), 8))
    d.line(8,
           (ts("Memory", "Memory tracked for queries and the server's resident memory.",
               [T(f'sum by (hostname) (chi_clickhouse_metric_MemoryTracking{{{CH}}})', "{{hostname}} queries"),
                T(f'sum by (hostname) (chi_clickhouse_metric_MemoryResident{{{CH}}})', "{{hostname}} resident")],
               unit="bytes"), 8),
           (ts("Disk: data size & free", "MergeTree data on disk and the free share of the volume.",
               [T(f'sum by (hostname) (chi_clickhouse_metric_DiskDataBytes{{{CH}}})', "{{hostname}} data"),
                T(f'min by (hostname) (chi_clickhouse_metric_DiskFreeBytes{{{CH}}} / chi_clickhouse_metric_DiskTotalBytes{{{CH}}})', "{{hostname}} free")],
               unit="bytes", overrides=[{"matcher": {"id": "byRegexp", "options": ".* free"},
                                         "properties": [{"id": "unit", "value": "percentunit"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Connections, replication & fetch errors", "TCP / HTTP connections, worst replica delay, and "
               "scrape errors of the operator's metrics exporter (a failing server reads as 'fetch errors').",
               [T(f'sum by (hostname) (chi_clickhouse_metric_TCPConnection{{{CH}}} + chi_clickhouse_metric_HTTPConnection{{{CH}}})', "{{hostname}} connections"),
                T(f'max by (hostname) (chi_clickhouse_metric_ReplicasMaxAbsoluteDelay{{{CH}}})', "{{hostname}} replica delay (s)"),
                T(f'sum by (fetch_type) (chi_clickhouse_metric_fetch_errors{{{CH}}})', "fetch errors {{fetch_type}}")],
               overrides=[ov_color("fetch errors.*", RED)]), 8))

    d.row("OpenMetadata")
    d.line(8,
           (ts("API requests & latency", "OpenMetadata API requests per second by status and the 99th percentile "
               "latency.",
               [T(f'sum by (status) (rate(http_server_requests_sec_seconds_count{{{OM}}}[$__rate_interval]))', "{{status}}"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(http_server_requests_sec_seconds_bucket{{{OM}}}[$__rate_interval])))', "p99")],
               unit="reqps", overrides=[ov_color("5..", RED),
                                        {"matcher": {"id": "byName", "options": "p99"},
                                         "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Database queries", "Queries per second to its PostgreSQL and the 99th percentile query time.",
               [T(f'sum(rate(db_query_duration_seconds_count{{{OM}}}[$__rate_interval]))', "queries/s"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(db_query_duration_seconds_bucket{{{OM}}}[$__rate_interval])))', "p99"),
                T(f'sum(rate(db_errors_total{{{OM}}}[$__rate_interval]))', "errors/s")],
               unit="ops", overrides=[ov_color("errors/s", RED),
                                      {"matcher": {"id": "byName", "options": "p99"},
                                       "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Ingestion, auth & storage errors", "Entity operations, authentication failures, S3 (RustFS) "
               "errors and dropped ingestion log lines.",
               [T(f'sum(rate(entity_operations_total{{{OM}}}[$__rate_interval]))', "entity ops/s"),
                T(f'sum(rate(auth_failures_total{{{OM}}}[$__rate_interval]))', "auth failures/s"),
                T(f'sum(rate(om_s3_errors_total{{{OM}}}[$__rate_interval]))', "S3 errors/s"),
                T(f'sum(rate(om_ingestion_logs_dropped_total{{{OM}}}[$__rate_interval]))', "logs dropped/s")],
               unit="ops", overrides=[ov_color("(auth|S3).*", RED)]), 8))

    d.row("Trino (Iceberg on RustFS)")
    d.line(8,
           (ts("Queries", "Running and queued queries, and failed queries per second (JMX exporter on the "
               "coordinator).",
               [T(f'sum(trino_execution_QueryManager_RunningQueries{{{TR}}})', "running"),
                T(f'sum(trino_execution_QueryManager_QueuedQueries{{{TR}}})', "queued"),
                T(f'sum(rate(trino_execution_QueryManager_FailedQueries_TotalCount{{{TR}}}[$__rate_interval]))', "failed/s")],
               overrides=[ov_color("failed/s", RED)]), 8),
           (ts("Cluster memory", "Distributed memory reserved by queries against the pool total, and queries killed "
               "for running out of memory.",
               [T(f'sum(trino_memory_ClusterMemoryPool_ReservedDistributedBytes{{{TR}}})', "reserved"),
                T(f'sum(trino_memory_ClusterMemoryPool_TotalDistributedBytes{{{TR}}})', "total"),
                T(f'sum(trino_memory_ClusterMemoryManager_QueriesKilledDueToOutOfMemory{{{TR}}})', "OOM-killed queries")],
               unit="bytes", overrides=[ov_dashed("total"),
                                        {"matcher": {"id": "byName", "options": "OOM-killed queries"},
                                         "properties": [{"id": "unit", "value": "short"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Active workers & input", "Workers the coordinator's failure detector sees, and input bytes "
               "consumed per second.",
               [T(f'max(trino_failuredetector_HeartbeatFailureDetector_ActiveCount{{{TR}}})', "workers"),
                T(f'sum(rate(trino_execution_QueryManager_ConsumedInputBytes_TotalCount{{{TR}}}[$__rate_interval]))', "input B/s")],
               overrides=[{"matcher": {"id": "byName", "options": "input B/s"},
                           "properties": [{"id": "unit", "value": "Bps"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("Data applications")
    d.line(10, (pods_table("Data application pods", "RustFS, Trino, MongoDB, Neo4j, Airbyte, Dagster, Prefect, "
                           "Metabase, MLflow, JupyterHub, LibreDB Studio, Kafka UI and Valkey Admin.", DATA_APPS), 24))
    d.line(8, *[(p, 8) for p in pods_resources(DATA_APPS)])
    d.line(9, (component_logs("Data application errors (Loki)", "Errors from the data applications.",
                              f'namespace="adhar-system", pod=~"{DATA_APPS}"'), 24))
    return d


DASHBOARDS = [
    ("cloudnativepg", "Data", postgres),
    ("redis", "Data", cache),
    ("kafka-cluster", "Data", kafka),
    ("rabbitmq-overview", "Data", rabbit),
    ("data-analytics", "Data", analytics),
]
