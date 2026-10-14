package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The application controller must declare memory, in the file that INSTALLS it.
//
// `hack/argocd/values.yaml` carried `controller.resources.requests` of
// cpu 500m / memory 1Gi, with a comment explaining why. The GENERATED
// StatefulSet carried no resources at all, so the live pod ran with
// `resources: {}` — measured on 2026-10-13 using 2510Mi while the scheduler
// accounted for nothing.
//
// That matters beyond this one pod. The scheduler places by requests, so a
// process using 2.5 GiB that declares nothing makes every node look emptier
// than it is; nodes were packed accordingly, ran out of real memory, and
// starved the kubelet and containerd until they went NotReady. Cluster-wide
// the requests understated actual use by 34.6 GiB.
//
// This is the same drift class as TestArgoCDReconciliationMatchesItsGenerator
// Values: a correct value in the generator that never reached the manifest.
func TestTheArgoCDControllerDeclaresItsMemoryInBothPlaces(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		b, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			t.Fatalf("reading %s: %v", rel, rerr)
		}
		return string(b)
	}

	// The generator values must declare a real request, not `{}`.
	memory := regexp.MustCompile(`(?m)^\s+memory:\s*(\S+)`)
	for _, rel := range []string{"hack/argocd/values.yaml", "hack/argocd/values-ha.yaml"} {
		body := read(rel)
		ctrl := controllerBlock(t, body)
		if strings.Contains(ctrl, "resources: {}") {
			t.Errorf("%s leaves controller.resources empty; the application controller is the "+
				"heaviest process on the cluster and must declare what it uses", rel)
			continue
		}
		if !memory.MatchString(ctrl) {
			t.Errorf("%s declares no controller memory request", rel)
		}
	}

	// And the generated manifests — the files that actually install — must
	// carry it on the container, or the values are decoration.
	for _, rel := range []string{
		"platform/controllers/adharplatform/resources/argocd/install.yaml",
		"platform/controllers/adharplatform/resources/argocd/install-ha.yaml",
	} {
		body := read(rel)
		i := strings.Index(body, "name: application-controller\n")
		if i < 0 {
			t.Fatalf("%s no longer defines the application-controller container", rel)
		}
		// Look only at the container, not the RBAC `resources:` lists elsewhere.
		win := body[i:min(i+1200, len(body))]
		if !strings.Contains(win, "resources:") || !strings.Contains(win, "memory:") {
			t.Errorf("%s installs the application controller with no memory request, so the "+
				"scheduler accounts nothing for a process measured at 2510Mi", rel)
		}
	}
}

// controllerBlock returns the top-level `controller:` mapping only.
func controllerBlock(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "\ncontroller:\n")
	if i < 0 {
		t.Fatal("no top-level controller: block")
	}
	rest := body[i+1:]
	// Ends at the next top-level key (a letter in column 0).
	next := regexp.MustCompile(`(?m)^[a-zA-Z]`).FindStringIndex(rest[len("controller:\n"):])
	if next == nil {
		return rest
	}
	return rest[:len("controller:\n")+next[0]]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
