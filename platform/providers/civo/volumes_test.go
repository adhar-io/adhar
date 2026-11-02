/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package civo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/civo/civogo"

	provider "adhar-io/adhar/platform/providers"
)

type fakeCivo struct {
	volumes   []civogo.Volume
	instances []civogo.Instance
	clusters  []civogo.KubernetesCluster
	deleted   []string
}

func (f *fakeCivo) ListVolumes() ([]civogo.Volume, error) { return f.volumes, nil }

func (f *fakeCivo) DeleteVolume(id string) (*civogo.SimpleResponse, error) {
	for i, v := range f.volumes {
		if v.ID == id {
			if v.InstanceID != "" {
				return nil, errors.New("DatabaseVolumeStillAttachedCannotResize")
			}
			f.deleted = append(f.deleted, id)
			f.volumes = append(f.volumes[:i], f.volumes[i+1:]...)
			return &civogo.SimpleResponse{Result: "success"}, nil
		}
	}
	return nil, errors.New("DatabaseVolumeNotFoundError")
}

func (f *fakeCivo) DetachVolume(id string) (*civogo.SimpleResponse, error) {
	for i := range f.volumes {
		if f.volumes[i].ID == id {
			f.volumes[i].InstanceID = ""
		}
	}
	return &civogo.SimpleResponse{Result: "success"}, nil
}

func (f *fakeCivo) ListAllInstances() ([]civogo.Instance, error) { return f.instances, nil }

func (f *fakeCivo) ListKubernetesClusters() (*civogo.PaginatedKubernetesClusters, error) {
	return &civogo.PaginatedKubernetesClusters{Items: f.clusters}, nil
}

func civoWith(f *fakeCivo) *Provider {
	return &Provider{config: &Config{Region: "mum1", KeepClusterVolumes: true}, volumeAPI: f}
}

const managedUUID = "5c1d6b7e-8f9a-4b2c-9d3e-0f1a2b3c4d5e"

func TestCivoClusterVolumesAreTheManagedClustersOwn(t *testing.T) {
	f := &fakeCivo{volumes: []civogo.Volume{
		{ID: "v1", Name: "pvc-1", ClusterID: managedUUID, SizeGigabytes: 10, CreatedAt: time.Now()},
		{ID: "v2", Name: "pvc-2", ClusterID: "6c1d6b7e-8f9a-4b2c-9d3e-0f1a2b3c4d5f"},
		{ID: "v3", Name: "root", ClusterID: managedUUID, Bootable: true},
		{ID: "v4", Name: "pvc-4", ClusterID: "adhar"},
	}}
	got, err := civoWith(f).ClusterVolumes(context.Background(), provider.VolumeOwner{ProviderClusterID: managedUUID})
	if err != nil || len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("ClusterVolumes = %+v, %v; want only v1", got, err)
	}
	if got[0].DeleteCommand != "civo volume delete v1 --region mum1 --yes" {
		t.Errorf("delete command = %q", got[0].DeleteCommand)
	}
	// A compute cluster's "id" is its name (or name tag): never an identity.
	for _, id := range []string{"adhar", "adhar-cluster-adhar", ""} {
		if got, _ := civoWith(f).ClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar", ProviderClusterID: id}); len(got) != 0 {
			t.Errorf("owner id %q was given volumes: %+v", id, got)
		}
	}
}

func TestCivoLegacyVolumesNeedNoClusterLeft(t *testing.T) {
	vols := []civogo.Volume{
		{ID: "old-compute", Name: "pvc-a", ClusterID: "adhar"},
		{ID: "untagged", Name: "pvc-b"},
		{ID: "managed", Name: "pvc-c", ClusterID: managedUUID},
		{ID: "other", Name: "pvc-d", ClusterID: "prod"},
		{ID: "attached", Name: "pvc-e", InstanceID: "i-1"},
		{ID: "data", Name: "my-data"},
	}
	for name, f := range map[string]*fakeCivo{
		"a platform instance": {volumes: vols, instances: []civogo.Instance{{Hostname: "adhar-master", Tags: []string{"adhar-cluster-adhar"}}}},
		"a managed cluster":   {volumes: vols, clusters: []civogo.KubernetesCluster{{Name: "team"}}},
	} {
		got, err := civoWith(f).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
		if err != nil || got.Provable || len(got.Volumes) != 0 {
			t.Errorf("with %s left the legacy volumes were offered: %+v, %v", name, got, err)
		}
	}
	got, err := civoWith(&fakeCivo{volumes: vols}).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil || !got.Provable {
		t.Fatalf("legacy = %+v, %v", got, err)
	}
	var ids []string
	for _, v := range got.Volumes {
		ids = append(ids, v.ID)
	}
	if strings.Join(ids, ",") != "old-compute,untagged" {
		t.Fatalf("legacy volumes = %v, want old-compute,untagged", ids)
	}
}

func TestCivoVolumeStepDeletesDetachedVolumesAndVerifies(t *testing.T) {
	f := &fakeCivo{volumes: []civogo.Volume{
		{ID: "free", Name: "pvc-free", ClusterID: managedUUID},
		{ID: "held", Name: "pvc-held", ClusterID: managedUUID, InstanceID: "i-elsewhere"},
	}}
	p := civoWith(f)
	vols, _ := p.ClusterVolumes(context.Background(), provider.VolumeOwner{ProviderClusterID: managedUUID})
	res := provider.ReapVolumes(context.Background(), p, vols, provider.ReapOptions{
		DetachTimeout: 20 * time.Millisecond, VerifyTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond,
	})
	if len(res.Deleted) != 1 || res.Deleted[0].ID != "free" || len(res.StillAttached) != 1 || strings.Join(f.deleted, ",") != "free" {
		t.Fatalf("result = %+v, deleted %v", res, f.deleted)
	}
}

// Under `adhar down` (KeepClusterVolumes) DeleteCluster's sweep must leave the
// cluster's own volumes for the volume step — which asks first and honours
// --keep-volumes. Without it (`adhar cluster delete`) they go as they always did.
func TestTheTeardownSweepLeavesTheClustersVolumesToAdharDown(t *testing.T) {
	identity := clusterIdentity{name: "team", civoClusterID: managedUUID, instanceIDs: map[string]bool{}}
	vols := func() []civogo.Volume {
		return []civogo.Volume{{ID: "v1", Name: "pvc-1", ClusterID: managedUUID}}
	}

	kept := &fakeCivo{volumes: vols()}
	if problems := civoWith(kept).sweepClusterVolumes(identity); len(problems) != 0 || len(kept.deleted) != 0 {
		t.Fatalf("under adhar down the sweep deleted %v (%v); the volume step owns them", kept.deleted, problems)
	}

	swept := &fakeCivo{volumes: vols()}
	p := civoWith(swept)
	p.config.KeepClusterVolumes = false
	if problems := p.sweepClusterVolumes(identity); len(problems) != 0 || strings.Join(swept.deleted, ",") != "v1" {
		t.Fatalf("without KeepClusterVolumes the cluster's volume was not deleted: %v (%v)", swept.deleted, problems)
	}
}
