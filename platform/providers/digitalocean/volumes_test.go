package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	provider "adhar-io/adhar/platform/providers"
)

type fakeStorage struct {
	godo.StorageService
	vols    []godo.Volume
	deleted []string
}

func notFound() *godo.Response {
	return &godo.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}
}

func (f *fakeStorage) ListVolumes(_ context.Context, _ *godo.ListVolumeParams) ([]godo.Volume, *godo.Response, error) {
	return f.vols, &godo.Response{Response: &http.Response{StatusCode: 200}}, nil
}

func (f *fakeStorage) GetVolume(_ context.Context, id string) (*godo.Volume, *godo.Response, error) {
	for i := range f.vols {
		if f.vols[i].ID == id {
			v := f.vols[i]
			return &v, &godo.Response{Response: &http.Response{StatusCode: 200}}, nil
		}
	}
	return nil, notFound(), errors.New("404 not found")
}

func (f *fakeStorage) DeleteVolume(_ context.Context, id string) (*godo.Response, error) {
	for i := range f.vols {
		if f.vols[i].ID == id {
			if len(f.vols[i].DropletIDs) > 0 {
				return &godo.Response{Response: &http.Response{StatusCode: 409}}, errors.New("409 attached")
			}
			f.deleted = append(f.deleted, id)
			f.vols = append(f.vols[:i], f.vols[i+1:]...)
			return &godo.Response{Response: &http.Response{StatusCode: 204}}, nil
		}
	}
	return notFound(), errors.New("404 not found")
}

type fakeDroplets struct {
	godo.DropletsService
	droplets []godo.Droplet
}

func (f *fakeDroplets) List(context.Context, *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
	return f.droplets, &godo.Response{Response: &http.Response{StatusCode: 200}}, nil
}

func doProvider(vols []godo.Volume, droplets ...godo.Droplet) (*Provider, *fakeStorage) {
	st := &fakeStorage{vols: vols}
	return &Provider{
		config: &Config{Region: "blr1"},
		client: &godo.Client{Storage: st, Droplets: &fakeDroplets{droplets: droplets}},
	}, st
}

func vol(id, name string, tags []string, droplets ...int) godo.Volume {
	return godo.Volume{ID: id, Name: name, SizeGigaBytes: 8, Tags: tags, DropletIDs: droplets,
		Region: &godo.Region{Slug: "blr1"}, CreatedAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
}

func TestDOClusterVolumesAreExactlyThisInstallsVolumes(t *testing.T) {
	const uuid = "0b7a3c3e-1f0e-4b8e-9a6f-2a1d3c4b5e6f"
	p, _ := doProvider([]godo.Volume{
		vol("v1", "pvc-1", []string{provider.DigitalOceanClusterIDTag("aaaaaaaaaaaaaaaa")}),
		vol("v2", "pvc-2", []string{provider.DigitalOceanClusterIDTag("bbbbbbbbbbbbbbbb")}),
		vol("v3", "pvc-3", []string{"adhar-cluster-adhar"}),
		vol("v4", "pvc-4", []string{doksVolumeTag(uuid)}),
	})
	got, err := p.ClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar", InstallID: "aaaaaaaaaaaaaaaa"})
	if err != nil || len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("ClusterVolumes(install id) = %+v, %v; want only v1", got, err)
	}
	if got[0].DeleteCommand != "doctl compute volume delete v1 --force" {
		t.Errorf("delete command = %q", got[0].DeleteCommand)
	}
	got, _ = p.ClusterVolumes(context.Background(), provider.VolumeOwner{ProviderClusterID: uuid})
	if len(got) != 1 || got[0].ID != "v4" {
		t.Fatalf("ClusterVolumes(DOKS uuid) = %+v; want only v4", got)
	}
	// A name — even the name tag the driver used to write — attributes nothing,
	// and neither does a compute cluster's name-derived "id".
	for _, owner := range []provider.VolumeOwner{{ClusterName: "adhar"}, {ProviderClusterID: "adhar-cluster-adhar"}} {
		if got, _ := p.ClusterVolumes(context.Background(), owner); len(got) != 0 {
			t.Errorf("owner %+v was given volumes without an id: %+v", owner, got)
		}
	}
}

