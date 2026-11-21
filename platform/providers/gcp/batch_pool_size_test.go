package gcp

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
	provider "adhar-io/adhar/platform/providers"
)

// A node the scale path adds to an EMPTY batch pool (design 0025 D14) takes the
// pool's configured machine type (provider.KeyBatchMachineType), not the
// provider-wide worker size; a pool with members keeps their type; the general
// workers never take the pool's type; a type asked for explicitly (a new node
// group's own) still wins.
func TestAnEmptyBatchPoolScalesAtThePoolsMachineType(t *testing.T) {
	for _, where := range []map[string]interface{}{
		{"region": "asia-southeast1", provider.KeyWorkerMachineType: "e2-standard-8", provider.KeyBatchMachineType: "c3-standard-22"},
		{"region": "asia-southeast1", "config": map[string]interface{}{provider.KeyWorkerMachineType: "e2-standard-8", provider.KeyBatchMachineType: "c3-standard-22"}},
	} {
		cfg, err := parseProviderConfig(where)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ group, asked, members, want string }{
			{globals.BatchNodeGroupName, "", "", "c3-standard-22"},
			{globals.BatchNodeGroupName, "", "n2-standard-16", "n2-standard-16"},
			{globals.BatchNodeGroupName, "n2-highmem-8", "", "n2-highmem-8"},
			{"workers", "", "", "e2-standard-8"},
			{"workers", "", "n2-standard-16", "n2-standard-16"},
		} {
			if got := cfg.scaleWorkerSize(c.group, c.asked, c.members); got != c.want {
				t.Errorf("%v: group %s asked %q with members %q scales at %q, want %q", where, c.group, c.asked, c.members, got, c.want)
			}
		}
	}
	// A pool that names no type: the worker size, as before.
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "asia-southeast1", provider.KeyWorkerMachineType: "e2-standard-8"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.scaleWorkerSize(globals.BatchNodeGroupName, "", ""); got != "e2-standard-8" {
		t.Errorf("an empty pool with no type of its own scales at %q, want the worker size", got)
	}

	// The scale path the node autoscaler drives sizes new nodes through it.
	src, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (p *Provider) scaleWorkers(")
	if start < 0 {
		t.Fatal("scaleWorkers not found")
	}
	end := strings.Index(body[start:], "\n}\n")
	if !strings.Contains(body[start:start+end], "p.config.scaleWorkerSize(nodeGroupName, machineType, workersMachineType(infra.WorkerNodes, prefix))") {
		t.Error("scaleWorkers does not size new nodes with scaleWorkerSize, so an empty batch pool takes the worker size")
	}
}
