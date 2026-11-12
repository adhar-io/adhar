/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/duration"
)

var (
	runsStatus string
	runsLimit  int
)

var runsCmd = &cobra.Command{
	Use:   "runs <job>",
	Short: "List a job's runs with trigger, actor, business date and exit code",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := connect(cmd)
		if err != nil {
			return err
		}
		ns, err := resolveNamespace(ctx(cmd), cl.dc, cmd)
		if err != nil {
			return err
		}
		ns, name := parseRef(args[0], ns)
		if _, err := getBatchJob(ctx(cmd), cl.dc, ns, name); err != nil {
			return err
		}
		rows, podsUnknown, err := runsOfJob(ctx(cmd), cl.dc, ns, name)
		if err != nil {
			return err
		}
		rows, err = filterRuns(rows, runsStatus, runsLimit)
		if err != nil {
			return err
		}
		views := make([]runView, 0, len(rows))
		for _, r := range rows {
			views = append(views, runViewOf(r, now()))
		}
		if machineReadable() {
			return printMachine(views)
		}
		if len(views) == 0 {
			helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("No runs of %s in %s%s.", name, ns, statusSuffix(runsStatus))))
			return nil
		}
		fmt.Println(runsTable(views).Render())
		if podsUnknown {
			helpers.Printf("%s\n", helpers.CreateMuted("Pods could not be read, so exit codes and outcomes are unknown rather than absent."))
		}
		return nil
	},
}

func init() {
	runsCmd.Flags().StringVar(&runsStatus, "status", "", "Only runs in this phase (pending, running, stopping, stopped, succeeded, failed)")
	runsCmd.Flags().IntVar(&runsLimit, "limit", 20, "How many runs to show, newest first (0 for all)")
}

func statusSuffix(status string) string {
	if status == "" {
		return ""
	}
	return " with status " + status
}

// filterRuns keeps the runs in the asked phase, newest first, and at most limit.
func filterRuns(rows []runRow, status string, limit int) ([]runRow, error) {
	if status != "" {
		want := runPhase(strings.ToLower(status))
		switch want {
		case phasePending, phaseRunning, phaseStopping, phaseStopped, phaseSucceeded, phaseFailed, phaseDeleting, phaseUnknown:
		default:
			return nil, fmt.Errorf("--status %q: expected one of pending, running, stopping, stopped, succeeded, failed", status)
		}
		kept := rows[:0:0]
		for _, r := range rows {
			if r.Phase == want {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// runView is one run as `runs`, `describe` and `-o json` report it.
type runView struct {
	Name         string                 `json:"name" yaml:"name"`
	Namespace    string                 `json:"namespace" yaml:"namespace"`
	BatchJob     string                 `json:"batchJob,omitempty" yaml:"batchJob,omitempty"`
	Kind         string                 `json:"kind" yaml:"kind"`
	Status       string                 `json:"status" yaml:"status"`
	Message      string                 `json:"message,omitempty" yaml:"message,omitempty"`
	Trigger      string                 `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Actor        string                 `json:"actor,omitempty" yaml:"actor,omitempty"`
	BusinessDate string                 `json:"businessDate,omitempty" yaml:"businessDate,omitempty"`
	RestartOf    string                 `json:"restartOf,omitempty" yaml:"restartOf,omitempty"`
	Attempts     int                    `json:"attempts" yaml:"attempts"`
	StartedAt    string                 `json:"startedAt,omitempty" yaml:"startedAt,omitempty"`
	FinishedAt   string                 `json:"finishedAt,omitempty" yaml:"finishedAt,omitempty"`
	Duration     string                 `json:"duration,omitempty" yaml:"duration,omitempty"`
	ExitCode     *int                   `json:"exitCode,omitempty" yaml:"exitCode,omitempty"`
	Outcome      string                 `json:"outcome,omitempty" yaml:"outcome,omitempty"`
	ExecutionID  *int64                 `json:"executionId,omitempty" yaml:"executionId,omitempty"`
	Summary      map[string]interface{} `json:"summary,omitempty" yaml:"summary,omitempty"`
	Image        string                 `json:"image,omitempty" yaml:"image,omitempty"`
	Age          string                 `json:"age,omitempty" yaml:"age,omitempty"`
	phase        runPhase
}

func runViewOf(r runRow, at time.Time) runView {
	v := runView{
		Name: r.Name, Namespace: r.Namespace, BatchJob: r.BatchJob, Kind: r.Kind,
		Status: r.Phase.label(), Message: r.Message, Trigger: r.Trigger, Actor: r.Actor,
		BusinessDate: r.BusinessDate, RestartOf: r.RestartOf, Attempts: r.Attempts,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, ExitCode: r.ExitCode,
		Outcome: outcomeLabel(r.Summary), Image: r.Image, phase: r.Phase,
	}
	if r.HasDuration {
		v.Duration = duration.HumanDuration(r.Duration)
	}
	if r.Summary != nil {
		v.Summary = r.Summary.Raw
		v.ExecutionID = r.Summary.ExecutionID
	}
	if !r.Created.IsZero() {
		v.Age = duration.HumanDuration(at.Sub(r.Created))
	}
	return v
}

func runsTable(views []runView) *helpers.Table {
	t := helpers.NewTable("RUN", "STATUS", "TRIGGER", "ACTOR", "BUSINESS DATE", "STARTED", "DURATION", "EXIT", "OUTCOME")
	for _, v := range views {
		t.Row(v.Name, phaseCell(v.phase), dash(v.Trigger), dash(v.Actor), dash(v.BusinessDate), startedCell(v), dash(v.Duration), exitCell(v.ExitCode), dash(v.Outcome))
	}
	return t
}

func startedCell(v runView) string {
	if v.StartedAt == "" {
		return "—"
	}
	if at, ok := parseTime(v.StartedAt); ok {
		return duration.HumanDuration(now().Sub(at)) + " ago"
	}
	return v.StartedAt
}

func exitCell(code *int) string {
	if code == nil {
		return "—"
	}
	return fmt.Sprint(*code)
}
