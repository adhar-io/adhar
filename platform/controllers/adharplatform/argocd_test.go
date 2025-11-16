package adharplatform

import (
	"context"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/utils/fs"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeKubeClient struct {
	mock.Mock
	client.Client
}

func (f *fakeKubeClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	args := f.Called(ctx, list, opts)
	return args.Error(0)
}

func (f *fakeKubeClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	args := f.Called(ctx, obj, patch, opts)
	return args.Error(0)
}

type testCase struct {
	err         error
	listApps    []argov1alpha1.Application
	annotations []map[string]string
}

func TestGetRawInstallResources(t *testing.T) {
	e := EmbeddedInstallation{
		resourceFS:   argoCDFS,
		resourcePath: "hack/argo-cd", // Path within the FS to the ArgoCD resources
	}
	resources, err := fs.ConvertFSToBytes(e.resourceFS, e.resourcePath,
		v1alpha1.BuildCustomizationSpec{
			Protocol:       "",
			Host:           "",
			Port:           "",
			UsePathRouting: false,
		},
	)
	if err != nil {
		t.Fatalf("GetRawInstallResources() error: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("GetRawInstallResources() resources len != 2, got %d", len(resources))
	}

	resourcePrefix := "# UCP ARGO INSTALL RESOURCES\n"
	checkPrefix := resources[1][0:len(resourcePrefix)]
	if resourcePrefix != string(checkPrefix) {
		t.Fatalf("GetRawInstallResources() expected 1 resource with prefix %q, got %q", resourcePrefix, checkPrefix)
	}
}

func TestGetK8sInstallResources(t *testing.T) {
	e := EmbeddedInstallation{
		resourceFS:   argoCDFS,
		resourcePath: "hack/argo-cd", // Path within the FS to the ArgoCD resources
	}
	objs, err := e.installResources(k8s.GetScheme(), v1alpha1.BuildCustomizationSpec{
		Protocol:       "",
		Host:           "",
		Port:           "",
		UsePathRouting: false,
	})
	if err != nil {
		t.Fatalf("GetK8sInstallResources() error: %v", err)
	}

	if len(objs) != 61 {
		t.Fatalf("Expected 61 Argo Install Resources, got: %d", len(objs))
	}
}

func TestArgoCDAppAnnotation(t *testing.T) {
	ctx := context.Background()

	cases := []testCase{
		{
			err: nil,
			listApps: []argov1alpha1.Application{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       argov1alpha1.ApplicationSchemaGroupVersionKind.Kind,
						APIVersion: argov1alpha1.ApplicationSchemaGroupVersionKind.GroupVersion().String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "nil-annotation",
						Namespace: "argocd",
					},
				},
			},
			annotations: []map[string]string{
				{
					argoCDApplicationAnnotationKeyRefresh: argoCDApplicationAnnotationValueRefreshNormal,
				},
			},
		},
		{
			err: nil,
			listApps: []argov1alpha1.Application{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       argov1alpha1.ApplicationSchemaGroupVersionKind.Kind,
						APIVersion: argov1alpha1.ApplicationSchemaGroupVersionKind.GroupVersion().String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "existing-annotation",
						Namespace: "argocd",
						Annotations: map[string]string{
							"test": "value",
						},
					},
				},
			},
			annotations: []map[string]string{
				{
					"test":                                "value",
					argoCDApplicationAnnotationKeyRefresh: argoCDApplicationAnnotationValueRefreshNormal,
				},
			},
		},
		{
			err: nil,
			listApps: []argov1alpha1.Application{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       argov1alpha1.ApplicationSchemaGroupVersionKind.Kind,
						APIVersion: argov1alpha1.ApplicationSchemaGroupVersionKind.GroupVersion().String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "owned-by-appset",
						Namespace: "argocd",
						Annotations: map[string]string{
							"test": "value",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "ApplicationSet",
							},
						},
					},
				},
			},
			annotations: nil,
		},
		{
			err: nil,
			listApps: []argov1alpha1.Application{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       argov1alpha1.ApplicationSchemaGroupVersionKind.Kind,
						APIVersion: argov1alpha1.ApplicationSchemaGroupVersionKind.GroupVersion().String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "owned-by-non-appset",
						Namespace: "argocd",
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "Something",
							},
						},
					},
				},
			},
			annotations: []map[string]string{
				{
					argoCDApplicationAnnotationKeyRefresh: argoCDApplicationAnnotationValueRefreshNormal,
				},
			},
		},
	}

	for i := range cases {
		c := cases[i]
		fClient := new(fakeKubeClient)
		fClient.On("List", ctx, mock.Anything, []client.ListOption{client.InNamespace(globals.AdharSystemNamespace)}).
			Run(func(args mock.Arguments) {
				apps := args.Get(1).(*argov1alpha1.ApplicationList)
				apps.Items = c.listApps
			}).Return(c.err)
		for j := range c.annotations {
			app := c.listApps[j]
			u := makeUnstructured(app.Name, app.Namespace, app.GroupVersionKind(), c.annotations[j])
			fClient.On("Patch", ctx, u, client.Apply, []client.PatchOption{client.FieldOwner(v1alpha1.FieldManager)}).Return(nil)
		}
		rec := AdharPlatformReconciler{
			Client: fClient,
		}
		err := rec.requestArgoCDAppRefresh(ctx)
		fClient.AssertExpectations(t)
		assert.NoError(t, err)
	}
}

