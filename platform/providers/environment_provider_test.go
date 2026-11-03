package provider

import (
	"reflect"
	"testing"

	"adhar-io/adhar/platform/config"
)

// EnvironmentProviderOverrides is the layer `adhar up` puts over the provider
// block (buildProviderConfig), and the only way another command builds an
// environment's provider the same way. If the two drift, `adhar down` looks for
// a cluster in a region or mode `adhar up` never created it in.
func TestEnvironmentProviderOverridesAreWhatAdharUpLayersOverTheBlock(t *testing.T) {
	block := &config.ConfigProviderConfig{Type: "digitalocean", Region: "blr1", ClusterMode: "compute"}
	env := &config.ResolvedEnvironmentConfig{
		ProviderConfig: block,
		ResolvedRegion: "sgp1",
		ResolvedClusterConfig: []config.KeyValueConfig{
			{Key: "clusterMode", Value: "managed"},
			{Key: "nodeCount", Value: "3"},
		},
	}

	over := EnvironmentProviderOverrides(env)
	want := map[string]interface{}{"region": "sgp1", "clusterMode": "managed", "nodeCount": "3"}
	if !reflect.DeepEqual(over, want) {
		t.Fatalf("EnvironmentProviderOverrides = %v; want %v", over, want)
	}

	// The block plus that layer IS what `adhar up` builds its provider from.
	layered := block.ToProviderMap()
	for k, v := range over {
		layered[k] = v
	}
	if got := buildProviderConfig(env); !reflect.DeepEqual(got, layered) {
		t.Fatalf("buildProviderConfig = %v\nblock + EnvironmentProviderOverrides = %v; they must be the same map", got, layered)
	}
	if mode, err := ParseClusterMode(layered); err != nil || mode != ClusterModeManaged {
		t.Fatalf("the layered map parses as %q, %v; want the environment's managed mode", mode, err)
	}

	// No environment region: the block's region stands.
	if _, has := EnvironmentProviderOverrides(&config.ResolvedEnvironmentConfig{ProviderConfig: block})["region"]; has {
		t.Error("an empty environment region overrode the block's")
	}
	if len(EnvironmentProviderOverrides(nil)) != 0 {
		t.Error("a nil environment produced overrides")
	}
}
