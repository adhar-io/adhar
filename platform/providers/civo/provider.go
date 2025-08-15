package civo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/civo/civogo"
	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"

	"k8s.io/client-go/tools/clientcmd"
)

// Register the Civo provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("civo", func(config map[string]interface{}) (provider.Provider, error) {

		civoConfig := &Config{}

		// Parse authentication
		if token, ok := config["token"].(string); ok {
			civoConfig.Token = token
		}
		if useEnv, ok := config["useEnvironment"].(bool); ok {
			civoConfig.UseEnvironment = useEnv
		}
		if region, ok := config["region"].(string); ok {
			civoConfig.Region = region
		}

		// Parse configuration section
		if configSection, ok := config["config"].(map[string]interface{}); ok {

			// Basic configuration
			if size, ok := configSection["size"].(string); ok {
				civoConfig.Size = size

			}
			if diskImage, ok := configSection["disk_image"].(string); ok {
				civoConfig.DiskImage = diskImage

			}
			if defaultNodeCount, ok := configSection["default_node_count"].(float64); ok {
				civoConfig.DefaultNodeCount = int(defaultNodeCount)

			}

			// Network configuration
			if networkID, ok := configSection["network_id"].(string); ok {
				civoConfig.NetworkID = networkID
			}
			if networkLabel, ok := configSection["network_label"].(string); ok {
				civoConfig.NetworkLabel = networkLabel
			}
			if reuseNetwork, ok := configSection["reuse_existing_network"].(bool); ok {
				civoConfig.ReuseExistingNetwork = reuseNetwork
			}

			// SSH configuration
			if sshKeys, ok := configSection["ssh_key_ids"].([]interface{}); ok {
				for _, key := range sshKeys {
					if keyStr, ok := key.(string); ok {
						civoConfig.SSHKeyIDs = append(civoConfig.SSHKeyIDs, keyStr)
					}
				}
			}

			// Parse tags
			if tags, ok := configSection["tags"].([]interface{}); ok {
				for _, tag := range tags {
					if tagStr, ok := tag.(string); ok {
						civoConfig.Tags = append(civoConfig.Tags, tagStr)
					}
				}
			}

			// Parse firewall rules
			if firewallRules, ok := configSection["firewall_rules"].([]interface{}); ok {
				for _, rule := range firewallRules {
					if ruleMap, ok := rule.(map[string]interface{}); ok {
						fwRule := FirewallRuleConfig{}

						if direction, ok := ruleMap["direction"].(string); ok {
							fwRule.Direction = direction
						}
						if protocol, ok := ruleMap["protocol"].(string); ok {
							fwRule.Protocol = protocol
						}
						if startPort, ok := ruleMap["start_port"].(string); ok {
							fwRule.StartPort = startPort
						}
						if endPort, ok := ruleMap["end_port"].(string); ok {
							fwRule.EndPort = endPort
						}
						if cidr, ok := ruleMap["cidr"].(string); ok {
							fwRule.Cidr = cidr
						}
						if label, ok := ruleMap["label"].(string); ok {
							fwRule.Label = label
						}

						civoConfig.FirewallRules = append(civoConfig.FirewallRules, fwRule)
					}
				}
			}
		}

		return NewProvider(civoConfig)
	})
}

type Provider struct {
	client           *civogo.Client
	config           *Config
	clusters         map[string]*types.Cluster
	resourceTrackers map[string]*ResourceTracker
}

type Config struct {
	Token                string               `json:"token"`
	TokenFile            string               `json:"tokenFile,omitempty"`
	UseEnvironment       bool                 `json:"useEnvironment,omitempty"`
	Region               string               `json:"region"`
	Size                 string               `json:"size"`
	DiskImage            string               `json:"diskImage"`
	DefaultNodeCount     int                  `json:"defaultNodeCount"`
	NetworkID            string               `json:"networkId,omitempty"`
	NetworkLabel         string               `json:"networkLabel,omitempty"`
	ReuseExistingNetwork bool                 `json:"reuseExistingNetwork,omitempty"`
	SSHKeyIDs            []string             `json:"sshKeyIds,omitempty"`
	FirewallRules        []FirewallRuleConfig `json:"firewallRules,omitempty"`
	Tags                 []string             `json:"tags,omitempty"`
}

