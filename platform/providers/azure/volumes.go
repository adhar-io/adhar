/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package azure

// The managed-disk half of `adhar down`'s volume step (see
// platform/providers/volume_attribution.go).
//
// Azure differs from the other clouds in one way that matters here: the disk
// CSI driver puts a cluster's disks in the cluster's RESOURCE GROUP, and when
// Adhar created that group, deleting it used to delete them — without the list
// being shown and whatever --keep-volumes said. So under `adhar down`
// (KeepClusterVolumes) DeleteCluster removes the cluster's other resources one by
// one and leaves a group that still holds CSI disks; the volume step deletes the
// disks, and AfterClusterVolumes deletes the group once nothing is left in it.
//
// A cluster built since attribution existed tags every disk
// `adhar-cluster-id=<install id>` (blockStorageParameters). Disks from before
// that are the driver's: tagged `k8s-azure-created-by` and
// `kubernetes.io-created-for-pvc-*`, in the cluster's own resource group, and
// offered only when no VM is left in that group.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"

	provider "adhar-io/adhar/platform/providers"
)

// azureVolumeAPI is the slice of Azure the volume step uses.
type azureVolumeAPI interface {
	// listDisks lists every managed disk in the subscription.
	listDisks(ctx context.Context) ([]*armcompute.Disk, error)
	listDisksInGroup(ctx context.Context, rg string) ([]*armcompute.Disk, error)
	getDisk(ctx context.Context, rg, name string) (*armcompute.Disk, error)
	// deleteDisk starts the deletion; the volume step verifies it.
	deleteDisk(ctx context.Context, rg, name string) error
	listVMsInGroup(ctx context.Context, rg string) ([]*armcompute.VirtualMachine, error)
}

var (
	_ provider.ClusterVolumeManager   = (*Provider)(nil)
	_ provider.ClusterVolumeFinalizer = (*Provider)(nil)
)

const (
	// Tags the Azure disk CSI driver writes on every disk it provisions.
	azureDiskCreatedByTag    = "k8s-azure-created-by"
	azurePVCNameTag          = "kubernetes.io-created-for-pvc-name"
	azurePVCNamespaceTag     = "kubernetes.io-created-for-pvc-namespace"
	azurePVNameTag           = "kubernetes.io-created-for-pv-name"
	azureDiskCreatedByDriver = "kubernetes-azure-dd"
)

type armVolumeAPI struct {
	disks *armcompute.DisksClient
	vms   *armcompute.VirtualMachinesClient
}

func (a armVolumeAPI) listDisks(ctx context.Context) ([]*armcompute.Disk, error) {
	var out []*armcompute.Disk
	pager := a.disks.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Value...)
	}
	return out, nil
}

func (a armVolumeAPI) listDisksInGroup(ctx context.Context, rg string) ([]*armcompute.Disk, error) {
	var out []*armcompute.Disk
	pager := a.disks.NewListByResourceGroupPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Value...)
	}
	return out, nil
}

func (a armVolumeAPI) getDisk(ctx context.Context, rg, name string) (*armcompute.Disk, error) {
	resp, err := a.disks.Get(ctx, rg, name, nil)
	if err != nil {
		return nil, err
	}
	return &resp.Disk, nil
}

func (a armVolumeAPI) deleteDisk(ctx context.Context, rg, name string) error {
	_, err := a.disks.BeginDelete(ctx, rg, name, nil)
	return err
}

func (a armVolumeAPI) listVMsInGroup(ctx context.Context, rg string) ([]*armcompute.VirtualMachine, error) {
	var out []*armcompute.VirtualMachine
	pager := a.vms.NewListPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Value...)
	}
	return out, nil
}

func (p *Provider) volumeClient() (azureVolumeAPI, error) {
	if p.volumeAPI != nil {
		return p.volumeAPI, nil
	}
	if p.diskClient == nil || p.virtualMachineClient == nil {
		return nil, fmt.Errorf("the Azure disk and virtual machine clients are not initialised")
	}
	return armVolumeAPI{disks: p.diskClient, vms: p.virtualMachineClient}, nil
}

// azureNotFound reports whether an ARM error is a 404.
func azureNotFound(err error) bool {
	var re *azcore.ResponseError
	return errors.As(err, &re) && re.StatusCode == http.StatusNotFound
}

// clusterResourceGroup is the group the cluster's VMs — and its CSI disks —
// were created in: the configured one, else `<cluster>-rg`, exactly as
// createClusterInfrastructure chose it.
func (p *Provider) clusterResourceGroup(clusterName string) string {
	if p.config != nil && p.config.ResourceGroup != "" {
		return p.config.ResourceGroup
	}
	return clusterName + "-rg"
}

