package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	apiv1alpha1 "adhar-io/adhar/api/v1alpha1"
)

// Config is the root configuration structure.
type Config struct {
	GlobalSettings       GlobalSettings                 `mapstructure:"globalSettings" yaml:"globalSettings"`
	EnvironmentTemplates map[string]EnvironmentTemplate `mapstructure:"environmentTemplates" yaml:"environmentTemplates"`
	Environments         map[string]EnvironmentConfig   `mapstructure:"environments" yaml:"environments"`

	ResolvedEnvironments map[string]*ResolvedEnvironmentConfig `mapstructure:"-" yaml:"-"` // Ignored by Viper/YAML
}

// GlobalSettings defines platform-wide configurations.
type GlobalSettings struct {
	AdharContext     string `mapstructure:"adharContext" yaml:"adharContext"`
	DefaultHost      string `mapstructure:"defaultHost,omitempty" yaml:"defaultHost,omitempty"`
	DefaultHttpPort  int    `mapstructure:"defaultHttpPort,omitempty" yaml:"defaultHttpPort,omitempty"`
	DefaultHttpsPort int    `mapstructure:"defaultHttpsPort,omitempty" yaml:"defaultHttpsPort,omitempty"`
	DefaultRegion    string `mapstructure:"defaultRegion,omitempty" yaml:"defaultRegion,omitempty"`

	// Dual-provider configuration
	ProductionProvider    apiv1alpha1.EnvironmentProvider `mapstructure:"productionProvider" yaml:"productionProvider"`
	NonProductionProvider apiv1alpha1.EnvironmentProvider `mapstructure:"nonProductionProvider" yaml:"nonProductionProvider"`
	ProductionRegion      string                          `mapstructure:"productionRegion,omitempty" yaml:"productionRegion,omitempty"`
	NonProductionRegion   string                          `mapstructure:"nonProductionRegion,omitempty" yaml:"nonProductionRegion,omitempty"`

	ProviderCredentials ProviderCredentialsConfig `mapstructure:"providerCredentials" yaml:"providerCredentials"`
}

// ProviderCredentialsConfig specifies how to load credentials for different providers.
type ProviderCredentialsConfig struct {
	DO    *CredentialSource `mapstructure:"do,omitempty" yaml:"do,omitempty"`
	GKE   *CredentialSource `mapstructure:"gke,omitempty" yaml:"gke,omitempty"`
	AWS   *CredentialSource `mapstructure:"aws,omitempty" yaml:"aws,omitempty"`
	Azure *CredentialSource `mapstructure:"azure,omitempty" yaml:"azure,omitempty"`
	Civo  *CredentialSource `mapstructure:"civo,omitempty" yaml:"civo,omitempty"`
}

// CredentialSource defines how to obtain credentials.
type CredentialSource struct {
	Type      string `mapstructure:"type" yaml:"type"`
	EnvVar    string `mapstructure:"envVar,omitempty" yaml:"envVar,omitempty"`
	Path      string `mapstructure:"path,omitempty" yaml:"path,omitempty"`
	ProjectID string `mapstructure:"projectID,omitempty" yaml:"projectID,omitempty"` // Added for GCP credentials
}

// ClusterConfig defines a concrete type for cluster configuration.
type ClusterConfig struct {
	Key   string `mapstructure:"key" yaml:"key"`
	Value string `mapstructure:"value" yaml:"value"`
}

// EnvironmentTemplate defines reusable configurations for environment types.
type EnvironmentTemplate struct {
	ClusterConfig []ClusterConfig               `mapstructure:"clusterConfig,omitempty" yaml:"clusterConfig,omitempty"`
	CoreServices  *apiv1alpha1.CoreServicesSpec `mapstructure:"coreServices,omitempty" yaml:"coreServices,omitempty"`
	Addons        []apiv1alpha1.AddonSpec       `mapstructure:"addons,omitempty" yaml:"addons,omitempty"`
}

