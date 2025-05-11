package adharplatform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"adhar-io/adhar/api/v1alpha1"
)

func TestReconcileNginx(t *testing.T) {
	// Create a fake client and scheme
	fakeClient := fake.NewClientBuilder().Build()
	resource := &v1alpha1.AdharPlatform{
		Spec: v1alpha1.AdharPlatformSpec{},
		Status: v1alpha1.AdharPlatformStatus{
			Nginx: v1alpha1.NginxStatus{},
		},
	}

	reconciler := &AdharPlatformReconciler{
		Client: fakeClient,
	}

	// Create a reconcile request
	req := reconcile.Request{}

	// Call the ReconcileNginx function
	result, err := reconciler.ReconcileNginx(context.TODO(), req, resource)

	// Assert no error and expected result
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
	assert.True(t, resource.Status.Nginx.Available, "Nginx should be marked as available")
}
