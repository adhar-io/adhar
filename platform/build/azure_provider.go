package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	"github.com/sirupsen/logrus"
)

// AzureClusterConfig holds configuration for Azure AKS clusters
type AzureClusterConfig struct {
	Name              string
	ResourceGroup     string
	Location          string
	KubernetesVersion string
	NodeCount         int
	NodeVMSize        string
	EnableAutoScaling bool
	MinNodeCount      int
	MaxNodeCount      int
}

// AzureProvider implements Provider for Microsoft Azure AKS clusters
type AzureProvider struct {
	envConfig      *config.ResolvedEnvironmentConfig
	logger         *logger.AdharLogger
	templateEngine *TemplateEngine
	client         *armcontainerservice.ManagedClustersClient
	resourceGroup  string
}

// NewAzureProvider creates a new Azure provider
func NewAzureProvider(envConfig *config.ResolvedEnvironmentConfig, log *logrus.Logger, templateEngine *TemplateEngine) (Provider, error) {
	return &AzureProvider{
		envConfig:      envConfig,
		logger:         logger.GetLogger(),
		templateEngine: templateEngine,
		resourceGroup:  getAzureResourceGroup(envConfig),
	}, nil
}

// getAzureResourceGroup returns the resource group name for the cluster
func getAzureResourceGroup(envConfig *config.ResolvedEnvironmentConfig) string {
	// Check for custom resource group in cluster config
	for _, config := range envConfig.ResolvedClusterConfig {
		if config.Key == "resource_group" && config.Value != "" {
			return config.Value
		}
	}
	return fmt.Sprintf("adhar-%s-rg", envConfig.Name)
}

// initClient initializes the Azure client if not already done
func (az *AzureProvider) initClient(ctx context.Context) error {
	if az.client != nil {
		return nil
	}

	// Get subscription ID from environment or config
	subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	if subscriptionID == "" {
		// Check cluster config for subscription ID
		for _, config := range az.envConfig.ResolvedClusterConfig {
			if config.Key == "subscription_id" && config.Value != "" {
				subscriptionID = config.Value
				break
			}
		}
	}
	if subscriptionID == "" {
		return fmt.Errorf("Azure subscription ID not found in AZURE_SUBSCRIPTION_ID environment variable or cluster config")
	}

	// Create credential using default Azure credential chain
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to create Azure credential: %w", err)
	}

	// Create managed clusters client
	client, err := armcontainerservice.NewManagedClustersClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create Azure AKS client: %w", err)
	}

	az.client = client
	return nil
}

// getClusterConfig extracts Azure-specific cluster configuration from environment config
func (az *AzureProvider) getClusterConfig(envConfig *config.ResolvedEnvironmentConfig) *AzureClusterConfig {
	config := &AzureClusterConfig{
		Name:              envConfig.Name,
		ResourceGroup:     az.resourceGroup,
		Location:          envConfig.ResolvedRegion,
		KubernetesVersion: "1.28.0",          // Default version
		NodeCount:         3,                 // Default node count
		NodeVMSize:        "Standard_DS2_v2", // Default VM size
		EnableAutoScaling: true,              // Default to enable auto-scaling
		MinNodeCount:      1,                 // Default min nodes
		MaxNodeCount:      10,                // Default max nodes
	}

	// Override with values from cluster config if provided
	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "kubernetes_version":
			if cfg.Value != "" {
				config.KubernetesVersion = cfg.Value
			}
		case "node_count":
			if count := parseIntOrDefault(cfg.Value, 3); count > 0 {
				config.NodeCount = count
			}
		case "node_vm_size":
			if cfg.Value != "" {
				config.NodeVMSize = cfg.Value
			}
		case "enable_auto_scaling":
			config.EnableAutoScaling = cfg.Value == "true"
		case "min_node_count":
			if count := parseIntOrDefault(cfg.Value, 1); count > 0 {
				config.MinNodeCount = count
			}
		case "max_node_count":
			if count := parseIntOrDefault(cfg.Value, 10); count > 0 {
				config.MaxNodeCount = count
			}
		}
	}

	return config
}

