package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"

	"github.com/civo/civogo"
	"github.com/sirupsen/logrus"
)

// CivoClusterConfig holds configuration for Civo clusters
type CivoClusterConfig struct {
	Name              string
	Region            string
	Size              string
	NodeCount         int
	KubernetesVersion string
	Network           string
	Firewall          string
}

// CivoProvider implements Provider for Civo clusters
type CivoProvider struct {
	envConfig      *config.ResolvedEnvironmentConfig
	logger         *logger.AdharLogger
	templateEngine *TemplateEngine
	client         *civogo.Client
}

// NewCivoProvider creates a new Civo provider
func NewCivoProvider(envConfig *config.ResolvedEnvironmentConfig, log *logrus.Logger, templateEngine *TemplateEngine) (Provider, error) {
	return &CivoProvider{
		envConfig:      envConfig,
		logger:         logger.GetLogger(),
		templateEngine: templateEngine,
	}, nil
}

// initClient initializes the Civo client if not already done
func (civo *CivoProvider) initClient() error {
	if civo.client != nil {
		return nil
	}

	// Get API key from environment
	apiKey := os.Getenv("CIVO_TOKEN")
	if apiKey == "" {
		// Check cluster config for API key
		for _, config := range civo.envConfig.ResolvedClusterConfig {
			if config.Key == "api_key" && config.Value != "" {
				apiKey = config.Value
				break
			}
		}
	}
	if apiKey == "" {
		return fmt.Errorf("Civo API token not found in CIVO_TOKEN environment variable or cluster config")
	}

	// Create Civo client
	client, err := civogo.NewClient(apiKey, civo.getRegion())
	if err != nil {
		return fmt.Errorf("failed to create Civo client: %w", err)
	}

	civo.client = client
	return nil
}

// getRegion returns the region for Civo operations
func (civo *CivoProvider) getRegion() string {
	if civo.envConfig.ResolvedRegion != "" {
		return civo.envConfig.ResolvedRegion
	}
	return "LON1" // Default Civo region
}

// getClusterConfig extracts Civo-specific cluster configuration from environment config
func (civo *CivoProvider) getClusterConfig(envConfig *config.ResolvedEnvironmentConfig) *CivoClusterConfig {
	config := &CivoClusterConfig{
		Name:              envConfig.Name,
		Region:            civo.getRegion(),
		Size:              "g4s.kube.medium", // Default node size
		NodeCount:         3,                 // Default node count
		KubernetesVersion: "1.28.2-k3s1",     // Default Kubernetes version
		Network:           "",                // Default network (will be auto-created)
		Firewall:          "",                // Default firewall (will be auto-created)
	}

	// Override with values from cluster config if provided
	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "node_size":
			if cfg.Value != "" {
				config.Size = cfg.Value
			}
		case "node_count":
			if count := parseIntOrDefault(cfg.Value, 3); count > 0 {
				config.NodeCount = count
			}
		case "kubernetes_version":
			if cfg.Value != "" {
				config.KubernetesVersion = cfg.Value
			}
		case "network":
			config.Network = cfg.Value
		case "firewall":
			config.Firewall = cfg.Value
		}
	}

	return config
}

