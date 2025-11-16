package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config represents the main Adhar configuration
type Config struct {
	GlobalSettings       GlobalSettingsConfig                  `mapstructure:"globalSettings" json:"globalSettings"`
	Providers            map[string]ConfigProviderConfig       `mapstructure:"providers" json:"providers"`
	EnvironmentTemplates map[string]EnvironmentTemplateConfig  `mapstructure:"environmentTemplates" json:"environmentTemplates"`
	Environments         map[string]EnvironmentConfig          `mapstructure:"environments" json:"environments"`
	ResolvedEnvironments map[string]*ResolvedEnvironmentConfig `json:"resolvedEnvironments,omitempty"`
}

// GlobalSettingsConfig holds global settings
type GlobalSettingsConfig struct {
	AdharContext          string `mapstructure:"adharContext" json:"adharContext"`
	DefaultHost           string `mapstructure:"defaultHost" json:"defaultHost"`
	DefaultHttpPort       int    `mapstructure:"defaultHttpPort" json:"defaultHttpPort"`
	DefaultHttpsPort      int    `mapstructure:"defaultHttpsPort" json:"defaultHttpsPort"`
	EnableHAMode          bool   `mapstructure:"enableHAMode" json:"enableHAMode"`
	Email                 string `mapstructure:"email" json:"email"`
	ProductionProvider    string `mapstructure:"productionProvider" json:"productionProvider"`
	NonProductionProvider string `mapstructure:"nonProductionProvider" json:"nonProductionProvider"`
}

// ConfigProviderConfig holds provider-specific configuration
type ConfigProviderConfig struct {
	Type    string `mapstructure:"type" json:"type"`
	Region  string `mapstructure:"region" json:"region"`
	Primary bool   `mapstructure:"primary" json:"primary"`

	// Common authentication fields
	CredentialsFile string `mapstructure:"credentials_file" json:"credentialsFile"`
	UseEnvironment  bool   `mapstructure:"useEnvironment" json:"useEnvironment"`

	// AWS authentication
	AccessKeyID     string `mapstructure:"accessKeyId" json:"accessKeyId"`
	SecretAccessKey string `mapstructure:"secretAccessKey" json:"secretAccessKey"`
	SessionToken    string `mapstructure:"sessionToken" json:"sessionToken"`
	Profile         string `mapstructure:"profile" json:"profile"`
	UseInstanceRole bool   `mapstructure:"useInstanceRole" json:"useInstanceRole"`

	// Azure authentication
	ClientID           string `mapstructure:"clientId" json:"clientId"`
	ClientSecret       string `mapstructure:"clientSecret" json:"clientSecret"`
	TenantID           string `mapstructure:"tenantId" json:"tenantId"`
	CertificatePath    string `mapstructure:"certificatePath" json:"certificatePath"`
	UseManagedIdentity bool   `mapstructure:"useManagedIdentity" json:"useManagedIdentity"`
	UseAzureCLI        bool   `mapstructure:"useAzureCLI" json:"useAzureCLI"`

	// GCP authentication
	ProjectID                 string `mapstructure:"projectId" json:"projectId"`
	ServiceAccountKeyFile     string `mapstructure:"serviceAccountKeyFile" json:"serviceAccountKeyFile"`
	ServiceAccountKey         string `mapstructure:"serviceAccountKey" json:"serviceAccountKey"`
	ImpersonateServiceAccount string `mapstructure:"impersonateServiceAccount" json:"impersonateServiceAccount"`
	UseApplicationDefault     bool   `mapstructure:"useApplicationDefault" json:"useApplicationDefault"`
	UseComputeMetadata        bool   `mapstructure:"useComputeMetadata" json:"useComputeMetadata"`

	// DigitalOcean & Civo authentication (both use token)
	Token string `mapstructure:"token" json:"token"`

	Config map[string]interface{} `mapstructure:"config" json:"config"`
}

// EnvironmentConfig holds environment-specific configuration
type EnvironmentConfig struct {
	Type          string                   `mapstructure:"type" json:"type"`
	Provider      string                   `mapstructure:"provider" json:"provider,omitempty"`
	Region        string                   `mapstructure:"region" json:"region,omitempty"`
	Template      string                   `mapstructure:"template" json:"template"`
	ClusterConfig []KeyValueConfig         `mapstructure:"clusterConfig" json:"clusterConfig"`
	CoreServices  map[string]ServiceConfig `mapstructure:"coreServices" json:"coreServices,omitempty"`
	Addons        []AddonConfig            `mapstructure:"addons" json:"addons,omitempty"`
}