type FirewallRuleConfig struct {
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	StartPort string `json:"startPort"`
	EndPort   string `json:"endPort"`
	Cidr      string `json:"cidr"`
	Label     string `json:"label"`
}

type NodeInfo struct {
	Name       string
	InstanceID string
	PublicIP   string
	PrivateIP  string
	Size       string
	IsMaster   bool
	PrivateKey string
	PublicKey  string
	CreatedAt  time.Time
}

type ClusterInfrastructure struct {
	NetworkID     string
	NetworkLabel  string
	FirewallID    string
	FirewallLabel string
	MasterNodes   []NodeInfo
	WorkerNodes   []NodeInfo
	K3sToken      string
}

type ResourceTracker struct {
	Region    string
	Networks  []string
	Firewalls []string
	Instances []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewProvider(config *Config) (*Provider, error) {
	log.Println("Initializing Civo provider...")

	var token string
	switch {
	case config.Token != "":
		token = config.Token
	case config.TokenFile != "":
		tokenBytes, err := os.ReadFile(config.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read token file %s: %w", config.TokenFile, err)
		}
		token = strings.TrimSpace(string(tokenBytes))
	case config.UseEnvironment:
		token = os.Getenv("CIVO_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("CIVO_TOKEN environment variable is not set")
		}
	default:
		return nil, fmt.Errorf("Civo token is required")
	}

	if config.Region == "" {
		config.Region = "LON1"
	}
	if config.Size == "" {
		config.Size = "g4s.kube.medium"
	}
	if config.DiskImage == "" {
		config.DiskImage = "ubuntu-24.04"
	}
	if config.DefaultNodeCount == 0 {
		config.DefaultNodeCount = 3
	}

	client, err := civogo.NewClient(token, config.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to create Civo client: %w", err)
	}

	return &Provider{
		client:           client,
		config:           config,
		clusters:         make(map[string]*types.Cluster),
		resourceTrackers: make(map[string]*ResourceTracker),
	}, nil
}

// Provider interface implementation
func (p *Provider) Name() string {
	return "civo"
}

func (p *Provider) Region() string {
	return p.config.Region
}

func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	return nil
}

func (p *Provider) ValidatePermissions(ctx context.Context) error {
	_, err := p.client.ListInstances(1, 1)
	return err
}

