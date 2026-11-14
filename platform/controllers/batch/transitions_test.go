package batch

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"adhar-io/adhar/platform/events"
)

// step observes the run as it is now and returns what would be published.
func step(rec *runRecord, run *unstructured.Unstructured, pods ...unstructured.Unstructured) []string {
	return types0(transitionsOf(observe(run, pods), rec, false))
}

func want(t *testing.T, what string, got []string, expected ...string) {
	t.Helper()
	if len(got) == 0 && len(expected) == 0 {
		return
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("%s: published %v, want %v", what, got, expected)
	}
}

// A3.1: every transition of a run is published once, in order, and looking
// again at an unchanged run publishes nothing — the reconcile runs on every
// pod and Job update, and the bus carries transitions only.
func TestEveryRunTransitionIsPublishedExactlyOnce(t *testing.T) {
	rec := &runRecord{UID: "u1"}
	run := runJob("importer-09932cb3", "u1", "manual", map[string]string{"businessDate": "2026-11-03"})
	want(t, "created", step(rec, run), events.TypeBatchRunRequested)
	want(t, "created, again", step(rec, run))

	setStatus(run, "active", int64(1))
	pending := runPod("p1", run, t0, podState{phase: "Pending"})
	want(t, "pod pending", step(rec, run, pending))

	running := runPod("p1", run, t0, podState{phase: "Running", running: true})
	want(t, "pod running", step(rec, run, running), events.TypeBatchRunStarted)
	want(t, "pod running, again", step(rec, run, running))

	done := runPod("p1", run, t0, podState{phase: "Succeeded", exitCode: code(0), message: okSummary})
	setStatus(run, "active", int64(0))
	setStatus(run, "startTime", "2026-11-03T17:30:01Z")
	setStatus(run, "completionTime", "2026-11-03T17:43:40Z")
	setCond(run, "SuccessCriteriaMet", "CompletionsReached", "2026-11-03T17:43:39Z")
	setCond(run, "Complete", "CompletionsReached", "2026-11-03T17:43:40Z")
	want(t, "complete", step(rec, run, done), events.TypeBatchRunSucceeded)
	want(t, "complete, again", step(rec, run, done))

	if !rec.Satisfies || rec.BusinessDate != "2026-11-03" || rec.Seconds != 819 {
		t.Errorf("the record must keep what dependsOn and the SLA need: %+v", rec)
	}
}

// A run nobody saw running (it finished between two looks) still started:
// `started` comes before its end, never after and never not at all.
func TestARunThatFinishedUnseenStillStartsFirst(t *testing.T) {
	rec := &runRecord{UID: "u1"}
	run := runJob("importer-09932cb3", "u1", "manual", nil)
	setCond(run, "Complete", "CompletionsReached", "2026-11-03T17:43:40Z")
	done := runPod("p1", run, t0, podState{phase: "Succeeded", exitCode: code(0), message: okSummary})
	want(t, "first look", step(rec, run, done),
		events.TypeBatchRunRequested, events.TypeBatchRunStarted, events.TypeBatchRunSucceeded)
}

// The clock is not a requester: a scheduled run's first event is `started`.
func TestAScheduledRunIsNotRequested(t *testing.T) {
	rec := &runRecord{UID: "u1"}
	run := runJob("importer-28991234", "u1", "schedule", nil)
	running := runPod("p1", run, t0, podState{phase: "Running", running: true})
	want(t, "scheduled", step(rec, run, running), events.TypeBatchRunStarted)
}

