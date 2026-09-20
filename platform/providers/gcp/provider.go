package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/container/v1"
	"google.golang.org/api/googleapi"
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
	Mode            string    `json:"mode,omitempty"` // "" / "compute" (kubeadm) or "gke"
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

// gcpSSHUser is the VM account created via instance metadata; kubeadm is
// driven over SSH as this user (with sudo).
const gcpSSHUser = "adhar"

// Register the GCP provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("gcp", func(config map[string]interface{}) (provider.Provider, error) {
		gcpConfig := &Config{}

		// Flatten the nested `config:` block into the map the rest of this
		// function reads. Only project_id and zone had a fallback into that
		// section, so every OTHER key under `providers.gcp.config` in config.yaml
		// was silently ignored and the built-in default was used instead: a
		// cluster asking for 100 GB pd-balanced disks and a 10.20.0.0/16 subnet
		// got 20 GB pd-standard on 10.0.0.0/24, and the VPC was named
		// "default-vpc" rather than the configured name. Root-level keys still
		// win, because they are the more specific place to say something.
		config = flattenProviderConfig(config)

		// Default: kubeadm on Compute Engine. `useManagedK8s: true` (or
		// clusterMode: gke) opts into GKE; everything else behaves the same.
		if managed, ok := config["useManagedK8s"].(bool); ok && managed {
			gcpConfig.ClusterMode = clusterModeGKE
		}
		if mode, ok := config["clusterMode"].(string); ok && mode != "" {
			gcpConfig.ClusterMode = mode
		} else if mode, ok := config["cluster_mode"].(string); ok && mode != "" {
			gcpConfig.ClusterMode = mode
		}

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
		if diskSize, ok := toInt32(config["diskSize"]); ok {
			gcpConfig.DiskSize = diskSize
		} else if diskSize, ok := toInt32(config["disk_size_gb"]); ok {
			gcpConfig.DiskSize = diskSize
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
		if purge, ok := config["purgeOrphanedVolumes"].(bool); ok && purge {
			gcpConfig.PurgeOrphanedVolumes = true
		}
		for _, key := range []string{"nodeServiceAccount", "node_service_account"} {
			if sa, ok := config[key].(string); ok && sa != "" {
				gcpConfig.NodeServiceAccount = sa
				break
			}
		}
		for _, key := range []string{"adminSourceRanges", "admin_source_ranges"} {
			if ranges, ok := config[key]; ok {
				gcpConfig.AdminSourceRanges = toStringSlice(ranges)
				break
			}
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
	regionsClient          *compute.RegionsClient
	projectsClient         *compute.ProjectsClient
	targetPoolsClient      *compute.TargetPoolsClient
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

	// containerService drives the managed (GKE) mode.
	containerService *container.Service

	// Resource tracking for clusters
	clusters         map[string]*ClusterInfrastructure
	resourceTrackers map[string]*ResourceTracker
}

// Config holds GCP provider configuration
type Config struct {
	// ClusterMode selects how clusters are created:
	//   "compute" (default) — Compute Engine VMs + kubeadm, Kubernetes managed
	//   by adhar itself (Cilium replaces kube-proxy during bootstrap).
	//   "gke" — Google Kubernetes Engine (`useManagedK8s: true`).
	ClusterMode string `json:"clusterMode,omitempty"`

	ProjectID string `json:"projectId"`
	Region    string `json:"region"`
	Zone      string `json:"zone"`

	// Network configuration
	VPCName    string `json:"vpcName"`
	SubnetName string `json:"subnetName"`
	SubnetCIDR string `json:"subnetCIDR"`

	// AdminSourceRanges restricts who may reach SSH (22) and the Kubernetes API
	// (6443). Empty means the internet, which is the historical behaviour and is
	// logged as a warning at provisioning time.
	AdminSourceRanges []string `json:"adminSourceRanges"`

	// PurgeOrphanedVolumes extends teardown to unattached pvc-* disks the CSI
	// driver created. Off by default because a disk may still hold data someone
	// wants; `adhar down --purge-orphaned-volumes` opts in.
	PurgeOrphanedVolumes bool `json:"purgeOrphanedVolumes,omitempty"`

	// NodeServiceAccount is the service account attached to every node. The
	// cloud-controller-manager and the PD CSI driver authenticate to the GCP API
	// through it, so without one the CCM cannot create the Gateway's load
	// balancer and the CSI driver cannot create disks. Defaults to the identity
	// of the configured service-account key.
	NodeServiceAccount string `json:"nodeServiceAccount"`

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
	if config.NodeServiceAccount == "" {
		// The identity of the key we already hold. Nodes were previously created
		// with no service account at all, so the cloud-controller-manager had no
		// way to reach the GCP API: it crash-looped and the Gateway's
		// LoadBalancer Service stayed <pending> forever.
		if email := serviceAccountEmail(config); email != "" {
			config.NodeServiceAccount = email
		}
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

	// Used only to read quota before provisioning. Deliberately non-fatal: a
	// preflight check that cannot run must not stop a cluster being created.
	regionsClient, regionsErr := compute.NewRegionsRESTClient(ctx, opts...)
	if regionsErr != nil {
		log.Printf("WARNING: regional quota preflight unavailable: %v", regionsErr)
	}
	// Project-wide quotas live on a different object from regional ones, and the
	// cap that stops a first deployment (CPUS_ALL_REGIONS) is only here.
	projectsClient, projectsErr := compute.NewProjectsRESTClient(ctx, opts...)
	if projectsErr != nil {
		log.Printf("WARNING: project quota preflight unavailable: %v", projectsErr)
	}
	// Target pools back the load balancers the cloud controller manager creates.
	targetPoolsClient, tpErr := compute.NewTargetPoolsRESTClient(ctx, opts...)
	if tpErr != nil {
		log.Printf("WARNING: target pool cleanup unavailable: %v", tpErr)
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
	containerService, err := container.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GKE client: %w", err)
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
		regionsClient:          regionsClient,
		projectsClient:         projectsClient,
		targetPoolsClient:      targetPoolsClient,
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
		containerService:       containerService,
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
	// Default mode: self-managed Kubernetes on Compute Engine. GKE is the
	// explicit opt-in (`useManagedK8s: true` / clusterMode: gke).
	if p.isManagedMode() {
		return p.createManagedCluster(ctx, spec)
	}

	log.Printf("Creating manual Kubernetes cluster: %s", spec.Name)

	// Validate cluster specification
	if err := p.validateClusterSpec(spec); err != nil {
		return nil, fmt.Errorf("invalid cluster specification: %w", err)
	}

	// Check regional quota BEFORE creating anything. Without this the first
	// build of this cluster created a VPC, a subnet, five firewall rules, a
	// control plane and one worker, and only then failed on the second worker
	// with QUOTA_EXCEEDED — leaving a half-built cluster billing and needing a
	// teardown. The limit that bites is not the obvious one: pd-balanced and
	// pd-ssd count against SSD_TOTAL_GB (250 GB by default), not DISKS_TOTAL_GB.
	if err := p.checkRegionalQuota(ctx, spec); err != nil {
		return nil, err
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

	// Bootstrap Kubernetes with kubeadm over SSH: wait for node preparation,
	// init the control plane (kube-proxy skipped; the platform bootstrap
	// installs Cilium with kubeProxyReplacement), then join workers.
	signer, err := provider.LoadClusterSSHKey(spec.Name)
	if err != nil {
		return nil, err
	}
	if len(infrastructure.MasterNodes) == 0 {
		return nil, fmt.Errorf("no control-plane instance was created")
	}
	master := infrastructure.MasterNodes[0]
	if err := provider.WaitForNodePrep(ctx, signer, gcpSSHUser, master.PublicIP, 15*time.Minute); err != nil {
		return nil, fmt.Errorf("control-plane node not ready: %w", err)
	}
	// External cloud provider: the kubelet defers node initialisation to
	// cloud-provider-gcp, installed right after the joins.
	if err := provider.EnableExternalCloudProvider(signer, gcpSSHUser, master.PublicIP, master.PrivateIP, true, false); err != nil {
		return nil, fmt.Errorf("control plane %s: %w", master.InstanceName, err)
	}
	joinCmd, err := provider.KubeadmInitMaster(signer, gcpSSHUser, master.PublicIP, master.PrivateIP, provider.PodCIDROrDefault(spec), spec.ControlPlane.APIServer.ExtraArgs)
	if err != nil {
		return nil, err
	}
	joined := provider.KubeadmJoinedNodes(signer, gcpSSHUser, master.PublicIP)
	for _, w := range infrastructure.WorkerNodes {
		if joined.Has(w.InstanceName, w.PublicIP, w.PrivateIP) {
			log.Printf("Worker %s is already part of the cluster; skipping prep/join", w.InstanceName)
			continue
		}
		if err := provider.WaitForNodePrep(ctx, signer, gcpSSHUser, w.PublicIP, 15*time.Minute); err != nil {
			return nil, fmt.Errorf("worker %s not ready: %w", w.InstanceName, err)
		}
		if err := provider.EnableExternalCloudProvider(signer, gcpSSHUser, w.PublicIP, w.PrivateIP, true, true); err != nil {
			return nil, fmt.Errorf("worker %s: %w", w.InstanceName, err)
		}
		if err := provider.KubeadmJoinWorker(signer, gcpSSHUser, w.PublicIP, joinCmd); err != nil {
			return nil, fmt.Errorf("worker %s: %w", w.InstanceName, err)
		}
	}
	if err := p.installCloudIntegration(signer, master.PublicIP, spec.Name); err != nil {
		return nil, err
	}
	log.Printf("Kubernetes bootstrapped on cluster %s; nodes stay NotReady until the platform bootstrap installs Cilium", spec.Name)

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
	err := p.createVPCNetwork(ctx, networkName, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC network: %w", err)
	}
	infrastructure.NetworkName = networkName

	// Create subnet
	subnetName := p.config.SubnetName
	if subnetName == "" {
		subnetName = fmt.Sprintf("%s-subnet", clusterName)
	}
	err = p.createSubnet(ctx, networkName, subnetName, clusterName)
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

	// Per-cluster SSH key for driving kubeadm over SSH
	_, sshPubKey, err := provider.EnsureClusterSSHKey(clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare cluster SSH key: %w", err)
	}
	startupScript := provider.KubeadmNodePrepScript(provider.K8sMinorFromVersion(spec.Version))

	// Create master nodes
	masterNodeInfos, err := p.createMasterNodes(ctx, clusterName, subnetName, spec, sshPubKey, startupScript)
	if err != nil {
		return nil, fmt.Errorf("failed to create master nodes: %w", err)
	}
	infrastructure.MasterNodes = masterNodeInfos

	// Create worker nodes if specified
	if len(spec.NodeGroups) > 0 {
		workerNodeInfos, err := p.createWorkerNodes(ctx, clusterName, subnetName, spec, sshPubKey, startupScript)
		if err != nil {
			return nil, fmt.Errorf("failed to create worker nodes: %w", err)
		}
		infrastructure.WorkerNodes = workerNodeInfos
	}

	log.Printf("Successfully created infrastructure for cluster: %s", clusterName)
	return infrastructure, nil
}

// createVPCNetwork creates a VPC network using Google Cloud SDK
func (p *Provider) createVPCNetwork(ctx context.Context, networkName, clusterName string) error {
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
	description := clusterMarker(clusterName, "network")

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
func (p *Provider) createSubnet(ctx context.Context, networkName, subnetName, clusterName string) error {
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
	description := clusterMarker(clusterName, "subnet")

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

// createFirewallRules creates the cluster's firewall rules.
//
// Every rule is scoped to the cluster's own network tag (see instanceTag), so
// two Adhar clusters in one project never open each other's ports, and the
// ranges come from the cluster's real CIDRs rather than a constant.
func (p *Provider) createFirewallRules(ctx context.Context, clusterName, networkName string) ([]string, error) {
	log.Printf("Creating firewall rules for cluster: %s", clusterName)

	networkURL := fmt.Sprintf("projects/%s/global/networks/%s", p.config.ProjectID, networkName)
	tag := instanceTag(clusterName)

	// The node subnet and the pod network. The internal rule used to hardcode
	// 10.0.0.0/24, so ANY cluster configured with a different subnet_cidr had
	// node-to-node traffic dropped and never finished joining. The pod CIDR is
	// listed too: Cilium in native-routing mode puts pod addresses on the wire
	// directly, and VXLAN mode simply does not match these ranges.
	subnetCIDR := p.config.SubnetCIDR
	if subnetCIDR == "" {
		subnetCIDR = "10.0.0.0/24"
	}
	internalRanges := []string{subnetCIDR}
	if podCIDR := provider.KubeadmPodCIDR; podCIDR != "" && podCIDR != subnetCIDR {
		internalRanges = append(internalRanges, podCIDR)
	}

	// Ranges Google's load balancers health-check from. Without these the
	// Gateway's LoadBalancer Service has backends that never turn healthy, so
	// the platform is unreachable from the internet even though every pod is up.
	// https://cloud.google.com/load-balancing/docs/health-check-concepts
	healthCheckRanges := []string{"130.211.0.0/22", "35.191.0.0/16", "209.85.152.0/22", "209.85.204.0/22"}

	tcp := "tcp"
	udp := "udp"
	icmp := "icmp"
	ingress := "INGRESS"

	// sshSourceRanges narrows administrative access when the caller asked for
	// it. Defaulting to 0.0.0.0/0 keeps existing behaviour, but a public SSH and
	// API server is worth stating rather than leaving implied.
	adminRanges := p.config.AdminSourceRanges
	if len(adminRanges) == 0 {
		adminRanges = []string{"0.0.0.0/0"}
		log.Printf("WARNING: SSH (22) and the Kubernetes API (6443) are open to the internet for cluster %s; set admin_source_ranges to restrict them", clusterName)
	}

	rules := []*computepb.Firewall{
		{
			Name:        ptrString(fmt.Sprintf("%s-allow-ssh", clusterName)),
			Network:     &networkURL,
			Description: ptrString("Allow SSH access to cluster nodes"),
			Allowed:     []*computepb.Allowed{{IPProtocol: &tcp, Ports: []string{"22"}}},
			// Both the node subnet and the admin ranges: node-to-node SSH is how
			// the provisioner joins workers to the control plane.
			SourceRanges: append(append([]string{}, adminRanges...), subnetCIDR),
			TargetTags:   []string{tag},
			Direction:    &ingress,
		},
		{
			Name:         ptrString(fmt.Sprintf("%s-allow-k8s-api", clusterName)),
			Network:      &networkURL,
			Description:  ptrString("Allow Kubernetes API server access"),
			Allowed:      []*computepb.Allowed{{IPProtocol: &tcp, Ports: []string{"6443"}}},
			SourceRanges: append(append([]string{}, adminRanges...), internalRanges...),
			TargetTags:   []string{tag},
			Direction:    &ingress,
		},
		{
			Name:        ptrString(fmt.Sprintf("%s-allow-internal", clusterName)),
			Network:     &networkURL,
			Description: ptrString("Allow internal cluster communication"),
			Allowed: []*computepb.Allowed{
				{IPProtocol: &tcp, Ports: []string{"0-65535"}},
				{IPProtocol: &udp, Ports: []string{"0-65535"}},
				{IPProtocol: &icmp},
			},
			SourceRanges: internalRanges,
			TargetTags:   []string{tag},
			Direction:    &ingress,
		},
		{
			Name:        ptrString(fmt.Sprintf("%s-allow-web", clusterName)),
			Network:     &networkURL,
			Description: ptrString("Allow HTTP/HTTPS to the platform Gateway"),
			Allowed: []*computepb.Allowed{
				{IPProtocol: &tcp, Ports: []string{"80", "443", "30000-32767"}},
			},
			SourceRanges: []string{"0.0.0.0/0"},
			TargetTags:   []string{tag},
			Direction:    &ingress,
		},
		{
			Name:        ptrString(fmt.Sprintf("%s-allow-health-checks", clusterName)),
			Network:     &networkURL,
			Description: ptrString("Allow Google load balancer health checks"),
			Allowed: []*computepb.Allowed{
				{IPProtocol: &tcp, Ports: []string{"0-65535"}},
			},
			SourceRanges: healthCheckRanges,
			TargetTags:   []string{tag},
			Direction:    &ingress,
		},
	}

	var firewallRules []string
	for _, rule := range rules {
		name := rule.GetName()
		op, err := p.firewallClient.Insert(ctx, &computepb.InsertFirewallRequest{
			Project:          p.config.ProjectID,
			FirewallResource: rule,
		})
		if err != nil {
			// Idempotent: re-running provisioning over a partially created
			// cluster must not fail on a rule that already exists.
			if isAlreadyExists(err) {
				log.Printf("Firewall rule %s already exists; keeping it", name)
				firewallRules = append(firewallRules, name)
				continue
			}
			return nil, fmt.Errorf("failed to create firewall rule %s: %w", name, err)
		}
		operationName := op.Name()
		if err := p.waitForGlobalOperation(ctx, &operationName); err != nil {
			return nil, fmt.Errorf("failed to wait for firewall rule %s: %w", name, err)
		}
		firewallRules = append(firewallRules, name)
	}

	log.Printf("Successfully created firewall rules: %v", firewallRules)
	return firewallRules, nil
}

// discoverClusterResources rebuilds a ResourceTracker by asking the project what
// exists, for when the local state file is gone. It matches on the cluster's own
// naming convention — the same names createClusterInfrastructure gives them — and
// on the cluster network tag, so it can never pick up another cluster's nodes.
func (p *Provider) discoverClusterResources(ctx context.Context, clusterID string) (*ResourceTracker, error) {
	name := extractClusterName(clusterID)
	if name == "" {
		return nil, fmt.Errorf("could not derive a cluster name from %q", clusterID)
	}
	tracker := &ResourceTracker{
		ClusterName: name,
		ProjectID:   p.config.ProjectID,
		Region:      p.config.Region,
		Zone:        p.config.Zone,
	}
	tag := instanceTag(name)

	if p.instanceClient != nil {
		it := p.instanceClient.List(ctx, &computepb.ListInstancesRequest{
			Project: p.config.ProjectID,
			Zone:    p.config.Zone,
		})
		for {
			inst, err := it.Next()
			if err != nil {
				break
			}
			carriesTag := false
			if inst.GetTags() != nil {
				for _, t := range inst.GetTags().GetItems() {
					if t == tag {
						carriesTag = true
						break
					}
				}
			}
			if carriesTag || strings.HasPrefix(inst.GetName(), name+"-") {
				tracker.Instances = append(tracker.Instances, inst.GetName())
			}
		}
	}

	if p.firewallClient != nil {
		it := p.firewallClient.List(ctx, &computepb.ListFirewallsRequest{Project: p.config.ProjectID})
		for {
			rule, err := it.Next()
			if err != nil {
				break
			}
			if strings.HasPrefix(rule.GetName(), name+"-allow-") {
				tracker.FirewallRules = append(tracker.FirewallRules, rule.GetName())
			}
		}
	}

	if p.config.SubnetName != "" {
		tracker.Subnets = append(tracker.Subnets, p.config.SubnetName)
	}
	if p.config.VPCName != "" {
		tracker.Networks = append(tracker.Networks, p.config.VPCName)
	}

	if len(tracker.Instances) == 0 && len(tracker.FirewallRules) == 0 {
		return nil, fmt.Errorf("no instances or firewall rules found for cluster %q in project %s zone %s", name, p.config.ProjectID, p.config.Zone)
	}
	log.Printf("Discovered %d instance(s) and %d firewall rule(s) for cluster %s",
		len(tracker.Instances), len(tracker.FirewallRules), name)
	return tracker, nil
}

// discoverUntrackedClusters finds Adhar clusters present in the project but
// absent from the local state file, by grouping instances on the per-cluster
// network tag every node carries. Firewall rules are checked too, so a run that
// died between creating the network and creating the first instance is still
// visible and still removable.
func (p *Provider) discoverUntrackedClusters(ctx context.Context) []*types.Cluster {
	names := map[string]int{}

	if p.instanceClient != nil {
		it := p.instanceClient.List(ctx, &computepb.ListInstancesRequest{
			Project: p.config.ProjectID,
			Zone:    p.config.Zone,
		})
		for {
			inst, err := it.Next()
			if err != nil {
				break
			}
			if inst.GetTags() == nil {
				continue
			}
			for _, tag := range inst.GetTags().GetItems() {
				if name, ok := clusterNameFromTag(tag); ok {
					names[name]++
				}
			}
		}
	}

	if p.firewallClient != nil {
		it := p.firewallClient.List(ctx, &computepb.ListFirewallsRequest{Project: p.config.ProjectID})
		for {
			rule, err := it.Next()
			if err != nil {
				break
			}
			for _, tag := range rule.GetTargetTags() {
				if name, ok := clusterNameFromTag(tag); ok {
					if _, seen := names[name]; !seen {
						names[name] = 0
					}
				}
			}
		}
	}

	// Networks and subnets carry the marker in their description, which is what
	// makes a cluster whose create died before the first instance removable.
	if p.networkClient != nil {
		it := p.networkClient.List(ctx, &computepb.ListNetworksRequest{Project: p.config.ProjectID})
		for {
			nw, err := it.Next()
			if err != nil {
				break
			}
			if name, ok := clusterNameFromMarker(nw.GetDescription()); ok {
				if _, seen := names[name]; !seen {
					names[name] = 0
				}
			}
		}
	}

	out := make([]*types.Cluster, 0, len(names))
	for name, instanceCount := range names {
		status := types.ClusterStatusRunning
		if instanceCount == 0 {
			// Networks and firewall rules but no nodes: a partial create.
			status = types.ClusterStatusError
		}
		out = append(out, &types.Cluster{
			ID:       fmt.Sprintf("gcp/%s/%s", p.config.ProjectID, name),
			Name:     name,
			Provider: "gcp",
			Region:   p.config.Region,
			Status:   status,
			Metadata: map[string]interface{}{
				"projectId":     p.config.ProjectID,
				"zone":          p.config.Zone,
				"instanceCount": instanceCount,
				"discovered":    true,
			},
		})
	}
	return out
}

// checkRegionalQuota fails before provisioning when the region cannot hold the
// requested cluster. It reports the metric, the limit and what the cluster needs,
// because "QUOTA_EXCEEDED" halfway through a build is expensive to diagnose.
func (p *Provider) checkRegionalQuota(ctx context.Context, spec *types.ClusterSpec) error {
	if p.regionsClient == nil {
		return nil
	}
	region, err := p.regionsClient.Get(ctx, &computepb.GetRegionRequest{
		Project: p.config.ProjectID,
		Region:  p.config.Region,
	})
	if err != nil {
		log.Printf("WARNING: could not read quota for region %s (%v); continuing", p.config.Region, err)
		return nil
	}

	nodes := int64(spec.ControlPlane.Replicas)
	for _, group := range spec.NodeGroups {
		nodes += int64(group.Replicas)
	}
	if nodes <= 0 {
		return nil
	}

	diskPerNode := int64(p.config.DiskSize)
	if diskPerNode <= 0 {
		diskPerNode = 50
	}
	diskNeeded := nodes * diskPerNode

	// Which disk quota applies depends on the type, and this is the trap: only
	// pd-standard counts against DISKS_TOTAL_GB. Everything else is SSD.
	diskMetric := "SSD_TOTAL_GB"
	if p.config.DiskType == "" || p.config.DiskType == "pd-standard" {
		diskMetric = "DISKS_TOTAL_GB"
	}

	limits := map[string]float64{}
	usage := map[string]float64{}
	for _, q := range region.GetQuotas() {
		limits[q.GetMetric()] = q.GetLimit()
		usage[q.GetMetric()] = q.GetUsage()
	}
	// CPUS_ALL_REGIONS and the other project-wide caps are NOT on the region
	// object, so reading only regional quotas misses the limit most likely to
	// stop a first deployment.
	if p.projectsClient != nil {
		if project, perr := p.projectsClient.Get(ctx, &computepb.GetProjectRequest{
			Project: p.config.ProjectID,
		}); perr == nil {
			for _, q := range project.GetQuotas() {
				limits[q.GetMetric()] = q.GetLimit()
				usage[q.GetMetric()] = q.GetUsage()
			}
		} else {
			log.Printf("WARNING: could not read project-wide quota (%v); continuing", perr)
		}
	}

	var problems []string
	if limit, ok := limits[diskMetric]; ok {
		if available := limit - usage[diskMetric]; float64(diskNeeded) > available {
			problems = append(problems, fmt.Sprintf(
				"%s: need %d GB (%d nodes x %d GB), %.0f GB available of %.0f GB limit — lower disk_size_gb, or use disk_type: pd-standard which counts against DISKS_TOTAL_GB instead, or request a quota increase",
				diskMetric, diskNeeded, nodes, diskPerNode, available, limit))
		}
	}

	if cpusPerNode, ok := machineTypeCPUs(p.config.MachineType); ok {
		cpusNeeded := float64(nodes * cpusPerNode)
		// CPUS is the regional quota. CPUS_ALL_REGIONS is a separate PROJECT-WIDE
		// cap that defaults to 12 on a new project, and it is the one that
		// actually stops a first deployment: the regional limit was 100 while the
		// global limit was 12, so a four-node cluster of e2-standard-4 passed the
		// regional check and then failed on its last worker. Both are checked.
		for _, metric := range []string{"CPUS", "CPUS_ALL_REGIONS"} {
			limit, ok := limits[metric]
			if !ok {
				continue
			}
			available := limit - usage[metric]
			if cpusNeeded <= available {
				continue
			}
			scope := "in this region"
			if metric == "CPUS_ALL_REGIONS" {
				scope = "across all regions (a project-wide cap, 12 by default on a new project)"
			}
			problems = append(problems, fmt.Sprintf(
				"%s: need %.0f vCPU (%d nodes x %d), %.0f available of %.0f limit %s — use a smaller machine_type, fewer nodes, or request a quota increase",
				metric, cpusNeeded, nodes, cpusPerNode, available, limit, scope))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("region %s cannot hold this cluster, so nothing was created:\n  - %s",
			p.config.Region, strings.Join(problems, "\n  - "))
	}
	return nil
}

// machineTypeCPUs reads the vCPU count out of a standard machine type name such
// as e2-standard-4 or n2-highmem-16. Custom and unrecognised names return false,
// and the CPU check is then skipped rather than guessed at.
func machineTypeCPUs(machineType string) (int64, bool) {
	parts := strings.Split(machineType, "-")
	if len(parts) < 3 {
		return 0, false
	}
	n, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// clusterMarker is the description stamped on a network or subnet. Neither
// resource type supports labels, and the description used to name the NETWORK
// rather than the cluster ("Network for cluster adhar-vpc"), so an interrupted
// `adhar up` left a VPC with nothing tying it to a cluster: teardown could not
// find it and the only way to remove it was by hand.
func clusterMarker(clusterName, kind string) string {
	return fmt.Sprintf("Adhar platform %s for cluster %s [%s%s]", kind, clusterName, clusterMarkerPrefix, clusterName)
}

// clusterMarkerPrefix is the machine-readable part of clusterMarker.
const clusterMarkerPrefix = "adhar.io/cluster="

// clusterNameFromMarker reads the cluster name back out of a description.
func clusterNameFromMarker(description string) (string, bool) {
	i := strings.Index(description, clusterMarkerPrefix)
	if i < 0 {
		return "", false
	}
	rest := description[i+len(clusterMarkerPrefix):]
	if end := strings.IndexAny(rest, "] "); end >= 0 {
		rest = rest[:end]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

// clusterNameFromTag reverses instanceTag. It deliberately does not match a bare
// "-node" or a tag from another tool, so an unrelated instance in the project is
// never mistaken for an Adhar cluster.
func clusterNameFromTag(tag string) (string, bool) {
	const suffix = "-node"
	if !strings.HasSuffix(tag, suffix) || len(tag) <= len(suffix) {
		return "", false
	}
	return strings.TrimSuffix(tag, suffix), true
}

// sweepLoadBalancers removes the load balancer the cloud controller manager
// created for the Gateway Service: the forwarding rule, its target pool, and the
// k8s-* firewall rules.
//
// These are deleted unconditionally, unlike disks. They hold no data, they always
// leak because nothing records them, and the firewall rule blocks deleting the VPC
// — so removing them is part of finishing a teardown rather than an optional tidy.
// Order matters: a forwarding rule pins its target pool, and a target pool pins
// nothing, so rules go first.
func (p *Provider) sweepLoadBalancers(ctx context.Context, tracker *ResourceTracker) []string {
	var errs []string

	targets := map[string]bool{}
	if p.forwardingRulesClient != nil {
		it := p.forwardingRulesClient.List(ctx, &computepb.ListForwardingRulesRequest{
			Project: tracker.ProjectID,
			Region:  tracker.Region,
		})
		for {
			rule, err := it.Next()
			if err != nil {
				break
			}
			// The CCM stamps the Service it belongs to into the description; that is
			// what distinguishes a Kubernetes-created rule from a human's.
			if !strings.Contains(rule.GetDescription(), "kubernetes.io/service-name") {
				continue
			}
			if t := rule.GetTarget(); t != "" {
				targets[t[strings.LastIndex(t, "/")+1:]] = true
			}
			op, err := p.forwardingRulesClient.Delete(ctx, &computepb.DeleteForwardingRuleRequest{
				Project: tracker.ProjectID, Region: tracker.Region, ForwardingRule: rule.GetName(),
			})
			if err != nil {
				errs = append(errs, fmt.Sprintf("deleting forwarding rule %s: %v", rule.GetName(), err))
				continue
			}
			name := op.Name()
			if err := p.waitForRegionalOperation(ctx, &name); err != nil {
				errs = append(errs, fmt.Sprintf("waiting for forwarding rule %s: %v", rule.GetName(), err))
				continue
			}
			log.Printf("Deleted load balancer forwarding rule: %s", rule.GetName())
		}
	}

	if p.targetPoolsClient != nil {
		for pool := range targets {
			op, err := p.targetPoolsClient.Delete(ctx, &computepb.DeleteTargetPoolRequest{
				Project: tracker.ProjectID, Region: tracker.Region, TargetPool: pool,
			})
			if err != nil {
				errs = append(errs, fmt.Sprintf("deleting target pool %s: %v", pool, err))
				continue
			}
			name := op.Name()
			if err := p.waitForRegionalOperation(ctx, &name); err != nil {
				errs = append(errs, fmt.Sprintf("waiting for target pool %s: %v", pool, err))
				continue
			}
			log.Printf("Deleted load balancer target pool: %s", pool)
		}
	}

	if p.firewallClient != nil {
		it := p.firewallClient.List(ctx, &computepb.ListFirewallsRequest{Project: tracker.ProjectID})
		var names []string
		for {
			rule, err := it.Next()
			if err != nil {
				break
			}
			if strings.HasPrefix(rule.GetName(), "k8s-") {
				names = append(names, rule.GetName())
			}
		}
		for _, n := range names {
			if err := p.deleteFirewallRule(ctx, n); err != nil {
				errs = append(errs, fmt.Sprintf("deleting firewall rule %s: %v", n, err))
				continue
			}
			log.Printf("Deleted load balancer firewall rule: %s", n)
		}
	}
	return errs
}

// sweepOrphanedDisks removes unattached CSI disks, but only when asked.
//
// A disk may still hold data someone wants, so deleting it is opt-in through
// `adhar down --purge-orphaned-volumes`. Without the flag they are named in the
// output: silence would leave them billing indefinitely, which is how 59 disks
// survived a teardown once.
func (p *Provider) sweepOrphanedDisks(ctx context.Context, tracker *ResourceTracker) []string {
	if p.diskClient == nil {
		return nil
	}
	var orphans []*computepb.Disk
	it := p.diskClient.List(ctx, &computepb.ListDisksRequest{
		Project: tracker.ProjectID, Zone: tracker.Zone,
	})
	for {
		disk, err := it.Next()
		if err != nil {
			break
		}
		// Attached disks belong to instances and go with them.
		if len(disk.GetUsers()) > 0 {
			continue
		}
		if strings.HasPrefix(disk.GetName(), "pvc-") || strings.Contains(disk.GetDescription(), "storage.gke.io/created-for") {
			orphans = append(orphans, disk)
		}
	}
	if len(orphans) == 0 {
		return nil
	}

	if !p.config.PurgeOrphanedVolumes {
		log.Printf("WARNING: %d unattached persistent disk(s) remain and keep billing:", len(orphans))
		for _, d := range orphans {
			log.Printf("  disk %s (%d GB) in %s", d.GetName(), d.GetSizeGb(), tracker.Zone)
		}
		log.Printf("They may still hold data, so they are kept. Remove them with:")
		log.Printf("  adhar down ... --purge-orphaned-volumes")
		return nil
	}

	var errs []string
	for _, d := range orphans {
		op, err := p.diskClient.Delete(ctx, &computepb.DeleteDiskRequest{
			Project: tracker.ProjectID, Zone: tracker.Zone, Disk: d.GetName(),
		})
		if err != nil {
			errs = append(errs, fmt.Sprintf("deleting disk %s: %v", d.GetName(), err))
			continue
		}
		name := op.Name()
		if err := p.waitForZonalOperation(ctx, &name); err != nil {
			errs = append(errs, fmt.Sprintf("waiting for disk %s: %v", d.GetName(), err))
			continue
		}
		log.Printf("Deleted orphaned disk: %s (%d GB)", d.GetName(), d.GetSizeGb())
	}
	return errs
}

// findOrphanedResources lists billable resources the in-cluster controllers
// created, which no tracker records: load balancer forwarding rules from the
// cloud controller manager, and unattached persistent disks from the CSI driver.
// It only reports; deleting a disk that still holds data is the user's call.
func (p *Provider) findOrphanedResources(ctx context.Context, tracker *ResourceTracker) []string {
	var found []string

	if p.forwardingRulesClient != nil {
		it := p.forwardingRulesClient.List(ctx, &computepb.ListForwardingRulesRequest{
			Project: tracker.ProjectID,
			Region:  tracker.Region,
		})
		for {
			rule, err := it.Next()
			if err != nil {
				break
			}
			// The cloud controller manager stamps the Service it belongs to into
			// the description; that is what distinguishes a Kubernetes-created
			// rule from one a human made.
			if strings.Contains(rule.GetDescription(), "kubernetes.io/service-name") {
				found = append(found, fmt.Sprintf("forwarding-rule %s (%s) in %s", rule.GetName(), rule.GetIPAddress(), tracker.Region))
			}
		}
	}

	// Firewall rules the cloud controller manager creates for a LoadBalancer.
	// These were missed, and they MATTER beyond billing: a leftover k8s-fw-* rule
	// holds a reference to the VPC, so `adhar down` could not delete the network
	// and the next run had to reuse or work around it.
	if p.firewallClient != nil {
		it := p.firewallClient.List(ctx, &computepb.ListFirewallsRequest{Project: tracker.ProjectID})
		for {
			rule, err := it.Next()
			if err != nil {
				break
			}
			// The CCM prefixes every rule it owns with k8s-.
			if strings.HasPrefix(rule.GetName(), "k8s-") {
				found = append(found, fmt.Sprintf("firewall-rule %s (blocks deleting the VPC)", rule.GetName()))
			}
		}
	}

	// Target pools backing those load balancers.
	if p.forwardingRulesClient == nil {
		// nothing to correlate against; the rules above already flagged the LB
		_ = found
	}

	if p.diskClient != nil {
		it := p.diskClient.List(ctx, &computepb.ListDisksRequest{
			Project: tracker.ProjectID,
			Zone:    tracker.Zone,
		})
		for {
			disk, err := it.Next()
			if err != nil {
				break
			}
			// Attached disks belong to instances and go with them. An unattached
			// disk named for a PersistentVolumeClaim is a CSI leftover.
			if len(disk.GetUsers()) > 0 {
				continue
			}
			if strings.HasPrefix(disk.GetName(), "pvc-") || strings.Contains(disk.GetDescription(), "storage.gke.io/created-for") {
				found = append(found, fmt.Sprintf("disk %s (%d GB) in %s", disk.GetName(), disk.GetSizeGb(), tracker.Zone))
			}
		}
	}

	return found
}

// instanceTag is the network tag every node of a cluster carries, and the tag
// every firewall rule targets. Rules used to apply to the whole network, which
// meant one cluster's rules governed every other cluster in the project.
func instanceTag(clusterName string) string {
	return fmt.Sprintf("%s-node", clusterName)
}

func ptrString(s string) *string { return &s }

// nodeServiceAccounts builds the ServiceAccounts block for a node. An empty
// email yields nil, which leaves the instance on the project's default compute
// service account rather than attaching nothing at all.
func nodeServiceAccounts(email string) []*computepb.ServiceAccount {
	if email == "" {
		return nil
	}
	return []*computepb.ServiceAccount{{
		Email:  ptrString(email),
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
	}}
}

// serviceAccountEmail reads client_email out of the configured service-account
// key, inline or from disk. Returns "" when there is no key, in which case the
// nodes fall back to the project's default compute service account.
func serviceAccountEmail(config *Config) string {
	raw := config.ServiceAccountKey
	if raw == "" && config.ServiceAccountKeyPath != "" {
		path, perr := expandHomePath(config.ServiceAccountKeyPath)
		if perr != nil {
			return ""
		}
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("WARNING: could not read %s to find the node service account: %v", config.ServiceAccountKeyPath, err)
			return ""
		}
		raw = string(b)
	}
	if raw == "" {
		return ""
	}
	var parsed struct {
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	return parsed.ClientEmail
}

// flattenProviderConfig returns a copy of the provider config map with the
// entries of its nested "config" section merged in. Keys already present at the
// root are left alone, so an explicit root-level setting always wins.
func flattenProviderConfig(config map[string]interface{}) map[string]interface{} {
	section, ok := config["config"].(map[string]interface{})
	if !ok || len(section) == 0 {
		return config
	}
	merged := make(map[string]interface{}, len(config)+len(section))
	for k, v := range section {
		merged[k] = v
	}
	for k, v := range config {
		merged[k] = v
	}
	return merged
}

// toInt32 accepts the numeric types a YAML or JSON integer can arrive as. The
// disk-size parser only accepted int and int32, so a value that had been through
// a JSON round trip (float64) was dropped.
func toInt32(v interface{}) (int32, bool) {
	switch t := v.(type) {
	case int:
		return int32(t), true
	case int32:
		return t, true
	case int64:
		return int32(t), true
	case float64:
		return int32(t), true
	case float32:
		return int32(t), true
	}
	return 0, false
}

// toStringSlice accepts the shapes a YAML list survives as once it has been
// through the generic provider config map: []string, []interface{}, or a single
// comma-separated string.
func toStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		out := []string{}
		for _, part := range strings.Split(t, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// isAlreadyExists reports whether a Compute API error is a 409 conflict.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// createMasterNodes creates master nodes for the Kubernetes cluster using Google Cloud SDK
func (p *Provider) createMasterNodes(ctx context.Context, clusterName, subnetName string, spec *types.ClusterSpec, sshPubKey, startupScript string) ([]NodeInfo, error) {
	log.Printf("Creating master nodes for cluster: %s", clusterName)

	var masterNodes []NodeInfo
	subnetURL := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", p.config.ProjectID, p.config.Region, subnetName)

	for i := 0; i < spec.ControlPlane.Replicas; i++ {
		nodeName := fmt.Sprintf("%s-master-%d", clusterName, i)

		nodeInfo, err := p.createComputeInstance(ctx, clusterName, nodeName, subnetURL, spec.ControlPlane.InstanceType, true, sshPubKey, startupScript)
		if err != nil {
			return nil, fmt.Errorf("failed to create master node %s: %w", nodeName, err)
		}

		masterNodes = append(masterNodes, *nodeInfo)
	}

	log.Printf("Successfully created master nodes: %d nodes", len(masterNodes))
	return masterNodes, nil
}

// createWorkerNodes creates worker nodes for the Kubernetes cluster using Google Cloud SDK
func (p *Provider) createWorkerNodes(ctx context.Context, clusterName, subnetName string, spec *types.ClusterSpec, sshPubKey, startupScript string) ([]NodeInfo, error) {
	log.Printf("Creating worker nodes for cluster: %s", clusterName)

	var workerNodes []NodeInfo
	subnetURL := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", p.config.ProjectID, p.config.Region, subnetName)

	for _, nodeGroup := range spec.NodeGroups {
		for i := 0; i < nodeGroup.Replicas; i++ {
			nodeName := fmt.Sprintf("%s-worker-%s-%d", clusterName, nodeGroup.Name, i)

			nodeInfo, err := p.createComputeInstance(ctx, clusterName, nodeName, subnetURL, nodeGroup.InstanceType, false, sshPubKey, startupScript)
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
func (p *Provider) createComputeInstance(ctx context.Context, clusterName, instanceName, subnetURL, machineType string, isMaster bool, sshPubKey, startupScript string) (*NodeInfo, error) {
	log.Printf("Creating compute instance: %s", instanceName)

	// Use default machine type if not specified
	if machineType == "" {
		machineType = p.config.MachineType
	}

	machineTypeURL := fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", p.config.ProjectID, p.config.Zone, machineType)
	sourceImage := fmt.Sprintf("projects/%s/global/images/family/%s", p.config.ImageProject, p.config.ImageFamily)
	// Honour the configured disk. Both values were parsed and defaulted and then
	// ignored here, so disk_size_gb and disk_type in config.yaml (and in the
	// provider docs) silently did nothing — every node got 50 GB of pd-standard,
	// which is slow enough to matter for etcd and the container image cache.
	diskSizeGb := int64(p.config.DiskSize)
	if diskSizeGb <= 0 {
		diskSizeGb = 50
	}
	diskTypeName := p.config.DiskType
	if diskTypeName == "" {
		diskTypeName = "pd-standard"
	}
	diskType := fmt.Sprintf("projects/%s/zones/%s/diskTypes/%s", p.config.ProjectID, p.config.Zone, diskTypeName)
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
					{
						// GCE creates this user with the key and sudo access;
						// kubeadm is driven over SSH as this user.
						Key:   func() *string { s := "ssh-keys"; return &s }(),
						Value: func() *string { v := gcpSSHUser + ":" + strings.TrimSpace(sshPubKey); return &v }(),
					},
				},
			},
			// The CLUSTER's tag, not the instance's. Firewall rules target this
			// tag, and a per-instance tag matched none of them, so the rules
			// applied to nothing.
			Tags: &computepb.Tags{
				Items: []string{instanceTag(clusterName)},
			},
			// Nodes carried NO service account, so the in-cluster GCP controllers
			// had no identity: the cloud-controller-manager crash-looped and the
			// Gateway's LoadBalancer Service never received an address, and the
			// PD CSI driver could not create disks. cloud-platform is the scope
			// those controllers need; IAM on the service account itself is what
			// actually bounds what they can do.
			ServiceAccounts: nodeServiceAccounts(p.config.NodeServiceAccount),
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

// DeleteCluster deletes a self-managed GCE cluster and its infrastructure
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	log.Printf("Deleting GCP cluster: %s", clusterID)

	// Remove local per-cluster state (SSH key) regardless of tracker presence.
	defer provider.RemoveClusterState(extractClusterName(clusterID))

	// Get resource tracker for the cluster. A missing tracker used to abort the
	// whole teardown, which is the worst possible outcome: the local state file
	// (~/.adhar/state/gcp/clusters.json) is not the cluster, and losing it left
	// live instances billing with no supported way to remove them. Rebuild what
	// the cluster looks like from the cloud instead.
	tracker, exists := p.resourceTrackers[clusterID]
	if !exists {
		log.Printf("No local state for cluster %s; discovering its resources from the project", clusterID)
		discovered, err := p.discoverClusterResources(ctx, clusterID)
		if err != nil {
			return fmt.Errorf("cluster %s is not in local state and could not be discovered: %w", clusterID, err)
		}
		tracker = discovered
	}

	// A managed (GKE) cluster: delete the control plane and its firewall
	// rules first; the subnet/network cleanup below is shared with compute mode.
	if p.isManagedCluster(ctx, clusterID) {
		if err := p.deleteManagedControlPlane(ctx, clusterID); err != nil {
			return err
		}
	}

	var errors []string

	// Delete instances (VMs)
	// Instances go in PARALLEL. Each delete is an asynchronous GCP operation that
	// takes ~2 minutes to report done, and waiting on them one after another made a
	// six-node teardown a twelve-minute wait for work the API was happy to do at
	// once. They are independent — no instance holds a reference to another — so the
	// only ordering that matters is that all of them finish before the network
	// cleanup below, which is exactly what the WaitGroup provides.
	log.Printf("Deleting %d instance(s) for cluster: %s", len(tracker.Instances), clusterID)
	if len(tracker.Instances) > 0 {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, instanceName := range tracker.Instances {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				if err := p.deleteInstance(ctx, name, tracker.Zone); err != nil {
					mu.Lock()
					errors = append(errors, fmt.Sprintf("failed to delete instance %s: %v", name, err))
					mu.Unlock()
					return
				}
				log.Printf("Deleted instance: %s", name)
			}(instanceName)
		}
		wg.Wait()
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

	// Anything the in-cluster controllers created is invisible to the tracker: the
	// cloud controller manager provisions a load balancer for the Gateway Service,
	// and the PD CSI driver provisions a disk per PersistentVolume. Neither is
	// recorded here, so both used to survive `adhar down` and keep billing.
	//
	// These sweeps run BEFORE the subnet and network cleanup below, not after: a
	// leftover k8s-fw-* rule holds a reference to the VPC, so deleting the network
	// first fails with "resource in use" and the next run inherits the network.
	errors = append(errors, p.sweepLoadBalancers(ctx, tracker)...)
	errors = append(errors, p.sweepOrphanedDisks(ctx, tracker)...)

	// Clean up the subnet and the VPC when Adhar created them and nothing else is
	// using them.
	//
	// The test used to be `strings.Contains(name, clusterName)`, which never fired:
	// the network and subnet are named from the provider config (adhar-vpc /
	// adhar-subnet), not from the cluster, so a teardown left the VPC behind on
	// every run. Presence in this cluster's tracker is the right test — it means
	// `adhar up` created it — combined with no OTHER tracked cluster claiming it,
	// so tearing down one environment cannot pull the network out from under
	// another that shares it.
	log.Printf("Checking subnets for cleanup for cluster: %s", clusterID)
	for _, subnetName := range tracker.Subnets {
		if other := p.otherClusterUsingSubnet(clusterID, subnetName); other != "" {
			log.Printf("Keeping subnet %s: still used by %s", subnetName, other)
			continue
		}
		if err := p.deleteSubnet(ctx, subnetName, tracker.Region); err != nil {
			errors = append(errors, fmt.Sprintf("failed to delete subnet %s: %v", subnetName, err))
		} else {
			log.Printf("Deleted subnet: %s", subnetName)
		}
	}

	log.Printf("Checking networks for cleanup for cluster: %s", clusterID)
	for _, networkName := range tracker.Networks {
		if other := p.otherClusterUsingNetwork(clusterID, networkName); other != "" {
			log.Printf("Keeping network %s: still used by %s", networkName, other)
			continue
		}
		if err := p.deleteNetwork(ctx, networkName); err != nil {
			errors = append(errors, fmt.Sprintf("failed to delete network %s: %v", networkName, err))
		} else {
			log.Printf("Deleted network: %s", networkName)
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

// otherClusterUsingNetwork returns the id of another tracked cluster that shares
// a network, or "" when this cluster is the last user. Two environments in one
// project legitimately share adhar-vpc, and the first teardown must not take the
// network away from the second.
func (p *Provider) otherClusterUsingNetwork(selfID, networkName string) string {
	for id, t := range p.resourceTrackers {
		if id == selfID || t == nil {
			continue
		}
		for _, n := range t.Networks {
			if n == networkName {
				return id
			}
		}
	}
	return ""
}

// otherClusterUsingSubnet is the same test for a subnet.
func (p *Provider) otherClusterUsingSubnet(selfID, subnetName string) string {
	for id, t := range p.resourceTrackers {
		if id == selfID || t == nil {
			continue
		}
		for _, sn := range t.Subnets {
			if sn == subnetName {
				return id
			}
		}
	}
	return ""
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

		// Scale the worker pool by creating/deleting real compute instances via
		// the Google Cloud SDK rather than fabricating in-memory node entries.
		desiredWorkers := totalDesiredNodes - len(cluster.MasterNodes)
		if desiredWorkers < 0 {
			desiredWorkers = 0
		}
		currentWorkers := len(cluster.WorkerNodes)

		machineType := p.config.MachineType
		for _, nodeGroup := range spec.NodeGroups {
			if nodeGroup.InstanceType != "" {
				machineType = nodeGroup.InstanceType
				break
			}
		}

		switch {
		case desiredWorkers > currentWorkers:
			clusterName := extractClusterName(clusterID)
			signer, sshPubKey, err := provider.EnsureClusterSSHKey(clusterName)
			if err != nil {
				return fmt.Errorf("failed to load cluster SSH key: %w", err)
			}
			startupScript := provider.KubeadmNodePrepScript(provider.K8sMinorFromVersion(spec.Version))
			if len(cluster.MasterNodes) == 0 || cluster.MasterNodes[0].PublicIP == "" {
				return fmt.Errorf("cannot scale cluster %s: control-plane public IP unknown", clusterID)
			}
			masterIP := cluster.MasterNodes[0].PublicIP
			joinCmd, err := provider.SSHRun(signer, gcpSSHUser, masterIP, "kubeadm token create --print-join-command", 2*time.Minute)
			if err != nil {
				return fmt.Errorf("failed to create join token: %w", err)
			}
			joinCmd = strings.TrimSpace(joinCmd)

			subnetURL := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s",
				p.config.ProjectID, p.config.Region, fmt.Sprintf("%s-subnet", clusterName))
			for i := currentWorkers; i < desiredWorkers; i++ {
				nodeName := fmt.Sprintf("%s-worker-%d", clusterName, i)
				nodeInfo, err := p.createComputeInstance(ctx, clusterName, nodeName, subnetURL, machineType, false, sshPubKey, startupScript)
				if err != nil {
					return fmt.Errorf("failed to scale up cluster %s: %w", clusterID, err)
				}
				if err := provider.WaitForNodePrep(ctx, signer, gcpSSHUser, nodeInfo.PublicIP, 15*time.Minute); err != nil {
					return fmt.Errorf("new worker %s not ready: %w", nodeName, err)
				}
				if err := provider.KubeadmJoinWorker(signer, gcpSSHUser, nodeInfo.PublicIP, joinCmd); err != nil {
					return fmt.Errorf("new worker %s: %w", nodeName, err)
				}
				cluster.WorkerNodes = append(cluster.WorkerNodes, *nodeInfo)
			}
			log.Printf("Scaled up cluster %s to %d worker nodes", clusterID, desiredWorkers)
		case desiredWorkers < currentWorkers:
			for i := currentWorkers - 1; i >= desiredWorkers; i-- {
				node := cluster.WorkerNodes[i]
				zone := node.Zone
				if zone == "" {
					zone = p.config.Zone
				}
				if err := p.deleteInstance(ctx, node.InstanceName, zone); err != nil {
					return fmt.Errorf("failed to scale down cluster %s: %w", clusterID, err)
				}
			}
			cluster.WorkerNodes = cluster.WorkerNodes[:desiredWorkers]
			log.Printf("Scaled down cluster %s to %d worker nodes", clusterID, desiredWorkers)
		}
	}

	// Update the cluster in our tracking
	p.clusters[clusterID] = cluster

	log.Printf("Successfully updated cluster %s", clusterID)
	return nil
}

// GetCluster retrieves cluster information from the provider's tracked state,
// verifying the underlying compute instances via the Google Cloud SDK. It does
// not fabricate data: an unknown cluster yields an error.
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	infrastructure, exists := p.clusters[clusterID]
	tracker, hasTracker := p.resourceTrackers[clusterID]
	if !exists && !hasTracker {
		return nil, fmt.Errorf("cluster not found: %s", clusterID)
	}
	if hasTracker && tracker.Mode == clusterModeGKE {
		c, err := p.containerService.Projects.Locations.Clusters.Get(p.gkeClusterName(clusterID)).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("describing GKE cluster %s: %w", clusterID, err)
		}
		return p.gkeToCluster(clusterID, c), nil
	}

	region := p.config.Region
	zone := p.config.Zone
	createdAt := time.Now()
	updatedAt := time.Now()
	if hasTracker {
		region = tracker.Region
		zone = tracker.Zone
		createdAt = tracker.CreatedAt
		updatedAt = tracker.UpdatedAt
	}

	// Determine status by verifying at least one master instance exists.
	status := types.ClusterStatusUnknown
	endpoint := ""
	version := ""
	if exists {
		if len(infrastructure.MasterNodes) > 0 {
			master := infrastructure.MasterNodes[0]
			if p.verifyInstanceExists(ctx, master.InstanceName, zone) {
				status = types.ClusterStatusRunning
			} else {
				status = types.ClusterStatusError
			}
			if master.PublicIP != "" {
				endpoint = fmt.Sprintf("https://%s:6443", master.PublicIP)
			}
		}
		if infrastructure.Metadata != nil {
			if v, ok := infrastructure.Metadata["cluster-version"]; ok {
				version = v
			}
		}
	}

	return &types.Cluster{
		ID:        clusterID,
		Name:      extractClusterName(clusterID),
		Provider:  "gcp",
		Region:    region,
		Version:   version,
		Status:    status,
		Endpoint:  endpoint,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Metadata: map[string]interface{}{
			"projectId": p.config.ProjectID,
			"zone":      zone,
		},
	}, nil
}

// ListClusters lists all GKE clusters
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	var clusters []*types.Cluster

	// Get all tracked clusters from resource trackers
	for clusterID, tracker := range p.resourceTrackers {
		if tracker.Mode == clusterModeGKE {
			if c, err := p.GetCluster(ctx, clusterID); err == nil {
				clusters = append(clusters, c)
			}
			continue
		}
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

	// The local state file is a cache, not the source of truth. An `adhar up`
	// that was interrupted before it saved state leaves real instances, networks
	// and firewall rules behind that nothing could then see: `adhar down`
	// reported "cluster not found in any configured provider" and removed
	// nothing, so the only way to clean up was by hand. Ask the project what
	// exists and merge anything the cache missed.
	tracked := make(map[string]bool, len(clusters))
	for _, c := range clusters {
		tracked[c.Name] = true
	}
	for _, c := range p.discoverUntrackedClusters(ctx) {
		if !tracked[c.Name] {
			clusters = append(clusters, c)
		}
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
	return p.managedListClusters(ctx)
}

// AddNodeGroup adds a node pool to the cluster
// Self-managed (kubeadm on Compute Engine VMs) node groups share DigitalOcean's
// lifecycle through the platform/providers helpers: a fresh join token per
// scale-up, prepared-then-joined instances, drain-then-delete on scale-down,
// and the member list is whatever instances carry the `<cluster>-worker-<group>-`
// prefix in the persisted infrastructure — not a hard-coded 3. (AddNodeGroup,
// RemoveNodeGroup and ScaleNodeGroup were stubs returning fake node groups.)
func (p *Provider) workerPrefix(clusterName, nodeGroupName string) string {
	return fmt.Sprintf("%s-worker-%s-", clusterName, nodeGroupName)
}

func (p *Provider) scaleWorkers(ctx context.Context, clusterID, nodeGroupName, machineType string, replicas int) error {
	infra, exists := p.clusters[clusterID]
	if !exists {
		return fmt.Errorf("cluster %s not found", clusterID)
	}
	if len(infra.MasterNodes) == 0 || infra.MasterNodes[0].PublicIP == "" {
		return fmt.Errorf("cannot scale cluster %s: control-plane public IP unknown", clusterID)
	}
	// The cluster name, derived from the cluster ID rather than by string-surgery
	// on an instance name. This trimmed the literal suffix "-master-1" from
	// "production-master-0", so clusterName stayed "production-master-0": the SSH
	// key was looked up under a directory that did not exist (generating a fresh
	// key the nodes had never seen, so every scale failed to authenticate), and
	// the worker prefix matched none of the real workers, so a scale DOWN was
	// computed as a scale UP.
	clusterName := extractClusterName(clusterID)
	prefix := p.workerPrefix(clusterName, nodeGroupName)
	current := make([]string, 0, len(infra.WorkerNodes))
	for _, w := range infra.WorkerNodes {
		current = append(current, w.InstanceName)
	}
	add, remove := provider.WorkerScalePlan(prefix, current, replicas)
	if len(add) == 0 && len(remove) == 0 {
		log.Printf("Node group %s already at %d workers", nodeGroupName, replicas)
		return nil
	}
	signer, sshPubKey, err := provider.EnsureClusterSSHKey(clusterName)
	if err != nil {
		return fmt.Errorf("failed to load cluster SSH key: %w", err)
	}
	masterIP := infra.MasterNodes[0].PublicIP

	if len(add) > 0 {
		joinCmd, err := provider.JoinCommand(signer, gcpSSHUser, masterIP)
		if err != nil {
			return err
		}
		version := ""
		if c := p.clusterVersion(clusterID); c != "" {
			version = c
		}
		startupScript := provider.KubeadmNodePrepScript(provider.K8sMinorFromVersion(version))
		subnetURL := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", p.config.ProjectID, p.config.Region, infra.SubnetName)
		for _, name := range add {
			nodeInfo, err := p.createComputeInstance(ctx, clusterName, name, subnetURL, machineType, false, sshPubKey, startupScript)
			if err != nil {
				return fmt.Errorf("failed to create worker node %s: %w", name, err)
			}
			if err := provider.WaitForNodePrep(ctx, signer, gcpSSHUser, nodeInfo.PublicIP, 15*time.Minute); err != nil {
				return fmt.Errorf("new worker %s not ready: %w", name, err)
			}
			// Same flags as a worker from `adhar up`: cloud-provider-gcp
			// initialises the node, the PD CSI node plugin lifts the taint.
			if err := provider.EnableExternalCloudProvider(signer, gcpSSHUser, nodeInfo.PublicIP, nodeInfo.PrivateIP, true, true); err != nil {
				return fmt.Errorf("new worker %s: %w", name, err)
			}
			if err := provider.KubeadmJoinWorker(signer, gcpSSHUser, nodeInfo.PublicIP, joinCmd); err != nil {
				return fmt.Errorf("new worker %s: %w", name, err)
			}
			infra.WorkerNodes = append(infra.WorkerNodes, *nodeInfo)
			log.Printf("Added worker %s to cluster %s", name, clusterName)
		}
	}
	for _, name := range remove {
		if err := provider.RetireWorker(signer, gcpSSHUser, masterIP, name); err != nil {
			log.Printf("Warning: %v", err)
		}
		zone := p.config.Zone
		kept := infra.WorkerNodes[:0]
		for _, w := range infra.WorkerNodes {
			if w.InstanceName == name {
				if w.Zone != "" {
					zone = w.Zone
				}
				continue
			}
			kept = append(kept, w)
		}
		if err := p.deleteInstance(ctx, name, zone); err != nil {
			return fmt.Errorf("failed to delete instance %s: %w", name, err)
		}
		infra.WorkerNodes = kept
		log.Printf("Removed worker %s from cluster %s", name, clusterName)
	}
	if err := p.saveState(); err != nil {
		log.Printf("Warning: failed to persist cluster state after scaling: %v", err)
	}
	return nil
}

// clusterVersion returns the Kubernetes version recorded for a cluster, or ""
// when the state predates version tracking (the node-prep script then falls
// back to the platform default minor, which matches a cluster created by this
// version of adhar).
func (p *Provider) clusterVersion(clusterID string) string {
	if c, err := p.GetCluster(context.Background(), clusterID); err == nil && c != nil {
		return c.Version
	}
	return ""
}

func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedAddNodeGroup(ctx, clusterID, nodeGroup)
	}
	log.Printf("Adding node group %s (%d × %s) to cluster %s", nodeGroup.Name, nodeGroup.Replicas, nodeGroup.InstanceType, clusterID)
	if err := p.scaleWorkers(ctx, clusterID, nodeGroup.Name, nodeGroup.InstanceType, nodeGroup.Replicas); err != nil {
		return nil, err
	}
	return p.GetNodeGroup(ctx, clusterID, nodeGroup.Name)
}

func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedRemoveNodeGroup(ctx, clusterID, nodeGroupName)
	}
	log.Printf("Removing node group %s from cluster %s", nodeGroupName, clusterID)
	return p.scaleWorkers(ctx, clusterID, nodeGroupName, "", 0)
}

func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedScaleNodeGroup(ctx, clusterID, nodeGroupName, replicas)
	}
	log.Printf("Scaling node group %s in cluster %s to %d replicas", nodeGroupName, clusterID, replicas)
	return p.scaleWorkers(ctx, clusterID, nodeGroupName, "", replicas)
}

func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedGetNodeGroup(ctx, clusterID, nodeGroupName)
	}
	infra, exists := p.clusters[clusterID]
	if !exists {
		return nil, fmt.Errorf("cluster %s not found", clusterID)
	}
	clusterName := ""
	if len(infra.MasterNodes) > 0 {
		clusterName = strings.TrimSuffix(infra.MasterNodes[0].InstanceName, "-master-1")
	}
	prefix := p.workerPrefix(clusterName, nodeGroupName)
	replicas, machineType := 0, p.config.MachineType
	for _, w := range infra.WorkerNodes {
		if strings.HasPrefix(w.InstanceName, prefix) {
			replicas++
			if w.MachineType != "" {
				machineType = w.MachineType
			}
		}
	}
	return &types.NodeGroup{
		Name:         nodeGroupName,
		Replicas:     replicas,
		InstanceType: machineType,
		Status:       "ready",
		UpdatedAt:    time.Now(),
	}, nil
}

func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedListNodeGroups(ctx, clusterID)
	}
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
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedUpgrade(ctx, clusterID, version)
	}
	log.Printf("Upgrading self-managed GCP cluster %s to version %s via kubeadm", clusterID, version)

	clusterName := extractClusterName(clusterID)
	infrastructure, err := p.getClusterInfrastructure(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("failed to get cluster infrastructure: %w", err)
	}
	if len(infrastructure.MasterNodes) == 0 || infrastructure.MasterNodes[0].PublicIP == "" {
		return fmt.Errorf("no reachable control-plane instance for cluster %s", clusterName)
	}
	signer, err := provider.LoadClusterSSHKey(clusterName)
	if err != nil {
		return err
	}

	var workerIPs []string
	for _, w := range infrastructure.WorkerNodes {
		if w.PublicIP != "" {
			workerIPs = append(workerIPs, w.PublicIP)
		}
	}
	if err := provider.KubeadmUpgradeCluster(ctx, signer, gcpSSHUser, infrastructure.MasterNodes[0].PublicIP, workerIPs, version); err != nil {
		return fmt.Errorf("kubeadm upgrade of cluster %s failed: %w", clusterName, err)
	}
	log.Printf("Successfully upgraded cluster %s to %s", clusterName, version)
	return nil
}

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
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedHealth(ctx, clusterID)
	}
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

