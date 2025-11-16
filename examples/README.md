# Adhar Platform Examples

This directory contains basic examples of Adhar Platform Custom Resources (CRs) to help you get started.

## 📁 Structure

```
examples/
├── README.md                          # This file
├── auth-config.yaml                   # Authentication configuration
├── application.yaml                   # Application deployment example
├── application-template.yaml          # Application template example
├── cluster.yaml                       # Cluster configuration (Kind)
├── database.yaml                      # Database provisioning
├── deployment.yaml                    # GitOps deployment
├── environment.yaml                   # Environment definition
├── organisation.yaml                  # Organization setup
├── pipeline.yaml                      # CI/CD pipeline
├── registry.yaml                      # Container registry
├── repository.yaml                    # Git repository
├── secret.yaml                        # Secret management
└── team.yaml                          # Team configuration
```

## 🚀 Quick Start

### 1. Basic Local Setup

Create a local development environment with Kind:

```bash
# Apply the cluster configuration
kubectl apply -f cluster.yaml

# Create an organization
kubectl apply -f organisation.yaml

# Create a team
kubectl apply -f team.yaml

# Create a development environment
kubectl apply -f environment.yaml
```

### 2. Deploy an Application

```bash
# Set up container registry
kubectl apply -f registry.yaml

# Create a Git repository
kubectl apply -f repository.yaml

# Create a database
kubectl apply -f database.yaml

# Deploy the application
kubectl apply -f application.yaml
```

### 3. Configure Authentication

```bash
# Apply authentication configuration
adhar auth configure -f auth-config.yaml
```

## 📚 Resource Descriptions

### Core Resources

| Resource | Description | Example File |
|----------|-------------|--------------|
| **Organisation** | Top-level organizational unit | `organisation.yaml` |
| **Team** | Development team within an org | `team.yaml` |
| **Environment** | Deployment environment (dev/staging/prod) | `environment.yaml` |
| **Cluster** | Kubernetes cluster configuration | `cluster.yaml` |

### Application Resources

| Resource | Description | Example File |
|----------|-------------|--------------|
| **Application** | Complete application definition | `application.yaml` |
| **ApplicationTemplate** | Reusable application template | `application-template.yaml` |
| **Deployment** | GitOps-based deployment | `deployment.yaml` |
| **Pipeline** | CI/CD pipeline definition | `pipeline.yaml` |

### Infrastructure Resources

| Resource | Description | Example File |
|----------|-------------|--------------|
| **Database** | Database provisioning | `database.yaml` |
| **Registry** | Container registry configuration | `registry.yaml` |
| **Repository** | Git repository reference | `repository.yaml` |
| **Secret** | Platform secret management | `secret.yaml` |

## 🎯 Common Use Cases

### Use Case 1: Create a Microservice

```bash
# 1. Create organization and team
kubectl apply -f organisation.yaml
kubectl apply -f team.yaml

# 2. Set up infrastructure
kubectl apply -f environment.yaml
kubectl apply -f registry.yaml
kubectl apply -f database.yaml

# 3. Deploy application
kubectl apply -f application.yaml
```

### Use Case 2: Local Development Environment

```bash
# Create a local Kind cluster
kubectl apply -f cluster.yaml

# Create development environment
kubectl apply -f environment.yaml

# Deploy your app
kubectl apply -f application.yaml
```

### Use Case 3: Multi-Environment Deployment

```bash
# Create environments
cat environment.yaml | sed 's/dev/staging/' | kubectl apply -f -
cat environment.yaml | sed 's/dev/prod/' | kubectl apply -f -

# Deploy to each environment via GitOps
kubectl apply -f deployment.yaml
```

## 🔧 Customization

All examples use placeholder values. Customize them for your needs:

1. **Update Organization Details**:
   ```yaml
   metadata:
     name: "your-org-name"
   spec:
     displayName: "Your Organization"
   ```

2. **Configure Cloud Provider**:
   ```yaml
   spec:
     provider: "aws"  # or gcp, azure, digitalocean, civo
   ```

3. **Adjust Resource Limits**:
   ```yaml
   spec:
     resourceQuota:
       hard:
         requests.cpu: "100"
         requests.memory: "512Gi"
   ```

## 📖 Documentation

For detailed information, see:

- **[Getting Started Guide](../docs/GETTING_STARTED.md)** - Setup and installation
- **[User Guide](../docs/USER_GUIDE.md)** - Platform usage and features
- **[Architecture](../docs/ARCHITECTURE.md)** - System design and concepts
- **[Provider Guide](../docs/PROVIDER_GUIDE.md)** - Cloud provider integration

## 🆘 Need Help?

- **Documentation**: See [docs/](../docs/)
- **Issues**: [GitHub Issues](https://github.com/adhar-io/adhar/issues)
- **Slack**: [Join our Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)
- **Email**: support@adhar.io

## ⚠️ Important Notes

- These are **basic examples** for learning and testing
- **Do not use in production** without proper configuration
- Replace all placeholder values with your actual values
- Review security settings before deploying
- Consult the [Security Policy](../SECURITY.md) for best practices

---

**Happy Building!** 🚀

