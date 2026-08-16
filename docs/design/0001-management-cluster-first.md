# Low-Level Design — Management-cluster-first with two-phase bootstrap

Detailed design for [ADR-0001](../adr/0001-management-cluster-first.md). This is the authoritative as-built description of how `adhar up` brings a management cluster into existence: the imperative **bootstrap phase** (strictly ordered, embedded, Server-Side Applied foundation) and the handoff to the declarative **GitOps phase** (Gitea + an ArgoCD ApplicationSet). It maps the real CLI entry points, the `AdharPlatform` reconcile loop, the per-component reconcilers, the GitOps seeding, and the two topology postures (local `ExitOnSync` vs. in-cluster continuous).

## 0. Context recap

An IDP needs one cluster that owns platform state and provisions/governs the rest, and that cluster cannot bootstrap itself purely via GitOps (ArgoCD cannot install the CNI it runs on; the Git server it syncs from does not yet exist). ADR-0001 fixes the boundary at **foundation vs. everything else**: the CLI/controller imperatively installs a minimal ordered foundation, seeds Gitea, and hands all further change to an ApplicationSet. The same code path scales down to a single Kind node (roles collapse, controllers exit) and up to a cloud management cluster (controllers run in-cluster continuously).

## 1. Invariants

- **INV-1** The foundation is installed in a **fixed deterministic order**; each step is idempotent Server-Side Apply with `FieldManager = "adhar"` and `ForceOwnership`.
- **INV-2** Everything is embedded (`//go:embed`) — the bootstrap is offline-capable; no manifest is fetched at runtime.
- **INV-3** After Gitea is seeded, Git is the only write path; the controller applies exactly one ApplicationSet and then stops touching workloads.
- **INV-4** Local and production differ only in **size** (`EnableHAMode`, provider) and **controller placement** (in-process-and-exit vs. in-cluster Deployment), never in architecture.
- **INV-5** The `AdharPlatform` CR is the single source of bootstrap truth; its `.status` gates every phase and every re-run adopts existing state.

## 2. CLI entry points (`cmd/up/`)

`UpCmd.RunE = create` ([up.go](../../cmd/up/up.go)). `create` dispatches on the `--file/-f` flag:

```go
// cmd/up/up.go
func create(cmd *cobra.Command, args []string) error {
    ctx, ctxCancel := context.WithCancel(cmd.Context())
    defer ctxCancel()
    if configFile != "" {
        return createProductionCluster(ctx, cmd, args, ctxCancel)   // production.go
    }
    return createLocalDevelopmentCluster(ctx, cmd, args, ctxCancel) // local.go
}
```

| Mode | Entry | Cluster creation | Controller posture |
|---|---|---|---|
| Local (default) | `createLocalDevelopmentCluster` → `LocalProvisioner.Provision` ([local.go](../../cmd/up/local.go)) | `kind.NewCluster().Reconcile()` | in-process manager, `ExitOnSync=true`, exits on convergence; optional `--in-cluster` installs the Deployment afterwards |
| Production (`-f`) | `createProductionCluster` → `providerManager.ProvisionEnvironment` → `bootstrapPlatformOnCluster` ([production.go](../../cmd/up/production.go), [bootstrap.go](../../cmd/up/bootstrap.go)) | provider factory (`platform/providers/*`) | in-process bootstrap manager (`ExitOnSync=true`), then **always** `EnsureControllerManager` for continuous reconciliation |

Both paths converge on the identical primitive: install CRDs, generate TLS, start the controllers via `controllers.RunControllers`, and create one `AdharPlatform` CR. The CR — not the CLI — drives the foundation.

### 2.1 Local `Provision` sequence (`LocalProvisioner.Provision`)

A `helpers.StageTracker` renders the live checklist; stages 1–3 run on the CLI, stages 4–8 are owned by the controller and advanced by `pollPlatformStages` reading `AdharPlatform.Status`:

1. **Kind cluster** — `ReconcileKindCluster` builds the node from `platform/providers/kind/resources/kind.yaml.tmpl` (`disableDefaultCNI: true`, `kubeProxyMode: none` — Cilium replaces both; host ports `30080→8080`/`30443→8443`; `service-node-port-range: "8443-32767"`).
2. **Platform CRDs** — `ReconcileCRDs` → `controllers.EnsureCRDs` ([crd.go](../../platform/controllers/crd.go)) applies the embedded `AdharPlatform`, `GitRepository`, `CustomPackage` CRDs and blocks on each becoming `Established`.
3. **Networking** — `kind.SetupCoreDNS` (rewrite rules for `*.adhar.localtest.me`) + `kind.SetupSelfSignedCertificate` ([coredns.go](../../platform/providers/kind/coredns.go), [tls.go](../../platform/providers/kind/tls.go)); the PEM is stashed on `BuildCustomizationSpec.SelfSignedCert`.
4–8. **Controllers + CR** — `RunControllers` starts the manager, then `controllerutil.CreateOrUpdate` writes the `AdharPlatform` (`Provider: ProviderKind`, the `CliStartTimeAnnotation`, `Argo`/`EmbeddedArgoApplications` enabled). The CLI blocks on `managerExit`; the controller cancels the context on success (`ExitOnSync`).

### 2.2 Production `bootstrapPlatformOnCluster`

After the provider returns a `ProvisionResult`, the kubeconfig is materialised to a temp file and exported as `KUBECONFIG` (the GitOps seeding shells out to `kubectl`). Then: `EnsureCRDs` → `SetupSelfSignedCertificate` (initial Gateway TLS; cert-manager replaces it later) → resolve `platform/stack` on disk → start a bootstrap manager (`ExitOnSync=true`) → create the `AdharPlatform` (`Provider` mapped via `providerNameToEnvironmentProvider`, `Port: "443"`, `EnableHAMode` from config) → wait for shutdown → `EnsureControllerManager` installs the in-cluster `adhar-controller-manager` Deployment (image `ghcr.io/adhar-io/adhar:<version>`) so reconciliation continues after the CLI exits.

## 3. Controller wiring (`platform/controllers/`)

`RunControllers` ([run.go](../../platform/controllers/run.go)) registers three reconcilers on one manager, sharing a `utils.RepoMap` lock, and starts `mgr.Start(ctx)` in a goroutine that reports over `exitCh`:

```go
&adharplatform.AdharPlatformReconciler{Client, Scheme, ExitOnSync, CancelFunc, Config, TempDir, StackDir, RepoMap}
&gitrepository.GitRepositoryReconciler{...}
&custompackage.CustomPackageReconciler{...}
```

The `AdharPlatformReconciler` ([controller.go](../../platform/controllers/adharplatform/controller.go)) is the bootstrap driver. Key fields:

- `ExitOnSync bool` / `shouldShutdown bool` — the local "exit after convergence" behaviour; `CancelFunc` cancels the manager.
- `StackDir string` — absolute path to `platform/stack/` on the CLI host; **required** for GitOps seeding (the in-cluster manager runs with an empty `StackDir` and only reconciles an already-seeded platform).
- `Config v1alpha1.BuildCustomizationSpec`, `RepoMap *utils.RepoMap`, `lastFailureReason/Message` (surfaced on the `Ready` condition).

`SetupWithManager` watches only `AdharPlatform` (`For(&v1alpha1.AdharPlatform{})`); there is no `Owns`/`Watches` fan-out — the reconcile is a self-requeuing pipeline driven off `.status`.

## 4. Reconcile flow (`Reconcile`)

Every pass runs `defer r.postProcessReconcile(...)` (persists status + conditions with `RetryOnConflict`, and — when `shouldShutdown` — refreshes ArgoCD apps/appsets then calls `CancelFunc`). The body is a status-gated pipeline:

```
ReconcileProjectNamespace                                  (ensure adhar-<name> ns)
   │
   ├─ ExitOnSync && Crossplane.ControlPlaneApplied && isPlatformAlreadyDeployed → shouldShutdown, return
   │
   ├─ if any of ArgoCD/Gateway/Gitea/Crossplane.Available == false
   │        OR !Crossplane.ControlPlaneApplied
   │      → installCorePackagesSync()          (§5 — ordered foundation)   [fail → recordFailure, requeue 5s]
   │
   ├─ if !Gitea.RepositoriesCreated
   │      → applyPlatformStack()                (§6 — seed Gitea + repo auth + ApplicationSet) [fail → requeue 5s]
   │
   ├─ if !Crossplane.ControlPlaneApplied → requeue 15s   (control plane converges async in watch mode)
   │
   ├─ if ExitOnSync → shouldShutDown()?  yes → shouldShutdown, return ; no → requeue 10s
   │
   └─ if Config.StaticPassword → rotate ArgoCD & Gitea admin passwords to `developer`
      requeue 15s (steady poll; the in-cluster manager lives here)
```

Requeue constants: `defaultRequeueTime = 15s`, `errRequeueTime = 5s`. `installCorePackagesSync` is guarded so it re-runs whenever **any** component is not yet `Available` (idempotent SSA makes re-application cheap). The `ControlPlaneApplied` gate exists because the Crossplane kubernetes/helm `ClusterProviderConfig`s only apply once their provider CRDs register (a minute or two after the provider packages install) — without the explicit requeue the control plane never converges in watch mode.

### 4.1 Shutdown vs. continuous

- **`ExitOnSync=true`** (local default, and the production bootstrap manager): `shouldShutDown` returns true only once **all** of these hold: `Gitea.RepositoriesCreated`, `Crossplane.ControlPlaneApplied`, and `isPlatformAlreadyDeployed`. The last verifies the platform is genuinely usable, not merely installed: gitea + `argo-cd-argocd-server` + crossplane Deployments ready, **≥1 ApplicationSet present, and the `gitea-argocd` Service present** (proof the ArgoCD→Gitea repo auth landed — without it the ApplicationSet exists but ArgoCD cannot pull the repos). `postProcessReconcile` then refreshes ArgoCD and cancels the context → `mgr.Start` returns → CLI unblocks. The last status write is retried on conflict so `Ready=True` survives the exit (no controller remains locally to re-set it). Because every one of these is verified before the process is allowed to exit, a clean `adhar up` **cannot** report success with a half-wired platform — and if the process is interrupted before they hold, re-running `adhar up` resumes from the exact gate that was pending (§9).
- **`ExitOnSync=false`** (the in-cluster `adhar-controller-manager` Deployment): the loop never shuts down; it re-reconciles every 15s, self-healing the foundation and re-pushing the stack on `adhar upgrade` (via the exported `ApplyPlatformStack`).

## 5. Bootstrap phase — ordered foundation (`installCorePackagesSync`)

The heart of INV-1. Installers run in a fixed slice, each a `subReconciler` (`func(ctx, req, *AdharPlatform) (ctrl.Result, error)`):

```
Gateway API CRDs → Cilium → Gateway → [CNPG, if EnableHAMode] → ArgoCD → Gitea → Crossplane
```

