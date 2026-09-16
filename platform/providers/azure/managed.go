package azure

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// Managed mode: Azure Kubernetes Service. The default (`clusterMode:
// compute`) provisions Kubernetes with kubeadm on VMs; `useManagedK8s: true`
// (or `clusterMode: aks`) hands the control plane to AKS. The cluster is
// created with `networkPlugin: none` (BYO CNI) so the platform bootstrap
// installs Cilium exactly as it does on every other provider.

const (
	clusterModeAKS     = "aks"
	aksDefaultVMSize   = "Standard_D4s_v5"
	aksDefaultReplicas = 2
	aksSystemPoolName  = "system"
)

var aksPoolNameRe = regexp.MustCompile(`[^a-z0-9]`)

// aksPoolName maps a node-group name onto AKS's agent-pool constraints
// (lowercase alphanumerics, ≤ 12 characters, starts with a letter).
func aksPoolName(name string) string {
	n := aksPoolNameRe.ReplaceAllString(strings.ToLower(name), "")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "np" + n
	}
	if len(n) > 12 {
		n = n[:12]
	}
	return n
}

func (p *Provider) isManagedMode() bool {
	return provider.ClusterModeIsManaged(p.config.ClusterMode, clusterModeAKS)
}

// resourceGroupFor returns the resource group a cluster lives in.
func (p *Provider) resourceGroupFor(clusterID string) string {
	if tracker, ok := p.resourceTrackers[clusterID]; ok && tracker.ResourceGroup != "" {
		return tracker.ResourceGroup
	}
	if p.config.ResourceGroup != "" {
		return p.config.ResourceGroup
	}
	return extractClusterName(clusterID) + "-rg"
}

// isManagedCluster reports whether the cluster is an AKS cluster: recorded as
// such in state, or (for a cluster created elsewhere) found through the API.
func (p *Provider) isManagedCluster(ctx context.Context, clusterID string) bool {
	if c, ok := p.clusters[clusterID]; ok && c.Metadata != nil {
		if mode, _ := c.Metadata["mode"].(string); mode == clusterModeAKS {
			return true
		}
		return false
	}
	if p.managedClustersClient == nil {
		return false
	}
	_, err := p.managedClustersClient.Get(ctx, p.resourceGroupFor(clusterID), extractClusterName(clusterID), nil)
	return err == nil
}

// agentPoolProfile maps a node-group spec onto an AKS agent pool.
func (p *Provider) agentPoolProfile(ng *types.NodeGroupSpec, system bool) *armcontainerservice.ManagedClusterAgentPoolProfile {
	replicas := ng.Replicas
	if replicas <= 0 {
		replicas = aksDefaultReplicas
	}
	vmSize := ng.InstanceType
	if vmSize == "" {
		vmSize = p.config.VMSize
	}
	if vmSize == "" {
		vmSize = aksDefaultVMSize
	}
	mode := armcontainerservice.AgentPoolModeUser
	if system {
		mode = armcontainerservice.AgentPoolModeSystem
	}
	profile := &armcontainerservice.ManagedClusterAgentPoolProfile{
		Name:   to.Ptr(aksPoolName(ng.Name)),
		Count:  to.Ptr(int32(replicas)),
		VMSize: to.Ptr(vmSize),
		Mode:   to.Ptr(mode),
		OSType: to.Ptr(armcontainerservice.OSTypeLinux),
		Type:   to.Ptr(armcontainerservice.AgentPoolTypeVirtualMachineScaleSets),
	}
	if len(ng.Labels) > 0 {
		profile.NodeLabels = map[string]*string{}
		for k, v := range ng.Labels {
			profile.NodeLabels[k] = to.Ptr(v)
		}
	}
	for _, t := range ng.Taints {
		profile.NodeTaints = append(profile.NodeTaints, to.Ptr(fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect)))
	}
	if ng.AutoScaling.MaxReplicas > 0 {
		minCount := ng.AutoScaling.MinReplicas
		if minCount <= 0 {
			minCount = 1
		}
		profile.EnableAutoScaling = to.Ptr(true)
		profile.MinCount = to.Ptr(int32(minCount))
		profile.MaxCount = to.Ptr(int32(ng.AutoScaling.MaxReplicas))
	}
	return profile
}

