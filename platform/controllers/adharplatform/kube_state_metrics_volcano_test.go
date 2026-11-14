package adharplatform

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Volcano's Job, PodGroup and Queue phases reach Prometheus through
// kube-state-metrics' custom-resource state (design 0025 §11.1, PLAT-2.7), from
// observability/kube-prometheus/custom-resource-state.yaml, the chart's second
// values file. The batch queue alerts (manifests/volcano-alerts.yaml) and the
// composed per-job BatchGangPending read those series, so these guards hold the
// chain together: a field path the CRD does not have, a kind kube-state-metrics
// may not list, a rendering that lost the config, or an alert reading a series
// nothing publishes all look the same on a cluster: an alert that never fires.

const (
	ksmCRSFile      = "observability/kube-prometheus/custom-resource-state.yaml"
	ksmInstallFile  = "observability/kube-prometheus/manifests/install.yaml"
	ksmGenerator    = "observability/kube-prometheus/generate-manifests.sh"
	volcanoAlerts   = "observability/kube-prometheus/manifests/volcano-alerts.yaml"
	ksmRegenerateIt = "run platform/stack/packages/observability/kube-prometheus/generate-manifests.sh"
)

type ksmCRSMetric struct {
	Name string `json:"name"`
	Each struct {
		Type  string `json:"type"`
		Gauge *struct {
			Path           []string            `json:"path"`
			LabelFromKey   string              `json:"labelFromKey"`
			LabelsFromPath map[string][]string `json:"labelsFromPath"`
			ValueFrom      []string            `json:"valueFrom"`
			NilIsZero      bool                `json:"nilIsZero"`
		} `json:"gauge"`
		StateSet *struct {
			LabelName string   `json:"labelName"`
			Path      []string `json:"path"`
			List      []string `json:"list"`
		} `json:"stateSet"`
	} `json:"each"`
}

type ksmCRSResource struct {
	GVK struct {
		Group   string `json:"group"`
		Version string `json:"version"`
		Kind    string `json:"kind"`
	} `json:"groupVersionKind"`
	Prefix         string              `json:"metricNamePrefix"`
	LabelsFromPath map[string][]string `json:"labelsFromPath"`
	Metrics        []ksmCRSMetric      `json:"metrics"`
}

type ksmCRSConfig struct {
	Kind string `json:"kind"`
	Spec struct {
		Resources []ksmCRSResource `json:"resources"`
	} `json:"spec"`
}

type ksmCRSValues struct {
	KSM struct {
		RBAC struct {
			ExtraRules []struct {
				APIGroups []string `json:"apiGroups"`
				Resources []string `json:"resources"`
				Verbs     []string `json:"verbs"`
			} `json:"extraRules"`
		} `json:"rbac"`
		CRS struct {
			Enabled bool           `json:"enabled"`
			Config  map[string]any `json:"config"`
		} `json:"customResourceState"`
	} `json:"kube-state-metrics"`
}

func ksmCRSValuesFile(t *testing.T) (ksmCRSValues, ksmCRSConfig) {
	t.Helper()
	var v ksmCRSValues
	if err := yaml.Unmarshal([]byte(readStackFile(t, ksmCRSFile)), &v); err != nil {
		t.Fatalf("%s: %v", ksmCRSFile, err)
	}
	var c ksmCRSConfig
	b, _ := yaml.Marshal(v.KSM.CRS.Config)
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatalf("%s: customResourceState.config: %v", ksmCRSFile, err)
	}
	return v, c
}

// The Volcano kinds kube-state-metrics must publish, by group/kind.
var ksmVolcanoKinds = []string{"scheduling.volcano.sh/PodGroup", "batch.volcano.sh/Job", "scheduling.volcano.sh/Queue"}

// published returns every series name a resource publishes and the label names
// each one carries (kube-state-metrics' own customresource_* labels aside).
func (r ksmCRSResource) published() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, m := range r.Metrics {
		labels := map[string]bool{}
		for l := range r.LabelsFromPath {
			labels[l] = true
		}
		if g := m.Each.Gauge; g != nil {
			if g.LabelFromKey != "" {
				labels[g.LabelFromKey] = true
			}
			for l := range g.LabelsFromPath {
				labels[l] = true
			}
		}
		if s := m.Each.StateSet; s != nil {
			labels[s.LabelName] = true
		}
		out[r.Prefix+"_"+m.Name] = labels
	}
	return out
}

