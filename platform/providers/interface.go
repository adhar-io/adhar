package provider

import (
	"context"

	"adhar-io/adhar/platform/types"
)

// Provider defines the interface that all cloud providers must implement
type Provider interface {
	// Provider Information
	Name() string
	Region() string

	// Authentication
	Authenticate(ctx context.Context, credentials *types.Credentials) error
	ValidatePermissions(ctx context.Context) error

	// Cluster Management
	CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error)
	DeleteCluster(ctx context.Context, clusterID string) error
	UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error
	GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error)
	ListClusters(ctx context.Context) ([]*types.Cluster, error)
	GetKubeconfig(ctx context.Context, clusterID string) (string, error)

	// Node Management
	AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error)
	RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error
	ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error
	GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error)
	ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error)

	// Infrastructure Management
	CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error)
	DeleteVPC(ctx context.Context, vpcID string) error
	GetVPC(ctx context.Context, vpcID string) (*types.VPC, error)

	CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error)
	DeleteLoadBalancer(ctx context.Context, lbID string) error
	GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error)

	CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error)
	DeleteStorage(ctx context.Context, storageID string) error
	GetStorage(ctx context.Context, storageID string) (*types.Storage, error)

	// Lifecycle Management
	UpgradeCluster(ctx context.Context, clusterID string, version string) error
	BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error)
	RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error

	// Monitoring and Health
	GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error)
	GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error)

	// Addon Management
	InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error
	UninstallAddon(ctx context.Context, clusterID string, addonName string) error
	ListAddons(ctx context.Context, clusterID string) ([]string, error)

	// Cost Management
	GetClusterCost(ctx context.Context, clusterID string) (float64, error)
	GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error)

	// Investigation and Debugging
	InvestigateCluster(ctx context.Context, clusterID string) error
}

// ProviderFactory creates provider instances
type ProviderFactory interface {
	CreateProvider(providerType string, config map[string]interface{}) (Provider, error)
	SupportedProviders() []string
}

// ProviderConfig holds provider-specific configuration
type ProviderConfig struct {
	Type        string                 `json:"type"`
	Region      string                 `json:"region"`
	Credentials *types.Credentials     `json:"credentials"`
	Config      map[string]interface{} `json:"config"`
}

// ProviderStatus represents the status of a provider
type ProviderStatus struct {
	Name          string   `json:"name"`
	Region        string   `json:"region"`
	Authenticated bool     `json:"authenticated"`
	Available     bool     `json:"available"`
	LastChecked   string   `json:"lastChecked"`
	Version       string   `json:"version,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
}
