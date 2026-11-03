package gcp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"

	provider "adhar-io/adhar/platform/providers"
)

type fakeCompute struct {
	disks     []*computepb.Disk
	instances []*computepb.Instance
	deleted   []string
}

func (f *fakeCompute) listDisks(context.Context, string) ([]*computepb.Disk, error) {
	return f.disks, nil
}

func (f *fakeCompute) getDisk(_ context.Context, _, zone, name string) (*computepb.Disk, error) {
	for _, d := range f.disks {
		if d.GetName() == name && lastSegment(d.GetZone()) == zone {
			return d, nil
		}
	}
	return nil, &googleapi.Error{Code: http.StatusNotFound}
}

func (f *fakeCompute) deleteDisk(_ context.Context, _, zone, name string) error {
	for i, d := range f.disks {
		if d.GetName() == name && lastSegment(d.GetZone()) == zone {
			if len(d.GetUsers()) > 0 {
				return errors.New("resourceInUseByAnotherResource")
			}
			f.deleted = append(f.deleted, zone+"/"+name)
			f.disks = append(f.disks[:i], f.disks[i+1:]...)
			return nil
		}
	}
	return &googleapi.Error{Code: http.StatusNotFound}
}

func (f *fakeCompute) listInstances(context.Context, string) ([]*computepb.Instance, error) {
	return f.instances, nil
}

func ptr[T any](v T) *T { return &v }

// pdDisk is a disk the way the PD CSI driver creates one.
func pdDisk(name, zone string, labels map[string]string, users ...string) *computepb.Disk {
	return &computepb.Disk{
		Name:              ptr(name),
		Zone:              ptr("https://www.googleapis.com/compute/v1/projects/p/zones/" + zone),
		SizeGb:            ptr(int64(10)),
		CreationTimestamp: ptr("2026-10-30T15:32:54.707-07:00"),
		Description: ptr(`{"kubernetes.io/created-for/pv/name":"` + name + `","kubernetes.io/created-for/pvc/name":"data-0",` +
			`"kubernetes.io/created-for/pvc/namespace":"adhar-system","storage.gke.io/created-by":"pd.csi.storage.gke.io"}`),
		Labels: labels,
		Users:  users,
	}
}

func gcpProvider(f *fakeCompute) *Provider {
	return &Provider{config: &Config{ProjectID: "p", Region: "us-central1", Zone: "us-central1-a"}, volumeAPI: f}
}

func TestGCPClusterVolumesAreExactlyThisInstallsDisks(t *testing.T) {
	f := &fakeCompute{disks: []*computepb.Disk{
		pdDisk("pvc-mine", "us-central1-a", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}),
		pdDisk("pvc-mine-other-zone", "us-central1-b", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, "zones/us-central1-b/instances/adhar-worker-1"),
		pdDisk("pvc-other-install", "us-central1-a", map[string]string{provider.ClusterIDLabel: "bbbbbbbbbbbbbbbb"}),
		pdDisk("pvc-legacy", "us-central1-a", nil),
	}}
	got, err := gcpProvider(f).ClusterVolumes(context.Background(), provider.VolumeOwner{InstallID: "aaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ClusterVolumes = %+v, want the two disks labelled with this install id", got)
	}
	for _, v := range got {
		if !strings.HasPrefix(v.Name, "pvc-mine") || v.PVC() != "adhar-system/data-0" || v.Created.IsZero() {
			t.Errorf("unexpected or incomplete volume %+v", v)
		}
		if !strings.Contains(v.DeleteCommand, "gcloud compute disks delete "+v.Name+" --zone "+v.Location) {
			t.Errorf("no usable delete command for %s: %q", v.ID, v.DeleteCommand)
		}
	}
	if none, _ := gcpProvider(f).ClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"}); len(none) != 0 {
		t.Errorf("a cluster with no install id was given volumes by name: %+v", none)
	}
}

