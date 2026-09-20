/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package civo

import (
	"fmt"
	"log"
	"strings"
	"time"

	"adhar-io/adhar/platform/types"

	"github.com/civo/civogo"
)

// Teardown and preflight, brought up to the level the GCP and DigitalOcean
// providers reached under live use.
//
// THE PROBLEM THESE SOLVE. A cluster's cloud footprint is created by two parties:
// the provider code here, which records what it makes, and the controllers running
// INSIDE the cluster, which do not. The CSI driver provisions a volume per
// PersistentVolume and the cloud-controller-manager provisions a load balancer per
// Service of type LoadBalancer. Neither is in any tracker, so before these sweeps
// both survived `adhar down` — the volumes kept billing, and a load balancer
// holding a reference to the network stopped the network being deleted at all, so
// the next run inherited it.
//
// WHY A PREDICATE AND A SWEEP ARE SEPARATE. Every decision about what to delete is
// a pure function over the API's own structs (volumeDisposition,
// loadBalancerBelongsToCluster, quotaShortfall). Deleting cloud resources is the
// least reversible thing this codebase does and none of these three providers can
// be exercised in CI, so the part that decides is written to be tested without an
// account and the part that acts is a thin loop over it.

// volumeAction is what a sweep should do with one volume.
type volumeAction int

const (
	volumeKeep         volumeAction = iota
	volumeDelete                    // belongs to this cluster
	volumeDeleteOrphan              // unattached pvc-*, only with --purge-orphaned-volumes
)

// volumeDisposition decides the fate of one volume.
//
// Three rules, narrowest first:
//  1. attached to an instance of THIS cluster, or carrying this cluster's id —
//     it is ours, delete it.
//  2. attached to anything else — never touch it, whatever its name.
//  3. unattached and named pvc-* — a CSI leftover whose cluster is gone. Deleting
//     it needs the operator's opt-in, because an unattached volume looks exactly
//     the same whether its cluster was torn down an hour ago or is being rebuilt
//     right now.
func volumeDisposition(v civogo.Volume, clusterID string, clusterInstanceIDs map[string]bool, purgeOrphans bool) (volumeAction, string) {
	if v.Bootable {
		// A bootable volume is an instance's root disk; deleting the instance
		// takes it, and deleting it out from under a running instance would be
		// destructive in a way nothing here intends.
		return volumeKeep, "bootable root disk"
	}
	if v.InstanceID != "" && clusterInstanceIDs[v.InstanceID] {
		return volumeDelete, "attached to an instance of this cluster"
	}
	if clusterID != "" && v.ClusterID == clusterID {
		return volumeDelete, "tagged with this cluster's id"
	}
	if v.InstanceID != "" {
		return volumeKeep, "attached to an instance outside this cluster"
	}
	if v.ClusterID != "" && v.ClusterID != clusterID {
		return volumeKeep, "belongs to another Kubernetes cluster"
	}
	if purgeOrphans && strings.HasPrefix(v.Name, "pvc-") {
		return volumeDeleteOrphan, "unattached CSI volume, purge requested"
	}
	if strings.HasPrefix(v.Name, "pvc-") {
		return volumeKeep, "unattached CSI volume; pass --purge-orphaned-volumes to remove it"
	}
	return volumeKeep, "not created by this platform"
}

// sweepClusterVolumes deletes the block storage a cluster's CSI driver created.
// Returns human-readable problems rather than one error, so a single stubborn
// volume does not abandon the rest of the teardown.
func (p *Provider) sweepClusterVolumes(id clusterIdentity) []string {
	var problems []string
	volumes, err := p.client.ListVolumes()
	if err != nil {
		return []string{fmt.Sprintf("could not list volumes to clean up: %v", err)}
	}

	var kept int
	for _, v := range volumes {
		action, why := volumeDisposition(v, id.name, id.instanceIDs, p.config.PurgeOrphanedVolumes)
		if action == volumeKeep {
			if strings.HasPrefix(v.Name, "pvc-") {
				kept++
			}
			continue
		}

		// Detach first: Civo refuses to delete an attached volume, and the
		// instance may still be shutting down when we get here.
		if v.InstanceID != "" {
			if _, err := p.client.DetachVolume(v.ID); err != nil {
				log.Printf("Warning: could not detach volume %s (%s): %v", v.Name, v.ID, err)
			}
			time.Sleep(5 * time.Second)
		}
		if _, err := p.client.DeleteVolume(v.ID); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete volume %s (%s): %v", v.Name, v.ID, err))
			continue
		}
		log.Printf("Deleted volume %s (%d GB) — %s", v.Name, v.SizeGigabytes, why)
	}

	if kept > 0 && !p.config.PurgeOrphanedVolumes {
		log.Printf("Left %d unattached pvc-* volume(s) in place; they keep billing.", kept)
		log.Printf("  Remove them with: adhar down ... --purge-orphaned-volumes")
	}
	return problems
}