func makeUnstructured(name, namespace string, gvk schema.GroupVersionKind, annotations map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAnnotations(annotations)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetGroupVersionKind(gvk)
	return u
}

func TestAdharPlatformReconciler_ReconcileArgo(t *testing.T) {
	ctx := context.Background()
	s := scheme.Scheme
	err := v1alpha1.AddToScheme(s)
	assert.NoError(t, err)

	// Mock EmbeddedInstallation.Install or ensure it's robustly tested elsewhere.
	// For this test, we'll assume Install succeeds and makes no specific client calls that need deep mocking here,
	// beyond what the fake client handles for status updates.

	// Create a fake client
	fakeClientBuilder := fake.NewClientBuilder().WithScheme(s)

	// AdharPlatform resource
	adharPlatform := &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-platform",
			Namespace: "default",
		},
		Spec: v1alpha1.AdharPlatformSpec{
			PackageConfigs: v1alpha1.PackageConfigsSpec{ // Changed PackageConfigs to PackageConfigsSpec
				CorePackageCustomization: map[string]v1alpha1.PackageCustomization{
					v1alpha1.ArgoCDPackageName: {
						FilePath: "some/custom/path.yaml",
					},
				},
			},
		},
		Status: v1alpha1.AdharPlatformStatus{},
	}

	// Create the AdharPlatform resource in the fake client
	fc := fakeClientBuilder.WithObjects(adharPlatform).Build()

	// AdharPlatformReconciler
	reconciler := &AdharPlatformReconciler{
		Client: fc,
		Scheme: s,
		Config: v1alpha1.BuildCustomizationSpec{}, // Provide a default or test-specific config
		// Fill other required fields for AdharPlatformReconciler if necessary
	}

	req := ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(adharPlatform),
	}

	// --- Test Case: Successful Argo Reconciliation ---
	t.Run("Successful Argo Reconciliation", func(t *testing.T) {
		// If EmbeddedInstallation.Install needs specific mocks, set them up here.
		// For example, if it creates deployments, services, etc.

		result, err := reconciler.ReconcileArgo(ctx, req, adharPlatform.DeepCopy())
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify status update
		updatedPlatform := &v1alpha1.AdharPlatform{}
		err = fc.Get(ctx, client.ObjectKeyFromObject(adharPlatform), updatedPlatform)
		assert.NoError(t, err)
		assert.True(t, updatedPlatform.Status.ArgoCD.Available, "ArgoCD status should be available")

		// Reset status for next run if necessary, or use DeepCopy of initial object
		adharPlatform.Status.ArgoCD.Available = false
		err = fc.Status().Update(ctx, adharPlatform) // Reset status in fake client
		assert.NoError(t, err)
	})

	// --- Test Case: EmbeddedInstallation.Install returns error ---
	// This would require a way to make EmbeddedInstallation.Install fail,
	// possibly by mocking client calls within it or by having a test mode for EmbeddedInstallation.
	// For now, this case is skipped due to complexity without EmbeddedInstallation's source/mockability.
	t.Run("Failed Argo Reconciliation due to Install error", func(t *testing.T) {
		t.Skip("Skipping test for Install error: requires more complex mocking or EmbeddedInstallation modification")
		// Setup scenario where EmbeddedInstallation.Install would fail
		// ...
		// result, err := reconciler.ReconcileArgo(ctx, req, adharPlatform.DeepCopy())
		// assert.Error(t, err)
		// assert.Contains(t, err.Error(), "expected error from Install")
		// updatedPlatform := &v1alpha1.AdharPlatform{}
		// err = fc.Get(ctx, client.ObjectKeyFromObject(adharPlatform), updatedPlatform)
		// assert.NoError(t, err)
		// assert.False(t, updatedPlatform.Status.ArgoCD.Available, "ArgoCD status should not be available on failure")
	})

}