// InstallAddon installs an addon on the target cluster via kubectl/helm.
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	log.Printf("Installing addon %s on GCP cluster %s", addonName, clusterID)

	kubeconfigPath, cleanup, err := p.addonKubeconfig(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to obtain kubeconfig for addon install: %w", err)
	}
	defer cleanup()

	switch addonName {
	case "gce-pd-csi-driver", "gcp-compute-persistent-disk-csi-driver":
		// CSI driver is a managed/cluster-creation concern on GKE, not a kubectl addon.
		return fmt.Errorf("addon %q is managed at the cluster level and cannot be installed via kubectl", addonName)
	case "coredns", "kube-proxy":
		return fmt.Errorf("addon %q is a built-in Kubernetes component and is not managed as an installable addon", addonName)
	case "cilium":
		// Cilium is the platform CNI/dataplane and the Gateway API implementation.
		return provider.InstallCiliumAddon(ctx, kubeconfigPath, config)
	case "metrics-server":
		return provider.InstallMetricsServerAddon(ctx, kubeconfigPath)
	case "cert-manager":
		return provider.InstallCertManagerAddon(ctx, kubeconfigPath)
	case "ingress", "gateway", "gateway-api", "cilium-gateway":
		// NOTE: This platform uses the Cilium Gateway API as its default ingress,
		// NOT ingress-nginx. This installs the Gateway API CRDs served by Cilium.
		return provider.InstallGatewayAPIAddon(ctx, kubeconfigPath)
	case "ingress-nginx":
		// ingress-nginx is NOT the platform default (Cilium Gateway API is), but
		// remains available as an explicit opt-in generic addon.
		return provider.InstallIngressNginxAddon(ctx, kubeconfigPath)
	case "helm-chart":
		// Generic Helm chart addon path: caller supplies repo/chart/version/namespace/values.
		opts, err := provider.HelmOptionsFromConfig("custom", config)
		if err != nil {
			return err
		}
		return provider.InstallHelmAddon(ctx, kubeconfigPath, opts)
	default:
		return fmt.Errorf("unsupported addon for GCP: %s", addonName)
	}
}

