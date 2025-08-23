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

package custompackage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	argocdapplication "github.com/cnoe-io/argocd-api/api/argo/application"
	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/utils"
)

const (
	requeueTime = time.Second * 30
)

// CustomPackageReconciler reconciles a CustomPackage object
type CustomPackageReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
	Config   v1alpha1.BuildCustomizationSpec
	TempDir  string
	RepoMap  *utils.RepoMap
}

// +kubebuilder:rbac:groups=platform.adhar.io,resources=custompackages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.adhar.io,resources=custompackages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.adhar.io,resources=custompackages/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CustomPackage object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *CustomPackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	pkg := v1alpha1.CustomPackage{}
	err := r.Get(ctx, req.NamespacedName, &pkg)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.V(1).Info("reconciling custom package", "name", req.Name, "namespace", req.Namespace)
	defer r.postProcessReconcile(ctx, req, &pkg)
	result, err := r.reconcileCustomPackage(ctx, &pkg)
	if err != nil {
		r.Recorder.Event(&pkg, "Warning", "reconcile error", err.Error())
	} else {
		r.Recorder.Event(&pkg, "Normal", "reconcile success", "Successfully reconciled")
	}

	return result, err
}

func (r *CustomPackageReconciler) postProcessReconcile(ctx context.Context, req ctrl.Request, pkg *v1alpha1.CustomPackage) {
	logger := log.FromContext(ctx)

	err := r.Status().Update(ctx, pkg)
	if err != nil {
		logger.Error(err, "failed updating repo status")
	}

	err = utils.UpdateSyncAnnotation(ctx, r.Client, pkg)
	if err != nil {
		logger.Error(err, "failed updating repo annotation")
	}
}

// create an in-cluster repository CR, update the application spec, then apply
func (r *CustomPackageReconciler) reconcileCustomPackage(ctx context.Context, resource *v1alpha1.CustomPackage) (ctrl.Result, error) {
	b, err := r.getArgoCDAppFile(ctx, resource)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading file %s: %w", resource.Spec.ArgoCD.ApplicationFile, err)
	}

	objs, err := k8s.ConvertYamlToObjects(r.Scheme, b)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("converting yaml to object %w", err)
	}
	if len(objs) == 0 {
		return ctrl.Result{}, fmt.Errorf("file contained 0 kubernetes objects %s", resource.Spec.ArgoCD.ApplicationFile)
	}

	switch resource.Spec.ArgoCD.Type {
	case argocdapplication.ApplicationKind:
		app, ok := objs[0].(*argov1alpha1.Application)
		if !ok {
			return ctrl.Result{}, fmt.Errorf("object is not an ArgoCD application %s", resource.Spec.ArgoCD.ApplicationFile)
		}
		utils.SetPackageLabels(app)

		res, err := r.reconcileArgoCDApp(ctx, resource, app)
		if err != nil {
			return ctrl.Result{}, err
		}

		foundAppObj := argov1alpha1.Application{}
		err = r.Client.Get(ctx, client.ObjectKeyFromObject(app), &foundAppObj)
		if err != nil {
			if errors.IsNotFound(err) {
				err = r.Client.Create(ctx, app)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("creating %s app CR: %w", app.Name, err)
				}

				return ctrl.Result{RequeueAfter: requeueTime}, nil
			}
			return ctrl.Result{}, fmt.Errorf("getting argocd application object: %w", err)
		}
		utils.SetPackageLabels(&foundAppObj)
		foundAppObj.Spec = app.Spec
		foundAppObj.ObjectMeta.Annotations = app.GetAnnotations()
		foundAppObj.ObjectMeta.Labels = app.GetLabels()
		err = r.Client.Update(ctx, &foundAppObj)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating argocd application object %s: %w", app.Name, err)
		}
		return res, nil

	case argocdapplication.ApplicationSetKind:
		// application set embeds application spec. extract it then handle git generator repoURLs.
		appSet, ok := objs[0].(*argov1alpha1.ApplicationSet)
		if !ok {
			return ctrl.Result{}, fmt.Errorf("object is not an ArgoCD application set %s", resource.Spec.ArgoCD.ApplicationFile)
		}

		utils.SetPackageLabels(appSet)

		res, err := r.reconcileArgoCDAppSet(ctx, resource, appSet)
		if err != nil {
			return ctrl.Result{}, err
		}

		foundAppSetObj := argov1alpha1.ApplicationSet{}
		err = r.Client.Get(ctx, client.ObjectKeyFromObject(appSet), &foundAppSetObj)
		if err != nil {
			if errors.IsNotFound(err) {
				err = r.Client.Create(ctx, appSet)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("creating %s argocd application set CR: %w", appSet.Name, err)
				}
				return ctrl.Result{RequeueAfter: requeueTime}, nil
			}
			return ctrl.Result{}, fmt.Errorf("getting argocd application set object: %w", err)
		}

		utils.SetPackageLabels(&foundAppSetObj)
		foundAppSetObj.Spec = appSet.Spec
		foundAppSetObj.ObjectMeta.Annotations = appSet.GetAnnotations()
		foundAppSetObj.ObjectMeta.Labels = appSet.GetLabels()
		err = r.Client.Update(ctx, &foundAppSetObj)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating argocd application object %s: %w", appSet.Name, err)
		}
		return res, nil

	default:
		return ctrl.Result{}, fmt.Errorf("file is not a supported argocd kind %s", resource.Spec.ArgoCD.ApplicationFile)
	}
}

