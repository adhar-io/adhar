package provider

import (
	"os"
	"path/filepath"
	"testing"

	"adhar-io/adhar/platform/config"
)

// repoRootFromProviders walks up to the module root so the test can read the
// shipped example configs wherever it is run from.
func repoRootFromProviders(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// The cluster mode written in a config file must REACH the provider.
//
// It did not. `clusterMode` is a provider-level key in every shipped example and
// config.ConfigProviderConfig had no field for it, so mapstructure dropped it and
// ToProviderMap never carried it — which means ParseClusterMode saw nothing and
// returned the default. A file that plainly said `clusterMode: managed` therefore
// built kubeadm on raw compute, and the only symptom was a cluster of the wrong
// kind: no cloud-controller-manager, no load-balancer address for the Gateway, no
// A record, no answering URL.
//
// This loads the real example files through the real loader, which is the only
// way to catch a key that is lost in the decode rather than in the parse.
func TestClusterModeInAConfigFileReachesTheProvider(t *testing.T) {
	root := repoRootFromProviders(t)

	for _, tc := range []struct {
		file     string
		provider string
		want     string
	}{
		// compute everywhere except Civo, whose cloud-controller-manager only
		// works against Civo's own managed Kubernetes — so compute mode there has
		// no load balancer for the Gateway (see GatewayHostNetworkRequired).
		{"examples/aws-config.yaml", "aws", ClusterModeCompute},
		{"examples/azure-config.yaml", "azure", ClusterModeCompute},
		{"examples/gcp-config.yaml", "gcp", ClusterModeCompute},
		{"examples/digitalocean-config.yaml", "digitalocean", ClusterModeCompute},
		{"examples/civo-config.yaml", "civo", ClusterModeManaged},
		{"examples/provided-config.yaml", "custom", ClusterModeProvided},
	} {
		t.Run(tc.file, func(t *testing.T) {
			cfg, err := config.LoadConfig(filepath.Join(root, tc.file))
			if err != nil {
				t.Fatalf("loading %s: %v", tc.file, err)
			}
			block, ok := cfg.Providers[tc.provider]
			if !ok {
				t.Fatalf("%s has no %q provider block", tc.file, tc.provider)
			}
			got, err := ParseClusterMode(block.ToProviderMap())
			if err != nil {
				t.Fatalf("%s: ParseClusterMode: %v", tc.file, err)
			}
			if got != tc.want {
				t.Errorf("%s declares clusterMode %q but the provider map resolves to %q — "+
					"the key is being dropped between the file and the provider",
					tc.file, tc.want, got)
			}
		})
	}
}

// The provided-mode example must also carry WHERE the existing cluster is, or
// the mode has nothing to install onto.
func TestProvidedExampleNamesTheExistingCluster(t *testing.T) {
	root := repoRootFromProviders(t)
	cfg, err := config.LoadConfig(filepath.Join(root, "examples/provided-config.yaml"))
	if err != nil {
		t.Fatalf("loading the provided example: %v", err)
	}
	block := cfg.Providers["custom"]
	providerMap := block.ToProviderMap()

	if got := providedConfigString(providerMap, "kubeconfig"); got == "" {
		t.Error("examples/provided-config.yaml does not carry `kubeconfig` into the provider map")
	}
	if got := providedConfigString(providerMap, "kubeContext"); got == "" {
		t.Error("examples/provided-config.yaml does not carry `kubeContext` into the provider map")
	}
}
