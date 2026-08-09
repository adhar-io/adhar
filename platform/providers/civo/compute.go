package civo

// Self-managed ("compute") cluster mode: provision raw Civo instances and
// bootstrap Kubernetes on them with kubeadm, mirroring the platform's Kind
// flow — containerd runtime, kube-proxy skipped (Cilium installed by the
// adhar bootstrap replaces it), and no CNI preinstalled. Nodes therefore stay
// NotReady until the platform bootstrap installs Cilium; the cluster is
// considered created once the API server answers.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/civo/civogo"
	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

const (
	// computeClusterTagPrefix marks every instance belonging to a compute-mode
	// cluster; the per-cluster tag is computeClusterTagPrefix + name.
	computeClusterTagPrefix = "adhar-cluster-"
	computeMasterTag        = "adhar-role-master"
	computeWorkerTag        = "adhar-role-worker"

	// Civo Ubuntu images log in as the "civo" user (civogo.DefaultInstanceUser);
	// the shared SSHRun wraps commands in sudo for non-root users.
	computeSSHUser = civogo.DefaultInstanceUser

	// Ubuntu 22.04 disk image name in Civo's catalogue.
	computeDiskImage = "ubuntu-jammy"
)

// computeClusterName normalizes a cluster ID/name for compute-mode resources.
func computeClusterName(name string) string {
	return strings.TrimPrefix(name, computeClusterTagPrefix)
}

func computeClusterTag(name string) string {
	return computeClusterTagPrefix + computeClusterName(name)
}

// ensureComputeSSHKey loads/creates the per-cluster keypair and makes sure the
// public key is registered with Civo so new instances accept it.
func (p *Provider) ensureComputeSSHKey(clusterName string) (string, ssh.Signer, error) {
	signer, pubKey, err := provider.EnsureClusterSSHKey(clusterName)
	if err != nil {
		return "", nil, err
	}

	keyName := "adhar-" + clusterName

	// Reuse the registered key when present (matching by name).
	if keys, err := p.client.ListSSHKeys(); err == nil {
		for _, k := range keys {
			if k.Name == keyName {
				return k.ID, signer, nil
			}
		}
	}

	resp, err := p.client.NewSSHKey(keyName, pubKey)
	if err != nil {
		return "", nil, fmt.Errorf("failed to register SSH key with Civo: %w", err)
	}
	return resp.ID, signer, nil
}

// ensureComputeNetwork returns the network ID to place the cluster in: an
// explicitly configured network, or a per-cluster network created on demand.
func (p *Provider) ensureComputeNetwork(clusterName string) (string, error) {
	if p.config.NetworkID != "" {
		return p.config.NetworkID, nil
	}

	label := fmt.Sprintf("adhar-%s-net", clusterName)
	if p.config.NetworkLabel != "" {
		label = p.config.NetworkLabel
	}

	if networks, err := p.client.ListNetworks(); err == nil {
		for _, n := range networks {
			if n.Label == label {
				return n.ID, nil
			}
		}
	}

	network, err := p.client.NewNetwork(label)
	if err != nil {
		return "", fmt.Errorf("failed to create network %s: %w", label, err)
	}
	return network.ID, nil
}

