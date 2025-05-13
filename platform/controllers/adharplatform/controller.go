/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package adharplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.gitea.io/sdk/gitea"
	argocdapp "github.com/cnoe-io/argocd-api/api/argo/application"
	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sel "k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/utils"
)

const (
	defaultArgoCDProjectName string = "default"
	defaultRequeueTime              = time.Second * 15
	errRequeueTime                  = time.Second * 5

	argoCDApplicationAnnotationKeyRefresh         = "argocd.argoproj.io/refresh"
	argoCDApplicationAnnotationValueRefreshNormal = "normal"
	argoCDApplicationSetAnnotationKeyRefresh      = "argocd.argoproj.io/application-set-refresh"
	argoCDApplicationSetAnnotationKeyRefreshTrue  = "true"
)

type ArgocdSession struct {
	Token string `json:"token"`
}

// AdharPlatformReconciler reconciles a AdharPlatform object
type AdharPlatformReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	CancelFunc     context.CancelFunc
	ExitOnSync     bool
	shouldShutdown bool
	Config         v1alpha1.BuildCustomizationSpec
	TempDir        string
	RepoMap        *utils.RepoMap
}

type subReconciler func(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error)

// +kubebuilder:rbac:groups=platform.adhar.io,resources=adharplatforms,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.adhar.io,resources=adharplatforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.adhar.io,resources=adharplatforms/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AdharPlatform object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *AdharPlatformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Reconciling", "resource", req.NamespacedName)

	var localBuild v1alpha1.AdharPlatform
	if err := r.Get(ctx, req.NamespacedName, &localBuild); err != nil {
		logger.Error(err, "unable to fetch Resource")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	defer r.postProcessReconcile(ctx, req, &localBuild)

	_, err := r.ReconcileProjectNamespace(ctx, req, &localBuild)
	if err != nil {
		return ctrl.Result{}, err
	}

	errChan := make(chan error, 3)

	go r.installCorePackages(ctx, req, &localBuild, errChan)

	select {
	case <-ctx.Done():
		logger.Info("Main context cancelled during core package installation")
		return ctrl.Result{}, ctx.Err()
	case instErr := <-errChan:
		if instErr != nil {
			logger.V(1).Info("failed installing core package. likely not fatal. will try again", "error", instErr)
			return ctrl.Result{RequeueAfter: errRequeueTime}, nil
		}
	}

	if r.Config.StaticPassword {
		logger.V(1).Info("static password is enabled")

		argocdInitialAdminPassword, err := r.extractArgocdInitialAdminSecret(ctx)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}

		logger.V(1).Info("Initial argocd admin secret found ...")

		if argocdInitialAdminPassword != "" && argocdInitialAdminPassword != utils.StaticPassword {
			err = r.updateArgocdPassword(ctx, argocdInitialAdminPassword)
			if err != nil {
				return ctrl.Result{}, err
			} else {
				logger.V(1).Info("Argocd admin password change succeeded !")
			}
		}

		giteaAdminPassword, err := r.extractGiteaAdminSecret(ctx)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}
		logger.V(1).Info("Gitea admin secret found ...")
		if giteaAdminPassword != "" && giteaAdminPassword != utils.StaticPassword {
			err = r.updateGiteaPassword(ctx, giteaAdminPassword)
			if err != nil {
				return ctrl.Result{}, err
			} else {
				logger.V(1).Info("Gitea admin password change succeeded !")
			}
		}
	}

	logger.V(1).Info("done installing core packages. passing control to argocd")
	_, err = r.ReconcileArgoAppsWithGitea(ctx, req, &localBuild)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
}

func (r *AdharPlatformReconciler) installCorePackages(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform, errChan chan error) {
	logger := log.FromContext(ctx)
	defer close(errChan)
	var wg sync.WaitGroup

	installers := map[string]subReconciler{
		v1alpha1.IngressNginxPackageName: r.ReconcileNginx,
		v1alpha1.ArgoCDPackageName:       r.ReconcileArgo,
		v1alpha1.GiteaPackageName:        r.ReconcileGitea,
		v1alpha1.CiliumPackageName:       r.ReconcileCilium,
	}
	logger.V(1).Info("installing core packages")
	for k, v := range installers {
		wg.Add(1)
		name := k
		inst := v
		go func() {
			defer wg.Done()
			_, iErr := inst(ctx, req, resource)
			if iErr != nil {
				logger.V(1).Info("failed installing", "name", name, "error", iErr)
				errChan <- fmt.Errorf("failed installing %s: %w", name, iErr)
			}
		}()
	}
	wg.Wait()
}

