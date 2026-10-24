package adharplatform

// One per-application dashboard TEMPLATE per kind of application
// (hack/dashboards/build-app-dashboard.py): a backend has no browser and a
// single-page app has no JVM, and one dashboard for every app made each of them
// scroll past sections that could never hold data. These tests pin which
// template an Application gets, and that each template carries its own
// sections and none of another kind's.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var dashboardTypes = []string{"backend-jvm", "backend-generic", "frontend", "fullstack"}

// composedDashboardConfigMap renders the composition for app `cart` and returns
// the dashboard ConfigMap.
func composedDashboardConfigMap(t *testing.T, extraParams string, labels map[string]any) map[string]any {
	t.Helper()
	req := applicationRequest(t, "cart", extraParams)
	if labels != nil {
		xr := req["observed"].(map[string]any)["composite"].(map[string]any)["resource"].(map[string]any)
		xr["metadata"].(map[string]any)["labels"] = labels
	}
	docs := renderGoTemplate(t, req, "compositions", "apps", "argocd-multienv.yaml")
	cm := composedManifest(docs, "ConfigMap", "app-dashboard-cart")
	if cm == nil {
		t.Fatal("the Application composition composes no app-dashboard-cart ConfigMap")
	}
	return cm
}

func dashboardOfType(t *testing.T, kind string) (string, paDashboard) {
	t.Helper()
	cm := composedDashboardConfigMap(t, fmt.Sprintf("    dashboard: {type: %s}\n", kind), nil)
	body, _ := dig(cm, "data", "cart.json").(string)
	d, problems := paCheckDashboard(body, paProvisionedDatasourceUIDs(t))
	for _, p := range problems {
		t.Errorf("%s: %s", kind, p)
	}
	return body, d
}

// The selection rule: the explicit field, then the label a scaffolder stamps,
// then browser-app inference, then the generic backend.
func TestEachApplicationGetsTheDashboardOfItsKind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		labels map[string]any
		want   string
	}{
		{name: "nothing said: a generic backend", want: "backend-generic"},
		{name: "a worker", params: "    type: worker\n", want: "backend-generic"},
		{name: "type web", params: "    type: web\n", want: "frontend"},
		{name: "the SPA descriptors' frontend label", labels: map[string]any{"adhar.io/frontend": "true"}, want: "frontend"},
		{name: "frontend label false", labels: map[string]any{"adhar.io/frontend": "false"}, want: "backend-generic"},
		{name: "the dashboard-type label", labels: map[string]any{"adhar.io/dashboard-type": "backend-jvm"}, want: "backend-jvm"},
		{name: "the label beats inference", labels: map[string]any{"adhar.io/dashboard-type": "fullstack", "adhar.io/frontend": "true"}, want: "fullstack"},
		{name: "the field beats the label", params: "    dashboard: {type: backend-jvm}\n", labels: map[string]any{"adhar.io/dashboard-type": "frontend"}, want: "backend-jvm"},
		{name: "the field beats inference", params: "    type: web\n    dashboard: {type: fullstack}\n", want: "fullstack"},
		{name: "auto defers to the label", params: "    dashboard: {type: auto}\n", labels: map[string]any{"adhar.io/dashboard-type": "backend-jvm"}, want: "backend-jvm"},
		{name: "auto defers to inference", params: "    type: web\n    dashboard: {type: auto}\n", want: "frontend"},
		{name: "an unknown label value is ignored", labels: map[string]any{"adhar.io/dashboard-type": "cobol", "adhar.io/frontend": "true"}, want: "frontend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := composedDashboardConfigMap(t, tc.params, tc.labels)
			if got := dig(cm, "metadata", "labels", "adhar.io/dashboard-type"); got != tc.want {
				t.Errorf("ConfigMap label adhar.io/dashboard-type = %v, want %s", got, tc.want)
			}
			body, _ := dig(cm, "data", "cart.json").(string)
			var d paDashboard
			if err := json.Unmarshal([]byte(body), &d); err != nil {
				t.Fatalf("not valid JSON: %v", err)
			}
			if !containsString(d.Tags, tc.want) {
				t.Errorf("the dashboard is tagged %v; it is not the %s template", d.Tags, tc.want)
			}
		})
	}
}

// panelsDeep flattens rows into their panels, keeping the row title.
func panelsDeep(d paDashboard) (rows []string, byRow map[string][]paPanel) {
	byRow = map[string][]paPanel{}
	current := ""
	for _, p := range d.Panels {
		if p.Type == "row" {
			current = p.Title
			rows = append(rows, p.Title)
			byRow[current] = append(byRow[current], p.Panels...)
			continue
		}
		byRow[current] = append(byRow[current], p)
	}
	return rows, byRow
}

