package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The npm library release writes its OWN .npmrc over the repository's, so any
// setting the install depends on has to be in that heredoc. Without
// link-workspace-packages, pnpm 10 resolved a sibling dependency
// ("@adhar-ui/utils": "^0.1.1") from npm-hosted instead of the checkout, and
// on a platform that had published nothing yet every adhar-ui release failed
// with ERR_PNPM_FETCH_404 (2026-10-21).
func TestLibraryReleaseLinksWorkspaceSiblings(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "application/adhar-libraries/manifests/npm-release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "cat > .npmrc <<EOF\n")
	if start < 0 {
		t.Fatal("the publish step no longer writes .npmrc")
	}
	npmrc := src[start : start+strings.Index(src[start:], "\n        EOF\n")]
	if !strings.Contains(npmrc, "\n        link-workspace-packages=true") {
		t.Errorf("the release .npmrc does not set link-workspace-packages=true:\n%s", npmrc)
	}
	if strings.Index(src, "cat > .npmrc") > strings.Index(src, "pnpm install") {
		t.Error(".npmrc is written after pnpm install, so the install never sees it")
	}
}
