package adharplatform

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/runtime"
)

//go:embed resources/nginx/*
var installNginxFS embed.FS

func RawNginxInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	// If config.FilePath is empty or not set, default to "install.yaml"
	filePath := config.FilePath
	if filePath == "" {
		filePath = "install.yaml"
	}
	// The fsRootPrefix for k8s.BuildCustomizedManifests should be "." if installNginxFS embeds the directory containing install.yaml directly.
	// Since //go:embed resources/nginx/* embeds the contents of the 'nginx' directory, and if 'install.yaml' is directly inside it,
	// then the path for BuildCustomizedManifests is just "install.yaml".
	return k8s.BuildCustomizedManifests(filePath, ".", installNginxFS, scheme, templateData)
}

func (r *AdharPlatformReconciler) ReconcileNginx(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Nginx core package")

	nginxManifestPath := "resources/nginx/install.yaml"              // Corrected path for embedded resource
	manifestBytes, err := installNginxFS.ReadFile(nginxManifestPath) // Read from the correct path in nginxFS
	if err != nil {
		logger.Error(err, "Failed to read Nginx install manifest", "path", nginxManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading nginx manifest %s: %w", nginxManifestPath, err)
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
			logger.Error(err, "Failed to decode object from Nginx manifest")
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
		logger.V(1).Info("Checking owner reference for Nginx object with fresh owner data",
			"kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(),
			"ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID, "ownerDeletionTimestamp", freshAdharPlatform.ObjectMeta.DeletionTimestamp)

		// Determine if the resource is cluster-scoped
		groupVersionKind := obj.GetObjectKind().GroupVersionKind()
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
				{Group: "networking.k8s.io", Kind: "IngressClass"}:                              true,
			}
			if knownClusterScopedKinds[groupVersionKind.GroupKind()] {
				isClusterScoped = true
			}
			logger.V(1).Info("Could not determine scope from RESTMapper, falling back", "gvk", groupVersionKind, "error", err, "assumed clusterScoped", isClusterScoped)
		}

		canSetOwnerRef := false
		if freshAdharPlatform.ObjectMeta.DeletionTimestamp.IsZero() {
			if !isClusterScoped {
				resourceNamespace := obj.GetNamespace()
				if resourceNamespace == "" {
					resourceNamespace = freshAdharPlatform.Namespace
					obj.SetNamespace(freshAdharPlatform.Namespace)
				}

				if resourceNamespace == freshAdharPlatform.Namespace {
					canSetOwnerRef = true
				} else {
					logger.V(1).Info("Skipping owner reference for resource in different namespace",
						"resource", groupVersionKind.Kind+"/"+obj.GetName(), "resourceNamespace", resourceNamespace, "ownerNamespace", freshAdharPlatform.Namespace)
				}
			} else {
				logger.V(1).Info("Skipping owner reference for cluster-scoped resource", "resource", groupVersionKind.Kind+"/"+obj.GetName())
			}
		} else {
			logger.V(1).Info("Owner resource is being deleted, skipping owner reference setting", "owner", freshAdharPlatform.Name, "targetKind", obj.GetKind(), "targetName", obj.GetName())
		}

		if canSetOwnerRef {
			// Add logging before attempting to set the reference
			logger.V(1).Info("Owner is not being deleted, attempting to set owner reference", "targetKind", obj.GetKind(), "targetName", obj.GetName())
			// Log owner details just before setting reference
			logger.V(1).Info("Owner details before SetControllerReference", "ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID)
			if err := controllerutil.SetControllerReference(freshAdharPlatform, obj, r.Scheme); err != nil { // Use freshAdharPlatform here
				logger.Error(err, "Failed to set controller reference on Nginx object", "kind", obj.GetKind(), "name", obj.GetName())
				applyErrors = append(applyErrors, fmt.Errorf("setting owner ref on %s/%s: %w", obj.GetKind(), obj.GetName(), err))
				continue // Skip applying this object if owner ref fails
			}
			// Add logging after successfully setting the reference
			logger.V(1).Info("Successfully set owner reference", "targetKind", obj.GetKind(), "targetName", obj.GetName())
		}

		// Apply the object using Server-Side Apply
		patch := client.Apply
		opts := []client.PatchOption{client.ForceOwnership, client.FieldOwner(v1alpha1.FieldManager)}
		err = r.Patch(ctx, obj, patch, opts...)
		if err != nil {
			logger.Error(err, "Failed to apply Nginx object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
			applyErrors = append(applyErrors, fmt.Errorf("applying %s/%s: %w", obj.GetKind(), obj.GetName(), err))
			continue
		}
		logger.V(1).Info("Applied Nginx object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
	}

	if len(applyErrors) > 0 {
		combinedErr := fmt.Errorf("encountered %d errors applying nginx manifest: %v", len(applyErrors), applyErrors)
		logger.Error(combinedErr, "Failed to apply all Nginx resources")
		return ctrl.Result{}, combinedErr
	}

	resource.Status.Nginx.Available = true
	logger.Info("Nginx reconciliation completed successfully")
	return ctrl.Result{}, nil
}