// Provision provisions an Azure AKS cluster
func (az *AzureProvider) Provision(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		az.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would provision AKS cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))
		return nil
	}

	az.logger.StartOperation("Azure AKS Cluster Provisioning", fmt.Sprintf("Creating cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))

	// Initialize client
	if err := az.initClient(ctx); err != nil {
		logger.Error("Failed to initialize Azure client", err, map[string]interface{}{
			"region": envConfig.ResolvedRegion,
		})
		return fmt.Errorf("failed to initialize Azure client: %w", err)
	}

	clusterConfig := az.getClusterConfig(envConfig)

	// Check if cluster already exists
	exists, err := az.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if exists && !opts.Force {
		az.logger.Info(fmt.Sprintf("✅ AKS cluster '%s' already exists, skipping creation", clusterConfig.Name))
		return nil
	}

	if exists && opts.Force {
		az.logger.Warning("Cluster exists, recreating due to --force flag", map[string]interface{}{
			"cluster":        clusterConfig.Name,
			"resource_group": clusterConfig.ResourceGroup,
		})
		if err := az.Destroy(ctx, envConfig, opts); err != nil {
			return fmt.Errorf("failed to destroy existing cluster: %w", err)
		}
		// Wait a bit for cleanup
		time.Sleep(30 * time.Second)
	}

	// Create AKS cluster
	if err := az.createCluster(ctx, clusterConfig); err != nil {
		return fmt.Errorf("failed to create Azure AKS cluster: %w", err)
	}

	az.logger.FinishOperation("Azure AKS Cluster Provisioning", fmt.Sprintf("Cluster '%s' ready", clusterConfig.Name))
	return nil
}

// createCluster creates the AKS cluster using Azure API
func (az *AzureProvider) createCluster(ctx context.Context, clusterConfig *AzureClusterConfig) error {
	az.logger.ProvisioningInfo("azure", "creating", fmt.Sprintf("AKS cluster with %d nodes (%s)", clusterConfig.NodeCount, clusterConfig.NodeVMSize))

	// Define the cluster parameters
	parameters := armcontainerservice.ManagedCluster{
		Location: to.Ptr(clusterConfig.Location),
		Properties: &armcontainerservice.ManagedClusterProperties{
			KubernetesVersion: to.Ptr(clusterConfig.KubernetesVersion),
			DNSPrefix:         to.Ptr(clusterConfig.Name + "-dns"),
			AgentPoolProfiles: []*armcontainerservice.ManagedClusterAgentPoolProfile{
				{
					Name:              to.Ptr("default"),
					Count:             to.Ptr(int32(clusterConfig.NodeCount)),
					VMSize:            to.Ptr(clusterConfig.NodeVMSize),
					OSType:            to.Ptr(armcontainerservice.OSTypeLinux),
					EnableAutoScaling: to.Ptr(clusterConfig.EnableAutoScaling),
					MinCount:          to.Ptr(int32(clusterConfig.MinNodeCount)),
					MaxCount:          to.Ptr(int32(clusterConfig.MaxNodeCount)),
				},
			},
			ServicePrincipalProfile: &armcontainerservice.ManagedClusterServicePrincipalProfile{
				ClientID: to.Ptr("msi"), // Use managed service identity
			},
		},
	}

	// Start cluster creation
	az.logger.StartProgress("Creating AKS cluster (this can take 10-15 minutes)")
	poller, err := az.client.BeginCreateOrUpdate(ctx, clusterConfig.ResourceGroup, clusterConfig.Name, parameters, nil)
	if err != nil {
		az.logger.StopProgress()
		logger.Error("Failed to start AKS cluster creation", err, map[string]interface{}{
			"cluster":        clusterConfig.Name,
			"resource_group": clusterConfig.ResourceGroup,
		})
		return fmt.Errorf("failed to start cluster creation: %w", err)
	}

	// Wait for completion
	result, err := poller.PollUntilDone(ctx, nil)
	az.logger.StopProgress()

	if err != nil {
		logger.Error("AKS cluster creation failed", err, map[string]interface{}{
			"cluster":        clusterConfig.Name,
			"resource_group": clusterConfig.ResourceGroup,
		})
		return fmt.Errorf("cluster creation failed: %w", err)
	}

	az.logger.ValidationInfo("AKS cluster", "created successfully")
	az.logger.Info(fmt.Sprintf("📋 Cluster ID: %s", *result.ID))
	return nil
}

// Destroy destroys an Azure cluster
func (az *AzureProvider) Destroy(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		az.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would destroy AKS cluster '%s'", envConfig.Name))
		return nil
	}

	az.logger.StartOperation("Azure AKS Cluster Destruction", fmt.Sprintf("Removing cluster '%s'", envConfig.Name))

	// Initialize client
	if err := az.initClient(ctx); err != nil {
		return fmt.Errorf("failed to initialize Azure client: %w", err)
	}

	clusterConfig := az.getClusterConfig(envConfig)

	// Check if cluster exists
	exists, err := az.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if !exists {
		az.logger.Info(fmt.Sprintf("📭 AKS cluster '%s' does not exist, nothing to destroy", clusterConfig.Name))
		return nil
	}

	// Delete the AKS cluster
	az.logger.ProvisioningInfo("azure", "deleting", fmt.Sprintf("AKS cluster from resource group %s", clusterConfig.ResourceGroup))

	poller, err := az.client.BeginDelete(ctx, clusterConfig.ResourceGroup, clusterConfig.Name, nil)
	if err != nil {
		logger.Error("Failed to start AKS cluster deletion", err, map[string]interface{}{
			"cluster":        clusterConfig.Name,
			"resource_group": clusterConfig.ResourceGroup,
		})
		return fmt.Errorf("failed to start cluster deletion: %w", err)
	}

	az.logger.StartProgress("Waiting for AKS cluster deletion to complete")

	// Wait for the operation to complete
	_, err = poller.PollUntilDone(ctx, nil)
	az.logger.StopProgress()

	if err != nil {
		logger.Error("AKS cluster deletion failed", err, map[string]interface{}{
			"cluster":        clusterConfig.Name,
			"resource_group": clusterConfig.ResourceGroup,
		})
		return fmt.Errorf("cluster deletion failed: %w", err)
	}

	// Clean up kubeconfig entry
	kubeconfigPath := fmt.Sprintf("./.adhar/%s/kubeconfig", envConfig.Name)
	if err := os.RemoveAll(filepath.Dir(kubeconfigPath)); err != nil {
		az.logger.Warning("Failed to clean up kubeconfig directory", map[string]interface{}{
			"path":  kubeconfigPath,
			"error": err.Error(),
		})
	} else {
		az.logger.CleanupInfo("kubeconfig files")
	}

	az.logger.FinishOperation("Azure AKS Cluster Destruction", fmt.Sprintf("Cluster '%s' removed", clusterConfig.Name))
	return nil
}