func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	log.Printf("Creating Civo cluster: %s in region %s", spec.Name, p.config.Region)

	if err := p.validateClusterSpec(spec); err != nil {
		return nil, fmt.Errorf("invalid cluster spec: %w", err)
	}

	infrastructure, err := p.createClusterInfrastructure(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster infrastructure: %w", err)
	}

	masterNode := infrastructure.MasterNodes[0]
	clusterID := fmt.Sprintf("civo-%s", spec.Name)

	log.Printf("Waiting for cluster %s to become ready...", spec.Name)
	kubeconfig, err := p.waitForClusterReady(ctx, masterNode.PublicIP, masterNode.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("cluster readiness check failed: %w", err)
	}
	log.Printf("Cluster %s is ready.", spec.Name)

	if err := p.saveKubeconfig(spec.Name, kubeconfig); err != nil {
		log.Printf("Warning: failed to save kubeconfig: %v", err)
	}

	cluster := &types.Cluster{
		ID:        clusterID,
		Name:      spec.Name,
		Provider:  "civo",
		Region:    p.config.Region,
		Version:   spec.Version,
		Status:    types.ClusterStatusRunning,
		Endpoint:  fmt.Sprintf("https://%s:6443", masterNode.PublicIP),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"network":          infrastructure.NetworkLabel,
			"masterNodes":      len(infrastructure.MasterNodes),
			"workerNodes":      len(infrastructure.WorkerNodes),
			"masterPublicIP":   masterNode.PublicIP,
			"masterPrivateKey": masterNode.PrivateKey,
		},
	}

	p.clusters[cluster.ID] = cluster
	p.saveClustersToCache()

	// Track resources
	resourceTracker := &ResourceTracker{
		Region:    p.config.Region,
		Networks:  []string{infrastructure.NetworkID},
		CreatedAt: time.Now(),
	}
	for _, node := range infrastructure.MasterNodes {
		resourceTracker.Instances = append(resourceTracker.Instances, node.InstanceID)
	}
	for _, node := range infrastructure.WorkerNodes {
		resourceTracker.Instances = append(resourceTracker.Instances, node.InstanceID)
	}
	p.resourceTrackers[cluster.ID] = resourceTracker

	return cluster, nil
}
func (p *Provider) saveKubeconfig(clusterName, kubeconfig string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not get user home directory: %w", err)
	}

	kubeDir := filepath.Join(homeDir, ".kube")
	if err := os.MkdirAll(kubeDir, 0755); err != nil {
		return fmt.Errorf("could not create .kube directory: %w", err)
	}

	configPath := filepath.Join(kubeDir, fmt.Sprintf("adhar-%s-config", clusterName))
	err = os.WriteFile(configPath, []byte(kubeconfig), 0600)
	if err != nil {
		return fmt.Errorf("could not write kubeconfig to file: %w", err)
	}

	log.Printf("Kubeconfig saved to %s", configPath)
	log.Printf("You can now use the cluster with: export KUBECONFIG=%s", configPath)
	return nil
}

func (p *Provider) createClusterInfrastructure(ctx context.Context, spec *types.ClusterSpec) (*ClusterInfrastructure, error) {
	log.Println("Creating cluster infrastructure...")

	networkID, err := p.createNetwork(ctx, spec.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create network: %w", err)
	}

	// Create master node
	masterNode, err := p.createNode(ctx, spec.Name, "master-0", networkID, spec.ControlPlane.InstanceType, true, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create master node: %w", err)
	}

	log.Printf("Waiting for master node %s to be ready...", masterNode.Name)
	if err := p.waitForInstanceReady(ctx, masterNode.InstanceID); err != nil {
		return nil, fmt.Errorf("master node failed to become ready: %w", err)
	}
	// Refresh master node info to get IP
	instance, err := p.client.GetInstance(masterNode.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated master instance details: %w", err)
	}
	masterNode.PublicIP = instance.PublicIP
	masterNode.PrivateIP = instance.PrivateIP

	// Wait for the setup script to complete on the master node
	log.Printf("Waiting for setup script to complete on master node %s...", masterNode.Name)
	if err := p.waitForScriptCompletion(ctx, masterNode.PublicIP, masterNode.PrivateKey); err != nil {
		return nil, fmt.Errorf("setup script on master node failed or timed out: %w", err)
	}
	log.Printf("Setup script completed on master node %s.", masterNode.Name)

	// Get K3s join token from master
	log.Println("Retrieving K3s join token from master node...")
	k3sToken, err := p.getK3sJoinToken(masterNode.PublicIP, masterNode.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get k3s join token: %w", err)
	}
	log.Println("Successfully retrieved K3s join token.")

	// Create worker nodes
	var workerNodes []NodeInfo
	for _, nodeGroup := range spec.NodeGroups {
		for i := 0; i < nodeGroup.Replicas; i++ {
			workerName := fmt.Sprintf("%s-%d", nodeGroup.Name, i)
			workerNode, err := p.createNode(ctx, spec.Name, workerName, networkID, nodeGroup.InstanceType, false, masterNode.PrivateIP, k3sToken)
			if err != nil {
				return nil, fmt.Errorf("failed to create worker node %s: %w", workerName, err)
			}
			workerNodes = append(workerNodes, *workerNode)
		}
	}

	for _, wn := range workerNodes {
		log.Printf("Waiting for worker node %s to be ready...", wn.Name)
		if err := p.waitForInstanceReady(ctx, wn.InstanceID); err != nil {
			log.Printf("Warning: worker node %s failed to become ready: %v", wn.Name, err)
		}
	}

	return &ClusterInfrastructure{
		NetworkID:    networkID,
		NetworkLabel: fmt.Sprintf("%s-network", spec.Name),
		MasterNodes:  []NodeInfo{*masterNode},
		WorkerNodes:  workerNodes,
		K3sToken:     k3sToken,
	}, nil
}

