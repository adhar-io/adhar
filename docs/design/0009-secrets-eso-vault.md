# Low-Level Design — External Secrets Operator as sync plane, Vault as source of truth, never Git

Detailed design for [ADR-0009](../adr/0009-secrets-eso-vault.md). This is the authoritative
as-built design for how secrets enter an Adhar cluster: the External Secrets Operator (ESO) sync
plane, the HashiCorp Vault source of truth, the `ClusterSecretStore` fabric that fronts both Vault
and in-cluster reflection, the `ExternalSecret` pointer contract that GitOps manifests carry, the
sync-wave ordering that makes the whole chain converge, and the rotation/enforcement story.

## 0. Context recap

GitOps makes Git the only write path ([ADR-0001](../adr/0001-management-cluster-first.md)), but
secrets must never be committed. ADR-0009 puts secret *material* outside Git in a dedicated store and
uses **ESO to sync it into Kubernetes `Secret`s**; Git carries only `ExternalSecret` *pointers*.
**Vault is the default source of truth** (shipped as a curated-core package); a cloud secret manager
plugs in as a different `SecretStore` without touching any application manifest. Both ESO and Vault
are `enabled: "true"` in the local curated core
([`platform/stack/adhar-appset-local.yaml`](../../platform/stack/adhar-appset-local.yaml) — the
`external-secrets` and `vault` list elements, `category: security`).

## 1. The two-layer secret fabric

```
                 write path (never Git)                    sync plane                consumers
  ┌───────────────────────────────────┐         ┌───────────────────────┐    ┌──────────────────┐
  │ Vault KV v2  secret/…              │  ◀────  │ ClusterSecretStore     │    │ k8s Secret       │
  │ (source of truth, ADR default)     │         │   "vault"  (provider   │───▶│ (mounted/env by  │
  ├───────────────────────────────────┤         │    vault)              │    │  a pod)          │
  │ in-cluster Secrets in adhar-system │  ◀────  │ ClusterSecretStore     │    └──────────────────┘
  │ (Keycloak client secrets, CLI-gen, │         │   keycloak/gitea/argocd│         ▲
  │  self-signed CA)                   │         │   (provider kubernetes)│         │ ExternalSecret
  └───────────────────────────────────┘         └───────────────────────┘─────────┘ (the Git pointer)
```

Two ESO provider families are actually in use, both fronted by cluster-scoped `ClusterSecretStore`s
so any namespace can reference them:

- **`provider: vault`** — the ADR's source-of-truth path. One store, `vault`, backed by the in-cluster
  Vault KV v2 engine. Anything an operator writes with `vault kv put secret/…` is reachable.
- **`provider: kubernetes`** — in-cluster *reflection*. Three stores (`keycloak`, `gitea`, `argocd`)
  that read `Secret`s already living in `adhar-system` (Keycloak-issued OIDC client secrets, the
  self-signed platform CA, ESO-`Password`-generated credentials) and re-project them into the exact
  `Secret` name/shape each consumer chart expects. This is how SSO wiring
  ([ADR-0008](../adr/0008-keycloak-platform-identity.md)) crosses packages without any secret in Git.

The manifests are identical in either case — only the store's `provider` block differs, which is
exactly the store-agnostic swap ADR-0009 promises.

## 2. External Secrets Operator — the sync plane

Installed from the upstream Helm chart, rendered to a single embedded manifest
([`security/external-secrets/generate-manifests.sh`](../../platform/stack/packages/security/external-secrets/generate-manifests.sh),
`CHART_VERSION="2.5.0"`, app `v2.5.0`) into
[`manifests/install.yaml`](../../platform/stack/packages/security/external-secrets/manifests/install.yaml).
Everything lands in `adhar-system` ([ADR-0011 shared namespace](../adr/0011-shared-platform-namespace.md)):
three Deployments — `external-secrets` (controller), `external-secrets-webhook`,
`external-secrets-cert-controller` — plus the CRDs (`ExternalSecret`, `ClusterExternalSecret`,
`SecretStore`, `ClusterSecretStore`, `PushSecret`, and the `generators.external-secrets.io` set).

**Webhook fails open.** [`values.yaml`](../../platform/stack/packages/security/external-secrets/values.yaml)
sets `webhook.failurePolicy: Ignore`. With the chart default (`Fail`) the apiserver rejects every
`ExternalSecret`/`ClusterSecretStore` apply whenever the webhook is briefly unreachable — routine
during bootstrap, since the webhook and its cert-controller come up alongside the very
`ExternalSecret`s that depend on them. Fail-open avoids wedging the whole SSO chain (e.g. Keycloak's
`keycloak-config` `ExternalSecret` never applying → Postgres `StatefulSet` stuck in
`CreateContainerConfigError`).