func (r *CustomPackageReconciler) reconcileArgoCDApp(ctx context.Context, resource *v1alpha1.CustomPackage, app *argov1alpha1.Application) (ctrl.Result, error) {
	appSourcesSynced := true
	repoRefs := make([]v1alpha1.ObjectRef, 0, 1)
	if app.Spec.HasMultipleSources() {
		notSyncedRepos := 0
		for j := range app.Spec.Sources {
			s := &app.Spec.Sources[j]
			res, sErr := r.reconcileHelmValueObject(ctx, s, resource, app.Name)
			if sErr != nil {
				return res, sErr
			}

			res, repo, sErr := r.reconcileArgoCDSource(ctx, resource, s.RepoURL, app.Name)
			if sErr != nil {
				return res, sErr
			}

			if repo != nil {
				if repo.Status.InternalGitRepositoryUrl == "" {
					notSyncedRepos += 1
				}
				s.RepoURL = repo.Status.InternalGitRepositoryUrl
				repoRefs = append(repoRefs, v1alpha1.ObjectRef{
					Namespace: repo.Namespace,
					Name:      repo.Name,
					UID:       string(repo.ObjectMeta.UID),
				})
			}
		}
		appSourcesSynced = notSyncedRepos == 0
	} else {
		s := app.Spec.Source
		res, sErr := r.reconcileHelmValueObject(ctx, s, resource, app.Name)
		if sErr != nil {
			return res, sErr
		}

		res, repo, sErr := r.reconcileArgoCDSource(ctx, resource, s.RepoURL, app.Name)
		if sErr != nil {
			return res, sErr
		}

		if repo != nil {
			appSourcesSynced = repo.Status.InternalGitRepositoryUrl != ""
			s.RepoURL = repo.Status.InternalGitRepositoryUrl
			repoRefs = append(repoRefs, v1alpha1.ObjectRef{
				Namespace: repo.Namespace,
				Name:      repo.Name,
				UID:       string(repo.ObjectMeta.UID),
			})
		}
	}
	resource.Status.GitRepositoryRefs = repoRefs
	resource.Status.Synced = appSourcesSynced
	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

func (r *CustomPackageReconciler) reconcileArgoCDAppSet(ctx context.Context, resource *v1alpha1.CustomPackage, appSet *argov1alpha1.ApplicationSet) (ctrl.Result, error) {
	notSyncedRepos := 0
	for i := range appSet.Spec.Generators {
		g := appSet.Spec.Generators[i]
		if g.Git != nil {
			res, repo, gErr := r.reconcileArgoCDSource(ctx, resource, g.Git.RepoURL, appSet.GetName())
			if gErr != nil {
				return res, fmt.Errorf("reconciling git generator URL %s, %s: %w", g.Git.RepoURL, resource.Spec.ArgoCD.ApplicationFile, gErr)
			}
			if repo != nil {
				g.Git.RepoURL = repo.Status.InternalGitRepositoryUrl
				if repo.Status.InternalGitRepositoryUrl == "" {
					notSyncedRepos += 1
				}
			}
		}
		if g.Matrix != nil {
			for j := range g.Matrix.Generators {
				nestedGenerator := g.Matrix.Generators[j]
				if nestedGenerator.Git != nil {
					res, repo, gErr := r.reconcileArgoCDSource(ctx, resource, nestedGenerator.Git.RepoURL, appSet.GetName())
					if gErr != nil {
						return res, fmt.Errorf("reconciling git generator URL %s, %s: %w", nestedGenerator.Git.RepoURL, resource.Spec.ArgoCD.ApplicationFile, gErr)
					}
					if repo != nil {
						nestedGenerator.Git.RepoURL = repo.Status.InternalGitRepositoryUrl
						if repo.Status.InternalGitRepositoryUrl == "" {
							notSyncedRepos += 1
						}
					}
				}
			}
		}
	}

	gitGeneratorsSynced := notSyncedRepos == 0
	app := argov1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: appSet.GetName(), Namespace: appSet.Namespace},
	}
	app.Spec = appSet.Spec.Template.Spec

	_, err := r.reconcileArgoCDApp(ctx, resource, &app)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling application set %s %w", resource.Spec.ArgoCD.ApplicationFile, err)
	}

	resource.Status.Synced = resource.Status.Synced && gitGeneratorsSynced

	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

