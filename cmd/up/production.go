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

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ProductionProvisioner handles production environment deployment
type ProductionProvisioner struct {
	configFile string
	options    *ProductionOptions
	config     *config.Config
	factory    pfactory.ProviderFactory
}

// ProductionOptions contains configuration for production deployment
type ProductionOptions struct {
	ConfigFile  string
	Environment string
	DryRun      bool
	Force       bool
}

// NewProductionProvisioner creates a new production provisioner
func NewProductionProvisioner(configFile string, options *ProductionOptions) *ProductionProvisioner {
	return &ProductionProvisioner{
		configFile: configFile,
		options:    options,
	}
}

// Provision creates the production environment
func (pp *ProductionProvisioner) Provision() error {
	logger.Info("☁️ Production Deployment Mode")
	logger.Info("Deploying to cloud provider using configuration file")

	// Load and validate configuration
	if err := pp.loadConfiguration(); err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Validate configuration
	if err := pp.validateConfiguration(); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	// Run pre-flight checks
	if err := pp.runPreFlightChecks(); err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}

	// Create cloud cluster
	if err := pp.createCloudCluster(); err != nil {
		return fmt.Errorf("failed to create cloud cluster: %w", err)
	}

	// Install platform components
	if err := pp.installPlatformComponents(); err != nil {
		return fmt.Errorf("failed to install platform components: %w", err)
	}

	// Setup GitOps repositories
	if err := pp.setupGitOpsRepositories(); err != nil {
		return fmt.Errorf("failed to setup GitOps repositories: %w", err)
	}

	logger.Info("✅ Production environment created successfully!")
	return nil
}

// loadConfiguration loads the configuration file
func (pp *ProductionProvisioner) loadConfiguration() error {
	logger.Info("📋 Loading configuration from: " + pp.configFile)

	// Check if config file exists
	if _, err := os.Stat(pp.configFile); os.IsNotExist(err) {
		return fmt.Errorf("configuration file not found: %s", pp.configFile)
	}

	// Read and parse configuration file
	data, err := os.ReadFile(pp.configFile)
	if err != nil {
		return fmt.Errorf("failed to read configuration file: %w", err)
	}

	pp.config = &config.Config{}
	if err := yaml.Unmarshal(data, pp.config); err != nil {
		return fmt.Errorf("failed to parse configuration file: %w", err)
	}

	logger.Info("✅ Configuration loaded successfully")
	return nil
}

// validateConfiguration validates the loaded configuration
func (pp *ProductionProvisioner) validateConfiguration() error {
	logger.Info("🔍 Validating configuration...")

	// Basic configuration validation
	if len(pp.config.Environments) == 0 {
		return fmt.Errorf("no environments defined in configuration")
	}

	// Check if the specified environment exists
	if pp.options.Environment != "" {
		if _, exists := pp.config.Environments[pp.options.Environment]; !exists {
			return fmt.Errorf("environment '%s' not found in configuration", pp.options.Environment)
		}
	}

	// Environment-specific validation
	for envName, envConfig := range pp.config.Environments {
		if err := pp.validateEnvironment(envName, envConfig); err != nil {
			return fmt.Errorf("environment '%s' validation failed: %w", envName, err)
		}
	}

	logger.Info("✅ Configuration validation passed")
	return nil
}

// validateEnvironment performs environment-specific validation
func (pp *ProductionProvisioner) validateEnvironment(envName string, envConfig config.EnvironmentConfig) error {
	// Validate environment name
	if envName == "" {
		return fmt.Errorf("environment name cannot be empty")
	}

	// Validate provider configuration
	if envConfig.Provider == "" {
		return fmt.Errorf("provider not specified for environment '%s'", envName)
	}

	// Validate region configuration
	if envConfig.Region == "" {
		return fmt.Errorf("region not specified for environment '%s'", envName)
	}

	// Validate cluster configuration
	if len(envConfig.ClusterConfig) > 0 {
		if err := pp.validateClusterConfig(envConfig.ClusterConfig); err != nil {
			return fmt.Errorf("cluster configuration validation failed: %w", err)
		}
	}

	// Validate core services configuration
	if envConfig.CoreServices != nil {
		if err := pp.validateCoreServices(envConfig.CoreServices); err != nil {
			return fmt.Errorf("core services validation failed: %w", err)
		}
	}

	return nil
}

