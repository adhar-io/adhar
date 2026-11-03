package gcp

// The Persistent Disk half of `adhar down`'s volume step (see
// platform/providers/volume_attribution.go).
//
// A cluster built since attribution existed labels every disk its block
// classes create `adhar-cluster-id=<install id>` (blockStorageParameters), so
// finding them is one label match across the project's zones. Disks from before
// that are recognised by the PD CSI driver's own description — a JSON object
// naming the PV, the PVC and `storage.gke.io/created-by` — and offered only when
// no instance that could own them is left in the cluster's region.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	provider "adhar-io/adhar/platform/providers"
)

// gcpVolumeAPI is the slice of Compute Engine the volume step uses.
type gcpVolumeAPI interface {
	// listDisks lists every disk in the project, in every zone.
	listDisks(ctx context.Context, project string) ([]*computepb.Disk, error)
	getDisk(ctx context.Context, project, zone, name string) (*computepb.Disk, error)
	// deleteDisk starts the deletion; the volume step verifies it.
	deleteDisk(ctx context.Context, project, zone, name string) error
	// listInstances lists every instance in the project, in every zone.
	listInstances(ctx context.Context, project string) ([]*computepb.Instance, error)
}

var _ provider.ClusterVolumeManager = (*Provider)(nil)

// pdCSIDriverName is what the PD CSI driver records as the disk's creator.
const pdCSIDriverName = "pd.csi.storage.gke.io"

type computeVolumeAPI struct {
	disks     *compute.DisksClient
	instances *compute.InstancesClient
}

func (c computeVolumeAPI) listDisks(ctx context.Context, project string) ([]*computepb.Disk, error) {
	it := c.disks.AggregatedList(ctx, &computepb.AggregatedListDisksRequest{Project: project})
	var out []*computepb.Disk
	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if pair.Value != nil {
			out = append(out, pair.Value.GetDisks()...)
		}
	}
}

func (c computeVolumeAPI) getDisk(ctx context.Context, project, zone, name string) (*computepb.Disk, error) {
	return c.disks.Get(ctx, &computepb.GetDiskRequest{Project: project, Zone: zone, Disk: name})
}

func (c computeVolumeAPI) deleteDisk(ctx context.Context, project, zone, name string) error {
	_, err := c.disks.Delete(ctx, &computepb.DeleteDiskRequest{Project: project, Zone: zone, Disk: name})
	return err
}

func (c computeVolumeAPI) listInstances(ctx context.Context, project string) ([]*computepb.Instance, error) {
	it := c.instances.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{Project: project})
	var out []*computepb.Instance
	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if pair.Value != nil {
			out = append(out, pair.Value.GetInstances()...)
		}
	}
}

func (p *Provider) volumeClient() (gcpVolumeAPI, error) {
	if p.volumeAPI != nil {
		return p.volumeAPI, nil
	}
	if p.diskClient == nil || p.instanceClient == nil {
		return nil, fmt.Errorf("the Compute Engine disk and instance clients are not initialised")
	}
	return computeVolumeAPI{disks: p.diskClient, instances: p.instanceClient}, nil
}

// ClusterVolumes lists the disks labelled with the owner's install id, in any
// zone of the project.
func (p *Provider) ClusterVolumes(ctx context.Context, owner provider.VolumeOwner) ([]provider.BlockVolume, error) {
	if owner.InstallID == "" {
		return nil, nil
	}
	api, err := p.volumeClient()
	if err != nil {
		return nil, err
	}
	disks, err := api.listDisks(ctx, p.config.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("listing disks in project %s: %w", p.config.ProjectID, err)
	}
	var out []provider.BlockVolume
	for _, d := range disks {
		if d.GetLabels()[provider.ClusterIDLabel] != owner.InstallID {
			continue
		}
		if !isPDCSIDisk(d) {
			// On GKE the id is a cluster resource label, which Google also puts
			// on the nodes' boot disks: those go with their nodes and are not
			// the cluster's VOLUMES.
			continue
		}
		out = append(out, p.pdBlockVolume(d, fmt.Sprintf("labelled %s=%s", provider.ClusterIDLabel, owner.InstallID)))
	}
	provider.SortVolumes(out)
	return out, nil
}

