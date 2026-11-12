/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

var (
	runParamArgs []string
	runWait      bool
	runFollow    bool
	runTimeout   time.Duration

	// pollInterval is how often --wait re-reads the run; tests shorten it.
	pollInterval = 2 * time.Second
)

var runCmd = &cobra.Command{
	Use:   "run <job>",
	Short: "Start a run of a job, named for its identifying parameters",
	Long: `Start a run from the job's deployed run template. The parameters are checked
against what the job declares, the defaults are applied, and the run is named
<job>-<hash8> for its identifying parameters, so the same identity never runs
twice: an existing run of that name is shown instead.

With --wait the command follows the run to its end and carries its outcome:
an existing run that completed is a success with no new run; one still
running is waited for; one that failed is restarted as the next -r<n>; one
that is stopped is reported as Stopped with a hint to resume. The process exit
code is the run's (0 includes a skipped run), 4 for a stopped one, and -o json
prints {run, namespace, batchJob, kind, status, exitCode, executionId, summary}.`,
	Example: `  adhar batch run importer -p businessDate=2026-11-03
  adhar batch run importer -p businessDate=2026-11-03 --wait --follow
  adhar batch run importer --env prod --wait -o json | jq .status`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if runFollow && !runWait {
			runWait = true
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
		values, err := parseParamArgs(runParamArgs)
		if err != nil {
			return err
		}
		var logs logSource
		if runFollow {
			logs = &podLogSource{cs: cl.cs}
		}
		outcome, err := startRun(ctx(cmd), cl.dc, logs, ns, name, values, runWait, runTimeout)
		if err != nil {
			return err
		}
		if machineReadable() {
			if err := printMachine(outcome); err != nil {
				return err
			}
		}
		if outcome.ExitCode != 0 {
			return helpers.ExitCode(outcome.ExitCode, nil)
		}
		return nil
	},
}

func init() {
	runCmd.Flags().StringArrayVarP(&runParamArgs, "param", "p", nil, "A parameter as key=value (repeatable)")
	runCmd.Flags().BoolVar(&runWait, "wait", false, "Wait for the run to end and exit with its exit code")
	runCmd.Flags().BoolVar(&runFollow, "follow", false, "Stream the run's logs while waiting (implies --wait)")
	runCmd.Flags().DurationVar(&runTimeout, "timeout", 0, "Give up waiting after this long (default: the job's activeDeadlineSeconds plus ten minutes)")
}

