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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
)

// ssaFieldOwner is the field manager used for all server-side applies.
const ssaFieldOwner = client.FieldOwner(v1alpha1.FieldManager)

// ssaApply server-side-applies obj with force ownership — the platform-wide SSA
// idiom (see platform/controllers/adharplatform/helpers.go). Centralised here so
// the one deprecation shim for client.Apply lives in a single place.
func ssaApply(ctx context.Context, c client.Client, obj client.Object) error {
	//nolint:staticcheck // client.Apply is the established platform-wide SSA patch idiom
	return c.Patch(ctx, obj, client.Apply, ssaFieldOwner, client.ForceOwnership)
}

// ensureInfra realises the underlying cluster and returns a client to it once
// reachable. The second return value reports readiness; a nil error with
// ready=false means "still provisioning, requeue".
func (r *DataPlaneReconciler) ensureInfra(ctx context.Context, dp *v1alpha1.DataPlane) (client.Client, bool, error) {
	switch dp.Spec.Infrastructure.Mode {
	case v1alpha1.InfraModeAdopt:
		return r.ensureAdopt(ctx, dp)
	case v1alpha1.InfraModeComposite:
		return r.ensureComposite(ctx, dp)
	case v1alpha1.InfraModeVCluster:
		return r.ensureVCluster(ctx, dp)
	default:
		return nil, false, fmt.Errorf("unknown infrastructure mode %q", dp.Spec.Infrastructure.Mode)
	}
}

// ensureAdopt registers an existing cluster from a referenced kubeconfig
// secret. Fully implemented: load the secret, build a client, verify
// reachability by querying the discovery endpoint.
func (r *DataPlaneReconciler) ensureAdopt(ctx context.Context, dp *v1alpha1.DataPlane) (client.Client, bool, error) {
	ref := dp.Spec.Infrastructure.KubeconfigSecretRef
	if ref == nil || ref.Name == "" {
		return nil, false, fmt.Errorf("mode=adopt requires spec.infrastructure.kubeconfigSecretRef")
	}
	ns := ref.Namespace
	if ns == "" {
		ns = controlPlaneNamespace
	}
	return r.clientFromKubeconfigSecret(ctx, dp, ref.Name, ns, true)
}

// ensureComposite server-side-applies a CompositeCluster XR (owned for GC) and
// waits for the composition to publish a `<dp>-kubeconfig` connection secret.
// Real but tolerant: if the CompositeCluster CRD is absent (e.g. envtest) the
// apply is skipped so reconciliation still converges deterministically.
func (r *DataPlaneReconciler) ensureComposite(ctx context.Context, dp *v1alpha1.DataPlane) (client.Client, bool, error) {
	logger := log.FromContext(ctx)

	xr := r.compositeClusterFor(dp)
	if err := controllerutil.SetControllerReference(dp, xr, r.Scheme); err != nil {
		return nil, false, fmt.Errorf("setting owner reference on CompositeCluster: %w", err)
	}
	if err := ssaApply(ctx, r.Client, xr); err != nil {
		if !isMissingKind(err) {
			return nil, false, fmt.Errorf("applying CompositeCluster: %w", err)
		}
		logger.V(1).Info("CompositeCluster CRD not installed; skipping XR apply", "error", err.Error())
	}

	// Record the composite reference on the spec once, so re-runs adopt the
	// existing XR rather than re-deriving it.
	if dp.Spec.Infrastructure.CompositeRef == nil {
		dp.Spec.Infrastructure.CompositeRef = &v1alpha1.NamedRef{Name: dp.Name, Namespace: controlPlaneNamespace}
		if err := r.Update(ctx, dp); err != nil {
			return nil, false, fmt.Errorf("recording compositeRef: %w", err)
		}
	}

	// The composition publishes the connection secret named <dp>-kubeconfig.
	return r.clientFromKubeconfigSecret(ctx, dp, dp.Name+"-kubeconfig", controlPlaneNamespace, false)
}

// ensureVCluster server-side-applies a vcluster resource on the control plane
// and waits for its kubeconfig secret. Real but tolerant: the vcluster CRD may
// be absent under envtest, in which case the apply is skipped and the phase
// stays not-ready until a `<dp>-kubeconfig` secret appears.
func (r *DataPlaneReconciler) ensureVCluster(ctx context.Context, dp *v1alpha1.DataPlane) (client.Client, bool, error) {
	logger := log.FromContext(ctx)

	vc := r.vclusterFor(dp)
	if err := controllerutil.SetControllerReference(dp, vc, r.Scheme); err != nil {
		return nil, false, fmt.Errorf("setting owner reference on vcluster: %w", err)
	}
	if err := ssaApply(ctx, r.Client, vc); err != nil {
		if !isMissingKind(err) {
			return nil, false, fmt.Errorf("applying vcluster: %w", err)
		}
		logger.V(1).Info("vcluster CRD not installed; skipping apply", "error", err.Error())
	}

	// vcluster publishes its kubeconfig into a secret named <dp>-kubeconfig.
	return r.clientFromKubeconfigSecret(ctx, dp, dp.Name+"-kubeconfig", controlPlaneNamespace, false)
}

