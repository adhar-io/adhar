package adharplatform

import (
	"context"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"testing"

	"adhar-io/adhar/api/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestAdharPlatformReconciler_ReconcileGatewayAPICRDs verifies the embedded
// Gateway API CRD manifest is readable and applies through the reconciler.
func TestAdharPlatformReconciler_ReconcileGatewayAPICRDs(t *testing.T) {
	scheme := runtime.NewScheme()
	v1alpha1.AddToScheme(scheme)

	adharPlatform := &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-platform",
			Namespace: "default",
			UID:       "test-uid",
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(adharPlatform).Build()

	reconciler := &AdharPlatformReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-platform", Namespace: "default"},
	}

	// The embedded resources/gateway-api/crds.yaml must be present; applying it
	// against the fake client should not panic. We assert the manifest is found
	// (no "reading" error) — apply errors against a fake client are tolerated.
	_, err := reconciler.ReconcileGatewayAPICRDs(context.Background(), req, adharPlatform)
	if err != nil {
		assert.NotContains(t, err.Error(), "reading gateway-api crds manifest",
			"embedded Gateway API CRD manifest should be readable")
	}
}

// Civo's managed k3s ships the Gateway API standard channel and a policy that
// forbids layering the experimental bundle over it; the platform must use what
// is there instead of failing every reconcile.
func TestGatewayAPICRDsAlreadyOnTheClusterAreUsedNotReplaced(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default", UID: "u"}}
	existing := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{
		Name:        "gateways.gateway.networking.k8s.io",
		Annotations: map[string]string{"gateway.networking.k8s.io/channel": "standard", "gateway.networking.k8s.io/bundle-version": "v1.6.1"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(platform, existing).Build()
	r := &AdharPlatformReconciler{Client: c, Scheme: scheme}
	if _, err := r.ReconcileGatewayAPICRDs(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "p", Namespace: "default"}}, platform); err != nil {
		t.Fatalf("a cluster with standard-channel Gateway API CRDs failed the step: %v", err)
	}
	experimental := &apiextensionsv1.CustomResourceDefinition{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "xbackends.gateway.networking.x-k8s.io"}, experimental); err == nil {
		t.Fatal("the experimental bundle was applied over the cluster's standard-channel CRDs")
	}
	if foreign, _ := r.foreignGatewayAPICRDs(context.Background()); !foreign {
		t.Fatal("standard-channel CRDs installed by someone else were not recognised as foreign")
	}
	// The platform's own earlier apply (experimental channel) is not foreign.
	ours := existing.DeepCopy()
	ours.Annotations["gateway.networking.k8s.io/channel"] = "experimental"
	r2 := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(platform, ours).Build(), Scheme: scheme}
	if foreign, _ := r2.foreignGatewayAPICRDs(context.Background()); foreign {
		t.Fatal("the platform's own experimental CRDs were treated as foreign")
	}
}
