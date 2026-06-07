package aws

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

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
		awsConfig := &Config{}

		// Parse AWS-specific configuration with multiple auth methods
		if region, ok := config["region"].(string); ok {
			awsConfig.Region = region
		}

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

		return NewProvider(awsConfig)
	})
}

// Provider implements the AWS provider for manual Kubernetes clusters
type Provider struct {
	config    *Config
	awsConfig aws.Config
	ec2Client *ec2.Client
}

// Config holds AWS provider configuration
type Config struct {
	Region string `json:"region"`

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
		config:    config,
		awsConfig: cfg,
		ec2Client: ec2.NewFromConfig(cfg),
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

// generateRandomString generates a random string for tokens
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(result)
}

// Helper function to extract cluster name from security group
func extractClusterNameFromSG(sgID string) string {
	// This is a simplified implementation
	// In production, you'd query the security group tags to get the cluster name
	return "adhar-cluster" // Default fallback
}

// Helper functions
func (p *Provider) getClusterServiceRoleArn() string {
	return "arn:aws:iam::123456789012:role/EKSClusterServiceRole"
}

func (p *Provider) getSubnetIds() []string {
	return []string{"subnet-12345", "subnet-67890"}
}

func (p *Provider) getVpcId() string {
	return "vpc-12345"
}

func generateClusterID() string {
	return fmt.Sprintf("cluster-%d", time.Now().Unix())
}

func extractClusterName(clusterID string) string {
	if len(clusterID) > 4 && clusterID[:4] == "aws-" {
		return clusterID[4:]
	}
	return clusterID
}

// getUbuntuAMI dynamically finds the latest Ubuntu 22.04 LTS AMI for the current region
func (p *Provider) getUbuntuAMI(ctx context.Context) (string, error) {
	// Search for the latest Ubuntu 22.04 LTS AMI
	result, err := p.ec2Client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("name"),
				Values: []string{"ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"},
			},
			{
				Name:   aws.String("owner-id"),
				Values: []string{"099720109477"}, // Canonical's AWS account ID
			},
			{
				Name:   aws.String("state"),
				Values: []string{"available"},
			},
			{
				Name:   aws.String("architecture"),
				Values: []string{"x86_64"},
			},
		},
		Owners: []string{"099720109477"}, // Canonical
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe Ubuntu AMIs: %w", err)
	}

	if len(result.Images) == 0 {
		return "", fmt.Errorf("no Ubuntu 22.04 LTS AMI found in region %s", p.config.Region)
	}

	// Find the most recent AMI by creation date
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
		return "", fmt.Errorf("failed to find valid Ubuntu AMI")
	}

	return *latestAMI.ImageId, nil
}

// Helper function to convert EKS status to health status
func getHealthFromEKSStatus(eksStatus string) string {
	switch eksStatus {
	case "ACTIVE":
		return "healthy"
	case "CREATING", "UPDATING":
		return "pending"
	case "DELETING", "FAILED":
		return "unhealthy"
	default:
		return "unknown"
	}
}

// isKubectlAvailable checks if kubectl is available in PATH
func isKubectlAvailable() bool {
	cmd := exec.Command("kubectl", "version", "--client", "--short")
	err := cmd.Run()
	return err == nil
}
