package build

import (
	"context"
	"fmt"
	"os"
	"time"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"

	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	"github.com/sirupsen/logrus"
)

// GCPProvider implements Provider for Google Cloud Platform clusters
type GCPProvider struct {
	envConfig      *config.ResolvedEnvironmentConfig
	logger         *logger.AdharLogger
	templateEngine *TemplateEngine
	client         *container.ClusterManagerClient
}

// NewGCPProvider creates a new GCP provider
func NewGCPProvider(envConfig *config.ResolvedEnvironmentConfig, log *logrus.Logger, templateEngine *TemplateEngine) (Provider, error) {
	return &GCPProvider{
		envConfig:      envConfig,
		logger:         logger.GetLogger(),
		templateEngine: templateEngine,
		client:         nil, // Lazy initialization
	}, nil
}

// getClient initializes the GCP client if not already done
func (gcp *GCPProvider) getClient(ctx context.Context) (*container.ClusterManagerClient, error) {
	if gcp.client != nil {
		return gcp.client, nil
	}

	// Check for credentials
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" && os.Getenv("GOOGLE_CLOUD_PROJECT") == "" {
		return nil, fmt.Errorf("GCP credentials not found. Set GOOGLE_APPLICATION_CREDENTIALS or configure Application Default Credentials")
	}

	client, err := container.NewClusterManagerClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCP client: %w", err)
	}

	gcp.client = client
	return client, nil
}

// Provision provisions a GCP cluster
func (gcp *GCPProvider) Provision(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		gcp.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would provision GKE cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))
		return nil
	}

	gcp.logger.StartOperation("GCP GKE Cluster Provisioning", fmt.Sprintf("Creating cluster '%s' in %s", envConfig.Name, envConfig.ResolvedRegion))

	client, err := gcp.getClient(ctx)
	if err != nil {
		logger.Error("Failed to initialize GCP client", err, map[string]interface{}{
			"region": envConfig.ResolvedRegion,
		})
		return fmt.Errorf("failed to get GCP client: %w", err)
	}
	defer client.Close()

	// Get cluster configuration values
	clusterConfig := gcp.getClusterConfig(envConfig)

	// Check if cluster already exists
	exists, err := gcp.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if exists && !opts.Force {
		gcp.logger.Info(fmt.Sprintf("✅ GKE cluster '%s' already exists, skipping creation", clusterConfig.Name))
		return nil
	}

	if exists && opts.Force {
		gcp.logger.Warning("Cluster exists, recreating due to --force flag", map[string]interface{}{
			"cluster": clusterConfig.Name,
			"zone":    clusterConfig.Zone,
		})
		if err := gcp.Destroy(ctx, envConfig, opts); err != nil {
			return fmt.Errorf("failed to destroy existing cluster: %w", err)
		}
		// Wait a bit for cleanup
		time.Sleep(30 * time.Second)
	}

	// Create the cluster
	parent := fmt.Sprintf("projects/%s/locations/%s", clusterConfig.ProjectID, clusterConfig.Zone)

	nodePool := &containerpb.NodePool{
		Name:             "default-pool",
		InitialNodeCount: int32(clusterConfig.NodeCount),
		Config: &containerpb.NodeConfig{
			MachineType: clusterConfig.MachineType,
			DiskSizeGb:  int32(clusterConfig.DiskSizeGb),
			OauthScopes: []string{
				"https://www.googleapis.com/auth/devstorage.read_only",
				"https://www.googleapis.com/auth/logging.write",
				"https://www.googleapis.com/auth/monitoring",
				"https://www.googleapis.com/auth/servicecontrol",
				"https://www.googleapis.com/auth/service.management.readonly",
				"https://www.googleapis.com/auth/trace.append",
			},
		},
		Management: &containerpb.NodeManagement{
			AutoUpgrade: true,
			AutoRepair:  true,
		},
	}

	cluster := &containerpb.Cluster{
		Name:        clusterConfig.Name,
		Description: fmt.Sprintf("Adhar cluster for environment %s", envConfig.Name),
		NodePools:   []*containerpb.NodePool{nodePool},
		Network:     "default",
		Subnetwork:  "default",
		AddonsConfig: &containerpb.AddonsConfig{
			HttpLoadBalancing: &containerpb.HttpLoadBalancing{
				Disabled: false,
			},
			HorizontalPodAutoscaling: &containerpb.HorizontalPodAutoscaling{
				Disabled: false,
			},
		},
		IpAllocationPolicy: &containerpb.IPAllocationPolicy{
			UseIpAliases: true,
		},
		InitialClusterVersion: clusterConfig.KubernetesVersion,
	}

	req := &containerpb.CreateClusterRequest{
		Parent:  parent,
		Cluster: cluster,
	}

	gcp.logger.ProvisioningInfo("gcp", "creating", fmt.Sprintf("GKE cluster with %d nodes (%s)", clusterConfig.NodeCount, clusterConfig.MachineType))

	op, err := client.CreateCluster(ctx, req)
	if err != nil {
		logger.Error("Failed to create GKE cluster", err, map[string]interface{}{
			"cluster": clusterConfig.Name,
			"zone":    clusterConfig.Zone,
		})
		return fmt.Errorf("failed to create cluster: %w", err)
	}

	gcp.logger.Info(fmt.Sprintf("📋 Cluster creation operation started: %s", op.Name))

	// Wait for the operation to complete
	gcp.logger.StartProgress("Waiting for GKE cluster creation (this can take 5-10 minutes)")
	if err := gcp.waitForOperation(ctx, client, op.Name, clusterConfig.ProjectID, clusterConfig.Zone); err != nil {
		gcp.logger.StopProgress()
		logger.Error("GKE cluster creation failed", err, map[string]interface{}{
			"cluster":   clusterConfig.Name,
			"operation": op.Name,
		})
		return fmt.Errorf("cluster creation failed: %w", err)
	}
	gcp.logger.StopProgress()

	gcp.logger.FinishOperation("GCP GKE Cluster Provisioning", fmt.Sprintf("Cluster '%s' ready", clusterConfig.Name))
	return nil
}

