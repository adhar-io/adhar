# Low-Level Design — SSO bootstrap via idempotent in-cluster config job

Detailed design for [ADR-0013](../adr/0013-sso-bootstrap-config-job.md). This is the as-built description of the Keycloak bootstrap `config` Job that provisions the `adhar` realm, groups, test users and OIDC clients on a fresh cluster, exports every confidential client secret into the `keycloak-clients` Secret, and reconciles that state on every ArgoCD sync — the first link in the SSO chain established by [ADR-0008](../adr/0008-keycloak-platform-identity.md) and [ADR-0009](../adr/0009-secrets-eso-vault.md).

## 0. Context recap

Keycloak is the platform IdP, but on a fresh `adhar up` nothing exists inside it — no realm, no clients, no `keycloak-clients` Secret for downstream `ExternalSecret`s to read. ADR-0013 chose a **scripted bash Job shipped with the keycloak package**, run as an **ArgoCD Sync hook**, over a Keycloak Operator / realm CRs (covers creation but not secret *export*) or a Terraform/Crossplane Keycloak provider (heavy bootstrap dependency). The job does both halves — provision *and* export credentials back into Kubernetes — in one reviewable file: `platform/stack/packages/security/keycloak/manifests/keycloak-config.yaml`.

## 1. The SSO chain

```
config Job (wave 20) --writes--> Secret keycloak-clients (adhar-system)
      |                                     |
      | (creates realm+clients in Keycloak) | read via ClusterSecretStore "keycloak"
      v                                     v
  Keycloak "adhar" realm            per-service ExternalSecret (wave 40)
                                            |
                                            v
                          grafana-oidc / argocd-secret / gitea-oauth /
                          headlamp-oauth2-proxy / harbor / minio / vault / … 
                                            |
                                            v
                             oauth2-proxy or native OIDC login
```

`keycloak-clients` is the single hand-off point. Producers: the config Job (full-provision build, rebuild-if-missing path, and per-client resync loop). Consumers: `ExternalSecret`s that read it through the kubernetes-backed `ClusterSecretStore` named `keycloak` (defined in `secret-gen.yaml`).

## 2. Package objects (`security/keycloak/manifests/keycloak-config.yaml`)

| Object | Kind | Purpose |
|---|---|---|
| `keycloak-config` | ServiceAccount | identity the Job runs as |
| `keycloak-config` | Role / RoleBinding | `secrets: get,create,update,patch` in `adhar-system` — lets the Job write `keycloak-clients` |
| `config-job` | ConfigMap | all Admin-API JSON payloads (realm, groups, group-mapper, users, one `*-client-payload.json` per client), mounted read-only at `/var/config/` |
| `config` | Job | the bootstrap script (Sync hook, wave 20) |

Two volumes feed the Job container (`docker.io/alpine/k8s:1.31.0`, which ships `bash`+`curl`+`jq`+`kubectl` — chosen to avoid the per-sync `apt`/`kubectl`-download cost of the previous `ubuntu:22.04` image):

- `keycloak-config` — the `keycloak-config` **Secret** (produced by `secret-gen.yaml`, see §6) mounted at `/var/secrets/`; the script reads `KEYCLOAK_ADMIN_PASSWORD` and `USER_PASSWORD` from files there.
- `config-payloads` — the `config-job` ConfigMap at `/var/config/`.

## 3. Ordering — sync waves & hook semantics

The Job carries:

```yaml
annotations:
  argocd.argoproj.io/hook: Sync
  argocd.argoproj.io/hook-delete-policy: BeforeHookCreation
  argocd.argoproj.io/sync-wave: "20"
```

Wave layout inside the keycloak package (all in `adhar-system`):

| Wave | Object | Kind |
|---|---|---|
| 0 | `oidc-loopback-proxy` | Deployment/Service |
| 5 | `keycloak-db` | CNPG `Cluster` (Postgres backing store) |
| 6 | `keycloak-db-daily` | CNPG `ScheduledBackup` |
| 10 | `keycloak` | Deployment (+ Service, ConfigMap) |
| 15 | `keycloak` HTTPRoute | Gateway route |
| **20** | **`config`** | **Job (Sync hook)** — realm/clients + writes `keycloak-clients` |
| 30 | `gitea-oauth-config` | Job (Sync hook) — registers the Gitea login source |
| 40 | `argocd-oidc`, `gitea-oauth`, `grafana-oidc` | ExternalSecrets consuming `keycloak-clients` |

