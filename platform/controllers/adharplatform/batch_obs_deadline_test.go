package adharplatform

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/template"

	"sigs.k8s.io/yaml"
)

// Design 0025 §11.3/§11.4, PLAT-3.5: BatchDeadlineEndangered and BatchDeadlineMissed
// are evaluated by the Mimir ruler on the batch observer's SLA gauges. The first
// version of these rules read a gauge the observer did not publish, with labels it
// did not carry, from a manager whose metrics endpoint was off — and every guard
// passed, because the observer's side of the contract was a table written in this
// package. Each half is now read from where it lives: the gauge names and labels
// from platform/controllers/batch's own prometheus.New* calls, the scrape path from
// the manager Deployment and Alloy's config. An alert on a series that never
// arrives does not fail; it stays quiet and reads exactly like a job on time.
const (
	batchObserverDir = "../batch"
	managerManifest  = "../resources/manager/manager.yaml"
)

// obsPath is rel, or the copy named by env: mutation tests point these guards at a
// mutated copy of another lane's files instead of editing them in place.
func obsPath(env, rel string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	return rel
}

// observerGauges returns every adhar_batch_* metric platform/controllers/batch
// constructs, with the label names it declares. Opts must name the metric with a
// literal; labels may be a []string literal or a package-level var holding one.
func observerGauges(t *testing.T) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	dir := obsPath("ADHAR_OBS_OBSERVER_DIR", batchObserverDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	vars := map[string]ast.Expr{}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						vars[n.Name] = vs.Values[i]
					}
				}
			}
		}
	}
	strs := func(e ast.Expr) ([]string, bool) {
		if id, ok := e.(*ast.Ident); ok {
			e = vars[id.Name]
		}
		lit, ok := e.(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		var out []string
		for _, el := range lit.Elts {
			b, ok := el.(*ast.BasicLit)
			if !ok || b.Kind != token.STRING {
				return nil, false
			}
			s, _ := strconv.Unquote(b.Value)
			out = append(out, s)
		}
		return out, true
	}
	gauges := map[string][]string{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasPrefix(sel.Sel.Name, "New") {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "prometheus" || len(call.Args) == 0 {
				return true
			}
			opts, ok := call.Args[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			parts := map[string]string{}
			for _, el := range opts.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				k, _ := kv.Key.(*ast.Ident)
				if k == nil || (k.Name != "Name" && k.Name != "Namespace" && k.Name != "Subsystem") {
					continue
				}
				b, ok := kv.Value.(*ast.BasicLit)
				if !ok {
					t.Errorf("%s: a metric's %s is not a string literal; this guard cannot read it", fset.Position(kv.Pos()), k.Name)
					continue
				}
				parts[k.Name], _ = strconv.Unquote(b.Value)
			}
			var name []string
			for _, k := range []string{"Namespace", "Subsystem", "Name"} {
				if parts[k] != "" {
					name = append(name, parts[k])
				}
			}
			fq := strings.Join(name, "_")
			if !strings.HasPrefix(fq, "adhar_batch") {
				return true
			}
			var labels []string
			if strings.HasSuffix(sel.Sel.Name, "Vec") {
				if len(call.Args) < 2 {
					t.Errorf("%s: %s %s has no label names", fset.Position(call.Pos()), sel.Sel.Name, fq)
					return true
				}
				if labels, ok = strs(call.Args[1]); !ok {
					t.Errorf("%s: %s's labels are not a []string literal (or a var holding one); this guard cannot read them", fset.Position(call.Pos()), fq)
					return true
				}
			}
			gauges[fq] = labels
			return true
		})
	}
	return gauges
}

// The deadline rules in Mimir, with the annotations TestMimirBatchAlerts* do not read.
type obsDeadlineSpec struct {
	Groups []struct {
		Rules []struct {
			Record      string            `json:"record"`
			Alert       string            `json:"alert"`
			Expr        string            `json:"expr"`
			Annotations map[string]string `json:"annotations"`
		} `json:"rules"`
	} `json:"groups"`
}

var (
	obsPromMatcherNames = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(=~|!~|!=|=)`)
	obsPromLabelReplace = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)",\s*"[^"]*",\s*"([A-Za-z0-9_]*)"`)
	obsTemplateLabel    = regexp.MustCompile(`\$labels\.([A-Za-z_][A-Za-z0-9_]*)`)
)

func obsSet(xs ...[]string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		for _, s := range x {
			m[s] = true
		}
	}
	return m
}

func obsKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The rules read the observer's gauges by the name and labels the observer
// declares. A recording rule on a gauge aggregates by EVERY label the gauge has
// (anything less collapses two jobs into one series — the defect behind
// `max by (… k8s_namespace_name …)` on a gauge labelled `namespace`) plus `cluster`
// (the hub's external label, which the loader pins every query to), and by nothing
// else, because nothing else is on the series once the scrape's own pod/instance
// are aggregated away. Every label an alert then joins on, or names in its text,
// is one those recordings actually carry.
func TestTheMimirDeadlineRulesReadExactlyTheObserversGauges(t *testing.T) {
	gauges := observerGauges(t)
	if len(gauges) == 0 {
		t.Fatalf("platform/controllers/batch constructs no adhar_batch_* metric; the deadline alerts read nothing")
	}
	doc, _ := obsLoadPromRule(t, mimirBatchAlerts)
	var spec obsDeadlineSpec
	b, _ := yaml.Marshal(doc["spec"])
	if err := yaml.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	recordedFrom := map[string]map[string]bool{} // record -> the labels it carries
	type rule struct {
		name, expr string
		record     bool
		ann        map[string]string
	}
	var rules []rule
	for _, g := range spec.Groups {
		for _, r := range g.Rules {
			rules = append(rules, rule{r.Record + r.Alert, r.Expr, r.Record != "", r.Annotations})
		}
	}
	for _, r := range rules {
		code := obsPromString.ReplaceAllString(r.expr, `""`)
		for gauge, labels := range gauges {
			sel := regexp.MustCompile(`(^|[^A-Za-z0-9_:])` + gauge + `\s*(\{[^}]*\})?`)
			uses := sel.FindAllStringSubmatch(code, -1)
			if uses == nil {
				continue
			}
			read[gauge] = true
			allowed := obsSet(labels, []string{"cluster"})
			for _, u := range uses {
				for _, m := range obsPromMatcherNames.FindAllStringSubmatch(u[2], -1) {
					if !allowed[m[1]] {
						t.Errorf("%s matches %s on %q, which the observer does not publish (it has %v)", r.name, gauge, m[1], labels)
					}
				}
			}
			groupings := obsPromAgg.FindAllStringSubmatch(r.expr, -1)
			if len(groupings) == 0 {
				t.Errorf("%s reads %s without aggregating: the scrape's pod and instance stay on it, so a manager restart doubles every deadline", r.name, gauge)
			}
			for _, m := range groupings {
				by := obsSet(strings.Split(strings.ReplaceAll(m[2], " ", ""), ","))
				for l := range by {
					if !allowed[l] {
						t.Errorf("%s groups %s by %q, which the observer does not publish (it has %v)", r.name, gauge, l, labels)
					}
				}
				for _, l := range labels {
					if !by[l] {
						t.Errorf("%s groups %s without %q: two jobs' deadlines collapse into one series", r.name, gauge, l)
					}
				}
				if !by["cluster"] {
					t.Errorf("%s groups %s without cluster", r.name, gauge)
				}
			}
			out := obsSet(labels, []string{"cluster"})
			for _, m := range obsPromLabelReplace.FindAllStringSubmatch(r.expr, -1) {
				if m[2] != "" && !allowed[m[2]] {
					t.Errorf("%s copies %q into %q, but the observer's %s has no %q", r.name, m[2], m[1], gauge, m[2])
				}
				out[m[1]] = true
			}
			if r.record {
				recordedFrom[r.name] = out
			}
		}
	}
	for gauge, labels := range gauges {
		if !read[gauge] {
			t.Errorf("the observer publishes %s %v and no Mimir batch rule reads it", gauge, labels)
		}
	}
	if len(recordedFrom) == 0 {
		t.Fatal("no recording rule reads the observer's gauges")
	}
	// What reads a recording reads only the labels that recording carries.
	alerting := 0
	for _, r := range rules {
		var carries map[string]bool
		for rec, labels := range recordedFrom {
			if regexp.MustCompile(`(^|[^A-Za-z0-9_:])` + regexp.QuoteMeta(rec) + `([^A-Za-z0-9_:]|$)`).MatchString(r.expr) {
				if carries == nil {
					carries = map[string]bool{}
				}
				for l := range labels {
					carries[l] = true
				}
			}
		}
		if carries == nil {
			continue
		}
		alerting++
		used := map[string]bool{}
		for _, m := range obsPromAgg.FindAllStringSubmatch(r.expr, -1) {
			for _, l := range strings.Split(strings.ReplaceAll(m[2], " ", ""), ",") {
				used[l] = true
			}
		}
		for _, m := range obsPromLabelReplace.FindAllStringSubmatch(r.expr, -1) {
			if m[2] != "" {
				used[m[2]] = true
			}
			carries[m[1]] = true
		}
		for _, text := range r.ann {
			for _, m := range obsTemplateLabel.FindAllStringSubmatch(text, -1) {
				used[m[1]] = true
			}
		}
		for l := range used {
			if !carries[l] {
				t.Errorf("%s uses %q, which its deadline recordings do not carry (they carry %v)", r.name, l, obsKeys(carries))
			}
		}
	}
	if alerting == 0 {
		t.Error("no rule reads the deadline recordings")
	}
}

