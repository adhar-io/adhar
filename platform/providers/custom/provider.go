package custom

// Bring-your-own-hosts provider: the user supplies existing Linux machines
// (bare metal or VMs) reachable over SSH with their own key, and adhar
// bootstraps Kubernetes on them with kubeadm via the shared compute helpers —
// containerd runtime, kube-proxy skipped (the platform bootstrap installs
// Cilium with kubeProxyReplacement, exactly like the local Kind flow), and no
// CNI preinstalled. Nodes therefore report NotReady until the platform
// bootstrap installs Cilium; the cluster counts as created once the API
// server answers. The machines themselves are never created or destroyed:
// DeleteCluster only runs `kubeadm reset` on them.

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"

	"golang.org/x/crypto/ssh"
)

const (
	// Hosts are not cloud VMs with user-data, so the node-preparation script
	// runs synchronously over SSH; apt installs dominate the runtime.
	nodePrepTimeout = 20 * time.Minute

	// defaultClusterName names the (single) cluster this provider manages when
	// it has to be discovered rather than created in this process.
	defaultClusterName = "on-premises-cluster"
)

// Register the Custom provider on package import
func init() {
	provider.DefaultFactory.RegisterProvider("custom", func(config map[string]interface{}) (provider.Provider, error) {
		if managed, ok := config["useManagedK8s"].(bool); ok && managed {
			return nil, fmt.Errorf("useManagedK8s is not applicable to the custom provider: bring-your-own hosts are always self-managed")
		}
		customConfig := &Config{}

		customConfig.MasterIPs = toStringSlice(config["masterIPs"])
		customConfig.WorkerIPs = toStringSlice(config["workerIPs"])
		customConfig.NodeIPs = toStringSlice(config["nodeIPs"])
		if user, ok := config["sshUser"].(string); ok {
			customConfig.SSHUser = user
		}
		// Legacy key name for the SSH user.
		if customConfig.SSHUser == "" {
			if user, ok := config["username"].(string); ok {
				customConfig.SSHUser = user
			}
		}
		if keyPath, ok := config["sshKeyPath"].(string); ok {
			customConfig.SSHKeyPath = keyPath
		}
		switch port := config["sshPort"].(type) {
		case int:
			customConfig.SSHPort = port
		case float64:
			customConfig.SSHPort = int(port)
		}

		return NewProvider(customConfig)
	})
}

// toStringSlice converts a config value ([]interface{} or []string) to a
// string slice, dropping empty entries.
func toStringSlice(v interface{}) []string {
	var out []string
	appendIfSet := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	switch vals := v.(type) {
	case []string:
		for _, s := range vals {
			appendIfSet(s)
		}
	case []interface{}:
		for _, item := range vals {
			if s, ok := item.(string); ok {
				appendIfSet(s)
			}
		}
	}
	return out
}

// Provider implements the Custom provider for bring-your-own hosts
// (on-premises bare metal / existing VMs).
type Provider struct {
	config *Config
	signer ssh.Signer // lazily loaded from Config.SSHKeyPath
}

// Config holds Custom provider configuration. The hosts already exist and are
// reachable over SSH on port 22 with the user's own private key; adhar never
// provisions or deletes them.
type Config struct {
	MasterIPs  []string `json:"masterIPs"`  // Control-plane host IPs; exactly one is supported
	WorkerIPs  []string `json:"workerIPs"`  // Worker host IPs
	SSHUser    string   `json:"sshUser"`    // SSH user (root, or a user with passwordless sudo); legacy key: "username"
	SSHKeyPath string   `json:"sshKeyPath"` // Path to the user's passphrase-less SSH private key
	SSHPort    int      `json:"sshPort"`    // Legacy; only 22 is supported

	// NodeIPs is the legacy host list: the first entry is the master and the
	// rest are workers. Used only when MasterIPs is empty.
	NodeIPs []string `json:"nodeIPs"`
}