// create a gitrepository custom resource, then let the git repository controller take care of the rest
func (r *CustomPackageReconciler) reconcileArgoCDSource(ctx context.Context, resource *v1alpha1.CustomPackage, repoUrl, appName string) (ctrl.Result, *v1alpha1.GitRepository, error) {
	// Since we're no longer using adhar:// scheme, treat all URLs as regular Git URLs
	// Return nil to indicate no special processing needed
	return ctrl.Result{}, nil, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CustomPackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.CustomPackage{}).
		Complete(r)
}

func (r *CustomPackageReconciler) getArgoCDAppFile(ctx context.Context, resource *v1alpha1.CustomPackage) ([]byte, error) {
	filePath := resource.Spec.ArgoCD.ApplicationFile

	if resource.Spec.RemoteRepository.Url == "" {
		return os.ReadFile(filePath)
	}

	cloneDir := utils.RepoDir(resource.Spec.RemoteRepository.Url, r.TempDir)
	st := r.RepoMap.LoadOrStore(resource.Spec.RemoteRepository.Url, cloneDir)
	st.MU.Lock()
	wt, _, err := utils.CloneRemoteRepoToDir(ctx, resource.Spec.RemoteRepository, 1, false, cloneDir, "")
	defer st.MU.Unlock()
	if err != nil {
		return nil, fmt.Errorf("cloning repo, %s: %w", resource.Spec.RemoteRepository.Url, err)
	}
	return utils.ReadWorktreeFile(wt, filePath)
}

func (r *CustomPackageReconciler) reconcileHelmValueObject(ctx context.Context, source *argov1alpha1.ApplicationSource,
	resource *v1alpha1.CustomPackage, appName string,
) (ctrl.Result, error) {
	if source.Helm == nil || source.Helm.ValuesObject == nil {
		return ctrl.Result{}, nil
	}

	var data any
	err := json.Unmarshal(source.Helm.ValuesObject.Raw, &data)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing helm valuesObject: %w", err)
	}

	res, err := r.reconcileHelmValueObjectSource(ctx, &data, resource, appName)
	if err != nil {
		return ctrl.Result{}, err
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("converting helm valuesObject to json")
	}

	source.Helm.ValuesObject.Raw = raw
	return res, nil
}

func (r *CustomPackageReconciler) reconcileHelmValueObjectSource(ctx context.Context,
	valueObject *any, resource *v1alpha1.CustomPackage, appName string,
) (ctrl.Result, error) {

	switch val := (*valueObject).(type) {
	case string:
		res, repo, err := r.reconcileArgoCDSource(ctx, resource, val, appName)
		if err != nil {
			return res, fmt.Errorf("processing %s in helmValueObject: %w", val, err)
		}
		if repo != nil {
			*valueObject = repo.Status.InternalGitRepositoryUrl
		}
	case map[string]any:
		for k := range val {
			v := val[k]
			res, err := r.reconcileHelmValueObjectSource(ctx, &v, resource, appName)
			if err != nil {
				return res, err
			}
			val[k] = v
		}
	case []any:
		for k := range val {
			v := val[k]
			res, err := r.reconcileHelmValueObjectSource(ctx, &v, resource, appName)
			if err != nil {
				return res, err
			}
			val[k] = v
		}
	}
	return ctrl.Result{RequeueAfter: requeueTime}, nil
}

func localRepoName(appName, dir string) string {
	return fmt.Sprintf("%s-%s", appName, filepath.Base(dir))
}

func remoteRepoName(appName, pathToPkg string, repo v1alpha1.RemoteRepositorySpec) string {
	return fmt.Sprintf("%s-%s", appName, filepath.Base(pathToPkg))
}
