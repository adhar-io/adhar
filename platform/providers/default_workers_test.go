package provider

import (
	"testing"

	"adhar-io/adhar/platform/config"
)

// A cloud platform starts as 1 control plane + 2 WORKERS.
//
// Two, not one: a single worker leaves every eviction, drain and node replacement
// with nowhere to put the pods, and it hits the kubelet's 110-pod ceiling long
// before it runs out of CPU — measured on Azure, where one 4-vCPU worker reported
// "Too many pods" with 105 pods Pending. Two, not three: the autoscaler now adds
// capacity at the scale-up utilisation threshold rather than waiting for pods to go
// Pending, so a start does not need to be provisioned for the peak.
func TestProductionStartsWithTwoWorkers(t *testing.T) {
	spec, err := buildClusterSpec(&config.ResolvedEnvironmentConfig{
		Name: "prod", ResolvedType: config.EnvironmentTypeProduction, ResolvedProvider: "azure", ResolvedRegion: "malaysiawest",
	})
	if err != nil {
		t.Fatalf("buildClusterSpec: %v", err)
	}
	if spec.ControlPlane.Replicas != 1 {
		t.Errorf("control plane replicas = %d, want 1", spec.ControlPlane.Replicas)
	}
	if len(spec.NodeGroups) != 1 {
		t.Fatalf("expected one node group, got %d", len(spec.NodeGroups))
	}
	if got := spec.NodeGroups[0].Replicas; got != 2 {
		t.Errorf("worker replicas = %d, want 2", got)
	}
}

// Local keeps zero workers: the control plane runs untainted on Kind, so a separate
// worker would be pure overhead on a laptop.
func TestLocalStartsWithNoSeparateWorker(t *testing.T) {
	spec, err := buildClusterSpec(&config.ResolvedEnvironmentConfig{
		Name: "local", ResolvedType: config.EnvironmentTypeNonProduction, ResolvedProvider: "kind",
	})
	if err != nil {
		t.Fatalf("buildClusterSpec: %v", err)
	}
	if got := spec.NodeGroups[0].Replicas; got != 0 {
		t.Errorf("local worker replicas = %d, want 0", got)
	}
}

// An explicit autoscaling floor above the default raises the starting size, so a
// cluster never boots smaller than its own configuration asks for.
func TestAutoscalingFloorRaisesTheStartingWorkers(t *testing.T) {
	spec, err := buildClusterSpec(&config.ResolvedEnvironmentConfig{
		Name: "prod", ResolvedType: config.EnvironmentTypeProduction, ResolvedProvider: "azure",
		Autoscaling: &config.AutoscalingConfig{Enabled: true, MinWorkers: 4, MaxWorkers: 8},
	})
	if err != nil {
		t.Fatalf("buildClusterSpec: %v", err)
	}
	ng := spec.NodeGroups[0]
	if ng.Replicas != 4 {
		t.Errorf("replicas = %d, want the minWorkers floor of 4", ng.Replicas)
	}
	if ng.AutoScaling.MaxReplicas != 8 {
		t.Errorf("maxReplicas = %d, want 8", ng.AutoScaling.MaxReplicas)
	}
}
