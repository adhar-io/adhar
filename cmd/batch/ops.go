/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Ops commands (design 0025 §3.4, §5.2 `BatchCommandRunner`, §8.1): what a
// person does to an EXECUTION in the job's own JobRepository, which no platform
// component reads. The CLI does not touch the database; it starts an ops Job —
// the deployed run template, unchanged but for what §6.5 lets a client set —
// with ADHAR_BATCH_COMMAND naming the verb and ADHAR_BATCH_EXECUTION_ID the
// execution, and the kit's runner does the rest with the job's own credentials.
// adhar-batch-run-integrity admits it like any client's run.
//
// The contract the kit's ops runner (KIT-3.x) implements:
//
//   - the trigger is `ops` (label and ADHAR_BATCH_TRIGGER), ADHAR_BATCH_PARAMS is
//     `{}`: the runner acts on the execution id and resolves no parameters, so an
//     ops Job never needs a business date;
//   - the template is used whole, so on a partitioned job every index or task
//     starts; only the single/manager role acts, workers exit 0;
//   - the exit code and the summary follow §5.7 and D13 like any run.
//
// The run is `<job>-ops-<verb>-<executionId>` (§8.3), the job prefix trimmed so
// that the name and its retries `-r<n>` fit 63 characters; a failed ops Job is
// retried by asking again with --wait, as `run --wait` retries (D2). It is never
// `restart`ed: a restart is built from the template's parameters and would run
// the BUSINESS job instead of the command.

const (
	opsAbandon   = "abandon"
	opsRecover   = "recover"
	opsDLQReplay = "dlq-replay"

	// maxRunName is a Job name that still fits the label value the Job
	// controller copies it into (batch.kubernetes.io/job-name).
	maxRunName = 63
)

var (
	opsExecution int64
	opsWait      bool
	opsTimeout   time.Duration
)

var abandonCmd = &cobra.Command{
	Use:   "abandon <run>",
	Short: "Mark a run's execution ABANDONED through an ops Job, so its identity can be run anew",
	Long: `Abandon the Spring Batch execution of a failed or stopped run. An abandoned
execution is never restarted: the same identity then exits 6, so a run with
those parameters needs new ones. The CLI starts an ops Job from the job's run
template with ADHAR_BATCH_COMMAND=abandon and ADHAR_BATCH_EXECUTION_ID; the
kit's runner changes the execution in the job's own repository.

The execution defaults to the one the run's summary names; a run that lost its
pod wrote no summary, so give it with --execution.`,
	Example: `  adhar batch abandon importer-09932cb3
  adhar batch abandon importer-09932cb3 --execution 412 --wait`,
	Args: cobra.ExactArgs(1),
	RunE: opsRunE(opsAbandon),
}

var recoverCmd = &cobra.Command{
	Use:   "recover <run>",
	Short: "Recover a run's execution left STARTED by a dead owner, through an ops Job",
	Long: `Recover the Spring Batch execution of a run whose owner died while it was
STARTED (a node lost, an OOM kill with the lease not yet stale), so the next run
or restart of that identity can start instead of reporting a duplicate. The CLI
starts an ops Job from the job's run template with ADHAR_BATCH_COMMAND=recover
and ADHAR_BATCH_EXECUTION_ID; the kit's runner checks the lease and recovers.

A run that is still active is refused: its owner may be alive. Stop it first.`,
	Example: `  adhar batch recover importer-09932cb3 --execution 412 --wait`,
	Args:    cobra.ExactArgs(1),
	RunE:    opsRunE(opsRecover),
}

func init() {
	for _, c := range []*cobra.Command{abandonCmd, recoverCmd} {
		c.Flags().Int64Var(&opsExecution, "execution", 0, "The Spring Batch execution id (default: the one the run's summary names)")
		c.Flags().BoolVar(&opsWait, "wait", false, "Wait for the ops Job to end and exit with its exit code; retries a failed one as -r<n>")
		c.Flags().DurationVar(&opsTimeout, "timeout", 0, "Give up waiting after this long (default: the job's activeDeadlineSeconds plus ten minutes)")
	}
}

func opsRunE(verb string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if opsExecution < 0 {
			return fmt.Errorf("--execution must be a positive execution id")
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
		outcome, err := startOpsOnRun(ctx(cmd), cl.dc, nil, ns, name, verb, opsExecution, opsWait, opsTimeout)
		if err != nil {
			return err
		}
		return finishOutcome(outcome)
	}
}

// finishOutcome prints D2's outcome for -o json|yaml and ends the process with
// the run's exit code.
func finishOutcome(outcome runOutcome) error {
	if machineReadable() {
		if err := printMachine(outcome); err != nil {
			return err
		}
	}
	if outcome.ExitCode != 0 {
		return helpers.ExitCode(outcome.ExitCode, nil)
	}
	return nil
}

