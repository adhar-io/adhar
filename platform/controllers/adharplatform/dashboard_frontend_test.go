package adharplatform

// The Frontend section of the per-application dashboard: what real users
// experience in the browser, from Grafana Faro. A single-page app served by
// nginx publishes no server metrics, so without it the dashboard of a frontend
// only explained, panel after panel, that there was nothing to show. The golden
// path frontends load the Faro Web SDK; the platform's Faro receiver writes
// their signals to Loki under {source="faro", app_name, app_environment, kind}
// and their spans to Tempo, and this section reads exactly that.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const frontendRowTitle = "Frontend (Faro: real users)"

// composedAppDashboardFor renders the Application composition for app `cart`
// with extra spec.parameters and XR labels, and returns its dashboard JSON.
func composedAppDashboardFor(t *testing.T, extraParams string, labels map[string]any) string {
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
	body, _ := dig(cm, "data", "cart.json").(string)
	if body == "" {
		t.Fatal("app-dashboard-cart carries no cart.json")
	}
	return body
}

// browserRows are the rows of the browser (Faro) section: the open KPI row and
// its three detail rows.
var browserRows = []string{frontendRowTitle, "Browser Errors", "Browser Network (fetch / XHR)", "Pages, Sessions & Releases"}

// frontendSection returns the Frontend row and every panel of the browser
// section, wherever they sit: nested in a collapsed row, or following an
// expanded one at the top level.
func frontendSection(t *testing.T, d paDashboard) (row paPanel, panels []paPanel) {
	t.Helper()
	found := false
	in := false
	for _, p := range d.Panels {
		if p.Type == "row" {
			in = false
			for _, r := range browserRows {
				if p.Title == r {
					in = true
				}
			}
			if p.Title == frontendRowTitle {
				row, found = p, true
			}
			if in {
				panels = append(panels, p.Panels...)
			}
			continue
		}
		if in {
			panels = append(panels, p)
		}
	}
	if !found {
		t.Fatalf("the application dashboard has no %q row", frontendRowTitle)
	}
	return row, panels
}

// The browser section belongs to browser apps only, open and right below the
// overview; a backend's dashboard has no trace of it (it used to carry it as a
// closed row on every service).
func TestTheFrontendSectionOpensForBrowserApps(t *testing.T) {
	uids := paProvisionedDatasourceUIDs(t)
	for _, tc := range []struct {
		name    string
		params  string
		labels  map[string]any
		browser bool
	}{
		{name: "a service", browser: false},
		{name: "a worker", params: "    type: worker\n", browser: false},
		{name: "a web app", params: "    type: web\n", browser: true},
		{name: "an app labelled as a frontend", labels: map[string]any{"adhar.io/frontend": "true"}, browser: true},
		{name: "an app labelled as not a frontend", labels: map[string]any{"adhar.io/frontend": "false"}, browser: false},
		{name: "a fullstack app", params: "    dashboard: {type: fullstack}\n", browser: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := composedAppDashboardFor(t, tc.params, tc.labels)
			d, problems := paCheckDashboard(body, uids)
			for _, p := range problems {
				t.Errorf("%s", p)
			}
			if !tc.browser {
				if strings.Contains(body, frontendRowTitle) || strings.Contains(body, `source="faro"`) {
					t.Fatal("a backend's dashboard carries the browser (Faro) section")
				}
				return
			}
			row, panels := frontendSection(t, d)
			if row.Collapsed {
				t.Error("the Frontend row of a browser app is collapsed")
			}
			if len(panels) < 30 {
				t.Errorf("the browser section holds %d panels, want the full section", len(panels))
			}
			var order []string
			for _, p := range d.Panels {
				if p.Type == "row" {
					order = append(order, p.Title)
				}
			}
			if len(order) < 2 || order[0] != "Overview" || order[1] != frontendRowTitle {
				t.Errorf("rows are %v; the Frontend section belongs right below Overview", order)
			}
		})
	}
}

