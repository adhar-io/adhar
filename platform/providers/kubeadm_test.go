package provider

import (
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// The kubeadm package stream must follow the platform-wide Kubernetes default
// so cloud clusters run the same minor as local Kind.
func TestKubeadmDefaultMinorMatchesGlobals(t *testing.T) {
	want := K8sMinorFromVersion(globals.DefaultKubernetesVersion)
	if KubeadmDefaultK8sMinor != want {
		t.Fatalf("KubeadmDefaultK8sMinor=%q but globals.DefaultKubernetesVersion=%q (minor %q)", KubeadmDefaultK8sMinor, globals.DefaultKubernetesVersion, want)
	}
	if !strings.HasPrefix(KubeadmNodePrepScript(KubeadmDefaultK8sMinor), "#!/bin/bash") {
		t.Fatal("node prep script must be a bash script")
	}
}

func TestWorkerScalePlanAddsLowestFreeIndicesAndRemovesHighestFirst(t *testing.T) {
	prefix := "adhar-dev-workers-"
	// Gaps are reused (workers-4 was retired earlier), never renumbered.
	add, remove := WorkerScalePlan(prefix, []string{prefix + "1", prefix + "2", prefix + "3", prefix + "5"}, 6)
	if len(remove) != 0 || len(add) != 2 || add[0] != prefix+"4" || add[1] != prefix+"6" {
		t.Fatalf("scale up: add=%v remove=%v", add, remove)
	}
	// Scale down retires the youngest (highest-indexed) workers first.
	add, remove = WorkerScalePlan(prefix, []string{prefix + "1", prefix + "2", prefix + "3", prefix + "5"}, 2)
	if len(add) != 0 || len(remove) != 2 || remove[0] != prefix+"5" || remove[1] != prefix+"3" {
		t.Fatalf("scale down: add=%v remove=%v", add, remove)
	}
	// Names that are not members of this group are ignored, not counted.
	add, remove = WorkerScalePlan(prefix, []string{prefix + "1", "adhar-dev-master-1", "other-workers-9"}, 1)
	if len(add) != 0 || len(remove) != 0 {
		t.Fatalf("unrelated names must not count: add=%v remove=%v", add, remove)
	}
	// A negative desired count is treated as zero.
	_, remove = WorkerScalePlan(prefix, []string{prefix + "1"}, -3)
	if len(remove) != 1 {
		t.Fatalf("negative desired: remove=%v", remove)
	}
}
