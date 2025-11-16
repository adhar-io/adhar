# Adhar Platform Documentation

**Version**: v0.3.8  
**Last Updated**: November 2025

---

## 📚 Documentation Overview

Welcome to the Adhar Platform documentation! This comprehensive guide will help you understand, deploy, and operate Adhar - an open Internal Developer Platform for cloud-native engineering.

---

## 🎯 Quick Navigation

### For New Users
Start here to get Adhar up and running quickly:

1. **[Getting Started Guide](GETTING_STARTED.md)** - Install and deploy your first cluster (10 minutes)
2. **[User Guide](USER_GUIDE.md)** - Learn about platform capabilities and day-to-day usage

### For Platform Engineers  
Deep dive into architecture and implementation:

1. **[Architecture](ARCHITECTURE.md)** - System design, components, and technical overview
2. **[Provider Guide](PROVIDER_GUIDE.md)** - Multi-cloud provider system implementation
3. **[Advanced Guide](ADVANCED.md)** - HA mode, production deployment, and best practices

### For Contributors
1. **[Contributing Guide](../CONTRIBUTING.md)** - How to contribute to Adhar
2. Architecture and Provider guides for understanding the codebase

---

## 📖 Documentation Structure

### Core Documentation

| Document | Description | Audience |
|----------|-------------|----------|
| **[Getting Started](GETTING_STARTED.md)** | Installation, quick start, and first deployment | All users |
| **[User Guide](USER_GUIDE.md)** | Platform capabilities, configuration, CLI reference, and best practices | Developers, DevOps |
| **[Architecture](ARCHITECTURE.md)** | Technical architecture, design principles, and component overview | Platform Engineers |
| **[Provider Guide](PROVIDER_GUIDE.md)** | Multi-cloud provider system with detailed implementation guides | Platform Engineers |
| **[Advanced Guide](ADVANCED.md)** | HA mode, production deployment, migration, disaster recovery | Operations, SRE |

---

## 🚀 Quick Start

```bash
# Install Adhar CLI
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/scripts/install.sh | bash

# Create local cluster (Kind)
adhar up

# Access the platform
open https://adhar.localtest.me
```

---

## 🎯 Common Tasks

