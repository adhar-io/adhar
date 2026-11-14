package adharplatform

// The workflow step `adhar-batch-steps/run` (docs/design/0025 §12.1, D10) is the
// contract between Argo Workflows and a BatchJob: a workflow that wants a batch
// run reaches it through this one ClusterWorkflowTemplate, which IS
// `adhar batch run <batchJob> -n <ns> --wait -o json` on the platform image. The
// guards below hold the template to that contract, because each way it can drift
// fails somewhere far from the template: a step retried on FAILURE restarts the
// failed run as -r1 behind the workflow's back; a pod labelled like a run is
// counted as one by the CLI and the observer; an Argo expression rendered away by
// the seed-time Go template reaches Argo as an empty string; and a missing output
// default turns a failed run into a step error with no exit code at all.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/utils/files"
)

const (
	batchStepsFile  = "manifests/catalog/adhar-batch-steps.yaml.tmpl"
	batchStepsImage = "ghcr.io/adhar-io/adhar:9.9.9-test"
	// batchStepOutputs is the directory the CLI writes the step's outputs into.
	batchStepOutputs = "/tmp/adhar-batch-step"
)

// batchStepRun renders the catalogue file exactly as stageStack does and
// returns the ClusterWorkflowTemplate and its `run` template.
func batchStepRun(t *testing.T) (cwt map[string]any, run map[string]any, rendered string) {
	t.Helper()
	data := argoStageData()
	data.platformImage = batchStepsImage
	out, err := files.ApplyTemplate([]byte(readStackFile(t, argoPkg+"/"+batchStepsFile)), data)
	if err != nil {
		t.Fatalf("rendering %s: %v", batchStepsFile, err)
	}
	rendered = string(out)
	docs := parseDocs(t, batchStepsFile, rendered)
	if len(docs) != 1 {
		t.Fatalf("%s renders %d documents; it must be the one ClusterWorkflowTemplate", batchStepsFile, len(docs))
	}
	cwt = argoFind(docs, "ClusterWorkflowTemplate", "adhar-batch-steps")
	if cwt == nil {
		t.Fatalf("%s holds no ClusterWorkflowTemplate adhar-batch-steps", batchStepsFile)
	}
	templates, _ := dig(cwt, "spec", "templates").([]any)
	for _, raw := range templates {
		if m, ok := raw.(map[string]any); ok && m["name"] == "run" {
			run = m
		}
	}
	if run == nil {
		t.Fatal("adhar-batch-steps has no template `run` (design 0025 §12.1 names it adhar-batch-steps/run)")
	}
	return cwt, run, rendered
}

func batchStepParams(run map[string]any, path ...string) map[string]map[string]any {
	out := map[string]map[string]any{}
	list, _ := dig(run, path...).([]any)
	for _, raw := range list {
		if m, ok := raw.(map[string]any); ok {
			out[argoStr(m["name"])] = m
		}
	}
	return out
}

// TestBatchStepIsInTheCatalogue: the designer builds its "Platform steps" palette
// from the catalogue's labels and annotations (0026 §4.7, §5.7), and Argo CD
// applies only what the catalogue kustomization lists by its RENDERED name.
func TestBatchStepIsInTheCatalogue(t *testing.T) {
	cwt, run, _ := batchStepRun(t)
	if dig(cwt, "metadata", "labels", "adhar.io/catalog") != "true" {
		t.Error(`adhar-batch-steps is not labelled adhar.io/catalog: "true"; the designer's palette will not offer it`)
	}
	for _, key := range []string{"workflows.argoproj.io/title", "workflows.argoproj.io/description"} {
		if argoStr(dig(cwt, "metadata", "annotations", key)) == "" {
			t.Errorf("adhar-batch-steps has no %s annotation", key)
		}
		if argoStr(dig(run, "metadata", "annotations", key)) == "" {
			t.Errorf("adhar-batch-steps/run has no %s annotation", key)
		}
	}
	if got := dig(run, "metadata", "annotations", "adhar.io/node-kind"); got != "batch-job" {
		t.Errorf("adhar-batch-steps/run node kind = %v, want batch-job (0026 §4.7)", got)
	}

	kust := withoutComments(readStackFile(t, argoPkg+"/manifests/catalog/kustomization.yaml"))
	if !regexp.MustCompile(`(?m)^\s*-\s*adhar-batch-steps\.yaml\s*$`).MatchString(kust) {
		t.Error("manifests/catalog/kustomization.yaml does not list adhar-batch-steps.yaml (the rendered name)")
	}
	if strings.Contains(kust, "adhar-batch-steps.yaml.tmpl") {
		t.Error("manifests/catalog/kustomization.yaml lists the .tmpl; Gitea only ever holds the rendered file")
	}
	if !strings.Contains(withoutComments(readStackFile(t, argoPkg+"/manifests/platform/kustomization.yaml")), "../catalog") {
		t.Error("manifests/platform/kustomization.yaml no longer includes ../catalog")
	}
}

