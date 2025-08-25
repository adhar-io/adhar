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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
	ptypes "adhar-io/adhar/platform/types"
	"bytes"
	"io"
)

// ProvisionOptions contains options for provisioning
type ProvisionOptions struct {
	DryRun bool
	Force  bool
}

// providerManager manages cloud providers
type providerManager struct {
	factory pfactory.ProviderFactory
}

// newProviderManagerWithFactory creates a new provider manager
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
	if envConfig.ResolvedProvider == "kind" {
		if baseDomain == "" || baseDomain == "platform.adhar.io" {
			baseDomain = "adhar.localtest.me"
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

// parseIntOrDefault parses a string to int, returning default if parsing fails
func parseIntOrDefault(s string, defaultValue int) int {
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	return defaultValue
}

// installCiliumCNI installs Cilium CNI in the Kind cluster
func installCiliumCNI() error {
	// Install Cilium using Helm
	cmd := exec.Command("helm", "repo", "add", "cilium", "https://helm.cilium.io/")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add Cilium Helm repo: %w", err)
	}

	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	cmd = exec.Command("helm", "install", "cilium", "cilium/cilium", "--namespace", "kube-system", "--set", "kubeProxyReplacement=strict", "--set", "k8sServiceHost=kind-control-plane", "--set", "k8sServicePort=6443")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install Cilium: %w", err)
	}

	return nil
}

// applyPlatformStack applies the core platform components in the correct order with progress tracking
func applyPlatformStack(envConfig *config.ResolvedEnvironmentConfig) error {
	// Set environment variable to disable Kind provider progress display
	os.Setenv("ADHAR_PLATFORM_SETUP", "true")
	defer os.Unsetenv("ADHAR_PLATFORM_SETUP")

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

	progress := helpers.NewProgressTrackerWithDetails("🚀 Setting up Adhar Platform", stepNames, stepDescriptions)
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

	// Step 2: Create required namespaces
	progress.StartStep(1, "")
	if err := createNamespaces(); err != nil {
		progress.FailStep(1, err)
		return fmt.Errorf("failed to create namespaces: %w", err)
	}
	progress.CompleteStep(1)

	// Step 3: Install ArgoCD from platform resources
	progress.StartStep(2, "")
	if err := applyPlatformManifest("argocd"); err != nil {
		progress.FailStep(2, err)
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}
	progress.CompleteStep(2)

	// Step 4: Install Gitea from platform resources
	progress.StartStep(3, "")
	if err := applyPlatformManifest("gitea"); err != nil {
		progress.FailStep(3, err)
		return fmt.Errorf("failed to install Gitea: %w", err)
	}
	progress.CompleteStep(3)

	// Step 5: Install Crossplane from platform resources
	progress.StartStep(4, "")
	if err := applyPlatformManifest("crossplane"); err != nil {
		progress.FailStep(4, err)
		return fmt.Errorf("failed to install Crossplane: %w", err)
	}
	progress.CompleteStep(4)

	// Step 6: Install Nginx Ingress from platform resources
	progress.StartStep(5, "")
	if err := applyPlatformManifest("nginx"); err != nil {
		progress.FailStep(5, err)
		return fmt.Errorf("failed to install Nginx Ingress: %w", err)
	}
	progress.CompleteStep(5)

	// Step 7: Install Ingress Resources for platform components
	progress.StartStep(6, "")
	if err := applyIngressManifests(); err != nil {
		progress.FailStep(6, err)
		return fmt.Errorf("failed to install ingress resources: %w", err)
	}
	progress.CompleteStep(6)

	// Step 8: Label core secrets for CLI discovery
	progress.StartStep(7, "")
	if err := labelCoreSecrets(); err != nil {
		// Don't fail completely, just warn and skip
		progress.SkipStep(7, "Failed to label secrets, continuing anyway")
		logger.Warnf("Secret labeling failed, continuing anyway: %v", err)
	} else {
		progress.CompleteStep(7)
	}

	// Step 9: Wait for ArgoCD to be ready
	progress.StartStep(8, "")
	if err := waitForArgoCD(); err != nil {
		progress.FailStep(8, err)
		return fmt.Errorf("failed to wait for ArgoCD: %w", err)
	}
	progress.CompleteStep(8)

	// Step 10: Apply platform stack ApplicationSets
	progress.StartStep(9, "")
	if err := applyPlatformApplicationSets(); err != nil {
		progress.FailStep(9, err)
		return fmt.Errorf("failed to apply platform ApplicationSets: %w", err)
	}
	progress.CompleteStep(9)

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

// applyManifests applies manifests using kubectl
func applyManifests(path string) error {
	logger.Debugf("Applying manifests from: %s", path)

	// Check if the path exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("manifest path does not exist: %s", path)
	}

	// Check if it's a directory with kustomization.yaml
	if stat, err := os.Stat(path); err == nil && stat.IsDir() {
		// Check if kustomization.yaml exists
		if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); err == nil {
			// Use kubectl apply -k for kustomize-based manifests
			cmd := exec.Command("kubectl", "apply", "-k", path)
			// Suppress output during progress tracking, only capture errors
			var stderr bytes.Buffer
			cmd.Stdout = io.Discard
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to apply kustomize manifests from %s: %w\nStderr: %s", path, err, stderr.String())
			}
		} else {
			// Directory without kustomization.yaml, apply all YAML files
			return applyYAMLFilesInDirectory(path)
		}
	} else {
		// Single file, apply directly
		cmd := exec.Command("kubectl", "apply", "-f", path)
		// Suppress output during progress tracking, only capture errors
		var stderr bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to apply manifest file %s: %w\nStderr: %s", path, err, stderr.String())
		}
	}

	logger.Debugf("Successfully applied manifests from: %s", path)
	return nil
}

