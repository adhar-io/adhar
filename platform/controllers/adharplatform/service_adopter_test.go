package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An ApplicationSet that is written but never applied is a feature that does
// not exist.
//
// A template's skeleton carries `.adhar/app.yaml`, an `Application`
// (platform.adhar.io) which the Crossplane control plane expands into the
// per-environment ArgoCD Applications. Nothing turns that file into an XR by
// itself: `adhar-services-appset.yaml` is the bridge, a Gitea SCM generator that
// finds every org repo carrying the descriptor and syncs its `.adhar/`
// directory. ArgoCD ApplicationSets can only emit ArgoCD Applications, never
// XRs, which is exactly why that indirection exists.
//
// It shipped in the stack and was never applied by the reconciler, so the whole
// path was inert. Live on 2026-10-11: a cluster with 2 organisations, 3 teams, 1
// project and 1 repository had ZERO Application XRs, and the applications that
// did exist were hand-wired ArgoCD Applications with no control-plane object
// behind them.
func TestTheServiceAdopterApplicationSetIsApplied(t *testing.T) {
	// The manifest must exist in the stack the reconciler reads from.
	path := filepath.Join(stackRoot(t), "adhar-services-appset.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	manifest := string(b)
	// The descriptor path is the contract between the template and the adopter;
	// if they disagree, no repo is ever adopted and nothing says why.
	if !strings.Contains(manifest, ".adhar/app.yaml") {
		t.Error("the service adopter does not filter on .adhar/app.yaml, which is the file a template's " +
			"skeleton carries; without that filter it either adopts every repo or none")
	}

	// And the reconciler must actually apply it.
	src, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	code := withoutComments(string(src))
	if !strings.Contains(code, `"adhar-services-appset.yaml"`) {
		t.Error("the reconciler never applies adhar-services-appset.yaml; the descriptor a scaffolded " +
			"repo carries is then never turned into an Application XR, so an app created from a " +
			"template never reaches the control plane")
	}
	if !strings.Contains(code, "Service adopter ApplicationSet") {
		t.Error("the service adopter is read but not handed to applyManifestNoOwner under its own name")
	}
	// A missing file must not fail the bootstrap, matching the workload set.
	i := strings.Index(code, `"adhar-services-appset.yaml"`)
	if i < 0 {
		t.FailNow()
	}
	if !strings.Contains(code[i:], "os.IsNotExist") {
		t.Error("a missing service adopter manifest aborts the reconcile; it must be tolerated the way " +
			"the workload ApplicationSet is, or an older stack directory cannot be bootstrapped")
	}
}
