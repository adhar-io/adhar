package azure

import (
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"

	"adhar-io/adhar/globals"
)

// Cloud integration for a self-managed (kubeadm on Azure VMs) cluster:
// cloud-provider-azure (CCM + cloud-node-manager), the Azure Disk CSI driver,
// a default StandardSSD StorageClass and the CSI-startup-taint toleration.
// Pinned like the DigitalOcean CCM/CSI; bump deliberately with a bring-up.
const (
	azureCCMChartVersion = "1.32.0"  // chart cloud-provider-azure
	azureCSIChartVersion = "v1.32.0" // chart azuredisk-csi-driver

	// azureCCMImageTag must be set EXPLICITLY. The chart leaves `imageTag`
	// commented out in its defaults, so an install that does not supply one
	// renders `mcr.microsoft.com/oss/kubernetes/azure-cloud-controller-manager:`
	// with an empty tag, and the API server rejects every object:
	//
	//   DaemonSet.apps "cloud-node-manager" is invalid:
	//     spec.template.spec.containers[0].image: Required value
	//   Deployment.apps "cloud-controller-manager" is invalid: … Required value
	//
	// That failed a live bring-up AFTER kubeadm had succeeded and both VMs were
	// running (2026-09-26), which is the most expensive place to discover it.
	// Kept in step with the chart's own 1.32 line (newest patch there).
	azureCCMImageTag = "v1.32.9"
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
	// Refuse to write a config that cannot authenticate. cloud-provider-azure
	// does not validate this: given an aadClientId with an EMPTY aadClientSecret
	// and no managed identity it silently falls back to DefaultAzureCredential,
	// finds no identity on a plain VM, and dies on a nil-pointer panic deep in
	// azidentity that never mentions credentials. The visible symptom is that no
	// Azure load balancer is ever created — the Gateway Service sits at
	// EXTERNAL-IP <pending> and every platform hostname is unreachable, which
	// looks like a networking or DNS fault. It happens whenever the cluster is
	// created with `az login` credentials and no service principal in the
	// environment, because ClientSecret is then empty.
	if !p.config.UseManagedIdentity && strings.TrimSpace(p.config.ClientSecret) == "" {
		return "", fmt.Errorf("azure.json would have no usable credential: cloud-provider-azure needs a service principal " +
			"(set providers.azure.clientSecret, or AZURE_CLIENT_SECRET with AZURE_CLIENT_ID and AZURE_TENANT_ID) " +
			"or managed identity (useManagedIdentity: true). Azure CLI login is enough to CREATE the cluster but the " +
			"in-cluster controllers cannot use it, and without this the cloud-controller-manager crash-loops and no " +
			"load balancer is created")
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
	return append([]provider.IntegrationStep{
		provider.StepEnsureHelm(),
		// BOTH, because the two consumers read it differently: the CSI driver takes
		// the Secret, while the cloud-provider chart mounts /etc/kubernetes by
		// hostPath and passes --cloud-config=/etc/kubernetes/azure.json, so the
		// controller-manager needs the FILE and died without it.
		provider.StepSecret("azure.json Secret for the CSI driver", "kube-system", "azure-cloud-provider", map[string]string{"cloud-config": cloudConfig}),
		provider.StepWriteNodeFile("azure.json on the control plane for the CCM",
			"/etc/kubernetes/azure.json", cloudConfig, "0600"),
		provider.StepHelmInstall("cloud-provider-azure", "cloud-provider-azure", "cloud-provider-azure",
			"https://raw.githubusercontent.com/kubernetes-sigs/cloud-provider-azure/master/helm/repo", "cloud-provider-azure", azureCCMChartVersion, "kube-system", map[string]string{
				"infra.clusterName": clusterName,
				// Cilium owns pod routing.
				"cloudControllerManager.configureCloudRoutes":  "false",
				"cloudControllerManager.cloudConfigSecretName": "azure-cloud-provider",
				"cloudControllerManager.replicas":              "1",
				// REQUIRED: the chart ships no default tag — see azureCCMImageTag.
				"cloudControllerManager.imageTag": azureCCMImageTag,
				"cloudNodeManager.imageTag":       azureCCMImageTag,
				// No Windows nodes on this platform, and the Windows DaemonSet is
				// the third object that fails for a missing image when enabled.
				"cloudNodeManager.enableWindows": "false",
			}),
		provider.StepHelmInstall("Azure Disk CSI driver", "azuredisk-csi-driver", "azuredisk-csi-driver",
			"https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts", "azuredisk-csi-driver", azureCSIChartVersion, "kube-system", map[string]string{
				"controller.replicas":              "1",
				"controller.cloudConfigSecretName": "azure-cloud-provider",
				"node.cloudConfigSecretName":       "azure-cloud-provider",
			}),
		provider.StepWaitDaemonSet("kube-system", "csi-azuredisk-node"),
		provider.StepTolerateCSIStartupTaint("kube-system", "csi-azuredisk-node"),
		// Block storage stays available but is NOT the default: an
		// E4bds_v5 accepts 8 data disks and an E2bds_v5 only 4, so four
		// workers offer ~32 attach slots against the ~90 claims the stack
		// makes. Node-local storage carries the rest.
		provider.StepStorageClass("adhar-block", "disk.csi.azure.com", map[string]string{"skuName": "StandardSSD_LRS"}, false),
		provider.StepClearDefaultStorageClass("adhar-block"),
	}, provider.StepNodeLocalStorageClass(globals.DefaultStorageClass)...), nil
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
