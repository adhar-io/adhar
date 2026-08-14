# Low-Level Design — CI on the platform: Jenkins X pipeline model on Tekton, promotion via GitOps

Detailed design for [ADR-0018](../adr/0018-jenkins-x-ci-model.md). This documents the as-built CI
substrate (Tekton engine, kpack builds, the platform pipeline catalog, golden-path scaffolds) and the
🔜 adoption layer (Lighthouse forge events + Kargo promotion) that ADR-0018 targets for Roadmap Phase 3.
The boundary the ADR defends — **jx for CI, ArgoCD/Kargo for CD** — is a load-bearing invariant here.

## 0. Context recap

The platform already ships delivery *infrastructure* (Tekton, Cloud Native Buildpacks, Kargo, Argo
Workflows); infrastructure is not a developer experience. ADR-0018 adopts the **Jenkins X pipeline
model on top of Tekton** as the paved road: apps carry a minimal `jenkins-x.yml` + `.lighthouse/triggers.yaml`,
the heavy lifting lives in a platform-owned **pipeline catalog** teams inherit, **Lighthouse** owns
forge webhooks/ChatOps/merge across Gitea|GitHub|GitLab, builds go through **kpack** to Harbor and are
signed per [ADR-0019](../adr/0019-secure-supply-chain-chainguard.md), and **CI ends at a Git commit** —
a version-bump PR against the `environments` repo, after which **Kargo** promotes and **ArgoCD** syncs
([ADR-0004](../adr/0004-applicationset-package-model.md)). Jenkins X's own CD machinery and preview
mechanism are explicitly *not* adopted (previews come from [ADR-0017](../adr/0017-preview-environments.md)).

**Shipped vs. 🔜.** Tekton (`enabled: "true"`), kpack, and Kargo packages exist today; the Lighthouse
(`jenkins-x`) package and Kargo are wired but `enabled: "false"` in `adhar-appset-local.yaml` — the jx
layer is the Phase-3 adoption target, Kargo is Phase 2/2.5. The golden-path scaffolds already emit the
jx/Lighthouse pipeline-as-code so consumers are ready the day the package flips on.

## 1. Package map (`platform/stack/packages/application/`)

| Package | Ships | Namespace | appset `enabled` | Role |
|---|---|---|---|---|
| `tekton/` | Pipelines `v0.65.0` + Triggers `v0.30.0` + Dashboard `v0.52.0` (vendored release YAML) | `adhar-system` | `true` | execution engine |
| `buildpack/` | kpack `v0.17.1` (vendored release YAML) | `kpack` (own) | `false` | CNB image builds |
| `jenkins-x/` | Lighthouse chart `1.33.1` + pipeline catalog + secrets/HTTPRoute/webhook-Job | `adhar-system` | `false` 🔜 | forge events, ChatOps, catalog |
| `kargo/` | Kargo chart + Project/Warehouse/Stages | `adhar-environments` (own) | `false` 🔜 | GitOps promotion |
| `adhar-templates/` | Backstage scaffolder golden paths | — | (console) | seeds jx/Lighthouse-ready repos |

All CNCF packages follow the platform conventions: single shared namespace `adhar-system` unless a
component hardcodes cluster-scoped/namespace-owning objects (kpack → `kpack`, Kargo Project → its
own `adhar-environments`, both sanctioned [ADR-0011](../adr/0011-shared-platform-namespace.md)
exceptions), Gateway-API-only exposure (no nginx Ingress), and secrets via ESO not Git
([ADR-0009](../adr/0009-secrets-eso-vault.md)).

## 2. The Tekton engine (`application/tekton`) — shipped

`generate-manifests.sh` concatenates four upstream release manifests (pipeline, triggers, interceptors,
dashboard) into `manifests/install.yaml`, then rewrites the vendored namespaces into the shared one:

```bash
sed -i.bak 's/tekton-pipelines-resolvers/adhar-system/g; s/tekton-pipelines/adhar-system/g' ${INSTALL_YAML}
```

The Dashboard has no native auth, so `manifests/dashboard-sso.yaml` fronts it with an
`quay.io/oauth2-proxy/oauth2-proxy:v7.7.1` Deployment bound to the Keycloak `tekton` client
(`--oidc-issuer-url=https://keycloak.adhar.localtest.me:8443/realms/adhar`,
`--upstream=http://tekton-dashboard.adhar-system.svc.cluster.local:9097`). The client secret and a
generated cookie secret are projected by an `ExternalSecret` with
`refreshPolicy: CreatedOnce` + `refreshInterval: 5m` — the standing [ADR-0013](../adr/0013-sso-bootstrap-config-job.md)
pattern that retries until the Keycloak realm Job has run instead of freezing on the first cold-boot miss.
`manifests/httproute.yaml` routes `tekton.adhar.localtest.me` → `tekton-oauth2-proxy:4180` (never the
dashboard directly).