func (p *Provider) createNode(ctx context.Context, clusterName, nodeName, networkID, size string, isMaster bool, masterIP, k3sToken string) (*NodeInfo, error) {
	// Generate a unique suffix for the hostname to prevent conflicts
	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random bytes for hostname: %w", err)
	}
	randSuffix := hex.EncodeToString(randBytes)

	instanceName := fmt.Sprintf("%s-%s-%s", clusterName, nodeName, randSuffix)
	log.Printf("Creating instance with unique hostname: %s", instanceName)

	privateKey, publicKey, err := p.generateSSHKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH key: %w", err)
	}

	sshKeyName := fmt.Sprintf("adhar-%s-%d", instanceName, time.Now().Unix())
	sshKeyID, err := p.createSSHKey(sshKeyName, publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH key in Civo: %w", err)
	}

	if err := p.savePrivateKey(instanceName, privateKey); err != nil {
		log.Printf("Warning: failed to save private key: %v", err)
	}

	var script string
	if isMaster {
		script = p.generateMasterSetupScript(publicKey)
	} else {
		script = p.generateWorkerSetupScript(masterIP, k3sToken, publicKey)
	}

	validatedSize, err := p.validateInstanceSize(ctx, size)
	if err != nil {
		return nil, err
	}

	instanceConfig := &civogo.InstanceConfig{
		Hostname:  instanceName,
		Size:      validatedSize,
		Region:    p.config.Region,
		NetworkID: networkID,
		SSHKeyID:  sshKeyID,
		Script:    script,
		Tags:      []string{"adhar-cluster", clusterName},
	}

	instance, err := p.client.CreateInstance(instanceConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create instance: %w", err)
	}

	return &NodeInfo{
		Name:       instanceName,
		InstanceID: instance.ID,
		IsMaster:   isMaster,
		PrivateKey: privateKey,
		PublicKey:  publicKey,
		CreatedAt:  time.Now(),
	}, nil
}