func TestGCPLegacyDisksRequireThatNoClusterNodeRemainsInTheRegion(t *testing.T) {
	legacy := []*computepb.Disk{pdDisk("pvc-legacy", "us-central1-a", nil)}
	inst := func(name, zone string, tags []string, labels map[string]string) *computepb.Instance {
		return &computepb.Instance{Name: ptr(name), Zone: ptr("zones/" + zone), Tags: &computepb.Tags{Items: tags}, Labels: labels}
	}
	for name, i := range map[string]*computepb.Instance{
		"a platform node": inst("adhar-worker-1", "us-central1-c", []string{"adhar-node"}, nil),
		"a GKE node":      inst("gke-team-pool-1", "us-central1-a", nil, map[string]string{"goog-k8s-cluster-name": "team"}),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := gcpProvider(&fakeCompute{disks: legacy, instances: []*computepb.Instance{i}}).
				LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Provable || len(got.Volumes) != 0 {
				t.Fatalf("with %s in the region the legacy disks were offered: %+v", name, got)
			}
		})
	}
	// A node in ANOTHER region, or an unrelated VM, does not block.
	got, err := gcpProvider(&fakeCompute{disks: legacy, instances: []*computepb.Instance{
		inst("adhar-worker-1", "europe-west1-b", []string{"adhar-node"}, nil),
		inst("bastion", "us-central1-a", []string{"ssh"}, nil),
	}}).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil || !got.Provable || len(got.Volumes) != 1 {
		t.Fatalf("legacy = %+v, %v; want the one disk offered", got, err)
	}
}

func TestGCPLegacyDisksAreOnlyUnclaimedCSIDisksInTheRegion(t *testing.T) {
	f := &fakeCompute{disks: []*computepb.Disk{
		pdDisk("pvc-offer", "us-central1-a", nil),
		pdDisk("pvc-id", "us-central1-a", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}),
		pdDisk("pvc-attached", "us-central1-a", nil, "zones/us-central1-a/instances/x"),
		pdDisk("pvc-other-region", "europe-west1-b", nil),
		{Name: ptr("handmade"), Zone: ptr("zones/us-central1-a"), Description: ptr("my scratch disk")},
	}}
	got, err := gcpProvider(f).LegacyVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Volumes) != 1 || got.Volumes[0].Name != "pvc-offer" {
		t.Fatalf("legacy = %+v, want only pvc-offer", got.Volumes)
	}
}

func TestGCPVolumeStepDeletesDetachedDisksAndVerifies(t *testing.T) {
	f := &fakeCompute{disks: []*computepb.Disk{
		pdDisk("pvc-free", "us-central1-a", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}),
		pdDisk("pvc-held", "us-central1-a", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"}, "zones/us-central1-a/instances/not-ours"),
	}}
	p := gcpProvider(f)
	vols, _ := p.ClusterVolumes(context.Background(), provider.VolumeOwner{InstallID: "aaaaaaaaaaaaaaaa"})
	res := provider.ReapVolumes(context.Background(), p, vols, provider.ReapOptions{
		DetachTimeout: 20 * time.Millisecond, VerifyTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond,
	})
	if len(res.Deleted) != 1 || res.Deleted[0].Name != "pvc-free" || len(res.StillAttached) != 1 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if strings.Join(f.deleted, ",") != "us-central1-a/pvc-free" {
		t.Fatalf("deleted %v", f.deleted)
	}
}

// The install id must reach adhar-block (and so adhar-ci, the controller's copy
// of it) as a GCE disk label.
func TestTheInstallIDReachesTheGCPBlockStorageClass(t *testing.T) {
	p := &Provider{config: &Config{ProjectID: "p", Region: "us-central1", Zone: "us-central1-a"}}
	steps, err := p.cloudIntegrationSteps("adhar", "a1b2c3d4e5f6g7h8")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range steps {
		if strings.Contains(s.Cmd, "name: adhar-block") {
			found = true
			if !strings.Contains(s.Cmd, `labels: "adhar-cluster-id=a1b2c3d4e5f6g7h8"`) {
				t.Errorf("adhar-block does not label its disks with the install id:\n%s", s.Cmd)
			}
		}
	}
	if !found {
		t.Fatal("adhar-block is no longer created; this check checks nothing")
	}
	if params := p.blockStorageParameters(""); params["labels"] != "" {
		t.Errorf("no install id still produced a label parameter: %v", params)
	}
}

