/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package civo

import (
	"strings"
	"testing"

	"adhar-io/adhar/platform/types"

	"github.com/civo/civogo"
)

// Deleting cloud storage is the least reversible thing this provider does and
// cannot be exercised in CI, so the rule that decides is tested exhaustively here.

func TestVolumeDispositionNeverTouchesAnotherClustersStorage(t *testing.T) {
	t.Parallel()
	const cluster = "adhar-mgmt"
	ours := map[string]bool{"i-1": true, "i-2": true}

	cases := []struct {
		name   string
		vol    civogo.Volume
		purge  bool
		want   volumeAction
		reason string
	}{
		{
			name: "attached to one of our instances",
			vol:  civogo.Volume{Name: "pvc-abc", InstanceID: "i-1"},
			want: volumeDelete,
		},
		{
			name: "tagged with our cluster id",
			vol:  civogo.Volume{Name: "pvc-abc", ClusterID: cluster},
			want: volumeDelete,
		},
		{
			name:   "attached to an instance outside the cluster",
			vol:    civogo.Volume{Name: "pvc-abc", InstanceID: "someone-elses"},
			purge:  true,
			want:   volumeKeep,
			reason: "outside this cluster",
		},
		{
			name:   "belongs to another Kubernetes cluster",
			vol:    civogo.Volume{Name: "pvc-abc", ClusterID: "other-cluster"},
			purge:  true,
			want:   volumeKeep,
			reason: "another Kubernetes cluster",
		},
		{
			name:   "unattached CSI volume without the purge flag",
			vol:    civogo.Volume{Name: "pvc-abc"},
			want:   volumeKeep,
			reason: "--purge-orphaned-volumes",
		},
		{
			name:  "unattached CSI volume with the purge flag",
			vol:   civogo.Volume{Name: "pvc-abc"},
			purge: true,
			want:  volumeDeleteOrphan,
		},
		{
			name:   "somebody's hand-made volume is never an orphan",
			vol:    civogo.Volume{Name: "my-database-backup"},
			purge:  true,
			want:   volumeKeep,
			reason: "not created by this platform",
		},
		{
			name:   "a root disk is left to the instance deletion",
			vol:    civogo.Volume{Name: "pvc-abc", Bootable: true, InstanceID: "i-1"},
			purge:  true,
			want:   volumeKeep,
			reason: "bootable",
		},
	}
	for _, c := range cases {
		got, why := volumeDisposition(c.vol, cluster, ours, c.purge)
		if got != c.want {
			t.Errorf("%s: got %v (%s), want %v", c.name, got, why, c.want)
		}
		if c.reason != "" && !strings.Contains(why, c.reason) {
			t.Errorf("%s: reason %q does not mention %q", c.name, why, c.reason)
		}
	}
}

func TestLoadBalancerMatchIsExactAndNeverClaimsASibling(t *testing.T) {
	t.Parallel()
	p := &Provider{config: &Config{}}
	id := p.clusterIdentityFor("adhar-mgmt", []civogo.Instance{
		{ID: "i-1", Hostname: "adhar-adhar-mgmt-master-1", PublicIP: "1.2.3.4", PrivateIP: "10.0.0.4"},
		{ID: "i-2", Hostname: "adhar-adhar-mgmt-worker-1", PrivateIP: "10.0.0.5"},
	})

	mine := []civogo.LoadBalancer{
		{Name: "adhar-mgmt"},
		{ClusterID: "adhar-mgmt", Name: "anything-at-all"},
		{Name: "x", InstancePool: []civogo.InstancePool{{Tags: []string{"adhar-cluster-adhar-mgmt"}}}},
		{Name: "x", InstancePool: []civogo.InstancePool{{Names: []string{"adhar-adhar-mgmt-worker-1"}}}},
		{Name: "x", Backends: []civogo.LoadBalancerBackend{{IP: "10.0.0.5"}}},
	}
	for _, lb := range mine {
		if !loadBalancerBelongsToCluster(lb, id) {
			t.Errorf("load balancer %q should belong to adhar-mgmt", lb.Name)
		}
	}

	// The sibling-cluster trap: a load balancer whose NAME merely starts with a
	// shorter cluster's name must not be claimed by it, or tearing down "adhar"
	// takes "adhar-mgmt"'s traffic down. Identity, not prefixes.
	shortCluster := p.clusterIdentityFor("adhar", nil)
	notMine := []civogo.LoadBalancer{
		{Name: "adhar-mgmt-gateway"},
		{ServiceName: "adhar-mgmt/gateway"},
		{ClusterID: "adhar-mgmt"},
		{Name: "x", InstancePool: []civogo.InstancePool{{Tags: []string{"adhar-cluster-adhar-mgmt"}}}},
		{Name: "x", Backends: []civogo.LoadBalancerBackend{{IP: "10.0.0.5"}}},
	}
	for _, lb := range notMine {
		if loadBalancerBelongsToCluster(lb, shortCluster) {
			t.Errorf("cluster adhar must not claim %+v", lb)
		}
	}

	if loadBalancerBelongsToCluster(civogo.LoadBalancer{Name: "anything"}, p.clusterIdentityFor("", nil)) {
		t.Error("an empty cluster name must match nothing")
	}
}