**Generators.** ESO's `generators.external-secrets.io/v1alpha1` `Password` generator is used to *mint*
secrets that have no external source — the Keycloak admin/DB passwords are generated in
[`keycloak/secret-gen.yaml`](../../platform/stack/packages/security/keycloak/manifests/secret-gen.yaml)
(`length: 36`, 5 digits, 5 symbols) and materialised via an `ExternalSecret` `dataFrom.generatorRef`.

## 3. Vault — the source of truth

Chart `hashicorp/vault` `0.32.0`, image `hashicorp/vault:1.21.2`, rendered to
[`vault/manifests/install.yaml`](../../platform/stack/packages/security/vault/manifests/install.yaml)
by [`generate-manifests.sh`](../../platform/stack/packages/security/vault/generate-manifests.sh). It
runs as a **single-node standalone** server (`StatefulSet/vault`, file storage `/vault/data`, 1Gi)
per [`values.yaml`](../../platform/stack/packages/security/vault/values.yaml). Notable local tuning:

- **Readiness deadlock fix.** `readinessProbe.path:
  /v1/sys/health?standbyok=true&sealedcode=204&uninitcode=204` — the chart default probe (`vault
  status`) exits non-zero while sealed, so the pod never becomes `Ready`, so ArgoCD's sync-waves never
  advance to the bootstrap Job that unseals it. Returning 204 for sealed/uninitialised breaks the
  deadlock.
- UI exposed through the Cilium Gateway on its **own host** `vault.adhar.localtest.me`
  ([`httproute.yaml`](../../platform/stack/packages/security/vault/manifests/httproute.yaml)) — a
  dedicated host, not a sub-path, because the Vault UI hardcodes root-absolute asset paths.
- Prometheus telemetry: `unauthenticated_metrics_access = true` on the listener, scraped by
  [`servicemonitor.yaml`](../../platform/stack/packages/security/vault/manifests/servicemonitor.yaml)
  at `/v1/sys/metrics`.

### 3.1 Bootstrap Job (init / unseal / configure)

[`vault/manifests/bootstrap.yaml`](../../platform/stack/packages/security/vault/manifests/bootstrap.yaml)
is a fully idempotent `vault-bootstrap` Job (`argocd.argoproj.io/hook: Sync`,
`hook-delete-policy: BeforeHookCreation`, so it re-runs every sync and reconciles drift). It carries
its own `ServiceAccount`/`Role`/`RoleBinding` (get/create/patch `secrets` in `adhar-system`). Steps:

1. Fetches a static `kubectl` (the Vault image has none), then waits for `vault status` to answer.
2. `vault operator init -key-shares=1 -key-threshold=1`; stores the init JSON (unseal key + root
   token) in the `vault-keys` `Secret`. *Dev-only* — see §7.
3. Unseals with the stored key (skips if already unsealed).
4. Logs in with the root token and idempotently configures:
   - **KV v2** at `secret/`,
   - **`kubernetes` auth** method (mount `kubernetes/`) configured from the in-cluster
     `KUBERNETES_SERVICE_HOST`/CA; token review runs under Vault's own pod SA via its
     `system:auth-delegator` binding,
   - policy **`external-secrets`** = read on `secret/data/*` + `secret/metadata/*`,
   - kubernetes auth **role `external-secrets`** bound to `bound_service_account_names=external-secrets`,
     `bound_service_account_namespaces=adhar-system`, `ttl=1h`.
5. Best-effort **OIDC auth** (Keycloak): reads the `vault-oidc` client secret and `vault-ca`
   (both delivered by `ExternalSecret`s in
   [`oidc.yaml`](../../platform/stack/packages/security/vault/manifests/oidc.yaml)); if absent it
   skips and configures on a later sync. Enables the "Sign in with OIDC" flow against
   `realms/adhar`.

## 4. The store plane — four `ClusterSecretStore`s

