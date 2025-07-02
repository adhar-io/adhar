# Configuration Guide

This guide covers configuration options for the Adhar platform across different deployment scenarios.

## Quick Reference

### Basic Local Development

```yaml
# adhar-config.yaml
globalSettings:
  provider: "kind"
  
cluster:
  name: "adhar-local"
```

### Single Cloud Provider

```yaml
# adhar-config.yaml
globalSettings:
  provider: "gke"
  region: "us-east1-a"
  
cluster:
  name: "adhar-production"
  version: "1.30"
  nodeCount: 3
  
environments:
  dev:
    template: development-defaults
  prod:
    template: production-defaults
```

### Multi-Cloud Setup

```yaml
# adhar-config.yaml
globalSettings:
  productionProvider: "gke"
  productionRegion: "us-east1-a"
  nonProductionProvider: "do"
  nonProductionRegion: "nyc3"
  
cluster:
  name: "adhar-management"
  type: "management"
  
environments:
  dev:
    template: development-defaults
  staging:
    type: production
    template: staging-defaults
  prod:
    type: production
    template: production-defaults
```

## Global Settings

### Provider Configuration

```yaml
globalSettings:
  # Single provider
  provider: "gke"                    # gke, aws, azure, do, civo, onprem
  region: "us-east1-a"
  
  # Dual provider
  productionProvider: "gke"
  productionRegion: "us-east1-a"
  nonProductionProvider: "do"
  nonProductionRegion: "nyc3"
  
  # Authentication
  auth:
    method: "oidc"
    oidcIssuer: "https://auth.company.com"
    
  # Networking
  networking:
    cni: "cilium"
    serviceCidr: "10.96.0.0/12"
    podCidr: "10.244.0.0/16"
```

## Cluster Configuration

### Management Cluster

```yaml
cluster:
  name: "adhar-management"
  type: "management"
  version: "1.30"
  
  # Node configuration
  nodes:
    minSize: 3
    maxSize: 10
    desiredSize: 3
    machineType: "e2-standard-4"
    diskSize: "100GB"
    
  # High availability
  highAvailability:
    enabled: true
    masters: 3
    
  # Security
  security:
    privateCluster: true
    authorizedNetworks:
      - cidr: "10.0.0.0/8"
        displayName: "internal"
```

### Environment Cluster

```yaml
cluster:
  name: "adhar-production"
  type: "environment"
  version: "1.30"
  
  nodes:
    minSize: 2
    maxSize: 20
    desiredSize: 5
    machineType: "e2-standard-2"
    
  monitoring:
    enabled: true
    retention: "30d"
    
  backup:
    enabled: true
    schedule: "0 2 * * *"
```

## Environment Templates

### Development Template

```yaml
templates:
  development-defaults:
    resources:
      cpu: "1"
      memory: "2Gi"
      storage: "10Gi"
    scaling:
      minReplicas: 1
      maxReplicas: 3
    monitoring:
      enabled: true
      level: "basic"
    backup:
      enabled: false
```

### Staging Template

```yaml
templates:
  staging-defaults:
    resources:
      cpu: "2"
      memory: "4Gi"
      storage: "20Gi"
    scaling:
      minReplicas: 2
      maxReplicas: 5
    monitoring:
      enabled: true
      level: "detailed"
    backup:
      enabled: true
      schedule: "0 6 * * *"
```

### Production Template

```yaml
templates:
  production-defaults:
    resources:
      cpu: "4"
      memory: "8Gi"
      storage: "50Gi"
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
```

## Environment Definitions

```yaml
environments:
  # Development environment
  dev:
    template: development-defaults
    provider: "do"  # Override global provider
    region: "nyc3"
    
  # Testing environment
  test:
    template: development-defaults
    resources:
      cpu: "2"
      memory: "4Gi"
    
  # Staging environment
  staging:
    type: production
    template: staging-defaults
    
  # Production environment
  prod:
    type: production
    template: production-defaults
    highAvailability: true
```

## Cloud Provider Specific Configuration

### Google Cloud Platform (GKE)

```yaml
globalSettings:
  provider: "gke"
  region: "us-east1-a"
  projectId: "my-project-id"
  
cluster:
  name: "adhar-gke"
  version: "1.30"
  
  # GKE specific settings
  gke:
    releaseChannel: "REGULAR"
    networkPolicy: true
    privateCluster: true
    masterIpv4CidrBlock: "172.16.0.0/28"
    
  nodes:
    machineType: "e2-standard-4"
    diskType: "pd-ssd"
    diskSize: "100GB"
    preemptible: false
    
  # Security
  security:
    enableShieldedNodes: true
    enableWorkloadIdentity: true
```

### Amazon Web Services (EKS)

