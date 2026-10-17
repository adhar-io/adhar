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
	"fmt"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Why a cloud `adhar up` is slow is almost always capacity, and the operator
// cannot see it: the checklist shows "n/m apps Synced + Healthy" climbing
// slowly while the real cause is Pending pods waiting for the autoscaler to
// add a worker every cooldown. This puts the number in front of them at the
// moment it is decided.

// productionProfileRequestCPUMilli is what the production profile's enabled
// packages REQUEST in total, measured on live clusters (DigitalOcean 2026-10-17:
// eight 8-vCPU workers settled at ~18 vCPU of requests; CLAUDE.md: "a platform
// that fits in 18"). Requests, not usage: the scheduler places by requests, so
// this is the capacity below which pods stay Pending.
const productionProfileRequestCPUMilli int64 = 18_000

// capacityAdvice says, in one sentence, whether the workers the cluster starts
// with can hold the profile, and if not how long the autoscaler will take to
// get there — or that it cannot, when maxWorkers is below the need. Empty when
// the cluster is big enough.
func capacityAdvice(workers int, perWorkerCPUMilli, profileCPUMilli int64, maxWorkers int32, cooldown time.Duration, burst int32) string {
	if workers <= 0 || perWorkerCPUMilli <= 0 {
		return ""
	}
	have := int64(workers) * perWorkerCPUMilli
	if have >= profileCPUMilli {
		return ""
	}
	// Pods do not tile a node perfectly; the scheduler needs ~15% slack.
	need := int(math.Ceil(float64(profileCPUMilli) / (float64(perWorkerCPUMilli) * 0.85)))
	if burst < 1 {
		burst = 1
	}
	if int32(need) > maxWorkers {
		return fmt.Sprintf("the platform requests ~%.0f vCPU and %d workers offer %.1f; even at maxWorkers=%d (%.1f vCPU) part of it stays Pending — raise nodeCount/maxWorkers or the worker size",
			float64(profileCPUMilli)/1000, workers, float64(have)/1000, maxWorkers, float64(int64(maxWorkers)*perWorkerCPUMilli)/1000)
	}
	steps := int(math.Ceil(float64(need-workers) / float64(burst)))
	return fmt.Sprintf("the platform requests ~%.0f vCPU and %d workers offer %.1f; the autoscaler will add ~%d workers in %d step(s) of %s — start at nodeCount=%d to skip that wait",
		float64(profileCPUMilli)/1000, workers, float64(have)/1000, need-workers, steps, cooldown, need)
}

// workerCapacity reads the cluster's schedulable workers: their count and the
// smallest allocatable CPU among them (the shape the scaler will add).
func workerCapacity(ctx context.Context, c client.Client) (workers int, perWorkerCPUMilli int64, err error) {
	nodes := &corev1.NodeList{}
	if err := c.List(ctx, nodes); err != nil {
		return 0, 0, err
	}
	for _, n := range nodes.Items {
		if _, cp := n.Labels["node-role.kubernetes.io/control-plane"]; cp {
			continue
		}
		if _, m := n.Labels["node-role.kubernetes.io/master"]; m {
			continue
		}
		cpu := n.Status.Allocatable.Cpu().MilliValue()
		if cpu <= 0 {
			continue
		}
		workers++
		if perWorkerCPUMilli == 0 || cpu < perWorkerCPUMilli {
			perWorkerCPUMilli = cpu
		}
	}
	return workers, perWorkerCPUMilli, nil
}
