package adharplatform

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
)

// applyManifest applies a YAML manifest to the cluster, setting a controller
// owner reference to the AdharPlatform CR on same-namespace namespaced objects
// so they are garbage-collected with the platform. Use this for platform-managed
// infrastructure (Cilium, Gateway, ArgoCD, Gitea, Crossplane, CNPG).
func (r *AdharPlatformReconciler) applyManifest(ctx context.Context, manifestBytes []byte, resource *v1alpha1.AdharPlatform, manifestName string) error {
	return r.applyManifestOwned(ctx, manifestBytes, resource, manifestName, true)
}

// applyManifestNoOwner applies a YAML manifest WITHOUT any owner reference to the
// AdharPlatform CR. Use this for GitOps handoff resources — the ArgoCD
// ApplicationSet and its repo auth — whose lifecycle must be independent of the
// platform CR. In local mode the AdharPlatform CR is ephemeral (recreated on
// every `adhar up`); owning the ApplicationSet by it caused Kubernetes to
// garbage-collect the ApplicationSet (and cascade-delete every generated
// Application) whenever the CR churned, leaving ArgoCD empty. ArgoCD itself owns
// the Applications the ApplicationSet generates.
func (r *AdharPlatformReconciler) applyManifestNoOwner(ctx context.Context, manifestBytes []byte, resource *v1alpha1.AdharPlatform, manifestName string) error {
	return r.applyManifestOwned(ctx, manifestBytes, resource, manifestName, false)
}

// applyManifestOwned applies a YAML manifest to the cluster. When setOwnerRef is
// true it sets a controller owner reference to the AdharPlatform CR on
// same-namespace namespaced objects.
func (r *AdharPlatformReconciler) applyManifestOwned(ctx context.Context, manifestBytes []byte, resource *v1alpha1.AdharPlatform, manifestName string, setOwnerRef bool) error {
	logger := log.FromContext(ctx)

	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifestBytes), 100)
	var applyErrors []error

	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Error(err, "Failed to decode object from manifest", "manifest", manifestName)
			applyErrors = append(applyErrors, fmt.Errorf("decoding object from %s: %w", manifestName, err))
			continue
		}

		if obj.Object == nil {
			continue
		}

		// Determine if the resource is cluster-scoped
		groupVersionKind := obj.GroupVersionKind()
		mapping, err := r.RESTMapper().RESTMapping(groupVersionKind.GroupKind(), groupVersionKind.Version)
		isClusterScoped := false
		if err == nil {
			isClusterScoped = mapping.Scope.Name() == meta.RESTScopeNameRoot
		} else {
			knownClusterScopedKinds := map[schema.GroupKind]bool{
				{Group: "", Kind: "Namespace"}:                                                  true,
				{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}:                       true,
				{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"}:                true,
				{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}:               true,
				{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration"}:   true,
				{Group: "admissionregistration.k8s.io", Kind: "ValidatingWebhookConfiguration"}: true,
			}
			if knownClusterScopedKinds[groupVersionKind.GroupKind()] {
				isClusterScoped = true
			}
			logger.V(1).Info("Could not determine scope from RESTMapper, falling back", "gvk", groupVersionKind, "error", err, "assumed clusterScoped", isClusterScoped)
		}

		canSetOwnerRef := false
		if setOwnerRef && !isClusterScoped {
			resourceNamespace := obj.GetNamespace()
			if resourceNamespace == "" {
				resourceNamespace = resource.Namespace
				obj.SetNamespace(resource.Namespace)
			}

			if resourceNamespace == resource.Namespace {
				canSetOwnerRef = true
			} else {
				logger.V(1).Info("Skipping owner reference for resource in different namespace",
					"resource", groupVersionKind.Kind+"/"+obj.GetName(), "resourceNamespace", resourceNamespace, "ownerNamespace", resource.Namespace)
			}
		} else {
			logger.V(1).Info("Skipping owner reference for cluster-scoped resource", "resource", groupVersionKind.Kind+"/"+obj.GetName())
		}

		if canSetOwnerRef {
			if err := controllerutil.SetControllerReference(resource, obj, r.Scheme); err != nil {
				applyErrors = append(applyErrors, fmt.Errorf("setting owner ref on %s %s/%s: %w", groupVersionKind.Kind, obj.GetNamespace(), obj.GetName(), err))
				continue
			}
		}

		logger.V(1).Info("Applying resource", "kind", groupVersionKind.Kind, "name", obj.GetName(), "namespace", obj.GetNamespace(), "manifest", manifestName)
		if err := r.Patch(ctx, obj, client.Apply, client.FieldOwner(v1alpha1.FieldManager), client.ForceOwnership); err != nil {
			// A resource whose CRD is not registered yet during the imperative
			// bootstrap (e.g. a chart-bundled ServiceMonitor in the Gitea/HA
			// manifests, before the Prometheus-Operator CRDs arrive with the
			// GitOps observability stack) must NOT fail the whole install: that
			// would wedge the reconciler forever (GiteaReady never flips, so
			// Crossplane/GitOps stay blocked). These monitoring CRs are also
			// shipped by the kube-prometheus package, which ArgoCD applies once
			// the CRDs exist — so skip-with-warning here is safe and idempotent.
			if meta.IsNoMatchError(err) {
				logger.Info("Skipping resource whose CRD is not installed yet during bootstrap; GitOps will reconcile it later",
					"kind", groupVersionKind.Kind, "name", obj.GetName(), "manifest", manifestName)
				continue
			}
			applyErrors = append(applyErrors, fmt.Errorf("applying %s %s in namespace %s: %w", groupVersionKind.Kind, obj.GetName(), obj.GetNamespace(), err))
		}
	}

	if len(applyErrors) > 0 {
		return fmt.Errorf("encountered %d errors applying %s manifest: %v", len(applyErrors), manifestName, applyErrors)
	}

	return nil
}
