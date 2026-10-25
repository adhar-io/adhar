"""Resilience folder: one dashboard for how the platform withstands and recovers from
failure — backups (Velero), chaos experiments (Litmus, the platform's chaos engine),
load tests (k6) and the Adhar resilience / scorecard jobs that run them on a schedule.

Litmus-specific dashboards are used for chaos visibility across profiles in
favour of Litmus.
"""
from dashlib import *  # noqa: F401,F403
from dash_common import (KSM, SYS, UIDS, component_logs, controller_runtime, links, pods_ready_stat, pods_resources,
                         pods_table, tags, up_stat)

VE = 'job="velero"'
K6 = 'job=~"k6-operator|k6-k6-operator-controller-manager-metrics-service"'
JOBS = "adhar-resilience-runner|adhar-scorecard-scorer"


def resilience():
    d = Dash(UIDS["resilience"], "Resilience / Chaos, Backup & Load Testing",
             "Can the platform survive a failure and recover from one? Velero backups and restores (schedules, "
             "outcomes, age of the last good backup, snapshots, the storage location), Litmus chaos experiments, "
             "k6 load tests, and the Adhar resilience-runner and scorecard CronJobs that exercise them.",
             tags("Resilience", "velero", "litmus", "k6"),
             [ds_var(), qvar("schedule", "Backup schedule", f'label_values(velero_backup_last_status{{{VE}}}, schedule)')],
             links=links("Resilience", ("Storage", "k8s-storage"), ("Developer platform", "dev-platform")),
             collapse=1)
    SC = f'{VE}, schedule=~"$schedule"'

    d.row("Resilience at a glance")
    d.line(4,
           (stat("Last backup", "Outcome of the most recent backup of each schedule (worst shown). 1 = succeeded.",
                 [f'min(velero_backup_last_status{{{SC}}})'], mappings=vmap((0, "FAILED", RED), (1, "Succeeded", GREEN)),
                 no_value="no backup yet"), 3),
           (stat("Oldest last good backup", "Hours since the last successful backup, worst schedule. The daily "
                 "schedule should never pass ~26 h.",
                 [f'max(time() - velero_backup_last_successful_timestamp{{{SC}}}) / 3600'], unit="h",
                 th=steps((GREEN, None), (AMBER, 26), (RED, 50)), decimals=1, no_value="no backup yet"), 3),
           (stat("Backup failures (7d)", "Failed and partially failed backups in the last 7 days.",
                 [f'sum(increase(velero_backup_failure_total{{{SC}}}[7d])) + sum(increase(velero_backup_partial_failure_total{{{SC}}}[7d]))'],
                 th=OK_WARN_BAD, decimals=0), 3),
           (stat("Backup location", "Whether Velero's backup storage location (RustFS) is available.",
                 [f'min(velero_backup_location_status_gauge{{{VE}}})'], mappings=vmap((0, "UNAVAILABLE", RED), (1, "Available", GREEN))), 3),
           (stat("Restores (30d)", "Restores attempted in the last 30 days and how many failed.",
                 [T(f'sum(increase(velero_restore_attempt_total{{{VE}}}[30d]))', "attempted"),
                  T(f'sum(increase(velero_restore_failed_total{{{VE}}}[30d]))', "failed")],
                 color_mode="value", text_mode="value_and_name", decimals=0), 3),
           (pods_ready_stat("Litmus", "Litmus ChaosCenter: server, auth server, frontend, database.", "litmus-.+"), 3),
           (up_stat("k6 operator", "k6-operator metrics endpoint.", "k6-operator|k6-k6-operator-controller-manager-metrics-service"), 3),
           (stat("Resilience jobs last success", "Hours since the resilience runner / scorecard CronJobs last "
                 "succeeded (worst).",
                 [f'max(time() - kube_cronjob_status_last_successful_time{{{KSM}, cronjob=~"{JOBS}"}}) / 3600'],
                 unit="h", th=steps((GREEN, None), (AMBER, 26), (RED, 50)), decimals=1, no_value="never"), 3))

    d.row("Velero: backups")
    d.line(8,
           (ts("Backups by outcome", "Backups per interval by outcome (bars).",
               [T(f'sum(increase(velero_backup_success_total{{{SC}}}[$__rate_interval]))', "succeeded"),
                T(f'sum(increase(velero_backup_partial_failure_total{{{SC}}}[$__rate_interval]))', "partially failed"),
                T(f'sum(increase(velero_backup_failure_total{{{SC}}}[$__rate_interval]))', "failed"),
                T(f'sum(increase(velero_backup_validation_failure_total{{{SC}}}[$__rate_interval]))', "validation failed")],
               draw="bars", stack=True, decimals=0, overrides=[ov_color("succeeded", GREEN), ov_color("partially failed", AMBER),
                                                               ov_color("failed|validation failed", RED)]), 8),
           (table("Schedules", "Each backup schedule: last outcome, hours since the last success, items backed up "
                  "and item errors in the last backup.",
                  [f'max by (schedule) (velero_backup_last_status{{{SC}}})',
                   f'max by (schedule) (time() - velero_backup_last_successful_timestamp{{{SC}}}) / 3600',
                   f'max by (schedule) (velero_backup_items_total{{{SC}}})',
                   f'max by (schedule) (velero_backup_items_errors{{{SC}}})'],
                  [("schedule", "Schedule", None, None),
                   ("Value #A", "Last", None, {"mappings": vmap((0, "FAILED", RED), (1, "Succeeded", GREEN)), "cell": "color-background"}),
                   ("Value #B", "Since success", "h", {"decimals": 1, "cell": "color-text", "th": steps((GREEN, None), (AMBER, 26), (RED, 50))}),
                   ("Value #C", "Items", "short", None),
                   ("Value #D", "Item errors", "short", {"cell": "color-background", "th": OK_BAD})]), 8),
           (ts("Backup duration p95", "95th percentile backup duration per schedule.",
               [T(f'histogram_quantile(0.95, sum by (le, schedule) (rate(velero_backup_duration_seconds_bucket{{{SC}}}[1h])))', "{{schedule}}")],
               unit="s"), 8))
    d.line(8,
           (ts("Snapshots", "Volume and CSI snapshots attempted / failed per interval.",
               [T(f'sum(increase(velero_volume_snapshot_attempt_total{{{VE}}}[$__rate_interval]))', "volume attempted"),
                T(f'sum(increase(velero_volume_snapshot_failure_total{{{VE}}}[$__rate_interval]))', "volume failed"),
                T(f'sum(increase(velero_csi_snapshot_attempt_total{{{VE}}}[$__rate_interval]))', "CSI attempted"),
                T(f'sum(increase(velero_csi_snapshot_failure_total{{{VE}}}[$__rate_interval]))', "CSI failed")],
               draw="bars", decimals=0, overrides=[ov_color(".*failed", RED)]), 8),
           (ts("Restores", "Restores per interval by outcome.",
               [T(f'sum(increase(velero_restore_success_total{{{VE}}}[$__rate_interval]))', "succeeded"),
                T(f'sum(increase(velero_restore_partial_failure_total{{{VE}}}[$__rate_interval]))', "partially failed"),
                T(f'sum(increase(velero_restore_failed_total{{{VE}}}[$__rate_interval]))', "failed")],
               draw="bars", decimals=0, overrides=[ov_color("succeeded", GREEN), ov_color("partially failed", AMBER),
                                                   ov_color("failed", RED)]), 8),
           (ts("Backup deletions & warnings", "Expired backups deleted (garbage collection) and backups that "
               "completed with warnings.",
               [T(f'sum(increase(velero_backup_deletion_success_total{{{VE}}}[$__rate_interval]))', "deleted"),
                T(f'sum(increase(velero_backup_deletion_failure_total{{{VE}}}[$__rate_interval]))', "deletion failed"),
                T(f'sum(increase(velero_backup_warning_total{{{VE}}}[$__rate_interval]))', "with warnings")],
               draw="bars", decimals=0, overrides=[ov_color("deletion failed", RED), ov_color("with warnings", AMBER)]), 8))
    d.line(8, *[(p, 8) for p in pods_resources("velero-.+|node-agent-.+")])

    d.row("Chaos experiments (Litmus)")
    d.line(9, (pods_table("Litmus pods", "The ChaosCenter control plane. Experiments run as pods in the target "
                          "namespace and appear in Workloads & Pods.", "litmus-.+"), 24))
    d.line(8, *[(p, 8) for p in pods_resources("litmus-.+")])
    d.line(9, (component_logs("Chaos runs (Loki)", "Litmus server and chaos-runner lines about experiments.",
                              'namespace="adhar-system", pod=~"litmus-.+|.*-runner.*"',
                              pattern='(?i)(experiment|chaos|verdict|fail|error)'), 24))

    d.row("Load tests (k6)")
    d.line(8, *[(p, 6) for p in controller_runtime("k6-operator|k6-k6-operator-controller-manager-metrics-service", what="k6")])
    d.line(8,
           (ts("k6 runner pods", "k6 runner / initializer / starter pods (TestRuns) by phase.",
               [T(f'sum by (phase) (kube_pod_status_phase{{{KSM}, pod=~".*k6.*", pod!~"k6-k6-operator.*"}})', "{{phase}}")],
               stack=True, decimals=0), 12),
           (ts("k6 runner CPU", "CPU used by k6 runner pods: a load generator pegged at its limit under-reports the "
               "load it was asked to produce.",
               [T(f'sum by (pod) (rate(container_cpu_usage_seconds_total{{job="kubelet", metrics_path="/metrics/cadvisor", pod=~".*k6.*", container!=""}}[$__rate_interval]))', "{{pod}}")],
               unit="cores"), 12))

    d.row("Adhar resilience & scorecard jobs")
    d.line(8,
           (table("CronJobs", "The jobs that exercise the platform on a schedule: last run, last success, active.",
                  [f'time() - max by (cronjob) (kube_cronjob_status_last_schedule_time{{{KSM}, cronjob=~"{JOBS}"}})',
                   f'time() - max by (cronjob) (kube_cronjob_status_last_successful_time{{{KSM}, cronjob=~"{JOBS}"}})',
                   f'max by (cronjob) (kube_cronjob_status_active{{{KSM}, cronjob=~"{JOBS}"}})',
                   f'max by (cronjob) (kube_cronjob_spec_suspend{{{KSM}, cronjob=~"{JOBS}"}})'],
                  [("cronjob", "CronJob", None, None), ("Value #A", "Last run", "s", None),
                   ("Value #B", "Last success", "s", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 93600), (RED, 180000))}),
                   ("Value #C", "Active", "short", None),
                   ("Value #D", "Suspended", None, {"mappings": vmap((0, "no", GREEN), (1, "SUSPENDED", AMBER))})]), 12),
           (ts("Job runs", "Runs of those jobs per interval by outcome.",
               [T(f'sum(increase(kube_job_status_succeeded{{{KSM}, job_name=~"({JOBS})-.+"}}[$__rate_interval]))', "succeeded"),
                T(f'sum(increase(kube_job_status_failed{{{KSM}, job_name=~"({JOBS})-.+"}}[$__rate_interval]))', "failed")],
               draw="bars", decimals=0, overrides=[ov_color("succeeded", GREEN), ov_color("failed", RED)]), 12))
    return d


DASHBOARDS = [
    ("resilience", "Resilience", resilience),
]
