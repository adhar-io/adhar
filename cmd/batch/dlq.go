/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	platformevents "adhar-io/adhar/platform/events"

	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
)

// The dead-letter queue (design 0025 §5.8 ADHAR_BATCH_DEAD_LETTER, §8.1, A3.6).
// The rejected items live in the job's OWN database, which no platform
// component reads, so the counts the CLI shows are what the runs said about
// themselves: the termination summary's `deadLettered` for the runs the
// cluster still holds, and the `counts.deadLettered` of the observer's end
// events on adhar.batch for the ones it no longer does. Replaying is the kit's
// `dlq-replay` ops command (ops.go), one ops Job per execution, which re-reads
// only that execution's unreplayed rows.

var (
	dlqReplay    bool
	dlqExecution int64
	dlqWait      bool
	dlqBridge    string
	dlqNoEvents  bool
	dlqTimeout   time.Duration
)

var dlqCmd = &cobra.Command{
	Use:   "dlq <job>",
	Short: "Dead-lettered items per execution; --replay re-processes them through an ops Job",
	Long: `Show how many items each execution of a job dead-lettered: from the run
summaries of the runs the cluster still holds, and from the adhar.batch events
for older ones (through the event bridge; without it only the runs are counted).

--replay starts the kit's dlq-replay ops command for every execution listed
with dead letters (or the one --execution names), as <job>-ops-dlq-replay-<id>:
the runner re-processes only the rejected items of that execution and marks
them replayed. Asking again shows the existing replay; with --wait a failed one
is retried as -r<n>.`,
	Example: `  adhar batch dlq importer --env prod
  adhar batch dlq importer --replay --execution 412 --wait`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if dlqExecution < 0 {
			return fmt.Errorf("--execution must be a positive execution id")
		}
		if dlqExecution > 0 && !dlqReplay {
			return fmt.Errorf("--execution chooses what --replay replays; add --replay")
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
		report, err := deadLetters(ctx(cmd), cl.dc, ns, name, eventBus{cluster: cl.cfg, bridge: dlqBridge}, !dlqNoEvents)
		if err != nil {
			return err
		}
		if dlqReplay {
			if err := replayDeadLetters(ctx(cmd), cl.dc, report, dlqExecution, dlqWait, dlqTimeout); err != nil {
				return err
			}
		}
		if machineReadable() {
			if err := printMachine(report); err != nil {
				return err
			}
		} else {
			printDeadLetters(report)
		}
		for _, o := range report.Replays {
			if o.ExitCode != 0 {
				return helpers.ExitCode(o.ExitCode, nil)
			}
		}
		return nil
	},
}

func init() {
	dlqCmd.Flags().BoolVar(&dlqReplay, "replay", false, "Replay the dead letters through the kit's dlq-replay ops Job, one per execution")
	dlqCmd.Flags().Int64Var(&dlqExecution, "execution", 0, "With --replay: only this execution")
	dlqCmd.Flags().BoolVar(&dlqWait, "wait", false, "With --replay: wait for each replay to end; retries a failed one as -r<n>")
	dlqCmd.Flags().DurationVar(&dlqTimeout, "timeout", 0, "Give up waiting after this long (default: the job's activeDeadlineSeconds plus ten minutes)")
	dlqCmd.Flags().StringVar(&dlqBridge, "bridge", "", "Kafka bridge base URL (default: the platform's, through the API server)")
	dlqCmd.Flags().BoolVar(&dlqNoEvents, "no-events", false, "Count only the runs the cluster holds; do not read adhar.batch")
}

