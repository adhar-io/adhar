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

Both phases are driven off the `AdharPlatform` CR's `.status`: each phase is gated on a status flag (the foundation on per-component `Available`/`ControlPlaneApplied`; the GitOps phase on `RepositoriesCreated`). This makes the bootstrap **idempotent and resumable** — a failure or interruption re-runs only the pending phase, and the process is never allowed to report success while any gate is unmet. The low-level design ([design/0001](../design/0001-management-cluster-first.md)) is the authoritative as-built reference; operational procedures live in the [User Guide — Bootstrap & Day-2 Operations](../USER_GUIDE.md#6-bootstrap--day-2-operations) and failure recovery in [Troubleshooting](../TROUBLESHOOTING.md).

## Consequences

- ✅ One source of truth; workload clusters are disposable and reconstructable
- ✅ Bootstrap is deterministic and offline-capable; GitOps owns everything else, so drift is self-healed
- ✅ Local and production differ in size and controller placement, not in architecture
- ✅ Status-gated, resumable phases make the bootstrap rock-solid: idempotent SSA re-application, deterministic install ordering (Gateway API CRDs → Cilium → Cilium Gateway → ArgoCD → Gitea), and a usable-platform exit gate mean a clean `adhar up` cannot finish half-wired
- ⚠️ In local `ExitOnSync` mode the controller is ephemeral (in-process, exits on convergence), so an interruption mid-bootstrap has nothing to retry until the operator **re-runs `adhar up`** — which resumes from the pending gate. Production installs run the in-cluster controller and self-heal without a re-run
- ⚠️ The management cluster is a critical dependency — it needs HA and DR first (Production Guide); its outage degrades the platform to "no changes" but never stops running workloads
- ⚠️ Two code paths (imperative bootstrap vs declarative operation) must stay consistent; the boundary is fixed at "foundation vs everything else"