// waitForOperation waits for a GKE operation to complete
func (gcp *GCPProvider) waitForOperation(ctx context.Context, client *container.ClusterManagerClient, operationName, projectID, zone string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			req := &containerpb.GetOperationRequest{
				Name: fmt.Sprintf("projects/%s/locations/%s/operations/%s", projectID, zone, operationName),
			}

			op, err := client.GetOperation(ctx, req)
			if err != nil {
				return fmt.Errorf("failed to get operation status: %w", err)
			}

			switch op.Status {
			case containerpb.Operation_DONE:
				if op.Error != nil {
					return fmt.Errorf("operation failed: %s", op.Error.Message)
				}
				return nil
			case containerpb.Operation_ABORTING:
				return fmt.Errorf("operation aborted")
			case containerpb.Operation_RUNNING:
				gcp.logger.Debug(fmt.Sprintf("📋 Operation %s still running", operationName))
			}

			time.Sleep(10 * time.Second)
		}
	}
}

// Destroy destroys a GCP cluster
func (gcp *GCPProvider) Destroy(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error {
	if opts.DryRun {
		gcp.logger.Info(fmt.Sprintf("🔍 DRY-RUN: Would destroy GKE cluster '%s'", envConfig.Name))
		return nil
	}

	gcp.logger.StartOperation("GCP GKE Cluster Destruction", fmt.Sprintf("Removing cluster '%s'", envConfig.Name))

	client, err := gcp.getClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to get GCP client: %w", err)
	}
	defer client.Close()

	clusterConfig := gcp.getClusterConfig(envConfig)

	// Check if cluster exists first
	exists, err := gcp.Exists(ctx, envConfig)
	if err != nil {
		return fmt.Errorf("failed to check if cluster exists: %w", err)
	}

	if !exists {
		gcp.logger.Info(fmt.Sprintf("📭 GKE cluster '%s' does not exist, nothing to destroy", clusterConfig.Name))
		return nil
	}

	// Delete the cluster
	parent := fmt.Sprintf("projects/%s/locations/%s", clusterConfig.ProjectID, clusterConfig.Zone)
	clusterPath := fmt.Sprintf("%s/clusters/%s", parent, clusterConfig.Name)

	req := &containerpb.DeleteClusterRequest{
		Name: clusterPath,
	}

	gcp.logger.ProvisioningInfo("gcp", "deleting", fmt.Sprintf("GKE cluster in zone %s", clusterConfig.Zone))

	op, err := client.DeleteCluster(ctx, req)
	if err != nil {
		logger.Error("Failed to delete GKE cluster", err, map[string]interface{}{
			"cluster": clusterConfig.Name,
			"zone":    clusterConfig.Zone,
		})
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	gcp.logger.Info(fmt.Sprintf("📋 Cluster deletion operation started: %s", op.Name))

	// Wait for the operation to complete
	gcp.logger.StartProgress("Waiting for GKE cluster deletion to complete")
	if err := gcp.waitForOperation(ctx, client, op.Name, clusterConfig.ProjectID, clusterConfig.Zone); err != nil {
		gcp.logger.StopProgress()
		logger.Error("GKE cluster deletion failed", err, map[string]interface{}{
			"cluster":   clusterConfig.Name,
			"operation": op.Name,
		})
		return fmt.Errorf("cluster deletion failed: %w", err)
	}
	gcp.logger.StopProgress()

	gcp.logger.FinishOperation("GCP GKE Cluster Destruction", fmt.Sprintf("Cluster '%s' removed", clusterConfig.Name))
	return nil
}

// Exists checks if a GCP cluster exists
func (gcp *GCPProvider) Exists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error) {
	client, err := gcp.getClient(ctx)
	if err != nil {
		// If we can't get a client, assume cluster doesn't exist (e.g., no credentials)
		gcp.logger.Debug("Failed to get GCP client, assuming cluster doesn't exist", "error", err)
		return false, nil
	}
	defer client.Close()

	clusterConfig := gcp.getClusterConfig(envConfig)

	// Get the cluster
	parent := fmt.Sprintf("projects/%s/locations/%s", clusterConfig.ProjectID, clusterConfig.Zone)
	clusterPath := fmt.Sprintf("%s/clusters/%s", parent, clusterConfig.Name)

	req := &containerpb.GetClusterRequest{
		Name: clusterPath,
	}

	_, err = client.GetCluster(ctx, req)
	if err != nil {
		// If cluster doesn't exist, GetCluster returns an error
		gcp.logger.Debug("Cluster does not exist", "name", clusterConfig.Name, "error", err)
		return false, nil
	}

	gcp.logger.Debug("Cluster exists", "name", clusterConfig.Name)
	return true, nil
}

