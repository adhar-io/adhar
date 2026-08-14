# Low-Level Design — Package lifecycle operations: toggling, verification, clean removal

Detailed design for [ADR-0014](../adr/0014-package-lifecycle-operations.md). This is the authoritative as-built description of how a platform package moves through its lifecycle on a live Adhar cluster: the `enabled` gate and generator `selector` that decide what is deployed, the toggle path (targeted patch vs. deliberate whole-set apply), the health-not-sync verification surface, the convergent removal discipline, and the catalog that records each package's constraints. It maps the real ApplicationSet fields, the `AdharPlatform` re-apply entry points, the `adhar get status` package dashboard, the ArgoCD sync policy, and the conflict catalog — and is explicit about which rules are **codified** and which are **operational runbook** the ADR mandates but the code does not yet enforce.

## 0. Context recap

[ADR-0004](../adr/0004-applicationset-package-model.md) gives every package one line in a single ApplicationSet with an `enabled` gate. ADR-0014 makes the gate's promise — *any package can be turned on at any time and works, and turning it off returns the cluster to its prior state* — operationally real. It fixes four rules learned on a laptop-class node: (1) toggle by **targeted patch**, never full-file re-apply, because a large Server-Side Apply under load can time out client-side yet land server-side later with stale content; (2) verify in **small waves against ArgoCD health, not sync**, because a loaded repo-server reports sync `Unknown` as an artifact; (3) removal must **converge forcibly**, stripping app finalizers and deleting cluster-scoped debris (webhooks, APIServices) that otherwise poison the whole cluster; (4) the **catalog carries** each package's verified state and constraints so "enable-anytime" is checkable, not folklore.

## 1. Invariants

- **INV-1** The `enabled` label is the *only* thing that decides membership; the generator `selector` filters on it. A package flips on/off by changing one string.
- **INV-2** The ApplicationSet **file** in Gitea (`platform/stack/adhar-appset-<profile>.yaml`) is the declarative source of truth, applied whole only at bootstrap and on deliberate `adhar upgrade`. A single-package runtime toggle patches the live object by list index — the file is never re-applied as a side effect of toggling one package (ADR rule 1).
- **INV-3** A package passes verification when its ArgoCD **health** is `Healthy`; sync status is recorded but is **not** a pass criterion (ADR rule 2).
- **INV-4** Every generated Application carries `resources-finalizer.argoproj.io` and `prune: true`, so a package removed from the selected set is cascade-pruned — and removal must additionally reclaim cluster-scoped kinds by instance label (ADR rule 3).
- **INV-5** Mutually-exclusive packages and shared-namespace collisions are enumerated in `platform/stack/packages/CONFLICTS.md` and enforced against the appsets by `parity_test.go`; enabling a package is only safe after the collision scan is clean (ADR rule 4).

## 2. The gate and selector — declarative source of truth

`platform/stack/adhar-appset-local.yaml` (and `-production.yaml`) is one `argoproj.io/v1alpha1` `ApplicationSet` with a **list generator**: every package is one element carrying `name`, `enabled`, `namespace`, `category`, `manifestPath`. Locally **78** packages are wired; **24** are `enabled: "true"` (a resources-safe single-node core — the full set OOM-kills the node, see [ADR-0012](../adr/0012-single-node-resilience-tuning.md)). Production wires the same 78 with **73** enabled.

```yaml
# platform/stack/adhar-appset-local.yaml
spec:
  generators:
    - list:
        elements:
          - name: "external-secrets"
            enabled: "true"
            namespace: "adhar-system"
            category: "security"
            manifestPath: "security/external-secrets/manifests"
          - name: "falco"
            enabled: "false"          # wired, off by default — flip to "true" to enable
            ...
      selector:
        matchLabels:
          enabled: "true"             # ← the gate: only enabled elements template an Application
  goTemplate: true
  goTemplateOptions: [missingkey=error]
  template:
    metadata:
      finalizers: [resources-finalizer.argoproj.io]   # INV-4: cascade-prune on removal
      labels:
        adhar.io/package-name: "{{ .name }}"
        adhar.io/category: "{{ .category }}"
        environment: "local"
      name: "{{ .name }}"
    spec:
      destination: { namespace: "{{ .namespace }}", server: https://kubernetes.default.svc }
      project: default
      sources:
        - path: "{{ .manifestPath }}"
          repoURL: http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/packages
          targetRevision: main
      syncPolicy:
        automated: { prune: true, selfHeal: true }
        retry: { backoff: { duration: 5s, factor: 2, maxDuration: 1m0s }, limit: 15 }   # ~12 min budget
        syncOptions: [CreateNamespace=true, ServerSideApply=true]
```