// ClusterVolumes lists the managed disks tagged with the owner's install id,
// anywhere in the subscription (a StorageClass may name another group).
func (p *Provider) ClusterVolumes(ctx context.Context, owner provider.VolumeOwner) ([]provider.BlockVolume, error) {
	if owner.InstallID == "" {
		return nil, nil
	}
	api, err := p.volumeClient()
	if err != nil {
		return nil, err
	}
	disks, err := api.listDisks(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing managed disks: %w", err)
	}
	var out []provider.BlockVolume
	for _, d := range disks {
		if tagValue(d.Tags, provider.ClusterIDLabel) != owner.InstallID {
			continue
		}
		out = append(out, azureBlockVolume(d, fmt.Sprintf("tagged %s=%s", provider.ClusterIDLabel, owner.InstallID)))
	}
	provider.SortVolumes(out)
	return out, nil
}

// LegacyVolumes offers the unattached CSI disks with no install id in the
// cluster's own resource group, and only when no VM is left in that group.
func (p *Provider) LegacyVolumes(ctx context.Context, owner provider.VolumeOwner) (provider.LegacyVolumes, error) {
	if owner.ClusterName == "" {
		return provider.LegacyVolumes{Reason: "not provable: no cluster name to find its resource group by"}, nil
	}
	api, err := p.volumeClient()
	if err != nil {
		return provider.LegacyVolumes{}, err
	}
	rg := p.clusterResourceGroup(owner.ClusterName)
	vms, err := api.listVMsInGroup(ctx, rg)
	if azureNotFound(err) {
		return provider.LegacyVolumes{Provable: true, Reason: "resource group " + rg + " no longer exists"}, nil
	}
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing VMs in resource group %s: %w", rg, err)
	}
	if len(vms) > 0 {
		var names []string
		for _, vm := range vms {
			if vm != nil && vm.Name != nil {
				names = append(names, *vm.Name)
			}
		}
		sort.Strings(names)
		return provider.LegacyVolumes{Reason: fmt.Sprintf(
			"not provable: %d VM(s) remain in resource group %s and could own an untagged CSI disk — %s",
			len(vms), rg, strings.Join(names, ", "))}, nil
	}
	disks, err := api.listDisksInGroup(ctx, rg)
	if azureNotFound(err) {
		return provider.LegacyVolumes{Provable: true, Reason: "resource group " + rg + " no longer exists"}, nil
	}
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing disks in resource group %s: %w", rg, err)
	}
	res := provider.LegacyVolumes{
		Provable: true,
		Reason: fmt.Sprintf("no VM remains in %s, the cluster's resource group; each disk is an unattached "+
			"CSI disk in it with no cluster id", rg),
	}
	for _, d := range disks {
		if ok, why := legacyAzureCandidate(d); ok {
			res.Volumes = append(res.Volumes, azureBlockVolume(d, why))
		}
	}
	provider.SortVolumes(res.Volumes)
	return res, nil
}

// GetVolume re-reads one disk by its ARM id.
func (p *Provider) GetVolume(ctx context.Context, id string) (provider.BlockVolume, bool, error) {
	api, err := p.volumeClient()
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	rg, name := resourceGroupFromID(id), lastPathSegment(id)
	if rg == "" || name == "" {
		return provider.BlockVolume{}, false, fmt.Errorf("%q is not a managed disk id", id)
	}
	d, err := api.getDisk(ctx, rg, name)
	if azureNotFound(err) {
		return provider.BlockVolume{}, false, nil
	}
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	return azureBlockVolume(d, ""), true, nil
}

// DeleteVolume starts the deletion of one detached disk.
func (p *Provider) DeleteVolume(ctx context.Context, v provider.BlockVolume) error {
	api, err := p.volumeClient()
	if err != nil {
		return err
	}
	rg, name := resourceGroupFromID(v.ID), lastPathSegment(v.ID)
	if rg == "" || name == "" {
		return fmt.Errorf("%q is not a managed disk id", v.ID)
	}
	if err := api.deleteDisk(ctx, rg, name); err != nil && !azureNotFound(err) {
		return err
	}
	return nil
}

// AfterClusterVolumes removes the cluster's resource group once the volume step
// has left nothing in it — the step DeleteCluster deferred so that disks
// nobody approved deleting were not deleted with the group. Only a group Adhar
// created (managedBy=adhar-platform), and only when no managed disk remains in
// it at all.
func (p *Provider) AfterClusterVolumes(ctx context.Context, owner provider.VolumeOwner, remaining []provider.BlockVolume) error {
	if !p.config.KeepClusterVolumes || owner.ClusterName == "" || p.resourceGroupClient == nil {
		return nil
	}
	rg := p.clusterResourceGroup(owner.ClusterName)
	for _, v := range remaining {
		if strings.EqualFold(resourceGroupFromID(v.ID), rg) {
			return nil // kept disks live in it: the group stays with them
		}
	}
	group, err := p.resourceGroupClient.Get(ctx, rg, nil)
	if azureNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading resource group %s: %w", rg, err)
	}
	if tagValue(group.ResourceGroup.Tags, "managedBy") != "adhar-platform" {
		return nil
	}
	api, err := p.volumeClient()
	if err != nil {
		return err
	}
	disks, err := api.listDisksInGroup(ctx, rg)
	if err != nil {
		return fmt.Errorf("listing disks in resource group %s: %w", rg, err)
	}
	if len(disks) > 0 {
		return nil
	}
	poller, err := p.resourceGroupClient.BeginDelete(ctx, rg, nil)
	if err != nil {
		return fmt.Errorf("deleting resource group %s: %w", rg, err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("waiting for resource group %s to delete: %w", rg, err)
	}
	return nil
}