**Standing conflict.** The Tekton package is mutually exclusive with `open-function`, which vendors
Knative+Tekton objects whose names are fixed upstream (`ConfigMap/config-logging`,
`config-observability`, `config-defaults`, `config-tracing`, `Secret/webhook-certs`) — see
`packages/CONFLICTS.md`. `open-function` therefore installs into its own `openfunction` namespace and
stays `enabled: "false"`; the two must not both target `adhar-system`.

## 3. The pipeline catalog (`application/jenkins-x/manifests/pipeline-catalog.yaml`) — shipped

The golden-path property lives here: three reusable `tekton.dev/v1` **Tasks** and two **Pipelines**,
all labeled `adhar.io/component: pipeline-catalog`, in `adhar-system`. App repos reference them by name
from `.lighthouse/triggers.yaml`; fixing a step here fixes every consumer on its next run.

| Object | Kind | What it does |
|---|---|---|
| `adhar-git-clone` | Task | `alpine/git:2.47.2` — `git init` + shallow fetch of `$(params.revision)` into the `source` workspace |
| `adhar-test` | Task | runs `ci/test.sh` if present, else `make test` if the Makefile defines it, else a no-op notice (`ubuntu:24.04` default toolchain, overridable) |
| `adhar-promote-pr` | Task | **CI's terminal act**: via the Gitea API, branch `promote-<app>-<version>` off `main`, `sed`-rewrite the image tag in `$(params.targetFile)`, PUT the file, open a PR against `adhar/environments` |
| `adhar-pr-verify` | Pipeline | presubmit: `clone → test` |
| `adhar-release` | Pipeline | postsubmit on `main`: `clone → test → promote` |

`adhar-promote-pr` reads its Git token from `secretKeyRef {name: lighthouse-oauth-token, key: oauth}`
and is deliberately idempotent — the branch-create and PR-open calls tolerate `409` (already exists).
Its terminal act is a **PR, not a merge**: CD stays declarative and out of CI's hands.

**🔜 gap (as-built vs. ADR).** ADR-0018 says builds produce buildpacks images, signed/attested, pushed
to Harbor, and that CI does "automatic semantic version tagging and release notes." The shipped
`adhar-release` pipeline has **only** `clone → test → promote` — no build/scan/sign step, and `VERSION`
defaults to `$(params.PULL_BASE_SHA)` (no semver tagging, no release notes). The catalog header records
the reason: pointing kpack at Harbor needs the platform-CA trust wiring, which is the documented
follow-up before the build step is added. Today the enforcement point (ADR-0019 supply-chain contract)
is *scaffolded, not yet enforced* in the catalog.

## 4. Lighthouse — the forge/ChatOps/trigger layer (`application/jenkins-x`) — 🔜 (`enabled: "false"`)

`generate-manifests.sh` renders the `jenkins-x/lighthouse` chart `1.33.1` into `manifests/install.yaml`
(28 documents): ServiceAccounts + Deployments for **foghorn**, **keeper** (tide/merge), **webhooks**
(the `hook` receiver Service), **tekton-controller**, and **gc-jobs**. `values.yaml` wires it to the
platform:

- **Engine**: `engines.tekton: true`, `jx: false`, `jenkins: false` — Lighthouse launches Tekton
  PipelineRuns, the legacy jx/jenkins engines stay off.
- **Forge**: `git.kind: gitea`, `git.server: http://gitea-http.adhar-system.svc.cluster.local:3000`,
  `user: gitea_admin` — in-cluster Gitea is the default forge ([ADR-0003](../adr/0003-in-cluster-gitea.md)),
  but the same config is forge-portable to GitHub/GitLab by construction.
- **Prow-style config**: `in_repo_config.enabled["*"]: true` (every repo may carry `.lighthouse/`),
  `pod_namespace`/`prowjob_namespace: adhar-system`, and a **Keeper/tide** block that auto-merges PRs
  in org `adhar` carrying the `approved` label and lacking `do-not-merge*`.
- **ChatOps plugins**: `approve, assign, help, hold, label, lgtm, lifecycle, size, trigger, wip` for
  the `adhar` org, with `lgtm_acts_as_approve: true`. Merge automation comes from Lighthouse, not
  per-forge bots — forge choice stays reversible (pillar 8).