func (r *AdharPlatformReconciler) ReconcileCilium(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Cilium core package")

	ciliumManifestPath := "pkg/controllers/localbuild/resources/cilium/k8s/install.yaml"
	manifestBytes, err := os.ReadFile(ciliumManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read Cilium install manifest", "path", ciliumManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading cilium manifest %s: %w", ciliumManifestPath, err)
	}

	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifestBytes), 100)
	var applyErrors []error

	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Error(err, "Failed to decode object from Cilium manifest")
			applyErrors = append(applyErrors, fmt.Errorf("decoding object: %w", err))
			continue
		}

		if obj.Object == nil {
			continue
		}

		// Fetch a fresh copy of the AdharPlatform resource to ensure we have the latest state
		freshAdharPlatform := &v1alpha1.AdharPlatform{}
		if err := r.Get(ctx, req.NamespacedName, freshAdharPlatform); err != nil {
			logger.Error(err, "Failed to get fresh AdharPlatform resource before setting owner ref", "kind", obj.GetKind(), "name", obj.GetName())
			applyErrors = append(applyErrors, fmt.Errorf("getting fresh owner for %s/%s: %w", obj.GetKind(), obj.GetName(), err))
			continue
		}

		// Log the state before attempting to set owner reference using the fresh object
		logger.V(1).Info("Checking owner reference for Cilium object with fresh owner data",
			"kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(),
			"ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID, "ownerDeletionTimestamp", freshAdharPlatform.ObjectMeta.DeletionTimestamp)

		if freshAdharPlatform.ObjectMeta.DeletionTimestamp.IsZero() {
			// Add logging before attempting to set the reference
			logger.V(1).Info("Owner is not being deleted, attempting to set owner reference", "targetKind", obj.GetKind(), "targetName", obj.GetName())
			// Log owner details just before setting reference
			logger.V(1).Info("Owner details before SetControllerReference", "ownerName", freshAdharPlatform.Name, "ownerUID", freshAdharPlatform.UID)
			if err := controllerutil.SetControllerReference(freshAdharPlatform, obj, r.Scheme); err != nil { // Use freshAdharPlatform here
				logger.Error(err, "Failed to set controller reference on Cilium object", "kind", obj.GetKind(), "name", obj.GetName())
				applyErrors = append(applyErrors, fmt.Errorf("setting owner ref on %s/%s: %w", obj.GetKind(), obj.GetName(), err))
				continue // Skip applying this object if owner ref fails
			}
			// Add logging after successfully setting the reference
			logger.V(1).Info("Successfully set owner reference", "targetKind", obj.GetKind(), "targetName", obj.GetName())
		} else {
			logger.V(1).Info("Owner resource is being deleted, skipping owner reference setting", "owner", freshAdharPlatform.Name, "targetKind", obj.GetKind(), "targetName", obj.GetName())
		}

		// Apply the object using Server-Side Apply
		patch := client.Apply
		opts := []client.PatchOption{client.ForceOwnership, client.FieldOwner(v1alpha1.FieldManager)}
		err = r.Patch(ctx, obj, patch, opts...)
		if err != nil {
			logger.Error(err, "Failed to apply Cilium object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
			applyErrors = append(applyErrors, fmt.Errorf("applying %s/%s: %w", obj.GetKind(), obj.GetName(), err))
			continue
		}
		logger.V(1).Info("Applied Cilium object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
	}

	if len(applyErrors) > 0 {
		combinedErr := fmt.Errorf("encountered %d errors applying cilium manifest: %v", len(applyErrors), applyErrors)
		logger.Error(combinedErr, "Failed to apply all Cilium resources")
		return ctrl.Result{}, combinedErr
	}

	logger.Info("Successfully reconciled Cilium core package")
	return ctrl.Result{}, nil
}

func (r *AdharPlatformReconciler) postProcessReconcile(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) {
	logger := log.FromContext(ctx)

	logger.Info("Checking if we should shutdown")
	if r.shouldShutdown {
		logger.Info("Shutting Down")
		err := r.requestArgoCDAppRefresh(ctx)
		if err != nil {
			logger.V(1).Info("failed requesting argocd application refresh", "error", err)
		}
		err = r.requestArgoCDAppSetRefresh(ctx)
		if err != nil {
			logger.V(1).Info("failed requesting argocd application set refresh", "error", err)
		}
		r.CancelFunc()
		return
	}

	resource.Status.ObservedGeneration = resource.GetGeneration()
	if err := r.Status().Update(ctx, resource); err != nil {
		logger.Error(err, "Failed to update resource status after reconcile")
	}
}

func (r *AdharPlatformReconciler) ReconcileProjectNamespace(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	nsResource := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: globals.GetProjectNamespace(resource.Name),
		},
	}

	logger.V(1).Info("Create or update namespace", "resource", nsResource)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, nsResource, func() error {
		if err := controllerutil.SetControllerReference(resource, nsResource, r.Scheme); err != nil {
			logger.Error(err, "Setting controller ref on namespace resource")
			return err
		}
		return nil
	})
	if err != nil {
		logger.Error(err, "Create or update namespace resource")
	}
	return ctrl.Result{}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *AdharPlatformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AdharPlatform{}).
		Complete(r)
}

