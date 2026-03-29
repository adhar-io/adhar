# CLAUDE.md - Adhar Platform

## Project Overview

**Adhar** (Sanskrit: Foundation) is an open-source Internal Developer Platform (IDP) that provides standardized, production-grade cloud-native infrastructure with a single `adhar up` command. It integrates 50+ CNCF and open-source tools into a unified platform, supporting multi-cloud deployment across AWS EKS, Azure AKS, GCP GKE, DigitalOcean DOKS, Civo K3S, and local Kind clusters.

- **Version**: 0.3.8
- **Language**: Go 1.23+
- **License**: Apache 2.0
- **Organization**: Adharlabs Pte Ltd (adhar-io)
- **Status**: Active development (APIs may change)

### Vision

Become the definitive open foundation for cloud-native platform engineering. A single `adhar up` provisions complete, production-grade platforms in under 10 minutes — no infrastructure tickets, no security reviews, no integration projects.

### Value Proposition

- **Standardization as enablement, not constraint** — battle-tested patterns with 50+ services pre-configured
- **Self-service with guardrails** — instant provisioning within security/compliance boundaries
- **GitOps-native** — declarative infrastructure and application management via Git + ArgoCD
- **Multi-cloud freedom** — consistent experience across 6 cloud providers + local Kind
- **100% open source** — Apache 2.0, no vendor lock-in

## Architecture

### Design Pattern: Management Cluster First + GitOps-Driven

Adhar uses a two-phase deployment model:
1. **Bootstrap Phase** (imperative): Install Cilium CNI → Nginx Ingress → ArgoCD → Gitea (in this order)
2. **GitOps Phase** (declarative): Everything else managed through Git repos in Gitea + ArgoCD ApplicationSet reconciliation

### `adhar up` Sequence (Local Development)

1. Create Kind cluster with Cilium CNI disabled (Adhar manages CNI), ports 8080/8443 mapped
2. Install CRDs (AdharPlatform, GitRepository, CustomPackage)
3. Start controller-runtime manager with 3 controllers
4. Setup CoreDNS (custom rewrite rules for `*.adhar.localtest.me`)
5. Generate self-signed TLS certificate
6. Create `AdharPlatform` CR in `adhar-system` namespace
7. Controller reconciles:
   - Install Cilium (CNI + Hubble)
   - Install Nginx Ingress (wait for deployment ready — webhook must be accessible)
   - Install ArgoCD (install + post-install with Ingress)
   - Install Gitea (install + post-install with Ingress)
8. Wait for Gitea API readiness (deployment + pod + HTTP probe)
9. Create `environments` and `packages` repos in Gitea (via API with `auto_init: true`)
10. Populate repos: `kubectl cp` from `platform/stack/{packages,environments}` → Gitea pod → git push
11. Apply ArgoCD auth (repo secrets + dedicated `gitea-argocd` service)
12. Apply `adhar-appset-local.yaml` (ApplicationSet with 28+ packages)
13. ArgoCD syncs all applications from Gitea repos
14. Controller detects platform is deployed → graceful shutdown → success message

### Core Components

| Layer | Technology |
|-------|-----------|
| CLI Framework | Cobra + Viper + Bubbletea (TUI) + Lipgloss (styling) |
| Kubernetes | client-go, controller-runtime (KubeBuilder v4) |
| CNI | Cilium (eBPF-based, replaces kube-proxy) |
| GitOps | ArgoCD (ApplicationSet for all platform packages) |
| Git Server | Gitea (self-hosted, in-cluster) |
| Ingress | Nginx Ingress Controller (NodePort 30080/30443) |
| IaC | Crossplane |
| Cloud SDKs | AWS SDK v2, Azure SDK, GCP API, DigitalOcean, Civo |

### Custom Resource Definitions (CRDs)

All CRDs are in API group `platform.adhar.io/v1alpha1`:

- **AdharPlatform** - Top-level platform resource; manages component lifecycle (Cilium, ArgoCD, Gitea, Nginx, Crossplane)
- **GitRepository** - Manages Git repos across providers (Gitea, GitHub, GitLab, Bitbucket); supports local, remote, and embedded sources
- **CustomPackage** - Deploys custom applications via ArgoCD Application/ApplicationSet

CRD types defined in `api/v1alpha1/`, generated with controller-gen, embedded YAML in `platform/controllers/resources/`.

### Three Kubernetes Controllers

Located in `platform/controllers/`:
- **AdharPlatform Controller** (`adharplatform/`) - Reconciles platform components with individual reconcilers per service (argocd.go, cilium.go, gitea.go, nginx.go, crossplane.go). Installs in deterministic order: Cilium → Nginx → ArgoCD → Gitea
- **GitRepository Controller** (`gitrepository/`) - Reconciles git repos across multiple providers
- **CustomPackage Controller** (`custompackage/`) - Manages ArgoCD app deployment via Gitea

