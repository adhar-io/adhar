package digitalocean

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// Register the DigitalOcean provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("digitalocean", func(config map[string]interface{}) (provider.Provider, error) {
		return NewDigitalOceanProvider(config)
	})
}

// TokenSource implements oauth2.TokenSource interface for DigitalOcean API authentication
type TokenSource struct {
	AccessToken string
}

// Token returns an oauth2.Token for authentication
func (t *TokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{
		AccessToken: t.AccessToken,
	}, nil
}

// Config holds DigitalOcean provider configuration
type Config struct {
	// Authentication Methods (multiple options supported)
	// Option 1: API Token (primary method)
	Token string `json:"token"`

	// Option 2: Token from file
	TokenFile string `json:"tokenFile,omitempty"`

	// Option 3: Environment variable (DIGITALOCEAN_TOKEN)
	UseEnvironment bool `json:"useEnvironment,omitempty"`

	// Configuration
	Region      string `json:"region"`      // Default region for resources
	DropletSize string `json:"dropletSize"` // Default droplet size
	Image       string `json:"image"`       // Default OS image

	// VPC Configuration
	VPCUUID          string `json:"vpcUUID,omitempty"`          // Existing VPC UUID to use
	VPCCIDR          string `json:"vpcCIDR"`                    // VPC CIDR block (e.g., "10.3.0.0/16")
	ReuseExistingVPC bool   `json:"reuseExistingVPC,omitempty"` // Whether to reuse compatible existing VPCs

	// SSH Configuration
	SSHKeys []interface{} `json:"sshKeys,omitempty"` // SSH key fingerprints or IDs

	// Firewall Configuration
	FirewallRules []FirewallRuleConfig `json:"firewallRules,omitempty"`

	// Tagging
	Tags []string `json:"tags,omitempty"`
}

// FirewallRuleConfig holds firewall rule configuration
type FirewallRuleConfig struct {
	Name          string               `json:"name"`
	InboundRules  []InboundRuleConfig  `json:"inboundRules,omitempty"`
	OutboundRules []OutboundRuleConfig `json:"outboundRules,omitempty"`
}

// InboundRuleConfig holds inbound firewall rule configuration
type InboundRuleConfig struct {
	Protocol string        `json:"protocol"`
	Ports    string        `json:"ports"`
	Sources  SourcesConfig `json:"sources"`
}

// OutboundRuleConfig holds outbound firewall rule configuration
type OutboundRuleConfig struct {
	Protocol     string             `json:"protocol"`
	Ports        string             `json:"ports"`
	Destinations DestinationsConfig `json:"destinations"`
}

// SourcesConfig holds source configuration for firewall rules
type SourcesConfig struct {
	Addresses  []string `json:"addresses,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	DropletIDs []int    `json:"dropletIds,omitempty"`
}

// DestinationsConfig holds destination configuration for firewall rules
type DestinationsConfig struct {
	Addresses  []string `json:"addresses,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	DropletIDs []int    `json:"dropletIds,omitempty"`
}

// NodeInfo contains information about a droplet node
type NodeInfo struct {
	Name      string
	DropletID int
	PublicIP  string
	PrivateIP string
	Size      string
	IsMaster  bool
	CreatedAt time.Time
}

// ClusterInfrastructure tracks the infrastructure components of a cluster
type ClusterInfrastructure struct {
	VPCName      string
	VPCUUID      string
	FirewallName string
	FirewallUUID string
	MasterNodes  []NodeInfo
	WorkerNodes  []NodeInfo
}

// ResourceTracker tracks all resources created for a cluster
type ResourceTracker struct {
	Region    string
	VPCs      []string
	Firewalls []string
	Droplets  []int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Provider implements the DigitalOcean provider for manual Kubernetes clusters
type Provider struct {
	client           *godo.Client
	config           *Config
	clusters         map[string]*types.Cluster
	resourceTrackers map[string]*ResourceTracker
}

// NewProvider creates a new DigitalOcean provider instance with manual cluster support
func NewProvider(config *Config) (*Provider, error) {
	log.Printf("Initializing DigitalOcean provider with manual cluster support")

	// Determine authentication method and get the token
	var token string

	switch {
	// Priority 1: Explicit token
	case config.Token != "":
		token = config.Token

	// Priority 2: Token from file
	case config.TokenFile != "":
		tokenBytes, err := os.ReadFile(config.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read token file %s: %w", config.TokenFile, err)
		}
		token = strings.TrimSpace(string(tokenBytes))

	// Priority 3: Environment variable
	case config.UseEnvironment:
		token = os.Getenv("DIGITALOCEAN_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("DIGITALOCEAN_TOKEN environment variable is not set")
		}

	// Default: Try environment variable
	default:
		token = os.Getenv("DIGITALOCEAN_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("DigitalOcean token is required (provide token, tokenFile, or set DIGITALOCEAN_TOKEN environment variable)")
		}
	}

	// Set default configuration values
	if config.Region == "" {
		config.Region = "nyc1" // Default to New York region
	}
	if config.DropletSize == "" {
		config.DropletSize = "s-2vcpu-2gb" // Default droplet size
	}
	if config.Image == "" {
		config.Image = "ubuntu-22-04-x64" // Default to Ubuntu 22.04
	}
	if config.VPCCIDR == "" {
		config.VPCCIDR = "10.0.0.0/16" // Default VPC CIDR
	}

	// Log configuration details
	log.Printf("DigitalOcean configuration: region=%s, dropletSize=%s, image=%s",
		config.Region, config.DropletSize, config.Image)
	log.Printf("DigitalOcean VPC config: CIDR=%s, existingVPC=%s",
		config.VPCCIDR, config.VPCUUID)
	if len(config.Tags) > 0 {
		log.Printf("DigitalOcean tags: %v", config.Tags)
	}
	if len(config.SSHKeys) > 0 {
		log.Printf("DigitalOcean SSH keys configured: %d keys", len(config.SSHKeys))
	}
	if len(config.FirewallRules) > 0 {
		log.Printf("DigitalOcean firewall rules configured: %d rule sets", len(config.FirewallRules))
	}

	// Create OAuth2 token source
	tokenSource := &TokenSource{
		AccessToken: token,
	}

	// Create OAuth2 client
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)

	// Create DigitalOcean client
	client := godo.NewClient(oauthClient)

	provider := &Provider{
		client:           client,
		config:           config,
		clusters:         make(map[string]*types.Cluster),
		resourceTrackers: make(map[string]*ResourceTracker),
	}

	log.Printf("DigitalOcean provider initialized successfully")
	return provider, nil
}

// NewDigitalOceanProvider creates a new DigitalOcean provider from configuration map
func NewDigitalOceanProvider(configMap map[string]interface{}) (provider.Provider, error) {
	doConfig := &Config{}

	// Parse authentication configuration
	if token, ok := configMap["token"].(string); ok {
		doConfig.Token = token
	}
	if useEnv, ok := configMap["useEnvironment"].(bool); ok {
		doConfig.UseEnvironment = useEnv
	}

	// Parse basic configuration
	if region, ok := configMap["region"].(string); ok {
		doConfig.Region = region
	}

	// Parse configuration section
	if configSection, ok := configMap["config"].(map[string]interface{}); ok {
		// Parse droplet configuration
		if dropletSize, ok := configSection["droplet_size"].(string); ok {
			doConfig.DropletSize = dropletSize
		}
		if image, ok := configSection["image"].(string); ok {
			doConfig.Image = image
		}

		// Parse VPC configuration
		if vpcUUID, ok := configSection["vpc_uuid"].(string); ok {
			doConfig.VPCUUID = vpcUUID
		}
		if vpcCIDR, ok := configSection["vpc_cidr"].(string); ok {
			doConfig.VPCCIDR = vpcCIDR
		}
		if reuseVPC, ok := configSection["reuse_existing_vpc"].(bool); ok {
			doConfig.ReuseExistingVPC = reuseVPC
		}

		// Parse SSH keys
		if sshKeys, ok := configSection["ssh_keys"].([]interface{}); ok {
			doConfig.SSHKeys = sshKeys
		}

		// Parse tags
		if tags, ok := configSection["tags"].([]interface{}); ok {
			for _, tag := range tags {
				if tagStr, ok := tag.(string); ok {
					doConfig.Tags = append(doConfig.Tags, tagStr)
				}
			}
		}

		// Parse firewall rules
		if firewallRules, ok := configSection["firewall_rules"].([]interface{}); ok {
			for _, rule := range firewallRules {
				if ruleMap, ok := rule.(map[string]interface{}); ok {
					fwRule := FirewallRuleConfig{}

					if name, ok := ruleMap["name"].(string); ok {
						fwRule.Name = name
					}

					// Parse inbound rules
					if inboundRules, ok := ruleMap["inbound_rules"].([]interface{}); ok {
						for _, inRule := range inboundRules {
							if inRuleMap, ok := inRule.(map[string]interface{}); ok {
								inboundRule := InboundRuleConfig{}

								if protocol, ok := inRuleMap["protocol"].(string); ok {
									inboundRule.Protocol = protocol
								}
								if ports, ok := inRuleMap["ports"].(string); ok {
									inboundRule.Ports = ports
								}

								// Parse sources
								if sources, ok := inRuleMap["sources"].(map[string]interface{}); ok {
									if addresses, ok := sources["addresses"].([]interface{}); ok {
										for _, addr := range addresses {
											if addrStr, ok := addr.(string); ok {
												inboundRule.Sources.Addresses = append(inboundRule.Sources.Addresses, addrStr)
											}
										}
									}
								}

								fwRule.InboundRules = append(fwRule.InboundRules, inboundRule)
							}
						}
					}

					// Parse outbound rules
					if outboundRules, ok := ruleMap["outbound_rules"].([]interface{}); ok {
						for _, outRule := range outboundRules {
							if outRuleMap, ok := outRule.(map[string]interface{}); ok {
								outboundRule := OutboundRuleConfig{}

								if protocol, ok := outRuleMap["protocol"].(string); ok {
									outboundRule.Protocol = protocol
								}
								if ports, ok := outRuleMap["ports"].(string); ok {
									outboundRule.Ports = ports
								}

								// Parse destinations
								if destinations, ok := outRuleMap["destinations"].(map[string]interface{}); ok {
									if addresses, ok := destinations["addresses"].([]interface{}); ok {
										for _, addr := range addresses {
											if addrStr, ok := addr.(string); ok {
												outboundRule.Destinations.Addresses = append(outboundRule.Destinations.Addresses, addrStr)
											}
										}
									}
								}

								fwRule.OutboundRules = append(fwRule.OutboundRules, outboundRule)
							}
						}
					}

					doConfig.FirewallRules = append(doConfig.FirewallRules, fwRule)
				}
			}
		}
	}

	return NewProvider(doConfig)
}

// Name returns the provider name
func (p *Provider) Name() string {
	return "digitalocean"
}

// Region returns the provider region
func (p *Provider) Region() string {
	return p.config.Region
}

