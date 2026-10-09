/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package civo

import (
	"context"
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
		action, why := volumeDisposition(v, id.clusterIDForMatching(), id.instanceIDs, p.config.PurgeOrphanedVolumes)
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
	// civoClusterID is the Civo Kubernetes cluster UUID, set only in MANAGED
	// mode (a compute cluster is not a Civo "cluster" and has none).
	//
	// It is what makes a managed teardown exact rather than best-effort. Civo's
	// CSI driver stamps `ClusterID` on every volume it provisions and the CCM
	// does the same on every load balancer, so with the UUID in hand both
	// sweeps POSITIVELY identify the cluster's own resources — no
	// --purge-orphaned-volumes guess required, because nothing is being
	// inferred from "unattached and named pvc-*".
	civoClusterID string
}

// clusterIDForMatching is what the volume and load-balancer predicates compare
// against `ClusterID`. Managed clusters have a real UUID; compute clusters fall
// back to the name, which is what the compute path has always passed.
func (id clusterIdentity) clusterIDForMatching() string {
	if id.civoClusterID != "" {
		return id.civoClusterID
	}
	return id.name
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
	if lb.ClusterID != "" && lb.ClusterID == id.clusterIDForMatching() {
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
func quotaShortfall(q *civogo.Quota, nodes, cpu, ramMB, diskGB int) []string {
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
	// Totals, already summed per node by the caller. They used to be
	// perNode × nodes, which silently assumed every instance was the same size and
	// over-counted whenever the control plane was smaller than the workers — the
	// usual shape. A 1 × g3.medium + 2 × g3.xlarge cluster is 14 vCPU; multiplying
	// the WORKER size by three made it 18 and refused to build inside a 16-core
	// quota it actually fitted.
	check("instances", nodes, q.InstanceCountUsage, q.InstanceCountLimit)
	check("CPU cores", cpu, q.CPUCoreUsage, q.CPUCoreLimit)
	check("RAM (MB)", ramMB, q.RAMMegabytesUsage, q.RAMMegabytesLimit)
	check("disk (GB)", diskGB, q.DiskGigabytesUsage, q.DiskGigabytesLimit)
	// One network per cluster: the create fails outright without it.
	check("networks", 1, q.NetworkCountUsage, q.NetworkCountLimit)
	// The Gateway Service's load balancer is deliberately NOT checked. An
	// exhausted load-balancer quota leaves that one Service Pending while the
	// whole platform still comes up and is reachable on its node ports, so
	// refusing to build the cluster over it would trade a recoverable
	// inconvenience for a hard failure.
	return out
}

// Minimum shape the platform's own control plane needs on Civo. Measured, not
// guessed: a g3.medium (2 vCPU, 4096 MB) control plane carried kubeadm, etcd and
// the API server through the bootstrap and then became UNREACHABLE — both the
// API and SSH stopped answering, while Civo still reported the instance ACTIVE —
// once Argo CD began reconciling the production profile's 76 applications across
// 85+ pods on a live mum1 bring-up (2026-10-06). The workers were idle at the
// time; it is the control plane that runs out, and it is RAM first: etcd plus the
// API server's watch cache on 4 GB with that many watchers thrashes.
//
// g3.large (4 vCPU, 8192 MB) is the smallest size with room for it. Azure's
// control plane, for comparison, runs 2 vCPU with 16 GB.
const (
	civoControlPlaneMinCPU   = 4
	civoControlPlaneMinRAMMB = 8192
)

// controlPlaneSizeAdvice returns a warning when the chosen control-plane size is
// below what the platform needs, and "" when it is fine or unknown.
//
// A WARNING rather than a refusal, for the same reason the load-balancer quota
// is not checked above: how much control plane is enough depends on how many
// packages the profile enables, and a trimmed profile fits on less. The operator
// gets the numbers before any instance is billed, which is the part that was
// missing.
func controlPlaneSizeAdvice(size string, cpu, ramMB int) string {
	// 0,0 means the size was not found in the region — sizeShape already warned,
	// and guessing from a name would be worse than saying nothing.
	if cpu == 0 && ramMB == 0 {
		return ""
	}
	if cpu >= civoControlPlaneMinCPU && ramMB >= civoControlPlaneMinRAMMB {
		return ""
	}
	return fmt.Sprintf("control plane %s has %d vCPU and %d MB RAM; the platform's control plane "+
		"wants at least %d vCPU and %d MB (g3.large). A smaller one bootstraps and then stops "+
		"answering once Argo CD reconciles the full profile. Set controlPlaneMachineType to g3.large "+
		"or larger, or enable fewer packages.",
		size, cpu, ramMB, civoControlPlaneMinCPU, civoControlPlaneMinRAMMB)
}

// checkQuota fails a create before it starts when the account cannot hold the
// cluster. A quota the API will not report is not a reason to refuse: it logs and
// proceeds, so a token without quota access still works.
func (p *Provider) checkQuota(plan []string) error {
	q, err := p.client.GetQuota()
	if err != nil {
		log.Printf("Warning: could not read the account quota (%v); proceeding without a preflight check", err)
		return nil
	}
	// Sum the REAL shapes. A cluster is a control plane plus workers and those are
	// routinely different sizes, so one size times a node count is the wrong model:
	// it refuses clusters that fit.
	var cpu, ram, disk int
	for _, size := range plan {
		c, r, d := p.sizeShape(size)
		cpu, ram, disk = cpu+c, ram+r, disk+d
	}
	// plannedInstanceSizes puts the control plane first, which is the one node
	// whose size decides whether the cluster stays reachable.
	if len(plan) > 0 {
		c, r, _ := p.sizeShape(plan[0])
		if advice := controlPlaneSizeAdvice(plan[0], c, r); advice != "" {
			log.Printf("Warning: %s", advice)
		}
	}

	shortfalls := quotaShortfall(q, len(plan), cpu, ram, disk)
	summary := strings.Join(plan, " + ")
	if len(shortfalls) == 0 {
		log.Printf("Quota preflight passed: %s (%d vCPU, %d MB RAM, %d GB disk) fits within the account limits",
			summary, cpu, ram, disk)
		return nil
	}
	return fmt.Errorf("the Civo account cannot hold this cluster (%s):\n  %s\n"+
		"Request an increase at https://dashboard.civo.com/quota, or lower nodeCount / choose a smaller size",
		summary, strings.Join(shortfalls, "\n  "))
}

// plannedInstanceSizes lists the size of EVERY instance the spec will create,
// control plane first, in the same order createComputeCluster builds them. It is
// what the quota check sums.
func plannedInstanceSizes(spec *types.ClusterSpec, fallbackSize string, defaultNodeCount int) []string {
	control := fallbackSize
	if spec != nil && spec.ControlPlane.InstanceType != "" {
		control = spec.ControlPlane.InstanceType
	}
	replicas := 1
	if spec != nil && spec.ControlPlane.Replicas > 1 {
		replicas = spec.ControlPlane.Replicas
	}
	plan := make([]string, 0, replicas+2)
	for i := 0; i < replicas; i++ {
		plan = append(plan, control)
	}

	workers := 0
	if spec != nil {
		for _, ng := range spec.NodeGroups {
			size := ng.InstanceType
			if size == "" {
				size = fallbackSize
			}
			count := ng.Replicas
			if count <= 0 {
				count = 1
			}
			for i := 0; i < count; i++ {
				plan = append(plan, size)
			}
			workers += count
		}
	}
	if workers == 0 {
		// Mirrors the create path's own default when a spec declares no groups.
		n := 2
		if defaultNodeCount > 0 {
			n = defaultNodeCount
		}
		for i := 0; i < n; i++ {
			plan = append(plan, fallbackSize)
		}
	}
	return plan
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

// PurgeOrphanedVolumes removes unattached pvc-* volumes WITHOUT needing a live
// cluster, so the advice `sweepClusterVolumes` prints stays usable after the
// cluster is gone.
//
// THE GAP THIS CLOSES. sweepClusterVolumes runs only inside DeleteCluster and
// needs a clusterIdentity — the cluster's name and the ids of its instances —
// gathered before those instances are deleted. Once the cluster is gone there is
// no identity to gather, so there was no path to the purge at all:
// `adhar down --purge-orphaned-volumes` built the provider, found it did not
// implement helpers.OrphanVolumeSweeper, printed "provider civo cannot sweep
// orphaned volumes without a cluster yet" and deleted nothing — at exactly the
// moment an operator reads the hint and tries it.
//
// This is the same gap that left 74 disks / 771 GB billing on GCP (2026-09-26).
// It was fixed then for GCP, Azure and DigitalOcean; Civo was missed, and on
// 2026-10-09 a deleted mum1 cluster left **36 volumes / 317 GB** behind —
// 36 of a 40-volume account quota, which is a hard stop on the next bring-up
// long before RAM or CPU runs out.
//
// WHY THE EMPTY IDENTITY IS CORRECT HERE. volumeDisposition is given a zero
// clusterIdentity, so no volume can match "belongs to this cluster"; the only
// action it can return for a pvc-* volume is volumeDeleteOrphan, and only when
// purging is on. Volumes attached to an instance, bootable root disks, and
// volumes tagged for another Kubernetes cluster are all still kept by the same
// pure function the teardown path uses — so a cluster being rebuilt right now
// does not lose its storage.
//
// Reaching this method IS the operator's opt-in (it is only called by
// --purge-orphaned-volumes), so purging is forced rather than left to depend on
// how the provider happened to be constructed.
func (p *Provider) PurgeOrphanedVolumes(ctx context.Context) (deleted int, errs []string) {
	if p.client == nil {
		return 0, []string{"civo client is not initialised"}
	}
	before := p.countOrphanedVolumes()
	if before == 0 {
		return 0, nil
	}

	prev := p.config.PurgeOrphanedVolumes
	p.config.PurgeOrphanedVolumes = true
	errs = p.sweepClusterVolumes(clusterIdentity{instanceIDs: map[string]bool{}})
	p.config.PurgeOrphanedVolumes = prev

	after := p.countOrphanedVolumes()
	return before - after, errs
}

// countOrphanedVolumes counts what PurgeOrphanedVolumes would act on, so the
// caller can report a real number instead of "done".
func (p *Provider) countOrphanedVolumes() int {
	if p.client == nil {
		return 0
	}
	volumes, err := p.client.ListVolumes()
	if err != nil {
		return 0
	}
	n := 0
	for _, v := range volumes {
		if action, _ := volumeDisposition(v, "", map[string]bool{}, true); action == volumeDeleteOrphan {
			n++
		}
	}
	return n
}

// managedClusterIdentity gathers everything needed to attribute a MANAGED
// cluster's cloud resources, from the cluster record itself.
//
// It must be called BEFORE DeleteKubernetesCluster, for the same reason the
// compute path gathers its identity before deleting instances: once the cluster
// is gone its pool instances are gone with it, every volume it provisioned is
// unattached and unclaimed, and no load balancer can be attributed at all.
func (p *Provider) managedClusterIdentity(cluster *civogo.KubernetesCluster) clusterIdentity {
	id := clusterIdentity{
		name:          cluster.Name,
		tag:           computeClusterTag(cluster.Name),
		civoClusterID: cluster.ID,
		instanceIDs:   map[string]bool{},
		instanceNames: map[string]bool{},
		instanceIPs:   map[string]bool{},
	}
	for _, pool := range cluster.Pools {
		for _, name := range pool.InstanceNames {
			if name != "" {
				id.instanceNames[name] = true
			}
		}
		for i := range pool.Instances {
			inst := pool.Instances[i]
			if inst.ID != "" {
				id.instanceIDs[inst.ID] = true
			}
			if inst.Hostname != "" {
				id.instanceNames[inst.Hostname] = true
			}
			if inst.PublicIP != "" {
				id.instanceIPs[inst.PublicIP] = true
			}
		}
	}
	if cluster.MasterIP != "" {
		id.instanceIPs[cluster.MasterIP] = true
	}
	return id
}

// sweepManagedClusterResources removes what the cluster's own in-cluster
// controllers created and no tracker knows about: CSI block volumes and CCM
// load balancers.
//
// WHY THIS EXISTS AS ITS OWN FUNCTION. Both sweeps were written for the compute
// teardown (deleteComputeCluster) and wired ONLY there. The managed
// DeleteCluster called DeleteKubernetesCluster, waited, cleared local state and
// returned — so on the mode Civo actually ships in, `adhar down` deleted the
// cluster and left every volume and load balancer behind. The header of this
// file describes exactly that failure ("both survived `adhar down` — the volumes
// kept billing, and a load balancer holding a reference to the network stopped
// the network being deleted at all"); the fix was written and then applied to
// one of the two modes.
//
// Measured consequence (mum1, 2026-10-09): after a managed cluster was deleted,
// 36 pvc-* volumes totalling 317 GB were still on the account — 36 of a
// 40-volume quota. The next bring-up then stalled with Gitea's PVCs Pending and
// the CSI driver answering `OutOfRange: Requested volume would exceed volume
// count limit quota of 40`, which reads like a storage bug and is a teardown
// bug.
//
// Load balancers are swept FIRST: one holds a reference to the network, and on
// the compute path that is what blocked the network delete. Problems are logged
// and collected rather than returned, because a single stubborn resource is not
// a reason to abandon the cluster delete that follows.
func (p *Provider) sweepManagedClusterResources(id clusterIdentity) {
	var problems []string
	problems = append(problems, p.sweepLoadBalancers(id)...)
	problems = append(problems, p.sweepClusterVolumes(id)...)
	for _, problem := range problems {
		log.Printf("Warning: %s", problem)
	}
}
