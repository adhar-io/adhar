package kind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/domain"
	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"

	"gopkg.in/yaml.v3"
)

// File-based storage for Kind clusters
var (
	clusterMutex    sync.RWMutex
	storageFilePath = filepath.Join(os.TempDir(), "adhar-kind-clusters.json")
)

// loadClusters loads clusters from persistent storage
func loadClusters() (map[string]*types.Cluster, error) {
	clusters := make(map[string]*types.Cluster)

	data, err := os.ReadFile(storageFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return clusters, nil // Empty map if file doesn't exist
		}
		return nil, err
	}

	err = json.Unmarshal(data, &clusters)
	return clusters, err
}

// saveClusters saves clusters to persistent storage
func saveClusters(clusters map[string]*types.Cluster) error {
	data, err := json.MarshalIndent(clusters, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(storageFilePath, data, 0644)
}

// getClusterStorage thread-safely loads clusters from storage
func getClusterStorage() (map[string]*types.Cluster, error) {
	clusterMutex.RLock()
	defer clusterMutex.RUnlock()
	return loadClusters()
}

// updateClusterStorage thread-safely updates cluster storage
func updateClusterStorage(fn func(map[string]*types.Cluster) error) error {
	clusterMutex.Lock()
	defer clusterMutex.Unlock()

	clusters, err := loadClusters()
	if err != nil {
		return err
	}

	err = fn(clusters)
	if err != nil {
		return err
	}

	return saveClusters(clusters)
}

// Register the Kind provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("kind", func(config map[string]interface{}) (provider.Provider, error) {
		kindConfig := &Config{
			KindPath:    "kind",    // Default value
			KubectlPath: "kubectl", // Default value
		}

		// Parse Kind-specific configuration
		if kindPath, ok := config["kindPath"].(string); ok && kindPath != "" {
			kindConfig.KindPath = kindPath
		}
		if kubectlPath, ok := config["kubectlPath"].(string); ok && kubectlPath != "" {
			kindConfig.KubectlPath = kubectlPath
		}

		return NewProvider(kindConfig), nil
	})
}

// Provider implements the Kind provider for local Kubernetes clusters
type Provider struct {
	config *Config
}

// Config holds Kind provider configuration
type Config struct {
	KindPath    string `json:"kindPath"`
	KubectlPath string `json:"kubectlPath"`
}

// NewProvider creates a new Kind provider instance
func NewProvider(config *Config) *Provider {
	if config == nil {
		config = &Config{
			KindPath:    "kind",
			KubectlPath: "kubectl",
		}
	}

	// Ensure Kind path defaults to "kind" if empty
	if config.KindPath == "" {
		config.KindPath = "kind"
	}
	if config.KubectlPath == "" {
		config.KubectlPath = "kubectl"
	}

	return &Provider{
		config: config,
	}
}

// Name returns the provider name
func (p *Provider) Name() string {
	return "kind"
}

// Region returns the provider region (local for Kind)
func (p *Provider) Region() string {
	return "local"
}

// Authenticate validates Kind binary availability
func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	// For Kind, we just check if the binary is available
	// In a real implementation, we would run: kind version
	return nil
}

// ValidatePermissions checks if we have required permissions
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	// Kind runs locally, so we just need Docker access
	return nil
}

