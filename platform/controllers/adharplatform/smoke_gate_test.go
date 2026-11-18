package adharplatform

// The smoke-run gate (design 0025 D6, A3.7): in a test-tier environment, every
// BatchJob of a verified application that declares `smoke` is run once per build by
// the Project composition's verification, as `adhar batch run <job> --smoke --wait`,
// and a build whose smoke run fails is never verified there, so it never reaches
// prod. Rendered in-process and through `crossplane render`. Each guard was
// mutation-tested: the behaviour reverted in the composition, the test seen failing,
// the fix restored.

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

const sgCLIImage = "ghcr.io/adhar-io/adhar:0.1.69"

func sgBatchJob(ns, name string, params map[string]any) map[string]any {
	p := map[string]any{"job": "j", "image": "harbor-core.adhar-system.svc.cluster.local/library/" + name, "database": map[string]any{"secretRef": name + "-db-app"}}
	for k, v := range params {
		p[k] = v
	}
	return map[string]any{"apiVersion": "platform.adhar.io/v1alpha1", "kind": "BatchJob",
		"metadata": map[string]any{"name": name, "namespace": ns}, "spec": map[string]any{"parameters": p}}
}

func sgManager(image string) map[string]any {
	return map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "adhar-controller-manager", "namespace": "adhar-system"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "manager", "image": image},
			// A sidecar after it: the image is the manager container's, not the last one's.
			map[string]any{"name": "kube-rbac-proxy", "image": "quay.io/brancz/kube-rbac-proxy:v0.18.0"},
		}}}}}
}

// sgDefaultBatchJobs: the importer application's own job with a smoke run and its
// housekeeping job without one; reconciler's nightly job (application named
// explicitly) with one; a service application's batch job with one; a job of an
// application that is not this project's; and the importer's dev copy.
func sgDefaultBatchJobs() []map[string]any {
	smoke := map[string]any{"parameters": map[string]any{"businessDate": "2026-11-03"}}
	return []map[string]any{
		sgBatchJob("adhar-test", "importer", map[string]any{"smoke": smoke, "timeouts": map[string]any{"activeDeadlineSeconds": 7200}}),
		sgBatchJob("adhar-test", "importer-housekeeping", map[string]any{"application": "importer"}),
		sgBatchJob("adhar-test", "reconciler-nightly", map[string]any{"application": "reconciler", "smoke": map[string]any{}}),
		sgBatchJob("adhar-test", "ledger", map[string]any{"application": "transaction-service", "smoke": smoke}),
		sgBatchJob("adhar-test", "stranger", map[string]any{"smoke": smoke}),
		sgBatchJob("adhar-dev", "importer", map[string]any{"smoke": smoke}),
		sgBatchJob("adhar-prod", "importer", map[string]any{"smoke": smoke}),
	}
}

// sgRender renders the Project composition with the fixture's applications plus the
// two batch applications of batch_delivery_test.go, the BatchJobs Crossplane would
// return for each requirement, and (when image is set) the controller-manager.
func sgRender(t *testing.T, batchJobs []map[string]any, image string) []map[string]any {
	t.Helper()
	project := readYAMLDocs(t, filepath.Join(fdFixtureDir, "project.yaml"))[0]
	var apps []any
	for _, a := range readYAMLDocs(t, filepath.Join(fdFixtureDir, "required-applications.yaml")) {
		apps = append(apps, map[string]any{"resource": a})
	}
	for _, name := range []string{"importer", "reconciler"} {
		apps = append(apps, map[string]any{"resource": map[string]any{
			"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": name, "namespace": "adhar-system"},
			"spec":     map[string]any{"parameters": map[string]any{"type": "batch", "project": "adhar"}},
			"status": map[string]any{"delivery": map[string]any{
				"promote": true, "image": "harbor-core.adhar-system.svc.cluster.local/library/" + name,
				"repoURL": "http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/" + name + ".git", "path": "deploy",
			}},
		}})
	}
	extra := map[string]any{"applications": map[string]any{"items": apps}}
	// Crossplane answers each batchjobs-<env> requirement with that namespace's
	// BatchJobs; the template still filters by namespace, since `crossplane render`
	// (and an older core) ignores the namespace of a label selector.
	var all []any
	for _, bj := range batchJobs {
		all = append(all, map[string]any{"resource": bj})
	}
	for _, env := range []string{"dev", "test", "prod"} {
		extra["batchjobs-"+env] = map[string]any{"items": all}
	}
	var mgr []any
	if image != "" {
		mgr = append(mgr, map[string]any{"resource": sgManager(image)})
	}
	extra["batchCLI"] = map[string]any{"items": mgr}
	return renderGoTemplate(t, map[string]any{
		"observed":       map[string]any{"composite": map[string]any{"resource": project}},
		"extraResources": extra,
	}, "compositions", "project", "local.yaml")
}

