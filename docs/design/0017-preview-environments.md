# Low-Level Design — Ephemeral preview environments per pull request

Detailed design for [ADR-0017](../adr/0017-preview-environments.md). Preview environments are built entirely from machinery the platform already runs — the **ArgoCD ApplicationSet Pull Request generator** (ADR-0004), the in-cluster **Gitea** forge (ADR-0003), the shared **Cilium Gateway** (ADR-0002), and the **Crossplane** guardrail/day-2 stack (ADR-0005) — with **no preview-specific controller**. This document maps the shipped namespace-scoped path (`examples/preview-environments-appset.yaml`) to the concrete Gitea/ArgoCD/Gateway wiring, then specifies the still-🔜 pieces (per-PR routing injection, vcluster-backed previews, the TTL reaper) as forward-looking design.

## 0. Context recap

"Does this change work?" should be answerable at a URL, not by a reviewer checking out the branch. ADR-0017 makes **the PR the lifecycle**: an open PR (opt-in via a `preview` label) materialises one ArgoCD `Application`; every push re-syncs it at the PR head; closing the PR prunes it. CI never creates or deletes environments — it only builds the image (ADR-0018) and posts the URL back. The ADR is **partially built**: namespace-scoped previews ship today as a copy-per-repo ApplicationSet; vcluster-backed previews (ADR-0016), automatic per-PR `HTTPRoute` injection, and the TTL reaper (CronOperation, ADR-0005) are designed here but not yet wired.

## 1. Invariants

- **INV-1 — Forge is the source of truth.** Preview state is a pure function of `{open PRs in Gitea} × Git content at each PR head`. A dead CI job cannot leak an environment because CI never owns the lifecycle (ADR-0015 pillar 2: Git-only write path).
- **INV-2 — No new controller.** Every moving part is an existing installed component; a preview is "just another ArgoCD Application" reconciled by the ApplicationSet controller.
- **INV-3 — Opt-in twice.** Previews are opt-in *per repo* (you copy the ApplicationSet) and *per PR* (the `preview` label gates generation).
- **INV-4 — Guardrails are not preview-specific.** A preview namespace is governed by the same tenant guardrails (ResourceQuota/LimitRange/NetworkPolicy/PodSecurity) and Kyverno pack as any other tenant namespace.
- **INV-5 — Capacity is real.** N open previews × quota must fit the target cluster; on the local profile (ADR-0012) previews contend with the platform itself, so local previews default to 1–2 concurrent.

## 2. The shipped artifact — PR-generator ApplicationSet (`examples/preview-environments-appset.yaml`)

One ApplicationSet per opted-in application repo, committed to the `environments` repo or applied directly into `adhar-system`. The generator polls Gitea for open PRs; the template materialises one Application per PR:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: preview-<app>
  namespace: adhar-system
spec:
  goTemplate: true
  goTemplateOptions: [ missingkey=error ]
  generators:
    - pullRequest:
        requeueAfterSeconds: 60                 # poll bound on "push → preview updated"
        gitea:
          owner: gitea_admin
          repo: <app>
          api: http://gitea-http.adhar-system.svc.cluster.local:3000
          tokenRef: { secretName: gitea-credential, key: token }
        labels: [ preview ]                     # opt-in per PR
  template:
    metadata:
      name: "preview-<app>-pr-{{ .number }}"
      labels:
        adhar.io/preview: "true"
        adhar.io/preview-app: "<app>"
      finalizers: [ resources-finalizer.argoproj.io ]   # PR close → prune resources
    spec:
      project: default
      source:
        repoURL: http://gitea-http.adhar-system.svc.cluster.local:3000/gitea_admin/<app>
        targetRevision: "{{ .head_sha }}"       # deploy the PR head; every push updates
        path: manifests
      destination:
        server: https://kubernetes.default.svc
        namespace: "preview-<app>-pr-{{ .number }}"
      syncPolicy:
        automated: { prune: true, selfHeal: true }
        syncOptions: [ CreateNamespace=true, ServerSideApply=true ]