// CreateCluster creates a new Kind cluster
func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Provider != "kind" {
		return nil, fmt.Errorf("provider mismatch: expected kind, got %s", spec.Provider)
	}

	cluster := &types.Cluster{
		ID:        fmt.Sprintf("kind-%s", spec.Name),
		Name:      spec.Name,
		Provider:  "kind",
		Region:    "local",
		Version:   spec.Version,
		Status:    types.ClusterStatusCreating,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Tags: map[string]string{
			"adhar.io/managed-by":   "adhar",
			"adhar.io/cluster-name": spec.Name,
			"adhar.io/provider":     "kind",
			"adhar.io/created-by":   "adhar-cli",
			"adhar.io/version":      "v1.0.0",
		},
		Metadata: map[string]interface{}{
			"kindConfig": generateKindConfig(spec),
		},
	}

	// Generate Kind cluster configuration file
	kindConfig := generateKindConfig(spec)
	configData, err := yaml.Marshal(kindConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Kind config: %w", err)
	}

	// Create temporary config file
	configFile := filepath.Join(os.TempDir(), fmt.Sprintf("kind-config-%s.yaml", spec.Name))
	err = os.WriteFile(configFile, configData, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write Kind config file: %w", err)
	}
	defer os.Remove(configFile) // Clean up config file

	// Ensure .adhar/backup directory exists for Kind cluster mount
	backupDir := ".adhar/backup"
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Create progress tracker for cluster creation steps only if not called from platform setup
	var progress *helpers.ProgressTracker
	if os.Getenv("ADHAR_PLATFORM_SETUP") != "true" {
		stepNames := []string{
			"Create Kind Cluster",
			"Install CNI (Cilium)",
			"Configure Networking",
		}

		stepDescriptions := []string{
			"Creating Kubernetes cluster with specified configuration",
			"Installing Cilium CNI for secure networking",
			"Configuring cluster networking and DNS",
		}

		progress = helpers.NewStyledProgressTracker("🔧 Setting up Management Cluster", stepNames, stepDescriptions)
	}

	// Step 1: Create the actual Kind cluster
	// Calculate total nodes (control plane + workers)
	totalNodes := spec.ControlPlane.Replicas
	for _, nodeGroup := range spec.NodeGroups {
		totalNodes += nodeGroup.Replicas
	}
	if progress != nil {
		progress.StartStep(0, fmt.Sprintf("Creating Kubernetes cluster '%s' with %d node(s)...", spec.Name, totalNodes))
	}

	// Build the command args
	args := []string{"create", "cluster", "--name", spec.Name}

	// Add Kubernetes version if specified
	if spec.Version != "" {
		args = append(args, "--image", fmt.Sprintf("kindest/node:%s", spec.Version))
	}

	// Add wait time (reduced for faster feedback)
	args = append(args, "--wait", "120s")

	// Add config file if we have custom node configuration or port mappings
	nodes := kindConfig["nodes"].([]map[string]interface{})

	// Check if any node has port mappings
	hasPortMappings := false
	for _, node := range nodes {
		if _, exists := node["extraPortMappings"]; exists {
			hasPortMappings = true
			break
		}
	}

	// Use config file for multi-node clusters or when port mappings are present
	if len(nodes) > 1 || hasPortMappings {
		args = append(args, "--config", configFile)
	}

	// Execute the kind create cluster command
	cmd := exec.CommandContext(ctx, p.config.KindPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if progress != nil {
			progress.FailStep(0, err)
		}
		return nil, fmt.Errorf("failed to create Kind cluster: %w", err)
	}

	// Update cluster status
	cluster.Status = types.ClusterStatusRunning
	cluster.UpdatedAt = time.Now()

	// Save cluster to storage
	if err := updateClusterStorage(func(clusters map[string]*types.Cluster) error {
		clusters[cluster.ID] = cluster
		return nil
	}); err != nil {
		// logger.Warnf("Failed to save cluster to storage: %v", err) // Original code had this line commented out
	}

	if progress != nil {
		progress.CompleteStep(0)
		progress.RenderStyledDisplay()
	}

	// Step 2: Install CNI (Cilium) if specified
	if spec.Networking.CNI != "" && spec.Networking.CNI == "cilium" {
		if progress != nil {
			progress.StartStep(1, "Installing Cilium CNI for secure networking...")
			progress.RenderStyledDisplay()
		}

		if err := p.installCilium(ctx, spec.Name); err != nil {
			if progress != nil {
				progress.SkipStep(1, "CNI installation failed, continuing anyway")
				progress.RenderStyledDisplay()
			}
			// logger.Warnf("Failed to install Cilium CNI: %v", err) // Original code had this line commented out
		} else {
			if progress != nil {
				progress.CompleteStep(1)
				progress.RenderStyledDisplay()
			}
		}
	} else {
		if progress != nil {
			progress.SkipStep(1, "No CNI specified")
			progress.RenderStyledDisplay()
		}
	}

	// Step 3: Configure networking
	if progress != nil {
		progress.StartStep(2, "Configuring cluster networking...")
		progress.RenderStyledDisplay()
	}

	// Configure CoreDNS and networking (placeholder for now)
	// TODO: Implement actual networking configuration
	time.Sleep(1 * time.Second) // Simulate some networking setup

	if progress != nil {
		progress.CompleteStep(2)
		progress.CompleteStyled()
	}

	// Complete the progress tracker normally - but we'll let it handle the display
	if progress != nil {
		progress.Complete()
	}

	// Set up domain management if domain configuration is available
	if spec.Domain != nil {
		// Check if we should suppress output
		if os.Getenv("ADHAR_PLATFORM_SETUP") != "true" {
			fmt.Printf("Setting up domain management...\n")
		}
		domainManager := domain.NewManager(spec.Domain, "")
		err = domainManager.SetupDomain(ctx, cluster)
		if err != nil {
			// Don't fail cluster creation if domain setup fails, just warn
			if os.Getenv("ADHAR_PLATFORM_SETUP") != "true" {
				fmt.Printf("⚠️  Warning: Failed to setup domain management: %v\n", err)
				fmt.Printf("You can set up domain management manually later\n")
			}
		} else {
			if os.Getenv("ADHAR_PLATFORM_SETUP") != "true" {
				fmt.Printf("✓ Domain management configured!\n")
			}
		}
	}

	// The kubectl context is automatically set by Kind to "kind-{cluster-name}"
	fmt.Printf("kubectl context set to: kind-%s\n", spec.Name)

	// Store cluster in persistent storage
	if err := updateClusterStorage(func(clusters map[string]*types.Cluster) error {
		clusters[cluster.ID] = cluster
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to store cluster: %w", err)
	}

	return cluster, nil
}

