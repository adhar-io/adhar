/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package autoscaler

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
)

// Action is what a tick concluded the cluster needs.
type Action string

const (
	// ActionNone means the cluster is the right size (or a guard — cooldown,
	// bound, unsafe candidate — blocked the move).
	ActionNone Action = "None"
	// ActionScaleUp means one worker should be added.
	ActionScaleUp Action = "ScaleUp"
	// ActionScaleDown means the named worker should be drained and removed.
	ActionScaleDown Action = "ScaleDown"
)

const (
	// controlPlaneLabel marks a control-plane node; anything without it is a
	// worker. Adhar's kubeadm bootstrap labels the master with it, and every
	// managed provider does the same.
	controlPlaneLabel = "node-role.kubernetes.io/control-plane"
	// legacyMasterLabel is the pre-1.24 spelling, still applied by some
	// provider images.
	legacyMasterLabel = "node-role.kubernetes.io/master"
	// mirrorPodAnnotation identifies static pods, which the kubelet owns and
	// no eviction can move.
	mirrorPodAnnotation = "kubernetes.io/config.mirror"
)

// capacityShortageMarkers are the scheduler messages that mean "no node has
// room" — the only unschedulable reason a new, identical worker can fix. A
// pending pod blocked by taints, affinity or a missing volume is deliberately
// not a scale-up trigger: buying a node would not schedule it.
var capacityShortageMarkers = []string{
	"insufficient cpu",
	"insufficient memory",
	"insufficient pods",
	"too many pods",
	"insufficient ephemeral-storage",
}

// Snapshot is everything a decision is made from. It is a plain value so the
// policy can be exercised without a cluster.
type Snapshot struct {
	// Spec must already be defaulted (AutoscalingSpec.WithDefaults).
	Spec v1alpha1.AutoscalingSpec
	// Status is the previous status, carrying the cooldown clocks.
	Status v1alpha1.AutoscalingStatus
	Nodes  []corev1.Node
	Pods   []corev1.Pod
	// RWOClaims holds "<namespace>/<claimName>" for every PVC bound to a
	// ReadWriteOnce volume. A node hosting a pod with one is never retired:
	// the volume is attached to that machine and, on most clouds, cannot
	// follow the pod to another node without an orderly detach we do not
	// control here.
	RWOClaims map[string]bool
	Now       time.Time
}

// Utilization is the cluster-wide requested share of worker capacity.
type Utilization struct {
	CPU    float64
	Memory float64
}

// Decision is the outcome of one tick.
type Decision struct {
	Action Action
	// Node is the worker to retire (ActionScaleDown only).
	Node string
	// Reason is a single line explaining the decision, surfaced in status and
	// as an Event.
	Reason string
	// Workers is the observed count of schedulable workers.
	Workers int32
	// UnderutilizedSince carries the scale-down clock forward: nil resets it.
	UnderutilizedSince *time.Time
	Utilization        Utilization
	// PendingPods is how many pending pods were attributed to capacity
	// shortage (diagnostics only).
	PendingPods int
}