// createManagedCluster provisions the resource group and the AKS cluster with
// one agent pool per spec node group (the first one is the System pool).
func (p *Provider) createManagedCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("cluster name is required")
	}
	name := spec.Name
	clusterID := fmt.Sprintf("azure-%s", name)
	resourceGroup := p.config.ResourceGroup
	if resourceGroup == "" {
		resourceGroup = name + "-rg"
	}
	log.Printf("Creating managed AKS cluster %s in %s (%s)", name, p.config.Location, resourceGroup)
	if err := p.createResourceGroup(ctx, resourceGroup); err != nil {
		return nil, fmt.Errorf("failed to create resource group: %w", err)
	}

	groups := spec.NodeGroups
	if len(groups) == 0 {
		groups = []types.NodeGroupSpec{{Name: aksSystemPoolName, Replicas: aksDefaultReplicas}}
	}
	var pools []*armcontainerservice.ManagedClusterAgentPoolProfile
	for i := range groups {
		pools = append(pools, p.agentPoolProfile(&groups[i], i == 0))
	}

	mc := armcontainerservice.ManagedCluster{
		Location: to.Ptr(p.config.Location),
		Tags:     map[string]*string{"managedBy": to.Ptr("adhar-platform"), "cluster": to.Ptr(name)},
		Identity: &armcontainerservice.ManagedClusterIdentity{Type: to.Ptr(armcontainerservice.ResourceIdentityTypeSystemAssigned)},
		Properties: &armcontainerservice.ManagedClusterProperties{
			DNSPrefix:         to.Ptr(aksPoolName(name)),
			EnableRBAC:        to.Ptr(true),
			AgentPoolProfiles: pools,
			NetworkProfile: &armcontainerservice.NetworkProfile{
				// BYO CNI: the platform bootstrap installs Cilium (kube-proxy
				// replacement) on AKS the same way it does everywhere else.
				NetworkPlugin:   to.Ptr(armcontainerservice.NetworkPluginNone),
				LoadBalancerSKU: to.Ptr(armcontainerservice.LoadBalancerSKUStandard),
			},
		},
	}
	if v := provider.ManagedVersion(spec.Version); v != "" {
		mc.Properties.KubernetesVersion = to.Ptr(v)
	}
	for k, v := range spec.Tags {
		mc.Tags[k] = to.Ptr(v)
	}

	poller, err := p.managedClustersClient.BeginCreateOrUpdate(ctx, resourceGroup, name, mc, nil)
	if err != nil {
		return nil, fmt.Errorf("creating AKS cluster %s: %w", name, err)
	}
	log.Printf("Waiting for AKS cluster %s to be provisioned...", name)
	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("AKS cluster %s provisioning: %w", name, err)
	}

	cluster := p.aksToCluster(clusterID, resourceGroup, &resp.ManagedCluster)
	cluster.Tags = spec.Tags
	p.clusters[clusterID] = cluster
	p.resourceTrackers[clusterID] = &ResourceTracker{
		SubscriptionID: p.config.SubscriptionID,
		ResourceGroup:  resourceGroup,
		Location:       p.config.Location,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := p.saveState(); err != nil {
		log.Printf("Warning: failed to save state: %v", err)
	}
	log.Printf("AKS cluster %s is ready at %s", name, cluster.Endpoint)
	return cluster, nil
}

func (p *Provider) aksToCluster(clusterID, resourceGroup string, mc *armcontainerservice.ManagedCluster) *types.Cluster {
	status := types.ClusterStatusUnknown
	version, endpoint := "", ""
	if mc.Properties != nil {
		switch strings.ToLower(deref(mc.Properties.ProvisioningState)) {
		case "succeeded":
			status = types.ClusterStatusRunning
		case "creating":
			status = types.ClusterStatusCreating
		case "updating", "upgrading", "scaling":
			status = types.ClusterStatusUpdating
		case "deleting":
			status = types.ClusterStatusDeleting
		case "failed", "canceled":
			status = types.ClusterStatusError
		}
		version = deref(mc.Properties.CurrentKubernetesVersion)
		if version == "" {
			version = deref(mc.Properties.KubernetesVersion)
		}
		if fqdn := deref(mc.Properties.Fqdn); fqdn != "" {
			endpoint = "https://" + fqdn + ":443"
		}
	}
	if version != "" && !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return &types.Cluster{
		ID:        clusterID,
		Name:      extractClusterName(clusterID),
		Provider:  "azure",
		Region:    p.config.Location,
		Version:   version,
		Status:    status,
		Endpoint:  endpoint,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"mode":          clusterModeAKS,
			"resourceGroup": resourceGroup,
			"location":      p.config.Location,
		},
	}
}