The `selector.matchLabels.enabled: "true"` is the mechanism behind INV-1: an element with `enabled: "false"` never becomes an Application, so no workload is created and nothing is pruned. Flipping the string is the entire toggle. `prune: true` + `selfHeal: true` mean ArgoCD both removes pruned resources and reverts out-of-band `kubectl` edits — which is why live fixes to a package must be pushed to Gitea, not `kubectl`-applied (they are self-healed away). `ServerSideApply=true` per-app avoids the last-applied-annotation size limit that large CRD-heavy packages hit.

## 3. Toggling — targeted patch vs. deliberate whole-set apply

### 3.1 The whole-set apply path (codified)

The full ApplicationSet is Server-Side Applied by the `AdharPlatform` controller exactly twice in a package's life:

- **At bootstrap** — `applyPlatformStack` selects the file with `appSetFileForProvider(spec.Provider)` (`platform/controllers/adharplatform/controller.go:378`; kind/empty → `adhar-appset-local.yaml`, else `-production.yaml`) and applies it via `applyManifest` (SSA, `FieldManager="adhar"`, `ForceOwnership`). This is the imperative→declarative handoff described in [design/0001 §6](0001-management-cluster-first.md).
- **On `adhar upgrade`** — the exported `AdharPlatformReconciler.ApplyPlatformStack` (`controller.go:301`) re-pushes the stack and re-applies the appset. `cmd/upgrade/upgrade.go` first runs `diffStack` (clones the in-cluster `packages`/`environments` repos, diffs against local `platform/stack/`), prints the summary, and only on confirmation calls `ApplyPlatformStack` — a *deliberate* whole-set change, exactly the case ADR rule 1 reserves file re-apply for.

### 3.2 The single-package toggle path (operational runbook)

ADR rule 1 requires a runtime enable/disable of *one* package to patch only that element, because a whole-file SSA under load can time out client-side yet land later with stale content, silently reverting narrower changes. The as-built toggle is a **JSON patch by list index** against the live ApplicationSet — an operator command, not a dedicated `adhar` subcommand (see Drift):

```bash
# enable element N (0-based) of the live ApplicationSet without re-applying the file
kubectl -n adhar-system patch applicationset helm-charts-local --type=json \
  -p='[{"op":"replace","path":"/spec/generators/0/list/elements/12/enabled","value":"true"}]'
```

Because this mutates the *live* object and not Gitea, the durable change must also be written back to `platform/stack/adhar-appset-<profile>.yaml` **and** the matching `platform/stack/environments/<env>/config.yaml` (which mirrors the enabled flags) so the next `adhar upgrade` does not revert it — `parity_test.go` fails the build if the two drift (§6). The patch is the fast, load-safe runtime action; the file edit is the durable record.

## 4. Verification — health, not sync

### 4.1 The health surface (codified)

`adhar get status` renders a package readiness dashboard from live ArgoCD Applications. `collectPackageHealth` (`cmd/get/platform_health.go`) lists `argov1alpha1.Application` objects in `adhar-system` and buckets each by **health**, keeping sync only as an annotation:

```go
// cmd/get/platform_health.go
health := string(app.Status.Health.Status)   // Healthy | Progressing | Degraded | Missing | ""
sync   := string(app.Status.Sync.Status)     // Synced | OutOfSync | Unknown | ""
switch health {
case healthHealthy:                              summary.Healthy++     // ✅ pass (INV-3)
case healthProgressing, "Suspended", healthUnknown: summary.Syncing++  // 🔄 in-flight
default: /* Degraded, Missing */                 summary.Degraded++    // ❌ fail
}
```

