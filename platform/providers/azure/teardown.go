/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package azure

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"adhar-io/adhar/platform/types"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

// Teardown and preflight, brought up to the level the GCP and DigitalOcean
// providers reached under live use.
//
// AZURE IS DIFFERENT FROM THE OTHER CLOUDS HERE, and in a way that does most of
// the work for us: everything a cluster owns lives in one resource group, so
// deleting the group takes the VMs, the NICs, the load balancer the
// cloud-controller-manager created and the disks the CSI driver created, all at
// once. What follows fills the three holes that leaves.
//
//  1. NO TRACKER, NO TEARDOWN. DeleteCluster used to require an entry in the
//     local state file and return "cluster not found" without one — so a cluster
//     created on another machine, or one whose state file was lost, could be
//     LISTED (discoverExistingClusters finds it) but never deleted, and the
//     operator was left to the portal. trackerFor rebuilds the tracker from Azure
//     itself.
//  2. A RESOURCE GROUP WE DO NOT OWN. When the group was not created by Adhar —
//     an operator pointed the platform at an existing group — the old code logged
//     "manual cleanup required" and stopped. deleteClusterResources removes the
//     cluster's own resources and leaves the group alone, which is what "we do not
//     own this group" should mean.
//  3. DISKS OUTSIDE THE GROUP. A CSI driver configured with its own resource
//     group, or a StorageClass left from an earlier cluster, leaves unattached
//     `pvc-*` disks that the group deletion never sees. Those keep billing, and
//     removing them is opt-in for the same reason as everywhere else.

// trackerFor returns the tracker for a cluster, rebuilding it from Azure when the
// local state file has no record of it.
func (p *Provider) trackerFor(ctx context.Context, clusterID string) (*ResourceTracker, error) {
	if t, ok := p.resourceTrackers[clusterID]; ok && t != nil {
		return t, nil
	}
	name := extractClusterName(clusterID)
	log.Printf("No local state for cluster %s; rediscovering its resources from Azure", clusterID)

	t, err := p.discoverClusterResources(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(t.VirtualMachines) == 0 {
		// No VMs is NOT nothing to do. A teardown that deleted the VMs and stopped,
		// or a create that failed before any VM existed, leaves the network, the
		// security group, the NICs, the public IPs and the OS disks behind — all
		// billing. Refusing here is how those became unreachable: `adhar down`
		// answered "not found" about a resource group it had itself filled
		// (2026-09-26).
		//
		// The resource group comes from configuration in this case rather than from
		// a VM's id, and mergeDiscoveredResources finds the rest by name prefix.
		if p.config.ResourceGroup == "" {
			return nil, fmt.Errorf("cluster %s not found: no VM matches it and no "+
				"resourceGroup is configured to search for leftovers", clusterID)
		}
		t.ResourceGroup = p.config.ResourceGroup
		log.Printf("No VMs remain for %s; searching resource group %s for leftover resources",
			name, t.ResourceGroup)
		return t, nil
	}
	log.Printf("Rediscovered cluster %s: %d VM(s) in resource group %s", name, len(t.VirtualMachines), t.ResourceGroup)
	return t, nil
}

// discoverClusterResources rebuilds a tracker by scanning the subscription.
//
// VMs are the anchor because they are the one resource whose name carries the
// cluster (`<cluster>-master-N` / `<cluster>-worker-N`) AND whose id carries the
// resource group — which is what the rest of the teardown needs.
func (p *Provider) discoverClusterResources(ctx context.Context, clusterName string) (*ResourceTracker, error) {
	if p.virtualMachineClient == nil {
		return nil, fmt.Errorf("virtual machine client not initialized")
	}
	t := &ResourceTracker{
		SubscriptionID: p.config.SubscriptionID,
		Location:       p.config.Location,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	pager := p.virtualMachineClient.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMs while rediscovering cluster %s: %w", clusterName, err)
		}
		for _, vm := range page.Value {
			if vm == nil || vm.Name == nil || vm.ID == nil {
				continue
			}
			if !vmBelongsToCluster(*vm.Name, vm.Tags, clusterName) {
				continue
			}
			t.VirtualMachines = append(t.VirtualMachines, *vm.Name)
			if rg := resourceGroupFromID(*vm.ID); rg != "" && t.ResourceGroup == "" {
				t.ResourceGroup = rg
			}
			if vm.Location != nil && t.Location == "" {
				t.Location = *vm.Location
			}
		}
	}
	if t.ResourceGroup == "" {
		t.ResourceGroup = p.config.ResourceGroup
	}
	return t, nil
}

