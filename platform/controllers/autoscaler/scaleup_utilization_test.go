package autoscaler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"adhar-io/adhar/api/v1alpha1"
)

// The autoscaler must add a worker while the cluster is merely BUSY, not wait for
// something to become unschedulable.
//
// Pending pods were the only scale-up signal, which means the cluster grew only
// after work had already stopped: a pod that cannot be placed waits out a node
// create, minutes on every cloud. Reacting at high utilisation avoids that
// entirely, and the Pending path remains the backstop for a burst too large to
// anticipate.
func TestScaleUpOnHighUtilizationBeforeAnythingIsPending(t *testing.T) {
	// One worker, filled past 90% CPU by running pods, nothing Pending.
	spec := (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 5}).WithDefaults()
	d := Decide(Snapshot{
		Spec:  spec,
		Nodes: []corev1.Node{controlPlane(), workerSized("w1", 1000, 8)},
		Pods: []corev1.Pod{
			placed("default", "a", "w1", 500, 1),
			placed("default", "b", "w1", 450, 1), // 950m of 1000m = 95%
		},
		Now: testNow,
	})
	if d.Action != ActionScaleUp {
		t.Fatalf("expected ScaleUp at 95%% CPU with nothing pending, got %s (%s)", d.Action, d.Reason)
	}
	if d.PendingPods != 0 {
		t.Fatalf("this must not depend on pending pods, got %d", d.PendingPods)
	}
	if d.ScaleUpBy != 1 {
		t.Fatalf("a proactive add is one worker at a time, got %d", d.ScaleUpBy)
	}
}

// Busy but below the threshold is left alone, or the cluster would grow on
// ordinary variation.
func TestNoScaleUpBelowTheUtilizationThreshold(t *testing.T) {
	spec := (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 5}).WithDefaults()
	d := Decide(Snapshot{
		Spec:  spec,
		Nodes: []corev1.Node{controlPlane(), workerSized("w1", 1000, 8)},
		Pods:  []corev1.Pod{placed("default", "a", "w1", 700, 1)}, // 70%
		Now:   testNow,
	})
	if d.Action == ActionScaleUp {
		t.Fatalf("70%% should not scale up against a 90%% threshold: %s", d.Reason)
	}
}

// maxWorkers is still the ceiling, and the reason must say so rather than looking
// like the cluster is fine.
func TestProactiveScaleUpRespectsMaxWorkers(t *testing.T) {
	spec := (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 1}).WithDefaults()
	d := Decide(Snapshot{
		Spec:  spec,
		Nodes: []corev1.Node{controlPlane(), workerSized("w1", 1000, 8)},
		Pods:  []corev1.Pod{placed("default", "a", "w1", 960, 1)},
		Now:   testNow,
	})
	if d.Action != ActionNone {
		t.Fatalf("expected no action at maxWorkers, got %s", d.Action)
	}
	if !contains(d.Reason, "maxWorkers=1") {
		t.Fatalf("the reason must name the ceiling, got %q", d.Reason)
	}
}

// A scale-up threshold below the scale-down threshold would add a node and then
// immediately qualify to remove it.
func TestScaleUpThresholdNeverSitsBelowScaleDown(t *testing.T) {
	s := (&v1alpha1.AutoscalingSpec{
		ScaleDownUtilizationThreshold: "80%",
		ScaleUpUtilizationThreshold:   "50%",
	}).WithDefaults()
	if up, down := s.ScaleUpThreshold(), s.ScaleDownThreshold(); up < down {
		t.Fatalf("scale-up %.2f is below scale-down %.2f", up, down)
	}
}

func TestScaleUpThresholdParsing(t *testing.T) {
	for in, want := range map[string]float64{"90%": 0.9, "0.9": 0.9, "": 0.9, "nonsense": 0.9, "150%": 1} {
		s := v1alpha1.AutoscalingSpec{ScaleUpUtilizationThreshold: in, ScaleDownUtilizationThreshold: "50%"}
		if got := s.ScaleUpThreshold(); got != want {
			t.Errorf("ScaleUpThreshold(%q) = %v, want %v", in, got, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
