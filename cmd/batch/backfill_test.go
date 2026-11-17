/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

// A run the backfill released is no longer the backfill's: the release takes
// the mark away in the same patch, so when a person then stops that run — and
// Kubernetes 1.36 clears its start time, leaving a Job that reads exactly like
// one never released — the same backfill asked again releases the dates still
// queued and leaves the stopped one stopped.
func TestBackfillNeverResumesARunAPersonStoppedAfterItsRelease(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams))
	d1, d2 := dateRun(t, "2026-11-01"), dateRun(t, "2026-11-02")
	suspended := func(name string) bool {
		s, _, _ := unstructured.NestedBool(getJob(t, dc, "prod", name).Object, "spec", "suspend")
		return s
	}
	mark := func(name string) string { return getJob(t, dc, "prod", name).GetAnnotations()[annotationQueued] }

	// Nothing ends the first date here, so with --parallel 1 the command gives
	// up with the second still queued: an interrupted backfill.
	report, err := backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-02", nil, 1, false, 20*time.Millisecond)
	if code, has := helpers.ExitCodeOf(err); !has || code != 1 || report == nil {
		t.Fatalf("a backfill that ran out of time reports what it did: %+v %v", report, err)
	}
	if suspended(d1) || mark(d1) != "" {
		t.Fatalf("the first date is released, and released without the mark: suspended %v, %s=%q", suspended(d1), annotationQueued, mark(d1))
	}
	if !suspended(d2) || mark(d2) != queuedByBackfill {
		t.Fatalf("the second date waits, marked as the backfill's: suspended %v, %s=%q", suspended(d2), annotationQueued, mark(d2))
	}
	if report.Runs[0].Status != "Running" || report.Runs[1].Status != "Queued" {
		t.Errorf("statuses = %s, %s; want Running, Queued", report.Runs[0].Status, report.Runs[1].Status)
	}

	// A person stops the first date before its pod started; the Job controller
	// of Kubernetes 1.36 leaves no start time and no failed pod behind.
	run := getJob(t, dc, "prod", d1)
	_ = unstructured.SetNestedField(run.Object, true, "spec", "suspend")
	run.Object["status"] = map[string]interface{}{"conditions": []interface{}{
		map[string]interface{}{"type": "Suspended", "status": "True", "reason": "JobSuspended"}}}
	if _, err := dc.Resource(jobGVR).Namespace("prod").Update(ctx, run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	again, err := backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-02", nil, 1, false, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !suspended(d1) {
		t.Error("the backfill resumed a run a person had stopped")
	}
	if suspended(d2) || mark(d2) != "" {
		t.Errorf("the date still queued is released: suspended %v, %s=%q", suspended(d2), annotationQueued, mark(d2))
	}
	if again.Runs[0].Action != "stopped" || again.Runs[0].Status != "Stopped" || again.Runs[1].Action != "exists" {
		t.Errorf("the repeat = %+v", again.Runs)
	}
}

// --parallel 0 (or less) is refused before anything is created: no run could
// ever be released, so every date would sit suspended while the command waited
// for ever.
func TestBackfillRefusesAParallelismThatReleasesNothing(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	for _, parallel := range []int{0, -1} {
		dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams))
		report, err := backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-02", nil, parallel, true, 20*time.Millisecond)
		if err == nil || report != nil || !strings.Contains(err.Error(), "--parallel must be at least 1") {
			t.Errorf("--parallel %d: %+v %v", parallel, report, err)
		}
		if got := jobNames(t, dc); got != "" {
			t.Errorf("--parallel %d created %s before refusing", parallel, got)
		}
	}
}

