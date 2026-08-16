# Low-Level Design — Embedded, pre-rendered manifests for bootstrap

Detailed design for [ADR-0006](../adr/0006-embedded-bootstrap-manifests.md). This is the authoritative as-built description of how Adhar ships its entire foundation *inside the binary*: manifests are pre-rendered at development time from pinned Helm charts (values under `hack/`), embedded via `//go:embed`, and applied at runtime with Server-Side Apply + `ForceOwnership`. It maps the real generation scripts, the embedded FS variables, the SSA apply path, the CRD installer, and the offline/idempotency guarantees this produces.

## 0. Context recap

Bootstrap installs Cilium, the Gateway, ArgoCD, Gitea, and Crossplane *before* any GitOps machinery exists — so it cannot rely on ArgoCD, chart repos, or the network being reachable. ADR-0006 fixes the boundary: **render once at dev time from pinned chart versions, ship the rendered YAML in the release artifact, apply it with SSA**. The binary is therefore a complete, self-contained, deterministic installer that works air-gapped (modulo container image pulls). The same philosophy extends down-stack — GitOps packages are pre-rendered ([ADR-0004](../adr/0004-applicationset-package-model.md)) and the Crossplane control plane ships as an embedded `configuration/` FS ([ADR-0005](../adr/0005-crossplane-v2-namespaced.md)).

## 1. Invariants

- **INV-1 — Nothing is fetched at runtime.** Every foundation manifest is compiled into the binary via `//go:embed`; the reconcile path reads bytes from an `embed.FS`, never from a chart repo, registry index, or `https://`.
- **INV-2 — Rendered at dev time from pinned versions.** Each `hack/<component>/generate-manifests.sh` pins an exact chart/release version and `helm template`s it into `resources/<component>/`. A given commit therefore always embeds byte-identical manifests.
- **INV-3 — Idempotent SSA.** All embedded content is applied with `client.Apply`, `FieldOwner("adhar")`, `ForceOwnership` — re-runs converge, and drift is attributable to a field owner.
- **INV-4 — Upgrades are release-coupled.** Bumping Cilium/ArgoCD/Gitea/Crossplane means re-running the `hack/` scripts, committing the regenerated YAML, and cutting a release — a deliberate trade for determinism (ADR ⚠️).
- **INV-5 — Platform CRDs embed too.** The `platform.adhar.io` API group's own CRDs are embedded and installed by the CLI before any CR is created.

## 2. The two-stage pipeline: generate (dev-time) → embed → apply (runtime)

```
 ┌─ DEV TIME (hack/embedded-resources.sh) ────────────────┐   ┌─ COMPILE ─────┐   ┌─ RUNTIME (reconcile) ──────┐
 │ helm repo add/update <chart>                           │   │  //go:embed   │   │ fs.ReadFile / ConvertFS...  │
 │ helm template … --version <PINNED> -f hack/../values → │ → │  resources/…  │ → │ applyManifest (SSA+Force)   │
 │   platform/controllers/adharplatform/resources/<c>/*   │   │  embed.FS var │   │ owner refs on ns objects    │
 └────────────────────────────────────────────────────────┘   └───────────────┘   └────────────────────────────┘
```

Nothing between "commit" and "run" re-renders anything. `make embedded-resources` (Makefile, depends on `kustomize helm`) runs `hack/embedded-resources.sh`, which iterates `argocd cilium crossplane gateway-api gitea` and calls each `hack/<dir>/generate-manifests.sh`.

## 3. Dev-time generation (`hack/`)

Each script anchors its output to `$(dirname $0)/../../platform/controllers/adharplatform/resources/<component>` (an absolute path — a relative path once spawned a stray `hack/argocd/platform/...` copy) and pins a version:

| Component | Script | Source | Pinned version | Outputs (under `resources/<c>/`) |
|---|---|---|---|---|
| ArgoCD | `hack/argocd/generate-manifests.sh` | `argo/argo-cd` Helm chart, `--include-crds` | `10.3.3` | `install.yaml`, `install-ha.yaml` (from `values.yaml` / `values-ha.yaml`) |
| Cilium | `hack/cilium/generate-manifests.sh` | `cilium/cilium` Helm chart, `--include-crds` | `1.20.0` | `install.yaml` |
| Crossplane | `hack/crossplane/generate-manifests.sh` | `crossplane/crossplane` Helm chart, `--include-crds` | `v2.3.1` | `install.yaml`, `install-ha.yaml` |
| Gitea | `hack/gitea/generate-manifests.sh` | `gitea-charts/gitea` Helm chart | `12.7.0` (arg-overridable) | `install.yaml`, `install-ha.yaml` |
| Gateway API | `hack/gateway-api/generate-manifests.sh` | GitHub release `experimental-install.yaml` | `v1.6.1` | `crds.yaml` |

Notes that matter for as-built behaviour:

- **HA variants are separate pre-renders**, not runtime toggles. `values.yaml` → `install.yaml` (single-replica), `values-ha.yaml` → `install-ha.yaml` (replicas/PDBs/HA redis / replicated DB). The reconciler *selects the file* by `Spec.BuildCustomization.EnableHAMode` (§5) — it does not template replica counts.
- **Gateway API uses the *experimental* channel, not standard.** Cilium's Gateway controller indexes `TLSRoute` at `gateway.networking.k8s.io/v1alpha2`, which the standard channel ships but serves `false`; the experimental bundle avoids the `cilium-operator` crash `no matches for kind "TLSRoute"`. The script also strips the release's `safe-upgrades` `ValidatingAdmissionPolicy` (+ binding) with an inline `python3` doc-filter, since it blocks applying experimental CRDs over standard ones. `GATEWAY_API_VERSION` is kept in sync with the Cilium version by hand.
- **`gateway/`, `gateway-api` CRDs, `gateway/gateway.yaml`, `*/post-install.yaml`, `cnpg/gitea-db.yaml`** are **hand-authored** manifests, not chart output — they live in the tree directly and are not (re)generated by `embedded-resources.sh`. `hack/crossplane` produces only the *core* install; the control plane `configuration/` tree is embedded separately (`platform/controlplane/embed.go`, ADR-0005).

## 4. Embedded FS variables (`//go:embed`)

One `embed.FS` per component, declared beside its reconciler. Verified directives:

| Var | Directive | File | Sizes (as-built) |
|---|---|---|---|
| `argoCDFS` | `//go:embed resources/argocd` | `argocd.go` | `install.yaml` ~1.9 MB, `install-ha.yaml` ~2.0 MB, `post-install.yaml` 2.4 KB |
| `ciliumFS` | `//go:embed resources/cilium` | `cilium.go` | `install.yaml` ~108 KB, `post-install.yaml` 206 B |
| `cnpgFS` | `//go:embed resources/cnpg` | `cnpg.go` | `install.yaml` ~1.2 MB, `gitea-db.yaml` 2.1 KB |
| `crossplaneFS` | `//go:embed resources/crossplane` | `crossplane.go` | `install.yaml` ~29 KB, `install-ha.yaml` ~30 KB (core only) |
| `gatewayFS` | `//go:embed resources/gateway-api` + `//go:embed resources/gateway` | `gateway.go` | `gateway-api/crds.yaml` ~1.4 MB; `gateway/{gateway,gateway-cloud}.yaml` ~2.5 KB |
| `giteaFS` | `//go:embed resources/gitea` | `gitea.go` | `install.yaml` ~46 KB, `install-ha.yaml` ~147 KB, `post-install.yaml` 6 KB |
| `crdFS` | `//go:embed resources/*.yaml` | `controllers/crd.go` | the 3 platform CRDs (§7) |
| `managerFS` | `//go:embed resources/manager/*.yaml` | `controllers/manager.go` | in-cluster manager Deployment + RBAC |
| `ConfigurationFS` | `//go:embed all:configuration` | `platform/controlplane/embed.go` | Crossplane control-plane tree (ADR-0005) |

