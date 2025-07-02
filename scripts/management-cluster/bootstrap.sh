#!/bin/bash
# Adhar Management Cluster Bootstrap Script
# This script automates the complete setup of a production-grade Kubernetes management cluster
# with Cilium CNI, following industry best practices for security, reliability, and day-2 operations.

set -euo pipefail

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${SCRIPT_DIR}/cluster-config.yaml"
LOG_FILE="${SCRIPT_DIR}/bootstrap.log"

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging function
log() {
    echo -e "${BLUE}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1" | tee -a "$LOG_FILE"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" | tee -a "$LOG_FILE"
    exit 1
}

success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" | tee -a "$LOG_FILE"
}

warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" | tee -a "$LOG_FILE"
}

# Check if running as root
check_root() {
    if [[ $EUID -eq 0 ]]; then
        error "This script should not be run as root. Please run as a regular user with sudo privileges."
    fi
}

# Validate system requirements
validate_system() {
    log "Validating system requirements..."
    
    # Check OS
    if ! grep -q "Red Hat\|CentOS\|Rocky\|AlmaLinux" /etc/os-release; then
        error "This script is designed for RHEL-based systems (RHEL 9, CentOS 9, Rocky 9, AlmaLinux 9)"
    fi
    
    # Check memory (minimum 8GB)
    local mem_gb=$(free -g | awk 'NR==2{print $2}')
    if [[ $mem_gb -lt 8 ]]; then
        error "Insufficient memory. Minimum 8GB required, found ${mem_gb}GB"
    fi
    
    # Check CPU cores (minimum 4)
    local cpu_cores=$(nproc)
    if [[ $cpu_cores -lt 4 ]]; then
        error "Insufficient CPU cores. Minimum 4 cores required, found ${cpu_cores}"
    fi
    
    # Check disk space (minimum 50GB in /)
    local disk_gb=$(df / | awk 'NR==2{print int($4/1024/1024)}')
    if [[ $disk_gb -lt 50 ]]; then
        error "Insufficient disk space. Minimum 50GB required, found ${disk_gb}GB available"
    fi
    
    success "System requirements validation passed"
}

# Update system packages
update_system() {
    log "Updating system packages..."
    
    sudo dnf makecache --refresh
    sudo dnf update -y
    sudo dnf install -y ca-certificates curl gpg wget unzip jq
    
    success "System packages updated"
}

# Configure SELinux for Kubernetes
configure_selinux() {
    log "Configuring SELinux for Kubernetes..."
    
    # Set SELinux to permissive mode
    sudo setenforce 0
    sudo sed -i 's/^SELINUX=enforcing$/SELINUX=permissive/' /etc/selinux/config
    
    # Install SELinux policies for containers (for future enforcing mode)
    sudo dnf install -y container-selinux
    
    success "SELinux configured"
}

# Load kernel modules
load_kernel_modules() {
    log "Loading required kernel modules..."
    
    # Create modules configuration
    sudo tee /etc/modules-load.d/k8s.conf > /dev/null <<EOF
overlay
br_netfilter
EOF

    # Load modules immediately
    sudo modprobe overlay
    sudo modprobe br_netfilter
    
    # Verify modules are loaded
    if ! lsmod | grep -q overlay; then
        error "Failed to load overlay module"
    fi
    if ! lsmod | grep -q br_netfilter; then
        error "Failed to load br_netfilter module"
    fi
    
    success "Kernel modules loaded"
}

# Configure network parameters
configure_network() {
    log "Configuring network parameters for Kubernetes..."
    
    # Create sysctl configuration
    sudo tee /etc/sysctl.d/k8s.conf > /dev/null <<EOF
# Enable IP forwarding
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1

# Enable bridge netfilter
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1

# Optimize network performance
net.core.somaxconn = 32768
net.core.netdev_max_backlog = 5000
net.core.rmem_default = 262144
net.core.rmem_max = 16777216
net.core.wmem_default = 262144
net.core.wmem_max = 16777216

# Optimize for high connection workloads
net.ipv4.tcp_max_syn_backlog = 8192
net.ipv4.tcp_syncookies = 1
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 30
net.ipv4.tcp_keepalive_time = 120
net.ipv4.tcp_keepalive_probes = 3
net.ipv4.tcp_keepalive_intvl = 15

# File descriptor limits
fs.file-max = 2097152
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 524288
EOF

    # Apply sysctl settings
    sudo sysctl --system
    
    success "Network parameters configured"
}