func (p *Provider) generateMasterSetupScript(publicKey string) string {
	return fmt.Sprintf(`#!/bin/bash
set -ex
exec > >(tee /var/log/k8s-setup.log) 2>&1
echo "--- Starting K3s Master setup at $(date) ---"

# --- 1. SSH and Security ---
echo "--- 1. Configuring SSH and Security ---"
mkdir -p /root/.ssh
echo "%s" > /root/.ssh/authorized_keys
chmod 700 /root/.ssh
chmod 600 /root/.ssh/authorized_keys
sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config
sed -i 's/PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
systemctl restart sshd

# --- 2. System Prep ---
echo "--- 2. Preparing System ---"
apt-get update -y
apt-get install -y apt-transport-https ca-certificates curl ufw

# --- 3. Firewall ---
echo "--- 3. Configuring Firewall ---"
ufw --force enable
ufw default deny incoming
ufw default allow outgoing
ufw allow ssh
ufw allow 6443/tcp  # K3s API Server
ufw allow 10250/tcp # Kubelet
ufw allow 8472/udp  # Cilium VXLAN
ufw allow 4240/tcp  # Hubble
ufw allow from 10.0.0.0/8 to any port 6443 proto tcp # Allow internal K3s traffic
ufw allow from 10.0.0.0/8 to any port 10250 proto tcp
ufw allow from 10.0.0.0/8 to any port 2379 proto tcp # etcd
ufw allow from 10.0.0.0/8 to any port 2380 proto tcp # etcd
ufw reload

# --- 4. K3s Installation (Server) ---
echo "--- 4. Installing K3s Server ---"
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--flannel-backend=none --disable-network-policy" sh -s - server --cluster-init

# --- 5. Wait for Kubeconfig and API Server ---
echo "--- 5. Waiting for Kubeconfig file ---"
timeout 120s bash -c 'while [ ! -f /etc/rancher/k3s/k3s.yaml ]; do echo "waiting for kubeconfig..."; sleep 5; done'
chmod 644 /etc/rancher/k3s/k3s.yaml
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
echo "--- Kubeconfig found. Waiting for Node to appear ---"
timeout 180s bash -c 'while ! /usr/local/bin/k3s kubectl get nodes; do sleep 5; done'

# --- 6. Cilium Installation ---
echo "--- 6. Installing Cilium CNI ---"
curl -L --remote-name-all https://github.com/cilium/cilium-cli/releases/latest/download/cilium-linux-amd64.tar.gz{,.sha256sum}
sha256sum --check cilium-linux-amd64.tar.gz.sha256sum
tar -zxvf cilium-linux-amd64.tar.gz -C /usr/local/bin
cilium install --version 1.15.7
echo "--- Waiting for Cilium to be ready ---"
cilium status --wait

# --- 7. Final Verification ---
echo "--- 7. Final Verification ---"
/usr/local/bin/k3s kubectl get nodes -o wide
/usr/local/bin/k3s kubectl get pods -A -o wide

echo "--- K3s Master setup complete at $(date) ---"
touch /var/log/k8s-setup-complete
`, publicKey)
}

func (p *Provider) generateWorkerSetupScript(masterIP, k3sToken, publicKey string) string {
	return fmt.Sprintf(`#!/bin/bash
set -ex
exec > >(tee /var/log/k8s-setup.log) 2>&1
echo "--- Starting K3s Worker setup ---"

# --- SSH and Security ---
mkdir -p /root/.ssh
echo "%s" > /root/.ssh/authorized_keys
chmod 700 /root/.ssh
chmod 600 /root/.ssh/authorized_keys
sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config
sed -i 's/PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
systemctl restart sshd

# --- System Prep ---
apt-get update -y
apt-get install -y apt-transport-https ca-certificates curl ufw

# --- Firewall ---
ufw --force enable
ufw default deny incoming
ufw default allow outgoing
ufw allow ssh
ufw allow 10250/tcp # Kubelet
ufw allow 8472/udp  # Cilium VXLAN
ufw allow from 10.0.0.0/8 to any port 10250 proto tcp
ufw reload

# --- K3s Installation (Agent) ---
curl -sfL https://get.k3s.io | K3S_URL="https://%s:6443" K3S_TOKEN="%s" INSTALL_K3S_EXEC="--flannel-backend=none" sh -
systemctl enable k3s-agent
systemctl start k3s-agent

echo "--- K3s Worker setup complete ---"
touch /var/log/k8s-setup-complete
`, publicKey, masterIP, k3sToken)
}

func (p *Provider) getK3sJoinToken(masterIP, privateKey string) (string, error) {
	var token string
	var err error

	for i := 0; i < 10; i++ {
		token, err = p.runSSHCommand(masterIP, privateKey, "cat /var/lib/rancher/k3s/server/node-token")
		if err == nil && token != "" {
			return strings.TrimSpace(token), nil
		}
		log.Printf("Waiting for K3s join token... (attempt %d/10)", i+1)
		time.Sleep(15 * time.Second)
	}
	return "", fmt.Errorf("failed to get k3s join token after multiple attempts: %w", err)
}

