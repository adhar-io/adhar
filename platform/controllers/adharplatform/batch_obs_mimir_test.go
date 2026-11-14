package adharplatform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Design 0025 §11.3, PLAT-3.5: the batch alerts whose series live only in Mimir
// (the OTLP meters run pods push, the observer's SLA gauges) are evaluated by the
// Mimir ruler. Four pieces have to agree, and each one failing reads exactly like a
// healthy batch platform, because an alert that is never evaluated never fires:
// the ruler runs and notifies the platform Alertmanager (observability/mimir),
// Alloy loads exactly the rules labelled adhar.io/rules-target: mimir into it
// (observability/alloy), Prometheus evaluates exactly the others
// (observability/kube-prometheus), and the rules read the series as the OTLP
// exporter actually names them. PLAT-3.9's batch cost is the adhar.cost.batch group
// of adhar-cost-governance and the Batch row of Observability / Cost.
const (
	mimirInstall        = "observability/mimir/manifests/install.yaml"
	mimirValues         = "observability/mimir/values.yaml"
	mimirBatchAlerts    = "observability/mimir/manifests/batch-app-alerts.yaml"
	mimirRulerAlerts    = "observability/mimir/manifests/ruler-alerts.yaml"
	alloyInstall        = "observability/alloy/manifests/install.yaml"
	alloyHubEndpoints   = "observability/alloy/manifests/hub-endpoints.yaml"
	kubePromInstall     = "observability/kube-prometheus/manifests/install.yaml"
	costGovernanceRules = "observability/adhar-cost-governance/manifests/rules.yaml"
	rustfsBuckets       = "data/rustfs/manifests/buckets.yaml"

	rulesTargetLabel   = "adhar.io/rules-target"
	rulesTargetMimir   = "mimir"
	platformAlertmgr   = "http://prometheus-kube-prometheus-alertmanager.adhar-system.svc.cluster.local:9093"
	batchJobComposeRel = "../../controlplane/configuration/compositions/batchjob/kubernetes.yaml"
)

// obsDocs returns every YAML document of a stack file that parses.
func obsDocs(t *testing.T, rel string) []map[string]any {
	t.Helper()
	return yamlDocs(t, filepath.Join(stackPackagesDir(t), filepath.FromSlash(rel)))
}

func obsDocNamed(t *testing.T, docs []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, d := range docs {
		meta, _ := d["metadata"].(map[string]any)
		if d["kind"] == kind && meta["name"] == name {
			return d
		}
	}
	t.Fatalf("no %s %s in the render", kind, name)
	return nil
}

func obsDig(m any, path ...string) any {
	for _, p := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			return nil
		}
		m = mm[p]
	}
	return m
}

// obsRuleSpec is a PrometheusRule's groups as promtool reads them.
type obsRuleSpec struct {
	Groups []struct {
		Name     string `json:"name"`
		Interval string `json:"interval"`
		Rules    []struct {
			Record string            `json:"record"`
			Alert  string            `json:"alert"`
			Expr   string            `json:"expr"`
			Labels map[string]string `json:"labels"`
		} `json:"rules"`
	} `json:"groups"`
}

func obsLoadPromRule(t *testing.T, rel string) (map[string]any, obsRuleSpec) {
	t.Helper()
	docs := obsDocs(t, rel)
	var doc map[string]any
	for _, d := range docs {
		if d["kind"] == "PrometheusRule" {
			doc = d
		}
	}
	if doc == nil {
		t.Fatalf("%s has no PrometheusRule", rel)
	}
	var spec obsRuleSpec
	b, _ := yaml.Marshal(doc["spec"])
	if err := yaml.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	return doc, spec
}

