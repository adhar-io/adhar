# Crossplane Configuration

This directory contains Helm values files for deploying Crossplane in different environments, optimized for both local development and production use cases.

## 📁 Files Overview

- `values.yaml` - **Non-HA configuration** for local development
- `values-ha.yaml` - **HA configuration** for production environments
- `generate-manifests.sh` - Script to generate Kubernetes manifests

## 🏠 Non-HA Configuration (`values.yaml`) - Local Development

**✅ Optimized for:**
- Resource efficiency for local development environments
- Minimal resource consumption suitable for development machines
- Simple single-instance deployment

**📋 Key Features:**
- **Crossplane Controller**: 1 replica
- **RBAC Manager**: 1 replica  
- **Resource Allocation**:
  - Crossplane: CPU 50m-200m, Memory 128Mi-512Mi
  - RBAC Manager: CPU 25m-50m, Memory 128Mi-256Mi
- **Cache Sizes**: Package 20Mi, Function 512Mi (minimal)
- **Metrics**: Disabled (reduces overhead)
- **Anti-Affinity**: Disabled (not needed for single instances)

## 🏭 HA Configuration (`values-ha.yaml`) - Production

**✅ Optimized for:**
- High availability with multiple controller instances
- Production-grade resource allocation
- Enhanced monitoring and observability
- Pod distribution across nodes

**📋 Key Features:**
- **Crossplane Controller**: 2 replicas (HA enabled)
- **RBAC Manager**: 2 replicas (HA enabled)
- **Resource Allocation**:
  - Crossplane: CPU 200m-1000m, Memory 512Mi-2048Mi
  - RBAC Manager: CPU 100m-200m, Memory 256Mi-1024Mi
- **Cache Sizes**: Package 100Mi, Function 1Gi (enhanced performance)
- **Metrics**: Enabled (full observability)
- **Anti-Affinity**: Enabled (distributes pods across nodes)

## 🔍 Detailed Comparison

| **Component** | **Non-HA (Local)** | **HA (Production)** |
|---------------|-------------------|-------------------|
| **Crossplane Replicas** | 1 | 2 |
| **RBAC Manager Replicas** | 1 | 2 |
| **Crossplane CPU Limits** | 200m | 1000m |
| **Crossplane Memory Limits** | 512Mi | 2048Mi |
| **Crossplane CPU Requests** | 50m | 200m |
| **Crossplane Memory Requests** | 128Mi | 512Mi |
| **RBAC Manager CPU Limits** | 50m | 200m |
| **RBAC Manager Memory Limits** | 256Mi | 1024Mi |
| **RBAC Manager CPU Requests** | 25m | 100m |
| **RBAC Manager Memory Requests** | 128Mi | 256Mi |
| **Package Cache Size** | 20Mi | 100Mi |
| **Function Cache Size** | 512Mi | 1Gi |
| **Metrics Collection** | ❌ Disabled | ✅ Enabled |
| **Pod Anti-Affinity** | ❌ Disabled | ✅ Enabled |
| **Resource Footprint** | 🟢 Minimal | 🟡 Production-grade |
| **High Availability** | ❌ Single point of failure | ✅ Resilient to failures |

## 🚀 Getting Started

### Prerequisites

- Kubernetes cluster (v1.19+)
- Helm 3.x installed
- `kubectl` configured to access your cluster

### Installation

#### 1. Add Crossplane Helm Repository

```bash
helm repo add crossplane-stable https://charts.crossplane.io/stable
helm repo update
```

#### 2. Local Development Deployment

For local development environments (Kind, Minikube, etc.):

```bash
# Install Crossplane with non-HA configuration
helm install crossplane crossplane-stable/crossplane \
  --namespace crossplane-system \
  --create-namespace \
  -f hack/crossplane/values.yaml

# Verify installation
kubectl get pods -n crossplane-system
```

#### 3. Production HA Deployment

For production environments:

```bash
# Install Crossplane with HA configuration
helm install crossplane crossplane-stable/crossplane \
  --namespace crossplane-system \
  --create-namespace \
  -f hack/crossplane/values-ha.yaml

# Verify installation and HA setup
kubectl get pods -n crossplane-system
kubectl get pods -n crossplane-system -o wide  # Check pod distribution
```

