package config

import (
	"fmt"
	"reflect"
	"strings"
)

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string
	Value   interface{}
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("validation error in field '%s': %s (value: %v)", e.Field, e.Message, e.Value)
}

// ValidationErrors represents multiple validation errors
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	var messages []string
	for _, err := range e {
		messages = append(messages, err.Error())
	}
	return strings.Join(messages, "; ")
}

// SchemaValidator provides configuration validation
type SchemaValidator struct {
	errors ValidationErrors
}

// NewSchemaValidator creates a new schema validator
func NewSchemaValidator() *SchemaValidator {
	return &SchemaValidator{
		errors: make(ValidationErrors, 0),
	}
}

// ValidateConfig validates the entire configuration
func (v *SchemaValidator) ValidateConfig(config *Config) error {
	v.errors = make(ValidationErrors, 0)

	// Validate global settings
	v.validateGlobalSettings(&config.GlobalSettings)

	// Validate providers
	v.validateProviders(config.Providers)

	// Validate environment templates
	v.validateEnvironmentTemplates(config.EnvironmentTemplates)

	// Validate environments
	v.validateEnvironments(config.Environments)

	if len(v.errors) > 0 {
		return v.errors
	}

	return nil
}

// validateGlobalSettings validates global settings
func (v *SchemaValidator) validateGlobalSettings(settings *GlobalSettingsConfig) {
	if settings.AdharContext == "" {
		v.addError("globalSettings.adharContext", settings.AdharContext, "adhar context is required")
	}

	if settings.DefaultHost == "" {
		v.addError("globalSettings.defaultHost", settings.DefaultHost, "default host is required")
	}

	if settings.DefaultHttpPort <= 0 || settings.DefaultHttpPort > 65535 {
		v.addError("globalSettings.defaultHttpPort", settings.DefaultHttpPort, "HTTP port must be between 1 and 65535")
	}

	if settings.DefaultHttpsPort <= 0 || settings.DefaultHttpsPort > 65535 {
		v.addError("globalSettings.defaultHttpsPort", settings.DefaultHttpsPort, "HTTPS port must be between 1 and 65535")
	}

	if settings.Email == "" {
		v.addError("globalSettings.email", settings.Email, "email is required for certificate management")
	}
}

// validateProviders validates provider configurations
func (v *SchemaValidator) validateProviders(providers map[string]ConfigProviderConfig) {
	if len(providers) == 0 {
		v.addError("providers", providers, "at least one provider must be configured")
		return
	}

	primaryProviders := 0
	validProviderTypes := []string{"aws", "azure", "gcp", "digitalocean", "civo", "custom", "kind"}

	for name, provider := range providers {
		// Validate provider type
		if !v.isValidProviderType(provider.Type, validProviderTypes) {
			v.addError(fmt.Sprintf("providers.%s.type", name), provider.Type,
				fmt.Sprintf("invalid provider type, must be one of: %s", strings.Join(validProviderTypes, ", ")))
		}

		// Validate region
		if provider.Region == "" {
			v.addError(fmt.Sprintf("providers.%s.region", name), provider.Region, "region is required")
		}

		// Count primary providers
		if provider.Primary {
			primaryProviders++
		}

		// Validate provider-specific configurations
		v.validateProviderConfig(name, provider)
	}

	// Validate primary provider rules
	if len(providers) == 1 {
		// Single provider should be primary (auto-handled)
	} else if len(providers) == 2 {
		if primaryProviders == 0 {
			v.addError("providers", nil, "with 2 providers, one must be marked as 'primary: true' for management cluster")
		} else if primaryProviders > 1 {
			v.addError("providers", nil, "only one provider can be marked as primary")
		}
	} else {
		v.addError("providers", nil, "maximum of 2 providers allowed")
	}
}

// validateProviderConfig validates provider-specific configuration
func (v *SchemaValidator) validateProviderConfig(name string, provider ConfigProviderConfig) {
	switch provider.Type {
	case "aws":
		v.validateAWSConfig(name, provider.Config)
		// v.validateAWSAuthentication(name, provider)
	case "gcp":
		v.validateGCPConfig(name, provider.Config)
		// v.validateGCPAuthentication(name, provider)
	case "azure":
		v.validateAzureConfig(name, provider.Config)
		// v.validateAzureAuthentication(name, provider)
	case "digitalocean":
		v.validateDigitalOceanConfig(name, provider.Config)
		// v.validateDigitalOceanAuthentication(name, provider)
	case "civo":
		v.validateCivoConfig(name, provider.Config)
		// v.validateCivoAuthentication(name, provider)
	case "custom":
		v.validateCustomConfig(name, provider.Config)
	case "kind":
		v.validateKindConfig(name, provider.Config)
	}
}

