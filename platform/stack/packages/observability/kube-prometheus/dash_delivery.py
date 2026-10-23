"""Delivery folder: three dashboards in place of fifteen.

  GitOps & Promotion     Argo CD (applications, syncs, reconciliation, repo server, cluster
                         cache), Argo Rollouts, Kargo and Gitea — how a commit becomes a
                         running release.
  CI & Artifacts         Tekton pipelines / tasks / triggers, Argo Events, the image and
                         package registries (Harbor, Nexus) and kpack builds.
  Developer Platform     Crossplane (managed resources, compositions, functions), Coder
                         workspaces and the Dapr control plane.

Argo Workflows (2) and Terraform dashboards were dropped with their packages (disabled
in every profile).
"""
from dashlib import *  # noqa: F401,F403
from dash_common import (KSM, SYS, UIDS, component_logs, controller_runtime, go_process, links, pods_ready_stat,
                         pods_resources, pods_table, tags, up_stat)

ACD = 'job="adhar-system/argocd"'


# =============================================================================================
# GitOps & Promotion
# =============================================================================================

def gitops():
    APP = 'name=~"$app", project=~"$project"'
    RO = 'job="argo-rollouts"'
    d = Dash(UIDS["gitops"], "Delivery / GitOps & Promotion",
             "How a commit becomes a running release: Argo CD applications (health, sync, reconciliation, the repo "
             "server's git traffic, the cluster cache), Argo Rollouts' progressive delivery, Kargo's promotion "
             "pipeline and the Gitea server they read from. Replaces the ArgoCD, Argo Rollouts, Kargo and Gitea "
             "dashboards. Argo CD's reconciliation period is 120s; Gitea webhooks make syncs immediate.",
             tags("Delivery", "argocd", "gitops"),
             [ds_var(),
              qvar("project", "Project", f'label_values(argocd_app_info{{{ACD}}}, project)'),
              qvar("app", "Application", f'label_values(argocd_app_info{{{ACD}, project=~"$project"}}, name)')],
             links=links("Delivery", ("CI & artifacts", "ci"), ("Developer platform", "dev-platform")),
             collapse=1)

    d.row("GitOps at a glance")
    d.line(4,
           (stat("Applications", "Argo CD Applications in the selection.",
                 [f'count(argocd_app_info{{{ACD}, {APP}}})'], color_mode="value"), 3),
           (stat("Healthy", "Applications whose health is Healthy.",
                 [f'count(argocd_app_info{{{ACD}, {APP}, health_status="Healthy"}}) or vector(0)'],
                 color_mode="value", th=steps((GREEN, None))), 3),
           (stat("Degraded / Missing", "Applications Degraded or Missing — something they deploy is broken or "
                 "was never created.",
                 [f'count(argocd_app_info{{{ACD}, {APP}, health_status=~"Degraded|Missing"}}) or vector(0)'], th=OK_BAD), 3),
           (stat("Progressing", "Applications still rolling out.",
                 [f'count(argocd_app_info{{{ACD}, {APP}, health_status="Progressing"}}) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1))), 3),
           (stat("OutOfSync", "Applications whose live state differs from Git. Normal for ~10 minutes after a "
                 "push to the packages repo (every app re-compares); persistent means a sync that cannot finish.",
                 [f'count(argocd_app_info{{{ACD}, {APP}, sync_status="OutOfSync"}}) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 10))), 3),
           (stat("Unknown (cannot compare)", "Applications Argo CD could not compare — a manifest that does not "
                 "render, or an API server that times out. 'Unknown' is not a health verdict.",
                 [f'count(argocd_app_info{{{ACD}, {APP}, sync_status="Unknown"}}) or vector(0)'], th=OK_WARN_BAD), 3),
           (stat("Failed syncs (24h)", "Sync operations that ended Failed or Error in the last 24 hours.",
                 [f'sum(increase(argocd_app_sync_total{{{ACD}, {APP}, phase=~"Failed|Error"}}[24h])) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 5)), decimals=0), 3),
           (stat("Cluster cache", "Argo CD's connection to the cluster it deploys to (1 = Successful).",
                 [f'min(argocd_cluster_connection_status{{{ACD}}})'], mappings=vmap((0, "FAILED", RED), (1, "Connected", GREEN))), 3))

    d.row("Applications")
    health_map = {"Healthy": GREEN, "Progressing": BLUE, "Degraded": RED, "Missing": RED, "Suspended": PURPLE, "Unknown": GREY}
    d.line(8,
           (ts("Applications by health", "Applications per health status over time.",
               [T(f'count by (health_status) (argocd_app_info{{{ACD}, {APP}}})', "{{health_status}}")],
               stack=True, decimals=0, overrides=[ov_color(k, v) for k, v in health_map.items()]), 8),
           (ts("Applications by sync status", "Applications Synced / OutOfSync / Unknown over time.",
               [T(f'count by (sync_status) (argocd_app_info{{{ACD}, {APP}}})', "{{sync_status}}")],
               stack=True, decimals=0, overrides=[ov_color("Synced", GREEN), ov_color("OutOfSync", AMBER),
                                                  ov_color("Unknown", GREY)]), 8),
           (table("Applications that need attention", "Every application that is not Healthy and Synced, with "
                  "its destination and repository.",
                  [f'max by (name, project, health_status, sync_status, dest_namespace) (argocd_app_info{{{ACD}, {APP}}}) '
                   f'unless on (name) max by (name) (argocd_app_info{{{ACD}, health_status="Healthy", sync_status="Synced"}})'],
                  [("name", "Application", None, None), ("project", "Project", None, None),
                   ("health_status", "Health", None, {"cell": "color-text", "mappings": vmap(
                       *[(k, k, v) for k, v in health_map.items()])}),
                   ("sync_status", "Sync", None, {"cell": "color-text", "mappings": vmap(
                       ("Synced", "Synced", GREEN), ("OutOfSync", "OutOfSync", AMBER), ("Unknown", "Unknown", GREY))}),
                   ("dest_namespace", "Namespace", None, None)],
                  sort=("Health", False), hide=("Value",)), 8))
    d.line(8,
           (ts("Syncs by phase", "Sync operations per interval by outcome (bars).",
               [T(f'sum by (phase) (increase(argocd_app_sync_total{{{ACD}, {APP}}}[$__rate_interval]))', "{{phase}}")],
               draw="bars", decimals=0, stack=True, overrides=[ov_color("Succeeded", GREEN), ov_color("Failed|Error", RED),
                                                               ov_color("Running", BLUE)]), 8),
           (bargauge("Sync time per application (range)", "Seconds spent syncing per application over the time "
                     "range (top 15). Long totals are apps that sync repeatedly or wait on hooks.",
                     [T(f'topk(15, sum by (name) (increase(argocd_app_sync_duration_seconds_total{{{ACD}, {APP}}}[$__range])))', "{{name}}")],
                     unit="s", min=0, max=None, th=NEUTRAL, decimals=0), 8),
           (ts("Orphaned resources", "Resources in an application's namespaces that no application owns (top 10).",
               [T(f'topk(10, max by (name) (argocd_app_orphaned_resources_count{{{ACD}, {APP}}})) > 0', "{{name}}")],
               decimals=0), 8))

    d.row("Controller: reconciliation & cluster cache")
    d.line(8,
           (heatmap("Application reconcile time", "How long the application controller takes to reconcile an app "
                    "(diff + health). A band moving right means a slow repo server or API server.",
                    f'sum by (le) (increase(argocd_app_reconcile_bucket{{{ACD}}}[$__rate_interval]))'), 8),
           (ts("Kubernetes API requests by verb", "Requests the application controller makes to the API server "
               "per second, by verb and response code.",
               [T(f'sum by (verb, response_code) (rate(argocd_app_k8s_request_total{{{ACD}}}[$__rate_interval]))', "{{verb}} {{response_code}}")],
               unit="reqps", overrides=[ov_color(".* 5..", RED), ov_color(".* 4..", "orange")]), 8),
           (ts("Cluster cache", "Resources and API kinds held in the cluster cache, and seconds since the cache "
               "was last refreshed. Hundreds of CRDs make the cache expensive to build.",
               [T(f'sum(argocd_cluster_api_resource_objects{{{ACD}}})', "objects"),
                T(f'sum(argocd_cluster_api_resources{{{ACD}}})', "API kinds"),
                T(f'max(argocd_cluster_cache_age_seconds{{{ACD}}})', "cache age (s)")],
               overrides=[{"matcher": {"id": "byName", "options": "cache age (s)"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(8,
           (ts("Resource events processed", "Watch events the controller processes per second, by kind (top 10).",
               [T(f'topk(10, sum by (kind) (rate(argocd_cluster_events_total{{{ACD}}}[$__rate_interval])))', "{{kind}}")],
               unit="ops"), 8),
           (ts("kubectl operations", "kubectl apply / delete executions per second and those still pending.",
               [T(f'sum by (command) (rate(argocd_kubectl_exec_total{{{ACD}}}[$__rate_interval]))', "{{command}}"),
                T(f'sum by (command) (argocd_kubectl_exec_pending{{{ACD}}})', "{{command}} pending")],
               overrides=[ov_dashed(".* pending")]), 8),
           (ts("ApplicationSets", "Applications owned per ApplicationSet and reconcile time p99.",
               [T(f'max by (name) (argocd_appset_owned_applications{{{ACD}}})', "{{name}}"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(argocd_appset_reconcile_bucket{{{ACD}}}[$__rate_interval])))', "reconcile p99")],
               decimals=0, overrides=[{"matcher": {"id": "byName", "options": "reconcile p99"},
                                       "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("Repo server & Git")
    d.line(8,
           (ts("Git requests", "Git fetch / ls-remote requests per second per repository.",
               [T(f'sum by (repo, request_type) (rate(argocd_git_request_total{{{ACD}}}[$__rate_interval]))', "{{request_type}} {{repo}}")],
               unit="reqps"), 8),
           (ts("Git request latency p99", "99th percentile time of git requests to Gitea, by type.",
               [T(f'histogram_quantile(0.99, sum by (le, request_type) (rate(argocd_git_request_duration_seconds_bucket{{{ACD}}}[$__rate_interval])))', "{{request_type}}")],
               unit="s"), 8),
           (ts("Pending manifest generations & Redis", "Requests waiting for the repo server per repository, and "
               "Argo CD's Redis cache requests (failed drawn red).",
               [T(f'sum by (repo) (argocd_repo_pending_request_total{{{ACD}}})', "pending {{repo}}"),
                T(f'sum by (failed) (rate(argocd_redis_request_total{{{ACD}}}[$__rate_interval]))', "redis failed={{failed}}")],
               overrides=[ov_color("redis failed=true", RED)]), 8))
    d.line(8,
           (ts("API server gRPC", "Argo CD API server gRPC calls per second by code (UI and CLI traffic).",
               [T(f'sum by (grpc_code) (rate(grpc_server_handled_total{{{ACD}}}[$__rate_interval]))', "{{grpc_code}}")],
               unit="reqps", overrides=[ov_color("Unknown|Internal|Unavailable", RED)]), 8),
           (ts("Logins", "Argo CD logins by result.",
               [T(f'sum by (status) (increase(argocd_login_request_total{{{ACD}}}[$__rate_interval]))', "{{status}}")],
               draw="bars", decimals=0, overrides=[ov_color("failure", RED)]), 8),
           (ts("Webhook handlers in flight", "Gitea push webhooks being processed (they trigger immediate refresh).",
               [T(f'sum(argocd_webhook_handlers_in_flight{{{ACD}}})', "in flight")], decimals=0), 8))
    d.line(9, (pods_table("Argo CD pods", "Application controller, repo server, API server, ApplicationSet and "
                          "notifications controllers, Dex and Redis.", "argo-cd-.+"), 24))
    d.line(8, *[(p, 8) for p in pods_resources("argo-cd-.+")])

    d.row("Argo Rollouts")
    d.line(8,
           (table("Rollouts", "Every Rollout: phase, strategy and replicas (desired / updated / available).",
                  [f'max by (namespace, name, phase, strategy) (rollout_info{{{RO}}})',
                   f'max by (namespace, name) (rollout_info_replicas_desired{{{RO}}})',
                   f'max by (namespace, name) (rollout_info_replicas_updated{{{RO}}})',
                   f'max by (namespace, name) (rollout_info_replicas_available{{{RO}}})'],
                  [("namespace", "Namespace", None, None), ("name", "Rollout", None, None),
                   ("phase", "Phase", None, {"cell": "color-text", "mappings": vmap(
                       ("Healthy", "Healthy", GREEN), ("Progressing", "Progressing", BLUE), ("Paused", "Paused", AMBER),
                       ("Degraded", "Degraded", RED))}),
                   ("strategy", "Strategy", None, None), ("Value #B", "Desired", "short", None),
                   ("Value #C", "Updated", "short", None), ("Value #D", "Available", "short", None)],
                  hide=("Value #A",)), 12),
           (ts("Rollout events & reconcile time", "Rollout events per second by reason (e.g. RolloutAborted drawn "
               "red) and the 99th percentile reconcile time.",
               [T(f'sum by (reason) (rate(rollout_events_total{{{RO}}}[$__rate_interval]))', "{{reason}}"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(rollout_reconcile_bucket{{{RO}}}[$__rate_interval])))', "reconcile p99")],
               unit="ops", overrides=[ov_color(".*Abort.*", RED),
                                      {"matcher": {"id": "byName", "options": "reconcile p99"},
                                       "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 12))

    d.row("Kargo (promotion)")
    d.line(4,
           (pods_ready_stat("Kargo", "Kargo API, controller, management controller and webhooks.", "kargo-.+"), 6),
           (up_stat("Kargo API metrics", "Kargo API server metrics endpoint.", "kargo-api"), 6),
           (stat("Promotion reconcile errors / s", "Reconcile errors of Kargo's controllers (Stages, Promotions, "
                 "Warehouses, Freight). A failing promotion shows up here before it shows up in the UI.",
                 ['sum(rate(controller_runtime_reconcile_errors_total{job=~"kargo.*"}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (RED, 0.01)), no_value="not scraped"), 6),
           (stat("Environment syncs (24h)", "Argo CD syncs of environment applications (<project>-<app>-<env>) in "
                 "the last 24 hours. Every Kargo promotion ends in one of these.",
                 [f'sum(increase(argocd_app_sync_total{{{ACD}, name=~".+-(dev|test|prod)"}}[24h])) or vector(0)'],
                 color_mode="value", decimals=0), 6))
    d.line(8, *[(p, 6) for p in controller_runtime('kargo.*', what="Kargo")])
    d.line(8, *[(p, 8) for p in pods_resources("kargo-.+")])

    d.row("Gitea")
    G = 'job="gitea-http"'
    d.line(4, *[(stat(n, f"{n} hosted in Gitea.", [f'max(gitea_{m}{{{G}}})'], color_mode="value", graph="area"), 4)
                for n, m in [("Repositories", "repositories"), ("Organizations", "organizations"), ("Users", "users"),
                             ("Mirrors", "mirrors"), ("Webhooks", "webhooks"), ("Releases", "releases")]])
    d.line(8,
           (ts("Hook tasks", "Webhook deliveries Gitea has queued (gitea_hooktasks). Growth that does not drain "
               "means a webhook receiver (Argo CD, Tekton triggers) is unreachable.",
               [T(f'max(gitea_hooktasks{{{G}}})', "hook tasks")], decimals=0), 8),
           (ts("Gitea CPU & memory", "Gitea process CPU and resident memory.",
               [T(f'sum(rate(process_cpu_seconds_total{{{G}}}[$__rate_interval]))', "cpu"),
                T(f'sum(process_resident_memory_bytes{{{G}}})', "rss")],
               unit="cores", overrides=[{"matcher": {"id": "byName", "options": "rss"},
                                         "properties": [{"id": "unit", "value": "bytes"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Content", "Issues (open / closed), comments and stars over time.",
               [T(f'max(gitea_issues_open{{{G}}})', "open issues"), T(f'max(gitea_issues_closed{{{G}}})', "closed issues"),
                T(f'max(gitea_comments{{{G}}})', "comments"), T(f'max(gitea_stars{{{G}}})', "stars")], decimals=0), 8))
    d.line(9, (component_logs("Argo CD & Gitea errors (Loki)", "Errors from Argo CD and Gitea.",
                              'namespace="adhar-system", pod=~"argo-cd-.+|gitea-.+"', pattern='level=(error|fatal)|(?i)\\\\berror\\\\b'), 24))
    return d


# =============================================================================================
# CI & Artifacts
# =============================================================================================

def ci():
    TK = 'job="adhar-system-controller"'
    TT = 'job="tekton-triggers-controller"'
    NX = 'job="nexus"'
    d = Dash(UIDS["ci"], "Delivery / CI & Artifacts",
             "The paved-road CI: Tekton pipelines and tasks (success rate, duration by pipeline and task, queueing, "
             "pod start latency), Tekton Triggers and Argo Events (what starts a run), and the artifact stores the "
             "runs publish to — Harbor (images, built by kpack) and Nexus (Maven / npm / PyPI). Replaces the Tekton "
             "Overview, Argo Events, Harbor and Nexus dashboards.",
             tags("Delivery", "tekton", "ci"),
             [ds_var(),
              qvar("pipeline", "Pipeline", f'label_values(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{TK}}}, pipeline)',
                   all_value=".*")],
             links=links("Delivery", ("GitOps & promotion", "gitops"), ("Developer platform", "dev-platform")),
             collapse=1)
    PL = f'{TK}, pipeline=~"$pipeline"'

    d.row("CI at a glance")
    d.line(4,
           (stat("Pipeline runs (range)", "PipelineRuns finished in the time range.",
                 [f'sum(increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}}}[$__range]))'],
                 color_mode="value", decimals=0), 3),
           (stat("Success rate (range)", "Share of finished PipelineRuns that succeeded.",
                 [f'100 * sum(increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}, status="success"}}[$__range])) / '
                  f'sum(increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}}}[$__range]))'],
                 unit="percent", th=steps((RED, None), (AMBER, 80), (GREEN, 95)), decimals=0, no_value="no runs"), 3),
           (stat("Running now", "PipelineRuns and TaskRuns executing right now.",
                 [T(f'max(tekton_pipelines_controller_running_pipelineruns{{{TK}}})', "pipelines"),
                  T(f'max(tekton_pipelines_controller_running_taskruns{{{TK}}})', "tasks")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("Throttled TaskRuns", "TaskRuns that cannot start because of a ResourceQuota or because no node "
                 "has room — a CI backlog that looks like slow builds.",
                 [f'max(tekton_pipelines_controller_running_taskruns_throttled_by_quota{{{TK}}}) + max(tekton_pipelines_controller_running_taskruns_throttled_by_node{{{TK}}})'],
                 th=OK_WARN_BAD), 3),
           (stat("p95 pipeline duration", "95th percentile PipelineRun duration over the range.",
                 [f'histogram_quantile(0.95, sum by (le) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_bucket{{{PL}}}[$__range])))'],
                 unit="s", color_mode="value", no_value="no runs"), 3),
           (pods_ready_stat("Harbor", "Harbor core, registry, jobservice, portal, Trivy and its database / cache.",
                            "harbor-.+"), 3),
           (up_stat("Nexus", "Nexus Repository metrics endpoint.", "nexus"), 3),
           (stat("Nexus blocked / throttled", "Requests Nexus blocked or throttled (license-free OSS limits and "
                 "rate limiting).",
                 [f'max(nexus_analytics_blocked_requests_count{{{NX}}}) + max(nexus_analytics_throttled_requests{{{NX}}})'],
                 th=OK_WARN_BAD, no_value="not scraped"), 3))

    d.row("Tekton pipelines")
    d.line(8,
           (ts("Pipeline runs by status", "Finished PipelineRuns per interval by status (bars).",
               [T(f'sum by (status) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}}}[$__rate_interval]))', "{{status}}")],
               draw="bars", stack=True, decimals=0, overrides=[ov_color("success", GREEN), ov_color("failed", RED),
                                                               ov_color("cancelled", GREY)]), 8),
           (ts("Pipeline duration p50 / p95 by pipeline", "Median and 95th percentile duration per pipeline.",
               [T(f'histogram_quantile(0.95, sum by (le, pipeline) (rate(tekton_pipelines_controller_pipelinerun_duration_seconds_bucket{{{PL}}}[1h])))', "{{pipeline}} p95"),
                T(f'histogram_quantile(0.5, sum by (le, pipeline) (rate(tekton_pipelines_controller_pipelinerun_duration_seconds_bucket{{{PL}}}[1h])))', "{{pipeline}} p50")],
               unit="s", overrides=[ov_dashed(".* p50")]), 8),
           (table("Pipelines", "Per pipeline over the time range: runs, failures, success rate and p95 duration.",
                  [f'sum by (pipeline) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}}}[$__range]))',
                   f'sum by (pipeline) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}, status="failed"}}[$__range]))',
                   f'100 * sum by (pipeline) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}, status="success"}}[$__range])) / sum by (pipeline) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_count{{{PL}}}[$__range]))',
                   f'histogram_quantile(0.95, sum by (le, pipeline) (increase(tekton_pipelines_controller_pipelinerun_duration_seconds_bucket{{{PL}}}[$__range])))'],
                  [("pipeline", "Pipeline", None, None), ("Value #A", "Runs", "short", {"decimals": 0}),
                   ("Value #B", "Failed", "short", {"decimals": 0, "cell": "color-background", "th": OK_BAD}),
                   ("Value #C", "Success", "percent", {"cell": "gauge", "th": steps((RED, None), (AMBER, 80), (GREEN, 95)), "min": 0, "max": 100}),
                   ("Value #D", "p95", "s", None)],
                  sort=("Runs", True)), 8))
    d.line(8,
           (ts("Slowest tasks (p95)", "95th percentile duration of the ten slowest tasks inside pipelines (build, "
               "scan, sign, test ...). The critical path of a pipeline is usually one of these.",
               [T(f'topk(10, histogram_quantile(0.95, sum by (le, pipeline, task) (rate(tekton_pipelines_controller_pipelinerun_taskrun_duration_seconds_bucket{{{PL}}}[1h]))))', "{{pipeline}}/{{task}}")],
               unit="s"), 8),
           (ts("Failing tasks", "TaskRuns inside pipelines that failed per interval, by task.",
               [T(f'sum by (task) (increase(tekton_pipelines_controller_pipelinerun_taskrun_duration_seconds_count{{{PL}, status="failed"}}[$__rate_interval])) > 0', "{{task}}")],
               draw="bars", decimals=0), 8),
           (ts("TaskRun pod start latency", "Time from TaskRun creation to its pod being scheduled, by task: image "
               "pulls, node scale-up and CI volume binding show up here.",
               [T(f'max by (task) (tekton_pipelines_controller_taskruns_pod_latency_milliseconds{{{TK}}}) / 1000', "{{task}}")],
               unit="s"), 8))
    d.line(8,
           (ts("Running & waiting", "Running PipelineRuns / TaskRuns, and runs waiting on pipeline / task resolution "
               "(the git resolver fetching from Gitea).",
               [T(f'max(tekton_pipelines_controller_running_pipelineruns{{{TK}}})', "pipelineruns"),
                T(f'max(tekton_pipelines_controller_running_taskruns{{{TK}}})', "taskruns"),
                T(f'max(tekton_pipelines_controller_running_pipelineruns_waiting_on_pipeline_resolution{{{TK}}}) + max(tekton_pipelines_controller_running_pipelineruns_waiting_on_task_resolution{{{TK}}})', "waiting on resolution")],
               decimals=0), 8),
           (ts("Controller work queue & reconcile", "Tekton controller work-queue depth and reconcile latency p99.",
               [T(f'sum by (reconciler) (tekton_pipelines_controller_work_queue_depth{{{TK}}})', "{{reconciler}} depth"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(tekton_pipelines_controller_reconcile_latency_bucket{{{TK}}}[$__rate_interval]))) / 1000', "reconcile p99 (s)")],
               overrides=[{"matcher": {"id": "byRegexp", "options": "reconcile.*"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Triggers", "Tekton Triggers objects (EventListeners, TriggerTemplates, bindings, interceptors) and "
               "the triggers controller's reconcile rate.",
               [T(f'max(controller_eventlistener_count{{{TT}}})', "event listeners"),
                T(f'max(controller_triggertemplate_count{{{TT}}})', "trigger templates"),
                T(f'max(controller_triggerbinding_count{{{TT}}}) + max(controller_clustertriggerbinding_count{{{TT}}})', "bindings"),
                T(f'sum(rate(controller_reconcile_count{{{TT}}}[$__rate_interval]))', "reconciles/s")],
               overrides=[{"matcher": {"id": "byName", "options": "reconciles/s"},
                           "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("Argo Events")
    AE = 'job="argo-events-controller-manager"'
    d.line(8,
           (ts("Events received & triggers fired", "Events EventSources receive and Sensor triggers fired per second "
               "(the argo_events_* metrics come from the EventSource / Sensor pods' own endpoints).",
               [T('sum by (eventsource_name) (rate(argo_events_events_sent_total[$__rate_interval]))', "{{eventsource_name}} sent"),
                T('sum by (sensor_name) (rate(argo_events_action_triggered_total[$__rate_interval]))', "{{sensor_name}} triggered")],
               unit="ops"), 8),
           (ts("Failures", "Events that failed to send, event processing failures and failed trigger actions.",
               [T('sum by (eventsource_name) (rate(argo_events_events_sent_failed_total[$__rate_interval]))', "{{eventsource_name}} send failed"),
                T('sum by (eventsource_name) (rate(argo_events_events_processing_failed_total[$__rate_interval]))', "{{eventsource_name}} processing failed"),
                T('sum by (sensor_name) (rate(argo_events_action_failed_total[$__rate_interval]))', "{{sensor_name}} action failed")],
               unit="ops", th=steps((GREEN, None), (RED, 0.01))), 8),
           (ts("Controller reconciles", "Argo Events controller reconciles per second by controller and result.",
               [T(f'sum by (controller, result) (rate(controller_runtime_reconcile_total{{{AE}}}[$__rate_interval]))', "{{controller}} {{result}}")],
               unit="ops", overrides=[ov_color(".* error", RED)]), 8))

    d.row("Harbor & kpack (images)")
    d.line(9, (pods_table("Harbor & kpack pods", "Harbor's components and the kpack build controller. Harbor's own "
                          "Prometheus exporter is not enabled, so its health is read from its pods.",
                          "harbor-.+|kpack-.+|buildpack.*"), 24))
    d.line(8, *[(p, 8) for p in pods_resources("harbor-.+")])
    d.line(9, (component_logs("Harbor errors (Loki)", "Errors from Harbor core, registry and jobservice — "
                              "'unauthorized' and 'blob unknown' here are failed pushes from CI.",
                              'namespace="adhar-system", pod=~"harbor-(core|registry|jobservice).+"'), 24))

    d.row("Nexus Repository")
    fmts = ("maven2", "npm", "pypi", "docker", "helm", "raw")
    d.line(8,
           (ts("Bytes downloaded by format", "Bytes served per second by repository format.",
               [T(f'sum(rate(bytes_downloaded_by_format_{f}{{{NX}}}[$__rate_interval]))', f) for f in fmts],
               unit="Bps"), 8),
           (ts("Bytes uploaded by format", "Bytes published per second by repository format (library releases, "
               "CI publishes).",
               [T(f'sum(rate(bytes_uploaded_by_format_{f}{{{NX}}}[$__rate_interval]))', f) for f in fmts],
               unit="Bps"), 8),
           (ts("Repositories, components & uptime", "Repositories (total / proxy), stored components, and requests "
               "blocked or throttled.",
               [T(f'max(nexus_analytics_repository_total_count{{{NX}}})', "repositories"),
                T(f'max(nexus_analytics_repository_proxy_count{{{NX}}})', "proxy repositories"),
                T(f'max(nexus_analytics_component_total_count{{{NX}}})', "components"),
                T(f'max(nexus_analytics_blocked_requests_count{{{NX}}})', "blocked requests")],
               decimals=0, overrides=[ov_color("blocked requests", RED)]), 8))
    d.line(8,
           (ts("Nexus JVM heap", "Heap used against its maximum. Nexus runs on the JVM; a heap pinned near max is "
               "GC thrash and slow builds.",
               [T(f'sum(jvm_memory_heap_used{{{NX}}})', "used"),
                T(f'sum(jvm_memory_heap_max{{{NX}}})', "max"),
                T(f'sum(rate(jvm_garbage_collectors_G1_Old_Generation_time{{{NX}}}[$__rate_interval])) / 1000', "old-gen GC s/s")],
               unit="bytes", overrides=[ov_dashed("max", RED),
                                        {"matcher": {"id": "byName", "options": "old-gen GC s/s"},
                                         "properties": [{"id": "unit", "value": "percentunit"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Log events", "Nexus log events per second by level (error drawn red).",
               [T(f'sum(rate(metrics_error_total{{{NX}}}[$__rate_interval]))', "error"),
                T(f'sum(rate(metrics_warn_total{{{NX}}}[$__rate_interval]))', "warn")],
               unit="ops", overrides=[ov_color("error", RED), ov_color("warn", AMBER)]), 8),
           (ts("Read-only mode", "Whether Nexus is frozen read-only (backup or upgrade); publishes fail while it is.",
               [T(f'max(readonly_enabled{{{NX}}})', "read-only")], decimals=0, max=1), 8))
    d.line(8, *[(p, 8) for p in pods_resources("nexus.+")])
    return d


# =============================================================================================
# Developer Platform
# =============================================================================================

def dev_platform():
    XP = 'job="crossplane"'
    CD = 'job="coder"'
    d = Dash(UIDS["dev-platform"], "Delivery / Developer Platform",
             "The self-service control plane and the developer tools around it: Crossplane (compositions, managed "
             "resources, composition functions, reconcile health), Coder cloud development environments, and the "
             "Dapr control plane applications use. Replaces the Crossplane, Coder and Dapr (system services + "
             "sidecar) dashboards.",
             tags("Delivery", "crossplane", "coder", "dapr"),
             [ds_var()],
             links=links("Delivery", ("GitOps & promotion", "gitops"), ("CI & artifacts", "ci")),
             collapse=1)

    d.row("Platform at a glance")
    d.line(4,
           (pods_ready_stat("Crossplane", "Crossplane core, RBAC manager, providers and functions.",
                            "crossplane.*|provider-.+|function-.+|upbound-.+"), 3),
           (stat("Managed resources ready", "Managed resources reporting Ready (crossplane_managed_resource_*).",
                 [T(f'sum(crossplane_managed_resource_ready{{{XP}}})', "ready"),
                  T(f'sum(crossplane_managed_resource_exists{{{XP}}})', "exist")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("Reconcile errors / s", "Crossplane reconcile errors per second (compositions, XRDs, packages, "
                 "managed resources).",
                 [f'sum(rate(controller_runtime_reconcile_errors_total{{{XP}}}[5m]))'], unit="ops",
                 th=steps((GREEN, None), (AMBER, 0.05), (RED, 0.5)), decimals=2), 3),
           (stat("Function errors / s", "Composition function calls that did not return OK.",
                 [f'sum(rate(function_run_function_response_total{{{XP}, grpc_code!="OK"}}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (RED, 0.01)), decimals=2), 3),
           (stat("Work queue backlog", "Items waiting in Crossplane's work queues.",
                 [f'sum(workqueue_depth{{{XP}}})'], th=steps((GREEN, None), (AMBER, 50), (RED, 500))), 3),
           (pods_ready_stat("Coder", "Coder control plane (coderd).", "coder-.+"), 3),
           (up_stat("Coder metrics", "coderd's Prometheus endpoint.", "coder"), 3),
           (pods_ready_stat("Dapr", "Dapr operator, placement, sentry, scheduler and sidecar injector.", "dapr-.+"), 3))

    d.row("Crossplane: reconciliation")
    d.line(8, *[(p, 6) for p in controller_runtime("crossplane", what="Crossplane")])
    d.line(8,
           (ts("Managed resources: exist / ready / synced", "Managed resources Crossplane tracks, and how many are "
               "Ready and Synced, by kind.",
               [T(f'sum by (gvk) (crossplane_managed_resource_exists{{{XP}}})', "{{gvk}} exist"),
                T(f'sum by (gvk) (crossplane_managed_resource_ready{{{XP}}})', "{{gvk}} ready"),
                T(f'sum by (gvk) (crossplane_managed_resource_synced{{{XP}}})', "{{gvk}} synced")],
               decimals=0, overrides=[ov_dashed(".* exist")]), 8),
           (ts("Time to first readiness p95", "How long new managed resources take to become Ready (cloud API "
               "latency + provider queueing).",
               [T(f'histogram_quantile(0.95, sum by (le, gvk) (rate(crossplane_managed_resource_first_time_to_readiness_seconds_bucket{{{XP}}}[1h])))', "{{gvk}}")],
               unit="s"), 8),
           (ts("Circuit breaker & watches", "Watch-triggered reconciles the circuit breaker allowed / dropped (a "
               "resource fighting another controller trips it), and dynamic watches started.",
               [T(f'sum by (state) (rate(circuit_breaker_events_total{{{XP}}}[$__rate_interval]))', "breaker {{state}}"),
                T(f'sum(rate(engine_watches_started_total{{{XP}}}[$__rate_interval]))', "watches started")],
               unit="ops"), 8))

    d.row("Crossplane: composition functions")
    d.line(8,
           (ts("Function calls by function", "RunFunction calls per second per composition function.",
               [T(f'sum by (function_name) (rate(function_run_function_request_total{{{XP}}}[$__rate_interval]))', "{{function_name}}")],
               unit="ops"), 8),
           (ts("Function latency p99", "99th percentile RunFunction latency per function. A slow function slows every "
               "XR that uses its composition.",
               [T(f'histogram_quantile(0.99, sum by (le, function_name) (rate(function_run_function_seconds_bucket{{{XP}}}[$__rate_interval])))', "{{function_name}}")],
               unit="s"), 8),
           (ts("Function results by code & severity", "Responses by gRPC code and the most severe result the function "
               "reported (Fatal results stop the composition).",
               [T(f'sum by (grpc_code, result_severity) (rate(function_run_function_response_total{{{XP}}}[$__rate_interval]))', "{{grpc_code}} {{result_severity}}")],
               unit="ops", overrides=[ov_color(".*(Fatal|Unavailable|Internal).*", RED), ov_color(".*Warning.*", AMBER)]), 8))
    d.line(8, *[(p, 8) for p in go_process("crossplane")])
    d.line(9, (pods_table("Crossplane pods", "Core, RBAC manager, providers (AWS / Azure / GCP / kubernetes / "
                          "helm) and functions.", "crossplane.*|provider-.+|function-.+|upbound-.+"), 24))

    d.row("Coder (cloud development environments)")
    d.line(8,
           (ts("Workspaces & agents", "Workspace agents connected and running workspaces by status.",
               [T(f'sum(coderd_agents_up{{{CD}}})', "agents up"),
                T(f'sum by (status) (coderd_workspace_latest_build_status{{{CD}}})', "builds {{status}}")],
               decimals=0), 8),
           (ts("Sessions by client", "Active sessions per IDE / client (VS Code, JetBrains, SSH, web terminal).",
               [T(f'sum(coderd_agentstats_session_count_vscode{{{CD}}})', "VS Code"),
                T(f'sum(coderd_agentstats_session_count_jetbrains{{{CD}}})', "JetBrains"),
                T(f'sum(coderd_agentstats_session_count_ssh{{{CD}}})', "SSH"),
                T(f'sum(coderd_agentstats_session_count_reconnecting_pty{{{CD}}})', "terminal")],
               stack=True, decimals=0), 8),
           (ts("API requests & latency", "coderd API requests per second by code and the 99th percentile latency.",
               [T(f'sum by (code) (rate(coderd_api_requests_processed_total{{{CD}}}[$__rate_interval]))', "{{code}}"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(coderd_api_request_latencies_seconds_bucket{{{CD}}}[$__rate_interval])))', "p99")],
               unit="reqps", overrides=[ov_color("5..", RED),
                                        {"matcher": {"id": "byName", "options": "p99"},
                                         "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(8, *[(p, 8) for p in pods_resources("coder-.+")])

    d.row("Dapr control plane")
    d.line(8,
           (ts("Sidecar injections", "Sidecar injection requests per second handled by the injector.",
               [T('sum(rate(dapr_injector_sidecar_injection_requests_total{job="sidecar-injector"}[$__rate_interval]))', "requests")],
               unit="ops"), 6),
           (timeline("Placement leadership", "Whether a placement replica is the leader (Raft). No leader = actors "
                     "cannot be placed.",
                     [T('max by (pod) (dapr_placement_leader_status{job="placement"})', "{{pod}}")],
                     vmap((0, "follower", GREY), (1, "leader", GREEN))), 6),
           (ts("Sentry certificates", "Workload certificate sign requests vs successes (mTLS between sidecars).",
               [T('sum(rate(dapr_sentry_cert_sign_request_received_total{job="sentry"}[$__rate_interval]))', "requests"),
                T('sum(rate(dapr_sentry_cert_sign_success_total{job="sentry"}[$__rate_interval]))', "signed")],
               unit="ops"), 6),
           (stat("Issuer certificate expires in", "Days until Dapr's mTLS issuer certificate expires. When it does, "
                 "every sidecar-to-sidecar call fails.",
                 ['min(dapr_sentry_issuercert_expiry_timestamp{job="sentry"} - time()) / 86400'],
                 unit="d", th=steps((RED, None), (AMBER, 30), (GREEN, 90)), decimals=0), 6))
    d.line(8, *[(p, 8) for p in pods_resources("dapr-.+")])
    return d


DASHBOARDS = [
    ("delivery-gitops", "Delivery", gitops),
    ("delivery-ci", "Delivery", ci),
    ("delivery-dev-platform", "Delivery", dev_platform),
]
