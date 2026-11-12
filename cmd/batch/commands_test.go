package batch

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	k8stesting "k8s.io/client-go/testing"
)

func quietOutput(t *testing.T) {
	t.Helper()
	prev := outputFlag
	outputFlag = "json"
	t.Cleanup(func() { outputFlag = prev })
}

func getJob(t *testing.T, dc dynamic.Interface, ns, name string) *unstructured.Unstructured {
	t.Helper()
	run, err := dc.Resource(jobGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("run %s/%s: %v", ns, name, err)
	}
	return run
}

// `adhar batch run` without --wait does one thing: create <job>-<hash8> from
// the template, and show the run that already carries the identity when the
// create is refused. The identity is the Console's: importer-09932cb3 for
// businessDate=2026-11-03, region left at its default.
func TestRunCreatesTheNamedRunOrShowsTheExistingOne(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(`[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"},{"name":"region","identifying":false,"default":"sg"}]`))
	ctx := context.Background()

	out, err := startRun(ctx, dc, nil, "prod", "importer", map[string]string{"businessDate": "2026-11-03"}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Run != "importer-09932cb3" || out.Status != "Running" || out.ExitCode != 0 || out.BatchJob != "importer" {
		t.Fatalf("outcome = %+v", out)
	}
	run := getJob(t, dc, "prod", "importer-09932cb3")
	if run.GetLabels()[labelTrigger] != "manual" || envOf(run, envParams) != `{"businessDate":"2026-11-03","region":"sg"}` || envOf(run, envTrigger) != "manual" {
		t.Errorf("created run = labels %v params %q", run.GetLabels(), envOf(run, envParams))
	}
	if envOf(run, "ADHAR_BATCH_JOB") != "importJob" || runImage(run) == "" {
		t.Errorf("the run is the template: %v", run.Object)
	}

	// The same identity again: shown, not run twice, and nothing else created.
	again, err := startRun(ctx, dc, nil, "prod", "importer", map[string]string{"businessDate": "2026-11-03", "region": "sg"}, false, 0)
	if err != nil || again.Run != "importer-09932cb3" {
		t.Fatalf("second run = %+v %v", again, err)
	}
	list, _ := dc.Resource(jobGVR).Namespace("prod").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Errorf("a second create happened: %d runs", len(list.Items))
	}

	// Refused parameters never reach the cluster.
	if _, err := startRun(ctx, dc, nil, "prod", "importer", map[string]string{"businessDate": "yesterday"}, false, 0); err == nil || !strings.Contains(err.Error(), "businessDate") {
		t.Errorf("an invalid date must be refused before any create: %v", err)
	}
	if _, err := startRun(ctx, dc, nil, "prod", "nope", nil, false, 0); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a missing job: %v", err)
	}
}