// Decide applies the autoscaling policy to a snapshot. It is pure: no cluster
// or cloud calls, so the rules below are exactly what the tests assert.
//
// Order matters — scale-up is evaluated first and wins. A cluster that is
// simultaneously "underutilized on average" and unable to schedule a pod has a
// fragmentation problem, and removing a node would make it worse.
func Decide(s Snapshot) Decision {
	spec := s.Spec
	workers := schedulableWorkers(s.Nodes)
	util := utilization(workers, s.Pods)
	d := Decision{
		Action:      ActionNone,
		Workers:     int32(len(workers)),
		Utilization: util,
	}

	// --- Scale up -----------------------------------------------------------
	pending := pendingForCapacity(s.Pods, workers)
	d.PendingPods = len(pending)
	if len(pending) > 0 {
		// Any pending pod resets the idle clock: the cluster is not idle.
		switch {
		case int32(len(workers)) >= spec.MaxWorkers:
			d.Reason = fmt.Sprintf("%d pod(s) pending for capacity but maxWorkers=%d reached", len(pending), spec.MaxWorkers)
			return d
		case !cooledDown(s.Status.LastScaleUp, s.Now, spec.ScaleUpCooldown.Duration):
			d.Reason = fmt.Sprintf("%d pod(s) pending for capacity; waiting out the %s scale-up cooldown", len(pending), spec.ScaleUpCooldown.Duration)
			return d
		default:
			d.Action = ActionScaleUp
			d.Reason = fmt.Sprintf("%d pod(s) unschedulable for capacity (e.g. %s); adding a worker (%d/%d)",
				len(pending), pending[0], len(workers)+1, spec.MaxWorkers)
			return d
		}
	}

	// --- Scale down ---------------------------------------------------------
	threshold := spec.ScaleDownThreshold()
	if util.CPU >= threshold || util.Memory >= threshold {
		// Busy enough: forget any accumulated idle time.
		d.Reason = fmt.Sprintf("utilization cpu=%.0f%% memory=%.0f%% at or above the %.0f%% scale-down threshold",
			util.CPU*100, util.Memory*100, threshold*100)
		return d
	}

	// Under the threshold: start or continue the idle clock.
	since := s.Now
	if s.Status.UnderutilizedSince != nil {
		since = s.Status.UnderutilizedSince.Time
	}
	d.UnderutilizedSince = &since

	if int32(len(workers)) <= spec.MinWorkers {
		d.Reason = fmt.Sprintf("cluster idle (cpu=%.0f%% memory=%.0f%%) but minWorkers=%d reached", util.CPU*100, util.Memory*100, spec.MinWorkers)
		return d
	}
	if idle := s.Now.Sub(since); idle < spec.ScaleDownDelay.Duration {
		d.Reason = fmt.Sprintf("cluster idle for %s of the required %s", idle.Round(time.Second), spec.ScaleDownDelay.Duration)
		return d
	}
	// A node added moments ago must not be removed by the next idle tick, and
	// removals are spaced by the same delay so the cluster settles between
	// them (one node per tick, at most one per delay window).
	if !cooledDown(s.Status.LastScaleUp, s.Now, spec.ScaleDownDelay.Duration) {
		d.Reason = "cluster idle but a worker was added recently; holding"
		return d
	}
	if !cooledDown(s.Status.LastScaleDown, s.Now, spec.ScaleDownDelay.Duration) {
		d.Reason = "cluster idle but a worker was removed recently; holding"
		return d
	}

	candidate, why := scaleDownCandidate(workers, s.Pods, s.RWOClaims)
	if candidate == "" {
		d.Reason = "cluster idle but no worker is safe to drain: " + why
		return d
	}
	d.Action = ActionScaleDown
	d.Node = candidate
	d.Reason = fmt.Sprintf("cluster idle for %s (cpu=%.0f%% memory=%.0f%%); removing %s (%d→%d workers)",
		s.Now.Sub(since).Round(time.Second), util.CPU*100, util.Memory*100, candidate, len(workers), len(workers)-1)
	return d
}

// cooledDown reports whether `d` has elapsed since the (optional) timestamp.
// A never-happened event is always cooled down.
func cooledDown(last *metav1.Time, now time.Time, d time.Duration) bool {
	if last == nil {
		return true
	}
	return now.Sub(last.Time) >= d
}

// IsWorker reports whether a node is a schedulable worker: not a control
// plane, not cordoned, not being deleted, and registered as Ready. Cordoned
// nodes are excluded because the platform (or an operator) has already taken
// them out of service — counting them would hide a real capacity shortage.
func IsWorker(n corev1.Node) bool {
	if _, ok := n.Labels[controlPlaneLabel]; ok {
		return false
	}
	if _, ok := n.Labels[legacyMasterLabel]; ok {
		return false
	}
	if n.Spec.Unschedulable || n.DeletionTimestamp != nil {
		return false
	}
	return true
}