func queriesOf(ps []paPanel) string {
	var b strings.Builder
	for _, p := range ps {
		for _, tg := range p.Targets {
			b.WriteString(tg.Expr + tg.Query + "\n")
		}
	}
	return b.String()
}

// Each template carries its kind's sections and none of another kind's.
func TestEachDashboardTemplateCarriesOnlyItsKindsSections(t *testing.T) {
	type spec struct {
		rows      []string
		want      []string
		forbidden []string
	}
	faro, jvm, scrapedRED := `source="faro"`, "jvm_", "http_server_requests_seconds"
	specs := map[string]spec{
		"backend-jvm": {
			rows:      []string{"Overview", "Golden Signals (RED)", "Dependencies (what this service calls)", "JVM", "Business Metrics (adhar-kit)", "Saturation & Resources", "Logs (Loki)", "Traces (Tempo)"},
			want:      []string{scrapedRED, "jvm_memory_used_bytes", "hikaricp_connections_active", "kafka_consumer_fetch_manager_records_lag_max", "traces_service_graph_request_total", "adhar_business_"},
			forbidden: []string{faro, "pattern ", "nodejs_", "go_goroutines", "service_name=\"cart\""},
		},
		"backend-generic": {
			rows: []string{"Overview", "Golden Signals (RED)", "Dependencies (what this service calls)", "Runtime (Go / Python / Node.js / process)", "Saturation & Resources", "Logs (Loki)", "Traces (Tempo)"},
			// Both feeds: Micrometer-named scrapes and every OTLP HTTP convention.
			want: []string{scrapedRED, "http_server_request_duration_seconds_bucket", "http_server_duration_milliseconds_bucket",
				"http_server_requests_milliseconds_bucket", `service_name="cart",k8s_namespace_name=~"$env|"`,
				"go_goroutines", "nodejs_eventloop_lag_p99_seconds", "process_runtime_cpython", "container_pressure_cpu_waiting_seconds_total",
				"traces_service_graph_request_total"},
			forbidden: []string{faro, jvm, "hikaricp_", "adhar_business_", "pattern "},
		},
		"frontend": {
			rows:      []string{"Overview", frontendRowTitle, "Browser Errors", "Browser Network (fetch / XHR)", "Pages, Sessions & Releases", "Served by the Pod (nginx)", "Saturation & Resources", "Logs (Loki)", "Traces (Tempo)"},
			want:      []string{faro, "unwrap lcp", "browser_name", "app_version", "page_url", "event_data_httpHost", "pattern ", "kube-probe"},
			forbidden: []string{jvm, scrapedRED, "hikaricp_", "traces_service_graph", "nodejs_", "service_name="},
		},
		"fullstack": {
			rows: []string{"Overview", frontendRowTitle, "Browser Errors", "Browser Network (fetch / XHR)", "Pages, Sessions & Releases",
				"Server: Golden Signals (RED)", "Server: Node.js Runtime", "Dependencies (what this service calls)", "Saturation & Resources", "Logs (Loki)", "Traces (Tempo)"},
			want:      []string{faro, "unwrap inp", scrapedRED, "nodejs_eventloop_lag_p99_seconds", "nodejs_heap_size_used_bytes", "traces_service_graph_request_total"},
			forbidden: []string{jvm, "hikaricp_", "adhar_business_", "pattern "},
		},
	}
	for _, kind := range dashboardTypes {
		t.Run(kind, func(t *testing.T) {
			sp := specs[kind]
			body, d := dashboardOfType(t, kind)
			rows, byRow := panelsDeep(d)
			if strings.Join(rows, " | ") != strings.Join(sp.rows, " | ") {
				t.Errorf("rows are\n  %v\nwant\n  %v", rows, sp.rows)
			}
			var all []paPanel
			for _, ps := range byRow {
				all = append(all, ps...)
			}
			q := queriesOf(all)
			for _, w := range sp.want {
				if !strings.Contains(q, w) {
					t.Errorf("the %s dashboard never queries %q", kind, w)
				}
			}
			for _, f := range sp.forbidden {
				if strings.Contains(q, f) {
					t.Errorf("the %s dashboard queries %q, which belongs to another kind of application", kind, f)
				}
			}
			if left := regexp.MustCompile(`__[A-Z_]+__`).FindAllString(body, -1); len(left) > 0 {
				t.Errorf("unfilled placeholders: %v", left)
			}
		})
	}
}

