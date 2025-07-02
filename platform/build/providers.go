package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/management"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sirupsen/logrus"
)

// Provisioner defines the interface for provisioning environments
type Provisioner interface {
	Provision(config *config.ResolvedEnvironmentConfig) error
}

// --- Message types for status updates ---

// StepMsg signals a change in the major step of the provisioning process.
type StepMsg string

// StatusMsg provides a detailed status update within a step.
type StatusMsg string

// ErrorMsg signals an error occurred during provisioning.
type ErrorMsg struct{ Err error }

// DoneMsg signals the successful completion of the provisioning process.
type DoneMsg struct{}

// ClusterInfoMsg provides detailed cluster information.
type ClusterInfoMsg string

// Send is a helper function to send messages to the UI channel.
func Send(ch chan<- tea.Msg, msg tea.Msg) {
	if ch != nil {
		ch <- msg
	}
}

// DigitalOceanProvisioner implements cluster provisioning for DigitalOcean
type DigitalOceanProvisioner struct {
	token  string
	logger *logrus.Logger
}

// NewDigitalOceanProvisioner creates a new DigitalOcean provisioner
func NewDigitalOceanProvisioner(token string, logger *logrus.Logger) Provisioner {
	return &DigitalOceanProvisioner{
		token:  token,
		logger: logger,
	}
}

// Provision provisions a DigitalOcean Kubernetes cluster
func (d *DigitalOceanProvisioner) Provision(envConfig *config.ResolvedEnvironmentConfig) error {
	d.logger.Info("Starting DigitalOcean cluster provisioning")

	// Extract cluster configuration
	clusterName := fmt.Sprintf("adhar-%s", envConfig.Name)
	region := envConfig.ResolvedRegion

	// Get node pool configuration
	nodeSize := "s-2vcpu-4gb" // default
	nodeCount := 3            // default

	// For management cluster, use larger nodes and higher count
	if envConfig.Name == "adhar-management" {
		nodeSize = "s-4vcpu-8gb" // More resources for management cluster
		nodeCount = 3            // HA setup
		d.logger.Info("Provisioning management cluster with enhanced configuration")
	}

	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "nodeSize":
			nodeSize = cfg.Value
		case "nodeCount":
			nodeCount = parseInt(cfg.Value, 3)
		case "name":
			clusterName = cfg.Value
		}
	}

	// Create cluster using doctl CLI
	cmd := exec.Command("doctl", "kubernetes", "cluster", "create",
		clusterName,
		"--region", region,
		"--size", nodeSize,
		"--count", fmt.Sprintf("%d", nodeCount),
		"--version", "latest",
		"--wait",
		"--token", d.token,
	)

	d.logger.Info("Executing doctl command", "cmd", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create DigitalOcean cluster: %w, output: %s", err, string(output))
	}

	d.logger.Info("DigitalOcean cluster created successfully", "output", string(output))

	// Configure kubectl
	if err := d.configureKubectl(clusterName); err != nil {
		return fmt.Errorf("failed to configure kubectl: %w", err)
	}

	// For management cluster, ensure Cilium is enabled (DigitalOcean uses Cilium by default)
	if envConfig.Name == "adhar-management" {
		d.logger.Info("Management cluster provisioned with Cilium CNI (DigitalOcean default)")
	}

	return nil
}

// configureKubectl configures kubectl to use the new cluster
func (d *DigitalOceanProvisioner) configureKubectl(clusterName string) error {
	cmd := exec.Command("doctl", "kubernetes", "cluster", "kubeconfig", "save",
		clusterName,
		"--token", d.token,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to configure kubectl: %w, output: %s", err, string(output))
	}

	d.logger.Info("Kubectl configured successfully", "output", string(output))
	return nil
}

// GCPProvisioner implements cluster provisioning for GCP
type GCPProvisioner struct {
	creds  *config.CredentialSource
	logger *logrus.Logger
}

// NewGCPProvisioner creates a new GCP provisioner
func NewGCPProvisioner(creds *config.CredentialSource, logger *logrus.Logger) Provisioner {
	return &GCPProvisioner{
		creds:  creds,
		logger: logger,
	}
}

