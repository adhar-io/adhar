package adharplatform

import (
	"strings"
	"testing"
)

func TestDropManifestDocument(t *testing.T) {
	manifest := []byte(`# header
apiVersion: v1
kind: ConfigMap
metadata:
  name: keep-me
data: {a: b}
---
apiVersion: v1
kind: Secret
metadata:
  name: gitea-credential
  namespace: adhar-system
stringData:
  password: bootstrap
---
apiVersion: batch/v1
kind: Job
metadata:
  name: gitea-token-gen
`)
	out := string(dropManifestDocument(manifest, "Secret", "gitea-credential"))
	if strings.Contains(out, "gitea-credential") || strings.Contains(out, "password: bootstrap") {
		t.Fatalf("secret document should have been dropped:\n%s", out)
	}
	for _, want := range []string{"name: keep-me", "data: {a: b}", "name: gitea-token-gen", "# header"} {
		if !strings.Contains(out, want) {
			t.Errorf("other documents must survive intact, missing %q:\n%s", want, out)
		}
	}
	if got := string(dropManifestDocument(manifest, "Secret", "other")); got != string(manifest) {
		t.Errorf("a non-matching name must leave the manifest unchanged")
	}
}