// LegacyVolumes offers the unattached PD CSI disks with no install id in the
// cluster's region, and only when no instance there could own them.
func (p *Provider) LegacyVolumes(ctx context.Context, owner provider.VolumeOwner) (provider.LegacyVolumes, error) {
	api, err := p.volumeClient()
	if err != nil {
		return provider.LegacyVolumes{}, err
	}
	region := p.config.Region
	if region == "" {
		return provider.LegacyVolumes{Reason: "not provable: no region is configured to search"}, nil
	}
	instances, err := api.listInstances(ctx, p.config.ProjectID)
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing instances in project %s: %w", p.config.ProjectID, err)
	}
	var blockers []string
	for _, inst := range instances {
		if !inRegion(lastSegment(inst.GetZone()), region) {
			continue
		}
		if why := clusterNodeMarker(inst); why != "" {
			blockers = append(blockers, inst.GetName()+" ("+why+")")
		}
	}
	if len(blockers) > 0 {
		sort.Strings(blockers)
		return provider.LegacyVolumes{Reason: fmt.Sprintf(
			"not provable: %d instance(s) in %s could own an unlabelled CSI disk — %s",
			len(blockers), region, strings.Join(blockers, ", "))}, nil
	}
	disks, err := api.listDisks(ctx, p.config.ProjectID)
	if err != nil {
		return provider.LegacyVolumes{}, fmt.Errorf("listing disks in project %s: %w", p.config.ProjectID, err)
	}
	res := provider.LegacyVolumes{
		Provable: true,
		Reason: fmt.Sprintf("no instance left in %s is labelled or tagged as a node of the platform or of a GKE cluster; "+
			"each disk is an unattached PD CSI disk with no cluster id", region),
	}
	for _, d := range disks {
		if !inRegion(lastSegment(d.GetZone()), region) {
			continue
		}
		if ok, why := legacyPDCandidate(d); ok {
			res.Volumes = append(res.Volumes, p.pdBlockVolume(d, why))
		}
	}
	provider.SortVolumes(res.Volumes)
	return res, nil
}

// GetVolume re-reads one disk; its id is "<zone>/<name>".
func (p *Provider) GetVolume(ctx context.Context, id string) (provider.BlockVolume, bool, error) {
	api, err := p.volumeClient()
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	zone, name, ok := strings.Cut(id, "/")
	if !ok {
		return provider.BlockVolume{}, false, fmt.Errorf("disk id %q is not <zone>/<name>", id)
	}
	d, err := api.getDisk(ctx, p.config.ProjectID, zone, name)
	if isNotFound(err) {
		return provider.BlockVolume{}, false, nil
	}
	if err != nil {
		return provider.BlockVolume{}, false, err
	}
	return p.pdBlockVolume(d, ""), true, nil
}

