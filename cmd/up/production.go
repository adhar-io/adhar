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

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"

	// Import providers so their init() functions register with DefaultFactory
	_ "adhar-io/adhar/platform/providers/aws"
	_ "adhar-io/adhar/platform/providers/azure"
	_ "adhar-io/adhar/platform/providers/civo"
	_ "adhar-io/adhar/platform/providers/custom"
	_ "adhar-io/adhar/platform/providers/digitalocean"
	_ "adhar-io/adhar/platform/providers/gcp"
	_ "adhar-io/adhar/platform/providers/kind"

	"github.com/spf13/cobra"
)

// createProductionCluster handles production cluster provisioning using the new ProviderManager
func createProductionCluster(ctx context.Context, cmd *cobra.Command, args []string, ctxCancel context.CancelFunc) error {
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

	result, err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts)
	if err != nil {
		logger.Error("Environment provisioning failed", err, map[string]interface{}{
			"environment": environment,
			"provider":    envConfig.ResolvedProvider,
		})
		return fmt.Errorf("failed to provision environment %s: %w", environment, err)
	}

	// Bootstrap the platform (foundation + GitOps stack + in-cluster controller
	// manager) on the freshly provisioned cluster — same flow as local, sized
	// by enableHAMode.
	if result != nil {
		if err := bootstrapPlatformOnCluster(ctx, result, envConfig, cfg); err != nil {
			return fmt.Errorf("failed to bootstrap platform on environment %s: %w", environment, err)
		}
	}

	log.FinishOperation("Environment Provisioning", fmt.Sprintf("%s environment ready", environment))

	// Print success message
	printProductionSuccessMsg(environment)
	return nil
}

// loadConfigFromFile loads configuration from a specific file path using Viper
// (which understands mapstructure tags used by the Config struct)
func loadConfigFromFile(configPath string) (*config.Config, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	// Resolve environment configurations (merge templates, assign providers)
	if err := cfg.ResolveEnvironments(); err != nil {
		return nil, fmt.Errorf("failed to resolve environments: %w", err)
	}

	return cfg, nil
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
	fmt.Printf("  ✓ Core platform services (ArgoCD, Gitea, Cilium Gateway)\n")
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

		result, err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts)
		if err != nil {
			fmt.Printf("  ❌ Failed to provision %s: %v\n", envName, err)
			continue
		}
		if result != nil {
			if err := bootstrapPlatformOnCluster(ctx, result, envConfig, cfg); err != nil {
				fmt.Printf("  ❌ Failed to bootstrap platform on %s: %v\n", envName, err)
				continue
			}
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
		fmt.Printf("  Gateway:   %v\n", envConfig.ResolvedCoreServices.Gateway != nil)
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
func validateEnvironmentExists(configPath, envName string) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config file: %w", err)
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
