/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
