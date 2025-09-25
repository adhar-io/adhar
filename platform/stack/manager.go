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

package stack

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
)

// Global variable to store HA mode setting (will be set by applyPlatformStack)
var platformHAMode bool

// setPlatformHAMode sets the global HA mode setting
func setPlatformHAMode(enableHA bool) {
	platformHAMode = enableHA
}

// GetPlatformHAMode returns the current platform HA mode setting
func GetPlatformHAMode() bool {
	return platformHAMode
}

// ApplyPlatformStack applies the core platform components in the correct order with progress tracking
func ApplyPlatformStack(envConfig *config.ResolvedEnvironmentConfig) error {
	// Set environment variable to disable Kind provider progress display
	os.Setenv("ADHAR_PLATFORM_SETUP", "true")
	defer os.Unsetenv("ADHAR_PLATFORM_SETUP")

	// Note: Kind provider progress is shown separately above
	// This progress tracker shows platform-specific installation steps

	// Set HA mode for manifest selection
	enableHA := false
	if envConfig != nil && envConfig.GlobalSettings != nil {
		enableHA = envConfig.GlobalSettings.EnableHAMode
	}
	setPlatformHAMode(enableHA)

	logger.Debugf("Platform HA mode: %t", enableHA)

	// Create progress tracker with detailed step descriptions for the entire workflow
	stepNames := []string{
		"Install Platform CRDs",
		"Create Required Namespaces",
		"Install ArgoCD",
		"Install Gitea",
		"Install Crossplane",
		"Install Nginx Ingress",
		"Install Ingress Resources",
		"Label Core Secrets",
		"Wait for ArgoCD Ready",
		"Apply Platform Stack",
		"Setup GitOps Repositories",
	}

	stepDescriptions := []string{
		"Installing Custom Resource Definitions for platform components",
		"Creating adhar-system namespace",
		"Installing ArgoCD GitOps controller from platform resources",
		"Installing Gitea Git server from platform resources",
		"Installing Crossplane infrastructure provider from platform resources",
		"Installing Nginx Ingress controller from platform resources",
		"Installing ingress resources for platform components",
		"Adding Adhar labels to core secrets for CLI discovery",
		"Waiting for ArgoCD components to be ready",
		"Applying platform ApplicationSets for ArgoCD management",
		"Creating and populating GitOps repositories in Gitea",
	}

	// Create progress tracker for platform setup
	progress := helpers.NewStyledProgressTracker("🚀 Setting up Adhar Platform", stepNames, stepDescriptions)
	defer progress.CompleteStyled()

	// Step 1: Install platform CRDs
	progress.StartStep(0, "Installing Custom Resource Definitions for platform components")
	if err := applyManifests("platform/controllers/resources/"); err != nil {
		progress.FailStep(0, err)
		return fmt.Errorf("failed to install platform CRDs: %w", err)
	}
	progress.CompleteStep(0)
	progress.RenderStyledDisplay()

	// Step 2: Create required namespaces
	progress.StartStep(1, "Creating adhar-system namespace")
	if err := createNamespaces(); err != nil {
		progress.FailStep(1, err)
		return fmt.Errorf("failed to create namespaces: %w", err)
	}
	progress.CompleteStep(1)
	progress.RenderStyledDisplay()

	// Step 3: Install ArgoCD from platform resources
	progress.StartStep(2, "Installing ArgoCD GitOps controller from platform resources")
	if err := applyPlatformManifest("argocd"); err != nil {
		progress.FailStep(2, err)
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}
	progress.CompleteStep(2)
	progress.RenderStyledDisplay()

	// Step 4: Install Gitea from platform resources
	progress.StartStep(3, "Installing Gitea Git server from platform resources")
	if err := applyPlatformManifest("gitea"); err != nil {
		progress.FailStep(3, err)
		return fmt.Errorf("failed to install Gitea: %w", err)
	}
	progress.CompleteStep(3)
	progress.RenderStyledDisplay()

	// Step 5: Install Crossplane from platform resources
	progress.StartStep(4, "Installing Crossplane infrastructure provider from platform resources")
	if err := applyPlatformManifest("crossplane"); err != nil {
		progress.FailStep(4, err)
		return fmt.Errorf("failed to install Crossplane: %w", err)
	}
	progress.CompleteStep(4)
	progress.RenderStyledDisplay()

	// Step 6: Install Nginx Ingress from platform resources
	progress.StartStep(5, "Installing Nginx Ingress controller from platform resources")
	if err := applyPlatformManifest("nginx"); err != nil {
		progress.FailStep(5, err)
		return fmt.Errorf("failed to install Nginx Ingress: %w", err)
	}
	progress.CompleteStep(5)
	progress.RenderStyledDisplay()

	// Step 7: Install Ingress Resources for platform components
	progress.StartStep(6, "Installing ingress resources for platform components")
	if err := applyIngressManifests(); err != nil {
		progress.FailStep(6, err)
		return fmt.Errorf("failed to install ingress resources: %w", err)
	}
	progress.CompleteStep(6)
	progress.RenderStyledDisplay()

	// Step 8: Label core secrets for CLI discovery
	progress.StartStep(7, "Adding Adhar labels to core secrets for CLI discovery")
	if err := labelCoreSecrets(); err != nil {
		// Don't fail completely, just warn and skip
		progress.SkipStep(7, "Failed to label secrets, continuing anyway")
		progress.RenderStyledDisplay()
		logger.Warnf("Secret labeling failed, continuing anyway: %v", err)
	} else {
		progress.CompleteStep(7)
		progress.RenderStyledDisplay()
	}

	// Step 9: Wait for ArgoCD to be ready
	progress.StartStep(8, "Waiting for ArgoCD components to be ready")
	if err := waitForArgoCD(); err != nil {
		progress.FailStep(8, err)
		return fmt.Errorf("failed to wait for ArgoCD: %w", err)
	}
	progress.CompleteStep(8)
	progress.RenderStyledDisplay()

	// Step 10: Apply platform stack ApplicationSets with GitOps
	progress.StartStep(9, "Applying platform ApplicationSets for ArgoCD management with GitOps")
	if err := applyPlatformApplicationSetsWithGitOps(); err != nil {
		progress.FailStep(9, err)
		return fmt.Errorf("failed to apply platform ApplicationSets with GitOps: %w", err)
	}
	progress.CompleteStep(9)
	progress.RenderStyledDisplay()

	// Step 11: Setup GitOps repositories
	progress.StartStep(10, "Creating and populating GitOps repositories in Gitea")
	if err := setupGitOpsRepositories(); err != nil {
		progress.FailStep(10, err)
		return fmt.Errorf("failed to setup GitOps repositories: %w", err)
	}
	progress.CompleteStep(10)
	progress.RenderStyledDisplay()

	// Complete the progress tracker
	progress.CompleteStyled()

	return nil
}

