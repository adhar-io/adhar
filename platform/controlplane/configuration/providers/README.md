# Provider Configuration

This directory contains provider configurations for all supported cloud providers and Kubernetes platforms.

## Supported Providers

### Cloud Providers

#### AWS (Amazon Web Services)
- **Providers**: EKS, EC2, RDS, IAM, S3
- **Configuration**: `aws-providerconfig.yaml`
- **Resources**: EKS clusters, VPCs, RDS databases, IAM roles, S3 buckets
- **Credential Secret**: `aws-credentials` (AWS access key and secret)

#### Azure (Microsoft Azure)
- **Providers**: Container Service, Network, SQL, Storage
- **Configuration**: `azure-providerconfig.yaml`
- **Resources**: AKS clusters, VNets, Azure SQL, Storage accounts
- **Credential Secret**: `azure-credentials` (Service Principal with client ID, secret, tenant ID, subscription ID)

#### GCP (Google Cloud Platform)
- **Providers**: Container (GKE), Compute, SQL, Storage
- **Configuration**: `gcp-providerconfig.yaml`
- **Resources**: GKE clusters, VPCs, Cloud SQL, Cloud Storage buckets
- **Credential Secret**: `gcp-credentials` (Service Account JSON key)

#### DigitalOcean
- **Provider**: DigitalOcean
- **Configuration**: `digitalocean-providerconfig.yaml`
- **Resources**: DOKS clusters, Droplets, Load Balancers, Volumes
- **Credential Secret**: `digitalocean-credentials` (API token)

#### Civo
- **Provider**: Civo
- **Configuration**: `civo-providerconfig.yaml`
- **Resources**: Civo K3s clusters, Instances, Networks
- **Credential Secret**: `civo-credentials` (API key)

### Kubernetes Platforms

#### Kubernetes Provider (Kind, On-Premises)
- **Provider**: Kubernetes
- **Configuration**: `kubernetes-providerconfig.yaml`
- **Use Cases**: 
  - Managing existing Kind clusters
  - On-premises Kubernetes clusters
  - Self-managed Kubernetes installations
- **Credential Options**:
  - In-cluster (InjectedIdentity) for local access
  - External kubeconfig secret for remote clusters

#### Helm Provider
- **Provider**: Helm
- **Configuration**: `helm-providerconfig.yaml`
- **Purpose**: Deploy applications and add-ons to managed clusters
- **Credential Options**:
  - In-cluster for local deployments
  - External kubeconfig for remote deployments

## Credential Management

### Setting Up Credentials

All provider credentials must be created in the `crossplane-system` namespace before deploying resources.

**Template File**: See `credential-secrets-template.yaml` for credential secret templates.

### Creating Credentials

```bash
# AWS
kubectl create secret generic aws-credentials \
  -n crossplane-system \
  --from-literal=credentials="[default]
aws_access_key_id = YOUR_ACCESS_KEY
aws_secret_access_key = YOUR_SECRET_KEY"

# Azure
kubectl create secret generic azure-credentials \
  -n crossplane-system \
  --from-literal=credentials='{
    "clientId": "YOUR_CLIENT_ID",
    "clientSecret": "YOUR_CLIENT_SECRET",
    "tenantId": "YOUR_TENANT_ID",
    "subscriptionId": "YOUR_SUBSCRIPTION_ID"
  }'

# GCP
kubectl create secret generic gcp-credentials \
  -n crossplane-system \
  --from-file=credentials=./gcp-service-account.json

# DigitalOcean
kubectl create secret generic digitalocean-credentials \
  -n crossplane-system \
  --from-literal=token=YOUR_DO_TOKEN

# Civo
kubectl create secret generic civo-credentials \
  -n crossplane-system \
  --from-literal=token=YOUR_CIVO_API_KEY

# External Kubernetes Cluster
kubectl create secret generic kubernetes-credentials \
  -n crossplane-system \
  --from-file=kubeconfig=./external-cluster-kubeconfig.yaml
```

## Provider Configuration Details

### ProviderConfig Resources

Each provider has dedicated `ProviderConfig` resources that reference credential secrets:

- **AWS**: Separate configs for EKS, EC2, RDS, IAM, S3 (all using `aws-credentials`)
- **Azure**: Separate configs for Container Service, Network, SQL, Storage (all using `azure-credentials`)
- **GCP**: Separate configs for Container, Compute, SQL, Storage (all using `gcp-credentials`)
- **DigitalOcean**: Single config using `digitalocean-credentials`
- **Civo**: Single config using `civo-credentials` with default region
- **Kubernetes**: Two configs (in-cluster and external)
- **Helm**: Two configs (in-cluster and external)

### Automatic Credential Discovery

The Adhar platform includes automatic credential discovery that can:
- Detect credentials from environment variables
- Find credentials in Kubernetes secrets
- Create provider secrets automatically
- Validate all required credentials are present

## Resource Organization

```
providers/
├── aws-providerconfig.yaml           # AWS provider configs (5 providers)
├── azure-providerconfig.yaml         # Azure provider configs (4 providers)
├── gcp-providerconfig.yaml           # GCP provider configs (4 providers)
├── digitalocean-providerconfig.yaml  # DigitalOcean config
├── civo-providerconfig.yaml          # Civo config
├── kubernetes-providerconfig.yaml    # Kubernetes configs (2 variants)
├── helm-providerconfig.yaml          # Helm configs (2 variants)
├── credential-secrets-template.yaml  # Templates for all secrets
└── README.md                         # This file
```

## Best Practices

1. **Least Privilege**: Grant only necessary permissions to service accounts/credentials
2. **Secret Rotation**: Regularly rotate credentials and update secrets
3. **Namespace Isolation**: Keep all provider secrets in `crossplane-system` namespace
4. **Secret Naming**: Use consistent naming: `<provider>-credentials`
5. **Documentation**: Document which service account/credentials are used for each environment
6. **Backup**: Securely backup credential information
7. **Monitoring**: Monitor credential usage and expiration

## Troubleshooting

### Provider Not Ready
```bash
# Check provider status
kubectl get providers

# Check provider logs
kubectl logs -n crossplane-system deployment/<provider-name>
```

### Credential Issues
```bash
# Verify secret exists
kubectl get secret <provider>-credentials -n crossplane-system

# Check secret contents (be careful with output!)
kubectl get secret <provider>-credentials -n crossplane-system -o yaml
```

### ProviderConfig Not Found
```bash
# List all providerconfigs
kubectl get providerconfigs

# Check specific providerconfig
kubectl describe providerconfig <config-name>
```
