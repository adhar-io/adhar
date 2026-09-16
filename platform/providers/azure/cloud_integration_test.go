package azure

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAzureCloudConfigCarriesIdentityAndNetwork(t *testing.T) {
	p := &Provider{config: &Config{TenantID: "t", SubscriptionID: "s", ClientID: "c", ClientSecret: "x", Location: "eastus"}}
	raw, err := p.azureCloudConfig("dev-rg", "dev-vnet", "dev-subnet", "dev-nsg")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"tenantId": "t", "subscriptionId": "s", "aadClientId": "c", "aadClientSecret": "x", "resourceGroup": "dev-rg", "vnetName": "dev-vnet", "subnetName": "dev-subnet", "securityGroupName": "dev-nsg", "location": "eastus", "loadBalancerSku": "standard", "useInstanceMetadata": true} {
		if cfg[k] != want {
			t.Errorf("azure.json %s = %v, want %v", k, cfg[k], want)
		}
	}
}

func TestCloudIntegrationStepsInstallCCMAndDiskCSI(t *testing.T) {
	p := &Provider{config: &Config{TenantID: "t", SubscriptionID: "s", ClientID: "c", ClientSecret: "x", Location: "eastus"}}
	steps, err := p.cloudIntegrationSteps("dev", "dev-rg", "vnet", "subnet", "nsg")
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, s := range steps {
		joined += s.Cmd + "\n"
	}
	for _, want := range []string{
		"create secret generic azure-cloud-provider", "--from-literal='cloud-config={",
		"helm repo add 'cloud-provider-azure' 'https://raw.githubusercontent.com/kubernetes-sigs/cloud-provider-azure/master/helm/repo'",
		"--version '" + azureCCMChartVersion + "'", "cloudControllerManager.configureCloudRoutes=false",
		"helm repo add 'azuredisk-csi-driver' 'https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts'",
		"--version '" + azureCSIChartVersion + "'",
		"get daemonset csi-azuredisk-node", "node.adhar.io/csi-not-ready",
		"provisioner: disk.csi.azure.com", `skuName: "StandardSSD_LRS"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Azure integration lacks %q", want)
		}
	}
}

func TestAKSPoolNameFitsAgentPoolRules(t *testing.T) {
	cases := map[string]string{"workers": "workers", "gpu-workers-large": "gpuworkersla", "1st": "np1st", "": "np"}
	for in, want := range cases {
		if got := aksPoolName(in); got != want {
			t.Errorf("aksPoolName(%q) = %q, want %q", in, got, want)
		}
	}
	if (&Provider{config: &Config{}}).isManagedMode() || !(&Provider{config: &Config{ClusterMode: "aks"}}).isManagedMode() {
		t.Error("clusterMode must default to kubeadm and opt into AKS with \"aks\"")
	}
}
