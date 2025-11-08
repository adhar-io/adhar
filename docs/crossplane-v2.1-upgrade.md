# Crossplane v2.1 Upgrade & Multi-Cloud Provider Expansion

**Date**: November 8, 2025  
**Version**: Crossplane v2.1.0  
**Status**: ✅ Complete

## Overview

The Adhar control plane has been upgraded to **Crossplane v2.1** with comprehensive multi-cloud provider support across AWS, Azure, GCP, DigitalOcean, Civo, Kind, and on-premises Kubernetes clusters.

## What's New

### 🎯 Crossplane 2.1 Features

- **Enhanced Pipeline Mode**: Improved composition pipeline with better status aggregation
- **Function Ecosystem**: Added `function-patch-and-transform` alongside `function-go-templating`
- **Better Lifecycle Management**: Improved resource dependency handling and readiness detection
- **Performance Improvements**: Faster reconciliation and reduced memory footprint
- **Enhanced Observability**: Better metrics and logging for troubleshooting

### 🌐 Provider Expansion

#### AWS - Modular Providers (v1.7.0+)
- ✅ **provider-aws-eks**: EKS cluster management
- ✅ **provider-aws-ec2**: VPC, subnets, security groups, EC2 instances
- ✅ **provider-aws-rds**: RDS databases (PostgreSQL, MySQL, Aurora)
- ✅ **provider-aws-iam**: IAM roles, policies, IRSA
- ✅ **provider-aws-s3**: S3 buckets and policies

**Benefits**:
- Granular version control per service
- Reduced blast radius for updates
- Better resource isolation
- Smaller controller footprints

#### Azure - Modular Providers (v1.3.0+)
- ✅ **provider-azure-containerservice**: AKS cluster management
- ✅ **provider-azure-network**: VNets, subnets, security groups
- ✅ **provider-azure-sql**: Azure SQL databases
- ✅ **provider-azure-storage**: Storage accounts and containers

#### GCP - Modular Providers (v1.7.0+)
- ✅ **provider-gcp-container**: GKE cluster management
- ✅ **provider-gcp-compute**: VPCs, subnets, firewall rules, instances
- ✅ **provider-gcp-sql**: Cloud SQL databases
- ✅ **provider-gcp-storage**: Cloud Storage buckets

#### DigitalOcean (v0.7.0+)
- ✅ Updated to latest version with enhanced features
- ✅ DOKS cluster management
- ✅ Droplets, Load Balancers, Volumes
- ✅ Improved credential handling

#### Civo (v0.3.0+)
- ✅ Updated with improved K3s support
- ✅ Civo Cloud K3s cluster management
- ✅ Instance and network management
- ✅ Enhanced regional support

#### Kubernetes & Helm
- ✅ **provider-kubernetes (v0.14.0+)**: Manage existing K8s clusters
  - Kind clusters
  - On-premises Kubernetes
  - Any accessible Kubernetes cluster
- ✅ **provider-helm (v0.19.0+)**: Deploy applications across all clusters

### 📦 New Compositions

#### Kind/On-Premises Kubernetes Composition
**File**: `compositions/cluster/kind-kubernetes.yaml`

**Features**:
- Manages existing Kubernetes clusters (not provisioning new ones)
- Supports both in-cluster and external kubeconfig access
- Automated add-on installation:
  - Cilium CNI
  - Nginx Ingress
  - Metrics Server
- Cluster health checking
- ConfigMap-based metadata storage

**Use Cases**:
- Local development with Kind
- On-premises data center Kubernetes
- Edge computing with K3s/K0s
- Testing with MicroK8s
- Any standard Kubernetes distribution

**Example**:
```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: Cluster
metadata:
  name: local-kind
spec:
  compositionSelector:
    matchLabels:
      provider: kind
  parameters:
    clusterName: local-kind
    clusterType: development
    installAddons: true
    addons:
      - cilium
      - nginx-ingress
      - metrics-server
```

### 🔐 Credential Management

#### New Provider Configurations