// startRun is `adhar batch run`, in full, with the kube access behind the
// dynamic client so the fake can drive it end to end.
func startRun(c context.Context, dc dynamic.Interface, logs logSource, ns, name string, values map[string]string, wait bool, timeout time.Duration) (runOutcome, error) {
	job, err := getBatchJob(c, dc, ns, name)
	if err != nil {
		return runOutcome{}, err
	}
	t, err := readRunTemplate(c, dc, job)
	if err != nil {
		return runOutcome{}, err
	}
	if t.ParamsError != "" {
		return runOutcome{}, fmt.Errorf("the run template's parameters cannot be read: %s", t.ParamsError)
	}
	resolved := resolveParams(t.Fields, values, envOf(t.Manifest, envTimeZone), now())
	if len(resolved.Errors) > 0 {
		return runOutcome{}, fmt.Errorf("parameters refused: %s", strings.Join(resolved.problems(), "; "))
	}
	base, err := runName(name, resolved.Identifying)
	if err != nil {
		return runOutcome{}, err
	}
	rows, _, err := runsOfJob(c, dc, ns, name)
	if err != nil {
		return runOutcome{}, err
	}
	decision, err := decideRun(base, rows)
	if err != nil {
		return runOutcome{}, err
	}
	if timeout <= 0 {
		timeout = defaultWait(job)
	}
	say := func(format string, args ...interface{}) {
		if !machineReadable() {
			helpers.Printf(format+"\n", args...)
		}
	}
	if len(resolved.Params) > 0 {
		say("%s", helpers.CreateMuted("parameters "+paramsLine(t.Fields, resolved.Params)))
	}

	// Without --wait the command does one thing: create the run, and show the
	// existing one when the identity is taken (§3.4, §8.1). Restarting or
	// judging what exists is --wait's job (D2), never a side effect of a plain
	// `run` — a person who asked for a run and got a restart would not know.
	if !wait {
		if decision.Action != "create" {
			existing := *decision.Existing
			say("%s", helpers.CreateWarning(fmt.Sprintf("%s already exists (%s); showing it instead of starting a second run", existing.Name, existing.Phase.label())))
			showRun(existing)
			sayHint(say, existing)
			return outcomeOf(existing), nil
		}
		run := buildRun(t, base, ns, "manual", resolved.Params, "")
		created, err := createRun(c, dc, run)
		if errors.Is(err, errRunExists) {
			return showExisting(c, dc, ns, base, say)
		}
		if err != nil {
			return runOutcome{}, err
		}
		say("%s", helpers.CreateSuccess(fmt.Sprintf("Started %s in %s (`adhar batch logs %s -f` follows it)", created.GetName(), ns, created.GetName())))
		row := runRowOf(created, nil, now())
		return runOutcome{Run: row.Name, Namespace: ns, BatchJob: name, Kind: row.Kind, Status: "Running"}, nil
	}

	var target string
	switch decision.Action {
	case "reuse":
		say("%s", helpers.CreateSuccess(fmt.Sprintf("%s already completed; nothing to run", decision.Target)))
		showRun(*decision.Existing)
		return outcomeOf(*decision.Existing), nil
	case "stopped":
		say("%s", helpers.CreateWarning(fmt.Sprintf("%s is stopped; `adhar batch resume %s` continues it", decision.Target, decision.Target)))
		showRun(*decision.Existing)
		return outcomeOf(*decision.Existing), nil
	case "wait":
		say("%s", helpers.CreateInfo(fmt.Sprintf("%s is %s; waiting for it", decision.Target, decision.Existing.Phase)))
		target = decision.Target
	case "restart":
		prev := decision.Existing
		if w := restartWarning(prev.ExitCode); w != "" {
			say("%s", helpers.CreateWarning(w))
		}
		say("%s", helpers.CreateInfo(fmt.Sprintf("%s failed; restarting it as %s", prev.Name, decision.Target)))
		run := buildRun(t, decision.Target, ns, "restart", resolved.Params, prev.Name)
		if _, err := createRun(c, dc, run); err != nil && !errors.Is(err, errRunExists) {
			return runOutcome{}, err
		}
		target = decision.Target
	default:
		run := buildRun(t, base, ns, "manual", resolved.Params, "")
		if _, err := createRun(c, dc, run); err != nil && !errors.Is(err, errRunExists) {
			return runOutcome{}, err
		}
		say("%s", helpers.CreateSuccess(fmt.Sprintf("Started %s in %s", base, ns)))
		target = base
	}
	row, err := waitForRun(c, dc, logs, ns, target, timeout)
	if err != nil {
		return runOutcome{}, err
	}
	showRun(row)
	sayHint(say, row)
	return outcomeOf(row), nil
}

// showExisting answers an AlreadyExists the listing did not see (a run
// created between the list and the create): read it and show it.
func showExisting(c context.Context, dc dynamic.Interface, ns, name string, say func(string, ...interface{})) (runOutcome, error) {
	run, err := getRun(c, dc, ns, name)
	if err != nil {
		return runOutcome{}, err
	}
	row := runRowOf(run, nil, now())
	say("%s", helpers.CreateWarning(fmt.Sprintf("%s already exists (%s); showing it instead of starting a second run", name, row.Phase.label())))
	showRun(row)
	return outcomeOf(row), nil
}

func sayHint(say func(string, ...interface{}), row runRow) {
	switch row.Phase {
	case phaseFailed:
		say("%s", helpers.CreateMuted(fmt.Sprintf("`adhar batch restart %s` re-executes only the failed steps; `adhar batch logs %s` shows why", row.Name, row.Name)))
	case phaseStopped:
		say("%s", helpers.CreateMuted(fmt.Sprintf("`adhar batch resume %s` continues it from its last commit", row.Name)))
	}
}

