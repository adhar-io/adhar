package provider

import (
	"fmt"
	"strings"
)

// Factory implements the ProviderFactory interface
type Factory struct {
	providers map[string]func(map[string]interface{}) (Provider, error)
}

// NewFactory creates a new provider factory
func NewFactory() *Factory {
	f := &Factory{
		providers: make(map[string]func(map[string]interface{}) (Provider, error)),
	}

	// Register built-in providers
	f.registerBuiltinProviders()

	return f
}

// registerBuiltinProviders registers all built-in providers
func (f *Factory) registerBuiltinProviders() {
	// Providers will register themselves via init() functions
	// This avoids import cycles
}

// CreateProvider creates a provider instance of the specified type
func (f *Factory) CreateProvider(providerType string, config map[string]interface{}) (Provider, error) {
	providerType = strings.ToLower(providerType)

	creator, exists := f.providers[providerType]
	if !exists {
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}

	return creator(config)
}

// SupportedProviders returns a list of supported provider types
func (f *Factory) SupportedProviders() []string {
	providers := make([]string, 0, len(f.providers))
	for name := range f.providers {
		providers = append(providers, name)
	}
	return providers
}

// RegisterProvider allows registering custom providers
func (f *Factory) RegisterProvider(name string, creator func(map[string]interface{}) (Provider, error)) {
	f.providers[strings.ToLower(name)] = creator
}

// IsSupported checks if a provider type is supported
func (f *Factory) IsSupported(providerType string) bool {
	_, exists := f.providers[strings.ToLower(providerType)]
	return exists
}

// GetProviderInfo returns information about a specific provider
func (f *Factory) GetProviderInfo(providerType string) (*ProviderInfo, error) {
	providerType = strings.ToLower(providerType)

	if !f.IsSupported(providerType) {
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}

	// Return provider-specific information
	switch providerType {
	case "kind":
		return &ProviderInfo{
			Name:        "Kind",
			Type:        "kind",
			Description: "Local Kubernetes clusters for development and testing",
			Capabilities: []string{
				"cluster-management",
				"node-management",
				"addon-management",
				"local-development",
			},
			RequiredCredentials: []string{}, // No credentials required
			SupportedRegions:    []string{"local"},
			CostModel:           "free",
		}, nil
	case "aws":
		return &ProviderInfo{
			Name:        "Amazon Web Services",
			Type:        "aws",
			Description: "Enterprise-grade cloud platform with comprehensive services",
			Capabilities: []string{
				"cluster-management",
				"node-management",
				"vpc-management",
				"load-balancer-management",
				"storage-management",
				"addon-management",
				"auto-scaling",
				"high-availability",
				"backup-restore",
				"cost-tracking",
			},
			RequiredCredentials: []string{"accessKeyId", "secretAccessKey"},
			SupportedRegions: []string{
				"us-east-1", "us-east-2", "us-west-1", "us-west-2",
				"eu-west-1", "eu-west-2", "eu-central-1",
				"ap-south-1", "ap-southeast-1", "ap-southeast-2", "ap-northeast-1",
			},
			CostModel: "pay-per-use",
		}, nil
	case "azure":
		return &ProviderInfo{
			Name:        "Microsoft Azure",
			Type:        "azure",
			Description: "Microsoft's cloud platform with enterprise integration",
			Capabilities: []string{
				"cluster-management",
				"node-management",
				"vnet-management",
				"load-balancer-management",
				"storage-management",
				"addon-management",
				"auto-scaling",
				"high-availability",
				"backup-restore",
				"cost-tracking",
				"azure-ad-integration",
			},
			RequiredCredentials: []string{"subscriptionId", "clientId", "clientSecret", "tenantId"},
			SupportedRegions: []string{
				"East US", "East US 2", "West US", "West US 2",
				"Central US", "North Central US", "South Central US",
				"West Europe", "North Europe", "UK South", "UK West",
				"Southeast Asia", "East Asia", "Australia East", "Australia Southeast",
			},
			CostModel: "pay-per-use",
		}, nil
	case "gcp":
		return &ProviderInfo{
			Name:        "Google Cloud Platform",
			Type:        "gcp",
			Description: "Google's cloud platform optimized for data and ML workloads",
			Capabilities: []string{
				"cluster-management",
				"node-management",
				"vpc-management",
				"load-balancer-management",
				"storage-management",
				"addon-management",
				"auto-scaling",
				"high-availability",
				"backup-restore",
				"cost-tracking",
				"gke-integration",
			},
			RequiredCredentials: []string{"projectId", "serviceAccountKey"},
			SupportedRegions: []string{
				"us-central1", "us-east1", "us-east4", "us-west1", "us-west2", "us-west3", "us-west4",
				"europe-west1", "europe-west2", "europe-west3", "europe-west4", "europe-west6",
				"asia-east1", "asia-east2", "asia-northeast1", "asia-south1", "asia-southeast1",
			},
			CostModel: "pay-per-use",
		}, nil
	case "digitalocean":
		return &ProviderInfo{
			Name:        "DigitalOcean",
			Type:        "digitalocean",
			Description: "Developer-friendly cloud with simple pricing",
			Capabilities: []string{
				"cluster-management",
				"node-management",
				"vpc-management",
				"load-balancer-management",
				"storage-management",
				"addon-management",
				"auto-scaling",
				"backup-restore",
				"cost-tracking",
			},
			RequiredCredentials: []string{"token"},
			SupportedRegions: []string{
				"nyc1", "nyc2", "nyc3", "ams2", "ams3", "sfo1", "sfo2", "sfo3",
				"sgp1", "lon1", "fra1", "tor1", "blr1", "syd1",
			},
			CostModel: "simple-pricing",
		}, nil
	case "civo":
		return &ProviderInfo{
			Name:        "Civo",
			Type:        "civo",
			Description: "Fast Kubernetes cloud with developer focus",
			Capabilities: []string{
				"cluster-management",
				"node-management",
				"network-management",
				"load-balancer-management",
				"storage-management",
				"addon-management",
				"backup-restore",
				"cost-tracking",
			},
			RequiredCredentials: []string{"apiKey"},
			SupportedRegions:    []string{"LON1", "NYC1", "FRA1"},
			CostModel:           "transparent-pricing",
		}, nil
	case "custom":
		return &ProviderInfo{
			Name:        "Custom/On-Premises",
			Type:        "custom",
			Description: "On-premises or private cloud deployments",
			Capabilities: []string{
				"cluster-management",
				"node-management",
				"vm-management",
				"network-management",
				"storage-management",
				"addon-management",
				"backup-restore",
			},
			RequiredCredentials: []string{"endpoint", "username", "password"},
			SupportedRegions:    []string{"on-premises"},
			CostModel:           "bring-your-own-infrastructure",
		}, nil
	default:
		return nil, fmt.Errorf("provider info not available for: %s", providerType)
	}
}

// ProviderInfo contains information about a provider
type ProviderInfo struct {
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Description         string   `json:"description"`
	Capabilities        []string `json:"capabilities"`
	RequiredCredentials []string `json:"requiredCredentials"`
	SupportedRegions    []string `json:"supportedRegions"`
	CostModel           string   `json:"costModel"`
}

// Global factory instance
var DefaultFactory = NewFactory()
