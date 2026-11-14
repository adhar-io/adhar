package autoscaler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
)

// What Volcano writes, read the way the pod rule reads a pod (design 0025
// §4.7, A2.6): a single-mode run on Volcano still buys its node, a gang buys
// only for a capacity shortage a new worker answers, and a gang is sized by
// its members and the room the cluster already has.

// gangMember is a waiting pod of the Volcano Job whose gang is `gang`: the
// controller reference, group-name and queue annotations Volcano's job
// controller stamps (v1.16.0 job_controller_util.go createJobPod).
func gangMember(name, gang string, cpuMilli, memGi int64, message string) corev1.Pod {
	p := pending(name, message)
	p.Namespace = "acme-payments-prod"
	p.Spec.SchedulerName = volcanoSchedulerName
	p.Labels = map[string]string{workloadLabel: "batch", scaleUpLabel: "true", environmentLabel: "prod"}
	p.Annotations = map[string]string{
		volcanoGroupNameAnnotation: gang,
		volcanoQueueAnnotation:     "acme-payments-prod",
	}
	controller := true
	p.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch.volcano.sh/v1alpha1", Kind: "Job", Name: gang, Controller: &controller,
	}}
	p.Spec.Containers[0].Resources.Requests = cpuMem(cpuMilli, memGi)
	return p
}

// singleModeRun is a prod `scaleUp: true` run on Volcano in single mode: a
// batch/v1 Job's pod with only schedulerName and the queue annotation changed
// (§6.4), and the PodGroup Volcano's PodGroup controller made for it, which
// has no labels and the native Job as its owner (pg_controller_handler.go).
func singleModeRun(message string) (corev1.Pod, Gang) {
	p := pending("settle-29000000-abc", message)
	p.Namespace = "acme-payments-prod"
	p.Spec.SchedulerName = volcanoSchedulerName
	p.Labels = map[string]string{workloadLabel: "batch", scaleUpLabel: "true", environmentLabel: "prod"}
	p.Annotations = map[string]string{
		volcanoGroupNameAnnotation: "podgroup-6f1d",
		volcanoQueueAnnotation:     "acme-payments-prod",
	}
	controller := true
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "settle-29000000", Controller: &controller}}
	g := Gang{
		Namespace: p.Namespace, Name: "podgroup-6f1d", Phase: gangPhaseInqueue, Unschedulable: true,
		Message:   "1/1 tasks in gang unschedulable: " + message,
		MinMember: 1, MinResources: cpuMem(500, 1), Queue: "acme-payments-prod",
	}
	return p, g
}

const nodeShortOfCPU = "Node: 0/3 nodes are unavailable: 1 node(s) had untolerated taint, 2 Insufficient cpu."

func TestASingleModeVolcanoRunBuysAWorkerThroughThePodRule(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*corev1.Pod, *Snapshot)
		want   Action
	}{
		// The verifier's B1: the same run buys a worker natively, so it buys one
		// on Volcano.
		{"an opted-in prod run short of CPU", nil, ActionScaleUp},
		{"a run that did not opt in", func(p *corev1.Pod, _ *Snapshot) { delete(p.Labels, scaleUpLabel) }, ActionNone},
		// Volcano says "pod group is not ready" and nothing about nodes when the
		// run waits on its queue (v1.16.0 RecordJobStatusEvent with no fit error).
		{"held by its queue, not by the nodes", func(p *corev1.Pod, _ *Snapshot) {
			p.Status.Conditions[0].Message = "pod group is not ready, 1 Pending, 1 minAvailable; Pending: 1 Unschedulable"
		}, ActionNone},
		// §4.7: a queue at capability never scales, whatever the pod's message.
		{"its queue is at capability", func(_ *corev1.Pod, s *Snapshot) {
			q := s.Queues["acme-payments-prod"]
			q.Allocated = cpuMem(8000, 4)
			s.Queues["acme-payments-prod"] = q
		}, ActionNone},
		{"its queue was not read", func(_ *corev1.Pod, s *Snapshot) { s.Queues = nil }, ActionNone},
		{"the queue binding put it in a dev leaf", func(p *corev1.Pod, _ *Snapshot) {
			p.Annotations[volcanoQueueAnnotation] = "acme-payments-dev"
		}, ActionNone},
		{"it names no queue, and default was not read", func(p *corev1.Pod, _ *Snapshot) {
			delete(p.Annotations, volcanoQueueAnnotation)
		}, ActionNone},
		// A Volcano Job's member is its gang's to count: with no gang read, the
		// pod alone buys nothing.
		{"a Volcano Job's member", func(p *corev1.Pod, s *Snapshot) {
			controller := true
			p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch.volcano.sh/v1alpha1", Kind: "Job", Name: "settle", Controller: &controller}}
			s.Gangs = nil
		}, ActionNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, g := singleModeRun(nodeShortOfCPU)
			s := gangSnapshot(g)
			if c.mutate != nil {
				c.mutate(&p, &s)
			}
			s.Pods = append(s.Pods, p)
			d := Decide(s)
			if d.Action != c.want {
				t.Fatalf("got %s by %d (%s), want %s", d.Action, d.ScaleUpBy, d.Reason, c.want)
			}
			if c.want == ActionScaleUp && (d.ScaleUpBy != 1 || d.PendingPods != 1 || d.PendingGangs != 0) {
				t.Fatalf("got %s by %d for %d pod(s), %d gang(s) (%s); want one worker for the pod, the PodGroup counted nowhere",
					d.Action, d.ScaleUpBy, d.PendingPods, d.PendingGangs, d.Reason)
			}
		})
	}
}