func sgMetrics(t *testing.T, docs []map[string]any, env string) map[string]map[string]any {
	t.Helper()
	at := composedManifest(docs, "AnalysisTemplate", env+"-health")
	if at == nil {
		t.Fatalf("no AnalysisTemplate %s-health", env)
	}
	out := map[string]map[string]any{}
	for _, m := range dig(at, "spec", "metrics").([]any) {
		mm := m.(map[string]any)
		out[fmt.Sprint(mm["name"])] = mm
	}
	return out
}

func TestTheSmokeGateRunsEveryDeclaredSmokeInTestOnly(t *testing.T) {
	docs := sgRender(t, sgDefaultBatchJobs(), sgCLIImage)
	metrics := sgMetrics(t, docs, "test")
	for name, w := range map[string]struct {
		app      string
		job      string
		deadline string
	}{
		"importer-smoke-run":           {"importer", "importer", "8100"},
		"reconciler-nightly-smoke-run": {"reconciler", "reconciler-nightly", "22500"},
		// A service application's BatchJob is verified the same way (§6.1).
		"ledger-smoke-run": {"transaction-service", "ledger", "22500"},
	} {
		m := metrics[name]
		if m == nil {
			t.Errorf("test-health has no %s metric", name)
			continue
		}
		job, _ := dig(m, "provider", "job").(map[string]any)
		if job == nil {
			t.Errorf("%s is not a Job metric: %v", name, m["provider"])
			continue
		}
		if dig(job, "metadata", "labels", "adhar.io/batch-smoke") != w.job || dig(job, "metadata", "labels", "adhar.io/application") != w.app {
			t.Errorf("%s labels = %v", name, dig(job, "metadata", "labels"))
		}
		// The gate is the client, never a run: a batch-job label would make the
		// integrity policy compare it with the run template and refuse it.
		for _, labels := range []any{dig(job, "metadata", "labels"), dig(job, "spec", "template", "metadata", "labels")} {
			if l, _ := labels.(map[string]any); l["adhar.io/batch-job"] != nil || l["adhar.io/workload"] != nil {
				t.Errorf("%s's gate Job is labelled as a batch run: %v", name, l)
			}
		}
		spec, _ := job["spec"].(map[string]any)
		if fmt.Sprint(spec["backoffLimit"]) != "0" || fmt.Sprint(spec["activeDeadlineSeconds"]) != w.deadline {
			t.Errorf("%s: backoffLimit %v, activeDeadlineSeconds %v (want 0 and the run's deadline + 900 = %s)", name, spec["backoffLimit"], spec["activeDeadlineSeconds"], w.deadline)
		}
		pod, _ := dig(spec, "template", "spec").(map[string]any)
		if pod["serviceAccountName"] != "adhar-batch-smoke" || pod["restartPolicy"] != "Never" {
			t.Errorf("%s pod = %v", name, pod)
		}
		c := pod["containers"].([]any)[0].(map[string]any)
		if c["image"] != sgCLIImage {
			t.Errorf("%s runs %v, want the controller-manager's own image %s", name, c["image"], sgCLIImage)
		}
		// D6, exactly: the CLI names the run <job>-smoke-<sha7>, reads smoke.parameters
		// and exits with the run's code.
		if got := fmt.Sprint(c["args"]); got != fmt.Sprintf("[batch run %s -n adhar-test --smoke --wait -o json]", w.job) {
			t.Errorf("%s args = %s", name, got)
		}
		if c["command"] != nil {
			t.Errorf("%s overrides the image's entrypoint (/adhar): %v", name, c["command"])
		}
	}
	for _, gone := range []string{"importer-housekeeping-smoke-run", "stranger-smoke-run"} {
		if metrics[gone] != nil {
			t.Errorf("test-health has %s: a job with no smoke declared, or not of this project's applications, must not run", gone)
		}
	}
	// Test only: dev and prod verify without a smoke run, though their copies declare one.
	for _, env := range []string{"dev", "prod"} {
		for name := range sgMetrics(t, docs, env) {
			if strings.HasSuffix(name, "-smoke-run") {
				t.Errorf("%s-health has %s; the smoke run is the test stage's (D6)", env, name)
			}
		}
	}
	// The rest of the batch verification is untouched.
	for _, keep := range []string{"importer-batchjob-healthy", "importer-runs-not-failing", "importer-no-oom", "transaction-service-resilience"} {
		if metrics[keep] == nil {
			t.Errorf("test-health lost %s", keep)
		}
	}
}

