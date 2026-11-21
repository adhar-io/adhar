package aws

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
		{"region": "ap-southeast-1", provider.KeyWorkerMachineType: "m6i.xlarge", provider.KeyBatchMachineType: "c6i.4xlarge"},
		{"region": "ap-southeast-1", "config": map[string]interface{}{provider.KeyWorkerMachineType: "m6i.xlarge", provider.KeyBatchMachineType: "c6i.4xlarge"}},
	} {
		cfg, err := parseProviderConfig(where)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ group, members, want string }{
			{globals.BatchNodeGroupName, "", "c6i.4xlarge"},
			{globals.BatchNodeGroupName, "r6i.2xlarge", "r6i.2xlarge"},
			{"workers", "", "m6i.xlarge"},
			{"workers", "r6i.2xlarge", "r6i.2xlarge"},
		} {
			if got := cfg.scaleWorkerType(c.group, c.members); got != c.want {
				t.Errorf("%v: group %s with members %q scales at %q, want %q", where, c.group, c.members, got, c.want)
			}
		}
	}
	// A pool that names no type: the worker size, as before.
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "ap-southeast-1", provider.KeyWorkerMachineType: "m6i.xlarge"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.scaleWorkerType(globals.BatchNodeGroupName, ""); got != "m6i.xlarge" {
		t.Errorf("an empty pool with no type of its own scales at %q, want the worker size", got)
	}

	// The scale path the node autoscaler drives sizes new nodes through it.
	src, err := os.ReadFile("provider_compute.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (p *Provider) ScaleNodeGroup(")
	if start < 0 {
		t.Fatal("the scale path was not found")
	}
	end := strings.Index(body[start:], "\n}\n")
	if !strings.Contains(body[start:start+end], "p.config.scaleWorkerType(nodeGroupName, membersInstanceType(members))") {
		t.Error("the scale path does not size new nodes with scaleWorkerType, so an empty batch pool takes the worker size")
	}
}
