/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// gang.go is what Volcano adds to a run (design 0025 §4.5, Phase 2): the queue
// it waits in and its gang — how many pods must start together and how many
// have. A partitioned run is a Volcano Job whose PodGroup is all-or-nothing; a
// single run under Volcano is a plain Job whose pods name schedulerName
// volcano, and Volcano gives it a PodGroup of its own. Everything here is
// ABSENT for a run kube-scheduler places, so a cluster without Volcano reads
// and prints exactly what it did before Phase 2.

const (
	// annotationQueueName is the pod annotation the queue binding mutates (§4.3).
	annotationQueueName = "scheduling.volcano.sh/queue-name"
	// labelVolcanoTask is the task a Volcano Job pod belongs to (`manager` or
	// `worker` in §4.5's shape); Volcano stamps it on every pod it creates.
	labelVolcanoTask = "volcano.sh/task-spec"
	schedulerVolcano = "volcano"
)

// gangView is a run's gang as Volcano reports it. The counts are the Volcano
// Job's own (per pod, across tasks) and, for a plain Job, its PodGroup's.
type gangView struct {
	MinMember int64 `json:"minMember" yaml:"minMember"`
	Running   int64 `json:"running" yaml:"running"`
	Pending   int64 `json:"pending,omitempty" yaml:"pending,omitempty"`
	Succeeded int64 `json:"succeeded,omitempty" yaml:"succeeded,omitempty"`
	Failed    int64 `json:"failed,omitempty" yaml:"failed,omitempty"`
	// Phase is the PodGroup's: Pending (the queue has not admitted it),
	// Inqueue (admitted, pods being created), Running, Completed, Unknown.
	Phase string `json:"phase,omitempty" yaml:"phase,omitempty"`
	// Message is why the gang cannot start, from the PodGroup's Unschedulable condition.
	Message  string `json:"message,omitempty" yaml:"message,omitempty"`
	PodGroup string `json:"podGroup,omitempty" yaml:"podGroup,omitempty"`
}

// volcanoScheduled says Volcano places this run's pods: it is a Volcano Job, or
// a Job whose pod template names the volcano scheduler.
func volcanoScheduled(run *unstructured.Unstructured) bool {
	if runKindOf(run) == "VolcanoJob" {
		return true
	}
	for _, tpl := range podTemplatesOf(run) {
		if s, _, _ := unstructured.NestedString(tpl, "spec", "schedulerName"); s == schedulerVolcano {
			return true
		}
	}
	return false
}

// queueOfRun is the queue the run was admitted to: spec.queue of a Volcano
// Job, else the annotation on its pod template.
func queueOfRun(run *unstructured.Unstructured) string {
	if q, _, _ := unstructured.NestedString(run.Object, "spec", "queue"); q != "" {
		return q
	}
	for _, tpl := range podTemplatesOf(run) {
		if q, _, _ := unstructured.NestedString(tpl, "metadata", "annotations", annotationQueueName); q != "" {
			return q
		}
	}
	return ""
}

// gangOfRun is what the run object itself says. A Volcano Job carries its
// gang size (minAvailable, which Volcano defaults to every replica) and its pod
// counts; a plain Job has no gang until its PodGroup is read.
func gangOfRun(run *unstructured.Unstructured) *gangView {
	if runKindOf(run) != "VolcanoJob" {
		return nil
	}
	g := &gangView{}
	if n, ok, _ := unstructured.NestedInt64(run.Object, "spec", "minAvailable"); ok {
		g.MinMember = n
	} else {
		tasks, _, _ := unstructured.NestedSlice(run.Object, "spec", "tasks")
		for _, t := range tasks {
			if m, ok := t.(map[string]interface{}); ok {
				r, _, _ := unstructured.NestedInt64(m, "replicas")
				g.MinMember += r
			}
		}
	}
	g.Running = statusInt(run, "running")
	g.Pending = statusInt(run, "pending")
	g.Succeeded = statusInt(run, "succeeded")
	g.Failed = statusInt(run, "failed")
	return g
}

// podGroupOf finds the run's PodGroup: the one it owns (Volcano names a Volcano
// Job's `<job>-<uid>` and a plain Job's `podgroup-<uid>`, and both carry the
// run as owner), else one of those names.
func podGroupOf(run *unstructured.Unstructured, groups []unstructured.Unstructured) *unstructured.Unstructured {
	uid := string(run.GetUID())
	for i := range groups {
		pg := &groups[i]
		if pg.GetNamespace() != run.GetNamespace() {
			continue
		}
		for _, o := range pg.GetOwnerReferences() {
			if uid != "" && string(o.UID) == uid {
				return pg
			}
		}
	}
	names := map[string]bool{run.GetName(): true}
	if uid != "" {
		names[run.GetName()+"-"+uid] = true
		names["podgroup-"+uid] = true
	}
	for i := range groups {
		if pg := &groups[i]; pg.GetNamespace() == run.GetNamespace() && names[pg.GetName()] {
			return pg
		}
	}
	return nil
}

