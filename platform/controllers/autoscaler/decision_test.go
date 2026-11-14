package autoscaler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

var testNow = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

// testWorkerCPUMilli / testWorkerMemGi are the shape every test node has: one
// homogeneous node group, which is what the autoscaler assumes.
const (
	testWorkerCPUMilli = 4000
	testWorkerMemGi    = 8
)

// worker builds a schedulable worker node with the standard allocatable
// capacity; workerSized varies it where a test needs a different shape.
func worker(name string) corev1.Node {
	return workerSized(name, testWorkerCPUMilli, testWorkerMemGi)
}

func workerSized(name string, cpuMilli int64, memGi int64) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memGi*1024*1024*1024, resource.BinarySI),
			},
		},
	}
}

func controlPlane() corev1.Node {
	n := worker("cp")
	n.Labels = map[string]string{controlPlaneLabel: ""}
	return n
}

// placed builds a running pod owned by a ReplicaSet on a node.
func placed(ns, name, node string, cpuMilli int64, memGi int64) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       ns,
			Name:            name,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs"}},
		},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(memGi*1024*1024*1024, resource.BinarySI),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// pending builds an unschedulable pod carrying the scheduler's message.
func pending(name, message string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(1024*1024*1024, resource.BinarySI),
			}},
		}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  corev1.PodReasonUnschedulable,
				Message: message,
			}},
		},
	}
}

func enabledSpec() v1alpha1.AutoscalingSpec {
	s := &v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 5}
	return s.WithDefaults()
}

func TestScaleUpOnPendingCapacityPods(t *testing.T) {
	d := Decide(Snapshot{
		Spec:  enabledSpec(),
		Nodes: []corev1.Node{controlPlane(), worker("w1")},
		Pods:  []corev1.Pod{pending("api-1", "0/2 nodes are available: 2 Insufficient cpu.")},
		Now:   testNow,
	})
	if d.Action != ActionScaleUp {
		t.Fatalf("expected ScaleUp, got %s (%s)", d.Action, d.Reason)
	}
	if d.Workers != 1 {
		t.Fatalf("expected 1 worker counted (control plane excluded), got %d", d.Workers)
	}
}

func TestScaleUpIgnoresNonCapacityUnschedulable(t *testing.T) {
	// A pod blocked by taints or affinity is not a capacity problem: buying a
	// node would not schedule it.
	for _, msg := range []string{
		"0/3 nodes are available: 3 node(s) had untolerated taint {dedicated: gpu}.",
		"0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.",
		"0/3 nodes are available: 3 pod has unbound immediate PersistentVolumeClaims.",
	} {
		d := Decide(Snapshot{
			Spec:  enabledSpec(),
			Nodes: []corev1.Node{worker("w1")},
			Pods:  []corev1.Pod{pending("x", msg)},
			Now:   testNow,
		})
		if d.Action == ActionScaleUp {
			t.Fatalf("scaled up for a non-capacity message: %q", msg)
		}
	}
}

func TestScaleUpBlockedByMaxWorkers(t *testing.T) {
	spec := (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 2}).WithDefaults()
	d := Decide(Snapshot{
		Spec:  spec,
		Nodes: []corev1.Node{worker("w1"), worker("w2")},
		Pods:  []corev1.Pod{pending("x", "0/2 nodes are available: 2 Insufficient memory.")},
		Now:   testNow,
	})
	if d.Action != ActionNone {
		t.Fatalf("expected no action at maxWorkers, got %s", d.Action)
	}
}

func TestScaleUpBlockedByCooldown(t *testing.T) {
	spec := enabledSpec()
	last := metav1.NewTime(testNow.Add(-1 * time.Minute)) // cooldown is 3m
	d := Decide(Snapshot{
		Spec:   spec,
		Status: v1alpha1.AutoscalingStatus{LastScaleUp: &last},
		Nodes:  []corev1.Node{worker("w1")},
		Pods:   []corev1.Pod{pending("x", "0/1 nodes are available: 1 Too many pods.")},
		Now:    testNow,
	})
	if d.Action != ActionNone {
		t.Fatalf("expected cooldown to block the scale-up, got %s", d.Action)
	}
	// Past the cooldown the same snapshot must act.
	old := metav1.NewTime(testNow.Add(-5 * time.Minute))
	d = Decide(Snapshot{
		Spec:   spec,
		Status: v1alpha1.AutoscalingStatus{LastScaleUp: &old},
		Nodes:  []corev1.Node{worker("w1")},
		Pods:   []corev1.Pod{pending("x", "0/1 nodes are available: 1 Too many pods.")},
		Now:    testNow,
	})
	if d.Action != ActionScaleUp {
		t.Fatalf("expected ScaleUp after the cooldown, got %s (%s)", d.Action, d.Reason)
	}
}

