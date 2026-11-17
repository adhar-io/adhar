package batch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const stepParams = `[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"},{"name":"region","identifying":false,"default":"sg"}]`

func envFrom(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func annotated(run *unstructured.Unstructured, workflow string) *unstructured.Unstructured {
	a := run.GetAnnotations()
	a[annotationWorkflow] = workflow
	run.SetAnnotations(a)
	return run
}

func jobNames(t *testing.T, dc *dynamicfake.FakeDynamicClient) string {
	t.Helper()
	list, err := dc.Resource(jobGVR).Namespace("prod").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range list.Items {
		out = append(out, it.GetName())
	}
	return strings.Join(out, ",")
}

// The step's four variables are exactly what adhar-batch-steps.yaml.tmpl sets
// (TestBatchStepRunsTheCLIContract pins that side). The parameters are a JSON
// object of scalars, each kept as the text it was written as; -p wins; the
// retry count is a number or it is a first attempt.
func TestStepContractIsReadFromTheEnvironment(t *testing.T) {
	step, err := stepFromEnv(envFrom(map[string]string{
		"ADHAR_BATCH_STEP_WORKFLOW":   "nightly-7x2kq",
		"ADHAR_BATCH_STEP_PARAMETERS": `{"businessDate":"2026-11-03","asOf":20261103,"rate":1.50,"dryRun":true,"region":null}`,
		"ADHAR_BATCH_STEP_OUTPUTS":    "/tmp/adhar-batch-step",
		"ADHAR_BATCH_STEP_RETRY":      "2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if step.Workflow != "nightly-7x2kq" || step.Outputs != "/tmp/adhar-batch-step" || step.Retry != 2 {
		t.Errorf("step = %+v", step)
	}
	want := map[string]string{"businessDate": "2026-11-03", "asOf": "20261103", "rate": "1.50", "dryRun": "true"}
	if len(step.Params) != len(want) {
		t.Errorf("params = %v, want %v (null leaves a parameter out)", step.Params, want)
	}
	for k, v := range want {
		if step.Params[k] != v {
			t.Errorf("parameter %s = %q, want %q", k, step.Params[k], v)
		}
	}
	merged := step.withParameters(map[string]string{"businessDate": "2026-11-04", "source": "cli"})
	if merged["businessDate"] != "2026-11-04" || merged["source"] != "cli" || merged["asOf"] != "20261103" {
		t.Errorf("-p wins over the step's parameters, and both are kept: %v", merged)
	}

	// A person's run: nothing set, nothing changes.
	none, err := stepFromEnv(envFrom(nil))
	if err != nil || none.Workflow != "" || none.Outputs != "" || none.Retry != 0 || none.Params != nil {
		t.Errorf("no variables = %+v %v", none, err)
	}
	flags := map[string]string{"a": "1"}
	if got := none.withParameters(flags); len(got) != 1 || got["a"] != "1" {
		t.Errorf("without step parameters the flags are the parameters: %v", got)
	}
	for _, blank := range []string{"", "  ", "{}"} {
		if p, err := parseStepParameters(blank); err != nil || len(p) != 0 {
			t.Errorf("parameters %q = %v %v, want none", blank, p, err)
		}
	}
	// {{retries}} unsubstituted, or anything that is not a count, is a first attempt.
	for _, raw := range []string{"{{retries}}", "-1", "x", ""} {
		if s, _ := stepFromEnv(envFrom(map[string]string{envStepRetry: raw})); s.Retry != 0 {
			t.Errorf("retry %q read as %d, want 0", raw, s.Retry)
		}
	}
	for raw, why := range map[string]string{
		`["2026-11-03"]`:                         "not an object",
		`"businessDate=2026-11-03"`:              "not an object",
		`{"businessDate":{"from":"2026-11-03"}}`: `"businessDate" is not`,
		`{"dates":["2026-11-03"]}`:               `"dates" is not`,
		`{"businessDate":"2026-11-03"} trailing`: "after the object",
		`{"businessDate":`:                       envStepParameters,
	} {
		if _, err := stepFromEnv(envFrom(map[string]string{envStepParameters: raw})); err == nil || !strings.Contains(err.Error(), why) || !strings.Contains(err.Error(), envStepParameters) {
			t.Errorf("parameters %s: %v, want a refusal naming %q", raw, err, why)
		}
	}
}

// §12.1: a run a workflow asks for is the run `adhar batch run` would start —
// the same name for the same parameters — labelled with the trigger workflow
// and annotated with the workflow's name. A3.8: another workflow asking for the
// same parameters finds the completed run and executes nothing.
func TestWorkflowStepStartsTheRunAndASecondWorkflowReusesIt(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(stepParams))
	step := workflowStep{Workflow: "nightly-7x2kq", Params: map[string]string{"businessDate": "2026-11-03"}}

	out, err := startStepRun(ctx, dc, nil, "prod", "importer", step.withParameters(nil), step, false, 0)
	if err != nil || out.Run != "importer-09932cb3" || out.Status != "Running" {
		t.Fatalf("the step's run = %+v %v", out, err)
	}
	run := getJob(t, dc, "prod", "importer-09932cb3")
	if run.GetLabels()[labelTrigger] != "workflow" || envOf(run, envTrigger) != "workflow" {
		t.Errorf("trigger = label %q env %q, want workflow", run.GetLabels()[labelTrigger], envOf(run, envTrigger))
	}
	if run.GetAnnotations()["adhar.io/batch-workflow"] != "nightly-7x2kq" {
		t.Errorf("the run names its workflow in %s: %v", annotationWorkflow, run.GetAnnotations())
	}
	if _, kept := run.GetAnnotations()[annotationActor]; !kept {
		t.Errorf("the template's own annotations are kept (Kyverno overwrites the actor): %v", run.GetAnnotations())
	}
	if envOf(run, envParams) != `{"businessDate":"2026-11-03","region":"sg"}` {
		t.Errorf("params = %s", envOf(run, envParams))
	}
	for _, tpl := range podTemplatesOf(run) {
		if a, _, _ := unstructured.NestedStringMap(tpl, "metadata", "annotations"); a[annotationWorkflow] != "" {
			t.Error("the workflow is named on the run, not in the pod template run integrity compares")
		}
	}

	// A person's run of the same parameters is untouched by all of this.
	plain := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(stepParams))
	if _, err := startRun(ctx, plain, nil, "prod", "importer", map[string]string{"businessDate": "2026-11-03"}, false, 0); err != nil {
		t.Fatal(err)
	}
	manual := getJob(t, plain, "prod", "importer-09932cb3")
	if _, has := manual.GetAnnotations()[annotationWorkflow]; has || manual.GetLabels()[labelTrigger] != "manual" {
		t.Errorf("a person's run = labels %v annotations %v", manual.GetLabels(), manual.GetAnnotations())
	}

	// The run completes; a second workflow with the same parameters reuses it.
	run.Object["status"] = map[string]interface{}{"succeeded": int64(1), "conditions": []interface{}{map[string]interface{}{"type": "Complete", "status": "True"}}}
	_, _ = dc.Resource(jobGVR).Namespace("prod").Update(ctx, run, metav1.UpdateOptions{})
	_, _ = dc.Resource(podGVR).Namespace("prod").Create(ctx, podObject("importer-09932cb3-p", "prod", "importer-09932cb3", 0, map[string]interface{}{"v": 1, "outcome": "completed", "exitCode": 0, "executionId": 12}, testNow), metav1.CreateOptions{})
	second := workflowStep{Workflow: "nightly-9d4hm", Params: step.Params}
	out, err = startStepRun(ctx, dc, nil, "prod", "importer", second.withParameters(nil), second, true, time.Minute)
	if err != nil || out.Run != "importer-09932cb3" || out.Status != "Succeeded" || out.ExitCode != 0 || out.ExecutionID == nil || *out.ExecutionID != 12 {
		t.Fatalf("the second workflow = %+v %v", out, err)
	}
	if got := jobNames(t, dc); got != "importer-09932cb3" {
		t.Errorf("re-running the workflow executed again: %s", got)
	}
	if getJob(t, dc, "prod", "importer-09932cb3").GetAnnotations()[annotationWorkflow] != "nightly-7x2kq" {
		t.Error("a reused run still names the workflow that started it")
	}
}

// The one place the step leaves D2: a RETRY of the step (its pod was lost) that
// finds a failed run its own workflow started reports that failure, because the
// run already answered and restarting it would run the job twice behind a
// failure nobody saw. A first attempt, and a failed run another workflow
// started, restart as -r<n> as a person's `run --wait` does — annotated, so
// the rule holds for the restart too.
func TestRetriedStepReportsTheFailureItsOwnWorkflowStarted(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	at := testNow.Add(-time.Hour)
	values := map[string]string{"businessDate": "2026-11-03"}
	const base = "importer-09932cb3"
	cluster := func(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
		return fakeCluster(append([]runtime.Object{batchJobXR("importer", "prod", "", ""), templateConfigMap(stepParams)}, objs...)...)
	}
	failedBy := func(name, workflow string) *unstructured.Unstructured {
		return annotated(jobObject(name, "prod", "importer", "failed", at), workflow)
	}
	exit3 := func(run string) *unstructured.Unstructured {
		return podObject(run+"-p", "prod", run, 3, map[string]interface{}{"v": 1, "outcome": "failed", "exitCode": 3, "executionId": 12}, at)
	}

	// Retry 1 of the step of the workflow that started the failed run: report it.
	dc := cluster(failedBy(base, "nightly-7x2kq"), exit3(base))
	out, err := startStepRun(ctx, dc, nil, "prod", "importer", values, workflowStep{Workflow: "nightly-7x2kq", Retry: 1}, true, time.Minute)
	if err != nil || out.Run != base || out.Status != "Failed" || out.ExitCode != 3 || out.ExecutionID == nil || *out.ExecutionID != 12 {
		t.Fatalf("a retried step reports the failed run with its exit code: %+v %v", out, err)
	}
	if got := jobNames(t, dc); got != base {
		t.Errorf("the retried step restarted the run it had already failed: %s", got)
	}

	// The same failed run, but a first attempt (`argo retry`, retries 0): restart.
	dc = cluster(failedBy(base, "nightly-7x2kq"), exit3(base))
	_, err = startStepRun(ctx, dc, nil, "prod", "importer", values, workflowStep{Workflow: "nightly-7x2kq"}, true, time.Millisecond)
	if !errors.Is(err, errStillRunning) {
		t.Fatalf("a first attempt restarts and waits: %v", err)
	}
	r1 := getJob(t, dc, "prod", base+"-r1")
	if r1.GetLabels()[labelTrigger] != "restart" || envOf(r1, envRestartOf) != base || r1.GetAnnotations()[annotationWorkflow] != "nightly-7x2kq" {
		t.Errorf("the restart = labels %v restartOf %q annotations %v", r1.GetLabels(), envOf(r1, envRestartOf), r1.GetAnnotations())
	}

	// A retry that finds a failed run ANOTHER workflow started: that failure was
	// never this step's answer, so it restarts.
	dc = cluster(failedBy(base, "nightly-older"), exit3(base))
	_, err = startStepRun(ctx, dc, nil, "prod", "importer", values, workflowStep{Workflow: "nightly-7x2kq", Retry: 1}, true, time.Millisecond)
	if !errors.Is(err, errStillRunning) || getJob(t, dc, "prod", base+"-r1") == nil {
		t.Fatalf("another workflow's failure is restarted: %v", err)
	}

	// The restart this workflow created failed, and the pod was lost again:
	// retry 2 reports -r1 and never creates -r2.
	dc = cluster(failedBy(base, "nightly-older"), failedBy(base+"-r1", "nightly-7x2kq"), exit3(base+"-r1"))
	out, err = startStepRun(ctx, dc, nil, "prod", "importer", values, workflowStep{Workflow: "nightly-7x2kq", Retry: 2}, true, time.Minute)
	if err != nil || out.Run != base+"-r1" || out.Status != "Failed" || out.ExitCode != 3 {
		t.Fatalf("the chain's latest member is what the retry reports: %+v %v", out, err)
	}
	if got := jobNames(t, dc); strings.Contains(got, "-r2") {
		t.Errorf("the retried step restarted again: %s", got)
	}

	// A person's `run --wait` never reports instead of restarting, whatever
	// the failed run is annotated with.
	dc = cluster(failedBy(base, "nightly-7x2kq"), exit3(base))
	if _, err = startRun(ctx, dc, nil, "prod", "importer", values, true, time.Millisecond); !errors.Is(err, errStillRunning) || getJob(t, dc, "prod", base+"-r1") == nil {
		t.Fatalf("a person's run --wait restarts a failed run: %v", err)
	}
}

func readOutputs(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

// §12.1's outputs: one file per output in ADHAR_BATCH_STEP_OUTPUTS, the file
// being the value. They are written when the step fails too — the process then
// ends with the run's exit code — and a wait that gave up still names the run.
func TestStepOutputsAreOneFilePerOutputEvenWhenTheRunFails(t *testing.T) {
	prev := outputFlag
	outputFlag = "table"
	t.Cleanup(func() { outputFlag = prev })
	id := int64(412)

	// The directory does not exist in the step's emptyDir until the CLI makes it.
	dir := filepath.Join(t.TempDir(), "adhar-batch-step")
	ok := runOutcome{Run: "importer-09932cb3", Namespace: "prod", BatchJob: "importer", Kind: "Job", Status: "Succeeded", ExecutionID: &id,
		Summary: map[string]interface{}{"v": float64(1), "outcome": "completed", "executionId": float64(412), "message": "a<b & c"}}
	if err := finishStep(workflowStep{Outputs: dir}, ok, nil); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"run": "importer-09932cb3", "status": "Succeeded", "exitCode": "0", "executionId": "412",
		"summary": `{"executionId":412,"message":"a<b & c","outcome":"completed","v":1}`}
	got := readOutputs(t, dir)
	if len(got) != len(want) {
		t.Errorf("outputs = %v, want exactly %v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("output %s = %q, want %q", name, got[name], value)
		}
	}

	// A failed run: its outputs AND its exit code.
	failed := runOutcome{Run: "importer-09932cb3", Status: "Failed", ExitCode: 3, Summary: map[string]interface{}{"outcome": "failed", "exitCode": float64(3)}}
	err := finishStep(workflowStep{Outputs: dir}, failed, nil)
	if code, has := helpers.ExitCodeOf(err); !has || code != 3 {
		t.Fatalf("the step ends with the run's exit code: %v", err)
	}
	got = readOutputs(t, dir)
	if got["status"] != "Failed" || got["exitCode"] != "3" || got["executionId"] != "" || got["summary"] != `{"exitCode":3,"outcome":"failed"}` {
		t.Errorf("a failed run's outputs = %v", got)
	}

	// Started but not waited for, and no summary yet: no exit code to give.
	if err := finishStep(workflowStep{Outputs: dir}, runOutcome{Run: "importer-09932cb3", Status: "Running"}, nil); err != nil {
		t.Fatal(err)
	}
	if got = readOutputs(t, dir); got["status"] != "Running" || got["exitCode"] != "" || got["summary"] != "{}" {
		t.Errorf("a run that has not ended has no exit code and an empty summary: %v", got)
	}

	// No outcome (the job was not found, the parameters were refused): nothing
	// is written, the template's defaults stand, the error is the command's.
	empty := filepath.Join(t.TempDir(), "none")
	refused := errors.New("parameters refused")
	if err := finishStep(workflowStep{Outputs: empty}, runOutcome{}, refused); err != refused {
		t.Errorf("the command's error is returned: %v", err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Error("outputs were written for a run that was never named")
	}
	// A person's run writes nowhere.
	if err := finishStep(workflowStep{}, ok, nil); err != nil {
		t.Fatal(err)
	}

	// Outputs that cannot be written fail a step that would otherwise pass (its
	// status would read Unknown behind a green step), but never replace a run's
	// own exit code.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finishStep(workflowStep{Outputs: filepath.Join(blocked, "out")}, ok, nil); err == nil || !strings.Contains(err.Error(), envStepOutputs) {
		t.Errorf("unwritable outputs on a succeeded run: %v", err)
	}
	err = finishStep(workflowStep{Outputs: filepath.Join(blocked, "out")}, failed, nil)
	if code, has := helpers.ExitCodeOf(err); !has || code != 3 {
		t.Errorf("unwritable outputs on a failed run keep the run's exit code: %v", err)
	}
}

// A wait that gives up is exit 1 — and the outputs still say WHICH run the
// workflow should look at, as Running, with no exit code.
func TestStepThatGivesUpWaitingStillNamesTheRun(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(stepParams))
	dir := filepath.Join(t.TempDir(), "out")
	step := workflowStep{Workflow: "nightly-7x2kq", Outputs: dir}
	out, runErr := startStepRun(context.Background(), dc, nil, "prod", "importer", map[string]string{"businessDate": "2026-11-03"}, step, true, time.Millisecond)
	if !errors.Is(runErr, errStillRunning) || out.Run != "importer-09932cb3" || out.Status != "Running" || out.Kind != "Job" {
		t.Fatalf("a wait that timed out = %+v %v", out, runErr)
	}
	err := finishStep(step, out, runErr)
	if code, has := helpers.ExitCodeOf(err); !has || code != 1 || !strings.Contains(err.Error(), "still") {
		t.Fatalf("giving up is exit 1 with the run named: %v", err)
	}
	if got := readOutputs(t, dir); got["run"] != "importer-09932cb3" || got["status"] != "Running" || got["exitCode"] != "" {
		t.Errorf("outputs = %v", got)
	}
}