The Job **must** run at wave 20 as a `Sync` hook rather than `PostSync`: as a `PostSync` hook it only ran after the whole sync succeeded, but the sync could never succeed because the wave-0/wave-40 `ExternalSecret`s failed with *"could not get secret data from provider"* (`keycloak-clients` did not yet exist) — a deadlock that left Keycloak permanently `OutOfSync`. Running mid-sync at wave 20 writes `keycloak-clients` before the wave-40 consumers are reconciled.

**Hook diff caveat (from the ADR):** hook manifests are not part of ArgoCD's desired-state diff, so editing the Job script alone never triggers auto-sync — rolling out a script change requires an explicit `adhar` / ArgoCD sync (or any non-hook change in the same package). `BeforeHookCreation` deletes the prior Job instance before each run so a re-sync always executes a fresh Job.

## 4. Script control flow

`command: ["/bin/bash","-c"]`, `set -ex -o pipefail`, `restartPolicy: Never`. `KEYCLOAK_URL=http://keycloak.adhar-system.svc.cluster.local:8080` (in-cluster plaintext; TLS is terminated at the Cilium Gateway).

### 4.1 Token acquisition & lifetime fix

1. Password-grant an admin token from the **master** realm (`username=adhar-admin`, `client_id=admin-cli`), `jq -e -r '.access_token'`.
2. `set +e` — everything after is best-effort/non-fatal until full provisioning.
3. **Raise `accessTokenLifespan` to 1800** on the master realm, then **re-fetch** the token so it carries the new lifespan. Master admin tokens default to 60s, but the Job spends minutes between API calls; without this, later calls got HTTP 401 and the Job died half-provisioned.

### 4.2 TLS relaxation (runs every sync, before the early-exit)

`PUT {"sslRequired":"NONE"}` on `admin/realms/master` and `admin/realms/adhar`. The platform sits behind the self-signed Cilium Gateway that forwards to Keycloak over plain HTTP; with `sslRequired=external`, Keycloak rejects logins with `{"error":"HTTPS required"}` when `X-Forwarded-Proto` is not guaranteed. The `adhar` PUT is a harmless 404 on the very first run (realm not yet created; it is later created with `sslRequired=NONE` from `realm-payload.json`).

### 4.3 Client reconcile loop (idempotent, every sync)

```
for c in argo adhar-console headlamp grafana argocd gitea prometheus vault harbor rustfs minio adhar-cli tekton; do
  pf=/var/config/${c}-client-payload.json; [ -f $pf ] || continue
  cid=$(jq -r .clientId $pf)
  id=$(GET clients?clientId=$cid | .[0].id)
  if id present:  PUT clients/$id  <- $pf         # reconcile redirect URIs/scopes/webOrigins
  else:           POST clients      <- $pf         # create missing client
                  attach the "groups" default-client-scope so tokens carry the groups claim
done
```

This is what makes config changes (e.g. the move to per-app subdomains, or a newly added client like `adhar-console`) reach an **already-provisioned** realm — without it the early-exit below would skip client setup and SSO would break with `redirect_uri` mismatch. Note the loop key `argo` maps to `argo-client-payload.json` whose `clientId` is `argo-workflows`.

### 4.4 Secret resync loop (every sync — keeps `keycloak-clients` authoritative)

If the `keycloak-clients` Secret exists, for each `clientId:KEYPREFIX` pair the script reads the client's **live** `.secret` from Keycloak and `kubectl patch`es it back into `keycloak-clients`:

```
for pair in argo-workflows:ARGO_WORKFLOWS adhar-console:ADHAR_CONSOLE headlamp:HEADLAMP \
            grafana:GRAFANA argocd:ARGOCD gitea:GITEA prometheus:PROMETHEUS vault:VAULT \
            harbor:HARBOR rustfs:RUSTFS tekton:TEKTON minio:MINIO jupyterhub:JUPYTERHUB; do
  patch keycloak-clients stringData {KEYPREFIX}_CLIENT_ID=$cid, {KEYPREFIX}_CLIENT_SECRET=<live secret>
done
```

