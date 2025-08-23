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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
	pkind "adhar-io/adhar/platform/providers/kind"
	ptypes "adhar-io/adhar/platform/types"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	recreateClusterUsage           = "Delete cluster first if it already exists."
	devPasswordUsage               = "Set the password \"developer\" for the admin user of the applications: argocd & gitea."
	kubeVersionUsage               = "Version of the kind kubernetes cluster to create."
	extraPortsMappingUsage         = "List of extra ports to expose on the docker container and kubernetes cluster as nodePort(e.g. \"22:32222,9090:39090,etc\")."
	registryConfigUsage            = "List of paths to mount as the registry config, uses the first one that exists"
	kindConfigPathUsage            = "Path or URL to the kind config file to be used instead of the default."
	hostUsage                      = "Host name to access resources in this cluster."
	ingressHostUsage               = "Host name used by ingresses. Useful when you have another proxy in front of ingress-nginx that adhar provisions."
	protocolUsage                  = "Protocol to use to access web UIs. http or https."
	portUsage                      = "Port number to use to access web UIs."
	pathRoutingUsage               = "When set to true, web UIs are exposed under single domain name. e.g. \"https://adhar.localtest.me/argocd\" instead of \"https://argocd.adhar.localtest.me\""
	extraPackagesUsage             = "Paths to locations containing custom packages"
	packageCustomizationFilesUsage = "Name of the package and the path to file to customize the core packages with. valid package names are: argocd, nginx, and gitea. e.g. argocd:/tmp/argocd.yaml"
	noExitUsage                    = "When set, adhar will not exit after all packages are synced. Useful for continuously syncing local directories."
)

var (
	// Flags
	recreateCluster           bool
	devPassword               bool
	kubeVersion               string
	extraPortsMapping         string
	kindConfigPath            string
	extraPackages             []string
	registryConfig            []string
	packageCustomizationFiles []string
	noExit                    bool
	protocol                  string
	host                      string
	ingressHost               string
	port                      string
	pathRouting               bool
	verbose                   bool // Add verbose flag

	// Production cluster provisioning flags
	configFile  string
	environment string
	dryRun      bool
	force       bool
)

// Local provisioning options (replaces legacy build.ProvisionOptions)
type ProvisionOptions struct {
	DryRun bool
	Force  bool
}

// Lightweight provider manager backed by platform/providers factory
type providerManager struct {
	factory pfactory.ProviderFactory
}

func newProviderManagerWithFactory(_ interface{}, factory pfactory.ProviderFactory) *providerManager {
	return &providerManager{factory: factory}
}

// ProvisionEnvironment provisions using the appropriate provider based on configuration
func (pm *providerManager) ProvisionEnvironment(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	providerType := strings.ToLower(envConfig.ResolvedProvider)

	// Build provider configuration from environment config
	providerConfig := buildProviderConfig(envConfig)

	// Create provider instance
	prov, err := pm.factory.CreateProvider(providerType, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create %s provider: %w", providerType, err)
	}

	if opts.DryRun {
		fmt.Printf("DRY-RUN: Would create %s cluster '%s' in region '%s'\n",
			envConfig.ResolvedProvider, envConfig.Name, envConfig.ResolvedRegion)
		return nil
	}

	// Build cluster specification based on provider and environment
	spec, err := buildClusterSpec(envConfig)
	if err != nil {
		return fmt.Errorf("failed to build cluster specification: %w", err)
	}

	// Authenticate with the provider
	if err := prov.Authenticate(ctx, buildCredentials(envConfig)); err != nil {
		return fmt.Errorf("authentication failed for %s provider: %w", providerType, err)
	}

	// Validate permissions
	if err := prov.ValidatePermissions(ctx); err != nil {
		return fmt.Errorf("permission validation failed for %s provider: %w", providerType, err)
	}

	// Create the cluster
	logger.Infof("Creating cluster '%s' using %s provider in region %s", envConfig.Name, providerType, envConfig.ResolvedRegion)

	cluster, err := prov.CreateCluster(ctx, spec)
	if err != nil {
		return fmt.Errorf("failed to create %s cluster: %w", providerType, err)
	}

	logger.Infof("Cluster created successfully - ID: %s, Status: %s", cluster.ID, cluster.Status)

	// Apply platform stack manifests for all providers
	if err := applyPlatformStack(envConfig); err != nil {
		return fmt.Errorf("failed to apply platform stack: %w", err)
	}

	return nil
}

// buildProviderConfig creates provider-specific configuration from environment config
func buildProviderConfig(envConfig *config.ResolvedEnvironmentConfig) map[string]interface{} {
	providerConfig := make(map[string]interface{})

	// Add region
	if envConfig.ResolvedRegion != "" {
		providerConfig["region"] = envConfig.ResolvedRegion
	}

	// Add cluster-specific configuration
	for _, kv := range envConfig.ResolvedClusterConfig {
		providerConfig[kv.Key] = kv.Value
	}

	return providerConfig
}

// buildClusterSpec creates a cluster specification based on environment configuration
func buildClusterSpec(envConfig *config.ResolvedEnvironmentConfig) (*ptypes.ClusterSpec, error) {
	spec := &ptypes.ClusterSpec{
		Provider: envConfig.ResolvedProvider,
		Region:   envConfig.ResolvedRegion,
		ObjectMeta: ptypes.ObjectMeta{
			Name: envConfig.Name,
		},
	}

	// Set defaults based on environment type
	isProduction := envConfig.ResolvedType == config.EnvironmentTypeProduction

	// Configure control plane
	controlPlaneReplicas := 1
	if isProduction {
		controlPlaneReplicas = 3 // HA for production
	}
	spec.ControlPlane = ptypes.ControlPlaneSpec{
		Replicas: controlPlaneReplicas,
	}

	// Configure node groups
	workerReplicas := 0 // Single-node cluster for local development
	if isProduction {
		workerReplicas = 3 // More workers for production
	}
	spec.NodeGroups = []ptypes.NodeGroupSpec{
		{
			Name:     "workers",
			Replicas: workerReplicas,
		},
	}

	// Configure networking
	spec.Networking = ptypes.NetworkingSpec{
		CNI:         "cilium",
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
	}

	// Configure domain management
	spec.Domain = buildDomainConfig(envConfig)

	// Apply cluster-specific configuration
	for _, kv := range envConfig.ResolvedClusterConfig {
		switch kv.Key {
		case "kubeVersion", "version":
			spec.Version = kv.Value
		case "controlPlaneReplicas":
			if replicas := parseIntOrDefault(kv.Value, controlPlaneReplicas); replicas > 0 {
				spec.ControlPlane.Replicas = replicas
			}
		case "workerReplicas":
			if replicas := parseIntOrDefault(kv.Value, workerReplicas); replicas > 0 {
				spec.NodeGroups[0].Replicas = replicas
			}
		case "nodeInstanceType", "instanceType":
			spec.NodeGroups[0].InstanceType = kv.Value
		case "diskSize":
			// Note: DiskSize not available in current NodeGroupSpec
			// This could be added to the spec if needed in the future
		}
	}

	return spec, nil
}

// buildDomainConfig creates domain configuration based on environment and provider
func buildDomainConfig(envConfig *config.ResolvedEnvironmentConfig) *ptypes.DomainConfig {
	// Get domain configuration from global settings or use defaults
	var baseDomain string
	var email string

	if envConfig.GlobalSettings != nil {
		if envConfig.GlobalSettings.DefaultHost != "" {
			baseDomain = envConfig.GlobalSettings.DefaultHost
		}
		if envConfig.GlobalSettings.Email != "" {
			email = envConfig.GlobalSettings.Email
		}
	}

	// Use Kind-specific defaults for local development
	if envConfig.ResolvedProvider == "kind" || envConfig.ResolvedProvider == globals.CloudProviderKind {
		if baseDomain == "" || baseDomain == "platform.adhar.io" {
			baseDomain = globals.DefaultHostName // "adhar.localtest.me"
		}
		if email == "" {
			email = "admin@" + baseDomain
		}
	}

	// Default email if still not set
	if email == "" {
		email = "admin@adhar.io"
	}

	// Default domain if still not set
	if baseDomain == "" {
		baseDomain = "platform.adhar.io"
	}

	domainConfig := &ptypes.DomainConfig{
		Name:            "default",
		BaseDomain:      baseDomain,
		CertificateType: "selfsigned", // Use self-signed certs for local development
		TLS: ptypes.TLSConfig{
			Enabled:     true,
			Email:       email,
			Environment: "staging", // Use staging for local development
		},
		DNS: ptypes.DNSConfig{
			Provider: "coredns", // Use CoreDNS for local resolution
		},
		Ingress: ptypes.IngressConfig{
			Provider: "nginx", // Use NGINX ingress controller
		},
	}

	return domainConfig
}

// buildCredentials creates credentials from environment configuration
func buildCredentials(envConfig *config.ResolvedEnvironmentConfig) *ptypes.Credentials {
	// For now, credentials will be loaded from environment variables or cloud provider defaults
	// In the future, this could be enhanced to read from config file or secret stores
	return &ptypes.Credentials{
		// Provider-specific credentials will be handled by each provider implementation
	}
}

