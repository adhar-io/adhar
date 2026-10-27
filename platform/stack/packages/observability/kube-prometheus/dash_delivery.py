"""Delivery folder: four dashboards in place of fifteen.

  GitOps & Promotion     Argo CD (applications, syncs, reconciliation, repo server, cluster
                         cache), Argo Rollouts, Kargo and Gitea — how a commit becomes a
                         running release.
  CI & Artifacts         Tekton pipelines / tasks / triggers, Argo Events, the image and
                         package registries (Harbor, Nexus) and kpack builds.
  Developer Platform     Crossplane (managed resources, compositions, functions), Coder
                         workspaces and a summary of the Dapr control plane.
  Dapr                   The Dapr runtime end to end: its control plane, every sidecar by
                         namespace and app-id, service invocation, pub/sub, state, actors,
                         workflows, resiliency, mTLS and the dapr-state store.

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
           # The pods-ready tile above read 5/5 green for the 35 hours the scheduler had no
           # etcd leader: Ready does not mean elected.
           (_linked(stat("Scheduler leader", "Whether the Dapr scheduler's embedded etcd has a leader. Without one "
                         "the Jobs API, actor reminders and every Workflow are stopped while the pod stays Ready. "
                         "The Dapr dashboard (panel menu) has the whole runtime.",
                         ['max(etcd_server_has_leader{job="scheduler"})'], mappings=DAPR_LEADER,
                         th=steps((RED, None), (GREEN, 1)), no_value="not scraped"),
                    "Dapr", UIDS["dapr"]), 4),
           (ts("Sidecar injections", "Sidecar injection requests per second handled by the injector.",
               [T('sum(rate(dapr_injector_sidecar_injection_requests_total{job="sidecar-injector"}[$__rate_interval]))', "requests")],
               unit="ops"), 5),
           (timeline("Placement leadership", "Whether a placement replica is the leader (Raft). No leader = actors "
                     "cannot be placed.",
                     [T('max by (pod) (dapr_placement_leader_status{job="placement"})', "{{pod}}")],
                     vmap((0, "follower", GREY), (1, "leader", GREEN))), 5),
           (ts("Sentry certificates", "Workload certificate sign requests vs successes (mTLS between sidecars).",
               [T('sum(rate(dapr_sentry_cert_sign_request_received_total{job="sentry"}[$__rate_interval]))', "requests"),
                T('sum(rate(dapr_sentry_cert_sign_success_total{job="sentry"}[$__rate_interval]))', "signed")],
               unit="ops"), 5),
           (_linked(stat("Issuer certificate expires in", "Days until Dapr's mTLS issuer certificate expires. When "
                         "it does, every sidecar-to-sidecar call fails.",
                         ['min(dapr_sentry_issuercert_expiry_timestamp{job="sentry"} - time()) / 86400'],
                         unit="d", th=steps((RED, None), (AMBER, 30), (GREEN, 90)), decimals=0),
                    "Dapr", UIDS["dapr"]), 5))
    d.line(8, *[(p, 8) for p in pods_resources("dapr-.+")])
    return d


# =============================================================================================
# Dapr
# =============================================================================================

# What application/dapr/manifests/servicemonitor.yaml names things: its PodMonitor
# dapr-sidecars scrapes every injected daprd (and copies the pod's dapr.io/app-id onto
# every series as app_id), its ServiceMonitor names each control-plane job after the
# component (operator, placement, scheduler, sentry, sidecar-injector), and dapr-state
# is scraped as job dapr-state.
DAPR_SIDECARS = 'job="adhar-system/dapr-sidecars"'
DAPR_CP_PODS = "dapr-(operator|placement-server|scheduler-server|sentry|sidecar-injector).*"
DAPR_LEADER = vmap((0, "NO LEADER", RED), (1, "leader", GREEN))


def _linked(panel, title, uid):
    """The panel with a link (its corner menu) to another built dashboard, keeping the time range."""
    panel["links"] = [{"title": title, "url": f"/d/{uid}?${{__url_time_range}}", "targetBlank": False}]
    return panel


def dapr():
    """Daprd publishes through OpenCensus: counters keep their view name (no _total added),
    histograms are in milliseconds, and most series appear only once something happened
    — an idle sidecar draws empty panels, not zeros. Names are Dapr 1.18.3's
    (TestDaprPanelsAndRulesUseMetricsDaprPublishes)."""
    S = f'{DAPR_SIDECARS}, namespace=~"$namespace", app_id=~"$app_id"'
    ST = 'job="dapr-state"'

    def rate(metric, by, extra=""):
        sel = f"{S}, {extra}" if extra else S
        return f'sum by ({by}) (rate({metric}{{{sel}}}[$__rate_interval]))'

    def p95(metric, by, extra="", q="0.95"):
        sel = f"{S}, {extra}" if extra else S
        return f'histogram_quantile({q}, sum by (le, {by}) (rate({metric}{{{sel}}}[$__rate_interval])))'

    def share(metric, bad, by):
        """The share of `metric` matching `bad` (0 when nothing failed, absent when idle)."""
        return f"({rate(metric, by, bad)} or {rate(metric, by)} * 0) / {rate(metric, by)}"

    d = Dash(UIDS["dapr"], "Delivery / Dapr",
             "The Dapr runtime end to end. Its control plane in adhar-system (the scheduler and its embedded etcd, "
             "placement, sentry, the sidecar injector, the operator), then every sidecar by namespace and app-id: the "
             "Dapr API, calls into the app, service invocation between apps, pub/sub, state, configuration, secrets, "
             "bindings, actors, workflows, resiliency and mTLS — and the dapr-state store behind every environment's "
             "components. Sidecar panels stay empty until an app runs one; application/dapr/README.md says how an "
             "app opts in.",
             tags("Delivery", "dapr"),
             [ds_var(),
              qvar("namespace", "Namespace", f'label_values(up{{{DAPR_SIDECARS}}}, namespace)', all_value=".*"),
              qvar("app_id", "App ID", f'label_values(up{{{DAPR_SIDECARS}, namespace=~"$namespace"}}, app_id)',
                   all_value=".*")],
             links=links("Delivery"), collapse=1)

    d.row("Dapr at a glance")
    d.line(4,
           (pods_ready_stat("Control plane", "Dapr operator, placement, scheduler, sentry and sidecar injector.",
                            DAPR_CP_PODS), 3),
           (stat("Scheduler leader", "Whether the scheduler's embedded etcd has a leader (etcd_server_has_leader). "
                 "Without one the Jobs API, actor reminders and every Workflow are stopped, while the pod stays Ready "
                 "(DaprSchedulerNoLeader).",
                 ['max(etcd_server_has_leader{job="scheduler"})'], mappings=DAPR_LEADER,
                 th=steps((RED, None), (GREEN, 1)), no_value="not scraped"), 3),
           (stat("Placement leader", "Whether a placement replica is the Raft leader. Without one no actor can be "
                 "placed, so actor calls and workflows fail (DaprPlacementNoLeader).",
                 ['max(dapr_placement_leader_status{job="placement"})'], mappings=DAPR_LEADER,
                 th=steps((RED, None), (GREEN, 1)), no_value="not scraped"), 3),
           (stat("Sidecars up", "daprd sidecars Prometheus scrapes in every namespace (pods the injector labelled "
                 "dapr.io/sidecar-injected), up / total.",
                 [T(f'sum(up{{{DAPR_SIDECARS}}})', "up"), T(f'count(up{{{DAPR_SIDECARS}}})', "total")],
                 color_mode="value", text_mode="value_and_name", no_value="no sidecars"), 3),
           (stat("dapr-state", "The standalone Valkey behind every environment's state, locks, configuration and "
                 "(without Kafka) pub/sub, from its redis_exporter (DaprStateStoreDown).",
                 [f'max(redis_up{{{ST}}})'], mappings=vmap((0, "DOWN", RED), (1, "up", GREEN)),
                 th=steps((RED, None), (GREEN, 1)), no_value="not scraped"), 3),
           (stat("Issuer certificate", "Days until Dapr's mTLS issuer certificate expires; after it every "
                 "sidecar-to-sidecar call fails (DaprIssuerCertExpiring fires under 30).",
                 ['min(dapr_sentry_issuercert_expiry_timestamp{job="sentry"} - time()) / 86400'],
                 unit="d", th=steps((RED, None), (AMBER, 30), (GREEN, 90)), decimals=0, no_value="not scraped"), 3),
           (stat("Component init failures (1h)", "Components that failed to initialise in a sidecar in the last hour "
                 "(DaprComponentInitFailed). The sidecar restarts on a failed component unless it sets ignoreErrors.",
                 ['sum(increase(dapr_runtime_component_init_fail_total[1h])) or vector(0)'], th=OK_BAD, decimals=0), 3),
           (stat("Open circuit breakers", "Resiliency circuit breakers that are open right now: calls to their target "
                 "fail fast (DaprCircuitBreakerOpen).",
                 ['count(dapr_resiliency_cb_state{status="open"} == 1) or vector(0)'], th=OK_BAD), 3))

    d.row("Control plane")
    d.line(8,
           (timeline("Scheduler etcd leader", "Per scheduler pod: whether its embedded etcd has a leader. A one-member "
                     "scheduler without one has a data directory that still records an older cluster — "
                     "application/dapr/README.md, \"Recovering a leaderless scheduler\".",
                     [T('max by (pod) (etcd_server_has_leader{job="scheduler"})', "{{pod}}")],
                     vmap((0, "no leader", RED), (1, "leader", GREEN))), 6),
           (ts("Sidecars connected to the scheduler", "daprd sidecars holding a stream to the scheduler, which hands "
               "them their jobs, actor reminders and workflow timers when they fall due.",
               [T('sum(dapr_scheduler_sidecars_connected{job="scheduler"})', "connected")], decimals=0), 6),
           (ts("Scheduler jobs / s", "Jobs (Jobs API, actor reminders, workflow timers) created, triggered, failed and "
               "undelivered per second, by type. Undelivered: no connected sidecar could take the trigger.",
               [T('sum by (type) (rate(dapr_scheduler_jobs_created_total{job="scheduler"}[$__rate_interval]))', "created {{type}}"),
                T('sum by (type) (rate(dapr_scheduler_jobs_triggered_total{job="scheduler"}[$__rate_interval]))', "triggered {{type}}"),
                T('sum by (type) (rate(dapr_scheduler_jobs_failed_total{job="scheduler"}[$__rate_interval]))', "failed {{type}}"),
                T('sum by (type) (rate(dapr_scheduler_jobs_undelivered_total{job="scheduler"}[$__rate_interval]))', "undelivered {{type}}")],
               unit="ops", overrides=[ov_color("failed.*", RED), ov_color("undelivered.*", AMBER)]), 6),
           (ts("Trigger latency p95", "95th percentile time from a job falling due to its delivery to a sidecar, by "
               "type.",
               [T('histogram_quantile(0.95, sum by (le, type) (rate(dapr_scheduler_trigger_latency_bucket{job="scheduler"}[$__rate_interval])))', "{{type}}")],
               unit="ms"), 6))
    d.line(8,
           (timeline("Placement leadership", "Whether a placement replica is the Raft leader. No leader: no actor can "
                     "be placed.",
                     [T('max by (pod) (dapr_placement_leader_status{job="placement"})', "{{pod}}")],
                     vmap((0, "follower", GREY), (1, "leader", GREEN))), 6),
           (ts("Placement: hosts per namespace", "Sidecars reporting to placement, and those hosting actor types, per "
               "namespace.",
               [T('sum by (host_namespace) (dapr_placement_runtimes_total{job="placement"})', "{{host_namespace}} hosts"),
                T('sum by (host_namespace) (dapr_placement_actor_runtimes_total{job="placement"})', "{{host_namespace}} actor hosts")],
               decimals=0, overrides=[ov_dashed(".* actor hosts")]), 6),
           (ts("Sentry certificates / s", "Workload certificate requests, signatures and failures (by reason). Every "
               "sidecar renews its certificate within its 24h TTL.",
               [T('sum(rate(dapr_sentry_cert_sign_request_received_total{job="sentry"}[$__rate_interval]))', "requests"),
                T('sum(rate(dapr_sentry_cert_sign_success_total{job="sentry"}[$__rate_interval]))', "signed"),
                T('sum by (reason) (rate(dapr_sentry_cert_sign_failure_total{job="sentry"}[$__rate_interval]))', "failed {{reason}}")],
               unit="ops", overrides=[ov_color("failed.*", RED)]), 6),
           (ts("Sidecar injections / s", "Pod admissions the injector saw, the sidecars it injected and the ones it "
               "failed to inject. A failure is silent for the pod (the webhook fails open): it starts without daprd.",
               [T('sum(rate(dapr_injector_sidecar_injection_requests_total{job="sidecar-injector"}[$__rate_interval]))', "requests"),
                T('sum(rate(dapr_injector_sidecar_injection_succeeded_total{job="sidecar-injector"}[$__rate_interval]))', "injected"),
                T('sum by (reason) (rate(dapr_injector_sidecar_injection_failed_total{job="sidecar-injector"}[$__rate_interval]))', "failed {{reason}}")],
               unit="ops", overrides=[ov_color("failed.*", RED)]), 6))
    d.line(8,
           (ts("Scheduler etcd: proposals", "Raft proposals committed per second, failed per second (red) and "
               "pending. Failures with no leader are the scheduler refusing every write.",
               [T('sum(rate(etcd_server_proposals_committed_total{job="scheduler"}[$__rate_interval]))', "committed"),
                T('sum(rate(etcd_server_proposals_failed_total{job="scheduler"}[$__rate_interval]))', "failed"),
                T('sum(etcd_server_proposals_pending{job="scheduler"})', "pending")],
               overrides=[ov_color("failed", RED)]), 8),
           (ts("Scheduler etcd: database size", "Size of the scheduler's etcd database on its volume (16Gi). Jobs and "
               "reminders live here.",
               [T('max by (pod) (etcd_mvcc_db_total_size_in_bytes{job="scheduler"})', "{{pod}}")], unit="bytes"), 8),
           (ts("Operator: app Services", "Services the operator created, updated and deleted for Dapr apps (one "
               "<app-id>-dapr Service per app).",
               [T('sum(rate(dapr_operator_service_created_total{job="operator"}[$__rate_interval]))', "created"),
                T('sum(rate(dapr_operator_service_updated_total{job="operator"}[$__rate_interval]))', "updated"),
                T('sum(rate(dapr_operator_service_deleted_total{job="operator"}[$__rate_interval]))', "deleted")],
               unit="ops"), 8))
    d.line(9, (pods_table("Control-plane pods", "Dapr's operator, placement, scheduler, sentry and sidecar injector "
                          "(dapr-state has its own row).", DAPR_CP_PODS), 24))
    d.line(8, *[(p, 8) for p in pods_resources(DAPR_CP_PODS)])

    d.row("Sidecars: $namespace / $app_id")
    d.line(8,
           (ts("Dapr API requests / s", "Requests each app made to its sidecar's Dapr API, over HTTP and gRPC.",
               [T(rate("dapr_http_server_request_count", "app_id"), "{{app_id}} http"),
                T(rate("dapr_grpc_io_server_completed_rpcs", "app_id"), "{{app_id}} grpc")],
               unit="reqps"), 8),
           (ts("Dapr API latency p50 / p95", "Dapr API latency per app as the sidecar measures it (HTTP), p95 solid "
               "and p50 dashed.",
               [T(p95("dapr_http_server_latency_bucket", "app_id"), "{{app_id}} p95"),
                T(p95("dapr_http_server_latency_bucket", "app_id", q="0.5"), "{{app_id}} p50")],
               unit="ms", overrides=[ov_dashed(".* p50")]), 8),
           (ts("Dapr API error ratio", "Share of each app's Dapr API calls that failed: HTTP 5xx and gRPC calls that did "
               "not return OK. 0 when nothing failed; empty when the app made no calls.",
               [T(share("dapr_http_server_request_count", 'status=~"5.."', "app_id"), "{{app_id}} http"),
                T(share("dapr_grpc_io_server_completed_rpcs", 'grpc_server_status!="OK"', "app_id"), "{{app_id}} grpc")],
               unit="percentunit", max=1), 8))
    d.line(8,
           (ts("Calls into the app / s", "Requests the sidecar made to its app (the app channel): service invocations "
               "arriving from other apps, pub/sub deliveries, bindings and actor calls, by status.",
               [T(rate("dapr_http_client_completed_count", "app_id, status"), "{{app_id}} {{status}}")],
               unit="reqps", overrides=[ov_color(".* 5..", RED)]), 8),
           (ts("App latency p95 (app channel)", "How long the app takes to answer its sidecar — the app's own time, "
               "as Dapr sees it.",
               [T(p95("dapr_http_client_roundtrip_latency_bucket", "app_id"), "{{app_id}}")], unit="ms"), 8),
           (ts("Dapr errors by code", "Errors the Dapr API returned, by Dapr error code (recordErrorCodes is on in the "
               "environments' adhar-dapr Configuration): which building block failed and why.",
               [T(rate("dapr_error_code_total", "app_id, error_code"), "{{app_id}} {{error_code}}")],
               unit="ops"), 8))
    d.line(8,
           (ts("Sidecar CPU", "CPU each app's daprd uses (its process_cpu_seconds_total).",
               [T(rate("process_cpu_seconds_total", "app_id"), "{{app_id}}")], unit="cores"), 8),
           (ts("Sidecar memory (RSS)", "Resident memory of each app's daprd.",
               [T(f'sum by (app_id) (process_resident_memory_bytes{{{S}}})', "{{app_id}}")], unit="bytes"), 8),
           (ts("Component initialisation", "Components each sidecar initialised and failed to initialise, by type "
               "(per interval). A failure stops the sidecar unless the component sets ignoreErrors.",
               [T(f'sum by (app_id, component) (increase(dapr_runtime_component_init_total{{{S}}}[$__rate_interval]))', "{{app_id}} {{component}}"),
                T(f'sum by (app_id, componentName, reason) (increase(dapr_runtime_component_init_fail_total{{{S}}}[$__rate_interval]))', "{{app_id}} FAILED {{componentName}} ({{reason}})")],
               draw="bars", decimals=0, overrides=[ov_color(".*FAILED.*", RED)]), 8))

    d.row("Service invocation")
    d.line(9, (table("Invocation edges (caller → callee)", "Every app-to-app call path seen in the last 5 minutes: "
                     "requests per second, 95th percentile round trip as the caller measures it, and the share of "
                     "responses that were not a success (HTTP 2xx, gRPC OK).",
                     [f'sum by (app_id, dst_app_id) (rate(dapr_runtime_service_invocation_req_sent_total{{{S}}}[5m]))',
                      f'max by (app_id, dst_app_id) (label_replace(histogram_quantile(0.95, sum by (app_id, src_app_id, le) '
                      f'(rate(dapr_runtime_service_invocation_res_recv_latency_ms_bucket{{{S}}}[5m]))), "dst_app_id", "$1", "src_app_id", "(.*)"))',
                      f'(sum by (app_id, dst_app_id) (label_replace(rate(dapr_runtime_service_invocation_res_recv_total{{{S}, status!~"0|2.."}}[5m]), "dst_app_id", "$1", "src_app_id", "(.*)")) '
                      f'or sum by (app_id, dst_app_id) (label_replace(rate(dapr_runtime_service_invocation_res_recv_total{{{S}}}[5m]), "dst_app_id", "$1", "src_app_id", "(.*)")) * 0) '
                      f'/ sum by (app_id, dst_app_id) (label_replace(rate(dapr_runtime_service_invocation_res_recv_total{{{S}}}[5m]), "dst_app_id", "$1", "src_app_id", "(.*)"))'],
                     [("app_id", "Caller", None, None), ("dst_app_id", "Callee", None, None),
                      ("Value #A", "Requests / s", "reqps", {"decimals": 2}),
                      ("Value #B", "p95", "ms", {"decimals": 1}),
                      ("Value #C", "Not OK", "percentunit",
                       {"cell": "color-background", "th": steps((GREEN, None), (AMBER, 0.01), (RED, 0.05))})],
                     sort=("Requests / s", True)), 24))
    d.line(8,
           (ts("Requests sent / s by edge", "Service invocations each app sent, by destination app.",
               [T(rate("dapr_runtime_service_invocation_req_sent_total", "app_id, dst_app_id"), "{{app_id}} → {{dst_app_id}}")],
               unit="reqps"), 12),
           (ts("Responses received by status", "Responses each caller received, by the callee and its status (HTTP "
               "code, or gRPC code with 0 = OK).",
               [T(rate("dapr_runtime_service_invocation_res_recv_total", "app_id, src_app_id, status"),
                  "{{app_id}} ← {{src_app_id}} {{status}}")],
               unit="reqps", overrides=[ov_color(".* 5..$", RED)]), 12))

    d.row("Pub/sub")
    d.line(8,
           (ts("Published / s", "Messages each app published, by pub/sub component and topic; failed publishes red. "
               "Topics are namespaced on the wire (the environments' consumerID is \"{namespace}\").",
               [T(rate("dapr_component_pubsub_egress_count", "app_id, component, topic, success"),
                  "{{app_id}} → {{component}}/{{topic}} {{success}}")],
               unit="ops", overrides=[ov_color(".* false", RED)]), 8),
           (ts("Delivered / s by outcome", "Messages delivered to each app, by topic and what the app answered: "
               "success, retry (amber, redelivered) or drop (red, gone — DaprPubsubDrops).",
               [T(rate("dapr_component_pubsub_ingress_count", "app_id, topic, process_status"),
                  "{{topic}} → {{app_id}} {{process_status}}")],
               unit="ops", overrides=[ov_color(".* retry", AMBER), ov_color(".* drop", RED)]), 8),
           (ts("App processing p95", "95th percentile time the app takes to handle a delivered message.",
               [T(p95("dapr_component_pubsub_ingress_latencies_bucket", "app_id, topic"), "{{topic}} → {{app_id}}")],
               unit="ms"), 8))
    d.line(8,
           (ts("Publish latency p95", "95th percentile time the broker takes to accept a publish.",
               [T(p95("dapr_component_pubsub_egress_latencies_bucket", "app_id, component"), "{{app_id}} → {{component}}")],
               unit="ms"), 8),
           (ts("Drops and retries", "Deliveries the app asked to retry or dropped, per interval.",
               [T(f'sum by (app_id, topic, process_status) (increase(dapr_component_pubsub_ingress_count{{{S}, '
                  f'process_status=~"drop|retry"}}[$__rate_interval]))', "{{topic}} → {{app_id}} {{process_status}}")],
               draw="bars", decimals=0, overrides=[ov_color(".* retry", AMBER), ov_color(".* drop", RED)]), 8),
           (ts("Kafka consumer lag (Dapr groups)", "Messages waiting per Dapr consumer group on the platform's Kafka. A "
               "Dapr app's group is <namespace>.<app-id>; empty where the environment's pub/sub is Redis streams.",
               [T(r'sum by (consumergroup, topic) (kafka_consumergroup_lag{consumergroup=~"(${namespace})\\.(${app_id})"})',
                  "{{consumergroup}} {{topic}}")], decimals=0), 8))

    d.row("State, configuration, secrets, locks & bindings")
    d.line(8,
           (ts("State operations / s", "State store operations per app, by operation; failed ones red. Actor and "
               "workflow state goes through the same store.",
               [T(rate("dapr_component_state_count", "app_id, operation, success"), "{{app_id}} {{operation}} {{success}}")],
               unit="ops", overrides=[ov_color(".* false", RED)]), 8),
           (ts("State latency p95", "95th percentile state store latency, by operation.",
               [T(p95("dapr_component_state_latencies_bucket", "operation"), "{{operation}}")], unit="ms"), 8),
           (ts("Configuration & secrets / s", "Configuration store and secret store calls per app, by operation; failed "
               "ones red.",
               [T(rate("dapr_component_configuration_count", "app_id, operation, success"), "{{app_id}} config {{operation}} {{success}}"),
                T(rate("dapr_component_secret_count", "app_id, operation, success"), "{{app_id}} secret {{operation}} {{success}}")],
               unit="ops", overrides=[ov_color(".* false", RED)]), 8))
    d.line(8,
           (ts("Lock errors", "Dapr publishes no lock-store metric; failed lock and unlock calls show here as Dapr "
               "error codes.",
               [T(rate("dapr_error_code_total", "app_id, error_code", 'error_code=~".*LOCK.*"'), "{{app_id}} {{error_code}}")],
               unit="ops"), 8),
           (ts("Bindings / s", "Input binding events delivered to each app and output binding calls it made; failed "
               "ones red.",
               [T(rate("dapr_component_input_binding_count", "app_id, component, success"), "in {{component}} → {{app_id}} {{success}}"),
                T(rate("dapr_component_output_binding_count", "app_id, component, operation, success"),
                  "out {{app_id}} → {{component}} {{operation}} {{success}}")],
               unit="ops", overrides=[ov_color(".* false", RED)]), 8),
           (ts("Jobs delivered to the app / s", "Jobs API triggers the scheduler handed to each app's sidecar, and the "
               "ones that failed (red).",
               [T(rate("dapr_component_job_success_count", "app_id"), "{{app_id}} delivered"),
                T(rate("dapr_component_job_failure_count", "app_id"), "{{app_id}} failed")],
               unit="ops", overrides=[ov_color(".* failed", RED)]), 8))

    d.row("Actors")
    d.line(8,
           (ts("Pending actor calls", "Calls waiting for an actor's turn-based lock, by actor type. A queue that only "
               "grows is one actor doing slow work while callers pile up.",
               [T(f'sum by (app_id, actor_type) (dapr_runtime_actor_pending_actor_calls{{{S}}})', "{{app_id}} {{actor_type}}")],
               decimals=0), 8),
           (ts("Timers & reminders fired / s", "Actor timers and reminders fired, by actor type; failed ones red.",
               [T(rate("dapr_runtime_actor_timers_fired_total", "app_id, actor_type, success"), "{{actor_type}} timer {{success}}"),
                T(rate("dapr_runtime_actor_reminders_fired_total", "app_id, actor_type, success"), "{{actor_type}} reminder {{success}}")],
               unit="ops", overrides=[ov_color(".* false", RED)]), 8),
           (ts("Deactivations & rebalancing", "Actors deactivated (idle or moved), failed deactivations (red) and "
               "placement rebalances, per second.",
               [T(rate("dapr_runtime_actor_deactivated_total", "app_id, actor_type"), "{{actor_type}} deactivated"),
                T(rate("dapr_runtime_actor_deactivated_failed_total", "app_id, actor_type"), "{{actor_type}} deactivation failed"),
                T(rate("dapr_runtime_actor_rebalanced_total", "app_id, actor_type"), "{{actor_type}} rebalanced")],
               unit="ops", overrides=[ov_color(".* failed", RED)]), 8))

    d.row("Workflows")
    d.line(8,
           (ts("Workflow executions / s", "Workflow executions per app, by workflow and outcome.",
               [T(rate("dapr_runtime_workflow_execution_count", "app_id, workflow_name, status"),
                  "{{app_id}} {{workflow_name}} {{status}}")],
               unit="ops", overrides=[ov_color(".* failed", RED)]), 8),
           (ts("Workflow execution p95", "95th percentile time a workflow takes from start to completion.",
               [T(p95("dapr_runtime_workflow_execution_latency_bucket", "workflow_name"), "{{workflow_name}}")],
               unit="ms"), 8),
           (ts("Workflow scheduling p95", "95th percentile delay between a workflow being scheduled and starting — "
               "the scheduler and the actor runtime, not the workflow's code.",
               [T(p95("dapr_runtime_workflow_scheduling_latency_bucket", "workflow_name"), "{{workflow_name}}")],
               unit="ms"), 8))
    d.line(8,
           (ts("Activities / s", "Activity executions per app, by activity and outcome.",
               [T(rate("dapr_runtime_workflow_activity_execution_count", "app_id, activity_name, status"),
                  "{{app_id}} {{activity_name}} {{status}}")],
               unit="ops", overrides=[ov_color(".* failed", RED)]), 8),
           (ts("Activity p95", "95th percentile time an activity takes to run.",
               [T(p95("dapr_runtime_workflow_activity_execution_latency_bucket", "activity_name"), "{{activity_name}}")],
               unit="ms"), 8),
           (ts("Workflow API operations / s", "Workflow management calls (start, get, raise event, terminate, purge) "
               "by outcome.",
               [T(rate("dapr_runtime_workflow_operation_count", "operation, status"), "{{operation}} {{status}}")],
               unit="ops", overrides=[ov_color(".* failed", RED)]), 8))

    d.row("Resiliency & circuit breakers")
    d.line(8,
           (timeline("Circuit breakers", "State of each circuit breaker per app and target: closed, half-open (testing "
                     "with one request) or open (failing fast).",
                     [T(f'max by (app_id, target) ((dapr_resiliency_cb_state{{{S}, status="open"}} * 2) '
                        f'or dapr_resiliency_cb_state{{{S}, status="half-open"}} '
                        f'or (dapr_resiliency_cb_state{{{S}, status="closed"}} * 0))', "{{app_id}} {{target}}")],
                     vmap((0, "closed", GREEN), (1, "half-open", AMBER), (2, "OPEN", RED))), 12),
           (ts("Policy activations / s", "Resiliency policies that acted (a retry, a timeout, a breaker changing "
               "state), by app, policy and target.",
               [T(rate("dapr_resiliency_activations_total", "app_id, policy, target"), "{{app_id}} {{policy}} {{target}}")],
               unit="ops"), 12))
    d.line(8,
           (ts("Policy executions / s", "Calls wrapped by a resiliency policy, by policy type and direction.",
               [T(rate("dapr_resiliency_count", "policy, flow_direction"), "{{policy}} {{flow_direction}}")],
               unit="ops"), 12),
           (table("Resiliencies loaded", "Which Resiliency resources each sidecar loaded at start. Every app in an "
                  "environment should list adhar-default (compositions/env/local.yaml).",
                  [f'max by (namespace, app_id, name) (dapr_resiliency_loaded{{{S}}})'],
                  [("namespace", "Namespace", None, None), ("app_id", "App ID", None, None),
                   ("name", "Resiliency", None, None), ("Value #A", "Loads", "short", {"decimals": 0})],
                  sort=("App ID", False)), 12))

    d.row("mTLS")
    d.line(8,
           (ts("Workload certificate rotations", "Sidecars rotating their workload certificate (24h TTL), and failed "
               "rotations (red) — a sidecar that cannot rotate loses mTLS when its certificate expires.",
               [T(f'sum by (app_id) (increase(dapr_runtime_mtls_workload_cert_rotated_total{{{S}}}[$__rate_interval]))', "{{app_id}} rotated"),
                T(f'sum by (app_id, reason) (increase(dapr_runtime_mtls_workload_cert_rotated_fail_total{{{S}}}[$__rate_interval]))', "{{app_id}} failed {{reason}}")],
               draw="bars", decimals=0, overrides=[ov_color(".* failed.*", RED)]), 8),
           (ts("mTLS initialisation", "Sidecars that set up mTLS at start, and the ones that failed to (red).",
               [T(f'sum by (app_id) (increase(dapr_runtime_mtls_init_total{{{S}}}[$__rate_interval]))', "{{app_id}} ok"),
                T(f'sum by (app_id, reason) (increase(dapr_runtime_mtls_init_fail_total{{{S}}}[$__rate_interval]))', "{{app_id}} failed {{reason}}")],
               draw="bars", decimals=0, overrides=[ov_color(".* failed.*", RED)]), 8),
           (ts("Sentry signing failures", "Certificate requests sentry refused, by reason (DaprCertSignFailures).",
               [T('sum by (reason) (rate(dapr_sentry_cert_sign_failure_total{job="sentry"}[$__rate_interval]))', "{{reason}}")],
               unit="ops", overrides=[ov_color(".*", RED)]), 8))

    d.row("dapr-state store")
    d.line(8,
           (ts("Memory vs maxmemory", "dapr-state's memory against its maxmemory (dashed). It runs noeviction: at the "
               "line every state write, lock and Redis-streams publish fails (DaprStateStoreMemoryHigh at 80%).",
               [T(f'max(redis_memory_used_bytes{{{ST}}})', "used"), T(f'max(redis_memory_max_bytes{{{ST}}})', "maxmemory")],
               unit="bytes", overrides=[ov_dashed("maxmemory", RED)]), 8),
           (ts("Keys per database", "Keys in each database: db0 the platform namespace, db1-3 dev/test/prod state, "
               "locks and streams, db9-11 their configuration.",
               [T(f'sum by (db) (redis_db_keys{{{ST}}})', "{{db}}")], decimals=0), 8),
           (ts("Commands / s & clients", "Commands the store processes per second, and connected clients (every "
               "sidecar holds connections per component).",
               [T(f'sum(rate(redis_commands_processed_total{{{ST}}}[$__rate_interval]))', "commands / s"),
                T(f'max(redis_connected_clients{{{ST}}})', "clients")],
               overrides=[{"matcher": {"id": "byName", "options": "clients"},
                           "properties": [{"id": "custom.axisPlacement", "value": "right"}, {"id": "decimals", "value": 0}]}]), 8))
    return d


DASHBOARDS = [
    ("delivery-gitops", "Delivery", gitops),
    ("delivery-ci", "Delivery", ci),
    ("delivery-dev-platform", "Delivery", dev_platform),
    ("delivery-dapr", "Delivery", dapr),
]