// vmBelongsToCluster decides whether a VM is part of a named cluster.
//
// Both halves are required. The tag alone would claim every Adhar cluster in the
// subscription, and the name alone would claim a VM someone else happened to call
// `adhar-worker-1`. The name test requires the cluster name to be followed by a
// role separator, so cluster "adhar" does not swallow "adhar-staging".
func vmBelongsToCluster(vmName string, tags map[string]*string, clusterName string) bool {
	if clusterName == "" {
		return false
	}
	managed := false
	if v, ok := tags["managedBy"]; ok && v != nil && *v == "adhar-platform" {
		managed = true
	}
	if !managed {
		return false
	}
	for _, sep := range []string{"-master-", "-worker-", "-control-plane-"} {
		if idx := strings.Index(vmName, sep); idx > 0 && vmName[:idx] == clusterName {
			return true
		}
	}
	return false
}

// resourceGroupFromID pulls the resource group out of an ARM resource id, which
// is the only place the group appears when a resource is found by a subscription
// -wide list.
func resourceGroupFromID(id string) string {
	parts := strings.Split(id, "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Resource-level teardown, for a group Adhar does not own
// ---------------------------------------------------------------------------

// deleteClusterResources removes a cluster's own resources and leaves the
// resource group in place. Order matters: Azure refuses to delete a NIC attached
// to a VM, a subnet with a NIC in it, or a disk attached to anything.
func (p *Provider) deleteClusterResources(ctx context.Context, t *ResourceTracker, clusterName string) []string {
	var problems []string
	rg := t.ResourceGroup

	// The tracker is a CACHE, not the source of truth — so ask the resource group
	// what is actually there and merge anything it missed.
	//
	// Every loop below iterates a tracker list. A create that failed before saving
	// its state therefore left them all empty, and the teardown deleted only the
	// VMs it could find by name while reporting success. Measured on a real run
	// (2026-09-26): after "Teardown complete — removed: dev", the resource group
	// still held a virtual network, a security group, two NICs, two public IPs and
	// two unattached OS disks, all billing. GCP had the identical problem and the
	// identical fix (discoverUntrackedClusters).
	p.mergeDiscoveredResources(ctx, rg, clusterName, t)

	for _, vm := range t.VirtualMachines {
		poller, err := p.virtualMachineClient.BeginDelete(ctx, rg, vm, nil)
		if err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete VM %s: %v", vm, err))
			continue
		}
		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed waiting for VM %s to delete: %v", vm, err))
			continue
		}
		log.Printf("Deleted VM %s", vm)
	}

	for _, nic := range t.NetworkInterfaces {
		if poller, err := p.networkInterfaceClient.BeginDelete(ctx, rg, nic, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete NIC %s: %v", nic, err))
		} else if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed waiting for NIC %s: %v", nic, err))
		}
	}

	// The load balancer and public IPs the cloud-controller-manager created are
	// not in the tracker — they are found by name here, not remembered.
	problems = append(problems, p.sweepLoadBalancers(ctx, rg, clusterName)...)
	problems = append(problems, p.sweepPublicIPs(ctx, rg, t)...)
	problems = append(problems, p.sweepClusterDisks(ctx, rg, clusterName)...)

	for _, nsg := range t.NetworkSecurityGroups {
		if poller, err := p.networkSecurityGroupClient.BeginDelete(ctx, rg, nsg, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete NSG %s: %v", nsg, err))
		} else if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed waiting for NSG %s: %v", nsg, err))
		}
	}
	for _, vnet := range t.VirtualNetworks {
		if poller, err := p.virtualNetworkClient.BeginDelete(ctx, rg, vnet, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete VNet %s: %v", vnet, err))
		} else if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed waiting for VNet %s: %v", vnet, err))
		}
	}
	return problems
}