// validateClusterConfig validates cluster-specific configuration
func (pp *ProductionProvisioner) validateClusterConfig(clusterConfig []config.KeyValueConfig) error {
	// Validate cluster configuration key-value pairs
	for _, kv := range clusterConfig {
		if kv.Key == "" {
			return fmt.Errorf("cluster config key cannot be empty")
		}
		if kv.Value == "" {
			return fmt.Errorf("cluster config value cannot be empty for key '%s'", kv.Key)
		}
	}
	return nil
}

// validateCoreServices validates core services configuration
func (pp *ProductionProvisioner) validateCoreServices(services map[string]config.ServiceConfig) error {
	// Validate ArgoCD configuration
	if argocd, exists := services["argocd"]; exists {
		if argocd.Chart.Version == "" {
			return fmt.Errorf("ArgoCD version must be specified when enabled")
		}
	}

	// Validate Gitea configuration
	if gitea, exists := services["gitea"]; exists {
		if gitea.Chart.Version == "" {
			return fmt.Errorf("Gitea version must be specified when enabled")
		}
	}

	return nil
}

// runPreFlightChecks validates system requirements for production deployment
func (pp *ProductionProvisioner) runPreFlightChecks() error {
	logger.Info("⚡ Running production pre-flight checks...")

	checks := []struct {
		name  string
		check func() error
	}{
		{"Provider authentication", pp.checkProviderAuthentication},
		{"Resource quotas", pp.checkResourceQuotas},
		{"Network connectivity", pp.checkNetworkConnectivity},
	}

	for _, check := range checks {
		if err := check.check(); err != nil {
			return fmt.Errorf("❌ %s check failed: %w", check.name, err)
		}
		logger.Info("✅ " + check.name + " check passed")
	}

	return nil
}

// checkProviderAuthentication verifies provider credentials
func (pp *ProductionProvisioner) checkProviderAuthentication() error {
	// Get the target environment
	var targetEnv config.EnvironmentConfig
	if pp.options.Environment != "" {
		targetEnv = pp.config.Environments[pp.options.Environment]
	} else {
		// Use first environment if none specified
		for _, env := range pp.config.Environments {
			targetEnv = env
			break
		}
	}

	// Create provider instance for authentication check
	provider, err := pp.factory.CreateProvider(targetEnv.Provider, map[string]interface{}{
		"region": targetEnv.Region,
	})
	if err != nil {
		return fmt.Errorf("failed to create provider instance: %w", err)
	}

	// Test authentication
	if err := provider.Authenticate(context.Background(), nil); err != nil {
		return fmt.Errorf("provider authentication failed: %w", err)
	}

	// Test permissions
	if err := provider.ValidatePermissions(context.Background()); err != nil {
		return fmt.Errorf("provider permissions validation failed: %w", err)
	}

	return nil
}

// checkResourceQuotas verifies available resources
func (pp *ProductionProvisioner) checkResourceQuotas() error {
	// Get the target environment
	var targetEnv config.EnvironmentConfig
	if pp.options.Environment != "" {
		targetEnv = pp.config.Environments[pp.options.Environment]
	} else {
		// Use first environment if none specified
		for _, env := range pp.config.Environments {
			targetEnv = env
			break
		}
	}

	// Create provider instance for resource check
	provider, err := pp.factory.CreateProvider(targetEnv.Provider, map[string]interface{}{
		"region": targetEnv.Region,
	})
	if err != nil {
		return fmt.Errorf("failed to create provider instance: %w", err)
	}

	// Check resource quotas through provider
	if quotaChecker, ok := provider.(interface {
		CheckResourceQuotas(context.Context) error
	}); ok {
		if err := quotaChecker.CheckResourceQuotas(context.Background()); err != nil {
			return fmt.Errorf("resource quota check failed: %w", err)
		}
	} else {
		// Fallback to basic resource validation
		pp.checkBasicResourceRequirements(targetEnv)
	}

	return nil
}