Client secrets live in Keycloak and can drift from `keycloak-clients` (a reconcile PUT, a manual regenerate, a partial realm rebuild); when they drift the app holds a stale secret and its server-side token exchange fails, silently breaking SSO (observed on Grafana). Re-pushing the live secret every reconcile keeps every downstream OIDC `ExternalSecret` correct.

### 4.5 Idempotency gate — sentinel client, not bare realm

```
REALM_OK=1
GET admin/realms/adhar            || REALM_OK=0
if REALM_OK: SENTINEL=$(GET clients?clientId=argocd | .[0].id)
             if empty: "half-provisioned"; DELETE admin/realms/adhar; REALM_OK=0
if REALM_OK:                       # realm fully provisioned
   if keycloak-clients Secret missing (checked via SA token against kube-apiserver):
       rebuild it from the live realm (getsec per client + ARGOCD_SESSION_TOKEN)
   exit 0
set -e                             # fall through -> full provisioning
```

Bare realm existence is **not** proof of provisioning: a Job killed mid-run once left a clientless realm, and a naive early-exit then rebuilt `keycloak-clients` with empty values, breaking every login. The gate therefore requires the **`argocd` sentinel client** to be present; if the realm exists but the sentinel is missing it deletes the partial realm and re-provisions from scratch. The **rebuild-if-missing** branch covers the inverse (healthy realm, lost Secret — e.g. a namespace migration): it recreates `keycloak-clients` from the live client secrets (`getsec` helper) plus a best-effort `ARGOCD_SESSION_TOKEN`, rather than failing.

### 4.6 Full provisioning (fresh realm only, `set -e`)

Executed only when `REALM_OK=0`. Fatal on error (a genuinely fresh realm must fully succeed):

1. `POST admin/realms` ← `realm-payload.json` (`{"realm":"adhar","enabled":true,"sslRequired":"NONE"}`).
2. `POST client-scopes` ← `client-scope-groups-payload.json` (the `groups` scope).
3. Create groups: `admin`, `base-user`, then the platform RBAC groups `platform-admin`, `platform-developer`, `platform-viewer`.
4. Add the `oidc-group-membership-mapper` (`group-mapper-payload.json`) onto the `groups` scope so tokens carry a flat `groups` claim.
5. Create test users `user1` (→`/platform-admin`) and `user2` (→`/platform-developer`); set their passwords from `USER_PASSWORD` via `reset-password`.
6. Create each OIDC client (`POST clients` ← payload), look up its `id`, attach the `groups` default-client-scope, and capture `.secret` into a shell variable. Clients created here, in order: `argo-workflows`, `adhar-console`, `headlamp`, `grafana`, `argocd`, `gitea`, `prometheus`, `vault`, `harbor`, `rustfs`, `minio`, `adhar-cli` (public — no secret), `tekton`.
7. Fetch `ARGOCD_PASSWORD` from `argocd-initial-admin-secret` and mint a best-effort `ARGOCD_SESSION_TOKEN` (`// empty` + `|| true` keep it non-fatal — ArgoCD runs insecure HTTP on port 80 and may be briefly unreachable; aborting here under `set -e` would stall the whole chain).
8. Render `keycloak-clients` as YAML and `kubectl apply` it. Every value is **quoted** so an empty one (e.g. a missing `ARGOCD_SESSION_TOKEN`) is written as `""` rather than YAML `null` — an unquoted null makes `kubectl` drop the key and every dependent `ExternalSecret` fails with *"could not get secret data"*.

## 5. Clients and where their secrets land