### Provider Abstraction

`platform/providers/interface.go` defines a comprehensive provider interface covering:
- Cluster CRUD, node group management, VPC/networking, load balancers, storage
- Health checks, metrics, cost reporting, addon management

Implementations in `platform/providers/{aws,azure,gcp,digitalocean,civo,kind,custom}/`.
Factory pattern in `platform/providers/factory.go` for dynamic instantiation.

### Key Deployment Names (in adhar-system namespace)

- ArgoCD server: `argo-cd-argocd-server` (Helm release prefix `argo-cd`)
- Gitea: `gitea`
- Nginx: `ingress-nginx-controller`
- Gitea service for ArgoCD: `gitea-argocd` (dedicated ClusterIP)
- Gitea HTTP service: `gitea-http` (ClusterIP, port 3000)

### Default Credentials

- Gitea admin: `gitea_admin` / `r8sA8CPHD9!bt6d`
- ArgoCD admin: `admin` / auto-generated (retrieve via `adhar get secrets -p argocd`)

## Directory Structure

```
adhar/
├── cmd/                           # CLI commands (27 subcommands via Cobra)
│   ├── main.go                    # Entry point
│   ├── root.go                    # Root command, banner, help
│   ├── up/                        # `adhar up` - platform creation
│   ├── down/                      # `adhar down` - platform teardown
│   ├── get/                       # Resource display (apps, secrets, status)
│   ├── cluster/                   # Cluster operations (create, list, delete, debug)
│   ├── apps/                      # Application lifecycle
│   ├── helpers/                   # Shared CLI utilities (progress, styles, validation)
│   └── version/                   # Version info (set via ldflags)
├── platform/                      # Core platform logic
│   ├── controllers/               # 3 Kubernetes controllers
│   │   ├── adharplatform/         # Platform reconciler + per-component reconcilers
│   │   │   ├── resources/         # Embedded YAML manifests (argocd/, cilium/, gitea/, nginx/)
│   │   │   ├── controller.go      # Main reconciliation loop + GitOps repo setup
│   │   │   ├── helpers.go         # applyManifest (server-side apply with owner refs)
│   │   │   ├── argocd.go          # ArgoCD install + post-install
│   │   │   ├── cilium.go          # Cilium install + Hubble UI ingress
│   │   │   ├── gitea.go           # Gitea install + ingress
│   │   │   └── nginx.go           # Nginx install + readiness wait
│   │   ├── gitrepository/         # Git repo reconciler (multi-provider)
│   │   ├── custompackage/         # Custom package reconciler
│   │   └── crd.go                 # CRD installation from embedded resources
│   ├── providers/                 # 7 cloud provider implementations
│   │   └── kind/                  # Local Kind cluster (cluster.go, config.go, coredns.go, tls.go)
│   ├── config/                    # Multi-layered config (global, provider, template, environment)
│   ├── stack/                     # GitOps content pushed to Gitea repos
│   │   ├── adhar-appset-local.yaml  # ArgoCD ApplicationSet (28+ packages)
│   │   ├── argocd-auth.yaml         # ArgoCD repo secrets + gitea-argocd service
│   │   ├── packages/                # 87 package directories (security/, data/, observability/, etc.)
│   │   └── environments/            # Environment configs (local, dev, staging, prod)
│   ├── k8s/                       # Kubernetes client, schema, provisioning, deserialization
│   ├── utils/                     # ArgoCD, Gitea, Git, URL, filesystem utilities
│   └── domain/                    # CoreDNS, TLS cert management
├── api/v1alpha1/                  # CRD type definitions (Go structs)
├── globals/                       # Global constants (project name, providers, namespaces, TLS)
├── hack/                          # Helm values and generation scripts for core components
├── tests/                         # E2E tests + provider-specific test configs
├── examples/                      # Example YAML resources (v1alpha1)
├── docs/                          # Documentation (architecture, guides, provider setup)
├── config.yaml                    # Main platform configuration
├── Makefile                       # Build system
└── go.mod / go.sum                # Go module dependencies
```

## Build & Development

### Prerequisites
- Go 1.23+
- Docker v20.10+
- kubectl v1.24+
- Make

### Quick Build & Run
```bash
go build -o ./adhar ./cmd/       # Build binary
./adhar up                        # Create local Kind cluster with full platform
./adhar up --port 9443            # Use custom port (avoids conflicts)
./adhar up --recreate             # Delete existing cluster and recreate
./adhar up --dry-run              # Preview what would be created
./adhar get secrets               # Get service passwords
```

