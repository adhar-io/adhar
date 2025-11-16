# Adhar Platform Architecture

This document provides a comprehensive overview of the Adhar platform architecture, design principles, and technical implementation.

## Executive Summary

Adhar is a cloud-native Internal Developer Platform (IDP) that implements a **Management Cluster First** architecture. It provides a unified control plane for provisioning, managing, and operating Kubernetes environments across multiple cloud providers while maintaining strong security, governance, and observability standards.

## Architecture Principles

### 1. Management Cluster First
- Central control plane using production-grade Kubernetes with Cilium CNI
- Single source of truth for platform state and configuration
- Unified API surface for all platform operations

### 2. GitOps-Driven Operations
- Declarative infrastructure and application management
- Version-controlled configuration with audit trails
- Automated reconciliation and drift detection

### 3. Multi-Cloud by Design
- Provider-agnostic abstractions using Crossplane
- Unified experience across AWS, GCP, Azure, DigitalOcean, Civo, and on-premises
- Cost optimization through dual-provider strategies

### 4. Security by Default
- Zero-trust networking with Cilium and network policies
- Comprehensive security scanning and policy enforcement
- Identity and access management with Keycloak and RBAC

### 5. Platform as a Product
- Self-service capabilities for development teams
- Golden path templates and standardized workflows
- Comprehensive observability and developer experience tools

## High-Level Architecture

```text
┌─────────────────────────────────────────────────────────────────────────┐
│                          Adhar Platform                                 │
│                                                                         │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐       │
│  │   Developer     │  │   Platform      │  │   Operations    │       │
│  │   Experience    │  │   Services      │  │   & Security    │       │
│  │                 │  │                 │  │                 │       │
│  │ • Adhar Console │  │ • ArgoCD        │  │ • Prometheus    │       │
│  │ • CLI Tools     │  │ • Gitea         │  │ • Grafana       │       │
│  │ • IDE Plugins   │  │ • Harbor        │  │ • Keycloak      │       │
│  │ • AI Assistant  │  │ • Kaniko        │  │ • Vault         │       │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘       │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────────┤
│  │                    Management Cluster                              │
│  │                 (Cilium + Crossplane + ArgoCD)                     │
│  │                                                                     │
│  │  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌───────────┐      │
│  │  │  Master   │  │  Master   │  │  Master   │  │  Worker   │      │
│  │  │  Node 1   │  │  Node 2   │  │  Node 3   │  │  Nodes    │      │
│  │  └───────────┘  └───────────┘  └───────────┘  └───────────┘      │
│  └─────────────────────────────────────────────────────────────────────┤
│                                     │                                   │
│         ┌───────────────────────────┼───────────────────────────┐       │
│         │                           │                           │       │
│  ┌──────▼──────┐            ┌──────▼──────┐            ┌──────▼──────┐ │
│  │Environment  │            │Environment  │            │Environment  │ │
│  │Cluster      │            │Cluster      │            │Cluster      │ │
│  │(Development)│            │(Staging)    │            │(Production) │ │
│  │             │            │             │            │             │ │
│  │• App Workld │            │• App Workld │            │• App Workld │ │
│  │• Monitoring │            │• Monitoring │            │• Monitoring │ │
│  │• Security   │            │• Security   │            │• Security   │ │
│  └─────────────┘            └─────────────┘            └─────────────┘ │
└─────────────────────────────────────────────────────────────────────────┘
```

## Core Components

### Management Cluster

The management cluster serves as the central control plane with the following components:

#### Control Plane
- **Kubernetes Masters**: Highly available API servers, controllers, and schedulers
- **etcd Cluster**: Distributed key-value store for cluster state
- **Cilium CNI**: eBPF-based networking with advanced security features
- **Load Balancer**: HAProxy or cloud-native load balancer for API access

