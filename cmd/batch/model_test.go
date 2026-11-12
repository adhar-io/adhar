package batch

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The phase a person reads comes from the Job's conditions and suspend flag,
// the way the Console's runPhaseOf reads them. FailureTarget and
// SuccessCriteriaMet count: the Job has decided, its pods are only leaving.
func TestRunPhaseReadsJobConditions(t *testing.T) {
	for phase, want := range map[string]runPhase{
		"succeeded": phaseSucceeded, "failed": phaseFailed, "running": phaseRunning,
		"pending": phasePending, "stopped": phaseStopped, "stopping": phaseStopping,
	} {
		if got := runPhaseOf(jobObject("r", "prod", "importer", phase, testNow)); got != want {
			t.Errorf("%s: phase = %s, want %s", phase, got, want)
		}
	}
	decided := jobObject("r", "prod", "importer", "running", testNow)
	decided.Object["status"].(map[string]interface{})["conditions"] = []interface{}{map[string]interface{}{"type": "FailureTarget", "status": "True"}}
	if got := runPhaseOf(decided); got != phaseFailed {
		t.Errorf("FailureTarget with pods still active = %s, want failed", got)
	}
	deleting := jobObject("r", "prod", "importer", "running", testNow)
	deleting.Object["metadata"].(map[string]interface{})["deletionTimestamp"] = testNow.Format(time.RFC3339)
	if got := runPhaseOf(deleting); got != phaseDeleting {
		t.Errorf("a deletionTimestamp = %s, want deleting", got)
	}
	volcano := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch.volcano.sh/v1alpha1", "kind": "Job",
		"metadata": map[string]interface{}{"name": "v", "namespace": "prod"},
		"status":   map[string]interface{}{"state": map[string]interface{}{"phase": "Aborted"}},
	}}
	if got := runPhaseOf(volcano); got != phaseStopped {
		t.Errorf("a Volcano Aborted job = %s, want stopped", got)
	}
}

// The termination summary is the run's result (§5.13, D13). A whole document
// reads as JSON; one the kubelet cut still yields the fields before the cut
// and says so; text that is not a summary is nil, never a guess.
func TestSummaryToleratesTruncation(t *testing.T) {
	whole := `{"v":1,"job":"importJob","batchJob":"importer","instanceId":42,"executionId":97,"status":"COMPLETED","exitCode":0,"outcome":"skipped","reason":"already-complete","trigger":"manual","businessDate":"2026-11-03","read":120000,"written":119990,"skipped":10,"steps":[{"name":"importStep","status":"COMPLETED"}]}`
	s := parseSummary(whole)
	if s == nil || s.Truncated || *s.ExitCode != 0 || *s.ExecutionID != 97 || s.BusinessDate != "2026-11-03" || *s.Read != 120000 {
		t.Fatalf("whole summary = %+v", s)
	}
	if got := outcomeLabel(s); got != "Skipped: this identity already completed" {
		t.Errorf("outcomeLabel = %q", got)
	}
	cut := whole[:len(whole)-40]
	s = parseSummary(cut)
	if s == nil || !s.Truncated || s.Outcome != "skipped" || *s.ExecutionID != 97 || *s.Read != 120000 {
		t.Fatalf("cut summary = %+v", s)
	}
	if parseSummary("Exception in thread main") != nil || parseSummary("") != nil || parseSummary(`{"hello":1}`) != nil {
		t.Error("text that is not a summary must be nil")
	}
	if exitMeaning(3) != "failed, not retryable" || exitMeaning(99) != "exit 99" {
		t.Errorf("exitMeaning: %q %q", exitMeaning(3), exitMeaning(99))
	}
	if restartWarning(intp(2)) == "" || restartWarning(intp(6)) == "" || restartWarning(intp(5)) != "" || restartWarning(nil) != "" {
		t.Error("exit 2 and 6 restart the same way; every other code restarts cleanly")
	}
}