// ensureComputeFirewall creates the cluster firewall on the given network:
// SSH + Kubernetes API + NodePort range from anywhere, everything between
// cluster members (network CIDR), all egress.
func (p *Provider) ensureComputeFirewall(clusterName, networkID string) (string, error) {
	fwName := fmt.Sprintf("adhar-%s-fw", clusterName)

	if fws, err := p.client.ListFirewalls(); err == nil {
		for _, fw := range fws {
			if fw.Name == fwName {
				return fw.ID, nil
			}
		}
	}

	// Create without Civo's default rules; explicit rules follow.
	noDefaults := false
	fw, err := p.client.NewFirewall(&civogo.FirewallConfig{
		Name:        fwName,
		Region:      p.config.Region,
		NetworkID:   networkID,
		CreateRules: &noDefaults,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create firewall %s: %w", fwName, err)
	}

	// Intra-cluster traffic is scoped to the network CIDR (etcd, kubelet,
	// Cilium VXLAN/health, etc.); fall back to the private range when the API
	// does not report one.
	networkCIDR := "10.0.0.0/8"
	if network, err := p.client.GetNetwork(networkID); err == nil && network.CIDR != "" {
		networkCIDR = network.CIDR
	}

	anywhere := []string{"0.0.0.0/0"}
	rules := []civogo.FirewallRuleConfig{
		{Protocol: "tcp", StartPort: "22", EndPort: "22", Cidr: anywhere, Label: "ssh"},
		{Protocol: "tcp", StartPort: "6443", EndPort: "6443", Cidr: anywhere, Label: "kubernetes-api"},
		{Protocol: "tcp", StartPort: "30000", EndPort: "32767", Cidr: anywhere, Label: "nodeports-tcp"},
		{Protocol: "udp", StartPort: "30000", EndPort: "32767", Cidr: anywhere, Label: "nodeports-udp"},
		{Protocol: "tcp", StartPort: "1", EndPort: "65535", Cidr: []string{networkCIDR}, Label: "cluster-internal-tcp"},
		{Protocol: "udp", StartPort: "1", EndPort: "65535", Cidr: []string{networkCIDR}, Label: "cluster-internal-udp"},
		{Direction: "egress", Protocol: "tcp", StartPort: "1", EndPort: "65535", Cidr: anywhere, Label: "egress-tcp"},
		{Direction: "egress", Protocol: "udp", StartPort: "1", EndPort: "65535", Cidr: anywhere, Label: "egress-udp"},
	}
	for i := range rules {
		rules[i].FirewallID = fw.ID
		rules[i].Region = p.config.Region
		rules[i].Action = "allow"
		if rules[i].Direction == "" {
			rules[i].Direction = "ingress"
		}
		if _, err := p.client.NewFirewallRule(&rules[i]); err != nil {
			return "", fmt.Errorf("failed to create firewall rule %s: %w", rules[i].Label, err)
		}
	}
	return fw.ID, nil
}

// computeDiskImageID resolves the Ubuntu 22.04 disk image for instances.
func (p *Provider) computeDiskImageID() (string, error) {
	image, err := p.client.FindDiskImage(computeDiskImage)
	if err != nil {
		return "", fmt.Errorf("failed to find disk image %q: %w", computeDiskImage, err)
	}
	return image.ID, nil
}

// createComputeInstance creates one instance for the cluster and waits until
// it is ACTIVE with a public IP.
func (p *Provider) createComputeInstance(ctx context.Context, name, networkID, firewallID, sshKeyID, imageID, size, userData string, tags []string) (*civogo.Instance, error) {
	config := &civogo.InstanceConfig{
		Count:            1,
		Hostname:         name,
		Size:             size,
		Region:           p.config.Region,
		PublicIPRequired: "true",
		NetworkID:        networkID,
		TemplateID:       imageID,
		InitialUser:      computeSSHUser,
		SSHKeyID:         sshKeyID,
		Script:           userData,
		Tags:             tags,
		FirewallID:       firewallID,
	}

	instance, err := p.client.CreateInstance(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create instance %s: %w", name, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		i, err := p.client.GetInstance(instance.ID)
		if err == nil && i.Status == "ACTIVE" && i.PublicIP != "" {
			return i, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("instance %s did not become ACTIVE in time", name)
		case <-time.After(10 * time.Second):
		}
	}
}

// createComputeCluster provisions Civo instances and bootstraps Kubernetes on
// them with kubeadm.
func (p *Provider) createComputeCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	name := computeClusterName(spec.Name)
	log.Printf("Creating self-managed Kubernetes cluster %q on Civo instances", name)

	if spec.ControlPlane.Replicas > 1 || spec.ControlPlane.HighAvailability {
		return nil, fmt.Errorf("compute mode currently supports a single control-plane node; HA control planes require a load balancer and stacked etcd and are not implemented yet")
	}

	k8sMinor := provider.K8sMinorFromVersion(spec.Version)
	userData := provider.KubeadmNodePrepScript(k8sMinor)
	tag := computeClusterTag(name)

	sshKeyID, signer, err := p.ensureComputeSSHKey(name)
	if err != nil {
		return nil, err
	}
	networkID, err := p.ensureComputeNetwork(name)
	if err != nil {
		return nil, err
	}
	firewallID, err := p.ensureComputeFirewall(name, networkID)
	if err != nil {
		return nil, err
	}
	imageID, err := p.computeDiskImageID()
	if err != nil {
		return nil, err
	}

	// Control-plane instance
	masterSize := spec.ControlPlane.InstanceType
	if masterSize == "" {
		masterSize = p.config.Size
	}
	master, err := p.createComputeInstance(ctx, fmt.Sprintf("adhar-%s-master-1", name), networkID, firewallID, sshKeyID, imageID, masterSize, userData, []string{tag, computeMasterTag})
	if err != nil {
		return nil, err
	}

	// Worker instances (default pool when the spec declares none)
	type workerReq struct {
		name string
		size string
	}
	var workers []workerReq
	for _, ng := range spec.NodeGroups {
		size := ng.InstanceType
		if size == "" {
			size = p.config.Size
		}
		count := ng.Replicas
		if count <= 0 {
			count = 1
		}
		for i := 1; i <= count; i++ {
			workers = append(workers, workerReq{fmt.Sprintf("adhar-%s-%s-%d", name, ng.Name, i), size})
		}
	}
	if len(workers) == 0 {
		for i := 1; i <= 2; i++ {
			workers = append(workers, workerReq{fmt.Sprintf("adhar-%s-worker-%d", name, i), p.config.Size})
		}
	}

	workerInstances := make([]*civogo.Instance, 0, len(workers))
	for _, w := range workers {
		instance, err := p.createComputeInstance(ctx, w.name, networkID, firewallID, sshKeyID, imageID, w.size, userData, []string{tag, computeWorkerTag})
		if err != nil {
			return nil, err
		}
		workerInstances = append(workerInstances, instance)
	}

	// Wait for node preparation, then drive kubeadm over SSH.
	if err := provider.WaitForNodePrep(ctx, signer, computeSSHUser, master.PublicIP, 15*time.Minute); err != nil {
		return nil, fmt.Errorf("control-plane node not ready: %w", err)
	}

	joinCmd, err := provider.KubeadmInitMaster(signer, computeSSHUser, master.PublicIP, master.PrivateIP)
	if err != nil {
		return nil, err
	}

	for _, instance := range workerInstances {
		if err := provider.WaitForNodePrep(ctx, signer, computeSSHUser, instance.PublicIP, 15*time.Minute); err != nil {
			return nil, fmt.Errorf("worker %s not ready: %w", instance.Hostname, err)
		}
		if err := provider.KubeadmJoinWorker(signer, computeSSHUser, instance.PublicIP, joinCmd); err != nil {
			return nil, fmt.Errorf("worker %s: %w", instance.Hostname, err)
		}
	}

	cluster := &types.Cluster{
		ID:        tag,
		Name:      name,
		Provider:  "civo",
		Region:    p.config.Region,
		Version:   k8sMinor,
		Status:    types.ClusterStatusRunning,
		Endpoint:  fmt.Sprintf("https://%s:6443", master.PublicIP),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"mode":        "compute",
			"region":      p.config.Region,
			"network":     networkID,
			"masterIP":    master.PublicIP,
			"workerNodes": len(workerInstances),
		},
	}
	p.clusters[cluster.ID] = cluster

	log.Printf("Self-managed cluster %q is up: API %s (%d workers). Nodes stay NotReady until the platform bootstrap installs Cilium.", name, cluster.Endpoint, len(workerInstances))
	return cluster, nil
}

