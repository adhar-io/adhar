# Low-Level Design — Keycloak as the platform identity provider (OIDC everywhere)

Detailed design for [ADR-0008](../adr/0008-keycloak-platform-identity.md). This is the authoritative as-built description of how a single Keycloak realm becomes the one identity for every human-facing surface on the platform: the realm/group model, the two OIDC integration idioms (native OIDC vs. oauth2-proxy sidecar), the group-claim → per-service-RBAC mappings, Kubernetes API OIDC, and the `adhar auth` CLI. The *bootstrap job* that provisions the realm and exports client secrets — the producer end of every wire described here — is owned by [ADR-0013](0013-sso-bootstrap-config-job.md); this doc treats `keycloak-clients` as a given and documents the **consumers**.

## 0. Context recap

The platform ships ~15 UIs, each with a local account system. ADR-0008 makes **Keycloak the sole IdP**: every identity-aware component authenticates via **OIDC against the `adhar` realm**, and authorization derives from a single flat **`groups` claim** mapped onto each service's native role model (ArgoCD RBAC, Gitea teams, Grafana org roles, Kubernetes RBAC). Keycloak ships as a curated-core package (`enabled: "true"` in `adhar-appset-local.yaml`), backed by CNPG PostgreSQL local and production alike. The realm is identical everywhere; only enterprise directories federate *into* it, so services never see anything but Keycloak.

## 1. Invariants

- **INV-1** One realm (`adhar`), one issuer. Every client trusts exactly `https://keycloak.<host>:<port>/realms/adhar` (local default `https://keycloak.adhar.localtest.me:8443/realms/adhar`).
- **INV-2** Authorization is carried by one **flat `groups` claim** (`full.path:false`), attached via the shared `groups` default-client-scope on every client. Group names (`platform-admin`/`platform-developer`/`platform-viewer`) are the only authorization currency; each service translates them locally.
- **INV-3** Client credentials never live in a package's manifests. They flow **Keycloak → `keycloak-clients` Secret → per-service `ExternalSecret`** through the kubernetes-backed `keycloak` `ClusterSecretStore` (ADR-0009/0013). A package references property names, never literals.
- **INV-4** Keycloak is availability-critical for **login only**. Running sessions, ArgoCD reconciliation, and workload service accounts are unaffected by a Keycloak outage; every UI keeps a documented local break-glass account.
- **INV-5** TLS to the issuer is the self-signed platform cert (`adhar-cert`), unknown ahead of time. Each consumer resolves this its own way — skip-verify, an injected CA bundle, or `oidc_discovery_ca_pem` — but **none** pins a CA at build time.

## 2. Realm model (`security/keycloak/manifests/keycloak-config.yaml`)

The bootstrap Job (ADR-0013) provisions, in the `adhar` realm:

- **Groups.** `admin`, `base-user` (legacy) plus the three platform RBAC groups **`platform-admin` / `platform-developer` / `platform-viewer`**. These three are the entire authorization vocabulary of INV-2.
- **The `groups` client-scope** (`client-scope-groups-payload.json`, `type: default`) carrying an `oidc-group-membership-mapper` (`group-mapper-payload.json`) with `claim.name: groups`, `full.path: false`, and `id/access/userinfo.token.claim: true`. Every client gets this scope attached as a *default* scope, so every issued token — id, access, and userinfo — carries `groups`.
- **Test users** `user1` → `/platform-admin`, `user2` → `/platform-developer` (passwords from the ESO-generated `USER_PASSWORD`).
- **One OIDC client per integrated service** (§3 table). Confidential clients export `.secret` into `keycloak-clients`; `adhar-cli` is public.