func (p *Provider) runSSHCommand(host, privateKey, command string) (string, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", host), config)
	if err != nil {
		return "", fmt.Errorf("failed to dial ssh: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(command); err != nil {
		return "", fmt.Errorf("failed to run command '%s': %w, stderr: %s", command, err, stderr.String())
	}

	return stdout.String(), nil
}
func (p *Provider) waitForClusterReady(ctx context.Context, masterIP, privateKey string) (string, error) {
	log.Printf("Waiting for cluster to be ready at %s", masterIP)
	timeout := time.After(15 * time.Minute)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var kubeconfig string
	var err error

	// First, wait for the kubeconfig file to be created
	for {
		select {
		case <-timeout:
			return "", fmt.Errorf("timeout waiting for kubeconfig file")
		case <-ticker.C:
			kubeconfig, err = p.runSSHCommand(masterIP, privateKey, "cat /etc/rancher/k3s/k3s.yaml")
			if err == nil && kubeconfig != "" {
				goto KubeconfigReady
			}
			log.Println("Waiting for kubeconfig to be generated on master node...")
		}
	}

KubeconfigReady:
	log.Println("Kubeconfig retrieved. Now verifying cluster connectivity.")

	// Now use the kubeconfig to check for node readiness
	for {
		select {
		case <-timeout:
			return "", fmt.Errorf("timeout waiting for cluster to become ready")
		case <-ticker.C:
			// Create a temporary file for the kubeconfig
			tmpfile, err := os.CreateTemp("", "kubeconfig-")
			if err != nil {
				log.Printf("Error creating temp kubeconfig file: %v", err)
				continue
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write([]byte(kubeconfig)); err != nil {
				log.Printf("Error writing to temp kubeconfig file: %v", err)
				tmpfile.Close()
				continue
			}
			tmpfile.Close()

			// Use kubectl to check cluster status
			cmd := exec.Command("kubectl", "--kubeconfig", tmpfile.Name(), "get", "nodes")
			output, err := cmd.CombinedOutput()
			if err == nil {
				log.Println("`kubectl get nodes` successful. Cluster is ready.")
				log.Printf("Nodes:\n%s", string(output))
				return kubeconfig, nil
			}
			log.Printf("`kubectl get nodes` failed, cluster not ready yet. Error: %v. Output:\n%s", err, string(output))
		}
	}
}

func (p *Provider) waitForScriptCompletion(ctx context.Context, masterIP, privateKey string) error {
	log.Printf("Waiting for setup script to complete on %s...", masterIP)
	timeout := time.After(10 * time.Minute)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for script to complete on %s", masterIP)
		case <-ticker.C:
			// Check for the existence of the completion file
			_, err := p.runSSHCommand(masterIP, privateKey, "test -f /var/log/k8s-setup-complete")
			if err == nil {
				log.Println("Setup script completion signal found.")
				return nil
			}
			log.Println("Setup script still running...")
		}
	}
}

func (p *Provider) waitForInstanceReady(ctx context.Context, instanceID string) error {
	log.Printf("Waiting for instance %s to become active...", instanceID)
	timeout := time.After(10 * time.Minute)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for instance %s to become active", instanceID)
		case <-ticker.C:
			instance, err := p.client.GetInstance(instanceID)
			if err != nil {
				log.Printf("Error checking instance status: %v", err)
				continue
			}
			if instance.Status == "ACTIVE" {
				log.Printf("Instance %s is ACTIVE.", instanceID)
				return nil
			}
			log.Printf("Instance %s status: %s", instanceID, instance.Status)
		}
	}
}

