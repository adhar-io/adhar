package aws

import (
	"testing"

	"adhar-io/adhar/platform/types"
)

func TestVCPUsForInstanceType(t *testing.T) {
	for in, want := range map[string]int{
		"t3.micro": 1, "t3.medium": 1, "m6i.large": 2, "m6i.xlarge": 4,
		"m6i.2xlarge": 8, "m6i.4xlarge": 16, "m6i.8xlarge": 32, "m6i.12xlarge": 48,
		"": 2, "nonsense": 2,
	} {
		if got := vcpusForInstanceType(in); got != want {
			t.Errorf("vcpusForInstanceType(%q) = %d, want %d", in, got, want)
		}
	}
}

// The quota must be sized for the autoscaling MAXIMUM. Sizing to the starting
// replica count is how a cluster boots and then cannot buy the capacity the
// platform actually needs.
func TestNeededVCPUsUsesTheAutoscalingMaximum(t *testing.T) {
	spec := &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{Replicas: 1, InstanceType: "m6i.2xlarge"},
		NodeGroups: []types.NodeGroupSpec{{
			Name: "workers", Replicas: 2, InstanceType: "m6i.2xlarge",
			AutoScaling: types.AutoScalingSpec{MinReplicas: 2, MaxReplicas: 6},
		}},
	}
	// 1 control plane + 6 workers, 8 vCPU each = 56, not the 24 the starting
	// replica count would suggest.
	if got := neededVCPUs(spec); got != 56 {
		t.Fatalf("neededVCPUs = %d, want 56 (control plane + maxReplicas)", got)
	}

	// Without autoscaling, the replica count stands.
	spec.NodeGroups[0].AutoScaling = types.AutoScalingSpec{}
	if got := neededVCPUs(spec); got != 24 {
		t.Fatalf("neededVCPUs without autoscaling = %d, want 24", got)
	}
}

func TestInstanceTypeForPrefersTheNodeGroup(t *testing.T) {
	spec := &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{InstanceType: "m6i.large"},
		NodeGroups:   []types.NodeGroupSpec{{InstanceType: "m6i.2xlarge"}},
	}
	if got := instanceTypeFor(spec); got != "m6i.2xlarge" {
		t.Errorf("instanceTypeFor = %q, want the worker type", got)
	}
	if got := instanceTypeFor(&types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{InstanceType: "m6i.large"}}); got != "m6i.large" {
		t.Errorf("with no node groups it must fall back to the control plane, got %q", got)
	}
}

// AWS's own "this would have been allowed" and "your parameters are nonsense"
// answers must both count as an authorisation pass, or a dry-run probe reports
// false denials.
func TestDryRunAndParameterErrorsCountAsPermitted(t *testing.T) {
	if !isDryRunSuccess(errStr("api error DryRunOperation: Request would have succeeded")) {
		t.Error("DryRunOperation means permitted")
	}
	if !isParameterComplaint(errStr("InvalidAMIID.Malformed: Invalid id")) {
		t.Error("a malformed AMI id means authorisation already passed")
	}
	if isParameterComplaint(errStr("UnauthorizedOperation: You are not authorized")) {
		t.Error("an authorisation failure is not a parameter complaint")
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
