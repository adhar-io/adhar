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
	if err := os.Setenv("ADHAR_PLATFORM_SETUP", "true"); err != nil {
		return fmt.Errorf("failed to set ADHAR_PLATFORM_SETUP: %w", err)
	}
	defer func() {
		if err := os.Unsetenv("ADHAR_PLATFORM_SETUP"); err != nil {
			logger.Warnf("failed to unset ADHAR_PLATFORM_SETUP: %v", err)
		}
	}()

	// Note: Kind provider progress is shown separately above
	// This progress tracker shows platform-specific installation steps

	// Set HA mode for manifest selection
	enableHA := false
	if envConfig != nil && envConfig.GlobalSettings != nil {
		enableHA = envConfig.GlobalSettings.EnableHAMode
	}
	setPlatformHAMode(enableHA)

	logger.Debugf("Platform HA mode: %t", enableHA)

	// Create progress tracker with detailed step descriptions for GitOps-first workflow
	// Bootstrap: Only install what's needed to enable GitOps (CNI, ArgoCD, Gitea)
	// Everything else: Managed through Git and ArgoCD
	stepNames := []string{
		"Bootstrap: Install Cilium CNI",
		"Bootstrap: Create Namespaces",
		"Bootstrap: Install ArgoCD",
		"Bootstrap: Install Gitea",
		"GitOps: Setup Repositories",
		"GitOps: Create Bootstrap App",
		"GitOps: Apply Platform Stack",
		"GitOps: Wait for Sync",
	}

	stepDescriptions := []string{
		"Installing Cilium CNI (network must work first)",
		"Creating adhar-system and required namespaces",
		"Installing ArgoCD GitOps controller (enables GitOps)",
		"Installing Gitea Git server (hosts manifests)",
		"Creating and populating Git repositories with platform manifests",
		"Creating ArgoCD Application to bootstrap platform from Git",
		"Applying ApplicationSets for platform packages and environments",
		"Waiting for ArgoCD to sync all platform components from Git",
	}

	// Create progress tracker for platform setup
	progress := helpers.NewStyledProgressTracker("🚀 Setting up Adhar Platform (GitOps-First)", stepNames, stepDescriptions)
	defer progress.CompleteStyled()

	// =================================================================
	// BOOTSTRAP PHASE: Minimal imperative setup to enable GitOps
	// =================================================================

	// Step 1: Install Cilium CNI (network must work first)
	progress.StartStep(0, "Installing Cilium CNI for container networking")
	if err := bootstrapCilium(); err != nil {
		progress.FailStep(0, err)
		return fmt.Errorf("failed to bootstrap Cilium CNI: %w", err)
	}
	progress.CompleteStep(0)
	progress.RenderStyledDisplay()

	// Step 2: Create required namespaces
	progress.StartStep(1, "Creating adhar-system and required namespaces")
	if err := createNamespaces(); err != nil {
		progress.FailStep(1, err)
		return fmt.Errorf("failed to create namespaces: %w", err)
	}
	progress.CompleteStep(1)
	progress.RenderStyledDisplay()

	// Step 3: Bootstrap ArgoCD (enables GitOps)
	progress.StartStep(2, "Bootstrapping ArgoCD GitOps controller")
	if err := bootstrapArgoCD(); err != nil {
		progress.FailStep(2, err)
		return fmt.Errorf("failed to bootstrap ArgoCD: %w", err)
	}
	progress.CompleteStep(2)
	progress.RenderStyledDisplay()

	// Step 4: Bootstrap Gitea (hosts Git repositories)
	progress.StartStep(3, "Bootstrapping Gitea Git server")
	if err := bootstrapGitea(); err != nil {
		progress.FailStep(3, err)
		return fmt.Errorf("failed to bootstrap Gitea: %w", err)
	}
	progress.CompleteStep(3)
	progress.RenderStyledDisplay()

	// =================================================================
	// GITOPS PHASE: Everything else managed through Git
	// =================================================================

	// Step 5: Setup GitOps repositories with ALL platform manifests
	progress.StartStep(4, "Creating and populating Git repositories with platform manifests")
	if err := setupGitOpsRepositoriesWithBootstrap(); err != nil {
		progress.FailStep(4, err)
		return fmt.Errorf("failed to setup GitOps repositories: %w", err)
	}
	progress.CompleteStep(4)
	progress.RenderStyledDisplay()

	// Step 6: Create ArgoCD Application for bootstrap repo
	progress.StartStep(5, "Creating ArgoCD Application to bootstrap platform from Git")
	if err := createBootstrapApplication(); err != nil {
		progress.FailStep(5, err)
		return fmt.Errorf("failed to create bootstrap application: %w", err)
	}
	progress.CompleteStep(5)
	progress.RenderStyledDisplay()

	// Step 7: Apply platform ApplicationSets (after bootstrap app is syncing)
	progress.StartStep(6, "Applying platform ApplicationSets for packages and environments")
	if err := applyPlatformApplicationSetsWithGitOps(); err != nil {
		progress.FailStep(6, err)
		return fmt.Errorf("failed to apply platform ApplicationSets: %w", err)
	}
	progress.CompleteStep(6)
	progress.RenderStyledDisplay()

	// Step 8: Wait for ArgoCD to sync all applications from Git
	progress.StartStep(7, "Waiting for ArgoCD to sync all platform components from Git")
	if err := waitForPlatformSync(); err != nil {
		// Non-fatal - platform might still be syncing
		logger.Warnf("Some platform components may still be syncing: %v", err)
		progress.SkipStep(7, "Platform components syncing (may take a few minutes)")
	} else {
		progress.CompleteStep(7)
	}
	progress.RenderStyledDisplay()

	// Complete the progress tracker
	progress.CompleteStyled()

	logger.Info("✅ Platform bootstrap complete! ArgoCD is now managing all components from Git.")
	logger.Info("📊 Monitor sync status: kubectl get applications -n adhar-system")

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
	waitForComponentReady(component)

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