func TestScaleUpSkipsPodPinnedToAbsentLabels(t *testing.T) {
	p := pending("gpu", "0/1 nodes are available: 1 Insufficient cpu.")
	p.Spec.NodeSelector = map[string]string{"accelerator": "nvidia"}
	d := Decide(Snapshot{
		Spec:  enabledSpec(),
		Nodes: []corev1.Node{worker("w1")},
		Pods:  []corev1.Pod{p},
		Now:   testNow,
	})
	if d.Action != ActionNone {
		t.Fatalf("a new identical worker cannot carry the label; expected no action, got %s", d.Action)
	}
}

func TestScaleUpSkipsPodLargerThanAnyWorker(t *testing.T) {
	p := pending("huge", "0/1 nodes are available: 1 Insufficient cpu.")
	p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(32000, resource.DecimalSI)
	d := Decide(Snapshot{
		Spec:  enabledSpec(),
		Nodes: []corev1.Node{worker("w1")},
		Pods:  []corev1.Pod{p},
		Now:   testNow,
	})
	if d.Action != ActionNone {
		t.Fatalf("pod does not fit a worker of this shape; expected no action, got %s", d.Action)
	}
}

// idleSnapshot: three workers, one nearly empty, well under the threshold and
// idle for longer than the delay.
func idleSnapshot() Snapshot {
	since := metav1.NewTime(testNow.Add(-30 * time.Minute))
	return Snapshot{
		Spec:   (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 10}).WithDefaults(),
		Status: v1alpha1.AutoscalingStatus{UnderutilizedSince: &since},
		Nodes:  []corev1.Node{worker("w1"), worker("w2"), worker("w3")},
		Pods: []corev1.Pod{
			placed("apps", "a", "w1", 800, 1),
			placed("apps", "b", "w2", 400, 1),
			placed("apps", "c", "w3", 100, 1),
		},
		Now: testNow,
	}
}

func TestScaleDownPicksEmptiestWorker(t *testing.T) {
	d := Decide(idleSnapshot())
	if d.Action != ActionScaleDown {
		t.Fatalf("expected ScaleDown, got %s (%s)", d.Action, d.Reason)
	}
	if d.Node != "w3" {
		t.Fatalf("expected the emptiest worker w3, got %s", d.Node)
	}
}

func TestScaleDownSkipsNodeWithReadWriteOnceVolume(t *testing.T) {
	s := idleSnapshot()
	p := placed("data", "db-0", "w3", 100, 1)
	p.Spec.Volumes = []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "db-data"}},
	}}
	s.Pods = append(s.Pods, p)
	s.RWOClaims = map[string]bool{"data/db-data": true}

	d := Decide(s)
	if d.Action != ActionScaleDown {
		t.Fatalf("expected ScaleDown of another node, got %s (%s)", d.Action, d.Reason)
	}
	if d.Node == "w3" {
		t.Fatal("w3 hosts a ReadWriteOnce volume and must not be retired")
	}
}

func TestScaleDownSkipsNodeWithUnmanagedPod(t *testing.T) {
	s := idleSnapshot()
	bare := placed("ops", "debug", "w3", 50, 1)
	bare.OwnerReferences = nil
	s.Pods = append(s.Pods, bare)

	d := Decide(s)
	if d.Node == "w3" {
		t.Fatal("w3 hosts a pod no controller would recreate and must not be retired")
	}
}

func TestScaleDownIgnoresDaemonSetPodsWhenRankingNodes(t *testing.T) {
	s := idleSnapshot()
	ds := placed("kube-system", "agent", "w3", 2000, 4)
	ds.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: "agent"}}
	s.Pods = append(s.Pods, ds)

	d := Decide(s)
	if d.Node != "w3" {
		t.Fatalf("DaemonSet load must not make w3 look busy; got %s", d.Node)
	}
}

func TestScaleDownBlockedByMinWorkers(t *testing.T) {
	s := idleSnapshot()
	s.Spec = (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 3, MaxWorkers: 10}).WithDefaults()
	d := Decide(s)
	if d.Action != ActionNone {
		t.Fatalf("expected no action at minWorkers, got %s → %s", d.Action, d.Node)
	}
}

