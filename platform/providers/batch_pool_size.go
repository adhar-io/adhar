package provider

import (
	"strings"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// KeyBatchMachineType carries the batch node pool's machine type
// (environments[].batchPool.machineType, design 0025 D14) in a provider map, so a
// SCALE path can size a pool node when the pool has no node left to copy.
//
// At create time the pool's type rides on its node group (buildClusterSpec), but a
// scale path has no cluster spec: it sizes a new node from the group's existing
// members, and an EMPTY group fell back to the provider-wide worker size — so a
// pool whose last node was retired or lost came back at the general workers' size,
// not the one the pool declares. The key reaches the providers two ways: the
// environment layer of every provider `adhar up` builds (EnvironmentProviderOverrides)
// and the recorded provider map in adhar-cluster-spec, from which the adhar-controlplane
// node autoscaler — the pool's only scaler — constructs its provider.
const KeyBatchMachineType = "batchMachineType"

// BatchPoolMachineType is the pool's configured machine type, "" when there is no
// pool or it names none (the provider's worker default then sizes it).
func BatchPoolMachineType(envConfig *config.ResolvedEnvironmentConfig) string {
	if envConfig == nil || envConfig.BatchPool == nil {
		return ""
	}
	return strings.TrimSpace(envConfig.BatchPool.MachineType)
}

// EmptyBatchGroupType is the node-group size a scale path passes to WorkerSize for
// a group: the batch pool's configured type when the group IS the pool and has no
// member to copy, "" otherwise. A pool that still has nodes keeps their size, as
// every other group does, so a scaled pool stays homogeneous; any group other than
// the pool is untouched.
func EmptyBatchGroupType(nodeGroup, membersType, batchMachineType string) string {
	if nodeGroup != globals.BatchNodeGroupName || strings.TrimSpace(membersType) != "" {
		return ""
	}
	return strings.TrimSpace(batchMachineType)
}
