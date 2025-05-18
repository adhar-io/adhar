package adharplatform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
)

func (r *AdharPlatformReconciler) ReconcileCilium(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Cilium core package")

	ciliumManifestPath := "hack/cilium/install.yaml"
	manifestBytes, err := os.ReadFile(ciliumManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read Cilium install manifest", "path", ciliumManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading cilium manifest %s: %w", ciliumManifestPath, err)
	}

	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifestBytes), 100)
	var applyErrors []error

	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Error(err, "Failed to decode object from Cilium manifest")
			applyErrors = append(applyErrors, fmt.Errorf("decoding object: %w", err))
			continue
		}

		if obj.Object == nil {
			continue
		}

		// Fetch a fresh copy of the AdharPlatform resource to ensure we have the latest state
		freshAdharPlatform := &v1alpha1.AdharPlatform{}
		if err := r.Get(ctx, req.NamespacedName, freshAdharPlatform); err != nil {
			logger.Error(err, "Failed to get fresh AdharPlatform resource before setting owner ref", "kind", obj.GetKind(), "name", obj.GetName())
			applyErrors = append(applyErrors, fmt.Errorf("getting fresh owner for %s/%s: %w", obj.GetKind(), obj.GetName(), err))
			continue
		}

		// Log the state before attempting to set owner reference using the fresh object
		logger.V(1).Info("Checking owner reference for Cilium object with fresh owner data",
			"kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(),
			"ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID, "ownerDeletionTimestamp", freshAdharPlatform.ObjectMeta.DeletionTimestamp)

		if freshAdharPlatform.ObjectMeta.DeletionTimestamp.IsZero() {
			// Add logging before attempting to set the reference
			logger.V(1).Info("Owner is not being deleted, attempting to set owner reference", "targetKind", obj.GetKind(), "targetName", obj.GetName())
			// Log owner details just before setting reference
			logger.V(1).Info("Owner details before SetControllerReference", "ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID)
			if err := controllerutil.SetControllerReference(freshAdharPlatform, obj, r.Scheme); err != nil { // Use freshAdharPlatform here
				logger.Error(err, "Failed to set controller reference on Cilium object", "kind", obj.GetKind(), "name", obj.GetName())
				applyErrors = append(applyErrors, fmt.Errorf("setting owner ref on %s/%s: %w", obj.GetKind(), obj.GetName(), err))
				continue // Skip applying this object if owner ref fails
			}
			// Add logging after successfully setting the reference
			logger.V(1).Info("Successfully set owner reference", "targetKind", obj.GetKind(), "targetName", obj.GetName())
		} else {
			logger.V(1).Info("Owner resource is being deleted, skipping owner reference setting", "owner", freshAdharPlatform.Name, "targetKind", obj.GetKind(), "targetName", obj.GetName())
		}

		// Apply the object using Server-Side Apply
		patch := client.Apply
		opts := []client.PatchOption{client.ForceOwnership, client.FieldOwner(v1alpha1.FieldManager)}
		err = r.Patch(ctx, obj, patch, opts...)
		if err != nil {
			logger.Error(err, "Failed to apply Cilium object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
			applyErrors = append(applyErrors, fmt.Errorf("applying %s/%s: %w", obj.GetKind(), obj.GetName(), err))
			continue
		}
		logger.V(1).Info("Applied Cilium object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
	}

	if len(applyErrors) > 0 {
		combinedErr := fmt.Errorf("encountered %d errors applying cilium manifest: %v", len(applyErrors), applyErrors)
		logger.Error(combinedErr, "Failed to apply all Cilium resources")
		return ctrl.Result{}, combinedErr
	}

	logger.Info("Successfully reconciled Cilium core package")
	return ctrl.Result{}, nil
}