// withPodGroup completes a run's gang from its PodGroup: the phase, the reason
// it waits, and — for a plain Job, whose own status knows no gang — the size
// and counts. The Unschedulable message is kept only while the gang has not
// started: a condition left over from the wait would read as a current problem.
func withPodGroup(g *gangView, pg *unstructured.Unstructured) *gangView {
	if pg == nil {
		return g
	}
	if g == nil {
		g = &gangView{}
		g.Running = statusInt(pg, "running")
		g.Succeeded = statusInt(pg, "succeeded")
		g.Failed = statusInt(pg, "failed")
	}
	if g.MinMember == 0 {
		g.MinMember, _, _ = unstructured.NestedInt64(pg.Object, "spec", "minMember")
	}
	g.PodGroup = pg.GetName()
	g.Phase, _, _ = unstructured.NestedString(pg.Object, "status", "phase")
	if g.Phase == "Pending" || g.Phase == "Inqueue" || g.Phase == "" {
		newest := ""
		for _, c := range conditionsOfAt(pg) {
			if c.Type == "Unschedulable" && c.Status == "True" && c.at >= newest {
				g.Message, newest = c.Message, c.at
			}
		}
	}
	return g
}

type timedCondition struct {
	condition
	at string
}

func conditionsOfAt(obj *unstructured.Unstructured) []timedCondition {
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	var out []timedCondition
	for _, c := range raw {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		var tc timedCondition
		tc.Type, _ = m["type"].(string)
		tc.Status, _ = m["status"].(string)
		tc.Reason, _ = m["reason"].(string)
		tc.Message, _ = m["message"].(string)
		tc.at, _ = m["lastTransitionTime"].(string)
		out = append(out, tc)
	}
	return out
}

// attachGangs reads the PodGroups of the namespace ("" for all) ONLY when a
// row is Volcano-scheduled, and completes those rows. A cluster without
// Volcano never makes the call; a PodGroup list that fails (no permission, the
// API going away) leaves what the run objects said.
func attachGangs(c context.Context, dc dynamic.Interface, namespace string, rows []runRow) {
	any := false
	for _, r := range rows {
		if r.Scheduler == schedulerVolcano {
			any = true
			break
		}
	}
	if !any {
		return
	}
	list, err := dc.Resource(podGroupGVR).Namespace(namespace).List(c, metav1.ListOptions{})
	if err != nil {
		return
	}
	applyPodGroups(rows, list.Items)
}

// applyPodGroups is attachGangs without the fetch.
func applyPodGroups(rows []runRow, groups []unstructured.Unstructured) {
	for i := range rows {
		r := &rows[i]
		if r.Scheduler != schedulerVolcano || r.obj == nil {
			continue
		}
		pg := podGroupOf(r.obj, groups)
		if pg == nil {
			continue
		}
		r.Gang = withPodGroup(r.Gang, pg)
		if r.Queue == "" {
			r.Queue, _, _ = unstructured.NestedString(pg.Object, "spec", "queue")
		}
		if r.Phase == phasePending && r.Message == "" && r.Gang.Message != "" {
			r.Message = gangWaitMessage(r)
		}
	}
}

// gangWaitMessage says why a pending run has not started, in the queue's words.
func gangWaitMessage(r *runRow) string {
	where := "its queue"
	if r.Queue != "" {
		where = "queue " + r.Queue
	}
	if r.Gang.MinMember > 1 {
		return fmt.Sprintf("gang of %d waiting in %s: %s", r.Gang.MinMember, where, r.Gang.Message)
	}
	return fmt.Sprintf("waiting in %s: %s", where, r.Gang.Message)
}

// gangCell is the gang in a table: its PodGroup phase, then pods started of
// the gang size.
func gangCell(g *gangView) string {
	if g == nil {
		return "—"
	}
	phase := strings.ToLower(g.Phase)
	if g.MinMember == 0 {
		return dash(phase)
	}
	started := g.Running + g.Succeeded + g.Failed
	if phase == "" {
		return fmt.Sprintf("%d/%d", started, g.MinMember)
	}
	return fmt.Sprintf("%s %d/%d", phase, started, g.MinMember)
}

/* ─────────────────────────── task roles (§4.5) ─────────────────────────── */

// podRole is a pod's role in its run: the adhar.io/batch-role label the
// composition writes, else the Volcano task it belongs to. A Volcano Job
// numbers each task from 0, so worker-0 also has task index 0 — without the
// task it would read as a manager.
func podRole(p *unstructured.Unstructured) string {
	if r := podLabel(p, labelRole); r != "" {
		return r
	}
	return podLabel(p, labelVolcanoTask)
}

// podIndex is a pod's index: the completion index of an Indexed Job, else
// Volcano's task index (a label on current releases, an annotation on all).
func podIndex(p *unstructured.Unstructured) string {
	if i := podLabel(p, labelNativeIndex); i != "" {
		return i
	}
	if i := podLabel(p, labelVolcanoIndex); i != "" {
		return i
	}
	return p.GetAnnotations()[labelVolcanoIndex]
}
