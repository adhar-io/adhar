/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// model.go is what a run IS, read from Kubernetes objects alone: its phase, its
// termination summary (§5.13, D13), its business date and the attempts that
// ended without a word. The rules are the CLI's (`cmd/batch/model.go`) and the
// Console's (`batch-model.ts`), because three readers that disagreed about
// whether a run succeeded would publish, print and show three different things.

/* ─────────────────────────── the termination summary ─────────────────────────── */

type runSummary struct {
	Status       string
	ExitCode     *int
	Outcome      string
	Reason       string
	Message      string
	Job          string
	BusinessDate string
	ExecutionID  *int64
	InstanceID   *int64
	Start, End   string
	Read         *int64
	Written      *int64
	Skipped      *int64
	DeadLettered *int64
	Truncated    bool
}

var summaryScalar = regexp.MustCompile(`"([A-Za-z]+)"\s*:\s*("(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?|true|false)\s*[,}]`)

// parseSummary reads a termination message: whole JSON, or the head of one the
// kubelet cut at 4 KiB. Text that is not a summary — a JVM that died before the
// runner started writes something else, or nothing — is nil, never a guess.
// That nil is what makes an attempt LOST (§12).
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
	s := &runSummary{Truncated: truncated}
	str := func(k string) string { v, _ := o[k].(string); return v }
	num := func(k string) *int64 {
		f, ok := o[k].(float64)
		if !ok {
			return nil
		}
		n := int64(f)
		return &n
	}
	s.Status, s.Outcome, s.Reason, s.Job = str("status"), str("outcome"), str("reason"), str("job")
	s.Message = str("exitDescription")
	s.BusinessDate, s.Start, s.End = str("businessDate"), str("start"), str("end")
	if n := num("exitCode"); n != nil {
		code := int(*n)
		s.ExitCode = &code
	}
	s.ExecutionID, s.InstanceID = num("executionId"), num("instanceId")
	s.Read, s.Written, s.Skipped, s.DeadLettered = num("read"), num("written"), num("skipped"), num("deadLettered")
	return s
}

/* ─────────────────────────── phases ─────────────────────────── */

type runPhase string

const (
	phasePending   runPhase = "pending"
	phaseRunning   runPhase = "running"
	phaseStopping  runPhase = "stopping"
	phaseStopped   runPhase = "stopped"
	phaseSucceeded runPhase = "succeeded"
	phaseFailed    runPhase = "failed"
	phaseUnknown   runPhase = "unknown"
)

func (p runPhase) terminal() bool { return p == phaseSucceeded || p == phaseFailed }

func runKindOf(run *unstructured.Unstructured) string {
	if strings.HasPrefix(run.GetAPIVersion(), "batch.volcano.sh") {
		return kindVolcanoJob
	}
	return kindJob
}

const (
	kindJob        = "Job"
	kindVolcanoJob = "VolcanoJob"
)

func condition(run *unstructured.Unstructured, typ string) (ok bool, reason, message, at string) {
	conds, _, _ := unstructured.NestedSlice(run.Object, "status", "conditions")
	for _, c := range conds {
		m, isMap := c.(map[string]interface{})
		if !isMap || m["type"] != typ || m["status"] != "True" {
			continue
		}
		reason, _ = m["reason"].(string)
		message, _ = m["message"].(string)
		at, _ = m["lastTransitionTime"].(string)
		return true, reason, message, at
	}
	return false, "", "", ""
}

func statusInt(run *unstructured.Unstructured, field string) int64 {
	n, _, _ := unstructured.NestedInt64(run.Object, "status", field)
	return n
}

