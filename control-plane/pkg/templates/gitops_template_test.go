package templates_test

import (
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"adhar-io/adhar/control-plane/pkg/templates"
)

func TestGitOpsTemplateStatus(t *testing.T) {
	tmplPath := filepath.Join("..", "..", "configuration", "compositions", "gitops", "argocd-project.yaml")
	tmpl, err := templates.LoadTemplate(tmplPath)
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	data := map[string]any{
		"observed": map[string]any{
			"composite": map[string]any{
				"resource": map[string]any{
					"metadata": map[string]any{"name": "sample-gitops"},
					"spec": map[string]any{
						"parameters": map[string]any{
							"project": map[string]any{
								"name":        "platform",
								"sourceRepos": []any{"https://git.example.com/platform"},
							},
							"applicationSets": []any{
								map[string]any{
									"name":       "apps",
									"generators": []any{map[string]any{"git": map[string]any{"repoURL": "https://git.example.com/apps"}}},
									"template":   map[string]any{"metadata": map[string]any{"name": "{{name}}"}},
								},
							},
						},
						"writeConnectionSecretToRef": map[string]any{},
						"providerConfigRef":          map[string]any{"name": "default"},
					},
				},
			},
			"resources": map[string]any{
				"argocd-project": map[string]any{
					"resource": map[string]any{
						"status": map[string]any{
							"conditions": []any{map[string]any{"type": "Synced", "status": "True"}},
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
	status := desired["status"].(map[string]any)

	if synced, _ := status["projectSynced"].(bool); !synced {
		t.Errorf("expected projectSynced true")
	}

	switch v := status["managedApplicationSets"].(type) {
	case int:
		if v != 1 {
			t.Errorf("expected managedApplicationSets 1, got %d", v)
		}
	case int64:
		if v != 1 {
			t.Errorf("expected managedApplicationSets 1, got %d", v)
		}
	default:
		t.Errorf("unexpected managedApplicationSets type %T", v)
	}
}
