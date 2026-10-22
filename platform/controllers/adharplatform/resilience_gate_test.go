package adharplatform

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Prod requests only freight VERIFIED in test, so the application's load test
// and chaos suite belong in test's verification: a build that slows down under
// load or stops answering when a pod dies never reaches prod (2026-10-21).
func TestTheTestEnvironmentGatesOnEachApplicationsResilienceSuite(t *testing.T) {
	req := projectRequest(t,
		deliveredApplication(t, "cart", "team-payments", "shop", ""),
		deliveredApplication(t, "api", "team-payments", "shop", ""),
	)
	docs := renderGoTemplate(t, req, "compositions", "project", "local.yaml")

	gates := map[string][]string{}
	var script string
	for _, at := range manifestsOfKind(docs, "AnalysisTemplate") {
		env := dig(at, "metadata", "labels", "adhar.io/environment").(string)
		metrics, _ := dig(at, "spec", "metrics").([]any)
		for _, m := range metrics {
			mm := m.(map[string]any)
			name := mm["name"].(string)
			if !strings.HasSuffix(name, "-resilience") {
				continue
			}
			gates[env] = append(gates[env], name)
			job, _ := dig(mm, "provider", "job", "spec", "template", "spec").(map[string]any)
			if job == nil {
				t.Fatalf("%s/%s is not a job metric", env, name)
			}
			if job["serviceAccountName"] != "adhar-resilience-gate" {
				t.Errorf("%s/%s runs as %v", env, name, job["serviceAccountName"])
			}
			c := job["containers"].([]any)[0].(map[string]any)
			script = c["args"].([]any)[0].(string)
		}
	}
	if strings.Join(gates["test"], ",") != "api-resilience,cart-resilience" && strings.Join(gates["test"], ",") != "cart-resilience,api-resilience" {
		t.Errorf("test environment gates = %v, want one per application", gates["test"])
	}
	if len(gates["dev"]) != 0 || len(gates["prod"]) != 0 {
		t.Errorf("the gate belongs only before prod: dev=%v prod=%v", gates["dev"], gates["prod"])
	}

	// The script starts the runner in gate mode and exits with its verdict.
	for _, want := range []string{`"VERIFY_APP"`, `"VERIFY_NAMESPACE"`, `[ "$ok" = 1 ]`} {
		if !strings.Contains(script, want) {
			t.Errorf("gate script lost %q", want)
		}
	}
	f := filepath.Join(t.TempDir(), "gate.sh")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
		t.Errorf("the gate script is not valid bash: %v\n%s", err, out)
	}

	// Its identity: allowed to start the runner, and Kargo allowed to run it.
	names := map[string]bool{}
	for _, kind := range []string{"ServiceAccount", "RoleBinding"} {
		for _, m := range manifestsOfKind(docs, kind) {
			names[kind+"/"+dig(m, "metadata", "namespace").(string)+"/"+dig(m, "metadata", "name").(string)] = true
		}
	}
	for _, want := range []string{
		"ServiceAccount/shop/adhar-resilience-gate",
		"RoleBinding/shop/adhar-kargo-verification-jobs",
		"RoleBinding/adhar-system/adhar-resilience-gate-shop",
	} {
		if !names[want] {
			t.Errorf("missing %s (have %v)", want, names)
		}
	}
}

// A booting pod is not a broken one: every per-app health metric waits before
// its first sample, or a slow JVM start fails the Stage (2026-10-21).
func TestVerificationWaitsForTheNewPodsToBoot(t *testing.T) {
	req := projectRequest(t, deliveredApplication(t, "cart", "team-payments", "shop", ""))
	docs := renderGoTemplate(t, req, "compositions", "project", "local.yaml")
	checked := 0
	for _, at := range manifestsOfKind(docs, "AnalysisTemplate") {
		metrics, _ := dig(at, "spec", "metrics").([]any)
		for _, m := range metrics {
			mm := m.(map[string]any)
			if _, isJob := dig(mm, "provider", "job").(map[string]any); isJob {
				continue
			}
			checked++
			if d, _ := mm["initialDelay"].(string); d == "" {
				t.Errorf("metric %v samples a pod that may still be booting (no initialDelay)", mm["name"])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no health metrics rendered; this test checks nothing")
	}
}

// A release in progress is not a broken release. Sampling "is the Rollout
// Healthy / are the pods ready" at fixed times failed a JVM canary that was
// still walking its steps (my-service in test, 2026-10-22). The rollout check
// fails only on Degraded or on being unhealthy for the WHOLE settle window, the
// pod check only on a pod still not ready once older than that window — and
// the run must last long enough for those to be observed at all.
func TestVerificationFailsBrokenReleasesNotOnesInProgress(t *testing.T) {
	// Once with the published settings, once with an application that sets
	// neither count nor settleSeconds, so the composition's own defaults must
	// hold too.
	bare := deliveredApplication(t, "cart", "team-payments", "shop", "")
	v := dig(bare, "status", "delivery", "verification").(map[string]any)
	delete(v, "count")
	delete(v, "settleSeconds")
	for _, app := range []map[string]any{deliveredApplication(t, "cart", "team-payments", "shop", ""), bare} {
		checkVerificationSemantics(t, renderGoTemplate(t, projectRequest(t, app), "compositions", "project", "local.yaml"))
	}
}

func checkVerificationSemantics(t *testing.T, docs []map[string]any) {
	t.Helper()
	window := regexp.MustCompile(`min_over_time\(.*\[(\d+)s:30s\]\)`)
	age := regexp.MustCompile(`\(time\(\) - kube_pod_created\{[^}]*\}\) > (\d+)\)`)
	seen := 0
	for _, at := range manifestsOfKind(docs, "AnalysisTemplate") {
		metrics, _ := dig(at, "spec", "metrics").([]any)
		for _, m := range metrics {
			mm := m.(map[string]any)
			name, _ := mm["name"].(string)
			q, _ := dig(mm, "provider", "prometheus", "query").(string)
			span := func() int {
				d, _ := time.ParseDuration(mm["initialDelay"].(string))
				iv, _ := time.ParseDuration(mm["interval"].(string))
				var n int
				switch c := mm["count"].(type) {
				case int:
					n = c
				case int64:
					n = int(c)
				case float64:
					n = int(c)
				}
				return int(d.Seconds()) + n*int(iv.Seconds())
			}
			switch {
			case strings.HasSuffix(name, "-rollout-healthy"):
				seen++
				if !strings.Contains(q, `phase="Degraded"`) {
					t.Errorf("%s does not fail on a Degraded Rollout: %s", name, q)
				}
				w := window.FindStringSubmatch(q)
				if w == nil {
					t.Fatalf("%s fails on any not-Healthy sample, including a canary in progress: %s", name, q)
				}
				s, _ := strconv.Atoi(w[1])
				if s < 300 || span() < s+60 {
					t.Errorf("%s: settle window %ds, run %ds — the window must allow a JVM canary and the run must outlast it", name, s, span())
				}
			case strings.HasSuffix(name, "-pods-ready"):
				seen++
				a := age.FindStringSubmatch(q)
				if a == nil {
					t.Fatalf("%s fails on a pod that is merely booting: %s", name, q)
				}
				if s, _ := strconv.Atoi(a[1]); s < 300 || span() < s+60 {
					t.Errorf("%s: pods count as stuck after %ss but the run lasts %ds", name, a[1], span())
				}
			}
		}
	}
	if seen < 2 {
		t.Fatalf("rendered %d rollout/pod checks; this test checks nothing", seen)
	}
}