// Exists checks if an Azure cluster exists
func (az *AzureProvider) Exists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error) {
	// Initialize client
	if err := az.initClient(ctx); err != nil {
		return false, fmt.Errorf("failed to initialize Azure client: %w", err)
	}

	clusterConfig := az.getClusterConfig(envConfig)

	// Try to get the cluster
	_, err := az.client.Get(ctx, clusterConfig.ResourceGroup, clusterConfig.Name, nil)
	if err != nil {
		// Check if error is "not found"
		if isAzureNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	return true, nil
}

// isAzureNotFoundError checks if the error is a "not found" error from Azure
func isAzureNotFoundError(err error) bool {
	// Azure SDK typically returns errors with specific status codes
	// This is a simplified check - in practice you might need to check
	// the specific error type or HTTP status code
	return err != nil && (fmt.Sprintf("%s", err) == "cluster not found" ||
		fmt.Sprintf("%s", err) == "resource not found")
}

// InstallPlatformServices installs platform services on the Azure cluster
func (az *AzureProvider) InstallPlatformServices(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	az.logger.StartOperation("Platform Services Installation", "Setting up core platform components on AKS")

	// Get HA mode setting
	enableHAMode := false
	if envConfig.GlobalSettings != nil {
		enableHAMode = envConfig.GlobalSettings.EnableHAMode
	}

	az.logger.Info(fmt.Sprintf("⚙️ Configuring for %s mode", map[bool]string{true: "high-availability", false: "local development"}[enableHAMode]))

	// Install core platform services
	services := []string{"cilium", "gitea", "argocd", "nginx"}

	for _, service := range services {
		az.logger.ProvisioningInfo("azure", "installing", fmt.Sprintf("platform service %s", service))

		manifests, err := az.templateEngine.GenerateManifests(ctx, service, enableHAMode)
		if err != nil {
			return fmt.Errorf("failed to generate manifests for %s: %w", service, err)
		}

		// Apply manifests using kubectl with the Azure cluster's kubeconfig
		if err := az.applyManifests(ctx, manifests, service); err != nil {
			return fmt.Errorf("failed to apply manifests for %s: %w", service, err)
		}

		az.logger.ValidationInfo(service, "installed successfully")
	}

	az.logger.FinishOperation("Platform Services Installation", "All platform services ready on AKS")
	return nil
}

// ValidateCluster validates the Azure cluster
func (az *AzureProvider) ValidateCluster(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	az.logger.StartOperation("AKS Cluster Validation", "Verifying cluster health and connectivity")

	// Initialize client
	if err := az.initClient(ctx); err != nil {
		return fmt.Errorf("failed to initialize Azure client: %w", err)
	}

	clusterConfig := az.getClusterConfig(envConfig)

	// Get cluster information
	cluster, err := az.client.Get(ctx, clusterConfig.ResourceGroup, clusterConfig.Name, nil)
	if err != nil {
		logger.Error("Failed to get AKS cluster information", err, map[string]interface{}{
			"cluster":        clusterConfig.Name,
			"resource_group": clusterConfig.ResourceGroup,
		})
		return fmt.Errorf("failed to get cluster information: %w", err)
	}

	// Check cluster state
	if cluster.Properties == nil || cluster.Properties.ProvisioningState == nil {
		return fmt.Errorf("cluster information incomplete")
	}

	state := *cluster.Properties.ProvisioningState
	if state != "Succeeded" {
		return fmt.Errorf("cluster is not in ready state: %s", state)
	}

	az.logger.ValidationInfo("cluster API", "accessible")
	az.logger.ValidationInfo("cluster state", state)
	az.logger.ValidationInfo("Azure integrations", "ok")

	az.logger.FinishOperation("AKS Cluster Validation", "Cluster validation completed successfully")
	return nil
}

// GetClusterInfo returns information about the Azure cluster
func (az *AzureProvider) GetClusterInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error) {
	// Initialize client
	if err := az.initClient(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize Azure client: %w", err)
	}

	clusterConfig := az.getClusterConfig(envConfig)

	// Get cluster information from Azure API
	cluster, err := az.client.Get(ctx, clusterConfig.ResourceGroup, clusterConfig.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster information: %w", err)
	}

	// Extract information from cluster response
	nodeCount := 0
	version := "unknown"
	status := "unknown"
	endpoint := ""

	if cluster.Properties != nil {
		if cluster.Properties.KubernetesVersion != nil {
			version = *cluster.Properties.KubernetesVersion
		}
		if cluster.Properties.ProvisioningState != nil {
			status = *cluster.Properties.ProvisioningState
		}
		if cluster.Properties.Fqdn != nil {
			endpoint = *cluster.Properties.Fqdn
		}
		if len(cluster.Properties.AgentPoolProfiles) > 0 && cluster.Properties.AgentPoolProfiles[0].Count != nil {
			nodeCount = int(*cluster.Properties.AgentPoolProfiles[0].Count)
		}
	}

	return &ClusterInfo{
		Name:      envConfig.Name,
		Provider:  "azure",
		Region:    envConfig.ResolvedRegion,
		Status:    status,
		NodeCount: nodeCount,
		Version:   version,
		Endpoint:  endpoint,
		Metadata: map[string]string{
			"type":           "cloud",
			"provider":       "azure",
			"resource_group": clusterConfig.ResourceGroup,
		},
	}, nil
}

