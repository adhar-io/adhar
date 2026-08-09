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

	// ClusterMode selects how clusters are created:
	//   "compute" (default) — raw droplets + kubeadm, Kubernetes managed by
	//   adhar itself (Cilium replaces kube-proxy during bootstrap, matching
	//   the local Kind flow).
	//   "doks" — DigitalOcean's managed Kubernetes service.
	ClusterMode string `json:"clusterMode,omitempty"`

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

// Provider implements the DigitalOcean provider backed by managed DOKS
// (DigitalOcean Kubernetes) clusters via the godo API.
type Provider struct {
	client   *godo.Client
	config   *Config
	clusters map[string]*types.Cluster
	// token is retained for in-cluster cloud integration (CCM/CSI secret).
	token string
}

// Compile-time check that Provider satisfies the platform provider interface.
var _ provider.Provider = (*Provider)(nil)

// NewProvider creates a new DigitalOcean provider instance for managed DOKS clusters
func NewProvider(config *Config) (*Provider, error) {
	log.Printf("Initializing DigitalOcean provider (managed DOKS)")

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
		client:   client,
		config:   config,
		clusters: make(map[string]*types.Cluster),
		token:    token,
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
	// credentials_file is the generic key ToProviderMap emits; tokenFile is the
	// provider-native spelling. Either populates the token-from-file path.
	if tokenFile, ok := configMap["credentials_file"].(string); ok {
		doConfig.TokenFile = tokenFile
	}
	if tokenFile, ok := configMap["tokenFile"].(string); ok {
		doConfig.TokenFile = tokenFile
	}
	if useEnv, ok := configMap["useEnvironment"].(bool); ok {
		doConfig.UseEnvironment = useEnv
	}
	// Canonical mode switch: raw compute (default) vs the managed service.
	if managed, ok := configMap["useManagedK8s"].(bool); ok && managed {
		doConfig.ClusterMode = "doks"
	}

	// Parse basic configuration
	if region, ok := configMap["region"].(string); ok {
		doConfig.Region = region
	}

	// Parse configuration section
	if configSection, ok := configMap["config"].(map[string]interface{}); ok {
		// Cluster creation mode: "compute" (default) or "doks"
		if mode, ok := configSection["cluster_mode"].(string); ok {
			doConfig.ClusterMode = mode
		}
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

// ValidatePermissions checks the token has the access DOKS cluster management
// actually needs: the Kubernetes API for clusters and the VPC API (clusters
// are attached to a VPC when one is configured).
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	log.Printf("Validating DigitalOcean permissions for DOKS cluster management")

	if _, _, err := p.client.Kubernetes.List(ctx, &godo.ListOptions{Page: 1, PerPage: 1}); err != nil {
		return fmt.Errorf("insufficient Kubernetes (DOKS) permissions: %w", err)
	}

	if _, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{Page: 1, PerPage: 1}); err != nil {
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

	// Default mode: self-managed Kubernetes on raw droplets. The managed DOKS
	// service is an explicit opt-in via cluster_mode: doks.
	switch strings.ToLower(p.config.ClusterMode) {
	case "", "compute", "droplets", "self-managed":
		return p.createComputeCluster(ctx, spec)
	case "doks", "managed":
		// fall through to the DOKS implementation below
	default:
		return nil, fmt.Errorf("unknown cluster_mode %q (expected \"compute\" or \"doks\")", p.config.ClusterMode)
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
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	log.Printf("Listing DigitalOcean clusters (compute + DOKS)")

	clusters, err := p.listComputeClusters(ctx)
	if err != nil {
		log.Printf("Warning: failed to discover compute-mode clusters: %v", err)
	}
	for _, c := range clusters {
		p.clusters[c.ID] = c
	}
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

	log.Printf("Found %d DigitalOcean clusters", len(clusters))
	return clusters, nil
}

// GetCluster returns a specific managed DOKS cluster by ID using the godo API.
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	if p.isComputeCluster(ctx, clusterID) {
		name := computeClusterName(clusterID)
		droplets, err := p.computeClusterDroplets(ctx, name)
		if err != nil {
			return nil, err
		}
		if len(droplets) == 0 {
			return nil, fmt.Errorf("compute cluster %s not found", name)
		}
		cluster := p.computeClusterFromDroplets(name, droplets)
		p.clusters[cluster.ID] = cluster
		return cluster, nil
	}

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
	if p.isComputeCluster(ctx, clusterID) {
		return p.deleteComputeCluster(ctx, clusterID)
	}

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
			return nil
		case <-ticker.C:
		}
	}

	delete(p.clusters, clusterID)
	log.Printf("Successfully deleted DOKS cluster: %s", clusterID)
	return nil
}

