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
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/types"

	"golang.org/x/crypto/ssh"
)

// KubeadmDefaultK8sMinor selects the pkgs.k8s.io package stream when the
// cluster spec does not pin a Kubernetes version. It is DERIVED from the
// platform-wide default (globals.DefaultKubernetesVersion) rather than typed
// here: the two were once maintained by hand and drifted — globals said
// v1.37.0 while this stayed "1.36" — so a DigitalOcean install came up on the
// latest 1.36 patch while Kind ran 1.37. With the derivation, bumping the one
// constant in globals moves every provider, and apt installs the newest patch
// of that minor on each node at provisioning time.
var KubeadmDefaultK8sMinor = minorOf(globals.DefaultKubernetesVersion, kubeadmFallbackK8sMinor)

// kubeadmFallbackK8sMinor is used only if globals carries an unparseable
// version; it must never be the value anyone edits to change the platform.
const kubeadmFallbackK8sMinor = "1.37"

// minorOf extracts "MAJOR.MINOR" from "v1.37.0" / "1.37" / "1.37.2", or
// returns fallback when the input has no numeric major.minor.
func minorOf(version, fallback string) string {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.Split(v, ".")
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		if strings.IndexFunc(parts[0]+parts[1], func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			return parts[0] + "." + parts[1]
		}
	}
	return fallback
}

const (
	// KubeadmCloudInitMarker is touched by the node-preparation script so the
	// provider knows a VM finished preparing before it drives kubeadm via SSH.
	KubeadmCloudInitMarker = "/var/lib/adhar/cloud-init-done"
)

// CriticalPathImages are the images the platform bootstrap needs before ANY
// workload can schedule: the Cilium CNI and its Envoy/operator. Until they are
// on the node, every pod stays Pending, so on a cold node their download is
// pure added latency on `adhar up`.
//
// Node prep pre-pulls them in the background (see KubeadmNodePrepScript) so the
// download overlaps `kubeadm join` instead of following it, and the Kind
// provider seeds the same set from the host cache. Keep in sync with
// platform/controllers/adharplatform/resources/cilium/install.yaml.
var CriticalPathImages = []string{
	"quay.io/cilium/cilium:v1.20.0",
	"quay.io/cilium/cilium-envoy:v1.37.5-1782911245-7cffc778c923f68a77954a53b1a98d6b5353f004",
	"quay.io/cilium/operator-generic:v1.20.0",
}

const (

	// KubeadmPodCIDR matches the pod network the platform's Cilium install
	// expects (same value the Kind flow uses). It is only the default: a
	// cluster meant to join a Cilium Cluster Mesh must be initialised with a
	// Pod CIDR that does not overlap any of its peers, so the environment's
	// `podCIDR` clusterConfig entry wins when it is set.
	KubeadmPodCIDR = "10.244.0.0/16"
)

// PodCIDROrDefault returns the Pod CIDR a cluster spec asks for, or the
// platform default when it says nothing.
func PodCIDROrDefault(spec *types.ClusterSpec) string {
	if spec != nil && spec.Networking.PodCIDR != "" {
		return spec.Networking.PodCIDR
	}
	return KubeadmPodCIDR
}

// K8sMinorFromVersion derives the pkgs.k8s.io minor stream ("1.34") from a
// requested version ("", "1.34", "1.34.2", "v1.34.2").
func K8sMinorFromVersion(requested string) string {
	return minorOf(requested, KubeadmDefaultK8sMinor)
}

