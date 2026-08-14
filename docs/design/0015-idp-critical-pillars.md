# Low-Level Design — The eight critical IDP pillars and the tests that enforce them

Detailed design for [ADR-0015](../adr/0015-idp-critical-pillars.md). This is the authoritative as-built description of how Adhar's eight pillars are turned from prose into **executable invariants**: which unit/parity/envtest and e2e test guards each pillar, what exact assertion it makes, and where the guarded code lives. The pillars are a review rubric ("this violates pillar 4"); this document is the machine-checkable half of that rubric.

## 0. Context recap

ADR-0015 states that every feature/package/change is tested against eight ordered pillars (one-command bootstrap; Git as the only write path; self-service with guardrails; local–production parity; secure-by-default; observable-by-construction; day-2-by-design; 100% open source). Silence-is-not-an-option: a violation needs its own ADR. Prose criteria erode, so the load-bearing ones are backed by tests that fail CI when the invariant drifts. The enforcement lives in three places:

- **Parity + manifest-invariant unit tests** — `platform/controllers/adharplatform/{parity_test.go,ha_test.go,argocd_test.go}` (fast, no cluster; run under `make test`).
- **Reconcile envtest** — the same package's `*_test.go` drive the installers against a real API server (CRD path `resources/argocd/install.yaml`, metrics off).
- **End-to-end** — `tests/e2e/bootstrap/bootstrap_test.go` (`make e2e`): a full `adhar up --recreate` → verify → `adhar down` on Kind, phased to mirror the bootstrap sequence.

## 1. Pillar → enforcing test map

| # | Pillar | Primary guard(s) | Assertion in one line |
|---|---|---|---|
| 1 | One command, whole platform | e2e `Test_FullBootstrapSequence` (Phase 1) | `adhar up --recreate` with zero manual steps drives the CR to aggregate `Ready=True` |
| 2 | Git is the only write path | e2e Phase 3 + `TestEnvironmentConfigsMatchAppSets` | Gitea serves the seeded `packages`/`environments` repos; every appset entry is mirrored 1:1 in the env config in Git |
| 3 | Self-service with guardrails | Crossplane XRD corpus (ADR-0005) + `TestGetK8sInstallResources` | namespaced XR APIs (23 XRDs) are the provisioning path; bootstrap installers decode both scheme and unstructured (Gateway API) kinds |
| 4 | Local–production parity is sacred | `TestLocalProductionAppSetParity`, `TestEnvironmentConfigsMatchAppSets`, `TestAppSetFileForProvider`, `Test{ArgoCD,Gitea}HAManifestInvariants` | local and production appsets wire the identical package set, differing only in `enabled`; HA is same-chart scale-up |
| 5 | Secure by default, not by add-on | `TestArgoCDHAManifestInvariants` (oidc parity) + e2e Phase 2/3 (SPIRE, gitea token) | both ArgoCD variants keep the Keycloak `oidc.config`; SPIRE server ships in the foundation; no secrets in Git |
| 6 | Observable by construction | e2e Phase 5 (`alloy` health probe) + Phase 2 (argocd-cm CNPG health) | enabling a package makes it converge in the standard pipeline; ArgoCD carries the fast CNPG health check |
| 7 | Day-2 is designed, not discovered | `TestGiteaHAManifestInvariants`, `TestCNPGBootstrapManifests`, e2e Phase 6 (`adhar get status`) | HA/backup DB path is pre-rendered and CNPG-backed; the CLI surfaces package + condition dashboards |
| 8 | 100% open source, no lock-in | `TestGatewayCloudManifest` (portable Gateway API seam) | the edge is Gateway API + cert-manager, provider-swappable; no proprietary listener |

Pillars 1/2/4/5/7 have direct, dedicated tests in the studied files; 3/6/8 are covered obliquely here and directly by their own ADRs' tests (0005, 0010, 0002). The sections below give the exact code path per pillar.