// dlqEntry is one execution that dead-lettered items.
type dlqEntry struct {
	Run          string `json:"run" yaml:"run"`
	BusinessDate string `json:"businessDate,omitempty" yaml:"businessDate,omitempty"`
	ExecutionID  *int64 `json:"executionId,omitempty" yaml:"executionId,omitempty"`
	DeadLettered int64  `json:"deadLettered" yaml:"deadLettered"`
	Ended        string `json:"ended,omitempty" yaml:"ended,omitempty"`
	// Source is `run` (its summary) or `event` (the observer's end event).
	Source string `json:"source" yaml:"source"`
	// Replay is the phase of the latest dlq-replay ops Job for this
	// execution, when the cluster still holds one.
	Replay    string `json:"replay,omitempty" yaml:"replay,omitempty"`
	ReplayRun string `json:"replayRun,omitempty" yaml:"replayRun,omitempty"`
}

// dlqReport is `-o json`'s document.
type dlqReport struct {
	BatchJob  string       `json:"batchJob" yaml:"batchJob"`
	Namespace string       `json:"namespace" yaml:"namespace"`
	Entries   []dlqEntry   `json:"entries" yaml:"entries"`
	Total     int64        `json:"total" yaml:"total"`
	Events    string       `json:"events" yaml:"events"`
	Replays   []runOutcome `json:"replays,omitempty" yaml:"replays,omitempty"`

	rows []runRow
}

// dlqEntriesOf joins the two sources. A run the cluster holds speaks for its
// execution; an event fills in only executions (or runs) no held run names,
// so a run that is both in the cluster and on the bus is counted once. Ops
// Jobs are not business runs and are never counted.
func dlqEntriesOf(rows []runRow, evs []platformevents.Event) []dlqEntry {
	var out []dlqEntry
	seenExec := map[int64]bool{}
	seenRun := map[string]bool{}
	for _, r := range rows {
		if r.obj != nil && isOpsRun(r.obj) {
			continue
		}
		if r.Summary == nil || r.Summary.DeadLettered == nil || *r.Summary.DeadLettered <= 0 {
			continue
		}
		e := dlqEntry{Run: r.Name, BusinessDate: r.BusinessDate, ExecutionID: r.Summary.ExecutionID, DeadLettered: *r.Summary.DeadLettered, Ended: r.FinishedAt, Source: "run"}
		if e.ExecutionID != nil {
			seenExec[*e.ExecutionID] = true
		}
		seenRun[r.Name] = true
		out = append(out, e)
	}
	for _, ev := range evs {
		switch ev.Type {
		case platformevents.TypeBatchRunSucceeded, platformevents.TypeBatchRunFailed, platformevents.TypeBatchRunSkipped:
		default:
			continue
		}
		d := eventDataOf(ev)
		if d.Counts == nil || d.Counts.DeadLettered == nil || *d.Counts.DeadLettered <= 0 {
			continue
		}
		if d.Run != nil && d.Run.Trigger == triggerOps {
			continue
		}
		e := dlqEntry{DeadLettered: *d.Counts.DeadLettered, Ended: d.End, Source: "event"}
		if d.Run != nil {
			e.Run = d.Run.Name
		}
		if d.SpringBatch != nil {
			e.BusinessDate, e.ExecutionID = d.SpringBatch.BusinessDate, d.SpringBatch.ExecutionID
		}
		if e.Ended == "" {
			e.Ended = ev.Time
		}
		if (e.ExecutionID != nil && seenExec[*e.ExecutionID]) || (e.Run != "" && seenRun[e.Run]) {
			continue
		}
		if e.ExecutionID != nil {
			seenExec[*e.ExecutionID] = true
		}
		if e.Run != "" {
			seenRun[e.Run] = true
		}
		out = append(out, e)
	}
	// Each execution's latest replay, from the ops Jobs the cluster holds.
	latest := map[string]runRow{}
	for _, r := range rows {
		if r.obj == nil || envOf(r.obj, envCommand) != opsDLQReplay {
			continue
		}
		id := envOf(r.obj, envExecutionID)
		if prev, ok := latest[id]; !ok || r.Created.After(prev.Created) {
			latest[id] = r
		}
	}
	for i := range out {
		if out[i].ExecutionID == nil {
			continue
		}
		if r, ok := latest[strconv.FormatInt(*out[i].ExecutionID, 10)]; ok {
			out[i].Replay, out[i].ReplayRun = r.Phase.label(), r.Name
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ended > out[j].Ended })
	return out
}

