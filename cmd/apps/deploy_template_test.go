package apps

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"adhar-io/adhar/globals"

	"code.gitea.io/sdk/gitea"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// A platform Gitea holding one template (its template.yaml and skeleton) and
// no application repositories, recording every write.
type templateGitea struct {
	mu      sync.Mutex
	writes  []string
	created map[string]any
	commit  gitea.ChangeFilesOptions
}

func newTemplateGitea(t *testing.T, id, templateYAML string, skeleton map[string]string) (*gitea.Client, *templateGitea) {
	t.Helper()
	f := &templateGitea{}
	repo := "/api/v1/repos/" + globals.GiteaPlatformOrg + "/" + globals.GitOpsRepoTemplates
	dir := globals.GitOpsTemplatesPath + "/" + id + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == repo+"/git/trees/main":
			entries := []map[string]string{{"path": dir + "template.yaml", "type": "blob"}}
			for p := range skeleton {
				entries = append(entries, map[string]string{"path": dir + "skeleton/" + p, "type": "blob"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": "t", "truncated": false, "tree": entries})
		case r.Method == http.MethodGet && r.URL.Path == repo+"/raw/"+dir+"template.yaml":
			_, _ = w.Write([]byte(templateYAML))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, repo+"/raw/"+dir+"skeleton/"):
			content, ok := skeleton[strings.TrimPrefix(r.URL.Path, repo+"/raw/"+dir+"skeleton/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(content))
		case r.Method == http.MethodGet:
			http.NotFound(w, r) // no application repository exists yet
		default:
			f.mu.Lock()
			f.writes = append(f.writes, r.Method+" "+r.URL.Path)
			if strings.HasSuffix(r.URL.Path, "/contents") {
				_ = json.NewDecoder(r.Body).Decode(&f.commit)
			}
			f.mu.Unlock()
			if strings.HasSuffix(r.URL.Path, "/repos") {
				f.mu.Lock()
				_ = json.NewDecoder(r.Body).Decode(&f.created)
				f.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"name": f.created["name"], "empty": true})
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

func (f *templateGitea) file(path string) string {
	for _, op := range f.commit.Files {
		if op.Path == path {
			b, _ := base64.StdEncoding.DecodeString(op.Content)
			return string(b)
		}
	}
	return ""
}

// The cluster the CLI registers into: Project `payments` exists, and every
// server-side apply of an Application is recorded (and may be refused).
func applyRecorder(t *testing.T, refuse func(obj map[string]any) error) (*dynamicfake.FakeDynamicClient, *[]k8stesting.PatchActionImpl) {
	t.Helper()
	dc := fakeCluster(&unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Project",
		"metadata": map[string]interface{}{"name": "payments", "namespace": "adhar-system"},
	}})
	var applies []k8stesting.PatchActionImpl
	dc.PrependReactor("patch", "applications", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p := a.(k8stesting.PatchActionImpl)
		applies = append(applies, p)
		var obj map[string]any
		if err := json.Unmarshal(p.GetPatch(), &obj); err != nil {
			return true, nil, err
		}
		if refuse != nil {
			if err := refuse(obj); err != nil {
				return true, nil, err
			}
		}
		return true, &unstructured.Unstructured{Object: obj}, nil
	})
	return dc, &applies
}

// The deploy flags a template scaffold reads, set for one test.
func withDeployFlags(t *testing.T, project string, params []string, path string) {
	t.Helper()
	saved := []any{projectFlag, paramFlags, sourcePathFlag, envFlag}
	projectFlag, paramFlags, sourcePathFlag, envFlag = project, params, path, ""
	t.Cleanup(func() {
		projectFlag, paramFlags, sourcePathFlag, envFlag = saved[0].(string), saved[1].([]string), saved[2].(string), saved[3].(string)
	})
}

const serviceTemplateYAML = `
apiVersion: scaffolder.backstage.io/v1beta3
kind: Template
metadata:
  name: go-web-service
  tags: [go, web]
  labels: {adhar.io/dashboard-type: backend-generic}
spec:
  owner: group:platform-engineers
  parameters:
    - required: [name]
      properties:
        name: {type: string}
        description: {type: string}
        owner: {type: string, ui:field: OwnerPicker}
        port: {type: integer, default: 8080}
        project: {type: string, default: adhar}
    - required: [repoUrl]
      properties:
        repoUrl: {type: string, ui:field: RepoUrlPicker}
  steps:
    - action: fetch:template
      input:
        url: ./skeleton
        values:
          name: ${{ parameters.name }}
          owner: ${{ parameters.owner }}
          port: ${{ parameters.port }}
          project: ${{ parameters.project }}
          giteaHost: ${{ (parameters.repoUrl | parseRepoUrl).host }}
    - action: adhar:create-argocd-app
      input: {path: deploy}
`

var serviceSkeleton = map[string]string{
	"catalog-info.yaml":                  "metadata: {name: ${{ values.name }}}\nspec: {owner: ${{ values.owner }}}\n",
	"README.md":                          "# ${{ values.name }} on :${{ values.port }} at https://${{ values.giteaHost }}\n",
	"deploy/base/rollout.yaml":           "project: ${{ values.project }}\n",
	"deploy/envs/dev/kustomization.yaml": "resources: [../../base]\n",
}

// Everything that can refuse is asked before the repository exists — the one
// side effect a re-run cannot undo — and before anything is registered.
func TestATemplateDeployCreatesNothingUntilEveryCheckPasses(t *testing.T) {
	broken := map[string]string{"compose.yaml": "{% if values.port in values.ports %}x{% endif %}\n"}
	for k, v := range serviceSkeleton {
		broken[k] = v
	}
	for _, tc := range []struct {
		name, app, project string
		explicit           bool
		params             []string
		path               string
		skeleton           map[string]string
		want               string
	}{
		{"a name the Console refuses", "My_App", "adhar", false, nil, "", serviceSkeleton, "kebab-case"},
		{"a project that does not exist", "billing", "paymnets", true, nil, "", serviceSkeleton, "does not exist"},
		{"--project and --param project disagree", "billing", "payments", true, []string{"project=billing"}, "", serviceSkeleton, "disagree"},
		{"a parameter the template does not declare", "billing", "adhar", false, []string{"prot=8080"}, "", serviceSkeleton, "no parameter"},
		{"a skeleton the renderer cannot render", "billing", "adhar", false, nil, "", broken, "compose.yaml"},
		{"a --path with no overlays under it", "billing", "adhar", false, nil, "manifests", serviceSkeleton, "deploy/envs/"},
	} {
		withDeployFlags(t, tc.project, tc.params, tc.path)
		gc, f := newTemplateGitea(t, "go-web-service", serviceTemplateYAML, tc.skeleton)
		dc, applies := applyRecorder(t, nil)
		_, _, _, err := scaffoldTemplateApplication(t.Context(), dc, gc, "https://gitea.example.io:443", tc.app, "go-web-service", tc.explicit)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one mentioning %q", tc.name, err, tc.want)
		}
		if len(f.writes) != 0 || len(*applies) != 0 {
			t.Errorf("%s: wrote %v to Gitea and applied %d Applications", tc.name, f.writes, len(*applies))
		}
	}
}