// applyYAMLFilesInDirectory applies all YAML files in a directory
func applyYAMLFilesInDirectory(dirPath string) error {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", dirPath, err)
	}

	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".yaml") {
			filePath := filepath.Join(dirPath, file.Name())
			logger.Debugf("Applying manifest file: %s", filePath)

			cmd := exec.Command("kubectl", "apply", "-f", filePath)
			// Suppress output during progress tracking, only capture errors
			var stderr bytes.Buffer
			cmd.Stdout = io.Discard
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				logger.Warnf("Failed to apply manifest file %s: %v\nStderr: %s", filePath, err, stderr.String())
				// Continue with other files
			} else {
				logger.Debugf("Successfully applied manifest file: %s", filePath)
			}
		}
	}

	return nil
}

// createNamespaces creates the required namespaces for the platform
func createNamespaces() error {
	logger.Info("Creating required namespaces")

	namespaces := []string{
		"argocd",
		"gitea",
		"crossplane",
		"nginx-ingress",
		"adhar-system",
	}

	for _, ns := range namespaces {
		// Check if namespace already exists
		cmd := exec.Command("kubectl", "get", "namespace", ns)
		// Suppress output during progress tracking
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err == nil {
			logger.Debugf("Namespace %s already exists, skipping", ns)
			continue
		}

		// Create namespace
		createCmd := exec.Command("kubectl", "create", "namespace", ns)
		// Suppress output during progress tracking, only capture errors
		var stderr bytes.Buffer
		createCmd.Stdout = io.Discard
		createCmd.Stderr = &stderr

		if err := createCmd.Run(); err != nil {
			return fmt.Errorf("failed to create namespace %s: %w\nStderr: %s", ns, err, stderr.String())
		}

		logger.Debugf("Created namespace: %s", ns)
	}

	logger.Info("All required namespaces created successfully")
	return nil
}

