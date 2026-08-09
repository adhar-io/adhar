package provider

// Shared machinery for "compute mode" cluster creation: providers provision
// raw VMs from their cloud and adhar bootstraps Kubernetes on them with
// kubeadm — containerd runtime, kube-proxy skipped (the platform bootstrap
// installs Cilium with kubeProxyReplacement, exactly like the local Kind
// flow), and no CNI preinstalled. Nodes therefore report NotReady until the
// platform bootstrap installs Cilium; a cluster counts as created once the
// API server answers.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// KubeadmDefaultK8sMinor selects the pkgs.k8s.io package stream when the
	// cluster spec does not pin a Kubernetes version. Derived from the
	// platform-wide default in globals (currently v1.36.x).
	KubeadmDefaultK8sMinor = "1.36"

	// KubeadmCloudInitMarker is touched by the node-preparation script so the
	// provider knows a VM finished preparing before it drives kubeadm via SSH.
	KubeadmCloudInitMarker = "/var/lib/adhar/cloud-init-done"

	// KubeadmPodCIDR matches the pod network the platform's Cilium install
	// expects (same value the Kind flow uses).
	KubeadmPodCIDR = "10.244.0.0/16"
)

// K8sMinorFromVersion derives the pkgs.k8s.io minor stream ("1.34") from a
// requested version ("", "1.34", "1.34.2", "v1.34.2").
func K8sMinorFromVersion(requested string) string {
	v := strings.TrimPrefix(strings.TrimSpace(requested), "v")
	if v == "" {
		return KubeadmDefaultK8sMinor
	}
	parts := strings.Split(v, ".")
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		if strings.IndexFunc(parts[0]+parts[1], func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			return parts[0] + "." + parts[1]
		}
	}
	return KubeadmDefaultK8sMinor
}

// KubeadmNodePrepScript returns the VM user-data/startup script that prepares
// an Ubuntu VM as a Kubernetes node: containerd with the systemd cgroup
// driver, kubeadm/kubelet/kubectl from the pinned pkgs.k8s.io minor stream,
// swap off, kernel modules and sysctls, and the completion marker.
func KubeadmNodePrepScript(k8sMinor string) string {
	return fmt.Sprintf(`#!/bin/bash
set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive

# Kernel prerequisites
cat >/etc/modules-load.d/k8s.conf <<'EOF'
overlay
br_netfilter
EOF
modprobe overlay
modprobe br_netfilter
cat >/etc/sysctl.d/k8s.conf <<'EOF'
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF
sysctl --system

# Swap must be off for kubelet
swapoff -a
sed -i '/ swap / s/^/#/' /etc/fstab || true

apt-get update
apt-get install -y containerd apt-transport-https ca-certificates curl gpg

# containerd with the systemd cgroup driver
mkdir -p /etc/containerd
containerd config default >/etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl restart containerd
systemctl enable containerd

# kubeadm / kubelet / kubectl from the pinned minor stream
mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v%[1]s/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v%[1]s/deb/ /" >/etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

mkdir -p "$(dirname %[2]s)"
touch %[2]s
`, k8sMinor, KubeadmCloudInitMarker)
}

// ClusterStateDir returns (creating if needed) the local directory holding
// per-cluster state such as the SSH key used to drive kubeadm.
func ClusterStateDir(clusterName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".adhar", "clusters", clusterName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create cluster state dir %s: %w", dir, err)
	}
	return dir, nil
}

// EnsureClusterSSHKey loads the cluster's local ed25519 keypair, generating
// and persisting it on first use. It returns the signer plus the public key
// in authorized_keys format for registration with the cloud provider.
func EnsureClusterSSHKey(clusterName string) (ssh.Signer, string, error) {
	dir, err := ClusterStateDir(clusterName)
	if err != nil {
		return nil, "", err
	}
	keyPath := filepath.Join(dir, "id_ed25519")

	if data, err := os.ReadFile(keyPath); err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse existing cluster SSH key %s: %w", keyPath, err)
		}
		return signer, string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate SSH key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "adhar cluster "+clusterName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal SSH private key: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, "", fmt.Errorf("failed to write SSH private key %s: %w", keyPath, err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create SSH signer: %w", err)
	}
	return signer, string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
}

