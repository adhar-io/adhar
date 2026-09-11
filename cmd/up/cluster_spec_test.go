package up

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

const (
	testRegion   = "blr1"
	testNodeSize = "s-8vcpu-16gb"
)

func TestAutoscalingSpecFromConfig(t *testing.T) {
	if spec, err := autoscalingSpecFromConfig(nil); err != nil || spec != nil {
		t.Fatalf("no config must produce no spec, got %v / %v", spec, err)
	}

	spec, err := autoscalingSpecFromConfig(&config.AutoscalingConfig{
		Enabled:         true,
		MinWorkers:      3,
		MaxWorkers:      10,
		ScaleDownDelay:  "15m",
		ScaleUpCooldown: "2m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Enabled || spec.MinWorkers != 3 || spec.MaxWorkers != 10 {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if spec.ScaleDownDelay.Duration != 15*time.Minute || spec.ScaleUpCooldown.Duration != 2*time.Minute {
		t.Fatalf("durations not parsed: %+v", spec)
	}

	// A malformed duration is reported: silently ignoring it would change
	// scaling behaviour invisibly.
	bad := &config.AutoscalingConfig{Enabled: true, ScaleDownDelay: "ten minutes"}
	if _, err := autoscalingSpecFromConfig(bad); err == nil {
		t.Fatal("expected an error for a malformed duration")
	}
}

func TestEnsureClusterSpecConfigMapExcludesCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	env := &config.ResolvedEnvironmentConfig{
		Name:             "dev",
		ResolvedProvider: dnsDigitalOcean,
		ResolvedRegion:   testRegion,
		ResolvedClusterConfig: []config.KeyValueConfig{
			{Key: "nodeSize", Value: testNodeSize},
			{Key: "nodeCount", Value: "3"},
		},
		ProviderConfig: &config.ConfigProviderConfig{
			Type:   dnsDigitalOcean,
			Region: testRegion,
			Token:  "dop_v1_supersecret",
			Config: map[string]interface{}{"droplet_size": testNodeSize},
		},
		Autoscaling: &config.AutoscalingConfig{Enabled: true, MinWorkers: 3, MaxWorkers: 10},
	}
	if err := ensureClusterSpecConfigMap(context.Background(), c, env, "dev"); err != nil {
		t.Fatal(err)
	}

	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}
	if err := c.Get(context.Background(), key, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[keyProvider] != dnsDigitalOcean || cm.Data["region"] != testRegion || cm.Data["clusterName"] != "dev" {
		t.Fatalf("unexpected data: %v", cm.Data)
	}
	if cm.Data["size"] != testNodeSize || cm.Data["nodeGroup"] != "workers" {
		t.Fatalf("unexpected node data: %v", cm.Data)
	}

	var provider map[string]interface{}
	if err := json.Unmarshal([]byte(cm.Data["providerConfig"]), &provider); err != nil {
		t.Fatal(err)
	}
	if _, leaked := provider[keyToken]; leaked {
		t.Fatal("the API token must never be written to a ConfigMap")
	}
	if provider["region"] != testRegion {
		t.Fatalf("provider config lost its region: %v", provider)
	}
}
