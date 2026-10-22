package adharplatform

// Structural guards for the backing-service and Kubernetes dashboards built by
// platform/stack/packages/observability/kube-prometheus/build-service-dashboards.py.
//
// Grafana accepts almost anything it is given and fails quietly: a panel that
// overlaps another is silently pushed down, a datasource UID that does not
// exist renders "Datasource not found" only when someone opens the panel, and a
// query naming a template variable the dashboard does not define is sent to
// Prometheus with the literal "$cluster" in it and simply returns nothing —
// which is indistinguishable from "this service is idle". The community
// dashboards these replaced had all three problems (2026-10-23: the RabbitMQ
// dashboard was 49/49 panels without data, the Kubernetes compute mixins read
// metrics removed in kube-state-metrics v2). These checks make the next copy of
// that fail in CI instead of on a dashboard someone opened during an incident.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// builtDashboards are the dashboard ConfigMaps (manifests/dashboard-<name>.yaml)
// written by build-service-dashboards.py.
var builtDashboards = []string{
	"cloudnativepg", "redis", "kafka-cluster", "kafka-exporter", "rabbitmq-overview",
	"k8s-cluster-overview", "k8s-capacity",
	// kubernetes-mixin, vendored from kube-prometheus and post-processed
	"k8s-apiserver", "k8s-controller-manager", "k8s-scheduler", "k8s-kubelet",
	"k8s-compute-cluster", "k8s-compute-namespace-pods", "k8s-compute-namespace-workloads",
	"k8s-compute-node-pods", "k8s-compute-nodes-overview", "k8s-compute-pod", "k8s-compute-workload",
	"k8s-persistent-volumes-detail", "k8s-use-cluster",
	"k8s-networking-cluster", "k8s-networking-namespace-pods", "k8s-networking-namespace-workload",
}

// Datasource UIDs a panel may name: the dashboard's own datasource variable,
// the UIDs Grafana is provisioned with (kube-prometheus values.yaml +
// extra-datasources.yaml), and Grafana's built-in pseudo datasources.
var allowedDatasourceUIDs = map[string]bool{
	"${datasource}": true, "$datasource": true,
	"prometheus": true, "loki": true, "tempo": true, "mimir": true, "pyroscope": true, "alertmanager": true,
	"-- Mixed --": true, "-- Grafana --": true, "-- Dashboard --": true,
}

