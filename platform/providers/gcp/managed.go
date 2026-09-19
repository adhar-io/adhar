package gcp

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/api/container/v1"
	"google.golang.org/api/googleapi"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// Managed mode: Google Kubernetes Engine. The default (`clusterMode:
// compute`) provisions Kubernetes with kubeadm on Compute Engine;
// `useManagedK8s: true` (or `clusterMode: gke`) hands the control plane to
// GKE. The VPC network and subnet come from the same helpers as compute mode
// and are tracked the same way, so `adhar down` cleans both modes up alike.

const (
	clusterModeGKE         = "gke"
	gkePollInterval        = 15 * time.Second
	gkeOperationTimeout    = 40 * time.Minute
	gkeDefaultMachineType  = "e2-standard-4"
	gkeDefaultNodeCount    = 2
	gkeDefaultDiskSizeGB   = 100
	gkeCloudPlatformScope  = "https://www.googleapis.com/auth/cloud-platform"
	gkeDefaultNodePoolName = "workers"
)

func (p *Provider) isManagedMode() bool {
	return provider.ClusterModeIsManaged(p.config.ClusterMode, clusterModeGKE)
}

// gkeParent is the location GKE clusters are created in (zonal, the
// provider's zone).
func (p *Provider) gkeParent() string {
	return fmt.Sprintf("projects/%s/locations/%s", p.config.ProjectID, p.config.Zone)
}

func (p *Provider) gkeClusterName(clusterID string) string {
	return p.gkeParent() + "/clusters/" + extractClusterName(clusterID)
}

func isGKENotFound(err error) bool {
	var ge *googleapi.Error
	if ok := asGoogleAPIError(err, &ge); ok {
		return ge.Code == 404
	}
	return false
}

// isManagedCluster reports whether the cluster is a GKE cluster: recorded as
// such in the tracker, or found through the API.
func (p *Provider) isManagedCluster(ctx context.Context, clusterID string) bool {
	if tracker, ok := p.resourceTrackers[clusterID]; ok {
		return tracker.Mode == clusterModeGKE
	}
	if p.containerService == nil {
		return false
	}
	_, err := p.containerService.Projects.Locations.Clusters.Get(p.gkeClusterName(clusterID)).Context(ctx).Do()
	return err == nil
}

// waitGKEOperation polls a GKE operation until it is DONE.
func (p *Provider) waitGKEOperation(ctx context.Context, op *container.Operation) error {
	if op == nil {
		return nil
	}
	name := fmt.Sprintf("%s/operations/%s", p.gkeParent(), op.Name)
	deadline := time.Now().Add(gkeOperationTimeout)
	for {
		cur, err := p.containerService.Projects.Locations.Operations.Get(name).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("polling GKE operation %s: %w", op.Name, err)
		}
		if cur.Status == "DONE" {
			if cur.Error != nil {
				return fmt.Errorf("GKE operation %s (%s) failed: %s", op.Name, op.OperationType, cur.Error.Message)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for GKE operation %s (%s)", op.Name, op.OperationType)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gkePollInterval):
		}
	}
}

// gkeTaintEffect maps a Kubernetes taint effect onto GKE's spelling.
func gkeTaintEffect(effect string) string {
	switch strings.ToLower(strings.ReplaceAll(effect, "_", "")) {
	case "noexecute":
		return "NO_EXECUTE"
	case "prefernoschedule":
		return "PREFER_NO_SCHEDULE"
	default:
		return "NO_SCHEDULE"
	}
}

// gkeNodePool maps a node-group spec onto a GKE node pool.
func (p *Provider) gkeNodePool(ng *types.NodeGroupSpec) *container.NodePool {
	replicas := ng.Replicas
	if replicas <= 0 {
		replicas = gkeDefaultNodeCount
	}
	machine := ng.InstanceType
	if machine == "" {
		machine = p.config.MachineType
	}
	if machine == "" {
		machine = gkeDefaultMachineType
	}
	disk := int64(p.config.DiskSize)
	if disk <= 0 {
		disk = gkeDefaultDiskSizeGB
	}
	pool := &container.NodePool{
		Name:             ng.Name,
		InitialNodeCount: int64(replicas),
		Config: &container.NodeConfig{
			MachineType: machine,
			DiskSizeGb:  disk,
			DiskType:    p.config.DiskType,
			OauthScopes: []string{gkeCloudPlatformScope},
			Labels:      ng.Labels,
		},
	}
	for _, t := range ng.Taints {
		pool.Config.Taints = append(pool.Config.Taints, &container.NodeTaint{Key: t.Key, Value: t.Value, Effect: gkeTaintEffect(t.Effect)})
	}
	if ng.AutoScaling.MaxReplicas > 0 {
		minCount := ng.AutoScaling.MinReplicas
		if minCount <= 0 {
			minCount = 1
		}
		pool.Autoscaling = &container.NodePoolAutoscaling{Enabled: true, MinNodeCount: int64(minCount), MaxNodeCount: int64(ng.AutoScaling.MaxReplicas)}
	}
	return pool
}