// KubeadmNodePrepScript returns the VM user-data/startup script that prepares
// an Ubuntu VM as a Kubernetes node: containerd with the systemd cgroup
// driver, kubeadm/kubelet/kubectl from the pinned pkgs.k8s.io minor stream,
// swap off, kernel modules and sysctls, and the completion marker.
func KubeadmNodePrepScript(k8sMinor string) string {
	return fmt.Sprintf(`#!/bin/bash
set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive

# Make the machine's own hostname resolvable.
#
# Without this every single sudo prints
#   sudo: unable to resolve host <name>: Name or service not known
# on STDERR, and that line then contaminates anything that captures a command's
# output. It corrupted the fetched kubeconfig — the warning landed above
# "apiVersion: v1" and the platform bootstrap died on
# "yaml: mapping values are not allowed in this context" — after a cluster had
# been built successfully (Azure, 2026-09-26). Cloud images that do not seed
# /etc/hosts from the instance name hit this; it costs one line to prevent.
HOSTNAME_SELF="$(hostname)"
if ! grep -qE "[[:space:]]${HOSTNAME_SELF}([[:space:]]|$)" /etc/hosts; then
  echo "127.0.1.1 ${HOSTNAME_SELF}" >>/etc/hosts
fi

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
# Kubernetes nodes run dozens of controllers, each holding inotify instances
# and watches. The kernel defaults (128 instances / 8192 watches) are exhausted
# on a dense platform node and controller-runtime managers then die with
# "too many open files" (observed: chaos-mesh restarting ~60 times).
fs.inotify.max_user_instances       = 8192
fs.inotify.max_user_watches         = 524288
fs.file-max                         = 2097152
EOF
sysctl --system

# Swap must be off for kubelet
swapoff -a
sed -i '/ swap / s/^/#/' /etc/fstab || true

# Resilient node DNS. Cloud images point /etc/resolv.conf at the
# systemd-resolved stub (127.0.0.53). The stub is a single local daemon: when
# it is unavailable — it is among the first processes the kernel OOM-killer
# reaps on a loaded node — every containerd image pull fails with
# "connection refused on 127.0.0.53" and pods across the cluster get stuck in
# ImagePullBackOff even though the network is fine. Pin /etc/resolv.conf to the
# real upstream resolvers instead (taken from resolved's own upstream list, so
# the cloud/on-prem provider's servers are preserved, with public resolvers
# only as a last resort) so image pulls never depend on a local daemon.
# CoreDNS forwards to this file too, so cluster DNS gains the same resilience.
upstreams=""
if [ -s /run/systemd/resolve/resolv.conf ]; then
  upstreams=$(grep -E '^nameserver' /run/systemd/resolve/resolv.conf | awk '{print $2}' | grep -v '^127\.' || true)
fi
if [ -z "$upstreams" ]; then
  upstreams=$(grep -E '^nameserver' /etc/resolv.conf 2>/dev/null | awk '{print $2}' | grep -v '^127\.' || true)
fi
if [ -z "$upstreams" ]; then
  upstreams="1.1.1.1 8.8.8.8"
fi
{
  echo "# Managed by adhar (node prep): static upstream resolvers so container"
  echo "# image pulls do not depend on the local systemd-resolved stub."
  for ns in $upstreams; do echo "nameserver $ns"; done
  echo "options timeout:2 attempts:3"
} >/etc/resolv.conf.adhar
rm -f /etc/resolv.conf
mv /etc/resolv.conf.adhar /etc/resolv.conf
# Keep resolved running for the rest of the system, with the same upstreams.
mkdir -p /etc/systemd/resolved.conf.d
{
  echo "[Resolve]"
  echo "DNS=$(echo $upstreams | tr '\n' ' ')"
  echo "FallbackDNS=1.1.1.1 8.8.8.8"
} >/etc/systemd/resolved.conf.d/adhar.conf
systemctl restart systemd-resolved 2>/dev/null || true

apt-get update
apt-get install -y containerd apt-transport-https ca-certificates curl gpg

# systemd limits. containerd runs every container as a transient systemd scope
# ("cri-containerd-<id>.scope"); on a dense node the defaults run out and pods
# fail to start with "unable to apply cgroup configuration: unable to start
# unit" / FailedCreatePodContainer, which looks like an image or app fault but
# is the node refusing to create the cgroup.
mkdir -p /etc/systemd/system.conf.d
cat >/etc/systemd/system.conf.d/adhar.conf <<'EOF'
[Manager]
DefaultTasksMax=infinity
DefaultLimitNOFILE=1048576:1048576
EOF
systemctl daemon-reexec || true

# containerd with the systemd cgroup driver
mkdir -p /etc/containerd
containerd config default >/etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
# Per-registry host config (certs.d) for BOTH pull paths of containerd 2.x.
# The CRI images plugin already defaults config_path to certs.d, but pulls
# go through the transfer service (use_local_image_pull = false), whose own
# config_path is empty — so a hosts.toml/CA dropped into certs.d (the
# harbor package's node DaemonSet, for kpack-built images on the in-cluster
# Harbor) was silently ignored and every such pull failed on DNS/TLS
# (2026-09-15 DO bring-up). Set it on every table that carries the key.
sed -i "s|config_path = ''|config_path = '/etc/containerd/certs.d'|" /etc/containerd/config.toml
# And let the CRI plugin pull images itself (the classic, certs.d-honouring
# path) instead of delegating to the transfer service: on a node provisioned
# with only the config_path change above, a pull still resolved the original
# registry host by DNS and ignored the hosts.toml mirror (2026-09-15).
sed -i 's/use_local_image_pull = false/use_local_image_pull = true/' /etc/containerd/config.toml
# Concurrent layer downloads: containerd defaults to 3, which becomes the new
# bottleneck the moment the kubelet stops serialising pulls (TuneImagePulls). A
# production profile pulls ~70 images per node, so the queue — not the bandwidth —
# is what makes a bring-up slow.
sed -i 's/max_concurrent_downloads = 3/max_concurrent_downloads = 8/' /etc/containerd/config.toml
mkdir -p /etc/containerd/certs.d
systemctl restart containerd
systemctl enable containerd

# kubeadm / kubelet / kubectl from the pinned minor stream
mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v%[1]s/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v%[1]s/deb/ /" >/etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

# Pre-pull the CNI images the platform bootstrap cannot start without. Detached
# and best-effort: node prep returns immediately, the download overlaps the
# kubeadm join and the control plane coming up, and a failure here only means
# the kubelet pulls them later exactly as before. --hosts-dir keeps the certs.d
# mirrors (in-cluster Harbor, any registry mirror) in effect.
nohup sh -c 'for img in %[3]s; do ctr -n k8s.io images pull --hosts-dir /etc/containerd/certs.d "$img" >/dev/null 2>&1 || true; done' >/dev/null 2>&1 &

mkdir -p "$(dirname %[2]s)"
touch %[2]s
`, k8sMinor, KubeadmCloudInitMarker, strings.Join(CriticalPathImages, " "))
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

// ApplyAPIServerExtraArgs adds flags to the kube-apiserver static pod manifest.
//
// `kubeadm init` takes arbitrary apiserver flags only through a config file, and
// this bootstrap deliberately drives kubeadm from the command line, so the flags
// are inserted into /etc/kubernetes/manifests/kube-apiserver.yaml afterwards —
// the same technique already used above for --kubelet-preferred-address-types.
// The kubelet notices the manifest change and restarts the static pod.
//
// Idempotent per flag: an arg whose NAME is already present is left alone, so a
// re-run never duplicates it and never fights an operator's manual edit. Keys
// are sorted so a re-run produces a byte-identical manifest.
//
// Safety note for the OIDC case: kube-apiserver does not require the OIDC issuer
// to be reachable at startup (the authenticator fetches and refreshes JWKS
// lazily, with retries), which is what makes it safe to set these during
// bootstrap even though Keycloak is installed later in the same run. A wrong or
// permanently unreachable issuer therefore degrades OIDC logins, and does NOT
// prevent the API server from serving certificate-authenticated clients.
// apiServerFlagCommand builds the idempotent shell one-liner that inserts a
// single flag into the kube-apiserver static pod manifest.
//
// Extracted so it can be unit-tested: the `\n` below must reach sed as
// BACKSLASH-n, which in a double-quoted Go literal has to be written `\\n`.
// Written as `\n` it compiles to a real newline, splitting the sed script over
// two lines, and the node answers
//
//	sed: -e expression #1, char 43: unterminated `s' command
//
// — which aborts `adhar up` only AFTER the droplets have been created, i.e. the
// most expensive possible moment to discover a quoting slip. TestAPIServerFlagCommand
// pins it.
func apiServerFlagCommand(name, value string) string {
	const manifest = "/etc/kubernetes/manifests/kube-apiserver.yaml"
	flag := fmt.Sprintf("--%s=%s", name, value)
	// grep on the flag NAME (not the whole assignment) so an existing flag with a
	// different value counts as already-configured rather than being added a
	// second time — two copies of one apiserver flag is a start-up error.
	return fmt.Sprintf(
		"grep -q -- '--%s=' %s || sed -i 's|    - kube-apiserver|    - kube-apiserver\\n    - %s|' %s",
		name, manifest, flag, manifest)
}

func ApplyAPIServerExtraArgs(signer ssh.Signer, user, publicIP string, args map[string]string) error {
	if len(args) == 0 {
		return nil
	}
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		// grep on the flag NAME (not the whole assignment) so an existing flag
		// with a different value is treated as already-configured rather than
		// added a second time — two copies of the same apiserver flag is a
		// start-up error.
		if out, err := SSHRun(signer, user, publicIP, apiServerFlagCommand(name, args[name]), 2*time.Minute); err != nil {
			return fmt.Errorf("failed to set apiserver flag --%s: %w (output: %s)", name, err, LastLines(out, 5))
		}
	}
	return nil
}