// computeClusterInstances returns the instances belonging to a compute cluster.
func (p *Provider) computeClusterInstances(clusterName string) ([]civogo.Instance, error) {
	tag := computeClusterTag(clusterName)
	all, err := p.client.ListAllInstances()
	if err != nil {
		return nil, fmt.Errorf("failed to list instances for cluster %s: %w", clusterName, err)
	}
	var instances []civogo.Instance
	for _, i := range all {
		for _, t := range i.Tags {
			if t == tag {
				instances = append(instances, i)
				break
			}
		}
	}
	return instances, nil
}

func isComputeMaster(instance *civogo.Instance) bool {
	for _, t := range instance.Tags {
		if t == computeMasterTag {
			return true
		}
	}
	return false
}

// computeMasterIP finds the public IP of the cluster's control-plane instance.
func (p *Provider) computeMasterIP(clusterName string) (string, error) {
	instances, err := p.computeClusterInstances(clusterName)
	if err != nil {
		return "", err
	}
	for i := range instances {
		if isComputeMaster(&instances[i]) {
			if instances[i].PublicIP == "" {
				return "", fmt.Errorf("control-plane instance %s has no public IP", instances[i].Hostname)
			}
			return instances[i].PublicIP, nil
		}
	}
	return "", fmt.Errorf("no control-plane instance found for cluster %s", clusterName)
}

