# CLAUDE.md - Adhar Platform

## Project Overview

**Adhar** (Sanskrit: Foundation) is an open-source Internal Developer Platform (IDP) that provides standardized, production-grade cloud-native infrastructure with a single `adhar up` command. It integrates 50+ CNCF and open-source tools into a unified platform, supporting multi-cloud deployment across AWS EKS, Azure AKS, GCP GKE, DigitalOcean DOKS, Civo K3S, and local Kind clusters.

- **Version**: 0.1.0
- **Language**: Go 1.26+
- **License**: Apache 2.0
- **Organization**: Adharlabs Pte Ltd (adhar-io)
- **Status**: Active development (APIs may change)

### Vision

Become the definitive open foundation for cloud-native platform engineering. A single `adhar up` provisions complete, production-grade platforms in under 10 minutes — no infrastructure tickets, no security reviews, no integration projects.

### Value Proposition

- **Standardization as enablement, not constraint** — battle-tested patterns with 99 packages pre-configured
- **Self-service with guardrails** — instant provisioning within security/compliance boundaries
- **GitOps-native** — declarative infrastructure and application management via Git + ArgoCD
- **Multi-cloud freedom** — consistent experience across 6 cloud providers + local Kind
- **100% open source** — Apache 2.0, no vendor lock-in

## Architecture

### Design Pattern: Management Cluster First + GitOps-Driven

Adhar uses a two-phase deployment model:
1. **Bootstrap Phase** (imperative): Install Gateway API CRDs → Cilium CNI (Gateway data path) → Cilium Gateway → ArgoCD → Gitea (in this order)
2. **GitOps Phase** (declarative): Everything else managed through Git repos in Gitea + ArgoCD ApplicationSet reconciliation

### `adhar up` Sequence (Local Development)

1. Create Kind cluster with Cilium CNI disabled (Adhar manages CNI), ports 8080/8443 mapped; start the per-registry pull-through image caches (`adhar-registry-cache-*` on the `kind` network, volume `adhar-registry-cache`) the node's containerd mirrors to via certs.d — every later `adhar up` pulls all ~90 images from local disk
2. Install CRDs (AdharPlatform, GitRepository, CustomPackage)
3. Start controller-runtime manager with 5 controllers
4. Setup CoreDNS (custom rewrite rules for `*.adhar.localtest.me`)
5. Generate self-signed TLS certificate
6. Create `AdharPlatform` CR in `adhar-system` namespace
7. Controller reconciles:
   - Install Cilium (CNI + Hubble)
   - Install Gateway API CRDs, then the Cilium Gateway (GatewayClass + adhar-gateway); pin the Gateway Service node ports
   - Install ArgoCD (install + post-install HTTPRoute)
   - Install Gitea (install + post-install HTTPRoute)