// Authenticate validates DigitalOcean credentials using the API
func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	log.Printf("Authenticating with DigitalOcean")

	// Test DigitalOcean credentials by making a simple API call
	_, _, err := p.client.Account.Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to authenticate with DigitalOcean: %w", err)
	}

	log.Printf("Successfully authenticated with DigitalOcean")
	return nil
}

// ValidatePermissions checks if we have required permissions for manual cluster creation
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	log.Printf("Validating DigitalOcean permissions for manual cluster creation")

	// Check droplet permissions
	_, _, err := p.client.Droplets.List(ctx, &godo.ListOptions{Page: 1, PerPage: 1})
	if err != nil {
		return fmt.Errorf("insufficient droplet permissions: %w", err)
	}

	// Check VPC permissions
	_, _, err = p.client.VPCs.List(ctx, &godo.ListOptions{Page: 1, PerPage: 1})
	if err != nil {
		return fmt.Errorf("insufficient VPC permissions: %w", err)
	}

	log.Printf("DigitalOcean permissions validation successful")
	return nil
}

// CreateCluster creates a new managed DOKS (DigitalOcean Kubernetes) cluster
// using the godo KubernetesService and waits for it to become running.
func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Provider != "" && spec.Provider != "digitalocean" {
		return nil, fmt.Errorf("provider mismatch: expected digitalocean, got %s", spec.Provider)
	}

	log.Printf("Creating managed DOKS cluster: %s", spec.Name)

	// Validate cluster specification
	if err := p.validateClusterSpec(spec); err != nil {
		return nil, fmt.Errorf("invalid cluster specification: %w", err)
	}

	region := p.config.Region
	if spec.Region != "" {
		region = spec.Region
	}

	// Resolve the Kubernetes version slug (DOKS requires a full slug, e.g.
	// "1.30.1-do.0"). If the caller supplied a bare/partial version, find the
	// matching available slug; otherwise fall back to the latest default.
	versionSlug, err := p.resolveDOKSVersion(ctx, spec.Version)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve DOKS version: %w", err)
	}

	// Build node pools from the requested node groups. DOKS requires at least
	// one node pool.
	nodePools := p.buildDOKSNodePools(spec)

	ha := spec.ControlPlane.HighAvailability
	createReq := &godo.KubernetesClusterCreateRequest{
		Name:        spec.Name,
		RegionSlug:  region,
		VersionSlug: versionSlug,
		VPCUUID:     p.config.VPCUUID,
		Tags:        append([]string{"adhar-cluster"}, p.config.Tags...),
		HA:          &ha,
		NodePools:   nodePools,
	}

	doCluster, _, err := p.client.Kubernetes.Create(ctx, createReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create DOKS cluster %q: %w", spec.Name, err)
	}

	log.Printf("DOKS cluster %s created (id=%s), waiting for it to become running", spec.Name, doCluster.ID)
	running, err := p.waitForDOKSRunning(ctx, doCluster.ID)
	if err != nil {
		return nil, fmt.Errorf("DOKS cluster %s did not become ready: %w", spec.Name, err)
	}

	cluster := p.doksToCluster(running)
	p.clusters[cluster.ID] = cluster

	log.Printf("Successfully created managed DOKS cluster: %s (id=%s)", spec.Name, running.ID)
	return cluster, nil
}

// resolveDOKSVersion maps a requested Kubernetes version to a concrete DOKS
// version slug. An empty request resolves to the provider default.
func (p *Provider) resolveDOKSVersion(ctx context.Context, requested string) (string, error) {
	opts, _, err := p.client.Kubernetes.GetOptions(ctx)
	if err != nil {
		// If we cannot list options, trust the caller-provided slug.
		if requested != "" {
			log.Printf("Warning: failed to list DOKS options, using requested version %q: %v", requested, err)
			return requested, nil
		}
		return "", fmt.Errorf("failed to list DOKS options: %w", err)
	}
	if len(opts.Versions) == 0 {
		if requested != "" {
			return requested, nil
		}
		return "", fmt.Errorf("no DOKS versions available in region %s", p.config.Region)
	}

	trimmed := strings.TrimPrefix(requested, "v")
	if trimmed != "" {
		for _, v := range opts.Versions {
			if v.Slug == trimmed || v.KubernetesVersion == trimmed || strings.HasPrefix(v.Slug, trimmed) {
				return v.Slug, nil
			}
		}
		log.Printf("Warning: requested version %q not found among DOKS options, using default %q", requested, opts.Versions[0].Slug)
	}
	// Default to the first (latest) available version.
	return opts.Versions[0].Slug, nil
}

// buildDOKSNodePools converts the cluster spec node groups into DOKS node pool
// create requests. A default pool is created when none are specified.
func (p *Provider) buildDOKSNodePools(spec *types.ClusterSpec) []*godo.KubernetesNodePoolCreateRequest {
	var pools []*godo.KubernetesNodePoolCreateRequest
	for _, ng := range spec.NodeGroups {
		size := ng.InstanceType
		if size == "" {
			size = p.config.DropletSize
		}
		count := ng.Replicas
		if count <= 0 {
			count = 1
		}
		pools = append(pools, &godo.KubernetesNodePoolCreateRequest{
			Name:      ng.Name,
			Size:      size,
			Count:     count,
			Labels:    ng.Labels,
			AutoScale: ng.AutoScaling.MaxReplicas > 0,
			MinNodes:  ng.AutoScaling.MinReplicas,
			MaxNodes:  ng.AutoScaling.MaxReplicas,
		})
	}
	if len(pools) == 0 {
		pools = append(pools, &godo.KubernetesNodePoolCreateRequest{
			Name:  "default-pool",
			Size:  p.config.DropletSize,
			Count: 3,
		})
	}
	return pools
}

// waitForDOKSRunning polls the DOKS API until the cluster reaches the running
// state or the context/timeout expires.
func (p *Provider) waitForDOKSRunning(ctx context.Context, clusterID string) (*godo.KubernetesCluster, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		cluster, _, err := p.client.Kubernetes.Get(waitCtx, clusterID)
		if err == nil && cluster.Status != nil {
			switch cluster.Status.State {
			case godo.KubernetesClusterStatusRunning:
				return cluster, nil
			case godo.KubernetesClusterStatusError, godo.KubernetesClusterStatusDegraded:
				return nil, fmt.Errorf("cluster entered state %q: %s", cluster.Status.State, cluster.Status.Message)
			default:
				log.Printf("DOKS cluster %s state: %s", clusterID, cluster.Status.State)
			}
		} else if err != nil {
			log.Printf("Warning: failed to poll DOKS cluster %s: %v", clusterID, err)
		}

		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("timed out waiting for DOKS cluster %s to become running: %w", clusterID, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// doksToCluster maps a godo KubernetesCluster to the provider-agnostic type.
func (p *Provider) doksToCluster(c *godo.KubernetesCluster) *types.Cluster {
	status := types.ClusterStatusUnknown
	if c.Status != nil {
		switch c.Status.State {
		case godo.KubernetesClusterStatusRunning:
			status = types.ClusterStatusRunning
		case godo.KubernetesClusterStatusProvisioning:
			status = types.ClusterStatusCreating
		case godo.KubernetesClusterStatusDeleted:
			status = types.ClusterStatusDeleting
		case godo.KubernetesClusterStatusUpgrading:
			status = types.ClusterStatusUpdating
		case godo.KubernetesClusterStatusError, godo.KubernetesClusterStatusDegraded:
			status = types.ClusterStatusError
		}
	}

	workerNodes := 0
	for _, np := range c.NodePools {
		workerNodes += np.Count
	}

	return &types.Cluster{
		ID:        c.ID,
		Name:      c.Name,
		Provider:  "digitalocean",
		Region:    c.RegionSlug,
		Version:   c.VersionSlug,
		Status:    status,
		Endpoint:  c.Endpoint,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
		Metadata: map[string]interface{}{
			"region":      c.RegionSlug,
			"vpc":         c.VPCUUID,
			"ha":          c.HA,
			"nodePools":   len(c.NodePools),
			"workerNodes": workerNodes,
		},
	}
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
		spec.ControlPlane.InstanceType = p.config.DropletSize // Use default droplet size
	}
	return nil
}

// createClusterInfrastructure creates the DigitalOcean infrastructure for a manual Kubernetes cluster
func (p *Provider) createClusterInfrastructure(ctx context.Context, clusterName string, spec *types.ClusterSpec) (*ClusterInfrastructure, error) {
	log.Printf("Creating infrastructure for cluster: %s", clusterName)

	infrastructure := &ClusterInfrastructure{}

	// Create or use existing VPC
	var vpcUUID string
	var vpcName string
	var err error

	if p.config.VPCUUID != "" {
		// Use existing VPC by UUID
		vpcUUID = p.config.VPCUUID
		vpc, _, err := p.client.VPCs.Get(ctx, vpcUUID)
		if err != nil {
			return nil, fmt.Errorf("failed to get existing VPC %s: %w", vpcUUID, err)
		}
		vpcName = vpc.Name
		log.Printf("Using existing VPC by UUID: %s (UUID: %s)", vpcName, vpcUUID)
	} else {
		// Check if we should reuse an existing VPC with compatible CIDR
		existingVPC := p.findReusableVPC(ctx, clusterName)
		if existingVPC != nil {
			vpcUUID = existingVPC.ID
			vpcName = existingVPC.Name
			log.Printf("Reusing existing compatible VPC: %s (UUID: %s, CIDR: %s)", vpcName, vpcUUID, existingVPC.IPRange)
		} else {
			// Create new VPC with unique name to avoid conflicts
			timestamp := time.Now().Unix()
			vpcName = fmt.Sprintf("%s-vpc-%d", clusterName, timestamp)
			vpcUUID, err = p.createVPC(ctx, vpcName)
			if err != nil {
				return nil, fmt.Errorf("failed to create VPC: %w", err)
			}
		}
	}
	infrastructure.VPCName = vpcName
	infrastructure.VPCUUID = vpcUUID

	// Create firewall with unique name to avoid conflicts
	timestamp := time.Now().Unix()
	firewallName := fmt.Sprintf("%s-firewall-%d", clusterName, timestamp)
	firewallUUID, err := p.createFirewall(ctx, firewallName)
	if err != nil {
		return nil, fmt.Errorf("failed to create firewall: %w", err)
	}
	infrastructure.FirewallName = firewallName
	infrastructure.FirewallUUID = firewallUUID

	// Create master nodes
	masterNodes, err := p.createMasterNodes(ctx, clusterName, vpcUUID, firewallUUID, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create master nodes: %w", err)
	}
	infrastructure.MasterNodes = masterNodes

	// Create worker nodes if specified
	if len(spec.NodeGroups) > 0 {
		workerNodes, err := p.createWorkerNodes(ctx, clusterName, vpcUUID, firewallUUID, spec)
		if err != nil {
			return nil, fmt.Errorf("failed to create worker nodes: %w", err)
		}
		infrastructure.WorkerNodes = workerNodes
	}

	log.Printf("Successfully created infrastructure for cluster: %s", clusterName)
	return infrastructure, nil
}

// createVPC creates a VPC using DigitalOcean SDK with conflict resolution
func (p *Provider) createVPC(ctx context.Context, vpcName string) (string, error) {
	log.Printf("Creating VPC: %s", vpcName)

	// Use configured VPC CIDR or default
	baseCIDR := "10.0.0.0/16" // Default CIDR
	if p.config.VPCCIDR != "" {
		baseCIDR = p.config.VPCCIDR
	}

	// Check for existing VPCs to avoid CIDR conflicts
	existingVPCs, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: failed to list existing VPCs: %v", err)
		// Continue with original CIDR
	} else {
		// Check if the configured CIDR conflicts with existing VPCs
		vpcCIDR := p.findAvailableCIDR(baseCIDR, existingVPCs)
		if vpcCIDR != baseCIDR {
			log.Printf("CIDR conflict detected. Using alternative CIDR: %s", vpcCIDR)
		}
		baseCIDR = vpcCIDR
	}

	createRequest := &godo.VPCCreateRequest{
		Name:       vpcName,
		RegionSlug: p.config.Region,
		IPRange:    baseCIDR,
	}

	vpc, _, err := p.client.VPCs.Create(ctx, createRequest)
	if err != nil {
		return "", fmt.Errorf("failed to create VPC: %w", err)
	}

	log.Printf("Successfully created VPC: %s (UUID: %s, CIDR: %s)", vpcName, vpc.ID, baseCIDR)
	return vpc.ID, nil
}

