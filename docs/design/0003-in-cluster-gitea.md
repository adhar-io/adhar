# Low-Level Design — Self-hosted in-cluster Gitea as the platform source of truth

Detailed design for [ADR-0003](../adr/0003-in-cluster-gitea.md). This is the authoritative as-built description of how Adhar ships **Gitea inside the cluster** as the GitOps system of record: the embedded install/post-install manifests, the bootstrap reconciler that installs it, the CLI-driven repository seeding (org → repos → `git push`), the ArgoCD↔Gitea wiring, SSO/team mapping, and password rotation. Every path and symbol below resolves in the current tree.

## 0. Context recap

GitOps needs a Git server, and the bootstrap cannot depend on an external SaaS or customer forge — those break the "one command, offline, no accounts" promise and would require credentials that don't exist yet. ADR-0003 ships **Gitea in-cluster** as part of the bootstrap foundation: the controller installs it, waits for API readiness, creates the `environments`/`packages` repos, force-pushes `platform/stack/` into them, and wires ArgoCD to sync from it. Gitea is the platform's system of record; developers may still use GitHub/GitLab/Bitbucket as first-class `GitRepository` providers and mirror content, but the in-cluster copy is what ArgoCD reconciles.

## 1. Invariants

- **INV-1** Gitea is installed by the **5th** step of the ordered foundation (`Gateway API CRDs → Cilium → Gateway → [CNPG, HA] → ArgoCD → Gitea → Crossplane`), after the data path and ArgoCD, before the GitOps handoff.
- **INV-2** The install is **embedded** (`//go:embed resources/gitea`) — offline-capable, no chart fetched at runtime; applied via Server-Side Apply with `FieldManager = "adhar"`, `ForceOwnership`.
- **INV-3** Repository **seeding is a CLI-bootstrap-only capability**: it shells out to `kubectl exec`/`kubectl cp` against the `gitea` pod and requires `r.StackDir != ""`. The in-cluster manager runs with an empty `StackDir` and only reconciles an already-seeded platform.
- **INV-4** All Gitea state lives in namespace **`adhar-system`** (not a dedicated `gitea` namespace); the admin credential Secret `gitea-credential` lives there too.
- **INV-5** Every seeding API call is **idempotent**: org/team creation tolerates 409/422, repo creation tolerates 409, population is a `git push -f`. Re-runs adopt existing state.

## 2. Reconciler — install (`gitea.go`)

[`platform/controllers/adharplatform/gitea.go`](../../platform/controllers/adharplatform/gitea.go) embeds the manifest tree and runs a two-manifest install:

```go
//go:embed resources/gitea
var giteaFS embed.FS

func (r *AdharPlatformReconciler) ReconcileGitea(ctx, req, resource) (ctrl.Result, error) {
    giteaManifestPath := "resources/gitea/install.yaml"
    if resource.Spec.BuildCustomization.EnableHAMode {
        giteaManifestPath = "resources/gitea/install-ha.yaml"   // replicated PG (CNPG), PDBs
    }
    // applyManifest → SSA; then apply resources/gitea/post-install.yaml (HTTPRoute, creds, token Job)
    resource.Status.Gitea.Available = true
}
```

`RawGiteaInstallResources` routes through `k8s.BuildCustomizedManifests(config.FilePath, "resources/gitea", giteaFS, …)` so a user-provided override file on disk can layer on top of the embedded base. `giteaInternalBaseUrl(config)` renders the in-cluster URL used by other components — `http://gitea.<host>:<port>` (host routing) or `http://<host>:<port>/gitea` (`UsePathRouting`).

`ReconcileGitea` sets **only** `Status.Gitea.Available = true`; `syncConditions` derives `GiteaReady` from it, and `Status.Gitea.RepositoriesCreated` (set later, §5) drives the separate `GitOpsReady` condition.

## 3. Embedded install manifest (`resources/gitea/install.yaml`)

The single-node `install.yaml` (~1500 lines) is a rendered Gitea Helm chart (`gitea-12.7.0`, app `1.27.0`) plus its bundled datastores, all pinned into `adhar-system`:

| Object | Kind | Notes |
|---|---|---|
| `gitea` | Deployment | `replicas: 1`, image `docker.gitea.com/gitea:1.27.0-rootless`; init containers + main container mount `/data` from PVC `gitea-shared-storage` |
| `gitea-shared-storage` | PersistentVolumeClaim | `10Gi`, `ReadWriteOnce` (repositories, LFS, avatars) |
| `gitea-postgresql` | StatefulSet | bundled `bitnami/postgresql`, `5Gi` PVC — Gitea's metadata DB (single-node) |
| `gitea-valkey-primary` | StatefulSet | bundled `valkey` (`8Gi` PVC) — session + queue backend (redis protocol) |
| `gitea-inline-config` | Secret | non-secret `app.ini` sections consumed by the init container |
| `gitea` | Secret | rendered chart secret / init scripts |
| `gitea-http` | Service | headless ClusterIP, port `3000` — the in-cluster HTTP endpoint |
| `gitea-ssh` | Service | headless ClusterIP, port `22 → 2222` — Git-over-SSH |

Key `gitea-inline-config` settings (verified at [install.yaml:186-220](../../platform/controllers/adharplatform/resources/gitea/install.yaml)):

```ini
[database]  HOST=gitea-postgresql.adhar-system.svc.cluster.local:5432  NAME=gitea USER=gitea
[server]    DOMAIN=gitea.adhar.localtest.me  HTTP_PORT=3000  PROTOCOL=http
            ROOT_URL=https://gitea.adhar.localtest.me:8443/   # drives AppSubURL / link generation
            SSH_DOMAIN=adhar.localtest.me  SSH_LISTEN_PORT=2222  START_SSH_SERVER=true
[oauth2_client] ENABLE_AUTO_REGISTRATION=true  USERNAME=preferred_username  ACCOUNT_LINKING=auto
[queue]/[session] TYPE/PROVIDER=redis → gitea-valkey-primary…:6379
[security] INSTALL_LOCK=true
```

`ENABLE_AUTO_REGISTRATION=true` + `ACCOUNT_LINKING=auto` is load-bearing for SSO (§6): the first Keycloak login auto-creates the Gitea account from OIDC claims instead of stalling on the interactive register page, which is what lets the auth source's `--group-team-map` grant org/team membership on first login.

### 3.1 HA variant (`install-ha.yaml`)

When `EnableHAMode` is set, `ReconcileGitea` applies `install-ha.yaml` instead. Per [`ha_test.go`](../../platform/controllers/adharplatform/ha_test.go) invariants, the HA rendering: contains `PodDisruptionBudget`s; points the DB at the **CNPG-managed** cluster `HOST=gitea-db-rw.adhar-system.svc.cluster.local:5432`; and contains **no** `gitea-postgresql` chart-bundled Postgres (the CNPG operator, installed as foundation step 3.5 in HA mode, owns the database). This realizes ADR-0003's ⚠️ "production requires external PostgreSQL (CNPG), replicas, backup".

## 4. Post-install manifest (`resources/gitea/post-install.yaml`)

Applied after `install.yaml` on every pass. Three concerns:

1. **`gitea-server` HTTPRoute** — `gateway.networking.k8s.io/v1`, `parentRefs: adhar-gateway`, `hostnames: [gitea.adhar.localtest.me]`, backend `gitea-http:3000`. TLS terminates at the Cilium Gateway (`adhar-cert`). Gitea's sub-path model is the inverse of ArgoCD's: `ROOT_URL` sets `AppSubURL=/gitea` for link/asset/cookie/OAuth-callback generation, while a companion `URLRewrite ReplacePrefixMatch "/"` on the routed path strips `/gitea` before forwarding (Gitea serves handlers at root). Both halves are required together — the long header comment in the file documents why forwarding the prefix intact 404s and stripping without `ROOT_URL` breaks assets.
2. **`gitea-credential` Secret** — `Opaque`, `stringData: {username: gitea_admin, password: r8sA8CPHD9!bt6d}`, mirroring `hack/gitea/values.yaml`. This is the break-glass admin, consumed by the password-sync flow (§7) and by the console's `gitea-credentials` ExternalSecret via a `gitea` ClusterSecretStore.
3. **`gitea-token-gen` Job (+ SA/Role/RoleBinding)** — ensures `gitea-credential` also carries a `token` key. The chart secret ships username/password only; consumers needing token auth (Lighthouse's oauth secret, the preview-environments PR generator) read `token`. The Job is idempotent (exits when the key already exists), waits on `/api/healthz`, mints an `all`-scoped token via `POST /api/v1/users/${USER}/tokens`, and merge-patches it back into the Secret.

## 5. Repository seeding (`controller.go`)

Runs from `applyPlatformStack` once `!Status.Gitea.RepositoriesCreated`. Requires `r.StackDir != ""` — otherwise it errors with "GitOps repository seeding requires the CLI bootstrap (adhar up)" (INV-3). The seeding functions all live in [`controller.go`](../../platform/controllers/adharplatform/controller.go):

```
applyPlatformStack
 ├─ setupGitOpsRepositories
 │    ├─ waitForGiteaReady          (3-stage readiness gate)
 │    ├─ createGiteaOrg             (org "adhar" + teams developers/viewers)
 │    ├─ createGiteaRepository("environments")   auto_init:true, default_branch:main
 │    ├─ createGiteaRepository("packages")
 │    ├─ populateRepositories → populateGiteaRepo × 2   (kubectl cp + git push -f)
 │    └─ Status.Gitea.RepositoriesCreated = true
 ├─ applyArgoCDRepoAuth             (SSA platform/stack/argocd-auth.yaml)
 ├─ applyManifest(appSetFileForProvider(provider))   local vs production ApplicationSet
 └─ applyManifest(adhar-appset-workload.yaml)         if present (thin-agent profile)
```

### 5.1 `waitForGiteaReady` — 3-stage gate

1. **Deployment** ready — polls `Deployment/gitea` for `ReadyReplicas > 0 && AvailableReplicas > 0`, 60 × 10s (10-min budget).
2. **Pods** running — lists `app=gitea` pods, requires all `PodRunning` + `PodReady=True`, 30 × 10s.
3. **API** responding — `kubectl exec … -c gitea -- curl -sf http://localhost:3000/api/v1/version`, 30 × 10s.

This replaces a fixed sleep so seeding never races an unready API. `getGiteaPodName` resolves the pod via `client.MatchingLabels{"app": "gitea"}`.

### 5.2 `createGiteaOrg` — org + group-mapped teams

Runs all API calls as `curl -u gitea_admin:r8sA8CPHD9!bt6d` from **inside** the pod (`kubectl exec -c gitea -- sh -c`, capturing `-w "%{http_code}"`). Creates:

- Org **`adhar`** (`globals.GiteaPlatformOrg`), `visibility: public`.
- Teams **`developers`** and **`viewers`** with `permission: read`, `includes_all_repositories: true`, units `repo.code/issues/pulls/releases/wiki`. `Owners` is Gitea's built-in team.

409 (conflict) and 422 (name taken) are treated as success — the whole function is idempotent. The teams exist so Keycloak group membership (via `--group-team-map`, §6) grants repo access without per-user collaborators; write to platform config stays with `platform-admin` → `Owners`.

### 5.3 `populateGiteaRepo` — clone, copy, force-push

For each repo, entirely inside the pod via `kubectl exec … sh -c`:

1. `rm -rf` working/staging dirs under `/tmp`.
2. `git clone http://gitea_admin:<pw>@localhost:3000/adhar/<repo>.git` (falls back to `git init -b main` if the clone fails).
3. `rm -rf $(ls -A | grep -v .git)` to clear the working tree.
4. `kubectl cp <StackDir>/<repo>` from the **host** into a staging dir, then `cp -a staging/. working/` (kubectl cp copies the dir itself, so stage-then-move flattens the contents to the repo root).
5. `git config` identity `Adhar Platform <admin@adhar.io>`, `git add -A`, commit (skips on `NO_CHANGES`).
6. `branch=$(git rev-parse --abbrev-ref HEAD) && git push -f origin "$branch:main"` — the `-f … :main` handles any local branch name and makes population a force-push (idempotent).

Sources are `filepath.Join(r.StackDir, "packages")` and `…/environments`.

## 6. ArgoCD ↔ Gitea wiring (`applyArgoCDRepoAuth` → `platform/stack/argocd-auth.yaml`)

SSA of [`argocd-auth.yaml`](../../platform/stack/argocd-auth.yaml) creates:

- **`gitea-argocd` Service** — a dedicated ClusterIP (port 3000, selector `app.kubernetes.io/name=gitea`) giving ArgoCD a stable in-cluster endpoint independent of the headless `gitea-http`.
- **`repo-environments` / `repo-packages` Secrets** — labeled `argocd.argoproj.io/secret-type: repository`, `type: git`, `url: http://gitea-argocd.adhar-system.svc.cluster.local:3000/adhar/<repo>`, `username: gitea_admin`, `password: …`, `insecure: "true"` (plain HTTP in-cluster).
- **`gitea-argocd-config` ConfigMap** — service/credential discovery for tooling.

ArgoCD then reconciles the provider-selected ApplicationSet (`appSetFileForProvider`: `ProviderKind`/`""` → `adhar-appset-local.yaml`, else `adhar-appset-production.yaml`) whose Applications point at these two repos. This is the imperative→declarative boundary: after seeding, the controller stops writing workloads.

## 7. SSO / group-team mapping (GitOps phase)

Gitea's OIDC auth source is **not** created during bootstrap — it is configured by the `gitea-oauth-config` Job in the Keycloak package ([`platform/stack/packages/security/keycloak/manifests/gitea-oauth-config.yaml`](../../platform/stack/packages/security/keycloak/manifests/gitea-oauth-config.yaml)), applied via ArgoCD once Keycloak has published `keycloak-clients`. The Job `kubectl exec`s into the Gitea pod and runs `gitea admin auth add-oauth` (or `update-oauth` if a source named `keycloak` exists):

```
--provider openidConnect --auto-discover-url .../realms/adhar/.well-known/openid-configuration
--scopes 'openid profile email groups' --group-claim-name groups --admin-group platform-admin
--group-team-map '{"platform-admin":{"adhar":["Owners"]},
                   "platform-developer":{"adhar":["developers"]},
                   "platform-viewer":{"adhar":["viewers"]}}'
--group-team-map-removal
```

This maps the three Keycloak platform groups onto the org teams created in §5.2. It is non-fatal (`|| echo WARN`): a transient OIDC hiccup must not wedge the Keycloak Sync hook — Gitea local login (`gitea_admin`) remains the break-glass path.

## 8. Password rotation (`StaticPassword` mode)

When `Config.StaticPassword` is set, the steady-state reconcile rotates the Gitea admin password to `developer`:

- `updateGiteaPassword` ([controller.go:988](../../platform/controllers/adharplatform/controller.go)) builds a `code.gitea.io/sdk/gitea` client against `utils.GiteaBaseUrl`, calls `AdminEditUser` to set the new password, then `utils.PatchPasswordSecret`.
- `utils.PatchPasswordSecret` ([`platform/utils/gitea.go`](../../platform/utils/gitea.go)) SSA-patches `gitea-credential.data.password`, and because the secret name contains `gitea`, also **re-mints** the admin API token (`GetGiteaToken` deletes the existing `admin` token and `CreateAccessToken` with `AccessTokenScopeAll`), writing it to `data.token`. This keeps token consumers valid across a rotation.

## 9. `GitRepository` CRD — Gitea is source-of-record, not the only forge

ADR-0003's "system of record, not necessarily the developers' forge" is realized by the `GitRepository` CRD, whose provider enum ([`api/v1alpha1/gitrepository_types.go`](../../api/v1alpha1/gitrepository_types.go)) is `gitea | gitlab | github | bitbucket`. Organizations can host developer content on an external forge and mirror it, while ArgoCD still syncs the platform stack from in-cluster Gitea. Two-way mirror sync is the organization's responsibility (ADR ⚠️).

## 10. Data model

```go
// api/v1alpha1/adharplatform_types.go
type GiteaStatus struct {
    Available           bool   // set by ReconcileGitea → GiteaReady condition
    ExternalURL         string
    InternalURL         string
    RepositoriesCreated bool   // set by setupGitOpsRepositories → GitOpsReady condition
    // + admin secret ref
}
```

```go
// globals/project.go
GiteaPlatformOrg       = "adhar"
GitOpsRepoPackages     = "packages"
GitOpsRepoEnvironments = "environments"
GiteaAdminUser         = "gitea_admin"
GiteaAdminPassword     = "r8sA8CPHD9!bt6d"
AdharSystemNamespace   = "adhar-system"

// platform/utils/gitea.go
GiteaNamespace   = "adhar-system"
GiteaAdminSecret = "gitea-credential"
```

## 11. Failure modes & idempotency

- **Gitea slow to serve** — `waitForGiteaReady`'s 10-min deployment budget + pod + API probe; seeding never races an unready API.
- **Re-run after partial seed** — `setupGitOpsRepositories` short-circuits on `RepositoriesCreated`; org/team creation tolerates 409/422; repo creation tolerates 409; population force-pushes. No manual cleanup.
- **`adhar upgrade` re-push** — the exported `ApplyPlatformStack` sets `resource.Status.Gitea.RepositoriesCreated = false` on the in-memory object before calling `applyPlatformStack`, forcing a repopulate (creation is 409-tolerant, population is `-f`). Without this the upgrade silently pushed nothing on an already-bootstrapped platform (observed live).
- **In-cluster manager without stack** — `applyPlatformStack` errors fast if `StackDir == ""`, so the continuous manager never attempts seeding it can't perform.
- **OIDC hiccup** — the `gitea-oauth-config` Job is non-fatal; local admin login is always available.

## 12. Testing

- **Unit** ([`gitea_test.go`](../../platform/controllers/adharplatform/gitea_test.go)): `TestGiteaInternalBaseUrl` locks host-vs-path URL rendering; `TestGetGiteaToken` covers the token-mint client (timeout path); `TestAdharPlatformReconciler_ReconcileGitea` drives the installer under a fake client and asserts it errors on a missing manifest.
- **HA** ([`ha_test.go`](../../platform/controllers/adharplatform/ha_test.go)): `TestGiteaHAManifestInvariants` asserts `install-ha.yaml` ships PDBs, points at `gitea-db-rw` (CNPG), and drops the bundled `gitea-postgresql`.
- **Conditions** ([`conditions_test.go`](../../platform/controllers/adharplatform/conditions_test.go)): `GiteaReady`/`GitOpsReady` derivation from `Available`/`RepositoriesCreated`.
- **E2E** (`tests/e2e/bootstrap`, `make e2e`): full `adhar up` verifies Gitea is reachable and the `adhar` org + `packages`/`environments` repos are seeded and syncing.

## 13. Code & file map

| Path | Responsibility |
|---|---|
| `platform/controllers/adharplatform/gitea.go` | `ReconcileGitea` (install/post-install, HA switch), `giteaFS` embed, `RawGiteaInstallResources`, `giteaInternalBaseUrl` |
| `platform/controllers/adharplatform/resources/gitea/install.yaml` | rendered Gitea chart (Deployment, PVC, bundled Postgres/Valkey, `gitea-inline-config`, `gitea-http`/`gitea-ssh` Services) |
| `platform/controllers/adharplatform/resources/gitea/install-ha.yaml` | HA rendering — CNPG-backed DB, PDBs, replicas |
| `platform/controllers/adharplatform/resources/gitea/post-install.yaml` | `gitea-server` HTTPRoute, `gitea-credential` Secret, `gitea-token-gen` Job/RBAC |
| `platform/controllers/adharplatform/controller.go` | `applyPlatformStack`, `setupGitOpsRepositories`, `waitForGiteaReady`, `createGiteaOrg`, `createGiteaRepository`, `populateGiteaRepo`, `applyArgoCDRepoAuth`, `appSetFileForProvider`, `ApplyPlatformStack`, `updateGiteaPassword` |
| `platform/utils/gitea.go` | `GetGiteaToken`, `GiteaBaseUrl`, `PatchPasswordSecret`, Gitea constants |
| `platform/stack/argocd-auth.yaml` | `gitea-argocd` Service, `repo-environments`/`repo-packages` ArgoCD repo Secrets, config ConfigMap |
| `platform/stack/packages/security/keycloak/manifests/gitea-oauth-config.yaml` | `gitea-oauth-config` Job — OIDC auth source + `--group-team-map` |
| `api/v1alpha1/gitrepository_types.go` | `GitProvider{Gitea,Gitlab,Github,Bitbucket}` — multi-forge `GitRepository` support |
| `globals/project.go` | `GiteaPlatformOrg`, `GitOpsRepo{Packages,Environments}`, admin creds, `AdharSystemNamespace` |

## Drift vs. ADR-0003

- The ADR lists the foundation order ending at Gitea; as-built Gitea is step 5 and **Crossplane follows it** (see ADR-0001 design). Minor — doesn't affect Gitea's role.
- The ADR says the controller "wires ArgoCD … and pushes"; the **SSO/team mapping** is actually applied in the GitOps phase by the Keycloak package's `gitea-oauth-config` Job, not by the bootstrap controller. The bootstrap only creates the empty teams; group→team binding lands when Keycloak converges.
- ADR mentions production "external PostgreSQL (CNPG)"; single-node `install.yaml` ships a **chart-bundled bitnami Postgres** StatefulSet — CNPG is used only in the `EnableHAMode` `install-ha.yaml` path.
