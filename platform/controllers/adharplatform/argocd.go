package adharplatform

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// NOTE: This embeds files from the 'platform/controllers/adharplatform/resources/argocd' directory.
// This directory is populated by the 'make embedded-resources' target, which runs generation scripts.
//
//go:embed resources/argocd
var argoCDFS embed.FS

func RawArgocdInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	filePath := config.FilePath
	if filePath == "" {
		// Default to "install.yaml" if no specific file path is provided in the customization.
		// This assumes "install.yaml" is the main manifest in the embedded 'resources/argocd' directory.
		filePath = "install.yaml"
	}
	// argoCDFS embeds the 'resources/argocd' directory. Files within this directory (e.g., install.yaml)
	// are at the root of the argoCDFS.
	// filePath (e.g., "install.yaml") is expected to be the direct name of the file in argoCDFS.
	// The fsRootPrefix for BuildCustomizedManifests should be "." to indicate the root of argoCDFS.
	return k8s.BuildCustomizedManifests(filePath, ".", argoCDFS, scheme, templateData)
}

func (r *AdharPlatformReconciler) ReconcileArgo(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Starting ArgoCD reconciliation")

	// ArgoCD will be installed using direct manifest application

	// Apply install.yaml
	logger.Info("Applying ArgoCD install manifest")
	argocdManifestPath := "resources/argocd/install.yaml"
	manifestBytes, err := argoCDFS.ReadFile(argocdManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read ArgoCD install manifest", "path", argocdManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading argocd manifest %s: %w", argocdManifestPath, err)
	}

	if err := r.applyManifest(ctx, manifestBytes, resource, "ArgoCD install"); err != nil {
		logger.Error(err, "Failed to apply ArgoCD install manifest")
		return ctrl.Result{}, err
	}
	logger.Info("Successfully applied ArgoCD install manifest")

	// Apply post-install.yaml for ArgoCD
	logger.Info("Applying ArgoCD post-install manifest")
	argocdPostInstallPath := "resources/argocd/post-install.yaml"
	postInstallBytes, err := argoCDFS.ReadFile(argocdPostInstallPath)
	if err != nil {
		// post-install is optional (may not exist in embedded resources)
		if errors.Is(err, fs.ErrNotExist) {
			logger.V(1).Info("ArgoCD post-install manifest not found, skipping", "path", argocdPostInstallPath)
		} else {
			logger.Error(err, "Failed to read ArgoCD post-install manifest", "path", argocdPostInstallPath)
			return ctrl.Result{}, fmt.Errorf("reading argocd post-install manifest %s: %w", argocdPostInstallPath, err)
		}
	} else {
		if err := r.applyManifest(ctx, postInstallBytes, resource, "ArgoCD post-install"); err != nil {
			logger.Error(err, "Failed to apply ArgoCD post-install manifest")
			return ctrl.Result{}, err
		}
		logger.Info("Successfully applied ArgoCD post-install manifest")
	}

	resource.Status.ArgoCD.Available = true
	logger.Info("ArgoCD reconciliation completed successfully")
	return ctrl.Result{}, nil
}