#### Platform Services
- **Crossplane**: Infrastructure as Code for cloud resource provisioning
- **ArgoCD**: GitOps continuous deployment engine
- **Gitea**: Git repository management and source control
- **Keycloak**: Identity and access management
- **Harbor**: Container registry with security scanning
- **Vault**: Secrets management and encryption

#### Observability Stack
- **Prometheus**: Metrics collection and alerting
- **Grafana**: Visualization and dashboards
- **Loki**: Log aggregation and analysis
- **Tempo**: Distributed tracing
- **Jaeger**: Application performance monitoring
- **Alertmanager**: Alert routing and notification

### Environment Clusters

Environment clusters host application workloads and are provisioned by the management cluster:

#### Application Runtime
- **Kubernetes**: Container orchestration platform
- **Cilium**: Networking and security enforcement
- **Istio/Linkerd**: Service mesh (optional)
- **NGINX Ingress**: Traffic routing and SSL termination

#### Platform Integration
- **ArgoCD Agent**: Application deployment synchronization
- **Monitoring Agents**: Prometheus Node Exporter, cAdvisor
- **Security Agents**: Falco runtime security, Trivy scanning
- **Backup Agents**: Velero for disaster recovery

## Technical Implementation

### Networking Architecture

```text
Management Cluster Network (10.0.0.0/16)
├── Control Plane Subnet (10.0.1.0/24)
│   ├── API Server Load Balancer
│   ├── Master Nodes (10.0.1.10-12)
│   └── etcd Cluster (10.0.1.20-22)
├── Worker Subnet (10.0.2.0/24)
│   ├── Platform Services (10.0.2.0/25)
│   └── System Components (10.0.2.128/25)
└── Services Subnet (10.0.3.0/24)
    ├── ClusterIP Services
    └── LoadBalancer Services

Environment Clusters
├── Development (10.1.0.0/16)
├── Staging (10.2.0.0/16)
└── Production (10.3.0.0/16)
```

### Security Architecture

#### Zero-Trust Networking
- **Network Policies**: Cilium-based microsegmentation
- **Service Mesh**: Mutual TLS for service-to-service communication
- **Ingress Security**: WAF and DDoS protection at ingress layer
- **Pod Security**: Security contexts and policies enforcement

#### Identity and Access Management
- **Authentication**: OIDC integration with Keycloak
- **Authorization**: Kubernetes RBAC with group-based access
- **Service Accounts**: Workload identity for cloud resource access
- **API Security**: JWT tokens and rate limiting

#### Secrets Management
- **Vault Integration**: Centralized secrets storage and rotation
- **Encryption**: Encryption at rest and in transit
- **Secret Injection**: CSI driver for secure secret mounting
- **Audit Logging**: Comprehensive access and change tracking

### Data Flow Architecture

```text
Developer Workflow:
┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│   Developer │───▶│ Adhar CLI/  │───▶│   Gitea     │───▶│   ArgoCD    │
│   Laptop    │    │   Console   │    │ Repository  │    │   Sync      │
└─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘
                                             │                    │
Infrastructure Provisioning:                │                    ▼
┌─────────────┐    ┌─────────────┐    ┌─────▼─────┐    ┌─────────────┐
│   Cloud     │◀───│ Crossplane  │◀───│ Management │───▶│Environment  │
│ Resources   │    │ Providers   │    │  Cluster   │    │  Clusters   │
└─────────────┘    └─────────────┘    └───────────┘    └─────────────┘

Observability Data Flow:
┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│Application  │───▶│  Prometheus │───▶│   Grafana   │───▶│  Dashboard  │
│  Metrics    │    │  Collection │    │Visualization│    │   Views     │
└─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘

┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│Application  │───▶│    Loki     │───▶│   Grafana   │
│    Logs     │    │Aggregation  │    │   Explore   │
└─────────────┘    └─────────────┘    └─────────────┘

┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│Application  │───▶│   Tempo/    │───▶│   Jaeger    │
│   Traces    │    │   Jaeger    │    │     UI      │
└─────────────┘    └─────────────┘    └─────────────┘
```