// The gate's identity: one ServiceAccount in the project namespace, where Kargo
// creates the Job, bound in each test-tier environment that has a smoke run to what
// `adhar batch run --wait` uses, and nowhere else.
func TestTheSmokeGateMayOperateRunsInTestOnly(t *testing.T) {
	docs := sgRender(t, sgDefaultBatchJobs(), sgCLIImage)
	if sa := composedManifest(docs, "ServiceAccount", "adhar-batch-smoke"); sa == nil || dig(sa, "metadata", "namespace") != "adhar" {
		t.Fatalf("the smoke gate's ServiceAccount = %v, want adhar/adhar-batch-smoke", sa)
	}
	bindings := map[string]map[string]any{}
	for _, d := range manifestsOfKind(docs, "RoleBinding") {
		if strings.HasPrefix(fmt.Sprint(dig(d, "metadata", "name")), "adhar-batch-smoke") {
			bindings[fmt.Sprintf("%v/%v", dig(d, "metadata", "namespace"), dig(d, "roleRef", "name"))] = d
		}
	}
	if len(bindings) != 2 || bindings["adhar-test/adhar-batch-operator"] == nil || bindings["adhar-test/view"] == nil {
		t.Fatalf("the smoke gate is bound %v, want adhar-batch-operator and view in adhar-test only", bindings)
	}
	for k, b := range bindings {
		subjects, _ := b["subjects"].([]any)
		if len(subjects) != 1 || dig(subjects[0].(map[string]any), "name") != "adhar-batch-smoke" || dig(subjects[0].(map[string]any), "namespace") != "adhar" {
			t.Errorf("%s subjects = %v", k, subjects)
		}
	}
	// adhar-batch-operator grants what the CLI does besides listing pods (view):
	// read the BatchJob and its template, create and watch the run.
	root, _ := filepath.Abs("../../..")
	var rules []any
	for _, d := range readYAMLDocs(t, filepath.Join(root, "platform/stack/packages/core/adhar-batch/manifests/rbac.yaml")) {
		if dig(d, "metadata", "name") == "adhar-batch-operator" {
			rules, _ = d["rules"].([]any)
		}
	}
	grants := func(group, resource, verb string) bool {
		for _, r := range rules {
			m := r.(map[string]any)
			if fnContains(toStrings(m["apiGroups"]), group) && fnContains(toStrings(m["resources"]), resource) && fnContains(toStrings(m["verbs"]), verb) {
				return true
			}
		}
		return false
	}
	for _, g := range [][3]string{{"platform.adhar.io", "batchjobs", "get"}, {"", "configmaps", "get"}, {"batch", "jobs", "create"}, {"batch", "jobs", "watch"}, {"batch.volcano.sh", "jobs", "create"}} {
		if !grants(g[0], g[1], g[2]) {
			t.Errorf("adhar-batch-operator does not grant %s on %s.%s, which the smoke run needs", g[2], g[1], g[0])
		}
	}

	// Without a smoke run anywhere there is no gate identity at all, and another
	// project's application declaring one does not make one here.
	docs = sgRender(t, []map[string]any{sgBatchJob("adhar-test", "importer", nil),
		sgBatchJob("adhar-test", "stranger", map[string]any{"smoke": map[string]any{}})}, sgCLIImage)
	if composedManifest(docs, "ServiceAccount", "adhar-batch-smoke") != nil {
		t.Error("a project with no smoke run of its own composes the smoke gate's ServiceAccount")
	}
	for name := range sgMetrics(t, docs, "test") {
		if strings.HasSuffix(name, "-smoke-run") {
			t.Errorf("test-health has %s without any smoke declared", name)
		}
	}
}