func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	log.Printf("Deleting Civo cluster: %s", clusterID)
	clusterName := strings.TrimPrefix(clusterID, "civo-")

	// Find all instances associated with the cluster
	allInstances, err := p.client.ListAllInstances()
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	var instancesToDelete []string
	for _, instance := range allInstances {
		if strings.HasPrefix(instance.Hostname, clusterName) {
			instancesToDelete = append(instancesToDelete, instance.ID)
		}
	}

	if len(instancesToDelete) == 0 {
		log.Printf("No instances found for cluster %s.", clusterName)
	} else {
		for _, instanceID := range instancesToDelete {
			log.Printf("Deleting instance %s", instanceID)
			_, err := p.client.DeleteInstance(instanceID)
			if err != nil {
				log.Printf("Warning: failed to delete instance %s: %v", instanceID, err)
			}
		}
	}

	// Delete network
	networks, err := p.client.ListNetworks()
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}
	for _, network := range networks {
		if network.Label == fmt.Sprintf("%s-network", clusterName) {
			log.Printf("Deleting network %s", network.ID)
			// Wait a bit before deleting the network
			time.Sleep(15 * time.Second)
			_, err := p.client.DeleteNetwork(network.ID)
			if err != nil {
				log.Printf("Warning: failed to delete network %s: %v", network.ID, err)
			}
			break
		}
	}

	// Clean up local state
	delete(p.clusters, clusterID)
	delete(p.resourceTrackers, clusterID)
	p.clearClustersFromCache(clusterID)

	log.Printf("Successfully initiated deletion of cluster %s", clusterID)
	return nil
}

func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	log.Printf("Attempting to get kubeconfig for cluster: %s", clusterID)

	cluster, ok := p.clusters[clusterID]
	if !ok {
		return "", fmt.Errorf("cluster %s not found in provider cache", clusterID)
	}

	log.Println("Cluster found in cache. Checking for master node details in metadata...")

	masterIP, ok := cluster.Metadata["masterPublicIP"].(string)
	if !ok || masterIP == "" {
		return "", fmt.Errorf("masterPublicIP not found or is empty in cluster metadata for %s", clusterID)
	}

	privateKey, ok := cluster.Metadata["masterPrivateKey"].(string)
	if !ok || privateKey == "" {
		return "", fmt.Errorf("masterPrivateKey not found or is empty in cluster metadata for %s", clusterID)
	}

	log.Printf("Found master node details: IP=%s", masterIP)
	log.Println("Attempting to retrieve kubeconfig via SSH...")

	kubeconfig, err := p.runSSHCommand(masterIP, privateKey, "cat /etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to retrieve kubeconfig from master node %s: %w", masterIP, err)
	}

	log.Println("Successfully retrieved kubeconfig from master node.")

	// Replace the server address from 127.0.0.1 to the master's public IP
	kubeconfig = strings.Replace(kubeconfig, "127.0.0.1", masterIP, 1)
	log.Printf("Updated kubeconfig to use master public IP: %s", masterIP)

	// Validate the final kubeconfig
	if _, err := clientcmd.Load([]byte(kubeconfig)); err != nil {
		return "", fmt.Errorf("retrieved kubeconfig is invalid: %w", err)
	}
	log.Println("Kubeconfig validation successful.")

	return kubeconfig, nil
}

// Unimplemented methods
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	return fmt.Errorf("UpdateCluster not implemented for Civo")
}

func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	if cluster, exists := p.clusters[clusterID]; exists {
		return cluster, nil
	}
	return nil, fmt.Errorf("cluster %s not found", clusterID)
}

func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	// For now, returns clusters created in the current session.
	// A real implementation would discover clusters from the Civo API.
	var clusters []*types.Cluster
	for _, c := range p.clusters {
		clusters = append(clusters, c)
	}
	return clusters, nil
}

// Helper methods
func (p *Provider) validateClusterSpec(spec *types.ClusterSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("cluster name is required")
	}
	if spec.ControlPlane.Replicas != 1 {
		return fmt.Errorf("Civo provider currently supports single-master clusters only")
	}
	return nil
}

func (p *Provider) generateSSHKey() (string, string, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	pub, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicKey := ssh.MarshalAuthorizedKey(pub)
	return string(privateKeyPEM), string(publicKey), nil
}

