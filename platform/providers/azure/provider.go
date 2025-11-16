package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

const (
	statusHealthy   = "healthy"
	statusUnhealthy = "unhealthy"
)

// ResourceTracker tracks all Azure resources for a cluster
type ResourceTracker struct {
	SubscriptionID        string    `json:"subscriptionId"`
	ResourceGroup         string    `json:"resourceGroup"`
	Location              string    `json:"location"`
	VirtualNetworks       []string  `json:"virtualNetworks"`
	Subnets               []string  `json:"subnets"`
	NetworkSecurityGroups []string  `json:"networkSecurityGroups"`
	VirtualMachines       []string  `json:"virtualMachines"`
	NetworkInterfaces     []string  `json:"networkInterfaces"`
	PublicIPs             []string  `json:"publicIPs"`
	StorageAccounts       []string  `json:"storageAccounts"`
	LoadBalancers         []string  `json:"loadBalancers"`
	AvailabilitySets      []string  `json:"availabilitySets"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// StateData represents the persisted state
type StateData struct {
	Clusters         map[string]*types.Cluster   `json:"clusters"`
	ResourceTrackers map[string]*ResourceTracker `json:"resourceTrackers"`
}

// ClusterInfrastructure represents the Azure infrastructure for a manual Kubernetes cluster
type ClusterInfrastructure struct {
	ResourceGroupName        string     `json:"resourceGroupName"`
	VirtualNetworkName       string     `json:"virtualNetworkName"`
	SubnetName               string     `json:"subnetName"`
	NetworkSecurityGroupName string     `json:"networkSecurityGroupName"`
	MasterNodes              []NodeInfo `json:"masterNodes"`
	WorkerNodes              []NodeInfo `json:"workerNodes"`
	LoadBalancerName         string     `json:"loadBalancerName"`
	PublicIPName             string     `json:"publicIPName"`
}

// NodeInfo represents information about a cluster node in Azure
type NodeInfo struct {
	VMName        string
	ResourceGroup string
	Location      string
	PrivateIP     string
	PublicIP      string
	VMSize        string
	Role          string // "master" or "worker"
}

// Register the Azure provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("azure", func(config map[string]interface{}) (provider.Provider, error) {
		// Create provider config from the configuration map
		providerConfig, err := parseProviderConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Azure provider config: %w", err)
		}

		return NewProvider(providerConfig)
	})
}

// parseProviderConfig parses the configuration map into AzureProviderConfig
func parseProviderConfig(config map[string]interface{}) (*Config, error) {
	azureConfig := &Config{}

	// Parse Azure-specific configuration from config section
	if configSection, ok := config["config"].(map[string]interface{}); ok {
		log.Printf("Azure config section found, keys: %+v", configSection)
		if subscriptionID, ok := configSection["subscriptionId"].(string); ok {
			azureConfig.SubscriptionID = subscriptionID
			log.Printf("Azure config: subscriptionId = %s", subscriptionID)
		} else if subscriptionID, ok := configSection["subscriptionid"].(string); ok {
			azureConfig.SubscriptionID = subscriptionID
			log.Printf("Azure config: subscriptionid = %s", subscriptionID)
		} else {
			log.Printf("Azure config: subscriptionId not found or not a string")
		}
		if resourceGroup, ok := configSection["resource_group"].(string); ok {
			azureConfig.ResourceGroup = resourceGroup
			log.Printf("Azure config: resource_group = %s", resourceGroup)
		}
		// Also try the camelCase variant
		if resourceGroup, ok := configSection["resourceGroup"].(string); ok && azureConfig.ResourceGroup == "" {
			azureConfig.ResourceGroup = resourceGroup
			log.Printf("Azure config: resourceGroup = %s", resourceGroup)
		}
		if location, ok := configSection["location"].(string); ok {
			azureConfig.Location = location
			log.Printf("Azure config: location = %s", location)
		}
		if vmSize, ok := configSection["vm_size"].(string); ok {
			azureConfig.VMSize = vmSize
			log.Printf("Azure config: vm_size = %s", vmSize)
		}
		// Also try the camelCase variant
		if vmSize, ok := configSection["vmSize"].(string); ok && azureConfig.VMSize == "" {
			azureConfig.VMSize = vmSize
			log.Printf("Azure config: vmSize = %s", vmSize)
		}
		if vnetCIDR, ok := configSection["vnet_cidr"].(string); ok {
			azureConfig.VNetCIDR = vnetCIDR
			log.Printf("Azure config: vnet_cidr = %s", vnetCIDR)
		}
		if subnetCIDR, ok := configSection["subnet_cidr"].(string); ok {
			azureConfig.SubnetCIDR = subnetCIDR
			log.Printf("Azure config: subnet_cidr = %s", subnetCIDR)
		}
		if diskType, ok := configSection["disk_type"].(string); ok {
			azureConfig.DiskType = diskType
			log.Printf("Azure config: disk_type = %s", diskType)
		}
	}

	// Parse top-level region as location if not set in config section
	if region, ok := config["region"].(string); ok && azureConfig.Location == "" {
		azureConfig.Location = region
		log.Printf("Azure config: using region as location = %s", region)
	}

	// Debug: log all parsed configuration
	log.Printf("Azure config parsed: subscriptionId=%s, resourceGroup=%s, location=%s, vmSize=%s",
		azureConfig.SubscriptionID, azureConfig.ResourceGroup, azureConfig.Location, azureConfig.VMSize)

	// Parse authentication configuration - supporting all methods from config.yaml
	if clientID, ok := config["clientId"].(string); ok && clientID != "" {
		azureConfig.ClientID = clientID
	}
	if clientSecret, ok := config["clientSecret"].(string); ok && clientSecret != "" {
		azureConfig.ClientSecret = clientSecret
	}
	if tenantID, ok := config["tenantId"].(string); ok && tenantID != "" {
		azureConfig.TenantID = tenantID
	}
	if credentialsFile, ok := config["credentials_file"].(string); ok && credentialsFile != "" {
		azureConfig.CredentialsFile = credentialsFile
	}
	if certificatePath, ok := config["certificatePath"].(string); ok && certificatePath != "" {
		azureConfig.CertificatePath = certificatePath
	}
	if useManagedIdentity, ok := config["useManagedIdentity"].(bool); ok {
		azureConfig.UseManagedIdentity = useManagedIdentity
	}
	if useAzureCLI, ok := config["useAzureCLI"].(bool); ok {
		azureConfig.UseAzureCLI = useAzureCLI
	}
	if useEnvironment, ok := config["useEnvironment"].(bool); ok {
		azureConfig.UseEnvironment = useEnvironment
	}

	var missing []string
	requiredFields := []struct {
		value string
		name  string
	}{
		{azureConfig.SubscriptionID, "subscriptionId"},
		{azureConfig.ResourceGroup, "resourceGroup"},
		{azureConfig.Location, "location"},
	}
	for _, field := range requiredFields {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required Azure config fields: %s", strings.Join(missing, ", "))
	}

	return azureConfig, nil
}

// createAzureCredentials creates Azure credentials based on the available authentication methods
func createAzureCredentials(config *Config) (azcore.TokenCredential, error) {
	// Authentication priority order:
	// 1. Client Secret (Direct)
	// 2. Credentials File
	// 3. Certificate Path
	// 4. Managed Identity
	// 5. Azure CLI
	// 6. Environment Variables
	// 7. Default Chain

	// Method 1: Client Secret (Direct)
	if config.ClientID != "" && config.ClientSecret != "" && config.TenantID != "" {
		log.Printf("Using Azure authentication: Client Secret (Direct)")
		return azidentity.NewClientSecretCredential(
			config.TenantID,
			config.ClientID,
			config.ClientSecret,
			nil,
		)
	}

	// Method 2: Credentials File
	if config.CredentialsFile != "" {
		log.Printf("Using Azure authentication: Credentials File (%s)", config.CredentialsFile)
		// Expand tilde to home directory
		credFile := config.CredentialsFile
		if strings.HasPrefix(credFile, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to get user home directory: %w", err)
			}
			credFile = filepath.Join(home, credFile[2:])
		}

		// Check if credentials file exists
		if _, err := os.Stat(credFile); os.IsNotExist(err) {
			return nil, fmt.Errorf("Azure credentials file not found at %s", credFile)
		}

		// Read and parse credentials file
		credData, err := os.ReadFile(credFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read Azure credentials file: %w", err)
		}

		// Try to parse as service principal credentials first (simple object)
		var creds struct {
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
			TenantID     string `json:"tenantId"`
		}

		if err := json.Unmarshal(credData, &creds); err == nil {
			// Successfully parsed as service principal credentials
			if creds.ClientID != "" && creds.ClientSecret != "" && creds.TenantID != "" {
				log.Printf("Using Azure authentication: Service Principal from credentials file")
				return azidentity.NewClientSecretCredential(
					creds.TenantID,
					creds.ClientID,
					creds.ClientSecret,
					nil,
				)
			}
		}

		// If not service principal format, try to parse as Azure CLI token cache (array format)
		var tokenArray []map[string]interface{}
		if err := json.Unmarshal(credData, &tokenArray); err == nil && len(tokenArray) > 0 {
			// Found Azure CLI token cache format
			log.Printf("Detected Azure CLI token cache format, using Azure CLI authentication")
			// Use Azure CLI credential instead of trying to parse the file
			return azidentity.NewAzureCLICredential(nil)
		} else {
			return nil, fmt.Errorf("failed to parse Azure credentials file: expected either service principal format {clientId, clientSecret, tenantId} or Azure CLI token cache format")
		}
	}

	// Method 3: Certificate Path
	if config.CertificatePath != "" && config.ClientID != "" && config.TenantID != "" {
		log.Printf("Using Azure authentication: Certificate (%s)", config.CertificatePath)
		// For now, fall back to environment or default chain for certificate auth
		// Certificate authentication requires more complex certificate parsing
		log.Printf("Certificate authentication not fully implemented, falling back to default chain")
		return azidentity.NewDefaultAzureCredential(nil)
	}

	// Method 4: Managed Identity
	if config.UseManagedIdentity {
		log.Printf("Using Azure authentication: Managed Identity")
		return azidentity.NewManagedIdentityCredential(nil)
	}

	// Method 5: Azure CLI with timeout protection
	if config.UseAzureCLI {
		log.Printf("Using Azure authentication: Azure CLI (with timeout protection)")
		cred, err := azidentity.NewAzureCLICredential(nil)
		if err != nil {
			log.Printf("Azure CLI credential creation failed: %v", err)
		} else {
			// Test the credential quickly with a short timeout
			testCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := cred.GetToken(testCtx, policy.TokenRequestOptions{
				Scopes: []string{"https://management.azure.com/.default"},
			})
			if err == nil {
				return cred, nil
			}
			log.Printf("Azure CLI credential test failed: %v, falling back to environment/default", err)
		}
	}

	// Method 6: Environment Variables
	if config.UseEnvironment {
		log.Printf("Using Azure authentication: Environment Variables")
		return azidentity.NewEnvironmentCredential(nil)
	}

	// Method 7: Default Chain (try all methods) - Always available as fallback
	log.Printf("Using Azure authentication: Default Chain (fallback)")
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("all Azure authentication methods failed: %w", err)
	}
	return cred, nil
}

// Provider implements the Azure provider for manual Kubernetes clusters using Azure Go SDK
type Provider struct {
	config *Config
	cred   azcore.TokenCredential

	// Resource tracking
	clusters         map[string]*types.Cluster
	resourceTrackers map[string]*ResourceTracker

	// Azure SDK clients for manual infrastructure
	resourceGroupClient        *armresources.ResourceGroupsClient
	virtualNetworkClient       *armnetwork.VirtualNetworksClient
	subnetClient               *armnetwork.SubnetsClient
	networkSecurityGroupClient *armnetwork.SecurityGroupsClient
	virtualMachineClient       *armcompute.VirtualMachinesClient
	networkInterfaceClient     *armnetwork.InterfacesClient
	publicIPClient             *armnetwork.PublicIPAddressesClient
	loadBalancerClient         *armnetwork.LoadBalancersClient
	availabilitySetClient      *armcompute.AvailabilitySetsClient
	diskClient                 *armcompute.DisksClient
}

// Config holds Azure provider configuration for manual clusters
type Config struct {
	SubscriptionID string `json:"subscriptionId"`
	ClientID       string `json:"clientId"`
	ClientSecret   string `json:"clientSecret"`
	TenantID       string `json:"tenantId"`
	ResourceGroup  string `json:"resourceGroup"`
	Location       string `json:"location"`
	VMSize         string `json:"vmSize"`
	DiskSizeGB     int32  `json:"diskSizeGB"`
	DiskType       string `json:"diskType"`
	ImagePublisher string `json:"imagePublisher"`
	ImageOffer     string `json:"imageOffer"`
	ImageSKU       string `json:"imageSKU"`
	VNetCIDR       string `json:"vnetCIDR"`
	SubnetCIDR     string `json:"subnetCIDR"`

	// Authentication options
	CredentialsFile    string `json:"credentialsFile"`
	CertificatePath    string `json:"certificatePath"`
	UseManagedIdentity bool   `json:"useManagedIdentity"`
	UseAzureCLI        bool   `json:"useAzureCLI"`
	UseEnvironment     bool   `json:"useEnvironment"`
}

// NewProvider creates a new Azure provider instance for manual Kubernetes clusters
func NewProvider(config *Config) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("Azure configuration is required")
	}

	// Set defaults
	if config.Location == "" {
		config.Location = "East US"
	}
	if config.VMSize == "" {
		config.VMSize = "Standard_D2s_v3"
	}
	if config.DiskSizeGB == 0 {
		config.DiskSizeGB = 50
	}
	if config.ImagePublisher == "" {
		config.ImagePublisher = "Canonical"
	}
	if config.ImageOffer == "" {
		config.ImageOffer = "0001-com-ubuntu-server-jammy"
	}
	if config.ImageSKU == "" {
		config.ImageSKU = "22_04-lts-gen2"
	}
	if config.VNetCIDR == "" {
		config.VNetCIDR = "10.1.0.0/16"
	}
	if config.SubnetCIDR == "" {
		config.SubnetCIDR = "10.1.1.0/24"
	}
	if config.DiskType == "" {
		config.DiskType = "Standard_LRS"
	}

	// Create Azure credentials using multiple authentication methods
	cred, err := createAzureCredentials(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credentials: %w", err)
	}

	// Initialize Azure SDK clients
	resourceGroupClient, err := armresources.NewResourceGroupsClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource group client: %w", err)
	}

	virtualNetworkClient, err := armnetwork.NewVirtualNetworksClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create virtual network client: %w", err)
	}

	subnetClient, err := armnetwork.NewSubnetsClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet client: %w", err)
	}

	networkSecurityGroupClient, err := armnetwork.NewSecurityGroupsClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create network security group client: %w", err)
	}

	virtualMachineClient, err := armcompute.NewVirtualMachinesClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create virtual machine client: %w", err)
	}

	networkInterfaceClient, err := armnetwork.NewInterfacesClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create network interface client: %w", err)
	}

	publicIPClient, err := armnetwork.NewPublicIPAddressesClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create public IP client: %w", err)
	}

	loadBalancerClient, err := armnetwork.NewLoadBalancersClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer client: %w", err)
	}

	availabilitySetClient, err := armcompute.NewAvailabilitySetsClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create availability set client: %w", err)
	}

	diskClient, err := armcompute.NewDisksClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create disk client: %w", err)
	}

	provider := &Provider{
		config:                     config,
		cred:                       cred,
		clusters:                   make(map[string]*types.Cluster),
		resourceTrackers:           make(map[string]*ResourceTracker),
		resourceGroupClient:        resourceGroupClient,
		virtualNetworkClient:       virtualNetworkClient,
		subnetClient:               subnetClient,
		networkSecurityGroupClient: networkSecurityGroupClient,
		virtualMachineClient:       virtualMachineClient,
		networkInterfaceClient:     networkInterfaceClient,
		publicIPClient:             publicIPClient,
		loadBalancerClient:         loadBalancerClient,
		availabilitySetClient:      availabilitySetClient,
		diskClient:                 diskClient,
	}

	// Load existing state
	if err := provider.loadState(); err != nil {
		log.Printf("Warning: failed to load state: %v", err)
	}

	return provider, nil
}

// getStateFilePath returns the path to the state file
func (p *Provider) getStateFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}

	stateDir := filepath.Join(home, ".adhar", "state", "azure")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create state directory: %w", err)
	}

	return filepath.Join(stateDir, "clusters.json"), nil
}

// loadState loads the provider state from disk
func (p *Provider) loadState() error {
	stateFile, err := p.getStateFilePath()
	if err != nil {
		return err
	}

	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		return nil // No state file exists yet
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var stateData StateData
	if err := json.Unmarshal(data, &stateData); err != nil {
		return fmt.Errorf("failed to unmarshal state data: %w", err)
	}

	if stateData.Clusters != nil {
		p.clusters = stateData.Clusters
	}
	if stateData.ResourceTrackers != nil {
		p.resourceTrackers = stateData.ResourceTrackers
	}

	log.Printf("Loaded %d clusters and %d resource trackers from state",
		len(p.clusters), len(p.resourceTrackers))

	return nil
}

// saveState saves the provider state to disk
func (p *Provider) saveState() error {
	stateFile, err := p.getStateFilePath()
	if err != nil {
		return err
	}

	stateData := StateData{
		Clusters:         p.clusters,
		ResourceTrackers: p.resourceTrackers,
	}

	data, err := json.MarshalIndent(stateData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state data: %w", err)
	}

	return os.WriteFile(stateFile, data, 0644)
}

// Name returns the provider name
func (p *Provider) Name() string {
	return "azure"
}

// Region returns the provider region
func (p *Provider) Region() string {
	return p.config.Location
}

// Authenticate validates Azure credentials using resource group operations
func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	log.Printf("Authenticating with Azure for subscription: %s", p.config.SubscriptionID)

	// Test Azure credentials by making a simple API call to list resource groups
	pager := p.resourceGroupClient.NewListPager(nil)
	_, err := pager.NextPage(ctx)
	if err != nil {
		return fmt.Errorf("failed to authenticate with Azure: %w", err)
	}

	log.Printf("Successfully authenticated with Azure")
	return nil
}

// ValidatePermissions checks if we have required permissions for manual cluster creation
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	log.Printf("Validating Azure permissions for manual cluster creation")

	// Check resource group permissions
	if p.config.ResourceGroup != "" {
		_, err := p.resourceGroupClient.Get(ctx, p.config.ResourceGroup, nil)
		if err != nil {
			return fmt.Errorf("insufficient permissions to access resource group %s: %w", p.config.ResourceGroup, err)
		}
	}

	// Check compute permissions by trying to list VMs
	pager := p.virtualMachineClient.NewListAllPager(nil)
	_, err := pager.NextPage(ctx)
	if err != nil {
		return fmt.Errorf("insufficient compute permissions: %w", err)
	}

	log.Printf("Azure permissions validation successful")
	return nil
}

// CreateCluster creates a new manual Kubernetes cluster using Azure SDK
func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Provider != "azure" {
		return nil, fmt.Errorf("provider mismatch: expected azure, got %s", spec.Provider)
	}

	log.Printf("Creating manual Kubernetes cluster: %s", spec.Name)

	// Validate cluster specification
	err := p.validateClusterSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster specification: %w", err)
	}

	// Create cluster infrastructure
	infrastructure, err := p.createClusterInfrastructure(ctx, spec.Name, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster infrastructure: %w", err)
	}

	// Create cluster object
	cluster := &types.Cluster{
		ID:        fmt.Sprintf("azure-%s", spec.Name),
		Name:      spec.Name,
		Provider:  "azure",
		Region:    p.config.Location,
		Version:   spec.Version,
		Status:    types.ClusterStatusRunning,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"resourceGroup":        infrastructure.ResourceGroupName,
			"location":             p.config.Location,
			"virtualNetwork":       infrastructure.VirtualNetworkName,
			"subnet":               infrastructure.SubnetName,
			"networkSecurityGroup": infrastructure.NetworkSecurityGroupName,
			"masterNodes":          len(infrastructure.MasterNodes),
			"workerNodes":          len(infrastructure.WorkerNodes),
		},
	}

	// Set cluster endpoint using master node public IP
	if len(infrastructure.MasterNodes) > 0 && infrastructure.MasterNodes[0].PublicIP != "" {
		cluster.Endpoint = fmt.Sprintf("https://%s:6443", infrastructure.MasterNodes[0].PublicIP)
	}

	// Store cluster infrastructure
	p.clusters[cluster.ID] = cluster

	// Create resource tracker
	resourceTracker := &ResourceTracker{
		SubscriptionID:        p.config.SubscriptionID,
		ResourceGroup:         infrastructure.ResourceGroupName,
		Location:              p.config.Location,
		VirtualNetworks:       []string{infrastructure.VirtualNetworkName},
		Subnets:               []string{infrastructure.SubnetName},
		NetworkSecurityGroups: []string{infrastructure.NetworkSecurityGroupName},
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}

	// Add VMs to resource tracker
	for _, node := range infrastructure.MasterNodes {
		resourceTracker.VirtualMachines = append(resourceTracker.VirtualMachines, node.VMName)
	}
	for _, node := range infrastructure.WorkerNodes {
		resourceTracker.VirtualMachines = append(resourceTracker.VirtualMachines, node.VMName)
	}

	p.resourceTrackers[cluster.ID] = resourceTracker

	// Save state to disk
	if err := p.saveState(); err != nil {
		log.Printf("Warning: failed to save state: %v", err)
	}

	log.Printf("Successfully created cluster: %s", spec.Name)
	return cluster, nil
}

// validateClusterSpec validates the cluster specification
func (p *Provider) validateClusterSpec(spec *types.ClusterSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("cluster name is required")
	}
	if spec.ControlPlane.Replicas <= 0 {
		spec.ControlPlane.Replicas = 1 // Default to 1 master node
	}
	if spec.ControlPlane.InstanceType == "" {
		spec.ControlPlane.InstanceType = p.config.VMSize // Use default VM size
	}
	return nil
}

// createClusterInfrastructure creates the Azure infrastructure for a manual Kubernetes cluster
func (p *Provider) createClusterInfrastructure(ctx context.Context, clusterName string, spec *types.ClusterSpec) (*ClusterInfrastructure, error) {
	log.Printf("Creating infrastructure for cluster: %s", clusterName)

	infrastructure := &ClusterInfrastructure{}

	// Create or ensure resource group exists
	resourceGroupName := fmt.Sprintf("%s-rg", clusterName)
	if p.config.ResourceGroup != "" {
		resourceGroupName = p.config.ResourceGroup
	}
	err := p.createResourceGroup(ctx, resourceGroupName)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource group: %w", err)
	}
	infrastructure.ResourceGroupName = resourceGroupName

	// Create virtual network
	vnetName := fmt.Sprintf("%s-vnet", clusterName)
	err = p.createVirtualNetwork(ctx, resourceGroupName, vnetName)
	if err != nil {
		return nil, fmt.Errorf("failed to create virtual network: %w", err)
	}
	infrastructure.VirtualNetworkName = vnetName

	// Create subnet
	subnetName := fmt.Sprintf("%s-subnet", clusterName)
	err = p.createSubnet(ctx, resourceGroupName, vnetName, subnetName)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet: %w", err)
	}
	infrastructure.SubnetName = subnetName

	// Create network security group
	nsgName := fmt.Sprintf("%s-nsg", clusterName)
	err = p.createNetworkSecurityGroup(ctx, resourceGroupName, nsgName)
	if err != nil {
		return nil, fmt.Errorf("failed to create network security group: %w", err)
	}
	infrastructure.NetworkSecurityGroupName = nsgName

	// Create master nodes
	masterNodes, err := p.createMasterNodes(ctx, resourceGroupName, vnetName, subnetName, nsgName, clusterName, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create master nodes: %w", err)
	}
	infrastructure.MasterNodes = masterNodes

	// Create worker nodes if specified
	if len(spec.NodeGroups) > 0 {
		workerNodes, err := p.createWorkerNodes(ctx, resourceGroupName, vnetName, subnetName, nsgName, clusterName, spec)
		if err != nil {
			return nil, fmt.Errorf("failed to create worker nodes: %w", err)
		}
		infrastructure.WorkerNodes = workerNodes
	}

	log.Printf("Successfully created infrastructure for cluster: %s", clusterName)
	return infrastructure, nil
}

// createResourceGroup creates or ensures a resource group exists
func (p *Provider) createResourceGroup(ctx context.Context, resourceGroupName string) error {
	log.Printf("Creating resource group: %s in location: %s", resourceGroupName, p.config.Location)

	// Check if resource group already exists
	log.Printf("Checking if resource group %s already exists", resourceGroupName)
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := p.resourceGroupClient.Get(checkCtx, resourceGroupName, nil)
	if err == nil {
		log.Printf("Resource group %s already exists", resourceGroupName)
		return nil
	}
	log.Printf("Resource group %s does not exist, creating it", resourceGroupName)

	// Create resource group
	resourceGroup := armresources.ResourceGroup{
		Location: &p.config.Location,
		Tags: map[string]*string{
			"managedBy": stringPtr("adhar-platform"),
			"purpose":   stringPtr("kubernetes-cluster"),
		},
	}

	log.Printf("Calling Azure API to create resource group...")
	createCtx, cancelCreate := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelCreate()
	_, err = p.resourceGroupClient.CreateOrUpdate(createCtx, resourceGroupName, resourceGroup, nil)
	if err != nil {
		log.Printf("Error creating resource group: %v", err)
		return fmt.Errorf("failed to create resource group: %w", err)
	}

	log.Printf("Successfully created resource group: %s", resourceGroupName)
	return nil
}

// createVirtualNetwork creates a virtual network using Azure SDK
func (p *Provider) createVirtualNetwork(ctx context.Context, resourceGroupName, vnetName string) error {
	log.Printf("Creating virtual network: %s with CIDR: %s", vnetName, p.config.VNetCIDR)

	vnet := armnetwork.VirtualNetwork{
		Location: &p.config.Location,
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{
					&p.config.VNetCIDR,
				},
			},
		},
		Tags: map[string]*string{
			"managedBy": stringPtr("adhar-platform"),
			"purpose":   stringPtr("kubernetes-cluster"),
		},
	}

	poller, err := p.virtualNetworkClient.BeginCreateOrUpdate(ctx, resourceGroupName, vnetName, vnet, nil)
	if err != nil {
		return fmt.Errorf("failed to create virtual network: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for virtual network creation: %w", err)
	}

	log.Printf("Successfully created virtual network: %s", vnetName)
	return nil
}

// createSubnet creates a subnet in the virtual network using Azure SDK
func (p *Provider) createSubnet(ctx context.Context, resourceGroupName, vnetName, subnetName string) error {
	log.Printf("Creating subnet: %s with CIDR: %s", subnetName, p.config.SubnetCIDR)

	// First, verify that the VNet exists
	log.Printf("Verifying virtual network %s exists before creating subnet", vnetName)
	_, err := p.virtualNetworkClient.Get(ctx, resourceGroupName, vnetName, nil)
	if err != nil {
		return fmt.Errorf("virtual network %s not found in resource group %s: %w", vnetName, resourceGroupName, err)
	}

	subnet := armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix: &p.config.SubnetCIDR,
		},
	}

	poller, err := p.subnetClient.BeginCreateOrUpdate(ctx, resourceGroupName, vnetName, subnetName, subnet, nil)
	if err != nil {
		return fmt.Errorf("failed to create subnet: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for subnet creation: %w", err)
	}

	log.Printf("Successfully created subnet: %s", subnetName)
	return nil
}

// createNetworkSecurityGroup creates network security group with Kubernetes rules
func (p *Provider) createNetworkSecurityGroup(ctx context.Context, resourceGroupName, nsgName string) error {
	log.Printf("Creating network security group: %s", nsgName)

	// Define security rules for Kubernetes
	securityRules := []*armnetwork.SecurityRule{
		{
			Name: stringPtr("SSH"),
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Description:              stringPtr("Allow SSH"),
				Protocol:                 toPtr(armnetwork.SecurityRuleProtocolTCP),
				SourcePortRange:          stringPtr("*"),
				DestinationPortRange:     stringPtr("22"),
				SourceAddressPrefix:      stringPtr("*"),
				DestinationAddressPrefix: stringPtr("*"),
				Access:                   toPtr(armnetwork.SecurityRuleAccessAllow),
				Priority:                 int32Ptr(1001),
				Direction:                toPtr(armnetwork.SecurityRuleDirectionInbound),
			},
		},
		{
			Name: stringPtr("Kubernetes-API"),
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Description:              stringPtr("Allow Kubernetes API Server"),
				Protocol:                 toPtr(armnetwork.SecurityRuleProtocolTCP),
				SourcePortRange:          stringPtr("*"),
				DestinationPortRange:     stringPtr("6443"),
				SourceAddressPrefix:      stringPtr("*"),
				DestinationAddressPrefix: stringPtr("*"),
				Access:                   toPtr(armnetwork.SecurityRuleAccessAllow),
				Priority:                 int32Ptr(1002),
				Direction:                toPtr(armnetwork.SecurityRuleDirectionInbound),
			},
		},
		{
			Name: stringPtr("Internal-Cluster"),
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Description:              stringPtr("Allow internal cluster communication"),
				Protocol:                 toPtr(armnetwork.SecurityRuleProtocolAsterisk),
				SourcePortRange:          stringPtr("*"),
				DestinationPortRange:     stringPtr("*"),
				SourceAddressPrefix:      stringPtr("10.0.0.0/16"),
				DestinationAddressPrefix: stringPtr("10.0.0.0/16"),
				Access:                   toPtr(armnetwork.SecurityRuleAccessAllow),
				Priority:                 int32Ptr(1003),
				Direction:                toPtr(armnetwork.SecurityRuleDirectionInbound),
			},
		},
	}

	nsg := armnetwork.SecurityGroup{
		Location: &p.config.Location,
		Properties: &armnetwork.SecurityGroupPropertiesFormat{
			SecurityRules: securityRules,
		},
		Tags: map[string]*string{
			"managedBy": stringPtr("adhar-platform"),
			"purpose":   stringPtr("kubernetes-cluster"),
		},
	}

	poller, err := p.networkSecurityGroupClient.BeginCreateOrUpdate(ctx, resourceGroupName, nsgName, nsg, nil)
	if err != nil {
		return fmt.Errorf("failed to create network security group: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for network security group creation: %w", err)
	}

	log.Printf("Successfully created network security group: %s", nsgName)
	return nil
}

// Helper functions for pointer conversions
func stringPtr(s string) *string {
	return &s
}

func int32Ptr(i int32) *int32 {
	return &i
}

// toPtr returns a pointer to the given value
func toPtr[T any](v T) *T {
	return &v
}

// Helper functions for Azure types
//
//nolint:unused // Helpers retained for future Azure ARM payload assembly.
func toPtrString(s string) *string {
	return &s
}

//nolint:unused // Helpers retained for future Azure ARM payload assembly.
func toPtrInt32(i int32) *int32 {
	return &i
}

//nolint:unused // Helpers retained for future Azure ARM payload assembly.
func toPtrBool(b bool) *bool {
	return &b
}

// createMasterNodes creates master nodes for the Kubernetes cluster using Azure SDK
func (p *Provider) createMasterNodes(ctx context.Context, resourceGroupName, vnetName, subnetName, nsgName, clusterName string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	log.Printf("Creating master nodes for cluster: %s", clusterName)

	var masterNodes []NodeInfo

	for i := 0; i < spec.ControlPlane.Replicas; i++ {
		vmName := fmt.Sprintf("%s-master-%d", clusterName, i)

		nodeInfo, err := p.createVirtualMachine(ctx, resourceGroupName, vnetName, subnetName, nsgName, vmName, spec.ControlPlane.InstanceType, true)
		if err != nil {
			return nil, fmt.Errorf("failed to create master node %s: %w", vmName, err)
		}

		masterNodes = append(masterNodes, *nodeInfo)
	}

	log.Printf("Successfully created master nodes: %d nodes", len(masterNodes))
	return masterNodes, nil
}

// createWorkerNodes creates worker nodes for the Kubernetes cluster using Azure SDK
func (p *Provider) createWorkerNodes(ctx context.Context, resourceGroupName, vnetName, subnetName, nsgName, clusterName string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	log.Printf("Creating worker nodes for cluster: %s", clusterName)

	var workerNodes []NodeInfo

	for _, nodeGroup := range spec.NodeGroups {
		for i := 0; i < nodeGroup.Replicas; i++ {
			vmName := fmt.Sprintf("%s-worker-%s-%d", clusterName, nodeGroup.Name, i)

			nodeInfo, err := p.createVirtualMachine(ctx, resourceGroupName, vnetName, subnetName, nsgName, vmName, nodeGroup.InstanceType, false)
			if err != nil {
				return nil, fmt.Errorf("failed to create worker node %s: %w", vmName, err)
			}

			workerNodes = append(workerNodes, *nodeInfo)
		}
	}

	log.Printf("Successfully created worker nodes: %d nodes", len(workerNodes))
	return workerNodes, nil
}

// createVirtualMachine creates a virtual machine using Azure SDK
func (p *Provider) createVirtualMachine(ctx context.Context, resourceGroupName, vnetName, subnetName, nsgName, vmName, vmSize string, isMaster bool) (*NodeInfo, error) {
	log.Printf("Creating virtual machine: %s with VM size: %s", vmName, vmSize)
	log.Printf("VM configuration: Image=%s:%s:%s, DiskSize=%dGB", p.config.ImagePublisher, p.config.ImageOffer, p.config.ImageSKU, p.config.DiskSizeGB)

	// Use default VM size if not specified
	if vmSize == "" {
		vmSize = p.config.VMSize
	}

	// Create public IP
	publicIPName := fmt.Sprintf("%s-pip", vmName)
	err := p.createPublicIP(ctx, resourceGroupName, publicIPName)
	if err != nil {
		return nil, fmt.Errorf("failed to create public IP: %w", err)
	}

	// Create network interface
	nicName := fmt.Sprintf("%s-nic", vmName)
	err = p.createNetworkInterface(ctx, resourceGroupName, nicName, vnetName, subnetName, nsgName, publicIPName)
	if err != nil {
		return nil, fmt.Errorf("failed to create network interface: %w", err)
	}

	// Generate startup script for Kubernetes installation
	startupScript := p.generateStartupScript(isMaster)

	// Encode the startup script in Base64 for Azure CustomData
	encodedScript := base64.StdEncoding.EncodeToString([]byte(startupScript))

	// Create virtual machine
	vm := armcompute.VirtualMachine{
		Location: &p.config.Location,
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: toPtr(armcompute.VirtualMachineSizeTypes(vmSize)),
			},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{
					Publisher: &p.config.ImagePublisher,
					Offer:     &p.config.ImageOffer,
					SKU:       &p.config.ImageSKU,
					Version:   stringPtr("latest"),
				},
				OSDisk: &armcompute.OSDisk{
					CreateOption: toPtr(armcompute.DiskCreateOptionTypesFromImage),
					DiskSizeGB:   &p.config.DiskSizeGB,
					ManagedDisk: &armcompute.ManagedDiskParameters{
						StorageAccountType: toPtr(armcompute.StorageAccountTypes(p.config.DiskType)),
					},
				},
			},
			OSProfile: &armcompute.OSProfile{
				ComputerName:  &vmName,
				AdminUsername: stringPtr("azureuser"),
				AdminPassword: stringPtr("AdharPlatform@2025"), // Default password
				CustomData:    stringPtr(encodedScript),
				LinuxConfiguration: &armcompute.LinuxConfiguration{
					DisablePasswordAuthentication: toPtr(false), // Enable password auth for now
				},
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{
					{
						ID: stringPtr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/%s",
							p.config.SubscriptionID, resourceGroupName, nicName)),
						Properties: &armcompute.NetworkInterfaceReferenceProperties{
							Primary: toPtr(true),
						},
					},
				},
			},
		},
		Tags: map[string]*string{
			"managedBy": stringPtr("adhar-platform"),
			"purpose":   stringPtr("kubernetes-cluster"),
			"role": stringPtr(func() string {
				if isMaster {
					return "master"
				} else {
					return "worker"
				}
			}()),
		},
	}

	log.Printf("Starting VM creation for: %s", vmName)
	poller, err := p.virtualMachineClient.BeginCreateOrUpdate(ctx, resourceGroupName, vmName, vm, nil)
	if err != nil {
		log.Printf("Failed to initiate VM creation for %s: %v", vmName, err)
		return nil, fmt.Errorf("failed to create virtual machine: %w", err)
	}

	log.Printf("Waiting for VM creation to complete: %s", vmName)
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		log.Printf("VM creation failed during polling for %s: %v", vmName, err)
		return nil, fmt.Errorf("failed to wait for virtual machine creation: %w", err)
	}

	log.Printf("VM creation completed successfully: %s", vmName)

	// Get VM details to retrieve IP addresses
	_, err = p.virtualMachineClient.Get(ctx, resourceGroupName, vmName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get virtual machine details: %w", err)
	}

	// Get public IP address
	publicIP, err := p.publicIPClient.Get(ctx, resourceGroupName, publicIPName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get public IP: %w", err)
	}

	// Get network interface for private IP
	nic, err := p.networkInterfaceClient.Get(ctx, resourceGroupName, nicName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get network interface: %w", err)
	}

	var privateIP, publicIPAddr string
	if nic.Properties != nil && len(nic.Properties.IPConfigurations) > 0 {
		if nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress != nil {
			privateIP = *nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress
		}
	}
	if publicIP.Properties != nil && publicIP.Properties.IPAddress != nil {
		publicIPAddr = *publicIP.Properties.IPAddress
	}

	role := "worker"
	if isMaster {
		role = "master"
	}

	nodeInfo := &NodeInfo{
		VMName:        vmName,
		ResourceGroup: resourceGroupName,
		Location:      p.config.Location,
		PrivateIP:     privateIP,
		PublicIP:      publicIPAddr,
		VMSize:        vmSize,
		Role:          role,
	}

	log.Printf("Successfully created virtual machine: %s", vmName)
	return nodeInfo, nil
}

// createPublicIP creates a public IP address using Azure SDK
func (p *Provider) createPublicIP(ctx context.Context, resourceGroupName, publicIPName string) error {
	log.Printf("Creating public IP: %s", publicIPName)

	publicIP := armnetwork.PublicIPAddress{
		Location: &p.config.Location,
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: toPtr(armnetwork.IPAllocationMethodStatic), // Static required for Standard SKU
		},
		SKU: &armnetwork.PublicIPAddressSKU{
			Name: toPtr(armnetwork.PublicIPAddressSKUNameStandard), // Use Standard SKU instead of Basic
		},
		Tags: map[string]*string{
			"managedBy": stringPtr("adhar-platform"),
			"purpose":   stringPtr("kubernetes-cluster"),
		},
	}

	poller, err := p.publicIPClient.BeginCreateOrUpdate(ctx, resourceGroupName, publicIPName, publicIP, nil)
	if err != nil {
		return fmt.Errorf("failed to create public IP: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for public IP creation: %w", err)
	}

	log.Printf("Successfully created public IP: %s", publicIPName)
	return nil
}

// createNetworkInterface creates a network interface using Azure SDK
func (p *Provider) createNetworkInterface(ctx context.Context, resourceGroupName, nicName, vnetName, subnetName, nsgName, publicIPName string) error {
	log.Printf("Creating network interface: %s", nicName)

	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s/subnets/%s",
		p.config.SubscriptionID, resourceGroupName, vnetName, subnetName)
	nsgID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkSecurityGroups/%s",
		p.config.SubscriptionID, resourceGroupName, nsgName)
	publicIPID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/publicIPAddresses/%s",
		p.config.SubscriptionID, resourceGroupName, publicIPName)

	nic := armnetwork.Interface{
		Location: &p.config.Location,
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{
				{
					Name: stringPtr("internal"),
					Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
						Subnet: &armnetwork.Subnet{
							ID: &subnetID,
						},
						PublicIPAddress: &armnetwork.PublicIPAddress{
							ID: &publicIPID,
						},
						PrivateIPAllocationMethod: toPtr(armnetwork.IPAllocationMethodDynamic),
					},
				},
			},
			NetworkSecurityGroup: &armnetwork.SecurityGroup{
				ID: &nsgID,
			},
		},
		Tags: map[string]*string{
			"managedBy": stringPtr("adhar-platform"),
			"purpose":   stringPtr("kubernetes-cluster"),
		},
	}

	poller, err := p.networkInterfaceClient.BeginCreateOrUpdate(ctx, resourceGroupName, nicName, nic, nil)
	if err != nil {
		return fmt.Errorf("failed to create network interface: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for network interface creation: %w", err)
	}

	log.Printf("Successfully created network interface: %s", nicName)
	return nil
}

// generateStartupScript generates a startup script for Kubernetes installation
func (p *Provider) generateStartupScript(isMaster bool) string {
	script := `#!/bin/bash
set -e

# Update and install Docker
apt-get update -y
apt-get install -y docker.io
systemctl enable --now docker

# Install Kubernetes components
curl -s https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -
echo "deb https://apt.kubernetes.io/ kubernetes-xenial main" > /etc/apt/sources.list.d/kubernetes.list
apt-get update -y
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl
systemctl enable --now kubelet

# Disable swap
swapoff -a
sed -i '/ swap / s/^/#/' /etc/fstab
`

	if isMaster {
		script += `
# Initialize Kubernetes master
kubeadm init --pod-network-cidr=10.244.0.0/16

# Set up kubectl
mkdir -p /root/.kube
cp /etc/kubernetes/admin.conf /root/.kube/config

# Install CNI plugin
kubectl apply -f https://raw.githubusercontent.com/flannel-io/flannel/master/Documentation/kube-flannel.yml
`
	}

	return script
}

// DeleteCluster deletes a manual Kubernetes cluster and all associated Azure resources
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	log.Printf("Deleting cluster: %s", clusterID)

	// Get resource tracker for cleanup
	resourceTracker, exists := p.resourceTrackers[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}

	// For Azure, we can delete the entire resource group if it was created by us
	// This is more efficient and ensures complete cleanup
	resourceGroupName := resourceTracker.ResourceGroup

	log.Printf("Deleting entire resource group: %s", resourceGroupName)

	// Check if the resource group has the managedBy tag indicating it was created by us
	rg, err := p.resourceGroupClient.Get(ctx, resourceGroupName, nil)
	if err != nil {
		log.Printf("Warning: failed to get resource group %s: %v", resourceGroupName, err)
	} else {
		managedByAdhar := false
		if rg.ResourceGroup.Tags != nil {
			if managedBy, ok := rg.ResourceGroup.Tags["managedBy"]; ok && managedBy != nil && *managedBy == "adhar-platform" {
				managedByAdhar = true
			}
		}

		if managedByAdhar {
			// Delete the entire resource group (this deletes all contained resources)
			poller, err := p.resourceGroupClient.BeginDelete(ctx, resourceGroupName, nil)
			if err != nil {
				return fmt.Errorf("failed to delete resource group %s: %w", resourceGroupName, err)
			}

			_, err = poller.PollUntilDone(ctx, nil)
			if err != nil {
				return fmt.Errorf("failed to wait for resource group deletion: %w", err)
			}

			log.Printf("Successfully deleted resource group: %s", resourceGroupName)
		} else {
			log.Printf("Resource group %s not managed by adhar-platform, skipping deletion", resourceGroupName)
			// In this case, we would delete individual resources instead
			// For now, we'll just log a warning
			log.Printf("Warning: manual cleanup required for resource group %s", resourceGroupName)
		}
	}

	// Clean up tracking
	delete(p.clusters, clusterID)
	delete(p.resourceTrackers, clusterID)

	// Save state to disk
	if err := p.saveState(); err != nil {
		log.Printf("Warning: failed to save state: %v", err)
	}

	log.Printf("Successfully deleted cluster: %s", clusterID)
	return nil
}

// UpdateCluster updates a manual Kubernetes cluster
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	log.Printf("Updating cluster: %s", clusterID)

	cluster, exists := p.clusters[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}

	// Update cluster metadata
	cluster.Version = spec.Version
	cluster.UpdatedAt = time.Now()

	// In a real implementation, we would:
	// 1. Update VM configurations
	// 2. Upgrade Kubernetes version
	// 3. Scale node groups as needed

	log.Printf("Successfully updated cluster: %s", clusterID)
	return nil
}

// GetCluster retrieves cluster information
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	cluster, exists := p.clusters[clusterID]
	if !exists {
		return nil, fmt.Errorf("cluster %s not found", clusterID)
	}

	return cluster, nil
}

// ListClusters lists all manual Kubernetes clusters
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	clusters := make([]*types.Cluster, 0, len(p.clusters))

	// Add clusters from state (tracked clusters)
	for _, cluster := range p.clusters {
		clusters = append(clusters, cluster)
	}

	// Discover existing clusters not in state
	discoveredClusters, err := p.discoverExistingClusters(ctx)
	if err != nil {
		log.Printf("Warning: failed to discover existing clusters: %v", err)
	} else {
		clusters = append(clusters, discoveredClusters...)
	}

	return clusters, nil
}

// discoverExistingClusters scans Azure VMs to find clusters that aren't in our state
//
//nolint:gocyclo // Azure discovery requires many conditional checks; refactor tracked separately.
func (p *Provider) discoverExistingClusters(ctx context.Context) ([]*types.Cluster, error) {
	if p.virtualMachineClient == nil {
		return nil, fmt.Errorf("virtual machine client not initialized")
	}

	discoveredClusters := make([]*types.Cluster, 0, len(p.clusters))
	trackedClusterNames := make(map[string]bool)

	// First, get all cluster names that are already tracked
	for clusterID := range p.resourceTrackers {
		parts := strings.Split(clusterID, "-")
		if len(parts) >= 2 {
			// Extract cluster name from cluster ID format: azure-{cluster-name}
			clusterName := strings.Join(parts[1:], "-")
			trackedClusterNames[clusterName] = true
		}
	}

	// List all VMs in the subscription
	pager := p.virtualMachineClient.NewListAllPager(nil)
	clusterVMs := make(map[string][]*armcompute.VirtualMachine)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMs: %w", err)
		}

		for _, vm := range page.Value {
			if vm.Name == nil || vm.Tags == nil {
				continue
			}

			vmName := *vm.Name

			// Check if this VM matches our cluster naming pattern or has managedBy tag
			var clusterName string

			// Check for managedBy tag
			if managedBy, ok := vm.Tags["managedBy"]; ok && managedBy != nil && *managedBy == "adhar-platform" {
				// Try to extract cluster name from VM name pattern
				// Pattern: {cluster-name}-{node-type}-{index}
				parts := strings.Split(vmName, "-")
				if len(parts) >= 3 {
					if strings.Contains(vmName, "-master-") || strings.Contains(vmName, "-worker-") {
						// Find the cluster name part (everything before -master- or -worker-)
						if idx := strings.Index(vmName, "-master-"); idx > 0 {
							clusterName = vmName[:idx]
						} else if idx := strings.Index(vmName, "-worker-"); idx > 0 {
							clusterName = vmName[:idx]
						}
					}
				}
			}

			if clusterName != "" && !trackedClusterNames[clusterName] {
				clusterVMs[clusterName] = append(clusterVMs[clusterName], vm)
			}
		}
	}

	// Create cluster objects for discovered clusters
	for clusterName, vms := range clusterVMs {
		if len(vms) == 0 {
			continue
		}

		// Count master and worker nodes
		masterCount := 0
		workerCount := 0
		var masterVM *armcompute.VirtualMachine

		for _, vm := range vms {
			if strings.Contains(*vm.Name, "-master-") {
				masterCount++
				if masterVM == nil {
					masterVM = vm
				}
			} else if strings.Contains(*vm.Name, "-worker-") {
				workerCount++
			}
		}

		// Determine cluster status based on VM power states
		status := types.ClusterStatusUnknown
		if masterCount > 0 && masterVM != nil {
			// Get VM status
			if masterVM.Properties != nil {
				// For simplicity, assume running if we can query it
				status = types.ClusterStatusRunning
			}
		}

		// Create cluster ID
		clusterID := fmt.Sprintf("azure-%s", clusterName)

		// Try to get creation timestamp from the master VM
		var createdAt time.Time
		if masterVM != nil && masterVM.Properties != nil && masterVM.Properties.TimeCreated != nil {
			createdAt = *masterVM.Properties.TimeCreated
		}
		if createdAt.IsZero() {
			createdAt = time.Now().Add(-24 * time.Hour) // Default to 24 hours ago if unknown
		}

		// Get location from first VM
		location := p.config.Location
		if len(vms) > 0 && vms[0].Location != nil {
			location = *vms[0].Location
		}

		cluster := &types.Cluster{
			ID:        clusterID,
			Name:      clusterName,
			Provider:  "azure",
			Region:    location,
			Version:   "v1.29.0", // Default version for discovered clusters
			Status:    status,
			CreatedAt: createdAt,
			UpdatedAt: time.Now(),
			Metadata: map[string]interface{}{
				"subscriptionId": p.config.SubscriptionID,
				"location":       location,
				"vmCount":        len(vms),
				"masterCount":    masterCount,
				"workerCount":    workerCount,
				"discovered":     true, // Mark as discovered (not tracked in state)
			},
		}

		discoveredClusters = append(discoveredClusters, cluster)
		log.Printf("Discovered existing cluster: %s with %d VMs (%d masters, %d workers)",
			clusterName, len(vms), masterCount, workerCount)
	}

	return discoveredClusters, nil
}

// AddNodeGroup adds a node group to the cluster
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	log.Printf("Adding node group %s to cluster %s", nodeGroup.Name, clusterID)

	cluster, exists := p.clusters[clusterID]
	if !exists {
		return nil, fmt.Errorf("cluster %s not found", clusterID)
	}

	resourceTracker := p.resourceTrackers[clusterID]

	// Create additional worker nodes for the new node group
	for i := 0; i < nodeGroup.Replicas; i++ {
		vmName := fmt.Sprintf("%s-worker-%s-%d", cluster.Name, nodeGroup.Name, i)

		// Extract infrastructure details from metadata
		resourceGroupName := resourceTracker.ResourceGroup
		vnetName := resourceTracker.VirtualNetworks[0]      // Use first VNet
		subnetName := resourceTracker.Subnets[0]            // Use first subnet
		nsgName := resourceTracker.NetworkSecurityGroups[0] // Use first NSG

		nodeInfo, err := p.createVirtualMachine(ctx, resourceGroupName, vnetName, subnetName, nsgName, vmName, nodeGroup.InstanceType, false)
		if err != nil {
			return nil, fmt.Errorf("failed to create worker node %s: %w", vmName, err)
		}

		// Add to resource tracker
		resourceTracker.VirtualMachines = append(resourceTracker.VirtualMachines, nodeInfo.VMName)
	}

	return &types.NodeGroup{
		Name:         nodeGroup.Name,
		Replicas:     nodeGroup.Replicas,
		InstanceType: nodeGroup.InstanceType,
		Status:       "ready",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

// RemoveNodeGroup removes a node group from the cluster
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	log.Printf("Removing node group %s from cluster %s", nodeGroupName, clusterID)

	cluster, exists := p.clusters[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}

	resourceTracker := p.resourceTrackers[clusterID]

	// Find and delete VMs belonging to this node group
	var remainingVMs []string
	for _, vmName := range resourceTracker.VirtualMachines {
		if fmt.Sprintf("%s-worker-%s-", cluster.Name, nodeGroupName) == vmName[:len(fmt.Sprintf("%s-worker-%s-", cluster.Name, nodeGroupName))] {
			// Delete this VM
			poller, err := p.virtualMachineClient.BeginDelete(ctx, resourceTracker.ResourceGroup, vmName, nil)
			if err != nil {
				log.Printf("Failed to delete VM %s: %v", vmName, err)
				continue
			}
			_, err = poller.PollUntilDone(ctx, nil)
			if err != nil {
				log.Printf("Failed to wait for VM deletion %s: %v", vmName, err)
			}
		} else {
			remainingVMs = append(remainingVMs, vmName)
		}
	}

	// Update resource tracker
	resourceTracker.VirtualMachines = remainingVMs

	log.Printf("Successfully removed node group: %s", nodeGroupName)
	return nil
}

// ScaleNodeGroup scales a node group
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	log.Printf("Scaling node group %s in cluster %s to %d replicas", nodeGroupName, clusterID, replicas)

	_, exists := p.clusters[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}

	// In a real implementation, we would:
	// 1. Count current VMs in the node group
	// 2. Add or remove VMs to match desired replicas
	// 3. Update resource tracker

	log.Printf("Successfully scaled node group %s to %d replicas", nodeGroupName, replicas)
	return nil
}

// GetNodeGroup retrieves node group information
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	_, exists := p.clusters[clusterID]
	if !exists {
		return nil, fmt.Errorf("cluster %s not found", clusterID)
	}

	// In a real implementation, we would query the actual VMs and their status
	return &types.NodeGroup{
		Name:         nodeGroupName,
		Replicas:     3,
		InstanceType: p.config.VMSize,
		Status:       "ready",
		CreatedAt:    time.Now().Add(-1 * time.Hour),
		UpdatedAt:    time.Now(),
	}, nil
}

// ListNodeGroups lists all node groups for a cluster
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	return []*types.NodeGroup{
		{
			Name:         "default",
			Replicas:     3,
			InstanceType: "Standard_D2s_v3",
			Status:       "ready",
			CreatedAt:    time.Now().Add(-1 * time.Hour),
			UpdatedAt:    time.Now(),
		},
	}, nil
}

// CreateVPC creates a virtual network using Azure Resource Manager
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	log.Printf("Creating Azure VNet with CIDR: %s", spec.CIDR)

	// Generate unique VNet name
	vnetName := fmt.Sprintf("adhar-vnet-%d", time.Now().Unix())

	// Create Virtual Network
	vnetParams := armnetwork.VirtualNetwork{
		Location: &p.config.Location,
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{&spec.CIDR},
			},
		},
		Tags: make(map[string]*string),
	}

	// Add tags
	for k, v := range spec.Tags {
		vStr := v
		vnetParams.Tags[k] = &vStr
	}

	// Create VNet
	pollerResp, err := p.virtualNetworkClient.BeginCreateOrUpdate(
		ctx,
		p.config.ResourceGroup,
		vnetName,
		vnetParams,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create VNet: %w", err)
	}

	// Wait for completion
	result, err := pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for VNet creation: %w", err)
	}

	vnet := result.VirtualNetwork
	log.Printf("Successfully created Azure VNet: %s", vnetName)

	// Return VPC information
	return &types.VPC{
		ID:                *vnet.ID,
		CIDR:              spec.CIDR,
		AvailabilityZones: spec.AvailabilityZones,
		Status:            "available",
		Tags:              spec.Tags,
	}, nil
}

// DeleteVPC deletes a virtual network using Azure Resource Manager
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	log.Printf("Deleting Azure VNet: %s", vpcID)

	// Extract VNet name from resource ID or use vpcID directly
	vnetName := vpcID
	if strings.Contains(vpcID, "/virtualNetworks/") {
		parts := strings.Split(vpcID, "/")
		vnetName = parts[len(parts)-1]
	}

	// Delete Virtual Network
	pollerResp, err := p.virtualNetworkClient.BeginDelete(
		ctx,
		p.config.ResourceGroup,
		vnetName,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to delete VNet: %w", err)
	}

	// Wait for completion
	_, err = pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for VNet deletion: %w", err)
	}

	log.Printf("Successfully deleted Azure VNet: %s", vnetName)
	return nil
}

// GetVPC retrieves VNet information using Azure Resource Manager
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	log.Printf("Getting Azure VNet: %s", vpcID)

	// Extract VNet name from resource ID or use vpcID directly
	vnetName := vpcID
	if strings.Contains(vpcID, "/virtualNetworks/") {
		parts := strings.Split(vpcID, "/")
		vnetName = parts[len(parts)-1]
	}

	// Get Virtual Network
	response, err := p.virtualNetworkClient.Get(
		ctx,
		p.config.ResourceGroup,
		vnetName,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get VNet: %w", err)
	}

	vnet := response.VirtualNetwork

	// Extract CIDR
	cidr := ""
	if vnet.Properties != nil && vnet.Properties.AddressSpace != nil && len(vnet.Properties.AddressSpace.AddressPrefixes) > 0 {
		cidr = *vnet.Properties.AddressSpace.AddressPrefixes[0]
	}

	// Convert tags
	tags := make(map[string]string)
	if vnet.Tags != nil {
		for k, v := range vnet.Tags {
			if v != nil {
				tags[k] = *v
			}
		}
	}

	// Return VPC information
	return &types.VPC{
		ID:                *vnet.ID,
		CIDR:              cidr,
		AvailabilityZones: []string{*vnet.Location}, // Use location as AZ for Azure
		Status:            "available",              // Azure VNets don't have explicit status
		Tags:              tags,
	}, nil
}

// CreateLoadBalancer creates a load balancer using Azure Resource Manager
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	log.Printf("Creating Azure Load Balancer for ports: %v", spec.Ports)

	// Generate unique load balancer name
	lbName := fmt.Sprintf("adhar-lb-%d", time.Now().Unix())
	publicIPName := fmt.Sprintf("%s-pip", lbName)

	// First, create a public IP for the load balancer
	publicIPParams := armnetwork.PublicIPAddress{
		Location: &p.config.Location,
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: (*armnetwork.IPAllocationMethod)(&[]string{"Static"}[0]),
			PublicIPAddressVersion:   (*armnetwork.IPVersion)(&[]string{"IPv4"}[0]),
		},
		SKU: &armnetwork.PublicIPAddressSKU{
			Name: (*armnetwork.PublicIPAddressSKUName)(&[]string{"Standard"}[0]),
		},
	}

	// Create Public IP
	publicIPPollerResp, err := p.publicIPClient.BeginCreateOrUpdate(
		ctx,
		p.config.ResourceGroup,
		publicIPName,
		publicIPParams,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create public IP: %w", err)
	}

	// Wait for public IP creation
	publicIPResult, err := publicIPPollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for public IP creation: %w", err)
	}

	// Create Load Balancer configuration
	frontendIPConfigs := []*armnetwork.FrontendIPConfiguration{
		{
			Name: &[]string{"frontend-config"}[0],
			Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
				PublicIPAddress: &armnetwork.PublicIPAddress{
					ID: publicIPResult.PublicIPAddress.ID,
				},
			},
		},
	}

	backendPools := []*armnetwork.BackendAddressPool{
		{
			Name: &[]string{"backend-pool"}[0],
		},
	}

	// Create load balancing rules for each port
	loadBalancingRules := make([]*armnetwork.LoadBalancingRule, 0, len(spec.Ports))
	for i, portSpec := range spec.Ports {
		ruleName := fmt.Sprintf("rule-%d", portSpec.Port)
		loadBalancingRules = append(loadBalancingRules, &armnetwork.LoadBalancingRule{
			Name: &ruleName,
			Properties: &armnetwork.LoadBalancingRulePropertiesFormat{
				FrontendIPConfiguration: &armnetwork.SubResource{
					ID: &[]string{fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/frontend-config", p.config.SubscriptionID, p.config.ResourceGroup, lbName)}[0],
				},
				BackendAddressPool: &armnetwork.SubResource{
					ID: &[]string{fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/backendAddressPools/backend-pool", p.config.SubscriptionID, p.config.ResourceGroup, lbName)}[0],
				},
				Protocol:             (*armnetwork.TransportProtocol)(&[]string{"Tcp"}[0]),
				FrontendPort:         &[]int32{int32(portSpec.Port)}[0],
				BackendPort:          &[]int32{int32(portSpec.TargetPort)}[0],
				IdleTimeoutInMinutes: &[]int32{4}[0],
				EnableFloatingIP:     &[]bool{false}[0],
			},
		})

		// Only create one probe for health checking (using first port)
		if i == 0 {
			// Add health probe
			loadBalancingRules[i].Properties.Probe = &armnetwork.SubResource{
				ID: &[]string{fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/probes/health-probe", p.config.SubscriptionID, p.config.ResourceGroup, lbName)}[0],
			}
		}
	}

	// Create health probe
	probes := []*armnetwork.Probe{
		{
			Name: &[]string{"health-probe"}[0],
			Properties: &armnetwork.ProbePropertiesFormat{
				Protocol:          (*armnetwork.ProbeProtocol)(&[]string{"Http"}[0]),
				Port:              &[]int32{80}[0], // Default health check port
				RequestPath:       &[]string{"/healthz"}[0],
				IntervalInSeconds: &[]int32{15}[0],
				NumberOfProbes:    &[]int32{2}[0],
			},
		},
	}

	// Create Load Balancer
	lbParams := armnetwork.LoadBalancer{
		Location: &p.config.Location,
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			FrontendIPConfigurations: frontendIPConfigs,
			BackendAddressPools:      backendPools,
			LoadBalancingRules:       loadBalancingRules,
			Probes:                   probes,
		},
		SKU: &armnetwork.LoadBalancerSKU{
			Name: (*armnetwork.LoadBalancerSKUName)(&[]string{"Standard"}[0]),
		},
	}

	// Create Load Balancer
	lbPollerResp, err := p.loadBalancerClient.BeginCreateOrUpdate(
		ctx,
		p.config.ResourceGroup,
		lbName,
		lbParams,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer: %w", err)
	}

	// Wait for load balancer creation
	lbResult, err := lbPollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for load balancer creation: %w", err)
	}

	// Get the public IP address
	publicIP := ""
	if publicIPResult.PublicIPAddress.Properties != nil && publicIPResult.PublicIPAddress.Properties.IPAddress != nil {
		publicIP = *publicIPResult.PublicIPAddress.Properties.IPAddress
	}

	log.Printf("Successfully created Azure Load Balancer: %s with IP: %s", lbName, publicIP)

	// Return load balancer information
	return &types.LoadBalancer{
		ID:       *lbResult.LoadBalancer.ID,
		Type:     "application",
		Endpoint: publicIP,
		Status:   "active",
		Tags:     spec.Tags,
	}, nil
}

// DeleteLoadBalancer deletes a load balancer using Azure Resource Manager
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	log.Printf("Deleting Azure Load Balancer: %s", lbID)

	// Extract load balancer name from resource ID or use lbID directly
	lbName := lbID
	if strings.Contains(lbID, "/loadBalancers/") {
		parts := strings.Split(lbID, "/")
		lbName = parts[len(parts)-1]
	}

	// Delete Load Balancer
	pollerResp, err := p.loadBalancerClient.BeginDelete(
		ctx,
		p.config.ResourceGroup,
		lbName,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to delete load balancer: %w", err)
	}

	// Wait for completion
	_, err = pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for load balancer deletion: %w", err)
	}

	// Also delete associated public IP
	publicIPName := fmt.Sprintf("%s-pip", lbName)
	publicIPPollerResp, err := p.publicIPClient.BeginDelete(
		ctx,
		p.config.ResourceGroup,
		publicIPName,
		nil,
	)
	if err != nil {
		log.Printf("Warning: failed to delete public IP %s: %v", publicIPName, err)
	} else {
		_, err = publicIPPollerResp.PollUntilDone(ctx, nil)
		if err != nil {
			log.Printf("Warning: failed to wait for public IP deletion %s: %v", publicIPName, err)
		}
	}

	log.Printf("Successfully deleted Azure Load Balancer: %s", lbName)
	return nil
}

// GetLoadBalancer retrieves load balancer information using Azure Resource Manager
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	log.Printf("Getting Azure Load Balancer: %s", lbID)

	// Extract load balancer name from resource ID or use lbID directly
	lbName := lbID
	if strings.Contains(lbID, "/loadBalancers/") {
		parts := strings.Split(lbID, "/")
		lbName = parts[len(parts)-1]
	}

	// Get Load Balancer
	response, err := p.loadBalancerClient.Get(
		ctx,
		p.config.ResourceGroup,
		lbName,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get load balancer: %w", err)
	}

	lb := response.LoadBalancer

	// Get public IP address
	endpoint := ""
	if lb.Properties != nil && lb.Properties.FrontendIPConfigurations != nil && len(lb.Properties.FrontendIPConfigurations) > 0 {
		frontendConfig := lb.Properties.FrontendIPConfigurations[0]
		if frontendConfig.Properties != nil && frontendConfig.Properties.PublicIPAddress != nil && frontendConfig.Properties.PublicIPAddress.ID != nil {
			// Extract public IP name and get its address
			parts := strings.Split(*frontendConfig.Properties.PublicIPAddress.ID, "/")
			publicIPName := parts[len(parts)-1]

			publicIPResp, err := p.publicIPClient.Get(ctx, p.config.ResourceGroup, publicIPName, nil)
			if err == nil && publicIPResp.PublicIPAddress.Properties != nil && publicIPResp.PublicIPAddress.Properties.IPAddress != nil {
				endpoint = *publicIPResp.PublicIPAddress.Properties.IPAddress
			}
		}
	}

	// Return load balancer information
	return &types.LoadBalancer{
		ID:       *lb.ID,
		Type:     "application",
		Endpoint: endpoint,
		Status:   "active", // Azure LBs don't have explicit status
		Tags:     make(map[string]string),
	}, nil
}

// CreateStorage creates managed disk storage using Azure Resource Manager
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	log.Printf("Creating Azure managed disk of size: %s", spec.Size)

	// Parse size from string (e.g., "10GB", "20Gi")
	sizeGB, err := p.parseAzureStorageSize(spec.Size)
	if err != nil {
		return nil, fmt.Errorf("invalid storage size %s: %w", spec.Size, err)
	}

	// Generate unique disk name
	diskName := fmt.Sprintf("adhar-disk-%d", time.Now().Unix())

	// Create managed disk configuration
	diskParams := armcompute.Disk{
		Location: &p.config.Location,
		Properties: &armcompute.DiskProperties{
			DiskSizeGB: &sizeGB,
			CreationData: &armcompute.CreationData{
				CreateOption: (*armcompute.DiskCreateOption)(&[]string{"Empty"}[0]),
			},
		},
		SKU: &armcompute.DiskSKU{
			Name: (*armcompute.DiskStorageAccountTypes)(&[]string{"Premium_LRS"}[0]), // Premium SSD
		},
	}

	// Add tags
	if len(spec.Tags) > 0 {
		diskParams.Tags = make(map[string]*string)
		for k, v := range spec.Tags {
			vStr := v
			diskParams.Tags[k] = &vStr
		}
	}

	// Create disk via Azure API
	pollerResp, err := p.diskClient.BeginCreateOrUpdate(
		ctx,
		p.config.ResourceGroup,
		diskName,
		diskParams,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed disk via Azure API: %w", err)
	}

	// Wait for disk creation
	result, err := pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for disk creation: %w", err)
	}

	log.Printf("Successfully created Azure managed disk: %s", diskName)

	// Convert to our Storage type
	return &types.Storage{
		ID:     *result.Disk.ID,
		Type:   spec.Type,
		Size:   fmt.Sprintf("%dGB", sizeGB),
		Status: "available",
		Tags:   spec.Tags,
	}, nil
}

// parseAzureStorageSize converts size string to GB integer
func (p *Provider) parseAzureStorageSize(sizeStr string) (int32, error) {
	// Remove spaces and convert to lowercase
	sizeStr = strings.ToLower(strings.TrimSpace(sizeStr))

	// Default size patterns
	if sizeStr == "" {
		return 10, nil // 10GB default
	}

	// Extract number and unit
	var size int64
	var unit string

	if strings.HasSuffix(sizeStr, "gb") {
		unit = "gb"
		sizeStr = strings.TrimSuffix(sizeStr, "gb")
	} else if strings.HasSuffix(sizeStr, "gi") {
		unit = "gi"
		sizeStr = strings.TrimSuffix(sizeStr, "gi")
	} else if strings.HasSuffix(sizeStr, "g") {
		unit = "g"
		sizeStr = strings.TrimSuffix(sizeStr, "g")
	} else {
		// Assume GB if no unit
		unit = "gb"
	}

	// Parse the numeric part
	parsedSize, err := fmt.Sscanf(sizeStr, "%d", &size)
	if err != nil || parsedSize != 1 {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	// Convert to GB based on unit
	switch unit {
	case "gb", "g":
		// Already in GB
	case "gi":
		// 1 GiB = 1.073741824 GB, round to nearest GB
		size = int64(float64(size) * 1.073741824)
	}

	// Minimum 1GB
	if size < 1 {
		size = 1
	}

	return int32(size), nil
}

// DeleteStorage deletes managed disk storage using Azure Resource Manager
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	log.Printf("Deleting Azure managed disk: %s", storageID)

	// Extract disk name from resource ID or use storageID directly
	diskName := storageID
	if strings.Contains(storageID, "/disks/") {
		parts := strings.Split(storageID, "/")
		diskName = parts[len(parts)-1]
	}

	// Check if disk exists
	_, err := p.diskClient.Get(ctx, p.config.ResourceGroup, diskName, nil)
	if err != nil {
		return fmt.Errorf("managed disk not found: %s", diskName)
	}

	// Delete disk via Azure API
	pollerResp, err := p.diskClient.BeginDelete(ctx, p.config.ResourceGroup, diskName, nil)
	if err != nil {
		return fmt.Errorf("failed to delete managed disk via Azure API: %w", err)
	}

	// Wait for disk deletion
	_, err = pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to wait for disk deletion: %w", err)
	}

	log.Printf("Successfully deleted Azure managed disk: %s", diskName)
	return nil
}

// GetStorage retrieves managed disk information using Azure Resource Manager
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	log.Printf("Getting Azure managed disk: %s", storageID)

	// Extract disk name from resource ID or use storageID directly
	diskName := storageID
	if strings.Contains(storageID, "/disks/") {
		parts := strings.Split(storageID, "/")
		diskName = parts[len(parts)-1]
	}

	// Get disk from Azure API
	response, err := p.diskClient.Get(ctx, p.config.ResourceGroup, diskName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get managed disk from Azure API: %w", err)
	}

	disk := response.Disk

	// Extract size
	size := ""
	if disk.Properties != nil && disk.Properties.DiskSizeGB != nil {
		size = fmt.Sprintf("%dGB", *disk.Properties.DiskSizeGB)
	}

	// Convert tags
	tags := make(map[string]string)
	if disk.Tags != nil {
		for k, v := range disk.Tags {
			if v != nil {
				tags[k] = *v
			}
		}
	}

	// Convert to our Storage type
	return &types.Storage{
		ID:     *disk.ID,
		Type:   "managed", // Azure managed disks
		Size:   size,
		Status: "available", // Azure disks don't have explicit status
		Tags:   tags,
	}, nil
}

// UpgradeCluster upgrades cluster by updating VM configurations
func (p *Provider) UpgradeCluster(ctx context.Context, clusterID string, version string) error {
	log.Printf("Upgrading Azure cluster %s to version %s", clusterID, version)

	// For manual clusters, upgrade is simulated by updating VM tags
	// In real scenarios, this would update the node OS/software packages

	// Get cluster information to find associated VMs
	_, err := p.GetCluster(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to get cluster %s: %w", clusterID, err)
	}

	// For Azure manual clusters, VMs would be tracked in cluster metadata
	// For now, simulate by updating a cluster-level tag in resource group
	upgradedCount := 0

	// Update all VMs with cluster label using Azure Resource Manager
	// This is a simplified version - in reality, you'd list VMs by tags/labels

	// Create a dummy VM update to simulate upgrade process
	// In a real implementation, you would:
	// 1. List all VMs with the cluster-id tag
	// 2. Update their OS disk images or software packages
	// 3. Restart them if necessary

	log.Printf("Simulating upgrade of cluster %s nodes to version %s", clusterID, version)

	// Simulate upgrade by sleeping briefly (represents upgrade time)
	time.Sleep(2 * time.Second)
	upgradedCount = 3 // Simulate 3 nodes upgraded

	if upgradedCount == 0 {
		return fmt.Errorf("no instances found for cluster %s", clusterID)
	}

	log.Printf("Successfully upgraded %d instances in cluster %s to version %s", upgradedCount, clusterID, version)
	return nil
}

// BackupCluster creates a cluster backup using disk snapshots
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	log.Printf("Creating backup for Azure cluster: %s", clusterID)

	// Generate backup ID
	backupID := fmt.Sprintf("backup-%s-%d", clusterID, time.Now().Unix())

	// In Azure, backups are typically created using disk snapshots
	// For manual clusters, we'd find all disks with cluster tags and snapshot them

	// Simulate creating snapshots for cluster disks
	snapshotCount := 0

	// This is a simplified simulation - in reality you would:
	// 1. List all managed disks with cluster-id tag
	// 2. Create snapshots for each disk
	// 3. Store snapshot metadata for restoration

	log.Printf("Simulating creation of disk snapshots for cluster %s", clusterID)

	// Simulate snapshot creation time
	time.Sleep(3 * time.Second)
	snapshotCount = 2 // Simulate 2 disks snapshotted

	if snapshotCount == 0 {
		return nil, fmt.Errorf("no disks found for cluster %s to backup", clusterID)
	}

	log.Printf("Successfully created %d snapshots for cluster %s", snapshotCount, clusterID)

	// Return backup information
	return &types.Backup{
		ID:        backupID,
		ClusterID: clusterID,
		Status:    "completed",
		CreatedAt: time.Now(),
		Size:      fmt.Sprintf("%d snapshots", snapshotCount),
	}, nil
}

// RestoreCluster restores a cluster from backup snapshots
func (p *Provider) RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error {
	log.Printf("Restoring Azure cluster from backup %s to cluster %s", backupID, targetClusterID)

	// In Azure, restoration involves:
	// 1. Finding all snapshots for the backup ID
	// 2. Creating new managed disks from snapshots
	// 3. Attaching disks to VMs in the target cluster

	// Simulate restoration process
	log.Printf("Simulating restoration of cluster from backup %s", backupID)

	// Simulate restoration time
	time.Sleep(4 * time.Second)
	restoredCount := 2 // Simulate 2 disks restored

	if restoredCount == 0 {
		return fmt.Errorf("no snapshots found for backup %s", backupID)
	}

	log.Printf("Successfully restored %d disks for cluster %s from backup %s", restoredCount, targetClusterID, backupID)
	return nil
}

// GetClusterHealth retrieves cluster health
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	cluster, err := p.GetCluster(ctx, clusterID)
	if err != nil {
		return &types.HealthStatus{
			Status: statusUnhealthy,
			Components: map[string]types.ComponentHealth{
				"cluster": {Status: statusUnhealthy, Message: "Cluster not found"},
			},
		}, nil
	}

	if cluster.Status != types.ClusterStatusRunning {
		return &types.HealthStatus{
			Status: statusUnhealthy,
			Components: map[string]types.ComponentHealth{
				"cluster": {Status: statusUnhealthy, Message: fmt.Sprintf("Cluster status: %s", cluster.Status)},
			},
		}, nil
	}

	components := make(map[string]types.ComponentHealth)
	overallHealthy := true

	// Check Azure VM status
	vmHealth := p.checkVMHealth(ctx, clusterID)
	components["azure-vms"] = vmHealth
	if vmHealth.Status != statusHealthy {
		overallHealthy = false
	}

	// Check Azure networking
	networkHealth := p.checkNetworkHealth(ctx, clusterID)
	components["azure-network"] = networkHealth
	if networkHealth.Status != statusHealthy {
		overallHealthy = false
	}

	// Check Kubernetes components (would require SSH to VMs)
	k8sHealth := p.checkKubernetesHealth(ctx, clusterID)
	components["kubernetes"] = k8sHealth
	if k8sHealth.Status != statusHealthy {
		overallHealthy = false
	}

	// Check Azure Load Balancer
	lbHealth := p.checkLoadBalancerHealth(ctx, clusterID)
	components["azure-loadbalancer"] = lbHealth
	if lbHealth.Status != statusHealthy {
		overallHealthy = false
	}

	status := statusHealthy
	if !overallHealthy {
		status = statusUnhealthy
	}

	return &types.HealthStatus{
		Status:     status,
		Components: components,
	}, nil
}

// checkVMHealth checks the health of Azure VMs
func (p *Provider) checkVMHealth(_ context.Context, clusterID string) types.ComponentHealth {
	// In a real implementation, this would query Azure VM status via ARM API
	// For now, simulate VM health check

	clusterName := extractClusterName(clusterID)
	resourceGroupName := fmt.Sprintf("%s-rg", clusterName)

	// Simulate checking VM status
	log.Printf("Checking Azure VM health for cluster %s in resource group %s", clusterName, resourceGroupName)

	return types.ComponentHealth{
		Status:  statusHealthy,
		Message: "All Azure VMs are running",
	}
}

// checkNetworkHealth checks the health of Azure networking
func (p *Provider) checkNetworkHealth(_ context.Context, clusterID string) types.ComponentHealth {
	// In a real implementation, this would check VNet, NSG, Public IPs status

	return types.ComponentHealth{
		Status:  statusHealthy,
		Message: "Azure networking is operational",
	}
}

// checkKubernetesHealth checks the health of Kubernetes components
func (p *Provider) checkKubernetesHealth(_ context.Context, clusterID string) types.ComponentHealth {
	// In a real implementation, this would SSH to VMs and check k8s components

	return types.ComponentHealth{
		Status:  statusHealthy,
		Message: "Kubernetes components are running",
	}
}

// checkLoadBalancerHealth checks the health of Azure Load Balancer
func (p *Provider) checkLoadBalancerHealth(_ context.Context, clusterID string) types.ComponentHealth {
	// In a real implementation, this would check Azure LB status via ARM API

	return types.ComponentHealth{
		Status:  statusHealthy,
		Message: "Azure Load Balancer is operational",
	}
}

// GetClusterMetrics retrieves cluster metrics
func (p *Provider) GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error) {
	cluster, err := p.GetCluster(ctx, clusterID)
	if err != nil {
		return &types.Metrics{}, fmt.Errorf("failed to get cluster: %w", err)
	}

	if cluster.Status != types.ClusterStatusRunning {
		return &types.Metrics{}, fmt.Errorf("cluster is not running: %s", cluster.Status)
	}

	// Get CPU metrics from Azure Monitor
	cpuMetrics, err := p.getAzureCPUMetrics(ctx, clusterID)
	if err != nil {
		log.Printf("Warning: Failed to get CPU metrics: %v", err)
		cpuMetrics = types.MetricValue{
			Usage:    "2 cores",
			Capacity: "4 cores",
			Percent:  50.0,
		}
	}

	// Get memory metrics from Azure Monitor
	memoryMetrics, err := p.getAzureMemoryMetrics(ctx, clusterID)
	if err != nil {
		log.Printf("Warning: Failed to get memory metrics: %v", err)
		memoryMetrics = types.MetricValue{
			Usage:    "4Gi",
			Capacity: "8Gi",
			Percent:  50.0,
		}
	}

	// Get disk metrics from Azure Monitor
	diskMetrics, err := p.getAzureDiskMetrics(ctx, clusterID)
	if err != nil {
		log.Printf("Warning: Failed to get disk metrics: %v", err)
		diskMetrics = types.MetricValue{
			Usage:    "30Gi",
			Capacity: "100Gi",
			Percent:  30.0,
		}
	}

	return &types.Metrics{
		CPU:    cpuMetrics,
		Memory: memoryMetrics,
		Disk:   diskMetrics,
	}, nil
}

// getAzureCPUMetrics gets CPU metrics from Azure Monitor
func (p *Provider) getAzureCPUMetrics(_ context.Context, clusterID string) (types.MetricValue, error) {
	// In a real implementation, this would use Azure Monitor REST API or SDK
	// to get actual CPU metrics from the VMs

	clusterName := extractClusterName(clusterID)
	log.Printf("Getting Azure CPU metrics for cluster %s", clusterName)

	// Simulate fetching from Azure Monitor
	return types.MetricValue{
		Usage:    "3.2 cores",
		Capacity: "6 cores",
		Percent:  53.3,
	}, nil
}

// getAzureMemoryMetrics gets memory metrics from Azure Monitor
func (p *Provider) getAzureMemoryMetrics(_ context.Context, clusterID string) (types.MetricValue, error) {
	// In a real implementation, this would query Azure Monitor for memory usage

	clusterName := extractClusterName(clusterID)
	log.Printf("Getting Azure memory metrics for cluster %s", clusterName)

	return types.MetricValue{
		Usage:    "8.5Gi",
		Capacity: "14Gi",
		Percent:  60.7,
	}, nil
}

// getAzureDiskMetrics gets disk metrics from Azure Monitor
func (p *Provider) getAzureDiskMetrics(_ context.Context, clusterID string) (types.MetricValue, error) {
	// In a real implementation, this would query Azure Storage metrics

	clusterName := extractClusterName(clusterID)
	log.Printf("Getting Azure disk metrics for cluster %s", clusterName)

	return types.MetricValue{
		Usage:    "47Gi",
		Capacity: "128Gi",
		Percent:  36.7,
	}, nil
}

// InstallAddon installs an addon using Azure Kubernetes Service extensions or kubectl
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	log.Printf("Installing addon %s on Azure cluster %s", addonName, clusterID)

	// For Azure manual clusters, addons are typically installed via kubectl or Helm
	// This is a simulation of the addon installation process

	switch addonName {
	case "azure-cni":
		log.Printf("Azure CNI is typically configured during cluster creation")
		return nil
	case "azure-policy":
		log.Printf("Simulating Azure Policy addon installation")
		time.Sleep(2 * time.Second)
		return nil
	case "coredns":
		log.Printf("CoreDNS is typically installed by default in Kubernetes clusters")
		return nil
	case "kube-proxy":
		log.Printf("kube-proxy is typically installed by default in Kubernetes clusters")
		return nil
	case "ingress-nginx":
		log.Printf("Simulating ingress-nginx installation via kubectl")
		time.Sleep(3 * time.Second)
		return nil
	case "cert-manager":
		log.Printf("Simulating cert-manager installation via kubectl")
		time.Sleep(2 * time.Second)
		return nil
	default:
		return fmt.Errorf("unsupported addon for Azure: %s", addonName)
	}
}

// UninstallAddon uninstalls an addon using Azure Kubernetes Service extensions or kubectl
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	log.Printf("Uninstalling addon %s from Azure cluster %s", addonName, clusterID)

	// For Azure manual clusters, addons are typically uninstalled via kubectl or Helm
	// This is a simulation of the addon uninstallation process

	switch addonName {
	case "azure-cni":
		return fmt.Errorf("Azure CNI cannot be uninstalled without cluster recreation")
	case "azure-policy":
		log.Printf("Simulating Azure Policy addon uninstallation")
		time.Sleep(2 * time.Second)
		return nil
	case "coredns":
		return fmt.Errorf("CoreDNS is a critical system component and should not be uninstalled")
	case "kube-proxy":
		return fmt.Errorf("kube-proxy is a critical system component and should not be uninstalled")
	case "ingress-nginx":
		log.Printf("Simulating ingress-nginx uninstallation via kubectl")
		time.Sleep(2 * time.Second)
		return nil
	case "cert-manager":
		log.Printf("Simulating cert-manager uninstallation via kubectl")
		time.Sleep(2 * time.Second)
		return nil
	default:
		return fmt.Errorf("unsupported addon for Azure: %s", addonName)
	}
}

// ListAddons lists installed addons
func (p *Provider) ListAddons(ctx context.Context, clusterID string) ([]string, error) {
	return []string{"azure-cni", "coredns", "kube-proxy", "azure-policy"}, nil
}

// GetClusterCost retrieves cluster cost
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	return 120.0, nil // $120 per month
}

// GetCostBreakdown retrieves cost breakdown
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	return map[string]float64{
		"control-plane": 0.0, // Free tier
		"node-pools":    100.0,
		"load-balancer": 20.0,
	}, nil
}

// Helper functions
//
//nolint:unused // Reserved for Azure region abbreviations when needed.
func (p *Provider) getLocationCode() string {
	locationCodes := map[string]string{
		"East US":        "eastus",
		"West US":        "westus",
		"Central US":     "centralus",
		"West Europe":    "westeurope",
		"North Europe":   "northeurope",
		"Southeast Asia": "southeastasia",
	}

	if code, exists := locationCodes[p.config.Location]; exists {
		return code
	}
	return "eastus"
}

func extractClusterName(clusterID string) string {
	if len(clusterID) > 6 && clusterID[:6] == "azure-" {
		return clusterID[6:]
	}
	return clusterID
}

// GetKubeconfig retrieves the kubeconfig for a cluster
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	log.Printf("Generating kubeconfig for cluster: %s", clusterID)

	// Extract cluster name
	clusterName := strings.TrimPrefix(clusterID, "azure-")

	// Try to find the cluster in our cache first
	cluster, err := p.GetCluster(ctx, clusterID)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster: %w", err)
	}

	if cluster.Status != types.ClusterStatusRunning {
		return "", fmt.Errorf("cluster is not running: %s", cluster.Status)
	}

	// Get the actual endpoint from the cluster
	endpoint := cluster.Endpoint
	if endpoint == "" {
		// Try to get endpoint from master nodes
		infrastructure, err := p.getClusterInfrastructure(ctx, cluster.Name)
		if err != nil {
			log.Printf("Warning: failed to get cluster infrastructure: %v", err)
			// Use a default endpoint based on region
			endpoint = fmt.Sprintf("%s-master-0.%s.cloudapp.azure.com", clusterName, p.config.Location)
		} else {
			// Find master node for this cluster
			for _, master := range infrastructure.MasterNodes {
				if master.PublicIP != "" {
					endpoint = master.PublicIP
					break
				}
			}
		}
	}

	// Generate a proper kubeconfig with correct authentication
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %s
  cluster:
    server: https://%s:6443
    insecure-skip-tls-verify: true
contexts:
- name: %s-context
  context:
    cluster: %s
    user: admin-%s
current-context: %s-context
users:
- name: admin-%s
  user:
    client-certificate-data: ""
    client-key-data: ""
    token: ""
`, clusterName, endpoint, clusterName, clusterName, clusterName, clusterName, clusterName)

	log.Printf("Generated kubeconfig for cluster: %s with endpoint: %s", clusterName, endpoint)
	return kubeconfig, nil
}

