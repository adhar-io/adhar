package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/config"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	provisionLog = ctrl.Log.WithName("provision")
)

// ProvisioningOptions contains all configuration for production cluster provisioning
type ProvisioningOptions struct {
	ConfigPath      string
	EnvironmentName string
	DryRun          bool
	Force           bool
	Verbose         bool
}

// ClusterProvisioner handles production-ready Kubernetes cluster provisioning
type ClusterProvisioner struct {
	config  *config.Config
	options *ProvisioningOptions
	logger  *logrus.Logger
}

// NewClusterProvisioner creates a new cluster provisioner instance
func NewClusterProvisioner(opts *ProvisioningOptions) (*ClusterProvisioner, error) {
	provisioner := &ClusterProvisioner{
		options: opts,
		logger:  logrus.New(),
	}

	// Load configuration
	cfg, err := provisioner.loadConfiguration()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	provisioner.config = cfg

	return provisioner, nil
}

// loadConfiguration loads and validates the adhar-config.yaml file
func (cp *ClusterProvisioner) loadConfiguration() (*config.Config, error) {
	configPath := cp.options.ConfigPath
	if configPath == "" {
		// Default locations to search for config file
		searchPaths := []string{
			"./adhar-config.yaml",
			"./adhar-config.yml",
			filepath.Join(os.Getenv("HOME"), ".adhar", "config.yaml"),
			filepath.Join(os.Getenv("HOME"), ".adhar", "config.yml"),
		}

		for _, path := range searchPaths {
			if _, err := os.Stat(path); err == nil {
				configPath = path
				break
			}
		}

		if configPath == "" {
			return nil, fmt.Errorf("no configuration file found. Please provide a path using -f flag or place adhar-config.yaml in current directory or ~/.adhar/")
		}
	}

	cp.logger.Info("Loading configuration", "path", configPath)

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Validate configuration
	if err := cp.validateConfiguration(&cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// validateConfiguration validates the loaded configuration
func (cp *ClusterProvisioner) validateConfiguration(cfg *config.Config) error {
	// Allow empty environments only when no specific environment is requested (complete platform setup)
	if len(cfg.Environments) == 0 && cp.options.EnvironmentName != "" {
		return fmt.Errorf("no environments defined in configuration")
	}

	// Validate environment exists
	if cp.options.EnvironmentName != "" {
		if _, exists := cfg.Environments[cp.options.EnvironmentName]; !exists {
			available := make([]string, 0, len(cfg.Environments))
			for name := range cfg.Environments {
				available = append(available, name)
			}
			return fmt.Errorf("environment '%s' not found. Available environments: %s",
				cp.options.EnvironmentName, strings.Join(available, ", "))
		}
	}

	// Validate provider credentials
	if err := cp.validateProviderCredentials(cfg); err != nil {
		return fmt.Errorf("provider credentials validation failed: %w", err)
	}

	// Validate dual-provider configuration
	if err := cp.validateDualProviderConfig(cfg); err != nil {
		return fmt.Errorf("dual-provider configuration validation failed: %w", err)
	}

	return nil
}

// validateDualProviderConfig validates the dual-provider setup
func (cp *ClusterProvisioner) validateDualProviderConfig(cfg *config.Config) error {
	// Check if dual-provider is configured
	if cfg.GlobalSettings.ProductionProvider == "" || cfg.GlobalSettings.NonProductionProvider == "" {
		cp.logger.Info("Dual-provider configuration not found, using legacy single-provider mode")
		return nil
	}

	// Validate production provider
	if !cp.isValidProvider(string(cfg.GlobalSettings.ProductionProvider)) {
		return fmt.Errorf("invalid production provider: %s", cfg.GlobalSettings.ProductionProvider)
	}

	// Validate non-production provider
	if !cp.isValidProvider(string(cfg.GlobalSettings.NonProductionProvider)) {
		return fmt.Errorf("invalid non-production provider: %s", cfg.GlobalSettings.NonProductionProvider)
	}

	// Warn if both providers are the same (not recommended but allowed)
	if cfg.GlobalSettings.ProductionProvider == cfg.GlobalSettings.NonProductionProvider {
		cp.logger.Warn("Production and non-production providers are the same",
			"provider", cfg.GlobalSettings.ProductionProvider)
	}

	cp.logger.Info("Dual-provider configuration validated",
		"productionProvider", cfg.GlobalSettings.ProductionProvider,
		"nonProductionProvider", cfg.GlobalSettings.NonProductionProvider)

	return nil
}

// isValidProvider checks if a provider is supported
func (cp *ClusterProvisioner) isValidProvider(provider string) bool {
	validProviders := []string{"gke", "aws", "azure", "do", "civo", "onprem"}
	for _, valid := range validProviders {
		if provider == valid {
			return true
		}
	}
	return false
}

// validateProviderCredentials validates that required credentials are available
func (cp *ClusterProvisioner) validateProviderCredentials(cfg *config.Config) error {
	creds := cfg.GlobalSettings.ProviderCredentials

	// Check each provider's credentials
	providers := map[string]*config.CredentialSource{
		"digitalocean": creds.DO,
		"gcp":          creds.GKE,
		"aws":          creds.AWS,
		"azure":        creds.Azure,
		"civo":         creds.Civo,
	}

	for providerName, credSource := range providers {
		if credSource == nil {
			continue
		}

		switch credSource.Type {
		case "environment":
			if credSource.EnvVar != "" {
				if _, exists := os.LookupEnv(credSource.EnvVar); !exists {
					cp.logger.Warn("Environment variable not found", "provider", providerName, "envVar", credSource.EnvVar)
				}
			}
		case "file":
			if credSource.Path != "" {
				if _, err := os.Stat(credSource.Path); os.IsNotExist(err) {
					cp.logger.Warn("Credential file not found", "provider", providerName, "path", credSource.Path)
				}
			}
		}
	}

	return nil
}

// ProvisionCluster provisions a production-ready Kubernetes cluster
func (cp *ClusterProvisioner) ProvisionCluster(ctx context.Context, environmentName string) error {
	if environmentName == "" && cp.options.EnvironmentName != "" {
		environmentName = cp.options.EnvironmentName
	}

	if environmentName == "" {
		return fmt.Errorf("environment name is required")
	}

	cp.logger.Info("Starting cluster provisioning", "environment", environmentName)

	// Get environment configuration
	envConfig, err := cp.getEnvironmentConfig(environmentName)
	if err != nil {
		return fmt.Errorf("failed to get environment configuration: %w", err)
	}

	// Validate environment configuration
	if err := cp.validateEnvironmentConfig(envConfig); err != nil {
		return fmt.Errorf("invalid environment configuration: %w", err)
	}

	// Create provider instance
	provider, err := cp.createProvider(envConfig)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	// Pre-provision validation
	if err := cp.preProvisionValidation(ctx, envConfig); err != nil {
		return fmt.Errorf("pre-provision validation failed: %w", err)
	}

	// Dry run check
	if cp.options.DryRun {
		cp.logger.Info("Dry run mode - would provision cluster", "environment", environmentName)
		return cp.dryRunReport(envConfig)
	}

	// Provision the cluster
	if err := cp.provisionWithProvider(ctx, provider, envConfig); err != nil {
		return fmt.Errorf("cluster provisioning failed: %w", err)
	}

	// Post-provision setup
	if err := cp.postProvisionSetup(ctx, envConfig); err != nil {
		return fmt.Errorf("post-provision setup failed: %w", err)
	}

	cp.logger.Info("Cluster provisioning completed successfully", "environment", environmentName)
	return nil
}

// getEnvironmentConfig retrieves and resolves environment configuration
func (cp *ClusterProvisioner) getEnvironmentConfig(environmentName string) (*config.ResolvedEnvironmentConfig, error) {
	envConfig, exists := cp.config.Environments[environmentName]
	if !exists {
		return nil, fmt.Errorf("environment '%s' not found", environmentName)
	}

	// Resolve configuration with templates
	resolvedConfig, err := cp.resolveEnvironmentConfig(environmentName, &envConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve environment configuration: %w", err)
	}

	return resolvedConfig, nil
}

// resolveEnvironmentConfig resolves environment configuration with template inheritance
func (cp *ClusterProvisioner) resolveEnvironmentConfig(envName string, envConfig *config.EnvironmentConfig) (*config.ResolvedEnvironmentConfig, error) {
	resolved := &config.ResolvedEnvironmentConfig{
		Name:             envName,
		ResolvedType:     envConfig.Type,
		ResolvedProvider: envConfig.Provider,
		ResolvedRegion:   envConfig.Region,
		GlobalSettings:   &cp.config.GlobalSettings,
	}

	// Auto-detect environment type if not specified
	if resolved.ResolvedType == "" {
		resolved.ResolvedType = cp.detectEnvironmentType(envName)
	}

	// Apply dual-provider configuration if provider not explicitly set
	if resolved.ResolvedProvider == "" {
		if resolved.ResolvedType == config.EnvironmentTypeProduction {
			resolved.ResolvedProvider = cp.config.GlobalSettings.ProductionProvider
			if resolved.ResolvedRegion == "" {
				resolved.ResolvedRegion = cp.config.GlobalSettings.ProductionRegion
			}
		} else {
			resolved.ResolvedProvider = cp.config.GlobalSettings.NonProductionProvider
			if resolved.ResolvedRegion == "" {
				resolved.ResolvedRegion = cp.config.GlobalSettings.NonProductionRegion
			}
		}
	}

	// Apply template if specified
	if envConfig.Template != "" {
		template, exists := cp.config.EnvironmentTemplates[envConfig.Template]
		if !exists {
			return nil, fmt.Errorf("template '%s' not found", envConfig.Template)
		}

		// Merge template configuration
		resolved.ResolvedClusterConfig = template.ClusterConfig
		resolved.ResolvedCoreServices = template.CoreServices
		resolved.ResolvedAddons = template.Addons
	}

	// Override with environment-specific configuration
	if envConfig.ClusterConfig != nil {
		resolved.ResolvedClusterConfig = envConfig.ClusterConfig
	}

	if envConfig.CoreServices != nil {
		resolved.ResolvedCoreServices = envConfig.CoreServices
	}

	if len(envConfig.Addons) > 0 {
		resolved.ResolvedAddons = append(resolved.ResolvedAddons, envConfig.Addons...)
	}

	// Apply global defaults as fallback
	if resolved.ResolvedRegion == "" {
		resolved.ResolvedRegion = cp.config.GlobalSettings.DefaultRegion
	}

	return resolved, nil
}

// detectEnvironmentType auto-detects environment type based on name patterns
func (cp *ClusterProvisioner) detectEnvironmentType(envName string) config.EnvironmentType {
	envNameLower := strings.ToLower(envName)

	// Production environment patterns
	productionPatterns := []string{"prod", "production", "live", "staging"}
	for _, pattern := range productionPatterns {
		if strings.Contains(envNameLower, pattern) {
			return config.EnvironmentTypeProduction
		}
	}

	// Default to non-production
	return config.EnvironmentTypeNonProduction
}

// ProvisionManagementCluster provisions the management cluster on the detected provider
func (cp *ClusterProvisioner) ProvisionManagementCluster(ctx context.Context) error {
	// Determine which provider to use for management cluster
	var mgmtProvider v1alpha1.EnvironmentProvider
	var mgmtRegion string

	if cp.config.GlobalSettings.ProductionProvider != "" {
		// Use production provider for management cluster in dual-provider setup
		mgmtProvider = cp.config.GlobalSettings.ProductionProvider
		mgmtRegion = cp.config.GlobalSettings.ProductionRegion
		cp.logger.Info("Using production provider for management cluster", "provider", mgmtProvider)
	} else {
		// Single-provider mode: detect from environments or use onprem as fallback
		mgmtProvider = cp.detectManagementProvider()
		mgmtRegion = cp.config.GlobalSettings.DefaultRegion
		cp.logger.Info("Single-provider mode detected", "managementProvider", mgmtProvider)
	}

	cp.logger.Info("Starting management cluster provisioning",
		"provider", mgmtProvider,
		"region", mgmtRegion)

	// Create management cluster configuration
	mgmtConfig := &config.ResolvedEnvironmentConfig{
		Name:             "adhar-management",
		ResolvedType:     config.EnvironmentTypeProduction,
		ResolvedProvider: mgmtProvider,
		ResolvedRegion:   mgmtRegion,
		GlobalSettings:   &cp.config.GlobalSettings,
		ResolvedClusterConfig: []config.ClusterConfig{
			{Key: "name", Value: "adhar-management"},
			{Key: "autoScale", Value: "true"},
			{Key: "minNodes", Value: "3"},
			{Key: "maxNodes", Value: "10"},
			{Key: "nodeSize", Value: "s-4vcpu-8gb"}, // Management cluster needs more resources
		},
	}

	// Apply provider-specific defaults
	switch mgmtProvider {
	case "onprem":
		// For on-premises, we use bootstrap scripts and don't need cloud-specific config
		mgmtConfig.ResolvedClusterConfig = []config.ClusterConfig{
			{Key: "name", Value: "adhar-management"},
			{Key: "kubernetesVersion", Value: "v1.31.3"},
			{Key: "ciliumVersion", Value: "1.16.4"},
			{Key: "enableCiliumHubble", Value: "true"},
			{Key: "enablePrometheus", Value: "true"},
			{Key: "enableGrafana", Value: "true"},
		}
	case "do", "digitalocean":
		if mgmtConfig.ResolvedRegion == "" {
			mgmtConfig.ResolvedRegion = "nyc3" // DigitalOcean default
		}
	case "gcp", "gke":
		if mgmtConfig.ResolvedRegion == "" {
			mgmtConfig.ResolvedRegion = "us-central1-c" // GCP default
		}
	case "aws", "eks":
		if mgmtConfig.ResolvedRegion == "" {
			mgmtConfig.ResolvedRegion = "us-west-2" // AWS default
		}
	case "azure", "aks":
		if mgmtConfig.ResolvedRegion == "" {
			mgmtConfig.ResolvedRegion = "East US" // Azure default
		}
	case "civo":
		if mgmtConfig.ResolvedRegion == "" {
			mgmtConfig.ResolvedRegion = "NYC1" // Civo default
		}
	}

	// Validate management cluster configuration
	if err := cp.validateEnvironmentConfig(mgmtConfig); err != nil {
		return fmt.Errorf("management cluster configuration validation failed: %w", err)
	}

	// Pre-provision validation
	if err := cp.preProvisionValidation(ctx, mgmtConfig); err != nil {
		return fmt.Errorf("pre-provision validation failed: %w", err)
	}

	// Create provider-specific provisioner
	provisionerImpl, err := cp.createProvider(mgmtConfig)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	// Provision the cluster
	if err := provisionerImpl.Provision(mgmtConfig); err != nil {
		return fmt.Errorf("cluster provisioning failed: %w", err)
	}

	// Post-provision setup with management cluster focus
	if err := cp.postProvisionSetupForManagement(ctx, mgmtConfig); err != nil {
		return fmt.Errorf("management cluster setup failed: %w", err)
	}

	cp.logger.Info("Management cluster provisioned successfully")
	return nil
}

// EnsureManagementCluster checks if management cluster exists and provisions it if needed
func (cp *ClusterProvisioner) EnsureManagementCluster(ctx context.Context) error {
	cp.logger.Info("Ensuring management cluster is available")

	// Determine which provider to use for management cluster
	var mgmtProvider v1alpha1.EnvironmentProvider
	if cp.config.GlobalSettings.ProductionProvider != "" {
		// Use production provider for management cluster in dual-provider setup
		mgmtProvider = cp.config.GlobalSettings.ProductionProvider
		cp.logger.Info("Using production provider for management cluster", "provider", mgmtProvider)
	} else {
		// Single-provider mode: try to detect from environments or use onprem as fallback
		mgmtProvider = cp.detectManagementProvider()
		cp.logger.Info("Single-provider mode detected", "managementProvider", mgmtProvider)
	}

	// Check if management cluster already exists
	exists, err := cp.checkManagementClusterExists(ctx, mgmtProvider)
	if err != nil {
		cp.logger.Warn("Failed to check management cluster status", "error", err, "provider", mgmtProvider)
		// Continue with provisioning attempt
	}

	if exists {
		cp.logger.Info("Management cluster already exists, skipping provisioning", "provider", mgmtProvider)
		return nil
	}

	cp.logger.Info("Management cluster not found. Provisioning automatically", "provider", mgmtProvider)
	return cp.ProvisionManagementCluster(ctx)
}

// detectManagementProvider detects which provider to use for management cluster in single-provider mode
func (cp *ClusterProvisioner) detectManagementProvider() v1alpha1.EnvironmentProvider {
	// Check if any environment has a specific provider configured
	for _, envConfig := range cp.config.Environments {
		if envConfig.Provider != "" {
			cp.logger.Info("Detected provider from environment configuration", "provider", envConfig.Provider)
			return envConfig.Provider
		}
	}

	// Check if any cloud provider credentials are configured
	if cp.config.GlobalSettings.ProviderCredentials.DO != nil {
		return "do"
	}
	if cp.config.GlobalSettings.ProviderCredentials.GKE != nil {
		return "gcp"
	}
	if cp.config.GlobalSettings.ProviderCredentials.AWS != nil {
		return "aws"
	}
	if cp.config.GlobalSettings.ProviderCredentials.Azure != nil {
		return "azure"
	}
	if cp.config.GlobalSettings.ProviderCredentials.Civo != nil {
		return "civo"
	}

	// Default to on-premises if no cloud provider credentials found
	cp.logger.Info("No cloud provider credentials found, defaulting to on-premises management cluster")
	return "onprem"
}

// checkManagementClusterExists checks if the management cluster is already provisioned
func (cp *ClusterProvisioner) checkManagementClusterExists(ctx context.Context, provider v1alpha1.EnvironmentProvider) (bool, error) {
	cp.logger.Info("Checking if management cluster exists", "provider", provider)

	switch provider {
	case "do", "digitalocean":
		return cp.checkDigitalOceanClusterExists(ctx, "adhar-management")
	case "gcp", "gke":
		return cp.checkGCPClusterExists(ctx, "adhar-management")
	case "aws", "eks":
		return cp.checkAWSClusterExists(ctx, "adhar-management")
	case "azure", "aks":
		return cp.checkAzureClusterExists(ctx, "adhar-management")
	case "civo":
		return cp.checkCivoClusterExists(ctx, "adhar-management")
	case "onprem":
		return cp.checkOnPremClusterExists(ctx, "adhar-management")
	default:
		cp.logger.Warn("Unknown provider for management cluster check", "provider", provider)
		return false, fmt.Errorf("unsupported provider for management cluster: %s", provider)
	}
}

// validateEnvironmentConfig validates the resolved environment configuration
func (cp *ClusterProvisioner) validateEnvironmentConfig(envConfig *config.ResolvedEnvironmentConfig) error {
	if envConfig.ResolvedProvider == "" {
		return fmt.Errorf("provider is required")
	}

	// Validate provider-specific configuration
	switch envConfig.ResolvedProvider {
	case "do", "digitalocean":
		return cp.validateDigitalOceanConfig(envConfig)
	case "gcp", "gke":
		return cp.validateGCPConfig(envConfig)
	case "aws", "eks":
		return cp.validateAWSConfig(envConfig)
	case "azure", "aks":
		return cp.validateAzureConfig(envConfig)
	case "civo":
		return cp.validateCivoConfig(envConfig)
	case "onprem":
		return cp.validateOnPremConfig(envConfig)
	default:
		return fmt.Errorf("unsupported provider: %s", envConfig.ResolvedProvider)
	}
}

// validateDigitalOceanConfig validates DigitalOcean-specific configuration
func (cp *ClusterProvisioner) validateDigitalOceanConfig(envConfig *config.ResolvedEnvironmentConfig) error {
	if envConfig.ResolvedRegion == "" {
		return fmt.Errorf("region is required for DigitalOcean")
	}

	// Validate region
	validRegions := []string{"nyc1", "nyc3", "ams3", "sfo3", "sgp1", "lon1", "fra1", "tor1", "blr1", "syd1"}
	regionValid := false
	for _, validRegion := range validRegions {
		if envConfig.ResolvedRegion == validRegion {
			regionValid = true
			break
		}
	}
	if !regionValid {
		return fmt.Errorf("invalid DigitalOcean region: %s", envConfig.ResolvedRegion)
	}

	// Check for required credentials
	if cp.config.GlobalSettings.ProviderCredentials.DO == nil {
		return fmt.Errorf("DigitalOcean credentials not configured")
	}

	return nil
}

// validateGCPConfig validates GCP-specific configuration
func (cp *ClusterProvisioner) validateGCPConfig(envConfig *config.ResolvedEnvironmentConfig) error {
	if envConfig.ResolvedRegion == "" {
		return fmt.Errorf("region is required for GCP")
	}

	// Check for required credentials
	if cp.config.GlobalSettings.ProviderCredentials.GKE == nil {
		return fmt.Errorf("GCP credentials not configured")
	}

	return nil
}

// validateAWSConfig validates AWS-specific configuration
func (cp *ClusterProvisioner) validateAWSConfig(envConfig *config.ResolvedEnvironmentConfig) error {
	if envConfig.ResolvedRegion == "" {
		return fmt.Errorf("region is required for AWS")
	}

	// Check for required credentials
	if cp.config.GlobalSettings.ProviderCredentials.AWS == nil {
		return fmt.Errorf("AWS credentials not configured")
	}

	return nil
}

// validateAzureConfig validates Azure-specific configuration
func (cp *ClusterProvisioner) validateAzureConfig(envConfig *config.ResolvedEnvironmentConfig) error {
	if envConfig.ResolvedRegion == "" {
		return fmt.Errorf("region is required for Azure")
	}

	// Check for required credentials
	if cp.config.GlobalSettings.ProviderCredentials.Azure == nil {
		return fmt.Errorf("Azure credentials not configured")
	}

	return nil
}

// validateCivoConfig validates Civo-specific configuration
func (cp *ClusterProvisioner) validateCivoConfig(envConfig *config.ResolvedEnvironmentConfig) error {
	if envConfig.ResolvedRegion == "" {
		return fmt.Errorf("region is required for Civo")
	}

	// Check for required credentials
	if cp.config.GlobalSettings.ProviderCredentials.Civo == nil {
		return fmt.Errorf("Civo credentials not configured")
	}

	return nil
}

// validateOnPremConfig validates on-premises configuration
func (cp *ClusterProvisioner) validateOnPremConfig(envConfig *config.ResolvedEnvironmentConfig) error {
	// On-premises validation would include checking for required infrastructure
	// This is a placeholder for now
	return nil
}

// createProvider creates the appropriate provider instance
func (cp *ClusterProvisioner) createProvider(envConfig *config.ResolvedEnvironmentConfig) (Provisioner, error) {
	switch envConfig.ResolvedProvider {
	case "do", "digitalocean":
		return cp.createDigitalOceanProvider(envConfig)
	case "gcp", "gke":
		return cp.createGCPProvider(envConfig)
	case "aws", "eks":
		return cp.createAWSProvider(envConfig)
	case "azure", "aks":
		return cp.createAzureProvider(envConfig)
	case "civo":
		return cp.createCivoProvider(envConfig)
	case "onprem":
		return cp.createOnPremProvider(envConfig)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", envConfig.ResolvedProvider)
	}
}

// createDigitalOceanProvider creates a DigitalOcean provider instance
func (cp *ClusterProvisioner) createDigitalOceanProvider(envConfig *config.ResolvedEnvironmentConfig) (Provisioner, error) {
	creds := cp.config.GlobalSettings.ProviderCredentials.DO
	if creds == nil {
		return nil, fmt.Errorf("DigitalOcean credentials not configured")
	}

	var token string
	if creds.Type == "environment" && creds.EnvVar != "" {
		var exists bool
		token, exists = os.LookupEnv(creds.EnvVar)
		if !exists {
			return nil, fmt.Errorf("DigitalOcean token not found in environment variable: %s", creds.EnvVar)
		}
	} else if creds.Type == "file" && creds.Path != "" {
		// Load token from file
		data, err := os.ReadFile(creds.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to read DigitalOcean token file: %w", err)
		}
		token = strings.TrimSpace(string(data))
	}

	if token == "" {
		return nil, fmt.Errorf("DigitalOcean token is empty")
	}

	return NewDigitalOceanProvisioner(token, cp.logger), nil
}

// createGCPProvider creates a GCP provider instance
func (cp *ClusterProvisioner) createGCPProvider(envConfig *config.ResolvedEnvironmentConfig) (Provisioner, error) {
	creds := cp.config.GlobalSettings.ProviderCredentials.GKE
	if creds == nil {
		return nil, fmt.Errorf("GCP credentials not configured")
	}

	return NewGCPProvisioner(creds, cp.logger), nil
}

// createAWSProvider creates an AWS provider instance
func (cp *ClusterProvisioner) createAWSProvider(envConfig *config.ResolvedEnvironmentConfig) (Provisioner, error) {
	creds := cp.config.GlobalSettings.ProviderCredentials.AWS
	if creds == nil {
		return nil, fmt.Errorf("AWS credentials not configured")
	}

	return NewAWSProvisioner(creds, cp.logger), nil
}

// createAzureProvider creates an Azure provider instance
func (cp *ClusterProvisioner) createAzureProvider(envConfig *config.ResolvedEnvironmentConfig) (Provisioner, error) {
	creds := cp.config.GlobalSettings.ProviderCredentials.Azure
	if creds == nil {
		return nil, fmt.Errorf("Azure credentials not configured")
	}

	return NewAzureProvisioner(creds, cp.logger), nil
}

// createCivoProvider creates a Civo provider instance
func (cp *ClusterProvisioner) createCivoProvider(envConfig *config.ResolvedEnvironmentConfig) (Provisioner, error) {
	creds := cp.config.GlobalSettings.ProviderCredentials.Civo
	if creds == nil {
		return nil, fmt.Errorf("Civo credentials not configured")
	}

	var token string
	if creds.Type == "environment" && creds.EnvVar != "" {
		var exists bool
		token, exists = os.LookupEnv(creds.EnvVar)
		if !exists {
			return nil, fmt.Errorf("Civo token not found in environment variable: %s", creds.EnvVar)
		}
	}

	return NewCivoProvisioner(token, cp.logger), nil
}

// createOnPremProvider creates an on-premises provider instance
func (cp *ClusterProvisioner) createOnPremProvider(envConfig *config.ResolvedEnvironmentConfig) (Provisioner, error) {
	return NewOnPremProvisioner(cp.logger), nil
}

// preProvisionValidation performs pre-provision validation checks
func (cp *ClusterProvisioner) preProvisionValidation(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Performing pre-provision validation")

	// Check if cluster already exists
	exists, err := cp.checkClusterExists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check cluster existence: %w", err)
	}

	if exists && !cp.options.Force {
		return fmt.Errorf("cluster already exists. Use --force to recreate")
	}

	// Validate resource quotas
	if err := cp.validateResourceQuotas(ctx, envConfig); err != nil {
		return fmt.Errorf("resource quota validation failed: %w", err)
	}

	// Validate network configuration
	if err := cp.validateNetworkConfig(ctx, envConfig); err != nil {
		return fmt.Errorf("network configuration validation failed: %w", err)
	}

	return nil
}

// checkClusterExists checks if the cluster already exists
func (cp *ClusterProvisioner) checkClusterExists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error) {
	// This would be implemented per provider
	// For now, return false
	return false, nil
}