// computeGetKubeconfig fetches admin.conf from the control-plane node and
// rewrites the server endpoint to the public IP.
func (p *Provider) computeGetKubeconfig(clusterName string) (string, error) {
	masterIP, err := p.computeMasterIP(clusterName)
	if err != nil {
		return "", err
	}
	signer, err := provider.LoadClusterSSHKey(clusterName)
	if err != nil {
		return "", err
	}
	return provider.FetchAdminKubeconfig(signer, computeSSHUser, masterIP)
}

// computeClusterFromInstances summarizes a compute cluster from its instances.
func (p *Provider) computeClusterFromInstances(clusterName string, instances []civogo.Instance) *types.Cluster {
	status := types.ClusterStatusRunning
	endpoint := ""
	workers := 0
	var created time.Time
	for i := range instances {
		instance := &instances[i]
		if instance.Status != "ACTIVE" {
			status = types.ClusterStatusCreating
		}
		if isComputeMaster(instance) {
			if instance.PublicIP != "" {
				endpoint = fmt.Sprintf("https://%s:6443", instance.PublicIP)
			}
		} else {
			workers++
		}
		if !instance.CreatedAt.IsZero() && (created.IsZero() || instance.CreatedAt.Before(created)) {
			created = instance.CreatedAt
		}
	}
	return &types.Cluster{
		ID:        computeClusterTag(clusterName),
		Name:      clusterName,
		Provider:  "civo",
		Region:    p.config.Region,
		Status:    status,
		Endpoint:  endpoint,
		CreatedAt: created,
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"mode":        "compute",
			"workerNodes": workers,
		},
	}
}

// listComputeClusters discovers compute-mode clusters from instance tags.
func (p *Provider) listComputeClusters() ([]*types.Cluster, error) {
	all, err := p.client.ListAllInstances()
	if err != nil {
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}
	byCluster := map[string][]civogo.Instance{}
	for _, i := range all {
		for _, t := range i.Tags {
			if strings.HasPrefix(t, computeClusterTagPrefix) {
				name := strings.TrimPrefix(t, computeClusterTagPrefix)
				byCluster[name] = append(byCluster[name], i)
			}
		}
	}

	var clusters []*types.Cluster
	for name, instances := range byCluster {
		clusters = append(clusters, p.computeClusterFromInstances(name, instances))
	}
	return clusters, nil
}