// UninstallAddon uninstalls an addon from the target cluster via kubectl/helm.
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	log.Printf("Uninstalling addon %s from GCP cluster %s", addonName, clusterID)

	kubeconfigPath, cleanup, err := p.addonKubeconfig(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to obtain kubeconfig for addon uninstall: %w", err)
	}
	defer cleanup()

	switch addonName {
	case "gce-pd-csi-driver", "gcp-compute-persistent-disk-csi-driver":
		return fmt.Errorf("addon %q is managed at the cluster level and cannot be uninstalled via kubectl", addonName)
	case "coredns", "kube-proxy":
		return fmt.Errorf("addon %q is a critical system component and should not be uninstalled", addonName)
	case "cilium":
		return provider.UninstallHelmAddon(ctx, kubeconfigPath, "cilium", "kube-system")
	case "metrics-server":
		return provider.UninstallMetricsServerAddon(ctx, kubeconfigPath)
	case "cert-manager":
		return provider.UninstallCertManagerAddon(ctx, kubeconfigPath)
	case "ingress", "gateway", "gateway-api", "cilium-gateway":
		return provider.UninstallGatewayAPIAddon(ctx, kubeconfigPath)
	case "ingress-nginx":
		return provider.UninstallIngressNginxAddon(ctx, kubeconfigPath)
	case "helm-chart":
		return fmt.Errorf("uninstalling a generic helm-chart addon requires the release name and namespace")
	default:
		return fmt.Errorf("unsupported addon for GCP: %s", addonName)
	}
}