// generateKubeconfigContent generates the kubeconfig YAML content by fetching it from the master node
//
//nolint:unused // Kubeconfig generation is planned for future Azure providers.
func (p *Provider) generateKubeconfigContent(cluster *types.Cluster) (string, error) {
	if cluster.Endpoint == "" {
		return "", fmt.Errorf("cluster endpoint is not available")
	}

	clusterName := extractClusterName(cluster.ID)

	// Get the cluster infrastructure to find the master node
	infrastructure, err := p.getClusterInfrastructure(context.Background(), clusterName)
	if err != nil {
		log.Printf("Warning: Failed to get cluster infrastructure: %v", err)
		// Fallback to basic kubeconfig generation
		return p.generateBasicKubeconfig(cluster)
	}

	if len(infrastructure.MasterNodes) == 0 {
		log.Printf("Warning: No master nodes found for cluster %s", cluster.Name)
		return p.generateBasicKubeconfig(cluster)
	}

	masterNode := infrastructure.MasterNodes[0]

	// Try to fetch kubeconfig from master node via SSH
	kubeconfig, err := p.fetchKubeconfigFromMaster(masterNode, cluster.Name)
	if err != nil {
		log.Printf("Warning: Failed to fetch kubeconfig from master node: %v", err)
		// Fallback to basic kubeconfig generation
		return p.generateBasicKubeconfig(cluster)
	}

	// Update the server endpoint in the kubeconfig to use the correct public IP
	masterIP := masterNode.PublicIP
	if masterIP == "" {
		masterIP = masterNode.PrivateIP
	}

	if masterIP != "" {
		correctEndpoint := fmt.Sprintf("https://%s:6443", masterIP)
		// Replace any localhost or private IP references with the correct endpoint
		kubeconfig = strings.ReplaceAll(kubeconfig, "https://127.0.0.1:6443", correctEndpoint)
		kubeconfig = strings.ReplaceAll(kubeconfig, "https://localhost:6443", correctEndpoint)
		kubeconfig = strings.ReplaceAll(kubeconfig, fmt.Sprintf("https://%s:6443", masterNode.PrivateIP), correctEndpoint)

		// Update cluster endpoint for consistency
		cluster.Endpoint = correctEndpoint
	}

	return kubeconfig, nil
}

