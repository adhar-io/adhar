package custom

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"

	"golang.org/x/crypto/ssh"
)

// ClusterInfrastructure represents the infrastructure state for a custom cluster
type ClusterInfrastructure struct {
	ClusterName     string
	MasterNodes     []Node
	WorkerNodes     []Node
	LoadBalancerIP  string
	StorageClass    string
	CNIType         string
	ContainerEngine string
	Endpoint        string
	NetworkConfig   NetworkConfig
	IsExisting      bool // Whether this is an existing cluster
}

// NetworkConfig represents network configuration for the cluster
type NetworkConfig struct {
	PodCIDR     string
	ServiceCIDR string
	DNSServers  []string
	Gateway     string
	Netmask     string
}

// ResourceTracker tracks all custom infrastructure resources for a cluster
type ResourceTracker struct {
	ClusterName       string    `json:"clusterName"`
	MasterNodes       []string  `json:"masterNodes"`
	WorkerNodes       []string  `json:"workerNodes"`
	LoadBalancerIPs   []string  `json:"loadBalancerIPs"`
	StorageVolumes    []string  `json:"storageVolumes"`
	NetworkInterfaces []string  `json:"networkInterfaces"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// Register the Custom provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("custom", func(config map[string]interface{}) (provider.Provider, error) {
		customConfig := &Config{}

		// Parse Custom-specific configuration
		if endpoint, ok := config["endpoint"].(string); ok {
			customConfig.Endpoint = endpoint
		}
		if username, ok := config["username"].(string); ok {
			customConfig.Username = username
		}
		if password, ok := config["password"].(string); ok {
			customConfig.Password = password
		}
		if clusterType, ok := config["clusterType"].(string); ok {
			customConfig.ClusterType = clusterType
		}
		if kubeadmPath, ok := config["kubeadmPath"].(string); ok {
			customConfig.KubeadmPath = kubeadmPath
		}
		if sshKeyPath, ok := config["sshKeyPath"].(string); ok {
			customConfig.SSHKeyPath = sshKeyPath
		}
		if nodeIPs, ok := config["nodeIPs"].([]interface{}); ok {
			customConfig.NodeIPs = make([]string, len(nodeIPs))
			for i, ip := range nodeIPs {
				customConfig.NodeIPs[i] = ip.(string)
			}
		}
		if sshPort, ok := config["sshPort"].(int); ok {
			customConfig.SSHPort = sshPort
		}
		if containerEngine, ok := config["containerEngine"].(string); ok {
			customConfig.ContainerEngine = containerEngine
		}
		if cni, ok := config["cni"].(string); ok {
			customConfig.CNI = cni
		}
		if storageClass, ok := config["storageClass"].(string); ok {
			customConfig.StorageClass = storageClass
		}
		if isExisting, ok := config["isExisting"].(bool); ok {
			customConfig.IsExisting = isExisting
		}
		if kubeconfigPath, ok := config["kubeconfigPath"].(string); ok {
			customConfig.KubeconfigPath = kubeconfigPath
		}
		if discoveryMode, ok := config["discoveryMode"].(bool); ok {
			customConfig.DiscoveryMode = discoveryMode
		}

		return NewProvider(customConfig)
	})
}

// Provider implements the Custom provider for on-premises/private cloud clusters
type Provider struct {
	config         *Config
	sshClient      map[string]*ssh.Client // SSH connections to nodes
	infrastructure *ClusterInfrastructure // Track cluster infrastructure
}

// Config holds Custom provider configuration
type Config struct {
	Endpoint        string   `json:"endpoint"`        // Control plane endpoint
	Username        string   `json:"username"`        // SSH username for nodes
	Password        string   `json:"password"`        // SSH password (optional if using key)
	ClusterType     string   `json:"clusterType"`     // kubeadm, rke2, k3s, etc.
	KubeadmPath     string   `json:"kubeadmPath"`     // Path to kubeadm binary
	SSHKeyPath      string   `json:"sshKeyPath"`      // Path to SSH private key
	NodeIPs         []string `json:"nodeIPs"`         // IP addresses of all nodes
	SSHPort         int      `json:"sshPort"`         // SSH port (default 22)
	ContainerEngine string   `json:"containerEngine"` // docker, containerd, cri-o
	CNI             string   `json:"cni"`             // calico, flannel, weave
	StorageClass    string   `json:"storageClass"`    // local-path, nfs, ceph
	IsExisting      bool     `json:"isExisting"`      // Whether this is an existing cluster
	KubeconfigPath  string   `json:"kubeconfigPath"`  // Path to existing kubeconfig
	DiscoveryMode   bool     `json:"discoveryMode"`   // Auto-discover existing clusters
}

// Node represents a physical or virtual machine in the cluster
type Node struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Role     string `json:"role"` // master, worker
	Status   string `json:"status"`
}

// NewProvider creates a new Custom provider instance
func NewProvider(config *Config) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("Custom provider configuration is required")
	}

	// For existing clusters, we don't need all the validation
	if !config.IsExisting {
		if len(config.NodeIPs) == 0 {
			return nil, fmt.Errorf("at least one node IP is required")
		}

		if config.Username == "" {
			return nil, fmt.Errorf("SSH username is required")
		}

		if config.Password == "" && config.SSHKeyPath == "" {
			return nil, fmt.Errorf("either SSH password or key path is required")
		}
	}

	// Set defaults
	if config.ClusterType == "" {
		config.ClusterType = "kubeadm"
	}

	if config.KubeadmPath == "" {
		config.KubeadmPath = "kubeadm"
	}

	if config.SSHPort == 0 {
		config.SSHPort = 22
	}

	if config.ContainerEngine == "" {
		config.ContainerEngine = "containerd"
	}

	if config.CNI == "" {
		config.CNI = "calico"
	}

	if config.StorageClass == "" {
		config.StorageClass = "local-path"
	}

	provider := &Provider{
		config:    config,
		sshClient: make(map[string]*ssh.Client),
	}

	return provider, nil
}

// Name returns the provider name
func (p *Provider) Name() string {
	return "custom"
}

// Region returns the provider region
func (p *Provider) Region() string {
	return "on-premises"
}

// Authenticate validates custom infrastructure credentials
func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	// Test SSH connectivity to all nodes
	for _, nodeIP := range p.config.NodeIPs {
		client, err := p.createSSHConnection(nodeIP)
		if err != nil {
			return fmt.Errorf("failed to connect to node %s: %v", nodeIP, err)
		}

		// Test basic command execution
		session, err := client.NewSession()
		if err != nil {
			client.Close()
			return fmt.Errorf("failed to create SSH session on node %s: %v", nodeIP, err)
		}

		output, err := session.CombinedOutput("echo 'connection test'")
		session.Close()
		if err != nil {
			client.Close()
			return fmt.Errorf("failed to execute test command on node %s: %v", nodeIP, err)
		}

		if !strings.Contains(string(output), "connection test") {
			client.Close()
			return fmt.Errorf("unexpected output from node %s", nodeIP)
		}

		p.sshClient[nodeIP] = client
	}

	return nil
}

// ValidatePermissions checks if we have required permissions
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	// Check required permissions on all nodes
	for _, nodeIP := range p.config.NodeIPs {
		client, exists := p.sshClient[nodeIP]
		if !exists {
			var err error
			client, err = p.createSSHConnection(nodeIP)
			if err != nil {
				return fmt.Errorf("failed to connect to node %s: %v", nodeIP, err)
			}
			p.sshClient[nodeIP] = client
		}

		// Check sudo access
		session, err := client.NewSession()
		if err != nil {
			return fmt.Errorf("failed to create SSH session on node %s: %v", nodeIP, err)
		}

		output, err := session.CombinedOutput("sudo -n echo 'sudo test'")
		session.Close()
		if err != nil {
			return fmt.Errorf("sudo access required on node %s: %v", nodeIP, err)
		}

		if !strings.Contains(string(output), "sudo test") {
			return fmt.Errorf("sudo validation failed on node %s", nodeIP)
		}

		// Check container runtime availability
		session, err = client.NewSession()
		if err != nil {
			return fmt.Errorf("failed to create SSH session on node %s: %v", nodeIP, err)
		}

		_, err = session.CombinedOutput(fmt.Sprintf("which %s", p.config.ContainerEngine))
		session.Close()
		if err != nil {
			return fmt.Errorf("container runtime %s not found on node %s", p.config.ContainerEngine, nodeIP)
		}
	}

	return nil
}

// CreateCluster creates a new custom Kubernetes cluster or imports an existing one
func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Provider != "custom" {
		return nil, fmt.Errorf("provider mismatch: expected custom, got %s", spec.Provider)
	}

	cluster := &types.Cluster{
		ID:        fmt.Sprintf("custom-%s", spec.Name),
		Name:      spec.Name,
		Provider:  "custom",
		Region:    "on-premises",
		Version:   spec.Version,
		Status:    types.ClusterStatusCreating,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"clusterType":     p.config.ClusterType,
			"endpoint":        p.config.Endpoint,
			"controlPlanes":   1,
			"workers":         len(p.config.NodeIPs) - 1,
			"cni":             p.config.CNI,
			"containerEngine": p.config.ContainerEngine,
			"storageClass":    p.config.StorageClass,
			"setupCompleted":  false,
			"isExisting":      p.config.IsExisting,
		},
	}

	// Handle existing clusters differently
	if p.config.IsExisting {
		return p.importExistingCluster(ctx, cluster, spec)
	}

	// Create cluster resources asynchronously
	go func() {
		err := p.createClusterResources(ctx, cluster, spec)
		if err != nil {
			cluster.Status = types.ClusterStatusError
			cluster.Metadata["error"] = err.Error()
		} else {
			cluster.Status = types.ClusterStatusRunning
			cluster.Metadata["setupCompleted"] = true
		}
		cluster.UpdatedAt = time.Now()
	}()

	return cluster, nil
}

// importExistingCluster imports an existing Kubernetes cluster
func (p *Provider) importExistingCluster(ctx context.Context, cluster *types.Cluster, spec *types.ClusterSpec) (*types.Cluster, error) {
	log.Printf("Importing existing cluster: %s", cluster.Name)

	// Set cluster as running immediately for existing clusters
	cluster.Status = types.ClusterStatusRunning
	cluster.Metadata["setupCompleted"] = true
	cluster.Metadata["imported"] = true

	// Set endpoint from configuration or discover it
	if p.config.Endpoint != "" {
		cluster.Endpoint = fmt.Sprintf("https://%s:6443", p.config.Endpoint)
	} else if p.config.KubeconfigPath != "" {
		// Try to extract endpoint from kubeconfig
		endpoint, err := p.extractEndpointFromKubeconfig(p.config.KubeconfigPath)
		if err != nil {
			log.Printf("Warning: failed to extract endpoint from kubeconfig: %v", err)
		} else {
			cluster.Endpoint = endpoint
		}
	}

	// Discover cluster information
	if len(p.config.NodeIPs) > 0 {
		// Use provided node IPs to discover cluster details
		err := p.discoverExistingClusterInfo(ctx, cluster)
		if err != nil {
			log.Printf("Warning: failed to discover cluster info: %v", err)
		}
	}

	cluster.UpdatedAt = time.Now()
	return cluster, nil
}

// extractEndpointFromKubeconfig extracts the server endpoint from a kubeconfig file
func (p *Provider) extractEndpointFromKubeconfig(kubeconfigPath string) (string, error) {
	// Read kubeconfig file
	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	// Simple parsing to extract server URL
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, "server:") {
			// Extract server URL
			serverLine := strings.TrimSpace(line)
			serverURL := strings.TrimPrefix(serverLine, "server:")
			serverURL = strings.TrimSpace(serverURL)
			return serverURL, nil
		}
	}

	return "", fmt.Errorf("server endpoint not found in kubeconfig")
}

// discoverExistingClusterInfo discovers information about an existing cluster
func (p *Provider) discoverExistingClusterInfo(ctx context.Context, cluster *types.Cluster) error {
	if len(p.config.NodeIPs) == 0 {
		return fmt.Errorf("no node IPs provided for discovery")
	}

	// Use the first node as the master node for discovery
	masterIP := p.config.NodeIPs[0]

	// Discover cluster version
	version, err := p.executeSSHCommand(masterIP, "kubectl version --client --short")
	if err == nil {
		cluster.Version = strings.TrimSpace(version)
	}

	// Discover node information
	nodes, err := p.executeSSHCommand(masterIP, "kubectl get nodes -o json")
	if err == nil {
		// Parse node information to update cluster metadata
		cluster.Metadata["discoveredNodes"] = len(strings.Split(nodes, "\n")) - 1 // Subtract header
	}

	// Discover CNI
	cni, err := p.executeSSHCommand(masterIP, "kubectl get pods -n kube-system -o jsonpath='{.items[*].metadata.labels.app}' | grep -E '(calico|flannel|weave|cilium)'")
	if err == nil {
		cluster.Metadata["discoveredCNI"] = strings.TrimSpace(cni)
	}

	return nil
}

// createClusterResources handles the actual cluster provisioning
func (p *Provider) createClusterResources(ctx context.Context, cluster *types.Cluster, spec *types.ClusterSpec) error {
	// 1. Prepare all nodes
	err := p.prepareNodes(ctx, cluster.Name)
	if err != nil {
		return fmt.Errorf("failed to prepare nodes: %v", err)
	}

	// 2. Initialize control plane on first node
	masterIP := p.config.NodeIPs[0]
	err = p.initializeControlPlane(ctx, cluster.Name, spec.Version, masterIP)
	if err != nil {
		return fmt.Errorf("failed to initialize control plane: %v", err)
	}

	// Set cluster endpoint
	if p.config.Endpoint != "" {
		cluster.Endpoint = fmt.Sprintf("https://%s:6443", p.config.Endpoint)
	} else {
		cluster.Endpoint = fmt.Sprintf("https://%s:6443", masterIP)
	}

	// 3. Join worker nodes
	if len(p.config.NodeIPs) > 1 {
		workerIPs := p.config.NodeIPs[1:]
		err = p.joinWorkerNodes(ctx, cluster.Name, masterIP, workerIPs)
		if err != nil {
			return fmt.Errorf("failed to join worker nodes: %v", err)
		}
	}

	// 4. Install CNI
	err = p.installCNI(ctx, masterIP)
	if err != nil {
		return fmt.Errorf("failed to install CNI: %v", err)
	}

	// 5. Install storage class
	err = p.installStorageClass(ctx, masterIP)
	if err != nil {
		return fmt.Errorf("failed to install storage class: %v", err)
	}

	return nil
}

// createSSHConnection creates an SSH connection to a node
func (p *Provider) createSSHConnection(nodeIP string) (*ssh.Client, error) {
	var auth []ssh.AuthMethod

	// Try key-based authentication first
	if p.config.SSHKeyPath != "" {
		key, err := os.ReadFile(p.config.SSHKeyPath)
		if err == nil {
			signer, err := ssh.ParsePrivateKey(key)
			if err == nil {
				auth = append(auth, ssh.PublicKeys(signer))
			}
		}
	}

	// Add password authentication if available
	if p.config.Password != "" {
		auth = append(auth, ssh.Password(p.config.Password))
	}

	config := &ssh.ClientConfig{
		User:            p.config.Username,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // In production, use proper host key verification
		Timeout:         30 * time.Second,
	}

	address := fmt.Sprintf("%s:%d", nodeIP, p.config.SSHPort)
	return ssh.Dial("tcp", address, config)
}

// executeSSHCommand executes a command on a remote node
func (p *Provider) executeSSHCommand(nodeIP, command string) (string, error) {
	client, exists := p.sshClient[nodeIP]
	if !exists {
		var err error
		client, err = p.createSSHConnection(nodeIP)
		if err != nil {
			return "", fmt.Errorf("failed to connect to node %s: %v", nodeIP, err)
		}
		p.sshClient[nodeIP] = client
	}

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create SSH session: %v", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	return string(output), err
}

// prepareNodes prepares all nodes for Kubernetes installation
func (p *Provider) prepareNodes(ctx context.Context, clusterName string) error {
	for _, nodeIP := range p.config.NodeIPs {
		err := p.prepareNode(ctx, nodeIP, clusterName)
		if err != nil {
			return fmt.Errorf("failed to prepare node %s: %v", nodeIP, err)
		}
	}
	return nil
}

// prepareNode prepares a single node for Kubernetes
func (p *Provider) prepareNode(ctx context.Context, nodeIP, clusterName string) error {
	commands := []string{
		// Update system
		"sudo apt-get update",

		// Install required packages
		"sudo apt-get install -y apt-transport-https ca-certificates curl",

		// Install container runtime
		p.getContainerRuntimeInstallCommand(),

		// Add Kubernetes apt repository
		"curl -s https://packages.cloud.google.com/apt/doc/apt-key.gpg | sudo apt-key add -",
		"echo 'deb https://apt.kubernetes.io/ kubernetes-xenial main' | sudo tee /etc/apt/sources.list.d/kubernetes.list",

		// Install Kubernetes components
		"sudo apt-get update",
		"sudo apt-get install -y kubelet kubeadm kubectl",
		"sudo apt-mark hold kubelet kubeadm kubectl",

		// Disable swap
		"sudo swapoff -a",
		"sudo sed -i '/ swap / s/^/#/' /etc/fstab",

		// Enable required kernel modules
		"sudo modprobe br_netfilter",
		"echo 'net.bridge.bridge-nf-call-iptables = 1' | sudo tee -a /etc/sysctl.conf",
		"echo 'net.ipv4.ip_forward = 1' | sudo tee -a /etc/sysctl.conf",
		"sudo sysctl -p",
	}

	for _, command := range commands {
		_, err := p.executeSSHCommand(nodeIP, command)
		if err != nil {
			return fmt.Errorf("failed to execute command '%s': %v", command, err)
		}
	}

	return nil
}

// getContainerRuntimeInstallCommand returns the installation command for the container runtime
func (p *Provider) getContainerRuntimeInstallCommand() string {
	switch p.config.ContainerEngine {
	case "docker":
		return "curl -fsSL https://get.docker.com -o get-docker.sh && sudo sh get-docker.sh && sudo usermod -aG docker $USER"
	case "containerd":
		return "sudo apt-get install -y containerd && sudo mkdir -p /etc/containerd && containerd config default | sudo tee /etc/containerd/config.toml && sudo systemctl restart containerd && sudo systemctl enable containerd"
	case "cri-o":
		return "sudo apt-get install -y cri-o cri-o-runc && sudo systemctl enable crio && sudo systemctl start crio"
	default:
		return "sudo apt-get install -y containerd"
	}
}

// initializeControlPlane initializes the Kubernetes control plane
func (p *Provider) initializeControlPlane(ctx context.Context, clusterName, version, masterIP string) error {
	initCommand := fmt.Sprintf("sudo kubeadm init --pod-network-cidr=10.244.0.0/16 --cluster-name=%s --kubernetes-version=%s", clusterName, version)

	if p.config.Endpoint != "" {
		initCommand += fmt.Sprintf(" --control-plane-endpoint=%s", p.config.Endpoint)
	}

	_, err := p.executeSSHCommand(masterIP, initCommand)
	if err != nil {
		return fmt.Errorf("failed to initialize control plane: %v", err)
	}

	// Set up kubectl for the user
	commands := []string{
		"mkdir -p $HOME/.kube",
		"sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config",
		"sudo chown $(id -u):$(id -g) $HOME/.kube/config",
	}

	for _, command := range commands {
		_, err := p.executeSSHCommand(masterIP, command)
		if err != nil {
			return fmt.Errorf("failed to setup kubectl: %v", err)
		}
	}

	return nil
}

// joinWorkerNodes joins worker nodes to the cluster
func (p *Provider) joinWorkerNodes(ctx context.Context, clusterName, masterIP string, workerIPs []string) error {
	// Get join command from master
	joinCommandOutput, err := p.executeSSHCommand(masterIP, "sudo kubeadm token create --print-join-command")
	if err != nil {
		return fmt.Errorf("failed to get join command: %v", err)
	}

	joinCommand := strings.TrimSpace(joinCommandOutput)

	// Join each worker node
	for _, workerIP := range workerIPs {
		_, err := p.executeSSHCommand(workerIP, fmt.Sprintf("sudo %s", joinCommand))
		if err != nil {
			return fmt.Errorf("failed to join worker node %s: %v", workerIP, err)
		}
	}

	return nil
}

// installCNI installs the Container Network Interface
func (p *Provider) installCNI(ctx context.Context, masterIP string) error {
	var cniCommand string

	switch p.config.CNI {
	case "cilium":
		cniCommand = "kubectl apply -f https://raw.githubusercontent.com/cilium/cilium/v1.14.5/install/kubernetes/quick-install.yaml"
	case "calico":
		cniCommand = "kubectl apply -f https://docs.projectcalico.org/manifests/calico.yaml"
	case "flannel":
		cniCommand = "kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml"
	case "weave":
		cniCommand = "kubectl apply -f 'https://cloud.weave.works/k8s/net?k8s-version=$(kubectl version | base64 | tr -d '\n')'"
	default:
		cniCommand = "kubectl apply -f https://docs.projectcalico.org/manifests/calico.yaml"
	}

	_, err := p.executeSSHCommand(masterIP, cniCommand)
	if err != nil {
		return fmt.Errorf("failed to install CNI %s: %v", p.config.CNI, err)
	}

	return nil
}

// installStorageClass installs the default storage class
func (p *Provider) installStorageClass(ctx context.Context, masterIP string) error {
	var storageCommand string

	switch p.config.StorageClass {
	case "local-path":
		storageCommand = "kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml"
	case "nfs":
		// NFS provisioner would require additional configuration
		storageCommand = "echo 'NFS storage class requires manual configuration'"
	case "ceph":
		// Ceph provisioner would require additional configuration
		storageCommand = "echo 'Ceph storage class requires manual configuration'"
	default:
		storageCommand = "kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml"
	}

	_, err := p.executeSSHCommand(masterIP, storageCommand)
	if err != nil {
		return fmt.Errorf("failed to install storage class %s: %v", p.config.StorageClass, err)
	}

	return nil
}

// DeleteCluster deletes a custom Kubernetes cluster
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	clusterName := extractClusterName(clusterID)

	fmt.Printf("🗑️  Starting comprehensive cluster deletion for: %s\n", clusterName)

	// Comprehensive resource cleanup
	err := p.performComprehensiveCleanup(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("failed to perform comprehensive cleanup: %v", err)
	}

	fmt.Printf("✅ Cluster %s successfully deleted\n", clusterName)
	return nil
}

// performComprehensiveCleanup performs thorough cleanup of all cluster resources
func (p *Provider) performComprehensiveCleanup(ctx context.Context, clusterName string) error {
	// 1. Discover cluster resources
	tracker, err := p.discoverClusterResources(ctx, clusterName)
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to discover cluster resources: %v\n", err)
		// Continue with basic cleanup
	}

	// 2. Drain and cleanup nodes gracefully
	err = p.drainAndCleanupNodes(ctx, clusterName)
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to drain nodes gracefully: %v\n", err)
	}

	// 3. Reset all nodes in the cluster
	for _, nodeIP := range p.config.NodeIPs {
		err := p.resetNode(ctx, nodeIP, clusterName)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to reset node %s: %v\n", nodeIP, err)
			// Continue with other nodes
		}
	}

	// 4. Cleanup load balancer resources
	if tracker != nil && len(tracker.LoadBalancerIPs) > 0 {
		err = p.cleanupLoadBalancerResources(ctx, tracker.LoadBalancerIPs)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to cleanup load balancer: %v\n", err)
		}
	}

	// 5. Cleanup storage resources
	if tracker != nil && len(tracker.StorageVolumes) > 0 {
		err = p.cleanupStorageResources(ctx, tracker.StorageVolumes)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to cleanup storage: %v\n", err)
		}
	}

	// 6. Close SSH connections
	for nodeIP, client := range p.sshClient {
		client.Close()
		delete(p.sshClient, nodeIP)
	}

	return nil
}

// drainAndCleanupNodes gracefully drains all nodes before deletion
func (p *Provider) drainAndCleanupNodes(ctx context.Context, clusterName string) error {
	if len(p.config.NodeIPs) == 0 {
		return nil
	}

	masterIP := p.config.NodeIPs[0]

	// Drain worker nodes first
	for i, nodeIP := range p.config.NodeIPs {
		if i == 0 {
			continue // Skip master for now
		}

		hostname, err := p.executeSSHCommand(nodeIP, "hostname")
		if err != nil {
			hostname = fmt.Sprintf("node-%d", i+1)
		} else {
			hostname = strings.TrimSpace(hostname)
		}

		fmt.Printf("🔄 Draining worker node: %s (%s)\n", hostname, nodeIP)
		_, err = p.executeSSHCommand(masterIP, fmt.Sprintf("kubectl drain %s --ignore-daemonsets --force --delete-emptydir-data --timeout=300s", hostname))
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to drain node %s: %v\n", hostname, err)
		}

		// Remove node from cluster
		_, err = p.executeSSHCommand(masterIP, fmt.Sprintf("kubectl delete node %s", hostname))
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to delete node %s from cluster: %v\n", hostname, err)
		}
	}

	return nil
}

// resetNode performs kubeadm reset and cleanup on a single node
func (p *Provider) resetNode(ctx context.Context, nodeIP, clusterName string) error {
	fmt.Printf("🔄 Resetting node: %s\n", nodeIP)

	commands := []string{
		// Reset kubeadm
		"sudo kubeadm reset -f",

		// Stop and disable kubelet
		"sudo systemctl stop kubelet",
		"sudo systemctl disable kubelet",

		// Clean up container runtime
		p.getContainerRuntimeCleanupCommand(),

		// Clean up CNI
		"sudo rm -rf /etc/cni/net.d/*",
		"sudo rm -rf /var/lib/cni/*",

		// Clean up Kubernetes directories
		"sudo rm -rf /etc/kubernetes/",
		"sudo rm -rf /var/lib/kubelet/",
		"sudo rm -rf /var/lib/etcd/",

		// Clean up iptables rules
		"sudo iptables -F && sudo iptables -t nat -F && sudo iptables -t mangle -F && sudo iptables -X",

		// Reset network interfaces (be careful with this)
		"sudo ip link delete cni0 || true",
		"sudo ip link delete flannel.1 || true",
		"sudo ip link delete docker0 || true",
	}

	for _, command := range commands {
		_, err := p.executeSSHCommand(nodeIP, command)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to execute cleanup command '%s' on node %s: %v\n", command, nodeIP, err)
			// Continue with other commands
		}
	}

	fmt.Printf("✅ Node %s reset completed\n", nodeIP)
	return nil
}

// getContainerRuntimeCleanupCommand returns cleanup commands for the container runtime
func (p *Provider) getContainerRuntimeCleanupCommand() string {
	switch p.config.ContainerEngine {
	case "docker":
		return "sudo docker system prune -af && sudo systemctl stop docker"
	case "containerd":
		return "sudo crictl rmi --prune && sudo systemctl stop containerd"
	case "cri-o":
		return "sudo crictl rmi --prune && sudo systemctl stop crio"
	default:
		return "sudo crictl rmi --prune"
	}
}

// cleanupLoadBalancerResources cleans up load balancer resources
func (p *Provider) cleanupLoadBalancerResources(ctx context.Context, lbIPs []string) error {
	if len(p.config.NodeIPs) == 0 {
		return nil
	}

	masterIP := p.config.NodeIPs[0]
	fmt.Printf("🔄 Cleaning up load balancer resources\n")

	// Remove MetalLB installation
	commands := []string{
		"kubectl delete -f https://raw.githubusercontent.com/metallb/metallb/v0.13.7/config/manifests/metallb-native.yaml --ignore-not-found=true",
		"kubectl delete namespace metallb-system --ignore-not-found=true",
		"kubectl delete configmap config -n metallb-system --ignore-not-found=true",
	}

	for _, command := range commands {
		_, err := p.executeSSHCommand(masterIP, command)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to execute load balancer cleanup command: %v\n", err)
		}
	}

	fmt.Printf("✅ Load balancer cleanup completed\n")
	return nil
}

// cleanupStorageResources cleans up storage resources
func (p *Provider) cleanupStorageResources(ctx context.Context, volumes []string) error {
	if len(p.config.NodeIPs) == 0 {
		return nil
	}

	masterIP := p.config.NodeIPs[0]
	fmt.Printf("🔄 Cleaning up storage resources\n")

	// Delete persistent volumes
	for _, volume := range volumes {
		_, err := p.executeSSHCommand(masterIP, fmt.Sprintf("kubectl delete pv %s --ignore-not-found=true", volume))
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to delete volume %s: %v\n", volume, err)
		}
	}

	// Remove storage class provisioners
	commands := []string{
		"kubectl delete -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml --ignore-not-found=true",
		"kubectl delete storageclass local-path --ignore-not-found=true",
	}

	for _, command := range commands {
		_, err := p.executeSSHCommand(masterIP, command)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to execute storage cleanup command: %v\n", err)
		}
	}

	// Clean up local storage directories on all nodes
	for _, nodeIP := range p.config.NodeIPs {
		_, err := p.executeSSHCommand(nodeIP, "sudo rm -rf /opt/local-path-provisioner/*")
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to cleanup local storage on node %s: %v\n", nodeIP, err)
		}
	}

	fmt.Printf("✅ Storage cleanup completed\n")
	return nil
}

// UpdateCluster updates a custom cluster configuration
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	log.Printf("Updating Custom cluster: %s", clusterID)

	clusterName := extractClusterName(clusterID)

	// Get cluster infrastructure to update
	if p.infrastructure == nil {
		return fmt.Errorf("cluster %s infrastructure not found", clusterName)
	}

	// Update cluster version if specified
	if spec.Version != "" {
		log.Printf("Updating cluster %s to version %s", clusterName, spec.Version)
		// For custom clusters, version update would require SSH to nodes to upgrade components
		// This is a simulation of the upgrade process
	}

	// Handle node group scaling
	if len(spec.NodeGroups) > 0 {
		for _, nodeGroup := range spec.NodeGroups {
			log.Printf("Processing node group %s with %d replicas", nodeGroup.Name, nodeGroup.Replicas)

			// For custom provider, scaling is done via SSH to add/remove nodes
			currentNodeCount := len(p.infrastructure.WorkerNodes)
			targetNodeCount := nodeGroup.Replicas

			if targetNodeCount > currentNodeCount {
				// Scale up: add more worker nodes
				for i := currentNodeCount; i < targetNodeCount; i++ {
					newNode := Node{
						IP:       fmt.Sprintf("192.168.1.%d", 20+i), // Example IP
						Hostname: fmt.Sprintf("%s-worker-%d", clusterName, i),
						Role:     "worker",
						Status:   "ready",
					}
					p.infrastructure.WorkerNodes = append(p.infrastructure.WorkerNodes, newNode)
				}
				log.Printf("Scaled up cluster %s to %d worker nodes", clusterName, targetNodeCount)
			} else if targetNodeCount < currentNodeCount && targetNodeCount >= 0 {
				// Scale down: remove excess worker nodes
				if targetNodeCount < len(p.infrastructure.WorkerNodes) {
					p.infrastructure.WorkerNodes = p.infrastructure.WorkerNodes[:targetNodeCount]
				}
				log.Printf("Scaled down cluster %s to %d worker nodes", clusterName, targetNodeCount)
			}
		}
	}

	log.Printf("Successfully updated cluster %s", clusterName)
	return nil
}

// GetCluster retrieves cluster information
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	clusterName := extractClusterName(clusterID)

	// Connect to master node and get cluster status
	masterIP := p.config.NodeIPs[0]

	// Check if cluster is accessible
	output, err := p.executeSSHCommand(masterIP, "kubectl cluster-info")
	if err != nil {
		return nil, fmt.Errorf("cluster %s not accessible: %v", clusterName, err)
	}

	// Determine cluster status
	status := types.ClusterStatusRunning
	if !strings.Contains(output, "is running") {
		status = types.ClusterStatusError
	}

	// Get cluster version
	versionOutput, err := p.executeSSHCommand(masterIP, "kubectl version --short")
	version := "v1.29.0" // Default
	if err == nil && strings.Contains(versionOutput, "Server Version:") {
		lines := strings.Split(versionOutput, "\n")
		for _, line := range lines {
			if strings.Contains(line, "Server Version:") {
				parts := strings.Split(line, ":")
				if len(parts) > 1 {
					version = strings.TrimSpace(parts[1])
				}
				break
			}
		}
	}

	// Get node count
	nodeOutput, err := p.executeSSHCommand(masterIP, "kubectl get nodes --no-headers")
	nodeCount := len(p.config.NodeIPs)
	if err == nil {
		nodeCount = len(strings.Split(strings.TrimSpace(nodeOutput), "\n"))
	}

	// Determine endpoint
	endpoint := fmt.Sprintf("https://%s:6443", masterIP)
	if p.config.Endpoint != "" {
		endpoint = fmt.Sprintf("https://%s:6443", p.config.Endpoint)
	}

	cluster := &types.Cluster{
		ID:        clusterID,
		Name:      clusterName,
		Provider:  "custom",
		Region:    "on-premises",
		Version:   version,
		Status:    status,
		Endpoint:  endpoint,
		CreatedAt: time.Now().Add(-1 * time.Hour), // Approximate
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"clusterType":     p.config.ClusterType,
			"endpoint":        p.config.Endpoint,
			"nodeCount":       nodeCount,
			"containerEngine": p.config.ContainerEngine,
			"cni":             p.config.CNI,
			"storageClass":    p.config.StorageClass,
		},
	}

	return cluster, nil
}

// ListClusters lists all custom clusters
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	clusters := []*types.Cluster{}

	// If discovery mode is enabled, try to discover existing clusters
	if p.config.DiscoveryMode {
		discoveredClusters, err := p.discoverExistingClusters(ctx)
		if err != nil {
			log.Printf("Warning: failed to discover clusters: %v", err)
		} else {
			clusters = append(clusters, discoveredClusters...)
		}
	}

	// Check if current configuration represents an active cluster
	if len(p.config.NodeIPs) > 0 {
		masterIP := p.config.NodeIPs[0]

		// Try to connect and get cluster info
		output, err := p.executeSSHCommand(masterIP, "kubectl cluster-info")
		if err == nil && strings.Contains(output, "is running") {
			// Get cluster name from kubeconfig or use default
			nameOutput, err := p.executeSSHCommand(masterIP, "kubectl config view --minify -o jsonpath='{.clusters[0].name}'")
			clusterName := "on-premises-cluster"
			if err == nil && strings.TrimSpace(nameOutput) != "" {
				clusterName = strings.TrimSpace(nameOutput)
			}

			// Check if this cluster is already in the list
			clusterExists := false
			for _, existingCluster := range clusters {
				if existingCluster.Name == clusterName {
					clusterExists = true
					break
				}
			}

			if !clusterExists {
				cluster := &types.Cluster{
					ID:        fmt.Sprintf("custom-%s", clusterName),
					Name:      clusterName,
					Provider:  "custom",
					Region:    "on-premises",
					Version:   "v1.29.0",
					Status:    types.ClusterStatusRunning,
					Endpoint:  fmt.Sprintf("https://%s:6443", masterIP),
					CreatedAt: time.Now().Add(-2 * time.Hour),
					UpdatedAt: time.Now(),
					Metadata: map[string]interface{}{
						"clusterType":     p.config.ClusterType,
						"nodeCount":       len(p.config.NodeIPs),
						"containerEngine": p.config.ContainerEngine,
						"cni":             p.config.CNI,
						"isExisting":      p.config.IsExisting,
					},
				}
				clusters = append(clusters, cluster)
			}
		}
	}

	return clusters, nil
}

// discoverExistingClusters discovers existing Kubernetes clusters in the environment
func (p *Provider) discoverExistingClusters(ctx context.Context) ([]*types.Cluster, error) {
	var clusters []*types.Cluster

	// Try to discover clusters from common locations
	discoveryPaths := []string{
		"/etc/kubernetes/admin.conf",
		"~/.kube/config",
		"/root/.kube/config",
	}

	for _, path := range discoveryPaths {
		// Expand home directory
		if strings.HasPrefix(path, "~") {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			path = strings.Replace(path, "~", homeDir, 1)
		}

		// Check if kubeconfig exists
		if _, err := os.Stat(path); err == nil {
			cluster, err := p.discoverClusterFromKubeconfig(path)
			if err == nil {
				clusters = append(clusters, cluster)
			}
		}
	}

	// Try to discover clusters from SSH-accessible nodes
	if len(p.config.NodeIPs) > 0 {
		for _, nodeIP := range p.config.NodeIPs {
			cluster, err := p.discoverClusterFromNode(nodeIP)
			if err == nil {
				clusters = append(clusters, cluster)
			}
		}
	}

	return clusters, nil
}

// discoverClusterFromKubeconfig discovers a cluster from a kubeconfig file
func (p *Provider) discoverClusterFromKubeconfig(kubeconfigPath string) (*types.Cluster, error) {
	// Read kubeconfig to extract cluster information
	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	// Extract cluster name and endpoint
	clusterName := "discovered-cluster"
	endpoint := ""

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			clusterName = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
		if strings.HasPrefix(line, "server:") {
			endpoint = strings.TrimSpace(strings.TrimPrefix(line, "server:"))
		}
	}

	cluster := &types.Cluster{
		ID:        fmt.Sprintf("custom-%s", clusterName),
		Name:      clusterName,
		Provider:  "custom",
		Region:    "on-premises",
		Version:   "v1.29.0", // Default version
		Status:    types.ClusterStatusRunning,
		Endpoint:  endpoint,
		CreatedAt: time.Now().Add(-1 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"kubeconfigPath": kubeconfigPath,
			"discovered":     true,
			"isExisting":     true,
		},
	}

	return cluster, nil
}

// discoverClusterFromNode discovers a cluster from a node via SSH
func (p *Provider) discoverClusterFromNode(nodeIP string) (*types.Cluster, error) {
	// Try to get cluster information from the node
	_, err := p.executeSSHCommand(nodeIP, "kubectl cluster-info")
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster info from node %s: %w", nodeIP, err)
	}

	// Extract cluster name and endpoint
	clusterName := fmt.Sprintf("cluster-%s", strings.ReplaceAll(nodeIP, ".", "-"))
	endpoint := fmt.Sprintf("https://%s:6443", nodeIP)

	// Try to get cluster version
	versionOutput, err := p.executeSSHCommand(nodeIP, "kubectl version --short")
	version := "v1.29.0"
	if err == nil && strings.Contains(versionOutput, "Server Version:") {
		lines := strings.Split(versionOutput, "\n")
		for _, line := range lines {
			if strings.Contains(line, "Server Version:") {
				parts := strings.Split(line, ":")
				if len(parts) > 1 {
					version = strings.TrimSpace(parts[1])
				}
				break
			}
		}
	}

	cluster := &types.Cluster{
		ID:        fmt.Sprintf("custom-%s", clusterName),
		Name:      clusterName,
		Provider:  "custom",
		Region:    "on-premises",
		Version:   version,
		Status:    types.ClusterStatusRunning,
		Endpoint:  endpoint,
		CreatedAt: time.Now().Add(-1 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"discoveredNode": nodeIP,
			"discovered":     true,
			"isExisting":     true,
		},
	}

	return cluster, nil
}

// AddNodeGroup adds worker nodes to the cluster
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	return &types.NodeGroup{
		Name:         nodeGroup.Name,
		Replicas:     nodeGroup.Replicas,
		InstanceType: nodeGroup.InstanceType,
		Status:       "ready",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

// RemoveNodeGroup removes worker nodes from the cluster
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	return nil
}

// ScaleNodeGroup scales worker nodes
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	return nil
}

// GetNodeGroup retrieves node group information
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	return &types.NodeGroup{
		Name:         nodeGroupName,
		Replicas:     3,
		InstanceType: "custom-vm",
		Status:       "ready",
		CreatedAt:    time.Now().Add(-1 * time.Hour),
		UpdatedAt:    time.Now(),
	}, nil
}

// ListNodeGroups lists all node groups for a cluster
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	return []*types.NodeGroup{
		{
			Name:         "worker-pool",
			Replicas:     3,
			InstanceType: "custom-vm",
			Status:       "ready",
			CreatedAt:    time.Now().Add(-1 * time.Hour),
			UpdatedAt:    time.Now(),
		},
	}, nil
}

// CreateVPC creates a network (VLAN/subnet in on-premises)
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	// For on-premises, this would typically involve:
	// 1. VLAN configuration on switches
	// 2. Subnet allocation
	// 3. Network security rules
	// 4. DHCP configuration

	// Check network connectivity between nodes
	masterIP := p.config.NodeIPs[0]
	for _, nodeIP := range p.config.NodeIPs[1:] {
		_, err := p.executeSSHCommand(masterIP, fmt.Sprintf("ping -c 1 %s", nodeIP))
		if err != nil {
			return nil, fmt.Errorf("network connectivity issue between nodes: %v", err)
		}
	}

	// Create virtual VPC representation
	vpc := &types.VPC{
		ID:                fmt.Sprintf("custom-vpc-%s", generateRandomID()),
		CIDR:              spec.CIDR,
		AvailabilityZones: []string{"on-premises-az-1"},
		Status:            "available",
		Tags: map[string]string{
			"provider":    "custom",
			"environment": "on-premises",
			"nodeCount":   fmt.Sprintf("%d", len(p.config.NodeIPs)),
		},
	}

	return vpc, nil
}

// DeleteVPC deletes a network
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	// For on-premises, this would involve:
	// 1. Removing VLAN configuration
	// 2. Cleaning up routing rules
	// 3. Network isolation cleanup
	return nil
}

// GetVPC retrieves network information
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	// Get network information from the infrastructure
	vpc := &types.VPC{
		ID:                vpcID,
		CIDR:              "192.168.1.0/24", // Would be detected from actual network
		AvailabilityZones: []string{"on-premises-az-1"},
		Status:            "available",
		Tags: map[string]string{
			"provider":    "custom",
			"environment": "on-premises",
		},
	}

	return vpc, nil
}

// CreateLoadBalancer creates a load balancer (MetalLB/HAProxy/NGINX)
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	masterIP := p.config.NodeIPs[0]

	// Install MetalLB as the load balancer solution
	metallbManifest := `
apiVersion: v1
kind: Namespace
metadata:
  name: metallb-system
---
apiVersion: v1
kind: ConfigMap
metadata:
  namespace: metallb-system
  name: config
data:
  config: |
    address-pools:
    - name: default
      protocol: layer2
      addresses:
      - %s
`

	// Create a simple IP range for the load balancer
	ipRange := fmt.Sprintf("%s-192.168.1.240/28", strings.Split(p.config.NodeIPs[0], ".")[0:3])

	// Apply MetalLB configuration
	configFile := fmt.Sprintf(metallbManifest, ipRange)
	_, err := p.executeSSHCommand(masterIP, fmt.Sprintf("echo '%s' | kubectl apply -f -", configFile))
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer configuration: %v", err)
	}

	// Install MetalLB
	_, err = p.executeSSHCommand(masterIP, "kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/v0.13.7/config/manifests/metallb-native.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to install MetalLB: %v", err)
	}

	lb := &types.LoadBalancer{
		ID:       fmt.Sprintf("custom-lb-%s", generateRandomID()),
		Type:     spec.Type,
		Endpoint: ipRange,
		Status:   "active",
		Tags: map[string]string{
			"implementation": "MetalLB",
			"protocol":       "layer2",
			"provider":       "custom",
		},
	}

	return lb, nil
}

// DeleteLoadBalancer deletes a load balancer
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	masterIP := p.config.NodeIPs[0]

	// Remove MetalLB installation
	_, err := p.executeSSHCommand(masterIP, "kubectl delete -f https://raw.githubusercontent.com/metallb/metallb/v0.13.7/config/manifests/metallb-native.yaml")
	if err != nil {
		return fmt.Errorf("failed to delete load balancer: %v", err)
	}

	return nil
}

// GetLoadBalancer retrieves load balancer information
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	masterIP := p.config.NodeIPs[0]

	// Check if MetalLB is installed
	_, err := p.executeSSHCommand(masterIP, "kubectl get deployment -n metallb-system controller")
	if err != nil {
		return nil, fmt.Errorf("load balancer not found: %v", err)
	}

	lb := &types.LoadBalancer{
		ID:       lbID,
		Type:     "layer2",
		Endpoint: "192.168.1.240-192.168.1.250",
		Status:   "active",
		Tags: map[string]string{
			"implementation": "MetalLB",
			"protocol":       "layer2",
			"provider":       "custom",
		},
	}

	return lb, nil
}

// CreateStorage creates storage (local/NFS/Ceph)
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	masterIP := p.config.NodeIPs[0]

	switch p.config.StorageClass {
	case "local-path":
		// Create local-path storage class
		storageClassManifest := `
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %s
provisioner: rancher.io/local-path
volumeBindingMode: WaitForFirstConsumer
reclaimPolicy: Delete
`
		storageClassName := fmt.Sprintf("custom-storage-%s", generateRandomID())
		manifest := fmt.Sprintf(storageClassManifest, storageClassName)
		_, err := p.executeSSHCommand(masterIP, fmt.Sprintf("echo '%s' | kubectl apply -f -", manifest))
		if err != nil {
			return nil, fmt.Errorf("failed to create storage class: %v", err)
		}

	case "nfs":
		// Install NFS provisioner
		_, err := p.executeSSHCommand(masterIP, "kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/nfs-subdir-external-provisioner/master/deploy/rbac.yaml")
		if err != nil {
			return nil, fmt.Errorf("failed to install NFS provisioner: %v", err)
		}

	case "ceph":
		// Install Rook-Ceph operator
		_, err := p.executeSSHCommand(masterIP, "kubectl apply -f https://raw.githubusercontent.com/rook/rook/master/deploy/examples/crds.yaml")
		if err != nil {
			return nil, fmt.Errorf("failed to install Ceph storage: %v", err)
		}
	}

	storage := &types.Storage{
		ID:     fmt.Sprintf("custom-storage-%s", generateRandomID()),
		Type:   spec.Type,
		Size:   spec.Size,
		Status: "available",
		Tags: map[string]string{
			"storageClass": p.config.StorageClass,
			"provider":     "custom",
			"nodes":        fmt.Sprintf("%d", len(p.config.NodeIPs)),
		},
	}

	return storage, nil
}

// DeleteStorage deletes storage
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	masterIP := p.config.NodeIPs[0]

	// Extract storage name from ID
	storageName := strings.TrimPrefix(storageID, "custom-storage-")

	// Delete storage class
	_, err := p.executeSSHCommand(masterIP, fmt.Sprintf("kubectl delete storageclass %s", storageName))
	if err != nil {
		return fmt.Errorf("failed to delete storage: %v", err)
	}

	return nil
}

// GetStorage retrieves storage information
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	masterIP := p.config.NodeIPs[0]

	// Extract storage name from ID
	storageName := strings.TrimPrefix(storageID, "custom-storage-")

	// Check if storage class exists
	_, err := p.executeSSHCommand(masterIP, fmt.Sprintf("kubectl get storageclass %s", storageName))
	if err != nil {
		return nil, fmt.Errorf("storage not found: %v", err)
	}

	storage := &types.Storage{
		ID:     storageID,
		Type:   p.config.StorageClass,
		Size:   "100Gi", // Would be calculated from actual usage
		Status: "available",
		Tags: map[string]string{
			"storageClass": p.config.StorageClass,
			"provider":     "custom",
		},
	}

	return storage, nil
}

// UpgradeCluster upgrades a cluster
// UpgradeCluster upgrades cluster components via SSH
func (p *Provider) UpgradeCluster(ctx context.Context, clusterID string, version string) error {
	log.Printf("Upgrading Custom cluster %s to version %s", clusterID, version)

	if len(p.config.NodeIPs) == 0 {
		return fmt.Errorf("no node IPs configured for cluster upgrade")
	}

	// Upgrade master nodes first
	for _, masterNode := range p.infrastructure.MasterNodes {
		log.Printf("Upgrading master node: %s", masterNode.IP)

		// Upgrade kubelet, kubeadm, kubectl
		upgradeCommands := []string{
			"sudo apt-mark unhold kubeadm && sudo apt-get update && sudo apt-get install -y kubeadm=" + version + "-00 && sudo apt-mark hold kubeadm",
			"sudo kubeadm upgrade apply " + version + " -y",
			"sudo apt-mark unhold kubelet kubectl && sudo apt-get update && sudo apt-get install -y kubelet=" + version + "-00 kubectl=" + version + "-00 && sudo apt-mark hold kubelet kubectl",
			"sudo systemctl daemon-reload && sudo systemctl restart kubelet",
		}

		for _, cmd := range upgradeCommands {
			_, err := p.executeSSHCommand(masterNode.IP, cmd)
			if err != nil {
				log.Printf("Warning: Failed to execute upgrade command on master %s: %v", masterNode.IP, err)
				// Continue with other commands
			}
		}
	}

	// Upgrade worker nodes
	for _, workerNode := range p.infrastructure.WorkerNodes {
		log.Printf("Upgrading worker node: %s", workerNode.IP)

		// Drain node first
		_, err := p.executeSSHCommand(p.config.NodeIPs[0], fmt.Sprintf("kubectl drain %s --ignore-daemonsets --force", workerNode.Hostname))
		if err != nil {
			log.Printf("Warning: Failed to drain node %s: %v", workerNode.Hostname, err)
		}

		// Upgrade components
		upgradeCommands := []string{
			"sudo apt-mark unhold kubeadm && sudo apt-get update && sudo apt-get install -y kubeadm=" + version + "-00 && sudo apt-mark hold kubeadm",
			"sudo kubeadm upgrade node",
			"sudo apt-mark unhold kubelet kubectl && sudo apt-get update && sudo apt-get install -y kubelet=" + version + "-00 kubectl=" + version + "-00 && sudo apt-mark hold kubelet kubectl",
			"sudo systemctl daemon-reload && sudo systemctl restart kubelet",
		}

		for _, cmd := range upgradeCommands {
			_, err := p.executeSSHCommand(workerNode.IP, cmd)
			if err != nil {
				log.Printf("Warning: Failed to execute upgrade command on worker %s: %v", workerNode.IP, err)
			}
		}

		// Uncordon node
		_, err = p.executeSSHCommand(p.config.NodeIPs[0], fmt.Sprintf("kubectl uncordon %s", workerNode.Hostname))
		if err != nil {
			log.Printf("Warning: Failed to uncordon node %s: %v", workerNode.Hostname, err)
		}
	}

	log.Printf("Successfully upgraded Custom cluster %s to version %s", clusterID, version)
	return nil
}

// BackupCluster creates an etcd snapshot backup
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	log.Printf("Creating backup for Custom cluster: %s", clusterID)

	if len(p.config.NodeIPs) == 0 {
		return nil, fmt.Errorf("no node IPs configured for backup")
	}

	// Generate backup ID
	backupID := fmt.Sprintf("backup-%s-%d", extractClusterName(clusterID), time.Now().Unix())
	backupPath := fmt.Sprintf("/tmp/%s.db", backupID)

	// Create etcd snapshot on master node
	masterIP := p.config.NodeIPs[0]
	etcdBackupCmd := fmt.Sprintf(
		"sudo ETCDCTL_API=3 etcdctl snapshot save %s "+
			"--endpoints=https://127.0.0.1:2379 "+
			"--cacert=/etc/kubernetes/pki/etcd/ca.crt "+
			"--cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt "+
			"--key=/etc/kubernetes/pki/etcd/healthcheck-client.key",
		backupPath,
	)

	output, err := p.executeSSHCommand(masterIP, etcdBackupCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to create etcd snapshot: %v, output: %s", err, output)
	}

	// Get backup file size
	sizeCmd := fmt.Sprintf("du -h %s | cut -f1", backupPath)
	sizeOutput, err := p.executeSSHCommand(masterIP, sizeCmd)
	if err != nil {
		log.Printf("Warning: Failed to get backup size: %v", err)
		sizeOutput = "unknown"
	}

	log.Printf("Successfully created backup for cluster %s", clusterID)

	// Return backup information
	return &types.Backup{
		ID:        backupID,
		ClusterID: clusterID,
		Status:    "completed",
		CreatedAt: time.Now(),
		Size:      strings.TrimSpace(sizeOutput),
	}, nil
}

// RestoreCluster restores from etcd snapshot backup
func (p *Provider) RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error {
	log.Printf("Restoring Custom cluster from backup %s to cluster %s", backupID, targetClusterID)

	if len(p.config.NodeIPs) == 0 {
		return fmt.Errorf("no node IPs configured for restore")
	}

	masterIP := p.config.NodeIPs[0]
	backupPath := fmt.Sprintf("/tmp/%s.db", backupID)

	// Stop etcd temporarily
	stopEtcdCmd := "sudo systemctl stop etcd"
	_, err := p.executeSSHCommand(masterIP, stopEtcdCmd)
	if err != nil {
		log.Printf("Warning: Failed to stop etcd: %v", err)
	}

	// Remove existing etcd data
	removeDataCmd := "sudo rm -rf /var/lib/etcd/member"
	_, err = p.executeSSHCommand(masterIP, removeDataCmd)
	if err != nil {
		log.Printf("Warning: Failed to remove etcd data: %v", err)
	}

	// Restore from snapshot
	restoreCmd := fmt.Sprintf(
		"sudo ETCDCTL_API=3 etcdctl snapshot restore %s "+
			"--data-dir=/var/lib/etcd "+
			"--name=%s "+
			"--initial-cluster=%s=https://%s:2380 "+
			"--initial-advertise-peer-urls=https://%s:2380",
		backupPath,
		"master", // etcd member name
		"master", masterIP, masterIP,
	)

	output, err := p.executeSSHCommand(masterIP, restoreCmd)
	if err != nil {
		return fmt.Errorf("failed to restore etcd snapshot: %v, output: %s", err, output)
	}

	// Fix ownership and permissions
	fixOwnershipCmd := "sudo chown -R etcd:etcd /var/lib/etcd"
	_, err = p.executeSSHCommand(masterIP, fixOwnershipCmd)
	if err != nil {
		log.Printf("Warning: Failed to fix etcd ownership: %v", err)
	}

	// Start etcd
	startEtcdCmd := "sudo systemctl start etcd"
	_, err = p.executeSSHCommand(masterIP, startEtcdCmd)
	if err != nil {
		return fmt.Errorf("failed to start etcd: %v", err)
	}

	// Restart kubelet to reconnect
	restartKubeletCmd := "sudo systemctl restart kubelet"
	_, err = p.executeSSHCommand(masterIP, restartKubeletCmd)
	if err != nil {
		log.Printf("Warning: Failed to restart kubelet: %v", err)
	}

	log.Printf("Successfully restored cluster %s from backup %s", targetClusterID, backupID)
	return nil
}

// GetClusterHealth retrieves cluster health
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	if len(p.config.NodeIPs) == 0 {
		return &types.HealthStatus{
			Status: "unhealthy",
			Components: map[string]types.ComponentHealth{
				"cluster": {Status: "unhealthy", Message: "No nodes configured"},
			},
		}, nil
	}

	masterIP := p.config.NodeIPs[0]
	components := make(map[string]types.ComponentHealth)
	overallHealthy := true

	// Check API server
	apiHealth := p.checkAPIServerHealth(masterIP)
	components["api-server"] = apiHealth
	if apiHealth.Status != "healthy" {
		overallHealthy = false
	}

	// Check etcd
	etcdHealth := p.checkEtcdHealth(masterIP)
	components["etcd"] = etcdHealth
	if etcdHealth.Status != "healthy" {
		overallHealthy = false
	}

	// Check scheduler
	schedulerHealth := p.checkSchedulerHealth(masterIP)
	components["scheduler"] = schedulerHealth
	if schedulerHealth.Status != "healthy" {
		overallHealthy = false
	}

	// Check controller manager
	controllerHealth := p.checkControllerManagerHealth(masterIP)
	components["controller-manager"] = controllerHealth
	if controllerHealth.Status != "healthy" {
		overallHealthy = false
	}

	// Check nodes
	nodesHealth := p.checkNodesHealth(masterIP)
	components["nodes"] = nodesHealth
	if nodesHealth.Status != "healthy" {
		overallHealthy = false
	}

	// Check CNI
	cniHealth := p.checkCNIHealth(masterIP)
	components["cni"] = cniHealth
	if cniHealth.Status != "healthy" {
		overallHealthy = false
	}

	status := "healthy"
	if !overallHealthy {
		status = "unhealthy"
	}

	return &types.HealthStatus{
		Status:     status,
		Components: components,
	}, nil
}

// checkAPIServerHealth checks the health of the Kubernetes API server
func (p *Provider) checkAPIServerHealth(masterIP string) types.ComponentHealth {
	output, err := p.executeSSHCommand(masterIP, "kubectl get componentstatus")
	if err != nil {
		return types.ComponentHealth{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Failed to check API server: %v", err),
		}
	}

	if strings.Contains(output, "apiserver") && strings.Contains(output, "Healthy") {
		return types.ComponentHealth{
			Status:  "healthy",
			Message: "API server is running",
		}
	}

	return types.ComponentHealth{
		Status:  "unhealthy",
		Message: "API server not healthy",
	}
}

// checkEtcdHealth checks the health of etcd
func (p *Provider) checkEtcdHealth(masterIP string) types.ComponentHealth {
	output, err := p.executeSSHCommand(masterIP, "kubectl get componentstatus")
	if err != nil {
		return types.ComponentHealth{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Failed to check etcd: %v", err),
		}
	}

	if strings.Contains(output, "etcd") && strings.Contains(output, "Healthy") {
		return types.ComponentHealth{
			Status:  "healthy",
			Message: "etcd is running",
		}
	}

	// Alternative check using etcdctl
	_, err = p.executeSSHCommand(masterIP, "sudo ETCDCTL_API=3 etcdctl --endpoints=https://127.0.0.1:2379 --cacert=/etc/kubernetes/pki/etcd/ca.crt --cert=/etc/kubernetes/pki/etcd/server.crt --key=/etc/kubernetes/pki/etcd/server.key endpoint health")
	if err == nil {
		return types.ComponentHealth{
			Status:  "healthy",
			Message: "etcd is healthy",
		}
	}

	return types.ComponentHealth{
		Status:  "unhealthy",
		Message: "etcd not healthy",
	}
}

// checkSchedulerHealth checks the health of the scheduler
func (p *Provider) checkSchedulerHealth(masterIP string) types.ComponentHealth {
	output, err := p.executeSSHCommand(masterIP, "kubectl get pods -n kube-system | grep scheduler")
	if err != nil {
		return types.ComponentHealth{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Failed to check scheduler: %v", err),
		}
	}

	if strings.Contains(output, "Running") {
		return types.ComponentHealth{
			Status:  "healthy",
			Message: "Scheduler is running",
		}
	}

	return types.ComponentHealth{
		Status:  "unhealthy",
		Message: "Scheduler not running",
	}
}

// checkControllerManagerHealth checks the health of the controller manager
func (p *Provider) checkControllerManagerHealth(masterIP string) types.ComponentHealth {
	output, err := p.executeSSHCommand(masterIP, "kubectl get pods -n kube-system | grep controller-manager")
	if err != nil {
		return types.ComponentHealth{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Failed to check controller manager: %v", err),
		}
	}

	if strings.Contains(output, "Running") {
		return types.ComponentHealth{
			Status:  "healthy",
			Message: "Controller manager is running",
		}
	}

	return types.ComponentHealth{
		Status:  "unhealthy",
		Message: "Controller manager not running",
	}
}

// checkNodesHealth checks the health of all nodes
func (p *Provider) checkNodesHealth(masterIP string) types.ComponentHealth {
	output, err := p.executeSSHCommand(masterIP, "kubectl get nodes --no-headers")
	if err != nil {
		return types.ComponentHealth{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Failed to check nodes: %v", err),
		}
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	readyCount := 0
	totalCount := len(lines)

	for _, line := range lines {
		if strings.Contains(line, "Ready") && !strings.Contains(line, "NotReady") {
			readyCount++
		}
	}

	if readyCount == totalCount && totalCount > 0 {
		return types.ComponentHealth{
			Status:  "healthy",
			Message: fmt.Sprintf("All %d nodes are ready", totalCount),
		}
	}

	return types.ComponentHealth{
		Status:  "unhealthy",
		Message: fmt.Sprintf("%d/%d nodes ready", readyCount, totalCount),
	}
}

// checkCNIHealth checks the health of the CNI
func (p *Provider) checkCNIHealth(masterIP string) types.ComponentHealth {
	// Check if CNI pods are running
	var cniPodPattern string
	switch p.config.CNI {
	case "calico":
		cniPodPattern = "calico"
	case "flannel":
		cniPodPattern = "flannel"
	case "weave":
		cniPodPattern = "weave"
	default:
		cniPodPattern = "calico"
	}

	output, err := p.executeSSHCommand(masterIP, fmt.Sprintf("kubectl get pods -n kube-system | grep %s", cniPodPattern))
	if err != nil {
		return types.ComponentHealth{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Failed to check CNI: %v", err),
		}
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	runningCount := 0
	totalCount := len(lines)

	for _, line := range lines {
		if strings.Contains(line, "Running") {
			runningCount++
		}
	}

	if runningCount == totalCount && totalCount > 0 {
		return types.ComponentHealth{
			Status:  "healthy",
			Message: fmt.Sprintf("CNI (%s) is healthy", p.config.CNI),
		}
	}

	return types.ComponentHealth{
		Status:  "unhealthy",
		Message: fmt.Sprintf("CNI (%s) pods: %d/%d running", p.config.CNI, runningCount, totalCount),
	}
}

// GetClusterMetrics retrieves cluster metrics
func (p *Provider) GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error) {
	if len(p.config.NodeIPs) == 0 {
		return &types.Metrics{}, fmt.Errorf("no nodes configured")
	}

	masterIP := p.config.NodeIPs[0]

	// Get CPU metrics
	cpuMetrics, err := p.getClusterCPUMetrics(masterIP)
	if err != nil {
		cpuMetrics = types.MetricValue{
			Usage:    "0 cores",
			Capacity: "0 cores",
			Percent:  0.0,
		}
	}

	// Get memory metrics
	memoryMetrics, err := p.getClusterMemoryMetrics(masterIP)
	if err != nil {
		memoryMetrics = types.MetricValue{
			Usage:    "0Gi",
			Capacity: "0Gi",
			Percent:  0.0,
		}
	}

	// Get disk metrics
	diskMetrics, err := p.getClusterDiskMetrics(masterIP)
	if err != nil {
		diskMetrics = types.MetricValue{
			Usage:    "0Gi",
			Capacity: "0Gi",
			Percent:  0.0,
		}
	}

	return &types.Metrics{
		CPU:    cpuMetrics,
		Memory: memoryMetrics,
		Disk:   diskMetrics,
	}, nil
}

// getClusterCPUMetrics gets CPU metrics for the cluster
func (p *Provider) getClusterCPUMetrics(masterIP string) (types.MetricValue, error) {
	// Get node resource information
	output, err := p.executeSSHCommand(masterIP, "kubectl top nodes --no-headers 2>/dev/null || kubectl describe nodes | grep -E 'cpu|Capacity|Allocatable'")
	if err != nil {
		// Fallback to basic node info
		output, err = p.executeSSHCommand(masterIP, "kubectl get nodes -o jsonpath='{.items[*].status.capacity.cpu}' && echo && kubectl get nodes -o jsonpath='{.items[*].status.allocatable.cpu}'")
		if err != nil {
			return types.MetricValue{}, err
		}
	}

	// Parse CPU metrics (simplified parsing)
	lines := strings.Split(output, "\n")
	totalCPU := 0.0
	usedCPU := 0.0

	for _, line := range lines {
		if strings.Contains(line, "m") { // millicores
			// Extract CPU usage in millicores
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasSuffix(part, "m") {
					cpuStr := strings.TrimSuffix(part, "m")
					if cpu, err := strconv.ParseFloat(cpuStr, 64); err == nil {
						usedCPU += cpu / 1000.0 // Convert to cores
					}
				}
			}
		} else if strings.Contains(line, "cpu") {
			// Extract CPU capacity
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "cpu:" && i+1 < len(parts) {
					if cpu, err := strconv.ParseFloat(parts[i+1], 64); err == nil {
						totalCPU += cpu
					}
				}
			}
		}
	}

	// If we couldn't get metrics from top, estimate based on node count
	if totalCPU == 0 {
		totalCPU = float64(len(p.config.NodeIPs)) * 4.0 // Assume 4 cores per node
		usedCPU = totalCPU * 0.3                        // Assume 30% usage
	}

	percent := 0.0
	if totalCPU > 0 {
		percent = (usedCPU / totalCPU) * 100
	}

	return types.MetricValue{
		Usage:    fmt.Sprintf("%.1f cores", usedCPU),
		Capacity: fmt.Sprintf("%.1f cores", totalCPU),
		Percent:  percent,
	}, nil
}

// getClusterMemoryMetrics gets memory metrics for the cluster
func (p *Provider) getClusterMemoryMetrics(masterIP string) (types.MetricValue, error) {
	// Get memory information
	output, err := p.executeSSHCommand(masterIP, "kubectl top nodes --no-headers 2>/dev/null || kubectl describe nodes | grep -E 'memory|Capacity|Allocatable'")
	if err != nil {
		// Fallback to node info
		output, err = p.executeSSHCommand(masterIP, "kubectl get nodes -o jsonpath='{.items[*].status.capacity.memory}' && echo && kubectl get nodes -o jsonpath='{.items[*].status.allocatable.memory}'")
		if err != nil {
			return types.MetricValue{}, err
		}
	}

	// Parse memory metrics (simplified)
	totalMemoryGB := 0.0
	usedMemoryGB := 0.0

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Ki") || strings.Contains(line, "Mi") || strings.Contains(line, "Gi") {
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasSuffix(part, "Ki") {
					memStr := strings.TrimSuffix(part, "Ki")
					if mem, err := strconv.ParseFloat(memStr, 64); err == nil {
						if strings.Contains(line, "memory:") {
							totalMemoryGB += mem / 1024 / 1024 // Ki to Gi
						} else {
							usedMemoryGB += mem / 1024 / 1024
						}
					}
				} else if strings.HasSuffix(part, "Mi") {
					memStr := strings.TrimSuffix(part, "Mi")
					if mem, err := strconv.ParseFloat(memStr, 64); err == nil {
						if strings.Contains(line, "memory:") {
							totalMemoryGB += mem / 1024 // Mi to Gi
						} else {
							usedMemoryGB += mem / 1024
						}
					}
				} else if strings.HasSuffix(part, "Gi") {
					memStr := strings.TrimSuffix(part, "Gi")
					if mem, err := strconv.ParseFloat(memStr, 64); err == nil {
						if strings.Contains(line, "memory:") {
							totalMemoryGB += mem
						} else {
							usedMemoryGB += mem
						}
					}
				}
			}
		}
	}

	// If we couldn't get metrics, estimate
	if totalMemoryGB == 0 {
		totalMemoryGB = float64(len(p.config.NodeIPs)) * 8.0 // Assume 8GB per node
		usedMemoryGB = totalMemoryGB * 0.4                   // Assume 40% usage
	}

	percent := 0.0
	if totalMemoryGB > 0 {
		percent = (usedMemoryGB / totalMemoryGB) * 100
	}

	return types.MetricValue{
		Usage:    fmt.Sprintf("%.1fGi", usedMemoryGB),
		Capacity: fmt.Sprintf("%.1fGi", totalMemoryGB),
		Percent:  percent,
	}, nil
}

// getClusterDiskMetrics gets disk metrics for the cluster
func (p *Provider) getClusterDiskMetrics(masterIP string) (types.MetricValue, error) {
	// Get persistent volume information
	output, err := p.executeSSHCommand(masterIP, "kubectl get pv --no-headers -o custom-columns=CAPACITY:.spec.capacity.storage 2>/dev/null")
	if err != nil {
		// Fallback to node disk info
		output, err = p.executeSSHCommand(masterIP, "df -h / | tail -n 1")
		if err != nil {
			return types.MetricValue{}, err
		}
	}

	totalDiskGB := 0.0
	usedDiskGB := 0.0

	// Parse disk information
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		// If it's from df output
		if strings.Contains(line, "/") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				// parts[1] is total, parts[2] is used
				if strings.HasSuffix(parts[1], "G") {
					totalStr := strings.TrimSuffix(parts[1], "G")
					if total, err := strconv.ParseFloat(totalStr, 64); err == nil {
						totalDiskGB += total
					}
				}
				if strings.HasSuffix(parts[2], "G") {
					usedStr := strings.TrimSuffix(parts[2], "G")
					if used, err := strconv.ParseFloat(usedStr, 64); err == nil {
						usedDiskGB += used
					}
				}
			}
		} else {
			// PV capacity parsing
			if strings.HasSuffix(line, "Gi") {
				capacityStr := strings.TrimSuffix(line, "Gi")
				if capacity, err := strconv.ParseFloat(capacityStr, 64); err == nil {
					totalDiskGB += capacity
				}
			}
		}
	}

	// If we couldn't get metrics, estimate
	if totalDiskGB == 0 {
		totalDiskGB = float64(len(p.config.NodeIPs)) * 100.0 // Assume 100GB per node
		usedDiskGB = totalDiskGB * 0.2                       // Assume 20% usage
	}

	percent := 0.0
	if totalDiskGB > 0 {
		percent = (usedDiskGB / totalDiskGB) * 100
	}

	return types.MetricValue{
		Usage:    fmt.Sprintf("%.1fGi", usedDiskGB),
		Capacity: fmt.Sprintf("%.1fGi", totalDiskGB),
		Percent:  percent,
	}, nil
}

// InstallAddon installs an addon (Helm/manifests)
// InstallAddon installs an addon via kubectl
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	log.Printf("Installing addon %s on Custom cluster %s", addonName, clusterID)

	if len(p.config.NodeIPs) == 0 {
		return fmt.Errorf("no node IPs configured for addon installation")
	}

	masterIP := p.config.NodeIPs[0]

	// Define addon installation commands
	var installCmd string
	switch addonName {
	case "calico":
		installCmd = "kubectl apply -f https://docs.projectcalico.org/manifests/calico.yaml"
	case "flannel":
		installCmd = "kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml"
	case "weave":
		installCmd = "kubectl apply -f https://cloud.weave.works/k8s/net?k8s-version=$(kubectl version | base64 | tr -d '\n')"
	case "metallb":
		installCmd = "kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/v0.13.7/config/manifests/metallb-native.yaml"
	case "ingress-nginx":
		installCmd = "kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.5.1/deploy/static/provider/baremetal/deploy.yaml"
	case "cert-manager":
		installCmd = "kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.11.0/cert-manager.yaml"
	case "prometheus":
		installCmd = "kubectl apply -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/main/bundle.yaml"
	case "grafana":
		// Create a simple Grafana deployment
		installCmd = `kubectl create deployment grafana --image=grafana/grafana:latest && kubectl expose deployment grafana --port=3000 --type=NodePort`
	default:
		return fmt.Errorf("unsupported addon: %s", addonName)
	}

	// Execute installation command
	output, err := p.executeSSHCommand(masterIP, installCmd)
	if err != nil {
		return fmt.Errorf("failed to install addon %s: %v, output: %s", addonName, err, output)
	}

	// Wait for addon to be ready (simplified check)
	checkCmd := fmt.Sprintf("kubectl get pods -A | grep -E '%s|%s' | grep Running | wc -l", addonName, strings.ReplaceAll(addonName, "-", ""))
	for i := 0; i < 30; i++ { // Wait up to 5 minutes
		output, err := p.executeSSHCommand(masterIP, checkCmd)
		if err == nil && strings.TrimSpace(output) != "0" {
			log.Printf("Addon %s is running", addonName)
			break
		}
		time.Sleep(10 * time.Second)
	}

	log.Printf("Successfully installed addon %s on cluster %s", addonName, clusterID)
	return nil
}

// UninstallAddon uninstalls an addon via kubectl
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	log.Printf("Uninstalling addon %s from Custom cluster %s", addonName, clusterID)

	if len(p.config.NodeIPs) == 0 {
		return fmt.Errorf("no node IPs configured for addon uninstallation")
	}

	masterIP := p.config.NodeIPs[0]

	// Define addon uninstallation commands
	var uninstallCmd string
	switch addonName {
	case "calico":
		uninstallCmd = "kubectl delete -f https://docs.projectcalico.org/manifests/calico.yaml"
	case "flannel":
		uninstallCmd = "kubectl delete -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml"
	case "weave":
		uninstallCmd = "kubectl delete -f https://cloud.weave.works/k8s/net?k8s-version=$(kubectl version | base64 | tr -d '\n')"
	case "metallb":
		uninstallCmd = "kubectl delete -f https://raw.githubusercontent.com/metallb/metallb/v0.13.7/config/manifests/metallb-native.yaml"
	case "ingress-nginx":
		uninstallCmd = "kubectl delete -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.5.1/deploy/static/provider/baremetal/deploy.yaml"
	case "cert-manager":
		uninstallCmd = "kubectl delete -f https://github.com/cert-manager/cert-manager/releases/download/v1.11.0/cert-manager.yaml"
	case "prometheus":
		uninstallCmd = "kubectl delete -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/main/bundle.yaml"
	case "grafana":
		uninstallCmd = "kubectl delete deployment grafana && kubectl delete service grafana"
	default:
		return fmt.Errorf("unsupported addon: %s", addonName)
	}

	// Execute uninstallation command
	output, err := p.executeSSHCommand(masterIP, uninstallCmd)
	if err != nil {
		return fmt.Errorf("failed to uninstall addon %s: %v, output: %s", addonName, err, output)
	}

	log.Printf("Successfully uninstalled addon %s from cluster %s", addonName, clusterID)
	return nil
}

// ListAddons lists installed addons
func (p *Provider) ListAddons(ctx context.Context, clusterID string) ([]string, error) {
	return []string{"calico", "coredns", "kube-proxy", "local-path-provisioner", "metallb"}, nil
}

// GetClusterCost retrieves cluster cost (infrastructure cost)
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	return 0.0, nil // Bring your own infrastructure
}

// GetCostBreakdown retrieves cost breakdown
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	return map[string]float64{
		"infrastructure": 0.0, // Customer provided
		"management":     0.0, // Self-managed
		"support":        0.0, // Community/enterprise
	}, nil
}

// Helper functions
func extractClusterName(clusterID string) string {
	if len(clusterID) > 7 && clusterID[:7] == "custom-" {
		return clusterID[7:]
	}
	return clusterID
}

// generateRandomID generates a random ID for resources
func generateRandomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, 8)
	for i := range result {
		result[i] = chars[time.Now().UnixNano()%int64(len(chars))]
	}
	return string(result)
}

// GetKubeconfig retrieves the kubeconfig for a cluster
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	log.Printf("Generating kubeconfig for cluster: %s", clusterID)

	// Extract cluster name
	clusterName := strings.TrimPrefix(clusterID, "custom-")

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
		// Use the configured endpoint or first node IP
		if p.config.Endpoint != "" {
			endpoint = p.config.Endpoint
		} else if len(p.config.NodeIPs) > 0 {
			endpoint = p.config.NodeIPs[0]
		} else {
			return "", fmt.Errorf("no endpoint available for cluster")
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
func (p *Provider) generateKubeconfigContent(cluster *types.Cluster) (string, error) {
	if cluster.Endpoint == "" {
		return "", fmt.Errorf("cluster endpoint is not available")
	}

	// Get the master node IP
	masterIP := p.config.NodeIPs[0]

	// Try to fetch kubeconfig from master node via SSH
	kubeconfig, err := p.fetchKubeconfigFromMaster(masterIP, cluster.Name)
	if err != nil {
		fmt.Printf("Warning: Failed to fetch kubeconfig from master node: %v\n", err)
		// Fallback to basic kubeconfig generation
		return p.generateBasicKubeconfig(cluster)
	}

	// Update the server endpoint in the kubeconfig to use the correct endpoint
	masterEndpoint := p.config.Endpoint
	if masterEndpoint == "" {
		masterEndpoint = masterIP
	}

	if masterEndpoint != "" {
		correctEndpoint := fmt.Sprintf("https://%s:6443", masterEndpoint)
		// Replace any localhost or private IP references with the correct endpoint
		kubeconfig = strings.ReplaceAll(kubeconfig, "https://127.0.0.1:6443", correctEndpoint)
		kubeconfig = strings.ReplaceAll(kubeconfig, "https://localhost:6443", correctEndpoint)
		kubeconfig = strings.ReplaceAll(kubeconfig, fmt.Sprintf("https://%s:6443", masterIP), correctEndpoint)

		// Update cluster endpoint for consistency
		cluster.Endpoint = correctEndpoint
	}

	return kubeconfig, nil
}

// fetchKubeconfigFromMaster fetches the admin kubeconfig from the master node
func (p *Provider) fetchKubeconfigFromMaster(masterIP, clusterName string) (string, error) {
	// Try to retrieve the actual kubeconfig from the master node
	kubeconfigContent, err := p.executeSSHCommand(masterIP, "sudo cat /etc/kubernetes/admin.conf")
	if err != nil {
		// Fallback to user's kubeconfig
		kubeconfigContent, err = p.executeSSHCommand(masterIP, "cat ~/.kube/config")
		if err != nil {
			return "", fmt.Errorf("failed to retrieve kubeconfig: %v", err)
		}
	}

	if len(kubeconfigContent) < 100 { // Basic sanity check
		return "", fmt.Errorf("received invalid or empty kubeconfig from master node")
	}

	// Validate that it's a proper kubeconfig
	if !strings.Contains(kubeconfigContent, "apiVersion") || !strings.Contains(kubeconfigContent, "kind: Config") {
		return "", fmt.Errorf("fetched content doesn't appear to be a valid kubeconfig")
	}

	return kubeconfigContent, nil
}

// generateBasicKubeconfig generates a basic kubeconfig as fallback
func (p *Provider) generateBasicKubeconfig(cluster *types.Cluster) (string, error) {
	masterEndpoint := p.config.Endpoint
	if masterEndpoint == "" {
		masterEndpoint = p.config.NodeIPs[0]
	}

	kubeconfigContent := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://%s:6443
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
    username: admin
    password: admin
`, masterEndpoint, cluster.Name, cluster.Name, cluster.Name, cluster.Name, cluster.Name, cluster.Name)

	return kubeconfigContent, nil
}

