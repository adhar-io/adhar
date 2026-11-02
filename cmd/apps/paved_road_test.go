package apps

import (
	"testing"

	"adhar-io/adhar/globals"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A template scaffold must register the same Application the Console's Create
// wizard does (apps/console/app/server/control-plane.ts applicationManifest):
// in adhar-system, with no composition selector, so the XRD default
// application-multienv composes dev/test/prod and the Kargo promotion chain.
// Until 2026-11-03 the CLI sent namespace dev with selector
// feature=application/provider=local and every CLI-scaffolded app got
// application-local-argocd: one environment, no Kargo.
func TestTemplateScaffoldRegistersThePavedRoadApplication(t *testing.T) {
	cases := []struct {
		name, dashboard, workload, wantType string
	}{
		{"qua-service", "backend-jvm", "", ""},
		{"reactapp", "frontend", "", "web"},
		{"tanstack-app", "fullstack", "", "web"},
		{"deno-func", "backend-generic", "function", "function"},
		{"odd-labels", "not-a-dashboard", "", ""},
	}
	for _, c := range cases {
		s := &scaffoldedRepo{
			CloneURL:       "https://gitea.platform.adhar.io:443/adhar/" + c.name + ".git",
			ManifestPath:   "deploy",
			TemplateLabels: map[string]string{"adhar.io/dashboard-type": c.dashboard, "adhar.io/workload": c.workload},
		}
		app := pavedRoadApplication(c.name, "", s, "", "")

		if app.GetKind() != "Application" || app.GetAPIVersion() != "platform.adhar.io/v1alpha1" {
			t.Errorf("%s: kind %s %s", c.name, app.GetAPIVersion(), app.GetKind())
		}
		if app.GetNamespace() != globals.AdharSystemNamespace {
			t.Errorf("%s: namespace %q, want %q — the Console registers Applications there", c.name, app.GetNamespace(), globals.AdharSystemNamespace)
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(app.Object, "spec", "crossplane"); found {
			t.Errorf("%s: spec.crossplane is set; a selector overrides the XRD default (application-multienv)", c.name)
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(app.Object, "spec", "compositionSelector"); found {
			t.Errorf("%s: spec.compositionSelector is set; a selector overrides the XRD default (application-multienv)", c.name)
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(app.Object, "spec", "parameters", "destination"); found {
			t.Errorf("%s: spec.parameters.destination is set; the project's environments decide where the app runs", c.name)
		}
		labels := app.GetLabels()
		for k, v := range map[string]string{"adhar.io/project": "adhar", "adhar.io/application": c.name, "adhar.io/scaffolded": "true"} {
			if labels[k] != v {
				t.Errorf("%s: label %s = %q, want %q", c.name, k, labels[k], v)
			}
		}
		if c.dashboard == "not-a-dashboard" {
			if _, ok := labels["adhar.io/dashboard-type"]; ok {
				t.Errorf("%s: an unknown dashboard type was passed on", c.name)
			}
		} else if labels["adhar.io/dashboard-type"] != c.dashboard {
			t.Errorf("%s: dashboard-type label %q, want %q", c.name, labels["adhar.io/dashboard-type"], c.dashboard)
		}
		gotType, _, _ := unstructured.NestedString(app.Object, "spec", "parameters", "type")
		if gotType != c.wantType {
			t.Errorf("%s: spec.parameters.type %q, want %q", c.name, gotType, c.wantType)
		}
		if c.workload == "function" && labels["adhar.io/workload"] != "function" {
			t.Errorf("%s: a function template's app lost its adhar.io/workload label", c.name)
		}
		project, _, _ := unstructured.NestedString(app.Object, "spec", "parameters", "project")
		repoURL, _, _ := unstructured.NestedString(app.Object, "spec", "parameters", "source", "repoURL")
		path, _, _ := unstructured.NestedString(app.Object, "spec", "parameters", "source", "path")
		rev, _, _ := unstructured.NestedString(app.Object, "spec", "parameters", "source", "targetRevision")
		if project != "adhar" || path != "deploy" || rev != "main" {
			t.Errorf("%s: project/path/revision = %q/%q/%q", c.name, project, path, rev)
		}
		if want := "https://gitea.platform.adhar.io/adhar/" + c.name + ".git"; repoURL != want {
			t.Errorf("%s: repoURL %q, want %q (the Console's public clone URL)", c.name, repoURL, want)
		}
	}
}

func TestPublicCloneURLDropsOnlyTheDefaultPort(t *testing.T) {
	for in, want := range map[string]string{
		"https://gitea.example.io:443/adhar/a.git":  "https://gitea.example.io/adhar/a.git",
		"http://gitea.example.io:80/adhar/a.git":    "http://gitea.example.io/adhar/a.git",
		"https://gitea.example.io:8443/adhar/a.git": "https://gitea.example.io:8443/adhar/a.git",
		"https://gitea.example.io/adhar/a.git":      "https://gitea.example.io/adhar/a.git",
	} {
		if got := publicCloneURL(in); got != want {
			t.Errorf("publicCloneURL(%q) = %q, want %q", in, got, want)
		}
	}
}