// validateAWSConfig validates AWS provider configuration
func (v *SchemaValidator) validateAWSConfig(providerName string, config map[string]interface{}) {
	if config == nil {
		return
	}

	// Validate VPC CIDR
	if vpcCidr, exists := config["vpc_cidr"]; exists {
		if str, ok := vpcCidr.(string); ok && str != "" {
			if !v.isValidCIDR(str) {
				v.addError(fmt.Sprintf("providers.%s.config.vpc_cidr", providerName), vpcCidr, "invalid CIDR format")
			}
		}
	}

	// Validate instance types
	if instanceTypes, exists := config["instance_types"]; exists {
		if types, ok := instanceTypes.(map[string]interface{}); ok {
			if controlPlane, exists := types["control_plane"]; exists {
				if str, ok := controlPlane.(string); ok && str == "" {
					v.addError(fmt.Sprintf("providers.%s.config.instance_types.control_plane", providerName), controlPlane, "control plane instance type cannot be empty")
				}
			}
			if worker, exists := types["worker"]; exists {
				if str, ok := worker.(string); ok && str == "" {
					v.addError(fmt.Sprintf("providers.%s.config.instance_types.worker", providerName), worker, "worker instance type cannot be empty")
				}
			}
		}
	}
}

// validateGCPConfig validates GCP provider configuration
func (v *SchemaValidator) validateGCPConfig(providerName string, config map[string]interface{}) {
	if config == nil {
		return
	}

	// Validate project ID
	if projectID, exists := config["project_id"]; exists {
		if str, ok := projectID.(string); ok && str == "" {
			v.addError(fmt.Sprintf("providers.%s.config.project_id", providerName), projectID, "GCP project ID is required")
		}
	}

	// Validate zone
	if zone, exists := config["zone"]; exists {
		if str, ok := zone.(string); ok && str == "" {
			v.addError(fmt.Sprintf("providers.%s.config.zone", providerName), zone, "GCP zone is required")
		}
	}

	// Validate subnet CIDR
	if subnetCidr, exists := config["subnet_cidr"]; exists {
		if str, ok := subnetCidr.(string); ok && str != "" {
			if !v.isValidCIDR(str) {
				v.addError(fmt.Sprintf("providers.%s.config.subnet_cidr", providerName), subnetCidr, "invalid CIDR format")
			}
		}
	}
}

// validateAzureConfig validates Azure provider configuration
func (v *SchemaValidator) validateAzureConfig(providerName string, config map[string]interface{}) {
	if config == nil {
		return
	}

	// Validate resource group
	if resourceGroup, exists := config["resource_group"]; exists {
		if str, ok := resourceGroup.(string); ok && str == "" {
			v.addError(fmt.Sprintf("providers.%s.config.resource_group", providerName), resourceGroup, "Azure resource group is required")
		}
	}

	// Validate VNet CIDR
	if vnetCidr, exists := config["vnet_cidr"]; exists {
		if str, ok := vnetCidr.(string); ok && str != "" {
			if !v.isValidCIDR(str) {
				v.addError(fmt.Sprintf("providers.%s.config.vnet_cidr", providerName), vnetCidr, "invalid CIDR format")
			}
		}
	}
}

// validateDigitalOceanConfig validates DigitalOcean provider configuration
func (v *SchemaValidator) validateDigitalOceanConfig(providerName string, config map[string]interface{}) {
	if config == nil {
		return
	}

	// Validate VPC CIDR
	if vpcCidr, exists := config["vpc_cidr"]; exists {
		if str, ok := vpcCidr.(string); ok && str != "" {
			if !v.isValidCIDR(str) {
				v.addError(fmt.Sprintf("providers.%s.config.vpc_cidr", providerName), vpcCidr, "invalid CIDR format")
			}
		}
	}
}

