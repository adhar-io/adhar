package down

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The per-cluster kubeconfig `adhar up` writes lives at
// ~/.adhar/clusters/<name>/kubeconfig, which the "*-kubeconfig.yaml" glob can
// never match. A live Civo teardown therefore reported "No leftover kubeconfig
// files to remove" and left a kubeconfig aimed at a deleted control plane, so
// every later kubectl timed out and looked like a broken cluster.
//
// It must remove it for the clusters that were DELETED and leave every other
// cluster's alone: that tree holds the kubeconfigs of clusters still running.
func TestCleanupFilesRemovesOnlyTheDeletedClustersKubeconfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mk := func(cluster string) string {
		dir := filepath.Join(home, ".adhar", "clusters", cluster)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "kubeconfig")
		if err := os.WriteFile(path, []byte("apiVersion: v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	gone := mk("adhar")
	kept := mk("still-running")

	removed := cleanupFiles("adhar")

	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("the deleted cluster's kubeconfig survived: %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("another cluster's kubeconfig was removed: %v", err)
	}
	var reported bool
	for _, r := range removed {
		if r == gone {
			reported = true
		}
		if r == kept {
			t.Errorf("reported removing a kubeconfig it must not touch: %s", r)
		}
	}
	if !reported {
		t.Errorf("removal not reported to the operator; got %v", removed)
	}
}

// A correct cleanupFiles is useless if the teardown calls it with no clusters,
// which is how it shipped: the function grew the per-cluster path and both call
// sites still passed nothing. The unit test above cannot see that, because it
// calls the function directly — so check the wiring in the source.
func TestTeardownPassesTheDeletedClustersToCleanup(t *testing.T) {
	b, err := os.ReadFile("down.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "cleanupFiles()"); n != 0 {
		t.Errorf("%d call site(s) invoke cleanupFiles() with no clusters, so the per-cluster "+
			"kubeconfig is never removed and the teardown reports nothing to clean", n)
	}
	// Both teardown paths — the Kind one and the cloud/multi-env one — must hand
	// over what they deleted.
	for _, want := range []string{"cleanupFiles(clusterNames...)", "cleanupFiles(deletedClusters...)"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing call %s: that teardown path leaves its kubeconfig behind", want)
		}
	}
}