// volcanoCRDSchemas returns the served storage version and its openAPIV3Schema
// for each Volcano CRD, keyed group/kind.
func volcanoCRDSchemas(t *testing.T) map[string][2]any {
	t.Helper()
	out := map[string][2]any{}
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) != "CustomResourceDefinition" {
			continue
		}
		group, _ := dig(doc, "spec", "group").(string)
		kind, _ := dig(doc, "spec", "names", "kind").(string)
		for _, v := range anyList(dig(doc, "spec", "versions")) {
			ver := asMap(v)
			if ver["storage"] == true && ver["served"] == true {
				out[group+"/"+kind] = [2]any{ver["name"], dig(ver, "schema", "openAPIV3Schema")}
			}
		}
	}
	return out
}

// schemaHas reports whether a field path exists in a CRD's structural schema.
// metadata is not in a CRD schema; only the paths kube-state-metrics may read
// there are accepted.
func schemaHas(schema any, path []string) bool {
	if len(path) > 0 && path[0] == "metadata" {
		if len(path) == 2 {
			return path[1] == "name" || path[1] == "namespace" || path[1] == "creationTimestamp"
		}
		return len(path) == 3 && path[1] == "labels"
	}
	node := asMap(schema)
	for _, p := range path {
		props := asMap(node["properties"])
		next, ok := props[p]
		if !ok {
			return false
		}
		node = asMap(next)
	}
	return true
}

func TestKubeStateMetricsPublishesVolcanoState(t *testing.T) {
	values, cfg := ksmCRSValuesFile(t)
	if !values.KSM.CRS.Enabled || cfg.Kind != "CustomResourceStateMetrics" {
		t.Fatalf("%s must enable kube-state-metrics.customResourceState with a CustomResourceStateMetrics config", ksmCRSFile)
	}
	schemas := volcanoCRDSchemas(t)
	byKind := map[string]ksmCRSResource{}
	for _, r := range cfg.Spec.Resources {
		byKind[r.GVK.Group+"/"+r.GVK.Kind] = r
	}
	for _, gk := range ksmVolcanoKinds {
		r, ok := byKind[gk]
		if !ok {
			t.Errorf("no custom-resource state for %s; its phases never reach Prometheus", gk)
			continue
		}
		crd, served := schemas[gk]
		if !served {
			t.Errorf("core/volcano serves no CRD for %s", gk)
			continue
		}
		// kube-state-metrics watches exactly this version; one Volcano stops
		// serving is a resource that silently publishes nothing.
		if r.GVK.Version != crd[0] {
			t.Errorf("%s is read at %s but core/volcano stores it at %v", gk, r.GVK.Version, crd[0])
		}
		if !strings.HasPrefix(r.Prefix, "kube_volcano_") {
			t.Errorf("%s publishes under %q; the alerts and the Console read kube_volcano_*", gk, r.Prefix)
		}
		paths := map[string][]string{}
		for l, p := range r.LabelsFromPath {
			paths["label "+l] = p
		}
		phases := false
		for _, m := range r.Metrics {
			switch {
			case m.Each.Gauge != nil:
				paths[m.Name] = m.Each.Gauge.Path
			case m.Each.StateSet != nil:
				paths[m.Name] = m.Each.StateSet.Path
				phases = phases || len(m.Each.StateSet.List) > 0
			default:
				t.Errorf("%s metric %s is neither a Gauge nor a StateSet", gk, m.Name)
			}
		}
		for what, p := range paths {
			if !schemaHas(crd[1], p) {
				t.Errorf("%s %s reads %v, which the %s CRD does not have: the series is never published", gk, what, p, gk)
			}
		}
		if !phases {
			t.Errorf("%s publishes no phase or state", gk)
		}
	}

	// A phase the StateSet does not list is published as 0 for every listed one:
	// the gang looks like it is in no phase at all.
	want := map[string][]string{
		"scheduling.volcano.sh/PodGroup": {"Pending", "Inqueue", "Running", "Unknown", "Completed"},
		"batch.volcano.sh/Job":           {"Pending", "Aborting", "Aborted", "Running", "Restarting", "Completing", "Completed", "Terminating", "Terminated", "Failed"},
	}
	// The Queue CRD carries its own enum; the StateSet must be exactly it.
	if enum := anyList(dig(asMap(schemas["scheduling.volcano.sh/Queue"][1]), "properties", "status", "properties", "state", "enum")); len(enum) > 0 {
		want["scheduling.volcano.sh/Queue"] = stringList(enum)
	} else {
		t.Error("the Queue CRD's status.state enum has moved; this guard needs updating")
	}
	for gk, phases := range want {
		for _, m := range byKind[gk].Metrics {
			if s := m.Each.StateSet; s != nil {
				got, w := append([]string(nil), s.List...), append([]string(nil), phases...)
				sort.Strings(got)
				sort.Strings(w)
				if !reflect.DeepEqual(got, w) {
					t.Errorf("%s %s lists %v, want Volcano's %v", gk, m.Name, s.List, phases)
				}
			}
		}
	}

	// kube-state-metrics may list and watch every kind it publishes.
	plurals := map[string]string{}
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) == "CustomResourceDefinition" {
			group, _ := dig(doc, "spec", "group").(string)
			kind, _ := dig(doc, "spec", "names", "kind").(string)
			plurals[group+"/"+kind], _ = dig(doc, "spec", "names", "plural").(string)
		}
	}
	for _, gk := range ksmVolcanoKinds {
		group, _, _ := strings.Cut(gk, "/")
		plural := plurals[gk]
		granted := false
		for _, r := range values.KSM.RBAC.ExtraRules {
			if containsString(r.APIGroups, group) && containsString(r.Resources, plural) && containsString(r.Verbs, "list") && containsString(r.Verbs, "watch") {
				granted = true
			}
		}
		if !granted {
			t.Errorf("kube-state-metrics.rbac.extraRules grants no list/watch on %s.%s", plural, group)
		}
	}
}