// A gang keeps its Unschedulable condition for as long as it waits, so a gang
// held by anything a node does not answer would otherwise buy a worker every
// cooldown until maxWorkers.
func TestAGangHeldForAnotherReasonBuysNothing(t *testing.T) {
	const taint = "Node: 0/3 nodes are unavailable: 3 node(s) had untolerated taint {dedicated: gpu}."
	cases := []struct {
		name    string
		message string
		members func() []corev1.Pod
		want    Action
	}{
		{"short of CPU, said by the gang", gangShortOfCPU, nil, ActionScaleUp},
		{"held by a taint no worker lifts", "2/2 tasks in gang unschedulable: pod group is not ready, 2 Pending, 2 minAvailable; Pending: 2 Unschedulable. " + taint,
			func() []corev1.Pod {
				return []corev1.Pod{gangMember("m-0", "g", 1500, 2, taint), gangMember("w-0", "g", 1500, 2, taint)}
			}, ActionNone},
		// The gang's message carries ONE task's node summary; a member says its own.
		{"short of CPU, said by one member", "2/2 tasks in gang unschedulable: pod group is not ready, 2 Pending, 2 minAvailable; Pending: 2 Unschedulable. " + taint,
			func() []corev1.Pod {
				return []corev1.Pod{gangMember("m-0", "g", 1500, 2, taint), gangMember("w-0", "g", 1500, 2, nodeShortOfCPU)}
			}, ActionScaleUp},
		{"waiting on its queue", "1/2 tasks in gang unschedulable: pod group is not ready, 2 Inqueue, 2 minAvailable", nil, ActionNone},
		{"a member needs a label no worker has", gangShortOfCPU, func() []corev1.Pod {
			gpu := gangMember("w-0", "g", 1500, 2, nodeShortOfCPU)
			gpu.Spec.NodeSelector = map[string]string{"accelerator": "nvidia"}
			return []corev1.Pod{gangMember("m-0", "g", 1500, 2, nodeShortOfCPU), gpu}
		}, ActionNone},
		{"a member pinned to a node by name", gangShortOfCPU, func() []corev1.Pod {
			pinned := gangMember("w-0", "g", 1500, 2, nodeShortOfCPU)
			pinned.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"w9"}}},
				}}},
			}}
			return []corev1.Pod{gangMember("m-0", "g", 1500, 2, nodeShortOfCPU), pinned}
		}, ActionNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := prodGang("g", 2, 1500, 2)
			g.Phase, g.Message = gangPhaseInqueue, c.message
			s := gangSnapshot(g)
			if c.members != nil {
				s.Pods = append(s.Pods, c.members()...)
			}
			if d := Decide(s); d.Action != c.want {
				t.Fatalf("got %s by %d (%s), want %s", d.Action, d.ScaleUpBy, d.Reason, c.want)
			}
		})
	}
}

// unboundedGangSnapshot is gangSnapshot with room for eight more workers and a
// prod queue that caps nothing, so only the sizing decides.
func unboundedGangSnapshot(g Gang) Snapshot {
	s := gangSnapshot(g)
	s.Spec = (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 10}).WithDefaults()
	s.Queues["acme-payments-prod"] = GangQueue{Name: "acme-payments-prod"}
	s.Queues["adhar-batch"] = GangQueue{Name: "adhar-batch"}
	return s
}

// gangOf is an Inqueue gang whose members are the given pods, with totals the
// PodGroup would carry for them.
func gangOf(name string, members []corev1.Pod) Gang {
	var cpu, mem int64
	for _, p := range members {
		c, m := podRequests(p)
		cpu += c
		mem += m
	}
	g := prodGang(name, int32(len(members)), 0, 0)
	g.Phase = gangPhaseInqueue
	g.MinResources = corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(cpu, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(mem, resource.BinarySI),
	}
	return g
}