| clientId | Type | `keycloak-clients` keys | Downstream consumer (ExternalSecret / target) |
|---|---|---|---|
| `argo-workflows` | confidential | `ARGO_WORKFLOWS_CLIENT_ID/SECRET` | `application/argo-workflows/.../external-secret.yaml` |
| `adhar-console` | confidential (BFF) | `ADHAR_CONSOLE_CLIENT_ID/SECRET` | `core/adhar-console` `console-oidc` → `console-env-vars`; tokens carry `aud=kubernetes` + `groups` for kube-apiserver impersonation |
| `headlamp` | confidential | `HEADLAMP_CLIENT_ID/SECRET` | `observability/headlamp/manifests/oidc.yaml` |
| `grafana` | confidential | `GRAFANA_CLIENT_ID/SECRET` | `grafana-oidc-external-secret.yaml` → `grafana-oidc` |
| `argocd` | confidential (sentinel) | `ARGOCD_CLIENT_ID/SECRET` + `ARGOCD_SESSION_TOKEN` | `argocd-oidc-external-secret.yaml` → `argocd-secret` (`creationPolicy: Merge`) |
| `gitea` | confidential | `GITEA_CLIENT_ID/SECRET` | `gitea-oauth-external-secret.yaml` → `gitea-oauth`; login source registered by `gitea-oauth-config` Job (wave 30) |
| `prometheus` | confidential | `PROMETHEUS_CLIENT_ID/SECRET` | `kube-prometheus/.../prometheus-oauth2-proxy.yaml` |
| `vault` | confidential | `VAULT_CLIENT_ID/SECRET` | `security/vault/manifests/oidc.yaml` |
| `harbor` | confidential | `HARBOR_CLIENT_ID/SECRET` | `application/harbor/manifests/oidc.yaml` |
| `rustfs` | confidential | `RUSTFS_CLIENT_ID/SECRET` | `data/rustfs/manifests/console-sso.yaml` |
| `minio` | confidential | `MINIO_CLIENT_ID/SECRET` | `data/minio/manifests/oidc.yaml`; hardcoded `policy=consoleAdmin` claim mapper |
| `tekton` | confidential | `TEKTON_CLIENT_ID/SECRET` | `application/tekton/manifests/dashboard-sso.yaml` |
| `adhar-cli` | **public** | none (public client) | `adhar auth login/token/whoami` password-grant; `groups` claim drives kube-RBAC |
| `jupyterhub` | confidential | `JUPYTERHUB_CLIENT_ID/SECRET` (resync only) | created by `data/jupyterhub/.../jupyterhub-config.yaml`'s own job; this Job only resyncs it |

`adhar-console` and `headlamp` and `adhar-cli` payloads also add an `oidc-audience-mapper` injecting `aud=kubernetes` (matching the kube-apiserver `--oidc-client-id`). Note the inline warning in the ConfigMap: a client `description` over 255 chars makes creation fail with an opaque 500, the non-fatal create silently skips the client, and the exported secret is empty — keep descriptions short.

## 6. Secret source & store (`secret-gen.yaml`)

- A `generators.external-secrets.io/v1alpha1 Password` generator `keycloak` (36 chars) feeds an `ExternalSecret` `keycloak-config` whose `target` Secret `keycloak-config` holds `KEYCLOAK_ADMIN_PASSWORD`, `KC_DB_PASSWORD`, `USER_PASSWORD`, etc. — this is the Secret the Job mounts at `/var/secrets/`.
- Two `ClusterSecretStore`s (`keycloak`, `gitea`) use the **kubernetes** provider with `remoteNamespace: adhar-system`, CA from the `kube-root-ca.crt` ConfigMap, and the `eso-store` ServiceAccount (RBAC: `secrets get/list/watch` + `selfsubjectrulesreviews create`). Every downstream OIDC `ExternalSecret` reads `keycloak-clients` through `secretStoreRef: {name: keycloak, kind: ClusterSecretStore}`.

## 7. Kubernetes RBAC hand-off (`k8s-rbac.yaml`)

The realm's platform groups map onto standard aggregated ClusterRoles via `ClusterRoleBinding`s keyed on the `groups` claim (prefixed `oidc:`): `platform-admin → cluster-admin`, `platform-developer → edit`, `platform-viewer → view`. This is why the config Job attaches the `groups` scope to every client and stamps `user1`/`user2` into the platform groups — the group claim is the authorization currency for `adhar auth` + kube-apiserver OIDC.

## 8. Failure modes & how the Job defends against each

| Failure | Defense in the script |
|---|---|
| Admin token expiry mid-run (60s default) | raise `accessTokenLifespan=1800`, re-fetch token first (§4.1) |
| Realm created but clients missing (killed mid-run) | sentinel `argocd` client gate → delete + re-provision (§4.5) |
| `keycloak-clients` lost but realm healthy | rebuild-if-missing branch from live secrets (§4.5) |
| Client secret drift (regenerate/PUT/partial rebuild) | per-client resync loop every sync (§4.4) |
| Config drift (redirect URIs/scopes) on existing realm | reconcile PUT loop every sync (§4.3) |
| `HTTPS required` behind the Gateway | `sslRequired=NONE` on master + adhar every sync (§4.2) |
| ArgoCD briefly down when minting session token | `// empty` + `|| true`, quoted empty value in Secret (§4.6) |
| Wave-40 ExternalSecrets read before Secret exists | Job is a `Sync` hook at wave 20, not `PostSync` (§3) |

