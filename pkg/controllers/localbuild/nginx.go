package localbuild

import (
	"context"
	"embed"

	"github.com/adhar-io/adhar/api/v1alpha1"
	"github.com/adhar-io/adhar/globals"
	"github.com/adhar-io/adhar/pkg/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
)

//go:embed resources/nginx/k8s/*
var installNginxFS embed.FS

func RawNginxInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	return k8s.BuildCustomizedManifests(config.FilePath, "resources/nginx/k8s", installNginxFS, scheme, templateData)
}

func (r *LocalbuildReconciler) ReconcileNginx(ctx context.Context, req ctrl.Request, resource *v1alpha1.Localbuild) (ctrl.Result, error) {
	nginx := EmbeddedInstallation{
		name:         "Nginx",
		resourcePath: "resources/nginx/k8s",
		resourceFS:   installNginxFS,
		namespace:    globals.NginxNamespace,
		monitoredResources: map[string]schema.GroupVersionKind{
			"ingress-nginx-controller": {
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
		},
	}

	v, ok := resource.Spec.PackageConfigs.CorePackageCustomization[v1alpha1.IngressNginxPackageName]
	if ok {
		nginx.customization = v
	}

	if result, err := nginx.Install(ctx, resource, r.Client, r.Scheme, r.Config); err != nil {
		return result, err
	}

	resource.Status.Nginx.Available = true
	return ctrl.Result{}, nil
}