// phaseOf is the CLI's runPhaseOf: FailureTarget and SuccessCriteriaMet count,
// because the Job has decided and its pods are only leaving.
func phaseOf(run *unstructured.Unstructured) runPhase {
	if runKindOf(run) == kindVolcanoJob {
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
	for _, typ := range []string{"Complete", "SuccessCriteriaMet"} {
		if ok, _, _, _ := condition(run, typ); ok {
			return phaseSucceeded
		}
	}
	for _, typ := range []string{"Failed", "FailureTarget"} {
		if ok, _, _, _ := condition(run, typ); ok {
			return phaseFailed
		}
	}
	active := statusInt(run, "active")
	suspended, _, _ := unstructured.NestedBool(run.Object, "spec", "suspend")
	if condSuspended, _, _, _ := condition(run, "Suspended"); suspended || condSuspended {
		if active > 0 || statusInt(run, "terminating") > 0 {
			return phaseStopping
		}
		return phaseStopped
	}
	if active > 0 {
		return phaseRunning
	}
	return phasePending
}

/* ─────────────────────────── pods ─────────────────────────── */

type termination struct {
	ExitCode int
	Reason   string
	Message  string
	At       string
}

// terminationOf is the batch container's end state, current or the last before
// a restart in place.
func terminationOf(pod *unstructured.Unstructured) *termination {
	statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	var chosen map[string]interface{}
	for _, s := range statuses {
		if m, ok := s.(map[string]interface{}); ok && m["name"] == batchContainer {
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
		at, _, _ := unstructured.NestedString(t, "finishedAt")
		return &termination{ExitCode: int(code), Reason: reason, Message: msg, At: at}
	}
	return nil
}

func podPhase(pod *unstructured.Unstructured) string {
	p, _, _ := unstructured.NestedString(pod.Object, "status", "phase")
	return p
}

func podRole(pod *unstructured.Unstructured) string {
	return pod.GetLabels()[labelRole]
}

// speaks says a pod's summary may speak for the run: not a gang worker (§5.5:
// the manager, index 0 of an indexed job, or the single pod).
func speaks(pod *unstructured.Unstructured) bool {
	if podRole(pod) == "worker" {
		return false
	}
	if idx, ok := pod.GetLabels()[labelNativeIdx]; ok && idx != "0" {
		return false
	}
	return true
}

// podsOfRun are the pods of one run, newest first.
func podsOfRun(run *unstructured.Unstructured, pods []unstructured.Unstructured) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	volcano := runKindOf(run) == kindVolcanoJob
	for i := range pods {
		p := &pods[i]
		if p.GetNamespace() != run.GetNamespace() {
			continue
		}
		l := p.GetLabels()
		if volcano {
			if l[labelVolcanoJob] == run.GetName() {
				out = append(out, p)
			}
			continue
		}
		owned := false
		for _, o := range p.GetOwnerReferences() {
			if o.UID == run.GetUID() {
				owned = true
			}
		}
		if owned || l[labelNativeJob] == run.GetName() || l[labelLegacyJob] == run.GetName() {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].GetCreationTimestamp().After(out[j].GetCreationTimestamp().Time)
	})
	return out
}

/* ─────────────────────────── one run, observed ─────────────────────────── */

// lostAttempt is a pod that ended without a summary: the kubelet killed it for
// memory, something sent SIGKILL, or its node went away (§12). Exit 0 with no
// summary is not lost — it finished and had nothing to say.
type lostAttempt struct {
	Pod      string
	Reason   string
	ExitCode *int
	Message  string
}