// managedKubeconfig returns the cluster-admin kubeconfig AKS issues.
func (p *Provider) managedKubeconfig(ctx context.Context, clusterID string) (string, error) {
	creds, err := p.managedClustersClient.ListClusterAdminCredentials(ctx, p.resourceGroupFor(clusterID), extractClusterName(clusterID), nil)
	if err != nil {
		return "", fmt.Errorf("fetching AKS credentials: %w", err)
	}
	for _, kc := range creds.Kubeconfigs {
		if len(kc.Value) > 0 {
			// The SDK already base64-decodes the value; guard against a raw
			// base64 payload anyway.
			if !strings.HasPrefix(strings.TrimSpace(string(kc.Value)), "apiVersion") {
				if decoded, err := base64.StdEncoding.DecodeString(string(kc.Value)); err == nil {
					return string(decoded), nil
				}
			}
			return string(kc.Value), nil
		}
	}
	return "", fmt.Errorf("AKS returned no kubeconfig for %s", clusterID)
}

// deleteManagedControlPlane deletes the AKS cluster; the caller then removes
// the resource group through the shared path.
func (p *Provider) deleteManagedControlPlane(ctx context.Context, clusterID string) error {
	name := extractClusterName(clusterID)
	log.Printf("Deleting AKS cluster %s", name)
	poller, err := p.managedClustersClient.BeginDelete(ctx, p.resourceGroupFor(clusterID), name, nil)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFound") || strings.Contains(err.Error(), "404") {
			return nil
		}
		return fmt.Errorf("deleting AKS cluster %s: %w", name, err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("deleting AKS cluster %s: %w", name, err)
	}
	return nil
}

func aksPoolToNodeGroup(name string, props *armcontainerservice.ManagedClusterAgentPoolProfileProperties) *types.NodeGroup {
	g := &types.NodeGroup{Name: name, Status: types.NodeGroupStatusReady, UpdatedAt: time.Now(), CreatedAt: time.Now()}
	if props == nil {
		return g
	}
	g.Replicas = int(derefInt32(props.Count))
	g.InstanceType = deref(props.VMSize)
	switch strings.ToLower(deref(props.ProvisioningState)) {
	case "creating":
		g.Status = types.NodeGroupStatusCreating
	case "scaling", "updating", "upgrading":
		g.Status = types.NodeGroupStatusScaling
	case "deleting":
		g.Status = types.NodeGroupStatusDeleting
	case "failed":
		g.Status = types.NodeGroupStatusError
	}
	if len(props.NodeLabels) > 0 {
		g.Labels = map[string]string{}
		for k, v := range props.NodeLabels {
			g.Labels[k] = deref(v)
		}
	}
	return g
}

func (p *Provider) managedAddNodeGroup(ctx context.Context, clusterID string, ng *types.NodeGroupSpec) (*types.NodeGroup, error) {
	profile := p.agentPoolProfile(ng, false)
	pool := armcontainerservice.AgentPool{Properties: &armcontainerservice.ManagedClusterAgentPoolProfileProperties{
		Count: profile.Count, VMSize: profile.VMSize, Mode: profile.Mode, OSType: profile.OSType, Type: profile.Type,
		NodeLabels: profile.NodeLabels, NodeTaints: profile.NodeTaints,
		EnableAutoScaling: profile.EnableAutoScaling, MinCount: profile.MinCount, MaxCount: profile.MaxCount,
	}}
	poller, err := p.agentPoolsClient.BeginCreateOrUpdate(ctx, p.resourceGroupFor(clusterID), extractClusterName(clusterID), *profile.Name, pool, nil)
	if err != nil {
		return nil, fmt.Errorf("creating agent pool %s: %w", ng.Name, err)
	}
	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("creating agent pool %s: %w", ng.Name, err)
	}
	return aksPoolToNodeGroup(*profile.Name, resp.Properties), nil
}