func TestScaleDownBlockedUntilDelayElapses(t *testing.T) {
	s := idleSnapshot()
	// Idle clock started a minute ago; the default delay is 10m.
	since := metav1.NewTime(testNow.Add(-1 * time.Minute))
	s.Status.UnderutilizedSince = &since
	d := Decide(s)
	if d.Action != ActionNone {
		t.Fatalf("expected the scale-down delay to hold, got %s", d.Action)
	}
	if d.UnderutilizedSince == nil || !d.UnderutilizedSince.Equal(since.Time) {
		t.Fatal("the idle clock must be carried forward, not restarted")
	}
}

func TestScaleDownBlockedAfterRecentScaleUp(t *testing.T) {
	s := idleSnapshot()
	last := metav1.NewTime(testNow.Add(-2 * time.Minute))
	s.Status.LastScaleUp = &last
	d := Decide(s)
	if d.Action != ActionNone {
		t.Fatalf("a node added 2 minutes ago must not be removed, got %s", d.Action)
	}
}

func TestBusyClusterResetsIdleClock(t *testing.T) {
	s := idleSnapshot()
	// Fill w1 so cluster CPU is above 50%.
	s.Pods = append(s.Pods, placed("apps", "hog", "w1", 6000, 6))
	d := Decide(s)
	if d.Action != ActionNone {
		t.Fatalf("expected no action on a busy cluster, got %s", d.Action)
	}
	if d.UnderutilizedSince != nil {
		t.Fatal("the idle clock must reset once the cluster is busy")
	}
}

func TestCordonedAndControlPlaneNodesAreNotWorkers(t *testing.T) {
	cordoned := worker("w2")
	cordoned.Spec.Unschedulable = true
	d := Decide(Snapshot{
		Spec:  enabledSpec(),
		Nodes: []corev1.Node{controlPlane(), worker("w1"), cordoned},
		Now:   testNow,
	})
	if d.Workers != 1 {
		t.Fatalf("expected 1 schedulable worker, got %d", d.Workers)
	}
}

// TestScaleUpOnVolumeAttachLimit pins the DigitalOcean reality that drove this
// marker: the platform runs out of per-droplet block-device slots (7 on DO)
// before it runs out of CPU or memory, and the scheduler reports that in a
// message that mentions volumes. Another worker brings more slots, so this
// must scale up — while a genuinely unschedulable volume (zone affinity) must
// not, because another identical droplet would not help.
func TestScaleUpOnVolumeAttachLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		wantUp  bool
	}{
		{
			name: "attach limit is capacity",
			message: "0/4 nodes are available: 1 node(s) had untolerated taint(s), " +
				"3 node(s) exceed max volume count. preemption: 0/4 nodes are available.",
			wantUp: true,
		},
		{
			name:    "zone affinity is not capacity",
			message: "0/4 nodes are available: 4 node(s) had volume node affinity conflict.",
			wantUp:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(Snapshot{
				Spec:  enabledSpec(),
				Nodes: []corev1.Node{controlPlane(), worker("w1")},
				Pods:  []corev1.Pod{pending("db-0", tc.message)},
				Now:   testNow,
			})
			if got := d.Action == ActionScaleUp; got != tc.wantUp {
				t.Fatalf("message %q: action=%s wantScaleUp=%v (%s)", tc.message, d.Action, tc.wantUp, d.Reason)
			}
		})
	}
}

