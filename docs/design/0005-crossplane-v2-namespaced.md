# Low-Level Design — Crossplane v2 namespaced XRs for self-service infrastructure

Detailed design for [ADR-0005](../adr/0005-crossplane-v2-namespaced.md). This is the authoritative
as-built design for the Adhar control plane: the API surface (XRDs), the implementation layer
(Compositions + functions), managed-resource/ProviderConfig wiring, the `.xpkg` packaging and
embedded install path, the controller reconcile flow, day-2 Operations, and tests. The narrative
companion is [`docs/CONTROL_PLANE.md`](../CONTROL_PLANE.md); the rule book is
[`platform/controlplane/CONVENTIONS.md`](../../platform/controlplane/CONVENTIONS.md).

## 0. Context recap

Self-service infrastructure ("give me a database/cluster/bucket") needs a Kubernetes-native API with
guardrails. Crossplane v1's claim/XR split doubled the object model and muddied RBAC. ADR-0005 adopts
**Crossplane v2's namespaced composite-resource model** (`apiextensions.crossplane.io/v2`,
`scope: Namespaced`, no claims): a team's XR lives in *their* namespace so ordinary RBAC/quotas apply.
The control plane ships as one versioned Configuration package and is installed by the `AdharPlatform`
controller as the last foundation component (Cilium → Gateway → ArgoCD → Gitea → **Crossplane**).

All content lives under `platform/controlplane/`:

```text
platform/controlplane/
├── CONVENTIONS.md                         # authoritative rule book
├── embed.go                               # //go:embed all:configuration → ConfigurationFS
├── configuration/
│   ├── crossplane.yaml                    # Configuration package meta + dependsOn (NOT applied at runtime)
│   ├── xrd/*.xrd.yaml                      # 23 XRDs — the platform API surface
│   ├── compositions/<domain>/*.yaml       # 34 Compositions — one per provider/impl
│   ├── functions/functions.yaml           # 5 composition/operation functions
│   ├── providers/                         # provider packages + ClusterProviderConfigs + cred templates
│   └── operations/*.yaml                  # day-2 CronOperation/WatchOperation
├── features/registry.yaml
└── dist/adhar-control-plane-<version>.xpkg # gitignored build artifact (currently v0.1.4)
```

## 1. The API layer — XRDs (`configuration/xrd/`, 23 files)

Every file defines one platform API in group `platform.adhar.io`, version `v1alpha1`. All use the v2
namespaced shape. From `xrd/database.xrd.yaml`:

```yaml
apiVersion: apiextensions.crossplane.io/v2      # v2: only the XRD moved to /v2
kind: CompositeResourceDefinition
metadata: { name: compositedatabases.platform.adhar.io }
spec:
  group: platform.adhar.io
  scope: Namespaced                              # v2 model — XR lives in the user's namespace, no claims
  names: { kind: CompositeDatabase, plural: compositedatabases }
  defaultCompositionRef: { name: compositedatabase-aws-rds-postgresql }
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:                         # spec.{compositionSelector,parameters,writeConnectionSecretToRef} + status only
          ...
```

Schema conventions enforced across all 23 XRDs ([CONVENTIONS §1](../../platform/controlplane/CONVENTIONS.md)):

- **No `spec.crossplane`** in the schema — Crossplane injects that reserved stanza
  (`compositionRef`, `compositionSelector`, `compositionRevisionRef`, `compositionUpdatePolicy`) into
  every XR. XRDs instead expose a `spec.compositionSelector` (with a `default.matchLabels`, e.g.
  `{feature: database}`) plus `spec.parameters` (the user-facing contract) and a `status` block.
- **No `connectionSecretKeys`** — v2 XRs have no native connection propagation; Compositions that
  surface credentials create a `Secret` explicitly. The database XRD exposes an optional
  `spec.writeConnectionSecretToRef` the KCL program honours.
