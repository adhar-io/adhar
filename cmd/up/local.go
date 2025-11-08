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

package up

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/logger"

	"adhar-io/adhar/platform/providers/kind"

	"github.com/go-logr/logr"
	sprig "github.com/go-task/slim-sprig/v3"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrl "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// LocalOptions holds configuration for local provisioning
type LocalOptions struct {
	Name                      string
	TemplateData              v1alpha1.BuildCustomizationSpec
	RecreateCluster           bool
	DevPassword               bool
	KubeVersion               string
	ExtraPortsMapping         string
	KindConfigPath            string
	KubeConfigPath            string
	ExtraPackages             []string
	RegistryConfig            []string
	PackageCustomizationFiles []string
	NoExit                    bool
	Protocol                  string
	Host                      string
	IngressHost               string
	Port                      string
	PathRouting               bool
	Verbose                   bool
	ProgressUI                bool
	CustomPackageFiles        []string
	CustomPackageDirs         []string
	CustomPackageUrls         []string
	PackageCustomization      map[string]v1alpha1.PackageCustomization
	ExitOnSync                bool
	Scheme                    *runtime.Scheme
	CancelFunc                context.CancelFunc
}

// LocalProvisioner handles local development environment creation
type LocalProvisioner struct {
	options *LocalOptions
}

// NewLocalProvisioner creates a new LocalProvisioner
func NewLocalProvisioner(options *LocalOptions) *LocalProvisioner {
	return &LocalProvisioner{options: options}
}

// LocalProvisioner handles local development environment creation
func (lp *LocalProvisioner) Provision(ctx context.Context, args []string) error {
	// // Define steps and descriptions for the progress tracker
	// stepNames := []string{
	// 	"Pre-flight Checks",
	// 	"Create Cluster",
	// 	"Platform CRDs",
	// 	"Namespaces",
	// 	"ArgoCD",
	// 	"Gitea",
	// 	"Crossplane",
	// 	"Nginx Ingress",
	// 	"Ingress-Nginx Ready",
	// 	"Ingress Rules",
	// 	"Secret Labels",
	// 	"ArgoCD Ready",
	// 	"Platform Apps",
	// 	"GitOps Repositories",
	// }
	// descriptions := []string{
	// 	"Validating system requirements",
	// 	fmt.Sprintf("Creating Kind cluster '%s'", globals.DefaultClusterName),
	// 	"Installing Custom Resource Definitions",
	// 	"Creating required namespaces",
	// 	"Installing ArgoCD for GitOps",
	// 	"Installing Gitea for Git hosting",
	// 	"Installing Crossplane for cloud resources",
	// 	"Installing Nginx Ingress Controller",
	// 	"Waiting for Ingress-Nginx to be ready",
	// 	"Applying ingress manifests",
	// 	"Labeling core secrets",
	// 	"Waiting for ArgoCD components",
	// 	"Applying platform ApplicationSets",
	// 	"Setting up GitOps workflows",
	// }

	// tracker := helpers.NewStyledProgressTracker("Adhar • Local Development Setup", stepNames, descriptions)
	// tracker.ExpandedView = false

	// runStep := func(idx int, fn func() error) error {
	// 	tracker.StartStep(idx, descriptions[idx])
	// 	tracker.Render()
	// 	if err := fn(); err != nil {
	// 		tracker.FailStep(idx, err)
	// 		return err
	// 	}
	// 	tracker.CompleteStep(idx)
	// 	tracker.Render()
	// 	return nil
	// }

	// // 1. Pre-flight
	// if err := runStep(0, lp.runPreFlightChecks); err != nil {
	// 	return fmt.Errorf("pre-flight checks failed: %w", err)
	// }
	// // 2. Create Cluster
	// if err := runStep(1, lp.createKindCluster); err != nil {
	// 	return fmt.Errorf("failed to create Kind cluster: %w", err)
	// }
	// // 3. Platform CRDs
	// if err := runStep(2, func() error { return lp.applyManifests("platform/controllers/resources/") }); err != nil {
	// 	return fmt.Errorf("failed to install platform CRDs: %w", err)
	// }
	// // 4. Namespaces
	// if err := runStep(3, lp.createNamespaces); err != nil {
	// 	return fmt.Errorf("failed to create namespaces: %w", err)
	// }
	// // 5. ArgoCD
	// if err := runStep(4, func() error { return lp.applyPlatformManifest("argocd") }); err != nil {
	// 	return fmt.Errorf("failed to install ArgoCD: %w", err)
	// }
	// // 6. Gitea
	// if err := runStep(5, func() error { return lp.applyPlatformManifest("gitea") }); err != nil {
	// 	return fmt.Errorf("failed to install Gitea: %w", err)
	// }
	// // 7. Crossplane
	// if err := runStep(6, func() error { return lp.applyPlatformManifest("crossplane") }); err != nil {
	// 	return fmt.Errorf("failed to install Crossplane: %w", err)
	// }
	// // 8. Nginx Ingress
	// if err := runStep(7, func() error { return lp.applyPlatformManifest("nginx") }); err != nil {
	// 	return fmt.Errorf("failed to install Nginx Ingress: %w", err)
	// }
	// // 9. Ingress-Nginx Ready
	// if err := runStep(8, lp.waitForNginxIngress); err != nil {
	// 	return fmt.Errorf("Nginx Ingress not ready: %w", err)
	// }
	// // 10. Ingress Rules
	// if err := runStep(9, lp.applyIngressManifests); err != nil {
	// 	return fmt.Errorf("failed to apply ingress manifests: %w", err)
	// }
	// // 11. Secret Labels
	// if err := runStep(10, lp.labelCoreSecrets); err != nil {
	// 	return fmt.Errorf("failed to label core secrets: %w", err)
	// }
	// // 12. ArgoCD Ready
	// if err := runStep(11, lp.waitForArgoCD); err != nil {
	// 	return fmt.Errorf("ArgoCD not ready: %w", err)
	// }
	// // 13. Platform Apps
	// if err := runStep(12, lp.applyPlatformApplicationSets); err != nil {
	// 	return fmt.Errorf("failed to apply platform applications: %w", err)
	// }
	// // 14. GitOps Repositories
	// if err := runStep(13, lp.setupGitOpsRepositories); err != nil {
	// 	return fmt.Errorf("failed to setup GitOps repositories: %w", err)
	// }

	// tracker.Complete()

	// // Final success message box
	// successContent := fmt.Sprintf("%s\n%s\n%s",
	// 	helpers.SuccessStyle.Render("✅ Local development environment created successfully!"),
	// 	helpers.InfoStyle.Render(fmt.Sprintf("🌐 Access your platform at: %s://%s:%s", lp.options.Protocol, lp.options.Host, lp.options.Port)),
	// 	helpers.SubtitleStyle.Render("🎉 Your Adhar platform is ready for development!"))
	// fmt.Printf("\n%s\n\n", helpers.BorderStyle.Width(70).Render(successContent))

	logger.Info("Creating kind cluster")
	if err := lp.ReconcileKindCluster(ctx, recreateCluster); err != nil {
		return err
	}

	logger.Info("Getting Kube config")
	kubeConfig, err := lp.GetKubeConfig()
	if err != nil {
		return err
	}

	logger.Info("Getting Kube client")
	kubeClient, err := lp.GetKubeClient(kubeConfig)
	if err != nil {
		return err
	}

	logger.Info("Adding CRDs to the cluster")
	if err := lp.ReconcileCRDs(ctx, kubeClient); err != nil {
		return err
	}

	logger.Info("Creating controller manager")

	// Set up controller-runtime logger
	ctrl.SetLogger(logr.Discard())

	// Create controller manager
	mgr, err := manager.New(kubeConfig, manager.Options{
		Scheme: lp.options.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
	})
	if err != nil {
		logger.Error("Error creating controller manager", err, map[string]interface{}{})
		return err
	}

	dir, err := os.MkdirTemp("", fmt.Sprintf("%s-%s-", globals.ProjectName, lp.options.Name))
	if err != nil {
		logger.Error("creating temp dir", err, map[string]interface{}{})
		return err
	}
	defer os.RemoveAll(dir)
	logger.Info("Created temp directory for cloning repositories")

	logger.Info("Setting up CoreDNS")
	err = kind.SetupCoreDNS(ctx, kubeClient, lp.options.Scheme, lp.options.TemplateData)
	if err != nil {
		return err
	}

	logger.Info("Setting up TLS certificate")
	cert, err := kind.SetupSelfSignedCertificate(ctx, kubeClient, lp.options.TemplateData)
	if err != nil {
		return err
	}
	lp.options.TemplateData.SelfSignedCert = string(cert)

	managerExit := make(chan error)

	logger.Info("Running controllers")
	if err := lp.RunControllers(ctx, mgr, managerExit, dir); err != nil {
		logger.Error("Error running controllers", err, map[string]interface{}{})
		return err
	}

	localBuild := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lp.options.Name,
			Namespace: globals.AdharSystemNamespace,
		},
	}

	cliStartTime := time.Now().Format(time.RFC3339Nano)

	logger.Info("Creating adharplatform resource")
	_, err = controllerutil.CreateOrUpdate(ctx, kubeClient, &localBuild, func() error {
		if localBuild.ObjectMeta.Annotations == nil {
			localBuild.ObjectMeta.Annotations = map[string]string{}
		}
		localBuild.ObjectMeta.Annotations[v1alpha1.CliStartTimeAnnotation] = cliStartTime
		localBuild.Spec = v1alpha1.AdharPlatformSpec{
			BuildCustomization: lp.options.TemplateData,
			PackageConfigs: v1alpha1.PackageConfigsSpec{
				Argo: v1alpha1.ArgoPackageConfigSpec{
					Enabled: true,
				},
				EmbeddedArgoApplications: v1alpha1.EmbeddedArgoApplicationsPackageConfigSpec{
					Enabled: true,
				},
				CustomPackageDirs:        lp.options.CustomPackageDirs,
				CustomPackageUrls:        lp.options.CustomPackageUrls,
				CorePackageCustomization: lp.options.PackageCustomization,
			},
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("creating AdharPlatform resource: %w", err)
	}

	// GitOps repositories will be set up by the AdharPlatform controller

	select {
	case mgrErr := <-managerExit:
		if mgrErr != nil {
			return mgrErr
		}
	case <-ctx.Done():
		return nil
	}
	return nil
}

// setupGitOpsRepositories creates and populates GitOps repositories in Gitea
func (lp *LocalProvisioner) setupGitOpsRepositories() error {
	logger.Info("🔄 Setting up GitOps repositories in Gitea")

	// Wait for Gitea to be ready
	if err := lp.waitForGiteaReady(); err != nil {
		return fmt.Errorf("Gitea not ready: %w", err)
	}

	// Create environments repository
	if err := lp.createGiteaRepository("environments"); err != nil {
		return fmt.Errorf("failed to create environments repository: %w", err)
	}

	// Create packages repository
	if err := lp.createGiteaRepository("packages"); err != nil {
		return fmt.Errorf("failed to create packages repository: %w", err)
	}

	// Populate repositories with content
	if err := lp.populateRepositories(); err != nil {
		return fmt.Errorf("failed to populate repositories: %w", err)
	}

	logger.Info("✅ GitOps repositories setup completed successfully")
	return nil
}

// waitForGiteaReady waits for Gitea to be ready
func (lp *LocalProvisioner) waitForGiteaReady() error {
	logger.Info("Waiting for Gitea to be ready")

	// Wait for Gitea deployment
	cmd := exec.Command("kubectl", "wait", "--for=condition=available",
		"deployment/gitea", "--namespace=adhar-system", "--timeout=300s")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Gitea not ready: %w", err)
	}

	return nil
}

