package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// valkey-operator v0.0.61 gives a Valkey with no storage a default 8Gi claim,
// then fails every later reconcile on "storage has been removed" before it
// assigns hash slots — so a Cache composed without storage runs, answers PING
// and rejects every command with CLUSTERDOWN. The composition must state the
// storage, and state exactly the operator's default so live caches reconcile.
func TestLocalValkeyCacheDeclaresItsStorage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/cache/local-valkey.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i := strings.Index(s, "kind: Valkey")
	if i < 0 {
		t.Fatal("local-valkey composes no Valkey")
	}
	body := s[i:]
	for _, want := range []string{"storage:\n", "accessModes: [ReadWriteOnce]", "storage: 8Gi", "volumeMode: Filesystem"} {
		if !strings.Contains(body, want) {
			t.Errorf("the composed Valkey must declare its storage (missing %q)", want)
		}
	}
}
