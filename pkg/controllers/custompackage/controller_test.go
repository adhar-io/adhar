package custompackage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adhar-io/adhar/api/v1alpha1"
	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type testCase struct {
	expectedGitRepo        v1alpha1.GitRepository
	expectedApplicationSet argov1alpha1.ApplicationSet
	input                  v1alpha1.CustomPackage
}

func TestReconcileCustomPkg(t *testing.T) {
	s := k8sruntime.NewScheme()
	sb := k8sruntime.NewSchemeBuilder(
		v1.AddToScheme,
		argov1alpha1.AddToScheme,
		v1alpha1.AddToScheme,
	)
	sb.AddToScheme(s)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "resources"),
			"../localbuild/resources/argo/install.yaml",
		},
		ErrorIfCRDPathMissing: true,
		Scheme:                s,
		BinaryAssetsDirectory: filepath.Join("..", "..", "..", "bin", "k8s",
			fmt.Sprintf("1.29.1-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer testEnv.Stop()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: s,
	})
	require.NoError(t, err)

	ctx, ctxCancel := context.WithCancel(context.Background())
	stoppedCh := make(chan error)
	go func() {
		err := mgr.Start(ctx)
		stoppedCh <- err
	}()

	defer func() {
		ctxCancel()
		err := <-stoppedCh
		if err != nil {
			t.Errorf("Starting controller manager: %v", err)
			t.FailNow()
		}
	}()

	r := &Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("test-custompkg-controller"),
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)

	customPkgs := []v1alpha1.CustomPackage{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test1",
				Namespace: "test",
				UID:       "abc",
			},
			Spec: v1alpha1.CustomPackageSpec{
				Replicate:           true,
				GitServerURL:        "https://adhar.io",
				InternalGitServeURL: "http://internal.adhar.io",
				ArgoCD: v1alpha1.ArgoCDPackageSpec{
					ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/testDir/app.yaml"),
					Name:            "my-app",
					Namespace:       "argocd",
					Type:            "Application",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test2",
				Namespace: "test",
				UID:       "abc",
			},
			Spec: v1alpha1.CustomPackageSpec{
				Replicate:           false,
				GitServerURL:        "https://adhar.io",
				InternalGitServeURL: "http://adhar.io/internal",
				ArgoCD: v1alpha1.ArgoCDPackageSpec{
					ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/testDir2/exampleApp.yaml"),
					Name:            "guestbook",
					Namespace:       "argocd",
					Type:            "Application",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test3",
				Namespace: "test",
				UID:       "abc",
			},
			Spec: v1alpha1.CustomPackageSpec{
				Replicate:           true,
				GitServerURL:        "https://adhar.io",
				InternalGitServeURL: "http://internal.adhar.io",
				ArgoCD: v1alpha1.ArgoCDPackageSpec{
					ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/testDir/app2.yaml"),
					Name:            "my-app2",
					Namespace:       "argocd",
					Type:            "Application",
				},
			},
		},
	}

	for _, n := range []string{"argocd", "test"} {
		ns := v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: n,
			},
		}
		err = mgr.GetClient().Create(context.Background(), &ns)
		if err != nil {
			t.Fatalf("creating test ns: %v", err)
		}
	}

	for i := range customPkgs {
		_, err = r.reconcileCustomPackage(context.Background(), &customPkgs[i])
		if err != nil {
			t.Fatalf("reconciling custom packages %v", err)
		}
	}
	time.Sleep(1 * time.Second)
	// verify repo.
	c := mgr.GetClient()
	repo := v1alpha1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      localRepoName("my-app", "test/resources/customPackages/testDir/app1"),
			Namespace: "test",
		},
	}
	err = c.Get(context.Background(), client.ObjectKeyFromObject(&repo), &repo)
	if err != nil {
		t.Fatalf("getting my-app-app1 git repo %v", err)
	}

	p, _ := filepath.Abs("test/resources/customPackages/testDir/app1")
	expectedRepo := v1alpha1.GitRepository{
		Spec: v1alpha1.GitRepositorySpec{
			Source: v1alpha1.GitRepositorySource{
				Type: "local",
				Path: p,
			},
			Provider: v1alpha1.Provider{
				Name:             v1alpha1.GitProviderGitea,
				GitURL:           "https://adhar.io",
				InternalGitURL:   "http://internal.adhar.io",
				OrganizationName: v1alpha1.GiteaAdminUserName,
			},
		},
	}
	assert.Equal(t, repo.Spec, expectedRepo.Spec)
	ok := reflect.DeepEqual(repo.Spec, expectedRepo.Spec)
	assert.True(t, ok)

	tcs := []struct {
		name string
	}{
		{
			name: "my-app",
		},
		{
			name: "my-app2",
		},
		{
			name: "guestbook",
		},
	}

	for _, tc := range tcs {
		app := argov1alpha1.Application{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tc.name,
				Namespace: "argocd",
			},
		}
		err = c.Get(context.Background(), client.ObjectKeyFromObject(&app), &app)
		assert.NoError(t, err)

		if app.ObjectMeta.Labels == nil {
			t.Fatalf("labels not set")
		}

		_, ok := app.ObjectMeta.Labels[v1alpha1.PackageNameLabelKey]
		if !ok {
			t.Fatalf("label %s not set", v1alpha1.PackageTypeLabelKey)
		}

		_, ok = app.ObjectMeta.Labels[v1alpha1.PackageNameLabelKey]
		if !ok {
			t.Fatalf("label %s not set", v1alpha1.PackageNameLabelKey)
		}

		if app.Spec.Sources == nil {
			if strings.HasPrefix(app.Spec.Source.RepoURL, v1alpha1.ADHARURIScheme) {
				t.Fatalf("%s prefix should be removed", v1alpha1.ADHARURIScheme)
			}
			continue
		}
		for _, s := range app.Spec.Sources {
			if strings.HasPrefix(s.RepoURL, v1alpha1.ADHARURIScheme) {
				t.Fatalf("%s prefix should be removed", v1alpha1.ADHARURIScheme)
			}
		}

	}
}

