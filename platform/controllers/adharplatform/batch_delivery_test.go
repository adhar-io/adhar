package adharplatform

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A batch application travels the normal delivery path (design 0025 §6.1, §6.6), and
// three things in it were written for processes that keep running: the verification
// counted pods with Ready=False, which every completed run reports; the resilience
// gate waits for a Rollout or Deployment; the dashboard shows golden signals of a
// server. These hold the batch branch of each.

func bdProjectDocs(t *testing.T) []map[string]any {
	t.Helper()
	project := readYAMLDocs(t, filepath.Join(fdFixtureDir, "project.yaml"))[0]
	var items []any
	for _, a := range readYAMLDocs(t, filepath.Join(fdFixtureDir, "required-applications.yaml")) {
		items = append(items, map[string]any{"resource": a})
	}
	// Two batch applications beside the fixture's function and service: one by type, one
	// by label, which a scaffolder can stamp without a schema change.
	for name, app := range map[string]map[string]any{
		"importer": {
			"spec": map[string]any{"parameters": map[string]any{"type": "batch", "project": "adhar"}},
		},
		"reconciler": {
			"labels": map[string]any{"adhar.io/workload": "batch"},
			"spec":   map[string]any{"parameters": map[string]any{"project": "adhar"}},
		},
	} {
		meta := map[string]any{"name": name, "namespace": "adhar-system"}
		if l, ok := app["labels"]; ok {
			meta["labels"] = l
		}
		items = append(items, map[string]any{"resource": map[string]any{
			"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Application",
			"metadata": meta,
			"spec":     app["spec"],
			"status": map[string]any{"delivery": map[string]any{
				"promote": true, "image": "harbor-core.adhar-system.svc.cluster.local/library/" + name,
				"repoURL": "http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/" + name + ".git", "path": "deploy",
			}},
		}})
	}
	return renderGoTemplate(t, map[string]any{
		"observed":       map[string]any{"composite": map[string]any{"resource": project}},
		"extraResources": map[string]any{"applications": map[string]any{"items": items}},
	}, "compositions", "project", "local.yaml")
}

func TestABatchApplicationIsVerifiedByWhatItsRunsDid(t *testing.T) {
	docs := bdProjectDocs(t)
	for _, env := range []string{"dev", "test", "prod"} {
		at := composedManifest(docs, "AnalysisTemplate", env+"-health")
		if at == nil {
			t.Fatalf("no AnalysisTemplate %s-health", env)
		}
		metrics := map[string]map[string]any{}
		for _, m := range dig(at, "spec", "metrics").([]any) {
			mm := m.(map[string]any)
			metrics[fmt.Sprint(mm["name"])] = mm
		}
		query := func(name string) string { return fmt.Sprint(dig(metrics[name], "provider", "prometheus", "query")) }
		ens := "adhar-" + env
		for _, app := range []string{"importer", "reconciler"} {
			// None of what was written for long-running processes applies.
			for _, gone := range []string{"-pods-ready", "-rollout-healthy", "-no-restarts", "-function-healthy", "-resilience"} {
				if _, ok := metrics[app+gone]; ok {
					t.Errorf("%s/%s has the metric %s, which a process that finishes can only ever fail or pass without measuring", env, app, gone)
				}
			}
			for name, wants := range map[string][]string{
				app + "-batchjob-healthy": {fmt.Sprintf(`argocd_app_info{name="adhar-%s-%s",health_status="Degraded"}`, app, env), `health_status!="Healthy"`},
				// Found by the label the composition stamps, never by a name: an
				// application may own several BatchJobs.
				app + "-runs-not-failing": {fmt.Sprintf(`kube_job_failed{namespace=%q,condition="true"} == 1`, ens),
					fmt.Sprintf(`kube_job_labels{namespace=%q,label_adhar_io_application=%q}`, ens, app), "time() - kube_job_created", "< 480"},
				// restartPolicy: Never makes the kill its current state; the other series alone never fires.
				app + "-no-oom": {fmt.Sprintf(`kube_pod_container_status_terminated_reason{namespace=%q,reason="OOMKilled"}`, ens),
					fmt.Sprintf(`kube_pod_labels{namespace=%q,label_adhar_io_application=%q}`, ens, app), "time() - kube_pod_created", "< 480"},
			} {
				if _, ok := metrics[name]; !ok {
					t.Errorf("%s-health has no %s metric", env, name)
					continue
				}
				for _, w := range wants {
					if !strings.Contains(query(name), w) {
						t.Errorf("%s-health %s does not query %s:\n%s", env, name, w, query(name))
					}
				}
				// A quiet window is not a failure: no run started while this was watching.
				if !strings.Contains(query(name), "or vector(0)") {
					t.Errorf("%s-health %s fails on silence: %s", env, name, query(name))
				}
				if dig(metrics[name], "successCondition") != "result[0] < 1" {
					t.Errorf("%s-health %s succeeds on %v", env, name, dig(metrics[name], "successCondition"))
				}
			}
		}
		// Everything else is untouched.
		if _, ok := metrics["transaction-service-rollout-healthy"]; !ok {
			t.Errorf("%s-health lost transaction-service's Rollout check", env)
		}
		if _, ok := metrics["test-func-function-healthy"]; !ok {
			t.Errorf("%s-health lost the function's check", env)
		}
		if _, ok := metrics["transaction-service-resilience"]; ok != (env == "test") {
			t.Errorf("%s-health: transaction-service's gate present=%v", env, ok)
		}
	}
}

