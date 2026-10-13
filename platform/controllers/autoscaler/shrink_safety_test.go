package autoscaler

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
)

// A cluster-wide MEAN cannot answer "is it safe to give a node back", and
// believing it made the autoscaler undo every attempt to relieve the cluster.
//
// Live on 2026-10-13, DigitalOcean: `adhar-adhar-workers-4` sat at 90% memory
// with the kernel OOM-killing processes on it (`SystemOOM`: java, alloy,
// prometheus, argocd-application-controller), which stalled its kubelet and
// flapped the node. Three workers were added to relieve it. Because the new
// nodes were empty they pulled the cluster mean down to `cpu=25% memory=38%`,
// and the very next tick decided:
//
//	action=ScaleDown workers=7
//	reason="cluster idle for 20m17s (cpu=25% memory=38%); removing adhar-adhar-workers-7 (7→6 workers)"
//
// Two of the three were retired within four minutes. Kubernetes never migrates
// a RUNNING pod, so the hot node stayed exactly as hot, and the cluster
// converged back to the overloaded shape it started in — which is what the
// node flapping that day actually was.
func TestAnIdleMeanDoesNotRetireANodeWhileAWorkerIsLoaded(t *testing.T) {
	since := metav1.NewTime(testNow.Add(-30 * time.Minute))
	s := Snapshot{
		Spec:   (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 10}).WithDefaults(),
		Status: v1alpha1.AutoscalingStatus{UnderutilizedSince: &since},
		// One loaded worker and three the operator just added to rescue it.
		Nodes: []corev1.Node{worker("hot"), worker("new1"), worker("new2"), worker("new3")},
		Pods: []corev1.Pod{
			// 7 of the node's 8 GiB: 87% of `hot`, but only 22% of the cluster.
			placed("apps", "heavy", "hot", 500, 7),
		},
		Now: testNow,
	}

	// The premise: the mean really is below the threshold, so without the peak
	// check this snapshot scales down.
	if u := utilization(s.Nodes, s.Pods); u.Memory >= s.Spec.ScaleDownThreshold() {
		t.Fatalf("test premise broken: cluster mean memory %.0f%% is not under the %.0f%% threshold",
			u.Memory*100, s.Spec.ScaleDownThreshold()*100)
	}

	d := Decide(s)
	if d.Action == ActionScaleDown {
		t.Fatalf("retired %s while `hot` carries 87%% of its memory — a mean hides the one node "+
			"that makes shrinking unsafe, and removing capacity here is what made the cluster "+
			"oscillate (reason: %s)", d.Node, d.Reason)
	}
	if !strings.Contains(d.Reason, "hot") {
		t.Errorf("the decision must name the worker that blocked it, got %q", d.Reason)
	}
}

// A worker that joined moments ago is empty — nothing reschedules onto it —
// so it is the lowest-scored drain candidate and gets retired first.
//
// `Status.LastScaleUp` guards nodes THIS controller added. It knows nothing
// about one added by `adhar cluster scale` from an operator's laptop, which is
// exactly how the three rescue workers above were reclaimed: the controller's
// own LastScaleUp was stale, so its "a worker was added recently" hold never
// fired. The node's age is the fact that does not depend on who created it.
func TestAWorkerThatJustJoinedIsNotRetired(t *testing.T) {
	s := idleSnapshot()
	fresh := worker("w4")
	fresh.CreationTimestamp = metav1.NewTime(testNow.Add(-1 * time.Minute))
	s.Nodes = append(s.Nodes, fresh)

	d := Decide(s)
	if d.Node == "w4" {
		t.Fatalf("retired w4 one minute after it joined; it is empty only because nothing has "+
			"rescheduled onto it yet (reason: %s)", d.Reason)
	}

	// And when EVERY worker is that young there is nothing to retire at all,
	// rather than falling through and taking one anyway.
	all := idleSnapshot()
	for i := range all.Nodes {
		all.Nodes[i].CreationTimestamp = metav1.NewTime(testNow.Add(-1 * time.Minute))
	}
	if d := Decide(all); d.Action == ActionScaleDown {
		t.Fatalf("every worker joined a minute ago, yet %s was retired (%s)", d.Node, d.Reason)
	}
}

// The peak must track whichever dimension is closest to its ceiling: a node at
// 95% CPU blocks scheduling exactly as hard as one at 95% memory.
func TestPeakUtilizationReportsTheBusiestWorkerInEitherDimension(t *testing.T) {
	nodes := []corev1.Node{worker("a"), worker("b")}
	pods := []corev1.Pod{
		placed("x", "cpu-bound", "a", 3800, 1), // 95% of 4000 milli
		placed("x", "mem-bound", "b", 100, 7),  // 87% of 8 GiB
	}
	peak, hottest := peakUtilization(nodes, pods)
	if hottest != "a" {
		t.Errorf("busiest worker is %q, want \"a\" at 95%% CPU", hottest)
	}
	if peak.CPU < 0.9 {
		t.Errorf("peak CPU %.2f does not reflect the 95%% node", peak.CPU)
	}
}

// `scaleDownDelay: 0s` must not disable the new-worker grace.
//
// The live cluster carried exactly that, so every cool-down check passed
// instantly: the "a worker was added recently" hold, the "removed recently"
// hold, and — had the age filter been derived from the same setting — the
// grace too. A configuration that switches off the delay is asking for a
// responsive autoscaler, not for nodes to be retired before anything can be
// scheduled onto them.
func TestZeroScaleDownDelayStillLeavesANewWorkerAlone(t *testing.T) {
	s := idleSnapshot()
	s.Spec.ScaleDownDelay = metav1.Duration{Duration: 0}
	fresh := worker("w4")
	fresh.CreationTimestamp = metav1.NewTime(testNow.Add(-2 * time.Minute))
	s.Nodes = append(s.Nodes, fresh)

	if d := Decide(s); d.Node == "w4" {
		t.Fatalf("scaleDownDelay=0s retired a two-minute-old worker (%s)", d.Reason)
	}
}