// D5: the calendar is the runner's. A range across a weekend gets a run for
// every date, each carrying the trigger backfill — what the runner skips a
// non-business day for — and the CLI never reads the calendar itself. A date the
// runner skipped ended with exit 0: the report says Skipped rather than only
// Succeeded, the backfill's exit code is 0, and asked again it runs nothing.
func TestBackfillAcrossAHolidayRunsEveryDateAndShowsWhatWasSkipped(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	at := testNow.Add(-time.Hour)
	// Friday to Monday: the 7th and the 8th of November 2026 are a weekend.
	dates := []string{"2026-11-06", "2026-11-07", "2026-11-08", "2026-11-09"}
	skipped := map[string]interface{}{"v": 1, "outcome": "skipped", "reason": "calendar", "exitCode": 0}
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams),
		podObject("sat", "prod", dateRun(t, "2026-11-07"), 0, skipped, at), podObject("sun", "prod", dateRun(t, "2026-11-08"), 0, skipped, at))
	w := newWorld(t, dc)
	dc.PrependReactor("*", "configmaps", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if get, ok := a.(k8stesting.GetAction); !ok || get.GetName() != "importer-run" {
			t.Errorf("the backfill did %s on ConfigMaps beyond reading its run template; the calendar is the runner's to evaluate", a.GetVerb())
		}
		return false, nil, nil
	})

	report, err := backfill(ctx, dc, "prod", "importer", dates[0], dates[3], nil, 2, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.created) != 4 || len(report.Runs) != 4 {
		t.Fatalf("one run per date, the weekend included: created %d, reported %d", len(w.created), len(report.Runs))
	}
	for i, it := range report.Runs {
		run := w.created[it.Run]
		if it.BusinessDate != dates[i] || run == nil || run.GetLabels()[labelTrigger] != triggerBackfill || envOf(run, envTrigger) != triggerBackfill {
			t.Errorf("%s: run %s must carry the trigger backfill in its label and its environment", dates[i], it.Run)
		}
		want := ""
		if i == 1 || i == 2 {
			want = "Skipped: not a business day"
		}
		if it.Status != "Succeeded" || it.Outcome != want || (want != "" && (it.ExitCode == nil || *it.ExitCode != 0)) {
			t.Errorf("%s: status %s outcome %q exit %v, want Succeeded with outcome %q", dates[i], it.Status, it.Outcome, it.ExitCode, want)
		}
	}
	if report.exitCode() != 0 {
		t.Errorf("a skipped date is not a failed one: exit %d", report.exitCode())
	}

	creates := w.creates
	again, err := backfill(ctx, dc, "prod", "importer", dates[0], dates[3], nil, 2, true, time.Minute)
	if err != nil || w.creates != creates {
		t.Fatalf("a repeat across the same weekend created %d runs: %v", w.creates-creates, err)
	}
	for _, it := range again.Runs {
		if it.Action != "completed" {
			t.Errorf("%s: %s on a repeat, want completed", it.BusinessDate, it.Action)
		}
	}
}

// A backfill asked again while its first dates are still running is the same
// backfill, not a second one beside it: the runs already in flight count
// against --parallel, so the repeat creates nothing and releases nothing until
// one of them ends — and then releases exactly as many as ended. --parallel is
// the invocation's: a larger one releases more.
func TestBackfillRepeatedWhileItsRunsAreInFlightReleasesNothingMore(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams))
	var runs []string
	for _, d := range []string{"2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04"} {
		runs = append(runs, dateRun(t, d))
	}
	// held is which of the four dates are still suspended, in date order.
	held := func() string {
		out := ""
		for _, name := range runs {
			s, _, _ := unstructured.NestedBool(getJob(t, dc, "prod", name).Object, "spec", "suspend")
			out += map[bool]string{true: "Q", false: "R"}[s]
		}
		return out
	}
	again := func(parallel int) (*backfillReport, error) {
		return backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-04", nil, parallel, false, 20*time.Millisecond)
	}
	outOfTime := func(err error) bool {
		code, has := helpers.ExitCodeOf(err)
		return has && code == 1
	}

	// Nothing ends a run here: the first two are released and stay in flight.
	if _, err := again(2); !outOfTime(err) || held() != "RRQQ" {
		t.Fatalf("the first backfill releases two and waits: %s %v", held(), err)
	}
	report, err := again(2)
	if !outOfTime(err) || held() != "RRQQ" {
		t.Fatalf("a repeat with two still in flight released more: %s %v", held(), err)
	}
	if got := strings.Count(jobNames(t, dc), ",") + 1; got != 4 {
		t.Errorf("a repeat created runs: %d exist, want the 4 dates", got)
	}
	for i, want := range []string{"Running", "Running", "Queued", "Queued"} {
		if it := report.Runs[i]; it.Action != "exists" || it.Status != want {
			t.Errorf("%s on the repeat: action %s status %s, want exists/%s", it.BusinessDate, it.Action, it.Status, want)
		}
	}

	// The first date ends: one place is free, and only the third date takes it.
	run := getJob(t, dc, "prod", runs[0])
	run.Object["status"] = map[string]interface{}{"succeeded": int64(1), "conditions": []interface{}{map[string]interface{}{"type": "Complete", "status": "True"}}}
	if _, err := dc.Resource(jobGVR).Namespace("prod").Update(ctx, run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if report, err = again(2); !outOfTime(err) || held() != "RRRQ" {
		t.Fatalf("one run ended, so one more is released: %s %v", held(), err)
	}
	if report.Runs[0].Action != "completed" || report.Runs[3].Status != "Queued" {
		t.Errorf("the repeat = %+v", report.Runs)
	}

	// Asked for three at a time, the last date fits beside the two in flight.
	if _, err = again(3); err != nil || held() != "RRRR" {
		t.Fatalf("--parallel 3 with two in flight releases the last date: %s %v", held(), err)
	}
}