type gPanel struct {
	ID          int              `json:"id"`
	Type        string           `json:"type"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Collapsed   bool             `json:"collapsed"`
	Datasource  json.RawMessage  `json:"datasource"`
	GridPos     gGridPos         `json:"gridPos"`
	Panels      []gPanel         `json:"panels"`
	Targets     []map[string]any `json:"targets"`
	Links       []map[string]any `json:"links"`
	FieldConfig struct {
		Overrides []struct {
			Properties []struct {
				ID    string `json:"id"`
				Value any    `json:"value"`
			} `json:"properties"`
		} `json:"overrides"`
	} `json:"fieldConfig"`
}

type gGridPos struct{ H, W, X, Y int }

type gDashboard struct {
	UID        string   `json:"uid"`
	Title      string   `json:"title"`
	Panels     []gPanel `json:"panels"`
	Templating struct {
		List []struct {
			Name       string `json:"name"`
			Definition string `json:"definition"`
			Query      any    `json:"query"`
		} `json:"list"`
	} `json:"templating"`
}

// loadBuiltDashboard reads manifests/dashboard-<name>.yaml and returns its JSON.
func loadBuiltDashboard(t *testing.T, name string) (gDashboard, string) {
	t.Helper()
	path := filepath.Join(stackRoot(t), "packages", "observability", "kube-prometheus", "manifests",
		"dashboard-"+name+".yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatalf("%s is not valid YAML: %v", path, err)
	}
	body, ok := cm.Data[name+".json"]
	if !ok {
		t.Fatalf("%s has no data key %s.json", path, name)
	}
	var d gDashboard
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("%s: dashboard JSON does not parse: %v", path, err)
	}
	return d, body
}

// datasourceUID accepts both the object form {"type":..,"uid":..} and the
// legacy string form.
func datasourceUID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var o struct {
		UID string `json:"uid"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.UID, true
	}
	return string(raw), true
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:[:.][^}]*)?\}|\$([A-Za-z_][A-Za-z0-9_]*)|\[\[([A-Za-z_][A-Za-z0-9_]*)(?::[A-Za-z]+)?\]\]`)

// referencedVariables returns template-variable names used in s, ignoring
// Grafana's built-ins ($__rate_interval, ${__data.fields.x}, ...).
func referencedVariables(s string) []string {
	var out []string
	for _, m := range varRef.FindAllStringSubmatch(s, -1) {
		name := m[1] + m[2] + m[3]
		if strings.HasPrefix(name, "__") {
			continue
		}
		out = append(out, name)
	}
	return out
}

func TestBuiltDashboardsAreWellFormed(t *testing.T) {
	seenUIDs := map[string]string{}
	for _, name := range builtDashboards {
		t.Run(name, func(t *testing.T) {
			d, _ := loadBuiltDashboard(t, name)
			if d.UID == "" || d.Title == "" {
				t.Fatalf("dashboard has no uid/title (uid=%q title=%q)", d.UID, d.Title)
			}
			if other, dup := seenUIDs[d.UID]; dup {
				t.Errorf("uid %q is also used by %s — Grafana provisions only one of them", d.UID, other)
			}
			seenUIDs[d.UID] = name

			vars := map[string]bool{}
			for _, v := range d.Templating.List {
				vars[v.Name] = true
			}

			ids := map[int]string{}
			var problems []string
			checkVars := func(where, s string) {
				for _, ref := range referencedVariables(s) {
					if !vars[ref] {
						problems = append(problems, fmt.Sprintf("%s references $%s, which is not a template variable of this dashboard", where, ref))
					}
				}
			}
			checkDS := func(where string, raw json.RawMessage) {
				uid, ok := datasourceUID(raw)
				if ok && !allowedDatasourceUIDs[uid] {
					problems = append(problems, fmt.Sprintf("%s uses datasource %q, which is neither the $datasource variable nor a provisioned UID", where, uid))
				}
			}

			// Grid groups: the top level, plus each collapsed row's children
			// (which are laid out on their own once the row opens).
			groups := [][]gPanel{nil}
			var walk func(ps []gPanel, inCollapsed bool)
			walk = func(ps []gPanel, inCollapsed bool) {
				for _, p := range ps {
					where := fmt.Sprintf("panel %d %q", p.ID, p.Title)
					if prev, dup := ids[p.ID]; dup {
						problems = append(problems, fmt.Sprintf("%s reuses id %d of %q", where, p.ID, prev))
					}
					ids[p.ID] = p.Title
					if !inCollapsed {
						groups[0] = append(groups[0], p)
					}
					if p.GridPos.X < 0 || p.GridPos.W <= 0 || p.GridPos.H <= 0 || p.GridPos.X+p.GridPos.W > 24 {
						problems = append(problems, fmt.Sprintf("%s has gridPos %+v outside the 24-column grid", where, p.GridPos))
					}
					if p.Type != "row" {
						if strings.TrimSpace(p.Description) == "" {
							problems = append(problems, where+" has no description")
						}
						checkDS(where, p.Datasource)
						checkVars(where+" title", p.Title)
						checkVars(where+" datasource", string(p.Datasource))
						for i, tg := range p.Targets {
							tw := fmt.Sprintf("%s target %d", where, i)
							if raw, err := json.Marshal(tg["datasource"]); err == nil && tg["datasource"] != nil {
								checkDS(tw, raw)
								checkVars(tw+" datasource", string(raw))
							}
							if expr, _ := tg["expr"].(string); expr != "" {
								checkVars(tw, expr)
							}
						}
						for _, l := range p.Links {
							if u, _ := l["url"].(string); u != "" {
								checkVars(where+" link", u)
							}
						}
						for _, o := range p.FieldConfig.Overrides {
							for _, prop := range o.Properties {
								if prop.ID == "links" {
									raw, _ := json.Marshal(prop.Value)
									checkVars(where+" data link", string(raw))
								}
							}
						}
					}
					if p.Type == "row" && len(p.Panels) > 0 {
						if p.Collapsed {
							groups = append(groups, p.Panels)
						}
						walk(p.Panels, true)
					}
				}
			}
			walk(d.Panels, false)
			for _, v := range d.Templating.List {
				checkVars("variable "+v.Name, v.Definition)
				if q, ok := v.Query.(string); ok {
					checkVars("variable "+v.Name, q)
				} else if qm, ok := v.Query.(map[string]any); ok {
					if q, _ := qm["query"].(string); q != "" {
						checkVars("variable "+v.Name, q)
					}
				}
			}

			for _, g := range groups {
				for i := 0; i < len(g); i++ {
					for j := i + 1; j < len(g); j++ {
						a, b := g[i].GridPos, g[j].GridPos
						if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
							problems = append(problems, fmt.Sprintf("panel %d %q %+v overlaps panel %d %q %+v",
								g[i].ID, g[i].Title, a, g[j].ID, g[j].Title, b))
						}
					}
				}
			}

			sort.Strings(problems)
			for _, p := range problems {
				t.Error(p)
			}
		})
	}
}

// The hand-authored dashboards must leave no hole in the grid: every grid line
// is filled edge to edge, so the page reads as one composed view rather than a
// pile of panels. (The vendored mixins keep upstream's layout and are exempt.)
func TestHandAuthoredDashboardsFillEveryGridLine(t *testing.T) {
	for _, name := range builtDashboards[:7] {
		t.Run(name, func(t *testing.T) {
			d, _ := loadBuiltDashboard(t, name)
			width := map[int]int{}
			for _, p := range d.Panels {
				for y := p.GridPos.Y; y < p.GridPos.Y+p.GridPos.H; y++ {
					width[y] += p.GridPos.W
				}
			}
			for y, w := range width {
				if w != 24 {
					t.Errorf("grid line y=%d is %d columns wide, not 24 — a gap or a ragged line", y, w)
				}
			}
		})
	}
}

// kube-state-metrics series are also re-exported by OpenCost under the same
// names; any hand-authored query that reads a kube_* metric without pinning
// job="kube-state-metrics" double-counts whatever OpenCost still exports.
func TestHandAuthoredDashboardsPinKubeStateMetrics(t *testing.T) {
	kubeSel := regexp.MustCompile(`\bkube_[a-z_]+\{([^}]*)\}`)
	for _, name := range builtDashboards[:7] {
		t.Run(name, func(t *testing.T) {
			_, body := loadBuiltDashboard(t, name)
			var d struct {
				Panels []gPanel `json:"panels"`
			}
			_ = json.Unmarshal([]byte(body), &d)
			for _, p := range d.Panels {
				for _, tg := range p.Targets {
					expr, _ := tg["expr"].(string)
					for _, m := range kubeSel.FindAllStringSubmatch(expr, -1) {
						if !strings.Contains(m[1], `job="kube-state-metrics"`) {
							t.Errorf("panel %q: %s{%s} is not pinned to job=\"kube-state-metrics\"", p.Title,
								strings.SplitN(m[0], "{", 2)[0], m[1])
						}
					}
				}
			}
		})
	}
}