## 2. Pillar 4 — parity is a structural invariant (`parity_test.go`)

Parity is the most heavily tested pillar because it is the cheapest to violate silently: someone enables a package locally that production never got, or edits an appset without touching the environment config. Three unit tests plus two HA invariants nail it down. The data model each test parses:

```go
type appSetElement struct {
    Name, Enabled, Namespace, Category, ManifestPath string // json tags: name/enabled/namespace/category/manifestPath
}
// wiring() is the identity minus enablement — the parity key.
func wiring(e appSetElement) [4]string { return [4]string{e.Name, e.Namespace, e.Category, e.ManifestPath} }
```

`loadAppSetElements` reads `platform/stack/adhar-appset-{local,production}.yaml` and pulls `spec.generators[0].list.elements`; `loadEnvPackages` reads `platform/stack/environments/<env>/config.yaml`'s top-level `packages:` list (same shape).

### 2.1 `TestLocalProductionAppSetParity`

The core parity gate. Three assertions:

1. **Set equality on `wiring()`** — every `[name,namespace,category,manifestPath]` present locally must be present in production and vice-versa. A package can be *disabled* differently, but its *wiring* must be identical across topologies (pillar 4: "same manifests, same wiring, different values").
2. **Enablement is a subset** — every package with `enabled=="true"` locally must also be enabled in production (`prodEnabled[e.Name]`). The curated single-node core is a strict subset of production; local can never enable something production disables.
3. **Enabled ⇒ manifests on disk** — for every enabled element in either set, `platform/stack/packages/<ManifestPath>` must `os.Stat` as a directory. A package can't be enabled with no manifests behind it.

This is exactly the "can this be exercised in T1?" test from the ADR, made mechanical: nothing lands in production (T2/T3) that isn't wired identically into local (T1).

### 2.2 `TestEnvironmentConfigsMatchAppSets`