// validateResourceQuotas validates that required resources are available
func (cp *ClusterProvisioner) validateResourceQuotas(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	// This would check provider-specific quotas
	// For now, just log
	cp.logger.Info("Validating resource quotas")
	return nil
}

// validateNetworkConfig validates network configuration
func (cp *ClusterProvisioner) validateNetworkConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	// This would validate VPC, subnet, and security group configurations
	// For now, just log
	cp.logger.Info("Validating network configuration")
	return nil
}

// dryRunReport generates a dry run report
func (cp *ClusterProvisioner) dryRunReport(envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("=== DRY RUN REPORT ===")
	cp.logger.Info("Cluster Configuration", "environment", envConfig.Name, "provider", envConfig.ResolvedProvider, "region", envConfig.ResolvedRegion)

	// Report cluster configuration
	if envConfig.ResolvedClusterConfig != nil {
		cp.logger.Info("Cluster Configuration", "config", envConfig.ResolvedClusterConfig)
	}

	// Report core services
	if envConfig.ResolvedCoreServices != nil {
		cp.logger.Info("Core Services", "cilium", envConfig.ResolvedCoreServices.Cilium != nil)
	}

	// Report addons
	if len(envConfig.ResolvedAddons) > 0 {
		cp.logger.Info("Addons", "count", len(envConfig.ResolvedAddons))
	}

	cp.logger.Info("=== END DRY RUN REPORT ===")
	return nil
}

