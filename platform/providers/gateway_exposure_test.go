package provider

import "testing"

// Which clusters need the Gateway on the host network. Getting this wrong is
// expensive in BOTH directions: a cluster that needs it and does not get it has
// no reachable URL at all, and a cluster that gets it when it should not loses
// the local host-port mapping (kind) or has its own Cilium reconfigured by a
// tool that was only asked to install onto it (provided).
func TestGatewayHostNetworkIsOnlyForClustersWithNoLoadBalancer(t *testing.T) {
	for _, tc := range []struct {
		providerType string
		clusterMode  string
		want         bool
		why          string
	}{
		{"civo", ClusterModeCompute, true, "Civo's CCM only works against its managed Kubernetes, so compute mode has no load balancer"},
		{"civo", "", true, "empty means compute, the default"},
		{"civo", ClusterModeManaged, false, "Civo's managed k3s runs the CCM, so the Gateway Service gets a real address"},
		{"civo", ClusterModeProvided, false, "the cluster is the operator's; its Cilium is theirs to configure"},
		{"custom", ClusterModeCompute, true, "bring-your-own hosts have no cloud API and no load balancer"},
		{"kind", ClusterModeCompute, false, "local runs on NodePort 30080/30443, which the host port-mapping depends on"},
		{"aws", ClusterModeCompute, false, "the AWS CCM gives compute mode a real ELB"},
		{"azure", ClusterModeCompute, false, "cloud-provider-azure gives compute mode a real LB"},
		{"gcp", ClusterModeCompute, false, "cloud-provider-gcp gives compute mode a real LB"},
		{"digitalocean", ClusterModeCompute, false, "the DO CCM gives compute mode a real LB"},
		{"", "", false, "an unknown provider must not have its data path changed"},
	} {
		t.Run(tc.providerType+"/"+tc.clusterMode, func(t *testing.T) {
			if got := GatewayHostNetworkRequired(tc.providerType, tc.clusterMode); got != tc.want {
				t.Errorf("GatewayHostNetworkRequired(%q, %q) = %v, want %v — %s",
					tc.providerType, tc.clusterMode, got, tc.want, tc.why)
			}
		})
	}
}

// The provider name reaches this from several places (config `type:`, the
// cluster-spec ConfigMap, a CLI flag) and the aliases have to fold, or a
// cluster recorded as `do` silently gets a different answer from one recorded
// as `digitalocean`.
func TestGatewayHostNetworkFoldsProviderAliases(t *testing.T) {
	for _, alias := range []string{"CIVO", " civo ", "Civo"} {
		if !GatewayHostNetworkRequired(alias, ClusterModeCompute) {
			t.Errorf("provider %q was not recognised as civo", alias)
		}
	}
	for _, alias := range []string{"byo", "on-premises", "onprem", "CUSTOM"} {
		if !GatewayHostNetworkRequired(alias, ClusterModeCompute) {
			t.Errorf("provider %q was not recognised as the custom provider", alias)
		}
	}
	for _, alias := range []string{"do", "digital-ocean", "eks", "aks", "gke", "google"} {
		if GatewayHostNetworkRequired(alias, ClusterModeCompute) {
			t.Errorf("provider %q was given the host-network Gateway; that cloud has a CCM", alias)
		}
	}
}