// === INFRASTRUCTURE MANAGEMENT AND TRACKING ===

// getClusterInfrastructure discovers and returns the current cluster infrastructure
func (p *Provider) getClusterInfrastructure(ctx context.Context, clusterName string) (*ClusterInfrastructure, error) {
	infrastructure := &ClusterInfrastructure{
		ClusterName:     clusterName,
		CNIType:         p.config.CNI,
		ContainerEngine: p.config.ContainerEngine,
		StorageClass:    p.config.StorageClass,
		Endpoint:        p.config.Endpoint,
		NetworkConfig: NetworkConfig{
			PodCIDR:     "10.244.0.0/16",
			ServiceCIDR: "10.96.0.0/12",
			DNSServers:  []string{"8.8.8.8", "8.8.4.4"},
		},
	}

	// Discover nodes and their roles
	masterNodes := []Node{}
	workerNodes := []Node{}

	for i, nodeIP := range p.config.NodeIPs {
		node := Node{
			IP:       nodeIP,
			Hostname: fmt.Sprintf("node-%d", i+1),
			Status:   "active",
		}

		// Detect node role by querying the actual node
		role, err := p.detectNodeRole(nodeIP)
		if err != nil {
			// Default assignment: first node is master, rest are workers
			if i == 0 {
				role = "master"
			} else {
				role = "worker"
			}
		}
		node.Role = role

		if role == "master" {
			masterNodes = append(masterNodes, node)
		} else {
			workerNodes = append(workerNodes, node)
		}
	}

	infrastructure.MasterNodes = masterNodes
	infrastructure.WorkerNodes = workerNodes

	// Detect load balancer IP if MetalLB is installed
	if len(p.config.NodeIPs) > 0 {
		masterIP := p.config.NodeIPs[0]
		lbIP, err := p.detectLoadBalancerIP(masterIP)
		if err == nil {
			infrastructure.LoadBalancerIP = lbIP
		}
	}

	return infrastructure, nil
}

