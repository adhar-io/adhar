package custompackage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils"

	argocdapplication "github.com/cnoe-io/argocd-api/api/argo/application"
	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestReconciler(t *testing.T) *CustomPackageReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, argov1alpha1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	return &CustomPackageReconciler{
		Client:  fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:  scheme,
		TempDir: t.TempDir(),
		RepoMap: utils.NewRepoLock(),
	}
}

func writeTempManifest(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	return path
}

func TestReconcileCustomPackageCreatesApplication(t *testing.T) {
	rec := newTestReconciler(t)
	tmpDir := t.TempDir()
	appPath := writeTempManifest(t, tmpDir, "app.yaml", `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: sample-app
  namespace: argocd
spec:
  destination:
    namespace: default
    server: https://kubernetes.default.svc
  project: default
  source:
    repoURL: https://example.com/repo.git
    path: .
`)

	resource := &v1alpha1.CustomPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample",
			Namespace: "test",
		},
		Spec: v1alpha1.CustomPackageSpec{
			ArgoCD: v1alpha1.ArgoCDPackageSpec{
				ApplicationFile: appPath,
				Type:            argocdapplication.ApplicationKind,
			},
		},
	}

	res, err := rec.reconcileCustomPackage(context.Background(), resource)
	require.NoError(t, err)
	assert.Equal(t, requeueTime, res.RequeueAfter)

	created := &argov1alpha1.Application{}
	require.NoError(t, rec.Client.Get(context.Background(), client.ObjectKey{Name: "sample-app", Namespace: "argocd"}, created))
	assert.Equal(t, "sample-app", created.Labels[v1alpha1.PackageNameLabelKey])
	assert.Equal(t, v1alpha1.PackageTypeLabelCustom, created.Labels[v1alpha1.PackageTypeLabelKey])
}

func TestReconcileCustomPackageCreatesApplicationSet(t *testing.T) {
	rec := newTestReconciler(t)
	tmpDir := t.TempDir()
	appPath := writeTempManifest(t, tmpDir, "appset.yaml", `
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: sample-appset
  namespace: argocd
spec:
  generators:
  - list:
      elements:
      - cluster: default
        url: https://kubernetes.default.svc
  template:
    spec:
      destination:
        namespace: default
        server: https://kubernetes.default.svc
      source:
        repoURL: https://example.com/repo.git
        path: .
      project: default
`)

	resource := &v1alpha1.CustomPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample-appset",
			Namespace: "test",
		},
		Spec: v1alpha1.CustomPackageSpec{
			ArgoCD: v1alpha1.ArgoCDPackageSpec{
				ApplicationFile: appPath,
				Type:            argocdapplication.ApplicationSetKind,
			},
		},
	}

	res, err := rec.reconcileCustomPackage(context.Background(), resource)
	require.NoError(t, err)
	assert.Equal(t, requeueTime, res.RequeueAfter)

	created := &argov1alpha1.ApplicationSet{}
	require.NoError(t, rec.Client.Get(context.Background(), client.ObjectKey{Name: "sample-appset", Namespace: "argocd"}, created))
	assert.Equal(t, "sample-appset", created.Labels[v1alpha1.PackageNameLabelKey])
	assert.Equal(t, v1alpha1.PackageTypeLabelCustom, created.Labels[v1alpha1.PackageTypeLabelKey])
}

func TestReconcileHelmValueObject_NoChangesWhenNoRepos(t *testing.T) {
	rec := newTestReconciler(t)
	source := &argov1alpha1.ApplicationSource{
		Helm: &argov1alpha1.ApplicationSourceHelm{
			ValuesObject: &runtime.RawExtension{
				Raw: []byte(`{"repoURLGit":"https://example.com/repo.git","nested":{"path":"./charts"}}`),
			},
		},
	}

	res, err := rec.reconcileHelmValueObject(context.Background(), source, &v1alpha1.CustomPackage{}, "demo")
	require.NoError(t, err)
	assert.Equal(t, requeueTime, res.RequeueAfter)
	assert.JSONEq(t, `{"repoURLGit":"https://example.com/repo.git","nested":{"path":"./charts"}}`, string(source.Helm.ValuesObject.Raw))
}