// The happy path, end to end against fakes: one repository, one commit, and
// one server-side apply of the paved-road Application as the CLI's own field
// manager, without force — the Console's applyApplication.
func TestATemplateDeployCommitsOnceAndAppliesThePavedRoadApplication(t *testing.T) {
	withDeployFlags(t, "payments", []string{"port=9090", "description=Orders & co"}, "./deploy/")
	gc, f := newTemplateGitea(t, "go-web-service", serviceTemplateYAML, serviceSkeleton)
	dc, applies := applyRecorder(t, nil)
	name, ns, project, err := scaffoldTemplateApplication(t.Context(), dc, gc, "https://gitea.example.io:443", "billing", "go-web-service", true)
	if err != nil {
		t.Fatal(err)
	}
	if name != "billing" || ns != "adhar-system" || project != "payments" {
		t.Errorf("registered %s/%s in project %s", ns, name, project)
	}
	if strings.Join(f.writes, ",") != "POST /api/v1/orgs/adhar/repos,POST /api/v1/repos/adhar/billing/contents" {
		t.Fatalf("Gitea writes %v", f.writes)
	}
	// The repository is described by the application, not by the template:
	// the Console creates it with body.description.
	if f.created["name"] != "billing" || f.created["description"] != "Orders & co" || f.created["private"] != false ||
		f.created["auto_init"] != false || f.created["default_branch"] != "main" {
		t.Errorf("repository created as %v", f.created)
	}
	// The owner nobody named is the template's own, and an explicit --project
	// is what the template renders as well as what is registered.
	if got := f.file("catalog-info.yaml"); !strings.Contains(got, "owner: group:platform-engineers") {
		t.Errorf("catalog-info.yaml: %q", got)
	}
	if got := f.file("deploy/base/rollout.yaml"); got != "project: payments\n" {
		t.Errorf("rollout.yaml: %q", got)
	}
	if got := f.file("README.md"); got != "# billing on :9090 at https://gitea.example.io\n" {
		t.Errorf("README.md: %q", got)
	}
	if len(*applies) != 1 {
		t.Fatalf("%d applies", len(*applies))
	}
	a := (*applies)[0]
	if a.GetPatchType() != types.ApplyPatchType || a.GetNamespace() != "adhar-system" || a.GetName() != "billing" ||
		a.PatchOptions.FieldManager != "adhar-cli" || (a.PatchOptions.Force != nil && *a.PatchOptions.Force) {
		t.Errorf("apply %s %s/%s as %q force %v", a.GetPatchType(), a.GetNamespace(), a.GetName(), a.PatchOptions.FieldManager, a.PatchOptions.Force)
	}
	var got map[string]any
	_ = json.Unmarshal(a.GetPatch(), &got)
	src, _, _ := unstructured.NestedStringMap(got, "spec", "parameters", "source")
	if src["path"] != "deploy" || src["repoURL"] != "https://gitea.example.io/adhar/billing.git" || src["targetRevision"] != "main" {
		t.Errorf("source %v", src)
	}
	if p, _, _ := unstructured.NestedString(got, "spec", "parameters", "project"); p != "payments" {
		t.Errorf("project %q", p)
	}
}