// waitForComponentReady waits for a component to be ready
func waitForComponentReady(component string) {
	logger.Infof("Waiting for %s to be ready", component)

	// Simple wait - in production this would be more sophisticated
	time.Sleep(10 * time.Second)
}

// waitForGiteaReady waits for Gitea to be ready with comprehensive checks
func waitForGiteaReady() error {
	logger.Info("Waiting for Gitea to be fully ready...")

	// Step 1: Wait for Gitea deployment to be available
	logger.Info("1/5: Waiting for Gitea deployment to be available")
	cmd := exec.Command("kubectl", "wait", "--for=condition=available",
		"deployment/gitea", "--namespace=adhar-system", "--timeout=600s")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Gitea deployment not available: %w - %s", err, string(output))
	}

	// Step 2: Wait for Gitea pods to be running
	logger.Info("2/5: Waiting for Gitea pods to be running")
	podCmd := exec.Command("kubectl", "wait", "--for=condition=ready",
		"pod", "-l", "app=gitea", "--namespace=adhar-system", "--timeout=600s")
	podOutput, podErr := podCmd.CombinedOutput()
	if podErr != nil {
		return fmt.Errorf("Gitea pods not ready: %w - %s", podErr, string(podOutput))
	}

	// Step 3: Wait for Gitea service to have endpoints
	logger.Info("3/5: Verifying Gitea service has endpoints")
	time.Sleep(5 * time.Second)
	endpointCmd := exec.Command("kubectl", "get", "endpoints", "gitea-http",
		"-n", "adhar-system", "-o", "jsonpath={.subsets[0].addresses[0].ip}")
	endpointOutput, endpointErr := endpointCmd.Output()
	if endpointErr != nil || strings.TrimSpace(string(endpointOutput)) == "" {
		logger.Warn("Gitea service endpoints not ready, waiting additional 10 seconds...")
		time.Sleep(10 * time.Second)
	}

	// Step 4: Give Gitea additional time to fully initialize its database and API
	logger.Info("4/5: Waiting for Gitea database and API initialization (30 seconds)")
	time.Sleep(30 * time.Second)

	// Step 5: Verify Gitea is actually responding
	logger.Info("5/5: Verifying Gitea API is responding")
	if err := verifyGiteaAPI(); err != nil {
		logger.Warnf("Gitea API verification failed, waiting additional 15 seconds: %v", err)
		time.Sleep(15 * time.Second)
		// Try one more time
		if err := verifyGiteaAPI(); err != nil {
			return fmt.Errorf("Gitea API still not responding: %w", err)
		}
	}

	logger.Info("✅ Gitea is fully ready for repository operations")
	return nil
}

