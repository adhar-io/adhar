# Adhar Platform User Guide

**Version**: v0.3.8  
**Last Updated**: November 2025

---

## 📖 Table of Contents

1. [Overview](#overview)
2. [Platform Capabilities](#platform-capabilities)
3. [Configuration](#configuration)
4. [CLI Reference](#cli-reference)
5. [Platform Services](#platform-services)
6. [Application Management](#application-management)
7. [Environment Management](#environment-management)
8. [Monitoring & Observability](#monitoring--observability)
9. [Security & Compliance](#security--compliance)
10. [Best Practices](#best-practices)

---

## Overview

Adhar is a complete Internal Developer Platform that provides everything needed to build, deploy, and operate cloud-native applications across multiple cloud providers.

### Core Features

- **Multi-Cloud Support**: Deploy to AWS, Azure, GCP, DigitalOcean, Civo, or locally with Kind
- **GitOps Operations**: Declarative, version-controlled infrastructure management
- **Self-Service Portal**: Developer-friendly console for all platform operations
- **Integrated Toolchain**: 60+ pre-configured tools for complete SDLC
- **Security by Default**: Zero-trust networking, policy enforcement, and compliance

---

## Platform Capabilities

### 🎯 Core Components

#### Networking (Cilium)
- **eBPF-based networking** for high performance
- **Network policies** for security
- **Service mesh** capabilities
- **Hubble UI** for network observability

#### GitOps (ArgoCD + Gitea)
- **ArgoCD**: Continuous deployment engine
- **Gitea**: Internal Git repository hosting
- **Automated sync**: Git-to-cluster synchronization
- **Multi-environment**: Dev, staging, production

#### Infrastructure as Code (Crossplane)
- **Multi-cloud provisioning**: Unified API for all providers
- **Composition**: Reusable infrastructure patterns
- **Policy enforcement**: Guardrails and compliance
- **Drift detection**: Automatic reconciliation

### 📦 Platform Services (60+ Tools)

#### Developer Tools
- **Adhar Console**: Self-service portal
- **JupyterHub**: Interactive notebooks
- **Code Server**: Browser-based VS Code
- **Harbor**: Container registry
- **Kaniko**: Container builds

#### Security & Identity
- **Keycloak**: Identity and access management
- **Vault**: Secrets management
- **Kyverno**: Policy engine
- **Falco**: Runtime security
- **Trivy**: Vulnerability scanning

#### Observability
- **Prometheus**: Metrics collection
- **Grafana**: Visualization and dashboards
- **Loki**: Log aggregation
- **Jaeger**: Distributed tracing
- **Tempo**: Trace backend

#### Data & Analytics
- **PostgreSQL**: Relational database
- **Redis**: In-memory data store
- **MinIO**: Object storage
- **Kafka**: Event streaming
- **Elasticsearch**: Search and analytics

#### CI/CD
- **Argo Workflows**: Workflow engine
- **Tekton**: Cloud-native CI/CD
- **Drone**: Container-native CI
- **Kaniko**: Serverless builds

---

## Configuration

### Configuration File Structure

Adhar uses a YAML configuration file (`config.yaml`) for platform setup:

```yaml
# Global Settings
globalSettings:
  adharContext: "adhar-mgmt"
  defaultHost: "cloud.adhar.io"
  defaultHttpPort: 80
  defaultHttpsPort: 443
  enableHAMode: false
  email: "YOUR_EMAIL@example.com"

# Provider Configuration
providers:
  # Local development (enabled by default)
  kind:
    type: kind
    region: local
    primary: true

  # Cloud providers (uncomment to enable)
  # aws:
  #   type: aws
  #   region: us-east-1
  #   credentials_file: "~/.aws/credentials"
  
  # gcp:
  #   type: gcp
  #   region: us-central1
  #   credentials_file: "~/.config/gcloud/credentials.json"

# Environment Templates
environmentTemplates:
  prod-defaults:
    clusterConfig:
      - key: "autoScale"
        value: "true"
      - key: "minNodes"
        value: "3"
```

### Provider Configuration

#### Kind (Local Development)
```yaml
kind:
  type: kind
  region: local
  primary: true
  config:
    cluster_config:
      networking:
        disable_default_cni: false
      nodes:
        - role: control-plane
        - role: worker
```

#### AWS (EKS)
```yaml
aws:
  type: aws
  region: us-east-1
  credentials_file: "~/.aws/credentials"
  config:
    vpc_cidr: "10.0.0.0/16"
    instance_types:
      control_plane: "t3.medium"
      worker: "t3.medium"
```

#### GCP (GKE)
```yaml
gcp:
  type: gcp
  region: us-central1
  credentials_file: "~/.config/gcloud/credentials.json"
  config:
    project_id: "YOUR_PROJECT_ID"
    machine_type: "e2-medium"
```

---

## CLI Reference

### Core Commands

#### `adhar up`
Create and provision a platform cluster.

```bash
# Local development cluster
adhar up

# Production cluster with config
adhar up -f config.yaml

# Specific provider
adhar up --provider aws --region us-east-1
```

#### `adhar down`
Tear down a cluster and clean up resources.

```bash
# Destroy local cluster
adhar down

# Destroy specific cluster
adhar down --context adhar-prod
```

#### `adhar get`
Retrieve platform resources and status.

```bash
# Get all resources
adhar get all

# Get specific resource type
adhar get applications
adhar get environments
adhar get clusters
```

### Application Management

#### `adhar apps deploy`
Deploy applications to the platform.

```bash
# Deploy from Git repository
adhar apps deploy my-app \
  --repo https://github.com/org/repo \
  --path manifests/ \
  --dest-namespace default

# Deploy with specific environment
adhar apps deploy my-app \
  --repo https://github.com/org/repo \
  --environment production
```

#### `adhar apps list`
List all deployed applications.

```bash
# List all applications
adhar apps list

# List apps in specific namespace
adhar apps list --namespace production
```

#### `adhar apps delete`
Remove an application.

```bash
# Delete application
adhar apps delete my-app

# Force delete
adhar apps delete my-app --force
```

### Cluster Management

#### `adhar cluster create`
Create a new Kubernetes cluster.

```bash
# Create cluster with provider
adhar cluster create prod-cluster \
  --provider gcp \
  --region us-central1 \
  --nodes 3

# Create with specific configuration
adhar cluster create staging-cluster \
  --config staging-config.yaml
```

#### `adhar cluster list`
List all managed clusters.

```bash
adhar cluster list
```

#### `adhar cluster kubeconfig`
Get kubeconfig for a cluster.

```bash
# Get kubeconfig
adhar cluster kubeconfig prod-cluster

# Save to file
adhar cluster kubeconfig prod-cluster > prod-kubeconfig.yaml
```

### Environment Management

#### `adhar env create`
Create a new environment.

```bash
# Create development environment
adhar env create dev \
  --provider digitalocean \
  --template nonprod-defaults

# Create production environment
adhar env create prod \
  --provider aws \
  --template prod-defaults \
  --ha-mode
```

#### `adhar env list`
List all environments.

```bash
adhar env list
```

---

## Platform Services

### Accessing Services

All platform services are accessible via the Adhar Console at `https://adhar.localtest.me/` (local) or your configured domain.

#### ArgoCD
- **URL**: `https://adhar.localtest.me/argocd/`
- **Purpose**: GitOps continuous delivery
- **Credentials**: Run `adhar get secrets argocd`

#### Gitea
- **URL**: `https://adhar.localtest.me/gitea/`
- **Purpose**: Git repository hosting
- **Credentials**: Run `adhar get secrets gitea`

#### Harbor
- **URL**: `https://adhar.localtest.me/harbor/`
- **Purpose**: Container registry
- **Credentials**: Run `adhar get secrets harbor`

#### Grafana
- **URL**: `https://adhar.localtest.me/grafana/`
- **Purpose**: Metrics visualization
- **Credentials**: Run `adhar get secrets grafana`

---

## Monitoring & Observability

### Metrics (Prometheus + Grafana)

Adhar includes pre-configured Prometheus for metrics collection and Grafana for visualization.

**Access Dashboards**:
```bash
# Open Grafana
open https://adhar.localtest.me/grafana/

# Pre-installed dashboards:
# - Kubernetes Cluster Monitoring
# - Node Exporter
# - Application Metrics
# - ArgoCD Metrics
```

### Logs (Loki)

Centralized log aggregation with Loki.

**Query Logs**:
```bash
# View logs in Grafana
# Navigate to Explore > Select Loki data source

# Query syntax examples:
# {namespace="default"}
# {app="my-app"} |= "error"
# {container="nginx"}
```

### Tracing (Jaeger)

Distributed tracing for microservices.

**Access Jaeger UI**:
```bash
open https://adhar.localtest.me/jaeger/
```

---

## Security & Compliance

### Network Policies

Adhar uses Cilium network policies for micro-segmentation.

**Example Policy**:
```yaml
apiVersion: cilium.io/v2
kind:CiliumNetworkPolicy
metadata:
  name: allow-frontend-to-backend
spec:
  endpointSelector:
    matchLabels:
      app: backend
  ingress:
    - fromEndpoints:
        - matchLabels:
            app: frontend
      toPorts:
        - ports:
            - port: "8080"
```

### Policy Enforcement (Kyverno)

Automated policy enforcement for compliance.

**Example Policies**:
- Require resource limits
- Block privileged containers
- Enforce label standards
- Require network policies

### Security Scanning

Automated vulnerability scanning with Trivy.

```bash
# Scan container image
trivy image myapp:latest

# Scan Kubernetes manifests
trivy config k8s-manifests/
```

---

## Best Practices

### Development Workflow

1. **Local Development**: Use `adhar up` for local Kind cluster
2. **Git Workflow**: Commit changes to Git repositories
3. **GitOps Deployment**: ArgoCD automatically syncs changes
4. **Testing**: Test in development environment first
5. **Promotion**: Promote to staging, then production

### Resource Management

- **Set Resource Limits**: Always define CPU/memory limits
- **Use Namespaces**: Isolate workloads by namespace
- **Apply Labels**: Use consistent labeling strategy
- **Monitor Usage**: Regular review of resource consumption

### Security Practices

- **Least Privilege**: Use RBAC for minimal permissions
- **Secrets Management**: Store secrets in Vault
- **Network Policies**: Implement network segmentation
- **Regular Scanning**: Automate security scans
- **Audit Logs**: Enable and review audit logs

### Cost Optimization

- **Right-Size Resources**: Match resources to actual needs
- **Auto-Scaling**: Enable cluster and pod autoscaling
- **Spot Instances**: Use spot/preemptible instances for non-critical workloads
- **Resource Cleanup**: Remove unused resources
- **Cost Monitoring**: Track costs per environment

---

## Troubleshooting

### Common Issues

#### Pod Not Starting
```bash
# Check pod status
kubectl get pods -n <namespace>

# View pod events
kubectl describe pod <pod-name> -n <namespace>

# Check logs
kubectl logs <pod-name> -n <namespace>
```

#### Service Not Accessible
```bash
# Check service
kubectl get svc -n <namespace>

# Check endpoints
kubectl get endpoints -n <namespace>

# Test connectivity
kubectl run test --rm -it --image=curlimages/curl -- curl http://service-name
```

#### ArgoCD Sync Issues
```bash
# Check application status
kubectl get application -n argocd

# View sync status
argocd app get <app-name>

# Force sync
argocd app sync <app-name>
```

### Getting Help

- **Documentation**: Check this guide and other docs
- **CLI Help**: Run `adhar <command> --help`
- **GitHub Issues**: [github.com/adhar-io/adhar/issues](https://github.com/adhar-io/adhar/issues)
- **Logs**: Check platform logs with `adhar logs`

---

## Next Steps

- **[Architecture Guide](ARCHITECTURE.md)**: Learn about platform architecture
- **[Provider Guide](PROVIDER_GUIDE.md)**: Deep dive into providers
- **[Advanced Guide](ADVANCED.md)**: HA mode and production practices
- **[Contributing](../CONTRIBUTING.md)**: Contribute to the project

---

**Need help?** Check our [GitHub Issues](https://github.com/adhar-io/adhar/issues) or reach out to the community!