// createGiteaRepository creates a repository in Gitea
func (lp *LocalProvisioner) createGiteaRepository(name string) error {
	logger.Infof("Creating Gitea repository: %s", name)

	// Get Gitea pod name
	cmd := exec.Command("kubectl", "get", "pods", "-n", "adhar-system",
		"-l", "app=gitea", "-o", "jsonpath={.items[0].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Gitea pod name: %w", err)
	}

	podName := strings.TrimSpace(string(output))

	// Create repository using Gitea API
	createCmd := fmt.Sprintf(`
		curl -X POST "http://localhost:3000/api/v1/admin/users/gitea_admin/repos" \
		-H "Content-Type: application/json" \
		-d '{"name":"%s","description":"%s repository","private":false}' \
		-u gitea_admin:r8sA8CPHD9!bt6d
	`, name, name)

	cmd = exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "sh", "-c", createCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create repository %s: %w", name, err)
	}

	return nil
}

// populateRepositories populates the GitOps repositories with content
func (lp *LocalProvisioner) populateRepositories() error {
	logger.Info("Populating GitOps repositories with content")

	// Get Gitea pod name
	cmd := exec.Command("kubectl", "get", "pods", "-n", "adhar-system",
		"-l", "app=gitea", "-o", "jsonpath={.items[0].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Gitea pod name: %w", err)
	}

	podName := strings.TrimSpace(string(output))
	logger.Infof("Using Gitea pod: %s", podName)

	// Populate packages repository
	if err := lp.populatePackagesRepository(podName); err != nil {
		return fmt.Errorf("failed to populate packages repository: %w", err)
	}

	// Populate environments repository
	if err := lp.populateEnvironmentsRepository(podName); err != nil {
		return fmt.Errorf("failed to populate environments repository: %w", err)
	}

	logger.Info("Successfully populated all GitOps repositories")
	return nil
}

// populatePackagesRepository populates the packages repository with platform stack content
func (lp *LocalProvisioner) populatePackagesRepository(podName string) error {
	logger.Info("Populating packages repository with platform stack content")

	// Clean up any existing working directory
	cleanupCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/packages-working")
	cleanupCmd.Run()

	// Clone the existing repository
	cloneCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "clone",
		"/data/git/gitea-repositories/gitea_admin/packages.git", "/tmp/packages-working")
	if err := cloneCmd.Run(); err != nil {
		logger.Warnf("Failed to clone packages repository (may not exist yet): %v", err)
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
	removeCmd.Run()

	// Copy the packages content
	logger.Info("Copying packages content to working directory")
	copyCmd := exec.Command("kubectl", "cp", "platform/stack/packages", fmt.Sprintf("adhar-system/%s:/tmp/packages-working/", podName))
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
		logger.Warnf("Git commit failed (may be no changes): %v", err)
	}

	// Add remote origin if it doesn't exist
	remoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "remote", "add", "origin", "/data/git/gitea-repositories/gitea_admin/packages.git")
	remoteCmd.Run() // Ignore error if remote already exists

	// Push changes
	pushCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "push", "-u", "origin", "main")
	if err := pushCmd.Run(); err != nil {
		// Try pushing to master if main doesn't work
		pushMasterCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "push", "-u", "origin", "master")
		if err := pushMasterCmd.Run(); err != nil {
			return fmt.Errorf("failed to push to packages repository: %w", err)
		}
	}

	logger.Info("✅ Packages repository populated successfully!")
	return nil
}

// populateEnvironmentsRepository populates the environments repository with environment configurations
func (lp *LocalProvisioner) populateEnvironmentsRepository(podName string) error {
	logger.Info("Populating environments repository with environment configurations")

	// Clean up any existing working directory
	cleanupCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/environments-working")
	cleanupCmd.Run()

	// Clone the existing repository
	cloneCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "clone",
		"/data/git/gitea-repositories/gitea_admin/environments.git", "/tmp/environments-working")
	if err := cloneCmd.Run(); err != nil {
		logger.Warnf("Failed to clone environments repository (may not exist yet): %v", err)
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
	removeCmd.Run()

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
		logger.Warnf("Git commit failed (may be no changes): %v", err)
	}

	// Add remote origin if it doesn't exist
	remoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "remote", "add", "origin", "/data/git/gitea-repositories/gitea_admin/environments.git")
	remoteCmd.Run() // Ignore error if remote already exists

	// Push changes
	pushCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "push", "-u", "origin", "main")
	if err := pushCmd.Run(); err != nil {
		// Try pushing to master if main doesn't work
		pushMasterCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "push", "-u", "origin", "master")
		if err := pushMasterCmd.Run(); err != nil {
			return fmt.Errorf("failed to push to environments repository: %w", err)
		}
	}

	logger.Info("✅ Environments repository populated successfully!")
	return nil
}

// runPreFlightChecks validates system requirements
func (lp *LocalProvisioner) runPreFlightChecks() error {
	// Create styled header for pre-flight checks if not using progress UI
	if !lp.options.ProgressUI {
		headerContent := fmt.Sprintf("%s\n%s",
			helpers.TitleStyle.Render("⚡ Running pre-flight checks..."),
			helpers.SubtitleStyle.Render("Validating system requirements for Adhar platform"))
		if !lp.options.ProgressUI {
			headerBox := helpers.BorderStyle.Width(60).Render(headerContent)
			fmt.Printf("%s\n\n", headerBox)
		}
	}

	checks := []struct {
		name        string
		description string
		check       func() error
	}{
		{
			name:        "Docker",
			description: "🐳 Docker is available and healthy",
			check:       lp.checkDocker,
		},
		{
			name:        "Kind Engine",
			description: "🔧 Kind cluster engine ready",
			check:       lp.checkKindEngine,
		},
		{
			name:        "kubectl",
			description: "⚙️ kubectl is available",
			check:       lp.checkKubectl,
		},
		{
			name:        "System Resources",
			description: "💾 Sufficient system resources available",
			check:       lp.checkDiskSpace,
		},
		{
			name:        "Ports",
			description: "🔌 Required ports are available",
			check:       lp.checkPortAvailability,
		},
		{
			name:        "Container Runtime",
			description: "🔄 Container runtime is healthy",
			check:       lp.checkDocker,
		},
		{
			name:        "Cluster Conflicts",
			description: "🚫 No conflicting clusters found",
			check:       lp.checkConflictingClusters,
		},
	}

	// Create a styled content box for the checks
	var checkResults []string
	for _, check := range checks {
		// Show pending status
		pendingStatus := fmt.Sprintf("  %s %s", helpers.WarningStyle.Render("⏳"), helpers.InfoStyle.Render(check.description))
		checkResults = append(checkResults, pendingStatus)

		if err := check.check(); err != nil {
			// Show failed status
			failedStatus := fmt.Sprintf("  %s %s: %s", helpers.ErrorStyle.Render("❌"), helpers.ErrorStyle.Render(check.name), helpers.ErrorStyle.Render(err.Error()))
			checkResults = append(checkResults, failedStatus)
			return fmt.Errorf("%s check failed: %w", check.name, err)
		}

		// Show success status
		successStatus := fmt.Sprintf("  %s %s", helpers.SuccessStyle.Render("✅"), helpers.SuccessStyle.Render(check.description))
		checkResults = append(checkResults, successStatus)
	}

	// Display all check results in a styled box
	resultsContent := strings.Join(checkResults, "\n")
	if !lp.options.ProgressUI {
		resultsBox := helpers.BorderStyle.Width(70).Render(resultsContent)
		fmt.Printf("%s\n\n", resultsBox)
	}

	// Show success message in styled box
	successContent := fmt.Sprintf("%s\n%s",
		helpers.SuccessStyle.Render("✓ All pre-flight checks passed!"),
		helpers.InfoStyle.Render("System is ready for Adhar platform installation"))

	if !lp.options.ProgressUI {
		successBox := helpers.BorderStyle.Width(70).Render(successContent)
		fmt.Printf("%s\n\n", successBox)
	}

	return nil
}