### Key Makefile Targets
```bash
make build              # Build binary with version metadata
make test               # Unit tests with envtest (K8s 1.31)
make e2e                # End-to-end tests on Kind (15min timeout)
make lint               # golangci-lint v1.63.4
make manifests          # Generate CRDs, RBAC, webhooks via controller-gen
make generate           # Generate DeepCopy methods for CRD types
```

### Build Flags
Version info injected via ldflags: `cmd/version.Version`, `cmd/version.GitCommit`, `cmd/version.BuildDate`

## Networking & Ports

### Default Local Development Ports
- **HTTPS**: `8443` (host) → `30443` (Nginx NodePort) → `443` (Nginx container)
- **HTTP**: `8080` (host) → `30080` (Nginx NodePort) → `80` (Nginx container)
- **SSH**: `32222` (host) → `32222` (Gitea SSH)
- **Access URLs**: `https://adhar.localtest.me:8443/argocd`, `https://adhar.localtest.me:8443/gitea`

### Port Customization
Use `--port` flag to set HTTPS port: `adhar up --port 9443` (HTTP auto-derives as port-363, e.g., 9080)

### Kind Config Template
Located at `platform/providers/kind/resources/kind.yaml.tmpl`. Disables default CNI and kube-proxy (Cilium replaces both).

## Key Constants (globals/project.go)

- Default cluster name: `adhar`
- Default namespace: `adhar-system`
- Default hostname: `adhar.localtest.me`
- Default HTTPS port: `8443`
- Supported cloud providers: GKE, AWS, DO, Azure, Civo, Kind
- Supported git providers: Gitea, GitLab, GitHub, Bitbucket

## Configuration Model

`config.yaml` has four layers:
1. **globalSettings** - Context, default host, ports (8443 HTTPS), HA mode, email
2. **providers** - Per-cloud credentials and infrastructure config (Kind, AWS, Azure, GCP, DO, Civo, Custom)
3. **environmentTemplates** - Reusable templates (prod-defaults, nonprod-defaults)
4. **environments** - Named instances (dev, test, staging, production) inheriting from templates

Validated against `config.schema.json` (JSON Schema draft-07).

## Testing

- **Unit/Integration**: Go testing + Ginkgo v2 + Gomega + envtest (Kubernetes 1.31)
- **E2E**: Kind cluster-based, Ginkgo v2
- **Known failing tests**: `TestGetRawInstallResources` (expects `hack/argo-cd` dir), `TestPatchPasswordSecret` (gitea_test.go) — pre-existing issues

## Release Process

- **Versioning**: Semantic (major/minor/patch + pre-release)
- **Distribution**: GoReleaser → Homebrew tap (adhar-io/homebrew-tap), archives (tar.gz/zip), checksums
- **Platforms**: Linux, macOS, Windows (amd64, arm64)
- **Container**: distroless/static:nonroot (non-root user 65532)

## Code Style & Conventions

- DCO (Developer Certificate of Origin) required for contributions
- Controller-runtime patterns for Kubernetes controllers
- Cobra command pattern: one package per subcommand in `cmd/`
- Provider interface pattern for cloud abstraction
- Labels: `adhar.io/*` prefix for platform-managed resources
- Server-Side Apply with `ForceOwnership` for all manifest application
- 20+ linters enabled via golangci-lint

## Integrated Services (50+)

### Core (Bootstrap Phase - Embedded Manifests)
Cilium, Nginx Ingress, ArgoCD, Gitea

### GitOps Phase (28+ packages via ApplicationSet)
**Security**: cert-manager, external-secrets, kyverno, kyverno-policies, keycloak
**Data**: cnpg, jupyterhub, minio, redis, spark-operator
**Observability**: metrics-server, kube-prometheus, loki, alloy, tempo, mimir, opencost, oncall, headlamp
**Application**: argo-workflows, harbor, kargo, k6, dapr, keda
**Infrastructure**: crossplane, terraform
**Core**: adhar-console, velero

## Important Implementation Notes

- Core packages install in deterministic order: Cilium → Nginx (wait for ready) → ArgoCD → Gitea
- Nginx readiness wait is critical — admission webhook must be accessible before Ingress resources
- The `StackDir` field on `AdharPlatformReconciler` holds the absolute path to `platform/stack/`
- Repository population uses `kubectl cp` + `kubectl exec` with `sh -c` for proper shell expansion
- Gitea repos created with `auto_init: true` and `default_branch: main`
- Git push uses `git push -f origin "$branch:main"` to handle any branch naming
- The `platform/controlplane/` contains a pre-built Crossplane `.xpkg` package
- Resources in `platform/controllers/adharplatform/resources/` are embedded via `//go:embed`
- Git branch: `main` is the primary branch; `master` may exist locally
