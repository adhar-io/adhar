package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"

	"adhar-io/adhar/platform/types"
)

func TestAKSToClusterMapsProvisioningStateVersionAndEndpoint(t *testing.T) {
	p := &Provider{config: &Config{Location: "eastus"}}
	states := map[string]types.ClusterStatus{
		"Succeeded": types.ClusterStatusRunning, "Creating": types.ClusterStatusCreating, "Updating": types.ClusterStatusUpdating,
		"Upgrading": types.ClusterStatusUpdating, "Deleting": types.ClusterStatusDeleting, "Failed": types.ClusterStatusError, "Odd": types.ClusterStatusUnknown,
	}
	for state, want := range states {
		c := p.aksToCluster("azure-dev", "dev-rg", &armcontainerservice.ManagedCluster{Properties: &armcontainerservice.ManagedClusterProperties{
			ProvisioningState: to.Ptr(state), CurrentKubernetesVersion: to.Ptr("1.32.4"), Fqdn: to.Ptr("dev.hcp.eastus.azmk8s.io")}})
		if c.Status != want || c.Version != "v1.32.4" || c.Endpoint != "https://dev.hcp.eastus.azmk8s.io:443" || c.Name != "dev" || c.Region != "eastus" {
			t.Errorf("%s: unexpected cluster %+v", state, c)
		}
		if c.Metadata["mode"] != clusterModeAKS || c.Metadata["resourceGroup"] != "dev-rg" {
			t.Errorf("%s: unexpected metadata %+v", state, c.Metadata)
		}
	}
	c := p.aksToCluster("azure-dev", "dev-rg", &armcontainerservice.ManagedCluster{Properties: &armcontainerservice.ManagedClusterProperties{KubernetesVersion: to.Ptr("1.31")}})
	if c.Version != "v1.31" || c.Endpoint != "" {
		t.Errorf("requested version is the fallback and no FQDN means no endpoint: %+v", c)
	}
	if c := p.aksToCluster("azure-dev", "dev-rg", &armcontainerservice.ManagedCluster{}); c.Status != types.ClusterStatusUnknown {
		t.Errorf("nil properties must not panic: %+v", c)
	}
}

func TestAgentPoolProfileDefaultsLabelsTaintsAndAutoscaling(t *testing.T) {
	p := &Provider{config: &Config{VMSize: "Standard_D8s_v5"}}
	sys := p.agentPoolProfile(&types.NodeGroupSpec{Name: "System-Pool"}, true)
	if *sys.Name != "systempool" || *sys.Count != aksDefaultReplicas || *sys.VMSize != "Standard_D8s_v5" || *sys.Mode != armcontainerservice.AgentPoolModeSystem || sys.EnableAutoScaling != nil {
		t.Errorf("unexpected system pool %+v", sys)
	}
	user := p.agentPoolProfile(&types.NodeGroupSpec{Name: "gpu", Replicas: 3, InstanceType: "Standard_NC6", Labels: map[string]string{"gpu": "true"},
		Taints: []types.TaintSpec{{Key: "gpu", Value: "true", Effect: "NoSchedule"}}, AutoScaling: types.AutoScalingSpec{MaxReplicas: 8}}, false)
	if *user.Mode != armcontainerservice.AgentPoolModeUser || *user.Count != 3 || *user.VMSize != "Standard_NC6" || *user.NodeLabels["gpu"] != "true" {
		t.Errorf("unexpected user pool %+v", user)
	}
	if len(user.NodeTaints) != 1 || *user.NodeTaints[0] != "gpu=true:NoSchedule" {
		t.Errorf("unexpected taints %v", user.NodeTaints)
	}
	if !*user.EnableAutoScaling || *user.MinCount != 1 || *user.MaxCount != 8 {
		t.Errorf("autoscaling with no min must start at 1: %+v", user)
	}
	if bare := (&Provider{config: &Config{}}).agentPoolProfile(&types.NodeGroupSpec{Name: "w"}, false); *bare.VMSize != aksDefaultVMSize {
		t.Errorf("no configured VM size falls back to %s, got %s", aksDefaultVMSize, *bare.VMSize)
	}
}

func TestAKSPoolToNodeGroupMapsState(t *testing.T) {
	states := map[string]types.NodeGroupStatus{"Succeeded": types.NodeGroupStatusReady, "Creating": types.NodeGroupStatusCreating, "Scaling": types.NodeGroupStatusScaling, "Deleting": types.NodeGroupStatusDeleting, "Failed": types.NodeGroupStatusError}
	for state, want := range states {
		g := aksPoolToNodeGroup("workers", &armcontainerservice.ManagedClusterAgentPoolProfileProperties{Count: to.Ptr(int32(3)), VMSize: to.Ptr("Standard_D4s_v5"), ProvisioningState: to.Ptr(state), NodeLabels: map[string]*string{"a": to.Ptr("b")}})
		if g.Status != want || g.Replicas != 3 || g.InstanceType != "Standard_D4s_v5" || g.Labels["a"] != "b" {
			t.Errorf("%s: unexpected node group %+v", state, g)
		}
	}
	if g := aksPoolToNodeGroup("bare", nil); g.Name != "bare" || g.Status != types.NodeGroupStatusReady {
		t.Errorf("nil properties must not panic: %+v", g)
	}
}

func TestResourceGroupForPrecedence(t *testing.T) {
	p := &Provider{config: &Config{}, resourceTrackers: map[string]*ResourceTracker{"azure-tracked": {ResourceGroup: "from-tracker"}}, clusters: map[string]*types.Cluster{}}
	if got := p.resourceGroupFor("azure-tracked"); got != "from-tracker" {
		t.Errorf("tracker wins: %s", got)
	}
	if got := p.resourceGroupFor("azure-dev"); got != "dev-rg" {
		t.Errorf("default is <name>-rg: %s", got)
	}
	p.config.ResourceGroup = "shared"
	if got := p.resourceGroupFor("azure-dev"); got != "shared" {
		t.Errorf("configured resource group beats the default: %s", got)
	}
}

func TestIsManagedClusterReadsTheRecordedMode(t *testing.T) {
	p := &Provider{config: &Config{}, clusters: map[string]*types.Cluster{
		"azure-aks": {Metadata: map[string]interface{}{"mode": clusterModeAKS}},
		"azure-vm":  {Metadata: map[string]interface{}{"resourceGroup": "x"}},
	}, resourceTrackers: map[string]*ResourceTracker{}}
	if !p.isManagedCluster(context.Background(), "azure-aks") {
		t.Error("a cluster recorded as AKS is managed")
	}
	if p.isManagedCluster(context.Background(), "azure-vm") {
		t.Error("a kubeadm cluster in state is not managed")
	}
	if p.isManagedCluster(context.Background(), "azure-unknown") {
		t.Error("with no client and no state nothing is managed")
	}
}
