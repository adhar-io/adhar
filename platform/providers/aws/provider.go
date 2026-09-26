package aws

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// ClusterInfrastructure represents the infrastructure state for a cluster
type ClusterInfrastructure struct {
	VPCId               string
	SubnetIds           []string
	SecurityGroups      []string
	LoadBalancerDNS     string
	MasterNodes         []NodeInfo
	WorkerNodes         []NodeInfo
	InternetGatewayId   string
	NATGatewayIds       []string
	RouteTableIds       []string
	NetworkInterfaceIds []string
	ElasticIPs          []string
	KeyPairName         string
	LoadBalancerArns    []string
	TargetGroupArns     []string
}

// ResourceTracker tracks all AWS resources created for a cluster
type ResourceTracker struct {
	ClusterName       string    `json:"clusterName"`
	VPCs              []string  `json:"vpcs"`
	Subnets           []string  `json:"subnets"`
	SecurityGroups    []string  `json:"securityGroups"`
	Instances         []string  `json:"instances"`
	InternetGateways  []string  `json:"internetGateways"`
	NATGateways       []string  `json:"natGateways"`
	RouteTables       []string  `json:"routeTables"`
	NetworkInterfaces []string  `json:"networkInterfaces"`
	ElasticIPs        []string  `json:"elasticIPs"`
	KeyPairs          []string  `json:"keyPairs"`
	LoadBalancers     []string  `json:"loadBalancers"`
	TargetGroups      []string  `json:"targetGroups"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// parseProviderConfig turns the generic provider map into an AWS Config.
//
// Extracted from the registration closure so the mapping can be TESTED. It was
// inline, which meant the only way to check that a key was honoured — an AMI
// override, say — was to construct a live provider. Azure already had this shape.
func parseProviderConfig(config map[string]interface{}) (*Config, error) {
	awsConfig := &Config{}

	// Default: kubeadm on EC2. `useManagedK8s: true` (or clusterMode: eks)
	// opts into Amazon EKS; everything else behaves the same.
	if managed, ok := config["useManagedK8s"].(bool); ok && managed {
		awsConfig.ClusterMode = clusterModeEKS
	}
	if mode, ok := config["clusterMode"].(string); ok && mode != "" {
		awsConfig.ClusterMode = mode
	} else if mode, ok := config["cluster_mode"].(string); ok && mode != "" {
		awsConfig.ClusterMode = mode
	}

	// Parse AWS-specific configuration with multiple auth methods
	if region, ok := config["region"].(string); ok {
		awsConfig.Region = region
	}
	// Set by `adhar down --purge-orphaned-volumes` / `adhar cluster delete
	// --purge-orphaned-volumes`; see sweepOrphanedVolumes.
	if purge, ok := config["purgeOrphanedVolumes"].(bool); ok && purge {
		awsConfig.PurgeOrphanedVolumes = true
	}

	// Node image overrides. Both camelCase and snake_case are accepted
	// because the environment's clusterConfig matches keys ignoring case and
	// separators, and an operator will reasonably write either here too.
	firstString := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := config[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	awsConfig.AMI = firstString("ami", "amiId", "ami_id", "imageId", "image_id")
	awsConfig.ImageNameFilter = firstString("imageNameFilter", "image_name_filter", "imageFilter", "image_filter")
	awsConfig.ImageOwner = firstString("imageOwner", "image_owner")
	awsConfig.ImageArchitecture = firstString("imageArchitecture", "image_architecture", "arch")

	// Authentication Method 1: Access Key + Secret Key
	if accessKey, ok := config["accessKeyId"].(string); ok {
		awsConfig.AccessKeyID = accessKey
	}
	if secretKey, ok := config["secretAccessKey"].(string); ok {
		awsConfig.SecretAccessKey = secretKey
	}
	if sessionToken, ok := config["sessionToken"].(string); ok {
		awsConfig.SessionToken = sessionToken
	}

	// Authentication Method 2: Credentials file
	if credFile, ok := config["credentialsFile"].(string); ok {
		awsConfig.CredentialsFile = credFile
	}
	if profile, ok := config["profile"].(string); ok {
		awsConfig.Profile = profile
	}

	// Authentication Method 3: IAM Role
	if roleArn, ok := config["roleArn"].(string); ok {
		awsConfig.RoleArn = roleArn
	}
	if externalId, ok := config["externalId"].(string); ok {
		awsConfig.ExternalId = externalId
	}

	// Authentication Method 4: Environment variables
	if useEnv, ok := config["useEnvironment"].(bool); ok {
		awsConfig.UseEnvironment = useEnv
	}

	// Authentication Method 5: Instance profile
	if useInstance, ok := config["useInstanceProfile"].(bool); ok {
		awsConfig.UseInstanceProfile = useInstance
	}

	return awsConfig, nil
}

// NodeInfo represents information about a cluster node
type NodeInfo struct {
	InstanceId   string
	PrivateIP    string
	PublicIP     string
	InstanceType string
	Role         string // "master" or "worker"
}

// Register the AWS provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("aws", func(config map[string]interface{}) (provider.Provider, error) {
		awsConfig, err := parseProviderConfig(config)
		if err != nil {
			return nil, err
		}
		return NewProvider(awsConfig)
	})
}