// mergeDiscoveredResources adds anything in the resource group that belongs to
// this cluster but is absent from the tracker.
//
// Matching is by NAME PREFIX (`<cluster>-`), which is how every resource this
// provider creates is named, and it deliberately does not require the
// `managedBy` tag: a resource created just before a failure may never have been
// tagged, and those are exactly the ones that leak.
func (p *Provider) mergeDiscoveredResources(ctx context.Context, rg, clusterName string, t *ResourceTracker) {
	prefix := clusterName + "-"
	add := func(have []string, name string) []string {
		for _, h := range have {
			if strings.EqualFold(h, name) {
				return have
			}
		}
		log.Printf("Teardown discovered untracked resource %s", name)
		return append(have, name)
	}

	if p.virtualMachineClient != nil {
		pager := p.virtualMachineClient.NewListPager(rg, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				break
			}
			for _, v := range page.Value {
				if v != nil && v.Name != nil && strings.HasPrefix(*v.Name, prefix) {
					t.VirtualMachines = add(t.VirtualMachines, *v.Name)
				}
			}
		}
	}
	if p.networkInterfaceClient != nil {
		pager := p.networkInterfaceClient.NewListPager(rg, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				break
			}
			for _, n := range page.Value {
				if n != nil && n.Name != nil && strings.HasPrefix(*n.Name, prefix) {
					t.NetworkInterfaces = add(t.NetworkInterfaces, *n.Name)
				}
			}
		}
	}
	if p.networkSecurityGroupClient != nil {
		pager := p.networkSecurityGroupClient.NewListPager(rg, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				break
			}
			for _, g := range page.Value {
				if g != nil && g.Name != nil && strings.HasPrefix(*g.Name, prefix) {
					t.NetworkSecurityGroups = add(t.NetworkSecurityGroups, *g.Name)
				}
			}
		}
	}
	if p.virtualNetworkClient != nil {
		pager := p.virtualNetworkClient.NewListPager(rg, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				break
			}
			for _, v := range page.Value {
				if v != nil && v.Name != nil && strings.HasPrefix(*v.Name, prefix) {
					t.VirtualNetworks = add(t.VirtualNetworks, *v.Name)
				}
			}
		}
	}
	// Public IPs last but far from least: sweepPublicIPs only walks the tracker, so
	// two of these survived an otherwise-complete teardown. A public address that is
	// never released keeps billing and, on a static SKU, stays reserved.
	if p.publicIPClient != nil {
		pager := p.publicIPClient.NewListPager(rg, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				break
			}
			for _, ip := range page.Value {
				if ip != nil && ip.Name != nil && strings.HasPrefix(*ip.Name, prefix) {
					t.PublicIPs = add(t.PublicIPs, *ip.Name)
				}
			}
		}
	}
}

