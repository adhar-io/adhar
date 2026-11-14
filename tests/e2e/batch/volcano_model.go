package batch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"
)

// What the Phase-2 canary reads from Volcano and from the BatchJob
// composition's Volcano branch, written down from the design (0025 §4.3–4.5,
// §6.4, §6.5, FU-3) rather than from the code under test, so a composition
// that drifted from the contract fails here. Untagged and unit-tested.

var (
	vcJobGVR     = schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1alpha1", Resource: "jobs"}
	vcCronJobGVR = schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1alpha1", Resource: "cronjobs"}
	podGroupGVR  = schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}
	queueGVR     = schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "queues"}
	teamGVR      = schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "teams"}
	appSetGVR    = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}
)

const (
	// DescriptorName is design D7's ConfigMap in adhar-system: present means
	// the BatchJob composition takes the Volcano path (scheduler: auto).
	DescriptorName = "adhar-batch-scheduler"
	// RootQueue is the parent of every team leaf (§4.3, core/volcano).
	RootQueue = "adhar-batch"
	// SchedulerConfigMap holds volcano-scheduler.conf (§4.2).
	SchedulerConfigMap = "volcano-scheduler-configmap"
	schedulerConfKey   = "volcano-scheduler.conf"

	annotationQueueName = "scheduling.volcano.sh/queue-name"
	annotationGroupName = "scheduling.k8s.io/group-name"
	labelVCJobName      = "volcano.sh/job-name"
	labelTaskSpec       = "volcano.sh/task-spec"
	keyTaskIndex        = "volcano.sh/task-index"
	labelBatchRole      = "adhar.io/batch-role"
	labelWorkload       = "adhar.io/workload"
	labelScaleUp        = "adhar.io/scale-up"
	labelTier           = "adhar.io/tier"
	labelOrganisation   = "adhar.io/organisation"
	labelTeam           = "adhar.io/team"
	argoTrackingID      = "argocd.argoproj.io/tracking-id"

	// The Volcano run classes (user decision DEC-1, 2026-11-14): Volcano will
	// neither reclaim nor preempt for a pod whose preemptionPolicy is Never,
	// so Volcano-scheduled runs get their own PreemptLowerPriority classes and
	// the native adhar-batch / adhar-batch-critical stay Never.
	ClassVolcano         = "adhar-batch-volcano"
	ClassVolcanoCritical = "adhar-batch-volcano-critical"

	// VolcanoScheduler is what a pod names to be scheduled by Volcano.
	VolcanoScheduler = "volcano"
)

// Tiers are the three leaves every team has (§4.3).
var Tiers = []string{"dev", "test", "prod"}

// LeafQueue is the queue a namespace's runs must use: `<org>-<team>-<tier>`
// from its labels, exactly as adhar-batch-queue-binding derives it.
func LeafQueue(nsLabels map[string]string) (string, error) {
	org, team, tier := nsLabels[labelOrganisation], nsLabels[labelTeam], nsLabels[labelTier]
	if org == "" || team == "" {
		return "", fmt.Errorf("the namespace has no %s/%s labels: it is not a team namespace", labelOrganisation, labelTeam)
	}
	ok := false
	for _, t := range Tiers {
		ok = ok || t == tier
	}
	if !ok {
		return "", fmt.Errorf("the namespace's %s is %q, not one of %v: it has no leaf queue", labelTier, tier, Tiers)
	}
	return org + "-" + team + "-" + tier, nil
}

/* ─────────────── Volcano's objects ─────────────── */

// Queue is a scheduling.volcano.sh Queue.
type Queue struct {
	Name, Parent, State string
	Reclaimable         *bool
	Capability          corev1.ResourceList
	Deserved            corev1.ResourceList
	Allocated           corev1.ResourceList
	Labels              map[string]string
}

func resourceListAt(obj map[string]interface{}, fields ...string) corev1.ResourceList {
	m, ok, _ := unstructured.NestedMap(obj, fields...)
	if !ok {
		return nil
	}
	out := corev1.ResourceList{}
	for k, v := range m {
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case int64:
			s = strconv.FormatInt(x, 10)
		case float64:
			s = strconv.FormatFloat(x, 'f', -1, 64)
		default:
			continue
		}
		if q, err := resource.ParseQuantity(s); err == nil {
			out[corev1.ResourceName(k)] = q
		}
	}
	return out
}

// ParseQueue reads a Queue.
func ParseQueue(u *unstructured.Unstructured) Queue {
	q := Queue{Name: u.GetName(), Labels: u.GetLabels()}
	q.Parent, _, _ = unstructured.NestedString(u.Object, "spec", "parent")
	q.State, _, _ = unstructured.NestedString(u.Object, "status", "state")
	if r, ok, _ := unstructured.NestedBool(u.Object, "spec", "reclaimable"); ok {
		q.Reclaimable = &r
	}
	q.Capability = resourceListAt(u.Object, "spec", "capability")
	q.Deserved = resourceListAt(u.Object, "spec", "deserved")
	q.Allocated = resourceListAt(u.Object, "status", "allocated")
	return q
}

// PodGroupCondition is one of a PodGroup's conditions.
type PodGroupCondition struct{ Type, Status, Reason, Message string }

// PodGroup is a scheduling.volcano.sh PodGroup.
type PodGroup struct {
	Name, Namespace, Queue, Phase, PriorityClass string
	MinMember                                    int64
	MinResources                                 corev1.ResourceList
	OwnerUID                                     types.UID
	OwnerKind                                    string
	Labels                                       map[string]string
	Conditions                                   []PodGroupCondition
	Running, Succeeded, Failed                   int64
}

