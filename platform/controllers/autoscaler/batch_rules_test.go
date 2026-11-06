package autoscaler

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// What the node autoscaler owes batch (design 0025 §4.7, G3, acceptance A1.8): it
// never retires a node out from under a run that asked it not to, and a run that has
// not opted in never makes it buy a node. Both rules read what the BatchJob
// composition stamps on every run pod.

func batchRun(name, node string) corev1.Pod {
	p := placed("dev", name, node, 1000, 2)
	p.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: name}}
	p.Labels = map[string]string{workloadLabel: "batch", "adhar.io/batch-job": "importer"}
	p.Annotations = map[string]string{safeToEvictAnnotation: "false"}
	return p
}

func TestANodeWithARunThatAskedNotToBeEvictedIsNeverDrained(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*corev1.Pod)
		retires bool
	}{
		{"a running run with safe-to-evict false", nil, false},
		{"a run still pulling its image", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }, false},
		// `disruption: allow`: the run stops gracefully and restarts from its last commit.
		{"disruption allow", func(p *corev1.Pod) { delete(p.Annotations, safeToEvictAnnotation) }, true},
		{"safe-to-evict true", func(p *corev1.Pod) { p.Annotations[safeToEvictAnnotation] = "true" }, true},
		// Finished pods wait out the run's TTL, a week by default. If they counted, a
		// node that ran one job could not be retired for seven days.
		{"a run that succeeded", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }, true},
		{"a run that failed", func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := idleSnapshot()
			// Every other worker holds something heavier, so w3 is what gets retired
			// unless the rule stops it.
			p := batchRun("importer-1", "w3")
			p.Spec.Containers[0].Resources.Requests = nil
			if c.mutate != nil {
				c.mutate(&p)
			}
			s.Pods = append(s.Pods, p)
			d := Decide(s)
			if c.retires {
				if d.Action != ActionScaleDown || d.Node != "w3" {
					t.Fatalf("expected w3 to be retired, got %s %q (%s)", d.Action, d.Node, d.Reason)
				}
				return
			}
			if d.Node == "w3" {
				t.Fatalf("w3 holds a run that asked not to be evicted and was chosen for removal: %s", d.Reason)
			}
		})
	}
}

// The annotation is upstream's convention and not batch's: a pod of any kind that
// carries it is honoured.
func TestSafeToEvictFalseIsHonouredForAnyPod(t *testing.T) {
	p := placed("apps", "training", "w1", 100, 1)
	p.Annotations = map[string]string{safeToEvictAnnotation: "false"}
	why := unsafeToDrain([]corev1.Pod{p}, nil)
	if !strings.Contains(why, "apps/training") || !strings.Contains(why, safeToEvictAnnotation) {
		t.Fatalf("unsafeToDrain said %q", why)
	}
	// When every worker is blocked the operator is told why.
	s := idleSnapshot()
	for _, n := range s.Nodes {
		if IsWorker(n) {
			r := placed("dev", "run-"+n.Name, n.Name, 10, 0)
			r.Annotations = map[string]string{safeToEvictAnnotation: "false"}
			s.Pods = append(s.Pods, r)
		}
	}
	d := Decide(s)
	if d.Action != ActionNone || !strings.Contains(d.Reason, "safe-to-evict") {
		t.Fatalf("got %s (%s), want no action naming the annotation", d.Action, d.Reason)
	}
}

// A run can land on the node between the decision and the cordon. The drain gives the
// node back rather than evict it.
func TestADrainStopsAtARunThatLandedAfterTheDecision(t *testing.T) {
	w := worker("w2")
	run := batchRun("importer-1", "w2")
	r := newReconciler(t, &fakeScaler{}, &w, &run)

	err := r.drainOrRestore(context.Background(), "w2")
	if err == nil || !strings.Contains(err.Error(), "dev/importer-1") {
		t.Fatalf("the drain went ahead: %v", err)
	}
	pod := &corev1.Pod{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "dev", Name: "importer-1"}, pod); err != nil {
		t.Fatalf("the run is gone: %v", err)
	}
	node := &corev1.Node{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "w2"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Spec.Unschedulable {
		t.Fatal("the node stayed cordoned")
	}
}

