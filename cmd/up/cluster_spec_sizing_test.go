package up

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// The adhar-controlplane autoscaler builds its provider from what this ConfigMap
// records, so it must record the worker size the cluster was BUILT with —
// resolved as the provider resolved it — and the environment's disk settings,
// which the CLI's provider map carried and the recorded one did not.
func TestClusterSpecRecordsTheResolvedWorkerSizeAndDisk(t *testing.T) {
	record := func(env *config.ResolvedEnvironmentConfig) (map[string]string, map[string]interface{}) {
		t.Helper()
		scheme := runtime.NewScheme()
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		if err := ensureClusterSpecConfigMap(context.Background(), c, env, "dev", nil); err != nil {
			t.Fatal(err)
		}
		cm := &corev1.ConfigMap{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}, cm); err != nil {
			t.Fatal(err)
		}
		var pm map[string]interface{}
		if err := json.Unmarshal([]byte(cm.Data["providerConfig"]), &pm); err != nil {
			t.Fatal(err)
		}
		return cm.Data, pm
	}

	// GCP: the provider block says e2-standard-4, the environment builds
	// e2-standard-8 workers — the recorded size must be the latter.
	gcp := &config.ResolvedEnvironmentConfig{
		Name: "dev", ResolvedProvider: "gcp", ResolvedRegion: "asia-southeast1",
		ResolvedClusterConfig: []config.KeyValueConfig{{Key: "machineType", Value: "e2-standard-8"}, {Key: "diskSizeGb", Value: "200"}, {Key: "nodeCount", Value: "2"}},
		ProviderConfig: &config.ConfigProviderConfig{Type: "gcp", Region: "asia-southeast1",
			Config: map[string]interface{}{"machine_type": "e2-standard-4"}},
	}
	data, pm := record(gcp)
	if data["size"] != "e2-standard-8" {
		t.Errorf("recorded size %q, want the environment's e2-standard-8", data["size"])
	}
	if pm["diskSizeGb"] != "200" {
		t.Errorf("environment diskSizeGb not recorded: %v", pm)
	}
	if _, leaked := pm["nodeCount"]; leaked {
		t.Error("only node-shape keys belong in the recorded provider map")
	}

	// Azure: the size lives in the provider block's lower-cased `vmsize`, which
	// the old literal lookup (vm_size) never matched.
	azure := &config.ResolvedEnvironmentConfig{
		Name: "dev", ResolvedProvider: "azure", ResolvedRegion: "centralindia",
		ProviderConfig: &config.ConfigProviderConfig{Type: "azure", Region: "centralindia",
			Config: map[string]interface{}{"vmsize": "Standard_E4bds_v5"}},
	}
	if data, _ := record(azure); data["size"] != "Standard_E4bds_v5" {
		t.Errorf("recorded size %q, want the provider block's vmSize", data["size"])
	}

	// Nothing configured: the size the provider will actually build.
	aws := &config.ResolvedEnvironmentConfig{
		Name: "dev", ResolvedProvider: "aws", ResolvedRegion: "ap-southeast-1",
		ProviderConfig: &config.ConfigProviderConfig{Type: "aws", Region: "ap-southeast-1"},
	}
	if data, _ := record(aws); data["size"] != "r6i.xlarge" {
		t.Errorf("recorded size %q, want the built-in r6i.xlarge", data["size"])
	}
}
