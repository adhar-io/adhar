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

// applyManifest applies a YAML manifest to the cluster
func (r *AdharPlatformReconciler) applyManifest(ctx context.Context, manifestBytes []byte, resource *v1alpha1.AdharPlatform, manifestName string) error {
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
		if !isClusterScoped {
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
			applyErrors = append(applyErrors, fmt.Errorf("applying %s %s in namespace %s: %w", groupVersionKind.Kind, obj.GetName(), obj.GetNamespace(), err))
		}
	}

	if len(applyErrors) > 0 {
		return fmt.Errorf("encountered %d errors applying %s manifest: %v", len(applyErrors), manifestName, applyErrors)
	}

	return nil
}