// isPersistentVolumeDisk reports whether a disk was made for a
// PersistentVolume — by tag, or by the driver's pvc- naming — as opposed to a
// node's OS disk.
func isPersistentVolumeDisk(d *armcompute.Disk) bool {
	if d == nil {
		return false
	}
	if tagValue(d.Tags, provider.ClusterIDLabel) != "" || tagValue(d.Tags, azurePVCNameTag) != "" ||
		tagValue(d.Tags, azureDiskCreatedByTag) == azureDiskCreatedByDriver {
		return true
	}
	return d.Name != nil && strings.HasPrefix(*d.Name, "pvc-")
}

// legacyAzureCandidate decides whether one disk in the cluster's group is a
// legacy CSI leftover the teardown may offer, and why.
func legacyAzureCandidate(d *armcompute.Disk) (bool, string) {
	if d == nil || d.Name == nil || d.ID == nil {
		return false, "unreadable"
	}
	if diskAttached(d) {
		return false, "attached"
	}
	if id := tagValue(d.Tags, provider.ClusterIDLabel); id != "" {
		return false, "carries install id " + id
	}
	if !isPersistentVolumeDisk(d) {
		// An OS disk goes with its VM in DeleteCluster; a disk someone made by
		// hand is never a CSI leftover.
		return false, "not created by the disk CSI driver"
	}
	return true, "unattached CSI disk with no cluster id in the cluster's resource group; no VM remains there"
}

func diskAttached(d *armcompute.Disk) bool {
	if d.ManagedBy != nil && *d.ManagedBy != "" {
		return true
	}
	if d.Properties != nil && d.Properties.DiskState != nil {
		switch *d.Properties.DiskState {
		case armcompute.DiskStateUnattached:
			return false
		default:
			return true
		}
	}
	return false
}

func azureBlockVolume(d *armcompute.Disk, evidence string) provider.BlockVolume {
	bv := provider.BlockVolume{Evidence: evidence}
	if d == nil {
		return bv
	}
	if d.ID != nil {
		bv.ID = *d.ID
		bv.DeleteCommand = "az disk delete --ids " + *d.ID + " --yes"
	}
	if d.Name != nil {
		bv.Name = *d.Name
	}
	if d.Location != nil {
		bv.Location = *d.Location
	}
	bv.PVCNamespace = tagValue(d.Tags, azurePVCNamespaceTag)
	bv.PVCName = tagValue(d.Tags, azurePVCNameTag)
	if d.Properties != nil {
		if d.Properties.DiskSizeGB != nil {
			bv.SizeGiB = int64(*d.Properties.DiskSizeGB)
		}
		if d.Properties.TimeCreated != nil {
			bv.Created = *d.Properties.TimeCreated
		}
	}
	if d.ManagedBy != nil && *d.ManagedBy != "" {
		bv.AttachedTo = []string{lastPathSegment(*d.ManagedBy)}
	} else if diskAttached(d) {
		bv.AttachedTo = []string{"disk state " + string(*d.Properties.DiskState)}
	}
	return bv
}

func tagValue(tags map[string]*string, key string) string {
	if v, ok := tags[key]; ok && v != nil {
		return *v
	}
	return ""
}

// deleteWholeResourceGroup decides whether DeleteCluster may delete the
// cluster's resource group in one call. Only a group Adhar created — and not
// while it still holds CSI disks that `adhar down` has been told to leave for
// its own, confirmed volume step: deleting the group takes every disk in it.
func deleteWholeResourceGroup(managedByAdhar, keepClusterVolumes, holdsPersistentVolumes bool) bool {
	return managedByAdhar && !(keepClusterVolumes && holdsPersistentVolumes)
}

// groupHoldsPersistentVolumeDisks reports whether a resource group contains
// any disk made for a PersistentVolume. A group that cannot be read is assumed
// to hold some: the cost of that guess is a group deleted one resource at a
// time, the cost of the other is data deleted without asking.
func (p *Provider) groupHoldsPersistentVolumeDisks(ctx context.Context, rg string) bool {
	api, err := p.volumeClient()
	if err != nil {
		return true
	}
	disks, err := api.listDisksInGroup(ctx, rg)
	if err != nil {
		return !azureNotFound(err)
	}
	for _, d := range disks {
		if isPersistentVolumeDisk(d) {
			return true
		}
	}
	return false
}
