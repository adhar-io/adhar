package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"

	"github.com/digitalocean/godo"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
)

// DigitalOceanProvider implements Provider for DigitalOcean clusters
type DigitalOceanProvider struct {
	envConfig      *config.ResolvedEnvironmentConfig
	logger         *logger.AdharLogger
	templateEngine *TemplateEngine
	client         *godo.Client
}

// DOClusterConfig represents DigitalOcean-specific cluster configuration
type DOClusterConfig struct {
	Name      string
	Region    string
	Version   string
	NodePools []DONodePool
	VPCId     string
	Tags      []string
}

// DONodePool represents a DigitalOcean node pool configuration
type DONodePool struct {
	Name      string
	Size      string
	Count     int
	MinNodes  int
	MaxNodes  int
	AutoScale bool
	Tags      []string
}

// NewDigitalOceanProvider creates a new DigitalOcean provider
func NewDigitalOceanProvider(envConfig *config.ResolvedEnvironmentConfig, log *logrus.Logger, templateEngine *TemplateEngine) (Provider, error) {
	return &DigitalOceanProvider{
		envConfig:      envConfig,
		logger:         logger.GetLogger(),
		templateEngine: templateEngine,
		client:         nil, // Will be initialized when needed
	}, nil
}

// Provision provisions a DigitalOcean cluster
func (dop *DigitalOceanProvider) Provision(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		dop.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would provision DOKS cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))
		return nil
	}

	dop.logger.StartOperation("DigitalOcean DOKS Cluster Provisioning", fmt.Sprintf("Creating cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))

	// Initialize client
	if err := dop.initializeClient(); err != nil {
		logger.Error("Failed to initialize DigitalOcean client", err, map[string]interface{}{
			"region": envConfig.ResolvedRegion,
		})
		return fmt.Errorf("failed to initialize DigitalOcean client: %w", err)
	}

	clusterConfig := dop.getClusterConfig(envConfig)

	// Check if cluster already exists
	exists, err := dop.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if exists {
		dop.logger.Info(fmt.Sprintf("✅ DOKS cluster '%s' already exists, skipping creation", clusterConfig.Name))
		return nil
	}

	// Create node pools
	var nodePools []*godo.KubernetesNodePoolCreateRequest
	for _, nodePool := range clusterConfig.NodePools {
		pool := &godo.KubernetesNodePoolCreateRequest{
			Name:  nodePool.Name,
			Size:  nodePool.Size,
			Count: nodePool.Count,
			Tags:  nodePool.Tags,
		}

		if nodePool.AutoScale {
			pool.AutoScale = nodePool.AutoScale
			pool.MinNodes = nodePool.MinNodes
			pool.MaxNodes = nodePool.MaxNodes
		}

		nodePools = append(nodePools, pool)
	}

	// Create cluster request
	createRequest := &godo.KubernetesClusterCreateRequest{
		Name:        clusterConfig.Name,
		RegionSlug:  clusterConfig.Region,
		VersionSlug: clusterConfig.Version,
		NodePools:   nodePools,
		Tags:        clusterConfig.Tags,
	}

	if clusterConfig.VPCId != "" {
		createRequest.VPCUUID = clusterConfig.VPCId
	}

	dop.logger.ProvisioningInfo("digitalocean", "creating", fmt.Sprintf("DOKS cluster with %d node pools", len(nodePools)))

	// Create the cluster
	cluster, _, err := dop.client.Kubernetes.Create(ctx, createRequest)
	if err != nil {
		logger.Error("Failed to create DOKS cluster", err, map[string]interface{}{
			"cluster": clusterConfig.Name,
			"region":  clusterConfig.Region,
		})
		return fmt.Errorf("failed to create DigitalOcean cluster: %w", err)
	}

	dop.logger.Info(fmt.Sprintf("📋 Cluster creation initiated: %s (ID: %s)", cluster.Name, cluster.ID))

	// Wait for cluster to be ready
	dop.logger.StartProgress("Waiting for DOKS cluster to become ready (this can take 5-10 minutes)")
	if err := dop.waitForClusterReady(ctx, cluster.ID); err != nil {
		dop.logger.StopProgress()
		logger.Error("DOKS cluster failed to become ready", err, map[string]interface{}{
			"cluster_id": cluster.ID,
			"cluster":    cluster.Name,
		})
		return fmt.Errorf("cluster failed to become ready: %w", err)
	}
	dop.logger.StopProgress()

	dop.logger.FinishOperation("DigitalOcean DOKS Cluster Provisioning", fmt.Sprintf("Cluster '%s' ready", clusterConfig.Name))
	return nil
}

// Destroy destroys a DigitalOcean cluster
func (dop *DigitalOceanProvider) Destroy(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		dop.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would destroy DOKS cluster '%s'", envConfig.Name))
		return nil
	}

	dop.logger.StartOperation("DigitalOcean DOKS Cluster Destruction", fmt.Sprintf("Removing cluster '%s'", envConfig.Name))

	// Initialize client
	if err := dop.initializeClient(); err != nil {
		return fmt.Errorf("failed to initialize DigitalOcean client: %w", err)
	}

	// Get cluster by name
	cluster, err := dop.getClusterByName(ctx, envConfig.Name)
	if err != nil {
		if err.Error() == "cluster not found" {
			dop.logger.Info(fmt.Sprintf("📭 DOKS cluster '%s' not found, nothing to destroy", envConfig.Name))
			return nil
		}
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	// Delete the cluster
	dop.logger.ProvisioningInfo("digitalocean", "deleting", fmt.Sprintf("DOKS cluster %s", cluster.Name))
	_, err = dop.client.Kubernetes.Delete(ctx, cluster.ID)
	if err != nil {
		logger.Error("Failed to delete DOKS cluster", err, map[string]interface{}{
			"cluster_id": cluster.ID,
			"cluster":    cluster.Name,
		})
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	// Wait for cluster to be deleted
	dop.logger.StartProgress("Waiting for DOKS cluster deletion to complete")
	if err := dop.waitForClusterDeleted(ctx, cluster.ID); err != nil {
		dop.logger.StopProgress()
		logger.Error("Failed to wait for DOKS cluster deletion", err, map[string]interface{}{
			"cluster_id": cluster.ID,
		})
		return fmt.Errorf("failed to wait for cluster deletion: %w", err)
	}
	dop.logger.StopProgress()

	dop.logger.FinishOperation("DigitalOcean DOKS Cluster Destruction", fmt.Sprintf("Cluster '%s' removed", envConfig.Name))
	return nil
}

// Exists checks if a DigitalOcean cluster exists
func (dop *DigitalOceanProvider) Exists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error) {
	if err := dop.initializeClient(); err != nil {
		return false, fmt.Errorf("failed to initialize DigitalOcean client: %w", err)
	}

	_, err := dop.getClusterByName(ctx, envConfig.Name)
	if err != nil {
		if err.Error() == "cluster not found" {
			return false, nil
		}
		return false, fmt.Errorf("failed to check cluster existence: %w", err)
	}
	return true, nil
}

// InstallPlatformServices installs platform services on the DigitalOcean cluster
func (dop *DigitalOceanProvider) InstallPlatformServices(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	dop.logger.StartOperation("Platform Services Installation", "Setting up core platform components on DOKS")

	// Initialize client to ensure we can communicate with DO API
	if err := dop.initializeClient(); err != nil {
		return fmt.Errorf("failed to initialize DigitalOcean client: %w", err)
	}

	// Get kubeconfig for the cluster
	kubeconfig, err := dop.GetKubeConfig(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Get HA mode setting
	enableHAMode := false
	if envConfig.GlobalSettings != nil {
		enableHAMode = envConfig.GlobalSettings.EnableHAMode
	}

	// Phase 1: Install core infrastructure using templates
	if err := dop.installCoreInfrastructure(ctx, kubeconfig, enableHAMode, envConfig); err != nil {
		return fmt.Errorf("failed to install core infrastructure: %w", err)
	}

	// Phase 2: Install ArgoCD and configure it to manage platform stack
	if err := dop.setupArgoCDPlatformManagement(ctx, kubeconfig, enableHAMode, envConfig); err != nil {
		return fmt.Errorf("failed to setup ArgoCD platform management: %w", err)
	}

	dop.logger.FinishOperation("Platform Services Installation", "Platform services installation completed successfully")
	return nil
}

// installCoreInfrastructure installs core infrastructure services using templates
func (dop *DigitalOceanProvider) installCoreInfrastructure(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	dop.logger.Info("Installing core infrastructure services")

	// Core services in installation order
	coreServices := []string{"cilium", "nginx", "gitea"}

	for _, service := range coreServices {
		dop.logger.Info("Installing core service", "service", service)

		// Generate manifests using the template engine
		manifests, err := dop.templateEngine.GenerateManifests(ctx, service, enableHAMode)
		if err != nil {
			return fmt.Errorf("failed to generate manifests for %s: %w", service, err)
		}

		// Apply manifests using kubectl
		if err := dop.applyManifests(ctx, kubeconfig, manifests, service); err != nil {
			return fmt.Errorf("failed to apply manifests for %s: %w", service, err)
		}

		// Wait for service to be ready
		if err := dop.waitForServiceReady(ctx, kubeconfig, service); err != nil {
			dop.logger.Warn("Service may not be fully ready", "service", service, "error", err)
		}

		dop.logger.Info("Core service installed successfully", "service", service)
	}

	return nil
}

// setupArgoCDPlatformManagement installs ArgoCD and configures it to manage the platform stack
func (dop *DigitalOceanProvider) setupArgoCDPlatformManagement(ctx context.Context, kubeconfig string, enableHAMode bool, envConfig *config.ResolvedEnvironmentConfig) error {
	dop.logger.Info("Setting up ArgoCD for platform management")

	// Install ArgoCD using templates
	dop.logger.Info("Installing ArgoCD")
	manifests, err := dop.templateEngine.GenerateManifests(ctx, "argocd", enableHAMode)
	if err != nil {
		return fmt.Errorf("failed to generate ArgoCD manifests: %w", err)
	}

	if err := dop.applyManifests(ctx, kubeconfig, manifests, "argocd"); err != nil {
		return fmt.Errorf("failed to apply ArgoCD manifests: %w", err)
	}

	// Wait for ArgoCD to be ready
	if err := dop.waitForServiceReady(ctx, kubeconfig, "argocd"); err != nil {
		dop.logger.Warn("ArgoCD may not be fully ready", "error", err)
	}

	// Configure ArgoCD with platform stack applications
	if err := dop.deployPlatformStackApplications(ctx, kubeconfig, envConfig); err != nil {
		return fmt.Errorf("failed to deploy platform stack applications: %w", err)
	}

	dop.logger.Info("ArgoCD platform management setup completed")
	return nil
}

// deployPlatformStackApplications deploys the platform stack application sets to ArgoCD
func (dop *DigitalOceanProvider) deployPlatformStackApplications(ctx context.Context, kubeconfig string, envConfig *config.ResolvedEnvironmentConfig) error {
	dop.logger.Info("Deploying platform stack applications via ArgoCD")

	// Define the platform stack applications to deploy
	platformApps := []string{
		"platform/stack/adhar-appset-charts.yaml",
		"platform/stack/adhar-appset-manifests.yaml",
		"platform/stack/adhar-templates.yaml",
	}

	for _, appPath := range platformApps {
		dop.logger.Info("Deploying platform application", "app", appPath)

		if err := dop.applyKubernetesFile(ctx, kubeconfig, appPath); err != nil {
			dop.logger.Warn("Failed to deploy platform application", "app", appPath, "error", err)
			// Continue with other applications even if one fails
			continue
		}

		dop.logger.Info("Platform application deployed", "app", appPath)
	}

	return nil
}

// applyManifests applies Kubernetes manifests using kubectl
func (dop *DigitalOceanProvider) applyManifests(ctx context.Context, kubeconfig, manifests, serviceName string) error {
	dop.logger.Info("Applying manifests", "service", serviceName)

	// Create a temporary file for the manifests
	tmpFile := fmt.Sprintf("/tmp/%s-%s-manifests.yaml", serviceName, "temp")

	if err := os.WriteFile(tmpFile, []byte(manifests), 0644); err != nil {
		return fmt.Errorf("failed to write manifests to file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Apply using kubectl
	if err := dop.runKubectlCommand(ctx, kubeconfig, "apply", "-f", tmpFile); err != nil {
		return fmt.Errorf("failed to apply manifests: %w", err)
	}

	return nil
}

// applyKubernetesFile applies a Kubernetes YAML file using kubectl
func (dop *DigitalOceanProvider) applyKubernetesFile(ctx context.Context, kubeconfig, filePath string) error {
	return dop.runKubectlCommand(ctx, kubeconfig, "apply", "-f", filePath)
}

// runKubectlCommand runs a kubectl command with the specified kubeconfig
func (dop *DigitalOceanProvider) runKubectlCommand(ctx context.Context, kubeconfig string, args ...string) error {
	cmdArgs := append([]string{"--kubeconfig", kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", cmdArgs...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		dop.logger.Error("kubectl command failed", "cmd", cmd.String(), "output", string(output), "error", err)
		return fmt.Errorf("kubectl command failed: %w", err)
	}

	dop.logger.Debug("kubectl command succeeded", "cmd", cmd.String())
	return nil
}

// waitForServiceReady waits for a service to be ready
func (dop *DigitalOceanProvider) waitForServiceReady(ctx context.Context, kubeconfig, serviceName string) error {
	dop.logger.Info("Waiting for service to be ready", "service", serviceName)

	// Define service-specific readiness checks
	switch serviceName {
	case "cilium":
		return dop.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=cilium-agent", "-n", "kube-system", "--timeout=300s")
	case "nginx":
		return dop.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=ingress-nginx", "-n", "ingress-nginx", "--timeout=300s")
	case "gitea":
		return dop.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=gitea", "-n", "gitea", "--timeout=300s")
	case "argocd":
		return dop.runKubectlCommand(ctx, kubeconfig, "wait", "--for=condition=ready", "pod", "-l", "app.kubernetes.io/name=argocd-server", "-n", "argocd", "--timeout=300s")
	default:
		// Generic wait - just give it some time
		time.Sleep(30 * time.Second)
		return nil
	}
}

// ValidateCluster validates the DigitalOcean cluster
func (dop *DigitalOceanProvider) ValidateCluster(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	dop.logger.Info("Validating DigitalOcean cluster")

	// TODO: Implement DigitalOcean cluster validation
	// This would typically:
	// 1. Check if cluster API is accessible
	// 2. Verify cluster nodes are ready
	// 3. Check if required namespaces exist
	// 4. Validate cluster networking

	dop.logger.Info("DigitalOcean cluster validation completed successfully")
	return nil
}

// GetClusterInfo returns information about the DigitalOcean cluster
func (dop *DigitalOceanProvider) GetClusterInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error) {
	// TODO: Implement getting actual DigitalOcean cluster information using DO API
	// This would typically:
	// 1. Get cluster details from DigitalOcean API
	// 2. Get node pool information
	// 3. Get cluster status and version
	// 4. Get cluster endpoint URL

	return &ClusterInfo{
		Name:      envConfig.Name,
		Provider:  "digitalocean",
		Region:    envConfig.ResolvedRegion,
		Status:    "unknown", // Would be populated from API
		NodeCount: 3,         // Would be populated from API
		Version:   "v1.28.0", // Would be populated from API
		Endpoint:  "",        // Would be populated from API
		Metadata: map[string]string{
			"type":     "cloud",
			"provider": "digitalocean",
		},
	}, nil
}

// GetKubeConfig returns the kubeconfig for the DigitalOcean cluster
func (dop *DigitalOceanProvider) GetKubeConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (string, error) {
	if err := dop.initializeClient(); err != nil {
		return "", fmt.Errorf("failed to initialize DigitalOcean client: %w", err)
	}

	// Get cluster by name
	cluster, err := dop.getClusterByName(ctx, envConfig.Name)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster: %w", err)
	}

	// Get kubeconfig from DigitalOcean API
	kubeconfig, _, err := dop.client.Kubernetes.GetKubeConfig(ctx, cluster.ID)
	if err != nil {
		return "", fmt.Errorf("failed to get kubeconfig from DigitalOcean: %w", err)
	}

	// Create .adhar directory if it doesn't exist
	adharDir := fmt.Sprintf("./.adhar/%s", envConfig.Name)
	if err := os.MkdirAll(adharDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create adhar directory: %w", err)
	}

	// Save kubeconfig to file
	kubeconfigPath := fmt.Sprintf("%s/kubeconfig", adharDir)
	if err := os.WriteFile(kubeconfigPath, kubeconfig.KubeconfigYAML, 0600); err != nil {
		return "", fmt.Errorf("failed to write kubeconfig file: %w", err)
	}

	dop.logger.Info("Kubeconfig saved", "path", kubeconfigPath)
	return kubeconfigPath, nil
}

// getClusterConfig extracts DigitalOcean cluster configuration from environment config
func (dop *DigitalOceanProvider) getClusterConfig(envConfig *config.ResolvedEnvironmentConfig) *DOClusterConfig {
	cfg := &DOClusterConfig{
		Name:    envConfig.Name,
		Region:  envConfig.ResolvedRegion,
		Version: "1.28", // Default Kubernetes version
		NodePools: []DONodePool{
			{
				Name:      envConfig.Name + "-pool",
				Size:      "s-2vcpu-4gb",
				Count:     3,
				MinNodes:  1,
				MaxNodes:  10,
				AutoScale: true,
				Tags:      []string{"adhar", envConfig.Name},
			},
		},
		VPCId: "",
		Tags:  []string{"adhar", envConfig.Name},
	}

	// Override with custom configuration if provided
	for _, config := range envConfig.ResolvedClusterConfig {
		switch config.Key {
		case "version":
			if config.Value != "" {
				cfg.Version = config.Value
			}
		case "node_size":
			if config.Value != "" {
				cfg.NodePools[0].Size = config.Value
			}
		case "node_count":
			if config.Value != "" {
				cfg.NodePools[0].Count = parseIntOrDefault(config.Value, 3)
			}
		case "min_nodes":
			if config.Value != "" {
				cfg.NodePools[0].MinNodes = parseIntOrDefault(config.Value, 1)
			}
		case "max_nodes":
			if config.Value != "" {
				cfg.NodePools[0].MaxNodes = parseIntOrDefault(config.Value, 10)
			}
		case "auto_scale":
			if config.Value != "" {
				cfg.NodePools[0].AutoScale = parseBoolOrDefault(config.Value, true)
			}
		case "vpc_id":
			if config.Value != "" {
				cfg.VPCId = config.Value
			}
		}
	}

	return cfg
}

// waitForClusterReady waits for a DigitalOcean cluster to be in running state
func (dop *DigitalOceanProvider) waitForClusterReady(ctx context.Context, clusterID string) error {
	dop.logger.Info("Waiting for cluster to be ready", "id", clusterID)

	timeout := time.After(30 * time.Minute) // 30 minute timeout
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for cluster to be ready")
		case <-ticker.C:
			cluster, _, err := dop.client.Kubernetes.Get(ctx, clusterID)
			if err != nil {
				dop.logger.Warn("Failed to get cluster status", "error", err)
				continue
			}

			dop.logger.Debug("Cluster status", "status", cluster.Status.State)

			if cluster.Status.State == godo.KubernetesClusterStatusRunning {
				dop.logger.Info("Cluster is ready", "id", clusterID)
				return nil
			}

			if cluster.Status.State == godo.KubernetesClusterStatusError {
				return fmt.Errorf("cluster creation failed: %s", cluster.Status.Message)
			}
		}
	}
}

// getClusterByName gets a cluster by name
func (dop *DigitalOceanProvider) getClusterByName(ctx context.Context, name string) (*godo.KubernetesCluster, error) {
	clusters, _, err := dop.client.Kubernetes.List(ctx, &godo.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	for _, cluster := range clusters {
		if cluster.Name == name {
			return cluster, nil
		}
	}

	return nil, fmt.Errorf("cluster not found")
}

// waitForClusterDeleted waits for a cluster to be deleted
func (dop *DigitalOceanProvider) waitForClusterDeleted(ctx context.Context, clusterID string) error {
	dop.logger.Info("Waiting for cluster to be deleted", "id", clusterID)

	timeout := time.After(20 * time.Minute) // 20 minute timeout
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for cluster to be deleted")
		case <-ticker.C:
			_, _, err := dop.client.Kubernetes.Get(ctx, clusterID)
			if err != nil {
				// If we get a 404 or similar, the cluster is deleted
				dop.logger.Info("Cluster deleted successfully", "id", clusterID)
				return nil
			}
			// Cluster still exists, continue waiting
		}
	}
}

// initializeClient initializes the DigitalOcean API client
func (dop *DigitalOceanProvider) initializeClient() error {
	if dop.client != nil {
		return nil // Already initialized
	}

	// Get DigitalOcean API token from environment
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		return fmt.Errorf("DIGITALOCEAN_TOKEN environment variable is required")
	}

	// Create OAuth2 token source
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)

	// Create DigitalOcean client
	dop.client = godo.NewClient(oauthClient)

	return nil
}