// values.yaml holds none of it, the generator passes the file, and the rendered
// manifest carries the same config, RBAC and mount the values describe.
func TestKubeStateMetricsCustomResourceStateIsRendered(t *testing.T) {
	if strings.Contains(readStackFile(t, "observability/kube-prometheus/values.yaml"), "customResourceState:") {
		t.Errorf("observability/kube-prometheus/values.yaml sets customResourceState; it lives in %s alone, "+
			"so the move to resource-state-metrics is one change (design 0025 §11.1)", ksmCRSFile)
	}
	gen := readStackFile(t, ksmGenerator)
	if !regexp.MustCompile(`helm template [^\n]*-f values\.yaml -f custom-resource-state\.yaml`).MatchString(gen) {
		t.Errorf("%s does not pass custom-resource-state.yaml after values.yaml; the next regeneration drops it", ksmGenerator)
	}

	values, _ := ksmCRSValuesFile(t)
	var renderedCfg map[string]any
	var args []string
	var mounts, volumes string
	var rules []map[string]any
	for _, chunk := range strings.Split(readStackFile(t, ksmInstallFile), "\n---\n") {
		if !strings.Contains(chunk, "kube-state-metrics") {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil || doc == nil {
			continue
		}
		switch name, _ := dig(doc, "metadata", "name").(string); {
		case doc["kind"] == "ConfigMap" && name == "prometheus-kube-state-metrics-customresourcestate-config":
			raw, _ := dig(doc, "data", "config.yaml").(string)
			if err := yaml.Unmarshal([]byte(raw), &renderedCfg); err != nil {
				t.Fatalf("rendered custom-resource state config: %v", err)
			}
		case doc["kind"] == "ClusterRole" && name == "prometheus-kube-state-metrics":
			for _, r := range anyList(doc["rules"]) {
				rules = append(rules, asMap(r))
			}
		case doc["kind"] == "Deployment" && name == "prometheus-kube-state-metrics":
			for _, c := range anyList(dig(doc, "spec", "template", "spec", "containers")) {
				if asMap(c)["name"] == "kube-state-metrics" {
					args = stringList(asMap(c)["args"])
					b, _ := yaml.Marshal(asMap(c)["volumeMounts"])
					mounts = string(b)
				}
			}
			b, _ := yaml.Marshal(dig(doc, "spec", "template", "spec", "volumes"))
			volumes = string(b)
		}
	}
	if renderedCfg == nil {
		t.Fatalf("%s has no custom-resource state ConfigMap; %s", ksmInstallFile, ksmRegenerateIt)
	}
	if !reflect.DeepEqual(renderedCfg, values.KSM.CRS.Config) {
		t.Errorf("the rendered custom-resource state differs from %s; %s", ksmCRSFile, ksmRegenerateIt)
	}
	if !containsString(args, "--custom-resource-state-config-file=/etc/customresourcestate/config.yaml") {
		t.Errorf("kube-state-metrics is not started with the custom-resource state config: %v", args)
	}
	if !strings.Contains(mounts, "mountPath: /etc/customresourcestate") ||
		!strings.Contains(volumes, "name: prometheus-kube-state-metrics-customresourcestate-config") {
		t.Errorf("the config is not mounted at /etc/customresourcestate:\n%s\n%s", mounts, volumes)
	}
	// The rendered ClusterRole carries every extra rule, and the CRD watch
	// kube-state-metrics discovers the Volcano kinds with.
	has := func(group, resource string) bool {
		for _, r := range rules {
			if containsString(stringList(r["apiGroups"]), group) && containsString(stringList(r["resources"]), resource) &&
				containsString(stringList(r["verbs"]), "list") && containsString(stringList(r["verbs"]), "watch") {
				return true
			}
		}
		return false
	}
	if !has("apiextensions.k8s.io", "customresourcedefinitions") {
		t.Error("kube-state-metrics may not watch CRDs, so it never sees the Volcano kinds arrive")
	}
	for _, r := range values.KSM.RBAC.ExtraRules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				if !has(g, res) {
					t.Errorf("the rendered kube-state-metrics ClusterRole lacks %s.%s; %s", res, g, ksmRegenerateIt)
				}
			}
		}
	}
}