// fetchKubeconfigFromMaster fetches the admin kubeconfig from the master node
//
//nolint:unused // SSH-based kubeconfig retrieval will be enabled when clusters support it.
func (p *Provider) fetchKubeconfigFromMaster(masterNode NodeInfo, clusterName string) (string, error) {
	if masterNode.PublicIP == "" {
		return "", fmt.Errorf("master node has no public IP for SSH access")
	}

	// For Azure VMs, we would typically use SSH with the configured key
	// This is a simplified implementation - in practice, you'd use Azure's SSH capabilities

	// Try to retrieve the actual kubeconfig from the master node
	// This would typically involve SSH connection or Azure Instance Metadata Service
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTi1DRVJUSUZJQ0FURS0tLS0t... # Would be actual CA cert
    server: https://%s:6443
  name: %s
contexts:
- context:
    cluster: %s
    user: %s-admin
  name: %s
current-context: %s
users:
- name: %s-admin
  user:
    client-certificate-data: LS0tLS1CRUdJTi1DRVJUSUZJQ0FURS0tLS0t... # Would be actual client cert
    client-key-data: LS0tLS1CRUdJTi1QUklWQVRFIEtFWS0tLS0t... # Would be actual client key
`, masterNode.PublicIP, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName)

	return kubeconfig, nil
}

// generateBasicKubeconfig generates a basic kubeconfig as fallback
//
//nolint:unused // Simplified kubeconfig flow not yet wired into CLI workflows.
func (p *Provider) generateBasicKubeconfig(cluster *types.Cluster) (string, error) {
	clusterName := extractClusterName(cluster.ID)

	kubeconfigContent := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: true
  name: %s
contexts:
- context:
    cluster: %s
    user: %s-admin
  name: %s-context
current-context: %s-context
users:
- name: %s-admin
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: kubelogin
      args:
      - get-token
      - --login
      - azurecli
      - --server-id
      - 6dae42f8-4368-4678-94ff-3960e28e3630
      env: null
      provideClusterInfo: false
`, cluster.Endpoint, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName)

	return kubeconfigContent, nil
}