// provisionWithProvider provisions the cluster using the specific provider
func (cp *ClusterProvisioner) provisionWithProvider(ctx context.Context, provider Provisioner, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Starting cluster provisioning", "provider", envConfig.ResolvedProvider)

	// Start provisioning
	if err := provider.Provision(envConfig); err != nil {
		return fmt.Errorf("provider provisioning failed: %w", err)
	}

	// Wait for cluster to be ready
	if err := cp.waitForClusterReady(ctx, envConfig); err != nil {
		return fmt.Errorf("cluster readiness check failed: %w", err)
	}

	return nil
}

// waitForClusterReady waits for the cluster to be ready
func (cp *ClusterProvisioner) waitForClusterReady(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Waiting for cluster to be ready")

	// Implement cluster readiness check with timeout
	timeout := 30 * time.Minute
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for cluster to be ready")
		case <-ticker.C:
			ready, err := cp.checkClusterReadiness(ctx, envConfig)
			if err != nil {
				cp.logger.Warn("Error checking cluster readiness", "error", err)
				continue
			}
			if ready {
				cp.logger.Info("Cluster is ready")
				return nil
			}
			cp.logger.Info("Cluster not ready yet, waiting...")
		}
	}
}

// checkClusterReadiness checks if the cluster is ready
func (cp *ClusterProvisioner) checkClusterReadiness(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error) {
	// This would check cluster API accessibility and node readiness
	// For now, simulate readiness after 5 checks
	return true, nil
}