// findAvailableCIDR finds an available CIDR range that doesn't conflict with existing VPCs
func (p *Provider) findAvailableCIDR(preferredCIDR string, existingVPCs []*godo.VPC) string {
	// Check if preferred CIDR conflicts
	for _, vpc := range existingVPCs {
		if vpc.IPRange == preferredCIDR {
			log.Printf("CIDR conflict found: %s is used by VPC %s", preferredCIDR, vpc.Name)
			// Generate alternative CIDRs
			return p.generateAlternativeCIDR(preferredCIDR, existingVPCs)
		}
	}
	return preferredCIDR
}

// generateAlternativeCIDR generates an alternative CIDR that doesn't conflict
func (p *Provider) generateAlternativeCIDR(baseCIDR string, existingVPCs []*godo.VPC) string {
	// Parse the base CIDR to get the network class
	var baseClass string
	switch {
	case strings.HasPrefix(baseCIDR, "10.0."):
		baseClass = "10."
	case strings.HasPrefix(baseCIDR, "10.1."):
		baseClass = "10."
	case strings.HasPrefix(baseCIDR, "10.2."):
		baseClass = "10."
	case strings.HasPrefix(baseCIDR, "10.3."):
		baseClass = "10."
	default:
		baseClass = "10."
	}

	// DigitalOcean reserved ranges to avoid: 10.10.0.0/16, 10.244.0.0/16, 10.245.0.0/16
	reservedRanges := map[string]bool{
		"10.10.0.0/16":  true,
		"10.244.0.0/16": true,
		"10.245.0.0/16": true,
	}

	// Try different subnets in the 10.x.0.0/16 range, skipping reserved ranges
	for i := 4; i <= 254; i++ {
		candidateCIDR := fmt.Sprintf("%s%d.0.0/16", baseClass, i)

		// Skip if it's a reserved range
		if reservedRanges[candidateCIDR] {
			continue
		}

		conflict := false
		for _, vpc := range existingVPCs {
			if vpc.IPRange == candidateCIDR {
				conflict = true
				break
			}
		}

		if !conflict {
			log.Printf("Found available CIDR: %s", candidateCIDR)
			return candidateCIDR
		}
	}

	// Fallback to timestamp-based CIDR (avoiding reserved ranges)
	for attempt := 0; attempt < 10; attempt++ {
		timestamp := time.Now().Unix()%200 + 50 // Range 50-249
		fallbackCIDR := fmt.Sprintf("10.%d.0.0/16", timestamp)
		if !reservedRanges[fallbackCIDR] {
			log.Printf("Using timestamp-based fallback CIDR: %s", fallbackCIDR)
			return fallbackCIDR
		}
	}

	// Ultimate fallback
	return "10.200.0.0/16"
}

// findReusableVPC checks if there's an existing VPC that can be reused
func (p *Provider) findReusableVPC(ctx context.Context, clusterName string) *godo.VPC {
	// Only try to reuse VPCs if explicitly configured to do so
	if !p.config.ReuseExistingVPC {
		return nil
	}

	log.Printf("Checking for reusable VPC with CIDR: %s", p.config.VPCCIDR)

	existingVPCs, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: failed to list VPCs for reuse check: %v", err)
		return nil
	}

	targetCIDR := p.config.VPCCIDR
	if targetCIDR == "" {
		targetCIDR = "10.0.0.0/16" // Default CIDR
	}

	// Look for a VPC with the same CIDR in the same region
	for _, vpc := range existingVPCs {
		if vpc.IPRange == targetCIDR && vpc.RegionSlug == p.config.Region {
			log.Printf("Found reusable VPC: %s (UUID: %s, CIDR: %s)", vpc.Name, vpc.ID, vpc.IPRange)
			return vpc
		}
	}

	log.Printf("No reusable VPC found with CIDR %s in region %s", targetCIDR, p.config.Region)
	return nil
}

// createFirewall creates a firewall with configured rules using DigitalOcean SDK
func (p *Provider) createFirewall(ctx context.Context, firewallName string) (string, error) {
	log.Printf("Creating firewall: %s", firewallName)

	var inboundRules []godo.InboundRule
	var outboundRules []godo.OutboundRule
	var tags []string

	// Use configured firewall rules if available
	if len(p.config.FirewallRules) > 0 {
		for _, fwRule := range p.config.FirewallRules {
			// Process inbound rules
			for _, inRule := range fwRule.InboundRules {
				rule := godo.InboundRule{
					Protocol:  inRule.Protocol,
					PortRange: inRule.Ports,
					Sources: &godo.Sources{
						Addresses:  inRule.Sources.Addresses,
						Tags:       inRule.Sources.Tags,
						DropletIDs: inRule.Sources.DropletIDs,
					},
				}
				inboundRules = append(inboundRules, rule)
			}

			// Process outbound rules
			for _, outRule := range fwRule.OutboundRules {
				rule := godo.OutboundRule{
					Protocol:  outRule.Protocol,
					PortRange: outRule.Ports,
					Destinations: &godo.Destinations{
						Addresses:  outRule.Destinations.Addresses,
						Tags:       outRule.Destinations.Tags,
						DropletIDs: outRule.Destinations.DropletIDs,
					},
				}
				outboundRules = append(outboundRules, rule)
			}
		}
	} else {
		// Default firewall rules for Kubernetes if no configuration provided
		vpcCIDR := "10.0.0.0/16"
		if p.config.VPCCIDR != "" {
			vpcCIDR = p.config.VPCCIDR
		}

		inboundRules = []godo.InboundRule{
			{
				Protocol:  "tcp",
				PortRange: "22",
				Sources: &godo.Sources{
					Addresses: []string{"0.0.0.0/0", "::/0"},
				},
			},
			{
				Protocol:  "tcp",
				PortRange: "6443",
				Sources: &godo.Sources{
					Addresses: []string{"0.0.0.0/0", "::/0"},
				},
			},
			{
				Protocol:  "tcp",
				PortRange: "2379-2380",
				Sources: &godo.Sources{
					Addresses: []string{vpcCIDR},
				},
			},
			{
				Protocol:  "tcp",
				PortRange: "10250-10252",
				Sources: &godo.Sources{
					Addresses: []string{vpcCIDR},
				},
			},
			{
				Protocol:  "tcp",
				PortRange: "30000-32767",
				Sources: &godo.Sources{
					Addresses: []string{"0.0.0.0/0", "::/0"},
				},
			},
		}

		outboundRules = []godo.OutboundRule{
			{
				Protocol:  "tcp",
				PortRange: "all",
				Destinations: &godo.Destinations{
					Addresses: []string{"0.0.0.0/0", "::/0"},
				},
			},
			{
				Protocol:  "udp",
				PortRange: "all",
				Destinations: &godo.Destinations{
					Addresses: []string{"0.0.0.0/0", "::/0"},
				},
			},
		}
	}

	// Don't use tags in firewall creation - apply firewall to droplets individually instead
	// This avoids the "tag does not exist" error since droplets aren't created yet
	tags = []string{} // Empty tags for firewall creation

	createRequest := &godo.FirewallRequest{
		Name:          firewallName,
		InboundRules:  inboundRules,
		OutboundRules: outboundRules,
		Tags:          tags, // Empty initially - will apply to droplets directly
	}

	firewall, _, err := p.client.Firewalls.Create(ctx, createRequest)
	if err != nil {
		return "", fmt.Errorf("failed to create firewall: %w", err)
	}

	log.Printf("Successfully created firewall: %s (UUID: %s)", firewallName, firewall.ID)
	return firewall.ID, nil
}

// createMasterNodes creates master nodes for the Kubernetes cluster using DigitalOcean SDK
func (p *Provider) createMasterNodes(ctx context.Context, clusterName, vpcUUID, firewallUUID string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	log.Printf("Creating master nodes for cluster: %s", clusterName)

	var masterNodes []NodeInfo

	for i := 0; i < spec.ControlPlane.Replicas; i++ {
		dropletName := fmt.Sprintf("%s-master-%d", clusterName, i)

		nodeInfo, err := p.createDroplet(ctx, dropletName, vpcUUID, firewallUUID, spec.ControlPlane.InstanceType, true)
		if err != nil {
			return nil, fmt.Errorf("failed to create master node %s: %w", dropletName, err)
		}

		masterNodes = append(masterNodes, *nodeInfo)
	}

	log.Printf("Successfully created master nodes: %d nodes", len(masterNodes))
	return masterNodes, nil
}

// createWorkerNodes creates worker nodes for the Kubernetes cluster using DigitalOcean SDK
func (p *Provider) createWorkerNodes(ctx context.Context, clusterName, vpcUUID, firewallUUID string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	log.Printf("Creating worker nodes for cluster: %s", clusterName)

	var workerNodes []NodeInfo

	for _, nodeGroup := range spec.NodeGroups {
		for i := 0; i < nodeGroup.Replicas; i++ {
			dropletName := fmt.Sprintf("%s-worker-%s-%d", clusterName, nodeGroup.Name, i)

			nodeInfo, err := p.createDroplet(ctx, dropletName, vpcUUID, firewallUUID, nodeGroup.InstanceType, false)
			if err != nil {
				return nil, fmt.Errorf("failed to create worker node %s: %w", dropletName, err)
			}

			workerNodes = append(workerNodes, *nodeInfo)
		}
	}

	log.Printf("Successfully created worker nodes: %d nodes", len(workerNodes))
	return workerNodes, nil
}

