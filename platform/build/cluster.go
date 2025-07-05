package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// ManagementClusterConfig represents the configuration for cluster deployment
type ManagementClusterConfig struct {
	Cluster struct {
		Name                 string `yaml:"name"`
		KubernetesVersion    string `yaml:"kubernetesVersion"`
		ControlPlaneEndpoint string `yaml:"controlPlaneEndpoint"`
		Networking           struct {
			PodSubnet     string `yaml:"podSubnet"`
			ServiceSubnet string `yaml:"serviceSubnet"`
			DNSDomain     string `yaml:"dnsDomain"`
		} `yaml:"networking"`
		Masters []struct {
			Name     string `yaml:"name"`
			IP       string `yaml:"ip"`
			Hostname string `yaml:"hostname"`
		} `yaml:"masters"`
		Workers []struct {
			Name     string `yaml:"name"`
			IP       string `yaml:"ip"`
			Hostname string `yaml:"hostname"`
		} `yaml:"workers"`
	} `yaml:"cluster"`
	Cilium struct {
		Version  string `yaml:"version"`
		Features struct {
			KubeProxyReplacement bool   `yaml:"kubeProxyReplacement"`
			Hubble               bool   `yaml:"hubble"`
			Encryption           bool   `yaml:"encryption"`
			EncryptionType       string `yaml:"encryptionType"`
			L7Proxy              bool   `yaml:"l7Proxy"`
			GatewayAPI           bool   `yaml:"gatewayAPI"`
		} `yaml:"features"`
	} `yaml:"cilium"`
	Monitoring struct {
		Prometheus struct {
			Enabled     bool   `yaml:"enabled"`
			Retention   string `yaml:"retention"`
			StorageSize string `yaml:"storageSize"`
		} `yaml:"prometheus"`
		Grafana struct {
			Enabled       bool   `yaml:"enabled"`
			AdminPassword string `yaml:"adminPassword"`
		} `yaml:"grafana"`
		Alertmanager struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"alertmanager"`
	} `yaml:"monitoring"`
	Security struct {
		Audit struct {
			Enabled    bool   `yaml:"enabled"`
			LogPath    string `yaml:"logPath"`
			PolicyFile string `yaml:"policyFile"`
		} `yaml:"audit"`
		NetworkPolicies struct {
			DefaultDeny bool `yaml:"defaultDeny"`
			AllowDNS    bool `yaml:"allowDNS"`
		} `yaml:"networkPolicies"`
		RBAC struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"rbac"`
	} `yaml:"security"`
}

// ManagementCluster represents a management cluster deployment
type ManagementCluster struct {
	config     *ManagementClusterConfig
	scriptsDir string
	logger     *logrus.Logger
}

// NewManagementCluster creates a new management cluster instance
func NewManagementCluster(configPath string) (*ManagementCluster, error) {
	// Load configuration
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read configuration file: %w", err)
	}

	var config ManagementClusterConfig
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return nil, fmt.Errorf("failed to parse configuration: %w", err)
	}

	// Determine scripts directory
	scriptsDir := filepath.Dir(configPath)

	return &ManagementCluster{
		config:     &config,
		scriptsDir: scriptsDir,
		logger:     logrus.New(),
	}, nil
}

// Deploy deploys the management cluster
func (mc *ManagementCluster) Deploy(ctx context.Context) error {
	mc.logger.Info("Starting management cluster deployment")

	// Validate prerequisites
	if err := mc.validatePrerequisites(); err != nil {
		return fmt.Errorf("prerequisite validation failed: %w", err)
	}

	// Run bootstrap script
	if err := mc.runBootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// Validate deployment
	if err := mc.validateDeployment(ctx); err != nil {
		return fmt.Errorf("deployment validation failed: %w", err)
	}

	// Install Crossplane for environment provisioning
	if err := mc.installCrossplane(ctx); err != nil {
		return fmt.Errorf("Crossplane installation failed: %w", err)
	}

	// Setup RBAC for Adhar platform
	if err := mc.setupAdharRBAC(ctx); err != nil {
		return fmt.Errorf("Adhar RBAC setup failed: %w", err)
	}

	mc.logger.Info("Management cluster deployment completed successfully")
	return nil
}

// validatePrerequisites validates system prerequisites
func (mc *ManagementCluster) validatePrerequisites() error {
	mc.logger.Info("Validating prerequisites")

	// Check if running on supported OS (simplified check)
	// Note: This is a simplified implementation - in production you'd have more thorough checks

	// Check if scripts exist
	bootstrapScript := filepath.Join(mc.scriptsDir, "bootstrap.sh")
	if _, err := os.Stat(bootstrapScript); os.IsNotExist(err) {
		return fmt.Errorf("bootstrap script not found: %s", bootstrapScript)
	}

	// Check if user has sudo privileges (simplified check)
	cmd := exec.Command("sudo", "-n", "true")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("user must have sudo privileges")
	}

	// Check if required tools are available
	requiredTools := []string{"curl", "wget", "systemctl"}
	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool not found: %s", tool)
		}
	}

	mc.logger.Info("Prerequisites validation passed")
	return nil
}

