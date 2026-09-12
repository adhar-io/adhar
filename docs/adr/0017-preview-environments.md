# ADR-0017: Ephemeral preview environments per pull request

**Status**: Accepted — **implemented** (2026-09) for namespace-scoped previews: the `application/preview-environments` package ships the org-wide pull-request ApplicationSet and the Kyverno quota/limit guardrails, the supply-chain `app-ci` pipeline builds and tags PR heads by commit SHA, and the golden-path skeletons ship the `preview/` overlay. vcluster-backed previews still follow ADR-0016 Composition wiring. A live open-PR-to-URL run is the remaining verification. · **Date**: 2026-07 (implemented 2026-09)

## Context

"Does this change work?" should be answerable by opening a URL, not by reviewers checking out branches locally. That requires an environment per open pull request: created when the PR opens, updated on every push, routable at a predictable URL, and destroyed when the PR closes. Doing this by hand (shared "staging" fought over by every PR) serializes teams and makes staging permanently dirty. Options for the mechanics:

- **CI scripts `kubectl apply` into a shared namespace** — imperative, drift-prone, violates the Git-only write path (pillar 2, ADR-0015), and leaks resources when CI jobs die mid-teardown
- **A dedicated preview operator** — another controller to run and maintain, duplicating what ArgoCD ApplicationSets already do
- **ArgoCD ApplicationSet Pull Request generator** — ArgoCD polls the forge (Gitea/GitHub/GitLab — all providers the platform already speaks, ADR-0003) for open PRs and materializes one Application per PR; closing the PR deletes the Application and prunes its resources. Declarative, no new components

## Decision

Preview environments are driven by an **ApplicationSet with the Pull Request generator**, one per application repo that opts in:

- **Lifecycle is the PR**: an open PR (optionally gated on a `preview` label so previews are opt-in per PR) generates `Application` `preview-<repo>-pr-<number>`; every push re-syncs it; closing the PR prunes everything. No CI step creates or deletes environments — CI only builds the image (ADR-0018) and the generator picks up the new tag via the PR's head SHA.
- **Isolation is tiered by what the PR changes**:
  - *Application PRs* (the common case) get a **namespace-scoped preview** — namespace `preview-pr-<number>`, standard tenant guardrails (ResourceQuota, LimitRange, NetworkPolicy, baseline PodSecurity), TTL-labeled
  - *PRs that change cluster-scoped machinery* (operators, CRDs, webhooks) get a **vcluster-backed preview** (ADR-0016), because a namespace cannot contain them
- **Routing is convention**: each preview gets `HTTPRoute` `https://pr-<number>.<app>.<domain>` attached to the shared Cilium Gateway (ADR-0002) under a wildcard host; TLS is terminated at the Gateway with the platform certificate, so previews are HTTPS from the first push. The preview URL is posted back to the PR as a comment/status by the CI pipeline.
- **Data is disposable and synthetic**: previews get ephemeral dependencies (a CNPG database from a template, seeded fixtures) — never production data. Previews needing "real" shared services consume the platform's dev-grade instances, not prod.
- **Cost control is structural**: quotas cap each preview; a TTL controller (CronOperation, ADR-0005) reaps previews whose PR went stale beyond N days even if the forge webhook was missed; `enabled`-gating per environment keeps previews off environments that shouldn't run them (e.g. prod clusters).

## Consequences

- ✅ Every PR is reviewable at a URL with zero reviewer setup; "works on my branch" becomes verifiable by product owners, not just engineers
- ✅ Fully declarative: preview state is derivable from forge state + Git; a dead CI job can't leak an environment because CI never owned the lifecycle
- ✅ Reuses installed machinery end-to-end (ArgoCD, Gateway, CNPG, Kyverno guardrails) — no preview-specific controller to operate
- ⚠️ Preview capacity is real capacity: N open PRs × quota must fit the target cluster; on the local profile previews contend with the platform itself (ADR-0012), so local previews default to 1–2 concurrent
- ⚠️ The PR generator polls or needs forge webhooks — poll interval bounds "push → preview updated" latency; webhook config is per-forge setup documented in the User Guide
- ⚠️ Secrets for previews must flow through ESO like everything else (ADR-0009) — teams must resist the shortcut of committing "harmless" preview credentials, which is exactly how real credentials end up in Git

## Implementation notes (2026-09)

Where the shipped package differs from the sketch above, and why:

- **One ApplicationSet for the org, not one per repo.** A matrix generator pairs the Gitea
  `scmProvider` generator (every repo carrying `preview/kustomization.yaml`) with the Gitea
  `pullRequest` generator (every open PR on it labelled `preview`). Opting a repository in is
  therefore a file in that repository, not a platform change — which keeps the Git-only write
  path intact for teams that do not own the `environments` repo.
- **Names are `<repo>-pr-<number>`; namespaces are `preview-<repo>-<number>`.** The sketch's
  `preview-pr-<number>` is not unique across repositories once more than one app opts in.
- **The overlay reconciles the two manifest layouts.** Backstage-scaffolded repos keep manifests
  in `manifests/`, Console-scaffolded ones in `deploy/`; `preview/kustomization.yaml` names its
  own base, so the platform does not have to guess a path.
- **The PR head SHA is the image tag.** `app-ci` now pushes every build under `:latest` *and*
  `:<commit sha>` and builds the head of any PR labelled `preview` (without the GitOps writeback,
  which must stay a main-branch action). The preview Application overrides the kustomize image tag
  with `head_sha`, so a preview can never silently serve main's image.
- **No TTL reaper for closed PRs.** Teardown is the generator's: the PR leaves the result set, the
  ApplicationSet controller deletes the Application, and `resources-finalizer.argoproj.io` prunes
  its resources — including the Namespace, which the overlay declares as a managed resource
  precisely so it is pruned. A CronOperation remains the right place to reap *abandoned but open*
  PRs if a fleet ever needs it.
- **Previews are unauthenticated-but-internal.** Fronting them with the platform oauth2-proxy
  needs a Keycloak client per hostname (`hack/gen-sso.sh`), and preview hostnames are created and
  destroyed with pull requests — provisioning and reaping those clients is exactly the
  preview-specific controller this ADR rejected. Previews are reachable only on the platform
  Gateway and carry synthetic data only.
- **`adhar.io/plane: preview`** is a third value of the ADR-0023 placement label; the
  `require-namespace-plane-label` policy was widened to `control | data | preview` so ephemeral
  namespaces do not show as governance violations.
