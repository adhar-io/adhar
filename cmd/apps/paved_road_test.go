package apps

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"adhar-io/adhar/globals"

	"gopkg.in/yaml.v3"
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
		{"nightly-import", "batch-jvm", "batch", "batch"},
		{"odd-labels", "not-a-dashboard", "", ""},
	}
	for _, c := range cases {
		s := &scaffoldedRepo{
			CloneURL:       "https://gitea.platform.adhar.io:443/adhar/" + c.name + ".git",
			ManifestPath:   "deploy",
			TemplateLabels: map[string]string{"adhar.io/dashboard-type": c.dashboard, "adhar.io/workload": c.workload},
		}
		app := pavedRoadApplication(c.name, "", s, "")

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
		if c.workload != "" && labels["adhar.io/workload"] != c.workload {
			t.Errorf("%s: a %s template's app lost its adhar.io/workload label (got %q)", c.name, c.workload, labels["adhar.io/workload"])
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

// What the Console's applicationManifest returns for the same four apps —
// output of apps/console/app/server/control-plane.ts (with scaffoldWorkload and
// dashboardTypeOf over each template.yaml), captured 2026-11-03 (the batch
// one 2026-11-13). The CLI's Application must be this object, labelled as the
// CLI's.
var consoleApplicationManifests = map[string]string{
	"nightly-import": `{"apiVersion":"platform.adhar.io/v1alpha1","kind":"Application","metadata":{"name":"nightly-import","namespace":"adhar-system","labels":{"adhar.io/managed-by":"adhar-console","adhar.io/project":"adhar","adhar.io/application":"nightly-import","adhar.io/scaffolded":"true","adhar.io/dashboard-type":"batch-jvm","adhar.io/workload":"batch"}},"spec":{"parameters":{"project":"adhar","displayName":"nightly-import","type":"batch","source":{"repoURL":"https://gitea.platform.adhar.io/adhar/nightly-import.git","path":"deploy","targetRevision":"main"}}}}`,
	"qua-service":    `{"apiVersion":"platform.adhar.io/v1alpha1","kind":"Application","metadata":{"name":"qua-service","namespace":"adhar-system","labels":{"adhar.io/managed-by":"adhar-console","adhar.io/project":"adhar","adhar.io/application":"qua-service","adhar.io/scaffolded":"true","adhar.io/dashboard-type":"backend-jvm"}},"spec":{"parameters":{"project":"adhar","displayName":"qua-service","source":{"repoURL":"https://gitea.platform.adhar.io/adhar/qua-service.git","path":"deploy","targetRevision":"main"}}}}`,
	"reactapp":       `{"apiVersion":"platform.adhar.io/v1alpha1","kind":"Application","metadata":{"name":"reactapp","namespace":"adhar-system","labels":{"adhar.io/managed-by":"adhar-console","adhar.io/project":"adhar","adhar.io/application":"reactapp","adhar.io/scaffolded":"true","adhar.io/dashboard-type":"frontend"}},"spec":{"parameters":{"project":"adhar","displayName":"reactapp","type":"web","source":{"repoURL":"https://gitea.platform.adhar.io/adhar/reactapp.git","path":"deploy","targetRevision":"main"}}}}`,
	"deno-func":      `{"apiVersion":"platform.adhar.io/v1alpha1","kind":"Application","metadata":{"name":"deno-func","namespace":"adhar-system","labels":{"adhar.io/managed-by":"adhar-console","adhar.io/project":"adhar","adhar.io/application":"deno-func","adhar.io/scaffolded":"true","adhar.io/dashboard-type":"backend-generic","adhar.io/workload":"function"}},"spec":{"parameters":{"project":"adhar","displayName":"deno-func","type":"function","source":{"repoURL":"https://gitea.platform.adhar.io/adhar/deno-func.git","path":"deploy","targetRevision":"main"}}}}`,
}

func TestThePavedRoadApplicationIsTheConsolesManifest(t *testing.T) {
	// The three templates' own metadata, as template.yaml declares it.
	templates := map[string]*scaffoldedRepo{
		"qua-service": {TemplateName: "quarkus-web-service", TemplateTags: []string{"recommended", "java", "quarkus"},
			TemplateLabels: map[string]string{"adhar.io/dashboard-type": "backend-jvm"}},
		"reactapp": {TemplateName: "react-spa-app", TemplateTags: []string{"recommended", "react", "frontend"},
			TemplateLabels: map[string]string{"adhar.io/dashboard-type": "frontend"}},
		"deno-func": {TemplateName: "function-deno-service", TemplateTags: []string{"function", "serverless", "deno"},
			TemplateLabels: map[string]string{"adhar.io/dashboard-type": "backend-generic", "adhar.io/workload": "function"}},
		"nightly-import": {TemplateName: "springboot-batch-service", TemplateTags: []string{"recommended", "java", "springboot"},
			TemplateLabels: map[string]string{"adhar.io/dashboard-type": "batch-jvm", "adhar.io/workload": "batch"}},
	}
	for name, s := range templates {
		// What commitScaffold builds from utils.GiteaBaseUrl, which spells :443.
		s.CloneURL = scaffoldGiteaBase("https://gitea.platform.adhar.io:443") + "/adhar/" + name + ".git"
		s.ManifestPath = "deploy"
		var want map[string]interface{}
		if err := json.Unmarshal([]byte(consoleApplicationManifests[name]), &want); err != nil {
			t.Fatal(err)
		}
		_ = unstructured.SetNestedField(want, cliFieldManager, "metadata", "labels", "adhar.io/managed-by")
		got := pavedRoadApplication(name, "", s, "")
		if !reflect.DeepEqual(got.Object, want) {
			gj, _ := json.Marshal(got.Object)
			wj, _ := json.Marshal(want)
			t.Errorf("%s:\n got %s\nwant %s", name, gj, wj)
		}
	}
}

// The Console decides "function" from the template's name or a `function`
// tag (scaffoldWorkload → templateFamilyOf); the CLI read only the
// adhar.io/workload label, so a template carrying the name or tag alone was
// registered as a service by the CLI and as a Function by the Console.
func TestAFunctionTemplateIsRecognisedTheWayTheConsoleRecognisesIt(t *testing.T) {
	for _, tc := range []struct {
		s    scaffoldedRepo
		want bool
	}{
		{scaffoldedRepo{TemplateName: "function-go-service"}, true},
		{scaffoldedRepo{TemplateName: "function"}, true},
		{scaffoldedRepo{TemplateName: "thumbnailer", TemplateTags: []string{"serverless", " Function "}}, true},
		{scaffoldedRepo{TemplateName: "thumbnailer", TemplateLabels: map[string]string{"adhar.io/workload": "function"}}, true},
		{scaffoldedRepo{TemplateName: "functional-web-service", TemplateTags: []string{"functions"}}, false},
		{scaffoldedRepo{TemplateName: "go-web-service", TemplateTags: []string{"go", "web"}}, false},
	} {
		if got := isFunctionTemplate(&tc.s); got != tc.want {
			t.Errorf("isFunctionTemplate(%+v) = %v, want %v", tc.s, got, tc.want)
		}
	}
	app := pavedRoadApplication("thumbs", "", &scaffoldedRepo{TemplateName: "thumbnailer", TemplateTags: []string{"function"},
		CloneURL: "https://gitea.example.io/adhar/thumbs.git", ManifestPath: "deploy"}, "")
	if typ, _, _ := unstructured.NestedString(app.Object, "spec", "parameters", "type"); typ != "function" || app.GetLabels()["adhar.io/workload"] != "function" {
		t.Errorf("a function-tagged template registered type %q, labels %v", typ, app.GetLabels())
	}
}

// The Console decides "batch" from the template's adhar.io/workload: batch
// label, a `springboot-batch-*` name or a `batch` tag (isBatchTemplate), and
// checks it BEFORE the function rule and the name regexes — a batch template is
// routinely named `*-service` and tagged `etl`. Until 2026-11-13 the CLI knew
// no batch at all: a springboot-batch-service scaffold registered as a plain
// `service` and lost its batch-jvm dashboard label, so the Application
// composition's $isBatch branch never fired.
func TestABatchTemplateIsRecognisedTheWayTheConsoleRecognisesIt(t *testing.T) {
	for _, tc := range []struct {
		s    scaffoldedRepo
		want bool
	}{
		{scaffoldedRepo{TemplateName: "springboot-batch-job"}, true},
		{scaffoldedRepo{TemplateName: "springboot-batch"}, true},
		{scaffoldedRepo{TemplateName: "Springboot-Batch-Partitioned", TemplateTags: []string{"java"}}, true},
		{scaffoldedRepo{TemplateName: "nightly-import", TemplateTags: []string{"java", " Batch "}}, true},
		{scaffoldedRepo{TemplateName: "nightly-import", TemplateLabels: map[string]string{"adhar.io/workload": "batch"}}, true},
		{scaffoldedRepo{TemplateName: "springboot-batching-service", TemplateTags: []string{"batches"}}, false},
		{scaffoldedRepo{TemplateName: "springboot-batchless-api"}, false},
		{scaffoldedRepo{TemplateName: " springboot-batch-job\n"}, true},
		{scaffoldedRepo{TemplateName: "legacy-springboot-batch-job"}, false},
		{scaffoldedRepo{TemplateName: "springboot-web-service", TemplateTags: []string{"java", "springboot"}}, false},
	} {
		if got := isBatchTemplate(&tc.s); got != tc.want {
			t.Errorf("isBatchTemplate(%+v) = %v, want %v", tc.s, got, tc.want)
		}
	}

	// An etl-tagged batch template is batch, not data; a *-service name is
	// batch, not service; and the function rule does not outrank it.
	for name, s := range map[string]*scaffoldedRepo{
		"etl":     {TemplateName: "springboot-batch-service", TemplateTags: []string{"etl", "batch"}},
		"service": {TemplateName: "springboot-batch-service", TemplateTags: []string{"java"}},
		"tag":     {TemplateName: "function-shaped-worker", TemplateTags: []string{"batch"}},
	} {
		if got := scaffoldWorkload(s); got != "batch" {
			t.Errorf("%s: scaffoldWorkload = %q, want batch", name, got)
		}
	}
	// What the template DECLARES comes first (inferFamily): a template labelled
	// function is a function whatever it is named or tagged.
	if got := scaffoldWorkload(&scaffoldedRepo{TemplateName: "springboot-batch-job", TemplateTags: []string{"batch"},
		TemplateLabels: map[string]string{"adhar.io/workload": "function"}}); got != "function" {
		t.Errorf("a template declaring adhar.io/workload: function registered as %q", got)
	}
	if got := scaffoldWorkload(&scaffoldedRepo{TemplateName: "go-web-service", TemplateTags: []string{"go"}}); got != "" {
		t.Errorf("a plain service registered as %q", got)
	}

	app := pavedRoadApplication("nightly", "", &scaffoldedRepo{TemplateName: "springboot-batch-service", TemplateTags: []string{"java", "etl"},
		TemplateLabels: map[string]string{"adhar.io/dashboard-type": "batch-jvm"},
		CloneURL:       "https://gitea.example.io/adhar/nightly.git", ManifestPath: "deploy"}, "")
	typ, _, _ := unstructured.NestedString(app.Object, "spec", "parameters", "type")
	if labels := app.GetLabels(); typ != "batch" || labels["adhar.io/workload"] != "batch" || labels["adhar.io/dashboard-type"] != "batch-jvm" {
		t.Errorf("a batch template registered type %q, labels %v; want type batch, workload batch, dashboard batch-jvm", typ, labels)
	}

	// An old control plane refuses `batch` the way it refused `function`; the
	// retry drops the field and keeps the labels, which still say batch.
	var sent []bool
	err := applyWithTypeRetry(app, func(obj *unstructured.Unstructured) error {
		_, has, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "parameters", "type")
		sent = append(sent, has)
		if has {
			return errors.New(`Application.platform.adhar.io "nightly" is invalid: spec.parameters.type: Unsupported value: "batch"`)
		}
		return nil
	})
	if err != nil || len(sent) != 2 || !sent[0] || sent[1] {
		t.Errorf("batch refusal: err %v, type sent per attempt %v", err, sent)
	}
	if labels := app.GetLabels(); labels["adhar.io/workload"] != "batch" || labels["adhar.io/dashboard-type"] != "batch-jvm" {
		t.Errorf("the retry dropped the batch labels: %v", labels)
	}
}

// agent-service declares a `project` parameter and renders it into
// deploy/base/agentworkload.yaml; the Console registers the Application in
// that same project (body.params.project). The CLI used --project alone, so
// `--param project=payments` rendered payments into the repository and
// registered the Application in adhar.
func TestTheProjectComesFromTheTemplateParameterTheConsoleReads(t *testing.T) {
	var withProject, without backstageTemplate
	_ = yaml.Unmarshal([]byte(`
metadata: {name: agent-service}
spec:
  parameters:
    - properties:
        name: {type: string}
        project: {type: string, default: adhar}
`), &withProject)
	_ = yaml.Unmarshal([]byte("metadata: {name: go-web-service}\nspec: {parameters: [{properties: {name: {type: string}}}]}\n"), &without)

	for _, tc := range []struct {
		name         string
		tpl          *backstageTemplate
		flag         string
		flagSet      bool
		param        string
		want         string
		wantRendered string
		wantErr      bool
	}{
		{"template default", &withProject, "adhar", false, "", "adhar", "", false},
		{"--param", &withProject, "adhar", false, "payments", "payments", "payments", false},
		{"--project renders too", &withProject, "payments", true, "", "payments", "payments", false},
		{"both, agreeing", &withProject, "payments", true, "payments", "payments", "payments", false},
		{"both, disagreeing", &withProject, "payments", true, "billing", "", "", true},
		{"no parameter: --project", &without, "payments", true, "", "payments", "", false},
		{"no parameter: default", &without, "adhar", false, "", "adhar", "", false},
		{"`default` is the platform project", &without, "default", true, "", "adhar", "", false},
	} {
		overrides := map[string]string{}
		if tc.param != "" {
			overrides["project"] = tc.param
		}
		got, err := scaffoldProject(tc.tpl, tc.flag, tc.flagSet, overrides)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: project %q, err %v; want %q (error %v)", tc.name, got, err, tc.want, tc.wantErr)
		}
		if !tc.wantErr && overrides["project"] != tc.wantRendered {
			t.Errorf("%s: the template renders project %q, want %q", tc.name, overrides["project"], tc.wantRendered)
		}
	}
}

