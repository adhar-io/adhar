/*
Copyright 2025.

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

package autoscaler

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// The batch node pool (design 0025 D14, §4.7 Phase 4).
//
// A cluster with a pool is two node groups to this controller, decided apart
// with the same rules:
//
//   - the GENERAL workers (Decide → generalView): pool nodes are not workers
//     here, so minWorkers/maxWorkers, utilization and the scale-down candidate
//     describe the general workers alone; and no batch pod is demand for them,
//     opted in or not, because a batch run's demand is the pool's. A batch run
//     still lands on a general worker when one has room (its pool affinity is
//     preferred, not required), and while it runs there it is load like any
//     other pod: it keeps a node from being retired, but never buys one.
//   - the POOL (DecidePool → poolView): its nodes are the workers, between
//     minNodes and maxNodes; its demand is the batch runs that may buy nodes
//     (a prod run labelled adhar.io/scale-up "true", or its Volcano gang, the
//     Phase-1/2 rules unchanged) and that tolerate the pool's taint — a general
//     pod is never demand for it. It grows only for runs that are WAITING, never
//     proactively on utilization, and retires idle pool nodes down to minNodes.
//
// A batch pod that is pending without the pool's toleration was admitted while
// no pool node existed (or while Kyverno was down) and can never use a pool
// node: it buys nothing at all. That is why a pool's floor is at least one node.

// isPoolNode reports whether a node belongs to the batch pool.
func isPoolNode(n corev1.Node) bool {
	return n.Labels[globals.BatchNodePoolLabel] == globals.BatchNodePoolValue
}

// isBatchPod reports whether the pod is a batch run's.
func isBatchPod(p corev1.Pod) bool {
	return p.Labels[workloadLabel] == "batch"
}

// toleratesPool reports whether the pod tolerates the pool's NoSchedule taint,
// which is what adhar-batch-node-pool adds to a batch run's pod.
func toleratesPool(p corev1.Pod) bool {
	for _, t := range p.Spec.Tolerations {
		if t.Effect != "" && t.Effect != corev1.TaintEffectNoSchedule {
			continue
		}
		switch {
		case t.Key == "" && t.Operator == corev1.TolerationOpExists:
			return true
		case t.Key != globals.BatchNodePoolTaintKey:
			continue
		case t.Operator == corev1.TolerationOpExists:
			return true
		case t.Value == globals.BatchNodePoolTaintValue:
			return true
		}
	}
	return false
}

// isUnscheduled reports whether the pod is waiting for a node.
func isUnscheduled(p corev1.Pod) bool {
	return p.Status.Phase == corev1.PodPending && p.Spec.NodeName == "" && p.DeletionTimestamp == nil
}

// generalView is the snapshot of the general workers in a cluster with a pool.
func generalView(s Snapshot) Snapshot {
	out := s
	out.Nodes = make([]corev1.Node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		if !isPoolNode(n) {
			out.Nodes = append(out.Nodes, n)
		}
	}
	out.Pods = make([]corev1.Pod, 0, len(s.Pods))
	for _, p := range s.Pods {
		if isBatchPod(p) && isUnscheduled(p) {
			continue
		}
		out.Pods = append(out.Pods, p)
	}
	// Every gang that can buy nodes is a batch run's (gangWantsNodes), so all
	// gang demand is the pool's.
	out.Gangs = nil
	out.batchIsThePools = true
	return out
}

// poolView is the snapshot of the pool: its nodes as the workers, its bounds as
// min/max, its own clocks, and only batch runs that may buy a node and could
// run on one as pending demand.
func poolView(s Snapshot) Snapshot {
	pool := s.Spec.BatchPool.WithDefaults()
	out := s
	out.Spec = s.Spec
	out.Spec.BatchPool = nil
	out.Spec.NodeGroup = pool.NodeGroup
	out.Spec.MinWorkers = pool.MinNodes
	out.Spec.MaxWorkers = pool.MaxNodes
	out.Status = v1alpha1.AutoscalingStatus{
		Enabled:            s.Status.Enabled,
		Workers:            s.PoolStatus.Nodes,
		LastScaleUp:        s.PoolStatus.LastScaleUp,
		LastScaleDown:      s.PoolStatus.LastScaleDown,
		UnderutilizedSince: s.PoolStatus.UnderutilizedSince,
	}
	out.Nodes = make([]corev1.Node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		if !isPoolNode(n) {
			continue
		}
		// Every pod counted as the pool's demand tolerates the pool's taint, so
		// to this view it is not a taint: left in, freeRoom would see no room on
		// any pool node and every gang would buy a node more than it needs.
		n = *n.DeepCopy()
		n.Spec.Taints = WithoutTaint(n.Spec.Taints, globals.BatchNodePoolTaintKey)
		out.Nodes = append(out.Nodes, n)
	}
	out.Pods = make([]corev1.Pod, 0, len(s.Pods))
	for _, p := range s.Pods {
		if isUnscheduled(p) && (!isBatchPod(p) || batchMayNotBuyNodes(p) || !toleratesPool(p)) {
			continue
		}
		out.Pods = append(out.Pods, p)
	}
	out.batchIsThePools = false
	out.reactiveOnly = true
	return out
}

// poolReason rewords a decision's reason for the pool.
var poolReason = strings.NewReplacer("maxWorkers", "maxNodes", "minWorkers", "minNodes")

// DecidePool applies the policy to the batch node pool (see the top of this
// file). A cluster without a pool gets ActionNone with no reason.
func DecidePool(s Snapshot) Decision {
	if s.Spec.BatchPool == nil {
		return Decision{Action: ActionNone}
	}
	d := decide(poolView(s))
	d.Reason = "batch pool: " + poolReason.Replace(d.Reason)
	return d
}