// applyPlatformStack applies the core platform components in the correct order with progress tracking
func applyPlatformStack(envConfig *config.ResolvedEnvironmentConfig) error {
	// Set HA mode for manifest selection
	enableHA := false
	if envConfig != nil && envConfig.GlobalSettings != nil {
		enableHA = envConfig.GlobalSettings.EnableHAMode
	}
	setPlatformHAMode(enableHA)

	logger.Debugf("Platform HA mode: %t", enableHA)

	// Create progress tracker with detailed step descriptions
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

	progress := helpers.NewProgressTrackerWithDetails("Setting up Adhar Platform", stepNames, stepDescriptions)
	defer func() {
		// Clear the progress display
		fmt.Print("\r\033[K")
	}()

	// Step 1: Install platform CRDs
	progress.StartStep(0, "")
	if err := applyManifests("platform/controllers/resources/"); err != nil {
		progress.FailStep(0, err)
		return fmt.Errorf("failed to install platform CRDs: %w", err)
	}
	progress.CompleteStep(0)
	time.Sleep(800 * time.Millisecond) // Brief pause for visual feedback

	// Step 2: Create required namespaces
	progress.StartStep(1, "")
	if err := createNamespaces(); err != nil {
		progress.FailStep(1, err)
		return fmt.Errorf("failed to create namespaces: %w", err)
	}
	progress.CompleteStep(1)
	time.Sleep(800 * time.Millisecond)

	// Step 3: Install ArgoCD from platform resources
	progress.StartStep(2, "")
	if err := applyPlatformManifest("argocd"); err != nil {
		progress.FailStep(2, err)
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}
	progress.CompleteStep(2)
	time.Sleep(800 * time.Millisecond)

	// Step 4: Install Gitea from platform resources
	progress.StartStep(3, "")
	if err := applyPlatformManifest("gitea"); err != nil {
		progress.FailStep(3, err)
		return fmt.Errorf("failed to install Gitea: %w", err)
	}
	progress.CompleteStep(3)
	time.Sleep(800 * time.Millisecond)

	// Step 5: Install Crossplane from platform resources
	progress.StartStep(4, "")
	if err := applyPlatformManifest("crossplane"); err != nil {
		progress.FailStep(4, err)
		return fmt.Errorf("failed to install Crossplane: %w", err)
	}
	progress.CompleteStep(4)
	time.Sleep(800 * time.Millisecond)

	// Step 6: Install Nginx Ingress from platform resources
	progress.StartStep(5, "")
	if err := applyPlatformManifest("nginx"); err != nil {
		progress.FailStep(5, err)
		return fmt.Errorf("failed to install Nginx Ingress: %w", err)
	}
	progress.CompleteStep(5)
	time.Sleep(800 * time.Millisecond)

	// Step 7: Install Ingress Resources for platform components
	progress.StartStep(6, "")
	if err := applyIngressManifests(); err != nil {
		progress.FailStep(6, err)
		return fmt.Errorf("failed to install ingress resources: %w", err)
	}
	progress.CompleteStep(6)
	time.Sleep(800 * time.Millisecond)

	// Step 8: Label core secrets for CLI discovery
	progress.StartStep(7, "")
	if err := labelCoreSecrets(); err != nil {
		// Don't fail completely, just warn and skip
		progress.SkipStep(7, "Failed to label secrets, continuing anyway")
		logger.Warnf("Secret labeling failed, continuing anyway: %v", err)
	} else {
		progress.CompleteStep(7)
	}
	time.Sleep(800 * time.Millisecond)

	// Step 9: Wait for ArgoCD to be ready
	progress.StartStep(8, "")
	if err := waitForArgoCD(); err != nil {
		progress.FailStep(8, err)
		return fmt.Errorf("failed to wait for ArgoCD: %w", err)
	}
	progress.CompleteStep(8)
	time.Sleep(800 * time.Millisecond)

	// Step 10: Apply platform stack ApplicationSets
	progress.StartStep(9, "")
	if err := applyPlatformApplicationSets(); err != nil {
		progress.FailStep(9, err)
		return fmt.Errorf("failed to apply platform ApplicationSets: %w", err)
	}
	progress.CompleteStep(9)
	time.Sleep(800 * time.Millisecond)

	// Step 11: Setup GitOps repositories
	progress.StartStep(10, "")
	if err := setupGitOpsRepositories(); err != nil {
		progress.FailStep(10, err)
		return fmt.Errorf("failed to setup GitOps repositories: %w", err)
	}
	progress.CompleteStep(10)

	// Complete the progress tracker
	progress.Complete()

	return nil
}

// setupGitOpsRepositories creates and populates GitOps repositories in Gitea for ArgoCD
func setupGitOpsRepositories() error {
	logger.Debugf("Setting up GitOps repositories in Gitea...")

	// Get Gitea admin credentials
	giteaUsername, giteaPassword := getGiteaAdminCredentials()
	if giteaUsername == "" || giteaPassword == "" {
		return fmt.Errorf("failed to get Gitea admin credentials")
	}

	giteaBaseURL := "https://adhar.localtest.me/gitea"

	// Wait for Gitea to be fully ready for API calls
	if err := waitForGiteaReady(giteaBaseURL, giteaUsername, giteaPassword); err != nil {
		return fmt.Errorf("failed to wait for Gitea readiness: %w", err)
	}

	// Configure Gitea for larger repository uploads via kubectl patch
	if err := configureGiteaForLargeUploads(); err != nil {
		logger.Warnf("Failed to configure Gitea for large uploads: %v", err)
		// Continue anyway - this is not critical
	}

	// Define repositories to create
	repositories := map[string]string{
		"environments": "Platform environment configurations",
		"packages":     "Platform component packages and charts",
	}

	// Create repositories in Gitea with retry logic
	for repoName, description := range repositories {
		logger.Debugf("Creating Gitea repository: %s", repoName)
		if err := createGiteaRepositoryWithRetry(giteaBaseURL, giteaUsername, giteaPassword, repoName, description, 5); err != nil {
			return fmt.Errorf("failed to create repository %s: %w", repoName, err)
		}
	}

	// Populate repositories with content
	if err := populateGiteaRepositories(giteaBaseURL, giteaUsername, giteaPassword); err != nil {
		return fmt.Errorf("failed to populate repositories: %w", err)
	}

	// Configure ArgoCD to use Gitea repositories
	if err := configureArgoCDRepositories(giteaBaseURL, giteaUsername, giteaPassword); err != nil {
		return fmt.Errorf("failed to configure ArgoCD repositories: %w", err)
	}

	fmt.Printf("\n\n%s\n", boldStyle.Render("✅ GitOps Repositories Configured!"))
	fmt.Printf("Platform repositories created in Gitea:\n")
	fmt.Printf("  • Environments: %s/gitea_admin/environments\n", giteaBaseURL)
	fmt.Printf("  • Packages: %s/gitea_admin/packages\n", giteaBaseURL)
	fmt.Printf("ArgoCD is now configured to use these repositories for GitOps workflows.\n\n")

	return nil
}

// applyPlatformApplicationSets applies the platform stack ApplicationSets for ArgoCD management
func applyPlatformApplicationSets() error {
	logger.Debugf("Applying platform ApplicationSets...")

	// Define the ApplicationSet files to apply
	applicationSets := []string{
		"platform/stack/adhar-appset-charts.yaml",
		"platform/stack/adhar-appset-manifests.yaml",
		"platform/stack/adhar-templates.yaml",
	}

	for _, appSetPath := range applicationSets {
		logger.Debugf("Applying ApplicationSet: %s", appSetPath)

		// Check if file exists
		if !fileExists(appSetPath) {
			logger.Warnf("ApplicationSet file not found: %s, skipping", appSetPath)
			continue
		}

		// Apply the ApplicationSet
		if err := applyManifests(appSetPath); err != nil {
			return fmt.Errorf("failed to apply ApplicationSet %s: %w", appSetPath, err)
		}

		logger.Debugf("Successfully applied ApplicationSet: %s", appSetPath)
	}

	fmt.Printf("\n\n%s\n", boldStyle.Render("✅ Platform ApplicationSets Applied!"))
	fmt.Printf("ArgoCD is now managing the platform stack. You can monitor application deployments at:\n")
	fmt.Printf("  • ArgoCD UI: https://adhar.localtest.me/argocd/\n")
	fmt.Printf("  • Use: %s to view ArgoCD credentials\n\n", getCodeStyle.Render("adhar get secrets -p argocd"))

	return nil
}

// labelCoreSecrets adds proper labels to core secrets for CLI discovery
func labelCoreSecrets() error {
	// Core secrets mapping with their namespaces
	coreSecrets := map[string]map[string]string{
		"argocd-initial-admin-secret": {
			"namespace":    "adhar-system",
			"package-name": "argocd",
			"package-type": "core",
		},
		"gitea": {
			"namespace":    "adhar-system",
			"package-name": "gitea",
			"package-type": "core",
		},
	}

	for secretName, config := range coreSecrets {
		// Check if secret exists before labeling
		checkCmd := exec.Command("kubectl", "get", "secret", secretName, "-n", config["namespace"], "-o", "name")
		if err := checkCmd.Run(); err != nil {
			// Secret doesn't exist, skip it
			logger.Debugf("Secret %s/%s not found, skipping labeling", config["namespace"], secretName)
			continue
		}

		// Apply labels to the secret
		labels := map[string]string{
			"adhar.io/cli-secret":   "true",
			"adhar.io/package-name": config["package-name"],
			"adhar.io/package-type": config["package-type"],
		}

		var labelArgs []string
		labelArgs = append(labelArgs, "label", "secret", secretName, "-n", config["namespace"], "--overwrite")
		for key, value := range labels {
			labelArgs = append(labelArgs, fmt.Sprintf("%s=%s", key, value))
		}

		cmd := exec.Command("kubectl", labelArgs...)
		if err := cmd.Run(); err != nil {
			logger.Warnf("Failed to label secret %s/%s: %v", config["namespace"], secretName, err)
			continue
		}

		logger.Debugf("Successfully labeled secret %s/%s", config["namespace"], secretName)
	}

	return nil
}