func TestABatchBacklogBuysANodeOnlyWhenItOptedIn(t *testing.T) {
	const noRoom = "0/3 nodes are available: 3 Insufficient memory."
	snapshot := func(labels map[string]string) Snapshot {
		p := pending("importer-1", noRoom)
		p.Labels = labels
		return Snapshot{
			Spec:  enabledSpec(),
			Nodes: []corev1.Node{controlPlane(), worker("w1"), worker("w2")},
			Pods:  []corev1.Pod{p},
			Now:   testNow,
		}
	}
	for _, c := range []struct {
		name   string
		labels map[string]string
		want   Action
		count  int
	}{
		{"a dev backlog", map[string]string{workloadLabel: "batch", "adhar.io/environment": "dev"}, ActionNone, 0},
		{"the opt-in spelled wrong is no opt-in", map[string]string{workloadLabel: "batch", scaleUpLabel: "yes"}, ActionNone, 0},
		{"a prod run that opted in", map[string]string{workloadLabel: "batch", scaleUpLabel: "true"}, ActionScaleUp, 1},
		{"a service, as ever", map[string]string{workloadLabel: "service"}, ActionScaleUp, 1},
		{"no labels at all, as ever", nil, ActionScaleUp, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(snapshot(c.labels))
			if d.Action != c.want || d.PendingPods != c.count {
				t.Fatalf("got %s with %d pending (%s), want %s with %d", d.Action, d.PendingPods, d.Reason, c.want, c.count)
			}
		})
	}
}

// The other door to a new node is the utilization threshold: dev runs filling the
// workers must not get through it either. They are still load when the question is
// whether a node can go.
func TestRunningBatchThatMayNotBuyNodesDoesNotCountTowardsScalingUp(t *testing.T) {
	full := func(labels map[string]string) Snapshot {
		s := Snapshot{
			Spec:  enabledSpec(),
			Nodes: []corev1.Node{controlPlane(), worker("w1"), worker("w2")},
			Now:   testNow,
		}
		for _, n := range []string{"w1", "w2"} {
			p := placed("dev", "run-"+n, n, 3900, 7)
			p.Labels = labels
			s.Pods = append(s.Pods, p)
		}
		return s
	}
	if d := Decide(full(nil)); d.Action != ActionScaleUp {
		t.Fatalf("the fixture must scale up for services, got %s (%s)", d.Action, d.Reason)
	}
	batch := map[string]string{workloadLabel: "batch"}
	d := Decide(full(batch))
	if d.Action != ActionNone {
		t.Fatalf("a full cluster of dev runs bought a node: %s", d.Reason)
	}
	if d.Utilization.CPU < 0.9 {
		t.Fatalf("the reported utilization is %.2f; the runs are real load and status must say so", d.Utilization.CPU)
	}
	if !strings.Contains(d.Reason, "scale-down threshold") {
		t.Fatalf("the cluster must read as busy, not idle, for shrinking: %s", d.Reason)
	}
	if d := Decide(full(map[string]string{workloadLabel: "batch", scaleUpLabel: "true"})); d.Action != ActionScaleUp {
		t.Fatalf("opted-in runs must count, got %s (%s)", d.Action, d.Reason)
	}

	// Measured CPU is node-level, so the runs' requests are taken out of it.
	s := full(batch)
	s.NodeCPUUsageMilli = map[string]int64{"w1": 3900, "w2": 3900}
	if d := Decide(s); d.Action != ActionNone {
		t.Fatalf("runs burning exactly what they requested bought a node: %s", d.Reason)
	}
	s.Pods = append(s.Pods, placed("apps", "api", "w1", 0, 0))
	s.NodeCPUUsageMilli = map[string]int64{"w1": 8000, "w2": 8000}
	if d := Decide(s); d.Action != ActionScaleUp {
		t.Fatalf("usage well past what the runs account for is real load, got %s (%s)", d.Action, d.Reason)
	}
}
