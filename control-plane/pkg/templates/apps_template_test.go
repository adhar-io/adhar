package templates_test

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"adhar-io/adhar/control-plane/pkg/templates"
)

func TestAppsTemplateSuccess(t *testing.T) {
	tmplPath := filepath.Join("..", "..", "configuration", "compositions", "apps", "argocd-application.yaml")
	tmpl, err := templates.LoadTemplate(tmplPath)
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	data := map[string]any{
		"observed": map[string]any{
			"composite": map[string]any{
				"resource": map[string]any{
					"metadata": map[string]any{"name": "sample-app"},
					"spec": map[string]any{
						"parameters": map[string]any{
							"project":           "sample",
							"displayName":       "sample-app",
							"ignoreDifferences": []any{},
							"source": map[string]any{
								"repoURL":        "https://git.example.com/repos/app.git",
								"path":           "deploy/chart",
								"targetRevision": "main",
							},
							"destination": map[string]any{
								"server":    "https://kubernetes.default.svc",
								"namespace": "demo",
							},
							"syncPolicy": map[string]any{
								"automated": map[string]any{"prune": true, "selfHeal": true},
							},
						},
						"writeConnectionSecretToRef": map[string]any{
							"name":      "sample-app-conn",
							"namespace": "crossplane-system",
						},
						"providerConfigRef": map[string]any{"name": "default"},
					},
				},
			},
			"resources": map[string]any{
				"argocd-application": map[string]any{
					"resource": map[string]any{
						"status": map[string]any{
							"sync":   map[string]any{"status": "Synced", "revision": "abc123"},
							"health": map[string]any{"status": "Healthy"},
							"conditions": []any{
								map[string]any{"type": "Synced", "status": "True", "message": "app synced"},
							},
						},
					},
				},
			},
		},
	}

	rendered, err := templates.Render(tmpl, data)
	if err != nil {
		t.Fatalf("render template: %v", err)
	}

	var functionIO map[string]any
	if err := yaml.Unmarshal([]byte(rendered), &functionIO); err != nil {
		t.Fatalf("unmarshal rendered template: %v", err)
	}

	desired := functionIO["desired"].(map[string]any)

	connection := desired["connectionDetails"].(map[string]any)
	if got := connection["syncStatus"]; got != "Synced" {
		t.Errorf("syncStatus = %v, want Synced", got)
	}

	status := desired["status"].(map[string]any)
	appStatus := status["applicationStatus"].(map[string]any)
	if got := appStatus["health"]; got != "Healthy" {
		t.Errorf("health = %v, want Healthy", got)
	}

	tResources := desired["resources"].([]any)
	firstResource := tResources[0].(map[string]any)
	resource, ok := firstResource["resource"].(map[string]any)
	if !ok {
		t.Fatalf("resource manifest missing")
	}
	spec, ok := resource["spec"].(map[string]any)
	if !ok {
		t.Fatalf("resource spec missing")
	}
	forProvider, ok := spec["forProvider"].(map[string]any)
	if !ok {
		t.Fatalf("forProvider missing")
	}
	manifest, ok := forProvider["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing")
	}
	if manifest["kind"] != "Application" {
		t.Errorf("manifest kind = %v, want Application", manifest["kind"])
	}
}

func TestAppsTemplateMissingRepo(t *testing.T) {
	tmplPath := filepath.Join("..", "..", "configuration", "compositions", "apps", "argocd-application.yaml")
	tmpl, err := templates.LoadTemplate(tmplPath)
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	data := map[string]any{
		"observed": map[string]any{
			"composite": map[string]any{
				"resource": map[string]any{
					"metadata": map[string]any{"name": "missing-repo"},
					"spec": map[string]any{
						"parameters": map[string]any{
							"project":           "sample",
							"displayName":       "missing-repo",
							"ignoreDifferences": []any{},
							"source":            map[string]any{},
							"destination":       map[string]any{"server": "https://kubernetes.default.svc"},
							"syncPolicy":        map[string]any{},
						},
						"writeConnectionSecretToRef": map[string]any{},
						"providerConfigRef":          map[string]any{"name": "default"},
					},
				},
			},
			"resources": map[string]any{},
		},
		"resources": map[string]any{},
	}

	_, err = templates.Render(tmpl, data)
	if err == nil {
		t.Fatalf("expected error for missing repoURL")
	}

	if !strings.Contains(err.Error(), "source.repoURL must be provided") {
		t.Fatalf("unexpected error: %v", err)
	}
}
