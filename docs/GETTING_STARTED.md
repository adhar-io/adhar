# Getting Started with Adhar Platform

This comprehensive guide will help you get started with the Adhar Platform, from installation to deploying your first applications.

## Prerequisites

Before you begin, ensure you have the following tools installed on your system:

- **Docker**: Required for running containers. [Install Docker](https://docs.docker.com/get-docker/)
- **kubectl**: Command-line tool for interacting with Kubernetes clusters. [Install kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)

## Installation

### Option 1: Quick Install (Recommended)

The following command can be used as a convenience for installing `adhar`:

```bash
# Install Adhar CLI
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/hack/install.sh | bash

# Verify installation
adhar version
```

### Option 2: Manual Installation

Alternatively, you can download the latest binary from [the latest release page](https://github.com/adhar-io/adhar/releases/latest).

## Quick Start

### 1. Basic Local Setup

The most basic command creates a local Kubernetes cluster (Kind cluster) with core packages:

```bash
adhar up
```

This command will:

- Create a local Kubernetes cluster using Kind
- Install core platform services (ArgoCD, Gitea, Nginx Ingress, Keycloak)
- Set up basic authentication and networking

### 2. Access Platform Services

Once provisioning is complete, you can access the platform services:

- **Adhar Console**: <https://adhar.localtest.me/> (Self-service portal)
- **ArgoCD**: <https://adhar.localtest.me/argocd/> (GitOps deployments)
- **Gitea**: <https://adhar.localtest.me/gitea/> (Git repositories)
- **Keycloak**: <https://adhar.localtest.me/keycloak/> (Identity management)
- **Headlamp**: <https://adhar.localtest.me/headlamp/> (Kubernetes UI)
- **JupyterHub**: <https://adhar.localtest.me/jupyterhub/> (Notebooks)

### 3. Get Credentials

Retrieve credentials for the platform services:

```bash
adhar get secrets
```

Default credentials:

- **Admin Users**: `user1` / `USER_PASSWORD` (from secrets output)
- **Regular Users**: `user2` / `USER_PASSWORD` (from secrets output)
- **Keycloak Admin**: `adhar-admin` / `KEYCLOAK_ADMIN_PASSWORD` (from secrets output)

### 4. Tear Down

To remove the entire cluster:

```bash
adhar down
```

## Cloud Deployment

For production deployments, you can deploy to any supported cloud provider:

### Single Provider Setup

```bash
# Create configuration file
cat > my-config.yaml << EOF
globalSettings:
  provider: "gke"  # or aws, azure, do, civo
  region: "us-east1-a"
  
cluster:
  name: "my-adhar-cluster"
  version: "1.30"
  
environments:
  dev:
    template: development-defaults
  prod:
    template: production-defaults
EOF

# Deploy to cloud
adhar up -f my-config.yaml
```

### Multi-Provider Setup

```bash
# Create dual-provider configuration
cat > dual-config.yaml << EOF
globalSettings:
  productionProvider: "gke"        # Production workloads
  productionRegion: "us-east1-a"
  nonProductionProvider: "do"      # Cost-effective dev/test
  nonProductionRegion: "nyc3"
  
cluster:
  name: "adhar-management"
  type: "management"
  
environments:
  dev:
    template: development-defaults
    # Uses nonProductionProvider (DigitalOcean)
  staging:
    type: production
    template: staging-defaults
    # Uses productionProvider (GKE)
  prod:
    type: production
    template: production-defaults
    # Uses productionProvider (GKE)
EOF

# Deploy dual-provider setup
adhar up -f dual-config.yaml
```

## Core Platform Services

The Adhar platform includes these core services:

### GitOps & CI/CD

- **ArgoCD**: Declarative continuous deployment
- **Argo Workflows**: Container-native workflow engine
- **Argo Events**: Event-driven workflow automation
- **Argo Rollouts**: Advanced deployment capabilities

### Source Control & Artifacts

- **Gitea**: Self-hosted Git service
- **Harbor**: Container image registry with security scanning
- **Kaniko**: Container image building
- **Paketo Buildpacks**: Cloud-native buildpack implementations

### Observability

- **Prometheus**: Metrics collection and alerting
- **Grafana**: Visualization and dashboards
- **Grafana Loki**: Log aggregation
- **Grafana Tempo**: Distributed tracing
- **Jaeger**: End-to-end distributed tracing

### Security & Governance

- **Keycloak**: Identity and access management
- **Cert Manager**: Certificate management
- **HashiCorp Vault**: Secrets management
- **Kyverno**: Policy management
- **Trivy**: Security scanning
- **Falco**: Runtime security

### Infrastructure

- **Cilium**: eBPF-based networking and security
- **Nginx Ingress**: Ingress controller
- **External DNS**: DNS management
- **Crossplane**: Infrastructure as code
- **Velero**: Backup and restore

## Next Steps

After getting started with Adhar:

1. **Explore the Console**: Use the Adhar Console to deploy your first application
2. **Set Up GitOps**: Configure ArgoCD for your application deployments
3. **Configure Monitoring**: Set up dashboards and alerts for your services
4. **Implement Security**: Configure policies and security scanning
5. **Scale Your Platform**: Add more environments and services as needed

## Getting Help

- **Documentation**: Check the [comprehensive guides](README.md)
- **Community**: Join our [Slack channel](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)
- **Issues**: Report bugs or request features on [GitHub](https://github.com/adhar-io/adhar/issues)
- **Contribution**: See our [contribution guidelines](../CONTRIBUTING.md)

## Troubleshooting

### Common Issues

**Installation fails with permission errors:**

```bash
# Ensure Docker is running and your user has access
sudo usermod -aG docker $USER
# Log out and back in
```

**Cannot access platform services:**

```bash
# Check if services are running
kubectl get pods -A
# Check ingress configuration
kubectl get ingress -A
```

**Secrets command fails:**

```bash
# Ensure cluster is running and kubectl is configured
kubectl cluster-info
adhar get secrets
```

For more detailed troubleshooting, see the [Advanced Guide](ADVANCED.md#troubleshooting).