Keycloak runs `quay.io/keycloak/keycloak:26.7.1` in `start-dev` with **hostname v2** (Keycloak 26): a single absolute frontend URL **`hostname=https://keycloak.adhar.localtest.me:8443`** (plus `hostname-admin` the same), **`proxy-headers=xforwarded`** + `http-enabled=true` (TLS terminates at the Cilium Gateway; the v1 `proxy=edge`/`hostname-port`/`hostname-strict-backchannel` keys were removed in v2), all in the `install.yaml` ConfigMap. In v2 backchannel (server-side OIDC) requests resolve to that fixed `hostname` by default (this replaces the old `hostname-strict-backchannel=true`): the console and other services do server-side OIDC over the in-cluster HTTP backchannel (`…svc:8080`), and the fixed `https` frontend URL keeps discovery URLs and the token `iss` claim on `https`, so both id-token verification and kube-apiserver acceptance (whose `--oidc-issuer-url` is `https`) work. Health (`/health/*`) + metrics (`/metrics`) are served on the dedicated management interface (port 9000); readiness/liveness/startup probes and the ServiceMonitor target it. The bootstrap admin uses `KC_BOOTSTRAP_ADMIN_USERNAME/PASSWORD` (Keycloak 26 renamed the deprecated `KEYCLOAK_ADMIN*`). Login pages use the custom **`adhar`** login theme (§2.1). Hardening: non-root/dropped-caps/seccomp `securityContext`, CPU/memory requests+limits, `KC_LOG_LEVEL=INFO`.

### 2.1 Login theme (Adhar branding)

Every login-flow page (sign-in, registration, password reset, OTP, error) uses the custom **`adhar`** Keycloak login theme, so the SSO entry point matches the Adhar design system rather than stock Keycloak.

- **Source of truth**: `security/keycloak/theme/adhar/login/` — `theme.properties` (`parent=keycloak`, `import=common/keycloak`, `styles=css/styles.css css/adhar.css`), `resources/css/adhar.css` (the full restyle), and `resources/img/adhar-{logo,symbol}.svg`. Design tokens from the brand system (`docs/images/branding/`): the Blue→Indigo→Violet gradient (`#3B82F6`→`#6366F1`→`#8B5CF6`), slate neutrals, Inter type stack (system fallback — no web-font fetch, air-gap safe), the "Open Cloud-Native Foundation" tagline and "Adhar • Built with ❤️ for developers!" footer.
- **No FreeMarker override**: the theme ships **no `.ftl`** files — it inherits all base templates and form logic unchanged (zero risk to username/password, social login, remember-me, registration, reset). Branding is CSS-only: the logo is a `background-image` on the header, tagline/footer are `::after` content, and the primary button/hairline carry the brand gradient. Selectors target both PatternFly v4 (`.pf-c-*`) and v5 (`.pf-v5-c-*`) so it degrades gracefully.
- **Delivery**: the theme files are baked into the `keycloak-theme-adhar` ConfigMap (`manifests/theme-configmap.yaml`) and mounted at `/opt/keycloak/themes/adhar` via a ConfigMap volume whose `items[].path` reconstructs the nested `login/…` layout (ConfigMap keys cannot contain `/`). The realm's `loginTheme` is set to `adhar` in both the realm-creation payload and the idempotent sync PUT (`keycloak-config.yaml`), so it survives realm re-imports. `start-dev` disables theme caching, so a pod `rollout restart` picks up edits. See `security/keycloak/theme/README.md` for the iteration workflow and the base-stylesheet-name caveat.

## 3. The client inventory & credential hand-off

The realm Job writes `keycloak-clients` (namespace `adhar-system`); every consumer below reads it through `secretStoreRef: {name: keycloak, kind: ClusterSecretStore}` at **sync-wave 40** (the Job writes it at wave 20).

| clientId | Integration idiom | Consumer manifest → target Secret / config |
|---|---|---|
| `argocd` | native OIDC (`oidc.config`) | `argocd-oidc-external-secret.yaml` → `argocd-secret` (`creationPolicy: Merge`) |
| `gitea` | native OIDC login source (imperative) | `gitea-oauth-external-secret.yaml` → `gitea-oauth`; source registered by `gitea-oauth-config` Job (wave 30) |
| `grafana` | native OIDC (`generic_oauth`) | `grafana-oidc-external-secret.yaml` → `grafana-oidc` (env vars) |
| `adhar-console` | native OIDC (server-side BFF) | `core/adhar-console/manifests/install.yaml` `console-oidc` → `console-env-vars` |
| `headlamp` | native OIDC sign-in | `observability/headlamp/manifests/oidc.yaml` → `oidc` |
| `vault` | native OIDC auth method | `security/vault/manifests/oidc.yaml` → `vault-oidc` (+ `vault-ca`) |
| `harbor` | native OIDC | `application/harbor/manifests/oidc.yaml` |
| `minio` | native OIDC (+ hardcoded `policy=consoleAdmin` mapper) | `data/minio/manifests/oidc.yaml` |
| `prometheus` | **oauth2-proxy sidecar** | `observability/kube-prometheus/manifests/prometheus-oauth2-proxy.yaml` |
| `rustfs` | oauth2-proxy sidecar | `data/rustfs/manifests/console-sso.yaml` |
| `tekton` | oauth2-proxy sidecar | `application/tekton/manifests/dashboard-sso.yaml` |
| `argo-workflows` | native OIDC (SSO) | `application/argo-workflows/.../external-secret.yaml` |
| `adhar-cli` | **public** client, password grant | `adhar auth login/token/whoami` (§6) |
| `jupyterhub` | native OIDC | created by jupyterhub's own job; keycloak only resyncs the secret |