// parseIntOrDefault parses a string to int, returning default if parsing fails
func parseIntOrDefault(s string, defaultValue int) int {
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	return defaultValue
}

func applyManifests(path string) error {
	cmd := exec.Command("kubectl", "apply", "-f", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply failed for %s: %v\n%s", path, err, string(out))
	}
	return nil
}

// applyManifestsIfNotEmpty applies manifests only if the file is not empty
func applyManifestsIfNotEmpty(path string) error {
	// Check if file exists and is not empty
	fileInfo, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to check file %s: %w", path, err)
	}

	if fileInfo.Size() == 0 {
		logger.Debugf("Skipping empty manifest file: %s", path)
		return nil
	}

	return applyManifests(path)
}

// applyPlatformManifest intelligently chooses between regular and HA manifests based on config
func applyPlatformManifest(component string) error {
	basePath := fmt.Sprintf("platform/controllers/adharplatform/resources/%s", component)

	// Determine if HA mode is enabled
	useHAMode := getPlatformHAMode()

	var manifestPath string

	if useHAMode {
		// Try HA manifest first
		haPath := fmt.Sprintf("%s/install-ha.yaml", basePath)
		if fileExists(haPath) && !isFileEmpty(haPath) {
			manifestPath = haPath
			logger.Debugf("Using HA manifest for %s: %s", component, manifestPath)
		} else {
			// Fall back to regular manifest
			manifestPath = fmt.Sprintf("%s/install.yaml", basePath)
			logger.Debugf("HA manifest not available for %s, using regular manifest: %s", component, manifestPath)
		}
	} else {
		// Use regular manifest for non-HA mode
		manifestPath = fmt.Sprintf("%s/install.yaml", basePath)
		logger.Debugf("Using non-HA manifest for %s: %s", component, manifestPath)
	}

	// Apply the chosen manifest
	return applyManifestsIfNotEmpty(manifestPath)
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isFileEmpty checks if a file is empty
func isFileEmpty(path string) bool {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return true
	}
	return fileInfo.Size() == 0
}

// Global variable to store HA mode setting (will be set by applyPlatformStack)
var globalHAMode bool

// setPlatformHAMode sets the global HA mode for manifest selection
func setPlatformHAMode(enableHA bool) {
	globalHAMode = enableHA
}

// getPlatformHAMode returns the current HA mode setting
func getPlatformHAMode() bool {
	return globalHAMode
}

// applyIngressManifests applies ingress manifests for platform components
func applyIngressManifests() error {
	// Wait for nginx admission webhook to be ready
	if err := waitForNginxAdmissionWebhook(); err != nil {
		return fmt.Errorf("failed to wait for nginx admission webhook: %w", err)
	}

	// Apply ArgoCD ingress with retry
	if err := applyManifestsWithRetry("platform/controllers/adharplatform/resources/ingress/argocd-ingress.yaml", 3); err != nil {
		return fmt.Errorf("failed to apply ArgoCD ingress: %w", err)
	}
	logger.Debugf("Applied ArgoCD ingress")

	// Apply Gitea service (needed for proper ClusterIP)
	if err := applyManifestsIfNotEmpty("platform/controllers/adharplatform/resources/ingress/gitea-service.yaml"); err != nil {
		return fmt.Errorf("failed to apply Gitea service: %w", err)
	}
	logger.Debugf("Applied Gitea service")

	// Apply Gitea ingress with retry
	if err := applyManifestsWithRetry("platform/controllers/adharplatform/resources/ingress/gitea-ingress.yaml", 3); err != nil {
		return fmt.Errorf("failed to apply Gitea ingress: %w", err)
	}
	logger.Debugf("Applied Gitea ingress")

	// TODO: Add other component ingress manifests here when they're created
	// Example: Crossplane UI, other services

	return nil
}

// waitForNginxAdmissionWebhook waits for the nginx admission webhook to be ready
func waitForNginxAdmissionWebhook() error {
	logger.Debugf("Waiting for nginx admission webhook to be ready...")

	timeout := 120 * time.Second
	interval := 5 * time.Second
	start := time.Now()

	for time.Since(start) < timeout {
		// Check if the nginx controller pod is ready
		cmd := exec.Command("kubectl", "get", "pods", "-n", "adhar-system",
			"-l", "app.kubernetes.io/name=ingress-nginx,app.kubernetes.io/component=controller",
			"-o", "jsonpath={.items[0].status.containerStatuses[0].ready}")
		if output, err := cmd.Output(); err == nil && strings.TrimSpace(string(output)) == "true" {
			logger.Debugf("Nginx controller pod is ready")

			// Also check if the admission webhook jobs completed
			jobCmd := exec.Command("kubectl", "get", "jobs", "-n", "adhar-system",
				"-l", "app.kubernetes.io/name=ingress-nginx",
				"-o", "jsonpath={.items[*].status.conditions[?(@.type=='Complete')].status}")
			if jobOutput, jobErr := jobCmd.Output(); jobErr == nil {
				completions := strings.Fields(string(jobOutput))
				allComplete := true
				for _, completion := range completions {
					if completion != "True" {
						allComplete = false
						break
					}
				}
				if allComplete && len(completions) > 0 {
					logger.Debugf("Admission webhook jobs completed, giving extra time for webhook to be ready...")
					// Give extra time for the webhook endpoint to be fully ready
					time.Sleep(10 * time.Second)
					return nil
				}
			}
		}

		logger.Debugf("Nginx admission webhook not ready yet, waiting...")
		time.Sleep(interval)
	}

	return fmt.Errorf("nginx admission webhook did not become ready within %v", timeout)
}

// applyManifestsWithRetry applies manifests with retry logic for webhook readiness
func applyManifestsWithRetry(manifestPath string, maxRetries int) error {
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := applyManifestsIfNotEmpty(manifestPath)
		if err == nil {
			return nil
		}

		// Check if it's a webhook connection error
		if strings.Contains(err.Error(), "connection refused") && strings.Contains(err.Error(), "admission") {
			logger.Debugf("Admission webhook not ready (attempt %d/%d), waiting 15 seconds...", attempt, maxRetries)
			if attempt < maxRetries {
				time.Sleep(15 * time.Second)
				continue
			}
		}

		// For other errors or final attempt, return the error
		return err
	}

	return fmt.Errorf("failed to apply manifests after %d attempts", maxRetries)
}

// createNamespaces creates the required namespaces for the platform
func createNamespaces() error {
	namespaces := []string{"adhar-system", "argocd"}

	for _, ns := range namespaces {
		cmd := exec.Command("kubectl", "create", "namespace", ns, "--dry-run=client", "-o", "yaml")
		createCmd := exec.Command("kubectl", "apply", "-f", "-")

		// Pipe the output of the first command to the second
		createCmd.Stdin, _ = cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to generate namespace %s: %w", ns, err)
		}

		if err := createCmd.Run(); err != nil {
			// Ignore errors if namespace already exists
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("failed to create namespace %s: %w", ns, err)
			}
		}

		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("failed to wait for namespace generation %s: %w", ns, err)
		}
	}

	return nil
}

// waitForArgoCD waits for ArgoCD components to be ready with timeout handling
func waitForArgoCD() error {
	// Start a simple progress indicator for the wait
	done := make(chan error, 1)

	go func() {
		cmd := exec.Command("kubectl", "wait",
			"--for=condition=ready", "pod",
			"--selector=app.kubernetes.io/name=argocd-applicationset-controller",
			"-n", "adhar-system",
			"--timeout=180s")

		out, err := cmd.CombinedOutput()
		if err != nil {
			done <- fmt.Errorf("ArgoCD not ready: %v\nOutput: %s", err, string(out))
		} else {
			done <- nil
		}
	}()

	// Show a simple spinner while waiting
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(180 * time.Second)

	for {
		select {
		case err := <-done:
			return err
		case <-timeout:
			return fmt.Errorf("timeout waiting for ArgoCD to be ready")
		case <-ticker.C:
			// Just continue waiting, the progress tracker will show the spinner
		}
	}
}

// waitForGiteaReady waits for Gitea to be ready to accept API calls
func waitForGiteaReady(giteaBaseURL, username, password string) error {
	logger.Debugf("Waiting for Gitea API to be ready...")

	timeout := 300 * time.Second // 5 minutes timeout
	interval := 10 * time.Second
	start := time.Now()

	// Create HTTP client with timeout and TLS skip
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 15 * time.Second,
	}

	for time.Since(start) < timeout {
		// Try to ping Gitea API
		apiURL := fmt.Sprintf("%s/api/v1/user", giteaBaseURL)
		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			logger.Debugf("Failed to create request for Gitea readiness check: %v", err)
			time.Sleep(interval)
			continue
		}

		req.SetBasicAuth(username, password)
		resp, err := client.Do(req)
		if err != nil {
			logger.Debugf("Gitea API not ready yet (connection error): %v", err)
			time.Sleep(interval)
			continue
		}
		resp.Body.Close()

		// Check if we get a successful response (200) or auth-related response (401, etc.)
		// Both indicate the API is working, just auth might be wrong
		if resp.StatusCode == 200 || resp.StatusCode == 401 || resp.StatusCode == 403 {
			logger.Debugf("Gitea API is ready (status: %d)", resp.StatusCode)
			return nil
		}

		// 503 means service unavailable, keep waiting
		if resp.StatusCode == 503 {
			logger.Debugf("Gitea API still unavailable (503), waiting...")
			time.Sleep(interval)
			continue
		}

		// Other status codes might indicate the service is up but having issues
		logger.Debugf("Gitea API responded with status %d, considering ready", resp.StatusCode)
		return nil
	}

	return fmt.Errorf("Gitea API did not become ready within %v", timeout)
}

