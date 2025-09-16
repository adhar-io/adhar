package adharplatform

import (
	"context"
	"embed"
	"fmt"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed resources/gitea
var giteaFS embed.FS // Added embedded FS

// RawGiteaInstallResources loads and processes the Gitea installation manifests.
func RawGiteaInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	// If config.FilePath is empty or not set, default to "install.yaml"
	filePath := config.FilePath
	if filePath == "" {
		filePath = "install.yaml"
	}
	// The fsRootPrefix for k8s.BuildCustomizedManifests should be "." if giteaFS embeds the directory containing install.yaml directly.
	// Since //go:embed resources/gitea embeds the 'gitea' directory, and if 'install.yaml' is directly inside it,
	// then the path for BuildCustomizedManifests is just "install.yaml".
	return k8s.BuildCustomizedManifests(filePath, ".", giteaFS, scheme, templateData) // Ensure k8s.BuildCustomizedManifests is correctly implemented and imported
}

func (r *AdharPlatformReconciler) ReconcileGitea(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Gitea core package")

	// Apply install.yaml
	giteaManifestPath := "resources/gitea/install.yaml"
	manifestBytes, err := giteaFS.ReadFile(giteaManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read Gitea install manifest", "path", giteaManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading gitea manifest %s: %w", giteaManifestPath, err)
	}

	if err := r.applyManifest(ctx, manifestBytes, resource, "Gitea install"); err != nil {
		logger.Error(err, "Failed to apply Gitea install manifest")
		return ctrl.Result{}, err
	}

	// Apply post-install.yaml
	giteaPostInstallPath := "resources/gitea/post-install.yaml"
	postInstallBytes, err := giteaFS.ReadFile(giteaPostInstallPath)
	if err != nil {
		logger.Error(err, "Failed to read Gitea post-install manifest", "path", giteaPostInstallPath)
		return ctrl.Result{}, fmt.Errorf("reading gitea post-install manifest %s: %w", giteaPostInstallPath, err)
	}

	if err := r.applyManifest(ctx, postInstallBytes, resource, "Gitea post-install"); err != nil {
		logger.Error(err, "Failed to apply Gitea post-install manifest")
		return ctrl.Result{}, err
	}

	resource.Status.Gitea.Available = true
	logger.Info("Gitea reconciliation completed successfully")
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