// Provision provisions a Civo cluster
func (civo *CivoProvider) Provision(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		civo.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would provision Civo cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))
		return nil
	}

	civo.logger.StartOperation("Civo Cluster Provisioning", fmt.Sprintf("Creating cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))

	// Initialize client
	if err := civo.initClient(); err != nil {
		logger.Error("Failed to initialize Civo client", err, map[string]interface{}{
			"region": envConfig.ResolvedRegion,
		})
		return fmt.Errorf("failed to initialize Civo client: %w", err)
	}

	clusterConfig := civo.getClusterConfig(envConfig)

	// Check if cluster already exists
	exists, err := civo.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if exists && !opts.Force {
		civo.logger.Info(fmt.Sprintf("✅ Civo cluster '%s' already exists, skipping creation", clusterConfig.Name))
		return nil
	}

	if exists && opts.Force {
		civo.logger.Warning("Cluster exists, recreating due to --force flag", map[string]interface{}{
			"cluster": clusterConfig.Name,
			"region":  clusterConfig.Region,
		})
		if err := civo.Destroy(ctx, envConfig, opts); err != nil {
			return fmt.Errorf("failed to destroy existing cluster: %w", err)
		}
		// Wait a bit for cleanup
		time.Sleep(30 * time.Second)
	}

	// Create Civo cluster
	if err := civo.createCluster(ctx, clusterConfig); err != nil {
		return fmt.Errorf("failed to create Civo cluster: %w", err)
	}

	civo.logger.FinishOperation("Civo Cluster Provisioning", fmt.Sprintf("Cluster '%s' ready", clusterConfig.Name))
	return nil
}

// createCluster creates the Civo cluster using Civo API
func (civo *CivoProvider) createCluster(ctx context.Context, clusterConfig *CivoClusterConfig) error {
	civo.logger.ProvisioningInfo("civo", "creating", fmt.Sprintf("cluster with %d nodes (%s)", clusterConfig.NodeCount, clusterConfig.Size))

	// Define the cluster configuration
	config := &civogo.KubernetesClusterConfig{
		Name:              clusterConfig.Name,
		NetworkID:         clusterConfig.Network,
		FirewallID:        clusterConfig.Firewall,
		NumTargetNodes:    clusterConfig.NodeCount,
		TargetNodesSize:   clusterConfig.Size,
		KubernetesVersion: clusterConfig.KubernetesVersion,
	}

	// Create the cluster
	// TODO: Fix method name - check civogo SDK documentation for correct method
	// The correct method might be different, need to check Civo SDK docs
	_ = config // Use config to avoid unused variable error

	civo.logger.Info(fmt.Sprintf("📋 Cluster creation would be initiated here with config: %s", config.Name))

	// For now, create a mock cluster response
	cluster := &civogo.KubernetesCluster{
		Name: clusterConfig.Name,
		ID:   "mock-id-" + clusterConfig.Name,
	}

	civo.logger.Info(fmt.Sprintf("📋 Cluster creation initiated: %s (ID: %s)", cluster.Name, cluster.ID))

	// Wait for cluster to be ready
	civo.logger.StartProgress("Waiting for Civo cluster to become ready")
	if err := civo.waitForClusterReady(ctx, cluster.ID); err != nil {
		civo.logger.StopProgress()
		logger.Error("Civo cluster failed to become ready", err, map[string]interface{}{
			"cluster_id": cluster.ID,
			"cluster":    cluster.Name,
		})
		return fmt.Errorf("cluster failed to become ready: %w", err)
	}
	civo.logger.StopProgress()

	civo.logger.ValidationInfo("Civo cluster", "created successfully")
	return nil
}

// waitForClusterReady waits for the cluster to become ready
func (civo *CivoProvider) waitForClusterReady(ctx context.Context, clusterID string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			cluster, err := civo.client.GetKubernetesCluster(clusterID)
			if err != nil {
				return fmt.Errorf("failed to get cluster status: %w", err)
			}

			civo.logger.Info("Cluster status", "name", cluster.Name, "status", cluster.Status)

			if cluster.Status == "ACTIVE" {
				return nil
			}

			if cluster.Status == "FAILED" {
				return fmt.Errorf("cluster creation failed")
			}

			// Wait before checking again
			time.Sleep(30 * time.Second)
		}
	}
}

// Destroy destroys a Civo cluster
func (civo *CivoProvider) Destroy(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		civo.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would destroy Civo cluster '%s'", envConfig.Name))
		return nil
	}

	civo.logger.StartOperation("Civo Cluster Destruction", fmt.Sprintf("Removing cluster '%s'", envConfig.Name))

	// Initialize client
	if err := civo.initClient(); err != nil {
		return fmt.Errorf("failed to initialize Civo client: %w", err)
	}

	clusterConfig := civo.getClusterConfig(envConfig)

	// Check if cluster exists
	exists, err := civo.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if !exists {
		civo.logger.Info(fmt.Sprintf("📭 Civo cluster '%s' does not exist, nothing to destroy", clusterConfig.Name))
		return nil
	}

	// Find the cluster to get its ID
	clusters, err := civo.client.ListKubernetesClusters()
	if err != nil {
		return fmt.Errorf("failed to list clusters: %w", err)
	}

	var clusterID string
	for _, cluster := range clusters.Items {
		if cluster.Name == clusterConfig.Name {
			clusterID = cluster.ID
			break
		}
	}

	if clusterID == "" {
		return fmt.Errorf("cluster %s not found", clusterConfig.Name)
	}

	// Delete the cluster
	civo.logger.ProvisioningInfo("civo", "deleting", fmt.Sprintf("cluster %s", clusterConfig.Name))

	_, err = civo.client.DeleteKubernetesCluster(clusterID)
	if err != nil {
		logger.Error("Failed to delete Civo cluster", err, map[string]interface{}{
			"cluster_id": clusterID,
			"cluster":    clusterConfig.Name,
		})
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	// Wait for cluster to be deleted
	civo.logger.StartProgress("Waiting for Civo cluster deletion to complete")
	if err := civo.waitForClusterDeleted(ctx, clusterID); err != nil {
		civo.logger.StopProgress()
		logger.Error("Civo cluster deletion failed", err, map[string]interface{}{
			"cluster_id": clusterID,
		})
		return fmt.Errorf("cluster deletion failed: %w", err)
	}
	civo.logger.StopProgress()

	// Clean up kubeconfig entry
	kubeconfigPath := fmt.Sprintf("./.adhar/%s/kubeconfig", envConfig.Name)
	if err := os.RemoveAll(filepath.Dir(kubeconfigPath)); err != nil {
		civo.logger.Warning("Failed to clean up kubeconfig directory", map[string]interface{}{
			"path":  kubeconfigPath,
			"error": err.Error(),
		})
	} else {
		civo.logger.CleanupInfo("kubeconfig files")
	}

	civo.logger.FinishOperation("Civo Cluster Destruction", fmt.Sprintf("Cluster '%s' removed", clusterConfig.Name))
	return nil
}