// A3.1: an OOM-killed attempt writes no summary, and is published as
// `run.lost` with the outcome in the event. The run then goes on (the Job's
// backoff makes a replacement pod) and its success is published as usual;
// the lost attempt is not published again.
func TestAnOOMKilledAttemptIsPublishedAsLost(t *testing.T) {
	rec := &runRecord{UID: "u1"}
	run := runJob("importer-09932cb3", "u1", "manual", nil)
	setStatus(run, "active", int64(1))
	first := runPod("p1", run, t0, podState{phase: "Running", running: true})
	want(t, "running", step(rec, run, first), events.TypeBatchRunRequested, events.TypeBatchRunStarted)

	killed := runPod("p1", run, t0, podState{phase: "Failed", exitCode: code(137), reason: "OOMKilled"})
	replacement := runPod("p2", run, t0.Add(time.Minute), podState{phase: "Pending"})
	o := observe(run, []unstructured.Unstructured{killed, replacement})
	ts := transitionsOf(o, rec, false)
	want(t, "oom", types0(ts), events.TypeBatchRunLost)
	if len(ts) == 1 {
		e := runEvent(ts[0], o, nil, hierarchy{})
		var d eventData
		_ = json.Unmarshal(e.Data, &d)
		if d.Pod != "p1" || d.Outcome == nil || d.Outcome.Reason != "OOMKilled" || d.Outcome.ExitCode == nil || *d.Outcome.ExitCode != 137 {
			t.Errorf("run.lost must carry the attempt and its outcome, got %s", e.Data)
		}
	}
	want(t, "oom, again", step(rec, run, killed, replacement))

	done := runPod("p2", run, t0.Add(time.Minute), podState{phase: "Succeeded", exitCode: code(0), message: okSummary})
	setStatus(run, "active", int64(0))
	setCond(run, "Complete", "CompletionsReached", "2026-11-03T17:43:40Z")
	want(t, "recovered", step(rec, run, killed, done), events.TypeBatchRunSucceeded)
}

// An attempt that ended with a summary is an ordinary failed attempt, and one
// that exited 0 with nothing to say is not lost either.
func TestOnlyAnAttemptWithoutASummaryIsLost(t *testing.T) {
	run := runJob("importer-09932cb3", "u1", "manual", nil)
	retryable := runPod("p1", run, t0, podState{phase: "Failed", exitCode: code(5), message: `{"v":1,"outcome":"failed","exitCode":5}`})
	quiet := runPod("p2", run, t0, podState{phase: "Succeeded", exitCode: code(0)})
	if o := observe(run, []unstructured.Unstructured{retryable, quiet}); len(o.Lost) != 0 {
		t.Errorf("lost = %+v, want none", o.Lost)
	}
	nodeGone := runPod("p3", run, t0, podState{phase: "Failed"})
	_ = unstructured.SetNestedField(nodeGone.Object, "NodeLost", "status", "reason")
	if o := observe(run, []unstructured.Unstructured{nodeGone}); len(o.Lost) != 1 || o.Lost[0].Reason != "NodeLost" {
		t.Errorf("a pod that failed without a container end is lost with its reason, got %+v", o.Lost)
	}
}

// A pod last seen Running that disappears with no end (its node was
// force-removed) is a lost attempt too: nothing else would ever say so.
func TestAPodThatVanishesWhileRunningIsLost(t *testing.T) {
	rec := &runRecord{UID: "u1"}
	run := runJob("importer-09932cb3", "u1", "manual", nil)
	setStatus(run, "active", int64(1))
	running := runPod("p1", run, t0, podState{phase: "Running", running: true})
	step(rec, run, running)
	want(t, "vanished pod", step(rec, run), events.TypeBatchRunLost)
	want(t, "vanished pod, again", step(rec, run))
}

// A run's stop and resume are published each time — a run can be stopped,
// resumed and stopped again, and each is a separate thing someone did.
func TestStopAndResumeArePublishedEachTime(t *testing.T) {
	rec := &runRecord{UID: "u1", Requested: true, Started: true}
	run := runJob("importer-09932cb3", "u1", "manual", nil)
	_ = unstructured.SetNestedField(run.Object, true, "spec", "suspend")
	want(t, "stopped", step(rec, run), events.TypeBatchRunStopped)
	want(t, "stopped, again", step(rec, run))
	_ = unstructured.SetNestedField(run.Object, false, "spec", "suspend")
	setStatus(run, "active", int64(1))
	want(t, "resumed", step(rec, run), events.TypeBatchRunResumed)
	_ = unstructured.SetNestedField(run.Object, true, "spec", "suspend")
	setStatus(run, "active", int64(0))
	want(t, "stopped twice", step(rec, run), events.TypeBatchRunStopped)
}

// Exit 0 covers four outcomes (D13). A run the kit skipped is `run.skipped`,
// never also `run.succeeded`, and only "already-complete" counts as succeeded
// for a job that depends on it.
func TestASkippedRunIsSkippedAndOnlyAlreadyCompleteSatisfiesADependency(t *testing.T) {
	for reason, satisfies := range map[string]bool{"already-complete": true, "calendar": false, "duplicate": false} {
		rec := &runRecord{UID: "u1", Requested: true, Started: true}
		run := runJob("importer-09932cb3", "u1", "dependency", nil)
		setCond(run, "Complete", "CompletionsReached", "2026-11-03T17:43:40Z")
		pod := runPod("p1", run, t0, podState{phase: "Succeeded", exitCode: code(0),
			message: `{"v":1,"outcome":"skipped","reason":"` + reason + `","exitCode":0,"businessDate":"2026-11-03"}`})
		want(t, reason, step(rec, run, pod), events.TypeBatchRunSkipped)
		if rec.Satisfies != satisfies {
			t.Errorf("%s: satisfies a dependency = %v, want %v", reason, rec.Satisfies, satisfies)
		}
	}
}