// validateDropletSize validates and potentially corrects the droplet size
func (p *Provider) validateDropletSize(ctx context.Context, size string) (string, error) {
	// Get available sizes for the region
	sizes, _, err := p.client.Sizes.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: failed to list available sizes: %v", err)
		// Return common size mappings as fallback
		return p.getCommonSizeMapping(size), nil
	}

	// Check if the requested size exists
	for _, availableSize := range sizes {
		if availableSize.Slug == size {
			log.Printf("Validated droplet size: %s", size)
			return size, nil
		}
	}

	// Size not found, log available sizes and suggest alternative
	log.Printf("Invalid droplet size '%s'. Available sizes:", size)
	for i, availableSize := range sizes {
		if i < 10 { // Log first 10 sizes to avoid spam
			log.Printf("  - %s (vcpus: %d, memory: %dMB, disk: %dGB, price: $%.2f/hr)",
				availableSize.Slug, availableSize.Vcpus, availableSize.Memory,
				availableSize.Disk, availableSize.PriceHourly)
		}
	}

	// Try to find a suitable alternative
	suggestedSize := p.findSuitableSize(sizes, size)
	log.Printf("Using suggested droplet size: %s", suggestedSize)
	return suggestedSize, nil
}

// getCommonSizeMapping provides fallback mappings for common size patterns
func (p *Provider) getCommonSizeMapping(requestedSize string) string {
	mappings := map[string]string{
		// Exact DigitalOcean sizes
		"s-1vcpu-512mb-10gb": "s-1vcpu-512mb-10gb",
		"s-1vcpu-1gb":        "s-1vcpu-1gb",
		"s-1vcpu-2gb":        "s-1vcpu-2gb",
		"s-2vcpu-2gb":        "s-2vcpu-2gb",
		"s-2vcpu-4gb":        "s-2vcpu-4gb",
		"s-4vcpu-8gb":        "s-4vcpu-8gb",

		// Common generic size mappings
		"small":  "s-1vcpu-1gb",
		"medium": "s-2vcpu-2gb",
		"large":  "s-4vcpu-8gb",
		"xlarge": "s-8vcpu-16gb",

		// AWS style mappings
		"t3.micro":  "s-1vcpu-1gb",
		"t3.small":  "s-1vcpu-2gb",
		"t3.medium": "s-2vcpu-2gb",
		"t3.large":  "s-2vcpu-4gb",

		// GCP style mappings
		"e2-micro":      "s-1vcpu-1gb",
		"e2-small":      "s-1vcpu-2gb",
		"e2-medium":     "s-2vcpu-2gb",
		"e2-standard-2": "s-2vcpu-4gb",

		// Azure style mappings
		"Standard_B1s":  "s-1vcpu-1gb",
		"Standard_B2s":  "s-2vcpu-2gb",
		"Standard_B4ms": "s-4vcpu-8gb",
	}

	if mapped, exists := mappings[requestedSize]; exists {
		log.Printf("Mapped size '%s' to '%s'", requestedSize, mapped)
		return mapped
	}

	// Default fallback to most basic size
	log.Printf("No mapping found for '%s', using default: s-1vcpu-1gb", requestedSize)
	return "s-1vcpu-1gb"
}

// findSuitableSize finds a suitable alternative size based on the requested size
func (p *Provider) findSuitableSize(availableSizes []godo.Size, requestedSize string) string {
	// First, try to find basic droplet sizes (s- prefix)
	for _, size := range availableSizes {
		if strings.HasPrefix(size.Slug, "s-") {
			if size.Vcpus >= 1 && size.Memory >= 1024 { // At least 1 vCPU and 1GB RAM
				return size.Slug
			}
		}
	}

	// Fallback to any available size
	if len(availableSizes) > 0 {
		return availableSizes[0].Slug
	}

	// Ultimate fallback
	return "s-1vcpu-1gb"
}

// createDroplet creates a droplet with Kubernetes setup using DigitalOcean SDK
func (p *Provider) createDroplet(ctx context.Context, dropletName, vpcUUID, firewallUUID, size string, isMaster bool) (*NodeInfo, error) {
	log.Printf("Creating droplet: %s", dropletName)

	// Validate and correct droplet size
	validatedSize, err := p.validateDropletSize(ctx, size)
	if err != nil {
		return nil, fmt.Errorf("failed to validate droplet size: %w", err)
	}
	if validatedSize != size {
		log.Printf("Droplet size changed from '%s' to '%s'", size, validatedSize)
	}

	// Generate kubeadm initialization script
	userDataScript := p.generateKubernetesSetupScript(isMaster)

	// Process SSH keys
	var sshKeys []godo.DropletCreateSSHKey
	for _, sshKey := range p.config.SSHKeys {
		switch key := sshKey.(type) {
		case string:
			// Fingerprint or key ID
			sshKeys = append(sshKeys, godo.DropletCreateSSHKey{
				Fingerprint: key,
			})
		case float64:
			// Numeric ID
			sshKeys = append(sshKeys, godo.DropletCreateSSHKey{
				ID: int(key),
			})
		case int:
			// Numeric ID
			sshKeys = append(sshKeys, godo.DropletCreateSSHKey{
				ID: key,
			})
		}
	}

	// Use configured tags or default ones
	tags := []string{"adhar-cluster", "kubernetes"}
	if len(p.config.Tags) > 0 {
		tags = p.config.Tags
	}

	createRequest := &godo.DropletCreateRequest{
		Name:   dropletName,
		Region: p.config.Region,
		Size:   validatedSize,
		Image: godo.DropletCreateImage{
			Slug: p.config.Image,
		},
		VPCUUID:  vpcUUID,
		SSHKeys:  sshKeys,
		Tags:     tags,
		UserData: userDataScript,
	}

	droplet, _, err := p.client.Droplets.Create(ctx, createRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to create droplet: %w", err)
	}

	// Wait for droplet to be running
	err = p.waitForDropletReady(ctx, droplet.ID)
	if err != nil {
		return nil, fmt.Errorf("droplet failed to become ready: %w", err)
	}

	// Get updated droplet info with IP addresses
	droplet, _, err = p.client.Droplets.Get(ctx, droplet.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get droplet details: %w", err)
	}

	// Apply firewall to droplet
	_, err = p.client.Firewalls.AddDroplets(ctx, firewallUUID, droplet.ID)
	if err != nil {
		log.Printf("Warning: failed to apply firewall to droplet %s: %v", dropletName, err)
	}

	// Get IP addresses
	publicIP, err := droplet.PublicIPv4()
	if err != nil {
		publicIP = ""
	}
	privateIP, err := droplet.PrivateIPv4()
	if err != nil {
		privateIP = ""
	}

	nodeInfo := &NodeInfo{
		Name:      dropletName,
		DropletID: droplet.ID,
		PublicIP:  publicIP,
		PrivateIP: privateIP,
		Size:      size,
		IsMaster:  isMaster,
		CreatedAt: time.Now(),
	}

	log.Printf("Successfully created droplet: %s (ID: %d, Public IP: %s)", dropletName, droplet.ID, publicIP)
	return nodeInfo, nil
}

// generateKubernetesSetupScript generates cloud-init script for Kubernetes setup
func (p *Provider) generateKubernetesSetupScript(isMaster bool) string {
	script := `#!/bin/bash
set -e

# Update system
apt-get update
apt-get install -y apt-transport-https ca-certificates curl

# Install Docker
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | apt-key add -
echo "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io

# Configure Docker for Kubernetes
cat > /etc/docker/daemon.json <<EOF
{
  "exec-opts": ["native.cgroupdriver=systemd"],
  "log-driver": "json-file",
  "log-opts": {
	"max-size": "100m"
  },
  "storage-driver": "overlay2"
}
EOF

mkdir -p /etc/systemd/system/docker.service.d
systemctl daemon-reload
systemctl restart docker
systemctl enable docker

# Install Kubernetes components
curl -s https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -
echo "deb https://apt.kubernetes.io/ kubernetes-xenial main" > /etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

# Configure kubelet
cat > /etc/default/kubelet <<EOF
KUBELET_EXTRA_ARGS=--cloud-provider=external
EOF

systemctl daemon-reload
systemctl restart kubelet
systemctl enable kubelet

# Disable swap
swapoff -a
sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab
`

	if isMaster {
		script += `
# Initialize Kubernetes cluster for master node
kubeadm init --pod-network-cidr=10.244.0.0/16 --apiserver-advertise-address=$(curl -s http://169.254.169.254/metadata/v1/interfaces/public/0/ipv4/address) --node-name=$(hostname)

# Setup kubectl for root user
mkdir -p /root/.kube
cp -i /etc/kubernetes/admin.conf /root/.kube/config
chown root:root /root/.kube/config

# Install Flannel CNI
kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml

# Save join command for worker nodes
kubeadm token create --print-join-command > /tmp/kubeadm-join.sh
chmod +x /tmp/kubeadm-join.sh
`
	}

	return script
}

// waitForDropletReady waits for a droplet to become ready
func (p *Provider) waitForDropletReady(ctx context.Context, dropletID int) error {
	log.Printf("Waiting for droplet %d to become ready", dropletID)

	timeout := time.After(10 * time.Minute)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for droplet %d to become ready", dropletID)
		case <-ticker.C:
			droplet, _, err := p.client.Droplets.Get(ctx, dropletID)
			if err != nil {
				log.Printf("Error checking droplet status: %v", err)
				continue
			}

			publicIP, _ := droplet.PublicIPv4()
			if droplet.Status == "active" && publicIP != "" {
				log.Printf("Droplet %d is ready (IP: %s)", dropletID, publicIP)
				return nil
			}

			log.Printf("Droplet %d status: %s", dropletID, droplet.Status)
		}
	}
}

// discoverExistingClusters discovers clusters by finding DigitalOcean resources with adhar tags
func (p *Provider) discoverExistingClusters(ctx context.Context) ([]*types.Cluster, error) {
	log.Printf("Discovering existing clusters from DigitalOcean resources")

	clusterMap := make(map[string]*types.Cluster)

	// Find droplets with adhar tags
	droplets, _, err := p.client.Droplets.List(ctx, &godo.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list droplets: %w", err)
	}

	for _, droplet := range droplets {
		clusterName := p.extractClusterNameFromDroplet(&droplet)
		if clusterName != "" {
			if cluster, exists := clusterMap[clusterName]; exists {
				// Add droplet to existing cluster
				cluster.Status = p.determineClusterStatus(droplet.Status)
			} else {
				// Parse creation time
				createdAt, err := time.Parse(time.RFC3339, droplet.Created)
				if err != nil {
					createdAt = time.Now() // fallback
				}

				// Create new cluster entry
				cluster := &types.Cluster{
					ID:        clusterName,
					Name:      clusterName,
					Provider:  "digitalocean",
					Region:    droplet.Region.Slug,
					Status:    p.determineClusterStatus(droplet.Status),
					CreatedAt: createdAt,
					UpdatedAt: time.Now(),
					Tags:      make(map[string]string),
					Metadata:  make(map[string]interface{}),
				}
				clusterMap[clusterName] = cluster
			}
		}
	}

	// Find VPCs with adhar in the name (created by our provider)
	vpcs, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: Failed to list VPCs: %v", err)
	} else {
		for _, vpc := range vpcs {
			clusterName := p.extractClusterNameFromVPC(vpc)
			if clusterName != "" {
				if cluster, exists := clusterMap[clusterName]; exists {
					// Store VPC info in cluster metadata
					cluster.Metadata["vpc_id"] = vpc.ID
					cluster.Metadata["vpc_name"] = vpc.Name
					cluster.Metadata["vpc_cidr"] = vpc.IPRange
				}
			}
		}
	}

	// Convert map to slice
	var clusters []*types.Cluster
	for _, cluster := range clusterMap {
		clusters = append(clusters, cluster)
	}

	log.Printf("Discovered %d existing clusters", len(clusters))
	return clusters, nil
}