// KubeadmInitMaster runs kubeadm init on the control-plane node (idempotent —
// skipped when the node is already initialized) and returns the worker join
// command. kube-proxy is skipped: the platform bootstrap installs Cilium with
// kubeProxyReplacement.
func KubeadmInitMaster(signer ssh.Signer, user, publicIP, privateIP, podCIDR string, apiServerExtraArgs map[string]string) (string, error) {
	if podCIDR == "" {
		podCIDR = KubeadmPodCIDR
	}
	initCmd := fmt.Sprintf(
		"test -f /etc/kubernetes/admin.conf || kubeadm init "+
			"--pod-network-cidr=%s "+
			"--skip-phases=addon/kube-proxy "+
			"--control-plane-endpoint=%s "+
			"--apiserver-cert-extra-sans=%s,%s",
		podCIDR, publicIP, publicIP, privateIP)
	if out, err := SSHRun(signer, user, publicIP, initCmd, 15*time.Minute); err != nil {
		return "", fmt.Errorf("kubeadm init failed: %w (output: %s)", err, LastLines(out, 15))
	}

	// Parallel image pulls (see TuneImagePulls). The control plane pulls the whole
	// platform's control-plane components, so it benefits as much as a worker.
	if err := TuneImagePulls(signer, user, publicIP); err != nil {
		log.Printf("Warning: %v", err)
	}

	// Cloud node hostnames are generally not DNS-resolvable by the API
	// server; prefer node IPs so `kubectl logs/exec` work. Static-pod edit is
	// idempotent (delete + single re-insert).
	addrFix := `grep -q kubelet-preferred-address-types /etc/kubernetes/manifests/kube-apiserver.yaml || sed -i 's|    - kube-apiserver|    - kube-apiserver\n    - --kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname|' /etc/kubernetes/manifests/kube-apiserver.yaml`
	if out, err := SSHRun(signer, user, publicIP, addrFix, 2*time.Minute); err != nil {
		return "", fmt.Errorf("failed to set apiserver kubelet address preference: %w (output: %s)", err, LastLines(out, 5))
	}

	if err := ApplyAPIServerExtraArgs(signer, user, publicIP, apiServerExtraArgs); err != nil {
		return "", err
	}

	joinCmd, err := SSHRun(signer, user, publicIP, "kubeadm token create --print-join-command", 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to create kubeadm join token: %w", err)
	}
	return strings.TrimSpace(joinCmd), nil
}

