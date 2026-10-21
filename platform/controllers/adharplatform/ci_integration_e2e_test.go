package adharplatform

import (
	"os"
	"path/filepath"
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
