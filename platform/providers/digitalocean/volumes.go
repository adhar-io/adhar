package digitalocean

// The block-storage half of `adhar down`'s volume step (see
// platform/providers/volume_attribution.go).
//
// DigitalOcean's CSI driver takes exactly one `--do-tag`, and a cluster built
// since attribution existed passes it the install id
// (`adhar-cluster-id:<id>`, computeVolumeTag). A DOKS cluster's own CSI driver
// tags its volumes `k8s:<cluster uuid>`, which identifies that cluster for its
// whole life too. Volumes from before either are offered only with evidence:
// the cluster's name tag with no droplet of that cluster left, or — for the
// untagged pvc-* volumes the driver made before it tagged anything — no droplet
// of ANY Kubernetes cluster left in the region.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/digitalocean/godo"

	provider "adhar-io/adhar/platform/providers"
)

var (
	_ provider.ClusterVolumeManager   = (*Provider)(nil)
	_ provider.ClusterVolumeFinalizer = (*Provider)(nil)
)

// doksVolumeTag is the tag DOKS's managed CSI driver writes on a cluster's
// volumes.
func doksVolumeTag(clusterUUID string) string { return "k8s:" + clusterUUID }

func (p *Provider) volumeClient() (*godo.Client, error) {
	if p.client == nil || p.client.Storage == nil || p.client.Droplets == nil {
		return nil, fmt.Errorf("the DigitalOcean client is not initialised")
	}
	return p.client, nil
}

// regionVolumes lists every volume in the provider's region.
func (p *Provider) regionVolumes(ctx context.Context) ([]godo.Volume, error) {
	c, err := p.volumeClient()
	if err != nil {
		return nil, err
	}
	var out []godo.Volume
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		vols, resp, err := c.Storage.ListVolumes(ctx, &godo.ListVolumeParams{Region: p.config.Region, ListOptions: opts})
		if err != nil {
			return nil, fmt.Errorf("listing volumes in %s: %w", p.config.Region, err)
		}
		out = append(out, vols...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() || len(vols) == 0 {
			return out, nil
		}
		opts = &godo.ListOptions{Page: opts.Page + 1, PerPage: opts.PerPage}
	}
}

// ownerTags are the tags that attribute a volume to owner, exactly.
func ownerTags(owner provider.VolumeOwner) []string {
	var tags []string
	if owner.InstallID != "" {
		tags = append(tags, provider.DigitalOceanClusterIDTag(owner.InstallID))
	}
	if provider.IsCloudClusterUUID(owner.ProviderClusterID) {
		tags = append(tags, doksVolumeTag(owner.ProviderClusterID))
	}
	return tags
}

// ClusterVolumes lists the volumes carrying the owner's install-id tag, or its
// DOKS cluster tag.
func (p *Provider) ClusterVolumes(ctx context.Context, owner provider.VolumeOwner) ([]provider.BlockVolume, error) {
	want := ownerTags(owner)
	if len(want) == 0 {
		return nil, nil
	}
	vols, err := p.regionVolumes(ctx)
	if err != nil {
		return nil, err
	}
	var out []provider.BlockVolume
	for _, v := range vols {
		for _, t := range want {
			if hasTag(v.Tags, t) {
				out = append(out, p.doBlockVolume(v, "tagged "+t))
				break
			}
		}
	}
	provider.SortVolumes(out)
	return out, nil
}

// LegacyVolumes offers volumes from before attribution existed, with evidence.
func (p *Provider) LegacyVolumes(ctx context.Context, owner provider.VolumeOwner) (provider.LegacyVolumes, error) {
	c, err := p.volumeClient()
	if err != nil {
		return provider.LegacyVolumes{}, err
	}
	droplets, err := allDroplets(ctx, c)
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing droplets: %w", err)
	}
	nameTag := ""
	if owner.ClusterName != "" {
		nameTag = computeClusterTag(computeClusterName(owner.ClusterName))
	}
	clusterLeft, anyClusterLeft := legacyBlockers(droplets, nameTag, p.config.Region)
	if clusterLeft != "" {
		return provider.LegacyVolumes{Reason: "not provable: " + clusterLeft}, nil
	}
	vols, err := p.regionVolumes(ctx)
	if err != nil {
		return provider.LegacyVolumes{}, err
	}
	res := provider.LegacyVolumes{Provable: true}
	for _, v := range vols {
		if ok, why := legacyDOCandidate(v, nameTag, anyClusterLeft == ""); ok {
			res.Volumes = append(res.Volumes, p.doBlockVolume(v, why))
		}
	}
	switch {
	case anyClusterLeft != "":
		res.Reason = fmt.Sprintf("no droplet tagged %s remains, so volumes carrying that tag are its leftovers; "+
			"untagged pvc-* volumes are not offered because %s", nameTag, anyClusterLeft)
	default:
		res.Reason = fmt.Sprintf("no droplet of this or any other Kubernetes cluster remains in %s; each volume is an "+
			"unattached CSI volume carrying no install id", p.config.Region)
	}
	provider.SortVolumes(res.Volumes)
	return res, nil
}

