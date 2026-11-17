/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Backfill (design 0025 §8.1, A3.3): one run per business date, each named for
// its identity like any run, so a backfill asked for twice finds its runs and
// starts nothing twice. How many run at once is the CLI's to enforce on the
// native path and the queue's on Volcano's:
//
//   - native: every date's Job is created SUSPENDED (spec.suspend is not part
//     of what adhar-batch-run-integrity compares), and the CLI releases them in
//     date order, never letting more than --parallel be in flight. It stays
//     until the last one is released, or with --wait until every run ended.
//     Interrupted, the rest stay suspended; the same command again releases them.
//   - Volcano: every run is created at once and the BatchJob's queue decides
//     how many start; --parallel is not applied, because a CLI holding runs back
//     would fight the fair share the queue exists for.
//
// A run counts as in flight from the moment it is released until it ends, and
// one a person suspended while it ran keeps counting until its pods are gone.
//
// A run the backfill holds back and a run a person stopped are the same Job to
// read — suspended, no pods — and since Kubernetes 1.36 not even status.startTime
// tells them apart (MutableSchedulingDirectivesForSuspendedJobs: the Job
// controller clears it on every suspend). So the backfill says which runs are
// its own: it creates them annotated adhar.io/batch-queued=backfill and removes
// the annotation in the patch that releases them. Only a run still carrying it
// is ever released; anything else suspended is a person's, and is left alone.

var (
	backfillFrom      string
	backfillTo        string
	backfillParallel  int
	backfillWait      bool
	backfillParamArgs []string
	backfillTimeout   time.Duration
)

// maxBackfillDates bounds one backfill: a mistyped year would otherwise create
// thousands of Jobs before anyone noticed.
const maxBackfillDates = 366

// annotationQueued marks a run created suspended that its creator has not
// released yet; the value names the creator. On the run only, which run
// integrity does not compare (§10.3), and removed by resumeRequest.
const (
	annotationQueued = "adhar.io/batch-queued"
	queuedByBackfill = "backfill"
)