// deleteComputeCluster tears down all cloud resources of a compute cluster:
// instances (by tag), the firewall, the per-cluster network, the registered
// SSH key, and local state.
func (p *Provider) deleteComputeCluster(ctx context.Context, clusterID string) error {
	name := computeClusterName(clusterID)
	tag := computeClusterTag(name)
	log.Printf("Deleting self-managed cluster %q (instances tagged %s)", name, tag)

	instances, err := p.computeClusterInstances(name)
	if err != nil {
		return err
	}
	for i := range instances {
		if _, err := p.client.DeleteInstance(instances[i].ID); err != nil {
			return fmt.Errorf("failed to delete instance %s: %w", instances[i].Hostname, err)
		}
	}

	// Wait for instances to disappear so the firewall and network can be
	// removed.
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		remaining, err := p.computeClusterInstances(name)
		if err == nil && len(remaining) == 0 {
			break
		}
		select {
		case <-waitCtx.Done():
			log.Printf("Warning: instances of cluster %s still present after timeout; continuing cleanup", name)
		case <-time.After(10 * time.Second):
			continue
		}
		break
	}

	// Firewall
	if fws, err := p.client.ListFirewalls(); err == nil {
		for _, fw := range fws {
			if fw.Name == fmt.Sprintf("adhar-%s-fw", name) {
				if _, err := p.client.DeleteFirewall(fw.ID); err != nil {
					log.Printf("Warning: failed to delete firewall %s: %v", fw.Name, err)
				}
			}
		}
	}

	// Per-cluster network (only the one we created by naming convention)
	if networks, err := p.client.ListNetworks(); err == nil {
		for _, n := range networks {
			if n.Label == fmt.Sprintf("adhar-%s-net", name) {
				if _, err := p.client.DeleteNetwork(n.ID); err != nil {
					log.Printf("Warning: failed to delete network %s (may still have members): %v", n.Label, err)
				}
			}
		}
	}

	// Registered SSH key
	if keys, err := p.client.ListSSHKeys(); err == nil {
		for _, k := range keys {
			if k.Name == "adhar-"+name {
				if _, err := p.client.DeleteSSHKey(k.ID); err != nil {
					log.Printf("Warning: failed to delete SSH key %s: %v", k.Name, err)
				}
			}
		}
	}

	// Local state
	provider.RemoveClusterState(name)

	delete(p.clusters, tag)
	delete(p.clusters, name)
	log.Printf("Deleted self-managed cluster %q", name)
	return nil
}

// isComputeCluster reports whether the given cluster ID/name refers to a
// compute-mode cluster (by tag prefix or instance discovery).
func (p *Provider) isComputeCluster(clusterID string) bool {
	if strings.HasPrefix(clusterID, computeClusterTagPrefix) {
		return true
	}
	instances, err := p.computeClusterInstances(clusterID)
	return err == nil && len(instances) > 0
}

// upgradeComputeCluster performs an in-place kubeadm upgrade of a compute
// cluster: control plane first, then every worker.
func (p *Provider) upgradeComputeCluster(ctx context.Context, clusterID, version string) error {
	name := computeClusterName(clusterID)
	log.Printf("Upgrading self-managed cluster %q to %s via kubeadm", name, version)

	instances, err := p.computeClusterInstances(name)
	if err != nil {
		return err
	}
	var masterIP string
	var workerIPs []string
	for i := range instances {
		if instances[i].PublicIP == "" {
			continue
		}
		if isComputeMaster(&instances[i]) {
			masterIP = instances[i].PublicIP
		} else {
			workerIPs = append(workerIPs, instances[i].PublicIP)
		}
	}
	if masterIP == "" {
		return fmt.Errorf("no reachable control-plane instance for cluster %s", name)
	}
	signer, err := provider.LoadClusterSSHKey(name)
	if err != nil {
		return err
	}
	if err := provider.KubeadmUpgradeCluster(ctx, signer, computeSSHUser, masterIP, workerIPs, version); err != nil {
		return fmt.Errorf("kubeadm upgrade of cluster %s failed: %w", name, err)
	}
	log.Printf("Successfully upgraded cluster %q to %s", name, version)
	return nil
}