Guards pillar 2 as much as pillar 4: the ApplicationSet is what the controller applies, but the environment `config.yaml` in Git is the human-editable source of truth — they must not diverge. For `{local→appset-local, production→appset-production}` it builds `map[wiring]enabled` from both the appset and the env config and asserts they are element-for-element equal, including cardinality (`len(got) != len(want)` fails). If Git and the applied appset disagree, the "rebuild from Git alone" promise (pillar 2's test) is already broken.

### 2.3 `TestAppSetFileForProvider` (`argocd_test.go`)

Locks the provider→appset selection function `appSetFileForProvider` ([controller.go:378](../../platform/controllers/adharplatform/controller.go)):

```go
func appSetFileForProvider(provider v1alpha1.EnvironmentProvider) string {
    if provider == v1alpha1.ProviderKind || provider == "" { return "adhar-appset-local.yaml" }
    return "adhar-appset-production.yaml"
}
```

The test table asserts Kind/empty → `adhar-appset-local.yaml` and every cloud provider (AWS/GKE/Azure/DO/Civo/Custom) → `adhar-appset-production.yaml`. Because §2.1 already proves the two files are wiring-identical, this selection changes only *values/enablement*, never architecture (INV of pillar 4).

## 3. Pillar 4 + 7 — HA is same-chart scale-up, not a fork (`ha_test.go`)

The `EnableHAMode` gate ([gitea.go:33](../../platform/controllers/adharplatform/gitea.go), [argocd.go:39](../../platform/controllers/adharplatform/argocd.go)) swaps `resources/<c>/install.yaml` for `install-ha.yaml`. If HA were a hand-maintained fork it would drift from the base (different chart version, dropped SSO) — violating parity. Four tests guard it:

### 3.1 `TestArgoCDHAManifestInvariants`

Reads both variants from `argoCDFS` (`//go:embed resources/argocd`) and asserts:

- HA contains `kind: PodDisruptionBudget` and scales beyond one replica (`replicas: 2` or `3`) — the *day-2 resilience* half (pillar 7).
- **Same chart version** — `chartLine()` extracts the first `helm.sh/chart:` label from each; base and HA must match, or "an HA toggle silently up/downgrades ArgoCD." This is the parity guard: HA is the *same* ArgoCD, scaled.
- **SSO parity (pillar 5)** — *both* `install.yaml` and `install-ha.yaml` must contain `oidc.config: |`. An HA toggle must never silently drop the Keycloak OIDC wiring; secure-by-default survives every rendering.

### 3.2 `TestGiteaHAManifestInvariants` (pillar 7 — day-2 data durability)

From `giteaFS`: HA must contain `PodDisruptionBudget`, must point Gitea's DB at the CNPG service `HOST=gitea-db-rw.adhar-system.svc.cluster.local:5432`, and must **not** contain `gitea-postgresql` (the chart-bundled single-pod PostgreSQL). In HA the database is the replicated, backup-capable CNPG cluster — the "documented, exercised path" day-2 demands — and the two DB identities must not coexist.

### 3.3 `TestCNPGBootstrapManifests`

From `cnpgFS` (`//go:embed resources/cnpg`): `install.yaml` exists and `gitea-db.yaml` declares `kind: Cluster`, `name: gitea-db`, `instances: 2`, `name: gitea-db-credentials`. This is the operator + replicated cluster + credentials secret that §3.2 depends on — slotted into the foundation install order before Gitea when `EnableHAMode` is set (see [0001 §5](0001-management-cluster-first.md)).

## 4. Pillar 8 — the edge is a portable standard (`TestGatewayCloudManifest`)

Lock-in is tested at the seam most prone to it: the ingress edge. `TestGatewayCloudManifest` renders `resources/gateway/gateway-cloud.yaml` through `files.ApplyTemplate` with `BuildCustomizationSpec{Host:"example.com"}` and asserts the rendered result is portable Gateway API, not a proprietary controller:

- `type: LoadBalancer`, `hostname: "*.example.com"`, `cert-manager.io/cluster-issuer: adhar-selfsigned`, `name: adhar-gateway` — all present.
- `port: 8443` — **absent**: the Kind-specific NodePort listener must not leak into the cloud manifest.

The gateway is `gateway.networking.k8s.io` + cert-manager throughout — any conformant Gateway controller can replace Cilium's implementation at this seam (pillar 8's "replaced at a seam" test). The Kind vs. cloud split (`gateway.yaml` vs. `gateway-cloud.yaml`) is again values-only, reinforcing pillar 4.

## 5. Pillar 3 — self-service surface decodes cleanly (`argocd_test.go` envtest)