// NewProvider creates a new Custom provider instance
func NewProvider(config *Config) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("Custom provider configuration is required")
	}

	// Legacy layout: nodeIPs = master followed by workers.
	if len(config.MasterIPs) == 0 && len(config.NodeIPs) > 0 {
		config.MasterIPs = config.NodeIPs[:1]
		config.WorkerIPs = append(config.WorkerIPs, config.NodeIPs[1:]...)
	}

	if len(config.MasterIPs) == 0 {
		return nil, fmt.Errorf("at least one control-plane host IP is required (masterIPs)")
	}
	if len(config.MasterIPs) > 1 {
		return nil, fmt.Errorf("exactly one control-plane host is supported; HA control planes require a load balancer and stacked etcd and are not implemented yet")
	}
	if config.SSHUser == "" {
		return nil, fmt.Errorf("SSH user is required (sshUser)")
	}
	if config.SSHKeyPath == "" {
		return nil, fmt.Errorf("SSH private key path is required (sshKeyPath); the custom provider connects to your hosts with your own key")
	}
	if config.SSHPort != 0 && config.SSHPort != 22 {
		return nil, fmt.Errorf("only SSH port 22 is supported (got %d)", config.SSHPort)
	}

	// Expand a leading ~ in the key path.
	if strings.HasPrefix(config.SSHKeyPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve home directory for SSH key path %s: %w", config.SSHKeyPath, err)
		}
		config.SSHKeyPath = filepath.Join(home, strings.TrimPrefix(config.SSHKeyPath, "~"))
	}

	return &Provider{config: config}, nil
}

// Name returns the provider name
func (p *Provider) Name() string {
	return "custom"
}

// Region returns the provider region
func (p *Provider) Region() string {
	return "on-premises"
}

// masterIP returns the (single) control-plane host IP.
func (p *Provider) masterIP() string {
	return p.config.MasterIPs[0]
}

// allIPs returns the master followed by all workers.
func (p *Provider) allIPs() []string {
	return append([]string{p.masterIP()}, p.config.WorkerIPs...)
}

// sshSigner loads (once) the user's private key configured via sshKeyPath.
func (p *Provider) sshSigner() (ssh.Signer, error) {
	if p.signer != nil {
		return p.signer, nil
	}
	data, err := os.ReadFile(p.config.SSHKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH private key %s: %w", p.config.SSHKeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, fmt.Errorf("SSH key %s is passphrase-protected; the custom provider only supports passphrase-less keys", p.config.SSHKeyPath)
		}
		return nil, fmt.Errorf("failed to parse SSH private key %s: %w", p.config.SSHKeyPath, err)
	}
	p.signer = signer
	return signer, nil
}

// run executes a command on a host via the shared SSH helper (which wraps the
// command in sudo when the user is not root).
func (p *Provider) run(ip, command string, timeout time.Duration) (string, error) {
	signer, err := p.sshSigner()
	if err != nil {
		return "", err
	}
	return provider.SSHRun(signer, p.config.SSHUser, ip, command, timeout)
}

// Authenticate validates SSH connectivity to every configured host.
func (p *Provider) Authenticate(ctx context.Context, credentials *types.Credentials) error {
	for _, ip := range p.allIPs() {
		out, err := p.run(ip, "echo connection-ok", 30*time.Second)
		if err != nil {
			return fmt.Errorf("failed to connect to host %s as %s: %w", ip, p.config.SSHUser, err)
		}
		if !strings.Contains(out, "connection-ok") {
			return fmt.Errorf("unexpected output from host %s", ip)
		}
	}
	return nil
}

