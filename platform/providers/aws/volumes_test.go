/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	provider "adhar-io/adhar/platform/providers"
)

// fakeEC2 is an EC2 that only knows volumes, instances and VPCs, and applies
// the filters the volume step sends the way EC2 does.
type fakeEC2 struct {
	volumes   []ec2types.Volume
	instances []ec2types.Instance
	vpcs      []ec2types.Vpc

	deleted []string
	// stuck makes a delete succeed while the volume stays: an unverified
	// deletion, which the step must report as failed.
	stuck map[string]bool
	// detachAfter makes a volume report attached for this many reads first.
	detachAfter map[string]int
	reads       map[string]int

	instancesErr error
}

func (f *fakeEC2) matches(tags []ec2types.Tag, state string, id string, filters []ec2types.Filter) bool {
	tm := tagMap(tags)
	for _, flt := range filters {
		name := aws.ToString(flt.Name)
		ok := false
		for _, want := range flt.Values {
			switch {
			case strings.HasPrefix(name, "tag:"):
				if v, has := tm[strings.TrimPrefix(name, "tag:")]; has && v == want {
					ok = true
				}
			case name == "status" || name == "instance-state-name":
				ok = ok || state == want
			case name == "volume-id":
				ok = ok || id == want
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (f *fakeEC2) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if f.reads == nil {
		f.reads = map[string]int{}
	}
	var out []ec2types.Volume
	for i := range f.volumes {
		id := aws.ToString(f.volumes[i].VolumeId)
		f.reads[id]++
		if n, ok := f.detachAfter[id]; ok && f.reads[id] > n {
			// The instance is gone: the volume really detaches.
			f.volumes[i].Attachments = nil
			f.volumes[i].State = ec2types.VolumeStateAvailable
		}
		v := f.volumes[i]
		if !f.matches(v.Tags, string(v.State), id, in.Filters) {
			continue
		}
		out = append(out, v)
	}
	return &ec2.DescribeVolumesOutput{Volumes: out}, nil
}

func (f *fakeEC2) DeleteVolume(_ context.Context, in *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	id := aws.ToString(in.VolumeId)
	for i, v := range f.volumes {
		if aws.ToString(v.VolumeId) != id {
			continue
		}
		if len(v.Attachments) > 0 {
			return nil, errors.New("VolumeInUse: " + id + " is attached")
		}
		f.deleted = append(f.deleted, id)
		if !f.stuck[id] {
			f.volumes = append(f.volumes[:i], f.volumes[i+1:]...)
		}
		return &ec2.DeleteVolumeOutput{}, nil
	}
	return nil, errors.New("InvalidVolume.NotFound: " + id)
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if f.instancesErr != nil {
		return nil, f.instancesErr
	}
	var insts []ec2types.Instance
	for _, i := range f.instances {
		state := ""
		if i.State != nil {
			state = string(i.State.Name)
		}
		if f.matches(i.Tags, state, "", in.Filters) {
			insts = append(insts, i)
		}
	}
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: insts}}}, nil
}

func (f *fakeEC2) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	var out []ec2types.Vpc
	for _, v := range f.vpcs {
		if f.matches(v.Tags, "", "", in.Filters) {
			out = append(out, v)
		}
	}
	return &ec2.DescribeVpcsOutput{Vpcs: out}, nil
}

// csiVolume is an EBS CSI volume the way the driver tags one.
func csiVolume(id string, attachedTo string, extra ...string) ec2types.Volume {
	kv := append([]string{
		ebsCSIClusterTag, "true",
		ebsCSIVolumeNameTag, "pvc-" + id,
		createdForPVCTag, "data-" + id,
		createdForPVCNamespaceTag, "adhar-system",
	}, extra...)
	v := ec2types.Volume{
		VolumeId:         aws.String(id),
		Size:             aws.Int32(4),
		State:            ec2types.VolumeStateAvailable,
		AvailabilityZone: aws.String("ap-southeast-1a"),
		CreateTime:       aws.Time(time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)),
		Tags:             tags(kv...),
	}
	if attachedTo != "" {
		v.State = ec2types.VolumeStateInUse
		v.Attachments = []ec2types.VolumeAttachment{{InstanceId: aws.String(attachedTo), State: ec2types.VolumeAttachmentStateAttached}}
	}
	return v
}