| Store | Provider | Backend / read scope | Auth | Defined in |
|---|---|---|---|---|
| `vault` | `vault` | `http://vault.adhar-system.svc.cluster.local:8200`, `path: secret`, `version: v2` | k8s auth, mount `kubernetes`, role `external-secrets`, SA `external-secrets`/`adhar-system` | [`vault/manifests/vault-clustersecretstore.yaml`](../../platform/stack/packages/security/vault/manifests/vault-clustersecretstore.yaml) |
| `keycloak` | `kubernetes` | `remoteNamespace: adhar-system` | SA `eso-store` | [`keycloak/secret-gen.yaml`](../../platform/stack/packages/security/keycloak/manifests/secret-gen.yaml) |
| `gitea` | `kubernetes` | `remoteNamespace: adhar-system` | SA `eso-store` | [`keycloak/secret-gen.yaml`](../../platform/stack/packages/security/keycloak/manifests/secret-gen.yaml) |
| `argocd` | `kubernetes` | `remoteNamespace: adhar-system` | SA `eso-store-argocd` | [`core/adhar-console/manifests/argocd-secrets.yaml`](../../platform/stack/packages/core/adhar-console/manifests/argocd-secrets.yaml) |

The `vault` store lives in the **vault** package, not external-secrets, on purpose: its readiness
depends on Vault being initialised/unsealed with the `external-secrets` role, so keeping it here means
the external-secrets `Application` reports `Healthy` as soon as its controllers are up, and the
store's transient not-ready window is attributed to the app that owns it (`sync-wave: "3"`, just after
the wave-2 bootstrap Job). The `kubernetes`-provider stores authenticate via a least-privilege
`eso-store` `ServiceAccount` (`get/list/watch secrets` + `create selfsubjectrulesreviews` in
`adhar-system` — defined alongside them in `secret-gen.yaml`) and read the platform namespace with
the cluster CA from the `kube-root-ca.crt` ConfigMap.

## 5. The pointer contract — `ExternalSecret`

Every `ExternalSecret` in Git carries pointers only. Three idioms appear across the 22 objects in the
repo:

**(a) Vault-backed (the ADR default path).** Reference the `vault` store by KV path + property, e.g.
the documented pattern `vault kv put secret/myapp/config …` then:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
spec:
  secretStoreRef: { name: vault, kind: ClusterSecretStore }
  data:
    - secretKey: password
      remoteRef: { key: myapp/config, property: password }
```

**(b) Reflection of a Keycloak-issued client secret** — the dominant idiom in the curated core, via a
`kubernetes`-provider store. From
[`keycloak/argocd-oidc-external-secret.yaml`](../../platform/stack/packages/security/keycloak/manifests/argocd-oidc-external-secret.yaml):

```yaml
spec:
  secretStoreRef: { name: keycloak, kind: ClusterSecretStore }
  target:
    name: argocd-secret
    creationPolicy: Merge          # patch one OIDC key; chart owns the rest of argocd-secret
  data:
    - secretKey: oidc.keycloak.clientSecret
      remoteRef: { key: keycloak-clients, property: ARGOCD_CLIENT_SECRET }
```

`creationPolicy: Merge` is the key move for injecting a single field into a chart-owned `Secret`. The
same pattern feeds Grafana, Gitea OAuth, Argo Workflows, Harbor, Headlamp, MinIO, Tekton, Kargo, etc.

**(c) Generated** — `dataFrom.sourceRef.generatorRef` pointing at a `Password` generator (§2), the
source for Keycloak's own `keycloak-config` `Secret`.

**`refreshInterval` discipline.** SSO consumers use `1h`, **not `0`** — a `0`/sync-once
`ExternalSecret` created before the Keycloak realm Job has written `keycloak-clients` wedges as
`SecretSyncedError` forever with no retry (documented inline in `oidc.yaml`). Generated one-shots
(`keycloak-config`) use `'0'` because their generator is always available.

**Labels.** ESO-generated `Secret`s are stamped `adhar.io/cli-secret: 'true'` and
`adhar.io/package-name: <pkg>` via the `target.template.metadata.labels` so platform tooling can find
platform-managed credentials.

## 6. End-to-end sync + ordering

The chain converges by ArgoCD **sync-waves**, since ESO, Vault, its bootstrap, and the consumers all
land in one wave-ordered `Application`:

```
wave 0   Vault chart objects (StatefulSet/Service) + external-secrets controllers
wave 1   vault-bootstrap RBAC + ConfigMap(bootstrap.sh)
wave 2   vault-bootstrap Job  → init/unseal, enable KV v2 + kubernetes auth + es role  (Sync hook, re-runs)
wave 3   ClusterSecretStore "vault"  → first auth check succeeds immediately (no long Degraded window)
wave 20  Keycloak realm Job writes keycloak-clients Secret
wave 40  ExternalSecrets that read keycloak-clients (argocd-oidc, vault-oidc, grafana, …)
```

Runtime flow for one `ExternalSecret`:

```
apply ExternalSecret (Git) → ESO controller resolves secretStoreRef
  → store authenticates (vault: k8s-auth login → external-secrets policy | kubernetes: eso-store SA)
  → fetch remoteRef(s) from Vault KV / adhar-system Secret / generator
  → render target Secret (creationPolicy Owner|Merge, optional template) into the ES namespace
  → re-pull every refreshInterval; force-sync annotation triggers an immediate re-pull