// createGiteaRepositoryWithRetry creates a repository with retry logic
func createGiteaRepositoryWithRetry(giteaBaseURL, username, password, repoName, description string, maxRetries int) error {
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := createGiteaRepository(giteaBaseURL, username, password, repoName, description)
		if err == nil {
			return nil
		}

		// Check if it's a 503 error (service unavailable)
		if strings.Contains(err.Error(), "status: 503") {
			if attempt < maxRetries {
				waitTime := time.Duration(attempt*5) * time.Second
				logger.Debugf("Gitea API returned 503, retrying in %v (attempt %d/%d)", waitTime, attempt, maxRetries)
				time.Sleep(waitTime)
				continue
			}
		}

		// For other errors, don't retry
		return err
	}

	return fmt.Errorf("failed to create repository after %d attempts", maxRetries)
}

// createGiteaRepository creates a new repository in Gitea using the API
func createGiteaRepository(giteaBaseURL, username, password, repoName, description string) error {
	// Gitea API endpoint for creating repositories
	apiURL := fmt.Sprintf("%s/api/v1/user/repos", giteaBaseURL)

	// Repository payload
	repoData := map[string]interface{}{
		"name":           repoName,
		"description":    description,
		"private":        false,
		"auto_init":      true,
		"default_branch": "main", // Explicitly set default branch to main
	}

	jsonData, err := json.Marshal(repoData)
	if err != nil {
		return fmt.Errorf("failed to marshal repo data: %w", err)
	}

	// Create HTTP client that skips TLS verification for self-signed certs
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 409 {
		// Repository already exists, this is fine
		logger.Debugf("Repository %s already exists", repoName)
		return nil
	}

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create repository, status: %d, body: %s", resp.StatusCode, string(body))
	}

	logger.Debugf("Successfully created repository: %s", repoName)
	return nil
}

// populateGiteaRepositories populates the created repositories with platform content
func populateGiteaRepositories(giteaBaseURL, username, password string) error {
	// For now, we'll use git commands to populate the repositories
	// This requires git to be available on the system

	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git command not found. Please install git to populate repositories")
	}

	// Create temporary directory for git operations
	tmpDir, err := os.MkdirTemp("", "adhar-gitops-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Populate environments repository
	if err := populateEnvironmentsRepo(tmpDir, giteaBaseURL, username, password); err != nil {
		return fmt.Errorf("failed to populate environments repository: %w", err)
	}

	// Populate packages repository
	if err := populatePackagesRepo(tmpDir, giteaBaseURL, username, password); err != nil {
		return fmt.Errorf("failed to populate packages repository: %w", err)
	}

	return nil
}

// populateEnvironmentsRepo populates the environments repository
func populateEnvironmentsRepo(tmpDir, giteaBaseURL, username, password string) error {
	envRepoDir := filepath.Join(tmpDir, "environments")

	// Use credentials in URL for HTTPS authentication
	authenticatedURL := buildAuthenticatedGitURL(giteaBaseURL, username, password, "environments")

	// Clone the repository
	logger.Debugf("Cloning environments repository from: %s", authenticatedURL)
	cmd := exec.Command("git", "clone", authenticatedURL, envRepoDir)

	// Use clean git environment without credential managers
	cmd.Env = createCleanGitEnv()

	// Capture output for debugging
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Debugf("Git clone failed. Stdout: %s, Stderr: %s", stdout.String(), stderr.String())
		return fmt.Errorf("failed to clone environments repository: %w. Stderr: %s", err, stderr.String())
	}
	logger.Debugf("Successfully cloned environments repository")

	// Copy environment configurations from platform/stack/environments/
	srcDir := "platform/stack/environments"
	if err := copyDir(srcDir, envRepoDir); err != nil {
		return fmt.Errorf("failed to copy environment configs: %w", err)
	}

	// Git add, commit, and push
	return commitAndPushRepo(envRepoDir, "Add platform environment configurations", username, password)
}

// populatePackagesRepo populates the packages repository
func populatePackagesRepo(tmpDir, giteaBaseURL, username, password string) error {
	pkgRepoDir := filepath.Join(tmpDir, "packages")

	// Use credentials in URL for HTTPS authentication
	authenticatedURL := buildAuthenticatedGitURL(giteaBaseURL, username, password, "packages")

	// Clone the repository
	logger.Debugf("Cloning packages repository from: %s", authenticatedURL)
	cmd := exec.Command("git", "clone", authenticatedURL, pkgRepoDir)

	// Use clean git environment without credential managers
	cmd.Env = createCleanGitEnv()

	// Capture output for debugging
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Debugf("Git clone failed. Stdout: %s, Stderr: %s", stdout.String(), stderr.String())
		return fmt.Errorf("failed to clone packages repository: %w. Stderr: %s", err, stderr.String())
	}
	logger.Debugf("Successfully cloned packages repository")

	// Copy packages from platform/stack/packages/
	srcDir := "platform/stack/packages"
	if err := copyDir(srcDir, pkgRepoDir); err != nil {
		return fmt.Errorf("failed to copy package configs: %w", err)
	}

	// Git add, commit, and push
	return commitAndPushRepo(pkgRepoDir, "Add platform component packages", username, password)
}

// copyDir recursively copies a directory
func copyDir(src, dst string) error {
	return exec.Command("cp", "-r", src+"/.", dst+"/").Run()
}

// createCleanGitEnv creates a clean Git environment without credential managers
func createCleanGitEnv() []string {
	return []string{
		"HOME=" + os.Getenv("HOME"),     // Keep HOME for basic functionality
		"PATH=" + os.Getenv("PATH"),     // Keep PATH for git binary
		"TMPDIR=" + os.Getenv("TMPDIR"), // Keep TMPDIR for temp files
		"GIT_TERMINAL_PROMPT=0",         // Disable interactive prompts
		"GIT_CONFIG_GLOBAL=/dev/null",   // Disable global git config
		"GIT_CONFIG_SYSTEM=/dev/null",   // Disable system git config
		"GIT_CREDENTIAL_HELPER=",        // Disable all credential helpers
		"GIT_ASKPASS=",                  // Disable askpass
		"SSH_ASKPASS=",                  // Disable SSH askpass
		"GIT_CONFIG_NOSYSTEM=1",         // Skip system config
		"GCM_CREDENTIAL_STORE=",         // Disable Git Credential Manager store
		"GCM_CREDENTIAL_CACHE=",         // Disable Git Credential Manager cache
	}
}

// configureGiteaForLargeUploads configures Gitea to handle larger repository uploads
func configureGiteaForLargeUploads() error {
	// Patch Gitea deployment to increase client_max_body_size and related configs
	patchJSON := `{
		"spec": {
			"template": {
				"metadata": {
					"annotations": {
						"kubectl.kubernetes.io/restartedAt": "` + time.Now().Format(time.RFC3339) + `"
					}
				},
				"spec": {
					"containers": [
						{
							"name": "gitea",
							"env": [
								{
									"name": "GITEA__server__LFS_MAX_FILE_SIZE",
									"value": "1024"
								},
								{
									"name": "GITEA__repository__MAX_CREATION_LIMIT",
									"value": "100"
								},
								{
									"name": "GITEA__repository__UPLOAD_MAX_SIZE",
									"value": "1024"
								}
							]
						}
					]
				}
			}
		}
	}`

	cmd := exec.Command("kubectl", "patch", "deployment", "gitea", "-n", "adhar-system",
		"--type", "strategic", "--patch", patchJSON)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to patch Gitea deployment: %w", err)
	}

	// Also configure nginx to handle larger uploads
	nginxPatchJSON := `{
		"data": {
			"client-max-body-size": "1024m",
			"client-body-buffer-size": "16m",
			"proxy-body-size": "1024m",
			"proxy-buffer-size": "16k",
			"proxy-buffers-number": "8"
		}
	}`

	nginxCmd := exec.Command("kubectl", "patch", "configmap", "ingress-nginx-controller",
		"-n", "adhar-system", "--type", "merge", "--patch", nginxPatchJSON)
	nginxCmd.Run() // Ignore errors for nginx config

	logger.Debugf("Configured Gitea and Nginx for large uploads")
	return nil
}