// What the composition asks Crossplane for: the BatchJobs of test-tier environments
// only, and the controller-manager whose image carries the CLI.
func TestTheSmokeGateReadsTestBatchJobsAndTheCLIImage(t *testing.T) {
	docs := sgRender(t, sgDefaultBatchJobs(), sgCLIImage)
	reqs := calRequirements(docs)
	bj, _ := reqs["batchjobs-test"].(map[string]any)
	if bj["kind"] != "BatchJob" || bj["namespace"] != "adhar-test" || bj["matchLabels"] == nil {
		t.Errorf("batchjobs-test = %v", bj)
	}
	for _, env := range []string{"dev", "prod"} {
		if reqs["batchjobs-"+env] != nil {
			t.Errorf("the composition asks for %s's BatchJobs, which have no smoke run", env)
		}
	}
	cli, _ := reqs["batchCLI"].(map[string]any)
	if cli["kind"] != "Deployment" || cli["matchName"] != "adhar-controller-manager" || cli["namespace"] != "adhar-system" {
		t.Errorf("batchCLI = %v", cli)
	}
	// Without the Deployment the gate fails closed and says why; it never passes a
	// build whose smoke run did not run.
	docs = sgRender(t, sgDefaultBatchJobs(), "")
	c := dig(sgMetrics(t, docs, "test")["importer-smoke-run"], "provider", "job", "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(c["args"]), "exit 1") || !strings.Contains(fmt.Sprint(c["args"]), "adhar-controller-manager") {
		t.Errorf("with no CLI image the gate runs %v %v", c["command"], c["args"])
	}
}

// Crossplane reads the controller-manager with its own identity (BatchJobs are an
// XRD's kind, which Crossplane grants itself), and a requirement it may not read is
// answered as nothing found: the gate would then fail closed on every build.
func TestTheSmokeGateExtraResourcesAreGrantedToCrossplane(t *testing.T) {
	ok := false
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "rbac/local-compose-rbac.yaml")) {
		if dig(d, "metadata", "name") != "crossplane-compose-local" {
			continue
		}
		rules, _ := d["rules"].([]any)
		for _, r := range rules {
			m := r.(map[string]any)
			if fnContains(toStrings(m["apiGroups"]), "apps") && fnContains(toStrings(m["resources"]), "deployments") &&
				fnContains(toStrings(m["verbs"]), "get") && fnContains(toStrings(m["verbs"]), "list") {
				ok = true
			}
		}
	}
	if !ok {
		t.Error("Crossplane may not read apps/deployments, so the smoke gate could never find the CLI image")
	}
}

// The real function containers: the smoke metric is what Crossplane composes.
func TestTheSmokeGateRendersWithCrossplane(t *testing.T) {
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
	dir := t.TempDir()
	write := func(name string, docs ...map[string]any) string {
		var parts []string
		for _, d := range docs {
			y, _ := yaml.Marshal(d)
			parts = append(parts, string(y))
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(parts, "---\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	required := readYAMLDocs(t, filepath.Join(fdFixtureDir, "required-applications.yaml"))
	required = append(required, map[string]any{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "importer", "namespace": "adhar-system"},
		"spec":     map[string]any{"parameters": map[string]any{"type": "batch", "project": "adhar"}},
		"status": map[string]any{"delivery": map[string]any{"promote": true, "image": "harbor-core.adhar-system.svc.cluster.local/library/importer",
			"repoURL": "http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/importer.git", "path": "deploy"}},
	}, sgManager(sgCLIImage))
	required = append(required, sgDefaultBatchJobs()...)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "render", filepath.Join(fdFixtureDir, "project.yaml"), filepath.Join(controlPlaneDir(t), "compositions/project/local.yaml"),
		filepath.Join(bjFixtureDir, "functions.yaml"), "--required-resources="+write("required.yaml", required...))
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runRender(cmd); err != nil {
		t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
	}
	var docs []map[string]any
	for _, d := range strings.Split(stdout.String(), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err == nil && len(m) > 0 {
			docs = append(docs, m)
		}
	}
	metrics := sgMetrics(t, docs, "test")
	m := metrics["importer-smoke-run"]
	if m == nil {
		t.Fatalf("crossplane render composes no importer-smoke-run; test-health has %d metrics", len(metrics))
	}
	c := dig(m, "provider", "job", "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	if c["image"] != sgCLIImage || fmt.Sprint(c["args"]) != "[batch run importer -n adhar-test --smoke --wait -o json]" {
		t.Errorf("through crossplane render the smoke metric runs %v %v", c["image"], c["args"])
	}
	// `crossplane render` ignores a label selector's namespace, as this composition
	// expects: the dev and prod copies, and the stranger, still compose nothing.
	for name := range metrics {
		if name == "stranger-smoke-run" {
			t.Error("crossplane render composed a smoke run for another project's application")
		}
	}
	if composedManifest(docs, "ServiceAccount", "adhar-batch-smoke") == nil {
		t.Error("crossplane render composes no smoke gate ServiceAccount")
	}
}
