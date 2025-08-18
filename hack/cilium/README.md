# Cilium Configuration

This directory contains Helm values files for deploying Cilium, a high-performance Container Network Interface (CNI) that provides networking, security, and observability for Kubernetes clusters. Cilium is designed to be naturally distributed and scalable without requiring separate HA configurations.

## 📁 Files Overview

- `values.yaml` - **Unified configuration** suitable for all environments
- `generate-manifests.sh` - Script to generate Kubernetes manifests

## 🌐 Cilium Overview

Cilium is a modern CNI that leverages eBPF (extended Berkeley Packet Filter) to provide:

- **Advanced Networking**: Layer 3/4 and Layer 7 networking with load balancing
- **Network Security**: Identity-based security policies and encryption
- **Observability**: Deep network visibility with Hubble
- **Service Mesh**: Transparent proxy and traffic management
- **Multi-Cluster**: Cross-cluster connectivity and service discovery

## ⚙️ Configuration Features

### 🔧 **Core Networking Settings**
```yaml
# IPV4/IPV6 Configuration
ipv4:
  enabled: true
ipv6:
  enabled: false

# IPAM (IP Address Management)
ipam:
  mode: "kubernetes"              # kubernetes, cluster-pool, multi-pool
  operator:
    clusterPoolIPv4PodCIDRList: ["10.0.0.0/8"]
    clusterPoolIPv4MaskSize: 24
```

### 🛡️ **Security Features**
```yaml
# Network Policies
policyEnforcementMode: "default"   # default, always, never

# Encryption
encryption:
  enabled: false                   # Enable transparent encryption
  type: "wireguard"               # wireguard, ipsec

# Identity Allocation
identityAllocationMode: "crd"      # crd, kvstore
```

### 📊 **Hubble Observability**
```yaml
hubble:
  enabled: true                    # Enable Hubble for observability
  relay:
    enabled: true                  # Enable Hubble Relay
    replicas: 1
  ui:
    enabled: true                  # Enable Hubble UI
    replicas: 1
```

### 🎯 **Operator Configuration**
```yaml
operator:
  enabled: true                    # Enable Cilium Operator
  replicas: 2                      # HA for operator
  rollOutPods: false              # Auto-restart on config changes
```

## 🚀 Getting Started

### Prerequisites

- Kubernetes cluster (v1.21+)
- Helm 3.x installed
- `kubectl` configured to access your cluster
- **Note**: Cilium should be installed before other CNI plugins

### Installation

#### 1. Add Cilium Helm Repository

```bash
helm repo add cilium https://helm.cilium.io/
helm repo update
```

#### 2. Deploy Cilium CNI

```bash
# Install Cilium CNI
helm install cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml

# Wait for Cilium to be ready
kubectl wait --namespace kube-system \
  --for=condition=ready pod \
  --selector=k8s-app=cilium \
  --timeout=120s
```

#### 3. Verify Installation

```bash
# Check Cilium status
kubectl get pods -n kube-system -l k8s-app=cilium

# Verify connectivity
kubectl get nodes -o wide

# Check Cilium operator
kubectl get pods -n kube-system -l name=cilium-operator
```

## 🔧 Environment-Specific Configurations

### 🏠 Local Development (Kind/Minikube)

For local development environments:

```bash
# Install with development-friendly settings
helm install cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml \
  --set debug.enabled=true \
  --set hubble.ui.enabled=true \
  --set hubble.relay.enabled=true \
  --set operator.replicas=1
```

### 🏭 Production Environment

For production deployments:

```bash
# Install with production settings
helm install cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml \
  --set operator.replicas=2 \
  --set hubble.metrics.enabled="dns:query;ignoreAAAA,drop,tcp,flow,icmp,http" \
  --set hubble.relay.replicas=2 \
  --set hubble.ui.replicas=2 \
  --set encryption.enabled=true \
  --set encryption.type=wireguard
```

### ☁️ Cloud Provider Specific

#### AWS (EKS)
```yaml
eni:
  enabled: true                    # Enable ENI mode for AWS
  updateEC2AdapterLimitViaAPI: true
  
tunnel: "disabled"                 # Disable tunneling for ENI
autoDirectNodeRoutes: true
```