# Disable swap
disable_swap() {
    log "Disabling swap..."
    
    # Disable swap immediately
    sudo swapoff -a
    
    # Remove swap entries from fstab
    sudo sed -e '/swap/s/^/#/g' -i /etc/fstab
    
    # Verify swap is disabled
    if [[ $(cat /proc/swaps | wc -l) -gt 1 ]]; then
        error "Failed to disable swap"
    fi
    
    success "Swap disabled"
}

# Install and configure containerd
install_containerd() {
    log "Installing and configuring containerd..."
    
    # Add Docker CE repository
    sudo dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
    sudo dnf makecache
    
    # Install containerd
    sudo dnf install -y containerd.io
    
    # Create containerd configuration directory
    sudo mkdir -p /etc/containerd
    
    # Generate default configuration
    sudo sh -c "containerd config default > /etc/containerd/config.toml"
    
    # Enable systemd cgroups (required for Kubernetes)
    sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
    
    # Configure runtime for better performance
    sudo sed -i '/\[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options\]/a \ \ \ \ \ \ \ \ BinaryName = "/usr/bin/runc"' /etc/containerd/config.toml
    
    # Enable and start containerd
    sudo systemctl enable containerd.service
    sudo systemctl restart containerd.service
    
    # Verify containerd is running
    if ! sudo systemctl is-active --quiet containerd; then
        error "Failed to start containerd"
    fi
    
    success "Containerd installed and configured"
}

# Install Kubernetes components
install_kubernetes() {
    log "Installing Kubernetes components..."
    
    # Add Kubernetes repository
    sudo tee /etc/yum.repos.d/kubernetes.repo > /dev/null <<EOF
[kubernetes]
name=Kubernetes
baseurl=https://pkgs.k8s.io/core:/stable:/v1.31/rpm/
enabled=1
gpgcheck=1
gpgkey=https://pkgs.k8s.io/core:/stable:/v1.31/rpm/repodata/repomd.xml.key
exclude=kubelet kubeadm kubectl cri-tools kubernetes-cni
EOF

    # Update repository cache
    sudo dnf makecache
    
    # Install specific versions of Kubernetes components
    local k8s_version="1.31.7-150500.1.1.x86_64"
    sudo dnf install -y \
        kubelet-${k8s_version} \
        kubeadm-${k8s_version} \
        kubectl-${k8s_version} \
        --disableexcludes=kubernetes
    
    # Enable kubelet (it will crash-loop until cluster init)
    sudo systemctl enable kubelet.service
    
    success "Kubernetes components installed"
}