// TestBatchStepRunsTheCLIContract: §12.1's behaviour is EXACTLY
// `adhar batch run <batchJob> -n <ns> --wait -o json`, on the image the control
// plane itself runs, with what a command line cannot carry in the environment.
func TestBatchStepRunsTheCLIContract(t *testing.T) {
	_, run, rendered := batchStepRun(t)
	c, _ := run["container"].(map[string]any)
	if c == nil {
		t.Fatal("adhar-batch-steps/run is not a container step")
	}
	if got := argoStr(c["image"]); got != batchStepsImage {
		t.Errorf("the step runs %q; it must be {{ .PlatformImage }} (%q here), so step and control plane are one release", got, batchStepsImage)
	}
	if got := strings.Join(argoStrings(c["command"]), " "); got != "/adhar" {
		t.Errorf("command = %q, want /adhar: emissary must not have to look the entrypoint up in a registry", got)
	}
	wantArgs := []string{
		"batch", "run", "{{inputs.parameters.batchJob}}",
		"-n", "{{inputs.parameters.namespace}}",
		"--wait={{inputs.parameters.wait}}",
		"--timeout={{inputs.parameters.timeout}}",
		"-o", "json",
	}
	if got := argoStrings(c["args"]); strings.Join(got, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("args = %q,\nwant %q", got, wantArgs)
	}

	inputs := batchStepParams(run, "inputs", "parameters")
	if _, ok := inputs["batchJob"]; !ok || inputs["batchJob"]["default"] != nil || inputs["batchJob"]["value"] != nil {
		t.Error("input batchJob must exist and be required (no default)")
	}
	for name, want := range map[string]string{
		"namespace":  "{{workflow.namespace}}",
		"parameters": "{}",
		"wait":       "true",
		"timeout":    "0s",
	} {
		p, ok := inputs[name]
		if !ok {
			t.Errorf("input %s is missing (§12.1)", name)
			continue
		}
		if got := argoStr(p["default"]); got != want {
			t.Errorf("input %s defaults to %q, want %q", name, got, want)
		}
	}

	env := map[string]string{}
	for _, raw := range asList(c["env"]) {
		if m, ok := raw.(map[string]any); ok {
			env[argoStr(m["name"])] = argoStr(m["value"])
		}
	}
	for name, want := range map[string]string{
		"ADHAR_BATCH_STEP_WORKFLOW":   "{{workflow.name}}",
		"ADHAR_BATCH_STEP_PARAMETERS": "{{inputs.parameters.parameters}}",
		"ADHAR_BATCH_STEP_OUTPUTS":    batchStepOutputs,
		// The retry count lets a retried step tell "my pod was lost after my
		// run failed" (report it) from a fresh attempt (restart as -r<n>).
		"ADHAR_BATCH_STEP_RETRY": "{{retries}}",
	} {
		if env[name] != want {
			t.Errorf("env %s = %q, want %q", name, env[name], want)
		}
	}

	// Every Argo expression survived the seed-time Go template and names an
	// input the template declares (a typo is an unresolved-variable error at
	// submit time, in someone else's workflow).
	for _, m := range regexp.MustCompile(`\{\{\s*inputs\.parameters\.([A-Za-z0-9_-]+)\s*\}\}`).FindAllStringSubmatch(withoutComments(rendered), -1) {
		if _, ok := inputs[m[1]]; !ok {
			t.Errorf("the template references inputs.parameters.%s, which it does not declare", m[1])
		}
	}
	if strings.Contains(withoutComments(rendered), "{{ .") || strings.Contains(withoutComments(rendered), "{{.") {
		t.Error("the rendered template still holds a Go template action")
	}
}

