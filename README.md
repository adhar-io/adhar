<div align="center">

## Adhar Platform – Production-Ready Internal Developer Platform

<a href="https://github.com/adhar/platform"><img alt="Build" src="https://img.shields.io/badge/build-passing-brightgreen"></a>
<a href="https://golang.org"><img alt="Go Version" src="https://img.shields.io/badge/go-1.21%2B-blue"></a>
<a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache%202.0-green"></a>
<a href="https://github.com/adhar/platform"><img alt="Status" src="https://img.shields.io/badge/status-production%20ready-success"></a>

</div>

> Adhar (Sanskrit for "Foundation") is a unified, Kubernetes‑native Internal Developer Platform (IDP) for the entire software delivery lifecycle. It provides a consistent multi‑cloud experience, GitOps‑first operations, and a secure, production‑grade foundation.

---

## Table of Contents

- Overview
- Key Features
- Architecture
- Getting Started
- Configuration
- Usage Examples
- Platform Components
- Platform Capabilities
- Security and Observability
- Testing and Quality
- Build and Release
- Roadmap
- Community and License

---

## Overview

Adhar delivers a unified workflow for platform and application teams across six providers (AWS, Azure, GCP, DigitalOcean, Civo, Kind) with a single CLI and GitOps‑managed platform services.

## Key Features

- Multi‑Cloud Support (6 providers)
- Unified CLI with automatic kubeconfig/context management
- Production‑grade infrastructure: error handling, structured logging, automated backups
- GitOps‑driven operations with ArgoCD, Helm, Kustomize
- Security by default: zero‑trust networking, policy enforcement

### Providers

- AWS (EKS), Azure (AKS), GCP (GKE), DigitalOcean, Civo, Kind (local)

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Adhar Platform                           │
├─────────────────────────────────────────────────────────────┤
│  AWS  │  Azure  │  GCP  │ DigitalOcean │  Civo  │  Kind    │
├─────────────────────────────────────────────────────────────┤
│ Core Services │ Security Services │ Observability Services   │
├─────────────────────────────────────────────────────────────┤
│        CLI Interface │ API Gateway │ Web UI (Console)        │
└─────────────────────────────────────────────────────────────┘
```

---

## Getting Started

### Prerequisites

```bash
go 1.21+
docker
kubectl
helm
# optional
kind
```

### Quick Start (installer)

```bash
# Install Adhar CLI
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/hack/install.sh | bash

# Create a local cluster with core services
adhar up

# Access the platform
open https://adhar.localtest.me
```

### From Source

```bash
# Clone the repository
git clone https://github.com/adhar/platform.git
cd adhar

# Build and install the CLI (standard Go install)
go install ./...

# Verify
adhar version
```

### Access Platform Services (local)

- Adhar Console: https://adhar.localtest.me/
- ArgoCD: https://adhar.localtest.me/argocd/
- Gitea: https://adhar.localtest.me/gitea/
- Grafana: https://adhar.localtest.me/grafana/

Credentials: `adhar get secrets`

---

## Configuration

```yaml
# config.yaml
global:
  log_level: info
  log_format: json
  log_output: stdout

providers:
  civo:
    type: civo
    region: MUM1
    primary: true
    token: "your-civo-token"
    config:
      size: g4s.kube.medium
      disk_image: ubuntu-22.04-x64
      firewall_rules:
        - label: adhar-cluster
          rules:
            - protocol: tcp
              start_port: "22"
              end_port: "22"
              cidr: ["0.0.0.0/0"]
              direction: ingress

environments:
  development:
    providers: ["kind", "civo"]
    replicas: 1
    ha_mode: false
  production:
    providers: ["aws", "azure"]
    replicas: 3
    ha_mode: true
```

More examples: see `docs/CONFIGURATION.md` and `docs/examples/`.

---

## Usage Examples

### Unified CLI

```bash
# Create a cluster
adhar cluster create my-cluster --provider civo --region MUM1

# List clusters
adhar cluster list

# Get details
adhar cluster get my-cluster

# Delete cluster
adhar cluster delete my-cluster

# Get kubeconfig
adhar cluster kubeconfig my-cluster
```

### Advanced Operations

```bash
# Multi-cloud deployment
adhar cluster create prod-cluster \
  --provider aws \
  --region us-west-2 \
  --ha-mode \
  --replicas 3

# Environment-specific
adhar cluster create dev-cluster \
  --environment development \
  --provider civo

# Backup and restore
adhar cluster backup my-cluster
adhar cluster restore backup-id target-cluster

# Health and metrics
adhar cluster health my-cluster
adhar cluster metrics my-cluster

# Get platform secrets
adhar get secrets -p argocd     # ArgoCD admin credentials
adhar get secrets -p gitea      # Gitea admin credentials

### Provider-Specific

```bash
# AWS
adhar cluster create aws-cluster \
  --provider aws \
  --instance-type t3.medium \
  --vpc-cidr 10.0.0.0/16

# Azure
adhar cluster create azure-cluster \
  --provider azure \
  --vm-size Standard_D2s_v3 \
  --resource-group my-rg

# GCP
adhar cluster create gcp-cluster \
  --provider gcp \
  --machine-type e2-medium \
  --network default
```

---

## Platform Components

- Adhar Console: self‑service portal for developers and platform admins
- CLI: powerful automation and workflows from the terminal
- Control Plane (API server): validates desired state and reconciles actual state
- AI Assistance: intelligent guidance across setup, troubleshooting, and optimization
- Git‑based Infrastructure: desired state managed in Git for traceability
- Golden Templates Catalog: curated Helm charts and templates for golden paths

---

## Platform Capabilities

Core applications are always installed; optional applications can be activated and configured via the Console.

- Kubernetes, Cilium, Nginx Ingress, External DNS
- Argo CD, Argo Workflows, Argo Events, Argo Rollouts
- Gitea, Harbor, Kaniko, Paketo Buildpacks
- CloudNativePG, Redis, MinIO
- Prometheus, Grafana, Loki, Tempo, Jaeger, OpenTelemetry, Hubble
- Vault, Kyverno, Trivy, Falco
- Crossplane, Headlamp, Backstage

### Supported Providers

- `aws` (EKS), `azure` (AKS), `google` (GKE), `digitalocean`, `civo`, `kind`

---

## Security and Observability

### Security

- Zero‑trust networking with Cilium CNI
- Policy enforcement with Kyverno
- Secrets management with Vault
- Image and workload scanning with Trivy; runtime protection with Falco

### Observability

- Metrics: Prometheus
- Logs: Loki
- Traces: Tempo and Jaeger
- Network visibility: Hubble; dashboards in Grafana

Structured logging example:

```go
logger.Info("Creating cluster", logger.WithFields(logger.Fields{
    "provider": "civo",
    "cluster": clusterName,
    "region": region,
}))
logger.ErrorWithContext("Failed to create cluster", err)
```

---

## Testing and Quality

```bash
# Run unit tests
go test ./...

# Static checks (if configured)
golangci-lint run || true
```

---

## Build and Release

```bash
# Build CLI locally
go build ./...

# Install CLI
go install ./...
```

---

## Roadmap (high‑level)

- Multi‑cloud provider support (complete)
- Unified CLI experience (complete)
- Advanced networking and security hardening (ongoing)
- Multi‑cluster management and federation (upcoming)
- Intelligent autoscaling and anomaly detection (upcoming)

---

## Community and License

- Discussions and issues: GitHub
- Contributions welcome – see `docs/CONTRIBUTING.md`

This project is licensed under the Apache 2.0 License – see `LICENSE` for details.

---

Built with ❤️ by the Adhar Platform team.