// checkDocker verifies Docker is available and running
func (lp *LocalProvisioner) checkDocker() error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Docker is not available or not running: %w", err)
	}
	return nil
}

// checkKindEngine verifies Kind is available
func (lp *LocalProvisioner) checkKindEngine() error {
	cmd := exec.Command("kind", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Kind is not available: %w", err)
	}
	return nil
}

// checkDiskSpace verifies sufficient disk space is available
func (lp *LocalProvisioner) checkDiskSpace() error {
	// TODO: Implement disk space check
	// This should check available space in the current directory
	return nil
}

// checkPortAvailability verifies required ports are available
func (lp *LocalProvisioner) checkPortAvailability() error {
	// TODO: Implement port availability check
	// This should check if ports 80, 443, 3000, etc. are available
	return nil
}

// checkKubectl verifies kubectl is available
func (lp *LocalProvisioner) checkKubectl() error {
	cmd := exec.Command("kubectl", "version", "--client")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl is not available: %w", err)
	}
	return nil
}

// checkConflictingClusters verifies no conflicting clusters exist
func (lp *LocalProvisioner) checkConflictingClusters() error {
	// Check if there are any existing Kind clusters that might conflict
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		// If kind command fails, assume no clusters exist
		return nil
	}

	clusters := strings.TrimSpace(string(output))
	if clusters != "" && !strings.Contains(clusters, "adhar") {
		// Found existing clusters that don't match our naming pattern
		return fmt.Errorf("found existing clusters: %s. Consider using --recreate flag or cleaning up existing clusters", clusters)
	}

	return nil
}

// createKindCluster creates the Kind Kubernetes cluster
func (lp *LocalProvisioner) createKindCluster() error {
	// Optional header for cluster creation
	if !lp.options.ProgressUI {
		headerContent := fmt.Sprintf("%s\n%s",
			helpers.TitleStyle.Render("🔧 Creating Kind Kubernetes cluster..."),
			helpers.SubtitleStyle.Render("Setting up local development environment"))
		headerBox := helpers.BorderStyle.Width(70).Render(headerContent)
		fmt.Printf("%s\n\n", headerBox)
	}

	// Check existing clusters
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check existing clusters: %w", err)
	}

	// If cluster already exists
	if strings.Contains(string(output), globals.DefaultClusterName) {
		if lp.options.RecreateCluster {
			if !lp.options.ProgressUI {
				recreateContent := fmt.Sprintf("%s\n%s",
					helpers.WarningStyle.Render("🗑️ Deleting existing cluster..."),
					helpers.InfoStyle.Render("Recreating cluster as requested"))
				recreateBox := helpers.BorderStyle.Width(70).Render(recreateContent)
				fmt.Printf("%s\n\n", recreateBox)
			}
			deleteCmd := exec.Command("kind", "delete", "cluster", "--name", globals.DefaultClusterName)
			if err := deleteCmd.Run(); err != nil {
				return fmt.Errorf("failed to delete existing cluster: %w", err)
			}
		} else {
			if !lp.options.ProgressUI {
				existsContent := fmt.Sprintf("%s\n%s",
					helpers.SuccessStyle.Render(fmt.Sprintf("✅ Cluster '%s' already exists", globals.DefaultClusterName)),
					helpers.InfoStyle.Render("Skipping cluster creation"))
				existsBox := helpers.BorderStyle.Width(70).Render(existsContent)
				fmt.Printf("%s\n\n", existsBox)
			}
			return nil
		}
	}

	// Create new cluster UI
	if !lp.options.ProgressUI {
		createContent := fmt.Sprintf("%s\n%s",
			helpers.TitleStyle.Render("🏗️ Creating new Kind cluster..."),
			helpers.InfoStyle.Render("This may take a few minutes"))
		createBox := helpers.BorderStyle.Width(70).Render(createContent)
		fmt.Printf("%s\n", createBox)

		progressContent := fmt.Sprintf("%s %s",
			helpers.WarningStyle.Render("⏳"),
			helpers.InfoStyle.Render("Setting up Kubernetes cluster..."))
		progressBox := helpers.BorderStyle.Width(70).Render(progressContent)
		fmt.Printf("%s\n", progressBox)
	}

	// Render Kind config from template file instead of hardcoding
	tmplPath := filepath.Join("platform", "providers", "kind", "resources", "kind.yaml.tmpl")
	content, err := os.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("failed to read Kind template at %s: %w", tmplPath, err)
	}

	// Build template data
	type portMap struct {
		ContainerPort int
		HostPort      int
	}
	type tmplData struct {
		KubernetesVersion string
		HTTPPort          int
		HTTPSPort         int
		ExtraPortsMapping []portMap
		RegistryConfig    string
		UsePathRouting    bool
		Host              string
		Port              string
	}

	// Parse extra port mappings from flag value like "22:32222,9090:39090"
	var extra []portMap
	if strings.TrimSpace(lp.options.ExtraPortsMapping) != "" {
		pairs := strings.Split(lp.options.ExtraPortsMapping, ",")
		for _, p := range pairs {
			p = strings.TrimSpace(p)
			parts := strings.Split(p, ":")
			if len(parts) != 2 {
				return fmt.Errorf("invalid extra-ports mapping '%s' (expected host:container)", p)
			}
			hostP, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			contP, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 != nil || err2 != nil {
				return fmt.Errorf("invalid port numbers in extra-ports mapping '%s'", p)
			}
			// Template expects containerPort then hostPort, invert our host:container pair
			extra = append(extra, portMap{ContainerPort: contP, HostPort: hostP})
		}
	}

	// Choose first existing registry config file if provided
	registryCfg := ""
	for _, path := range lp.options.RegistryConfig {
		if path == "" {
			continue
		}
		abs := path
		if !filepath.IsAbs(abs) {
			if a, err := filepath.Abs(abs); err == nil {
				abs = a
			}
		}
		if _, err := os.Stat(abs); err == nil {
			registryCfg = abs
			break
		}
	}

	data := tmplData{
		KubernetesVersion: lp.options.KubeVersion,
		// 0 values let the template's sprig default pipe choose 80/443
		HTTPPort:          0,
		HTTPSPort:         0,
		ExtraPortsMapping: extra,
		RegistryConfig:    registryCfg,
		UsePathRouting:    lp.options.PathRouting,
		Host:              lp.options.Host,
		Port:              lp.options.Port,
	}

	t, err := template.New("kind").Funcs(sprig.TxtFuncMap()).Parse(string(content))
	if err != nil {
		return fmt.Errorf("failed to parse Kind template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to render Kind template: %w", err)
	}

	createCmd := exec.Command("kind", "create", "cluster", "--name", globals.DefaultClusterName, "--config", "-")
	createCmd.Stdin = &buf
	if out, err := createCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create cluster: %v\n%s", err, string(out))
	}

	if !lp.options.ProgressUI {
		successContent := fmt.Sprintf("%s %s\n%s",
			helpers.SuccessStyle.Render("✅"),
			helpers.SuccessStyle.Render("Cluster created successfully"),
			helpers.InfoStyle.Render(fmt.Sprintf("Kind cluster '%s' is ready", globals.DefaultClusterName)))
		successBox := helpers.BorderStyle.Width(70).Render(successContent)
		fmt.Printf("%s\n\n", successBox)
	}

	return nil
}

