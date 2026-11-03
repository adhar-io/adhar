/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package aws

// The EBS half of `adhar down`'s volume step (see
// platform/providers/volume_attribution.go for the whole design).
//
// A cluster built since attribution existed tags every volume
// `adhar.io/cluster-id=<install id>` (cloudIntegrationSteps, and the EKS addon's
// configuration), so finding them is one tag filter. Volumes from BEFORE that
// carry only what every EBS CSI driver writes — `CSIVolumeName`,
// `ebs.csi.aws.com/cluster=true` and the PVC they were created for — and are
// offered only when nothing else in the region could own them: no instance the
// platform created, and no instance any Kubernetes cluster runs on. That is the
// state the six 2026-10-31 leftovers were found in.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	provider "adhar-io/adhar/platform/providers"
)

// ec2VolumeAPI is the slice of EC2 the volume step uses. *ec2.Client satisfies
// it; tests substitute a fake so the rules can be checked without an account.
type ec2VolumeAPI interface {
	DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DeleteVolume(ctx context.Context, in *ec2.DeleteVolumeInput, optFns ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error)
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
}

var _ provider.ClusterVolumeManager = (*Provider)(nil)

const (
	// Written by every EBS CSI driver on every volume it creates.
	ebsCSIClusterTag    = "ebs.csi.aws.com/cluster"
	ebsCSIVolumeNameTag = "CSIVolumeName"
	// Written when the driver runs with --k8s-tag-cluster-id.
	ebsCSIClusterNameTag = "ebs.csi.aws.com/cluster-name"
	// Written with --extra-create-metadata (the chart's default).
	createdForPVCNamespaceTag = "kubernetes.io/created-for/pvc/namespace"

	// platformVPCTag marks the VPCs this provider creates.
	platformVPCTagKey   = "Created-By"
	platformVPCTagValue = "adhar-platform"
)

func (p *Provider) volumeEC2() (ec2VolumeAPI, error) {
	if p.volumeAPI != nil {
		return p.volumeAPI, nil
	}
	if p.ec2Client == nil {
		return nil, fmt.Errorf("the EC2 client is not initialised")
	}
	return p.ec2Client, nil
}

func (p *Provider) region() string {
	if p.config == nil {
		return ""
	}
	return p.config.Region
}

// ClusterVolumes lists the EBS volumes carrying the owner's install id.
func (p *Provider) ClusterVolumes(ctx context.Context, owner provider.VolumeOwner) ([]provider.BlockVolume, error) {
	if owner.InstallID == "" {
		return nil, nil
	}
	api, err := p.volumeEC2()
	if err != nil {
		return nil, err
	}
	vols, err := describeAllVolumes(ctx, api, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{{Name: aws.String("tag:" + provider.ClusterIDTag), Values: []string{owner.InstallID}}},
	})
	if err != nil {
		return nil, fmt.Errorf("listing EBS volumes tagged %s=%s in %s: %w", provider.ClusterIDTag, owner.InstallID, p.region(), err)
	}
	out := make([]provider.BlockVolume, 0, len(vols))
	for _, v := range vols {
		// The filter is the API's; the check is ours. A volume reaches the list
		// only if the tag is really there with exactly this value.
		if tagMap(v.Tags)[provider.ClusterIDTag] != owner.InstallID {
			continue
		}
		out = append(out, ebsBlockVolume(v, p.region(), fmt.Sprintf("tagged %s=%s", provider.ClusterIDTag, owner.InstallID)))
	}
	provider.SortVolumes(out)
	return out, nil
}