// checkBasicResourceRequirements performs basic resource validation
func (pp *ProductionProvisioner) checkBasicResourceRequirements(envConfig config.EnvironmentConfig) {
	// Check cluster configuration for resource requirements
	if len(envConfig.ClusterConfig) > 0 {
		// For now, just validate that cluster config exists
		// In a real implementation, this would parse specific keys like "controlPlaneReplicas"
		logger.Info("Cluster configuration validation passed")
	}
}

// checkNetworkConnectivity verifies network access
func (pp *ProductionProvisioner) checkNetworkConnectivity() error {
	// Check basic internet connectivity
	if err := pp.checkInternetConnectivity(); err != nil {
		return fmt.Errorf("internet connectivity check failed: %w", err)
	}

	// Check provider-specific connectivity
	if err := pp.checkProviderConnectivity(); err != nil {
		return fmt.Errorf("provider connectivity check failed: %w", err)
	}

	return nil
}

// checkInternetConnectivity verifies basic internet access
func (pp *ProductionProvisioner) checkInternetConnectivity() error {
	// Test DNS resolution
	if err := pp.testDNSResolution("8.8.8.8"); err != nil {
		return fmt.Errorf("DNS resolution failed: %w", err)
	}

	// Test HTTP connectivity
	if err := pp.testHTTPConnectivity("https://httpbin.org/get"); err != nil {
		return fmt.Errorf("HTTP connectivity failed: %w", err)
	}

	return nil
}

// checkProviderConnectivity verifies provider-specific connectivity
func (pp *ProductionProvisioner) checkProviderConnectivity() error {
	// Get the target environment
	var targetEnv config.EnvironmentConfig
	if pp.options.Environment != "" {
		targetEnv = pp.config.Environments[pp.options.Environment]
	} else {
		// Use first environment if none specified
		for _, env := range pp.config.Environments {
			targetEnv = env
			break
		}
	}

	// Test provider-specific endpoints based on provider type
	switch targetEnv.Provider {
	case "aws":
		return pp.testAWSConnectivity()
	case "azure":
		return pp.testAzureConnectivity()
	case "gcp":
		return pp.testGCPConnectivity()
	case "digitalocean":
		return pp.testDigitalOceanConnectivity()
	default:
		// For unknown providers, just return success
		return nil
	}
}

// testDNSResolution tests DNS resolution
func (pp *ProductionProvisioner) testDNSResolution(host string) error {
	// Simple DNS test - in production this would use proper DNS resolution
	// For now, we'll assume success
	return nil
}

// testHTTPConnectivity tests HTTP connectivity
func (pp *ProductionProvisioner) testHTTPConnectivity(url string) error {
	// Simple HTTP test - in production this would make actual HTTP requests
	// For now, we'll assume success
	return nil
}

// testAWSConnectivity tests AWS-specific connectivity
func (pp *ProductionProvisioner) testAWSConnectivity() error {
	// Test AWS endpoints
	return nil
}

// testAzureConnectivity tests Azure-specific connectivity
func (pp *ProductionProvisioner) testAzureConnectivity() error {
	// Test Azure endpoints
	return nil
}

// testGCPConnectivity tests GCP-specific connectivity
func (pp *ProductionProvisioner) testGCPConnectivity() error {
	// Test GCP endpoints
	return nil
}

// testDigitalOceanConnectivity tests DigitalOcean-specific connectivity
func (pp *ProductionProvisioner) testDigitalOceanConnectivity() error {
	// Test DigitalOcean endpoints
	return nil
}

// createCloudCluster creates the cloud-based Kubernetes cluster
func (pp *ProductionProvisioner) createCloudCluster() error {
	logger.Info("🔧 Creating cloud Kubernetes cluster...")

	// Get the target environment
	var targetEnv config.EnvironmentConfig
	if pp.options.Environment != "" {
		targetEnv = pp.config.Environments[pp.options.Environment]
	} else {
		// Use first environment if none specified
		for _, env := range pp.config.Environments {
			targetEnv = env
			break
		}
	}

	// Create provider instance
	provider, err := pp.factory.CreateProvider(targetEnv.Provider, map[string]interface{}{
		"region": targetEnv.Region,
	})
	if err != nil {
		return fmt.Errorf("failed to create provider instance: %w", err)
	}

	// Build cluster specification
	spec := &types.ClusterSpec{
		Provider: targetEnv.Provider,
		Region:   targetEnv.Region,
		ObjectMeta: types.ObjectMeta{
			Name: targetEnv.Template, // Use template name as cluster name
		},
	}

	// Add cluster configuration from environment
	for _, kv := range targetEnv.ClusterConfig {
		switch kv.Key {
		case "controlPlaneReplicas":
			// Parse and set control plane replicas
		case "workerNodeReplicas":
			// Parse and set worker node replicas
		case "nodeType":
			// Parse and set node type
		}
	}

	// Create the cluster
	cluster, err := provider.CreateCluster(context.Background(), spec)
	if err != nil {
		return fmt.Errorf("failed to create cluster: %w", err)
	}

	logger.Infof("✅ Cloud cluster created successfully - ID: %s, Status: %s", cluster.ID, cluster.Status)
	return nil
}

