package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The console runs a pinned release, not a floating tag (2026-10-24). A
// floating tag made the running version unknowable from Git and upgraded the
// console on whatever pod restart happened to pull.
func TestTheConsoleImageIsPinnedToARelease(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "core", "adhar-console", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`image: ghcr\.io/adhar-io/adhar-console:(\S+)`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no adhar-console image found")
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).Match(m[1]) {
		t.Errorf("the console image tag is %q; pin a released version (e.g. 0.1.156, no v prefix)", m[1])
	}
}
