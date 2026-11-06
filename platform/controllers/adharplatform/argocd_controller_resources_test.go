package adharplatform

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
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
	// carry the SAME request on the container, or the values are decoration.
	//
	// Read the way the controller reads them (applyManifestFull's decoder), not
	// by searching the text. From 2026-10-15 both files carried a hand-copied
	// `resources:` block right under the container's name, and a text search
	// found it and passed — but the chart already renders `resources:` further
	// down the same container, the decoder keeps the LAST of two equal keys, and
	// clusters kept running 1Gi (install.yaml) and no request at all
	// (install-ha.yaml) until the manifests were regenerated for Argo CD v3.6.1.
	for _, pair := range [][2]string{
		{"hack/argocd/values.yaml", "platform/controllers/adharplatform/resources/argocd/install.yaml"},
		{"hack/argocd/values-ha.yaml", "platform/controllers/adharplatform/resources/argocd/install-ha.yaml"},
	} {
		want := valuesControllerRequests(t, pair[0], read(pair[0]))
		got := installedControllerRequests(t, pair[1], read(pair[1]))
		for _, res := range []string{"cpu", "memory"} {
			if want[res] == "" {
				t.Errorf("%s declares no controller.resources.requests.%s", pair[0], res)
				continue
			}
			if got[res] != want[res] {
				t.Errorf("%s installs the application controller with requests.%s %q, but %s asks for %q; "+
					"the scheduler sees the decoded manifest, so a process measured at 2510Mi is "+
					"accounted as %q", pair[1], res, got[res], pair[0], want[res], got[res])
			}
		}
	}
}

// valuesControllerRequests returns controller.resources.requests from a Helm
// values file.
func valuesControllerRequests(t *testing.T, rel, body string) map[string]string {
	t.Helper()
	var values struct {
		Controller struct {
			Resources struct {
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"controller"`
	}
	if err := sigsyaml.Unmarshal([]byte(body), &values); err != nil {
		t.Fatalf("parsing %s: %v", rel, err)
	}
	return values.Controller.Resources.Requests
}

// installedControllerRequests decodes a generated Argo CD manifest exactly as
// applyManifestFull does and returns the application-controller container's
// resource requests as the API server would receive them.
func installedControllerRequests(t *testing.T, rel, body string) map[string]string {
	t.Helper()
	dec := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(body), 100)
	for {
		obj := &unstructured.Unstructured{}
		err := dec.Decode(obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decoding %s the way the controller does: %v", rel, err)
		}
		if obj.GetKind() != "StatefulSet" || obj.GetName() != "argo-cd-argocd-application-controller" {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		for _, c := range containers {
			container, _ := c.(map[string]interface{})
			if container["name"] != "application-controller" {
				continue
			}
			requests, _, _ := unstructured.NestedStringMap(container, "resources", "requests")
			return requests
		}
		t.Fatalf("%s: the application-controller StatefulSet has no application-controller container", rel)
	}
	t.Fatalf("%s no longer defines the argo-cd-argocd-application-controller StatefulSet", rel)
	return nil
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