// One ruler, evaluating in-process, whose notifications go to the Alertmanager every
// Prometheus-evaluated rule uses (and so to the team's OnCall route). The chart's
// default points it at Mimir's own Alertmanager, which this profile does not run:
// that ruler evaluates everything and delivers nothing.
func TestMimirRulerRunsAndNotifiesThePlatformAlertmanager(t *testing.T) {
	var values map[string]any
	if err := yaml.Unmarshal([]byte(readStackFile(t, mimirValues)), &values); err != nil {
		t.Fatal(err)
	}
	if obsDig(values, "ruler", "enabled") != true {
		t.Error("mimir values.yaml: ruler.enabled is not true")
	}
	docs := obsDocs(t, mimirInstall)
	ruler := obsDocNamed(t, docs, "Deployment", "mimir-ruler")
	if r := fmt.Sprint(obsDig(ruler, "spec", "replicas")); r != "1" {
		t.Errorf("mimir-ruler runs %v replicas, want 1", obsDig(ruler, "spec", "replicas"))
	}
	cm := obsDocNamed(t, docs, "ConfigMap", "mimir-config")
	raw, _ := obsDig(cm, "data", "mimir.yaml").(string)
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("mimir-config mimir.yaml: %v", err)
	}
	if got := obsDig(cfg, "ruler", "alertmanager_url"); got != platformAlertmgr {
		t.Errorf("ruler.alertmanager_url = %v, want the platform Alertmanager %s", got, platformAlertmgr)
	}
	// The URL must name a Service the kube-prometheus package renders, on that port.
	am := obsDocNamed(t, obsDocs(t, kubePromInstall), "Service", "prometheus-kube-prometheus-alertmanager")
	found := false
	for _, p := range obsDig(am, "spec", "ports").([]any) {
		if fmt.Sprint(obsDig(p, "port")) == "9093" {
			found = true
		}
	}
	if !found {
		t.Error("Service prometheus-kube-prometheus-alertmanager has no port 9093")
	}
	// The ruler keeps its rule groups in the object store; RustFS creates the bucket.
	bucket, _ := obsDig(cfg, "ruler_storage", "s3", "bucket_name").(string)
	if bucket == "" || !regexp.MustCompile(`\b`+regexp.QuoteMeta(bucket)+`\b`).MatchString(readStackFile(t, rustfsBuckets)) {
		t.Errorf("ruler_storage bucket %q is not created by %s", bucket, rustfsBuckets)
	}
}

// obsAlloyBlock returns the body of the first `<component> "<label>" {` block of a
// config.alloy.
func obsAlloyBlock(config, component string) string {
	i := strings.Index(config, component+" ")
	if i < 0 {
		return ""
	}
	open := strings.Index(config[i:], "{")
	if open < 0 {
		return ""
	}
	depth := 0
	for j := i + open; j < len(config); j++ {
		switch config[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return config[i+open+1 : j]
			}
		}
	}
	return ""
}