8. Wait for Gitea API readiness (deployment + pod + HTTP probe)
9. Create the `adhar` Gitea org (teams `Owners`/`developers`/`viewers`, mapped from Keycloak groups via the auth source's `--group-team-map`) and the `environments` and `packages` repos under it (via API with `auto_init: true`); constants in `globals/project.go` (`GiteaPlatformOrg`, `GitOpsRepo*`)
10. Populate repos: the staged `platform/stack/{packages,environments,templates}` trees are committed on the HOST into throw-away repos and exported as git bundles (delta-packed by the host git, ~6 MB for the 62 MB packages tree), `kubectl cp` of the bundle → Gitea pod → `git fetch` + `read-tree` + commit on top of the repo history → push (no recompression in the CPU-limited pod; falls back to in-pod `git add` when the host has no git). The three repos seed concurrently and the Crossplane core Deployment is applied just before seeding so its startup overlaps it; the CLI checklist shows seeding as its own "GitOps repos" stage (~16 s, was ~60 s). Gitea runs with a 2-CPU limit because receiving that push at the chart default 200m was CFS-throttled
11. Apply ArgoCD auth (repo secrets + dedicated `gitea-argocd` service)
12. Apply `adhar-appset-local.yaml` (ApplicationSet wiring 104 elements over 99 packages; a `selector` on `enabled: "true"` deploys a curated local-DEVELOPMENT core (15): adhar-console, cnpg, keycloak, external-secrets, valkey, rustfs, tekton, buildpack, harbor, adhar-supply-chain (+adhar-supply-chain-kyverno), kyverno, metrics-server, kube-prometheus, headlamp. Production delivery, CI infrastructure and heavy JVM services stay wired but disabled — they are what made a local `adhar up` CPU-bound, the rest are wired but disabled)
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
| Gateway | Cilium Gateway API (GatewayClass `adhar`, Gateway `adhar-gateway`, NodePort 30080/30443) |
| IaC | Crossplane |
| Cloud SDKs | AWS SDK v2, Azure SDK, GCP API, DigitalOcean, Civo |

### Custom Resource Definitions (CRDs)

All CRDs are in API group `platform.adhar.io/v1alpha1`:

- **AdharPlatform** - Top-level platform resource; manages component lifecycle (Cilium, Gateway, ArgoCD, Gitea, Crossplane)
- **GitRepository** - Manages Git repos across providers (Gitea, GitHub, GitLab, Bitbucket); supports local, remote, and embedded sources
- **CustomPackage** - Deploys custom applications via ArgoCD Application/ApplicationSet

CRD types defined in `api/v1alpha1/`, generated with controller-gen, embedded YAML in `platform/controllers/resources/`.

### Five Kubernetes Controllers

Located in `platform/controllers/`:
- **AdharPlatform Controller** (`adharplatform/`) - Reconciles platform components with individual reconcilers per service (argocd.go, cilium.go, gitea.go, gateway.go, crossplane.go). Installs in deterministic order: Gateway API CRDs → Cilium → Gateway → ArgoCD → Gitea
- **GitRepository Controller** (`gitrepository/`) - Reconciles git repos across multiple providers
- **CustomPackage Controller** (`custompackage/`) - Manages ArgoCD app deployment via Gitea
- **DataPlane Controller** (`dataplane/`) - Registers and manages workload clusters (`mode: vcluster | adopt | composite`), mirrors their kubeconfig, wires the observability hub, joins the Cilium mesh, and rolls the fleet up onto `AdharPlatform.status.fleet`
- **Node Autoscaler** (`autoscaler/`) - Moves the worker count between `minWorkers` and `maxWorkers` on self-managed clusters; the upstream cluster-autoscaler has no provider for kubeadm-on-raw-compute. Adds a node when pods are Pending for capacity (including DigitalOcean's `exceed max volume count`), drains and removes the emptiest node when the cluster stays idle

### Provider Abstraction

`platform/providers/interface.go` defines a comprehensive provider interface covering:
- Cluster CRUD, node group management, VPC/networking, load balancers, storage
- Health checks, metrics, cost reporting, addon management

Implementations in `platform/providers/{aws,azure,gcp,digitalocean,civo,kind,custom}/`.
Factory pattern in `platform/providers/factory.go` for dynamic instantiation.

### Key Deployment Names (in adhar-system namespace)

- ArgoCD server: `argo-cd-argocd-server` (Helm release prefix `argo-cd`)
- Gitea: `gitea`
- Gateway Service: `cilium-gateway-adhar-gateway` (NodePort 30080/30443, served by Cilium Envoy)
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
│   │   │   ├── resources/         # Embedded YAML manifests (argocd/, cilium/, gitea/, gateway/, gateway-api/)
│   │   │   ├── controller.go      # Main reconciliation loop + GitOps repo setup
│   │   │   ├── helpers.go         # applyManifest (server-side apply with owner refs)
│   │   │   ├── argocd.go          # ArgoCD install + post-install
│   │   │   ├── cilium.go          # Cilium install (Gateway API enabled) + Hubble UI (port-forward)
│   │   │   ├── gitea.go           # Gitea install + HTTPRoute
│   │   │   └── gateway.go         # Gateway API CRDs + Cilium Gateway + node-port pinning
│   │   ├── gitrepository/         # Git repo reconciler (multi-provider)
│   │   ├── custompackage/         # Custom package reconciler
│   │   └── crd.go                 # CRD installation from embedded resources
│   ├── providers/                 # 7 cloud provider implementations
│   │   └── kind/                  # Local Kind cluster (cluster.go, config.go, coredns.go, tls.go)
│   ├── config/                    # Multi-layered config (global, provider, template, environment)
│   ├── stack/                     # GitOps content pushed to Gitea repos
│   │   ├── adhar-appset-local.yaml  # ArgoCD ApplicationSet (104 elements / 99 packages, enabled-gated; 15 enabled locally; adhar-appset-production.yaml: 77 enabled)
│   │   ├── argocd-auth.yaml         # ArgoCD repo secrets + gitea-argocd service
│   │   ├── packages/                # 99 packages, each with an adhar-package.yaml contract (ai/, application/, core/, data/, infrastructure/, observability/, security/)
│   │   └── environments/            # Environment configs (local, dev, staging, prod)
│   ├── k8s/                       # Kubernetes client, schema, provisioning, deserialization
│   ├── utils/                     # ArgoCD, Gitea, Git, URL, filesystem utilities
│   └── domain/                    # CoreDNS, TLS cert management
├── api/v1alpha1/                  # CRD type definitions (Go structs)
├── globals/                       # Global constants (project name, providers, namespaces, TLS)
├── hack/                          # Helm values and generation scripts for core components
├── tests/                         # E2E tests + provider-specific test configs
├── examples/                      # ONE example per platform object (37 XRs + 4 CRDs), indexed in examples/README.md and pinned by TestEveryPlatformObjectHasAnExample / TestExamplesMatchTheirSchema
│   ├── config.yaml                # the annotated master config template
│   ├── {aws,azure,gcp,digitalocean,civo}-config.yaml   # `adhar up -f`, one per cloud
│   └── provided-config.yaml       # clusterMode: provided — install onto an existing cluster
├── docs/                          # Documentation (architecture, guides, provider setup)
├── Makefile                       # Build system
└── go.mod / go.sum                # Go module dependencies
```

## Build & Development

### Prerequisites
- Go 1.26+
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
make test               # Unit tests with envtest (K8s version derived from k8s.io/api, currently 1.36)
make e2e                # End-to-end tests on Kind (15min timeout)
make lint               # golangci-lint v2 (latest)
make manifests          # Generate CRDs, RBAC, webhooks via controller-gen
make generate           # Generate DeepCopy methods for CRD types
```

### Build Flags
Version info injected via ldflags: `cmd/version.Version`, `cmd/version.GitCommit`, `cmd/version.BuildDate`

## Networking & Ports

### Default Local Development Ports
- **HTTPS**: `8443` (host) → `30443` (Gateway NodePort) → `443` (Cilium Envoy / HTTPS listener)
- **HTTP**: `8080` (host) → `30080` (Gateway NodePort) → `80` (Cilium Envoy / HTTP listener)
- **SSH**: `32222` (host) → `32222` (Gitea SSH)
- **Access URLs**: `https://argocd.adhar.localtest.me:8443`, `https://gitea.adhar.localtest.me:8443`

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

`examples/config.yaml` is the annotated template (one per provider too:
`examples/<provider>-config.yaml`). `adhar config create` writes a fresh
`./config.yaml` in the working directory; pass any of them with `-f`. Four layers:
1. **globalSettings** - Context, default host, ports (8443 HTTPS), HA mode, email
2. **providers** - Per-cloud credentials and infrastructure config (Kind, AWS, Azure, GCP, DO, Civo, Custom)
3. **environmentTemplates** - Reusable templates (prod-defaults, nonprod-defaults)
4. **environments** - Named instances (dev, test, staging, production) inheriting from templates

Validated against `config.schema.json` (JSON Schema draft-07).

## Testing

- **Unit/Integration**: Go testing + testify + envtest (Kubernetes 1.36)
- **E2E**: `make e2e` runs `tests/e2e/bootstrap` — a full `adhar up` → verify → `adhar down` cycle on Kind. ⚠️ It recreates the local `adhar` cluster (destroys existing state). `ADHAR_E2E_SKIP_UP=1` verifies an already-running platform without touching it; `ADHAR_E2E_KEEP=1` leaves the cluster up afterward
- **Test suite is fully green** (`make test`) — keep it that way. Unit tests use local git fixtures (no network); controller tests run under envtest with the CRD path `platform/controllers/adharplatform/resources/argocd/install.yaml` and metrics servers disabled (`BindAddress: "0"`)
- **Status conditions**: the AdharPlatform controller maintains standard `metav1.Condition`s (`ArgoCDReady`, `GatewayReady`, `GiteaReady`, `CrossplaneReady`, `GitOpsReady`, aggregate `Ready` carrying the last reconcile failure); `adhar get status` displays them plus per-package ArgoCD health

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

## Integrated Services (99 packages)

### Core (Bootstrap Phase - Embedded Manifests)
Cilium (with Gateway API), Cilium Gateway, ArgoCD, Gitea, Crossplane

### Crossplane Control Plane (Crossplane v2, namespaced model)

- Built on **Crossplane v2.3.1** core. XRDs use `apiextensions.crossplane.io/v2` with `scope: Namespaced` (no claims); Compositions stay `apiextensions.crossplane.io/v1`, Pipeline mode. Managed resources use namespaced `.m` API groups (`*.aws.m.upbound.io`, `kubernetes.m.crossplane.io`, `helm.m.crossplane.io`) and reference shared `ClusterProviderConfig`s. See `platform/controlplane/CONVENTIONS.md`.
- **37 XRDs** in `platform/controlplane/configuration/xrd/` — the concept hierarchy of ADR-0026 (Organisation → Team → Project → Application → Environment → Release, plus AgentWorkload), and Cluster, Database, Network, Logging, PlatformConfig, etc. Every object a hierarchy composition emits carries `adhar.io/organisation|team|project|application|environment` and `adhar.io/plane: workload`; a project composes its environments (an XR composing XRs), its Kargo project and its Argo CD AppProject; an application observes its project for namespaces and composes Argo CD Applications + Kargo Warehouse/Stages; a release composes a Kargo Promotion.
- **60 Compositions** in `platform/controlplane/configuration/compositions/` — multi-cloud (AWS/Azure/GCP via Upbound v2) + Kubernetes-native (provider-kubernetes/helm). Managed data services for teams (2026-10-05): **Search** (`search/opensearch-operator.yaml`) composes an `OpenSearchCluster` plus generated admin/monitoring credentials, ISM policy and a daily snapshot into RustFS through the platform's opensearch-k8s-operator; **Vector** (`vector/qdrant.yaml`) installs the pinned qdrant/qdrant chart via provider-helm and configures it — generated API keys reach Qdrant as `QDRANT__SERVICE__*` env vars from Secrets (the chart's `apiKey.valueFrom` resolves with Helm `lookup` at install time and would silently run keyless), declared collections are created by an idempotent Job named by a hash of the declaration. Both stamp the hierarchy labels; `datastores_test.go` pins them.
- **Self-service kinds are named after the THING, with no `Composite` prefix** (renamed 2026-10-07): `Database`, `Project`, `Bucket` — not `CompositeDatabase`. The prefix leaked Crossplane's implementation into every manifest a team writes, and `kubectl get compositedatabases` is not a name anyone would choose. 30 kinds, 228 files, plus the templates and Console repos; `TestNoXRDKindCarriesTheCompositePrefix` and `TestCompositionsAndXRDsAgreeOnKinds` hold the two halves together, because the failure mode of a partial rename is silent — a Composition whose `compositeTypeRef` names a kind no XRD defines is never selected, so the XR is created, accepted, and composes nothing. **The trade-off, accepted deliberately:** `Secret`, `Service`, `Cluster`, `Application`, `Project`, `Release` and `Restore` now collide on their SHORT name with core/v1, CNPG, Argo CD, Kargo, provider-helm and Velero. Different API groups, so nothing breaks functionally, but `kubectl get applications` is ambiguous — use `applications.platform.adhar.io` (or `adhar apps list`) when it matters.
- **Six resources named after what teams actually ask for** (2026-10-07), each composing onto a service the platform ALREADY runs rather than standing up a second copy: **Repository** (a Gitea repo + its Harbor project + optional Nexus hosted repos, in one idempotent Job — a team given only the git repo discovers the other two when CI first pushes, as `UNAUTHORIZED` from kpack and a 404 from npm), **Cache** (a team Valkey through the valkey-operator), **Topic** (a KafkaTopic on the shared `adhar-kafka`; it lands in the CLUSTER's namespace because Strimzi's topic operator watches only there, while the connection Secret lands with the workload), **Queue** (a RabbitMQ queue + exchange binding + dead-letter queue through the management API, since this platform runs the chart and not the cluster operator), **Bucket** (RustFS, with the versioning/quota/lifecycle a bucket is useless without — `deletionPolicy: Orphan` by default, because a bucket holds data) and **Table** (an Iceberg table created with Trino DDL, which is the one path Spark/Dagster/Metabase/OpenMetadata all agree about; `IF NOT EXISTS` makes it idempotent and it deliberately never ALTERs an existing table). All six verified offline with `crossplane render`.
- **5 Functions** — function-kcl, function-go-templating, function-patch-and-transform, function-auto-ready, function-python.
- **4 Operations** in `configuration/operations/` — CronOperation (daily backup, weekly secret rotation) + WatchOperation (ConfigMap drift); requires core `--enable-operations`.
- **ProviderConfigs** — shared `ClusterProviderConfig` per cloud family (AWS/Azure/GCP), plus provider-kubernetes & provider-helm (`ClusterProviderConfig`); DigitalOcean/Civo remain legacy `ProviderConfig`.
- **Package .xpkg** built via `crossplane xpkg build` (`make build-control-plane`) into the gitignored `platform/controlplane/dist/adhar-control-plane-<version>.xpkg`, versioned from the latest git tag (Makefile `VERSION`) and uploaded as a release asset by GoReleaser; the controller applies the embedded `configuration/` tree directly, so the file is not tracked in git.
- Install order: Crossplane core → wait for ready → XRDs → Compositions → Functions → ProviderConfigs → Operations

### GitOps Phase (99 packages / 104 ApplicationSet elements; 77 enabled in production, 15 in the local development core)

**Off by default in EVERY profile, opt-in:** `kubescape`, `n8n`, `beyla`, `llm-d`,
`redis`, `spark-operator`, `pyroscope`, `argo-workflows`. Most are scope decisions —
llm-d runs vLLM model servers, the heaviest workload here; `redis` is the alternative
to `valkey`, which is the primary key-value store every package wires to, and the one
package declaring redis (`libredb-studio`) marks it optional. kubescape
is the one with an operational reason rather than a scope one — it registers an
aggregated APIServer (`spdx.softwarecomposition.kubescape.io`), and while its pod
cannot run that API is unavailable, so every `kubectl` call in the cluster prints a
discovery warning to stderr. That contaminates anything capturing command output:
it is what made `adhar upgrade` read a Gitea 409 "already exists" as a failure and
refuse to push to an established cluster (2026-09-26).
Categories (count, packages carrying a contract): **ai** (5) · **application** (28) · **core** (6) · **data** (24) · **infrastructure** (2) · **observability** (17) · **security** (17). Six directories under `packages/` are empty or stubs and are NOT packages (`application/{tldraw,webstudio,pyroscope,adhar-templates}`, `backup/velero`, `data/dbt`) — `adhar-templates` holds the golden-path skeletons, and the real pyroscope/velero live under observability/ and core/.

**ai**: adhar-ai (agent runtime; images from the separate `adhar-io/adhar-ai` repo), agentgateway (the AI data plane — LLM routing by model name, federated MCP, JWT/CEL/guardrails/budgets; NOTE: an Allow rule's `matchExpressions` are OR'd, only one `jwtAuthentication` policy is honoured per target, and `mcp.tool.*` is populated only on policies attached to the MCP AgentgatewayBackend — see ai/agentgateway/manifests/security.yaml), **k8sgpt** (k8sgpt-operator; explains unhealthy objects with the platform model THROUGH agentgateway as its own ServiceAccount identity, one model, no tools; Result CRs readable by adhar-ai), llm-d (distributed inference: endpoint-picker router + agentgateway sidecar in front of vLLM, serves `local/*` models), vllm (bare vLLM, GPU profile). Opt-in; llm-d/adhar-ai/agentgateway on in production.
**Security**: cert-manager, external-secrets, keycloak, kyverno, kyverno-policies, **openbao** (production secrets backend; `vault` is disabled but the ESO store keeps that name), cosign, supply-chain-policies (audit + enforce variants), trivy, falco, tetragon, kubescape, policy-packs, policy-reporter, credential-rotation, vault
**Data**: **rustfs** (the PRIMARY S3 object store; S3 Tables = the Iceberg REST catalog inside it, warehouse `lakehouse`), cnpg, valkey, **valkey-admin** (Redis Commander behind Keycloak SSO over the shared `adhar-cache`; MIT — RedisInsight is SSPL), redis, **opensearch** (the opensearch-k8s-operator + an OpenSearchCluster CR since 2026-10-03 — generated admin credentials, operator-generated ServiceMonitor, an ISM policy and a daily snapshot into RustFS; the REST Service is `opensearch`, not the old chart's `opensearch-cluster-master`), kafka-operator, rabbitmq, **libredb-studio** (browser SQL IDE over every platform database, Keycloak SSO), airbyte, metabase, trino (`iceberg` catalog on RustFS), kubeflow, jupyterhub, dagster, prefect, spark-operator, open-metadata (ingests the lakehouse), **mlflow** (model registry), mongodb, **mariadb-operator** (the platform's MySQL-protocol database since 2026-10-03, with declarative databases/users/grants and a daily logical backup into RustFS), mysql-operator (DISABLED in every profile in its favour — nothing declared it as a dependency, and chaos-mesh and kubeflow bundle their own MySQL), kafka-ui, minio (disabled in every profile — opt-in alternative)
**Observability**: metrics-server, kube-prometheus, loki-stack, alloy, tempo, mimir, opencost, oncall, headlamp, hubble, beyla, faro, fluent-bit, pixie, pyroscope, victoria-metrics
**Application**: argo-workflows, argo-events, argo-rollout, harbor, kargo, tekton, supply-chain, **strapi** (headless CMS, CNPG-backed, Keycloak SSO; self-scaffolds on first boot, amd64-only image so off in the local profile), **preview-environments** (PR-labelled ephemeral environments), adhar-templates (four golden paths: microservice, frontend, data-pipeline, ml), scorecards, coder, n8n, penpot, plane, posthog, nexus, keda, dapr, k6, **litmus** (the chaos-engineering engine; chaos-mesh is disabled in its favour), buildpack (the one package outside `adhar-system` — see ADR-0011), external-dns, knative, open-function, baserow, tooljet, adhar-libraries
**Infrastructure**: crossplane, terraform
**Core**: adhar-console, velero, vcluster, sveltos, **karmada** (multi-cloud/multi-cluster control plane; replaced open-cluster-management on 2026-09-20 — the fleet hub runs `installMode: host`, member clusters get the agent from the DataPlane controller), Kamaji

## Important Implementation Notes

- **Object store**: RustFS is the platform's primary S3 store and publishes the `root-creds` credential every consumer reads; MinIO is disabled in every profile. RustFS S3 Tables is the Iceberg REST catalog (`http://rustfs.adhar-system.svc.cluster.local:9000/iceberg`, warehouse `lakehouse`, SigV4 with the S3 keys). Control-plane buckets (`Storage type: object`) are created there
- **Kyverno admits `adhar-system`** (`security/kyverno` sets `config.excludeKyvernoNamespace: false` and an explicit webhook `namespaceSelector`): the chart's default excludes its own release namespace, which on this platform is every package. Validation policies stay Audit or exclude `adhar-system` themselves; the exception exists so mutate rules that target platform pods (the kpack build-pod CA injection in `application/adhar-supply-chain`) actually fire
- **Nodes pull kpack-built images from the in-cluster Harbor** via a containerd certs.d host config: Kind mounts it at creation (`platform/providers/kind`), cloud nodes get it from the harbor package's `harbor-node-registry-trust` DaemonSet plus the kubeadm cloud-init's containerd settings (`use_local_image_pull = true` so the CRI plugin pulls itself and honours certs.d, and `config_path` on every registry table — the transfer service containerd 2.x delegates to ignored the hosts.toml mirror)
- **One namespace**: every package installs into `adhar-system` (ADR-0011), including `buildpack`/kpack since 2026-09-19. kpack and the sigstore policy-controller (`security/cosign`) both write `Secret/webhook-certs` under a name compiled into their binaries, so they cannot share a namespace: cosign is therefore `stability: beta` and disabled in both profiles, and image signatures are enforced by the `adhar-supply-chain-kyverno` ClusterPolicy `verify-supply-chain-images` against the same cosign key. **CoreDNS stays in `kube-system`**: relocating it was tried and reverted — it took cluster DNS down (a Service only selects pods in its own namespace, so the `kube-dns` ClusterIP lost every endpoint), then stalled the bootstrap, then made the Cilium stage 3m20s instead of 18s. `kube-system` cannot be emptied anyway (four static control-plane pods), and `local-path-storage` is Kind's own, created before `adhar-system` exists. Kargo `Project` namespaces (`adhar-environments`) and data-plane vclusters (`dp-<name>`) are the only other platform-created namespaces. Sharing one namespace means Kubernetes injects a `<NAME>_PORT` env var per Service — cosign's `Service/webhook` becomes `WEBHOOK_PORT=tcp://…`, which crashes pods that decode env strictly, so those set `enableServiceLinks: false` (see `platform/stack/packages/CONFLICTS.md`)
- **Provider modes**: exactly THREE, the same three words on every cloud — `clusterMode: compute` (kubeadm on raw instances, the code default when unset), `clusterMode: managed` (the cloud's hosted Kubernetes: EKS, AKS, GKE, DOKS, Civo k3s) and `clusterMode: provided` (the cluster ALREADY EXISTS; the platform installs onto it and owns none of its infrastructure). The service name is implied by the provider. The per-cloud spellings (`eks|aks|gke|doks|k3s`) and the `useManagedK8s` boolean were removed on 2026-10-07 and are now REJECTED rather than mapped: a stale `clusterMode: gke` that silently resolved to compute would build a kubeadm cluster for someone who asked for GKE, discovered only from the bill or a missing load balancer (`provider.NormalizeClusterMode` / `ParseClusterMode`, guarded by `platform/providers/clustermode_test.go`). Every shipped config sets `clusterMode: compute` EXCEPT Civo's, which sets `managed` (2026-10-08). Compute is the default because it is the path the platform is designed around, pays no control-plane charge, is the only mode the node autoscaler can grow (it provisions an instance and kubeadm-joins it), and keeps the node ours — which the kubelet flags and the containerd certs.d trust for the in-cluster Harbor both depend on. On AWS/Azure/GCP/DigitalOcean nothing is given up at the edge: compute mode installs the cloud-controller-manager (`cloudintegration.go`), so the Gateway still gets a real load-balancer address. Civo is the exception — its CCM resolves `CIVO_CLUSTER_ID` against the MANAGED Kubernetes API and nil-panics on a kubeadm cluster — so Civo ships `managed`, and Civo compute reaches the edge through the host-network Gateway instead (see the bullet below). The two modes that BUILD a cluster share the same operations (each provider's `managed.go` dispatches create/kubeconfig/node groups/scale/upgrade/health/delete by mode). Shared machinery: `platform/providers/kubeadm.go` (node prep, init/join, external-CCM kubelet flags, drain/retire, scale plan, upgrade), `cloudintegration.go` (idempotent CCM/CSI/StorageClass/taint-toleration steps run over SSH after the first joins, per-provider lists in `<cloud>/cloud_integration.go`), `managed.go` (mode parsing, version normalisation, exec-plugin kubeconfigs). Only DigitalOcean is live-verified; the rest is built and unit-tested (`docs/PROVIDER_GUIDE.md` §2.1)
- **`clusterMode: provided` installs onto a cluster the platform does not own, and that ownership boundary is the whole design.** No network, no instances, no managed control plane: `ProvidedProvider` (`platform/providers/provided.go`) replaces the cloud provider for cluster operations only, adopts the cluster through `kubeconfig:`/`kubeContext:` in the provider block (defaulting to `$KUBECONFIG` then `~/.kube/config`), and REFUSES every lifecycle call — delete, update, upgrade, scale, node groups, VPC/LB/volume. The configured cloud still supplies DNS, the cert-manager DNS-01 solver and the Crossplane credentials, so `type:` names the cloud that hosts the zone, not where the cluster runs; `type: custom` covers "no cloud at all". Three things had to be gated, each one a path that destroys or bills someone else's infrastructure: `adhar down` reads the mode BEFORE it locates a cluster (`environmentClusterMode` in `cmd/down/down.go`, because the lookup ends in `DeleteCluster`) and reports the cluster as left running; `--recreate` is refused in `ProvisionEnvironment`; and the node autoscaler refuses at `newProvider`, the single point every scaling action passes through. The kubeconfig is MINIFIED to the named context before anything sees it — it becomes `KUBECONFIG` for the whole bootstrap and is merged into the operator's default file, so carrying their other clusters along is a second chance to target the wrong one. The CNI is the hard constraint: the platform's data path IS Cilium with kubeProxyReplacement and the Gateway is a Cilium Gateway, so the preflight accepts a cluster already running Cilium (reused untouched — `ReconcileCilium` skips its own manifests when `adhar-cluster-spec`'s `clusterMode` is `provided` and a `cilium` DaemonSet exists outside the platform namespace) or a cluster with NO CNI (Cilium installs as usual), and REFUSES Calico/Flannel/the AWS VPC CNI/kindnet rather than replacing the networking of a cluster it was not given. It also fails on Cilium without `gatewayAPI.enabled=true` (no Gateway, no URL answers) and warns without a default StorageClass (~90 PVCs name no class, and nothing installs a provisioner in this mode). `region:` stops being required. See `examples/provided-config.yaml`; guards in `platform/providers/provided_test.go`, `provided_wiring_test.go`, `cmd/down/provided_cluster_test.go`, `platform/controllers/{autoscaler,adharplatform}/*provided*_test.go`
- **A cluster with no cloud load balancer serves the platform edge from its NODES, and that took six things agreeing.** Civo compute mode (and the `custom` BYO-hosts provider) has no cloud-controller-manager, so the Cilium Gateway Service never gets an address: the first live Civo bring-up produced a healthy platform whose every URL timed out. `provider.GatewayHostNetworkRequired` now selects Cilium's Gateway API **host-network mode** for exactly those clusters (not kind, which needs NodePort 30080/30443 for the host port-mapping; not `provided`, whose Cilium belongs to the operator). Inside Cilium, three edits must ALL land — `rewriteCiliumGatewayHostNetwork` patches `gateway-api-hostnetwork-enabled: "true"` in cilium-config, adds `NET_BIND_SERVICE` to the **cilium-envoy container only** (the same capability block appears five times in the rendered chart), and adds `--keep-cap-net-bind-service` to cilium-envoy-starter, which otherwise drops that capability before exec'ing Envoy. The earlier attempt set only the config key, which is why the Gateway reported Programmed with node addresses while nothing listened on 443. Outside Cilium: the Civo firewall opens 80/443 (`ensureComputeFirewall` now RECONCILES an existing firewall, or a cluster built before the rule existed would never get it); the CLI asks the provider for the nodes' PUBLIC IPs (`GatewayAddressProvider`) and records them in `adhar-cluster-spec`, because a node with no CCM has no ExternalIP and nothing in-cluster can discover them; the controller hands them to external-dns as `external-dns.alpha.kubernetes.io/target` on the Gateway, which overrides its status addresses (verified against external-dns v0.15.1 `source/gateway.go`); and CoreDNS points `*.<host>` at a selector-less shim Service whose EndpointSlice lists the nodes' INTERNAL addresses — pods must not be sent to the public IP, which NATs back to a node and does not hairpin. Guards: `platform/providers/gateway_exposure_test.go`, `platform/controllers/adharplatform/{cilium_hostnetwork_test.go,gateway_hostnetwork_test.go}`. Built and unit-tested; **not yet live-verified** on Civo
- **The GitOps sync tail is dominated by waiting, not work**: ArgoCD re-compares on `timeout.reconciliation` (30s + 10s jitter — at the former 300s+60s an app needing two passes idled up to 12 min, and until 2026-09-30 the DEFAULT non-HA rendering still sat at 120s+15s while only `install-ha.yaml` had been tuned, so the common path was the slow one; the default controller also sits at `kubectl.parallelism.limit: 4` against upstream's 20, which looks like an oversight and is not: raising it to 16 made a local `adhar up` FAIL at 6/17 apps with `context deadline exceeded` on the API server, because this manifest also installs ArgoCD on single-node Kind. Apply concurrency is the wrong knob for a phase that is dominated by waiting; raising it needs to be provider-aware first), the convergence driver re-compares apps that are OutOfSync with no sync in flight, every ExternalSecret reading `keycloak-clients` refreshes at 30s (it fails until Keycloak's wave-20 config Job writes that Secret), and oauth2-proxy Deployments cap `progressDeadlineSeconds` at 120s instead of the 600s default. The critical path is CNPG → keycloak-db → Keycloak → client Job → ~12 consumer ExternalSecrets → 9 oauth2-proxy rollouts
- **Single sign-on comes from Keycloak's session, NOT from a shared cookie**: each oauth2-proxy sets its OWN `--cookie-name=_adhar_sso_<client>` and no `--cookie-domain`, plus `--skip-provider-button=true` and `--whitelist-domain`. Sharing one cookie name on the parent domain with the shared `adhar-sso-cookie` secret was tried and made things worse: every proxy decrypted and reused the others' sessions, so Argo CD received a token minted for the `tekton` client and rejected it (`expected audience "argocd" got ["tekton"]`), and whichever app was opened last overwrote every other session. With per-app cookies a proxy that has none redirects to Keycloak, which already knows the user and returns a code immediately — no login screen, and each app gets a token with its own audience. The one `adhar-sso-cookie` Secret is kept only because one secret is easier to operate than 29. Guarded by `TestEveryOAuth2ProxyKeepsItsOwnSessionCookie`. Click-free coverage: proxied apps (Prometheus/RustFS/Tekton/…); Argo CD via its own bootstrap proxy with `--pass-authorization-header` (NOT `--set-authorization-header`, which only sets the response header) and anchored `--skip-auth-route` entries so the `argocd` CLI still works; Gitea via an HTTPRoute `RequestRedirect` of `GET /user/login` to `/user/oauth2/keycloak` (port MUST be stated or it drops to 443, and `?local=1` stays as break-glass); Grafana via `auth.generic_oauth.auto_login`. Harbor and Headlamp draw sign-in in the browser and cannot be intercepted — one click, no password
- **One CLI table and one icon vocabulary**: `cmd/helpers/table.go` sizes columns from content in DISPLAY cells (emoji and colour escapes included) and fits the terminal; `cmd/helpers/icons.go` defines single-cell glyphs plus `State*` helpers. Tab-separated rows and `%-30s` widths both shipped broken output — tabs snap to 8-column stops, and fixed widths overflowed the border so every row wrapped
- **The default StorageClass is node-local, not the cloud's block class.** A block volume occupies one of the VM's data-disk attach slots and that budget is small — 8 on an Azure `Standard_E4bds_v5`, 7 on a DigitalOcean droplet, 16 only from 8 vCPU up — while the enabled packages ask for ~90 PersistentVolumeClaims. A 1 + 4 cluster therefore offers ~32 slots, and a live Azure bring-up stalled at 39 of 75 apps with every worker at its ceiling, 27 claims Pending, and the unschedulable pods asking for 1.6 CPU cores between them. The symptom (`node(s) exceed max volume count`) reads like a compute shortage and is not; buying the slots would take ~64 vCPU for a platform that fits in 18. So `provider.StepNodeLocalStorageClass` installs `local-path-provisioner` and makes `adhar-local` the default on every self-managed cloud, `adhar-block` stays installed but demoted, and worker disks are 256 GiB because platform volumes now live on the node filesystem. A node-local volume does not survive its node — durability is CNPG replication plus barman backups into the object store, and anything needing a real block device (or a volume bigger than a node's filesystem, like RustFS's 150 GiB) names `adhar-block` explicitly
- **A scaled worker takes its node group's machine size, not the provider-level one.** `p.config.VMSize` describes the CONTROL PLANE, which is routinely smaller: reading it in `ScaleNodeGroup` gave a cluster with 4-vCPU `Standard_E4bds_v5` workers a pair of 2-vCPU `Standard_E2bds_v5` ones, so each autoscaled node arrived with half the CPU and half the disk slots the decision had counted on, and the preflight's quota budget no longer described what was built. AWS already read it from existing members; Azure now does too (`pickWorkerVMSize`)
- **Probe timeouts must tolerate a loaded node**: no package may ship a probe with `timeoutSeconds: 1` (enforced by `TestNoProbeShipsAOneSecondTimeout`). Harbor's database shipped the upstream default and its health check measures 1.675 s, so kubelet killed a healthy database 29 times and Harbor's whole app cascaded into crash loops
- **`adhar up` waits for the apps, not just the foundation**: after the ApplicationSet is applied the controller drives Applications to Synced + Healthy — hard-refreshing the ones ArgoCD parks at `Unknown`/ComparisonError (a first-comparison DNS miss on `gitea-http` left 19 of 32 there until the 5-minute resync) — and reports `n/m apps Synced + Healthy`. Bounded by `--apps-timeout` (default 15m); ArgoCD keeps converging after it exits
- **Kubernetes version precedence**: explicit `adhar up --kube-version` (every provider, not just Kind) → the environment's `kubeVersion`/`version` → `globals.DefaultKubernetesVersion` (v1.37.0). A node added later by `adhar cluster scale` or the node autoscaler takes its version from the running control plane, so a scaled cluster cannot skew
- **A wave-stuck ArgoCD sync replays the revision it started with.** While an Application sits at `waiting for healthy state of <resource>`, pushing a fix to Gitea changes nothing and `kubectl` edits are reverted. `operation: null` does not clear it — terminate via the API (`DELETE /api/v1/applications/<app>/operation` with a `content-type: application/json` header), then hard-refresh
- **The paved-road CI (`app-ci`, supply-chain package) is a gated graph, not a script**: clone → buildpacks build → {test-auto, gitleaks, trivy source scan, trivy image scan, syft SBOM} in parallel → cosign sign (waits for EVERY gate) → SBOM attestation → cosign verify → GitOps writeback, `finally` a summary. Tests run on a private copy of the checkout (they must not write into the shared workspace), scanner reports go to /tmp (never into the image), only the SBOM — produced after the build — lives in the workspace. Scaffolders commit a skeleton as ONE commit (Gitea batch contents API): file-per-commit fired the push webhook once per file and started 14 parallel runs for one new service
- **The Console's templates come from a Gitea repo that BOOTSTRAP mirrors, not a package.** `GET /api/templates` and `adhar apps templates` both enumerate `adhar/adhar-templates/templates/<id>/template.yaml` in Gitea and render nothing when that repo is absent — "No templates available" on the Create New page is almost always a missing repo, not a broken page. `mirrorGiteaRepository` (adhar-platform controller) creates it as a Gitea **pull mirror** of github.com/adhar-io/adhar-templates, so the offered templates track upstream without a platform release. Bootstrap deliberately does NOT seed it: it once seeded a thinner `templates` repo from `platform/stack/templates/` and the Console and CLI then disagreed about which collection was authoritative. Best effort — no egress means no templates, a missing feature rather than a broken cluster. The mirror lived in the `adhar-libraries` package until 2026-09-30, which broke it two ways: that package is `enabled: false` locally because its release builds cost ~1 CPU core each spent precisely while CNPG and Keycloak boot, and its Jobs waited 20 minutes for a `nexus-credentials` Secret the templates mirror never needed (Tekton's CEL filters match only `adhar-ui`/`adhar-kit`, so nothing releases templates). Those two stay force-pushed COPIES rather than mirrors because they are meant to be edited in-cluster, which a pull mirror forbids
- **The GitOps phase is bound by one identity chain, not by app count.** CNPG → the `keycloak-db` Postgres Cluster → Keycloak's JVM → its wave-20 config Job writes `keycloak-clients` → wave 25 → wave 40 → then every consumer's ExternalSecret and oauth2-proxy rollout. Eight of the fifteen local apps read that Secret and cannot go Healthy until it exists, so they finish early and idle; the rest of the platform is waiting on a Postgres cluster and a JVM boot. Every hop also pays fixed polling latency — ArgoCD re-compares on `timeout.reconciliation` (30s + 10s jitter) and each consumer ExternalSecret retries on its `refreshInterval` — which is why the `keycloak-clients` consumers sit at 10s rather than 30s. Adding work that competes for CPU during this window (a JVM, a Tekton build) lengthens the chain itself, which is why `nexus` and `adhar-libraries` are off locally
- **Civo now gets a publicly trusted certificate, and it took four things agreeing.** cert-manager has no native Civo DNS-01 solver, so a Civo platform used to publish DNS records through external-dns and then serve a SELF-SIGNED certificate on every URL. The fix is a webhook solver (`packages/security/cert-manager/manifests/civo-dns-webhook.yaml.tmpl`, rendered from okteto/cert-manager-webhook-civo 0.5.4 and applied ONLY when `dnsProvider: civo` — an aggregated APIService that cannot serve makes every `kubectl api-resources` in the cluster noisy, the same trap kubescape set), a `civo` branch in `cluster-issuers.yaml.tmpl`, **`civo` in `acmeDNS01Providers`** — without that one line `ACMEIssuer()` returns the self-signed issuer and the solver is inert — and a Civo entry in the preflight's nameserver marks. It is a third-party component in the certificate path, so the image tag is pinned, the chart's `helm test` Pod is dropped, and its secret-reader Role is narrowed from "every secret in adhar-system" to `adhar-dns-provider`
- **A pinned upstream manifest URL is a time bomb; `ADHAR_NET_TESTS=1 go test ./platform/providers/ -run TestPinnedUpstreamRefs` is the detonator check.** The first live Civo bring-up failed AFTER creating three billable instances because both of its pins had rotted: the CCM was pinned `v0.0.24` when upstream tags are `v0.1.x` AND the manifest had moved to `doc/yaml/ccm-install.yaml`, and the CSI was pinned `v0.2.0` when upstream is `v0.10.x`. A URL constant compiles perfectly and is only resolved over SSH on a real node, a quarter of an hour into a paid run. The newer Civo CSI also declares a `VolumeSnapshotClass` without shipping its CRDs, so external-snapshotter's three CRDs are applied first — DigitalOcean's integration has always had the equivalent step. Bump a pin by FETCHING the URL and diffing the resource names the steps wait on (`civo-volume`, `civo-csi-node`), never by assuming the path survived
- **Every package needs an `adhar-package.yaml`** marketplace contract; `hack/validate-packages.sh` enforces it and CI fails a PR that adds a package without one. `hack/gen-package-contracts.sh` generates missing ones
- **Contracts carry verification evidence (ADR-0014)**: the `verification:` block (`verified` / `known-broken` / `unverified`, profile, Adhar + Kubernetes version, date, reason) is written by `adhar stack verify --write` from live Argo CD health — never by hand — and an `unverified` run never downgrades recorded evidence. Elements that point into a package (`ai/vllm/manifests/cpu`, the enforce policy pack) resolve to that package's contract
- Core packages install in deterministic order: Gateway API CRDs → Cilium → Cilium Gateway → ArgoCD → Gitea
- The Cilium Gateway must be Programmed before HTTPRoutes resolve; the controller pins the generated Service node ports (30080/30443) so Kind host port-mapping works
- The `StackDir` field on `AdharPlatformReconciler` holds the absolute path to `platform/stack/`
- Repository population uses `kubectl cp` + `kubectl exec` with `sh -c` for proper shell expansion
- Gitea repos created with `auto_init: true` and `default_branch: main`
- Git push uses `git push -f origin "$branch:main"` to handle any branch naming
- The `platform/controlplane/` contains a pre-built Crossplane `.xpkg` package
- Resources in `platform/controllers/adharplatform/resources/` are embedded via `//go:embed`
- Git branch: `main` is the primary branch; `master` may exist locally