# Install HAProxy for load balancing
install_haproxy() {
    log "Installing and configuring HAProxy..."
    
    # Install HAProxy
    sudo dnf install -y haproxy
    
    # Backup original config
    sudo cp /etc/haproxy/haproxy.cfg /etc/haproxy/haproxy.cfg.backup
    
    # Read cluster configuration
    local cluster_name=$(yq eval '.cluster.name' "$CONFIG_FILE")
    local api_endpoint=$(yq eval '.cluster.controlPlaneEndpoint' "$CONFIG_FILE")
    local masters=($(yq eval '.cluster.masters[].ip' "$CONFIG_FILE"))
    
    # Generate HAProxy configuration
    sudo tee /etc/haproxy/haproxy.cfg > /dev/null <<EOF
global
    daemon
    log stdout local0
    chroot /var/lib/haproxy
    stats socket /run/haproxy/admin.sock mode 660 level admin
    stats timeout 30s
    user haproxy
    group haproxy

defaults
    mode http
    log global
    option httplog
    option dontlognull
    option log-health-checks
    option forwardfor except 127.0.0.0/8
    option redispatch
    retries 3
    timeout http-request 10s
    timeout queue 20s
    timeout connect 10s
    timeout client 1m
    timeout server 1m
    timeout http-keep-alive 10s
    timeout check 10s

# Stats page
listen stats
    bind *:8404
    stats enable
    stats uri /stats
    stats refresh 5s
    stats admin if TRUE

# Kubernetes API Server Frontend
frontend kubernetes-frontend
    bind *:7443
    mode tcp
    option tcplog
    default_backend kubernetes-backend

# Kubernetes API Server Backend
backend kubernetes-backend
    mode tcp
    balance roundrobin
    option tcp-check
    tcp-check connect
    tcp-check send-binary 16030100
    tcp-check expect binary 1603010
EOF

    # Add master nodes to HAProxy config
    local master_index=1
    for master_ip in "${masters[@]}"; do
        echo "    server master0${master_index} ${master_ip}:6443 check inter 2s rise 2 fall 3" | sudo tee -a /etc/haproxy/haproxy.cfg > /dev/null
        ((master_index++))
    done
    
    # Enable and start HAProxy
    sudo systemctl enable haproxy.service
    sudo systemctl restart haproxy.service
    
    # Verify HAProxy is running
    if ! sudo systemctl is-active --quiet haproxy; then
        error "Failed to start HAProxy"
    fi
    
    success "HAProxy installed and configured"
}

# Generate kubeadm configuration
generate_kubeadm_config() {
    log "Generating kubeadm configuration..."
    
    local cluster_name=$(yq eval '.cluster.name' "$CONFIG_FILE")
    local control_plane_endpoint=$(yq eval '.cluster.controlPlaneEndpoint' "$CONFIG_FILE")
    local pod_subnet=$(yq eval '.cluster.networking.podSubnet' "$CONFIG_FILE")
    local service_subnet=$(yq eval '.cluster.networking.serviceSubnet' "$CONFIG_FILE")
    local kubernetes_version=$(yq eval '.cluster.kubernetesVersion' "$CONFIG_FILE")
    
    # Create kubeadm configuration
    tee "${SCRIPT_DIR}/kubeadm-config.yaml" > /dev/null <<EOF
# Initial configuration
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
nodeRegistration:
  criSocket: unix:///var/run/containerd/containerd.sock
  kubeletExtraArgs:
    cgroup-driver: systemd
    container-runtime-endpoint: unix:///var/run/containerd/containerd.sock

---
# Cluster configuration
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
clusterName: ${cluster_name}
kubernetesVersion: ${kubernetes_version}
controlPlaneEndpoint: ${control_plane_endpoint}
networking:
  podSubnet: ${pod_subnet}
  serviceSubnet: ${service_subnet}
  dnsDomain: cluster.local

# API Server configuration
apiServer:
  timeoutForControlPlane: 4m0s
  extraArgs:
    # Audit logging
    audit-log-path: /var/log/kubernetes/audit.log
    audit-policy-file: /etc/kubernetes/audit-policy.yaml
    audit-log-maxage: "30"
    audit-log-maxbackup: "3"
    audit-log-maxsize: "100"
    # Security
    profiling: "false"
    enable-admission-plugins: "NamespaceLifecycle,LimitRanger,ServiceAccount,DefaultStorageClass,DefaultTolerationSeconds,MutatingAdmissionWebhook,ValidatingAdmissionWebhook,ResourceQuota,NodeRestriction,PodNodeSelector"
    disable-admission-plugins: ""
    # Performance
    default-not-ready-toleration-seconds: "60"
    default-unreachable-toleration-seconds: "60"
  extraVolumes:
  - name: audit-policy
    hostPath: /etc/kubernetes/audit-policy.yaml
    mountPath: /etc/kubernetes/audit-policy.yaml
    readOnly: true
    pathType: File
  - name: audit-logs
    hostPath: /var/log/kubernetes
    mountPath: /var/log/kubernetes
    pathType: DirectoryOrCreate

# Controller Manager configuration
controllerManager:
  extraArgs:
    # Performance tuning
    node-monitor-period: "5s"
    node-monitor-grace-period: "40s"
    pod-eviction-timeout: "5m0s"
    use-service-account-credentials: "true"
    profiling: "false"

# Scheduler configuration
scheduler:
  extraArgs:
    profiling: "false"

# etcd configuration
etcd:
  local:
    extraArgs:
      listen-metrics-urls: http://0.0.0.0:2381
      # Performance tuning
      heartbeat-interval: "100"
      election-timeout: "1000"
      max-snapshots: "5"
      max-wals: "5"
      auto-compaction-retention: "8"

---
# Kubelet configuration
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cgroupDriver: systemd
serverTLSBootstrap: true
rotateCertificates: true
# Resource management
maxPods: 110
imageGCHighThresholdPercent: 85
imageGCLowThresholdPercent: 80
# Performance tuning
syncFrequency: 1m0s
fileCheckFrequency: 20s
httpCheckFrequency: 20s
nodeStatusUpdateFrequency: 10s
nodeStatusReportFrequency: 5m0s
runtimeRequestTimeout: 15m0s
# Security
protectKernelDefaults: true
makeIPTablesUtilChains: true
streamingConnectionIdleTimeout: 4h0m0s

---
# Kube-proxy configuration (will be replaced by Cilium)
apiVersion: kubeproxy.config.k8s.io/v1alpha1
kind: KubeProxyConfiguration
mode: "iptables"
clusterCIDR: ${pod_subnet}
EOF

    success "Kubeadm configuration generated"
}

