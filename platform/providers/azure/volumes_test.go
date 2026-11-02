/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package azure

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"

	provider "adhar-io/adhar/platform/providers"
)

type fakeAzure struct {
	disks   []*armcompute.Disk
	vms     map[string][]*armcompute.VirtualMachine // by resource group
	deleted []string
}

func (f *fakeAzure) listDisks(context.Context) ([]*armcompute.Disk, error) { return f.disks, nil }

func (f *fakeAzure) listDisksInGroup(_ context.Context, rg string) ([]*armcompute.Disk, error) {
	var out []*armcompute.Disk
	for _, d := range f.disks {
		if strings.EqualFold(resourceGroupFromID(*d.ID), rg) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeAzure) getDisk(_ context.Context, rg, name string) (*armcompute.Disk, error) {
	for _, d := range f.disks {
		if strings.EqualFold(resourceGroupFromID(*d.ID), rg) && *d.Name == name {
			return d, nil
		}
	}
	return nil, &azcore.ResponseError{StatusCode: http.StatusNotFound}
}

func (f *fakeAzure) deleteDisk(_ context.Context, rg, name string) error {
	for i, d := range f.disks {
		if strings.EqualFold(resourceGroupFromID(*d.ID), rg) && *d.Name == name {
			f.deleted = append(f.deleted, name)
			f.disks = append(f.disks[:i], f.disks[i+1:]...)
			return nil
		}
	}
	return &azcore.ResponseError{StatusCode: http.StatusNotFound}
}

func (f *fakeAzure) listVMsInGroup(_ context.Context, rg string) ([]*armcompute.VirtualMachine, error) {
	return f.vms[rg], nil
}

// csiDisk is a disk the way the Azure disk CSI driver creates one.
func csiDisk(rg, name string, extraTags map[string]string, attachedTo string) *armcompute.Disk {
	tags := map[string]*string{
		azureDiskCreatedByTag: to.Ptr(azureDiskCreatedByDriver),
		azurePVCNameTag:       to.Ptr("data-" + name),
		azurePVCNamespaceTag:  to.Ptr("adhar-system"),
		azurePVNameTag:        to.Ptr(name),
	}
	for k, v := range extraTags {
		tags[k] = to.Ptr(v)
	}
	state := armcompute.DiskStateUnattached
	d := &armcompute.Disk{
		ID:       to.Ptr("/subscriptions/s/resourceGroups/" + rg + "/providers/Microsoft.Compute/disks/" + name),
		Name:     to.Ptr(name),
		Location: to.Ptr("centralindia"),
		Tags:     tags,
		Properties: &armcompute.DiskProperties{
			DiskSizeGB:  to.Ptr[int32](8),
			DiskState:   &state,
			TimeCreated: to.Ptr(time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)),
		},
	}
	if attachedTo != "" {
		attached := armcompute.DiskStateAttached
		d.Properties.DiskState = &attached
		d.ManagedBy = to.Ptr("/subscriptions/s/resourceGroups/" + rg + "/providers/Microsoft.Compute/virtualMachines/" + attachedTo)
	}
	return d
}

func azureWith(f *fakeAzure) *Provider {
	return &Provider{config: &Config{Location: "centralindia", KeepClusterVolumes: true}, volumeAPI: f}
}

func TestAzureClusterVolumesAreExactlyThisInstallsDisks(t *testing.T) {
	f := &fakeAzure{disks: []*armcompute.Disk{
		csiDisk("adhar-rg", "pvc-mine", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, "adhar-worker-1"),
		csiDisk("elsewhere-rg", "pvc-mine-in-another-group", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, ""),
		csiDisk("adhar-rg", "pvc-other-install", map[string]string{provider.ClusterIDLabel: "bbbbbbbbbbbbbbbb"}, ""),
		csiDisk("adhar-rg", "pvc-legacy", nil, ""),
	}}
	got, err := azureWith(f).ClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar", InstallID: "aaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ClusterVolumes = %+v, want the two disks tagged with this install id", got)
	}
	for _, v := range got {
		if !strings.HasPrefix(v.Name, "pvc-mine") || v.PVC() != "adhar-system/data-"+v.Name || v.SizeGiB != 8 {
			t.Errorf("unexpected or incomplete volume %+v", v)
		}
		if v.DeleteCommand != "az disk delete --ids "+v.ID+" --yes" {
			t.Errorf("delete command = %q", v.DeleteCommand)
		}
	}
	if got[0].Name == "pvc-mine" && !got[0].Attached() {
		t.Error("the attached disk lost its attachment")
	}
}

func TestAzureLegacyDisksRequireAnEmptyClusterResourceGroup(t *testing.T) {
	disks := []*armcompute.Disk{
		csiDisk("adhar-rg", "pvc-legacy", nil, ""),
		csiDisk("adhar-rg", "pvc-has-id", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, ""),
		csiDisk("adhar-rg", "pvc-attached", nil, "somevm"),
		csiDisk("other-rg", "pvc-other-group", nil, ""),
		{ID: to.Ptr("/subscriptions/s/resourceGroups/adhar-rg/providers/Microsoft.Compute/disks/adhar-master-1_OsDisk_1"),
			Name: to.Ptr("adhar-master-1_OsDisk_1")},
	}
	busy := &fakeAzure{disks: disks, vms: map[string][]*armcompute.VirtualMachine{"adhar-rg": {{Name: to.Ptr("adhar-worker-1")}}}}
	got, err := azureWith(busy).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provable || len(got.Volumes) != 0 {
		t.Fatalf("with a VM still in the group the legacy disks were offered: %+v", got)
	}

	empty := &fakeAzure{disks: disks}
	got, err = azureWith(empty).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Provable || len(got.Volumes) != 1 || got.Volumes[0].Name != "pvc-legacy" {
		t.Fatalf("legacy = %+v, want only pvc-legacy", got)
	}
}