// applyPlatformManifest intelligently chooses between regular and HA manifests based on config
func applyPlatformManifest(component string) error {
	logger.Infof("Installing %s component", component)

	// Determine manifest path based on component and HA mode
	var manifestPath string
	haMode := getPlatformHAMode()

	switch component {
	case "argocd":
		if haMode {
			manifestPath = "platform/controllers/adharplatform/resources/argocd/install-ha.yaml"
		} else {
			manifestPath = "platform/controllers/adharplatform/resources/argocd/install.yaml"
		}
	case "gitea":
		manifestPath = "platform/controllers/adharplatform/resources/gitea/install.yaml"
	case "crossplane":
		manifestPath = "platform/controllers/adharplatform/resources/crossplane/install.yaml"
	case "nginx":
		manifestPath = "platform/controllers/adharplatform/resources/nginx/install.yaml"
	default:
		return fmt.Errorf("unknown component: %s", component)
	}

	// Check if manifest exists
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("manifest not found: %s", manifestPath)
	}

	// Apply the manifest
	cmd := exec.Command("kubectl", "apply", "-f", manifestPath)
	// Suppress output during progress tracking, only capture errors
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install %s: %w\nStderr: %s", component, err, stderr.String())
	}

	logger.Debugf("Successfully installed %s component", component)
	return nil
}

// applyIngressManifests applies ingress manifests for platform components
func applyIngressManifests() error {
	logger.Info("Installing ingress resources")

	// Wait for NGINX ingress controller to be fully ready before applying ingress resources
	logger.Info("Waiting for NGINX ingress controller to be ready...")
	nginxCmd := exec.Command("kubectl", "wait", "--for=condition=available", "--timeout=300s", "deployment/ingress-nginx-controller", "-n", "adhar-system")
	var nginxStderr bytes.Buffer
	nginxCmd.Stdout = io.Discard
	nginxCmd.Stderr = &nginxStderr

	if err := nginxCmd.Run(); err != nil {
		logger.Warnf("NGINX ingress controller not ready within timeout: %v\nStderr: %s", err, nginxStderr.String())
	} else {
		logger.Info("NGINX ingress controller is ready")
	}

	// Additional wait for webhook readiness - give it a moment to register
	logger.Info("Allowing webhook to initialize...")
	time.Sleep(10 * time.Second)

	// Apply ingress manifests for platform components
	ingressManifests := []string{
		"platform/controllers/adharplatform/resources/ingress/argocd-ingress.yaml",
		"platform/controllers/adharplatform/resources/ingress/gitea-ingress.yaml",
		"platform/controllers/adharplatform/resources/ingress/crossplane-ingress.yaml",
	}

	for _, manifest := range ingressManifests {
		// Check if manifest exists
		if _, err := os.Stat(manifest); os.IsNotExist(err) {
			logger.Warnf("Ingress manifest not found, skipping: %s", manifest)
			continue
		}

		// Apply the manifest with retry logic for webhook readiness
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			cmd := exec.Command("kubectl", "apply", "-f", manifest)
			// Suppress output during progress tracking, only capture errors
			var stderr bytes.Buffer
			cmd.Stdout = io.Discard
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				lastErr = err
				if strings.Contains(stderr.String(), "failed calling webhook") {
					logger.Warnf("Webhook not ready, retrying in 5 seconds (attempt %d/3)...", attempt)
					time.Sleep(5 * time.Second)
					continue
				} else {
					logger.Warnf("Failed to apply ingress manifest %s: %v\nStderr: %s", manifest, err, stderr.String())
					break
				}
			} else {
				logger.Debugf("Applied ingress manifest: %s", manifest)
				lastErr = nil
				break
			}
		}

		if lastErr != nil {
			logger.Warnf("Failed to apply ingress manifest %s after 3 attempts: %v", manifest, lastErr)
			// Continue with other manifests
		}
	}

	logger.Info("Ingress resources installation completed")
	return nil
}