```yaml
globalSettings:
  provider: "aws"
  region: "us-east-1"
  
cluster:
  name: "adhar-eks"
  version: "1.30"
  
  # EKS specific settings
  eks:
    endpointConfig:
      privateAccess: true
      publicAccess: true
      publicAccessCidrs: ["0.0.0.0/0"]
      
  nodeGroups:
    - name: "general"
      instanceType: "m5.large"
      minSize: 3
      maxSize: 10
      desiredSize: 3
      diskSize: "100"
      
  # VPC configuration
  vpc:
    cidr: "10.0.0.0/16"
    privateSubnets: ["10.0.1.0/24", "10.0.2.0/24", "10.0.3.0/24"]
    publicSubnets: ["10.0.4.0/24", "10.0.5.0/24", "10.0.6.0/24"]
```

### Microsoft Azure (AKS)

```yaml
globalSettings:
  provider: "azure"
  region: "eastus"
  resourceGroup: "adhar-rg"
  
cluster:
  name: "adhar-aks"
  version: "1.30"
  
  # AKS specific settings
  aks:
    dnsPrefix: "adhar"
    enablePrivateCluster: true
    
  nodePool:
    vmSize: "Standard_D2s_v3"
    count: 3
    minCount: 3
    maxCount: 10
    osDiskSize: "100"
    
  # Network configuration
  networking:
    networkPlugin: "cilium"
    serviceCidr: "10.96.0.0/12"
    dnsServiceIp: "10.96.0.10"
```

### DigitalOcean

```yaml
globalSettings:
  provider: "do"
  region: "nyc3"
  
cluster:
  name: "adhar-do"
  version: "1.30"
  
  # DigitalOcean specific settings
  do:
    tags: ["adhar", "kubernetes"]
    
  nodePool:
    size: "s-2vcpu-4gb"
    count: 3
    minNodes: 3
    maxNodes: 10
    autoScale: true
```

### On-Premises

```yaml
globalSettings:
  provider: "onprem"
  
cluster:
  name: "adhar-onprem"
  type: "management"
  
  # On-premises specific settings
  onprem:
    controlPlaneEndpoint: "10.0.1.100:6443"
    podSubnet: "10.244.0.0/16"
    serviceSubnet: "10.96.0.0/12"
    
  nodes:
    - name: "master-1"
      role: "master"
      ip: "10.0.1.101"
    - name: "master-2"
      role: "master"
      ip: "10.0.1.102"
    - name: "master-3"
      role: "master"
      ip: "10.0.1.103"
    - name: "worker-1"
      role: "worker"
      ip: "10.0.1.111"
    - name: "worker-2"
      role: "worker"
      ip: "10.0.1.112"
```

## Advanced Configuration

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
      
  # Network security
  networkPolicies:
    enabled: true
    defaultDeny: true
    
  # Pod security
  podSecurityPolicy:
    enabled: true
    runAsNonRoot: true
    
  # Image security
  imagePolicy:
    scanningRequired: true
    allowedRegistries:
      - "harbor.adhar.local"
      - "gcr.io/adhar-project"
```

### Monitoring Configuration

```yaml
monitoring:
  prometheus:
    retention: "30d"
    storageClass: "ssd"
    resources:
      requests:
        memory: "2Gi"
        cpu: "1"
        
  grafana:
    persistence: true
    adminPassword: "secure-password"
    
  alertmanager:
    config:
      global:
        slack_api_url: "https://hooks.slack.com/services/..."
      receivers:
      - name: 'slack-notifications'
        slack_configs:
        - channel: '#alerts'
```

### Backup Configuration

```yaml
backup:
  velero:
    enabled: true
    provider: "gcp"  # gcp, aws, azure, minio
    bucket: "adhar-backups"
    schedule: "0 2 * * *"
    retention: "30d"
    
    # Provider-specific configuration
    gcp:
      serviceAccount: "velero@project-id.iam.gserviceaccount.com"
      
    aws:
      region: "us-east-1"
      
    azure:
      subscriptionId: "subscription-id"
      resourceGroup: "backup-rg"
```

## Configuration Validation

### CLI Validation

```bash
# Validate configuration file
adhar config validate -f adhar-config.yaml

# Check provider credentials
adhar config check-credentials --provider gke

# Preview deployment
adhar up -f adhar-config.yaml --dry-run
```

### Schema Validation

The configuration file is validated against a JSON schema. You can find the schema at:
`adhar-config.schema.json`

### Best Practices

1. **Version Control**: Store configuration files in Git
2. **Environment Separation**: Use separate config files per environment
3. **Secret Management**: Use external secret management for sensitive data
4. **Resource Limits**: Always set resource limits for production workloads
5. **Backup Strategy**: Enable backups for all production environments
6. **Monitoring**: Configure comprehensive monitoring for all environments

## Troubleshooting Configuration

### Common Issues

**Invalid provider configuration:**
```bash
# Check provider-specific requirements
adhar config validate --provider gke --verbose
```

**Resource quota exceeded:**
```bash
# Check cloud provider quotas
gcloud compute project-info describe --project=$PROJECT_ID
```

**Network configuration conflicts:**
```bash
# Validate network CIDR ranges
adhar config validate --check-networks
```

For more detailed configuration examples, see the `docs/samples/` directory.