func TestQuotaShortfallNamesEveryExhaustedLimit(t *testing.T) {
	t.Parallel()
	q := &civogo.Quota{
		InstanceCountLimit: 16, InstanceCountUsage: 14,
		CPUCoreLimit: 16, CPUCoreUsage: 8,
		RAMMegabytesLimit: 32768, RAMMegabytesUsage: 16384,
		DiskGigabytesLimit: 500, DiskGigabytesUsage: 100,
		LoadBalancerCountLimit: 1, LoadBalancerCountUsage: 1,
		NetworkCountLimit: 10, NetworkCountUsage: 1,
	}
	// 4 nodes × 2 cores fits the CPU limit but not the instance count.
	got := quotaShortfall(q, 4, 2, 4096, 50)
	joined := strings.Join(got, "; ")
	if !strings.Contains(joined, "instances") {
		t.Errorf("shortfall %q does not mention the exhausted instance count", joined)
	}
	if strings.Contains(joined, "CPU cores") {
		t.Errorf("CPU cores fit but were reported: %q", joined)
	}
	// An exhausted load-balancer slot must NOT fail a create: the Gateway Service
	// sits Pending while the platform still comes up on its node ports.
	if strings.Contains(joined, "load balancer") {
		t.Errorf("the load-balancer quota must not block a create: %q", joined)
	}

	// A cluster that fits reports nothing.
	if got := quotaShortfall(q, 1, 2, 4096, 50); len(got) != 0 {
		t.Errorf("a fitting cluster reported shortfalls: %v", got)
	}
	// A zero limit means "the account reports no limit", not "no capacity".
	if got := quotaShortfall(&civogo.Quota{}, 10, 8, 16384, 100); len(got) != 0 {
		t.Errorf("unreported limits must not block a create: %v", got)
	}
	if got := quotaShortfall(nil, 4, 2, 4096, 50); got != nil {
		t.Errorf("a nil quota must not block a create: %v", got)
	}
}

// The quota preflight must count the cluster it will actually build.
//
// It used to take ONE size and multiply by the node count, which assumed every
// instance was identical. A control plane smaller than the workers is the usual
// shape, so that over-counted: 1 × g3.medium + 2 × g3.xlarge is 14 vCPU, and
// multiplying the worker size by three made it 18 — refusing to build a cluster
// that fitted inside a 16-core quota.
func TestPlannedInstanceSizesMatchWhatCreateWillBuild(t *testing.T) {
	t.Parallel()

	// No node groups: the create path falls back to two workers plus the control
	// plane, all at the configured size.
	if got := plannedInstanceSizes(&types.ClusterSpec{}, sizeMedium, 0); len(got) != 3 {
		t.Errorf("default shape = %v, want 3 instances", got)
	}
	if got := plannedInstanceSizes(&types.ClusterSpec{}, sizeMedium, 4); len(got) != 5 {
		t.Errorf("defaultNodeCount=4 = %v, want 5 instances", got)
	}

	// The mixed shape the old arithmetic got wrong: a small control plane and
	// larger workers must be listed at their own sizes, control plane first.
	spec := &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{InstanceType: sizeMedium},
		NodeGroups: []types.NodeGroupSpec{
			{Name: groupWorkers, Replicas: 2, InstanceType: sizeXLarge},
		},
	}
	got := plannedInstanceSizes(spec, "g3.small", 0)
	want := []string{sizeMedium, sizeXLarge, sizeXLarge}
	if len(got) != len(want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("plan = %v, want %v", got, want)
		}
	}

	// A node group with no size of its own falls back to the provider's.
	spec = &types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{
		{Name: groupWorkers, Replicas: 1, InstanceType: sizeLarge},
		{Name: "gpu", Replicas: 1},
	}}
	got = plannedInstanceSizes(spec, "g3.small", 0)
	if len(got) != 3 || got[1] != sizeLarge || got[2] != "g3.small" {
		t.Errorf("plan = %v, want [g3.small g3.large g3.small] (control plane falls back too)", got)
	}

	// An HA control plane contributes every replica.
	spec = &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{InstanceType: sizeLarge, Replicas: 3},
		NodeGroups:   []types.NodeGroupSpec{{Name: groupWorkers, Replicas: 1, InstanceType: sizeXLarge}},
	}
	if got := plannedInstanceSizes(spec, "g3.small", 0); len(got) != 4 {
		t.Errorf("HA plan = %v, want 4 instances (3 control plane + 1 worker)", got)
	}
}

// Create and teardown must agree on the network's label, or the network leaks.
//
// They did not. Create preferred config.NetworkLabel and fell back to a
// per-cluster convention; teardown only ever matched the convention. Every shipped
// Civo example sets `network_label`, so on a real teardown the network was left
// behind while the confirmation prompt promised to delete it — verified on a live
// mum1 cluster (2026-10-02): instances, firewall and SSH key all went, and
// `label: adhar-network` survived. Civo caps networks per account, so the leak
// eventually blocks new clusters.
func TestNetworkLabelIsTheSameForCreateAndTeardown(t *testing.T) {
	t.Parallel()

	// Configured label wins, and both paths see the same string.
	configured := &Provider{config: &Config{NetworkLabel: "adhar-network"}}
	if got := configured.computeNetworkLabel("adhar"); got != "adhar-network" {
		t.Errorf("configured label = %q, want the operator's adhar-network", got)
	}

	// No label configured: the per-cluster convention.
	convention := &Provider{config: &Config{}}
	if got := convention.computeNetworkLabel("adhar"); got != "adhar-adhar-net" {
		t.Errorf("default label = %q, want adhar-adhar-net", got)
	}

	// The bug in one assertion: the configured case must NOT resolve to the
	// convention, which is what teardown used to look for.
	if configured.computeNetworkLabel("adhar") == convention.computeNetworkLabel("adhar") {
		t.Error("a configured network_label must not collapse to the convention; that mismatch is what leaked the network")
	}
}
