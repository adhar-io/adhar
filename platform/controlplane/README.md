# Adhar Control Plane

This directory contains the **Crossplane v2.1** based control plane configuration that powers the Adhar platform's multi-cloud infrastructure management. The control-plane is automatically deployed when running `adhar up` and is built as part of the main `make build` process. It provides a comprehensive, declarative API surface for managing infrastructure, applications, and platform services through Crossplane composite resources. Each CLI command maps to one or more composite resources managed from this directory.

## Table of Contents
- [Overview](#overview)
- [What's New in v2.1](#whats-new-in-v21)
- [What Lives Here](#what-lives-here)
- [Architecture](#architecture)
- [Supported Providers](#supported-providers)
- [Composite Resources](#composite-resources)
- [Development Workflow](#development-workflow)
- [Building & Deploying](#building--deploying)
- [Credential Management](#credential-management)
- [Advanced Features](#advanced-features)
- [Examples](#examples)
- [Status & Roadmap](#status--roadmap)

## Overview

The control plane provides:
- **Multi-cloud cluster provisioning** across AWS (EKS), GCP (GKE), Azure (AKS), DigitalOcean (DOKS), Civo, Kind, and on-premises Kubernetes
- **Comprehensive cloud resources** including compute, networking, databases, storage, and IAM
- **Application lifecycle management** via ArgoCD with structured status reporting
- **GitOps workflows** with ArgoCD projects and application sets
- **Infrastructure resources** including databases, networks, backup policies, and auth stacks
- **Credential discovery** with automatic secret management for cloud providers
- **Advanced provider features** including logging, monitoring, identity, and networking
- **Kubernetes-native operations** for existing clusters (Kind, on-prem) via provider-kubernetes

## What's New in v2.1

### Crossplane 2.1 Upgrade
- ✅ Updated to Crossplane v2.1.0 with latest features and improvements
- ✅ Enhanced pipeline composition mode with better status reporting
- ✅ Improved function ecosystem with function-patch-and-transform
- ✅ Better resource lifecycle management and dependency handling

### Provider Expansion
- ✅ **AWS**: Modular providers (EKS, EC2, RDS, IAM, S3) for granular resource management
- ✅ **Azure**: Modular providers (Container Service, Network, SQL, Storage)
- ✅ **GCP**: Modular providers (Container, Compute, SQL, Storage)
- ✅ **DigitalOcean**: Updated to v0.7.0 with enhanced features
- ✅ **Civo**: Updated to v0.3.0 with improved K3s support
- ✅ **Kubernetes**: Added provider-kubernetes for Kind and on-prem cluster management
- ✅ **Helm**: Updated provider-helm for application deployment across all clusters

### New Capabilities
- ✅ **Kind/On-Prem Support**: Full composition for managing existing Kubernetes clusters
- ✅ **Add-on Management**: Automated installation of Cilium, Nginx, Metrics Server via Helm
- ✅ **Credential Templates**: Comprehensive secret templates for all providers
- ✅ **ProviderConfig Management**: Dedicated configs for each cloud service
- ✅ **Enhanced Credential Discovery**: Automatic detection and validation of credentials

## What Lives Here

```
platform/controlplane/
├── configuration/          # Crossplane package definition
│   ├── compositions/       # Provider-specific compositions
│   │   ├── apps/          # Application compositions
│   │   ├── auth/          # Authentication stack compositions
│   │   ├── cluster/       # Cluster compositions (EKS, GKE, AKS, DOKS, Civo)
│   │   └── gitops/        # GitOps compositions
│   ├── functions/         # Crossplane function dependencies
│   ├── providers/         # Provider configurations
│   └── xrd/              # Composite Resource Definitions
│       ├── apps.xrd.yaml
│       ├── auth.xrd.yaml
│       ├── backup.xrd.yaml
│       ├── cluster.xrd.yaml
│       ├── database.xrd.yaml
│       ├── gitops.xrd.yaml
│       └── network.xrd.yaml
├── docs/                  # Architecture and design docs
├── examples/              # Sample composite resources
├── features/              # CLI command → resource registry
│   └── registry.yaml
└── pkg/                   # Go helper libraries
    ├── credentials/       # Credential discovery & management
    ├── registry/          # Feature registry validation
    └── templates/         # Template rendering utilities
```

## Architecture

### Crossplane Composition Pipeline

The control plane uses Crossplane's Pipeline composition mode with the `fn-go-templating` function. This allows:
- Dynamic resource generation based on input parameters
- Complex conditional logic in compositions
- Status aggregation from managed resources back to composites
- Full Go template support with Sprig functions

### Credential Management

The new credential management system (`pkg/credentials/`) provides:
- **Multi-source discovery**: Environment variables, Kubernetes secrets, and credential files
- **Automatic secret creation**: Converts discovered credentials to Kubernetes secrets
- **Provider-specific validation**: Ensures all required credentials are present
- **Common secret naming**: Uses predictable secret names for each provider

Example providers supported:
- **AWS**: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` (5 modular providers)
- **Azure**: `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` (4 modular providers)
- **GCP**: `GOOGLE_CREDENTIALS` or `GOOGLE_APPLICATION_CREDENTIALS` (4 modular providers)
- **DigitalOcean**: `DIGITALOCEAN_TOKEN` or `DIGITALOCEAN_ACCESS_TOKEN`
- **Civo**: `CIVO_TOKEN` or `CIVO_API_KEY`
- **Kubernetes**: External cluster kubeconfig or in-cluster identity
- **Helm**: External cluster kubeconfig or in-cluster identity

## Supported Providers

### Cloud Providers

| Provider | Services | Version | Resources |
|----------|----------|---------|-----------|
| **AWS** | EKS, EC2, RDS, IAM, S3 | v1.7.0+ | Clusters, VPCs, Databases, Buckets |
| **Azure** | AKS, VNet, SQL, Storage | v1.3.0+ | Clusters, Networks, Databases, Storage |
| **GCP** | GKE, Compute, SQL, Storage | v1.7.0+ | Clusters, VPCs, Databases, Buckets |
| **DigitalOcean** | DOKS, Droplets, LB | v0.7.0+ | Clusters, Compute, Networking |
| **Civo** | K3s, Instances | v0.3.0+ | Clusters, Compute, Networks |

### Kubernetes Platforms

| Platform | Provider | Version | Use Case |
|----------|----------|---------|----------|
| **Kind** | provider-kubernetes | v0.14.0+ | Local development clusters |
| **On-Premises** | provider-kubernetes | v0.14.0+ | Self-managed Kubernetes |
| **Existing Clusters** | provider-kubernetes | v0.14.0+ | Any accessible K8s cluster |
| **Applications** | provider-helm | v0.19.0+ | Deploy apps to all clusters |

### Provider Architecture

**Modular Approach**: Cloud providers use a modular architecture where each service (e.g., EKS, EC2, RDS) is a separate provider. This enables:
- **Granular Updates**: Update specific services without affecting others
- **Reduced Blast Radius**: Issues in one provider don't impact others
- **Better Resource Isolation**: Each provider has its own controller and configuration
- **Smaller Image Sizes**: Only install providers you need

## Composite Resources

### Cluster (CompositeCluster)

Provisions Kubernetes clusters across multiple cloud providers and platforms with extensive configuration options.

**Key Features:**
- Multi-provider support (AWS EKS, GCP GKE, Azure AKS, DigitalOcean DOKS, Civo K3s, Kind, On-Premises)
- Multiple node pools with auto-scaling
- Advanced networking (VPCs, subnets, security groups)
- Logging and monitoring integration
- Identity and access management
- Encryption at rest
- High availability configurations
- Add-on management (Cilium, Nginx, Metrics Server)

**Enhanced Provider-Specific Features:**

*AWS EKS:*
- Control plane logging (API, audit, authenticator, controller manager, scheduler)
- IRSA (IAM Roles for Service Accounts) support
- KMS encryption for secrets
- Security group management
- Add-on management (vpc-cni, coredns, kube-proxy, etc.)

*GCP GKE:*
- Cloud Logging and Monitoring integration
- Workload Identity
- Binary Authorization
- Shielded nodes
- Intranode visibility
- Master authorized networks
- Network policy and add-on configuration

*Azure AKS:*
- Azure Monitor integration (OMS agent)
- Azure Policy
- Azure AD RBAC
- Managed identity support
- Auto-scaler profile configuration
- Private cluster support

*DigitalOcean & Civo:*
- Credential secret references for secure authentication
- HA mode support (DigitalOcean)
- Firewall integration
- Monitoring and alerting

*Kind & On-Premises Kubernetes:*
- Support for existing Kubernetes clusters
- In-cluster or external kubeconfig-based access
- Automated add-on installation via Helm
- Cluster health checking and connectivity verification
- ConfigMap-based metadata storage
- Works with Kind, k3s, k0s, microk8s, and any standard Kubernetes

**Example:**
```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: Cluster
metadata:
  name: production-eks
spec:
  compositionSelector:
    matchLabels:
      feature: cluster
      provider: aws
  parameters:
    provider: AWS_EKS
    region: us-west-2
    version: "1.28"
    controlPlane:
      highAvailability: true
      endpointAccess: PublicAndPrivate
    providerSettings:
      aws:
        loggingTypes:
          - api
          - audit
          - authenticator
        enableIRSA: true
        kmsKeyArn: arn:aws:kms:us-west-2:123456789012:key/12345678-1234-1234-1234-123456789012
        addonConfigs:
          - name: vpc-cni
            version: v1.15.0
            resolveConflicts: OVERWRITE
    networkConfig:
      vpcId: vpc-12345
      subnetIds:
        - subnet-abc123
        - subnet-def456
    nodePools:
      - name: system
        instanceType: t3.large
        count: 3
        minCount: 3
        maxCount: 5
  writeConnectionSecretToRef:
    name: production-eks-kubeconfig
    namespace: crossplane-system
```

### Application (CompositeApplication)

Manages application deployments through ArgoCD with structured condition mapping and detailed status reporting.

**Key Features:**
- Full ArgoCD Application spec support
- Helm, Kustomize, and directory source types
- Sync policies with auto-healing and pruning
- Ignore differences configuration
- **Enhanced status reporting** with structured conditions
- Resource-level health tracking
- Operation state monitoring

**Enhanced Status Fields:**
- `phase`: Overall application phase (Healthy, Syncing, Degraded, OutOfSync, Suspended, Missing, Pending)
- `operationState`: Current sync operation details with timestamps
- `conditions`: Structured ArgoCD conditions (type, status, reason, message, lastTransitionTime)
- `resources`: Summary of all managed resources with individual health status

**Example:**
```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: Application
metadata:
  name: my-service
spec:
  compositionSelector:
    matchLabels:
      feature: apps
  parameters:
    project: production
    source:
      repoURL: https://github.com/org/repo
      path: k8s/overlays/prod
      targetRevision: main
      kustomize:
        images:
          - myapp=myregistry/myapp:v1.2.3
    destination:
      server: https://kubernetes.default.svc
      namespace: production
    syncPolicy:
      automated:
        prune: true
        selfHeal: true
      syncOptions:
        - CreateNamespace=true
```

### GitOps (CompositeGitOps)

Manages ArgoCD Projects and ApplicationSets for multi-environment deployments.

**Key Features:**
- ArgoCD Project configuration with RBAC
- Source repository whitelisting
- Destination cluster/namespace management
- Cluster resource whitelisting
- ApplicationSet support for templated applications

### Database (CompositeDatabase)

Provisions managed database instances with backup, monitoring, and security.

**Key Features:**
- Multiple database engines (PostgreSQL, MySQL, MariaDB, MongoDB, Redis)
- Multi-AZ deployments
- Automated backups with retention policies
- Encryption at rest with KMS
- Performance Insights
- Enhanced monitoring
- Network isolation

### Network (CompositeNetwork)

Creates and manages cloud networking infrastructure.

**Key Features:**
- VPC/VNet creation with custom CIDR blocks
- Public, private, and isolated subnets
- Internet and NAT gateways
- Route tables and network ACLs
- VPC peering
- Flow logs for traffic analysis
- Multi-provider support

### AuthStack (CompositeAuthStack)

Deploys and configures identity and access management systems.

**Key Features:**
- Keycloak, Dex, or OAuth2-Proxy support
- Realm and client management
- User and group provisioning
- External identity provider federation
- RBAC policy enforcement
- MFA configuration
- Session management

### BackupPolicy (CompositeBackupPolicy)

Manages backup and disaster recovery policies.

**Key Features:**
- Velero or Stash integration
- Scheduled backups with cron expressions
- Resource filtering (namespaces, labels, resource types)
- Volume snapshots with Restic fallback
- Pre/post-backup hooks
- Encryption support
- Verification workflows
- Notification channels (Slack, email, webhooks)

## Development Workflow

1. **Design the API**: Create or update XRD in `configuration/xrd/`
   ```sh
   # XRDs define the schema for composite resources
   vim configuration/xrd/myresource.xrd.yaml
   ```

2. **Create Compositions**: Add provider-specific compositions
   ```sh
   # Compositions define how to provision resources
   vim configuration/compositions/myresource/provider-composition.yaml
   ```

3. **Register in Feature Registry**: Update `features/registry.yaml`
   ```yaml
   commands:
     - name: mycommand
       compositeKind: CompositeMyResource
       status: in-progress
       compositions:
         - name: provider-implementation
           readiness: alpha
   ```

4. **Add Helper Code**: Implement validation or templating in `pkg/`
   ```go
   // pkg/myresource/helpers.go
   package myresource
   
   func ValidateSpec(spec map[string]interface{}) error {
       // Validation logic
   }
   ```

5. **Test Locally**: Use Crossplane CLI to render compositions
   ```sh
   crossplane beta render examples/myresource.yaml \
     configuration/compositions/myresource/ \
     configuration/xrd/
   ```

6. **Build Package**: Generate the Crossplane package
   ```sh
   make build
   ```

## Building & Deploying

### Build the Package

```sh
cd control-plane
make build            # Produces adhar-control-plane.xpkg
make lint             # Validates manifests and registry
make test             # Runs unit tests
make clean            # Removes build artifacts
```

### Install to Cluster

```sh
# Install to existing Crossplane runtime
kubectl crossplane install configuration \
  file://adhar-control-plane.xpkg

# Or push to registry
kubectl crossplane push configuration \
  ghcr.io/yourorg/adhar-control-plane:v1.0.0 \
  adhar-control-plane.xpkg
```

### Install Dependencies

The package automatically installs required Crossplane providers:
- provider-kubernetes (for ArgoCD resources)
- provider-aws (for EKS)
- provider-gcp (for GKE)
- provider-azure (for AKS)
- provider-helm (for Helm releases)

## Credential Management

### Using the Credential Manager

```go
import "github.com/adhar/control-plane/pkg/credentials"

// Create credential manager
cm, err := credentials.NewCredentialManager(kubeConfig)
if err != nil {
    log.Fatal(err)
}

// Discover credentials for a provider
cred, err := cm.DiscoverCredentials(ctx, credentials.ProviderAzure)
if err != nil {
    log.Fatal(err)
}

// Validate credentials
if err := cm.ValidateCredentials(cred); err != nil {
    log.Fatal(err)
}

// Create Kubernetes secret
secretRef, err := cm.GetOrCreateSecret(ctx, cred, "crossplane-system", "azure-creds")
```

### Credential Discovery Priority

1. **Environment Variables**: Checked first for quick local development
2. **Kubernetes Secrets**: Common secret names in standard namespaces
3. **Credential Files**: Provider-specific credential file locations

### Setting Up Provider Credentials

**AWS:**
```sh
kubectl create secret generic aws-credentials \
  --from-literal=AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE \
  --from-literal=AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY \
  -n crossplane-system
```

**Azure:**
```sh
kubectl create secret generic azure-credentials \
  --from-literal=AZURE_CLIENT_ID=12345678-1234-1234-1234-123456789012 \
  --from-literal=AZURE_CLIENT_SECRET=your-secret \
  --from-literal=AZURE_TENANT_ID=12345678-1234-1234-1234-123456789012 \
  --from-literal=AZURE_SUBSCRIPTION_ID=12345678-1234-1234-1234-123456789012 \
  -n crossplane-system
```

**DigitalOcean:**
```sh
kubectl create secret generic digitalocean-credentials \
  --from-literal=DIGITALOCEAN_TOKEN=your-token \
  -n crossplane-system
```

**Civo:**
```sh
kubectl create secret generic civo-credentials \
  --from-literal=CIVO_TOKEN=your-token \
  -n crossplane-system
```

## Advanced Features

### Template Functions

Compositions use Go templates with Sprig functions plus custom helpers:

```yaml
{{- $loggingTypes := default (list) (get $aws "loggingTypes") -}}
{{- if $loggingTypes }}
logging:
  clusterLogging:
    {{- range $loggingTypes }}
    - types:
        - {{ quote . }}
      enabled: true
    {{- end }}
{{- end }}
```

### Status Aggregation

Compositions map provider-specific status back to composite status:

```yaml
status:
  phase: {{ quote $phase }}
  healthStatus: {{ quote $healthStatus }}
  syncStatus: {{ quote $syncStatus }}
  {{- if $structuredConditions }}
  conditions: {{ toYaml $structuredConditions | nindent 18 }}
  {{- end }}
```

### Connection Secrets

All composites can write connection details to secrets:

```yaml
spec:
  writeConnectionSecretToRef:
    name: my-secret
    namespace: default
```

Connection secrets contain provider-specific details (endpoints, credentials, etc.)

## Examples

See `examples/` directory for complete samples:
- `examples/cluster/aws-eks-sample.yaml` - Production EKS cluster with logging
- `examples/cluster/azure-aks-sample.yaml` - AKS with Azure Monitor
- `examples/apps/helm-app.yaml` - Helm chart deployment
- `examples/gitops/project.yaml` - ArgoCD project configuration

## Complete Feature Implementation

### Implementation Date
**November 8, 2025** - All control plane features completed using **KCL-based Crossplane compositions**.

### Technology Stack
- **Crossplane**: v2.1.0+ 
- **Function**: function-kcl v0.9.0+
- **Language**: KCL (Kubernetes Configuration Language)
- **Pattern**: Declarative, GitOps-driven infrastructure

### ✅ Production-Ready Features (11 Total)

#### 1. Pipeline (CI/CD)
**XRD**: `compositepipelines.platform.adhar.io`
- **Compositions**: `argo-workflows`, `tekton`
- **Features**: Multi-stage pipelines, Git/schedule/manual triggers, artifact storage, notifications, timeout management

#### 2. Policy (Compliance & Security)
**XRD**: `compositecompliancepolicies.platform.adhar.io`
- **Compositions**: `kyverno`, `opa-gatekeeper`
- **Features**: Resource limits enforcement, privileged container prevention, compliance standards (CIS, NIST, PCI-DSS, HIPAA), audit/enforce modes

#### 3. Secrets Management
**XRD**: `compositesecrets.platform.adhar.io`
- **Compositions**: `external-secrets`
- **Features**: Multi-provider secret stores (AWS, Azure, GCP, Vault), automatic rotation, encryption, access auditing

#### 4. Storage Management
**XRD**: `compositestorages.platform.adhar.io`
- **Compositions**: `persistent-volume`
- **Features**: Block/file/object/database storage, configurable storage classes, access modes, backup scheduling, encryption

#### 5. Service Management
**XRD**: `compositeservices.platform.adhar.io`
- **Compositions**: `kubernetes-service`
- **Features**: All service types (ClusterIP, NodePort, LoadBalancer, ExternalName), port configuration, monitoring integration

#### 6. Metrics Collection
**XRD**: `compositemetrics.platform.adhar.io`
- **Compositions**: `prometheus-servicemonitor`
- **Features**: Configurable scrape intervals, multi-target support, AlertManager integration, custom metrics endpoints

#### 7. Health Monitoring
**XRD**: `compositehealths.platform.adhar.io`
- **Compositions**: `healthcheck`
- **Features**: HTTP/TCP/gRPC health probes, configurable intervals/timeouts, multi-target checks, CronJob-based scheduling

#### 8. Distributed Tracing
**XRD**: `compositetraces.platform.adhar.io`
- **Compositions**: `jaeger`
- **Features**: Jaeger, Tempo, Zipkin, Datadog support, sampling rate configuration, trace retention policies

#### 9. Webhook Management
**XRD**: `compositewebhooks.platform.adhar.io`
- **Compositions**: `kubernetes-webhook`
- **Features**: Event-driven webhooks, authentication (bearer token, mTLS), retry policies, success rate tracking

#### 10. Auto-scaling
**XRD**: `compositescales.platform.adhar.io`
- **Compositions**: `hpa`
- **Features**: CPU/memory-based scaling, custom metrics, min/max replica configuration, KEDA integration ready, scaling behavior customization

#### 11. Restore Operations
**XRD**: `compositerestores.platform.adhar.io`
- **Compositions**: `velero-restore`
- **Features**: Full/selective/database/config restores, namespace-scoped restores, resource filtering, PersistentVolume restoration

### 🎉 Enterprise Features

The control-plane module now includes ALL planned enterprise features:

- ✅ **Secret Rotation Automation** - AWS Secrets Manager, Azure KeyVault, GCP Secret Manager
- ✅ **Cost Tracking & Optimization** - OpenCost with budget alerts and optimization recommendations
- ✅ **Compliance Policy Enforcement** - Kyverno/OPA with CIS, NIST, PCI-DSS, HIPAA standards
- ✅ **Service Mesh Integration** - Cilium ServiceMesh with eBPF, no sidecars
- ✅ **Observability Stack** - Prometheus, Grafana, Loki, AlertManager, Tempo
- ✅ **Disaster Recovery Automation** - Velero with cross-region replication and DR drills
- ✅ **Cross-Cloud Cluster Federation** - Unified control plane across AWS, GCP, Azure

### KCL Implementation Benefits

#### Type Safety
- Strong typing with KCL schema validation
- Compile-time error detection
- IDE autocomplete support

#### Reusability
- Modular KCL functions
- Template reuse across compositions
- DRY (Don't Repeat Yourself) principles

#### Maintainability
- Clear, readable configuration code
- Easy to test and validate
- Version control friendly

#### Flexibility
- Dynamic resource generation
- Conditional logic support
- Complex transformations

### Integration with CLI

Each XRD maps to a CLI command:

| CLI Command | XRD | Status |
|-------------|-----|--------|
| `adhar pipeline` | CompositePipeline | ✅ Production |
| `adhar policy` | CompositeCompliancePolicy | ✅ Production |
| `adhar secrets` | CompositeSecret | ✅ Production |
| `adhar storage` | CompositeStorage | ✅ Production |
| `adhar service` | CompositeService | ✅ Production |
| `adhar metrics` | CompositeMetrics | ✅ Production |
| `adhar health` | CompositeHealth | ✅ Production |
| `adhar traces` | CompositeTrace | ✅ Production |
| `adhar webhook` | CompositeWebhook | ✅ Production |
| `adhar scale` | CompositeScale | ✅ Production |
| `adhar restore` | CompositeRestore | ✅ Production |

### Build Status

```
✓ Control-plane package ready: platform/controlplane/adhar-control-plane-v0.3.8.xpkg
```

**Total**: 11 XRDs, 14 KCL Compositions - **Production Ready 🚀**

## Contributing

When adding new features:
1. Update or create XRDs in `configuration/xrd/`
2. Create compositions in `configuration/compositions/`
3. Add tests in `pkg/*/`
4. Update `features/registry.yaml`
5. Document in this README
6. Add examples in `examples/`

## License

See LICENSE file in the root of the repository.