#### Google Cloud (GKE)
```yaml
gke:
  enabled: true                    # Enable GKE optimizations
  
ipam:
  mode: "kubernetes"              # Use GKE's native IPAM
```

#### Azure (AKS)
```yaml
azure:
  enabled: true                    # Enable Azure optimizations
  
tunnel: "disabled"                 # Azure native routing
autoDirectNodeRoutes: true
```

## 📊 Hubble Observability

### Enable Hubble UI

```bash
# Port forward to access Hubble UI
kubectl port-forward -n kube-system svc/hubble-ui 12000:80

# Access at: http://localhost:12000
```

### Hubble CLI

```bash
# Install Hubble CLI
HUBBLE_VERSION=$(curl -s https://raw.githubusercontent.com/cilium/hubble/master/stable.txt)
curl -L --remote-name-all https://github.com/cilium/hubble/releases/download/$HUBBLE_VERSION/hubble-linux-amd64.tar.gz{,.sha256sum}
tar xzvfC hubble-linux-amd64.tar.gz /usr/local/bin
rm hubble-linux-amd64.tar.gz{,.sha256sum}

# Port forward Hubble Relay
kubectl port-forward -n kube-system svc/hubble-relay 4245:80

# Export Hubble endpoint
export HUBBLE_DEFAULT_SOCKET_PATH=localhost:4245

# Use Hubble CLI
hubble status
hubble observe
hubble observe --protocol tcp
```

### Hubble Metrics for Prometheus

```yaml
hubble:
  metrics:
    enabled:
      - dns:query;ignoreAAAA
      - drop
      - tcp
      - flow
      - icmp
      - http
    serviceMonitor:
      enabled: true
```

## 🛡️ Network Security

### Network Policies

#### Basic Ingress Policy
```yaml
apiVersion: "cilium.io/v2"
kind: CiliumNetworkPolicy
metadata:
  name: "l3-rule"
spec:
  endpointSelector:
    matchLabels:
      app: backend
  ingress:
  - fromEndpoints:
    - matchLabels:
        app: frontend
```

#### Layer 7 HTTP Policy
```yaml
apiVersion: "cilium.io/v2"
kind: CiliumNetworkPolicy
metadata:
  name: "l7-rule"
spec:
  endpointSelector:
    matchLabels:
      app: api-server
  ingress:
  - fromEndpoints:
    - matchLabels:
        app: client
    toPorts:
    - ports:
      - port: "8080"
        protocol: TCP
      rules:
        http:
        - method: "GET"
          path: "/api/v1/.*"
```

#### DNS Policy
```yaml
apiVersion: "cilium.io/v2"
kind: CiliumNetworkPolicy
metadata:
  name: "dns-visibility"
spec:
  endpointSelector:
    matchLabels:
      app: test-app
  egress:
  - toFQDNs:
    - matchName: "example.com"
  - toPorts:
    - ports:
      - port: "53"
        protocol: UDP
      rules:
        dns:
        - matchPattern: "*.example.com"
```

### Enable Encryption

#### WireGuard Encryption
```bash
# Install with WireGuard encryption
helm upgrade cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml \
  --set encryption.enabled=true \
  --set encryption.type=wireguard
```

#### IPSec Encryption
```bash
# Create IPSec secret
kubectl create -n kube-system secret generic cilium-ipsec-keys \
  --from-literal=keys="3 rfc4106(gcm(aes)) $(echo $(dd if=/dev/urandom count=20 bs=1 2> /dev/null | xxd -p -c 64)) 128"

# Install with IPSec encryption
helm upgrade cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml \
  --set encryption.enabled=true \
  --set encryption.type=ipsec
```

## 🌐 Multi-Cluster Networking

### Cluster Mesh Setup

#### 1. Enable Cluster Mesh
```bash
# Install with cluster mesh enabled
helm upgrade cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml \
  --set cluster.name=cluster1 \
  --set cluster.id=1 \
  --set clustermesh.useAPIServer=true
```

#### 2. Configure External Workloads
```yaml
externalWorkloads:
  enabled: true

nodePort:
  enabled: true
```

### Service Mesh Features

#### Load Balancing
```yaml
kubeProxyReplacement: "strict"      # Replace kube-proxy entirely

loadBalancer:
  algorithm: "round_robin"          # round_robin, least_request, random
  mode: "snat"                     # snat, dsr, hybrid
```