type observation struct {
	UID          string
	Name         string
	Namespace    string
	Kind         string
	BatchJob     string
	Application  string
	Phase        runPhase
	Trigger      string
	Actor        string
	ScheduledAt  string
	BusinessDate string
	Params       map[string]string
	Created      time.Time
	Started      time.Time
	Finished     time.Time
	// Ran says some attempt got as far as running: a pod Running, or a batch
	// container that terminated.
	Ran         bool
	Summary     *runSummary
	ExitCode    *int
	Reason      string
	Message     string
	Scheduler   string
	Queue       string
	Lost        []lostAttempt
	RunningPods []string
	PodNames    map[string]bool
	Image       string
	// DependsOn is the jobs a dependency run was started after (the observer
	// writes it on the run it creates).
	DependsOn string
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// observe reads one run and its pods.
func observe(run *unstructured.Unstructured, allPods []unstructured.Unstructured) observation {
	labels, annotations := run.GetLabels(), run.GetAnnotations()
	o := observation{
		UID:         string(run.GetUID()),
		Name:        run.GetName(),
		Namespace:   run.GetNamespace(),
		Kind:        runKindOf(run),
		BatchJob:    labels[labelBatchJob],
		Application: labels["adhar.io/application"],
		Phase:       phaseOf(run),
		Actor:       annotations[annotationActor],
		ScheduledAt: annotations[annotationScheduled],
		DependsOn:   annotations[annotationDependsOn],
		Params:      runParams(run),
		Created:     run.GetCreationTimestamp().Time,
		PodNames:    map[string]bool{},
	}
	if c := containersOf(run); len(c) > 0 {
		o.Image, _ = c[0]["image"].(string)
	}
	o.Trigger = labels[labelTrigger]
	if o.Trigger == "" {
		o.Trigger = envOf(run, envTrigger)
	}
	if o.Trigger == "" {
		for _, ref := range run.GetOwnerReferences() {
			if ref.Kind == "CronJob" {
				o.Trigger = TriggerSchedule
			}
		}
		if o.ScheduledAt != "" {
			o.Trigger = TriggerSchedule
		}
	}
	if o.Kind == kindVolcanoJob {
		o.Scheduler = "volcano"
		o.Queue, _, _ = unstructured.NestedString(run.Object, "spec", "queue")
	} else {
		o.Scheduler = "kubernetes"
		for _, tpl := range podTemplatesOf(run) {
			if s, _, _ := unstructured.NestedString(tpl, "spec", "schedulerName"); s == "volcano" {
				o.Scheduler = "volcano"
				o.Queue, _, _ = unstructured.NestedString(tpl, "metadata", "annotations", "scheduling.volcano.sh/queue-name")
			}
		}
	}

	pods := podsOfRun(run, allPods)
	var speaker *unstructured.Unstructured
	var speakerTerm *termination
	for _, p := range pods {
		o.PodNames[p.GetName()] = true
		phase := podPhase(p)
		term := terminationOf(p)
		if phase == "Running" {
			o.Ran = true
			if speaks(p) {
				o.RunningPods = append(o.RunningPods, p.GetName())
			}
		}
		if term != nil {
			o.Ran = true
		}
		if !speaks(p) {
			continue
		}
		var summary *runSummary
		if term != nil {
			summary = parseSummary(term.Message)
		}
		if summary != nil && speaker == nil {
			speaker, speakerTerm = p, term
			o.Summary = summary
		}
		if lost, ok := lostOf(p, phase, term, summary); ok {
			o.Lost = append(o.Lost, lost)
		}
	}
	// Oldest first, so a run's lost attempts are published in the order they died.
	slices.Reverse(o.Lost)

	switch {
	case o.Summary != nil && o.Summary.ExitCode != nil:
		o.ExitCode = o.Summary.ExitCode
	case speakerTerm != nil:
		code := speakerTerm.ExitCode
		o.ExitCode = &code
	default:
		// The newest attempt that terminated says how the run ended when no
		// attempt wrote a summary.
		for _, p := range pods {
			if t := terminationOf(p); t != nil && speaks(p) {
				code := t.ExitCode
				o.ExitCode = &code
				break
			}
		}
	}

	for _, typ := range []string{"Failed", "FailureTarget", "Suspended"} {
		if ok, reason, msg, _ := condition(run, typ); ok {
			o.Reason, o.Message = reason, msg
			break
		}
	}
	if o.Kind == kindVolcanoJob && o.Message == "" {
		o.Reason, _, _ = unstructured.NestedString(run.Object, "status", "state", "reason")
		o.Message, _, _ = unstructured.NestedString(run.Object, "status", "state", "message")
	}
	if o.Summary != nil && o.Summary.Reason != "" {
		o.Reason = o.Summary.Reason
	}
	if o.Summary != nil && o.Summary.Message != "" && o.Message == "" {
		o.Message = o.Summary.Message
	}

	switch {
	case o.Summary != nil && o.Summary.BusinessDate != "":
		o.BusinessDate = o.Summary.BusinessDate
	case o.Params["businessDate"] != "":
		o.BusinessDate = o.Params["businessDate"]
	case o.ScheduledAt != "":
		if at := parseTime(o.ScheduledAt); !at.IsZero() {
			o.BusinessDate = dateInZone(at, envOf(run, envTimeZone))
		}
	}

	if s, _, _ := unstructured.NestedString(run.Object, "status", "startTime"); s != "" {
		o.Started = parseTime(s)
	}
	if o.Started.IsZero() && o.Summary != nil {
		o.Started = parseTime(o.Summary.Start)
	}
	if o.Started.IsZero() && len(pods) > 0 {
		o.Started = pods[len(pods)-1].GetCreationTimestamp().Time
	}
	o.Finished = finishedAt(run, o.Phase, o.Summary)
	return o
}

// lostOf decides whether one pod is a lost attempt. A pod still running is not;
// a pod that exited 0 is not (nothing to say is not lost); a pod that ended with
// a summary is an ordinary failed attempt. Everything else ended without a
// word — and the outcome (OOMKilled and 137, a node's DisruptionTarget) goes in
// the event, because that is the case an operator most needs explained.
func lostOf(pod *unstructured.Unstructured, phase string, term *termination, summary *runSummary) (lostAttempt, bool) {
	if summary != nil {
		return lostAttempt{}, false
	}
	switch {
	case term != nil && term.ExitCode != 0:
		code := term.ExitCode
		return lostAttempt{Pod: pod.GetName(), Reason: firstNonEmpty(term.Reason, "Error"), ExitCode: &code, Message: term.Message}, true
	case term == nil && phase == "Failed":
		// The kubelet never reported a container end: the node was lost, or the
		// pod was evicted before the container started.
		reason, _, _ := unstructured.NestedString(pod.Object, "status", "reason")
		msg, _, _ := unstructured.NestedString(pod.Object, "status", "message")
		conds, _, _ := unstructured.NestedSlice(pod.Object, "status", "conditions")
		for _, c := range conds {
			if m, ok := c.(map[string]interface{}); ok && m["type"] == "DisruptionTarget" && m["status"] == "True" {
				r, _ := m["reason"].(string)
				reason = firstNonEmpty(reason, r)
				if msg == "" {
					msg, _ = m["message"].(string)
				}
			}
		}
		return lostAttempt{Pod: pod.GetName(), Reason: firstNonEmpty(reason, "PodFailed"), Message: msg}, true
	}
	return lostAttempt{}, false
}

func finishedAt(run *unstructured.Unstructured, phase runPhase, summary *runSummary) time.Time {
	if !phase.terminal() {
		return time.Time{}
	}
	if t, _, _ := unstructured.NestedString(run.Object, "status", "completionTime"); t != "" {
		return parseTime(t)
	}
	types := map[runPhase][]string{phaseFailed: {"Failed", "FailureTarget"}, phaseSucceeded: {"Complete", "SuccessCriteriaMet"}}[phase]
	for _, typ := range types {
		if ok, _, _, at := condition(run, typ); ok && at != "" {
			return parseTime(at)
		}
	}
	if at, _, _ := unstructured.NestedString(run.Object, "status", "state", "lastTransitionTime"); at != "" {
		return parseTime(at)
	}
	if summary != nil {
		return parseTime(summary.End)
	}
	return time.Time{}
}

// skippedReason is the kit's `skipped` outcome (D13), or "" for a run that did
// its work. A skipped run is published as run.skipped INSTEAD of run.succeeded:
// the two mean different things to anyone counting what ran.
func skippedReason(o observation) string {
	if o.Summary == nil || o.Summary.Outcome != "skipped" {
		return ""
	}
	return firstNonEmpty(o.Summary.Reason, "skipped")
}

// satisfiesDependency says a succeeded run counts as "succeeded for D" to a job
// that depends on it. A skip because the identity already completed is a
// success for D (some run did the work); a calendar or duplicate skip is not —
// nothing was done for D, and a downstream job must not start on it.
func satisfiesDependency(o observation) bool {
	if o.Phase != phaseSucceeded {
		return false
	}
	switch skippedReason(o) {
	case "":
		return true
	case "already-complete":
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
