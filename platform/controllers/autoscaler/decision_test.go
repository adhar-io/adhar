package autoscaler

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
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