// EnvironmentTemplateConfig holds environment template configuration
type EnvironmentTemplateConfig struct {
	ClusterConfig []KeyValueConfig         `mapstructure:"clusterConfig" json:"clusterConfig"`
	CoreServices  map[string]ServiceConfig `mapstructure:"coreServices" json:"coreServices"`
	Addons        []AddonConfig            `mapstructure:"addons" json:"addons,omitempty"`
}

// KeyValueConfig holds key-value configuration pairs
type KeyValueConfig struct {
	Key   string `mapstructure:"key" json:"key"`
	Value string `mapstructure:"value" json:"value"`
}

// ServiceConfig holds service configuration
type ServiceConfig struct {
	Chart  ChartConfig      `mapstructure:"chart" json:"chart"`
	Values []KeyValueConfig `mapstructure:"values" json:"values,omitempty"`
}

// ChartConfig holds Helm chart configuration
type ChartConfig struct {
	RepoURL string `mapstructure:"repoURL" json:"repoURL"`
	Name    string `mapstructure:"name" json:"name"`
	Version string `mapstructure:"version" json:"version"`
}

// AddonConfig holds addon configuration
type AddonConfig struct {
	Name            string           `mapstructure:"name" json:"name"`
	Chart           ChartConfig      `mapstructure:"chart" json:"chart"`
	TargetNamespace string           `mapstructure:"targetNamespace" json:"targetNamespace,omitempty"`
	CreateNamespace bool             `mapstructure:"createNamespace" json:"createNamespace,omitempty"`
	Values          []KeyValueConfig `mapstructure:"values" json:"values,omitempty"`
}

// ResolvedEnvironmentConfig holds a resolved environment configuration
type ResolvedEnvironmentConfig struct {
	Name                  string                `json:"name"`
	ResolvedProvider      string                `json:"resolvedProvider"`
	ResolvedRegion        string                `json:"resolvedRegion"`
	ResolvedType          string                `json:"resolvedType"`
	ResolvedClusterConfig []KeyValueConfig      `json:"resolvedClusterConfig"`
	ResolvedCoreServices  *ResolvedCoreServices `json:"resolvedCoreServices,omitempty"`
	ResolvedAddons        []AddonConfig         `json:"resolvedAddons,omitempty"`
	GlobalSettings        *GlobalSettings       `json:"globalSettings,omitempty"`
}

// ResolvedCoreServices holds resolved core service configurations
type ResolvedCoreServices struct {
	ArgoCD *ServiceConfig `json:"argocd,omitempty"`
	Gitea  *ServiceConfig `json:"gitea,omitempty"`
	Nginx  *ServiceConfig `json:"nginx,omitempty"`
	Cilium *ServiceConfig `json:"cilium,omitempty"`
}

// GlobalSettings holds global platform settings
type GlobalSettings struct {
	AdharContext string `json:"adharContext"`
	DefaultHost  string `json:"defaultHost"`
	EnableHAMode bool   `json:"enableHAMode"`
	Email        string `json:"email"`
}

// ClusterConfig represents a key-value cluster configuration
type ClusterConfig struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Environment type constants
const (
	EnvironmentTypeProduction    = "production"
	EnvironmentTypeNonProduction = "non-production"
)

