package azure

import (
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// Cloud integration for a self-managed (kubeadm on Azure VMs) cluster:
// cloud-provider-azure (CCM + cloud-node-manager), the Azure Disk CSI driver,
// a default StandardSSD StorageClass and the CSI-startup-taint toleration.
// Pinned like the DigitalOcean CCM/CSI; bump deliberately with a bring-up.
const (
	azureCCMChartVersion = "1.32.0"  // chart cloud-provider-azure
	azureCSIChartVersion = "v1.32.0" // chart azuredisk-csi-driver
)

// azureCloudConfig is the `azure.json` both cloud-provider-azure and the disk
// driver read from Secret kube-system/azure-cloud-provider.
func (p *Provider) azureCloudConfig(resourceGroup, vnet, subnet, nsg string) (string, error) {
	cfg := map[string]any{
		"cloud":                       "AzurePublicCloud",
		"tenantId":                    p.config.TenantID,
		"subscriptionId":              p.config.SubscriptionID,
		"aadClientId":                 p.config.ClientID,
		"aadClientSecret":             p.config.ClientSecret,
		"resourceGroup":               resourceGroup,
		"location":                    p.config.Location,
		"vmType":                      "standard",
		"vnetName":                    vnet,
		"subnetName":                  subnet,
		"securityGroupName":           nsg,
		"loadBalancerSku":             "standard",
		"useInstanceMetadata":         true,
		"useManagedIdentityExtension": p.config.UseManagedIdentity,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (p *Provider) cloudIntegrationSteps(clusterName, resourceGroup, vnet, subnet, nsg string) ([]provider.IntegrationStep, error) {
	cloudConfig, err := p.azureCloudConfig(resourceGroup, vnet, subnet, nsg)
	if err != nil {
		return nil, err
	}
	return []provider.IntegrationStep{
		provider.StepEnsureHelm(),
		provider.StepSecret("azure.json for CCM/CSI", "kube-system", "azure-cloud-provider", map[string]string{"cloud-config": cloudConfig}),
		provider.StepHelmInstall("cloud-provider-azure", "cloud-provider-azure", "cloud-provider-azure",
			"https://raw.githubusercontent.com/kubernetes-sigs/cloud-provider-azure/master/helm/repo", "cloud-provider-azure", azureCCMChartVersion, "kube-system", map[string]string{
				"infra.clusterName": clusterName,
				// Cilium owns pod routing.
				"cloudControllerManager.configureCloudRoutes":  "false",
				"cloudControllerManager.cloudConfigSecretName": "azure-cloud-provider",
				"cloudControllerManager.replicas":              "1",
			}),
		provider.StepHelmInstall("Azure Disk CSI driver", "azuredisk-csi-driver", "azuredisk-csi-driver",
			"https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts", "azuredisk-csi-driver", azureCSIChartVersion, "kube-system", map[string]string{
				"controller.replicas":              "1",
				"controller.cloudConfigSecretName": "azure-cloud-provider",
				"node.cloudConfigSecretName":       "azure-cloud-provider",
			}),
		provider.StepWaitDaemonSet("kube-system", "csi-azuredisk-node"),
		provider.StepTolerateCSIStartupTaint("kube-system", "csi-azuredisk-node"),
		provider.StepDefaultStorageClass("adhar-block", "disk.csi.azure.com", map[string]string{"skuName": "StandardSSD_LRS"}),
	}, nil
}

func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP, clusterName, resourceGroup, vnet, subnet, nsg string) error {
	steps, err := p.cloudIntegrationSteps(clusterName, resourceGroup, vnet, subnet, nsg)
	if err != nil {
		return fmt.Errorf("Azure cloud integration: %w", err)
	}
	if err := provider.ApplyCloudIntegration(signer, azureSSHUser, masterIP, steps); err != nil {
		return fmt.Errorf("Azure cloud integration: %w", err)
	}
	return nil
}
