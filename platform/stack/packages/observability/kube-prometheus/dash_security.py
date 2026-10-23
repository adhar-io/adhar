"""Security folder: three dashboards in place of thirteen.

  Policy & Compliance                 Kyverno (admission, policy results, background
                                      scans), PolicyReports and Trivy (vulnerabilities,
                                      misconfigurations, RBAC, exposed secrets, CIS).
  Runtime Threats                     Falco (rule matches, drops, the falcosidekick
                                      fan-out, the events themselves from Loki) and
                                      Tetragon (process / policy events, probe health).
  Identity, Secrets & Certificates    Keycloak, External Secrets, OpenBao and cert-manager.

Cosign and Kubescape dashboards were dropped with their packages (disabled in every
profile); falcosidekick's own Loki dashboard is folded into Runtime Threats.
"""
from dashlib import *  # noqa: F401,F403
from dash_common import (KSM, SYS, UIDS, component_logs, controller_runtime, go_process, links, pods_ready_stat,
                         pods_resources, pods_table, tags, up_stat)

KY = 'job=~"kyverno-.+-controller"'


# =============================================================================================
# Policy & Compliance
# =============================================================================================

def policy():
    PR = 'job="policy-reporter"'
    TV = 'job="trivy-operator", namespace=~"$namespace"'
    d = Dash(UIDS["policy"], "Security / Policy & Compliance",
             "What is allowed to run and how it measures up: Kyverno admission and background policy results, "
             "PolicyReports aggregated by Policy Reporter, and Trivy Operator's vulnerability, misconfiguration, "
             "RBAC, exposed-secret and CIS compliance reports for every running workload. Replaces the Kyverno, "
             "PolicyReports and Trivy Operator dashboards.",
             tags("Security", "kyverno", "trivy", "policy"),
             [ds_var(), qvar("namespace", "Namespace", 'label_values(trivy_image_vulnerabilities, namespace)')],
             links=links("Security", ("Runtime threats", "runtime-security"), ("Identity, secrets & certificates", "identity")),
             collapse=1)

    d.row("Compliance at a glance")
    d.line(4,
           (pods_ready_stat("Kyverno", "Kyverno admission, background, reports and cleanup controllers.", "kyverno-.+"), 3),
           (stat("Admission denials / s", "Admission requests Kyverno rejected (enforce policies).",
                 [f'sum(rate(kyverno_admission_requests_total{{{KY}, request_allowed="false"}}[5m])) or vector(0)'],
                 unit="reqps", th=steps((GREEN, None), (AMBER, 0.01), (RED, 1)), decimals=2), 3),
           (stat("Failing policy results", "Policy report results with status fail, across every namespaced and "
                 "cluster PolicyReport.",
                 [f'sum(policy_report_result{{{PR}, status="fail"}}) + (sum(cluster_policy_report_result{{{PR}, status="fail"}}) or vector(0))'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 100))), 3),
           (stat("Critical CVEs", "Critical vulnerabilities in running images (Trivy).",
                 [f'sum(trivy_image_vulnerabilities{{{TV}, severity="Critical"}})'], th=steps((GREEN, None), (AMBER, 1), (RED, 50))), 3),
           (stat("High CVEs", "High vulnerabilities in running images (Trivy).",
                 [f'sum(trivy_image_vulnerabilities{{{TV}, severity="High"}})'], th=steps((GREEN, None), (AMBER, 1), (RED, 200))), 3),
           (stat("Exposed secrets", "Secrets (keys, tokens) Trivy found baked into running images.",
                 [f'sum(trivy_image_exposedsecrets{{{TV}}})'], th=OK_WARN_BAD), 3),
           (stat("Critical misconfigurations", "Critical configuration-audit findings on workloads.",
                 [f'sum(trivy_resource_configaudits{{{TV}, severity="Critical"}})'], th=OK_WARN_BAD), 3),
           (stat("CIS controls failing", "Failing controls of the cluster compliance reports (CIS Kubernetes "
                 "benchmark, NSA hardening).",
                 ['sum(trivy_cluster_compliance{job="trivy-operator", status="Fail"})'], th=OK_WARN_BAD), 3))

    d.row("Kyverno: admission")
    d.line(8,
           (ts("Admission requests by result", "Admission reviews per second, allowed vs denied.",
               [T(f'sum by (request_allowed) (rate(kyverno_admission_requests_total{{{KY}}}[$__rate_interval]))', "allowed={{request_allowed}}")],
               unit="reqps", overrides=[ov_color("allowed=false", RED), ov_color("allowed=true", GREEN)]), 8),
           (ts("Admission by kind & operation (top 10)", "Which resources and operations go through Kyverno's "
               "webhooks most.",
               [T(f'topk(10, sum by (resource_kind, resource_request_operation) (rate(kyverno_admission_requests_total{{{KY}}}[$__rate_interval])))', "{{resource_kind}} {{resource_request_operation}}")],
               unit="reqps"), 8),
           (heatmap("Admission review latency", "Time Kyverno takes to answer an admission review. The API server "
                    "waits for it on every matching request, so this adds to every create / update.",
                    f'sum by (le) (increase(kyverno_admission_review_duration_seconds_bucket{{{KY}}}[$__rate_interval]))'), 8))

    d.row("Kyverno: policy results")
    d.line(8,
           (ts("Results by outcome", "Rule results per second (admission + background) by outcome.",
               [T(f'sum by (rule_result) (rate(kyverno_policy_results_total{{{KY}}}[$__rate_interval]))', "{{rule_result}}")],
               unit="ops", overrides=[ov_color("fail", RED), ov_color("pass", GREEN), ov_color("skip", GREY),
                                      ov_color("error", PURPLE)]), 8),
           (bargauge("Policies failing most (24h)", "Failed rule evaluations per policy over 24 hours (top 15).",
                     [T(f'topk(15, sum by (policy_name) (increase(kyverno_policy_results_total{{{KY}, rule_result="fail"}}[24h])))', "{{policy_name}}")],
                     unit="short", min=0, max=None, th=steps((AMBER, None), (RED, 100)), decimals=0), 8),
           (ts("Policy execution p99", "99th percentile time to evaluate a policy, by policy (top 10).",
               [T(f'topk(10, histogram_quantile(0.99, sum by (le, policy_name) (rate(kyverno_policy_execution_duration_seconds_bucket{{{KY}}}[$__rate_interval]))))', "{{policy_name}}")],
               unit="s"), 8))
    d.line(8,
           (table("Policies & rules", "Every policy rule Kyverno has loaded: type, mode (audit / enforce) and "
                  "background scanning.",
                  [f'max by (policy_name, rule_name, rule_type, policy_validation_mode, policy_background_mode) (kyverno_policy_rule_info_total{{{KY}}})'],
                  [("policy_name", "Policy", None, None), ("rule_name", "Rule", None, None),
                   ("rule_type", "Type", None, None),
                   ("policy_validation_mode", "Mode", None, {"cell": "color-text", "mappings": vmap(("enforce", "enforce", RED), ("audit", "audit", BLUE))}),
                   ("policy_background_mode", "Background", None, None)],
                  hide=("Value",)), 12),
           (ts("Policy changes & controller health", "Policies created / updated / deleted, reconciles of the "
               "reports and background controllers, and circuit-breaker trips (Kyverno shedding report load).",
               [T(f'sum by (policy_change_type) (increase(kyverno_policy_changes_total{{{KY}}}[$__rate_interval]))', "policy {{policy_change_type}}"),
                T(f'sum by (controller_name) (rate(kyverno_controller_reconcile_total{{{KY}}}[$__rate_interval]))', "{{controller_name}}"),
                T(f'sum by (circuit_name) (increase(kyverno_breaker_total{{{KY}}}[$__rate_interval]))', "breaker {{circuit_name}}")],
               overrides=[ov_color("breaker.*", RED)]), 12))
    d.line(8, *[(p, 8) for p in pods_resources("kyverno-.+")])

    d.row("Policy reports")
    d.line(8,
           (ts("Results by status", "PolicyReport results by status over time (namespaced + cluster reports).",
               [T(f'sum by (status) ({{__name__=~"policy_report_result|cluster_policy_report_result", {PR}}})', "{{status}}")],
               stack=True, decimals=0, overrides=[ov_color("fail", RED), ov_color("pass", GREEN), ov_color("warn", AMBER),
                                                  ov_color("error", PURPLE), ov_color("skip", GREY)]), 8),
           (bargauge("Failing results by namespace", "Failing PolicyReport results per namespace (top 15).",
                     [T(f'topk(15, sum by (namespace) (policy_report_result{{{PR}, status="fail"}}))', "{{namespace}}")],
                     unit="short", min=0, max=None, th=steps((AMBER, None), (RED, 50)), decimals=0), 8),
           (bargauge("Failing results by policy", "Failing results per policy, namespaced and cluster (top 15).",
                     [T(f'topk(15, sum by (policy) (policy_report_result{{{PR}, status="fail"}}))', "{{policy}}")],
                     unit="short", min=0, max=None, th=steps((AMBER, None), (RED, 50)), decimals=0), 8))
    d.line(8, (table("Failing results", "Each failing result: policy, rule, the resource and its severity / "
                     "category. Sorted by policy.",
                     [f'max by (namespace, policy, rule, kind, name, severity, category) (policy_report_result{{{PR}, status="fail"}})'],
                     [("namespace", "Namespace", None, None), ("policy", "Policy", None, None), ("rule", "Rule", None, None),
                      ("kind", "Kind", None, None), ("name", "Resource", None, None),
                      ("severity", "Severity", None, {"cell": "color-text", "mappings": vmap(
                          ("critical", "critical", RED), ("high", "high", "orange"), ("medium", "medium", AMBER), ("low", "low", BLUE))}),
                      ("category", "Category", None, None)],
                     sort=("Policy", False), hide=("Value",)), 24))

    d.row("Trivy: vulnerabilities & exposed secrets")
    sev = [ov_color("Critical", RED), ov_color("High", "orange"), ov_color("Medium", AMBER), ov_color("Low", BLUE),
           ov_color("Unknown", GREY)]
    d.line(8,
           (ts("Vulnerabilities by severity", "Vulnerabilities in running images over time by severity.",
               [T(f'sum by (severity) (trivy_image_vulnerabilities{{{TV}}})', "{{severity}}")],
               stack=True, decimals=0, overrides=sev), 8),
           (bargauge("Most vulnerable images (Critical + High)", "Images with the most Critical and High "
                     "vulnerabilities (top 15).",
                     [T(f'topk(15, sum by (image_repository, image_tag) (trivy_image_vulnerabilities{{{TV}, severity=~"Critical|High"}}))', "{{image_repository}}:{{image_tag}}")],
                     unit="short", min=0, max=None, th=steps((AMBER, None), (RED, 20)), decimals=0), 8),
           (bargauge("Vulnerabilities by namespace", "Critical + High vulnerabilities per namespace.",
                     [T(f'sum by (namespace) (trivy_image_vulnerabilities{{{TV}, severity=~"Critical|High"}})', "{{namespace}}")],
                     unit="short", min=0, max=None, th=steps((AMBER, None), (RED, 100)), decimals=0), 8))
    d.line(8, (table("Images", "Every scanned image with vulnerability counts per severity and exposed secrets.",
                     [f'sum by (image_registry, image_repository, image_tag) (trivy_image_vulnerabilities{{{TV}, severity="Critical"}})',
                      f'sum by (image_registry, image_repository, image_tag) (trivy_image_vulnerabilities{{{TV}, severity="High"}})',
                      f'sum by (image_registry, image_repository, image_tag) (trivy_image_vulnerabilities{{{TV}, severity="Medium"}})',
                      f'sum by (image_registry, image_repository, image_tag) (trivy_image_exposedsecrets{{{TV}}})'],
                     [("image_registry", "Registry", None, None), ("image_repository", "Repository", None, None),
                      ("image_tag", "Tag", None, None),
                      ("Value #A", "Critical", "short", {"cell": "color-background", "th": steps((GREEN, None), (RED, 1))}),
                      ("Value #B", "High", "short", {"cell": "color-background", "th": steps((GREEN, None), ("orange", 1))}),
                      ("Value #C", "Medium", "short", {"cell": "color-text", "th": steps((GREEN, None), (AMBER, 1))}),
                      ("Value #D", "Secrets", "short", {"cell": "color-background", "th": OK_BAD})],
                     sort=("Critical", True)), 24))

    d.row("Trivy: configuration, RBAC & compliance")
    d.line(8,
           (ts("Misconfigurations by severity", "Configuration-audit findings on workloads by severity.",
               [T(f'sum by (severity) (trivy_resource_configaudits{{{TV}}})', "{{severity}}")],
               stack=True, decimals=0, overrides=sev), 6),
           (ts("RBAC findings by severity", "Risky permissions in Roles and ClusterRoles (wildcards, secrets "
               "access, escalation).",
               [T('sum by (severity) ({__name__=~"trivy_role_rbacassessments|trivy_clusterrole_clusterrbacassessments", job="trivy-operator"})', "{{severity}}")],
               stack=True, decimals=0, overrides=sev), 6),
           (ts("Infrastructure findings", "Findings on control-plane components (kube-apiserver, etcd, kubelet "
               "configuration).",
               [T('sum by (severity) (trivy_resource_infraassessments{job="trivy-operator"})', "{{severity}}")],
               stack=True, decimals=0, overrides=sev), 6),
           (table("Compliance reports", "Cluster compliance reports and their pass / fail counts.",
                  ['sum by (title, status) (trivy_cluster_compliance{job="trivy-operator"})'],
                  [("title", "Report", None, None), ("status", "Status", None, {"cell": "color-text", "mappings": vmap(("Pass", "Pass", GREEN), ("Fail", "Fail", RED))}),
                   ("Value", "Controls", "short", None)]), 6))
    d.line(8, *[(p, 6) for p in controller_runtime("trivy-operator", what="Trivy")])
    return d