// ParsePodGroup reads a PodGroup; its owner is the controller reference.
func ParsePodGroup(u *unstructured.Unstructured) PodGroup {
	pg := PodGroup{Name: u.GetName(), Namespace: u.GetNamespace(), Labels: u.GetLabels()}
	pg.Queue, _, _ = unstructured.NestedString(u.Object, "spec", "queue")
	pg.PriorityClass, _, _ = unstructured.NestedString(u.Object, "spec", "priorityClassName")
	pg.MinMember, _, _ = unstructured.NestedInt64(u.Object, "spec", "minMember")
	pg.MinResources = resourceListAt(u.Object, "spec", "minResources")
	pg.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	pg.Running, _, _ = unstructured.NestedInt64(u.Object, "status", "running")
	pg.Succeeded, _, _ = unstructured.NestedInt64(u.Object, "status", "succeeded")
	pg.Failed, _, _ = unstructured.NestedInt64(u.Object, "status", "failed")
	for _, o := range u.GetOwnerReferences() {
		if o.Controller != nil && *o.Controller {
			pg.OwnerUID, pg.OwnerKind = o.UID, o.Kind
		}
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]interface{})
		s := func(k string) string { v, _ := m[k].(string); return v }
		pg.Conditions = append(pg.Conditions, PodGroupCondition{Type: s("type"), Status: s("status"), Reason: s("reason"), Message: s("message")})
	}
	return pg
}

// Unschedulable is the gang's Unschedulable condition message, while it holds.
func (pg PodGroup) Unschedulable() (string, bool) {
	for _, c := range pg.Conditions {
		if c.Type == "Unschedulable" && c.Status == "True" {
			return c.Message, true
		}
	}
	return "", false
}

// Unstarted is a gang that has not been admitted to run: Volcano's Pending
// (not enqueued, so its pods are not even created [S20]) or Inqueue (enqueued,
// pods created and waiting to be bound together).
func (pg PodGroup) Unstarted() bool { return pg.Phase == "Pending" || pg.Phase == "Inqueue" }

// VolcanoJob is a batch.volcano.sh Job's status.
type VolcanoJob struct {
	Name, Namespace, Phase, Queue, PriorityClass string
	UID                                          types.UID
	MinAvailable, RetryCount                     int64
	Pending, Running, Succeeded, Failed          int64
}

// ParseVolcanoJob reads a Volcano Job.
func ParseVolcanoJob(u *unstructured.Unstructured) VolcanoJob {
	j := VolcanoJob{Name: u.GetName(), Namespace: u.GetNamespace(), UID: u.GetUID()}
	j.Phase, _, _ = unstructured.NestedString(u.Object, "status", "state", "phase")
	j.Queue, _, _ = unstructured.NestedString(u.Object, "spec", "queue")
	j.PriorityClass, _, _ = unstructured.NestedString(u.Object, "spec", "priorityClassName")
	j.MinAvailable, _, _ = unstructured.NestedInt64(u.Object, "spec", "minAvailable")
	j.RetryCount, _, _ = unstructured.NestedInt64(u.Object, "status", "retryCount")
	j.Pending, _, _ = unstructured.NestedInt64(u.Object, "status", "pending")
	j.Running, _, _ = unstructured.NestedInt64(u.Object, "status", "running")
	j.Succeeded, _, _ = unstructured.NestedInt64(u.Object, "status", "succeeded")
	j.Failed, _, _ = unstructured.NestedInt64(u.Object, "status", "failed")
	return j
}

// Finished says whether a Volcano Job phase is terminal.
func (j VolcanoJob) Finished() bool {
	switch j.Phase {
	case "Completed", "Failed", "Terminated", "Aborted":
		return true
	}
	return false
}

// PodGroupOf finds the job's gang: by controller owner UID, else by Volcano's
// naming (`<job>-<uid>` from v1.10, `podgroup-<uid>` for a plain pod), which is
// one of the formats the canary records live (FU-11).
func PodGroupOf(job VolcanoJob, groups []PodGroup) (PodGroup, bool) {
	for _, g := range groups {
		if job.UID != "" && g.OwnerUID == job.UID {
			return g, true
		}
	}
	for _, g := range groups {
		if g.Namespace == job.Namespace && (g.Name == job.Name || strings.HasPrefix(g.Name, job.Name+"-")) {
			return g, true
		}
	}
	return PodGroup{}, false
}

// TaskIndex is a Volcano pod's index in its task: the label if Volcano sets
// one, else the annotation (which of the two v1.16 writes is recorded live).
func TaskIndex(p corev1.Pod) (string, string, bool) {
	if v, ok := p.Labels[keyTaskIndex]; ok {
		return v, "label", true
	}
	if v, ok := p.Annotations[keyTaskIndex]; ok {
		return v, "annotation", true
	}
	return "", "", false
}

// RoleOf is a run pod's role: the composition's adhar.io/batch-role, else
// Volcano's task name.
func RoleOf(p corev1.Pod) string {
	if r := p.Labels[labelBatchRole]; r != "" {
		return r
	}
	return p.Labels[labelTaskSpec]
}

/* ─────────────── the scheduler's configuration ─────────────── */

// SchedulerConf is volcano-scheduler.conf: the actions and each plugin's
// arguments by name.
type SchedulerConf struct {
	Actions []string
	Plugins map[string]map[string]interface{}
	// Tier is the 0-based tier each plugin is in.
	Tier map[string]int
}

// ParseSchedulerConf reads the ConfigMap's volcano-scheduler.conf.
func ParseSchedulerConf(conf string) (SchedulerConf, error) {
	var doc struct {
		Actions string `json:"actions"`
		Tiers   []struct {
			Plugins []map[string]interface{} `json:"plugins"`
		} `json:"tiers"`
	}
	if err := yaml.Unmarshal([]byte(conf), &doc); err != nil {
		return SchedulerConf{}, err
	}
	c := SchedulerConf{Plugins: map[string]map[string]interface{}{}, Tier: map[string]int{}}
	for _, a := range strings.Split(doc.Actions, ",") {
		if a = strings.TrimSpace(a); a != "" {
			c.Actions = append(c.Actions, a)
		}
	}
	for i, t := range doc.Tiers {
		for _, p := range t.Plugins {
			name, _ := p["name"].(string)
			if name == "" {
				continue
			}
			args, _ := p["arguments"].(map[string]interface{})
			if args == nil {
				args = map[string]interface{}{}
			}
			c.Plugins[name], c.Tier[name] = args, i
		}
	}
	if len(c.Actions) == 0 || len(c.Plugins) == 0 {
		return c, fmt.Errorf("no actions or plugins: not a volcano-scheduler.conf")
	}
	return c, nil
}