// sweepLoadBalancers deletes the load balancers the in-cluster CCM created for
// Services of type LoadBalancer. They are the resource most likely to hold the
// subnet and to keep billing after everything visible is gone.
func (p *Provider) sweepLoadBalancers(ctx context.Context, rg, clusterName string) []string {
	if p.loadBalancerClient == nil {
		return nil
	}
	var problems []string
	pager := p.loadBalancerClient.NewListPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Printf("Warning: could not list load balancers in %s: %v", rg, err)
			return problems
		}
		for _, lb := range page.Value {
			if lb == nil || lb.Name == nil {
				continue
			}
			if !azureResourceBelongsToCluster(*lb.Name, lb.Tags, clusterName) {
				continue
			}
			poller, err := p.loadBalancerClient.BeginDelete(ctx, rg, *lb.Name, nil)
			if err != nil {
				problems = append(problems, fmt.Sprintf("failed to delete load balancer %s: %v", *lb.Name, err))
				continue
			}
			if _, err := poller.PollUntilDone(ctx, nil); err != nil {
				problems = append(problems, fmt.Sprintf("failed waiting for load balancer %s: %v", *lb.Name, err))
				continue
			}
			log.Printf("Deleted load balancer %s", *lb.Name)
		}
	}
	return problems
}

// sweepPublicIPs releases the tracked public addresses. An unreleased static IP
// is billed whether or not anything is attached to it.
func (p *Provider) sweepPublicIPs(ctx context.Context, rg string, t *ResourceTracker) []string {
	if p.publicIPClient == nil {
		return nil
	}
	var problems []string
	for _, ip := range t.PublicIPs {
		poller, err := p.publicIPClient.BeginDelete(ctx, rg, ip, nil)
		if err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete public IP %s: %v", ip, err))
			continue
		}
		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			problems = append(problems, fmt.Sprintf("failed waiting for public IP %s: %v", ip, err))
		}
	}
	return problems
}

// azureResourceBelongsToCluster is the shared name/tag test for resources the
// CCM created, which inherit neither Adhar's tags nor a predictable id.
func azureResourceBelongsToCluster(name string, tags map[string]*string, clusterName string) bool {
	if clusterName == "" {
		return false
	}
	if v, ok := tags["managedBy"]; ok && v != nil && *v == "adhar-platform" {
		return true
	}
	// The Azure CCM names the cluster's load balancer after the cluster, and its
	// per-Service rules after the Service. An exact match or a name-plus-separator
	// prefix is the cluster's; a bare substring would claim a sibling cluster's.
	if name == clusterName || name == "kubernetes" {
		return true
	}
	return strings.HasPrefix(name, clusterName+"-")
}

// ---------------------------------------------------------------------------
// Orphaned disks
// ---------------------------------------------------------------------------

// diskIsOrphan reports whether an unattached disk is a CSI leftover this teardown
// may remove, and why. Kept separate from the sweep so the rule can be tested
// without a subscription.
func diskIsOrphan(d *armcompute.Disk, clusterName string, purge bool) (bool, string) {
	if d == nil || d.Name == nil {
		return false, ""
	}
	name := *d.Name
	// Attached disks are somebody's data, including a running cluster's.
	if d.ManagedBy != nil && *d.ManagedBy != "" {
		return false, "attached to " + *d.ManagedBy
	}
	if d.Properties != nil && d.Properties.DiskState != nil && *d.Properties.DiskState != armcompute.DiskStateUnattached {
		return false, "disk state is " + string(*d.Properties.DiskState)
	}
	// A disk this cluster owns goes regardless of the purge flag: the cluster it
	// belonged to is the one being deleted.
	if v, ok := d.Tags["kubernetes.io-created-for-pvc-namespace"]; ok && v != nil && clusterName != "" {
		return true, "CSI disk of this cluster"
	}
	// A node's OS disk. Azure does NOT delete these with the VM unless the VM was
	// created asking it to, so they outlive the cluster and bill indefinitely.
	// After one real teardown two of them remained `Unattached` in the resource
	// group, and nothing would ever have removed them: they are not CSI disks, so
	// the pvc- test below skipped them, and they carry no PVC tag (2026-09-26).
	//
	// They hold a destroyed node's root filesystem — no user data — so they go with
	// the cluster and do NOT need --purge-orphaned-volumes. VMs are created with
	// DeleteOption=Delete now, which stops new ones being orphaned; this covers
	// clusters built before that.
	if clusterName != "" && strings.HasPrefix(name, clusterName+"-") && strings.Contains(name, "_OsDisk_") {
		return true, "OS disk of this cluster's node"
	}
	if !strings.HasPrefix(name, "pvc-") {
		return false, "not a CSI-provisioned disk"
	}
	if !purge {
		return false, "unattached CSI disk; pass --purge-orphaned-volumes to remove it"
	}
	return true, "unattached CSI disk, purge requested"
}