// waitForArgoCD waits for ArgoCD components to be ready with timeout handling
func waitForArgoCD() error {
	logger.Info("Waiting for ArgoCD to be ready")

	// Wait for ArgoCD server deployment to be ready
	logger.Info("Waiting for ArgoCD server deployment...")
	cmd := exec.Command("kubectl", "wait", "--for=condition=available", "--timeout=300s", "deployment/argo-cd-argocd-server", "-n", "adhar-system")
	// Suppress output during progress tracking, only capture errors
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD server deployment not ready within timeout: %w\nStderr: %s", err, stderr.String())
	}
	logger.Info("ArgoCD server deployment is ready")

	// Wait for ArgoCD application controller pod to be ready
	// Since there's no service for the application controller, check pod readiness directly
	logger.Info("Waiting for ArgoCD application controller pod...")
	cmd = exec.Command("kubectl", "wait", "--for=condition=ready", "--timeout=300s", "pod/argo-cd-argocd-application-controller-0", "-n", "adhar-system")
	// Suppress output during progress tracking, only capture errors
	stderr.Reset()
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Try to get more diagnostic information
		logger.Warnf("ArgoCD application controller pod wait failed, checking status...")

		// Check the actual pod status
		statusCmd := exec.Command("kubectl", "get", "pod", "argo-cd-argocd-application-controller-0", "-n", "adhar-system", "-o", "yaml")
		var statusOutput bytes.Buffer
		statusCmd.Stdout = &statusOutput
		statusCmd.Stderr = io.Discard

		if statusErr := statusCmd.Run(); statusErr == nil {
			logger.Warnf("Pod status: %s", statusOutput.String())
		}

		// Check pod logs for any errors
		logsCmd := exec.Command("kubectl", "logs", "argo-cd-argocd-application-controller-0", "-n", "adhar-system", "--tail=20")
		var logsOutput bytes.Buffer
		logsCmd.Stdout = &logsOutput
		logsCmd.Stderr = io.Discard

		if logsErr := logsCmd.Run(); logsErr == nil {
			logger.Warnf("Pod logs: %s", logsOutput.String())
		}

		return fmt.Errorf("ArgoCD application controller pod not ready within timeout: %w\nStderr: %s", err, stderr.String())
	}
	logger.Info("ArgoCD application controller pod is ready")

	// Wait for ArgoCD repo server deployment to be ready
	logger.Info("Waiting for ArgoCD repo server deployment...")
	cmd = exec.Command("kubectl", "wait", "--for=condition=available", "--timeout=300s", "deployment/argo-cd-argocd-repo-server", "-n", "adhar-system")
	// Suppress output during progress tracking, only capture errors
	stderr.Reset()
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD repo server deployment not ready within timeout: %w\nStderr: %s", err, stderr.String())
	}
	logger.Info("ArgoCD repo server deployment is ready")

	// Additional check: ensure all ArgoCD pods are actually running
	logger.Info("Verifying all ArgoCD pods are running...")
	time.Sleep(5 * time.Second) // Give pods a moment to stabilize

	verifyCmd := exec.Command("kubectl", "get", "pods", "-l", "app.kubernetes.io/part-of=argocd", "-n", "adhar-system", "--no-headers")
	var verifyOutput bytes.Buffer
	verifyCmd.Stdout = &verifyOutput
	verifyCmd.Stderr = io.Discard

	if err := verifyCmd.Run(); err != nil {
		logger.Warnf("Failed to verify ArgoCD pods: %v", err)
	} else {
		podLines := strings.Split(strings.TrimSpace(verifyOutput.String()), "\n")
		for _, line := range podLines {
			if line != "" && !strings.Contains(line, "Running") && !strings.Contains(line, "Completed") {
				logger.Warnf("ArgoCD pod not in expected state: %s", line)
			}
		}
	}

	logger.Info("ArgoCD is ready")
	return nil
}

// applyPlatformApplicationSets applies the platform stack ApplicationSets for ArgoCD management
func applyPlatformApplicationSets() error {
	logger.Info("Applying platform ApplicationSets")

	// Apply platform stack ApplicationSets
	appsetManifests := []string{
		"platform/stack/adhar-appset-manifests.yaml",
		"platform/stack/adhar-appset-charts.yaml",
	}

	for _, manifest := range appsetManifests {
		// Check if manifest exists
		if _, err := os.Stat(manifest); os.IsNotExist(err) {
			logger.Warnf("ApplicationSet manifest not found, skipping: %s", manifest)
			continue
		}

		// Apply the manifest
		cmd := exec.Command("kubectl", "apply", "-f", manifest)
		// Suppress output during progress tracking, only capture errors
		var stderr bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			logger.Warnf("Failed to apply ApplicationSet manifest %s: %v\nStderr: %s", manifest, err, stderr.String())
			// Continue with other manifests
		} else {
			logger.Debugf("Applied ApplicationSet manifest: %s", manifest)
		}
	}

	logger.Info("Platform ApplicationSets applied successfully")
	return nil
}

