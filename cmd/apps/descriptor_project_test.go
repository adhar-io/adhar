package apps

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An SPA-shaped template: no `project` parameter, and a skeleton that commits
// .adhar/app.yaml — the app's own Application, project `adhar` written in —
// when localisation is on, as react-spa-app, angular-spa-app and
// tanstack-start-fullstack-app do by default.
const descriptorTemplateYAML = `
apiVersion: scaffolder.backstage.io/v1beta3
kind: Template
metadata:
  name: react-spa-app
  tags: [react, frontend]
  labels: {adhar.io/dashboard-type: frontend}
spec:
  owner: group:platform-engineers
  parameters:
    - required: [name]
      properties:
        name: {type: string}
        withLocalisation: {type: boolean, default: true}
    - required: [repoUrl]
      properties:
        repoUrl: {type: string, ui:field: RepoUrlPicker}
  steps:
    - action: fetch:template
      input:
        url: ./skeleton
        values:
          name: ${{ parameters.name }}
          withLocalisation: ${{ parameters.withLocalisation }}
    - action: adhar:create-argocd-app
      input: {path: deploy}
`

func descriptorSkeleton(descriptor string) map[string]string {
	return map[string]string{
		"catalog-info.yaml":                  "metadata: {name: ${{ values.name }}}\n",
		"deploy/envs/dev/kustomization.yaml": "resources: []\n",
		".adhar/app.yaml":                    descriptor,
	}
}

// What the SPA templates write: the descriptor, or (localisation off) a
// comment saying why there is none.
const spaDescriptor = `{% if values.withLocalisation -%}
# Control-plane descriptor
apiVersion: platform.adhar.io/v1alpha1
kind: Application
metadata:
  name: ${{ values.name }}
  namespace: adhar-system
  labels:
    adhar.io/managed-by: adhar-console
    adhar.io/project: adhar
spec:
  parameters:
    project: adhar
    displayName: ${{ values.name }}
{%- else -%}
# Localisation is off for this app, so there is no control-plane descriptor here.
{%- endif %}
`

// Argo CD applies a repository's .adhar/app.yaml with self-heal, so an app
// the CLI registered in another project than the one written there was moved
// back by the first sync. That is refused before anything is created; a
// registration the descriptor agrees with, or no descriptor at all, goes ahead.
func TestARegistrationTheRepositorysDescriptorWouldOverturnIsRefused(t *testing.T) {
	second := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: ${{ values.name }}-settings}\n---\n" + spaDescriptor
	other := strings.Replace(spaDescriptor, "name: ${{ values.name }}\n  namespace", "name: ${{ values.name }}-docs\n  namespace", 1)
	defaulted := strings.Replace(spaDescriptor, "    project: adhar\n", "    project: default\n", 1)
	elsewhere := strings.Replace(spaDescriptor, "namespace: adhar-system", "namespace: dev", 1)
	for _, tc := range []struct {
		name       string
		project    string
		explicit   bool
		params     []string
		descriptor string
		refused    bool
		registered string
	}{
		{"--project payments over a descriptor in adhar", "payments", true, nil, spaDescriptor, true, ""},
		{"the descriptor after another document", "payments", true, nil, second, true, ""},
		{"the default project, which the descriptor names", "adhar", false, nil, spaDescriptor, false, "adhar"},
		{"--project adhar, said explicitly", "adhar", true, nil, spaDescriptor, false, "adhar"},
		{"a descriptor in `default`, the platform project", "adhar", false, nil, defaulted, false, "adhar"},
		{"localisation off: no descriptor", "payments", true, []string{"withLocalisation=false"}, spaDescriptor, false, "payments"},
		{"a descriptor for another Application", "payments", true, nil, other, false, "payments"},
		{"a descriptor for an Application in another namespace", "payments", true, nil, elsewhere, false, "payments"},
	} {
		withDeployFlags(t, tc.project, tc.params, "")
		gc, f := newTemplateGitea(t, "react-spa-app", descriptorTemplateYAML, descriptorSkeleton(tc.descriptor))
		dc, applies := applyRecorder(t, nil)
		_, _, project, err := scaffoldTemplateApplication(t.Context(), dc, gc, "https://gitea.example.io", "storefront", "react-spa-app", tc.explicit)
		if tc.refused {
			if err == nil || !strings.Contains(err.Error(), `commits .adhar/app.yaml, this same Application in project "adhar"`) ||
				!strings.Contains(err.Error(), `registering it in project "payments"`) {
				t.Errorf("%s: error %v, want the descriptor's project named", tc.name, err)
			}
			if len(f.writes) != 0 || len(*applies) != 0 {
				t.Errorf("%s: wrote %v to Gitea and applied %d Applications before refusing", tc.name, f.writes, len(*applies))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if project != tc.registered || len(*applies) != 1 {
			t.Errorf("%s: registered in %q with %d applies, want %q", tc.name, project, len(*applies), tc.registered)
			continue
		}
		var got map[string]any
		_ = json.Unmarshal((*applies)[0].GetPatch(), &got)
		if p, _, _ := unstructured.NestedString(got, "spec", "parameters", "project"); p != tc.registered {
			t.Errorf("%s: the Application says project %q, want %q", tc.name, p, tc.registered)
		}
	}
}