Two structural idioms cover every service:

1. **Native OIDC** — the service speaks OIDC itself; the `ExternalSecret` just lands the client secret where the service reads it (an env var, a chart Secret key, or a config file). ArgoCD, Grafana, Gitea, Console, Headlamp, Vault, Harbor, MinIO, Argo Workflows.
2. **oauth2-proxy sidecar** — the service has no native OIDC, so an `oauth2-proxy` in front of it does the code flow and forwards authenticated requests. Prometheus, RustFS, Tekton. Flow: `browser → Gateway (svc.adhar.localtest.me) → oauth2-proxy → upstream`.

## 4. Per-service authorization mapping (the group claim in practice)

Every service receives the same `groups` claim and translates it locally. The mappings ship in-repo:

**ArgoCD** (`resources/argocd/install.yaml`, `argocd-cm`/`argocd-rbac-cm`):
```yaml
oidc.config: |
  name: Keycloak
  issuer: https://keycloak.adhar.localtest.me:8443/realms/adhar
  clientID: argocd
  clientSecret: $oidc.keycloak.clientSecret        # resolved from argocd-secret (ES Merge)
  requestedScopes: [openid, profile, email, groups, offline_access]   # offline_access → refresh-token session renewal (ArgoCD v3.5)
  requestedIDTokenClaims: { groups: { essential: true } }
oidc.tls.insecure.skip.verify: "true"              # self-signed issuer (INV-5)
# policy.csv:
g, platform-admin,     role:admin
g, platform-developer, role:platform-developer      # custom p-rules: get/sync/logs/repos
g, platform-viewer,    role:readonly
scopes: '[groups]'
```

**Grafana** (`kube-prometheus/values.yaml`, `grafana.ini` `auth.generic_oauth`) — `role_attribute_path` is a JMESPath over the same claim:
```
contains(groups[*], 'platform-admin') && 'Admin' || contains(groups[*], 'platform-developer') && 'Editor' || 'Viewer'
```
`tls_skip_verify_insecure: true` handles the self-signed token/userinfo endpoints; client id/secret arrive via `envFromSecrets: [{name: grafana-oidc, optional: true}]` (optional so Grafana health never gates on Keycloak).

**Gitea** (`gitea-oauth-config.yaml`, wave-30 Job running `gitea admin auth add-oauth`/`update-oauth` inside the Gitea pod) maps groups → Gitea teams via `--group-team-map`, and — because `gitea admin auth` has no skip-TLS flag — builds a CA bundle from the image CAs + `adhar-cert` and exports `SSL_CERT_FILE`:
```
--group-claim-name groups  --admin-group platform-admin
--group-team-map '{"platform-admin":{"adhar":["Owners"]},"platform-developer":{"adhar":["developers"]},"platform-viewer":{"adhar":["viewers"]}}'
--group-team-map-removal
```
This is the same `adhar` org / `Owners`/`developers`/`viewers` teams the GitOps seeding creates (ADR-0003).

**Kubernetes API** (§5) binds the same three groups onto aggregated ClusterRoles.

## 5. Kubernetes API OIDC (`k8s-rbac.yaml` + `kind.yaml.tmpl`)

Human access to the API server flows through the same realm. The Kind apiserver is templated (`platform/providers/kind/resources/kind.yaml.tmpl`) with:

```yaml
oidc-issuer-url: "{{ .OIDCIssuerURL }}"      # https://keycloak.<host>:<port>/realms/adhar
oidc-client-id: "kubernetes"
oidc-ca-file: "{{ .OIDCCAPath }}"            # platform cert on-node (/etc/adhar/pki/…)
oidc-username-claim: "preferred_username"
oidc-username-prefix: "oidc:"
oidc-groups-claim: "groups"
oidc-groups-prefix: "oidc:"
```

