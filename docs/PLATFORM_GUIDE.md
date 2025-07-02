# Adhar Platform Guide

This comprehensive guide covers advanced topics for deploying, managing, and operating the Adhar platform in production environments.

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [Production Deployment](#production-deployment)
- [Multi-Provider Setup](#multi-provider-setup)
- [Management Cluster](#management-cluster)
- [Configuration Reference](#configuration-reference)
- [Operations & Maintenance](#operations--maintenance)
- [Security & Governance](#security--governance)
- [Troubleshooting](#troubleshooting)

## Architecture Overview

The Adhar platform implements a **Management Cluster First** approach where a highly available Kubernetes cluster serves as the central control plane for provisioning and managing multiple environment clusters across cloud providers.

### Core Architecture Components

```text
┌─────────────────────────────────────────────────────────────┐
│                     Management Cluster                      │
│                  (Production-Grade K8s + Cilium)           │
│                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐        │
│  │   Master    │  │   Master    │  │   Master    │        │
│  │   Node 1    │  │   Node 2    │  │   Node 3    │        │
│  └─────────────┘  └─────────────┘  └─────────────┘        │
│                                                             │
│  ┌─────────────┐  ┌─────────────┐                         │
│  │   Worker    │  │   Worker    │                         │
│  │   Node 1    │  │   Node 2    │                         │
│  │ Crossplane  │  │ ArgoCD      │                         │
│  │ Controllers │  │ Applications│                         │
│  │ Cilium CNI  │  │ Monitoring  │                         │
│  └─────────────┘  └─────────────┘                         │
└─────────────────────────────────────────────────────────────┘
                                │
        ┌───────────────────────┼───────────────────────┐
        │                       │                       │
┌───────▼────────┐    ┌────────▼────────┐    ┌────────▼────────┐
│ Environment     │    │ Environment     │    │ Environment     │
│ Cluster         │    │ Cluster         │    │ Cluster         │
│ (Dev/Test)      │    │ (Staging)       │    │ (Production)    │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

### Platform Components

- **Management Cluster**: Central control plane with Cilium CNI
- **Environment Clusters**: Application workload clusters provisioned via Crossplane
- **GitOps Engine**: ArgoCD for declarative deployments
- **Observability Stack**: Prometheus, Grafana, Loki, Tempo, Jaeger
- **Security Layer**: Keycloak, Vault, Kyverno, Falco, Trivy
- **Infrastructure**: Crossplane for cloud resources, Velero for backups

## Production Deployment

### Supported Cloud Providers

- **Google Cloud Platform (GCP)**: GKE clusters with private nodes
- **Amazon Web Services (AWS)**: EKS clusters with proper IAM
- **Microsoft Azure**: AKS clusters with enhanced security
- **DigitalOcean**: Managed Kubernetes with Cilium
- **Civo**: Cloud-native Kubernetes
- **On-Premises**: Bootstrap script integration

### Production-Ready Features

- **High Availability**: Multi-master setup with auto-scaling
- **Security Hardening**: RBAC, network policies, audit logging
- **Cilium CNI**: Advanced networking with Hubble and encryption
- **Monitoring**: Full observability stack with alerting
- **Backup & Recovery**: Automated backup strategies
- **GitOps**: Declarative infrastructure and application management

### Cloud Provider Setup

#### Google Cloud Platform

```bash
# Prerequisites
gcloud auth application-default login
export PROJECT_ID="your-project-id"
gcloud config set project $PROJECT_ID

# Enable required APIs
gcloud services enable container.googleapis.com
gcloud services enable compute.googleapis.com
gcloud services enable cloudresourcemanager.googleapis.com

# Configuration
cat > gcp-config.yaml << EOF
globalSettings:
  provider: "gke"
  region: "us-east1-a"
  projectId: "$PROJECT_ID"

cluster:
  name: "adhar-management"
  version: "1.30"
  nodeCount: 3
  machineType: "e2-standard-4"
  diskSize: "100GB"

security:
  privateCluster: true
  authorizedNetworks:
    - cidr: "10.0.0.0/8"
      displayName: "internal"

networking:
  cni: "cilium"
  enableNetworkPolicy: true
  
monitoring:
  enabled: true
  retention: "30d"
EOF

# Deploy
adhar up -f gcp-config.yaml
```

#### Amazon Web Services

```bash
# Prerequisites
aws configure
export AWS_REGION="us-east-1"
export CLUSTER_NAME="adhar-management"

# Configuration
cat > aws-config.yaml << EOF
globalSettings:
  provider: "aws"
  region: "$AWS_REGION"

cluster:
  name: "$CLUSTER_NAME"
  version: "1.30"
  nodeGroups:
    - name: "general"
      instanceType: "m5.large"
      minSize: 3
      maxSize: 10
      desiredSize: 3

security:
  enablePrivateEndpoint: true
  enablePublicEndpoint: true
  publicAccessCidrs:
    - "0.0.0.0/0"  # Restrict in production

networking:
  cni: "cilium"
  
addons:
  - name: "vpc-cni"
  - name: "coredns"
  - name: "kube-proxy"
EOF

# Deploy
adhar up -f aws-config.yaml
```

#### Azure

```bash
# Prerequisites
az login
export RESOURCE_GROUP="adhar-rg"
export LOCATION="eastus"

# Configuration
cat > azure-config.yaml << EOF
globalSettings:
  provider: "azure"
  region: "$LOCATION"
  resourceGroup: "$RESOURCE_GROUP"

cluster:
  name: "adhar-management"
  version: "1.30"
  nodePool:
    vmSize: "Standard_D2s_v3"
    count: 3
    minCount: 3
    maxCount: 10

security:
  enablePrivateCluster: true
  authorizedIPRanges:
    - "10.0.0.0/8"

networking:
  cni: "cilium"
  networkPolicy: "cilium"
EOF

# Deploy
adhar up -f azure-config.yaml
```

## Multi-Provider Setup

The dual-provider architecture allows you to configure separate cloud providers for production and non-production environments.

### Configuration Example

```yaml
globalSettings:
  # Dual-Provider Configuration
  productionProvider: "gke"        # Main production provider
  productionRegion: "us-east1-a"   # Default region for production
  
  nonProductionProvider: "do"      # Cost-effective provider for dev/test
  nonProductionRegion: "nyc3"      # Default region for non-production

cluster:
  name: "adhar-management"
  type: "management"

environments:
  # Development - uses nonProductionProvider (DigitalOcean)
  dev:
    template: development-defaults
    resources:
      cpu: "2"
      memory: "4Gi"
      storage: "20Gi"
    
  # Testing - uses nonProductionProvider (DigitalOcean)
  test:
    template: development-defaults
    resources:
      cpu: "4"
      memory: "8Gi"
      storage: "50Gi"
    
  # Staging - uses productionProvider (GKE)
  staging:
    type: production
    template: staging-defaults
    resources:
      cpu: "8"
      memory: "16Gi"
      storage: "100Gi"
    
  # Production - uses productionProvider (GKE)
  prod:
    type: production
    template: production-defaults
    resources:
      cpu: "16"
      memory: "32Gi"
      storage: "200Gi"
    highAvailability: true
    backup:
      enabled: true
      schedule: "0 2 * * *"
```

### Benefits

- **Cost Optimization**: Use cheaper providers for non-critical environments
- **Risk Separation**: Isolate production workloads
- **Flexibility**: Different providers for different use cases
- **Compliance**: Meet regulatory requirements with provider separation

## Management Cluster

The management cluster is the central control plane that provisions and manages all environment clusters.

### Key Features

- **Cilium CNI**: Advanced networking with eBPF
- **Crossplane**: Infrastructure as Code for cloud resources
- **ArgoCD**: GitOps for application deployments
- **High Availability**: Multi-master setup with etcd clustering
- **Security**: RBAC, network policies, audit logging
- **Monitoring**: Full observability stack

### Bootstrap Process

```bash
# Direct management cluster deployment
adhar cluster create --provider gke --region us-east1-a

# Check cluster status
adhar cluster status

# Access cluster
adhar cluster kubeconfig

# Day-2 operations
adhar cluster upgrade --version 1.31
adhar cluster scale --nodes 5
adhar cluster backup
```

### Management Operations

```bash
# Environment provisioning
adhar cluster provision-env --name dev --provider do --region nyc3

# Environment management
adhar get environments
adhar cluster delete-env --name old-test

# Monitoring
adhar cluster health
adhar cluster logs --component crossplane
```

## Configuration Reference

### Global Settings

```yaml
globalSettings:
  # Single provider setup
  provider: "gke"                    # Primary cloud provider
  region: "us-east1-a"              # Default region
  
  # Dual provider setup
  productionProvider: "gke"         # Production workloads
  productionRegion: "us-east1-a"
  nonProductionProvider: "do"       # Non-production workloads
  nonProductionRegion: "nyc3"
  
  # Authentication
  auth:
    method: "oidc"                  # oidc, basic, token
    oidcIssuer: "https://auth.company.com"
    
  # Networking
  networking:
    cni: "cilium"                   # cilium, calico, flannel
    serviceCidr: "10.96.0.0/12"
    podCidr: "10.244.0.0/16"
    
  # Security
  security:
    enablePodSecurityPolicy: true
    enableNetworkPolicy: true
    enableAuditLogging: true
    
  # Monitoring
  monitoring:
    enabled: true
    retention: "30d"
    alerting: true
    
  # Backup
  backup:
    enabled: true
    provider: "velero"
    schedule: "0 2 * * *"
    retention: "30d"
```

### Cluster Configuration

```yaml
cluster:
  name: "adhar-production"
  version: "1.30"
  type: "management"                # management, environment
  
  # Node configuration
  nodes:
    minSize: 3
    maxSize: 10
    desiredSize: 3
    machineType: "e2-standard-4"
    diskSize: "100GB"
    diskType: "pd-ssd"
    
  # High availability
  highAvailability:
    enabled: true
    masters: 3
    etcdBackup: true
    
  # Security
  security:
    privateCluster: true
    authorizedNetworks:
      - cidr: "10.0.0.0/8"
        displayName: "internal"
    enableShieldedNodes: true
    enableWorkloadIdentity: true
    
  # Add-ons
  addons:
    cilium:
      enabled: true
      hubble: true
      encryption: true
    crossplane:
      enabled: true
      providers: ["gcp", "aws", "azure"]
    argocd:
      enabled: true
      ha: true
    monitoring:
      enabled: true
      stack: "prometheus"
```

### Environment Templates

```yaml
templates:
  development-defaults:
    resources:
      cpu: "2"
      memory: "4Gi"
      storage: "20Gi"
    scaling:
      minReplicas: 1
      maxReplicas: 3
    monitoring:
      enabled: true
      level: "basic"
    backup:
      enabled: false
      
  staging-defaults:
    resources:
      cpu: "4"
      memory: "8Gi"
      storage: "50Gi"
    scaling:
      minReplicas: 2
      maxReplicas: 5
    monitoring:
      enabled: true
      level: "detailed"
    backup:
      enabled: true
      schedule: "0 6 * * *"
      
  production-defaults:
    resources:
      cpu: "8"
      memory: "16Gi"
      storage: "100Gi"
    scaling:
      minReplicas: 3
      maxReplicas: 10
    highAvailability: true
    monitoring:
      enabled: true
      level: "comprehensive"
      alerting: true
    backup:
      enabled: true
      schedule: "0 2 * * *"
    security:
      networkPolicies: true
      podSecurityPolicy: true
      imageScanningPolicy: true
```

## Operations & Maintenance

### Day-2 Operations

```bash
# Cluster maintenance
adhar cluster upgrade --version 1.31
adhar cluster scale --nodes 5
adhar cluster drain-node --node worker-1

# Backup operations
adhar backup create --name manual-backup-$(date +%Y%m%d)
adhar backup list
adhar backup restore --name backup-20240315

# Monitoring
adhar cluster health
adhar cluster metrics
adhar cluster logs --component cilium --follow

# Security
adhar security scan
adhar security policies validate
adhar security audit --output json
```

### Automated Operations

```yaml
# GitOps configuration for platform operations
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: platform-ops
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://gitea.adhar.local/platform/ops
    targetRevision: HEAD
    path: manifests
  destination:
    server: https://kubernetes.default.svc
    namespace: adhar-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
```

### Monitoring & Alerting

```yaml
# Custom monitoring configuration
monitoring:
  prometheus:
    retention: "30d"
    storageClass: "ssd"
    resources:
      requests:
        memory: "2Gi"
        cpu: "1"
      limits:
        memory: "4Gi"
        cpu: "2"
        
  grafana:
    persistence: true
    adminPassword: "secure-password"
    dashboards:
      - kubernetes-cluster
      - cilium-overview
      - argocd-metrics
      
  alertmanager:
    config:
      global:
        slack_api_url: "https://hooks.slack.com/services/..."
      route:
        group_by: ['alertname']
        group_wait: 10s
        group_interval: 10s
        repeat_interval: 1h
        receiver: 'slack-notifications'
      receivers:
      - name: 'slack-notifications'
        slack_configs:
        - channel: '#alerts'
          title: 'Adhar Platform Alert'
```

## Security & Governance

### Security Configuration

```yaml
security:
  # Authentication
  auth:
    oidc:
      issuerUrl: "https://keycloak.adhar.local/auth/realms/adhar"
      clientId: "adhar-platform"
      usernameClaim: "email"
      groupsClaim: "groups"
      
  # Authorization
  rbac:
    enabled: true
    rules:
      - subjects:
        - kind: Group
          name: "platform-admins"
        roleRef:
          kind: ClusterRole
          name: "cluster-admin"
      - subjects:
        - kind: Group
          name: "developers"
        roleRef:
          kind: ClusterRole
          name: "edit"
          
  # Network security
  networkPolicies:
    enabled: true
    defaultDeny: true
    policies:
      - name: "allow-dns"
        podSelector: {}
        policyTypes: ["Egress"]
        egress:
        - to: []
          ports:
          - protocol: UDP
            port: 53
            
  # Pod security
  podSecurityPolicy:
    enabled: true
    policies:
      - name: "restricted"
        runAsNonRoot: true
        allowPrivilegeEscalation: false
        requiredDropCapabilities: ["ALL"]
        
  # Image security
  imagePolicy:
    enabled: true
    scanningRequired: true
    allowedRegistries:
      - "harbor.adhar.local"
      - "gcr.io/adhar-project"
```

### Compliance & Governance

```yaml
governance:
  # Policy enforcement
  policies:
    - name: "resource-quotas"
      enforce: true
      rules:
        - namespaces must have resource quotas
        - containers must have resource limits
        
    - name: "security-standards"
      enforce: true
      rules:
        - no privileged containers
        - no host network access
        - required security contexts
        
  # Audit logging
  audit:
    enabled: true
    policy: "comprehensive"
    retention: "90d"
    
  # Compliance reporting
  compliance:
    frameworks: ["CIS", "PCI-DSS", "SOC2"]
    reporting:
      enabled: true
      schedule: "0 0 1 * *"  # Monthly
      recipients: ["compliance@company.com"]
```

## Troubleshooting

### Common Issues

#### Cluster Provisioning Failures

```bash
# Check provider credentials
adhar cluster validate-credentials --provider gke

# Debug provisioning
adhar cluster create --provider gke --debug --dry-run

# Check resource quotas
gcloud compute project-info describe --project=$PROJECT_ID
```

#### Networking Issues

```bash
# Check Cilium status
kubectl -n kube-system exec -it cilium-agent-xxxxx -- cilium status

# Test connectivity
kubectl -n kube-system exec -it cilium-agent-xxxxx -- cilium connectivity test

# Check network policies
kubectl get networkpolicies -A
kubectl describe networkpolicy <policy-name> -n <namespace>
```

#### Application Deployment Issues

```bash
# Check ArgoCD application status
kubectl get applications -n argocd
kubectl describe application <app-name> -n argocd

# Check GitOps sync status
adhar apps status
adhar apps sync <app-name> --force

# Debug failed deployments
kubectl get events --sort-by=.metadata.creationTimestamp
kubectl logs -n <namespace> <pod-name> --previous
```

#### Performance Issues

```bash
# Check resource utilization
kubectl top nodes
kubectl top pods -A

# Check cluster health
adhar cluster health --verbose

# Monitor system components
kubectl get componentstatuses
kubectl -n kube-system get pods
```

### Log Analysis

```bash
# Centralized logging
kubectl logs -n adhar-system -l app=loki --tail=100 -f

# Search logs
curl "http://loki.adhar.local/loki/api/v1/query_range" \
  --data-urlencode 'query={namespace="default"}' \
  --data-urlencode 'start=2024-01-01T00:00:00Z' \
  --data-urlencode 'end=2024-01-31T23:59:59Z'

# Application-specific logs
kubectl logs -n production -l app=my-app --tail=1000 | grep ERROR
```

### Performance Tuning

```bash
# Node optimization
kubectl patch node <node-name> -p '{"spec":{"taints":[{"key":"performance","value":"high","effect":"NoSchedule"}]}}'

# Resource optimization
kubectl patch deployment <deployment-name> -p '{"spec":{"template":{"spec":{"containers":[{"name":"<container-name>","resources":{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"cpu":"500m","memory":"512Mi"}}}]}}}}'

# Scaling optimization
kubectl patch hpa <hpa-name> -p '{"spec":{"minReplicas":3,"maxReplicas":10,"targetCPUUtilizationPercentage":70}}'
```

### Emergency Procedures

```bash
# Emergency cluster access
adhar cluster emergency-access --cluster production

# Rollback deployment
kubectl rollout undo deployment/<deployment-name> -n <namespace>

# Scale down problematic service
kubectl scale deployment <deployment-name> --replicas=0 -n <namespace>

# Emergency backup
adhar backup create --name emergency-$(date +%Y%m%d-%H%M%S) --priority high

# Disaster recovery
adhar cluster restore --backup emergency-20240315-143000 --confirm
```

For additional support and detailed troubleshooting guides, visit our [community documentation](https://github.com/adhar-io/adhar/wiki) or join our [Slack channel](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww).