// OvercommitFactor is the overcommit plugin's factor (Volcano's default 1.2
// when the plugin names none), and false without the plugin (DEC-2).
func (c SchedulerConf) OvercommitFactor() (float64, bool) {
	args, ok := c.Plugins["overcommit"]
	if !ok {
		return 0, false
	}
	switch v := args["overcommit-factor"].(type) {
	case float64:
		return v, true
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
	}
	return 1.2, true
}

/* ─────────────── room on the cluster ─────────────── */

// Room is CPU in millicores and memory in bytes.
type Room struct{ MilliCPU, Memory int64 }

func (r Room) String() string {
	return fmt.Sprintf("%dm CPU / %dMi", r.MilliCPU, r.Memory>>20)
}

// Sub is r minus o, floored at zero.
func (r Room) Sub(o Room) Room {
	return Room{MilliCPU: max(r.MilliCPU-o.MilliCPU, 0), Memory: max(r.Memory-o.Memory, 0)}
}

func roomOf(l corev1.ResourceList) Room {
	return Room{MilliCPU: l.Cpu().MilliValue(), Memory: l.Memory().Value()}
}

// Schedulable says whether a batch pod can land on the node: Ready, not
// cordoned, no NoSchedule/NoExecute taint (run pods carry no tolerations).
func Schedulable(n corev1.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
			return false
		}
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// PodRequests is what the scheduler charges a pod: the larger of the sum of
// its containers and its largest init container, per resource.
func PodRequests(p corev1.Pod) Room {
	var sum, init Room
	for _, c := range p.Spec.Containers {
		sum.MilliCPU += c.Resources.Requests.Cpu().MilliValue()
		sum.Memory += c.Resources.Requests.Memory().Value()
	}
	for _, c := range p.Spec.InitContainers {
		init.MilliCPU = max(init.MilliCPU, c.Resources.Requests.Cpu().MilliValue())
		init.Memory = max(init.Memory, c.Resources.Requests.Memory().Value())
	}
	return Room{MilliCPU: max(sum.MilliCPU, init.MilliCPU), Memory: max(sum.Memory, init.Memory)}
}

// ClusterRoom is the allocatable and the idle room of the schedulable nodes:
// allocatable minus the requests of every bound, unfinished pod — the view
// both kube-scheduler and Volcano's node info take. Idle is per node.
func ClusterRoom(nodes []corev1.Node, pods []corev1.Pod) (total Room, idle map[string]Room) {
	idle = map[string]Room{}
	for _, n := range nodes {
		if Schedulable(n) {
			a := Room{MilliCPU: n.Status.Allocatable.Cpu().MilliValue(), Memory: n.Status.Allocatable.Memory().Value()}
			idle[n.Name] = a
			total.MilliCPU += a.MilliCPU
			total.Memory += a.Memory
		}
	}
	for _, p := range pods {
		r, ok := idle[p.Spec.NodeName]
		if !ok || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		idle[p.Spec.NodeName] = r.Sub(PodRequests(p))
	}
	return total, idle
}

// RoomiestNode is the schedulable node with the most idle CPU.
func RoomiestNode(idle map[string]Room) (string, Room, bool) {
	var best string
	for n, r := range idle {
		if best == "" || r.MilliCPU > idle[best].MilliCPU || (r.MilliCPU == idle[best].MilliCPU && n < best) {
			best = n
		}
	}
	return best, idle[best], best != ""
}

// BallastFor is the CPU each node is filled with so that the cluster's only
// room is `room` on node `keep`: a scenario sized for one node must not pass
// or fail because a second node had room, or because the room was split
// between nodes so that no member fits on either.
func BallastFor(idle map[string]Room, keep string, room Room) (map[string]Room, error) {
	k, ok := idle[keep]
	if !ok {
		return nil, fmt.Errorf("node %s is not schedulable", keep)
	}
	if k.MilliCPU < room.MilliCPU {
		return nil, fmt.Errorf("node %s has %s idle, less than the %s room", keep, k, room)
	}
	out := map[string]Room{}
	for n, r := range idle {
		b := r.MilliCPU
		if n == keep {
			b -= room.MilliCPU
		}
		if b > 0 {
			out[n] = Room{MilliCPU: b}
		}
	}
	return out, nil
}

// memberMemory is what every synthetic gang member requests (and limits): small
// enough that CPU is always the dimension a scenario is sized by.
const memberMemory = 32 << 20

// minMemberCPU is the smallest member worth placing; below it the platform's
// own churn is larger than the margin between "fits" and "does not".
const minMemberCPU = 50

// GangFit is A2.1's arrangement on one node: ballast leaves Room, in which
// Fits members of Member fit and Fits+1 do not, with half a member of margin
// each way.
type GangFit struct {
	Room, Member Room
	Fits         int
}

// PlanGangFit sizes A2.1 from the node's idle room and the leaf's free
// capability: the room is at most 90% of idle and 60% of the queue's free
// capability, so neither the queue nor a pod the platform starts meanwhile is
// what keeps the gang out — only the room the suite left.
func PlanGangFit(nodeIdle, queueFree Room, n int) (GangFit, error) {
	if n < 1 {
		return GangFit{}, fmt.Errorf("a room for %d members", n)
	}
	room := nodeIdle.MilliCPU * 9 / 10
	if queueFree.MilliCPU > 0 {
		room = min(room, queueFree.MilliCPU*6/10)
	}
	member := Room{MilliCPU: room * 2 / int64(2*n+1), Memory: memberMemory}
	if member.MilliCPU < minMemberCPU {
		return GangFit{}, fmt.Errorf("a room of %dm (node idle %s, queue free %s) leaves %dm per member for %d members; give the Kind node more CPU",
			room, nodeIdle, queueFree, member.MilliCPU, n+1)
	}
	if int64(n+1)*memberMemory > nodeIdle.Memory {
		return GangFit{}, fmt.Errorf("idle memory %s cannot hold %d members of %dMi", nodeIdle, n+1, memberMemory>>20)
	}
	return GangFit{Room: Room{MilliCPU: member.MilliCPU*int64(n) + member.MilliCPU/2}, Member: member, Fits: n}, nil
}

