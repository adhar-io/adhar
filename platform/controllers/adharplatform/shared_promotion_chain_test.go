package adharplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"sigs.k8s.io/yaml"
)

// Environments are the PROJECT's, shared by every application in it: one Kargo
// chain per project (Stages named after the environments) that every
// application's Warehouse feeds, instead of a Stage chain `<app>-<env>` per
// application. These tests RENDER the compositions' Go templates the way
// function-go-templating does (Sprig + its helpers, a JSON-shaped request) and
// assert the contract the console builds against.

// renderGoTemplate executes the inline template of the composition's first
// function-go-templating step against req and returns the rendered documents.
func renderGoTemplate(t *testing.T, req map[string]any, parts ...string) []map[string]any {
	t.Helper()
	var comp struct {
		Spec struct {
			Pipeline []struct {
				FunctionRef struct {
					Name string `json:"name"`
				} `json:"functionRef"`
				Input struct {
					Inline struct {
						Template string `json:"template"`
					} `json:"inline"`
				} `json:"input"`
			} `json:"pipeline"`
		} `json:"spec"`
	}
	body := readConfigurationFile(t, parts...)
	// A leading `---` and comment header are fine for the YAML decoder.
	if err := yaml.Unmarshal([]byte(body), &comp); err != nil {
		t.Fatalf("%s does not parse: %v", strings.Join(parts, "/"), err)
	}
	src := ""
	for _, s := range comp.Spec.Pipeline {
		if s.FunctionRef.Name == "function-go-templating" {
			src = s.Input.Inline.Template
			break
		}
	}
	if src == "" {
		t.Fatalf("%s has no function-go-templating step", strings.Join(parts, "/"))
	}
	funcs := template.FuncMap{
		// function-go-templating's own helpers that these templates use.
		"getExtraResources": func(r map[string]any, name string) []any {
			for _, key := range []string{"requiredResources", "extraResources"} {
				if m, ok := r[key].(map[string]any); ok {
					if rs, ok := m[name].(map[string]any); ok {
						items, _ := rs["items"].([]any)
						return items
					}
				}
			}
			return nil
		},
		"toYaml": func(v any) (string, error) { b, err := yaml.Marshal(v); return string(b), err },
	}
	tpl, err := template.New("manifests").Funcs(sprig.TxtFuncMap()).Funcs(funcs).Parse(src)
	if err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	// The function hands the template protojson: numbers are float64.
	raw, _ := json.Marshal(req)
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	var out bytes.Buffer
	if err := tpl.Execute(&out, in); err != nil {
		t.Fatalf("template does not render: %v", err)
	}
	var docs []map[string]any
	for _, d := range strings.Split(out.String(), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("a rendered document is not YAML: %v\n%s", err, d)
		}
		if len(m) > 0 {
			docs = append(docs, m)
		}
	}
	return docs
}

