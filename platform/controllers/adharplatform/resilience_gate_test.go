package adharplatform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