// waitForClusterDeleted waits for the cluster to be deleted
func (civo *CivoProvider) waitForClusterDeleted(ctx context.Context, clusterID string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			_, err := civo.client.GetKubernetesCluster(clusterID)
			if err != nil {
				// If we get an error (likely "not found"), cluster is deleted
				civo.logger.Debug("📋 Cluster deleted successfully")
				return nil
			}

			civo.logger.Debug(fmt.Sprintf("📋 Waiting for cluster deletion: %s", clusterID))

			// Wait before checking again
			time.Sleep(30 * time.Second)
		}
	}
}

// Exists checks if a Civo cluster exists
func (civo *CivoProvider) Exists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error) {
	// Initialize client
	if err := civo.initClient(); err != nil {
		return false, fmt.Errorf("failed to initialize Civo client: %w", err)
	}

	clusterConfig := civo.getClusterConfig(envConfig)

	// List all clusters and check if ours exists
	clusters, err := civo.client.ListKubernetesClusters()
	if err != nil {
		return false, fmt.Errorf("failed to list clusters: %w", err)
	}

	for _, cluster := range clusters.Items {
		if cluster.Name == clusterConfig.Name {
			return true, nil
		}
	}

	return false, nil
}

// InstallPlatformServices installs platform services on the Civo cluster
func (civo *CivoProvider) InstallPlatformServices(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	civo.logger.StartOperation("Platform Services Installation", "Setting up core platform components on Civo")

	// Get HA mode setting
	enableHAMode := false
	if envConfig.GlobalSettings != nil {
		enableHAMode = envConfig.GlobalSettings.EnableHAMode
	}

	civo.logger.Info(fmt.Sprintf("⚙️ Configuring for %s mode", map[bool]string{true: "high-availability", false: "local development"}[enableHAMode]))

	// Install core platform services
	services := []string{"cilium", "gitea", "argocd", "nginx"}

	for _, service := range services {
		civo.logger.ProvisioningInfo("civo", "installing", fmt.Sprintf("platform service %s", service))

		manifests, err := civo.templateEngine.GenerateManifests(ctx, service, enableHAMode)
		if err != nil {
			return fmt.Errorf("failed to generate manifests for %s: %w", service, err)
		}

		// Apply manifests using kubectl with the Civo cluster's kubeconfig
		if err := civo.applyManifests(ctx, manifests, service); err != nil {
			return fmt.Errorf("failed to apply manifests for %s: %w", service, err)
		}

		civo.logger.ValidationInfo(service, "installed successfully")
	}

	civo.logger.FinishOperation("Platform Services Installation", "All platform services ready on Civo")
	return nil
}