// The argocd health check a failed night cannot reach: the verification reads the
// Application's Degraded, and the BatchJob's Lua reads nothing about runs
// (TestArgoCDAssessesBatchJobHealth), so a failed run gates only the promotion
// that it happened inside (A1.4).

func TestABatchApplicationsDashboardIsBatchJVM(t *testing.T) {
	xr := readYAMLDocs(t, filepath.Join(bjFixtureDir, "application-batch.yaml"))[0]
	render := func(x map[string]any) string {
		t.Helper()
		docs := renderGoTemplate(t, map[string]any{"observed": map[string]any{
			"composite": map[string]any{"resource": x},
			"resources": fnObserved(t, readYAMLDocs(t, filepath.Join(fdFixtureDir, "observed-deployed.yaml"))),
		}}, "compositions", "apps", "argocd-multienv.yaml")
		cm := composedManifest(docs, "ConfigMap", "app-dashboard-importer")
		if cm == nil {
			t.Fatal("no dashboard ConfigMap for importer")
		}
		return fmt.Sprint(dig(cm, "data", "importer.json"))
	}
	body := render(xr)
	var d struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("the dashboard is not JSON: %v", err)
	}
	if !strings.HasPrefix(d.Description, "batch-jvm dashboard") {
		t.Errorf("a batch application got: %s", d.Description)
	}
	for _, want := range []string{`label_adhar_io_application=\"importer\"`, "kube_job_failed", "spring_batch_item_read"} {
		if !strings.Contains(body, want) {
			t.Errorf("the batch dashboard never reads %s", want)
		}
	}
	// By label alone, and over a dashboard.type naming something else.
	byLabel := fnWithParams(xr, map[string]any{"type": nil, "dashboard": map[string]any{"type": "backend-jvm"}})
	if err := json.Unmarshal([]byte(render(byLabel)), &d); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.Description, "batch-jvm dashboard") {
		t.Errorf("the label adhar.io/workload: batch got: %s", d.Description)
	}

	// `batch` is accepted by the schema (the strict enum is what would refuse it).
	xrd := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/apps.xrd.yaml"))[0]
	v0 := dig(xrd, "spec", "versions").([]any)[0].(map[string]any)
	typ := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties", "type", "enum")
	if !strings.Contains(fmt.Sprint(typ), "batch") {
		t.Errorf("Application type enum = %v", typ)
	}
}

