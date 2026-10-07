# Adhar Platform Examples

Working examples of the self-service resources the platform serves. Every
`platform.adhar.io/v1alpha1` object here is a Crossplane v2 composite (XR) whose
contract lives in `platform/controlplane/configuration/xrd/`; they apply as-is to
any cluster created by `adhar up` (`kubectl explain database.spec` shows
the full schema).

Kinds are named after the thing: `Database`, not `CompositeDatabase` (renamed
2026-10-07 — the old names are rejected, not aliased). Several of them therefore
share a SHORT name with another API group (`Secret` and `Service` with core,
`Cluster` with CNPG, `Application` with Argo CD, `Project` with Kargo, `Release`
with provider-helm, `Restore` with Velero). Nothing breaks — the groups are
different — but `kubectl get applications` is ambiguous; qualify it as
`applications.platform.adhar.io` when it matters.

Two things are true of all of them:

- **They are namespaced.** Create them in your team's namespace; the
  ResourceQuota/tenant-quota guardrails are enforced there.
- **The provider is chosen by label**, under `spec.crossplane.compositionSelector`.
  `provider: local` is the in-cluster implementation (CNPG, RustFS, Tekton,
  Argo CD); on a cloud cluster set it to `aws`/`azure`/`gcp` and the same request
  produces the managed service instead.

## Files

One example per object the platform defines — 37 Crossplane composites and the
four CRDs its own controllers reconcile. `TestEveryPlatformObjectHasAnExample`
fails if an object loses its example, and `TestExamplesMatchTheirSchema` checks
every field here against the real schema, because an unknown field is PRUNED by
the API server rather than rejected: a misspelled example applies cleanly and
configures nothing.

### Tenancy (the hierarchy of ADR-0026)

| File | Kind | What you get |
|------|------|--------------|
| `organisation.yaml` | Organisation | The tenant root: home namespace, Keycloak group, Gitea organisation |
| `team.yaml` | Team | A team — the onboarding and isolation unit; its Keycloak and Gitea groups |
| `project.yaml` | Project | A team project: guard-railed namespace, RBAC bound to the team's Keycloak group, Gitea repo, AppProject, Kargo project |
| `environment.yaml` | Environment | A tiered environment (dev/test/prod) with quotas and default-deny network policy |
| `application.yaml` | Application | An Argo CD-managed app with its Kargo Warehouse and Stages |
| `release.yaml` | Release | One promotion: move a specific piece of freight into one environment |

### Backing services

| File | Kind | What you get |
|------|------|--------------|
| `database.yaml` | Database | PostgreSQL (CNPG locally, RDS/CloudSQL/Azure SQL on a cloud) with WAL archiving + daily backup |
| `storage.yaml` | Storage | An S3 bucket on RustFS with the credential delivered as a Secret |
| `search.yaml` | Search | An OpenSearch cluster with generated credentials, an ISM policy and nightly snapshots |
| `vector.yaml` | Vector | A Qdrant vector store with generated API keys and declared collections |
| `messaging.yaml` | Messaging | A Strimzi Kafka cluster and its topics |
| `cache.yaml` | Cache | A team Valkey through the valkey-operator, with its exporter and ServiceMonitor |
| `bucket.yaml` | Bucket | An S3 bucket on RustFS, with versioning, quota and a lifecycle rule |
| `table.yaml` | Table | An Iceberg table in the lakehouse, created with Trino DDL |
| `topic.yaml` | Topic | A Kafka topic on the SHARED Strimzi cluster |
| `queue.yaml` | Queue | A RabbitMQ queue with its exchange binding and dead-letter queue |
| `secret.yaml` | Secret | A secret sourced from OpenBao through External Secrets |
| `secret-rotation.yaml` | SecretRotation | Scheduled rotation, with the consumers restarted afterwards |

### Delivery and runtime

| File | Kind | What you get |
|------|------|--------------|
| `repository.yaml` | Repository | A Gitea repository, its Harbor project and its Nexus package repositories — one request |
| `pipeline.yaml` | Pipeline | A Tekton CI pipeline (clone → build → scan → sign → deploy) |
| `gitops.yaml` | GitOps | An Argo CD AppProject and the ApplicationSets that fill it |
| `service.yaml` | Service | A Service with its ServiceMonitor |
| `scale.yaml` | Scale | Horizontal pod autoscaling for a workload |
| `webhook.yaml` | Webhook | Platform events delivered to an endpoint of your own |
| `agent-workload.yaml` | AgentWorkload | A governed AI agent: its own namespace, quota, egress policy, and an allowlist of models and MCP tools |

### Clusters and infrastructure