// deadLetters builds the report. The bus is optional: a cluster without it,
// or a bridge that does not answer, still shows what the runs say, and the
// report says the events were not read.
func deadLetters(c context.Context, dc dynamic.Interface, ns, name string, bus eventBus, withEvents bool) (*dlqReport, error) {
	if _, err := getBatchJob(c, dc, ns, name); err != nil {
		return nil, err
	}
	rows, _, err := runsOfJob(c, dc, ns, name)
	if err != nil {
		return nil, err
	}
	report := &dlqReport{BatchJob: name, Namespace: ns, Events: "not read", rows: rows}
	var evs []platformevents.Event
	if withEvents {
		got, _, err := readBatchHistory(c, bus, ns+"/"+name, time.Time{}, 0)
		if err != nil {
			// One line: this ends a table's footer and is a field of -o json; the
			// remedy the error goes on to explain belongs to `adhar batch history`.
			why, _, _ := strings.Cut(err.Error(), "\n")
			report.Events = "unavailable: " + why
		} else {
			report.Events = "read"
			evs = got
		}
	}
	report.Entries = dlqEntriesOf(rows, evs)
	if report.Entries == nil {
		report.Entries = []dlqEntry{}
	}
	for _, e := range report.Entries {
		report.Total += e.DeadLettered
	}
	return report, nil
}

// replayDeadLetters starts one dlq-replay ops Job per execution: the one
// --execution names, else every listed execution with dead letters. An
// execution the counts do not know is still replayed when named: the counts
// are what the runs reported, the rows are in the job's database.
func replayDeadLetters(c context.Context, dc dynamic.Interface, report *dlqReport, only int64, wait bool, timeout time.Duration) error {
	var ids []int64
	if only > 0 {
		ids = []int64{only}
	} else {
		seen := map[int64]bool{}
		for _, e := range report.Entries {
			if e.ExecutionID != nil && !seen[*e.ExecutionID] {
				seen[*e.ExecutionID] = true
				ids = append(ids, *e.ExecutionID)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	if len(ids) == 0 {
		return fmt.Errorf("no execution of %s is known to have dead-lettered items; name one with --execution", report.BatchJob)
	}
	job, err := getBatchJob(c, dc, report.Namespace, report.BatchJob)
	if err != nil {
		return err
	}
	t, err := readRunTemplate(c, dc, job)
	if err != nil {
		return err
	}
	for _, id := range ids {
		out, err := executeRun(c, dc, nil, job, opsSpec(t, report.BatchJob, report.Namespace, opsDLQReplay, id), wait, timeout)
		if err != nil {
			return err
		}
		report.Replays = append(report.Replays, out)
	}
	return nil
}

func printDeadLetters(r *dlqReport) {
	if len(r.Entries) == 0 {
		helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("No dead-lettered items reported for %s/%s (events %s).", r.Namespace, r.BatchJob, r.Events)))
		return
	}
	tb := helpers.NewTable("RUN", "DATE", "EXECUTION", "DEAD-LETTERED", "ENDED", "FROM", "REPLAY")
	for _, e := range r.Entries {
		exec := "—"
		if e.ExecutionID != nil {
			exec = strconv.FormatInt(*e.ExecutionID, 10)
		}
		replay := "—"
		if e.Replay != "" {
			replay = e.Replay + " (" + e.ReplayRun + ")"
		}
		tb.Row(dash(e.Run), dash(e.BusinessDate), exec, strconv.FormatInt(e.DeadLettered, 10), dash(e.Ended), e.Source, replay)
	}
	fmt.Println(helpers.SectionHeading(helpers.IconInfo, fmt.Sprintf("Dead letters of %s/%s", r.Namespace, r.BatchJob)))
	fmt.Println(tb.Render())
	helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("%d items in %d executions · events %s · `adhar batch dlq %s --replay` re-processes them", r.Total, len(r.Entries), r.Events, r.BatchJob)))
}
