# Low-Level Design — Day-2 operations as a first-class CLI surface

Detailed design for [ADR-0021](../adr/0021-day2-operations-first-class.md). This is the authoritative as-built description of how Adhar realises "the platform owns day-2 *mechanism*, the operator owns day-2 *policy*": the `adhar` CLI day-2 command surface (`cmd/`), the load-bearing flows that are actually implemented (status, upgrade, package churn, cluster scale/upgrade, backup/restore reads), the packaged mechanics that ship the schedules/alerts (Velero, CNPG, OpenCost, OnCall, Crossplane Operations), and the GitOps-first write discipline that governs all of them. It also names, honestly, the large scaffolded tail of day-2 verbs that print structure but do not yet act.

## 0. Context recap

A 50-tool catalog is a 50-tool liability if each tool carries its own upgrade/backup/failure semantics. [ADR-0021](../adr/0021-day2-operations-first-class.md) fixes the stance: **the platform automates mechanism** (how to check, upgrade, back up, remove) while **the operator retains policy** (timing, retention, what's enabled where), expressed declaratively through environment configs and the `AdharPlatform` CR — never by editing platform internals. The six mechanisms it names each map to a concrete code path:

| ADR mechanism | Primary code path |
|---|---|
| 1. Status is one command | `cmd/get/status.go` + `cmd/get/platform_health.go` |
| 2. Backup/DR shipped | `platform/stack/packages/core/velero/`, `.../data/cnpg/`, `platform/controlplane/configuration/operations/*.yaml`; CLI reads `cmd/backup/`, `cmd/restore/velero.go` |
| 3. Upgrades converge-and-diff | `cmd/upgrade/upgrade.go` → `AdharPlatformReconciler.ApplyPlatformStack` |
| 4. Package churn (ADR-0014) | `cmd/apps/`, appset `enabled`-gating in `platform/stack/adhar-appset-*.yaml` |
| 5. Cost & incident in the box | `platform/stack/packages/observability/{opencost,oncall,kube-prometheus}/` |
| 6. Runbooks in definition-of-done | `docs/PRODUCTION.md` |

## 1. Invariants

- **INV-1 (mechanism/policy split).** CLI commands invoke platform *mechanism*; retention windows, schedules, upgrade timing, and enablement live in Git (`platform/stack/environments/*`, package manifests) and on the `AdharPlatform` CR — the CLI never hard-codes policy.
- **INV-2 (one status pane).** "Is the platform OK and where do I look next" is answered by exactly one command, `adhar get status`, with no per-tool archaeology.
- **INV-3 (GitOps-first writes).** Durable change to platform packages goes through Git (`adhar upgrade` → force-push → ArgoCD sync); direct `kubectl`/API writes to ArgoCD-managed objects are reverted by ArgoCD `selfHeal`. Bootstrap/foundation components are the exception (patched directly, then converged by `adhar upgrade`). Read/observe commands hit the API server directly.
- **INV-4 (restore-from-Git DR).** The DR model is *re-bootstrap + data restore*, not snapshot archaeology — Git + secret store + object storage hold everything (pillar 2), so `adhar up` + a Velero/CNPG data restore reconstructs a cluster.
- **INV-5 (parity laptop↔prod).** The same day-2 command paths run on a Kind laptop and a cloud management cluster; only size/policy differ.

## 2. The command surface (`cmd/`, registered in `cmd/main.go`)

`cmd/main.go`'s `AddCommand(...)` wires 27 top-level commands onto `rootCmd`. The day-2 subset is a two-tier system:

**Tier A — load-bearing, implemented.** These do real work against the cluster/providers/Git today and carry the ADR's weight:

| Command | Verbs (real) | Platform action | Write path |
|---|---|---|---|
| `adhar get status` (aliases `st`, `health`) | — | single-pane health: nodes/pods/core services + `AdharPlatform` conditions + ArgoCD package health + access URLs | read-only |
| `adhar upgrade` | — | converge foundation (SSA) + stack diff + force-push + ArgoCD refresh | **GitOps** (via Gitea) + direct SSA (foundation) |
| `adhar get secrets` / `apps`/`applications`/`cluster`/`environments`/`all` | — | read platform admin creds & resource inventory | read-only |
| `adhar apps` | `deploy`, `scale`, `delete`, `list`, `status` | app lifecycle via `platform.adhar.io/v1alpha1` `Application` CR + Deployment scale subresource | CR / direct |
| `adhar cluster` | `scale`, `upgrade` (+ `create`/`delete`/`list`/`status`/`kubeconfig`/`debug`/`investigate`) | worker scale & K8s version upgrade via the provider (kubeadm-over-SSH or cloud API) | provider API |
| `adhar backup` | `list`, `status`, `verify`, `schedule` | read/inspect Velero `Backup` CRs + Velero `Schedule`s | read / Velero CR |
| `adhar restore` | `velero list`/`create`/`status` | create & track Velero `Restore` CRs | Velero CR |
| `adhar metrics` | `list` (ServiceMonitors + PromQL) | query Prometheus Operator targets / run PromQL | read-only |
| `adhar health` | `check`, `checks`, `report`, `history` | component-level readiness probes | read-only |
| `adhar secrets` | `list`, `get` | list/read Kubernetes Secrets | read-only |
| `adhar policy` | `list`, `status` | read Kyverno policy inventory & results | read-only |

**Tier B — packaged mechanics (no CLI verb needed).** The ADR's "shipped, not suggested" mechanisms are *installed via the GitOps ApplicationSet* and run on schedules — they need no imperative command:

- **Velero** (`packages/core/velero/`) — cluster-state backup to object storage; schedules enabled in production profiles.
- **CNPG** (`packages/data/cnpg/`) — scheduled backups + PITR for all platform Postgres (Gitea, Keycloak, Harbor); the `ScheduledBackup` lives in `packages/data/cnpg/manifests/install.yaml`.
- **Crossplane Operations** (`platform/controlplane/configuration/operations/`) — `backup-cronoperation.yaml` (`0 2 * * *`, emits a `velero.io/v1` Backup), `secret-rotation-cronoperation.yaml`, `reconstructability-drill.yaml` (the < 1h rebuild SLO drill) — see [design 0005 §5](0005-crossplane-v2-namespaced.md).
- **OpenCost / OnCall / kube-prometheus** (`packages/observability/`) — cost attribution per namespace, incident routing, alert rules shipped *with* the packages.

**Tier C — scaffolded (structure without action).** A wide tail of day-2 verbs exists as cobra commands with `// TODO: Implement` bodies that print success without mutating anything. These define the *intended* surface and are honest drift to track (§12). Notable stubs: `backup create`, `secrets rotate`/`encrypt`/`audit`, all of `gitops` (`sync`/`rollback`/`status`/`repo`/`workflow`), most of `security`/`policy apply`/`restore full`·`database`·`config`·`selective`, `db backup`/`restore`/`migrate`, `env backup`/`restore`, `pipeline create`, and the `auth` sub-verbs.

## 3. Status is one command (`cmd/get/status.go`, `platform_health.go`)

`runGetStatus` builds a `*kubernetes.Clientset`, calls `collectPlatformStatus`, and renders a table (or `--output json|yaml`). The aggregate model:

```go
// cmd/get/status.go
type PlatformStatus struct {
    OverallStatus  string
    CoreServices   []ServiceStatus   // ArgoCD, Gitea, Cilium Gateway (envoy DS), Cilium, Crossplane
    Nodes          NodeStatus
    Workloads      WorkloadStatus
    Resources      ResourceStatus
    NetworkStatus  NetworkStatus
    HealthScore    int               // 100, minus penalties per NotReady node / degraded service / failed pod
    Warnings, CriticalIssues []string // CrashLoopBackOff → critical; ImagePullBackOff/Failed → warn
    Platform []PlatformConditionInfo  // AdharPlatform CR conditions
    Packages *PackageHealthSummary    // ArgoCD Application health roll-up
    URLs     []AccessURL              // HTTPRoute hostnames → browsable https URLs
}
```

`collectCoreServicesStatus` probes fixed selectors in `adhar-system` (e.g. `app.kubernetes.io/name=argocd-server`, `app=gitea`, `app.kubernetes.io/name=cilium-envoy` for the Gateway data path, `app=crossplane`), preferring a Deployment and falling back to DaemonSet (Cilium/Envoy). `attachPlatformHealth` then enriches the struct **best-effort** (15s budget) via a controller-runtime client with the platform scheme (`platform/k8s.GetScheme()`):

- `collectPlatformConditions` — reads `AdharPlatformList[0].Status.Conditions` (`Ready`, `ArgoCDReady`, `GatewayReady`, `GiteaReady`, `CrossplaneReady`, `GitOpsReady`) — returns `nil` on a non-Adhar cluster so `get status` still works everywhere.
- `collectPackageHealth` — lists `argov1alpha1.ApplicationList` in `adhar-system` and rolls up `Healthy`/`Progressing`/`Degraded` counts (the ADR's "per-package ArgoCD health"). This is the same condition set derived by the controller's `syncConditions` ([design 0001 §7](0001-management-cluster-first.md)).
- `collectAccessURLs` — enumerates `gateway.networking.k8s.io/v1` `HTTPRoute` hostnames, skipping wildcards/`localhost`, and formats `https://<host>:<port>` using `AdharPlatform.Spec.BuildCustomization.Port` (default `8443`).

This is INV-2 in code: one command, three enrichment sources, graceful degradation. Deep diagnosis (`--events`, or Grafana/Hubble/Headlamp per ADR-0010) is a next hop, never a prerequisite.

## 4. Upgrades — converge-and-diff (`cmd/upgrade/upgrade.go`)

`adhar upgrade` is the flagship as-built realisation of ADR §3 and is fully implemented. It reuses the controller's reconcilers directly rather than reimplementing install logic. `runUpgrade`:

1. **Resolve the platform.** Load `~/.kube/config`, build a `client.Client` with `k8s.GetScheme()`, `Get` the `AdharPlatform` (default name `adhar`; if absent and exactly one platform exists, adopt it — else demand `--name`).
2. **Phase 1 — converge foundation** (unless `--skip-foundation`/`--diff-only`). Construct an `adharplatform.AdharPlatformReconciler{Client, Scheme, Config: platform.Spec.BuildCustomization, TempDir, StackDir, RepoMap}` and run `controllers.EnsureCRDs` then the embedded-manifest reconcilers **in the same fixed order as bootstrap** ([design 0001 §5](0001-management-cluster-first.md)):
   ```go
   steps := []step{{"gateway-api-crds", r.ReconcileGatewayAPICRDs}, {"cilium", r.ReconcileCilium}, {"gateway", r.ReconcileGateway}}
   if platform.Spec.BuildCustomization.EnableHAMode { steps = append(steps, step{"cnpg", r.ReconcileCNPG}) }
   steps = append(steps, step{"argocd", r.ReconcileArgo}, step{"gitea", r.ReconcileGitea}, step{"crossplane", r.ReconcileCrossplane})
   ```
   Each is idempotent Server-Side Apply (`FieldManager="adhar"`, `ForceOwnership`): unchanged manifests are no-ops; changed components roll to this binary's embedded versions. This is why "foundation and catalog are one platform version."
3. **Phase 2 — stack diff.** `diffStack` reads Gitea admin creds (`utils.GetSecretByName(..., GiteaAdminSecret)`), then for `packages` and `environments` clones the in-cluster Gitea repo over its external URL (`git -c http.sslVerify=false clone --depth 1`, self-signed TLS tolerated) and runs `git diff --no-index --name-status <clone> <local stackDir/repo>` (`gitDiffNames`). Because the stack is pre-rendered manifests ([ADR-0004]), **the Git diff *is* the cluster diff**. The summary prints per-repo changed-file counts (capped at 40). `--diff-only` stops here.
4. **Phase 3 — push + sync.** On confirmation (`--yes` skips the `[y/N]` prompt), `reconciler.ApplyPlatformStack(ctx, &platform)` clears `Status.Gitea.RepositoriesCreated` in memory and re-runs `applyPlatformStack` — force-pushing the local stack to Gitea, re-applying the provider-selected ApplicationSet, and requesting an ArgoCD refresh ([design 0001 §6](0001-management-cluster-first.md)). Progress is then tracked with `adhar get status`.

Credentials are never echoed: `cloneGiteaRepo` errors are passed through `sanitize(out, password)`. Tests: `TestGitDiffNames`, `TestGitDiffNamesIdentical`, `TestSanitize` (`cmd/upgrade/upgrade_test.go`).

## 5. Package churn follows ADR-0014 (`cmd/apps/`)

Enable/disable of *platform* packages is the `enabled: "true"` selector gate in `platform/stack/adhar-appset-*.yaml` pushed via `adhar upgrade` — not an imperative verb (ADR §4: ad-hoc `kubectl delete` of platform packages is off the paved road; ArgoCD `selfHeal` reverts it). For *user* applications, `cmd/apps/` provides the sanctioned lifecycle:

- `apps deploy <name>` (`--template|--repo|--file`, `--wait`) — builds a `platform.adhar.io/v1alpha1` **`Application`** object (a control-plane claim routed through Crossplane, [design 0005](0005-crossplane-v2-namespaced.md)) and SSA-applies it; `--wait` polls health via `waitForApplicationReady`. Templates resolve under `control-plane/examples/apps/<template>.yaml`.
- `apps scale <name> --replicas=N` — issues a Deployment `UpdateScale` subresource call (imperative; appropriate for user workloads in their own namespaces, subject to the app's own ArgoCD policy if managed).
- `apps delete <name>` — deletes the `Application` CR via the dynamic client (`applicationGVR = platform.adhar.io/v1alpha1/applications`), with a `[y/N]` confirmation.
- `apps list`/`status` — read `Application` CRs and summarise sync/health.

## 6. Cluster day-2 (`cmd/cluster/{scale,upgrade}.go`)

Infrastructure-level day-2 (distinct from the platform `adhar upgrade`) is delegated to the provider abstraction (`platform/providers`, [ADR-0022](../adr/0022-custom-clusters-no-managed-k8s.md)):

- `adhar cluster scale <name> --workers=N [--node-group workers] [-p provider]` → `resolveClusterProvider` locates the cluster by name/ID across configured providers, then `prov.ScaleNodeGroup(ctx, clusterID, nodeGroup, N)`. Scale-up provisions instances and joins them with kubeadm; scale-down drains, removes, then deletes.
- `adhar cluster upgrade <name> --version=X.Y.Z` → `prov.UpgradeCluster(ctx, clusterID, version)` — kubeadm-over-SSH (control plane first, then workers) for self-managed clusters, or the cloud API for managed ones.

Both are direct provider-API mutations (there is no GitOps representation of node counts / K8s version for imperatively-managed clusters); policy (target counts, versions) is the operator's, supplied by flag or `--file` config.

## 7. Backup / DR — shipped, read via CLI (`cmd/backup/`, `cmd/restore/`)

Per ADR §2 the *mechanism* is packaged and scheduled (Tier B, §2); the CLI is mostly a **read/trigger** surface over Velero:

- `adhar backup list`/`status`/`verify` — list `velero.io/v1` `Backup` CRs (`backupGVR`, namespace `velero`) via the dynamic client, mapping phase/timing; a missing CRD yields a friendly "velero not present" error (`cmd/backup/{list,status,verify,helpers}.go`).
- `adhar backup schedule …` — inspect/toggle Velero `Schedule`s.
- `adhar restore velero create --from-backup <b>` / `list` / `status` — create and track `velero.io/v1` `Restore` CRs (`cmd/restore/velero.go`), the imperative half of the DR runbook.

The **DR model itself is INV-4**: because Git (via Gitea) + the secret store + object storage hold all durable state, restore = `adhar up` (re-bootstrap the foundation + re-seed the ApplicationSet, [design 0001](0001-management-cluster-first.md)) + a Velero/CNPG data restore. The `reconstructability-drill.yaml` CronOperation exercises the < 1h SLO on a schedule. `adhar backup create` and the granular `restore full`/`database`/`config`/`selective` verbs are **scaffolded** (Tier C) — the working path today is the packaged Velero/CNPG schedules plus `restore velero create`.

## 8. Cost & incident — in the box (packaged)

No imperative command is required (ADR §5):

- **OpenCost** (`packages/observability/opencost/`) — per-namespace/workload spend attribution, enabled from day one including local (laptop RAM is a budget).
- **OnCall + kube-prometheus** (`packages/observability/{oncall,kube-prometheus}/`) — Grafana alerting routes incidents; alert rules ship with packages, not as operator homework. `adhar metrics list --query <promql>` gives an ad-hoc read into the same Prometheus.

## 9. Runbooks — definition-of-done

`docs/PRODUCTION.md` carries the operational procedures (HA, hardening, backup/DR, upgrade). ADR §6 makes a capability without its runbook section *incomplete*; the roadmap's cross-cutting commitment is that every feature ships with docs. This design doc + PRODUCTION.md are the day-2 documentation pair.

## 10. GitOps-first write discipline (summary)

| Command | Effect | Reverted by ArgoCD selfHeal? |
|---|---|---|
| `adhar upgrade` | Gitea force-push + foundation SSA | No — Git *is* the source of truth |
| `adhar apps deploy/delete` | `Application` CR create/delete (control plane) | No — reconciled by Crossplane, not the platform appset |
| `adhar apps scale` | Deployment `UpdateScale` | Only if that Deployment is ArgoCD-managed with selfHeal |
| `adhar restore velero create` | Velero `Restore` CR | No — Velero, not appset-managed |
| `adhar cluster scale/upgrade` | provider API | N/A — no GitOps representation |
| `adhar get *`, `health`, `metrics list`, `secrets list`, `policy list` | read-only | N/A |

The rule (from [MEMORY: GitOps live-fix workflow]): platform *packages* must be changed through Gitea (kubectl edits are reverted); *bootstrap* components (Cilium/Gitea/ArgoCD/foundation) are patched directly and then converged by `adhar upgrade`.

## Testing

- **`cmd/upgrade/upgrade_test.go`** — `TestGitDiffNames`/`TestGitDiffNamesIdentical` cover the diff-name extraction (exit-code-1 handling, temp-prefix stripping); `TestSanitize` asserts credentials never leak into error output.
- **`cmd/auth/credentials_test.go`** — `TestSessionRoundTrip`, `TestParseClaims` (auth token/session plumbing).
- **`cmd/get/status.go`** paths are exercised live/e2e (`get status` runs in the bootstrap e2e as a smoke check) — the reconciler-side condition derivation it renders is unit-tested in `platform/controllers/adharplatform/conditions_test.go` (`TestSyncConditions`) and the pillar/parity suite (`parity_test.go`, ADR-0015).
- **e2e** (`tests/e2e/bootstrap`, `make e2e`) — a full `adhar up` → verify → `adhar down` cycle; `adhar get status`/`adhar backup list`/`adhar restore velero list` degrade gracefully when a mechanism (e.g. Velero) is absent.
- **Tests to add** — coverage for `apps deploy/scale/delete` against envtest (`Application` CR + Deployment scale), a `backup list`/`restore velero` fake-dynamic-client test, and a parity check that every Tier-C stub either graduates or is removed so `--help` never advertises a no-op.

## Code & file map

| Path | Responsibility |
|---|---|
| `cmd/main.go` (`AddCommand`) | registers all 27 top-level commands incl. the day-2 surface |
| `cmd/get/status.go` | `runGetStatus`, `PlatformStatus`, core-service/node/workload collection, table/JSON/YAML render |
| `cmd/get/platform_health.go` | `attachPlatformHealth`, `collectPlatformConditions`, `collectPackageHealth`, `collectAccessURLs` |
| `cmd/get/{secrets,applications,cluster,environments,all}.go` | read-only inventory/credential views |
| `cmd/upgrade/upgrade.go` | `runUpgrade` (converge foundation → `diffStack` → `ApplyPlatformStack`), `cloneGiteaRepo`, `gitDiffNames`, `sanitize` |
| `cmd/apps/{apps,deploy,scale,delete,list,status,status_helpers}.go` | user-app lifecycle via `platform.adhar.io/v1alpha1` `Application` CR + Deployment scale |
| `cmd/cluster/{scale,upgrade,helpers}.go` | `ScaleNodeGroup`/`UpgradeCluster` via provider; `resolveClusterProvider` |
| `cmd/backup/{list,status,verify,schedule,helpers}.go` | Velero `Backup`/`Schedule` reads (`veleroNamespace="velero"`) |
| `cmd/restore/velero.go` | Velero `Restore` create/list/status |
| `cmd/metrics/list.go`, `cmd/health/{check,checks,report,history}.go`, `cmd/secrets/{list,get}.go`, `cmd/policy/{list,status}.go` | read/observe verbs (PromQL, component checks, secret & policy inventory) |
| `platform/controllers/adharplatform/controller.go` | `ApplyPlatformStack` (the upgrade push path), `installCorePackagesSync`, `syncConditions` |
| `platform/stack/adhar-appset-{local,production}.yaml` | `enabled`-gated package churn (ADR-0014) |
| `platform/stack/packages/core/velero/`, `.../data/cnpg/`, `.../observability/{opencost,oncall,kube-prometheus}/` | packaged backup/cost/incident mechanics + schedules/alerts |
| `platform/controlplane/configuration/operations/{backup-cronoperation,secret-rotation-cronoperation,reconstructability-drill}.yaml` | scheduled backup / rotation / DR-drill Operations ([design 0005 §5](0005-crossplane-v2-namespaced.md)) |
| `docs/PRODUCTION.md` | operational runbooks (definition-of-done, ADR §6) |

## 11. Failure modes & idempotency

- **`adhar upgrade` on a partially-diverged cluster** — foundation convergence is SSA-idempotent (re-adopts existing objects); the stack push is a force-push + 409-tolerant repo create ([design 0001 §6](0001-management-cluster-first.md)); safe to re-run.
- **Mechanism not installed** (Velero/CNPG absent, non-Adhar cluster) — every reader (`get status`, `backup list`, `restore velero`, `collect*Health`) degrades to a friendly message or `nil` rather than crashing (best-effort enrichment, CRD-missing guards).
- **Direct write to an ArgoCD-managed package** — reverted by `selfHeal` on the next sync; the sanctioned path is `adhar upgrade`.
- **`adhar upgrade` credential exposure** — cloned Gitea URLs embed basic-auth creds; output is always run through `sanitize` before surfacing.

## 12. Drift & notes (as-built vs. ADR)

- **Two-tier reality vs. one-stance narrative.** ADR-0021 reads as though every mechanism has a supported command. As built, the *load-bearing* commands are `get status`, `upgrade`, `apps`, `cluster scale/upgrade`, `backup`/`restore velero` reads, and the read verbs; a large **Tier-C tail** (`backup create`, `secrets rotate`, all of `gitops`, most `security`/`restore *`/`db`/`env`/`policy apply`/`pipeline`/`auth`) is scaffolded with `// TODO: Implement` bodies that print success without acting. The ADR's guarantees hold via **packaged mechanics + the few implemented commands**, not the full CLI surface. This tail is the single biggest honesty gap to reconcile (graduate or hide).
- **`backup create` is a filesystem stub, not Velero.** `cmd/backup/create.go` prints to a `backupDir` and returns; the real backup mechanism is the packaged Velero/CNPG schedules and the `backup-cronoperation.yaml`. The *read* verbs (`backup list/status/verify`) are genuine Velero-CR clients — an asymmetric surface worth aligning.
- **`gitops sync`/`rollback` are stubs, but `adhar upgrade` already implements the real GitOps push.** The `gitops` command group advertises sync/rollback that the `upgrade` flow (and ArgoCD itself) actually performs; the group is currently redundant scaffolding.
- **`apps` CLI GVR vs. the XRD.** `cmd/apps/status_helpers.go` targets `platform.adhar.io/v1alpha1` resource `applications` (kind `Application`), but the installed XRD is `CompositeApplication` (plural `compositeapplications`, [design 0005 §1](0005-crossplane-v2-namespaced.md)) — there is no `applications` XRD today, so `apps deploy/list/delete` bind to a resource the control plane doesn't currently serve. Either add an `Application` XRD/alias or retarget the CLI to `compositeapplications`.
- **Two commands named "upgrade".** Top-level `adhar upgrade` (platform converge-and-diff) and `adhar cluster upgrade` (K8s version via provider) are different operations; the ADR's "upgrades are a converge-and-diff flow" refers only to the former.
- **`get status` alias `health` shadows the `adhar health` command** — both exist; `adhar get status` (alias `health`) and the standalone `adhar health` overlap and should be reconciled to one canonical entry point.
