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
	"math"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
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
	// safeToEvictAnnotation is upstream cluster-autoscaler's convention for a pod
	// that must not be evicted to retire a node. The BatchJob composition sets it
	// to "false" on every run pod unless `disruption: allow` (design 0025 §4.7).
	safeToEvictAnnotation = "cluster-autoscaler.kubernetes.io/safe-to-evict"
	// workloadLabel names what a pod is; `batch` marks a BatchJob's run pods.
	workloadLabel = "adhar.io/workload"
	// scaleUpLabel opts a batch run into buying nodes. The composition sets it
	// only for `scaleUp: true`, and Kyverno permits that only in prod namespaces.
	scaleUpLabel = "adhar.io/scale-up"
	// environmentLabel is the hierarchy label every composed object carries.
	environmentLabel = "adhar.io/environment"
	// volcanoSchedulerName is the schedulerName a Volcano-scheduled pod names.
	// A Volcano Job's member's demand is read from its PodGroup, never from the
	// pod (design 0025 §4.7); see pendingForCapacity for the single-mode run.
	volcanoSchedulerName = "volcano"
	// volcanoJobAPIGroup is the group of Volcano's Job (vcjob), the controller of
	// every gang member's pod and of the PodGroup Volcano creates for it.
	volcanoJobAPIGroup = "batch.volcano.sh"
	// volcanoGroupNameAnnotation names the PodGroup a Volcano pod belongs to; the
	// job controller and the PodGroup controller both stamp it.
	volcanoGroupNameAnnotation = "scheduling.k8s.io/group-name"
	// volcanoQueueAnnotation is the pod's queue, which adhar-batch-queue-binding
	// mutates to the namespace's `<org>-<team>-<env>` leaf (§4.3).
	volcanoQueueAnnotation = "scheduling.volcano.sh/queue-name"
)

// capacityShortageMarkers are the scheduler messages that mean "no node has
// room" — the only unschedulable reason a new, identical worker can fix. A
// pending pod blocked by taints or by affinity no current worker satisfies is
// deliberately not a scale-up trigger: buying a node would not schedule it.
//
// "exceed max volume count" belongs here even though it names a volume. It is
// the CSI attach-limit predicate: the node is out of block-device slots, not
// the volume out of zones. On DigitalOcean that limit is 7 volumes per droplet
// and it is the FIRST capacity wall the platform hits — a full catalogue runs
// out of attachment slots long before it runs out of CPU or memory, and a
// fresh droplet brings 7 more slots, so another worker is exactly the fix.
// (An unbound or zone-pinned volume reads as "had volume node affinity
// conflict" / "didn't find available persistent volumes", which are NOT here.)
// volumeLimitMarker is the one capacity marker whose remedy is arithmetic: each
// new worker brings exactly VolumesPerNode more attachment slots.
const volumeLimitMarker = "exceed max volume count"

var capacityShortageMarkers = []string{
	"insufficient cpu",
	"insufficient memory",
	"insufficient pods",
	"too many pods",
	"insufficient ephemeral-storage",
	"exceed max volume count",
}

// antiAffinityMarker is the scheduler's wording when a pod cannot be placed
// because another member of its own group already occupies every candidate.
//
// It is NOT in capacityShortageMarkers, because whether a new worker fixes it
// depends on the topology key: a fresh host satisfies hostname anti-affinity
// (it hosts none of the group yet) and does nothing at all for zone
// anti-affinity, since new workers join the existing zones. So the marker is
// paired with hostAntiAffinityOnly below.
//
// Without it an HA workload sits Pending forever on an under-sized cluster with
// the autoscaler idle and room to grow. Measured on a live Civo bring-up
// (2026-10-06): production mode installs Argo CD HA, whose Redis wants three
// replicas with REQUIRED anti-affinity on kubernetes.io/hostname, the shipped
// config provisions two workers, and the control plane is tainted — so
// argo-cd-redis-ha-server-2 and its haproxy stayed unschedulable with
// maxWorkers=5 and nothing to stop the autoscaler adding the third host.
const antiAffinityMarker = "didn't match pod anti-affinity rules"

// hostAntiAffinityOnly reports whether every REQUIRED pod anti-affinity term
// spreads over kubernetes.io/hostname. Only then is "add a host" the remedy.
func hostAntiAffinityOnly(p corev1.Pod) bool {
	if p.Spec.Affinity == nil || p.Spec.Affinity.PodAntiAffinity == nil {
		return false
	}
	terms := p.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if len(terms) == 0 {
		return false
	}
	for _, t := range terms {
		if t.TopologyKey != corev1.LabelHostname {
			return false
		}
	}
	return true
}

// affinityAssistantPinGrace is how long a pod held next to a Tekton affinity
// assistant may wait before it counts as demand for a new worker.
//
// Every TaskRun that mounts a CI run's ReadWriteOnce workspace carries a
// REQUIRED pod affinity to the run's affinity assistant, so the scheduler
// reports it as "1 Insufficient memory, N node(s) didn't match pod affinity
// rules" when the assistant's node is full. "Insufficient memory" reads as
// capacity, but a new worker hosts no assistant and the pod cannot go there:
// counting it bought nodes nothing could use (the DigitalOcean CI runs of
// 2026-11-01 sat pinned while workers were added). The tekton package's
// affinity-assistant relocator moves the assistant to an existing node with
// room within about two minutes, and the pinned pods follow. A pod still
// waiting after this grace means no node had room — and then a new worker is
// exactly where the relocator moves the assistant next.
const affinityAssistantPinGrace = 4 * time.Minute

// tektonAffinityAssistantLabel is the component label Tekton puts on every
// affinity assistant and names in the pod affinity of the pods it pins.
const tektonAffinityAssistantLabel = "app.kubernetes.io/component"

// pinnedToAffinityAssistant reports whether the pod is REQUIRED to share a node
// with a Tekton affinity assistant.
func pinnedToAffinityAssistant(p corev1.Pod) bool {
	if p.Spec.Affinity == nil || p.Spec.Affinity.PodAffinity == nil {
		return false
	}
	for _, t := range p.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if t.LabelSelector != nil && t.LabelSelector.MatchLabels[tektonAffinityAssistantLabel] == "affinity-assistant" {
			return true
		}
	}
	return false
}