// Health at a glance: every template opens on a colour-coded row of stats, every
// panel says what it shows, detail rows fold away, and the shared furniture
// (links, env/pod variables, deploy annotations) is on every one.
func TestEveryDashboardTemplateOpensOnAColouredHealthRow(t *testing.T) {
	for _, kind := range dashboardTypes {
		t.Run(kind, func(t *testing.T) {
			_, d := dashboardOfType(t, kind)
			if len(d.Panels) == 0 || d.Panels[0].Type != "row" || d.Panels[0].Title != "Overview" || d.Panels[0].Collapsed {
				t.Fatal("the dashboard does not open on an expanded Overview row")
			}
			var health []paPanel
			for _, p := range d.Panels[1:] {
				if p.Type == "row" {
					break
				}
				health = append(health, p)
			}
			coloured := 0
			for _, p := range health {
				if p.Type != "stat" {
					t.Errorf("the health row holds a %s (%q); it is stats only", p.Type, p.Title)
				}
				if p.Options.ColorMode == "background" {
					coloured++
				}
			}
			if len(health) < 14 || coloured < 12 {
				t.Errorf("the health row has %d stats, %d of them colour-coded; want ≥14 and ≥12", len(health), coloured)
			}
			// Overview + the kind's primary section are open, the detail rows fold.
			open := 0
			_, byRow := panelsDeep(d)
			for _, p := range d.Panels {
				if p.Type == "row" && !p.Collapsed {
					open++
				}
			}
			if open != 2 {
				t.Errorf("%d rows are expanded; want Overview and the primary section only", open)
			}
			for row, ps := range byRow {
				for _, p := range ps {
					if p.Type != "text" && strings.TrimSpace(p.Description) == "" {
						t.Errorf("row %q: panel %q has no description", row, p.Title)
					}
				}
			}
			links := map[string]bool{}
			for _, l := range d.Links {
				links[l.Title] = true
			}
			// Console and Adhar Platform were dropped on 2026-10-24 to keep the
			// controls on one row (TestApplicationDashboardsHaveACompactLinkBar).
			for _, want := range []string{"Argo CD", "Kargo", "Repository", "Application Logs", "Applications"} {
				if !links[want] {
					t.Errorf("no %q link", want)
				}
			}
			vars := map[string]bool{}
			for _, v := range d.Templating.List {
				vars[v.Name] = true
			}
			if !vars["env"] || !vars["pod"] || !vars["search"] {
				t.Errorf("variables are %v; env, pod and search are on every template", vars)
			}
			if vars["business_metric"] != (kind == "backend-jvm") {
				t.Error("the business_metric variable belongs to the JVM template (adhar-kit) only")
			}
			if len(d.Annotations.List) < 4 {
				t.Error("the deploy / sync / restart annotations are missing")
			}
		})
	}
}

// A Loki stat or table wants ONE value: an instant query. Without queryType a
// Loki stat runs a range query and shows the last bucket only. This walks the
// collapsed rows too, which is where most of each template's tables live.
func TestEveryLokiStatAndTableIsAnInstantQuery(t *testing.T) {
	for _, kind := range dashboardTypes {
		body, _ := dashboardOfType(t, kind)
		var raw struct {
			Panels []json.RawMessage `json:"panels"`
		}
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		type panel struct {
			Type    string            `json:"type"`
			Title   string            `json:"title"`
			Panels  []json.RawMessage `json:"panels"`
			Targets []struct {
				Datasource struct {
					UID string `json:"uid"`
				} `json:"datasource"`
				QueryType string `json:"queryType"`
			} `json:"targets"`
		}
		seen := 0
		var walk func([]json.RawMessage)
		walk = func(ps []json.RawMessage) {
			for _, r := range ps {
				var p panel
				_ = json.Unmarshal(r, &p)
				walk(p.Panels)
				for _, tg := range p.Targets {
					if tg.Datasource.UID == "loki" && (p.Type == "stat" || p.Type == "table") {
						seen++
						if tg.QueryType != "instant" {
							t.Errorf("%s: Loki %s %q runs queryType %q, want instant", kind, p.Type, p.Title, tg.QueryType)
						}
					}
				}
			}
		}
		walk(raw.Panels)
		if kind != "backend-generic" && seen == 0 {
			t.Errorf("%s: found no Loki stat/table; the check is not looking at the template", kind)
		}
	}
}

