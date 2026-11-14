/*
Copyright 2026 The Adhar Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package up

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/config"
	provider "adhar-io/adhar/platform/providers"
)

// Volcano's root queue (the batch ceiling) and every team's prod queue share are
// sized from THIS cluster at seed time (design 0025 D17; user decision DEC-3,
// 2026-11-14): globalSettings.batch, or 30% of the CPU and 20% of the memory of
// maxWorkers x worker allocatable. The result rides on the platform's
// BuildCustomizationSpec into the stack templates core/volcano renders. It is
// inert while the volcano package is disabled, and costs one node LIST.

// The kubelet reserve every self-managed node is given (platform/providers
// kubeadm.go: systemReserved 200m / 1Gi + kubeReserved 300m / 1536Mi, and a 1Gi
// memory.available eviction margin). Used only when no worker can be read yet and
// the allocatable is estimated from the machine type instead.
const (
	nodeReservedCPUMilli int64 = 200 + 300
	nodeReservedMemory   int64 = 1<<30 + 1536<<20 + 1<<30
)

// workerAllocatable reads the schedulable workers: how many, and the smallest
// allocatable CPU and memory among them (the shape the autoscaler adds). A
// cluster whose only schedulable nodes are control-plane nodes (Kind, a
// single-node cluster) counts those.
func workerAllocatable(ctx context.Context, c client.Client) (workers int, cpuMilli, memBytes int64, err error) {
	nodes := &corev1.NodeList{}
	if err := c.List(ctx, nodes); err != nil {
		return 0, 0, 0, err
	}
	schedulable := func(n corev1.Node) bool {
		if n.Spec.Unschedulable {
			return false
		}
		for _, t := range n.Spec.Taints {
			if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
				return false
			}
		}
		return n.Status.Allocatable.Cpu().MilliValue() > 0 && n.Status.Allocatable.Memory().Value() > 0
	}
	controlPlane := func(n corev1.Node) bool {
		_, cp := n.Labels["node-role.kubernetes.io/control-plane"]
		_, m := n.Labels["node-role.kubernetes.io/master"]
		return cp || m
	}
	count := func(wantControlPlane bool) {
		for _, n := range nodes.Items {
			if controlPlane(n) != wantControlPlane || !schedulable(n) {
				continue
			}
			workers++
			cpu, mem := n.Status.Allocatable.Cpu().MilliValue(), n.Status.Allocatable.Memory().Value()
			if cpuMilli == 0 || cpu < cpuMilli {
				cpuMilli = cpu
			}
			if memBytes == 0 || mem < memBytes {
				memBytes = mem
			}
		}
	}
	count(false)
	if workers == 0 {
		count(true)
	}
	return workers, cpuMilli, memBytes, nil
}

// batchMaxWorkers is the most workers this cluster may have: the autoscaler's
// maxWorkers when it is on, else the workers it has. Never fewer than it has.
func batchMaxWorkers(workers int, autoscaling *v1alpha1.AutoscalingSpec) int {
	if autoscaling == nil || !autoscaling.Enabled {
		return workers
	}
	return max(int(autoscaling.WithDefaults().MaxWorkers), workers)
}

// estimatedWorkerAllocatable is one worker's allocatable from its machine type,
// for a cluster that has no readable worker yet. Zero when the size is unknown.
func estimatedWorkerAllocatable(envConfig *config.ResolvedEnvironmentConfig) (cpuMilli, memBytes int64) {
	if envConfig == nil {
		return 0, 0
	}
	sizes, err := provider.ResolveEffectiveSizes(envConfig)
	if err != nil || sizes.Worker == "" {
		return 0, 0
	}
	p := provider.CanonicalProviderName(envConfig.ResolvedProvider)
	vcpu, gib := provider.EstimateVCPU(p, sizes.Worker), provider.EstimateMemoryGiB(p, sizes.Worker)
	if vcpu <= 0 || gib <= 0 {
		return 0, 0
	}
	cpuMilli = int64(vcpu)*1000 - nodeReservedCPUMilli
	memBytes = int64(gib)<<30 - nodeReservedMemory
	if cpuMilli <= 0 || memBytes <= 0 {
		return 0, 0
	}
	return cpuMilli, memBytes
}

// batchSchedulerSizing resolves globalSettings.batch against the cluster and
// returns what the seed renders. settings may be nil (every field defaults).
func batchSchedulerSizing(ctx context.Context, c client.Client, settings *config.BatchSettings, envConfig *config.ResolvedEnvironmentConfig, autoscaling *v1alpha1.AutoscalingSpec) (config.BatchScheduler, []string, error) {
	workers, cpuMilli, memBytes, err := workerAllocatable(ctx, c)
	if err != nil || workers == 0 {
		// Not measurable from the cluster: estimate one worker from its machine
		// type and the configured count, or fall back to the reference values.
		cpuMilli, memBytes = estimatedWorkerAllocatable(envConfig)
		workers = 0
		if envConfig != nil {
			if sizes, sErr := provider.ResolveEffectiveSizes(envConfig); sErr == nil {
				workers = sizes.Workers
			}
		}
	}
	return config.ResolveBatchScheduler(settings, batchMaxWorkers(workers, autoscaling), cpuMilli, memBytes)
}

// withBatchSizing records the sizing on the template data.
func withBatchSizing(spec *v1alpha1.BuildCustomizationSpec, s config.BatchScheduler) {
	spec.BatchCeilingCPU = s.CeilingCPU
	spec.BatchCeilingMemory = s.CeilingMemory
	spec.BatchProdDeservedCPU = s.ProdDeservedCPU
	spec.BatchProdDeservedMemory = s.ProdDeservedMemory
}