// pinnedToNamedNodes reports whether every required node-selector term of the
// pod names its node by metadata.name — a DaemonSet pod, or an affinity
// assistant the relocator pinned to the nodes with room. A node that does not
// exist yet has a name none of them lists, so no scale-up can place the pod.
func pinnedToNamedNodes(p corev1.Pod) bool {
	if p.Spec.Affinity == nil || p.Spec.Affinity.NodeAffinity == nil ||
		p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return false
	}
	terms := p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) == 0 {
		return false
	}
	for _, t := range terms {
		named := false
		for _, f := range t.MatchFields {
			if f.Key == "metadata.name" && f.Operator == corev1.NodeSelectorOpIn {
				named = true
			}
		}
		if !named {
			return false
		}
	}
	return true
}

// unschedulableSince is when the scheduler first reported the pod
// unschedulable; zero when it has not (or did not say when).
func unschedulableSince(p corev1.Pod) time.Time {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
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
	// VolumesPerNode is the CSI attach limit a single worker advertises
	// (CSINode.spec.drivers[].allocatable.count) — 7 on DigitalOcean. It is how
	// many pending volume-limited pods one new worker can actually absorb, so
	// it sizes a scale-up burst. Zero means "unknown": the burst then falls
	// back to one worker at a time.
	VolumesPerNode int
	// NodeCPUUsageMilli is each node's MEASURED CPU use (kubelet stats), when it
	// could be read. Requests understate what the platform's pods use while they
	// boot, so utilization takes the larger of the two. See usage.go.
	NodeCPUUsageMilli map[string]int64
	// Gangs are the cluster's Volcano PodGroups and Queues the gangs' queues
	// (and their ancestors), by name. Both are empty on a cluster without
	// Volcano, which then decides exactly as it always has. See gangWantsNodes.
	Gangs  []Gang
	Queues map[string]GangQueue
	Now    time.Time
}

