package adharplatform

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// A Function travels the platform's normal delivery path: a repository from a
// function-* template → app-ci → Kargo promotes its image digest into
// <path>/envs/<env> → Argo CD syncs the Function XR into the environment →
// Knative. The Application composition (compositions/apps/argocd-multienv.yaml)
// and the project's shared promotion chain (compositions/project/local.yaml)
// were written for Rollouts, and test-func showed three ways that went wrong
// (2026-10-26):
//
//   - every environment read Healthy while nothing was deployed — Argo CD calls an
//     Application with no resources Healthy, and one it cannot compare too;
//   - the seed Job crashed on the missing overlay every hour behind a Ready XR;
//   - Kargo's verification matched pods named <app>-<rs>-<pod> and Rollouts,
//     neither of which a Knative Service has, so it passed without measuring.
//
// The fixtures in testdata/function-delivery are what `crossplane render` takes
// (TestFunctionDeliveryRendersWithCrossplane runs them through the real
// functions); the other tests render the same templates in-process.

const fdFixtureDir = "testdata/function-delivery"

func fdApplicationRequest(t *testing.T, observed string) map[string]any {
	t.Helper()
	xr := readYAMLDocs(t, filepath.Join(fdFixtureDir, "application-function.yaml"))
	if len(xr) != 1 {
		t.Fatal("application-function.yaml must hold one Application")
	}
	return map[string]any{"observed": map[string]any{
		"composite": map[string]any{"resource": xr[0]},
		"resources": fnObserved(t, readYAMLDocs(t, filepath.Join(fdFixtureDir, observed))),
	}}
}

func fdConditions(docs []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, d := range docs {
		if d["kind"] != "ClaimConditions" {
			continue
		}
		conds, _ := d["conditions"].([]any)
		for _, c := range conds {
			if m, ok := c.(map[string]any); ok {
				out[fmt.Sprint(m["type"])] = m
			}
		}
	}
	return out
}

func fdJob(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == "Job" && dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name") == "stage-init" {
			return d
		}
	}
	t.Fatal("the Application composition no longer composes the stage-init Job")
	return nil
}

// An environment counts as healthy only when it is Synced, Healthy and deploys
// something; the status message names every one that is not, and why.
func TestAFunctionsEnvironmentsAreHealthyOnlyWhenTheyDeploySomething(t *testing.T) {
	docs := renderGoTemplate(t, fdApplicationRequest(t, "observed-undeployed.yaml"), "compositions", "apps", "argocd-multienv.yaml")
	st := docOfKind(docs, "Application")
	if phase := dig(st, "status", "phase"); phase != "Pending" {
		t.Errorf("test-func with nothing deployed anywhere is %v, want Pending", phase)
	}
	msg := fmt.Sprint(dig(st, "status", "message"))
	for _, want := range []string{
		"0/3 environments healthy",
		"dev deploys nothing (stage/dev renders no manifests)",
		"test cannot be compared (", "unable to resolve 'stage/test'",
		"prod cannot be compared (",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the status message does not say %q: %s", want, msg)
		}
	}

	docs = renderGoTemplate(t, fdApplicationRequest(t, "observed-deployed.yaml"), "compositions", "apps", "argocd-multienv.yaml")
	st = docOfKind(docs, "Application")
	if dig(st, "status", "phase") != "Healthy" || dig(st, "status", "message") != "3/3 environments healthy" {
		t.Errorf("a Function running in every environment reads %v / %v, want Healthy / 3/3 environments healthy",
			dig(st, "status", "phase"), dig(st, "status", "message"))
	}
}

