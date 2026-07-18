# ADR-0007: Dual provisioning paths — imperative provider interface + declarative Crossplane

**Status**: Accepted · **Date**: 2026-07

## Context

Adhar provisions infrastructure in two fundamentally different situations:

1. **Day-0**: no cluster exists yet. Something outside Kubernetes must create the management cluster — a chicken-and-egg problem no in-cluster tool (Crossplane included) can solve for itself.
2. **Day-1+**: the management cluster exists and should manage all further infrastructure (workload clusters, databases, networks) declaratively, with reconciliation and drift correction.

A single mechanism cannot serve both well: pure CLI/SDK provisioning gives no continuous reconciliation; pure Crossplane cannot bootstrap its own host.

## Decision

Maintain **two deliberate provisioning paths with a defined hand-off**:

- **Imperative path** — the Go `Provider` interface (`platform/providers/interface.go`, seven implementations, factory-instantiated). Scope: creating/attaching the management cluster and CLI-driven day-2 cluster operations (`adhar up`, `adhar cluster …`). Kept intentionally broad (node groups, VPC, LB, storage, health, cost) so the CLI is useful standalone.
- **Declarative path** — the Crossplane control plane (ADR-0005). Scope: everything after the management cluster exists — workload clusters (`CompositeCluster`), databases, networks, and all GitOps-managed infrastructure.

**Hand-off rule**: the imperative path's job ends when the management cluster is bootstrapped; anything that should *stay* managed belongs to Crossplane. The two paths share credentials (and should converge on workload identity), and any new provider should land in both (interface implementation + Compositions) to keep parity.

## Consequences

- ✅ Clean bootstrap story with no external IaC dependency, plus full GitOps reconciliation for steady state
- ✅ CLI remains useful without a running platform (create, inspect, destroy clusters)
- ⚠️ Two places understand each cloud — provider work has a dual cost; parity is a review-checklist item, and capability gaps between paths must be documented per provider
- ⚠️ Risk of scope creep: imperative day-2 features that belong in Crossplane should be resisted; the hand-off rule is the test
