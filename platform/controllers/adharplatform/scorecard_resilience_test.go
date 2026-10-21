package adharplatform

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		"perfcenter": fmt.Sprintf(`{"shop":{"count":4,"tests":["shop-load","shop-soak"],"latest":{"test":"shop-load","trigger":"schedule","verdict":"pass","reasons":"","at":%q}}}`,
			time.Unix(now-7200, 0).UTC().Format(time.RFC3339)),
		"gameday": fmt.Sprintf(`{"shop":{"count":2,"experiments":["pod-network-loss"],"latest":{"experiment":"pod-network-loss","verdict":"Pass","probe":"100","at":%q}}}`,
			time.Unix(now-3600, 0).UTC().Format(time.RFC3339)),
		"pods": `{"prod":[{"ns":"prod","labels":{"app":"shop","x":"y"},"restarts":0}],"dev":[{"ns":"dev","labels":{"app":"cart"},"restarts":9}]}`,
		"evidence": fmt.Sprintf(`{"shop":{"environment":"test","testedAtEpoch":%d,
		  "performance":{"status":"passed","p95Ms":120,"errorRate":0,"rps":200,"vus":10,"thresholdP95Ms":500},
		  "chaos":{"status":"passed","verdict":"Pass","probeSuccessPercentage":100,"recoverySeconds":20,"replicas":2,"detail":"kept answering"},
		  "chaosSuite":{"pod-network-latency":{"status":"passed","probeSuccessPercentage":100,"detail":"kept answering with 300 ms of added network latency"},
		                "pod-cpu-hog":{"status":"passed","probeSuccessPercentage":100,"detail":"kept answering with its CPU saturated"}},
		  "slo":{"environment":"prod","requests":5000,"p95Ms":150,"errorRatio":0.0001,"targetAvailability":99.9}}}`, now-3600),
	}
	args := []string{"-n", "-f", write("score.jq", script["score.jq"])}
	for k, body := range files {
		args = append(args, "--slurpfile", k+"f", write(k+".json", body))
	}
	for k, v := range map[string]string{"wrel": "35", "wsec": "25", "wobs": "15", "wops": "25", "wperf": "20", "wres": "25",
		"ga": "90", "gb": "80", "gc": "70", "gd": "60", "evdays": "7", "p95default": "500", "recoverymax": "120", "now": fmt.Sprint(now)} {
		args = append(args, "--argjson", k, v)
	}
	args = append(args, "--arg", "envorder", "prod,test,dev", "--arg", "ts", "now")
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
	shop, cart, kc := res.Services[by["shop"]], res.Services[by["cart"]], res.Services[by["keycloak"]]
	if shop.Kind != "application" || shop.Environment != "prod" {
		t.Errorf("shop graded as %s on %q; want an application graded on prod, its most-promoted environment", shop.Kind, shop.Environment)
	}
	if p, r := shop.Categories["performance"], shop.Categories["resilience"]; p == nil || r == nil || *p != 100 || *r != 100 {
		t.Errorf("a fully tested and hardened application should score 100 on performance and resilience, got %v/%v", p, r)
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
	for _, n := range []string{"chaos_network_latency", "chaos_cpu_stress"} {
		if v, ok := shopSig[n]; !ok || v != 1 {
			t.Errorf("the chaos suite's %s passed but scored %v (applicable %t)", n, v, ok)
		}
		if _, ok := sig[n]; ok {
			t.Errorf("%s is applicable on an application the suite never ran against", n)
		}
	}
	if kc.Kind != "package" || kc.Categories["performance"] != nil || kc.Categories["resilience"] != nil {
		t.Error("a platform package must report performance/resilience as null (nothing was tested), never a free score")
	}
	if kc.Score != 100 {
		t.Errorf("a platform package's score moved to %d: the new categories must not change how packages are graded", kc.Score)
	}
}
