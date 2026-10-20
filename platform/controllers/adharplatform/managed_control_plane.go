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

package adharplatform

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
)

// On Civo's managed k3s the control plane is not hosted by Civo: the k3s server
// — API server, controllers and the sqlite datastore — runs on the FIRST node of
// the pool, the one Civo gives the cluster's public address (`master_ip`). That
// node is otherwise an ordinary, untainted worker, so the scheduler fills it
// with platform pods exactly like the others.
//
// On 2026-10-15 (mum1, 3 × g4c.kube.small) that is what killed the bring-up: the
// foundation came up, Argo CD started syncing 77 applications, and ~30 minutes
// in the API server on that node stopped answering — TCP to :6443 timed out
// while the node still answered ping and Civo reported the cluster UPGRADING
// with `ClusterVersionSync=False` for an hour, its upgrade job stuck against a
// k3s that would not come back. From the outside this read as the cloud
// breaking; it was an 8-core node asked to run a control plane for ~600 CRDs
// AND a third of the platform.
//
// A kubeadm cluster never has this problem because kubeadm taints its control
// plane. This does the same for Civo: the node carrying an ExternalIP (the only
// one Civo gives a public address, and the one `master_ip` names) is tainted
// `node-role.kubernetes.io/control-plane:NoSchedule` and labelled as a control
// plane before anything else is installed, so the platform's pods land on the
// other nodes and the k3s server keeps the machine to itself. Civo's own
// DaemonSets (Cilium, CSI) tolerate the taint as DaemonSets do.
//
// Only for `provider: civo` in `clusterMode: managed`: on EKS/AKS/GKE/DOKS the
// control plane is genuinely hosted and no node is special; on compute mode
// kubeadm already did this; on a provided cluster the nodes are not ours.

const (
	controlPlaneTaintKey   = "node-role.kubernetes.io/control-plane"
	controlPlaneRoleLabel  = "node-role.kubernetes.io/control-plane"
	civoProviderName       = "civo"
	controlPlaneAnnotation = "adhar.io/control-plane-reserved"
)

// managedControlPlaneOnNodes reports whether this cluster's managed control
// plane runs on one of its own nodes, so that node must be kept for it.
func managedControlPlaneOnNodes(providerName, clusterMode string) bool {
	return providerName == civoProviderName && clusterMode == "managed"
}

// controlPlaneReserveWait bounds how long the foundation install waits for the
// managed control-plane node to be identified (Civo's cloud-controller gives it
// its public address a minute or two after the cluster reports ready). Past it
// the install proceeds unprotected rather than never, and says so.
const controlPlaneReserveWait = 5 * time.Minute

// reserveManagedControlPlaneNode taints and labels the node that hosts the
// managed control plane. Idempotent; a cluster with no such node (or whose
// provider hosts its control plane) is left alone.
//
// pending reports that this cluster's control plane DOES share a node with the
// platform and that node is not reserved yet — the caller must not install
// anything until it is, because NoSchedule does not move what already landed:
// on 2026-10-20 the reservation arrived two minutes after Argo CD, Gitea,
// Crossplane and Falco had been scheduled onto the k3s server's node, and the
// API server stopped answering four minutes later with the node still pinging.
func (r *AdharPlatformReconciler) reserveManagedControlPlaneNode(ctx context.Context) (pending bool, err error) {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}
	if err := r.Get(ctx, key, cm); err != nil {
		return false, nil // no spec: nothing is known about the cloud, nothing to reserve
	}
	if !managedControlPlaneOnNodes(cm.Data["provider"], cm.Data[globals.ClusterSpecClusterModeKey]) {
		return false, nil
	}
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return true, fmt.Errorf("listing nodes to find the managed control plane: %w", err)
	}
	for i := range nodes.Items {
		if hasControlPlaneTaint(&nodes.Items[i]) {
			return false, nil
		}
	}
	master := managedControlPlaneNode(nodes.Items)
	if master == nil {
		return true, nil
	}
	logger := log.FromContext(ctx)
	patch := client.MergeFrom(master.DeepCopy())
	master.Spec.Taints = append(master.Spec.Taints, corev1.Taint{Key: controlPlaneTaintKey, Effect: corev1.TaintEffectNoSchedule})
	if master.Labels == nil {
		master.Labels = map[string]string{}
	}
	master.Labels[controlPlaneRoleLabel] = ""
	if master.Annotations == nil {
		master.Annotations = map[string]string{}
	}
	master.Annotations[controlPlaneAnnotation] = "the managed control plane (k3s server) runs here; platform pods are kept off it"
	if err := r.Patch(ctx, master, patch); err != nil {
		return true, fmt.Errorf("tainting the control-plane node %s: %w", master.Name, err)
	}
	logger.Info("Reserved the node that hosts the managed control plane", "node", master.Name)
	return false, nil
}

// managedControlPlaneNode picks the node Civo gave the cluster's public address:
// it is the only one with an ExternalIP, and it is `master_ip`. With no such
// node, or more than one, nothing is chosen — guessing wrong would taint a
// worker.
func managedControlPlaneNode(nodes []corev1.Node) *corev1.Node {
	var found *corev1.Node
	for i := range nodes {
		for _, a := range nodes[i].Status.Addresses {
			if a.Type == corev1.NodeExternalIP && a.Address != "" {
				if found != nil {
					return nil
				}
				found = &nodes[i]
				break
			}
		}
	}
	return found
}

func hasControlPlaneTaint(n *corev1.Node) bool {
	for _, t := range n.Spec.Taints {
		if t.Key == controlPlaneTaintKey && t.Effect == corev1.TaintEffectNoSchedule {
			return true
		}
	}
	return false
}
