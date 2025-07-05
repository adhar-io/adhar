package build

import (
	"context"
	"fmt"

	"adhar-io/adhar/platform/config"

	"github.com/sirupsen/logrus"
)

// ProvisionOptions contains options for provisioning operations
type ProvisionOptions struct {
	DryRun bool
	Force  bool
}

// Provider defines the unified interface for provisioning both local and cloud environments
type Provider interface {
	// Core provisioning methods
	Provision(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error
	Destroy(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error
	Exists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error)

	// Platform service management
	InstallPlatformServices(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error
	ValidateCluster(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error

	// Configuration and status
	GetClusterInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error)
	GetKubeConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (string, error)
}

// ClusterInfo represents information about a provisioned cluster
type ClusterInfo struct {
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Region    string            `json:"region"`
	Status    string            `json:"status"`
	NodeCount int               `json:"node_count"`
	Version   string            `json:"version"`
	Endpoint  string            `json:"endpoint"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ProviderManager manages different providers and provides a unified interface
type ProviderManager struct {
	logger         *logrus.Logger
	templateEngine *TemplateEngine
}

// NewProviderManager creates a new provider manager
func NewProviderManager(logger *logrus.Logger, templateEngine *TemplateEngine) *ProviderManager {
	return &ProviderManager{
		logger:         logger,
		templateEngine: templateEngine,
	}
}

// GetProvider returns the appropriate provider based on the environment configuration
func (pm *ProviderManager) GetProvider(envConfig *config.ResolvedEnvironmentConfig) (Provider, error) {
	switch envConfig.ResolvedProvider {
	case "kind":
		return NewKindProvider(envConfig, pm.logger, pm.templateEngine)
	case "do":
		return NewDigitalOceanProvider(envConfig, pm.logger, pm.templateEngine)
	case "gke":
		return NewGCPProvider(envConfig, pm.logger, pm.templateEngine)
	case "aws":
		return NewAWSProvider(envConfig, pm.logger, pm.templateEngine)
	case "azure":
		return NewAzureProvider(envConfig, pm.logger, pm.templateEngine)
	case "civo":
		return NewCivoProvider(envConfig, pm.logger, pm.templateEngine)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", envConfig.ResolvedProvider)
	}
}

// ProvisionEnvironment provisions an environment using the appropriate provider
func (pm *ProviderManager) ProvisionEnvironment(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	// For dry-run mode, create a lightweight provider that just logs what would happen
	if opts.DryRun {
		pm.logger.Info("DRY-RUN: Would provision environment", "name", envConfig.Name, "provider", envConfig.ResolvedProvider, "region", envConfig.ResolvedRegion)
		pm.logger.Info("DRY-RUN: Would check if cluster exists", "name", envConfig.Name)
		pm.logger.Info("DRY-RUN: Would create cluster (assuming it doesn't exist)", "name", envConfig.Name)
		pm.logger.Info("DRY-RUN: Would validate cluster", "name", envConfig.Name)
		pm.logger.Info("DRY-RUN: Would install platform services", "name", envConfig.Name)
		pm.logger.Info("DRY-RUN: Environment would be provisioned successfully", "name", envConfig.Name)
		return nil
	}

	provider, err := pm.GetProvider(envConfig)
	if err != nil {
		return fmt.Errorf("failed to get provider: %w", err)
	}

	pm.logger.Info("Starting environment provisioning", "name", envConfig.Name, "provider", envConfig.ResolvedProvider)

	// Check if cluster already exists
	exists, err := provider.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if exists {
		pm.logger.Info("Cluster already exists", "name", envConfig.Name)
	} else {
		// Provision the cluster
		if err := provider.Provision(ctx, envConfig, opts); err != nil {
			return fmt.Errorf("failed to provision cluster: %w", err)
		}
	}

	// Skip platform services installation if dry-run
	if opts.DryRun {
		pm.logger.Info("Dry-run mode: skipping cluster validation and platform services installation")
		return nil
	}

	// Validate cluster
	if err := provider.ValidateCluster(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to validate cluster: %w", err)
	}

	// Install platform services
	if err := provider.InstallPlatformServices(ctx, envConfig); err != nil {
		return fmt.Errorf("failed to install platform services: %w", err)
	}

	pm.logger.Info(fmt.Sprintf("✅ Environment '%s' provisioning completed successfully", envConfig.Name))
	return nil
}

// DestroyEnvironment destroys an environment using the appropriate provider
func (pm *ProviderManager) DestroyEnvironment(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	provider, err := pm.GetProvider(envConfig)
	if err != nil {
		return fmt.Errorf("failed to get provider: %w", err)
	}

	pm.logger.Info("Starting environment destruction", "name", envConfig.Name, "provider", envConfig.ResolvedProvider)

	if err := provider.Destroy(ctx, envConfig, opts); err != nil {
		return fmt.Errorf("failed to destroy cluster: %w", err)
	}

	pm.logger.Info("Environment destruction completed successfully", "name", envConfig.Name)
	return nil
}

// GetEnvironmentInfo gets information about an environment
func (pm *ProviderManager) GetEnvironmentInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error) {
	provider, err := pm.GetProvider(envConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider: %w", err)
	}

	return provider.GetClusterInfo(ctx, envConfig)
}
