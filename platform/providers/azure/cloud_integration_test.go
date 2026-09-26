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

// cloud-provider-azure does not validate its own config: with an aadClientId but
// an EMPTY aadClientSecret and no managed identity it falls back to
// DefaultAzureCredential, finds nothing on a plain VM, and dies on a nil-pointer
// panic inside azidentity. Nothing in that panic mentions credentials; what an
// operator sees is that no Azure load balancer is ever created, the Gateway
// Service stays at EXTERNAL-IP <pending>, and every platform URL is unreachable.
// It happens to anyone who creates the cluster with `az login` and no service
// principal in the environment. So the step list must refuse to be built.
func TestCloudConfigRefusesToBeWrittenWithNoUsableCredential(t *testing.T) {
	p := &Provider{config: &Config{
		SubscriptionID: "sub", TenantID: "tenant", ClientID: "client",
		ClientSecret: "", UseManagedIdentity: false, Location: "centralindia",
	}}
	_, err := p.azureCloudConfig("adhar-rg", "vnet", "subnet", "nsg")
	if err == nil {
		t.Fatal("an azure.json with no client secret and no managed identity must be refused")
	}
	for _, want := range []string{"clientSecret", "AZURE_CLIENT_SECRET", "useManagedIdentity", "load balancer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must say how to fix it and what breaks; missing %q in: %v", want, err)
		}
	}
}

func TestCloudConfigAcceptsAServicePrincipalOrManagedIdentity(t *testing.T) {
	base := Config{SubscriptionID: "sub", TenantID: "tenant", Location: "centralindia"}

	sp := base
	sp.ClientID, sp.ClientSecret = "client", "secret"
	if _, err := (&Provider{config: &sp}).azureCloudConfig("rg", "v", "s", "n"); err != nil {
		t.Errorf("a service principal must be accepted: %v", err)
	}

	mi := base
	mi.UseManagedIdentity = true
	if _, err := (&Provider{config: &mi}).azureCloudConfig("rg", "v", "s", "n"); err != nil {
		t.Errorf("managed identity must be accepted without a client secret: %v", err)
	}
}