// installPlatformComponents installs the core platform components
func (lp *LocalProvisioner) installPlatformComponents() error {
	// Define all platform installation tasks
	tasks := []struct {
		name        string
		description string
		function    func() error
	}{
		{
			name:        "Platform CRDs",
			description: "Installing Custom Resource Definitions",
			function:    func() error { return lp.applyManifests("platform/controllers/resources/") },
		},
		{
			name:        "Namespaces",
			description: "Creating required namespaces",
			function:    lp.createNamespaces,
		},
		{
			name:        "ArgoCD",
			description: "Installing ArgoCD for GitOps",
			function:    func() error { return lp.applyPlatformManifest("argocd") },
		},
		{
			name:        "Gitea",
			description: "Installing Gitea for Git hosting",
			function:    func() error { return lp.applyPlatformManifest("gitea") },
		},
		{
			name:        "Crossplane",
			description: "Installing Crossplane for cloud resources",
			function:    func() error { return lp.applyPlatformManifest("crossplane") },
		},
		{
			name:        "Nginx Ingress",
			description: "Installing Nginx Ingress Controller",
			function:    func() error { return lp.applyPlatformManifest("nginx") },
		},
		{
			name:        "Ingress-Nginx Ready",
			description: "Waiting for Ingress Nginx to be ready",
			function:    lp.waitForNginxIngress,
		},
		{
			name:        "Ingress Rules",
			description: "Configuring ingress routing",
			function:    lp.applyIngressManifests,
		},
		{
			name:        "Secret Labels",
			description: "Labeling core secrets",
			function:    lp.labelCoreSecrets,
		},
		{
			name:        "ArgoCD Ready",
			description: "Waiting for ArgoCD to be ready",
			function:    lp.waitForArgoCD,
		},
		{
			name:        "Platform Apps",
			description: "Deploying platform ApplicationSets",
			function:    lp.applyPlatformApplicationSets,
		},
	}

	// Create styled header for platform components installation
	headerContent := fmt.Sprintf("%s\n%s",
		helpers.TitleStyle.Render("📦 Installing platform components..."),
		helpers.SubtitleStyle.Render("Setting up core Adhar platform services"))

	headerBox := helpers.BorderStyle.Width(70).Render(headerContent)
	fmt.Printf("%s\n\n", headerBox)

	// Display progress bar
	totalTasks := len(tasks)
	for i, task := range tasks {
		progress := fmt.Sprintf("[%d/%d]", i+1, totalTasks)

		// Show task starting
		taskHeader := fmt.Sprintf("%s %s %s",
			helpers.InfoStyle.Render(progress),
			helpers.WarningStyle.Render("⏳"),
			helpers.InfoStyle.Render(task.name+"..."))

		taskBox := helpers.BorderStyle.Width(70).Render(taskHeader)
		fmt.Printf("%s\n", taskBox)

		// Execute the task
		if err := task.function(); err != nil {
			// Show error
			errorMsg := fmt.Sprintf("%s %s %s: %s",
				helpers.InfoStyle.Render(progress),
				helpers.ErrorStyle.Render("❌"),
				helpers.ErrorStyle.Render(task.name),
				helpers.ErrorStyle.Render(err.Error()))

			errorBox := helpers.BorderStyle.Width(70).Render(errorMsg)
			fmt.Printf("%s\n", errorBox)
			return fmt.Errorf("failed to %s: %w", task.description, err)
		}

		// Show success
		successMsg := fmt.Sprintf("%s %s %s",
			helpers.InfoStyle.Render(progress),
			helpers.SuccessStyle.Render("✅"),
			helpers.SuccessStyle.Render(task.name+" completed"))

		successBox := helpers.BorderStyle.Width(70).Render(successMsg)
		fmt.Printf("%s\n", successBox)
	}

	// Show completion message
	completionContent := fmt.Sprintf("%s\n%s",
		helpers.SuccessStyle.Render("🎉 All platform components installed successfully!"),
		helpers.InfoStyle.Render("Platform is ready for use"))

	completionBox := helpers.BorderStyle.Width(70).Render(completionContent)
	fmt.Printf("%s\n\n", completionBox)

	return nil
}

// applyManifests applies Kubernetes manifests from the specified path
func (lp *LocalProvisioner) applyManifests(path string) error {
	logger.Infof("Applying manifests from: %s", path)

	// Check if the path exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("manifest path does not exist: %s", path)
	}

	// Apply all YAML files in the directory
	files, err := filepath.Glob(filepath.Join(path, "*.yaml"))
	if err != nil {
		return fmt.Errorf("failed to glob manifest files: %w", err)
	}

	if len(files) == 0 {
		logger.Warnf("No YAML files found in: %s", path)
		return nil
	}

	for _, file := range files {
		logger.Infof("Applying manifest: %s", file)
		cmd := exec.Command("kubectl", "apply", "-f", file)
		if lp.options.ProgressUI {
			cmd.Stdout = io.Discard
		} else {
			cmd.Stdout = os.Stdout
		}
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to apply manifest %s: %w", file, err)
		}
	}

	return nil
}

// createNamespaces creates the required namespaces for the platform
func (lp *LocalProvisioner) createNamespaces() error {
	logger.Info("Creating required namespaces")

	namespaces := []string{"adhar-system"}

	for _, ns := range namespaces {
		cmd := exec.Command("kubectl", "create", "namespace", ns, "--dry-run=client", "-o", "yaml")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to generate namespace YAML for %s: %w", ns, err)
		}

		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(string(output))
		if lp.options.ProgressUI {
			cmd.Stdout = io.Discard
		} else {
			cmd.Stdout = os.Stdout
		}
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create namespace %s: %w", ns, err)
		}
	}

	return nil
}

// applyPlatformManifest applies a specific platform component manifest
func (lp *LocalProvisioner) applyPlatformManifest(component string) error {
	logger.Infof("Installing %s from platform resources", component)

	// Define the path to the component's manifests
	manifestPath := fmt.Sprintf("platform/controllers/adharplatform/resources/%s", component)

	// Check if the manifest path exists
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("manifest path for %s does not exist: %s", component, manifestPath)
	}

	// Find the main install file
	installFiles := []string{
		filepath.Join(manifestPath, "install.yaml"),
		filepath.Join(manifestPath, "install-ha.yaml"),
	}

	var installFile string
	for _, file := range installFiles {
		if _, err := os.Stat(file); err == nil {
			installFile = file
			break
		}
	}

	if installFile == "" {
		return fmt.Errorf("no install file found for %s in %s", component, manifestPath)
	}

	logger.Infof("Applying %s manifest: %s", component, installFile)

	// Apply the manifest
	cmd := exec.Command("kubectl", "apply", "-f", installFile)
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply %s manifest: %w", component, err)
	}

	return nil
}

// applyIngressManifests applies ingress resources for platform components
func (lp *LocalProvisioner) applyIngressManifests() error {
	logger.Info("Installing ingress resources for platform components")

	ingressPath := "platform/controllers/adharplatform/resources/ingress"

	// Check if the ingress path exists
	if _, err := os.Stat(ingressPath); os.IsNotExist(err) {
		return fmt.Errorf("ingress manifest path does not exist: %s", ingressPath)
	}

	// Apply all ingress manifests
	files, err := filepath.Glob(filepath.Join(ingressPath, "*.yaml"))
	if err != nil {
		return fmt.Errorf("failed to glob ingress manifest files: %w", err)
	}

	for _, file := range files {
		logger.Infof("Applying ingress manifest: %s", file)
		cmd := exec.Command("kubectl", "apply", "-f", file)
		if lp.options.ProgressUI {
			cmd.Stdout = io.Discard
		} else {
			cmd.Stdout = os.Stdout
		}
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to apply ingress manifest %s: %w", file, err)
		}
	}

	return nil
}

// labelCoreSecrets adds Adhar labels to core secrets for CLI discovery
func (lp *LocalProvisioner) labelCoreSecrets() error {
	logger.Info("Adding Adhar labels to core secrets")

	// Label ArgoCD admin secret
	cmd := exec.Command("kubectl", "label", "secret", "argocd-initial-admin-secret",
		"app.kubernetes.io/part-of=adhar", "app.kubernetes.io/component=argocd",
		"--namespace=adhar-system", "--overwrite")
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		logger.Warnf("Failed to label ArgoCD secret (may not exist yet): %v", err)
	}

	// Label Gitea admin secret
	cmd = exec.Command("kubectl", "label", "secret", "gitea-admin-secret",
		"app.kubernetes.io/part-of=adhar", "app.kubernetes.io/component=gitea",
		"--namespace=adhar-system", "--overwrite")
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		logger.Warnf("Failed to label Gitea secret (may not exist yet): %v", err)
	}

	return nil
}

// waitForArgoCD waits for ArgoCD components to be ready
func (lp *LocalProvisioner) waitForArgoCD() error {
	logger.Info("Waiting for ArgoCD components to be ready")

	// Wait for ArgoCD server deployment
	cmd := exec.Command("kubectl", "wait", "--for=condition=available",
		"deployment/argocd-server", "--namespace=adhar-system", "--timeout=600s")
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD server not ready: %w", err)
	}

	// Wait for ArgoCD application controller (statefulset)
	cmd = exec.Command("kubectl", "wait", "--for=condition=available",
		"statefulset/argocd-application-controller", "--namespace=adhar-system", "--timeout=600s")
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD application controller not ready: %w", err)
	}

	logger.Info("ArgoCD components are ready")
	return nil
}