// Provider implements the AWS provider for manual Kubernetes clusters
type Provider struct {
	config    *Config
	awsConfig aws.Config
	ec2Client *ec2.Client
	eksClient *eks.Client
	iamClient *iam.Client

	// Teardown and preflight (teardown.go). The load-balancer clients exist
	// because the in-cluster controllers create load balancers this provider never
	// recorded, in BOTH AWS flavours: the in-tree provider makes a classic ELB,
	// the out-of-tree one an NLB through the v2 API. The quota client is read-only
	// and a credential without access to it does not block a create.
	elbClient   *elb.Client
	elbv2Client *elbv2.Client
	quotaClient *servicequotas.Client
}

// Config holds AWS provider configuration
type Config struct {
	Region string `json:"region"`

	// ClusterMode selects how clusters are created:
	//   "compute" (default) — EC2 instances + kubeadm, Kubernetes managed by
	//   adhar itself (Cilium replaces kube-proxy during bootstrap).
	//   "eks" — Amazon's managed Kubernetes service (`useManagedK8s: true`).
	ClusterMode string `json:"clusterMode,omitempty"`

	// Authentication Methods (multiple options supported)
	// Option 1: Access Key and Secret Key
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`

	// Option 2: Credentials file path
	CredentialsFile string `json:"credentialsFile,omitempty"`
	Profile         string `json:"profile,omitempty"`

	// Option 3: IAM Role ARN (for cross-account access)
	RoleArn    string `json:"roleArn,omitempty"`
	ExternalId string `json:"externalId,omitempty"`

	// Option 4: Environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)
	UseEnvironment bool `json:"useEnvironment,omitempty"`

	// Option 5: EC2 Instance Profile / IRSA (for EKS)
	UseInstanceProfile bool `json:"useInstanceProfile,omitempty"`

	VPCConfig    VPCConfig           `json:"vpcConfig"`
	DomainConfig *types.DomainConfig `json:"domainConfig,omitempty"`

	// Node image. All four are optional; together they replace what used to be a
	// hardcoded Ubuntu 22.04 lookup that no configuration could reach — so a
	// cluster could not be pinned to a known-good image, could not follow the
	// distro the rest of the platform uses (GCP boots 24.04), and could not use a
	// hardened or private base image at all.
	//
	// AMI short-circuits the lookup entirely: set it to pin an exact image, which
	// is what a reproducible or air-gapped build needs. The other three steer the
	// search when AMI is empty.
	AMI               string `json:"ami,omitempty"`
	ImageNameFilter   string `json:"imageNameFilter,omitempty"`
	ImageOwner        string `json:"imageOwner,omitempty"`
	ImageArchitecture string `json:"imageArchitecture,omitempty"`

	// PurgeOrphanedVolumes extends teardown to unattached EBS volumes the CSI
	// driver provisioned that carry no cluster tag at all. Off by default and
	// never inferred: such a volume looks identical whether its cluster is gone or
	// is being rebuilt, so deleting one is the operator's call —
	// `adhar down --purge-orphaned-volumes`.
	PurgeOrphanedVolumes bool `json:"purgeOrphanedVolumes,omitempty"`
}

// VPCConfig holds VPC-specific configuration
type VPCConfig struct {
	CIDR        string   `json:"cidr"`
	SubnetCIDRs []string `json:"subnetCidrs"`
}

// NewProvider creates a new AWS provider instance
func NewProvider(config *Config) (*Provider, error) {
	if config == nil {
		config = &Config{
			Region: "us-east-1",
		}
	}

	ctx := context.Background()

	// Determine authentication method and configure AWS SDK
	var cfg aws.Config
	var err error

	switch {
	// Priority 1: Explicit access key and secret key
	case config.AccessKeyID != "" && config.SecretAccessKey != "":
		credProvider := credentials.NewStaticCredentialsProvider(
			config.AccessKeyID,
			config.SecretAccessKey,
			config.SessionToken,
		)
		cfg, err = awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(config.Region),
			awsconfig.WithCredentialsProvider(credProvider),
		)

	// Priority 2: Credentials file with optional profile
	case config.CredentialsFile != "":
		profile := config.Profile
		if profile == "" {
			profile = "default"
		}
		cfg, err = awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(config.Region),
			awsconfig.WithSharedCredentialsFiles([]string{config.CredentialsFile}),
			awsconfig.WithSharedConfigProfile(profile),
		)

	// Priority 3: IAM Role ARN (assume role)
	case config.RoleArn != "":
		// First load default config to get base credentials
		baseCfg, baseErr := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.Region))
		if baseErr != nil {
			return nil, fmt.Errorf("failed to load base AWS config for role assumption: %w", baseErr)
		}

		// TODO: Implement STS assume role logic
		// For now, fallback to default config
		cfg = baseCfg

	// Priority 4: Instance profile / IRSA
	case config.UseInstanceProfile:
		cfg, err = awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(config.Region),
			awsconfig.WithEC2IMDSRegion(),
		)

	// Priority 5: Environment variables (default behavior)
	case config.UseEnvironment:
		cfg, err = awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.Region))

	// Default: Try environment variables, then instance profile, then error
	default:
		cfg, err = awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.Region))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &Provider{
		config:      config,
		awsConfig:   cfg,
		ec2Client:   ec2.NewFromConfig(cfg),
		eksClient:   eks.NewFromConfig(cfg),
		iamClient:   iam.NewFromConfig(cfg),
		elbClient:   elb.NewFromConfig(cfg),
		elbv2Client: elbv2.NewFromConfig(cfg),
		quotaClient: servicequotas.NewFromConfig(cfg),
	}, nil
}

// Name returns the provider name
func (p *Provider) Name() string {
	return "aws"
}

// Region returns the provider region
func (p *Provider) Region() string {
	return p.config.Region
}

// Authenticate validates AWS credentials
func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	_, err := p.ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return fmt.Errorf("failed to authenticate with AWS: %w", err)
	}
	return nil
}

// ValidatePermissions checks if we have required permissions
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	// Check basic EC2 permissions for manual cluster creation
	_, err := p.ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return fmt.Errorf("insufficient EC2 permissions: %w", err)
	}
	return nil
}

// Helper function to extract cluster name from security group
func extractClusterNameFromSG(sgID string) string {
	// This is a simplified implementation
	// In production, you'd query the security group tags to get the cluster name
	return "adhar-cluster" // Default fallback
}

func extractClusterName(clusterID string) string {
	if len(clusterID) > 4 && clusterID[:4] == "aws-" {
		return clusterID[4:]
	}
	return clusterID
}

// Default node image: the latest Ubuntu LTS, from Canonical, x86_64.
//
// 24.04 (noble), not 22.04 (jammy). This used to be hardcoded to jammy while the
// GCP provider booted `ubuntu-2404-lts-amd64`, so the same platform ran on two
// different distro releases depending on the cloud — different kernels, different
// containerd, and a class of "works on GCP, not on AWS" that points nowhere near
// its cause.
//
// The name is matched with a wildcard across the storage-type segment
// (`hvm-ssd` and `hvm-ssd-gp3` both exist for noble) so the lookup does not break
// the next time Canonical changes that part of the path.
const (
	defaultImageNameFilter   = "ubuntu/images/hvm-ssd*/ubuntu-noble-24.04-amd64-server-*"
	canonicalOwnerID         = "099720109477"
	defaultImageArchitecture = "x86_64"
)

// getUbuntuAMI resolves the node image for this region.
//
// Order: an explicitly pinned AMI wins; otherwise the newest image matching the
// configured (or default) name filter, owner and architecture. Every part is
// configurable so a cluster can be pinned for reproducibility, follow a different
// LTS, or boot a hardened private image — none of which was possible while this
// was a hardcoded jammy lookup.
func (p *Provider) getUbuntuAMI(ctx context.Context) (string, error) {
	// Pinned: skip the search entirely. An air-gapped or reproducible build needs
	// the image to be an input, not a discovery.
	if p.config.AMI != "" {
		log.Printf("Using pinned AMI %s", p.config.AMI)
		return p.config.AMI, nil
	}

	nameFilter := p.config.ImageNameFilter
	if nameFilter == "" {
		nameFilter = defaultImageNameFilter
	}
	owner := p.config.ImageOwner
	if owner == "" {
		owner = canonicalOwnerID
	}
	arch := p.config.ImageArchitecture
	if arch == "" {
		arch = defaultImageArchitecture
	}

	result, err := p.ec2Client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("name"), Values: []string{nameFilter}},
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("architecture"), Values: []string{arch}},
		},
		// Owners is the API's own scoping; an owner-id filter as well was
		// redundant.
		Owners: []string{owner},
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe images (name %q, owner %s, arch %s): %w",
			nameFilter, owner, arch, err)
	}
	if len(result.Images) == 0 {
		// Name the filter: "no AMI found" with nothing else is unactionable, and
		// the usual cause is a pattern that no longer matches Canonical's naming.
		return "", fmt.Errorf("no AMI in region %s matches name %q owned by %s for %s "+
			"— override with `ami`, or `imageNameFilter`/`imageOwner`/`imageArchitecture`",
			p.config.Region, nameFilter, owner, arch)
	}

	// Newest by creation date.
	var latestAMI ec2types.Image
	var latestDate time.Time
	for _, image := range result.Images {
		if image.CreationDate == nil {
			continue
		}
		creationDate, err := time.Parse(time.RFC3339, *image.CreationDate)
		if err != nil {
			continue
		}
		if creationDate.After(latestDate) {
			latestDate = creationDate
			latestAMI = image
		}
	}
	if latestAMI.ImageId == nil {
		return "", fmt.Errorf("no AMI with a parseable creation date matched %q in %s",
			nameFilter, p.config.Region)
	}
	name := ""
	if latestAMI.Name != nil {
		name = *latestAMI.Name
	}
	log.Printf("Selected AMI %s (%s)", *latestAMI.ImageId, name)
	return *latestAMI.ImageId, nil
}

// isKubectlAvailable checks if kubectl is available in PATH
func isKubectlAvailable() bool {
	cmd := exec.Command("kubectl", "version", "--client", "--short")
	err := cmd.Run()
	return err == nil
}