`OIDCIssuerURL` is built by `(*Cluster).oidcIssuerURL()` (`platform/providers/kind/cluster.go`) from the same host/port the platform serves on; `OIDCCAPath` = the platform cert materialised into the node before kubeadm init (`ensurePlatformPKI`, mounted via `extraVolumes` — kubeadm otherwise won't see the CA and the apiserver won't boot).

`k8s-rbac.yaml` binds the `oidc:`-prefixed groups onto the standard aggregated roles:

| Realm group (as API sees it) | ClusterRole |
|---|---|
| `oidc:platform-admin` | `cluster-admin` |
| `oidc:platform-developer` | `edit` |
| `oidc:platform-viewer` | `view` |

The `oidc:` prefix guarantees a federated identity can never collide with a built-in `system:` user/group. Note the apiserver client id is **`kubernetes`**, not `adhar-cli`: tokens are accepted because `adhar-cli`, `adhar-console`, and `headlamp` all carry an `oidc-audience-mapper` injecting `aud=kubernetes` (see their payloads), so a CLI/console/headlamp token validates against `--oidc-client-id=kubernetes`.

### 5.1 The loopback issuer problem (`oidc-loopback-proxy.yaml`)

kube-apiserver runs in the node's **host** network namespace and must reach the *public* issuer URL for discovery+JWKS. `*.localtest.me` resolves to `127.0.0.1`, but nothing on the node's loopback listens on 8443 (the Kind host→node port map exists only on the Docker-host side, and Cilium doesn't translate loopback node-port connects). The `oidc-loopback-proxy` **DaemonSet** (wave 0, `hostNetwork: true`, `dnsPolicy: ClusterFirstWithHostNet`, control-plane-tolerated) runs `socat TCP4-LISTEN:8443,bind=127.0.0.1 → cilium-gateway-adhar-gateway.adhar-system.svc:8443` (TLS passthrough, so the platform cert still validates). It depends on Cilium socketLB (`hack/cilium/values.yaml`) to translate the host-namespace connect to the Gateway ClusterIP.

## 6. The CLI: `adhar auth` (`cmd/auth/`)

`adhar auth` is a first-class OIDC client of the realm using the public `adhar-cli` client. Defaults (`cmd/auth/keycloak.go`): issuer `https://keycloak.adhar.localtest.me:8443/realms/adhar`, client `adhar-cli`, realm `adhar`. All overridable via persistent flags (`--issuer/--realm/--client-id/--client-secret/--admin-url/--admin-token/--insecure`).

- **`adhar auth login [user]`** (`login.go`) → `keycloak.passwordGrant` (OIDC ROPC grant on `…/protocol/openid-connect/token`, `client_id=adhar-cli`). On success `saveSession` persists the token pair to `credentialsPath()` (0600, honouring `ADHAR_CONFIG_DIR`) and prints the user's groups from `parseClaims`.
- **`adhar auth token`** (`token.go`) → prints a valid access token for the saved session, transparently refreshing via `currentSession`/`refreshGrant` (`credentials.go`). Composes directly with kube: `kubectl --token "$(adhar auth token)" get pods`.
- **`adhar auth whoami`** (`whoami.go`) → decodes the (unverified, display-only) JWT and shows username/email/**groups**/realm roles/expiry.
- **`adhar auth logout`** (`logout.go`) → `endSession` (revokes the refresh token) + `deleteSession`.
- `user`/`group`/`role` subcommands read the Keycloak **Admin REST API** (`adminGet`, bearer resolved via `--admin-token` or a `client_credentials` grant).

Because `adhar-cli` tokens carry `groups` (INV-2) *and* `aud=kubernetes` (§5), the single `adhar auth login` session authorizes both the CLI's Keycloak Admin views and direct `kubectl` against the group→RBAC bindings — one login, everything.

## 7. TLS-to-issuer strategies (INV-5, catalogued)

The issuer is served by the Cilium Gateway with the per-cluster self-signed `adhar-cert`, unknown at build time. Each consumer resolves this differently — worth cataloguing because it is the single most common SSO break:

| Consumer | Strategy |
|---|---|
| ArgoCD | `oidc.tls.insecure.skip.verify: "true"` |
| Grafana | `tls_skip_verify_insecure: true` |
| Gitea | inject `adhar-cert` into a CA bundle, `SSL_CERT_FILE` (no skip-TLS flag exists) |
| Vault | `vault-ca` ExternalSecret → `oidc_discovery_ca_pem` |
| adhar-console | in-cluster HTTP backchannel (`KEYCLOAK_INTERNAL_URL=http://…svc:8080`), avoids TLS entirely; the fixed hostname-v2 frontend URL keeps the `iss` claim `https` |
| kube-apiserver | `--oidc-ca-file` = the platform cert on-node |
| `adhar auth` CLI | `--insecure` opt-in |

## 8. Console: the BFF / kube-impersonation path

`adhar-console` is the most involved consumer and the reason the `aud=kubernetes` + `groups` design matters. Its `install.yaml` env: `KEYCLOAK_URL=https://keycloak.adhar.localtest.me:8443` (browser-facing), `KEYCLOAK_INTERNAL_URL=http://keycloak.adhar-system.svc.cluster.local:8080` (server-side discovery/token/JWKS over the backchannel), `KEYCLOAK_CLIENT_ID=adhar-console`, `AUTH_PUBLIC_URL=https://console.adhar.localtest.me:8443`, `AUTH_SCOPES=openid profile email offline_access`. `AUTH_CLIENT_SECRET` is templated from `ADHAR_CONSOLE_CLIENT_SECRET` (via the `console-oidc` ExternalSecret). The console runs a server-side authorization-code (BFF) flow; the resulting access token carries `aud=kubernetes` + `groups`, which the console uses for **per-user kube-apiserver impersonation** — so a console user's Kubernetes actions are RBAC-checked as themselves, not as a shared service account.

## 9. Failure modes & how the design defends

| Failure | Defense |
|---|---|
| Keycloak down | INV-4: only login blocked; sessions/reconciliation/workloads unaffected; every UI keeps a local break-glass account |
| Self-signed issuer cert unknown to a client | per-consumer TLS strategy (§7) — never a build-time CA pin |
| Consumer `ExternalSecret` reconciled before `keycloak-clients` exists | consumers pinned to sync-wave 40; realm Job writes the Secret at wave 20 (ADR-0013) |
| ES created with `refreshInterval: 0` before the Job → wedged `SecretSyncedError` | headlamp/vault set `refreshInterval: 1h` so it retries |
| apiserver can't reach the loopback issuer | `oidc-loopback-proxy` DaemonSet + Cilium socketLB (§5.1) |
| Grafana health gated on Keycloak | `grafana-oidc` ES lives in the *keycloak* package + `envFromSecrets … optional: true` |
| Token `iss`/discovery scheme mismatch over backchannel | the fixed hostname-v2 frontend URL (`hostname=https://…:8443`) forces the `https` issuer (§2) |
| Client secret drift breaks server-side token exchange | realm Job's per-client secret resync loop keeps `keycloak-clients` authoritative (ADR-0013 §4.4) |

## 10. Testing

- **e2e** (`tests/e2e/bootstrap`, `make e2e`) — a green `adhar up` implies the realm Job produced `keycloak-clients` and every wave-40 consumer ES resolved; otherwise the keycloak Application stays `OutOfSync` and the platform never reports deployed.
- **parity** (`platform/controllers/adharplatform/parity_test.go`) — keeps the keycloak package present + `enabled: "true"` in the appset so the whole SSO chain can't be silently dropped.
- **CLI unit** (`cmd/auth/credentials_test.go`) — session persistence / claim parsing for `adhar auth`.
- **Manual** — `adhar auth login user1 --insecure && adhar auth whoami` should show `platform-admin`; `kubectl --token "$(adhar auth token)" auth can-i '*' '*'` should be `yes` (via `oidc:platform-admin → cluster-admin`).
- **Suggested additions** — a smoke check that each confidential client resolves a non-empty `*_CLIENT_SECRET`; a lint that every service with an OIDC redirect URI has a matching client payload and a wave-40 ExternalSecret.

## 11. Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/security/keycloak/manifests/install.yaml` | Keycloak 26.7.1 Deployment/Service/ConfigMap (hostname v2, management port 9000, `securityContext`/probes/resources), the `adhar` login-theme ConfigMap mount, CNPG `keycloak-db` + backup |
| `.../keycloak/manifests/keycloak-config.yaml` | realm/group/client provisioning Job + all payloads (owned by ADR-0013) |
| `.../keycloak/manifests/secret-gen.yaml` | ESO Password generator, `keycloak`/`gitea` `ClusterSecretStore`s, `eso-store` SA/RBAC |
| `.../keycloak/manifests/k8s-rbac.yaml` | `oidc:platform-*` group → aggregated ClusterRole bindings |
| `.../keycloak/manifests/oidc-loopback-proxy.yaml` | host-loopback `socat` DaemonSet so the apiserver reaches the issuer (§5.1) |
| `.../keycloak/manifests/httproute.yaml` | Keycloak Gateway route (wave 15, own subdomain, serves at `/`) |
| `.../keycloak/manifests/{argocd-oidc,grafana-oidc,gitea-oauth}-external-secret.yaml` | wave-40 native-OIDC credential consumers |
| `.../keycloak/manifests/gitea-oauth-config.yaml` | wave-30 Job registering Gitea's OIDC login source + group→team map |
| `platform/controllers/adharplatform/resources/argocd/install.yaml` | ArgoCD `oidc.config` + `policy.csv` group→role RBAC |
| `platform/stack/packages/observability/kube-prometheus/values.yaml` | Grafana `generic_oauth` + `role_attribute_path` group mapping |
| `.../observability/kube-prometheus/manifests/prometheus-oauth2-proxy.yaml` | oauth2-proxy sidecar idiom (Prometheus has no native OIDC) |
| `.../observability/headlamp/manifests/oidc.yaml` | Headlamp native-OIDC `oidc` Secret |
| `.../security/vault/manifests/oidc.yaml` | Vault OIDC auth-method inputs (`vault-oidc` + `vault-ca`) |
| `.../core/adhar-console/manifests/install.yaml` | Console BFF OIDC env + `console-oidc` ExternalSecret; kube impersonation |
| `.../data/{minio,rustfs}/manifests/*`, `.../application/{harbor,tekton,argo-workflows}/manifests/*` | remaining native-OIDC / oauth2-proxy consumers |
| `.../data/jupyterhub/manifests/jupyterhub-config.yaml` | independently creates the `jupyterhub` client (keycloak only resyncs its secret) |
| `platform/providers/kind/resources/kind.yaml.tmpl` | kube-apiserver `--oidc-*` flags |
| `platform/providers/kind/{cluster.go,config.go}` | `oidcIssuerURL()`, `OIDCIssuerURL`/`OIDCCAPath`, `ensurePlatformPKI` |
| `cmd/auth/{auth,login,token,whoami,logout,keycloak,credentials,session,user,group,role}.go` | `adhar auth` OIDC CLI (`adhar-cli` public client, password/refresh grants, Admin REST) |

## 12. Drift from the ADR (as-built notes)

- **Two apiserver audiences.** The ADR frames "Kubernetes API access uses OIDC group claims"; in practice the apiserver's `--oidc-client-id` is a distinct **`kubernetes`** audience, and `adhar-cli`/`adhar-console`/`headlamp` reach it only via an `oidc-audience-mapper` injecting `aud=kubernetes`. A confidential client without that mapper (e.g. `grafana`) cannot authenticate to the API server — intentional, but not spelled out in the ADR.
- **Break-glass is documented, not yet rotated into Vault.** ADR-0008 describes bootstrap creds (`gitea_admin`, ArgoCD `admin`) being rotated into Vault as break-glass once SSO is wired. As built they remain plain day-0 credentials (each service's local login is the break-glass path per INV-4); the Vault rotation is roadmap, not implemented.
- **"oauth2-proxy where they lack native OIDC" is a 3-service minority.** The ADR lists oauth2-proxy as a general fallback; in practice only Prometheus, RustFS, and Tekton use it — every other service (including Vault, Harbor, MinIO) speaks native OIDC.
- **Gitea SSO is imperative, not purely declarative.** The `gitea-oauth` ExternalSecret exists, but the actual login source is registered by a `gitea admin auth` Job inside the Gitea pod (wave 30), because Gitea has no declarative OIDC config surface. (Also noted in ADR-0013.)
- **`start-dev` (Keycloak 26).** The local Keycloak runs in dev mode with a fixed hostname-v2 frontend URL and `sslRequired=NONE` (set every sync by the realm Job) — appropriate for local, explicitly not production posture. Production hardening is `start --optimized` behind the LoadBalancer Gateway with cert-manager TLS; the config, probes, resources, and `securityContext` are already production-shaped.
