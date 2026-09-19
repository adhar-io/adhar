package provider

import "testing"

// clusterConfig keys arrive in three conventions: camelCase from config.yaml,
// snake_case from the provider docs and the shipped test fixtures, and the
// internal names. They must all resolve to the same key. Matching literally in
// camelCase meant a config asking for node_count silently got the default worker
// count, with nothing logged (found preparing the GCP deployment, 2026-09-19).
func TestNormalizeClusterConfigKeyFoldsEverySpelling(t *testing.T) {
	groups := [][]string{
		{"nodeCount", "node_count", "NODE_COUNT", "node-count"},
		{"machineType", "machine_type", "Machine.Type"},
		{"workerReplicas", "worker_replicas"},
		{"podCIDR", "pod_cidr", "podCidr"},
	}
	for _, group := range groups {
		want := normalizeClusterConfigKey(group[0])
		if want == "" {
			t.Fatalf("%q normalized to the empty string", group[0])
		}
		for _, spelling := range group[1:] {
			if got := normalizeClusterConfigKey(spelling); got != want {
				t.Errorf("%q normalized to %q, want %q (same key as %q)", spelling, got, want, group[0])
			}
		}
	}

	// Distinct keys must stay distinct.
	if normalizeClusterConfigKey("nodeCount") == normalizeClusterConfigKey("numNodes") {
		t.Error("nodeCount and numNodes are different keys and must not collide")
	}
}
