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

//go:embed resources/argocd/*
var installArgoFS embed.FS

func RawArgocdInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	return k8s.BuildCustomizedManifests(config.FilePath, "resources/argocd", installArgoFS, scheme, templateData)
}

func (r *AdharPlatformReconciler) ReconcileArgo(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	argocd := EmbeddedInstallation{
		name:         "Argo CD",
		resourcePath: "resources/argocd",
		resourceFS:   installArgoFS,
		namespace:    globals.ArgoCDNamespace,
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