// addFilesInBatches adds files to git in smaller batches to avoid HTTP 413 errors
func addFilesInBatches(repoDir string, gitEnv []string) error {
	// Get list of all files to add
	listCmd := exec.Command("find", ".", "-type", "f", "-not", "-path", "./.git/*")
	listCmd.Dir = repoDir
	output, err := listCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list files: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(files) == 0 || (len(files) == 1 && files[0] == "") {
		return nil // No files to add
	}

	// Sort files by size (add smaller files first)
	type fileInfo struct {
		path string
		size int64
	}

	var fileInfos []fileInfo
	for _, file := range files {
		if file == "" {
			continue
		}
		fullPath := filepath.Join(repoDir, file)
		if stat, err := os.Stat(fullPath); err == nil {
			fileInfos = append(fileInfos, fileInfo{path: file, size: stat.Size()})
		}
	}

	// Sort by size (smallest first)
	for i := 0; i < len(fileInfos)-1; i++ {
		for j := i + 1; j < len(fileInfos); j++ {
			if fileInfos[i].size > fileInfos[j].size {
				fileInfos[i], fileInfos[j] = fileInfos[j], fileInfos[i]
			}
		}
	}

	// Add files in batches
	batchSize := 50 // Add 50 files at a time
	for i := 0; i < len(fileInfos); i += batchSize {
		end := i + batchSize
		if end > len(fileInfos) {
			end = len(fileInfos)
		}

		batch := make([]string, end-i)
		for j := i; j < end; j++ {
			batch[j-i] = fileInfos[j].path
		}

		// Add this batch of files
		addArgs := append([]string{"add"}, batch...)
		addCmd := exec.Command("git", addArgs...)
		addCmd.Dir = repoDir
		addCmd.Env = gitEnv

		if err := addCmd.Run(); err != nil {
			// If batch fails, try adding files one by one
			for _, file := range batch {
				singleAddCmd := exec.Command("git", "add", file)
				singleAddCmd.Dir = repoDir
				singleAddCmd.Env = gitEnv
				singleAddCmd.Run() // Ignore individual file errors
			}
		}
	}

	return nil
}

// buildAuthenticatedGitURL builds a Git URL with embedded credentials
func buildAuthenticatedGitURL(giteaBaseURL, username, password, repoName string) string {
	// Parse the base URL properly
	baseURL, err := url.Parse(giteaBaseURL)
	if err != nil {
		// Fallback to simple concatenation if parsing fails
		return fmt.Sprintf("%s/%s/%s.git", giteaBaseURL, username, repoName)
	}

	// Create a new URL with embedded credentials
	repoURL := &url.URL{
		Scheme: baseURL.Scheme,
		User:   url.UserPassword(username, password), // This properly encodes special characters
		Host:   baseURL.Host,
		Path:   fmt.Sprintf("%s/%s/%s.git", baseURL.Path, username, repoName),
	}

	return repoURL.String()
}

// updateRemoteURLWithCredentials updates the remote URL to include credentials
func updateRemoteURLWithCredentials(repoDir, username, password string) error {
	// Use clean git environment
	gitEnv := createCleanGitEnv()

	// Get current remote URL
	getURLCmd := exec.Command("git", "remote", "get-url", "origin")
	getURLCmd.Dir = repoDir
	getURLCmd.Env = gitEnv
	currentURL, err := getURLCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get remote URL: %w", err)
	}

	urlStr := strings.TrimSpace(string(currentURL))

	// Parse the URL to extract components
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("failed to parse remote URL: %w", err)
	}

	// Add credentials to the URL using proper encoding
	parsedURL.User = url.UserPassword(username, password) // url.UserPassword handles encoding properly

	// Update the remote URL
	setURLCmd := exec.Command("git", "remote", "set-url", "origin", parsedURL.String())
	setURLCmd.Dir = repoDir
	setURLCmd.Env = gitEnv

	if err := setURLCmd.Run(); err != nil {
		return fmt.Errorf("failed to set remote URL: %w", err)
	}

	return nil
}

// commitAndPushRepo commits and pushes changes to a git repository
func commitAndPushRepo(repoDir, commitMessage, username, password string) error {
	// Disable credential manager and configure git properly
	gitEnv := createCleanGitEnv()

	// Set git config and perform operations
	configCmds := [][]string{
		{"git", "config", "user.name", "Adhar Platform"},
		{"git", "config", "user.email", "platform@adhar.io"},
		{"git", "config", "credential.helper", ""},           // Disable credential helper explicitly
		{"git", "config", "credential.useHttpPath", "false"}, // Disable HTTP path credential matching
		{"git", "config", "credential.modalPrompt", "false"}, // Disable modal prompts
		{"git", "config", "core.askPass", ""},                // Disable askpass
	}

	// Execute config commands
	for _, cmdArgs := range configCmds {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		cmd.Dir = repoDir
		cmd.Env = gitEnv
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed git command %v: %w", cmdArgs, err)
		}
	}

	// Add files in smaller batches to avoid HTTP 413 errors
	if err := addFilesInBatches(repoDir, gitEnv); err != nil {
		return fmt.Errorf("failed to add files: %w", err)
	}

	// Check if there are changes to commit
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoDir
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	// Only commit and push if there are changes
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		// Commit changes
		commitCmd := exec.Command("git", "commit", "-m", commitMessage)
		commitCmd.Dir = repoDir
		commitCmd.Env = gitEnv
		if err := commitCmd.Run(); err != nil {
			return fmt.Errorf("failed to commit changes: %w", err)
		}

		// Update the remote URL to include credentials for push
		if err := updateRemoteURLWithCredentials(repoDir, username, password); err != nil {
			return fmt.Errorf("failed to update remote URL: %w", err)
		}

		// Get the current branch name
		branchCmd := exec.Command("git", "branch", "--show-current")
		branchCmd.Dir = repoDir
		branchOutput, err := branchCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get current branch: %w", err)
		}
		currentBranch := strings.TrimSpace(string(branchOutput))
		if currentBranch == "" {
			currentBranch = "main" // fallback to main
		}

		logger.Debugf("Pushing to branch: %s", currentBranch)

		// Configure git for larger pushes to handle HTTP 413 errors
		configLargePushCmds := [][]string{
			{"git", "config", "http.postBuffer", "524288000"},   // 500MB buffer
			{"git", "config", "http.lowSpeedLimit", "0"},        // Disable low speed limit
			{"git", "config", "http.lowSpeedTime", "999999"},    // Set high timeout
			{"git", "config", "core.compression", "9"},          // Maximum compression
			{"git", "config", "core.deltaBaseCacheLimit", "2g"}, // Increase delta cache
			{"git", "config", "pack.windowMemory", "2g"},        // Increase pack window memory
			{"git", "config", "pack.packSizeLimit", "2g"},       // Increase pack size limit
			{"git", "config", "pack.threads", "1"},              // Single thread for stability
		}

		for _, cmdArgs := range configLargePushCmds {
			cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
			cmd.Dir = repoDir
			cmd.Env = gitEnv
			cmd.Run() // Ignore errors for these config settings
		}

		// Push changes to the current branch
		pushCmd := exec.Command("git", "push", "origin", currentBranch)
		pushCmd.Dir = repoDir
		pushCmd.Env = gitEnv

		// Capture both stdout and stderr for better debugging
		var stdout, stderr bytes.Buffer
		pushCmd.Stdout = &stdout
		pushCmd.Stderr = &stderr

		if err := pushCmd.Run(); err != nil {
			// Log detailed error information
			logger.Debugf("Git push failed. Stdout: %s, Stderr: %s", stdout.String(), stderr.String())
			return fmt.Errorf("failed to push changes to branch %s: %w. Stderr: %s", currentBranch, err, stderr.String())
		}
	} else {
		logger.Debugf("No changes to commit in repository: %s", repoDir)
	}

	return nil
}

// configureArgoCDRepositories configures ArgoCD to use the Gitea repositories
func configureArgoCDRepositories(giteaBaseURL, username, password string) error {
	// Create ArgoCD repository secrets for the Gitea repositories
	repositories := []string{"environments", "packages"}

	for _, repoName := range repositories {
		repoURL := fmt.Sprintf("%s/%s/%s", giteaBaseURL, username, repoName)
		secretName := fmt.Sprintf("repo-%s", repoName)

		if err := createArgoCDRepoSecret(secretName, repoURL, username, password); err != nil {
			return fmt.Errorf("failed to create ArgoCD repo secret for %s: %w", repoName, err)
		}
	}

	return nil
}

