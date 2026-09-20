/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package aws

import (
	"context"
	"fmt"
	"log"
	"strings"

	"adhar-io/adhar/platform/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
)

// Teardown and preflight, brought up to the level the GCP and DigitalOcean
// providers reached under live use.
//
// WHAT THE OLD TEARDOWN MISSED, AND WHY IT MATTERED. The eight steps in
// DeleteCluster remove what the provider itself created, discovering resources by
// the `Cluster` tag it writes. The controllers running INSIDE the cluster write a
// different tag — `kubernetes.io/cluster/<name>` — and create three things this
// code never saw:
//
//   - a load balancer per Service of type LoadBalancer (the Gateway Service is
//     one), plus its target groups and its own `k8s-elb-*` security group;
//   - an EBS volume per PersistentVolume, from the EBS CSI driver;
//   - security-group rules on the node groups that reference the load balancer's
//     group.
//
// Each cost money on its own. Worse, an ELB and its security group both hold a
// reference to the VPC, so step 8 failed with DependencyViolation and the VPC,
// its subnets and its internet gateway all survived — which is why a second
// `adhar up` in the same account used to inherit a half-deleted network.
//
// AWS tags are the whole mechanism here, so the matching rules are pure functions
// (resourceBelongsToCluster, volumeDisposition) tested without an account: this is
// the least reversible code in the provider and it cannot be exercised in CI.

const (
	// clusterTagPrefix is what every in-cluster AWS controller writes:
	// `kubernetes.io/cluster/<cluster-name>` = owned | shared.
	clusterTagPrefix = "kubernetes.io/cluster/"

	// createdForPVCTag is written by the EBS CSI driver on each volume it
	// provisions, which is how a CSI leftover is told from a hand-made volume.
	createdForPVCTag = "kubernetes.io/created-for/pvc/name"
)

// resourceBelongsToCluster reports whether a set of AWS tags marks a resource as
// belonging to one cluster, and whether any OTHER cluster claims it.
//
// The second return value is what makes a shared resource safe: an ELB tagged for
// two clusters (`shared`) must not be deleted with the first of them.
func resourceBelongsToCluster(tags map[string]string, clusterName string) (mine, otherClusters bool) {
	for k, v := range tags {
		if !strings.HasPrefix(k, clusterTagPrefix) {
			// The provider's own tag, written at create time.
			if k == "Cluster" && v == clusterName {
				mine = true
			}
			continue
		}
		name := strings.TrimPrefix(k, clusterTagPrefix)
		if name == clusterName {
			mine = true
			continue
		}
		if name != "" {
			otherClusters = true
		}
	}
	return mine, otherClusters
}

// volumeAction is what a sweep should do with one EBS volume.
type volumeAction int

const (
	volumeKeep volumeAction = iota
	volumeDelete
	volumeDeleteOrphan
)

// volumeDisposition decides the fate of one EBS volume.
//
// Rules, narrowest first: a volume still attached to anything is untouched; a
// volume tagged for this cluster goes; a volume tagged for another cluster stays
// whatever else it says; and an unattached CSI volume with no cluster tag at all
// is an orphan whose cluster is gone — removing that needs --purge-orphaned-volumes,
// because it looks identical whether the cluster was deleted an hour ago or is
// being rebuilt right now.
func volumeDisposition(v ec2types.Volume, clusterName string, purgeOrphans bool) (volumeAction, string) {
	if len(v.Attachments) > 0 {
		return volumeKeep, "still attached to an instance"
	}
	tags := tagMap(v.Tags)
	mine, other := resourceBelongsToCluster(tags, clusterName)
	if other && !mine {
		return volumeKeep, "tagged for another Kubernetes cluster"
	}
	if mine {
		return volumeDelete, "tagged for this cluster"
	}
	if _, fromCSI := tags[createdForPVCTag]; !fromCSI {
		return volumeKeep, "not provisioned by a CSI driver"
	}
	if !purgeOrphans {
		return volumeKeep, "unattached CSI volume; pass --purge-orphaned-volumes to remove it"
	}
	return volumeDeleteOrphan, "unattached CSI volume, purge requested"
}

func tagMap(tags []ec2types.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		if t.Key == nil {
			continue
		}
		out[*t.Key] = aws.ToString(t.Value)
	}
	return out
}