var backfillCmd = &cobra.Command{
	Use:   "backfill <job>",
	Short: "Run a job once per business date in a range, at most --parallel at a time",
	Long: `Run a job once for every business date from --from to --to, inclusive. Each
date is its own run, named for its identity (<job>-<hash8>), so a backfill
repeated starts only what has not run: a date that completed is left alone, one
in flight is waited for, and one that failed is restarted as the next -r<n>.

On the native path the runs are created suspended and released in date order,
at most --parallel at a time, while the command waits; it ends once the last
one is released, or with --wait once every run has ended (exit 1 when any
failed). Stopped part way, the rest stay suspended until the same command is
run again. Where the job runs on Volcano, every run is created at once and the
job's queue decides how many start.

The date goes into the job's business-date parameter (businessDate, else the
first date parameter its schedule supplies); -p gives the others. Every date in
the range gets a run, weekends and holidays included: the job's calendar is the
runner's to read, and a date that is not a business day ends at once as
Skipped, exit 0.

Only the runs a backfill queued (annotation adhar.io/batch-queued) are ever
released by one; a run a person stopped stays stopped until it is resumed.`,
	Example: `  adhar batch backfill importer --from 2026-11-01 --to 2026-11-07 --parallel 2
  adhar batch backfill importer --from 2026-11-01 --to 2026-11-07 -p region=uk --wait -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Checked here, not with MarkFlagRequired: a missing date is a sentence
		// to read, not forty lines of usage (silenceUsageOnceParsed).
		if backfillFrom == "" || backfillTo == "" {
			return fmt.Errorf("--from and --to name the first and last business date, yyyy-mm-dd")
		}
		cl, err := connect(cmd)
		if err != nil {
			return err
		}
		ns, err := resolveNamespace(ctx(cmd), cl.dc, cmd)
		if err != nil {
			return err
		}
		ns, name := parseRef(args[0], ns)
		extra, err := parseParamArgs(backfillParamArgs)
		if err != nil {
			return err
		}
		report, err := backfill(ctx(cmd), cl.dc, ns, name, backfillFrom, backfillTo, extra, backfillParallel, backfillWait, backfillTimeout)
		if err != nil && report == nil {
			return err
		}
		if machineReadable() {
			if perr := printMachine(report); perr != nil {
				return perr
			}
		} else {
			printBackfill(report)
		}
		if err != nil {
			return err
		}
		if code := report.exitCode(); code != 0 {
			return helpers.ExitCode(code, nil)
		}
		return nil
	},
}

func init() {
	backfillCmd.Flags().StringVar(&backfillFrom, "from", "", "First business date, yyyy-mm-dd")
	backfillCmd.Flags().StringVar(&backfillTo, "to", "", "Last business date, yyyy-mm-dd (inclusive)")
	backfillCmd.Flags().IntVar(&backfillParallel, "parallel", 1, "At most this many runs in flight at once (native path; on Volcano the queue decides)")
	backfillCmd.Flags().BoolVar(&backfillWait, "wait", false, "Wait for every run to end; exit 1 when any failed")
	backfillCmd.Flags().StringArrayVarP(&backfillParamArgs, "param", "p", nil, "Another parameter as key=value, the same for every date (repeatable)")
	backfillCmd.Flags().DurationVar(&backfillTimeout, "timeout", 0, "Give up waiting after this long (default: no limit); the runs keep going")
}

// businessDateField is the parameter a business date goes into: the one named
// businessDate, else the first date parameter the schedule supplies — the
// batch observer's rule for a dependency run, so a backfill and a dependency
// run of the same date are the same identity.
func businessDateField(fields []paramField) (paramField, bool) {
	for _, f := range fields {
		if f.Name == "businessDate" {
			return f, true
		}
	}
	for _, f := range fields {
		if f.Type == "date" && f.Source == "schedule" {
			return f, true
		}
	}
	return paramField{}, false
}

// backfillDates is every date from from to to, inclusive, in order.
func backfillDates(from, to string) ([]string, error) {
	start, err := time.Parse("2006-01-02", from)
	if err != nil || start.Format("2006-01-02") != from {
		return nil, fmt.Errorf("--from %q is not a date, yyyy-mm-dd", from)
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil || end.Format("2006-01-02") != to {
		return nil, fmt.Errorf("--to %q is not a date, yyyy-mm-dd", to)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("--to %s is before --from %s", to, from)
	}
	var out []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if len(out) == maxBackfillDates {
			return nil, fmt.Errorf("%s to %s is more than %d dates; backfill it in parts", from, to, maxBackfillDates)
		}
		out = append(out, d.Format("2006-01-02"))
	}
	return out, nil
}

// backfillItem is one date of a backfill.
type backfillItem struct {
	BusinessDate string `json:"businessDate" yaml:"businessDate"`
	Run          string `json:"run" yaml:"run"`
	// Action is what this invocation found or did: created, restarted, exists
	// (in flight, or still queued from an earlier invocation), completed,
	// stopped (suspended by a person after it started: left alone), or not
	// created (the backfill was refused part way, before this date's run).
	Action string `json:"action" yaml:"action"`
	// Status is the run's as it stands (D2's, or Queued, or Gone); empty for a
	// date whose run was not created.
	Status string `json:"status" yaml:"status"`
	// Outcome is the run's own word for how it ended, from its summary: exit 0
	// is also a date the runner skipped because the calendar says it is not a
	// business day (D5), and a range across a holiday should show which.
	Outcome  string `json:"outcome,omitempty" yaml:"outcome,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty" yaml:"exitCode,omitempty"`

	params    map[string]string
	restartOf string
}

// backfillReport is the outcome of one backfill, `-o json`'s document.
type backfillReport struct {
	BatchJob  string         `json:"batchJob" yaml:"batchJob"`
	Namespace string         `json:"namespace" yaml:"namespace"`
	Scheduler string         `json:"scheduler" yaml:"scheduler"`
	Parallel  int            `json:"parallel" yaml:"parallel"`
	Waited    bool           `json:"waited" yaml:"waited"`
	Runs      []backfillItem `json:"runs" yaml:"runs"`
}

// exitCode is 1 when a date failed and the command waited to know it, 4 when
// one was stopped by a person, else 0.
func (r *backfillReport) exitCode() int {
	if !r.Waited {
		return 0
	}
	code := 0
	for _, it := range r.Runs {
		switch it.Status {
		case "Failed":
			return 1
		case "Stopped":
			code = 4
		}
	}
	return code
}

// actionNotCreated is the action of a date a refused backfill never reached.
const actionNotCreated = "not created"

// The state of a backfill's run, as the release loop counts it.
const (
	bfDone     = "done"     // ended: succeeded or failed (or gone)
	bfQueued   = "queued"   // created suspended and never started: ours to release
	bfInFlight = "inflight" // released and not ended, or still stopping
	bfStopped  = "stopped"  // a person suspended it after it started
)

// backfillState classifies one run. Phase alone cannot: a Job created
// suspended reads as Stopped exactly as a run a person stopped does, and one
// just released can still carry its Suspended condition for a sync. A
// conservative count is the A3.3 guarantee, so anything not provably queued,
// stopped or ended is in flight. Queued is the one state the backfill acts on
// (it resumes the run), so it needs the backfill's own mark AND no sign the run
// ever started: a suspended run without the mark was stopped by a person, or
// queued by something this command knows nothing about.
func backfillState(r runRow) string {
	if r.Phase == phaseSucceeded || r.Phase == phaseFailed || r.Phase == phaseDeleting {
		return bfDone
	}
	suspended, _, _ := unstructured.NestedBool(r.obj.Object, "spec", "suspend")
	if !suspended {
		if r.Phase == phaseStopped && r.Kind == "VolcanoJob" {
			return bfStopped
		}
		return bfInFlight
	}
	if statusInt(r.obj, "active") > 0 || statusInt(r.obj, "terminating") > 0 {
		return bfInFlight
	}
	if r.obj.GetAnnotations()[annotationQueued] != queuedByBackfill {
		return bfStopped
	}
	started, _, _ := unstructured.NestedString(r.obj.Object, "status", "startTime")
	if started == "" && statusInt(r.obj, "failed") == 0 && statusInt(r.obj, "succeeded") == 0 {
		return bfQueued
	}
	return bfStopped
}

// justReleased says a native run reads as Stopped only by the Suspended
// condition it was created with: spec.suspend is already false, and the Job
// controller has not synced since the release. It is in flight (backfillState
// counts it so), and nobody stopped it.
func justReleased(r runRow) bool {
	suspended, _, _ := unstructured.NestedBool(r.obj.Object, "spec", "suspend")
	return r.Kind != "VolcanoJob" && r.Phase == phaseStopped && !suspended
}

// backfill is `adhar batch backfill` with the kube access behind the dynamic
// client. Every date is resolved and named BEFORE anything is created, so one
// refused date creates nothing. A non-nil report with an error is a backfill
// that started and then could not finish waiting.
func backfill(c context.Context, dc dynamic.Interface, ns, name, from, to string, extra map[string]string, parallel int, wait bool, timeout time.Duration) (*backfillReport, error) {
	// Refused here, where the release loop is: with no run allowed in flight it
	// would create every date suspended and then wait for ever to release one.
	if parallel < 1 {
		return nil, fmt.Errorf("--parallel must be at least 1: it is how many runs may be in flight at once")
	}
	dates, err := backfillDates(from, to)
	if err != nil {
		return nil, err
	}
	job, err := getBatchJob(c, dc, ns, name)
	if err != nil {
		return nil, err
	}
	t, err := readRunTemplate(c, dc, job)
	if err != nil {
		return nil, err
	}
	if t.ParamsError != "" {
		return nil, fmt.Errorf("the run template's parameters cannot be read: %s", t.ParamsError)
	}
	field, ok := businessDateField(t.Fields)
	if !ok {
		return nil, fmt.Errorf("%s declares no business-date parameter (businessDate, or a date its schedule supplies), so there is nothing to backfill by", name)
	}
	if _, given := extra[field.Name]; given {
		return nil, fmt.Errorf("-p %s is the business date; --from and --to give it", field.Name)
	}
	volcano := runKindOf(t.Manifest) == "VolcanoJob"
	report := &backfillReport{BatchJob: name, Namespace: ns, Scheduler: "kubernetes", Parallel: parallel, Waited: wait}
	if volcano {
		report.Scheduler = schedulerVolcano
	}

	var refused []string
	for _, d := range dates {
		values := map[string]string{field.Name: d}
		for k, v := range extra {
			values[k] = v
		}
		resolved := resolveParams(t.Fields, values, envOf(t.Manifest, envTimeZone), now())
		if len(resolved.Errors) > 0 {
			refused = append(refused, d+": "+strings.Join(resolved.problems(), "; "))
			continue
		}
		base, err := runName(name, resolved.Identifying)
		if err != nil {
			return nil, err
		}
		report.Runs = append(report.Runs, backfillItem{BusinessDate: d, Run: base, params: resolved.Params})
	}
	if len(refused) > 0 {
		return nil, fmt.Errorf("parameters refused: %s", strings.Join(refused, " | "))
	}

	say := func(format string, args ...interface{}) {
		if !machineReadable() {
			helpers.Printf(format+"\n", args...)
		}
	}
	if volcano && parallel > 1 {
		say("%s", helpers.CreateMuted("this job runs on Volcano: every run is created now and its queue decides how many start; --parallel is not applied"))
	}

	// Plan against what exists: the same command twice starts nothing twice.
	rows, _, err := runsOfJob(c, dc, ns, name)
	if err != nil {
		return nil, err
	}
	for i := range report.Runs {
		it := &report.Runs[i]
		decision, err := decideRun(it.Run, rows)
		if err != nil {
			return nil, err
		}
		switch decision.Action {
		case "create":
			it.Action = "created"
		case "restart":
			it.Action, it.restartOf, it.Run = "restarted", decision.Existing.Name, decision.Target
		case "reuse":
			it.Action, it.Run = "completed", decision.Target
		case "stopped":
			it.Run = decision.Target
			it.Action = "exists"
			if backfillState(*decision.Existing) == bfStopped {
				it.Action = "stopped"
			}
		default:
			it.Action, it.Run = "exists", decision.Target
		}
	}
	for i := range report.Runs {
		it := &report.Runs[i]
		if it.Action != "created" && it.Action != "restarted" {
			continue
		}
		trigger := triggerBackfill
		if it.Action == "restarted" {
			trigger = "restart"
		}
		run := buildRun(t, it.Run, ns, trigger, it.params, it.restartOf)
		if !volcano {
			if err := unstructured.SetNestedField(run.Object, true, "spec", "suspend"); err != nil {
				return nil, err
			}
			annotations := run.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[annotationQueued] = queuedByBackfill
			run.SetAnnotations(annotations)
		}
		if _, err := createRun(c, dc, run); err != nil && !errors.Is(err, errRunExists) {
			// Refused part way (a quota on Jobs, the API server): this date and
			// the ones after it have no run, and the report must not say they do.
			// Nothing was released, so what exists waits for the command again.
			for j := i; j < len(report.Runs); j++ {
				if a := report.Runs[j].Action; a == "created" || a == "restarted" {
					report.Runs[j].Action = actionNotCreated
				}
			}
			fillBackfillStatus(c, dc, report)
			return report, fmt.Errorf("%w\n  the runs created before it stay queued; the same command continues the backfill, and one over fewer dates releases them", err)
		}
	}
	created := 0
	for _, it := range report.Runs {
		if it.Action == "created" || it.Action == "restarted" {
			created++
		}
	}
	if volcano {
		say("%s", helpers.CreateInfo(fmt.Sprintf("%d dates, %d runs created; the queue starts them", len(report.Runs), created)))
	} else {
		say("%s", helpers.CreateInfo(fmt.Sprintf("%d dates, %d runs created suspended; at most %d in flight", len(report.Runs), created, parallel)))
	}

	err = driveBackfill(c, dc, report, volcano, parallel, wait, timeout, say)
	fillBackfillStatus(c, dc, report)
	return report, err
}

// driveBackfill is the release loop: it releases queued runs in date order
// while fewer than parallel are in flight, and returns once none is queued, or
// with wait once every run has ended.
func driveBackfill(c context.Context, dc dynamic.Interface, report *backfillReport, volcano bool, parallel int, wait bool, timeout time.Duration, say func(string, ...interface{})) error {
	if volcano && !wait {
		return nil
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		rows, _, err := runsOfJob(c, dc, report.Namespace, report.BatchJob)
		if err != nil {
			return err
		}
		byName := map[string]runRow{}
		for _, r := range rows {
			byName[r.Name] = r
		}
		inFlight, open := 0, 0
		var queued []runRow
		for _, it := range report.Runs {
			r, ok := byName[it.Run]
			if !ok {
				// Gone: deleted, or cleaned up by its TTL after it ended.
				continue
			}
			switch backfillState(r) {
			case bfInFlight:
				inFlight++
				open++
			case bfQueued:
				queued = append(queued, r)
				open++
			}
		}
		for !volcano && inFlight < parallel && len(queued) > 0 {
			next := queued[0]
			queued = queued[1:]
			if err := perform(c, dc, resumeRequest(next.obj)); err != nil {
				return err
			}
			inFlight++
			say("%s", helpers.CreateSuccess(fmt.Sprintf("Released %s (%s): %d in flight, %d queued", next.Name, dash(next.BusinessDate), inFlight, len(queued))))
		}
		if len(queued) == 0 && (!wait || open == 0) {
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return helpers.ExitCode(1, fmt.Errorf("the backfill of %s is not finished after %s (%d queued, %d in flight); its runs keep going, and the same command releases the rest", report.BatchJob, timeout, len(queued), inFlight))
		}
		select {
		case <-c.Done():
			return c.Err()
		case <-time.After(pollInterval):
		}
	}
}

// fillBackfillStatus sets each date's status and exit code from its run as it
// stands now (D2's statuses; Queued for one not yet released, Gone for one no
// longer there).
func fillBackfillStatus(c context.Context, dc dynamic.Interface, report *backfillReport) {
	rows, _, err := runsOfJob(c, dc, report.Namespace, report.BatchJob)
	if err != nil {
		return
	}
	byName := map[string]runRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	for i := range report.Runs {
		it := &report.Runs[i]
		r, ok := byName[it.Run]
		switch {
		case !ok && it.Action == actionNotCreated:
			// Never there: no status to give.
		case !ok:
			it.Status = "Gone"
		case backfillState(r) == bfQueued:
			it.Status = "Queued"
		default:
			o := outcomeOf(r)
			it.Status, it.Outcome = o.Status, outcomeLabel(r.Summary)
			// Without --wait the command ends the moment the last run is
			// released, before the Job controller has seen the release.
			if justReleased(r) {
				it.Status = "Running"
			}
			if r.ExitCode != nil {
				code := *r.ExitCode
				it.ExitCode = &code
			}
		}
	}
	sort.SliceStable(report.Runs, func(i, j int) bool { return report.Runs[i].BusinessDate < report.Runs[j].BusinessDate })
}

func printBackfill(r *backfillReport) {
	if r == nil {
		return
	}
	tb := helpers.NewTable("DATE", "RUN", "ACTION", "STATUS", "OUTCOME", "EXIT")
	for _, it := range r.Runs {
		exit := "—"
		if it.ExitCode != nil {
			exit = fmt.Sprintf("%d", *it.ExitCode)
		}
		tb.Row(it.BusinessDate, it.Run, it.Action, dash(it.Status), dash(it.Outcome), exit)
	}
	fmt.Println(helpers.SectionHeading(helpers.IconApp, fmt.Sprintf("Backfill of %s/%s", r.Namespace, r.BatchJob)))
	fmt.Println(tb.Render())
	if !r.Waited {
		helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("`adhar batch runs %s` follows them; --wait waits for every run to end", r.BatchJob)))
	}
}