// setupGitOpsRepositories creates and populates GitOps repositories in Gitea for ArgoCD
func setupGitOpsRepositories() error {
	logger.Info("Setting up GitOps repositories")

	// Wait for Gitea to be ready before setting up repositories
	logger.Info("Waiting for Gitea to be ready...")
	cmd := exec.Command("kubectl", "wait", "--for=condition=available", "--timeout=300s", "deployment/gitea", "-n", "adhar-system")
	// Suppress output during progress tracking, only capture errors
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Warnf("Gitea not ready within timeout, skipping repository setup: %v\nStderr: %s", err, stderr.String())
		return nil
	}

	// Create GitOps repositories in Gitea
	logger.Info("Creating GitOps repositories in Gitea...")

	// Create environments repository
	if err := createGiteaRepository("environments", "Platform environment configurations"); err != nil {
		logger.Warnf("Failed to create environments repository: %v", err)
		return fmt.Errorf("failed to create environments repository: %w", err)
	}

	// Create packages repository
	if err := createGiteaRepository("packages", "Platform package manifests"); err != nil {
		logger.Warnf("Failed to create packages repository: %v", err)
		return fmt.Errorf("failed to create packages repository: %w", err)
	}

	// Push platform stack content to repositories
	logger.Info("Pushing platform stack content to repositories...")

	if err := pushPlatformStackContent(); err != nil {
		logger.Warnf("Failed to push platform stack content: %v", err)
		return fmt.Errorf("failed to push platform stack content: %w", err)
	}

	logger.Info("GitOps repositories setup completed successfully")
	return nil
}

// createGiteaRepository creates a new repository in Gitea
func createGiteaRepository(name, description string) error {
	logger.Infof("Creating Gitea repository: %s", name)

	// Create repository using Gitea API
	createRepoCmd := exec.Command("kubectl", "exec", "-n", "adhar-system", "deployment/gitea", "--",
		"gitea", "admin", "repo", "create", "--name", name, "--description", description,
		"--private", "--owner", "gitea_admin")

	var stderr bytes.Buffer
	createRepoCmd.Stdout = io.Discard
	createRepoCmd.Stderr = &stderr

	if err := createRepoCmd.Run(); err != nil {
		// If repository already exists, that's fine
		if strings.Contains(stderr.String(), "already exists") {
			logger.Infof("Repository %s already exists", name)
			return nil
		}
		return fmt.Errorf("failed to create repository %s: %w\nStderr: %s", name, err, stderr.String())
	}

	logger.Infof("Successfully created repository: %s", name)
	return nil
}

