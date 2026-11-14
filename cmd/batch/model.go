/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// model.go is the pure half of `adhar batch`: what a BatchJob XR and the runs
// it produces MEAN, with no fetching, so every rule is unit-tested and agrees
// with the Console's `batch-model.ts`. The contract it reads is design 0025:
//
//   - A run is a Job, or a Volcano Job, labelled adhar.io/batch-job. The
//     kit writes a summary into the batch container's termination message
//     (§5.13, D13); the exit code means what §5.7 says.
//   - Every field is optional. A command that errors on a missing status
//     shows nothing at all, and the kit's runner and the composition are not
//     on every cluster yet.

/* ─────────────────────────── the termination summary (§5.13, D13) ─────────────────────────── */

// runSummary is the kit's termination summary. Raw keeps the document as
// written for `-o json`; the typed fields are what the tables read.
type runSummary struct {
	Raw            map[string]interface{}
	Status         string
	ExitCode       *int
	Outcome        string
	Reason         string
	Trigger        string
	BusinessDate   string
	ExecutionID    *int64
	InstanceID     *int64
	Start, End     string
	Read, Written  *int64
	Skipped        *int64
	DeadLettered   *int64
	StepsTruncated bool
	// Truncated says the text was cut mid-document; Raw holds what could be recovered.
	Truncated bool
}

var summaryScalar = regexp.MustCompile(`"([A-Za-z]+)"\s*:\s*("(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?|true|false)\s*[,}]`)

// parseSummary reads a termination message. A whole document is read as JSON;
// one cut part way through (the kubelet keeps 4 KiB) still yields every field
// written before the cut, and says it was truncated. Text that is not a summary
// at all — a JVM that died before the runner started writes something else, or
// nothing — is nil, never a guess.
func parseSummary(text string) *runSummary {
	t := strings.TrimSpace(text)
	if t == "" || t[0] != '{' {
		return nil
	}
	var whole map[string]interface{}
	if err := json.Unmarshal([]byte(t), &whole); err == nil {
		if !looksLikeSummary(whole) {
			return nil
		}
		return summaryFrom(whole, false)
	}
	// Cut mid-document. Top-level scalars come before `steps`, and each step
	// has its own `read` and `written`, so scalars are read from the head only.
	head := t
	if i := strings.Index(t, `"steps":`); i >= 0 {
		head = t[:i]
	}
	o := map[string]interface{}{}
	for _, m := range summaryScalar.FindAllStringSubmatch(head, -1) {
		var v interface{}
		if err := json.Unmarshal([]byte(m[2]), &v); err == nil {
			o[m[1]] = v
		}
	}
	if !looksLikeSummary(o) {
		return nil
	}
	return summaryFrom(o, true)
}

func looksLikeSummary(o map[string]interface{}) bool {
	_, v := o["v"]
	_, outcome := o["outcome"]
	_, bj := o["batchJob"]
	return v || outcome || bj
}

func summaryFrom(o map[string]interface{}, truncated bool) *runSummary {
	s := &runSummary{Raw: o, Truncated: truncated}
	str := func(k string) string { v, _ := o[k].(string); return v }
	num := func(k string) *int64 {
		f, ok := o[k].(float64)
		if !ok {
			return nil
		}
		n := int64(f)
		return &n
	}
	s.Status, s.Outcome, s.Reason, s.Trigger = str("status"), str("outcome"), str("reason"), str("trigger")
	s.BusinessDate, s.Start, s.End = str("businessDate"), str("start"), str("end")
	if n := num("exitCode"); n != nil {
		code := int(*n)
		s.ExitCode = &code
	}
	s.ExecutionID, s.InstanceID = num("executionId"), num("instanceId")
	s.Read, s.Written, s.Skipped, s.DeadLettered = num("read"), num("written"), num("skipped"), num("deadLettered")
	s.StepsTruncated = o["stepsTruncated"] == true
	return s
}

/* ─────────────────────────── exit codes (§5.7) ─────────────────────────── */

