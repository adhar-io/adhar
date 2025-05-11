package providers

import (
	"errors"

	"adhar-io/adhar/platform/logger"
)

// Enhanced DigitalOceanProvider with error handling and configuration validation.
type DigitalOceanProvider struct {
	APIToken string
}

func (d *DigitalOceanProvider) ProvisionCluster(clusterConfig map[string]interface{}) error {
	if d.APIToken == "" {
		return errors.New("missing DigitalOcean API token")
	}
	logger.Logger.Infof("Provisioning cluster on DigitalOcean with config: %v", clusterConfig)
	return nil
}

func (d *DigitalOceanProvider) DeleteCluster(clusterName string) error {
	if clusterName == "" {
		return errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Deleting cluster %s on DigitalOcean", clusterName)
	return nil
}

func (d *DigitalOceanProvider) GetClusterInfo(clusterName string) (map[string]interface{}, error) {
	if clusterName == "" {
		return nil, errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Fetching info for cluster %s on DigitalOcean", clusterName)
	return map[string]interface{}{"name": clusterName, "status": "active"}, nil
}

// AWSProvider is an implementation of the CloudProvider interface for AWS.
type AWSProvider struct{}

func (a *AWSProvider) ProvisionCluster(clusterConfig map[string]interface{}) error {
	logger.Logger.Info("Provisioning cluster on AWS")
	return nil
}

func (a *AWSProvider) DeleteCluster(clusterName string) error {
	if clusterName == "" {
		return errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Deleting cluster %s on AWS", clusterName)
	return nil
}

func (a *AWSProvider) GetClusterInfo(clusterName string) (map[string]interface{}, error) {
	if clusterName == "" {
		return nil, errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Fetching info for cluster %s on AWS", clusterName)
	return map[string]interface{}{"name": clusterName, "status": "active"}, nil
}

// AzureProvider is an implementation of the CloudProvider interface for Azure.
type AzureProvider struct{}

func (a *AzureProvider) ProvisionCluster(clusterConfig map[string]interface{}) error {
	logger.Logger.Info("Provisioning cluster on Azure")
	return nil
}

func (a *AzureProvider) DeleteCluster(clusterName string) error {
	if clusterName == "" {
		return errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Deleting cluster %s on Azure", clusterName)
	return nil
}

func (a *AzureProvider) GetClusterInfo(clusterName string) (map[string]interface{}, error) {
	if clusterName == "" {
		return nil, errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Fetching info for cluster %s on Azure", clusterName)
	return map[string]interface{}{"name": clusterName, "status": "active"}, nil
}

// GCPProvider is an implementation of the CloudProvider interface for GCP.
type GCPProvider struct{}

func (g *GCPProvider) ProvisionCluster(clusterConfig map[string]interface{}) error {
	logger.Logger.Info("Provisioning cluster on GCP")
	return nil
}

func (g *GCPProvider) DeleteCluster(clusterName string) error {
	if clusterName == "" {
		return errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Deleting cluster %s on GCP", clusterName)
	return nil
}

func (g *GCPProvider) GetClusterInfo(clusterName string) (map[string]interface{}, error) {
	if clusterName == "" {
		return nil, errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Fetching info for cluster %s on GCP", clusterName)
	return map[string]interface{}{"name": clusterName, "status": "active"}, nil
}

// CivoProvider is an implementation of the CloudProvider interface for Civo.
type CivoProvider struct{}

func (c *CivoProvider) ProvisionCluster(clusterConfig map[string]interface{}) error {
	logger.Logger.Info("Provisioning cluster on Civo")
	return nil
}

func (c *CivoProvider) DeleteCluster(clusterName string) error {
	if clusterName == "" {
		return errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Deleting cluster %s on Civo", clusterName)
	return nil
}

func (c *CivoProvider) GetClusterInfo(clusterName string) (map[string]interface{}, error) {
	if clusterName == "" {
		return nil, errors.New("cluster name cannot be empty")
	}
	logger.Logger.Infof("Fetching info for cluster %s on Civo", clusterName)
	return map[string]interface{}{"name": clusterName, "status": "active"}, nil
}