The `argoCDFS`/`giteaFS` component FSes are consumed two ways: directly via `argoCDFS.ReadFile(path)` in the reconciler, and via exported `RawArgocdInstallResources`/`RawGiteaInstallResources` → `k8s.BuildCustomizedManifests(config.FilePath, "resources/<c>", <fs>, scheme, templateData)`, which renders the tree and layers any user override file on top (§6).

## 5. Runtime application — per-component reconcilers

Each `Reconcile<Component>` reads the right embedded file(s) and hands the bytes to `applyManifest`. Pattern (from `ReconcileArgo`, `argocd.go`):

```go
argocdManifestPath := "resources/argocd/install.yaml"
if resource.Spec.BuildCustomization.EnableHAMode {
    argocdManifestPath = "resources/argocd/install-ha.yaml"   // separate pre-render, not a toggle
}
manifestBytes, err := argoCDFS.ReadFile(argocdManifestPath)   // INV-1: bytes from the binary
...
if err := r.applyManifest(ctx, manifestBytes, resource, "ArgoCD install"); err != nil { ... }
// then resources/argocd/post-install.yaml (adhar-controller ClusterRole/RBAC), same path
resource.Status.ArgoCD.Available = true
```

| Reconciler (file) | Reads | Runtime transform before apply |
|---|---|---|
| `ReconcileArgo` (`argocd.go`) | `install.yaml`\|`install-ha.yaml` + `post-install.yaml` | none |
| `ReconcileCilium` (`cilium.go`) | `install.yaml` + `post-install.yaml` | `rewriteCiliumAPIEndpoint` rewrites `k8sServiceHost`/`k8sServicePort` in the embedded bytes to the live API endpoint (Cilium replaces kube-proxy, so it needs a reachable apiserver address) |
| `ReconcileGatewayAPICRDs` / `ReconcileGateway` (`gateway.go`) | `gateway-api/crds.yaml`; then `gateway/gateway.yaml` (Kind) or `gateway-cloud.yaml` | `gateway-cloud.yaml` is templated (`{{ .Host }}` in the listener hostname); Kind path pins the generated Service node ports |
| `ReconcileGitea` (`gitea.go`) | `install.yaml`\|`install-ha.yaml` + `post-install.yaml` | none |
| `ReconcileCNPG` (`cnpg.go`, HA only) | `install.yaml` (CNPG operator) + `gitea-db.yaml` | none; waits `clusters.postgresql.cnpg.io` CRD + operator ready (`cnpgReadyTimeout = 3m`) |
| `ReconcileCrossplane` (`crossplane.go`) | `install.yaml`\|`install-ha.yaml` (core), then the embedded `configuration/` tree | selected by `EnableHAMode`; control-plane apply per ADR-0005 |

These reconcilers run in the fixed foundation order (`installCorePackagesSync`, ADR-0001 §5): Gateway API CRDs → Cilium → Gateway → [CNPG if HA] → ArgoCD → Gitea → Crossplane. Ordering is a bootstrap concern (ADR-0001); embedding is what makes each step's input deterministic and offline.

## 6. The SSA apply path (`applyManifest`, `helpers.go`)

`applyManifest(ctx, manifestBytes, resource, name)` is the single choke point through which all embedded foundation YAML reaches the cluster. It:

1. **Decodes** multi-doc YAML with `k8syaml.NewYAMLOrJSONDecoder` (streaming; skips empty docs).
2. **Resolves scope** per object via the `RESTMapper` (`RESTScopeNameRoot` ⇒ cluster-scoped). When the mapper can't resolve a GVK yet (its CRD may not be registered this pass), it falls back to a hard-coded `knownClusterScopedKinds` set — `Namespace`, `ClusterRole(Binding)`, `CustomResourceDefinition`, `Mutating`/`ValidatingWebhookConfiguration`.
3. **Sets a controller owner reference** *only* on namespaced objects that land in the platform namespace (`obj.GetNamespace() == resource.Namespace`, defaulting an empty namespace to `resource.Namespace`). Cluster-scoped and cross-namespace objects get **no** owner ref (avoids illegal cluster→namespace ownership and cross-namespace refs).
4. **Applies** with force ownership:

```go
r.Patch(ctx, obj, client.Apply,
    client.FieldOwner(v1alpha1.FieldManager),   // "adhar"  (api/v1alpha1/adharplatform_types.go)
    client.ForceOwnership)
```

Errors are accumulated per-doc and returned aggregated (`encountered N errors applying <name> manifest`), so one bad doc doesn't silently drop the rest, and the outer reconcile requeues. `FieldManager = "adhar"` (INV-3) is the field owner that makes re-runs idempotent and any post-apply drift attributable. `EnsureControllerManager` (`manager.go`) uses the identical `client.Apply`/`FieldOwner`/`ForceOwnership` pattern for the in-cluster manager Deployment.

### Templating layer (`fs.ConvertFSToBytes` / `k8s.BuildCustomizedManifests`)

Where a manifest carries `{{ … }}` placeholders (`gateway-cloud.yaml`, the manager manifests), the exported `Raw*Resources` path renders through `fs.ConvertFSToBytes(fs, dir, templateData)` → `files.ApplyTemplate` per file, then optionally `applyOverrides(config.FilePath, …)` merges a user-supplied override file (`k8s.ConvertYamlToObjectsWithOverride`). The install/post-install manifests read directly via `ReadFile` are **not** templated — they are fully-rendered chart output; the only mutation is `rewriteCiliumAPIEndpoint`'s byte substitution. This is the sole user-facing customization seam (ADR ⚠️ "user knobs go through the CR / override file, not chart edits").

## 7. Embedded platform CRDs (`platform/controllers/crd.go`)

The platform's own API group is embedded the same way — `//go:embed resources/*.yaml` (`crdFS`) over `platform/controllers/resources/`:

- `platform.adhar.io_adharplatforms.yaml`, `platform.adhar.io_custompackages.yaml`, `platform.adhar.io_gitrepositories.yaml` (controller-gen output; regenerated by `make manifests`, not `embedded-resources.sh`).

`EnsureCRDs` → `getK8sResources` renders the FS with `fs.ConvertFSToBytes` and `k8s.ConvertRawResourcesToObjects`, then `EnsureCRD` **creates-or-updates each CRD** (Get → `IsNotFound` ⇒ Create, else copy `ResourceVersion` + Update) and **blocks in a poll loop until `Established=True`** (500 ms tick). This runs on the CLI host before any `AdharPlatform` CR is written (ADR-0001 §2.1 stage 2), so the API server can admit the CR the controller then reconciles. Note this path uses Create/Update rather than the SSA `applyManifest` used for foundation workloads.

## 8. Guarantees this buys (ADR consequences, verified in code)

- **Deterministic & reproducible** — a binary embeds byte-identical YAML from pinned versions; two `adhar up` runs from the same commit install the same foundation (INV-1/2).
- **Offline / air-gapped** — no chart repo, registry index, or GitHub in the reconcile critical path; only container images still need a registry or preload (Helm access is a *dev-time* concern in `hack/`).
- **Idempotent re-runs** — SSA + `ForceOwnership` under one field manager means `installCorePackagesSync`'s "re-run whenever any component isn't Available" guard is cheap and safe; partial failures self-heal without a flag day.
- **Release-coupled upgrades (⚠️)** — the price of determinism: version bumps are a `hack/` regenerate + commit + release, a platform-developer op, not a user op.
- **Binary size (⚠️)** — the embedded foundation is dominated by ArgoCD (~4 MB across both variants), Gateway API CRDs (~1.4 MB), CNPG (~1.2 MB), Cilium (~108 KB); distribution is via compressed archives (GoReleaser), so on-wire cost is far lower.

## 9. Failure modes