// createManagedCluster provisions the network, subnet and the GKE cluster with
// one node pool per spec node group.
func (p *Provider) createManagedCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("cluster name is required")
	}
	name := spec.Name
	clusterID := fmt.Sprintf("gcp/%s/%s", p.config.ProjectID, name)
	log.Printf("Creating managed GKE cluster %s in %s", name, p.config.Zone)

	networkName := p.config.VPCName
	if networkName == "" {
		networkName = fmt.Sprintf("%s-network", name)
	}
	if err := p.createVPCNetwork(ctx, networkName, name); err != nil {
		return nil, fmt.Errorf("failed to create VPC network: %w", err)
	}
	subnetName := p.config.SubnetName
	if subnetName == "" || subnetName == "default-subnet" {
		subnetName = fmt.Sprintf("%s-subnet", name)
	}
	if err := p.createSubnet(ctx, networkName, subnetName, name); err != nil {
		return nil, fmt.Errorf("failed to create subnet: %w", err)
	}

	groups := spec.NodeGroups
	if len(groups) == 0 {
		groups = []types.NodeGroupSpec{{Name: gkeDefaultNodePoolName, Replicas: gkeDefaultNodeCount}}
	}
	var pools []*container.NodePool
	for i := range groups {
		pools = append(pools, p.gkeNodePool(&groups[i]))
	}
	labels := map[string]string{"managed-by": "adhar", "adhar-cluster": name}
	for k, v := range spec.Tags {
		labels[strings.ToLower(k)] = strings.ToLower(v)
	}
	cluster := &container.Cluster{
		Name:               name,
		Network:            networkName,
		Subnetwork:         subnetName,
		NodePools:          pools,
		ResourceLabels:     labels,
		IpAllocationPolicy: &container.IPAllocationPolicy{UseIpAliases: true},
	}
	if v := provider.ManagedVersion(spec.Version); v != "" {
		cluster.InitialClusterVersion = v
	} else {
		cluster.ReleaseChannel = &container.ReleaseChannel{Channel: "REGULAR"}
	}

	existing, err := p.containerService.Projects.Locations.Clusters.Get(p.gkeClusterName(clusterID)).Context(ctx).Do()
	switch {
	case err == nil:
		log.Printf("GKE cluster %s already exists (%s); reusing", name, existing.Status)
	case isGKENotFound(err):
		op, err := p.containerService.Projects.Locations.Clusters.Create(p.gkeParent(), &container.CreateClusterRequest{Cluster: cluster}).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("creating GKE cluster %s: %w", name, err)
		}
		log.Printf("Waiting for GKE cluster %s to be provisioned...", name)
		if err := p.waitGKEOperation(ctx, op); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("describing GKE cluster %s: %w", name, err)
	}
	got, err := p.containerService.Projects.Locations.Clusters.Get(p.gkeClusterName(clusterID)).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("describing GKE cluster %s: %w", name, err)
	}

	p.resourceTrackers[clusterID] = &ResourceTracker{
		ClusterName: name,
		ProjectID:   p.config.ProjectID,
		Region:      p.config.Region,
		Zone:        p.config.Zone,
		Mode:        clusterModeGKE,
		Networks:    []string{networkName},
		Subnets:     []string{subnetName},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := p.saveState(); err != nil {
		log.Printf("Warning: failed to save state: %v", err)
	}
	result := p.gkeToCluster(clusterID, got)
	result.Tags = spec.Tags
	log.Printf("GKE cluster %s is ready at %s", name, result.Endpoint)
	return result, nil
}