// DeleteVolume starts the deletion of one detached disk.
func (p *Provider) DeleteVolume(ctx context.Context, v provider.BlockVolume) error {
	api, err := p.volumeClient()
	if err != nil {
		return err
	}
	zone, name, ok := strings.Cut(v.ID, "/")
	if !ok {
		return fmt.Errorf("disk id %q is not <zone>/<name>", v.ID)
	}
	if err := api.deleteDisk(ctx, p.config.ProjectID, zone, name); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// pdCSIDescription is the JSON the PD CSI driver writes into a disk's
// description.
type pdCSIDescription struct {
	PVName       string `json:"kubernetes.io/created-for/pv/name"`
	PVCName      string `json:"kubernetes.io/created-for/pvc/name"`
	PVCNamespace string `json:"kubernetes.io/created-for/pvc/namespace"`
	CreatedBy    string `json:"storage.gke.io/created-by"`
}

func parsePDDescription(desc string) pdCSIDescription {
	var d pdCSIDescription
	_ = json.Unmarshal([]byte(desc), &d)
	return d
}

func (p *Provider) pdBlockVolume(d *computepb.Disk, evidence string) provider.BlockVolume {
	zone := lastSegment(d.GetZone())
	meta := parsePDDescription(d.GetDescription())
	bv := provider.BlockVolume{
		ID:           zone + "/" + d.GetName(),
		Name:         d.GetName(),
		SizeGiB:      d.GetSizeGb(),
		PVCNamespace: meta.PVCNamespace,
		PVCName:      meta.PVCName,
		Location:     zone,
		Evidence:     evidence,
		DeleteCommand: fmt.Sprintf("gcloud compute disks delete %s --zone %s --project %s --quiet",
			d.GetName(), zone, p.config.ProjectID),
	}
	if ts, err := time.Parse(time.RFC3339, d.GetCreationTimestamp()); err == nil {
		bv.Created = ts
	}
	for _, u := range d.GetUsers() {
		bv.AttachedTo = append(bv.AttachedTo, lastSegment(u))
	}
	return bv
}

// clusterNodeMarker says why an instance could own an unlabelled CSI disk, or
// "" when it could not. Like the AWS rule, the question is wider than "did the
// platform create it": a GKE node runs the same PD CSI driver.
func clusterNodeMarker(inst *computepb.Instance) string {
	for _, tag := range inst.GetTags().GetItems() {
		if name, ok := clusterNameFromTag(tag); ok {
			return "a node of platform cluster " + name
		}
	}
	labels := inst.GetLabels()
	if c := labels["goog-k8s-cluster-name"]; c != "" {
		return "a node of GKE cluster " + c
	}
	if strings.HasPrefix(inst.GetName(), "gke-") {
		return "a GKE node"
	}
	if labels["managed-by"] == "adhar" {
		return "created by the platform"
	}
	return ""
}

// legacyPDCandidate decides whether one disk is a legacy CSI leftover the
// teardown may offer, and why.
func legacyPDCandidate(d *computepb.Disk) (bool, string) {
	if len(d.GetUsers()) > 0 {
		return false, "attached"
	}
	if id := d.GetLabels()[provider.ClusterIDLabel]; id != "" {
		return false, "carries install id " + id
	}
	if !isPDCSIDisk(d) {
		// A disk someone created by hand, or a node's boot disk, is never a
		// CSI leftover.
		return false, "not created by the PD CSI driver"
	}
	return true, "unattached PD CSI disk with no cluster id; no instance remains that could own it"
}

// isPDCSIDisk reports whether the PD CSI driver made the disk for a
// PersistentVolume: its description names the driver or the PV, or it has the
// driver's pvc-* name.
func isPDCSIDisk(d *computepb.Disk) bool {
	meta := parsePDDescription(d.GetDescription())
	return meta.CreatedBy == pdCSIDriverName || meta.PVName != "" || strings.HasPrefix(d.GetName(), "pvc-")
}

// lastSegment returns what follows the final '/' of a Compute resource URL.
func lastSegment(url string) string {
	if i := strings.LastIndex(url, "/"); i >= 0 {
		return url[i+1:]
	}
	return url
}

// inRegion reports whether a zone ("us-central1-a") is in a region
// ("us-central1").
func inRegion(zone, region string) bool {
	return zone != "" && region != "" && strings.HasPrefix(zone, region+"-")
}

// diskCarriesInstallID reports whether a disk is attributed by an install id,
// which keeps it out of every name- or shape-based sweep.
func diskCarriesInstallID(d *computepb.Disk) bool {
	return d.GetLabels()[provider.ClusterIDLabel] != ""
}

// isOrphanCSIDisk is the ONE rule every shape-based disk sweep uses — the
// count, the opt-in purge (--purge-orphaned-volumes) and the leftover report:
// an unattached disk the PD CSI driver made (a pvc-* name, or its created-for
// description) that carries NO install id. Attached disks belong to instances
// and go with them; a disk carrying an install id is attributed exactly, and
// only `adhar down` for that cluster removes it, after asking.
func isOrphanCSIDisk(d *computepb.Disk) bool {
	if len(d.GetUsers()) > 0 || diskCarriesInstallID(d) {
		return false
	}
	return strings.HasPrefix(d.GetName(), "pvc-") || strings.Contains(d.GetDescription(), "storage.gke.io/created-for")
}
