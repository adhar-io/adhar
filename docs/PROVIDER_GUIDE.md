# Adhar Provider System Guide

**Document Version**: 1.0  
**Last Updated**: January 15, 2025  
**Status**: Production Ready ✅

## Overview

The Adhar Provider System is the core architecture that enables unified Kubernetes platform management across multiple cloud providers. This guide provides comprehensive technical documentation for developers, platform engineers, and operators working with the Adhar platform.

---

## 🏗️ Architecture Overview

### Core Components

The Adhar Provider System consists of four main components:

1. **Provider Interface**: Unified API for all cloud platforms
2. **ProviderManager**: Orchestration and provider selection logic
3. **Template Engine**: KCL-based manifest generation system
4. **CLI Integration**: Unified command-line experience

```
┌─────────────────────────────────────────────────────────────────┐
│                    Adhar Provider System                       │
│                                                                │
│  ┌─────────────┐    ┌──────────────────────────────────────┐  │
│  │    CLI      │    │           ProviderManager            │  │
│  │  Commands   │◄──►│                                      │  │
│  └─────────────┘    │  • Provider Selection                │  │
│                      │  • Configuration Validation         │  │
│                      │  • Dry-run Support                  │  │
│                      │  • Error Handling                   │  │
│                      └──────────────────────────────────────┘  │
│                                       │                       │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │                Provider Implementations                  │  │
│  │                                                         │  │
│  │ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐   │  │
│  │ │   Kind   │ │ Digital  │ │   GCP    │ │   AWS    │   │  │
│  │ │Provider  │ │ Ocean    │ │Provider  │ │Provider  │   │  │
│  │ └──────────┘ │Provider  │ └──────────┘ └──────────┘   │  │
│  │              └──────────┘                             │  │
│  │ ┌──────────┐ ┌──────────┐                             │  │
│  │ │  Azure   │ │   Civo   │                             │  │
│  │ │Provider  │ │Provider  │                             │  │
│  │ └──────────┘ └──────────┘                             │  │
│  └─────────────────────────────────────────────────────────┘  │
│                              │                                │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │                Template Engine                          │  │
│  │         • KCL Configuration                            │  │
│  │         • YAML Template Processing                     │  │
│  │         • HA Mode Support                             │  │
│  │         • Service-specific Patches                    │  │
│  └─────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 🔌 Provider Interface

### Interface Definition

All providers implement the unified `Provider` interface:

```go
type Provider interface {
    // Core cluster lifecycle methods
    Provision(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error
    Destroy(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig, opts ProvisionOptions) error
    Exists(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (bool, error)
    
    // Platform service management
    InstallPlatformServices(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error
    
    // Cluster operations
    ValidateCluster(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) error
    GetClusterInfo(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (*ClusterInfo, error)
    GetKubeConfig(ctx context.Context, envConfig *config.ResolvedEnvironmentConfig) (string, error)
}
```

### Method Responsibilities

#### `Provision()`
- Creates and configures Kubernetes clusters
- Handles provider-specific networking and security setup
- Supports dry-run mode for testing configurations
- Returns detailed error information for troubleshooting

#### `Destroy()`
- Safely destroys clusters and associated resources
- Handles graceful shutdown and resource cleanup
- Supports dry-run mode for impact assessment
- Validates cluster existence before destruction

#### `Exists()`
- Checks if a cluster exists without modifying state
- Used for idempotent operations and validation
- Fast execution for status checking
- Provider-specific cluster identification

#### `InstallPlatformServices()`
- Deploys core platform services (Cilium, ArgoCD, Gitea, Nginx)
- Uses Template Engine for manifest generation
- Configures GitOps workflows
- Handles service dependencies and ordering

#### `ValidateCluster()`
- Verifies cluster health and readiness
- Checks node status and networking
- Validates platform service deployment
- Returns comprehensive validation results

#### `GetClusterInfo()`
- Returns cluster metadata and status information
- Provides resource usage and configuration details
- Used for monitoring and management
- Standardized across all providers

#### `GetKubeConfig()`
- Retrieves kubeconfig for cluster access
- Handles provider-specific authentication
- Saves config to local filesystem
- Supports multiple cluster contexts

---

## 🎯 Supported Providers

### Local Development

#### Kind Provider
**Purpose**: Local development and testing

**Features**:
- Single-node Kubernetes clusters using Docker
- Port forwarding for service access
- Local persistent storage
- Fast cluster creation/destruction
- No cloud costs or credentials required

**Configuration**:
```yaml
environments:
  local:
    provider: kind
    name: adhar-local
    type: development
```

**Use Cases**:
- Local development workflows
- CI/CD pipeline testing
- Learning and experimentation
- Offline development

### Cloud Providers

#### DigitalOcean Provider
**Purpose**: Cost-effective production deployments

**Features**:
- DigitalOcean Kubernetes (DOKS) integration
- Node pool management with auto-scaling
- VPC and Load Balancer integration
- Competitive pricing for small/medium workloads

**Configuration**:
```yaml
environments:
  production:
    provider: digitalocean
    name: adhar-prod
    region: nyc3
    clusterConfig:
      - key: node_size
        value: s-2vcpu-4gb
      - key: node_count
        value: "3"
      - key: auto_scale
        value: "true"
```

**Authentication**:
```bash
export DIGITALOCEAN_TOKEN="your-do-token"
```

#### Google Cloud Provider (GCP)
**Purpose**: Enterprise-grade deployments with Google services

**Features**:
- Google Kubernetes Engine (GKE) integration
- Autopilot and Standard mode support
- Workload Identity integration
- Advanced networking with VPC-native clusters
- Google Cloud service integration

**Configuration**:
```yaml
environments:
  production:
    provider: gcp
    name: adhar-gcp-prod
    region: us-central1
    clusterConfig:
      - key: machine_type
        value: e2-standard-4
      - key: disk_size
        value: "50"
      - key: node_count
        value: "3"
```

**Authentication**:
```bash
gcloud auth application-default login
# OR
export GOOGLE_APPLICATION_CREDENTIALS="path-to-service-account.json"
```

#### Amazon Web Services (AWS)
**Purpose**: Enterprise deployments with AWS ecosystem integration

**Features**:
- Elastic Kubernetes Service (EKS) integration
- Managed node groups with auto-scaling
- IAM roles for service accounts (IRSA)
- VPC and subnet configuration
- AWS service integration

**Configuration**:
```yaml
environments:
  production:
    provider: aws
    name: adhar-aws-prod
    region: us-west-2
    clusterConfig:
      - key: instance_type
        value: m5.large
      - key: desired_capacity
        value: "3"
      - key: min_size
        value: "1"
      - key: max_size
        value: "10"
```

**Authentication**:
```bash
aws configure
# OR
export AWS_ACCESS_KEY_ID="your-access-key"
export AWS_SECRET_ACCESS_KEY="your-secret-key"
```

#### Microsoft Azure Provider
**Purpose**: Enterprise deployments in Azure ecosystem

**Features**:
- Azure Kubernetes Service (AKS) integration
- Azure Active Directory integration
- Virtual network and subnet configuration
- Azure service integration
- Auto-scaling and node pool management

**Configuration**:
```yaml
environments:
  production:
    provider: azure
    name: adhar-azure-prod
    region: East US
    clusterConfig:
      - key: node_vm_size
        value: Standard_D2s_v3
      - key: node_count
        value: "3"
      - key: enable_auto_scaling
        value: "true"
```

**Authentication**:
```bash
az login
# OR
export AZURE_CLIENT_ID="your-client-id"
export AZURE_CLIENT_SECRET="your-client-secret"
export AZURE_TENANT_ID="your-tenant-id"
```

#### Civo Provider
**Purpose**: Fast, cost-effective development and staging environments

**Features**:
- Civo Kubernetes integration (K3s-based)
- Fast cluster provisioning (< 5 minutes)
- Cost-effective pricing
- Simple node configuration
- Developer-friendly experience

**Configuration**:
```yaml
environments:
  staging:
    provider: civo
    name: adhar-civo-staging
    region: LON1
    clusterConfig:
      - key: node_size
        value: g4s.kube.medium
      - key: node_count
        value: "3"
```

**Authentication**:
```bash
export CIVO_API_KEY="your-civo-api-key"
```

---

## 🎛️ ProviderManager

### Responsibilities

The `ProviderManager` is the central orchestrator that:

1. **Provider Selection**: Automatically selects the correct provider based on configuration
2. **Configuration Validation**: Validates environment configuration before provisioning
3. **Dry-run Support**: Enables safe testing without actual resource creation
4. **Error Handling**: Provides consistent error handling across all providers
5. **Lifecycle Management**: Coordinates the complete platform lifecycle

### Usage Example

```go
// Create ProviderManager
manager := NewProviderManager(logger, templateEngine)

// Provision with dry-run
opts := ProvisionOptions{
    DryRun: true,
    Force:  false,
}

err := manager.ProvisionEnvironment(ctx, envConfig, opts)
if err != nil {
    log.Fatalf("Dry-run failed: %v", err)
}

// Actual provisioning
opts.DryRun = false
err = manager.ProvisionEnvironment(ctx, envConfig, opts)
```

### Provider Selection Logic

```go
func (pm *ProviderManager) selectProvider(envConfig *config.ResolvedEnvironmentConfig) (Provider, error) {
    switch envConfig.Provider {
    case config.ProviderKind:
        return NewKindProvider(envConfig, pm.logger, pm.templateEngine)
    case config.ProviderDigitalOcean:
        return NewDigitalOceanProvider(envConfig, pm.logger, pm.templateEngine)
    case config.ProviderGCP:
        return NewGCPProvider(envConfig, pm.logger, pm.templateEngine)
    case config.ProviderAWS:
        return NewAWSProvider(envConfig, pm.logger, pm.templateEngine)
    case config.ProviderAzure:
        return NewAzureProvider(envConfig, pm.logger, pm.templateEngine)
    case config.ProviderCivo:
        return NewCivoProvider(envConfig, pm.logger, pm.templateEngine)
    default:
        return nil, fmt.Errorf("unsupported provider: %s", envConfig.Provider)
    }
}
```

---

## 📄 Template Engine

### Overview

The Template Engine generates Kubernetes manifests using KCL configuration and YAML templates. It provides consistent manifest generation across all providers with support for:

- KCL-based configuration
- HA mode scaling
- Service-specific patches
- Environment-specific customization

### Architecture

```
Template Engine Components:
├── KCL Configuration (config.k)
├── YAML Templates (platform/build/templates/)
├── Service Patches (per-service customization)
└── HA Mode Support (replica scaling)
```

### KCL Configuration

The KCL configuration file (`platform/build/templates/config.k`) defines:

```python
# Global settings
global_settings = {
    "namespace": "argocd",
    "domain": "localtest.me",
    "enable_ha": False
}

# Service configurations
services = {
    "argocd": {
        "replicas": 1,
        "replicas_ha": 3,
        "resources": {
            "requests": {"cpu": "100m", "memory": "128Mi"},
            "limits": {"cpu": "500m", "memory": "512Mi"}
        }
    },
    "gitea": {
        "replicas": 1,
        "replicas_ha": 2,
        "database": {
            "type": "postgres",
            "replicas": 1,
            "replicas_ha": 3
        }
    }
}
```

### Template Directory Structure

```
platform/build/templates/
├── config.k                 # KCL configuration
├── platform-apps/          # Core platform services
│   ├── argocd/
│   │   ├── install.yaml
│   │   └── patches/
│   ├── cilium/
│   │   ├── install.yaml
│   │   └── patches/
│   ├── gitea/
│   │   ├── install.yaml
│   │   └── patches/
│   └── nginx/
│       ├── install.yaml
│       └── patches/
└── overlays/               # Environment-specific overlays
    ├── development/
    ├── staging/
    └── production/
```

### Manifest Generation Process

1. **Load KCL Configuration**: Parse config.k for service settings
2. **Load Base Templates**: Read YAML manifests for requested service
3. **Apply Patches**: Apply service-specific modifications
4. **Scale for HA**: Adjust replicas based on HA mode setting
5. **Generate Final Manifest**: Combine all elements into deployable YAML

### Usage Example

```go
// Create template engine
logger := logrus.New()
templateEngine := NewTemplateEngine(logger)

// Generate manifests for ArgoCD
manifests, err := templateEngine.GenerateManifests(ctx, "argocd", enableHAMode)
if err != nil {
    return fmt.Errorf("failed to generate manifests: %w", err)
}

// Apply manifests using kubectl
err = applyManifests(ctx, kubeconfig, manifests, "argocd")
```

---

## 🚀 Platform Services

### Core Services

The Adhar platform deploys four core services on every cluster:

#### 1. Cilium (CNI & Service Mesh)
- **Purpose**: Container networking and security
- **Features**: eBPF-based networking, network policies, service mesh
- **Installation**: Template engine with provider-specific patches

#### 2. ArgoCD (GitOps Controller)
- **Purpose**: Continuous deployment and GitOps workflows
- **Features**: Application management, sync policies, webhooks
- **Installation**: Template engine with HA configuration

#### 3. Gitea (Git Repository)
- **Purpose**: Git repository management
- **Features**: Git hosting, CI/CD integration, user management
- **Installation**: Template engine with database configuration

#### 4. Nginx (Ingress Controller)
- **Purpose**: HTTP/HTTPS traffic routing
- **Features**: SSL termination, path-based routing, load balancing
- **Installation**: Template engine with provider-specific LoadBalancer

### Installation Strategy

The platform uses a two-phase installation approach:

**Phase 1: Core Infrastructure**
- Install Cilium, Nginx, and Gitea using Template Engine
- Wait for services to be ready
- Validate network connectivity

**Phase 2: GitOps Management**
- Install ArgoCD using Template Engine
- Deploy platform stack ApplicationSets
- Configure ArgoCD to manage additional services

### Service Dependencies

```
Installation Order:
1. Cilium (CNI) → Network foundation
2. Nginx (Ingress) → Traffic routing
3. Gitea (Git) → Source code management
4. ArgoCD (GitOps) → Continuous deployment
5. Platform Stack → Additional services via ArgoCD
```

---

## 🔧 Configuration System

### Configuration Schema

The Adhar configuration uses a hierarchical structure:

```yaml
apiVersion: v1alpha1
kind: Config

# Global settings applied to all environments
globalSettings:
  adharContext: "adhar-platform"
  defaultHost: "localtest.me"
  enableHAMode: false

# Reusable environment templates
environmentTemplates:
  production:
    type: production
    provider: gcp
    region: us-central1
    clusterConfig:
      - key: machine_type
        value: e2-standard-4

# Specific environment configurations
environments:
  prod-gcp:
    template: production
    name: adhar-prod
    region: us-west2  # Override template region
```

### Configuration Resolution

The configuration system resolves environments in this order:

1. **Load base configuration** from YAML file
2. **Apply environment template** if specified
3. **Override with environment-specific** settings
4. **Apply global settings** where not overridden
5. **Validate final configuration** against schema

### Provider-Specific Configuration

Each provider supports specific configuration options:

#### Kind Configuration
```yaml
clusterConfig:
  - key: cluster_name
    value: adhar-local
  - key: port_mappings
    value: "80:30080,443:30443"
```

#### Cloud Provider Configuration
```yaml
clusterConfig:
  # Common options
  - key: kubernetes_version
    value: "1.28"
  - key: node_count
    value: "3"
  
  # Provider-specific options
  - key: instance_type     # AWS
    value: m5.large
  - key: machine_type      # GCP
    value: e2-standard-4
  - key: node_vm_size      # Azure
    value: Standard_D2s_v3
  - key: node_size         # DigitalOcean/Civo
    value: s-2vcpu-4gb
```

---

## 🎮 CLI Integration

### Command Structure

The Adhar CLI provides a unified interface for all providers:

```bash
# Local development (Kind)
adhar up                                    # Zero-config local setup

# Production deployment
adhar up -f config.yaml                     # All environments in config
adhar up -f config.yaml -e production       # Specific environment

# Dry-run testing
adhar up -f config.yaml --dry-run           # Test configuration
adhar up -f config.yaml -e staging --dry-run

# Environment management
adhar get envs -f config.yaml               # List environments
adhar get status                            # Platform status

# Cleanup
adhar down                                  # Local development
adhar down -f config.yaml -e staging        # Specific environment
```

### CLI Implementation

The CLI uses the ProviderManager for all operations:

```go
func runUp(cmd *cobra.Command, args []string) error {
    // Load configuration
    config, err := loadConfiguration(configFile)
    if err != nil {
        return err
    }

    // Resolve environment
    envConfig, err := resolveEnvironment(config, environmentName)
    if err != nil {
        return err
    }

    // Create provider manager
    manager := build.NewProviderManager(logger, templateEngine)

    // Set provision options
    opts := build.ProvisionOptions{
        DryRun: dryRun,
        Force:  force,
    }

    // Provision environment
    return manager.ProvisionEnvironment(cmd.Context(), envConfig, opts)
}
```

### Error Handling

The CLI provides comprehensive error handling:

- **Configuration Errors**: Detailed schema validation messages
- **Authentication Errors**: Provider-specific credential guidance
- **Resource Errors**: Cloud provider error translation
- **Network Errors**: Connectivity and timeout information

---

## 🧪 Testing and Validation

### Dry-Run Mode

All providers support dry-run mode for safe testing:

```bash
# Test configuration without creating resources
adhar up -f config.yaml --dry-run

# Validate specific environment
adhar up -f config.yaml -e production --dry-run
```

Dry-run mode:
- Validates configuration syntax and semantics
- Checks provider authentication
- Verifies resource quotas and permissions
- Shows what would be created
- Estimates costs (where available)

### Test Configurations

The platform includes comprehensive test configurations:

- `kind-local-config.yaml`: Local development testing
- `test-config.yaml`: Multi-environment testing
- `digitalocean-test-config.yaml`: DigitalOcean testing
- `gcp-test-config.yaml`: Google Cloud testing
- `aws-test-config.yaml`: AWS testing
- `azure-test-config.yaml`: Azure testing
- `civo-test-config.yaml`: Civo testing

### Validation Coverage

The testing system validates:

- ✅ Configuration parsing and resolution
- ✅ Provider selection logic
- ✅ Authentication and credentials
- ✅ Template engine manifest generation
- ✅ CLI command parsing and execution
- ✅ Error handling and user messaging

---

## 🔍 Troubleshooting

### Common Issues

#### Configuration Issues
```bash
# Problem: YAML syntax errors
# Solution: Use YAML validator
yamllint config.yaml

# Problem: Schema validation failures
# Solution: Check against JSON schema
ajv validate --spec=draft7 -s adhar-config.schema.json -d config.yaml
```

#### Authentication Issues
```bash
# DigitalOcean
export DIGITALOCEAN_TOKEN="your-token"

# GCP
gcloud auth application-default login

# AWS
aws configure

# Azure
az login

# Civo
export CIVO_API_KEY="your-api-key"
```

#### Cluster Issues
```bash
# Check cluster status
adhar get status

# Validate cluster connectivity
kubectl --kubeconfig .adhar/cluster-name/kubeconfig get nodes

# Check platform services
kubectl --kubeconfig .adhar/cluster-name/kubeconfig get pods -A
```

### Debug Mode

Enable debug logging for detailed troubleshooting:

```bash
export ADHAR_LOG_LEVEL=debug
adhar up -f config.yaml --dry-run
```

Debug mode provides:
- Detailed API request/response logging
- Configuration resolution steps
- Template generation details
- Provider selection logic
- Error stack traces

---

## 📊 Monitoring and Observability

### Built-in Monitoring

The platform includes observability for:

- **Cluster Health**: Node status, resource utilization
- **Service Status**: Platform service health checks
- **Deployment Progress**: Real-time provisioning status
- **Error Tracking**: Comprehensive error collection

### Integration Points

- **Prometheus**: Metrics collection and alerting
- **Grafana**: Visualization and dashboards
- **Jaeger**: Distributed tracing
- **Fluentd**: Log aggregation

### Metrics

Key metrics tracked:
- Cluster provisioning time
- Service deployment success rate
- Resource utilization
- Error rates by provider
- CLI command performance

---

## 🚀 Best Practices

### Configuration Management

1. **Use Environment Templates**: Reduce duplication with reusable templates
2. **Version Control**: Store configurations in Git repositories
3. **Validate Early**: Use dry-run mode before production deployment
4. **Separate Concerns**: Different configs for different environments

### Security

1. **Credential Management**: Use environment variables or cloud IAM
2. **Network Policies**: Enable Cilium network policies
3. **RBAC**: Configure appropriate role-based access control
4. **Secret Management**: Use Kubernetes secrets or external secret managers

### Operations

1. **Monitoring**: Enable comprehensive observability
2. **Backup**: Regular cluster and application backups
3. **Updates**: Keep platform services updated
4. **Documentation**: Maintain operational runbooks

### Development

1. **Local Testing**: Use Kind for development workflows
2. **Staging Environments**: Test on cloud providers before production
3. **CI/CD Integration**: Automate testing and deployment
4. **GitOps**: Manage configurations through Git workflows

---

## 🔮 Future Enhancements

### Planned Features

1. **Advanced Monitoring**: Enhanced observability stack
2. **Custom Packages**: Extensible package marketplace
3. **Multi-tenancy**: Namespace-based tenant isolation
4. **Cost Management**: Resource usage monitoring and optimization
5. **Edge Computing**: Edge deployment capabilities

### Integration Roadmap

1. **CI/CD Systems**: Jenkins, GitLab CI, GitHub Actions
2. **Monitoring Tools**: Datadog, New Relic, Splunk
3. **Security Tools**: Snyk, Aqua Security, Twistlock
4. **Business Tools**: Jira, Slack, Microsoft Teams

### Community

1. **Open Source**: Community-driven development
2. **Marketplace**: Community package contributions
3. **Documentation**: Community documentation improvements
4. **Integrations**: Community-developed integrations

---

## 📚 Additional Resources

### Documentation
- [Getting Started Guide](GETTING_STARTED.md)
- [Configuration Reference](USER_GUIDE.md#configuration)
- [Platform Capabilities](USER_GUIDE.md#platform-capabilities)
- [Architecture Overview](ARCHITECTURE.md)

### Examples
- [Example Configurations](examples/)
- [Platform Templates](samples/)
- [Integration Examples](examples/integrations/)

### Community
- [GitHub Repository](https://github.com/adhar-io/adhar)
- [Documentation Site](https://docs.adhar.io)
- [Community Slack](https://adhar-community.slack.com)
- [Contributing Guide](../CONTRIBUTING.md)

The Adhar Provider System represents a significant advancement in multi-cloud Kubernetes platform management, providing unprecedented flexibility and consistency across diverse cloud environments while maintaining a superior developer experience.