// EnvironmentConfig defines the configuration specific to a named environment instance.
type EnvironmentConfig struct {
	Template      string                          `mapstructure:"template" yaml:"template"`
	Type          EnvironmentType                 `mapstructure:"type,omitempty" yaml:"type,omitempty"`
	Provider      apiv1alpha1.EnvironmentProvider `mapstructure:"provider,omitempty" yaml:"provider,omitempty"`
	Region        string                          `mapstructure:"region,omitempty" yaml:"region,omitempty"`
	ClusterConfig []ClusterConfig                 `mapstructure:"clusterConfig,omitempty" yaml:"clusterConfig,omitempty"`
	CoreServices  *apiv1alpha1.CoreServicesSpec   `mapstructure:"coreServices,omitempty" yaml:"coreServices,omitempty"`
	Addons        []apiv1alpha1.AddonSpec         `mapstructure:"addons,omitempty" yaml:"addons,omitempty"`
}

// ResolvedEnvironmentConfig holds the final, merged configuration for a specific environment.
type ResolvedEnvironmentConfig struct {
	Name string

	ResolvedType          EnvironmentType
	ResolvedProvider      apiv1alpha1.EnvironmentProvider
	ResolvedRegion        string
	ResolvedClusterConfig []ClusterConfig
	ResolvedCoreServices  *apiv1alpha1.CoreServicesSpec
	ResolvedAddons        []apiv1alpha1.AddonSpec

	GlobalSettings *GlobalSettings
}

// EnvironmentType defines whether an environment is production or non-production
type EnvironmentType string

const (
	EnvironmentTypeProduction    EnvironmentType = "production"
	EnvironmentTypeNonProduction EnvironmentType = "non-production"
)

const (
	configFileName = "adhar-config"
	configFileType = "yaml"
	configKey      = "adhar"
	schemaFileName = "adhar-config.schema.json"
)

// LoadConfig reads the adhar-config.yaml file and parses it into a Config struct
func LoadConfig() (*Config, error) {
	configPath := "adhar-config.yaml" // Default path
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("configuration file not found at %s", configPath)
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open configuration file: %w", err)
	}
	defer file.Close()

	var cfg Config
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse configuration file: %w", err)
	}

	return &cfg, nil
}

// resolveEnvironments merges template defaults into specific environment configurations.
func (c *Config) resolveEnvironments() error {
	c.ResolvedEnvironments = make(map[string]*ResolvedEnvironmentConfig)

	for envName, envConf := range c.Environments {
		var template EnvironmentTemplate
		if envConf.Template != "" {
			var ok bool
			template, ok = c.EnvironmentTemplates[envConf.Template]
			if !ok {
				return fmt.Errorf("environment template '%s' referenced by environment '%s' not found", envConf.Template, envName)
			}
		}

		resolved := &ResolvedEnvironmentConfig{
			Name:           envName,
			GlobalSettings: &c.GlobalSettings,
		}

		resolved.ResolvedProvider = envConf.Provider
		if resolved.ResolvedProvider == "" {
			return fmt.Errorf("provider for environment '%s' is missing", envName)
		}

		resolved.ResolvedRegion = envConf.Region
		if resolved.ResolvedRegion == "" {
			resolved.ResolvedRegion = c.GlobalSettings.DefaultRegion
		}
		if resolved.ResolvedRegion == "" && resolved.ResolvedProvider != apiv1alpha1.ProviderKind {
			return fmt.Errorf("region for environment '%s' could not be resolved (missing in environment and global defaults)", envName)
		}

		resolved.ResolvedClusterConfig = append(template.ClusterConfig, envConf.ClusterConfig...)

		resolved.ResolvedCoreServices = mergeCoreServices(template.CoreServices, envConf.CoreServices)

		resolved.ResolvedAddons = mergeAddons(template.Addons, envConf.Addons)

		c.ResolvedEnvironments[envName] = resolved
	}
	return nil
}

