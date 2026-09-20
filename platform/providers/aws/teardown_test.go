/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package aws

import (
	"strings"
	"testing"

	"adhar-io/adhar/platform/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// AWS teardown is driven entirely by tags, so the tag rules are what must be
// right. Deleting an EBS volume or a load balancer cannot be undone and cannot be
// exercised in CI, so every branch is covered here.

func tags(kv ...string) []ec2types.Tag {
	out := make([]ec2types.Tag, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, ec2types.Tag{Key: aws.String(kv[i]), Value: aws.String(kv[i+1])})
	}
	return out
}

func TestClusterTagMatchingDistinguishesSharedResources(t *testing.T) {
	t.Parallel()
	const cluster = "adhar-mgmt"

	mine, other := resourceBelongsToCluster(map[string]string{clusterTagPrefix + cluster: "owned"}, cluster)
	if !mine || other {
		t.Errorf("an owned resource: mine=%v other=%v", mine, other)
	}

	// The provider's own create-time tag counts too.
	if mine, _ := resourceBelongsToCluster(map[string]string{"Cluster": cluster}, cluster); !mine {
		t.Error("the provider's own Cluster tag was not recognised")
	}

	// A resource shared between two clusters must be reported as both ours and
	// somebody else's, so the sweep can leave it alone.
	mine, other = resourceBelongsToCluster(map[string]string{
		clusterTagPrefix + cluster: "shared",
		clusterTagPrefix + "other": "shared",
	}, cluster)
	if !mine || !other {
		t.Errorf("a shared resource: mine=%v other=%v, want both true", mine, other)
	}
	if ownsExclusively(map[string]string{
		clusterTagPrefix + cluster: "shared",
		clusterTagPrefix + "other": "shared",
	}, cluster) {
		t.Error("a resource tagged for two clusters must not be reported as exclusively ours")
	}

	// An unrelated cluster's resource is neither.
	mine, other = resourceBelongsToCluster(map[string]string{clusterTagPrefix + "someone-else": "owned"}, cluster)
	if mine || !other {
		t.Errorf("another cluster's resource: mine=%v other=%v", mine, other)
	}

	// A name that merely starts with ours is a different cluster: the tag is an
	// exact key, which is why AWS is easier to get right here than a name match.
	if mine, _ := resourceBelongsToCluster(map[string]string{clusterTagPrefix + "adhar-mgmt-staging": "owned"}, cluster); mine {
		t.Error("adhar-mgmt must not claim adhar-mgmt-staging's resources")
	}
}

func TestVolumeDispositionProtectsDataItDoesNotOwn(t *testing.T) {
	t.Parallel()
	const cluster = "adhar-mgmt"
	cases := []struct {
		name   string
		vol    ec2types.Volume
		purge  bool
		want   volumeAction
		reason string
	}{
		{
			name:   "attached volumes are never touched",
			vol:    ec2types.Volume{VolumeId: aws.String("vol-1"), Attachments: []ec2types.VolumeAttachment{{InstanceId: aws.String("i-1")}}},
			purge:  true,
			want:   volumeKeep,
			reason: "still attached",
		},
		{
			name: "tagged for this cluster",
			vol:  ec2types.Volume{VolumeId: aws.String("vol-2"), Tags: tags(clusterTagPrefix+cluster, "owned")},
			want: volumeDelete,
		},
		{
			name:   "tagged for another cluster",
			vol:    ec2types.Volume{VolumeId: aws.String("vol-3"), Tags: tags(clusterTagPrefix+"other", "owned", createdForPVCTag, "data")},
			purge:  true,
			want:   volumeKeep,
			reason: "another Kubernetes cluster",
		},
		{
			name:   "CSI orphan without the flag",
			vol:    ec2types.Volume{VolumeId: aws.String("vol-4"), Tags: tags(createdForPVCTag, "data-0")},
			want:   volumeKeep,
			reason: "--purge-orphaned-volumes",
		},
		{
			name:  "CSI orphan with the flag",
			vol:   ec2types.Volume{VolumeId: aws.String("vol-5"), Tags: tags(createdForPVCTag, "data-0")},
			purge: true,
			want:  volumeDeleteOrphan,
		},
		{
			name:   "somebody's hand-made volume is never an orphan",
			vol:    ec2types.Volume{VolumeId: aws.String("vol-6"), Tags: tags("Name", "my-backup")},
			purge:  true,
			want:   volumeKeep,
			reason: "not provisioned by a CSI driver",
		},
	}
	for _, c := range cases {
		got, why := volumeDisposition(c.vol, cluster, c.purge)
		if got != c.want {
			t.Errorf("%s: got %v (%s), want %v", c.name, got, why, c.want)
		}
		if c.reason != "" && !strings.Contains(why, c.reason) {
			t.Errorf("%s: reason %q does not mention %q", c.name, why, c.reason)
		}
	}
}

func TestQuotaShortfallReportsRealArithmetic(t *testing.T) {
	t.Parallel()
	// A new account's default is 5 vCPU: a 4 × t3.large (2 vCPU) cluster does not
	// fit, and the message must say how much is already in use.
	got := quotaShortfall(5, 2, 8)
	if got == "" {
		t.Fatal("8 vCPU into a 5 vCPU limit reported no shortfall")
	}
	for _, want := range []string{"need 8", "2 of 5"} {
		if !strings.Contains(got, want) {
			t.Errorf("shortfall %q does not contain %q", got, want)
		}
	}
	if s := quotaShortfall(64, 16, 32); s != "" {
		t.Errorf("a fitting cluster reported %q", s)
	}
	// Exactly at the limit fits: the quota is a ceiling, not an exclusive bound.
	if s := quotaShortfall(32, 0, 32); s != "" {
		t.Errorf("a cluster exactly at the limit reported %q", s)
	}
	// An unreadable quota (0) must not block a create.
	if s := quotaShortfall(0, 100, 100); s != "" {
		t.Errorf("an unreported limit reported %q", s)
	}
}

func TestPlannedShapeMatchesWhatCreateWillBuild(t *testing.T) {
	t.Parallel()
	// A spec with no node groups builds the control plane only: the compute path
	// skips zero-replica groups and adds no default pool, so the preflight must
	// not invent a worker that will never exist.
	if got := plannedNodeCount(&types.ClusterSpec{}); got != 1 {
		t.Errorf("a bare spec = %d nodes, want 1 (the control plane alone)", got)
	}
	if got := plannedNodeCount(&types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{{Name: "empty", Replicas: 0}}}); got != 1 {
		t.Errorf("a zero-replica node group = %d nodes, want 1", got)
	}
	spec := &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{Replicas: 1},
		NodeGroups: []types.NodeGroupSpec{
			{Name: "workers", Replicas: 4, InstanceType: "m5.xlarge"},
		},
	}
	if got := plannedNodeCount(spec); got != 5 {
		t.Errorf("1 + 4 = %d nodes, want 5", got)
	}
	if got := plannedInstanceType(spec, nil); got != "m5.xlarge" {
		t.Errorf("instance type = %q, want the node group's m5.xlarge", got)
	}
	if got := plannedInstanceType(&types.ClusterSpec{}, nil); got != "t3.medium" {
		t.Errorf("instance type = %q, want the compute path's own default", got)
	}
}