func intp(n int) *int { return &n }

// A run's row takes its exit code and business date from the summary of the
// pod that speaks for it, and its trigger from the label; a scheduled run with
// no params gets its date from the scheduled timestamp in the job's zone.
func TestRunRowReadsTheSpeakerPodsSummary(t *testing.T) {
	created := testNow.Add(-time.Hour)
	run := jobObject("importer-09932cb3", "prod", "importer", "failed", created)
	older := podObject("importer-09932cb3-aaa", "prod", "importer-09932cb3", 5, map[string]interface{}{"v": 1, "outcome": "failed", "exitCode": 5}, created)
	newer := podObject("importer-09932cb3-bbb", "prod", "importer-09932cb3", 3, map[string]interface{}{"v": 1, "outcome": "failed", "exitCode": 3, "executionId": 7, "businessDate": "2026-11-03"}, created.Add(10*time.Minute))
	other := podObject("other-pod", "prod", "other-run", 0, nil, created)
	row := runRowOf(run, []unstructured.Unstructured{*older, *other, *newer}, testNow)
	if row.Phase != phaseFailed || row.ExitCode == nil || *row.ExitCode != 3 || row.Attempts != 2 {
		t.Fatalf("row = phase %s exit %v attempts %d", row.Phase, row.ExitCode, row.Attempts)
	}
	if row.Trigger != "manual" || row.Actor != "alice" || row.BusinessDate != "2026-11-03" || row.Summary.ExecutionID == nil {
		t.Errorf("row = trigger %q actor %q date %q summary %+v", row.Trigger, row.Actor, row.BusinessDate, row.Summary)
	}
	if !row.HasDuration || row.Duration != 5*time.Minute {
		t.Errorf("a failed run's duration ends at its Failed condition: %v %v", row.HasDuration, row.Duration)
	}
	if !strings.Contains(row.Message, "exit code 3") {
		t.Errorf("the Failed condition's message is the row's: %q", row.Message)
	}

	scheduled := jobObject("importer-29210000-abcde", "prod", "importer", "running", created)
	meta := scheduled.Object["metadata"].(map[string]interface{})
	delete(meta["labels"].(map[string]interface{}), labelTrigger)
	meta["annotations"] = map[string]interface{}{annotationScheduled: "2026-11-03T17:30:00Z"}
	scheduled.Object["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})["env"] = []interface{}{
		map[string]interface{}{"name": "ADHAR_BATCH_PARAMS", "value": "{}"},
		map[string]interface{}{"name": "ADHAR_BATCH_TIME_ZONE", "value": "Asia/Singapore"},
	}
	srow := runRowOf(scheduled, nil, testNow)
	if srow.Trigger != "schedule" || srow.BusinessDate != "2026-11-04" {
		t.Errorf("a scheduled run: trigger %q, business date %q (17:30Z is the 4th in Singapore)", srow.Trigger, srow.BusinessDate)
	}
	if !srow.HasDuration || srow.Duration != time.Hour {
		t.Errorf("a running run's duration runs to now: %v", srow.Duration)
	}
	rows := runRowsOf([]unstructured.Unstructured{*run, *scheduled}, nil, testNow)
	if rows[0].Name != "importer-09932cb3" && rows[0].Created.Before(rows[1].Created) {
		t.Error("rows are newest first")
	}
}