// With --wait the command carries D2: completed → success with no new run;
// failed → restarted as -r<n> and waited for; stopped → Stopped, exit 4, hint
// to resume; running → waited for.
func TestRunWithWaitCarriesTheIdempotencyOfD2(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	prev := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = prev })
	params := `[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"}]`
	created := testNow.Add(-time.Hour)
	ctx := context.Background()
	values := map[string]string{"businessDate": "2026-11-03"}

	// Completed: reused, exit 0, and the skipped summary is the outcome.
	done := jobObject("importer-09932cb3", "prod", "importer", "succeeded", created)
	pod := podObject("importer-09932cb3-p", "prod", "importer-09932cb3", 0, map[string]interface{}{"v": 1, "outcome": "skipped", "reason": "already-complete", "exitCode": 0, "executionId": 12}, created)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(params), done, pod)
	out, err := startRun(ctx, dc, nil, "prod", "importer", values, true, time.Minute)
	if err != nil || out.Status != "Succeeded" || out.ExitCode != 0 || out.Run != "importer-09932cb3" || *out.ExecutionID != 12 {
		t.Fatalf("completed = %+v %v", out, err)
	}
	if list, _ := dc.Resource(jobGVR).Namespace("prod").List(ctx, metav1.ListOptions{}); len(list.Items) != 1 {
		t.Errorf("a completed identity must not be run again: %d runs", len(list.Items))
	}

	// Failed: restarted as -r1; the fake never finishes it, so the wait times
	// out — and the restart it created is the object a person would then see.
	failed := jobObject("importer-09932cb3", "prod", "importer", "failed", created)
	dc = fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(params), failed)
	_, err = startRun(ctx, dc, nil, "prod", "importer", values, true, time.Millisecond)
	if code, ok := helpers.ExitCodeOf(err); !ok || code != 1 || !strings.Contains(err.Error(), "still") {
		t.Fatalf("a wait that times out is exit 1 with the run named: %v", err)
	}
	restart := getJob(t, dc, "prod", "importer-09932cb3-r1")
	if restart.GetLabels()[labelTrigger] != "restart" || envOf(restart, envRestartOf) != "importer-09932cb3" || envOf(restart, envParams) != `{"businessDate":"2026-11-03"}` {
		t.Errorf("the restart = labels %v restartOf %q params %q", restart.GetLabels(), envOf(restart, envRestartOf), envOf(restart, envParams))
	}

	// Stopped: reported, exit 4, no new run.
	stopped := jobObject("importer-09932cb3", "prod", "importer", "stopped", created)
	dc = fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(params), stopped)
	out, err = startRun(ctx, dc, nil, "prod", "importer", values, true, time.Minute)
	if err != nil || out.Status != "Stopped" || out.ExitCode != 4 {
		t.Fatalf("stopped = %+v %v", out, err)
	}
	if list, _ := dc.Resource(jobGVR).Namespace("prod").List(ctx, metav1.ListOptions{}); len(list.Items) != 1 {
		t.Errorf("a stopped identity is resumed, never re-run: %d runs", len(list.Items))
	}

	// Nothing exists: created, and when it fails with exit 3 the CLI exits 3.
	dc = fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(params))
	go func() {
		time.Sleep(20 * time.Millisecond)
		run := getJob(t, dc, "prod", "importer-09932cb3")
		run.Object["status"] = map[string]interface{}{"failed": int64(1), "conditions": []interface{}{map[string]interface{}{"type": "Failed", "status": "True"}}}
		_, _ = dc.Resource(jobGVR).Namespace("prod").Update(ctx, run, metav1.UpdateOptions{})
		_, _ = dc.Resource(podGVR).Namespace("prod").Create(ctx, podObject("importer-09932cb3-p", "prod", "importer-09932cb3", 3, map[string]interface{}{"v": 1, "outcome": "failed", "exitCode": 3}, testNow), metav1.CreateOptions{})
	}()
	out, err = startRun(ctx, dc, nil, "prod", "importer", values, true, time.Minute)
	if err != nil || out.Status != "Failed" || out.ExitCode != 3 {
		t.Fatalf("a run that fails with 3 = %+v %v", out, err)
	}
	if _, has := out.Summary["outcome"]; !has {
		t.Errorf("the summary travels with the outcome: %+v", out)
	}
}

// A run created between the listing and the create: the API's AlreadyExists is
// the identity guard of last resort, and it shows the run rather than failing.
func TestRunShowsTheRunThatAppearedAfterTheListing(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(`[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"}]`),
		jobObject("importer-09932cb3", "prod", "importer", "running", testNow))
	dc.PrependReactor("list", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{Object: map[string]interface{}{"apiVersion": "batch/v1", "kind": "JobList"}}, nil
	})
	out, err := startRun(context.Background(), dc, nil, "prod", "importer", map[string]string{"businessDate": "2026-11-03"}, false, 0)
	if err != nil || out.Run != "importer-09932cb3" || out.Status != "Running" {
		t.Fatalf("outcome = %+v %v", out, err)
	}
}

type recordingLogs struct{ pods []string }

func (r *recordingLogs) stream(_ context.Context, _, pod string, _ bool, _ io.Writer) {
	r.pods = append(r.pods, pod)
}

