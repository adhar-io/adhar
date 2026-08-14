# Low-Level Design — Single ApplicationSet with an enabled-gated package list

Detailed design for [ADR-0004](../adr/0004-applicationset-package-model.md). This is the authoritative as-built description of how every platform package reaches the cluster: one ArgoCD `ApplicationSet` per environment, whose list generator carries one element per package, gated by a `selector` on `enabled: "true"`. It maps the real appset files, the element schema, the pre-rendered-manifest convention, the provider-driven appset selection in the controller, the parity tests that stop the imperative/declarative boundary from drifting, and the day-2 toggle path.

## 0. Context recap

The stack ships ~86 package directories and wires 77 of them for deployment. ADR-0004 rejected one-Application-per-package (69 files of boilerplate), the Git directory generator ("everything in the repo deploys" — no gating), and app-of-apps (still per-package manifests) in favour of **one `ApplicationSet` with an explicit list generator**: each package is a single list element carrying `{name, enabled, namespace, category, manifestPath}`. A generator `selector` on `enabled: "true"` means *everything is wired, only enabled entries deploy* — which makes partial enablement (a single Kind node OOMs on the full set) first-class rather than an afterthought. Packages are **pre-rendered manifests** (`helm template` output), so ArgoCD syncs plain YAML and Git diffs are the exact cluster diff.

## 1. The element schema

Every generator element is exactly five string fields (`platform/stack/adhar-appset-local.yaml`):

```yaml
- name: "cert-manager"
  enabled: "false"            # ← the gate; matched by the generator selector
  namespace: "adhar-system"   # per-element destination ns (ADR-0011 shared ns)
  category: "security"        # security|data|observability|application|core|infrastructure
  manifestPath: "security/cert-manager/manifests"  # path within the packages repo
```

The Go struct that pins this contract is `appSetElement` in [`parity_test.go`](../../platform/controllers/adharplatform/parity_test.go):

```go
type appSetElement struct {
    Name         string `json:"name"`
    Enabled      string `json:"enabled"`        // string "true"/"false", not bool — ArgoCD list generators are string-only
    Namespace    string `json:"namespace"`
    Category     string `json:"category"`
    ManifestPath string `json:"manifestPath"`
}
```

`enabled` is a **string**, not a boolean: ArgoCD list-generator elements are `map[string]string`, and the selector matches label values, which are strings. `wiring(e)` (the same file) is the 4-tuple `{Name, Namespace, Category, ManifestPath}` — the package's *identity*, everything except enablement. Parity is defined over `wiring`; enablement is the only field allowed to differ across environments.

## 2. The ApplicationSet body (`adhar-appset-local.yaml`)

One `ApplicationSet` per environment, all in namespace `adhar-system`:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata: { name: helm-charts-local, namespace: adhar-system }
spec:
  generators:
    - list:
        elements: [ …77 package elements… ]
      selector:                 # ← the gate: only enabled: "true" elements template an Application
        matchLabels: { enabled: "true" }
  goTemplate: true
  goTemplateOptions: [ missingkey=error ]   # a typo'd/absent field fails the render, never renders empty
  template:
    metadata:
      finalizers: [ resources-finalizer.argoproj.io ]
      labels:
        adhar.io/package-name: "{{ .name }}"      # day-2 selector handle (§6)
        adhar.io/category: "{{ .category }}"
        environment: "local"
      name: "{{ .name }}"                          # Application name == package name
    spec:
      destination:
        namespace: "{{ .namespace }}"              # per-element, so a package can opt out of adhar-system
        server: https://kubernetes.default.svc
      project: default
      sources:
        - path: "{{ .manifestPath }}"
          repoURL: http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/packages
          targetRevision: main
      syncPolicy:
        automated: { prune: true, selfHeal: true }
        retry: { backoff: { duration: 5s, factor: 2, maxDuration: 1m0s }, limit: 15 }
        syncOptions: [ CreateNamespace=true, ServerSideApply=true ]
