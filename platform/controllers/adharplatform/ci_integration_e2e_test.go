package adharplatform

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every build runs the templates' end-to-end and integration suites before it
// is signed (2026-10-21): Playwright for the frontends, Karate against the
// real app and its Testcontainers dependencies for the JVM backends — the
// latter through a kubedock sidecar, because a CI pod has no Docker API.
func TestCIRunsE2EAndIntegrationSuitesBeforeSigning(t *testing.T) {
	dir := filepath.Join(stackPackagesDir(t), "application", "adhar-supply-chain", "manifests")
	docs := map[string]map[string]any{}
	for _, f := range []string{"70-app-ci.yaml", "72-app-ci-stages.yaml", "74-testcontainers-sandbox.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range strings.Split(string(b), "\n---\n") {
			var d map[string]any
			if yaml.Unmarshal([]byte(raw), &d) != nil || d == nil {
				continue
			}
			docs[d["kind"].(string)+"/"+dig(d, "metadata", "name").(string)] = d
		}
	}

	// The pipeline: e2e beside the unit tests, and signing waits for both.
	tasks, _ := dig(docs["Pipeline/app-ci"], "spec", "tasks").([]any)
	byName := map[string]map[string]any{}
	for _, x := range tasks {
		m := x.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if e2e := byName["e2e"]; e2e == nil || dig(e2e, "taskRef", "name") != "e2e-playwright" {
		t.Fatal("app-ci has no e2e stage running e2e-playwright")
	}
	// Only a repository that ships a Playwright suite gets the e2e stage, and
	// that is decided BEFORE its pod is requested: a JVM service (my-service,
	// 2026-10-22) waited indefinitely for a browser pod it did not need on the
	// full node its workspace pins every task to, so it was never signed.
	when, _ := byName["e2e"]["when"].([]any)
	gated := false
	for _, w := range when {
		m := w.(map[string]any)
		vals, _ := m["values"].([]any)
		if m["input"] == "$(tasks.build.results.e2e)" && m["operator"] == "in" && len(vals) == 1 && vals[0] == "true" {
			gated = true
		}
	}
	if !gated {
		t.Errorf("the e2e stage is not gated on the toolchain's e2e result; every backend would schedule a browser pod")
	}
	// The flag travels detect-toolchain → build → e2e, like the versions do.
	build, err := os.ReadFile(filepath.Join(dir, "60-build-pipelines.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), `printf '%s' "$(params.e2e)"           > "$(results.e2e.path)"`) {
		t.Error("buildpacks-build does not pass the e2e flag through to its results")
	}
	passed := false
	for _, p := range byName["build"]["params"].([]any) {
		pm := p.(map[string]any)
		if pm["name"] == "e2e" && pm["value"] == "$(tasks.toolchain.results.e2e)" {
			passed = true
		}
	}
	if !passed {
		t.Error("the build stage is not given detect-toolchain's e2e flag")
	}
	script := dig(docs["Task/detect-toolchain"], "spec", "steps").([]any)[0].(map[string]any)["script"].(string)
	detect := regexp.MustCompile(`(?s)\n\s*e2e=false\n.*?printf '%s' "\$e2e" > "\$\(results\.e2e\.path\)"`).FindString(script)
	if detect == "" {
		t.Fatal("detect-toolchain no longer writes the e2e result")
	}
	for _, c := range []struct {
		files []string
		want  string
	}{
		{[]string{"pom.xml"}, "false"},
		{[]string{"package.json"}, "false"},
		{[]string{"package.json", "e2e/playwright.config.ts"}, "true"},
		{[]string{"e2e/playwright.config.mjs"}, "true"},
	} {
		dir := t.TempDir()
		for _, f := range c.files {
			_ = os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
			_ = os.WriteFile(filepath.Join(dir, f), nil, 0o644)
		}
		out := filepath.Join(dir, ".result")
		cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+strings.ReplaceAll(detect, "$(results.e2e.path)", out))
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("detection snippet failed for %v: %v\n%s", c.files, err, b)
		}
		if got, _ := os.ReadFile(out); string(got) != c.want {
			t.Errorf("repository with %v: e2e=%q, want %q", c.files, got, c.want)
		}
	}

	after := map[string]bool{}
	for _, a := range byName["sign"]["runAfter"].([]any) {
		after[a.(string)] = true
	}
	if !after["e2e"] || !after["test"] {
		t.Errorf("sign runs after %v; an image whose e2e or integration tests failed would still be signed", byName["sign"]["runAfter"])
	}

	// The e2e image's browsers match the version the templates pin.
	e2e := docs["Task/e2e-playwright"]
	step := dig(e2e, "spec", "steps").([]any)[0].(map[string]any)
	if step["image"] != "mcr.microsoft.com/playwright:v1.64.1-noble" {
		t.Errorf("e2e image %v does not match the templates' @playwright/test 1.64.1", step["image"])
	}

	// Integration tests: `mvn verify` against a kubedock that only this pod
	// can reach, with no Docker-in-Docker.
	ta := docs["Task/test-auto"]
	src, _ := yaml.Marshal(ta)
	if !strings.Contains(string(src), "-Dmaven.repo.local=/workspace/.m2 verify") {
		t.Error("test-auto runs `mvn test`: failsafe integration tests (*IT) never run")
	}
	side := dig(ta, "spec", "sidecars").([]any)[0].(map[string]any)
	args := map[string]bool{}
	for _, a := range side["args"].([]any) {
		args[a.(string)] = true
	}
	for _, want := range []string{"--listen-addr=127.0.0.1:2475", "--namespace=adhar-ci-sandbox", "--reverse-proxy", "--disable-dind"} {
		if !args[want] {
			t.Errorf("kubedock lacks %s", want)
		}
	}
	if args["--prune-start"] {
		t.Error("kubedock --prune-start deletes OTHER runs' containers in the shared sandbox")
	}

	// Its rights exist in the sandbox only.
	rb := docs["RoleBinding/kubedock-adhar-pipeline"]
	if dig(rb, "metadata", "namespace") != "adhar-ci-sandbox" || dig(rb, "roleRef", "kind") != "Role" {
		t.Error("kubedock's permissions are not confined to adhar-ci-sandbox")
	}
	if docs["NetworkPolicy/testcontainers-isolation"] == nil || docs["ResourceQuota/testcontainers"] == nil || docs["LimitRange/testcontainers-defaults"] == nil {
		t.Error("the sandbox lost its isolation, quota or default limits")
	}
}
