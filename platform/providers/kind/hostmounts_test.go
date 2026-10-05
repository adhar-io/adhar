package kind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every bind-mount source in the Kind config must exist before `kind create`:
// Podman refuses a missing one (exit 125, "statfs … no such file or
// directory") where Docker silently creates it. The E2E matrix ran green on
// Docker and failed in ten seconds on Podman for exactly this (2026-10-05).
func TestEnsureHostMountsCreatesEveryBindSource(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, ".adhar", "backup")
	pki := filepath.Join(dir, ".adhar", "pki")
	cfgFile := filepath.Join(dir, "registry", "config.json")
	raw := []byte(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraMounts:
  - hostPath: ` + backup + `
    containerPath: /backup
  - hostPath: ` + pki + `
    containerPath: /etc/adhar/pki
    readOnly: true
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ` + cfgFile + `
`)
	if err := ensureHostMounts(raw); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{backup, pki, filepath.Dir(cfgFile)} {
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			t.Errorf("%s must exist as a directory before the node is created: %v", d, err)
		}
	}
	if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
		t.Error("a FILE mount's source must not be created as a directory — only its parent")
	}
	// Idempotent on an existing tree.
	if err := ensureHostMounts(raw); err != nil {
		t.Fatal(err)
	}
}

// The template that ships must mount only paths this function will create:
// a relative `.adhar/backup` is resolved against the working directory, which
// is where `adhar up` runs — the same place the backup command writes.
func TestShippedKindTemplateMountsAreCreatable(t *testing.T) {
	raw, err := os.ReadFile("resources/kind.yaml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hostPath: .adhar/backup", "hostPath: {{ .PKIHostPath }}"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("kind.yaml.tmpl no longer mounts %q — update ensureHostMounts' doc and this test together", want)
		}
	}
}
