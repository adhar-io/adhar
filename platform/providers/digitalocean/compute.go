package digitalocean

// Self-managed ("compute") cluster mode: provision raw droplets and bootstrap
// Kubernetes on them with kubeadm, mirroring the platform's Kind flow —
// containerd runtime, kube-proxy skipped (Cilium installed by the adhar
// bootstrap replaces it), and no CNI preinstalled. Nodes therefore stay
// NotReady until the platform bootstrap installs Cilium; the cluster is
// considered created once the API server answers.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

const (
	// computeClusterTag marks every droplet belonging to a compute-mode
	// cluster; the per-cluster tag is computeClusterTagPrefix + name.
	computeClusterTagPrefix = "adhar-cluster-"
	computeMasterTag        = "adhar-role-master"
	computeWorkerTag        = "adhar-role-worker"

	// Droplets allow direct root SSH with the injected key.
	computeSSHUser = "root"
)

// computeClusterName normalizes a cluster ID/name for compute-mode resources.
func computeClusterName(name string) string {
	return strings.TrimPrefix(name, computeClusterTagPrefix)
}

func computeClusterTag(name string) string {
	return computeClusterTagPrefix + computeClusterName(name)
}

// ensureSSHKey loads/creates the per-cluster keypair and makes sure the
// public key is registered with DigitalOcean so new droplets accept it for
// the root user.
func (p *Provider) ensureSSHKey(ctx context.Context, clusterName string) (*godo.Key, ssh.Signer, error) {
	signer, pubKey, err := provider.EnsureClusterSSHKey(clusterName)
	if err != nil {
		return nil, nil, err
	}

	keyName := "adhar-" + clusterName

	// Reuse the registered key when present (matching by name).
	keys, _, err := p.client.Keys.List(ctx, &godo.ListOptions{PerPage: 200})
	if err == nil {
		for i := range keys {
			if keys[i].Name == keyName {
				return &keys[i], signer, nil
			}
		}
	}

	key, _, err := p.client.Keys.Create(ctx, &godo.KeyCreateRequest{
		Name:      keyName,
		PublicKey: pubKey,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to register SSH key with DigitalOcean: %w", err)
	}
	return key, signer, nil
}

// ensureComputeVPC returns the VPC UUID to place the cluster in: an explicitly
// configured VPC, or a per-cluster VPC created on demand.
func (p *Provider) ensureComputeVPC(ctx context.Context, clusterName string) (string, error) {
	if p.config.VPCUUID != "" {
		return p.config.VPCUUID, nil
	}

	vpcName := fmt.Sprintf("adhar-%s-vpc", clusterName)
	vpcs, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{PerPage: 200})
	if err == nil {
		for _, v := range vpcs {
			if v.Name == vpcName && v.RegionSlug == p.config.Region {
				return v.ID, nil
			}
		}
	}

	// Accounts routinely hold other VPCs; on a range-overlap rejection walk
	// alternative /16s instead of failing the whole cluster creation.
	candidates := []string{p.config.VPCCIDR}
	for _, second := range []int{20, 21, 22, 23, 24, 25, 26, 27} {
		candidates = append(candidates, fmt.Sprintf("10.%d.0.0/16", second))
	}
	var lastErr error
	for _, cidr := range candidates {
		vpc, _, err := p.client.VPCs.Create(ctx, &godo.VPCCreateRequest{
			Name:       vpcName,
			RegionSlug: p.config.Region,
			IPRange:    cidr,
		})
		if err == nil {
			if cidr != p.config.VPCCIDR {
				log.Printf("VPC CIDR %s overlapped an existing network; using %s instead", p.config.VPCCIDR, cidr)
			}
			return vpc.ID, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "overlaps") {
			break
		}
	}
	return "", fmt.Errorf("failed to create VPC %s: %w", vpcName, lastErr)
}