// opsRunName is `<job>-ops-<verb>-<executionId>`. The verb and the id are what
// make the name, so when it is too long the JOB prefix is cut, leaving room for
// the `-r99` of the last retry: a cut id would name another execution.
func opsRunName(batchJob, verb string, executionID int64) string {
	suffix := "-ops-" + verb + "-" + strconv.FormatInt(executionID, 10)
	room := maxRunName - len("-r99") - len(suffix)
	prefix := batchJob
	if len(prefix) > room {
		prefix = strings.TrimRight(prefix[:room], "-.")
	}
	return prefix + suffix
}

// isOpsRun says a run carries a command rather than the business job.
func isOpsRun(run *unstructured.Unstructured) bool {
	return run.GetLabels()[labelTrigger] == triggerOps || envOf(run, envCommand) != ""
}

// buildOpsRun is the run template with the command and the execution set in
// the containers that carry the batch contract, exactly where buildRun sets
// the parameters.
func buildOpsRun(t *runTemplate, name, namespace, trigger, restartOf, verb string, executionID int64) *unstructured.Unstructured {
	run := buildRun(t, name, namespace, trigger, map[string]string{}, restartOf)
	id := strconv.FormatInt(executionID, 10)
	for _, tpl := range podTemplatesOf(run) {
		for _, c := range liveContainers(tpl) {
			env, _ := c["env"].([]interface{})
			if !hasEnv(env, envParams) {
				continue
			}
			env = setEnv(env, envCommand, verb, true)
			env = setEnv(env, envExecutionID, id, true)
			c["env"] = env
		}
	}
	return run
}

// opsSpec is the run spec of one ops command.
func opsSpec(t *runTemplate, batchJob, namespace, verb string, executionID int64) runSpec {
	return runSpec{Base: opsRunName(batchJob, verb, executionID), Trigger: triggerOps, RestartTrigger: triggerOps,
		build: func(n, trigger, restartOf string) *unstructured.Unstructured {
			return buildOpsRun(t, n, namespace, trigger, restartOf, verb, executionID)
		}}
}

// startOpsOnRun is `adhar batch abandon|recover <run>`: the run names the job
// and, through its summary, the execution; the gates are the ones a person
// would apply. An active run's owner may be alive, and a completed execution
// has nothing to abandon or recover (Spring Batch refuses both).
func startOpsOnRun(c context.Context, dc dynamic.Interface, logs logSource, ns, name, verb string, executionID int64, wait bool, timeout time.Duration) (runOutcome, error) {
	run, err := getRun(c, dc, ns, name)
	if err != nil {
		return runOutcome{}, err
	}
	if isOpsRun(run) {
		return runOutcome{}, fmt.Errorf("%s is itself an ops Job (%s); name the run whose execution it is about", name, envOf(run, envCommand))
	}
	batchJob := run.GetLabels()[labelBatchJob]
	rows, _, err := runsOfJob(c, dc, ns, batchJob)
	if err != nil {
		return runOutcome{}, err
	}
	var row *runRow
	for i := range rows {
		if rows[i].Name == name {
			row = &rows[i]
		}
	}
	if row == nil {
		return runOutcome{}, fmt.Errorf("run %s is not listed among the runs of %s", name, batchJob)
	}
	switch {
	case row.Phase.active():
		return runOutcome{}, fmt.Errorf("%s is %s: its owner may still hold the execution; `adhar batch stop %s` first", name, row.Phase, name)
	case row.Phase == phaseSucceeded:
		return runOutcome{}, fmt.Errorf("%s succeeded: a completed execution has nothing to %s", name, verb)
	case row.Phase == phaseDeleting:
		return runOutcome{}, fmt.Errorf("%s is being deleted", name)
	}
	if executionID == 0 {
		if row.Summary == nil || row.Summary.ExecutionID == nil {
			return runOutcome{}, fmt.Errorf("%s wrote no summary naming its execution (a run lost with its pod writes none); give it with --execution — `adhar batch history %s` shows the ids its events carry", name, batchJob)
		}
		executionID = *row.Summary.ExecutionID
	}
	job, err := getBatchJob(c, dc, ns, batchJob)
	if err != nil {
		return runOutcome{}, err
	}
	t, err := readRunTemplate(c, dc, job)
	if err != nil {
		return runOutcome{}, err
	}
	if !machineReadable() {
		helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("%s execution %d of %s through an ops Job", verb, executionID, name)))
	}
	return executeRun(c, dc, logs, job, opsSpec(t, batchJob, ns, verb, executionID), wait, timeout)
}