// verifyGiteaAPI verifies that Gitea's API is accessible and responding
func verifyGiteaAPI() error {
	logger.Debug("Verifying Gitea API accessibility")

	// Get Gitea pod name
	cmd := exec.Command("kubectl", "get", "pods", "-n", "adhar-system",
		"-l", "app=gitea", "-o", "jsonpath={.items[0].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Gitea pod: %w", err)
	}

	podName := strings.TrimSpace(string(output))
	if podName == "" {
		return fmt.Errorf("no Gitea pod found")
	}

	// Try to access Gitea API version endpoint
	testCmd := "curl -s -f -m 5 http://localhost:3000/api/v1/version"
	apiCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "sh", "-c", testCmd)
	apiOutput, apiErr := apiCmd.CombinedOutput()

	if apiErr != nil {
		return fmt.Errorf("Gitea API not responding: %w - %s", apiErr, string(apiOutput))
	}

	logger.Debug("✅ Gitea API is accessible and responding")
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
	if podName == "" {
		return fmt.Errorf("no Gitea pod found")
	}

	logger.Infof("Using Gitea pod: %s", podName)

	// Create repository using Gitea API with retries
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		createCmd := fmt.Sprintf(`
			curl -s -X POST "http://localhost:3000/api/v1/admin/users/gitea_admin/repos" \
			-H "Content-Type: application/json" \
			-d '{"name":"%s","description":"%s repository","private":false,"auto_init":true,"default_branch":"main"}' \
			-u gitea_admin:r8sA8CPHD9!bt6d
		`, name, name)

		cmd = exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "sh", "-c", createCmd)
		output, err := cmd.CombinedOutput()

		if err == nil {
			logger.Infof("✅ Repository '%s' created successfully", name)
			return nil
		}

		// Check if repository already exists (which is fine)
		if strings.Contains(string(output), "already exists") || strings.Contains(string(output), "conflict") {
			logger.Infof("✅ Repository '%s' already exists", name)
			return nil
		}

		logger.Warnf("Attempt %d/%d failed to create repository %s: %v", i+1, maxRetries, name, err)
		if i < maxRetries-1 {
			logger.Info("Retrying in 5 seconds...")
			time.Sleep(5 * time.Second)
		}
	}

	return fmt.Errorf("failed to create repository %s after %d attempts", name, maxRetries)
}

// populatePackagesRepository populates the packages repository with platform stack content
func populatePackagesRepository(podName string) error {
	logger.Info("Populating packages repository with platform stack content")

	// Clean up any existing working directory
	cleanupCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/packages-working")
	if err := cleanupCmd.Run(); err != nil {
		logger.Debugf("failed to remove existing packages working dir: %v", err)
	}

	// Wait a moment for cleanup
	time.Sleep(2 * time.Second)

	// Clone the existing repository
	logger.Info("Cloning packages repository")
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
		initCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "init", "-b", "main")
		if err := initCmd.Run(); err != nil {
			return fmt.Errorf("failed to initialize git repository: %w", err)
		}
	}

	// Remove all existing content (excluding .git)
	logger.Info("Cleaning existing content")
	removeCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "sh", "-c", "cd /tmp/packages-working && find . -mindepth 1 -maxdepth 1 ! -name '.git' -exec rm -rf {} +")
	if err := removeCmd.Run(); err != nil {
		logger.Debugf("failed to clean packages directory contents: %v", err)
	}

	// Copy the packages content
	logger.Info("Copying platform/stack/packages content to repository")
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
	logger.Info("Committing changes to packages repository")
	commitCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "commit", "-m", "feat: Add all platform packages and stack content")
	output, commitErr := commitCmd.CombinedOutput()
	if commitErr != nil {
		// If commit fails, it might be because there are no changes, which is okay
		if strings.Contains(string(output), "nothing to commit") {
			logger.Info("No changes to commit (repository already up to date)")
			return nil
		}
		logger.Warnf("Git commit failed: %v - %s", commitErr, string(output))
	}

	// Add remote origin if it doesn't exist
	remoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "remote", "add", "origin", "/data/git/gitea-repositories/gitea_admin/packages.git")
	if err := remoteCmd.Run(); err != nil {
		logger.Debugf("unable to add packages remote (likely exists): %v", err)
	}

	// Push changes with force to ensure it works
	logger.Info("Pushing changes to packages repository")
	pushCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "push", "-u", "origin", "main", "--force")
	pushOutput, pushErr := pushCmd.CombinedOutput()
	if pushErr != nil {
		logger.Warnf("Failed to push to main branch: %v - %s", pushErr, string(pushOutput))
		// Try pushing to master if main doesn't work
		logger.Info("Retrying with master branch")
		pushMasterCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/packages-working", "push", "-u", "origin", "master", "--force")
		pushMasterOutput, pushMasterErr := pushMasterCmd.CombinedOutput()
		if pushMasterErr != nil {
			return fmt.Errorf("failed to push to packages repository: %v - %s", pushMasterErr, string(pushMasterOutput))
		}
	}

	logger.Info("✅ Packages repository populated and pushed successfully!")
	return nil
}

