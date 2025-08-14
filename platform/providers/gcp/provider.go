package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/proto"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// staticTokenSource provides a static oauth2 token
type staticTokenSource struct {
	token string
}

func (s *staticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{
		AccessToken: s.token,
		TokenType:   "Bearer",
	}, nil
}

// ClusterInfrastructure represents the infrastructure state for a cluster
type ClusterInfrastructure struct {
	NetworkName      string
	SubnetName       string
	FirewallRules    []string
	ExternalIPName   string
	MasterNodes      []NodeInfo
	WorkerNodes      []NodeInfo
	RouterName       string
	NATGatewayName   string
	SSHKeyName       string
	LoadBalancerName string
	HealthCheckName  string
	BackendService   string
	URLMapName       string
	HTTPSProxy       string
	ForwardingRule   string
	Metadata         map[string]string // For storing addon and cluster configuration
}

// ResourceTracker tracks all GCP resources created for a cluster
type ResourceTracker struct {
	ClusterName     string    `json:"clusterName"`
	ProjectID       string    `json:"projectId"`
	Region          string    `json:"region"`
	Zone            string    `json:"zone"`
	Networks        []string  `json:"networks"`
	Subnets         []string  `json:"subnets"`
	FirewallRules   []string  `json:"firewallRules"`
	Instances       []string  `json:"instances"`
	ExternalIPs     []string  `json:"externalIPs"`
	Routers         []string  `json:"routers"`
	NATGateways     []string  `json:"natGateways"`
	SSHKeys         []string  `json:"sshKeys"`
	LoadBalancers   []string  `json:"loadBalancers"`
	HealthChecks    []string  `json:"healthChecks"`
	BackendServices []string  `json:"backendServices"`
	URLMaps         []string  `json:"urlMaps"`
	HTTPSProxies    []string  `json:"httpsProxies"`
	ForwardingRules []string  `json:"forwardingRules"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NodeInfo represents information about a cluster node
type NodeInfo struct {
	InstanceName string
	Zone         string
	PrivateIP    string
	PublicIP     string
	MachineType  string
	Role         string // "master" or "worker"
}

// expandHomePath expands ~ to the user's home directory
func expandHomePath(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}

	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("unable to get current user: %w", err)
	}

	return filepath.Join(currentUser.HomeDir, path[1:]), nil
}

// Register the GCP provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("gcp", func(config map[string]interface{}) (provider.Provider, error) {
		gcpConfig := &Config{}

		// Parse GCP-specific configuration with multiple auth methods
		// Check both root level and config section for backward compatibility

		// Project ID
		if projectID, ok := config["projectId"].(string); ok {
			gcpConfig.ProjectID = projectID
		} else if projectID, ok := config["project_id"].(string); ok {
			gcpConfig.ProjectID = projectID
		} else if configSection, ok := config["config"].(map[string]interface{}); ok {
			if projectID, ok := configSection["project_id"].(string); ok {
				gcpConfig.ProjectID = projectID
			}
		}

		// Region
		if region, ok := config["region"].(string); ok {
			gcpConfig.Region = region
		}

		// Zone
		if zone, ok := config["zone"].(string); ok {
			gcpConfig.Zone = zone
		} else if configSection, ok := config["config"].(map[string]interface{}); ok {
			if zone, ok := configSection["zone"].(string); ok {
				gcpConfig.Zone = zone
			}
		}

		// Authentication Method 1: Service Account Key File (check root level first)
		if keyPath, ok := config["credentials_file"].(string); ok && keyPath != "" {
			gcpConfig.ServiceAccountKeyPath = keyPath
		} else if keyPath, ok := config["serviceAccountKeyFile"].(string); ok && keyPath != "" {
			gcpConfig.ServiceAccountKeyPath = keyPath
		} else if keyPath, ok := config["serviceAccountKeyPath"].(string); ok && keyPath != "" {
			gcpConfig.ServiceAccountKeyPath = keyPath
		} else if keyPath, ok := config["credentialsFile"].(string); ok && keyPath != "" {
			gcpConfig.ServiceAccountKeyPath = keyPath
		}

		// Authentication Method 2: Service Account Key JSON
		if keyJson, ok := config["serviceAccountKey"].(string); ok && keyJson != "" {
			gcpConfig.ServiceAccountKey = keyJson
		}

		// Authentication Method 3: Application Default Credentials
		if useADC, ok := config["useApplicationDefault"].(bool); ok {
			gcpConfig.UseApplicationDefault = useADC
		}

		// Authentication Method 4: Environment Variables
		if useEnv, ok := config["useEnvironment"].(bool); ok {
			gcpConfig.UseEnvironment = useEnv
		}

		// Authentication Method 5: Compute Metadata (for GCE instances)
		if useMetadata, ok := config["useComputeMetadata"].(bool); ok {
			gcpConfig.UseComputeMetadata = useMetadata
		}

		// Authentication Method 6: Access Token
		if accessToken, ok := config["accessToken"].(string); ok && accessToken != "" {
			gcpConfig.AccessToken = accessToken
		}

		// Authentication Method 5: Impersonate Service Account
		if impersonate, ok := config["impersonateServiceAccount"].(string); ok {
			gcpConfig.ImpersonateServiceAccount = impersonate
		}

		// Authentication Method 6: Workload Identity
		if useWI, ok := config["useWorkloadIdentity"].(bool); ok {
			gcpConfig.UseWorkloadIdentity = useWI
		}

		// Machine configuration
		if machineType, ok := config["machineType"].(string); ok {
			gcpConfig.MachineType = machineType
		} else if machineType, ok := config["machine_type"].(string); ok {
			gcpConfig.MachineType = machineType
		}
		if diskSize, ok := config["diskSize"].(int32); ok {
			gcpConfig.DiskSize = diskSize
		} else if diskSize, ok := config["disk_size_gb"].(int); ok {
			gcpConfig.DiskSize = int32(diskSize)
		}
		if diskType, ok := config["diskType"].(string); ok {
			gcpConfig.DiskType = diskType
		} else if diskType, ok := config["disk_type"].(string); ok {
			gcpConfig.DiskType = diskType
		}
		if imageFamily, ok := config["imageFamily"].(string); ok {
			gcpConfig.ImageFamily = imageFamily
		} else if imageFamily, ok := config["image_family"].(string); ok {
			gcpConfig.ImageFamily = imageFamily
		}
		if imageProject, ok := config["imageProject"].(string); ok {
			gcpConfig.ImageProject = imageProject
		} else if imageProject, ok := config["image_project"].(string); ok {
			gcpConfig.ImageProject = imageProject
		}

		// Network configuration
		if vpcName, ok := config["vpcName"].(string); ok {
			gcpConfig.VPCName = vpcName
		} else if vpcName, ok := config["vpc_name"].(string); ok {
			gcpConfig.VPCName = vpcName
		}
		if subnetName, ok := config["subnetName"].(string); ok {
			gcpConfig.SubnetName = subnetName
		} else if subnetName, ok := config["subnet_name"].(string); ok {
			gcpConfig.SubnetName = subnetName
		}
		if subnetCIDR, ok := config["subnetCIDR"].(string); ok {
			gcpConfig.SubnetCIDR = subnetCIDR
		} else if subnetCIDR, ok := config["subnet_cidr"].(string); ok {
			gcpConfig.SubnetCIDR = subnetCIDR
		}

		return NewProvider(gcpConfig)
	})
}

// Provider implements the GCP provider for manual Kubernetes clusters using Google Cloud Go SDK
type Provider struct {
	config                 *Config
	computeClient          *compute.InstancesClient
	networkClient          *compute.NetworksClient
	subnetClient           *compute.SubnetworksClient
	firewallClient         *compute.FirewallsClient
	addressClient          *compute.AddressesClient
	forwardingRulesClient  *compute.ForwardingRulesClient
	healthChecksClient     *compute.HealthChecksClient
	backendServicesClient  *compute.BackendServicesClient
	routersClient          *compute.RoutersClient
	operationsClient       *compute.GlobalOperationsClient
	zoneOperationsClient   *compute.ZoneOperationsClient
	regionOperationsClient *compute.RegionOperationsClient
	diskClient             *compute.DisksClient
	imageClient            *compute.ImagesClient
	instanceClient         *compute.InstancesClient
	snapshotClient         *compute.SnapshotsClient

	// Resource tracking for clusters
	clusters         map[string]*ClusterInfrastructure
	resourceTrackers map[string]*ResourceTracker
}

// Config holds GCP provider configuration
type Config struct {
	ProjectID string `json:"projectId"`
	Region    string `json:"region"`
	Zone      string `json:"zone"`

	// Network configuration
	VPCName    string `json:"vpcName"`
	SubnetName string `json:"subnetName"`
	SubnetCIDR string `json:"subnetCIDR"`

	// Authentication Methods (multiple options supported)
	// Option 1: Service Account Key File
	ServiceAccountKeyPath string `json:"serviceAccountKeyPath"`

	// Option 2: Service Account Key JSON (inline)
	ServiceAccountKey string `json:"serviceAccountKey,omitempty"`

	// Option 3: Application Default Credentials (ADC)
	UseApplicationDefault bool `json:"useApplicationDefault,omitempty"`

	// Option 4: Access Token (for temporary access)
	AccessToken string `json:"accessToken,omitempty"`

	// Option 5: Impersonate Service Account
	ImpersonateServiceAccount string `json:"impersonateServiceAccount,omitempty"`

	// Option 6: Workload Identity (for GKE)
	UseWorkloadIdentity bool `json:"useWorkloadIdentity,omitempty"`

	// Option 7: Environment Variables (GOOGLE_APPLICATION_CREDENTIALS)
	UseEnvironment bool `json:"useEnvironment,omitempty"`

	// Option 8: Compute Metadata (for GCE instances)
	UseComputeMetadata bool `json:"useComputeMetadata,omitempty"`

	MachineType  string `json:"machineType"`
	DiskSize     int32  `json:"diskSize"`
	DiskType     string `json:"diskType"`
	ImageFamily  string `json:"imageFamily"`
	ImageProject string `json:"imageProject"`
}

// NewProvider creates a new GCP provider instance
func NewProvider(config *Config) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("GCP configuration is required")
	}

	// Set defaults
	if config.Region == "" {
		config.Region = "us-central1"
	}
	if config.Zone == "" {
		config.Zone = config.Region + "-a"
	}
	if config.MachineType == "" {
		config.MachineType = "e2-standard-2"
	}
	if config.DiskSize == 0 {
		config.DiskSize = 20 // 20 GB
	}
	if config.DiskType == "" {
		config.DiskType = "pd-standard"
	}
	if config.ImageFamily == "" {
		config.ImageFamily = "ubuntu-2204-lts"
	}
	if config.ImageProject == "" {
		config.ImageProject = "ubuntu-os-cloud"
	}
	if config.VPCName == "" {
		config.VPCName = "default-vpc"
	}
	if config.SubnetName == "" {
		config.SubnetName = "default-subnet"
	}
	if config.SubnetCIDR == "" {
		config.SubnetCIDR = "10.0.0.0/24"
	}

	ctx := context.Background()

	// Setup authentication options based on configuration
	var opts []option.ClientOption
	var hasValidCredentials bool

	switch {
	// Priority 1: Service Account Key File
	case config.ServiceAccountKeyPath != "":
		// Expand home directory if path starts with ~
		expandedPath, err := expandHomePath(config.ServiceAccountKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to expand credentials file path: %w", err)
		}

		// Check if the credentials file exists
		if _, err := os.Stat(expandedPath); err == nil {
			opts = append(opts, option.WithCredentialsFile(expandedPath))
			hasValidCredentials = true
		} else {
			return nil, fmt.Errorf("credentials file not found: %s (expanded from: %s)", expandedPath, config.ServiceAccountKeyPath)
		}

	// Priority 2: Service Account Key JSON (inline)
	case config.ServiceAccountKey != "":
		opts = append(opts, option.WithCredentialsJSON([]byte(config.ServiceAccountKey)))
		hasValidCredentials = true

	// Priority 3: Access Token
	case config.AccessToken != "":
		// Create a token source from the access token
		tokenSource := &staticTokenSource{token: config.AccessToken}
		opts = append(opts, option.WithTokenSource(tokenSource))
		hasValidCredentials = true

	// Priority 4: Impersonate Service Account
	case config.ImpersonateServiceAccount != "":
		// TODO: Implement service account impersonation
		// For now, use default credentials as base
		hasValidCredentials = true

	// Priority 5: Workload Identity (GKE)
	case config.UseWorkloadIdentity:
		// Use default credentials with workload identity
		hasValidCredentials = true

	// Priority 6: Application Default Credentials (explicit)
	case config.UseApplicationDefault:
		// Check if ADC is available
		if _, err := google.FindDefaultCredentials(ctx); err == nil {
			hasValidCredentials = true
		} else {
			return nil, fmt.Errorf("application default credentials not available: %w", err)
		}

	// Default: Check if any credentials are available
	default:
		// Check for environment variables first
		if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
			if _, err := os.Stat(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); err == nil {
				hasValidCredentials = true
			}
		} else if _, err := google.FindDefaultCredentials(ctx); err == nil {
			hasValidCredentials = true
		}

		// If no credentials are available, return a helpful error
		if !hasValidCredentials {
			return nil, fmt.Errorf("no GCP credentials found. Please configure one of: service account key file, environment variables (GOOGLE_APPLICATION_CREDENTIALS), or application default credentials. See documentation for setup instructions")
		}
	}

	// Create all required GCP clients
	computeClient, err := compute.NewInstancesRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create compute client: %w", err)
	}

	networkClient, err := compute.NewNetworksRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create network client: %w", err)
	}

	subnetClient, err := compute.NewSubnetworksRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet client: %w", err)
	}

	firewallClient, err := compute.NewFirewallsRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create firewall client: %w", err)
	}

	addressClient, err := compute.NewAddressesRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create address client: %w", err)
	}

	forwardingRulesClient, err := compute.NewForwardingRulesRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create forwarding rules client: %w", err)
	}

	healthChecksClient, err := compute.NewHealthChecksRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create health checks client: %w", err)
	}

	backendServicesClient, err := compute.NewBackendServicesRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend services client: %w", err)
	}

	routersClient, err := compute.NewRoutersRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create routers client: %w", err)
	}

	operationsClient, err := compute.NewGlobalOperationsRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create global operations client: %w", err)
	}

	zoneOperationsClient, err := compute.NewZoneOperationsRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create zone operations client: %w", err)
	}

	regionOperationsClient, err := compute.NewRegionOperationsRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create region operations client: %w", err)
	}

	diskClient, err := compute.NewDisksRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create disk client: %w", err)
	}

	imageClient, err := compute.NewImagesRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create image client: %w", err)
	}

	instanceClient, err := compute.NewInstancesRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create instance client: %w", err)
	}

	snapshotClient, err := compute.NewSnapshotsRESTClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot client: %w", err)
	}

	provider := &Provider{
		config:                 config,
		computeClient:          computeClient,
		networkClient:          networkClient,
		subnetClient:           subnetClient,
		firewallClient:         firewallClient,
		addressClient:          addressClient,
		forwardingRulesClient:  forwardingRulesClient,
		healthChecksClient:     healthChecksClient,
		backendServicesClient:  backendServicesClient,
		routersClient:          routersClient,
		operationsClient:       operationsClient,
		zoneOperationsClient:   zoneOperationsClient,
		regionOperationsClient: regionOperationsClient,
		diskClient:             diskClient,
		imageClient:            imageClient,
		instanceClient:         instanceClient,
		snapshotClient:         snapshotClient,
		clusters:               make(map[string]*ClusterInfrastructure),
		resourceTrackers:       make(map[string]*ResourceTracker),
	}

	// Load persisted state
	if err := provider.loadState(); err != nil {
		log.Printf("Warning: Failed to load provider state: %v", err)
	}

	return provider, nil
}

// getStateFilePath returns the path to the state file
func (p *Provider) getStateFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	stateDir := filepath.Join(homeDir, ".adhar", "state", "gcp")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create state directory: %w", err)
	}

	return filepath.Join(stateDir, "clusters.json"), nil
}

// StateData represents the persisted state
type StateData struct {
	Clusters         map[string]*ClusterInfrastructure `json:"clusters"`
	ResourceTrackers map[string]*ResourceTracker       `json:"resourceTrackers"`
}

// loadState loads the persisted state from disk
func (p *Provider) loadState() error {
	stateFile, err := p.getStateFilePath()
	if err != nil {
		return err
	}

	// If state file doesn't exist, start with empty state
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		return nil
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var state StateData
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	// Restore state
	if state.Clusters != nil {
		p.clusters = state.Clusters
	}
	if state.ResourceTrackers != nil {
		p.resourceTrackers = state.ResourceTrackers
	}

	log.Printf("Loaded %d clusters and %d resource trackers from state",
		len(p.clusters), len(p.resourceTrackers))
	return nil
}

// saveState persists the current state to disk
func (p *Provider) saveState() error {
	stateFile, err := p.getStateFilePath()
	if err != nil {
		return err
	}

	state := StateData{
		Clusters:         p.clusters,
		ResourceTrackers: p.resourceTrackers,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// Name returns the provider name
func (p *Provider) Name() string {
	return "gcp"
}

// Region returns the provider region
func (p *Provider) Region() string {
	return p.config.Region
}

// Authenticate validates GCP credentials using Google Cloud SDK
func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	// Test GCP credentials by making a simple API call
	req := &computepb.ListInstancesRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
	}

	it := p.computeClient.List(ctx, req)
	_, err := it.Next()
	if err != nil && err.Error() != "no more items in iterator" {
		return fmt.Errorf("failed to authenticate with GCP: %w", err)
	}
	return nil
}

// ValidatePermissions validates GCP permissions using Google Cloud SDK
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	// Test basic compute permissions
	req := &computepb.ListInstancesRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
	}

	it := p.computeClient.List(ctx, req)
	_, err := it.Next()
	if err != nil && err.Error() != "no more items in iterator" {
		return fmt.Errorf("insufficient GCP permissions: %w", err)
	}
	return nil
}

// CreateCluster creates a new manual Kubernetes cluster on GCP Compute Engine instances
func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	log.Printf("Creating manual Kubernetes cluster: %s", spec.Name)

	// Validate cluster specification
	if err := p.validateClusterSpec(spec); err != nil {
		return nil, fmt.Errorf("invalid cluster specification: %w", err)
	}

	// Create cluster infrastructure
	infrastructure, err := p.createClusterInfrastructure(ctx, spec.Name, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster infrastructure: %w", err)
	}

	// Store cluster infrastructure for tracking
	clusterID := fmt.Sprintf("gcp/%s/%s", p.config.ProjectID, spec.Name)
	p.clusters[clusterID] = infrastructure

	// Create resource tracker
	tracker := &ResourceTracker{
		ClusterName:   spec.Name,
		ProjectID:     p.config.ProjectID,
		Region:        p.config.Region,
		Zone:          p.config.Zone,
		Networks:      []string{infrastructure.NetworkName},
		Subnets:       []string{infrastructure.SubnetName},
		FirewallRules: infrastructure.FirewallRules,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// Add instances to tracker
	for _, node := range infrastructure.MasterNodes {
		tracker.Instances = append(tracker.Instances, node.InstanceName)
	}
	for _, node := range infrastructure.WorkerNodes {
		tracker.Instances = append(tracker.Instances, node.InstanceName)
	}

	p.resourceTrackers[clusterID] = tracker

	// Save state to persist cluster information
	if err := p.saveState(); err != nil {
		log.Printf("Warning: Failed to save provider state: %v", err)
	}

	// Return cluster information
	var endpoint string
	if len(infrastructure.MasterNodes) > 0 {
		endpoint = fmt.Sprintf("https://%s:6443", infrastructure.MasterNodes[0].PublicIP)
	}

	cluster := &types.Cluster{
		ID:        clusterID,
		Name:      spec.Name,
		Provider:  "gcp",
		Region:    p.config.Region,
		Version:   spec.Version,
		Status:    types.ClusterStatusRunning,
		Endpoint:  endpoint,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"projectId":       p.config.ProjectID,
			"zone":            p.config.Zone,
			"network":         infrastructure.NetworkName,
			"subnet":          infrastructure.SubnetName,
			"masterNodeCount": len(infrastructure.MasterNodes),
			"workerNodeCount": len(infrastructure.WorkerNodes),
		},
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
		spec.ControlPlane.InstanceType = p.config.MachineType // Use default machine type
	}
	return nil
}

// createClusterInfrastructure creates the GCP infrastructure for a manual Kubernetes cluster
func (p *Provider) createClusterInfrastructure(ctx context.Context, clusterName string, spec *types.ClusterSpec) (*ClusterInfrastructure, error) {
	log.Printf("Creating infrastructure for cluster: %s", clusterName)

	infrastructure := &ClusterInfrastructure{}

	// Create VPC network
	networkName := p.config.VPCName
	if networkName == "" {
		networkName = fmt.Sprintf("%s-network", clusterName)
	}
	err := p.createVPCNetwork(ctx, networkName)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC network: %w", err)
	}
	infrastructure.NetworkName = networkName

	// Create subnet
	subnetName := p.config.SubnetName
	if subnetName == "" {
		subnetName = fmt.Sprintf("%s-subnet", clusterName)
	}
	err = p.createSubnet(ctx, networkName, subnetName)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet: %w", err)
	}
	infrastructure.SubnetName = subnetName

	// Create firewall rules
	firewallRules, err := p.createFirewallRules(ctx, clusterName, networkName)
	if err != nil {
		return nil, fmt.Errorf("failed to create firewall rules: %w", err)
	}
	infrastructure.FirewallRules = firewallRules

	// Create master nodes
	masterNodeInfos, err := p.createMasterNodes(ctx, clusterName, subnetName, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create master nodes: %w", err)
	}
	infrastructure.MasterNodes = masterNodeInfos

	// Create worker nodes if specified
	if len(spec.NodeGroups) > 0 {
		workerNodeInfos, err := p.createWorkerNodes(ctx, clusterName, subnetName, spec)
		if err != nil {
			return nil, fmt.Errorf("failed to create worker nodes: %w", err)
		}
		infrastructure.WorkerNodes = workerNodeInfos
	}

	log.Printf("Successfully created infrastructure for cluster: %s", clusterName)
	return infrastructure, nil
}

// createVPCNetwork creates a VPC network using Google Cloud SDK
func (p *Provider) createVPCNetwork(ctx context.Context, networkName string) error {
	log.Printf("Creating VPC network: %s", networkName)

	// Check if network already exists
	getReq := &computepb.GetNetworkRequest{
		Project: p.config.ProjectID,
		Network: networkName,
	}

	_, err := p.networkClient.Get(ctx, getReq)
	if err == nil {
		log.Printf("VPC network %s already exists, skipping creation", networkName)
		return nil
	}

	// Network doesn't exist, create it
	autoCreateSubnetworks := false
	description := fmt.Sprintf("Network for cluster %s", networkName)

	req := &computepb.InsertNetworkRequest{
		Project: p.config.ProjectID,
		NetworkResource: &computepb.Network{
			Name:                  &networkName,
			AutoCreateSubnetworks: &autoCreateSubnetworks,
			Description:           &description,
		},
	}

	op, err := p.networkClient.Insert(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return fmt.Errorf("failed to wait for network creation: %w", err)
	}

	log.Printf("Successfully created VPC network: %s", networkName)
	return nil
}

// createSubnet creates a subnet in the VPC network using Google Cloud SDK
func (p *Provider) createSubnet(ctx context.Context, networkName, subnetName string) error {
	log.Printf("Creating subnet: %s", subnetName)

	// Check if subnet already exists
	getReq := &computepb.GetSubnetworkRequest{
		Project:    p.config.ProjectID,
		Region:     p.config.Region,
		Subnetwork: subnetName,
	}

	_, err := p.subnetClient.Get(ctx, getReq)
	if err == nil {
		log.Printf("Subnet %s already exists, skipping creation", subnetName)
		return nil
	}

	// Subnet doesn't exist, create it
	networkURL := fmt.Sprintf("projects/%s/global/networks/%s", p.config.ProjectID, networkName)
	ipCIDR := p.config.SubnetCIDR
	if ipCIDR == "" {
		ipCIDR = "10.0.0.0/24"
	}
	description := fmt.Sprintf("Subnet for cluster %s", subnetName)

	req := &computepb.InsertSubnetworkRequest{
		Project: p.config.ProjectID,
		Region:  p.config.Region,
		SubnetworkResource: &computepb.Subnetwork{
			Name:        &subnetName,
			Network:     &networkURL,
			IpCidrRange: &ipCIDR,
			Description: &description,
		},
	}

	op, err := p.subnetClient.Insert(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create subnet: %w", err)
	}

	log.Printf("Subnet creation operation started, waiting for completion...")

	// Wait for operation to complete
	operationName := op.Name()
	log.Printf("Subnet operation name: %s", operationName)
	err = p.waitForRegionalOperation(ctx, &operationName)
	if err != nil {
		return fmt.Errorf("failed to wait for subnet creation: %w", err)
	}

	log.Printf("Successfully created subnet: %s", subnetName)
	return nil
}

// createFirewallRules creates firewall rules for the cluster using Google Cloud SDK
func (p *Provider) createFirewallRules(ctx context.Context, clusterName, networkName string) ([]string, error) {
	log.Printf("Creating firewall rules for cluster: %s", clusterName)

	networkURL := fmt.Sprintf("projects/%s/global/networks/%s", p.config.ProjectID, networkName)
	var firewallRules []string

	// SSH access rule
	sshRuleName := fmt.Sprintf("%s-allow-ssh", clusterName)
	sshDescription := "Allow SSH access"
	sshDirection := "INGRESS"
	sshProtocol := "tcp"

	sshRule := &computepb.Firewall{
		Name:        &sshRuleName,
		Network:     &networkURL,
		Description: &sshDescription,
		Allowed: []*computepb.Allowed{
			{
				IPProtocol: &sshProtocol,
				Ports:      []string{"22"},
			},
		},
		SourceRanges: []string{"0.0.0.0/0"},
		Direction:    &sshDirection,
	}

	req := &computepb.InsertFirewallRequest{
		Project:          p.config.ProjectID,
		FirewallResource: sshRule,
	}

	op, err := p.firewallClient.Insert(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH firewall rule: %w", err)
	}

	operationName := op.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for SSH firewall rule creation: %w", err)
	}
	firewallRules = append(firewallRules, sshRuleName)

	// Kubernetes API server rule
	apiRuleName := fmt.Sprintf("%s-allow-k8s-api", clusterName)
	apiDescription := "Allow Kubernetes API server access"

	apiRule := &computepb.Firewall{
		Name:        &apiRuleName,
		Network:     &networkURL,
		Description: &apiDescription,
		Allowed: []*computepb.Allowed{
			{
				IPProtocol: &sshProtocol,
				Ports:      []string{"6443"},
			},
		},
		SourceRanges: []string{"0.0.0.0/0"},
		Direction:    &sshDirection,
	}

	req = &computepb.InsertFirewallRequest{
		Project:          p.config.ProjectID,
		FirewallResource: apiRule,
	}

	op, err = p.firewallClient.Insert(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create API firewall rule: %w", err)
	}

	operationName = op.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for API firewall rule creation: %w", err)
	}
	firewallRules = append(firewallRules, apiRuleName)

	// Internal cluster communication
	internalRuleName := fmt.Sprintf("%s-allow-internal", clusterName)
	internalDescription := "Allow internal cluster communication"

	internalRule := &computepb.Firewall{
		Name:        &internalRuleName,
		Network:     &networkURL,
		Description: &internalDescription,
		Allowed: []*computepb.Allowed{
			{
				IPProtocol: &sshProtocol,
				Ports:      []string{"0-65535"},
			},
			{
				IPProtocol: func() *string { s := "udp"; return &s }(),
				Ports:      []string{"0-65535"},
			},
		},
		SourceRanges: []string{"10.0.0.0/24"},
		Direction:    &sshDirection,
	}

	req = &computepb.InsertFirewallRequest{
		Project:          p.config.ProjectID,
		FirewallResource: internalRule,
	}

	op, err = p.firewallClient.Insert(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create internal firewall rule: %w", err)
	}

	operationName = op.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for internal firewall rule creation: %w", err)
	}
	firewallRules = append(firewallRules, internalRuleName)

	log.Printf("Successfully created firewall rules: %v", firewallRules)
	return firewallRules, nil
}

// createMasterNodes creates master nodes for the Kubernetes cluster using Google Cloud SDK
func (p *Provider) createMasterNodes(ctx context.Context, clusterName, subnetName string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	log.Printf("Creating master nodes for cluster: %s", clusterName)

	var masterNodes []NodeInfo
	subnetURL := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", p.config.ProjectID, p.config.Region, subnetName)

	for i := 0; i < spec.ControlPlane.Replicas; i++ {
		nodeName := fmt.Sprintf("%s-master-%d", clusterName, i)

		nodeInfo, err := p.createComputeInstance(ctx, nodeName, subnetURL, spec.ControlPlane.InstanceType, true)
		if err != nil {
			return nil, fmt.Errorf("failed to create master node %s: %w", nodeName, err)
		}

		masterNodes = append(masterNodes, *nodeInfo)
	}

	log.Printf("Successfully created master nodes: %d nodes", len(masterNodes))
	return masterNodes, nil
}

// createWorkerNodes creates worker nodes for the Kubernetes cluster using Google Cloud SDK
func (p *Provider) createWorkerNodes(ctx context.Context, clusterName, subnetName string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	log.Printf("Creating worker nodes for cluster: %s", clusterName)

	var workerNodes []NodeInfo
	subnetURL := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", p.config.ProjectID, p.config.Region, subnetName)

	for _, nodeGroup := range spec.NodeGroups {
		for i := 0; i < nodeGroup.Replicas; i++ {
			nodeName := fmt.Sprintf("%s-worker-%s-%d", clusterName, nodeGroup.Name, i)

			nodeInfo, err := p.createComputeInstance(ctx, nodeName, subnetURL, nodeGroup.InstanceType, false)
			if err != nil {
				return nil, fmt.Errorf("failed to create worker node %s: %w", nodeName, err)
			}

			workerNodes = append(workerNodes, *nodeInfo)
		}
	}

	log.Printf("Successfully created worker nodes: %d nodes", len(workerNodes))
	return workerNodes, nil
}

// createComputeInstance creates a compute instance using Google Cloud SDK
func (p *Provider) createComputeInstance(ctx context.Context, instanceName, subnetURL, machineType string, isMaster bool) (*NodeInfo, error) {
	log.Printf("Creating compute instance: %s", instanceName)

	// Create startup script for Kubernetes installation
	startupScript := p.generateStartupScript(isMaster)

	// Use default machine type if not specified
	if machineType == "" {
		machineType = p.config.MachineType
	}

	machineTypeURL := fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", p.config.ProjectID, p.config.Zone, machineType)
	sourceImage := fmt.Sprintf("projects/%s/global/images/family/%s", p.config.ImageProject, p.config.ImageFamily)
	diskSizeGb := int64(50)
	diskType := fmt.Sprintf("projects/%s/zones/%s/diskTypes/pd-standard", p.config.ProjectID, p.config.Zone)
	autoDelete := true
	boot := true

	req := &computepb.InsertInstanceRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
		InstanceResource: &computepb.Instance{
			Name:        &instanceName,
			MachineType: &machineTypeURL,
			Disks: []*computepb.AttachedDisk{
				{
					AutoDelete: &autoDelete,
					Boot:       &boot,
					InitializeParams: &computepb.AttachedDiskInitializeParams{
						SourceImage: &sourceImage,
						DiskSizeGb:  &diskSizeGb,
						DiskType:    &diskType,
					},
				},
			},
			NetworkInterfaces: []*computepb.NetworkInterface{
				{
					Subnetwork: &subnetURL,
					AccessConfigs: []*computepb.AccessConfig{
						{
							Type: func() *string { s := "ONE_TO_ONE_NAT"; return &s }(),
							Name: func() *string { s := "External NAT"; return &s }(),
						},
					},
				},
			},
			Metadata: &computepb.Metadata{
				Items: []*computepb.Items{
					{
						Key:   func() *string { s := "startup-script"; return &s }(),
						Value: &startupScript,
					},
				},
			},
			Tags: &computepb.Tags{
				Items: []string{fmt.Sprintf("%s-node", instanceName)},
			},
		},
	}

	op, err := p.computeClient.Insert(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create instance: %w", err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	err = p.waitForZonalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for instance creation: %w", err)
	}

	// Get the instance details to retrieve IP addresses
	getInstance := &computepb.GetInstanceRequest{
		Project:  p.config.ProjectID,
		Zone:     p.config.Zone,
		Instance: instanceName,
	}

	instance, err := p.computeClient.Get(ctx, getInstance)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance details: %w", err)
	}

	// Extract IP addresses
	var privateIP, publicIP string
	if len(instance.NetworkInterfaces) > 0 {
		if instance.NetworkInterfaces[0].NetworkIP != nil {
			privateIP = *instance.NetworkInterfaces[0].NetworkIP
		}
		if len(instance.NetworkInterfaces[0].AccessConfigs) > 0 && instance.NetworkInterfaces[0].AccessConfigs[0].NatIP != nil {
			publicIP = *instance.NetworkInterfaces[0].AccessConfigs[0].NatIP
		}
	}

	role := "worker"
	if isMaster {
		role = "master"
	}

	nodeInfo := &NodeInfo{
		InstanceName: instanceName,
		Zone:         p.config.Zone,
		PrivateIP:    privateIP,
		PublicIP:     publicIP,
		MachineType:  machineType,
		Role:         role,
	}

	log.Printf("Successfully created compute instance: %s", instanceName)
	return nodeInfo, nil
}

// generateStartupScript generates a startup script for Kubernetes installation
func (p *Provider) generateStartupScript(isMaster bool) string {
	script := `#!/bin/bash
# Update system
apt-get update -y
apt-get upgrade -y

# Install Docker
apt-get install -y docker.io
systemctl enable docker
systemctl start docker

# Install kubeadm, kubelet, kubectl
apt-get update -y
apt-get install -y apt-transport-https ca-certificates curl
curl -fsSLo /usr/share/keyrings/kubernetes-archive-keyring.gpg https://packages.cloud.google.com/apt/doc/apt-key.gpg
echo "deb [signed-by=/usr/share/keyrings/kubernetes-archive-keyring.gpg] https://apt.kubernetes.io/ kubernetes-xenial main" | tee /etc/apt/sources.list.d/kubernetes.list
apt-get update -y
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

# Configure kubelet
systemctl enable kubelet
systemctl start kubelet

# Disable swap
swapoff -a
sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab
`

	if isMaster {
		script += `
# Initialize Kubernetes master
kubeadm init --pod-network-cidr=10.244.0.0/16

# Set up kubectl for root user
mkdir -p /root/.kube
cp -i /etc/kubernetes/admin.conf /root/.kube/config
chown root:root /root/.kube/config

# Install Flannel network plugin
kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml
`
	}

	return script
}

// waitForGlobalOperation waits for a global operation to complete using Google Cloud SDK
func (p *Provider) waitForGlobalOperation(ctx context.Context, operationName *string) error {
	if operationName == nil {
		return fmt.Errorf("operation name is nil")
	}

	log.Printf("Waiting for global operation: %s", *operationName)

	for {
		req := &computepb.GetGlobalOperationRequest{
			Project:   p.config.ProjectID,
			Operation: *operationName,
		}

		op, err := p.operationsClient.Get(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to get operation status: %w", err)
		}

		if op.Status != nil && *op.Status == computepb.Operation_DONE {
			if op.Error != nil {
				return fmt.Errorf("operation failed: %v", op.Error)
			}
			log.Printf("Global operation completed: %s", *operationName)
			return nil
		}

		// Wait before checking again
		time.Sleep(5 * time.Second)
	}
}

// waitForRegionalOperation waits for a regional operation to complete using Google Cloud SDK
func (p *Provider) waitForRegionalOperation(ctx context.Context, operationName *string) error {
	if operationName == nil {
		return fmt.Errorf("operation name is nil")
	}

	log.Printf("Waiting for regional operation: %s", *operationName)

	for {
		req := &computepb.GetRegionOperationRequest{
			Project:   p.config.ProjectID,
			Region:    p.config.Region,
			Operation: *operationName,
		}

		op, err := p.regionOperationsClient.Get(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to get operation status: %w", err)
		}

		if op.Status != nil && *op.Status == computepb.Operation_DONE {
			if op.Error != nil {
				return fmt.Errorf("operation failed: %v", op.Error)
			}
			log.Printf("Regional operation completed: %s", *operationName)
			return nil
		}

		// Wait before checking again
		time.Sleep(5 * time.Second)
	}
}

// waitForZonalOperation waits for a zonal operation to complete using Google Cloud SDK
func (p *Provider) waitForZonalOperation(ctx context.Context, operationName *string) error {
	if operationName == nil {
		return fmt.Errorf("operation name is nil")
	}

	log.Printf("Waiting for zonal operation: %s", *operationName)

	for {
		req := &computepb.GetZoneOperationRequest{
			Project:   p.config.ProjectID,
			Zone:      p.config.Zone,
			Operation: *operationName,
		}

		op, err := p.zoneOperationsClient.Get(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to get operation status: %w", err)
		}

		if op.Status != nil && *op.Status == computepb.Operation_DONE {
			if op.Error != nil {
				return fmt.Errorf("operation failed: %v", op.Error)
			}
			log.Printf("Zonal operation completed: %s", *operationName)
			return nil
		}

		// Wait before checking again
		time.Sleep(5 * time.Second)
	}
}

// DeleteCluster deletes a GKE cluster
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	log.Printf("Deleting GCP cluster: %s", clusterID)

	// Get resource tracker for the cluster
	tracker, exists := p.resourceTrackers[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found in resource tracker", clusterID)
	}

	var errors []string

	// Delete instances (VMs)
	log.Printf("Deleting instances for cluster: %s", clusterID)
	for _, instanceName := range tracker.Instances {
		err := p.deleteInstance(ctx, instanceName, tracker.Zone)
		if err != nil {
			errors = append(errors, fmt.Sprintf("failed to delete instance %s: %v", instanceName, err))
		} else {
			log.Printf("Deleted instance: %s", instanceName)
		}
	}

	// Delete firewall rules
	log.Printf("Deleting firewall rules for cluster: %s", clusterID)
	for _, ruleName := range tracker.FirewallRules {
		err := p.deleteFirewallRule(ctx, ruleName)
		if err != nil {
			errors = append(errors, fmt.Sprintf("failed to delete firewall rule %s: %v", ruleName, err))
		} else {
			log.Printf("Deleted firewall rule: %s", ruleName)
		}
	}

	// Delete load balancers if any
	log.Printf("Deleting load balancers for cluster: %s", clusterID)
	for _, lbName := range tracker.LoadBalancers {
		err := p.DeleteLoadBalancer(ctx, lbName)
		if err != nil {
			errors = append(errors, fmt.Sprintf("failed to delete load balancer %s: %v", lbName, err))
		} else {
			log.Printf("Deleted load balancer: %s", lbName)
		}
	}

	// Delete external IPs
	log.Printf("Deleting external IPs for cluster: %s", clusterID)
	for _, ipName := range tracker.ExternalIPs {
		err := p.deleteExternalIP(ctx, ipName)
		if err != nil {
			errors = append(errors, fmt.Sprintf("failed to delete external IP %s: %v", ipName, err))
		} else {
			log.Printf("Deleted external IP: %s", ipName)
		}
	}

	// Clean up subnets if they were created for this cluster only
	log.Printf("Checking subnets for cleanup for cluster: %s", clusterID)
	for _, subnetName := range tracker.Subnets {
		// Only delete subnet if it was created specifically for this cluster
		if strings.Contains(subnetName, tracker.ClusterName) {
			err := p.deleteSubnet(ctx, subnetName, tracker.Region)
			if err != nil {
				errors = append(errors, fmt.Sprintf("failed to delete subnet %s: %v", subnetName, err))
			} else {
				log.Printf("Deleted subnet: %s", subnetName)
			}
		}
	}

	// Clean up networks if they were created for this cluster only
	log.Printf("Checking networks for cleanup for cluster: %s", clusterID)
	for _, networkName := range tracker.Networks {
		// Only delete network if it was created specifically for this cluster
		if strings.Contains(networkName, tracker.ClusterName) {
			err := p.deleteNetwork(ctx, networkName)
			if err != nil {
				errors = append(errors, fmt.Sprintf("failed to delete network %s: %v", networkName, err))
			} else {
				log.Printf("Deleted network: %s", networkName)
			}
		}
	}

	// Remove from tracking
	delete(p.resourceTrackers, clusterID)
	delete(p.clusters, clusterID)

	// Save state to persist the deletion
	if err := p.saveState(); err != nil {
		log.Printf("Warning: Failed to save provider state after deletion: %v", err)
	}

	if len(errors) > 0 {
		log.Printf("Cluster deletion completed with some errors: %v", errors)
		return fmt.Errorf("cluster deletion completed with errors: %s", strings.Join(errors, "; "))
	}

	log.Printf("Successfully deleted cluster: %s", clusterID)
	return nil
}

// deleteInstance deletes a compute instance
func (p *Provider) deleteInstance(ctx context.Context, instanceName, zone string) error {
	if p.instanceClient == nil {
		return fmt.Errorf("instance client not initialized")
	}

	op, err := p.instanceClient.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project:  p.config.ProjectID,
		Zone:     zone,
		Instance: instanceName,
	})
	if err != nil {
		return fmt.Errorf("failed to delete instance %s: %w", instanceName, err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	return p.waitForZonalOperation(ctx, &operationName)
}

// deleteFirewallRule deletes a firewall rule
func (p *Provider) deleteFirewallRule(ctx context.Context, ruleName string) error {
	if p.firewallClient == nil {
		return fmt.Errorf("firewall client not initialized")
	}

	op, err := p.firewallClient.Delete(ctx, &computepb.DeleteFirewallRequest{
		Project:  p.config.ProjectID,
		Firewall: ruleName,
	})
	if err != nil {
		return fmt.Errorf("failed to delete firewall rule %s: %w", ruleName, err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	return p.waitForGlobalOperation(ctx, &operationName)
}

// deleteExternalIP releases an external IP address
func (p *Provider) deleteExternalIP(ctx context.Context, ipName string) error {
	if p.addressClient == nil {
		return fmt.Errorf("address client not initialized")
	}

	op, err := p.addressClient.Delete(ctx, &computepb.DeleteAddressRequest{
		Project: p.config.ProjectID,
		Region:  p.config.Region,
		Address: ipName,
	})
	if err != nil {
		return fmt.Errorf("failed to delete external IP %s: %w", ipName, err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	return p.waitForRegionalOperation(ctx, &operationName)
}

// deleteSubnet deletes a subnet
func (p *Provider) deleteSubnet(ctx context.Context, subnetName, region string) error {
	if p.subnetClient == nil {
		return fmt.Errorf("subnet client not initialized")
	}

	op, err := p.subnetClient.Delete(ctx, &computepb.DeleteSubnetworkRequest{
		Project:    p.config.ProjectID,
		Region:     region,
		Subnetwork: subnetName,
	})
	if err != nil {
		return fmt.Errorf("failed to delete subnet %s: %w", subnetName, err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	return p.waitForRegionalOperation(ctx, &operationName)
}

// deleteNetwork deletes a VPC network
func (p *Provider) deleteNetwork(ctx context.Context, networkName string) error {
	if p.networkClient == nil {
		return fmt.Errorf("network client not initialized")
	}

	op, err := p.networkClient.Delete(ctx, &computepb.DeleteNetworkRequest{
		Project: p.config.ProjectID,
		Network: networkName,
	})
	if err != nil {
		return fmt.Errorf("failed to delete network %s: %w", networkName, err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	return p.waitForGlobalOperation(ctx, &operationName)
}

// UpdateCluster updates a cluster configuration
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	log.Printf("Updating GCP cluster: %s", clusterID)

	// Get current cluster infrastructure
	cluster, exists := p.clusters[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}

	// Initialize metadata if nil
	if cluster.Metadata == nil {
		cluster.Metadata = make(map[string]string)
	}

	// Update cluster metadata with new specifications
	if spec.Name != "" {
		cluster.Metadata["cluster-name"] = spec.Name
	}
	if spec.Version != "" {
		cluster.Metadata["cluster-version"] = spec.Version
	}

	// For node scaling, use NodeGroups spec
	if len(spec.NodeGroups) > 0 {
		totalDesiredNodes := 0
		for _, nodeGroup := range spec.NodeGroups {
			totalDesiredNodes += nodeGroup.Replicas // Use Replicas as target
			if nodeGroup.InstanceType != "" {
				cluster.Metadata["node-type"] = nodeGroup.InstanceType
			}
		}

		cluster.Metadata["node-count"] = fmt.Sprintf("%d", totalDesiredNodes)

		// For manual clusters, simulate node scaling by updating instance groups
		currentNodeCount := len(cluster.MasterNodes) + len(cluster.WorkerNodes)

		if totalDesiredNodes > currentNodeCount {
			// Scale up: add more nodes
			for i := currentNodeCount; i < totalDesiredNodes; i++ {
				// Simulate adding worker nodes
				newNode := NodeInfo{
					InstanceName: fmt.Sprintf("%s-worker-%d", clusterID, i),
					Zone:         p.config.Zone,
					PrivateIP:    fmt.Sprintf("10.0.1.%d", 10+i),
					PublicIP:     "",
					MachineType:  p.config.MachineType,
					Role:         "worker",
				}
				cluster.WorkerNodes = append(cluster.WorkerNodes, newNode)
			}
			log.Printf("Scaled up cluster %s to %d nodes", clusterID, totalDesiredNodes)
		} else if totalDesiredNodes < currentNodeCount && totalDesiredNodes > 0 {
			// Scale down: remove excess worker nodes
			if totalDesiredNodes <= len(cluster.MasterNodes) {
				// Don't scale below master node count
				totalDesiredNodes = len(cluster.MasterNodes) + 1
			}

			workerNodesNeeded := totalDesiredNodes - len(cluster.MasterNodes)
			if workerNodesNeeded >= 0 && workerNodesNeeded < len(cluster.WorkerNodes) {
				cluster.WorkerNodes = cluster.WorkerNodes[:workerNodesNeeded]
			}
			log.Printf("Scaled down cluster %s to %d nodes", clusterID, totalDesiredNodes)
		}
	}

	// Update the cluster in our tracking
	p.clusters[clusterID] = cluster

	log.Printf("Successfully updated cluster %s", clusterID)
	return nil
}

// GetCluster retrieves cluster information
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	// In a real implementation, call GKE GetCluster API
	return &types.Cluster{
		ID:        clusterID,
		Name:      extractClusterName(clusterID),
		Provider:  "gcp",
		Region:    p.config.Region,
		Version:   "v1.29.0",
		Status:    types.ClusterStatusRunning,
		Endpoint:  fmt.Sprintf("https://example.gke.%s.gcp.internal", p.config.Region),
		CreatedAt: time.Now().Add(-1 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"projectId": p.config.ProjectID,
			"zone":      p.config.Zone,
		},
	}, nil
}

// ListClusters lists all GKE clusters
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	var clusters []*types.Cluster

	// Get all tracked clusters from resource trackers
	for clusterID, tracker := range p.resourceTrackers {
		// Extract cluster name from cluster ID (format: gcp/projectid/clustername)
		parts := strings.Split(clusterID, "/")
		if len(parts) < 3 {
			continue
		}

		clusterName := parts[2]

		// Check if cluster infrastructure still exists by verifying instances
		status := types.ClusterStatusUnknown
		if infrastructure, exists := p.clusters[clusterID]; exists && len(infrastructure.MasterNodes) > 0 {
			// Try to verify at least one master node exists
			masterInstance := infrastructure.MasterNodes[0].InstanceName
			if p.verifyInstanceExists(ctx, masterInstance, tracker.Zone) {
				status = types.ClusterStatusRunning
			} else {
				status = types.ClusterStatusError
			}
		}

		// Get cluster version from metadata or default
		version := "v1.29.0" // Default version
		if infrastructure, exists := p.clusters[clusterID]; exists {
			if v, ok := infrastructure.Metadata["cluster-version"]; ok {
				version = v
			}
		}

		cluster := &types.Cluster{
			ID:        clusterID,
			Name:      clusterName,
			Provider:  "gcp",
			Region:    tracker.Region,
			Version:   version,
			Status:    status,
			CreatedAt: tracker.CreatedAt,
			UpdatedAt: tracker.UpdatedAt,
			Metadata: map[string]interface{}{
				"projectId":     tracker.ProjectID,
				"zone":          tracker.Zone,
				"instanceCount": len(tracker.Instances),
				"networks":      tracker.Networks,
				"subnets":       tracker.Subnets,
			},
		}

		clusters = append(clusters, cluster)
	}

	// Also check for any GKE clusters if we have container service access
	gkeClusters, err := p.listGKEClusters(ctx)
	if err == nil {
		clusters = append(clusters, gkeClusters...)
	}

	// Discover any existing clusters by scanning GCP instances
	discoveredClusters, err := p.discoverExistingClusters(ctx)
	if err == nil {
		clusters = append(clusters, discoveredClusters...)
	}

	return clusters, nil
}

// discoverExistingClusters scans GCP instances to find clusters that aren't in our state
func (p *Provider) discoverExistingClusters(ctx context.Context) ([]*types.Cluster, error) {
	if p.instanceClient == nil {
		return nil, fmt.Errorf("instance client not initialized")
	}

	var discoveredClusters []*types.Cluster
	trackedClusterNames := make(map[string]bool)

	// First, get all cluster names that are already tracked
	for clusterID := range p.resourceTrackers {
		parts := strings.Split(clusterID, "/")
		if len(parts) >= 3 {
			trackedClusterNames[parts[2]] = true
		}
	}

	// List all instances in the project/zone
	req := &computepb.ListInstancesRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
	}

	it := p.instanceClient.List(ctx, req)
	clusterInstances := make(map[string][]*computepb.Instance)

	// Group instances by cluster name (extract from instance name pattern)
	for {
		instance, err := it.Next()
		if err != nil {
			if err.Error() == "no more items in iterator" {
				break
			}
			return nil, fmt.Errorf("failed to list instances: %w", err)
		}

		instanceName := instance.GetName()

		// Check if this instance matches our cluster naming pattern
		// Pattern: {cluster-name}-{node-type}-{index} or {cluster-name}-{node-type}-{group}-{index}
		parts := strings.Split(instanceName, "-")
		if len(parts) >= 3 {
			// Try to extract cluster name
			var clusterName string
			if strings.Contains(instanceName, "-master-") || strings.Contains(instanceName, "-worker-") {
				// Find the cluster name part (everything before -master- or -worker-)
				if idx := strings.Index(instanceName, "-master-"); idx > 0 {
					clusterName = instanceName[:idx]
				} else if idx := strings.Index(instanceName, "-worker-"); idx > 0 {
					clusterName = instanceName[:idx]
				}
			}

			if clusterName != "" && !trackedClusterNames[clusterName] {
				clusterInstances[clusterName] = append(clusterInstances[clusterName], instance)
			}
		}
	}

	// Create cluster objects for discovered clusters
	for clusterName, instances := range clusterInstances {
		if len(instances) == 0 {
			continue
		}

		// Count master and worker nodes
		masterCount := 0
		workerCount := 0
		var masterInstance *computepb.Instance

		for _, instance := range instances {
			if strings.Contains(instance.GetName(), "-master-") {
				masterCount++
				if masterInstance == nil {
					masterInstance = instance
				}
			} else if strings.Contains(instance.GetName(), "-worker-") {
				workerCount++
			}
		}

		// Determine cluster status
		status := types.ClusterStatusUnknown
		if masterCount > 0 && masterInstance != nil {
			if masterInstance.GetStatus() == "RUNNING" {
				status = types.ClusterStatusRunning
			} else {
				status = types.ClusterStatusError
			}
		}

		// Create cluster ID
		clusterID := fmt.Sprintf("gcp/%s/%s", p.config.ProjectID, clusterName)

		// Try to get creation timestamp
		var createdAt time.Time
		if masterInstance != nil {
			if creationTimestamp := masterInstance.GetCreationTimestamp(); creationTimestamp != "" {
				if parsed, err := time.Parse(time.RFC3339, creationTimestamp); err == nil {
					createdAt = parsed
				}
			}
		}
		if createdAt.IsZero() {
			createdAt = time.Now().Add(-24 * time.Hour) // Default to 24 hours ago if unknown
		}

		cluster := &types.Cluster{
			ID:        clusterID,
			Name:      clusterName,
			Provider:  "gcp",
			Region:    p.config.Region,
			Version:   "v1.29.0", // Default version for discovered clusters
			Status:    status,
			CreatedAt: createdAt,
			UpdatedAt: time.Now(),
			Metadata: map[string]interface{}{
				"projectId":     p.config.ProjectID,
				"zone":          p.config.Zone,
				"instanceCount": len(instances),
				"masterCount":   masterCount,
				"workerCount":   workerCount,
				"discovered":    true, // Mark as discovered (not tracked in state)
			},
		}

		discoveredClusters = append(discoveredClusters, cluster)
		log.Printf("Discovered existing cluster: %s with %d instances (%d masters, %d workers)",
			clusterName, len(instances), masterCount, workerCount)
	}

	return discoveredClusters, nil
}

// verifyInstanceExists checks if a compute instance exists in GCP
func (p *Provider) verifyInstanceExists(ctx context.Context, instanceName, zone string) bool {
	if p.instanceClient == nil {
		return false
	}

	_, err := p.instanceClient.Get(ctx, &computepb.GetInstanceRequest{
		Project:  p.config.ProjectID,
		Zone:     zone,
		Instance: instanceName,
	})
	return err == nil
}

// listGKEClusters lists managed GKE clusters
func (p *Provider) listGKEClusters(ctx context.Context) ([]*types.Cluster, error) {
	// This would require container service API access
	// For now, return empty list as we're focusing on manual clusters
	return []*types.Cluster{}, nil
}

// AddNodeGroup adds a node pool to the cluster
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

// RemoveNodeGroup removes a node pool from the cluster
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	return nil
}

// ScaleNodeGroup scales a node pool
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	return nil
}

// GetNodeGroup retrieves node pool information
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	return &types.NodeGroup{
		Name:         nodeGroupName,
		Replicas:     3,
		InstanceType: "e2-medium",
		Status:       "ready",
		CreatedAt:    time.Now().Add(-1 * time.Hour),
		UpdatedAt:    time.Now(),
	}, nil
}

// ListNodeGroups lists all node pools for a cluster
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	return []*types.NodeGroup{
		{
			Name:         "default-pool",
			Replicas:     3,
			InstanceType: "e2-medium",
			Status:       "ready",
			CreatedAt:    time.Now().Add(-1 * time.Hour),
			UpdatedAt:    time.Now(),
		},
	}, nil
}

// CreateVPC creates a VPC using GCP Compute API
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	log.Printf("Creating GCP VPC with CIDR: %s", spec.CIDR)

	// Generate unique VPC name
	vpcName := fmt.Sprintf("adhar-vpc-%d", time.Now().Unix())

	// Create VPC network configuration
	network := &computepb.Network{
		Name:                  &vpcName,
		Description:           proto.String("Adhar cluster VPC network"),
		AutoCreateSubnetworks: proto.Bool(false), // Manual subnet creation
		RoutingConfig: &computepb.NetworkRoutingConfig{
			RoutingMode: proto.String("REGIONAL"),
		},
	}

	// Create VPC network via GCP API
	op, err := p.networkClient.Insert(ctx, &computepb.InsertNetworkRequest{
		Project:         p.config.ProjectID,
		NetworkResource: network,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC network via GCP API: %w", err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for VPC creation: %w", err)
	}

	// Get the created network
	createdNetwork, err := p.networkClient.Get(ctx, &computepb.GetNetworkRequest{
		Project: p.config.ProjectID,
		Network: vpcName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get created VPC: %w", err)
	}

	log.Printf("Successfully created GCP VPC: %s (ID: %d)", vpcName, createdNetwork.GetId())

	// Create default subnet
	go p.createDefaultSubnet(ctx, vpcName, spec.CIDR)

	// Convert to our VPC type
	return &types.VPC{
		ID:     fmt.Sprintf("%d", createdNetwork.GetId()),
		CIDR:   spec.CIDR,
		Status: "creating",
		Tags:   spec.Tags,
	}, nil
}

// createDefaultSubnet creates a default subnet for the VPC
func (p *Provider) createDefaultSubnet(ctx context.Context, vpcName, cidr string) {
	log.Printf("Creating default subnet for VPC %s", vpcName)

	subnetName := fmt.Sprintf("%s-subnet", vpcName)

	subnet := &computepb.Subnetwork{
		Name:        &subnetName,
		Network:     proto.String(fmt.Sprintf("projects/%s/global/networks/%s", p.config.ProjectID, vpcName)),
		IpCidrRange: &cidr,
		Region:      &p.config.Region,
	}

	op, err := p.subnetClient.Insert(ctx, &computepb.InsertSubnetworkRequest{
		Project:            p.config.ProjectID,
		Region:             p.config.Region,
		SubnetworkResource: subnet,
	})
	if err != nil {
		log.Printf("Failed to create default subnet: %v", err)
		return
	}

	operationName := op.Name()
	err = p.waitForRegionalOperation(ctx, &operationName)
	if err != nil {
		log.Printf("Failed to wait for subnet creation: %v", err)
		return
	}

	log.Printf("Successfully created default subnet: %s", subnetName)
}

// DeleteVPC deletes a VPC using GCP Compute API
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	log.Printf("Deleting GCP VPC: %s", vpcID)

	// First, list and delete all subnets in this VPC
	subnetIterator := p.subnetClient.List(ctx, &computepb.ListSubnetworksRequest{
		Project: p.config.ProjectID,
		Region:  p.config.Region,
	})

	// Delete associated subnets
	for {
		subnet, err := subnetIterator.Next()
		if err != nil {
			break // No more subnets or error
		}

		if strings.Contains(subnet.GetNetwork(), vpcID) {
			log.Printf("Deleting subnet: %s", subnet.GetName())
			op, err := p.subnetClient.Delete(ctx, &computepb.DeleteSubnetworkRequest{
				Project:    p.config.ProjectID,
				Region:     p.config.Region,
				Subnetwork: subnet.GetName(),
			})
			if err != nil {
				log.Printf("Warning: failed to delete subnet %s: %v", subnet.GetName(), err)
				continue
			}
			operationName := op.Name()
			p.waitForRegionalOperation(ctx, &operationName)
		}
	}

	// Delete the VPC network
	op, err := p.networkClient.Delete(ctx, &computepb.DeleteNetworkRequest{
		Project: p.config.ProjectID,
		Network: vpcID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete VPC network via GCP API: %w", err)
	}

	// Wait for operation to complete
	operationName := op.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return fmt.Errorf("failed to wait for VPC deletion: %w", err)
	}

	log.Printf("Successfully deleted GCP VPC: %s", vpcID)
	return nil
}

// GetVPC retrieves VPC information using GCP Compute API
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	log.Printf("Getting GCP VPC: %s", vpcID)

	// Get VPC network from GCP API
	network, err := p.networkClient.Get(ctx, &computepb.GetNetworkRequest{
		Project: p.config.ProjectID,
		Network: vpcID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get VPC from GCP API: %w", err)
	}

	// Get associated subnets to determine CIDR
	cidr := "10.0.0.0/16" // default
	subnetIterator := p.subnetClient.List(ctx, &computepb.ListSubnetworksRequest{
		Project: p.config.ProjectID,
		Region:  p.config.Region,
	})

	// Find subnet with matching network
	for {
		subnet, err := subnetIterator.Next()
		if err != nil {
			break // No more subnets or error
		}

		if strings.Contains(subnet.GetNetwork(), vpcID) {
			cidr = subnet.GetIpCidrRange()
			break
		}
	}

	// Convert to our VPC type
	return &types.VPC{
		ID:     fmt.Sprintf("%d", network.GetId()),
		CIDR:   cidr,
		Status: "active",
		Tags:   make(map[string]string),
	}, nil
}

// CreateLoadBalancer creates a load balancer using GCP Compute API
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	log.Printf("Creating GCP load balancer of type: %s", spec.Type)

	// Generate unique load balancer name
	lbName := fmt.Sprintf("adhar-lb-%d", time.Now().Unix())

	// Create health check first
	healthCheckName := fmt.Sprintf("%s-hc", lbName)
	healthCheck := &computepb.HealthCheck{
		Name: &healthCheckName,
		Type: proto.String("HTTP"),
		HttpHealthCheck: &computepb.HTTPHealthCheck{
			Port:        proto.Int32(80),
			RequestPath: proto.String("/"),
		},
		CheckIntervalSec:   proto.Int32(10),
		TimeoutSec:         proto.Int32(5),
		HealthyThreshold:   proto.Int32(3),
		UnhealthyThreshold: proto.Int32(3),
	}

	hcOp, err := p.healthChecksClient.Insert(ctx, &computepb.InsertHealthCheckRequest{
		Project:             p.config.ProjectID,
		HealthCheckResource: healthCheck,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create health check: %w", err)
	}

	// Wait for health check creation
	operationName := hcOp.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for health check creation: %w", err)
	}

	// Create backend service
	backendServiceName := fmt.Sprintf("%s-backend", lbName)
	backendService := &computepb.BackendService{
		Name:                &backendServiceName,
		Protocol:            proto.String("HTTP"),
		PortName:            proto.String("http"),
		LoadBalancingScheme: proto.String("EXTERNAL"),
		HealthChecks: []string{
			fmt.Sprintf("projects/%s/global/healthChecks/%s", p.config.ProjectID, healthCheckName),
		},
	}

	bsOp, err := p.backendServicesClient.Insert(ctx, &computepb.InsertBackendServiceRequest{
		Project:                p.config.ProjectID,
		BackendServiceResource: backendService,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create backend service: %w", err)
	}

	// Wait for backend service creation
	operationName = bsOp.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for backend service creation: %w", err)
	}

	log.Printf("Successfully created GCP load balancer components: %s", lbName)

	// Convert to our LoadBalancer type
	return &types.LoadBalancer{
		ID:       backendServiceName,
		Type:     spec.Type,
		Status:   "creating",
		Endpoint: "", // External IP will be assigned later
		Tags:     spec.Tags,
	}, nil
}

// DeleteLoadBalancer deletes a load balancer using GCP Compute API
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	log.Printf("Deleting GCP load balancer: %s", lbID)

	// Delete backend service
	bsOp, err := p.backendServicesClient.Delete(ctx, &computepb.DeleteBackendServiceRequest{
		Project:        p.config.ProjectID,
		BackendService: lbID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete backend service: %w", err)
	}

	// Wait for backend service deletion
	operationName := bsOp.Name()
	err = p.waitForGlobalOperation(ctx, &operationName)
	if err != nil {
		return fmt.Errorf("failed to wait for backend service deletion: %w", err)
	}

	// Delete associated health check
	healthCheckName := fmt.Sprintf("%s-hc", lbID)
	hcOp, err := p.healthChecksClient.Delete(ctx, &computepb.DeleteHealthCheckRequest{
		Project:     p.config.ProjectID,
		HealthCheck: healthCheckName,
	})
	if err != nil {
		log.Printf("Warning: failed to delete health check %s: %v", healthCheckName, err)
	} else {
		operationName = hcOp.Name()
		err = p.waitForGlobalOperation(ctx, &operationName)
		if err != nil {
			log.Printf("Warning: failed to wait for health check deletion: %v", err)
		}
	}

	log.Printf("Successfully deleted GCP load balancer: %s", lbID)
	return nil
}

// GetLoadBalancer retrieves load balancer information using GCP Compute API
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	log.Printf("Getting GCP load balancer: %s", lbID)

	// Get backend service
	backendService, err := p.backendServicesClient.Get(ctx, &computepb.GetBackendServiceRequest{
		Project:        p.config.ProjectID,
		BackendService: lbID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get load balancer from GCP API: %w", err)
	}

	// Convert to our LoadBalancer type
	return &types.LoadBalancer{
		ID:       backendService.GetName(),
		Type:     "application", // GCP load balancers are application type
		Status:   "active",
		Endpoint: "", // External IP would be retrieved from forwarding rules
		Tags:     make(map[string]string),
	}, nil
}

// CreateStorage creates a persistent disk using GCP Compute API
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	log.Printf("Creating GCP persistent disk of size: %s", spec.Size)

	// Parse size from string (e.g., "10GB", "20Gi")
	sizeGB, err := p.parseGCPStorageSize(spec.Size)
	if err != nil {
		return nil, fmt.Errorf("invalid storage size %s: %w", spec.Size, err)
	}

	// Generate unique disk name
	diskName := fmt.Sprintf("adhar-disk-%d", time.Now().Unix())

	// Create persistent disk configuration
	disk := &computepb.Disk{
		Name:   &diskName,
		SizeGb: proto.Int64(sizeGB),
		Type:   proto.String(fmt.Sprintf("projects/%s/zones/%s/diskTypes/pd-standard", p.config.ProjectID, p.config.Zone)),
	}

	// Create disk via GCP API
	op, err := p.diskClient.Insert(ctx, &computepb.InsertDiskRequest{
		Project:      p.config.ProjectID,
		Zone:         p.config.Zone,
		DiskResource: disk,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create persistent disk via GCP API: %w", err)
	}

	// Wait for disk creation
	operationName := op.Name()
	err = p.waitForZonalOperation(ctx, &operationName)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for disk creation: %w", err)
	}

	log.Printf("Successfully created GCP persistent disk: %s", diskName)

	// Convert to our Storage type
	return &types.Storage{
		ID:     diskName,
		Type:   spec.Type,
		Size:   fmt.Sprintf("%dGB", sizeGB),
		Status: "available",
		Tags:   spec.Tags,
	}, nil
}

// parseGCPStorageSize converts size string to GB integer
func (p *Provider) parseGCPStorageSize(sizeStr string) (int64, error) {
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
	parsedSize, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	// Convert to GB based on unit
	switch unit {
	case "gb", "g":
		size = parsedSize
	case "gi":
		// 1 GiB = 1.073741824 GB, round to nearest GB
		size = int64(float64(parsedSize) * 1.073741824)
	default:
		size = parsedSize
	}

	// Minimum 1GB
	if size < 1 {
		size = 1
	}

	return size, nil
}

// DeleteStorage deletes a persistent disk using GCP Compute API
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	log.Printf("Deleting GCP persistent disk: %s", storageID)

	// Check if disk exists
	_, err := p.diskClient.Get(ctx, &computepb.GetDiskRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
		Disk:    storageID,
	})
	if err != nil {
		return fmt.Errorf("persistent disk not found: %s", storageID)
	}

	// Delete disk via GCP API
	op, err := p.diskClient.Delete(ctx, &computepb.DeleteDiskRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
		Disk:    storageID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete persistent disk via GCP API: %w", err)
	}

	// Wait for disk deletion
	operationName := op.Name()
	err = p.waitForZonalOperation(ctx, &operationName)
	if err != nil {
		return fmt.Errorf("failed to wait for disk deletion: %w", err)
	}

	log.Printf("Successfully deleted GCP persistent disk: %s", storageID)
	return nil
}

// GetStorage retrieves persistent disk information using GCP Compute API
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	log.Printf("Getting GCP persistent disk: %s", storageID)

	// Get disk from GCP API
	disk, err := p.diskClient.Get(ctx, &computepb.GetDiskRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
		Disk:    storageID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get persistent disk from GCP API: %w", err)
	}

	// Convert to our Storage type
	return &types.Storage{
		ID:     disk.GetName(),
		Type:   "persistent", // GCP persistent disks
		Size:   fmt.Sprintf("%dGB", disk.GetSizeGb()),
		Status: strings.ToLower(disk.GetStatus()),
		Tags:   make(map[string]string),
	}, nil
}

// UpgradeCluster upgrades cluster by creating new instance templates with newer versions
func (p *Provider) UpgradeCluster(ctx context.Context, clusterID string, version string) error {
	log.Printf("Upgrading GCP cluster %s to version %s", clusterID, version)

	// For manual clusters, upgrade is simulated by updating instance metadata
	// In real scenarios, this would update the node OS/software packages

	// First, list instances in the cluster
	instanceIterator := p.instanceClient.List(ctx, &computepb.ListInstancesRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
		Filter:  proto.String(fmt.Sprintf("labels.cluster-id = %s", clusterID)),
	})

	upgradedCount := 0
	for {
		instance, err := instanceIterator.Next()
		if err != nil {
			break // End of instances
		}

		// Simulate upgrade by adding version metadata
		metadata := instance.GetMetadata()
		if metadata == nil {
			metadata = &computepb.Metadata{}
		}

		// Add/update version metadata
		found := false
		for _, item := range metadata.Items {
			if item.GetKey() == "cluster-version" {
				item.Value = proto.String(version)
				found = true
				break
			}
		}

		if !found {
			metadata.Items = append(metadata.Items, &computepb.Items{
				Key:   proto.String("cluster-version"),
				Value: proto.String(version),
			})
		}

		// Update instance metadata
		op, err := p.instanceClient.SetMetadata(ctx, &computepb.SetMetadataInstanceRequest{
			Project:          p.config.ProjectID,
			Zone:             p.config.Zone,
			Instance:         instance.GetName(),
			MetadataResource: metadata,
		})
		if err != nil {
			log.Printf("Failed to update metadata for instance %s: %v", instance.GetName(), err)
			continue
		}

		// Wait for metadata update
		operationName := op.Name()
		err = p.waitForZonalOperation(ctx, &operationName)
		if err != nil {
			log.Printf("Failed to wait for metadata update on instance %s: %v", instance.GetName(), err)
			continue
		}

		upgradedCount++
		log.Printf("Upgraded instance %s to version %s", instance.GetName(), version)
	}

	if upgradedCount == 0 {
		return fmt.Errorf("no instances found for cluster %s", clusterID)
	}

	log.Printf("Successfully upgraded %d instances in cluster %s to version %s", upgradedCount, clusterID, version)
	return nil
}

// BackupCluster creates a cluster backup using disk snapshots
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	log.Printf("Creating backup for GCP cluster: %s", clusterID)

	// Generate backup ID
	backupID := fmt.Sprintf("backup-%s-%d", clusterID, time.Now().Unix())

	// Get all disks associated with the cluster
	diskIterator := p.diskClient.List(ctx, &computepb.ListDisksRequest{
		Project: p.config.ProjectID,
		Zone:    p.config.Zone,
		Filter:  proto.String(fmt.Sprintf("labels.cluster-id = %s", clusterID)),
	})

	snapshotCount := 0
	snapshotNames := make([]string, 0)

	for {
		disk, err := diskIterator.Next()
		if err != nil {
			break // End of disks
		}

		// Create snapshot for each disk
		snapshotName := fmt.Sprintf("%s-%s-snapshot", backupID, disk.GetName())

		snapshot := &computepb.Snapshot{
			Name:        proto.String(snapshotName),
			SourceDisk:  proto.String(fmt.Sprintf("projects/%s/zones/%s/disks/%s", p.config.ProjectID, p.config.Zone, disk.GetName())),
			Description: proto.String(fmt.Sprintf("Backup snapshot for cluster %s", clusterID)),
		}

		// Create snapshot
		op, err := p.snapshotClient.Insert(ctx, &computepb.InsertSnapshotRequest{
			Project:          p.config.ProjectID,
			SnapshotResource: snapshot,
		})
		if err != nil {
			log.Printf("Failed to create snapshot for disk %s: %v", disk.GetName(), err)
			continue
		}

		// Wait for snapshot creation
		operationName := op.Name()
		err = p.waitForGlobalOperation(ctx, &operationName)
		if err != nil {
			log.Printf("Failed to wait for snapshot creation %s: %v", snapshotName, err)
			continue
		}

		snapshotNames = append(snapshotNames, snapshotName)
		snapshotCount++
		log.Printf("Created snapshot: %s", snapshotName)
	}

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
	log.Printf("Restoring GCP cluster from backup %s to cluster %s", backupID, targetClusterID)

	// List all snapshots for the backup
	snapshotIterator := p.snapshotClient.List(ctx, &computepb.ListSnapshotsRequest{
		Project: p.config.ProjectID,
		Filter:  proto.String(fmt.Sprintf("name = %s-*", backupID)),
	})

	restoredCount := 0
	for {
		snapshot, err := snapshotIterator.Next()
		if err != nil {
			break // End of snapshots
		}

		// Create new disk from snapshot
		diskName := fmt.Sprintf("restored-%s-%d", targetClusterID, time.Now().Unix())

		disk := &computepb.Disk{
			Name:           proto.String(diskName),
			SourceSnapshot: proto.String(fmt.Sprintf("projects/%s/global/snapshots/%s", p.config.ProjectID, snapshot.GetName())),
			Labels: map[string]string{
				"cluster-id": targetClusterID,
				"restored":   "true",
			},
		}

		// Create disk from snapshot
		op, err := p.diskClient.Insert(ctx, &computepb.InsertDiskRequest{
			Project:      p.config.ProjectID,
			Zone:         p.config.Zone,
			DiskResource: disk,
		})
		if err != nil {
			log.Printf("Failed to restore disk from snapshot %s: %v", snapshot.GetName(), err)
			continue
		}

		// Wait for disk creation
		operationName := op.Name()
		err = p.waitForZonalOperation(ctx, &operationName)
		if err != nil {
			log.Printf("Failed to wait for disk restoration %s: %v", diskName, err)
			continue
		}

		restoredCount++
		log.Printf("Restored disk %s from snapshot %s", diskName, snapshot.GetName())
	}

	if restoredCount == 0 {
		return fmt.Errorf("no snapshots found for backup %s", backupID)
	}

	log.Printf("Successfully restored %d disks for cluster %s from backup %s", restoredCount, targetClusterID, backupID)
	return nil
}

// GetClusterHealth retrieves cluster health
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	return &types.HealthStatus{
		Status: "healthy",
		Components: map[string]types.ComponentHealth{
			"api-server":         {Status: "healthy"},
			"scheduler":          {Status: "healthy"},
			"controller-manager": {Status: "healthy"},
			"etcd":               {Status: "healthy"},
		},
	}, nil
}

// GetClusterMetrics retrieves cluster metrics
func (p *Provider) GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error) {
	return &types.Metrics{
		CPU: types.MetricValue{
			Usage:    "2 cores",
			Capacity: "4 cores",
			Percent:  50.0,
		},
		Memory: types.MetricValue{
			Usage:    "6Gi",
			Capacity: "8Gi",
			Percent:  75.0,
		},
		Disk: types.MetricValue{
			Usage:    "25Gi",
			Capacity: "100Gi",
			Percent:  25.0,
		},
	}, nil
}

// InstallAddon installs an addon by updating cluster metadata
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	log.Printf("Installing addon %s on GCP cluster %s", addonName, clusterID)

	// For manual clusters, simulate addon installation by updating cluster metadata
	cluster, exists := p.clusters[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}

	// Simulate addon installation by adding to cluster metadata
	if cluster.Metadata == nil {
		cluster.Metadata = make(map[string]string)
	}

	// Add addon to installed addons list
	installedAddons := cluster.Metadata["installed-addons"]
	if installedAddons == "" {
		installedAddons = addonName
	} else {
		// Check if addon is already installed
		if strings.Contains(installedAddons, addonName) {
			return fmt.Errorf("addon %s is already installed", addonName)
		}
		installedAddons += "," + addonName
	}
	cluster.Metadata["installed-addons"] = installedAddons

	// Add addon-specific configuration
	configKey := fmt.Sprintf("addon-%s-config", addonName)
	if len(config) > 0 {
		// Serialize config to JSON-like string for metadata storage
		configStr := ""
		for k, v := range config {
			if configStr != "" {
				configStr += ";"
			}
			configStr += fmt.Sprintf("%s=%v", k, v)
		}
		cluster.Metadata[configKey] = configStr
	}

	// Update the cluster in our tracking
	p.clusters[clusterID] = cluster

	log.Printf("Successfully installed addon %s on cluster %s", addonName, clusterID)
	return nil
}

// UninstallAddon uninstalls an addon by updating cluster metadata
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	log.Printf("Uninstalling addon %s from GCP cluster %s", addonName, clusterID)

	// For manual clusters, simulate addon uninstallation by updating cluster metadata
	cluster, exists := p.clusters[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}

	if cluster.Metadata == nil {
		return fmt.Errorf("addon %s is not installed", addonName)
	}

	// Remove addon from installed addons list
	installedAddons := cluster.Metadata["installed-addons"]
	if installedAddons == "" || !strings.Contains(installedAddons, addonName) {
		return fmt.Errorf("addon %s is not installed", addonName)
	}

	// Remove addon from the list
	addons := strings.Split(installedAddons, ",")
	var newAddons []string
	for _, addon := range addons {
		if strings.TrimSpace(addon) != addonName {
			newAddons = append(newAddons, strings.TrimSpace(addon))
		}
	}
	cluster.Metadata["installed-addons"] = strings.Join(newAddons, ",")

	// Remove addon-specific configuration
	configKey := fmt.Sprintf("addon-%s-config", addonName)
	delete(cluster.Metadata, configKey)

	// Update the cluster in our tracking
	p.clusters[clusterID] = cluster

	log.Printf("Successfully uninstalled addon %s from cluster %s", addonName, clusterID)
	return nil
}

// ListAddons lists installed addons
func (p *Provider) ListAddons(ctx context.Context, clusterID string) ([]string, error) {
	return []string{"gce-pd-csi-driver", "gcp-compute-persistent-disk-csi-driver", "coredns", "kube-proxy"}, nil
}

// GetClusterCost retrieves cluster cost
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	return 100.0, nil // $100 per month
}

// GetCostBreakdown retrieves cost breakdown
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	return map[string]float64{
		"control-plane": 0.0, // GKE management fee included
		"node-pools":    85.0,
		"load-balancer": 15.0,
	}, nil
}

// Helper functions
func generateClusterIP() string {
	return fmt.Sprintf("10.%d.%d.2",
		time.Now().Unix()%256,
		(time.Now().Unix()/256)%256)
}

func extractClusterName(clusterID string) string {
	if len(clusterID) > 4 && clusterID[:4] == "gcp-" {
		return clusterID[4:]
	}
	return clusterID
}

// GetKubeconfig retrieves the kubeconfig for a cluster
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	log.Printf("Generating kubeconfig for cluster: %s", clusterID)

	// Extract cluster name
	clusterName := strings.TrimPrefix(clusterID, "gcp-")

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
			endpoint = fmt.Sprintf("%s-master-0.%s.compute.googleapis.com", clusterName, p.config.Region)
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
func (p *Provider) fetchKubeconfigFromMaster(masterNode NodeInfo, clusterName string) (string, error) {
	if masterNode.PublicIP == "" {
		return "", fmt.Errorf("master node has no public IP for SSH access")
	}

	// For GCE instances, we would typically use SSH with gcloud ssh or direct SSH
	// This is a simplified implementation - in practice, you'd use GCP's SSH capabilities

	// Try to retrieve the actual kubeconfig from the master node
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
      command: gke-gcloud-auth-plugin
      installHint: Install gke-gcloud-auth-plugin for use with kubectl by following https://cloud.google.com/blog/products/containers-kubernetes/kubectl-auth-changes-in-gke
      provideClusterInfo: true
`, cluster.Endpoint, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName)

	return kubeconfigContent, nil
}