func TestReconcileCustomPkgAppSet(t *testing.T) {
	s := k8sruntime.NewScheme()
	sb := k8sruntime.NewSchemeBuilder(
		v1.AddToScheme,
		argov1alpha1.AddToScheme,
		v1alpha1.AddToScheme,
	)
	sb.AddToScheme(s)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "resources"),
			"../localbuild/resources/argo/install.yaml",
		},
		ErrorIfCRDPathMissing: true,
		Scheme:                s,
		BinaryAssetsDirectory: filepath.Join("..", "..", "..", "bin", "k8s",
			fmt.Sprintf("1.29.1-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	cfg, err := testEnv.Start()
	assert.Nil(t, err)
	defer testEnv.Stop()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: s,
	})
	assert.Nil(t, err)

	ctx, ctxCancel := context.WithCancel(context.Background())
	stoppedCh := make(chan error)
	go func() {
		err := mgr.Start(ctx)
		stoppedCh <- err
	}()

	defer func() {
		ctxCancel()
		err := <-stoppedCh
		if err != nil {
			t.Errorf("Starting controller manager: %v", err)
			t.FailNow()
		}
	}()

	r := &Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("test-custompkg-controller"),
	}
	cwd, err := os.Getwd()
	assert.Nil(t, err)

	for _, n := range []string{"argocd", "test"} {
		ns := v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: n,
			},
		}
		err = mgr.GetClient().Create(context.Background(), &ns)
		assert.Nil(t, err)
	}

	cases := []testCase{
		{
			input: v1alpha1.CustomPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1",
					Namespace: "test",
					UID:       "abc",
				},
				Spec: v1alpha1.CustomPackageSpec{
					Replicate:           true,
					GitServerURL:        "https://adhar.io",
					InternalGitServeURL: "http://internal.adhar.io",
					ArgoCD: v1alpha1.ArgoCDPackageSpec{
						ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/applicationSet/generator-single-source.yaml"),
						Type:            "ApplicationSet",
					},
				},
			},
			expectedGitRepo: v1alpha1.GitRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      localRepoName("generator-single-source", "test/resources/customPackages/applicationSet/test1"),
					Namespace: "test",
				},
				Spec: v1alpha1.GitRepositorySpec{
					Source: v1alpha1.GitRepositorySource{
						Type: "local",
						Path: filepath.Join(cwd, "test/resources/customPackages/applicationSet/test1"),
					},
					Provider: v1alpha1.Provider{
						Name:             v1alpha1.GitProviderGitea,
						GitURL:           "https://adhar.io",
						InternalGitURL:   "http://internal.adhar.io",
						OrganizationName: v1alpha1.GiteaAdminUserName,
					},
				},
			},
			expectedApplicationSet: argov1alpha1.ApplicationSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generator-single-source",
					Namespace: "argocd",
				},
				Spec: argov1alpha1.ApplicationSetSpec{
					Generators: []argov1alpha1.ApplicationSetGenerator{
						{
							Git: &argov1alpha1.GitGenerator{
								RepoURL: "",
							},
						},
					},
					Template: argov1alpha1.ApplicationSetTemplate{
						Spec: argov1alpha1.ApplicationSpec{
							Source: &argov1alpha1.ApplicationSource{
								RepoURL: "",
							},
						},
					},
				},
			},
		},
		{
			input: v1alpha1.CustomPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test2",
					Namespace: "test",
					UID:       "test2",
				},
				Spec: v1alpha1.CustomPackageSpec{
					Replicate:           true,
					GitServerURL:        "https://adhar.io",
					InternalGitServeURL: "http://internal.adhar.io",
					ArgoCD: v1alpha1.ArgoCDPackageSpec{
						ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/applicationSet/generator-multi-sources.yaml"),
						Type:            "ApplicationSet",
					},
				},
			},
			expectedGitRepo: v1alpha1.GitRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      localRepoName("generator-multi-sources", "test/resources/customPackages/applicationSet/test1"),
					Namespace: "test",
				},
				Spec: v1alpha1.GitRepositorySpec{
					Source: v1alpha1.GitRepositorySource{
						Type: "local",
						Path: filepath.Join(cwd, "test/resources/customPackages/applicationSet/test1"),
					},
					Provider: v1alpha1.Provider{
						Name:             v1alpha1.GitProviderGitea,
						GitURL:           "https://adhar.io",
						InternalGitURL:   "http://internal.adhar.io",
						OrganizationName: v1alpha1.GiteaAdminUserName,
					},
				},
			},
			expectedApplicationSet: argov1alpha1.ApplicationSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generator-multi-sources",
					Namespace: "argocd",
				},
				Spec: argov1alpha1.ApplicationSetSpec{
					Generators: []argov1alpha1.ApplicationSetGenerator{
						{
							Git: &argov1alpha1.GitGenerator{
								RepoURL: "",
							},
						},
					},
					Template: argov1alpha1.ApplicationSetTemplate{
						Spec: argov1alpha1.ApplicationSpec{
							Sources: []argov1alpha1.ApplicationSource{
								{
									RepoURL: "",
								},
							},
						},
					},
				},
			},
		},
		{
			input: v1alpha1.CustomPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test3",
					Namespace: "test",
					UID:       "test3",
				},
				Spec: v1alpha1.CustomPackageSpec{
					Replicate:           true,
					GitServerURL:        "https://adhar.io",
					InternalGitServeURL: "http://internal.adhar.io",
					ArgoCD: v1alpha1.ArgoCDPackageSpec{
						ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/applicationSet/no-generator-single-source.yaml"),
						Type:            "ApplicationSet",
					},
				},
			},
			expectedGitRepo: v1alpha1.GitRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      localRepoName("no-generator-single-source", "test/resources/customPackages/applicationSet/test1"),
					Namespace: "test",
				},
				Spec: v1alpha1.GitRepositorySpec{
					Source: v1alpha1.GitRepositorySource{
						Type: "local",
						Path: filepath.Join(cwd, "test/resources/customPackages/applicationSet/test1"),
					},
					Provider: v1alpha1.Provider{
						Name:             v1alpha1.GitProviderGitea,
						GitURL:           "https://adhar.io",
						InternalGitURL:   "http://internal.adhar.io",
						OrganizationName: v1alpha1.GiteaAdminUserName,
					},
				},
			},
			expectedApplicationSet: argov1alpha1.ApplicationSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-generator-single-source",
					Namespace: "argocd",
				},
				Spec: argov1alpha1.ApplicationSetSpec{
					Template: argov1alpha1.ApplicationSetTemplate{
						Spec: argov1alpha1.ApplicationSpec{
							Source: &argov1alpha1.ApplicationSource{
								RepoURL: "",
							},
						},
					},
				},
			},
		},
		{
			input: v1alpha1.CustomPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test4",
					Namespace: "test",
					UID:       "test4",
				},
				Spec: v1alpha1.CustomPackageSpec{
					Replicate:           true,
					GitServerURL:        "https://adhar.io",
					InternalGitServeURL: "http://internal.adhar.io",
					ArgoCD: v1alpha1.ArgoCDPackageSpec{
						ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/applicationSet/generator-matrix.yaml"),
						Type:            "ApplicationSet",
					},
				},
			},
			expectedGitRepo: v1alpha1.GitRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      localRepoName("generator-matrix", "test/resources/customPackages/applicationSet/test1"),
					Namespace: "test",
				},
				Spec: v1alpha1.GitRepositorySpec{
					Source: v1alpha1.GitRepositorySource{
						Type: "local",
						Path: filepath.Join(cwd, "test/resources/customPackages/applicationSet/test1"),
					},
					Provider: v1alpha1.Provider{
						Name:             v1alpha1.GitProviderGitea,
						GitURL:           "https://adhar.io",
						InternalGitURL:   "http://internal.adhar.io",
						OrganizationName: v1alpha1.GiteaAdminUserName,
					},
				},
			},
			expectedApplicationSet: argov1alpha1.ApplicationSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generator-matrix",
					Namespace: "argocd",
				},
				Spec: argov1alpha1.ApplicationSetSpec{
					Generators: []argov1alpha1.ApplicationSetGenerator{
						{
							Matrix: &argov1alpha1.MatrixGenerator{
								Generators: []argov1alpha1.ApplicationSetNestedGenerator{
									{
										Git: &argov1alpha1.GitGenerator{
											RepoURL: "",
										},
									},
								},
							},
						},
					},
					Template: argov1alpha1.ApplicationSetTemplate{
						Spec: argov1alpha1.ApplicationSpec{
							Source: &argov1alpha1.ApplicationSource{
								RepoURL: "",
							},
						},
					},
				},
			},
		},
	}

	for i := range cases {
		tc := cases[i]
		_, err = r.reconcileCustomPackage(context.Background(), &tc.input)
		assert.Nil(t, err)
		time.Sleep(1 * time.Second)

		c := mgr.GetClient()
		repo := v1alpha1.GitRepository{}
		err = c.Get(context.Background(), client.ObjectKeyFromObject(&tc.expectedGitRepo), &repo)
		assert.Nil(t, err)

		assert.Equal(t, tc.expectedGitRepo.Spec, repo.Spec)

		// verify argocd applicationSet
		appset := argov1alpha1.ApplicationSet{}
		err = c.Get(context.Background(), client.ObjectKeyFromObject(&tc.expectedApplicationSet), &appset)
		assert.Nil(t, err)

		if len(tc.expectedApplicationSet.Spec.Template.Spec.Sources) > 0 {
			for j := range tc.expectedApplicationSet.Spec.Template.Spec.Sources {
				exs := tc.expectedApplicationSet.Spec.Template.Spec.Sources[j]
				assert.Equal(t, exs.RepoURL, appset.Spec.Template.Spec.Sources[j].RepoURL)
				assert.False(t, strings.HasPrefix(appset.Spec.Template.Spec.Sources[j].RepoURL, v1alpha1.ADHARURIScheme))
			}
		} else {
			assert.Equal(t, tc.expectedApplicationSet.Spec.Template.Spec.Source.RepoURL, appset.Spec.Template.Spec.Source.RepoURL)
			assert.False(t, strings.HasPrefix(appset.Spec.Template.Spec.Source.RepoURL, v1alpha1.ADHARURIScheme))
		}

		if len(tc.expectedApplicationSet.Spec.Generators) > 0 {
			for j := range tc.expectedApplicationSet.Spec.Generators {
				exg := tc.expectedApplicationSet.Spec.Generators[j]
				if exg.Git != nil {
					assert.Equal(t, exg.Git.RepoURL, appset.Spec.Generators[j].Git.RepoURL)
				}
				if exg.Matrix != nil {
					for k := range exg.Matrix.Generators {
						if exg.Matrix.Generators[k].Git != nil {
							assert.Equal(t, exg.Matrix.Generators[k].Git.RepoURL, appset.Spec.Generators[j].Matrix.Generators[k].Git.RepoURL)
						}
					}
				}
			}
		}
	}
}