// populateEnvironmentsRepository populates the environments repository with environment configurations
func populateEnvironmentsRepository(podName string) error {
	logger.Info("Populating environments repository with environment configurations")

	// Clean up any existing working directory
	cleanupCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/environments-working")
	if err := cleanupCmd.Run(); err != nil {
		logger.Debugf("failed to remove environments working dir: %v", err)
	}

	// Wait a moment for cleanup
	time.Sleep(2 * time.Second)

	// Clone the existing repository
	logger.Info("Cloning environments repository")
	cloneCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "clone",
		"/data/git/gitea-repositories/gitea_admin/environments.git", "/tmp/environments-working")
	if err := cloneCmd.Run(); err != nil {
		logger.Warnf("Failed to clone environments repository (may not exist yet): %v", err)
		// Create the directory if it doesn't exist
		createCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "mkdir", "-p", "/tmp/environments-working")
		if err := createCmd.Run(); err != nil {
			return fmt.Errorf("failed to create environments working directory: %w", err)
		}
		// Initialize git repository with main branch
		initCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "init", "-b", "main")
		if err := initCmd.Run(); err != nil {
			return fmt.Errorf("failed to initialize git repository: %w", err)
		}
	}

	// Remove all existing content (excluding .git)
	logger.Info("Cleaning existing content")
	removeCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "sh", "-c", "cd /tmp/environments-working && find . -mindepth 1 -maxdepth 1 ! -name '.git' -exec rm -rf {} +")
	if err := removeCmd.Run(); err != nil {
		logger.Debugf("failed to clean environments directory contents: %v", err)
	}

	// Copy the environments content
	logger.Info("Copying platform/stack/environments content to repository")
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
	logger.Info("Committing changes to environments repository")
	commitCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "commit", "-m", "feat: Add environment configurations")
	output, commitErr := commitCmd.CombinedOutput()
	if commitErr != nil {
		// If commit fails, it might be because there are no changes, which is okay
		if strings.Contains(string(output), "nothing to commit") {
			logger.Info("No changes to commit (repository already up to date)")
			return nil
		}
		logger.Warnf("Git commit failed: %v - %s", commitErr, string(output))
	}

	// Add remote origin if it doesn't exist
	remoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "remote", "add", "origin", "/data/git/gitea-repositories/gitea_admin/environments.git")
	if err := remoteCmd.Run(); err != nil {
		logger.Debugf("unable to add environments remote (likely exists): %v", err)
	}

	// Push changes with force to ensure it works
	logger.Info("Pushing changes to environments repository")
	pushCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "push", "-u", "origin", "main", "--force")
	pushOutput, pushErr := pushCmd.CombinedOutput()
	if pushErr != nil {
		logger.Warnf("Failed to push to main branch: %v - %s", pushErr, string(pushOutput))
		// Try pushing to master if main doesn't work
		logger.Info("Retrying with master branch")
		pushMasterCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/environments-working", "push", "-u", "origin", "master", "--force")
		pushMasterOutput, pushMasterErr := pushMasterCmd.CombinedOutput()
		if pushMasterErr != nil {
			return fmt.Errorf("failed to push to environments repository: %v - %s", pushMasterErr, string(pushMasterOutput))
		}
	}

	logger.Info("✅ Environments repository populated and pushed successfully!")
	return nil
}