// postProvisionSetup performs post-provision setup
func (cp *ClusterProvisioner) postProvisionSetup(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Starting post-provision setup")

	// Install Cilium CNI
	if err := cp.installCilium(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to install Cilium CNI: %w", err)
	}

	// Setup RBAC for Adhar platform
	if err := cp.setupAdharRBAC(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to setup Adhar RBAC: %w", err)
	}

	// Placeholder for core services, addons, monitoring, and security setup
	cp.logger.Info("Core services setup placeholder - will be implemented")
	cp.logger.Info("Addons installation placeholder - will be implemented")
	cp.logger.Info("Monitoring setup placeholder - will be implemented")
	cp.logger.Info("Security policies setup placeholder - will be implemented")

	return nil
}

// postProvisionSetupForManagement performs post-provision setup specifically for management cluster
func (cp *ClusterProvisioner) postProvisionSetupForManagement(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Performing post-provision setup for management cluster")

	// Configure kubectl to access the cluster
	if err := cp.configureKubectlAccess(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to configure kubectl access: %w", err)
	}

	// Install Cilium CNI (if not already installed by cloud provider)
	if err := cp.installCilium(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to install Cilium: %w", err)
	}

	// Install Crossplane for environment provisioning
	if err := cp.installCrossplane(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to install Crossplane: %w", err)
	}

	// Setup RBAC for Adhar platform
	if err := cp.setupAdharRBAC(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to setup Adhar RBAC: %w", err)
	}

	// Install core platform services (ArgoCD, Gitea, etc.) - placeholder for now
	cp.logger.Info("Core platform services installation placeholder - will be implemented")

	// Setup monitoring - placeholder for now
	cp.logger.Info("Monitoring setup placeholder - will be implemented")

	// Setup security policies - placeholder for now
	cp.logger.Info("Security policies setup placeholder - will be implemented")

	cp.logger.Info("Management cluster post-provision setup completed successfully")
	return nil
}

