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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
)

// ensureAgents reports whether the thin-agent profile is healthy on the data
// plane. It is ready when the ArgoCD cluster secret exists and — if any ArgoCD
// Applications target this cluster — they are all Healthy. Envtest-friendly:
// tolerates the Application CRD being absent (treated as "no apps yet, ready").
func (r *DataPlaneReconciler) ensureAgents(ctx context.Context, dp *v1alpha1.DataPlane) (bool, error) {
	// The cluster registration must exist before agents can be placed.
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: argoClusterSecretName(dp), Namespace: controlPlaneNamespace}, secret)
	if apierrors.IsNotFound(err) {
		return false, nil // registration not visible yet; keep converging
	}
	if err != nil {
		return false, err
	}

	apps := &unstructured.UnstructuredList{}
	apps.SetGroupVersionKind(argoApplicationListGVK)
	if err := r.List(ctx, apps, client.InNamespace(controlPlaneNamespace)); err != nil {
		// Application CRD absent (envtest) — nothing to gate on.
		if isMissingKind(err) {
			return true, nil
		}
		return false, err
	}

	targeted := 0
	healthy := 0
	for i := range apps.Items {
		app := &apps.Items[i]
		if !applicationTargetsCluster(app, dp.Status.ArgoCDCluster) && !applicationTargetsCluster(app, dp.Name) {
			continue
		}
		targeted++
		if applicationHealthy(app) {
			healthy++
		}
	}
	// No profile apps discovered yet -> agents are not blocking readiness.
	if targeted == 0 {
		return true, nil
	}
	return healthy == targeted, nil
}

// applicationDestination extracts (server, name, namespace) from an ArgoCD
// Application's spec.destination.
func applicationDestination(u *unstructured.Unstructured) (server, name, namespace string) {
	server, _, _ = unstructured.NestedString(u.Object, "spec", "destination", "server")
	name, _, _ = unstructured.NestedString(u.Object, "spec", "destination", keyName)
	namespace, _, _ = unstructured.NestedString(u.Object, "spec", "destination", "namespace")
	return server, name, namespace
}

// applicationTargetsCluster reports whether an Application is destined for the
// given ArgoCD cluster (by name or server URL).
func applicationTargetsCluster(u *unstructured.Unstructured, cluster string) bool {
	if cluster == "" {
		return false
	}
	server, name, _ := applicationDestination(u)
	return name == cluster || server == cluster
}

// applicationHealthy reports whether an Application's health status is Healthy.
func applicationHealthy(u *unstructured.Unstructured) bool {
	status, _, _ := unstructured.NestedString(u.Object, "status", "health", "status")
	return status == "Healthy"
}
