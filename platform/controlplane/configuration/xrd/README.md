# Composite Resource Definitions

Define one XRD per command (or command group) to expose a declarative API that mirrors the `adhar` CLI.  XRD files should:

- Use the `platform.adhar.io` API group
- Default to version `v1alpha1` until stabilised
- Expose fields that align with existing Go types under `platform/types`
- Reference compositions via label selectors such as `feature` and `provider`

Example file naming convention: `cluster.xrd.yaml`, `apps.xrd.yaml`, etc.

## The concept hierarchy (ADR-0026)

`organisation` → `team` → `project` → `apps` (CompositeApplication) → `env` (CompositeEnvironment) → `release`,
plus `agentworkload`. One label vocabulary on everything they emit (`adhar.io/organisation|team|project|application|environment|release`,
`adhar.io/plane: workload`); tiers are `dev | test | prod` only. A project composes its environments as
CompositeEnvironment XRs, its Kargo project/policy/credentials and its Argo CD AppProject, and publishes
`status.environments[]`; an application observes its project and composes Argo CD Applications + Kargo
Warehouse/Stages; a release composes a Kargo Promotion. `platform/controllers/adharplatform/hierarchy_test.go`
pins the agreements between them.

## Managed data services

`search` (CompositeSearch) and `vector` (CompositeVector) give a team an OpenSearch cluster or a Qdrant
vector database of its own, in its namespace, with the same production shape the platform's shared
services have: credentials minted by ESO generators (never in a manifest), Prometheus metrics, and for
search an index-lifecycle policy plus a daily snapshot into the platform object store. Both carry the
hierarchy labels (`organisation|team|project|environment`) and `adhar.io/plane: workload`.
`platform/controllers/adharplatform/datastores_test.go` pins their agreements with the compositions,
the provider RBAC and the data/opensearch package whose operator the search composition relies on.