// Scaling one worker at a time is far too slow when the whole catalogue lands
// at once. The first wall is the CSI attach limit (7 volumes per droplet on
// DigitalOcean), and clearing ~50 blocked volumes one 7-slot worker per
// cooldown costs half an hour during which Applications sit Degraded. When the
// blocked pods are volume-limited, the number of workers needed is arithmetic,
// so the decision buys them in one step.
func TestScaleUpBurstsForVolumeLimitedPods(t *testing.T) {
	const volMsg = "0/3 nodes are available: 3 node(s) exceed max volume count."
	const cpuMsg = "0/3 nodes are available: 3 Insufficient cpu."

	pendingPods := func(n int, msg string) []corev1.Pod {
		out := make([]corev1.Pod, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, pending(fmt.Sprintf("p%d", i), msg))
		}
		return out
	}

	tests := []struct {
		name           string
		pods           []corev1.Pod
		volumesPerNode int
		workers        int32
		maxWorkers     int32
		want           int32
	}{
		{
			name:           "one blocked volume needs one worker",
			pods:           pendingPods(1, volMsg),
			volumesPerNode: 7,
			workers:        3, maxWorkers: 10,
			want: 1,
		},
		{
			name:           "exactly one node's worth still needs one worker",
			pods:           pendingPods(7, volMsg),
			volumesPerNode: 7,
			workers:        3, maxWorkers: 10,
			want: 1,
		},
		{
			// The case that cost half an hour: 8 blocked volumes against 7
			// slots per node is two workers, not one-then-wait.
			name:           "eight blocked volumes round up to two workers",
			pods:           pendingPods(8, volMsg),
			volumesPerNode: 7,
			workers:        3, maxWorkers: 10,
			want: 2,
		},
		{
			name:           "a large backlog is capped by maxScaleUpBurst",
			pods:           pendingPods(60, volMsg),
			volumesPerNode: 7,
			workers:        3, maxWorkers: 20,
			want: maxScaleUpBurst,
		},
		{
			name:           "the burst never exceeds the room under maxWorkers",
			pods:           pendingPods(60, volMsg),
			volumesPerNode: 7,
			workers:        8, maxWorkers: 10,
			want: 2,
		},
		{
			name:           "cpu pressure stays at one worker per tick",
			pods:           pendingPods(20, cpuMsg),
			volumesPerNode: 7,
			workers:        3, maxWorkers: 10,
			want: 1,
		},
		{
			// With no advertised CSI limit we cannot say how much one worker
			// absorbs, so fall back to the conservative single step.
			name:           "an unknown attach limit falls back to one worker",
			pods:           pendingPods(20, volMsg),
			volumesPerNode: 0,
			workers:        3, maxWorkers: 10,
			want: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			names := make([]string, 0, len(tc.pods))
			for i := range tc.pods {
				names = append(names, tc.pods[i].Namespace+"/"+tc.pods[i].Name)
			}
			s := Snapshot{Pods: tc.pods, VolumesPerNode: tc.volumesPerNode}
			if got := scaleUpBurst(s, names, tc.workers, tc.maxWorkers); got != tc.want {
				t.Fatalf("scaleUpBurst() = %d, want %d", got, tc.want)
			}
		})
	}
}

// A burst must never be zero or negative: Decide only calls it when it has
// already decided to scale up, so "add nothing" would stall the cluster.
func TestScaleUpBurstIsAlwaysAtLeastOne(t *testing.T) {
	s := Snapshot{VolumesPerNode: 7}
	for _, room := range []struct{ workers, max int32 }{{9, 10}, {10, 10}, {11, 10}} {
		if got := scaleUpBurst(s, nil, room.workers, room.max); got < 1 {
			t.Fatalf("scaleUpBurst(workers=%d,max=%d) = %d, must be >= 1",
				room.workers, room.max, got)
		}
	}
}

func csiTainted(name string) corev1.Node {
	n := worker(name)
	n.Spec.Taints = []corev1.Taint{{Key: globals.NodeCSIStartupTaint, Effect: corev1.TaintEffectNoSchedule}}
	return n
}

func csiNode(name string, drivers int) storagev1.CSINode {
	c := storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{Name: name}}
	for i := 0; i < drivers; i++ {
		c.Spec.Drivers = append(c.Spec.Drivers, storagev1.CSINodeDriver{Name: fmt.Sprintf("driver-%d", i)})
	}
	return c
}

func TestStartupTaintClearsOnceCSIDriverRegisters(t *testing.T) {
	now := time.Now()
	nodes := []corev1.Node{csiTainted("w1"), csiTainted("w2"), worker("w3")}
	got := StartupTaintsToClear(nodes, []storagev1.CSINode{csiNode("w1", 1), csiNode("w2", 0), csiNode("w3", 1)}, now, 10*time.Minute)
	if len(got) != 1 || got[0] != "w1" {
		t.Fatalf("expected only the tainted node whose CSINode has a driver, got %v", got)
	}
}

func TestStartupTaintClearsAfterMaxWaitWithoutCSI(t *testing.T) {
	now := time.Now()
	fresh := csiTainted("fresh")
	fresh.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(now.Add(-time.Minute))}}
	stale := csiTainted("stale")
	stale.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(now.Add(-11 * time.Minute))}}
	notReady := csiTainted("notready")
	got := StartupTaintsToClear([]corev1.Node{fresh, stale, notReady}, nil, now, 10*time.Minute)
	if len(got) != 1 || got[0] != "stale" {
		t.Fatalf("no CSI driver at all: only a node Ready past the fallback should be released, got %v", got)
	}
}

func TestWithoutTaintKeepsOtherTaints(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "node.cloudprovider.kubernetes.io/uninitialized", Effect: corev1.TaintEffectNoSchedule},
		{Key: globals.NodeCSIStartupTaint, Effect: corev1.TaintEffectNoSchedule},
	}
	got := WithoutTaint(taints, globals.NodeCSIStartupTaint)
	if len(got) != 1 || got[0].Key != "node.cloudprovider.kubernetes.io/uninitialized" {
		t.Fatalf("expected the other taint to survive, got %v", got)
	}
}