// sweepClusterDisks removes the cluster's CSI disks in one resource group.
func (p *Provider) sweepClusterDisks(ctx context.Context, rg, clusterName string) []string {
	if p.diskClient == nil {
		return nil
	}
	var problems []string
	var kept int
	pager := p.diskClient.NewListByResourceGroupPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Printf("Warning: could not list disks in %s: %v", rg, err)
			return problems
		}
		for _, d := range page.Value {
			orphan, why := diskIsOrphan(d, clusterName, p.config.PurgeOrphanedVolumes)
			if !orphan {
				if strings.Contains(why, "--purge-orphaned-volumes") {
					kept++
				}
				continue
			}
			poller, err := p.diskClient.BeginDelete(ctx, rg, *d.Name, nil)
			if err != nil {
				problems = append(problems, fmt.Sprintf("failed to delete disk %s: %v", *d.Name, err))
				continue
			}
			if _, err := poller.PollUntilDone(ctx, nil); err != nil {
				problems = append(problems, fmt.Sprintf("failed waiting for disk %s: %v", *d.Name, err))
				continue
			}
			log.Printf("Deleted disk %s — %s", *d.Name, why)
		}
	}
	if kept > 0 {
		log.Printf("Left %d unattached pvc-* disk(s) in %s; they keep billing.", kept, rg)
		log.Printf("  Remove them with: adhar down ... --purge-orphaned-volumes")
	}
	return problems
}

// PurgeOrphanedVolumes removes unattached CSI managed disks WITHOUT needing a
// live cluster, so the advice a teardown prints stays usable after the cluster
// is gone.
//
// sweepSubscriptionOrphanDisks runs only inside DeleteCluster and takes its
// location from that cluster's ResourceTracker. Once the cluster was deleted
// there was no path to the purge at all: the lookup found nothing and the sweep
// was never called, so `--purge-orphaned-volumes` did nothing at exactly the
// moment an operator reads the warning and tries it. Found on GCP, where 74
// disks / 771 GB were left billing behind that hint (2026-09-26); Azure and
// DigitalOcean had the same gap and are fixed the same way.
//
// Location comes from the provider's own configuration. It reports a count and
// collected problems rather than one error, because a single disk refusing to go
// is not a reason to abandon the rest.
func (p *Provider) PurgeOrphanedVolumes(ctx context.Context) (deleted int, errs []string) {
	if p.diskClient == nil {
		return 0, []string{"azure disk client is not initialised"}
	}
	before := p.countOrphanedDisks(ctx, p.config.Location)
	if before == 0 {
		return 0, nil
	}
	// Reaching this method IS the opt-in, so purging is forced rather than left
	// to depend on how the provider happened to be constructed.
	prev := p.config.PurgeOrphanedVolumes
	p.config.PurgeOrphanedVolumes = true
	errs = p.sweepSubscriptionOrphanDisks(ctx, p.config.Location)
	p.config.PurgeOrphanedVolumes = prev

	after := p.countOrphanedDisks(ctx, p.config.Location)
	return before - after, errs
}

// countOrphanedDisks counts what PurgeOrphanedVolumes would act on, so the
// caller can report a real number instead of "done".
func (p *Provider) countOrphanedDisks(ctx context.Context, location string) int {
	if p.diskClient == nil {
		return 0
	}
	n := 0
	pager := p.diskClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return n
		}
		for _, d := range page.Value {
			if d == nil || d.Name == nil || d.ID == nil {
				continue
			}
			if location != "" && d.Location != nil && !strings.EqualFold(*d.Location, location) {
				continue
			}
			if orphan, _ := diskIsOrphan(d, "", true); orphan {
				n++
			}
		}
	}
	return n
}