func TestAnIDLabelledDiskIsNeverAnOrphan(t *testing.T) {
	if !diskCarriesInstallID(pdDisk("pvc-x", "us-central1-a", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"})) {
		t.Fatal("an id-labelled disk was not recognised, so the orphan purge would take it")
	}
	if diskCarriesInstallID(pdDisk("pvc-x", "us-central1-a", nil)) {
		t.Fatal("an unlabelled disk was treated as attributed")
	}
	if isOrphanCSIDisk(pdDisk("pvc-x", "us-central1-a", map[string]string{provider.ClusterIDLabel: "aaaaaaaaaaaaaaaa"})) {
		t.Fatal("an id-labelled disk is an orphan to the sweeps, so --purge-orphaned-volumes would delete it unasked")
	}
	if isOrphanCSIDisk(pdDisk("pvc-x", "us-central1-a", nil, "zones/us-central1-a/instances/n1")) {
		t.Fatal("an attached disk is an orphan to the sweeps")
	}
	if !isOrphanCSIDisk(pdDisk("pvc-x", "us-central1-a", nil)) {
		t.Fatal("an unattached, unlabelled CSI disk is no longer an orphan; the purge would do nothing")
	}

	// And every sweep goes through that one rule: the purge, the count it
	// reports, and the leftover report. A loop with its own condition is how an
	// id-labelled disk would slip back into the purge.
	src, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"countOrphanedDisks", "sweepOrphanedDisks", "findOrphanedResources"} {
		body := string(src)
		i := strings.Index(body, "func (p *Provider) "+fn+"(")
		if i < 0 {
			t.Fatalf("%s is gone; this check checks nothing", fn)
		}
		body = body[i:]
		if j := strings.Index(body[1:], "\nfunc "); j > 0 {
			body = body[:j+1]
		}
		if !strings.Contains(body, "isOrphanCSIDisk(disk)") {
			t.Errorf("%s does not select disks with isOrphanCSIDisk", fn)
		}
		if strings.Contains(body, `"pvc-"`) {
			t.Errorf("%s carries its own pvc- condition beside isOrphanCSIDisk", fn)
		}
	}
}

// On GKE the install id travels as a cluster resource label, which Google
// also puts on the nodes' boot disks. The lookup must list only the PD CSI
// disks behind PersistentVolumes — never a boot disk carrying the same label.
func TestGKEClustersCarryTheInstallIDButBootDisksAreNotVolumes(t *testing.T) {
	labels := gkeResourceLabels("adhar", "a1b2c3d4e5f6g7h8", map[string]string{"Team": "Platform"})
	if labels[provider.ClusterIDLabel] != "a1b2c3d4e5f6g7h8" || labels["managed-by"] != "adhar" || labels["team"] != "platform" {
		t.Fatalf("GKE resource labels = %v; want the install id beside the platform's labels", labels)
	}
	if _, has := gkeResourceLabels("adhar", "", nil)[provider.ClusterIDLabel]; has {
		t.Error("a cluster with no install id was given an empty id label")
	}

	boot := &computepb.Disk{Name: ptr("gke-adhar-default-pool-1a2b3c4d-x7k9"), Zone: ptr("zones/us-central1-a"),
		Labels: map[string]string{provider.ClusterIDLabel: "a1b2c3d4e5f6g7h8", "goog-k8s-cluster-name": "adhar"}}
	f := &fakeCompute{disks: []*computepb.Disk{
		boot,
		pdDisk("pvc-data", "us-central1-a", map[string]string{provider.ClusterIDLabel: "a1b2c3d4e5f6g7h8"}),
	}}
	got, err := gcpProvider(f).ClusterVolumes(context.Background(), provider.VolumeOwner{InstallID: "a1b2c3d4e5f6g7h8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "pvc-data" {
		t.Fatalf("ClusterVolumes = %+v; want only the PersistentVolume's disk", got)
	}
}