// An HA workload that cannot place its last replica is waiting for a HOST, and
// the autoscaler is the thing that can give it one.
//
// Measured on a live Civo bring-up (2026-10-06): production mode installs Argo
// CD HA, whose Redis StatefulSet wants three replicas with REQUIRED anti-affinity
// on kubernetes.io/hostname; the config provisions two workers and the control
// plane is tainted. argo-cd-redis-ha-server-2 and its haproxy therefore stayed
// Pending indefinitely while maxWorkers was 5 and the autoscaler reported
// nothing to do, because "didn't match pod anti-affinity rules" was not a
// reason it recognised.
//
// It must stay precise: a new worker joins the EXISTING zones, so a zone spread
// is not something scaling fixes and must not trigger one.
func TestAntiAffinityPendingCountsOnlyWhenAHostWouldFixIt(t *testing.T) {
	const msg = "0/3 nodes are available: 1 node(s) had untolerated taint(s), " +
		"2 node(s) didn't match pod anti-affinity rules."

	withAntiAffinity := func(name, topologyKey string) corev1.Pod {
		p := pending(name, msg)
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				TopologyKey: topologyKey,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": name},
				},
			}},
		}}
		return p
	}

	workers := []corev1.Node{worker("w1"), worker("w2")}

	cases := []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{"hostname spread — another host is exactly the fix", withAntiAffinity("redis-ha-2", corev1.LabelHostname), true},
		{"zone spread — a new worker joins the same zones", withAntiAffinity("spread-z", "topology.kubernetes.io/zone"), false},
		{"the message without any required anti-affinity", pending("no-affinity", msg), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingForCapacity([]corev1.Pod{tc.pod}, workers, testNow)
			if tc.want && len(got) == 0 {
				t.Errorf("pod stays Pending forever: the autoscaler does not count it, so it never adds the host that would place it")
			}
			if !tc.want && len(got) != 0 {
				t.Errorf("counted %v, but a new worker cannot satisfy that constraint — scaling would burn money for nothing", got)
			}
		})
	}
}

// A scale-up is sized by what is pending, not fixed at one worker: a fresh
// cloud bring-up lands the whole platform at once, and one worker per cooldown
// starved it for twenty minutes (2026-10-17).
func TestScaleUpBurstsForCpuAndMemoryDemand(t *testing.T) {
	const cpuMsg = "0/3 nodes are available: 3 Insufficient cpu."
	pendingPods := func(n int) []corev1.Pod {
		out := make([]corev1.Pod, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, pending(fmt.Sprintf("p%d", i), cpuMsg)) // 500m / 1Gi each
		}
		return out
	}
	names := func(pods []corev1.Pod) []string {
		out := make([]string, 0, len(pods))
		for _, p := range pods {
			out = append(out, p.Namespace+"/"+p.Name)
		}
		return out
	}
	// Workers are 4000m / 8Gi; with the packing margin one takes 3400m / 6.8Gi.
	tests := []struct {
		name string
		pods int
		want int32
	}{
		{"one small pod needs one worker", 1, 1},
		{"six pods (3000m) still fit one", 6, 1},
		{"twelve pods (6000m) need two", 12, 2},
		{"forty pods (20000m) are capped at the burst maximum", 40, maxScaleUpBurst},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pods := pendingPods(tc.pods)
			s := Snapshot{Nodes: []corev1.Node{worker("w1"), worker("w2")}, Pods: pods, VolumesPerNode: 7}
			if got := scaleUpBurst(s, names(pods), 2, 10); got != tc.want {
				t.Fatalf("scaleUpBurst(%d pending pods) = %d, want %d", tc.pods, got, tc.want)
			}
		})
	}
	pods := pendingPods(40)
	s := Snapshot{Nodes: []corev1.Node{worker("w1"), worker("w2")}, Pods: pods, VolumesPerNode: 7}
	if got := scaleUpBurst(s, names(pods), 2, 4); got != 2 {
		t.Fatalf("burst exceeded the room to maxWorkers: %d", got)
	}
	s.VolumesPerNode = 0
	if got := scaleUpBurst(s, names(pods), 2, 10); got != maxScaleUpBurst {
		t.Fatalf("an unknown attach budget disabled demand sizing: %d", got)
	}
}

// --- Volcano gangs (design 0025 §4.7, acceptance A2.6) ----------------------

// gangShortOfCPU is the condition message Volcano v1.16.0's gang plugin writes
// for a gang a task of which no node had room for.
const gangShortOfCPU = "2/2 tasks in gang unschedulable: pod group is not ready, 2 Pending, 2 minAvailable; " +
	"Pending: 2 Unschedulable. Node: settlement-worker-0: 0/3 nodes are unavailable: 1 node(s) had untolerated taint, 2 Insufficient cpu."