// sweepOrphanedVolumes deletes the EBS volumes the cluster's CSI driver created.
func (p *Provider) sweepOrphanedVolumes(ctx context.Context, clusterName string) []string {
	var problems []string
	var kept int

	paginator := ec2.NewDescribeVolumesPaginator(p.ec2Client, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{{Name: aws.String("status"), Values: []string{"available"}}},
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return append(problems, fmt.Sprintf("could not list EBS volumes: %v", err))
		}
		for _, v := range page.Volumes {
			action, why := volumeDisposition(v, clusterName, p.config.PurgeOrphanedVolumes)
			if action == volumeKeep {
				if strings.Contains(why, "--purge-orphaned-volumes") {
					kept++
				}
				continue
			}
			if _, err := p.ec2Client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: v.VolumeId}); err != nil {
				problems = append(problems, fmt.Sprintf("failed to delete volume %s: %v", aws.ToString(v.VolumeId), err))
				continue
			}
			log.Printf("Deleted EBS volume %s (%d GiB) — %s", aws.ToString(v.VolumeId), aws.ToInt32(v.Size), why)
		}
	}
	if kept > 0 {
		fmt.Printf("   ⚠️  %d unattached CSI volume(s) left in place; they keep billing.\n", kept)
		fmt.Printf("       Remove them with: adhar down ... --purge-orphaned-volumes\n")
	}
	return problems
}

// sweepLoadBalancers deletes the load balancers the in-cluster
// cloud-controller-manager created, in both flavours AWS offers.
//
// Both APIs are needed and neither is optional: the in-tree AWS provider creates a
// CLASSIC ELB for a Service of type LoadBalancer unless it is annotated for NLB,
// and the out-of-tree controller creates an NLB through the v2 API. A teardown
// that knows only one of them leaves the other running and the VPC undeletable.
func (p *Provider) sweepLoadBalancers(ctx context.Context, clusterName, vpcID string) []string {
	var problems []string
	problems = append(problems, p.sweepELBv2(ctx, clusterName, vpcID)...)
	problems = append(problems, p.sweepClassicELB(ctx, clusterName, vpcID)...)
	return problems
}

// sweepELBv2 removes NLBs/ALBs and the target groups that point at them.
func (p *Provider) sweepELBv2(ctx context.Context, clusterName, vpcID string) []string {
	if p.elbv2Client == nil {
		return nil
	}
	var problems []string

	out, err := p.elbv2Client.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{})
	if err != nil {
		log.Printf("Warning: could not list v2 load balancers: %v", err)
		return nil
	}
	for _, lb := range out.LoadBalancers {
		if lb.LoadBalancerArn == nil {
			continue
		}
		if vpcID != "" && aws.ToString(lb.VpcId) != vpcID {
			continue
		}
		tags, err := p.elbv2Tags(ctx, *lb.LoadBalancerArn)
		if err != nil {
			log.Printf("Warning: could not read tags of %s: %v", aws.ToString(lb.LoadBalancerName), err)
			continue
		}
		mine, other := resourceBelongsToCluster(tags, clusterName)
		if !mine || other && !ownsExclusively(tags, clusterName) {
			continue
		}

		// Target groups must go before the load balancer they are attached to,
		// or AWS refuses to delete them and they linger as billable clutter.
		if tgs, err := p.elbv2Client.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
			LoadBalancerArn: lb.LoadBalancerArn,
		}); err == nil {
			for _, tg := range tgs.TargetGroups {
				if _, err := p.elbv2Client.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{
					TargetGroupArn: tg.TargetGroupArn,
				}); err != nil {
					problems = append(problems, fmt.Sprintf("failed to delete target group %s: %v", aws.ToString(tg.TargetGroupName), err))
				}
			}
		}
		if _, err := p.elbv2Client.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{
			LoadBalancerArn: lb.LoadBalancerArn,
		}); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete load balancer %s: %v", aws.ToString(lb.LoadBalancerName), err))
			continue
		}
		log.Printf("Deleted load balancer %s (%s)", aws.ToString(lb.LoadBalancerName), string(lb.Type))
	}
	return problems
}

func (p *Provider) elbv2Tags(ctx context.Context, arn string) (map[string]string, error) {
	out, err := p.elbv2Client.DescribeTags(ctx, &elbv2.DescribeTagsInput{ResourceArns: []string{arn}})
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, d := range out.TagDescriptions {
		for _, t := range d.Tags {
			if t.Key != nil {
				tags[*t.Key] = aws.ToString(t.Value)
			}
		}
	}
	return tags, nil
}