// ReclaimPlan is A2.4's arrangement on the dev run's node: ballast leaves
// Room; the dev run (App) and a dev filler gang of one member (Filler) take
// all but Spare of it; then a prod gang of two ProdMembers needs more than the
// spare plus the filler, so Volcano can serve it only by reclaiming the dev
// run too — within prod's deserved share, which is what entitles prod to
// reclaim from reclaimable dev [S15].
type ReclaimPlan struct {
	Room, App, Filler, Spare, ProdMember Room
}

// PlanReclaim sizes A2.4 from the node's idle room (with the dev run already
// running there, so it is NOT in idle), the run's request and the prod leaf's
// deserved share.
func PlanReclaim(nodeIdle, app, prodDeserved Room) (ReclaimPlan, error) {
	const spare = 100
	if prodDeserved.MilliCPU <= 0 {
		return ReclaimPlan{}, fmt.Errorf("the prod leaf has no deserved CPU, so it can never reclaim (design §4.3, DEC-3)")
	}
	// The room counts the dev run: prod may take all of it, being under its share.
	room := min((nodeIdle.MilliCPU+app.MilliCPU)*9/10, prodDeserved.MilliCPU*9/10)
	filler := room - app.MilliCPU - spare
	if filler < minMemberCPU {
		return ReclaimPlan{}, fmt.Errorf("the dev run requests %dm CPU and the room is %dm (node idle %s plus the run, 90%% of prod deserved %s): no room for a dev filler beside it",
			app.MilliCPU, room, nodeIdle, prodDeserved)
	}
	if (prodDeserved.Memory > 0 && 2*memberMemory > prodDeserved.Memory) || 3*memberMemory > nodeIdle.Memory {
		return ReclaimPlan{}, fmt.Errorf("prod deserved %s or node idle %s cannot hold the gangs' memory", prodDeserved, nodeIdle)
	}
	// Prod needs spare + filler + half the run: every free millicore and every
	// one the filler holds is not enough without the run's.
	prod := spare + filler + app.MilliCPU/2
	return ReclaimPlan{
		Room:       Room{MilliCPU: room},
		App:        app,
		Filler:     Room{MilliCPU: filler, Memory: memberMemory},
		Spare:      Room{MilliCPU: spare},
		ProdMember: Room{MilliCPU: (prod + 1) / 2, Memory: memberMemory},
	}, nil
}

/* ─────────────── DEC-3: runs sized onto one QueueFits verdict ─────────────── */

// QueueFitsManager is what the oversize BatchJobs give their manager; only the
// workers are sized from the leaf.
var QueueFitsManager = Room{MilliCPU: 100, Memory: 128 << 20}

// SizedRun is a partitioned run's resources: one manager plus Workers workers,
// which is what the BatchJob composition counts as the run's ask.
type SizedRun struct {
	Workers int64
	Worker  Room
}

// Ask is the whole gang, manager included, as the composition adds it up.
func (r SizedRun) Ask() Room {
	return Room{MilliCPU: QueueFitsManager.MilliCPU + r.Workers*r.Worker.MilliCPU, Memory: QueueFitsManager.Memory + r.Workers*r.Worker.Memory}
}

// Resources is the BatchJob's spec.parameters.resources for the run.
func (r SizedRun) Resources() map[string]interface{} {
	return map[string]interface{}{
		"cpu": resource.NewMilliQuantity(QueueFitsManager.MilliCPU, resource.DecimalSI).String(), "memory": resource.NewQuantity(QueueFitsManager.Memory, resource.BinarySI).String(),
		"workerCpu": resource.NewMilliQuantity(r.Worker.MilliCPU, resource.DecimalSI).String(), "workerMemory": resource.NewQuantity(r.Worker.Memory, resource.BinarySI).String(),
	}
}

// overBy is the per-worker share of two workers that takes the gang just past
// limit: (limit - manager + 1) / 2 rounded up, at least unit, in whole units.
func overBy(limit, manager, unit int64) int64 {
	need := max(limit-manager, 0) + 1
	w := (need + 1) / 2
	return max((w+unit-1)/unit*unit, unit)
}

// RunOverDeserved sizes a two-worker run larger than the leaf's deserved share
// in EVERY dimension and within its capability in every dimension it declares,
// so the composition's verdict is ExceedsDeserved and not ExceedsCapability,
// which it checks first. A leaf whose deserved share leaves no room under its
// capability (D8 caps deserved AT capability) cannot hold such a run.
func RunOverDeserved(deserved, capability corev1.ResourceList) (SizedRun, error) {
	d := roomOf(deserved)
	if _, ok := deserved[corev1.ResourceCPU]; !ok || d.MilliCPU <= 0 {
		return SizedRun{}, fmt.Errorf("the leaf has no deserved CPU to exceed (D8, DEC-3): %v", deserved)
	}
	if _, ok := deserved[corev1.ResourceMemory]; !ok || d.Memory <= 0 {
		return SizedRun{}, fmt.Errorf("the leaf has no deserved memory to exceed (D8, DEC-3): %v", deserved)
	}
	run := SizedRun{Workers: 2, Worker: Room{MilliCPU: overBy(d.MilliCPU, QueueFitsManager.MilliCPU, 1), Memory: overBy(d.Memory, QueueFitsManager.Memory, 1<<20)}}
	ask, c := run.Ask(), roomOf(capability)
	if _, ok := capability[corev1.ResourceCPU]; ok && ask.MilliCPU > c.MilliCPU {
		return SizedRun{}, fmt.Errorf("a run past deserved %s asks %s, more than capability %s: the composition would say ExceedsCapability first", d, ask, c)
	}
	if _, ok := capability[corev1.ResourceMemory]; ok && ask.Memory > c.Memory {
		return SizedRun{}, fmt.Errorf("a run past deserved %s asks %s, more than capability %s: the composition would say ExceedsCapability first", d, ask, c)
	}
	return run, nil
}

