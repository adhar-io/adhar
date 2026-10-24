package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var pkgs = filepath.Join("..", "..", "stack", "packages")

// Kyverno ignored every PolicyException (`--enablePolicyException=false`, the
// chart default) AND the exceptions sat in a manifests/exceptions/ subdirectory
// the ApplicationSet never syncs (it does not recurse). Either alone left the
// node agents that need host access by design — Cilium, Falco, Tetragon,
// node-exporter, the CSI drivers — reported as policy failures (2026-10-24).
func TestPolicyExceptionsAreHonouredAndShipped(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pkgs, "security", "kyverno", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "--enablePolicyException=false") || strings.Count(s, "--enablePolicyException=true") < 3 {
		t.Error("every Kyverno controller must run with --enablePolicyException=true")
	}
	if strings.Count(s, "--exceptionNamespace=adhar-system") < 3 {
		t.Error("exceptions must be honoured only from adhar-system, or a team could exempt its own workloads")
	}

	dir := filepath.Join(pkgs, "security", "adhar-kyverno-policies", "manifests")
	entries, _ := os.ReadDir(dir)
	var exceptions int
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("%s/%s is a subdirectory; the ApplicationSet does not recurse, so nothing in it ever reaches a cluster", dir, e.Name())
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		for _, doc := range strings.Split(string(b), "\n---") {
			if !strings.Contains(doc, "kind: PolicyException") {
				continue
			}
			exceptions++
			if !strings.Contains(doc, "apiVersion: kyverno.io/v2\n") {
				t.Errorf("%s: a PolicyException not on kyverno.io/v2", e.Name())
			}
			if !strings.Contains(doc, "namespace: adhar-system") {
				t.Errorf("%s: a PolicyException outside adhar-system is ignored", e.Name())
			}
		}
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "exceptions-platform-agents.yaml"))
	for _, agent := range []string{"cilium", "falco", "tetragon", "node-exporter", "kubescape-node-agent",
		"harbor-node-registry-trust", "oidc-loopback-proxy", "opensearch", "cloud-integration"} {
		if !strings.Contains(string(agents), "name: platform-agent-"+agent+"\n") {
			t.Errorf("no PolicyException for the %s agent", agent)
		}
	}
	if exceptions < 10 {
		t.Errorf("found %d PolicyExceptions; expected the platform agents plus the system ones", exceptions)
	}
}

// The plane policy predated the planes the platform itself stamps: `workload`
// on every team namespace (the hierarchy compositions) and `ci` on the build
// sandbox. And Kargo's namespaces carried no plane at all.
func TestEveryPlatformNamespaceHasAnAcceptedPlane(t *testing.T) {
	gov, _ := os.ReadFile(filepath.Join(pkgs, "security", "adhar-kyverno-policies", "manifests", "namespace-governance.yaml"))
	m := regexp.MustCompile(`adhar\.io/plane: "([^"]+)"`).FindSubmatch(gov)
	if m == nil {
		t.Fatal("namespace-governance no longer pins adhar.io/plane values")
	}
	allowed := map[string]bool{}
	for _, v := range strings.Split(string(m[1]), "|") {
		allowed[strings.TrimSpace(v)] = true
	}
	for _, plane := range []string{"control", "data", "workload", "ci", "preview"} {
		if !allowed[plane] {
			t.Errorf("the plane policy rejects %q, which the platform stamps on its own namespaces", plane)
		}
	}
	kargo, _ := os.ReadFile(filepath.Join(pkgs, "application", "kargo", "manifests", "install.yaml"))
	for _, ns := range []string{"kargo-cluster-secrets", "kargo-shared-resources", "kargo-system-resources"} {
		if !regexp.MustCompile(`name: "?` + ns + `"?\n  labels:\n(    .+\n)*?    adhar\.io/plane: control\n`).Match(kargo) {
			t.Errorf("Kargo namespace %s has no adhar.io/plane", ns)
		}
	}
	pipe, _ := os.ReadFile(filepath.Join(pkgs, "application", "kargo", "manifests", "promotion-pipeline.yaml"))
	if !strings.Contains(string(pipe), "adhar.io/plane: control") {
		t.Error("adhar-environments has no adhar.io/plane")
	}
}