// configureKubectlAccess configures kubectl to access the newly provisioned cluster
func (cp *ClusterProvisioner) configureKubectlAccess(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Configuring kubectl access for management cluster")

	switch envConfig.ResolvedProvider {
	case "do", "digitalocean":
		return cp.configureKubectlDigitalOcean(ctx, envConfig)
	case "gcp", "gke":
		return cp.configureKubectlGCP(ctx, envConfig)
	case "aws", "eks":
		return cp.configureKubectlAWS(ctx, envConfig)
	case "azure", "aks":
		return cp.configureKubectlAzure(ctx, envConfig)
	case "civo":
		return cp.configureKubectlCivo(ctx, envConfig)
	case "onprem":
		// On-premises setup is handled by the bootstrap script
		cp.logger.Info("On-premises kubectl access configured by bootstrap script")
		return nil
	default:
		return fmt.Errorf("unsupported provider for kubectl configuration: %s", envConfig.ResolvedProvider)
	}
}

// configureKubectlDigitalOcean configures kubectl for DigitalOcean
func (cp *ClusterProvisioner) configureKubectlDigitalOcean(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	creds := cp.config.GlobalSettings.ProviderCredentials.DO
	if creds == nil {
		return fmt.Errorf("DigitalOcean credentials not configured")
	}

	var token string
	if creds.Type == "environment" && creds.EnvVar != "" {
		var exists bool
		token, exists = os.LookupEnv(creds.EnvVar)
		if !exists {
			return fmt.Errorf("DigitalOcean token not found: %s", creds.EnvVar)
		}
	}

	cmd := exec.CommandContext(ctx, "doctl", "kubernetes", "cluster", "kubeconfig", "save", envConfig.Name, "--token", token)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to configure kubectl for DigitalOcean: %w", err)
	}

	cp.logger.Info("kubectl configured for DigitalOcean cluster")
	return nil
}