var exitMeanings = map[int]string{
	0:   "completed, or nothing to do",
	1:   "the JVM failed before the run started",
	2:   "invalid request (parameters, unknown job, migration)",
	3:   "failed, not retryable",
	4:   "stopped on request",
	5:   "failed, retryable",
	6:   "abandoned — a new identity is needed",
	7:   "repository state unknown",
	137: "killed (out of memory, or SIGKILL)",
	143: "terminated (SIGTERM)",
}

// exitMeaning is what an exit code means, in a few words.
func exitMeaning(code int) string {
	if m, ok := exitMeanings[code]; ok {
		return m
	}
	return fmt.Sprintf("exit %d", code)
}

var reasonMeanings = map[string]string{
	"already-complete":        "this identity already completed",
	"duplicate":               "another run of this identity is in progress",
	"calendar":                "not a business day",
	"parameters":              "the parameters were refused",
	"unknown-job":             "the job bean does not exist",
	"incrementer":             "the job uses an incrementer",
	"resourceless-repository": "no JDBC job repository",
	"migration":               "the schema migration failed",
	"business-date":           "the business date could not be resolved",
}

// outcomeLabel is the kit's outcome + reason for humans: exit 0 covers four
// different results (D13).
func outcomeLabel(s *runSummary) string {
	if s == nil || s.Outcome == "" {
		return ""
	}
	label := strings.ToUpper(s.Outcome[:1]) + s.Outcome[1:]
	if s.Reason == "" {
		return label
	}
	if m, ok := reasonMeanings[s.Reason]; ok {
		return label + ": " + m
	}
	return label + ": " + s.Reason
}

// restartWarning is what a restart has to say first, because the same identity
// will fail the same way (§5.7).
func restartWarning(exitCode *int) string {
	if exitCode == nil {
		return ""
	}
	switch *exitCode {
	case 2:
		return "this run was refused as an invalid request (exit 2); a restart sends the same parameters, so unless the job was corrected since, it will be refused again"
	case 6:
		return "this run's execution was abandoned (exit 6); the same identity will exit 6 again — it needs new parameters, which is a new run rather than a restart"
	}
	return ""
}

/* ─────────────────────────── runs ─────────────────────────── */

type runPhase string

const (
	phasePending   runPhase = "pending"
	phaseRunning   runPhase = "running"
	phaseStopping  runPhase = "stopping"
	phaseStopped   runPhase = "stopped"
	phaseSucceeded runPhase = "succeeded"
	phaseFailed    runPhase = "failed"
	phaseDeleting  runPhase = "deleting"
	phaseUnknown   runPhase = "unknown"
)

func (p runPhase) label() string { return strings.ToUpper(string(p)[:1]) + string(p)[1:] }

// active says the run has not ended: pending, running or still stopping.
func (p runPhase) active() bool { return p == phasePending || p == phaseRunning || p == phaseStopping }

// terminal says nothing more will happen to this run by itself.
func (p runPhase) terminal() bool {
	return p == phaseSucceeded || p == phaseFailed || p == phaseStopped
}

func condTrue(run *unstructured.Unstructured, typ string) (bool, string, string) {
	conds, _, _ := unstructured.NestedSlice(run.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok || m["type"] != typ || m["status"] != "True" {
			continue
		}
		msg, _ := m["message"].(string)
		at, _ := m["lastTransitionTime"].(string)
		return true, msg, at
	}
	return false, "", ""
}

func statusInt(run *unstructured.Unstructured, field string) int64 {
	n, _, _ := unstructured.NestedInt64(run.Object, "status", field)
	return n
}