// KubeadmJoinWorker joins a worker node to the cluster (idempotent).
// JoinedNodes lists the nodes already registered with the control plane, by
// node name and by every address, so providers can adopt an existing cluster
// without touching its workers over SSH: a worker that is already joined is
// skipped entirely (no prep wait, no kubeadm join). This keeps re-running
// `adhar up` against a live cluster independent of each node's sshd — the
// only host that must be reachable is the control plane.
type JoinedNodes struct {
	Names     map[string]bool
	Addresses map[string]bool
}

// Has reports whether a worker identified by name and/or addresses is joined.
func (j JoinedNodes) Has(name string, addrs ...string) bool {
	if j.Names[name] {
		return true
	}
	for _, a := range addrs {
		if a != "" && j.Addresses[a] {
			return true
		}
	}
	return false
}

// KubeadmJoinedNodes queries the control plane for the registered nodes. A
// failure yields an empty set (every worker is then processed as before).
func KubeadmJoinedNodes(signer ssh.Signer, user, masterIP string) JoinedNodes {
	j := JoinedNodes{Names: map[string]bool{}, Addresses: map[string]bool{}}
	out, err := SSHRun(signer, user, masterIP,
		`kubectl --kubeconfig /etc/kubernetes/admin.conf get nodes -o jsonpath='{range .items[*]}{.metadata.name}{range .status.addresses[*]} {.address}{end}{"\n"}{end}'`,
		2*time.Minute)
	if err != nil {
		return j
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		j.Names[fields[0]] = true
		for _, a := range fields[1:] {
			j.Addresses[a] = true
		}
	}
	return j
}