### 4.1 Webhook path & secrets

```
Gitea push/PR/comment ──HTTPRoute lighthouse.adhar.localtest.me──▶ Service hook:80 ──▶ lighthouse-webhooks:8080
        (HMAC-signed with lighthouse-hmac-token)                                             │
                                                                            trigger plugin ──▶ Tekton PipelineRun
```

`manifests/httproute.yaml` routes `lighthouse.adhar.localtest.me` → `hook:80`. `manifests/secrets.yaml`
supplies two secrets ([ADR-0009](../adr/0009-secrets-eso-vault.md): Git carries pointers, never values):

- `lighthouse-oauth-token` — an `ExternalSecret` projecting `gitea-credential.token` from the `gitea`
  `ClusterSecretStore` (`sync-wave: -5`).
- `lighthouse-hmac-token` — the webhook HMAC, generated **once in-cluster** by the `lighthouse-hmac-gen`
  Job (`openssl rand -hex 32`, `sync-wave: -5`, get-then-create so it never rotates), gated by a
  dedicated `lighthouse-secret-gen` SA/Role (`get,create` on secrets, `sync-wave: -10`).

`pipeline-catalog.yaml` also carries a **PostSync** `lighthouse-webhook-register` Job (`alpine/k8s:1.31.0`,
`backoffLimit: 3`) that registers a Gitea **system** webhook (admin API) pointing at
`http://hook.adhar-system.svc.cluster.local/hook`, signed with the HMAC, for events
`push, pull_request, pull_request_comment, issue_comment, create, delete`. It is idempotent — it checks
`/admin/hooks` for the URL and exits early if present. System-level registration is the IDP promise:
**every** adhar-org repo gets CI with zero per-repo setup.

> **Known ADR-0011 risk** (`CONFLICTS.md`): the chart hardcodes `Service/hook`, which injects a
> `HOOK_PORT` env var into every service-linked pod in `adhar-system`. Pods that parse `*_PORT`-shaped
> env vars must set `enableServiceLinks: false` (the standing namespace rule; the hmac-gen Job already does).

## 5. kpack builds (`application/buildpack`) — shipped, not yet wired to the pipeline

kpack `v0.17.1` installs from the upstream release manifest into its own `kpack` namespace; it exposes
no UI (no HTTPRoute — `values.yaml` is a placeholder for layout parity). Builds are driven by kpack
CRDs (`Image`, `Builder`). Per the catalog, the intended release-pipeline build step emits a kpack
`Image` on a Chainguard/Wolfi run image (no Dockerfile, SBOM at build) pushing to Harbor — **pending the
Harbor CA trust follow-up** (§3), so it is not referenced by `adhar-release` yet.

## 6. Golden-path scaffolds (`application/adhar-templates`) — shipped

The scaffolder templates are `scaffolder.backstage.io/v1beta3` `Template`s surfaced in the console. The
`microservice` (Go) and `frontend` templates run `fetch:template` → `publish:gitea` →
`adhar:create-argocd-app` → `catalog:register`, seeding a Gitea repo whose skeleton already carries the
jx pipeline-as-code:

- `skeleton/jenkins-x.yml` — a deliberately minimal stub (`buildPack: go` / `none`,
  `pipelineConfig.pipelines.overrides: []`); the comment makes the inheritance model explicit — the real
  build→test→scan→sign→package→promote-PR lifts from the platform catalog.
- `skeleton/.lighthouse/triggers.yaml` — `presubmits: [verify → pipelineRef adhar-pr-verify]`,
  `postsubmits: [release (branches: main) → pipelineRef adhar-release, params APP_NAME=${{values.name}}]`.
  No per-repo pipeline authoring; both reference the catalog Pipelines by name.

A scaffolded service is therefore CI-ready the moment `jenkins-x` is enabled: the system webhook is
already registered, and its `.lighthouse/triggers.yaml` resolves to catalog Pipelines.

## 7. Promotion — Kargo (`application/kargo`) — 🔜 (`enabled: "false"`, Roadmap P2.5)

CI ends at the promote-PR (§3); Kargo owns dev → staging → prod. `manifests/promotion-pipeline.yaml`
creates a `kargo.akuity.io/v1alpha1` `Project` in its own `adhar-environments` namespace (Kargo requires
a Project to own a same-named namespace — a sanctioned ADR-0011 exception, like open-function), with:

