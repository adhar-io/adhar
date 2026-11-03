package digitalocean

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/digitalocean/godo"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// snapshotStorage returns a fresh slice from ListVolumes, as the real API
// does, so a sweep that deletes while it iterates sees the listing it asked for.
type snapshotStorage struct{ *fakeStorage }

func (s snapshotStorage) ListVolumes(ctx context.Context, p *godo.ListVolumeParams) ([]godo.Volume, *godo.Response, error) {
	vols, resp, err := s.fakeStorage.ListVolumes(ctx, p)
	return append([]godo.Volume(nil), vols...), resp, err
}

// --purge-orphaned-volumes deletes, without listing them first, the detached
// pvc-* volumes that no cluster's tag claims. A DOKS cluster claims its volumes
// with `k8s:<uuid>` (and its droplets with `k8s`), which the purge did not
// recognise: on an account that also runs DOKS it took a running cluster's
// detached volumes — although its help text promises to skip any volume that
// carries another cluster's tag. Every sweep, and the legacy offer, now share
// one rule (namesACluster).
func TestThePurgeNeverTakesAVolumeAnotherClusterTagged(t *testing.T) {
	claimed := []godo.Volume{
		vol("doks", "pvc-doks", []string{"k8s:3f6a5b2e-1c4d-4e8f-9a0b-1c2d3e4f5a6b"}),
		vol("doks-bare", "pvc-doks-bare", []string{"k8s"}),
		vol("other", "pvc-other", []string{"adhar-cluster-other"}),
		vol("install", "pvc-install", []string{provider.DigitalOceanClusterIDTag("aaaaaaaaaaaaaaaa")}),
	}
	fixture := func(extra ...godo.Volume) []godo.Volume {
		out := append([]godo.Volume{}, claimed...)
		out = append(out,
			vol("attached", "pvc-attached", nil, 42),
			vol("handmade", "backups", nil),
			vol("orphan", "pvc-orphan", nil),
		)
		return append(out, extra...)
	}
	newProvider := func(vols []godo.Volume) (*Provider, *fakeStorage) {
		p, st := doProvider(vols)
		p.client.Storage = snapshotStorage{st}
		return p, st
	}

	// The purge after the cluster is gone (PurgeOrphanedVolumes).
	p, st := newProvider(fixture())
	n, errs := p.PurgeOrphanedVolumes(context.Background())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := strings.Join(st.deleted, ","); got != "orphan" || n != 1 {
		t.Fatalf("PurgeOrphanedVolumes deleted %q (%d); only the unclaimed pvc-* volume may go", got, n)
	}

	// The purge while the cluster is being deleted (deleteComputeVolumes).
	p, st = newProvider(fixture(vol("mine", "pvc-mine", []string{"adhar-cluster-adhar"})))
	p.config.PurgeOrphanedVolumes = true
	p.deleteComputeVolumes(context.Background(), "adhar-cluster-adhar")
	got := append([]string{}, st.deleted...)
	sort.Strings(got)
	if strings.Join(got, ",") != "mine,orphan" {
		t.Fatalf("deleteComputeVolumes with the purge deleted %v; want the cluster's own volume and the unclaimed one", got)
	}

	// And the legacy offer agrees: a volume a cluster's tag claims is nobody's leftover.
	for _, v := range claimed {
		if ok, _ := legacyDOCandidate(v, "adhar-cluster-adhar", true); ok {
			t.Errorf("volume %s tagged %v was offered as a legacy leftover", v.ID, v.Tags)
		}
	}
	if ok, _ := legacyDOCandidate(vol("orphan", "pvc-orphan", nil), "adhar-cluster-adhar", true); !ok {
		t.Error("an untagged detached pvc-* volume is no longer offered; the rule now refuses everything")
	}
}

// `adhar down --purge-orphaned-volumes` on a cluster that is still there runs
// the purge inside DeleteCluster, as the AWS, GCP, Azure and Civo teardowns do,
// while the cluster's own volumes — and every volume some cluster's tag claims —
// stay for the volume step and its confirmation. v0.1.66 skipped the whole sweep
// under `adhar down` (which always sets keepClusterVolumes), so on DigitalOcean
// the flag silently did nothing whenever the cluster was found.
func TestAdharDownStillPurgesWhenItFindsTheCluster(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name  string
		purge bool
		want  string
	}{
		{"adhar down", false, ""},
		{"adhar down --purge-orphaned-volumes", true, "orphan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStorage{vols: []godo.Volume{
				vol("named", "pvc-named", []string{"adhar-cluster-adhar"}),
				vol("install", "pvc-install", []string{provider.DigitalOceanClusterIDTag("aaaaaaaaaaaaaaaa")}),
				vol("doks", "pvc-doks", []string{"k8s:3f6a5b2e-1c4d-4e8f-9a0b-1c2d3e4f5a6b"}),
				vol("attached", "pvc-attached", nil, 42),
				vol("handmade", "backups", nil),
				vol("orphan", "pvc-orphan", nil),
			}}
			tags := &fakeTags{counts: map[string]int{"adhar-cluster-adhar": 1}}
			p := &Provider{
				config:   &Config{Region: "blr1", KeepClusterVolumes: true, PurgeOrphanedVolumes: tc.purge},
				clusters: map[string]*types.Cluster{},
				client: &godo.Client{Storage: snapshotStorage{st}, Droplets: emptyDroplets{}, Tags: tags,
					Firewalls: emptyFirewalls{}, VPCs: emptyVPCs{}, Keys: emptyKeys{}},
			}
			if err := p.deleteComputeCluster(context.Background(), "adhar"); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(st.deleted, ","); got != tc.want {
				t.Fatalf("deleteComputeCluster under `adhar down` deleted %q, want %q: the purge must take the unclaimed "+
					"pvc-* volume when asked, and nothing a cluster tag claims, attached, or not made by the CSI driver", got, tc.want)
			}
			if strings.Contains(strings.Join(tags.deleted, ","), "adhar-cluster-adhar") {
				t.Error("the name tag was deleted while a legacy volume still carries it")
			}
		})
	}
}