// applyManifests applies Kubernetes manifests from the specified path
func applyManifests(path string) error {
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
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to apply manifest %s: %w", file, err)
		}
	}

	return nil
}

// createNamespaces creates the required namespaces for the platform
func createNamespaces() error {
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
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create namespace %s: %w", ns, err)
		}
	}

	return nil
}

// applyPlatformManifest applies a specific platform component manifest
func applyPlatformManifest(component string) error {
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply %s manifest: %w", component, err)
	}

	// Wait for the component to be ready
	if err := waitForComponentReady(component); err != nil {
		logger.Warnf("Component %s may not be fully ready: %v", component, err)
	}

	return nil
}

// applyIngressManifests applies ingress resources for platform components
func applyIngressManifests() error {
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
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to apply ingress manifest %s: %w", file, err)
		}
	}

	return nil
}

// labelCoreSecrets adds Adhar labels to core secrets for CLI discovery
func labelCoreSecrets() error {
	logger.Info("Adding Adhar labels to core secrets")

	// Label ArgoCD admin secret
	cmd := exec.Command("kubectl", "label", "secret", "argocd-initial-admin-secret",
		"app.kubernetes.io/part-of=adhar", "app.kubernetes.io/component=argocd",
		"--namespace=adhar-system", "--overwrite")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		logger.Warnf("Failed to label ArgoCD secret (may not exist yet): %v", err)
	}

	// Label Gitea admin secret
	cmd = exec.Command("kubectl", "label", "secret", "gitea-admin-secret",
		"app.kubernetes.io/part-of=adhar", "app.kubernetes.io/component=gitea",
		"--namespace=adhar-system", "--overwrite")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		logger.Warnf("Failed to label Gitea secret (may not exist yet): %v", err)
	}

	return nil
}

