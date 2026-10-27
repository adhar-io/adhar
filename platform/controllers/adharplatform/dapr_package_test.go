package adharplatform

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The dapr package (platform/stack/packages/application/dapr): its generated
// control plane, the platform namespace's components, its monitors and its
// alerts. The environments' building blocks are dapr_environment_test.go.

const (
	daprPackage   = "application/dapr"
	daprStateHost = "dapr-state.adhar-system.svc.cluster.local:6379"
	daprPeer0     = "dapr-scheduler-server-0=https://dapr-scheduler-server-0.dapr-scheduler-server.adhar-system.svc.cluster.local:2380"
)

func daprDocs(t *testing.T, rel string) []map[string]any {
	t.Helper()
	return readYAMLDocs(t, filepath.Join(stackPackagesDir(t), daprPackage, filepath.FromSlash(rel)))
}

func daprFind(t *testing.T, docs []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind && dig(d, "metadata", "name") == name {
			return d
		}
	}
	t.Fatalf("no %s/%s", kind, name)
	return nil
}

// daprComponentMetadata is a Component's spec.metadata as name -> value, and
// whether any entry reads a secret.
func daprComponentMetadata(t *testing.T, c map[string]any) (map[string]string, bool) {
	t.Helper()
	out, secret := map[string]string{}, false
	items, _ := dig(c, "spec", "metadata").([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		name, _ := m["name"].(string)
		if _, dup := out[name]; dup {
			t.Errorf("component %v sets metadata %q twice", dig(c, "metadata", "name"), name)
		}
		if m["secretKeyRef"] != nil {
			secret = true
		}
		v, _ := m["value"].(string)
		out[name] = v
	}
	return out, secret
}

// The scheduler is a ONE-member embedded etcd: its replica count and its etcd
// peer list must say the same thing. On 2026-10-16 the replica count alone was
// hand-edited to 1, the peer list kept three members, and the one pod waited
// for two peers that would never exist — no leader for 35 hours, while
// /healthz stayed green and the Jobs API, actor reminders and every Workflow
// were dead. generate-manifests.sh names this test.
func TestDaprSchedulerIsASingleMemberEtcd(t *testing.T) {
	sts := daprFind(t, daprDocs(t, "manifests/install.yaml"), "StatefulSet", "dapr-scheduler-server")
	replicas, _ := dig(sts, "spec", "replicas").(float64)
	if replicas != 1 {
		t.Errorf("the scheduler runs %v replicas; the platform runs one copy of each component", dig(sts, "spec", "replicas"))
	}
	var args []string
	containers, _ := dig(sts, "spec", "template", "spec", "containers").([]any)
	for _, c := range containers {
		if m, _ := c.(map[string]any); m["name"] == "dapr-scheduler-server" {
			for _, a := range m["args"].([]any) {
				args = append(args, a.(string))
			}
		}
	}
	peers := ""
	for i, a := range args {
		if a == "--etcd-initial-cluster" && i+1 < len(args) {
			peers = args[i+1]
		} else if v, ok := strings.CutPrefix(a, "--etcd-initial-cluster="); ok {
			peers = v
		}
	}
	if peers == "" {
		t.Fatalf("the scheduler has no --etcd-initial-cluster argument: %v", args)
	}
	members := strings.Split(peers, ",")
	if float64(len(members)) != replicas {
		t.Errorf("--etcd-initial-cluster names %d members for %v replicas: an etcd waiting for members that do not exist never elects a leader", len(members), replicas)
	}
	if members[0] != daprPeer0 {
		t.Errorf("--etcd-initial-cluster = %q, want %q", peers, daprPeer0)
	}
	if pc := dig(sts, "spec", "template", "spec", "priorityClassName"); pc != "adhar-node-bound" {
		t.Errorf("the scheduler's priorityClassName is %v; its volume is node-local, so it has one legal node (adhar-node-bound)", pc)
	}
	gen, err := os.ReadFile(filepath.Join(stackPackagesDir(t), daprPackage, "generate-manifests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gen), "python3 post-render.py") {
		t.Error("generate-manifests.sh no longer runs post-render.py; the next render restores the chart's three-member scheduler")
	}
}

// post-render.py is what makes the scheduler a one-member etcd after every
// `helm template`. Run it on the shape the chart renders (three replicas, three
// peers) and on the committed install.yaml: it must reduce the first, and leave
// the second byte-for-byte alone (the committed file is its own output).
func TestDaprPostRenderReducesTheChartsScheduler(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	script := filepath.Join(stackPackagesDir(t), daprPackage, "post-render.py")
	run := func(path string) {
		t.Helper()
		if out, err := exec.Command(py, script, path).CombinedOutput(); err != nil {
			t.Fatalf("post-render.py %s: %v\n%s", filepath.Base(path), err, out)
		}
	}
	peer := func(i string) string {
		return "dapr-scheduler-server-" + i + "=https://dapr-scheduler-server-" + i + ".dapr-scheduler-server.adhar-system.svc.cluster.local:2380"
	}
	chart := "---\n# Source: dapr/charts/dapr_scheduler/templates/dapr_scheduler_statefulset.yaml\n" +
		"apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: dapr-scheduler-server\nspec:\n  replicas: 3\n" +
		"  serviceName: dapr-scheduler-server\n  template:\n    spec:\n      containers:\n      - name: dapr-scheduler-server\n" +
		"        args:\n        - \"--etcd-initial-cluster\"\n        - \"" + peer("0") + "," + peer("1") + "," + peer("2") + "\"\n" +
		"        - \"--etcd-embed=true\"\n      serviceAccountName: dapr-scheduler\n      volumes: []\n" +
		"---\n# Source: dapr/charts/dapr_sentry/templates/dapr_sentry_deployment.yaml\napiVersion: apps/v1\nkind: Deployment\n" +
		"metadata:\n  name: dapr-sentry\nspec:\n  replicas: 1\n"
	dir := t.TempDir()
	rendered := filepath.Join(dir, "install.yaml")
	if err := os.WriteFile(rendered, []byte(chart), 0o644); err != nil {
		t.Fatal(err)
	}
	run(rendered)
	first, _ := os.ReadFile(rendered)
	sts := daprFind(t, readYAMLDocs(t, rendered), "StatefulSet", "dapr-scheduler-server")
	if dig(sts, "spec", "replicas") != float64(1) {
		t.Errorf("post-render.py left the scheduler at %v replicas", dig(sts, "spec", "replicas"))
	}
	if s := string(first); strings.Contains(s, "dapr-scheduler-server-1") || strings.Contains(s, "dapr-scheduler-server-2") || !strings.Contains(s, "\""+daprPeer0+"\"") {
		t.Errorf("post-render.py did not reduce the peer list to member 0:\n%s", s)
	}
	if pc := dig(sts, "spec", "template", "spec", "priorityClassName"); pc != "adhar-node-bound" {
		t.Errorf("post-render.py did not give the scheduler adhar-node-bound (got %v)", pc)
	}
	run(rendered)
	if second, _ := os.ReadFile(rendered); string(second) != string(first) {
		t.Error("post-render.py is not idempotent: a second run changed its own output")
	}

	committed, err := os.ReadFile(filepath.Join(stackPackagesDir(t), daprPackage, "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "committed.yaml")
	if err := os.WriteFile(cp, committed, 0o644); err != nil {
		t.Fatal(err)
	}
	run(cp)
	if again, _ := os.ReadFile(cp); string(again) != string(committed) {
		t.Error("post-render.py changes the committed install.yaml: it was hand-edited, or not regenerated with generate-manifests.sh")
	}
}

// The platform namespace's own components point at dapr-state, the store this
// package ships. Before 2026-10-27 `pubsub` pointed at redis-master with a
// `redis` Secret (Dapr's quickstart defaults; neither existed) and
// `statestore` at adhar-cache, a cluster-mode Valkey that refuses the
// multi-key transactions every actor and Workflow save is made of.
func TestDaprPlatformComponentsUseTheStandaloneStore(t *testing.T) {
	ps := daprFind(t, daprDocs(t, "manifests/pubsub.yaml"), "Component", "pubsub")
	ss := daprFind(t, daprDocs(t, "manifests/statestore.yaml"), "Component", "statestore")
	for _, c := range []map[string]any{ps, ss} {
		name := dig(c, "metadata", "name")
		md, secret := daprComponentMetadata(t, c)
		if md["redisHost"] != daprStateHost {
			t.Errorf("%v: redisHost = %q, want %s", name, md["redisHost"], daprStateHost)
		}
		if md["redisDB"] != "0" {
			t.Errorf("%v: redisDB = %q; database 0 is the platform namespace's (1-3 and 9-11 are the environments')", name, md["redisDB"])
		}
		if secret {
			t.Errorf("%v reads a Secret; dapr-state has no password", name)
		}
	}
	if dig(ps, "spec", "type") != "pubsub.redis" {
		t.Errorf("adhar-system pubsub is %v; the platform namespace uses Redis streams on dapr-state (no dependency on the kafka package)", dig(ps, "spec", "type"))
	}
	if md, _ := daprComponentMetadata(t, ps); !strings.Contains(md["consumerID"], "{namespace}") || md["maxLenApprox"] == "" {
		t.Errorf("adhar-system pubsub must namespace its topics (consumerID \"{namespace}\") and cap its streams (maxLenApprox): %v", md)
	}
	if md, _ := daprComponentMetadata(t, ss); md["actorStateStore"] != "true" || md["keyPrefix"] != "namespace" {
		t.Errorf("adhar-system statestore must be the actor store with keyPrefix namespace: %v", md)
	}

	docs := daprDocs(t, "manifests/state-store.yaml")
	conf, _ := dig(daprFind(t, docs, "ConfigMap", "dapr-state"), "data", "valkey.conf").(string)
	if strings.Contains(conf, "cluster-enabled yes") || !regexp.MustCompile(`(?m)^databases 16$`).MatchString(conf) {
		t.Error("dapr-state must stay a standalone Valkey with 16 databases: the environments select a database per tier")
	}
	svc := daprFind(t, docs, "Service", "dapr-state")
	ports := map[string]float64{}
	for _, p := range dig(svc, "spec", "ports").([]any) {
		m := p.(map[string]any)
		ports[m["name"].(string)], _ = m["port"].(float64)
	}
	if ports["valkey"] != 6379 || ports["metrics"] == 0 {
		t.Errorf("Service dapr-state ports = %v; components dial 6379 and the ServiceMonitor scrapes `metrics`", ports)
	}
}

// One scrape per target. The scheduler pod sits behind TWO Services
// (dapr-scheduler-server and dapr-scheduler-server-a) that both carry the
// labels the control-plane ServiceMonitor selects, so every scheduler series
// was scraped twice and every sum over them doubled (2,722 series, 2026-10-26).
func TestDaprMonitorsScrapeEachTargetOnce(t *testing.T) {
	mon := daprDocs(t, "manifests/servicemonitor.yaml")
	cp := daprFind(t, mon, "ServiceMonitor", "dapr-control-plane")
	sel, _ := dig(cp, "spec", "selector", "matchLabels").(map[string]any)
	eps, _ := dig(cp, "spec", "endpoints").([]any)
	if len(eps) != 1 {
		t.Fatalf("dapr-control-plane has %d endpoints, want 1", len(eps))
	}
	ep := eps[0].(map[string]any)
	var dropped []*regexp.Regexp
	rels, _ := ep["relabelings"].([]any)
	for _, r := range rels {
		m := r.(map[string]any)
		src, _ := m["sourceLabels"].([]any)
		if m["action"] == "drop" && len(src) == 1 && src[0] == "__meta_kubernetes_service_name" {
			dropped = append(dropped, regexp.MustCompile("^(?:"+m["regex"].(string)+")$"))
		}
	}
	scraped := map[string][]string{} // component -> Services scraped
	for _, d := range daprDocs(t, "manifests/install.yaml") {
		if d["kind"] != "Service" {
			continue
		}
		labels, _ := dig(d, "metadata", "labels").(map[string]any)
		match := true
		for k, v := range sel {
			match = match && labels[k] == v
		}
		hasPort := false
		for _, p := range dig(d, "spec", "ports").([]any) {
			hasPort = hasPort || p.(map[string]any)["name"] == ep["port"]
		}
		name := dig(d, "metadata", "name").(string)
		for _, re := range dropped {
			match = match && !re.MatchString(name)
		}
		if match && hasPort {
			comp, _ := labels["app.kubernetes.io/component"].(string)
			scraped[comp] = append(scraped[comp], name)
		}
	}
	for _, comp := range []string{"operator", "placement", "scheduler", "sentry", "sidecar-injector"} {
		if len(scraped[comp]) != 1 {
			t.Errorf("the %s is scraped through %d Services %v, want exactly 1", comp, len(scraped[comp]), scraped[comp])
		}
	}
	mrels, _ := ep["metricRelabelings"].([]any)
	if raw, _ := json.Marshal(mrels); !strings.Contains(string(raw), "etcd_debugging_") {
		t.Error("the scheduler's etcd_debugging_* families are no longer dropped")
	}

	pm := daprFind(t, mon, "PodMonitor", "dapr-sidecars")
	if dig(pm, "spec", "selector", "matchLabels", "dapr.io/sidecar-injected") != "true" || dig(pm, "spec", "namespaceSelector", "any") != true {
		t.Error("dapr-sidecars must select every injected pod in every namespace")
	}
	pep := dig(pm, "spec", "podMetricsEndpoints").([]any)[0].(map[string]any)
	if pep["port"] != "dapr-metrics" {
		t.Errorf("dapr-sidecars scrapes port %v; the injector names daprd's metrics port dapr-metrics", pep["port"])
	}
	relabelsAppID := false
	prels, _ := pep["relabelings"].([]any)
	for _, r := range prels {
		m := r.(map[string]any)
		src, _ := m["sourceLabels"].([]any)
		if len(src) == 1 && src[0] == "__meta_kubernetes_pod_label_dapr_io_app_id" && m["targetLabel"] == "app_id" {
			relabelsAppID = true
		}
	}
	if !relabelsAppID {
		t.Error("dapr-sidecars no longer copies dapr.io/app-id onto every series as app_id: the dashboard's per-app sidecar CPU, memory and `up` panels go blank")
	}

	ds := daprFind(t, mon, "ServiceMonitor", "dapr-state")
	svc := daprFind(t, daprDocs(t, "manifests/state-store.yaml"), "Service", "dapr-state")
	svcLabels, _ := dig(svc, "metadata", "labels").(map[string]any)
	for k, v := range dig(ds, "spec", "selector", "matchLabels").(map[string]any) {
		if svcLabels[k] != v {
			t.Errorf("ServiceMonitor dapr-state selects %s=%v, which Service dapr-state does not carry", k, v)
		}
	}
}

func daprAlertsAndRecords(t *testing.T) (alerts map[string]map[string]any, records map[string]string, exprs []string) {
	t.Helper()
	rule := daprFind(t, daprDocs(t, "manifests/rules.yaml"), "PrometheusRule", "dapr")
	alerts, records = map[string]map[string]any{}, map[string]string{}
	if dig(rule, "metadata", "annotations", "argocd.argoproj.io/sync-wave") != "10" {
		t.Error("PrometheusRule dapr must sync at wave 10 like every package's rules (security/tetragon/manifests/rules.yaml)")
	}
	for _, g := range dig(rule, "spec", "groups").([]any) {
		for _, r := range g.(map[string]any)["rules"].([]any) {
			m := r.(map[string]any)
			expr, _ := m["expr"].(string)
			exprs = append(exprs, expr)
			if a, ok := m["alert"].(string); ok {
				alerts[a] = m
			}
			if rec, ok := m["record"].(string); ok {
				records[rec] = expr
			}
		}
	}
	return alerts, records, exprs
}

// Nothing alerted while the scheduler had no leader for 35 hours: there were
// no Dapr rules at all.
func TestDaprRulesCoverTheControlPlaneAndTheSidecars(t *testing.T) {
	alerts, records, _ := daprAlertsAndRecords(t)
	for _, a := range []string{"DaprSchedulerNoLeader", "DaprPlacementNoLeader", "DaprControlPlaneDown",
		"DaprIssuerCertExpiring", "DaprCertSignFailures", "DaprSidecarInjectionFailed", "DaprComponentInitFailed",
		"DaprCircuitBreakerOpen", "DaprPubsubDrops", "DaprStateStoreDown", "DaprStateStoreMemoryHigh"} {
		r, ok := alerts[a]
		if !ok {
			t.Errorf("no %s alert", a)
			continue
		}
		if sev := dig(r, "labels", "severity"); sev != "warning" && sev != "critical" {
			t.Errorf("%s has severity %v", a, sev)
		}
		for _, k := range []string{"summary", "description"} {
			if s, _ := dig(r, "annotations", k).(string); strings.TrimSpace(s) == "" {
				t.Errorf("%s has no %s", a, k)
			}
		}
	}
	if e, _ := alerts["DaprSchedulerNoLeader"]["expr"].(string); !strings.Contains(e, "etcd_server_has_leader") || !strings.Contains(e, `job="scheduler"`) {
		t.Errorf("DaprSchedulerNoLeader no longer reads the scheduler's etcd_server_has_leader: %s", e)
	}
	for _, rec := range []string{"dapr:http_requests:rate5m", "dapr:http_latency_ms:p95", "dapr:invocation_sent:rate5m",
		"dapr:pubsub_ingress:rate5m", "dapr:pubsub_egress:rate5m", "dapr:state_ops:rate5m"} {
		if records[rec] == "" {
			t.Errorf("no recording rule %s", rec)
		}
	}
	if q := records["dapr:http_latency_ms:p95"]; !strings.HasSuffix(strings.TrimSpace(q), ">= 0") {
		t.Errorf("dapr:http_latency_ms:p95 records the NaN of an idle window; filter it with >= 0: %s", q)
	}
}

// daprMetrics is what Dapr 1.18.3 publishes, read from its source
// (pkg/diagnostics, pkg/{scheduler,placement,operator,injector,sentry}/monitoring).
// daprd exports through OpenCensus: a counter keeps its view's name with no
// `_total` appended, so dapr_http_server_request_count is right and
// dapr_component_state_count_total does not exist. Histograms add
// _bucket/_sum/_count to the names in daprHistograms.
var (
	daprMetrics = setOf(
		// control plane
		"dapr_scheduler_sidecars_connected", "dapr_scheduler_jobs_created_total", "dapr_scheduler_jobs_triggered_total",
		"dapr_scheduler_jobs_failed_total", "dapr_scheduler_jobs_undelivered_total", "dapr_scheduler_concurrency_inflight",
		"dapr_scheduler_concurrency_pending", "dapr_scheduler_concurrency_throttled_total", "dapr_scheduler_jobs_deleted_total",
		"dapr_scheduler_jobs_bulk_deleted_total", "dapr_scheduler_jobs_created_failed_total", "dapr_scheduler_sidecar_errors_total",
		"dapr_placement_runtimes_total", "dapr_placement_actor_runtimes_total", "dapr_placement_leader_status",
		"dapr_placement_raft_leader_status", "dapr_operator_service_created_total", "dapr_operator_service_deleted_total",
		"dapr_operator_service_updated_total", "dapr_injector_sidecar_injection_requests_total",
		"dapr_injector_sidecar_injection_succeeded_total", "dapr_injector_sidecar_injection_failed_total",
		"dapr_sentry_cert_sign_request_received_total", "dapr_sentry_cert_sign_success_total", "dapr_sentry_cert_sign_failure_total",
		"dapr_sentry_servercert_issue_failed_total", "dapr_sentry_issuercert_changed_total", "dapr_sentry_issuercert_expiry_timestamp",
		// runtime
		"dapr_runtime_component_loaded", "dapr_runtime_component_init_total", "dapr_runtime_component_init_fail_total",
		"dapr_runtime_mtls_init_total", "dapr_runtime_mtls_init_fail_total", "dapr_runtime_mtls_workload_cert_rotated_total",
		"dapr_runtime_mtls_workload_cert_rotated_fail_total", "dapr_runtime_actor_status_report_total",
		"dapr_runtime_actor_status_report_fail_total", "dapr_runtime_actor_table_operation_recv_total",
		"dapr_runtime_actor_rebalanced_total", "dapr_runtime_actor_deactivated_total", "dapr_runtime_actor_deactivated_failed_total",
		"dapr_runtime_actor_pending_actor_calls", "dapr_runtime_actor_timers", "dapr_runtime_actor_reminders",
		"dapr_runtime_actor_reminders_fired_total", "dapr_runtime_actor_timers_fired_total",
		"dapr_runtime_service_invocation_req_sent_total", "dapr_runtime_service_invocation_req_recv_total",
		"dapr_runtime_service_invocation_res_sent_total", "dapr_runtime_service_invocation_res_recv_total",
		"dapr_runtime_workflow_operation_count", "dapr_runtime_workflow_execution_count",
		"dapr_runtime_workflow_activity_operation_count", "dapr_runtime_workflow_activity_execution_count",
		// components
		"dapr_component_pubsub_ingress_count", "dapr_component_pubsub_ingress_bulk_count", "dapr_component_pubsub_ingress_bulk_event_count",
		"dapr_component_pubsub_egress_count", "dapr_component_pubsub_egress_bulk_count", "dapr_component_pubsub_egress_bulk_event_count",
		"dapr_component_input_binding_count", "dapr_component_output_binding_count", "dapr_component_state_count",
		"dapr_component_configuration_count", "dapr_component_secret_count", "dapr_component_conversation_count",
		"dapr_component_crypto_count", "dapr_component_job_success_count", "dapr_component_job_failure_count",
		// HTTP / gRPC / resiliency / error codes
		"dapr_http_server_request_count", "dapr_http_client_completed_count", "dapr_http_healthprobes_completed_count",
		"dapr_grpc_io_server_completed_rpcs", "dapr_grpc_io_client_completed_rpcs", "dapr_grpc_io_healthprobes_completed_count",
		"dapr_resiliency_loaded", "dapr_resiliency_count", "dapr_resiliency_activations_total", "dapr_resiliency_cb_state",
		"dapr_error_code_total",
	)
	daprHistograms = setOf(
		"dapr_scheduler_trigger_latency", "dapr_runtime_service_invocation_res_recv_latency_ms",
		"dapr_runtime_workflow_operation_latency", "dapr_runtime_workflow_activity_operation_latency",
		"dapr_runtime_workflow_activity_execution_latency", "dapr_runtime_workflow_execution_latency",
		"dapr_runtime_workflow_scheduling_latency", "dapr_component_pubsub_ingress_latencies",
		"dapr_component_pubsub_ingress_bulk_latencies", "dapr_component_pubsub_egress_latencies",
		"dapr_component_pubsub_egress_bulk_latencies", "dapr_component_input_binding_latencies",
		"dapr_component_output_binding_latencies", "dapr_component_state_latencies", "dapr_component_configuration_latencies",
		"dapr_component_secret_latencies", "dapr_component_conversation_latencies", "dapr_component_crypto_latencies",
		"dapr_component_job_latencies", "dapr_http_server_latency", "dapr_http_server_request_bytes",
		"dapr_http_server_response_bytes", "dapr_http_client_sent_bytes", "dapr_http_client_received_bytes",
		"dapr_http_client_roundtrip_latency", "dapr_http_healthprobes_roundtrip_latency", "dapr_grpc_io_server_server_latency",
		"dapr_grpc_io_server_received_bytes_per_rpc", "dapr_grpc_io_server_sent_bytes_per_rpc",
		"dapr_grpc_io_client_roundtrip_latency", "dapr_grpc_io_client_sent_bytes_per_rpc",
		"dapr_grpc_io_client_received_bytes_per_rpc", "dapr_grpc_io_healthprobes_roundtrip_latency",
	)
)

func setOf(xs ...string) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[x] = true
	}
	return out
}

// daprMetricKnown reports whether name is a series Dapr 1.18.3 publishes.
func daprMetricKnown(name string) bool {
	if daprMetrics[name] {
		return true
	}
	for _, suf := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, suf); ok && daprHistograms[base] {
			return true
		}
	}
	return false
}

