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

// Fleet membership: every data plane becomes a Karmada member cluster.
//
// ---------------------------------------------------------------------------
// WHY THIS EXISTS
// ---------------------------------------------------------------------------
// Karmada has been `enabled: true` in the production profile since 2026-09-20
// and did nothing at all. Its control plane ran — apiserver, etcd, scheduler,
// controller-manager, webhook, aggregated apiserver — with ZERO member
// clusters, no `PropagationPolicy` anywhere in the tree, and no Go code that
// so much as mentioned it. The fleet hub did not know the fleet existed.
//
// The package's own values.yaml already described this step as done:
//
//	`agent` is the other half, installed on each MEMBER cluster; the
//	data-plane controller handles that when a cluster is registered
//
// It did not. ADR-0023 §3 names Karmada as the thing that enforces declared
// placement, and nothing can be placed onto clusters the fleet API has never
// heard of — so registration is the step every later policy depends on.
//
// ---------------------------------------------------------------------------
// PUSH, NOT PULL — AND THE REASON IS BLAST RADIUS
// ---------------------------------------------------------------------------
// Karmada offers two sync modes. Pull installs `karmada-agent` on the member,
// which then needs a credential for the FLEET API: every data plane would hold
// a key to the control plane that governs all the others, so compromising one
// workload cluster would reach the hub. Push keeps the direction of trust the
// way the rest of this platform already has it — the control plane holds
// members' credentials (ArgoCD holds exactly the same kubeconfig, two phases
// earlier in this reconcile) and reaches out. One control plane, many data
// planes, and no data plane holding anything that points back.
//
// ---------------------------------------------------------------------------
// NO NEW DEPENDENCY
// ---------------------------------------------------------------------------
// The `Cluster` object is written as `unstructured`. Vendoring
// karmada.io/api would pull the whole Karmada module (and its
// controller-runtime and k8s pins) into this binary for one CRD with four
// fields, and a version skew between that module and the chart would be a
// build-time fight over a resource we only ever create and read a condition
// from.

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
)

const (
	// karmadaKubeconfigSecret holds admin credentials for the fleet apiserver,
	// published by the karmada package's cert/bootstrap job. Its absence is how
	// this code knows Karmada is not in the profile.
	karmadaKubeconfigSecret = "karmada-kubeconfig"
	karmadaKubeconfigKey    = "kubeconfig"
	// karmadaClusterNamespace is where member credentials live INSIDE the fleet
	// control plane (not on the host cluster). Karmada's own tooling uses this
	// name and its controllers read `spec.secretRef` from wherever it points,
	// so this only has to be consistent with what we write.
	karmadaClusterNamespace = "karmada-cluster"
	karmadaSyncModePush     = "Push"
)

// karmadaClusterGVK is the member-cluster resource in the fleet API.
var karmadaClusterGVK = schema.GroupVersionKind{
	Group:   "cluster.karmada.io",
	Version: "v1alpha1",
	Kind:    "Cluster",
}

// fleetClient builds a client for the Karmada apiserver from the admin
// kubeconfig on the host cluster.
//
// Returns (nil, nil) when Karmada is not installed: a platform without a fleet
// hub is a supported shape (the local profile has `karmada` disabled), and a
// missing optional component must not fail a data plane's reconcile. Every
// other error is real and surfaces.
func (r *DataPlaneReconciler) fleetClient(ctx context.Context) (client.Client, error) {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: karmadaKubeconfigSecret, Namespace: controlPlaneNamespace}, secret)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", karmadaKubeconfigSecret, err)
	}
	raw := secret.Data[karmadaKubeconfigKey]
	if len(raw) == 0 {
		// The Secret exists but the bootstrap job has not filled it in yet.
		// Indistinguishable from "not installed" for our purposes, and the
		// next reconcile will find it.
		return nil, nil
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing the fleet kubeconfig: %w", err)
	}
	return r.newFleetClient(cfg)
}

// newFleetClient is a seam so tests can supply a fake fleet API without
// standing up a second apiserver.
func (r *DataPlaneReconciler) newFleetClient(cfg *rest.Config) (client.Client, error) {
	if r.FleetClientFor != nil {
		return r.FleetClientFor(cfg)
	}
	return client.New(cfg, client.Options{})
}

