# HA Mode Control with Kustomize over Helm Templates

This document explains how the Adhar platform implements High Availability (HA) mode control using Kustomize overlays on top of Helm-generated YAML manifests.

## Overview

The Adhar platform provides fine-grained control over service replica counts and resource usage through the `enableHAMode` configuration flag. This is implemented using a Kustomize-over-Helm approach that provides both flexibility and maintainability.

## Configuration

### Global Settings

In your `adhar-config.yaml`:

```yaml
globalSettings:
  # High Availability Mode - controls replica counts for all services
  # When false, uses single replicas for all services (optimized for local development)
  # When true, uses multiple replicas for high availability (production)
  enableHAMode: true
```

## Architecture

```text
┌─────────────────────────────────────────────────────────────┐
│                     HA Mode Control Flow                    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  1. Load Config File                                        │
│     ├── enableHAMode: true  → Production Mode               │
│     └── enableHAMode: false → Local Development Mode       │
│                                                             │
│  2. Generate Base YAML                                      │
│     ├── helm template cilium/cilium --values base-values.yaml │
│     └── Output: deployments, daemonsets, configmaps        │
│                                                             │
│  3. Create Kustomize Structure                              │
│     ├── base/                                               │
│     │   ├── kustomization.yaml (references all YAML files) │
│     │   └── Generated Helm templates                        │
│     └── overlays/                                           │
│         ├── local/                                          │
│         │   ├── kustomization.yaml (single replica patches) │
│         │   └── Applied when enableHAMode=false            │
│         └── production/                                     │
│             ├── kustomization.yaml (HA replica patches)    │
│             └── Applied when enableHAMode=true             │
│                                                             │
│  4. Apply Configuration                                     │
│     └── kubectl apply -k overlays/[local|production]       │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Implementation Details

### Base Values Generation

The system generates base Helm values that work for both modes:

```yaml
# base-values.yaml
kubeProxyReplacement: true
hubble:
  enabled: true
  relay:
    enabled: true
  ui:
    enabled: true
```

### Kustomize Overlays

#### Local Development Overlay (`overlays/local/`)

Applied when `enableHAMode: false` or local development mode:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base

patches:
  # Single replica for Cilium operator
  - target:
      kind: Deployment
      name: cilium-operator
    patch: |-
      - op: replace
        path: /spec/replicas
        value: 1
      - op: replace
        path: /spec/template/spec/containers/0/resources/requests/cpu
        value: "50m"
      - op: replace
        path: /spec/template/spec/containers/0/resources/requests/memory
        value: "128Mi"

  # Disable encryption for local development
  - target:
      kind: ConfigMap
      name: cilium-config
    patch: |-
      - op: add
        path: /data/enable-wireguard
        value: "false"
      - op: add
        path: /data/enable-l7-proxy
        value: "false"
```

#### Production Overlay (`overlays/production/`)

Applied when `enableHAMode: true`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base

patches:
  # High availability for Cilium operator
  - target:
      kind: Deployment
      name: cilium-operator
    patch: |-
      - op: replace
        path: /spec/replicas
        value: 2
      - op: replace
        path: /spec/template/spec/containers/0/resources/requests/cpu
        value: "100m"
      - op: replace
        path: /spec/template/spec/containers/0/resources/requests/memory
        value: "256Mi"

  # Enable encryption for production
  - target:
      kind: ConfigMap
      name: cilium-config
    patch: |-
      - op: add
        path: /data/enable-wireguard
        value: "true"
      - op: add
        path: /data/enable-l7-proxy
        value: "true"
```

## Usage Examples

### Local Development (Forced HA=false)

```bash
# This ALWAYS uses enableHAMode=false regardless of config file
adhar up

# Even with config file, local Kind clusters force HA=false
adhar up -f config-with-ha-true.yaml  # Still uses single replicas locally
```

### Production Deployment

```bash
# Respects enableHAMode setting from config file
adhar up -f production-config.yaml

# Specific environment
adhar up -f config.yaml --env production
```

## Mode Enforcement

### Local Development Mode

When using `adhar up` (without config file) or when using Kind clusters:

- **Force Override**: `enableHAMode` is always set to `false`
- **Replica Count**: All services use single replicas
- **Resources**: Minimal CPU/memory requests and limits
- **Features**: Encryption, L7 proxy, and other resource-intensive features disabled
- **Reason**: Optimal resource usage for development workstations

### Production Mode

When using `adhar up -f config.yaml` with cloud providers:

- **Config Respect**: Uses `enableHAMode` value from configuration file
- **High Availability**: Multiple replicas when `enableHAMode: true`
- **Full Features**: Encryption, monitoring, L7 proxy enabled
- **Resource Allocation**: Production-appropriate CPU/memory requests

## Benefits of This Approach

### 1. Flexibility

- Helm provides rich templating capabilities
- Kustomize provides declarative overlay management
- Clean separation of base configuration and environment-specific patches

### 2. Maintainability

- Base YAML generated from official Helm charts
- Environment-specific changes isolated in overlays
- No need to maintain separate Helm values files

### 3. Resource Optimization

- Local development uses minimal resources
- Production gets full HA and monitoring features
- Automatic optimization based on deployment context

### 4. Consistency

- Same underlying configuration mechanism
- Predictable behavior across environments
- Easy to extend for additional overlays (staging, testing, etc.)

## Extension Points

### Adding New Overlays

Create additional overlays for specific environments:

```bash
overlays/
├── local/              # enableHAMode=false
├── production/         # enableHAMode=true
├── staging/            # Custom staging optimizations
└── edge/               # Edge deployment optimizations
```

### Supporting Additional Services

The same pattern can be applied to other core services:

```text
services/
├── cilium/
│   ├── base/
│   └── overlays/
├── argocd/
│   ├── base/
│   └── overlays/
└── nginx/
    ├── base/
    └── overlays/
```

## Validation

### Verify Local Development Mode

```bash
# After adhar up, check Cilium operator replicas
kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.replicas}'
# Should output: 1

# Check resource requests
kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.template.spec.containers[0].resources.requests}'
# Should show minimal CPU/memory
```

### Verify Production Mode

```bash
# After adhar up -f production-config.yaml, check replicas
kubectl get deployment -n kube-system cilium-operator -o jsonpath='{.spec.replicas}'
# Should output: 2 (when enableHAMode: true)

# Check if encryption is enabled
kubectl get configmap -n kube-system cilium-config -o jsonpath='{.data.enable-wireguard}'
# Should output: true (when enableHAMode: true)
```

## Troubleshooting

### Debugging Kustomize Output

```bash
# Generate and inspect the final YAML without applying
kubectl kustomize overlays/local/ > debug-local.yaml
kubectl kustomize overlays/production/ > debug-production.yaml
```

### Checking Applied Configuration

```bash
# Verify which overlay was applied
kubectl get deployment -n kube-system cilium-operator -o yaml | grep -A 10 -B 10 "replicas"
```

This approach ensures that the Adhar platform provides optimal resource usage for local development while supporting full high-availability configurations for production environments, all controlled through a single `enableHAMode` configuration flag.