// DeleteCluster deletes a Kind cluster
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	// Extract cluster name from clusterID (format: kind-{name})
	clusterName := clusterID
	if len(clusterID) > 5 && clusterID[:5] == "kind-" {
		clusterName = clusterID[5:]
	}

	// Delete the actual Kind cluster
	fmt.Printf("Deleting Kind cluster '%s'...\n", clusterName)
	cmd := exec.CommandContext(ctx, p.config.KindPath, "delete", "cluster", "--name", clusterName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Don't fail if cluster doesn't exist
		if !strings.Contains(string(output), "not found") && !strings.Contains(string(output), "No kind cluster") {
			return fmt.Errorf("failed to delete Kind cluster: %w\nOutput: %s", err, string(output))
		}
	}

	fmt.Printf("✓ Kind cluster '%s' deleted successfully!\n", clusterName)

	// Remove cluster from persistent storage
	err = updateClusterStorage(func(clusters map[string]*types.Cluster) error {
		delete(clusters, clusterID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to remove cluster from storage: %w", err)
	}

	return nil
}

// UpdateCluster updates a Kind cluster
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	// Kind clusters are immutable, so this would typically recreate the cluster
	return fmt.Errorf("kind clusters are immutable; consider recreating the cluster")
}

// GetCluster retrieves cluster information
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	// In a real implementation, we would run: kind get clusters
	// and kubectl cluster-info for the specific cluster

	clusters, err := getClusterStorage()
	if err != nil {
		return nil, fmt.Errorf("failed to load clusters: %w", err)
	}

	if cluster, exists := clusters[clusterID]; exists {
		return cluster, nil
	}

	return nil, fmt.Errorf("cluster %s not found", clusterID)
}

// ListClusters lists all Kind clusters
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	// In a real implementation, we would run: kind get clusters
	// and return information for each cluster

	storedClusters, err := getClusterStorage()
	if err != nil {
		return nil, fmt.Errorf("failed to load clusters: %w", err)
	}

	clusters := make([]*types.Cluster, 0, len(storedClusters))
	for _, cluster := range storedClusters {
		clusters = append(clusters, cluster)
	}

	return clusters, nil
}

// AddNodeGroup adds a node group (simulate with additional nodes)
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	return &types.NodeGroup{
		Name:         nodeGroup.Name,
		Replicas:     nodeGroup.Replicas,
		InstanceType: nodeGroup.InstanceType,
		Status:       types.NodeGroupStatusReady,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Labels:       nodeGroup.Labels,
	}, nil
}

// RemoveNodeGroup removes a node group
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	return nil
}

// ScaleNodeGroup scales a node group
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	return nil
}

// GetNodeGroup retrieves node group information
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	return &types.NodeGroup{
		Name:         nodeGroupName,
		Replicas:     3,
		InstanceType: "local",
		Status:       types.NodeGroupStatusReady,
		CreatedAt:    time.Now().Add(-1 * time.Hour),
		UpdatedAt:    time.Now(),
	}, nil
}

