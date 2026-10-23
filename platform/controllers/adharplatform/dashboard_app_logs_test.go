package adharplatform

// Application Logs: the Alloy pod-log pipeline tags custom-application streams
// (observability/alloy/values.yaml) and one dashboard in the Applications folder
// reads those tags (kube-prometheus/dash_applications.py), pinned on every other.
//
// Before 2026-10-23 a custom app's pods carried only `app=<name>` in Loki, and its
// database, cache and event stream other names again (postgresql, <app>-cache,
// entity-operator, kafka), so "show me billing's logs in prod" meant knowing every
// pod name. The labels must appear ONLY in workload namespaces: on platform streams
// they would add series for nothing, and an `All` selection would drag adhar-system in.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// alloyConfig returns the Alloy config from values.yaml and from the rendered
// ConfigMap in manifests/install.yaml (they must be the same text).
func alloyConfig(t *testing.T) (fromValues, rendered string) {
	t.Helper()
	var values struct {
		Alloy struct {
			ConfigMap struct {
				Content string `yaml:"content"`
			} `yaml:"configMap"`
		} `yaml:"alloy"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, "observability/alloy/values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(strings.NewReader(readStackFile(t, "observability/alloy/manifests/install.yaml")))
	for {
		var doc struct {
			Kind string            `yaml:"kind"`
			Data map[string]string `yaml:"data"`
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc.Kind == "ConfigMap" && doc.Data["config.alloy"] != "" {
			rendered = doc.Data["config.alloy"]
		}
	}
	return values.Alloy.ConfigMap.Content, rendered
}

// alloyBlock returns the body of the top-level block `<header> {` ... `}`.
func alloyBlock(t *testing.T, cfg, header string) string {
	t.Helper()
	start := strings.Index(cfg, header+" {\n")
	if start < 0 {
		t.Fatalf("Alloy config has no %s block", header)
	}
	body := cfg[start+len(header)+3:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("%s block is not closed", header)
	}
	return body[:end]
}

type relabelRule struct {
	sources     []string
	separator   string
	regex       string
	target      string
	replacement string
	action      string
}

var (
	ruleBlock = regexp.MustCompile(`(?s)\trule \{\n(.*?)\n\t\}`)
	ruleAttr  = regexp.MustCompile(`(?m)^\t\t(\w+)\s*=\s*(.+)$`)
	quoted    = regexp.MustCompile(`"([^"]*)"`)
)

func parseRelabelRules(t *testing.T, block string) []relabelRule {
	t.Helper()
	var rules []relabelRule
	for _, m := range ruleBlock.FindAllStringSubmatch(block, -1) {
		r := relabelRule{separator: ";", regex: "(.*)", replacement: "$1", action: "replace"}
		for _, a := range ruleAttr.FindAllStringSubmatch(m[1], -1) {
			var vals []string
			for _, q := range quoted.FindAllStringSubmatch(a[2], -1) {
				vals = append(vals, q[1])
			}
			if len(vals) == 0 {
				t.Fatalf("cannot parse rule attribute %q", a[0])
			}
			switch a[1] {
			case "source_labels":
				r.sources = vals
			case "separator":
				r.separator = vals[0]
			case "regex":
				r.regex = vals[0]
			case "target_label":
				r.target = vals[0]
			case "replacement":
				r.replacement = vals[0]
			case "action":
				r.action = vals[0]
			}
		}
		rules = append(rules, r)
	}
	return rules
}

// relabel applies replace/keep rules with Prometheus semantics (anchored regex,
// an empty result deletes the target) and drops the `__` labels, as
// loki.source.kubernetes does when it builds a stream.
func relabel(t *testing.T, rules []relabelRule, in map[string]string) map[string]string {
	t.Helper()
	lb := map[string]string{}
	for k, v := range in {
		lb[k] = v
	}
	for _, r := range rules {
		vals := make([]string, len(r.sources))
		for i, s := range r.sources {
			vals[i] = lb[s]
		}
		val := strings.Join(vals, r.separator)
		re := regexp.MustCompile("^(?:" + r.regex + ")$")
		m := re.FindStringSubmatchIndex(val)
		switch r.action {
		case "keep":
			if m == nil {
				return nil
			}
		case "replace":
			if m == nil {
				continue
			}
			res := string(re.ExpandString(nil, regexp.MustCompile(`\$(\d+)`).ReplaceAllString(r.replacement, "$${$1}"), val, m))
			if res == "" {
				delete(lb, r.target)
			} else {
				lb[r.target] = res
			}
		default:
			t.Fatalf("relabel action %q not simulated", r.action)
		}
	}
	out := map[string]string{}
	for k, v := range lb {
		if !strings.HasPrefix(k, "__") {
			out[k] = v
		}
	}
	return out
}