// applyArgoCDRepoAuth applies ArgoCD repository authentication configuration
func applyArgoCDRepoAuth() error {
	logger.Info("Applying ArgoCD repository authentication for Gitea access")

	// Apply the argocd-auth.yaml file which contains repository secrets
	authFile := "platform/stack/argocd-auth.yaml"
	if _, err := os.Stat(authFile); err != nil {
		logger.Warnf("ArgoCD auth file not found: %s", authFile)
		return nil // Not fatal
	}

	cmd := exec.Command("kubectl", "apply", "-f", authFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply ArgoCD repository authentication: %w - %s", err, string(output))
	}

	logger.Info("✅ ArgoCD repository authentication applied successfully!")
	return nil
}

// =================================================================
// BOOTSTRAP FUNCTIONS: Minimal imperative setup for GitOps
// =================================================================

// bootstrapCilium installs Cilium CNI (network must work first)
func bootstrapCilium() error {
	logger.Info("Bootstrapping Cilium CNI for container networking")

	// Cilium is typically installed by the cluster provider (Kind, cloud providers)
	// For Kind, it's already specified in the Kind config
	// For cloud providers, they have their own CNI
	// This is mainly a placeholder for any CNI-specific setup needed

	logger.Info("✅ Cilium CNI bootstrap complete (using provider CNI)")
	return nil
}

// bootstrapArgoCD installs ArgoCD (enables GitOps)
func bootstrapArgoCD() error {
	logger.Info("Bootstrapping ArgoCD GitOps controller")

	if err := applyPlatformManifest("argocd"); err != nil {
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}

	// Wait for ArgoCD to be ready
	if err := waitForArgoCD(); err != nil {
		return fmt.Errorf("ArgoCD not ready: %w", err)
	}

	logger.Info("✅ ArgoCD bootstrap complete")
	return nil
}

// bootstrapGitea installs Gitea (hosts Git repositories)
func bootstrapGitea() error {
	logger.Info("Bootstrapping Gitea Git server")

	if err := applyPlatformManifest("gitea"); err != nil {
		return fmt.Errorf("failed to install Gitea: %w", err)
	}

	// Wait for Gitea to be fully ready with comprehensive checks
	logger.Info("Waiting for Gitea to be fully operational (this may take a few minutes)")
	if err := waitForGiteaReady(); err != nil {
		// Provide diagnostic information if Gitea fails to start
		logger.Warn("Gitea failed to become ready. Gathering diagnostic information...")

		// Check if Gitea pod exists
		checkCmd := exec.Command("kubectl", "get", "pods", "-n", "adhar-system", "-l", "app=gitea")
		if checkOutput, _ := checkCmd.CombinedOutput(); len(checkOutput) > 0 {
			logger.Warnf("Gitea pod status:\n%s", string(checkOutput))
		}

		// Check Gitea deployment status
		deployCmd := exec.Command("kubectl", "get", "deployment", "gitea", "-n", "adhar-system", "-o", "wide")
		if deployOutput, _ := deployCmd.CombinedOutput(); len(deployOutput) > 0 {
			logger.Warnf("Gitea deployment status:\n%s", string(deployOutput))
		}

		// Check pod logs for errors
		logsCmd := exec.Command("kubectl", "logs", "-n", "adhar-system", "-l", "app=gitea", "--tail=50")
		if logsOutput, _ := logsCmd.CombinedOutput(); len(logsOutput) > 0 {
			logger.Warnf("Gitea pod logs (last 50 lines):\n%s", string(logsOutput))
		}

		return fmt.Errorf("Gitea not ready: %w", err)
	}

	logger.Info("✅ Gitea bootstrap complete and fully operational")
	return nil
}

// =================================================================
// GITOPS FUNCTIONS: Repository and application management
// =================================================================