// The gauges reach Mimir only through Alloy's annotated_pods scrape: a PodMonitor
// feeds the in-cluster Prometheus, which does not write to Mimir. That scrape keeps
// pods annotated prometheus.io/scrape: "true" and scrapes /metrics on every
// DECLARED container port — it honours neither prometheus.io/port nor
// prometheus.io/path — so the manager must annotate its pod, serve metrics (the
// flag's default "0" is off), declare that port, and serve them at /metrics; and no
// gauge label may be one the scrape sets, or it arrives renamed exported_<label>.
func TestTheObserversGaugesReachMimirThroughAlloysPodScrape(t *testing.T) {
	raw, err := os.ReadFile(obsPath("ADHAR_OBS_MANAGER_MANIFEST", managerManifest))
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	tpl := template.Must(template.New("manager").Option("missingkey=error").Parse(string(raw)))
	if err := tpl.Execute(&rendered, struct{ Namespace, Image, PlatformName string }{"adhar-system", "ghcr.io/adhar-io/adhar:test", "adhar"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "manager.yaml")
	if err := os.WriteFile(path, rendered.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var deploy map[string]any
	for _, d := range yamlDocs(t, path) {
		if d["kind"] == "Deployment" {
			deploy = d
		}
	}
	if deploy == nil {
		t.Fatal("the manager manifest has no Deployment")
	}
	ann, _ := obsDig(deploy, "spec", "template", "metadata", "annotations").(map[string]any)
	if ann["prometheus.io/scrape"] != "true" {
		t.Errorf("the manager's pod template is not annotated prometheus.io/scrape: \"true\" (%v): Alloy never scrapes the deadline gauges", ann)
	}
	var manager map[string]any
	containers, _ := obsDig(deploy, "spec", "template", "spec", "containers").([]any)
	for _, c := range containers {
		if c.(map[string]any)["name"] == "manager" {
			manager = c.(map[string]any)
		}
	}
	if manager == nil {
		t.Fatal("the manager Deployment has no `manager` container")
	}
	port := ""
	args, _ := manager["args"].([]any)
	for _, a := range args {
		if s, _ := a.(string); strings.HasPrefix(s, "--metrics-bind-address=") {
			port = s[strings.LastIndex(s, ":")+1:]
			if !strings.Contains(s, ":") {
				port = ""
			}
		}
	}
	if p, err := strconv.Atoi(port); err != nil || p == 0 {
		t.Fatalf("the manager does not pass --metrics-bind-address=:<port> (args %v): the flag defaults to \"0\", which serves no metrics", args)
	}
	declared := false
	ports, _ := manager["ports"].([]any)
	for _, p := range ports {
		if strconv.Itoa(int(obsNum(p.(map[string]any)["containerPort"]))) == port {
			declared = true
		}
	}
	if !declared {
		t.Errorf("the manager serves metrics on %s but declares no containerPort %s: Alloy's pod scrape builds a target per declared port", port, port)
	}
	if p, ok := ann["prometheus.io/port"]; ok && p != port {
		t.Errorf("prometheus.io/port is %v but metrics are served on %s", p, port)
	}
	if p, ok := ann["prometheus.io/path"]; ok && p != "/metrics" {
		t.Errorf("prometheus.io/path is %v; Alloy's pod scrape ignores it and reads /metrics", p)
	}

	cm := obsDocNamed(t, obsDocs(t, alloyInstall), "ConfigMap", "alloy")
	config, _ := obsDig(cm, "data", "config.alloy").(string)
	flat := func(s string) string { return regexp.MustCompile(`\s+`).ReplaceAllString(s, " ") }
	scrape := flat(obsAlloyBlock(config, `prometheus.scrape "annotated_pods"`))
	relabel := flat(obsAlloyBlock(config, `discovery.relabel "scrape_pods"`))
	hub := flat(obsAlloyBlock(config, `prometheus.remote_write "hub"`))
	for what, c := range map[string]struct{ in, want string }{
		"the pod scrape reads the relabelled targets": {scrape, `targets = discovery.relabel.scrape_pods.output`},
		"the pod scrape writes to the hub":            {scrape, `forward_to = [prometheus.remote_write.hub.receiver]`},
		"the relabel keeps annotated pods":            {relabel, `source_labels = ["__meta_kubernetes_pod_annotation_prometheus_io_scrape"] regex = "true" action = "keep"`},
		"the hub is Mimir":                            {hub, `url = sys.env("HUB_MIMIR_URL")`},
		"the hub stamps cluster":                      {hub, `cluster = sys.env("HUB_CLUSTER_NAME")`},
	} {
		if !strings.Contains(c.in, c.want) {
			t.Errorf("config.alloy: %s: want %q in %q", what, c.want, c.in)
		}
	}
	if strings.Contains(scrape, "metrics_path") || strings.Contains(relabel, "__metrics_path__") || strings.Contains(relabel, "__address__") {
		t.Error("the pod scrape now rewrites the path or address: this guard's /metrics and declared-port checks no longer describe it, update them")
	}
	scrapeLabels := map[string]bool{"job": true, "instance": true, "cluster": true}
	for _, m := range regexp.MustCompile(`target_label = "([A-Za-z_][A-Za-z0-9_]*)"`).FindAllStringSubmatch(relabel, -1) {
		scrapeLabels[m[1]] = true
	}
	for gauge, labels := range observerGauges(t) {
		for _, l := range labels {
			if scrapeLabels[l] {
				t.Errorf("%s is labelled %q, which Alloy's pod scrape sets itself: the series arrives with exported_%s instead", gauge, l, l)
			}
		}
	}
}

func obsNum(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return -1
}