# Create audit policy
create_audit_policy() {
    log "Creating Kubernetes audit policy..."
    
    sudo mkdir -p /etc/kubernetes /var/log/kubernetes
    
    sudo tee /etc/kubernetes/audit-policy.yaml > /dev/null <<EOF
apiVersion: audit.k8s.io/v1
kind: Policy
rules:
# Don't log requests to the following
- level: None
  nonResourceURLs:
  - "/healthz*"
  - "/logs"
  - "/metrics"
  - "/swagger*"
  - "/version"

# Don't log watch requests by the "system:kube-proxy" on endpoints or services
- level: None
  users: ["system:kube-proxy"]
  verbs: ["watch"]
  resources:
  - group: ""
    resources: ["endpoints", "services"]

# Don't log authenticated requests to certain non-resource URL paths
- level: None
  userGroups: ["system:authenticated"]
  nonResourceURLs:
  - "/api*"
  - "/version"

# Log the request body of configmap changes in kube-system
- level: Request
  resources:
  - group: ""
    resources: ["configmaps"]
  namespaces: ["kube-system"]

# Log configmap and secret changes in all other namespaces at the Metadata level
- level: Metadata
  resources:
  - group: ""
    resources: ["secrets", "configmaps"]

# Log all other changes at Request level
- level: Request
  omitStages:
  - RequestReceived
  resources:
  - group: ""
    resources: ["*"]
  - group: "admissionregistration.k8s.io"
    resources: ["*"]
  - group: "apiextensions.k8s.io"
    resources: ["*"]
  - group: "apiregistration.k8s.io"
    resources: ["*"]
  - group: "apps"
    resources: ["*"]
  - group: "authentication.k8s.io"
    resources: ["*"]
  - group: "authorization.k8s.io"
    resources: ["*"]
  - group: "autoscaling"
    resources: ["*"]
  - group: "batch"
    resources: ["*"]
  - group: "certificates.k8s.io"
    resources: ["*"]
  - group: "extensions"
    resources: ["*"]
  - group: "metrics.k8s.io"
    resources: ["*"]
  - group: "networking.k8s.io"
    resources: ["*"]
  - group: "policy"
    resources: ["*"]
  - group: "rbac.authorization.k8s.io"
    resources: ["*"]
  - group: "settings.k8s.io"
    resources: ["*"]
  - group: "storage.k8s.io"
    resources: ["*"]

# Default level for all other requests
- level: Metadata
  omitStages:
  - RequestReceived
EOF

    success "Audit policy created"
}