// validateConfig performs Go-level validation after resolving templates.
func (c *Config) validateConfig() error {
	if c.GlobalSettings.AdharContext == "" {
		fmt.Println("Warning: globalSettings.adharContext is not set. Operations involving cloud providers will likely fail.")
	}

	// Verify that at least one environment exists
	if len(c.ResolvedEnvironments) == 0 {
		return fmt.Errorf("no environments defined in configuration")
	}

	for envName, resolvedEnv := range c.ResolvedEnvironments {
		// Validate provider-specific configurations
		switch resolvedEnv.ResolvedProvider {
		case apiv1alpha1.ProviderDO, apiv1alpha1.ProviderGKE, apiv1alpha1.ProviderAWS, apiv1alpha1.ProviderAzure, apiv1alpha1.ProviderCivo:
			if resolvedEnv.ResolvedRegion == "" {
				return fmt.Errorf("environment '%s' uses cloud provider '%s' but region is not resolved", envName, resolvedEnv.ResolvedProvider)
			}

			// More systematic credential validation
			var credProvider *CredentialSource
			var providerName string

			switch resolvedEnv.ResolvedProvider {
			case apiv1alpha1.ProviderDO:
				credProvider = c.GlobalSettings.ProviderCredentials.DO
				providerName = "do"
			case apiv1alpha1.ProviderGKE:
				credProvider = c.GlobalSettings.ProviderCredentials.GKE
				providerName = "gke"
			case apiv1alpha1.ProviderAWS:
				credProvider = c.GlobalSettings.ProviderCredentials.AWS
				providerName = "aws"
			case apiv1alpha1.ProviderAzure:
				credProvider = c.GlobalSettings.ProviderCredentials.Azure
				providerName = "azure"
			case apiv1alpha1.ProviderCivo:
				credProvider = c.GlobalSettings.ProviderCredentials.Civo
				providerName = "civo"
			}

			if credProvider == nil {
				return fmt.Errorf("environment '%s' uses provider '%s', but no credential source is configured in globalSettings.providerCredentials.%s",
					envName, resolvedEnv.ResolvedProvider, providerName)
			}

			// Validate credential configuration based on type
			switch credProvider.Type {
			case "file":
				if credProvider.Path == "" {
					return fmt.Errorf("environment '%s' provider '%s' credential type is 'file' but path is missing",
						envName, resolvedEnv.ResolvedProvider)
				}
				// Expand environment variables in file path
				expandedPath := os.ExpandEnv(credProvider.Path)
				if _, err := os.Stat(expandedPath); os.IsNotExist(err) {
					return fmt.Errorf("environment '%s' provider '%s' credential file '%s' does not exist",
						envName, resolvedEnv.ResolvedProvider, expandedPath)
				}
			case "environment":
				if credProvider.EnvVar == "" {
					return fmt.Errorf("environment '%s' provider '%s' credential type is 'environment' but envVar is missing",
						envName, resolvedEnv.ResolvedProvider)
				}
				// We don't check if the environment variable exists here as it might be set later
			default:
				return fmt.Errorf("environment '%s' provider '%s' has invalid credential type '%s' (must be 'file' or 'environment')",
					envName, resolvedEnv.ResolvedProvider, credProvider.Type)
			}

		case apiv1alpha1.ProviderKind:
			// For kind provider, ensure cluster name is set
			if len(resolvedEnv.ResolvedClusterConfig) == 0 || resolvedEnv.ResolvedClusterConfig[0].Value == "" {
				resolvedEnv.ResolvedClusterConfig = append(resolvedEnv.ResolvedClusterConfig, ClusterConfig{
					Key:   "name",
					Value: "adhar-" + envName,
				})
				fmt.Printf("Info: Setting default cluster name 'adhar-%s' for Kind environment '%s'\n", envName, envName)
			}
		default:
			return fmt.Errorf("environment '%s' has an unknown provider '%s'", envName, resolvedEnv.ResolvedProvider)
		}

		// Validate core services if specified
		if cs := resolvedEnv.ResolvedCoreServices; cs != nil {
			if err := validateHelmConfig("cilium", cs.Cilium); err != nil {
				return fmt.Errorf("environment '%s': %w", envName, err)
			}
			if err := validateHelmConfig("nginx", cs.Nginx); err != nil {
				return fmt.Errorf("environment '%s': %w", envName, err)
			}
			if err := validateHelmConfig("gitea", cs.Gitea); err != nil {
				return fmt.Errorf("environment '%s': %w", envName, err)
			}
			if err := validateHelmConfig("argocd", cs.ArgoCD); err != nil {
				return fmt.Errorf("environment '%s': %w", envName, err)
			}
		}

		// Validate addon specifications
		for _, addon := range resolvedEnv.ResolvedAddons {
			if addon.Name == "" {
				return fmt.Errorf("environment '%s' has an addon with a missing name", envName)
			}
			if err := validateChartSpec(fmt.Sprintf("addon '%s'", addon.Name), addon.Chart); err != nil {
				return fmt.Errorf("environment '%s': %w", envName, err)
			}
		}
	}

	return nil
}