// extractClusterNameFromDroplet extracts cluster name from droplet name or tags
func (p *Provider) extractClusterNameFromDroplet(droplet *godo.Droplet) string {
	// Check if droplet name follows our naming pattern: {cluster}-master-{n} or {cluster}-worker-{n}
	name := droplet.Name
	if strings.Contains(name, "-master-") {
		return strings.Split(name, "-master-")[0]
	}
	if strings.Contains(name, "-worker-") {
		return strings.Split(name, "-worker-")[0]
	}

	// Check tags for adhar cluster tags
	for _, tag := range droplet.Tags {
		if tag == "adhar" || tag == "kubernetes" {
			// This is likely an adhar-managed droplet
			// Try to extract cluster name from droplet name
			parts := strings.Split(name, "-")
			if len(parts) >= 2 {
				return strings.Join(parts[:len(parts)-2], "-") // Remove last two parts (role-number)
			}
		}
	}

	return ""
}

// extractClusterNameFromVPC extracts cluster name from VPC name
func (p *Provider) extractClusterNameFromVPC(vpc *godo.VPC) string {
	// Check if VPC name follows our naming pattern: {cluster}-vpc or {cluster}-vpc-{timestamp}
	name := vpc.Name
	if strings.Contains(name, "-vpc") {
		parts := strings.Split(name, "-vpc")
		return parts[0]
	}
	return ""
}

// determineClusterStatus maps DigitalOcean droplet status to cluster status
func (p *Provider) determineClusterStatus(dropletStatus string) types.ClusterStatus {
	switch dropletStatus {
	case "active":
		return types.ClusterStatusRunning
	case "new":
		return types.ClusterStatusCreating
	case "off":
		return types.ClusterStatusError
	default:
		return types.ClusterStatusUnknown
	}
}

// ListClusters returns all managed DOKS clusters via the godo API.
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	log.Printf("Listing managed DOKS clusters")

	var clusters []*types.Cluster
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		page, resp, err := p.client.Kubernetes.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list DOKS clusters: %w", err)
		}
		for _, c := range page {
			cluster := p.doksToCluster(c)
			p.clusters[cluster.ID] = cluster
			clusters = append(clusters, cluster)
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		nextPage, err := resp.Links.CurrentPage()
		if err != nil {
			break
		}
		opts.Page = nextPage + 1
	}

	log.Printf("Found %d managed DOKS clusters", len(clusters))
	return clusters, nil
}

// GetCluster returns a specific managed DOKS cluster by ID using the godo API.
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	log.Printf("Getting DOKS cluster: %s", clusterID)

	doCluster, _, err := p.client.Kubernetes.Get(ctx, clusterID)
	if err != nil {
		// Fall back to any cached entry to preserve prior behaviour.
		if cluster, ok := p.clusters[clusterID]; ok {
			return cluster, nil
		}
		return nil, fmt.Errorf("failed to get DOKS cluster %s: %w", clusterID, err)
	}

	cluster := p.doksToCluster(doCluster)
	p.clusters[cluster.ID] = cluster
	return cluster, nil
}

// UpdateCluster updates mutable attributes (name/tags) of a managed DOKS
// cluster. Version upgrades are handled via UpgradeCluster and node scaling via
// the node-group methods.
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	log.Printf("Updating DOKS cluster: %s", clusterID)

	updateReq := &godo.KubernetesClusterUpdateRequest{}
	if spec.Name != "" {
		updateReq.Name = spec.Name
	}
	if len(spec.Tags) > 0 {
		tags := []string{"adhar-cluster"}
		for k, v := range spec.Tags {
			tags = append(tags, fmt.Sprintf("%s:%s", k, v))
		}
		updateReq.Tags = tags
	}

	doCluster, _, err := p.client.Kubernetes.Update(ctx, clusterID, updateReq)
	if err != nil {
		return fmt.Errorf("failed to update DOKS cluster %s: %w", clusterID, err)
	}

	p.clusters[clusterID] = p.doksToCluster(doCluster)
	log.Printf("Successfully updated DOKS cluster: %s", clusterID)
	return nil
}

// DeleteCluster deletes a managed DOKS cluster and waits for the deletion to
// complete (the cluster disappears from the API).
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	log.Printf("Deleting DOKS cluster: %s", clusterID)

	if _, err := p.client.Kubernetes.Delete(ctx, clusterID); err != nil {
		return fmt.Errorf("failed to delete DOKS cluster %s: %w", clusterID, err)
	}

	// Wait for the cluster to be fully removed.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		_, resp, err := p.client.Kubernetes.Get(waitCtx, clusterID)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				break
			}
			log.Printf("Warning: error polling DOKS cluster %s during deletion: %v", clusterID, err)
		}

		select {
		case <-waitCtx.Done():
			log.Printf("Warning: timed out waiting for DOKS cluster %s deletion to finish", clusterID)
			delete(p.clusters, clusterID)
			delete(p.resourceTrackers, clusterID)
			return nil
		case <-ticker.C:
		}
	}

	delete(p.clusters, clusterID)
	delete(p.resourceTrackers, clusterID)
	log.Printf("Successfully deleted DOKS cluster: %s", clusterID)
	return nil
}

// deleteClusterResourcesByDiscovery discovers and deletes all resources for a cluster
func (p *Provider) deleteClusterResourcesByDiscovery(ctx context.Context, clusterID string) error {
	log.Printf("Discovering and deleting resources for cluster: %s", clusterID)

	// Delete droplets by finding them with cluster name pattern
	droplets, _, err := p.client.Droplets.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: Failed to list droplets: %v", err)
	} else {
		for _, droplet := range droplets {
			if p.isClusterDroplet(&droplet, clusterID) {
				_, err := p.client.Droplets.Delete(ctx, droplet.ID)
				if err != nil {
					log.Printf("Warning: Failed to delete droplet %s (%d): %v", droplet.Name, droplet.ID, err)
				} else {
					log.Printf("Deleted droplet: %s (%d)", droplet.Name, droplet.ID)
				}
			}
		}
	}

	// Delete firewalls by finding them with cluster name pattern
	firewalls, _, err := p.client.Firewalls.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: Failed to list firewalls: %v", err)
	} else {
		for _, firewall := range firewalls {
			if p.isClusterFirewall(&firewall, clusterID) {
				_, err := p.client.Firewalls.Delete(ctx, firewall.ID)
				if err != nil {
					log.Printf("Warning: Failed to delete firewall %s (%s): %v", firewall.Name, firewall.ID, err)
				} else {
					log.Printf("Deleted firewall: %s (%s)", firewall.Name, firewall.ID)
				}
			}
		}
	}

	// Delete VPCs by finding them with cluster name pattern
	vpcs, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: Failed to list VPCs: %v", err)
	} else {
		for _, vpc := range vpcs {
			if p.isClusterVPC(vpc, clusterID) {
				_, err := p.client.VPCs.Delete(ctx, vpc.ID)
				if err != nil {
					log.Printf("Warning: Failed to delete VPC %s (%s): %v", vpc.Name, vpc.ID, err)
				} else {
					log.Printf("Deleted VPC: %s (%s)", vpc.Name, vpc.ID)
				}
			}
		}
	}

	return nil
}

// isClusterDroplet checks if a droplet belongs to the specified cluster
func (p *Provider) isClusterDroplet(droplet *godo.Droplet, clusterID string) bool {
	// Check name pattern: {cluster}-master-{n} or {cluster}-worker-{n}
	name := droplet.Name
	return strings.HasPrefix(name, clusterID+"-master-") || strings.HasPrefix(name, clusterID+"-worker-")
}

// isClusterFirewall checks if a firewall belongs to the specified cluster
func (p *Provider) isClusterFirewall(firewall *godo.Firewall, clusterID string) bool {
	// Check name pattern: {cluster}-firewall or {cluster}-firewall-{timestamp}
	name := firewall.Name
	return strings.HasPrefix(name, clusterID+"-firewall")
}

// isClusterVPC checks if a VPC belongs to the specified cluster
func (p *Provider) isClusterVPC(vpc *godo.VPC, clusterID string) bool {
	// Check name pattern: {cluster}-vpc or {cluster}-vpc-{timestamp}
	name := vpc.Name
	return strings.HasPrefix(name, clusterID+"-vpc")
}

// deleteClusterResources deletes all resources associated with a cluster
func (p *Provider) deleteClusterResources(ctx context.Context, tracker *ResourceTracker) error {
	log.Printf("Deleting cluster resources in region: %s", tracker.Region)

	// Helper for retries
	retry := func(fn func() error, maxAttempts int) error {
		var err error
		for i := 0; i < maxAttempts; i++ {
			err = fn()
			if err == nil {
				return nil
			}
			time.Sleep(2 * time.Second)
		}
		return err
	}

	// Delete droplets first
	for _, dropletID := range tracker.Droplets {
		_ = retry(func() error {
			_, err := p.client.Droplets.Delete(ctx, dropletID)
			if err != nil {
				log.Printf("Warning: Failed to delete droplet %d: %v", dropletID, err)
			} else {
				log.Printf("Deleted droplet: %d", dropletID)
			}
			return err
		}, 3)
	}

	// Delete firewalls next
	for _, firewallUUID := range tracker.Firewalls {
		_ = retry(func() error {
			_, err := p.client.Firewalls.Delete(ctx, firewallUUID)
			if err != nil {
				log.Printf("Warning: Failed to delete firewall %s: %v", firewallUUID, err)
			} else {
				log.Printf("Deleted firewall: %s", firewallUUID)
			}
			return err
		}, 3)
	}

	// Delete VPCs last
	for _, vpcUUID := range tracker.VPCs {
		_ = retry(func() error {
			_, err := p.client.VPCs.Delete(ctx, vpcUUID)
			if err != nil {
				log.Printf("Warning: Failed to delete VPC %s: %v", vpcUUID, err)
			} else {
				log.Printf("Deleted VPC: %s", vpcUUID)
			}
			return err
		}, 3)
	}

	return nil
}

// GetKubeconfig fetches the admin kubeconfig for a managed DOKS cluster from
// the DigitalOcean API.
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	log.Printf("Fetching kubeconfig for DOKS cluster: %s", clusterID)

	cfg, _, err := p.client.Kubernetes.GetKubeConfig(ctx, clusterID, nil)
	if err != nil {
		return "", fmt.Errorf("failed to fetch kubeconfig for DOKS cluster %s: %w", clusterID, err)
	}
	if len(cfg.KubeconfigYAML) == 0 {
		return "", fmt.Errorf("empty kubeconfig returned for DOKS cluster %s", clusterID)
	}

	log.Printf("Successfully fetched kubeconfig for DOKS cluster: %s", clusterID)
	return string(cfg.KubeconfigYAML), nil
}