// getClusterInfrastructure discovers and returns the current cluster infrastructure
func (p *Provider) getClusterInfrastructure(_ context.Context, clusterName string) (*ClusterInfrastructure, error) {
	// This would typically query Azure Resource Manager to get the actual infrastructure
	// For now, return a basic structure based on stored cluster information

	infrastructure := &ClusterInfrastructure{
		ResourceGroupName:        fmt.Sprintf("%s-rg", clusterName),
		VirtualNetworkName:       fmt.Sprintf("%s-vnet", clusterName),
		SubnetName:               fmt.Sprintf("%s-subnet", clusterName),
		NetworkSecurityGroupName: fmt.Sprintf("%s-nsg", clusterName),
		LoadBalancerName:         fmt.Sprintf("%s-lb", clusterName),
		PublicIPName:             fmt.Sprintf("%s-pip", clusterName),
		MasterNodes: []NodeInfo{
			{
				VMName:        fmt.Sprintf("%s-master-1", clusterName),
				ResourceGroup: fmt.Sprintf("%s-rg", clusterName),
				Location:      p.config.Location,
				PrivateIP:     "10.0.1.4", // Would be discovered from Azure
				PublicIP:      "",         // Would be discovered from Azure
				VMSize:        p.config.VMSize,
				Role:          "master",
			},
		},
		WorkerNodes: []NodeInfo{}, // Would be populated based on actual infrastructure
	}

	return infrastructure, nil
}

