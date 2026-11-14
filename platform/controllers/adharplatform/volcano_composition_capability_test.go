package adharplatform

import (
	"fmt"
	"strings"
	"testing"
)

// A run larger than its leaf queue's capability is never admitted: Volcano's
// capacity plugin compares a PodGroup's minimum resources with the capability at
// enqueue, so the PodGroup stays Pending for good. BatchGangQueueRejected's
// description sends the reader to QueueFits to learn which, so QueueFits must say
// it, on every tier (dev and test have a capability but no deserved share), and
// before the deserved-share warning, which would otherwise describe a run that
// "starts on idle room" when it never starts. It stays a warning (DEC-3).
func TestQueueFitsWarnsWhenARunExceedsTheLeafsCapability(t *testing.T) {
	// The dev leaf's capability is 8 CPU / 16Gi; dev-quota allows 8Gi of memory.
	xr := vcXR(t, "xr-partitioned.yaml")
	for _, c := range []struct {
		name    string
		workers int
		res     map[string]any
		want    string
		says    []string
	}{
		{"cpu over the capability", 16, map[string]any{"cpu": "500m", "memory": "256Mi", "workerCpu": "1", "workerMemory": "256Mi"},
			"ExceedsCapability", []string{"acme-payments-dev", "cpu 16500m > capability 8"}},
		{"exactly the capability", 14, map[string]any{"cpu": "1", "memory": "256Mi", "workerCpu": "500m", "workerMemory": "256Mi"},
			"Fits", nil}, // 1000m + 14 × 500m = 8000m
		{"a milli over the capability", 14, map[string]any{"cpu": "1001m", "memory": "256Mi", "workerCpu": "500m", "workerMemory": "256Mi"},
			"ExceedsCapability", []string{"cpu 8001m > capability 8"}},
		{"within the capability", 4, map[string]any{"cpu": "500m", "memory": "1Gi", "workerCpu": "1", "workerMemory": "1536Mi"},
			"Fits", nil},
	} {
		docs := vcMustRender(t, bjWithParams(xr, map[string]any{"workers": c.workers, "resources": c.res}), vcCluster(t), nil)
		got := bjCondition(t, docs, "QueueFits")
		if got["reason"] != c.want {
			t.Errorf("%s: QueueFits = %v, want %s", c.name, got, c.want)
			continue
		}
		for _, s := range c.says {
			if !strings.Contains(fmt.Sprint(got["message"]), s) {
				t.Errorf("%s: the warning does not say %q: %v", c.name, s, got["message"])
			}
		}
		if c.want == "ExceedsCapability" {
			if got["status"] != "False" {
				t.Errorf("%s: QueueFits status = %v, want False", c.name, got["status"])
			}
			// A warning, never a denial: the job still runs on Volcano.
			if bjStatus(t, docs)["scheduler"] != "volcano" || bjCondition(t, docs, "PrerequisitesReady")["status"] != "True" {
				t.Errorf("%s: ExceedsCapability stopped the job", c.name)
			}
			if !strings.Contains(fmt.Sprint(bjStatus(t, docs)["message"]), "capability") {
				t.Errorf("%s: the warning is not in status.message", c.name)
			}
		}
	}
	// Memory alone over the capability (inside the quota once the quota is raised).
	cluster := vcEdit(vcCluster(t), "acme-payments-dev", func(d map[string]any) {
		d["spec"].(map[string]any)["capability"] = map[string]any{"cpu": "8", "memory": "4Gi"}
	})
	got := bjCondition(t, vcMustRender(t, xr, cluster, nil), "QueueFits") // 1Gi + 4 × 1536Mi = 7Gi
	if got["reason"] != "ExceedsCapability" || !strings.Contains(fmt.Sprint(got["message"]), "memory 7168Mi > capability 4Gi") {
		t.Errorf("memory over the capability: QueueFits = %v", got)
	}
	// A single run is one pod, and a pod larger than the capability is never admitted either.
	single := bjWithParams(bjXR(t, "xr-ondemand.yaml"), map[string]any{"resources": map[string]any{"cpu": "9", "memory": "1Gi"}})
	if got := bjCondition(t, vcMustRender(t, single, vcCluster(t), nil), "QueueFits"); got["reason"] != "ExceedsCapability" {
		t.Errorf("a single run over the capability: QueueFits = %v", got)
	}
	// On prod the capability is checked before the deserved share (8 CPU / 16Gi vs 2 / 4Gi).
	prod := bjWithParams(vcXR(t, "xr-partitioned-scheduled.yaml"), map[string]any{"workers": 20, "resources": map[string]any{"cpu": "500m", "memory": "512Mi"}})
	if got := bjCondition(t, vcMustRender(t, prod, vcCluster(t), nil), "QueueFits"); got["reason"] != "ExceedsCapability" {
		t.Errorf("a prod gang over the capability: QueueFits = %v, want ExceedsCapability before ExceedsDeserved", got)
	}
	// The native path has no queue to exceed.
	if got := bjCondition(t, vcMustRender(t, bjWithParams(xr, map[string]any{"workers": 16, "resources": map[string]any{"cpu": "500m", "memory": "256Mi", "workerCpu": "1", "workerMemory": "256Mi"}}), vcNoVolcano(t), nil), "QueueFits"); got["reason"] != "NativeScheduler" {
		t.Errorf("natively: QueueFits = %v", got)
	}
}