func (p *Provider) gkeToCluster(clusterID string, c *container.Cluster) *types.Cluster {
	status := types.ClusterStatusUnknown
	switch c.Status {
	case "RUNNING":
		status = types.ClusterStatusRunning
	case "PROVISIONING":
		status = types.ClusterStatusCreating
	case "RECONCILING":
		status = types.ClusterStatusUpdating
	case "STOPPING":
		status = types.ClusterStatusDeleting
	case "ERROR", "DEGRADED":
		status = types.ClusterStatusError
	}
	created := time.Now()
	if t, err := time.Parse(time.RFC3339, c.CreateTime); err == nil {
		created = t
	}
	version := c.CurrentMasterVersion
	if version != "" && !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	endpoint := ""
	if c.Endpoint != "" {
		endpoint = "https://" + c.Endpoint
	}
	return &types.Cluster{
		ID:        clusterID,
		Name:      c.Name,
		Provider:  "gcp",
		Region:    p.config.Region,
		Version:   version,
		Status:    status,
		Endpoint:  endpoint,
		CreatedAt: created,
		UpdatedAt: time.Now(),
		Tags:      c.ResourceLabels,
		Metadata: map[string]interface{}{
			"mode":      clusterModeGKE,
			"projectId": p.config.ProjectID,
			"zone":      c.Location,
		},
	}
}

// managedKubeconfig renders the GKE kubeconfig: the CA and endpoint from the
// API, credentials through `gke-gcloud-auth-plugin` (installed with the
// gcloud SDK; it must be on PATH wherever the kubeconfig is used, including on
// the machine running `adhar up`).
func (p *Provider) managedKubeconfig(ctx context.Context, clusterID string) (string, error) {
	c, err := p.containerService.Projects.Locations.Clusters.Get(p.gkeClusterName(clusterID)).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("describing GKE cluster: %w", err)
	}
	if c.Endpoint == "" || c.MasterAuth == nil || c.MasterAuth.ClusterCaCertificate == "" {
		return "", fmt.Errorf("GKE cluster %s has no endpoint yet (status %s)", c.Name, c.Status)
	}
	env := map[string]string{}
	if p.config.ServiceAccountKeyPath != "" {
		if expanded, err := expandHomePath(p.config.ServiceAccountKeyPath); err == nil {
			env["GOOGLE_APPLICATION_CREDENTIALS"] = expanded
		}
	}
	return provider.ExecKubeconfig(c.Name, "https://"+c.Endpoint, c.MasterAuth.ClusterCaCertificate, "gke-gcloud-auth-plugin", nil, env, true), nil
}

// deleteManagedControlPlane deletes the GKE cluster and the firewall rules
// GKE created in the cluster network; the caller then removes the subnet and
// network through the shared tracker cleanup.
func (p *Provider) deleteManagedControlPlane(ctx context.Context, clusterID string) error {
	name := extractClusterName(clusterID)
	op, err := p.containerService.Projects.Locations.Clusters.Delete(p.gkeClusterName(clusterID)).Context(ctx).Do()
	if err != nil {
		if !isGKENotFound(err) {
			return fmt.Errorf("deleting GKE cluster %s: %w", name, err)
		}
	} else if err := p.waitGKEOperation(ctx, op); err != nil {
		return err
	}
	// GKE adds gke-<name>-<hash>-{all,vms,master,...} rules that block the
	// network deletion.
	for _, rule := range p.listFirewallRulesWithPrefix(ctx, "gke-"+name+"-") {
		if err := p.deleteFirewallRule(ctx, rule); err != nil {
			log.Printf("Warning: firewall rule %s: %v", rule, err)
		}
	}
	return nil
}