- Schema is **platform vocabulary** (`engine`, `engineVersion`, `storageSize`, `multiAZ`,
  `backupRetentionDays`, `deletionPolicy`, `skipFinalSnapshot`), not cloud vocabulary — cloud
  specifics get defaulted inside each Composition.

### The 23 APIs by domain

| Domain | XRD file → kind |
|---|---|
| Workloads | `apps`→CompositeApplication, `service`→CompositeService, `pipeline`→CompositePipeline, `webhook`→CompositeWebhook |
| Infrastructure | `cluster`→CompositeCluster, `network`→CompositeNetwork, `storage`→CompositeStorage, `database`→CompositeDatabase |
| Environments | `env`→CompositeEnvironment, `config`→CompositePlatformConfig, `gitops`→CompositeGitOps |
| Security | `auth`→CompositeAuth, `secrets`→CompositeSecrets, `secretrotation`→CompositeSecretRotation, `compliancepolicy`→CompositeCompliancePolicy |
| Observability | `metrics`→CompositeMetrics, `logs`→CompositeLogs, `traces`→CompositeTraces, `health`→CompositeHealth, `costtracker`→CompositeCostTracker |
| Operations | `backup`→CompositeBackup, `restore`→CompositeRestore, `scale`→CompositeScale |

## 2. The implementation layer — Compositions (`configuration/compositions/`, 34 files)

One directory per domain; one file per implementation (e.g. `compositions/database/{aws-rds-postgresql,azure-sql,gcp-cloudsql}.yaml`;
`compositions/cluster/` carries all six providers). Compositions stay on the **v1 API** — v2 has no
`/v2` Composition kind — and are **`mode: Pipeline` only** (native patch & transform is gone):

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: compositedatabase-aws-rds-postgresql
  labels: { feature: database, provider: aws, engine: postgresql }   # dispatch labels
spec:
  compositeTypeRef: { apiVersion: platform.adhar.io/v1alpha1, kind: CompositeDatabase }
  mode: Pipeline
  pipeline:
    - step: render-rds
      functionRef: { name: function-kcl }
      input: { apiVersion: krm.kcl.dev/v1alpha1, kind: KCLRun, spec: { source: "<KCL program>" } }
