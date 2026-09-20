package apps

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The renderer implements a deliberate SUBSET of Backstage's fetch:template
// templating (see scaffold.go). These tests pin that subset, because the failure
// mode of getting it wrong is not an error — it is a repository full of files
// containing a literal `${{ values.name }}`, which only shows up when the
// generated app fails to build.

func ctx(values map[string]any) map[string]any {
	return map[string]any{"values": values, "parameters": values}
}

func TestRenderSubstitutesValues(t *testing.T) {
	out, err := renderContent("name: ${{ values.name }}\nport: ${{ values.port }}\n",
		ctx(map[string]any{"name": "billing", "port": 8080}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "name: billing\nport: 8080\n" {
		t.Fatalf("got %q", out)
	}
}

func TestRenderInterpolatesInsideALargerString(t *testing.T) {
	// The templates build hostnames this way: `${{ parameters.name }}.suffix`.
	out, err := renderContent("host: ${{ values.name }}.adhar.example.com",
		ctx(map[string]any{"name": "billing"}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "host: billing.adhar.example.com" {
		t.Fatalf("got %q", out)
	}
}

func TestRenderConditionalKeepsTheTakenBranch(t *testing.T) {
	src := "a\n{% if values.withDatabase %}\ndb: yes\n{% else %}\ndb: no\n{% endif %}\nz\n"
	for _, tc := range []struct {
		with bool
		want string
	}{
		{true, "a\ndb: yes\nz\n"},
		{false, "a\ndb: no\nz\n"},
	} {
		out, err := renderContent(src, ctx(map[string]any{"withDatabase": tc.with}))
		if err != nil {
			t.Fatalf("render(with=%v): %v", tc.with, err)
		}
		if out != tc.want {
			t.Fatalf("with=%v: got %q want %q", tc.with, out, tc.want)
		}
	}
}

func TestRenderConditionalAcceptsTheTrimmingForm(t *testing.T) {
	// `{%- if … -%}` is the whitespace-trimming variant; the shipped skeletons
	// use both spellings and they must render identically.
	out, err := renderContent("a\n{%- if values.on -%}\nb\n{%- endif -%}\nc\n",
		ctx(map[string]any{"on": true}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "a\nb\nc\n" {
		t.Fatalf("got %q", out)
	}
}

func TestRenderDoesNotEvaluateInsideADiscardedBranch(t *testing.T) {
	// The regression this guards: substituting first and branching second left a
	// literal `${{ … }}` for a value that only exists in the other branch, and
	// wrote it into the file rather than dropping it with the branch.
	out, err := renderContent("{% if values.on %}${{ values.missing }}{% endif %}ok",
		ctx(map[string]any{"on": false}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "ok" {
		t.Fatalf("got %q", out)
	}
}

func TestRenderNestsConditionals(t *testing.T) {
	src := "{% if values.a %}A{% if values.b %}B{% endif %}{% endif %}"
	for _, tc := range []struct {
		a, b bool
		want string
	}{
		{true, true, "AB"}, {true, false, "A"}, {false, true, ""},
	} {
		out, err := renderContent(src, ctx(map[string]any{"a": tc.a, "b": tc.b}))
		if err != nil {
			t.Fatalf("render(%v,%v): %v", tc.a, tc.b, err)
		}
		if out != tc.want {
			t.Fatalf("a=%v b=%v: got %q want %q", tc.a, tc.b, out, tc.want)
		}
	}
}

func TestRenderRejectsAnUnclosedConditional(t *testing.T) {
	// Loud failure beats half a file: a truncated conditional means the template
	// uses something this renderer does not understand.
	if _, err := renderContent("{% if values.on %}x", ctx(map[string]any{"on": true})); err == nil {
		t.Fatal("expected an error for an unclosed {% if %}")
	}
}

func TestRenderRejectsTemplatingOutsideTheSubset(t *testing.T) {
	// A filter this CLI does not implement must not silently render as empty.
	_, err := renderContent("${{ values.name | upper }}", ctx(map[string]any{"name": "x"}))
	if err == nil {
		t.Fatal("expected an error for an unimplemented filter")
	}
	if !strings.Contains(err.Error(), "does not implement") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestParseRepoURLMatchesBackstagesRepoUrlPicker(t *testing.T) {
	host, owner, repo := parseRepoURL("gitea.example.com?owner=adhar&repo=billing.git")
	if host != "gitea.example.com" || owner != "adhar" || repo != "billing" {
		t.Fatalf("got %q %q %q", host, owner, repo)
	}
}

func TestBuildValuesKeepsTypesAndResolvesParseRepoUrl(t *testing.T) {
	var spec map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(`
name: ${{ parameters.name }}
replicas: ${{ parameters.replicas }}
withDatabase: ${{ parameters.withDatabase }}
hostname: ${{ parameters.name }}.adhar.example.com
gitOwner: ${{ (parameters.repoUrl | parseRepoUrl).owner }}
repoName: ${{ (parameters.repoUrl | parseRepoUrl).repo }}
`), &spec); err != nil {
		t.Fatal(err)
	}
	values, err := buildValues(spec, map[string]any{
		"name":         "billing",
		"replicas":     3,
		"withDatabase": true,
		"repoUrl":      "gitea.example.com?owner=adhar&repo=billing",
	})
	if err != nil {
		t.Fatalf("buildValues: %v", err)
	}
	// A value that is exactly one expression keeps its type: rendering
	// `{% if values.withDatabase %}` against the string "true" would be true for
	// the string "false" as well.
	if values["withDatabase"] != true {
		t.Fatalf("withDatabase = %#v, want the bool true", values["withDatabase"])
	}
	if values["replicas"] != 3 {
		t.Fatalf("replicas = %#v, want the int 3", values["replicas"])
	}
	if values["hostname"] != "billing.adhar.example.com" {
		t.Fatalf("hostname = %#v", values["hostname"])
	}
	if values["gitOwner"] != "adhar" || values["repoName"] != "billing" {
		t.Fatalf("repo parse: %#v %#v", values["gitOwner"], values["repoName"])
	}
}

func TestCoerceParameterHonoursTheDeclaredType(t *testing.T) {
	// --param withDatabase=false must become the boolean false. As the string
	// "false" it is truthy in every conditional, so the database would be
	// provisioned by asking for it not to be.
	v, err := coerceParameter("false", "boolean")
	if err != nil || v != false {
		t.Fatalf("boolean: %#v %v", v, err)
	}
	if v, err := coerceParameter("8080", "integer"); err != nil || v != 8080 {
		t.Fatalf("integer: %#v %v", v, err)
	}
	if _, err := coerceParameter("nope", "integer"); err == nil {
		t.Fatal("expected an error for a non-integer")
	}
}

func TestResolveParametersRequiresDeclaredRequiredValues(t *testing.T) {
	var tpl backstageTemplate
	if err := yaml.Unmarshal([]byte(`
metadata: {name: svc}
spec:
  parameters:
    - title: Application
      required: [name, owner]
      properties:
        name: {type: string}
        owner: {type: string}
        port: {type: integer, default: 3000}
`), &tpl); err != nil {
		t.Fatal(err)
	}
	// `owner` is required and not supplied: report it by name rather than letting
	// it surface as an empty substitution in a rendered file.
	if _, err := resolveParameters(&tpl, "billing", "adhar", "billing", "gitea.example.com", nil); err == nil {
		t.Fatal("expected a missing-required-parameter error")
	} else if !strings.Contains(err.Error(), "owner") {
		t.Fatalf("error does not name the parameter: %v", err)
	}

	params, err := resolveParameters(&tpl, "billing", "adhar", "billing", "gitea.example.com",
		map[string]string{"owner": "team-a"})
	if err != nil {
		t.Fatalf("resolveParameters: %v", err)
	}
	if params["name"] != "billing" || params["owner"] != "team-a" {
		t.Fatalf("params: %#v", params)
	}
	// Declared defaults apply without being restated.
	if params["port"] != 3000 {
		t.Fatalf("port default = %#v", params["port"])
	}
	// The RepoUrlPicker shape the skeletons parse with | parseRepoUrl.
	if got := params["repoUrl"]; got != "gitea.example.com?owner=adhar&repo=billing" {
		t.Fatalf("repoUrl = %#v", got)
	}
}

func TestResolveParametersRejectsAnUnknownParameter(t *testing.T) {
	var tpl backstageTemplate
	_ = yaml.Unmarshal([]byte(`
metadata: {name: svc}
spec:
  parameters:
    - properties: {name: {type: string}}
`), &tpl)
	_, err := resolveParameters(&tpl, "billing", "adhar", "billing", "h", map[string]string{"prot": "grpc"})
	if err == nil {
		t.Fatal("expected an error for a misspelled --param")
	}
	// The message must list what IS accepted; a bare rejection sends the user to
	// read template.yaml in Gitea.
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("error does not list the declared parameters: %v", err)
	}
}

func TestFetchTemplateStepAndManifestPath(t *testing.T) {
	var tpl backstageTemplate
	if err := yaml.Unmarshal([]byte(`
metadata: {name: svc}
spec:
  steps:
    - id: template
      action: fetch:template
      input:
        url: ./skeleton
        values: {name: "${{ parameters.name }}"}
    - id: argo
      action: cnoe:create-argocd-app
      input: {path: "deploy/overlays/prod"}
`), &tpl); err != nil {
		t.Fatal(err)
	}
	skeleton, values, ok := tpl.fetchTemplateStep()
	if !ok || skeleton != "skeleton" || len(values) != 1 {
		t.Fatalf("fetchTemplateStep: %q %v %v", skeleton, len(values), ok)
	}
	// The template states where its manifests are, so --path need not be passed.
	if got := tpl.manifestPath(); got != "deploy/overlays/prod" {
		t.Fatalf("manifestPath = %q", got)
	}
}

func TestManifestPathDefaultsToDeploy(t *testing.T) {
	var tpl backstageTemplate
	_ = yaml.Unmarshal([]byte("metadata: {name: svc}\nspec: {steps: []}\n"), &tpl)
	if got := tpl.manifestPath(); got != "deploy" {
		t.Fatalf("manifestPath = %q, want the platform convention 'deploy'", got)
	}
}

func TestBinaryFilesAreNotTemplated(t *testing.T) {
	// Substituting into a PNG corrupts it; no shipped skeleton templates one.
	for _, p := range []string{"logo.png", "a/b/font.woff2", "app.jar"} {
		if isTextTemplate(p) {
			t.Errorf("%s would be rendered as text", p)
		}
	}
	for _, p := range []string{"main.go", "deploy/service.yaml", "Dockerfile", "README.md"} {
		if !isTextTemplate(p) {
			t.Errorf("%s would be committed unrendered", p)
		}
	}
}

func TestParseTemplateParams(t *testing.T) {
	got, err := parseTemplateParams([]string{"port=8080", "desc=a=b"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Only the FIRST '=' separates; a value may contain more.
	if got["port"] != "8080" || got["desc"] != "a=b" {
		t.Fatalf("got %#v", got)
	}
	if _, err := parseTemplateParams([]string{"nope"}); err == nil {
		t.Fatal("expected an error for a value without '='")
	}
}