**Created Files**:
1. `providers/aws-providerconfig.yaml` - 5 AWS provider configs
2. `providers/azure-providerconfig.yaml` - 4 Azure provider configs
3. `providers/gcp-providerconfig.yaml` - 4 GCP provider configs
4. `providers/digitalocean-providerconfig.yaml` - DigitalOcean config
5. `providers/civo-providerconfig.yaml` - Civo config
6. `providers/kubernetes-providerconfig.yaml` - Kubernetes configs (2 variants)
7. `providers/helm-providerconfig.yaml` - Helm configs (2 variants)
8. `providers/credential-secrets-template.yaml` - Templates for all secrets

#### Credential Secrets

All secrets must be created in the `crossplane-system` namespace:

```bash
# AWS
kubectl create secret generic aws-credentials \
  -n crossplane-system \
  --from-literal=credentials="[default]
aws_access_key_id = YOUR_KEY
aws_secret_access_key = YOUR_SECRET"

# Azure
kubectl create secret generic azure-credentials \
  -n crossplane-system \
  --from-literal=credentials='{"clientId":"...","clientSecret":"...","tenantId":"...","subscriptionId":"..."}'

# GCP
kubectl create secret generic gcp-credentials \
  -n crossplane-system \
  --from-file=credentials=./gcp-service-account.json

# DigitalOcean
kubectl create secret generic digitalocean-credentials \
  -n crossplane-system \
  --from-literal=token=YOUR_TOKEN

# Civo
kubectl create secret generic civo-credentials \
  -n crossplane-system \
  --from-literal=token=YOUR_API_KEY

# External Kubernetes Cluster
kubectl create secret generic kubernetes-credentials \
  -n crossplane-system \
  --from-file=kubeconfig=./cluster-kubeconfig.yaml
```

## Migration Guide

### Upgrading from Crossplane v2.0

1. **Update Package Definition**:
   ```bash
   # The crossplane.yaml has been updated to v2.1.0
   # Re-package and deploy the configuration
   make package
   ```

2. **Install New Providers**:
   ```bash
   # The new modular providers will be installed automatically
   # when you apply the updated configuration package
   kubectl apply -f dist/adhar-control-plane-*.xpkg
   ```

3. **Create Provider Credentials**:
   ```bash
   # Create secrets for each provider you plan to use
   # See credential-secrets-template.yaml for templates
   kubectl apply -f providers/credential-secrets-template.yaml
   ```

4. **Apply Provider Configs**:
   ```bash
   # Apply all provider configurations
   kubectl apply -f providers/
   ```

5. **Verify Providers**:
   ```bash
   # Check all providers are healthy
   kubectl get providers
   
   # Expected output should show all providers as HEALTHY
   # and INSTALLED=True
   ```

### Testing the Upgrade

1. **Test AWS EKS**:
   ```yaml
   apiVersion: platform.adhar.io/v1alpha1
   kind: Cluster
   metadata:
     name: test-eks
   spec:
     compositionSelector:
       matchLabels:
         provider: aws
     parameters:
       clusterName: test-eks
       region: us-west-2
       nodeCount: 2
   ```

2. **Test Kind Cluster**:
   ```yaml
   apiVersion: platform.adhar.io/v1alpha1
   kind: Cluster
   metadata:
     name: test-kind
   spec:
     compositionSelector:
       matchLabels:
         provider: kind
     parameters:
       clusterName: test-kind
       installAddons: true
       addons:
         - cilium
         - nginx-ingress
   ```

3. **Verify Resources**:
   ```bash
   # Check composite resources
   kubectl get composite
   
   # Check managed resources
   kubectl get managed
   
   # Check claims
   kubectl get cluster
   ```

## Architecture Changes

### Before (Crossplane v2.0)
```
Crossplane v2.0
├── provider-aws (monolithic)
├── provider-azure (monolithic)
├── provider-gcp (monolithic)
├── provider-digitalocean
├── provider-civo
└── provider-helm
```