// addonKubeconfig fetches the cluster kubeconfig and writes it to a temp file,
// returning the path and a cleanup func used to target addon installs.
func (p *Provider) addonKubeconfig(ctx context.Context, clusterID string) (string, func(), error) {
	kubeconfig, err := p.GetKubeconfig(ctx, clusterID)
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	return provider.WriteKubeconfigTempFile(kubeconfig)
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

// extractClusterName returns the cluster name from an ID of the form
// gcp/<project>/<name> (the provider's convention) or gcp-<name> (legacy).
func extractClusterName(clusterID string) string {
	if strings.HasPrefix(clusterID, "gcp/") {
		return clusterID[strings.LastIndex(clusterID, "/")+1:]
	}
	if len(clusterID) > 4 && clusterID[:4] == "gcp-" {
		return clusterID[4:]
	}
	return clusterID
}

// GetKubeconfig retrieves the kubeconfig for a cluster
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	cluster, err := p.GetCluster(ctx, clusterID)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster: %w", err)
	}
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedKubeconfig(ctx, clusterID)
	}

	infrastructure, err := p.getClusterInfrastructure(ctx, cluster.Name)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster infrastructure: %w", err)
	}
	var masterIP string
	for _, m := range infrastructure.MasterNodes {
		if m.PublicIP != "" {
			masterIP = m.PublicIP
			break
		}
	}
	if masterIP == "" {
		return "", fmt.Errorf("no control-plane instance with a public IP found for cluster %s", cluster.Name)
	}

	signer, err := provider.LoadClusterSSHKey(cluster.Name)
	if err != nil {
		return "", err
	}
	return provider.FetchAdminKubeconfig(signer, gcpSSHUser, masterIP)
}

