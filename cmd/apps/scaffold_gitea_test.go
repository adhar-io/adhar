package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"adhar-io/adhar/globals"

	"code.gitea.io/sdk/gitea"
	"gopkg.in/yaml.v3"
)

// fakeTemplatesGitea serves a templates repository holding one template's
// skeleton, and records every write a scaffold makes.
type fakeTemplatesGitea struct {
	mu     sync.Mutex
	writes []string
	commit gitea.ChangeFilesOptions
}

func newFakeTemplatesGitea(t *testing.T, templateID string, skeleton map[string]string) (*gitea.Client, *fakeTemplatesGitea) {
	t.Helper()
	f := &fakeTemplatesGitea{}
	prefix := globals.GitOpsTemplatesPath + "/" + templateID + "/skeleton/"
	treePath := "/api/v1/repos/" + globals.GiteaPlatformOrg + "/" + globals.GitOpsRepoTemplates + "/git/trees/main"
	rawPath := "/api/v1/repos/" + globals.GiteaPlatformOrg + "/" + globals.GitOpsRepoTemplates + "/raw/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == treePath:
			var entries []map[string]string
			for p := range skeleton {
				entries = append(entries, map[string]string{"path": prefix + p, "type": "blob"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": "t", "truncated": false, "tree": entries})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, rawPath):
			content, ok := skeleton[strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, rawPath), prefix)]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(content))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/repos/"+globals.GiteaPlatformOrg+"/"):
			http.NotFound(w, r) // the application's repository does not exist yet
		default:
			f.mu.Lock()
			f.writes = append(f.writes, r.Method+" "+r.URL.Path)
			if strings.HasSuffix(r.URL.Path, "/contents") {
				_ = json.NewDecoder(r.Body).Decode(&f.commit)
			}
			f.mu.Unlock()
			if strings.HasSuffix(r.URL.Path, "/repos") {
				_ = json.NewEncoder(w).Encode(map[string]any{"name": "billing", "empty": true})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	gc, err := gitea.NewClient(srv.URL, gitea.SetGiteaVersion("1.22.0"))
	if err != nil {
		t.Fatal(err)
	}
	return gc, f
}

func testTemplate(t *testing.T) *backstageTemplate {
	t.Helper()
	var tpl backstageTemplate
	if err := yaml.Unmarshal([]byte(`
metadata: {name: go-web-service, description: A Go service}
spec:
  parameters: [{properties: {name: {type: string}, withDatabase: {type: boolean, default: false}}}]
  steps:
    - action: fetch:template
      input: {url: ./skeleton, values: {name: "${{ parameters.name }}", withDatabase: "${{ parameters.withDatabase }}"}}
    - action: adhar:create-argocd-app
      input: {path: deploy}
`), &tpl); err != nil {
		t.Fatal(err)
	}
	return &tpl
}

// The repository is the one thing a re-run cannot undo, so nothing is written
// until the whole skeleton has rendered. It used to be created first: a
// skeleton the renderer refused (springboot-web-service's compose.yaml, with
// the template's defaults) left an EMPTY repository behind, which the next
// attempt adopted only to fail the same way.
func TestARenderFailureCreatesNoRepository(t *testing.T) {
	gc, f := newFakeTemplatesGitea(t, "go-web-service", map[string]string{
		"README.md":   "# ${{ values.name }}\n",
		"compose.yml": "{%- if values.withDatabase in values.engines %}\nservices: {}\n{%- endif %}\n",
	})
	_, err := renderScaffold(context.Background(), gc, testTemplate(t), "https://gitea.example.io:443", "go-web-service", "billing", "dev", nil)
	if err == nil || !strings.Contains(err.Error(), "compose.yml") {
		t.Fatalf("render error = %v, want one naming compose.yml", err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("a failed render wrote to Gitea: %v", f.writes)
	}
}

func TestARenderedSkeletonIsCommittedOnceToANewRepository(t *testing.T) {
	gc, f := newFakeTemplatesGitea(t, "go-web-service", map[string]string{
		"README.md":                      "# ${{ values.name }}\n",
		"deploy/base/kustomization.yaml": "resources: []\n",
		"public/logo.png":                "\x89PNG ${{ values.name }}",
	})
	r, err := renderScaffold(context.Background(), gc, testTemplate(t), "https://gitea.example.io:443", "go-web-service", "billing", "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("rendering wrote to Gitea: %v", f.writes)
	}
	s, err := commitScaffold(gc, "https://gitea.example.io:443", "billing", r)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /api/v1/orgs/adhar/repos", "POST /api/v1/repos/adhar/billing/contents"}
	if strings.Join(f.writes, ",") != strings.Join(want, ",") {
		t.Fatalf("writes %v, want %v", f.writes, want)
	}
	if len(f.commit.Files) != 3 || f.commit.Message != "scaffold billing from go-web-service" || f.commit.Branch != "main" {
		t.Fatalf("commit: %d files, %q on %q", len(f.commit.Files), f.commit.Message, f.commit.Branch)
	}
	if s.CloneURL != "https://gitea.example.io/adhar/billing.git" || s.ManifestPath != "deploy" || s.TemplateName != "go-web-service" {
		t.Fatalf("scaffolded %+v", s)
	}
}