| File | Kind | What you get |
|------|------|--------------|
| `cluster.yaml` | Cluster | A real cloud cluster (EKS/AKS/GKE/DOKS/Civo k3s) |
| `vcluster.yaml` | Cluster | A virtual workload cluster on this platform's own nodes |
| `network.yaml` | Network | A cloud VPC/VNet with its subnets and flow logs |
| `dataplane.yaml` | DataPlane | A workload cluster joined to this platform's fleet (`vcluster`, `composite` or `adopt`) |

### Observability, cost and governance

| File | Kind | What you get |
|------|------|--------------|
| `logging.yaml` | Logging | A Loki stack with its own retention |
| `metrics.yaml` | Metrics | Scrape targets and alert rules for things that are not pods |
| `trace.yaml` | Trace | A trace pipeline with its own backend and sampling |
| `health.yaml` | Health | Synthetic (black-box) checks through the platform edge |
| `cost-tracker.yaml` | CostTracker | Cost attribution by namespace, with budget alerts |
| `compliance-policy.yaml` | CompliancePolicy | Kyverno guardrails, in audit or enforce mode, with exceptions |
| `backup-policy.yaml` | BackupPolicy | A Velero schedule with retention, encryption and restore verification |
| `restore.yaml` | Restore | A restore from one of those backups |
| `authstack.yaml` | AuthStack | A Keycloak realm of your own: clients, groups, federation, password and session policy |
| `platform-config.yaml` | PlatformConfig | Platform-wide settings published into the cluster for workloads to read |

### The platform's own objects

These are reconciled by Adhar's controllers, not by Crossplane. `adhar up`
creates the first two; the others are for packages you deliver yourself.

| File | Kind | What you get |
|------|------|--------------|
| `adharplatform.yaml` | AdharPlatform | The platform: component lifecycle, node-autoscaler limits, Cluster Mesh identity, the host everything is rendered from |
| `custompackage.yaml` | CustomPackage | Your own package, delivered the way the platform delivers its own (replicated into Gitea first) |
| `gitrepository.yaml` | GitRepository | A repository the platform creates and populates on the git server |
| `preview-environments-appset.yaml` | ApplicationSet | Per-PR preview environments (superseded by the `adhar-preview-environments` package — see the file header) |

### Not objects: CLI input

| File | What it is |
|------|------------|
| `config.yaml` | the annotated master template: every provider, commented out, plus the four config layers |
| `aws-config.yaml`, `azure-config.yaml`, `gcp-config.yaml`, `digitalocean-config.yaml`, `civo-config.yaml` | `adhar up -f` cluster configuration, one per cloud. Every value is a placeholder — change `defaultHost`, `email` and the region before the first run. Credentials are never read from these files. |
| `provided-config.yaml` | `clusterMode: provided` — install the platform onto a cluster you ALREADY have. Creates no infrastructure, and `adhar down` deletes nothing |
| `auth-config.yaml` | `adhar auth configure` input (Keycloak realm/roles) |

## Walk-through: a team's first service

```bash
# 1. A project. Its namespace, quota, RBAC and repository come from this one object.
kubectl apply -f project.yaml

# 2. Backing services in that namespace.
kubectl apply -f database.yaml
kubectl apply -f storage.yaml
kubectl apply -f secret.yaml

# 3. CI for the repository, then the application itself.
kubectl apply -f pipeline.yaml
kubectl apply -f application.yaml

# Watch them become Ready (the composed resources are listed under each XR).
kubectl -n team-payments get project,database,storage,application
kubectl -n team-payments describe database payments-db
```

The CLI produces the same objects — `adhar project create`, `adhar database
create`, `adhar bucket create`, `adhar application deploy`, `adhar cluster
create` — so anything shown here can be scripted either way.

## Multi-environment

Environments are objects too (`adhar environment create` makes one); promotion
between them is a Kargo Stage, driven from the Console's Environments page or
`kargo promote` (the Kargo CLI):

```bash
sed 's/payments-staging/payments-prod/; s/tier: staging/tier: prod/' environment.yaml | kubectl apply -f -
```

## Documentation

- [Getting Started](../docs/GETTING_STARTED.md) · [User Guide](../docs/USER_GUIDE.md) · [Architecture](../docs/ARCHITECTURE.md) · [Provider Guide](../docs/PROVIDER_GUIDE.md)
- Control-plane conventions: `platform/controlplane/CONVENTIONS.md`
- Design records: `docs/adr/`

All examples use placeholder names (`acme`, `payments`); replace them with your
organisation and team. Repository URLs use `gitea.cloud.adhar.io` — substitute
your platform host.