func (p *Provider) createSSHKey(name, publicKey string) (string, error) {
	sshKey, err := p.client.NewSSHKey(name, publicKey)
	if err != nil {
		return "", err
	}
	return sshKey.ID, nil
}

func (p *Provider) savePrivateKey(instanceName, privateKey string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	keysDir := filepath.Join(homeDir, ".adhar", "keys")
	os.MkdirAll(keysDir, 0700)
	keyPath := filepath.Join(keysDir, fmt.Sprintf("%s.pem", instanceName))
	log.Printf("SSH private key saved to %s for debugging.", keyPath)
	return os.WriteFile(keyPath, []byte(privateKey), 0600)
}

func (p *Provider) createNetwork(ctx context.Context, clusterName string) (string, error) {
	networkName := fmt.Sprintf("%s-network", clusterName)
	log.Printf("Creating network: %s", networkName)
	network, err := p.client.NewNetwork(networkName)
	if err != nil {
		// If network exists, try to find and use it
		if strings.Contains(err.Error(), "already exists") {
			networks, listErr := p.client.ListNetworks()
			if listErr != nil {
				return "", fmt.Errorf("network exists but failed to list networks: %w", listErr)
			}
			for _, n := range networks {
				if n.Label == networkName {
					log.Printf("Found existing network: %s", n.ID)
					return n.ID, nil
				}
			}
		}
		return "", fmt.Errorf("failed to create network: %w", err)
	}
	log.Printf("Created network: %s (ID: %s)", networkName, network.ID)
	return network.ID, nil
}

func (p *Provider) validateInstanceSize(ctx context.Context, size string) (string, error) {
	sizes, err := p.client.ListInstanceSizes()
	if err != nil {
		log.Printf("Warning: failed to list available sizes, using provided size '%s': %v", size, err)
		return size, nil
	}
	for _, s := range sizes {
		if s.Name == size {
			return size, nil
		}
	}
	log.Printf("Invalid size '%s'. Defaulting to '%s'", size, p.config.Size)
	return p.config.Size, nil
}

func (p *Provider) saveClustersToCache() {
	cacheFile := fmt.Sprintf("/tmp/adhar-civo-clusters-%s.json", p.config.Region)
	var clusters []*types.Cluster
	for _, cluster := range p.clusters {
		clusters = append(clusters, cluster)
	}
	if data, err := json.Marshal(clusters); err == nil {
		os.WriteFile(cacheFile, data, 0644)
	}
}

func (p *Provider) clearClustersFromCache(clusterID string) {
	cacheFile := fmt.Sprintf("/tmp/adhar-civo-clusters-%s.json", p.config.Region)
	if data, err := os.ReadFile(cacheFile); err == nil {
		var cachedClusters []*types.Cluster
		if json.Unmarshal(data, &cachedClusters) == nil {
			var updatedClusters []*types.Cluster
			for _, c := range cachedClusters {
				if c.ID != clusterID {
					updatedClusters = append(updatedClusters, c)
				}
			}
			if updatedData, err := json.Marshal(updatedClusters); err == nil {
				os.WriteFile(cacheFile, updatedData, 0644)
			}
		}
	}
}

// Other interface methods (mostly unimplemented for Civo VM provider)
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, spec *types.NodeGroupSpec) (*types.NodeGroup, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID, nodeGroupName string) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID, nodeGroupName string, replicas int) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID, nodeGroupName string) (*types.NodeGroup, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) ListVPCs(ctx context.Context) ([]*types.VPC, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) ListLoadBalancers(ctx context.Context) ([]*types.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) UpgradeCluster(ctx context.Context, clusterID string, version string) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	return fmt.Errorf("not implemented")
}
func (p *Provider) ListAddons(ctx context.Context, clusterID string) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	return 0, fmt.Errorf("not implemented")
}
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	return fmt.Errorf("not implemented")
}