// FindEnvironment retrieves the resolved configuration for a specific environment name.
func (c *Config) FindEnvironment(name string) (*ResolvedEnvironmentConfig, error) {
	resolvedEnv, ok := c.ResolvedEnvironments[name]
	if !ok {
		return nil, fmt.Errorf("environment '%s' not found in configuration", name)
	}
	return resolvedEnv, nil
}

func validateHelmConfig(serviceName string, config *apiv1alpha1.HelmChartConfig) error {
	if config == nil {
		return nil
	}
	return validateChartSpec(fmt.Sprintf("core service '%s'", serviceName), config.Chart)
}

func validateChartSpec(context string, chart apiv1alpha1.ChartSpec) error {
	if chart.Repository == "" {
		return fmt.Errorf("%s: chart repository is required", context)
	}
	if chart.Name == "" {
		return fmt.Errorf("%s: chart name is required", context)
	}
	if chart.Version == "" {
		return fmt.Errorf("%s: chart version is required", context)
	}
	return nil
}

// Correct handling of Values as []ValuesConfig
func mergeCoreServices(template, env *apiv1alpha1.CoreServicesSpec) *apiv1alpha1.CoreServicesSpec {
	if template == nil && env == nil {
		return nil
	}
	merged := &apiv1alpha1.CoreServicesSpec{}

	if template != nil {
		*merged = *template
	}

	if env != nil {
		if env.Cilium != nil {
			merged.Cilium = mergeHelmConfig(template.Cilium, env.Cilium)
		}
		if env.Nginx != nil {
			merged.Nginx = mergeHelmConfig(template.Nginx, env.Nginx)
		}
		if env.Gitea != nil {
			merged.Gitea = mergeHelmConfig(template.Gitea, env.Gitea)
		}
		if env.ArgoCD != nil {
			merged.ArgoCD = mergeHelmConfig(template.ArgoCD, env.ArgoCD)
		}
		if env.Values != nil {
			merged.Values = mergeJSONValues(template.Values, env.Values)
		}
	}

	return merged
}

func mergeHelmConfig(template, env *apiv1alpha1.HelmChartConfig) *apiv1alpha1.HelmChartConfig {
	if env == nil {
		return template
	}
	if template == nil {
		return env
	}

	merged := &apiv1alpha1.HelmChartConfig{
		Chart: apiv1alpha1.ChartSpec{
			Repository: env.Chart.Repository,
			Name:       env.Chart.Name,
			Version:    env.Chart.Version,
		},
		Values: mergeJSONValues(template.Values, env.Values),
	}

	if merged.Chart.Repository == "" {
		merged.Chart.Repository = template.Chart.Repository
	}
	if merged.Chart.Name == "" {
		merged.Chart.Name = template.Chart.Name
	}
	if merged.Chart.Version == "" {
		merged.Chart.Version = template.Chart.Version
	}

	return merged
}

// Update mergeJSONValues to handle []ValuesConfig instead of map[string]interface{}
func mergeJSONValues(templateValues, envValues []apiv1alpha1.ValuesConfig) []apiv1alpha1.ValuesConfig {
	tempMap := make(map[string]string)

	for _, v := range templateValues {
		tempMap[v.Key] = v.Value
	}

	for _, v := range envValues {
		tempMap[v.Key] = v.Value
	}

	result := []apiv1alpha1.ValuesConfig{}
	for k, v := range tempMap {
		result = append(result, apiv1alpha1.ValuesConfig{Key: k, Value: v})
	}

	return result
}

func mergeAddons(template, env []apiv1alpha1.AddonSpec) []apiv1alpha1.AddonSpec {
	merged := []apiv1alpha1.AddonSpec{}
	envAddonMap := make(map[string]apiv1alpha1.AddonSpec)
	for _, addon := range env {
		if addon.Name != "" {
			envAddonMap[addon.Name] = addon
		}
	}

	for _, tAddon := range template {
		if eAddon, exists := envAddonMap[tAddon.Name]; exists {
			mergedAddon := eAddon
			merged = append(merged, mergedAddon)
			delete(envAddonMap, tAddon.Name)
		} else {
			merged = append(merged, tAddon)
		}
	}

	for _, eAddon := range envAddonMap {
		merged = append(merged, eAddon)
	}

	return merged
}