// Provision provisions a GKE cluster
func (g *GCPProvisioner) Provision(envConfig *config.ResolvedEnvironmentConfig) error {
	g.logger.Info("Starting GCP GKE cluster provisioning")

	// Set up authentication
	if err := g.setupAuth(); err != nil {
		return fmt.Errorf("failed to setup GCP authentication: %w", err)
	}

	// Extract cluster configuration
	clusterName := fmt.Sprintf("adhar-%s", envConfig.Name)
	region := envConfig.ResolvedRegion
	projectID := g.creds.ProjectID

	// Get cluster configuration
	machineType := "e2-standard-4" // default
	numNodes := 3                  // default

	// For management cluster, use larger machines and ensure Cilium
	if envConfig.Name == "adhar-management" {
		machineType = "e2-standard-8" // More resources for management cluster
		numNodes = 3                  // HA setup
		g.logger.Info("Provisioning management cluster with enhanced configuration")
	}

	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "machineType":
			machineType = cfg.Value
		case "numNodes":
			numNodes = parseInt(cfg.Value, 3)
		case "name":
			clusterName = cfg.Value
		case "projectID":
			projectID = cfg.Value
		}
	}

	if projectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}

	// Create cluster using gcloud CLI with enhanced security and networking
	cmdArgs := []string{
		"container", "clusters", "create", clusterName,
		"--zone", region,
		"--machine-type", machineType,
		"--num-nodes", fmt.Sprintf("%d", numNodes),
		"--enable-network-policy",
		"--enable-ip-alias",
		"--enable-autoscaling",
		"--min-nodes", "1",
		"--max-nodes", "10",
		"--enable-autorepair",
		"--enable-autoupgrade",
		"--project", projectID,
	}

	// For management cluster, add additional security and networking features
	if envConfig.Name == "adhar-management" {
		cmdArgs = append(cmdArgs,
			"--enable-private-nodes",
			"--master-ipv4-cidr", "172.16.0.0/28",
			"--enable-master-authorized-networks",
			"--enable-shielded-nodes",
			"--enable-network-policy",
			"--network-policy", "calico", // Will be replaced with Cilium post-provision
		)
	}

	cmd := exec.Command("gcloud", cmdArgs...)
	g.logger.Info("Executing gcloud command", "cmd", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create GKE cluster: %w, output: %s", err, string(output))
	}

	g.logger.Info("GKE cluster created successfully", "output", string(output))

	// Configure kubectl
	if err := g.configureKubectl(clusterName, region, projectID); err != nil {
		return fmt.Errorf("failed to configure kubectl: %w", err)
	}

	return nil
}

// setupAuth sets up GCP authentication
func (g *GCPProvisioner) setupAuth() error {
	if g.creds.Type == "file" && g.creds.Path != "" {
		// Set GOOGLE_APPLICATION_CREDENTIALS environment variable
		expandedPath := os.ExpandEnv(g.creds.Path)
		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", expandedPath)
		g.logger.Info("Set GOOGLE_APPLICATION_CREDENTIALS", "path", expandedPath)
	}

	// Authenticate with gcloud
	cmd := exec.Command("gcloud", "auth", "application-default", "print-access-token")
	_, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to authenticate with GCP: %w", err)
	}

	return nil
}

// configureKubectl configures kubectl to use the new GKE cluster
func (g *GCPProvisioner) configureKubectl(clusterName, zone, projectID string) error {
	cmd := exec.Command("gcloud", "container", "clusters", "get-credentials",
		clusterName,
		"--zone", zone,
		"--project", projectID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to configure kubectl: %w, output: %s", err, string(output))
	}

	g.logger.Info("Kubectl configured successfully", "output", string(output))
	return nil
}

// AWSProvisioner implements cluster provisioning for AWS
type AWSProvisioner struct {
	creds  *config.CredentialSource
	logger *logrus.Logger
}

// NewAWSProvisioner creates a new AWS provisioner
func NewAWSProvisioner(creds *config.CredentialSource, logger *logrus.Logger) Provisioner {
	return &AWSProvisioner{
		creds:  creds,
		logger: logger,
	}
}