func (r *AdharPlatformReconciler) ReconcileArgoAppsWithGitea(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("installing bootstrap apps to ArgoCD")

	bootStrapApps := []string{v1alpha1.ArgoCDPackageName, v1alpha1.IngressNginxPackageName, v1alpha1.GiteaPackageName}
	for _, n := range bootStrapApps {
		result, err := r.reconcileEmbeddedApp(ctx, n, resource)
		if err != nil {
			return result, fmt.Errorf("reconciling bootstrap apps %w", err)
		}
	}

	for _, s := range resource.Spec.PackageConfigs.CustomPackageDirs {
		result, err := r.reconcileCustomPkgDir(ctx, resource, s)
		if err != nil {
			return result, err
		}
	}

	for _, s := range resource.Spec.PackageConfigs.CustomPackageUrls {
		result, err := r.reconcileCustomPkgUrl(ctx, resource, s)
		if err != nil {
			return result, err
		}
	}

	shutdown, err := r.shouldShutDown(ctx, resource)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}
	r.shouldShutdown = shutdown

	return ctrl.Result{}, nil
}

func (r *AdharPlatformReconciler) reconcileEmbeddedApp(ctx context.Context, appName string, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.V(1).Info("Ensuring embedded ArgoCD Application", "name", appName)
	repo, err := r.reconcileGitRepo(ctx, resource, "embedded", appName, appName, "")

	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating %s repo CR: %w", appName, err)
	}

	app := &argov1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appName,
			Namespace: globals.ArgoCDNamespace,
		},
	}

	utils.SetPackageLabels(app)

	if err := controllerutil.SetControllerReference(resource, app, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	var targetRevision *string

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(app), app)
	if err != nil && k8serrors.IsNotFound(err) {
		utils.SetApplicationSpec(
			app,
			repo.Status.InternalGitRepositoryUrl,
			".",
			defaultArgoCDProjectName,
			appName,
			targetRevision,
		)
		err = r.Client.Create(ctx, app)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("creating %s app CR: %w", appName, err)
		}
	}

	utils.SetApplicationSpec(
		app,
		repo.Status.InternalGitRepositoryUrl,
		".",
		defaultArgoCDProjectName,
		appName,
		targetRevision,
	)
	err = r.Client.Update(ctx, app)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating argoapp: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *AdharPlatformReconciler) shouldShutDown(ctx context.Context, resource *v1alpha1.AdharPlatform) (bool, error) {
	logger := log.FromContext(ctx)

	if !r.ExitOnSync {
		return false, nil
	}

	cliStartTime, err := utils.GetCLIStartTimeAnnotationValue(resource.Annotations)
	if err != nil {
		return false, err
	}

	selector := labels.NewSelector()
	req, err := labels.NewRequirement(v1alpha1.PackageTypeLabelKey, sel.Equals, []string{v1alpha1.PackageTypeLabelCore})
	if err != nil {
		return false, fmt.Errorf("building labels with key %s and value %s : %w", v1alpha1.PackageTypeLabelKey, v1alpha1.PackageTypeLabelCore, err)
	}

	opts := client.ListOptions{
		LabelSelector: selector.Add(*req),
		Namespace:     "",
	}
	apps := argov1alpha1.ApplicationList{}
	err = r.Client.List(ctx, &apps, &opts)
	if err != nil {
		return false, fmt.Errorf("listing core packages: %w", err)
	}

	for _, app := range apps.Items {
		if app.Status.Health.Status != "Healthy" {
			return false, nil
		}
	}

	repos := &v1alpha1.GitRepositoryList{}
	err = r.Client.List(ctx, repos, client.InNamespace(resource.Namespace))
	if err != nil {
		return false, fmt.Errorf("listing repositories %w", err)
	}

	for i := range repos.Items {
		repo := repos.Items[i]

		startTimeAnnotation, gErr := utils.GetCLIStartTimeAnnotationValue(repo.ObjectMeta.Annotations)
		if gErr != nil {
			continue
		}

		if startTimeAnnotation != cliStartTime {
			continue
		}

		observedTime, gErr := utils.GetLastObservedSyncTimeAnnotationValue(repo.ObjectMeta.Annotations)
		if gErr != nil {
			logger.Info(gErr.Error())
			return false, nil
		}

		if !repo.Status.Synced || cliStartTime != observedTime {
			return false, nil
		}
	}

	pkgs := &v1alpha1.CustomPackageList{}
	err = r.Client.List(ctx, pkgs, client.InNamespace(resource.Namespace))
	if err != nil {
		return false, fmt.Errorf("listing custom packages %w", err)
	}
	for i := range pkgs.Items {
		pkg := pkgs.Items[i]
		startTimeAnnotation, gErr := utils.GetCLIStartTimeAnnotationValue(pkg.ObjectMeta.Annotations)
		if gErr != nil {
			continue
		}

		if startTimeAnnotation != cliStartTime {
			return false, nil
		}

		observedTime, gErr := utils.GetLastObservedSyncTimeAnnotationValue(pkg.ObjectMeta.Annotations)
		if gErr != nil {
			logger.Info(gErr.Error())
			return false, nil
		}
		if !pkg.Status.Synced || cliStartTime != observedTime {
			return false, nil
		}
	}

	return true, nil
}