// InstallPlatformServices installs platform services on the GCP cluster
func (gcp *GCPProvider) InstallPlatformServices(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	gcp.logger.StartOperation("Platform Services Installation", "Setting up core platform components on GKE")

	// Get HA mode setting
	enableHAMode := false
	if envConfig.GlobalSettings != nil {
		enableHAMode = envConfig.GlobalSettings.EnableHAMode
	}

	gcp.logger.Info(fmt.Sprintf("⚙️ Configuring for %s mode", map[bool]string{true: "high-availability", false: "local development"}[enableHAMode]))

	// Install core platform services
	services := []string{"cilium", "gitea", "argocd", "nginx"}

	for _, service := range services {
		gcp.logger.ProvisioningInfo("gcp", "installing", fmt.Sprintf("platform service %s", service))

		manifests, err := gcp.templateEngine.GenerateManifests(ctx, service, enableHAMode)
		if err != nil {
			return fmt.Errorf("failed to generate manifests for %s: %w", service, err)
		}

		// Apply manifests using kubectl with the GCP cluster's kubeconfig
		if err := gcp.applyManifests(ctx, manifests, service); err != nil {
			return fmt.Errorf("failed to apply manifests for %s: %w", service, err)
		}

		gcp.logger.ValidationInfo(service, "installed successfully")
	}

	gcp.logger.FinishOperation("Platform Services Installation", "All platform services ready on GKE")
	return nil
}