// Provision provisions an EKS cluster
func (a *AWSProvisioner) Provision(envConfig *config.ResolvedEnvironmentConfig) error {
	a.logger.Info("Starting AWS EKS cluster provisioning")

	// Extract cluster configuration
	clusterName := fmt.Sprintf("adhar-%s", envConfig.Name)
	region := envConfig.ResolvedRegion

	// Get cluster configuration
	nodeInstanceType := "t3.medium" // default
	nodeCount := 3                  // default
	kubernetesVersion := "1.29"     // default

	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "nodeInstanceType":
			nodeInstanceType = cfg.Value
		case "nodeCount":
			nodeCount = parseInt(cfg.Value, 3)
		case "name":
			clusterName = cfg.Value
		case "kubernetesVersion":
			kubernetesVersion = cfg.Value
		}
	}

	// Create cluster using eksctl
	cmd := exec.Command("eksctl", "create", "cluster",
		"--name", clusterName,
		"--region", region,
		"--version", kubernetesVersion,
		"--nodegroup-name", fmt.Sprintf("%s-workers", clusterName),
		"--node-type", nodeInstanceType,
		"--nodes", fmt.Sprintf("%d", nodeCount),
		"--nodes-min", "1",
		"--nodes-max", "10",
		"--managed",
		"--enable-ssm",
		"--with-oidc",
	)

	a.logger.Info("Executing eksctl command", "cmd", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create EKS cluster: %w, output: %s", err, string(output))
	}

	a.logger.Info("EKS cluster created successfully", "output", string(output))

	// Configure kubectl
	if err := a.configureKubectl(clusterName, region); err != nil {
		return fmt.Errorf("failed to configure kubectl: %w", err)
	}

	return nil
}

// configureKubectl configures kubectl to use the new EKS cluster
func (a *AWSProvisioner) configureKubectl(clusterName, region string) error {
	cmd := exec.Command("aws", "eks", "update-kubeconfig",
		"--region", region,
		"--name", clusterName,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to configure kubectl: %w, output: %s", err, string(output))
	}

	a.logger.Info("Kubectl configured successfully", "output", string(output))
	return nil
}

// AzureProvisioner implements cluster provisioning for Azure
type AzureProvisioner struct {
	creds  *config.CredentialSource
	logger *logrus.Logger
}

// NewAzureProvisioner creates a new Azure provisioner
func NewAzureProvisioner(creds *config.CredentialSource, logger *logrus.Logger) Provisioner {
	return &AzureProvisioner{
		creds:  creds,
		logger: logger,
	}
}

// Provision provisions an AKS cluster
func (az *AzureProvisioner) Provision(envConfig *config.ResolvedEnvironmentConfig) error {
	az.logger.Info("Starting Azure AKS cluster provisioning")

	// Extract cluster configuration
	clusterName := fmt.Sprintf("adhar-%s", envConfig.Name)
	region := envConfig.ResolvedRegion
	resourceGroup := fmt.Sprintf("rg-%s", clusterName)

	// Get cluster configuration
	vmSize := "Standard_D2_v3"  // default
	nodeCount := 3              // default
	kubernetesVersion := "1.29" // default

	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "vmSize":
			vmSize = cfg.Value
		case "nodeCount":
			nodeCount = parseInt(cfg.Value, 3)
		case "name":
			clusterName = cfg.Value
		case "resourceGroup":
			resourceGroup = cfg.Value
		case "kubernetesVersion":
			kubernetesVersion = cfg.Value
		}
	}

	// Create resource group
	if err := az.createResourceGroup(resourceGroup, region); err != nil {
		return fmt.Errorf("failed to create resource group: %w", err)
	}

	// Create cluster using az CLI
	cmd := exec.Command("az", "aks", "create",
		"--resource-group", resourceGroup,
		"--name", clusterName,
		"--location", region,
		"--kubernetes-version", kubernetesVersion,
		"--node-count", fmt.Sprintf("%d", nodeCount),
		"--node-vm-size", vmSize,
		"--enable-addons", "monitoring",
		"--enable-cluster-autoscaler",
		"--min-count", "1",
		"--max-count", "10",
		"--generate-ssh-keys",
		"--enable-managed-identity",
	)

	az.logger.Info("Executing az aks create command", "cmd", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create AKS cluster: %w, output: %s", err, string(output))
	}

	az.logger.Info("AKS cluster created successfully", "output", string(output))

	// Configure kubectl
	if err := az.configureKubectl(clusterName, resourceGroup); err != nil {
		return fmt.Errorf("failed to configure kubectl: %w", err)
	}

	return nil
}

// createResourceGroup creates an Azure resource group
func (az *AzureProvisioner) createResourceGroup(resourceGroup, region string) error {
	cmd := exec.Command("az", "group", "create",
		"--name", resourceGroup,
		"--location", region,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create resource group: %w, output: %s", err, string(output))
	}

	az.logger.Info("Resource group created successfully", "output", string(output))
	return nil
}

// configureKubectl configures kubectl to use the new AKS cluster
func (az *AzureProvisioner) configureKubectl(clusterName, resourceGroup string) error {
	cmd := exec.Command("az", "aks", "get-credentials",
		"--resource-group", resourceGroup,
		"--name", clusterName,
		"--overwrite-existing",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to configure kubectl: %w, output: %s", err, string(output))
	}

	az.logger.Info("Kubectl configured successfully", "output", string(output))
	return nil
}