```

Generator template variables (`{{ .number }}`, `{{ .head_sha }}`, `.branch`, `.title`, `.labels`) come from ArgoCD's Gitea PR generator. `goTemplateOptions: [missingkey=error]` fails loudly on a typo rather than emitting an empty name.

### 2.1 Why each field is what it is

| Field | Reason |
|---|---|
| `api: http://gitea-http.adhar-system.svc.cluster.local:3000` | the in-cluster Gitea HTTP Service (ADR-0003) — no external forge dependency for local/self-hosted repos |
| `tokenRef.secretName: gitea-credential`, `key: token` | the platform's Gitea admin secret (`utils.GiteaAdminSecret = "gitea-credential"`, `adhar-system`) already carries an API token under `token`, populated idempotently by the `gitea-token-gen` Job in `resources/gitea/post-install.yaml` |
| `labels: [preview]` | opt-in per PR (INV-3); drop it to preview every open PR |
| `targetRevision: "{{ .head_sha }}"` | pins the deploy to the PR's head commit so every push re-renders the Application (ArgoCD picks up the new image tag CI pushed, ADR-0018) |
| `finalizers: [resources-finalizer.argoproj.io]` + `automated.prune` | closing the PR removes the generator element → Application is deleted → finalizer cascades prune of every synced resource; the namespace goes with it |
| `syncOptions: [CreateNamespace=true, ServerSideApply=true]` | ArgoCD creates the ephemeral namespace and applies with SSA (same field-ownership model as the rest of the platform) |

### 2.2 Lifecycle as a state machine

```
PR opened + `preview` label     ──▶  generator yields element {number, head_sha}
        │                                     │
        │                                     ▼
        │                       Application preview-<app>-pr-<n>  (auto-sync)
        │                                     │  CreateNamespace=true
   push to PR ──▶ new head_sha ──▶  targetRevision changes ──▶ re-sync (selfHeal)
        │
   PR closed / merged  ──▶  element disappears  ──▶  Application deleted
                                     │
                                     ▼
                    resources-finalizer prunes ns + all resources
```

No `adhar` CLI verb and no CI step touches this transition — the `requeueAfterSeconds: 60` poll (or a Gitea webhook, §6) is the only clock.

## 3. Isolation — tiered by blast radius

The ADR splits previews by what the PR changes:

- **Application PRs (common case) → namespace-scoped preview.** The `Application` targets a fresh namespace `preview-<app>-pr-<n>` on the management cluster. Standard tenant guardrails apply through the **`CompositeEnvironment`** control-plane API (ADR-0005): `configuration/xrd/env.xrd.yaml` + composition `configuration/compositions/env/kubernetes-namespace.yaml` emit the `Namespace`, a `ResourceQuota`, a `LimitRange`, and (opt-in) a `NetworkPolicy` from one KCL program. Baseline PodSecurity and image/supply-chain admission come from the **Kyverno pack** (`platform/stack/packages/security/{kyverno,kyverno-policies}`) that already guards every namespace. A preview therefore inherits the identical guardrails as a real tenant namespace — nothing preview-specific to maintain (INV-4).

  Two ways to attach the guardrails: (a) the app's `manifests/` include a `CompositeEnvironment` for the preview namespace, or (b) the copied ApplicationSet is extended to render one. Today the example leaves quota to the app manifests; wiring an automatic `CompositeEnvironment` per preview is a §7 milestone.

- **Cluster-scoped PRs (operators, CRDs, webhooks) → vcluster-backed preview (🔜).** A namespace cannot contain a CRD or a validating webhook, so these previews target a disposable **vcluster** (ADR-0016, `platform/stack/packages/core/vcluster/`) instead. Design: the PR carries a second label (e.g. `preview-vcluster`); a matching ApplicationSet first syncs a vcluster into `preview-<app>-pr-<n>`, registers it as an ArgoCD cluster, and points the app Application's `destination.server` at that vcluster. This reuses the ADR-0016 vcluster package end-to-end; only the appset matrix that provisions-then-targets is new.

## 4. Routing — convention over the shared Gateway

Each preview is reachable at `https://pr-<n>.<app>.<domain>` on the shared **`adhar-gateway`** (ADR-0002, `namespace: adhar-system`). The Gateway's listeners set `allowedRoutes.namespaces.from: All` (`resources/gateway/gateway.yaml`), so an `HTTPRoute` in a `preview-*` namespace attaches with no per-namespace grant. TLS terminates at the Gateway with the platform certificate — previews are HTTPS from the first push. On cloud the HTTPS listener carries the wildcard hostname `*.{{ .Host }}` (`resources/gateway/gateway-cloud.yaml`) so `pr-<n>.<app>.<domain>` matches; locally the listeners set no hostname (serve all) and CoreDNS resolves `*.adhar.localtest.me`.

