<div align="center">

# Adhar Platform – The Open Foundation

<a href="https://github.com/adhar/platform"><img alt="Build" src="https://img.shields.io/badge/build-passing-brightgreen"></a>
<a href="https://golang.org"><img alt="Go Version" src="https://img.shields.io/badge/go-1.24%2B-blue"></a>
<a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache%202.0-green"></a>
<a href="https://github.com/adhar/platform"><img alt="Status" src="https://img.shields.io/badge/status-active%20development-orange"></a>
<a href="https://github.com/adhar/platform"><img alt="Issues" src="https://img.shields.io/github/issues/adhar/platform"></a>
<a href="https://github.com/adhar/platform"><img alt="Contributors" src="https://img.shields.io/github/contributors/adhar/platform"></a>

**Built with ❤️ for developers!**

</div>

<div align="center">

⚠️ **WARNING: This project is in active development and is NOT ready for production use.** ⚠️

</div>

> **Adhar** (Sanskrit for "Foundation") is a powerful Internal Developer Platform that makes it easy to build, deploy, and manage cloud-native applications across multiple cloud providers. Built on Kubernetes with 60+ carefully selected open-source tools, it gives you enterprise-level security, reliability, and an amazing developer experience while keeping you in full control of your infrastructure and policies.

---

## 🎯 What is Adhar Platform?

Adhar is a **Unified Internal Developer Platform (IDP)** that revolutionizes how organizations plan, build, deploy, manage and operate cloud-native applications. By utilizing Kubernetes and cloud infrastructure with leading open-source tools and frameworks, it can streamline the entire software development lifecycle in modern enterprises. Adhar aims to provide following core capabilities:

- **🎯 Developer Efficiency & Business Outcomes** - Freedom and autonomy developers need to excel
- **⚖️ Integrated Governance & Controls** - Achieve governance without blocking developer efficiency
- **🤖 AI-Powered Development** - Intelligent assistance and automation at every development task
- **🔄 GitOps-First Operations** - Declarative infrastructure management using ArgoCD
- **🛡️ Security by Default** - Zero-trust networking and comprehensive compliance frameworks
- **⚡ Self-Service Platform** - Developer autonomy with built-in governance and compliance
- **🏗️ Unified Multi-Cloud Experience** - Leading cloud providers support(AWS, Azure, GCP, DigitalOcean, Civo)
- **📊 Data & Analytics Platforms** - Complete ML/AI platform with advanced analytics capabilities
- **🌐 Hybrid Cloud Ready** - Seamless integration between on-premises and cloud environments
- **🚀 Future-Ready Platform** - Designed to adapt to emerging technologies and methodologies

---

## 📋 Prerequisites

Before you begin, make sure you have the following installed on your system:

### **Required Software**
- **Docker** (v20.10+) - Container runtime
- **kubectl** (v1.24+) - Kubernetes command-line tool

### **System Requirements**
- **RAM**: Minimum 8GB, Recommended 16GB+
- **Storage**: At least 20GB free disk space
- **CPU**: 4+ cores recommended
- **OS**: macOS 10.15+, Ubuntu 18.04+, or Windows 10+

### **Optional Tools**
- **Kind** - For local Kubernetes clusters
- **Git** - For version control operations
- **VS Code** - For enhanced development experience

---

## 📚 Getting Started

### Local Development

```bash
# Install Adhar CLI
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/scripts/install.sh | bash

# Verify Adhar installation
adhar version

# Create local development environment
adhar up

# Access Adhar platform console
open https://adhar.localtest.me

# Deploy your first application
adhar apps deploy my-app --template=nodejs
```

### Production Deployment

```bash
# 1. Configure production environment
adhar up -f config.yaml

# 2. Deploy production platform
adhar up -f production-config.yaml

# 3. Verify platform health
adhar status --detailed
```

---

## ⚙️ Configuration

### Sample Configuration

```yaml
# config.yaml
provider: kind
enableHAMode: false
clusterName: adhar-local
region: local

components:
  - cilium
  - nginx
  - gitea
  - argocd
  - crossplane
  - keycloak
  - prometheus
  - grafana
```

---

## 🤝 Community & Support

### **Getting Help**
- **GitHub Issues**: [Report bugs and request features](https://github.com/adhar/platform/issues)
- **Discord Community**: [Join our community](https://discord.gg/adhar-platform)
- **Documentation**: [Comprehensive guides](https://docs.adhar.io)
- **Examples**: [Sample applications and configurations](examples/)

### **Contributing**
We welcome contributions! Please see our [Contributing Guide](docs/CONTRIBUTING.md) for details.

### **License**
This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

---

## 🚀 Quick Commands Reference

```bash
# Platform Management
adhar up                    # Create/start platform
adhar down                  # Stop/destroy platform
adhar status               # Check platform health
adhar config               # Manage configuration

# Application Management
adhar apps deploy          # Deploy applications
adhar get applications     # List applications
adhar get secrets          # Get platform secrets

# Cluster Management
adhar cluster create       # Create new cluster
adhar cluster list         # List clusters
adhar cluster delete       # Delete cluster

# Help & Information
adhar help                 # Show help
adhar version              # Show version
adhar ai help              # AI-powered assistance
```

---

<div align="center">

**Adhar Platform v0.3.8 • Built with ❤️ for developers**

[Website](https://adhar.io) • [Documentation](https://docs.adhar.io) • [Discord](https://discord.gg/adhar-platform) • [GitHub](https://github.com/adhar/platform)

</div>
