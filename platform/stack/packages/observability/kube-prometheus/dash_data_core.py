"""Data folder: PostgreSQL (CloudNativePG), Valkey, Kafka (Strimzi brokers + topics +
consumer lag) and RabbitMQ — the backing services every package and every composed
application uses. Each is one dashboard; the Kafka dashboard folds the former
"Kafka / Topics & Consumer Lag" in as sections below the broker view.

Hand-authored because grafana.com's community dashboards for these services were
written for other exporters / label sets and mostly showed "No data" on this platform.
"""
from dashlib import *  # noqa: F401,F403
from dash_common import UIDS, links, tags

POD_DASH = UIDS["k8s-workloads"]
OVERVIEW_UID = UIDS["k8s-overview"]
DATA_TAGS = ["adhar", "adhar-data"]


def dash_links(*extra):
    return links("Data", *extra)

# =============================================================================================
# CloudNativePG
# =============================================================================================

def cloudnativepg():
    C = 'namespace=~"$namespace", pod=~"$instance"'
    CC = 'namespace=~"$namespace", cluster=~"$cluster", pod=~"$instance"'
    KSM = 'job="kube-state-metrics"'

    def bycl(inner, agg="sum", extra=""):
        return (f'{agg} by (namespace, cluster{extra}) (label_replace({inner}, "cluster", "$1", "pod", "(.+)-[0-9]+"))')

    def inst(inner):
        return f'label_replace({inner}, "cluster", "$1", "pod", "(.+)-[0-9]+")'

    d = Dash("cloudnative-pg", "CloudNativePG / PostgreSQL",
             "Every CloudNativePG cluster on the platform (the platform databases in adhar-system and "
             "application databases in the environment namespaces): health, roles, replication, WAL "
             "archiving and barman backups to RustFS, throughput, internals and pod resources.",
             DATA_TAGS + ["postgresql", "cloudnativepg"],
             [ds_var(),
              qvar("namespace", "Namespace", "label_values(cnpg_collector_up, namespace)"),
              qvar("cluster", "Cluster", 'label_values(cnpg_collector_up{namespace=~"$namespace"}, cluster)'),
              qvar("instance", "Instance", 'label_values(cnpg_collector_up{namespace=~"$namespace", cluster=~"$cluster"}, pod)')],
             links=dash_links(("Kubernetes overview", OVERVIEW_UID)))

    d.row("Health at a glance")
    d.line(4,
           (stat("Clusters", "Number of CloudNativePG clusters reporting metrics for the current selection.",
                 [E('count(count by (namespace, cluster) (cnpg_collector_up{<<CC>>}))', CC=CC)], color_mode="value"), 3),
           (stat("Instances down", "Instances whose CNPG metrics exporter reports it cannot reach PostgreSQL "
                 "(cnpg_collector_up == 0). Anything above zero means a database pod is running but PostgreSQL is not answering.",
                 [E('count(cnpg_collector_up{<<CC>>} == 0) or vector(0)', CC=CC)], th=OK_BAD), 3),
           (stat("Primaries", "Instances currently acting as primary (not in recovery). Expect exactly one per cluster.",
                 [E('count(cnpg_pg_replication_in_recovery{<<C>>} == 0) or vector(0)', C=C)], color_mode="value",
                 th=steps((GREEN, None))), 3),
           (stat("Replicas", "Instances in recovery, i.e. streaming standbys. Zero is normal for single-instance clusters.",
                 [E('count(cnpg_pg_replication_in_recovery{<<C>>} == 1) or vector(0)', C=C)], color_mode="value"), 3),
           (stat("Max replication lag", "Worst replay lag across all standbys (cnpg_pg_replication_lag). Above a few seconds a "
                 "failover would lose recent transactions or the standby cannot keep up with the primary's WAL rate.",
                 [E('max(cnpg_pg_replication_lag{<<C>>}) or vector(0)', C=C)], unit="s",
                 th=steps((GREEN, None), (AMBER, 10), (RED, 60))), 3),
           (stat("WAL archiving failing", "Instances whose most recent WAL archive attempt failed after the last success "
                 "(pg_stat_archiver.last_failed_time > last_archived_time). WAL that cannot reach the barman object store "
                 "(RustFS) piles up in pg_wal until the volume fills and point-in-time recovery is broken.",
                 [E('count(cnpg_pg_stat_archiver_last_failed_time{<<C>>} > cnpg_pg_stat_archiver_last_archived_time{<<C>>}) or vector(0)', C=C)],
                 th=OK_BAD), 3),
           (stat("Oldest last backup", "Age of the most recent completed base backup, worst cluster first. ScheduledBackups "
                 "run daily, so anything over ~26h means a backup was missed. 'No backup yet' until the first one completes.",
                 [E('max(time() - (cnpg_collector_last_available_backup_timestamp{<<C>>} > 0))', C=C)], unit="s",
                 th=steps((GREEN, None), (AMBER, 93600), (RED, 180000)), no_value="No backup yet", decimals=1), 3),
           (stat("Max connection usage", "Highest share of max_connections in use on any instance. Beyond ~80% new clients "
                 "start failing with 'too many clients'.",
                 [E('max(100 * sum by (namespace, pod) (cnpg_backends_total{<<C>>}) / on (namespace, pod) '
                    'max by (namespace, pod) (cnpg_pg_settings_setting{<<C>>, name="max_connections"}))', C=C)],
                 unit="percent", th=PCT, decimals=1), 3))

    role_map = vmap((0, "Primary", GREEN), (1, "Replica", BLUE))
    d.line(9, (table(
        "Instances", "One row per PostgreSQL instance. Role and up come from the CNPG exporter; volume usage is the "
        "database + WAL size against the PVC request (node-local volumes report no kubelet volume stats). Click a pod "
        "to open its Kubernetes resource dashboard.",
        [inst(E('max by (namespace, pod) (cnpg_pg_replication_in_recovery{<<C>>})', C=C)),
         inst(E('max by (namespace, pod) (cnpg_collector_up{<<C>>})', C=C)),
         inst(E('max by (namespace, pod, full) (cnpg_collector_postgres_version{<<C>>})', C=C)),
         inst(E('sum by (namespace, pod) (cnpg_backends_total{<<C>>})', C=C)),
         inst(E('100 * sum by (namespace, pod) (cnpg_backends_total{<<C>>}) / on (namespace, pod) max by (namespace, pod) (cnpg_pg_settings_setting{<<C>>, name="max_connections"})', C=C)),
         inst(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_xact_commit{<<C>>}[5m]) + rate(cnpg_pg_stat_database_xact_rollback{<<C>>}[5m]))', C=C)),
         inst(E('sum by (namespace, pod) (cnpg_pg_database_size_bytes{<<C>>})', C=C)),
         inst(E('max by (namespace, pod) (cnpg_pg_replication_lag{<<C>>})', C=C)),
         inst(E('max by (namespace, pod) (cnpg_collector_pg_wal_archive_status{<<C>>, value="ready"})', C=C)),
         inst(E('max by (namespace, pod) (cnpg_pg_stat_archiver_seconds_since_last_archival{<<C>>})', C=C)),
         inst(E('max by (namespace, pod) (time() - (cnpg_collector_last_available_backup_timestamp{<<C>>} > 0))', C=C)),
         inst(E('sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{<<C>>, container="postgres"}[5m]))', C=C)),
         inst(E('sum by (namespace, pod) (container_memory_working_set_bytes{<<C>>, container="postgres"})', C=C)),
         inst(E('100 * (sum by (namespace, pod) (cnpg_pg_database_size_bytes{<<C>>}) + max by (namespace, pod) (cnpg_collector_pg_wal{<<C>>, value="size"})) '
                '/ on (namespace, pod) label_replace(max by (namespace, persistentvolumeclaim) (kube_persistentvolumeclaim_resource_requests_storage_bytes{<<KSM>>, namespace=~"$namespace", persistentvolumeclaim=~"$instance"}), "pod", "$1", "persistentvolumeclaim", "(.+)")', C=C, KSM=KSM)),
         inst(E('max by (namespace, pod) (kube_pod_container_status_restarts_total{<<KSM>>, <<C>>, container="postgres"})', C=C, KSM=KSM))],
        [("namespace", "Namespace", None, None),
         ("cluster", "Cluster", None, None),
         ("pod", "Instance", None, {"links": pod_link()}),
         ("Value #A", "Role", None, {"mappings": role_map, "cell": "color-text"}),
         ("Value #B", "Up", None, {"mappings": vmap((0, "DOWN", RED), (1, "UP", GREEN)), "cell": "color-background"}),
         ("full", "Version", None, None),
         ("Value #D", "Conns", "short", None),
         ("Value #E", "Conn %", "percent", {"cell": "color-background", "th": PCT, "decimals": 0}),
         ("Value #F", "TPS", "ops", {"decimals": 1}),
         ("Value #G", "DB size", "bytes", None),
         ("Value #H", "Lag", "s", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 10), (RED, 60))}),
         ("Value #I", "WAL ready", "short", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 10), (RED, 50))}),
         ("Value #J", "Since archive", "s", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 600), (RED, 3600))}),
         ("Value #K", "Last backup", "s", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 93600), (RED, 180000)), "novalue": "never"}),
         ("Value #L", "CPU", "cores", {"decimals": 3}),
         ("Value #M", "Memory", "bytes", None),
         ("Value #N", "Volume %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #O", "Restarts", "short", {"cell": "color-text", "th": OK_WARN_BAD})],
        sort=("Cluster", False), hide=("Value #C",)), 24))

    d.line(7,
           (timeline("Role over time", "Primary/replica role of each instance. A colour change is a switchover or failover.",
                     [T(E('max by (pod) (cnpg_pg_replication_in_recovery{<<C>>})', C=C), "{{pod}}")], role_map), 12),
           (timeline("Exporter can reach PostgreSQL", "cnpg_collector_up per instance over time. Red bands are periods where "
                     "PostgreSQL did not answer the CNPG exporter (crash, restart, recovery).",
                     [T(E('max by (pod) (cnpg_collector_up{<<CC>>})', CC=CC), "{{pod}}")],
                     vmap((0, "Down", RED), (1, "Up", GREEN))), 12))

    d.row("Connections & activity")
    d.line(8,
           (ts("Connections by state", "Backends per instance split by state. Many 'idle in transaction' backends hold locks "
               "and block vacuum.", [T(E('sum by (pod, state) (cnpg_backends_total{<<C>>})', C=C), "{{pod}} {{state}}")],
               stack=True), 8),
           (bargauge("Connections vs max_connections", "Share of max_connections in use per instance.",
                     [T(E('100 * sum by (pod) (cnpg_backends_total{<<C>>}) / on (pod) max by (pod) (cnpg_pg_settings_setting{<<C>>, name="max_connections"})', C=C), "{{pod}}")]), 8),
           (ts("Waiting on locks & longest transaction", "Backends waiting on a lock (cnpg_backends_waiting_total) and the "
               "age of the oldest open transaction per instance. A long transaction blocks autovacuum and replication slot "
               "cleanup; lock waits mean contention.",
               [T(E('sum by (pod) (cnpg_backends_waiting_total{<<C>>})', C=C), "{{pod}} waiting"),
                T(E('max by (pod) (cnpg_backends_max_tx_duration_seconds{<<C>>})', C=C), "{{pod}} oldest tx (s)")],
               overrides=[{"matcher": {"id": "byRegexp", "options": ".*oldest tx.*"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(8,
           (ts("Transactions / s", "Commits (above the axis) and rollbacks (below) per cluster.",
               [T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_xact_commit{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} commit"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_xact_rollback{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} rollback")],
               unit="ops", overrides=[ov_negative(".*rollback")]), 12),
           (ts("Rollback ratio", "Rollbacks as a share of all transactions per cluster. A sudden rise usually means an "
               "application error path (constraint violations, serialization failures).",
               [T(bycl(E('100 * sum by (namespace, pod) (rate(cnpg_pg_stat_database_xact_rollback{<<C>>}[$__rate_interval])) / '
                         '(sum by (namespace, pod) (rate(cnpg_pg_stat_database_xact_commit{<<C>>}[$__rate_interval]) + '
                         'rate(cnpg_pg_stat_database_xact_rollback{<<C>>}[$__rate_interval])) > 0)', C=C), "max"), "{{cluster}}")],
               unit="percent", th=steps((GREEN, None), (AMBER, 5), (RED, 20)), soft_max=10), 6),
           (ts("Deadlocks & conflicts", "Deadlocks detected and recovery conflicts (queries cancelled on a standby) per "
               "minute. Any deadlock is an application locking-order bug.",
               [T(bycl(E('sum by (namespace, pod) (increase(cnpg_pg_stat_database_deadlocks{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} deadlocks"),
                T(bycl(E('sum by (namespace, pod) (increase(cnpg_pg_stat_database_conflicts{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} conflicts")],
               draw="bars", decimals=0), 6))
    d.line(8,
           (ts("Row activity", "Rows inserted / updated / deleted / fetched per second per cluster.",
               [T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_tup_inserted{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} inserted"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_tup_updated{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} updated"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_tup_deleted{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} deleted"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_tup_fetched{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} fetched")],
               unit="short"), 12),
           (ts("Temp files (sorts / hashes spilling to disk)", "Bytes written to temporary files per second. Sustained "
               "temp I/O means work_mem is too small for the queries being run.",
               [T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_temp_bytes{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}}")],
               unit="Bps"), 12))

    d.row("Replication & high availability")
    d.line(8,
           (ts("Replication lag", "Replay lag of each standby in seconds (0 on primaries).",
               [T(E('max by (pod) (cnpg_pg_replication_lag{<<C>>})', C=C), "{{pod}}")], unit="s",
               th=steps((GREEN, None), (AMBER, 10), (RED, 60))), 8),
           (ts("Standby write / flush / replay lag", "Per-standby lag as seen from the primary's pg_stat_replication. "
               "Empty for single-instance clusters, which have no standby.",
               [T(E('max by (pod, application_name) (cnpg_pg_stat_replication_write_lag_seconds{<<C>>})', C=C), "{{application_name}} write"),
                T(E('max by (pod, application_name) (cnpg_pg_stat_replication_flush_lag_seconds{<<C>>})', C=C), "{{application_name}} flush"),
                T(E('max by (pod, application_name) (cnpg_pg_stat_replication_replay_lag_seconds{<<C>>})', C=C), "{{application_name}} replay")],
               unit="s"), 8),
           (ts("Streaming & synchronous replicas", "Standbys streaming from each primary, and synchronous replicas "
               "observed vs expected. Observed below expected means commits wait or synchronous replication is degraded.",
               [T(E('sum by (pod) (cnpg_pg_replication_streaming_replicas{<<C>>})', C=C), "{{pod}} streaming"),
                T(E('max by (pod) (cnpg_collector_sync_replicas{<<C>>, value="observed"})', C=C), "{{pod}} sync observed"),
                T(E('max by (pod) (cnpg_collector_sync_replicas{<<C>>, value="expected"})', C=C), "{{pod}} sync expected")],
               decimals=0, overrides=[ov_dashed(".*expected")]), 8))

    d.row("WAL archiving & backups (barman -> RustFS)")
    d.line(4,
           (stat("WAL archived / h", "WAL segments shipped to the object store in the last hour.",
                 [E('sum(increase(cnpg_pg_stat_archiver_archived_count{<<C>>}[1h]))', C=C)], color_mode="value", decimals=0), 4),
           (stat("Archive failures / h", "Failed archive_command runs in the last hour. barman-cloud retries, but a "
                 "persistent failure means the object store is unreachable or the credentials are wrong.",
                 [E('sum(increase(cnpg_pg_stat_archiver_failed_count{<<C>>}[1h])) or vector(0)', C=C)], th=OK_WARN_BAD, decimals=0), 4),
           (stat("WAL files waiting", "WAL segments marked .ready but not yet archived, worst instance. Growing = archiving "
                 "is falling behind.", [E('max(cnpg_collector_pg_wal_archive_status{<<C>>, value="ready"})', C=C)],
                 th=steps((GREEN, None), (AMBER, 10), (RED, 50))), 4),
           (stat("Longest since archive", "Longest time any instance has gone without archiving a segment. Idle databases "
                 "archive on archive_timeout, so a value far above it means archiving stopped.",
                 [E('max(cnpg_pg_stat_archiver_seconds_since_last_archival{<<C>>})', C=C)], unit="s",
                 th=steps((GREEN, None), (AMBER, 900), (RED, 3600))), 4),
           (stat("Shortest recovery window", "Smallest point-in-time-recovery window across clusters: now minus the first "
                 "recoverability point (oldest backup still retained).",
                 [E('min(time() - (cnpg_collector_first_recoverability_point{<<C>>} > 0))', C=C)], unit="s",
                 color_mode="value", no_value="No backup yet"), 4),
           (stat("Failed backups", "Instances whose last failed backup is newer than their last successful one.",
                 [E('count(cnpg_collector_last_failed_backup_timestamp{<<C>>} > cnpg_collector_last_available_backup_timestamp{<<C>>}) or vector(0)', C=C)],
                 th=OK_BAD), 4))
    d.line(8,
           (ts("WAL generation", "Bytes of WAL written per second per cluster (pg_stat_wal). Drives archive volume and "
               "replication bandwidth.",
               [T(bycl(E('sum by (namespace, pod) (rate(cnpg_collector_wal_bytes{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}}")],
               unit="Bps"), 8),
           (ts("Archived vs failed segments", "WAL segments archived (above) and failed (below) per minute per cluster.",
               [T(bycl(E('sum by (namespace, pod) (increase(cnpg_pg_stat_archiver_archived_count{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} archived"),
                T(bycl(E('sum by (namespace, pod) (increase(cnpg_pg_stat_archiver_failed_count{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} failed")],
               draw="bars", decimals=0, overrides=[ov_negative(".*failed"), ov_color(".*failed", RED)]), 8),
           (ts("pg_wal size", "On-disk size of pg_wal per instance. It grows when archiving or a replication slot falls "
               "behind; if it reaches the volume size PostgreSQL stops.",
               [T(E('max by (pod) (cnpg_collector_pg_wal{<<C>>, value="size"})', C=C), "{{pod}}")], unit="bytes"), 8))
    d.line(7, (table(
        "Backups per cluster", "Last successful base backup, last failed backup and the start of the recovery window per "
        "instance (barman-cloud to s3://adhar-backups/cnpg/<cluster> on RustFS). 'never' = no backup has completed yet; "
        "ScheduledBackups run daily.",
        [inst(E('max by (namespace, pod) (cnpg_collector_last_available_backup_timestamp{<<C>>} * 1000 > 0)', C=C)),
         inst(E('max by (namespace, pod) (time() - (cnpg_collector_last_available_backup_timestamp{<<C>>} > 0))', C=C)),
         inst(E('max by (namespace, pod) (cnpg_collector_last_failed_backup_timestamp{<<C>>} * 1000 > 0)', C=C)),
         inst(E('max by (namespace, pod) (cnpg_collector_first_recoverability_point{<<C>>} * 1000 > 0)', C=C)),
         inst(E('max by (namespace, pod) (cnpg_pg_stat_archiver_archived_count{<<C>>})', C=C)),
         inst(E('max by (namespace, pod) (cnpg_pg_stat_archiver_failed_count{<<C>>})', C=C))],
        [("namespace", "Namespace", None, None), ("cluster", "Cluster", None, None), ("pod", "Instance", None, None),
         ("Value #A", "Last backup", "dateTimeAsIso", {"novalue": "never"}),
         ("Value #B", "Backup age", "s", {"cell": "color-background", "th": steps((GREEN, None), (AMBER, 93600), (RED, 180000)), "novalue": "never"}),
         ("Value #C", "Last failed backup", "dateTimeAsIso", {"novalue": "none"}),
         ("Value #D", "Recoverable from", "dateTimeAsIso", {"novalue": "-"}),
         ("Value #E", "WAL archived (total)", "short", None),
         ("Value #F", "Archive failures (total)", "short", {"cell": "color-text", "th": OK_WARN_BAD})],
        sort=("Cluster", False)), 24))

    d.row("Storage & growth")
    d.line(8,
           (ts("Database size", "Size of each database (pg_database_size) per cluster.",
               [T(bycl(E('max by (namespace, pod, datname) (cnpg_pg_database_size_bytes{<<C>>, datname!~"template.*"})', C=C), "max", ", datname"), "{{cluster}}/{{datname}}")],
               unit="bytes"), 12),
           (ts("Growth per hour", "Change in total database size per hour per cluster (derivative over the last hour).",
               [T(bycl(E('sum by (namespace, pod) (deriv(cnpg_pg_database_size_bytes{<<C>>}[1h])) * 3600', C=C)), "{{cluster}}")],
               unit="bytes"), 6),
           (bargauge("Volume used (estimate)", "Database + WAL size as a share of the instance's PVC request. Node-local "
                     "volumes (adhar-local) report no kubelet volume statistics, so this is computed from PostgreSQL's own "
                     "sizes and slightly under-counts (no filesystem overhead).",
                     [T(E('100 * (sum by (namespace, pod) (cnpg_pg_database_size_bytes{<<C>>}) + max by (namespace, pod) (cnpg_collector_pg_wal{<<C>>, value="size"})) '
                          '/ on (namespace, pod) label_replace(max by (namespace, persistentvolumeclaim) (kube_persistentvolumeclaim_resource_requests_storage_bytes{<<KSM>>, namespace=~"$namespace", persistentvolumeclaim=~"$instance"}), "pod", "$1", "persistentvolumeclaim", "(.+)")', C=C, KSM=KSM), "{{pod}}")]), 6))
    d.line(7,
           (ts("Transaction ID age (wraparound risk)", "age(datfrozenxid) per database. Autovacuum freezes rows to keep "
               "this low; PostgreSQL forces a shutdown at ~2 billion. Steady growth means vacuum cannot keep up "
               "(often blocked by a long transaction) — this is the autovacuum health signal.",
               [T(bycl(E('max by (namespace, pod, datname) (cnpg_pg_database_xid_age{<<C>>})', C=C), "max", ", datname"), "{{cluster}}/{{datname}}")],
               th=steps((GREEN, None), (AMBER, 200000000), (RED, 1000000000))), 12),
           (ts("Multixact ID age", "age(datminmxid) per database — the multixact equivalent of XID wraparound.",
               [T(bycl(E('max by (namespace, pod, datname) (cnpg_pg_database_mxid_age{<<C>>})', C=C), "max", ", datname"), "{{cluster}}/{{datname}}")]), 12))

    d.row("PostgreSQL internals")
    d.line(8,
           (ts("Cache hit ratio", "Share of block reads served from shared_buffers per cluster. OLTP workloads should "
               "stay above ~99%; lower means the working set does not fit in memory.",
               [T(bycl(E('100 * sum by (namespace, pod) (rate(cnpg_pg_stat_database_blks_hit{<<C>>}[$__rate_interval])) / '
                         '(sum by (namespace, pod) (rate(cnpg_pg_stat_database_blks_hit{<<C>>}[$__rate_interval]) + '
                         'rate(cnpg_pg_stat_database_blks_read{<<C>>}[$__rate_interval])) > 0)', C=C), "max"), "{{cluster}}")],
               unit="percent", min=None, soft_max=100, th=steps((RED, None), (AMBER, 90), (GREEN, 99))), 8),
           (ts("Checkpoints", "Timed vs requested checkpoints per minute. Requested checkpoints mean max_wal_size is "
               "reached before checkpoint_timeout — I/O spikes.",
               [T(bycl(E('sum by (namespace, pod) (increase(cnpg_pg_stat_checkpointer_checkpoints_timed{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} timed"),
                T(bycl(E('sum by (namespace, pod) (increase(cnpg_pg_stat_checkpointer_checkpoints_req{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} requested")],
               draw="bars", decimals=0), 8),
           (ts("Checkpoint write / sync time", "Time spent writing and fsyncing checkpoint buffers per second.",
               [T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_checkpointer_write_time{<<C>>}[$__rate_interval])) / 1000', C=C)), "{{cluster}} write"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_checkpointer_sync_time{<<C>>}[$__rate_interval])) / 1000', C=C)), "{{cluster}} sync")],
               unit="percentunit"), 8))
    d.line(8,
           (ts("Buffers written", "Buffers written by the checkpointer and the background writer, and new buffers "
               "allocated, per second.",
               [T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_checkpointer_buffers_written{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} checkpointer"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_bgwriter_buffers_clean{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} bgwriter"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_bgwriter_buffers_alloc{<<C>>}[$__rate_interval]))', C=C)), "{{cluster}} allocated")]), 12),
           (ts("Block I/O time", "Time backends spent reading and writing data blocks (requires track_io_timing).",
               [T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_blk_read_time{<<C>>}[$__rate_interval])) / 1000', C=C)), "{{cluster}} read"),
                T(bycl(E('sum by (namespace, pod) (rate(cnpg_pg_stat_database_blk_write_time{<<C>>}[$__rate_interval])) / 1000', C=C)), "{{cluster}} write")],
               unit="percentunit"), 12))

    d.row("Pod resources")
    d.line(8,
           (ts("CPU vs request", "PostgreSQL container CPU per instance with its request as a dashed line. Throttled "
               "CPU shows up here as a flat top at the limit.",
               [T(E('sum by (pod) (rate(container_cpu_usage_seconds_total{<<C>>, container="postgres"}[$__rate_interval]))', C=C), "{{pod}} used"),
                T(E('max by (pod) (kube_pod_container_resource_requests{<<KSM>>, <<C>>, container="postgres", resource="cpu"})', C=C, KSM=KSM), "{{pod}} request")],
               unit="cores", overrides=[ov_dashed(".*request")]), 8),
           (ts("Memory vs request / limit", "Working set per instance with request and limit dashed. Hitting the limit is "
               "an OOMKill of the database.",
               [T(E('sum by (pod) (container_memory_working_set_bytes{<<C>>, container="postgres"})', C=C), "{{pod}} working set"),
                T(E('max by (pod) (kube_pod_container_resource_requests{<<KSM>>, <<C>>, container="postgres", resource="memory"})', C=C, KSM=KSM), "{{pod}} request"),
                T(E('max by (pod) (kube_pod_container_resource_limits{<<KSM>>, <<C>>, container="postgres", resource="memory"})', C=C, KSM=KSM), "{{pod}} limit")],
               unit="bytes", overrides=[ov_dashed(".*(request|limit)")]), 8),
           (ts("Restarts", "Container restarts of the postgres container in each interval.",
               [T(E('sum by (pod) (increase(kube_pod_container_status_restarts_total{<<KSM>>, <<C>>, container="postgres"}[$__rate_interval]))', C=C, KSM=KSM), "{{pod}}")],
               draw="bars", decimals=0), 8))
    d.line(9, (logs("PostgreSQL errors (Loki)", "ERROR / FATAL / PANIC lines from the selected instances' postgres "
                    "containers.", '{namespace=~"$namespace", pod=~"$instance", container="postgres"} |~ "(?i)\\"error_severity\\":\\"(ERROR|FATAL|PANIC)|level\\":\\"error"'), 24))
    return d


# =============================================================================================
# Valkey / Redis (every redis_exporter target)
# =============================================================================================

def valkey():
    S = 'namespace=~"$namespace", job=~"$job", pod=~"$pod"'
    KSM = 'job="kube-state-metrics"'
    P = 'namespace=~"$namespace", pod=~"$pod"'
    d = Dash("adhar-valkey", "Valkey / Redis",
             "Every Valkey / Redis instance scraped through redis_exporter: the platform's shared Valkey "
             "(adhar-cache, valkey-operator) and each application's cache in the environment namespaces. "
             "Health, throughput and latency per command, cache efficiency, memory, clients, replication and persistence.",
             DATA_TAGS + ["valkey", "redis"],
             [ds_var(),
              qvar("namespace", "Namespace", "label_values(redis_up, namespace)"),
              qvar("job", "Instance group", 'label_values(redis_up{namespace=~"$namespace"}, job)'),
              qvar("pod", "Pod", 'label_values(redis_up{namespace=~"$namespace", job=~"$job"}, pod)')],
             links=dash_links(("Kubernetes overview", OVERVIEW_UID)))

    d.row("Health at a glance")
    d.line(4,
           (stat("Instances down", "redis_exporter targets that cannot reach their server (redis_up == 0).",
                 [E('count(redis_up{<<S>>} == 0) or vector(0)', S=S)], th=OK_BAD), 3),
           (stat("Masters / replicas", "Instances by replication role.",
                 [T(E('count(redis_instance_info{<<S>>, role="master"}) or vector(0)', S=S), "masters"),
                  T(E('count(redis_instance_info{<<S>>, role=~"slave|replica"}) or vector(0)', S=S), "replicas")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("Commands / s", "Commands processed per second across the selection.",
                 [E('sum(rate(redis_commands_processed_total{<<S>>}[$__rate_interval]))', S=S)], unit="ops",
                 color_mode="value", graph="area", decimals=1), 3),
           (stat("Hit ratio", "Keyspace hits / (hits + misses) over the time range. Low for a cache means it is not "
                 "holding what clients ask for (too small, TTLs too short, or keys never reused).",
                 [E('100 * sum(increase(redis_keyspace_hits_total{<<S>>}[$__range])) / (sum(increase(redis_keyspace_hits_total{<<S>>}[$__range])) + sum(increase(redis_keyspace_misses_total{<<S>>}[$__range])) > 0)', S=S)],
                 unit="percent", th=steps((RED, None), (AMBER, 50), (GREEN, 80)), decimals=1,
                 no_value="no lookups"), 3),
           (stat("Connected clients", "Client connections across the selection.",
                 [E('sum(redis_connected_clients{<<S>>})', S=S)], color_mode="value", graph="area"), 3),
           (stat("Blocked clients", "Clients blocked in BLPOP/BRPOP/WAIT etc. Persistently high with nothing to pop "
                 "usually means a stuck consumer.",
                 [E('sum(redis_blocked_clients{<<S>>})', S=S)], th=steps((GREEN, None), (AMBER, 10), (RED, 50))), 3),
           (stat("Memory used", "Memory allocated by the servers (used_memory).",
                 [E('sum(redis_memory_used_bytes{<<S>>})', S=S)], unit="bytes", color_mode="value", graph="area"), 3),
           (stat("Evictions (range)", "Keys evicted because maxmemory was reached, over the time range. With the "
                 "'noeviction' policy writes fail instead — watch the rejected-calls panel.",
                 [E('sum(increase(redis_evicted_keys_total{<<S>>}[$__range])) or vector(0)', S=S)],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 1000)), decimals=0), 3))
    d.line(8, (table(
        "Instances", "One row per server. Memory % is used_memory against maxmemory, or against the container "
        "memory limit when maxmemory is unset (0). 'Last RDB save' is the outcome of the last background save.",
        [E('max by (namespace, job, pod, role, redis_version, maxmemory_policy) (redis_instance_info{<<S>>})', S=S),
         E('max by (namespace, job, pod) (redis_up{<<S>>})', S=S),
         E('max by (namespace, job, pod) (redis_uptime_in_seconds{<<S>>})', S=S),
         E('max by (namespace, job, pod) (redis_connected_clients{<<S>>})', S=S),
         E('sum by (namespace, job, pod) (rate(redis_commands_processed_total{<<S>>}[5m]))', S=S),
         E('max by (namespace, job, pod) (redis_memory_used_bytes{<<S>>})', S=S),
         E('100 * max by (namespace, job, pod) (redis_memory_used_bytes{<<S>>}) / on (namespace, pod) group_left () '
           '(max by (namespace, pod) (redis_memory_max_bytes{<<S>>} > 0) or sum by (namespace, pod) (kube_pod_container_resource_limits{<<KSM>>, <<P>>, resource="memory", container!="metrics"}))', S=S, KSM=KSM, P=P),
         E('max by (namespace, job, pod) (redis_mem_fragmentation_ratio{<<S>>})', S=S),
         E('sum by (namespace, job, pod) (redis_db_keys{<<S>>})', S=S),
         E('100 * sum by (namespace, job, pod) (rate(redis_keyspace_hits_total{<<S>>}[1h])) / (sum by (namespace, job, pod) (rate(redis_keyspace_hits_total{<<S>>}[1h]) + rate(redis_keyspace_misses_total{<<S>>}[1h])) > 0)', S=S),
         E('max by (namespace, job, pod) (redis_rdb_last_bgsave_status{<<S>>})', S=S),
         E('max by (namespace, job, pod) (redis_aof_enabled{<<S>>})', S=S)],
        [("namespace", "Namespace", None, None), ("job", "Group", None, None),
         ("pod", "Pod", None, {"links": pod_link()}),
         ("role", "Role", None, None), ("redis_version", "Version", None, None),
         ("maxmemory_policy", "Eviction policy", None, None),
         ("Value #B", "Up", None, {"mappings": vmap((0, "DOWN", RED), (1, "UP", GREEN)), "cell": "color-background"}),
         ("Value #C", "Uptime", "s", None),
         ("Value #D", "Clients", "short", None),
         ("Value #E", "Ops/s", "ops", {"decimals": 1}),
         ("Value #F", "Memory", "bytes", None),
         ("Value #G", "Memory %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #H", "Frag ratio", "short", {"decimals": 2, "cell": "color-text", "th": steps((GREEN, None), (AMBER, 1.5), (RED, 3))}),
         ("Value #I", "Keys", "short", None),
         ("Value #J", "Hit % (1h)", "percent", {"decimals": 1, "cell": "color-text", "th": steps((RED, None), (AMBER, 50), (GREEN, 80))}),
         ("Value #K", "Last RDB save", None, {"mappings": vmap((0, "FAILED", RED), (1, "ok", GREEN)), "cell": "color-text"}),
         ("Value #L", "AOF", None, {"mappings": vmap((0, "off", GREY), (1, "on", GREEN))})],
        sort=("Namespace", False), hide=("Value #A",)), 24))

    d.row("Throughput & latency")
    d.line(8,
           (ts("Commands / s by instance", "Commands processed per second per server.",
               [T(E('sum by (namespace, pod) (rate(redis_commands_processed_total{<<S>>}[$__rate_interval]))', S=S), "{{namespace}}/{{pod}}")],
               unit="ops"), 8),
           (ts("Top commands / s", "The ten busiest commands across the selection.",
               [T(E('topk(10, sum by (cmd) (rate(redis_commands_total{<<S>>}[$__rate_interval])))', S=S), "{{cmd}}")],
               unit="ops", stack=True), 8),
           (ts("Command latency p99", "99th percentile server-side latency per command (from Valkey's per-command "
               "latency histograms). Server time only — network and client queueing are not included.",
               [T(E('histogram_quantile(0.99, sum by (cmd, le) (rate(redis_commands_latencies_usec_bucket{<<S>>}[$__rate_interval]))) / 1e6', S=S), "{{cmd}}")],
               unit="s"), 8))
    d.line(8,
           (heatmap("Command latency distribution", "All commands' server-side latency as a heatmap; a band moving "
                    "up is a slowdown (big keys, KEYS/SCAN, fork for persistence).",
                    E('sum by (le) (increase(redis_commands_latencies_usec_bucket{<<S>>}[$__rate_interval]))', S=S), unit="µs"), 12),
           (ts("Errors & rejected calls", "Failed calls, rejected calls (e.g. OOM under noeviction, ACL denials) and "
               "error replies per second.",
               [T(E('sum by (namespace, pod) (rate(redis_commands_failed_calls_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} failed"),
                T(E('sum by (namespace, pod) (rate(redis_commands_rejected_calls_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} rejected"),
                T(E('sum by (namespace, pod) (rate(redis_total_error_replies{<<S>>}[$__rate_interval]))', S=S), "{{pod}} error replies")],
               unit="ops", th=steps((GREEN, None), (RED, 1))), 12))

    d.row("Keyspace & cache efficiency")
    d.line(8,
           (ts("Hit ratio", "Keyspace hit ratio per server.",
               [T(E('100 * sum by (namespace, pod) (rate(redis_keyspace_hits_total{<<S>>}[$__rate_interval])) / (sum by (namespace, pod) (rate(redis_keyspace_hits_total{<<S>>}[$__rate_interval]) + rate(redis_keyspace_misses_total{<<S>>}[$__rate_interval])) > 0)', S=S), "{{namespace}}/{{pod}}")],
               unit="percent", max=100, min=0), 8),
           (ts("Hits vs misses / s", "Lookups that found (hits) or did not find (misses, below the axis) a key.",
               [T(E('sum by (pod) (rate(redis_keyspace_hits_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} hits"),
                T(E('sum by (pod) (rate(redis_keyspace_misses_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} misses")],
               unit="ops", overrides=[ov_negative(".*misses")]), 8),
           (ts("Expired & evicted keys / s", "Keys removed by TTL expiry and by eviction under memory pressure.",
               [T(E('sum by (pod) (rate(redis_expired_keys_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} expired"),
                T(E('sum by (pod) (rate(redis_evicted_keys_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} evicted")],
               unit="ops", overrides=[ov_color(".*evicted", RED)]), 8))
    d.line(8,
           (ts("Keys per database", "Number of keys per logical database (db0, db1, ...) per server, and how many of "
               "them carry a TTL (dashed).",
               [T(E('sum by (pod, db) (redis_db_keys{<<S>>})', S=S), "{{pod}} {{db}}"),
                T(E('sum by (pod, db) (redis_db_keys_expiring{<<S>>})', S=S), "{{pod}} {{db}} with TTL")],
               overrides=[ov_dashed(".*with TTL")]), 12),
           (ts("Pub/Sub & blocking", "Pub/Sub channels and patterns, and keys clients are blocked on.",
               [T(E('sum by (pod) (redis_pubsub_channels{<<S>>})', S=S), "{{pod}} channels"),
                T(E('sum by (pod) (redis_pubsub_patterns{<<S>>})', S=S), "{{pod}} patterns"),
                T(E('sum by (pod) (redis_total_blocking_keys{<<S>>})', S=S), "{{pod}} blocking keys")]), 12))

    d.row("Memory")
    d.line(8,
           (ts("Memory used vs limits", "used_memory, RSS and peak per server, with maxmemory (dashed, when set) and "
               "the container memory limit (dashed red). Under 'noeviction', reaching maxmemory makes writes fail.",
               [T(E('max by (pod) (redis_memory_used_bytes{<<S>>})', S=S), "{{pod}} used"),
                T(E('max by (pod) (redis_memory_used_rss_bytes{<<S>>})', S=S), "{{pod}} rss"),
                T(E('max by (pod) (redis_memory_max_bytes{<<S>>} > 0)', S=S), "{{pod}} maxmemory"),
                T(E('sum by (pod) (kube_pod_container_resource_limits{<<KSM>>, <<P>>, resource="memory", container!="metrics"})', P=P, KSM=KSM), "{{pod}} container limit")],
               unit="bytes", overrides=[ov_dashed(".*maxmemory"), ov_dashed(".*container limit", RED)]), 12),
           (ts("Fragmentation ratio", "RSS / used_memory. Above 1.5 memory is wasted to fragmentation (active defrag "
               "helps); below 1 the server is swapping.",
               [T(E('max by (pod) (redis_mem_fragmentation_ratio{<<S>>})', S=S), "{{pod}}")],
               decimals=2, th=steps((GREEN, None), (AMBER, 1.5), (RED, 3))), 6),
           (ts("Memory breakdown", "Dataset vs overhead (client buffers, replication backlog, internal structures).",
               [T(E('sum(redis_memory_used_dataset_bytes{<<S>>})', S=S), "dataset"),
                T(E('sum(redis_memory_used_overhead_bytes{<<S>>})', S=S), "overhead"),
                T(E('sum(redis_mem_clients_normal{<<S>>})', S=S), "client buffers")],
               unit="bytes", stack=True), 6))

    d.row("Clients & network")
    d.line(8,
           (ts("Clients", "Connected and blocked clients per server.",
               [T(E('sum by (pod) (redis_connected_clients{<<S>>})', S=S), "{{pod}} connected"),
                T(E('sum by (pod) (redis_blocked_clients{<<S>>})', S=S), "{{pod}} blocked")]), 8),
           (ts("Connections opened / rejected", "New connections per second, and connections rejected because maxclients "
               "was reached (below the axis).",
               [T(E('sum by (pod) (rate(redis_connections_received_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} received"),
                T(E('sum by (pod) (rate(redis_rejected_connections_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} rejected")],
               unit="cps", overrides=[ov_negative(".*rejected"), ov_color(".*rejected", RED)]), 8),
           (ts("Network I/O", "Bytes received (above) and sent (below) per server, including replication.",
               [T(E('sum by (pod) (rate(redis_net_input_bytes_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} in"),
                T(E('sum by (pod) (rate(redis_net_output_bytes_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} out")],
               unit="Bps", overrides=[ov_negative(".* out")]), 8))

    d.row("Replication & persistence")
    d.line(8,
           (ts("Connected replicas", "Replicas attached to each master. A drop means a replica disconnected and will "
               "need a (partial or full) resync.",
               [T(E('max by (pod) (redis_connected_slaves{<<S>>})', S=S), "{{pod}}")], decimals=0), 6),
           (ts("Replication throughput", "Rate the master replication offset advances (bytes of write traffic shipped "
               "to replicas) and full resyncs (bars).",
               [T(E('max by (pod) (rate(redis_master_repl_offset{<<S>>}[$__rate_interval]))', S=S), "{{pod}} offset rate"),
                T(E('sum by (pod) (increase(redis_replica_resyncs_full{<<S>>}[$__rate_interval]))', S=S), "{{pod}} full resyncs")],
               unit="Bps"), 6),
           (ts("Changes since last save & save age", "Writes not yet in an RDB snapshot (data at risk on crash without "
               "AOF) and seconds since the last successful save.",
               [T(E('max by (pod) (redis_rdb_changes_since_last_save{<<S>>})', S=S), "{{pod}} unsaved changes"),
                T(E('max by (pod) (time() - redis_rdb_last_save_timestamp_seconds{<<S>>})', S=S), "{{pod}} since save (s)")],
               overrides=[{"matcher": {"id": "byRegexp", "options": ".*since save.*"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 6),
           (ts("Fork & persistence status", "Duration of the last fork (blocks the server), and whether the last RDB "
               "save / AOF write succeeded (1 = ok).",
               [T(E('max by (pod) (redis_latest_fork_seconds{<<S>>})', S=S), "{{pod}} last fork (s)"),
                T(E('max by (pod) (redis_rdb_last_bgsave_status{<<S>>})', S=S), "{{pod}} rdb ok"),
                T(E('max by (pod) (redis_aof_last_write_status{<<S>>})', S=S), "{{pod}} aof ok")]), 6))

    d.row("Pod resources")
    d.line(8,
           (ts("CPU (server main thread + children)", "Server CPU per second split into main thread and background "
               "children (RDB/AOF rewrite forks).",
               [T(E('sum by (pod) (rate(redis_cpu_user_seconds_total{<<S>>}[$__rate_interval]) + rate(redis_cpu_sys_seconds_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} server"),
                T(E('sum by (pod) (rate(redis_cpu_user_children_seconds_total{<<S>>}[$__rate_interval]) + rate(redis_cpu_sys_children_seconds_total{<<S>>}[$__rate_interval]))', S=S), "{{pod}} children")],
               unit="cores"), 8),
           (ts("Container memory vs request", "Working set of the server container (excluding the exporter sidecar) "
               "with its memory request dashed.",
               [T(E('sum by (pod) (container_memory_working_set_bytes{<<P>>, container!="", container!="metrics", container!="POD"})', P=P), "{{pod}} working set"),
                T(E('sum by (pod) (kube_pod_container_resource_requests{<<KSM>>, <<P>>, resource="memory", container!="metrics"})', P=P, KSM=KSM), "{{pod}} request")],
               unit="bytes", overrides=[ov_dashed(".*request")]), 8),
           (ts("Restarts", "Container restarts per interval.",
               [T(E('sum by (pod) (increase(kube_pod_container_status_restarts_total{<<KSM>>, <<P>>}[$__rate_interval]))', P=P, KSM=KSM), "{{pod}}")],
               draw="bars", decimals=0), 8))
    d.line(8, (logs("Server logs (Loki)", "Log lines from the selected pods' server containers (warnings and "
                    "errors first: '#' marks Valkey warnings).", '{namespace=~"$namespace", pod=~"$pod", container!="metrics"} |~ "(?i)warn|error|#|oom|fail"'), 24))
    return d


# =============================================================================================
# Kafka (Strimzi) — brokers
# =============================================================================================

NOTOPIC = ', topic=""'
HEAP = ', area="heap"'
DOP_PRODUCE = ', delayedOperation="Produce"'
DOP_FETCH = ', delayedOperation="Fetch"'
CLIENT_REPLICA = ', clientId="Replica"'
KB_SEL = 'namespace=~"$namespace", strimzi_io_cluster=~"$cluster", pod=~"$broker"'
# Brokers are scraped by the shared strimzi PodMonitor AND (for composed Kafkas) a
# per-application PodMonitor, so every broker series exists twice with different
# `job`s. Every query collapses that with max by (... pod ...) before summing.
KB_KEY = "namespace, strimzi_io_cluster, pod"


def kafka_cluster():
    B = KB_SEL
    KSM = 'job="kube-state-metrics"'
    P = 'namespace=~"$namespace", pod=~"$broker"'

    def g(metric, extra=""):
        """De-duplicated per-broker gauge."""
        return f'max by ({KB_KEY}) ({metric}{{{B}{extra}}})'

    def r(metric, extra="", w="$__rate_interval"):
        return f'max by ({KB_KEY}) (rate({metric}{{{B}{extra}}}[{w}]))'

    d = Dash("adhar-kafka-cluster", "Kafka / Brokers (Strimzi)",
             "Strimzi Kafka (KRaft) brokers: the platform's shared adhar-kafka and every composed application "
             "cluster. Broker and controller health, partitions and replication, throughput, request latency, "
             "storage, JVM and pod resources. Topic-level production and consumer lag are on 'Kafka / Topics & "
             "Consumer Lag'.",
             DATA_TAGS + ["kafka", "strimzi"],
             [ds_var(),
              qvar("namespace", "Namespace", "label_values(kafka_server_replicamanager_partitioncount, namespace)"),
              qvar("cluster", "Kafka cluster", 'label_values(kafka_server_replicamanager_partitioncount{namespace=~"$namespace"}, strimzi_io_cluster)'),
              qvar("broker", "Broker", 'label_values(kafka_server_replicamanager_partitioncount{namespace=~"$namespace", strimzi_io_cluster=~"$cluster"}, pod)')],
             links=dash_links(("Kubernetes overview", OVERVIEW_UID)))

    d.row("Health at a glance")
    d.line(4,
           (stat("Brokers running", "Brokers whose BrokerState is RUNNING (3), out of all brokers reporting metrics.",
                 [T(f'count({g("kafka_server_kafkaserver_brokerstate")} == 3) or vector(0)', "running"),
                  T(f'count({g("kafka_server_kafkaserver_brokerstate")})', "total")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("Clusters without controller", "Kafka clusters where no broker reports itself the active KRaft "
                 "controller. Must be 0 — without a controller no leader elections or topic changes happen.",
                 [f'count(sum by (namespace, strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_activecontrollercount")}) == 0) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Offline partitions", "Partitions with no live leader — unavailable for produce and consume.",
                 [f'sum(max by (namespace, strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_offlinepartitionscount")})) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Under-replicated", "Partitions whose ISR is smaller than the replica set. A broker is down, slow or "
                 "out of disk.", [f'sum({g("kafka_server_replicamanager_underreplicatedpartitions")}) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Under min ISR", "Partitions below min.insync.replicas: producers with acks=all are being refused.",
                 [f'sum({g("kafka_server_replicamanager_underminisrpartitioncount")}) or vector(0)'], th=OK_BAD), 3),
           (stat("Fenced brokers", "Brokers the controller has fenced (missed heartbeats).",
                 [f'sum(max by (namespace, strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_fencedbrokercount")})) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Topics / partitions", "Topics and partitions known to the controller (all clusters in the selection).",
                 [T(f'sum(max by (namespace, strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_globaltopiccount")}))', "topics"),
                  T(f'sum(max by (namespace, strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_globalpartitioncount")}))', "partitions")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("Messages in / s", "Messages produced per second across the selected brokers.",
                 [f'sum({r("kafka_server_brokertopicmetrics_messagesin_total", NOTOPIC)})'],
                 unit="short", color_mode="value", graph="area", decimals=1), 3))
    state_map = vmap((0, "NotRunning", RED), (1, "Starting", AMBER), (2, "RecoveringFromUncleanShutdown", AMBER),
                     (3, "Running", GREEN), (6, "PendingControlledShutdown", AMBER), (7, "ShuttingDown", AMBER))
    d.line(7, (table(
        "Brokers", "One row per broker. 'Controller' is the active KRaft controller. Heap % is used / max heap.",
        [g("kafka_server_kafkaserver_brokerstate"),
         g("kafka_controller_kafkacontroller_activecontrollercount"),
         g("kafka_server_replicamanager_partitioncount"),
         g("kafka_server_replicamanager_leadercount"),
         g("kafka_server_replicamanager_underreplicatedpartitions"),
         g("kafka_log_logmanager_offlinelogdirectorycount"),
         r("kafka_server_brokertopicmetrics_messagesin_total", NOTOPIC, "5m"),
         r("kafka_server_brokertopicmetrics_bytesin_total", NOTOPIC, "5m"),
         r("kafka_server_brokertopicmetrics_bytesout_total", NOTOPIC, "5m"),
         f'100 * {g("jvm_memory_used_bytes", HEAP)} / {g("jvm_memory_max_bytes", HEAP)}',
         E('sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{<<P>>, container="kafka"}[5m])) * on (namespace, pod) group_left (strimzi_io_cluster) (0 * max by (namespace, pod, strimzi_io_cluster) (kafka_server_kafkaserver_brokerstate{<<B>>}) + 1)', P=P, B=B),
         E('max by (namespace, pod) (kube_pod_container_status_restarts_total{<<KSM>>, <<P>>, container="kafka"}) * on (namespace, pod) group_left (strimzi_io_cluster) (0 * max by (namespace, pod, strimzi_io_cluster) (kafka_server_kafkaserver_brokerstate{<<B>>}) + 1)', P=P, B=B, KSM=KSM)],
        [("namespace", "Namespace", None, None), ("strimzi_io_cluster", "Cluster", None, None),
         ("pod", "Broker", None, {"links": pod_link()}),
         ("Value #A", "State", None, {"mappings": state_map, "cell": "color-background"}),
         ("Value #B", "Controller", None, {"mappings": vmap((0, "-", GREY), (1, "ACTIVE", BLUE))}),
         ("Value #C", "Partitions", "short", None), ("Value #D", "Leaders", "short", None),
         ("Value #E", "URP", "short", {"cell": "color-background", "th": OK_BAD}),
         ("Value #F", "Offline log dirs", "short", {"cell": "color-text", "th": OK_BAD}),
         ("Value #G", "Msgs in/s", "short", {"decimals": 1}),
         ("Value #H", "Bytes in", "Bps", None), ("Value #I", "Bytes out", "Bps", None),
         ("Value #J", "Heap %", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #K", "CPU", "cores", {"decimals": 3}),
         ("Value #L", "Restarts", "short", {"cell": "color-text", "th": OK_WARN_BAD})],
        sort=("Cluster", False)), 24))

    d.row("Throughput")
    d.line(8,
           (ts("Bytes in / out per broker", "Client produce traffic in (above) and fetch traffic out (below) per "
               "broker, excluding replication.",
               [T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_bytesin_total", NOTOPIC)})', "{{pod}} in"),
                T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_bytesout_total", NOTOPIC)})', "{{pod}} out")],
               unit="Bps", overrides=[ov_negative(".* out")]), 12),
           (ts("Messages in / s per broker", "Messages appended per second per broker.",
               [T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_messagesin_total", NOTOPIC)})', "{{pod}}")]), 12))
    d.line(8,
           (ts("Bytes in by topic (top 10)", "Per-topic produce bytes from the broker's BrokerTopicMetrics. Needs the "
               "upstream Strimzi JMX rules (kafka-metrics ConfigMap) which label by topic; clusters using the compact "
               "rule set only expose the broker total, so use message rates on the Topics dashboard there.",
               [T(f'topk(10, sum by (topic) (max by ({KB_KEY}, topic) (rate(kafka_server_brokertopicmetrics_bytesin_total{{{B}, topic!=""}}[$__rate_interval]))))', "{{topic}}")],
               unit="Bps", stack=True), 12),
           (ts("Replication traffic", "Bytes per second followers fetch from leaders (in) and leaders serve to followers "
               "(out). Zero on single-broker clusters.",
               [T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_replicationbytesin_total", NOTOPIC)})', "{{pod}} in"),
                T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_replicationbytesout_total", NOTOPIC)})', "{{pod}} out")],
               unit="Bps", overrides=[ov_negative(".* out")]), 12))

    d.row("Requests")
    d.line(8,
           (ts("Produce / fetch requests", "Produce and fetch requests per second per broker.",
               [T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_totalproducerequests_total", NOTOPIC)})', "{{pod}} produce"),
                T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_totalfetchrequests_total", NOTOPIC)})', "{{pod}} fetch")],
               unit="reqps"), 8),
           (ts("Failed produce / fetch", "Requests that failed on the broker (not authorised, corrupt, not leader...).",
               [T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_failedproducerequests_total", NOTOPIC)})', "{{pod}} produce"),
                T(f'sum by (pod) ({r("kafka_server_brokertopicmetrics_failedfetchrequests_total", NOTOPIC)})', "{{pod}} fetch")],
               unit="reqps", th=steps((GREEN, None), (RED, 0.1))), 8),
           (ts("Request latency p99", "99th percentile total request time for Produce and consumer/follower Fetch "
               "(RequestMetrics, upstream Strimzi rules only).",
               [T(f'max by (pod, request) (kafka_network_requestmetrics_totaltimems{{{B}, quantile="0.99", request=~"Produce|FetchConsumer|FetchFollower"}})', "{{pod}} {{request}}")],
               unit="ms"), 8))
    d.line(8,
           (ts("Network processor idle", "Average idle share of the network threads. Below ~30% the broker cannot "
               "accept requests fast enough.",
               [T(f'100 * max by (pod) ({g("kafka_network_socketserver_networkprocessoravgidlepercent")})', "{{pod}}")],
               unit="percent", min=0, max=100, th=steps((RED, None), (AMBER, 30), (GREEN, 50))), 8),
           (ts("Request / response queues", "Requests waiting for an I/O thread and responses waiting to be sent.",
               [T(f'max by (pod) ({g("kafka_network_requestchannel_requestqueuesize")})', "{{pod}} requests"),
                T(f'max by (pod) ({g("kafka_network_requestchannel_responsequeuesize")})', "{{pod}} responses")]), 8),
           (ts("Purgatory (delayed operations)", "Produce (acks=all) and fetch (long-poll) requests parked waiting "
               "for replication or data.",
               [T(f'max by (pod) ({g("kafka_server_delayedoperationpurgatory_purgatorysize_delayedoperation_produce")} or {g("kafka_server_delayedoperationpurgatory_purgatorysize", DOP_PRODUCE)})', "{{pod}} produce"),
                T(f'max by (pod) ({g("kafka_server_delayedoperationpurgatory_purgatorysize_delayedoperation_fetch")} or {g("kafka_server_delayedoperationpurgatory_purgatorysize", DOP_FETCH)})', "{{pod}} fetch")]), 8))

    d.row("Replication & partitions")
    d.line(8,
           (ts("Under-replicated & under-min-ISR", "Per broker. Any non-zero value means reduced durability.",
               [T(f'max by (pod) ({g("kafka_server_replicamanager_underreplicatedpartitions")})', "{{pod}} under-replicated"),
                T(f'max by (pod) ({g("kafka_server_replicamanager_underminisrpartitioncount")})', "{{pod}} under min ISR"),
                T(f'max by (pod) ({g("kafka_server_replicamanager_atminisrpartitioncount")})', "{{pod}} at min ISR")],
               decimals=0, th=steps((GREEN, None), (RED, 1))), 8),
           (ts("ISR shrinks / expands", "Replicas leaving (shrink) and re-joining (expand) the in-sync set per minute. "
               "Flapping means a slow or overloaded follower.",
               [T(f'sum by (pod) (max by ({KB_KEY}) (increase(kafka_server_replicamanager_isrshrinks_total{{{B}}}[$__rate_interval])))', "{{pod}} shrinks"),
                T(f'sum by (pod) (max by ({KB_KEY}) (increase(kafka_server_replicamanager_isrexpands_total{{{B}}}[$__rate_interval])))', "{{pod}} expands")],
               draw="bars", decimals=0, overrides=[ov_negative(".*shrinks"), ov_color(".*shrinks", RED)]), 8),
           (ts("Partitions & leaders per broker", "Partition replicas hosted and partitions led per broker. Uneven "
               "leader counts mean unbalanced load.",
               [T(f'max by (pod) ({g("kafka_server_replicamanager_partitioncount")})', "{{pod}} partitions"),
                T(f'max by (pod) ({g("kafka_server_replicamanager_leadercount")})', "{{pod}} leaders")], decimals=0), 8))
    d.line(8,
           (ts("Replica fetcher max lag", "Largest message lag of any follower partition on this broker.",
               [T(f'max by (pod) ({g("kafka_server_replicafetchermanager_maxlag_clientid_replica")} or {g("kafka_server_replicafetchermanager_maxlag", CLIENT_REPLICA)})', "{{pod}}")]), 8),
           (ts("KRaft controller", "Active/fenced brokers seen by the controller, metadata errors, and how far "
               "behind the last applied metadata record is.",
               [T(f'max by (strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_activebrokercount")})', "{{strimzi_io_cluster}} active brokers"),
                T(f'max by (strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_fencedbrokercount")})', "{{strimzi_io_cluster}} fenced"),
                T(f'max by (strimzi_io_cluster) ({g("kafka_controller_kafkacontroller_metadataerrorcount")})', "{{strimzi_io_cluster}} metadata errors")],
               decimals=0), 8),
           (ts("Metadata apply lag", "Milliseconds between a metadata record being committed and this node applying it.",
               [T(f'max by (pod) ({g("kafka_controller_kafkacontroller_lastappliedrecordlagms")})', "{{pod}}")], unit="ms"), 8))

    d.row("Storage")
    d.line(8,
           (ts("Log size by topic", "On-disk size of each topic (sum of its partitions, leader and follower replicas). "
               "Works with both the upstream Strimzi rules (topic label) and the compact composed rule set (topic "
               "encoded in the metric name).",
               [T(f'sum by (topic) (max by ({KB_KEY}, topic, partition) (kafka_log_log_size{{{B}}})) or '
                  f'sum by (topic) (label_replace(max by ({KB_KEY}, __name__) ({{__name__=~"kafka_log_log_size_topic_.+", {B}}}), '
                  f'"topic", "$1", "__name__", "kafka_log_log_size_topic_(.+)_partition_[0-9]+"))', "{{topic}}")],
               unit="bytes", stack=True), 12),
           (bargauge("Broker volume used (estimate)", "Sum of all log segments on each broker against its data PVC "
                     "request (data-0-<broker> for JBOD, data-<broker> otherwise). Node-local volumes report no kubelet statistics, so this is computed "
                     "from Kafka's own log sizes.",
                     [T(f'100 * sum by (namespace, pod) (max by ({KB_KEY}, topic, partition) (kafka_log_log_size{{{B}}}) or '
                        f'max by ({KB_KEY}, __name__) ({{__name__=~"kafka_log_log_size_topic_.+", {B}}})) '
                        f'/ on (namespace, pod) label_replace(max by (namespace, persistentvolumeclaim) (kube_persistentvolumeclaim_resource_requests_storage_bytes{{{KSM}, namespace=~"$namespace", persistentvolumeclaim=~"data-.+"}}), "pod", "$1", "persistentvolumeclaim", "data-(?:0-)?(.+)")',
                        "{{pod}}")]), 6),
           (ts("Offline / cordoned log dirs", "Log directories the broker has taken offline (disk failure) or cordoned.",
               [T(f'max by (pod) ({g("kafka_log_logmanager_offlinelogdirectorycount")})', "{{pod}} offline"),
                T(f'max by (pod) ({g("kafka_log_logmanager_cordonedlogdirectorycount")})', "{{pod}} cordoned")],
               decimals=0, th=steps((GREEN, None), (RED, 1))), 6))

    d.row("JVM & pod resources")
    d.line(8,
           (ts("Heap used vs max", "JVM heap per broker against -Xmx (dashed).",
               [T(f'max by (pod) ({g("jvm_memory_used_bytes", HEAP)})', "{{pod}} used"),
                T(f'max by (pod) ({g("jvm_memory_max_bytes", HEAP)})', "{{pod}} max")],
               unit="bytes", overrides=[ov_dashed(".* max")]), 8),
           (ts("GC time", "Share of wall-clock time spent in garbage collection, per collector. Above ~5% the broker "
               "is starved for heap.",
               [T(f'max by (pod, gc) (rate(jvm_gc_collection_seconds_sum{{{B}}}[$__rate_interval]))', "{{pod}} {{gc}}")],
               unit="percentunit", th=steps((GREEN, None), (AMBER, 0.05), (RED, 0.15))), 8),
           (ts("CPU vs request", "Broker container CPU with its request dashed.",
               [T(E('sum by (pod) (rate(container_cpu_usage_seconds_total{<<P>>, container="kafka"}[$__rate_interval]))', P=P), "{{pod}} used"),
                T(E('max by (pod) (kube_pod_container_resource_requests{<<KSM>>, <<P>>, container="kafka", resource="cpu"})', P=P, KSM=KSM), "{{pod}} request")],
               unit="cores", overrides=[ov_dashed(".*request")]), 8))
    d.line(9, (logs("Broker errors (Loki)", "ERROR / WARN lines from the selected brokers.",
                    '{namespace=~"$namespace", pod=~"$broker", container="kafka"} |~ "ERROR|WARN"'), 24))
    return d


# =============================================================================================
# Kafka — topics & consumer lag (Strimzi kafka-exporter)
# =============================================================================================

def kafka_topics():
    X = 'namespace=~"$namespace", strimzi_io_cluster=~"$cluster"'
    TX = X + ', topic=~"$topic"'
    G = TX + ', consumergroup=~"$consumergroup"'
    KEY = "namespace, strimzi_io_cluster"

    d = Dash("jwPKIsniz", "Kafka / Topics & Consumer Lag",
             "Topic- and consumer-group-level view from the Strimzi kafka-exporter: production rate, retained "
             "messages, partition health and consumer lag per group. Broker internals are on 'Kafka / Brokers'.",
             DATA_TAGS + ["kafka", "strimzi"],
             [ds_var(),
              qvar("namespace", "Namespace", "label_values(kafka_brokers, namespace)"),
              qvar("cluster", "Kafka cluster", 'label_values(kafka_brokers{namespace=~"$namespace"}, strimzi_io_cluster)'),
              qvar("topic", "Topic", 'label_values(kafka_topic_partitions{namespace=~"$namespace", strimzi_io_cluster=~"$cluster"}, topic)',
                   all_value=".*", regex="/^(?!__).*/"),
              qvar("consumergroup", "Consumer group", 'label_values(kafka_consumergroup_lag{namespace=~"$namespace", strimzi_io_cluster=~"$cluster"}, consumergroup)',
                   all_value=".*")],
             links=dash_links(("Kafka brokers", "adhar-kafka-cluster")))

    d.row("At a glance")
    d.line(4,
           (stat("Brokers", "Brokers the exporter can see, per cluster.",
                 [T(f'max by (strimzi_io_cluster) (kafka_brokers{{{X}}})', "{{strimzi_io_cluster}}")],
                 color_mode="value", text_mode="value_and_name"), 4),
           (stat("Topics", "Topics in the selection (internal __ topics excluded).",
                 [f'count(count by ({KEY}, topic) (kafka_topic_partitions{{{TX}, topic!~"__.*"}}))'], color_mode="value"), 3),
           (stat("Partitions", "Partitions of the selected topics.",
                 [f'sum(max by ({KEY}, topic) (kafka_topic_partitions{{{TX}, topic!~"__.*"}}))'], color_mode="value"), 3),
           (stat("Under-replicated", "Partitions of the selected topics whose ISR is smaller than the replica set.",
                 [f'sum(max by ({KEY}, topic, partition) (kafka_topic_partition_under_replicated_partition{{{TX}}})) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Non-preferred leaders", "Partitions not led by their preferred replica: load is unbalanced until a "
                 "preferred leader election runs.",
                 [f'count(max by ({KEY}, topic, partition) (kafka_topic_partition_leader_is_preferred{{{TX}}}) == 0) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1))), 3),
           (stat("Consumer groups", "Consumer groups with committed offsets on the selected topics.",
                 [f'count(count by ({KEY}, consumergroup) (kafka_consumergroup_lag{{{G}}})) or vector(0)'], color_mode="value"), 2),
           (stat("Total lag", "Messages produced but not yet consumed, summed over the selected groups and topics.",
                 [f'sum(max by ({KEY}, consumergroup, topic, partition) (kafka_consumergroup_lag{{{G}}})) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1000), (RED, 100000))), 3),
           (stat("Messages / s", "Messages produced per second to the selected topics (rate of the partition end offsets).",
                 [f'sum(max by ({KEY}, topic, partition) (rate(kafka_topic_partition_current_offset{{{TX}, topic!~"__.*"}}[$__rate_interval])))'],
                 color_mode="value", graph="area", decimals=1), 3))

    d.row("Production")
    d.line(8,
           (ts("Messages produced / s by topic", "Rate at which each topic's partition end offsets advance.",
               [T(f'sum by (topic) (max by ({KEY}, topic, partition) (rate(kafka_topic_partition_current_offset{{{TX}, topic!~"__.*"}}[$__rate_interval])))', "{{topic}}")],
               stack=True), 12),
           (ts("Retained messages by topic", "End offset minus oldest offset summed over partitions: what retention "
               "currently keeps on disk, in messages.",
               [T(f'sum by (topic) (max by ({KEY}, topic, partition) (kafka_topic_partition_current_offset{{{TX}, topic!~"__.*"}} - kafka_topic_partition_oldest_offset{{{TX}, topic!~"__.*"}}))', "{{topic}}")]), 12))

    d.row("Consumers")
    d.line(8,
           (ts("Lag by group / topic", "Messages each consumer group is behind the end of each topic. Rising lag "
               "means consumers are slower than producers (or stopped).",
               [T(f'sum by (consumergroup, topic) (max by ({KEY}, consumergroup, topic, partition) (kafka_consumergroup_lag{{{G}}}))', "{{consumergroup}} / {{topic}}")],
               th=steps((GREEN, None), (AMBER, 1000), (RED, 100000))), 12),
           (ts("Consumption rate", "Messages per second each group commits on each topic.",
               [T(f'sum by (consumergroup, topic) (max by ({KEY}, consumergroup, topic, partition) (rate(kafka_consumergroup_current_offset{{{G}}}[$__rate_interval])))', "{{consumergroup}} / {{topic}}")]), 12))
    d.line(8,
           (ts("Estimated time to catch up", "Lag divided by the current consumption rate: how long the group would "
               "need at its present speed. Empty while a group consumes nothing.",
               [T(f'sum by (consumergroup, topic) (max by ({KEY}, consumergroup, topic, partition) (kafka_consumergroup_lag{{{G}}})) / '
                  f'(sum by (consumergroup, topic) (max by ({KEY}, consumergroup, topic, partition) (rate(kafka_consumergroup_current_offset{{{G}}}[$__rate_interval]))) > 0)', "{{consumergroup}} / {{topic}}")],
               unit="s"), 12),
           (table("Consumer groups", "Current lag and members per group and topic.",
                  [f'sum by ({KEY}, consumergroup, topic) (max by ({KEY}, consumergroup, topic, partition) (kafka_consumergroup_lag{{{G}}}))',
                   f'max by ({KEY}, consumergroup) (kafka_consumergroup_members{{{X}, consumergroup=~"$consumergroup"}})',
                   f'sum by ({KEY}, consumergroup, topic) (max by ({KEY}, consumergroup, topic, partition) (rate(kafka_consumergroup_current_offset{{{G}}}[5m])))'],
                  [("strimzi_io_cluster", "Cluster", None, None), ("consumergroup", "Group", None, None),
                   ("topic", "Topic", None, None),
                   ("Value #A", "Lag", "short", {"cell": "color-background", "th": steps((GREEN, None), (AMBER, 1000), (RED, 100000))}),
                   ("Value #B", "Members", "short", {"cell": "color-text", "th": steps((RED, None), (GREEN, 1))}),
                   ("Value #C", "Msgs/s", "short", {"decimals": 2})],
                  sort=("Lag", True), hide=("namespace",)), 12))

    d.row("Partitions")
    d.line(9,
           (table("Topics", "Per-topic partition layout and health. Replicas vs in-sync replicas are summed over "
                  "partitions; a gap is under-replication.",
                  [f'max by ({KEY}, topic) (kafka_topic_partitions{{{TX}}})',
                   f'sum by ({KEY}, topic) (max by ({KEY}, topic, partition) (kafka_topic_partition_replicas{{{TX}}}))',
                   f'sum by ({KEY}, topic) (max by ({KEY}, topic, partition) (kafka_topic_partition_in_sync_replica{{{TX}}}))',
                   f'sum by ({KEY}, topic) (max by ({KEY}, topic, partition) (kafka_topic_partition_under_replicated_partition{{{TX}}}))',
                   f'sum by ({KEY}, topic) (max by ({KEY}, topic, partition) (kafka_topic_partition_current_offset{{{TX}}} - kafka_topic_partition_oldest_offset{{{TX}}}))',
                   f'sum by ({KEY}, topic) (max by ({KEY}, topic, partition) (rate(kafka_topic_partition_current_offset{{{TX}}}[5m])))'],
                  [("namespace", "Namespace", None, None), ("strimzi_io_cluster", "Cluster", None, None),
                   ("topic", "Topic", None, None),
                   ("Value #A", "Partitions", "short", None), ("Value #B", "Replicas", "short", None),
                   ("Value #C", "In sync", "short", None),
                   ("Value #D", "Under-replicated", "short", {"cell": "color-background", "th": OK_BAD}),
                   ("Value #E", "Retained msgs", "short", None),
                   ("Value #F", "Msgs/s", "short", {"decimals": 2})],
                  sort=("Msgs/s", True)), 24))
    return d


# =============================================================================================
# RabbitMQ
# =============================================================================================

def rabbitmq():
    R = 'namespace=~"$namespace", pod=~"$pod"'
    A = R + ', rabbitmq_endpoint!="detailed"'
    KSM = 'job="kube-state-metrics"'
    d = Dash("Kn5xm-gZk", "RabbitMQ / Overview",
             "RabbitMQ brokers scraped through the rabbitmq_prometheus plugin (aggregated /metrics for node and "
             "message totals, /metrics/detailed for per-queue depth). Health and resource alarms, message flow, "
             "queues, connections and the Erlang runtime.",
             DATA_TAGS + ["rabbitmq"],
             [ds_var(),
              qvar("namespace", "Namespace", "label_values(rabbitmq_identity_info, namespace)"),
              qvar("cluster", "RabbitMQ cluster", 'label_values(rabbitmq_identity_info{namespace=~"$namespace"}, rabbitmq_cluster)'),
              qvar("pod", "Node", 'label_values(rabbitmq_identity_info{namespace=~"$namespace", rabbitmq_cluster=~"$cluster"}, pod)')],
             links=dash_links(("Kubernetes overview", OVERVIEW_UID)))

    def s(metric, extra=""):
        return f'sum(max by (namespace, pod) ({metric}{{{R}{extra}}}))'

    def rt(metric, extra="", by="namespace, pod"):
        return f'max by ({by}) (rate({metric}{{{R}{extra}}}[$__rate_interval]))'

    d.row("Health at a glance")
    d.line(4,
           (stat("Nodes", "RabbitMQ nodes reporting metrics.",
                 [f'count(rabbitmq_identity_info{{{A}}}) or vector(0)'], color_mode="value", no_value="not scraped"), 3),
           (stat("Resource alarms", "Memory, disk or file-descriptor alarms raised on any node. While an alarm is "
                 "active RabbitMQ BLOCKS every publishing connection.",
                 [f'sum(rabbitmq_alarms_memory_used_watermark{{{R}}} + rabbitmq_alarms_free_disk_space_watermark{{{R}}} + rabbitmq_alarms_file_descriptor_limit{{{R}}}) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Unreachable peers", "Cluster peers a node cannot reach (network partition risk).",
                 [f'sum(rabbitmq_unreachable_cluster_peers_count{{{R}}}) or vector(0)'], th=OK_BAD), 3),
           (stat("Connections", "Client connections.", [s("rabbitmq_connections")], color_mode="value", graph="area"), 3),
           (stat("Channels", "Open channels.", [s("rabbitmq_channels")], color_mode="value", graph="area"), 3),
           (stat("Consumers", "Consumers across all queues.", [s("rabbitmq_consumers")], color_mode="value", graph="area"), 3),
           (stat("Queues", "Declared queues.", [s("rabbitmq_queues")], color_mode="value"), 3),
           (stat("Messages ready / unacked", "Messages waiting for a consumer (ready) and delivered but not yet "
                 "acknowledged (unacked).",
                 [T(s("rabbitmq_queue_messages_ready"), "ready"), T(s("rabbitmq_queue_messages_unacked"), "unacked")],
                 color_mode="value", text_mode="value_and_name", th=steps((GREEN, None), (AMBER, 10000), (RED, 100000))), 3))
    d.line(6, (table(
        "Nodes", "One row per node: version, uptime, memory against the high watermark, free disk against the disk "
        "limit, file descriptors and Erlang processes.",
        [f'max by (namespace, pod, rabbitmq_cluster, rabbitmq_node) (rabbitmq_identity_info{{{A}}})',
         f'max by (namespace, pod, rabbitmq_version, erlang_version) (rabbitmq_build_info{{{R}}})',
         f'max by (namespace, pod) (rabbitmq_erlang_uptime_seconds{{{R}}})',
         f'100 * max by (namespace, pod) (rabbitmq_process_resident_memory_bytes{{{R}}}) / max by (namespace, pod) (rabbitmq_resident_memory_limit_bytes{{{R}}})',
         f'max by (namespace, pod) (rabbitmq_disk_space_available_bytes{{{R}}})',
         f'max by (namespace, pod) (rabbitmq_disk_space_available_limit_bytes{{{R}}})',
         f'100 * max by (namespace, pod) (rabbitmq_process_open_fds{{{R}}}) / max by (namespace, pod) (rabbitmq_process_max_fds{{{R}}})',
         f'100 * max by (namespace, pod) (rabbitmq_erlang_processes_used{{{R}}}) / max by (namespace, pod) (rabbitmq_erlang_processes_limit{{{R}}})',
         f'max by (namespace, pod) (rabbitmq_alarms_memory_used_watermark{{{R}}} + rabbitmq_alarms_free_disk_space_watermark{{{R}}} + rabbitmq_alarms_file_descriptor_limit{{{R}}})'],
        [("namespace", "Namespace", None, None), ("rabbitmq_cluster", "Cluster", None, None),
         ("pod", "Pod", None, {"links": pod_link()}), ("rabbitmq_version", "Version", None, None),
         ("erlang_version", "Erlang", None, None),
         ("Value #C", "Uptime", "s", None),
         ("Value #D", "Memory % of watermark", "percent", {"cell": "gauge", "th": PCT, "min": 0, "max": 100}),
         ("Value #E", "Disk free", "bytes", None), ("Value #F", "Disk limit", "bytes", None),
         ("Value #G", "FDs %", "percent", {"cell": "color-text", "th": PCT, "decimals": 1}),
         ("Value #H", "Erlang procs %", "percent", {"cell": "color-text", "th": PCT, "decimals": 1}),
         ("Value #I", "Alarms", None, {"mappings": vmap((0, "none", GREEN)), "cell": "color-background", "th": OK_BAD})],
        hide=("Value #A", "Value #B", "rabbitmq_node")), 24))

    d.row("Message flow")
    d.line(8,
           (ts("Published / delivered / acknowledged", "Messages received from publishers, delivered to consumers and "
               "acknowledged, per second (all protocols).",
               [T(f'sum({rt("rabbitmq_global_messages_received_total")})', "published"),
                T(f'sum({rt("rabbitmq_global_messages_routed_total")})', "routed to queues"),
                T(f'sum({rt("rabbitmq_global_messages_delivered_total")})', "delivered"),
                T(f'sum({rt("rabbitmq_global_messages_acknowledged_total")})', "acknowledged"),
                T(f'sum({rt("rabbitmq_global_messages_confirmed_total")})', "confirmed to publishers")],
               unit="short"), 12),
           (ts("Redelivered, unroutable & dead-lettered", "Redeliveries (consumer rejected/nacked or crashed), messages "
               "that matched no queue (dropped or returned) and messages dead-lettered, by reason.",
               [T(f'sum({rt("rabbitmq_global_messages_redelivered_total")})', "redelivered"),
                T(f'sum({rt("rabbitmq_global_messages_unroutable_dropped_total")})', "unroutable dropped"),
                T(f'sum({rt("rabbitmq_global_messages_unroutable_returned_total")})', "unroutable returned"),
                T(f'label_replace(sum({rt("rabbitmq_global_messages_dead_lettered_rejected_total")}), "reason", "rejected", "", "") or '
                  f'label_replace(sum({rt("rabbitmq_global_messages_dead_lettered_expired_total")}), "reason", "expired", "", "") or '
                  f'label_replace(sum({rt("rabbitmq_global_messages_dead_lettered_maxlen_total")}), "reason", "maxlen", "", "") or '
                  f'label_replace(sum({rt("rabbitmq_global_messages_dead_lettered_delivery_limit_total")}), "reason", "delivery_limit", "", "")',
                  "dead-lettered ({{reason}})")],
               unit="short", overrides=[ov_color("unroutable.*", RED)]), 12))
    d.line(8,
           (ts("Queue depth", "Messages ready for delivery and delivered-but-unacknowledged across all queues.",
               [T(s("rabbitmq_queue_messages_ready"), "ready"), T(s("rabbitmq_queue_messages_unacked"), "unacked")],
               stack=True), 8),
           (ts("Deepest queues (top 10)", "Ready + unacked messages per queue (from /metrics/detailed). Empty until a "
               "queue exists.",
               [T(f'topk(10, sum by (vhost, queue) (rabbitmq_detailed_queue_messages_ready{{{R}}} + rabbitmq_detailed_queue_messages_unacked{{{R}}}))', "{{vhost}}/{{queue}}")]), 8),
           (heatmap("Message size", "Distribution of published message sizes.",
                    f'sum by (le) (increase(rabbitmq_message_size_bytes_bucket{{{R}}}[$__rate_interval]))', unit="bytes"), 8))
    d.line(8, (table(
        "Queues", "Per-queue depth and consumers (from /metrics/detailed). A queue with messages and zero consumers "
        "is not being drained.",
        [f'max by (vhost, queue) (rabbitmq_detailed_queue_messages_ready{{{R}}})',
         f'max by (vhost, queue) (rabbitmq_detailed_queue_messages_unacked{{{R}}})',
         f'max by (vhost, queue) (rabbitmq_detailed_queue_consumers{{{R}}})'],
        [("vhost", "Vhost", None, None), ("queue", "Queue", None, None),
         ("Value #A", "Ready", "short", {"cell": "color-background", "th": steps((GREEN, None), (AMBER, 1000), (RED, 100000))}),
         ("Value #B", "Unacked", "short", None),
         ("Value #C", "Consumers", "short", {"cell": "color-text", "th": steps((RED, None), (GREEN, 1))})],
        sort=("Ready", True)), 24))

    d.row("Connections & channels")
    d.line(8,
           (ts("Connections, channels, consumers", "Open connections, channels and consumers.",
               [T(s("rabbitmq_connections"), "connections"), T(s("rabbitmq_channels"), "channels"),
                T(s("rabbitmq_consumers"), "consumers")]), 8),
           (ts("Churn", "Connections and channels opened (above) and closed (below) per second. High churn is a "
               "client that connects per message instead of reusing a connection.",
               [T(f'sum({rt("rabbitmq_connections_opened_total")})', "connections opened"),
                T(f'sum({rt("rabbitmq_connections_closed_total")})', "connections closed"),
                T(f'sum({rt("rabbitmq_channels_opened_total")})', "channels opened"),
                T(f'sum({rt("rabbitmq_channels_closed_total")})', "channels closed")],
               overrides=[ov_negative(".*closed")]), 8),
           (ts("Authentication", "Successful and failed authentication attempts per second.",
               [T(f'sum({rt("rabbitmq_auth_attempts_succeeded_total")})', "succeeded"),
                T(f'sum({rt("rabbitmq_auth_attempts_failed_total")})', "failed")],
               overrides=[ov_color("failed", RED)]), 8))

    d.row("Node resources & Erlang runtime")
    d.line(8,
           (ts("Memory vs high watermark", "Resident memory per node against the vm_memory_high_watermark limit "
               "(dashed). At the limit a memory alarm blocks publishers.",
               [T(f'max by (pod) (rabbitmq_process_resident_memory_bytes{{{R}}})', "{{pod}} used"),
                T(f'max by (pod) (rabbitmq_resident_memory_limit_bytes{{{R}}})', "{{pod}} watermark")],
               unit="bytes", overrides=[ov_dashed(".*watermark", RED)]), 8),
           (ts("Disk free vs limit", "Free disk on the data volume against disk_free_limit (dashed). Below the "
               "limit a disk alarm blocks publishers.",
               [T(f'max by (pod) (rabbitmq_disk_space_available_bytes{{{R}}})', "{{pod}} free"),
                T(f'max by (pod) (rabbitmq_disk_space_available_limit_bytes{{{R}}})', "{{pod}} limit")],
               unit="bytes", overrides=[ov_dashed(".*limit", RED)]), 8),
           (ts("File descriptors & Erlang processes", "Usage of the two hard limits as a percentage.",
               [T(f'100 * max by (pod) (rabbitmq_process_open_fds{{{R}}}) / max by (pod) (rabbitmq_process_max_fds{{{R}}})', "{{pod}} fds"),
                T(f'100 * max by (pod) (rabbitmq_erlang_processes_used{{{R}}}) / max by (pod) (rabbitmq_erlang_processes_limit{{{R}}})', "{{pod}} processes")],
               unit="percent", th=PCT, soft_max=100), 8))
    d.line(8,
           (ts("Scheduler run queue & GC", "Erlang run queue length (work waiting for a scheduler) and garbage "
               "collections per second.",
               [T(f'max by (pod) (rabbitmq_erlang_scheduler_run_queue{{{R}}})', "{{pod}} run queue"),
                T(f'max by (pod) (rate(rabbitmq_erlang_gc_runs_total{{{R}}}[$__rate_interval]))', "{{pod}} GC/s")]), 8),
           (ts("Disk I/O", "Bytes read and written by the message store and queue index.",
               [T(f'max by (pod) (rate(rabbitmq_io_read_bytes_total{{{R}}}[$__rate_interval]))', "{{pod}} read"),
                T(f'max by (pod) (rate(rabbitmq_io_write_bytes_total{{{R}}}[$__rate_interval]))', "{{pod}} write")],
               unit="Bps", overrides=[ov_negative(".*write")]), 8),
           (ts("Container CPU & restarts", "RabbitMQ container CPU and restarts per interval (bars, right axis).",
               [T(E('sum by (pod) (rate(container_cpu_usage_seconds_total{<<R>>, container="rabbitmq"}[$__rate_interval]))', R=R), "{{pod}} cpu"),
                T(E('sum by (pod) (increase(kube_pod_container_status_restarts_total{<<KSM>>, <<R>>, container="rabbitmq"}[$__rate_interval]))', R=R, KSM=KSM), "{{pod}} restarts")],
               unit="cores", overrides=[{"matcher": {"id": "byRegexp", "options": ".*restarts"},
                                         "properties": [{"id": "unit", "value": "short"},
                                                        {"id": "custom.drawStyle", "value": "bars"},
                                                        {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(8, (logs("Broker logs (Loki)", "Warnings and errors from the selected nodes.",
                    '{namespace=~"$namespace", pod=~"$pod", container="rabbitmq"} |~ "(?i)\\\\[(warning|error)\\\\]|alarm|blocked"'), 24))

    # Folded in from the former "RabbitMQ-Stream" dashboard (grafana.com 14798).
    d.row("Streams (stream protocol)")
    STR = A + ', protocol="stream"'

    def gs(metric, rate=False):
        inner = f'rate({metric}{{{STR}}}[$__rate_interval])' if rate else f'{metric}{{{STR}}}'
        return f'sum by (pod) (max by (namespace, pod) ({inner}))'
    d.line(8,
           (ts("Stream publishers & consumers", "Clients connected over the RabbitMQ stream protocol (port 5552), per "
               "node. Empty when no application uses streams.",
               [T(gs("rabbitmq_global_publishers"), "{{pod}} publishers"),
                T(gs("rabbitmq_global_consumers"), "{{pod}} consumers")], decimals=0), 8),
           (ts("Stream messages / s", "Messages received from publishers, confirmed back to them, and delivered to "
               "consumers (below the axis) over the stream protocol.",
               [T(gs("rabbitmq_global_messages_received_total", True), "{{pod}} received"),
                T(gs("rabbitmq_global_messages_confirmed_total", True), "{{pod}} confirmed"),
                T(gs("rabbitmq_global_messages_delivered_total", True), "{{pod}} delivered")],
               unit="ops", overrides=[ov_negative(".*delivered")]), 8),
           (ts("Stream protocol errors / s", "Every stream-protocol error class (access refused, authentication, "
               "stream does not exist / not available, frame too large, ...) per node. Any sustained value is a "
               "misconfigured client.",
               [T(f'sum by (pod) (rate({{__name__=~"rabbitmq_global_stream_error_.+_total", {STR}}}[$__rate_interval]))', "{{pod}}")],
               unit="ops", th=steps((GREEN, None), (RED, 0.01))), 8))
    return d