func fromYAML(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// composedManifest returns the provider-kubernetes Object's manifest of the
// given kind and name (or nil).
func composedManifest(docs []map[string]any, kind, name string) map[string]any {
	for _, d := range docs {
		m, _ := dig(d, "spec", "forProvider", "manifest").(map[string]any)
		if m == nil {
			continue
		}
		if m["kind"] == kind && dig(m, "metadata", "name") == name {
			return m
		}
	}
	return nil
}

func manifestsOfKind(docs []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, d := range docs {
		if m, _ := dig(d, "spec", "forProvider", "manifest").(map[string]any); m != nil && m["kind"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func docOfKind(docs []map[string]any, kind string) map[string]any {
	for _, d := range docs {
		if d["kind"] == kind && d["spec"] == nil {
			return d
		}
	}
	return nil
}

func projectEnvironmentsStatus() string {
	return `
status:
  atProvider:
    manifest:
      status:
        organisation: acme
        team: payments
        imageRegistry: harbor-core.adhar-system.svc.cluster.local/library
        environments:
          - {name: dev, namespace: shop-dev, tier: dev, autoPromote: true}
          - {name: test, namespace: shop-test, tier: test, autoPromote: true}
          - {name: prod, namespace: shop-prod, tier: prod, autoPromote: false}
`
}

func applicationRequest(t *testing.T, name, extraParams string) map[string]any {
	t.Helper()
	xr := fromYAML(t, fmt.Sprintf(`
apiVersion: platform.adhar.io/v1alpha1
kind: Application
metadata: {name: %s, namespace: team-payments}
spec:
  parameters:
    project: shop
    source: {repoURL: "https://gitea.example.com/acme/%s.git", path: deploy, targetRevision: main}
%s`, name, name, extraParams))
	return map[string]any{
		"observed": map[string]any{
			"composite": map[string]any{"resource": xr},
			"resources": map[string]any{"project": map[string]any{"resource": fromYAML(t, projectEnvironmentsStatus())}},
		},
	}
}

// The Application composition no longer owns any Kargo Stage or AnalysisTemplate;
// its Argo CD Applications authorise the PROJECT's environment Stage, and it
// publishes what the shared chain needs on status.delivery.
func TestAnApplicationJoinsItsProjectsSharedEnvironments(t *testing.T) {
	docs := renderGoTemplate(t, applicationRequest(t, "cart", ""), "compositions", "apps", "argocd-multienv.yaml")

	if st := manifestsOfKind(docs, "Stage"); len(st) != 0 {
		t.Fatalf("the Application composition still composes %d Kargo Stage(s); the project's shared Stages replace them", len(st))
	}
	if at := manifestsOfKind(docs, "AnalysisTemplate"); len(at) != 0 {
		t.Fatalf("the Application composition still composes per-app AnalysisTemplates")
	}
	wh := composedManifest(docs, "Warehouse", "cart")
	if wh == nil || dig(wh, "metadata", "namespace") != "shop" || dig(wh, "metadata", "labels", "adhar.io/application") != "cart" ||
		dig(wh, "metadata", "labels", "adhar.io/project") != "shop" {
		t.Fatalf("the Warehouse `cart` in the Kargo project namespace `shop`, labelled with its application and project, is missing: %v", wh)
	}
	for _, env := range []string{"dev", "test", "prod"} {
		app := composedManifest(docs, "Application", "shop-cart-"+env)
		if app == nil {
			t.Fatalf("no Argo CD Application shop-cart-%s", env)
		}
		if got := dig(app, "metadata", "annotations", "kargo.akuity.io/authorized-stage"); got != "shop:"+env {
			t.Errorf("shop-cart-%s authorises %v, want shop:%s (the project's shared Stage)", env, got, env)
		}
		if got := dig(app, "spec", "destination", "namespace"); got != "shop-"+env {
			t.Errorf("shop-cart-%s deploys into %v, want the project environment namespace shop-%s", env, got, env)
		}
	}
	st := docOfKind(docs, "Application")
	d, _ := dig(st, "status", "delivery").(map[string]any)
	if d == nil || d["promote"] != true || d["warehouse"] != "cart" ||
		d["image"] != "harbor-core.adhar-system.svc.cluster.local/library/cart" ||
		d["repoURL"] != "http://gitea-http.adhar-system.svc.cluster.local:3000/acme/cart.git" {
		t.Fatalf("status.delivery is not what the project's chain needs: %v", d)
	}
	envs, _ := dig(st, "status", "environments").([]any)
	if len(envs) != 3 || dig(envs[1].(map[string]any), "stage") != "test" || dig(envs[1].(map[string]any), "argoApplication") != "shop-cart-test" {
		t.Errorf("status.environments does not list the project's environments with their shared Stage: %v", envs)
	}
	cond := docOfKind(docs, "ClaimConditions")
	if cond == nil || !strings.Contains(fmt.Sprint(cond), "reason:Inherited") {
		t.Errorf("no ProjectEnvironments=Inherited condition: %v", cond)
	}
}

// The retired per-application environment list cannot remove, add or rename an
// environment; it is reported, not obeyed.
func TestAnApplicationsEnvironmentOverrideIsIgnoredAndReported(t *testing.T) {
	docs := renderGoTemplate(t, applicationRequest(t, "cart", `    environments: [{name: dev}, {name: staging}]`),
		"compositions", "apps", "argocd-multienv.yaml")
	for _, env := range []string{"dev", "test", "prod"} {
		if composedManifest(docs, "Application", "shop-cart-"+env) == nil {
			t.Errorf("the override removed environment %s; every application runs in all of its project's environments", env)
		}
	}
	if composedManifest(docs, "Application", "shop-cart-staging") != nil {
		t.Error("the override added an environment the project does not have")
	}
	cond := fmt.Sprint(docOfKind(docs, "ClaimConditions"))
	for _, want := range []string{"type:ProjectEnvironments", "reason:OverrideIgnored", "not a project environment: staging", "deployed anyway: test, prod"} {
		if !strings.Contains(cond, want) {
			t.Errorf("the ProjectEnvironments condition lacks %q: %s", want, cond)
		}
	}
}

func projectRequest(t *testing.T, apps ...map[string]any) map[string]any {
	t.Helper()
	xr := fromYAML(t, `
apiVersion: platform.adhar.io/v1alpha1
kind: Project
metadata: {name: shop, namespace: team-payments}
spec:
  parameters: {name: shop, organisation: acme, team: payments}
`)
	items := []any{}
	for _, a := range apps {
		items = append(items, map[string]any{"resource": a})
	}
	return map[string]any{
		"observed":       map[string]any{"composite": map[string]any{"resource": xr}},
		"extraResources": map[string]any{"applications": map[string]any{"items": items}},
	}
}

func deliveredApplication(t *testing.T, name, namespace, project, extra string) map[string]any {
	return fromYAML(t, fmt.Sprintf(`
apiVersion: platform.adhar.io/v1alpha1
kind: Application
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  parameters: {project: %[3]s, source: {repoURL: x}}
status:
  delivery:
    promote: true
    kargoProject: %[3]s
    warehouse: %[1]s
    image: harbor-core.adhar-system.svc.cluster.local/library/%[1]s
    repoURL: http://gitea-http.adhar-system.svc.cluster.local:3000/acme/%[1]s.git
    path: deploy
    targetRevision: main
    soakTime: 0s
    verification: {enabled: true, interval: 60s, count: 5, failureLimit: 1, errorRateThreshold: 0.01}
%[4]s`, name, namespace, project, extra))
}

// One chain per project: Stages named after the project's environments, in its
// order, each requesting freight from every application's Warehouse, with
// promotion steps per origin that update `<project>-<app>-<env>`.
func TestTheProjectComposesOneSharedPromotionChain(t *testing.T) {
	req := projectRequest(t,
		deliveredApplication(t, "cart", "team-payments", "shop", ""),
		deliveredApplication(t, "api", "team-payments", "shop", "    soakTime: 10m\n"),
		// Not this project's, not this namespace's, or not promotable: left out.
		deliveredApplication(t, "other", "team-payments", "elsewhere", ""),
		deliveredApplication(t, "stranger", "team-other", "shop", ""),
		fromYAML(t, "metadata: {name: manual, namespace: team-payments}\nspec: {parameters: {project: shop}}\nstatus: {delivery: {promote: false}}"),
	)
	docs := renderGoTemplate(t, req, "compositions", "project", "local.yaml")

	if er := docOfKind(docs, "ExtraResources"); dig(er, "requirements", "applications", "kind") != "Application" {
		t.Fatalf("the Project composition does not request its Applications as extra resources: %v", er)
	}
	stages := manifestsOfKind(docs, "Stage")
	if len(stages) != 3 {
		t.Fatalf("want one shared Stage per project environment (3), got %d", len(stages))
	}
	prev := ""
	for i, env := range []string{"dev", "test", "prod"} {
		st := stages[i]
		if dig(st, "metadata", "name") != env || dig(st, "metadata", "namespace") != "shop" {
			t.Fatalf("Stage %d is %v/%v, want shop/%s", i, dig(st, "metadata", "namespace"), dig(st, "metadata", "name"), env)
		}
		labels, _ := dig(st, "metadata", "labels").(map[string]any)
		if labels["adhar.io/project"] != "shop" || labels["adhar.io/environment"] != env || labels["adhar.io/stage-scope"] != "project" {
			t.Errorf("Stage %s labels %v lack project/environment/stage-scope=project", env, labels)
		}
		if _, ok := labels["adhar.io/application"]; ok {
			t.Errorf("the shared Stage %s carries adhar.io/application", env)
		}
		reqs, _ := dig(st, "spec", "requestedFreight").([]any)
		if len(reqs) != 2 {
			t.Fatalf("Stage %s requests %d origins, want one per promotable application of the project (api, cart): %v", env, len(reqs), reqs)
		}
		for j, app := range []string{"api", "cart"} {
			r := reqs[j].(map[string]any)
			if dig(r, "origin", "kind") != "Warehouse" || dig(r, "origin", "name") != app {
				t.Errorf("Stage %s origin %d is %v, want Warehouse %s", env, j, r["origin"], app)
			}
			if prev == "" {
				if dig(r, "sources", "direct") != true {
					t.Errorf("the first Stage must take %s's freight directly from its Warehouse: %v", app, r["sources"])
				}
			} else {
				if fmt.Sprint(dig(r, "sources", "stages")) != "["+prev+"]" {
					t.Errorf("Stage %s takes %s's freight from %v, want [%s]", env, app, dig(r, "sources", "stages"), prev)
				}
				if app == "api" && dig(r, "sources", "requiredSoakTime") != "10m" {
					t.Errorf("api's soakTime is not honoured on its origin in %s", env)
				}
			}
		}
		steps, _ := dig(st, "spec", "promotionTemplate", "spec", "steps").([]any)
		found := 0
		for _, s := range steps {
			sm := s.(map[string]any)
			if sm["uses"] == "argocd-update" && strings.Contains(fmt.Sprint(sm["if"]), "ctx.targetFreight.origin.name == 'cart'") {
				found++
				name := dig(sm["config"].(map[string]any), "apps")
				if !strings.Contains(fmt.Sprint(name), "name:shop-cart-"+env) {
					t.Errorf("cart's promotion in %s updates %v, want shop-cart-%s", env, name, env)
				}
				if !strings.Contains(fmt.Sprint(name), "outputs['push-cart'].commit") {
					t.Errorf("cart's Argo CD step does not read its own push step's commit: %v", name)
				}
			}
			if sm["uses"] == "kustomize-set-image" && strings.Contains(fmt.Sprint(sm["if"]), "'cart'") &&
				!strings.Contains(fmt.Sprint(sm["config"]), `imageFrom("harbor-core.adhar-system.svc.cluster.local/library/cart", warehouse("cart")).Digest`) {
				t.Errorf("cart's digest is not read from its own origin: %v", sm["config"])
			}
			if sm["uses"] == "git-commit" && !strings.Contains(fmt.Sprint(dig(sm["config"].(map[string]any), "message")), "[skip ci]") {
				t.Error("a promotion commit lacks [skip ci]; app-ci would build it and the chain never settles")
			}
			if sm["if"] == nil {
				t.Errorf("a promotion step in %s is not gated on the freight's origin: %v", env, sm)
			}
		}
		if found != 1 {
			t.Errorf("Stage %s has %d argocd-update steps for cart, want 1", env, found)
		}
		if dig(st, "spec", "verification", "analysisTemplates") == nil {
			t.Errorf("Stage %s does not verify", env)
		}
		at := composedManifest(docs, "AnalysisTemplate", env+"-health")
		if at == nil || !strings.Contains(fmt.Sprint(at), `namespace="shop-`+env+`"`) || !strings.Contains(fmt.Sprint(at), "cart-rollout-healthy") {
			t.Errorf("the environment AnalysisTemplate %s-health does not check every application in shop-%s", env, env)
		}
		prev = env
	}
	status := docOfKind(docs, "Project")
	if fmt.Sprint(dig(status, "status", "applications")) != "[api cart]" {
		t.Errorf("Project status.applications = %v, want [api cart]", dig(status, "status", "applications"))
	}
}

// A project without promotable applications composes no Stage (Kargo requires
// at least one requested origin) — and still renders everything else.
func TestAProjectWithoutApplicationsHasNoStages(t *testing.T) {
	docs := renderGoTemplate(t, projectRequest(t), "compositions", "project", "local.yaml")
	if st := manifestsOfKind(docs, "Stage"); len(st) != 0 {
		t.Fatalf("an empty project composed %d Stage(s) with no requested freight", len(st))
	}
	if manifestsOfKind(docs, "ProjectConfig") == nil {
		t.Fatal("the Project composition stopped rendering its Kargo ProjectConfig")
	}
}

// A Release promotes into the project's shared Stage, named after the environment.
func TestAReleasePromotesIntoTheProjectsEnvironmentStage(t *testing.T) {
	xr := fromYAML(t, `
apiVersion: platform.adhar.io/v1alpha1
kind: Release
metadata: {name: cart-prod-1, namespace: team-payments}
spec: {parameters: {project: shop, application: cart, environment: prod, freight: abc}}
`)
	docs := renderGoTemplate(t, map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}}},
		"compositions", "release", "kargo.yaml")
	job := composedManifest(docs, "Job", "release-cart-prod-1")
	if job == nil || !strings.Contains(fmt.Sprint(job), "name:STAGE value:prod") {
		t.Fatalf("the Release does not target Stage `prod`: %v", job)
	}
}
