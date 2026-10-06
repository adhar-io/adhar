package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Plane's backend runs the platform's OWN build, from the adhar-io/plane fork
// where its changes live, published as ghcr.io/adhar-io/plane-backend.
//
// Two things can silently undo that, which is why this is a test and not a
// comment. install.yaml is generated from the upstream plane-ce chart, so a
// regeneration reinstates artifacts.plane.so unless generate-manifests.sh
// rewrites it; and the backend image appears in four places in that file plus
// once in instance-setup.yaml, so a hand-edit easily misses one and leaves the
// workers on a different build from the API.
func TestPlaneBackendUsesThePlatformsOwnImage(t *testing.T) {
	const want = "ghcr.io/adhar-io/plane-backend"
	dir := filepath.Join(stackPackagesDir(t), "application/plane")

	upstream := regexp.MustCompile(`artifacts\.plane\.so/makeplane/plane-backend:`)
	ref := regexp.MustCompile(`image:\s*(\S*plane-backend:\S+)`)

	seen := 0
	for _, name := range []string{"manifests/install.yaml", "manifests/instance-setup.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ref.FindAllStringSubmatch(string(b), -1) {
			seen++
			if !strings.HasPrefix(m[1], want+":") {
				t.Errorf("%s uses %s for the backend; the platform's build is %s", name, m[1], want)
			}
		}
		// The comment in instance-setup.yaml legitimately names the upstream
		// image while explaining what it does not contain, so only `image:`
		// lines are checked above; this catches an actual image reference.
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "image:") && upstream.MatchString(line) {
				t.Errorf("%s still pulls the backend from artifacts.plane.so: %s", name, strings.TrimSpace(line))
			}
		}
	}
	if seen < 5 {
		t.Errorf("found %d backend image references, expected at least 5 (api, worker, beat-worker, migrator, instance setup) — "+
			"a missed one leaves components on different builds", seen)
	}

	// And the generator must reproduce it, or the next regeneration reverts.
	gen, err := os.ReadFile(filepath.Join(dir, "generate-manifests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gen), want) {
		t.Errorf("generate-manifests.sh does not rewrite the backend image to %s; regenerating would silently "+
			"restore the upstream one", want)
	}
}