`TestGetRawInstallResources` and `TestGetK8sInstallResources` drive `EmbeddedInstallation` over `resources/argocd`. The second asserts the decoded object set contains **both** a scheme-registered kind (`Deployment`) *and* an unstructured-fallback kind (`HTTPRoute`, a Gateway API CRD not in client-go's scheme). This matters for pillar 3/1: the bootstrap installer (`installResources` → `applyManifest`, SSA `FieldManager=v1alpha1.FieldManager`, `ForceOwnership`) must handle arbitrary CRD-backed APIs, which is exactly what the self-service Crossplane XR layer (ADR-0005, 23 namespaced XRDs) and the Gateway API layer rely on. `TestArgoCDAppAnnotation` covers `requestArgoCDAppRefresh` — the refresh nudge issued at handoff — proving appset-owned Applications are left alone (owner-ref `Kind: ApplicationSet` ⇒ no patch) while standalone apps are refreshed. The reconcile-level `TestAdharPlatformReconciler_ReconcileArgo` asserts `ReconcileArgo` drives `Status.ArgoCD.Available=true` under a fake client.

## 6. Pillars 1/2/5/6/7 — the e2e phases (`bootstrap_test.go`)

`Test_FullBootstrapSequence` (`//go:build e2e`) is the whole-platform proof. Knobs: `ADHAR_E2E_SKIP_UP=1` (verify a running platform), `ADHAR_E2E_KEEP=1` (leave it up); budgets `ADHAR_E2E_{TIMEOUT,UP_TIMEOUT,HEALTH_TIMEOUT}`. It runs `e2e.RunAdhar(ctx, upTimeout, "up", "--recreate")` then six phased subtests, each mapping to a pillar:

| Phase | Subtest | Pillar | Key assertion |
|---|---|---|---|
| 1 | platform reaches Ready condition | **1** | `e2e.WaitForPlatformReady` — the CR's aggregate `Ready` condition goes True with zero manual steps |
| 2 | foundation components are up | 1/5/6 | `argo-cd-argocd-server` + `gitea` Deployments Available; `cilium-gateway-adhar-gateway` Service has pinned node ports (`80→30080`, `443→30443`); `GitRepository`/`CustomPackage` CRDs serve; **`spire-server` StatefulSet present** (SPIFFE identity in the foundation, pillar 5); **`argocd-cm` carries `resource.customizations.health.postgresql.cnpg.io_Cluster`** (fast CNPG health, pillar 6) |
| 3 | gitea serves the seeded platform repos | **2** | `environments` and `packages` repos exist via the Gateway-routed URL; `gitea-credential` secret carries an API `token` (no secret in Git, pillar 5) |
| 4 | argocd api authenticates | 1/5 | `e2e.ArgoCDSessionToken` succeeds through the Gateway with bootstrap creds |
| 5 | applicationset deploys enabled packages | **2/6** | `ApplicationSet` `helm-charts-local` exists, generated Applications are non-empty, and the health probes converge (`WaitForAppsHealthy`) |
| 6 | cli reports platform status | **7** | `adhar get status` prints "Platform Packages" and (fresh up) "Platform Conditions" dashboards |

### 6.1 Probe set follows the curated core (pillar 6, drift-proof)

Phase 5's probes are not hardcoded. `resolveHealthProbes` intersects `healthProbeCandidates` (`external-secrets`, `cert-manager`, `metrics-server`, `hubble`, `kyverno`, `valkey`, `alloy`) with what `adhar-appset-local.yaml` actually enables (`enabled=="true"`), and `require.NotEmpty` fails loudly if the intersection is empty. So the "does Grafana already see it?" pillar-6 test rides the same single source of truth (`platform/stack/adhar-appset-local.yaml`) that pillar 4's parity tests assert against — enabling `alloy` in the appset is what makes the observability probe meaningful, and disabling every observability package would break the test rather than silently pass.

### 6.2 Pillar 2's rebuild test, mechanized

Pillar 2's stated test is "could the cluster be deleted and rebuilt from Git alone?" `--recreate` in Phase 1 literally destroys and rebuilds the cluster; Phase 3 then proves the rebuilt state came from the seeded Git repos, and `TestEnvironmentConfigsMatchAppSets` (§2.2) proves those repos' env configs are the applied truth. The two together are the rebuild drill in miniature.

## 7. How a new package is reviewed (the pillars as a gate)

The tests turn each pillar into a merge-blocking check for any package added under `platform/stack/packages/`:

1. Add the element to **both** `adhar-appset-local.yaml` and `adhar-appset-production.yaml` with identical `wiring()` → or `TestLocalProductionAppSetParity` fails (pillar 4).
2. Mirror it in the matching `environments/<env>/config.yaml` → or `TestEnvironmentConfigsMatchAppSets` fails (pillars 2, 4).
3. If enabled anywhere, ship real manifests at `manifestPath` → or the enabled-⇒-on-disk check fails (pillar 4).
4. Keep local enablement a subset of production → or the subset check fails (pillar 4).
5. If it has an HA story, render `install-ha.yaml` from the same chart with PDBs + SSO intact → or `ha_test.go` fails (pillars 4, 5, 7).
6. If it exposes an edge, use Gateway API + cert-manager, no NodePort leak into cloud → or `TestGatewayCloudManifest` fails (pillar 8).

A package that can't satisfy these needs an ADR arguing the exception — which is exactly the ADR-0015 escape valve made concrete.

## 8. Code & file map

| Path | Responsibility (pillar) |
|---|---|
| `docs/adr/0015-idp-critical-pillars.md` | the eight pillars + their prose tests |
| `platform/controllers/adharplatform/parity_test.go` | `TestLocalProductionAppSetParity`, `TestEnvironmentConfigsMatchAppSets`, `loadAppSetElements`/`loadEnvPackages`/`wiring` (pillars 2, 4) |
| `platform/controllers/adharplatform/ha_test.go` | `Test{ArgoCD,Gitea}HAManifestInvariants`, `TestCNPGBootstrapManifests`, `TestGatewayCloudManifest`, `TestAppSetFileForProvider`, `chartLine` (pillars 4, 5, 7, 8) |
| `platform/controllers/adharplatform/argocd_test.go` | `TestGet{Raw,K8s}InstallResources`, `TestArgoCDAppAnnotation`, `TestAdharPlatformReconciler_ReconcileArgo` (pillars 1, 3) |
| `tests/e2e/bootstrap/bootstrap_test.go` | `Test_FullBootstrapSequence` (6 phased subtests), `resolveHealthProbes` (pillars 1, 2, 5, 6, 7) |
| `tests/e2e/e2e.go` | shared consts/helpers: `ArgoCDServerDeployment`, `GiteaDeployment`, `GatewayService`, `PlatformAppSet="helm-charts-local"`, `GatewayHTTPNodePort=30080`, `GiteaCredentialSecret`, `WaitForPlatformReady`, `WaitForAppsHealthy`, `ArgoCDSessionToken`, `GiteaListRepoNames` |
| `platform/controllers/adharplatform/controller.go` | `appSetFileForProvider` (provider→appset selection, pillar 4) |
| `platform/controllers/adharplatform/{argocd,gitea}.go` | `EnableHAMode` install/install-ha selection |
| `platform/stack/adhar-appset-{local,production}.yaml` | the two wiring-identical ApplicationSets under test |
| `platform/stack/environments/{local,production}/config.yaml` | the in-Git env configs asserted to mirror the appsets |
| `platform/controllers/adharplatform/resources/{argocd,gitea,cnpg,gateway}/` | embedded manifests the invariant tests read (`install.yaml`/`install-ha.yaml`, `cnpg/gitea-db.yaml`, `gateway-cloud.yaml`) |

## 9. Drift & notes (as-built vs. ADR)

- **Not every pillar has a first-class test.** Pillars 1/2/4/5/7 are directly and repeatedly guarded here. Pillar 3 (self-service) is enforced by the Crossplane XRD corpus (ADR-0005 tests) and only obliquely here via the unstructured-decode test; pillar 6 (observable) rides the `alloy` health probe rather than asserting Grafana wiring; pillar 8 (open source) is spot-checked at the Gateway seam only. There is no single "pillar test" file — enforcement is distributed across parity/HA/argocd unit tests and the e2e phases, which is why this doc's §1 table is the map the ADR asks reviewers to use.
- **Parity is stricter than the ADR text.** ADR-0015 pillar 4 says "same manifests … different values"; the tests go further and forbid *any* wiring divergence and require local enablement to be a strict subset of production — a stronger, mechanically-checked form of the pillar.
- **The e2e budget reflects reality, not the ADR's "under 10 minutes."** A cold `adhar up` pulls the whole curated core (Keycloak, Harbor, kube-prometheus, Vault, SPIRE); the e2e up budget defaults to 60m. Pillar 1's "one command, no manual steps" holds; the vision's 10-minute figure assumes a warm image cache.
- **`PlatformAppSet` is `helm-charts-local`**, not a name derived from the file `adhar-appset-local.yaml` — the e2e Phase 5 lookup keys on the ApplicationSet's `metadata.name`, worth knowing when renaming appsets.
