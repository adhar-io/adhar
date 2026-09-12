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

package dataplane

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
)

// Secrets and paths the Cilium agent and KVStoreMesh already expect: the
// embedded install manifest projects both of these (optional) Secrets into
// /var/lib/cilium/clustermesh alongside the local/common client certificates,
// so joining a mesh is purely a matter of adding one entry per peer. Nothing
// has to be restarted or patched.
const (
	clusterMeshSecret   = "cilium-clustermesh"
	kvstoreMeshSecret   = "cilium-kvstoremesh"
	clusterMeshSvcName  = "clustermesh-apiserver"
	ciliumConfigMapName = "cilium-config"
	meshDomainSuffix    = ".mesh.cilium.io"

	// meshClusterAnnotation records the peer's Cilium cluster name on the
	// DataPlane, so the mesh can be unwound at finalize time without having to
	// reach a cluster that is being retired.
	meshClusterAnnotation = "adhar.io/mesh-cluster"
)

// meshEndpoint is how one cluster is reached by its peers.
type meshEndpoint struct {
	Cluster string
	Host    string
	Port    int32
}

// ensureMeshPeering joins two Cilium clusters by writing each side's coordinates
// into the other's clustermesh Secrets.
//
// It deliberately does NOT shell out to `cilium clustermesh connect`. That
// command drives the join by re-rendering the Cilium Helm chart, so it requires
// a Helm release to exist ("Unable to find Helm release for the target
// cluster"); Adhar installs Cilium from the embedded rendered manifest and has
// no release. The join itself is small and declarative — two Secret entries per
// direction, which is exactly what the chart writes when you set
// `clustermesh.config.clusters` — so the controller writes them directly and
// keeps `cilium clustermesh status` for verification, which needs no Helm.
func (r *DataPlaneReconciler) ensureMeshPeering(ctx context.Context, dp *v1alpha1.DataPlane, plane client.Client) error {
	local, err := meshEndpointOf(ctx, r.Client, "control plane")
	if err != nil {
		return err
	}
	remote, err := meshEndpointOf(ctx, plane, "data plane "+dp.Name)
	if err != nil {
		return err
	}
	if local.Cluster == remote.Cluster {
		return fmt.Errorf("both clusters call themselves %q: a Cluster Mesh needs a unique cluster name (and id) per member — set spec.clusterMesh on the data plane's AdharPlatform", local.Cluster)
	}
	if dp.Annotations[meshClusterAnnotation] != remote.Cluster {
		patched := dp.DeepCopy()
		if patched.Annotations == nil {
			patched.Annotations = map[string]string{}
		}
		patched.Annotations[meshClusterAnnotation] = remote.Cluster
		if err := r.Patch(ctx, patched, client.MergeFrom(dp)); err != nil {
			return fmt.Errorf("recording the mesh cluster name: %w", err)
		}
		dp.Annotations = patched.Annotations
	}
	if err := writeMeshPeer(ctx, r.Client, remote); err != nil {
		return fmt.Errorf("registering %s on the control plane: %w", remote.Cluster, err)
	}
	if err := writeMeshPeer(ctx, plane, local); err != nil {
		return fmt.Errorf("registering %s on data plane %s: %w", local.Cluster, dp.Name, err)
	}
	return nil
}

// meshEndpointOf reads a cluster's mesh identity and the address its peers must
// dial: the clustermesh-apiserver's LoadBalancer address, or a node address plus
// the Service's node port.
func meshEndpointOf(ctx context.Context, c client.Client, who string) (meshEndpoint, error) {
	cfg := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Name: ciliumConfigMapName, Namespace: controlPlaneNamespace}, cfg); err != nil {
		return meshEndpoint{}, fmt.Errorf("reading cilium-config on the %s: %w", who, err)
	}
	name := cfg.Data["cluster-name"]
	if name == "" {
		return meshEndpoint{}, fmt.Errorf("the %s has no Cilium cluster name", who)
	}

	svc := &corev1.Service{}
	if err := c.Get(ctx, types.NamespacedName{Name: clusterMeshSvcName, Namespace: controlPlaneNamespace}, svc); err != nil {
		if apierrors.IsNotFound(err) {
			return meshEndpoint{}, fmt.Errorf("the %s runs no clustermesh-apiserver: set spec.clusterMesh.apiServer.enabled on its AdharPlatform", who)
		}
		return meshEndpoint{}, err
	}
	if len(svc.Spec.Ports) == 0 {
		return meshEndpoint{}, fmt.Errorf("the %s clustermesh-apiserver Service exposes no port", who)
	}
	port := svc.Spec.Ports[0]

	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if host := ing.IP; host != "" {
				return meshEndpoint{Cluster: name, Host: host, Port: port.Port}, nil
			}
			if ing.Hostname != "" {
				return meshEndpoint{Cluster: name, Host: ing.Hostname, Port: port.Port}, nil
			}
		}
		return meshEndpoint{}, fmt.Errorf("the %s clustermesh-apiserver LoadBalancer has no address yet", who)
	case corev1.ServiceTypeNodePort:
		host, err := reachableNodeAddress(ctx, c)
		if err != nil {
			return meshEndpoint{}, fmt.Errorf("%s: %w", who, err)
		}
		return meshEndpoint{Cluster: name, Host: host, Port: port.NodePort}, nil
	default:
		return meshEndpoint{}, fmt.Errorf("the %s clustermesh-apiserver Service is %s, which no peer can reach: use NodePort or LoadBalancer", who, svc.Spec.Type)
	}
}