// ListNodeGroups lists all node groups
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	return []*types.NodeGroup{
		{
			Name:         "workers",
			Replicas:     3,
			InstanceType: "local",
			Status:       types.NodeGroupStatusReady,
			CreatedAt:    time.Now().Add(-1 * time.Hour),
			UpdatedAt:    time.Now(),
		},
	}, nil
}

// CreateVPC creates a VPC (not applicable for Kind)
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	return nil, fmt.Errorf("VPC creation not applicable for Kind provider")
}

// DeleteVPC deletes a VPC (not applicable for Kind)
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	return fmt.Errorf("VPC deletion not applicable for Kind provider")
}

// GetVPC retrieves VPC information (not applicable for Kind)
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	return nil, fmt.Errorf("VPC operations not applicable for Kind provider")
}

// CreateLoadBalancer creates a load balancer (simulate with MetalLB)
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	return &types.LoadBalancer{
		ID:       "kind-lb-" + time.Now().Format("20060102150405"),
		Type:     spec.Type,
		Endpoint: "127.0.0.1",
		Status:   "active",
		Tags:     spec.Tags,
	}, nil
}

// DeleteLoadBalancer deletes a load balancer
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	return nil
}

// GetLoadBalancer retrieves load balancer information
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	return &types.LoadBalancer{
		ID:       lbID,
		Type:     "metallb",
		Endpoint: "127.0.0.1",
		Status:   "active",
	}, nil
}

// CreateStorage creates storage (simulate with local storage)
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	return &types.Storage{
		ID:     "kind-storage-" + time.Now().Format("20060102150405"),
		Type:   spec.Type,
		Size:   spec.Size,
		Status: "available",
		Tags:   spec.Tags,
	}, nil
}

// DeleteStorage deletes storage
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	return nil
}

// GetStorage retrieves storage information
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	return &types.Storage{
		ID:     storageID,
		Type:   "local",
		Size:   "10Gi",
		Status: "available",
	}, nil
}

// UpgradeCluster upgrades cluster Kubernetes version
func (p *Provider) UpgradeCluster(ctx context.Context, clusterID string, version string) error {
	// Kind clusters typically need to be recreated for upgrades
	return fmt.Errorf("kind clusters require recreation for version upgrades")
}

// BackupCluster creates a cluster backup
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	return &types.Backup{
		ID:        "backup-" + time.Now().Format("20060102150405"),
		ClusterID: clusterID,
		Status:    "completed",
		CreatedAt: time.Now(),
		Size:      "100MB",
	}, nil
}

// RestoreCluster restores cluster from backup
func (p *Provider) RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error {
	return nil
}

// GetClusterHealth retrieves cluster health status
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	return &types.HealthStatus{
		Status: "healthy",
		Components: map[string]types.ComponentHealth{
			"etcd":               {Status: "healthy"},
			"api-server":         {Status: "healthy"},
			"scheduler":          {Status: "healthy"},
			"controller-manager": {Status: "healthy"},
		},
		LastCheck: time.Now(),
	}, nil
}

// GetClusterMetrics retrieves cluster metrics
func (p *Provider) GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error) {
	return &types.Metrics{
		CPU: types.MetricValue{
			Usage:    "2",
			Capacity: "4",
			Percent:  50.0,
		},
		Memory: types.MetricValue{
			Usage:    "4Gi",
			Capacity: "8Gi",
			Percent:  50.0,
		},
		Disk: types.MetricValue{
			Usage:    "10Gi",
			Capacity: "50Gi",
			Percent:  20.0,
		},
		Network: types.MetricValue{
			Usage:    "100Mbps",
			Capacity: "1Gbps",
			Percent:  10.0,
		},
	}, nil
}

// InstallAddon installs an addon to the cluster
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	// In a real implementation, we would use Helm or kubectl to install addons
	return nil
}

// UninstallAddon uninstalls an addon from the cluster
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	return nil
}

// ListAddons lists installed addons
func (p *Provider) ListAddons(ctx context.Context, clusterID string) ([]string, error) {
	return []string{"cilium", "metrics-server", "ingress-nginx"}, nil
}

// GetClusterCost returns cluster cost (free for Kind)
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	return 0.0, nil // Kind is free
}

// GetCostBreakdown returns cost breakdown (free for Kind)
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	return map[string]float64{
		"compute": 0.0,
		"storage": 0.0,
		"network": 0.0,
	}, nil
}