// validateCivoConfig validates Civo provider configuration
func (v *SchemaValidator) validateCivoConfig(providerName string, config map[string]interface{}) {
	// Civo has minimal required configuration validation
	if config == nil {
		return
	}
}

// validateCustomConfig validates Custom provider configuration
func (v *SchemaValidator) validateCustomConfig(providerName string, config map[string]interface{}) {
	if config == nil {
		return
	}

	// Validate nodes
	if nodes, exists := config["nodes"]; exists {
		if nodeList, ok := nodes.([]interface{}); ok {
			if len(nodeList) == 0 {
				v.addError(fmt.Sprintf("providers.%s.config.nodes", providerName), nodes, "at least one node must be configured")
			}
		}
	}
}

// validateKindConfig validates Kind provider configuration
func (v *SchemaValidator) validateKindConfig(providerName string, config map[string]interface{}) {
	// Kind has minimal configuration requirements
	if config == nil {
		return
	}
}

// validateEnvironmentTemplates validates environment templates
func (v *SchemaValidator) validateEnvironmentTemplates(templates map[string]EnvironmentTemplateConfig) {
	if len(templates) == 0 {
		v.addError("environmentTemplates", templates, "at least one environment template must be defined")
		return
	}

	for name, template := range templates {
		if len(template.ClusterConfig) == 0 {
			v.addError(fmt.Sprintf("environmentTemplates.%s.clusterConfig", name), template.ClusterConfig, "cluster config cannot be empty")
		}

		if len(template.CoreServices) == 0 {
			v.addError(fmt.Sprintf("environmentTemplates.%s.coreServices", name), template.CoreServices, "core services cannot be empty")
		}
	}
}

// validateEnvironments validates environment configurations
func (v *SchemaValidator) validateEnvironments(environments map[string]EnvironmentConfig) {
	validTypes := []string{"production", "non-production"}

	for name, env := range environments {
		// Validate type
		if !v.isValidString(env.Type, validTypes) {
			v.addError(fmt.Sprintf("environments.%s.type", name), env.Type,
				fmt.Sprintf("invalid type, must be one of: %s", strings.Join(validTypes, ", ")))
		}

		// Validate template reference
		if env.Template == "" {
			v.addError(fmt.Sprintf("environments.%s.template", name), env.Template, "template reference is required")
		}

		// Validate cluster config
		if len(env.ClusterConfig) == 0 {
			v.addError(fmt.Sprintf("environments.%s.clusterConfig", name), env.ClusterConfig, "cluster config cannot be empty")
		}
	}
}

// Helper methods

func (v *SchemaValidator) addError(field string, value interface{}, message string) {
	v.errors = append(v.errors, ValidationError{
		Field:   field,
		Value:   value,
		Message: message,
	})
}

func (v *SchemaValidator) isValidProviderType(providerType string, validTypes []string) bool {
	return v.isValidString(providerType, validTypes)
}

func (v *SchemaValidator) isValidString(value string, validValues []string) bool {
	for _, valid := range validValues {
		if value == valid {
			return true
		}
	}
	return false
}

func (v *SchemaValidator) isValidCIDR(cidr string) bool {
	// Simple CIDR validation - you might want to use a more robust validation
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return false
	}
	// Additional IP and subnet mask validation could be added here
	return true
}

// validateRequired validates that a field is not empty
func (v *SchemaValidator) validateRequired(fieldPath string, value interface{}, fieldName string) {
	val := reflect.ValueOf(value)

	switch val.Kind() {
	case reflect.String:
		if val.String() == "" {
			v.addError(fieldPath, value, fmt.Sprintf("%s is required", fieldName))
		}
	case reflect.Slice, reflect.Map:
		if val.Len() == 0 {
			v.addError(fieldPath, value, fmt.Sprintf("%s cannot be empty", fieldName))
		}
	case reflect.Ptr:
		if val.IsNil() {
			v.addError(fieldPath, value, fmt.Sprintf("%s is required", fieldName))
		}
	}
}

// validateRange validates that a numeric value is within a specific range
func (v *SchemaValidator) validateRange(fieldPath string, value interface{}, min, max int, fieldName string) {
	if intVal, ok := value.(int); ok {
		if intVal < min || intVal > max {
			v.addError(fieldPath, value, fmt.Sprintf("%s must be between %d and %d", fieldName, min, max))
		}
	}
}