// RunOverCapability sizes a two-worker run larger than the leaf's capability
// in one dimension, CPU where the leaf caps it: Volcano never enqueues it, so
// the composition says ExceedsCapability on any tier.
func RunOverCapability(capability corev1.ResourceList) (SizedRun, error) {
	c := roomOf(capability)
	if _, ok := capability[corev1.ResourceCPU]; ok && c.MilliCPU > 0 {
		return SizedRun{Workers: 2, Worker: Room{MilliCPU: overBy(c.MilliCPU, QueueFitsManager.MilliCPU, 1), Memory: memberMemory}}, nil
	}
	if _, ok := capability[corev1.ResourceMemory]; ok && c.Memory > 0 {
		return SizedRun{Workers: 2, Worker: Room{MilliCPU: minMemberCPU, Memory: overBy(c.Memory, QueueFitsManager.Memory, 1<<20)}}, nil
	}
	return SizedRun{}, fmt.Errorf("the leaf declares no capability to exceed (§4.3): %v", capability)
}

/* ─────────────── A2.7: who Volcano evicted ─────────────── */

// PodRecord is what the suite remembers of a pod before a scenario.
type PodRecord struct {
	Namespace, Name, Scheduler string
	UID                        types.UID
}

// Snapshot indexes pods by namespace/name.
func Snapshot(pods []corev1.Pod) map[string]PodRecord {
	out := map[string]PodRecord{}
	for _, p := range pods {
		s := p.Spec.SchedulerName
		if s == "" {
			s = corev1.DefaultSchedulerName
		}
		out[p.Namespace+"/"+p.Name] = PodRecord{Namespace: p.Namespace, Name: p.Name, Scheduler: s, UID: p.UID}
	}
	return out
}

// evictionReason is how Volcano words a victim: its reclaim and preempt
// actions evict, and the cache records the eviction on the pod.
var evictionReason = regexp.MustCompile(`(?i)evict|preempt|reclaim`)

// VolcanoEvictions are the events Volcano wrote about a pod it took away.
func VolcanoEvictions(events []corev1.Event) []corev1.Event {
	var out []corev1.Event
	for _, e := range events {
		by := strings.ToLower(e.Source.Component + " " + e.ReportingController)
		if e.InvolvedObject.Kind != "Pod" || !strings.Contains(by, "volcano") {
			continue
		}
		if evictionReason.MatchString(e.Reason) || evictionReason.MatchString(e.Message) {
			out = append(out, e)
		}
	}
	return out
}

// EvictedNotScheduled is A2.7's verdict: every pod Volcano evicted that it did
// not schedule. A pod the snapshot never saw is reported as unknown, never
// assumed to be Volcano's.
func EvictedNotScheduled(events []corev1.Event, before map[string]PodRecord) []string {
	var bad []string
	seen := map[string]bool{}
	for _, e := range VolcanoEvictions(events) {
		key := e.InvolvedObject.Namespace + "/" + e.InvolvedObject.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		r, ok := before[key]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s (not in the snapshot; event %s: %s)", key, e.Reason, e.Message))
		case r.Scheduler != VolcanoScheduler:
			bad = append(bad, fmt.Sprintf("%s (scheduled by %s; event %s: %s)", key, r.Scheduler, e.Reason, e.Message))
		}
	}
	sort.Strings(bad)
	return bad
}

/* ─────────────── capacity markers (A2.6, FU-11) ─────────────── */

// CapacityMarkers are the scheduler wordings the node autoscaler treats as "no
// node has room" (autoscaler/decision.go capacityShortageMarkers). The suite
// checks Volcano's live Unschedulable message against them: a Volcano that
// worded a shortage differently would leave every gang unbought.
// TestCapacityMarkersAreTheAutoscalers pins this list to the autoscaler's.
var CapacityMarkers = []string{
	"insufficient cpu",
	"insufficient memory",
	"insufficient pods",
	"too many pods",
	"insufficient ephemeral-storage",
	"exceed max volume count",
}