func TestTheFrontendSectionReadsWhatFaroWrites(t *testing.T) {
	body := composedAppDashboardFor(t, "    type: web\n", nil)
	d, _ := paCheckDashboard(body, paProvisionedDatasourceUIDs(t))
	_, panels := frontendSection(t, d)

	var queries strings.Builder
	for _, p := range panels {
		for _, tg := range p.Targets {
			queries.WriteString(tg.Expr + tg.Query + "\n")
			if uid := paDSUID(tg.Datasource); uid != "loki" && uid != "tempo" {
				t.Errorf("panel %q queries %q; browser telemetry lives in Loki and Tempo only", p.Title, uid)
			}
			// Every Loki query selects this app's Faro streams in the chosen environment.
			if paDSUID(tg.Datasource) == "loki" &&
				!strings.Contains(tg.Expr, `source="faro",app_name="cart",app_environment="${env:text}"`) {
				t.Errorf("panel %q does not select the app's Faro streams: %s", p.Title, tg.Expr)
			}
		}
	}
	q := queries.String()
	for _, want := range []string{
		`kind="measurement"`, `type="web-vitals"`,
		"unwrap lcp", "unwrap inp", "unwrap cls", "unwrap fcp", "unwrap ttfb", "quantile_over_time(0.75",
		`kind="exception"`, `event_name="faro.performance.navigation"`, `event_name="session_start"`,
		`event_name="faro.performance.resource"`, `event_data_initiatorType=~"fetch|xmlhttprequest"`,
		"unwrap event_data_duration", `event_data_responseStatus=~"[45][0-9][0-9]"`,
		`{resource.service.name="cart" && kind=client}`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("the Frontend section never queries %q", want)
		}
	}
	// Loki 3.6 fills only some fields of a multi-field `| logfmt a, b` and leaves
	// the rest empty (verified against Faro lines: the error table lost its
	// messages, the fetch-status panels went blank). One field or all of them.
	if m := regexp.MustCompile(`\| logfmt [A-Za-z_]+\s*,`).FindString(q); m != "" {
		t.Errorf("a Frontend query extracts several named logfmt fields (%q); use a bare `| logfmt`", m)
	}
	// The receiver's own uploads and the analytics proxy are not the app's traffic.
	if !strings.Contains(q, `event_data_name!~".*/collect([?].*)?|.*/ingest/.*"`) {
		t.Error("browser fetch panels count the page's own telemetry uploads as app traffic")
	}

	// Stat and table panels ask Loki for ONE value: an instant query. Without
	// queryType a Loki stat runs a range query and shows the last bucket only.
	var raw struct {
		Panels []struct {
			Type    string `json:"type"`
			Title   string `json:"title"`
			Targets []struct {
				Datasource struct {
					UID string `json:"uid"`
				} `json:"datasource"`
				QueryType string `json:"queryType"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range raw.Panels {
		for _, tg := range p.Targets {
			if tg.Datasource.UID == "loki" && (p.Type == "stat" || p.Type == "table") {
				seen++
				if tg.QueryType != "instant" {
					t.Errorf("Loki %s panel %q runs queryType %q, want instant", p.Type, p.Title, tg.QueryType)
				}
			}
		}
	}
	if seen < 10 {
		t.Errorf("found %d Loki stat/table queries at the top level; the check is not looking at the expanded section", seen)
	}
}

// The dashboard selects {source="faro", app_name, app_environment, kind}; those
// labels exist only because the receiver is told to set them. Without
// extra_log_labels every Faro stream reached Loki with no labels at all.
func TestTheFaroReceiverLabelsWhatTheDashboardSelects(t *testing.T) {
	dir := filepath.Join(stackRoot(t), "packages", "observability", "faro")
	values, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config string
	for _, doc := range strings.Split(string(raw), "\n---") {
		var cm struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if yaml.Unmarshal([]byte(doc), &cm) == nil && cm.Kind == "ConfigMap" && cm.Data["config.alloy"] != "" {
			config = cm.Data["config.alloy"]
		}
	}
	if config == "" {
		t.Fatal("the faro package renders no Alloy config")
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for where, text := range map[string]string{"values.yaml": string(values), "manifests/install.yaml": config} {
		for _, want := range []string{`source = "faro",`, `app_name = "",`, `app_environment = "",`, `kind = "",`} {
			if !strings.Contains(norm(text), "extra_log_labels = {") || !strings.Contains(norm(text), want) {
				t.Errorf("%s: the Faro receiver does not set the label %s that the app dashboard selects on", where, want)
			}
		}
	}
}
