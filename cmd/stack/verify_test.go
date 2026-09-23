package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateHealthIsTheOnlyPass(t *testing.T) {
	cases := []struct {
		name                     string
		enabled, present         bool
		health, message          string
		wantStatus, wantContains string
	}{
		{"healthy", true, true, "Healthy", "", "verified", ""},
		{"disabled", false, true, "Degraded", "x", "unverified", ""},
		{"enabled but absent", true, false, "", "", "unverified", ""},
		{"progressing is not broken", true, true, "Progressing", "rolling", "unverified", ""},
		{"degraded", true, true, "Degraded", "Deployment \"x\" exceeded its progress deadline\nmore", "known-broken", "Degraded: Deployment"},
		{"missing", true, true, "Missing", "", "known-broken", "Missing"},
	}
	for _, c := range cases {
		got := Evaluate(c.enabled, c.present, c.health, c.message)
		if got.Status != c.wantStatus {
			t.Errorf("%s: status %q, want %q", c.name, got.Status, c.wantStatus)
		}
		if c.wantContains != "" && !strings.Contains(got.Reason, c.wantContains) {
			t.Errorf("%s: reason %q should contain %q", c.name, got.Reason, c.wantContains)
		}
		if strings.Contains(got.Reason, "\n") {
			t.Errorf("%s: reason must be one line, got %q", c.name, got.Reason)
		}
	}
}

const contractFixture = `# contract
apiVersion: marketplace.adhar.io/v1alpha1
kind: AdharPackage
name: thing
stability: beta   # because
resources:
  localSafe: true   # fits
keywords:
  - a
`

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "adhar-package.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWriteVerificationAppendsAndPreservesEverythingElse(t *testing.T) {
	p := writeFixture(t, contractFixture)
	changed, err := writeVerification(p, verification{Status: "verified", Profile: "production", VerifiedAt: "2026-09-23", PlatformVersion: "v0.1.22", KubernetesVersion: "v1.37.0"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	if !strings.HasPrefix(s, contractFixture) {
		t.Fatalf("existing content (comments included) must be untouched:\n%s", s)
	}
	for _, want := range []string{"verification:\n  status: verified", "profile: production", `verifiedAt: "2026-09-23"`, `kubernetesVersion: "v1.37.0"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if !strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\n\n") {
		t.Errorf("file should end with exactly one newline")
	}
}

func TestWriteVerificationReplacesOnlyTheBlock(t *testing.T) {
	body := contractFixture + "verification:\n  status: known-broken\n  reason: \"old\"\n\n# trailing comment\nhomepage: https://x\n"
	p := writeFixture(t, body)
	if _, err := writeVerification(p, verification{Status: "verified", Profile: "local", VerifiedAt: "2026-09-23"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	if strings.Contains(s, "known-broken") || strings.Contains(s, `"old"`) {
		t.Errorf("old block should be gone:\n%s", s)
	}
	if !strings.Contains(s, "\n\n# trailing comment\nhomepage: https://x\n") {
		t.Errorf("content after the block, and its spacing, must survive:\n%s", s)
	}
	if !strings.HasPrefix(s, contractFixture) {
		t.Errorf("content before the block must survive")
	}
}

func TestWriteVerificationIsIdempotent(t *testing.T) {
	p := writeFixture(t, contractFixture)
	v := verification{Status: "verified", Profile: "production", VerifiedAt: "2026-09-23"}
	if _, err := writeVerification(p, v); err != nil {
		t.Fatal(err)
	}
	changed, err := writeVerification(p, v)
	if err != nil || changed {
		t.Fatalf("second identical write: changed=%v err=%v", changed, err)
	}
}

func TestUnverifiedNeverDowngradesRecordedEvidence(t *testing.T) {
	p := writeFixture(t, contractFixture+"verification:\n  status: verified\n  profile: production\n")
	changed, err := writeVerification(p, verification{Status: "unverified"})
	if err != nil || changed {
		t.Fatalf("a disabled-today package must keep its evidence: changed=%v err=%v", changed, err)
	}
	// But a fresh contract with no evidence does get an explicit unverified.
	p2 := writeFixture(t, contractFixture)
	changed, _ = writeVerification(p2, verification{Status: "unverified"})
	got, _ := os.ReadFile(p2)
	if !changed || !strings.HasSuffix(string(got), "verification:\n  status: unverified\n") {
		t.Errorf("fresh contract should record unverified:\n%s", got)
	}
}

func TestReasonIsYAMLSafe(t *testing.T) {
	p := writeFixture(t, contractFixture)
	if _, err := writeVerification(p, verification{Status: "known-broken", Profile: "production", VerifiedAt: "2026-09-23", Reason: `Degraded: pod "x" said \ hi`}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), `reason: "Degraded: pod \"x\" said \\ hi"`) {
		t.Errorf("reason not quoted safely:\n%s", got)
	}
}

func TestContractPathWalksUpToThePackageRoot(t *testing.T) {
	stack := t.TempDir()
	pkg := filepath.Join(stack, "packages", "ai", "vllm")
	if err := os.MkdirAll(filepath.Join(pkg, "manifests", "cpu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "adhar-package.yaml"), []byte("name: vllm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	elements := []element{
		{Name: "vllm-cpu", ManifestPath: "ai/vllm/manifests/cpu"},
		{Name: "vllm", ManifestPath: "ai/vllm/manifests"},
		{Name: "orphan", ManifestPath: "ai/nothing/manifests"},
	}
	want := filepath.Join(pkg, "adhar-package.yaml")
	if got := contractPath(stack, elements, "vllm-cpu"); got != want {
		t.Errorf("sub-directory element: got %q want %q", got, want)
	}
	if got := contractPath(stack, elements, "vllm"); got != want {
		t.Errorf("package element: got %q want %q", got, want)
	}
	if got := contractPath(stack, elements, "orphan"); got != "" {
		t.Errorf("an element with no contract must not borrow one: got %q", got)
	}
}