// runPhaseOf is the phase a person reads. FailureTarget / SuccessCriteriaMet
// count: the Job has decided, its pods are only leaving.
func runPhaseOf(run *unstructured.Unstructured) runPhase {
	if run.GetDeletionTimestamp() != nil {
		return phaseDeleting
	}
	if runKindOf(run) == "VolcanoJob" {
		phase, _, _ := unstructured.NestedString(run.Object, "status", "state", "phase")
		switch phase {
		case "Pending", "":
			return phasePending
		case "Running", "Restarting", "Completing":
			return phaseRunning
		case "Aborting":
			return phaseStopping
		case "Aborted":
			return phaseStopped
		case "Completed":
			return phaseSucceeded
		case "Failed", "Terminating", "Terminated":
			return phaseFailed
		default:
			return phaseUnknown
		}
	}
	if ok, _, _ := condTrue(run, "Complete"); ok {
		return phaseSucceeded
	}
	if ok, _, _ := condTrue(run, "SuccessCriteriaMet"); ok {
		return phaseSucceeded
	}
	if ok, _, _ := condTrue(run, "Failed"); ok {
		return phaseFailed
	}
	if ok, _, _ := condTrue(run, "FailureTarget"); ok {
		return phaseFailed
	}
	active := statusInt(run, "active")
	suspended, _, _ := unstructured.NestedBool(run.Object, "spec", "suspend")
	if condSuspended, _, _ := condTrue(run, "Suspended"); suspended || condSuspended {
		if active > 0 || statusInt(run, "terminating") > 0 {
			return phaseStopping
		}
		return phaseStopped
	}
	if active > 0 {
		ready, found, _ := unstructured.NestedInt64(run.Object, "status", "ready")
		if !found || ready > 0 {
			return phaseRunning
		}
		return phasePending
	}
	return phasePending
}

// runRow is everything a list row or a detail page shows about one run.
type runRow struct {
	Name         string
	Namespace    string
	Kind         string
	BatchJob     string
	Application  string
	Environment  string
	Phase        runPhase
	Message      string
	Trigger      string
	Actor        string
	BusinessDate string
	RestartOf    string
	ScheduledAt  string
	Attempts     int
	StartedAt    string
	FinishedAt   string
	Duration     time.Duration
	HasDuration  bool
	ExitCode     *int
	Summary      *runSummary
	Image        string
	Created      time.Time
	// Scheduler, Queue and Gang are set only for a run Volcano places (Phase
	// 2, gang.go); a run kube-scheduler places has none of them.
	Scheduler string
	Queue     string
	Gang      *gangView
	obj       *unstructured.Unstructured
}

func podLabel(pod *unstructured.Unstructured, key string) string { return pod.GetLabels()[key] }

// podsOfRun are the pods of one run, newest first.
func podsOfRun(run *unstructured.Unstructured, pods []unstructured.Unstructured) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	volcano := runKindOf(run) == "VolcanoJob"
	for i := range pods {
		p := &pods[i]
		if p.GetNamespace() != "" && run.GetNamespace() != "" && p.GetNamespace() != run.GetNamespace() {
			continue
		}
		if volcano {
			if podLabel(p, labelVolcanoJob) == run.GetName() {
				out = append(out, p)
			}
		} else if podLabel(p, labelNativeJob) == run.GetName() || podLabel(p, labelNativeJobLegacy) == run.GetName() {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].GetCreationTimestamp().Time.After(out[j].GetCreationTimestamp().Time)
	})
	return out
}

// termination is the batch container's end state, current or the last before a
// restart in place.
type termination struct {
	ExitCode int
	Reason   string
	Message  string
}

func terminationOf(pod *unstructured.Unstructured) *termination {
	statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	var chosen map[string]interface{}
	for _, s := range statuses {
		m, ok := s.(map[string]interface{})
		if ok && m["name"] == batchContainer {
			chosen = m
		}
	}
	if chosen == nil && len(statuses) == 1 {
		chosen, _ = statuses[0].(map[string]interface{})
	}
	if chosen == nil {
		return nil
	}
	for _, path := range [][]string{{"state", "terminated"}, {"lastState", "terminated"}} {
		t, ok, _ := unstructured.NestedMap(chosen, path...)
		if !ok {
			continue
		}
		code, _, _ := unstructured.NestedInt64(t, "exitCode")
		reason, _, _ := unstructured.NestedString(t, "reason")
		msg, _, _ := unstructured.NestedString(t, "message")
		return &termination{ExitCode: int(code), Reason: reason, Message: msg}
	}
	return nil
}