// defaultWait is the --timeout default: the job's activeDeadlineSeconds plus
// ten minutes, the bound §12.1 gives the workflow step.
func defaultWait(job *unstructured.Unstructured) time.Duration {
	deadline, _, _ := unstructured.NestedInt64(job.Object, "spec", "parameters", "timeouts", "activeDeadlineSeconds")
	if deadline <= 0 {
		deadline = 21600
	}
	return time.Duration(deadline)*time.Second + 10*time.Minute
}

// waitForRun polls the run until it ends, streaming each of its pods' logs
// once when a log source is given, and returns its final row. A run that
// disappears while being waited for (TTL, or deleted) is an error, not a
// success. The pods are listed every tick: they carry the summary.
func waitForRun(c context.Context, dc dynamic.Interface, logs logSource, ns, name string, timeout time.Duration) (runRow, error) {
	// The deadline is wall-clock time, not the (test-replaceable) clock the
	// rows are aged against: a frozen clock must not make a wait eternal.
	deadline := time.Now().Add(timeout)
	streamed := map[string]bool{}
	for {
		run, err := getRun(c, dc, ns, name)
		if err != nil {
			return runRow{}, err
		}
		pods, _ := dc.Resource(podGVR).Namespace(ns).List(c, metav1.ListOptions{LabelSelector: labelBatchJob})
		var items []unstructured.Unstructured
		if pods != nil {
			items = pods.Items
		}
		row := runRowOf(run, items, now())
		if logs != nil {
			for _, p := range podsOfRun(run, items) {
				phase, _, _ := unstructured.NestedString(p.Object, "status", "phase")
				if streamed[p.GetName()] || phase == "Pending" || phase == "" {
					continue
				}
				streamed[p.GetName()] = true
				logs.stream(c, ns, p.GetName(), !row.Phase.terminal(), os.Stdout)
			}
		}
		if row.Phase.terminal() {
			return row, nil
		}
		if time.Now().After(deadline) {
			return runRow{}, helpers.ExitCode(1, fmt.Errorf("%s is still %s after %s; it keeps running — `adhar batch runs %s` shows it", name, row.Phase, timeout, row.BatchJob))
		}
		select {
		case <-c.Done():
			return runRow{}, c.Err()
		case <-time.After(pollInterval):
		}
	}
}

// showRun prints the one-run summary block after a run or a wait.
func showRun(row runRow) {
	if machineReadable() {
		return
	}
	v := runViewOf(row, now())
	helpers.Printf("%s  %s\n", helpers.IconApp, v.Name)
	helpers.Printf("  status        %s\n", phaseCell(row.Phase))
	helpers.Printf("  trigger       %s\n", dash(v.Trigger))
	if v.BusinessDate != "" {
		helpers.Printf("  business date %s\n", v.BusinessDate)
	}
	if v.RestartOf != "" {
		helpers.Printf("  restart of    %s\n", v.RestartOf)
	}
	if v.Duration != "" {
		helpers.Printf("  duration      %s\n", v.Duration)
	}
	if v.ExitCode != nil {
		helpers.Printf("  exit code     %d (%s)\n", *v.ExitCode, exitMeaning(*v.ExitCode))
	}
	if v.Outcome != "" {
		helpers.Printf("  outcome       %s\n", v.Outcome)
	}
	if row.Summary != nil && row.Summary.Read != nil {
		helpers.Printf("  items         read %d, written %d, skipped %d\n", *row.Summary.Read, n(row.Summary.Written), n(row.Summary.Skipped))
	}
	if v.ExecutionID != nil {
		helpers.Printf("  execution     %d\n", *v.ExecutionID)
	}
	if v.Message != "" {
		helpers.Printf("  message       %s\n", v.Message)
	}
}

func n(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// paramsLine renders the resolved parameters, masking the sensitive ones.
func paramsLine(fields []paramField, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+displayParam(fields, k, params[k]))
	}
	return strings.Join(parts, " ")
}

func toJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// logSource streams one pod's batch container logs to out. The typed clientset
// is the only way to read logs, so it sits behind this seam for the tests.
type logSource interface {
	stream(c context.Context, ns, pod string, follow bool, out io.Writer)
}