// The loader selects exactly the rules Prometheus leaves out, reaches the ruler
// through the gateway (which supplies the tenant and routes the ruler API), owns a
// Mimir namespace prefix per cluster (it deletes every group under its prefix that
// its cluster does not declare, so a shared prefix would have hub and spokes
// deleting each other's rules), and pins every query to its own cluster's series.
func TestAlloyLoadsTheMimirTargetedRulesIntoTheRuler(t *testing.T) {
	cm := obsDocNamed(t, obsDocs(t, alloyInstall), "ConfigMap", "alloy")
	config, _ := obsDig(cm, "data", "config.alloy").(string)
	block := obsAlloyBlock(config, "mimir.rules.kubernetes")
	if block == "" {
		t.Fatal("config.alloy declares no mimir.rules.kubernetes component")
	}
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(block, " ")
	for what, want := range map[string]string{
		"the rule selector":     `rule_selector { match_labels = { "` + rulesTargetLabel + `" = "` + rulesTargetMimir + `", } }`,
		"the ruler address":     `address = string.trim_suffix(sys.env("HUB_MIMIR_URL"), "/api/v1/push")`,
		"a per-cluster prefix":  `mimir_namespace_prefix = string.format("adhar-%s", sys.env("HUB_CLUSTER_NAME"))`,
		"the cluster query pin": `matcher { name = "cluster" match_type = "=" value = sys.env("HUB_CLUSTER_NAME") }`,
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("mimir.rules.kubernetes lacks %s: want %q in\n%s", what, want, flat)
		}
	}
	if strings.Contains(flat, "rule_namespace_selector") {
		t.Error("mimir.rules.kubernetes narrows the namespaces: a PrometheusRule labelled for Mimir in any namespace must load")
	}
	// The trimmed address only reaches the ruler if HUB_MIMIR_URL is the gateway's
	// push path and the gateway routes the ruler API.
	hub := obsDocNamed(t, obsDocs(t, alloyHubEndpoints), "ConfigMap", "observability-hub")
	if u, _ := obsDig(hub, "data", "HUB_MIMIR_URL").(string); !strings.HasPrefix(u, "http://mimir-gateway.") || !strings.HasSuffix(u, "/api/v1/push") {
		t.Errorf("HUB_MIMIR_URL %q is not the mimir-gateway push path the loader trims", u)
	}
	if !regexp.MustCompile(`location /prometheus/config/v1/rules \{\s*set \$ruler mimir-ruler\.`).MatchString(readStackFile(t, mimirInstall)) {
		t.Error("the mimir gateway does not route /prometheus/config/v1/rules to mimir-ruler")
	}
}

