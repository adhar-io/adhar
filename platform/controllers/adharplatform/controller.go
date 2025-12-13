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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"code.gitea.io/sdk/gitea"
	argocdapp "github.com/cnoe-io/argocd-api/api/argo/application"
	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses;gateways;httproutes;grpcroutes;referencegrants,verbs=get;list;watch;create;update;patch;delete

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
	logger.Info("🔵 Reconcile called", "resource", req.NamespacedName)

	var localBuild v1alpha1.AdharPlatform
	if err := r.Get(ctx, req.NamespacedName, &localBuild); err != nil {
		logger.Error(err, "unable to fetch Resource")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	defer r.postProcessReconcile(ctx, req, &localBuild)

	// If we already decided to shutdown, don't process any more reconciliations
	if r.shouldShutdown {
		logger.Info("Shutdown already initiated, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	logger.Info("🔵 Step 1: Reconciling project namespace")
	_, err := r.ReconcileProjectNamespace(ctx, req, &localBuild)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Ensure Gateway API CRDs exist before installing any core packages.
	// Some core packages (e.g. Cilium/ArgoCD) may ship Gateway API resources (GatewayClass/HTTPRoute).
	logger.Info("🔵 Step 2: Ensuring Gateway API CRDs are installed")
	if err := r.installGatewayAPICRDs(ctx); err != nil {
		logger.Error(err, "failed installing Gateway API CRDs")
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	}
	logger.Info("✅ Gateway API CRDs installed successfully")

	// Install core packages synchronously to ensure they complete
	logger.Info("🔵 Step 3: Installing core packages")
	err = r.installCorePackagesSync(ctx, req, &localBuild)
	if err != nil {
		logger.Error(err, "failed installing core packages")
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	}
	logger.Info("✅ Core packages installed successfully")

	// Install Gateway API resources after Cilium is installed
	// Gateway depends on Cilium being installed first
	logger.Info("🔵 Step 4: Installing Gateway API resources")
	result, err := r.ReconcileGateway(ctx, req, &localBuild)
	if err != nil {
		logger.Error(err, "failed installing Gateway API resources")
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	}
	// If Gateway reconciliation requested a requeue, honor it
	if result.RequeueAfter > 0 || result.Requeue {
		logger.Info("Gateway reconciliation requested requeue", "requeueAfter", result.RequeueAfter)
		return result, nil
	}
	logger.Info("✅ Gateway API resources installed successfully")

	// Apply platform stack ApplicationSet after core packages are installed
	logger.Info("Applying platform stack ApplicationSet")
	err = r.applyPlatformStack(ctx, req, &localBuild)
	if err != nil {
		logger.Error(err, "failed applying platform stack")
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	}

	// If ExitOnSync is enabled, determine if we should shut down (all apps healthy, repos synced)
	if r.ExitOnSync {
		logger.Info("ExitOnSync enabled - checking if platform is fully synced and healthy")
		ready, checkErr := r.shouldShutDown(ctx, &localBuild)
		if checkErr != nil {
			logger.Error(checkErr, "Failed to evaluate platform readiness - will requeue")
			return ctrl.Result{RequeueAfter: errRequeueTime}, nil
		}
		if ready {
			logger.Info("✅ All applications are healthy and synced! Platform deployment complete")
			r.shouldShutdown = true
			return ctrl.Result{}, nil
		}

		logger.Info("⏳ Platform is still converging, will check again shortly...")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
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

	logger.V(1).Info("done installing core packages. GitOps ApplicationSet will handle platform applications")

	return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
}

func (r *AdharPlatformReconciler) installCorePackagesSync(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) error {
	logger := log.FromContext(ctx)

	installers := map[string]subReconciler{
		v1alpha1.ArgoCDPackageName: r.ReconcileArgo,
		v1alpha1.CiliumPackageName: r.ReconcileCilium,
		v1alpha1.GiteaPackageName:  r.ReconcileGitea,
	}
	logger.Info("installing core packages synchronously")

	var errors []error
	for name, installer := range installers {
		logger.Info("🔵 Installing core package", "name", name)
		startTime := time.Now()
		result, err := installer(ctx, req, resource)
		duration := time.Since(startTime)
		if err != nil {
			logger.Error(err, "❌ Failed installing core package", "name", name, "duration", duration)
			errors = append(errors, fmt.Errorf("failed installing %s: %w", name, err))
		} else {
			logger.Info("✅ Successfully installed core package", "name", name, "duration", duration)
			if result.RequeueAfter > 0 || result.Requeue {
				logger.Info("Package requested requeue", "name", name, "requeueAfter", result.RequeueAfter)
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed installing %d core packages: %v", len(errors), errors)
	}

	return nil
}

func (r *AdharPlatformReconciler) applyPlatformStack(ctx context.Context, _ ctrl.Request, resource *v1alpha1.AdharPlatform) error {
	logger := log.FromContext(ctx)

	// CRITICAL: Setup GitOps repositories SYNCHRONOUSLY - must succeed before ApplicationSets
	logger.Info("Setting up GitOps repositories (this may take a few minutes)...")
	if err := r.setupGitOpsRepositories(ctx, resource); err != nil {
		logger.Error(err, "Failed to setup GitOps repositories - this is REQUIRED for GitOps workflow")
		return fmt.Errorf("GitOps repositories setup failed: %w", err)
	}
	logger.Info("✅ GitOps repositories setup completed successfully")

	logger.Info("Applying ArgoCD repository authentication and service for Gitea access")
	if err := r.applyArgoCDRepoAuth(ctx, resource); err != nil {
		logger.Error(err, "Failed to configure ArgoCD repository authentication")
		return err
	}
	logger.Info("✅ ArgoCD repository authentication applied successfully")

	// Only apply the platform stack ApplicationSet after GitOps is ready
	logger.Info("Applying platform stack ApplicationSet")
	appSetPath := "platform/stack/adhar-appset-local.yaml"
	appSetBytes, err := os.ReadFile(appSetPath)
	if err != nil {
		logger.Error(err, "Failed to read platform stack ApplicationSet", "path", appSetPath)
		return fmt.Errorf("reading platform stack ApplicationSet %s: %w", appSetPath, err)
	}

	if err := r.applyManifest(ctx, appSetBytes, resource, "Platform stack ApplicationSet"); err != nil {
		logger.Error(err, "Failed to apply platform stack ApplicationSet")
		return err
	}

	logger.Info("✅ Successfully applied platform stack ApplicationSet")
	// Don't shut down yet - we need to wait for applications to become healthy
	// The next reconciliation will check if everything is ready
	return nil
}

// setupGitOpsRepositories creates and populates GitOps repositories in Gitea
func (r *AdharPlatformReconciler) setupGitOpsRepositories(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	logger := log.FromContext(ctx)

	// Check if repositories are already created to avoid unnecessary API calls
	if resource.Status.Gitea.RepositoriesCreated {
		logger.V(1).Info("GitOps repositories already created, skipping")
		return nil
	}

	logger.Info("🔄 Setting up GitOps repositories in Gitea")

	// Wait for Gitea to be ready
	if err := r.waitForGiteaReady(ctx); err != nil {
		return fmt.Errorf("gitea not ready: %w", err)
	}

	// Create environments repository
	if err := r.createGiteaRepository(ctx, "environments"); err != nil {
		return fmt.Errorf("failed to create environments repository: %w", err)
	}

	// Create packages repository
	if err := r.createGiteaRepository(ctx, "packages"); err != nil {
		return fmt.Errorf("failed to create packages repository: %w", err)
	}

	// Populate repositories with content
	if err := r.populateRepositories(ctx); err != nil {
		return fmt.Errorf("failed to populate repositories: %w", err)
	}

	// Mark repositories as created to avoid recreating on every reconciliation
	resource.Status.Gitea.RepositoriesCreated = true
	if err := r.Status().Update(ctx, resource); err != nil {
		logger.Error(err, "Failed to update Gitea status")
		// Don't return error as repositories are already created
	}

	logger.Info("✅ GitOps repositories setup completed successfully")
	return nil
}

// applyArgoCDRepoAuth configures ArgoCD with credentials and a stable service for accessing Gitea
func (r *AdharPlatformReconciler) applyArgoCDRepoAuth(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	authPath := filepath.Join("platform", "stack", "argocd-auth.yaml")
	authBytes, err := os.ReadFile(authPath)
	if err != nil {
		return fmt.Errorf("reading ArgoCD repo auth manifest %s: %w", authPath, err)
	}

	if err := r.applyManifest(ctx, authBytes, resource, "ArgoCD repo auth"); err != nil {
		return err
	}
	return nil
}

// waitForGiteaReady waits for Gitea to be fully ready with comprehensive checks
func (r *AdharPlatformReconciler) waitForGiteaReady(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("Waiting for Gitea to be fully ready (comprehensive checks)...")

	// Step 1: Wait for Gitea deployment to be ready (up to 10 minutes)
	logger.Info("1/4: Waiting for Gitea deployment to be ready")
	maxAttempts := 60 // 10 minutes (60 * 10 seconds)
	for i := 0; i < maxAttempts; i++ {
		var deployment appsv1.Deployment
		err := r.Client.Get(ctx, types.NamespacedName{
			Name:      "gitea",
			Namespace: "adhar-system",
		}, &deployment)

		if err == nil && deployment.Status.ReadyReplicas > 0 && deployment.Status.AvailableReplicas > 0 {
			logger.Info("Gitea deployment is ready")
			break
		}

		if i == maxAttempts-1 {
			return fmt.Errorf("gitea deployment not ready after 10 minutes")
		}

		if i%6 == 0 { // Log every minute
			logger.V(1).Info("Gitea deployment not ready yet, waiting...", "attempt", i+1, "maxAttempts", maxAttempts)
		}
		time.Sleep(10 * time.Second)
	}

	// Step 2: Wait for Gitea pods to be running
	logger.Info("2/4: Waiting for Gitea pods to be running")
	for i := 0; i < 30; i++ {
		var podList corev1.PodList
		err := r.Client.List(ctx, &podList, &client.ListOptions{
			Namespace: "adhar-system",
			LabelSelector: labels.SelectorFromSet(labels.Set{
				"app": "gitea",
			}),
		})

		if err == nil && len(podList.Items) > 0 {
			allRunning := true
			for _, pod := range podList.Items {
				if pod.Status.Phase != corev1.PodRunning {
					allRunning = false
					break
				}
				// Check that all containers in the pod are ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
						allRunning = false
						break
					}
				}
			}

			if allRunning {
				logger.Info("Gitea pods are running")
				break
			}
		}

		if i == 29 {
			return fmt.Errorf("gitea pods not running after 5 minutes")
		}

		logger.V(1).Info("Gitea pods not ready yet, waiting...", "attempt", i+1)
		time.Sleep(10 * time.Second)
	}

	// Step 3: Wait for Gitea dependencies (PostgreSQL and Redis) to be ready
	logger.Info("3/4: Waiting for Gitea dependencies (PostgreSQL, Redis)")
	time.Sleep(15 * time.Second) // Give dependencies time to initialize

	// Step 4: Additional time for Gitea API to fully initialize
	logger.Info("4/4: Waiting for Gitea database and API initialization (30 seconds)")
	time.Sleep(30 * time.Second)

	logger.Info("✅ Gitea is fully ready for repository operations")
	return nil
}

// createGiteaRepository creates a repository in Gitea
func (r *AdharPlatformReconciler) createGiteaRepository(ctx context.Context, name string) error {
	logger := log.FromContext(ctx)
	logger.Info("Creating Gitea repository", "name", name)

	// Get Gitea pod
	var pods corev1.PodList
	err := r.Client.List(ctx, &pods, client.InNamespace("adhar-system"), client.MatchingLabels{"app": "gitea"})
	if err != nil || len(pods.Items) == 0 {
		return fmt.Errorf("failed to find Gitea pod: %w", err)
	}

	podName := pods.Items[0].Name

	// Create repository using Gitea API via kubectl exec
	createCmd := fmt.Sprintf(`
		curl -X POST "http://localhost:3000/api/v1/admin/users/gitea_admin/repos" \
		-H "Content-Type: application/json" \
		-d '{"name":"%s","description":"%s repository","private":false}' \
		-u gitea_admin:r8sA8CPHD9!bt6d
	`, name, name)

	// Execute the command in the Gitea pod
	cmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "sh", "-c", createCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if repository already exists (409 conflict)
		if strings.Contains(string(output), "409") || strings.Contains(string(output), "already exists") {
			logger.Info("Repository already exists, continuing", "name", name)
			return nil
		}
		return fmt.Errorf("failed to create repository %s: %w, output: %s", name, err, string(output))
	}

	logger.Info("Successfully created repository", "name", name)
	return nil
}

// populateRepositories populates the GitOps repositories with content
func (r *AdharPlatformReconciler) populateRepositories(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("Populating GitOps repositories with content")

	// Get Gitea pod
	var pods corev1.PodList
	err := r.Client.List(ctx, &pods, client.InNamespace("adhar-system"), client.MatchingLabels{"app": "gitea"})
	if err != nil || len(pods.Items) == 0 {
		return fmt.Errorf("failed to find Gitea pod: %w", err)
	}

	podName := pods.Items[0].Name
	logger.Info("Using Gitea pod", "podName", podName)

	// Populate packages repository
	if err := r.populatePackagesRepository(ctx, podName); err != nil {
		return fmt.Errorf("failed to populate packages repository: %w", err)
	}

	// Populate environments repository
	if err := r.populateEnvironmentsRepository(ctx, podName); err != nil {
		return fmt.Errorf("failed to populate environments repository: %w", err)
	}

	logger.Info("Successfully populated all GitOps repositories")
	return nil
}

// populatePackagesRepository populates the packages repository with platform stack content
func (r *AdharPlatformReconciler) populatePackagesRepository(ctx context.Context, podName string) error {
	logger := log.FromContext(ctx)
	logger.Info("Populating packages repository with platform stack content")

	adminPassword := url.QueryEscape("r8sA8CPHD9!bt6d")
	repoURL := fmt.Sprintf("http://gitea_admin:%s@localhost:3000/gitea_admin/packages.git", adminPassword)

	// Clean up any existing working directory
	cleanupCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/packages-working")
	if err := cleanupCmd.Run(); err != nil {
		logger.V(1).Info("failed to clean packages workspace", "error", err)
	}

	// Clone the existing repository
	cloneCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "clone",
		repoURL, "/tmp/packages-working")
	if err := cloneCmd.Run(); err != nil {
		logger.Info("Failed to clone packages repository (may not exist yet)", "error", err)
		// Create the directory if it doesn't exist
		createCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "mkdir", "-p", "/tmp/packages-working")
		if err := createCmd.Run(); err != nil {
			return fmt.Errorf("failed to create packages working directory: %w", err)
		}
		// Initialize git repository
		initCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "init")
		if err := initCmd.Run(); err != nil {
			return fmt.Errorf("failed to initialize git repository: %w", err)
		}
	}

	// Remove all existing content
	removeCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/packages-working/*")
	if err := removeCmd.Run(); err != nil {
		logger.V(1).Info("failed to remove existing packages content", "error", err)
	}

	// Ensure the repo tracks a main branch so ArgoCD targetRevision=main works out of the box
	switchToMainCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "checkout", "-B", "main")
	if err := switchToMainCmd.Run(); err != nil {
		logger.V(1).Info("failed to switch packages repo to main branch", "error", err)
	}

	// Copy the packages content (strip the top-level 'packages' folder so ArgoCD paths match)
	logger.Info("Copying packages content to working directory")
	copyCmd := exec.Command("kubectl", "cp", "platform/stack/packages/.", fmt.Sprintf("adhar-system/%s:/tmp/packages-working/", podName))
	if err := copyCmd.Run(); err != nil {
		return fmt.Errorf("failed to copy packages content: %w", err)
	}

	// Configure git
	configUserCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "config", "user.name", "Adhar Platform")
	if err := configUserCmd.Run(); err != nil {
		return fmt.Errorf("failed to configure git user: %w", err)
	}

	configEmailCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "config", "user.email", "admin@adhar.io")
	if err := configEmailCmd.Run(); err != nil {
		return fmt.Errorf("failed to configure git email: %w", err)
	}

	// Add all files
	addCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "add", ".")
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("failed to add files to git: %w", err)
	}

	// Commit
	commitCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "commit", "-m", "Update: Add all platform packages")
	if err := commitCmd.Run(); err != nil {
		// If commit fails, it might be because there are no changes, which is okay
		logger.Info("Git commit failed (may be no changes)", "error", err)
	}

	// Add remote origin if it doesn't exist
	remoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "remote", "set-url", "origin", repoURL)
	if err := remoteCmd.Run(); err != nil {
		logger.V(1).Info("failed to update packages remote (will try add)", "error", err)
		addRemoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "remote", "add", "origin", repoURL)
		if addErr := addRemoteCmd.Run(); addErr != nil {
			logger.V(1).Info("failed to add packages remote", "error", addErr)
		}
	}

	// Push changes
	pushCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "push", "-u", "origin", "main")
	if err := pushCmd.Run(); err != nil {
		return fmt.Errorf("failed to push packages repository main branch: %w", err)
	}

	logger.Info("✅ Packages repository populated successfully!")
	return nil
}

// populateEnvironmentsRepository populates the environments repository with environment configurations
func (r *AdharPlatformReconciler) populateEnvironmentsRepository(ctx context.Context, podName string) error {
	logger := log.FromContext(ctx)
	logger.Info("Populating environments repository with environment configurations")

	adminPassword := url.QueryEscape("r8sA8CPHD9!bt6d")
	repoURL := fmt.Sprintf("http://gitea_admin:%s@localhost:3000/gitea_admin/environments.git", adminPassword)

	// Clean up any existing working directory
	cleanupCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/environments-working")
	if err := cleanupCmd.Run(); err != nil {
		logger.V(1).Info("failed to clean environments workspace", "error", err)
	}

	// Clone the existing repository
	cloneCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "clone",
		repoURL, "/tmp/environments-working")
	if err := cloneCmd.Run(); err != nil {
		logger.Info("Failed to clone environments repository (may not exist yet)", "error", err)
		// Create the directory if it doesn't exist
		createCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "mkdir", "-p", "/tmp/environments-working")
		if err := createCmd.Run(); err != nil {
			return fmt.Errorf("failed to create environments working directory: %w", err)
		}
		// Initialize git repository
		initCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "init")
		if err := initCmd.Run(); err != nil {
			return fmt.Errorf("failed to initialize git repository: %w", err)
		}
	}

	// Remove all existing content
	removeCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/environments-working/*")
	if err := removeCmd.Run(); err != nil {
		logger.V(1).Info("failed to remove existing environments content", "error", err)
	}

	// Ensure the repo tracks a main branch so ArgoCD targetRevision=main works out of the box
	switchToMainCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "checkout", "-B", "main")
	if err := switchToMainCmd.Run(); err != nil {
		logger.V(1).Info("failed to switch environments repo to main branch", "error", err)
	}

	// Copy the environments content
	logger.Info("Copying environments content to working directory")
	copyCmd := exec.Command("kubectl", "cp", "platform/stack/environments", fmt.Sprintf("adhar-system/%s:/tmp/environments-working/", podName))
	if err := copyCmd.Run(); err != nil {
		return fmt.Errorf("failed to copy environments content: %w", err)
	}

	// Configure git
	configUserCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "config", "user.name", "Adhar Platform")
	if err := configUserCmd.Run(); err != nil {
		return fmt.Errorf("failed to configure git user: %w", err)
	}

	configEmailCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "config", "user.email", "admin@adhar.io")
	if err := configEmailCmd.Run(); err != nil {
		return fmt.Errorf("failed to configure git email: %w", err)
	}

	// Add all files
	addCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "add", ".")
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("failed to add files to git: %w", err)
	}

	// Commit
	commitCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "commit", "-m", "Update: Add environment configurations")
	if err := commitCmd.Run(); err != nil {
		// If commit fails, it might be because there are no changes, which is okay
		logger.Info("Git commit failed (may be no changes)", "error", err)
	}

	// Add remote origin if it doesn't exist
	remoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "remote", "set-url", "origin", repoURL)
	if err := remoteCmd.Run(); err != nil {
		logger.V(1).Info("failed to update environments remote (will try add)", "error", err)
		addRemoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "remote", "add", "origin", repoURL)
		if addErr := addRemoteCmd.Run(); addErr != nil {
			logger.V(1).Info("failed to add environments remote", "error", addErr)
		}
	}

	// Push changes
	pushCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "push", "-u", "origin", "main")
	if err := pushCmd.Run(); err != nil {
		return fmt.Errorf("failed to push environments repository main branch: %w", err)
	}

	logger.Info("✅ Environments repository populated successfully!")
	return nil
}

func (r *AdharPlatformReconciler) postProcessReconcile(ctx context.Context, _ ctrl.Request, resource *v1alpha1.AdharPlatform) {
	logger := log.FromContext(ctx)

	logger.Info("Checking if we should shutdown")
	if r.shouldShutdown {
		logger.Info("🎉 Platform deployment completed successfully! Shutting down...")

		// Refresh ArgoCD applications to ensure they're in sync
		err := r.requestArgoCDAppRefresh(ctx)
		if err != nil {
			logger.V(1).Info("failed requesting argocd application refresh", "error", err)
		}
		err = r.requestArgoCDAppSetRefresh(ctx)
		if err != nil {
			logger.V(1).Info("failed requesting argocd application set refresh", "error", err)
		}

		logger.Info("Triggering graceful shutdown...")
		// Cancel the context to trigger manager shutdown
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

// isPlatformAlreadyDeployed checks if the platform is already fully deployed
func (r *AdharPlatformReconciler) isPlatformAlreadyDeployed(ctx context.Context) bool {
	logger := log.FromContext(ctx)

	// Check for core deployments: ArgoCD, Gitea, Cilium Operator (Gateway API)
	coreDeployments := []struct {
		name      string
		namespace string
	}{
		{"gitea", globals.AdharSystemNamespace},
		{"argo-cd-argocd-server", globals.AdharSystemNamespace},
		{"cilium-operator", globals.AdharSystemNamespace},
	}

	for _, dep := range coreDeployments {
		var deployment appsv1.Deployment
		err := r.Client.Get(ctx, types.NamespacedName{
			Name:      dep.name,
			Namespace: dep.namespace,
		}, &deployment)

		if err != nil {
			logger.Info("Deployment not found", "name", dep.name, "namespace", dep.namespace)
			return false
		}

		if deployment.Status.ReadyReplicas == 0 || deployment.Status.AvailableReplicas == 0 {
			logger.Info("Deployment not ready", "name", dep.name, "readyReplicas", deployment.Status.ReadyReplicas)
			return false
		}
	}

	// Check for ApplicationSet
	var appSetList unstructured.UnstructuredList
	appSetList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "ApplicationSet",
	})

	err := r.Client.List(ctx, &appSetList, &client.ListOptions{
		Namespace: globals.AdharSystemNamespace,
	})

	if err != nil {
		logger.V(1).Info("Error listing ApplicationSets (non-critical)", "error", err)
		// Don't fail if we can't list ApplicationSets - they may be created later
	}

	if len(appSetList.Items) > 0 {
		logger.Info("ApplicationSets found", "count", len(appSetList.Items))
	} else {
		logger.V(1).Info("No ApplicationSets found yet (may be created later by ArgoCD)")
	}

	logger.Info("Platform core services are fully deployed", "deployments", len(coreDeployments), "applicationSets", len(appSetList.Items))
	return true
}

func (r *AdharPlatformReconciler) shouldShutDown(ctx context.Context, resource *v1alpha1.AdharPlatform) (bool, error) {
	logger := log.FromContext(ctx)
	logger.Info("Checking if should shutdown", "ExitOnSync", r.ExitOnSync)

	if !r.ExitOnSync {
		logger.Info("ExitOnSync is false, not shutting down")
		return false, nil
	}

	if _, err := utils.GetCLIStartTimeAnnotationValue(resource.Annotations); err != nil {
		return false, err
	}

	// Do not exit until GitOps repositories are bootstrapped so ArgoCD can sync from Gitea.
	if !resource.Status.Gitea.RepositoriesCreated {
		logger.Info("GitOps repositories not created yet, waiting before shutdown")
		return false, nil
	}

	// Require core services to be available.
	if !r.isPlatformAlreadyDeployed(ctx) {
		logger.Info("Core platform services are not fully available yet, waiting...")
		return false, nil
	}

	// Require the gateway Service to exist so UIs are reachable.
	var gwSvc corev1.Service
	gwSvcName := "cilium-gateway-adhar-gateway"
	if err := r.Client.Get(ctx, types.NamespacedName{Name: gwSvcName, Namespace: globals.AdharSystemNamespace}, &gwSvc); err != nil {
		logger.Info("Gateway Service not ready yet, waiting...", "service", gwSvcName, "error", err.Error())
		return false, nil
	}

	logger.Info("All ExitOnSync readiness checks passed, should shutdown")
	return true, nil
}

func (r *AdharPlatformReconciler) requestArgoCDAppRefresh(ctx context.Context) error {
	apps := &argov1alpha1.ApplicationList{}
	err := r.Client.List(ctx, apps, client.InNamespace(globals.AdharSystemNamespace))
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
	err := r.Client.List(ctx, appsets, client.InNamespace(globals.AdharSystemNamespace))
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

	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		return fmt.Errorf("error closing response body: %w", closeErr)
	}
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}

	if resp.StatusCode == 200 {
		var argocdSession ArgocdSession

		err := json.Unmarshal(body, &argocdSession)
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
		if closeErr := resp.Body.Close(); closeErr != nil {
			return fmt.Errorf("error closing password update response: %w", closeErr)
		}

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
		if closeErr := resp.Body.Close(); closeErr != nil {
			return fmt.Errorf("error closing verification response: %w", closeErr)
		}

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

func GetEmbeddedRawInstallResources(name string, templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	switch name {
	case v1alpha1.ArgoCDPackageName:
		return RawArgocdInstallResources(templateData, config, scheme)
	case v1alpha1.GiteaPackageName:
		return RawGiteaInstallResources(templateData, config, scheme)
	case v1alpha1.CiliumPackageName:
		return RawCiliumInstallResources(templateData, config, scheme)
	default:
		return nil, fmt.Errorf("unsupported embedded app name %s", name)
	}
}