// A PodGroup carries only totals; its member pods say what each member is.
func TestAGangIsSizedByItsMembersNotTheirAverage(t *testing.T) {
	// Three 1800m members and three 100m ones. Their average (950m) packs three
	// to a 3400m worker and says two workers; two 1800m members do not share
	// one, so two workers leave a member that never runs and the gang with it.
	var members []corev1.Pod
	for i, c := range []int64{1800, 1800, 1800, 100, 100, 100} {
		members = append(members, gangMember("settlement-"+string(rune('a'+i)), "g", c, 1, nodeShortOfCPU))
	}
	s := unboundedGangSnapshot(gangOf("g", members))
	s.Pods = append(s.Pods, members...)
	if d := Decide(s); d.Action != ActionScaleUp || d.ScaleUpBy != 3 {
		t.Fatalf("got %s by %d (%s), want three workers, one per 1800m member", d.Action, d.ScaleUpBy, d.Reason)
	}

	// The verifier's B3: a 6000m manager on 4000m workers. The average (1600m)
	// says three workers; the manager fits none, so the gang never runs.
	members = []corev1.Pod{gangMember("settlement-manager-0", "g", 6000, 2, nodeShortOfCPU)}
	for _, n := range []string{"0", "1", "2", "3"} {
		members = append(members, gangMember("settlement-worker-"+n, "g", 500, 1, nodeShortOfCPU))
	}
	s = unboundedGangSnapshot(gangOf("g", members))
	s.Pods = append(s.Pods, members...)
	if d := Decide(s); d.Action != ActionNone {
		t.Fatalf("got %s by %d (%s) for a gang whose manager no worker can hold", d.Action, d.ScaleUpBy, d.Reason)
	}
}

// Room the workers already have takes members before a new worker does.
func TestAGangIsCreditedTheRoomWorkersAlreadyHave(t *testing.T) {
	twoHalfFull := func(s *Snapshot) {
		s.Pods = []corev1.Pod{placed("apps", "a", "w1", 2000, 2), placed("apps", "b", "w2", 2000, 2)}
	}
	cases := []struct {
		name   string
		gangs  []Gang
		mutate func(*Snapshot)
		by     int32
	}{
		// The verifier's B4: 4 × 2000m, two workers with 2000m free each. Two
		// members fit what is there, two need a worker each.
		{"half the gang fits the room there is", []Gang{prodGang("g", 4, 2000, 1)}, twoHalfFull, 2},
		{"the same, sized from its member pods", []Gang{gangOf("g", []corev1.Pod{
			gangMember("a", "g", 2000, 1, nodeShortOfCPU), gangMember("b", "g", 2000, 1, nodeShortOfCPU),
			gangMember("c", "g", 2000, 1, nodeShortOfCPU), gangMember("d", "g", 2000, 1, nodeShortOfCPU),
		})}, func(s *Snapshot) {
			twoHalfFull(s)
			for _, n := range []string{"a", "b", "c", "d"} {
				s.Pods = append(s.Pods, gangMember(n, "g", 2000, 1, nodeShortOfCPU))
			}
		}, 2},
		// Volcano says it does not fit and the requests say it does: the room is
		// not what it looks like, and one worker is the smallest answer.
		{"the whole gang fits the room by requests", []Gang{prodGang("g", 2, 1500, 1)}, func(s *Snapshot) { s.Pods = nil }, 1},
		// A tainted worker's room is not counted: w1 is empty but NoSchedule, w2
		// has 2000m, so one member goes there and three need workers.
		{"a tainted worker's room", []Gang{prodGang("g", 4, 2000, 1)}, func(s *Snapshot) {
			s.Nodes[1].Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}
			s.Pods = []corev1.Pod{placed("apps", "b", "w2", 2000, 2)}
		}, 3},
		// Room is credited once: gang a takes w1's 4000m, gang b gets none of it.
		{"two gangs and the room for one", []Gang{prodGang("a", 2, 2000, 1), prodGang("b", 2, 2000, 1)}, func(s *Snapshot) {
			s.Pods = []corev1.Pod{placed("apps", "busy", "w2", 3400, 7)}
		}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := unboundedGangSnapshot(c.gangs[0])
			s.Gangs = c.gangs
			c.mutate(&s)
			if d := Decide(s); d.Action != ActionScaleUp || d.ScaleUpBy != c.by {
				t.Fatalf("got %s by %d (%s), want %d worker(s)", d.Action, d.ScaleUpBy, d.Reason, c.by)
			}
		})
	}
}