func (r *AdharPlatformReconciler) reconcileCustomPkg(
	ctx context.Context,
	resource *v1alpha1.AdharPlatform,
	b []byte,
	filePath string,
	remote *utils.KustomizeRemote,
) error {
	o := &unstructured.Unstructured{}
	_, gvk, fErr := scheme.Codecs.UniversalDeserializer().Decode(b, nil, o)
	if fErr != nil {
		return fErr
	}

	if isSupportedArgoCDTypes(gvk) {
		kind := o.GetKind()
		appName := o.GetName()
		appNS := o.GetNamespace()
		customPkg := &v1alpha1.CustomPackage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getCustomPackageName(filepath.Base(filePath), appName),
				Namespace: globals.GetProjectNamespace(resource.Name),
			},
		}

		cliStartTime, _ := utils.GetCLIStartTimeAnnotationValue(resource.ObjectMeta.Annotations)

		_, fErr = controllerutil.CreateOrUpdate(ctx, r.Client, customPkg, func() error {
			if err := controllerutil.SetControllerReference(resource, customPkg, r.Scheme); err != nil {
				return err
			}
			if customPkg.ObjectMeta.Annotations == nil {
				customPkg.ObjectMeta.Annotations = make(map[string]string)
			}

			utils.SetCLIStartTimeAnnotationValue(customPkg.ObjectMeta.Annotations, cliStartTime)

			customPkg.Spec = v1alpha1.CustomPackageSpec{
				Replicate:           true,
				GitServerURL:        resource.Status.Gitea.ExternalURL,
				InternalGitServeURL: resource.Status.Gitea.InternalURL,
				GitServerAuthSecretRef: v1alpha1.SecretReference{
					Name:      resource.Status.Gitea.AdminUserSecretName,
					Namespace: resource.Status.Gitea.AdminUserSecretNamespace,
				},
				ArgoCD: v1alpha1.ArgoCDPackageSpec{
					ApplicationFile: filePath,
					Name:            appName,
					Namespace:       appNS,
					Type:            kind,
				},
			}

			if remote != nil {
				customPkg.Spec.RemoteRepository = v1alpha1.RemoteRepositorySpec{
					Url:             remote.CloneUrl(),
					Ref:             remote.Ref,
					CloneSubmodules: remote.Submodules,
					Path:            remote.Path(),
				}
			}

			return nil
		})
		return fErr
	}
	return nil
}