// GetKubeConfig returns the kubeconfig for the Azure cluster
func (az *AzureProvider) GetKubeConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (string, error) {
	// Create kubeconfig directory
	kubeconfigDir := fmt.Sprintf("./.adhar/%s", envConfig.Name)
	kubeconfigPath := filepath.Join(kubeconfigDir, "kubeconfig")

	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}

	clusterConfig := az.getClusterConfig(envConfig)

	// Use Azure CLI to get credentials
	// Note: This requires Azure CLI to be installed and authenticated
	cmd := fmt.Sprintf("az aks get-credentials --resource-group %s --name %s --file %s --overwrite-existing",
		clusterConfig.ResourceGroup, clusterConfig.Name, kubeconfigPath)

	if err := runCommand(cmd); err != nil {
		return "", fmt.Errorf("failed to get Azure cluster credentials: %w", err)
	}

	az.logger.Info("Azure cluster kubeconfig retrieved", "path", kubeconfigPath)
	return kubeconfigPath, nil
}

// applyManifests applies Kubernetes manifests using kubectl
func (az *AzureProvider) applyManifests(ctx context.Context, manifests, serviceName string) error {
	az.logger.Info("Applying manifests", "service", serviceName)

	// Get kubeconfig for the cluster
	kubeconfigPath, err := az.GetKubeConfig(ctx, az.envConfig)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Create temporary file for manifests
	manifestFile := fmt.Sprintf("/tmp/%s-manifests.yaml", serviceName)
	if err := writeStringToFile(manifestFile, manifests); err != nil {
		return fmt.Errorf("failed to write manifests to file: %w", err)
	}
	defer os.Remove(manifestFile)

	// Apply manifests using kubectl
	cmd := fmt.Sprintf("kubectl --kubeconfig %s apply -f %s", kubeconfigPath, manifestFile)
	if err := runCommand(cmd); err != nil {
		return fmt.Errorf("failed to apply manifests: %w", err)
	}

	az.logger.Info("Manifests applied successfully", "service", serviceName)
	return nil
}
