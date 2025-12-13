package adharplatform

import (
	"context"
	"testing"
	"testing/fstest"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeKubeClient struct {
	client.Client
	apps    argov1alpha1.ApplicationList
	patched []client.Object
}

func (f *fakeKubeClient) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	if appList, ok := list.(*argov1alpha1.ApplicationList); ok {
		appList.Items = f.apps.Items
	}
	return nil
}

func (f *fakeKubeClient) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	f.patched = append(f.patched, obj.DeepCopyObject().(client.Object))
	return nil
}

type applyFriendlyClient struct {
	client.Client
}

func (a *applyFriendlyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	existing := obj.DeepCopyObject().(client.Object)
	if err := a.Client.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		return a.Client.Create(ctx, obj)
	}
	return a.Client.Update(ctx, obj)
}

func overrideArgoFS(t *testing.T, newFS fstest.MapFS) func() {
	t.Helper()
	previous := ArgoInstallFS
	ArgoInstallFS = newFS
	return func() {
		ArgoInstallFS = previous
	}
}

func TestRawArgocdInstallResources(t *testing.T) {
	restore := overrideArgoFS(t, mapArgoFSWithManifests(false))
	defer restore()

	resources, err := RawArgocdInstallResources(nil, v1alpha1.PackageCustomization{}, k8s.GetScheme())
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Contains(t, string(resources[0]), "ConfigMap")
}

func TestEmbeddedInstallationToObjects(t *testing.T) {
	restore := overrideArgoFS(t, mapArgoFSWithManifests(true))
	defer restore()

	inst := EmbeddedInstallation{
		resourcePath: argoResourcePath,
		resourceFS:   ArgoInstallFS,
	}

	objs, err := inst.installResources(k8s.GetScheme(), v1alpha1.BuildCustomizationSpec{})
	require.NoError(t, err)
	require.Len(t, objs, 2)
	assert.Equal(t, "ConfigMap", objs[0].GetObjectKind().GroupVersionKind().Kind)
}

func TestRequestArgoCDAppRefresh(t *testing.T) {
	sch := runtime.NewScheme()
	require.NoError(t, argov1alpha1.AddToScheme(sch))

	app := argov1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: globals.AdharSystemNamespace,
			Annotations: map[string]string{
				"existing": "value",
			},
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       argov1alpha1.ApplicationSchemaGroupVersionKind.Kind,
			APIVersion: argov1alpha1.ApplicationSchemaGroupVersionKind.GroupVersion().String(),
		},
	}
	fc := &fakeKubeClient{
		apps: argov1alpha1.ApplicationList{Items: []argov1alpha1.Application{app}},
	}

	rec := &AdharPlatformReconciler{Client: fc}

	err := rec.requestArgoCDAppRefresh(context.Background())
	require.NoError(t, err)
	require.Len(t, fc.patched, 1)

	patched := fc.patched[0].GetAnnotations()
	assert.Equal(t, argoCDApplicationAnnotationValueRefreshNormal, patched[argoCDApplicationAnnotationKeyRefresh])
}

func TestReconcileArgoSetsStatus(t *testing.T) {
	restore := overrideArgoFS(t, mapArgoFSWithManifests(true))
	defer restore()

	sch := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))

	platform := &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-platform",
			Namespace: "default",
		},
	}

	install := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argo-install", Namespace: "default"}}
	post := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argo-post", Namespace: "default"}}

	baseClient := fake.NewClientBuilder().WithScheme(sch).WithObjects(platform, install, post).Build()
	cli := &applyFriendlyClient{Client: baseClient}
	rec := &AdharPlatformReconciler{
		Client: cli,
		Scheme: sch,
	}

	_, err := rec.ReconcileArgo(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(platform)}, platform)
	require.NoError(t, err)

	assert.True(t, platform.Status.ArgoCD.Available)
}

func mapArgoFSWithManifests(includePost bool) fstest.MapFS {
	fs := fstest.MapFS{
		"resources/argocd/install.yaml": &fstest.MapFile{
			Data: []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-install
  namespace: default
`),
		},
	}
	if includePost {
		fs["resources/argocd/post-install.yaml"] = &fstest.MapFile{
			Data: []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-post
  namespace: default
`),
		}
	}
	return fs
}