```

**Selector-based dispatch.** Every Composition is labeled `feature: <domain>` + `provider: <aws|gcp|azure|kind|kubernetes|kyverno|...>`
(database adds `engine:`). Selection is driven per-request by `spec.crossplane.compositionSelector`
(matching those labels) or the XRD's `defaultCompositionRef`. Several Compositions implement one XRD —
one per cloud — which is exactly how the same `CompositeDatabase` becomes RDS on AWS, Cloud SQL on GCP,
Azure SQL on Azure, or a Kubernetes-native database locally.

**Functions in use** (declared in `crossplane.yaml`, installed via `functions/functions.yaml`):

| Function (package, pinned version) | Where it runs | Notes |
|---|---|---|
| `function-kcl` (`function-kcl:v0.12.1`) | **primary generator** in nearly every Composition | KCL program reads `option("params").oxr`, computes MRs/native objects |
| `function-go-templating` (`function-go-templating:v0.12.1`) | 8 Compositions — all six `cluster/*`, `gitops/argocd-project`, `apps/argocd-application` | Go-template `GoTemplate`/`Inline` rendering |
| `function-auto-ready` (`function-auto-ready:v0.6.5`) | last step in 18 of 34 Compositions | derives XR `Ready` from composed-resource readiness |
| `function-python` (`function-python:v0.4.0`) | day-2 Operations only (§5) | `operate(req, rsp)` entrypoint |
| `function-patch-and-transform` (`function-patch-and-transform:v0.10.7`) | **installed but currently unreferenced** | available for declarative P&T (see Drift) |

The other 16 Compositions **compute their own XR status in KCL** (returning a
`platform.adhar.io/v1alpha1` status object as the last item in the `ResourceList`) and so omit
`function-auto-ready` — e.g. `database/aws-rds-postgresql.yaml` maps `dbInstanceStatus` →
`{phase, ready, endpoint, port}` itself; `env/kubernetes-namespace.yaml` sets
`phase: Available` after emitting the Namespace.

### Two composition idioms ([CONVENTIONS §2](../../platform/controlplane/CONVENTIONS.md))

1. **Native Kubernetes objects** (in-cluster resources): emit the object directly with `metadata.namespace`
   omitted — Crossplane v2 manages natives and stamps the XR's namespace. `env/kubernetes-namespace.yaml`
   emits a `v1/Namespace`, `v1/ResourceQuota`, `v1/LimitRange`, and (conditionally) a
   `networking.k8s.io/v1/NetworkPolicy` straight from KCL.
2. **Managed resources** (cloud/remote): use the **namespaced `.m` MR groups** and reference a
   cluster-scoped `ClusterProviderConfig`. `database/aws-rds-postgresql.yaml` emits
   `rds.aws.m.upbound.io/v1beta1` `Instance`/`SubnetGroup` and `iam.aws.m.upbound.io/v1beta1` `Role`,
   each with `spec.providerConfigRef = {name: "default", kind: "ClusterProviderConfig"}`.

## 3. Managed resources & ProviderConfigs (`configuration/providers/`)

Namespaced MRs add **`.m`** to the domain and reset to **`v1beta1`** (provider-kubernetes `Object` is
`v1alpha1`), referencing a shared cluster-scoped config by kind `ClusterProviderConfig`:

| Purpose | Namespaced group used |
|---|---|
| K8s object | `kubernetes.m.crossplane.io/v1alpha1` `Object` → config **`kubernetes-provider`** |
| Helm release | `helm.m.crossplane.io/v1beta1` `Release` → config **`helm-provider`** |
| AWS / Azure / GCP | `<svc>.{aws,azure,gcp}.m.upbound.io/v1beta1` → config **`default`** per cloud family |

- `providers/provider-packages.yaml` — `pkg.crossplane.io/v1` `Provider` for `provider-kubernetes:v1.2.1`
  and `provider-helm:v1.2.0` (the only providers needed for local/in-cluster compositions).
- `providers/config/{kubernetes,helm}-providerconfig.yaml` — the `kubernetes-provider`
  (`credentials.source: InjectedIdentity`) and `helm-provider` `ClusterProviderConfig`s, plus a
  `kubernetes-external` variant reading a kubeconfig Secret.
- `providers/cloud/` — 15 Upbound cloud `Provider` packages (`provider-packages.yaml`) + per-cloud
  `ClusterProviderConfig`s (`aws.m.upbound.io/v1beta1` `ClusterProviderConfig`, …) + a
  `credential-secrets-template.yaml`. Applied **only on cloud platforms** (see §4) because the images
  are hundreds of MB and crash-loop without credentials.
- **DigitalOcean & Civo** community providers (v0.x) have no namespaced MRs yet, so their compositions
  stay legacy cluster-scoped and reference plain `ProviderConfig` (`digitalocean` / `civo`).

Production credentials use workload identity (IRSA / GCP Workload Identity / Azure Managed Identity);
the credential Secret templates are the fallback.

## 4. Install & reconcile flow (`platform/controllers/adharplatform/crossplane.go`)

The control plane is embedded, not fetched: `platform/controlplane/embed.go` exposes
`ConfigurationFS` via `//go:embed all:configuration`, so the controller applies the same tree whether
it runs in-process during `adhar up` or in-cluster — no registry, `kubectl`, or on-disk source needed
(same philosophy as [ADR-0006](../adr/0006-embedded-bootstrap-manifests.md)).

`ReconcileCrossplane` is the last entry in the foundation installer list in `controller.go`
(`Gateway API CRDs → Cilium → Gateway → [CNPG if HA] → ArgoCD → Gitea → Crossplane`). It:

1. **Installs Crossplane core** — SSA of the embedded `resources/crossplane/install.yaml`
   (`//go:embed resources/crossplane` in `crossplane.go`) into namespace `adhar-system`
   ([ADR-0011 shared namespace](../adr/0011-shared-platform-namespace.md)). The Helm-derived values in
   `hack/crossplane/values.yaml` set `args: [--enable-operations]` to turn on the alpha Operations API.
2. **Waits** up to 30×10s for `Deployment/crossplane` (in `globals.AdharSystemNamespace`) to have
   `ReadyReplicas > 0`, then sets `Status.Crossplane.Available = true`.
3. **Applies the configuration tree** once (gated on `Status.Crossplane.ControlPlaneApplied`) via
   `applyControlPlaneConfiguration`, in strict order — each `applyEmbeddedManifests` call does
   Server-Side Apply (`applyManifest`, `ForceOwnership`, `FieldManager="adhar"`) of every YAML doc:

   ```
   configuration/xrd            (non-recursive)  → sleep 5s   # XRDs establish before Compositions reference them
   configuration/compositions   (recursive)                  # nested per-domain dirs
   configuration/functions      (non-recursive)              # Function CRD ships with core
   configuration/providers      (non-recursive)              # provider-kubernetes/helm packages only (no descent)
   configuration/providers/config(non-recursive)             # ClusterProviderConfigs (retry until provider CRDs register)
   configuration/operations     (non-recursive)              # day-2 ops
   if isCloudProvider(spec.Provider):
     configuration/providers/cloud (recursive, best-effort)  # heavy cloud providers + configs
   ```

   `crossplane.yaml` is package metadata (`meta.pkg.crossplane.io/v1`) and is **intentionally not
   applied** at runtime. Cloud is `isCloudProvider` = AWS/Azure/GKE/DO/Civo (empty ⇒ local kind, skipped).

`applyEmbeddedManifests(fsys, dir, resource, label, recursive, bestEffort)` walks the embedded FS,
`fs.SkipDir`-ing subdirectories when `!recursive`, and either aborts on first failure (so the reconcile
retries) or logs-and-skips when `bestEffort`.

**Idempotency / retry.** ProviderConfigs and Operations reference CRDs that register a minute or two
after their Provider packages install; first-pass failures return an error, `ReconcileCrossplane`
swallows it (returns no-requeue) and the outer reconcile retries because
`Status.Crossplane.ControlPlaneApplied` stays `false`. `controller.go`'s `ExitOnSync` shutdown and
`isPlatformAlreadyDeployed` both require `ControlPlaneApplied == true`, so `adhar up` will not report
success until the whole tree applies cleanly.

### Status & conditions

`api/v1alpha1/adharplatform_types.go`:

```go
type CrossplaneStatus struct {
    Available           bool `json:"available,omitempty"`           // core Deployment up
    ControlPlaneApplied bool `json:"controlPlaneApplied,omitempty"` // XRDs+Compositions+Providers+Ops applied
}
```

`conditions.go` derives `ConditionCrossplaneReady = Available && ControlPlaneApplied` and folds both
into the aggregate `Ready`. `adhar get status` surfaces it alongside ArgoCD/Gateway/Gitea/GitOps.

## 5. Day-2 Operations (`configuration/operations/`)

Crossplane v2's `ops.crossplane.io/v1alpha1` (`Operation`/`CronOperation`/`WatchOperation`, alpha,
gated by the core `--enable-operations` flag) covers scheduled/triggered actions that don't fit the
"reconcile a desired-state XR" model, reusing the same function machinery via `function-python`:

| File | Kind(s) | What it does |
|---|---|---|
| `backup-cronoperation.yaml` | CronOperation (`0 2 * * *`, `concurrencyPolicy: Forbid`, history 5/3, `startingDeadlineSeconds: 900`) | `function-python` emits a `velero.io/v1` `Backup` of `adhar-system` + `crossplane-system`; run recorded in `rsp.output` |
| `secret-rotation-cronoperation.yaml` | CronOperation | rotates platform-managed credentials on a schedule |
| `drift-watchoperation.yaml` | WatchOperation | watches ConfigMaps, reacts to out-of-band drift |
| `reconstructability-drill.yaml` | CronOperation + WatchOperation | periodic restore/rebuild drill |

An Operation pipeline's `function-python` `operate(req, rsp)` places resources in
`rsp.desired.resources` (Crossplane force-applies them, no owner refs) and writes `rsp.output` — so
every run is inspectable as an `Operation` object.

## 6. A request's life (end-to-end)

```
Developer applies CompositeDatabase (namespaced) in team-orders
  → API server admits it (RBAC on the namespace + XRD schema validation)
  → Crossplane selects the Composition by labels {feature=database, provider=aws, engine=postgresql}
  → function-kcl pipeline renders rds.aws.m.upbound.io Instance/SubnetGroup (+ connection Secret)
     into team-orders, each with providerConfigRef {name: default, kind: ClusterProviderConfig}
  → provider-aws converges RDS; MR status flows back; KCL maps dbInstanceStatus → XR status.{phase,ready,endpoint,port}
  → Developer reads status, mounts the connection Secret
```

Operating-model properties: continuous reconciliation (console drift is reverted); environment dispatch
(same manifest → RDS / Cloud SQL / CNPG by selector or `defaultCompositionRef`); deletion discipline
(XR delete cascades to MRs honouring each MR's `deletionPolicy`; the database XRD exposes
`deletionPolicy` + `skipFinalSnapshot` as parameters); tenancy for free (namespace RBAC + quotas +
Kyverno govern XRs like any resource).

## 7. Packaging — the `.xpkg` (`make build-control-plane`)

The same `configuration/` tree ships two ways: **embedded** (§4) and a **Crossplane Configuration
package** for standalone/versioned installs. The Makefile target:

```make
build-control-plane:                       # part of `make build`
  crossplane xpkg build \
    --package-root=platform/controlplane/configuration \
    --examples-root=platform/controlplane/dist/examples \
    -o platform/controlplane/dist/adhar-control-plane-$(VERSION).xpkg
```

`VERSION` derives from the latest git tag (`git describe --tags --abbrev=0`, fallback `v0.1.0`); if the
`crossplane` CLI is absent the target falls back to a `tar -czf` of the same tree. Output lands in the
**gitignored** `platform/controlplane/dist/` (currently `adhar-control-plane-v0.1.4.xpkg`) and is
published as a GoReleaser release asset. `crossplane.yaml`'s `spec.crossplane.version: ">=v2.3.0"` and
`dependsOn` (Upbound AWS/Azure/GCP families ≥v2, DO/Civo contrib, provider-kubernetes/helm ≥v1.2.0,
the five functions) are the package constraints the registry resolves.

## Testing

- **Unit / envtest** — `platform/controllers/adharplatform/conditions_test.go` asserts
  `ConditionCrossplaneReady` reflects `Available && ControlPlaneApplied`, and the aggregate `Ready`
  gating. `crossplane.go`'s `isYAML`/`isCloudProvider` helpers are covered indirectly by the reconcile
  path (envtest installs the ArgoCD CRD; Crossplane core CRDs aren't present under envtest, so the
  cloud/provider apply steps are exercised on live/e2e clusters).
- **e2e** (`make e2e`, `tests/e2e/bootstrap`) — a full `adhar up` waits on `ControlPlaneApplied`;
  post-up checks per `docs/CONTROL_PLANE.md §8`: `kubectl get xrd` (23), `kubectl get compositions`
  (34), `kubectl get providers,functions` Installed/Healthy, and applying an `examples/*` XR into a
  scratch namespace and watching `SYNCED/READY`.
- **Package build** — `make build-control-plane` (invoked by `make build`) validates the tree renders
  into a `.xpkg` without `crossplane xpkg build` errors.
- **Tests to add** — a lint/parity check that every `xrd/*.xrd.yaml` has ≥1 Composition whose
  `compositeTypeRef.kind` matches, that no Composition references a `functionRef.name` absent from
  `functions/functions.yaml`, and that no XRD schema declares `spec.crossplane`.

## Code & file map

| Path | Responsibility |
|---|---|
| `platform/controlplane/CONVENTIONS.md` | authoritative v2 rules (scope, `.m` groups, ProviderConfig names, function names) |
| `platform/controlplane/embed.go` | `//go:embed all:configuration` → `ConfigurationFS` |
| `platform/controlplane/configuration/crossplane.yaml` | Configuration package meta + `dependsOn` (build-time only) |
| `platform/controlplane/configuration/xrd/*.xrd.yaml` | 23 XRDs (`apiextensions/v2`, `Namespaced`) |
| `platform/controlplane/configuration/compositions/<domain>/*.yaml` | 34 Pipeline Compositions (v1) |
| `platform/controlplane/configuration/functions/functions.yaml` | 5 `pkg.crossplane.io/v1` Functions |
| `platform/controlplane/configuration/providers/{provider-packages,config/*,cloud/*}.yaml` | Provider packages + `ClusterProviderConfig`s + cred templates |
| `platform/controlplane/configuration/operations/*.yaml` | CronOperation/WatchOperation (day-2) |
| `platform/controllers/adharplatform/crossplane.go` | `ReconcileCrossplane`, `applyControlPlaneConfiguration`, `applyEmbeddedManifests`, `isCloudProvider` |
| `platform/controllers/adharplatform/resources/crossplane/install.yaml` | embedded Crossplane core install (namespace `adhar-system`) |
| `platform/controllers/adharplatform/controller.go` | foundation install ordering; `ExitOnSync`/`isPlatformAlreadyDeployed` gate on `ControlPlaneApplied` |
| `platform/controllers/adharplatform/conditions.go` | `ConditionCrossplaneReady` derivation + aggregate `Ready` |
| `api/v1alpha1/adharplatform_types.go` | `CrossplaneStatus{Available, ControlPlaneApplied}`, `CrossplanePackageName` |
| `hack/crossplane/values.yaml`, `values-ha.yaml` | core Helm values incl. `--enable-operations` |
| `Makefile` (`build-control-plane`) | `crossplane xpkg build` → `dist/adhar-control-plane-<version>.xpkg` |
| `docs/CONTROL_PLANE.md` | narrative guide + hands-on/debugging |

## Drift & notes (as-built vs. ADR / docs)

- **Core namespace is `adhar-system`, not `crossplane-system`.** `install.yaml` deploys Crossplane
  core into `adhar-system` and the controller waits for `Deployment/crossplane` there ([ADR-0011]).
  But `docs/CONTROL_PLANE.md §9` debug commands, the backup Operation's `includedNamespaces`, and
  connection-secret defaults (`writeConnectionSecretToRef.namespace: crossplane-system`) all reference
  `crossplane-system`. Provider/function pods run wherever Crossplane schedules them; the doc/manifest
  `crossplane-system` references are stale for this deployment model and worth reconciling.
- **`function-patch-and-transform` is installed but unused** — declared in `crossplane.yaml` +
  `functions.yaml`, referenced by zero Compositions today. ADR-0005/CONVENTIONS list it as available
  ("declarative P&T"); it is scaffolding, not in the active path.
- **Operations count** — CLAUDE.md/CONTROL_PLANE.md say "3 Operations"; there are **4 files** defining
  5 Operation objects (`reconstructability-drill.yaml` holds both a CronOperation and a WatchOperation),
  adding a reconstructability drill beyond backup/secret-rotation/drift-watch.
- **`function-auto-ready` is not universal** — used in 18/34 Compositions; the other 16 (database,
  env, and other KCL-status compositions) compute XR readiness themselves in KCL and omit it, which
  CONVENTIONS §2 explicitly permits.