// runBootstrap executes the bootstrap script
func (mc *ManagementCluster) runBootstrap(ctx context.Context) error {
	mc.logger.Info("Running bootstrap script")

	bootstrapScript := filepath.Join(mc.scriptsDir, "bootstrap.sh")

	// Make script executable
	if err := os.Chmod(bootstrapScript, 0755); err != nil {
		return fmt.Errorf("failed to make bootstrap script executable: %w", err)
	}

	// Execute bootstrap script
	cmd := exec.CommandContext(ctx, "bash", bootstrapScript)
	cmd.Dir = mc.scriptsDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootstrap script execution failed: %w", err)
	}

	mc.logger.Info("Bootstrap script completed")
	return nil
}

// validateDeployment validates the cluster deployment
func (mc *ManagementCluster) validateDeployment(ctx context.Context) error {
	mc.logger.Info("Validating cluster deployment")

	// Check if kubectl is accessible
	if err := mc.runKubectl(ctx, "get", "nodes"); err != nil {
		return fmt.Errorf("kubectl access failed: %w", err)
	}

	// Check if Cilium is ready
	if err := mc.runCilium(ctx, "status"); err != nil {
		return fmt.Errorf("Cilium validation failed: %w", err)
	}

	// Check system pods with increased timeout for slow environments
	systemPods := []string{
		"kube-system/daemonset/cilium",
		"kube-system/deployment/cilium-operator",
		"kube-system/deployment/coredns",
		"local-path-storage/deployment/local-path-provisioner", // Add storage provisioner validation
	}

	for _, pod := range systemPods {
		if err := mc.waitForResource(ctx, pod, 60*time.Minute); err != nil {
			return fmt.Errorf("system pod not ready: %s", pod)
		}
	}

	mc.logger.Info("Cluster deployment validation passed")
	return nil
}

// installCrossplane installs Crossplane for environment provisioning
func (mc *ManagementCluster) installCrossplane(ctx context.Context) error {
	mc.logger.Info("Installing Crossplane")

	// Add Crossplane Helm repository
	if err := mc.runHelm(ctx, "repo", "add", "crossplane-stable", "https://charts.crossplane.io/stable"); err != nil {
		return fmt.Errorf("failed to add Crossplane repo: %w", err)
	}

	if err := mc.runHelm(ctx, "repo", "update"); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w", err)
	}

	// Install Crossplane
	crossplaneArgs := []string{
		"upgrade", "--install", "crossplane",
		"crossplane-stable/crossplane",
		"--namespace", "crossplane-system",
		"--create-namespace",
		"--set", "metrics.enabled=true",
		"--set", "resourcesCrossplane.limits.memory=2Gi",
		"--set", "resourcesCrossplane.requests.memory=1Gi",
		"--wait", "--timeout=20m",
	}

	if err := mc.runHelm(ctx, crossplaneArgs...); err != nil {
		return fmt.Errorf("failed to install Crossplane: %w", err)
	}

	// Wait for Crossplane to be ready
	if err := mc.waitForResource(ctx, "crossplane-system/deployment/crossplane", 5*time.Minute); err != nil {
		return fmt.Errorf("Crossplane not ready: %w", err)
	}

	mc.logger.Info("Crossplane installed successfully")
	return nil
}

// setupAdharRBAC sets up RBAC for the Adhar platform
func (mc *ManagementCluster) setupAdharRBAC(ctx context.Context) error {
	mc.logger.Info("Setting up Adhar platform RBAC")

	// Create Adhar namespace
	if err := mc.runKubectl(ctx, "create", "namespace", "adhar-system", "--dry-run=client", "-o", "yaml"); err == nil {
		if err := mc.runKubectl(ctx, "apply", "-f", "-"); err != nil {
			return fmt.Errorf("failed to create adhar-system namespace: %w", err)
		}
	}

	// Apply RBAC manifests
	rbacManifest := `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: adhar-platform
  namespace: adhar-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: adhar-platform-manager
rules:
- apiGroups: ["*"]
  resources: ["*"]
  verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: adhar-platform-manager
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: adhar-platform-manager
subjects:
- kind: ServiceAccount
  name: adhar-platform
  namespace: adhar-system
`

	if err := mc.applyManifest(ctx, rbacManifest); err != nil {
		return fmt.Errorf("failed to apply RBAC manifests: %w", err)
	}

	mc.logger.Info("Adhar platform RBAC configured")
	return nil
}