// TestBatchStepOutputsSurviveAFailedRun: §12.1's outputs are what a workflow
// branches on, and a run that exits 3 must fail its step WITH exitCode 3 in its
// outputs (A3.8). Argo collects output files from a failed container, but a
// parameter whose file is missing and that has no default is an error that
// replaces the run's verdict, so every output has one.
func TestBatchStepOutputsSurviveAFailedRun(t *testing.T) {
	_, run, _ := batchStepRun(t)
	outputs := batchStepParams(run, "outputs", "parameters")
	var names []string
	for n := range outputs {
		names = append(names, n)
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "executionId,exitCode,run,status,summary"; got != want {
		t.Errorf("outputs = %s, want %s (§12.1)", got, want)
	}
	for name, p := range outputs {
		vf, _ := p["valueFrom"].(map[string]any)
		if got := argoStr(vf["path"]); got != batchStepOutputs+"/"+name {
			t.Errorf("output %s is read from %q, want %s/%s (where the CLI writes it)", name, got, batchStepOutputs, name)
		}
		if _, ok := vf["default"]; !ok {
			t.Errorf("output %s has no default: a run that never wrote it errors the step instead of failing it", name)
		}
	}
	mounted := false
	c, _ := run["container"].(map[string]any)
	for _, raw := range asList(c["volumeMounts"]) {
		if m, ok := raw.(map[string]any); ok && strings.HasPrefix(batchStepOutputs, strings.TrimSuffix(argoStr(m["mountPath"]), "/")+"/") {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("nothing writable is mounted over %s, and the root filesystem is read-only", batchStepOutputs)
	}
}

// TestBatchStepIsNeverABatchRunItself: the step pod is the workflow's, not the
// batch job's. It must not look like a run (the CLI and the observer select run
// pods by adhar.io/batch-job), must not wait in a batch queue (0026 §0.1), must
// run as the WORKFLOW's identity (§12.1: Kyverno stamps it as the actor), and is
// retried only when its pod was lost: retrying a failed step would make the CLI
// see a failed run and restart it as -r1, a restart nobody asked for.
func TestBatchStepIsNeverABatchRunItself(t *testing.T) {
	_, run, _ := batchStepRun(t)
	labels, _ := dig(run, "metadata", "labels").(map[string]any)
	for _, k := range []string{"adhar.io/batch-job", "adhar.io/workload", "adhar.io/batch-trigger", "adhar.io/batch-role"} {
		if _, ok := labels[k]; ok {
			t.Errorf("the step pod carries %s: it would be counted as a run of the job", k)
		}
	}
	for _, k := range []string{"schedulerName", "serviceAccountName", "priorityClassName"} {
		if run[k] != nil {
			t.Errorf("adhar-batch-steps/run sets %s: the step runs as the workflow's pod, never a batch run's", k)
		}
	}
	if policy := argoStr(dig(run, "retryStrategy", "retryPolicy")); run["retryStrategy"] != nil && policy != "OnError" {
		t.Errorf("retryStrategy.retryPolicy = %q: only OnError (a lost pod) may retry; a retried FAILURE restarts the run", policy)
	}

	c, _ := run["container"].(map[string]any)
	sc, _ := c["securityContext"].(map[string]any)
	if sc["runAsNonRoot"] != true || sc["readOnlyRootFilesystem"] != true || sc["allowPrivilegeEscalation"] != false {
		t.Errorf("the step's securityContext is %v; it must be non-root, read-only and without privilege escalation", sc)
	}
	if drop := argoStrings(dig(sc, "capabilities", "drop")); len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("the step drops %v capabilities, want ALL", drop)
	}
	for _, path := range [][]string{{"requests", "cpu"}, {"requests", "memory"}, {"limits", "memory"}} {
		if argoStr(dig(c, append([]string{"resources"}, path...)...)) == "" {
			t.Errorf("the step has no resources.%s; the team LimitRange would size it", strings.Join(path, "."))
		}
	}
}

// TestBatchStepRendersInTheStagedStack: the whole argo-workflows package stages
// with the catalogue file rendered to the name the kustomization lists.
func TestBatchStepRendersInTheStagedStack(t *testing.T) {
	src := filepath.Join(stackPackagesDir(t), filepath.FromSlash(argoPkg))
	t.Setenv(PlatformImageEnv, "")
	out, cleanup, err := stageStackWith(src, stackTemplateData{BuildCustomizationSpec: argoStageData().BuildCustomizationSpec, platformImage: batchStepsImage})
	if err != nil {
		t.Fatalf("staging %s: %v", argoPkg, err)
	}
	defer cleanup()
	b, err := os.ReadFile(filepath.Join(out, "manifests", "catalog", "adhar-batch-steps.yaml"))
	if err != nil {
		t.Fatalf("the staged package has no manifests/catalog/adhar-batch-steps.yaml: %v", err)
	}
	if !strings.Contains(string(b), "image: "+batchStepsImage) {
		t.Errorf("the staged step does not run the platform image %s", batchStepsImage)
	}
	if _, err := os.Stat(filepath.Join(out, "manifests", "catalog", "adhar-batch-steps.yaml.tmpl")); err == nil {
		t.Error("the staged package still holds the .tmpl")
	}
}

// TestBatchStepPlatformImageResolution: {{ .PlatformImage }} is never empty (a
// failed render fails the whole seed), and a reconcile prefers what its caller
// installed, then the environment, then the manager Deployment it runs in.
func TestBatchStepPlatformImageResolution(t *testing.T) {
	t.Setenv(PlatformImageEnv, "")
	if got := (stackTemplateData{}).PlatformImage(); got != fallbackPlatformImage {
		t.Errorf("PlatformImage() with nothing configured = %q, want %q", got, fallbackPlatformImage)
	}
	if got := (stackTemplateData{platformImage: "a:1"}).PlatformImage(); got != "a:1" {
		t.Errorf("PlatformImage() = %q, want the staged value a:1", got)
	}

	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: managerDeployment, Namespace: globals.AdharSystemNamespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Image: "wrong:1"},
			{Name: managerContainer, Image: "ghcr.io/adhar-io/adhar:0.1.70"},
		}}}},
	}
	r := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(dep).Build()}
	ctx := context.Background()
	if got := r.resolvePlatformImage(ctx); got != "ghcr.io/adhar-io/adhar:0.1.70" {
		t.Errorf("resolvePlatformImage() = %q, want the manager container's image", got)
	}
	t.Setenv(PlatformImageEnv, "env:2")
	if got := r.resolvePlatformImage(ctx); got != "env:2" {
		t.Errorf("resolvePlatformImage() = %q, want ADHAR_PLATFORM_IMAGE", got)
	}
	r.PlatformImage = "field:3"
	if got := r.resolvePlatformImage(ctx); got != "field:3" {
		t.Errorf("resolvePlatformImage() = %q, want the reconciler's PlatformImage", got)
	}
	t.Setenv(PlatformImageEnv, "")
	if got := (&AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).Build()}).resolvePlatformImage(ctx); got != "" {
		t.Errorf("resolvePlatformImage() with no manager = %q, want \"\" (PlatformImage falls back)", got)
	}
}

func argoStrings(v any) []string {
	var out []string
	for _, x := range asList(v) {
		out = append(out, argoStr(x))
	}
	return out
}