// A repository without <path>/envs/<env> is a condition on the XR that names the
// problem — not a Job crashing every hour behind a Ready XR.
func TestAMissingOverlayIsAnXRConditionNotAnHourlyCrash(t *testing.T) {
	docs := renderGoTemplate(t, fdApplicationRequest(t, "observed-undeployed.yaml"), "compositions", "apps", "argocd-multienv.yaml")
	ov := fdConditions(docs)["Overlays"]
	if ov == nil || ov["status"] != "False" || ov["reason"] != "OverlayMissing" {
		t.Fatalf("a missing overlay is reported as %v, want Overlays=False/OverlayMissing", ov)
	}
	for _, want := range []string{"http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/test-func.git on main",
		"deploy/envs/<env>", "not deploying: dev, test, prod", "adhar-system/test-func-stage-init"} {
		if !strings.Contains(fmt.Sprint(ov["message"]), want) {
			t.Errorf("the Overlays message lacks %q: %v", want, ov["message"])
		}
	}
	docs = renderGoTemplate(t, fdApplicationRequest(t, "observed-deployed.yaml"), "compositions", "apps", "argocd-multienv.yaml")
	if ov := fdConditions(docs)["Overlays"]; ov == nil || ov["status"] != "True" || ov["reason"] != "Seeded" {
		t.Errorf("once the seed Job completed, Overlays is %v, want True/Seeded", ov)
	}

	// The Job fails on its first missing/broken overlay instead of retrying, and
	// is new under a new name (its pod template is immutable).
	job := fdJob(t, docs)
	if dig(job, "metadata", "name") != "test-func-stage-init" {
		t.Errorf("the seed Job is %v, want test-func-stage-init", dig(job, "metadata", "name"))
	}
	if p := dig(job, "spec", "template", "spec", "restartPolicy"); p != "Never" {
		t.Errorf("the seed Job's restartPolicy is %v; podFailurePolicy requires Never", p)
	}
	rules := fmt.Sprint(dig(job, "spec", "podFailurePolicy", "rules"))
	for _, want := range []string{"action:FailJob onExitCodes:map[containerName:seed operator:In values:[3]]",
		"action:FailJob onExitCodes:map[containerName:seed operator:In values:[4]]"} {
		if !strings.Contains(rules, want) {
			t.Errorf("the seed Job's podFailurePolicy lacks %q: %s", want, rules)
		}
	}

	// And the script exits with exactly those codes. Run it with git and
	// kustomize stood in for: a clone copies a fixture checkout, ls-remote finds
	// the branches FAKE_BRANCHES names, kustomize fails an overlay marked BROKEN.
	cs, _ := dig(job, "spec", "template", "spec", "containers").([]any)
	args, _ := dig(cs[0].(map[string]any), "args").([]any)
	script := fmt.Sprint(args[0])
	bin := t.TempDir()
	writeFiles(t, bin, map[string]string{
		"git": `#!/bin/sh
while [ "$1" = "-c" ]; do shift 2; done
cmd="$1"; shift
for a in "$@"; do last="$a"; done
case "$cmd" in
  clone) cp -R "$FAKE_SRC" "$last" ;;
  ls-remote) case " $FAKE_BRANCHES " in *" $last "*) exit 0 ;; *) exit 2 ;; esac ;;
  push) echo "pushed $last" >> "$FAKE_LOG" ;;
esac
exit 0
`,
		"kustomize": `#!/bin/sh
grep -q BROKEN "$2/kustomization.yaml" && { echo "Error: accumulating resources" >&2; exit 1; }
cat "$2/kustomization.yaml"
`,
	})
	for _, f := range []string{"git", "kustomize"} {
		if err := os.Chmod(filepath.Join(bin, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	overlay := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [../../base]\n"
	for _, c := range []struct {
		name     string
		files    map[string]string
		branches string
		code     int
		pushes   int
	}{
		{"no overlays at all (a flat deploy/)", map[string]string{"deploy/function.yaml": "x"}, "", 3, 0},
		{"one overlay does not build", map[string]string{"deploy/envs/dev/kustomization.yaml": overlay,
			"deploy/envs/test/kustomization.yaml": "BROKEN", "deploy/envs/prod/kustomization.yaml": overlay}, "", 4, 2},
		{"every overlay present", map[string]string{"deploy/envs/dev/kustomization.yaml": overlay,
			"deploy/envs/test/kustomization.yaml": overlay, "deploy/envs/prod/kustomization.yaml": overlay}, "", 0, 3},
		{"existing branches are the promotions'", map[string]string{"deploy/function.yaml": "x"},
			"stage/dev stage/test stage/prod", 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			src, log := t.TempDir(), filepath.Join(t.TempDir(), "pushes")
			writeFiles(t, src, c.files)
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "REPO=http://gitea.example/adhar/x.git",
				"REVISION=main", "OVERLAYS=deploy", "ENVS=dev test prod", "HOME="+t.TempDir(),
				"GITEA_USER=u", "GITEA_PASSWORD=p", "FAKE_SRC="+src, "FAKE_BRANCHES="+c.branches, "FAKE_LOG="+log)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != c.code {
				t.Errorf("the seed script exited %d, want %d:\n%s", code, c.code, out.String())
			}
			pushed, _ := os.ReadFile(log)
			if n := strings.Count(string(pushed), "pushed"); n != c.pushes {
				t.Errorf("the seed script pushed %d branch(es), want %d:\n%s", n, c.pushes, out.String())
			}
		})
	}
}

// The per-application dashboard selects a Function's pods by Knative's
// revision naming.
func TestAFunctionsDashboardSelectsKnativeRevisionPods(t *testing.T) {
	docs := renderGoTemplate(t, fdApplicationRequest(t, "observed-deployed.yaml"), "compositions", "apps", "argocd-multienv.yaml")
	cm := composedManifest(docs, "ConfigMap", "app-dashboard-test-func")
	if cm == nil {
		t.Fatal("no dashboard ConfigMap for test-func")
	}
	body := fmt.Sprint(dig(cm, "data", "test-func.json"))
	if !strings.Contains(body, `test-func-[0-9]{5}-deployment-[a-z0-9]{6,10}-[a-z0-9]{5}`) {
		t.Error("the Function's dashboard does not select Knative revision pods (<name>-<generation>-deployment-…)")
	}
	if strings.Contains(body, `test-func-[a-z0-9]{6,10}-[a-z0-9]{5}`) {
		t.Error("the Function's dashboard still selects Deployment-shaped pod names, which a Knative Service never has")
	}
}

// The project's verification of a Function measures what Knative reports, and
// passes on silence: a function scaled to zero has no pod and no traffic.
func TestAFunctionIsVerifiedByWhatKnativeReports(t *testing.T) {
	project := readYAMLDocs(t, filepath.Join(fdFixtureDir, "project.yaml"))[0]
	var items []any
	for _, a := range readYAMLDocs(t, filepath.Join(fdFixtureDir, "required-applications.yaml")) {
		items = append(items, map[string]any{"resource": a})
	}
	docs := renderGoTemplate(t, map[string]any{
		"observed":       map[string]any{"composite": map[string]any{"resource": project}},
		"extraResources": map[string]any{"applications": map[string]any{"items": items}},
	}, "compositions", "project", "local.yaml")
	checkFunctionAnalysis(t, docs)
}

func checkFunctionAnalysis(t *testing.T, docs []map[string]any) {
	t.Helper()
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
		query := func(name string) string {
			return fmt.Sprint(dig(metrics[name], "provider", "prometheus", "query"))
		}
		ens := "adhar-" + env
		for _, fn := range []string{"test-func", "thumbnailer"} {
			if _, ok := metrics[fn+"-rollout-healthy"]; ok {
				t.Errorf("%s/%s is checked against rollout_info; a Knative Service has no Rollout, so that passes without measuring", env, fn)
			}
			if _, ok := metrics[fn+"-resilience"]; ok {
				t.Errorf("%s/%s runs the resilience gate, which waits for a Rollout or Deployment named %s and fails without one", env, fn, fn)
			}
			pods := fn + `-[0-9]{5}-deployment-[a-z0-9]+-[a-z0-9]+`
			for name, wants := range map[string][]string{
				fn + "-function-healthy": {fmt.Sprintf(`argocd_app_info{name="adhar-%s-%s",health_status="Degraded"}`, fn, env), `health_status!="Healthy"`, "[480s:30s]"},
				fn + "-pods-ready":       {fmt.Sprintf(`kube_pod_status_ready{namespace=%q,pod=~%q,condition="false"}`, ens, pods), "unless on(namespace, pod) kube_pod_deletion_timestamp", "> 480"},
				fn + "-no-restarts":      {fmt.Sprintf(`kube_pod_container_status_restarts_total{namespace=%q,pod=~%q`, ens, pods)},
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
				if !strings.Contains(query(name), "or vector(0)") {
					t.Errorf("%s-health %s fails on silence; a function scaled to zero has no series at all", env, name)
				}
			}
		}
		// The default 5xx check reads Knative's activator, and passes with no traffic.
		eq := query("test-func-error-rate")
		for _, w := range []string{`job="activator"`, fmt.Sprintf(`k8s_namespace_name=%q`, ens), `kn_service_name="test-func"`,
			`http_response_status_code=~"5.."`, "> 0)) or vector(0)"} {
			if !strings.Contains(eq, w) {
				t.Errorf("%s-health test-func-error-rate does not use %s: %s", env, w, eq)
			}
		}
		if q := query("thumbnailer-error-rate"); q != fmt.Sprintf(`my_errors{namespace=%q}`, ens) ||
			dig(metrics["thumbnailer-error-rate"], "successCondition") != "result[0] < 0.05" {
			t.Errorf("%s-health ignores the application's own error-rate query and threshold: %s", env, q)
		}
		// An ordinary service keeps the Rollout checks — and in test, the gate.
		if _, ok := metrics["transaction-service-rollout-healthy"]; !ok {
			t.Errorf("%s-health lost transaction-service's Rollout check", env)
		}
		if _, ok := metrics["transaction-service-resilience"]; ok != (env == "test") {
			t.Errorf("%s-health: transaction-service's resilience gate present=%v, want %v", env, ok, env == "test")
		}
	}
}