func schedulableWorkers(nodes []corev1.Node) []corev1.Node {
	out := make([]corev1.Node, 0, len(nodes))
	for _, n := range nodes {
		if IsWorker(n) {
			out = append(out, n)
		}
	}
	// Deterministic order so the candidate choice is stable between ticks when
	// two nodes are equally empty.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// utilization sums pod requests placed on worker nodes over their allocatable
// capacity. Requests (not usage) are what the scheduler reserves, so they are
// what decides whether the next pod fits.
func utilization(workers []corev1.Node, pods []corev1.Pod) Utilization {
	onWorker := map[string]bool{}
	var allocCPU, allocMem int64
	for _, n := range workers {
		onWorker[n.Name] = true
		allocCPU += n.Status.Allocatable.Cpu().MilliValue()
		allocMem += n.Status.Allocatable.Memory().Value()
	}
	if allocCPU == 0 || allocMem == 0 {
		return Utilization{}
	}
	var reqCPU, reqMem int64
	for _, p := range pods {
		if !onWorker[p.Spec.NodeName] || isTerminated(p) {
			continue
		}
		cpu, mem := podRequests(p)
		reqCPU += cpu
		reqMem += mem
	}
	return Utilization{
		CPU:    float64(reqCPU) / float64(allocCPU),
		Memory: float64(reqMem) / float64(allocMem),
	}
}

// pendingForCapacity returns the names ("<ns>/<name>") of pods the scheduler
// rejected for lack of room and that a new, identical worker could host.
func pendingForCapacity(pods []corev1.Pod, workers []corev1.Node) []string {
	var out []string
	for _, p := range pods {
		if p.Status.Phase != corev1.PodPending || p.Spec.NodeName != "" || p.DeletionTimestamp != nil {
			continue
		}
		msg, ok := unschedulableMessage(p)
		if !ok || !mentionsCapacityShortage(msg) {
			continue
		}
		// A pod pinned to labels no current worker carries will not fit the
		// next worker either — new nodes join the group with the same labels.
		if !placeableOnNewWorker(p, workers) {
			continue
		}
		// A pod larger than a whole worker is never schedulable by adding one
		// more of the same size; scaling up would burn money for nothing.
		if !fitsAnyWorkerShape(p, workers) {
			continue
		}
		out = append(out, p.Namespace+"/"+p.Name)
	}
	sort.Strings(out)
	return out
}

// unschedulableMessage extracts the scheduler's explanation from the
// PodScheduled=False/Unschedulable condition.
func unschedulableMessage(p corev1.Pod) (string, bool) {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c.Message, true
		}
	}
	return "", false
}

func mentionsCapacityShortage(msg string) bool {
	l := strings.ToLower(msg)
	for _, m := range capacityShortageMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// placeableOnNewWorker checks the pod's node selection constraints against the
// existing workers: a new worker is a clone of them, so a constraint no
// current worker satisfies cannot be satisfied by adding one.
func placeableOnNewWorker(p corev1.Pod, workers []corev1.Node) bool {
	required := map[string][]string{}
	for k, v := range p.Spec.NodeSelector {
		required[k] = append(required[k], v)
	}
	if p.Spec.Affinity != nil && p.Spec.Affinity.NodeAffinity != nil &&
		p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		for _, term := range p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			for _, expr := range term.MatchExpressions {
				switch expr.Operator {
				case corev1.NodeSelectorOpIn:
					required[expr.Key] = append(required[expr.Key], expr.Values...)
				case corev1.NodeSelectorOpExists:
					required[expr.Key] = append(required[expr.Key], "")
				}
			}
		}
	}
	if len(required) == 0 {
		return true
	}
	for key, values := range required {
		satisfied := false
		for _, n := range workers {
			got, ok := n.Labels[key]
			if !ok {
				continue
			}
			for _, want := range values {
				if want == "" || want == got {
					satisfied = true
					break
				}
			}
			if satisfied {
				break
			}
		}
		if !satisfied {
			return false
		}
	}
	return true
}

// fitsAnyWorkerShape reports whether the pod's requests fit inside the largest
// worker's allocatable capacity. With no workers at all we cannot tell, so we
// allow the scale-up (that is how a cluster grows its first worker back).
func fitsAnyWorkerShape(p corev1.Pod, workers []corev1.Node) bool {
	if len(workers) == 0 {
		return true
	}
	cpu, mem := podRequests(p)
	for _, n := range workers {
		if n.Status.Allocatable.Cpu().MilliValue() >= cpu && n.Status.Allocatable.Memory().Value() >= mem {
			return true
		}
	}
	return false
}

