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
	"adhar-io/adhar/platform/types"
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

// fakeTags is DigitalOcean's tag service: what each tag is on, and what was
// deleted.
type fakeTags struct {
	godo.TagsService
	counts  map[string]int // tag -> resources carrying it
	deleted []string
}

func (f *fakeTags) Get(_ context.Context, name string) (*godo.Tag, *godo.Response, error) {
	n, ok := f.counts[name]
	if !ok {
		return nil, notFound(), errors.New("404 not found")
	}
	return &godo.Tag{Name: name, Resources: &godo.TaggedResources{Count: n, Volumes: &godo.TaggedVolumesResources{Count: n}}},
		&godo.Response{Response: &http.Response{StatusCode: 200}}, nil
}

func (f *fakeTags) Delete(_ context.Context, name string) (*godo.Response, error) {
	f.deleted = append(f.deleted, name)
	return &godo.Response{Response: &http.Response{StatusCode: 204}}, nil
}

// Deleting a DigitalOcean tag strips it from every volume carrying it. For a
// cluster from before install ids, the name tag is the only evidence its kept
// volumes were its own, so `adhar down` keeps it while volumes carry it — and
// `adhar cluster delete` (which deletes those volumes itself) does not.
func TestTheNameTagSurvivesWhileLegacyVolumesCarryIt(t *testing.T) {
	carried := &godo.Tag{Name: "adhar-cluster-adhar", Resources: &godo.TaggedResources{Count: 2, Volumes: &godo.TaggedVolumesResources{Count: 2}}}
	bare := &godo.Tag{Name: "adhar-cluster-adhar", Resources: &godo.TaggedResources{}}
	for _, tc := range []struct {
		name string
		keep bool
		tag  *godo.Tag
		err  error
		want bool
	}{
		{"adhar down, volumes still carry it", true, carried, nil, false},
		{"adhar down, the tag cannot be read", true, nil, errors.New("503"), false},
		{"adhar down, nothing carries it", true, bare, nil, true},
		{"adhar cluster delete deletes the volumes and the tag", false, carried, nil, true},
	} {
		if got := nameTagMayBeDeleted(tc.keep, tc.tag, tc.err); got != tc.want {
			t.Errorf("%s: nameTagMayBeDeleted = %v, want %v", tc.name, got, tc.want)
		}
	}

	// And the volume step removes it once nothing carries it any more.
	tags := &fakeTags{counts: map[string]int{"adhar-cluster-adhar": 0, "adhar-cluster-busy": 1}}
	p, _ := doProvider(nil)
	p.client.Tags = tags
	if err := p.AfterClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "adhar"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.AfterClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "busy"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.AfterClusterVolumes(context.Background(), provider.VolumeOwner{ClusterName: "kept"},
		[]provider.BlockVolume{{ID: "v1"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags.deleted, ",") != "adhar-cluster-adhar" {
		t.Fatalf("deleted tags %v; want only the name tag nothing carries", tags.deleted)
	}
}

type emptyDroplets struct{ godo.DropletsService }

func (emptyDroplets) ListByTag(context.Context, string, *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
	return nil, &godo.Response{Response: &http.Response{StatusCode: 200}}, nil
}
func (emptyDroplets) DeleteByTag(context.Context, string) (*godo.Response, error) {
	return &godo.Response{Response: &http.Response{StatusCode: 204}}, nil
}

type emptyFirewalls struct{ godo.FirewallsService }

func (emptyFirewalls) List(context.Context, *godo.ListOptions) ([]godo.Firewall, *godo.Response, error) {
	return nil, nil, nil
}

type emptyVPCs struct{ godo.VPCsService }

func (emptyVPCs) List(context.Context, *godo.ListOptions) ([]*godo.VPC, *godo.Response, error) {
	return nil, nil, nil
}

type emptyKeys struct{ godo.KeysService }

func (emptyKeys) List(context.Context, *godo.ListOptions) ([]godo.Key, *godo.Response, error) {
	return nil, nil, nil
}

// The real teardown, end to end: under `adhar down` a legacy cluster's name tag
// outlives its droplets while its volumes carry it, and its volumes are left
// for the volume step; under `adhar cluster delete` both go, as before.
func TestDeleteComputeClusterKeepsTheEvidenceAdharDownNeeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name        string
		keep        bool
		wantTagGone bool
		wantDeleted string
	}{
		{"adhar down", true, false, ""},
		{"adhar cluster delete", false, true, "named"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := &fakeTags{counts: map[string]int{"adhar-cluster-adhar": 1}}
			st := &fakeStorage{vols: []godo.Volume{vol("named", "pvc-named", []string{"adhar-cluster-adhar"})}}
			p := &Provider{config: &Config{Region: "blr1", KeepClusterVolumes: tc.keep}, clusters: map[string]*types.Cluster{},
				client: &godo.Client{Storage: st, Droplets: emptyDroplets{}, Tags: tags, Firewalls: emptyFirewalls{}, VPCs: emptyVPCs{}, Keys: emptyKeys{}}}
			if err := p.deleteComputeCluster(context.Background(), "adhar"); err != nil {
				t.Fatal(err)
			}
			gone := strings.Contains(strings.Join(tags.deleted, ","), "adhar-cluster-adhar")
			if gone != tc.wantTagGone {
				t.Errorf("name tag deleted = %v, want %v", gone, tc.wantTagGone)
			}
			if strings.Join(st.deleted, ",") != tc.wantDeleted {
				t.Errorf("volumes deleted = %v, want %q", st.deleted, tc.wantDeleted)
			}
		})
	}
}