// speakerPod is the pod whose summary speaks for the run: the manager when
// there is one (in an indexed job, index 0 is the manager, §5.5), otherwise the
// newest attempt. Pods are newest first, and a pod still running has no summary
// yet, so the first pod that has written one wins within the eligible set.
func speakerPod(pods []*unstructured.Unstructured) *unstructured.Unstructured {
	var notWorkers, managers []*unstructured.Unstructured
	for _, p := range pods {
		role := podRole(p)
		if role == "worker" {
			continue
		}
		notWorkers = append(notWorkers, p)
		if role == "manager" || podIndex(p) == "0" {
			managers = append(managers, p)
		}
	}
	set := pods
	if len(managers) > 0 {
		set = managers
	} else if len(notWorkers) > 0 {
		set = notWorkers
	}
	for _, p := range set {
		if t := terminationOf(p); t != nil && parseSummary(t.Message) != nil {
			return p
		}
	}
	for _, p := range set {
		if terminationOf(p) != nil {
			return p
		}
	}
	if len(set) > 0 {
		return set[0]
	}
	return nil
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// finishedAtOf is when a finished run finished. A failed or suspended Job has
// no completionTime; its condition says when.
func finishedAtOf(run *unstructured.Unstructured, phase runPhase, summary *runSummary) string {
	if !phase.terminal() {
		return ""
	}
	if t, _, _ := unstructured.NestedString(run.Object, "status", "completionTime"); t != "" {
		return t
	}
	types := map[runPhase][]string{phaseFailed: {"Failed", "FailureTarget"}, phaseSucceeded: {"Complete", "SuccessCriteriaMet"}, phaseStopped: {"Suspended"}}[phase]
	for _, typ := range types {
		if ok, _, at := condTrue(run, typ); ok && at != "" {
			return at
		}
	}
	if at, _, _ := unstructured.NestedString(run.Object, "status", "state", "lastTransitionTime"); at != "" {
		return at
	}
	if summary != nil {
		return summary.End
	}
	return ""
}

func attemptsOf(run *unstructured.Unstructured, pods []*unstructured.Unstructured) int {
	if runKindOf(run) == "VolcanoJob" {
		phase, _, _ := unstructured.NestedString(run.Object, "status", "state", "phase")
		retries := statusInt(run, "retryCount")
		if phase == "Pending" && retries == 0 {
			return 0
		}
		return int(retries) + 1
	}
	counted := int(statusInt(run, "failed") + statusInt(run, "succeeded") + statusInt(run, "active"))
	// An indexed job counts one pod per index, not one per attempt: never
	// below what the status says.
	seen := 0
	for _, p := range pods {
		if podRole(p) != "worker" {
			seen++
		}
	}
	if seen > counted {
		return seen
	}
	return counted
}

// runRowOf builds the row of one run from the run object and the pods of its
// namespace (they carry the summaries and the exit codes).
func runRowOf(run *unstructured.Unstructured, allPods []unstructured.Unstructured, now time.Time) runRow {
	labels := run.GetLabels()
	annotations := run.GetAnnotations()
	pods := podsOfRun(run, allPods)
	var term *termination
	var summary *runSummary
	if speaker := speakerPod(pods); speaker != nil {
		term = terminationOf(speaker)
		if term != nil {
			summary = parseSummary(term.Message)
		}
	}
	phase := runPhaseOf(run)
	row := runRow{
		Name:        run.GetName(),
		Namespace:   run.GetNamespace(),
		Kind:        runKindOf(run),
		BatchJob:    labels[labelBatchJob],
		Application: labels["adhar.io/application"],
		Environment: labels["adhar.io/environment"],
		Phase:       phase,
		Actor:       annotations[annotationActor],
		ScheduledAt: annotations[annotationScheduled],
		RestartOf:   envOf(run, envRestartOf),
		Attempts:    attemptsOf(run, pods),
		Summary:     summary,
		Image:       runImage(run),
		Created:     run.GetCreationTimestamp().Time,
		obj:         run,
	}
	if volcanoScheduled(run) {
		row.Scheduler, row.Queue, row.Gang = schedulerVolcano, queueOfRun(run), gangOfRun(run)
	}
	for _, typ := range []string{"Failed", "FailureTarget", "Suspended"} {
		if ok, msg, _ := condTrue(run, typ); ok && msg != "" {
			row.Message = msg
			break
		}
	}
	if row.Message == "" {
		row.Message, _, _ = unstructured.NestedString(run.Object, "status", "state", "message")
	}
	row.Trigger = labels[labelTrigger]
	if row.Trigger == "" {
		row.Trigger = envOf(run, envTrigger)
	}
	if row.Trigger == "" && (row.ScheduledAt != "" || ownedByCronJob(run)) {
		row.Trigger = "schedule"
	}
	params := runParams(run)
	switch {
	case summary != nil && summary.BusinessDate != "":
		row.BusinessDate = summary.BusinessDate
	case params["businessDate"] != "":
		row.BusinessDate = params["businessDate"]
	case row.ScheduledAt != "":
		if at, ok := parseTime(row.ScheduledAt); ok {
			row.BusinessDate = dateInZone(at, envOf(run, envTimeZone))
		}
	}
	row.StartedAt, _, _ = unstructured.NestedString(run.Object, "status", "startTime")
	if row.StartedAt == "" && summary != nil {
		row.StartedAt = summary.Start
	}
	if row.StartedAt == "" && len(pods) > 0 {
		row.StartedAt = pods[len(pods)-1].GetCreationTimestamp().Format(time.RFC3339)
	}
	row.FinishedAt = finishedAtOf(run, phase, summary)
	if start, ok := parseTime(row.StartedAt); ok {
		end, endOK := parseTime(row.FinishedAt)
		if !endOK && phase.active() {
			end, endOK = now, true
		}
		if endOK && !end.Before(start) {
			row.Duration, row.HasDuration = end.Sub(start), true
		}
	}
	switch {
	case summary != nil && summary.ExitCode != nil:
		row.ExitCode = summary.ExitCode
	case term != nil:
		code := term.ExitCode
		row.ExitCode = &code
	}
	return row
}

func ownedByCronJob(run *unstructured.Unstructured) bool {
	for _, o := range run.GetOwnerReferences() {
		if o.Kind == "CronJob" {
			return true
		}
	}
	return false
}

// runRowsOf builds every row, newest first.
func runRowsOf(runs []unstructured.Unstructured, pods []unstructured.Unstructured, now time.Time) []runRow {
	rows := make([]runRow, 0, len(runs))
	for i := range runs {
		rows = append(rows, runRowOf(&runs[i], pods, now))
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Created.After(rows[j].Created) })
	return rows
}