// setupGitOpsRepositoriesWithBootstrap creates and populates ALL GitOps repositories
// including a new 'bootstrap' repository for platform components
func setupGitOpsRepositoriesWithBootstrap() error {
	logger.Info("Setting up GitOps repositories with platform bootstrap manifests")

	// CRITICAL: Wait for Gitea to be fully ready before any operations
	logger.Info("Ensuring Gitea is fully ready before repository operations...")
	if err := waitForGiteaReady(); err != nil {
		return fmt.Errorf("Gitea not ready for repository operations: %w", err)
	}

	// Verify Gitea API is accessible
	if err := verifyGiteaAPI(); err != nil {
		return fmt.Errorf("Gitea API not accessible: %w", err)
	}

	// Create all repositories
	logger.Info("Creating Git repositories in Gitea")
	repos := []string{"bootstrap", "packages", "environments"}
	for _, repo := range repos {
		if err := createGiteaRepository(repo); err != nil {
			return fmt.Errorf("failed to create repository %s: %w", repo, err)
		}
	}

	// Get Gitea pod name for file operations
	cmd := exec.Command("kubectl", "get", "pods", "-n", "adhar-system",
		"-l", "app=gitea", "-o", "jsonpath={.items[0].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Gitea pod name: %w", err)
	}
	podName := strings.TrimSpace(string(output))

	// Populate bootstrap repository with platform manifests
	if err := populateBootstrapRepository(podName); err != nil {
		return fmt.Errorf("failed to populate bootstrap repository: %w", err)
	}

	// Populate packages repository
	if err := populatePackagesRepository(podName); err != nil {
		return fmt.Errorf("failed to populate packages repository: %w", err)
	}

	// Populate environments repository
	if err := populateEnvironmentsRepository(podName); err != nil {
		return fmt.Errorf("failed to populate environments repository: %w", err)
	}

	// Apply ArgoCD repository authentication
	if err := applyArgoCDRepoAuth(); err != nil {
		logger.Warnf("Failed to apply ArgoCD repository authentication: %v", err)
	}

	logger.Info("✅ All GitOps repositories setup complete!")
	return nil
}

// populateBootstrapRepository populates the bootstrap repository with platform manifests
func populateBootstrapRepository(podName string) error {
	logger.Info("Populating bootstrap repository with platform manifests")

	// Clean up any existing working directory
	cleanupCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "rm", "-rf", "/tmp/bootstrap-working")
	if err := cleanupCmd.Run(); err != nil {
		logger.Debugf("failed to remove bootstrap working dir: %v", err)
	}
	time.Sleep(2 * time.Second)

	// Create and initialize working directory
	createCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "mkdir", "-p", "/tmp/bootstrap-working")
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("failed to create bootstrap working directory: %w", err)
	}

	initCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/bootstrap-working", "init", "-b", "main")
	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("failed to initialize git repository: %w", err)
	}

	// Copy platform manifests to bootstrap repository
	// These are the components that were previously applied directly
	logger.Info("Copying platform manifests to bootstrap repository")

	// Create directory structure
	mkdirCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--",
		"sh", "-c", "mkdir -p /tmp/bootstrap-working/{crossplane,nginx,ingress,control-plane,crds}")
	if err := mkdirCmd.Run(); err != nil {
		return fmt.Errorf("failed to create directory structure: %w", err)
	}

	// Copy manifests
	manifestSources := map[string]string{
		"platform/controllers/adharplatform/resources/crossplane/install.yaml": "crossplane/",
		"platform/controllers/adharplatform/resources/nginx/install.yaml":      "nginx/",
		"platform/controllers/adharplatform/resources/ingress":                 "ingress/",
		"platform/controllers/resources":                                       "crds/",
		"platform/controlplane/configuration":                                  "control-plane/",
	}

	for src, dest := range manifestSources {
		logger.Infof("Copying %s to bootstrap repo", src)
		copyCmd := exec.Command("kubectl", "cp", src, fmt.Sprintf("adhar-system/%s:/tmp/bootstrap-working/%s", podName, dest))
		if err := copyCmd.Run(); err != nil {
			logger.Warnf("Failed to copy %s (may not exist): %v", src, err)
			// Continue with other files
		}
	}

	// Configure git
	configUserCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/bootstrap-working", "config", "user.name", "Adhar Platform")
	if err := configUserCmd.Run(); err != nil {
		logger.Warnf("failed to configure bootstrap git user: %v", err)
	}

	configEmailCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/bootstrap-working", "config", "user.email", "admin@adhar.io")
	if err := configEmailCmd.Run(); err != nil {
		logger.Warnf("failed to configure bootstrap git email: %v", err)
	}

	// Add all files
	addCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/bootstrap-working", "add", ".")
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("failed to add files to git: %w", err)
	}

	// Commit
	logger.Info("Committing platform manifests to bootstrap repository")
	commitCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/bootstrap-working", "commit", "-m", "feat: Add platform bootstrap manifests")
	output, commitErr := commitCmd.CombinedOutput()
	if commitErr != nil {
		if strings.Contains(string(output), "nothing to commit") {
			logger.Info("No changes to commit (repository already up to date)")
			return nil
		}
		logger.Warnf("Git commit failed: %v - %s", commitErr, string(output))
	}

	// Add remote and push
	remoteCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/bootstrap-working", "remote", "add", "origin", "/data/git/gitea-repositories/gitea_admin/bootstrap.git")
	if err := remoteCmd.Run(); err != nil {
		logger.Debugf("unable to add bootstrap remote (likely exists): %v", err)
	}

	logger.Info("Pushing bootstrap manifests to Git")
	pushCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", podName, "--", "git", "-C", "/tmp/bootstrap-working", "push", "-u", "origin", "main", "--force")
	pushOutput, pushErr := pushCmd.CombinedOutput()
	if pushErr != nil {
		return fmt.Errorf("failed to push to bootstrap repository: %v - %s", pushErr, string(pushOutput))
	}

	logger.Info("✅ Bootstrap repository populated and pushed successfully!")
	return nil
}