// MentionsCapacity says whether a scheduler message names a capacity shortage.
func MentionsCapacity(msg string) bool {
	msg = strings.ToLower(msg)
	for _, m := range CapacityMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

/* ─────────────── A2.10: what owns the descriptor ─────────────── */

// AppSetOwner is the Argo CD Application that keeps the live platform
// ApplicationSet equal to applicationset.yaml in Gitea, with selfHeal
// (platform/stack/adhar-appset-source.yaml). A patch to the live ApplicationSet
// is reverted by it within seconds unless its automated sync is held first.
const AppSetOwner = "adhar-platform-applicationset"

var argoAppGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// SelfHealHold is the pair of JSON patches that take an Application's
// automated sync away and give it back as it was. Both are nil when the
// Application syncs by hand already: there is nothing to hold.
func SelfHealHold(app *unstructured.Unstructured) (hold, release []byte, err error) {
	automated, ok, err := unstructured.NestedFieldCopy(app.Object, "spec", "syncPolicy", "automated")
	if err != nil || !ok {
		return nil, nil, err
	}
	const path = "/spec/syncPolicy/automated"
	if hold, err = json.Marshal([]map[string]interface{}{{"op": "remove", "path": path}}); err != nil {
		return nil, nil, err
	}
	release, err = json.Marshal([]map[string]interface{}{{"op": "add", "path": path, "value": automated}})
	return hold, release, err
}

// TrackingApp is the Argo CD Application that manages an object, from its
// tracking-id annotation `<app>:<group>/<kind>:<namespace>/<name>`.
func TrackingApp(annotations map[string]string) (string, error) {
	id := annotations[argoTrackingID]
	app, rest, ok := strings.Cut(id, ":")
	if !ok || app == "" || !strings.Contains(rest, "/") {
		return "", fmt.Errorf("no Argo CD tracking id (%s=%q): the object is not managed by an Application", argoTrackingID, id)
	}
	return app, nil
}

// ElementPatch is the JSON patch that sets one list-generator element's
// `enabled` in a live ApplicationSet, guarded by a `test` of the element's
// name so a reordered list fails the patch rather than switching a neighbour.
func ElementPatch(appset *unstructured.Unstructured, element string, on bool) ([]byte, error) {
	gens, _, _ := unstructured.NestedSlice(appset.Object, "spec", "generators")
	for gi, g := range gens {
		gm, _ := g.(map[string]interface{})
		list, _ := gm["list"].(map[string]interface{})
		elems, _ := list["elements"].([]interface{})
		for ei, e := range elems {
			em, _ := e.(map[string]interface{})
			if em["name"] != element {
				continue
			}
			path := fmt.Sprintf("/spec/generators/%d/list/elements/%d", gi, ei)
			value := map[bool]string{true: "true", false: "false"}[on]
			op := "replace"
			if _, has := em["enabled"]; !has {
				op = "add"
			}
			return json.Marshal([]map[string]interface{}{
				{"op": "test", "path": path + "/name", "value": element},
				{"op": op, "path": path + "/enabled", "value": value},
			})
		}
	}
	return nil, fmt.Errorf("ApplicationSet %s has no list element %q", appset.GetName(), element)
}

// ElementForApplication is the list element whose Application is app: the
// platform ApplicationSet names each Application after its element.
func ElementForApplication(appset *unstructured.Unstructured, app string) (string, error) {
	gens, _, _ := unstructured.NestedSlice(appset.Object, "spec", "generators")
	for _, g := range gens {
		gm, _ := g.(map[string]interface{})
		list, _ := gm["list"].(map[string]interface{})
		elems, _ := list["elements"].([]interface{})
		for _, e := range elems {
			em, _ := e.(map[string]interface{})
			if name, _ := em["name"].(string); name != "" && name == app {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("Application %s is not an element of ApplicationSet %s", app, appset.GetName())
}

/* ─────────────── run templates of Phase 2 ─────────────── */

// ParseRunTemplateKinds reads ConfigMap <name>-run allowing the Phase-2 kinds
// (§6.5): Job (single, native or on Volcano), IndexedJob (partitioned, native
// fallback) and VolcanoJob (partitioned on Volcano). The manifest must be the
// kind the ConfigMap says.
func ParseRunTemplateKinds(data map[string]string) (*RunTemplate, error) {
	t := &RunTemplate{Kind: data["kind"], Naming: data["naming"]}
	if t.Naming != "v1" {
		return nil, fmt.Errorf("run template naming %q; this suite implements v1", t.Naming)
	}
	wantAPI := map[string]string{"Job": "batch/v1", "IndexedJob": "batch/v1", "VolcanoJob": "batch.volcano.sh/v1alpha1"}[t.Kind]
	if wantAPI == "" {
		return nil, fmt.Errorf("run template kind %q is not Job, IndexedJob or VolcanoJob (§6.5)", t.Kind)
	}
	var doc map[string]interface{}
	asJSON, err := yaml.YAMLToJSON([]byte(data["template.yaml"]))
	if err == nil {
		err = utiljson.Unmarshal(asJSON, &doc)
	}
	if err != nil || doc == nil {
		return nil, fmt.Errorf("template.yaml cannot be read: %v", err)
	}
	t.Manifest = &unstructured.Unstructured{Object: doc}
	if t.Manifest.GetAPIVersion() != wantAPI || t.Manifest.GetKind() != "Job" || t.Manifest.GetName() != "" {
		return nil, fmt.Errorf("template.yaml is a %s %s named %q; a %s run template is an unnamed %s Job",
			t.Manifest.GetAPIVersion(), t.Manifest.GetKind(), t.Manifest.GetName(), t.Kind, wantAPI)
	}
	if raw := data["parameters.json"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &t.Params); err != nil {
			return nil, fmt.Errorf("parameters.json: %w", err)
		}
	}
	return t, nil
}

func podSpecsOf(t *RunTemplate) []map[string]interface{} {
	var out []map[string]interface{}
	if t.Kind == "VolcanoJob" {
		tasks, _, _ := unstructured.NestedSlice(t.Manifest.Object, "spec", "tasks")
		for _, task := range tasks {
			tm, _ := task.(map[string]interface{})
			spec, _, _ := unstructured.NestedMap(tm, "template", "spec")
			out = append(out, spec)
		}
		return out
	}
	spec, _, _ := unstructured.NestedMap(t.Manifest.Object, "spec", "template", "spec")
	return append(out, spec)
}

func str(m map[string]interface{}, fields ...string) string {
	v, _, _ := unstructured.NestedString(m, fields...)
	return v
}

// CheckNativeRun is A2.10's verdict on a single-mode template: nothing in it
// names Volcano any more.
func CheckNativeRun(t *RunTemplate) []string {
	var p []string
	if t.Kind != "Job" {
		p = append(p, fmt.Sprintf("kind %s, want Job", t.Kind))
	}
	for _, spec := range podSpecsOf(t) {
		if s := str(spec, "schedulerName"); s != "" && s != corev1.DefaultSchedulerName {
			p = append(p, "the pod names schedulerName "+s)
		}
		if c := str(spec, "priorityClassName"); strings.HasPrefix(c, ClassVolcano) {
			p = append(p, "the pod keeps the Volcano class "+c)
		}
	}
	ann, _, _ := unstructured.NestedStringMap(t.Manifest.Object, "spec", "template", "metadata", "annotations")
	if q := ann[annotationQueueName]; q != "" {
		p = append(p, "the pod keeps the queue annotation "+q)
	}
	return p
}

// CheckVolcanoSingle is the single-mode Volcano branch (§6.4, FU-3e): still a
// native Job, whose pod alone names Volcano, its leaf and a Volcano class.
func CheckVolcanoSingle(t *RunTemplate, leaf string) []string {
	var p []string
	if t.Kind != "Job" {
		p = append(p, fmt.Sprintf("kind %s, want Job: single mode on Volcano changes only the pod template", t.Kind))
	}
	spec, _, _ := unstructured.NestedMap(t.Manifest.Object, "spec", "template", "spec")
	if s := str(spec, "schedulerName"); s != VolcanoScheduler {
		p = append(p, fmt.Sprintf("the pod's schedulerName is %q, want %s", s, VolcanoScheduler))
	}
	if c := str(spec, "priorityClassName"); c != ClassVolcano && c != ClassVolcanoCritical {
		p = append(p, fmt.Sprintf("the pod's PriorityClass is %q, want %s or %s (DEC-1)", c, ClassVolcano, ClassVolcanoCritical))
	}
	ann, _, _ := unstructured.NestedStringMap(t.Manifest.Object, "spec", "template", "metadata", "annotations")
	if q := ann[annotationQueueName]; q != "" && q != leaf {
		p = append(p, fmt.Sprintf("the pod's queue is %s, want the namespace's leaf %s", q, leaf))
	}
	return p
}

func intAt(m map[string]interface{}, fields ...string) (int64, bool) {
	v, ok, _ := unstructured.NestedFieldNoCopy(m, fields...)
	switch x := v.(type) {
	case int64:
		return x, ok
	case float64:
		return int64(x), ok
	}
	return 0, false
}

func envOfSpec(spec map[string]interface{}, name string) (string, bool) {
	containers, _, _ := unstructured.NestedSlice(spec, "containers")
	for _, c := range containers {
		cm, _ := c.(map[string]interface{})
		env, _ := cm["env"].([]interface{})
		for _, e := range env {
			em, _ := e.(map[string]interface{})
			if em["name"] == name {
				v, _ := em["value"].(string)
				return v, true
			}
		}
	}
	return "", false
}

// CheckIndexedFallback is the partitioned job without Volcano (§4.5 "Native
// fallback", A2.10): one Indexed Job of workers+1 that succeeds when index 0
// (the manager) does, with the run's podFailurePolicy and no Volcano in it.
func CheckIndexedFallback(t *RunTemplate, workers int) []string {
	var p []string
	if t.Kind != "IndexedJob" {
		return []string{fmt.Sprintf("kind %s, want IndexedJob", t.Kind)}
	}
	m := t.Manifest.Object
	if mode := str(m, "spec", "completionMode"); mode != "Indexed" {
		p = append(p, fmt.Sprintf("completionMode %q, want Indexed", mode))
	}
	for _, f := range []string{"completions", "parallelism"} {
		if n, _ := intAt(m, "spec", f); n != int64(workers+1) {
			p = append(p, fmt.Sprintf("%s %d, want workers+1 = %d", f, n, workers+1))
		}
	}
	rules, _, _ := unstructured.NestedSlice(m, "spec", "successPolicy", "rules")
	managerEnds := false
	for _, r := range rules {
		rm, _ := r.(map[string]interface{})
		managerEnds = managerEnds || rm["succeededIndexes"] == "0"
	}
	if !managerEnds {
		p = append(p, "no successPolicy rule succeededIndexes \"0\": the workers would outlive the manager")
	}
	if _, ok, _ := unstructured.NestedSlice(m, "spec", "podFailurePolicy", "rules"); !ok {
		p = append(p, "no podFailurePolicy: an invalid run would be retried")
	}
	spec, _, _ := unstructured.NestedMap(m, "spec", "template", "spec")
	if s := str(spec, "schedulerName"); s == VolcanoScheduler {
		p = append(p, "the pods still name schedulerName volcano")
	}
	if role, _ := envOfSpec(spec, "ADHAR_BATCH_ROLE"); role != "indexed" {
		p = append(p, fmt.Sprintf("ADHAR_BATCH_ROLE %q, want indexed (§5.10: index 0 is the manager)", role))
	}
	return p
}

// policyEvents are a Volcano LifecyclePolicy's events: it takes `event` or the
// list `events`, and the composition renders the list.
func policyEvents(pm map[string]interface{}) map[string]bool {
	ev := map[string]bool{}
	events, _ := pm["events"].([]interface{})
	for _, e := range events {
		s, _ := e.(string)
		ev[s] = true
	}
	if e, _ := pm["event"].(string); e != "" {
		ev[e] = true
	}
	return ev
}

// GangTemplate is what CheckVolcanoGang read from a VolcanoJob template.
type GangTemplate struct {
	MinAvailable int64
	Queue        string
	Tasks        map[string]int64 // task name → replicas
}

// CheckVolcanoGang is the partitioned job on Volcano (§4.5, FU-3): one gang of
// a manager and `workers` workers, all or nothing, on the namespace's leaf;
// exit codes 2/3/4/6/7 end the job, an evicted or failed pod restarts the
// gang (Spring Batch then reruns only unfinished partitions), and the manager
// completing completes the job.
func CheckVolcanoGang(t *RunTemplate, workers int, leaf string) (GangTemplate, []string) {
	var p []string
	g := GangTemplate{Tasks: map[string]int64{}}
	if t.Kind != "VolcanoJob" {
		return g, []string{fmt.Sprintf("kind %s, want VolcanoJob", t.Kind)}
	}
	m := t.Manifest.Object
	g.MinAvailable, _ = intAt(m, "spec", "minAvailable")
	g.Queue = str(m, "spec", "queue")
	if g.MinAvailable != int64(workers+1) {
		p = append(p, fmt.Sprintf("minAvailable %d, want 1 manager + %d workers", g.MinAvailable, workers))
	}
	if g.Queue != "" && g.Queue != leaf {
		p = append(p, fmt.Sprintf("queue %s, want the namespace's leaf %s", g.Queue, leaf))
	}
	if s := str(m, "spec", "schedulerName"); s != VolcanoScheduler {
		p = append(p, fmt.Sprintf("schedulerName %q, want volcano", s))
	}
	if c := str(m, "spec", "priorityClassName"); c != ClassVolcano && c != ClassVolcanoCritical {
		p = append(p, fmt.Sprintf("priorityClassName %q, want %s or %s (DEC-1)", c, ClassVolcano, ClassVolcanoCritical))
	}
	plugins, _, _ := unstructured.NestedMap(m, "spec", "plugins")
	for name := range plugins {
		if name != "env" {
			p = append(p, "plugin "+name+": a Volcano run uses env only (§4.5)")
		}
	}
	ends, restarts := map[int64]bool{}, false
	policies, _, _ := unstructured.NestedSlice(m, "spec", "policies")
	for _, pol := range policies {
		pm, _ := pol.(map[string]interface{})
		action, _ := pm["action"].(string)
		if code, ok := intAt(pm, "exitCode"); ok && action == "TerminateJob" {
			ends[code] = true
		}
		ev := policyEvents(pm)
		restarts = restarts || (action == "RestartJob" && ev["PodEvicted"] && ev["PodFailed"])
	}
	for _, code := range []int64{2, 3, 4, 6, 7} {
		if !ends[code] {
			p = append(p, fmt.Sprintf("exit %d does not TerminateJob (§5.7: it is not retryable)", code))
		}
	}
	if !restarts {
		p = append(p, "no policy restarts the gang on PodEvicted and PodFailed (A2.2)")
	}
	tasks, _, _ := unstructured.NestedSlice(m, "spec", "tasks")
	managerCompletes := false
	for _, task := range tasks {
		tm, _ := task.(map[string]interface{})
		name, _ := tm["name"].(string)
		replicas, _ := intAt(tm, "replicas")
		g.Tasks[name] = replicas
		spec, _, _ := unstructured.NestedMap(tm, "template", "spec")
		if rp := str(spec, "restartPolicy"); rp != "Never" {
			p = append(p, fmt.Sprintf("task %s restartPolicy %q: the gang restarts, never a container in place", name, rp))
		}
		role, _ := envOfSpec(spec, "ADHAR_BATCH_ROLE")
		if role != name {
			p = append(p, fmt.Sprintf("task %s runs ADHAR_BATCH_ROLE=%q (§5.10)", name, role))
		}
		if name != "manager" {
			continue
		}
		tp, _, _ := unstructured.NestedSlice(tm, "policies")
		for _, pol := range tp {
			pm, _ := pol.(map[string]interface{})
			managerCompletes = managerCompletes || (policyEvents(pm)["TaskCompleted"] && pm["action"] == "CompleteJob")
		}
	}
	if g.Tasks["manager"] != 1 || g.Tasks["worker"] != int64(workers) || len(g.Tasks) != 2 {
		p = append(p, fmt.Sprintf("tasks %v, want manager 1 and worker %d", g.Tasks, workers))
	}
	if !managerCompletes {
		p = append(p, "the manager task has no TaskCompleted → CompleteJob policy (A2.3)")
	}
	return g, p
}

/* ─────────────── A2.2: partitions rerun only when unfinished ─────────────── */

// PartitionReport is how a partitioned instance's partitions ran over its
// executions. Worker StepExecutions are named `<step>:<partition>` by Spring
// Batch's StepExecutionSplitter, and a restart creates StepExecutions only for
// the partitions that did not complete [S67].
type PartitionReport struct {
	// All are the partitions seen; Completed is each one's COMPLETED count.
	All       []string
	Completed map[string]int
	// Reran are partitions that ran again in a later execution after an
	// earlier one COMPLETED them — what A2.2 forbids.
	Reran []string
	// Unfinished are partitions an execution left un-COMPLETED; Resumed are
	// the ones a later execution ran.
	Unfinished, Resumed []string
}

// Partitions reads the ledger's worker steps.
func (l Ledger) Partitions() PartitionReport {
	r := PartitionReport{Completed: map[string]int{}}
	byPart := map[string][]StepExecution{}
	for _, s := range l.Steps {
		if strings.Contains(s.Name, ":") {
			byPart[s.Name] = append(byPart[s.Name], s)
		}
	}
	names := make([]string, 0, len(byPart))
	for n := range byPart {
		names = append(names, n)
	}
	sort.Strings(names)
	r.All = names
	for _, n := range names {
		steps := byPart[n]
		sort.Slice(steps, func(i, j int) bool { return steps[i].Execution < steps[j].Execution })
		done, open := false, false
		for _, s := range steps {
			if done {
				r.Reran = append(r.Reran, fmt.Sprintf("%s (execution %d after it COMPLETED)", n, s.Execution))
				break
			}
			if open {
				r.Resumed = append(r.Resumed, n)
				open = false
			}
			if s.Status == "COMPLETED" {
				done = true
				r.Completed[n]++
			} else {
				open = true
				r.Unfinished = append(r.Unfinished, n)
			}
		}
	}
	return r
}

// CheckPartitioned is A2.2's verdict for an instance of `items` rows: exactly
// once overall (one COMPLETED execution, the newest, every row written once),
// every partition COMPLETED exactly once, none run again after it completed,
// and at least `minPartitions` partitions.
func (l Ledger) CheckPartitioned(items int64, minPartitions int) error {
	if err := l.CheckExactlyOnce(items); err != nil {
		return err
	}
	r := l.Partitions()
	if len(r.All) < minPartitions {
		return fmt.Errorf("%d partitions, want at least %d: is the job partitioned (worker steps are `<step>:<partition>`)?", len(r.All), minPartitions)
	}
	if len(r.Reran) > 0 {
		return fmt.Errorf("partitions ran again after they completed: %v", r.Reran)
	}
	for _, n := range r.All {
		if r.Completed[n] != 1 {
			return fmt.Errorf("partition %s COMPLETED %d times, want once", n, r.Completed[n])
		}
	}
	return nil
}