func gkePoolToNodeGroup(np *container.NodePool) *types.NodeGroup {
	g := &types.NodeGroup{Name: np.Name, Replicas: int(np.InitialNodeCount), Status: types.NodeGroupStatusReady, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if np.Config != nil {
		g.InstanceType = np.Config.MachineType
		g.Labels = np.Config.Labels
	}
	switch np.Status {
	case "PROVISIONING":
		g.Status = types.NodeGroupStatusCreating
	case "RECONCILING":
		g.Status = types.NodeGroupStatusScaling
	case "STOPPING":
		g.Status = types.NodeGroupStatusDeleting
	case "ERROR", "RUNNING_WITH_ERROR":
		g.Status = types.NodeGroupStatusError
	}
	return g
}

func (p *Provider) managedAddNodeGroup(ctx context.Context, clusterID string, ng *types.NodeGroupSpec) (*types.NodeGroup, error) {
	op, err := p.containerService.Projects.Locations.Clusters.NodePools.Create(p.gkeClusterName(clusterID), &container.CreateNodePoolRequest{NodePool: p.gkeNodePool(ng)}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("creating node pool %s: %w", ng.Name, err)
	}
	if err := p.waitGKEOperation(ctx, op); err != nil {
		return nil, err
	}
	return p.managedGetNodeGroup(ctx, clusterID, ng.Name)
}

func (p *Provider) managedRemoveNodeGroup(ctx context.Context, clusterID, name string) error {
	op, err := p.containerService.Projects.Locations.Clusters.NodePools.Delete(p.gkeClusterName(clusterID) + "/nodePools/" + name).Context(ctx).Do()
	if err != nil {
		if isGKENotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting node pool %s: %w", name, err)
	}
	return p.waitGKEOperation(ctx, op)
}

func (p *Provider) managedScaleNodeGroup(ctx context.Context, clusterID, name string, replicas int) error {
	op, err := p.containerService.Projects.Locations.Clusters.NodePools.SetSize(p.gkeClusterName(clusterID)+"/nodePools/"+name, &container.SetNodePoolSizeRequest{NodeCount: int64(replicas)}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("scaling node pool %s: %w", name, err)
	}
	return p.waitGKEOperation(ctx, op)
}

func (p *Provider) managedGetNodeGroup(ctx context.Context, clusterID, name string) (*types.NodeGroup, error) {
	np, err := p.containerService.Projects.Locations.Clusters.NodePools.Get(p.gkeClusterName(clusterID) + "/nodePools/" + name).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("node pool %s: %w", name, err)
	}
	return gkePoolToNodeGroup(np), nil
}

func (p *Provider) managedListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	resp, err := p.containerService.Projects.Locations.Clusters.NodePools.List(p.gkeClusterName(clusterID)).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	var groups []*types.NodeGroup
	for _, np := range resp.NodePools {
		groups = append(groups, gkePoolToNodeGroup(np))
	}
	return groups, nil
}

// managedUpgrade moves the control plane, then every node pool, to the
// requested version.
func (p *Provider) managedUpgrade(ctx context.Context, clusterID, version string) error {
	target := provider.ManagedVersion(version)
	if target == "" {
		return fmt.Errorf("a target version is required")
	}
	name := p.gkeClusterName(clusterID)
	op, err := p.containerService.Projects.Locations.Clusters.Update(name, &container.UpdateClusterRequest{Update: &container.ClusterUpdate{DesiredMasterVersion: target}}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("upgrading GKE control plane: %w", err)
	}
	if err := p.waitGKEOperation(ctx, op); err != nil {
		return err
	}
	pools, err := p.containerService.Projects.Locations.Clusters.NodePools.List(name).Context(ctx).Do()
	if err != nil {
		return err
	}
	for _, np := range pools.NodePools {
		op, err := p.containerService.Projects.Locations.Clusters.Update(name, &container.UpdateClusterRequest{Update: &container.ClusterUpdate{DesiredNodeVersion: target, DesiredNodePoolId: np.Name}}).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("upgrading node pool %s: %w", np.Name, err)
		}
		if err := p.waitGKEOperation(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

// managedHealth reports the GKE cluster status.
func (p *Provider) managedHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	c, err := p.containerService.Projects.Locations.Clusters.Get(p.gkeClusterName(clusterID)).Context(ctx).Do()
	if err != nil {
		return &types.HealthStatus{Status: "unhealthy", Components: map[string]types.ComponentHealth{"cluster": {Status: "unhealthy", Message: err.Error()}}, LastCheck: time.Now()}, nil
	}
	status := "healthy"
	if c.Status != "RUNNING" {
		status = "degraded"
	}
	msg := "GKE " + c.Status
	if c.StatusMessage != "" {
		msg += ": " + c.StatusMessage
	}
	return &types.HealthStatus{Status: status, Components: map[string]types.ComponentHealth{"control-plane": {Status: status, Message: msg}}, LastCheck: time.Now()}, nil
}

// managedListClusters returns the adhar-labelled GKE clusters in the project.
func (p *Provider) managedListClusters(ctx context.Context) ([]*types.Cluster, error) {
	resp, err := p.containerService.Projects.Locations.Clusters.List(fmt.Sprintf("projects/%s/locations/-", p.config.ProjectID)).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	var clusters []*types.Cluster
	for _, c := range resp.Clusters {
		if c.ResourceLabels["managed-by"] != "adhar" {
			continue
		}
		clusters = append(clusters, p.gkeToCluster(fmt.Sprintf("gcp/%s/%s", p.config.ProjectID, c.Name), c))
	}
	return clusters, nil
}