# Initialize Kubernetes cluster
initialize_cluster() {
    log "Initializing Kubernetes cluster..."
    
    # Initialize the cluster (skip kube-proxy as Cilium will replace it)
    sudo kubeadm init \
        --config="${SCRIPT_DIR}/kubeadm-config.yaml" \
        --upload-certs \
        --skip-phases=addon/kube-proxy \
        --v=5
    
    # Setup kubectl for regular user
    mkdir -p "$HOME/.kube"
    sudo cp -i /etc/kubernetes/admin.conf "$HOME/.kube/config"
    sudo chown $(id -u):$(id -g) "$HOME/.kube/config"
    
    # Verify cluster is accessible
    if ! kubectl get nodes >/dev/null 2>&1; then
        error "Failed to access Kubernetes cluster"
    fi
    
    success "Kubernetes cluster initialized"
}

# Install Cilium CLI
install_cilium_cli() {
    log "Installing Cilium CLI..."
    
    # Download and install Cilium CLI
    curl -LO https://github.com/cilium/cilium-cli/releases/latest/download/cilium-linux-amd64.tar.gz
    tar xzvf cilium-linux-amd64.tar.gz
    sudo mv cilium /usr/local/bin/
    rm cilium-linux-amd64.tar.gz
    
    # Verify installation
    if ! cilium version --client >/dev/null 2>&1; then
        error "Failed to install Cilium CLI"
    fi
    
    success "Cilium CLI installed"
}

# Install and configure Cilium
install_cilium() {
    log "Installing Cilium CNI..."
    
    # Install Cilium with production configuration
    cilium install \
        --version 1.15.7 \
        --set operator.replicas=2 \
        --set kubeProxyReplacement=strict \
        --set hubble.relay.enabled=true \
        --set hubble.ui.enabled=true \
        --set prometheus.enabled=true \
        --set operator.prometheus.enabled=true \
        --set hubble.metrics.enabled="{dns,drop,tcp,flow,icmp,http}" \
        --set ipam.mode=kubernetes \
        --set tunnel=vxlan \
        --set encryption.enabled=true \
        --set encryption.type=wireguard \
        --set bpf.masquerade=true \
        --set gatewayAPI.enabled=true \
        --set l7Proxy=true \
        --set localRedirectPolicy=true
    
    # Wait for Cilium to be ready
    log "Waiting for Cilium to be ready..."
    cilium status --wait --timeout=10m
    
    # Verify Cilium installation
    if ! cilium status >/dev/null 2>&1; then
        error "Cilium installation failed"
    fi
    
    success "Cilium installed and ready"
}

# Install Helm
install_helm() {
    log "Installing Helm..."
    
    # Download and install Helm
    curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
    
    # Verify installation
    if ! helm version >/dev/null 2>&1; then
        error "Failed to install Helm"
    fi
    
    success "Helm installed"
}

# Setup monitoring stack
setup_monitoring() {
    log "Setting up monitoring stack..."
    
    # Add Prometheus Community Helm repository
    helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
    helm repo add grafana https://grafana.github.io/helm-charts
    helm repo update
    
    # Create monitoring namespace
    kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
    
    # Install kube-prometheus-stack
    helm upgrade --install prometheus prometheus-community/kube-prometheus-stack \
        --namespace monitoring \
        --set prometheus.prometheusSpec.retention=30d \
        --set prometheus.prometheusSpec.storageSpec.volumeClaimTemplate.spec.resources.requests.storage=50Gi \
        --set grafana.enabled=true \
        --set grafana.adminPassword=admin123 \
        --set alertmanager.enabled=true \
        --wait --timeout=10m
    
    success "Monitoring stack installed"
}

# Setup network policies
setup_network_policies() {
    log "Setting up network policies..."
    
    # Create network policies for security
    kubectl apply -f - <<EOF
# Default deny-all policy
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
---
# Allow DNS resolution
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-dns-access
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Egress
  egress:
  - to: []
    ports:
    - protocol: UDP
      port: 53
    - protocol: TCP
      port: 53
EOF

    success "Network policies configured"
}