func running(id string, kv ...string) ec2types.Instance {
	return ec2types.Instance{InstanceId: aws.String(id), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}, Tags: tags(kv...)}
}

func providerWith(f *fakeEC2) *Provider {
	return &Provider{config: &Config{Region: "ap-southeast-1"}, volumeAPI: f}
}

// Only volumes carrying THIS cluster's install id are the cluster's. A volume
// of another install of the same NAME, an untagged leftover and a hand-made
// volume are all somebody else's as far as the id lookup is concerned.
func TestClusterVolumesAreExactlyThisInstallsVolumes(t *testing.T) {
	f := &fakeEC2{volumes: []ec2types.Volume{
		csiVolume("vol-mine-attached", "i-node1", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa"),
		csiVolume("vol-mine", "", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa"),
		csiVolume("vol-same-name-older-install", "", provider.ClusterIDTag, "bbbbbbbbbbbbbbbb"),
		csiVolume("vol-legacy", ""),
		{VolumeId: aws.String("vol-handmade"), State: ec2types.VolumeStateAvailable, Tags: tags("Name", "backup")},
	}}
	got, err := providerWith(f).ClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar", InstallID: "aaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, v := range got {
		ids = append(ids, v.ID)
	}
	if strings.Join(ids, ",") != "vol-mine,vol-mine-attached" && strings.Join(ids, ",") != "vol-mine-attached,vol-mine" {
		t.Fatalf("ClusterVolumes = %v, want exactly the two volumes tagged with this install id", ids)
	}
	for _, v := range got {
		if v.PVCNamespace != "adhar-system" || !strings.HasPrefix(v.PVCName, "data-") || v.SizeGiB != 4 || v.Created.IsZero() {
			t.Errorf("volume %s lost its PVC/size/creation time: %+v", v.ID, v)
		}
		if !strings.Contains(v.DeleteCommand, "aws ec2 delete-volume --region ap-southeast-1 --volume-id "+v.ID) {
			t.Errorf("volume %s has no usable delete command: %q", v.ID, v.DeleteCommand)
		}
	}
}

// A cluster with no install id has NO attributed volumes. Matching by name
// instead is precisely the mistake attribution exists to stop: the account
// that leaked these volumes had run many clusters called "adhar".
func TestAClusterWithoutAnInstallIDOwnsNoVolumesByName(t *testing.T) {
	f := &fakeEC2{volumes: []ec2types.Volume{csiVolume("vol-1", "", clusterTagPrefix+"adhar", "owned")}}
	got, err := providerWith(f).ClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil || len(got) != 0 {
		t.Fatalf("ClusterVolumes without an install id = %v, %v; want nothing", got, err)
	}
	// The early return is the guard, not the filter: with an EMPTY id the tag
	// re-check (`tag value == ""`) holds for every volume WITHOUT the tag, so a
	// filter that lets everything through would hand the volume step every
	// untagged volume in the region.
	p := &Provider{config: &Config{Region: "ap-southeast-1"}, volumeAPI: sloppyFilterEC2{f}}
	got, err = p.ClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil || len(got) != 0 {
		t.Fatalf("ClusterVolumes without an install id, behind a filter that does not filter = %v, %v; want nothing", got, err)
	}
}

// Legacy volumes are offered only when nothing in the region could own them.
func TestLegacyVolumesRequireThatNoClusterInstanceRemains(t *testing.T) {
	legacy := []ec2types.Volume{
		csiVolume("vol-legacy-1", ""),
		csiVolume("vol-legacy-2", ""),
	}
	blockers := map[string]*fakeEC2{
		"a platform node":            {instances: []ec2types.Instance{running("i-1", "Cluster", "adhar")}},
		"another cluster's node":     {instances: []ec2types.Instance{running("i-2", clusterTagPrefix+"prod", "owned")}},
		"an EKS node":                {instances: []ec2types.Instance{running("i-3", "eks:cluster-name", "team-eks")}},
		"an instance in our own VPC": {instances: []ec2types.Instance{{InstanceId: aws.String("i-4"), VpcId: aws.String("vpc-ours"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}}}, vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-ours"), Tags: tags(platformVPCTagKey, platformVPCTagValue)}}},
		"a node still shutting down": {instances: []ec2types.Instance{{InstanceId: aws.String("i-5"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameShuttingDown}, Tags: tags("Cluster", "adhar")}}},
	}
	for name, f := range blockers {
		t.Run(name, func(t *testing.T) {
			f.volumes = legacy
			got, err := providerWith(f).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Provable || len(got.Volumes) != 0 {
				t.Fatalf("with %s still present the legacy volumes were offered: %+v", name, got)
			}
			if !strings.Contains(got.Reason, "not provable") {
				t.Errorf("the reason does not say why: %q", got.Reason)
			}
		})
	}

	t.Run("an unrelated instance does not block", func(t *testing.T) {
		f := &fakeEC2{volumes: legacy, instances: []ec2types.Instance{running("i-web", "Name", "web-server")}}
		got, err := providerWith(f).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Provable || len(got.Volumes) != 2 {
			t.Fatalf("legacy = %+v, want both volumes offered", got)
		}
	})
}

// With no cluster instance left, the offer is still only CSI volumes that
// carry no install id and no other cluster's tag, and are not attached.
func TestLegacyVolumesAreOnlyUnclaimedCSIVolumes(t *testing.T) {
	f := &fakeEC2{volumes: []ec2types.Volume{
		csiVolume("vol-offer", ""),
		csiVolume("vol-offer-this-name", "", clusterTagPrefix+"adhar", "owned"),
		csiVolume("vol-has-id", "", provider.ClusterIDTag, "cccccccccccccccc"),
		csiVolume("vol-other-cluster", "", clusterTagPrefix+"prod", "owned"),
		csiVolume("vol-other-cluster-name", "", ebsCSIClusterNameTag, "prod"),
		csiVolume("vol-attached", "i-elsewhere"),
		{VolumeId: aws.String("vol-handmade"), State: ec2types.VolumeStateAvailable, Tags: tags("Name", "backup")},
		{VolumeId: aws.String("vol-csi-tag-only"), State: ec2types.VolumeStateAvailable, Tags: tags(ebsCSIClusterTag, "true")},
	}}
	got, err := providerWith(f).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range got.Volumes {
		ids = append(ids, v.ID)
	}
	if strings.Join(ids, ",") != "vol-offer,vol-offer-this-name" {
		t.Fatalf("legacy volumes = %v, want only vol-offer and vol-offer-this-name", ids)
	}
}

// "Could not list the instances" must never read as "there are none".
func TestLegacyDiscoveryFailsClosedWhenInstancesCannotBeListed(t *testing.T) {
	f := &fakeEC2{volumes: []ec2types.Volume{csiVolume("vol-1", "")}, instancesErr: errors.New("UnauthorizedOperation")}
	got, err := providerWith(f).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err == nil || got.Provable || len(got.Volumes) != 0 {
		t.Fatalf("an unreadable instance list produced an offer: %+v, err=%v", got, err)
	}
}

// End to end through the shared step: an attached volume is never deleted, a
// detached one is deleted AND read back gone, and a deletion the API accepted
// but did not carry out is reported as a failure — not a success.
func TestTheVolumeStepDeletesOnlyDetachedVolumesAndVerifiesEachOne(t *testing.T) {
	f := &fakeEC2{
		volumes: []ec2types.Volume{
			csiVolume("vol-stays-attached", "i-not-ours", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa"),
			csiVolume("vol-detaches", "i-node1", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa"),
			csiVolume("vol-free", "", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa"),
			csiVolume("vol-stuck", "", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa"),
		},
		detachAfter: map[string]int{"vol-detaches": 2},
		stuck:       map[string]bool{"vol-stuck": true},
	}
	p := providerWith(f)
	vols, err := p.ClusterVolumes(context.Background(), provider.VolumeOwner{InstallID: "aaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	res := provider.ReapVolumes(context.Background(), p, vols, provider.ReapOptions{
		DetachTimeout: 50 * time.Millisecond, VerifyTimeout: 30 * time.Millisecond, PollInterval: time.Millisecond,
	})
	got := func(vs []provider.BlockVolume) string {
		var ids []string
		for _, v := range vs {
			ids = append(ids, v.ID)
		}
		return strings.Join(ids, ",")
	}
	if g := got(res.StillAttached); g != "vol-stays-attached" {
		t.Errorf("still attached = %q, want vol-stays-attached", g)
	}
	if g := got(res.Deleted); !strings.Contains(g, "vol-free") || !strings.Contains(g, "vol-detaches") || len(res.Deleted) != 2 {
		t.Errorf("deleted = %q, want vol-free and vol-detaches", g)
	}
	if len(res.Failed) != 1 || res.Failed[0].Volume.ID != "vol-stuck" || !strings.Contains(res.Failed[0].Reason, "still exists") {
		t.Errorf("failed = %+v, want vol-stuck reported as still existing", res.Failed)
	}
	for _, id := range f.deleted {
		if id == "vol-stays-attached" {
			t.Fatal("an attached volume was deleted")
		}
	}
}

// The orphan purge and the name-based sweep inside DeleteCluster must never
// take a volume that carries an install id — not this cluster's (the volume
// step owns those, and may have been told to keep them) and not another's.
func TestNoSweepTakesAVolumeThatCarriesAnInstallID(t *testing.T) {
	v := csiVolume("vol-1", "", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa", clusterTagPrefix+"adhar", "owned")
	if action, why := volumeDisposition(v, "adhar", true); action != volumeKeep {
		t.Fatalf("volumeDisposition = %v (%s); an id-tagged volume was handed to a sweep", action, why)
	}
}

// The install id must reach the EBS driver (every volume, whatever its class)
// and the platform's own block class.
func TestTheInstallIDReachesTheEBSDriverAndTheBlockStorageClass(t *testing.T) {
	const id = "a1b2c3d4e5f6g7h8"
	p := &Provider{config: &Config{Region: "ap-southeast-1", AccessKeyID: "AKIA", SecretAccessKey: "secret"}}
	joined := ""
	for _, s := range p.cloudIntegrationSteps("adhar", id) {
		joined += s.Cmd + "\n"
	}
	for _, want := range []string{
		`--set 'controller.extraVolumeTags.adhar\.io/cluster-id=` + id + `'`,
		`tagSpecification_1: "adhar.io/cluster-id=` + id + `"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("AWS cloud integration lacks %s", want)
		}
	}
	// The class the tag must reach is the one packages name.
	if !strings.Contains(joined, "name: adhar-block") {
		t.Fatal("adhar-block is no longer created; the attribution check above checks nothing")
	}

	none := ""
	for _, s := range p.cloudIntegrationSteps("adhar", "") {
		none += s.Cmd + "\n"
	}
	if strings.Contains(none, "extraVolumeTags") || strings.Contains(none, "tagSpecification") {
		t.Error("a cluster with no install id was given an empty attribution tag")
	}
}

func TestTheEKSAddonTagsVolumesWithTheInstallID(t *testing.T) {
	raw := eksEBSAddonConfiguration("a1b2c3d4e5f6g7h8")
	var cfg struct {
		Controller struct {
			ExtraVolumeTags map[string]string `json:"extraVolumeTags"`
		} `json:"controller"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("addon configuration %q is not JSON: %v", raw, err)
	}
	if cfg.Controller.ExtraVolumeTags[provider.ClusterIDTag] != "a1b2c3d4e5f6g7h8" {
		t.Fatalf("addon configuration %s does not tag volumes with the install id", raw)
	}
	if eksEBSAddonConfiguration("") != "" {
		t.Error("an empty install id produced an addon configuration")
	}
}

// The account state the 2026-10-31 teardown left in ap-southeast-1, exactly as
// `aws ec2 describe-volumes` reported it on 2026-11-03 (read-only): six detached
// EBS CSI volumes carrying only the driver's own tags and their PVC, no
// instance at all, and only the default VPC. The discovery must offer exactly
// these six — and nothing once a cluster node is running in the region.
func TestLegacyDiscoveryFindsTheSixVolumesTheOctober31TeardownLeft(t *testing.T) {
	observed := func(id string, size int32, created, pv, pvc string) ec2types.Volume {
		ts, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			t.Fatal(err)
		}
		return ec2types.Volume{
			VolumeId: aws.String(id), Size: aws.Int32(size), State: ec2types.VolumeStateAvailable,
			AvailabilityZone: aws.String("ap-southeast-1a"), CreateTime: aws.Time(ts),
			Tags: tags(
				"ebs.csi.aws.com/cluster", "true",
				"CSIVolumeName", pv,
				"kubernetes.io/created-for/pv/name", pv,
				"kubernetes.io/created-for/pvc/name", pvc,
				"kubernetes.io/created-for/pvc/namespace", "adhar-system",
			),
		}
	}
	account := func() *fakeEC2 {
		return &fakeEC2{
			volumes: []ec2types.Volume{
				observed("vol-0d90b44df61ac3db6", 2, "2026-10-31T10:01:16.443Z", "pvc-1d5a84b5-8e93-4d4e-a761-e3a10d0eed78", "pvc-99170f0b64"),
				observed("vol-042e8cc6cb8bf7b87", 2, "2026-10-31T06:29:18.066Z", "pvc-253ef96e-6ee7-4883-8ca8-f1d03caee997", "pvc-9573882729"),
				observed("vol-0b2a32a66416eedcf", 2, "2026-10-31T11:29:19.765Z", "pvc-19bfa5b8-85a3-4b7a-96ce-179a3be05895", "pvc-431b46dcd8"),
				observed("vol-0b8f2ef91244472fa", 4, "2026-10-30T15:40:30.648Z", "pvc-a67331ad-7597-4061-9459-ea98994e06f8", "pvc-0853ec850e"),
				observed("vol-0c2f4e863c19f8550", 8, "2026-10-30T15:32:54.707Z", "pvc-77b924c7-787a-42ba-8197-8f0ed476dfd3", "nexus-data"),
				observed("vol-0e9c681968606b340", 4, "2026-10-30T15:40:30.667Z", "pvc-38481422-d57d-4c3a-be58-84c6dfef46f7", "pvc-19e2bcb785"),
			},
			vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-0c8d8fd61382402e0"), IsDefault: aws.Bool(true)}},
		}
	}

	got, err := providerWith(account()).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range got.Volumes {
		ids = append(ids, v.ID)
	}
	want := "vol-0c2f4e863c19f8550,vol-0b8f2ef91244472fa,vol-0e9c681968606b340,vol-042e8cc6cb8bf7b87,vol-0d90b44df61ac3db6,vol-0b2a32a66416eedcf"
	if !got.Provable || strings.Join(ids, ",") != want {
		t.Fatalf("legacy discovery on the observed account = provable %v %v, want exactly (oldest first) %s", got.Provable, ids, want)
	}
	if total := provider.TotalGiB(got.Volumes); total != 22 {
		t.Errorf("total = %d GiB, want 22 (5 CI workspaces of 2-4 GiB and nexus-data's 8)", total)
	}
	if got.Volumes[0].PVC() != "adhar-system/nexus-data" {
		t.Errorf("the 8 GiB volume reads as %q, want adhar-system/nexus-data", got.Volumes[0].PVC())
	}

	busy := account()
	busy.instances = []ec2types.Instance{running("i-new-node", "Cluster", "adhar")}
	got, err = providerWith(busy).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil || got.Provable || len(got.Volumes) != 0 {
		t.Fatalf("with a new adhar node running the same volumes were offered: %+v, %v", got, err)
	}
}

// sloppyFilterEC2 is an EC2 whose tag filter does not filter — a typo in the
// filter name, or an API that matches more loosely than documented.
type sloppyFilterEC2 struct{ *fakeEC2 }

func (s sloppyFilterEC2) DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, o ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	var kept []ec2types.Filter
	for _, f := range in.Filters {
		if !strings.HasPrefix(aws.ToString(f.Name), "tag:") {
			kept = append(kept, f)
		}
	}
	return s.fakeEC2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{Filters: kept}, o...)
}

// The id lookup does not trust the API's filter alone: a volume reaches the
// list only if it carries exactly this install id, whatever the filter let
// through. The delete that follows is permanent.
func TestTheIDLookupChecksTheTagItselfNotJustTheFilter(t *testing.T) {
	f := &fakeEC2{volumes: []ec2types.Volume{
		csiVolume("vol-mine", "", provider.ClusterIDTag, "aaaaaaaaaaaaaaaa"),
		csiVolume("vol-other-install", "", provider.ClusterIDTag, "bbbbbbbbbbbbbbbb"),
		csiVolume("vol-untagged", ""),
	}}
	p := &Provider{config: &Config{Region: "ap-southeast-1"}, volumeAPI: sloppyFilterEC2{f}}
	got, err := p.ClusterVolumes(context.Background(), provider.VolumeOwner{InstallID: "aaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "vol-mine" {
		t.Fatalf("ClusterVolumes = %+v; only the volume carrying this install id may be listed", got)
	}
}