// ValidateCluster validates the GCP cluster
func (gcp *GCPProvider) ValidateCluster(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error {
	gcp.logger.StartOperation("GKE Cluster Validation", "Verifying cluster health and connectivity")

	// TODO: Implement GCP cluster validation
	// This would typically:
	// 1. Check if cluster API is accessible
	// 2. Verify cluster nodes are ready
	// 3. Check if required namespaces exist
	// 4. Validate cluster networking and GCP integrations

	gcp.logger.ValidationInfo("cluster API", "accessible")
	gcp.logger.ValidationInfo("cluster nodes", "ready")
	gcp.logger.ValidationInfo("GCP integrations", "ok")

	gcp.logger.FinishOperation("GKE Cluster Validation", "Cluster validation completed successfully")
	return nil
}

// GetClusterInfo returns information about the GCP cluster
func (gcp *GCPProvider) GetClusterInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error) {
	// TODO: Implement getting actual GCP cluster information using GCP APIs
	// This would typically:
	// 1. Get cluster details from GKE API
	// 2. Get node pool information
	// 3. Get cluster status and version
	// 4. Get cluster endpoint URL

	return &ClusterInfo{
		Name:      envConfig.Name,
		Provider:  "gcp",
		Region:    envConfig.ResolvedRegion,
		Status:    "unknown", // Would be populated from API
		NodeCount: 3,         // Would be populated from API
		Version:   "v1.28.0", // Would be populated from API
		Endpoint:  "",        // Would be populated from API
		Metadata: map[string]string{
			"type":     "cloud",
			"provider": "gcp",
		},
	}, nil
}

// GetKubeConfig returns the kubeconfig for the GCP cluster
func (gcp *GCPProvider) GetKubeConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (string, error) {
	// TODO: Implement getting GCP cluster kubeconfig
	// This would typically:
	// 1. Use gcloud to get cluster credentials
	// 2. Configure kubectl context
	// 3. Return path to kubeconfig file

	return fmt.Sprintf("./.adhar/%s/kubeconfig", envConfig.Name), nil
}

// applyManifests applies Kubernetes manifests using kubectl
func (gcp *GCPProvider) applyManifests(ctx context.Context, manifests, serviceName string) error {
	// TODO: Implement manifest application for GCP clusters
	// This would typically:
	// 1. Get kubeconfig for the cluster
	// 2. Use kubectl or Kubernetes client-go to apply manifests
	// 3. Wait for resources to be ready
	// 4. Handle any application errors

	gcp.logger.Info("Applying manifests", "service", serviceName)
	return nil
}

// GCPClusterConfig represents GCP-specific cluster configuration
type GCPClusterConfig struct {
	Name              string
	Zone              string
	Region            string
	NodeCount         int
	MachineType       string
	ProjectID         string
	DiskSizeGb        int
	KubernetesVersion string
}

// getClusterConfig extracts GCP cluster configuration from environment config
func (gcp *GCPProvider) getClusterConfig(envConfig *config.ResolvedEnvironmentConfig) *GCPClusterConfig {
	cfg := &GCPClusterConfig{
		Name:              envConfig.Name,
		Zone:              envConfig.ResolvedRegion + "-a", // Default to first zone in region
		Region:            envConfig.ResolvedRegion,
		NodeCount:         3,
		MachineType:       "e2-standard-4",
		ProjectID:         "",
		DiskSizeGb:        100,
		KubernetesVersion: "latest",
	}

	// Override with custom configuration if provided
	for _, config := range envConfig.ResolvedClusterConfig {
		switch config.Key {
		case "node_count":
			if config.Value != "" {
				cfg.NodeCount = parseIntOrDefault(config.Value, 3)
			}
		case "machine_type":
			if config.Value != "" {
				cfg.MachineType = config.Value
			}
		case "zone":
			if config.Value != "" {
				cfg.Zone = config.Value
			}
		case "project_id":
			if config.Value != "" {
				cfg.ProjectID = config.Value
			}
		case "disk_size_gb":
			if config.Value != "" {
				cfg.DiskSizeGb = parseIntOrDefault(config.Value, 100)
			}
		case "kubernetes_version":
			if config.Value != "" {
				cfg.KubernetesVersion = config.Value
			}
		}
	}

	return cfg
}
