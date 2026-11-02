/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package aws

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	provider "adhar-io/adhar/platform/providers"
)

// readOnlyEC2 forwards the Describe calls and REFUSES the one mutating call the
// volume step has, so running the discovery against a real account cannot
// change it — by construction, not by care.
type readOnlyEC2 struct {
	inner   ec2VolumeAPI
	refused []string
}

func (r *readOnlyEC2) DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, o ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return r.inner.DescribeVolumes(ctx, in, o...)
}

func (r *readOnlyEC2) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, o ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return r.inner.DescribeInstances(ctx, in, o...)
}

func (r *readOnlyEC2) DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, o ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return r.inner.DescribeVpcs(ctx, in, o...)
}

func (r *readOnlyEC2) DeleteVolume(_ context.Context, in *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	r.refused = append(r.refused, "DeleteVolume "+*in.VolumeId)
	return nil, errors.New("read-only validation: DeleteVolume refused")
}

// TestLiveLegacyVolumeDiscovery runs the legacy discovery against a REAL
// account, read-only, and prints what `adhar down` would offer:
//
//	ADHAR_AWS_LIVE_READONLY_REGION=ap-southeast-1 \
//	  go test ./platform/providers/aws -run TestLiveLegacyVolumeDiscovery -v
//
// ADHAR_AWS_LIVE_EXPECT_LEGACY (comma-separated volume ids) turns the printout
// into an assertion. Skipped unless the region is set, so CI never calls AWS.
func TestLiveLegacyVolumeDiscovery(t *testing.T) {
	region := os.Getenv("ADHAR_AWS_LIVE_READONLY_REGION")
	if region == "" {
		t.Skip("set ADHAR_AWS_LIVE_READONLY_REGION to run the read-only discovery against a real account")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("loading AWS configuration: %v", err)
	}
	ro := &readOnlyEC2{inner: ec2.NewFromConfig(cfg)}
	p := &Provider{config: &Config{Region: region}, volumeAPI: ro}

	legacy, err := p.LegacyVolumes(ctx, provider.VolumeOwner{ClusterName: os.Getenv("ADHAR_AWS_LIVE_CLUSTER_NAME")})
	if err != nil {
		t.Fatalf("legacy discovery: %v", err)
	}
	t.Logf("provable=%v: %s", legacy.Provable, legacy.Reason)
	var ids []string
	for _, v := range legacy.Volumes {
		ids = append(ids, v.ID)
		t.Logf("  %s  %3d GiB  %-32s  created %s  %s", v.ID, v.SizeGiB, v.PVC(), v.Created.UTC().Format(time.RFC3339), v.Location)
	}
	t.Logf("%d volume(s), %d GiB in total", len(legacy.Volumes), provider.TotalGiB(legacy.Volumes))

	if want := os.Getenv("ADHAR_AWS_LIVE_EXPECT_LEGACY"); want != "" {
		expected := strings.Split(want, ",")
		sort.Strings(expected)
		sort.Strings(ids)
		if strings.Join(ids, ",") != strings.Join(expected, ",") {
			t.Errorf("legacy discovery found %v, expected exactly %v", ids, expected)
		}
	}
	if len(ro.refused) > 0 {
		t.Fatalf("the discovery attempted a mutating call: %v", ro.refused)
	}
}