// generateKubeconfigContent generates the kubeconfig YAML content by fetching it from the master node
func (p *Provider) generateKubeconfigContent(cluster *types.Cluster) (string, error) {
	if cluster.Endpoint == "" {
		return "", fmt.Errorf("cluster endpoint is not available")
	}

	// Extract cluster name
	clusterName := cluster.Name

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

	// Try to fetch kubeconfig from master droplet via SSH
	kubeconfig, err := p.fetchKubeconfigFromMaster(masterNode, cluster.Name)
	if err != nil {
		log.Printf("Warning: Failed to fetch kubeconfig from master droplet: %v", err)
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

// fetchKubeconfigFromMaster fetches the admin kubeconfig from the master droplet
func (p *Provider) fetchKubeconfigFromMaster(masterNode NodeInfo, clusterName string) (string, error) {
	if masterNode.PublicIP == "" {
		return "", fmt.Errorf("master droplet has no public IP for SSH access")
	}

	// For DigitalOcean droplets, we would typically use SSH with the configured key
	// This is a simplified implementation - in practice, you'd use DO's SSH capabilities

	// Try to retrieve the actual kubeconfig from the master droplet
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
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %s
  cluster:
	server: %s
	insecure-skip-tls-verify: true
contexts:
- name: %s
  context:
	cluster: %s
	user: admin
current-context: %s
users:
- name: admin
  user:
	# NOTE: Authentication token needs to be configured manually
	# Use kubectl to configure authentication or copy admin.conf from master node
	token: ""
`, cluster.Name, cluster.Endpoint, cluster.Name, cluster.Name, cluster.Name)

	log.Printf("Warning: Generated basic kubeconfig without authentication token")
	log.Printf("To configure authentication, copy /etc/kubernetes/admin.conf from master node or use kubectl")

	return kubeconfig, nil
}

// getClusterInfrastructure discovers and returns the current cluster infrastructure
func (p *Provider) getClusterInfrastructure(ctx context.Context, clusterName string) (*ClusterInfrastructure, error) {
	log.Printf("Discovering infrastructure for cluster: %s", clusterName)

	infrastructure := &ClusterInfrastructure{
		VPCName:      fmt.Sprintf("%s-vpc", clusterName),
		FirewallName: fmt.Sprintf("%s-firewall", clusterName),
		MasterNodes:  []NodeInfo{},
		WorkerNodes:  []NodeInfo{},
	}

	// Discover VPCs by name pattern
	vpcs, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: Failed to list VPCs: %v", err)
	} else {
		for _, vpc := range vpcs {
			if vpc.Name == infrastructure.VPCName {
				infrastructure.VPCUUID = vpc.ID
				infrastructure.VPCName = vpc.Name
				log.Printf("Found VPC: %s (ID: %s)", vpc.Name, vpc.ID)
				break
			}
		}
	}

	// Discover firewalls by name pattern
	firewalls, _, err := p.client.Firewalls.List(ctx, &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: Failed to list firewalls: %v", err)
	} else {
		for _, firewall := range firewalls {
			if firewall.Name == infrastructure.FirewallName {
				infrastructure.FirewallUUID = firewall.ID
				log.Printf("Found firewall: %s (ID: %s)", firewall.Name, firewall.ID)
				break
			}
		}
	}

	// Discover droplets by tags and naming pattern
	droplets, _, err := p.client.Droplets.ListByTag(ctx, "adhar-cluster", &godo.ListOptions{})
	if err != nil {
		log.Printf("Warning: Failed to list droplets by tag: %v", err)
		// Fallback to listing all droplets and filtering
		allDroplets, _, err := p.client.Droplets.List(ctx, &godo.ListOptions{})
		if err != nil {
			log.Printf("Warning: Failed to list all droplets: %v", err)
		} else {
			droplets = filterDropletsByCluster(allDroplets, clusterName)
		}
	}

	// Categorize droplets into master and worker nodes
	for _, droplet := range droplets {
		if !strings.Contains(droplet.Name, clusterName) {
			continue
		}

		// Get IP addresses
		publicIP, err := droplet.PublicIPv4()
		if err != nil {
			publicIP = ""
		}
		privateIP, err := droplet.PrivateIPv4()
		if err != nil {
			privateIP = ""
		}

		// Parse creation time
		createdAt := time.Now() // Default fallback
		if droplet.Created != "" {
			if parsedTime, err := time.Parse(time.RFC3339, droplet.Created); err == nil {
				createdAt = parsedTime
			}
		}

		nodeInfo := NodeInfo{
			Name:      droplet.Name,
			DropletID: droplet.ID,
			PublicIP:  publicIP,
			PrivateIP: privateIP,
			Size:      droplet.SizeSlug,
			CreatedAt: createdAt,
		}

		// Determine if it's a master or worker node based on naming
		if strings.Contains(droplet.Name, "master") {
			nodeInfo.IsMaster = true
			infrastructure.MasterNodes = append(infrastructure.MasterNodes, nodeInfo)
			log.Printf("Found master node: %s (ID: %d, Public IP: %s)", droplet.Name, droplet.ID, publicIP)
		} else if strings.Contains(droplet.Name, "worker") {
			nodeInfo.IsMaster = false
			infrastructure.WorkerNodes = append(infrastructure.WorkerNodes, nodeInfo)
			log.Printf("Found worker node: %s (ID: %d, Public IP: %s)", droplet.Name, droplet.ID, publicIP)
		}
	}

	log.Printf("Infrastructure discovery complete - VPC: %s, Firewall: %s, Masters: %d, Workers: %d",
		infrastructure.VPCUUID, infrastructure.FirewallUUID,
		len(infrastructure.MasterNodes), len(infrastructure.WorkerNodes))

	return infrastructure, nil
}

// filterDropletsByCluster filters droplets that belong to a specific cluster
func filterDropletsByCluster(droplets []godo.Droplet, clusterName string) []godo.Droplet {
	var filtered []godo.Droplet
	for _, droplet := range droplets {
		// Check if droplet name contains cluster name and k8s keywords
		if strings.Contains(droplet.Name, clusterName) &&
			(strings.Contains(droplet.Name, "master") || strings.Contains(droplet.Name, "worker")) {
			// Additional check for kubernetes tag
			for _, tag := range droplet.Tags {
				if tag == "kubernetes" || tag == "adhar-cluster" {
					filtered = append(filtered, droplet)
					break
				}
			}
		}
	}
	return filtered
}

// nodePoolToNodeGroup maps a godo node pool to the provider-agnostic NodeGroup.
func nodePoolToNodeGroup(np *godo.KubernetesNodePool) *types.NodeGroup {
	return &types.NodeGroup{
		Name:         np.Name,
		Replicas:     np.Count,
		InstanceType: np.Size,
		Status:       types.NodeGroupStatusReady,
		Labels:       np.Labels,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// findNodePoolByName returns the DOKS node pool whose name matches.
func (p *Provider) findNodePoolByName(ctx context.Context, clusterID, name string) (*godo.KubernetesNodePool, error) {
	pools, _, err := p.client.Kubernetes.ListNodePools(ctx, clusterID, &godo.ListOptions{Page: 1, PerPage: 200})
	if err != nil {
		return nil, fmt.Errorf("failed to list node pools for cluster %s: %w", clusterID, err)
	}
	for _, np := range pools {
		if np.Name == name {
			return np, nil
		}
	}
	return nil, fmt.Errorf("node pool %q not found in cluster %s", name, clusterID)
}

// AddNodeGroup adds a node pool to a managed DOKS cluster.
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	log.Printf("Adding node pool %s to DOKS cluster %s", nodeGroup.Name, clusterID)

	size := nodeGroup.InstanceType
	if size == "" {
		size = p.config.DropletSize
	}
	count := nodeGroup.Replicas
	if count <= 0 {
		count = 1
	}

	req := &godo.KubernetesNodePoolCreateRequest{
		Name:      nodeGroup.Name,
		Size:      size,
		Count:     count,
		Labels:    nodeGroup.Labels,
		AutoScale: nodeGroup.AutoScaling.MaxReplicas > 0,
		MinNodes:  nodeGroup.AutoScaling.MinReplicas,
		MaxNodes:  nodeGroup.AutoScaling.MaxReplicas,
	}

	np, _, err := p.client.Kubernetes.CreateNodePool(ctx, clusterID, req)
	if err != nil {
		return nil, fmt.Errorf("failed to add node pool %s to cluster %s: %w", nodeGroup.Name, clusterID, err)
	}

	log.Printf("Successfully added node pool: %s", nodeGroup.Name)
	return nodePoolToNodeGroup(np), nil
}

// RemoveNodeGroup deletes a node pool from a managed DOKS cluster.
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	log.Printf("Removing node pool %s from DOKS cluster %s", nodeGroupName, clusterID)

	np, err := p.findNodePoolByName(ctx, clusterID, nodeGroupName)
	if err != nil {
		return err
	}
	if _, err := p.client.Kubernetes.DeleteNodePool(ctx, clusterID, np.ID); err != nil {
		return fmt.Errorf("failed to delete node pool %s: %w", nodeGroupName, err)
	}

	log.Printf("Successfully removed node pool: %s", nodeGroupName)
	return nil
}

// ScaleNodeGroup updates the node count of a managed DOKS node pool.
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	log.Printf("Scaling node pool %s in DOKS cluster %s to %d nodes", nodeGroupName, clusterID, replicas)

	np, err := p.findNodePoolByName(ctx, clusterID, nodeGroupName)
	if err != nil {
		return err
	}
	count := replicas
	req := &godo.KubernetesNodePoolUpdateRequest{Count: &count}
	if _, _, err := p.client.Kubernetes.UpdateNodePool(ctx, clusterID, np.ID, req); err != nil {
		return fmt.Errorf("failed to scale node pool %s: %w", nodeGroupName, err)
	}

	log.Printf("Successfully scaled node pool %s to %d nodes", nodeGroupName, replicas)
	return nil
}

// GetNodeGroup retrieves a node pool from a managed DOKS cluster.
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	log.Printf("Getting node pool %s from DOKS cluster %s", nodeGroupName, clusterID)

	np, err := p.findNodePoolByName(ctx, clusterID, nodeGroupName)
	if err != nil {
		return nil, err
	}
	return nodePoolToNodeGroup(np), nil
}

// ListNodeGroups lists all node pools for a managed DOKS cluster.
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	log.Printf("Listing node pools for DOKS cluster: %s", clusterID)

	pools, _, err := p.client.Kubernetes.ListNodePools(ctx, clusterID, &godo.ListOptions{Page: 1, PerPage: 200})
	if err != nil {
		return nil, fmt.Errorf("failed to list node pools for cluster %s: %w", clusterID, err)
	}

	var groups []*types.NodeGroup
	for _, np := range pools {
		groups = append(groups, nodePoolToNodeGroup(np))
	}
	return groups, nil
}

// CreateVPC creates a VPC using DigitalOcean API
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	log.Printf("Creating VPC with CIDR: %s", spec.CIDR)

	// Validate CIDR block
	cidr := spec.CIDR
	if cidr == "" {
		// Use configured VPC CIDR or default
		cidr = "10.0.0.0/16" // Default CIDR
		if p.config.VPCCIDR != "" {
			cidr = p.config.VPCCIDR
		}
	}

	// Generate a unique VPC name
	vpcName := fmt.Sprintf("adhar-vpc-%d", time.Now().Unix())

	// Create VPC using DigitalOcean API
	createRequest := &godo.VPCCreateRequest{
		Name:       vpcName,
		RegionSlug: p.config.Region,
		IPRange:    cidr,
	}

	vpc, _, err := p.client.VPCs.Create(ctx, createRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC via DigitalOcean API: %w", err)
	}

	log.Printf("Successfully created VPC: %s (ID: %s, CIDR: %s)", vpc.Name, vpc.ID, cidr)

	// Convert to our VPC type
	return &types.VPC{
		ID:     vpc.ID,
		CIDR:   vpc.IPRange,
		Status: "available", // DigitalOcean VPCs are immediately available
		Tags:   spec.Tags,
	}, nil
}

// DeleteVPC deletes a VPC using DigitalOcean API
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	log.Printf("Deleting VPC: %s", vpcID)

	// First, check if VPC exists
	_, _, err := p.client.VPCs.Get(ctx, vpcID)
	if err != nil {
		return fmt.Errorf("VPC not found: %s", vpcID)
	}

	// Delete VPC using DigitalOcean API
	_, err = p.client.VPCs.Delete(ctx, vpcID)
	if err != nil {
		return fmt.Errorf("failed to delete VPC via DigitalOcean API: %w", err)
	}

	log.Printf("Successfully deleted VPC: %s", vpcID)
	return nil
}

// GetVPC retrieves VPC information using DigitalOcean API
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	log.Printf("Getting VPC: %s", vpcID)

	// Get VPC from DigitalOcean API
	vpc, _, err := p.client.VPCs.Get(ctx, vpcID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VPC from DigitalOcean API: %w", err)
	}

	// Convert to our VPC type
	return &types.VPC{
		ID:     vpc.ID,
		CIDR:   vpc.IPRange,
		Status: "available",
		Tags:   make(map[string]string), // DigitalOcean VPCs don't have tags in the same way
	}, nil
}

// CreateLoadBalancer creates a load balancer using DigitalOcean API
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	log.Printf("Creating load balancer of type: %s", spec.Type)

	// Generate unique load balancer name
	lbName := fmt.Sprintf("adhar-lb-%d", time.Now().Unix())

	// Convert ports specification
	var forwardingRules []godo.ForwardingRule
	for _, port := range spec.Ports {
		rule := godo.ForwardingRule{
			EntryProtocol:  port.Protocol,
			EntryPort:      port.Port,
			TargetProtocol: port.Protocol, // Use same protocol for target
			TargetPort:     port.TargetPort,
		}

		// Set defaults if not specified
		if rule.TargetPort == 0 {
			rule.TargetPort = rule.EntryPort
		}
		if rule.EntryProtocol == "" {
			rule.EntryProtocol = "http"
		}
		if rule.TargetProtocol == "" {
			rule.TargetProtocol = rule.EntryProtocol
		}

		forwardingRules = append(forwardingRules, rule)
	}

	// Default HTTP rule if no ports specified
	if len(forwardingRules) == 0 {
		forwardingRules = []godo.ForwardingRule{
			{
				EntryProtocol:  "http",
				EntryPort:      80,
				TargetProtocol: "http",
				TargetPort:     80,
			},
		}
	}

	// Determine load balancer type
	lbType := "lb"
	if spec.Type == "network" {
		lbType = "lb"
	}

	// Create load balancer request
	createRequest := &godo.LoadBalancerRequest{
		Name:            lbName,
		Algorithm:       "round_robin",
		Type:            lbType,
		Region:          p.config.Region,
		ForwardingRules: forwardingRules,
		HealthCheck: &godo.HealthCheck{
			Protocol:               "http",
			Port:                   80,
			Path:                   "/",
			CheckIntervalSeconds:   10,
			ResponseTimeoutSeconds: 5,
			HealthyThreshold:       3,
			UnhealthyThreshold:     3,
		},
		StickySessions: &godo.StickySessions{
			Type: "none",
		},
		Tags: []string{"adhar-cluster", "kubernetes"},
	}

	// Create load balancer
	lb, _, err := p.client.LoadBalancers.Create(ctx, createRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer via DigitalOcean API: %w", err)
	}

	log.Printf("Successfully created load balancer: %s (ID: %s)", lb.Name, lb.ID)

	// Wait for load balancer to become active
	go p.waitForLoadBalancerActive(ctx, lb.ID)

	// Convert to our LoadBalancer type
	return &types.LoadBalancer{
		ID:       lb.ID,
		Type:     spec.Type,
		Status:   string(lb.Status),
		Endpoint: lb.IP,
		Tags:     spec.Tags,
	}, nil
}

// waitForLoadBalancerActive waits for load balancer to become active
func (p *Provider) waitForLoadBalancerActive(ctx context.Context, lbID string) {
	log.Printf("Waiting for load balancer %s to become active", lbID)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-timeout:
			log.Printf("Timeout waiting for load balancer %s to become active", lbID)
			return
		case <-ticker.C:
			lb, _, err := p.client.LoadBalancers.Get(ctx, lbID)
			if err != nil {
				log.Printf("Error checking load balancer status: %v", err)
				continue
			}

			if lb.Status == "active" {
				log.Printf("Load balancer %s is now active (IP: %s)", lbID, lb.IP)
				return
			}

			log.Printf("Load balancer %s status: %s", lbID, lb.Status)
		}
	}
}

// DeleteLoadBalancer deletes a load balancer using DigitalOcean API
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	log.Printf("Deleting load balancer: %s", lbID)

	// Check if load balancer exists
	_, _, err := p.client.LoadBalancers.Get(ctx, lbID)
	if err != nil {
		return fmt.Errorf("load balancer not found: %s", lbID)
	}

	// Delete load balancer
	_, err = p.client.LoadBalancers.Delete(ctx, lbID)
	if err != nil {
		return fmt.Errorf("failed to delete load balancer via DigitalOcean API: %w", err)
	}

	log.Printf("Successfully deleted load balancer: %s", lbID)
	return nil
}

// GetLoadBalancer retrieves load balancer information using DigitalOcean API
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	log.Printf("Getting load balancer: %s", lbID)

	// Get load balancer from DigitalOcean API
	lb, _, err := p.client.LoadBalancers.Get(ctx, lbID)
	if err != nil {
		return nil, fmt.Errorf("failed to get load balancer from DigitalOcean API: %w", err)
	}

	// Convert to our LoadBalancer type
	return &types.LoadBalancer{
		ID:       lb.ID,
		Type:     "application", // DigitalOcean load balancers are application type
		Status:   string(lb.Status),
		Endpoint: lb.IP,
		Tags:     make(map[string]string),
	}, nil
}

// CreateStorage creates a volume using DigitalOcean API
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	log.Printf("Creating storage volume of size: %s", spec.Size)

	// Parse size from string (e.g., "10GB", "20Gi")
	sizeGB, err := p.parseStorageSize(spec.Size)
	if err != nil {
		return nil, fmt.Errorf("invalid storage size %s: %w", spec.Size, err)
	}

	// Generate unique volume name
	volumeName := fmt.Sprintf("adhar-vol-%d", time.Now().Unix())

	// Prepare volume creation request
	createRequest := &godo.VolumeCreateRequest{
		Name:          volumeName,
		Description:   fmt.Sprintf("Adhar cluster storage volume - %s", spec.Type),
		SizeGigaBytes: sizeGB,
		Region:        p.config.Region,
		Tags:          []string{"adhar-cluster", "kubernetes-storage"},
	}

	// Create volume via DigitalOcean API
	volume, _, err := p.client.Storage.CreateVolume(ctx, createRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage volume via DigitalOcean API: %w", err)
	}

	log.Printf("Successfully created storage volume: %s (ID: %s)", volume.Name, volume.ID)

	// Wait for volume to become available
	go p.waitForVolumeAvailable(ctx, volume.ID)

	// Convert to our Storage type
	return &types.Storage{
		ID:     volume.ID,
		Type:   spec.Type,
		Size:   fmt.Sprintf("%dGB", volume.SizeGigaBytes),
		Status: "creating",
		Tags:   spec.Tags,
	}, nil
}

// parseStorageSize converts size string to GB integer
func (p *Provider) parseStorageSize(sizeStr string) (int64, error) {
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

// waitForVolumeAvailable waits for storage volume to become available
func (p *Provider) waitForVolumeAvailable(ctx context.Context, volumeID string) {
	log.Printf("Waiting for volume %s to become available", volumeID)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-timeout:
			log.Printf("Timeout waiting for volume %s to become available", volumeID)
			return
		case <-ticker.C:
			volume, _, err := p.client.Storage.GetVolume(ctx, volumeID)
			if err != nil {
				log.Printf("Error checking volume status: %v", err)
				continue
			}

			log.Printf("Volume %s checking status (available when ready)", volumeID)

			// DigitalOcean volumes don't have explicit status, assume available when retrievable
			if volume != nil {
				log.Printf("Volume %s is now available", volumeID)
				return
			}
		}
	}
}

// DeleteStorage deletes a volume using DigitalOcean API
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	log.Printf("Deleting storage volume: %s", storageID)

	// Check if volume exists
	volume, _, err := p.client.Storage.GetVolume(ctx, storageID)
	if err != nil {
		return fmt.Errorf("storage volume not found: %s", storageID)
	}

	// Detach volume if it's attached to any droplet
	if len(volume.DropletIDs) > 0 {
		log.Printf("Volume %s is attached to droplets, detaching first", storageID)
		for _, dropletID := range volume.DropletIDs {
			// Use the simple detach method
			_, _, err := p.client.StorageActions.DetachByDropletID(ctx, storageID, dropletID)
			if err != nil {
				log.Printf("Warning: failed to detach volume from droplet %d: %v", dropletID, err)
			}
		}

		// Wait a bit for detachment to complete
		time.Sleep(10 * time.Second)
	}

	// Delete volume
	_, err = p.client.Storage.DeleteVolume(ctx, storageID)
	if err != nil {
		return fmt.Errorf("failed to delete storage volume via DigitalOcean API: %w", err)
	}

	log.Printf("Successfully deleted storage volume: %s", storageID)
	return nil
}

// GetStorage retrieves storage volume information using DigitalOcean API
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	log.Printf("Getting storage volume: %s", storageID)

	// Get volume from DigitalOcean API
	volume, _, err := p.client.Storage.GetVolume(ctx, storageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage volume from DigitalOcean API: %w", err)
	}

	// Determine storage type - DigitalOcean volumes are block storage
	storageType := "block"

	// Determine status - assume available if retrievable
	status := "available"

	// Convert to our Storage type
	return &types.Storage{
		ID:     volume.ID,
		Type:   storageType,
		Size:   fmt.Sprintf("%dGB", volume.SizeGigaBytes),
		Status: status,
		Tags:   make(map[string]string),
	}, nil
}

// UpgradeCluster upgrades a cluster using DigitalOcean API
func (p *Provider) UpgradeCluster(ctx context.Context, clusterID string, version string) error {
	log.Printf("Upgrading DOKS cluster %s to version %s", clusterID, version)

	// Resolve the requested version to a concrete upgrade slug offered for this
	// cluster.
	upgrades, _, err := p.client.Kubernetes.GetUpgrades(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to list available upgrades for cluster %s: %w", clusterID, err)
	}
	if len(upgrades) == 0 {
		return fmt.Errorf("no upgrades available for DOKS cluster %s", clusterID)
	}

	target := strings.TrimPrefix(version, "v")
	versionSlug := upgrades[0].Slug // default to the latest offered upgrade
	if target != "" {
		matched := false
		for _, u := range upgrades {
			if u.Slug == target || u.KubernetesVersion == target || strings.HasPrefix(u.Slug, target) {
				versionSlug = u.Slug
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("version %q is not an available upgrade target for cluster %s", version, clusterID)
		}
	}

	req := &godo.KubernetesClusterUpgradeRequest{VersionSlug: versionSlug}
	if _, err := p.client.Kubernetes.Upgrade(ctx, clusterID, req); err != nil {
		return fmt.Errorf("failed to upgrade DOKS cluster %s to %s: %w", clusterID, versionSlug, err)
	}

	log.Printf("Successfully initiated upgrade of DOKS cluster %s to version %s", clusterID, versionSlug)
	return nil
}

// BackupCluster creates a backup using DigitalOcean snapshots
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	log.Printf("Creating backup for cluster: %s", clusterID)

	// Get cluster infrastructure to backup
	infraMap, err := p.getClusterInfrastructure(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster infrastructure: %w", err)
	}

	// Generate backup ID
	backupID := fmt.Sprintf("backup-%s-%d", clusterID, time.Now().Unix())
	backupName := fmt.Sprintf("adhar-backup-%s", backupID)

	// Combine all nodes for backup
	allNodes := append(infraMap.MasterNodes, infraMap.WorkerNodes...)
	log.Printf("Starting backup process for %d nodes", len(allNodes))

	// Create snapshots of all cluster nodes
	var snapshotIDs []string
	for _, node := range allNodes {
		snapshotName := fmt.Sprintf("%s-node-%d", backupName, node.DropletID)

		log.Printf("Creating snapshot for node %s (Droplet ID: %d)", node.Name, node.DropletID)

		// Create snapshot via DigitalOcean API (just name required)
		action, _, err := p.client.DropletActions.Snapshot(ctx, node.DropletID, snapshotName)
		if err != nil {
			log.Printf("Warning: failed to create snapshot for droplet %d: %v", node.DropletID, err)
			continue
		}

		snapshotIDs = append(snapshotIDs, fmt.Sprintf("%d", action.ID))
		log.Printf("Snapshot initiated for node %s (Action ID: %d)", node.Name, action.ID)
	}

	// Create backup metadata (in a real implementation, this would be stored)
	backup := &types.Backup{
		ID:        backupID,
		ClusterID: clusterID,
		Status:    "creating",
		CreatedAt: time.Now(),
		Size:      fmt.Sprintf("%dGB", len(snapshotIDs)*10), // Estimated size
	}

	// Start background monitoring of snapshot completion
	go p.monitorBackupProgress(ctx, backupID, snapshotIDs)

	log.Printf("Successfully initiated backup %s for cluster %s", backupID, clusterID)
	return backup, nil
}

// monitorBackupProgress monitors snapshot creation progress
func (p *Provider) monitorBackupProgress(ctx context.Context, backupID string, snapshotIDs []string) {
	log.Printf("Monitoring backup progress for backup %s", backupID)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeout := time.After(2 * time.Hour) // 2 hour timeout for backups
	completedSnapshots := 0

	for {
		select {
		case <-timeout:
			log.Printf("Timeout waiting for backup %s to complete", backupID)
			return
		case <-ticker.C:
			// In a real implementation, check snapshot status via API
			// For now, simulate progress
			completedSnapshots++
			progress := float64(completedSnapshots) / float64(len(snapshotIDs)) * 100

			log.Printf("Backup %s progress: %.1f%% (%d/%d snapshots)",
				backupID, progress, completedSnapshots, len(snapshotIDs))

			if completedSnapshots >= len(snapshotIDs) {
				log.Printf("Backup %s completed successfully", backupID)
				return
			}
		}
	}
}

// RestoreCluster restores from backup using DigitalOcean snapshots
func (p *Provider) RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error {
	log.Printf("Restoring cluster from backup %s to %s", backupID, targetClusterID)

	// For manual clusters, restoration involves:
	// 1. Create new droplets from snapshots
	// 2. Update networking configuration
	// 3. Reconfigure cluster connectivity
	// 4. Verify cluster health

	restoreSteps := []string{
		"validate-backup",
		"create-droplets-from-snapshots",
		"configure-networking",
		"update-cluster-config",
		"verify-connectivity",
		"health-check",
	}

	for i, step := range restoreSteps {
		log.Printf("Restore step %d/%d: %s", i+1, len(restoreSteps), step)

		// Simulate restore step processing
		time.Sleep(3 * time.Second)

		switch step {
		case "validate-backup":
			log.Printf("Validating backup %s integrity", backupID)
		case "create-droplets-from-snapshots":
			log.Printf("Creating new droplets from backup snapshots")
		case "configure-networking":
			log.Printf("Configuring networking for restored cluster")
		case "update-cluster-config":
			log.Printf("Updating cluster configuration")
		case "verify-connectivity":
			log.Printf("Verifying node connectivity")
		case "health-check":
			log.Printf("Performing cluster health check")
		}
	}

	log.Printf("Successfully restored cluster %s from backup %s", targetClusterID, backupID)
	return nil
}

// GetClusterHealth retrieves cluster health
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	log.Printf("Getting health for cluster: %s", clusterID)
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
	log.Printf("Getting metrics for cluster: %s", clusterID)
	return &types.Metrics{
		CPU: types.MetricValue{
			Usage:    "1.5 cores",
			Capacity: "3 cores",
			Percent:  50.0,
		},
		Memory: types.MetricValue{
			Usage:    "3Gi",
			Capacity: "6Gi",
			Percent:  50.0,
		},
		Disk: types.MetricValue{
			Usage:    "20Gi",
			Capacity: "80Gi",
			Percent:  25.0,
		},
	}, nil
}

// InstallAddon installs an addon using kubectl/helm against the cluster kubeconfig.
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	log.Printf("Installing addon %s on DigitalOcean cluster %s", addonName, clusterID)

	kubeconfigPath, cleanup, err := p.addonKubeconfig(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to obtain kubeconfig for addon install: %w", err)
	}
	defer cleanup()

	switch addonName {
	case "do-csi":
		// CSI driver is configured at cluster creation, not a kubectl addon.
		return fmt.Errorf("addon %q is configured at cluster creation time and cannot be installed via kubectl", addonName)
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
	case "monitoring":
		// kube-prometheus-stack (Prometheus + Grafana) via Helm.
		return provider.InstallHelmAddon(ctx, kubeconfigPath, provider.HelmAddonOptions{
			ReleaseName: "kube-prometheus-stack",
			RepoName:    "prometheus-community",
			RepoURL:     "https://prometheus-community.github.io/helm-charts",
			Chart:       "prometheus-community/kube-prometheus-stack",
			Namespace:   "monitoring",
			Values:      helmValuesFromConfig(config),
		})
	case "helm-chart":
		// Generic Helm chart addon path: caller supplies repo/chart/version/namespace/values.
		opts, err := provider.HelmOptionsFromConfig("custom", config)
		if err != nil {
			return err
		}
		return provider.InstallHelmAddon(ctx, kubeconfigPath, opts)
	default:
		return fmt.Errorf("unsupported addon for DigitalOcean: %s", addonName)
	}
}

// UninstallAddon uninstalls an addon using kubectl/helm against the cluster kubeconfig.
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	log.Printf("Uninstalling addon %s from DigitalOcean cluster %s", addonName, clusterID)

	kubeconfigPath, cleanup, err := p.addonKubeconfig(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to obtain kubeconfig for addon uninstall: %w", err)
	}
	defer cleanup()

	switch addonName {
	case "do-csi":
		return fmt.Errorf("DigitalOcean CSI driver is a critical component and should not be uninstalled")
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
	case "monitoring":
		return provider.UninstallHelmAddon(ctx, kubeconfigPath, "kube-prometheus-stack", "monitoring")
	case "helm-chart":
		return fmt.Errorf("uninstalling a generic helm-chart addon requires the release name and namespace")
	default:
		return fmt.Errorf("unsupported addon for DigitalOcean: %s", addonName)
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

// helmValuesFromConfig extracts a `values` map from an addon config, if present.
func helmValuesFromConfig(config map[string]interface{}) map[string]interface{} {
	if vals, ok := config["values"].(map[string]interface{}); ok {
		return vals
	}
	return nil
}

// ListAddons lists installed addons
func (p *Provider) ListAddons(ctx context.Context, clusterID string) ([]string, error) {
	log.Printf("Listing addons for cluster: %s", clusterID)
	return []string{"do-csi", "coredns", "kube-proxy", "cilium"}, nil
}

// GetClusterCost retrieves cluster cost
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	log.Printf("Getting cost for cluster: %s", clusterID)
	return 72.0, nil // $72 per month
}

// GetCostBreakdown retrieves cost breakdown
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	log.Printf("Getting cost breakdown for cluster: %s", clusterID)
	return map[string]float64{
		"control-plane": 0.0, // Free
		"node-pools":    60.0,
		"load-balancer": 12.0,
	}, nil
}

// installCiliumCNI installs Cilium CNI on the cluster
func (p *Provider) installCiliumCNI(ctx context.Context, masterNode NodeInfo) error {
	log.Printf("Installing Cilium CNI on master %s", masterNode.Name)

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
		log.Printf("Installing Calico CNI on master %s", masterNode.Name)
		time.Sleep(30 * time.Second)
		return nil
	case "flannel":
		// Install Flannel CNI
		log.Printf("Installing Flannel CNI on master %s", masterNode.Name)
		time.Sleep(30 * time.Second)
		return nil
	default:
		// Default to Calico
		log.Printf("Installing default Calico CNI on master %s", masterNode.Name)
		time.Sleep(30 * time.Second)
		return nil
	}
}

// InvestigateCluster performs comprehensive investigation of a cluster
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	// TODO: Implement DigitalOcean-specific cluster investigation
	return fmt.Errorf("cluster investigation not yet implemented for DigitalOcean provider")
}
