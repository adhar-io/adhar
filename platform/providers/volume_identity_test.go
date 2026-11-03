package provider

import (
	"context"
	"testing"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/types"
)

const testClusterUUID = "0b7a3c3e-1f0e-4b8e-9a6f-2a1d3c4b5e6f"

// A managed cluster's cloud UUID is what Civo's and DOKS's own CSI drivers
// stamp on its volumes. It is recorded on its own, once, and never in place of
// a name.
func TestACloudClusterIDIsRecordedOnceAndOnlyIfItIsAUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst, err := RecordClusterInstall("civo", "lon1", "adhar")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := RecordProviderClusterID("civo", "lon1", "adhar", "adhar-cluster-adhar"); err == nil {
		t.Fatal("a name-derived handle was accepted as a cloud cluster id; a name attributes nothing")
	}
	wrote, err := RecordProviderClusterID("civo", "lon1", "adhar", testClusterUUID)
	if err != nil || !wrote {
		t.Fatalf("RecordProviderClusterID = %v, %v", wrote, err)
	}
	if wrote, err := RecordProviderClusterID("civo", "lon1", "adhar", testClusterUUID); err != nil || wrote {
		t.Fatalf("recording the same id twice wrote again: %v, %v", wrote, err)
	}

	recs, err := LoadClusterInstalls("civo", "lon1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %+v; want the install record and one for the cloud id", recs)
	}
	if recs[0].ID != inst.ID || recs[0].ProviderClusterID != "" || !recs[0].Active() {
		t.Errorf("the install record was changed: %+v", recs[0])
	}
	if recs[1].ProviderClusterID != testClusterUUID || recs[1].Owner().ProviderClusterID != testClusterUUID {
		t.Errorf("cloud-id record = %+v", recs[1])
	}
}

// uuidCloud is a managed cloud whose clusters are known by a UUID.
type uuidCloud struct {
	Provider
	atCreate *ClusterInstall
}

func (u *uuidCloud) Authenticate(context.Context, *types.Credentials) error { return nil }
func (u *uuidCloud) ValidatePermissions(context.Context) error              { return nil }
func (u *uuidCloud) ListClusters(context.Context) ([]*types.Cluster, error) {
	return nil, nil
}
func (u *uuidCloud) CreateCluster(_ context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	return &types.Cluster{ID: testClusterUUID, Name: spec.Name, Status: types.ClusterStatusRunning}, nil
}

// `adhar up` writes the managed cluster's UUID next to its install id as soon
// as the cloud returns it, so a cluster later deleted some other way still has
// its volumes found by `adhar down`.
func TestProvisioningRecordsAManagedClustersCloudID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := NewFactory()
	f.RegisterProvider("fakecloud", func(map[string]interface{}) (Provider, error) { return &uuidCloud{}, nil })
	env := &config.ResolvedEnvironmentConfig{Name: "dev", ResolvedProvider: "fakecloud", ResolvedRegion: "region-1"}
	if _, err := NewProviderManager(f).ProvisionEnvironment(context.Background(), env, ProvisionOptions{ClusterName: "adhar"}); err != nil {
		t.Fatal(err)
	}
	active, err := ActiveClusterInstall("fakecloud", "region-1", "adhar")
	if err != nil || active == nil {
		t.Fatalf("no active record: %v", err)
	}
	if active.ProviderClusterID != testClusterUUID || !ValidClusterInstallID(active.ID) {
		t.Fatalf("record = %+v; want the install id AND the cloud's cluster UUID", active)
	}
}

// The mode a teardown reads is the mode `adhar up` used — provider block plus
// the environment's clusterConfig — not the provider block alone.
func TestEnvironmentClusterModeIsTheModeAdharUpUsed(t *testing.T) {
	block := &config.ConfigProviderConfig{Type: "digitalocean", ClusterMode: "compute"}
	for _, tc := range []struct {
		name string
		env  *config.ResolvedEnvironmentConfig
		want string
	}{
		{"nil is the default", nil, ClusterModeCompute},
		{"the block alone", &config.ResolvedEnvironmentConfig{ProviderConfig: block}, ClusterModeCompute},
		{"an environment's clusterConfig overrides the block", &config.ResolvedEnvironmentConfig{ProviderConfig: block,
			ResolvedClusterConfig: []config.KeyValueConfig{{Key: "clusterMode", Value: "provided"}}}, ClusterModeProvided},
		{"no block, mode in clusterConfig", &config.ResolvedEnvironmentConfig{
			ResolvedClusterConfig: []config.KeyValueConfig{{Key: "clusterMode", Value: "managed"}}}, ClusterModeManaged},
	} {
		got, err := EnvironmentClusterMode(tc.env)
		if err != nil || got != tc.want {
			t.Errorf("%s: EnvironmentClusterMode = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := EnvironmentClusterMode(&config.ResolvedEnvironmentConfig{ProviderConfig: block,
		ResolvedClusterConfig: []config.KeyValueConfig{{Key: "clusterMode", Value: "gke"}}}); err == nil {
		t.Error("a removed per-cloud spelling in clusterConfig was accepted")
	}
}
