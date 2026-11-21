package provider

import (
	"testing"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// An EMPTY batch pool takes the pool's configured type; a pool with members keeps
// theirs, and no other group is touched. (The per-provider scale paths are pinned
// in aws/, digitalocean/ and civo/ batch_pool_size_test.go.)
func TestAnEmptyBatchPoolTakesThePoolsMachineType(t *testing.T) {
	for _, c := range []struct {
		group, members, batch, want string
	}{
		{globals.BatchNodeGroupName, "", "c6i.4xlarge", "c6i.4xlarge"},
		{globals.BatchNodeGroupName, "m6i.xlarge", "c6i.4xlarge", ""}, // members win: a pool stays homogeneous
		{globals.BatchNodeGroupName, "", "", ""},                      // no pool type: the worker default
		{"workers", "", "c6i.4xlarge", ""},                            // never the general workers
		{"gpu", "", "c6i.4xlarge", ""},
	} {
		if got := EmptyBatchGroupType(c.group, c.members, c.batch); got != c.want {
			t.Errorf("EmptyBatchGroupType(%q, %q, %q) = %q, want %q", c.group, c.members, c.batch, got, c.want)
		}
	}
}

// Every provider `adhar up` builds for an environment carries the pool's type, at
// the top level where the providers' ConfigString reads it first.
func TestTheEnvironmentLayerCarriesTheBatchPoolsMachineType(t *testing.T) {
	env := &config.ResolvedEnvironmentConfig{Name: "prod", ResolvedProvider: "aws", ResolvedRegion: "ap-southeast-1",
		BatchPool: &config.BatchPoolConfig{MachineType: " c6i.4xlarge ", MinNodes: 1, MaxNodes: 3}}
	if got := ConfigString(EnvironmentProviderOverrides(env), KeyBatchMachineType); got != "c6i.4xlarge" {
		t.Errorf("the environment layer carries batchMachineType %q", got)
	}
	if got := ConfigString(buildProviderConfig(env), KeyBatchMachineType); got != "c6i.4xlarge" {
		t.Errorf("the provider map carries batchMachineType %q", got)
	}
	env.BatchPool.MachineType = ""
	if _, set := EnvironmentProviderOverrides(env)[KeyBatchMachineType]; set {
		t.Error("a pool that names no type still sets batchMachineType")
	}
	env.BatchPool = nil
	if _, set := EnvironmentProviderOverrides(env)[KeyBatchMachineType]; set {
		t.Error("no pool, yet batchMachineType is set")
	}
}
