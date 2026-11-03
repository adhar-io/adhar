/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package civo

// The Civo half of `adhar down`'s volume step (see
// platform/providers/volume_attribution.go).
//
// Civo volumes have no tags, so the install id cannot be written on them, and
// in compute mode the platform installs no block-storage CSI at all (see
// cloud_integration.go): a compute cluster creates no Civo volume of its own.
// On a MANAGED cluster Civo's own CSI driver stamps the cluster's UUID into
// every volume's ClusterID — attribution as exact as an install id, and the
// one this step uses. Volumes from older compute clusters, whose CSI was
// installed then removed, are offered only as legacy, with evidence.

import (
	"context"
	"fmt"
	"strings"

	"github.com/civo/civogo"

	provider "adhar-io/adhar/platform/providers"
)

// civoVolumeAPI is the slice of the Civo API the volume step uses.
// *civogo.Client satisfies it.
type civoVolumeAPI interface {
	ListVolumes() ([]civogo.Volume, error)
	DetachVolume(id string) (*civogo.SimpleResponse, error)
	DeleteVolume(id string) (*civogo.SimpleResponse, error)
	ListAllInstances() ([]civogo.Instance, error)
	ListKubernetesClusters() (*civogo.PaginatedKubernetesClusters, error)
}

var _ provider.ClusterVolumeManager = (*Provider)(nil)

func (p *Provider) volumeClient() (civoVolumeAPI, error) {
	if p.volumeAPI != nil {
		return p.volumeAPI, nil
	}
	if p.client == nil {
		return nil, fmt.Errorf("the Civo client is not initialised")
	}
	return p.client, nil
}

// ClusterVolumes lists the volumes Civo's CSI driver stamped with the managed
// cluster's UUID. An owner with no UUID — a compute cluster, whose "id" is its
// name tag — has none: a name is not an identity.
func (p *Provider) ClusterVolumes(_ context.Context, owner provider.VolumeOwner) ([]provider.BlockVolume, error) {
	if !provider.IsCloudClusterUUID(owner.ProviderClusterID) {
		return nil, nil
	}
	api, err := p.volumeClient()
	if err != nil {
		return nil, err
	}
	vols, err := api.ListVolumes()
	if err != nil {
		return nil, fmt.Errorf("listing Civo volumes in %s: %w", p.config.Region, err)
	}
	var out []provider.BlockVolume
	for _, v := range vols {
		if v.Bootable || v.ClusterID != owner.ProviderClusterID {
			continue
		}
		out = append(out, p.civoBlockVolume(v, "ClusterID "+owner.ProviderClusterID))
	}
	provider.SortVolumes(out)
	return out, nil
}

// LegacyVolumes offers unattached pvc-* volumes no live cluster can claim.
func (p *Provider) LegacyVolumes(_ context.Context, owner provider.VolumeOwner) (provider.LegacyVolumes, error) {
	api, err := p.volumeClient()
	if err != nil {
		return provider.LegacyVolumes{}, err
	}
	instances, err := api.ListAllInstances()
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing Civo instances in %s: %w", p.config.Region, err)
	}
	clusters, err := api.ListKubernetesClusters()
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing Civo Kubernetes clusters in %s: %w", p.config.Region, err)
	}
	if why := civoLegacyBlocker(instances, clusters); why != "" {
		return provider.LegacyVolumes{Reason: "not provable: " + why}, nil
	}
	vols, err := api.ListVolumes()
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing Civo volumes in %s: %w", p.config.Region, err)
	}
	res := provider.LegacyVolumes{
		Provable: true,
		Reason: fmt.Sprintf("no Civo Kubernetes cluster exists and no instance left in %s is tagged for a platform cluster; "+
			"each volume is an unattached pvc-* volume", p.config.Region),
	}
	for _, v := range vols {
		if ok, why := legacyCivoCandidate(v, owner.ClusterName); ok {
			res.Volumes = append(res.Volumes, p.civoBlockVolume(v, why))
		}
	}
	provider.SortVolumes(res.Volumes)
	return res, nil
}

// GetVolume re-reads one volume from the region's list: the API's not-found
// error is a decoded string, and absence from the list is the answer that
// cannot be misread.
func (p *Provider) GetVolume(_ context.Context, id string) (provider.BlockVolume, bool, error) {
	api, err := p.volumeClient()
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	vols, err := api.ListVolumes()
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	for _, v := range vols {
		if v.ID == id {
			return p.civoBlockVolume(v, ""), true, nil
		}
	}
	return provider.BlockVolume{}, false, nil
}

// DeleteVolume deletes one detached volume.
func (p *Provider) DeleteVolume(_ context.Context, v provider.BlockVolume) error {
	api, err := p.volumeClient()
	if err != nil {
		return err
	}
	_, err = api.DeleteVolume(v.ID)
	return err
}

func (p *Provider) civoBlockVolume(v civogo.Volume, evidence string) provider.BlockVolume {
	bv := provider.BlockVolume{
		ID:            v.ID,
		Name:          v.Name,
		SizeGiB:       int64(v.SizeGigabytes),
		Created:       v.CreatedAt,
		Location:      p.config.Region,
		Evidence:      evidence,
		DeleteCommand: fmt.Sprintf("civo volume delete %s --region %s --yes", v.ID, p.config.Region),
	}
	switch {
	case v.InstanceID != "":
		bv.AttachedTo = []string{v.InstanceID}
	case civoAttachmentInFlight(v.Status):
		// Mid-attach or mid-detach the instance id may already be cleared (or
		// not yet set); the volume is still not safe to delete.
		bv.AttachedTo = []string{"status " + v.Status}
	}
	return bv
}

// civoAttachmentInFlight reports whether a volume's status says it is attached,
// or on its way to or from an instance.
func civoAttachmentInFlight(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "attached", "attaching", "detaching":
		return true
	}
	return false
}

// civoLegacyBlocker names what could still own an unattributed volume: any
// instance of a platform cluster, or any Civo Kubernetes cluster.
func civoLegacyBlocker(instances []civogo.Instance, clusters *civogo.PaginatedKubernetesClusters) string {
	if clusters != nil && len(clusters.Items) > 0 {
		var names []string
		for _, c := range clusters.Items {
			names = append(names, c.Name)
		}
		return fmt.Sprintf("%d Kubernetes cluster(s) still exist (%s)", len(clusters.Items), strings.Join(names, ", "))
	}
	for _, i := range instances {
		for _, t := range i.Tags {
			if strings.HasPrefix(t, computeClusterTagPrefix) {
				return fmt.Sprintf("instance %s still carries %s", i.Hostname, t)
			}
		}
	}
	return ""
}

// legacyCivoCandidate decides whether one volume is a legacy leftover the
// teardown may offer, and why. A volume stamped with a cluster UUID belongs to
// that cluster's teardown, not to this one's evidence.
func legacyCivoCandidate(v civogo.Volume, clusterName string) (bool, string) {
	switch {
	case v.Bootable:
		return false, "bootable root disk"
	case v.InstanceID != "" || civoAttachmentInFlight(v.Status):
		return false, "attached"
	case !strings.HasPrefix(v.Name, "pvc-"):
		return false, "not a CSI volume"
	case provider.IsCloudClusterUUID(v.ClusterID):
		return false, "belongs to Kubernetes cluster " + v.ClusterID
	case v.ClusterID != "" && v.ClusterID != clusterName && v.ClusterID != computeClusterTag(clusterName):
		return false, "belongs to cluster " + v.ClusterID
	}
	return true, "unattached pvc-* volume, and no cluster that could own it remains"
}