// ensureComputeFirewall creates the cluster firewall targeting the cluster tag:
// SSH + Kubernetes API + NodePort range from anywhere, everything between
// cluster members, all egress.
func (p *Provider) ensureComputeFirewall(ctx context.Context, clusterName string) (string, error) {
	fwName := fmt.Sprintf("adhar-%s-fw", clusterName)
	tag := computeClusterTag(clusterName)

	// The firewall references the cluster tag as a source and target; the DO
	// API rejects that (422) unless the tag already exists, and no droplet has
	// created it implicitly yet at this point.
	if _, _, err := p.client.Tags.Create(ctx, &godo.TagCreateRequest{Name: tag}); err != nil &&
		!strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "409") {
		return "", fmt.Errorf("failed to create cluster tag %s: %w", tag, err)
	}

	fws, _, err := p.client.Firewalls.List(ctx, &godo.ListOptions{PerPage: 200})
	if err == nil {
		for _, fw := range fws {
			if fw.Name == fwName {
				return fw.ID, nil
			}
		}
	}

	anywhere := &godo.Sources{Addresses: []string{"0.0.0.0/0", "::/0"}}
	clusterSrc := &godo.Sources{Tags: []string{tag}}
	req := &godo.FirewallRequest{
		Name: fwName,
		Tags: []string{tag},
		InboundRules: []godo.InboundRule{
			{Protocol: "tcp", PortRange: "22", Sources: anywhere},
			{Protocol: "tcp", PortRange: "6443", Sources: anywhere},
			{Protocol: "tcp", PortRange: "30000-32767", Sources: anywhere},
			{Protocol: "udp", PortRange: "30000-32767", Sources: anywhere},
			// Unrestricted traffic between cluster members (etcd, kubelet,
			// Cilium VXLAN/Geneve/health, etc.).
			{Protocol: "tcp", PortRange: "1-65535", Sources: clusterSrc},
			{Protocol: "udp", PortRange: "1-65535", Sources: clusterSrc},
			{Protocol: "icmp", Sources: clusterSrc},
		},
		OutboundRules: []godo.OutboundRule{
			{Protocol: "tcp", PortRange: "1-65535", Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}}},
			{Protocol: "udp", PortRange: "1-65535", Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}}},
			{Protocol: "icmp", Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}}},
		},
	}

	fw, _, err := p.client.Firewalls.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to create firewall %s: %w", fwName, err)
	}
	return fw.ID, nil
}

// createComputeDroplet creates one droplet for the cluster and waits until it
// is active with a public IP.
func (p *Provider) createComputeDroplet(ctx context.Context, name, vpcUUID string, keyID int, size, userData string, tags []string) (*godo.Droplet, error) {
	req := &godo.DropletCreateRequest{
		Name:   name,
		Region: p.config.Region,
		Size:   size,
		Image:  godo.DropletCreateImage{Slug: p.config.Image},
		SSHKeys: []godo.DropletCreateSSHKey{
			{ID: keyID},
		},
		VPCUUID:  vpcUUID,
		Tags:     tags,
		UserData: userData,
	}

	droplet, _, err := p.client.Droplets.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create droplet %s: %w", name, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		d, _, err := p.client.Droplets.Get(waitCtx, droplet.ID)
		if err == nil && d.Status == "active" {
			if ip, _ := d.PublicIPv4(); ip != "" {
				return d, nil
			}
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("droplet %s did not become active in time", name)
		case <-time.After(10 * time.Second):
		}
	}
}