// sweepSubscriptionOrphanDisks looks beyond the cluster's resource group.
//
// Only with the purge flag, and only in the cluster's own location: a CSI driver
// pointed at a different resource group leaves disks the group deletion never
// sees, but a subscription-wide delete is not something to do by default.
func (p *Provider) sweepSubscriptionOrphanDisks(ctx context.Context, location string) []string {
	if p.diskClient == nil || !p.config.PurgeOrphanedVolumes {
		return nil
	}
	var problems []string
	pager := p.diskClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Printf("Warning: could not list subscription disks: %v", err)
			return problems
		}
		for _, d := range page.Value {
			if d == nil || d.Name == nil || d.ID == nil {
				continue
			}
			if location != "" && d.Location != nil && !strings.EqualFold(*d.Location, location) {
				continue
			}
			orphan, why := diskIsOrphan(d, "", true)
			if !orphan {
				continue
			}
			rg := resourceGroupFromID(*d.ID)
			if rg == "" {
				continue
			}
			poller, err := p.diskClient.BeginDelete(ctx, rg, *d.Name, nil)
			if err != nil {
				problems = append(problems, fmt.Sprintf("failed to delete disk %s/%s: %v", rg, *d.Name, err))
				continue
			}
			if _, err := poller.PollUntilDone(ctx, nil); err != nil {
				problems = append(problems, fmt.Sprintf("failed waiting for disk %s/%s: %v", rg, *d.Name, err))
				continue
			}
			log.Printf("Deleted orphaned disk %s/%s — %s", rg, *d.Name, why)
		}
	}
	return problems
}

// ---------------------------------------------------------------------------
// Quota preflight
// ---------------------------------------------------------------------------

// quotaShortfall reports which regional limits a cluster of this shape would
// exceed. Azure reports usage per family AND a region-wide "cores" total, and a
// create can fail on either, so both are checked.
func quotaShortfall(usages []*armcompute.Usage, family string, nodes, coresPerNode int) []string {
	need := int64(nodes * coresPerNode)
	if need <= 0 {
		return nil
	}
	var out []string
	for _, u := range usages {
		if u == nil || u.Name == nil || u.Name.Value == nil || u.Limit == nil || u.CurrentValue == nil {
			continue
		}
		name := *u.Name.Value
		relevant := strings.EqualFold(name, "cores") ||
			strings.EqualFold(name, "totalRegionalvCPUs") ||
			(family != "" && strings.EqualFold(name, family))
		if !relevant {
			continue
		}
		if int64(*u.CurrentValue)+need > *u.Limit {
			out = append(out, fmt.Sprintf("%s: need %d more vCPU, %d of %d already in use", name, need, *u.CurrentValue, *u.Limit))
		}
	}
	return out
}

// vmSizeFamilyQuotaName maps a VM size to the usage name that meters it.
//
// Azure's quota names look like `standardDSv3Family`, and NO API maps a size to
// one — the SKUs API reports a family as free text that does not match the usage
// name. So this derives it from the size string, which is documented and stable:
//
//	Standard_D4s_v3 → standardDSv3Family   (series D, suffix s, version v3)
//	Standard_D8_v3  → standardDv3Family    (no suffix)
//	Standard_NC6s_v3 → standardNCSv3Family
//
// The casing is not cosmetic: the series and suffix letters are upper case and the
// version keeps its lower-case `v`, and a mismatch means the family check silently
// never fires — the worst kind of failure for a preflight, since it looks like it
// passed.
func vmSizeFamilyQuotaName(size string) string {
	body := strings.TrimPrefix(strings.TrimPrefix(size, "Standard_"), "standard_")
	if body == "" {
		return ""
	}
	parts := strings.Split(body, "_")

	var series, suffix strings.Builder
	sawDigit := false
	for _, r := range parts[0] {
		switch {
		case r >= '0' && r <= '9':
			sawDigit = true // the core count, which is not part of the family
		case sawDigit:
			suffix.WriteRune(toUpper(r))
		default:
			series.WriteRune(toUpper(r))
		}
	}
	if series.Len() == 0 {
		return ""
	}
	version := ""
	if len(parts) > 1 {
		version = strings.ToLower(parts[1])
	}
	return "standard" + series.String() + suffix.String() + version + "Family"
}

func toUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - ('a' - 'A')
	}
	return r
}

// checkQuota refuses a create the subscription cannot hold, before any resource
// exists. A quota the API will not report is logged and skipped rather than
// treated as a failure: a credential without Microsoft.Compute/locations/usages
// read should still be able to build a cluster.
func (p *Provider) checkQuota(ctx context.Context, nodes int, vmSize string) error {
	if p.usageClient == nil || p.vmSizesClient == nil || nodes <= 0 {
		return nil
	}
	location := p.config.Location
	cores := p.vmSizeCores(ctx, location, vmSize)
	if cores == 0 {
		log.Printf("Warning: VM size %q not found in %s; skipping the quota preflight", vmSize, location)
		return nil
	}

	var usages []*armcompute.Usage
	pager := p.usageClient.NewListPager(location, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Printf("Warning: could not read compute quotas in %s (%v); proceeding without a preflight check", location, err)
			return nil
		}
		usages = append(usages, page.Value...)
	}

	shortfalls := quotaShortfall(usages, vmSizeFamilyQuotaName(vmSize), nodes, cores)
	if len(shortfalls) == 0 {
		log.Printf("Quota preflight passed: %d × %s (%d vCPU) fits in %s", nodes, vmSize, nodes*cores, location)
		return nil
	}
	return fmt.Errorf("subscription %s cannot hold this cluster in %s (%d × %s = %d vCPU):\n  %s\n"+
		"Request an increase in the Azure portal (Subscriptions → Usage + quotas), or lower nodeCount / choose a smaller vmSize",
		p.config.SubscriptionID, location, nodes, vmSize, nodes*cores, strings.Join(shortfalls, "\n  "))
}

// vmSizeCores returns the vCPU count of a VM size in a location, or 0 when the
// size is unknown there.
func (p *Provider) vmSizeCores(ctx context.Context, location, vmSize string) int {
	pager := p.vmSizesClient.NewListPager(location, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return 0
		}
		for _, s := range page.Value {
			if s == nil || s.Name == nil || s.NumberOfCores == nil {
				continue
			}
			if strings.EqualFold(*s.Name, vmSize) {
				return int(*s.NumberOfCores)
			}
		}
	}
	return 0
}

// plannedNodeCount is how many VMs a spec will create: the control plane (at
// least one) plus every node group's replicas.
func plannedNodeCount(spec *types.ClusterSpec) int {
	masters := spec.ControlPlane.Replicas
	if masters <= 0 {
		masters = 1
	}
	// Exactly Replicas workers per group. A zero-replica group creates nothing —
	// both create paths skip it — so counting one for it would make the preflight
	// refuse clusters that fit.
	workers := 0
	for _, ng := range spec.NodeGroups {
		if ng.Replicas > 0 {
			workers += ng.Replicas
		}
	}
	return masters + workers
}

// plannedVMSize is the size the bulk of the cluster will use. A mixed-size spec is
// measured by its first node group rather than by an average no VM actually has.
func plannedVMSize(spec *types.ClusterSpec, fallback string) string {
	for _, ng := range spec.NodeGroups {
		if ng.InstanceType != "" {
			return ng.InstanceType
		}
	}
	if spec.ControlPlane.InstanceType != "" {
		return spec.ControlPlane.InstanceType
	}
	return fallback
}