func TestDOLegacyVolumesNeedEvidenceAndNoClusterDropletLeft(t *testing.T) {
	vols := []godo.Volume{
		vol("named", "pvc-named", []string{"adhar-cluster-adhar"}),
		vol("untagged", "pvc-untagged", nil),
		vol("other-cluster", "pvc-other", []string{"adhar-cluster-prod"}),
		vol("with-id", "pvc-id", []string{provider.DigitalOceanClusterIDTag("aaaaaaaaaaaaaaaa"), "adhar-cluster-adhar"}),
		vol("attached", "pvc-att", []string{"adhar-cluster-adhar"}, 42),
		vol("handmade", "my-backup", nil),
	}
	ids := func(l provider.LegacyVolumes) string {
		var out []string
		for _, v := range l.Volumes {
			out = append(out, v.ID)
		}
		return strings.Join(out, ",")
	}
	owner := provider.VolumeOwner{ClusterName: "adhar"}

	// A droplet of THIS cluster left: nothing is provable.
	p, _ := doProvider(vols, godo.Droplet{ID: 1, Name: "adhar-master", Tags: []string{"adhar-cluster-adhar"}, Region: &godo.Region{Slug: "blr1"}})
	got, err := p.LegacyVolumes(context.Background(), owner)
	if err != nil || got.Provable || len(got.Volumes) != 0 {
		t.Fatalf("with a droplet of the cluster left: %+v, %v", got, err)
	}

	// Another cluster's droplet left: only the name-tagged leftovers.
	p, _ = doProvider(vols, godo.Droplet{ID: 2, Name: "prod-master", Tags: []string{"adhar-cluster-prod"}, Region: &godo.Region{Slug: "blr1"}})
	got, err = p.LegacyVolumes(context.Background(), owner)
	if err != nil || !got.Provable || ids(got) != "named" {
		t.Fatalf("with another cluster running: %s (%+v, %v); want only the name-tagged volume", ids(got), got, err)
	}

	// Nothing left anywhere: name-tagged and untagged pvc-* both.
	p, _ = doProvider(vols)
	got, err = p.LegacyVolumes(context.Background(), owner)
	if err != nil || !got.Provable || ids(got) != "named,untagged" {
		t.Fatalf("with nothing running: %s; want named,untagged", ids(got))
	}
}

func TestDOVolumeStepDeletesDetachedVolumesAndVerifies(t *testing.T) {
	p, st := doProvider([]godo.Volume{
		vol("free", "pvc-free", []string{provider.DigitalOceanClusterIDTag("aaaaaaaaaaaaaaaa")}),
		vol("held", "pvc-held", []string{provider.DigitalOceanClusterIDTag("aaaaaaaaaaaaaaaa")}, 7),
	})
	vols, _ := p.ClusterVolumes(context.Background(), provider.VolumeOwner{InstallID: "aaaaaaaaaaaaaaaa"})
	res := provider.ReapVolumes(context.Background(), p, vols, provider.ReapOptions{
		DetachTimeout: 20 * time.Millisecond, VerifyTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond,
	})
	if len(res.Deleted) != 1 || res.Deleted[0].ID != "free" || len(res.StillAttached) != 1 || strings.Join(st.deleted, ",") != "free" {
		t.Fatalf("result = %+v, deleted %v", res, st.deleted)
	}
}

// The install id must reach the CSI driver, whose one --do-tag tags every
// volume it creates — adhar-block, adhar-ci, do-block-storage alike.
func TestTheInstallIDReachesTheDOCSIDriver(t *testing.T) {
	p := &Provider{config: &Config{Region: "blr1"}, token: "x"}
	tag := computeVolumeTag("adhar-cluster-adhar", "a1b2c3d4e5f6g7h8")
	joined := ""
	for _, s := range p.doCloudIntegrationSteps("vpc-1", tag) {
		joined += s.Cmd + "\n"
	}
	if !strings.Contains(joined, `"value":"--do-tag=adhar-cluster-id:a1b2c3d4e5f6g7h8"`) {
		t.Fatalf("the CSI driver is not tagged with the install id:\n%s", joined)
	}
	if !strings.Contains(joined, "name: adhar-block") {
		t.Fatal("adhar-block is no longer created; this check checks nothing")
	}
	if computeVolumeTag("adhar-cluster-adhar", "") != "adhar-cluster-adhar" {
		t.Error("a cluster with no install id must keep the name tag it always had")
	}
}
