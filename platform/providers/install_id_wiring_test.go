package provider

import (
	"context"
	"testing"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/types"
)

// recordingProvider is a cloud that only knows how to create a cluster, and
// remembers what it was handed.
type recordingProvider struct {
	Provider
	region      string
	sawID       string
	recordAtNow *ClusterInstall
}

func (r *recordingProvider) Authenticate(context.Context, *types.Credentials) error { return nil }
func (r *recordingProvider) ValidatePermissions(context.Context) error              { return nil }
func (r *recordingProvider) ListClusters(context.Context) ([]*types.Cluster, error) {
	return nil, nil
}

func (r *recordingProvider) CreateCluster(_ context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	r.sawID = spec.ClusterInstallID
	// What the CLI had written down at the moment the cloud was asked.
	r.recordAtNow, _ = ActiveClusterInstall("fakecloud", r.region, spec.Name)
	return &types.Cluster{ID: "fake-" + spec.Name, Name: spec.Name, Status: types.ClusterStatusRunning}, nil
}

// The install id must be RECORDED before the cloud is asked to create anything,
// and handed to the provider in the spec — a volume tagged with an id nobody
// wrote down is exactly as unattributable as an untagged one.
func TestTheInstallIDIsRecordedBeforeTheClusterIsCreated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &recordingProvider{region: "region-1"}
	f := NewFactory()
	f.RegisterProvider("fakecloud", func(map[string]interface{}) (Provider, error) { return fake, nil })

	env := &config.ResolvedEnvironmentConfig{Name: "dev", ResolvedProvider: "fakecloud", ResolvedRegion: "region-1"}
	if _, err := NewProviderManager(f).ProvisionEnvironment(context.Background(), env, ProvisionOptions{ClusterName: "adhar"}); err != nil {
		t.Fatal(err)
	}
	if !ValidClusterInstallID(fake.sawID) {
		t.Fatalf("CreateCluster was handed install id %q", fake.sawID)
	}
	if fake.recordAtNow == nil || fake.recordAtNow.ID != fake.sawID {
		t.Fatalf("at CreateCluster the CLI's record was %+v; want install %s already written", fake.recordAtNow, fake.sawID)
	}
}
