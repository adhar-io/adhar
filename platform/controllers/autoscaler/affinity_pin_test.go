package autoscaler

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// pinnedCIPod is a TaskRun pod as Tekton builds it for a run whose workspace is
// a ReadWriteOnce claim: REQUIRED to share a node with the run's affinity
// assistant, and reported "Insufficient memory" on that node while every other
// node "didn't match pod affinity rules".
func pinnedCIPod(name string, waitingFor time.Duration) corev1.Pod {
	p := pending(name, "0/3 nodes are available: 1 Insufficient memory, 2 node(s) didn't match pod affinity rules. "+
		"preemption: 0/3 nodes are available: 1 No preemption victims found for incoming pod, 2 Preemption is not helpful for scheduling.")
	p.Namespace = "adhar-system"
	p.Status.Conditions[0].LastTransitionTime = metav1.NewTime(testNow.Add(-waitingFor))
	p.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey: corev1.LabelHostname,
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/instance":  "affinity-assistant-3f2c1a9b7e",
				"app.kubernetes.io/component": "affinity-assistant",
			}},
		}},
	}}
	return p
}

// A CI pod pinned to its run's affinity assistant is the relocator's to solve
// before it is the autoscaler's: a new worker hosts no assistant, so counting
// the pod at once bought a node it could not use. Only a pod still waiting
// after the grace — no existing node had room — is demand for a worker.
func TestAPodPinnedToACIAffinityAssistantIsTheRelocatorsFirst(t *testing.T) {
	workers := []corev1.Node{worker("w1"), worker("w2"), worker("w3")}
	cases := []struct {
		name    string
		waiting time.Duration
		want    bool
	}{
		{"just pinned: the relocator moves its assistant to a node with room", time.Minute, false},
		{"still pinned after the grace: no node had room, a new one is where it goes", affinityAssistantPinGrace + time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := pinnedCIPod("app-ci-gateway-service-vvzcc-clone-pod", tc.waiting)
			got := pendingForCapacity([]corev1.Pod{pod}, workers, testNow)
			if tc.want && len(got) == 0 {
				t.Fatalf("a CI pod waiting %s is never counted: with no node that has room, the run waits for a worker nobody adds", tc.waiting)
			}
			if !tc.want && len(got) != 0 {
				t.Fatalf("counted %v after %s: a new worker hosts no affinity assistant, so the node is bought for nothing", got, tc.waiting)
			}
			d := Decide(Snapshot{Spec: enabledSpec(), Nodes: workers, Pods: []corev1.Pod{pod}, Now: testNow})
			if (d.Action == ActionScaleUp) != tc.want {
				t.Fatalf("Decide after %s: %s (%s)", tc.waiting, d.Action, d.Reason)
			}
		})
	}

	// A pod with no affinity to an assistant keeps the old rule: counted at once.
	plain := pending("api-1", "0/3 nodes are available: 3 Insufficient memory.")
	plain.Status.Conditions[0].LastTransitionTime = metav1.NewTime(testNow.Add(-time.Minute))
	if got := pendingForCapacity([]corev1.Pod{plain}, workers, testNow); len(got) != 1 {
		t.Fatalf("an ordinary pending pod is no longer counted at once: %v", got)
	}
}

// A pod whose every required node-selector term names its node (a DaemonSet
// pod; an affinity assistant the relocator pinned to the nodes with room) can
// never land on a node that does not exist yet.
func TestAPodPinnedToNamedNodesNeverBuysANode(t *testing.T) {
	workers := []corev1.Node{worker("w1"), worker("w2")}
	for i := range workers {
		workers[i].Labels = map[string]string{"kubernetes.io/os": "linux"}
	}
	named := func(terms ...corev1.NodeSelectorTerm) corev1.Pod {
		p := pending("pinned", "0/2 nodes are available: 1 Insufficient memory, 1 node(s) didn't match Pod's node affinity/selector.")
		p.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms},
		}}
		return p
	}
	byName := func(n string) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{
			{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{n}}}}
	}
	byLabel := corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
		{Key: "kubernetes.io/os", Operator: corev1.NodeSelectorOpIn, Values: []string{"linux"}}}}

	for _, tc := range []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{"a DaemonSet pod for one node", named(byName("w1")), false},
		{"an assistant moved to two named nodes", named(byName("w1"), byName("w2")), false},
		{"one term a new node can satisfy", named(byName("w1"), byLabel), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingForCapacity([]corev1.Pod{tc.pod}, workers, testNow)
			if tc.want != (len(got) == 1) {
				t.Fatalf("counted=%v, want %v", got, tc.want)
			}
		})
	}
}