func TestAlloyTagsCustomApplicationLogsOnlyInWorkloadNamespaces(t *testing.T) {
	fromValues, rendered := alloyConfig(t)
	if fromValues == "" || rendered != fromValues {
		t.Fatal("manifests/install.yaml's config.alloy differs from values.yaml's alloy.configMap.content: " +
			"the generator's source and its output disagree")
	}
	// The namespace's labels reach pod targets only with attach_metadata.namespace.
	pods := alloyBlock(t, fromValues, `discovery.kubernetes "pods"`)
	if !regexp.MustCompile(`(?s)attach_metadata \{\s*namespace = true\s*\}`).MatchString(pods) {
		t.Error(`discovery.kubernetes "pods" has no attach_metadata { namespace = true }: ` +
			"__meta_kubernetes_namespace_label_* never exists, so no stream gets an environment")
	}
	rules := parseRelabelRules(t, alloyBlock(t, fromValues, `discovery.relabel "pod_logs"`))
	if len(rules) < 5 {
		t.Fatalf("parsed only %d pod_logs relabel rules", len(rules))
	}

	ns := func(env string) map[string]string {
		m := map[string]string{
			"__meta_kubernetes_namespace":                        env,
			"__meta_kubernetes_namespace_label_adhar_io_plane":   "workload",
			"__meta_kubernetes_namespace_label_adhar_io_team":    "platform",
			"__meta_kubernetes_namespace_label_adhar_io_project": "shop",
		}
		if env != "adhar" { // the Kargo project namespace has no environment
			m["__meta_kubernetes_namespace_label_adhar_io_environment"] = env
		}
		return m
	}
	with := func(base map[string]string, kv ...string) map[string]string {
		out := map[string]string{"__meta_kubernetes_pod_name": "p-0", "__meta_kubernetes_pod_container_name": "c",
			"__meta_kubernetes_pod_node_name": "n1"}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	const (
		app      = "__meta_kubernetes_pod_label_app"
		instance = "__meta_kubernetes_pod_label_app_kubernetes_io_instance"
		name     = "__meta_kubernetes_pod_label_app_kubernetes_io_name"
	)
	cases := []struct {
		name string
		in   map[string]string
		want map[string]string // adhar_* labels only; nil = none at all
	}{
		{"platform pod", with(map[string]string{"__meta_kubernetes_namespace": "adhar-system"}, app, "keycloak", instance, "keycloak"), nil},
		{"platform pod whose instance looks like a backing service",
			with(map[string]string{"__meta_kubernetes_namespace": "adhar-system"}, instance, "keycloak-db", name, "postgresql"), nil},
		{"the app's own pods", with(ns("dev"), app, "reactapp"),
			map[string]string{"adhar_application": "reactapp", "adhar_component": "app", "adhar_environment": "dev", "adhar_team": "platform", "adhar_project": "shop"}},
		{"CNPG instance", with(ns("prod"), instance, "transaction-service-db", name, "postgresql"),
			map[string]string{"adhar_application": "transaction-service", "adhar_component": "db", "adhar_environment": "prod", "adhar_team": "platform", "adhar_project": "shop"}},
		{"Valkey cache", with(ns("test"), app, "transaction-service-cache"),
			map[string]string{"adhar_application": "transaction-service", "adhar_component": "cache", "adhar_environment": "test", "adhar_team": "platform", "adhar_project": "shop"}},
		{"Strimzi event stream", with(ns("dev"), instance, "transaction-service-events", name, "kafka-exporter"),
			map[string]string{"adhar_application": "transaction-service", "adhar_component": "events", "adhar_environment": "dev", "adhar_team": "platform", "adhar_project": "shop"}},
		{"explicit pod labels win", with(ns("dev"), app, "billing-worker",
			"__meta_kubernetes_pod_label_adhar_io_application", "billing", "__meta_kubernetes_pod_label_adhar_io_component", "worker"),
			map[string]string{"adhar_application": "billing", "adhar_component": "worker", "adhar_environment": "dev", "adhar_team": "platform", "adhar_project": "shop"}},
		{"workload namespace without an environment", with(ns("adhar"), app, "seed"),
			map[string]string{"adhar_application": "seed", "adhar_component": "app", "adhar_team": "platform", "adhar_project": "shop"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]string{}
			out := relabel(t, rules, c.in)
			for k, v := range out {
				if strings.HasPrefix(k, "adhar_") {
					got[k] = v
				}
			}
			if out["namespace"] != c.in["__meta_kubernetes_namespace"] {
				t.Errorf("namespace label lost: %v", out)
			}
			if len(got) != len(c.want) {
				t.Errorf("adhar_* labels = %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
		})
	}
}

// The dashboard reads only the new labels, so `All` cannot match a platform stream,
// and its pickers list what the pipeline writes.
func TestApplicationLogsDashboardSelectsOnlyApplicationStreams(t *testing.T) {
	d, body := loadBuiltDashboard(t, "app-logs")
	if d.UID != "adhar-app-logs" || d.Title != "Application Logs" {
		t.Fatalf("uid/title = %q/%q", d.UID, d.Title)
	}
	var raw struct {
		Templating struct {
			List []struct {
				Name       string `json:"name"`
				Definition string `json:"definition"`
				AllValue   string `json:"allValue"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	for _, v := range raw.Templating.List {
		vars[v.Name] = v.Definition
		if (v.Name == "environment" || v.Name == "application" || v.Name == "component") && v.AllValue != ".+" {
			t.Errorf("$%s: All must be .+ (Loki refuses an all-empty-matching selector, and .+ keeps unlabelled streams out), got %q", v.Name, v.AllValue)
		}
	}
	for name, label := range map[string]string{"environment": "adhar_environment", "application": "adhar_application", "component": "adhar_component"} {
		if !strings.HasPrefix(vars[name], "label_values(") || !strings.HasSuffix(vars[name], ", "+label+")") {
			t.Errorf("$%s is not label_values(…, %s): %q", name, label, vars[name])
		}
	}
	selector := regexp.MustCompile(`\{([^{}]*)\}`)
	var walk func(ps []gPanel)
	n := 0
	walk = func(ps []gPanel) {
		for _, p := range ps {
			walk(p.Panels)
			for _, tg := range p.Targets {
				expr, _ := tg["expr"].(string)
				ds, _ := tg["datasource"].(map[string]any)
				if ds["uid"] != "loki" {
					t.Errorf("panel %q queries %v, not Loki", p.Title, ds)
				}
				for _, m := range selector.FindAllStringSubmatch(expr, -1) {
					if !strings.Contains(m[1], "=~") || strings.HasPrefix(strings.TrimSpace(m[1]), ".") {
						continue // a template ({{ .message }}), not a stream selector
					}
					n++
					for _, l := range []string{`adhar_environment=~"$environment"`, `adhar_application=~"$application"`, `adhar_component=~"$component"`} {
						if !strings.Contains(m[1], l) {
							t.Errorf("panel %q: selector {%s} lacks %s", p.Title, m[1], l)
						}
					}
				}
			}
		}
	}
	walk(d.Panels)
	if n < 15 {
		t.Fatalf("found only %d stream selectors", n)
	}
}

// Pinned: the platform front page leads its link bar with Application Logs, and every
// other built dashboard carries it in its link bar.
func TestEveryBuiltDashboardPinsApplicationLogs(t *testing.T) {
	type link struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	linksOf := func(body string) []link {
		var d struct {
			Links []link `json:"links"`
		}
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		return d.Links
	}
	for _, name := range builtDashboardNames(t) {
		if name == "app-logs" {
			continue
		}
		_, body := loadBuiltDashboard(t, name)
		found := false
		for _, l := range linksOf(body) {
			found = found || (l.Title == "Application Logs" && l.URL == "/d/adhar-app-logs")
		}
		if !found {
			t.Errorf("dashboard %s does not pin Application Logs in its link bar (dash_common.links)", name)
		}
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, "observability/kube-prometheus/manifests/dashboard-adhar-platform.yaml")), &cm); err != nil {
		t.Fatal(err)
	}
	ls := linksOf(cm.Data["adhar-platform.json"])
	if len(ls) == 0 || ls[0].Title != "Application Logs" || ls[0].URL != "/d/adhar-app-logs" {
		t.Errorf("the Adhar Platform link bar does not lead with Application Logs: %v", ls)
	}
}