// Utilization is the cluster-wide requested share of worker capacity.
type Utilization struct {
	CPU    float64
	Memory float64
	// MeasuredCPU is the fleet's actual CPU use over allocatable, when the
	// kubelets could be read; CPU is the larger of requests and this.
	MeasuredCPU float64
	Measured    bool
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
	// ScaleUpBy is how many workers to add when Action is ScaleUp. It is at
	// least 1 and never takes the cluster past MaxWorkers.
	//
	// Adding one worker per tick, behind a cooldown, is far too slow when the
	// whole catalogue lands at once: the first capacity wall is the CSI attach
	// limit, and clearing ~50 pending volumes one 7-slot worker at a time costs
	// half an hour of cooldowns during which pods stay Pending and their
	// Applications stay Degraded.
	ScaleUpBy int32
	// UnderutilizedSince carries the scale-down clock forward: nil resets it.
	UnderutilizedSince *time.Time
	Utilization        Utilization
	// PendingPods is how many pending pods were attributed to capacity
	// shortage (diagnostics only).
	PendingPods int
	// PendingGangs is how many Volcano gangs this decision buys workers for
	// (diagnostics only).
	PendingGangs int
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
	if measured, ok := measuredUtilization(workers, s.NodeCPUUsageMilli); ok {
		util.MeasuredCPU, util.Measured = measured, true
		if measured > util.CPU {
			util.CPU = measured
		}
	}
	d := Decision{
		Action:      ActionNone,
		Workers:     int32(len(workers)),
		Utilization: util,
	}

	// --- Scale up -----------------------------------------------------------
	pending := volcanoQueuesAdmit(s, pendingForCapacity(s.Pods, workers, s.Now))
	d.PendingPods = len(pending)
	// A Volcano gang held at enqueue has no pods at all, so the line above
	// cannot see it (design 0025 R3); its demand is its PodGroup's.
	gangs := planGangs(s, spec.MaxWorkers-int32(len(workers)))
	d.PendingGangs = len(gangs.served)
	if len(pending) > 0 || len(gangs.served) > 0 {
		// Any pending pod resets the idle clock: the cluster is not idle.
		switch {
		case int32(len(workers)) >= spec.MaxWorkers:
			d.Reason = fmt.Sprintf("%d pod(s) pending for capacity but maxWorkers=%d reached", len(pending), spec.MaxWorkers)
			return d
		case !cooledDown(s.Status.LastScaleUp, s.Now, spec.ScaleUpCooldown.Duration):
			d.Reason = fmt.Sprintf("%d pod(s) pending for capacity; waiting out the %s scale-up cooldown", len(pending), spec.ScaleUpCooldown.Duration)
			if len(gangs.served) > 0 {
				d.Reason = fmt.Sprintf("%d pod(s) and %d gang(s) pending for capacity; waiting out the %s scale-up cooldown",
					len(pending), len(gangs.served), spec.ScaleUpCooldown.Duration)
			}
			return d
		case len(gangs.served) == 0:
			d.Action = ActionScaleUp
			d.ScaleUpBy = scaleUpBurst(s, pending, int32(len(workers)), spec.MaxWorkers)
			if d.ScaleUpBy == 1 {
				d.Reason = fmt.Sprintf("%d pod(s) unschedulable for capacity (e.g. %s); adding a worker (%d/%d)",
					len(pending), pending[0], len(workers)+1, spec.MaxWorkers)
			} else {
				d.Reason = fmt.Sprintf("%d pod(s) unschedulable for capacity (e.g. %s); adding %d workers (%d/%d)",
					len(pending), pending[0], d.ScaleUpBy, int32(len(workers))+d.ScaleUpBy, spec.MaxWorkers)
			}
			return d
		default:
			// The gangs' count is exact and comes first: a gang given part of what
			// it needs schedules nothing (all-or-nothing) and holds the nodes idle.
			// Pending pods take what room is left, sized as they always are.
			d.Action = ActionScaleUp
			d.ScaleUpBy = gangs.nodes
			if after := int32(len(workers)) + gangs.nodes; len(pending) > 0 && after < spec.MaxWorkers {
				d.ScaleUpBy += scaleUpBurst(s, pending, after, spec.MaxWorkers)
			}
			d.Reason = fmt.Sprintf("%d gang(s) unschedulable for capacity (e.g. %s, %d worker(s)) and %d pod(s); adding %d worker(s) (%d/%d)",
				len(gangs.served), gangs.served[0], gangs.nodes, len(pending), d.ScaleUpBy, int32(len(workers))+d.ScaleUpBy, spec.MaxWorkers)
			return d
		}
	}
	// An opted-in gang that needs more workers than maxWorkers leaves room for
	// is not bought a part of its gang, and the cluster does not shrink under it
	// either: the same hold a pending pod at maxWorkers gets.
	if gangs.refused != "" {
		d.Reason = gangs.refused
		return d
	}

	// --- Scale up before anything becomes unschedulable ---------------------
	//
	// Pending pods (above) are the backstop, not the goal. By the time a pod
	// cannot be placed, work has already stopped and it must wait out a node
	// create — minutes on every cloud. Adding a worker while the cluster is merely
	// busy keeps that from happening at all.
	//
	// Either dimension triggers: a cluster at 95% CPU and 40% memory cannot place
	// another CPU-bound pod, and the platform's catalogue is CPU-bound.
	//
	// Batch that may not buy nodes does not count here either: a queue of dev runs
	// filling a cluster would otherwise do through this door exactly what
	// pendingForCapacity refuses at the other one. It still counts below, because
	// it is real load and the question there is whether a node can go.
	up := scaleUpUtilization(workers, s.Pods, util)
	if upThreshold := spec.ScaleUpThreshold(); up.CPU >= upThreshold || up.Memory >= upThreshold {
		switch {
		case int32(len(workers)) >= spec.MaxWorkers:
			d.Reason = fmt.Sprintf("utilization cpu=%.0f%% memory=%.0f%% at or above the %.0f%% scale-up threshold but maxWorkers=%d reached",
				up.CPU*100, up.Memory*100, upThreshold*100, spec.MaxWorkers)
			return d
		case !cooledDown(s.Status.LastScaleUp, s.Now, spec.ScaleUpCooldown.Duration):
			d.Reason = fmt.Sprintf("utilization cpu=%.0f%% memory=%.0f%% at or above the %.0f%% scale-up threshold; waiting out the %s cooldown",
				up.CPU*100, up.Memory*100, upThreshold*100, spec.ScaleUpCooldown.Duration)
			return d
		default:
			d.Action = ActionScaleUp
			d.ScaleUpBy = 1
			if up.Measured && up.MeasuredCPU >= upThreshold {
				// A fleet whose every core is busy cannot say how much more it
				// needs — usage is capped at capacity. Growing by half is the
				// smallest step that gets a starved bring-up moving in one
				// cooldown instead of four.
				d.ScaleUpBy = saturationBurst(int32(len(workers)), spec.MaxWorkers)
			}
			d.Reason = fmt.Sprintf("utilization cpu=%.0f%% (measured %.0f%%) memory=%.0f%% at or above the %.0f%% scale-up threshold; adding %d worker(s) (%d/%d)",
				up.CPU*100, up.MeasuredCPU*100, up.Memory*100, upThreshold*100, d.ScaleUpBy, int32(len(workers))+d.ScaleUpBy, spec.MaxWorkers)
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
	// The MEAN says the cluster is idle. Before believing it, ask the busiest
	// worker — see `peakUtilization` for why the mean cannot answer this.
	if peak, hottest := peakUtilization(workers, s.Pods); peak.CPU >= threshold || peak.Memory >= threshold {
		d.Reason = fmt.Sprintf(
			"cluster mean is low (cpu=%.0f%% memory=%.0f%%) but %s is at cpu=%.0f%% memory=%.0f%%; not shrinking while a worker is loaded",
			util.CPU*100, util.Memory*100, hottest, peak.CPU*100, peak.Memory*100)
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

	// A worker that joined moments ago carries nothing yet, which makes it the
	// LOWEST-scored drain candidate and so the first to be retired. See
	// `maturedWorkers`.
	drainable := maturedWorkers(workers, s.Now, maxDuration(spec.ScaleDownDelay.Duration, newWorkerGrace))
	if len(drainable) == 0 {
		d.Reason = "cluster idle but every worker joined too recently to retire"
		return d
	}
	candidate, why := scaleDownCandidate(drainable, s.Pods, s.RWOClaims)
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
func pendingForCapacity(pods []corev1.Pod, workers []corev1.Node, now time.Time) []string {
	var out []string
	for _, p := range pods {
		if p.Status.Phase != corev1.PodPending || p.Spec.NodeName != "" || p.DeletionTimestamp != nil {
			continue
		}
		// A pending batch pod is a question of capacity the job's own team
		// answers (design 0025 G3, INV-5): a dev or test backlog never buys a node.
		if batchMayNotBuyNodes(p) {
			continue
		}
		// A Volcano Job's pod is one member of a gang, and only the gang can say
		// what it needs: counting its members here as well would buy their nodes
		// twice, and buy them for gangs the PodGroup rules refuse.
		//
		// (2026-11-14) Only a Volcano JOB's members. Design 0025 §4.7 skips every
		// pod with schedulerName volcano, but a single-mode run on Volcano stays a
		// batch/v1 Job with that schedulerName (§6.4), and the PodGroup Volcano's
		// PodGroup controller makes for its pod carries no labels and the native
		// Job as its owner, so the gang rules can never read its opt-in: skipping
		// it here left a prod `scaleUp: true` run that cannot buy a node. It stays
		// on this rule, with its queue checked in Decide (volcanoQueuesAdmit).
		// Without Volcano nothing writes such a pod's PodScheduled condition, so
		// it never reaches the message test below.
		if p.Spec.SchedulerName == volcanoSchedulerName && volcanoJobMember(p) {
			continue
		}
		msg, ok := unschedulableMessage(p)
		if !ok {
			continue
		}
		// Either a straight shortage, or a hostname spread that one more host
		// satisfies. Anything else (a zone conflict, an unbound volume) is not
		// something another identical worker can solve.
		hostSpread := strings.Contains(strings.ToLower(msg), antiAffinityMarker) && hostAntiAffinityOnly(p)
		fixableByANewWorker := mentionsCapacityShortage(msg) || hostSpread
		if !fixableByANewWorker {
			continue
		}
		// A pod pinned to labels no current worker carries will not fit the
		// next worker either — new nodes join the group with the same labels.
		if !placeableOnNewWorker(p, workers) || pinnedToNamedNodes(p) {
			continue
		}
		// Held next to a CI run's affinity assistant: the relocator's to solve
		// first (see affinityAssistantPinGrace).
		if pinnedToAffinityAssistant(p) {
			if since := unschedulableSince(p); !since.IsZero() && now.Sub(since) < affinityAssistantPinGrace {
				continue
			}
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

// volcanoJobMember reports whether the pod's controller is a Volcano Job, which
// makes it one member of that Job's gang.
func volcanoJobMember(p corev1.Pod) bool {
	for _, o := range p.OwnerReferences {
		if isVolcanoJobRef(o) {
			return true
		}
	}
	return false
}

// isVolcanoJobRef reports whether an owner reference is a controller reference
// to a batch.volcano.sh Job (any version).
func isVolcanoJobRef(o metav1.OwnerReference) bool {
	return o.Controller != nil && *o.Controller && o.Kind == "Job" &&
		strings.HasPrefix(o.APIVersion, volcanoJobAPIGroup+"/")
}

// volcanoQueuesAdmit drops the pending Volcano pods their queue would not take.
// What is left on the pod rule from Volcano is a single-mode run (see
// pendingForCapacity), and its queue decides about it exactly as it does about
// a gang (§4.7: "a queue at capability never scales"): a run its own queue
// cannot admit waits for the queue, and a new worker would sit idle. Its queue
// is the pod annotation the queue binding mutates, so a dev or test leaf is a
// dev or test run whatever its labels claim; a queue that was not read is full.
func volcanoQueuesAdmit(s Snapshot, pending []string) []string {
	if len(pending) == 0 {
		return pending
	}
	held := map[string]bool{}
	for _, p := range s.Pods {
		if p.Spec.SchedulerName != volcanoSchedulerName {
			continue
		}
		queue := p.Annotations[volcanoQueueAnnotation]
		if queueIsDevOrTest(queue) {
			held[p.Namespace+"/"+p.Name] = true
			continue
		}
		if at, _ := queueAtCapability(queue, podResourceList(p), s.Queues); at {
			held[p.Namespace+"/"+p.Name] = true
		}
	}
	if len(held) == 0 {
		return pending
	}
	out := make([]string, 0, len(pending))
	for _, name := range pending {
		if !held[name] {
			out = append(out, name)
		}
	}
	return out
}

// queueIsDevOrTest reports whether a queue is a dev or test leaf (§4.3).
func queueIsDevOrTest(queue string) bool {
	return strings.HasSuffix(queue, "-dev") || strings.HasSuffix(queue, "-test")
}

// podResourceList is the pod's effective requests as a list: CPU and memory as
// podRequests counts them, plus every other resource its containers request.
func podResourceList(p corev1.Pod) corev1.ResourceList {
	cpu, mem := podRequests(p)
	out := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(cpu, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(mem, resource.BinarySI),
	}
	for _, c := range p.Spec.Containers {
		for name, q := range c.Resources.Requests {
			if name == corev1.ResourceCPU || name == corev1.ResourceMemory {
				continue
			}
			sum := out[name]
			sum.Add(q)
			out[name] = sum
		}
	}
	return out
}

// batchMayNotBuyNodes reports whether the pod is a batch run that has not opted
// into growing the cluster. The label is what the composition and Kyverno agree on,
// and it travels on the pod because that is all a tick reads.
func batchMayNotBuyNodes(p corev1.Pod) bool {
	return p.Labels[workloadLabel] == "batch" && p.Labels[scaleUpLabel] != "true"
}

// scaleUpUtilization is util without the batch runs that may not buy nodes, for the
// one comparison that decides to buy them. Requests are exact. Measured CPU cannot be
// attributed to a pod from the node-level figures a tick has, so those runs' CPU
// requests are subtracted from it as the best available estimate: a run with no CPU
// limit bursting well above its request can still read as load, and the cooldown and
// maxWorkers remain the bound on that.
func scaleUpUtilization(workers []corev1.Node, pods []corev1.Pod, util Utilization) Utilization {
	var others []corev1.Pod
	var batchCPU int64
	onWorker := map[string]bool{}
	var allocCPU int64
	for _, n := range workers {
		onWorker[n.Name] = true
		allocCPU += n.Status.Allocatable.Cpu().MilliValue()
	}
	for _, p := range pods {
		if batchMayNotBuyNodes(p) {
			if onWorker[p.Spec.NodeName] && !isTerminated(p) {
				c, _ := podRequests(p)
				batchCPU += c
			}
			continue
		}
		others = append(others, p)
	}
	up := utilization(workers, others)
	up.Measured = util.Measured
	if util.Measured && allocCPU > 0 {
		up.MeasuredCPU = util.MeasuredCPU - float64(batchCPU)/float64(allocCPU)
		if up.MeasuredCPU < 0 {
			up.MeasuredCPU = 0
		}
		if up.MeasuredCPU > up.CPU {
			up.CPU = up.MeasuredCPU
		}
	}
	return up
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
		// A run mid-chunk restarts from its last commit if evicted, but somebody
		// asked for it not to be, and a node's retirement is never urgent enough to
		// overrule that. byNode holds live pods only, so a finished run waiting out
		// its TTL does not pin the node for a week.
		if refusesEviction(p) {
			return fmt.Sprintf("pod %s/%s is annotated %s: \"false\"", p.Namespace, p.Name, safeToEvictAnnotation)
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

// refusesEviction reports whether the pod asked not to be evicted to retire a node.
func refusesEviction(p corev1.Pod) bool {
	return p.Annotations[safeToEvictAnnotation] == "false"
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

// maxScaleUpBurst bounds a single scale-up. Even when a hundred pods are
// pending, buying more than this at once risks over-shooting on a transient
// backlog and paying for machines the scale-down then has to retire.
const maxScaleUpBurst int32 = 4

// scaleUpBurst sizes a scale-up from what the pending pods are actually blocked
// on.
//
// Pods held up by the CSI attach limit are the case worth batching: each new
// worker brings exactly VolumesPerNode fresh attachment slots, so the number of
// workers needed is arithmetic rather than guesswork. Everything else (cpu,
// memory, pod count) stays at one worker per tick, because a single machine
// usually clears it and the next tick re-measures.
//
// The result is always at least 1 and never exceeds the room left under
// MaxWorkers.
func scaleUpBurst(s Snapshot, pending []string, workers, maxWorkers int32) int32 {
	room := maxWorkers - workers
	if room <= 1 {
		return 1
	}

	perNode := int32(s.VolumesPerNode)
	if perNode <= 0 {
		// Unknown attach budget: volumes cannot size the burst, demand still can.
		perNode = 1 << 30
	}

	pendingSet := make(map[string]bool, len(pending))
	for _, name := range pending {
		pendingSet[name] = true
	}

	var volumeBlocked int32
	for _, p := range s.Pods {
		if !pendingSet[p.Namespace+"/"+p.Name] {
			continue
		}
		msg, ok := unschedulableMessage(p)
		if ok && strings.Contains(strings.ToLower(msg), volumeLimitMarker) {
			volumeBlocked++
		}
	}
	// CPU/memory demand sizes the burst too. A fresh cloud bring-up starts at
	// nodeCount workers and the whole platform arrives at once; adding ONE
	// worker per three-minute cooldown took a 2-worker DO cluster 20 minutes
	// to reach the six it needed, with the identity chain starved the whole
	// way, and `adhar up` ran out of patience on the console (2026-10-17).
	// Pods that are pending for capacity are summed and divided by what one
	// worker can take (its allocatable, less a packing margin, since pods do
	// not tile a node perfectly).
	shapeCPU, shapeMem := workerShape(s.Nodes)
	demandNodes := nodesForDemand(s.Pods, pendingSet, shapeCPU, shapeMem)
	if volumeBlocked == 0 && demandNodes <= 1 {
		return 1
	}

	// Round up: 8 blocked pods against 7 slots per node needs 2 workers.
	needed := (volumeBlocked + perNode - 1) / perNode
	if demandNodes > needed {
		needed = demandNodes
	}
	if needed < 1 {
		needed = 1
	}
	if needed > maxScaleUpBurst {
		needed = maxScaleUpBurst
	}
	if needed > room {
		needed = room
	}
	return needed
}

// StartupTaintsToClear returns the workers whose CSI startup taint
// (globals.NodeCSIStartupTaint) can be removed: the node's CSINode reports at
// least one driver, or the node has been Ready for longer than maxWait — the
// fallback for a cluster with no CSI driver at all, so the taint can never
// strand a node forever.
//
// Why this exists: a worker becomes schedulable the moment the kubelet
// registers, but the scheduler only enforces the per-node volume attach limit
// once the CSI node plugin has published a CSINode. In that window it packs the
// node without limit — on DigitalOcean, 11 attachments onto a 7-volume droplet,
// seen on two separate bring-ups. The join path registers the node with this
// taint; this is the other half, lifting it exactly when the limit is known.
func StartupTaintsToClear(nodes []corev1.Node, csinodes []storagev1.CSINode, now time.Time, maxWait time.Duration) []string {
	drivers := make(map[string]int, len(csinodes))
	for _, c := range csinodes {
		drivers[c.Name] = len(c.Spec.Drivers)
	}
	var clear []string
	for _, n := range nodes {
		tainted := false
		for _, t := range n.Spec.Taints {
			if t.Key == globals.NodeCSIStartupTaint {
				tainted = true
				break
			}
		}
		if !tainted {
			continue
		}
		if drivers[n.Name] > 0 {
			clear = append(clear, n.Name)
			continue
		}
		if ready := readySince(n); ready != nil && now.Sub(*ready) > maxWait {
			clear = append(clear, n.Name)
		}
	}
	return clear
}

// readySince returns when the node last became Ready, or nil if it is not.
func readySince(n corev1.Node) *time.Time {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			t := c.LastTransitionTime.Time
			return &t
		}
	}
	return nil
}

// WithoutTaint returns a copy of taints with every entry of key removed.
func WithoutTaint(taints []corev1.Taint, key string) []corev1.Taint {
	out := make([]corev1.Taint, 0, len(taints))
	for _, t := range taints {
		if t.Key != key {
			out = append(out, t)
		}
	}
	return out
}

// peakUtilization is the busiest single worker's requested share, and its name.
//
// `utilization` is a cluster-wide MEAN. That is the right question for "could
// another pod be placed somewhere" (scale UP) and the wrong one for "is this
// cluster idle enough to hand a node back" (scale DOWN), because a mean hides
// the one thing that makes shrinking unsafe: a single node carrying everything.
//
// Worse, a mean makes ADDING capacity look like idleness, so the autoscaler
// reclaims exactly the nodes that were added to relieve pressure. Live on
// 2026-10-13: `adhar-adhar-workers-4` was at 90% memory and the kernel was
// OOM-killing processes on it, while three freshly added empty workers pulled
// the cluster mean to `cpu=25% memory=38%`. The next tick read that as idle and
// retired two of them with the reason "cluster idle for 20m17s". Nothing
// rebalances a RUNNING pod, so the hot node stayed hot and the cluster
// oscillated back to the overloaded shape it started in, one node at a time —
// which is what the node flapping all that day actually was.
//
// DaemonSet pods are excluded for the same reason `scaleDownCandidate` excludes
// them: they exist on every node and are never rescheduled, so they describe
// the node's floor rather than its load.
func peakUtilization(workers []corev1.Node, pods []corev1.Pod) (Utilization, string) {
	byNode := map[string][]corev1.Pod{}
	for _, p := range pods {
		if p.Spec.NodeName == "" || isTerminated(p) {
			continue
		}
		byNode[p.Spec.NodeName] = append(byNode[p.Spec.NodeName], p)
	}
	var peak Utilization
	hottest := ""
	for _, n := range workers {
		allocCPU := n.Status.Allocatable.Cpu().MilliValue()
		allocMem := n.Status.Allocatable.Memory().Value()
		var cpu, mem int64
		for _, p := range byNode[n.Name] {
			if isDaemonSetPod(p) {
				continue
			}
			c, m := podRequests(p)
			cpu += c
			mem += m
		}
		var u Utilization
		if allocCPU > 0 {
			u.CPU = float64(cpu) / float64(allocCPU)
		}
		if allocMem > 0 {
			u.Memory = float64(mem) / float64(allocMem)
		}
		// "Busiest" is whichever node is closest to a ceiling in EITHER
		// dimension; a node at 95% CPU blocks scheduling just as hard as one
		// at 95% memory.
		if max(u.CPU, u.Memory) > max(peak.CPU, peak.Memory) {
			peak, hottest = u, n.Name
		}
	}
	return peak, hottest
}

// newWorkerGrace is the shortest time a worker is left alone after joining,
// whatever `scaleDownDelay` says.
//
// The grace is not a policy preference, which is why configuration cannot set
// it below this: it is the time the cluster physically needs before a new node
// carries anything. Kubernetes does not migrate running pods, so a node is
// empty until something is rescheduled, and an empty node is the first one
// `scaleDownCandidate` picks.
//
// The live cluster that exposed this had `scaleDownDelay: 0s`, so every
// cool-down check passed instantly and three freshly added workers were
// retired within four minutes of joining.
const newWorkerGrace = 10 * time.Minute

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// maturedWorkers drops workers too young to be retirement candidates.
//
// A node that joined moments ago has nothing scheduled on it — Kubernetes does
// not migrate running pods onto it — so it scores lowest in
// `scaleDownCandidate` and is picked first. `Status.LastScaleUp` already guards
// nodes THIS controller added, but it knows nothing about one added by
// `adhar cluster scale` from an operator's laptop, and that is how three
// workers added to relieve an OOM-killing cluster were reclaimed within four
// minutes of joining. The node's own age is the fact that does not depend on
// who created it.
func maturedWorkers(workers []corev1.Node, now time.Time, minAge time.Duration) []corev1.Node {
	out := make([]corev1.Node, 0, len(workers))
	for _, n := range workers {
		if now.Sub(n.CreationTimestamp.Time) >= minAge {
			out = append(out, n)
		}
	}
	return out
}

// packingMargin is the share of a worker's allocatable the burst sizing assumes
// pending pods can actually occupy; the rest is lost to imperfect bin-packing
// and the DaemonSets every node carries.
const packingMargin = 0.85

// workerShape is the allocatable of the largest existing worker (CPU in milli,
// memory in bytes), or zeros when no worker is known.
func workerShape(nodes []corev1.Node) (cpuMilli, memBytes int64) {
	for _, n := range schedulableWorkers(nodes) {
		if c := n.Status.Allocatable.Cpu().MilliValue(); c > cpuMilli {
			cpuMilli = c
		}
		if m := n.Status.Allocatable.Memory().Value(); m > memBytes {
			memBytes = m
		}
	}
	return cpuMilli, memBytes
}

// nodesForDemand is how many workers of the given shape the pending pods'
// requests fill, whichever of CPU or memory runs out first. Zero when the shape
// is unknown or nothing is pending.
func nodesForDemand(pods []corev1.Pod, pending map[string]bool, cpuMilli, memBytes int64) int32 {
	if cpuMilli <= 0 || memBytes <= 0 {
		return 0
	}
	var cpu, mem int64
	for _, p := range pods {
		if !pending[p.Namespace+"/"+p.Name] {
			continue
		}
		c, m := podRequests(p)
		cpu += c
		mem += m
	}
	if cpu == 0 && mem == 0 {
		return 0
	}
	perNodeCPU := float64(cpuMilli) * packingMargin
	perNodeMem := float64(memBytes) * packingMargin
	byCPU := math.Ceil(float64(cpu) / perNodeCPU)
	byMem := math.Ceil(float64(mem) / perNodeMem)
	return int32(math.Max(byCPU, byMem))
}

// measuredUtilization is the fleet's actual CPU use over its allocatable, over
// the workers whose kubelet could be read. False when none could.
func measuredUtilization(workers []corev1.Node, usage map[string]int64) (float64, bool) {
	var used, alloc int64
	for _, n := range workers {
		u, ok := usage[n.Name]
		if !ok {
			continue
		}
		used += u
		alloc += n.Status.Allocatable.Cpu().MilliValue()
	}
	if alloc <= 0 {
		return 0, false
	}
	return float64(used) / float64(alloc), true
}

// saturationBurst is how many workers to add to a fleet that is saturated by
// measured CPU: half of it (at least one), within the burst cap and the room
// left to maxWorkers.
func saturationBurst(workers, maxWorkers int32) int32 {
	n := (workers + 1) / 2
	if n < 1 {
		n = 1
	}
	if n > maxScaleUpBurst {
		n = maxScaleUpBurst
	}
	if room := maxWorkers - workers; n > room && room >= 1 {
		n = room
	}
	return n
}

// Gang is the part of a Volcano PodGroup (scheduling.volcano.sh/v1beta1) the
// scale-up decision reads. A Volcano Job's pods are not created until its
// PodGroup leaves Pending, so a gang held at enqueue is invisible to every
// pod-based rule above; this is its demand (design 0025 §4.7, R3).
type Gang struct {
	Namespace string
	Name      string
	// Phase is status.phase: Pending, Inqueue, Running, Unknown or Completed.
	Phase string
	// Unschedulable is a status condition of type Unschedulable that is True,
	// which the gang plugin writes for every gang it could not make ready.
	Unschedulable bool
	// Message is that condition's message: "<n>/<m> tasks in gang
	// unschedulable: <JobInfo.FitError()>", which carries one task's node fit
	// summary ("… 0/3 nodes are unavailable: 3 Insufficient cpu.") when any
	// task was tried against the nodes (Volcano v1.16.0 gang.go, job_info.go).
	Message string
	// OwnedByVolcanoJob is a controller reference to a batch.volcano.sh Job. Only
	// those gangs can buy nodes: the opt-in below is the Job's labels, which
	// Volcano's job controller copies onto the PodGroup it creates for it, and a
	// PodGroup written by hand carries whatever labels its author chose.
	OwnedByVolcanoJob bool
	Labels            map[string]string
	MinMember         int32
	Running           int32
	// MinResources is what the whole gang needs before any member runs.
	MinResources corev1.ResourceList
	Queue        string
}

// Key is "<namespace>/<name>".
func (g Gang) Key() string { return g.Namespace + "/" + g.Name }

// GangQueue is the part of a Volcano Queue the decision reads.
type GangQueue struct {
	Name   string
	Parent string
	// Capability is spec.capability; a resource it does not name is unbounded.
	Capability corev1.ResourceList
	// Allocated is status.allocated.
	Allocated corev1.ResourceList
}

const (
	// gangPhasePending / gangPhaseInqueue are the two phases a gang waits in.
	gangPhasePending = "Pending"
	gangPhaseInqueue = "Inqueue"
	// volcanoDefaultQueue is where Volcano puts a PodGroup that names none.
	volcanoDefaultQueue = "default"
	// maxQueueDepth bounds the walk up a queue hierarchy, so a parent loop in
	// what was read cannot hang a tick.
	maxQueueDepth = 16
)

// gangPlan is what the gangs ask of one tick.
type gangPlan struct {
	// nodes is how many workers the served gangs need between them.
	nodes int32
	// served are the gangs ("<ns>/<name>") those workers are for.
	served []string
	// refused explains the first opted-in gang that needs more workers than
	// maxWorkers leaves room for; "" when there is none.
	refused string
}

// planGangs sizes the scale-up the pending gangs need. Every gang gets the whole
// count it needs or nothing at all: a gang is all-or-nothing [S20], so a worker
// bought towards a gang that still does not fit schedules none of it and only
// adds a node the scale-down then has to retire. Gangs are taken in a stable
// order and each one is added only if it fits in the room the earlier ones
// left. A gang buys only when it may (gangWantsNodes) and Volcano says it is
// short of what a new worker brings (gangShortOfCapacity).
//
// The burst cap that bounds pod-driven scale-ups (maxScaleUpBurst) deliberately
// does not apply: it guards against guessing from a transient backlog, and a
// gang's minResources is not a guess. It is bounded by the room under
// maxWorkers instead, and by its queue's capability, which is itself a share of
// what maxWorkers can hold (§14).
func planGangs(s Snapshot, room int32) gangPlan {
	var plan gangPlan
	if len(s.Gangs) == 0 {
		return plan
	}
	workers := schedulableWorkers(s.Nodes)
	free := freeRoom(workers, s.Pods)
	gangs := append([]Gang(nil), s.Gangs...)
	sort.Slice(gangs, func(i, j int) bool { return gangs[i].Key() < gangs[j].Key() })
	for _, g := range gangs {
		if ok, _ := gangWantsNodes(g, s.Queues); !ok {
			continue
		}
		members := gangMembers(g, s.Pods)
		if ok, _ := gangShortOfCapacity(g, members, workers); !ok {
			continue
		}
		need, left := gangNodes(gangDemand(g, members), s.Nodes, free)
		if need == 0 {
			// A member larger than a whole worker: no number of identical workers
			// schedules it.
			continue
		}
		if plan.nodes+need > room {
			if plan.refused == "" {
				plan.refused = fmt.Sprintf("gang %s needs %d worker(s) but %d can be added under maxWorkers; not buying part of a gang",
					g.Key(), need, max(room-plan.nodes, 0))
			}
			continue
		}
		// The room a served gang was credited with is its own from here on; a gang
		// that is not served (above) leaves it to the next.
		free = left
		plan.nodes += need
		plan.served = append(plan.served, g.Key())
	}
	return plan
}

// gangWantsNodes reports whether a gang may buy workers, or why not. All of:
//   - it is waiting (Pending or Inqueue) and the scheduler says it is
//     unschedulable, with fewer members running than it needs;
//   - its Volcano Job opted in: a batch run labelled adhar.io/scale-up "true"
//     (the composition sets it only for `scaleUp: true` in prod), and not
//     labelled as any environment but prod — a dev or test gang never buys a
//     node (G3, INV-5);
//   - its queue, and every queue above it, is below capability: a gang its own
//     queue cannot admit waits for the queue, and a new worker would sit idle.
func gangWantsNodes(g Gang, queues map[string]GangQueue) (bool, string) {
	if g.Phase != gangPhasePending && g.Phase != gangPhaseInqueue {
		return false, "phase " + g.Phase
	}
	if !g.Unschedulable {
		return false, "not reported unschedulable"
	}
	if g.MinMember > 0 && g.Running >= g.MinMember {
		return false, "already running"
	}
	if !g.OwnedByVolcanoJob {
		return false, "not a Volcano Job's gang"
	}
	if g.Labels[workloadLabel] != "batch" || g.Labels[scaleUpLabel] != "true" {
		return false, "not opted in to scale-up"
	}
	if env, ok := g.Labels[environmentLabel]; ok && env != "prod" {
		return false, "environment " + env
	}
	// The queue says the same thing from the other side: adhar-batch-queue-binding
	// pins every gang to its namespace's `<org>-<team>-<env>` leaf (§4.3), so a
	// gang in a dev or test leaf is a dev or test run whatever its labels claim.
	if queueIsDevOrTest(g.Queue) {
		return false, "queue " + g.Queue + " is a dev or test leaf"
	}
	if at, why := queueAtCapability(g.Queue, g.MinResources, queues); at {
		return false, why
	}
	return true, ""
}

// queueAtCapability walks from the gang's queue to the root and reports the
// first queue that is at its capability, or could not take the gang on top of
// what it has allocated. A queue that was not read is treated as full: the
// tick cannot show it has room.
func queueAtCapability(name string, want corev1.ResourceList, queues map[string]GangQueue) (bool, string) {
	if name == "" {
		name = volcanoDefaultQueue
	}
	if _, ok := queues[name]; !ok {
		return true, "queue " + name + " not found"
	}
	seen := map[string]bool{}
	for cur, depth := name, 0; cur != ""; depth++ {
		if seen[cur] || depth > maxQueueDepth {
			return true, "queue hierarchy loops at " + cur
		}
		seen[cur] = true
		q, ok := queues[cur]
		if !ok {
			// An ancestor that was not read (Volcano's own root): nothing more
			// is known above this point.
			break
		}
		for res, capacity := range q.Capability {
			// (2026-11-14) Only what the gang asks for: Volcano's capacity plugin
			// admits a gang against the dimensions of its minResources, so a queue
			// out of GPUs still takes a CPU-only gang, and refusing it here would
			// leave an admissible gang waiting on nodes nobody buys.
			if need := want[res]; need.IsZero() {
				continue
			}
			allocated := q.Allocated[res]
			if allocated.Cmp(capacity) >= 0 {
				return true, fmt.Sprintf("queue %s is at its %s capability", cur, res)
			}
			total := allocated.DeepCopy()
			total.Add(want[res])
			if total.Cmp(capacity) > 0 {
				return true, fmt.Sprintf("queue %s cannot admit the gang's %s within its capability", cur, res)
			}
		}
		cur = q.Parent
	}
	return false, ""
}

// gangMembers are the gang's pods still waiting for a node: the pods in its
// namespace whose group-name annotation names it. A Volcano Job creates them
// once the gang leaves Pending, so a gang in Inqueue normally has them all.
func gangMembers(g Gang, pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if p.Namespace != g.Namespace || p.Annotations[volcanoGroupNameAnnotation] != g.Name {
			continue
		}
		if p.Status.Phase != corev1.PodPending || p.Spec.NodeName != "" || p.DeletionTimestamp != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// gangShortOfCapacity reports whether what Volcano wrote says the gang waits for
// room a new worker brings, or why not. The pod rule asks the same two
// questions of a pod (pendingForCapacity), and a gang needs them more: it keeps
// its Unschedulable condition for as long as it waits, so a gang held by
// anything else would buy a worker every cooldown until maxWorkers.
//
//   - The shortage must be capacity: a capacity marker in the gang's condition
//     message, or in the PodScheduled message Volcano writes on any of its
//     waiting members (TaskSchedulingReason: that pod's own node fit errors).
//     A gang held by taints, affinity or a GPU no worker has says something
//     else, and so does a gang waiting on its queue ("pod group is not ready"
//     with no node summary).
//   - Every waiting member must fit a new worker as the existing ones are
//     labelled: a member pinned to labels or names no worker has never runs,
//     and the gang runs only whole.
//
// (2026-11-14) Design 0025 §4.7 asks only for "an unschedulable condition";
// this is that condition read the way the pod rule reads a pod's.
func gangShortOfCapacity(g Gang, members []corev1.Pod, workers []corev1.Node) (bool, string) {
	short := mentionsCapacityShortage(g.Message)
	for _, p := range members {
		if !placeableOnNewWorker(p, workers) || pinnedToNamedNodes(p) {
			return false, "member " + p.Name + " needs a node a new worker would not be"
		}
		if msg, ok := unschedulableMessage(p); ok && mentionsCapacityShortage(msg) {
			short = true
		}
	}
	if !short {
		return false, "not unschedulable for capacity"
	}
	return true, ""
}

// memberDemand is one gang member's requests.
type memberDemand struct{ cpu, mem int64 }

// gangDemand is what each member of the gang asks for. The waiting member pods
// say it exactly, and are used whenever they cover what the gang still lacks: a
// manager and its workers differ, and only the pods show by how much. A gang
// whose pods are not (yet) there is its PodGroup's totals spread evenly over
// minMember, the estimate a PodGroup allows.
func gangDemand(g Gang, members []corev1.Pod) []memberDemand {
	lacking := int(g.MinMember - g.Running)
	if lacking < 1 {
		lacking = 1
	}
	if len(members) >= lacking {
		out := make([]memberDemand, 0, len(members))
		for _, p := range members {
			c, m := podRequests(p)
			out = append(out, memberDemand{cpu: c, mem: m})
		}
		return out
	}
	n := int64(g.MinMember)
	if n < 1 {
		n = 1
	}
	cpu := quantity(g.MinResources, corev1.ResourceCPU).MilliValue()
	mem := quantity(g.MinResources, corev1.ResourceMemory).Value()
	avg := memberDemand{cpu: (cpu + n - 1) / n, mem: (mem + n - 1) / n}
	out := make([]memberDemand, n)
	for i := range out {
		out[i] = avg
	}
	return out
}

// workerRoom is what a worker can still take: allocatable less the requests of
// the pods placed on it.
type workerRoom struct {
	node     string
	cpu, mem int64
}

// freeRoom is the room on each schedulable worker. A worker carrying a
// NoSchedule or NoExecute taint is left out: whether a member tolerates it is
// not worth guessing, and leaving it out can only buy a worker too many, never
// one too few.
func freeRoom(workers []corev1.Node, pods []corev1.Pod) []workerRoom {
	out := make([]workerRoom, 0, len(workers))
	index := map[string]int{}
	for _, n := range workers {
		if hasSchedulingTaint(n) {
			continue
		}
		index[n.Name] = len(out)
		out = append(out, workerRoom{
			node: n.Name,
			cpu:  n.Status.Allocatable.Cpu().MilliValue(),
			mem:  n.Status.Allocatable.Memory().Value(),
		})
	}
	for _, p := range pods {
		i, ok := index[p.Spec.NodeName]
		if !ok || isTerminated(p) {
			continue
		}
		c, m := podRequests(p)
		out[i].cpu -= c
		out[i].mem -= m
	}
	return out
}

func hasSchedulingTaint(n corev1.Node) bool {
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
			return true
		}
	}
	return false
}

// gangNodes is how many workers a gang needs on top of the room the existing
// ones have, or 0 when one of its members is larger than a whole worker. It
// returns the room left once the gang's members are placed in it.
//
// It places members, not resources: a pod does not split across nodes. Five
// members of 3 CPU each need five 4-CPU workers, where the resource sum alone
// (15 CPU) would say four and buy a cluster the gang still does not fit.
// Members go largest first into the existing room, then onto new workers of the
// largest worker's shape less the packing margin pod bursts use (first fit
// decreasing); a member that fits a worker but not its margin gets one alone.
//
// When every member fits the existing room by this arithmetic it still returns
// one: Volcano has just said the gang does not fit (gangShortOfCapacity), so
// the room is not what it looks like from requests — fragmentation the
// scheduler packs differently, or pods this tick did not see — and one worker is
// the smallest step that answers it; the next tick measures again.
//
// (2026-11-14) Design 0025 §4.7 writes the count as ceil(minResources / worker
// allocatable). That is the lower bound this returns when members are small
// next to a worker and the cluster is full; where members are large it
// undercounts and buys a partial gang, which §4.7's own all-or-nothing
// reasoning forbids, and where the cluster has room it overbuys.
func gangNodes(demand []memberDemand, nodes []corev1.Node, free []workerRoom) (int32, []workerRoom) {
	shapeCPU, shapeMem := workerShape(nodes)
	if shapeCPU <= 0 || shapeMem <= 0 {
		// No worker to measure: one step, and the next tick sizes the rest.
		return 1, free
	}
	for _, m := range demand {
		if m.cpu > shapeCPU || m.mem > shapeMem {
			return 0, free
		}
	}
	sorted := append([]memberDemand(nil), demand...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].cpu != sorted[j].cpu {
			return sorted[i].cpu > sorted[j].cpu
		}
		return sorted[i].mem > sorted[j].mem
	})
	left := append([]workerRoom(nil), free...)
	binCPU := int64(float64(shapeCPU) * packingMargin)
	binMem := int64(float64(shapeMem) * packingMargin)
	var bins []workerRoom
	for _, m := range sorted {
		if placeMember(left, m) || placeMember(bins, m) {
			continue
		}
		// A member above the margin leaves its worker below zero, so nothing
		// else is placed beside it.
		bins = append(bins, workerRoom{cpu: binCPU - m.cpu, mem: binMem - m.mem})
	}
	return max(int32(len(bins)), 1), left
}

// placeMember puts m in the first room that holds it, and reports whether one did.
func placeMember(rooms []workerRoom, m memberDemand) bool {
	for i := range rooms {
		if rooms[i].cpu >= m.cpu && rooms[i].mem >= m.mem {
			rooms[i].cpu -= m.cpu
			rooms[i].mem -= m.mem
			return true
		}
	}
	return false
}