// CivoProvisioner implements cluster provisioning for Civo
type CivoProvisioner struct {
	token  string
	logger *logrus.Logger
}

// NewCivoProvisioner creates a new Civo provisioner
func NewCivoProvisioner(token string, logger *logrus.Logger) Provisioner {
	return &CivoProvisioner{
		token:  token,
		logger: logger,
	}
}

// Provision provisions a Civo Kubernetes cluster
func (c *CivoProvisioner) Provision(envConfig *config.ResolvedEnvironmentConfig) error {
	c.logger.Info("Starting Civo cluster provisioning")

	// Extract cluster configuration
	clusterName := fmt.Sprintf("adhar-%s", envConfig.Name)
	region := envConfig.ResolvedRegion

	// Get node configuration
	nodeSize := "g4s.kube.medium" // default
	nodeCount := 3                // default

	for _, cfg := range envConfig.ResolvedClusterConfig {
		switch cfg.Key {
		case "nodeSize":
			nodeSize = cfg.Value
		case "nodeCount":
			nodeCount = parseInt(cfg.Value, 3)
		case "name":
			clusterName = cfg.Value
		}
	}

	// Set Civo API token
	os.Setenv("CIVO_TOKEN", c.token)

	// Create cluster using civo CLI
	cmd := exec.Command("civo", "kubernetes", "create",
		clusterName,
		"--region", region,
		"--size", nodeSize,
		"--nodes", fmt.Sprintf("%d", nodeCount),
		"--wait",
	)

	c.logger.Info("Executing civo command", "cmd", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create Civo cluster: %w, output: %s", err, string(output))
	}

	c.logger.Info("Civo cluster created successfully", "output", string(output))

	// Configure kubectl
	if err := c.configureKubectl(clusterName); err != nil {
		return fmt.Errorf("failed to configure kubectl: %w", err)
	}

	return nil
}

// configureKubectl configures kubectl to use the new Civo cluster
func (c *CivoProvisioner) configureKubectl(clusterName string) error {
	cmd := exec.Command("civo", "kubernetes", "config", clusterName, "--save")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to configure kubectl: %w, output: %s", err, string(output))
	}

	c.logger.Info("Kubectl configured successfully", "output", string(output))
	return nil
}

// OnPremProvisioner implements cluster provisioning for on-premises
type OnPremProvisioner struct {
	logger *logrus.Logger
}

// NewOnPremProvisioner creates a new on-premises provisioner
func NewOnPremProvisioner(logger *logrus.Logger) Provisioner {
	return &OnPremProvisioner{
		logger: logger,
	}
}

// Provision provisions an on-premises Kubernetes cluster
func (o *OnPremProvisioner) Provision(envConfig *config.ResolvedEnvironmentConfig) error {
	o.logger.Info("Starting on-premises cluster provisioning", "environment", envConfig.Name)

	// Check if this is management cluster provisioning
	if envConfig.Name == "adhar-management" {
		return o.provisionManagementCluster(envConfig)
	}

	// For regular environments, expect cluster to already exist and validate connectivity
	return o.validateExistingCluster(envConfig)
}

// provisionManagementCluster provisions the management cluster using bootstrap scripts
func (o *OnPremProvisioner) provisionManagementCluster(envConfig *config.ResolvedEnvironmentConfig) error {
	o.logger.Info("Provisioning management cluster using bootstrap scripts and management module")

	// Get the current working directory to locate scripts
	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	scriptsDir := filepath.Join(pwd, "scripts", "management-cluster")
	configFile := filepath.Join(scriptsDir, "cluster-config.yaml")

	// Check if management cluster module can handle this
	if _, err := os.Stat(configFile); err == nil {
		o.logger.Info("Using management cluster module for provisioning")

		// Create management cluster instance
		mgmtCluster, err := management.NewManagementCluster(configFile)
		if err != nil {
			o.logger.Warn("Failed to create management cluster instance, falling back to direct script execution", "error", err)
			return o.fallbackToBootstrapScript(envConfig, scriptsDir)
		}

		// Deploy using the management cluster module
		ctx := context.Background() // Create a background context
		if err := mgmtCluster.Deploy(ctx); err != nil {
			o.logger.Warn("Management cluster module deployment failed, falling back to direct script execution", "error", err)
			return o.fallbackToBootstrapScript(envConfig, scriptsDir)
		}

		o.logger.Info("Management cluster provisioned successfully using management module")
		return nil
	}

	// Fallback to direct script execution
	o.logger.Info("Management cluster config not found, using direct bootstrap script execution")
	return o.fallbackToBootstrapScript(envConfig, scriptsDir)
}

