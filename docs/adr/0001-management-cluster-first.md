# ADR-0001: Management-cluster-first with two-phase bootstrap

**Status**: Accepted · **Date**: 2026-07

## Context

An IDP needs a control plane that provisions clusters, hosts shared services (Git, GitOps, identity, observability), and remains the source of truth. Alternatives considered:

- **Per-cluster standalone platforms** — every cluster carries its own full stack; no single source of truth, N× operational cost, config drift between clusters
- **External SaaS control plane** — conflicts with the 100%-open-source, no-lock-in goal
- **Management cluster** — one cluster owns platform state and provisions/governs workload clusters

A second question is how the management cluster itself comes to exist: a pure-GitOps system cannot bootstrap itself (ArgoCD cannot install the CNI it needs to schedule pods; the Git server it syncs from doesn't exist yet).

## Decision

Adopt **management cluster first** with a **two-phase lifecycle**:

1. **Bootstrap phase (imperative)** — the CLI/controller installs a minimal, strictly ordered foundation: Gateway API CRDs → Cilium → Cilium Gateway → ArgoCD → Gitea. Idempotent Server-Side Apply; embedded manifests (see ADR-0006).
2. **GitOps phase (declarative)** — the controller seeds Gitea with the `packages` and `environments` repos and hands control to an ArgoCD ApplicationSet. From then on, Git is the only write path.

The same model scales down (local Kind: management and workload roles collapse into one cluster; controllers exit after deployment) and up (topology T3: Crossplane on the management cluster provisions workload clusters; controllers run in-cluster continuously).

## Consequences

- ✅ One source of truth; workload clusters are disposable and reconstructable
- ✅ Bootstrap is deterministic and offline-capable; GitOps owns everything else, so drift is self-healed
- ✅ Local and production differ in size and controller placement, not in architecture
- ⚠️ The management cluster is a critical dependency — it needs HA and DR first (Production Guide); its outage degrades the platform to "no changes" but never stops running workloads
- ⚠️ Two code paths (imperative bootstrap vs declarative operation) must stay consistent; the boundary is fixed at "foundation vs everything else"
