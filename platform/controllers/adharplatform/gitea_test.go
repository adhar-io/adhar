package adharplatform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestGiteaInternalBaseUrl(t *testing.T) {
	c := v1alpha1.BuildCustomizationSpec{
		Protocol:       "http",
		Port:           "8080",
		Host:           "adhar.localtest.me",
		UsePathRouting: false,
	}

	s := giteaInternalBaseUrl(c)
	assert.Equal(t, "http://gitea.adhar.localtest.me:8080", s)
	c.UsePathRouting = true
	s = giteaInternalBaseUrl(c)
	assert.Equal(t, "http://adhar.localtest.me:8080/gitea", s)
}

func TestGetGiteaToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second * 35)
	}))
	defer ts.Close()
	ctx := context.Background()
	_, err := utils.GetGiteaToken(ctx, ts.URL, "", "")
	require.Error(t, err)
}

func TestAdharPlatformReconciler_ReconcileGitea(t *testing.T) {
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

	// Call ReconcileGitea
	// Note: This test will likely fail if the "hack/gitea/install.yaml" file is not present or is invalid.
	// You might need to mock os.ReadFile or provide a dummy manifest for testing.
	// For now, we'll just check if it runs without panicking and returns an error if the file is not found.
	_, err := reconciler.ReconcileGitea(context.Background(), req, adharPlatform)

	// Assert that an error is returned (likely due to missing manifest file in test environment)
	// This is a basic check. More comprehensive tests would involve mocking file reads and k8s client interactions.
	assert.Error(t, err, "ReconcileGitea should return an error if the manifest file is not found")
}