// waitForArgoCD waits for ArgoCD components to be ready
func waitForArgoCD() error {
	logger.Info("Waiting for ArgoCD components to be ready")

	// Wait for ArgoCD server deployment
	cmd := exec.Command("kubectl", "wait", "--for=condition=available",
		"deployment/argocd-server", "--namespace=adhar-system", "--timeout=300s")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD server not ready: %w", err)
	}

	// Wait for ArgoCD application controller
	cmd = exec.Command("kubectl", "wait", "--for=condition=available",
		"statefulset/argocd-application-controller", "--namespace=adhar-system", "--timeout=300s")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD application controller not ready: %w", err)
	}

	logger.Info("ArgoCD components are ready")
	return nil
}

// applyPlatformApplicationSets applies platform ApplicationSets for ArgoCD management
func applyPlatformApplicationSets() error {
	logger.Info("Applying platform ApplicationSets for ArgoCD management")

	// Apply the local ApplicationSet
	appsetFile := "platform/stack/adhar-appset-local.yaml"

	if _, err := os.Stat(appsetFile); os.IsNotExist(err) {
		return fmt.Errorf("ApplicationSet file does not exist: %s", appsetFile)
	}

	cmd := exec.Command("kubectl", "apply", "-f", appsetFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply ApplicationSet: %w", err)
	}

	return nil
}

// applyPlatformApplicationSetsWithGitOps applies platform ApplicationSets with GitOps and adhar:// references
func applyPlatformApplicationSetsWithGitOps() error {
	logger.Info("Applying platform ApplicationSets with GitOps and adhar:// references")

	// Create GitOps resolver
	resolver := NewGitOpsResolver()

	// Update ApplicationSet to use GitOps
	if err := resolver.UpdateApplicationSetToUseGitOps(); err != nil {
		return fmt.Errorf("failed to update ApplicationSet to use GitOps: %w", err)
	}

	// Also apply the original local ApplicationSet for backward compatibility
	appsetFile := "platform/stack/adhar-appset-local.yaml"
	if _, err := os.Stat(appsetFile); err == nil {
		cmd := exec.Command("kubectl", "apply", "-f", appsetFile)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			logger.Warnf("Failed to apply local ApplicationSet (non-critical): %v", err)
		}
	}

	return nil
}

// setupGitOpsRepositories creates and populates GitOps repositories in Gitea
func setupGitOpsRepositories() error {
	logger.Info("Setting up GitOps repositories in Gitea")

	// Wait for Gitea to be ready
	if err := waitForGiteaReady(); err != nil {
		return fmt.Errorf("Gitea not ready: %w", err)
	}

	// Create environments repository
	if err := createGiteaRepository("environments"); err != nil {
		return fmt.Errorf("failed to create environments repository: %w", err)
	}

	// Create packages repository
	if err := createGiteaRepository("packages"); err != nil {
		return fmt.Errorf("failed to create packages repository: %w", err)
	}

	// Populate repositories with content
	if err := populateRepositories(); err != nil {
		return fmt.Errorf("failed to populate repositories: %w", err)
	}

	return nil
}

// waitForComponentReady waits for a component to be ready
func waitForComponentReady(component string) error {
	logger.Infof("Waiting for %s to be ready", component)

	// Simple wait - in production this would be more sophisticated
	time.Sleep(10 * time.Second)

	return nil
}

// waitForGiteaReady waits for Gitea to be ready
func waitForGiteaReady() error {
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
func createGiteaRepository(name string) error {
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
func populateRepositories() error {
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
	if err := populatePackagesRepository(podName); err != nil {
		return fmt.Errorf("failed to populate packages repository: %w", err)
	}

	// Populate environments repository
	if err := populateEnvironmentsRepository(podName); err != nil {
		return fmt.Errorf("failed to populate environments repository: %w", err)
	}

	logger.Info("Successfully populated all GitOps repositories")
	return nil
}

// populatePackagesRepository populates the packages repository with platform stack content
func populatePackagesRepository(podName string) error {
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
func populateEnvironmentsRepository(podName string) error {
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
