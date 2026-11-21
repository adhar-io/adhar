package civo

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
	provider "adhar-io/adhar/platform/providers"
)

// A node the scale path adds to an EMPTY batch pool (design 0025 D14) takes the
// pool's configured machine type (environments[].batchPool.machineType, recorded as
// provider.KeyBatchMachineType), not the provider-wide worker size; a pool with
// members keeps their size; the general workers never take the pool's type.
func TestAnEmptyBatchPoolScalesAtThePoolsMachineType(t *testing.T) {
	for _, where := range []map[string]interface{}{
		{"region": "LON1", provider.KeyWorkerMachineType: "g3.large", provider.KeyBatchMachineType: "g3.2xlarge"},
		{"region": "LON1", "config": map[string]interface{}{provider.KeyWorkerMachineType: "g3.large", provider.KeyBatchMachineType: "g3.2xlarge"}},
	} {
		cfg, err := parseProviderConfig(where)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ group, members, want string }{
			{globals.BatchNodeGroupName, "", "g3.2xlarge"},
			{globals.BatchNodeGroupName, "g3.xlarge", "g3.xlarge"},
			{"workers", "", "g3.large"},
			{"workers", "g3.xlarge", "g3.xlarge"},
		} {
			if got := cfg.scaleWorkerSize(c.group, c.members); got != c.want {
				t.Errorf("%v: group %s with members %q scales at %q, want %q", where, c.group, c.members, got, c.want)
			}
		}
	}
	// A pool that names no type: the worker size, as before.
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "LON1", provider.KeyWorkerMachineType: "g3.large"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.scaleWorkerSize(globals.BatchNodeGroupName, ""); got != "g3.large" {
		t.Errorf("an empty pool with no type of its own scales at %q, want the worker size", got)
	}

	// The scale path the node autoscaler drives sizes new nodes through it.
	src, err := os.ReadFile("compute.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (p *Provider) scaleComputeWorkers(")
	if start < 0 {
		t.Fatal("the scale path was not found")
	}
	end := strings.Index(body[start:], "\n}\n")
	if !strings.Contains(body[start:start+end], "p.config.scaleWorkerSize(nodeGroupName, civoGroupMembersSize(prefix, byName))") {
		t.Error("the scale path does not size new nodes with scaleWorkerSize, so an empty batch pool takes the worker size")
	}
}
