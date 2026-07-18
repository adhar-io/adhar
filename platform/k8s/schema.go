package k8s

import (
	"adhar-io/adhar/api/v1alpha1"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GetScheme returns a runtime.Scheme registered with the core Kubernetes API
// groups plus the ArgoCD and Adhar v1alpha1 types used by the platform.
func GetScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	schemeBuilder := runtime.NewSchemeBuilder(
		admissionregistrationv1.AddToScheme,
		apiextensionsv1.AddToScheme,
		appsv1.AddToScheme,
		autoscalingv2.AddToScheme,
		batchv1.AddToScheme,
		corev1.AddToScheme,
		networkingv1.AddToScheme,
		policyv1.AddToScheme,
		rbacv1.AddToScheme,
		argov1alpha1.AddToScheme,
		v1alpha1.AddToScheme,
	)
	schemeBuilder.AddToScheme(scheme)
	return scheme
}