#### Ingress Controller
```yaml
ingressController:
  enabled: true
  loadbalancerMode: shared
  default: false
```

## 📈 Performance Tuning

### eBPF Optimization

```yaml
bpf:
  masquerade: true                  # Use eBPF for masquerading
  hostRouting: true                # Use eBPF for host routing
  lbExternalClusterIP: true        # eBPF load balancing for ClusterIP
  
datapathMode: "veth"               # veth, ipvlan, af_packet
```

### Resource Limits

```yaml
resources:
  limits:
    cpu: 1000m
    memory: 1Gi
  requests:
    cpu: 100m
    memory: 128Mi

operator:
  resources:
    limits:
      cpu: 100m
      memory: 128Mi
    requests:
      cpu: 100m
      memory: 128Mi
```

### Advanced Networking

```yaml
# Custom MTU
mtu: 1500

# Native routing
autoDirectNodeRoutes: true
enableIPv4Masquerade: false

# BGP Control Plane
bgpControlPlane:
  enabled: true

# Bandwidth management
bandwidth:
  enabled: true
```

## 🔍 Monitoring & Troubleshooting

### Cilium CLI Tool

```bash
# Install Cilium CLI
CILIUM_CLI_VERSION=$(curl -s https://raw.githubusercontent.com/cilium/cilium-cli/master/stable.txt)
CLI_ARCH=amd64
curl -L --fail --remote-name-all https://github.com/cilium/cilium-cli/releases/download/${CILIUM_CLI_VERSION}/cilium-linux-${CLI_ARCH}.tar.gz{,.sha256sum}
tar xzvfC cilium-linux-${CLI_ARCH}.tar.gz /usr/local/bin
rm cilium-linux-${CLI_ARCH}.tar.gz{,.sha256sum}

# Basic diagnostics
cilium status
cilium connectivity test
cilium sysdump
```

### Common Issues

#### 1. Connectivity Problems

```bash
# Check Cilium agent status
kubectl get pods -n kube-system -l k8s-app=cilium

# Check individual agent logs
kubectl logs -n kube-system ds/cilium

# Run connectivity test
cilium connectivity test
```

#### 2. Network Policy Issues

```bash
# Check policy enforcement
kubectl get cnp,ccnp -A

# Debug specific endpoint
cilium endpoint list
cilium policy get <endpoint-id>

# Monitor policy decisions
hubble observe --verdict DENIED
```

#### 3. Performance Issues

```bash
# Check eBPF program status
cilium bpf tunnel list
cilium bpf endpoint list

# Monitor datapath metrics
cilium metrics list
kubectl top pods -n kube-system -l k8s-app=cilium
```

#### 4. Encryption Issues

```bash
# Check encryption status
cilium encrypt status

# Verify WireGuard
cilium wg show

# Check IPSec (if used)
cilium encrypt flush
```

### Debugging Commands

```bash
# Comprehensive status
cilium status --verbose

# Endpoint debugging
cilium endpoint list
cilium endpoint get <endpoint-id>

# Policy debugging
cilium policy trace <src-endpoint> <dst-endpoint>

# BPF map inspection
cilium bpf policy get
cilium bpf ct list global

# Service debugging
cilium service list
cilium bpf lb list
```

## 📊 Metrics and Observability

### Prometheus Metrics

```yaml
prometheus:
  enabled: true
  port: 9962
  serviceMonitor:
    enabled: true
    labels:
      prometheus: monitoring

operator:
  prometheus:
    enabled: true
    port: 9963
    serviceMonitor:
      enabled: true
```

### Key Metrics to Monitor

- **cilium_datapath_errors_total**: Datapath errors
- **cilium_drop_count_total**: Dropped packets by reason
- **cilium_policy_verdict_total**: Policy verdicts
- **cilium_endpoint_count**: Number of managed endpoints
- **cilium_services_events_total**: Service events
- **cilium_node_connectivity_status**: Node connectivity health

### Grafana Dashboards

Use official Cilium dashboards:
- **Cilium Metrics**: Dashboard for general Cilium metrics
- **Hubble Dashboards**: Network observability dashboards
- **Cilium Operator**: Operator-specific metrics

## 🔄 Upgrade and Maintenance

### Upgrading Cilium