// A panel or an alert on a metric Dapr does not publish never shows anything
// and never fires, and reads exactly like a healthy runtime. The Dapr docs
// misspell two of them (dapr_placement_actorruntimes_total,
// dapr_scheduler_trigger_jobs_failed_total); the names below are the source's.
func TestDaprPanelsAndRulesUseMetricsDaprPublishes(t *testing.T) {
	_, _, exprs := daprAlertsAndRecords(t)
	sources := map[string][]string{"application/dapr/manifests/rules.yaml": exprs}
	for _, name := range []string{"delivery-dapr", "delivery-dev-platform"} {
		raw, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "observability/kube-prometheus/manifests", "dashboard-"+name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var cm struct {
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal(raw, &cm); err != nil {
			t.Fatal(err)
		}
		for key, body := range cm.Data {
			if !strings.HasSuffix(key, ".json") {
				continue
			}
			var walk func(ps []any)
			walk = func(ps []any) {
				for _, p := range ps {
					pm, _ := p.(map[string]any)
					tgs, _ := pm["targets"].([]any)
					for _, tg := range tgs {
						if e, _ := tg.(map[string]any)["expr"].(string); e != "" {
							sources[name] = append(sources[name], e)
						}
					}
					kids, _ := pm["panels"].([]any)
					walk(kids)
				}
			}
			var dash map[string]any
			if err := json.Unmarshal([]byte(body), &dash); err != nil {
				t.Fatal(err)
			}
			panels, _ := dash["panels"].([]any)
			walk(panels)
		}
	}
	metric := regexp.MustCompile(`\bdapr_[a-z0-9_]+`)
	seen := 0
	for src, list := range sources {
		for _, e := range list {
			for _, m := range metric.FindAllString(e, -1) {
				seen++
				switch {
				case m == "dapr_http_server_response_count":
					t.Errorf("%s reads dapr_http_server_response_count, which daprd registers only with increasedCardinality: true — the environments' adhar-dapr turns it off; use dapr_http_server_request_count", src)
				case !daprMetricKnown(m):
					t.Errorf("%s reads %s, which Dapr 1.18.3 does not publish", src, m)
				}
			}
		}
	}
	if seen < 80 {
		t.Errorf("found only %d Dapr metric references; this guard is not reading the dashboards and rules", seen)
	}
	var names []string
	for n := range daprMetrics {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.HasSuffix(n, "_total_total") {
			t.Errorf("%s: a doubled _total", n)
		}
	}
}