func (r *AdharPlatformReconciler) reconcileCustomPkgUrl(ctx context.Context, resource *v1alpha1.AdharPlatform, pkgUrl string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	remote, err := utils.NewKustomizeRemote(pkgUrl)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing url, %s: %w", pkgUrl, err)
	}
	rs := v1alpha1.RemoteRepositorySpec{
		Url:             remote.CloneUrl(),
		Ref:             remote.Ref,
		CloneSubmodules: remote.Submodules,
		Path:            remote.Path(),
	}

	cloneDir := utils.RepoDir(rs.Url, r.TempDir)
	st := r.RepoMap.LoadOrStore(rs.Url, cloneDir)
	st.MU.Lock()
	defer st.MU.Unlock()
	wt, _, err := utils.CloneRemoteRepoToDir(ctx, rs, 1, false, cloneDir, "")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cloning repo, %s: %w", pkgUrl, err)
	}

	yamlFiles, err := utils.GetWorktreeYamlFiles(remote.Path(), wt, false)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting yaml files from repo, %s: %w", pkgUrl, err)
	}

	for _, yamlFile := range yamlFiles {
		b, fErr := utils.ReadWorktreeFile(wt, yamlFile)
		if fErr != nil {
			logger.V(1).Info("processing", "file", yamlFile, "err", fErr)
			continue
		}

		rErr := r.reconcileCustomPkg(ctx, resource, b, yamlFile, remote)
		if rErr != nil {
			logger.Error(rErr, "reconciling custom pkg", "file", yamlFile, "pkgUrl", pkgUrl)
		}
	}
	return ctrl.Result{}, nil
}

func (r *AdharPlatformReconciler) reconcileCustomPkgDir(ctx context.Context, resource *v1alpha1.AdharPlatform, pkgDir string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	files, err := os.ReadDir(pkgDir)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading dir, %s: %w", pkgDir, err)
	}

	for i := range files {
		file := files[i]
		if !file.Type().IsRegular() || !utils.IsYamlFile(file.Name()) {
			continue
		}

		filePath := filepath.Join(pkgDir, file.Name())
		b, fErr := os.ReadFile(filePath)
		if fErr != nil {
			logger.Error(fErr, "reading file", "file", filePath)
			continue
		}

		rErr := r.reconcileCustomPkg(ctx, resource, b, filePath, nil)
		if rErr != nil {
			logger.Error(rErr, "reconciling custom pkg", "file", filePath, "pkgDir", pkgDir)
		}
	}

	return ctrl.Result{}, nil
}

func (r *AdharPlatformReconciler) reconcileGitRepo(ctx context.Context, resource *v1alpha1.AdharPlatform, repoType, repoName, embeddedName, absPath string) (*v1alpha1.GitRepository, error) {
	repo := &v1alpha1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      repoName,
			Namespace: globals.GetProjectNamespace(resource.Name),
		},
	}

	cliStartTime, err := utils.GetCLIStartTimeAnnotationValue(resource.Annotations)
	if err != nil {
		return nil, err
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, repo, func() error {
		if err := controllerutil.SetControllerReference(resource, repo, r.Scheme); err != nil {
			return err
		}

		if repo.ObjectMeta.Annotations == nil {
			repo.ObjectMeta.Annotations = make(map[string]string)
		}
		utils.SetCLIStartTimeAnnotationValue(repo.ObjectMeta.Annotations, cliStartTime)

		repo.Spec = v1alpha1.GitRepositorySpec{
			Source: v1alpha1.GitRepositorySource{
				Type: repoType,
			},
			Provider: v1alpha1.Provider{
				Name:             v1alpha1.GitProviderGitea,
				GitURL:           resource.Status.Gitea.ExternalURL,
				InternalGitURL:   resource.Status.Gitea.InternalURL,
				OrganizationName: v1alpha1.GiteaAdminUserName,
			},
			SecretRef: v1alpha1.SecretReference{
				Name:      resource.Status.Gitea.AdminUserSecretName,
				Namespace: resource.Status.Gitea.AdminUserSecretNamespace,
			},
		}

		if repoType == v1alpha1.SourceTypeEmbedded {
			repo.Spec.Source.EmbeddedAppName = embeddedName
		} else {
			repo.Spec.Source.Path = absPath
		}
		f, ok := resource.Spec.PackageConfigs.CorePackageCustomization[embeddedName]
		if ok {
			repo.Spec.Customization = v1alpha1.PackageCustomization{
				Name:     embeddedName,
				FilePath: f.FilePath,
			}
		}
		return nil
	})

	return repo, err
}