// imagePullTuning is the KubeletConfiguration the platform appends after kubeadm
// has written /var/lib/kubelet/config.yaml.
//
// WHY THIS IS THE BIGGEST SINGLE WIN IN A BRING-UP. The kubelet defaults to
// serializeImagePulls: true — it pulls ONE image at a time per node, start to
// finish, however much network and disk are idle. A production Adhar profile is ~80
// packages, and a live GCP bring-up measured 362 pulls totalling 4,530 seconds of
// pull time, with individual images waiting up to SEVEN MINUTES in the queue behind
// others. Nothing about that appears as an error: every app simply sits
// Progressing, and the platform looks slow rather than serialised.
//
// maxParallelImagePulls bounds the parallelism so a node does not thrash: five
// concurrent pulls saturate a cloud NIC without starving the kubelet's other work.
// containerd's own max_concurrent_downloads is raised to match in the node-prep
// script — leaving it at the default 3 would just move the queue one layer down.
//
// These are CONFIG fields, not flags: --serialize-image-pulls exists but is
// deprecated, and there is no flag form of maxParallelImagePulls at all. Writing the
// config kubeadm generated is the supported path, and it survives a kubelet upgrade
// that drops the flag.
// kubeletTuning is appended to the kubelet's own config after kubeadm has written
// it. Each key is guarded separately by kubeletTuningCommand, so a node that
// already has some of them gains only what it is missing.
var kubeletTuning = []struct{ Key, Line string }{
	// Parallel image pulls: the kubelet serialises by default, and a production
	// profile fetches ~70 images per node.
	{"serializeImagePulls", "serializeImagePulls: false"},
	{"maxParallelImagePulls", "maxParallelImagePulls: 5"},
	// maxPods: the kubelet's default of 110 is a HARD scheduling ceiling, and the
	// platform's catalogue is several hundred small pods. The Kind provider has
	// raised this to 250 for exactly this reason since it shipped; cloud nodes did
	// not, so every kubeadm cluster was capped at 110 per node however large the
	// machine.
	//
	// Measured on Azure (2026-09-26): of 113 pods that could not run, **96 were
	// blocked by the pod ceiling and only 1 by CPU**. The node had CPU to spare and
	// still could not place anything, and the symptom — FailedScheduling
	// "Too many pods" — reads as a capacity problem that adding CPU does not fix.
	// A platform component that cannot reschedule after a restart for this reason
	// looks like an unrelated outage.
	{"maxPods", "maxPods: 250"},
}

// TuneImagePulls lets the kubelet pull images in parallel. Idempotent: the grep
// guard means a re-run neither duplicates the keys nor restarts a healthy kubelet.
//
// Called AFTER kubeadm init/join, because kubeadm writes
// /var/lib/kubelet/config.yaml itself and would overwrite anything placed there
// first. A failure is returned but is never fatal to the caller: a node that pulls
// serially is slow, not broken.
func TuneImagePulls(signer ssh.Signer, user, ip string) error {
	if out, err := SSHRun(signer, user, ip, imagePullTuningCommand(), 2*time.Minute); err != nil {
		return fmt.Errorf("tuning image pulls on %s: %w (output: %s)", ip, err, LastLines(out, 5))
	}
	return nil
}

