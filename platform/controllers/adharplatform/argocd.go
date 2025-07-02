package adharplatform

import (
	"context"
	"embed"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
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
	argocd := EmbeddedInstallation{
		name: "Argo CD",
		// resourcePath is the path to the primary manifest file within resourceFS.
		// argoCDFS embeds the 'resources/argocd' directory. If 'install.yaml' is at the root of this FS,
		// then resourcePath should be "install.yaml".
		resourcePath: "install.yaml", // Changed from "."
		resourceFS:   argoCDFS,
		namespace:    globals.AdharSystemNamespace,
		monitoredResources: map[string]schema.GroupVersionKind{
			"argocd-server": {
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
			"argocd-repo-server": {
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
			"argocd-application-controller": {
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			},
		},
		skipReadinessCheck: true,
	}

	customization, ok := resource.Spec.PackageConfigs.CorePackageCustomization[v1alpha1.ArgoCDPackageName]
	if !ok {
		// Initialize with an empty struct if no specific customization is found.
		customization = v1alpha1.PackageCustomization{}
	}

	// Default FilePath to "install.yaml" if it's empty or explicitly ".".
	// This ensures that a specific file is targeted for reading from the embedded FS,
	// preventing an "is a directory" error if FilePath were problematic.
	if customization.FilePath == "" || customization.FilePath == "." {
		customization.FilePath = "install.yaml"
	}
	argocd.customization = customization

	if result, err := argocd.Install(ctx, resource, r.Client, r.Scheme, r.Config); err != nil {
		return result, err
	}

	resource.Status.ArgoCD.Available = true
	return ctrl.Result{}, nil
}