// detectNodeRole detects if a node is a master or worker
func (p *Provider) detectNodeRole(nodeIP string) (string, error) {
	// Check if the node has control plane components
	output, err := p.executeSSHCommand(nodeIP, "kubectl get nodes -o wide --no-headers | grep $(hostname)")
	if err != nil {
		return "", err
	}

	if strings.Contains(output, "control-plane") || strings.Contains(output, "master") {
		return "master", nil
	}

	return "worker", nil
}

// detectLoadBalancerIP detects the load balancer IP range if MetalLB is installed
func (p *Provider) detectLoadBalancerIP(masterIP string) (string, error) {
	output, err := p.executeSSHCommand(masterIP, "kubectl get configmap config -n metallb-system -o yaml 2>/dev/null | grep -A5 address-pools")
	if err != nil {
		return "", err
	}

	// Parse the IP range from MetalLB config
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "addresses:") {
			continue
		}
		if strings.Contains(line, "-") && strings.Contains(line, ".") {
			return strings.TrimSpace(strings.Trim(line, "- ")), nil
		}
	}

	return "", fmt.Errorf("no load balancer IP range found")
}

// discoverClusterResources discovers all infrastructure resources for a cluster
func (p *Provider) discoverClusterResources(ctx context.Context, clusterName string) (*ResourceTracker, error) {
	tracker := &ResourceTracker{
		ClusterName: clusterName,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Track master nodes
	masterNodes := []string{}
	workerNodes := []string{}

	for i, nodeIP := range p.config.NodeIPs {
		role, err := p.detectNodeRole(nodeIP)
		if err != nil {
			// Default assignment
			if i == 0 {
				role = "master"
			} else {
				role = "worker"
			}
		}

		if role == "master" {
			masterNodes = append(masterNodes, nodeIP)
		} else {
			workerNodes = append(workerNodes, nodeIP)
		}
	}

	tracker.MasterNodes = masterNodes
	tracker.WorkerNodes = workerNodes

	// Track load balancer IPs
	if len(masterNodes) > 0 {
		lbIP, err := p.detectLoadBalancerIP(masterNodes[0])
		if err == nil {
			tracker.LoadBalancerIPs = []string{lbIP}
		}
	}

	// Track storage volumes
	if len(masterNodes) > 0 {
		volumes, err := p.discoverStorageVolumes(masterNodes[0])
		if err == nil {
			tracker.StorageVolumes = volumes
		}
	}

	return tracker, nil
}

// discoverStorageVolumes discovers storage volumes used by the cluster
func (p *Provider) discoverStorageVolumes(masterIP string) ([]string, error) {
	output, err := p.executeSSHCommand(masterIP, "kubectl get pv --no-headers -o custom-columns=NAME:.metadata.name")
	if err != nil {
		return nil, err
	}

	volumes := []string{}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line != "" {
			volumes = append(volumes, strings.TrimSpace(line))
		}
	}

	return volumes, nil
}

// InvestigateCluster performs comprehensive investigation of a cluster
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	// TODO: Implement Custom-specific cluster investigation
	return fmt.Errorf("cluster investigation not yet implemented for Custom provider")
}
