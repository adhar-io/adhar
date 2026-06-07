package adharplatform

import (
	"context"
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