// Without --wait the command ends the moment the last run is released, sooner
// than the Job controller notices: the Job still carries the Suspended
// condition it was created with. That is a run in flight, and the report says
// Running — Stopped is what a person did, and nobody did.
func TestBackfillReportsAJustReleasedRunAsRunning(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams))
	// The Job controller's side: a Job created suspended is marked Suspended, and
	// the mark outlives the release until the next sync, which never comes here.
	dc.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if s, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); s {
			obj.Object["status"] = map[string]interface{}{"conditions": []interface{}{
				map[string]interface{}{"type": "Suspended", "status": "True", "reason": "JobSuspended"}}}
		}
		return false, nil, nil
	})

	report, err := backfill(context.Background(), dc, "prod", "importer", "2026-11-01", "2026-11-03", nil, 2, false, 20*time.Millisecond)
	if code, has := helpers.ExitCodeOf(err); !has || code != 1 || report == nil {
		t.Fatalf("two released, the third still queued when the time ran out: %+v %v", report, err)
	}
	for i, want := range []string{"Running", "Running", "Queued"} {
		if it := report.Runs[i]; it.Status != want || it.ExitCode != nil {
			t.Errorf("%s: status %s exit %v, want %s and no exit code", it.BusinessDate, it.Status, it.ExitCode, want)
		}
	}

	// A person stops the first date: suspended again, and that IS Stopped.
	run := getJob(t, dc, "prod", report.Runs[0].Run)
	_ = unstructured.SetNestedField(run.Object, true, "spec", "suspend")
	if _, err := dc.Resource(jobGVR).Namespace("prod").Update(context.Background(), run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	report, _ = backfill(context.Background(), dc, "prod", "importer", "2026-11-01", "2026-11-03", nil, 2, false, 20*time.Millisecond)
	if it := report.Runs[0]; it.Action != "stopped" || it.Status != "Stopped" {
		t.Errorf("a run a person stopped: action %s status %s, want stopped/Stopped", it.Action, it.Status)
	}

	// A Volcano Job has no spec.suspend to read a release from: Aborted is a
	// person's AbortJob, and stays Stopped.
	vc := fakeCluster(volcanoRun("settle-09932cb3", "Aborted", testNow))
	aborted := &backfillReport{BatchJob: "settle", Namespace: "prod", Runs: []backfillItem{{BusinessDate: "2026-11-03", Run: "settle-09932cb3"}}}
	fillBackfillStatus(context.Background(), vc, aborted)
	if aborted.Runs[0].Status != "Stopped" {
		t.Errorf("an aborted Volcano run: status %s, want Stopped", aborted.Runs[0].Status)
	}
}

// A backfill the API server refuses part way — a quota on Jobs counts the
// suspended ones too — has created some dates and not the rest. The report
// says which: the dates never reached are "not created" with no status, not
// "created"; the ones that exist are Queued, none released, and the error says
// how to go on. The same command then continues from there.
func TestBackfillRefusedPartWaySaysWhichDatesHaveNoRun(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	// The last date ran before, by itself: it is past the refusal and untouched by it.
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams),
		jobObject(dateRun(t, "2026-11-05"), "prod", "importer", "succeeded", testNow.Add(-time.Hour)))
	quota, creates := 2, 0
	dc.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if creates++; creates > quota {
			name := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).GetName()
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, name, errors.New("exceeded quota: environment, requested: count/jobs.batch=1, used: count/jobs.batch=2, limited: count/jobs.batch=2"))
		}
		return false, nil, nil
	})

	report, err := backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-05", nil, 2, true, time.Minute)
	if err == nil || report == nil || !strings.Contains(err.Error(), "exceeded quota") || !strings.Contains(err.Error(), "the same command continues") {
		t.Fatalf("a backfill refused part way reports what it did and why it stopped: %+v %v", report, err)
	}
	for i, want := range [][2]string{{"created", "Queued"}, {"created", "Queued"}, {actionNotCreated, ""}, {actionNotCreated, ""}, {"completed", "Succeeded"}} {
		if it := report.Runs[i]; it.Action != want[0] || it.Status != want[1] {
			t.Errorf("%s: action %q status %q, want %q/%q", it.BusinessDate, it.Action, it.Status, want[0], want[1])
		}
	}
	if got := strings.Count(jobNames(t, dc), ",") + 1; got != 3 {
		t.Fatalf("%d runs exist, want the two the quota allowed beside the one that was there", got)
	}
	for _, it := range report.Runs[:2] {
		if s, _, _ := unstructured.NestedBool(getJob(t, dc, "prod", it.Run).Object, "spec", "suspend"); !s {
			t.Errorf("%s was released by a backfill that could not create every date", it.Run)
		}
	}

	// The quota is raised: the same command creates the rest and goes on.
	quota = 100
	w := newWorld(t, dc, report.Runs[0].Run, report.Runs[1].Run)
	report, err = backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-05", nil, 2, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"exists", "exists", "created", "created", "completed"} {
		if it := report.Runs[i]; it.Action != want || it.Status != "Succeeded" {
			t.Errorf("%s on the repeat: action %s status %s, want %s/Succeeded", it.BusinessDate, it.Action, it.Status, want)
		}
	}
	if w.maxLive > 2 {
		t.Errorf("%d in flight with --parallel 2", w.maxLive)
	}
}