/* ─────────────────────────── what can be done to a run (§3.4, §7.4) ─────────────────────────── */

// runActions: stop while it can still stop, resume what was stopped, restart
// what failed — the same gates the Console's buttons use.
type runActions struct{ Stop, Resume, Restart bool }

func runActionsOf(phase runPhase) runActions {
	return runActions{
		Stop:    phase == phasePending || phase == phaseRunning,
		Resume:  phase == phaseStopped,
		Restart: phase == phaseFailed,
	}
}

// runRequest is one Kubernetes write, as data: the command performs it, the
// tests read it.
type runRequest struct {
	Verb      string // patch | create
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	Body      map[string]interface{}
}

// volcanoCommand is the bus.volcano.sh Command `vcctl job suspend` / `resume`
// would create.
func volcanoCommand(run *unstructured.Unstructured, action string) map[string]interface{} {
	owner := map[string]interface{}{
		"apiVersion":         "batch.volcano.sh/v1alpha1",
		"kind":               "Job",
		"name":               run.GetName(),
		"uid":                string(run.GetUID()),
		"controller":         true,
		"blockOwnerDeletion": true,
	}
	return map[string]interface{}{
		"apiVersion": "bus.volcano.sh/v1alpha1",
		"kind":       "Command",
		"metadata": map[string]interface{}{
			"generateName":    run.GetName() + "-" + strings.ToLower(action) + "-",
			"namespace":       run.GetNamespace(),
			"ownerReferences": []interface{}{owner},
		},
		"action": action,
		"target": owner,
	}
}

