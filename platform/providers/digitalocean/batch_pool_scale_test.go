package digitalocean

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/digitalocean/godo"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/types"
)

// A compute cluster with a batch node pool (design 0025 D14) is two node groups
// under ONE worker tag. The autoscaler scales each group to its own target, so
// each must count only its own droplets: counting all of them made "scale batch
// to 2" on three general workers and one pool node retire two GENERAL workers,
// and "scale workers to 4" a permanent no-op.
func TestEachNodeGroupCountsOnlyItsOwnDroplets(t *testing.T) {
	const created = "2026-11-21T06:00:00Z"
	workers := []godo.Droplet{
		droplet(1, "adhar-prod-workers-1", "10.3.0.3", created),
		droplet(2, "adhar-prod-workers-2", "10.3.0.4", created),
		droplet(3, "adhar-prod-workers-3", "10.3.0.5", created),
		droplet(4, "adhar-prod-batch-1", "10.3.0.6", created),
		droplet(5, "adhar-prod-worker-1", "10.3.0.7", created), // legacy name, no group
	}
	if got, want := names(nodeGroupDroplets(workers, globals.BatchNodeGroupName)), []string{"adhar-prod-batch-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("batch group = %v, want %v", got, want)
	}
	if got, want := names(nodeGroupDroplets(workers, "workers")),
		[]string{"adhar-prod-workers-1", "adhar-prod-workers-2", "adhar-prod-workers-3", "adhar-prod-worker-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("general group = %v, want %v (every non-pool worker, legacy names included)", got, want)
	}
	// Without a pool every worker is the general group's, exactly as before.
	if got := nodeGroupDroplets(workers[:3], "workers"); len(got) != 3 {
		t.Errorf("no pool: %v", names(got))
	}

	// The scale path is what the autoscaler drives; it must count through it.
	src, err := os.ReadFile("compute.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (p *Provider) scaleComputeWorkers(")
	if start < 0 {
		t.Fatal("scaleComputeWorkers not found")
	}
	end := strings.Index(body[start:], "\n}\n")
	if !strings.Contains(body[start:start+end], "nodeGroupDroplets(workers, nodeGroup)") {
		t.Error("scaleComputeWorkers counts every worker droplet as the group's; narrow them with nodeGroupDroplets")
	}
}

// A DOKS node pool built for the batch pool carries the pool's taint as well as
// its label: with the label alone every general pod could land on it.
func TestADOKSPoolCarriesItsGroupsTaints(t *testing.T) {
	g, _ := (&config.BatchPoolConfig{MinNodes: 1, MaxNodes: 3}).BatchPoolNodeGroup()
	ng := types.NodeGroupSpec{Name: g.Name, Replicas: g.Count, Labels: g.Labels}
	for _, tt := range g.Taints {
		ng.Taints = append(ng.Taints, types.TaintSpec{Key: tt.Key, Value: tt.Value, Effect: tt.Effect})
	}
	pools := (&Provider{config: &Config{}}).buildDOKSNodePools(&types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{{Name: "workers", Replicas: 2}, ng}})
	if len(pools) != 2 {
		t.Fatalf("pools = %d", len(pools))
	}
	if len(pools[0].Taints) != 0 {
		t.Errorf("general pool tainted: %+v", pools[0].Taints)
	}
	want := []godo.Taint{{Key: globals.BatchNodePoolTaintKey, Value: globals.BatchNodePoolTaintValue, Effect: "NoSchedule"}}
	if p := pools[1]; p.Name != globals.BatchNodeGroupName || !reflect.DeepEqual(p.Taints, want) ||
		p.Labels[globals.BatchNodePoolLabel] != globals.BatchNodePoolValue {
		t.Errorf("batch pool = %+v, want label %s=%s and taints %+v", p, globals.BatchNodePoolLabel, globals.BatchNodePoolValue, want)
	}
}