type volcanoAlertRule struct {
	Alert       string            `json:"alert"`
	Expr        string            `json:"expr"`
	For         string            `json:"for"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

func volcanoAlertRules(t *testing.T) (map[string]any, []volcanoAlertRule) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(readStackFile(t, volcanoAlerts)), &doc); err != nil {
		t.Fatal(err)
	}
	var rule struct {
		Spec struct {
			Groups []struct {
				Rules []volcanoAlertRule `json:"rules"`
			} `json:"groups"`
		} `json:"spec"`
	}
	b, _ := yaml.Marshal(doc)
	if err := yaml.Unmarshal(b, &rule); err != nil {
		t.Fatal(err)
	}
	var out []volcanoAlertRule
	for _, g := range rule.Spec.Groups {
		out = append(out, g.Rules...)
	}
	return doc, out
}

var (
	promSelector = regexp.MustCompile(`\b(kube_volcano_[a-z_]+)\b(\{[^}]*\})?`)
	promMatcher  = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:=~|!~|!=|=)`)
	promGrouping = regexp.MustCompile(`\b(?:by|on|without|ignoring|group_left|group_right)\s*\(([^)]*)\)`)
)

// Every series an alert reads is one kube-state-metrics publishes, with labels
// it carries: PromQL accepts a name nothing publishes and returns nothing, and
// an empty result is exactly what a healthy queue looks like.
func TestVolcanoAlertsReadOnlySeriesKubeStateMetricsPublishes(t *testing.T) {
	_, cfg := ksmCRSValuesFile(t)
	published := map[string]map[string]bool{}
	allLabels := map[string]bool{}
	for _, r := range cfg.Spec.Resources {
		for name, labels := range r.published() {
			published[name] = labels
			for l := range labels {
				allLabels[l] = true
			}
		}
	}
	doc, rules := volcanoAlertRules(t)
	if doc["kind"] != "PrometheusRule" || dig(doc, "metadata", "namespace") != "adhar-system" ||
		dig(doc, "metadata", "annotations", "argocd.argoproj.io/sync-wave") != "10" {
		t.Errorf("%s must be one PrometheusRule in adhar-system at sync wave 10", volcanoAlerts)
	}
	want := map[string]bool{"BatchQueueSaturated": false, "BatchCeilingSaturated": false, "BatchQueueClosed": false}
	for _, r := range rules {
		if _, ok := want[r.Alert]; ok {
			want[r.Alert] = true
		}
		if s := r.Labels["severity"]; s != "warning" && s != "critical" {
			t.Errorf("%s has severity %q", r.Alert, s)
		}
		if r.For == "" || r.Annotations["summary"] == "" || r.Annotations["description"] == "" {
			t.Errorf("%s needs a for, a summary and a description", r.Alert)
		}
		if strings.Contains(r.Expr, "volcano_queue_") && !strings.Contains(r.Expr, "kube_volcano_queue_") {
			t.Errorf("%s reads the scheduler's volcano_queue_* series, which vanish while the scheduler is down", r.Alert)
		}
		selectors := promSelector.FindAllStringSubmatch(r.Expr, -1)
		if len(selectors) == 0 {
			t.Errorf("%s reads no kube_volcano_* series", r.Alert)
		}
		for _, m := range selectors {
			labels, ok := published[m[1]]
			if !ok {
				t.Errorf("%s reads %s, which kube-state-metrics does not publish (%s)", r.Alert, m[1], ksmCRSFile)
				continue
			}
			for _, lm := range promMatcher.FindAllStringSubmatch(m[2], -1) {
				if !labels[lm[1]] {
					t.Errorf("%s matches %s on label %q, which that series does not carry", r.Alert, m[1], lm[1])
				}
			}
		}
		for _, g := range promGrouping.FindAllStringSubmatch(r.Expr, -1) {
			for _, l := range strings.Split(g[1], ",") {
				if l = strings.TrimSpace(l); l != "" && !allLabels[l] {
					t.Errorf("%s groups or matches on %q, which no kube_volcano_* series carries", r.Alert, l)
				}
			}
		}
		// The batch tree is named by its root, which core/volcano declares.
		if strings.Contains(r.Expr, `"adhar-batch"`) && !strings.Contains(readVolcanoRootQueue(t), "name: adhar-batch") {
			t.Errorf("%s names the root queue adhar-batch, which core/volcano no longer declares", r.Alert)
		}
	}
	for alert, found := range want {
		if !found {
			t.Errorf("%s has no %s (design 0025 §11.3)", volcanoAlerts, alert)
		}
	}
}