```

Salient points:

- **`selector` is a sibling of `list`, not nested inside it.** It filters the generated parameter set: elements whose `enabled` value isn't `"true"` never produce an Application. Wiring 77 packages but templating only the enabled subset is the whole mechanism.
- **`repoURL` is the in-cluster Gitea** `adhar/packages` repo seeded during bootstrap ([ADR-0001 §6](0001-management-cluster-first.md)) — not an upstream chart repo. `path` is the element's `manifestPath`; `targetRevision: main`.
- **`namespace` is templated per-element**, so nearly everything lands in the shared `adhar-system` ([ADR-0011](../adr/0011-shared-platform-namespace.md)) while an incompatible package opts out — `open-function` uses `namespace: "openfunction"` because it vendors Knative/Tekton objects whose fixed upstream names collide with the core `tekton` package (see [`packages/CONFLICTS.md`](../../platform/stack/packages/CONFLICTS.md)).
- **`selfHeal: true` + `prune: true`** — kubectl edits to a synced package are reverted; toggling must go through Git (that is the operational rule in [ADR-0014](../adr/0014-package-lifecycle-operations.md)).
- **`ServerSideApply=true` + `CreateNamespace=true`** on every Application. The retry budget (`limit: 15`, 5s×2 backoff capped at 1m ≈ 12 min) covers cold-bootstrap dependency races — e.g. `adhar-console`'s OIDC `ExternalSecret` waiting on Keycloak's realm client.

## 3. Pre-rendered manifests

Each package directory holds three things: `values.yaml`, `generate-manifests.sh`, and the checked-in `manifests/` the appset points at. The script is `helm template` piped to disk — e.g. [`security/cert-manager/generate-manifests.sh`](../../platform/stack/packages/security/cert-manager/generate-manifests.sh):

```bash
helm repo add jetstack https://charts.jetstack.io --force-update
helm template --namespace adhar-system cert-manager jetstack/cert-manager \
  -f values.yaml --version v1.20.2 --set crds.enabled=true >> manifests/install.yaml