// Every PrometheusRule is evaluated by exactly one ruler. Prometheus's selector is
// NotIn [mimir] on the label Alloy matches with = mimir, so the two are complements;
// and a rule labelled for Mimir ships only where Mimir and Alloy run, or nothing
// evaluates it.
func TestPrometheusAndTheMimirRulerPartitionThePrometheusRules(t *testing.T) {
	prom := obsDocNamed(t, obsDocs(t, kubePromInstall), "Prometheus", "prometheus-kube-prometheus-prometheus")
	sel, _ := obsDig(prom, "spec", "ruleSelector").(map[string]any)
	exprs, _ := sel["matchExpressions"].([]any)
	if len(sel) != 1 || len(exprs) != 1 {
		t.Fatalf("Prometheus ruleSelector = %v, want exactly one matchExpression excluding %s=%s", sel, rulesTargetLabel, rulesTargetMimir)
	}
	e := exprs[0].(map[string]any)
	vals, _ := e["values"].([]any)
	if e["key"] != rulesTargetLabel || e["operator"] != "NotIn" || len(vals) != 1 || vals[0] != rulesTargetMimir {
		t.Errorf("Prometheus ruleSelector expression = %v, want %s NotIn [%s]", e, rulesTargetLabel, rulesTargetMimir)
	}
	if ns, ok := obsDig(prom, "spec", "ruleNamespaceSelector").(map[string]any); !ok || len(ns) != 0 {
		t.Errorf("Prometheus ruleNamespaceSelector = %v, want {} (every namespace)", obsDig(prom, "spec", "ruleNamespaceSelector"))
	}

	enabledIn := func(manifestPath string) map[string]bool {
		on := map[string]bool{}
		for _, appset := range []string{"adhar-appset-local.yaml", "adhar-appset-production.yaml"} {
			for _, el := range loadAppSetElements(t, appset) {
				if el.ManifestPath == manifestPath && el.Enabled == "true" {
					on[appset] = true
				}
			}
		}
		return on
	}
	mimirOn, alloyOn := enabledIn("observability/mimir/manifests"), enabledIn("observability/alloy/manifests")
	if len(mimirOn) == 0 {
		t.Fatal("mimir is enabled in no profile")
	}
	root := stackPackagesDir(t)
	targeted := 0
	for _, path := range stackPackageManifests(t) {
		raw, _ := os.ReadFile(path)
		if !strings.Contains(string(raw), rulesTargetLabel) {
			continue
		}
		for _, d := range yamlDocs(t, path) {
			v, has := metaLabels(d)[rulesTargetLabel]
			if !has {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			if d["kind"] != "PrometheusRule" || v != rulesTargetMimir {
				t.Errorf("%s: %s %v carries %s=%v; the label is for PrometheusRules, and Mimir is its only target", rel, d["kind"], obsDig(d, "metadata", "name"), rulesTargetLabel, v)
				continue
			}
			targeted++
			dir := filepath.ToSlash(filepath.Dir(rel))
			for profile := range enabledIn(dir) {
				if !mimirOn[profile] || !alloyOn[profile] {
					t.Errorf("%s ships a Mimir-targeted rule in %s, where mimir or alloy is off: nothing evaluates it", rel, profile)
				}
			}
			if len(enabledIn(dir)) == 0 && dir != "observability/mimir/manifests" {
				t.Errorf("%s: its package %s is enabled nowhere", rel, dir)
			}
		}
	}
	if targeted == 0 {
		t.Error("no PrometheusRule is labelled for the Mimir ruler; the batch app alerts are gone")
	}
	// Compositions may compose a Mimir-targeted rule too; it must name the same target.
	compose, err := os.ReadFile(batchJobComposeRel)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(regexp.QuoteMeta(rulesTargetLabel)+`"?\s*:\s*"?([A-Za-z0-9_-]+)`).FindAllStringSubmatch(string(compose), -1) {
		if m[1] != rulesTargetMimir {
			t.Errorf("the BatchJob composition labels a rule %s=%s", rulesTargetLabel, m[1])
		}
	}
}

var (
	obsPromAgg     = regexp.MustCompile(`\b(by|on)\s*\(([^)]*)\)`)
	obsPromAggFree = regexp.MustCompile(`\b(without|ignoring)\s*\(`)
	obsPromIdent   = regexp.MustCompile(`[A-Za-z_:][A-Za-z0-9_:]*`)
	obsPromString  = regexp.MustCompile(`"[^"]*"`)
	obsPromBraces  = regexp.MustCompile(`\{[^}]*\}`)
)

// The loader rewrites every selector to {cluster="<this cluster>"}, recorded series
// included. A recording rule that aggregates `cluster` away records a series its
// own alert's rewritten selector can never match, and a join that ignores it joins
// two clusters' series. So every grouping keeps `cluster` and nothing drops it.
func TestMimirBatchRulesKeepTheClusterLabel(t *testing.T) {
	_, spec := obsLoadPromRule(t, mimirBatchAlerts)
	for _, g := range spec.Groups {
		for _, r := range g.Rules {
			name := r.Record + r.Alert
			for _, m := range obsPromAgg.FindAllStringSubmatch(r.Expr, -1) {
				labels := map[string]bool{}
				for _, l := range strings.Split(m[2], ",") {
					labels[strings.TrimSpace(l)] = true
				}
				if !labels["cluster"] {
					t.Errorf("%s: `%s (%s)` drops the cluster label", name, m[1], strings.TrimSpace(m[2]))
				}
			}
			if obsPromAggFree.MatchString(r.Expr) {
				t.Errorf("%s: uses without/ignoring, which cannot be checked to keep `cluster`", name)
			}
		}
	}
}

// The series the Mimir batch rules read from the kit, each as the OTLP exporter
// names it. Verified at the reader on 2026-11-14: the Micrometer names translate as
// below, and the Spring Batch `job` tag is overwritten by
// "<service.namespace>/<service.name>", so no rule may key on `job`. The observer's
// SLA gauges are NOT listed here: mimirBatchSources reads them from
// platform/controllers/batch itself (observerGauges), because a table of them in
// this package once vouched for a gauge the observer never published.
var mimirBatchSeries = map[string]string{
	"adhar_batch_last_commit_seconds":       "kit adhar.batch.last.commit (unit seconds), OTLP",
	"adhar_batch_run":                       "kit adhar.batch.run, OTLP",
	"adhar_batch_items_dead_lettered_total": "kit adhar.batch.items.dead.lettered (monotonic sum), OTLP",
}

// mimirBatchSources is every series a batch source publishes: the kit's OTLP
// meters above and the gauges the batch observer constructs.
func mimirBatchSources(t *testing.T) map[string]string {
	t.Helper()
	all := map[string]string{}
	for k, v := range mimirBatchSeries {
		all[k] = v
	}
	for name, labels := range observerGauges(t) {
		all[name] = fmt.Sprintf("batch observer (platform/controllers/batch) %v, scraped into Mimir", labels)
	}
	return all
}

// OTLP resource labels the rules key on, each from an attribute the BatchJob
// composition sets in OTEL_RESOURCE_ATTRIBUTES. adhar_team and adhar_batch_alerts
// are matched before the composition sets them (a matcher on an absent label is a
// no-op), so adding the attributes is the whole change.
var (
	composedResourceLabels = []string{"k8s_namespace_name", "k8s_pod_name", "adhar_batch_job", "adhar_application", "deployment_environment_name"}
	pendingResourceLabels  = map[string]bool{"adhar_team": true, "adhar_batch_alerts": true}
)

func TestMimirBatchAlertsReadOnlyTheSeriesTheirSourcesPublish(t *testing.T) {
	doc, spec := obsLoadPromRule(t, mimirBatchAlerts)
	sources := mimirBatchSources(t)
	if metaLabels(doc)[rulesTargetLabel] != rulesTargetMimir {
		t.Fatalf("%s is not labelled %s: %s, so Prometheus evaluates it where its series never arrive", mimirBatchAlerts, rulesTargetLabel, rulesTargetMimir)
	}
	recorded := map[string]bool{}
	alerts := map[string]bool{}
	for _, g := range spec.Groups {
		for _, r := range g.Rules {
			if r.Record != "" {
				recorded[r.Record] = true
			}
		}
	}
	for _, g := range spec.Groups {
		for _, r := range g.Rules {
			name := r.Record + r.Alert
			if r.Alert != "" {
				alerts[r.Alert] = true
				// Severity `auto`: derived from the environment, never a fixed label.
				if _, fixed := r.Labels["severity"]; fixed {
					t.Errorf("%s fixes severity %q; it is critical in prod and warning elsewhere", r.Alert, r.Labels["severity"])
				}
				if !strings.Contains(r.Expr, `"severity", "warning", "", "")`) || !strings.Contains(r.Expr, `"severity", "critical", "adhar_environment", "prod")`) {
					t.Errorf("%s does not derive severity from adhar_environment", r.Alert)
				}
			}
			// What is left once string literals, matchers and groupings are gone is
			// functions, operators and metric names.
			code := obsPromString.ReplaceAllString(r.Expr, `""`)
			if regexp.MustCompile(`(^|[^A-Za-z0-9_:])job([^A-Za-z0-9_:]|$)`).MatchString(code) {
				t.Errorf("%s keys on `job`, which the OTLP exporter overwrites", name)
			}
			code = obsPromAgg.ReplaceAllString(obsPromBraces.ReplaceAllString(code, ""), "")
			for _, id := range obsPromIdent.FindAllString(code, -1) {
				if !strings.HasPrefix(id, "adhar_batch") || recorded[id] {
					continue
				}
				if _, ok := sources[id]; !ok {
					t.Errorf("%s reads %s, which no batch source publishes under that name", name, id)
				}
			}
		}
	}
	for _, want := range []string{"BatchNoProgress", "BatchDeadLetterGrowing", "BatchDeadlineEndangered", "BatchDeadlineMissed"} {
		if !alerts[want] {
			t.Errorf("%s has no %s (design 0025 §11.3)", mimirBatchAlerts, want)
		}
	}
	// Every raw series of the table is read by some rule, and every resource label
	// the rules use is one the composition sets (or one listed as pending).
	all := ""
	for _, g := range spec.Groups {
		for _, r := range g.Rules {
			all += r.Expr + "\n"
		}
	}
	for name, src := range sources {
		if !regexp.MustCompile(`\b` + name + `\b`).MatchString(all) {
			t.Errorf("no rule reads %s (%s)", name, src)
		}
	}
	compose, err := os.ReadFile(batchJobComposeRel)
	if err != nil {
		t.Fatal(err)
	}
	attrs := regexp.MustCompile(`"OTEL_RESOURCE_ATTRIBUTES" \(printf "([^"]*)"`).FindStringSubmatch(string(compose))
	if attrs == nil {
		t.Fatal("the BatchJob composition sets no OTEL_RESOURCE_ATTRIBUTES")
	}
	set := map[string]bool{}
	for _, kv := range strings.Split(attrs[1], ",") {
		k := strings.SplitN(kv, "=", 2)[0]
		set[strings.ReplaceAll(k, ".", "_")] = true
	}
	for _, l := range composedResourceLabels {
		if !set[l] {
			t.Errorf("the rules key on %s, but the composition's OTEL_RESOURCE_ATTRIBUTES (%s) does not set it", l, attrs[1])
		}
		if !strings.Contains(all, l) {
			t.Errorf("composedResourceLabels lists %s, which no rule reads", l)
		}
	}
	for l := range pendingResourceLabels {
		if set[l] {
			t.Errorf("the composition now sets %s: move it from pendingResourceLabels to composedResourceLabels", l)
		}
	}
}

// obsPromtool writes spec as rules.yaml beside the named tests file and runs
// `promtool check rules` and `promtool test rules`. Skipped without promtool,
// failed without it under ADHAR_REQUIRE_RULE_TOOLS=1. The same check by hand:
//
//	docker run --rm --entrypoint promtool -v "$DIR":/w -w /w quay.io/prometheus/prometheus:v3.5.0 test rules tests.yaml
func obsPromtool(t *testing.T, ruleFile, tests string) {
	t.Helper()
	promtool := ruleTool(t, "see obsPromtool for the container equivalent", "promtool")
	doc, _ := obsLoadPromRule(t, ruleFile)
	spec, _ := yaml.Marshal(doc["spec"])
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rules.yaml"), spec, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests.yaml"), mustReadTestdata(t, tests), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"check", "rules", "rules.yaml"}, {"test", "rules", "tests.yaml"}} {
		cmd := exec.Command(promtool, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("promtool %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func TestMimirBatchAlertsPassTheirRuleTests(t *testing.T) {
	obsPromtool(t, mimirBatchAlerts, "testdata/batch-app-alerts/tests.yaml")
}

// The ruler's own health is Prometheus's to watch, never its own.
func TestMimirRulerAlertsPassTheirRuleTests(t *testing.T) {
	doc, _ := obsLoadPromRule(t, mimirRulerAlerts)
	if _, has := metaLabels(doc)[rulesTargetLabel]; has {
		t.Errorf("%s is labelled %s: the ruler cannot alert that it is down", mimirRulerAlerts, rulesTargetLabel)
	}
	obsPromtool(t, mimirRulerAlerts, "testdata/mimir-ruler-alerts/tests.yaml")
}

func TestBatchCostRulesPassTheirRuleTests(t *testing.T) {
	obsPromtool(t, costGovernanceRules, "testdata/batch-cost/tests.yaml")
}

// Batch cost is OpenCost's allocation of a run's pods, not kube-state-metrics'
// requests: KSM reports a finished pod's requests until the pod is deleted, which
// for a run is its Job's TTL (on 2026-11-14, 159 request series of 126 Succeeded
// pods, and no OpenCost allocation). And the group is evaluated every 60 s, which is
// what makes the dashboard's sum_over_time(...) / 60 a sum in USD.
func TestBatchCostReadsOpenCostAllocationNotRequests(t *testing.T) {
	_, spec := obsLoadPromRule(t, costGovernanceRules)
	var batch *struct {
		Name     string `json:"name"`
		Interval string `json:"interval"`
		Rules    []struct {
			Record string            `json:"record"`
			Alert  string            `json:"alert"`
			Expr   string            `json:"expr"`
			Labels map[string]string `json:"labels"`
		} `json:"rules"`
	}
	for i := range spec.Groups {
		if spec.Groups[i].Name == "adhar.cost.batch" {
			batch = &spec.Groups[i]
		}
	}
	if batch == nil {
		t.Fatal("adhar-cost-governance has no adhar.cost.batch group")
	}
	if batch.Interval != "60s" {
		t.Errorf("adhar.cost.batch is evaluated every %q; the Batch row's / 60 assumes 60s", batch.Interval)
	}
	reads := map[string]bool{}
	for _, r := range batch.Rules {
		if strings.Contains(r.Expr, "kube_pod_container_resource_requests") {
			t.Errorf("%s prices requests, which a finished run's pod keeps reporting until its TTL", r.Record)
		}
		for _, n := range []string{"container_cpu_allocation", "container_memory_allocation_bytes", "node_cpu_hourly_cost", "node_ram_hourly_cost"} {
			if regexp.MustCompile(`\b` + n + `\{job="opencost"\}`).MatchString(r.Expr) {
				reads[n] = true
			}
		}
	}
	for _, n := range []string{"container_cpu_allocation", "container_memory_allocation_bytes", "node_cpu_hourly_cost", "node_ram_hourly_cost"} {
		if !reads[n] {
			t.Errorf("adhar.cost.batch does not read OpenCost's %s{job=\"opencost\"}", n)
		}
	}
}

// The OpenCost view: Observability / Cost has a Batch row with spend by team, by
// job and per run, reading only series the adhar.cost.batch group records, each
// over the range as sum_over_time(...) / 60.
func TestCostDashboardShowsBatchSpendByTeamJobAndRun(t *testing.T) {
	_, spec := obsLoadPromRule(t, costGovernanceRules)
	recorded := map[string]bool{}
	for _, g := range spec.Groups {
		if g.Name != "adhar.cost.batch" {
			continue
		}
		for _, r := range g.Rules {
			recorded[r.Record] = true
		}
	}
	d, _ := loadBuiltDashboard(t, "observability-cost")
	var row *gPanel
	for i := range d.Panels {
		if d.Panels[i].Type == "row" && d.Panels[i].Title == "Batch" {
			row = &d.Panels[i]
		}
	}
	if row == nil {
		t.Fatal("Observability / Cost has no Batch row")
	}
	read := map[string]bool{}
	for _, p := range row.Panels {
		for _, tg := range p.Targets {
			expr, _ := tg["expr"].(string)
			for _, name := range obsPromIdent.FindAllString(expr, -1) {
				if strings.HasPrefix(name, "adhar:batch_") {
					if !recorded[name] {
						t.Errorf("panel %q reads %s, which adhar.cost.batch does not record", p.Title, name)
					}
					read[name] = true
				}
			}
			if strings.Contains(expr, "sum_over_time(") && !strings.HasSuffix(strings.TrimSpace(expr), "/ 60") {
				t.Errorf("panel %q sums a recorded hourly rate over the range without dividing by 60: %s", p.Title, expr)
			}
		}
	}
	var want []string
	for _, n := range []string{"adhar:batch_team_cost_usd:hourly_total", "adhar:batch_job_cost_usd:hourly_total", "adhar:batch_run_cost_usd:hourly"} {
		if !read[n] {
			want = append(want, n)
		}
	}
	sort.Strings(want)
	if len(want) > 0 {
		t.Errorf("the Batch row does not show %v (spend by team, by job and per run)", want)
	}
}
