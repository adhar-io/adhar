package autoscaler

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
)

// A drain that cannot finish (a PodDisruptionBudget holding a pod, a pod
// stuck terminating) must put the node back into service. A cordoned node is
// not a worker to Decide, so it is never chosen for removal again: leaving it
// cordoned stranded a billing machine forever.
func TestAFailedDrainReturnsTheNodeToService(t *testing.T) {
	w := worker("w2")
	stuck := placed("apps", "held", "w2", 100, 1)
	// The finalizer keeps the evicted pod around, so the drain cannot finish.
	stuck.Finalizers = []string{"example.com/hold"}
	r := newReconciler(t, &fakeScaler{}, &w, &stuck)
	r.DrainTimeout = time.Nanosecond

	if err := r.drainOrRestore(context.Background(), "w2"); err == nil {
		t.Fatal("expected the drain to report that it did not finish")
	}
	node := &corev1.Node{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "w2"}, node); err != nil {
		t.Fatal(err)
	}
	if node.Spec.Unschedulable {
		t.Fatal("node is still cordoned after a failed drain; nothing will ever retry or uncordon it")
	}
}

// A node an operator cordoned before the autoscaler chose it stays cordoned:
// restoring service is only for the cordon this drain added.
func TestAFailedDrainLeavesAnOperatorsCordonAlone(t *testing.T) {
	w := worker("w2")
	w.Spec.Unschedulable = true
	stuck := placed("apps", "held", "w2", 100, 1)
	stuck.Finalizers = []string{"example.com/hold"}
	r := newReconciler(t, &fakeScaler{}, &w, &stuck)
	r.DrainTimeout = time.Nanosecond

	_ = r.drainOrRestore(context.Background(), "w2")
	node := &corev1.Node{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "w2"}, node); err != nil {
		t.Fatal(err)
	}
	if !node.Spec.Unschedulable {
		t.Fatal("an operator's cordon was lifted")
	}
}

type deadlineScaler struct {
	fakeScaler
	hadDeadline bool
}

func (d *deadlineScaler) ScaleUp(ctx context.Context, spec *clusterSpec, g string, n int32) error {
	_, d.hadDeadline = ctx.Deadline()
	return d.fakeScaler.ScaleUp(ctx, spec, g, n)
}

// The reconcile context has no deadline and the controller runs one reconcile
// at a time, so a cloud call that never answers must not stop autoscaling for
// good: every scale action carries its own deadline.
func TestScaleActionsCarryADeadline(t *testing.T) {
	d := &deadlineScaler{}
	platform := platformWith(&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 10})
	w1 := worker("w1")
	pending := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "apps"},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: "0/2 nodes are available: 1 Insufficient cpu.",
		}}},
	}
	r := newReconciler(t, d, platform, &w1, &pending, clusterSpecConfigMap())
	tick(t, r)
	if len(d.upCalls) != 1 {
		t.Fatalf("expected a scale-up, got %v", d.upCalls)
	}
	if !d.hadDeadline {
		t.Fatal("scale-up ran on a context with no deadline")
	}
}