// The root queue manifest, rendered at seed time or static.
func readVolcanoRootQueue(t *testing.T) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(stackPackagesDir(t), volcanoDir, "manifests", "root-queue.yaml*"))
	if len(files) == 0 {
		t.Fatal("core/volcano declares no root queue (root-queue.yaml or root-queue.yaml.tmpl)")
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The rule tests' input series are shaped as kube-state-metrics really
// publishes them: names and label keys from a capture of v2.20.0 running the
// rendered config. A test written against a label the exporter does not set
// passes against rules that never fire.
func TestVolcanoQueueAlertRuleTestsUseTheSeriesKubeStateMetricsPublishes(t *testing.T) {
	captured := map[string]map[string]bool{}
	f, err := os.Open("testdata/volcano-alerts/kube-state-metrics-v2.20.0.prom")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	series := regexp.MustCompile(`^(kube_volcano_[a-z_]+)\{(.*)\}`)
	label := regexp.MustCompile(`([a-z_]+)="`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := series.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		if captured[m[1]] == nil {
			captured[m[1]] = map[string]bool{}
		}
		for _, l := range label.FindAllStringSubmatch(m[2], -1) {
			captured[m[1]][l[1]] = true
		}
	}
	// What is published is what the capture shows.
	_, cfg := ksmCRSValuesFile(t)
	for _, r := range cfg.Spec.Resources {
		for name := range r.published() {
			if captured[name] == nil {
				t.Errorf("%s is configured but absent from the capture; recapture it", name)
			}
		}
	}
	var tests struct {
		Tests []struct {
			InputSeries []struct {
				Series string `json:"series"`
			} `json:"input_series"`
		} `json:"tests"`
	}
	if err := yaml.Unmarshal(mustReadTestdata(t, "testdata/volcano-alerts/tests.yaml"), &tests); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tc := range tests.Tests {
		for _, in := range tc.InputSeries {
			n++
			m := series.FindStringSubmatch(in.Series)
			if m == nil {
				t.Errorf("input series %q is not a kube_volcano_* series", in.Series)
				continue
			}
			got, ok := captured[m[1]]
			if !ok {
				t.Errorf("input series %s is not one kube-state-metrics publishes", m[1])
				continue
			}
			for _, l := range label.FindAllStringSubmatch(m[2], -1) {
				if !got[l[1]] {
					t.Errorf("input series %s carries label %q, which kube-state-metrics never sets on it", m[1], l[1])
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("testdata/volcano-alerts/tests.yaml has no input series")
	}
}

// ruleToolsRequired names the environment variable that turns a missing
// rule-testing tool (lua, promtool) from a skip into a failure. The two tests
// that execute the Argo CD health checks and the queue alerts are where every
// semantic defect in them was caught, and a skip reads as a pass: CI sets this
// beside installing the tools, so a runner that lost them fails loudly instead
// of quietly no longer testing the rules.
const ruleToolsRequired = "ADHAR_REQUIRE_RULE_TOOLS"

// ruleTool returns the first of names on PATH. With none it skips, or fails
// when ruleToolsRequired is "1".
func ruleTool(t *testing.T, hint string, names ...string) string {
	t.Helper()
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	msg := fmt.Sprintf("none of %v is installed; %s", names, hint)
	if os.Getenv(ruleToolsRequired) == "1" {
		t.Fatalf("%s (%s=1 requires it)", msg, ruleToolsRequired)
	}
	t.Skip(msg)
	return ""
}

func mustReadTestdata(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The alerts themselves, evaluated: promtool runs testdata/volcano-alerts/tests.yaml
// against the PrometheusRule's groups. Skipped where promtool is not installed;
// the same check without it:
//
//	docker run --rm --entrypoint promtool -v "$DIR":/w -w /w quay.io/prometheus/prometheus:v3.5.0 test rules tests.yaml
func TestVolcanoQueueAlertsPassTheirRuleTests(t *testing.T) {
	promtool := ruleTool(t, "see the comment above for the container equivalent", "promtool")
	doc, _ := volcanoAlertRules(t)
	spec, _ := yaml.Marshal(doc["spec"])
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rules.yaml"), spec, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests.yaml"), mustReadTestdata(t, "testdata/volcano-alerts/tests.yaml"), 0o600); err != nil {
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

// Volcano's own series (volcano_e2e_*, volcano_queue_*) come from the scheduler
// and controllers through core/volcano's ServiceMonitors. Each must select a
// Service whose "metrics" port reaches the port the component listens on with
// --enable-metrics: v1.16.0 defaults --listen-address to :8080 (scheduler) and
// :8081 (controllers), and metrics are off unless the flag is set.
func TestVolcanoServiceMonitorsReachTheMetricsListeners(t *testing.T) {
	listen := map[string]string{"volcano-scheduler": "8080", "volcano-controllers": "8081"}
	services := map[string]map[string]any{}
	deployments := volcanoDeployments(t)
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) == "Service" {
			services[volcanoName(doc)] = doc
		}
	}
	monitors := volcanoPackageDocs(t)["servicemonitor.yaml"]
	for component, port := range listen {
		d, ok := deployments[component]
		if !ok {
			t.Errorf("core/volcano has no %s Deployment", component)
			continue
		}
		args := volcanoArgs(t, d)
		if !containsString(args, "--enable-metrics=true") {
			t.Errorf("%s does not run with --enable-metrics=true; it publishes nothing", component)
		}
		for _, a := range args {
			if strings.HasPrefix(a, "--listen-address=") && !strings.HasSuffix(a, ":"+port) {
				t.Errorf("%s listens at %s, not :%s", component, a, port)
			}
		}
		podLabels := asMap(dig(d, "spec", "template", "metadata", "labels"))
		scraped := false
		for _, m := range monitors {
			sel := asMap(dig(m, "spec", "selector", "matchLabels"))
			for _, svc := range services {
				if !labelsMatch(sel, asMap(dig(svc, "metadata", "labels"))) || !labelsMatch(asMap(dig(svc, "spec", "selector")), podLabels) {
					continue
				}
				for _, p := range anyList(dig(svc, "spec", "ports")) {
					pm := asMap(p)
					for _, ep := range anyList(dig(m, "spec", "endpoints")) {
						if asMap(ep)["port"] == pm["name"] && fmt.Sprint(pm["targetPort"]) == port {
							scraped = true
						}
					}
				}
			}
		}
		if !scraped {
			t.Errorf("no core/volcano ServiceMonitor reaches %s's metrics port %s", component, port)
		}
	}
}

func labelsMatch(selector, labels map[string]any) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
