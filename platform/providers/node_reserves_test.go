package provider

import (
	"strings"
	"testing"
)

// A node must hold back capacity for the kubelet, or it takes itself down.
//
// kubeadm reserves NOTHING by default. Measured live on 2026-10-13, every
// worker of an 8 vCPU / 16 GiB cluster reported:
//
//	cpu    capacity 8       allocatable 8        (zero reserved)
//	memory capacity 15.62G  allocatable 15.52G   (100Mi — the eviction default)
//
// so the scheduler could place pods on all eight cores and all but 100 MiB of
// RAM, leaving the kubelet and containerd to compete with the pods they
// manage. The result is not a scheduling error, it is the node disappearing:
//
//	ContainerGCFailed: rpc error: code = DeadlineExceeded
//	ImageGCFailed: context deadline exceeded
//	NodeNotReady
//
// and with a 100Mi margin the kernel OOM killer fires before the kubelet can
// evict — SystemOOM killing java, prometheus and argocd-application-controller
// instead of the pod over its request. Pods move to the next node and carry
// the failure around the cluster, which is what the node flapping was.
func TestEveryNodeReservesCapacityForTheKubelet(t *testing.T) {
	byKey := map[string]string{}
	for _, tn := range kubeletTuning {
		byKey[tn.Key] = tn.Line
	}

	for _, key := range []string{"systemReserved", "kubeReserved", "evictionHard"} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("kubeletTuning declares no %q: a kubeadm node reserves nothing by default, "+
				"so pods can starve the kubelet and the node goes NotReady", key)
		}
	}

	// Both CPU and memory must be reserved. Reserving only memory still lets
	// pods take all eight cores, and a CPU-starved kubelet misses its sync
	// loop exactly the same way.
	for _, key := range []string{"systemReserved", "kubeReserved"} {
		line := byKey[key]
		if !strings.Contains(line, "cpu:") {
			t.Errorf("%s reserves no cpu (%q); a CPU-starved kubelet stops heartbeating", key, line)
		}
		if !strings.Contains(line, "memory:") {
			t.Errorf("%s reserves no memory (%q)", key, line)
		}
	}

	// The eviction margin has to beat the kernel. 100Mi is the default that
	// lost that race.
	ev := byKey["evictionHard"]
	if strings.Contains(ev, "memory.available: 100Mi") {
		t.Error("evictionHard still uses the 100Mi default margin; the kubelet cannot evict " +
			"within it and the kernel OOM killer fires first")
	}
	if !strings.Contains(ev, "memory.available:") {
		t.Errorf("evictionHard sets no memory.available threshold: %q", ev)
	}
}

// The reserves are appended to a file kubeadm wrote, over ssh, through printf —
// so the map-valued keys have to survive that path and still be YAML.
func TestTheReservedCapacitySurvivesTheShellRoundTrip(t *testing.T) {
	cmd := imagePullTuningCommand()

	if strings.Contains(cmd, "\n") {
		t.Fatal("a real newline would split the ssh script; payloads must carry escaped \\n")
	}
	for _, key := range []string{"systemReserved", "kubeReserved", "evictionHard"} {
		// Guarded per key, so a node built by an earlier release gains only
		// what it lacks instead of having the block skipped wholesale.
		if !strings.Contains(cmd, "grep -q '^"+key+":'") {
			t.Errorf("%s is appended without its own grep guard", key)
		}
	}
	// A map value needs an indented continuation line, not a flat scalar.
	if !strings.Contains(cmd, `\n  cpu: `) || !strings.Contains(cmd, `\n  memory: `) {
		t.Error("the reserve payloads carry no indented cpu/memory entries, so the appended " +
			"YAML would not be a map the kubelet can read")
	}
}