// reachableNodeAddress picks the address a peer cluster dials for a NodePort
// clustermesh-apiserver.
//
// The InternalIP wins, deliberately. A cluster mesh is east-west traffic and
// belongs on the private network, and on the clouds Adhar provisions the node
// port is only served there anyway: Cilium runs with kube-proxy replacement on
// the private NIC, so a droplet's public address does not answer on the
// NodePort range at all (verified on DigitalOcean — even the platform Gateway's
// own node port is reachable only through the cloud load balancer, over the
// VPC). Meshed clusters must therefore share a network — on DigitalOcean, one
// VPC, which `providers.digitalocean.config.vpc_uuid` pins. Clusters in
// separate networks need a LoadBalancer-typed apiserver instead, which this
// function is not consulted for. The ExternalIP remains the fallback for
// clusters whose nodes have no private address at all.
func reachableNodeAddress(ctx context.Context, c client.Client) (string, error) {
	nodes := &corev1.NodeList{}
	if err := c.List(ctx, nodes); err != nil {
		return "", fmt.Errorf("listing nodes: %w", err)
	}
	external := ""
	for i := range nodes.Items {
		if !nodeReady(&nodes.Items[i]) {
			continue
		}
		for _, addr := range nodes.Items[i].Status.Addresses {
			switch addr.Type {
			case corev1.NodeInternalIP:
				return addr.Address, nil
			case corev1.NodeExternalIP:
				if external == "" {
					external = addr.Address
				}
			}
		}
	}
	if external != "" {
		return external, nil
	}
	return "", fmt.Errorf("no ready node carries an address a peer could dial")
}

func nodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// writeMeshPeer adds (or refreshes) one peer in a cluster's clustermesh
// Secrets. Existing entries are preserved: a control plane meshes with many
// data planes, and each join must be additive.
//
// The two files mirror what the chart writes for `clustermesh.config.clusters`:
// the agents read the peer's state from their OWN clustermesh-apiserver (that
// is what KVStoreMesh means), and KVStoreMesh itself dials the peer's apiserver
// over `<cluster>.mesh.cilium.io`, resolved through the `cilium-host-aliases`
// entry in the very same file. `*.mesh.cilium.io` is a SAN on every
// clustermesh-apiserver certificate, which is why the name — not the IP — is
// what gets dialled.
func writeMeshPeer(ctx context.Context, c client.Client, peer meshEndpoint) error {
	local := fmt.Sprintf(`endpoints:
- https://%s.%s.svc:2379
trusted-ca-file: /var/lib/cilium/clustermesh/local-etcd-client-ca.crt
key-file: /var/lib/cilium/clustermesh/local-etcd-client.key
cert-file: /var/lib/cilium/clustermesh/local-etcd-client.crt
`, clusterMeshSvcName, controlPlaneNamespace)

	remote := fmt.Sprintf(`endpoints:
- https://%[1]s%[2]s:%[3]d
trusted-ca-file: /var/lib/cilium/clustermesh/common-etcd-client-ca.crt
key-file: /var/lib/cilium/clustermesh/common-etcd-client.key
cert-file: /var/lib/cilium/clustermesh/common-etcd-client.crt
cilium-host-aliases:
- hostname: %[1]s%[2]s
  ips:
    - %[4]s
`, peer.Cluster, meshDomainSuffix, peer.Port, peer.Host)

	if err := upsertSecretKey(ctx, c, clusterMeshSecret, peer.Cluster, []byte(local)); err != nil {
		return err
	}
	return upsertSecretKey(ctx, c, kvstoreMeshSecret, peer.Cluster, []byte(remote))
}

func upsertSecretKey(ctx context.Context, c client.Client, name, key string, value []byte) error {
	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: controlPlaneNamespace}, secret)
	switch {
	case apierrors.IsNotFound(err):
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: controlPlaneNamespace,
				Labels:    map[string]string{"adhar.io/component": "clustermesh"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{key: value},
		}
		return c.Create(ctx, secret)
	case err != nil:
		return err
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if string(secret.Data[key]) == string(value) {
		return nil
	}
	secret.Data[key] = value
	return c.Update(ctx, secret)
}

// removeMeshPeer drops one peer from a cluster's clustermesh Secrets.
func removeMeshPeer(ctx context.Context, c client.Client, cluster string) error {
	for _, name := range []string{clusterMeshSecret, kvstoreMeshSecret} {
		secret := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: controlPlaneNamespace}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if _, ok := secret.Data[cluster]; !ok {
			continue
		}
		delete(secret.Data, cluster)
		if err := c.Update(ctx, secret); err != nil {
			return err
		}
	}
	return nil
}

// removeMeshPeering drops the data plane from the control plane's mesh. The
// data-plane side is deliberately left alone: an adopted cluster outlives its
// DataPlane object and may still be meshed with others, and by the time a
// plane is retired it is often already unreachable.
func (r *DataPlaneReconciler) removeMeshPeering(ctx context.Context, dp *v1alpha1.DataPlane) error {
	cfg := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: ciliumConfigMapName, Namespace: controlPlaneNamespace}, cfg); err != nil {
		return client.IgnoreNotFound(err)
	}
	// The peer is keyed by its Cilium cluster name, recorded when the mesh was
	// joined; the plane's own name is the fallback for a DataPlane that never
	// got that far.
	peer := dp.Annotations[meshClusterAnnotation]
	if peer == "" {
		peer = dp.Name
	}
	return removeMeshPeer(ctx, r.Client, peer)
}
