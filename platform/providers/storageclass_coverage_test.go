package provider

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every StorageClass a package NAMES must be created by every cloud provider.
//
// A package manifest cannot know which cloud it will land on, so when it asks
// for `adhar-block` that name has to mean something everywhere. DigitalOcean was
// the one provider that never created it: it demoted `do-block-storage` and left
// a comment telling applications to name that instead, which no package does.
//
// The failure is three hops from the cause, which is why it survived a live
// bring-up: the nexus claim stays Pending, so the nexus pod stays Pending, so its
// PostSync hook never runs, so `nexus-credentials` is never published, and the
// adhar-kit and adhar-ui release pipelines then fail at build/publish with
// CreateContainerConfigError — which looks like a broken pipeline, not missing
// storage (live 2026-10-12).
//
// Read from the stack rather than hardcoded, so a package that starts naming a
// new class fails this test instead of failing silently on one cloud.
func TestEveryStorageClassThePackagesNameIsCreatedByEveryCloud(t *testing.T) {
	stack := filepath.Join("..", "stack", "packages")
	if _, err := os.Stat(stack); err != nil {
		t.Skipf("stack packages not present: %v", err)
	}

	// Classes the platform itself provisions; a package naming a cloud-native
	// class directly (do-block-storage) is out of scope for this invariant.
	platformClasses := map[string]bool{}
	nameRE := regexp.MustCompile(`storageClassName:\s*"?([A-Za-z0-9][-A-Za-z0-9.]*)"?`)
	err := filepath.Walk(stack, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range nameRE.FindAllStringSubmatch(string(b), -1) {
			if n := m[1]; strings.HasPrefix(n, "adhar-") {
				platformClasses[n] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the stack: %v", err)
	}
	if len(platformClasses) == 0 {
		t.Fatal("no adhar-* storageClassName found in the stack; this guard is checking nothing")
	}

	// Each cloud's integration source must create every one of them.
	clouds := map[string]string{
		"aws":          filepath.Join("aws", "cloud_integration.go"),
		"azure":        filepath.Join("azure", "cloud_integration.go"),
		"gcp":          filepath.Join("gcp", "cloud_integration.go"),
		"digitalocean": filepath.Join("digitalocean", "compute.go"),
	}

	// Classes the platform CONTROLLER publishes on every cluster (each cloud and
	// Kind alike) need no per-cloud creation; the guard instead checks that the
	// controller really reconciles them on every pass.
	controllerPublished := map[string][2]string{
		"adhar-ci": {"reconcileCIStorageClass", filepath.Join("..", "controllers", "adharplatform", "ci_storage.go")},
	}
	for class, where := range controllerPublished {
		if !platformClasses[class] {
			continue
		}
		delete(platformClasses, class)
		impl, err1 := os.ReadFile(where[1])
		loop, err2 := os.ReadFile(filepath.Join("..", "controllers", "adharplatform", "controller.go"))
		if err1 != nil || err2 != nil || !strings.Contains(string(impl), "func (r *AdharPlatformReconciler) "+where[0]+"(") ||
			!strings.Contains(string(loop), "r."+where[0]+"(ctx)") {
			t.Errorf("a package names %q, which only the controller publishes, but the controller no longer reconciles it (%s)", class, where[0])
		}
	}
	want := make([]string, 0, len(platformClasses))
	for n := range platformClasses {
		want = append(want, n)
	}
	sort.Strings(want)

	for cloud, rel := range clouds {
		b, readErr := os.ReadFile(rel)
		if readErr != nil {
			t.Errorf("%s: %v", cloud, readErr)
			continue
		}
		src := string(b)
		for _, class := range want {
			// The default node-local class is created through a shared helper
			// that takes the name from globals, so match either the literal or
			// that helper.
			if class == "adhar-local" && strings.Contains(src, "StepNodeLocalStorageClass") {
				continue
			}
			if !strings.Contains(src, `"`+class+`"`) {
				t.Errorf("%s never creates StorageClass %q, but a package names it; every claim against "+
					"it stays Pending for ever on that cloud, and the first visible symptom is usually "+
					"somewhere else entirely", cloud, class)
			}
		}
	}
	t.Logf("storage classes the packages name: %v", want)
}