# Generate join commands
generate_join_commands() {
    log "Generating join commands for additional nodes..."
    
    # Generate token and discovery hash
    local token=$(sudo kubeadm token create --ttl 24h)
    local discovery_hash=$(openssl x509 -pubkey -in /etc/kubernetes/pki/ca.crt | openssl rsa -pubin -outform der 2>/dev/null | openssl dgst -sha256 -hex | sed 's/^.* //')
    local certificate_key=$(sudo kubeadm init phase upload-certs --upload-certs | tail -n 1)
    local control_plane_endpoint=$(yq eval '.cluster.controlPlaneEndpoint' "$CONFIG_FILE")
    
    # Create join commands file
    tee "${SCRIPT_DIR}/join-commands.txt" > /dev/null <<EOF
# Join additional control plane nodes (run on master nodes):
sudo kubeadm join ${control_plane_endpoint} \\
    --token ${token} \\
    --discovery-token-ca-cert-hash sha256:${discovery_hash} \\
    --control-plane \\
    --certificate-key ${certificate_key}

# Join worker nodes (run on worker nodes):
sudo kubeadm join ${control_plane_endpoint} \\
    --token ${token} \\
    --discovery-token-ca-cert-hash sha256:${discovery_hash}

# Token expires in 24 hours. To generate new tokens:
# sudo kubeadm token create --ttl 24h
# sudo kubeadm init phase upload-certs --upload-certs
EOF

    success "Join commands generated in ${SCRIPT_DIR}/join-commands.txt"
}