### Installation & Setup
- [Installing Adhar CLI](GETTING_STARTED.md#installation)
- [Creating a local cluster](GETTING_STARTED.md#quick-start)
- [Accessing platform services](GETTING_STARTED.md#access-platform-services)

### Configuration
- [Provider configuration](USER_GUIDE.md#configuration)
- [Environment setup](USER_GUIDE.md#environment-management)
- [HA mode configuration](ADVANCED.md#high-availability-mode)

### Application Management
- [Deploying applications](USER_GUIDE.md#application-management)
- [Managing environments](USER_GUIDE.md#environment-management)
- [GitOps workflows](USER_GUIDE.md#platform-services)

### Operations
- [Monitoring & observability](USER_GUIDE.md#monitoring--observability)
- [Security & compliance](USER_GUIDE.md#security--compliance)
- [Disaster recovery](ADVANCED.md#disaster-recovery)
- [Troubleshooting](ADVANCED.md#troubleshooting)

---

## 🏗️ Platform Architecture

Adhar implements a **Management Cluster First** architecture:

- **Control Plane**: Central Kubernetes cluster with Cilium CNI
- **GitOps**: Declarative operations with ArgoCD + Gitea
- **Multi-Cloud**: Unified abstraction with Crossplane
- **Security**: Zero-trust networking, Vault, Keycloak, policy enforcement
- **Observability**: Prometheus, Grafana, Loki, Jaeger

Learn more: [Architecture Documentation](ARCHITECTURE.md)

---

## 🌍 Multi-Cloud Support

Adhar supports 6 providers with unified experience:

| Provider | Status | Use Case |
|----------|--------|----------|
| **Kind** | ✅ Production Ready | Local development |
| **AWS (EKS)** | ✅ Production Ready | Enterprise cloud |
| **GCP (GKE)** | ✅ Production Ready | Google Cloud |
| **Azure (AKS)** | ✅ Production Ready | Microsoft Azure |
| **DigitalOcean** | ✅ Production Ready | Cost-effective cloud |
| **Civo** | ✅ Production Ready | Fast provisioning |

Learn more: [Provider Guide](PROVIDER_GUIDE.md)

---

## 📦 Platform Capabilities

### Core Services (Always Installed)
- **Cilium**: eBPF-based networking and security
- **ArgoCD**: GitOps continuous deployment
- **Gitea**: Git repository hosting
- **Nginx Ingress**: Traffic routing
- **Crossplane**: Infrastructure as code

### Integrated Tools (60+)
- **Security**: Vault, Keycloak, Kyverno, Falco, Trivy
- **Observability**: Prometheus, Grafana, Loki, Jaeger, Tempo
- **Data**: PostgreSQL, Redis, MinIO, Kafka, Elasticsearch
- **Developer**: JupyterHub, Code Server, Harbor
- **CI/CD**: Argo Workflows, Tekton, Kaniko

Learn more: [User Guide - Platform Capabilities](USER_GUIDE.md#platform-capabilities)

---

## 🔒 Security & Compliance

- **Zero-Trust Networking**: Cilium network policies
- **Secrets Management**: HashiCorp Vault
- **Identity & Access**: Keycloak with RBAC
- **Policy Enforcement**: Kyverno policies
- **Vulnerability Scanning**: Trivy automated scans
- **Runtime Security**: Falco threat detection

Learn more: [User Guide - Security](USER_GUIDE.md#security--compliance)

---

## 📊 Monitoring & Observability

- **Metrics**: Prometheus + Grafana dashboards
- **Logs**: Loki log aggregation
- **Traces**: Jaeger distributed tracing
- **Network**: Hubble observability
- **Alerts**: Pre-configured alerting rules

Learn more: [User Guide - Monitoring](USER_GUIDE.md#monitoring--observability)

---

## 🎓 Learning Path

### Beginner (Week 1)
1. Read [Getting Started](GETTING_STARTED.md)
2. Deploy local cluster with `adhar up`
3. Explore platform services
4. Deploy first application

### Intermediate (Week 2-3)
1. Read [User Guide](USER_GUIDE.md)
2. Configure cloud provider
3. Set up production environment
4. Implement GitOps workflows

### Advanced (Week 4+)
1. Read [Architecture](ARCHITECTURE.md)
2. Study [Provider Guide](PROVIDER_GUIDE.md)
3. Read [Advanced Guide](ADVANCED.md)
4. Implement HA mode
5. Set up disaster recovery

---

## 🤝 Getting Help

- **Documentation**: You're reading it!
- **GitHub Issues**: [Report bugs or request features](https://github.com/adhar-io/adhar/issues)
- **Slack Community**: [Join our Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)
- **Contributing**: See [Contributing Guide](../CONTRIBUTING.md)

---

## 📝 Additional Resources

### Examples & Samples
- Configuration examples in each guide
- Sample files in [samples/](../samples/) directory (if available)
- Real-world use cases in documentation

### Images & Diagrams
- Architecture diagrams in [images/](images/) directory
- System design visuals
- Network topology diagrams

### External Resources
- [Kubernetes Documentation](https://kubernetes.io/docs/)
- [Crossplane Documentation](https://docs.crossplane.io/)
- [ArgoCD Documentation](https://argo-cd.readthedocs.io/)
- [Cilium Documentation](https://docs.cilium.io/)

---

## 🚀 Next Steps

1. **New to Adhar?** Start with [Getting Started Guide](GETTING_STARTED.md)
2. **Ready to deploy?** Check [User Guide](USER_GUIDE.md)
3. **Building platforms?** Explore [Architecture](ARCHITECTURE.md)
4. **Going to production?** Read [Advanced Guide](ADVANCED.md)

---

**Built with ❤️ for developers by platform engineers**

*Adhar (अधार) - The Foundation of Cloud-Native Engineering*