# =============================================================================================
# Runtime Threats
# =============================================================================================

def runtime():
    FA = 'job="falco"'
    FS = 'job="falcosidekick"'
    TG = 'job="tetragon"'
    LK = '{source="syscall", priority=~"(?i)$priority"}'
    d = Dash(UIDS["runtime-security"], "Security / Runtime Threats",
             "What workloads actually do at runtime, from the kernel: Falco's rule matches (every event is a log "
             "line in Loki via falcosidekick, and Warning and above become Alertmanager alerts) and Tetragon's eBPF "
             "process / file / network events and enforcement policies. Also whether the sensors themselves are "
             "healthy — a dropping sensor is a blind spot. Replaces the Falco, Falco logs and Tetragon dashboards.",
             tags("Security", "falco", "tetragon", "runtime"),
             [ds_var(),
              {"name": "priority", "label": "Falco priority", "type": "custom", "multi": True, "includeAll": True,
               "allValue": ".+", "query": "emergency,alert,critical,error,warning,notice,informational,debug",
               "current": {"selected": True, "text": ["All"], "value": ["$__all"]}, "hide": 0, "options": []}],
             links=links("Security", ("Policy & compliance", "policy"), ("Identity, secrets & certificates", "identity")),
             collapse=1)

    d.row("Threats at a glance")
    d.line(4,
           (pods_ready_stat("Falco sensors", "Falco DaemonSet pods (one per node).", "falco-[a-z0-9]{5}"), 3),
           (stat("Critical+ events (24h)", "Falco events of priority Critical, Alert or Emergency in the last 24 "
                 "hours (falcosidekick's counter).",
                 [f'sum(increase(falcosecurity_falcosidekick_falco_events_total{{{FS}, priority_raw=~"critical|alert|emergency"}}[24h])) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 10)), decimals=0), 3),
           (stat("Warning+ events (24h)", "Falco events of priority Warning and Error in the last 24 hours.",
                 [f'sum(increase(falcosecurity_falcosidekick_falco_events_total{{{FS}, priority_raw=~"warning|error"}}[24h])) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 100)), decimals=0), 3),
           (stat("Kernel event drops / s", "Syscall events Falco's driver dropped (ring buffer full). Drops are "
                 "blind spots: an attack in a dropped window is invisible.",
                 [f'sum(rate(falcosecurity_scap_n_drops_total{{{FA}}}[5m]))'], unit="ops",
                 th=steps((GREEN, None), (AMBER, 1), (RED, 100)), decimals=1), 3),
           (stat("Output delivery errors / s", "falcosidekick deliveries that failed (Loki, Alertmanager, UI). An "
                 "event that is not delivered never reaches the Console or a pager.",
                 [f'sum(rate(falcosecurity_falcosidekick_outputs_total{{{FS}, status="error"}}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (AMBER, 0.01), (RED, 0.5)), decimals=2), 3),
           (pods_ready_stat("Tetragon sensors", "Tetragon DaemonSet pods (one per node).", "tetragon-[a-z0-9]{5}"), 3),
           (stat("Tetragon policy events (24h)", "Events matched by TracingPolicies (sensitive files, privilege "
                 "changes, ...) in the last 24 hours.",
                 [f'sum(increase(tetragon_policy_events_total{{{TG}}}[24h])) or vector(0)'],
                 th=steps((GREEN, None), (AMBER, 1), (RED, 50)), decimals=0), 3),
           (stat("Tetragon missed events / s", "Events Tetragon could not deliver (ring buffer full / lost).",
                 [f'sum(rate(tetragon_ringbuf_perf_event_lost_total{{{TG}}}[5m])) or sum(rate(tetragon_missed_events_total{{{TG}}}[5m])) or vector(0)'],
                 unit="ops", th=steps((GREEN, None), (AMBER, 1), (RED, 100)), decimals=1), 3))

    d.row("Falco: detections")
    d.line(8,
           (ts("Events by priority", "Falco events per second by priority (falcosidekick).",
               [T(f'sum by (priority_raw) (rate(falcosecurity_falcosidekick_falco_events_total{{{FS}, priority_raw=~"(?i)$priority"}}[$__rate_interval]))', "{{priority_raw}}")],
               unit="ops", stack=True, overrides=[ov_color("critical|alert|emergency", RED), ov_color("error", "orange"),
                                                  ov_color("warning", AMBER), ov_color("notice", BLUE)]), 8),
           (bargauge("Top rules (range)", "Rules that fired most over the time range.",
                     [T(f'topk(15, sum by (rule) (increase(falcosecurity_falcosidekick_falco_events_total{{{FS}, priority_raw=~"(?i)$priority"}}[$__range])))', "{{rule}}")],
                     unit="short", min=0, max=None, th=NEUTRAL, decimals=0), 8),
           (bargauge("Top workloads (range)", "Pods that triggered most events over the time range.",
                     [T(f'topk(15, sum by (k8s_ns_name, k8s_pod_name) (increase(falcosecurity_falcosidekick_falco_events_total{{{FS}, priority_raw=~"(?i)$priority"}}[$__range])))', "{{k8s_ns_name}}/{{k8s_pod_name}}")],
                     unit="short", min=0, max=None, th=NEUTRAL, decimals=0), 8))
    d.line(8,
           (table("Events by rule and workload", "Every rule / workload / node combination that fired in the time "
                  "range, with its priority.",
                  [f'sum by (rule, priority_raw, k8s_ns_name, k8s_pod_name, hostname) (increase(falcosecurity_falcosidekick_falco_events_total{{{FS}, priority_raw=~"(?i)$priority"}}[$__range])) > 0'],
                  [("rule", "Rule", None, None),
                   ("priority_raw", "Priority", None, {"cell": "color-text", "mappings": vmap(
                       ("critical", "critical", RED), ("error", "error", "orange"), ("warning", "warning", AMBER),
                       ("notice", "notice", BLUE), ("informational", "info", GREY))}),
                   ("k8s_ns_name", "Namespace", None, None), ("k8s_pod_name", "Pod", None, None),
                   ("hostname", "Node", None, None), ("Value", "Events", "short", {"decimals": 0})],
                  sort=("Events", True)), 12),
           (ts("Events by MITRE-tagged rule (Falco)", "Rule matches per second as Falco itself counts them, by rule "
               "(top 10), independent of delivery.",
               [T(f'topk(10, sum by (rule_name) (rate(falcosecurity_falco_rules_matches_total{{{FA}}}[$__rate_interval])))', "{{rule_name}}")],
               unit="ops"), 12))
    d.line(12, (logs("Falco events (Loki)", "Every Falco event delivered by falcosidekick, newest first. Filter with "
                     "the priority variable; open a line for its output fields.",
                     LK), 24))

    d.row("Falco: sensor health")
    d.line(8,
           (ts("Kernel drops by reason", "Syscall events dropped per second by reason and node.",
               [T(f'sum by (pod) (rate(falcosecurity_scap_n_drops_total{{{FA}}}[$__rate_interval]))', "{{pod}}"),
                T(f'sum by (drop) (rate(falcosecurity_scap_n_drops_buffer_total{{{FA}}}[$__rate_interval])) > 0', "buffer {{drop}}")],
               unit="ops"), 8),
           (ts("Delivery by output", "falcosidekick deliveries per second by destination and status (errors red).",
               [T(f'sum by (destination, status) (rate(falcosecurity_falcosidekick_outputs_total{{{FS}}}[$__rate_interval]))', "{{destination}} {{status}}")],
               unit="ops", overrides=[ov_color(".* error", RED)]), 8),
           (ts("Falco CPU & memory", "Per-node Falco CPU usage and memory.",
               [T(f'max by (pod) (falcosecurity_falco_cpu_usage_ratio{{{FA}}})', "{{pod}} cpu"),
                T(f'max by (pod) (falcosecurity_falco_memory_rss_bytes{{{FA}}})', "{{pod}} rss")],
               unit="percentunit", overrides=[{"matcher": {"id": "byRegexp", "options": ".* rss"},
                                               "properties": [{"id": "unit", "value": "bytes"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("Tetragon")
    d.line(8,
           (ts("Events by type", "Tetragon events per second by type (process exec / exit, kprobe, tracepoint ...).",
               [T(f'sum by (type) (rate(tetragon_events_total{{{TG}}}[$__rate_interval]))', "{{type}}")],
               unit="ops", stack=True), 8),
           (ts("Policy events by policy & workload", "TracingPolicy matches per second by policy and workload.",
               [T(f'sum by (policy, workload) (rate(tetragon_policy_events_total{{{TG}}}[$__rate_interval]))', "{{policy}} {{workload}}")],
               unit="ops"), 8),
           (bargauge("Busiest binaries (range)", "Binaries generating the most events over the range (top 15).",
                     [T(f'topk(15, sum by (binary) (increase(tetragon_events_total{{{TG}}}[$__range])))', "{{binary}}")],
                     unit="short", min=0, max=None, th=NEUTRAL, decimals=0), 8))
    d.line(8,
           (ts("Errors", "Tetragon internal errors per second by kind.",
               [T(f'sum by (error) (rate(tetragon_errors_total{{{TG}}}[$__rate_interval]))', "{{error}}")], unit="ops"), 8),
           (ts("Missed probes", "Probe events missed per attach point and policy — the kernel could not hand them "
               "to Tetragon.",
               [T(f'sum by (policy, attach) (rate(tetragon_missed_prog_probes_total{{{TG}}}[$__rate_interval])) > 0', "{{policy}} {{attach}}")],
               unit="ops"), 8),
           (ts("Process cache", "Processes Tetragon tracks per node (its process cache).",
               [T(f'max by (pod) (tetragon_process_cache_size{{{TG}}})', "{{pod}}")], decimals=0), 8))
    d.line(8, *[(p, 8) for p in pods_resources("falco-.+|tetragon-.+")])
    return d


# =============================================================================================
# Identity, Secrets & Certificates
# =============================================================================================

def identity():
    KC = 'job="keycloak"'
    ES = 'job="external-secrets"'
    BAO = 'job=~"openbao|vault"'
    CM = 'job="cert-manager"'
    d = Dash(UIDS["identity"], "Security / Identity, Secrets & Certificates",
             "Who may do what and with which credentials: Keycloak (single sign-on for every platform app), "
             "External Secrets (every credential the platform hands to a workload), OpenBao (the secrets backend, "
             "wire-compatible with Vault) and cert-manager (the edge certificate and every internal one). Replaces "
             "the two Keycloak dashboards, External Secrets Operator, HashiCorp Vault and cert-manager.",
             tags("Security", "keycloak", "secrets", "cert-manager"),
             [ds_var()],
             links=links("Security", ("Policy & compliance", "policy"), ("Gateway, DNS & edge", "net-edge")),
             collapse=1)

    d.row("Identity & secrets at a glance")
    d.line(4,
           (pods_ready_stat("Keycloak", "Keycloak servers (the identity provider for every SSO app).", "keycloak-[a-z0-9]{8,10}-[a-z0-9]{5}"), 3),
           (stat("Token requests / s", "Calls to the OIDC token endpoint (logins, refreshes, client credentials).",
                 [f'sum(rate(http_server_requests_seconds_count{{{KC}, uri=~"/realms/.+/protocol/.+/token"}}[5m]))'],
                 unit="reqps", color_mode="value", graph="area", decimals=2), 3),
           (stat("Keycloak 5xx", "Share of Keycloak responses that are server errors.",
                 [f'100 * (sum(rate(http_server_requests_seconds_count{{{KC}, status=~"5.."}}[5m])) or vector(0)) / sum(rate(http_server_requests_seconds_count{{{KC}}}[5m]))'],
                 unit="percent", th=steps((GREEN, None), (AMBER, 0.5), (RED, 2)), decimals=2), 3),
           (stat("ExternalSecrets not ready", "ExternalSecrets whose Ready condition is False: the workload that "
                 "mounts them is missing a credential (often waiting for keycloak-clients).",
                 [f'sum(externalsecret_status_condition{{{ES}, condition="Ready", status="False"}} == 1) or vector(0)'],
                 th=OK_BAD), 3),
           (stat("Secret stores ready", "ClusterSecretStores Ready (vault / OpenBao, generators).",
                 [T(f'sum(clustersecretstore_status_condition{{{ES}, condition="Ready", status="True"}})', "ready"),
                  T(f'count(clustersecretstore_status_condition{{{ES}, condition="Ready", status="True"}})', "total")],
                 color_mode="value", text_mode="value_and_name"), 3),
           (stat("OpenBao", "Whether OpenBao is unsealed (the ESO 'vault' store fails while it is sealed).",
                 [f'max(vault_core_unsealed{{{BAO}}})'], mappings=vmap((0, "SEALED", RED), (1, "unsealed", GREEN)),
                 no_value="not deployed"), 3),
           (stat("Certificates not ready", "cert-manager Certificates whose Ready condition is not True.",
                 [f'count(certmanager_certificate_ready_status{{{CM}, condition="True"}} == 0) or vector(0)'], th=OK_BAD), 3),
           (stat("Soonest certificate expiry", "Days until the next certificate expires. cert-manager renews 30 "
                 "days ahead, so under ~25 days means renewals are failing.",
                 [f'min(certmanager_certificate_expiration_timestamp_seconds{{{CM}}} - time()) / 86400'],
                 unit="d", th=steps((RED, None), (AMBER, 21), (GREEN, 30)), decimals=0), 3))

    d.row("Keycloak")
    d.line(8,
           (ts("Requests by endpoint (top 10)", "Requests per second by URI template: token, auth, userinfo, admin "
               "API (the client-reconcile CronJob) and health.",
               [T(f'topk(10, sum by (uri) (rate(http_server_requests_seconds_count{{{KC}}}[$__rate_interval])))', "{{uri}}")],
               unit="reqps"), 8),
           (ts("Responses by status", "Responses per second by status; 4xx on the token endpoint are failed logins "
               "or bad client secrets.",
               [T(f'sum by (status) (rate(http_server_requests_seconds_count{{{KC}}}[$__rate_interval]))', "{{status}}")],
               unit="reqps", stack=True, overrides=[ov_color("5..", RED), ov_color("4..", "orange"), ov_color("2..", GREEN)]), 8),
           (ts("Average latency by endpoint", "Mean response time by URI template (sum / count).",
               [T(f'topk(8, sum by (uri) (rate(http_server_requests_seconds_sum{{{KC}}}[$__rate_interval])) / sum by (uri) (rate(http_server_requests_seconds_count{{{KC}}}[$__rate_interval])))', "{{uri}}")],
               unit="s"), 8))
    d.line(8,
           (ts("Password hashing & session expiry", "Password hash validations per second (logins with a password) "
               "and the time the session-expiration task takes.",
               [T(f'sum(rate(keycloak_credentials_password_hashing_validations_total{{{KC}}}[$__rate_interval]))', "hash validations/s"),
                T(f'max(keycloak_session_expiration_task_seconds_max{{{KC}}})', "expiration task (s)")],
               overrides=[{"matcher": {"id": "byName", "options": "expiration task (s)"},
                           "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Infinispan caches", "Entries and hit ratio of Keycloak's caches (sessions, realms, users).",
               [T(f'sum by (cache) (vendor_statistics_number_of_entries{{{KC}}})', "{{cache}} entries"),
                T(f'avg by (cache) (vendor_statistics_hit_ratio{{{KC}}})', "{{cache}} hit ratio")],
               overrides=[{"matcher": {"id": "byRegexp", "options": ".*hit ratio"},
                           "properties": [{"id": "unit", "value": "percentunit"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("JVM heap & connections", "Heap used against max and active HTTP connections.",
               [T(f'sum(jvm_memory_used_bytes{{{KC}, area="heap"}})', "heap used"),
                T(f'sum(jvm_memory_max_bytes{{{KC}, area="heap"}})', "heap max"),
                T(f'sum(http_server_active_connections{{{KC}}})', "connections")],
               unit="bytes", overrides=[ov_dashed("heap max", RED),
                                        {"matcher": {"id": "byName", "options": "connections"},
                                         "properties": [{"id": "unit", "value": "short"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(8, *[(p, 8) for p in pods_resources("keycloak-[a-z0-9]{8,10}-[a-z0-9]{5}")])

    d.row("External Secrets")
    d.line(8,
           (table("ExternalSecrets not ready", "Every ExternalSecret whose Ready condition is False.",
                  [f'max by (namespace, name) (externalsecret_status_condition{{{ES}, condition="Ready", status="False"}} == 1)'],
                  [("namespace", "Namespace", None, None), ("name", "ExternalSecret", None, None)],
                  hide=("Value",)), 8),
           (ts("Syncs & errors", "Secret syncs per second and sync errors (red). An error rate that does not drop "
               "is an ExternalSecret reading a key its store does not have.",
               [T(f'sum(rate(externalsecret_sync_calls_total{{{ES}}}[$__rate_interval]))', "syncs"),
                T(f'sum(rate(externalsecret_sync_calls_error{{{ES}}}[$__rate_interval]))', "errors")],
               unit="ops", overrides=[ov_color("errors", RED)]), 8),
           (ts("Provider API calls", "Calls to the secret providers per second by provider, call and status.",
               [T(f'sum by (provider, call, status) (rate(externalsecret_provider_api_calls_count{{{ES}}}[$__rate_interval]))', "{{provider}} {{call}} {{status}}")],
               unit="ops", overrides=[ov_color(".* error", RED)]), 8))
    d.line(8, *[(p, 6) for p in controller_runtime("external-secrets", what="External Secrets")])

    d.row("OpenBao (secrets backend)")
    d.line(8,
           (ts("Requests & logins", "Requests handled per second and login requests (Kubernetes auth from ESO).",
               [T(f'max(rate(vault_core_handle_request_count{{{BAO}}}[$__rate_interval]))', "requests"),
                T(f'max(rate(vault_core_handle_login_request_count{{{BAO}}}[$__rate_interval]))', "logins")],
               unit="ops"), 6),
           (ts("Leases & tokens", "Active leases and irrevocable leases (revocation failed — they never expire).",
               [T(f'max(vault_expire_num_leases{{{BAO}}})', "leases"),
                T(f'max(vault_expire_num_irrevocable_leases{{{BAO}}})', "irrevocable")],
               decimals=0, overrides=[ov_color("irrevocable", RED)]), 6),
           (ts("Barrier operations", "Storage barrier get / put / list / delete per second.",
               [T(f'max(rate(vault_barrier_get_count{{{BAO}}}[$__rate_interval]))', "get"),
                T(f'max(rate(vault_barrier_put_count{{{BAO}}}[$__rate_interval]))', "put"),
                T(f'max(rate(vault_barrier_list_count{{{BAO}}}[$__rate_interval]))', "list"),
                T(f'max(rate(vault_barrier_delete_count{{{BAO}}}[$__rate_interval]))', "delete")],
               unit="ops"), 6),
           (ts("Audit failures & in-flight", "Audit log request / response failures (OpenBao refuses requests it "
               "cannot audit) and requests in flight.",
               [T(f'max(vault_audit_log_request_failure{{{BAO}}})', "audit request failures"),
                T(f'max(vault_audit_log_response_failure{{{BAO}}})', "audit response failures"),
                T(f'max(vault_core_in_flight_requests{{{BAO}}})', "in flight")],
               decimals=0, overrides=[ov_color("audit.*", RED)]), 6))

    d.row("cert-manager")
    d.line(8,
           (table("Certificates", "Every Certificate: issuer, ready, and days until it expires.",
                  [f'max by (namespace, name, issuer_name, issuer_kind) (certmanager_certificate_ready_status{{{CM}, condition="True"}})',
                   f'max by (namespace, name) (certmanager_certificate_expiration_timestamp_seconds{{{CM}}} - time()) / 86400',
                   f'max by (namespace, name) (certmanager_certificate_renewal_timestamp_seconds{{{CM}}} - time()) / 86400'],
                  [("namespace", "Namespace", None, None), ("name", "Certificate", None, None),
                   ("issuer_name", "Issuer", None, None), ("issuer_kind", "Kind", None, None),
                   ("Value #A", "Ready", None, {"mappings": vmap((0, "NOT READY", RED), (1, "Ready", GREEN)), "cell": "color-background"}),
                   ("Value #B", "Expires in", "d", {"decimals": 0, "cell": "color-text", "th": steps((RED, None), (AMBER, 21), (GREEN, 30))}),
                   ("Value #C", "Renews in", "d", {"decimals": 0})],
                  sort=("Expires in", False)), 16),
           (table("Issuers", "ClusterIssuers and Issuers with their Ready condition.",
                  [f'max by (name) (certmanager_clusterissuer_ready_status{{{CM}, condition="True"}})',
                   f'max by (name) (certmanager_issuer_ready_status{{{CM}, condition="True"}})'],
                  [("name", "Issuer", None, None),
                   ("Value #A", "ClusterIssuer ready", None, {"mappings": vmap((0, "NOT READY", RED), (1, "Ready", GREEN)), "cell": "color-background"}),
                   ("Value #B", "Issuer ready", None, {"mappings": vmap((0, "NOT READY", RED), (1, "Ready", GREEN)), "cell": "color-background"})]), 8))
    d.line(8,
           (ts("ACME requests", "Requests to the ACME server (Let's Encrypt) per second by status. 429 is the rate "
               "limit (5 certificates per exact name set per week).",
               [T(f'sum by (status, path) (rate(certmanager_http_acme_client_request_count{{{CM}}}[$__rate_interval]))', "{{status}} {{path}}")],
               unit="reqps", overrides=[ov_color("429.*", RED)]), 8),
           (ts("Controller syncs", "Controller sync calls per second by controller.",
               [T(f'sum by (controller) (rate(certmanager_controller_sync_call_count{{{CM}}}[$__rate_interval]))', "{{controller}}")],
               unit="ops"), 8),
           (ts("Days to expiry (top 10 soonest)", "Days until expiry of the ten certificates closest to it.",
               [T(f'bottomk(10, (certmanager_certificate_expiration_timestamp_seconds{{{CM}}} - time()) / 86400)', "{{name}}")],
               unit="d", th=steps((RED, None), (AMBER, 21), (GREEN, 30))), 8))
    d.line(9, (component_logs("Identity & secrets errors (Loki)", "Errors from Keycloak, External Secrets, OpenBao "
                              "and cert-manager.", 'namespace="adhar-system", pod=~"keycloak-[a-z0-9]{8,10}-[a-z0-9]{5}|external-secrets-.+|openbao-.+|cert-manager-.+"'), 24))
    return d


DASHBOARDS = [
    ("security-policy", "Security", policy),
    ("security-runtime", "Security", runtime),
    ("security-identity", "Security", identity),
]