- **Unregistered GVK on early pass** — `RESTMapper` can't map a CRD-defined kind before its CRD establishes; `applyManifest` falls back to `knownClusterScopedKinds` for the well-known cluster kinds and otherwise treats the object as namespaced. If an apply still fails (CRD truly absent), the per-doc error aggregates and the reconcile requeues after the CRD lands.
- **Stale embed after a `hack/` bump** — regenerating without committing, or editing `resources/` by hand, silently diverges the binary from the pinned chart; the scripts' version echo and the version-pinned filenames are the only guard (no automated drift check today — see §11).
- **Gateway API channel mismatch** — using the standard bundle reintroduces the `TLSRoute` crash; the experimental channel + `safe-upgrades` strip in `generate-manifests.sh` is load-bearing.
- **CRD never Established** — `EnsureCRD`'s poll loop has no timeout; it blocks until `Established` or a Get error, so a wedged apiserver surfaces as a hung bootstrap rather than a silent skip.

## 10. Testing

- **Unit / envtest** (`platform/controllers/adharplatform/*_test.go`) — `TestAdharPlatformReconciler_ReconcileArgo`/`ReconcileGitea`/`ReconcileCilium`/`ReconcileGatewayAPICRDs` drive each installer under envtest, exercising the embedded-read → `applyManifest` SSA path (CRD path `resources/argocd/install.yaml`, metrics off); `ha_test.go` asserts the `install-ha.yaml` selection + CNPG bootstrap invariants.
- **Manager** (`manager_test.go`) — covers `EnsureControllerManager` rendering + SSA of `resources/manager/manager.yaml`.
- **e2e** (`tests/e2e/bootstrap`, `make e2e`) — a full `adhar up` proves the embedded foundation applies cleanly on a fresh Kind node with no chart repos configured; because embed guarantees offline, this is the real regression net for the whole pattern.
- **To add** — a `make embedded-resources` + `git diff --exit-code` CI check (regenerate must be a no-op against the committed tree) would close the stale-embed gap (§9).

## 11. Code & file map

| Path | Responsibility |
|---|---|
| `hack/embedded-resources.sh` | driver: loops `argocd cilium crossplane gateway-api gitea` → each `generate-manifests.sh` |
| `hack/<c>/generate-manifests.sh` | `helm template`/`curl` a **pinned** version → `resources/<c>/*.yaml` (anchored to script dir) |
| `hack/<c>/{values,values-ha}.yaml` | chart values that produce `install.yaml` / `install-ha.yaml` |
| `Makefile` (`embedded-resources`) | `make embedded-resources` (deps `kustomize helm`) → runs the driver |
| `platform/controllers/adharplatform/{argocd,cilium,cnpg,crossplane,gateway,gitea}.go` | `embed.FS` var + `Reconcile<C>`: `ReadFile` embedded manifest → `applyManifest` |
| `platform/controllers/adharplatform/resources/{argocd,cilium,cnpg,crossplane,gateway,gateway-api,gitea}/` | the embedded pre-rendered foundation YAML |
| `platform/controllers/adharplatform/helpers.go` | `applyManifest` — SSA (`client.Apply`, `FieldOwner("adhar")`, `ForceOwnership`), scope resolution, owner-ref rules |
| `platform/controllers/crd.go` | `//go:embed resources/*.yaml` (`crdFS`); `EnsureCRDs`/`EnsureCRD` — install platform CRDs, wait for `Established` |
| `platform/controllers/manager.go` | `//go:embed resources/manager/*.yaml` (`managerFS`); `EnsureControllerManager` — same SSA pattern for the in-cluster Deployment |
| `platform/controllers/resources/{*.yaml,manager/manager.yaml}` | embedded platform CRDs + controller-manager manifests |
| `platform/k8s/util.go` | `BuildCustomizedManifests`/`BuildCustomizedObjects` — render embedded FS + layer user override file |
| `platform/utils/fs/fs.go` | `ConvertFSToBytes` — read an embedded dir, `ApplyTemplate` per file |
| `api/v1alpha1/adharplatform_types.go` | `FieldManager = "adhar"`, `BuildCustomizationSpec.EnableHAMode`, `PackageCustomization.FilePath` |
| `platform/controlplane/embed.go` | `//go:embed all:configuration` → `ConfigurationFS` (control-plane tree, ADR-0005) |
```