The per-preview `HTTPRoute` looks like the platform's own routes:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: preview
  namespace: preview-<app>-pr-<n>
spec:
  parentRefs:
    - name: adhar-gateway
      namespace: adhar-system
  hostnames: [ "pr-<n>.<app>.<domain>" ]
  rules:
    - matches: [ { path: { type: PathPrefix, value: / } } ]
      backendRefs: [ { name: <app>, port: 80 } ]
```

**Gap (🔜):** the shipped ApplicationSet syncs the app repo's static `manifests/` verbatim, so the `HTTPRoute` hostname is *not* templated with `{{ .number }}`. Getting a stable per-PR URL today requires either the app manifests to accept the PR number (via a Kustomize/Helm parameter the appset passes) or an appset-injected `HTTPRoute`. §7-M2 adds hostname injection to the template. The URL is posted back to the PR by CI (ADR-0018) once resolved.

## 5. Data — disposable and synthetic

Previews get ephemeral dependencies, never production data (ADR-0017): a per-preview PostgreSQL from the **CNPG** package (`platform/stack/packages/data/cnpg/`) seeded with fixtures, torn down when the namespace is pruned. Secrets flow through **ESO** like everything else (ADR-0009) — a `preview` `ExternalSecret` referencing dev-grade secret stores; committing "harmless" preview credentials to Git is exactly the anti-pattern the ADR calls out (INV-1). Previews that need shared services consume the platform's dev-grade instances, not prod.

> Drift: there is **no Kubernetes-native `CompositeDatabase` composition** yet — `configuration/compositions/database/` ships `aws-rds-postgresql`, `azure-sql`, `gcp-cloudsql` only. So "a CNPG database from a template" locally means an app-authored CNPG `Cluster` manifest (from the cnpg package), not a `CompositeDatabase` XR. Adding a `database/kubernetes-cnpg.yaml` composition would let previews request `CompositeDatabase` uniformly (see ADR-0005 design).

## 6. Push→preview latency and webhooks

The generator has two clocks:

- **Poll** — `requeueAfterSeconds: 60` bounds "push → preview updated" at ~1 min (the example's value).
- **Webhook** — configuring a Gitea webhook to ArgoCD's `/api/webhook` collapses the delay to near-real-time; this is per-forge setup documented in `docs/USER_GUIDE.md §4`. Gitea/GitHub/GitLab are all supported PR-generator providers (ADR-0003), so the same pattern works against an external forge by swapping the `pullRequest` generator block.

## 7. Milestones

- **M1 — namespace previews (shipped).** `examples/preview-environments-appset.yaml`, PR generator against Gitea, `gitea-credential` token, `preview` label gating, prune-on-close. Documented in `USER_GUIDE §4`.
- **M2 — first-class routing + guardrails.** Extend the template to inject a per-PR `HTTPRoute` (`pr-<n>.<app>.<domain>`) and a `CompositeEnvironment` (quota/limits/netpol) so a copied appset yields a governed, HTTPS-routable preview with zero app-manifest changes. CI posts the resolved URL to the PR.
- **M3 — TTL reaper.** A `CronOperation` (ADR-0005, modelled on `configuration/operations/backup-cronoperation.yaml`) that lists namespaces labeled `adhar.io/preview: "true"`, and deletes those whose backing PR is closed or whose last sync is older than N days — a backstop for missed forge webhooks (structural cost control, ADR-0017). Requires the `--enable-operations` core flag already set in `hack/crossplane/values.yaml`.
- **M4 — vcluster-backed previews.** Second label + appset matrix that provisions a vcluster (ADR-0016), registers it with ArgoCD, and targets the app Application at it — enabling PRs that change CRDs/operators/webhooks.
- **M5 — `enabled`-gating per environment.** Ship a preview ApplicationSet as a gated package in the stack (matchLabels `enabled` selector, like `adhar-appset-local.yaml`) so previews are off by default on prod clusters and on by policy elsewhere.

## 8. Risks & failure modes

- **Capacity exhaustion** — N previews × quota can starve the cluster; on local, contention is with the platform itself (ADR-0012). Mitigation: ResourceQuota per preview + the M3 TTL reaper + low local concurrency default (1–2).
- **Stale environments on missed webhook** — poll-only is the safety net; M3 reaper is the backstop.
- **Leaked secrets in Git** — enforce ESO-only secret flow via Kyverno; never commit preview credentials (INV-1).
- **URL not resolving without M2** — until hostname injection lands, per-PR routing needs app-manifest cooperation; the preview is still reachable via port-forward/ArgoCD in the interim.
- **`missingkey=error` template break** — a malformed generator field fails the whole ApplicationSet loudly (intended), so validate the copied file before commit.

## 9. Testing

- **Existing coverage.** The PR-generator machinery is ArgoCD's; the platform's `parity_test.go` (`platform/controllers/adharplatform/`) guards the appset/environment split it plugs into. The Gitea token path (`gitea-credential.token`) is exercised by the `gitea-token-gen` Job under the bootstrap e2e (`tests/e2e/bootstrap`).
- **Tests to add.**
  - *Lint/parity*: a check that any committed `preview-*` ApplicationSet references `gitea-credential`/`token`, uses `goTemplateOptions: [missingkey=error]`, and carries the `resources-finalizer.argoproj.io` finalizer (prune-on-close is not optional).
  - *e2e (local)*: open a PR labeled `preview` against a seeded Gitea repo, assert an `Application preview-<app>-pr-<n>` and namespace appear and become `Healthy`; push a commit, assert re-sync at the new `head_sha`; close the PR, assert the namespace is pruned.
  - *M3*: unit-test the reaper's "closed PR or stale > N days" predicate over labeled namespaces.

## 10. Code & file map

| Path | Responsibility |
|---|---|
| `examples/preview-environments-appset.yaml` | shipped copy-per-repo PR-generator ApplicationSet (namespace-scoped previews) |
| `platform/utils/gitea.go` | `GiteaAdminSecret = "gitea-credential"` — the secret the generator's `tokenRef` reads |
| `platform/controllers/adharplatform/resources/gitea/post-install.yaml` | `gitea-token-gen` Job — ensures `gitea-credential` carries the `token` key the generator needs; also the model `HTTPRoute` shape |
| `platform/controllers/adharplatform/resources/gateway/{gateway.yaml,gateway-cloud.yaml}` | `adhar-gateway` listeners (`allowedRoutes.from: All`), cloud wildcard host `*.{{ .Host }}` — where preview `HTTPRoute`s attach |
| `platform/controlplane/configuration/xrd/env.xrd.yaml` + `compositions/env/kubernetes-namespace.yaml` | `CompositeEnvironment` — Namespace + ResourceQuota + LimitRange + NetworkPolicy guardrails for a preview namespace |
| `platform/stack/packages/security/{kyverno,kyverno-policies}/` | baseline PodSecurity / supply-chain admission over preview namespaces |
| `platform/stack/packages/data/cnpg/` | ephemeral per-preview PostgreSQL |
| `platform/stack/packages/core/vcluster/` | disposable virtual cluster for cluster-scoped PRs (M4, ADR-0016) |
| `platform/controlplane/configuration/operations/backup-cronoperation.yaml` | the `CronOperation` template the M3 TTL reaper is modelled on |
| `docs/USER_GUIDE.md` §4 | user-facing preview workflow (copy the appset, label the PR) |

## 11. Drift & notes (as-built vs. ADR)

- **Namespace name.** ADR text says `preview-pr-<number>`; the shipped example uses `preview-<app>-pr-<number>` (app-qualified — necessary when multiple repos preview into one cluster). The `<app>`-qualified form is the correct one.
- **Repo owner.** The example points at `owner: gitea_admin` and `repoURL .../gitea_admin/<app>`, i.e. the admin user's namespace — not the platform org `adhar` (`globals.GiteaPlatformOrg`). Repos created via the golden path live under the `adhar` org; adjust `owner`/`repoURL` accordingly per repo.
- **Routing is convention, not yet automated.** The shipped appset does not template the `HTTPRoute` hostname with the PR number (§4 gap); stable per-PR URLs need M2.
- **CNPG-from-template.** No Kubernetes-native `CompositeDatabase` composition exists yet (§5); ephemeral DBs are app-authored CNPG `Cluster`s for now.
- **TTL reaper / vcluster previews are 🔜.** The ADR lists both as part of the decision; they are designed here (M3/M4) but not implemented — consistent with the ADR's "namespace-scoped previews first" status.
