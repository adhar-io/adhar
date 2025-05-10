package providers

// GitProvider defines the interface for Git provider operations.
type GitProvider interface {
	Configure() error
	GetRepositoryURL(repoName string) (string, error)
	CreateRepository(repoName string) error
}

// CloudProvider defines the interface for cloud provider operations.
type CloudProvider interface {
	ProvisionCluster(clusterConfig map[string]interface{}) error
	DeleteCluster(clusterName string) error
	GetClusterInfo(clusterName string) (map[string]interface{}, error)
}