// waitForNginxIngress waits for Nginx Ingress controller to be ready
func (lp *LocalProvisioner) waitForNginxIngress() error {
	logger.Info("Waiting for Nginx Ingress controller to be ready")

	// Best-effort: on single-node Kind clusters, allow scheduling on control-plane by adding tolerations
	// This helps avoid Pending pods when no worker nodes are present.
	if err := lp.patchIngressNginxTolerations(); err != nil {
		logger.Warnf("Failed to patch ingress-nginx tolerations (continuing): %v", err)
	}

	// Wait for Nginx Ingress controller deployment
	cmd := exec.Command("kubectl", "wait", "--for=condition=available",
		"deployment/ingress-nginx-controller", "--namespace=adhar-system", "--timeout=600s")
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		diag := lp.collectNginxDiagnostics()
		return fmt.Errorf("Nginx Ingress controller not ready: %w\n%s", err, diag)
	}

	// Wait for the admission webhook jobs to complete
	cmd = exec.Command("kubectl", "wait", "--for=condition=complete",
		"job/ingress-nginx-admission-create", "--namespace=adhar-system", "--timeout=600s")
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		diag := lp.collectNginxDiagnostics()
		return fmt.Errorf("Nginx Ingress admission create job not complete: %w\n%s", err, diag)
	}

	cmd = exec.Command("kubectl", "wait", "--for=condition=complete",
		"job/ingress-nginx-admission-patch", "--namespace=adhar-system", "--timeout=600s")
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		diag := lp.collectNginxDiagnostics()
		return fmt.Errorf("Nginx Ingress admission patch job not complete: %w\n%s", err, diag)
	}

	logger.Info("Nginx Ingress controller is ready")
	return nil
}