`displayPlatformHealth` prints `✅ Healthy / 🔄 Progressing / ❌ Degraded` counts and a per-package `PACKAGE | HEALTH | SYNC` table. Sync is shown for triage but never gates the verdict — the direct realization of "health is the pass signal; sync is an ArgoCD artifact under load."

A concrete "health ≠ sync" repair lives in the ArgoCD config: `resource.customizations.health.postgresql.cnpg.io_ScheduledBackup` (`hack/argocd/values.yaml:296`) forces a CNPG `ScheduledBackup` to `Healthy` so its declarative-cron first-run state does not keep a dependent sync wave `Progressing` for up to 24h. Together with `resource.customizations.ignoreResourceUpdates.all: /status`, these keep health an honest signal.

### 4.2 The wave loop (operational runbook)

Catalog qualification — verifying all optional packages — is the batched procedure ADR rule 2 specifies, run as an operator activity rather than codified in a command (see Drift):

1. **Enable ≤4 packages** per wave (patch by index, §3.2) — more than that collapses a single node.
2. **Require calm before each wave** — API server healthy and pod churn settled (`adhar get status` shows no `Progressing`/`Degraded` in the current core).
3. **Kick an explicit sync** per newly-enabled app (`argocd app sync <name>` / `kubectl` refresh annotation) rather than waiting for the auto-sync scan order, which starves under load.
4. **Pass on `health == Healthy`**; record the sync status and any `Degraded` resource-level reasons for triage.
5. Roll the wave off (§5) before starting the next, so peak resource pressure stays bounded.

This is a qualification activity measured in hours of wall-clock on a laptop node — explicitly *not* a user-facing operation.

## 5. Removal — converge, forcibly if needed

### 5.1 The graceful path (codified)

Disabling a package (flip `enabled` → `"false"`, §3.2) drops it from the selector, so the ApplicationSet controller deletes its Application; the `resources-finalizer.argoproj.io` on the template (INV-4) makes that delete **cascade-prune** every resource the app owned. For the common case this is the whole story: `prune: true` + finalizer = clean removal.

`adhar down` (`cmd/down/down.go`) is the *whole-cluster* teardown (`kind delete` + `cleanupFiles`), not a per-package operation — there is no per-package `adhar` removal command. `adhar apps delete` (`cmd/apps/delete.go`) deletes a `platform.adhar.io/v1alpha1` **Application claim** (a developer workload, [design/0021](../adr/0021-day2-operations-first-class.md)); it is a different surface from platform-package removal and does not touch the ApplicationSet.

### 5.2 The forcible convergence (operational runbook)

ADR rule 3 exists because ArgoCD's cascade delete deadlocks when repo-server is loaded, leaving a package half-removed — and, most dangerously, leaving **cluster-scoped debris**: admission webhooks pointing at deleted services degrade *every* API write cluster-wide, and orphaned aggregated APIServices break discovery. When graceful prune does not converge within a bounded time, the operator forces it:

```bash
PKG=falco
# 1. disable (already done) → give ArgoCD a bounded window to prune gracefully
# 2. strip the stuck app's finalizer so the Application object can delete
kubectl -n adhar-system patch application "$PKG" --type=merge \
  -p '{"metadata":{"finalizers":[]}}'
kubectl -n adhar-system delete application "$PKG" --ignore-not-found
# 3. delete anything left, BY INSTANCE LABEL, including cluster-scoped kinds
kubectl delete validatingwebhookconfiguration,mutatingwebhookconfiguration,apiservice,\
clusterrole,clusterrolebinding -l app.kubernetes.io/instance="$PKG" --ignore-not-found
kubectl -n adhar-system delete all,configmap,secret,sa,role,rolebinding \
  -l app.kubernetes.io/instance="$PKG" --ignore-not-found
```