// prodGang is a waiting gang of a prod run that opted into scale-up, which
// Volcano reports short of CPU: members × (cpuMilli, memGi) each, in queue
// acme-payments-prod.
func prodGang(name string, members int32, cpuMilli, memGi int64) Gang {
	return Gang{
		Namespace:         "acme-payments-prod",
		Name:              name,
		Phase:             gangPhasePending,
		Unschedulable:     true,
		Message:           gangShortOfCPU,
		OwnedByVolcanoJob: true,
		Labels: map[string]string{
			workloadLabel:        "batch",
			scaleUpLabel:         "true",
			environmentLabel:     "prod",
			"adhar.io/batch-job": name,
		},
		MinMember: members,
		MinResources: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(members)*cpuMilli, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(int64(members)*memGi*1024*1024*1024, resource.BinarySI),
		},
		Queue: "acme-payments-prod",
	}
}

func cpuMem(cpuMilli, memGi int64) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(memGi*1024*1024*1024, resource.BinarySI),
	}
}

// batchQueues is the §4.3 hierarchy: a root batch queue with room and the prod
// leaf under it, both empty.
func batchQueues() map[string]GangQueue {
	return map[string]GangQueue{
		"adhar-batch":        {Name: "adhar-batch", Parent: "root", Capability: cpuMem(12000, 24)},
		"acme-payments-prod": {Name: "acme-payments-prod", Parent: "adhar-batch", Capability: cpuMem(8000, 16)},
		"acme-payments-dev":  {Name: "acme-payments-dev", Parent: "adhar-batch", Capability: cpuMem(8000, 16)},
	}
}

// gangSnapshot: two 4000m/8Gi workers with 600m/1Gi left on each (busy, but
// under the 90% scale-up threshold), room for three more.
func gangSnapshot(gangs ...Gang) Snapshot {
	return Snapshot{
		Spec:   enabledSpec(), // maxWorkers 5
		Nodes:  []corev1.Node{controlPlane(), worker("w1"), worker("w2")},
		Pods:   []corev1.Pod{placed("apps", "busy-1", "w1", 3400, 7), placed("apps", "busy-2", "w2", 3400, 7)},
		Gangs:  gangs,
		Queues: batchQueues(),
		Now:    testNow,
	}
}

