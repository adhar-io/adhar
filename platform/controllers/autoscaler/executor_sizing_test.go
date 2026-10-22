package autoscaler

import (
	"testing"

	pfactory "adhar-io/adhar/platform/providers"
)

// A worker the autoscaler adds to an EMPTY group must take the size the cluster
// was built with on every cloud, not only on DigitalOcean (droplet_size): the
// size reaches each provider as its provider-level workerMachineType.
func TestScalingProviderMapCarriesTheRecordedWorkerSize(t *testing.T) {
	spec := &clusterSpec{Provider: "gcp", Region: "asia-southeast1", Size: "e2-standard-8",
		ProviderConfig: map[string]interface{}{"config": map[string]interface{}{"machine_type": "e2-standard-4"}}}
	cfg := providerMapFor(spec)
	if cfg[pfactory.KeyWorkerMachineType] != "e2-standard-8" {
		t.Errorf("workerMachineType = %v, want the recorded e2-standard-8", cfg[pfactory.KeyWorkerMachineType])
	}
	if cfg["region"] != "asia-southeast1" {
		t.Errorf("region = %v", cfg["region"])
	}
	// An explicit provider-level worker size recorded at bootstrap is kept.
	spec.ProviderConfig[pfactory.KeyWorkerMachineType] = "n2-standard-8"
	if cfg := providerMapFor(spec); cfg[pfactory.KeyWorkerMachineType] != "n2-standard-8" {
		t.Errorf("the configured workerMachineType was overwritten: %v", cfg[pfactory.KeyWorkerMachineType])
	}
}