// createComputeCluster provisions droplets and bootstraps Kubernetes on them
// with kubeadm.
func (p *Provider) createComputeCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	name := computeClusterName(spec.Name)
	log.Printf("Creating self-managed Kubernetes cluster %q on DigitalOcean droplets", name)

	if spec.ControlPlane.Replicas > 1 || spec.ControlPlane.HighAvailability {
		return nil, fmt.Errorf("compute mode currently supports a single control-plane node; HA control planes require a load balancer and stacked etcd and are not implemented yet")
	}

	k8sMinor := provider.K8sMinorFromVersion(spec.Version)
	userData := provider.KubeadmNodePrepScript(k8sMinor)
	tag := computeClusterTag(name)

	key, signer, err := p.ensureSSHKey(ctx, name)
	if err != nil {
		return nil, err
	}
	vpcUUID, err := p.ensureComputeVPC(ctx, name)
	if err != nil {
		return nil, err
	}
	if _, err := p.ensureComputeFirewall(ctx, name); err != nil {
		return nil, err
	}

	// Control-plane droplet
	masterSize := spec.ControlPlane.InstanceType
	if masterSize == "" {
		masterSize = p.config.DropletSize
	}
	existing, err := p.computeClusterDroplets(ctx, name)
	if err != nil {
		return nil, err
	}
	existingByName := map[string]*godo.Droplet{}
	for i := range existing {
		existingByName[existing[i].Name] = &existing[i]
	}

	// Adopt already-provisioned droplets so an interrupted `adhar up` can be
	// re-run without duplicating infrastructure; kubeadm init/join and the
	// cloud-integration steps below are individually idempotent.
	masterName := fmt.Sprintf("adhar-%s-master-1", name)
	master := existingByName[masterName]
	if master == nil {
		master, err = p.createComputeDroplet(ctx, masterName, vpcUUID, key.ID, masterSize, userData, []string{tag, computeMasterTag})
		if err != nil {
			return nil, err
		}
	} else {
		log.Printf("Adopting existing control-plane droplet %s", masterName)
	}
	masterIP, _ := master.PublicIPv4()
	masterPrivateIP, _ := master.PrivateIPv4()

	// Worker droplets (default pool when the spec declares none)
	type workerReq struct {
		name string
		size string
	}
	var workers []workerReq
	for _, ng := range spec.NodeGroups {
		size := ng.InstanceType
		if size == "" {
			size = p.config.DropletSize
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
			workers = append(workers, workerReq{fmt.Sprintf("adhar-%s-worker-%d", name, i), p.config.DropletSize})
		}
	}

	workerDroplets := make([]*godo.Droplet, 0, len(workers))
	for _, w := range workers {
		if d := existingByName[w.name]; d != nil {
			log.Printf("Adopting existing worker droplet %s", w.name)
			workerDroplets = append(workerDroplets, d)
			continue
		}
		d, err := p.createComputeDroplet(ctx, w.name, vpcUUID, key.ID, w.size, userData, []string{tag, computeWorkerTag})
		if err != nil {
			return nil, err
		}
		workerDroplets = append(workerDroplets, d)
	}

	// Wait for node preparation, then drive kubeadm over SSH.
	if err := provider.WaitForNodePrep(ctx, signer, computeSSHUser, masterIP, 15*time.Minute); err != nil {
		return nil, fmt.Errorf("control-plane node not ready: %w", err)
	}

	if err := enableExternalCloudProvider(signer, masterIP, masterPrivateIP); err != nil {
		return nil, fmt.Errorf("failed to enable external cloud provider on master: %w", err)
	}
	joinCmd, err := provider.KubeadmInitMaster(signer, computeSSHUser, masterIP, masterPrivateIP)
	if err != nil {
		return nil, err
	}

	for _, d := range workerDroplets {
		ip, _ := d.PublicIPv4()
		if err := provider.WaitForNodePrep(ctx, signer, computeSSHUser, ip, 15*time.Minute); err != nil {
			return nil, fmt.Errorf("worker %s not ready: %w", d.Name, err)
		}
		privIP, _ := d.PrivateIPv4()
		if err := enableExternalCloudProvider(signer, ip, privIP); err != nil {
			return nil, fmt.Errorf("worker %s: %w", d.Name, err)
		}
		if err := provider.KubeadmJoinWorker(signer, computeSSHUser, ip, joinCmd); err != nil {
			return nil, fmt.Errorf("worker %s: %w", d.Name, err)
		}
	}

	if err := p.installDOCloudIntegration(signer, masterIP, vpcUUID); err != nil {
		return nil, err
	}

	cluster := &types.Cluster{
		ID:        tag,
		Name:      name,
		Provider:  "digitalocean",
		Region:    p.config.Region,
		Version:   k8sMinor,
		Status:    types.ClusterStatusRunning,
		Endpoint:  fmt.Sprintf("https://%s:6443", masterIP),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"mode":        "compute",
			"region":      p.config.Region,
			"vpc":         vpcUUID,
			"masterIP":    masterIP,
			"workerNodes": len(workerDroplets),
		},
	}
	p.clusters[cluster.ID] = cluster

	log.Printf("Self-managed cluster %q is up: API %s (%d workers). Nodes stay NotReady until the platform bootstrap installs Cilium.", name, cluster.Endpoint, len(workerDroplets))
	return cluster, nil
}