func (p *Provider) managedRemoveNodeGroup(ctx context.Context, clusterID, name string) error {
	poller, err := p.agentPoolsClient.BeginDelete(ctx, p.resourceGroupFor(clusterID), extractClusterName(clusterID), aksPoolName(name), nil)
	if err != nil {
		return fmt.Errorf("deleting agent pool %s: %w", name, err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func (p *Provider) managedScaleNodeGroup(ctx context.Context, clusterID, name string, replicas int) error {
	rg, cluster, pool := p.resourceGroupFor(clusterID), extractClusterName(clusterID), aksPoolName(name)
	current, err := p.agentPoolsClient.Get(ctx, rg, cluster, pool, nil)
	if err != nil {
		return fmt.Errorf("agent pool %s: %w", name, err)
	}
	props := current.Properties
	if props == nil {
		props = &armcontainerservice.ManagedClusterAgentPoolProfileProperties{}
	}
	props.Count = to.Ptr(int32(replicas))
	if derefBool(props.EnableAutoScaling) {
		if derefInt32(props.MinCount) > int32(replicas) {
			props.MinCount = to.Ptr(int32(replicas))
		}
		if derefInt32(props.MaxCount) < int32(replicas) {
			props.MaxCount = to.Ptr(int32(replicas))
		}
	}
	poller, err := p.agentPoolsClient.BeginCreateOrUpdate(ctx, rg, cluster, pool, armcontainerservice.AgentPool{Properties: props}, nil)
	if err != nil {
		return fmt.Errorf("scaling agent pool %s: %w", name, err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func (p *Provider) managedGetNodeGroup(ctx context.Context, clusterID, name string) (*types.NodeGroup, error) {
	resp, err := p.agentPoolsClient.Get(ctx, p.resourceGroupFor(clusterID), extractClusterName(clusterID), aksPoolName(name), nil)
	if err != nil {
		return nil, fmt.Errorf("agent pool %s: %w", name, err)
	}
	return aksPoolToNodeGroup(deref(resp.Name), resp.Properties), nil
}

func (p *Provider) managedListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	pager := p.agentPoolsClient.NewListPager(p.resourceGroupFor(clusterID), extractClusterName(clusterID), nil)
	var groups []*types.NodeGroup
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, pool := range page.Value {
			groups = append(groups, aksPoolToNodeGroup(deref(pool.Name), pool.Properties))
		}
	}
	return groups, nil
}

// managedUpgrade moves the control plane, then every agent pool, to the
// requested version.
func (p *Provider) managedUpgrade(ctx context.Context, clusterID, version string) error {
	target := provider.ManagedVersion(version)
	if target == "" {
		return fmt.Errorf("a target version is required")
	}
	rg, name := p.resourceGroupFor(clusterID), extractClusterName(clusterID)
	current, err := p.managedClustersClient.Get(ctx, rg, name, nil)
	if err != nil {
		return err
	}
	mc := current.ManagedCluster
	if mc.Properties == nil {
		mc.Properties = &armcontainerservice.ManagedClusterProperties{}
	}
	mc.Properties.KubernetesVersion = to.Ptr(target)
	poller, err := p.managedClustersClient.BeginCreateOrUpdate(ctx, rg, name, mc, nil)
	if err != nil {
		return fmt.Errorf("upgrading AKS control plane: %w", err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("upgrading AKS control plane: %w", err)
	}
	pager := p.agentPoolsClient.NewListPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, pool := range page.Value {
			if pool.Properties == nil {
				continue
			}
			pool.Properties.OrchestratorVersion = to.Ptr(target)
			pp, err := p.agentPoolsClient.BeginCreateOrUpdate(ctx, rg, name, deref(pool.Name), *pool, nil)
			if err != nil {
				return fmt.Errorf("upgrading agent pool %s: %w", deref(pool.Name), err)
			}
			if _, err := pp.PollUntilDone(ctx, nil); err != nil {
				return fmt.Errorf("upgrading agent pool %s: %w", deref(pool.Name), err)
			}
		}
	}
	if c, ok := p.clusters[clusterID]; ok {
		c.Version = "v" + target
		c.UpdatedAt = time.Now()
		_ = p.saveState()
	}
	return nil
}

// managedHealth reports the AKS provisioning and power state.
func (p *Provider) managedHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	resp, err := p.managedClustersClient.Get(ctx, p.resourceGroupFor(clusterID), extractClusterName(clusterID), nil)
	if err != nil {
		return &types.HealthStatus{Status: "unhealthy", Components: map[string]types.ComponentHealth{"cluster": {Status: "unhealthy", Message: err.Error()}}, LastCheck: time.Now()}, nil
	}
	state := ""
	if resp.Properties != nil {
		state = deref(resp.Properties.ProvisioningState)
	}
	status := "healthy"
	if !strings.EqualFold(state, "Succeeded") {
		status = "degraded"
	}
	return &types.HealthStatus{Status: status, Components: map[string]types.ComponentHealth{"control-plane": {Status: status, Message: "AKS " + state}}, LastCheck: time.Now()}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt32(i *int32) int32 {
	if i == nil {
		return 0
	}
	return *i
}

func derefBool(b *bool) bool {
	return b != nil && *b
}