// ValidatePermissions checks that we get root on every host (directly, or via
// passwordless sudo — SSHRun wraps commands in sudo for non-root users).
func (p *Provider) ValidatePermissions(ctx context.Context) error {
	for _, ip := range p.allIPs() {
		out, err := p.run(ip, "id -u", 30*time.Second)
		if err != nil {
			return fmt.Errorf("root access check failed on host %s (is passwordless sudo configured for %s?): %w", ip, p.config.SSHUser, err)
		}
		if strings.TrimSpace(out) != "0" {
			return fmt.Errorf("commands on host %s do not run as root (got uid %s); configure passwordless sudo for %s", ip, strings.TrimSpace(out), p.config.SSHUser)
		}
	}
	return nil
}

// prepareHost runs the shared kubeadm node-preparation script on a host. The
// hosts are not cloud VMs with user-data, so the script is uploaded (base64
// over SSH stdin-free) and executed directly; the completion marker makes it
// idempotent.
func (p *Provider) prepareHost(ip, k8sMinor string) error {
	script := provider.KubeadmNodePrepScript(k8sMinor)
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	cmd := fmt.Sprintf(
		"test -f %[1]s || { echo %[2]s | base64 -d >/tmp/adhar-node-prep.sh && bash /tmp/adhar-node-prep.sh; }",
		provider.KubeadmCloudInitMarker, encoded)
	if out, err := p.run(ip, cmd, nodePrepTimeout); err != nil {
		return fmt.Errorf("node preparation failed on %s: %w (output: %s)", ip, err, provider.LastLines(out, 15))
	}
	return nil
}

// CreateCluster bootstraps Kubernetes with kubeadm on the configured hosts:
// node preparation on every host, kubeadm init on the master, kubeadm join on
// every worker. All steps are idempotent, so a failed run can be retried.
func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Provider != "custom" {
		return nil, fmt.Errorf("provider mismatch: expected custom, got %s", spec.Provider)
	}
	if spec.ControlPlane.Replicas > 1 || spec.ControlPlane.HighAvailability {
		return nil, fmt.Errorf("the custom provider currently supports a single control-plane node; HA control planes require a load balancer and stacked etcd and are not implemented yet")
	}

	signer, err := p.sshSigner()
	if err != nil {
		return nil, err
	}

	masterIP := p.masterIP()
	log.Printf("Bootstrapping Kubernetes with kubeadm on user-supplied hosts: master %s, %d worker(s)", masterIP, len(p.config.WorkerIPs))

	k8sMinor := provider.K8sMinorFromVersion(spec.Version)
	for _, ip := range p.allIPs() {
		log.Printf("Preparing host %s (containerd + kubeadm %s)", ip, k8sMinor)
		if err := p.prepareHost(ip, k8sMinor); err != nil {
			return nil, err
		}
	}

	joinCmd, err := provider.KubeadmInitMaster(signer, p.config.SSHUser, masterIP, masterIP)
	if err != nil {
		return nil, err
	}

	for _, ip := range p.config.WorkerIPs {
		if err := provider.KubeadmJoinWorker(signer, p.config.SSHUser, ip, joinCmd); err != nil {
			return nil, fmt.Errorf("worker %s: %w", ip, err)
		}
	}

	cluster := &types.Cluster{
		ID:        fmt.Sprintf("custom-%s", spec.Name),
		Name:      spec.Name,
		Provider:  "custom",
		Region:    "on-premises",
		Version:   k8sMinor,
		Status:    types.ClusterStatusRunning,
		Endpoint:  fmt.Sprintf("https://%s:6443", masterIP),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"mode":        "byo-kubeadm",
			"masterIP":    masterIP,
			"workerNodes": len(p.config.WorkerIPs),
		},
	}

	log.Printf("Cluster %q is up: API %s (%d workers). Nodes stay NotReady until the platform bootstrap installs Cilium.", spec.Name, cluster.Endpoint, len(p.config.WorkerIPs))
	return cluster, nil
}