```

71 of the packages carry a `generate-manifests.sh`. The consequence chain from ADR-0004: ArgoCD syncs **plain YAML** (no in-cluster Helm evaluation, no chart-repo network dependency at sync time), Git diffs are the literal cluster diff (reviewable), and installs are reproducible. Version bumps mean re-running the script and committing the regenerated `manifests/` — a `⚠️` the ADR accepts as automatable in CI. Per-environment values are handled by per-env manifest sub-paths, e.g. `argo-workflows` ships `manifests/base` and `manifests/dev`, and the element points at `application/argo-workflows/manifests/dev`.

## 4. The appset family (as-built: four files, ADR describes one)

The ADR describes "a single ApplicationSet per environment." As built there are **four** appset files under `platform/stack/`, of which the controller applies at most two per bootstrap:

| File | `metadata.name` | Generator | Selected by | Enabled |
|---|---|---|---|---|
| `adhar-appset-local.yaml` | `helm-charts-local` | `list` (77 elements) | `appSetFileForProvider` for `kind`/unset | 21 |
| `adhar-appset-production.yaml` | `helm-charts-production` | `list` (77 elements) | `appSetFileForProvider` for any cloud/on-prem provider | 72 |
| `adhar-appset-workload.yaml` | `adhar-workload-clusters` | `matrix` (clusters × list) | always applied if present (§5) | thin agent set |
| `adhar-appset-gitops.yaml` | `helm-charts-gitops` | `list` (72 elements) | **not** applied by the controller | 72 |

`local` and `production` are the parity-locked pair: identical 77-element wiring, differing **only** in the `enabled` field (local enables 21; production enables 72 — all but the documented conflict exclusions such as `knative`). The `namespace` on every element in both is `adhar-system` (bar `open-function`).

`adhar-appset-gitops.yaml` is the odd one out and diverges materially — see Drift (§8): it assigns each package its **own** namespace (`external-secrets`→`external-secrets`, `falco`→`falco`, `cert-manager`→`cert-manager`, …), wires only 72 packages (missing `tekton`, `pyroscope`, `credential-rotation`, `policy-packs`, `jenkins-x`), and is referenced only as an example input to `adhar gitops resolve` ([`cmd/gitops/resolve.go`](../../cmd/gitops/resolve.go)), not by the reconcile path.

`adhar-appset-workload.yaml` uses a **matrix** generator (`clusters × list`) with `packageName` (not `name`) elements, still enabled-gated by a sibling `selector` — the same ADR-0004 shape projected onto every ArgoCD-registered workload cluster (roadmap P2.2). Its list is a thin agent set: `metrics-server`, `kyverno`, `kyverno-policies`, `alloy`.

## 5. Provider-driven selection & apply (`controller.go`)

The appset is applied during the GitOps-seeding step of the `AdharPlatform` reconcile, after Gitea is populated and ArgoCD repo auth is in place ([`controller.go`](../../platform/controllers/adharplatform/controller.go) `applyPlatformStack`):

```go
appSetFile := appSetFileForProvider(resource.Spec.Provider)   // controller.go:339
appSetPath := filepath.Join(r.StackDir, appSetFile)
appSetBytes, _ := os.ReadFile(appSetPath)
r.applyManifest(ctx, appSetBytes, resource, "Platform stack ApplicationSet")
```

```go
// controller.go:378 — Kind (and unset) ⇒ curated local core; everything else ⇒ full production.
func appSetFileForProvider(provider v1alpha1.EnvironmentProvider) string {
    if provider == v1alpha1.ProviderKind || provider == "" {
        return "adhar-appset-local.yaml"
    }
    return "adhar-appset-production.yaml"
}
```

`applyManifest` is the shared SSA helper (`FieldManager="adhar"`, `ForceOwnership`) — so re-applying the appset on every `adhar up` / `adhar upgrade` is idempotent. Immediately after, the workload appset is applied best-effort if the file exists (`controller.go:358`) — it generates zero Applications until a workload cluster registers, so applying it unconditionally on a single-cluster platform is harmless:

```go
workloadAppSetPath := filepath.Join(r.StackDir, "adhar-appset-workload.yaml")
if workloadBytes, err := os.ReadFile(workloadAppSetPath); err == nil {
    r.applyManifest(ctx, workloadBytes, resource, "Workload cluster ApplicationSet")
} else if !os.IsNotExist(err) { return fmt.Errorf(...) }
```

`r.StackDir` is the absolute path to `platform/stack/` on the bootstrapping CLI host; this is a CLI-bootstrap capability (the in-cluster manager runs with an empty `StackDir`). Once applied, the controller stops writing workloads — ArgoCD reconciles the 21/72 enabled Applications from the seeded Gitea repo. This is the imperative→declarative boundary.

## 6. Day-2 toggling & the environment configs

The `template.metadata.labels` (`adhar.io/package-name`, `adhar.io/category`, `environment`) are stamped onto every generated Application so day-2 tooling can select by package. Runtime enable/disable **patches the affected list element's `enabled` value in Git** (Gitea `adhar/environments` + `adhar/packages`) rather than re-applying the whole file — because `selfHeal: true` reverts any live kubectl edit. The operational rules (wave-by-wave enablement, verification, removal) live in [ADR-0014](../adr/0014-package-lifecycle-operations.md).

Each environment additionally carries the **same package schema** in `environments/<env>/config.yaml` under a top-level `packages:` list (`local`, `production`, plus `development`/`staging`/`testing`). This is the per-environment enablement source of record; `environments/local/config.yaml` mirrors `adhar-appset-local.yaml` element-for-element and says so in its header comment ("Regenerate from the appset when it changes; the parity tests enforce the match").

## 7. Testing — parity is the load-bearing invariant

[`parity_test.go`](../../platform/controllers/adharplatform/parity_test.go) is the guard that keeps the explicit list honest (pillar 4, [ADR-0015](../adr/0015-idp-critical-pillars.md)):

**`TestLocalProductionAppSetParity`** — loads both appsets' generator elements and asserts:

- The `wiring` sets are **equal** in both directions — a package wired locally but absent from production (or vice versa) fails. Same names, namespaces, categories, manifest paths.
- Every package `enabled` locally is also `enabled` in production — the curated local core is a strict **subset** of production, never a divergence.
- Every `enabled: "true"` element has a real manifests directory on disk (`os.Stat` of `stack/packages/<manifestPath>`).

**`TestEnvironmentConfigsMatchAppSets`** — for `{local, production}`, asserts the `environments/<env>/config.yaml` `packages:` list matches the appset element-for-element, *including* the `enabled` value (`want[wiring]==enabled`), and that the counts are equal (`appset wires N, config lists N`).

Both tests parse YAML directly (`sigs.k8s.io/yaml`) with no cluster — they run under plain `go test`. They cover only `local` + `production`; the `workload` and `gitops` appsets are **not** parity-tested (§8). Broader coverage: `TestAppSetFileForProvider` (in the controller suite) locks the provider→appset selection; the e2e bootstrap (`make e2e`) applies the local appset live and verifies the enabled Applications reach `Synced/Healthy`.

## 8. Drift & notes (as-built vs. ADR / CLAUDE.md)

- **Counts.** ADR-0004 and CLAUDE.md say "69 packages wired" and a curated local core of "~16"; the appsets actually wire **77** packages each and local enables **21** (`external-secrets, hubble, vault, buildpack, cnpg, jupyterhub, headlamp, keycloak, tekton, adhar-console, metrics-server, kube-prometheus, loki, alloy, tempo, pyroscope, mimir, k6, dapr, redis, kafka-operator`). Production enables **72** (all but `knative` and the CONFLICTS exclusions). ~86 package directories exist on disk. Treat the ADR's numbers as illustrative, not current.
- **Two undocumented appsets.** The ADR frames "one ApplicationSet per environment"; the repo has four files. `adhar-appset-workload.yaml` (matrix, thin agent, roadmap) and `adhar-appset-gitops.yaml` are additive to the model.
- **`adhar-appset-gitops.yaml` contradicts ADR-0011 and is stale.** It gives each package its own namespace (not the shared `adhar-system`), wires only 72 packages (missing `tekton`, `pyroscope`, `credential-rotation`, `policy-packs`, `jenkins-x`), still labels `environment: "local"` in its template, and is unreferenced by the controller (only `adhar gitops resolve`'s help text). It is neither parity-tested nor applied — effectively an unmaintained per-namespace variant that has drifted from the local/production pair.
- **Production is "everything except conflicts," not "everything."** `adhar-appset-production.yaml` disables `knative` (and other CONFLICTS.md exclusions) inline with a reason comment; it is not full enablement despite the "multi-node cloud cluster has the capacity" framing.
- **Retry budgets differ by environment** (not called out in the ADR): local `limit: 15`/`maxDuration: 1m`; production and gitops `limit: 30`/`maxDuration: 3m`.

## 9. Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/adhar-appset-local.yaml` | `helm-charts-local` — 77 wired, 21 enabled; applied for `kind`/unset |
| `platform/stack/adhar-appset-production.yaml` | `helm-charts-production` — 77 wired, 72 enabled; applied for cloud/on-prem |
| `platform/stack/adhar-appset-workload.yaml` | `adhar-workload-clusters` — matrix (clusters × thin agent list), roadmap P2.2 |
| `platform/stack/adhar-appset-gitops.yaml` | `helm-charts-gitops` — per-namespace variant; input to `adhar gitops resolve` only |
| `platform/stack/packages/<category>/<pkg>/{values.yaml,generate-manifests.sh,manifests/}` | one directory per package: chart values, the `helm template` renderer, and the checked-in rendered YAML the appset syncs |
| `platform/stack/packages/CONFLICTS.md` | shared-namespace object/env-var collisions → the mutual-exclusion rationale behind `enabled: false` and per-element `namespace` opt-outs |
| `platform/stack/environments/<env>/config.yaml` | per-environment `packages:` list — the enablement source of record, parity-checked against the appset |
| `platform/controllers/adharplatform/controller.go` | `applyPlatformStack` (reads + SSA-applies the selected appset + workload appset), `appSetFileForProvider` |
| `platform/controllers/adharplatform/parity_test.go` | `appSetElement`, `wiring`, `TestLocalProductionAppSetParity`, `TestEnvironmentConfigsMatchAppSets` |
| `platform/controllers/adharplatform/helpers.go` | `applyManifest` — SSA (`FieldManager="adhar"`, `ForceOwnership`) used to apply the appset idempotently |
| `cmd/gitops/resolve.go` | `adhar gitops resolve` — resolves `adhar://` refs in appset manifests (uses the gitops appset as its example) |
