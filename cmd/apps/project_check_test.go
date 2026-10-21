package apps

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func appProject(namespaces ...string) *unstructured.Unstructured {
	dests := make([]interface{}, 0, len(namespaces))
	for _, ns := range namespaces {
		dests = append(dests, map[string]interface{}{"server": "https://kubernetes.default.svc", "namespace": ns})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"destinations": dests},
	}}
}

func TestProjectAllowsNamespace(t *testing.T) {
	// The platform's `adhar` project admits only its own namespaces.
	adhar := appProject("adhar", "dev", "test", "prod")
	for _, ns := range []string{"adhar", "dev", "test", "prod"} {
		if ok, _ := projectAllowsNamespace(adhar, ns); !ok {
			t.Errorf("adhar project should allow %q", ns)
		}
	}
	ok, patterns := projectAllowsNamespace(adhar, "orders")
	if ok {
		t.Fatal("adhar project must not allow a stand-alone namespace like orders")
	}
	if len(patterns) != 4 {
		t.Fatalf("expected the 4 allowed patterns for the error message, got %v", patterns)
	}

	// Destinations are globs, as in Argo CD.
	if ok, _ := projectAllowsNamespace(appProject("team-*"), "team-payments"); !ok {
		t.Error("team-* should allow team-payments")
	}
	if ok, _ := projectAllowsNamespace(appProject("*"), "anything"); !ok {
		t.Error("* (the default project) should allow any namespace")
	}

	// A project with no destinations allows nothing.
	if ok, _ := projectAllowsNamespace(appProject(), "dev"); ok {
		t.Error("a project with no destinations must not allow dev")
	}
}