// computeClusterDroplets returns the droplets belonging to a compute cluster.
func (p *Provider) computeClusterDroplets(ctx context.Context, clusterName string) ([]godo.Droplet, error) {
	tag := computeClusterTag(clusterName)
	var all []godo.Droplet
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		page, resp, err := p.client.Droplets.ListByTag(ctx, tag, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list droplets for cluster %s: %w", clusterName, err)
		}
		all = append(all, page...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		current, err := resp.Links.CurrentPage()
		if err != nil {
			break
		}
		opts.Page = current + 1
	}
	return all, nil
}

// computeMasterIP finds the public IP of the cluster's control-plane droplet.
func (p *Provider) computeMasterIP(ctx context.Context, clusterName string) (string, error) {
	droplets, err := p.computeClusterDroplets(ctx, clusterName)
	if err != nil {
		return "", err
	}
	for _, d := range droplets {
		for _, t := range d.Tags {
			if t == computeMasterTag {
				ip, err := d.PublicIPv4()
				if err != nil || ip == "" {
					return "", fmt.Errorf("control-plane droplet %s has no public IP", d.Name)
				}
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no control-plane droplet found for cluster %s", clusterName)
}

// computeGetKubeconfig fetches admin.conf from the control-plane node and
// rewrites the server endpoint to the public IP.
func (p *Provider) computeGetKubeconfig(ctx context.Context, clusterName string) (string, error) {
	masterIP, err := p.computeMasterIP(ctx, clusterName)
	if err != nil {
		return "", err
	}
	signer, err := provider.LoadClusterSSHKey(clusterName)
	if err != nil {
		return "", err
	}
	return provider.FetchAdminKubeconfig(signer, computeSSHUser, masterIP)
}

// computeClusterFromDroplets summarizes a compute cluster from its droplets.
func (p *Provider) computeClusterFromDroplets(clusterName string, droplets []godo.Droplet) *types.Cluster {
	status := types.ClusterStatusRunning
	endpoint := ""
	workers := 0
	var created time.Time
	for _, d := range droplets {
		if d.Status != "active" {
			status = types.ClusterStatusCreating
		}
		isMaster := false
		for _, t := range d.Tags {
			if t == computeMasterTag {
				isMaster = true
			}
		}
		if isMaster {
			if ip, _ := d.PublicIPv4(); ip != "" {
				endpoint = fmt.Sprintf("https://%s:6443", ip)
			}
		} else {
			workers++
		}
		if t, err := time.Parse(time.RFC3339, d.Created); err == nil && (created.IsZero() || t.Before(created)) {
			created = t
		}
	}
	return &types.Cluster{
		ID:        computeClusterTag(clusterName),
		Name:      clusterName,
		Provider:  "digitalocean",
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

// listComputeClusters discovers compute-mode clusters from droplet tags.
func (p *Provider) listComputeClusters(ctx context.Context) ([]*types.Cluster, error) {
	byCluster := map[string][]godo.Droplet{}
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		page, resp, err := p.client.Droplets.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list droplets: %w", err)
		}
		for _, d := range page {
			for _, t := range d.Tags {
				if strings.HasPrefix(t, computeClusterTagPrefix) {
					byCluster[strings.TrimPrefix(t, computeClusterTagPrefix)] = append(byCluster[strings.TrimPrefix(t, computeClusterTagPrefix)], d)
				}
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		current, err := resp.Links.CurrentPage()
		if err != nil {
			break
		}
		opts.Page = current + 1
	}

	var clusters []*types.Cluster
	for name, droplets := range byCluster {
		clusters = append(clusters, p.computeClusterFromDroplets(name, droplets))
	}
	return clusters, nil
}

// deleteComputeCluster tears down all cloud resources of a compute cluster:
// droplets (by tag), the firewall, the per-cluster VPC, the registered SSH
// key, and local state.
func (p *Provider) deleteComputeCluster(ctx context.Context, clusterName string) error {
	name := computeClusterName(clusterName)
	tag := computeClusterTag(name)
	log.Printf("Deleting self-managed cluster %q (droplets tagged %s)", name, tag)

	if _, err := p.client.Droplets.DeleteByTag(ctx, tag); err != nil {
		return fmt.Errorf("failed to delete droplets for cluster %s: %w", name, err)
	}

	// Wait for droplets to disappear so the VPC can be removed.
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		droplets, err := p.computeClusterDroplets(waitCtx, name)
		if err == nil && len(droplets) == 0 {
			break
		}
		select {
		case <-waitCtx.Done():
			log.Printf("Warning: droplets of cluster %s still present after timeout; continuing cleanup", name)
		case <-time.After(10 * time.Second):
			continue
		}
		break
	}

	// Firewall
	if fws, _, err := p.client.Firewalls.List(ctx, &godo.ListOptions{PerPage: 200}); err == nil {
		for _, fw := range fws {
			if fw.Name == fmt.Sprintf("adhar-%s-fw", name) {
				if _, err := p.client.Firewalls.Delete(ctx, fw.ID); err != nil {
					log.Printf("Warning: failed to delete firewall %s: %v", fw.Name, err)
				}
			}
		}
	}

	// Per-cluster VPC (only the one we created by naming convention). DO keeps
	// reporting membership for a short window after droplet deletion, so retry
	// the 409 with backoff instead of leaving the VPC behind.
	if vpcs, _, err := p.client.VPCs.List(ctx, &godo.ListOptions{PerPage: 200}); err == nil {
		for _, v := range vpcs {
			if v.Name == fmt.Sprintf("adhar-%s-vpc", name) {
				var delErr error
				for attempt := 0; attempt < 6; attempt++ {
					if _, delErr = p.client.VPCs.Delete(ctx, v.ID); delErr == nil {
						break
					}
					time.Sleep(10 * time.Second)
				}
				if delErr != nil {
					log.Printf("Warning: failed to delete VPC %s after retries: %v", v.Name, delErr)
				}
			}
		}
	}

	// Cluster tags (droplet deletion does not remove them)
	for _, t := range []string{tag, computeMasterTag, computeWorkerTag} {
		if _, err := p.client.Tags.Delete(ctx, t); err != nil {
			log.Printf("Note: tag %s not deleted (may be shared with other clusters): %v", t, err)
		}
	}

	// Registered SSH key
	if keys, _, err := p.client.Keys.List(ctx, &godo.ListOptions{PerPage: 200}); err == nil {
		for _, k := range keys {
			if k.Name == "adhar-"+name {
				if _, err := p.client.Keys.DeleteByID(ctx, k.ID); err != nil {
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
// compute-mode cluster (by tag prefix or droplet discovery).
func (p *Provider) isComputeCluster(ctx context.Context, clusterID string) bool {
	if strings.HasPrefix(clusterID, computeClusterTagPrefix) {
		return true
	}
	droplets, err := p.computeClusterDroplets(ctx, clusterID)
	return err == nil && len(droplets) > 0
}

// upgradeComputeCluster performs an in-place kubeadm upgrade of a compute
// cluster: control plane first, then every worker.
func (p *Provider) upgradeComputeCluster(ctx context.Context, clusterID, version string) error {
	name := computeClusterName(clusterID)
	log.Printf("Upgrading self-managed cluster %q to %s via kubeadm", name, version)

	droplets, err := p.computeClusterDroplets(ctx, name)
	if err != nil {
		return err
	}
	var masterIP string
	var workerIPs []string
	for _, d := range droplets {
		ip, _ := d.PublicIPv4()
		if ip == "" {
			continue
		}
		isMaster := false
		for _, t := range d.Tags {
			if t == computeMasterTag {
				isMaster = true
			}
		}
		if isMaster {
			masterIP = ip
		} else {
			workerIPs = append(workerIPs, ip)
		}
	}
	if masterIP == "" {
		return fmt.Errorf("no reachable control-plane droplet for cluster %s", name)
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

// scaleComputeWorkers scales the worker set of a compute cluster to the
// desired count: new droplets are prepared and kubeadm-joined; excess workers
// are drained and removed from the cluster before their droplets are deleted.
func (p *Provider) scaleComputeWorkers(ctx context.Context, clusterID string, desired int) error {
	name := computeClusterName(clusterID)
	if desired < 0 {
		return fmt.Errorf("desired worker count must be >= 0")
	}

	droplets, err := p.computeClusterDroplets(ctx, name)
	if err != nil {
		return err
	}
	var masterIP string
	var workers []godo.Droplet
	for _, d := range droplets {
		isMaster := false
		for _, t := range d.Tags {
			if t == computeMasterTag {
				isMaster = true
			}
		}
		if isMaster {
			masterIP, _ = d.PublicIPv4()
		} else {
			workers = append(workers, d)
		}
	}
	if masterIP == "" {
		return fmt.Errorf("no reachable control-plane droplet for cluster %s", name)
	}
	signer, err := provider.LoadClusterSSHKey(name)
	if err != nil {
		return err
	}

	current := len(workers)
	switch {
	case desired > current:
		key, _, err := p.ensureSSHKey(ctx, name)
		if err != nil {
			return err
		}
		vpcUUID, err := p.ensureComputeVPC(ctx, name)
		if err != nil {
			return err
		}
		userData := provider.KubeadmNodePrepScript(provider.KubeadmDefaultK8sMinor)
		joinCmd, err := provider.SSHRun(signer, computeSSHUser, masterIP, "kubeadm token create --print-join-command", 2*time.Minute)
		if err != nil {
			return fmt.Errorf("failed to create join token: %w", err)
		}
		joinCmd = strings.TrimSpace(joinCmd)
		tag := computeClusterTag(name)
		for i := current + 1; i <= desired; i++ {
			nodeName := fmt.Sprintf("adhar-%s-worker-%d", name, i)
			d, err := p.createComputeDroplet(ctx, nodeName, vpcUUID, key.ID, p.config.DropletSize, userData, []string{tag, computeWorkerTag})
			if err != nil {
				return err
			}
			ip, _ := d.PublicIPv4()
			if err := provider.WaitForNodePrep(ctx, signer, computeSSHUser, ip, 15*time.Minute); err != nil {
				return fmt.Errorf("new worker %s not ready: %w", nodeName, err)
			}
			privIP, _ := d.PrivateIPv4()
			if err := enableExternalCloudProvider(signer, ip, privIP); err != nil {
				return fmt.Errorf("new worker %s: %w", nodeName, err)
			}
			if err := provider.KubeadmJoinWorker(signer, computeSSHUser, ip, joinCmd); err != nil {
				return fmt.Errorf("new worker %s: %w", nodeName, err)
			}
		}
	case desired < current:
		// Remove the highest-indexed workers first: drain + remove from the
		// cluster on the control plane, then delete the droplet.
		for i := current - 1; i >= desired; i-- {
			d := workers[i]
			drain := fmt.Sprintf(
				"kubectl --kubeconfig /etc/kubernetes/admin.conf drain %[1]s --ignore-daemonsets --delete-emptydir-data --timeout=5m || true; "+
					"kubectl --kubeconfig /etc/kubernetes/admin.conf delete node %[1]s --ignore-not-found", d.Name)
			if out, err := provider.SSHRun(signer, computeSSHUser, masterIP, drain, 10*time.Minute); err != nil {
				log.Printf("Warning: drain of %s reported: %v (%s)", d.Name, err, provider.LastLines(out, 5))
			}
			if _, err := p.client.Droplets.Delete(ctx, d.ID); err != nil {
				return fmt.Errorf("failed to delete droplet %s: %w", d.Name, err)
			}
		}
	}

	log.Printf("Scaled cluster %q workers to %d", name, desired)
	return nil
}

// DigitalOcean cloud integration for self-managed clusters: the external
// cloud-controller-manager gives Service type=LoadBalancer real DO load
// balancers (required by the platform's cloud Gateway) and node lifecycle;
// the CSI driver provides the do-block-storage StorageClass every stateful
// platform component needs. Versions are pinned; manifests are applied on the
// control plane via kubectl so no local tooling is required.
const (
	doCCMManifestURL  = "https://raw.githubusercontent.com/digitalocean/digitalocean-cloud-controller-manager/master/releases/digitalocean-cloud-controller-manager/v0.1.62.yml"
	doCSIReleaseBase  = "https://raw.githubusercontent.com/digitalocean/csi-digitalocean/master/deploy/kubernetes/releases/csi-digitalocean-v4.14.0"
	kubectlAdminBase  = "kubectl --kubeconfig /etc/kubernetes/admin.conf"
	externalCloudFlag = "KUBELET_EXTRA_ARGS=--cloud-provider=external"
)

// enableExternalCloudProvider marks a node's kubelet for external cloud
// provider mode; must run before kubeadm init/join on that node. Nodes then
// carry the uninitialized taint until the CCM adopts them (Cilium's DaemonSet
// tolerates it, so the CNI still comes up first during platform bootstrap).
func enableExternalCloudProvider(signer ssh.Signer, ip, privateIP string) error {
	// --node-ip is required alongside external mode: without it the kubelet
	// registers no InternalIP until the CCM initializes the node, which
	// deadlocks scheduling (Cilium cannot start without a node IP, the CCM
	// cannot schedule until Cilium clears its taint) and breaks kubectl
	// logs/exec through the API server.
	flags := externalCloudFlag
	if privateIP != "" {
		flags += " --node-ip=" + privateIP
	}
	_, err := provider.SSHRun(signer, computeSSHUser, ip,
		"grep -q cloud-provider=external /etc/default/kubelet 2>/dev/null || { echo '"+flags+"' >> /etc/default/kubelet && systemctl restart kubelet; }", 2*time.Minute)
	return err
}

// installDOCloudIntegration applies the token secret, CCM, CSI driver and
// marks do-block-storage as the default StorageClass, all via the control
// plane's admin kubeconfig.
func (p *Provider) installDOCloudIntegration(signer ssh.Signer, masterIP, vpcUUID string) error {
	steps := []struct {
		desc string
		cmd  string
	}{
		{"cloud token secret", kubectlAdminBase + " -n kube-system get secret digitalocean >/dev/null 2>&1 || " +
			kubectlAdminBase + " -n kube-system create secret generic digitalocean --from-literal=access-token='" + p.token + "'"},
		{"cloud-controller-manager", kubectlAdminBase + " apply -f " + doCCMManifestURL},
		{"CSI CRDs", kubectlAdminBase + " apply -f " + doCSIReleaseBase + "/crds.yaml"},
		{"CSI driver", kubectlAdminBase + " apply -f " + doCSIReleaseBase + "/driver.yaml"},
		{"CSI snapshot controller", kubectlAdminBase + " apply -f " + doCSIReleaseBase + "/snapshot-controller.yaml"},
		// Without the cluster VPC pin the CCM creates load balancers in the
		// region's default VPC and droplet targeting fails with 422.
		{"CCM cluster VPC", kubectlAdminBase + " -n kube-system set env deploy/digitalocean-cloud-controller-manager DO_CLUSTER_VPC_ID=" + vpcUUID},
		{"default StorageClass", kubectlAdminBase + ` patch storageclass do-block-storage -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'`},
	}
	for _, st := range steps {
		if out, err := provider.SSHRun(signer, computeSSHUser, masterIP, st.cmd, 5*time.Minute); err != nil {
			return fmt.Errorf("cloud integration step %q failed: %w (output: %s)", st.desc, err, provider.LastLines(out, 8))
		}
	}
	log.Printf("DigitalOcean cloud integration installed (CCM %s, CSI %s)", "v0.1.62", "v4.14.0")
	return nil
}