| Order | Package (`api/v1alpha1` const) | Reconciler | What it applies (embedded under `resources/`) | Status set |
|---|---|---|---|---|
| 1 | `GatewayAPICRDsPackageName` | `ReconcileGatewayAPICRDs` | `gateway-api/crds.yaml` | — |
| 2 | `CiliumPackageName` | `ReconcileCilium` | `cilium/install.yaml` + `post-install.yaml` (Gateway API + Hubble) | — (no Cilium status field) |
| 3 | `GatewayPackageName` | `ReconcileGateway` | `gateway/gateway.yaml` (Kind) or `gateway-cloud.yaml` | `Gateway.Available` |
| 3.5 | `CNPGPackageName` (HA only) | `ReconcileCNPG` | `cnpg/…` (Gitea's replicated DB) | — |
| 4 | `ArgoCDPackageName` | `ReconcileArgo` | `argocd/install.yaml` or `install-ha.yaml` + `post-install.yaml` | `ArgoCD.Available` |
| 5 | `GiteaPackageName` | `ReconcileGitea` | `gitea/install.yaml` or `install-ha.yaml` + `post-install.yaml` (HTTPRoute) | `Gitea.Available` |
| 6 | `CrossplanePackageName` | `ReconcileCrossplane` | core install, then `configuration/{xrd,compositions,functions,providers,operations}` | `Crossplane.Available`, `ControlPlaneApplied` |

**Why this order** (embodying the "pure-GitOps can't self-bootstrap" argument): Gateway API CRDs must exist before Cilium starts with Gateway API enabled; the Gateway is created only after Cilium is up so Cilium can program it and generate the NodePort Service; ArgoCD and Gitea need the data path in place; Crossplane comes last as the provisioning control plane. In HA mode the CNPG operator is slotted before Gitea so Gitea's database can be a replicated CNPG cluster. Any installer error aborts the pass with `%s: %w` context and requeues at `errRequeueTime`.

Note a deliberate divergence from the ADR text, which lists the foundation as `Gateway API CRDs → Cilium → Cilium Gateway → ArgoCD → Gitea`: the as-built foundation also installs **CNPG (HA only)** and **Crossplane**, and shutdown is gated on `Crossplane.ControlPlaneApplied` — so "foundation" in practice extends through the control plane.

### 5.1 Embedded-manifest application (SSA)

Reconcilers read bytes from their `//go:embed resources/<component>` FS (e.g. `argoCDFS`, `ciliumFS`, `gatewayFS`) and hand them to `applyManifest` ([helpers.go](../../platform/controllers/adharplatform/helpers.go)):

```go
r.Patch(ctx, obj, client.Apply,
    client.FieldOwner(v1alpha1.FieldManager), client.ForceOwnership)
```

`applyManifest` decodes multi-doc YAML, resolves scope via the RESTMapper (with a hard-coded fallback set for cluster-scoped kinds), and sets a controller owner reference on namespaced objects **only** when they land in the platform namespace. Cluster-scoped and cross-namespace objects get no owner ref. This SSA-with-force pattern is what makes every installer idempotent and re-runnable (INV-1).

### 5.2 Gateway node-port pinning (Kind)

`ReconcileGateway` applies `gateway.yaml`, which embeds a `CiliumGatewayClassConfig` (the object that selects `service.type: NodePort` so Kind's host port-mapping works). Its CRD (`ciliumgatewayclassconfigs.cilium.io`) is installed by **Cilium** in the previous step, and `applyManifest` skips objects whose CRD is not yet installed (§5.1) — so the config lands on a pass where Cilium's CRD is already `Established`. Once it does, Cilium generates the `cilium-gateway-adhar-gateway` NodePort Service asynchronously. `ReconcileGateway` (Kind only) calls `pinGatewayNodePorts`, which retries (~90s, `RetryOnConflict` — Cilium owns and re-reconciles the Service) to pin `80→30080`, `443→30443`, and an alternate `8443→8443` (so on-node OIDC discovery against `https://keycloak.<host>:8443` reaches the Gateway). Pinning is **non-fatal**: if the Service isn't ready this pass, `Gateway.Available` is left unset and the core-install gate re-runs the reconciler next pass while the rest of the install still proceeds. Cloud providers use a LoadBalancer Service (`gateway-cloud.yaml`) and skip pinning.

## 6. GitOps phase — seed Gitea, hand off (`applyPlatformStack`)

Runs while `!Gitea.RepositoriesCreated`. Requires `StackDir != ""` (errors otherwise — GitOps seeding is a CLI-bootstrap-only capability). Steps:

1. **`setupGitOpsRepositories`** — `waitForGiteaReady` (deployment ready → pods Running/Ready → `GET /api/v1/version` responds, all via `kubectl exec` into the `gitea` pod, 10-min budget), then:
   - `createGiteaOrg` — creates org `adhar` (`globals.GiteaPlatformOrg`) and the Keycloak-group-mapped teams `developers`/`viewers` (read, `includes_all_repositories`); 409/422 tolerated (idempotent).
   - `createGiteaRepository` for `packages` and `environments` (`globals.GitOpsRepo*`), `auto_init:true`, `default_branch:main`; 409 tolerated.
   - `populateRepositories` → `populateGiteaRepo`: inside the pod, `git clone` the repo, `kubectl cp` the local `platform/stack/{packages,environments}` into a staging dir, copy over the working tree, commit, and `git push -f origin "$branch:main"`.
   - On success sets `Status.Gitea.RepositoriesCreated = true`.
2. **`applyArgoCDRepoAuth`** — SSA of `platform/stack/argocd-auth.yaml` (ArgoCD repo secrets + the dedicated `gitea-argocd` Service).
3. **`applyManifest` of the provider-selected ApplicationSet** — `appSetFileForProvider(resource.Spec.Provider)`: `ProviderKind`/unset → `adhar-appset-local.yaml` (curated single-node core), else → `adhar-appset-production.yaml` (full enablement). The `adhar-appset-workload.yaml` (thin-agent profile, generates nothing until workload clusters register) is applied when present.

`setupGitOpsRepositories` sets `Status.Gitea.RepositoriesCreated = true` once the org + both repos exist and are populated (step 1); the phase then applies repo auth (step 2) and the ApplicationSet(s) (step 3). After this, the controller stops writing workloads: ArgoCD reconciles everything from Gitea (INV-3). This is the imperative→declarative boundary.

The exported `ApplyPlatformStack` clears `RepositoriesCreated` on the in-memory object before calling `applyPlatformStack`, forcing a re-push (repo creation is 409-tolerant, population is a force push) — this is the `adhar upgrade` stack-push path.

### 6.1 GitOps-phase gate (`RepositoriesCreated`)

The GitOps phase is gated on a single status flag, `Gitea.RepositoriesCreated`, set inside `setupGitOpsRepositories` once the org + both repos exist and are populated. The reconcile loop (§4) keys the whole phase on it (`if !RepositoriesCreated → applyPlatformStack`), and `shouldShutDown` (§4.1) requires it before the process may exit. While it is false, `applyPlatformStack` runs its three steps in order — seed repos, apply the ArgoCD repo auth, apply the ApplicationSet(s) — each an idempotent Server-Side Apply, so a re-run re-applies them safely.

## 7. Data model (`api/v1alpha1/adharplatform_types.go`)

```go
type AdharPlatformSpec struct {
    Provider           EnvironmentProvider    // kind|aws|azure|gke|do|civo|custom; "" == kind
    PackageConfigs     PackageConfigsSpec
    BuildCustomization BuildCustomizationSpec // Protocol, Host, IngressHost, Port,
}                                             // UsePathRouting, SelfSignedCert, StaticPassword, EnableHAMode

type AdharPlatformStatus struct {
    ObservedGeneration int64
    ArgoCD     ArgoCDStatus     // Available, AppsCreated
    Gateway    GatewayStatus    // Available
    Gitea      GiteaStatus      // Available, RepositoriesCreated, External/InternalURL, admin secret ref
    Crossplane CrossplaneStatus // Available, ControlPlaneApplied
    Conditions []metav1.Condition
}
```

`syncConditions` ([conditions.go](../../platform/controllers/adharplatform/conditions.go)) rewrites the full condition set every pass from the component statuses: `ArgoCDReady`, `GatewayReady`, `GiteaReady`, `CrossplaneReady` (needs both `Available` **and** `ControlPlaneApplied`), `GitOpsReady` (from `RepositoriesCreated`), and the aggregate `Ready` — True only when all hold, otherwise carrying the last reconcile failure (`lastFailureReason/Message`) as its message. `adhar get status` renders these. There is intentionally **no** Cilium status field; Cilium readiness is implied by `Gateway.Available` (the Gateway can't program without Cilium).

## 8. Topology mapping (ADR §"scales down/up")

| | Local (T1, Kind) | Production (T3, cloud/on-prem) |
|---|---|---|
| Cluster | `adhar` Kind node, CNI/kube-proxy off | provider factory (`platform/providers/{aws,azure,gcp,digitalocean,civo,custom}`) |
| Management + workload | collapsed into one node | one management cluster; Crossplane provisions workload clusters |
| Controller placement | in-process, `ExitOnSync`, exits on convergence | in-process bootstrap → then `adhar-controller-manager` Deployment, continuous |
| Foundation size | `install.yaml` (single replica) | `install-ha.yaml` when `EnableHAMode` (replicas, PDBs, HA redis, CNPG for Gitea) |
| Gateway edge | NodePort, pinned 30080/30443/8443 | LoadBalancer Service + cert-manager listener cert |
| TLS | self-signed `adhar-cert` | self-signed initial → cert-manager ClusterIssuer |

Same CRDs, same reconcile pipeline, same embedded manifests — only `Provider`, `EnableHAMode`, and controller placement change (INV-4).

## 9. Failure modes & idempotency

Every known failure is either **retried in place** (watch mode / same process) or **resumed on re-run** (`adhar up` again). The bootstrap is designed so no failure requires manual cleanup or a flag day, and so a clean exit is impossible while any gate is unmet.

| Failure | Detection | Recovery |
|---|---|---|
| Partial foundation (any installer errors) | installer returns error | `recordFailure` + requeue 5s; the guard re-runs the whole ordered slice; SSA re-adopts already-applied objects |
| Gateway Service not yet NodePort (or `CiliumGatewayClassConfig` CRD not yet registered) | `pinGatewayNodePorts` timeout | non-fatal; `Gateway.Available` stays false → core-install gate re-runs the reconciler next pass, by which time Cilium's CRD is `Established` and the config applies |
| Gitea slow to serve | `waitForGiteaReady` (10-min deploy budget + in-pod `GET /api/v1/version` probe) | seeding never races an unready API; times out with a clear error → requeue |
| GitOps phase fails before repos are populated | `RepositoriesCreated` stays false (§6.1) | whole phase re-runs; org/repo creation is 409-tolerant, population is a force push, auth + ApplicationSet re-applied (SSA) |
| Control plane lags provider CRDs | `!ControlPlaneApplied` | explicit 15s requeue guarantees convergence even in watch mode where the `ExitOnSync` loop is skipped |
| Status write conflict on the final local pass | `postProcessReconcile` `RetryOnConflict` | `Ready`/`RepositoriesCreated`/`ControlPlaneApplied` are never dropped as the controller exits |
| **Local process interrupted mid-bootstrap** (Ctrl-C, terminal closed, laptop sleep, timeout) | on next start, `.status` gates reflect exactly what completed | **re-run `adhar up`** — it reuses the healthy Kind cluster (`Cluster.Reconcile(recreate=false)` returns early), re-enters the reconcile pipeline, and every already-satisfied gate short-circuits while the pending one runs. This is the residual risk of local `ExitOnSync` mode (an ephemeral in-process controller has nothing to retry once the process dies) and re-run is its designed, verified mitigation. Production installs carry the in-cluster `adhar-controller-manager` Deployment and self-heal without a re-run. |
| **ApplicationSet deleted/lost after bootstrap** (ArgoCD goes empty) | the platform ApplicationSet is gone; ArgoCD shows no apps | in local mode the controller is ephemeral and has already exited, so nothing re-creates it automatically — re-apply it by hand: `kubectl apply -n adhar-system -f platform/stack/adhar-appset-local.yaml -f platform/stack/adhar-appset-workload.yaml` (add `-f platform/stack/argocd-auth.yaml` if the repo secrets / `gitea-argocd` Service are also gone). Production runs the in-cluster `adhar-controller-manager`, which re-applies it on the next reconcile. |
| Management-cluster outage (ADR ⚠️) | — | degrades the platform to "no changes"; running workloads are unaffected. HA/DR is the Production Guide's concern |

**Rock-solid guarantees.** Taken together: (1) every foundation apply is idempotent SSA, so re-application is always safe; (2) each phase is gated on a status flag, so a satisfied phase short-circuits on re-entry; (3) the exit gate verifies the platform is *usable* (ApplicationSet + repo auth present), so `adhar up` never reports success on a half-wired platform; (4) any interruption is resumable by re-running the same command. See [Troubleshooting](../TROUBLESHOOTING.md) for the operator-facing failure-signature runbook and the [User Guide — Bootstrap & Day-2 Operations](../USER_GUIDE.md#6-bootstrap--day-2-operations) for verify-healthy / re-run / upgrade / teardown procedures.

## 10. Testing

- **Unit / envtest** (`platform/controllers/adharplatform/*_test.go`): `TestAdharPlatformReconciler_ReconcileArgo`/`ReconcileGitea`/`ReconcileCilium`/`ReconcileGatewayAPICRDs` drive each installer under envtest (CRD path `resources/argocd/install.yaml`, metrics off, `BindAddress:"0"`); `TestSyncConditions` covers the condition derivation; `TestAppSetFileForProvider` locks the provider→appset selection; `ha_test.go` asserts HA manifest invariants and the CNPG bootstrap.
- **Parity** (`parity_test.go`): `TestLocalProductionAppSetParity` and `TestEnvironmentConfigsMatchAppSets` keep `adhar-appset-local.yaml`/`-production.yaml` and the environment configs consistent — the imperative/declarative boundary can't silently drift (ADR ⚠️).
- **E2E** (`tests/e2e/bootstrap/bootstrap_test.go`, `make e2e`): a full `adhar up` → verify foundation + GitOps → `adhar down` cycle on Kind. `ADHAR_E2E_SKIP_UP=1` verifies an already-running platform; `ADHAR_E2E_KEEP=1` leaves it up.

## 11. Code & file map

| Path | Responsibility |
|---|---|
| `cmd/up/up.go` | `UpCmd`, flags, `create` dispatch (local vs. `-f` production) |
| `cmd/up/local.go` | `LocalProvisioner.Provision`, Kind create, stage tracker, `AdharPlatform` CR, `pollPlatformStages`, `ExitOnSync` wiring |
| `cmd/up/production.go` | config load/resolve, provider `ProvisionEnvironment`, per-env loop |
| `cmd/up/bootstrap.go` | `bootstrapPlatformOnCluster`: CRDs/TLS/manager/CR on a provisioned cluster, then `EnsureControllerManager` |
| `platform/controllers/run.go` | `RunControllers` — registers the 3 reconcilers, starts the manager |
| `platform/controllers/crd.go` | `EnsureCRDs`/`EnsureCRD` — embedded CRD install, waits for `Established` |
| `platform/controllers/manager.go` | `EnsureControllerManager` — in-cluster `adhar-controller-manager` Deployment + RBAC (`resources/manager/manager.yaml`) |
| `platform/controllers/adharplatform/controller.go` | `Reconcile`, `installCorePackagesSync` (ordered foundation), `applyPlatformStack`, GitOps seeding, `shouldShutDown`, `postProcessReconcile` |
| `.../adharplatform/{cilium,gateway,argocd,gitea,cnpg,crossplane}.go` | per-component reconcilers + embedded FS + status setters |
| `.../adharplatform/helpers.go` | `applyManifest` — SSA with `FieldManager="adhar"`, `ForceOwnership`, owner refs |
| `.../adharplatform/conditions.go` | `syncConditions` — component status → conditions + aggregate `Ready` |
| `.../adharplatform/installer.go` | `EmbeddedInstallation` — namespace/ensure/readiness-wait helper |
| `.../adharplatform/resources/{gateway-api,cilium,gateway,argocd,gitea,cnpg,crossplane}/` | embedded foundation manifests |
| `api/v1alpha1/adharplatform_types.go` | `AdharPlatformSpec/Status`, package-name consts, `FieldManager`, `CliStartTimeAnnotation`, `EnvironmentProvider` enum |
| `globals/project.go` | `AdharSystemNamespace`, `GiteaPlatformOrg`, `GitOpsRepo{Packages,Environments}`, Gitea admin creds, default host/cert names |
| `platform/providers/kind/{cluster.go,resources/kind.yaml.tmpl,coredns.go,tls.go}` | Kind node (CNI/kube-proxy disabled, port map, node-port range), CoreDNS rewrite, self-signed cert |
| `platform/stack/{adhar-appset-local.yaml,adhar-appset-production.yaml,adhar-appset-workload.yaml,argocd-auth.yaml,packages/,environments/}` | GitOps-phase content seeded into Gitea + the handoff ApplicationSet |
