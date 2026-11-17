/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The workflow step (design 0025 §12.1, D10): `adhar batch run` as the one
// container of ClusterWorkflowTemplate adhar-batch-steps/run, in the
// argo-workflows package's catalogue. The step IS this command, so D2's
// idempotency is not written twice; what differs is how the request arrives and
// where the answer goes. The platform image has no shell, so nothing can expand
// the step's `parameters` input into -p flags or tee the outcome into the files
// Argo reads outputs from: both travel in the environment, and the four
// variables below are the whole contract with that template
// (TestBatchStepRunsTheCLIContract pins its side).
const (
	// envStepWorkflow is the workflow's name. A run started for one carries the
	// trigger `workflow` and names the workflow in adhar.io/batch-workflow.
	envStepWorkflow = "ADHAR_BATCH_STEP_WORKFLOW"
	// envStepParameters is the step's `parameters` input: a JSON object of the
	// job's parameters, what -p key=value would set. -p wins for a key in both.
	envStepParameters = "ADHAR_BATCH_STEP_PARAMETERS"
	// envStepOutputs is a directory; the outcome is written into it as one file
	// per output of §12.1 (run, status, exitCode, executionId, summary), which
	// Argo reads even when the step fails.
	envStepOutputs = "ADHAR_BATCH_STEP_OUTPUTS"
	// envStepRetry is Argo's retry count for the step, 0 on the first attempt.
	envStepRetry = "ADHAR_BATCH_STEP_RETRY"

	annotationWorkflow = "adhar.io/batch-workflow"
	triggerWorkflow    = "workflow"
)

// workflowStep is the step's side of one `adhar batch run`; the zero value is a
// person's run, which none of this touches.
type workflowStep struct {
	// Workflow names the workflow the run is started for.
	Workflow string
	// Params are the job's parameters from the step's `parameters` input.
	Params map[string]string
	// Outputs is the directory the outcome is written into.
	Outputs string
	// Retry is Argo's retry count for this step.
	Retry int
}

// stepFromEnv reads the contract. A blank variable is one the step did not set,
// and a retry count that is not a number is 0: Argo substitutes {{retries}}
// only where the template has a retryStrategy, and a first attempt is what an
// unsubstituted one must be read as, because it restarts a failed run exactly
// as a person's `run --wait` does.
func stepFromEnv(getenv func(string) string) (workflowStep, error) {
	step := workflowStep{
		Workflow: strings.TrimSpace(getenv(envStepWorkflow)),
		Outputs:  strings.TrimSpace(getenv(envStepOutputs)),
	}
	if n, err := strconv.Atoi(strings.TrimSpace(getenv(envStepRetry))); err == nil && n > 0 {
		step.Retry = n
	}
	params, err := parseStepParameters(getenv(envStepParameters))
	if err != nil {
		return workflowStep{}, err
	}
	step.Params = params
	return step, nil
}

// parseStepParameters reads the `parameters` input: one JSON object whose
// values are scalars, each taken as the text it was written as — a number is
// decoded as its literal, so 20261103 stays 20261103 and never becomes
// 2.0261103e+07 on the way to the run's name. null leaves a parameter out, as
// not passing -p does. Anything else is refused: a nested value has no
// key=value spelling, and a guess at one would name a different run.
func parseStepParameters(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	refuse := func(why string) (map[string]string, error) {
		return nil, fmt.Errorf("%s must be a JSON object of the job's parameters, like {\"businessDate\":\"2026-11-03\"}: %s", envStepParameters, why)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var doc interface{}
	if err := dec.Decode(&doc); err != nil {
		return refuse(err.Error())
	}
	if _, err := dec.Token(); err != io.EOF {
		return refuse("there is text after the object")
	}
	object, ok := doc.(map[string]interface{})
	if !ok {
		return refuse("it is not an object")
	}
	out := map[string]string{}
	for k, v := range object {
		switch value := v.(type) {
		case nil:
		case string:
			out[k] = value
		case json.Number:
			out[k] = value.String()
		case bool:
			out[k] = strconv.FormatBool(value)
		default:
			return refuse(fmt.Sprintf("%q is not a string, a number or a boolean", k))
		}
	}
	return out, nil
}

// withParameters merges the step's parameters under the command line's.
func (s workflowStep) withParameters(flags map[string]string) map[string]string {
	if len(s.Params) == 0 {
		return flags
	}
	out := map[string]string{}
	for k, v := range s.Params {
		out[k] = v
	}
	for k, v := range flags {
		out[k] = v
	}
	return out
}

// annotate names the workflow on a run started for it. On the run only: run
// integrity compares labels and the pod template, never the run's own
// annotations, so this is one of the things a client may set.
func (s workflowStep) annotate(run *unstructured.Unstructured) *unstructured.Unstructured {
	if s.Workflow == "" {
		return run
	}
	annotations := run.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[annotationWorkflow] = s.Workflow
	run.SetAnnotations(annotations)
	return run
}

// reportsFailure decides the one case where the step must NOT carry D2's
// "failed → restart as -r<n>": a retry of the step (Argo retries it only when
// its pod was lost) that finds a failed run THIS workflow started. That run
// already gave the step its answer, and the pod died before reporting it;
// restarting it would run the job a second time behind a failure nobody saw.
// A first attempt — a new workflow, or `argo retry` of this one — restarts as
// a person would.
func (s runSpec) reportsFailure(failed *runRow) bool {
	if s.StepRetry <= 0 || s.Workflow == "" || failed == nil || failed.obj == nil {
		return false
	}
	return failed.obj.GetAnnotations()[annotationWorkflow] == s.Workflow
}

// stepOutputsOf is the outcome as the files of §12.1's outputs. A run that has
// not ended has no exit code, so that file is empty, as the template's default
// is; a run that named no execution leaves executionId empty; the summary is
// the termination summary as the runner wrote it, `{}` when there is none.
func stepOutputsOf(o runOutcome) map[string]string {
	out := map[string]string{"run": o.Run, "status": o.Status, "exitCode": "", "executionId": "", "summary": "{}"}
	if o.Status != "Running" {
		out["exitCode"] = strconv.Itoa(o.ExitCode)
	}
	if o.ExecutionID != nil {
		out["executionId"] = strconv.FormatInt(*o.ExecutionID, 10)
	}
	if o.Summary != nil {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(o.Summary); err == nil {
			out["summary"] = strings.TrimSpace(b.String())
		}
	}
	return out
}

// writeStepOutputs writes the outcome into the step's output directory, one
// file per output and no trailing newline: the file IS the parameter's value.
func writeStepOutputs(dir string, o runOutcome) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("writing the step outputs (%s): %w", envStepOutputs, err)
	}
	for name, value := range stepOutputsOf(o) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			return fmt.Errorf("writing the step output %s (%s): %w", name, envStepOutputs, err)
		}
	}
	return nil
}

// finishStep hands the outcome to the workflow and then to the process, in
// that order: the files are what the next step reads, so they are written
// before the exit code ends the container. Outputs that cannot be written fail
// a step that would otherwise succeed — its `status` would read as the
// template's default, Unknown, and whatever is gated on it would be skipped
// behind a green step — but never replace a run's own exit code, which says
// more.
func finishStep(step workflowStep, outcome runOutcome, runErr error) error {
	var writeErr error
	if step.Outputs != "" && outcome.Run != "" {
		writeErr = writeStepOutputs(step.Outputs, outcome)
	}
	if runErr != nil {
		return runErr
	}
	if err := finishOutcome(outcome); err != nil {
		if writeErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", writeErr)
		}
		return err
	}
	return writeErr
}