// LoadClusterSSHKey loads the cluster's local SSH key, failing when it does
// not exist (the cluster was created from another machine).
func LoadClusterSSHKey(clusterName string) (ssh.Signer, error) {
	dir, err := ClusterStateDir(clusterName)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "id_ed25519"))
	if err != nil {
		return nil, fmt.Errorf("cluster SSH key not found (was the cluster created from this machine?): %w", err)
	}
	return ssh.ParsePrivateKey(data)
}

// RemoveClusterState deletes the cluster's local state directory.
func RemoveClusterState(clusterName string) {
	if dir, err := ClusterStateDir(clusterName); err == nil {
		_ = os.RemoveAll(dir)
	}
}

// SSHRun executes a command on a VM and returns combined output. When user is
// not root the command is wrapped in sudo (cloud images typically disable
// direct root login).
func SSHRun(signer ssh.Signer, user, ip, command string, timeout time.Duration) (string, error) {
	if user != "root" {
		command = "sudo bash -c " + shellQuote(command)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106 -- hosts are freshly provisioned by us
		Timeout:         30 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(ip, "22"), cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", ip, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session %s: %w", ip, err)
	}
	defer session.Close()

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := session.CombinedOutput(command)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return string(r.out), fmt.Errorf("ssh %s: %w (output: %s)", ip, r.err, strings.TrimSpace(string(r.out)))
		}
		return string(r.out), nil
	case <-time.After(timeout):
		return "", fmt.Errorf("ssh %s: command timed out after %s", ip, timeout)
	}
}

// shellQuote single-quotes s for safe embedding in a shell command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// WaitForNodePrep waits until a VM accepts SSH and its preparation script
// finished (marker file present).
func WaitForNodePrep(ctx context.Context, signer ssh.Signer, user, ip string, deadline time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		out, err := SSHRun(signer, user, ip, "test -f "+KubeadmCloudInitMarker+" && echo ready", 30*time.Second)
		if err == nil && strings.Contains(out, "ready") {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("node %s did not finish preparation in %s (last error: %v)", ip, deadline, err)
		case <-time.After(10 * time.Second):
		}
	}
}

// KubeadmInitMaster runs kubeadm init on the control-plane node (idempotent —
// skipped when the node is already initialized) and returns the worker join
// command. kube-proxy is skipped: the platform bootstrap installs Cilium with
// kubeProxyReplacement.
func KubeadmInitMaster(signer ssh.Signer, user, publicIP, privateIP string) (string, error) {
	initCmd := fmt.Sprintf(
		"test -f /etc/kubernetes/admin.conf || kubeadm init "+
			"--pod-network-cidr=%s "+
			"--skip-phases=addon/kube-proxy "+
			"--control-plane-endpoint=%s "+
			"--apiserver-cert-extra-sans=%s,%s",
		KubeadmPodCIDR, publicIP, publicIP, privateIP)
	if out, err := SSHRun(signer, user, publicIP, initCmd, 15*time.Minute); err != nil {
		return "", fmt.Errorf("kubeadm init failed: %w (output: %s)", err, LastLines(out, 15))
	}

	// Cloud node hostnames are generally not DNS-resolvable by the API
	// server; prefer node IPs so `kubectl logs/exec` work. Static-pod edit is
	// idempotent (delete + single re-insert).
	addrFix := `grep -q kubelet-preferred-address-types /etc/kubernetes/manifests/kube-apiserver.yaml || sed -i 's|    - kube-apiserver|    - kube-apiserver\n    - --kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname|' /etc/kubernetes/manifests/kube-apiserver.yaml`
	if out, err := SSHRun(signer, user, publicIP, addrFix, 2*time.Minute); err != nil {
		return "", fmt.Errorf("failed to set apiserver kubelet address preference: %w (output: %s)", err, LastLines(out, 5))
	}

	joinCmd, err := SSHRun(signer, user, publicIP, "kubeadm token create --print-join-command", 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to create kubeadm join token: %w", err)
	}
	return strings.TrimSpace(joinCmd), nil
}

