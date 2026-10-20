package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Plane runs the platform's OWN build — every image, from the adhar-io/plane
// fork where its changes live, published as ghcr.io/adhar-io/plane-*.
//
// The backend carries the Keycloak login provider; the web, space and admin
// front ends carry the button for it. Mixing them is a live failure, not a
// style point: until 1.4.2-2 the fork backend ran under the STOCK frontend,
// which cannot draw a method it does not know, so the sign-in page could not
// show Keycloak and a Gitea-federation detour had to stand in for it.
//
// Two things can silently undo the pin, which is why this is a test and not a
// comment. install.yaml is generated from the upstream plane-ce chart, so a
// regeneration reinstates artifacts.plane.so unless generate-manifests.sh
// rewrites it; and the images appear in nine places across install.yaml and
// instance-setup.yaml, so a hand-edit easily misses one and leaves components
// on different builds — or on different TAGS, which is the same bug one step
// later.
func TestPlaneUsesThePlatformsOwnImages(t *testing.T) {
	const registry = "ghcr.io/adhar-io/plane-"
	dir := filepath.Join(stackPackagesDir(t), "application/plane")

	upstream := regexp.MustCompile(`artifacts\.plane\.so/makeplane/plane-`)
	ref := regexp.MustCompile(`image:\s*(\S*plane-(backend|frontend|admin|space|live):(\S+))`)

	seen := map[string]int{}
	tags := map[string]bool{}
	for _, name := range []string{"manifests/install.yaml", "manifests/instance-setup.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ref.FindAllStringSubmatch(string(b), -1) {
			seen[m[2]]++
			tags[m[3]] = true
			if !strings.HasPrefix(m[1], registry) {
				t.Errorf("%s uses %s for plane-%s; the platform's build is %s%s", name, m[1], m[2], registry, m[2])
			}
		}
		// Comments legitimately name the upstream image while explaining what
		// it lacks, so only `image:` lines are checked.
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "image:") && upstream.MatchString(line) {
				t.Errorf("%s still pulls from artifacts.plane.so: %s", name, strings.TrimSpace(line))
			}
		}
	}
	if seen["backend"] < 5 {
		t.Errorf("found %d backend image references, expected at least 5 (api, worker, beat-worker, migrator, instance setup) — "+
			"a missed one leaves components on different builds", seen["backend"])
	}
	for _, img := range []string{"frontend", "admin", "space", "live"} {
		if seen[img] < 1 {
			t.Errorf("no image reference for plane-%s; the front ends must be the fork's build too, or the "+
				"sign-in page cannot render the Keycloak login the backend offers", img)
		}
	}
	if len(tags) != 1 {
		t.Errorf("Plane images are pinned to %d different tags %v; the fork publishes all six under one tag "+
			"and the frontend's auth bundle must match the backend's instance payload", len(tags), keys(tags))
	}

	// And the generator must reproduce it, or the next regeneration reverts.
	gen, err := os.ReadFile(filepath.Join(dir, "generate-manifests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{registry, `"frontend"`, `"admin"`, `"space"`, `"live"`} {
		if !strings.Contains(string(gen), want) {
			t.Errorf("generate-manifests.sh does not rewrite %s to the fork build; regenerating would silently "+
				"restore the upstream image", want)
		}
	}
}