// The first look at a job with history records it and publishes none of it:
// a newly deployed observer replaying a week of runs would look exactly like
// a week of runs.
func TestTheFirstLookRecordsHistoryWithoutPublishingIt(t *testing.T) {
	rec := &runRecord{UID: "u1"}
	run := runJob("importer-09932cb3", "u1", "manual", nil)
	setStatus(run, "startTime", "2026-11-03T17:30:00Z")
	setCond(run, "Complete", "CompletionsReached", "2026-11-03T17:40:00Z")
	pod := runPod("p1", run, t0, podState{phase: "Succeeded", exitCode: code(0), message: okSummary})
	if got := transitionsOf(observe(run, []unstructured.Unstructured{pod}), rec, true); len(got) != 0 {
		t.Errorf("baseline published %v", types0(got))
	}
	if rec.End != endSucceeded || !rec.Satisfies || rec.Seconds != 600 {
		t.Errorf("baseline must still record the run: %+v", rec)
	}
	// An active run at the first look is news, and is published.
	rec2 := &runRecord{UID: "u2"}
	active := runJob("importer-aaaa0000", "u2", "manual", nil)
	if got := transitionsOf(observe(active, nil), rec2, true); len(got) != 1 {
		t.Errorf("an active run at baseline: published %v, want requested", types0(got))
	}
}

// An event belongs where the run IS: the hierarchy comes from the namespace,
// the subject is <namespace>/<batchjob> (the record key, so one job's events
// keep their order), the correlation is the run's UID, and a sensitive
// identifying value never reaches the bus (D8).
func TestRunEventsCarryTheNamespaceHierarchyAndMaskSensitiveValues(t *testing.T) {
	run := runJob("importer-09932cb3", "u1", "manual", map[string]string{"businessDate": "2026-11-03", "account": "acme-42", "dryRun": "false"})
	setCond(run, "Failed", "BackoffLimitExceeded", "2026-11-03T17:43:40Z")
	pod := runPod("p1", run, t0, podState{phase: "Failed", exitCode: code(3), message: `{"v":1,"outcome":"failed","exitCode":3,"businessDate":"2026-11-03"}`})
	o := observe(run, []unstructured.Unstructured{pod})
	fields := []paramField{
		{Name: "businessDate", Type: "date", Identifying: true},
		{Name: "account", Type: "string", Identifying: true, Sensitive: true},
		{Name: "dryRun", Type: "boolean", Identifying: false},
	}
	h := hierarchyOf(map[string]string{"adhar.io/organisation": "acme", "adhar.io/team": "payments", "adhar.io/project": "ledger", "adhar.io/environment": "prod", "adhar.io/plane": "workload"})
	e := runEvent(transition{Type: events.TypeBatchRunFailed}, o, fields, h)
	if e.Subject != "prod/importer" || e.Correlation != "u1" || e.Organisation != "acme" || e.Team != "payments" ||
		e.Project != "ledger" || e.Environment != "prod" || e.Plane != "workload" {
		t.Errorf("envelope = %+v", e)
	}
	if topic, ok := events.TopicFor(e.Type); !ok || topic != events.TopicBatch {
		t.Errorf("%s routes to %q", e.Type, topic)
	}
	var d eventData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	id := d.SpringBatch.Identifying
	if id["businessDate"] != "2026-11-03" || id["account"] != "••••••" || id["dryRun"] != "" {
		t.Errorf("identifying = %v: the business date is shown, a sensitive value masked, a non-identifying one left out", id)
	}
	if d.Outcome == nil || d.Outcome.ExitCode == nil || *d.Outcome.ExitCode != 3 || d.Outcome.Reason != "BackoffLimitExceeded" {
		t.Errorf("run.failed must carry its outcome, got %+v", d.Outcome)
	}
	if d.Run == nil || d.Run.Actor != "alice@example.com" || d.Run.Trigger != "manual" {
		t.Errorf("run = %+v", d.Run)
	}
}