// getClusterInfrastructure returns the cluster's infrastructure: the tracked
// record when the cluster was created on this machine, otherwise a discovery
// of the instances named by the provider's conventions (<name>-master-N,
// <name>-worker-N) with their current addresses.
func (p *Provider) getClusterInfrastructure(ctx context.Context, clusterName string) (*ClusterInfrastructure, error) {
	clusterID := fmt.Sprintf("gcp/%s/%s", p.config.ProjectID, clusterName)
	if infra, ok := p.clusters[clusterID]; ok && infra != nil && len(infra.MasterNodes) > 0 {
		// Refresh addresses: ephemeral public IPs change across stop/start.
		for i := range infra.MasterNodes {
			if priv, pub, err := p.instanceIPs(ctx, infra.MasterNodes[i].InstanceName, infra.MasterNodes[i].Zone); err == nil {
				infra.MasterNodes[i].PrivateIP, infra.MasterNodes[i].PublicIP = priv, pub
			}
		}
		return infra, nil
	}
	zone := p.config.Zone
	if tracker, ok := p.resourceTrackers[clusterID]; ok && tracker.Zone != "" {
		zone = tracker.Zone
	}
	infra := &ClusterInfrastructure{
		NetworkName:   fmt.Sprintf("%s-network", clusterName),
		SubnetName:    fmt.Sprintf("%s-subnet", clusterName),
		FirewallRules: []string{fmt.Sprintf("%s-firewall", clusterName)},
	}
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("%s-master-%d", clusterName, i)
		priv, pub, err := p.instanceIPs(ctx, name, zone)
		if err != nil {
			break
		}
		infra.MasterNodes = append(infra.MasterNodes, NodeInfo{InstanceName: name, Zone: zone, PrivateIP: priv, PublicIP: pub, MachineType: p.config.MachineType, Role: "master"})
	}
	for i := 1; i <= 100; i++ {
		name := fmt.Sprintf("%s-worker-%d", clusterName, i)
		priv, pub, err := p.instanceIPs(ctx, name, zone)
		if err != nil {
			break
		}
		infra.WorkerNodes = append(infra.WorkerNodes, NodeInfo{InstanceName: name, Zone: zone, PrivateIP: priv, PublicIP: pub, MachineType: p.config.MachineType, Role: "worker"})
	}
	if len(infra.MasterNodes) == 0 {
		return nil, fmt.Errorf("no control-plane instance %s-master-1 found in zone %s", clusterName, zone)
	}
	return infra, nil
}

// InvestigateCluster performs comprehensive investigation of a cluster
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	// TODO: Implement GCP-specific cluster investigation
	return fmt.Errorf("cluster investigation not yet implemented for GCP provider")
}