// --follow streams each pod of the run once, as it appears, never a Pending one.
func TestWaitStreamsEachPodOnce(t *testing.T) {
	fixedClock(t)
	prev := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = prev })
	created := testNow.Add(-time.Minute)
	run := jobObject("importer-09932cb3", "prod", "importer", "succeeded", created)
	pending := podObject("importer-09932cb3-pending", "prod", "importer-09932cb3", 0, nil, created)
	pending.Object["status"] = map[string]interface{}{"phase": "Pending"}
	dc := fakeCluster(run, podObject("importer-09932cb3-a", "prod", "importer-09932cb3", 0, nil, created), pending)
	logs := &recordingLogs{}
	row, err := waitForRun(context.Background(), dc, logs, "prod", "importer-09932cb3", time.Minute)
	if err != nil || row.Phase != phaseSucceeded {
		t.Fatalf("wait = %+v %v", row, err)
	}
	if len(logs.pods) != 1 || logs.pods[0] != "importer-09932cb3-a" {
		t.Errorf("streamed %v, want the one started pod once", logs.pods)
	}
}

// `adhar batch restart <run>` creates <run>-r<n> with the same identifying
// parameters and names the run it continues; only a failed run may be restarted.
func TestRestartCreatesTheNextRestart(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	params := `[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"},{"name":"region","identifying":true,"default":"sg"}]`
	created := testNow.Add(-time.Hour)
	failed := jobObject("importer-97cfb853", "prod", "importer", "failed", created)
	r1 := jobObject("importer-97cfb853-r1", "prod", "importer", "failed", created.Add(time.Minute))
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(params), failed, r1,
		jobObject("importer-aaaaaaaa", "prod", "importer", "running", created))
	ctx := context.Background()

	createdNew, plan, err := restartRun(ctx, dc, "prod", "importer-97cfb853")
	if err != nil || !createdNew {
		t.Fatalf("restart = %v %+v %v", createdNew, plan, err)
	}
	// The run's params say businessDate=2026-11-03, region=sg → base importer-09932cb3… no:
	// the identity hashes BOTH identifying parameters, and r1 of that base exists.
	base, _ := runName("importer", map[string]string{"businessDate": "2026-11-03", "region": "sg"})
	if plan.Base != base {
		t.Fatalf("plan base = %s, want %s", plan.Base, base)
	}
	run := getJob(t, dc, "prod", plan.Name)
	if !strings.HasPrefix(plan.Name, base+"-r") || envOf(run, envRestartOf) != "importer-97cfb853" || run.GetLabels()[labelTrigger] != "restart" {
		t.Errorf("restart = %s, restartOf %q, labels %v", plan.Name, envOf(run, envRestartOf), run.GetLabels())
	}
	if _, _, err := restartRun(ctx, dc, "prod", "importer-aaaaaaaa"); err == nil || !strings.Contains(err.Error(), "only a failed run") {
		t.Errorf("a running run must not be restarted: %v", err)
	}
	if _, _, err := restartRun(ctx, dc, "prod", "nope"); err == nil {
		t.Error("a missing run is an error")
	}
}

// Hold writes the annotation with its reason, release removes it, and each
// answers what the hold was before; the BatchJob's spec is never touched.
func TestHoldAndReleasePatchOnlyTheAnnotation(t *testing.T) {
	dc := fakeCluster(batchJobXR("importer", "prod", "30 1 * * *", ""))
	ctx := context.Background()
	reason := "upstream feed late"
	prev, err := setHold(ctx, dc, "prod", "importer", &reason)
	if err != nil || prev != nil {
		t.Fatalf("first hold = %v %v", prev, err)
	}
	job, _ := dc.Resource(batchJobGVR).Namespace("prod").Get(ctx, "importer", metav1.GetOptions{})
	if job.GetAnnotations()[annotationHold] != reason {
		t.Errorf("annotation = %v", job.GetAnnotations())
	}
	if cron, _, _ := scheduleOf(job); cron != "30 1 * * *" {
		t.Errorf("the spec is not for operators to change: %q", cron)
	}
	other := "another reason"
	if prev, err := setHold(ctx, dc, "prod", "importer", &other); err != nil || prev == nil || *prev != reason {
		t.Errorf("a second hold reports the first: %v %v", prev, err)
	}
	if prev, err := setHold(ctx, dc, "prod", "importer", nil); err != nil || prev == nil || *prev != other {
		t.Errorf("release reports what was held: %v %v", prev, err)
	}
	job, _ = dc.Resource(batchJobGVR).Namespace("prod").Get(ctx, "importer", metav1.GetOptions{})
	if _, held := job.GetAnnotations()[annotationHold]; held {
		t.Errorf("release must remove the annotation: %v", job.GetAnnotations())
	}
	if prev, err := setHold(ctx, dc, "prod", "importer", nil); err != nil || prev != nil {
		t.Errorf("releasing a job not on hold is a no-op: %v %v", prev, err)
	}
	if _, err := setHold(ctx, dc, "prod", "missing", &reason); err == nil {
		t.Error("holding a job that does not exist is an error")
	}
}