// fallbackToBootstrapScript provides fallback bootstrap script execution
func (o *OnPremProvisioner) fallbackToBootstrapScript(envConfig *config.ResolvedEnvironmentConfig, scriptsDir string) error {
	bootstrapScript := filepath.Join(scriptsDir, "bootstrap.sh")

	// Check if bootstrap script exists
	if _, err := os.Stat(bootstrapScript); os.IsNotExist(err) {
		return fmt.Errorf("management cluster bootstrap script not found at %s", bootstrapScript)
	}

	// Execute bootstrap script
	o.logger.Info("Executing management cluster bootstrap script", "script", bootstrapScript)

	cmd := exec.Command("sudo", "bash", bootstrapScript)
	cmd.Dir = scriptsDir

	// Set environment variables for the script
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CLUSTER_NAME=%s", envConfig.Name),
		fmt.Sprintf("ENVIRONMENT_TYPE=%s", envConfig.ResolvedType),
	)

	// Stream output
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("management cluster bootstrap failed: %w", err)
	}

	o.logger.Info("Management cluster bootstrap completed successfully")

	// Validate the cluster is ready
	return o.validateManagementCluster()
}

// validateExistingCluster validates connectivity to an existing cluster
func (o *OnPremProvisioner) validateExistingCluster(envConfig *config.ResolvedEnvironmentConfig) error {
	o.logger.Info("Validating existing on-premises cluster connectivity")

	// Test kubectl connectivity
	cmd := exec.Command("kubectl", "cluster-info")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to connect to existing cluster. Please ensure kubectl is configured: %w, output: %s", err, string(output))
	}

	o.logger.Info("Successfully connected to on-premises cluster", "output", string(output))
	return nil
}

// validateManagementCluster validates that the management cluster is properly set up
func (o *OnPremProvisioner) validateManagementCluster() error {
	o.logger.Info("Validating management cluster deployment")

	// Wait for cluster to be ready
	maxRetries := 60 // Increased from 30 to 60 retries
	for i := 0; i < maxRetries; i++ {
		cmd := exec.Command("kubectl", "get", "nodes", "--no-headers")
		output, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(output))) > 0 {
			// Check if nodes are Ready
			cmd = exec.Command("kubectl", "get", "nodes", "--no-headers", "--output=custom-columns=STATUS:.status.conditions[?(@.type=='Ready')].status")
			statusOutput, err := cmd.Output()
			if err == nil && strings.Contains(string(statusOutput), "True") {
				o.logger.Info("Management cluster nodes are ready")
				break
			}
		}

		if i == maxRetries-1 {
			return fmt.Errorf("management cluster did not become ready within expected time")
		}

		o.logger.Info("Waiting for management cluster to be ready", "attempt", i+1, "maxRetries", maxRetries)
		time.Sleep(45 * time.Second) // Increased from 30 to 45 seconds
	}

	// Validate Cilium is running
	cmd := exec.Command("kubectl", "get", "pods", "-n", "kube-system", "-l", "app.kubernetes.io/name=cilium", "--no-headers")
	output, err := cmd.Output()
	if err != nil {
		o.logger.Warn("Failed to check Cilium pods", "error", err)
	} else if len(strings.TrimSpace(string(output))) == 0 {
		o.logger.Warn("Cilium pods not found, cluster networking may not be ready")
	} else {
		o.logger.Info("Cilium networking is deployed")
	}

	// Validate system pods
	cmd = exec.Command("kubectl", "get", "pods", "-n", "kube-system", "--field-selector=status.phase!=Running", "--no-headers")
	output, err = cmd.Output()
	if err != nil {
		o.logger.Warn("Failed to check system pod status", "error", err)
	} else if len(strings.TrimSpace(string(output))) > 0 {
		o.logger.Warn("Some system pods are not running", "pods", string(output))
	} else {
		o.logger.Info("All system pods are running")
	}

	o.logger.Info("Management cluster validation completed")
	return nil
}

// Helper function to parse integer with default value
func parseInt(s string, defaultValue int) int {
	if s == "" {
		return defaultValue
	}

	var result int
	_, err := fmt.Sscanf(s, "%d", &result)
	if err != nil {
		return defaultValue
	}

	return result
}
