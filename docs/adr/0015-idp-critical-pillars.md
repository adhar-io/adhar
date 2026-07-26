# ADR-0015: Critical pillars of the IDP — the tests every addition must pass

**Status**: Accepted · **Date**: 2026-07

## Context

Adhar integrates 50+ tools, and the catalog keeps growing. Without an explicit definition of what the platform *is*, an IDP degenerates into a bag of parts: every popular CNCF project gets wired in, every wiring choice is re-litigated per package, and the operator inherits N tools with N philosophies. Individual ADRs record individual decisions, but nothing records the **criteria those decisions are tested against** — which makes it impossible to evaluate a proposal ("should we add X?") except by taste.

The industry framing (CNCF platforms whitepaper, Team Topologies' *platform as a product*) is directionally useful but too abstract to review a PR against. Adhar needs its own pillars, stated concretely enough that a reviewer can say "this violates pillar 4" and be understood.

## Decision

Every feature, package, and architectural change is tested against **eight pillars**. A change that violates a pillar needs an ADR explaining why the exception is justified — silence is not an option.

1. **One command, whole platform.** `adhar up` must always produce a complete working platform with zero manual steps; anything that adds a manual step to bootstrap is rejected (enforced by ADR-0001, ADR-0006, ADR-0013). The test: *can a new laptop reach a working platform with one command and no accounts?*
2. **Git is the only write path.** After bootstrap, all platform state changes flow through Git (ADR-0001, ADR-0003, ADR-0004). Imperative escapes exist only where declared and bounded (ADR-0007, ADR-0014). The test: *could the cluster be deleted and rebuilt from Git alone (plus the secret store)?*
3. **Self-service with guardrails.** Developers provision what they need (environments, databases, clusters, previews) through versioned platform APIs — Crossplane XRs (ADR-0005), vclusters (ADR-0016), preview environments (ADR-0017) — never through tickets, and never with raw cloud credentials. The test: *does the new capability have a namespaced API a tenant can use under standard RBAC?*
4. **Local–production parity is sacred.** Every capability must run (scaled down) on a laptop Kind cluster and (scaled up) in production — same manifests, same wiring, different values (ADR-0012 exists to make this true). The test: *can this be exercised in T1?* Nothing lands in T2/T3 that can't.
5. **Secure by default, not by add-on.** Identity (ADR-0008), secrets handling (ADR-0009), supply-chain integrity (ADR-0019), and policy are wired into the platform's own operation first, then offered to workloads — the platform must pass the security bar it sets for tenants. The test: *does the feature work with SSO, without secrets in Git, with signed/scanned images?*
6. **Observable by construction.** Every component's metrics, logs, and (where applicable) traces land in the standard pipeline (ADR-0010) without per-component setup. The test: *after enabling the package, does Grafana already see it?*
7. **Day-2 is designed, not discovered.** Backup, upgrade, scaling, cost, and removal paths are part of a capability's definition of done (ADR-0014, ADR-0021). The test: *is there a documented, exercised path to upgrade it and to remove it cleanly?*
8. **100% open source, no lock-in.** Apache-2.0-compatible components only; every integration point uses a portable standard (Gateway API, OTel, OIDC, S3 API, Iceberg tables) so any single vendor/project can be replaced at a seam. The test: *if this project dies tomorrow, what is the migration story?*

The pillars are ordered by precedence: when two collide (e.g. a security control that would add a bootstrap manual step), the lower-numbered pillar wins unless an ADR argues otherwise.

## Consequences

- ✅ Proposals get evaluated against stated criteria instead of taste; "violates pillar N" is a reviewable objection
- ✅ The existing ADR corpus gains coherence — each ADR is an instantiation of one or more pillars, and the pillar list is the map
- ✅ Exceptions become visible and deliberate (they require an ADR), which is exactly where architectural debt should live
- ⚠️ Pillars constrain: some popular tools will be rejected or wrapped (e.g. anything SaaS-only fails pillar 8, anything requiring manual bootstrap fails pillar 1)
- ⚠️ The precedence rule is a blunt instrument; genuinely hard trade-offs still need judgment and an ADR — the pillars frame the argument, they don't end it
- ⚠️ Pillar 4 (parity) has real engineering cost — heavy stacks need working scaled-down profiles (see ADR-0012, ADR-0020) — which is accepted as the price of "test locally, trust production"