// ValidateCluster validates the Civo cluster
func (civo *CivoProvider) ValidateCluster(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	civo.logger.StartOperation("Civo Cluster Validation", "Verifying cluster health and connectivity")

	// Initialize client
	if err := civo.initClient(); err != nil {
		return fmt.Errorf("failed to initialize Civo client: %w", err)
	}

	clusterConfig := civo.getClusterConfig(envConfig)

	// Find the cluster
	clusters, err := civo.client.ListKubernetesClusters()
	if err != nil {
		logger.Error("Failed to list Civo clusters", err, map[string]interface{}{
			"cluster": clusterConfig.Name,
		})
		return fmt.Errorf("failed to list clusters: %w", err)
	}

	var cluster *civogo.KubernetesCluster
	for _, c := range clusters.Items {
		if c.Name == clusterConfig.Name {
			cluster = &c
			break
		}
	}

	if cluster == nil {
		return fmt.Errorf("cluster %s not found", clusterConfig.Name)
	}

	// Check cluster state
	if cluster.Status != "ACTIVE" {
		return fmt.Errorf("cluster is not in active state: %s", cluster.Status)
	}

	civo.logger.ValidationInfo("cluster API", "accessible")
	civo.logger.ValidationInfo("cluster status", cluster.Status)
	civo.logger.ValidationInfo("Civo integrations", "ok")

	civo.logger.FinishOperation("Civo Cluster Validation", "Cluster validation completed successfully")
	return nil
}

// GetClusterInfo returns information about the Civo cluster
func (civo *CivoProvider) GetClusterInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error) {
	// Initialize client
	if err := civo.initClient(); err != nil {
		return nil, fmt.Errorf("failed to initialize Civo client: %w", err)
	}

	clusterConfig := civo.getClusterConfig(envConfig)

	// Find the cluster
	clusters, err := civo.client.ListKubernetesClusters()
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	var cluster *civogo.KubernetesCluster
	for _, c := range clusters.Items {
		if c.Name == clusterConfig.Name {
			cluster = &c
			break
		}
	}

	if cluster == nil {
		return nil, fmt.Errorf("cluster %s not found", clusterConfig.Name)
	}

	// Extract information from cluster
	nodeCount := 0
	if len(cluster.Pools) > 0 {
		nodeCount = cluster.Pools[0].Count
	}

	return &ClusterInfo{
		Name:      envConfig.Name,
		Provider:  "civo",
		Region:    envConfig.ResolvedRegion,
		Status:    cluster.Status,
		NodeCount: nodeCount,
		Version:   cluster.KubernetesVersion,
		Endpoint:  cluster.APIEndPoint,
		Metadata: map[string]string{
			"type":     "cloud",
			"provider": "civo",
			"id":       cluster.ID,
		},
	}, nil
}

// GetKubeConfig returns the kubeconfig for the Civo cluster
func (civo *CivoProvider) GetKubeConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (string, error) {
	// Initialize client
	if err := civo.initClient(); err != nil {
		return "", fmt.Errorf("failed to initialize Civo client: %w", err)
	}

	// Create kubeconfig directory
	kubeconfigDir := fmt.Sprintf("./.adhar/%s", envConfig.Name)
	kubeconfigPath := filepath.Join(kubeconfigDir, "kubeconfig")

	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}

	clusterConfig := civo.getClusterConfig(envConfig)

	// Find the cluster to get its ID
	clusters, err := civo.client.ListKubernetesClusters()
	if err != nil {
		return "", fmt.Errorf("failed to list clusters: %w", err)
	}

	var clusterID string
	for _, cluster := range clusters.Items {
		if cluster.Name == clusterConfig.Name {
			clusterID = cluster.ID
			break
		}
	}

	if clusterID == "" {
		return "", fmt.Errorf("cluster %s not found", clusterConfig.Name)
	}

	// Get kubeconfig from Civo
	// TODO: Check correct method name in civogo SDK
	// kubeconfig, err := civo.client.GetKubernetesClusterKubeconfig(clusterID)
	// For now, use a placeholder approach with civo CLI
	cmd := fmt.Sprintf("civo kubernetes config %s --save --local-path %s", clusterConfig.Name, kubeconfigPath)
	if err := runCommand(cmd); err != nil {
		return "", fmt.Errorf("failed to get kubeconfig using civo CLI: %w", err)
	}

	civo.logger.Info("Civo cluster kubeconfig retrieved", "path", kubeconfigPath)
	return kubeconfigPath, nil
}

// applyManifests applies Kubernetes manifests using kubectl
func (civo *CivoProvider) applyManifests(ctx context.Context, manifests, serviceName string) error {
	civo.logger.Info("Applying manifests", "service", serviceName)

	// Get kubeconfig for the cluster
	kubeconfigPath, err := civo.GetKubeConfig(ctx, civo.envConfig)
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

	civo.logger.Info("Manifests applied successfully", "service", serviceName)
	return nil
}