The label `app.kubernetes.io/instance=<package>` is applied by ArgoCD to every managed resource; force-removal depends on it being accurate (hand-added resources must carry it — an accepted ⚠️ in the ADR). The **ValidatingWebhookConfiguration / MutatingWebhookConfiguration / APIService** kinds are the non-negotiable ones: they must never survive a removal because a dead webhook or APIService breaks the cluster far beyond the package that owned it. `CONFLICTS.md`'s standing rule that packages must never ship a `kind: Namespace` object is the paired safeguard — otherwise a prune of `Namespace/adhar-system` would delete the entire platform.

## 6. The catalog — verification state and constraints

ADR rule 4 keeps "enable-anytime" checkable. The as-built catalog is spread across four codified artifacts:

- **`platform/stack/packages/CONFLICTS.md`** — enumerates the shared-`adhar-system`-namespace collisions and mutual exclusions: object-name collisions (`ConfigMap/config-logging` between knative/tekton; `Secret/webhook-certs` between cosign/tekton; `ServiceAccount/minio-sa` between mimir/minio) and service-link `*_PORT` env collisions (cosign's `Service/webhook` → crossplane `--webhook-port` crash, fixed with `enableServiceLinks: false`). It ships a **collision scan** (an inline `python3` script) to run before enabling any new package, and the "related invariants" (never ship `Namespace`, watch env-var/flag namespace references) that make removal safe.
- **ApplicationSet inline comments** — per-element notes record why a package is off and what it costs (e.g. `credential-rotation` disabled "so documented dev credentials keep working"; the retry-budget comment documents the cold-bootstrap slow-start behavior).
- **`platform/stack/environments/<env>/config.yaml`** — mirrors each package's `enabled` flag per environment; the durable record a runtime toggle must be written back to (§3.2).
- **`parity_test.go`** — `TestLocalProductionAppSetParity` asserts every locally-enabled package is also enabled in production (local must stay a subset) and that every enabled package has manifests on disk; `TestEnvironmentConfigsMatchAppSets` asserts the appset `enabled` flags match `environments/<env>/config.yaml`. These make the catalog's enabled-state a build-enforced invariant rather than folklore.

What is **not** yet in the catalog as structured data is a per-package `verified` / `known-broken` field and a machine-readable resource-weight/slow-start descriptor — today those live as prose in comments and CONFLICTS.md (see Drift).

## 7. Failure modes & idempotency

- **Toggle patch races a whole-set apply** — avoided by never re-applying the file on a single toggle (INV-2); the JSON-patch-by-index write is small and cannot half-land.
- **Sync `Unknown` under repo-server load** — never fails verification (INV-3); the package still counts as passing if health is `Healthy`, and sync is surfaced only for triage.
- **Cascade-prune deadlock** — the forcible path (§5.2) strips the finalizer and deletes by instance label; bounded, operator-driven, converges.
- **Orphaned cluster-scoped webhook/APIService** — degrades cluster-wide API writes; §5.2 deletes these kinds by label as a mandatory step; `CONFLICTS.md` bans `kind: Namespace` to prevent the catastrophic prune.
- **Enabling a conflicting pair** — flagged by the `CONFLICTS.md` collision scan before enable; the two apps would otherwise flap `OutOfSync`/`Synced` fighting over a shared object.
- **selfHeal reverts a live fix** — `automated.selfHeal: true` reverts `kubectl` edits to package workloads; durable changes must go through Gitea (`adhar upgrade` / stack push), per the platform's GitOps-live-fix rule.

## 8. Testing

- **Parity / envtest** (`platform/controllers/adharplatform/parity_test.go`): `TestLocalProductionAppSetParity` (local enabled ⊆ production, manifests exist) and `TestEnvironmentConfigsMatchAppSets` (appset flags == `environments/<env>/config.yaml`) — the catalog's enabled-state cannot silently drift (INV-5).
- **AppSet selection** (`TestAppSetFileForProvider`): locks `appSetFileForProvider` provider→file mapping so the whole-set apply path picks the right profile.
- **Collision scan** (`platform/stack/packages/CONFLICTS.md`): the inline `python3` scan is the pre-enable check for new object-name/service-link collisions; runnable from `platform/stack/packages`.
- **Health surface**: `collectPackageHealth`/`displayPlatformHealth` are exercised live via `adhar get status`; on foreign clusters they best-effort no-op (nil AdharPlatform/empty Application list).
- **Tests to add**: a unit test asserting `collectPackageHealth` buckets `Unknown` sync + `Healthy` health as a pass (locking INV-3); a lint check that no package manifest ships `kind: Namespace`; and, if the toggle/verify/remove runbooks are ever codified (Drift), e2e coverage of a wave enable → verify → force-remove cycle.

## 9. Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/adhar-appset-local.yaml` | list generator (78 wired / 24 enabled), `selector.matchLabels.enabled`, template finalizer + `syncPolicy` (prune/selfHeal/retry/SSA) — the gate |
| `platform/stack/adhar-appset-production.yaml` | same shape, 78 wired / 73 enabled (near-full) |
| `platform/stack/environments/<env>/config.yaml` | per-environment `enabled` mirror; durable toggle record |
| `platform/stack/packages/CONFLICTS.md` | shared-namespace collision catalog, mutual exclusions, collision scan, removal-safety invariants |
| `platform/controllers/adharplatform/controller.go` | `applyPlatformStack` / exported `ApplyPlatformStack` (whole-set SSA), `appSetFileForProvider` (profile selection) |
| `cmd/upgrade/upgrade.go` | `diffStack` + confirm + `ApplyPlatformStack` — the deliberate whole-set change path |
| `cmd/get/platform_health.go` | `collectPackageHealth`/`displayPlatformHealth` — health-not-sync dashboard (`adhar get status`) |
| `hack/argocd/values.yaml` | ArgoCD `resource.customizations.health.*` (ScheduledBackup→Healthy) + `ignoreResourceUpdates` keeping health honest |
| `platform/controllers/adharplatform/parity_test.go` | appset/environments parity + manifests-exist enforcement |
| `cmd/apps/delete.go` | deletes an Application **claim** (developer workload) — distinct from platform-package removal |
| `cmd/down/down.go` | whole-cluster teardown (`kind delete`) — not per-package |

## 10. Drift & notes (as-built vs. ADR)

- **The toggle, wave-verify, and force-remove flows are operational runbook, not `adhar` subcommands.** The ADR reads as operational *rules*, and the codified substrate that makes them safe exists (the `enabled` gate + selector, `prune`+finalizer, the health-not-sync dashboard, the conflict catalog, parity tests). But the JSON-patch-by-index toggle (§3.2), the ≤4-wide wave loop (§4.2), and the finalizer-strip + delete-by-label removal (§5.2) are `kubectl`/`argocd` procedures an operator runs by hand — there is no `adhar packages enable/disable/verify/remove` command today. This is the largest gap between the ADR's intent and the shipped tooling.
- **No structured per-package `verified`/`known-broken` field.** ADR rule 4 says verified state and constraints (resource weight, slow-start) "live with the package docs"; in practice they are prose in ApplicationSet comments and `CONFLICTS.md`, plus the machine-checked `enabled` flags. There is no schema-level verification marker, so "verified" is asserted by the enabled-in-core set rather than an explicit attestation.
- **Package counts differ from the ADR/CLAUDE.md figures.** The ADR speaks of a "73-element ApplicationSet" and "49 optional packages"; the as-built `adhar-appset-local.yaml` wires **78** packages (24 enabled locally, 73 enabled in production). CLAUDE.md's "69 wired" is likewise stale. The numbers move as the catalog grows; treat the file as authoritative.
- **Force-removal by label depends on `app.kubernetes.io/instance` accuracy** (accepted ⚠️ in the ADR). ArgoCD applies it; any hand-added resource that omits it will survive a label-scoped delete — the reason CONFLICTS.md insists on removal-safety hygiene.
</content>
</invoke>