```bash
# Check current version
cilium version

# Update Helm repository
helm repo update

# Upgrade Cilium
helm upgrade cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml

# Verify upgrade
cilium status
kubectl rollout status ds/cilium -n kube-system
```

### Rolling Restart

```bash
# Restart Cilium agents
kubectl rollout restart ds/cilium -n kube-system

# Restart Cilium operator
kubectl rollout restart deployment/cilium-operator -n kube-system

# Monitor restart
kubectl rollout status ds/cilium -n kube-system
```

### Configuration Updates

```bash
# Update configuration
helm upgrade cilium cilium/cilium \
  --namespace kube-system \
  -f hack/cilium/values.yaml \
  --set operator.rollOutPods=true

# Verify configuration
kubectl get configmap cilium-config -n kube-system -o yaml
```

## 📚 Best Practices

### 🏗️ **Production Deployment**

1. **High Availability**: Run operator with 2+ replicas
2. **Resource Limits**: Set appropriate CPU and memory limits
3. **Monitoring**: Enable comprehensive metrics and alerting
4. **Security**: Enable encryption and proper network policies
5. **Testing**: Regularly run connectivity tests

### 🔒 **Security Best Practices**

1. **Default Deny**: Implement default-deny network policies
2. **Encryption**: Enable transparent encryption for sensitive workloads
3. **Identity-based Policies**: Use Cilium's identity-based security
4. **Regular Audits**: Monitor policy violations and network flows
5. **Least Privilege**: Apply minimal necessary network permissions

### ⚡ **Performance Optimization**

1. **eBPF Features**: Enable all relevant eBPF optimizations
2. **MTU Optimization**: Configure optimal MTU for your environment
3. **Direct Routing**: Use direct node routing when possible
4. **Resource Tuning**: Adjust resource limits based on cluster size
5. **Monitoring**: Monitor eBPF map sizes and memory usage

## 📚 Additional Resources

- [Cilium Documentation](https://docs.cilium.io/)
- [Cilium Getting Started](https://docs.cilium.io/en/stable/gettingstarted/)
- [Hubble Observability](https://docs.cilium.io/en/stable/observability/)
- [Network Policies](https://docs.cilium.io/en/stable/security/policy/)
- [Cilium Service Mesh](https://docs.cilium.io/en/stable/network/servicemesh/)
- [Troubleshooting Guide](https://docs.cilium.io/en/stable/troubleshooting/)

## 🤝 Contributing

When modifying the values files:

1. **Test Network Connectivity**: Ensure all networking functions work correctly
2. **Validate Security Policies**: Test network policies and encryption
3. **Performance Testing**: Verify performance with your workload patterns
4. **Documentation**: Update this README for any configuration changes
5. **Version Compatibility**: Check compatibility with Kubernetes versions

## ⚠️ Important Notes

- **CNI Replacement**: Cilium replaces the default CNI - ensure no conflicts
- **Kernel Requirements**: eBPF features require modern Linux kernels (4.9+)
- **Resource Usage**: Monitor memory usage as eBPF maps can consume significant memory
- **Network Policies**: Default enforcement can break existing workloads
- **Encryption Overhead**: Encryption adds CPU overhead - plan accordingly
- **Cloud Integration**: Some cloud features may conflict with Cilium features
- **Cluster Size**: Large clusters may require eBPF map size adjustments

## 🔄 Quick Reference

### Common Operations

```bash
# Check Cilium status
cilium status

# Run connectivity test
cilium connectivity test

# View network flows
hubble observe

# List endpoints
cilium endpoint list

# Check policies
cilium policy get

# Monitor drops
hubble observe --verdict DENIED

# Performance metrics
cilium metrics list
```

### Service Types and Modes

| **Feature** | **Default** | **Production** | **Use Case** |
|-------------|-------------|----------------|--------------|
| **IPAM Mode** | kubernetes | cluster-pool | IP management |
| **Datapath** | veth | veth/ipvlan | Performance |
| **Encryption** | disabled | wireguard | Security |
| **kube-proxy** | partial | strict | Performance |
| **Hubble** | enabled | enabled | Observability |

This comprehensive configuration provides a robust foundation for running Cilium CNI in any Kubernetes environment with advanced networking, security, and observability capabilities! 🚀