// stopRequest: suspend a native Job (pods get SIGTERM and grace, §3.4), abort a
// Volcano one.
func stopRequest(run *unstructured.Unstructured) runRequest {
	if runKindOf(run) == "VolcanoJob" {
		return runRequest{Verb: "create", GVR: commandGVR, Namespace: run.GetNamespace(), Body: volcanoCommand(run, "AbortJob")}
	}
	return runRequest{Verb: "patch", GVR: jobGVR, Namespace: run.GetNamespace(), Name: run.GetName(),
		Body: map[string]interface{}{"spec": map[string]interface{}{"suspend": true}}}
}

// resumeRequest: the new pod finds the execution STOPPED and restarts it.
func resumeRequest(run *unstructured.Unstructured) runRequest {
	if runKindOf(run) == "VolcanoJob" {
		return runRequest{Verb: "create", GVR: commandGVR, Namespace: run.GetNamespace(), Body: volcanoCommand(run, "ResumeJob")}
	}
	return runRequest{Verb: "patch", GVR: jobGVR, Namespace: run.GetNamespace(), Name: run.GetName(),
		Body: map[string]interface{}{"spec": map[string]interface{}{"suspend": false}}}
}

// holdPatch is the ONE thing an operator may change on a BatchJob (§3.5,
// §10.3): the adhar.io/batch-hold annotation, set with a reason or removed.
func holdPatch(reason *string) map[string]interface{} {
	var value interface{}
	if reason != nil {
		value = strings.TrimSpace(*reason)
	}
	return map[string]interface{}{"metadata": map[string]interface{}{"annotations": map[string]interface{}{annotationHold: value}}}
}

/* ─────────────────────────── jobs ─────────────────────────── */

type jobStatus string

const (
	jobRunning   jobStatus = "running"
	jobFailed    jobStatus = "last run failed"
	jobNotReady  jobStatus = "not ready"
	jobStopped   jobStatus = "stopped"
	jobSucceeded jobStatus = "last run succeeded"
	jobIdle      jobStatus = "no runs yet"
	jobDeleting  jobStatus = "deleting"
)

// jobConditions are the conditions the composition reports that a BatchJob needs (§6.2).
var jobConditions = []string{"Ready", "Synced", "ScheduleReady", "TemplateReady", "PrerequisitesReady"}

type condition struct{ Type, Status, Reason, Message string }

func conditionsOf(obj *unstructured.Unstructured) []condition {
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	var out []condition
	for _, c := range raw {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		var cond condition
		cond.Type, _ = m["type"].(string)
		cond.Status, _ = m["status"].(string)
		cond.Reason, _ = m["reason"].(string)
		cond.Message, _ = m["message"].(string)
		out = append(out, cond)
	}
	return out
}

// jobProblem is the first condition that says the definition is not ready.
func jobProblem(job *unstructured.Unstructured) string {
	conds := conditionsOf(job)
	for _, typ := range jobConditions {
		for _, c := range conds {
			if c.Type == typ && c.Status == "False" {
				if c.Message != "" {
					return c.Message
				}
				return typ + ": " + c.Reason
			}
		}
	}
	return ""
}