// clusterIdentity is everything that identifies one cluster's instances, gathered
// before they are deleted. It is what makes the load-balancer sweep exact.
type clusterIdentity struct {
	name          string
	tag           string // adhar-cluster-<name>, on every instance
	instanceIDs   map[string]bool
	instanceNames map[string]bool
	instanceIPs   map[string]bool // public and private
}

func (p *Provider) clusterIdentityFor(clusterName string, instances []civogo.Instance) clusterIdentity {
	id := clusterIdentity{
		name:          clusterName,
		tag:           computeClusterTag(clusterName),
		instanceIDs:   map[string]bool{},
		instanceNames: map[string]bool{},
		instanceIPs:   map[string]bool{},
	}
	for i := range instances {
		id.instanceIDs[instances[i].ID] = true
		id.instanceNames[instances[i].Hostname] = true
		if instances[i].PublicIP != "" {
			id.instanceIPs[instances[i].PublicIP] = true
		}
		if instances[i].PrivateIP != "" {
			id.instanceIPs[instances[i].PrivateIP] = true
		}
	}
	return id
}

// loadBalancerBelongsToCluster reports whether a load balancer was created for
// this cluster.
//
// Every test here is an EXACT match against something only this cluster has: its
// instance tag, one of its instance names, one of its instance IPs, its Civo
// cluster id, or its own name. Name-PREFIX matching was tried first and is wrong
// in a way that matters: a load balancer called `adhar-mgmt-gateway` is
// indistinguishable from cluster "adhar" fronting a service called
// "mgmt-gateway", so tearing down "adhar" would have deleted cluster
// "adhar-mgmt"'s load balancer and taken its traffic down. Leaving a load balancer
// behind costs money; deleting someone else's causes an outage, so the ambiguous
// rule is not the one to take.
func loadBalancerBelongsToCluster(lb civogo.LoadBalancer, id clusterIdentity) bool {
	if id.name == "" {
		return false
	}
	if lb.ClusterID != "" && lb.ClusterID == id.name {
		return true
	}
	if lb.Name == id.name {
		return true
	}
	for _, pool := range lb.InstancePool {
		for _, tag := range pool.Tags {
			if tag == id.tag {
				return true
			}
		}
		for _, name := range pool.Names {
			if id.instanceNames[name] {
				return true
			}
		}
	}
	for _, backend := range lb.Backends {
		if backend.IP != "" && id.instanceIPs[backend.IP] {
			return true
		}
	}
	return false
}

// sweepLoadBalancers removes the load balancers the in-cluster CCM created. They
// are invisible to the tracker, cost money on their own, and hold a reference to
// the network that blocks its deletion.
func (p *Provider) sweepLoadBalancers(id clusterIdentity) []string {
	var problems []string
	lbs, err := p.client.ListLoadBalancers()
	if err != nil {
		// Not fatal: an account with no load-balancer access should still be able
		// to tear a cluster down.
		log.Printf("Warning: could not list load balancers: %v", err)
		return nil
	}
	for _, lb := range lbs {
		if !loadBalancerBelongsToCluster(lb, id) {
			continue
		}
		if _, err := p.client.DeleteLoadBalancer(lb.ID); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete load balancer %s: %v", lb.Name, err))
			continue
		}
		log.Printf("Deleted load balancer %s (%s)", lb.Name, lb.PublicIP)
	}
	return problems
}

// ---------------------------------------------------------------------------
// Quota preflight
// ---------------------------------------------------------------------------