// GetKubeconfig fetches the admin kubeconfig for a managed DOKS cluster from
// the DigitalOcean API.
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	if p.isComputeCluster(ctx, clusterID) {
		return p.computeGetKubeconfig(ctx, computeClusterName(clusterID))
	}

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
	if p.isComputeCluster(ctx, clusterID) {
		return p.scaleComputeWorkers(ctx, clusterID, replicas)
	}

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
	if p.isComputeCluster(ctx, clusterID) {
		return p.upgradeComputeCluster(ctx, clusterID, version)
	}

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

// BackupCluster is not supported for managed DOKS clusters: node droplets are
// ephemeral and recreated by the control plane, so droplet snapshots cannot
// restore a cluster. Use the platform's Velero package for workload backups.
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	return nil, fmt.Errorf("cluster backup is not supported for managed DOKS clusters; use Velero for workload backup/restore")
}

// RestoreCluster is not supported for managed DOKS clusters; see BackupCluster.
func (p *Provider) RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error {
	return fmt.Errorf("cluster restore is not supported for managed DOKS clusters; use Velero for workload backup/restore")
}

// GetClusterHealth retrieves cluster health
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	if p.isComputeCluster(ctx, clusterID) {
		name := computeClusterName(clusterID)
		droplets, err := p.computeClusterDroplets(ctx, name)
		if err != nil {
			return nil, err
		}
		status := "healthy"
		components := map[string]types.ComponentHealth{}
		for _, d := range droplets {
			s := "healthy"
			if d.Status != "active" {
				s = "degraded"
				status = "degraded"
			}
			components["droplet/"+d.Name] = types.ComponentHealth{Status: s, Message: d.Status}
		}
		return &types.HealthStatus{Status: status, Components: components}, nil
	}

	log.Printf("Getting health for DOKS cluster: %s", clusterID)

	doCluster, _, err := p.client.Kubernetes.Get(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get DOKS cluster %s: %w", clusterID, err)
	}

	status := "unhealthy"
	if doCluster.Status != nil {
		switch doCluster.Status.State {
		case godo.KubernetesClusterStatusRunning:
			status = "healthy"
		case godo.KubernetesClusterStatusProvisioning, godo.KubernetesClusterStatusUpgrading:
			status = "provisioning"
		case godo.KubernetesClusterStatusDegraded:
			status = "degraded"
		}
	}

	components := map[string]types.ComponentHealth{}
	if doCluster.Status != nil {
		components["control-plane"] = types.ComponentHealth{
			Status:  string(doCluster.Status.State),
			Message: doCluster.Status.Message,
		}
	}
	for _, pool := range doCluster.NodePools {
		ready := 0
		for _, node := range pool.Nodes {
			if node.Status != nil && node.Status.State == "running" {
				ready++
			}
		}
		poolStatus := "healthy"
		if ready < pool.Count {
			poolStatus = "degraded"
		}
		components["node-pool/"+pool.Name] = types.ComponentHealth{
			Status:  poolStatus,
			Message: fmt.Sprintf("%d/%d nodes running", ready, pool.Count),
		}
	}

	return &types.HealthStatus{Status: status, Components: components}, nil
}

// sizesBySlug returns droplet size details (cpu/memory/disk/price) keyed by
// size slug, used to derive real capacity and cost figures for node pools.
func (p *Provider) sizesBySlug(ctx context.Context) (map[string]godo.Size, error) {
	sizes := map[string]godo.Size{}
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		page, resp, err := p.client.Sizes.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list droplet sizes: %w", err)
		}
		for _, s := range page {
			sizes[s.Slug] = s
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		current, err := resp.Links.CurrentPage()
		if err != nil {
			break
		}
		opts.Page = current + 1
	}
	return sizes, nil
}