// Stop patches spec.suspend=true, resume patches it false, on the Job itself.
func TestStopAndResumeSuspendTheJob(t *testing.T) {
	ctx := context.Background()
	dc := fakeCluster(jobObject("importer-09932cb3", "prod", "importer", "running", testNow))
	run := getJob(t, dc, "prod", "importer-09932cb3")
	req, err := suspendRequest(run, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := perform(ctx, dc, req); err != nil {
		t.Fatal(err)
	}
	run = getJob(t, dc, "prod", "importer-09932cb3")
	if s, _, _ := unstructured.NestedBool(run.Object, "spec", "suspend"); !s {
		t.Error("stop must set spec.suspend")
	}
	// The fake does not run the Job controller, so clear the active count the
	// way the controller would once the pod is gone, then resume.
	run.Object["status"] = map[string]interface{}{}
	_, _ = dc.Resource(jobGVR).Namespace("prod").Update(ctx, run, metav1.UpdateOptions{})
	run = getJob(t, dc, "prod", "importer-09932cb3")
	req, err = suspendRequest(run, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := perform(ctx, dc, req); err != nil {
		t.Fatal(err)
	}
	run = getJob(t, dc, "prod", "importer-09932cb3")
	if s, _, _ := unstructured.NestedBool(run.Object, "spec", "suspend"); s {
		t.Error("resume must clear spec.suspend")
	}
}

// The list reads every job of the scope with its runs; --env resolves the
// namespace from the Project's published environments.
func TestListAndScopeResolution(t *testing.T) {
	fixedClock(t)
	created := testNow.Add(-time.Hour)
	dc := fakeCluster(
		batchJobXR("importer", "prod", "30 1 * * *", ""),
		batchJobXR("importer", "test", "30 1 * * *", "feed late"),
		batchJobXR("settle", "prod", "", ""),
		jobObject("importer-09932cb3", "prod", "importer", "failed", created),
		jobObject("importer-aaaaaaaa", "prod", "importer", "succeeded", created.Add(-time.Hour)),
		projectXR("adhar", map[string]string{"dev": "dev", "test": "test", "prod": "prod"}),
		projectXR("team-a", map[string]string{"dev": "team-a-dev"}),
	)
	ctx := context.Background()
	views, err := listJobs(ctx, dc, "")
	if err != nil || len(views) != 3 {
		t.Fatalf("list -A = %d %v", len(views), err)
	}
	// Application, then environment in promotion order: importer/test, importer/prod, settle/prod.
	if views[0].Namespace != "test" || views[1].Namespace != "prod" || views[2].Name != "settle" {
		t.Errorf("order = %s/%s %s/%s %s/%s", views[0].Namespace, views[0].Name, views[1].Namespace, views[1].Name, views[2].Namespace, views[2].Name)
	}
	if views[0].Held != "feed late" || views[0].NextRun != "" {
		t.Errorf("held job = held %q next %q", views[0].Held, views[0].NextRun)
	}
	prod := views[1]
	if prod.Status != jobFailed || prod.LastRun == nil || prod.LastRun.Name != "importer-09932cb3" || prod.NextRun == "" {
		t.Errorf("prod importer = status %s lastRun %+v next %q", prod.Status, prod.LastRun, prod.NextRun)
	}
	if prod.LastSuccess == "" {
		t.Errorf("last success falls back to the newest succeeded run: %+v", prod)
	}
	if views[2].Status != jobIdle || scheduleCell(views[2]) != "on demand" {
		t.Errorf("settle = %s %s", views[2].Status, scheduleCell(views[2]))
	}
	if one, _ := listJobs(ctx, dc, "test"); len(one) != 1 {
		t.Errorf("list -n test = %d", len(one))
	}

	cmd := &cobra.Command{}
	cmd.Flags().AddFlagSet(BatchCmd.PersistentFlags())
	reset := func() { namespaceFlag, envFlag, projectFlag = "", "", defaultProject }
	reset()
	if ns, err := resolveNamespace(ctx, dc, cmd); err != nil || ns != "dev" {
		t.Errorf("no scope flag = %q %v (the dev environment, as adhar apps)", ns, err)
	}
	_ = cmd.Flags().Set("env", "prod")
	if ns, err := resolveNamespace(ctx, dc, cmd); err != nil || ns != "prod" {
		t.Errorf("--env prod = %q %v", ns, err)
	}
	_ = cmd.Flags().Set("project", "team-a")
	_ = cmd.Flags().Set("env", "dev")
	if ns, err := resolveNamespace(ctx, dc, cmd); err != nil || ns != "team-a-dev" {
		t.Errorf("--project team-a --env dev = %q %v", ns, err)
	}
	_ = cmd.Flags().Set("env", "prod")
	if _, err := resolveNamespace(ctx, dc, cmd); err == nil || !strings.Contains(err.Error(), "dev") {
		t.Errorf("an environment the project lacks names what it has: %v", err)
	}
	_ = cmd.Flags().Set("namespace", "x")
	if _, err := resolveNamespace(ctx, dc, cmd); err == nil {
		t.Error("-n and --env together are ambiguous")
	}
	reset()
	if ns, name := parseRef("prod/importer", "dev"); ns != "prod" || name != "importer" {
		t.Errorf("a Console deep link reads as namespace/name: %s %s", ns, name)
	}
	if ns, name := parseRef("importer", "dev"); ns != "dev" || name != "importer" {
		t.Errorf("a bare name takes the scope: %s %s", ns, name)
	}
}

// `runs` filters by phase and limits, newest first; `logs` picks pods by role
// and index, oldest first.
func TestRunsFilterAndPodSelection(t *testing.T) {
	created := testNow.Add(-3 * time.Hour)
	var rows []runRow
	for i, phase := range []string{"failed", "succeeded", "failed", "running"} {
		rows = append(rows, runRowOf(jobObject("r"+string(rune('a'+i)), "prod", "importer", phase, created.Add(time.Duration(i)*time.Hour)), nil, testNow))
	}
	rows = runRowsOf(nil, nil, testNow) // empty is fine
	for i, phase := range []string{"failed", "succeeded", "failed", "running"} {
		rows = append(rows, runRowOf(jobObject("r"+string(rune('a'+i)), "prod", "importer", phase, created.Add(time.Duration(i)*time.Hour)), nil, testNow))
	}
	got, err := filterRuns(rows, "failed", 1)
	if err != nil || len(got) != 1 || got[0].Name != "ra" {
		t.Errorf("filter failed limit 1 = %v %v", names(got), err)
	}
	got, _ = filterRuns(rows, "", 0)
	if len(got) != 4 {
		t.Errorf("limit 0 is all: %d", len(got))
	}
	if _, err := filterRuns(rows, "bogus", 0); err == nil {
		t.Error("an unknown status must be refused")
	}

	mk := func(name, role, index string, at time.Time) unstructured.Unstructured {
		p := podObject(name, "prod", "run", 0, nil, at)
		p.Object["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{labelRole: role, labelNativeIndex: index}
		return *p
	}
	pods := []unstructured.Unstructured{mk("w2", "worker", "2", created.Add(2*time.Minute)), mk("m", "manager", "0", created), mk("w1", "worker", "1", created.Add(time.Minute))}
	if got := selectPods(pods, "worker", false, 0); len(got) != 2 || got[0].GetName() != "w1" {
		t.Errorf("--role worker = %v", podNames(got))
	}
	if got := selectPods(pods, "", true, 2); len(got) != 1 || got[0].GetName() != "w2" {
		t.Errorf("--index 2 = %v", podNames(got))
	}
	if got := selectPods(pods, "", false, 0); len(got) != 3 || got[0].GetName() != "m" {
		t.Errorf("all, oldest first = %v", podNames(got))
	}
}

func names(rows []runRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func podNames(pods []*unstructured.Unstructured) []string {
	var out []string
	for _, p := range pods {
		out = append(out, p.GetName())
	}
	return out
}
