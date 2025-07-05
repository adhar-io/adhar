# Example: HA Mode Control with Kustomize

This example demonstrates how the HA mode control works with different configurations.

## Example 1: Local Development (Always HA=false)

```bash
# This will ALWAYS use single replicas regardless of any config file
adhar up
```

**Generated Cilium Configuration:**

- Operator replicas: 1
- Hubble relay replicas: 1
- Hubble UI replicas: 1
- Encryption: disabled
- L7 proxy: disabled
- Resource requests: minimal (50m CPU, 128Mi memory)

## Example 2: Production with HA Enabled

**config.yaml:**
```yaml
globalSettings:
  enableHAMode: true  # Enable high availability
  productionProvider: "gke"
  nonProductionProvider: "do"
  
environments:
  production:
    type: production
    template: production-defaults
```

```bash
adhar up -f config.yaml --env production
```

**Generated Cilium Configuration:**
- Operator replicas: 2 (high availability)
- Hubble relay replicas: 2
- Hubble UI replicas: 2  
- Encryption: enabled (Wireguard)
- L7 proxy: enabled
- Resource requests: production-grade (100m CPU, 256Mi memory)

## Example 3: Development Environment with HA Disabled

**config.yaml:**
```yaml
globalSettings:
  enableHAMode: false  # Disable HA for cost optimization
  productionProvider: "gke"
  nonProductionProvider: "do"
  
environments:
  dev:
    type: non-production
    template: development-defaults
```

```bash
adhar up -f config.yaml --env dev
```

**Generated Cilium Configuration:**
- Operator replicas: 1 (single replica)
- Hubble relay replicas: 1
- Hubble UI replicas: 1
- Encryption: disabled
- L7 proxy: disabled
- Resource requests: optimized for cost

## Example 4: Mixed Environment Setup

**config.yaml:**
```yaml
globalSettings:
  enableHAMode: true  # Default HA mode
  productionProvider: "gke"
  nonProductionProvider: "do"
  
environments:
  dev:
    type: non-production  # Uses DO with cost optimization
  staging:
    type: production      # Uses GKE with HA
  production:
    type: production      # Uses GKE with full HA
```

```bash
# Deploy all environments
adhar up -f config.yaml
```

**Result:**
- **Dev environment**: Single replicas, minimal resources (cost-optimized)
- **Staging environment**: HA replicas, full monitoring (production-like testing)
- **Production environment**: HA replicas, encryption, full monitoring

## Verification Commands

### Check Replica Counts

```bash
# Check Cilium operator replicas
kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.replicas}'

# Check Hubble relay replicas  
kubectl get deployment -n kube-system hubble-relay -o jsonpath='{.spec.replicas}'

# Check Hubble UI replicas
kubectl get deployment -n kube-system hubble-ui -o jsonpath='{.spec.replicas}'
```

### Check Configuration

```bash
# Check if encryption is enabled
kubectl get configmap -n kube-system cilium-config -o jsonpath='{.data.enable-wireguard}'

# Check if L7 proxy is enabled
kubectl get configmap -n kube-system cilium-config -o jsonpath='{.data.enable-l7-proxy}'
```

### Check Resource Allocation

```bash
# Check CPU/memory requests for Cilium operator
kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.template.spec.containers[0].resources.requests}'

# Check CPU/memory limits
kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.template.spec.containers[0].resources.limits}'
```

## Expected Output Examples

### Local Development Mode

```bash
$ kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.replicas}'
1

$ kubectl get configmap -n kube-system cilium-config -o jsonpath='{.data.enable-wireguard}'
false

$ kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.template.spec.containers[0].resources.requests.cpu}'
50m
```

### Production Mode (HA Enabled)

```bash
$ kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.replicas}'
2

$ kubectl get configmap -n kube-system cilium-config -o jsonpath='{.data.enable-wireguard}'
true

$ kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.template.spec.containers[0].resources.requests.cpu}'
100m
```

## Benefits Demonstrated

1. **Resource Optimization**: Local development uses 50% fewer resources
2. **Environment Consistency**: Same configuration mechanism across all environments
3. **Flexibility**: Can mix HA and non-HA environments in the same configuration
4. **Cost Control**: Non-production environments can use single replicas for cost savings
5. **Production Readiness**: Production environments get full HA and monitoring by default