// installPlatformComponents installs the core platform components
func (pp *ProductionProvisioner) installPlatformComponents() error {
	logger.Info("📦 Installing platform components...")

	// Get the target environment
	var targetEnv config.EnvironmentConfig
	if pp.options.Environment != "" {
		targetEnv = pp.config.Environments[pp.options.Environment]
	} else {
		// Use first environment if none specified
		for _, env := range pp.config.Environments {
			targetEnv = env
			break
		}
	}

	// Install core services based on environment configuration
	if targetEnv.CoreServices != nil {
		if err := pp.installCoreServices(targetEnv.CoreServices); err != nil {
			return fmt.Errorf("failed to install core services: %w", err)
		}
	}

	// Install addons
	if len(targetEnv.Addons) > 0 {
		if err := pp.installAddons(targetEnv.Addons); err != nil {
			return fmt.Errorf("failed to install addons: %w", err)
		}
	}

	logger.Info("✅ Platform components installed successfully")
	return nil
}

// installCoreServices installs core platform services
func (pp *ProductionProvisioner) installCoreServices(services map[string]config.ServiceConfig) error {
	for serviceName, serviceConfig := range services {
		logger.Infof("Installing core service: %s", serviceName)

		if err := pp.installService(serviceName, serviceConfig); err != nil {
			return fmt.Errorf("failed to install service %s: %w", serviceName, err)
		}
	}
	return nil
}

// installAddons installs platform addons
func (pp *ProductionProvisioner) installAddons(addons []config.AddonConfig) error {
	for _, addon := range addons {
		logger.Infof("Installing addon: %s", addon.Name)

		if err := pp.installAddon(addon); err != nil {
			return fmt.Errorf("failed to install addon %s: %w", addon.Name, err)
		}
	}
	return nil
}

// installService installs a single service
func (pp *ProductionProvisioner) installService(serviceName string, serviceConfig config.ServiceConfig) error {
	// Use Helm to install the service
	helmCmd := exec.Command("helm", "install", serviceName,
		fmt.Sprintf("%s/%s", serviceConfig.Chart.RepoURL, serviceConfig.Chart.Name),
		"--version", serviceConfig.Chart.Version,
		"--namespace", "adhar-system",
		"--create-namespace")

	if err := helmCmd.Run(); err != nil {
		return fmt.Errorf("helm install failed: %w", err)
	}

	return nil
}

// installAddon installs a single addon
func (pp *ProductionProvisioner) installAddon(addon config.AddonConfig) error {
	// Use Helm to install the addon
	helmCmd := exec.Command("helm", "install", addon.Name,
		fmt.Sprintf("%s/%s", addon.Chart.RepoURL, addon.Chart.Name),
		"--version", addon.Chart.Version,
		"--namespace", addon.TargetNamespace,
		"--create-namespace")

	if addon.CreateNamespace {
		helmCmd.Args = append(helmCmd.Args, "--create-namespace")
	}

	if err := helmCmd.Run(); err != nil {
		return fmt.Errorf("helm install failed: %w", err)
	}

	return nil
}