// GetStatus returns the current status of the management cluster
func (mc *ManagementCluster) GetStatus(ctx context.Context) (*ClusterStatus, error) {
	status := &ClusterStatus{
		Name:      mc.config.Cluster.Name,
		Timestamp: time.Now(),
	}

	// Check cluster connectivity
	if err := mc.runKubectl(ctx, "get", "nodes", "--no-headers"); err != nil {
		status.Status = "Unreachable"
		status.Message = "Cannot connect to cluster"
		return status, nil
	}

	// Get node status
	nodes, err := mc.getNodes(ctx)
	if err != nil {
		status.Status = "Error"
		status.Message = fmt.Sprintf("Failed to get nodes: %v", err)
		return status, nil
	}
	status.Nodes = nodes

	// Check Cilium status
	ciliumStatus, err := mc.getCiliumStatus(ctx)
	if err != nil {
		status.Status = "Degraded"
		status.Message = fmt.Sprintf("Cilium issues: %v", err)
	} else {
		status.CiliumStatus = ciliumStatus
	}

	// Check Crossplane status
	crossplaneStatus, err := mc.getCrossplaneStatus(ctx)
	if err != nil {
		status.Status = "Degraded"
		status.Message = fmt.Sprintf("Crossplane issues: %v", err)
	} else {
		status.CrossplaneStatus = crossplaneStatus
	}

	// Determine overall status
	if status.Status == "" {
		if mc.allNodesReady(nodes) && ciliumStatus == "OK" && crossplaneStatus == "Ready" {
			status.Status = "Ready"
			status.Message = "All systems operational"
		} else {
			status.Status = "Degraded"
			status.Message = "Some components not ready"
		}
	}

	return status, nil
}

// Helper methods

func (mc *ManagementCluster) runKubectl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	return cmd.Run()
}

func (mc *ManagementCluster) runCilium(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "cilium", args...)
	return cmd.Run()
}

func (mc *ManagementCluster) runHelm(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "helm", args...)
	return cmd.Run()
}

func (mc *ManagementCluster) applyManifest(ctx context.Context, manifest string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	return cmd.Run()
}

func (mc *ManagementCluster) waitForResource(ctx context.Context, resource string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mc.logger.Info("Waiting for resource to be ready", "resource", resource, "timeout", timeout.String())

	retryCount := 0
	for {
		select {
		case <-ctx.Done():
			mc.logger.Error(nil, "Timeout waiting for resource", "resource", resource, "attempts", retryCount, "timeout", timeout.String())
			return fmt.Errorf("timeout waiting for resource: %s after %d attempts", resource, retryCount)
		default:
			retryCount++
			// Increase kubectl wait timeout to 300s for individual checks
			if err := mc.runKubectl(ctx, "wait", "--for=condition=Ready", resource, "--timeout=300s"); err == nil {
				mc.logger.Info("Resource is ready", "resource", resource, "attempts", retryCount)
				return nil
			}

			// Log progress every 6 attempts (1 minute)
			if retryCount%6 == 0 {
				mc.logger.Info("Still waiting for resource", "resource", resource, "attempts", retryCount, "elapsed", fmt.Sprintf("%.1f minutes", float64(retryCount)*10.0/60.0))
			}

			time.Sleep(10 * time.Second)
		}
	}
}

func (mc *ManagementCluster) getNodes(ctx context.Context) ([]NodeInfo, error) {
	// Implementation for getting node information
	// This would parse kubectl output and return node details
	return []NodeInfo{}, nil
}

func (mc *ManagementCluster) getCiliumStatus(ctx context.Context) (string, error) {
	// Implementation for getting Cilium status
	return "OK", nil
}

func (mc *ManagementCluster) getCrossplaneStatus(ctx context.Context) (string, error) {
	// Implementation for getting Crossplane status
	return "Ready", nil
}

func (mc *ManagementCluster) allNodesReady(nodes []NodeInfo) bool {
	for _, node := range nodes {
		if node.Status != "Ready" {
			return false
		}
	}
	return true
}

// Status types

type ClusterStatus struct {
	Name             string     `json:"name"`
	Status           string     `json:"status"`
	Message          string     `json:"message"`
	Timestamp        time.Time  `json:"timestamp"`
	Nodes            []NodeInfo `json:"nodes"`
	CiliumStatus     string     `json:"ciliumStatus"`
	CrossplaneStatus string     `json:"crossplaneStatus"`
}

type NodeInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Role   string `json:"role"`
	Age    string `json:"age"`
}