// §10.2: in prod the team is `view`, and operating a job is a separate right, bound
// by the Environment and controlled by the Project. A RoleBinding the provider may not
// create is an Object that silently stays unready.
func TestEnvironmentBindsTheBatchOperatorRoleInProdOnly(t *testing.T) {
	render := func(params map[string]any) []map[string]any {
		t.Helper()
		p := map[string]any{"name": "shop-prod", "tier": "prod", "environment": "prod", "organisation": "acme", "team": "payments", "project": "shop", "access": "view"}
		for k, v := range params {
			if v == nil {
				delete(p, k)
			} else {
				p[k] = v
			}
		}
		return renderGoTemplate(t, map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": map[string]any{
			"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Environment",
			"metadata": map[string]any{"name": "shop-prod", "namespace": "adhar-system"},
			"spec":     map[string]any{"parameters": p},
		}}}}, "compositions", "env", "local.yaml")
	}
	binding := func(docs []map[string]any) map[string]any {
		for _, d := range docs {
			if dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name") == "team-batch-operator" {
				return d
			}
		}
		return nil
	}
	b := binding(render(nil))
	if b == nil {
		t.Fatal("a prod environment does not bind the batch operator role by default (D8: team)")
	}
	m, _ := dig(b, "spec", "forProvider", "manifest").(map[string]any)
	if dig(m, "roleRef", "name") != "adhar-batch-operator" || dig(m, "metadata", "namespace") != "shop-prod" || m["kind"] != "RoleBinding" {
		t.Errorf("the binding is %v", m)
	}
	subjects, _ := m["subjects"].([]any)
	if len(subjects) != 1 || dig(subjects[0].(map[string]any), "name") != "oidc:ws-team-acme.payments" {
		t.Errorf("the binding names %v", subjects)
	}
	if binding(render(map[string]any{"batchOperations": "none"})) != nil {
		t.Error("batchOperations: none still bound the role")
	}
	for _, tier := range []string{"dev", "test"} {
		if binding(render(map[string]any{"tier": tier, "environment": tier, "access": "admin"})) != nil {
			t.Errorf("a %s environment bound the operator role; the team is admin there", tier)
		}
	}

	// The Project hands its choice down.
	found := false
	project := readConfigurationFile(t, "compositions", "project", "local.yaml")
	for _, l := range strings.Split(project, "\n") {
		if strings.Contains(l, `batchOperations: {{ dig "batchOperations" "team" $p }}`) {
			found = true
		}
	}
	if !found {
		t.Error("the Project composition does not pass batchOperations to its Environments")
	}
	for _, f := range []string{"xrd/project.xrd.yaml", "xrd/env.xrd.yaml"} {
		x := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), f))[0]
		v0 := dig(x, "spec", "versions").([]any)[0].(map[string]any)
		p := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties", "batchOperations").(map[string]any)
		if fmt.Sprint(p["enum"]) != "[team none]" || p["default"] != "team" {
			t.Errorf("%s batchOperations = %v", f, p)
		}
	}

	// The provider must be allowed to bind what it binds, and no more.
	var bound []string
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "providers/provider-runtime-rbac.yaml")) {
		rules, _ := d["rules"].([]any)
		for _, r := range rules {
			rm := r.(map[string]any)
			if fmt.Sprint(rm["verbs"]) == "[bind]" {
				names, _ := rm["resourceNames"].([]any)
				if len(names) == 0 {
					t.Errorf("%v grants bind on every ClusterRole, cluster-admin included", d["metadata"])
				}
				for _, n := range names {
					bound = append(bound, fmt.Sprint(n))
				}
			}
		}
	}
	if !fnContains(bound, "adhar-batch-operator") {
		t.Errorf("provider-kubernetes may not bind adhar-batch-operator (it may bind %v): the API server refuses it as privilege escalation", bound)
	}
}
