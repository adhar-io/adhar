package helpers

import (
	"context"
	"errors"
	"testing"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
	ptypes "adhar-io/adhar/platform/types"
)

// listingCloud knows a fixed set of clusters and counts how often it is asked.
type listingCloud struct {
	pfactory.Provider
	clusters []*ptypes.Cluster
	err      error
	asked    int
}

func (l *listingCloud) ListClusters(context.Context) ([]*ptypes.Cluster, error) {
	l.asked++
	return l.clusters, l.err
}

// A teardown of an ENVIRONMENT looks in that environment's provider and
// nowhere else: a cluster of the same name elsewhere — the developer's local
// Kind cluster, typically — is not the environment's, and was being deleted
// when the cloud cluster was already gone.
func TestFindClusterInLooksOnlyInTheEnvironmentsProvider(t *testing.T) {
	cloud := &listingCloud{}
	local := &listingCloud{clusters: []*ptypes.Cluster{{ID: "adhar", Name: "adhar"}}}
	broken := &listingCloud{err: errors.New("401 Unauthorized")}
	pfactory.DefaultFactory.RegisterProvider("scoped-cloud", func(map[string]interface{}) (pfactory.Provider, error) { return cloud, nil })
	pfactory.DefaultFactory.RegisterProvider("scoped-local", func(map[string]interface{}) (pfactory.Provider, error) { return local, nil })
	pfactory.DefaultFactory.RegisterProvider("scoped-broken", func(map[string]interface{}) (pfactory.Provider, error) { return broken, nil })
	cfg := &config.Config{Providers: map[string]config.ConfigProviderConfig{
		"scoped-cloud": {Type: "scoped-cloud"}, "scoped-local": {Type: "scoped-local"}, "scoped-broken": {Type: "scoped-broken"},
	}}

	found, err := FindClusterIn(context.Background(), cfg, "scoped-cloud", "adhar", nil, nil)
	var nf *NotFoundError
	if found != nil || !errors.As(err, &nf) || !nf.Conclusive() {
		t.Fatalf("FindClusterIn = %+v, %v; want a conclusive not-found from the environment's provider", found, err)
	}
	if local.asked != 0 {
		t.Fatal("another provider was searched; its cluster of the same name is not the environment's")
	}

	if found, err := FindClusterIn(context.Background(), cfg, "scoped-local", "adhar", nil, nil); err != nil || found.ProviderName != "scoped-local" {
		t.Fatalf("the environment's own cluster was not found: %+v, %v", found, err)
	}

	_, err = FindClusterIn(context.Background(), cfg, "scoped-broken", "adhar", nil, nil)
	if !errors.As(err, &nf) || nf.Conclusive() {
		t.Fatalf("a provider that could not be asked produced %v; that is never a conclusive not-found", err)
	}
	if _, err := FindClusterIn(context.Background(), cfg, "not-configured", "adhar", nil, nil); err == nil {
		t.Fatal("an unconfigured provider was searched")
	}
}