// sweepClassicELB removes classic ELBs, which the in-tree provider still creates.
func (p *Provider) sweepClassicELB(ctx context.Context, clusterName, vpcID string) []string {
	if p.elbClient == nil {
		return nil
	}
	var problems []string

	out, err := p.elbClient.DescribeLoadBalancers(ctx, &elb.DescribeLoadBalancersInput{})
	if err != nil {
		log.Printf("Warning: could not list classic load balancers: %v", err)
		return nil
	}
	for _, lb := range out.LoadBalancerDescriptions {
		if lb.LoadBalancerName == nil {
			continue
		}
		if vpcID != "" && aws.ToString(lb.VPCId) != vpcID {
			continue
		}
		tagsOut, err := p.elbClient.DescribeTags(ctx, &elb.DescribeTagsInput{
			LoadBalancerNames: []string{*lb.LoadBalancerName},
		})
		if err != nil {
			log.Printf("Warning: could not read tags of classic ELB %s: %v", *lb.LoadBalancerName, err)
			continue
		}
		tags := map[string]string{}
		for _, d := range tagsOut.TagDescriptions {
			for _, t := range d.Tags {
				if t.Key != nil {
					tags[*t.Key] = aws.ToString(t.Value)
				}
			}
		}
		if mine, _ := resourceBelongsToCluster(tags, clusterName); !mine {
			continue
		}
		if !ownsExclusively(tags, clusterName) {
			continue
		}
		if _, err := p.elbClient.DeleteLoadBalancer(ctx, &elb.DeleteLoadBalancerInput{
			LoadBalancerName: lb.LoadBalancerName,
		}); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete classic load balancer %s: %v", *lb.LoadBalancerName, err))
			continue
		}
		log.Printf("Deleted classic load balancer %s", *lb.LoadBalancerName)
	}
	return problems
}

// ownsExclusively reports whether this cluster is the only one claiming a
// resource. A load balancer tagged for two clusters is shared infrastructure and
// deleting it with the first of them would take the second's traffic down.
func ownsExclusively(tags map[string]string, clusterName string) bool {
	for k := range tags {
		if !strings.HasPrefix(k, clusterTagPrefix) {
			continue
		}
		if strings.TrimPrefix(k, clusterTagPrefix) != clusterName {
			return false
		}
	}
	return true
}

// sweepCCMSecurityGroups removes the security groups the load-balancer controller
// created (`k8s-elb-*`), which the tag-based discovery misses because they carry
// the in-cluster tag rather than the provider's own `Cluster` tag — and which hold
// a reference to the VPC, so the VPC deletion fails while they exist.
func (p *Provider) sweepCCMSecurityGroups(ctx context.Context, clusterName, vpcID string) []string {
	if vpcID == "" {
		return nil
	}
	var problems []string
	out, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	if err != nil {
		log.Printf("Warning: could not list security groups in %s: %v", vpcID, err)
		return nil
	}
	for _, sg := range out.SecurityGroups {
		if sg.GroupId == nil || aws.ToString(sg.GroupName) == "default" {
			continue
		}
		tags := tagMap(sg.Tags)
		mine, other := resourceBelongsToCluster(tags, clusterName)
		isELBGroup := strings.HasPrefix(aws.ToString(sg.GroupName), "k8s-elb-")
		if !(mine || isELBGroup) || other && !ownsExclusively(tags, clusterName) {
			continue
		}
		if _, err := p.ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: sg.GroupId}); err != nil {
			problems = append(problems, fmt.Sprintf("failed to delete security group %s (%s): %v",
				aws.ToString(sg.GroupId), aws.ToString(sg.GroupName), err))
			continue
		}
		log.Printf("Deleted security group %s (%s)", aws.ToString(sg.GroupId), aws.ToString(sg.GroupName))
	}
	return problems
}

// sweepInClusterResources runs every sweep for one cluster, in dependency order:
// load balancers first (they hold the security groups AND the subnets), then the
// groups they created, then the volumes.
func (p *Provider) sweepInClusterResources(ctx context.Context, clusterName string, tracker *ResourceTracker) []string {
	vpcID := ""
	if tracker != nil && len(tracker.VPCs) > 0 {
		vpcID = tracker.VPCs[0]
	}
	var problems []string
	problems = append(problems, p.sweepLoadBalancers(ctx, clusterName, vpcID)...)
	problems = append(problems, p.sweepCCMSecurityGroups(ctx, clusterName, vpcID)...)
	problems = append(problems, p.sweepOrphanedVolumes(ctx, clusterName)...)
	return problems
}

// ---------------------------------------------------------------------------
// Quota preflight
// ---------------------------------------------------------------------------

// standardVCPUQuotaCode is "Running On-Demand Standard (A, C, D, H, I, M, R, T, Z)
// instances", measured in vCPU. It is the limit a platform-sized cluster actually
// hits: the default is 5 vCPU on a new account, which is one node.
const (
	standardVCPUQuotaCode = "L-1216C47A"
	ec2ServiceQuotaCode   = "ec2"
	quotaHeadroomPercent  = 0 // no fudge factor: report the real arithmetic
)