// ensureKarmadaRegistration makes this data plane a member of the fleet.
//
// Returns the member name and whether it is Ready. ("", false, nil) means
// Karmada is not part of this platform — the caller records that and moves on.
func (r *DataPlaneReconciler) ensureKarmadaRegistration(ctx context.Context, dp *v1alpha1.DataPlane) (string, bool, error) {
	fleet, err := r.fleetClient(ctx)
	if err != nil {
		return "", false, err
	}
	if fleet == nil {
		return "", false, nil
	}

	name, ns := r.resolveKubeconfigRef(dp)
	endpoint, token, caBundle, err := r.memberCredentials(ctx, name, ns)
	if err != nil {
		return "", false, err
	}
	if token == "" {
		// A kubeconfig authenticating with a client certificate carries no
		// bearer token, and Karmada's push mode wants one. Say so plainly
		// rather than registering a member the hub cannot authenticate as.
		return "", false, fmt.Errorf("the kubeconfig in %s/%s has no bearer token; Karmada push mode needs a "+
			"ServiceAccount token (a client-certificate kubeconfig cannot be used to join the fleet)", ns, name)
	}

	if err := r.ensureFleetNamespace(ctx, fleet); err != nil {
		return "", false, err
	}

	credential := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: dp.Name, Namespace: karmadaClusterNamespace},
		Data:       map[string][]byte{"token": []byte(token), "caBundle": caBundle},
	}
	if err := ssaApply(ctx, fleet, credential); err != nil {
		return "", false, fmt.Errorf("applying the fleet member credential: %w", err)
	}

	member := &unstructured.Unstructured{}
	member.SetGroupVersionKind(karmadaClusterGVK)
	member.SetName(dp.Name)
	member.SetLabels(fleetMemberLabels(dp))
	if err := unstructured.SetNestedMap(member.Object, map[string]any{
		"syncMode":    karmadaSyncModePush,
		"apiEndpoint": endpoint,
		"secretRef": map[string]any{
			"namespace": karmadaClusterNamespace,
			"name":      dp.Name,
		},
		// The hub talks to the member over the CA in the credential above.
		"insecureSkipTLSVerification": len(caBundle) == 0,
	}, "spec"); err != nil {
		return "", false, fmt.Errorf("building the fleet member spec: %w", err)
	}
	if err := ssaApply(ctx, fleet, member); err != nil {
		return "", false, fmt.Errorf("applying the fleet member cluster: %w", err)
	}

	return dp.Name, r.fleetMemberReady(ctx, fleet, dp.Name), nil
}

// ensureFleetNamespace creates the member-credential namespace inside the fleet
// control plane. Idempotent; an existing namespace is left alone.
func (r *DataPlaneReconciler) ensureFleetNamespace(ctx context.Context, fleet client.Client) error {
	ns := &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: karmadaClusterNamespace},
	}
	if err := ssaApply(ctx, fleet, ns); err != nil {
		return fmt.Errorf("applying namespace %s in the fleet control plane: %w", karmadaClusterNamespace, err)
	}
	return nil
}

// fleetMemberReady reads the member's `Ready` condition. A member that has just
// been created has no status at all, which is "not yet", not an error — the
// caller requeues.
func (r *DataPlaneReconciler) fleetMemberReady(ctx context.Context, fleet client.Client, name string) bool {
	member := &unstructured.Unstructured{}
	member.SetGroupVersionKind(karmadaClusterGVK)
	if err := fleet.Get(ctx, types.NamespacedName{Name: name}, member); err != nil {
		return false
	}
	conditions, found, err := unstructured.NestedSlice(member.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, raw := range conditions {
		cond, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == "Ready" && cond["status"] == string(metav1.ConditionTrue) {
			return true
		}
	}
	return false
}

// fleetMemberLabels are the labels a PropagationPolicy's clusterAffinity
// selects on.
//
// They are the SAME labels the ArgoCD cluster secret carries, because a
// placement expressed once in Git must mean the same thing to both engines —
// `spec.placement.labels` plus the identity labels the platform stamps. A
// policy written against `tier: production` and an ApplicationSet generator
// written against `tier: production` have to land on the same clusters, or
// "declared placement" is two sets of rules that drift.
func fleetMemberLabels(dp *v1alpha1.DataPlane) map[string]string {
	labels := map[string]string{
		dataPlaneLabelKey:     dp.Name,
		clusterLabelKey:       dp.Name,
		dataPlaneModeLabelKey: string(dp.Spec.Infrastructure.Mode),
	}
	for k, v := range dp.Spec.Placement.Labels {
		labels[k] = v
	}
	return labels
}

// memberCredentials pulls the endpoint, bearer token and CA out of a data
// plane's kubeconfig secret — the same secret ArgoCD is registered from.
func (r *DataPlaneReconciler) memberCredentials(ctx context.Context, name, ns string) (string, string, []byte, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, secret); err != nil {
		return "", "", nil, fmt.Errorf("getting kubeconfig secret %s/%s: %w", ns, name, err)
	}
	raw := kubeconfigBytes(secret)
	if len(raw) == 0 {
		return "", "", nil, fmt.Errorf("kubeconfig secret %s/%s has no usable data", ns, name)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return "", "", nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	return cfg.Host, cfg.BearerToken, cfg.CAData, nil
}

// deleteKarmadaRegistration takes the plane out of the fleet during finalize.
// Tolerant of every "already gone" shape, including Karmada itself having been
// uninstalled: a DataPlane must still be deletable then.
func (r *DataPlaneReconciler) deleteKarmadaRegistration(ctx context.Context, dp *v1alpha1.DataPlane) error {
	fleet, err := r.fleetClient(ctx)
	if err != nil || fleet == nil {
		return nil
	}
	member := &unstructured.Unstructured{}
	member.SetGroupVersionKind(karmadaClusterGVK)
	member.SetName(dp.Name)
	if err := fleet.Delete(ctx, member); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return fmt.Errorf("deleting the fleet member cluster: %w", err)
	}
	credential := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: dp.Name, Namespace: karmadaClusterNamespace},
	}
	if err := fleet.Delete(ctx, credential); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting the fleet member credential: %w", err)
	}
	return nil
}
