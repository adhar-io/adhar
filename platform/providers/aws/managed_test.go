package aws

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"adhar-io/adhar/platform/types"
)

func TestEKSTaintEffectSpelling(t *testing.T) {
	cases := map[string]ekstypes.TaintEffect{
		"NoSchedule": ekstypes.TaintEffectNoSchedule, "NoExecute": ekstypes.TaintEffectNoExecute,
		"PreferNoSchedule": ekstypes.TaintEffectPreferNoSchedule, "NO_EXECUTE": ekstypes.TaintEffectNoExecute,
		"": ekstypes.TaintEffectNoSchedule, "garbage": ekstypes.TaintEffectNoSchedule,
	}
	for in, want := range cases {
		if got := eksTaintEffect(in); got != want {
			t.Errorf("eksTaintEffect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEKSScalingClampsIntoTheAutoscalingRange(t *testing.T) {
	cases := []struct {
		name                string
		spec                types.NodeGroupSpec
		desired, minS, maxS int32
		instanceType        string
	}{
		{"defaults", types.NodeGroupSpec{}, 2, 2, 2, eksDefaultNodeType},
		{"fixed", types.NodeGroupSpec{Replicas: 5, InstanceType: "m6i.large"}, 5, 5, 5, "m6i.large"},
		{"range with zero min", types.NodeGroupSpec{AutoScaling: types.AutoScalingSpec{MaxReplicas: 10}}, 2, 1, 10, eksDefaultNodeType},
		{"desired above max", types.NodeGroupSpec{Replicas: 20, AutoScaling: types.AutoScalingSpec{MinReplicas: 2, MaxReplicas: 10}}, 10, 2, 10, eksDefaultNodeType},
		{"desired below min", types.NodeGroupSpec{Replicas: 1, AutoScaling: types.AutoScalingSpec{MinReplicas: 3, MaxReplicas: 6}}, 3, 3, 6, eksDefaultNodeType},
	}
	for _, c := range cases {
		d, lo, hi, it := eksScaling(&c.spec)
		if d != c.desired || lo != c.minS || hi != c.maxS || it != c.instanceType {
			t.Errorf("%s: got (%d,%d,%d,%s), want (%d,%d,%d,%s)", c.name, d, lo, hi, it, c.desired, c.minS, c.maxS, c.instanceType)
		}
	}
}

func TestEKSToClusterMapsStatusVersionAndMetadata(t *testing.T) {
	p := &Provider{config: &Config{Region: "eu-west-1"}}
	created := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	statuses := map[ekstypes.ClusterStatus]types.ClusterStatus{
		ekstypes.ClusterStatusActive: types.ClusterStatusRunning, ekstypes.ClusterStatusCreating: types.ClusterStatusCreating,
		ekstypes.ClusterStatusPending: types.ClusterStatusCreating, ekstypes.ClusterStatusUpdating: types.ClusterStatusUpdating,
		ekstypes.ClusterStatusDeleting: types.ClusterStatusDeleting, ekstypes.ClusterStatusFailed: types.ClusterStatusError,
	}
	for in, want := range statuses {
		c := p.eksToCluster(&ekstypes.Cluster{Name: aws.String("dev"), Status: in, Version: aws.String("1.33"), Endpoint: aws.String("https://x.eks.amazonaws.com"),
			Arn: aws.String("arn:eks"), CreatedAt: &created, ResourcesVpcConfig: &ekstypes.VpcConfigResponse{VpcId: aws.String("vpc-1")}})
		if c.Status != want {
			t.Errorf("status %s → %s, want %s", in, c.Status, want)
		}
		if c.ID != "aws-dev" || c.Version != "v1.33" || c.Region != "eu-west-1" || c.Endpoint != "https://x.eks.amazonaws.com" || !c.CreatedAt.Equal(created) {
			t.Errorf("unexpected cluster %+v", c)
		}
		if c.Metadata["mode"] != clusterModeEKS || c.Metadata["vpcId"] != "vpc-1" || c.Metadata["arn"] != "arn:eks" {
			t.Errorf("unexpected metadata %+v", c.Metadata)
		}
	}
	if c := p.eksToCluster(&ekstypes.Cluster{Name: aws.String("dev"), Status: "WEIRD"}); c.Status != types.ClusterStatusUnknown || c.Version != "" {
		t.Errorf("unknown status must map to unknown with no version: %+v", c)
	}
}

func TestEKSNodegroupToNodeGroupMapsStatusAndScaling(t *testing.T) {
	statuses := map[ekstypes.NodegroupStatus]types.NodeGroupStatus{
		ekstypes.NodegroupStatusActive: types.NodeGroupStatusReady, ekstypes.NodegroupStatusCreating: types.NodeGroupStatusCreating,
		ekstypes.NodegroupStatusUpdating: types.NodeGroupStatusScaling, ekstypes.NodegroupStatusDeleting: types.NodeGroupStatusDeleting,
		ekstypes.NodegroupStatusDegraded: types.NodeGroupStatusError, ekstypes.NodegroupStatusCreateFailed: types.NodeGroupStatusError,
	}
	for in, want := range statuses {
		g := eksNodegroupToNodeGroup(&ekstypes.Nodegroup{NodegroupName: aws.String("workers"), Status: in,
			ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(4)}, InstanceTypes: []string{"t3.large"}, Labels: map[string]string{"role": "worker"}})
		if g.Status != want || g.Replicas != 4 || g.InstanceType != "t3.large" || g.Name != "workers" || g.Labels["role"] != "worker" {
			t.Errorf("status %s: unexpected node group %+v (want status %s)", in, g, want)
		}
	}
	if g := eksNodegroupToNodeGroup(&ekstypes.Nodegroup{NodegroupName: aws.String("bare")}); g.Replicas != 0 || g.InstanceType != "" {
		t.Errorf("a node group without scaling config must not panic: %+v", g)
	}
}

func TestIsEKSNotFoundOnlyMatchesTheResourceNotFoundType(t *testing.T) {
	if !isEKSNotFound(&ekstypes.ResourceNotFoundException{}) {
		t.Error("ResourceNotFoundException must be recognised")
	}
	if isEKSNotFound(&ekstypes.ResourceInUseException{}) || isEKSNotFound(nil) {
		t.Error("other errors are not not-found")
	}
}
