package adharplatform

import (
	"testing"

	"adhar-io/adhar/platform/config"
)

// A scheduler descriptor that names no prod share (one written before the seed
// rendered prodDeservedCPU/Memory, or edited by hand) must not leave prod with a
// share smaller than the largest gang the design's examples use: that gang could
// then never reclaim room from dev and test (design 0025 D17, user decision DEC-3).
// The Team composition's fallback is the same floor platform/config renders, and
// the leaf's capability still clamps it (TestTeamQueueCapabilityIsTheSmallerOfAllowanceAndCeiling
// covers the clamp).
func TestTeamProdShareFallbackIsTheGangFloor(t *testing.T) {
	desc := tqCluster(t, nil)[0]
	if desc["kind"] != "ConfigMap" || dig(desc, "metadata", "name") != "adhar-batch-scheduler" {
		t.Fatalf("required-resources.yaml no longer starts with the scheduler descriptor: %v", desc["metadata"])
	}
	data := map[string]any{}
	for k, v := range desc["data"].(map[string]any) {
		if k != "prodDeservedCPU" && k != "prodDeservedMemory" {
			data[k] = v
		}
	}
	bare := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": desc["metadata"], "data": data}
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, []string{"adhar-batch-scheduler"}, bare), nil)
	prod := tqQueues(docs)["acme-payments-prod"]
	if prod == nil {
		t.Fatalf("no prod leaf composed without the descriptor's prod share: %v", tqCondition(t, docs))
	}
	want := tqJSON(map[string]any{"cpu": config.BatchGangFloorCPU, "memory": config.BatchGangFloorMemory})
	if got := tqJSON(dig(prod, "spec", "deserved")); got != want {
		t.Errorf("a descriptor naming no prod share gives the prod leaf deserved %s; want D17's floor %s (platform/config BatchGangFloorCPU/Memory)", got, want)
	}
}