// createBootstrapApplication creates an ArgoCD Application for the bootstrap repository
func createBootstrapApplication() error {
	logger.Info("Creating ArgoCD Application for platform bootstrap")

	// Create the bootstrap Application manifest
	bootstrapApp := `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: adhar-bootstrap
  namespace: adhar-system
  labels:
    app.kubernetes.io/name: adhar-bootstrap
    app.kubernetes.io/part-of: adhar-platform
  finalizers:
    - resources-finalizer.argoproj.io
spec:
  destination:
    namespace: adhar-system
    server: https://kubernetes.default.svc
  project: default
  sources:
    - path: .
      repoURL: http://gitea-argocd.adhar-system.svc.cluster.local:3000/gitea_admin/bootstrap
      targetRevision: main
      directory:
        recurse: true
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    retry:
      backoff:
        duration: 5s
        factor: 2
        maxDuration: 3m0s
      limit: 30
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
`

	// Write and apply the manifest
	tmpFile := "/tmp/adhar-bootstrap-app.yaml"
	if err := os.WriteFile(tmpFile, []byte(bootstrapApp), 0644); err != nil {
		return fmt.Errorf("failed to write bootstrap application manifest: %w", err)
	}
	defer func() {
		if err := os.Remove(tmpFile); err != nil && !os.IsNotExist(err) {
			logger.Warnf("failed to remove temporary bootstrap manifest %s: %v", tmpFile, err)
		}
	}()

	cmd := exec.Command("kubectl", "apply", "-f", tmpFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply bootstrap application: %w - %s", err, string(output))
	}

	logger.Info("✅ Bootstrap Application created - ArgoCD will sync platform components from Git")
	return nil
}

// waitForPlatformSync waits for ArgoCD to sync platform applications
func waitForPlatformSync() error {
	logger.Info("Waiting for ArgoCD to sync platform components (this may take a few minutes)")

	// Wait up to 5 minutes for the bootstrap app to be healthy
	maxWait := 5 * time.Minute
	checkInterval := 10 * time.Second
	elapsed := 0 * time.Second

	for elapsed < maxWait {
		cmd := exec.Command("kubectl", "get", "application", "adhar-bootstrap", "-n", "adhar-system",
			"-o", "jsonpath={.status.sync.status}")
		output, err := cmd.Output()

		if err == nil && strings.TrimSpace(string(output)) == "Synced" {
			logger.Info("✅ Platform components synced from Git!")
			return nil
		}

		logger.Infof("Platform still syncing... (elapsed: %v)", elapsed)
		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	return fmt.Errorf("timeout waiting for platform sync (check: kubectl get app adhar-bootstrap -n adhar-system)")
}