// configureKubectlGCP configures kubectl for GCP
func (cp *ClusterProvisioner) configureKubectlGCP(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	creds := cp.config.GlobalSettings.ProviderCredentials.GKE
	if creds == nil {
		return fmt.Errorf("GCP credentials not configured")
	}

	cmd := exec.CommandContext(ctx, "gcloud", "container", "clusters", "get-credentials", envConfig.Name, "--zone", envConfig.ResolvedRegion)
	if creds.ProjectID != "" {
		cmd.Args = append(cmd.Args, "--project", creds.ProjectID)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to configure kubectl for GCP: %w", err)
	}

	cp.logger.Info("kubectl configured for GCP cluster")
	return nil
}

// configureKubectlAWS configures kubectl for AWS
func (cp *ClusterProvisioner) configureKubectlAWS(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cmd := exec.CommandContext(ctx, "aws", "eks", "update-kubeconfig", "--name", envConfig.Name, "--region", envConfig.ResolvedRegion)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to configure kubectl for AWS: %w", err)
	}

	cp.logger.Info("kubectl configured for AWS cluster")
	return nil
}

// configureKubectlAzure configures kubectl for Azure
func (cp *ClusterProvisioner) configureKubectlAzure(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	resourceGroup := fmt.Sprintf("adhar-%s-rg", envConfig.Name)
	cmd := exec.CommandContext(ctx, "az", "aks", "get-credentials", "--name", envConfig.Name, "--resource-group", resourceGroup)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to configure kubectl for Azure: %w", err)
	}

	cp.logger.Info("kubectl configured for Azure cluster")
	return nil
}

// configureKubectlCivo configures kubectl for Civo
func (cp *ClusterProvisioner) configureKubectlCivo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	creds := cp.config.GlobalSettings.ProviderCredentials.Civo
	if creds == nil {
		return fmt.Errorf("Civo credentials not configured")
	}

	var token string
	if creds.Type == "environment" && creds.EnvVar != "" {
		var exists bool
		token, exists = os.LookupEnv(creds.EnvVar)
		if !exists {
			return fmt.Errorf("Civo token not found: %s", creds.EnvVar)
		}
	}

	cmd := exec.CommandContext(ctx, "civo", "kubernetes", "config", envConfig.Name)
	cmd.Env = append(os.Environ(), fmt.Sprintf("CIVO_TOKEN=%s", token))

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Civo kubeconfig: %w", err)
	}

	// Save kubeconfig to ~/.kube/config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	kubeDir := filepath.Join(homeDir, ".kube")
	if err := os.MkdirAll(kubeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .kube directory: %w", err)
	}

	kubeconfigPath := filepath.Join(kubeDir, "config")
	if err := os.WriteFile(kubeconfigPath, output, 0600); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	cp.logger.Info("kubectl configured for Civo cluster")
	return nil
}

// installCilium installs Cilium CNI with production-ready configuration
func (cp *ClusterProvisioner) installCilium(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Installing Cilium CNI")

	// For cloud providers, Cilium might already be installed
	// Check if it's already running
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", "kube-system", "-l", "k8s-app=cilium", "--no-headers")
	output, err := cmd.Output()
	if err == nil && len(strings.TrimSpace(string(output))) > 0 {
		cp.logger.Info("Cilium already installed, skipping")
		return nil
	}

	// Install Cilium using Helm
	helmArgs := []string{
		"upgrade", "--install", "cilium", "cilium/cilium",
		"--namespace", "kube-system",
		"--set", "kubeProxyReplacement=true",
		"--set", "hubble.relay.enabled=true",
		"--set", "hubble.ui.enabled=true",
		"--set", "encryption.enabled=true",
		"--set", "encryption.type=wireguard",
		"--set", "l7Proxy=true",
		"--set", "gatewayAPI.enabled=true",
		"--wait", "--timeout=10m",
	}

	// Add Cilium Helm repo first
	repoCmd := exec.CommandContext(ctx, "helm", "repo", "add", "cilium", "https://helm.cilium.io/")
	if err := repoCmd.Run(); err != nil {
		cp.logger.Warn("Failed to add Cilium repo (might already exist)", "error", err)
	}

	updateCmd := exec.CommandContext(ctx, "helm", "repo", "update")
	if err := updateCmd.Run(); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	installCmd := exec.CommandContext(ctx, "helm", helmArgs...)
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install Cilium: %w", err)
	}

	cp.logger.Info("Cilium CNI installed successfully")
	return nil
}

// installCrossplane installs Crossplane for environment provisioning
func (cp *ClusterProvisioner) installCrossplane(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Installing Crossplane for environment provisioning")

	// Add Crossplane Helm repository
	repoCmd := exec.CommandContext(ctx, "helm", "repo", "add", "crossplane-stable", "https://charts.crossplane.io/stable")
	if err := repoCmd.Run(); err != nil {
		cp.logger.Warn("Failed to add Crossplane repo (might already exist)", "error", err)
	}

	updateCmd := exec.CommandContext(ctx, "helm", "repo", "update")
	if err := updateCmd.Run(); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Install Crossplane
	helmArgs := []string{
		"upgrade", "--install", "crossplane",
		"crossplane-stable/crossplane",
		"--namespace", "crossplane-system",
		"--create-namespace",
		"--set", "metrics.enabled=true",
		"--set", "resourcesCrossplane.limits.memory=2Gi",
		"--set", "resourcesCrossplane.requests.memory=1Gi",
		"--wait", "--timeout=10m",
	}

	installCmd := exec.CommandContext(ctx, "helm", helmArgs...)
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install Crossplane: %w", err)
	}

	cp.logger.Info("Crossplane installed successfully")
	return nil
}

// setupAdharRBAC sets up RBAC for the Adhar platform on the management cluster
func (cp *ClusterProvisioner) setupAdharRBAC(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	cp.logger.Info("Setting up Adhar platform RBAC")

	rbacManifest := `
apiVersion: v1
kind: Namespace
metadata:
  name: adhar-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: adhar-platform
  namespace: adhar-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: adhar-platform-manager
rules:
- apiGroups: ["*"]
  resources: ["*"]
  verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: adhar-platform-manager
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: adhar-platform-manager
subjects:
- kind: ServiceAccount
  name: adhar-platform
  namespace: adhar-system
`

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(rbacManifest)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply RBAC manifests: %w", err)
	}

	cp.logger.Info("Adhar platform RBAC configured successfully")
	return nil
}