// The same fixtures through the real function containers: the path Crossplane
// takes, so a difference between the template engines cannot hide.
func TestFunctionDeliveryRendersWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	render := func(args ...string) []map[string]any {
		t.Helper()
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, append([]string{"render"}, args...)...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("crossplane render %v: %v\n%s", args, err, stderr.String())
		}
		var docs []map[string]any
		for _, d := range strings.Split(stdout.String(), "\n---") {
			var m map[string]any
			if err := yaml.Unmarshal([]byte(d), &m); err == nil && len(m) > 0 {
				docs = append(docs, m)
			}
		}
		return docs
	}
	functions := filepath.Join(fnFixtureDir, "functions.yaml")
	app := filepath.Join(controlPlaneDir(t), "compositions", "apps", "argocd-multienv.yaml")

	docs := render(filepath.Join(fdFixtureDir, "application-function.yaml"), app, functions,
		"--observed-resources="+filepath.Join(fdFixtureDir, "observed-undeployed.yaml"))
	var xr map[string]any
	for _, d := range docs {
		if d["kind"] == "Application" && d["apiVersion"] == "platform.adhar.io/v1alpha1" {
			xr = d
		}
	}
	if xr == nil {
		t.Fatal("crossplane render returned no Application XR")
	}
	found := false
	for _, c := range dig(xr, "status", "conditions").([]any) {
		m := c.(map[string]any)
		if m["type"] == "Overlays" {
			found = true
			if m["status"] != "False" || m["reason"] != "OverlayMissing" {
				t.Errorf("rendered Overlays condition = %v", m)
			}
		}
	}
	if !found {
		t.Error("the rendered XR carries no Overlays condition")
	}
	if !strings.HasPrefix(fmt.Sprint(dig(xr, "status", "message")), "0/3 environments healthy; dev deploys nothing") {
		t.Errorf("rendered status.message = %v", dig(xr, "status", "message"))
	}
	seen := false
	for _, d := range docs {
		if d["kind"] == "Job" {
			seen = true
			if dig(d, "metadata", "name") != "test-func-stage-init" || dig(d, "spec", "podFailurePolicy") == nil {
				t.Errorf("rendered seed Job = %v", d["metadata"])
			}
		}
	}
	if !seen {
		t.Error("crossplane render composed no seed Job")
	}

	project := filepath.Join(controlPlaneDir(t), "compositions", "project", "local.yaml")
	checkFunctionAnalysis(t, render(filepath.Join(fdFixtureDir, "project.yaml"), project, functions,
		"--required-resources="+filepath.Join(fdFixtureDir, "required-applications.yaml")))
}