// Under `adhar down` an Adhar-created group that still holds CSI disks is NOT
// deleted in one call: that would delete the disks before anyone was asked.
func TestAnAdharGroupHoldingVolumesIsNotDeletedWholesaleByAdharDown(t *testing.T) {
	cases := []struct {
		owned, keep, holds, want bool
	}{
		{owned: true, keep: false, holds: true, want: true},    // adhar cluster delete: unchanged
		{owned: true, keep: true, holds: false, want: true},    // nothing to keep
		{owned: true, keep: true, holds: true, want: false},    // adhar down: disks first
		{owned: false, keep: false, holds: false, want: false}, // never someone else's group
	}
	for _, c := range cases {
		if got := deleteWholeResourceGroup(c.owned, c.keep, c.holds); got != c.want {
			t.Errorf("deleteWholeResourceGroup(owned=%v keep=%v holds=%v) = %v, want %v", c.owned, c.keep, c.holds, got, c.want)
		}
	}
	if !azureWith(&fakeAzure{disks: []*armcompute.Disk{csiDisk("adhar-rg", "pvc-1", nil, "")}}).groupHoldsPersistentVolumeDisks(context.Background(), "adhar-rg") {
		t.Error("a group holding a CSI disk was reported empty")
	}
	if azureWith(&fakeAzure{}).groupHoldsPersistentVolumeDisks(context.Background(), "adhar-rg") {
		t.Error("an empty group was reported as holding volumes")
	}
}

// An install id decides on its own: this cluster's goes with it, another's
// never does, and a subscription-wide purge takes none.
func TestAzureDiskDispositionFollowsTheInstallID(t *testing.T) {
	mine := csiDisk("shared-rg", "pvc-mine", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, "")
	theirs := csiDisk("shared-rg", "pvc-theirs", map[string]string{provider.ClusterIDLabel: "bbbbbbbbbbbbbbbb"}, "")
	if ok, why := diskDisposition(mine, "adhar", "aaaaaaaaaaaaaaaa", false); !ok {
		t.Errorf("this cluster's disk was kept: %s", why)
	}
	if ok, why := diskDisposition(theirs, "adhar", "aaaaaaaaaaaaaaaa", true); ok {
		t.Errorf("another install's disk in a shared group was taken: %s", why)
	}
	if ok, _ := diskIsOrphan(mine, "", true); ok {
		t.Error("the subscription-wide purge took a disk that carries an install id")
	}
	if !isPersistentVolumeDisk(csiDisk("g", "pvc-x", nil, "")) || isPersistentVolumeDisk(&armcompute.Disk{Name: to.Ptr("adhar-master-1_OsDisk_1")}) {
		t.Error("isPersistentVolumeDisk confuses CSI disks and OS disks")
	}
}

func TestAzureVolumeStepDeletesDetachedDisksAndVerifies(t *testing.T) {
	f := &fakeAzure{disks: []*armcompute.Disk{
		csiDisk("adhar-rg", "pvc-free", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, ""),
		csiDisk("adhar-rg", "pvc-held", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, "not-ours"),
	}}
	p := azureWith(f)
	vols, _ := p.ClusterVolumes(context.Background(), provider.VolumeOwner{InstallID: "aaaaaaaaaaaaaaaa"})
	res := provider.ReapVolumes(context.Background(), p, vols, provider.ReapOptions{
		DetachTimeout: 20 * time.Millisecond, VerifyTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond,
	})
	if len(res.Deleted) != 1 || res.Deleted[0].Name != "pvc-free" || len(res.StillAttached) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if strings.Join(f.deleted, ",") != "pvc-free" {
		t.Fatalf("deleted %v", f.deleted)
	}
}

// The install id must reach adhar-block (and so adhar-ci, the controller's copy
// of it) as an Azure disk tag.
func TestTheInstallIDReachesTheAzureBlockStorageClass(t *testing.T) {
	p := &Provider{config: &Config{TenantID: "t", SubscriptionID: "s", ClientID: "c", ClientSecret: "x", Location: "centralindia"}}
	steps, err := p.cloudIntegrationSteps("adhar", "adhar-rg", "vnet", "subnet", "nsg", "a1b2c3d4e5f6g7h8")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range steps {
		if strings.Contains(s.Cmd, "name: adhar-block") {
			found = true
			if !strings.Contains(s.Cmd, `tags: "adhar-cluster-id=a1b2c3d4e5f6g7h8"`) {
				t.Errorf("adhar-block does not tag its disks with the install id:\n%s", s.Cmd)
			}
		}
	}
	if !found {
		t.Fatal("adhar-block is no longer created; this check checks nothing")
	}
	if blockStorageParameters("")["tags"] != "" {
		t.Error("no install id still produced a tags parameter")
	}
}