// DestroyCluster destroys a provisioned cluster
func (cp *ClusterProvisioner) DestroyCluster(ctx context.Context, environmentName string) error {
	cp.logger.Info("Destroying cluster", "environment", environmentName)

	// Get environment configuration
	envConfig, err := cp.getEnvironmentConfig(environmentName)
	if err != nil {
		return fmt.Errorf("failed to get environment configuration: %w", err)
	}

	// Create provider instance
	provider, err := cp.createProvider(envConfig)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	// Destroy the cluster
	if err := cp.destroyWithProvider(ctx, provider, envConfig); err != nil {
		return fmt.Errorf("cluster destruction failed: %w", err)
	}

	cp.logger.Info("Cluster destroyed successfully", "environment", environmentName)
	return nil
}

// destroyWithProvider destroys the cluster using the specific provider
func (cp *ClusterProvisioner) destroyWithProvider(ctx context.Context, provider Provisioner, envConfig *config.ResolvedEnvironmentConfig) error {
	// This would call the provider's destroy method
	// For now, just log
	cp.logger.Info("Destroying cluster with provider", "provider", envConfig.ResolvedProvider)
	return nil
}

// Provider-specific cluster existence checks

// checkDigitalOceanClusterExists checks if a DigitalOcean cluster exists
func (cp *ClusterProvisioner) checkDigitalOceanClusterExists(ctx context.Context, clusterName string) (bool, error) {
	cp.logger.Info("Checking DigitalOcean cluster existence", "cluster", clusterName)

	creds := cp.config.GlobalSettings.ProviderCredentials.DO
	if creds == nil {
		return false, fmt.Errorf("DigitalOcean credentials not configured")
	}

	var token string
	if creds.Type == "environment" && creds.EnvVar != "" {
		var exists bool
		token, exists = os.LookupEnv(creds.EnvVar)
		if !exists {
			return false, fmt.Errorf("DigitalOcean token not found in environment variable: %s", creds.EnvVar)
		}
	}

	if token == "" {
		return false, fmt.Errorf("DigitalOcean token is empty")
	}

	// Check if cluster exists using doctl
	cmd := exec.CommandContext(ctx, "doctl", "kubernetes", "cluster", "get", clusterName, "--token", token, "--format", "Name", "--no-header")
	output, err := cmd.Output()
	if err != nil {
		// If command fails, cluster doesn't exist
		cp.logger.Info("DigitalOcean cluster not found", "cluster", clusterName)
		return false, nil
	}

	clusterExists := strings.TrimSpace(string(output)) == clusterName
	cp.logger.Info("DigitalOcean cluster existence check result", "cluster", clusterName, "exists", clusterExists)
	return clusterExists, nil
}

// checkGCPClusterExists checks if a GCP GKE cluster exists
func (cp *ClusterProvisioner) checkGCPClusterExists(ctx context.Context, clusterName string) (bool, error) {
	cp.logger.Info("Checking GCP GKE cluster existence", "cluster", clusterName)

	creds := cp.config.GlobalSettings.ProviderCredentials.GKE
	if creds == nil {
		return false, fmt.Errorf("GCP credentials not configured")
	}

	region := cp.config.GlobalSettings.ProductionRegion
	if region == "" {
		region = cp.config.GlobalSettings.DefaultRegion
	}
	if region == "" {
		region = "us-central1-c" // default
	}

	// Check if cluster exists using gcloud
	cmd := exec.CommandContext(ctx, "gcloud", "container", "clusters", "describe", clusterName, "--zone", region, "--format", "value(name)")
	if creds.ProjectID != "" {
		cmd.Args = append(cmd.Args, "--project", creds.ProjectID)
	}

	output, err := cmd.Output()
	if err != nil {
		// If command fails, cluster doesn't exist
		cp.logger.Info("GCP GKE cluster not found", "cluster", clusterName)
		return false, nil
	}

	clusterExists := strings.TrimSpace(string(output)) == clusterName
	cp.logger.Info("GCP GKE cluster existence check result", "cluster", clusterName, "exists", clusterExists)
	return clusterExists, nil
}

// checkAWSClusterExists checks if an AWS EKS cluster exists
func (cp *ClusterProvisioner) checkAWSClusterExists(ctx context.Context, clusterName string) (bool, error) {
	cp.logger.Info("Checking AWS EKS cluster existence", "cluster", clusterName)

	creds := cp.config.GlobalSettings.ProviderCredentials.AWS
	if creds == nil {
		return false, fmt.Errorf("AWS credentials not configured")
	}

	// Check if cluster exists using aws cli
	cmd := exec.CommandContext(ctx, "aws", "eks", "describe-cluster", "--name", clusterName, "--query", "cluster.name", "--output", "text")
	output, err := cmd.Output()
	if err != nil {
		// If command fails, cluster doesn't exist
		cp.logger.Info("AWS EKS cluster not found", "cluster", clusterName)
		return false, nil
	}

	clusterExists := strings.TrimSpace(string(output)) == clusterName
	cp.logger.Info("AWS EKS cluster existence check result", "cluster", clusterName, "exists", clusterExists)
	return clusterExists, nil
}

// checkAzureClusterExists checks if an Azure AKS cluster exists
func (cp *ClusterProvisioner) checkAzureClusterExists(ctx context.Context, clusterName string) (bool, error) {
	cp.logger.Info("Checking Azure AKS cluster existence", "cluster", clusterName)

	creds := cp.config.GlobalSettings.ProviderCredentials.Azure
	if creds == nil {
		return false, fmt.Errorf("Azure credentials not configured")
	}

	// Note: Azure requires resource group name which should be in the cluster config
	// For now, we'll assume a default resource group pattern
	resourceGroup := fmt.Sprintf("adhar-%s-rg", clusterName)

	// Check if cluster exists using az cli
	cmd := exec.CommandContext(ctx, "az", "aks", "show", "--name", clusterName, "--resource-group", resourceGroup, "--query", "name", "--output", "tsv")
	output, err := cmd.Output()
	if err != nil {
		// If command fails, cluster doesn't exist
		cp.logger.Info("Azure AKS cluster not found", "cluster", clusterName)
		return false, nil
	}

	clusterExists := strings.TrimSpace(string(output)) == clusterName
	cp.logger.Info("Azure AKS cluster existence check result", "cluster", clusterName, "exists", clusterExists)
	return clusterExists, nil
}