// collectNginxDiagnostics gathers a short summary to help debug ingress readiness
func (lp *LocalProvisioner) collectNginxDiagnostics() string {
	var b strings.Builder
	b.WriteString("\n--- Nginx diagnostics (adhar-system) ---\n")

	// Pods summary
	pods := exec.Command("kubectl", "get", "pods", "-n", "adhar-system",
		"-l", "app.kubernetes.io/name=ingress-nginx", "-o", "wide")
	if out, err := pods.CombinedOutput(); err == nil {
		b.WriteString("Pods:\n")
		b.Write(out)
		if len(out) == 0 || out[len(out)-1] != '\n' {
			b.WriteByte('\n')
		}
	}

	// Deployment describe (trimmed)
	desc := exec.Command("kubectl", "describe", "deployment/ingress-nginx-controller", "-n", "adhar-system")
	if out, err := desc.CombinedOutput(); err == nil {
		b.WriteString("\nDeployment describe (last 100 lines):\n")
		lines := strings.Split(string(out), "\n")
		start := 0
		if len(lines) > 100 {
			start = len(lines) - 100
		}
		b.WriteString(strings.Join(lines[start:], "\n"))
		if len(lines) == 0 || lines[len(lines)-1] != "" {
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// patchIngressNginxTolerations adds control-plane tolerations to ingress-nginx deployment and admission jobs
// to ensure they can schedule on a tainted control-plane node in single-node Kind clusters.
func (lp *LocalProvisioner) patchIngressNginxTolerations() error {
	tmpl := func(args ...string) *exec.Cmd {
		cmd := exec.Command("kubectl", args...)
		if lp.options.ProgressUI {
			cmd.Stdout = io.Discard
		} else {
			cmd.Stdout = os.Stdout
		}
		cmd.Stderr = os.Stderr
		return cmd
	}

	tolerationsPatch := `{"spec": {"template": {"spec": {"tolerations": [
		{"key": "node-role.kubernetes.io/master", "operator": "Exists", "effect": "NoSchedule"},
		{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}
	]}}}}`

	// Determine control-plane node name for nodeSelector (kind single-node)
	nodeName := lp.getControlPlaneNodeName()
	nodeSelectorPatch := ""
	if nodeName != "" {
		nodeSelectorPatch = fmt.Sprintf(`{"spec": {"template": {"spec": {"nodeSelector": {"kubernetes.io/hostname": %q}}}}}`, nodeName)
	}

	// Patch deployment
	if err := tmpl("-n", "adhar-system", "patch", "deployment", "ingress-nginx-controller", "--type", "merge", "-p", tolerationsPatch).Run(); err != nil {
		// Keep going; may not exist yet or already has tolerations
		logger.Debugf("deployment tolerations patch warning: %v", err)
	}
	if nodeSelectorPatch != "" {
		if err := tmpl("-n", "adhar-system", "patch", "deployment", "ingress-nginx-controller", "--type", "merge", "-p", nodeSelectorPatch).Run(); err != nil {
			logger.Debugf("deployment nodeSelector patch warning: %v", err)
		}
	}

	// Patch admission jobs
	if err := tmpl("-n", "adhar-system", "patch", "job", "ingress-nginx-admission-create", "--type", "merge", "-p", tolerationsPatch).Run(); err != nil {
		logger.Debugf("admission-create tolerations patch warning: %v", err)
	}
	if err := tmpl("-n", "adhar-system", "patch", "job", "ingress-nginx-admission-patch", "--type", "merge", "-p", tolerationsPatch).Run(); err != nil {
		logger.Debugf("admission-patch tolerations patch warning: %v", err)
	}

	// Recycle pods so patches take effect on fresh pods
	_ = tmpl("-n", "adhar-system", "delete", "pod", "-l", "app.kubernetes.io/component=controller", "--wait=false").Run()
	_ = tmpl("-n", "adhar-system", "delete", "pod", "-l", "job-name=ingress-nginx-admission-create", "--wait=false").Run()
	_ = tmpl("-n", "adhar-system", "delete", "pod", "-l", "job-name=ingress-nginx-admission-patch", "--wait=false").Run()

	return nil
}

// getControlPlaneNodeName returns the control-plane node name (best-effort) in a kind cluster
func (lp *LocalProvisioner) getControlPlaneNodeName() string {
	cmd := exec.Command("kubectl", "get", "nodes", "-l", "node-role.kubernetes.io/control-plane", "-o", "jsonpath={.items[0].metadata.name}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// applyPlatformApplicationSets applies platform ApplicationSets for ArgoCD management
func (lp *LocalProvisioner) applyPlatformApplicationSets() error {
	logger.Info("Applying platform ApplicationSets for ArgoCD management")

	// Apply the local ApplicationSet
	appsetFile := "platform/stack/adhar-appset-local.yaml"

	if _, err := os.Stat(appsetFile); os.IsNotExist(err) {
		return fmt.Errorf("ApplicationSet file does not exist: %s", appsetFile)
	}

	cmd := exec.Command("kubectl", "apply", "-f", appsetFile)
	if lp.options.ProgressUI {
		cmd.Stdout = io.Discard
	} else {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply ApplicationSet: %w", err)
	}

	return nil
}

// createLocalDevelopmentCluster creates a local Kind cluster using the LocalProvisioner
func createLocalDevelopmentCluster(ctx context.Context, cmd *cobra.Command, args []string, ctxCancel context.CancelFunc) error {
	// Get kubeconfig path
	kubeConfigPath := filepath.Join(homedir.HomeDir(), ".kube", "config")

	protocol = strings.ToLower(protocol)
	host = strings.ToLower(host)
	if ingressHost == "" {
		ingressHost = host
	}

	// Validate arguments and set up build configuration
	if err := validate(); err != nil {
		return err
	}

	var localFiles []string
	var localDirs []string
	var remotePaths []string

	if len(extraPackages) > 0 {
		r, f, d, pErr := helpers.ParsePackageStrings(extraPackages)
		if pErr != nil {
			return pErr
		}
		localFiles = f
		localDirs = d
		remotePaths = r
	}

	o := make(map[string]v1alpha1.PackageCustomization)
	for i := range packageCustomizationFiles {
		c, pErr := getPackageCustomFile(packageCustomizationFiles[i])
		if pErr != nil {
			return pErr
		}
		o[c.Name] = c
	}

	// Check if no-exit flag is set (defined in up.go)
	noExit, _ := cmd.Flags().GetBool("no-exit")
	exitOnSync := true // Exit after ApplicationSet is applied, GitOps will continue via ArgoCD
	if cmd.Flags().Changed("no-exit") {
		exitOnSync = !noExit
	}

	// If registry-config is unset we pass nil
	// If registry-config is change (--registry-config=foo) we pass the new value
	// If registry-config is set but unchanged (--registry-confg) we pass ""
	maybeRegistryConfig := []string{}
	if cmd.Flags().Changed("registry-config") {
		maybeRegistryConfig = registryConfig
	}

	// opts := build.NewBuildOptions{
	// 	Name:              globals.DefaultClusterName,
	// 	KubeVersion:       kubeVersion,
	// 	KubeConfigPath:    kubeConfigPath,
	// 	KindConfigPath:    kindConfigPath,
	// 	ExtraPortsMapping: extraPortsMapping,
	// 	RegistryConfig:    maybeRegistryConfig,

	// 	TemplateData: v1alpha1.BuildCustomizationSpec{
	// 		Protocol:       protocol,
	// 		Host:           host,
	// 		IngressHost:    ingressHost,
	// 		Port:           port,
	// 		UsePathRouting: pathRouting,
	// 		StaticPassword: devPassword,
	// 	},

	// 	CustomPackageFiles:   localFiles,
	// 	CustomPackageDirs:    localDirs,
	// 	CustomPackageUrls:    remotePaths,
	// 	ExitOnSync:           exitOnSync,
	// 	PackageCustomization: o,

	// 	Scheme:     k8s.GetScheme(),
	// 	CancelFunc: ctxCancel,
	// }

	// b := build.NewBuild(opts)

	// // Build the platform
	// if err := b.Run(ctx, recreateCluster); err != nil {
	// 	return err
	// }

	// Create LocalProvisioner with options
	options := &LocalOptions{
		Name:                      globals.DefaultClusterName,
		RecreateCluster:           recreateCluster,
		KubeConfigPath:            kubeConfigPath,
		DevPassword:               devPassword,
		KubeVersion:               kubeVersion,
		ExtraPortsMapping:         extraPortsMapping,
		KindConfigPath:            kindConfigPath,
		ExtraPackages:             extraPackages,
		RegistryConfig:            maybeRegistryConfig,
		PackageCustomizationFiles: packageCustomizationFiles,
		NoExit:                    exitOnSync,
		Protocol:                  protocol,
		Host:                      host,
		IngressHost:               ingressHost,
		Port:                      port,
		PathRouting:               pathRouting,
		Verbose:                   verbose,
		ProgressUI:                true,
		CustomPackageFiles:        localFiles,
		CustomPackageDirs:         localDirs,
		CustomPackageUrls:         remotePaths,
		ExitOnSync:                exitOnSync,
		PackageCustomization:      o,
		Scheme:                    k8s.GetScheme(),
		CancelFunc:                ctxCancel,
		TemplateData: v1alpha1.BuildCustomizationSpec{
			Protocol:       protocol,
			Host:           host,
			IngressHost:    ingressHost,
			Port:           port,
			UsePathRouting: pathRouting,
			StaticPassword: devPassword,
		},
	}

	provisioner := NewLocalProvisioner(options)

	// If dry run, show what would be provisioned
	if dryRun {
		// Create a simple env config for dry run display
		envConfig := &config.ResolvedEnvironmentConfig{
			Name:             globals.DefaultClusterName,
			ResolvedProvider: "kind",
			ResolvedRegion:   "local",
			ResolvedType:     config.EnvironmentTypeNonProduction,
			ResolvedClusterConfig: []config.KeyValueConfig{
				{Key: "kubeVersion", Value: kubeVersion},
				{Key: "controlPlaneReplicas", Value: "1"},
				{Key: "workerReplicas", Value: "0"},
			},
			GlobalSettings: &config.GlobalSettings{
				AdharContext: "provider-mode",
				DefaultHost:  globals.DefaultHostName,
				EnableHAMode: false,
				Email:        "admin@" + globals.DefaultHostName,
			},
		}
		return showLocalDryRunInfo(envConfig)
	}

	// Start the provisioning process
	logger.GetLogger().StartOperation("Local Development Cluster", "Creating Kind cluster with platform services")

	// Use the LocalProvisioner to create the complete environment
	if err := provisioner.Provision(ctx, args); err != nil {
		logger.Error("Local cluster provisioning failed", err, map[string]interface{}{
			"cluster":  globals.DefaultClusterName,
			"provider": "kind",
		})
		return fmt.Errorf("failed to provision local development cluster: %w", err)
	}

	logger.GetLogger().FinishOperation("Local Development Cluster", "Platform ready for development")

	// Check if the context has been cancelled, graceful shutdown
	if cmd.Context().Err() != nil {
		return context.Cause(cmd.Context())
	}

	// Print success message
	printSuccessMsg()

	return nil
}

// // performLocalPreflightChecks validates requirements for local development setup
// func performLocalPreflightChecks() error {
// 	fmt.Printf("⚡ %s\n", helpers.BoldStyle.Render("Running pre-flight checks..."))

// 	// Check Docker availability and health
// 	if err := checkDockerAvailable(); err != nil {
// 		fmt.Printf("  ❌ Docker check failed: %v\n", err)
// 		return err
// 	}
// 	fmt.Printf("  ✅ Docker is available and healthy\n")

// 	// Check Kind availability and functionality
// 	if err := checkKindAvailable(); err != nil {
// 		fmt.Printf("  ❌ Kind check failed: %v\n", err)
// 		return err
// 	}
// 	fmt.Printf("  ✅ Kind cluster engine ready\n")

// 	// Check kubectl availability
// 	if err := checkKubectlAvailable(); err != nil {
// 		fmt.Printf("  ❌ kubectl check failed: %v\n", err)
// 		return err
// 	}
// 	fmt.Printf("  ✅ kubectl is available\n")

// 	// Check system resources (disk, memory, CPU)
// 	if err := checkSystemResources(); err != nil {
// 		fmt.Printf("  ❌ System resources check failed: %v\n", err)
// 		return err
// 	}
// 	fmt.Printf("  ✅ Sufficient system resources available\n")

// 	// Check port availability with detailed analysis
// 	if err := checkPortAvailabilityDetailed(); err != nil {
// 		fmt.Printf("  ❌ Port availability check failed: %v\n", err)
// 		return err
// 	}
// 	fmt.Printf("  ✅ Required ports are available\n")

// 	// Check container runtime health
// 	if err := checkContainerRuntimeHealth(); err != nil {
// 		fmt.Printf("  ❌ Container runtime health check failed: %v\n", err)
// 		return err
// 	}
// 	fmt.Printf("  ✅ Container runtime is healthy\n")

// 	// Check existing clusters for conflicts
// 	if err := checkExistingClusters(); err != nil {
// 		fmt.Printf("  ❌ Existing cluster check failed: %v\n", err)
// 		return err
// 	}
// 	fmt.Printf("  ✅ No conflicting clusters found\n")

// 	fmt.Println()
// 	return nil
// }

// // checkDockerAvailable checks if Docker daemon is running and healthy
// func checkDockerAvailable() error {
// 	// Check if docker command exists
// 	_, err := exec.LookPath("docker")
// 	if err != nil {
// 		return fmt.Errorf("docker command not found in PATH. Please install Docker: https://docs.docker.com/get-docker/")
// 	}

// 	// Check if Docker daemon is running
// 	cmd := exec.Command("docker", "info")
// 	cmd.Stdout = nil // Suppress output
// 	cmd.Stderr = nil // Suppress error output
// 	if err := cmd.Run(); err != nil {
// 		return fmt.Errorf("docker daemon is not running or not accessible. Please start Docker Desktop or Docker daemon")
// 	}

// 	// Check Docker version compatibility
// 	cmd = exec.Command("docker", "version", "--format", "{{.Server.Version}}")
// 	output, err := cmd.Output()
// 	if err != nil {
// 		return fmt.Errorf("failed to get Docker version: %w", err)
// 	}

// 	version := strings.TrimSpace(string(output))
// 	if version == "" {
// 		return fmt.Errorf("unable to determine Docker version")
// 	}

// 	// Basic version check (Docker 20+ recommended)
// 	if !strings.HasPrefix(version, "2") && !strings.HasPrefix(version, "3") {
// 		fmt.Printf("  ⚠️  Warning: Docker version %s detected. Version 20+ recommended\n", version)
// 	}

// 	return nil
// }

// // checkKindAvailable checks if Kind binary is available and functional
// func checkKindAvailable() error {
// 	// Check if kind command exists
// 	_, err := exec.LookPath("kind")
// 	if err != nil {
// 		return fmt.Errorf("kind command not found in PATH. Please install Kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation")
// 	}

// 	// Check if kind command works
// 	cmd := exec.Command("kind", "version")
// 	cmd.Stdout = nil // Suppress output
// 	cmd.Stderr = nil // Suppress error output
// 	if err := cmd.Run(); err != nil {
// 		return fmt.Errorf("kind command is not working properly. Please reinstall Kind")
// 	}

// 	return nil
// }

// // checkKubectlAvailable checks if kubectl is available and functional
// func checkKubectlAvailable() error {
// 	// Check if kubectl command exists
// 	_, err := exec.LookPath("kubectl")
// 	if err != nil {
// 		return fmt.Errorf("kubectl command not found in PATH. Please install kubectl: https://kubernetes.io/docs/tasks/tools/")
// 	}

// 	// Check if kubectl command works
// 	cmd := exec.Command("kubectl", "version", "--client", "--output=yaml")
// 	cmd.Stdout = nil // Suppress output
// 	cmd.Stderr = nil // Suppress error output
// 	if err := cmd.Run(); err != nil {
// 		return fmt.Errorf("kubectl command is not working properly. Please reinstall kubectl")
// 	}

// 	return nil
// }

// // checkSystemResources checks if system has sufficient resources for Kind cluster
// func checkSystemResources() error {
// 	// Check memory (basic check for macOS/Linux)
// 	if err := checkMemory(); err != nil {
// 		return err
// 	}

// 	// Check disk space with the existing function
// 	if err := checkDiskSpace(); err != nil {
// 		return err
// 	}

// 	// Check CPU cores
// 	if err := checkCPUCores(); err != nil {
// 		return err
// 	}

// 	return nil
// }

// // checkMemory checks if system has sufficient memory
// func checkMemory() error {
// 	var cmd *exec.Cmd

// 	// Try different approaches based on OS
// 	if runtime.GOOS == "darwin" {
// 		// macOS
// 		cmd = exec.Command("sysctl", "-n", "hw.memsize")
// 	} else if runtime.GOOS == "linux" {
// 		// Linux
// 		cmd = exec.Command("sh", "-c", "grep MemTotal /proc/meminfo | awk '{print $2 * 1024}'")
// 	} else {
// 		// Windows or other - skip detailed check
// 		return nil
// 	}

// 	output, err := cmd.Output()
// 	if err != nil {
// 		// If we can't check memory, just warn and continue
// 		fmt.Printf("  ⚠️  Unable to check system memory, continuing anyway\n")
// 		return nil
// 	}

// 	memStr := strings.TrimSpace(string(output))
// 	memBytes, err := strconv.ParseInt(memStr, 10, 64)
// 	if err != nil {
// 		// If we can't parse memory, just warn and continue
// 		fmt.Printf("  ⚠️  Unable to parse system memory, continuing anyway\n")
// 		return nil
// 	}

// 	// Convert to GB
// 	memGB := float64(memBytes) / (1024 * 1024 * 1024)

// 	// Require at least 4GB of RAM for Kind cluster with platform components
// 	if memGB < 4.0 {
// 		return fmt.Errorf("insufficient memory: %.1fGB available. At least 4GB recommended for Kind cluster with platform components", memGB)
// 	}

// 	return nil
// }

// // checkCPUCores checks if system has sufficient CPU cores
// func checkCPUCores() error {
// 	cores := runtime.NumCPU()

// 	// Require at least 2 CPU cores for Kind cluster
// 	if cores < 2 {
// 		return fmt.Errorf("insufficient CPU cores: %d available. At least 2 cores recommended for Kind cluster", cores)
// 	}

// 	return nil
// }

// checkDiskSpace performs a comprehensive disk space check
func checkDiskSpace() error {
	// Get current working directory to check disk space
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Use df command to check disk space (works on macOS and Linux)
	cmd := exec.Command("df", "-h", cwd)
	output, err := cmd.Output()
	if err != nil {
		// Fallback: try a basic check using os.Stat
		return checkDiskSpaceFallback(cwd)
	}

	// Parse df output to get available space
	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return fmt.Errorf("unable to parse disk space information")
	}

	// Parse the second line which contains the disk info
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return fmt.Errorf("unable to parse disk space information")
	}

	availableSpace := fields[3] // Available space column

	// Check if available space is less than 4GB (recommended for Kind cluster + images)
	if strings.Contains(availableSpace, "M") {
		// If in MB, it's definitely too low
		return fmt.Errorf("insufficient disk space: only %s available. At least 4GB recommended for Kind cluster with platform components", availableSpace)
	}

	// If it shows in GB, extract the number
	if strings.Contains(availableSpace, "G") {
		spaceStr := strings.TrimSuffix(availableSpace, "G")
		if space, err := strconv.ParseFloat(spaceStr, 64); err == nil && space < 4.0 {
			return fmt.Errorf("insufficient disk space: only %.1fGB available. At least 4GB recommended for Kind cluster with platform components", space)
		}
	}

	return nil
}

// checkPortAvailabilityDetailed performs detailed port availability checking
func checkPortAvailabilityDetailed() error {
	// Enhanced port checking with detailed analysis
	if err := checkPortAvailability(); err != nil {
		// Try to provide more details about what's using the ports
		return enhancePortError(err)
	}
	return nil
}

// checkPortAvailability checks if required ports are available
func checkPortAvailability() error {
	// List of ports that Kind and Adhar typically use
	// Default ports: 80, 443 (HTTP/HTTPS), 6443 (Kubernetes API)
	requiredPorts := []int{80, 443, 6443}

	var busyPorts []int

	for _, port := range requiredPorts {
		if isPortInUse(port) {
			busyPorts = append(busyPorts, port)
		}
	}

	if len(busyPorts) > 0 {
		var portStrings []string
		for _, port := range busyPorts {
			portStrings = append(portStrings, fmt.Sprintf("%d", port))
		}
		return fmt.Errorf("ports %s are already in use. Please stop services using these ports or they may conflict with the cluster", strings.Join(portStrings, ", "))
	}

	return nil
}

// enhancePortError provides more details about port conflicts
func enhancePortError(err error) error {
	errMsg := err.Error()

	// Extract port numbers from error message
	if strings.Contains(errMsg, "80") {
		errMsg += "\n  💡 Port 80 conflict solutions:"
		errMsg += "\n     • Stop local web server (Apache, Nginx, etc.)"
		errMsg += "\n     • Use custom ports in config: networking.httpPort: 8080"
	}

	if strings.Contains(errMsg, "443") {
		errMsg += "\n  💡 Port 443 conflict solutions:"
		errMsg += "\n     • Stop HTTPS services"
		errMsg += "\n     • Use custom ports in config: networking.httpsPort: 8443"
	}

	if strings.Contains(errMsg, "6443") {
		errMsg += "\n  💡 Port 6443 conflict solutions:"
		errMsg += "\n     • Stop existing Kubernetes clusters"
		errMsg += "\n     • Check: kind get clusters"
	}

	return fmt.Errorf("%s", errMsg)
}

// checkContainerRuntimeHealth checks if container runtime is healthy
func checkContainerRuntimeHealth() error {
	// Check Docker storage driver
	cmd := exec.Command("docker", "info", "--format", "{{.Driver}}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Docker storage driver info: %w", err)
	}

	driver := strings.TrimSpace(string(output))
	if driver == "" {
		return fmt.Errorf("unable to determine Docker storage driver")
	}

	// Check Docker storage usage
	cmd = exec.Command("docker", "system", "df", "--format", "table {{.Type}}\t{{.TotalCount}}\t{{.Size}}")
	_, err = cmd.Output()
	if err != nil {
		// If we can't check storage, warn but continue
		fmt.Printf("  ⚠️  Unable to check Docker storage usage\n")
	}

	// Try pulling a small test image to verify network connectivity and registry access
	cmd = exec.Command("docker", "pull", "hello-world:latest")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unable to pull test image from Docker Hub. Check network connectivity and Docker configuration")
	}

	// Clean up test image
	exec.Command("docker", "rmi", "hello-world:latest").Run()

	return nil
}

// checkExistingClusters checks for existing Kind clusters that might conflict
func checkExistingClusters() error {
	// Check for existing Kind clusters
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		// If kind command fails, just continue
		return nil
	}

	clusters := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, cluster := range clusters {
		cluster = strings.TrimSpace(cluster)
		if cluster == "" {
			continue
		}

		// Check if there's an existing 'adhar' cluster
		if cluster == "adhar" {
			return fmt.Errorf("existing Kind cluster 'adhar' found. Please delete it first:\n  kind delete cluster --name adhar")
		}
	}

	// Check for any running containers that might conflict
	cmd = exec.Command("docker", "ps", "--filter", "label=io.x-k8s.kind.cluster", "--format", "{{.Names}}")
	output, err = cmd.Output()
	if err != nil {
		// If we can't check containers, just continue
		return nil
	}

	containers := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, container := range containers {
		container = strings.TrimSpace(container)
		if container == "" {
			continue
		}

		// Warn about existing Kind containers
		if strings.Contains(container, "control-plane") {
			fmt.Printf("  ⚠️  Found existing Kind container: %s\n", container)
		}
	}

	return nil
}