// GetVolume re-reads one volume.
func (p *Provider) GetVolume(ctx context.Context, id string) (provider.BlockVolume, bool, error) {
	c, err := p.volumeClient()
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	v, resp, err := c.Storage.GetVolume(ctx, id)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return provider.BlockVolume{}, false, nil
	}
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	return p.doBlockVolume(*v, ""), true, nil
}

// DeleteVolume deletes one detached volume.
func (p *Provider) DeleteVolume(ctx context.Context, v provider.BlockVolume) error {
	c, err := p.volumeClient()
	if err != nil {
		return err
	}
	resp, err := c.Storage.DeleteVolume(ctx, v.ID)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

// AfterClusterVolumes removes the install-id tag once no volume carries it. A
// tag is free, but an empty one per teardown is clutter nobody asked for.
func (p *Provider) AfterClusterVolumes(ctx context.Context, owner provider.VolumeOwner, remaining []provider.BlockVolume) error {
	if owner.InstallID == "" || len(remaining) > 0 || p.client == nil || p.client.Tags == nil {
		return nil
	}
	resp, err := p.client.Tags.Delete(ctx, provider.DigitalOceanClusterIDTag(owner.InstallID))
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (p *Provider) doBlockVolume(v godo.Volume, evidence string) provider.BlockVolume {
	bv := provider.BlockVolume{
		ID:            v.ID,
		Name:          v.Name,
		SizeGiB:       v.SizeGigaBytes,
		Created:       v.CreatedAt,
		Evidence:      evidence,
		DeleteCommand: "doctl compute volume delete " + v.ID + " --force",
	}
	if v.Region != nil {
		bv.Location = v.Region.Slug
	}
	for _, d := range v.DropletIDs {
		bv.AttachedTo = append(bv.AttachedTo, fmt.Sprintf("droplet %d", d))
	}
	return bv
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func allDroplets(ctx context.Context, c *godo.Client) ([]godo.Droplet, error) {
	var out []godo.Droplet
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		ds, resp, err := c.Droplets.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() || len(ds) == 0 {
			return out, nil
		}
		opts = &godo.ListOptions{Page: opts.Page + 1, PerPage: opts.PerPage}
	}
}

// legacyBlockers inspects the droplets that are left. clusterLeft is non-empty
// when a droplet of THIS cluster (its name tag) still exists — then nothing is
// provable. anyClusterLeft is non-empty when a droplet of any Kubernetes
// cluster exists in the region — then only name-tagged volumes are.
func legacyBlockers(droplets []godo.Droplet, nameTag, region string) (clusterLeft, anyClusterLeft string) {
	for _, d := range droplets {
		if d.Region != nil && region != "" && d.Region.Slug != region {
			continue
		}
		for _, t := range d.Tags {
			switch {
			case nameTag != "" && t == nameTag:
				return fmt.Sprintf("droplet %s (%d) still carries %s", d.Name, d.ID, nameTag), ""
			case anyClusterLeft == "" && (strings.HasPrefix(t, computeClusterTagPrefix) || t == "k8s" || strings.HasPrefix(t, "k8s:")):
				anyClusterLeft = fmt.Sprintf("droplet %s (%d) of another Kubernetes cluster (%s) is still running", d.Name, d.ID, t)
			}
		}
	}
	return "", anyClusterLeft
}

// legacyDOCandidate decides whether one volume is a legacy leftover the
// teardown may offer, and why.
func legacyDOCandidate(v godo.Volume, nameTag string, noClusterLeft bool) (bool, string) {
	if len(v.DropletIDs) > 0 {
		return false, "attached"
	}
	for _, t := range v.Tags {
		if strings.HasPrefix(t, provider.ClusterIDLabel+":") {
			// Attributed by install id: its own cluster's teardown deletes it.
			return false, "carries install id " + strings.TrimPrefix(t, provider.ClusterIDLabel+":")
		}
	}
	if nameTag != "" && hasTag(v.Tags, nameTag) {
		return true, "tagged " + nameTag + ", and no droplet of that cluster remains"
	}
	for _, t := range v.Tags {
		// Another cluster's name tag, ANY install id (they share the prefix), or
		// a DOKS cluster's tag: attributed to somebody, so not offered here.
		if strings.HasPrefix(t, computeClusterTagPrefix) || strings.HasPrefix(t, "k8s:") {
			return false, "tagged " + t
		}
	}
	if !strings.HasPrefix(v.Name, "pvc-") {
		return false, "not a CSI volume"
	}
	if !noClusterLeft {
		return false, "untagged, and a Kubernetes cluster still runs in the region"
	}
	return true, "untagged pvc-* volume, and no droplet of any Kubernetes cluster remains in the region"
}