func TestAPendingGangBuysWorkersOnlyWhenItMay(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Snapshot)
		want   Action
		by     int32
	}{
		// A2.6: fits one new worker (2 × 1500m/2Gi; 3400m after the packing margin).
		{"an opted-in prod gang that fits one worker", nil, ActionScaleUp, 1},
		{"held in Inqueue rather than Pending", func(s *Snapshot) { s.Gangs[0].Phase = gangPhaseInqueue }, ActionScaleUp, 1},

		// A2.6: a dev gang never buys a node, opted in or not.
		{"a dev gang", func(s *Snapshot) {
			g := &s.Gangs[0]
			g.Namespace, g.Queue = "acme-payments-dev", "acme-payments-dev"
			g.Labels[environmentLabel] = "dev"
			delete(g.Labels, scaleUpLabel)
		}, ActionNone, 0},
		{"a dev gang carrying the opt-in anyway", func(s *Snapshot) {
			s.Gangs[0].Labels[environmentLabel] = "dev"
			s.Gangs[0].Queue = "acme-payments-dev"
		}, ActionNone, 0},
		{"a test gang carrying the opt-in anyway", func(s *Snapshot) { s.Gangs[0].Labels[environmentLabel] = "test" }, ActionNone, 0},
		// A hand-written Volcano Job with the opt-in and no environment label: the
		// queue binding still put it in its namespace's dev leaf.
		{"an opted-in gang in a dev leaf with no environment label", func(s *Snapshot) {
			delete(s.Gangs[0].Labels, environmentLabel)
			s.Gangs[0].Queue = "acme-payments-dev"
		}, ActionNone, 0},
		{"a prod gang that did not opt in", func(s *Snapshot) { delete(s.Gangs[0].Labels, scaleUpLabel) }, ActionNone, 0},
		{"the opt-in spelled wrong", func(s *Snapshot) { s.Gangs[0].Labels[scaleUpLabel] = "yes" }, ActionNone, 0},
		{"an opted-in gang that is not batch", func(s *Snapshot) { s.Gangs[0].Labels[workloadLabel] = "service" }, ActionNone, 0},

		// A2.6: any gang at queue capability.
		{"its queue is at capability", func(s *Snapshot) {
			q := s.Queues["acme-payments-prod"]
			q.Allocated = cpuMem(8000, 4)
			s.Queues["acme-payments-prod"] = q
		}, ActionNone, 0},
		{"its queue cannot admit the gang on top of what it holds", func(s *Snapshot) {
			q := s.Queues["acme-payments-prod"]
			q.Allocated = cpuMem(6000, 4) // + 3000m > 8000m
			s.Queues["acme-payments-prod"] = q
		}, ActionNone, 0},
		{"the root batch queue is at capability", func(s *Snapshot) {
			q := s.Queues["adhar-batch"]
			q.Allocated = cpuMem(12000, 8)
			s.Queues["adhar-batch"] = q
		}, ActionNone, 0},
		{"its queue was not read", func(s *Snapshot) { s.Queues = nil }, ActionNone, 0},
		{"its queue names a resource it does not cap", func(s *Snapshot) {
			q := s.Queues["acme-payments-prod"]
			q.Capability = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}
			s.Queues["acme-payments-prod"] = q
		}, ActionScaleUp, 1},
		// Volcano admits a gang against what it asks for; a queue out of GPUs
		// still takes a CPU-only gang, which then waits on nodes.
		{"its queue is out of a resource the gang does not ask for", func(s *Snapshot) {
			q := s.Queues["acme-payments-prod"]
			q.Capability["nvidia.com/gpu"] = resource.MustParse("2")
			q.Allocated = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}
			s.Queues["acme-payments-prod"] = q
		}, ActionScaleUp, 1},

		// Only a gang Volcano is holding asks for a node.
		{"already running", func(s *Snapshot) { s.Gangs[0].Phase = "Running" }, ActionNone, 0},
		{"not reported unschedulable", func(s *Snapshot) { s.Gangs[0].Unschedulable = false }, ActionNone, 0},
		{"a PodGroup no Volcano Job owns", func(s *Snapshot) { s.Gangs[0].OwnedByVolcanoJob = false }, ActionNone, 0},
		{"a member larger than a whole worker", func(s *Snapshot) {
			s.Gangs[0] = prodGang("settlement", 1, 6000, 2)
		}, ActionNone, 0},

		// The cooldown spaces gang scale-ups like any other: one scale-up, then
		// the next tick waits while the worker joins.
		{"a worker was just added for it", func(s *Snapshot) {
			last := metav1.NewTime(testNow.Add(-time.Minute))
			s.Status.LastScaleUp = &last
		}, ActionNone, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := gangSnapshot(prodGang("settlement", 2, 1500, 2))
			if c.mutate != nil {
				c.mutate(&s)
			}
			d := Decide(s)
			if d.Action != c.want || d.ScaleUpBy != c.by {
				t.Fatalf("got %s by %d (%s), want %s by %d", d.Action, d.ScaleUpBy, d.Reason, c.want, c.by)
			}
		})
	}
}

// A gang is all-or-nothing, so a part of the workers it needs buys nothing it
// can use. It gets the right count, counted in members since a pod does not
// split across nodes, or none at all.
func TestAGangIsNeverBoughtPartOfWhatItNeeds(t *testing.T) {
	cases := []struct {
		name       string
		gang       Gang
		maxWorkers int32
		want       Action
		by         int32
	}{
		{"three members of 3000m need three workers", prodGang("g", 3, 3000, 2), 5, ActionScaleUp, 3},
		// 4 × 2000m: the sum (8000m) fills three 3400m slices, but a slice holds
		// one 2000m member, so four workers or the fourth member never runs.
		{"four members of 2000m need four workers, not the three their sum fills", prodGang("g", 4, 2000, 2), 7, ActionScaleUp, 4},
		// The resource sum (15000m) would say five 3400m slices; members say five
		// workers too, and five do not fit the room of three.
		{"five members of 3000m with room for three", prodGang("g", 5, 3000, 2), 5, ActionNone, 0},
		// The pod burst cap does not apply to a gang's exact count.
		{"five members of 3000m with room for five", prodGang("g", 5, 3000, 2), 7, ActionScaleUp, 5},
		// Memory decides as much as CPU: 3 × 5Gi, one member per 8Gi worker.
		{"memory-bound members", prodGang("g", 3, 500, 5), 5, ActionScaleUp, 3},
		{"at maxWorkers", prodGang("g", 1, 1000, 1), 2, ActionNone, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := gangSnapshot(c.gang)
			s.Spec = (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: c.maxWorkers}).WithDefaults()
			s.Queues["acme-payments-prod"] = GangQueue{Name: "acme-payments-prod", Parent: "adhar-batch"}
			s.Queues["adhar-batch"] = GangQueue{Name: "adhar-batch"}
			d := Decide(s)
			if d.Action != c.want || d.ScaleUpBy != c.by {
				t.Fatalf("got %s by %d (%s), want %s by %d", d.Action, d.ScaleUpBy, d.Reason, c.want, c.by)
			}
			if c.want == ActionNone && !strings.Contains(d.Reason, "not buying part of a gang") {
				t.Fatalf("the operator is not told why the gang waits: %s", d.Reason)
			}
		})
	}
}