// quotaShortfall reports whether a cluster fits inside a vCPU limit, given what is
// already running. Returns "" when it fits.
func quotaShortfall(limit float64, inUse, needed int) string {
	if limit <= 0 {
		return ""
	}
	if float64(inUse+needed) <= limit {
		return ""
	}
	return fmt.Sprintf("Running On-Demand Standard instances: need %d more vCPU, %d of %.0f already in use",
		needed, inUse, limit)
}

// checkQuota fails a create the account cannot hold, before any resource exists.
//
// This is the GCP lesson applied to AWS: a quota that is exceeded halfway through
// leaves a VPC, subnets, security groups and some of the instances behind, and the
// error that surfaces (VcpuLimitExceeded) names neither the limit nor how much
// room is left. A credential without servicequotas access is not a reason to
// refuse — it logs and proceeds.
func (p *Provider) checkQuota(ctx context.Context, nodes int, instanceType string) error {
	if p.quotaClient == nil || nodes <= 0 {
		return nil
	}
	perNode := p.instanceTypeVCPUs(ctx, instanceType)
	if perNode == 0 {
		log.Printf("Warning: could not read the vCPU count of %s; skipping the quota preflight", instanceType)
		return nil
	}
	q, err := p.quotaClient.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String(ec2ServiceQuotaCode),
		QuotaCode:   aws.String(standardVCPUQuotaCode),
	})
	if err != nil || q.Quota == nil || q.Quota.Value == nil {
		log.Printf("Warning: could not read the EC2 vCPU quota (%v); proceeding without a preflight check", err)
		return nil
	}
	inUse := p.runningVCPUs(ctx)
	if shortfall := quotaShortfall(*q.Quota.Value, inUse, nodes*perNode); shortfall != "" {
		return fmt.Errorf("the AWS account cannot hold this cluster in %s (%d × %s = %d vCPU):\n  %s\n"+
			"Request an increase (Service Quotas → EC2 → %s), or lower nodeCount / choose a smaller instance type",
			p.config.Region, nodes, instanceType, nodes*perNode, shortfall, standardVCPUQuotaCode)
	}
	log.Printf("Quota preflight passed: %d × %s (%d vCPU) fits the account limit in %s", nodes, instanceType, nodes*perNode, p.config.Region)
	return nil
}

// instanceTypeVCPUs asks EC2 for the vCPU count of an instance type rather than
// parsing the name, which does not encode it reliably.
func (p *Provider) instanceTypeVCPUs(ctx context.Context, instanceType string) int {
	if instanceType == "" {
		return 0
	}
	out, err := p.ec2Client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
	})
	if err != nil || len(out.InstanceTypes) == 0 {
		return 0
	}
	vcpu := out.InstanceTypes[0].VCpuInfo
	if vcpu == nil || vcpu.DefaultVCpus == nil {
		return 0
	}
	return int(*vcpu.DefaultVCpus)
}

// runningVCPUs totals the vCPUs of every running instance in the region, which is
// what the quota is measured against — including instances this platform did not
// create.
func (p *Provider) runningVCPUs(ctx context.Context) int {
	paginator := ec2.NewDescribeInstancesPaginator(p.ec2Client, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{{Name: aws.String("instance-state-name"), Values: []string{"running", "pending"}}},
	})
	total := 0
	types := map[string]int{}
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return total
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				if i.CpuOptions != nil && i.CpuOptions.CoreCount != nil {
					threads := int32(1)
					if i.CpuOptions.ThreadsPerCore != nil {
						threads = *i.CpuOptions.ThreadsPerCore
					}
					total += int(*i.CpuOptions.CoreCount * threads)
					continue
				}
				// Fall back to the type's default, resolved once per type.
				t := string(i.InstanceType)
				if _, ok := types[t]; !ok {
					types[t] = p.instanceTypeVCPUs(ctx, t)
				}
				total += types[t]
			}
		}
	}
	return total
}

// plannedNodeCount is how many EC2 instances a spec will create: the control plane
// (at least one) plus every node group's replicas.
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

// plannedInstanceType is the type the bulk of the cluster will use. A mixed-type
// spec is measured by its first node group rather than by an average no instance
// actually has.
func plannedInstanceType(spec *types.ClusterSpec, _ *Config) string {
	for _, ng := range spec.NodeGroups {
		if ng.InstanceType != "" {
			return ng.InstanceType
		}
	}
	if spec.ControlPlane.InstanceType != "" {
		return spec.ControlPlane.InstanceType
	}
	// The same default the compute path applies when a spec names no type
	// (provider_compute.go).
	return "t3.medium"
}