// `adhar batch run --wait` carries §12.1's idempotency (D2), judged on the
// LATEST member of the restart chain: completed → reuse, in flight → wait,
// failed → the next -r<n>, stopped → report and point at resume.
func TestDecideRunCarriesD2Idempotency(t *testing.T) {
	base := "importer-09932cb3"
	row := func(name, phase string) runRow {
		return runRowOf(jobObject(name, "prod", "importer", phase, testNow), nil, testNow)
	}
	d, _ := decideRun(base, nil)
	if d.Action != "create" || d.Target != base {
		t.Errorf("nothing exists: %+v", d)
	}
	d, _ = decideRun(base, []runRow{row(base, "succeeded"), row("importer-ffffffff", "failed")})
	if d.Action != "reuse" || d.Target != base {
		t.Errorf("completed: %+v (another identity's failure is not this one's)", d)
	}
	d, _ = decideRun(base, []runRow{row(base, "running")})
	if d.Action != "wait" || d.Target != base {
		t.Errorf("in flight: %+v", d)
	}
	d, _ = decideRun(base, []runRow{row(base, "failed")})
	if d.Action != "restart" || d.Target != base+"-r1" {
		t.Errorf("failed: %+v", d)
	}
	// The base failed and r1 succeeded: the identity is done. r1 failed: r2 is next.
	d, _ = decideRun(base, []runRow{row(base, "failed"), row(base+"-r1", "succeeded")})
	if d.Action != "reuse" || d.Target != base+"-r1" {
		t.Errorf("restart succeeded: %+v", d)
	}
	d, _ = decideRun(base, []runRow{row(base, "failed"), row(base+"-r1", "failed")})
	if d.Action != "restart" || d.Target != base+"-r2" {
		t.Errorf("restart failed: %+v", d)
	}
	d, _ = decideRun(base, []runRow{row(base, "stopped")})
	if d.Action != "stopped" || d.Target != base {
		t.Errorf("stopped: %+v", d)
	}
	if _, err := decideRun(base, []runRow{row(base+"-r99", "failed")}); err == nil {
		t.Error("r99 failed: there is no r100")
	}
}

// -o json prints {run, namespace, batchJob, kind, status, exitCode,
// executionId, summary}; the process exit code is the run's, 0 includes
// SKIPPED, a stopped run is 4, a failure whose code was lost is 1.
func TestOutcomeIsD2sShape(t *testing.T) {
	created := testNow.Add(-time.Hour)
	skipped := podObject("p", "prod", "importer-09932cb3", 0, map[string]interface{}{"v": 1, "outcome": "skipped", "reason": "already-complete", "exitCode": 0, "executionId": 97}, created)
	row := runRowOf(jobObject("importer-09932cb3", "prod", "importer", "succeeded", created), []unstructured.Unstructured{*skipped}, testNow)
	out := outcomeOf(row)
	if out.Status != "Succeeded" || out.ExitCode != 0 || *out.ExecutionID != 97 || out.Summary["reason"] != "already-complete" || out.BatchJob != "importer" || out.Kind != "Job" {
		t.Errorf("skipped run = %+v", out)
	}
	failed := podObject("p", "prod", "importer-09932cb3", 3, map[string]interface{}{"v": 1, "outcome": "failed", "exitCode": 3}, created)
	out = outcomeOf(runRowOf(jobObject("importer-09932cb3", "prod", "importer", "failed", created), []unstructured.Unstructured{*failed}, testNow))
	if out.Status != "Failed" || out.ExitCode != 3 {
		t.Errorf("failed run = %+v", out)
	}
	out = outcomeOf(runRowOf(jobObject("importer-09932cb3", "prod", "importer", "failed", created), nil, testNow))
	if out.Status != "Failed" || out.ExitCode != 1 {
		t.Errorf("a failure with no pod left = %+v", out)
	}
	out = outcomeOf(runRowOf(jobObject("importer-09932cb3", "prod", "importer", "stopped", created), nil, testNow))
	if out.Status != "Stopped" || out.ExitCode != 4 {
		t.Errorf("stopped run = %+v", out)
	}
	out = outcomeOf(runRowOf(jobObject("importer-09932cb3", "prod", "importer", "running", created), nil, testNow))
	if out.Status != "Running" || out.ExitCode != 0 {
		t.Errorf("running run = %+v", out)
	}
	for _, want := range []string{"run", "namespace", "batchJob", "kind", "status", "exitCode", "executionId", "summary"} {
		if !strings.Contains(toJSON(out), `"`+want+`"`) {
			t.Errorf("-o json lacks %q: %s", want, toJSON(out))
		}
	}
}

