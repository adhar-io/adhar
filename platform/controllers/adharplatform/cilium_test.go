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

func TestAdharPlatformReconciler_ReconcileCilium(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}

	// Create a fake client with an AdharPlatform object
	adharPlatform := &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-platform",
			Namespace: "default",
			UID:       "test-uid",
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(adharPlatform).Build()

	// Create a reconciler
	reconciler := &AdharPlatformReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Create a request
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-platform",
			Namespace: "default",
		},
	}

	// Call ReconcileCilium
	// Note: This test will likely fail if the "hack/cilium/install.yaml" file is not present or is invalid.
	// You might need to mock os.ReadFile or provide a dummy manifest for testing.
	// For now, we'll just check if it runs without panicking and returns an error if the file is not found.
	_, err := reconciler.ReconcileCilium(context.Background(), req, adharPlatform)

	// Assert that an error is returned (likely due to missing manifest file in test environment)
	// This is a basic check. More comprehensive tests would involve mocking file reads and k8s client interactions.
	assert.Error(t, err, "ReconcileCilium should return an error if the manifest file is not found")
}