func (r *AdharPlatformReconciler) requestArgoCDAppRefresh(ctx context.Context) error {
	apps := &argov1alpha1.ApplicationList{}
	err := r.Client.List(ctx, apps, client.InNamespace(globals.ArgoCDNamespace))
	if err != nil {
		return fmt.Errorf("listing argocd apps for refresh: %w", err)
	}

apps:
	for i := range apps.Items {
		app := apps.Items[i]
		for _, o := range app.OwnerReferences {
			if o.Kind == argocdapp.ApplicationSetKind {
				continue apps
			}
		}
		aErr := r.applyArgoCDAnnotation(ctx, &app, argocdapp.ApplicationKind, argoCDApplicationAnnotationKeyRefresh, argoCDApplicationAnnotationValueRefreshNormal)
		if aErr != nil {
			return aErr
		}
	}
	return nil
}

func (r *AdharPlatformReconciler) requestArgoCDAppSetRefresh(ctx context.Context) error {
	appsets := &argov1alpha1.ApplicationSetList{}
	err := r.Client.List(ctx, appsets, client.InNamespace(globals.ArgoCDNamespace))
	if err != nil {
		return fmt.Errorf("listing argocd apps for refresh: %w", err)
	}

	for i := range appsets.Items {
		appset := appsets.Items[i]
		aErr := r.applyArgoCDAnnotation(ctx, &appset, argocdapp.ApplicationSetKind, argoCDApplicationSetAnnotationKeyRefresh, argoCDApplicationSetAnnotationKeyRefreshTrue)
		if aErr != nil {
			return aErr
		}
	}
	return nil
}

func (r *AdharPlatformReconciler) extractArgocdInitialAdminSecret(ctx context.Context) (string, error) {
	sec := utils.ArgocdInitialAdminSecretObject()
	err := r.Client.Get(ctx, types.NamespacedName{
		Namespace: sec.GetNamespace(),
		Name:      sec.GetName(),
	}, &sec)

	if err != nil {
		if k8serrors.IsNotFound(err) {
			return "", fmt.Errorf("initial admin secret not found")
		}
	}
	return string(sec.Data["password"]), nil
}

func (r *AdharPlatformReconciler) extractGiteaAdminSecret(ctx context.Context) (string, error) {
	sec := utils.GiteaAdminSecretObject()
	err := r.Client.Get(ctx, types.NamespacedName{
		Namespace: sec.GetNamespace(),
		Name:      sec.GetName(),
	}, &sec)

	if err != nil {
		if k8serrors.IsNotFound(err) {
			return "", fmt.Errorf("gitea admin secret not found")
		}
	}
	return string(sec.Data["password"]), nil
}

func (r *AdharPlatformReconciler) updateGiteaPassword(ctx context.Context, adminPassword string) error {
	giteaBaseUrl, err := utils.GiteaBaseUrl(ctx)
	if err != nil {
		return fmt.Errorf("generating gitea url: %w", err)
	}

	client, err := gitea.NewClient(giteaBaseUrl, gitea.SetHTTPClient(utils.GetHttpClient()),
		gitea.SetBasicAuth("giteaAdmin", adminPassword), gitea.SetContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("cannot create gitea client: %w", err)
	}

	opts := gitea.EditUserOption{
		LoginName: "giteaAdmin",
		Password:  utils.StaticPassword,
	}

	resp, err := client.AdminEditUser("giteaAdmin", opts)
	if err != nil {
		return fmt.Errorf("cannot update gitea admin user. status: %d error : %w", resp.StatusCode, err)
	}

	err = utils.PatchPasswordSecret(ctx, r.Client, r.Config, utils.GiteaNamespace, utils.GiteaAdminSecret, utils.GiteaAdminName, utils.StaticPassword)
	if err != nil {
		return fmt.Errorf("patching the gitea credentials failed : %w", err)
	}
	return nil
}