// LegacyVolumes offers the unattached EBS CSI volumes that carry no install id,
// and only when no instance remains in the region that could own them.
func (p *Provider) LegacyVolumes(ctx context.Context, owner provider.VolumeOwner) (provider.LegacyVolumes, error) {
	api, err := p.volumeEC2()
	if err != nil {
		return provider.LegacyVolumes{}, err
	}
	region := p.region()

	platformVPCs := map[string]bool{}
	vpcs, err := api.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: aws.String("tag:" + platformVPCTagKey), Values: []string{platformVPCTagValue}}},
	})
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing the platform's VPCs in %s: %w", region, err)
	}
	for _, v := range vpcs.Vpcs {
		if v.VpcId != nil {
			platformVPCs[*v.VpcId] = true
		}
	}

	var blockers []string
	var token *string
	for {
		out, err := api.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			Filters: []ec2types.Filter{{Name: aws.String("instance-state-name"),
				Values: []string{"pending", "running", "shutting-down", "stopping", "stopped"}}},
			NextToken: token,
		})
		if err != nil {
			// Not being able to look is not the same as there being nothing.
			return provider.LegacyVolumes{}, fmt.Errorf("listing instances in %s: %w", region, err)
		}
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				if why := clusterInstanceMarker(inst, platformVPCs); why != "" {
					blockers = append(blockers, aws.ToString(inst.InstanceId)+" ("+why+")")
				}
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		token = out.NextToken
	}
	if len(blockers) > 0 {
		sort.Strings(blockers)
		return provider.LegacyVolumes{Reason: fmt.Sprintf(
			"not provable: %d instance(s) in %s could own an untagged EBS CSI volume — %s",
			len(blockers), region, strings.Join(blockers, ", "))}, nil
	}

	vols, err := describeAllVolumes(ctx, api, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + ebsCSIClusterTag), Values: []string{"true"}},
			{Name: aws.String("status"), Values: []string{"available"}},
		},
	})
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing EBS CSI volumes in %s: %w", region, err)
	}
	res := provider.LegacyVolumes{
		Provable: true,
		// Says what was CHECKED: an instance's tags. A cluster whose instances
		// carry none of them is invisible to this test, and the person typing
		// "yes" must not read it as "no Kubernetes cluster runs here".
		Reason: fmt.Sprintf("no instance left in %s was created by the platform, sits in its VPC, or is tagged "+
			"as a node of a Kubernetes cluster; each volume is an unattached EBS CSI volume with no cluster id", region),
	}
	for _, v := range vols {
		if ok, why := legacyEBSCandidate(v, owner.ClusterName); ok {
			res.Volumes = append(res.Volumes, ebsBlockVolume(v, region, why))
		}
	}
	provider.SortVolumes(res.Volumes)
	return res, nil
}

// GetVolume re-reads one volume. A filter (rather than VolumeIds) is used on
// purpose: an unknown id in VolumeIds is an ERROR, while a filter simply returns
// nothing — and "nothing" is exactly the answer a deletion is waiting for.
func (p *Provider) GetVolume(ctx context.Context, id string) (provider.BlockVolume, bool, error) {
	api, err := p.volumeEC2()
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	out, err := api.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{{Name: aws.String("volume-id"), Values: []string{id}}},
	})
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	for _, v := range out.Volumes {
		if aws.ToString(v.VolumeId) != id {
			continue
		}
		if v.State == ec2types.VolumeStateDeleted {
			return provider.BlockVolume{}, false, nil
		}
		return ebsBlockVolume(v, p.region(), ""), true, nil
	}
	return provider.BlockVolume{}, false, nil
}

// DeleteVolume deletes one detached EBS volume.
func (p *Provider) DeleteVolume(ctx context.Context, v provider.BlockVolume) error {
	api, err := p.volumeEC2()
	if err != nil {
		return err
	}
	_, err = api.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(v.ID)})
	return err
}

func describeAllVolumes(ctx context.Context, api ec2VolumeAPI, in *ec2.DescribeVolumesInput) ([]ec2types.Volume, error) {
	var out []ec2types.Volume
	pager := ec2.NewDescribeVolumesPaginator(api, in)
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Volumes...)
	}
	return out, nil
}