// imagePullTuningCommand builds the idempotent one-liner that appends the tuning to
// the kubelet config and restarts the kubelet.
//
// Extracted so it can be unit-tested, like apiServerFlagCommand above and for the
// same reason: the quoting has to survive Go → ssh → sh → printf, and a slip is only
// discovered on a node that has already been paid for. %q renders the constant's real
// newlines as backslash-n inside double quotes, which is exactly what `printf '%b'`
// expands again on the other side — so the command stays a single line while writing
// two real ones.
func imagePullTuningCommand() string {
	const cfg = "/var/lib/kubelet/config.yaml"
	// One guarded append per key, then a single restart if anything changed.
	//
	// Guarding the whole block on one key (it used to test only
	// `serializeImagePulls`) meant a node written by an older release could never
	// gain a key added later: the guard matched, and the append was skipped. Each
	// key now carries its own test, so an existing node picks up exactly what it
	// lacks.
	var b strings.Builder
	b.WriteString("changed=0; ")
	for _, t := range kubeletTuning {
		b.WriteString(fmt.Sprintf(
			"grep -q '^%[1]s:' %[2]s 2>/dev/null || "+
				"{ printf '%%b' %[3]q >> %[2]s; changed=1; }; ",
			t.Key, cfg, t.Line+"\n"))
	}
	b.WriteString("[ \"$changed\" = 1 ] && systemctl restart kubelet || true")
	return b.String()
}