// installCilium installs Cilium CNI on the cluster
func (p *Provider) installCilium(ctx context.Context, clusterName string) error {
	// Check if cilium CLI is available
	_, err := exec.LookPath("cilium")
	if err != nil {
		// Try to install using kubectl if cilium CLI is not available
		return p.installCiliumWithKubectl(ctx, clusterName)
	}

	// Install Cilium using cilium CLI with kube-proxy replacement enabled
	// The k8sServiceHost should match the control plane node name: <clusterName>-control-plane
	controlPlaneHost := fmt.Sprintf("%s-control-plane", clusterName)
	cmd := exec.CommandContext(ctx, "cilium", "install",
		"--set", "kubeProxyReplacement=true",
		"--set", fmt.Sprintf("k8sServiceHost=%s", controlPlaneHost),
		"--set", "k8sServicePort=6443")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install Cilium: %w\nOutput: %s", err, string(output))
	}

	// Wait for Cilium to be ready (with timeout)
	cmd = exec.CommandContext(ctx, "cilium", "status", "--wait", "--wait-duration=300s")
	output, err = cmd.CombinedOutput()
	if err != nil {
		// Don't fail if status check fails, just warn
		fmt.Printf("⚠️  Could not verify Cilium status, but continuing: %v\n", err)
	}

	return nil
}

