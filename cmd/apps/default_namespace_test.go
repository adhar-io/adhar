package apps

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// A custom application never lands in `default`: every command that falls back
// to a namespace falls back to the dev environment, the head of the promotion
// chain. `default` was the fallback in seven commands (2026-10-17).
func TestNoAppsCommandFallsBackToTheDefaultNamespace(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	bad := regexp.MustCompile(`= "default"\n`)
	for _, f := range files {
		if filepath.Base(f) == "default_namespace_test.go" || len(f) > 8 && f[len(f)-8:] == "_test.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bad.Match(b) {
			t.Errorf("%s still falls back to the `default` namespace; use globals.DefaultEnvironment()", f)
		}
	}
}