// DeleteCluster removes Kubernetes from every host with `kubeadm reset`
// (best-effort). The machines are user-supplied and are never deleted.
// Firewall/iptables rules are deliberately left untouched.
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	name := extractClusterName(clusterID)
	log.Printf("Removing Kubernetes from the hosts of cluster %q (the machines themselves are kept)", name)

	resetCmd := "kubeadm reset -f && rm -rf /etc/cni/net.d"
	// Workers first, master last, so the control plane can still answer while
	// workers leave.
	hosts := append(append([]string{}, p.config.WorkerIPs...), p.masterIP())
	for _, ip := range hosts {
		if out, err := p.run(ip, resetCmd, 10*time.Minute); err != nil {
			log.Printf("Warning: kubeadm reset on host %s failed: %v (%s)", ip, err, provider.LastLines(out, 5))
		}
	}

	log.Printf("Cluster %q removed from hosts", name)
	return nil
}

// UpdateCluster supports in-place version upgrades; the host set is fixed by
// configuration and cannot be changed here.
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	for _, ng := range spec.NodeGroups {
		if ng.Replicas != len(p.config.WorkerIPs) {
			return fmt.Errorf("the custom provider cannot scale node groups: the hosts are user-supplied; update workerIPs in the provider configuration and re-run cluster creation to join new hosts")
		}
	}
	if spec.Version != "" {
		return p.UpgradeCluster(ctx, clusterID, spec.Version)
	}
	return nil
}

// GetCluster reports the cluster as seen from the control-plane host.
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	name := extractClusterName(clusterID)
	masterIP := p.masterIP()

	out, err := p.run(masterIP, "kubectl --kubeconfig /etc/kubernetes/admin.conf get nodes --no-headers", 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("cluster %s not accessible on %s: %w", name, masterIP, err)
	}

	nodeCount, readyCount, version := parseNodeLines(out)

	return &types.Cluster{
		ID:        clusterID,
		Name:      name,
		Provider:  "custom",
		Region:    "on-premises",
		Version:   version,
		Status:    types.ClusterStatusRunning,
		Endpoint:  fmt.Sprintf("https://%s:6443", masterIP),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"mode":       "byo-kubeadm",
			"masterIP":   masterIP,
			"nodeCount":  nodeCount,
			"readyNodes": readyCount,
		},
	}, nil
}

// parseNodeLines parses `kubectl get nodes --no-headers` output
// (NAME STATUS ROLES AGE VERSION) into totals and the server version.
func parseNodeLines(out string) (nodeCount, readyCount int, version string) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		nodeCount++
		if fields[1] == "Ready" {
			readyCount++
		}
		if version == "" && len(fields) >= 5 {
			version = fields[4]
		}
	}
	return nodeCount, readyCount, version
}

// ListClusters reports the single cluster on the configured hosts, when the
// control plane has been initialized.
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	out, err := p.run(p.masterIP(), "test -f /etc/kubernetes/admin.conf && echo initialized", 30*time.Second)
	if err != nil || !strings.Contains(out, "initialized") {
		return []*types.Cluster{}, nil
	}

	cluster, err := p.GetCluster(ctx, fmt.Sprintf("custom-%s", defaultClusterName))
	if err != nil {
		return []*types.Cluster{}, nil
	}
	return []*types.Cluster{cluster}, nil
}

// GetKubeconfig fetches the admin kubeconfig from the control-plane host and
// rewrites loopback endpoints to the master IP.
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	signer, err := p.sshSigner()
	if err != nil {
		return "", err
	}
	return provider.FetchAdminKubeconfig(signer, p.config.SSHUser, p.masterIP())
}

// AddNodeGroup is not supported: the hosts are user-supplied.
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	return nil, fmt.Errorf("the custom provider cannot add node groups: add the new host IPs to workerIPs in the provider configuration and re-run cluster creation to join them")
}

// RemoveNodeGroup is not supported: the hosts are user-supplied.
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	return fmt.Errorf("the custom provider cannot remove node groups: drain the nodes, run `kubeadm reset` on them, and remove their IPs from workerIPs")
}