```

Running pods are unaffected by ESO/Vault hiccups; only *new* pods mounting a not-yet-synced `Secret`
block — the critical-path caveat ADR-0009 calls out, which is why both want HA in production.

## 7. Rotation

Two mechanisms, matching ADR-0009's "rotation happens in the store, ESO propagates":

1. **Refresh cadence (Crossplane day-2 Operation).**
   [`platform/controlplane/configuration/operations/secret-rotation-cronoperation.yaml`](../../platform/controlplane/configuration/operations/secret-rotation-cronoperation.yaml)
   — `CronOperation adhar-weekly-secret-rotation` (`0 3 * * 0`, `concurrencyPolicy: Forbid`) whose
   `function-python` step stamps the `force-sync: <unix-ts>` annotation on an `ExternalSecret`
   (`adhar-platform-secrets`/`adhar-system`), forcing ESO to re-pull from its backing store. Requires
   the core `--enable-operations` flag (see [ADR-0005 design §5](0005-crossplane-v2-namespaced.md)).

2. **Bootstrap-credential rotation (package).**
   [`security/credential-rotation/manifests/rotate-job.yaml`](../../platform/stack/packages/security/credential-rotation/manifests/rotate-job.yaml)
   — an idempotent Job (marker `Secret bootstrap-credentials-rotated` short-circuits re-runs) that
   rotates the day-0 `gitea_admin` and ArgoCD `admin` credentials to random values, updates
   `gitea-credential`/`argocd-secret`/`argocd-initial-admin-secret` in place, and **writes the new
   values into Vault** (`secret/adhar/bootstrap-credentials`) when the vault package is present. It is
   `enabled: "false"` locally (documented dev credentials keep working) and on in the production
   environment set, gated on SSO login being verified — see
   [Production Guide §3](../PRODUCTION.md#3-security-hardening-checklist).

The `adhar secrets` CLI group (`cmd/secrets/{rotate,create,list,audit,encrypt}.go`) is currently
**scaffolding** — `runRotate` validates `--name` and logs `TODO`; the live rotation path is the two
mechanisms above. `adhar get secrets` ([`cmd/get/secrets.go`](../../cmd/get/secrets.go)) reads
credentials for known services (e.g. by `app=gitea` label), not by the `cli-secret` label.

## 8. Enforcement & production hardening

ADR-0009's downstream-hardening requirements are tracked as a checklist in
[`docs/PRODUCTION.md` §3](../PRODUCTION.md#3-security-hardening-checklist): **etcd encryption at rest**
(so synced `Secret`s are protected — the last-hop RBAC caveat), **Vault HA + auto-unseal via cloud
KMS** (or a cloud secret manager as the single source), and enabling `credential-rotation`. On cloud
platforms the bootstrap Job's stored-keys mode must be dropped in favour of a `seal "awskms"` /
`"azurekeyvault"` / `"gcpckms"` stanza in `values.yaml` (documented in
[`vault/README.md`](../../platform/stack/packages/security/vault/README.md)).

## Testing

- **ApplicationSet parity** — [`platform/controllers/adharplatform/parity_test.go`](../../platform/controllers/adharplatform/parity_test.go)
  asserts every package (incl. `external-secrets`, `vault`) is wired consistently across the appset and
  the on-disk `packages/` tree ([ADR-0004](../adr/0004-applicationset-package-model.md)).
- **e2e** (`make e2e`, `tests/e2e/bootstrap`) brings up the curated core, which requires the ESO
  webhook + `vault` store `Ready` and the SSO `ExternalSecret`s synced before Keycloak-dependent apps
  go `Healthy` — an implicit end-to-end assertion of the whole chain.
- **Manifest generation** — `generate-manifests.sh` in both packages re-renders `install.yaml`
  deterministically; regenerate when bumping `CHART_VERSION`.
- **Tests to add** — a lint check that every `ExternalSecret` `secretStoreRef.name` resolves to a
  defined `ClusterSecretStore`, and that no plaintext secret material appears in `platform/stack`
  (the "no secrets in Git" invariant is asserted only informally today — see Drift).

## Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/security/external-secrets/manifests/install.yaml` | ESO controllers + CRDs (chart 2.5.0), ns `adhar-system` |
| `platform/stack/packages/security/external-secrets/values.yaml` | `webhook.failurePolicy: Ignore` (fail-open) |
| `platform/stack/packages/security/vault/manifests/install.yaml` | Vault standalone server (chart 0.32.0, image 1.21.2) |
| `platform/stack/packages/security/vault/values.yaml` | standalone + file storage + readiness `sealedcode=204` |
| `platform/stack/packages/security/vault/manifests/bootstrap.yaml` | idempotent init/unseal/configure Job (KV v2, k8s auth, `external-secrets` role, OIDC) |
| `platform/stack/packages/security/vault/manifests/vault-clustersecretstore.yaml` | `vault` `ClusterSecretStore` (wave 3) |
| `platform/stack/packages/security/vault/manifests/oidc.yaml` | `vault-oidc` + `vault-ca` `ExternalSecret`s (Keycloak client secret + platform CA) |
| `platform/stack/packages/security/keycloak/manifests/secret-gen.yaml` | `Password` generator, `keycloak-config` ES, `eso-store` SA/RBAC, `keycloak`+`gitea` stores |
| `platform/stack/packages/security/keycloak/manifests/{argocd,gitea,grafana}-*-external-secret.yaml` | reflect Keycloak client secrets into chart-owned Secrets (`creationPolicy: Merge`) |
| `platform/stack/packages/core/adhar-console/manifests/argocd-secrets.yaml` | `argocd` `ClusterSecretStore` (SA `eso-store-argocd`) + ESes |
| `platform/stack/packages/security/credential-rotation/manifests/rotate-job.yaml` | rotate bootstrap creds → Vault `secret/adhar/bootstrap-credentials` |
| `platform/controlplane/configuration/operations/secret-rotation-cronoperation.yaml` | weekly `force-sync` refresh CronOperation |
| `platform/stack/adhar-appset-local.yaml` | `external-secrets`/`vault` enabled in curated core; `credential-rotation` disabled |
| `cmd/secrets/`, `cmd/get/secrets.go` | secrets CLI (rotate/create/… are stubs); `get secrets` reads by service |
| `docs/PRODUCTION.md` §3 | etcd-encryption / Vault-HA / rotation hardening checklist |