// A gang that is waiting for room it cannot be given holds the cluster as a
// pending pod at maxWorkers does: shrinking under it would only push it further
// from fitting.
func TestAGangThatCannotBeServedKeepsTheClusterFromShrinking(t *testing.T) {
	s := idleSnapshot() // would retire w3
	s.Spec.MaxWorkers = 4
	s.Gangs = []Gang{prodGang("g", 2, 3950, 2)} // 7900m: within its queue; no worker has 3950m free, two new ones
	s.Queues = batchQueues()
	d := Decide(s)
	if d.Action != ActionNone {
		t.Fatalf("got %s %q (%s) under a gang that needs two workers", d.Action, d.Node, d.Reason)
	}
	// The same snapshot without the gang shrinks, so the gang is what held it.
	s.Gangs = nil
	if d := Decide(s); d.Action != ActionScaleDown {
		t.Fatalf("fixture must shrink without the gang, got %s (%s)", d.Action, d.Reason)
	}
}

// Two gangs: each is taken whole while the room lasts, and the one that does
// not fit after the first is not bought a part.
func TestGangsShareTheRoomWithoutSplittingAny(t *testing.T) {
	s := gangSnapshot(prodGang("a", 2, 3000, 2), prodGang("b", 2, 3000, 2)) // two workers each; room 3
	s.Queues["acme-payments-prod"] = GangQueue{Name: "acme-payments-prod"}
	d := Decide(s)
	if d.Action != ActionScaleUp || d.ScaleUpBy != 2 || d.PendingGangs != 1 {
		t.Fatalf("got %s by %d for %d gang(s) (%s), want a whole gang's 2 workers", d.Action, d.ScaleUpBy, d.PendingGangs, d.Reason)
	}
}

// A Volcano pod is one member of a gang. Its demand is its PodGroup's, so it is
// never counted again by the pod rule, and without Volcano no node helps it.
func TestVolcanoPodsAreCountedThroughTheirGangOnly(t *testing.T) {
	member := func(name string) corev1.Pod {
		return gangMember(name, "settlement", 1500, 2, "Node: 0/3 nodes are unavailable: 3 Insufficient cpu.")
	}
	// Members created once the gang went Inqueue: one worker, not one per member.
	g := prodGang("settlement", 2, 1500, 2)
	g.Phase = gangPhaseInqueue
	s := gangSnapshot(g)
	s.Pods = append(s.Pods, member("settlement-manager-0"), member("settlement-worker-0"))
	d := Decide(s)
	if d.Action != ActionScaleUp || d.ScaleUpBy != 1 || d.PendingPods != 0 {
		t.Fatalf("got %s by %d with %d pod(s) (%s), want the gang's one worker", d.Action, d.ScaleUpBy, d.PendingPods, d.Reason)
	}
	// No Volcano, no gangs: the members alone buy nothing.
	s = gangSnapshot()
	s.Pods = append(s.Pods, member("a"), member("b"))
	if d := Decide(s); d.Action != ActionNone {
		t.Fatalf("Volcano pods with no gang bought a node: %s", d.Reason)
	}
}

// Pods that pend beside a gang take what room the gang leaves.
func TestPendingPodsShareTheRoomAGangLeaves(t *testing.T) {
	s := gangSnapshot(prodGang("settlement", 2, 1500, 2))
	s.Pods = append(s.Pods, pending("api", "0/3 nodes are available: 3 Insufficient cpu."))
	d := Decide(s)
	if d.Action != ActionScaleUp || d.ScaleUpBy != 2 {
		t.Fatalf("got %s by %d (%s), want the gang's worker plus the pod's", d.Action, d.ScaleUpBy, d.Reason)
	}
	s.Spec.MaxWorkers = 3 // room for the gang alone
	if d := Decide(s); d.Action != ActionScaleUp || d.ScaleUpBy != 1 {
		t.Fatalf("got %s by %d (%s), want the gang's one worker within maxWorkers", d.Action, d.ScaleUpBy, d.Reason)
	}
}