// scaleDownCandidate picks the emptiest worker that is safe to drain, or ""
// plus the reason nothing qualified.
func scaleDownCandidate(workers []corev1.Node, pods []corev1.Pod, rwoClaims map[string]bool) (string, string) {
	byNode := map[string][]corev1.Pod{}
	for _, p := range pods {
		if p.Spec.NodeName == "" || isTerminated(p) {
			continue
		}
		byNode[p.Spec.NodeName] = append(byNode[p.Spec.NodeName], p)
	}

	type scored struct {
		name  string
		score float64
	}
	ranked := make([]scored, 0, len(workers))
	for _, n := range workers {
		allocCPU := n.Status.Allocatable.Cpu().MilliValue()
		allocMem := n.Status.Allocatable.Memory().Value()
		var cpu, mem int64
		for _, p := range byNode[n.Name] {
			// DaemonSet pods follow the node — they are recreated everywhere
			// and never need to be rescheduled, so they do not make a node
			// "busy" for retirement purposes.
			if isDaemonSetPod(p) {
				continue
			}
			c, m := podRequests(p)
			cpu += c
			mem += m
		}
		score := 0.0
		if allocCPU > 0 {
			score += float64(cpu) / float64(allocCPU)
		}
		if allocMem > 0 {
			score += float64(mem) / float64(allocMem)
		}
		ranked = append(ranked, scored{name: n.Name, score: score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].name < ranked[j].name
		}
		return ranked[i].score < ranked[j].score
	})

	blocked := ""
	for _, c := range ranked {
		if why := unsafeToDrain(byNode[c.name], rwoClaims); why != "" {
			if blocked == "" {
				blocked = c.name + ": " + why
			}
			continue
		}
		return c.name, ""
	}
	if blocked == "" {
		blocked = "no worker nodes"
	}
	return "", blocked
}

// unsafeToDrain returns why a node must not be retired, or "" when it may be.
func unsafeToDrain(pods []corev1.Pod, rwoClaims map[string]bool) string {
	for _, p := range pods {
		if isDaemonSetPod(p) || isMirrorPod(p) {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			if rwoClaims[p.Namespace+"/"+v.PersistentVolumeClaim.ClaimName] {
				return fmt.Sprintf("pod %s/%s holds ReadWriteOnce volume %s", p.Namespace, p.Name, v.PersistentVolumeClaim.ClaimName)
			}
		}
		// A pod with no controller has nothing to recreate it elsewhere;
		// evicting it destroys it. Same rule the upstream cluster-autoscaler
		// applies by default.
		if len(p.OwnerReferences) == 0 {
			return fmt.Sprintf("pod %s/%s is not managed by a controller", p.Namespace, p.Name)
		}
	}
	return ""
}

func isDaemonSetPod(p corev1.Pod) bool {
	for _, o := range p.OwnerReferences {
		if o.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func isMirrorPod(p corev1.Pod) bool {
	_, ok := p.Annotations[mirrorPodAnnotation]
	return ok
}

func isTerminated(p corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// podRequests returns the pod's scheduled CPU (milli) and memory (bytes)
// requests: the sum over regular containers, floored by the largest init
// container, which is how the scheduler computes a pod's footprint.
func podRequests(p corev1.Pod) (int64, int64) {
	var cpu, mem int64
	for _, c := range p.Spec.Containers {
		cpu += quantity(c.Resources.Requests, corev1.ResourceCPU).MilliValue()
		mem += quantity(c.Resources.Requests, corev1.ResourceMemory).Value()
	}
	for _, c := range p.Spec.InitContainers {
		if v := quantity(c.Resources.Requests, corev1.ResourceCPU).MilliValue(); v > cpu {
			cpu = v
		}
		if v := quantity(c.Resources.Requests, corev1.ResourceMemory).Value(); v > mem {
			mem = v
		}
	}
	return cpu, mem
}

func quantity(list corev1.ResourceList, name corev1.ResourceName) *resource.Quantity {
	if q, ok := list[name]; ok {
		return &q
	}
	return &resource.Quantity{}
}
