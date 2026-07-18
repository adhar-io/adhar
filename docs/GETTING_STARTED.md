# Getting Started with Adhar

**Version**: v0.1.0

From zero to a running Internal Developer Platform on your machine in under 10 minutes.

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| **Docker** | v20.10+ | Running, with your user in the `docker` group |
| **kubectl** | v1.24+ | For inspecting the cluster (Adhar drives it for you) |
| **RAM** | 8 GB min, 16 GB recommended | The curated local core runs ~16 services |
| **Disk** | 20 GB+ free | Container images and volumes |
| **CPU** | 4+ cores | |

## Install the CLI

```bash
# Quick install (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/scripts/install.sh | bash

# Or Homebrew (tap + trust once, then it's just `brew install adhar`)
brew tap adhar-io/tap
brew trust adhar-io/tap   # newer Homebrew versions gate third-party taps
brew install adhar

# Or download a release archive from
# https://github.com/adhar-io/adhar/releases

adhar version
```

## Launch the Platform

```bash
adhar up
```

What happens (details in the [Architecture](ARCHITECTURE.md#4-deployment-lifecycle)): Adhar creates a Kind cluster, installs the foundation in order (Cilium CNI → Gateway → ArgoCD → Gitea), seeds the in-cluster Git repos with the platform stack, and lets ArgoCD deploy the curated package core via GitOps. Progress is streamed to your terminal; the platform is ready when you see the success banner.

Useful variants:

```bash
adhar up --port 9443     # if 8443 is taken (HTTP port auto-derives)
adhar up --recreate      # delete and recreate an existing cluster
adhar up --dry-run       # preview without creating anything
```

## Take the Tour

Every service is a subdomain of `adhar.localtest.me` (which resolves to your machine — no /etc/hosts editing). The TLS certificate is self-signed, so accept the browser warning locally.

| Service | URL | Purpose |
|---------|-----|---------|
| Adhar Console | `https://console.adhar.localtest.me:8443` | Developer portal |
| ArgoCD | `https://argocd.adhar.localtest.me:8443` | GitOps — see every deployed package |
| Gitea | `https://gitea.adhar.localtest.me:8443` | The platform's source of truth (`adhar/packages`, `adhar/environments`) |
| Grafana / Headlamp / Hubble | `https://<name>.adhar.localtest.me:8443` | Observability and cluster UIs |

Credentials:

```bash
adhar get secrets                 # all service credentials
adhar get secrets -p argocd       # just ArgoCD (user: admin)
# Gitea default: gitea_admin / r8sA8CPHD9!bt6d
```

Check status anytime:

```bash
adhar get status      # platform health
adhar get apps        # ArgoCD application states
```

## Your First Platform Change (GitOps)

The whole platform is data in Gitea. Enable an extra package to see the loop:

1. Open `adhar/environments` in Gitea and edit the ApplicationSet file
2. Find a package with `enabled: "false"` (say `harbor`) and flip it to `"true"`
3. Commit — ArgoCD picks up the change and deploys it; watch it appear in the ArgoCD UI
4. When it's `Healthy`, it's live at `https://harbor.adhar.localtest.me:8443`

> Mind local resources: a laptop can't run all 69 packages. Check `kubectl top nodes` before enabling heavyweights. The full customization surface (values, custom packages, environments) is in the [Customization Guide](CUSTOMIZATION.md).

## Deploy to a Cloud

The same flow targets AWS EKS, Azure AKS, GCP GKE, DigitalOcean DOKS, or Civo K3S — declare a config file and run `adhar up -f`:

```yaml
# adhar-config.yaml
clusterName: adhar-prod
provider: AWS_EKS
region: us-east-1
enableHAMode: true
nodePools:
  - name: system
    instanceType: t3.large
    count: 3
```

```bash
adhar up -f adhar-config.yaml
adhar get status
```

Per-provider credentials and details: [Provider Guide](PROVIDER_GUIDE.md). Before running real workloads, follow the [Production Guide](PRODUCTION.md).

## Tear Down

```bash
adhar down          # destroys the local platform completely
```

## Troubleshooting

| Problem | Fix |
|---------|-----|
| `adhar up` fails early | Is Docker running? `docker ps`. Port conflict? `adhar up --port 9443` |
| Service URL doesn't load | Platform still syncing — `adhar get apps`; the Gateway needs the app `Healthy` |
| Browser TLS warning | Expected locally (self-signed). Production uses cert-manager-issued certs |
| Pods `Pending` / node pressure | Local resource limits — disable packages you enabled, or give Docker more RAM |
| Cluster seems wedged | `adhar up --recreate` rebuilds from scratch — the platform is fully reconstructable |

More: [Production Guide §7 Troubleshooting](PRODUCTION.md#7-day-2-operations) · [GitHub Issues](https://github.com/adhar-io/adhar/issues) · [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)

## Next Steps

1. [User Guide](USER_GUIDE.md) — day-to-day usage and CLI reference
2. [Architecture](ARCHITECTURE.md) — how it all fits together
3. [Customization Guide](CUSTOMIZATION.md) — make the platform yours
4. [Production Guide](PRODUCTION.md) — take it to production
