package provider

import (
	"os"
	"strings"
	"testing"
)

// The provisioning path has to ROUTE provided mode away from the cloud provider,
// and it has to refuse --recreate.
//
// Checked against the source: ProvisionEnvironment authenticates, preflights and
// creates against a real cloud account, so exercising it end to end needs
// credentials and spends money. What can be pinned without that is the shape of
// the decision — which is where the danger is. `--recreate` DELETES the cluster
// before rebuilding it, and on a provided cluster that is the single action this
// mode exists to prevent.
func TestProvisionEnvironmentRoutesProvidedModeAwayFromTheCloud(t *testing.T) {
	src, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatalf("reading provider.go: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "func (pm *ProviderManager) ProvisionEnvironment(")
	if start < 0 {
		t.Fatal("ProvisionEnvironment is gone; this test's assumptions need revisiting")
	}
	fn := body[start:]
	if end := strings.Index(fn, "\n// buildProviderConfig"); end > 0 {
		fn = fn[:end]
	}

	parse := strings.Index(fn, "ParseClusterMode(providerConfig)")
	provided := strings.Index(fn, "NewProvidedProvider(")
	cloud := strings.Index(fn, "pm.factory.CreateProvider(")
	recreate := strings.Index(fn, "if opts.Recreate {")
	guard := strings.Index(fn, "ClusterLifecycleIsOurs(clusterMode)")
	recreateCall := strings.Index(fn, "recreateCluster(ctx, prov, clusterName)")

	switch {
	case parse < 0:
		t.Error("ProvisionEnvironment does not parse the cluster mode, so `clusterMode: provided` cannot take effect")
	case provided < 0:
		t.Error("ProvisionEnvironment never builds a ProvidedProvider; provided mode would create a cloud cluster")
	case cloud < 0:
		t.Error("ProvisionEnvironment no longer builds a cloud provider at all")
	case parse > provided || parse > cloud:
		t.Error("the mode is parsed AFTER the provider is built; the decision has to come first")
	}

	switch {
	case guard < 0:
		t.Error("the --recreate path is not gated on ClusterLifecycleIsOurs: --recreate deletes the cluster first, " +
			"and on a provided cluster that destroys infrastructure the platform does not own")
	case recreate < 0 || recreateCall < 0:
		t.Error("the --recreate path has changed shape; this test's assumptions need revisiting")
	case guard > recreateCall:
		t.Error("the lifecycle guard runs after recreateCluster; by then the cluster is already being deleted")
	}
}