// Stop and resume are the Job's own suspend flag; a Volcano run gets the
// bus.volcano.sh Command vcctl would create. Hold and release are the ONE
// annotation an operator may change. These are byte-for-byte the Console's
// stopRequest, resumeRequest and holdPatch.
func TestStopResumeAndHoldAreTheConsolesWrites(t *testing.T) {
	run := jobObject("importer-09932cb3", "prod", "importer", "running", testNow)
	stop := stopRequest(run)
	if stop.Verb != "patch" || stop.GVR != jobGVR || stop.Name != run.GetName() || toJSON(stop.Body) != `{"spec":{"suspend":true}}` {
		t.Errorf("stop = %+v", stop)
	}
	if resume := resumeRequest(run); toJSON(resume.Body) != `{"spec":{"suspend":false}}` {
		t.Errorf("resume = %+v", resume)
	}
	volcano := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch.volcano.sh/v1alpha1", "kind": "Job",
		"metadata": map[string]interface{}{"name": "v", "namespace": "prod", "uid": "u1"},
	}}
	cmd := stopRequest(volcano)
	if cmd.Verb != "create" || cmd.GVR != commandGVR || cmd.Body["action"] != "AbortJob" {
		t.Errorf("volcano stop = %+v", cmd)
	}
	target := cmd.Body["target"].(map[string]interface{})
	if target["name"] != "v" || target["uid"] != "u1" || target["kind"] != "Job" {
		t.Errorf("the Command targets the job it stops: %v", target)
	}
	if resumeRequest(volcano).Body["action"] != "ResumeJob" {
		t.Error("volcano resume is ResumeJob")
	}

	reason := "  upstream feed late "
	if got := toJSON(holdPatch(&reason)); got != `{"metadata":{"annotations":{"adhar.io/batch-hold":"upstream feed late"}}}` {
		t.Errorf("hold patch = %s", got)
	}
	if got := toJSON(holdPatch(nil)); got != `{"metadata":{"annotations":{"adhar.io/batch-hold":null}}}` {
		t.Errorf("release patch = %s", got)
	}

	// The gates: stop while it can still stop, resume what was stopped, restart what failed.
	for phase, want := range map[string]runActions{
		"pending": {Stop: true}, "running": {Stop: true}, "stopped": {Resume: true}, "failed": {Restart: true},
		"succeeded": {}, "stopping": {},
	} {
		if got := runActionsOf(runPhaseOf(jobObject("r", "prod", "importer", phase, testNow))); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: actions = %+v, want %+v", phase, got, want)
		}
	}
	if _, err := suspendRequest(jobObject("r", "prod", "importer", "succeeded", testNow), true); err == nil {
		t.Error("a finished run cannot be stopped")
	}
	if _, err := suspendRequest(jobObject("r", "prod", "importer", "running", testNow), false); err == nil {
		t.Error("a running run cannot be resumed")
	}
}

