# Composite Resource Definitions

Define one XRD per command (or command group) to expose a declarative API that mirrors the `adhar` CLI.  XRD files should:

- Use the `platform.adhar.io` API group
- Default to version `v1alpha1` until stabilised
- Expose fields that align with existing Go types under `platform/types`
- Reference compositions via label selectors such as `feature` and `provider`

Example file naming convention: `cluster.xrd.yaml`, `apps.xrd.yaml`, etc.

**Kinds carry no `Composite` prefix** (renamed 2026-10-07): the kind is the thing
(`Database`, `Bucket`, `Project`), not Crossplane's word for how it is
implemented. `TestNoXRDKindCarriesTheCompositePrefix` enforces it, along with
`metadata.name == <plural>.<group>` — an XRD whose name disagrees is rejected at
install time.

## The concept hierarchy (ADR-0026)

`organisation` → `team` → `project` → `apps` (Application) → `env` (Environment) → `release`,
plus `agentworkload`. One label vocabulary on everything they emit (`adhar.io/organisation|team|project|application|environment|release`,
`adhar.io/plane: workload`); tiers are `dev | test | prod` only. A project composes its environments as
Environment XRs, its Kargo project/policy/credentials and its Argo CD AppProject, and publishes
`status.environments[]`; an application observes its project and composes Argo CD Applications + Kargo
Warehouse/Stages; a release composes a Kargo Promotion. `platform/controllers/adharplatform/hierarchy_test.go`
pins the agreements between them.

## Managed data services

`search` (Search) and `vector` (Vector) give a team an OpenSearch cluster or a Qdrant
vector database of its own, in its namespace, with the same production shape the platform's shared
services have: credentials minted by ESO generators (never in a manifest), Prometheus metrics, and for
search an index-lifecycle policy plus a daily snapshot into the platform object store. Both carry the
hierarchy labels (`organisation|team|project|environment`) and `adhar.io/plane: workload`.
`platform/controllers/adharplatform/datastores_test.go` pins their agreements with the compositions,
the provider RBAC and the data/opensearch package whose operator the search composition relies on.

## Shared-service resources

Five kinds give a team a piece of a service the platform ALREADY runs, rather
than a second copy of it — which is the difference between a request a cluster
can afford and one it cannot:

| Kind | File | Composes onto |
|---|---|---|
| `Bucket` | `bucket.xrd.yaml` | a bucket on RustFS, the platform object store |
| `Table` | `table.xrd.yaml` | an Iceberg table in the lakehouse, via Trino DDL |
| `Topic` | `topic.xrd.yaml` | a KafkaTopic on the shared `adhar-kafka` cluster |
| `Queue` | `queue.xrd.yaml` | a RabbitMQ queue on the shared broker, via its management API |
| `Repository` | `repository.xrd.yaml` | a Gitea repo, its Harbor project and its Nexus hosted repos |

`Cache` (`cache.xrd.yaml`) is the exception: it composes the team its OWN Valkey,
because a cache is cheap and its eviction policy is the team's business.

Two of them have an older, lower-level neighbour that still works and that the
CLI still uses: `Storage` with `type: object` (what `adhar bucket create` makes)
and `Database` with a cache engine (what `adhar cache create` makes, because it
also offers `--engine redis`).

## ApplicationType (`apps.xrd.yaml`)

`Application.spec.parameters.type` — `service | web | worker | data | ai`, default `service` — says what
kind of application this is, and every composition stamps it on the Argo CD Application and the namespace as
`platform.adhar.io/application-type`. For `ai` the `parameters.ai` block (environment, `isolation: microvm|container`,
`gpu: {count, sharing: mig|timeslice|none}`, `models`, `tools`, `budget`) additionally composes an **AgentWorkload**
(`agentworkload.xrd.yaml`: namespace `<name>-<env>`, ServiceAccount, quota, NetworkPolicy, agentgateway model/tool
allow-lists and budget) and labels that namespace `platform.adhar.io/isolation`, `gpu-sharing` and `gpu-count`. The
labels are the contract; the `ai-workload-isolation` Kyverno policy (`security/adhar-kyverno-policies`) is the
mechanism that turns them into a Kata RuntimeClass and GPU-pool placement for every pod.
