package adharplatform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedFromBundleScriptCommitsPackedTreeOnTopOfHistory(t *testing.T) {
	s := seedFromBundleScript("/tmp/packages-working", "/tmp/packages.bundle", "packages")
	for _, want := range []string{
		"cd /tmp/packages-working", "git fetch -q /tmp/packages.bundle main", "git read-tree --reset FETCH_HEAD",
		`echo "NO_CHANGES"`, `commit -q -m "Update: Add packages content"`, `user.name="Adhar Platform"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("bundle seed script lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "git add") || strings.Contains(s, "cp -a") {
		t.Error("the bundle path must not re-add or copy files (that is what recompresses objects in the pod)")
	}
}

func TestSeedFromTreeScriptIsTheInPodFallback(t *testing.T) {
	s := seedFromTreeScript("/tmp/environments-working", "/tmp/environments-staging", "environments")
	for _, want := range []string{"cp -a /tmp/environments-staging/. .", "git add -A", `echo "NO_CHANGES"`, `commit -q -m "Update: Add environments content"`, "grep -v '^.git$'"} {
		if !strings.Contains(s, want) {
			t.Errorf("tree seed script lacks %q:\n%s", want, s)
		}
	}
}

func TestBuildStackBundlePacksTheTreeWithHostGit(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("host git not available")
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "core", "argocd"), 0o755); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("apiVersion: v1\nkind: ConfigMap\ndata:\n  k: v\n---\n", 5000)
	for _, f := range []string{"core/argocd/install.yaml", "core/argocd/adhar-package.yaml", "README.md"} {
		if err := os.WriteFile(filepath.Join(src, f), []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bundle, cleanup, err := buildStackBundle(context.Background(), src, "packages")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
		t.Error("the source tree must not be turned into a repository")
	}
	info, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= int64(len(big)) {
		t.Errorf("bundle (%d bytes) must be delta/zlib packed, smaller than one %d-byte file", info.Size(), len(big))
	}
	out, err := exec.Command(gitBin, "bundle", "list-heads", bundle).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "refs/heads/main") {
		t.Errorf("bundle must carry refs/heads/main: %v %s", err, out)
	}
	// The bundle round-trips: cloning it yields the same files.
	clone := t.TempDir()
	if out, err := exec.Command(gitBin, "clone", "-q", "-b", "main", bundle, filepath.Join(clone, "r")).CombinedOutput(); err != nil {
		t.Fatalf("clone from bundle: %v %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(clone, "r", "core", "argocd", "install.yaml"))
	if err != nil || string(got) != big {
		t.Errorf("cloned content differs: %v", err)
	}
}
