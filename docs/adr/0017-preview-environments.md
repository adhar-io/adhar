# ADR-0017: Ephemeral preview environments per pull request

**Status**: Accepted (namespace-scoped previews first; vcluster-backed previews follow ADR-0016 Composition wiring) · **Date**: 2026-07

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