// jobStatusOf is one phrase for the job's row. What its runs say comes first:
// a failed run outranks a definition still reconciling.
func jobStatusOf(job *unstructured.Unstructured, runs []runRow) jobStatus {
	if job.GetDeletionTimestamp() != nil {
		return jobDeleting
	}
	for _, r := range runs {
		if r.Phase.active() {
			return jobRunning
		}
	}
	var last *runRow
	if len(runs) > 0 {
		last = &runs[0]
	}
	if last != nil && last.Phase == phaseFailed {
		return jobFailed
	}
	if jobProblem(job) != "" {
		return jobNotReady
	}
	if last != nil && last.Phase == phaseStopped {
		return jobStopped
	}
	if last != nil && last.Phase == phaseSucceeded {
		return jobSucceeded
	}
	return jobIdle
}

// heldReason is the hold on a job: the annotation, else what the composition
// last reported; "" and false when there is none.
func heldReason(job *unstructured.Unstructured) (string, bool) {
	if r, ok := job.GetAnnotations()[annotationHold]; ok {
		return r, true
	}
	if r, ok, _ := unstructured.NestedString(job.Object, "status", "held", "reason"); ok {
		return r, true
	}
	return "", false
}

// scheduleOf is the cron, its zone and whether Git suspended it.
func scheduleOf(job *unstructured.Unstructured) (cron, timeZone string, suspended bool) {
	cron, _, _ = unstructured.NestedString(job.Object, "spec", "parameters", "schedule", "cron")
	timeZone, _, _ = unstructured.NestedString(job.Object, "spec", "parameters", "schedule", "timeZone")
	if timeZone == "" {
		timeZone = "UTC"
	}
	suspended, _, _ = unstructured.NestedBool(job.Object, "spec", "parameters", "schedule", "suspend")
	return cron, timeZone, suspended
}