- a `Warehouse` subscribed to the `environments` repo `main` branch, `includePaths: [development/**]` —
  every commit under `development/` mints Freight;
- a `staging` `Stage` (`autoPromotionEnabled: true`) whose `promotionTemplate` runs
  `git-clone → copy development/config.yaml → staging/config.yaml → git-commit → git-push`;
- a `production` `Stage` (`autoPromotionEnabled: false`, promotes from `staging`, same copy pattern,
  manual approval in the Kargo UI/CLI).

Git credentials are a `kargo-gitea-creds` `ExternalSecret` (labeled `kargo.akuity.io/cred-type: git`)
projected from `gitea-credential`. Crucially, **every promotion is still a Git commit ArgoCD then syncs**
— Git stays the only write path (ADR-0004). Kargo is exposed Gateway-API-only (`api.ingress.enabled: false`,
`manifests/httproute.yaml`), admin creds pre-created in `manifests/ingress.yaml`.

## 8. End-to-end flow (once jx + Kargo are enabled)

```
dev pushes to app repo (Gitea)
  → system webhook → Service/hook → lighthouse-webhooks (HMAC-verified)
  → in_repo_config reads .lighthouse/triggers.yaml
     presubmit  → PipelineRun adhar-pr-verify   (clone → test), status posted to the PR
     ChatOps    → /lgtm,/approve → Keeper merges when `approved` && no do-not-merge
  → merge to main → postsubmit → PipelineRun adhar-release (clone → test → [kpack build 🔜] → adhar-promote-pr)
  → adhar-promote-pr opens version-bump PR against adhar/environments   ← CI ENDS HERE
  → (merge under development/**) Kargo Warehouse mints Freight
     → staging Stage auto-promotes (copy+commit+push) → ArgoCD syncs staging
     → production Stage promotes on manual approval    → ArgoCD syncs prod
```

**Two-engine boundary.** Lighthouse/Tekton never deploy; ArgoCD/Kargo never build. If a reviewer sees
CI reaching past a Git commit (kubectl-apply, direct deploy) or ArgoCD/Kargo invoking builds, that is
the ADR-0018 boundary breaking and must be rejected — otherwise the two GitOps engines fight.

## 9. Wiring & ordering (`platform/stack/adhar-appset-local.yaml`)

The ApplicationSet lists all packages with an `enabled` selector-gate
([ADR-0004](../adr/0004-applicationset-package-model.md)): `tekton` (`true`), `jenkins-x`, `buildpack`,
`kargo`, `open-function` all `false` on the local profile. Ordering is expressed with ArgoCD
sync-waves/hooks inside each package rather than cross-package DAG: Lighthouse secrets at waves
`-10/-5` land before the webhook-register PostSync Job; Kargo Project/Warehouse/Stages at waves `10–14`
land after the namespace. On the local single-node profile, CI runs are batched and previews capped
([ADR-0012](../adr/0012-single-node-resilience-tuning.md)).

## Testing

- **Manifest parity / lint** — `platform/controllers/adharplatform/parity_test.go` enforces that every
  `platform/stack/packages/**` dir is represented in the appset and that manifests render; the jenkins-x,
  tekton, buildpack, kargo packages are covered as package entries.
- **Regeneration** — `generate-manifests.sh` in `tekton/`, `jenkins-x/`, `buildpack/`, `kargo/` are the
  source of truth for the vendored `manifests/install.yaml`; re-run on version bumps (pinned:
  Tekton `v0.65.0`/`v0.30.0`/`v0.52.0`, Lighthouse `1.33.1`, kpack `v0.17.1`).
- **Tests to add (🔜)** — an e2e that flips `jenkins-x` + `buildpack` + `kargo` to `enabled: "true"`,
  scaffolds a `microservice`, pushes a PR, and asserts: (1) `adhar-pr-verify` PipelineRun succeeds and
  posts a `verify` status; (2) merge triggers `adhar-release` and opens a promotion PR against
  `adhar/environments`; (3) once the kpack build step lands, the image is in Harbor, signed, with an SBOM
  (ADR-0019); (4) Kargo auto-promotes staging and gates production. A catalog lint asserting every
  `skeleton/.lighthouse/triggers.yaml` `pipelineRef` resolves to a Pipeline in `pipeline-catalog.yaml`.

## Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/application/tekton/generate-manifests.sh` | vendors Tekton pipeline/triggers/dashboard, rewrites ns → `adhar-system` |
| `platform/stack/packages/application/tekton/manifests/{install,dashboard-sso,httproute}.yaml` | engine install, Keycloak oauth2-proxy gate, `tekton.<host>` route |
| `platform/stack/packages/application/jenkins-x/values.yaml` | Lighthouse config: Gitea forge, Tekton engine, Keeper/tide, ChatOps plugins |
| `platform/stack/packages/application/jenkins-x/manifests/install.yaml` | rendered Lighthouse chart `1.33.1` (foghorn/keeper/webhooks/tekton-controller/gc-jobs) |
| `platform/stack/packages/application/jenkins-x/manifests/pipeline-catalog.yaml` | 3 catalog Tasks + `adhar-pr-verify`/`adhar-release` Pipelines + PostSync webhook-register Job |
| `platform/stack/packages/application/jenkins-x/manifests/secrets.yaml` | `lighthouse-oauth-token` (ESO) + `lighthouse-hmac-token` (in-cluster gen Job) |
| `platform/stack/packages/application/jenkins-x/manifests/httproute.yaml` | `lighthouse.<host>` → `hook:80` |
| `platform/stack/packages/application/buildpack/{generate-manifests.sh,values.yaml,manifests/install.yaml}` | kpack `v0.17.1` in `kpack` namespace (CNB builds) |
| `platform/stack/packages/application/kargo/manifests/promotion-pipeline.yaml` | Project/Warehouse/staging+production Stages in `adhar-environments`, ESO git creds |
| `platform/stack/packages/application/adhar-templates/{microservice,frontend}/skeleton/jenkins-x.yml` | golden-path pipeline-as-code stub (inherits catalog) |
| `platform/stack/packages/application/adhar-templates/{microservice,frontend}/skeleton/.lighthouse/triggers.yaml` | presubmit/postsubmit → catalog Pipelines |
| `platform/stack/packages/application/adhar-templates/microservice/template.yaml` | Backstage scaffolder golden path (publish:gitea + adhar:create-argocd-app) |
| `platform/stack/adhar-appset-local.yaml` | package enablement gate (`tekton: true`; jenkins-x/buildpack/kargo/open-function: false) |
| `platform/stack/packages/CONFLICTS.md` | tekton↔open-function/cosign collisions; `Service/hook` `HOOK_PORT` risk |

## Milestones

1. **M1 — engine + catalog (done).** Tekton + Dashboard-SSO shipped; pipeline catalog authored;
   golden-path scaffolds emit jx/Lighthouse pipeline-as-code.
2. **M2 — Lighthouse on (Phase 3).** Flip `jenkins-x` to `enabled: "true"`; validate webhook register,
   presubmit/postsubmit, ChatOps, Keeper merge on the `adhar` org.
3. **M3 — build step.** Wire Harbor CA trust for kpack, add the `kpack build → sign/attest` step to
   `adhar-release`, enforce the ADR-0019 supply-chain contract in the catalog. Add semver tagging +
   release notes to close the ADR-0018 versioning promise.
4. **M4 — Kargo promotion (Phase 2.5).** Flip `kargo` on; join `adhar-promote-pr`'s PR target to the
   Warehouse's `development/**` watch path so CI's promote-PR feeds Kargo's dev→staging→prod chain.
5. **M5 — forge portability.** Prove the same `values.yaml`/catalog against GitHub/GitLab forges.

## Risks

- **Two GitOps engines fighting** — the "jx for CI, ArgoCD/Kargo for CD" boundary must be defended in
  review; any CI step that deploys, or any CD step that builds, is a defect (§8).
- **Supply-chain gap until M3** — the catalog is the ADR-0019 enforcement point, but `adhar-release`
  ships without build/sign/scan; teams could believe artifacts are signed when they are not. Track M3
  as a security-relevant blocker, not a nicety.
- **Promote-PR ↔ Kargo seam** — `adhar-promote-pr` opens a PR against `adhar/environments` `main` on an
  arbitrary `targetFile`; Kargo's Warehouse only watches `development/**`. Until M4 aligns the target
  path, the CI PR and the Kargo pipeline are not fully joined.
- **Namespace collisions** — `Service/hook`'s `HOOK_PORT` and the tekton↔open-function/cosign name
  clashes (`CONFLICTS.md`) constrain what can co-tenant `adhar-system`; the `enableServiceLinks: false`
  rule and open-function's separate namespace are mandatory, not optional.
- **Component budget** — Lighthouse adds five Deployments + Tekton controllers to the platform namespace
  with full ADR-0011/0012 obligations (probe tuning, resource budget); on the local profile CI is batched.
