package azure

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
	provider "adhar-io/adhar/platform/providers"
)

// A VM the scale path adds to an EMPTY batch pool (design 0025 D14) takes the
// pool's configured size (provider.KeyBatchMachineType), not the provider-wide
// worker size; a pool with members keeps their size; the general workers never
// take the pool's size.
func TestAnEmptyBatchPoolScalesAtThePoolsMachineType(t *testing.T) {
	for _, where := range []map[string]interface{}{
		{"region": "centralindia", provider.KeyWorkerMachineType: "Standard_E4bds_v5", provider.KeyBatchMachineType: "Standard_F16s_v2"},
		{"region": "centralindia", "config": map[string]interface{}{"workermachinetype": "Standard_E4bds_v5", "batchmachinetype": "Standard_F16s_v2"}},
	} {
		cfg, err := parseProviderConfig(where)
		if err != nil {
			t.Fatal(err)
		}
		p := &Provider{config: cfg}
		for _, c := range []struct {
			group   string
			vms     []string
			members string
			want    string
		}{
			{globals.BatchNodeGroupName, nil, "", "Standard_F16s_v2"},
			{globals.BatchNodeGroupName, []string{"adhar-worker-batch-1"}, "Standard_E8bds_v5", "Standard_E8bds_v5"},
			{"workers", nil, "", "Standard_E4bds_v5"},
			{"workers", []string{"adhar-worker-workers-1"}, "Standard_E8bds_v5", "Standard_E8bds_v5"},
		} {
			prefix := "adhar-worker-" + c.group + "-"
			got := pickWorkerVMSize(c.vms, prefix, p.config.emptyGroupSize(c.group), func(string) string { return c.members })
			if got != c.want {
				t.Errorf("%v: group %s with members %q scales at %q, want %q", where, c.group, c.members, got, c.want)
			}
		}
	}
	// A pool that names no size: the worker size, as before.
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "centralindia", provider.KeyWorkerMachineType: "Standard_E4bds_v5"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.emptyGroupSize(globals.BatchNodeGroupName); got != "Standard_E4bds_v5" {
		t.Errorf("an empty pool with no size of its own scales at %q, want the worker size", got)
	}

	// The scale path the node autoscaler drives sizes new VMs through it.
	src, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for fn, call := range map[string]string{
		"func (p *Provider) ScaleNodeGroup(": "p.workerVMSize(ctx, tracker, prefix, nodeGroupName)",
		"func (p *Provider) workerVMSize(":   "p.config.emptyGroupSize(nodeGroup)",
	} {
		start := strings.Index(body, fn)
		if start < 0 {
			t.Fatalf("%s not found", fn)
		}
		end := strings.Index(body[start:], "\n}\n")
		if !strings.Contains(body[start:start+end], call) {
			t.Errorf("%s does not call %s, so an empty batch pool takes the worker size", fn, call)
		}
	}
}