// The command reads the contract itself: a step's parameters that are not a
// JSON object are refused before anything is read from the cluster, and a
// smoke run takes parameters from neither -p nor the step.
func TestRunReadsTheStepContractBeforeTouchingTheCluster(t *testing.T) {
	prevSmoke, prevParams := runSmoke, runParamArgs
	t.Cleanup(func() { runSmoke, runParamArgs = prevSmoke, prevParams })
	runSmoke, runParamArgs = false, nil
	// No cluster to reach: were a refusal ever missing, the command must fail
	// to connect rather than start a run in whatever cluster the machine has.
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "no-cluster"))
	t.Setenv(envStepParameters, `[1,2]`)
	if err := runCmd.RunE(runCmd, []string{"importer"}); err == nil || !strings.Contains(err.Error(), envStepParameters) {
		t.Errorf("a parameters input that is not an object: %v", err)
	}
	runSmoke = true
	t.Setenv(envStepParameters, `{"businessDate":"2026-11-01"}`)
	if err := runCmd.RunE(runCmd, []string{"importer"}); err == nil || !strings.Contains(err.Error(), "spec.parameters.smoke") {
		t.Errorf("--smoke with step parameters: %v", err)
	}
}

// stepTemplatePath is the other side of the contract: the step as the
// argo-workflows package ships it.
const stepTemplatePath = "../../platform/stack/packages/application/argo-workflows/manifests/catalog/adhar-batch-steps.yaml.tmpl"