// clientFromKubeconfigSecret loads a kubeconfig secret and builds a
// client.Client for the data plane, verifying reachability via discovery.
// When required is false, a missing secret is treated as "still provisioning"
// (ready=false, nil error) rather than a hard failure.
func (r *DataPlaneReconciler) clientFromKubeconfigSecret(ctx context.Context, dp *v1alpha1.DataPlane, name, ns string, required bool) (client.Client, bool, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			if required {
				return nil, false, fmt.Errorf("kubeconfig secret %s/%s not found", ns, name)
			}
			return nil, false, nil // still provisioning
		}
		return nil, false, fmt.Errorf("getting kubeconfig secret %s/%s: %w", ns, name, err)
	}

	raw := kubeconfigBytes(secret)
	if len(raw) == 0 {
		if required {
			return nil, false, fmt.Errorf("kubeconfig secret %s/%s has no usable data", ns, name)
		}
		return nil, false, nil
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, false, fmt.Errorf("parsing kubeconfig from %s/%s: %w", ns, name, err)
	}

	// Verify reachability and record the server version + endpoint.
	disc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return nil, false, fmt.Errorf("building discovery client: %w", err)
	}
	ver, err := disc.ServerVersion()
	if err != nil {
		// Unreachable — surface for adopt (hard), tolerate for provisioning.
		if required {
			return nil, false, fmt.Errorf("data plane unreachable: %w", err)
		}
		return nil, false, nil
	}
	dp.Status.KubernetesVersion = ver.GitVersion
	dp.Status.Endpoint = restCfg.Host

	cl, err := client.New(restCfg, client.Options{Scheme: r.Scheme})
	if err != nil {
		return nil, false, fmt.Errorf("building data plane client: %w", err)
	}
	return cl, true, nil
}

// compositeClusterFor builds the CompositeCluster XR for a mode=composite plane.
func (r *DataPlaneReconciler) compositeClusterFor(dp *v1alpha1.DataPlane) *unstructured.Unstructured {
	pools := make([]interface{}, 0, len(dp.Spec.Infrastructure.NodePools))
	for _, p := range dp.Spec.Infrastructure.NodePools {
		pools = append(pools, map[string]interface{}{
			keyName: p.Name,
			"size":  p.Size,
			"count": int64(p.Count),
			"gpu":   p.GPU,
		})
	}
	xr := &unstructured.Unstructured{}
	xr.SetGroupVersionKind(compositeClusterGVK)
	xr.SetName(dp.Name)
	xr.SetNamespace(controlPlaneNamespace)
	_ = unstructured.SetNestedField(xr.Object, string(dp.Spec.Infrastructure.Provider), "spec", "provider")
	_ = unstructured.SetNestedField(xr.Object, dp.Spec.Infrastructure.Region, "spec", "region")
	if len(pools) > 0 {
		_ = unstructured.SetNestedSlice(xr.Object, pools, "spec", "nodePools")
	}
	_ = unstructured.SetNestedMap(xr.Object, map[string]interface{}{
		keyName:     dp.Name + "-kubeconfig",
		"namespace": controlPlaneNamespace,
	}, "spec", "writeConnectionSecretToRef")
	return xr
}

// vclusterFor builds the vcluster resource for a mode=vcluster plane. It uses a
// generic vcluster GVK; the real chart values (storage, gateway exposure) are a
// follow-up — this keeps the apply idempotent and tolerant.
func (r *DataPlaneReconciler) vclusterFor(dp *v1alpha1.DataPlane) *unstructured.Unstructured {
	vc := &unstructured.Unstructured{}
	vc.SetGroupVersionKind(vclusterGVK)
	vc.SetName(dp.Name)
	vc.SetNamespace(controlPlaneNamespace)
	_ = unstructured.SetNestedField(vc.Object, "adhar", "spec", "chart", keyName)
	_ = unstructured.SetNestedField(vc.Object, dp.Name+"-kubeconfig", "spec", "kubeConfigSecret")
	return vc
}

// vclusterGVK identifies the vcluster custom resource realised on the control
// plane. Kept as a variable so a real vcluster operator GVK can replace it once
// the chart/values wiring lands (follow-up).
var vclusterGVK = schema.GroupVersionKind{Group: "infra.adhar.io", Version: apiVersionV1alpha1, Kind: "VCluster"}

// deleteManagedInfra deletes the controller-created infra (CompositeCluster or
// vcluster) during finalize. Best-effort and tolerant of already-deleted or
// never-created resources (missing CRD).
func (r *DataPlaneReconciler) deleteManagedInfra(ctx context.Context, dp *v1alpha1.DataPlane) error {
	var obj *unstructured.Unstructured
	switch dp.Spec.Infrastructure.Mode {
	case v1alpha1.InfraModeComposite:
		obj = r.compositeClusterFor(dp)
	case v1alpha1.InfraModeVCluster:
		obj = r.vclusterFor(dp)
	default:
		return nil
	}
	if err := r.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) || isMissingKind(err) {
			return nil
		}
		return err
	}
	return nil
}

// kubeconfigBytes extracts kubeconfig data from a connection/kubeconfig secret,
// tolerating the common key names used by Crossplane, vcluster and adopt flows.
func kubeconfigBytes(secret *corev1.Secret) []byte {
	for _, key := range []string{"kubeconfig", "value", "config", "kubeConfig"} {
		if v, ok := secret.Data[key]; ok && len(v) > 0 {
			return v
		}
	}
	return nil
}

// isMissingKind reports whether an apply/delete failed because the target CRD
// is not installed — the tolerated case under envtest and pre-CRD clusters.
func isMissingKind(err error) bool {
	if err == nil {
		return false
	}
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err)
}
