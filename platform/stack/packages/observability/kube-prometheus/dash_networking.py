"""Networking folder: three dashboards in place of ten.

  Cilium & Hubble          the CNI and its datapath (agents, operator, policy, endpoints,
                           identities, IPAM, BPF maps, node connectivity) and Hubble's flow
                           and L7 metrics.
  Gateway, DNS & Edge      CoreDNS, external-dns, the Cilium Gateway's Envoy pods and the
                           edge certificate: everything between a URL and a pod.
  Traffic by Namespace     bandwidth, packets and drops per namespace, workload and pod
                           (kubernetes-mixin; kept the old Networking / Cluster uid).
"""
from dashlib import *  # noqa: F401,F403
from dash_common import (CAD, KSM, SYS, UIDS, cluster_var, component_logs, embed_mixin, links, pods_ready_stat,
                         pods_resources, pods_table, tags, up_stat)

AG = 'job="adhar-system/cilium-agent"'
OP = 'job="adhar-system/cilium-operator"'
HB = 'job="adhar-system/hubble"'
AGENT_PODS = "cilium-[a-z0-9]{5}"


def on_node(expr):
    """Add the node name to a per-agent series (agents are labelled by pod and node IP)."""
    return (f'({expr}) * on (pod) group_left (node) max by (pod, node) '
            f'(kube_pod_info{{{KSM}, {SYS}, pod=~"{AGENT_PODS}"}})')


# =============================================================================================
# Cilium & Hubble
# =============================================================================================