// pushPlatformStackContent pushes the platform stack manifests to Gitea repositories
func pushPlatformStackContent() error {
	logger.Info("Pushing platform stack content to Gitea repositories...")

	// Create temporary directories for git operations
	tempDir, err := os.MkdirTemp("", "adhar-platform-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Clone and push to environments repository
	if err := pushToRepository("environments", "platform/stack/environments", tempDir); err != nil {
		return fmt.Errorf("failed to push to environments repository: %w", err)
	}

	// Clone and push to packages repository
	if err := pushToRepository("packages", "platform/stack/packages", tempDir); err != nil {
		return fmt.Errorf("failed to push to packages repository: %w", err)
	}

	logger.Info("Successfully pushed platform stack content to repositories")
	return nil
}

// pushToRepository clones a repository and pushes content to it
func pushToRepository(repoName, sourcePath, tempDir string) error {
	logger.Infof("Pushing content to repository: %s", repoName)

	repoDir := filepath.Join(tempDir, repoName)

	// Initialize git repository
	initCmd := exec.Command("git", "init", repoDir)
	initCmd.Dir = tempDir
	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("failed to initialize git repository: %w", err)
	}

	// Copy source content
	if err := copyDirectory(sourcePath, repoDir); err != nil {
		return fmt.Errorf("failed to copy source content: %w", err)
	}

	// Add remote origin (using external Gitea URL)
	remoteCmd := exec.Command("git", "remote", "add", "origin",
		fmt.Sprintf("http://adhar.localtest.me:3000/gitea_admin/%s.git", repoName))
	remoteCmd.Dir = repoDir
	if err := remoteCmd.Run(); err != nil {
		return fmt.Errorf("failed to add remote origin: %w", err)
	}

	// Create and checkout main branch before first commit
	checkoutCmd := exec.Command("git", "checkout", "-b", "main")
	checkoutCmd.Dir = repoDir
	if err := checkoutCmd.Run(); err != nil {
		return fmt.Errorf("failed to create main branch: %w", err)
	}

	// Add all files
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = repoDir
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("failed to add files: %w", err)
	}

	// Commit
	commitCmd := exec.Command("git", "commit", "-m", "Initial platform stack content")
	commitCmd.Dir = repoDir
	commitCmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Adhar Platform", "GIT_AUTHOR_EMAIL=admin@adhar.io")
	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("failed to commit files: %w", err)
	}

	// Push to Gitea
	pushCmd := exec.Command("git", "push", "-u", "origin", "main")
	pushCmd.Dir = repoDir
	pushCmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Adhar Platform", "GIT_AUTHOR_EMAIL=admin@adhar.io")

	var stderr bytes.Buffer
	pushCmd.Stderr = &stderr

	if err := pushCmd.Run(); err != nil {
		return fmt.Errorf("failed to push to repository %s: %w\nStderr: %s", repoName, err, stderr.String())
	}

	logger.Infof("Successfully pushed content to repository: %s", repoName)
	return nil
}

// copyDirectory copies a directory recursively
func copyDirectory(src, dst string) error {
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip .DS_Store and other system files
		if info.Name() == ".DS_Store" || info.Name() == ".git" {
			return nil
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		// Copy file
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})

	return err
}

// labelCoreSecrets adds proper labels to core secrets for CLI discovery
func labelCoreSecrets() error {
	logger.Info("Labeling core secrets")

	// Label ArgoCD admin secret (in adhar-system namespace)
	argocdSecretCmd := exec.Command("kubectl", "label", "secret", "argocd-initial-admin-secret", "adhar.io/component=argocd", "adhar.io/managed-by=adhar", "-n", "adhar-system", "--overwrite")
	// Suppress output during progress tracking, only capture errors
	var stderr bytes.Buffer
	argocdSecretCmd.Stdout = io.Discard
	argocdSecretCmd.Stderr = &stderr

	if err := argocdSecretCmd.Run(); err != nil {
		logger.Warnf("Failed to label ArgoCD secret: %v\nStderr: %s", err, stderr.String())
		// Continue anyway
	} else {
		logger.Debugf("Labeled ArgoCD admin secret")
	}

	// Label Gitea admin secret (in adhar-system namespace)
	giteaSecretCmd := exec.Command("kubectl", "label", "secret", "gitea-admin-secret", "adhar.io/component=gitea", "adhar.io/managed-by=adhar", "-n", "adhar-system", "--overwrite")
	// Suppress output during progress tracking, only capture errors
	stderr.Reset()
	giteaSecretCmd.Stdout = io.Discard
	giteaSecretCmd.Stderr = &stderr

	if err := giteaSecretCmd.Run(); err != nil {
		logger.Warnf("Failed to label Gitea secret: %v\nStderr: %s", err, stderr.String())
		// Continue anyway
	} else {
		logger.Debugf("Labeled Gitea admin secret")
	}

	logger.Info("Core secrets labeling completed")
	return nil
}
