package apps

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// The cases below are where the CLI and the Console's scaffolder
// (apps/console/app/server/template-render.ts, scaffolder.ts) disagreed after
// the 2026-11-03 parity work. Each expectation is the Console's own output for
// the same input, captured by running its modules.

// `| dump` is JSON.stringify, which writes U+2028 and U+2029 as they are.
// encoding/json escapes them, and the check meant to undo that looked for the
// characters instead of the escapes, so it never ran: 17 skeleton lines dump
// the description, and a description carrying U+2028 was committed as
// the escape `\u2028` by the CLI and as the character by the Console.
func TestDumpWritesLineSeparatorsAsJSONStringifyDoes(t *testing.T) {
	ls, ps, replacement := string(rune(0x2028)), string(rune(0x2029)), string(rune(0xfffd))
	for in, want := range map[string]string{
		"line" + ls + "sep and " + ps + "para": `"line` + ls + "sep and " + ps + `para"`,
		// The six characters \u2028 as text: an escaped backslash, then text.
		"x" + `\` + "u2028 & <y>": `"x\\` + "u2028 & <y>" + `"`,
		"bad\xff":                 `"bad` + replacement + `"`, // TextDecoder's replacement character
		`plain "quoted"`:          `"plain \"quoted\""`,
	} {
		if got := render(t, "${{ values.d | dump }}", map[string]any{"d": in}); got != want {
			t.Errorf("dump of %q = %q, want %q", in, got, want)
		}
	}
}

// An unterminated literal is tokenised as a lone quote, which the Console reads
// as JavaScript's "'".slice(1, -1) — the empty string. The CLI sliced
// t[1:len(t)-1] and panicked, so one typo in a template crashed the command.
func TestALoneQuoteInAConditionIsTheEmptyString(t *testing.T) {
	tpl := "{% if values.x == ' %}Y{% else %}N{% endif %}"
	if got := render(t, tpl, map[string]any{"x": ""}); got != "Y" {
		t.Errorf("x empty: %q, want Y", got)
	}
	if got := render(t, tpl, map[string]any{"x": "a"}); got != "N" {
		t.Errorf("x set: %q, want N", got)
	}
}

// computeValues falls back to the parameters when the fetch:template step has
// no values map; an empty map is still a map. The CLI rendered every
// `${{ values.* }}` of such a template empty.
func TestAStepWithoutAValuesMapRendersWithTheParameters(t *testing.T) {
	var without, empty backstageTemplate
	_ = yaml.Unmarshal([]byte("spec: {steps: [{action: fetch:template, input: {url: ./skeleton}}]}"), &without)
	_ = yaml.Unmarshal([]byte("spec: {steps: [{action: fetch:template, input: {url: ./skeleton, values: {}}}]}"), &empty)
	params := map[string]any{"name": "billing", "port": 9000}

	_, spec, _ := without.fetchTemplateStep()
	values, err := buildValues(spec, params)
	if err != nil || values["name"] != "billing" || values["port"] != 9000 {
		t.Errorf("no values map: %v %v, want the parameters", values, err)
	}
	values["name"] = "changed"
	if params["name"] != "billing" {
		t.Error("the values context aliases the parameters")
	}
	_, spec, _ = empty.fetchTemplateStep()
	if values, err := buildValues(spec, params); err != nil || len(values) != 0 {
		t.Errorf("an empty values map: %v %v, want nothing", values, err)
	}
}

// The Console renders a file with `{ values, ...values }`: a value that is
// itself named `values` wins over the map.
func TestAValueNamedValuesShadowsTheMapAsInTheConsole(t *testing.T) {
	ctx := skeletonRenderContext(map[string]any{"values": map[string]any{"values": "shadow", "x": "y"}})
	out, err := renderContent("${{ values }}|${{ values.x }}|${{ x }}", ctx)
	if err != nil || out != "shadow||y" {
		t.Errorf("got %q %v, want shadow||y", out, err)
	}
}

// The Console synthesises the RepoUrlPicker value from Gitea's public URL,
// scheme included (`${publicGitea}?owner=${org}&repo=${name}`); the CLI wrote
// the bare host, so a skeleton using parameters.repoUrl as it is would differ.
func TestTheRepoUrlParameterIsTheOneTheConsoleSynthesises(t *testing.T) {
	tpl := testTemplate(t)
	_, spec, _ := tpl.fetchTemplateStep()
	for base, want := range map[string]string{
		"https://gitea.platform.adhar.io:443":   "https://gitea.platform.adhar.io?owner=adhar&repo=billing",
		"https://gitea.adhar.localtest.me:8443": "https://gitea.adhar.localtest.me:8443?owner=adhar&repo=billing",
	} {
		ctxVals, err := scaffoldContext(tpl, spec, base, "billing", "dev", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := ctxVals["parameters"].(map[string]any)["repoUrl"]; got != want {
			t.Errorf("%s: repoUrl %v, want %s", base, got, want)
		}
		// No description given is the empty string the Console sends
		// (body.description ?? ''), which `| dump` writes as "" — not null.
		if got, ok := ctxVals["parameters"].(map[string]any)["description"]; !ok || got != "" {
			t.Errorf("%s: description %#v, want \"\"", base, got)
		}
	}
}

// The Console's wizard always sends an owner, seeded from the template's own
// spec.owner; eleven skeletons write `owner: ${{ values.owner }}` into
// catalog-info.yaml with no fallback, so a CLI scaffold without --param owner
// committed a Component with no owner, which Backstage rejects.
func TestAnOwnerLeftUnsetIsTheTemplatesOwner(t *testing.T) {
	parse := func(src string) *backstageTemplate {
		var tpl backstageTemplate
		if err := yaml.Unmarshal([]byte(src), &tpl); err != nil {
			t.Fatal(err)
		}
		return &tpl
	}
	picker := parse(`
metadata: {name: go-web-service}
spec:
  owner: group:platform-engineers
  parameters: [{properties: {name: {type: string}, owner: {type: string, ui:field: OwnerPicker}}}]
`)
	for _, tc := range []struct {
		name      string
		tpl       *backstageTemplate
		overrides map[string]string
		want      any
	}{
		{"unset", picker, nil, "group:platform-engineers"},
		{"given", picker, map[string]string{"owner": "group:payments"}, "group:payments"},
		{"given empty", picker, map[string]string{"owner": ""}, ""},
		{"a default of its own", parse(`
spec:
  owner: group:platform-engineers
  parameters: [{properties: {owner: {type: string, default: group:web}}}]
`), nil, "group:web"},
		{"no owner parameter", parse(`
spec:
  owner: group:platform-engineers
  parameters: [{properties: {name: {type: string}}}]
`), nil, nil},
		{"no spec.owner", parse(`
spec:
  parameters: [{properties: {owner: {type: string}}}]
`), nil, nil},
	} {
		params, err := resolveParameters(tc.tpl, "billing", "adhar", "billing", "https://gitea.example.io", tc.overrides)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := params["owner"]; got != tc.want {
			t.Errorf("%s: owner %v, want %v", tc.name, got, tc.want)
		}
	}
	// The deploy says which owner it chose only when it chose one.
	if owner, ok := templateOwnerDefault(picker, map[string]string{"owner": ""}); ok {
		t.Errorf("an explicit empty owner was replaced by %q", owner)
	}
}

// Every shipped template deploys deploy/envs/<environment>, and the paved
// road deploys <path>/envs/<environment> in each environment of the project.
// The documented `--path manifests` registered an Application whose
// environments could never deploy; a --path with no overlays under it is now
// refused before the repository exists.
func TestATemplatePathMustHoldTheEnvironmentOverlays(t *testing.T) {
	r := &renderedScaffold{template: testTemplate(t), templateID: "go-web-service",
		paths: []string{"README.md", "deploy/base/rollout.yaml", "deploy/envs/dev/kustomization.yaml"}}
	for flag, want := range map[string]string{"": "", "deploy": "deploy", "./deploy/": "deploy", "/deploy": "deploy"} {
		if got, err := templateSourcePath(flag, r); err != nil || got != want {
			t.Errorf("--path %q: %q %v, want %q", flag, got, err, want)
		}
	}
	for _, flag := range []string{"manifests", "deploy/base", "."} {
		_, err := templateSourcePath(flag, r)
		if err == nil || !strings.Contains(err.Error(), "deploy/envs/") {
			t.Errorf("--path %q: %v, want a refusal naming deploy/envs/", flag, err)
		}
	}
	atRoot := &renderedScaffold{template: testTemplate(t), paths: []string{"envs/dev/kustomization.yaml"}}
	if got, err := templateSourcePath(".", atRoot); err != nil || got != "." {
		t.Errorf("overlays at the root: %q %v", got, err)
	}
}

// adhar-system is asked first, and a team member's kubeconfig may not read it:
// that refusal must not stop `status` and `delete` finding the dev Application
// they found before. Any other error still stops the search, so `delete` never
// falls through to a namespace it was not looking for.
func TestFindApplicationPassesOverANamespaceItMayNotRead(t *testing.T) {
	refuse := func(err error) func(k8stesting.Action) (bool, runtime.Object, error) {
		return func(a k8stesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == "adhar-system" {
				return true, nil, err
			}
			return false, nil, nil
		}
	}
	forbidden := k8serrors.NewForbidden(applicationGVR.GroupResource(), "old-app", errors.New("cannot get"))
	devApp := func() *unstructured.Unstructured {
		app := pavedRoadXR("old-app", "", "")
		app.SetNamespace("dev")
		return app
	}
	ctx := t.Context()
	defaults := applicationNamespaces(false, "dev")

	dc := fakeCluster(devApp())
	dc.PrependReactor("get", "applications", refuse(forbidden))
	if app, err := findApplication(ctx, dc, defaults, "old-app"); err != nil || app.GetNamespace() != "dev" {
		t.Fatalf("dev Application behind a forbidden adhar-system: %v %v", app, err)
	}
	if _, err := findApplication(ctx, dc, defaults, "qua-service"); !k8serrors.IsForbidden(err) || errors.Is(err, ErrApplicationNotFound) {
		t.Errorf("found nowhere, adhar-system unreadable: %v, want the refusal (it may be there)", err)
	}
	if _, err := findApplication(ctx, dc, []string{"adhar-system"}, "old-app"); !k8serrors.IsForbidden(err) {
		t.Errorf("an explicit --namespace that refuses: %v", err)
	}

	broken := fakeCluster(devApp())
	broken.PrependReactor("get", "applications", refuse(k8serrors.NewInternalError(errors.New("etcd timeout"))))
	if app, err := findApplication(ctx, broken, defaults, "old-app"); err == nil || app != nil {
		t.Errorf("an adhar-system that errors fell through to dev: %v %v", app, err)
	}
}

// A skeleton without a catalog-info.yaml, or with nothing under its manifest
// path, gets the files the Console's scaffolder generates for it
// (buildCatalogInfo, and the deploy starter). Every shipped template carries
// both, so only a template of the team's own reaches these; the expected
// files are what the Console (v0.1.169) committed for the same two templates.
func TestTheFilesASkeletonLacksAreTheOnesTheConsoleGenerates(t *testing.T) {
	parse := func(src string) *backstageTemplate {
		var tpl backstageTemplate
		if err := yaml.Unmarshal([]byte(src), &tpl); err != nil {
			t.Fatal(err)
		}
		return &tpl
	}
	bare := parse(`
metadata: {name: bare-service}
spec:
  owner: group:platform-engineers
  parameters:
    - properties:
        name: {type: string}
        description: {type: string}
        owner: {type: string, ui:field: OwnerPicker}
        title: {type: string, default: 'Bare "service" & co'}
        system: {type: string, default: payments}
        domain: {type: string, default: commerce}
        lifecycle: {type: string, enum: [experimental, production], default: production}
  steps:
    - action: fetch:template
      input: {url: ./skeleton, values: {name: "${{ parameters.name }}"}}
    - action: adhar:create-argocd-app
      input: {path: deploy}
`)
	site := parse(`
metadata: {name: tagged-site, tags: [website, docs]}
spec:
  type: website
  parameters: [{properties: {name: {type: string}, description: {type: string}}}]
  steps:
    - action: fetch:template
      input: {url: ./skeleton, values: {name: "${{ parameters.name }}"}}
    - action: adhar:create-argocd-app
      input: {path: deploy}
`)
	starter := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"
	for _, tc := range []struct {
		app, id, giteaURL string
		tpl               *backstageTemplate
		overrides         map[string]string
		catalog           string
	}{
		{"bare-f", "bare-service", "https://gitea.platform.adhar.io:443", bare,
			map[string]string{"description": `Orders & <fast> "q"`, "title": "T: it's", "system": "", "domain": "shop", "lifecycle": "experimental", "owner": "group:payments"},
			"apiVersion: backstage.io/v1alpha1\nkind: Component\nmetadata:\n  name: bare-f\n  title: \"T: it's\"\n  description: \"Orders & <fast> \\\"q\\\"\"\n  annotations:\n    adhar.io/git-repo: https://gitea.platform.adhar.io/adhar/bare-f\n    argocd/app-name: bare-f\n  tags: [\"gitea\"]\nspec:\n  type: service\n  lifecycle: experimental\n  owner: group:payments\n  domain: shop\n"},
		{"site-d", "tagged-site", "https://gitea.adhar.localtest.me:8443", site, nil,
			"apiVersion: backstage.io/v1alpha1\nkind: Component\nmetadata:\n  name: site-d\n  annotations:\n    adhar.io/git-repo: https://gitea.adhar.localtest.me:8443/adhar/site-d\n    argocd/app-name: site-d\n  tags: [\"website\", \"docs\"]\nspec:\n  type: website\n  lifecycle: experimental\n  owner: group:platform\n"},
	} {
		gc, _ := newFakeTemplatesGitea(t, tc.id, map[string]string{"README.md": "# ${{ values.name }}\n"})
		r, err := renderScaffold(t.Context(), gc, tc.tpl, tc.giteaURL, tc.id, tc.app, "dev", tc.overrides)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, op := range r.files {
			b, _ := base64.StdEncoding.DecodeString(op.Content)
			got[op.Path] = string(b)
		}
		if len(got) != 3 || got["catalog-info.yaml"] != tc.catalog || got["deploy/kustomization.yaml"] != starter {
			t.Errorf("%s: committed %d files\ncatalog-info.yaml:\n%s\nwant:\n%s\ndeploy/kustomization.yaml: %q", tc.app, len(got), got["catalog-info.yaml"], tc.catalog, got["deploy/kustomization.yaml"])
		}
		if r.description != tc.overrides["description"] {
			t.Errorf("%s: repository description %q, want the application's own %q", tc.app, r.description, tc.overrides["description"])
		}
	}
}

// `delete` without --namespace deletes the paved-road Application in
// adhar-system, asks about THAT namespace, and leaves a same-named dev
// Application alone; --namespace dev deletes only the dev one.
func TestDeleteRemovesTheApplicationItAskedAbout(t *testing.T) {
	devTwin := pavedRoadXR("qua-service", "", "")
	devTwin.SetNamespace("dev")
	gr := applicationGVR
	for _, tc := range []struct {
		name       string
		namespaces []string
		agree      bool
		wantAsked  string
		wantGone   string
	}{
		{"declined", applicationNamespaces(false, "dev"), false, "adhar-system", ""},
		{"no --namespace", applicationNamespaces(false, "dev"), true, "adhar-system", "adhar-system"},
		{"--namespace dev", applicationNamespaces(true, "dev"), true, "dev", "dev"},
	} {
		dc := fakeCluster(pavedRoadXR("qua-service", "Healthy", ""), devTwin.DeepCopy())
		asked := ""
		err := deleteApplication(t.Context(), dc, tc.namespaces, "qua-service", func(ns string) bool {
			asked = ns
			return tc.agree
		})
		if err != nil || asked != tc.wantAsked {
			t.Fatalf("%s: asked about %q, err %v", tc.name, asked, err)
		}
		for _, ns := range []string{"adhar-system", "dev"} {
			_, gerr := dc.Resource(gr).Namespace(ns).Get(t.Context(), "qua-service", metav1.GetOptions{})
			if gone := k8serrors.IsNotFound(gerr); gone != (ns == tc.wantGone) {
				t.Errorf("%s: %s/qua-service gone=%v", tc.name, ns, gone)
			}
		}
	}
	// A --repo/--file Application lives in dev alone: asked about, and deleted
	// from, dev.
	dc := fakeCluster(devTwin.DeepCopy())
	asked := ""
	if err := deleteApplication(t.Context(), dc, applicationNamespaces(false, "dev"), "qua-service", func(ns string) bool {
		asked = ns
		return true
	}); err != nil || asked != "dev" {
		t.Errorf("a dev-only Application: asked about %q, err %v", asked, err)
	}
	if _, err := dc.Resource(gr).Namespace("dev").Get(t.Context(), "qua-service", metav1.GetOptions{}); !k8serrors.IsNotFound(err) {
		t.Errorf("the dev-only Application survived: %v", err)
	}
	if err := deleteApplication(t.Context(), fakeCluster(), applicationNamespaces(false, "dev"), "nope", func(string) bool { return true }); !errors.Is(err, ErrApplicationNotFound) {
		t.Errorf("deleting what does not exist: %v", err)
	}
}
