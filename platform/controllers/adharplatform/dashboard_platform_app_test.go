package adharplatform

// Grafana dashboard quality for the two dashboards the platform generates
// itself: the "Adhar Platform" front page (kube-prometheus/build-platform-dashboard.py)
// and the per-application dashboard the Application composition composes
// (hack/dashboards/build-app-dashboard.py). They must parse, keep panel ids
// unique, never overlap on the grid, and query only datasources the platform
// actually provisions. A panel naming a datasource uid that does not exist
// renders "datasource not found"; an overlapping one is drawn on top of its
// neighbour; neither fails anything at sync time, which is why they are pinned.
//
// And the Applications folder holds per-application dashboards ONLY: two Chaos
// Mesh dashboards sat in it until 2026-10-23, between the services teams open it for.

import (
	"encoding/json"
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

// paProvisionedDatasourceUIDs reads the uids Grafana is actually given: the LGTM
// four from extra-datasources.yaml, Prometheus + Alertmanager from the chart values.
func paProvisionedDatasourceUIDs(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join(stackRoot(t), "packages", "observability", "kube-prometheus")
	uids := map[string]bool{}
	raw, err := os.ReadFile(filepath.Join(dir, "manifests", "extra-datasources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	for _, body := range cm.Data {
		var ds struct {
			Datasources []struct {
				UID string `json:"uid"`
			} `json:"datasources"`
		}
		if err := yaml.Unmarshal([]byte(body), &ds); err != nil {
			t.Fatal(err)
		}
		for _, d := range ds.Datasources {
			uids[d.UID] = true
		}
	}
	values, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^\s+uid: (prometheus|alertmanager)\s*$`).FindAllStringSubmatch(string(values), -1) {
		uids[m[1]] = true
	}
	for _, want := range []string{"prometheus", "mimir", "loki", "tempo"} {
		if !uids[want] {
			t.Fatalf("datasource uid %q is not provisioned (found %v); this guard is not reading the right files", want, sortedKeys(uids))
		}
	}
	return uids
}

type paGridPos struct{ X, Y, W, H int }

type paPanel struct {
	ID         int             `json:"id"`
	Type       string          `json:"type"`
	Title      string          `json:"title"`
	Collapsed  bool            `json:"collapsed"`
	GridPos    paGridPos       `json:"gridPos"`
	Datasource json.RawMessage `json:"datasource"`
	Targets    []struct {
		Datasource json.RawMessage `json:"datasource"`
		Expr       string          `json:"expr"`
		Query      string          `json:"query"`
		Exemplar   bool            `json:"exemplar"`
	} `json:"targets"`
	Panels []paPanel `json:"panels"`
}

type paDashboard struct {
	UID         string    `json:"uid"`
	Title       string    `json:"title"`
	Panels      []paPanel `json:"panels"`
	Annotations struct {
		List []struct {
			Datasource json.RawMessage `json:"datasource"`
		} `json:"list"`
	} `json:"annotations"`
	Templating struct {
		List []struct {
			Name       string          `json:"name"`
			Datasource json.RawMessage `json:"datasource"`
			Options    []struct {
				Text  string `json:"text"`
				Value string `json:"value"`
			} `json:"options"`
		} `json:"list"`
	} `json:"templating"`
	Links []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"links"`
}

func paDSUID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj struct {
		UID string `json:"uid"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.UID != "" {
		return obj.UID
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// paCheckDashboard returns every structural problem of one dashboard.
func paCheckDashboard(body string, uids map[string]bool) (paDashboard, []string) {
	var d paDashboard
	var problems []string
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		return d, []string{"not valid JSON: " + err.Error()}
	}
	okDS := func(where, uid string) {
		switch {
		case uid == "", strings.HasPrefix(uid, "$"),
			uid == "-- Grafana --", uid == "grafana", uid == "-- Mixed --", uid == "-- Dashboard --":
		case !uids[uid]:
			problems = append(problems, fmt.Sprintf("%s queries datasource uid %q, which Grafana is not provisioned with", where, uid))
		}
	}
	seen := map[int]string{}
	overlap := func(scope string, ps []paPanel) {
		for i := range ps {
			for j := i + 1; j < len(ps); j++ {
				a, b := ps[i].GridPos, ps[j].GridPos
				if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
					problems = append(problems, fmt.Sprintf("%s: panels %q and %q overlap (%v / %v)", scope, ps[i].Title, ps[j].Title, a, b))
				}
			}
		}
	}
	var visit func(ps []paPanel)
	visit = func(ps []paPanel) {
		for _, p := range ps {
			if prev, dup := seen[p.ID]; dup {
				problems = append(problems, fmt.Sprintf("panel id %d is used by both %q and %q", p.ID, prev, p.Title))
			}
			seen[p.ID] = p.Title
			if p.GridPos.W <= 0 || p.GridPos.H <= 0 || p.GridPos.X+p.GridPos.W > 24 {
				problems = append(problems, fmt.Sprintf("panel %q has an impossible gridPos %v", p.Title, p.GridPos))
			}
			where := fmt.Sprintf("panel %q", p.Title)
			okDS(where, paDSUID(p.Datasource))
			for _, tg := range p.Targets {
				okDS(where, paDSUID(tg.Datasource))
			}
			if p.Type != "row" && p.Type != "text" && len(p.Targets) == 0 {
				problems = append(problems, where+" has no query")
			}
			if len(p.Panels) > 0 {
				overlap(fmt.Sprintf("row %q", p.Title), p.Panels)
				visit(p.Panels)
			}
		}
	}
	// Top level: rows plus the panels of expanded rows share one grid.
	overlap("top level", d.Panels)
	visit(d.Panels)
	for _, a := range d.Annotations.List {
		okDS("an annotation", paDSUID(a.Datasource))
	}
	for _, v := range d.Templating.List {
		okDS("variable $"+v.Name, paDSUID(v.Datasource))
	}
	return d, problems
}

func paPlatformDashboardJSON(t *testing.T) string {
	t.Helper()
	p := filepath.Join(stackRoot(t), "packages", "observability", "kube-prometheus", "manifests", "dashboard-adhar-platform.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	body := cm.Data["adhar-platform.json"]
	if body == "" {
		t.Fatal("dashboard-adhar-platform.yaml carries no adhar-platform.json")
	}
	return body
}

// paComposedAppDashboard renders the Application composition for an app `cart`
// in project `shop` (environments dev/test/prod) and returns its dashboard JSON.
func paComposedAppDashboard(t *testing.T) string {
	t.Helper()
	docs := renderGoTemplate(t, applicationRequest(t, "cart", ""), "compositions", "apps", "argocd-multienv.yaml")
	cm := composedManifest(docs, "ConfigMap", "app-dashboard-cart")
	if cm == nil {
		t.Fatal("the Application composition composes no app-dashboard-cart ConfigMap")
	}
	if dig(cm, "metadata", "annotations", "grafana_folder") != "Applications" {
		t.Errorf("app-dashboard-cart is filed under %v, not Applications", dig(cm, "metadata", "annotations", "grafana_folder"))
	}
	body, _ := dig(cm, "data", "cart.json").(string)
	if body == "" {
		t.Fatal("app-dashboard-cart carries no cart.json")
	}
	return body
}

func TestThePlatformDashboardIsWellFormed(t *testing.T) {
	d, problems := paCheckDashboard(paPlatformDashboardJSON(t), paProvisionedDatasourceUIDs(t))
	for _, p := range problems {
		t.Errorf("Adhar Platform: %s", p)
	}
	if d.UID != "adhar-platform-overview" || d.Title != "Adhar Platform" {
		t.Errorf("the front page changed identity (uid %q, title %q); links across Grafana and the console point at adhar-platform-overview", d.UID, d.Title)
	}
	if len(d.Panels) < 10 {
		t.Errorf("the platform dashboard has only %d top-level panels", len(d.Panels))
	}
}

func TestTheComposedAppDashboardIsWellFormed(t *testing.T) {
	body := paComposedAppDashboard(t)
	d, problems := paCheckDashboard(body, paProvisionedDatasourceUIDs(t))
	for _, p := range problems {
		t.Errorf("app-cart: %s", p)
	}
	if left := regexp.MustCompile(`__[A-Z_]+__`).FindAllString(body, -1); len(left) > 0 {
		t.Errorf("unfilled placeholders in the composed dashboard: %v", left)
	}
	if d.UID != "app-cart" {
		t.Errorf("uid = %q, want app-cart (the console links to /d/app-<name>)", d.UID)
	}
	var env []string
	for _, v := range d.Templating.List {
		if v.Name == "env" {
			for _, o := range v.Options {
				env = append(env, o.Text+"="+o.Value)
			}
		}
	}
	if strings.Join(env, ",") != "dev=shop-dev,test=shop-test,prod=shop-prod" {
		t.Errorf("env variable options = %v, want the project's environments with their namespaces", env)
	}
	// The repository's public host (gitea.example.com) yields deep links.
	links := map[string]string{}
	for _, l := range d.Links {
		links[l.Title] = l.URL
	}
	if links["Argo CD"] != "https://argocd.example.com/applications/adhar-system/shop-cart-${env:text}" ||
		links["Kargo"] != "https://kargo.example.com/project/shop" ||
		links["Repository"] != "https://gitea.example.com/acme/cart" {
		t.Errorf("deep links are wrong: %v", links)
	}
}

// Every section a service owner needs, each fed by the series adhar-kit apps
// actually publish (verified against live Spring services).
func TestTheComposedAppDashboardCoversEverySignal(t *testing.T) {
	body := paComposedAppDashboard(t)
	d, _ := paCheckDashboard(body, paProvisionedDatasourceUIDs(t))
	rows := map[string][]paPanel{}
	var current string
	for _, p := range d.Panels {
		if p.Type == "row" {
			current = p.Title
			rows[current] = append(rows[current], p.Panels...)
			continue
		}
		rows[current] = append(rows[current], p)
	}
	queries := func(row string) string {
		var b strings.Builder
		for _, p := range rows[row] {
			for _, tg := range p.Targets {
				b.WriteString(tg.Expr + tg.Query + "\n")
				b.WriteString(paDSUID(tg.Datasource) + "\n")
			}
		}
		return b.String()
	}
	for row, wants := range map[string][]string{
		"Golden Signals (RED)":         {"http_server_requests_seconds_count", "http_server_requests_seconds_bucket", `status=~"5.."`, `status=~"4.."`, "histogram_quantile(0.99"},
		"Saturation & Resources":       {"container_cpu_usage_seconds_total", "container_memory_working_set_bytes", "kube_pod_container_resource_limits", "container_cpu_cfs_throttled_periods_total", "rollout_info_replicas_updated", "kube_horizontalpodautoscaler_status_desired_replicas"},
		"JVM":                          {"jvm_memory_used_bytes", "jvm_gc_pause_seconds", "jvm_threads_states_threads", "hikaricp_connections_active", "kafka_consumer_fetch_manager_records_lag_max"},
		"Business Metrics (adhar-kit)": {"$business_metric", "adhar_business_"},
		"Logs (Loki)":                  {`app="cart"`, "loki"},
		"Traces (Tempo)":               {`service="cart"`, "traces_spanmetrics_calls_total", `{resource.service.name="cart"}`, "tempo", "mimir"},
	} {
		if _, ok := rows[row]; !ok {
			t.Errorf("the application dashboard has no %q row", row)
			continue
		}
		q := queries(row)
		for _, w := range wants {
			if !strings.Contains(q, w) {
				t.Errorf("row %q never queries %q", row, w)
			}
		}
	}
	exemplar := false
	for _, p := range rows["Golden Signals (RED)"] {
		for _, tg := range p.Targets {
			exemplar = exemplar || (tg.Exemplar && strings.Contains(tg.Expr, "_bucket"))
		}
	}
	if !exemplar {
		t.Error("no latency query asks for exemplars, so a latency spike cannot open its trace")
	}
	// The app's own pods only: a looser `cart-.+-.+` also matched the CNPG
	// instance `cart-db-1` and summed Postgres into the app.
	podRe := regexp.MustCompile(`^cart-[a-z0-9]{6,10}-[a-z0-9]{5}$`)
	for pod, want := range map[string]bool{"cart-68f8cb58d9-cmrst": true, "cart-db-1": false, "cart-cache-68d6bd4df5-x7xfr": false} {
		if podRe.MatchString(pod) != want {
			t.Errorf("pod %s: matched=%v, want %v", pod, !want, want)
		}
	}
	if !strings.Contains(body, `cart-[a-z0-9]{6,10}-[a-z0-9]{5}`) {
		t.Error("the dashboard does not select the app's pods by <app>-<pod-template-hash>-<suffix>")
	}
}

// The Applications folder is for custom applications only — one dashboard each,
// composed per Application (app-dashboard-<name>), or shipped by a golden-path
// skeleton into the app's own repository. A platform component filed there
// (Chaos Mesh was, until 2026-10-23) buries the apps teams open it for.
func TestOnlyApplicationDashboardsUseTheApplicationsFolder(t *testing.T) {
	var offenders []string
	for _, d := range shippedDashboards(t) {
		if strings.TrimSpace(d.folder) != "Applications" {
			continue
		}
		rel := shortStackPath(d.path)
		if strings.HasPrefix(rel, "application/adhar-templates/") && strings.Contains(rel, "/skeleton/") {
			continue // rendered into a scaffolded app's repo: one per application
		}
		offenders = append(offenders, fmt.Sprintf("%s (%s)", d.title, rel))
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s is filed under Applications, which holds per-application dashboards only; file it by area "+
			"(e.g. Resilience, Observability)", o)
	}
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	if !strings.Contains(comp, `name: {{ quote (printf "app-dashboard-%s" $name) }}`) || !strings.Contains(comp, "grafana_folder: Applications") {
		t.Error("the Application composition no longer files app-dashboard-<name> under Applications")
	}
}

// Both dashboards are generated; a hand-edit of the output is lost on the next
// run, so the output must match its generator. Only these two generators: the
// other kube-prometheus generators have guards of their own.
func TestGeneratedDashboardsAreUpToDate(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	repo := filepath.Clean(filepath.Join(stackRoot(t), "..", ".."))
	for _, script := range []string{
		filepath.Join(repo, "hack", "dashboards", "build-app-dashboard.py"),
		filepath.Join(stackRoot(t), "packages", "observability", "kube-prometheus", "build-platform-dashboard.py"),
	} {
		out, err := exec.Command(py, script, "--check").CombinedOutput()
		if err != nil {
			t.Errorf("%s --check: %v\n%s", filepath.Base(script), err, out)
		}
	}
}