// The Console's NAME_RE, checked before the repository exists: the API server
// would refuse the Application only after the repository had been committed.
func TestAnApplicationNameIsCheckedBeforeAnythingIsCreated(t *testing.T) {
	for _, ok := range []string{"qua-service", "abc", "a1-b2", strings.Repeat("a", 63)} {
		if err := validateScaffoldName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"My_App", "ab", "1app", "app-", "app.name", strings.Repeat("a", 64), ""} {
		if err := validateScaffoldName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The Console's applyApplication retry: an XRD without spec.parameters.type,
// or without its `function` value, refuses the whole object; that one refusal
// is retried without the field, and nothing else is.
func TestOnlyATypeRefusalIsRetriedWithoutTheType(t *testing.T) {
	newApp := func() *unstructured.Unstructured {
		return pavedRoadApplication("deno-func", "", &scaffoldedRepo{TemplateName: "function-deno-service",
			CloneURL: "https://gitea.example.io/adhar/deno-func.git", ManifestPath: "deploy"}, "")
	}
	for _, refusal := range []string{
		`.spec.parameters.type: field not declared in schema`,
		`Application.platform.adhar.io "deno-func" is invalid: spec.parameters.type: Unsupported value: "function"`,
	} {
		app := newApp()
		var sent []bool
		err := applyWithTypeRetry(app, func(obj *unstructured.Unstructured) error {
			_, has, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "parameters", "type")
			sent = append(sent, has)
			if has {
				return errors.New(refusal)
			}
			return nil
		})
		if err != nil || len(sent) != 2 || !sent[0] || sent[1] {
			t.Errorf("%s: err %v, type sent per attempt %v", refusal, err, sent)
		}
		if app.GetLabels()["adhar.io/workload"] != "function" {
			t.Errorf("the retry dropped the workload label")
		}
	}
	calls := 0
	err := applyWithTypeRetry(newApp(), func(*unstructured.Unstructured) error {
		calls++
		return errors.New(`Apply failed with 1 conflict: conflict with "argocd-controller": .spec.parameters.source.repoURL`)
	})
	if err == nil || calls != 1 {
		t.Errorf("a conflict was retried (%d calls) or swallowed (%v)", calls, err)
	}
}

// Every skeleton links its repository as https://${{ values.giteaHost }}/…,
// and utils.GiteaBaseUrl spells the default port: CLI-scaffolded repositories
// read https://gitea.platform.adhar.io:443/… where the Console's read
// https://gitea.platform.adhar.io/…, down to the Application's repoURL through
// the .adhar/app.yaml descriptor (reactapp, 2026-11-03).
func TestTheSkeletonSeesThePublicGiteaHostTheConsoleWrites(t *testing.T) {
	var tpl backstageTemplate
	_ = yaml.Unmarshal([]byte(`
metadata: {name: react-spa-app}
spec:
  parameters: [{properties: {name: {type: string}}}]
  steps:
    - action: fetch:template
      input:
        url: ./skeleton
        values:
          giteaHost: ${{ (parameters.repoUrl | parseRepoUrl).host }}
`), &tpl)
	_, spec, _ := tpl.fetchTemplateStep()
	for base, wantHost := range map[string]string{
		"https://gitea.platform.adhar.io:443":   "gitea.platform.adhar.io",
		"https://gitea.adhar.localtest.me:8443": "gitea.adhar.localtest.me:8443",
	} {
		ctxVals, err := scaffoldContext(&tpl, spec, base, "reactapp", "dev", nil)
		if err != nil {
			t.Fatal(err)
		}
		values := ctxVals["values"].(map[string]any)
		// gitOwner and repoName are pinned to the repository the scaffold
		// creates, as the Console pins them, whatever the template derives.
		if values["giteaHost"] != wantHost || values["gitOwner"] != "adhar" || values["repoName"] != "reactapp" {
			t.Errorf("%s: giteaHost %v gitOwner %v repoName %v", base, values["giteaHost"], values["gitOwner"], values["repoName"])
		}
		if values["platformBaseDomain"] == "" || strings.Contains(stringify(values["hostname"]), ":") {
			t.Errorf("%s: hostname %v", base, values["hostname"])
		}
	}
}