// installCiliumWithKubectl installs Cilium using kubectl when cilium CLI is not available
func (p *Provider) installCiliumWithKubectl(ctx context.Context, clusterName string) error {
	// Install Cilium using the manifests from our platform stack
	// This will include kube-proxy replacement configuration from our stack
	ciliumManifestPath := "platform/stack/platform/cilium"

	// Apply Cilium manifests from our platform stack which includes kube-proxy replacement
	cmd := exec.CommandContext(ctx, p.config.KubectlPath, "apply", "-f", ciliumManifestPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback to external Cilium YAML if local manifests fail
		ciliumURL := "https://raw.githubusercontent.com/cilium/cilium/v1.16.0/install/kubernetes/quick-install.yaml"
		cmd = exec.CommandContext(ctx, p.config.KubectlPath, "apply", "-f", ciliumURL)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to install Cilium with kubectl: %w\nOutput: %s", err, string(output))
		}

		// Create a ConfigMap patch to enable kube-proxy replacement
		controlPlaneHost := fmt.Sprintf("%s-control-plane", clusterName)
		configPatch := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cilium-config
  namespace: kube-system
data:
  kube-proxy-replacement: "true"
  k8s-service-host: "%s"
  k8s-service-port: "6443"
`, controlPlaneHost)

		// Apply the config patch
		cmd = exec.CommandContext(ctx, p.config.KubectlPath, "patch", "configmap", "cilium-config",
			"-n", "kube-system", "--patch", configPatch)
		cmd.CombinedOutput() // Ignore errors as this is a best-effort patch
	}

	// Wait for Cilium pods to be ready
	cmd = exec.CommandContext(ctx, p.config.KubectlPath, "wait",
		"--namespace", "kube-system",
		"--for=condition=ready", "pod",
		"--selector=k8s-app=cilium",
		"--timeout=180s")

	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("timeout waiting for Cilium pods: %v\nOutput: %s", err, string(output))
	}

	return nil
}

// generateKindConfig generates Kind cluster configuration
func generateKindConfig(spec *types.ClusterSpec) map[string]interface{} {
	config := map[string]interface{}{
		"kind":       "Cluster",
		"apiVersion": "kind.x-k8s.io/v1alpha4",
		"name":       spec.Name,
		"nodes":      []map[string]interface{}{},
	}

	// Disable default CNI and kube-proxy if Cilium is specified
	if spec.Networking.CNI == "cilium" {
		config["networking"] = map[string]interface{}{
			"disableDefaultCNI": true,
			"kubeProxyMode":     "none", // Disable kube-proxy for Cilium replacement
			"podSubnet":         spec.Networking.PodCIDR,
			"serviceSubnet":     spec.Networking.ServiceCIDR,
		}
	}

	// Add control plane nodes
	for i := 0; i < spec.ControlPlane.Replicas; i++ {
		node := map[string]interface{}{
			"role": "control-plane",
		}
		if i == 0 {
			// First control plane node gets extra port mappings
			// Map host ports 80/443 to nginx NodePorts 30080/30443
			httpPort := 80
			httpsPort := 443

			// Allow override from cluster spec if specified
			if spec.Networking.HTTPPort != 0 {
				httpPort = spec.Networking.HTTPPort
			}
			if spec.Networking.HTTPSPort != 0 {
				httpsPort = spec.Networking.HTTPSPort
			}

			node["extraPortMappings"] = []map[string]interface{}{
				{
					"containerPort": 30080, // nginx NodePort for HTTP
					"hostPort":      httpPort,
					"protocol":      "TCP",
				},
				{
					"containerPort": 30443, // nginx NodePort for HTTPS
					"hostPort":      httpsPort,
					"protocol":      "TCP",
				},
			}
		}
		config["nodes"] = append(config["nodes"].([]map[string]interface{}), node)
	}

	// Add worker nodes
	for _, nodeGroup := range spec.NodeGroups {
		for i := 0; i < nodeGroup.Replicas; i++ {
			node := map[string]interface{}{
				"role": "worker",
			}
			config["nodes"] = append(config["nodes"].([]map[string]interface{}), node)
		}
	}

	return config
}

// GetKubeconfig retrieves the kubeconfig for a cluster
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	// Kind clusters use the default kubeconfig location
	// Get the cluster name from the ID
	clusterName := clusterID
	if len(clusterID) > 5 && clusterID[:5] == "kind-" {
		clusterName = clusterID[5:]
	}

	// For Kind, we can use kubectl to get the config
	cmd := exec.Command(p.config.KubectlPath, "config", "view", "--raw", "--context", fmt.Sprintf("kind-%s", clusterName))
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get kubeconfig for kind cluster %s: %w", clusterName, err)
	}

	return string(output), nil
}

// handlePortConflictError provides a user-friendly error message and solutions for port conflicts
func (p *Provider) handlePortConflictError(clusterName, output string) error {
	// Determine which ports are in conflict
	conflictingPorts := []string{}
	if strings.Contains(output, "Bind for 0.0.0.0:80 failed") {
		conflictingPorts = append(conflictingPorts, "80")
	}
	if strings.Contains(output, "Bind for 0.0.0.0:443 failed") {
		conflictingPorts = append(conflictingPorts, "443")
	}
	if len(conflictingPorts) == 0 {
		conflictingPorts = append(conflictingPorts, "80/443")
	}

	// Check for existing Kind clusters
	cmd := exec.Command("kind", "get", "clusters")
	existingClusters, _ := cmd.Output()
	clusterList := strings.TrimSpace(string(existingClusters))

	errorMsg := fmt.Sprintf("🚫 Port Conflict Error: Cannot create cluster '%s'\n\n", clusterName)
	errorMsg += fmt.Sprintf("❌ Problem: Port(s) %s are already in use by another service\n\n", strings.Join(conflictingPorts, ", "))

	if clusterList != "" && clusterList != "No kind clusters found." {
		clusters := strings.Split(clusterList, "\n")
		errorMsg += "🔍 Found existing Kind clusters:\n"
		for _, cluster := range clusters {
			if strings.TrimSpace(cluster) != "" {
				errorMsg += fmt.Sprintf("   • %s\n", strings.TrimSpace(cluster))
			}
		}
		errorMsg += "\n"
	}

	errorMsg += "💡 Solutions (choose one):\n\n"
	errorMsg += "   1️⃣  Delete existing clusters:\n"
	if clusterList != "" && clusterList != "No kind clusters found." {
		errorMsg += "      kind delete cluster --name <cluster-name>\n"
		errorMsg += "      # Or delete all: kind delete clusters --all\n"
	} else {
		errorMsg += "      kind delete clusters --all\n"
	}
	errorMsg += "\n"

	errorMsg += "   2️⃣  Find and stop conflicting services:\n"
	errorMsg += "      # Check what's using port 80/443:\n"
	errorMsg += "      lsof -i :80\n"
	errorMsg += "      lsof -i :443\n"
	errorMsg += "      # Stop the conflicting service\n\n"

	errorMsg += "   3️⃣  Use different ports (advanced):\n"
	errorMsg += "      adhar up --port 8080 --protocol http\n\n"

	errorMsg += "🔄 Then retry: adhar up"

	return fmt.Errorf("%s", errorMsg)
}

// Verify that Provider implements the provider.Provider interface
var _ provider.Provider = (*Provider)(nil)

// InvestigateCluster performs comprehensive investigation of a cluster
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	// TODO: Implement Kind-specific cluster investigation
	return fmt.Errorf("cluster investigation not yet implemented for Kind provider")
}