// ebsBlockVolume is one EBS volume in the step's own vocabulary.
func ebsBlockVolume(v ec2types.Volume, region, evidence string) provider.BlockVolume {
	tags := tagMap(v.Tags)
	bv := provider.BlockVolume{
		ID:           aws.ToString(v.VolumeId),
		Name:         tags[ebsCSIVolumeNameTag],
		SizeGiB:      int64(aws.ToInt32(v.Size)),
		PVCNamespace: tags[createdForPVCNamespaceTag],
		PVCName:      tags[createdForPVCTag],
		Location:     aws.ToString(v.AvailabilityZone),
		Evidence:     evidence,
		DeleteCommand: fmt.Sprintf("aws ec2 delete-volume --region %s --volume-id %s",
			region, aws.ToString(v.VolumeId)),
	}
	if v.CreateTime != nil {
		bv.Created = *v.CreateTime
	}
	for _, a := range v.Attachments {
		if a.State == ec2types.VolumeAttachmentStateDetached {
			continue
		}
		bv.AttachedTo = append(bv.AttachedTo, aws.ToString(a.InstanceId))
	}
	if len(bv.AttachedTo) == 0 && v.State == ec2types.VolumeStateInUse {
		// In use with no attachment listed: still not deletable.
		bv.AttachedTo = []string{"in-use"}
	}
	return bv
}

// clusterInstanceMarker says why an instance could own an untagged EBS CSI
// volume, or "" when it could not.
//
// The question is wider than "did the platform create it". Every EBS CSI
// driver — an EKS cluster's, a hand-built kubeadm cluster's — writes the same
// `ebs.csi.aws.com/cluster=true`, so the absence of the platform's own
// instances does not prove an untagged volume was a platform cluster's. An
// instance of ANY Kubernetes cluster in the region blocks the legacy offer.
func clusterInstanceMarker(inst ec2types.Instance, platformVPCs map[string]bool) string {
	tags := tagMap(inst.Tags)
	if c, ok := tags["Cluster"]; ok {
		return "created by the platform for cluster " + c
	}
	if tags["ManagedBy"] == "adhar" || tags[platformVPCTagKey] == platformVPCTagValue {
		return "created by the platform"
	}
	if c := tags["eks:cluster-name"]; c != "" {
		return "a node of EKS cluster " + c
	}
	if c := tags["aws:eks:cluster-name"]; c != "" {
		return "a node of EKS cluster " + c
	}
	for k := range tags {
		if strings.HasPrefix(k, clusterTagPrefix) {
			return "a node of Kubernetes cluster " + strings.TrimPrefix(k, clusterTagPrefix)
		}
	}
	if vpc := aws.ToString(inst.VpcId); vpc != "" && platformVPCs[vpc] {
		return "in VPC " + vpc + ", which the platform created"
	}
	return ""
}

// legacyEBSCandidate decides whether one EBS volume is a legacy leftover the
// teardown may offer, and why.
func legacyEBSCandidate(v ec2types.Volume, clusterName string) (bool, string) {
	if len(v.Attachments) > 0 || (v.State != "" && v.State != ec2types.VolumeStateAvailable) {
		return false, "attached or not available"
	}
	tags := tagMap(v.Tags)
	if tags[ebsCSIClusterTag] != "true" || tags[ebsCSIVolumeNameTag] == "" {
		// A volume someone made by hand, or with another tool, is never a
		// CSI leftover — however unattached it is.
		return false, "not created by an EBS CSI driver"
	}
	if id := tags[provider.ClusterIDTag]; id != "" {
		// Attributed by id: its own cluster's teardown deletes it, by that id.
		return false, "carries install id " + id
	}
	for k := range tags {
		if strings.HasPrefix(k, clusterTagPrefix) && strings.TrimPrefix(k, clusterTagPrefix) != clusterName {
			return false, "tagged for Kubernetes cluster " + strings.TrimPrefix(k, clusterTagPrefix)
		}
	}
	if n := tags[ebsCSIClusterNameTag]; n != "" && n != clusterName {
		return false, "tagged for Kubernetes cluster " + n
	}
	return true, "unattached EBS CSI volume with no cluster id; no instance remains that could own it"
}
