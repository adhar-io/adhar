package up

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// adhar-cluster-spec records the cluster mode `adhar up` provisioned with: the
// provider block AND the environment's clusterConfig
// (pfactory.EnvironmentClusterMode), the map ProvisionEnvironment parses.
//
// It read the block alone. An environment that sets `clusterMode: provided` in
// its clusterConfig is ADOPTED by `adhar up`, yet was recorded as compute — the
// one fact the in-cluster controllers cannot work out for themselves — so the
// controller treated someone else's cluster as one the platform built (its own
// Cilium over the operator's, an autoscaler growing it), and the install id
// this machine remembers for a cluster of that name was written into it, for a
// later teardown to attribute volumes by.
func TestTheClusterSpecRecordsTheModeTheEnvironmentAskedFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := pfactory.RecordClusterInstall(dnsDigitalOcean, testRegion, "adhar"); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	env := installIDEnv()
	env.ProviderConfig.ClusterMode = pfactory.ClusterModeCompute
	env.ResolvedClusterConfig = []config.KeyValueConfig{{Key: "clusterMode", Value: pfactory.ClusterModeProvided}}
	if err := ensureClusterSpecConfigMap(context.Background(), c, env, "adhar", nil); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}, cm); err != nil {
		t.Fatal(err)
	}
	if got := cm.Data[globals.ClusterSpecClusterModeKey]; got != pfactory.ClusterModeProvided {
		t.Fatalf("adhar-cluster-spec clusterMode = %q; want %q, the mode the environment's clusterConfig gave `adhar up`",
			got, pfactory.ClusterModeProvided)
	}
	if id := cm.Data[globals.ClusterSpecInstallIDKey]; id != "" {
		t.Fatalf("a provided cluster was given install id %q; the platform creates no volumes it owns there", id)
	}
}

// gatewayProvider is a provider that can report the nodes' addresses, and
// counts being asked.
type gatewayProvider struct {
	pfactory.Provider
	asked int
}

func (g *gatewayProvider) GatewayAddresses(context.Context, string) ([]string, error) {
	g.asked++
	return []string{"203.0.113.10"}, nil
}

// The edge addresses are asked for exactly when the cluster `adhar up` built
// needs the host-network Gateway — decided by the mode it was provisioned with,
// clusterConfig included. Read from the block alone, a Civo environment whose
// clusterConfig asks for the MANAGED cluster (which has a load balancer) was
// treated as a compute one and its nodes' addresses published instead.
func TestGatewayAddressesFollowTheEnvironmentsClusterMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		override string
		want     int
	}{
		{"compute from the block", "", 1},
		{"managed from the clusterConfig", pfactory.ClusterModeManaged, 0},
		{"provided from the clusterConfig", pfactory.ClusterModeProvided, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := &config.ResolvedEnvironmentConfig{
				Name: "dev", ResolvedProvider: globals.CloudProviderCivo, ResolvedRegion: "lon1",
				ProviderConfig: &config.ConfigProviderConfig{Type: globals.CloudProviderCivo, Region: "lon1",
					ClusterMode: pfactory.ClusterModeCompute},
			}
			if tc.override != "" {
				env.ResolvedClusterConfig = []config.KeyValueConfig{{Key: "clusterMode", Value: tc.override}}
			}
			p := &gatewayProvider{}
			resolveGatewayAddresses(context.Background(),
				&pfactory.ProvisionResult{Provider: p, Cluster: &types.Cluster{ID: "c1", Name: "adhar"}}, env)
			if p.asked != tc.want {
				t.Fatalf("the provider was asked for the edge addresses %d time(s), want %d", p.asked, tc.want)
			}
		})
	}
}