## Deployment Models

### Single Cloud Deployment

Suitable for organizations with a single cloud provider preference:

```yaml
# Single provider configuration
globalSettings:
  provider: "gke"
  region: "us-east1-a"
  
cluster:
  name: "adhar-platform"
  nodeCount: 5
  machineType: "e2-standard-4"
```

### Multi-Cloud Deployment

Optimal for cost optimization and risk distribution:

```yaml
# Dual provider configuration
globalSettings:
  productionProvider: "gke"        # High-performance production
  productionRegion: "us-east1-a"
  nonProductionProvider: "do"      # Cost-effective development
  nonProductionRegion: "nyc3"
```

### Hybrid Cloud Deployment

Combines public cloud and on-premises infrastructure:

```yaml
# Hybrid configuration
globalSettings:
  managementProvider: "onprem"     # On-premises management cluster
  environmentProviders:
    - provider: "gke"
      environments: ["production"]
    - provider: "aws"
      environments: ["staging"]
    - provider: "onprem"
      environments: ["development"]
```

## Scalability Considerations

### Horizontal Scaling
- **Management Cluster**: Auto-scaling worker nodes (3-50 nodes)
- **Environment Clusters**: Independent scaling per environment
- **Database Scaling**: PostgreSQL clustering for stateful services
- **Cache Scaling**: Redis clustering for session and application data

### Performance Optimization
- **Resource Allocation**: CPU and memory limits based on workload profiles
- **Storage Performance**: SSD storage classes for high-IOPS workloads
- **Network Optimization**: Cilium eBPF for high-performance networking
- **Monitoring Overhead**: Optimized metrics collection and retention

### Cost Optimization
- **Spot Instances**: Use spot instances for non-critical workloads
- **Resource Right-sizing**: Automated recommendations for resource optimization
- **Multi-Cloud Strategy**: Cost arbitrage across cloud providers
- **Scheduled Scaling**: Scale down non-production environments during off-hours

## Disaster Recovery

### Backup Strategy
- **etcd Backups**: Automated daily snapshots of cluster state
- **Application Data**: Velero for persistent volume backups
- **Configuration Backups**: Git-based infrastructure as code
- **Cross-Region Replication**: Backup data replicated across regions

### Recovery Procedures
- **RTO Target**: 4 hours for complete platform recovery
- **RPO Target**: 1 hour maximum data loss
- **Automated Recovery**: Self-healing for common failure scenarios
- **Manual Recovery**: Detailed runbooks for disaster scenarios

## Integration Patterns

### CI/CD Integration
- **Git Webhooks**: Automated triggers from code repositories
- **Container Builds**: Kaniko for secure container image building
- **Deployment Pipelines**: Argo Workflows for complex deployment scenarios
- **Quality Gates**: Automated testing and security scanning

### External System Integration
- **LDAP/AD Integration**: Enterprise directory service authentication
- **ITSM Integration**: ServiceNow, Jira for change management
- **Monitoring Integration**: PagerDuty, Slack for alerting
- **Compliance Integration**: Export audit logs to SIEM systems

## Future Roadmap

### Planned Enhancements
- **AI/ML Platform**: Kubeflow integration for machine learning workloads
- **Edge Computing**: K3s support for edge deployments
- **Serverless Computing**: Knative for serverless application patterns
- **Policy as Code**: Enhanced OPA/Gatekeeper integration

### Technology Evolution
- **Kubernetes Versions**: Support for latest Kubernetes releases
- **Cloud Provider Features**: Integration with new cloud services
- **Security Enhancements**: Zero-trust networking improvements
- **Developer Experience**: Enhanced IDE integrations and tooling

For detailed implementation guides and operational procedures, refer to the [User Guide](USER_GUIDE.md) and [Getting Started Guide](GETTING_STARTED.md).