// nextRunOf is when the job next fires: nothing for a held, suspended or
// on-demand job, because a time on the screen would say it is going to run.
func nextRunOf(job *unstructured.Unstructured, now time.Time) (time.Time, bool) {
	cron, tz, suspended := scheduleOf(job)
	if cron == "" || suspended {
		return time.Time{}, false
	}
	if _, held := heldReason(job); held {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return nextCron(cron, now, loc)
}

/* ─────────────────────────── the restart plan (§3.4) ─────────────────────────── */

type restartPlan struct {
	Name        string
	Base        string
	Params      map[string]string
	Identifying map[string]string
	// ImageFrom/ImageTo are set when the run's image and the template's differ.
	ImageFrom, ImageTo string
}

// planRestart restarts a failed run: the same IDENTIFYING parameters, so Spring
// Batch finds the same JobInstance and re-executes only the failed steps. A
// scheduled run has `ADHAR_BATCH_PARAMS: {}` and got its date from the
// schedule, so the restart must state it explicitly: from the run's summary,
// else its scheduled timestamp. Without that it would start a new JobInstance,
// re-processing a business date from the top.
func planRestart(batchJob string, run runRow, t *runTemplate, existingNames []string, now time.Time) (*restartPlan, error) {
	given := runParams(run.obj)
	values := map[string]string{}
	scheduledAt, hasScheduled := parseTime(run.ScheduledAt)
	for _, f := range t.Fields {
		switch {
		case given[f.Name] != "":
			values[f.Name] = given[f.Name]
		case f.Source == "schedule" && f.Type == "date" && run.BusinessDate != "":
			values[f.Name] = run.BusinessDate
		case f.Source == "schedule" && f.Type == "datetime" && hasScheduled:
			values[f.Name] = scheduledAt.UTC().Format(time.RFC3339)
		}
	}
	resolved := resolveParams(t.Fields, values, envOf(t.Manifest, envTimeZone), now)
	if len(resolved.Errors) > 0 {
		return nil, fmt.Errorf("this run's parameters cannot be reused: %s", strings.Join(resolved.problems(), "; "))
	}
	base, err := runName(batchJob, resolved.Identifying)
	if err != nil {
		return nil, err
	}
	name, ok := nextRestartName(base, existingNames)
	if !ok {
		return nil, fmt.Errorf("%s has reached %d restarts; investigate the failure before going further", base, maxRestarts)
	}
	plan := &restartPlan{Name: name, Base: base, Params: resolved.Params, Identifying: resolved.Identifying}
	if from, to := run.Image, runImage(t.Manifest); from != "" && to != "" && from != to {
		plan.ImageFrom, plan.ImageTo = from, to
	}
	return plan, nil
}

/* ─────────────────────────── the idempotent run (D2, §12.1) ─────────────────────────── */

// runDecision is what `adhar batch run --wait` does about the runs that
// already carry this identity.
type runDecision struct {
	// Action: create (nothing exists), reuse (completed), wait (in flight),
	// restart (failed → next -r<n>), stopped (suspended: needs resume).
	Action string
	// Target is the run the action is about: the one to create, or the one that exists.
	Target string
	// Existing is the latest run of the chain, when there is one.
	Existing *runRow
}

// decideRun looks at the restart CHAIN of base — base itself and its -r<n>
// restarts — and judges the LATEST member, because a base that failed and was
// restarted is described by its restart, not by itself.
func decideRun(base string, rows []runRow) (runDecision, error) {
	var latest *runRow
	latestIndex := -1
	var names []string
	for i := range rows {
		n, ok := restartIndex(base, rows[i].Name)
		if !ok {
			continue
		}
		names = append(names, rows[i].Name)
		if n > latestIndex {
			latestIndex, latest = n, &rows[i]
		}
	}
	if latest == nil {
		return runDecision{Action: "create", Target: base}, nil
	}
	switch latest.Phase {
	case phaseSucceeded:
		return runDecision{Action: "reuse", Target: latest.Name, Existing: latest}, nil
	case phaseFailed:
		next, ok := nextRestartName(base, names)
		if !ok {
			return runDecision{}, fmt.Errorf("%s has reached %d restarts; investigate the failure before going further", base, maxRestarts)
		}
		return runDecision{Action: "restart", Target: next, Existing: latest}, nil
	case phaseStopped:
		return runDecision{Action: "stopped", Target: latest.Name, Existing: latest}, nil
	default:
		return runDecision{Action: "wait", Target: latest.Name, Existing: latest}, nil
	}
}

// runOutcome is D2's answer: `{run, namespace, batchJob, kind, status,
// exitCode, executionId, summary}`, and the process exit code the CLI ends with.
type runOutcome struct {
	Run         string                 `json:"run" yaml:"run"`
	Namespace   string                 `json:"namespace" yaml:"namespace"`
	BatchJob    string                 `json:"batchJob" yaml:"batchJob"`
	Kind        string                 `json:"kind" yaml:"kind"`
	Status      string                 `json:"status" yaml:"status"`
	ExitCode    int                    `json:"exitCode" yaml:"exitCode"`
	ExecutionID *int64                 `json:"executionId" yaml:"executionId"`
	Summary     map[string]interface{} `json:"summary" yaml:"summary"`
}

// outcomeOf maps a run's phase onto D2's four statuses and the exit code the
// CLI's process ends with: the run's own (0 includes SKIPPED), 4 — "stopped on
// request" in §5.7 — for a suspended run, and 1 for a failure whose code was
// lost with its pod (OOM, node lost: the outcome is in the event, not here).
func outcomeOf(row runRow) runOutcome {
	out := runOutcome{Run: row.Name, Namespace: row.Namespace, BatchJob: row.BatchJob, Kind: row.Kind}
	if row.Summary != nil {
		out.Summary = row.Summary.Raw
		out.ExecutionID = row.Summary.ExecutionID
	}
	switch row.Phase {
	case phaseSucceeded:
		out.Status = "Succeeded"
	case phaseFailed:
		out.Status = "Failed"
		out.ExitCode = 1
		if row.ExitCode != nil && *row.ExitCode != 0 {
			out.ExitCode = *row.ExitCode
		}
	case phaseStopped, phaseStopping:
		out.Status = "Stopped"
		out.ExitCode = 4
	default:
		out.Status = "Running"
	}
	return out
}