// A control plane whose XRD predates `type: function` refuses the object; the
// CLI retries without the type, through the same apply, as the Console does.
func TestATemplateDeployRetriesARefusedTypeWithoutIt(t *testing.T) {
	withDeployFlags(t, "adhar", nil, "")
	fn := strings.Replace(serviceTemplateYAML, "name: go-web-service", "name: function-go-service", 1)
	gc, _ := newTemplateGitea(t, "function-go-service", fn, serviceSkeleton)
	dc, applies := applyRecorder(t, func(obj map[string]any) error {
		if _, has, _ := unstructured.NestedFieldNoCopy(obj, "spec", "parameters", "type"); has {
			return errors.New(`Application.platform.adhar.io "fn" is invalid: spec.parameters.type: Unsupported value: "function"`)
		}
		return nil
	})
	if _, _, _, err := scaffoldTemplateApplication(t.Context(), dc, gc, "https://gitea.example.io", "fn-app", "function-go-service", false); err != nil {
		t.Fatal(err)
	}
	if len(*applies) != 2 {
		t.Fatalf("%d applies, want the refused one and its retry", len(*applies))
	}
	var retried map[string]any
	_ = json.Unmarshal((*applies)[1].GetPatch(), &retried)
	if _, has, _ := unstructured.NestedFieldNoCopy(retried, "spec", "parameters", "type"); has {
		t.Error("the retry still carried spec.parameters.type")
	}
	if l, _, _ := unstructured.NestedString(retried, "metadata", "labels", "adhar.io/workload"); l != "function" {
		t.Error("the retry lost the adhar.io/workload label")
	}
}

// `deploy --wait` on a template deploy waits for the Application it registered
// in adhar-system: the composition's phase Healthy (dev healthy, the later
// environments awaiting their first promotion) ends the wait, with each
// environment's Argo CD state filled in; anything short of it times out
// naming the phase it last saw.
func TestDeployWaitEndsOnThePavedRoadPhase(t *testing.T) {
	dc := fakeCluster(
		pavedRoadXR("qua-service", "Healthy", "1/3 environments healthy; test awaits its first promotion"),
		pavedRoadXR("node-svc", "Pending", "0/3 environments healthy; dev is Synced/Degraded"),
		argoApp("adhar-qua-service-dev", "qua-service", "dev", "Synced", "Healthy"))

	status, err := waitForApplicationReadyWith(t.Context(), dc, "adhar-system", "qua-service", 2*time.Second)
	if err != nil {
		t.Fatalf("a Healthy paved-road app: %v", err)
	}
	if status.Namespace != "adhar-system" || status.EnvironmentStatuses[0].SyncStatus != "Synced" || status.EnvironmentStatuses[0].Health != "Healthy" {
		t.Errorf("status %+v", status)
	}

	_, err = waitForApplicationReadyWith(t.Context(), dc, "adhar-system", "node-svc", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "last: Pending: 0/3 environments healthy; dev is Synced/Degraded") {
		t.Errorf("a Pending app: %v", err)
	}
	// Registered, not yet readable (or a namespace it does not live in): the
	// wait keeps asking until the timeout rather than failing at once.
	if _, err := waitForApplicationReadyWith(t.Context(), dc, "dev", "qua-service", 50*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("an app not found yet: %v", err)
	}
}

// --namespace, --dest-* and --version do not apply to a template deploy and
// say so; --path and --env do apply.
func TestATemplateDeploySaysWhichFlagsItIgnores(t *testing.T) {
	changed := map[string]bool{"namespace": true, "dest-namespace": true, "dest-server": true, "version": true, "path": true, "env": true, "project": true}
	got := templateIgnoredFlags(func(f string) bool { return changed[f] })
	if len(got) != 4 {
		t.Fatalf("warnings %v", got)
	}
	for i, f := range []string{"--namespace", "--dest-namespace", "--dest-server", "--version"} {
		if !strings.HasPrefix(got[i], f+" does not apply to --template") {
			t.Errorf("warning %d = %q, want one for %s", i, got[i], f)
		}
	}
	if len(templateIgnoredFlags(func(string) bool { return false })) != 0 {
		t.Error("warned about flags nobody gave")
	}
}