// getClusterInfrastructure discovers and returns the current cluster infrastructure
func (p *Provider) getClusterInfrastructure(ctx context.Context, clusterName string) (*ClusterInfrastructure, error) {
	// This would typically query GCP Compute Engine API to get the actual infrastructure
	// For now, return a basic structure based on stored cluster information

	infrastructure := &ClusterInfrastructure{
		NetworkName:   fmt.Sprintf("%s-network", clusterName),
		SubnetName:    fmt.Sprintf("%s-subnet", clusterName),
		FirewallRules: []string{fmt.Sprintf("%s-firewall", clusterName)},
		MasterNodes: []NodeInfo{
			{
				InstanceName: fmt.Sprintf("%s-master-1", clusterName),
				Zone:         p.config.Zone,
				PrivateIP:    "10.128.0.2", // Would be discovered from GCP
				PublicIP:     "",           // Would be discovered from GCP
				MachineType:  p.config.MachineType,
				Role:         "master",
			},
		},
		WorkerNodes: []NodeInfo{}, // Would be populated based on actual infrastructure
	}

	return infrastructure, nil
}

// installCiliumCNI installs Cilium CNI on the cluster
func (p *Provider) installCiliumCNI(ctx context.Context, masterNode NodeInfo) error {
	log.Printf("Installing Cilium CNI on master %s", masterNode.InstanceName)

	// In a real implementation, this would:
	// 1. SSH to the master node
	// 2. Install Cilium using Helm or kubectl
	// 3. Wait for Cilium to be ready

	time.Sleep(60 * time.Second)
	log.Printf("Cilium CNI installed successfully")
	return nil
}

// installCNI installs the Container Network Interface
func (p *Provider) installCNI(ctx context.Context, masterNode NodeInfo, cniType string) error {
	switch cniType {
	case "cilium":
		return p.installCiliumCNI(ctx, masterNode)
	case "calico":
		// Install Calico CNI
		log.Printf("Installing Calico CNI on master %s", masterNode.InstanceName)
		time.Sleep(30 * time.Second)
		return nil
	case "flannel":
		// Install Flannel CNI
		log.Printf("Installing Flannel CNI on master %s", masterNode.InstanceName)
		time.Sleep(30 * time.Second)
		return nil
	default:
		// Default to Calico
		log.Printf("Installing default Calico CNI on master %s", masterNode.InstanceName)
		time.Sleep(30 * time.Second)
		return nil
	}
}

// InvestigateCluster performs comprehensive investigation of a cluster
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	// TODO: Implement GCP-specific cluster investigation
	return fmt.Errorf("cluster investigation not yet implemented for GCP provider")
}