# Create day-2 operations scripts
create_day2_scripts() {
    log "Creating day-2 operations scripts..."
    
    # Backup script
    tee "${SCRIPT_DIR}/backup.sh" > /dev/null <<'EOF'
#!/bin/bash
# Adhar Management Cluster Backup Script

set -euo pipefail

BACKUP_DIR="/opt/adhar-backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$BACKUP_DIR"

echo "Creating backup in $BACKUP_DIR..."

# Backup etcd
sudo ETCDCTL_API=3 etcdctl snapshot save "$BACKUP_DIR/etcd-snapshot.db" \
    --endpoints=https://127.0.0.1:2379 \
    --cacert=/etc/kubernetes/pki/etcd/ca.crt \
    --cert=/etc/kubernetes/pki/etcd/server.crt \
    --key=/etc/kubernetes/pki/etcd/server.key

# Backup Kubernetes certificates
sudo cp -r /etc/kubernetes/pki "$BACKUP_DIR/"

# Backup kubeconfig
cp ~/.kube/config "$BACKUP_DIR/admin.conf"

# Backup cluster configuration
kubectl get all --all-namespaces -o yaml > "$BACKUP_DIR/cluster-state.yaml"

echo "Backup completed: $BACKUP_DIR"
EOF

    # Health check script
    tee "${SCRIPT_DIR}/health-check.sh" > /dev/null <<'EOF'
#!/bin/bash
# Adhar Management Cluster Health Check Script

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

check_component() {
    local component=$1
    local check_cmd=$2
    
    echo -n "Checking $component... "
    if eval "$check_cmd" >/dev/null 2>&1; then
        echo -e "${GREEN}OK${NC}"
        return 0
    else
        echo -e "${RED}FAILED${NC}"
        return 1
    fi
}

echo "Adhar Management Cluster Health Check"
echo "====================================="

# Check Kubernetes API
check_component "Kubernetes API" "kubectl get nodes"

# Check Cilium
check_component "Cilium CNI" "cilium status"

# Check CoreDNS
check_component "CoreDNS" "kubectl get pods -n kube-system -l k8s-app=kube-dns"

# Check etcd
check_component "etcd" "kubectl get pods -n kube-system -l component=etcd"

# Check HAProxy
check_component "HAProxy" "systemctl is-active haproxy"

# Check monitoring
check_component "Prometheus" "kubectl get pods -n monitoring -l app.kubernetes.io/name=prometheus"

echo "Health check completed"
EOF

    # Upgrade script
    tee "${SCRIPT_DIR}/upgrade.sh" > /dev/null <<'EOF'
#!/bin/bash
# Adhar Management Cluster Upgrade Script

set -euo pipefail

usage() {
    echo "Usage: $0 <kubernetes-version>"
    echo "Example: $0 v1.31.8"
    exit 1
}

if [[ $# -ne 1 ]]; then
    usage
fi

K8S_VERSION=$1

echo "Upgrading Kubernetes to $K8S_VERSION..."
echo "This will upgrade the cluster in a rolling fashion."
read -p "Continue? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    exit 1
fi

# Upgrade kubeadm
sudo dnf upgrade -y kubeadm --disableexcludes=kubernetes

# Plan the upgrade
sudo kubeadm upgrade plan

# Apply the upgrade
sudo kubeadm upgrade apply "$K8S_VERSION" --yes

# Upgrade kubelet and kubectl
sudo dnf upgrade -y kubelet kubectl --disableexcludes=kubernetes

# Restart kubelet
sudo systemctl daemon-reload
sudo systemctl restart kubelet

echo "Upgrade completed. Please upgrade other nodes manually."
EOF

    # Make scripts executable
    chmod +x "${SCRIPT_DIR}"/{backup.sh,health-check.sh,upgrade.sh}
    
    success "Day-2 operations scripts created"
}

# Create cluster summary
create_cluster_summary() {
    log "Creating cluster summary..."
    
    local cluster_name=$(yq eval '.cluster.name' "$CONFIG_FILE")
    local control_plane_endpoint=$(yq eval '.cluster.controlPlaneEndpoint' "$CONFIG_FILE")
    
    tee "${SCRIPT_DIR}/cluster-info.txt" > /dev/null <<EOF
Adhar Management Cluster Information
===================================

Cluster Name: ${cluster_name}
Control Plane Endpoint: ${control_plane_endpoint}
Kubernetes Version: $(kubectl version --short --client | cut -d' ' -f3)
Cilium Version: $(cilium version --client | grep "cilium-cli" | cut -d' ' -f2)

Access Information:
- Kubeconfig: ~/.kube/config
- HAProxy Stats: http://$(hostname -I | awk '{print $1}'):8404/stats
- Grafana: kubectl port-forward -n monitoring svc/prometheus-grafana 3000:80

Important Files:
- Cluster config: ${CONFIG_FILE}
- Join commands: ${SCRIPT_DIR}/join-commands.txt
- Bootstrap log: ${LOG_FILE}

Day-2 Operations:
- Health check: ${SCRIPT_DIR}/health-check.sh
- Backup: ${SCRIPT_DIR}/backup.sh
- Upgrade: ${SCRIPT_DIR}/upgrade.sh

Next Steps:
1. Add additional master nodes using join commands
2. Add worker nodes using join commands
3. Install Crossplane for environment provisioning
4. Configure monitoring and alerting
EOF

    success "Cluster summary created in ${SCRIPT_DIR}/cluster-info.txt"
}

# Main execution
main() {
    echo "Adhar Management Cluster Bootstrap"
    echo "=================================="
    echo "This script will set up a production-grade Kubernetes management cluster"
    echo "with Cilium CNI, following industry best practices."
    echo ""
    
    # Validate prerequisites
    if [[ ! -f "$CONFIG_FILE" ]]; then
        error "Configuration file not found: $CONFIG_FILE"
    fi
    
    if ! command -v yq >/dev/null 2>&1; then
        log "Installing yq for YAML processing..."
        sudo wget -qO /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64
        sudo chmod +x /usr/local/bin/yq
    fi
    
    # Execute setup steps
    check_root
    validate_system
    update_system
    configure_selinux
    load_kernel_modules
    configure_network
    disable_swap
    install_containerd
    install_kubernetes
    install_haproxy
    generate_kubeadm_config
    create_audit_policy
    initialize_cluster
    install_cilium_cli
    install_cilium
    install_helm
    setup_monitoring
    setup_network_policies
    generate_join_commands
    create_day2_scripts
    create_cluster_summary
    
    echo ""
    success "Management cluster bootstrap completed successfully!"
    echo ""
    echo "Next steps:"
    echo "1. Review cluster information: cat ${SCRIPT_DIR}/cluster-info.txt"
    echo "2. Run health check: ${SCRIPT_DIR}/health-check.sh"
    echo "3. Add additional nodes using: ${SCRIPT_DIR}/join-commands.txt"
    echo "4. Install Crossplane for environment provisioning"
    echo ""
}

# Execute main function
main "$@"
