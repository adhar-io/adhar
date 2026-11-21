package autoscaler

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
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

// The batch pool's machine type, recorded in adhar-cluster-spec's providerConfig,
// reaches the provider a scaling action builds — through loadClusterSpec, as the
// controller reads it — so a node added to an EMPTY pool takes the pool's type.
func TestScalingProviderMapCarriesTheBatchPoolsMachineType(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace},
		Data: map[string]string{"provider": "aws", "clusterName": "prod", "size": "m6i.xlarge",
			"providerConfig": `{"region":"ap-southeast-1","batchMachineType":"c6i.4xlarge"}`},
	}
	c := fake.NewClientBuilder().WithObjects(cm).Build()
	spec, err := loadClusterSpec(context.Background(), c, globals.AdharSystemNamespace)
	if err != nil {
		t.Fatal(err)
	}
	cfg := providerMapFor(spec)
	if got := pfactory.ConfigString(cfg, pfactory.KeyBatchMachineType); got != "c6i.4xlarge" {
		t.Errorf("batchMachineType = %q, want the recorded c6i.4xlarge", got)
	}
	if got := pfactory.ConfigString(cfg, pfactory.KeyWorkerMachineType); got != "m6i.xlarge" {
		t.Errorf("the pool's type displaced the worker size: %q", got)
	}
}