// ScaleNodeGroup is not supported: the hosts are user-supplied.
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	return fmt.Errorf("the custom provider cannot scale node groups: the hosts are user-supplied; update workerIPs in the provider configuration")
}

// GetNodeGroup reports the single configured worker group.
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	if nodeGroupName != "workers" {
		return nil, fmt.Errorf("node group %s not found (the custom provider has a single %q group from workerIPs)", nodeGroupName, "workers")
	}
	return &types.NodeGroup{
		Name:         "workers",
		Replicas:     len(p.config.WorkerIPs),
		InstanceType: "byo-host",
		Status:       types.NodeGroupStatusReady,
		UpdatedAt:    time.Now(),
	}, nil
}

// ListNodeGroups reports the single configured worker group.
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	group, err := p.GetNodeGroup(ctx, clusterID, "workers")
	if err != nil {
		return nil, err
	}
	return []*types.NodeGroup{group}, nil
}

// CreateVPC is not supported: networking of user-supplied hosts is managed by the user.
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	return nil, fmt.Errorf("not supported for the custom provider: networking of user-supplied hosts is managed by the user")
}

// DeleteVPC is not supported: networking of user-supplied hosts is managed by the user.
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	return fmt.Errorf("not supported for the custom provider: networking of user-supplied hosts is managed by the user")
}

// GetVPC is not supported: networking of user-supplied hosts is managed by the user.
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	return nil, fmt.Errorf("not supported for the custom provider: networking of user-supplied hosts is managed by the user")
}

// CreateLoadBalancer is not supported: load balancing for user-supplied hosts is managed by the user.
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	return nil, fmt.Errorf("not supported for the custom provider: load balancing for user-supplied hosts is managed by the user")
}

// DeleteLoadBalancer is not supported: load balancing for user-supplied hosts is managed by the user.
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	return fmt.Errorf("not supported for the custom provider: load balancing for user-supplied hosts is managed by the user")
}

// GetLoadBalancer is not supported: load balancing for user-supplied hosts is managed by the user.
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	return nil, fmt.Errorf("not supported for the custom provider: load balancing for user-supplied hosts is managed by the user")
}

// CreateStorage is not supported: storage for user-supplied hosts is managed by the user.
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	return nil, fmt.Errorf("not supported for the custom provider: storage for user-supplied hosts is managed by the user")
}

// DeleteStorage is not supported: storage for user-supplied hosts is managed by the user.
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	return fmt.Errorf("not supported for the custom provider: storage for user-supplied hosts is managed by the user")
}

// GetStorage is not supported: storage for user-supplied hosts is managed by the user.
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	return nil, fmt.Errorf("not supported for the custom provider: storage for user-supplied hosts is managed by the user")
}

// UpgradeCluster performs an in-place kubeadm upgrade: control plane first,
// then every worker.
func (p *Provider) UpgradeCluster(ctx context.Context, clusterID string, version string) error {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if strings.Count(v, ".") < 2 {
		return fmt.Errorf("a full target version is required for kubeadm upgrades (e.g. 1.34.2), got %q", version)
	}

	signer, err := p.sshSigner()
	if err != nil {
		return err
	}
	log.Printf("Upgrading cluster %s to %s via kubeadm", extractClusterName(clusterID), version)
	if err := provider.KubeadmUpgradeCluster(ctx, signer, p.config.SSHUser, p.masterIP(), p.config.WorkerIPs, version); err != nil {
		return fmt.Errorf("kubeadm upgrade failed: %w", err)
	}
	log.Printf("Successfully upgraded cluster %s to %s", extractClusterName(clusterID), version)
	return nil
}

// BackupCluster is not implemented for bring-your-own hosts.
func (p *Provider) BackupCluster(ctx context.Context, clusterID string) (*types.Backup, error) {
	return nil, fmt.Errorf("not implemented")
}

