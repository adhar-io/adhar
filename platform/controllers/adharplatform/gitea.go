package adharplatform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"adhar-io/adhar/api/v1alpha1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *AdharPlatformReconciler) ReconcileGitea(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Gitea core package")

	giteaManifestPath := "hack/gitea/install.yaml"
	manifestBytes, err := os.ReadFile(giteaManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read Gitea install manifest", "path", giteaManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading gitea manifest %s: %w", giteaManifestPath, err)
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
			logger.Error(err, "Failed to decode object from Gitea manifest")
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
		logger.V(1).Info("Checking owner reference for Gitea object with fresh owner data",
			"kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(),
			"ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID, "ownerDeletionTimestamp", freshAdharPlatform.ObjectMeta.DeletionTimestamp)

		if freshAdharPlatform.ObjectMeta.DeletionTimestamp.IsZero() {
			// Add logging before attempting to set the reference
			logger.V(1).Info("Owner is not being deleted, attempting to set owner reference", "targetKind", obj.GetKind(), "targetName", obj.GetName())
			// Log owner details just before setting reference
			logger.V(1).Info("Owner details before SetControllerReference", "ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID)
			if err := controllerutil.SetControllerReference(freshAdharPlatform, obj, r.Scheme); err != nil { // Use freshAdharPlatform here
				logger.Error(err, "Failed to set controller reference on Gitea object", "kind", obj.GetKind(), "name", obj.GetName())
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
			logger.Error(err, "Failed to apply Gitea object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
			applyErrors = append(applyErrors, fmt.Errorf("applying %s/%s: %w", obj.GetKind(), obj.GetName(), err))
			continue
		}
		logger.V(1).Info("Applied Gitea object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
	}

	if len(applyErrors) > 0 {
		combinedErr := fmt.Errorf("encountered %d errors applying gitea manifest: %v", len(applyErrors), applyErrors)
		logger.Error(combinedErr, "Failed to apply all Gitea resources")
		return ctrl.Result{}, combinedErr
	}

	logger.Info("Successfully reconciled Gitea core package")
	return ctrl.Result{}, nil
}

// giteaInternalBaseUrl returns the internal URL for Gitea, used for in-cluster communication (e.g., by ArgoCD).
// The URL format depends on whether path-based routing is used.
func giteaInternalBaseUrl(config v1alpha1.BuildCustomizationSpec) string {
	// Define a template for Gitea URL. This might be better placed in a constants or utils package.
	// For now, let's assume a structure like: <protocol>://<subdomain><host>:<port><path>
	// Example: http://gitea.example.com:8080/
	// Example with path routing: http://example.com:8080/gitea
	const giteaURLTemplate = "%s://%s%s:%s%s"

	if config.UsePathRouting {
		return fmt.Sprintf(giteaURLTemplate, config.Protocol, "", config.Host, config.Port, "/gitea")
	}
	return fmt.Sprintf(giteaURLTemplate, config.Protocol, "gitea.", config.Host, config.Port, "")
}
