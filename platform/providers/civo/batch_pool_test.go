package civo

import (
	"testing"

	"github.com/civo/civogo"
	corev1 "k8s.io/api/core/v1"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/types"
)

// A node added to one group takes the size that group's members run: with a
// batch node pool (design 0025 D14) beside the general workers, reading the
// first worker by name across both put "adhar-c-batch-1"'s size on a new general
// worker.
func TestAScaledWorkerTakesItsOwnGroupsSize(t *testing.T) {
	byName := map[string]civogo.Instance{
		"adhar-c-batch-1":   {Hostname: "adhar-c-batch-1", Size: "g3.xlarge"},
		"adhar-c-workers-1": {Hostname: "adhar-c-workers-1", Size: "g3.large"},
		"adhar-c-workers-2": {Hostname: "adhar-c-workers-2", Size: "g3.large"},
	}
	if got := civoGroupMembersSize("adhar-c-workers-", byName); got != "g3.large" {
		t.Errorf("general worker size = %q, want g3.large", got)
	}
	if got := civoGroupMembersSize("adhar-c-batch-", byName); got != "g3.xlarge" {
		t.Errorf("pool node size = %q, want g3.xlarge", got)
	}
	if got := civoGroupMembersSize("adhar-c-gpu-", byName); got != "" {
		t.Errorf("an empty group borrowed another group's size: %q", got)
	}
}

// A managed Civo pool built for the batch pool carries the pool's taint as well
// as its label: with the label alone every general pod could land on it.
func TestACivoPoolCarriesItsGroupsTaints(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "LON1"})
	if err != nil {
		t.Fatal(err)
	}
	pools := (&Provider{config: cfg}).buildCivoPools(&types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{
		{Name: "workers", Replicas: 2},
		{Name: globals.BatchNodeGroupName, Replicas: 1,
			Labels: map[string]string{globals.BatchNodePoolLabel: globals.BatchNodePoolValue},
			Taints: []types.TaintSpec{{Key: globals.BatchNodePoolTaintKey, Value: globals.BatchNodePoolTaintValue, Effect: "NoSchedule"}}},
	}})
	if len(pools) != 2 || len(pools[0].Taints) != 0 {
		t.Fatalf("pools = %+v", pools)
	}
	got := pools[1].Taints
	if len(got) != 1 || got[0].Key != globals.BatchNodePoolTaintKey || got[0].Value != globals.BatchNodePoolTaintValue ||
		got[0].Effect != corev1.TaintEffectNoSchedule || pools[1].Labels[globals.BatchNodePoolLabel] != globals.BatchNodePoolValue {
		t.Errorf("batch pool = %+v", pools[1])
	}
}