// The XRD's enum, the composition's templates and the generator's types are the
// same four (plus `auto`). The XRD is strict: a value outside the enum is
// rejected, and a template the composition binds but the enum lacks can never
// be selected by the field.
func TestTheApplicationXRDNamesEveryDashboardTemplate(t *testing.T) {
	var xrd map[string]any
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, "xrd", "apps.xrd.yaml")), &xrd); err != nil {
		t.Fatal(err)
	}
	versions, _ := dig(xrd, "spec", "versions").([]any)
	v0, _ := versions[0].(map[string]any)
	dash, _ := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties", "dashboard").(map[string]any)
	if dash == nil {
		t.Fatal("the Application XRD has no spec.parameters.dashboard; an XR setting it is rejected (the XRD is strict)")
	}
	if dig(dash, "type") != "object" || dig(dash, "additionalProperties") != nil || dig(dash, "x-kubernetes-preserve-unknown-fields") != nil {
		t.Error("spec.parameters.dashboard must be a strict object")
	}
	enum, _ := dig(dash, "properties", "type", "enum").([]any)
	var got []string
	for _, e := range enum {
		got = append(got, fmt.Sprint(e))
	}
	sort.Strings(got)
	want := append([]string{"auto"}, dashboardTypes...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dashboard.type enum = %v, want %v", got, want)
	}
	if dig(dash, "properties", "type", "default") != "auto" {
		t.Error("dashboard.type must default to auto, so existing XRs keep the inferred template")
	}

	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	m := regexp.MustCompile(`\$dashTemplates := dict (.*) -}}`).FindStringSubmatch(comp)
	if m == nil {
		t.Fatal("the composition no longer binds its dashboard templates in $dashTemplates")
	}
	var keys []string
	for _, k := range regexp.MustCompile(`"([a-z-]+)" \$appDash`).FindAllStringSubmatch(m[1], -1) {
		keys = append(keys, k[1])
	}
	sort.Strings(keys)
	types := append([]string(nil), dashboardTypes...)
	sort.Strings(types)
	if strings.Join(keys, ",") != strings.Join(types, ",") {
		t.Errorf("the composition binds templates %v, want %v", keys, types)
	}

	gen, err := os.ReadFile(filepath.Join(stackRoot(t), "..", "..", "hack", "dashboards", "build-app-dashboard.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gen), `TYPES = ("backend-jvm", "backend-generic", "frontend", "fullstack")`) {
		t.Error("the generator's TYPES no longer match the four templates the composition selects between")
	}
}

// Logs are selected by the labels Alloy stamps on workload streams
// (adhar_application / adhar_environment / adhar_component), with the
// environment NAME — not the namespace — and a fallback for lines ingested
// before those labels existed that cannot double-count (adhar_application="").
// The link bar opens the Application Logs dashboard on this app and environment.
func TestAppLogsUseTheApplicationLabelsWithALegacyFallback(t *testing.T) {
	for _, kind := range dashboardTypes {
		_, d := dashboardOfType(t, kind)
		_, byRow := panelsDeep(d)
		q := queriesOf(byRow["Logs (Loki)"])
		for _, want := range []string{
			`{adhar_application="cart", adhar_environment="${env:text}", adhar_component=~"$component"}`,
			`{namespace="$env", app="cart", pod=~"$pod", adhar_application=""}`,
		} {
			if !strings.Contains(q, want) {
				t.Errorf("%s: the Logs row never selects %s", kind, want)
			}
		}
		if strings.Contains(q, `adhar_environment="$env"`) || strings.Contains(q, `adhar_environment=~"$env"`) {
			t.Errorf("%s: adhar_environment is the environment name, not the namespace $env holds", kind)
		}
		for _, p := range byRow["Logs (Loki)"] {
			if p.Type == "logs" && len(p.Targets) != 2 {
				t.Errorf("%s: the Logs panel has %d queries; want the labelled streams and the legacy ones", kind, len(p.Targets))
			}
		}
		found := false
		for _, l := range d.Links {
			if l.Title == "Application Logs" {
				found = true
				if l.URL != "/d/adhar-app-logs?var-application=cart&var-environment=${env:text}&${__url_time_range}" {
					t.Errorf("%s: Application Logs link is %q", kind, l.URL)
				}
			}
		}
		if !found {
			t.Errorf("%s: no Application Logs link", kind)
		}
		vars := map[string]bool{}
		for _, v := range d.Templating.List {
			vars[v.Name] = true
		}
		if !vars["component"] {
			t.Errorf("%s: no 'component' variable to show or hide the db/cache/events logs", kind)
		}
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