// GetClusterMetrics reports the cluster's aggregate node capacity derived from
// its node pool droplet sizes. Live usage requires metrics-server inside the
// cluster and is not reported here; usage fields are returned as zero rather
// than fabricated.
func (p *Provider) GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error) {
	log.Printf("Getting capacity metrics for DOKS cluster: %s", clusterID)

	doCluster, _, err := p.client.Kubernetes.Get(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get DOKS cluster %s: %w", clusterID, err)
	}
	sizes, err := p.sizesBySlug(ctx)
	if err != nil {
		return nil, err
	}

	var cpus, memMB, diskGB int
	for _, pool := range doCluster.NodePools {
		size, ok := sizes[pool.Size]
		if !ok {
			continue
		}
		cpus += size.Vcpus * pool.Count
		memMB += size.Memory * pool.Count
		diskGB += size.Disk * pool.Count
	}

	return &types.Metrics{
		CPU: types.MetricValue{
			Usage:    "unknown (requires metrics-server)",
			Capacity: fmt.Sprintf("%d cores", cpus),
		},
		Memory: types.MetricValue{
			Usage:    "unknown (requires metrics-server)",
			Capacity: fmt.Sprintf("%dGi", memMB/1024),
		},
		Disk: types.MetricValue{
			Usage:    "unknown (requires metrics-server)",
			Capacity: fmt.Sprintf("%dGi", diskGB),
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

// doksHAControlPlaneMonthlyUSD is DigitalOcean's flat monthly price for the
// high-availability control plane option (the non-HA control plane is free).
const doksHAControlPlaneMonthlyUSD = 40.0

// GetClusterCost computes the cluster's monthly cost from its node pool
// droplet sizes (live godo pricing) plus the HA control-plane fee when
// enabled. Load balancers and volumes provisioned by workloads are not
// attributed here.
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	breakdown, err := p.GetCostBreakdown(ctx, clusterID)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, v := range breakdown {
		total += v
	}
	return total, nil
}

// GetCostBreakdown retrieves the per-component monthly cost of a DOKS cluster.
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	log.Printf("Computing cost breakdown for DOKS cluster: %s", clusterID)

	doCluster, _, err := p.client.Kubernetes.Get(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get DOKS cluster %s: %w", clusterID, err)
	}
	sizes, err := p.sizesBySlug(ctx)
	if err != nil {
		return nil, err
	}

	breakdown := map[string]float64{}
	var nodesTotal float64
	for _, pool := range doCluster.NodePools {
		if size, ok := sizes[pool.Size]; ok {
			nodesTotal += size.PriceMonthly * float64(pool.Count)
		}
	}
	breakdown["node-pools"] = nodesTotal
	if doCluster.HA {
		breakdown["control-plane"] = doksHAControlPlaneMonthlyUSD
	} else {
		breakdown["control-plane"] = 0.0
	}
	return breakdown, nil
}

// InvestigateCluster prints a diagnostic report of a DOKS cluster: control
// plane state and message, endpoint, version, and per-pool node states.
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	doCluster, _, err := p.client.Kubernetes.Get(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to get DOKS cluster %s: %w", clusterID, err)
	}

	fmt.Printf("DOKS cluster: %s (id=%s)\n", doCluster.Name, doCluster.ID)
	fmt.Printf("  Region:   %s\n", doCluster.RegionSlug)
	fmt.Printf("  Version:  %s\n", doCluster.VersionSlug)
	fmt.Printf("  Endpoint: %s\n", doCluster.Endpoint)
	fmt.Printf("  HA:       %t  AutoUpgrade: %t  SurgeUpgrade: %t\n", doCluster.HA, doCluster.AutoUpgrade, doCluster.SurgeUpgrade)
	if doCluster.VPCUUID != "" {
		fmt.Printf("  VPC:      %s\n", doCluster.VPCUUID)
	}
	if doCluster.Status != nil {
		fmt.Printf("  State:    %s", doCluster.Status.State)
		if doCluster.Status.Message != "" {
			fmt.Printf(" (%s)", doCluster.Status.Message)
		}
		fmt.Println()
	}

	for _, pool := range doCluster.NodePools {
		fmt.Printf("  Node pool %q: size=%s count=%d autoscale=%t", pool.Name, pool.Size, pool.Count, pool.AutoScale)
		if pool.AutoScale {
			fmt.Printf(" (min=%d max=%d)", pool.MinNodes, pool.MaxNodes)
		}
		fmt.Println()
		for _, node := range pool.Nodes {
			state, msg := "unknown", ""
			if node.Status != nil {
				state, msg = node.Status.State, node.Status.Message
			}
			fmt.Printf("    node %s: %s", node.Name, state)
			if msg != "" {
				fmt.Printf(" (%s)", msg)
			}
			fmt.Println()
		}
	}

	return nil
}