// createArgoCDRepoSecret creates a repository secret for ArgoCD
func createArgoCDRepoSecret(secretName, repoURL, username, password string) error {
	secretData := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: %s
  username: %s
  password: %s
  insecure: "true"
`, secretName, repoURL, username, password)

	// Apply the secret using kubectl
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(secretData)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create ArgoCD repository secret: %w", err)
	}

	logger.Debugf("Created ArgoCD repository secret: %s", secretName)
	return nil
}

var (
	// Define lipgloss styles
	upTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62")) // Purple
	codeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Background(lipgloss.Color("236")).Padding(0, 1)
	boldStyle     = lipgloss.NewStyle().Bold(true)
	listItemStyle = lipgloss.NewStyle().SetString("• ")
	urlStyle      = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("39")) // Blue
)

var upCmd = &cobra.Command{
	Use:     "up",
	Aliases: []string{"create"},
	Short:   "Create an Adhar IDP",
	Long: fmt.Sprintf(`%s

%s
1. %s: Developers can use %s to quickly spin up a local Adhar cluster for testing and development purposes.
   By default, it sets up a Kubernetes cluster using Kind (Kubernetes in Docker) and provisions essential platform components like ArgoCD, Gitea, and Nginx.

   %s
	 %s
	 %s

2. %s: For production environments, %s can be used with a configuration file to deploy the Adhar platform on cloud infrastructure.
   The configuration file allows customization of cluster settings, package configurations, and resource allocations.

   %s
	 %s
	 %s

%s
• Supports local development with minimal setup
• Configures Kubernetes clusters in your favorite cloud vendor with custom settings
• Provisions core platform components like Cilium, ArgoCD, Gitea, Grafana, Keycloak, Backstage, Nginx and more
• Allows customization of packages and configurations
• Supports local development with rapid iteration
• Brings holistic governance to your development environment
• Enables developers to continuously sync local directories for rapid iteration
• Supports cloud-based production deployments with configuration files

For more information, visit the documentation at %s`,
		upTitleStyle.Render(`The "adhar up" command is used to create and configure an Adhar Internal Developer Platform (IDP)`),
		boldStyle.Render("This command supports two primary use cases:"),
		boldStyle.Render("Local Development"), codeStyle.Render("adhar up"),
		boldStyle.Render("Example:"),
		codeStyle.Render("adhar up"),
		codeStyle.Render("# List available environments: adhar get envs -f config.yaml"),
		boldStyle.Render("Production Setup"), codeStyle.Render("adhar up"),
		boldStyle.Render("Example:"),
		codeStyle.Render("adhar up -f config.yaml"),
		codeStyle.Render("adhar up -f config.yaml --env prod  # Deploy specific environment"),
		boldStyle.Render("Key Features:"),
		urlStyle.Render("https://adhar.io/docs"),
	),
	RunE:         create,
	PreRunE:      preCreateE,
	SilenceUsage: true,
}

func init() {
	// cluster related flags
	upCmd.PersistentFlags().BoolVar(&recreateCluster, "recreate", false, recreateClusterUsage)
	upCmd.PersistentFlags().BoolVar(&devPassword, "dev-password", false, devPasswordUsage)
	upCmd.PersistentFlags().StringVar(&kubeVersion, "kube-version", "v1.33.2", kubeVersionUsage)
	upCmd.PersistentFlags().StringVar(&extraPortsMapping, "extra-ports", "", extraPortsMappingUsage)
	upCmd.PersistentFlags().StringVar(&kindConfigPath, "kind-config", "", kindConfigPathUsage)
	upCmd.PersistentFlags().StringSliceVar(&registryConfig, "registry-config", []string{}, registryConfigUsage)
	upCmd.PersistentFlags().Lookup("registry-config").NoOptDefVal = "$XDG_RUNTIME_DIR/containers/auth.json,$HOME/.docker/config.json"

	// in-cluster resources related flags
	upCmd.PersistentFlags().StringVar(&host, "host", globals.DefaultHostName, hostUsage)
	upCmd.PersistentFlags().StringVar(&ingressHost, "ingress-host-name", "", ingressHostUsage)
	upCmd.PersistentFlags().StringVar(&protocol, "protocol", "https", protocolUsage)
	upCmd.PersistentFlags().StringVar(&port, "port", "8443", portUsage)
	upCmd.PersistentFlags().BoolVar(&pathRouting, "use-path-routing", true, pathRoutingUsage)
	upCmd.Flags().StringSliceVarP(&extraPackages, "package", "p", []string{"platform/stack"}, extraPackagesUsage)
	upCmd.Flags().StringSliceVarP(&packageCustomizationFiles, "package-custom-file", "e", []string{}, packageCustomizationFilesUsage)

	// adhar related flags
	upCmd.Flags().BoolVarP(&noExit, "watch", "w", true, noExitUsage)
	upCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging") // Add verbose flag

	// Production cluster provisioning flags
	upCmd.Flags().StringVarP(&configFile, "file", "f", "", "Path to the configuration file for the production cluster")
	upCmd.Flags().StringVar(&environment, "env", "", "Environment for the deployment (e.g., dev, test, prod)")
	upCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Simulate the command without making any changes")
	upCmd.Flags().BoolVarP(&force, "force", "F", false, "Force the operation, ignoring any warnings")

	// Add the upCmd to the root command
	rootCmd.AddCommand(upCmd)
}

func preCreateE(cmd *cobra.Command, args []string) error {
	// Set log level based on verbose flag or global debug flag
	debugFlag, _ := cmd.Root().PersistentFlags().GetBool("debug")
	if verbose || debugFlag {
		logger.CLILogLevel = "debug"
		_ = logger.SetLogLevel("debug")
	} else {
		logger.CLILogLevel = "info"
		_ = logger.SetLogLevel("info")
	}

	// Set colored output (enable by default, disable if NO_COLOR is set)
	logger.CLIColoredOutput = os.Getenv("NO_COLOR") == ""

	return logger.SetupKubernetesLogging()
}

func create(cmd *cobra.Command, args []string) error {
	ctx, ctxCancel := context.WithCancel(cmd.Context())
	defer ctxCancel()

	// Check if this is a production setup (config file provided)
	if configFile != "" {
		fmt.Printf("🏭 %s\n", boldStyle.Render("Production Platform Provisioning Mode"))
		fmt.Printf("Configuration file: %s\n", configFile)
		if environment != "" {
			fmt.Printf("Target environment: %s\n", environment)
		} else {
			fmt.Printf("Mode: Complete platform provisioning (all environments)\n")
		}
		fmt.Println()
		return createProductionCluster(ctx, cmd, args)
	}

	// Local development mode
	fmt.Printf("🏠 %s\n", boldStyle.Render("Local Development Mode"))
	fmt.Printf("Creating Kind-based Kubernetes cluster with essential platform components\n")

	// Perform pre-flight checks
	if err := performLocalPreflightChecks(); err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}

	fmt.Println()

	// Create local development cluster using new ProviderManager
	return createLocalDevelopmentCluster(ctx, cmd, args)
}

// createProductionCluster handles production cluster provisioning using the new ProviderManager
func createProductionCluster(ctx context.Context, cmd *cobra.Command, args []string) error {
	// Validate config file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return fmt.Errorf("configuration file not found: %s", configFile)
	}

	// Load configuration from file
	cfg, err := loadConfigFromFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize enhanced logger
	log := logger.GetLogger()
	if verbose {
		log.SetLevel(logger.DEBUG)
	}

	// Initialize template engine
	// Use platform/providers factory; register kind in factory to ensure availability
	_ = pkind.Provider{}
	providerManager := newProviderManagerWithFactory(log.Logger, pfactory.DefaultFactory)

	// Show banner
	logger.Banner("Adhar Platform", "Provisioning Management Cluster and Platform Components")

	// If no environment specified, provision the complete platform
	if environment == "" {
		return provisionCompletePlatformNew(ctx, providerManager, cfg, dryRun, force)
	}

	// Get environment configuration
	envConfig, err := resolveEnvironmentConfig(cfg, environment)
	if err != nil {
		return fmt.Errorf("failed to resolve environment configuration: %w", err)
	}

	// If dry run, show what would be provisioned
	if dryRun {
		return showDryRunInfo(envConfig)
	}

	// Provision the environment
	log.StartOperation("Environment Provisioning", fmt.Sprintf("Deploying %s environment", environment))

	provisionOpts := ProvisionOptions{
		DryRun: dryRun,
		Force:  force,
	}

	if err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts); err != nil {
		logger.Error("Environment provisioning failed", err, map[string]interface{}{
			"environment": environment,
			"provider":    envConfig.ResolvedProvider,
		})
		return fmt.Errorf("failed to provision environment %s: %w", environment, err)
	}

	log.FinishOperation("Environment Provisioning", fmt.Sprintf("%s environment ready", environment))

	// Print success message
	printProductionSuccessMsg(environment)
	return nil
}

// loadConfigFromFile loads configuration from a specific file path
func loadConfigFromFile(configPath string) (*config.Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open configuration file: %w", err)
	}
	defer file.Close()

	var cfg config.Config
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse configuration file: %w", err)
	}

	// Resolve environment configurations
	if err := cfg.ResolveEnvironments(); err != nil {
		return nil, fmt.Errorf("failed to resolve environments: %w", err)
	}

	return &cfg, nil
}

// resolveEnvironmentConfig resolves a specific environment configuration
func resolveEnvironmentConfig(cfg *config.Config, envName string) (*config.ResolvedEnvironmentConfig, error) {
	if cfg.ResolvedEnvironments == nil {
		return nil, fmt.Errorf("environments not resolved")
	}

	envConfig, exists := cfg.ResolvedEnvironments[envName]
	if !exists {
		return nil, fmt.Errorf("environment '%s' not found in configuration", envName)
	}

	return envConfig, nil
}

// printProductionSuccessMsg prints success message for production cluster
func printProductionSuccessMsg(envName string) {
	fmt.Printf("\n\n########################### Successfully Provisioned Production Cluster! ############################\n\n\n")
	fmt.Printf("Environment: %s\n", envName)
	fmt.Printf("Cluster has been provisioned with:\n")
	fmt.Printf("  ✓ Cilium CNI with production-ready configuration\n")
	fmt.Printf("  ✓ Core platform services (ArgoCD, Gitea, Nginx)\n")
	fmt.Printf("  ✓ Security policies and monitoring\n")
	fmt.Printf("  ✓ Auto-scaling and high availability\n\n")
	fmt.Printf("Next steps:\n")
	fmt.Printf("  1. Configure kubectl: kubectl config current-context\n")
	fmt.Printf("  2. Access ArgoCD dashboard\n")
	fmt.Printf("  3. Deploy your applications\n\n")
}

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

	_, _, err = helpers.ParsePackageStrings(extraPackages)
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

func printSuccessMsg() {
	var argoURL string

	// For Kind clusters (local development), use clean URLs without ports
	// For other providers, use the configured protocol and port
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
	fmt.Printf("🎉 %s\n\n", boldStyle.Render("Local Development Platform Ready!"))
	fmt.Printf("Your Adhar platform includes:\n")
	fmt.Printf("  ✅ Kind Kubernetes cluster\n")
	fmt.Printf("  ✅ Cilium CNI for secure networking\n")
	fmt.Printf("  ✅ ArgoCD for GitOps deployments\n")
	fmt.Printf("  ✅ Gitea for Git repository hosting\n")
	fmt.Printf("  ✅ Ingress-Nginx for traffic routing\n")
	fmt.Printf("  ✅ Platform observability stack\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Quick Access:"))
	fmt.Printf("ArgoCD Dashboard: %s\n", argoURL)
	fmt.Printf("Username: admin\n")
	fmt.Printf("Password: Run `adhar get secrets -p argocd`\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Next Steps:"))
	fmt.Printf("1. Deploy your first application via ArgoCD\n")
	fmt.Printf("2. Push code to the integrated Gitea instance\n")
	fmt.Printf("3. Use `adhar get secrets` to retrieve service credentials\n")
	fmt.Printf("4. Run `adhar get status` to monitor platform health\n\n")
}

func behindProxy() bool {
	// check if we are in codespaces: https://docs.github.com/en/codespaces/developing-in-a-codespace
	_, ok := os.LookupEnv("CODESPACES")
	return ok
}

// validateEnvironmentExists checks if the specified environment exists in the config file
func validateEnvironmentExists(configPath, envName string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	if len(cfg.Environments) == 0 {
		return fmt.Errorf("no environments defined in configuration file")
	}

	if _, exists := cfg.Environments[envName]; !exists {
		var availableEnvs []string
		for env := range cfg.Environments {
			availableEnvs = append(availableEnvs, env)
		}
		return fmt.Errorf("environment '%s' not found. Available environments: %v", envName, availableEnvs)
	}

	return nil
}

// performLocalPreflightChecks validates requirements for local development setup
func performLocalPreflightChecks() error {
	fmt.Printf("⚡ %s\n", boldStyle.Render("Running pre-flight checks..."))

	// Check Docker availability and health
	if err := checkDockerAvailable(); err != nil {
		fmt.Printf("  ❌ Docker check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Docker is available and healthy\n")

	// Check Kind availability and functionality
	if err := checkKindAvailable(); err != nil {
		fmt.Printf("  ❌ Kind check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Kind cluster engine ready\n")

	// Check kubectl availability
	if err := checkKubectlAvailable(); err != nil {
		fmt.Printf("  ❌ kubectl check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ kubectl is available\n")

	// Check system resources (disk, memory, CPU)
	if err := checkSystemResources(); err != nil {
		fmt.Printf("  ❌ System resources check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Sufficient system resources available\n")

	// Check port availability with detailed analysis
	if err := checkPortAvailabilityDetailed(); err != nil {
		fmt.Printf("  ❌ Port availability check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Required ports are available\n")

	// Check container runtime health
	if err := checkContainerRuntimeHealth(); err != nil {
		fmt.Printf("  ❌ Container runtime health check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ Container runtime is healthy\n")

	// Check existing clusters for conflicts
	if err := checkExistingClusters(); err != nil {
		fmt.Printf("  ❌ Existing cluster check failed: %v\n", err)
		return err
	}
	fmt.Printf("  ✅ No conflicting clusters found\n")

	fmt.Println()
	return nil
}

// checkDockerAvailable checks if Docker daemon is running and healthy
func checkDockerAvailable() error {
	// Check if docker command exists
	_, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker command not found in PATH. Please install Docker: https://docs.docker.com/get-docker/")
	}

	// Check if Docker daemon is running
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil // Suppress output
	cmd.Stderr = nil // Suppress error output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker daemon is not running or not accessible. Please start Docker Desktop or Docker daemon")
	}

	// Check Docker version compatibility
	cmd = exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Docker version: %w", err)
	}

	version := strings.TrimSpace(string(output))
	if version == "" {
		return fmt.Errorf("unable to determine Docker version")
	}

	// Basic version check (Docker 20+ recommended)
	if !strings.HasPrefix(version, "2") && !strings.HasPrefix(version, "3") {
		fmt.Printf("  ⚠️  Warning: Docker version %s detected. Version 20+ recommended\n", version)
	}

	return nil
}

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

// checkKindAvailable checks if Kind binary is available and functional
func checkKindAvailable() error {
	// Check if kind command exists
	_, err := exec.LookPath("kind")
	if err != nil {
		return fmt.Errorf("kind command not found in PATH. Please install Kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation")
	}

	// Check if kind command works
	cmd := exec.Command("kind", "version")
	cmd.Stdout = nil // Suppress output
	cmd.Stderr = nil // Suppress error output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kind command is not working properly. Please reinstall Kind")
	}

	return nil
}

// checkKubectlAvailable checks if kubectl is available and functional
func checkKubectlAvailable() error {
	// Check if kubectl command exists
	_, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl command not found in PATH. Please install kubectl: https://kubernetes.io/docs/tasks/tools/")
	}

	// Check if kubectl command works
	cmd := exec.Command("kubectl", "version", "--client", "--output=yaml")
	cmd.Stdout = nil // Suppress output
	cmd.Stderr = nil // Suppress error output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl command is not working properly. Please reinstall kubectl")
	}

	return nil
}

// checkSystemResources checks if system has sufficient resources for Kind cluster
func checkSystemResources() error {
	// Check memory (basic check for macOS/Linux)
	if err := checkMemory(); err != nil {
		return err
	}

	// Check disk space with the existing function
	if err := checkDiskSpace(); err != nil {
		return err
	}

	// Check CPU cores
	if err := checkCPUCores(); err != nil {
		return err
	}

	return nil
}

// checkMemory checks if system has sufficient memory
func checkMemory() error {
	var cmd *exec.Cmd

	// Try different approaches based on OS
	if runtime.GOOS == "darwin" {
		// macOS
		cmd = exec.Command("sysctl", "-n", "hw.memsize")
	} else if runtime.GOOS == "linux" {
		// Linux
		cmd = exec.Command("sh", "-c", "grep MemTotal /proc/meminfo | awk '{print $2 * 1024}'")
	} else {
		// Windows or other - skip detailed check
		return nil
	}

	output, err := cmd.Output()
	if err != nil {
		// If we can't check memory, just warn and continue
		fmt.Printf("  ⚠️  Unable to check system memory, continuing anyway\n")
		return nil
	}

	memStr := strings.TrimSpace(string(output))
	memBytes, err := strconv.ParseInt(memStr, 10, 64)
	if err != nil {
		// If we can't parse memory, just warn and continue
		fmt.Printf("  ⚠️  Unable to parse system memory, continuing anyway\n")
		return nil
	}

	// Convert to GB
	memGB := float64(memBytes) / (1024 * 1024 * 1024)

	// Require at least 4GB of RAM for Kind cluster with platform components
	if memGB < 4.0 {
		return fmt.Errorf("insufficient memory: %.1fGB available. At least 4GB recommended for Kind cluster with platform components", memGB)
	}

	return nil
}

// checkCPUCores checks if system has sufficient CPU cores
func checkCPUCores() error {
	cores := runtime.NumCPU()

	// Require at least 2 CPU cores for Kind cluster
	if cores < 2 {
		return fmt.Errorf("insufficient CPU cores: %d available. At least 2 cores recommended for Kind cluster", cores)
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

// provisionCompletePlatformNew provisions the complete Adhar platform using the new provider system
func provisionCompletePlatformNew(ctx context.Context, providerManager *providerManager, cfg *config.Config, dryRun bool, force bool) error {
	fmt.Printf("\n%s\n", boldStyle.Render("🚀 Starting Complete Adhar Platform Provisioning"))
	fmt.Println()

	// Determine environments to provision
	var environmentsToProvision []string
	if len(cfg.Environments) == 0 {
		return fmt.Errorf("no environments defined in configuration file")
	}

	// Use environments from config
	for envName := range cfg.Environments {
		environmentsToProvision = append(environmentsToProvision, envName)
	}

	// Provision each environment
	successCount := 0
	for _, envName := range environmentsToProvision {
		fmt.Printf("  Provisioning environment: %s...\n", envName)

		envConfig, err := resolveEnvironmentConfig(cfg, envName)
		if err != nil {
			fmt.Printf("  ❌ Failed to resolve configuration for %s: %v\n", envName, err)
			continue
		}

		provisionOpts := ProvisionOptions{
			DryRun: dryRun,
			Force:  force,
		}

		if err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts); err != nil {
			fmt.Printf("  ❌ Failed to provision %s: %v\n", envName, err)
			continue
		}
		fmt.Printf("  ✅ Environment %s provisioned successfully\n", envName)
		successCount++
	}

	// Print summary
	fmt.Printf("\n%s\n", boldStyle.Render("🎉 Platform Provisioning Complete!"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environments Provisioned: %d/%d              │\n", successCount, len(environmentsToProvision))
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	if successCount < len(environmentsToProvision) {
		return fmt.Errorf("failed to provision %d out of %d environments", len(environmentsToProvision)-successCount, len(environmentsToProvision))
	}

	return nil
}

// showDryRunInfo displays what would be provisioned in dry-run mode
func showDryRunInfo(envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", boldStyle.Render("🔍 Dry Run - Configuration Preview"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environment: %-30s │\n", envConfig.Name)
	fmt.Printf("│ Provider:    %-30s │\n", envConfig.ResolvedProvider)
	fmt.Printf("│ Region:      %-30s │\n", envConfig.ResolvedRegion)
	fmt.Printf("│ Type:        %-30s │\n", envConfig.ResolvedType)
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nCluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
		}
	}

	if envConfig.ResolvedCoreServices != nil {
		fmt.Printf("\nCore Services:\n")
		fmt.Printf("  ArgoCD:    %v\n", envConfig.ResolvedCoreServices.ArgoCD != nil)
		fmt.Printf("  Gitea:     %v\n", envConfig.ResolvedCoreServices.Gitea != nil)
		fmt.Printf("  Nginx:     %v\n", envConfig.ResolvedCoreServices.Nginx != nil)
		fmt.Printf("  Cilium:    %v\n", envConfig.ResolvedCoreServices.Cilium != nil)
	}

	if len(envConfig.ResolvedAddons) > 0 {
		fmt.Printf("\nAddons:\n")
		for _, addon := range envConfig.ResolvedAddons {
			fmt.Printf("  %s\n", addon.Name)
		}
	}

	fmt.Printf("\n%s\n", codeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// showLocalDryRunInfo displays what would be provisioned in local development dry-run mode
func showLocalDryRunInfo(adharSpec *v1alpha1.AdharPlatformSpec, envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", boldStyle.Render("🔍 Dry Run - Local Development Preview"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environment: %-30s │\n", envConfig.Name)
	fmt.Printf("│ Provider:    %-30s │\n", envConfig.ResolvedProvider)
	fmt.Printf("│ Region:      %-30s │\n", envConfig.ResolvedRegion)
	fmt.Printf("│ Type:        %-30s │\n", envConfig.ResolvedType)
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	fmt.Printf("\nPlatform Configuration:\n")
	fmt.Printf("  Host:        %s\n", adharSpec.BuildCustomization.Host)
	fmt.Printf("  Protocol:    %s\n", adharSpec.BuildCustomization.Protocol)
	fmt.Printf("  Port:        %s\n", adharSpec.BuildCustomization.Port)
	fmt.Printf("  Path Routing: %v\n", adharSpec.BuildCustomization.UsePathRouting)

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
	fmt.Printf("  ArgoCD:      %v\n", adharSpec.PackageConfigs.Argo.Enabled)
	fmt.Printf("  Gitea:       %v\n", adharSpec.PackageConfigs.EmbeddedArgoApplications.Enabled)
	fmt.Printf("  Nginx:       true\n")
	fmt.Printf("  Cilium:      true\n")

	if len(adharSpec.PackageConfigs.CustomPackageDirs) > 0 || len(adharSpec.PackageConfigs.CustomPackageUrls) > 0 {
		fmt.Printf("\nCustom Packages:\n")
		for _, pkg := range adharSpec.PackageConfigs.CustomPackageDirs {
			fmt.Printf("  Directory: %s\n", pkg)
		}
		for _, pkg := range adharSpec.PackageConfigs.CustomPackageUrls {
			fmt.Printf("  URL: %s\n", pkg)
		}
	}

	fmt.Printf("\n%s\n", codeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// createLocalDevelopmentCluster creates a local Kind cluster using the original template-based approach with ProviderManager
func createLocalDevelopmentCluster(ctx context.Context, cmd *cobra.Command, args []string) error {
	// Validate arguments and set up build configuration
	if err := validate(); err != nil {
		return err
	}

	customPackageDirs, customPackageUrls, err := helpers.ParsePackageStrings(extraPackages)
	if err != nil {
		return err
	}

	registryConfigPaths, err := helpers.GetAbsFilePaths(registryConfig, true)
	if err != nil {
		return err
	}
	_ = registryConfigPaths // TODO: Use registry config paths in build process

	packageCustomizations := map[string]v1alpha1.PackageCustomization{}
	for _, packageCustomFile := range packageCustomizationFiles {
		packageCustom, customFileErr := getPackageCustomFile(packageCustomFile)
		if customFileErr != nil {
			return customFileErr
		}
		packageCustomizations[packageCustom.Name] = packageCustom
	}

	// Create AdharPlatformSpec using the template approach
	adharSpec := &v1alpha1.AdharPlatformSpec{
		PackageConfigs: v1alpha1.PackageConfigsSpec{
			Argo: v1alpha1.ArgoPackageConfigSpec{
				Enabled: true,
			},
			EmbeddedArgoApplications: v1alpha1.EmbeddedArgoApplicationsPackageConfigSpec{
				Enabled: true,
			},
			CustomPackageDirs:        customPackageDirs,
			CustomPackageUrls:        customPackageUrls,
			CorePackageCustomization: packageCustomizations,
		},
		BuildCustomization: v1alpha1.BuildCustomizationSpec{
			Protocol:       protocol,
			Host:           host,
			IngressHost:    ingressHost,
			Port:           port,
			UsePathRouting: pathRouting,
			StaticPassword: devPassword,
		},
	}

	// Show banner for local development
	logger.Banner("Adhar Internal Developer Platform", "Provisioning Management Cluster and Platform Components")

	// Use the original template-based build approach with ProviderManager
	log := logger.GetLogger()
	if verbose {
		log.SetLevel(logger.DEBUG)
	}

	providerManager := newProviderManagerWithFactory(log.Logger, pfactory.DefaultFactory)

	// Create environment config for Kind provider with CLI flags that uses template mode
	var clusterConfig []config.KeyValueConfig

	if kubeVersion != "" {
		clusterConfig = append(clusterConfig, config.KeyValueConfig{
			Key:   "kubeVersion",
			Value: kubeVersion,
		})
	}

	if extraPortsMapping != "" {
		clusterConfig = append(clusterConfig, config.KeyValueConfig{
			Key:   "extraPorts",
			Value: extraPortsMapping,
		})
	}

	if kindConfigPath != "" {
		clusterConfig = append(clusterConfig, config.KeyValueConfig{
			Key:   "configPath",
			Value: kindConfigPath,
		})
	}

	envConfig := &config.ResolvedEnvironmentConfig{
		Name:                  globals.DefaultClusterName,
		ResolvedProvider:      string(v1alpha1.ProviderKind),
		ResolvedRegion:        "local",
		ResolvedType:          config.EnvironmentTypeNonProduction,
		ResolvedClusterConfig: clusterConfig,
		GlobalSettings: &config.GlobalSettings{
			AdharContext: "provider-mode",
			DefaultHost:  globals.DefaultHostName, // Use adhar.localtest.me for Kind clusters
			EnableHAMode: false,
			Email:        "admin@" + globals.DefaultHostName, // Set email for domain config
		},
	}

	// Set provision options
	provisionOpts := ProvisionOptions{
		DryRun: dryRun,
		Force:  force || recreateCluster,
	}

	// If dry run, show what would be provisioned
	if dryRun {
		return showLocalDryRunInfo(adharSpec, envConfig)
	}

	// Start the provisioning process
	log.StartOperation("Local Development Cluster", "Creating Kind cluster with platform services")

	// Use the ProviderManager to create the Kind cluster with template-based provisioning
	if err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts); err != nil {
		logger.Error("Local cluster provisioning failed", err, map[string]interface{}{
			"cluster":  envConfig.Name,
			"provider": "kind",
		})
		return fmt.Errorf("failed to provision local development cluster: %w", err)
	}

	log.FinishOperation("Local Development Cluster", "Platform ready for development")

	// Print success message
	printSuccessMsg()
	return nil
}

// printLocalSuccessMsg prints success message for local development cluster
func printLocalSuccessMsg() {
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
	fmt.Printf("🎉 %s\n\n", boldStyle.Render("Local Development Platform Ready!"))
	fmt.Printf("Your Adhar platform includes:\n")
	fmt.Printf("  ✅ Kind Kubernetes cluster\n")
	fmt.Printf("  ✅ Cilium CNI for secure networking\n")
	fmt.Printf("  ✅ ArgoCD for GitOps deployments\n")
	fmt.Printf("  ✅ Gitea for Git repository hosting\n")
	fmt.Printf("  ✅ Ingress-Nginx for traffic routing\n")
	fmt.Printf("  ✅ Platform observability stack\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Quick Access:"))
	fmt.Printf("ArgoCD Dashboard: %s\n", argoURL)
	fmt.Printf("Username: admin\n")
	fmt.Printf("Password: Run `adhar get secrets -p argocd`\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Next Steps:"))
	fmt.Printf("1. Deploy your first application via ArgoCD\n")
	fmt.Printf("2. Push code to the integrated Gitea instance\n")
	fmt.Printf("3. Use `adhar get secrets` to retrieve service credentials\n")
	fmt.Printf("4. Run `adhar get status` to monitor platform health\n\n")
	fmt.Printf("%s\n", boldStyle.Render("Local Development Commands:"))
	fmt.Printf("• Check cluster status: adhar get status\n")
	fmt.Printf("• Get service secrets: adhar get secrets\n")
	fmt.Printf("• Destroy cluster: adhar down\n\n")
}