// The contract has two sides in two packages, and each is pinned by its own
// test against literals (TestBatchStepRunsTheCLIContract there,
// TestStepContractIsReadFromTheEnvironment here), so the two could drift apart
// with both green. This reads the template itself: the ADHAR_BATCH_STEP_*
// variables it sets are exactly the ones this command reads, each fed from
// what the command takes it for; the files it tells Argo to read its outputs
// from are exactly the files the command writes, in the directory it is given;
// and the command line it runs parses with this command's own flags, for both
// values of `wait` and the default `timeout`.
func TestTheStepTemplateSpeaksThisCommandsContract(t *testing.T) {
	raw, err := os.ReadFile(stepTemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines = append(lines, l)
		}
	}
	// The seed-time escape {{`{{x}}`}} is the Argo variable {{x}}.
	text := strings.NewReplacer("{{`", "", "`}}", "").Replace(strings.Join(lines, "\n"))

	// The environment.
	sets := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*- name: (ADHAR_BATCH_STEP_[A-Z_]+)\n\s*value: "?([^"\n]*)"?$`).FindAllStringSubmatch(text, -1) {
		sets[m[1]] = m[2]
	}
	feeds := map[string]string{
		envStepWorkflow:   "{{workflow.name}}",
		envStepParameters: "{{inputs.parameters.parameters}}",
		envStepRetry:      "{{retries}}",
	}
	if len(sets) != len(feeds)+1 {
		t.Errorf("the template sets %v; this command reads %s, %s, %s and %s", sets, envStepWorkflow, envStepParameters, envStepOutputs, envStepRetry)
	}
	for name, want := range feeds {
		if sets[name] != want {
			t.Errorf("the template sets %s to %q; the command takes it for %s", name, sets[name], want)
		}
	}
	dir := sets[envStepOutputs]
	if !filepath.IsAbs(dir) {
		t.Fatalf("the template sets %s to %q, which is not a directory to write into", envStepOutputs, dir)
	}

	// The outputs: Argo reads the files this command writes.
	var reads []string
	for _, m := range regexp.MustCompile(`(?m)^\s*path: (\S+)$`).FindAllStringSubmatch(text, -1) {
		if filepath.Dir(m[1]) != dir {
			t.Errorf("the template reads the output %s outside %s, where the command writes", m[1], dir)
		}
		reads = append(reads, filepath.Base(m[1]))
	}
	// A run that has only started has the least to say, and still writes every
	// file: one left out would hand the next step the template's default.
	var writes []string
	for name := range stepOutputsOf(runOutcome{Run: "importer-09932cb3", Status: "Running"}) {
		writes = append(writes, name)
	}
	sort.Strings(reads)
	sort.Strings(writes)
	if strings.Join(reads, ",") != strings.Join(writes, ",") {
		t.Errorf("the template reads the outputs %v; the command writes %v", reads, writes)
	}

	// The command line.
	block := regexp.MustCompile(`(?s)\n\s*args:\n(.*?)\n\s*env:\n`).FindStringSubmatch(text)
	if block == nil {
		t.Fatal("the template's container has no args followed by env")
	}
	defaultOf := func(input string) string {
		m := regexp.MustCompile(`(?s)- name: ` + input + `\n.*?default: "?([^"\n]*)"?\n`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("the template's input %s has no default", input)
		}
		return m[1]
	}
	prevNS, prevOut, prevWait, prevTimeout := namespaceFlag, outputFlag, runWait, runTimeout
	t.Cleanup(func() {
		namespaceFlag, outputFlag, runWait, runTimeout = prevNS, prevOut, prevWait, prevTimeout
		runCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
	})
	for _, wait := range []string{"true", "false"} {
		inputs := strings.NewReplacer(
			"{{inputs.parameters.batchJob}}", "importer",
			"{{inputs.parameters.namespace}}", "prod",
			"{{inputs.parameters.wait}}", wait,
			"{{inputs.parameters.timeout}}", defaultOf("timeout"))
		var args []string
		for _, l := range strings.Split(block[1], "\n") {
			args = append(args, inputs.Replace(strings.Trim(strings.TrimPrefix(strings.TrimSpace(l), "- "), `"`)))
		}
		if len(args) < 2 || args[0] != "batch" {
			t.Fatalf("the step runs %v, not `adhar batch …`", args)
		}
		cmd, rest, err := BatchCmd.Find(args[1:])
		if err != nil || cmd != runCmd {
			t.Fatalf("the step runs %v, which is not `adhar batch run`: %v", args, err)
		}
		runWait, runTimeout, namespaceFlag, outputFlag = wait != "true", time.Hour, "", "table"
		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatalf("`adhar %s` does not parse: %v", strings.Join(args, " "), err)
		}
		if got := cmd.Flags().Args(); len(got) != 1 || got[0] != "importer" {
			t.Errorf("the job is the one positional argument: %v", got)
		}
		if (runWait != (wait == "true")) || runTimeout != 0 || namespaceFlag != "prod" || !machineReadable() {
			t.Errorf("wait=%s parsed as --wait %v --timeout %s -n %q -o %q", wait, runWait, runTimeout, namespaceFlag, outputFlag)
		}
	}
	if d := defaultOf("wait"); d != "true" {
		t.Errorf("the step waits by default (§12.1): wait defaults to %q", d)
	}
}
