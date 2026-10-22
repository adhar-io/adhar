package adharplatform

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// stackDocs returns every YAML document of a stack file, decoded.
func stackDocs(t *testing.T, rel string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, doc := range strings.Split(readStackFile(t, rel), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

func stackObject(t *testing.T, rel, kind, name string) map[string]any {
	t.Helper()
	for _, d := range stackDocs(t, rel) {
		md, _ := d["metadata"].(map[string]any)
		if d["kind"] == kind && md["name"] == name {
			return d
		}
	}
	t.Fatalf("%s has no %s/%s", rel, kind, name)
	return nil
}

func configMapData(t *testing.T, rel, name string) map[string]string {
	t.Helper()
	raw, _ := stackObject(t, rel, "ConfigMap", name)["data"].(map[string]any)
	out := map[string]string{}
	for k, v := range raw {
		out[k], _ = v.(string)
	}
	return out
}

const scorecardsDir = "application/adhar-scorecards/manifests/"

// Chaos belongs before production: the runner kills pods in whatever
// environments it is configured for, so prod must never be among them by
// default, and the runner must take the list from the config rather than
// deciding for itself.
func TestTheResilienceRunnerNeverTargetsProductionByDefault(t *testing.T) {
	cfg := configMapData(t, scorecardsDir+"scorecard-crd-and-samples.yaml", "adhar-scorecard-config")
	envs := cfg["runner_environments"]
	if envs == "" {
		t.Fatal("adhar-scorecard-config has no runner_environments")
	}
	for _, e := range strings.Split(envs, ",") {
		if strings.TrimSpace(e) == "prod" || strings.TrimSpace(e) == "production" {
			t.Errorf("runner_environments %q includes production: the resilience runner would kill production pods every day", envs)
		}
	}
	run := configMapData(t, scorecardsDir+"resilience-runner.yaml", "adhar-resilience-script")["run.sh"]
	if !strings.Contains(run, `ENVS="$(readcfg runner_environments test,dev)"`) {
		t.Error("run.sh no longer takes its target environments from runner_environments (default test,dev)")
	}
	for _, want := range []string{"chaosengines.litmuschaos.io", `type: "httpProbe", mode: "Continuous"`, "ADHAR_K6_SUMMARY", `case "$phase" in Completed*`, `exps="$(ann adhar.io/chaos-experiments`} {
		if !strings.Contains(run, want) {
			t.Errorf("run.sh lacks %q", want)
		}
	}
	if !strings.Contains(configMapData(t, scorecardsDir+"resilience-runner.yaml", "adhar-resilience-script")["load.js"], "'ADHAR_K6_SUMMARY '") {
		t.Error("load.js no longer prints the ADHAR_K6_SUMMARY line run.sh parses; every load test would record an error")
	}
}

// The runner creates the k6 Job and the ChaosEngine and writes the results; a
// missing verb fails each run silently into an "error" record.
func TestTheResilienceRunnerMayDoWhatItDoes(t *testing.T) {
	role := stackObject(t, scorecardsDir+"resilience-runner.yaml", "Role", "adhar-resilience-runner")
	b, _ := json.Marshal(role["rules"])
	rules := string(b)
	for _, want := range []string{`"jobs"`, `"chaosengines"`, `"chaosresults"`, `"configmaps"`, `"pods/log"`, `"create"`, `"delete"`} {
		if !strings.Contains(rules, want) {
			t.Errorf("Role adhar-resilience-runner lacks %s", want)
		}
	}
	reader := stackObject(t, scorecardsDir+"rbac.yaml", "ClusterRole", "adhar-scorecard-reader")
	b, _ = json.Marshal(reader["rules"])
	for _, want := range []string{`"rollouts"`, `"horizontalpodautoscalers"`, `"poddisruptionbudgets"`, `"pods"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the scorer cannot read %s, so its resilience signals would silently read empty", want)
		}
	}
}

// Runs the REAL scoring program (score.jq from the manifest) on fixtures.
func TestTheScorerGradesApplicationsOnPerformanceAndResilience(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	script := configMapData(t, scorecardsDir+"scorer-cronjob.yaml", "adhar-scorecard-script")
	if !strings.Contains(script["score.sh"], `jq -n -f "$SCRIPT_DIR/score.jq"`) {
		t.Fatal("score.sh no longer runs score.jq")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	now := time.Now().Unix()
	wl := func(ns, name string, replicas int, hardened bool) string {
		return fmt.Sprintf(`"%s/Rollout/%s":{"kind":"Rollout","ns":%q,"name":%q,"replicas":%d,"podLabels":{"app":%q},"progressive":true,"spread":%t,`+
			`"containers":[{"image":"h/library/%s@sha256:1","probes":true,"res":true,"preStop":%t}]}`, ns, name, ns, name, replicas, name, hardened, name, hardened)
	}
	files := map[string]string{
		"apps": `{"items":[
		  {"name":"adhar-shop-dev","app":"shop","env":"dev","ns":"dev","health":"Healthy","sync":"Synced","own":{"total":2,"wl":["dev/Rollout/shop"],"routes":1,"cnpg":[]}},
		  {"name":"adhar-shop-prod","app":"shop","env":"prod","ns":"prod","health":"Healthy","sync":"Synced","own":{"total":2,"wl":["prod/Rollout/shop"],"routes":1,"cnpg":[]}},
		  {"name":"adhar-cart-dev","app":"cart","env":"dev","ns":"dev","health":"Healthy","sync":"Synced","own":{"total":2,"wl":["dev/Rollout/cart"],"routes":1,"cnpg":[]}},
		  {"name":"keycloak","app":null,"env":"adhar-system","ns":"adhar-system","health":"Healthy","sync":"Synced","own":{"total":1,"wl":[],"routes":1,"cnpg":[]}}]}`,
		"workloads": `{"byKey":{` + wl("dev", "shop", 1, true) + "," + wl("prod", "shop", 2, true) + "," + wl("dev", "cart", 1, false) + `},"byInstance":{},"byNs":{}}`,
		"reports":   `{"byKey":{},"byNs":{}}`,
		"routes":    `{"nsSet":{}}`,
		"schedules": `{"all":false,"nsSet":{}}`,
		"cnpg":      `{"byKey":{},"byNs":{}}`,
		"hpas":      `{"byTarget":{"prod/Rollout/shop":{"min":2,"max":5}}}`,
		"pdbs":      `{"byNs":{"prod":[{"app":"shop"}]}}`,
		"perfcenter": fmt.Sprintf(`{"shop":{"count":4,"tests":["shop-load","shop-soak"],"latest":{"test":"shop-load","trigger":"schedule","verdict":"pass","reasons":"","at":%[1]q},"judged":{"test":"shop-load","trigger":"schedule","verdict":"pass","reasons":"","at":%[1]q}},"cart":{"count":2,"tests":["cart-load"],"latest":{"test":"cart-load","trigger":"console","verdict":"","at":%[1]q},"judged":null,"unjudged":2}}`,
			time.Unix(now-7200, 0).UTC().Format(time.RFC3339)),
		"gameday": fmt.Sprintf(`{"shop":{"count":2,"experiments":["pod-network-loss"],"latest":{"experiment":"pod-network-loss","verdict":"Pass","probe":"100","at":%q}}}`,
			time.Unix(now-3600, 0).UTC().Format(time.RFC3339)),
		// No Trivy report and no metrics-server: supply chain is graded on the
		// images alone and efficiency reports null (asserted below).
		"vulns":   `{}`,
		"metrics": `{}`,
		"pods":    `{"prod":[{"ns":"prod","labels":{"app":"shop","x":"y"},"restarts":0}],"dev":[{"ns":"dev","labels":{"app":"cart"},"restarts":9}]}`,
		"evidence": fmt.Sprintf(`{"shop":{"environment":"test","testedAtEpoch":%d,
		  "performance":{"status":"passed","p95Ms":120,"errorRate":0,"rps":200,"vus":10,"thresholdP95Ms":500},
		  "chaos":{"status":"passed","verdict":"Pass","probeSuccessPercentage":100,"recoverySeconds":20,"replicas":2,"detail":"kept answering"},
		  "chaosSuite":{"pod-network-latency":{"status":"passed","probeSuccessPercentage":100,"detail":"kept answering with 300 ms of added network latency"},
		                "pod-cpu-hog":{"status":"passed","probeSuccessPercentage":100,"detail":"kept answering with its CPU saturated"},
		                "pod-memory-hog":{"status":"passed","probeSuccessPercentage":100,"detail":"kept answering under memory pressure"}},
		  "gates":{"test":{"verdict":"pass","reasons":[],"environment":"test","image":"h/library/shop@sha256:1","at":%[2]q}},
		  "slo":{"environment":"prod","requests":5000,"p95Ms":150,"errorRatio":0.0001,"targetAvailability":99.9}},
		 "cart":{"chaosSuite":{"pod-memory-hog":{"status":"failed","probeSuccessPercentage":40,"detail":"stopped answering under memory pressure"}},
		  "gates":{"dev":{"verdict":"fail","reasons":["pod-delete failed: stopped answering while pods were deleted (1 replica(s))"],
		  "environment":"dev","image":"h/library/cart@sha256:1","at":%[2]q}}}}`, now-3600, time.Unix(now-1800, 0).UTC().Format(time.RFC3339)),
	}
	args := []string{"-n", "-f", write("score.jq", script["score.jq"])}
	for k, body := range files {
		args = append(args, "--slurpfile", k+"f", write(k+".json", body))
	}
	for k, v := range map[string]string{"wrel": "35", "wsec": "25", "wobs": "15", "wops": "25", "wperf": "20", "wres": "25",
		"wsup": "20", "weff": "10", "cpumin": "10", "cpumax": "100", "memmin": "20", "memmax": "100",
		"ga": "90", "gb": "80", "gc": "70", "gd": "60", "evdays": "7", "p95default": "500", "recoverymax": "120", "now": fmt.Sprint(now)} {
		args = append(args, "--argjson", k, v)
	}
	args = append(args, "--arg", "envorder", "prod,test,dev", "--arg", "ts", "now", "--arg", "trusted", "harbor")
	out, err := exec.Command("jq", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("score.jq failed: %v\n%s", err, out)
	}
	var res struct {
		Services []struct {
			Kind, Name, Environment string
			Score                   int
			Categories              map[string]*int
			Signals                 []struct {
				Name       string
				Applicable bool
				Score      float64
				Detail     string
			}
		}
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	by := map[string]int{}
	for i, s := range res.Services {
		by[s.Name] = i
	}
	for _, n := range []string{"adhar-shop-dev", "adhar-shop-prod", "adhar-cart-dev"} {
		if _, ok := by[n]; ok {
			t.Errorf("%s is scored per environment; an application must be ONE record named after it, or the catalog entity never joins it", n)
		}
	}
	shop, cart := res.Services[by["shop"]], res.Services[by["cart"]]
	if shop.Kind != "application" || shop.Environment != "prod" {
		t.Errorf("shop graded as %s on %q; want an application graded on prod, its most-promoted environment", shop.Kind, shop.Environment)
	}
	if p, r := shop.Categories["performance"], shop.Categories["resilience"]; p == nil || r == nil || *p != 100 || *r != 100 {
		t.Errorf("a fully tested and hardened application should score 100 on performance and resilience, got %v/%v", p, r)
	}
	// Nothing to measure efficiency with (no metrics-server answer) is null, never a free 100;
	// supply chain is graded on the running images alone.
	if e := shop.Categories["efficiency"]; e != nil {
		t.Errorf("efficiency without metrics-server should be null, got %d", *e)
	}
	if sc := shop.Categories["supply_chain"]; sc == nil {
		t.Errorf("supply chain should be graded on the workload's images")
	}
	sig := map[string]float64{}
	for _, s := range cart.Signals {
		if s.Applicable {
			sig[s.Name] = s.Score
		}
	}
	for _, n := range []string{"load_test", "chaos_pod_delete", "replicas_ha", "pod_disruption_budget", "graceful_shutdown", "restart_stability"} {
		if v, ok := sig[n]; !ok || v != 0 {
			t.Errorf("an untested single-replica application with no PDB and 9 restarts should FAIL %s (applicable, 0); got %v (applicable %t)", n, v, ok)
		}
	}
	shopSig := map[string]float64{}
	for _, sg := range shop.Signals {
		if sg.Applicable {
			shopSig[sg.Name] = sg.Score
		}
	}
	for _, n := range []string{"perf_center_gate", "chaos_gameday"} {
		if v, ok := shopSig[n]; !ok || v != 1 {
			t.Errorf("a passing team-run %s scored %v (applicable %t)", n, v, ok)
		}
		if _, ok := sig[n]; ok {
			t.Errorf("%s is applicable on an application no team test or game day targets", n)
		}
	}
	// cart's Performance Center results were recorded with no verdict (the
	// console recorded its own runs that way until 2026-10-22): never judged,
	// so neither a pass nor a failure — and the scorecard says why.
	for _, sg := range cart.Signals {
		if sg.Name == "perf_center_gate" && (sg.Applicable || !strings.Contains(sg.Detail, "none with a recorded verdict")) {
			t.Errorf("unjudged Performance Center results should leave perf_center_gate not applicable with a reason; got applicable=%t %q", sg.Applicable, sg.Detail)
		}
	}
	// The promotion gate and the memory-pressure fault the JVM suites run
	// (2026-10-21): pass on shop, a held-back build on cart.
	for _, n := range []string{"chaos_memory_pressure", "promotion_gate"} {
		if v, ok := shopSig[n]; !ok || v != 1 {
			t.Errorf("shop passed %s but it scored %v (applicable %t)", n, v, ok)
		}
	}
	if v, ok := sig["promotion_gate"]; !ok || v != 0 {
		t.Errorf("cart's build failed the promotion gate; promotion_gate should be applicable and 0, got %v (applicable %t)", v, ok)
	}
	if v, ok := sig["chaos_memory_pressure"]; !ok || v != 0 {
		t.Errorf("cart failed its memory-pressure fault; chaos_memory_pressure should be applicable and 0, got %v (applicable %t)", v, ok)
	}
	for _, n := range []string{"chaos_network_latency", "chaos_cpu_stress"} {
		if v, ok := shopSig[n]; !ok || v != 1 {
			t.Errorf("the chaos suite's %s passed but scored %v (applicable %t)", n, v, ok)
		}
		if _, ok := sig[n]; ok {
			t.Errorf("%s is applicable on an application the suite never ran against", n)
		}
	}
	// Scorecards cover the applications teams deliver — the Service Catalog's
	// list — never the platform's own packages (2026-10-22: 78 packages buried
	// 3 applications). keycloak is in the fixture as a package and must not be
	// graded, counted or averaged.
	if _, ok := by["keycloak"]; ok {
		t.Error("the platform package keycloak was graded; scorecards are for applications only")
	}
	for _, s := range res.Services {
		if s.Kind != "application" {
			t.Errorf("%s was graded as %q; only applications are scored", s.Name, s.Kind)
		}
	}
	if len(res.Services) != 2 {
		t.Errorf("want exactly the 2 applications (shop, cart), got %d services", len(res.Services))
	}
}

// The scorer must not even collect platform packages' resources: everything
// but the Argo CD Applications and Velero schedules is read from the
// namespaces applications deploy into.
func TestTheScorerOnlyScansApplicationNamespaces(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "application",
		"adhar-scorecards", "manifests", "scorer-cronjob.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `select(.metadata.labels["adhar.io/application"] != null)`) {
		t.Error("score.sh no longer keeps only Applications labelled adhar.io/application")
	}
	allowed := map[string]bool{"applications.argoproj.io": true, "schedules.velero.io": true}
	for _, m := range regexp.MustCompile(`(?m)^\s+fetch (\S+) \S+$`).FindAllStringSubmatch(src, -1) {
		if !allowed[m[1]] {
			t.Errorf("%s is fetched cluster-wide; collect it with fetch_ns over the application namespaces", m[1])
		}
	}
	// fetch() itself, and the Performance Center results — labelled per
	// application — are the only other cluster-wide reads.
	for _, line := range regexp.MustCompile(`kubectl get [^|\n]* -A[^\n]*`).FindAllString(src, -1) {
		if !strings.Contains(line, `"$1"`) && !strings.Contains(line, "adhar.io/perf-result=true,adhar.io/application") {
			t.Errorf("cluster-wide read outside fetch(): %s", line)
		}
	}
}

// Released Consoles read perfCenter.latest.verdict unguarded, so `latest` must
// stay the newest run whenever there are results; only `judged` may be null.
// Making `latest` null crashed the Performance Center page (2026-10-22).
func TestPerfCenterProjectionKeepsLatestForTheConsole(t *testing.T) {
	src := readStackFile(t, "application/adhar-scorecards/manifests/scorer-cronjob.yaml")
	i := strings.Index(src, `jq -c '[ (.items // [])[] | (.metadata.annotations // {}) as $a | {`)
	j := strings.Index(src[i:], `' "$WORKDIR/perfcenter.raw"`)
	if i < 0 || j < 0 {
		t.Fatal("cannot find the Performance Center projection in score.sh")
	}
	program := src[i+len(`jq -c '`) : i+j]
	program = regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAllString(program, "")
	cm := func(app, verdict, at string) string {
		ann := fmt.Sprintf(`"adhar.io/perf-finished":%q`, at)
		if verdict != "" {
			ann += fmt.Sprintf(`,"adhar.io/perf-verdict":%q`, verdict)
		}
		return fmt.Sprintf(`{"metadata":{"namespace":"test","creationTimestamp":%q,"labels":{"adhar.io/application":%q,"adhar.io/perf-test":"%s-load","adhar.io/perf-trigger":"console"},"annotations":{%s}}}`, at, app, app, ann)
	}
	in := `{"items":[` + strings.Join([]string{
		cm("shop", "pass", "2026-10-22T01:00:00Z"),
		cm("shop", "", "2026-10-22T02:00:00Z"),
		cm("cart", "", "2026-10-22T01:00:00Z"),
	}, ",") + `]}`
	cmd := exec.Command("jq", "-c", program)
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("projection failed: %v\n%s", err, out)
	}
	var got map[string]struct {
		Latest *struct{ Verdict, At string }
		Judged *struct{ Verdict, At string }
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if s := got["shop"]; s.Latest == nil || s.Latest.At != "2026-10-22T02:00:00Z" || s.Judged == nil || s.Judged.Verdict != "pass" {
		t.Errorf("shop: latest must be the newest run and judged the newest with a verdict; got %s", out)
	}
	if c := got["cart"]; c.Latest == nil || c.Judged != nil {
		t.Errorf("cart: latest must stay set (the Console reads it) and judged be null; got %s", out)
	}
}
