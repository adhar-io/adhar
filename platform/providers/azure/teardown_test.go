/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package azure

import (
	"strings"
	"testing"

	"adhar-io/adhar/platform/types"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

func strptr(s string) *string { return &s }

// Azure's teardown deletes a whole resource group, so the risk here is not
// over-deleting within one cluster — it is attributing the WRONG cluster's
// resources, or deleting a disk that belongs to a cluster that still exists.

func TestVMAttributionNeedsBothTheTagAndTheName(t *testing.T) {
	t.Parallel()
	adhar := map[string]*string{"managedBy": strptr("adhar-platform")}

	for _, name := range []string{"adhar-mgmt-master-0", "adhar-mgmt-worker-1", "adhar-mgmt-control-plane-0"} {
		if !vmBelongsToCluster(name, adhar, "adhar-mgmt") {
			t.Errorf("%q should belong to adhar-mgmt", name)
		}
	}

	// A sibling cluster whose name merely starts the same must not be claimed:
	// the name test compares the whole prefix before the role separator.
	for _, name := range []string{"adhar-mgmt-staging-worker-1", "other-master-0"} {
		if vmBelongsToCluster(name, adhar, "adhar-mgmt") {
			t.Errorf("%q must not belong to adhar-mgmt", name)
		}
	}

	// Somebody else's VM that happens to be named like ours is not ours: without
	// the tag there is no claim.
	if vmBelongsToCluster("adhar-mgmt-worker-1", map[string]*string{"managedBy": strptr("terraform")}, "adhar-mgmt") {
		t.Error("a VM without the adhar-platform tag must not be claimed")
	}
	if vmBelongsToCluster("adhar-mgmt-worker-1", nil, "adhar-mgmt") {
		t.Error("a VM with no tags at all must not be claimed")
	}
	// And a VM with the tag but no role in its name is not attributable.
	if vmBelongsToCluster("adhar-mgmt", adhar, "adhar-mgmt") {
		t.Error("a name with no role separator must not be attributed")
	}
	if vmBelongsToCluster("anything", adhar, "") {
		t.Error("an empty cluster name must match nothing")
	}
}

func TestResourceGroupIsReadFromTheARMID(t *testing.T) {
	t.Parallel()
	id := "/subscriptions/0000/resourceGroups/adhar-mgmt-rg/providers/Microsoft.Compute/virtualMachines/adhar-mgmt-master-0"
	if got := resourceGroupFromID(id); got != "adhar-mgmt-rg" {
		t.Errorf("resourceGroupFromID = %q, want adhar-mgmt-rg", got)
	}
	// ARM ids are case-insensitive on the segment names, and the SDK is not
	// consistent about which casing it returns.
	if got := resourceGroupFromID("/subscriptions/0/resourcegroups/rg1/providers/x"); got != "rg1" {
		t.Errorf("lowercase segment: got %q, want rg1", got)
	}
	if got := resourceGroupFromID("not-an-arm-id"); got != "" {
		t.Errorf("a malformed id returned %q, want empty", got)
	}
}

func TestDiskIsOrphanNeverTakesAttachedData(t *testing.T) {
	t.Parallel()
	unattached := armcompute.DiskStateUnattached
	attached := armcompute.DiskStateAttached

	// Attached disks are somebody's live data, purge flag or not.
	d := &armcompute.Disk{Name: strptr("pvc-1"), ManagedBy: strptr("/subscriptions/0/…/vm-1")}
	if orphan, why := diskIsOrphan(d, "adhar-mgmt", true); orphan {
		t.Errorf("an attached disk was reported as an orphan (%s)", why)
	}
	d = &armcompute.Disk{Name: strptr("pvc-2"), Properties: &armcompute.DiskProperties{DiskState: &attached}}
	if orphan, _ := diskIsOrphan(d, "adhar-mgmt", true); orphan {
		t.Error("a disk in state Attached was reported as an orphan")
	}

	// An unattached CSI disk is only removed with the operator's opt-in, and the
	// refusal has to say which flag enables it.
	d = &armcompute.Disk{Name: strptr("pvc-3"), Properties: &armcompute.DiskProperties{DiskState: &unattached}}
	orphan, why := diskIsOrphan(d, "adhar-mgmt", false)
	if orphan {
		t.Error("an unattached CSI disk was removed without the purge flag")
	}
	if !strings.Contains(why, "--purge-orphaned-volumes") {
		t.Errorf("refusal %q does not name the flag that enables it", why)
	}
	if orphan, _ := diskIsOrphan(d, "adhar-mgmt", true); !orphan {
		t.Error("an unattached CSI disk was not removed with the purge flag")
	}

	// A disk nobody's CSI driver made is never an orphan, however unattached.
	d = &armcompute.Disk{Name: strptr("my-data-disk"), Properties: &armcompute.DiskProperties{DiskState: &unattached}}
	if orphan, _ := diskIsOrphan(d, "adhar-mgmt", true); orphan {
		t.Error("a hand-made disk was reported as an orphan")
	}
	if orphan, _ := diskIsOrphan(nil, "adhar-mgmt", true); orphan {
		t.Error("a nil disk must not be reported as an orphan")
	}
}

func TestCCMResourceAttributionIsExact(t *testing.T) {
	t.Parallel()
	adhar := map[string]*string{"managedBy": strptr("adhar-platform")}
	if !azureResourceBelongsToCluster("anything", adhar, "adhar-mgmt") {
		t.Error("a resource carrying Adhar's own tag should be claimed")
	}
	// The Azure CCM names the cluster load balancer after the cluster, and calls
	// the default one "kubernetes".
	for _, name := range []string{"adhar-mgmt", "adhar-mgmt-internal", "kubernetes"} {
		if !azureResourceBelongsToCluster(name, nil, "adhar-mgmt") {
			t.Errorf("%q should be claimed by adhar-mgmt", name)
		}
	}
	if azureResourceBelongsToCluster("unrelated-lb", nil, "adhar-mgmt") {
		t.Error("an unrelated load balancer was claimed")
	}
	if azureResourceBelongsToCluster("adhar-mgmt", nil, "") {
		t.Error("an empty cluster name must match nothing")
	}
}

func TestQuotaShortfallChecksBothTheFamilyAndTheRegionTotal(t *testing.T) {
	t.Parallel()
	usage := func(name string, current int32, limit int64) *armcompute.Usage {
		return &armcompute.Usage{
			Name:         &armcompute.UsageName{Value: &name},
			CurrentValue: &current,
			Limit:        &limit,
		}
	}
	usages := []*armcompute.Usage{
		usage("cores", 10, 20),               // 10 vCPU left
		usage("standardDSv3Family", 0, 100),  // plenty
		usage("standardFSv2Family", 90, 100), // nearly full, but not our family
	}

	// 4 × 4 vCPU = 16 does not fit the 20-vCPU regional total with 10 in use.
	got := quotaShortfall(usages, "standardDSv3Family", 4, 4)
	if len(got) != 1 || !strings.Contains(got[0], "cores") {
		t.Fatalf("regional total not reported: %v", got)
	}
	// Another family being full is not our problem.
	if strings.Contains(strings.Join(got, ";"), "standardFSv2Family") {
		t.Errorf("an unrelated family was reported: %v", got)
	}
	// 2 × 4 = 8 fits.
	if got := quotaShortfall(usages, "standardDSv3Family", 2, 4); len(got) != 0 {
		t.Errorf("a fitting cluster reported %v", got)
	}
	// The family limit alone can fail a create even when the region has room.
	tight := []*armcompute.Usage{usage("cores", 0, 1000), usage("standardDSv3Family", 8, 10)}
	if got := quotaShortfall(tight, "standardDSv3Family", 2, 4); len(got) != 1 {
		t.Errorf("family limit not reported: %v", got)
	}
	if got := quotaShortfall(usages, "standardDSv3Family", 0, 4); got != nil {
		t.Errorf("a zero-node cluster reported %v", got)
	}
}

func TestVMSizeFamilyQuotaName(t *testing.T) {
	t.Parallel()
	// Azure's quota names cannot be looked up from a size through any API, so this
	// derivation is the mapping — and it has to match the names the usage API
	// returns exactly or the family check silently never fires.
	for size, want := range map[string]string{
		"Standard_D4s_v3": "standardDSv3Family",
		"Standard_D2s_v3": "standardDSv3Family",
		"Standard_D8_v3":  "standardDv3Family",
		"Standard_E4s_v5": "standardESv5Family",
		"Standard_F8s_v2": "standardFSv2Family",
	} {
		if got := vmSizeFamilyQuotaName(size); got != want {
			t.Errorf("vmSizeFamilyQuotaName(%q) = %q, want %q", size, got, want)
		}
	}
	// An unrecognisable size yields no family, which makes the check fall back to
	// the regional total rather than matching nothing at all.
	if got := vmSizeFamilyQuotaName(""); got != "" {
		t.Errorf("empty size = %q, want empty", got)
	}
}

func TestPlannedShapeMatchesWhatCreateWillBuild(t *testing.T) {
	t.Parallel()
	if got := plannedNodeCount(&types.ClusterSpec{}); got != 1 {
		t.Errorf("a bare spec = %d VMs, want 1 (the control plane alone)", got)
	}
	spec := &types.ClusterSpec{
		ControlPlane: types.ControlPlaneSpec{Replicas: 3},
		NodeGroups:   []types.NodeGroupSpec{{Name: "workers", Replicas: 4, InstanceType: "Standard_D8s_v3"}},
	}
	if got := plannedNodeCount(spec); got != 7 {
		t.Errorf("3 + 4 = %d VMs, want 7", got)
	}
	if got := plannedVMSize(spec, "Standard_D2s_v3"); got != "Standard_D8s_v3" {
		t.Errorf("size = %q, want the node group's", got)
	}
	if got := plannedVMSize(&types.ClusterSpec{}, "Standard_D2s_v3"); got != "Standard_D2s_v3" {
		t.Errorf("size = %q, want the configured fallback", got)
	}
}
