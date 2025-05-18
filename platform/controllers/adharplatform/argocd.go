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

// NOTE: This embeds files from the 'platform/controllers/adharplatform/resources/argo-cd' directory.
// This directory is populated by the 'make embedded-resources' target, which runs generation scripts.
//
//go:embed resources/argo-cd
var argoCDFS embed.FS

func RawArgocdInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	// argoCDFS embeds the 'resources/argo-cd' directory. Files within this directory (e.g., install.yaml)
	// are at the root of the argoCDFS.
	// config.FilePath (e.g., "install.yaml") is expected to be the direct name of the file in argoCDFS.
	// The fsRootPrefix for BuildCustomizedManifests should be "." to indicate the root of argoCDFS.
	return k8s.BuildCustomizedManifests(config.FilePath, ".", argoCDFS, scheme, templateData)
}

func (r *AdharPlatformReconciler) ReconcileArgo(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	argocd := EmbeddedInstallation{
		name: "Argo CD",
		// resourcePath is relative to the root of argoCDFS.
		// Since argoCDFS directly contains files like 'install.yaml' from the embedded 'resources/argo-cd' directory,
		// the path to these manifests within the FS is effectively the root (".").
		resourcePath: ".",
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

	v, ok := resource.Spec.PackageConfigs.CorePackageCustomization[v1alpha1.ArgoCDPackageName]
	if ok {
		argocd.customization = v
	}

	if result, err := argocd.Install(ctx, resource, r.Client, r.Scheme, r.Config); err != nil {
		return result, err
	}

	resource.Status.ArgoCD.Available = true
	return ctrl.Result{}, nil
}