def cilium():
    d = Dash(UIDS["net-cilium"], "Networking / Cilium & Hubble",
             "Cilium is the platform's CNI, kube-proxy replacement and Gateway data path. Agents (one per node), the "
             "operator, network policy enforcement and drops, endpoints and identities, IPAM, BPF map pressure, "
             "node-to-node connectivity, and Hubble's flow / HTTP metrics. Replaces the Cilium Agent, Operator, "
             "Hubble and Network Monitoring dashboards.",
             tags("Networking", "cilium", "hubble"),
             [ds_var(), qvar("node", "Node", f'label_values(kube_pod_info{{{KSM}, {SYS}, pod=~"{AGENT_PODS}"}}, node)')],
             links=links("Networking", ("Gateway, DNS & edge", "net-edge"), ("Traffic by namespace", "net-traffic")),
             collapse=1)

    AGN = f'{AG}, pod=~"$agent_pods"'
    d.add_variables([qvar("agent_pods", "Agent pods",
                          f'label_values(kube_pod_info{{{KSM}, {SYS}, pod=~"{AGENT_PODS}", node=~"$node"}}, pod)', hide=2)])

    d.row("Cilium at a glance")
    d.line(4,
           (pods_ready_stat("Agents ready", "Cilium agents (one DaemonSet pod per node).", AGENT_PODS), 3),
           (up_stat("Operator", "cilium-operator metrics endpoint.", "adhar-system/cilium-operator"), 3),
           (stat("Unreachable nodes", "Nodes this cluster's agents cannot reach through Cilium health checks "
                 "(worst agent). Anything above zero is a partitioned node: pods there cannot talk to the rest.",
                 [f'max(cilium_unreachable_nodes{{{AGN}}})'], th=OK_BAD), 3),
           (stat("Failing controllers", "Agent controllers whose last run failed, summed over agents. Persistent "
                 "failures mean state is not being programmed into the datapath.",
                 [f'sum(cilium_controllers_failing{{{AGN}}})'], th=OK_WARN_BAD), 3),
           (stat("Endpoints not ready", "Pod endpoints in any state other than ready (regenerating, waiting for "
                 "identity, ...). Brief spikes accompany rollouts.",
                 [f'sum(cilium_endpoint_state{{{AGN}, endpoint_state!="ready"}}) or vector(0)'], th=OK_WARN_BAD), 3),
           (stat("Policy drops / s", "Packets dropped because a network policy denied them. Expected from "
                 "default-deny environment namespaces; a sudden rise after a deploy is a missing allow rule.",
                 [f'sum(rate(cilium_drop_count_total{{{AGN}, reason=~"Policy denied.*"}}[5m])) or vector(0)'],
                 unit="pps", th=steps((GREEN, None), (AMBER, 1), (RED, 50)), decimals=1), 3),
           (stat("Other drops / s", "Packets dropped for any reason other than policy (no route, invalid packet, "
                 "CT map full, ...). These are datapath problems, not configuration.",
                 [f'sum(rate(cilium_drop_count_total{{{AGN}, reason!~"Policy denied.*"}}[5m])) or vector(0)'],
                 unit="pps", th=steps((GREEN, None), (AMBER, 1), (RED, 20)), decimals=1), 3),
           (stat("Fullest BPF map", "Highest fill of any BPF map on any agent. At 100% new connections / NAT "
                 "entries are dropped.",
                 [f'100 * max(cilium_bpf_map_pressure{{{AGN}}})'], unit="percent", th=PCT, decimals=1), 3))

    d.row("Datapath: forwarded & dropped")
    d.line(8,
           (ts("Forwarded traffic", "Bytes per second forwarded by the datapath, ingress (above) and egress "
               "(below), summed over the selected nodes.",
               [T(f'sum by (direction) (rate(cilium_forward_bytes_total{{{AGN}}}[$__rate_interval]))', "{{direction}}")],
               unit="Bps", overrides=[ov_negative("EGRESS")]), 8),
           (ts("Drops by reason", "Packets dropped per second by reason and direction.",
               [T(f'sum by (reason, direction) (rate(cilium_drop_count_total{{{AGN}}}[$__rate_interval]))', "{{reason}} ({{direction}})")],
               unit="pps", stack=True), 8),
           (ts("Hubble flows by verdict", "Flows Hubble observed per second by verdict (FORWARDED / DROPPED / "
               "AUDIT / ERROR).",
               [T(f'sum by (verdict) (rate(hubble_flows_processed_total{{{HB}}}[$__rate_interval]))', "{{verdict}}")],
               unit="ops", overrides=[ov_color("DROPPED", RED), ov_color("FORWARDED", GREEN)]), 8))
    d.line(8,
           (table("Drops by node and reason", "Drop rate over the last 5 minutes per node, reason and direction.",
                  [on_node(f'sum by (pod, reason, direction) (rate(cilium_drop_count_total{{{AGN}}}[5m])) > 0')],
                  [("node", "Node", None, None), ("reason", "Reason", None, None), ("direction", "Direction", None, None),
                   ("Value", "Drops / s", "pps", {"decimals": 2, "cell": "color-text", "th": steps((GREEN, None), (AMBER, 0.1), (RED, 10))})],
                  sort=("Drops / s", True), hide=("pod",)), 12),
           (ts("Hubble drops by reason", "Drops as Hubble records them (protocol and reason).",
               [T(f'sum by (reason, protocol) (rate(hubble_drop_total{{{HB}}}[$__rate_interval]))', "{{reason}} {{protocol}}")],
               unit="pps"), 12))

    d.row("Network policy")
    d.line(8,
           (ts("Endpoints by policy enforcement", "Endpoints with ingress / egress / both / no policy enforced.",
               [T(f'sum by (enforcement) (cilium_policy_endpoint_enforcement_status{{{AGN}}})', "{{enforcement}}")],
               stack=True, decimals=0), 6),
           (ts("Policies & revisions", "Policy rules loaded (max over agents) and policy changes per second.",
               [T(f'max(cilium_policy{{{AGN}}})', "rules loaded"),
                T(f'sum by (outcome) (rate(cilium_policy_change_total{{{AGN}}}[$__rate_interval]))', "changes {{outcome}}")],
               overrides=[{"matcher": {"id": "byRegexp", "options": "changes.*"},
                           "properties": [{"id": "custom.axisPlacement", "value": "right"}, {"id": "unit", "value": "ops"}]}]), 6),
           (ts("Policy implementation delay p99", "Time from a policy change to it being enforced in the datapath.",
               [T(f'histogram_quantile(0.99, sum by (le, source) (rate(cilium_policy_implementation_delay_bucket{{{AGN}}}[$__rate_interval])))', "{{source}}")],
               unit="s"), 6),
           (ts("L7 policy verdicts", "Requests seen by the L7 proxy (Envoy) per verdict.",
               [T(f'sum by (rule, proxy_type) (rate(cilium_policy_l7_total{{{AGN}}}[$__rate_interval]))', "{{proxy_type}} {{rule}}")],
               unit="reqps", overrides=[ov_color(".*denied", RED)]), 6))

    d.row("Endpoints, identities & IPAM")
    d.line(8,
           (ts("Endpoints by state", "Pod endpoints per state, summed over the selected nodes.",
               [T(f'sum by (endpoint_state) (cilium_endpoint_state{{{AGN}}})', "{{endpoint_state}}")],
               stack=True, decimals=0, overrides=[ov_color("ready", GREEN)]), 6),
           (ts("Endpoint regenerations", "Regenerations per second by outcome and the 99th percentile regeneration "
               "time. Slow regenerations delay new pods getting network.",
               [T(f'sum by (outcome) (rate(cilium_endpoint_regenerations_total{{{AGN}}}[$__rate_interval]))', "{{outcome}}"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(cilium_endpoint_regeneration_time_stats_seconds_bucket{{{AGN}, scope="total"}}[$__rate_interval])))', "p99 time")],
               unit="ops", overrides=[{"matcher": {"id": "byName", "options": "p99 time"},
                                       "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 6),
           (ts("Identities", "Security identities allocated, by type.",
               [T(f'max by (type) (cilium_identity{{{AGN}}})', "{{type}}")], decimals=0), 6),
           (bargauge("Pod IPs used per node", "Allocated pod IPs as a share of the node's pod CIDR. A full CIDR means "
                     "no new pod can start on that node.",
                     [T(on_node(f'100 * sum by (pod) (cilium_ip_addresses{{{AGN}, family="ipv4"}}) / sum by (pod) (cilium_ipam_capacity{{{AGN}, family="ipv4"}})'), "{{node}}")]), 6))

    d.row("BPF maps & connection tracking")
    d.line(8,
           (ts("BPF map pressure (top 10)", "Fill of the fullest BPF maps (conntrack, NAT, policy, LB).",
               [T(f'topk(10, max by (map_name) (cilium_bpf_map_pressure{{{AGN}}}))', "{{map_name}}")],
               unit="percentunit", max=1, th=steps((GREEN, None), (AMBER, 0.75), (RED, 0.9))), 8),
           (ts("Conntrack entries after GC", "Entries alive after each conntrack garbage collection, by family and "
               "protocol. Growth toward the map size means connections are leaking or the table is too small.",
               [T(f'sum by (family, protocol) (cilium_datapath_conntrack_gc_entries{{{AGN}, status="alive"}})', "{{family}} {{protocol}}")],
               decimals=0), 8),
           (ts("BPF map operation failures", "Failed BPF map operations per second by map and operation.",
               [T(f'sum by (map_name, operation) (rate(cilium_bpf_map_ops_total{{{AGN}, outcome="fail"}}[$__rate_interval])) > 0', "{{map_name}} {{operation}}")],
               unit="ops", th=steps((GREEN, None), (RED, 0.01))), 8))

    d.row("Node connectivity (health checks)")
    d.line(8,
           (ts("Unreachable nodes & endpoints", "Per agent: nodes and health endpoints it cannot reach.",
               [T(on_node(f'max by (pod) (cilium_unreachable_nodes{{{AGN}}})'), "{{node}} nodes"),
                T(on_node(f'max by (pod) (cilium_unreachable_health_endpoints{{{AGN}}})'), "{{node}} endpoints")],
               decimals=0, th=steps((GREEN, None), (RED, 1))), 8),
           (ts("Connectivity latency p99", "99th percentile node-to-node health-check latency, by protocol and "
               "address type. Spikes are a congested or lossy underlay.",
               [T(f'histogram_quantile(0.99, sum by (le, type, protocol) (rate(cilium_node_health_connectivity_latency_seconds_bucket{{{AGN}}}[$__rate_interval])))', "{{type}} {{protocol}}")],
               unit="s"), 8),
           (ts("Reachability by status", "Health-check targets by status (reachable / unreachable / unknown).",
               [T(f'sum by (status, type) (cilium_node_health_connectivity_status{{{AGN}}})', "{{type}} {{status}}")],
               decimals=0, overrides=[ov_color(".*unreachable", RED)]), 8))

    d.row("Agents")
    d.line(8,
           (ts("Errors & warnings", "Log errors and warnings per second emitted by the agents, by subsystem.",
               [T(f'sum by (level, subsystem) (rate(cilium_errors_warnings_total{{{AGN}}}[$__rate_interval]))', "{{level}} {{subsystem}}")],
               unit="ops", overrides=[ov_color("ERROR.*", RED)]), 8),
           (ts("Kubernetes API calls", "API calls per second the agents make, by method and return code, and their "
               "99th percentile latency.",
               [T(f'sum by (method, return_code) (rate(cilium_k8s_client_api_calls_total{{{AGN}}}[$__rate_interval]))', "{{method}} {{return_code}}"),
                T(f'histogram_quantile(0.99, sum by (le) (rate(cilium_k8s_client_api_latency_time_seconds_bucket{{{AGN}}}[$__rate_interval])))', "p99 latency")],
               unit="reqps", overrides=[{"matcher": {"id": "byName", "options": "p99 latency"},
                                         "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Agent CPU & memory", "Agent process CPU (cores) and resident memory per node.",
               [T(on_node(f'rate(cilium_process_cpu_seconds_total{{{AGN}}}[$__rate_interval])'), "{{node}} cpu"),
                T(on_node(f'cilium_process_resident_memory_bytes{{{AGN}}}'), "{{node}} rss")],
               unit="cores", overrides=[{"matcher": {"id": "byRegexp", "options": ".* rss"},
                                         "properties": [{"id": "unit", "value": "bytes"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8))
    d.line(9, (pods_table("Cilium pods", "Agents, operator, Envoy (Gateway data path) and Hubble relay / UI.",
                          "cilium.*|hubble-relay.*|hubble-ui.*"), 24))

    d.row("Operator")
    d.line(8,
           (ts("Operator errors & warnings", "Errors and warnings per second logged by cilium-operator.",
               [T(f'sum by (level, subsystem) (rate(cilium_operator_errors_warnings_total{{{OP}}}[$__rate_interval]))', "{{level}} {{subsystem}}")],
               unit="ops"), 6),
           (ts("Identity garbage collection", "Identities collected per run and run outcome.",
               [T(f'sum by (outcome, type) (cilium_operator_identity_gc_entries{{{OP}}})', "{{type}} {{outcome}}")], decimals=0), 6),
           (ts("Work queues", "Items waiting in the operator's work queues.",
               [T(f'sum by (name) (cilium_k8s_workqueue_depth{{{OP}}})', "{{name}}")], decimals=0), 6),
           (ts("Operator CPU & memory", "Operator process CPU and resident memory.",
               [T(f'sum(rate(cilium_operator_process_cpu_seconds_total{{{OP}}}[$__rate_interval]))', "cpu"),
                T(f'sum(cilium_operator_process_resident_memory_bytes{{{OP}}})', "rss")],
               unit="cores", overrides=[{"matcher": {"id": "byName", "options": "rss"},
                                         "properties": [{"id": "unit", "value": "bytes"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 6))

    d.row("Hubble: HTTP, TCP & ICMP")
    d.line(8,
           (ts("HTTP requests by method", "HTTP requests per second Hubble's L7 visibility recorded.",
               [T(f'sum by (method, protocol) (rate(hubble_http_requests_total{{{HB}}}[$__rate_interval]))', "{{method}} {{protocol}}")],
               unit="reqps"), 6),
           (ts("HTTP responses by status", "Responses per second by status code; 5xx are drawn red.",
               [T(f'sum by (status) (rate(hubble_http_responses_total{{{HB}}}[$__rate_interval]))', "{{status}}")],
               unit="reqps", stack=True, overrides=[ov_color("5..", RED), ov_color("4..", "orange")]), 6),
           (heatmap("HTTP latency", "Distribution of HTTP request latency observed by Hubble.",
                    f'sum by (le) (increase(hubble_http_request_duration_seconds_bucket{{{HB}}}[$__rate_interval]))'), 6),
           (ts("TCP flags, ICMP & lost events", "TCP flags seen per second (SYN, RST, FIN ...), ICMP messages, and "
               "events Hubble lost because its ring buffer overflowed (lost events = blind spots).",
               [T(f'sum by (flag) (rate(hubble_tcp_flags_total{{{HB}}}[$__rate_interval]))', "TCP {{flag}}"),
                T(f'sum by (type) (rate(hubble_icmp_total{{{HB}}}[$__rate_interval]))', "ICMP {{type}}"),
                T(f'sum by (source) (rate(hubble_lost_events_total{{{HB}}}[$__rate_interval]))', "lost {{source}}")],
               unit="ops", overrides=[ov_color("lost.*", RED), ov_color("TCP RST", "orange")]), 6))
    d.line(9, (component_logs("Agent warnings & errors (Loki)", "Cilium agent and operator log lines.",
                              'namespace="adhar-system", pod=~"cilium-.+", container=~"cilium-agent|cilium-operator"',
                              pattern='level=(warn|error)|(?i)error'), 24))
    return d


# =============================================================================================
# Gateway, DNS & Edge
# =============================================================================================

def edge():
    DNS = 'job="coredns"'
    XD = 'job="external-dns"'
    d = Dash(UIDS["net-edge"], "Networking / Gateway, DNS & Edge",
             "Everything between a URL and a pod: CoreDNS (cluster DNS and the *.<host> rewrite to the Gateway), "
             "external-dns (public records for the Gateway), the Cilium Gateway's Envoy pods and the edge TLS "
             "certificate. Replaces the CoreDNS, External DNS and Blackbox Exporter dashboards.",
             tags("Networking", "dns", "gateway"),
             [ds_var()],
             links=links("Networking", ("Cilium & Hubble", "net-cilium"), ("Identity, secrets & certificates", "identity")),
             collapse=1)

    d.row("Edge at a glance")
    d.line(4,
           (pods_ready_stat("CoreDNS", "CoreDNS replicas (kube-system).", "coredns-.+", ns='namespace="kube-system"'), 3),
           (stat("DNS queries / s", "Queries per second answered by CoreDNS.",
                 [f'sum(rate(coredns_dns_requests_total{{{DNS}}}[5m]))'], unit="reqps", color_mode="value", graph="area"), 3),
           (stat("SERVFAIL ratio", "Share of responses that are SERVFAIL — the upstream resolver or the API "
                 "server failed to answer. NXDOMAIN is normal (search-path expansion) and not counted.",
                 [f'100 * sum(rate(coredns_dns_responses_total{{{DNS}, rcode="SERVFAIL"}}[5m])) / sum(rate(coredns_dns_responses_total{{{DNS}}}[5m]))'],
                 unit="percent", th=steps((GREEN, None), (AMBER, 0.5), (RED, 2)), decimals=2), 3),
           (stat("DNS p99", "99th percentile time CoreDNS takes to answer.",
                 [f'histogram_quantile(0.99, sum by (le) (rate(coredns_dns_request_duration_seconds_bucket{{{DNS}}}[5m])))'],
                 unit="s", th=steps((GREEN, None), (AMBER, 0.1), (RED, 0.5))), 3),
           (stat("Cache hit ratio", "Share of queries answered from CoreDNS's cache.",
                 [f'100 * sum(rate(coredns_cache_hits_total{{{DNS}}}[5m])) / (sum(rate(coredns_cache_hits_total{{{DNS}}}[5m])) + sum(rate(coredns_cache_misses_total{{{DNS}}}[5m])))'],
                 unit="percent", th=steps((RED, None), (AMBER, 30), (GREEN, 60)), decimals=0), 3),
           (pods_ready_stat("Gateway Envoy", "cilium-envoy pods: the Gateway's data path on every node.",
                            "cilium-envoy-.+"), 3),
           (stat("external-dns last sync", "Time since external-dns last synced records with the DNS provider. "
                 "Over a few minutes means public records are stale.",
                 [f'time() - max(external_dns_controller_last_sync_timestamp_seconds{{{XD}}})'], unit="s",
                 th=steps((GREEN, None), (AMBER, 600), (RED, 3600)), no_value="not deployed"), 3),
           (stat("Edge certificate expires in", "Days until the platform's edge certificate (adhar-cert, served "
                 "by the Gateway for every *.<host> URL) expires. cert-manager renews it 30 days before.",
                 ['min(certmanager_certificate_expiration_timestamp_seconds{name="adhar-cert"} - time()) / 86400'],
                 unit="d", th=steps((RED, None), (AMBER, 14), (GREEN, 30)), decimals=0, no_value="no certificate"), 3))

    d.row("CoreDNS")
    d.line(8,
           (ts("Queries by type", "Queries per second by record type and transport.",
               [T(f'sum by (type, proto) (rate(coredns_dns_requests_total{{{DNS}}}[$__rate_interval]))', "{{type}} {{proto}}")],
               unit="reqps", stack=True), 8),
           (ts("Responses by rcode", "Responses per second by return code. SERVFAIL red, NXDOMAIN amber.",
               [T(f'sum by (rcode) (rate(coredns_dns_responses_total{{{DNS}}}[$__rate_interval]))', "{{rcode}}")],
               unit="reqps", stack=True, overrides=[ov_color("SERVFAIL", RED), ov_color("NXDOMAIN", AMBER),
                                                    ov_color("NOERROR", GREEN)]), 8),
           (heatmap("Query latency", "CoreDNS answer time distribution.",
                    f'sum by (le) (increase(coredns_dns_request_duration_seconds_bucket{{{DNS}}}[$__rate_interval]))'), 8))
    d.line(8,
           (ts("Upstream (forward) latency p99 & rcodes", "Queries CoreDNS forwards to the node's resolver: 99th "
               "percentile latency per upstream, and forward health-check failures.",
               [T(f'histogram_quantile(0.99, sum by (le, to) (rate(coredns_proxy_request_duration_seconds_bucket{{{DNS}}}[$__rate_interval])))', "{{to}} p99"),
                T(f'sum(rate(coredns_forward_healthcheck_broken_total{{{DNS}}}[$__rate_interval]))', "upstreams broken")],
               unit="s", overrides=[ov_color("upstreams broken", RED)]), 8),
           (ts("Cache", "Cache hits by type, misses, and entries held.",
               [T(f'sum by (type) (rate(coredns_cache_hits_total{{{DNS}}}[$__rate_interval]))', "hits {{type}}"),
                T(f'sum(rate(coredns_cache_misses_total{{{DNS}}}[$__rate_interval]))', "misses"),
                T(f'sum by (type) (coredns_cache_entries{{{DNS}}})', "entries {{type}}")],
               unit="ops", overrides=[{"matcher": {"id": "byRegexp", "options": "entries.*"},
                                       "properties": [{"id": "unit", "value": "short"}, {"id": "custom.axisPlacement", "value": "right"}]}]), 8),
           (ts("Panics, reload failures, rejected", "Things that should always be zero: plugin panics, Corefile "
               "reload failures, and forwards rejected at max_concurrent.",
               [T(f'sum(increase(coredns_panics_total{{{DNS}}}[$__rate_interval]))', "panics"),
                T(f'sum(increase(coredns_reload_failed_total{{{DNS}}}[$__rate_interval]))', "reload failed"),
                T(f'sum(increase(coredns_forward_max_concurrent_rejects_total{{{DNS}}}[$__rate_interval]))', "rejected")],
               draw="bars", decimals=0, th=steps((GREEN, None), (RED, 1))), 8))
    d.line(8, *[(p, 8) for p in pods_resources("coredns-.+", ns='namespace="kube-system"')])

    d.row("external-dns (public records)")
    d.line(8,
           (ts("Records: desired vs registered", "A records external-dns wants (source: the Gateway and its "
               "routes), owns in the zone (registry) and has verified.",
               [T(f'sum(external_dns_source_a_records{{{XD}}})', "source A"),
                T(f'sum(external_dns_registry_a_records{{{XD}}})', "registry A"),
                T(f'sum(external_dns_controller_verified_a_records{{{XD}}})', "verified A"),
                T(f'sum(external_dns_source_endpoints_total{{{XD}}})', "source endpoints")], decimals=0), 8),
           (ts("Errors", "Source, registry and provider (webhook) errors per second. Registry errors usually mean "
               "the DNS provider credential (adhar-dns-provider) is wrong or rate-limited.",
               [T(f'sum(rate(external_dns_source_errors_total{{{XD}}}[$__rate_interval]))', "source"),
                T(f'sum(rate(external_dns_registry_errors_total{{{XD}}}[$__rate_interval]))', "registry"),
                T(f'sum(rate(external_dns_webhook_provider_records_errors_total{{{XD}}}[$__rate_interval]))', "provider records"),
                T(f'sum(rate(external_dns_webhook_provider_applychanges_errors_total{{{XD}}}[$__rate_interval]))', "provider apply")],
               unit="ops", th=steps((GREEN, None), (RED, 0.001))), 8),
           (ts("Sync age & no-op runs", "Seconds since the last sync and since the last reconcile, and runs that "
               "found nothing to change (the steady state).",
               [T(f'time() - max(external_dns_controller_last_sync_timestamp_seconds{{{XD}}})', "since sync"),
                T(f'time() - max(external_dns_controller_last_reconcile_timestamp_seconds{{{XD}}})', "since reconcile"),
                T(f'sum(increase(external_dns_controller_no_op_runs_total{{{XD}}}[$__rate_interval]))', "no-op runs")],
               unit="s", overrides=[{"matcher": {"id": "byName", "options": "no-op runs"},
                                     "properties": [{"id": "unit", "value": "short"}, {"id": "custom.drawStyle", "value": "bars"},
                                                    {"id": "custom.axisPlacement", "value": "right"}]}]), 8))

    d.row("Gateway data path (Cilium Envoy)")
    d.line(9, (pods_table("Envoy pods", "The cilium-envoy DaemonSet terminates TLS and routes every HTTPRoute of "
                          "the adhar-gateway. A node whose Envoy is not ready drops the edge traffic that lands on it.",
                          "cilium-envoy-.+"), 24))
    d.line(8, *[(p, 8) for p in pods_resources("cilium-envoy-.+", label="{{pod}}")])
    d.line(9, (component_logs("CoreDNS & external-dns logs (Loki)", "Warnings and errors from cluster DNS and "
                              "external-dns.", 'pod=~"coredns-.+|external-dns-.+"'), 24))
    return d


# =============================================================================================
# Traffic by namespace & workload
# =============================================================================================

def traffic():
    v_ns = qvar("namespace", "Namespace", 'label_values(container_network_receive_packets_total, namespace)',
                multi=False, include_all=False)
    v_ns["current"] = {"selected": True, "text": "adhar-system", "value": "adhar-system"}
    d = Dash(UIDS["net-traffic"], "Networking / Traffic by Namespace & Workload",
             "Pod network traffic from cAdvisor: the whole cluster by namespace, then the selected namespace by "
             "workload and by pod — bandwidth, packets, drops and TCP retransmits (kubernetes-mixin networking set: "
             "Cluster, Namespace (Workload), Namespace (Pods)).",
             tags("Networking", "kubernetes"),
             [ds_var(), cluster_var(), v_ns,
              qvar("type", "Workload type", 'label_values(namespace_workload_pod:kube_pod_owner:relabel{namespace="$namespace", workload=~".+"}, workload_type)',
                   multi=False, include_all=True, all_value=".+")],
             links=links("Networking", ("Cilium & Hubble", "net-cilium"), ("Workloads & pods", "k8s-workloads")),
             collapse={"Traffic at a glance", "Cluster by namespace (kubernetes-mixin)"})
    N = 'namespace=~".+"'
    d.row("Traffic at a glance")
    d.line(4,
           (stat("Cluster receive", "Pod bytes received per second, whole cluster.",
                 [f'sum(rate(container_network_receive_bytes_total{{{CAD}, {N}}}[5m]))'], unit="Bps", color_mode="value", graph="area"), 4),
           (stat("Cluster transmit", "Pod bytes transmitted per second, whole cluster.",
                 [f'sum(rate(container_network_transmit_bytes_total{{{CAD}, {N}}}[5m]))'], unit="Bps", color_mode="value", graph="area"), 4),
           (stat("Namespace receive", "Bytes received per second by the selected namespace's pods.",
                 [f'sum(rate(container_network_receive_bytes_total{{{CAD}, namespace="$namespace"}}[5m]))'], unit="Bps", color_mode="value", graph="area"), 4),
           (stat("Namespace transmit", "Bytes transmitted per second by the selected namespace's pods.",
                 [f'sum(rate(container_network_transmit_bytes_total{{{CAD}, namespace="$namespace"}}[5m]))'], unit="Bps", color_mode="value", graph="area"), 4),
           (stat("Packets dropped / s", "Received + transmitted packets dropped per second at pod interfaces, "
                 "whole cluster.",
                 [f'sum(rate(container_network_receive_packets_dropped_total{{{CAD}, {N}}}[5m]) ) + sum(rate(container_network_transmit_packets_dropped_total{{{CAD}, {N}}}[5m]))'],
                 unit="pps", th=steps((GREEN, None), (AMBER, 1), (RED, 100)), decimals=1), 4),
           (stat("TCP retransmit ratio", "Retransmitted / sent TCP segments across all nodes.",
                 ['100 * sum(rate(node_netstat_Tcp_RetransSegs[5m])) / sum(rate(node_netstat_Tcp_OutSegs[5m]))'],
                 unit="percent", th=steps((GREEN, None), (AMBER, 1), (RED, 5)), decimals=2), 4))
    embed_mixin(d, "cluster-total", row="Cluster by namespace (kubernetes-mixin)")
    embed_mixin(d, "namespace-by-workload", row="$namespace by workload (kubernetes-mixin)")
    embed_mixin(d, "namespace-by-pod", row="$namespace by pod (kubernetes-mixin)")
    return d


DASHBOARDS = [
    ("net-cilium", "Networking", cilium),
    ("net-edge", "Networking", edge),
    ("net-traffic", "Networking", traffic),
]