## Drift & notes (as-built vs. ADR)

- **Most curated-core secrets flow through the `kubernetes` provider, not Vault.** Vault is the
  *shipped, enabled* source of truth and the documented target for app secrets, but the SSO plumbing
  that dominates the current package set reflects Keycloak-issued client secrets and the self-signed
  CA via the `keycloak`/`gitea`/`argocd` `kubernetes`-provider stores. ADR-0009 frames Vault as *the*
  default source; in practice it is one of two provider families, and the majority of live
  `ExternalSecret`s today do not read from Vault. The manifests would move to the `vault` store with
  only a `secretStoreRef` change — the ADR's store-agnostic claim holds — but "Vault is the source of
  truth" is aspirational for the SSO secrets as built.
- **`no-secrets-in-Git` is not machine-enforced.** ADR-0009 lists Gitea push-scanning / CI secret
  scanning as enforcement; no such gate is wired. The `trivy` package can scan for secrets but is
  `enabled: "false"` locally, and there is no gitleaks/trufflehog CI job. The invariant is upheld by
  convention (pointers-only manifests), not policy.
- **Namespace naming in docs is stale.** `vault/README.md` cites the Vault address as
  `http://vault.vault.svc.cluster.local:8200` and the init-keys `Secret` / ES controller SA as living
  in a `vault` namespace; the as-built manifests use `adhar-system` throughout (store server
  `vault.adhar-system.svc`, role bound to `bound_service_account_namespaces=adhar-system`). Same
  single-namespace consolidation noted for Crossplane in [ADR-0011].
- **Weekly rotation targets a placeholder.** `secret-rotation-cronoperation.yaml` refreshes
  `adhar-platform-secrets`, which is a documented example name, not an `ExternalSecret` that ships in
  the tree — it demonstrates the cadence and must be pointed at a real platform ES to be useful.