## 9. Testing

No dedicated unit/envtest covers the bash Job (it is imperative Admin-API scripting, per the ADR's accepted trade-off). Coverage is behavioural:

- **e2e** `tests/e2e/bootstrap` — a full `adhar up` → verify → `adhar down` cycle; a green run implies the realm job produced `keycloak-clients` and the wave-40 ExternalSecrets resolved (otherwise Keycloak stays `OutOfSync` and the platform never reports deployed).
- **parity** `platform/controllers/adharplatform/parity_test.go` — asserts appset/package wiring integrity that keeps the keycloak package present and enabled.
- **Manual/`adhar get`** — `adhar get secrets` surfaces `keycloak-clients`; ArgoCD app health for `keycloak` gates on the hook Job succeeding.

Suggested additions: a lint/CI check that every `*-client-payload.json` `description` is ≤255 chars; a smoke test that `keycloak-clients` contains a non-empty `*_CLIENT_SECRET` for each confidential client after `adhar up`.

## 10. Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/security/keycloak/manifests/keycloak-config.yaml` | SA/Role/RoleBinding, `config-job` ConfigMap (all payloads), and the `config` Job (this design) |
| `platform/stack/packages/security/keycloak/manifests/install.yaml` | Keycloak Deployment (wave 10) + CNPG `keycloak-db` (wave 5) + backup (wave 6) |
| `platform/stack/packages/security/keycloak/manifests/secret-gen.yaml` | Password generator, `keycloak-config` Secret, `keycloak`/`gitea` ClusterSecretStores, `eso-store` SA/RBAC |
| `platform/stack/packages/security/keycloak/manifests/k8s-rbac.yaml` | group→ClusterRole bindings (`oidc:` prefixed) |
| `platform/stack/packages/security/keycloak/manifests/{argocd-oidc,gitea-oauth,grafana-oidc}-external-secret.yaml` | wave-40 consumers of `keycloak-clients` |
| `platform/stack/packages/security/keycloak/manifests/gitea-oauth-config.yaml` | wave-30 Job registering the Gitea login source from `keycloak-clients` |
| `platform/stack/packages/security/keycloak/manifests/httproute.yaml` | Keycloak Gateway route (wave 15) |
| `platform/stack/packages/{application/harbor,data/minio,data/rustfs,security/vault,observability/headlamp,observability/kube-prometheus,application/tekton,application/argo-workflows,core/adhar-console}/manifests/*` | per-service OIDC ExternalSecrets / oauth2-proxy wiring reading `keycloak-clients` |
| `platform/stack/packages/data/jupyterhub/manifests/jupyterhub-config.yaml` | independently creates the `jupyterhub` client; keycloak's Job only resyncs its secret |

## 11. Drift from the ADR (as-built notes)

- **`jupyterhub` split-brain.** The ADR says "one OIDC client per integrated service" created by this job. In practice the `jupyterhub` client is created by the **jupyterhub package's own** config job; the keycloak Job neither creates it in the full-provision path nor writes `JUPYTERHUB_*` into the fresh `keycloak-clients` build — it only appears in the §4.4 resync loop (patched *if* the client already exists). On a truly fresh cluster `keycloak-clients` has no `JUPYTERHUB_*` keys until jupyterhub's job runs. Two jobs writing the same Secret is a mild coupling the ADR does not call out.
- **Gitea SSO is not purely an ExternalSecret.** The ADR frames the credential hand-off as "ESO → per-service secrets". For Gitea the `gitea-oauth` ExternalSecret exists, but the actual login source is registered imperatively by the separate `gitea-oauth-config` Job (wave 30) running `gitea admin auth ...` inside the Gitea pod.
- **Rebuild path client set.** The rebuild-if-missing branch and the full-provision build write 12 confidential clients + session token (no `JUPYTERHUB`), while the resync loop covers 13 (adds `jupyterhub`) — the three producers of `keycloak-clients` are not perfectly symmetric.