### After (Crossplane v2.1)
```
Crossplane v2.1
├── AWS (modular)
│   ├── provider-aws-eks
│   ├── provider-aws-ec2
│   ├── provider-aws-rds
│   ├── provider-aws-iam
│   └── provider-aws-s3
├── Azure (modular)
│   ├── provider-azure-containerservice
│   ├── provider-azure-network
│   ├── provider-azure-sql
│   └── provider-azure-storage
├── GCP (modular)
│   ├── provider-gcp-container
│   ├── provider-gcp-compute
│   ├── provider-gcp-sql
│   └── provider-gcp-storage
├── DigitalOcean
│   └── provider-digitalocean (v0.7.0)
├── Civo
│   └── provider-civo (v0.3.0)
├── Kubernetes
│   └── provider-kubernetes (v0.14.0) [NEW]
└── Helm
    └── provider-helm (v0.19.0)
```

## Benefits

### For Platform Engineers
- ✅ **Modular Updates**: Update specific cloud services independently
- ✅ **Better Troubleshooting**: Isolated controllers make debugging easier
- ✅ **Resource Efficiency**: Install only the providers you need
- ✅ **Reduced Risk**: Smaller blast radius for provider updates

### For Developers
- ✅ **Unified API**: Same Crossplane API across all providers
- ✅ **Local Development**: Full support for Kind clusters
- ✅ **Multi-Cloud**: Easy migration between cloud providers
- ✅ **Self-Service**: Declarative infrastructure provisioning

### For Operations
- ✅ **Better Observability**: Enhanced metrics and logging
- ✅ **Faster Reconciliation**: Performance improvements in v2.1
- ✅ **Credential Management**: Centralized secret management
- ✅ **GitOps Ready**: All configurations version controlled

## Known Issues & Limitations

### Provider-Specific
- **AWS**: Requires explicit region specification for all resources
- **Azure**: Resource group must exist before cluster creation
- **GCP**: Project ID must be set in provider config
- **Kind**: Cannot provision new clusters, only manage existing ones

### General
- Provider credentials must be created manually before use
- Some resources may take 10-15 minutes to provision
- Cross-provider networking requires manual setup

## Troubleshooting

### Provider Not Installing
```bash
# Check provider status
kubectl get providers

# Check provider pod logs
kubectl logs -n crossplane-system deployment/provider-<name>

# Describe provider for events
kubectl describe provider <name>
```

### Credential Issues
```bash
# Verify secret exists
kubectl get secret <provider>-credentials -n crossplane-system

# Validate secret format
kubectl get secret <provider>-credentials -n crossplane-system -o yaml
```

### Composition Errors
```bash
# Check composite resource status
kubectl describe <composite-type> <name>

# Check managed resources
kubectl get managed

# Check function logs
kubectl logs -n crossplane-system deployment/function-go-templating
```

## Resources

- [Crossplane v2.1 Release Notes](https://github.com/crossplane/crossplane/releases/tag/v2.1.0)
- [Upbound Provider Docs](https://marketplace.upbound.io/)
- [Crossplane Compositions](https://docs.crossplane.io/latest/concepts/compositions/)
- [Adhar Control Plane README](../README.md)
- [Provider Configuration Guide](../configuration/providers/README.md)

## Support

For issues or questions:
- GitHub Issues: https://github.com/adhar-io/adhar/issues
- Slack: https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww
- Documentation: https://github.com/adhar-io/adhar/tree/master/docs

## Changelog

### v2.1.0 - November 8, 2025
- ✅ Upgraded Crossplane to v2.1.0
- ✅ Migrated to modular AWS providers (EKS, EC2, RDS, IAM, S3)
- ✅ Migrated to modular Azure providers (AKS, Network, SQL, Storage)
- ✅ Migrated to modular GCP providers (GKE, Compute, SQL, Storage)
- ✅ Updated DigitalOcean provider to v0.7.0
- ✅ Updated Civo provider to v0.3.0
- ✅ Added provider-kubernetes v0.14.0 for Kind/on-prem support
- ✅ Updated provider-helm to v0.19.0
- ✅ Added function-patch-and-transform v0.6.0
- ✅ Created comprehensive provider configurations for all platforms
- ✅ Added Kind/on-premises Kubernetes composition
- ✅ Created credential secret templates for all providers
- ✅ Updated documentation with v2.1 features and migration guide