// LoadConfig loads configuration from file and environment variables
func LoadConfig(configFile string) (*Config, error) {
	v := viper.New()

	// Set default values
	setDefaults(v)

	// Set config file path
	if configFile != "" {
		v.SetConfigFile(configFile)
	} else {
		// Look for config in standard locations
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}

		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath(filepath.Join(home, ".adhar"))
		v.AddConfigPath(home)
	}

	// Environment variable settings
	v.SetEnvPrefix("ADHAR")
	v.AutomaticEnv()

	// Try to read config file
	configFound := true
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			configFound = false
		} else {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// Unmarshal to struct
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// If no config file found and no providers configured, set up Kind as default
	if !configFound && len(config.Providers) == 0 {
		config = getDefaultKindConfig()
	}

	// Validate configuration using schema validator
	validator := NewSchemaValidator()
	if err := validator.ValidateConfig(&config); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	// Validate provider configuration
	if err := validateProviderConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// setDefaults sets default configuration values
func setDefaults(v *viper.Viper) {
	// Global defaults
	v.SetDefault("globalSettings.adharContext", "adhar-mgmt")
	v.SetDefault("globalSettings.defaultHost", "platform.adhar.io")
	v.SetDefault("globalSettings.defaultHttpPort", 80)
	v.SetDefault("globalSettings.defaultHttpsPort", 443)
	v.SetDefault("globalSettings.enableHAMode", false)
	v.SetDefault("globalSettings.email", "admin@adhar.io")

	// Provider defaults
	v.SetDefault("providers.kind.type", "kind")
	v.SetDefault("providers.kind.region", "local")
	v.SetDefault("providers.kind.config.kind_path", "kind")
	v.SetDefault("providers.kind.config.kubectl_path", "kubectl")
}

// SaveConfig saves the configuration to file
func SaveConfig(config *Config, configFile string) error {
	v := viper.New()

	if configFile == "" {
		configFile = "./config.yaml"
	}

	v.SetConfigFile(configFile)
	v.SetConfigType("yaml")

	// Create directory if it doesn't exist
	dir := filepath.Dir(configFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Set values from config struct
	setConfigValues(v, config)

	// Write config file
	if err := v.WriteConfig(); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// setConfigValues sets viper values from config struct
func setConfigValues(v *viper.Viper, config *Config) {
	v.Set("globalSettings", config.GlobalSettings)
	v.Set("providers", config.Providers)
	v.Set("environmentTemplates", config.EnvironmentTemplates)
	v.Set("environments", config.Environments)
}

// InitConfig initializes a new configuration file with defaults
func InitConfig(configFile string) error {
	config := getDefaultKindConfig()

	if configFile == "" {
		configFile = "./config.yaml"
	}

	return SaveConfig(&config, configFile)
}

// GetConfigPath returns the default config file path
func GetConfigPath() string {
	// First check if config.yaml exists in current directory
	if _, err := os.Stat("./config.yaml"); err == nil {
		return "./config.yaml"
	}

	// Then check home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return "./config.yaml"
	}

	// Check home/.adhar/config.yaml (legacy location)
	legacyPath := filepath.Join(home, ".adhar", "config.yaml")
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}

	// Default to current directory
	return "./config.yaml"
}

// validateProviderConfig validates provider configuration for primary provider rules
func validateProviderConfig(config *Config) error {
	if len(config.Providers) == 0 {
		return fmt.Errorf("at least one provider must be configured")
	}

	// Count primary providers
	primaryProviders := []string{}
	for name, provider := range config.Providers {
		if provider.Primary {
			primaryProviders = append(primaryProviders, name)
		}
	}

	// Validation rules based on number of providers and primary designation
	providerCount := len(config.Providers)
	primaryCount := len(primaryProviders)

	switch {
	case providerCount == 1:
		// If only one provider, it's automatically used for everything
		// No validation needed - single provider handles both management and workloads

	case providerCount == 2:
		// If exactly 2 providers, one must be marked as primary for management cluster
		if primaryCount == 0 {
			return fmt.Errorf("when configuring 2 providers, one must be marked as 'primary: true' for management cluster provisioning")
		}
		if primaryCount > 1 {
			return fmt.Errorf("only one provider can be marked as primary, found %d primary providers: %v", primaryCount, primaryProviders)
		}

	case providerCount > 2:
		// More than 2 providers is not allowed according to requirements
		providerNames := make([]string, 0, len(config.Providers))
		for name := range config.Providers {
			providerNames = append(providerNames, name)
		}
		return fmt.Errorf("maximum of 2 providers allowed, found %d providers: %v", providerCount, providerNames)

	default:
		// This shouldn't happen since we check for len > 0 above, but for completeness
		return fmt.Errorf("unexpected provider configuration state")
	}

	return nil
}

// ValidateConfig validates the configuration (legacy function for compatibility)
func ValidateConfig(config *Config) error {
	// Use the new schema validator
	validator := NewSchemaValidator()
	return validator.ValidateConfig(config)
}

// getDefaultKindConfig returns a default configuration with Kind provider for local development
func getDefaultKindConfig() Config {
	return Config{
		GlobalSettings: GlobalSettingsConfig{
			AdharContext:     "adhar-mgmt",
			DefaultHost:      "platform.adhar.io",
			DefaultHttpPort:  80,
			DefaultHttpsPort: 443,
			EnableHAMode:     false,
			Email:            "admin@adhar.io",
		},
		Providers: map[string]ConfigProviderConfig{
			"kind": {
				Type:    "kind",
				Region:  "local",
				Primary: true,
				Config: map[string]interface{}{
					"kind_path":    "kind",
					"kubectl_path": "kubectl",
					"registry": map[string]interface{}{
						"enabled": false,
						"name":    "kind-registry",
						"port":    5001,
					},
					"cluster_config": map[string]interface{}{
						"api_version": "kind.x-k8s.io/v1alpha4",
						"kind":        "Cluster",
						"networking": map[string]interface{}{
							"disable_default_cni": false,
							"kube_proxy_mode":     "iptables",
						},
						"nodes": []map[string]interface{}{
							{
								"role": "control-plane",
								"extra_port_mappings": []map[string]interface{}{
									{"container_port": 80, "host_port": 80},
									{"container_port": 443, "host_port": 443},
								},
							},
							{"role": "worker"},
						},
					},
				},
			},
		},
		EnvironmentTemplates: map[string]EnvironmentTemplateConfig{
			"development-defaults": {
				ClusterConfig: []KeyValueConfig{
					{Key: "autoScale", Value: "true"},
					{Key: "minNodes", Value: "1"},
					{Key: "maxNodes", Value: "3"},
				},
				CoreServices: map[string]ServiceConfig{},
			},
		},
		Environments: map[string]EnvironmentConfig{
			"dev": {
				Type:     "non-production",
				Template: "development-defaults",
				ClusterConfig: []KeyValueConfig{
					{Key: "name", Value: "adhar-dev"},
					{Key: "nodeCount", Value: "1"},
				},
			},
		},
	}
}

// GetPrimaryProvider returns the name of the primary provider for management cluster
func (c *Config) GetPrimaryProvider() (string, error) {
	if len(c.Providers) == 0 {
		return "", fmt.Errorf("no providers configured")
	}

	// If only one provider, it's the primary
	if len(c.Providers) == 1 {
		for name := range c.Providers {
			return name, nil
		}
	}

	// If multiple providers, find the one marked as primary
	for name, provider := range c.Providers {
		if provider.Primary {
			return name, nil
		}
	}

	return "", fmt.Errorf("no primary provider found in multi-provider configuration")
}

// GetWorkloadProvider returns the name of the provider for development workloads
func (c *Config) GetWorkloadProvider() (string, error) {
	if len(c.Providers) == 0 {
		return "", fmt.Errorf("no providers configured")
	}

	// If only one provider, it handles both management and workloads
	if len(c.Providers) == 1 {
		for name := range c.Providers {
			return name, nil
		}
	}

	// If two providers, return the non-primary one for workloads
	primaryProvider, err := c.GetPrimaryProvider()
	if err != nil {
		return "", err
	}

	for name := range c.Providers {
		if name != primaryProvider {
			return name, nil
		}
	}

	return "", fmt.Errorf("unable to determine workload provider")
}

// IsManagementProvider checks if the given provider is designated for management cluster
func (c *Config) IsManagementProvider(providerName string) bool {
	primaryProvider, err := c.GetPrimaryProvider()
	if err != nil {
		return false
	}
	return providerName == primaryProvider
}

// ResolveEnvironments resolves all environment configurations by applying templates
func (c *Config) ResolveEnvironments() error {
	if c.ResolvedEnvironments == nil {
		c.ResolvedEnvironments = make(map[string]*ResolvedEnvironmentConfig)
	}

	for envName, envConfig := range c.Environments {
		c.ResolvedEnvironments[envName] = c.resolveEnvironment(envName, envConfig)
	}

	return nil
}

// resolveEnvironment resolves a single environment configuration
func (c *Config) resolveEnvironment(envName string, envConfig EnvironmentConfig) *ResolvedEnvironmentConfig {
	resolved := &ResolvedEnvironmentConfig{
		Name: envName,
	}

	// Resolve provider
	resolved.ResolvedProvider = envConfig.Provider
	if resolved.ResolvedProvider == "" {
		// Use the first provider if not specified
		for name := range c.Providers {
			resolved.ResolvedProvider = name
			break
		}
	}

	// Resolve region
	resolved.ResolvedRegion = envConfig.Region
	if resolved.ResolvedRegion == "" {
		if provider, exists := c.Providers[resolved.ResolvedProvider]; exists {
			resolved.ResolvedRegion = provider.Region
		}
	}

	// Resolve type
	resolved.ResolvedType = envConfig.Type
	if resolved.ResolvedType == "" {
		resolved.ResolvedType = EnvironmentTypeNonProduction
	}

	// Resolve cluster config by merging template and environment-specific config
	resolved.ResolvedClusterConfig = append([]KeyValueConfig{}, envConfig.ClusterConfig...)

	// Apply template if specified
	if envConfig.Template != "" {
		if template, exists := c.EnvironmentTemplates[envConfig.Template]; exists {
			// Prepend template cluster config (environment-specific config takes precedence)
			templateConfig := make([]KeyValueConfig, len(template.ClusterConfig))
			copy(templateConfig, template.ClusterConfig)
			resolved.ResolvedClusterConfig = append(templateConfig, resolved.ResolvedClusterConfig...)
		}
	}

	// Resolve core services
	resolved.ResolvedCoreServices = &ResolvedCoreServices{}
	if envConfig.CoreServices != nil {
		if argocd, exists := envConfig.CoreServices["argocd"]; exists {
			resolved.ResolvedCoreServices.ArgoCD = &argocd
		}
		if gitea, exists := envConfig.CoreServices["gitea"]; exists {
			resolved.ResolvedCoreServices.Gitea = &gitea
		}
		if nginx, exists := envConfig.CoreServices["nginx"]; exists {
			resolved.ResolvedCoreServices.Nginx = &nginx
		}
		if cilium, exists := envConfig.CoreServices["cilium"]; exists {
			resolved.ResolvedCoreServices.Cilium = &cilium
		}
	}

	// Apply template core services if specified
	if envConfig.Template != "" {
		if template, exists := c.EnvironmentTemplates[envConfig.Template]; exists {
			// Apply template core services if not already specified in environment
			if resolved.ResolvedCoreServices.ArgoCD == nil {
				if argocd, exists := template.CoreServices["argocd"]; exists {
					resolved.ResolvedCoreServices.ArgoCD = &argocd
				}
			}
			if resolved.ResolvedCoreServices.Gitea == nil {
				if gitea, exists := template.CoreServices["gitea"]; exists {
					resolved.ResolvedCoreServices.Gitea = &gitea
				}
			}
			if resolved.ResolvedCoreServices.Nginx == nil {
				if nginx, exists := template.CoreServices["nginx"]; exists {
					resolved.ResolvedCoreServices.Nginx = &nginx
				}
			}
			if resolved.ResolvedCoreServices.Cilium == nil {
				if cilium, exists := template.CoreServices["cilium"]; exists {
					resolved.ResolvedCoreServices.Cilium = &cilium
				}
			}
		}
	}

	// Resolve addons
	resolved.ResolvedAddons = append([]AddonConfig{}, envConfig.Addons...)

	// Apply template addons if specified
	if envConfig.Template != "" {
		if template, exists := c.EnvironmentTemplates[envConfig.Template]; exists {
			// Prepend template addons (environment-specific addons take precedence)
			templateAddons := make([]AddonConfig, len(template.Addons))
			copy(templateAddons, template.Addons)
			resolved.ResolvedAddons = append(templateAddons, resolved.ResolvedAddons...)
		}
	}

	// Set global settings
	resolved.GlobalSettings = &GlobalSettings{
		AdharContext: c.GlobalSettings.AdharContext,
		DefaultHost:  c.GlobalSettings.DefaultHost,
		EnableHAMode: c.GlobalSettings.EnableHAMode,
		Email:        c.GlobalSettings.Email,
	}

	return resolved
}