// KubeadmJoinWorker joins a worker node to the cluster (idempotent).
func KubeadmJoinWorker(signer ssh.Signer, user, ip, joinCmd string) error {
	if out, err := SSHRun(signer, user, ip, "test -f /etc/kubernetes/kubelet.conf || "+joinCmd, 10*time.Minute); err != nil {
		return fmt.Errorf("kubeadm join failed on %s: %w (output: %s)", ip, err, LastLines(out, 15))
	}
	return nil
}

// FetchAdminKubeconfig reads /etc/kubernetes/admin.conf from the control
// plane and rewrites loopback endpoints to the public IP.
func FetchAdminKubeconfig(signer ssh.Signer, user, masterIP string) (string, error) {
	out, err := SSHRun(signer, user, masterIP, "cat /etc/kubernetes/admin.conf", 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to fetch admin kubeconfig: %w", err)
	}
	return strings.ReplaceAll(out, "https://127.0.0.1:6443", fmt.Sprintf("https://%s:6443", masterIP)), nil
}

// LastLines returns the trailing n lines of s for error context.
func LastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// KubeadmUpgradeCluster performs an in-place minor/patch upgrade of a
// single-control-plane compute cluster: control plane first via
// `kubeadm upgrade apply`, then kubelet/kubectl on every node. Workers are
// upgraded via `kubeadm upgrade node` after the control plane. The package
// stream is switched to the target minor so apt can see the target version.
func KubeadmUpgradeCluster(ctx context.Context, signer ssh.Signer, user, masterIP string, workerIPs []string, targetVersion string) error {
	minor := K8sMinorFromVersion(targetVersion)
	repoSwitch := fmt.Sprintf(
		"curl -fsSL https://pkgs.k8s.io/core:/stable:/v%[1]s/deb/Release.key | gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg && "+
			"echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v%[1]s/deb/ /' >/etc/apt/sources.list.d/kubernetes.list && "+
			"apt-get update", minor)

	// Control plane: upgrade kubeadm, apply, then kubelet.
	steps := []string{
		repoSwitch,
		"apt-mark unhold kubeadm && apt-get install -y kubeadm && apt-mark hold kubeadm",
		fmt.Sprintf("kubeadm upgrade apply -y v%s", strings.TrimPrefix(targetVersion, "v")),
		"apt-mark unhold kubelet kubectl && apt-get install -y kubelet kubectl && apt-mark hold kubelet kubectl && systemctl restart kubelet",
	}
	for _, cmd := range steps {
		if out, err := SSHRun(signer, user, masterIP, cmd, 20*time.Minute); err != nil {
			return fmt.Errorf("control-plane upgrade step failed: %w (output: %s)", err, LastLines(out, 15))
		}
	}

	// Workers: upgrade kubeadm, node config, then kubelet.
	for _, ip := range workerIPs {
		wSteps := []string{
			repoSwitch,
			"apt-mark unhold kubeadm && apt-get install -y kubeadm && apt-mark hold kubeadm",
			"kubeadm upgrade node",
			"apt-mark unhold kubelet kubectl && apt-get install -y kubelet kubectl && apt-mark hold kubelet kubectl && systemctl restart kubelet",
		}
		for _, cmd := range wSteps {
			if out, err := SSHRun(signer, user, ip, cmd, 20*time.Minute); err != nil {
				return fmt.Errorf("worker %s upgrade step failed: %w (output: %s)", ip, err, LastLines(out, 15))
			}
		}
	}
	return nil
}