func TestReconcileHelmValueObject(t *testing.T) {
	s := k8sruntime.NewScheme()
	sb := k8sruntime.NewSchemeBuilder(
		v1.AddToScheme,
		argov1alpha1.AddToScheme,
		v1alpha1.AddToScheme,
	)
	sb.AddToScheme(s)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "resources"),
			"../localbuild/resources/argo/install.yaml",
		},
		ErrorIfCRDPathMissing: true,
		Scheme:                s,
		BinaryAssetsDirectory: filepath.Join("..", "..", "..", "bin", "k8s",
			fmt.Sprintf("1.29.1-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("Starting testenv: %v", err)
	}
	defer testEnv.Stop()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: s,
	})
	if err != nil {
		t.Fatalf("getting manager: %v", err)
	}
	ctx, ctxCancel := context.WithCancel(context.Background())
	stoppedCh := make(chan error)
	go func() {
		err := mgr.Start(ctx)
		stoppedCh <- err
	}()

	defer func() {
		ctxCancel()
		err := <-stoppedCh
		if err != nil {
			t.Errorf("Starting controller manager: %v", err)
			t.FailNow()
		}
	}()

	time.Sleep(1 * time.Second)

	for _, n := range []string{"argocd", "test"} {
		ns := v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: n,
			},
		}
		err = mgr.GetClient().Create(context.Background(), &ns)
		if err != nil {
			t.Fatalf("creating test ns: %v", err)
		}
	}

	r := &Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("test-custompkg-controller"),
	}

	cwd, _ := os.Getwd()

	resource := v1alpha1.CustomPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test1",
			Namespace: "test",
			UID:       "abc",
		},
		Spec: v1alpha1.CustomPackageSpec{
			Replicate:           true,
			GitServerURL:        "https://adhar.io",
			InternalGitServeURL: "http://internal.adhar.io",
			ArgoCD: v1alpha1.ArgoCDPackageSpec{
				ApplicationFile: filepath.Join(cwd, "test/resources/customPackages/helm/app.yaml"),
				Name:            "my-app",
				Namespace:       "argocd",
				Type:            "Application",
			},
		},
	}

	source := &argov1alpha1.ApplicationSource{
		Helm: &argov1alpha1.ApplicationSourceHelm{
			ValuesObject: &k8sruntime.RawExtension{
				Raw: []byte(`{
				 "repoURLGit": "adhar://test",
				 "nested": {
				   "repoURLGit": "adhar://test",
				   "bool": true,
				   "int": 123
				 },
				 "bool": false,
				 "int": 456,
				 "arrayString": [
				   "abc",
				   "adhar://test"
				 ],
				 "arrayMap": [
				   {
				     "test": "adhar://test",
				     "nested": {
				       "test": "adhar://test"
				     }
				   }
				 ]
				}`),
			},
		},
	}

	_, err = r.reconcileHelmValueObject(ctx, source, &resource, "test")
	assert.NoError(t, err)
	expectJson := `{"arrayMap":[{"nested":{"test":""},"test":""}],"arrayString":["abc",""],"bool":false,"int":456,"nested":{"bool":true,"int":123,"repoURLGit":""},"repoURLGit":""}`
	assert.JSONEq(t, expectJson, string(source.Helm.ValuesObject.Raw))
}