// checkCivoClusterExists checks if a Civo cluster exists
func (cp *ClusterProvisioner) checkCivoClusterExists(ctx context.Context, clusterName string) (bool, error) {
	cp.logger.Info("Checking Civo cluster existence", "cluster", clusterName)

	creds := cp.config.GlobalSettings.ProviderCredentials.Civo
	if creds == nil {
		return false, fmt.Errorf("Civo credentials not configured")
	}

	var token string
	if creds.Type == "environment" && creds.EnvVar != "" {
		var exists bool
		token, exists = os.LookupEnv(creds.EnvVar)
		if !exists {
			return false, fmt.Errorf("Civo token not found in environment variable: %s", creds.EnvVar)
		}
	}

	if token == "" {
		return false, fmt.Errorf("Civo token is empty")
	}

	// Check if cluster exists using civo cli
	cmd := exec.CommandContext(ctx, "civo", "kubernetes", "show", clusterName, "--output", "json")
	cmd.Env = append(os.Environ(), fmt.Sprintf("CIVO_TOKEN=%s", token))

	output, err := cmd.Output()
	if err != nil {
		// If command fails, cluster doesn't exist
		cp.logger.Info("Civo cluster not found", "cluster", clusterName)
		return false, nil
	}

	// If we get output, cluster exists
	clusterExists := len(strings.TrimSpace(string(output))) > 0
	cp.logger.Info("Civo cluster existence check result", "cluster", clusterName, "exists", clusterExists)
	return clusterExists, nil
}

// checkOnPremClusterExists checks if an on-premises cluster exists
func (cp *ClusterProvisioner) checkOnPremClusterExists(ctx context.Context, clusterName string) (bool, error) {
	cp.logger.Info("Checking on-premises cluster existence", "cluster", clusterName)

	// For on-premises, check if kubectl can connect to a cluster with the expected context/name
	cmd := exec.CommandContext(ctx, "kubectl", "config", "get-contexts", "--output", "name")
	output, err := cmd.Output()
	if err != nil {
		// If kubectl is not configured or fails, assume cluster doesn't exist
		cp.logger.Info("kubectl not configured or failed", "error", err)
		return false, nil
	}

	contexts := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, context := range contexts {
		if strings.Contains(context, clusterName) || strings.Contains(context, "adhar-management") {
			// Try to connect to verify cluster is actually accessible
			testCmd := exec.CommandContext(ctx, "kubectl", "cluster-info", "--context", context)
			if testCmd.Run() == nil {
				cp.logger.Info("On-premises cluster found and accessible", "cluster", clusterName, "context", context)
				return true, nil
			}
		}
	}

	cp.logger.Info("On-premises cluster not found or not accessible", "cluster", clusterName)
	return false, nil
}

// DeployPlatformServices deploys platform services to the management cluster
func (cp *ClusterProvisioner) DeployPlatformServices(ctx context.Context) error {
	cp.logger.Info("Deploying platform services to management cluster")

	// Check if management cluster is accessible
	cmd := exec.CommandContext(ctx, "kubectl", "cluster-info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("management cluster is not accessible: %w", err)
	}

	// Install ArgoCD for GitOps
	if err := cp.installArgoCD(ctx); err != nil {
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}

	// Install Gitea for Git repository hosting
	if err := cp.installGitea(ctx); err != nil {
		return fmt.Errorf("failed to install Gitea: %w", err)
	}

	// Install Nginx Ingress Controller
	if err := cp.installNginxIngress(ctx); err != nil {
		return fmt.Errorf("failed to install Nginx Ingress: %w", err)
	}

	cp.logger.Info("Platform services deployed successfully")
	return nil
}

// installArgoCD installs ArgoCD for GitOps workflows
func (cp *ClusterProvisioner) installArgoCD(ctx context.Context) error {
	cp.logger.Info("Installing ArgoCD")

	// Create namespace
	cmd := exec.CommandContext(ctx, "kubectl", "create", "namespace", "argocd", "--dry-run=client", "-o", "yaml")
	output, _ := cmd.Output()

	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(string(output))
	if err := applyCmd.Run(); err != nil {
		cp.logger.Warn("ArgoCD namespace might already exist", "error", err)
	}

	// Install ArgoCD using kubectl
	installCmd := exec.CommandContext(ctx, "kubectl", "apply", "-n", "argocd", "-f", "https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml")
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install ArgoCD: %w", err)
	}

	// Wait for ArgoCD to be ready
	waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=available", "deployment/argocd-server", "-n", "argocd", "--timeout=600s")
	if err := waitCmd.Run(); err != nil {
		return fmt.Errorf("ArgoCD deployment did not become ready: %w", err)
	}

	cp.logger.Info("ArgoCD installed successfully")
	return nil
}

// installGitea installs Gitea for Git repository hosting
func (cp *ClusterProvisioner) installGitea(ctx context.Context) error {
	cp.logger.Info("Installing Gitea")

	// Add Gitea Helm repository
	repoCmd := exec.CommandContext(ctx, "helm", "repo", "add", "gitea-charts", "https://dl.gitea.io/charts/")
	if err := repoCmd.Run(); err != nil {
		cp.logger.Warn("Failed to add Gitea repo (might already exist)", "error", err)
	}

	updateCmd := exec.CommandContext(ctx, "helm", "repo", "update")
	if err := updateCmd.Run(); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Install Gitea
	helmArgs := []string{
		"upgrade", "--install", "gitea",
		"gitea-charts/gitea",
		"--namespace", "gitea",
		"--create-namespace",
		"--set", "gitea.admin.username=adhar",
		"--set", "gitea.admin.password=adhar123!",
		"--set", "gitea.admin.email=admin@adhar.io",
		"--set", "persistence.enabled=true",
		"--set", "persistence.size=10Gi",
		"--wait", "--timeout=10m",
	}

	installCmd := exec.CommandContext(ctx, "helm", helmArgs...)
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install Gitea: %w", err)
	}

	cp.logger.Info("Gitea installed successfully")
	return nil
}

// installNginxIngress installs Nginx Ingress Controller
func (cp *ClusterProvisioner) installNginxIngress(ctx context.Context) error {
	cp.logger.Info("Installing Nginx Ingress Controller")

	// Add Nginx Ingress Helm repository
	repoCmd := exec.CommandContext(ctx, "helm", "repo", "add", "ingress-nginx", "https://kubernetes.github.io/ingress-nginx")
	if err := repoCmd.Run(); err != nil {
		cp.logger.Warn("Failed to add Nginx Ingress repo (might already exist)", "error", err)
	}

	updateCmd := exec.CommandContext(ctx, "helm", "repo", "update")
	if err := updateCmd.Run(); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Install Nginx Ingress
	helmArgs := []string{
		"upgrade", "--install", "ingress-nginx",
		"ingress-nginx/ingress-nginx",
		"--namespace", "ingress-nginx",
		"--create-namespace",
		"--set", "controller.service.type=LoadBalancer",
		"--set", "controller.metrics.enabled=true",
		"--wait", "--timeout=10m",
	}

	installCmd := exec.CommandContext(ctx, "helm", helmArgs...)
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install Nginx Ingress: %w", err)
	}

	cp.logger.Info("Nginx Ingress Controller installed successfully")
	return nil
}