### Post-Installation Setup

#### 1. Verify Crossplane Installation

```bash
# Check Crossplane controller status
kubectl get deployment -n crossplane-system crossplane

# Check RBAC Manager status
kubectl get deployment -n crossplane-system crossplane-rbac-manager

# View logs if needed
kubectl logs -n crossplane-system deployment/crossplane
```

#### 2. Install Providers

Example: Installing AWS Provider

```bash
# Create a provider configuration
cat <<EOF | kubectl apply -f -
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws
spec:
  package: xpkg.crossplane.io/crossplane-contrib/provider-aws:v0.44.0
EOF

# Check provider installation
kubectl get providers
```

#### 3. Configure Provider Credentials

```bash
# Create a secret with your cloud provider credentials
kubectl create secret generic aws-secret \
  -n crossplane-system \
  --from-file=creds=./aws-credentials.txt

# Create a ProviderConfig
cat <<EOF | kubectl apply -f -
apiVersion: aws.crossplane.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: aws-secret
      key: creds
EOF
```

## 📊 Monitoring (Production HA Only)

When using the HA configuration, metrics are enabled for monitoring:

```bash
# Check metrics endpoint
kubectl port-forward -n crossplane-system deployment/crossplane 8080:8080

# Access metrics at: http://localhost:8080/metrics
```

### Prometheus Integration

Add the following ServiceMonitor for Prometheus scraping:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: crossplane
  namespace: crossplane-system
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: crossplane
  endpoints:
  - port: metrics
    interval: 30s
```

## 🔧 Customization

### Modify Resource Limits

Edit the values files to adjust resource allocation:

```yaml
# In values.yaml or values-ha.yaml
resourcesCrossplane:
  limits:
    cpu: 1000m      # Adjust CPU limit
    memory: 2048Mi  # Adjust memory limit
  requests:
    cpu: 200m       # Adjust CPU request
    memory: 512Mi   # Adjust memory request
```

### Enable/Disable Features

```yaml
# Enable metrics (production)
metrics:
  enabled: true

# Configure leader election
leaderElection: true

# Add custom arguments
args:
  - --debug
  - --sync=1h
```

## 🐛 Troubleshooting

### Common Issues

#### 1. Pods Not Starting
```bash
# Check pod status and events
kubectl describe pods -n crossplane-system

# Check resource constraints
kubectl top pods -n crossplane-system
```

#### 2. Provider Installation Fails
```bash
# Check provider status
kubectl get providers
kubectl describe provider <provider-name>

# Check Crossplane logs
kubectl logs -n crossplane-system deployment/crossplane
```

#### 3. HA Configuration Issues
```bash
# Verify anti-affinity is working
kubectl get pods -n crossplane-system -o wide

# Check if pods are distributed across nodes
kubectl get nodes
```

### Debugging Commands

```bash
# Get all Crossplane resources
kubectl api-resources --api-group=crossplane.io

# Check Crossplane version
kubectl get deployment crossplane -n crossplane-system -o jsonpath='{.spec.template.spec.containers[0].image}'

# View all events in namespace
kubectl get events -n crossplane-system --sort-by='.lastTimestamp'
```

## 📚 Additional Resources

- [Crossplane Documentation](https://docs.crossplane.io/)
- [Provider Documentation](https://docs.crossplane.io/latest/concepts/providers/)
- [Composition Guide](https://docs.crossplane.io/latest/concepts/compositions/)
- [Crossplane GitHub Repository](https://github.com/crossplane/crossplane)

## 🤝 Contributing

When modifying the values files:

1. Test changes in both local and production-like environments
2. Ensure resource limits are appropriate for the target environment
3. Update this README if adding new configuration options
4. Verify backward compatibility with existing deployments

## ⚠️ Important Notes

- **Resource Requirements**: HA configuration requires significantly more resources
- **Node Distribution**: HA configuration works best with 2+ nodes
- **Metrics Overhead**: Enabling metrics adds ~50-100MB memory overhead
- **Leader Election**: Both configurations use leader election for safety
- **Cache Sizes**: Adjust cache sizes based on the number of packages/functions used
