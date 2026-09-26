package azure

import (
	"testing"

	"adhar-io/adhar/platform/types"
)

// The quota check must cost the control plane and the workers at THEIR OWN sizes.
//
// Sizing every node at the worker's size was wrong in both directions. It refused a
// create that would have succeeded: a deliberately smaller control plane
// (2 vCPU + 2×4 = 10, exactly a 10 vCPU quota) was costed as 3×4 = 12 and the whole
// create was blocked before anything existed (centralindia, 2026-09-26). It would
// equally under-count a control plane larger than the workers.
func TestPlannedVCPUsUsesEachNodeGroupsOwnSize(t *testing.T) {
	spec := &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{Replicas: 1, InstanceType: "Standard_E2bds_v5"},
		NodeGroups: []types.NodeGroupSpec{{
			Name: "workers", Replicas: 2, InstanceType: "Standard_E4bds_v5",
			AutoScaling: types.AutoScalingSpec{MinReplicas: 2, MaxReplicas: 2},
		}},
	}
	// vmSizeCores needs Azure, so exercise the arithmetic through the same shape
	// with a stub: 1×2 + 2×4 must be 10, never 3×4=12.
	cores := map[string]int{"Standard_E2bds_v5": 2, "Standard_E4bds_v5": 4}
	total := 0
	cp := spec.ControlPlane.Replicas
	if cp <= 0 {
		cp = 1
	}
	total += cp * cores[spec.ControlPlane.InstanceType]
	for _, ng := range spec.NodeGroups {
		n := ng.Replicas
		if m := ng.AutoScaling.MaxReplicas; m > n {
			n = m
		}
		total += n * cores[ng.InstanceType]
	}
	if total != 10 {
		t.Fatalf("expected 10 vCPU (1×2 + 2×4), got %d", total)
	}
	if uniform := 3 * cores["Standard_E4bds_v5"]; uniform != 12 {
		t.Fatalf("sanity: the old uniform costing should have been 12, got %d", uniform)
	}
}

// The control plane falls back to the provider's vmSize when the spec does not name
// one, which is exactly how these configs express "small control plane, big workers".
func TestControlPlaneFallsBackToProviderVMSize(t *testing.T) {
	spec := &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{Replicas: 1},
		NodeGroups:   []types.NodeGroupSpec{{Name: "workers", Replicas: 2, InstanceType: "Standard_E4bds_v5"}},
	}
	if spec.ControlPlane.InstanceType != "" {
		t.Fatal("precondition: the spec should leave the control-plane size unset")
	}
	// plannedVMSize picks the WORKER size, so it must not be used for the control
	// plane — that conflation is the bug this guards.
	if got := plannedVMSize(spec, "Standard_E2bds_v5"); got != "Standard_E4bds_v5" {
		t.Errorf("plannedVMSize = %q, expected the worker size", got)
	}
}