// A job's one-phrase status: what its runs say outranks a definition still
// reconciling; a hold is read from the annotation; next run is nothing for a
// held, suspended or on-demand job.
func TestJobStatusAndNextRun(t *testing.T) {
	job := batchJobXR("importer", "prod", "30 1 * * *", "")
	row := func(name, phase string) runRow {
		return runRowOf(jobObject(name, "prod", "importer", phase, testNow), nil, testNow)
	}
	if s := jobStatusOf(job, nil); s != jobIdle {
		t.Errorf("no runs = %s", s)
	}
	if s := jobStatusOf(job, []runRow{row("a", "failed"), row("b", "succeeded")}); s != jobFailed {
		t.Errorf("newest failed = %s", s)
	}
	if s := jobStatusOf(job, []runRow{row("a", "failed"), row("b", "running")}); s != jobRunning {
		t.Errorf("anything active = %s", s)
	}
	broken := batchJobXR("importer", "prod", "", "")
	broken.Object["status"].(map[string]interface{})["conditions"] = []interface{}{map[string]interface{}{"type": "PrerequisitesReady", "status": "False", "reason": "PriorityClassMissing", "message": "PriorityClass adhar-batch does not exist"}}
	if s := jobStatusOf(broken, []runRow{row("a", "succeeded")}); s != jobNotReady {
		t.Errorf("a False condition with no failure = %s", s)
	}
	if p := jobProblem(broken); !strings.Contains(p, "PriorityClass") {
		t.Errorf("jobProblem = %q", p)
	}
	next, ok := nextRunOf(job, testNow)
	// 23:30Z on the 3rd is already 07:30 on the 4th in Singapore, past that day's fire.
	if !ok || next.Format(time.RFC3339) != "2026-11-05T01:30:00+08:00" {
		t.Errorf("next run of 30 1 * * * in Singapore after 23:30Z on the 3rd = %v %v", next, ok)
	}
	held := batchJobXR("importer", "prod", "30 1 * * *", "feed late")
	if _, ok := nextRunOf(held, testNow); ok {
		t.Error("a held job has no next run")
	}
	if r, ok := heldReason(held); !ok || r != "feed late" {
		t.Errorf("heldReason = %q %v", r, ok)
	}
	if _, ok := nextRunOf(batchJobXR("importer", "prod", "", ""), testNow); ok {
		t.Error("an on-demand job has no next run")
	}
}

// A restart reuses the run's IDENTIFYING parameters; a scheduled run that got
// its date from the clock has the date stated explicitly so the restart
// continues that instance rather than starting a new one.
func TestRestartPlanStatesTheScheduledDate(t *testing.T) {
	tpl := mustParseTemplate(t, `[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"},{"name":"region","identifying":true,"default":"sg"}]`)
	created := testNow.Add(-time.Hour)
	scheduled := jobObject("importer-29210000-abcde", "prod", "importer", "failed", created)
	meta := scheduled.Object["metadata"].(map[string]interface{})
	// Fired two days before "today": the restart must continue THAT date, not
	// fall back to today's as a manual run would.
	meta["annotations"] = map[string]interface{}{annotationScheduled: "2026-11-01T17:30:00Z"}
	scheduled.Object["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})["env"] = []interface{}{
		map[string]interface{}{"name": "ADHAR_BATCH_PARAMS", "value": "{}"},
		map[string]interface{}{"name": "ADHAR_BATCH_TIME_ZONE", "value": "Asia/Singapore"},
	}
	row := runRowOf(scheduled, nil, testNow)
	plan, err := planRestart("importer", row, tpl, []string{"importer-29210000-abcde"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Identifying["businessDate"] != "2026-11-02" || plan.Identifying["region"] != "sg" {
		t.Errorf("identifying = %v", plan.Identifying)
	}
	base, _ := runName("importer", map[string]string{"businessDate": "2026-11-02", "region": "sg"})
	if plan.Base != base || plan.Name != base+"-r1" {
		t.Errorf("plan = %s / %s, want %s-r1", plan.Base, plan.Name, base)
	}
	if plan.ImageFrom != "img" || !strings.HasPrefix(plan.ImageTo, "harbor.") {
		t.Errorf("the image moved since the run and the plan must say so: %q → %q", plan.ImageFrom, plan.ImageTo)
	}
	// A manual run's params name both identifying parameters; its chain is that base.
	manualBase, _ := runName("importer", map[string]string{"businessDate": "2026-11-03", "region": "sg"})
	manual := runRowOf(jobObject(manualBase, "prod", "importer", "failed", created), nil, testNow)
	plan, err = planRestart("importer", manual, tpl, []string{manualBase, manualBase + "-r1"}, testNow)
	if err != nil || plan.Name != manualBase+"-r2" {
		t.Errorf("a manual run restarts with its own params as the next -r<n>: %+v %v", plan, err)
	}
}