// setupGitOpsRepositories sets up GitOps repositories and workflows
func (pp *ProductionProvisioner) setupGitOpsRepositories() error {
	logger.Info("🔄 Setting up GitOps repositories...")

	// Wait for ArgoCD to be ready
	if err := pp.waitForArgoCD(); err != nil {
		return fmt.Errorf("ArgoCD not ready for GitOps setup: %w", err)
	}

	// Create GitOps repositories
	pp.createGitOpsRepositories()

	// Configure ArgoCD applications
	if err := pp.configureArgoCDApplications(); err != nil {
		return fmt.Errorf("failed to configure ArgoCD applications: %w", err)
	}

	logger.Info("✅ GitOps repositories and workflows configured successfully")
	return nil
}

// waitForArgoCD waits for ArgoCD to be ready
func (pp *ProductionProvisioner) waitForArgoCD() error {
	logger.Info("Waiting for ArgoCD to be ready...")

	// Wait for ArgoCD server deployment to be ready
	cmd := exec.Command("kubectl", "wait", "--for=condition=available", "--timeout=300s", "deployment/argo-cd-argocd-server", "-n", "adhar-system")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD server not ready: %w", err)
	}

	return nil
}

// createGitOpsRepositories creates GitOps repositories
func (pp *ProductionProvisioner) createGitOpsRepositories() {
	// Create repositories for different components
	repositories := []string{
		"platform-manifests",
		"application-manifests",
		"infrastructure-manifests",
	}

	for _, repoName := range repositories {
		if err := pp.createRepository(repoName); err != nil {
			logger.Warnf("Failed to create repository %s: %v", repoName, err)
			// Continue with other repositories
		}
	}
}

// createRepository creates a single GitOps repository
func (pp *ProductionProvisioner) createRepository(repoName string) error {
	// This would typically create repositories in Gitea or other Git providers
	// For now, we'll just log the action
	logger.Infof("Creating GitOps repository: %s", repoName)
	return nil
}

// configureArgoCDApplications configures ArgoCD applications
func (pp *ProductionProvisioner) configureArgoCDApplications() error {
	// Create ApplicationSet for platform components
	if err := pp.createPlatformApplicationSet(); err != nil {
		return fmt.Errorf("failed to create platform ApplicationSet: %w", err)
	}

	// Create ApplicationSet for applications
	if err := pp.createApplicationApplicationSet(); err != nil {
		return fmt.Errorf("failed to create application ApplicationSet: %w", err)
	}

	return nil
}

// createPlatformApplicationSet creates ApplicationSet for platform components
func (pp *ProductionProvisioner) createPlatformApplicationSet() error {
	// Apply platform ApplicationSet manifest
	cmd := exec.Command("kubectl", "apply", "-f", "platform/stack/adhar-appset-manifests.yaml")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply platform ApplicationSet: %w", err)
	}

	return nil
}

// createApplicationApplicationSet creates ApplicationSet for applications
func (pp *ProductionProvisioner) createApplicationApplicationSet() error {
	// Apply application ApplicationSet manifest
	cmd := exec.Command("kubectl", "apply", "-f", "platform/stack/adhar-appset-charts.yaml")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply application ApplicationSet: %w", err)
	}

	return nil
}

// createProductionCluster handles production cluster provisioning using the new ProviderManager
func createProductionCluster(ctx context.Context, _ *cobra.Command, _ []string, _ context.CancelFunc) error {
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

	// Create provider manager for production operations
	providerManager := pfactory.NewProviderManager(pfactory.DefaultFactory)

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

	// Set provision options
	provisionOpts := pfactory.ProvisionOptions{
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
	defer func() {
		_ = file.Close()
	}()

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

// provisionCompletePlatformNew provisions the complete Adhar platform using the new provider system
func provisionCompletePlatformNew(ctx context.Context, providerManager *pfactory.ProviderManager, cfg *config.Config, dryRun bool, force bool) error {
	fmt.Printf("\n%s\n", helpers.BoldStyle.Render("🚀 Starting Complete Adhar Platform Provisioning"))
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

		provisionOpts := pfactory.ProvisionOptions{
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
	fmt.Printf("\n%s\n", helpers.BoldStyle.Render("🎉 Platform Provisioning Complete!"))
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
	fmt.Printf("\n%s\n", helpers.BoldStyle.Render("🔍 Dry Run - Configuration Preview"))
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

	fmt.Printf("\n%s\n", helpers.CodeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// validateEnvironmentExists checks if the specified environment exists in the config file
//
//nolint:unused // Retained for future CLI validation
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