// quotaShortfall reports what a cluster of this shape would exceed, as one line
// per exhausted limit and an empty slice when it fits.
//
// This exists because the failure it prevents is the expensive kind: without it a
// create runs for ten minutes, provisions some of the nodes, then fails on the
// one that crosses the limit and leaves a half-built cluster to clean up. The
// numbers come from the account, not from a guess about the default plan.
func quotaShortfall(q *civogo.Quota, nodes, cpuPerNode, ramMBPerNode, diskGBPerNode int) []string {
	if q == nil || nodes <= 0 {
		return nil
	}
	var out []string
	check := func(what string, need, usage, limit int) {
		if limit <= 0 { // 0 means the account reports no limit for this resource
			return
		}
		if usage+need > limit {
			out = append(out, fmt.Sprintf("%s: need %d, %d of %d already in use", what, need, usage, limit))
		}
	}
	check("instances", nodes, q.InstanceCountUsage, q.InstanceCountLimit)
	check("CPU cores", nodes*cpuPerNode, q.CPUCoreUsage, q.CPUCoreLimit)
	check("RAM (MB)", nodes*ramMBPerNode, q.RAMMegabytesUsage, q.RAMMegabytesLimit)
	check("disk (GB)", nodes*diskGBPerNode, q.DiskGigabytesUsage, q.DiskGigabytesLimit)
	// One network per cluster: the create fails outright without it.
	check("networks", 1, q.NetworkCountUsage, q.NetworkCountLimit)
	// The Gateway Service's load balancer is deliberately NOT checked. An
	// exhausted load-balancer quota leaves that one Service Pending while the
	// whole platform still comes up and is reachable on its node ports, so
	// refusing to build the cluster over it would trade a recoverable
	// inconvenience for a hard failure.
	return out
}

// checkQuota fails a create before it starts when the account cannot hold the
// cluster. A quota the API will not report is not a reason to refuse: it logs and
// proceeds, so a token without quota access still works.
func (p *Provider) checkQuota(nodes int, size string) error {
	q, err := p.client.GetQuota()
	if err != nil {
		log.Printf("Warning: could not read the account quota (%v); proceeding without a preflight check", err)
		return nil
	}
	cpu, ram, disk := p.sizeShape(size)
	shortfalls := quotaShortfall(q, nodes, cpu, ram, disk)
	if len(shortfalls) == 0 {
		log.Printf("Quota preflight passed: %d × %s fits within the account limits", nodes, size)
		return nil
	}
	return fmt.Errorf("the Civo account cannot hold this cluster (%d × %s):\n  %s\n"+
		"Request an increase at https://dashboard.civo.com/quota, or lower nodeCount / choose a smaller size",
		nodes, size, strings.Join(shortfalls, "\n  "))
}

// sizeShape returns the CPU, RAM and disk one instance of a size consumes,
// resolved from the region's own size list. Unknown sizes contribute nothing to
// the check rather than a made-up number, so a new size never blocks a create.
func (p *Provider) sizeShape(size string) (cpu, ramMB, diskGB int) {
	sizes, err := p.client.ListInstanceSizes()
	if err != nil {
		log.Printf("Warning: could not list instance sizes: %v", err)
		return 0, 0, 0
	}
	for _, s := range sizes {
		if s.Name == size {
			return s.CPUCores, s.RAMMegabytes, s.DiskGigabytes
		}
	}
	log.Printf("Warning: instance size %q not found in region %s; skipping its part of the quota check", size, p.config.Region)
	return 0, 0, 0
}

// plannedNodeCount is the number of instances a spec will create: the control
// plane plus every node group, or the two-worker default the create path falls
// back to when a spec declares no groups.
func plannedNodeCount(spec *types.ClusterSpec, defaultNodeCount int) int {
	workers := 0
	for _, ng := range spec.NodeGroups {
		if ng.Replicas > 0 {
			workers += ng.Replicas
			continue
		}
		workers++
	}
	if workers == 0 {
		workers = 2
		if defaultNodeCount > 0 {
			workers = defaultNodeCount
		}
	}
	return workers + 1
}

// plannedSize is the instance size the nodes will use. The quota check is about
// the bulk of the cluster, so a mixed-size spec is measured by its first node
// group rather than by averaging sizes into a number no node actually has.
func plannedSize(spec *types.ClusterSpec, fallback string) string {
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
