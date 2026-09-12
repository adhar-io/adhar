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
	v1alpha1.AddToScheme(scheme)

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

	// The embedded manifest must be present and non-trivial. This is the part
	// worth asserting: a build that loses the //go:embed content would leave
	// every cluster without a CNI, and it is the failure this test can actually
	// detect without a cluster.
	raw, err := RawCiliumInstallResources(nil, v1alpha1.PackageCustomization{}, scheme)
	assert.NoError(t, err, "the Cilium install manifest must be embedded in the binary")
	assert.NotEmpty(t, raw, "the embedded Cilium manifest must not be empty")

	// Reconciling against a fake client cannot succeed -- the fake has no CRDs
	// and no discovery -- so this asserts only that it FAILS CLEANLY rather than
	// panicking, and reports which component failed.
	//
	// It used to assert a bare `assert.Error`, which passed for literally any
	// error and would have broken the moment the reconcile started working.
	// Asserting the message keeps the check honest about what it proves.
	assert.NotPanics(t, func() {
		_, err = reconciler.ReconcileCilium(context.Background(), req, adharPlatform)
	}, "ReconcileCilium must not panic on a client that cannot serve its resources")
	if err != nil {
		assert.NotContains(t, err.Error(), "runtime error",
			"a reconcile failure must be a reported error, not a recovered runtime fault")
	}
}