func (r *AdharPlatformReconciler) updateArgocdPassword(ctx context.Context, adminPassword string) error {
	argocdBaseUrl, err := utils.ArgocdBaseUrl(ctx)
	if err != nil {
		return fmt.Errorf("error creating argocd Url: %w", err)
	}

	argocdEndpoint := argocdBaseUrl + "/api/v1"

	payload := map[string]string{
		"username": "admin",
		"password": adminPassword,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error creating JSON payload: %w", err)
	}

	req, err := http.NewRequest("POST", argocdEndpoint+"/session", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error creating HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	c := utils.GetHttpClient()

	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}

	if resp.StatusCode == 200 {
		var argocdSession ArgocdSession

		err := json.Unmarshal([]byte(body), &argocdSession)
		if err != nil {
			return fmt.Errorf("error unmarshalling JSON: %w", err)
		}

		payload := map[string]string{
			"name":            "admin",
			"currentPassword": adminPassword,
			"newPassword":     utils.StaticPassword,
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("error creating JSON payload: %w", err)
		}

		req, err := http.NewRequest("PUT", argocdEndpoint+"/account/password", bytes.NewBuffer(payloadBytes))
		if err != nil {
			return fmt.Errorf("error creating password update request: %w", err)
		}
		if req != nil {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", argocdSession.Token))
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("error sending password update request: %w", err)
		}
		defer resp.Body.Close()

		payload = map[string]string{
			"username": "admin",
			"password": utils.StaticPassword,
		}
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("error creating JSON payload for verification: %w", err)
		}

		req, err = http.NewRequest("POST", argocdEndpoint+"/session", bytes.NewBuffer(payloadBytes))
		if err != nil {
			return fmt.Errorf("error creating verification request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err = c.Do(req)
		if err != nil {
			return fmt.Errorf("error sending verification request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			err = utils.PatchPasswordSecret(ctx, r.Client, r.Config, utils.ArgocdNamespace, utils.ArgocdInitialAdminSecretName, utils.ArgocdAdminName, utils.StaticPassword)
			if err != nil {
				return fmt.Errorf("patching the argocd initial secret failed : %w", err)
			}
			return nil
		}
	}
	return nil
}

func (r *AdharPlatformReconciler) applyArgoCDAnnotation(ctx context.Context, obj client.Object, argoCDType, annotationKey, annotationValue string) error {
	annotations := obj.GetAnnotations()
	if annotations != nil {
		_, ok := annotations[annotationKey]
		if !ok {
			annotations[annotationKey] = annotationValue
			err := utils.ApplyAnnotation(ctx, r.Client, obj, annotations, client.FieldOwner(v1alpha1.FieldManager))
			if err != nil {
				return fmt.Errorf("applying %s refresh annotation for %s: %w", argoCDType, obj.GetName(), err)
			}
		} else {
			a := map[string]string{
				annotationKey: annotationValue,
			}
			err := utils.ApplyAnnotation(ctx, r.Client, obj, a, client.FieldOwner(v1alpha1.FieldManager))
			if err != nil {
				return fmt.Errorf("applying %s refresh annotation for %s: %w", argoCDType, obj.GetName(), err)
			}
		}
	}
	return nil
}

func getCustomPackageName(fileName, appName string) string {
	s := strings.Split(fileName, ".")
	return fmt.Sprintf("%s-%s", strings.ToLower(s[0]), appName)
}

func isSupportedArgoCDTypes(gvk *schema.GroupVersionKind) bool {
	if gvk == nil {
		return false
	}
	return gvk.Group == argocdapp.Group && (gvk.Kind == argocdapp.ApplicationKind || gvk.Kind == argocdapp.ApplicationSetKind)
}

func GetEmbeddedRawInstallResources(name string, templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	switch name {
	case v1alpha1.ArgoCDPackageName:
		// Still need to resolve lb.RawArgocdInstallResources
		// return lb.RawArgocdInstallResources(templateData, config, scheme)
	case v1alpha1.GiteaPackageName:
		// Still need to resolve lb.RawGiteaInstallResources
		// return lb.RawGiteaInstallResources(templateData, config, scheme)
	case v1alpha1.IngressNginxPackageName:
		// Still need to resolve lb.RawNginxInstallResources
		// return lb.RawNginxInstallResources(templateData, config, scheme)
	default:
		return nil, fmt.Errorf("unsupported embedded app name %s", name)
	}
	// Added temporary return to allow compilation check
	return nil, fmt.Errorf("Raw* functions not yet implemented/found")
}