func KubeadmJoinWorker(signer ssh.Signer, user, ip, joinCmd string) error {
	if out, err := SSHRun(signer, user, ip, "test -f /etc/kubernetes/kubelet.conf || "+joinCmd, 10*time.Minute); err != nil {
		return fmt.Errorf("kubeadm join failed on %s: %w (output: %s)", ip, err, LastLines(out, 15))
	}
	// Parallel image pulls, now that kubeadm has written the kubelet config. Slow
	// is not broken, so a failure here is logged by the caller rather than aborting
	// a join that otherwise succeeded.
	if err := TuneImagePulls(signer, user, ip); err != nil {
		log.Printf("Warning: %v", err)
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
	out = trimToKubeconfig(out)
	return strings.ReplaceAll(out, "https://127.0.0.1:6443", fmt.Sprintf("https://%s:6443", masterIP)), nil
}

// trimToKubeconfig drops anything a remote shell printed before the YAML starts.
//
// Belt as well as braces: node prep now makes the hostname resolvable so sudo
// stops warning, but ANY future warning on a captured command would corrupt the
// kubeconfig exactly the same way, and the resulting error names YAML rather than
// the shell noise that caused it. A kubeconfig always begins with apiVersion.
func trimToKubeconfig(out string) string {
	if i := strings.Index(out, "apiVersion:"); i > 0 {
		return out[i:]
	}
	return out
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

// ─── Self-managed worker lifecycle, shared by every raw-compute provider ────
//
// DigitalOcean grew these first (join with the external cloud-provider flags,
// drain-then-delete on scale-down, a deterministic scale plan) and the other
// providers re-implemented — or stubbed — them. They live here so a fix lands
// once: a scale-down on AWS, Azure, GCP or Civo drains exactly like one on
// DigitalOcean, and the CSI startup taint is spelled in one place.

// ExternalCloudProviderFlag is the kubelet argument that defers node
// initialisation to a cloud-controller-manager.
const ExternalCloudProviderFlag = "--cloud-provider=external"

// EnableExternalCloudProvider writes the kubelet extra args a raw-compute
// node needs BEFORE it joins, then restarts the kubelet:
//
//   - --cloud-provider=external, when the provider installs a CCM: without it
//     the node is never initialised by the CCM;
//   - --node-ip=<privateIP>: without it the kubelet registers no InternalIP
//     until the CCM initialises the node, which deadlocks scheduling (Cilium
//     cannot start without a node IP, the CCM cannot schedule until Cilium
//     clears its taint) and breaks kubectl logs/exec through the API server;
//   - --register-with-taints=<globals.NodeCSIStartupTaint>=:NoSchedule on a
//     WORKER whose provider installs a CSI driver: the scheduler enforces the
//     per-node volume attach limit only once the CSI node plugin has published
//     a CSINode, and before that it packs the node past the cloud's ceiling
//     (11 attachments on a 7-volume DigitalOcean droplet, seen twice). The node
//     autoscaler lifts the taint the moment the CSINode appears.
//
// Idempotent: a node whose /etc/default/kubelet already carries the flags is
// left alone, so a re-run of `adhar up` never restarts a healthy kubelet.
func EnableExternalCloudProvider(signer ssh.Signer, user, ip, privateIP string, externalCCM, csiStartupTaint bool) error {
	flags := "KUBELET_EXTRA_ARGS="
	var parts []string
	if externalCCM {
		parts = append(parts, ExternalCloudProviderFlag)
	}
	if privateIP != "" {
		parts = append(parts, "--node-ip="+privateIP)
	}
	if csiStartupTaint {
		parts = append(parts, "--register-with-taints="+globals.NodeCSIStartupTaint+"=:NoSchedule")
	}
	if len(parts) == 0 {
		return nil
	}
	flags += strings.Join(parts, " ")
	marker := parts[0]
	_, err := SSHRun(signer, user, ip,
		"grep -q -- '"+marker+"' /etc/default/kubelet 2>/dev/null || { echo '"+flags+"' >> /etc/default/kubelet && systemctl restart kubelet; }", 2*time.Minute)
	return err
}

// JoinCommand mints a fresh bootstrap token on the control plane and returns
// the `kubeadm join …` line a new worker runs. Tokens expire (24h), so a
// scale-up hours after `adhar up` must not reuse the bootstrap one.
func JoinCommand(signer ssh.Signer, user, masterIP string) (string, error) {
	out, err := SSHRun(signer, user, masterIP, "kubeadm token create --print-join-command", 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("creating a kubeadm join token on %s: %w", masterIP, err)
	}
	return strings.TrimSpace(out), nil
}

// RetireWorker drains a worker and removes its Node object through the
// control plane's admin kubeconfig, so the cloud instance can be deleted
// without stranding pods or leaving a NotReady ghost node behind. A failed
// drain is reported, not fatal: the node is going away regardless, and the
// pods it still holds are rescheduled once the Node object is gone.
func RetireWorker(signer ssh.Signer, user, masterIP, nodeName string) error {
	drain := fmt.Sprintf(
		"kubectl --kubeconfig /etc/kubernetes/admin.conf drain %[1]s --ignore-daemonsets --delete-emptydir-data --timeout=5m || true; "+
			"kubectl --kubeconfig /etc/kubernetes/admin.conf delete node %[1]s --ignore-not-found", nodeName)
	out, err := SSHRun(signer, user, masterIP, drain, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("retiring node %s: %w (%s)", nodeName, err, LastLines(out, 5))
	}
	return nil
}

// WorkerScalePlan decides which workers to add or remove to move a node group
// from its current members to a desired count. Names are `<prefix><index>`;
// new workers take the lowest free indices, removals take the highest-indexed
// members first (the youngest, least likely to hold state). Pure, so every
// provider's scale-up/down is exercised by the same unit tests.
func WorkerScalePlan(prefix string, current []string, desired int) (add, remove []string) {
	if desired < 0 {
		desired = 0
	}
	used := map[int]string{}
	for _, name := range current {
		if idx, ok := strings.CutPrefix(name, prefix); ok {
			if i, err := strconv.Atoi(idx); err == nil {
				used[i] = name
			}
		}
	}
	switch {
	case desired > len(used):
		for i := 1; len(add) < desired-len(used); i++ {
			if _, taken := used[i]; !taken {
				add = append(add, fmt.Sprintf("%s%d", prefix, i))
			}
		}
	case desired < len(used):
		idx := make([]int, 0, len(used))
		for i := range used {
			idx = append(idx, i)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(idx)))
		for _, i := range idx[:len(used)-desired] {
			remove = append(remove, used[i])
		}
	}
	return add, remove
}