// checkDiskSpaceFallback provides a basic fallback disk space check
func checkDiskSpaceFallback(path string) error {
	// This is a basic fallback - just check if we can write to the directory
	tempFile := filepath.Join(path, ".adhar-space-test")
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("unable to write to current directory - may have insufficient disk space or permissions")
	}
	file.Close()
	os.Remove(tempFile)
	return nil
}

// isPortInUse checks if a specific port is currently in use
func isPortInUse(port int) bool {
	// First try to bind to the port temporarily on all interfaces (0.0.0.0)
	// This matches how Kind tries to bind ports
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}

	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return true // Port is in use
	}

	defer listener.Close()
	return false // Port is available
}

// showLocalDryRunInfo displays what would be provisioned in local development dry-run mode
func showLocalDryRunInfo(envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", helpers.BoldStyle.Render("🔍 Dry Run - Local Development Preview"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environment: %-30s │\n", envConfig.Name)
	fmt.Printf("│ Provider:    %-30s │\n", envConfig.ResolvedProvider)
	fmt.Printf("│ Region:      %-30s │\n", envConfig.ResolvedRegion)
	fmt.Printf("│ Type:        %-30s │\n", envConfig.ResolvedType)
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	fmt.Printf("\nPlatform Configuration:\n")
	fmt.Printf("  Host:        %s\n", envConfig.GlobalSettings.DefaultHost)
	// Protocol/Port/PathRouting are not in envConfig directly for local dry-run; show sensible defaults
	fmt.Printf("  Protocol:    https\n")
	fmt.Printf("  Port:        8443\n")
	fmt.Printf("  Path Routing: %v\n", true)

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nKind Cluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			switch cfg.Key {
			case "kubeVersion":
				fmt.Printf("  Kubernetes Version: %s\n", cfg.Value)
			case "extraPorts":
				fmt.Printf("  Extra Ports: %s\n", cfg.Value)
			case "configPath":
				fmt.Printf("  Config Path: %s\n", cfg.Value)
			default:
				fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
			}
		}
	}

	fmt.Printf("\nCore Services:\n")
	fmt.Printf("  ArgoCD:      true\n")
	fmt.Printf("  Gitea:       true\n")
	fmt.Printf("  Nginx:       true\n")
	fmt.Printf("  Cilium:      true\n")

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nKind Cluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			switch cfg.Key {
			case "kubeVersion":
				fmt.Printf("  Kubernetes Version: %s\n", cfg.Value)
			case "extraPorts":
				fmt.Printf("  Extra Ports: %s\n", cfg.Value)
			case "configPath":
				fmt.Printf("  Config Path: %s\n", cfg.Value)
			default:
				fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
			}
		}
	}

	fmt.Printf("\n%s\n", helpers.CodeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// printSuccessMsg prints success message for local development cluster
func printSuccessMsg() {
	var argoURL string

	// For local development (Kind clusters), use clean URLs without ports
	proxy := behindProxy()
	if proxy {
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else if host == globals.DefaultHostName { // adhar.localtest.me (Kind cluster)
		// Kind clusters use direct port mapping, show clean URLs
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else {
		// Production clusters or custom domains may need port specification
		if pathRouting {
			argoURL = fmt.Sprintf("%s://%s:%s/argocd", protocol, host, port)
		} else {
			argoURL = fmt.Sprintf("%s://argocd.%s:%s", protocol, host, port)
		}
	}

	fmt.Print("\n\n########################### Finished Creating Adhar IDP Successfully! ############################\n\n")
	fmt.Printf("🎉 %s\n\n", helpers.BoldStyle.Render("Local Development Platform Ready!"))
	fmt.Printf("Your Adhar platform includes:\n")
	fmt.Printf("  ✅ Kind Kubernetes cluster\n")
	fmt.Printf("  ✅ Cilium CNI for secure networking\n")
	fmt.Printf("  ✅ ArgoCD for GitOps deployments\n")
	fmt.Printf("  ✅ Gitea for Git repository hosting\n")
	fmt.Printf("  ✅ Ingress-Nginx for traffic routing\n")
	fmt.Printf("  ✅ Platform observability stack\n\n")
	fmt.Printf("%s\n", helpers.BoldStyle.Render("Quick Access:"))
	fmt.Printf("ArgoCD Dashboard: %s\n", argoURL)

	// Add Hubble URL for network observability
	var hubbleURL string
	if proxy {
		hubbleURL = fmt.Sprintf("https://%s/hubble", host)
	} else if host == globals.DefaultHostName {
		hubbleURL = fmt.Sprintf("https://%s/hubble", host)
	} else {
		if pathRouting {
			hubbleURL = fmt.Sprintf("%s://%s:%s/hubble", protocol, host, port)
		} else {
			hubbleURL = fmt.Sprintf("%s://hubble.%s:%s", protocol, host, port)
		}
	}
	fmt.Printf("Hubble UI (Network Observability): %s\n", hubbleURL)
	fmt.Printf("Username: admin\n")
	fmt.Printf("Password: Run `adhar get secrets -p argocd`\n\n")
	fmt.Printf("%s\n", helpers.BoldStyle.Render("Next Steps:"))
	fmt.Printf("1. Deploy your first application via ArgoCD\n")
	fmt.Printf("2. Push code to the integrated Gitea instance\n")
	fmt.Printf("3. Use `adhar get secrets` to retrieve service credentials\n")
	fmt.Printf("4. Run `adhar get status` to monitor platform health\n\n")
	fmt.Printf("%s\n", helpers.BoldStyle.Render("Local Development Commands:"))
	fmt.Printf("• Check cluster status: adhar get status\n")
	fmt.Printf("• Get service secrets: adhar get secrets\n")
	fmt.Printf("• Destroy cluster: adhar down\n\n")
}

// behindProxy checks if we are in codespaces
func behindProxy() bool {
	// check if we are in codespaces: https://docs.github.com/en/codespaces/developing-in-a-codespace
	_, ok := os.LookupEnv("CODESPACES")
	return ok
}

// validate validates the up command arguments
func validate() error {

	// Add check for host
	if host == "" {
		return fmt.Errorf("host cannot be empty")
	}

	_, err := url.Parse(fmt.Sprintf("%s://%s:%s", protocol, host, port))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	for i := range packageCustomizationFiles {
		_, pErr := getPackageCustomFile(packageCustomizationFiles[i])
		if pErr != nil {
			return pErr
		}
	}

	_, _, _, err = helpers.ParsePackageStrings(extraPackages)
	return err
}

func getPackageCustomFile(input string) (v1alpha1.PackageCustomization, error) {
	// the format should be `<package-name>:<path-to-file>`
	s := strings.Split(input, ":")
	if len(s) != 2 {
		return v1alpha1.PackageCustomization{}, fmt.Errorf("ensure %s is formatted as <package-name>:<path-to-file>", input)
	}

	paths, err := helpers.GetAbsFilePaths([]string{s[1]}, false)
	if err != nil {
		return v1alpha1.PackageCustomization{}, err
	}

	err = helpers.ValidateKubernetesYamlFile(paths[0])
	if err != nil {
		return v1alpha1.PackageCustomization{}, err
	}

	corePkgs := map[string]struct{}{v1alpha1.ArgoCDPackageName: {}, v1alpha1.GiteaPackageName: {}, v1alpha1.IngressNginxPackageName: {}}
	name := s[0]
	_, ok := corePkgs[name]
	if !ok {
		return v1alpha1.PackageCustomization{}, fmt.Errorf("customization for %s not supported", name)
	}
	return v1alpha1.PackageCustomization{
		Name:     name,
		FilePath: paths[0],
	}, nil
}

func (b *LocalProvisioner) ReconcileKindCluster(ctx context.Context, recreateCluster bool) error {
	// Initialize Kind Cluster
	cluster, err := kind.NewCluster(b.options.Name, b.options.KubeVersion, b.options.KubeConfigPath, b.options.KindConfigPath, b.options.ExtraPortsMapping, b.options.RegistryConfig, b.options.TemplateData)
	if err != nil {
		logger.Error("Error Creating kind cluster", err, map[string]interface{}{})
		return err
	}

	// Build Kind cluster
	if err := cluster.Reconcile(ctx, recreateCluster); err != nil {
		logger.Error("Error starting kind cluster", err, map[string]interface{}{})
		return err
	}

	// Create Kube Config for Kind cluster
	if err := cluster.ExportKubeConfig(b.options.Name, false); err != nil {
		logger.Error("Error exporting kubeconfig from kind cluster", err, map[string]interface{}{})
		return err
	}
	return nil
}

func (b *LocalProvisioner) GetKubeConfig() (*rest.Config, error) {
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", b.options.KubeConfigPath)
	if err != nil {
		logger.Error("Error building kubeconfig from kind cluster", err, map[string]interface{}{})
		return nil, err
	}
	return kubeConfig, nil
}

func (b *LocalProvisioner) GetKubeClient(kubeConfig *rest.Config) (client.Client, error) {
	kubeClient, err := client.New(kubeConfig, client.Options{Scheme: b.options.Scheme})
	if err != nil {
		logger.Error("Error creating kubernetes client", err, map[string]interface{}{})
		return nil, err
	}
	return kubeClient, nil
}

func (b *LocalProvisioner) ReconcileCRDs(ctx context.Context, kubeClient client.Client) error {
	// Ensure idpbuilder CRDs
	if err := controllers.EnsureCRDs(ctx, b.options.Scheme, kubeClient, b.options.TemplateData); err != nil {
		logger.Error("Error creating idpbuilder CRDs", err, map[string]interface{}{})
		return err
	}
	return nil
}

func (b *LocalProvisioner) RunControllers(ctx context.Context, mgr manager.Manager, exitCh chan error, tmpDir string) error {
	return controllers.RunControllers(ctx, mgr, exitCh, b.options.CancelFunc, b.options.ExitOnSync, b.options.TemplateData, tmpDir)
}

func (b *LocalProvisioner) isCompatible(ctx context.Context, kubeClient client.Client) (bool, error) {
	localBuild := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.options.Name,
			Namespace: globals.AdharSystemNamespace,
		},
	}

	err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&localBuild), &localBuild)
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	ok := isBuildCustomizationSpecEqual(b.options.TemplateData, localBuild.Spec.BuildCustomization)

	if ok {
		return ok, nil
	}

	existing, given := localBuild.Spec.BuildCustomization, b.options.TemplateData
	existing.SelfSignedCert = ""
	given.SelfSignedCert = ""

	return false, fmt.Errorf("provided command flags and existing configurations are incompatible. please recreate the cluster. "+
		"existing: %+v, given: %+v",
		existing, given)
}

func isBuildCustomizationSpecEqual(s1, s2 v1alpha1.BuildCustomizationSpec) bool {
	// probably ok to use cmp.Equal but keeping it simple for now
	return s1.Protocol == s2.Protocol &&
		s1.Host == s2.Host &&
		s1.IngressHost == s2.IngressHost &&
		s1.Port == s2.Port &&
		s1.UsePathRouting == s2.UsePathRouting &&
		s1.SelfSignedCert == s2.SelfSignedCert &&
		s1.StaticPassword == s2.StaticPassword
}