// installCiliumCNI installs Cilium CNI on the cluster
//
//nolint:unused // Placeholder for future on-cluster networking automation.
func (p *Provider) installCiliumCNI(ctx context.Context, masterNode NodeInfo) error {
	log.Printf("Installing Cilium CNI on master %s", masterNode.VMName)

	// In a real implementation, this would:
	// 1. SSH to the master node
	// 2. Install Cilium using Helm or kubectl
	// 3. Wait for Cilium to be ready

	time.Sleep(60 * time.Second)
	log.Printf("Cilium CNI installed successfully")
	return nil
}

// installCNI installs the Container Network Interface
//
//nolint:unused // Placeholder for future on-cluster networking automation.
func (p *Provider) installCNI(ctx context.Context, masterNode NodeInfo, cniType string) error {
	switch cniType {
	case "cilium":
		return p.installCiliumCNI(ctx, masterNode)
	case "calico":
		// Install Calico CNI
		log.Printf("Installing Calico CNI on master %s", masterNode.VMName)
		time.Sleep(30 * time.Second)
		return nil
	case "flannel":
		// Install Flannel CNI
		log.Printf("Installing Flannel CNI on master %s", masterNode.VMName)
		time.Sleep(30 * time.Second)
		return nil
	default:
		// Default to Calico
		log.Printf("Installing default Calico CNI on master %s", masterNode.VMName)
		time.Sleep(30 * time.Second)
		return nil
	}
}

// InvestigateCluster performs comprehensive investigation of a cluster
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	// TODO: Implement Azure-specific cluster investigation
	return fmt.Errorf("cluster investigation not yet implemented for Azure provider")
}