// RestoreCluster is not implemented for bring-your-own hosts.
func (p *Provider) RestoreCluster(ctx context.Context, backupID string, targetClusterID string) error {
	return fmt.Errorf("not implemented")
}

// GetClusterHealth summarizes real node health as reported by the API server
// on the control-plane host.
func (p *Provider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	masterIP := p.masterIP()
	components := map[string]types.ComponentHealth{}

	out, err := p.run(masterIP, "kubectl --kubeconfig /etc/kubernetes/admin.conf get nodes --no-headers", 2*time.Minute)
	if err != nil {
		components["api-server"] = types.ComponentHealth{
			Status:  "unhealthy",
			Message: fmt.Sprintf("API server on %s not reachable: %v", masterIP, err),
		}
		return &types.HealthStatus{Status: "unhealthy", Components: components, LastCheck: time.Now()}, nil
	}
	components["api-server"] = types.ComponentHealth{Status: "healthy", Message: "API server is answering"}

	nodeCount, readyCount, _ := parseNodeLines(out)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, status := fields[0], fields[1]
		if status == "Ready" {
			components["node/"+name] = types.ComponentHealth{Status: "healthy", Message: "Ready"}
		} else {
			components["node/"+name] = types.ComponentHealth{
				Status:  "unhealthy",
				Message: fmt.Sprintf("%s (NotReady is expected until the platform bootstrap installs Cilium)", status),
			}
		}
	}

	status := "healthy"
	if readyCount < nodeCount || nodeCount == 0 {
		status = "unhealthy"
	}
	components["nodes"] = types.ComponentHealth{
		Status:  status,
		Message: fmt.Sprintf("%d/%d nodes Ready", readyCount, nodeCount),
	}

	return &types.HealthStatus{Status: status, Components: components, LastCheck: time.Now()}, nil
}

// GetClusterMetrics is not implemented for bring-your-own hosts.
func (p *Provider) GetClusterMetrics(ctx context.Context, clusterID string) (*types.Metrics, error) {
	return nil, fmt.Errorf("not implemented")
}

// InstallAddon is not supported: platform packages are deployed by the adhar
// GitOps bootstrap (ArgoCD), not by the provider.
func (p *Provider) InstallAddon(ctx context.Context, clusterID string, addonName string, config map[string]interface{}) error {
	return fmt.Errorf("not supported for the custom provider: platform packages are deployed by the adhar GitOps bootstrap")
}

// UninstallAddon is not supported: platform packages are deployed by the adhar
// GitOps bootstrap (ArgoCD), not by the provider.
func (p *Provider) UninstallAddon(ctx context.Context, clusterID string, addonName string) error {
	return fmt.Errorf("not supported for the custom provider: platform packages are deployed by the adhar GitOps bootstrap")
}

// ListAddons is not supported: platform packages are deployed by the adhar
// GitOps bootstrap (ArgoCD), not by the provider.
func (p *Provider) ListAddons(ctx context.Context, clusterID string) ([]string, error) {
	return nil, fmt.Errorf("not supported for the custom provider: platform packages are deployed by the adhar GitOps bootstrap")
}

// GetClusterCost reports zero: the infrastructure is user-owned.
func (p *Provider) GetClusterCost(ctx context.Context, clusterID string) (float64, error) {
	return 0.0, nil
}

// GetCostBreakdown reports zero: the infrastructure is user-owned.
func (p *Provider) GetCostBreakdown(ctx context.Context, clusterID string) (map[string]float64, error) {
	return map[string]float64{"infrastructure": 0.0}, nil
}

// InvestigateCluster is not implemented for the custom provider.
func (p *Provider) InvestigateCluster(ctx context.Context, clusterID string) error {
	return fmt.Errorf("cluster investigation not yet implemented for Custom provider")
}

// extractClusterName strips the "custom-" ID prefix.
func extractClusterName(clusterID string) string {
	return strings.TrimPrefix(clusterID, "custom-")
}
