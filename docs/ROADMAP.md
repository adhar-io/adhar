# Adhar Roadmap

This roadmap tracks the platform direction by delivery phase. Each point is
kept compact and outcome-focused.

## Current focus

- Stabilize Knative-first Functions end to end (API, templates, docs, runtime).
- Keep local startup predictable while preserving production parity.
- Continue reducing package overlap and operational drag.

## Phase 0 — Local Excellence (v0.1.x)

- Fast local bootstrap with deterministic order and clear health checks.
- GitOps seeding flow that is repeatable on a clean machine.
- Day-0 defaults that fit a laptop without hiding production behavior.

## Phase 1 — Single-Cluster Production

- Harden one-cluster operations: identity, ingress, backups, recovery, and drift control.
- Ensure provider docs map directly to working configs and operational limits.
- Keep upgrades and rollback paths explicit for operators.

## Phase 2 — Multi-Cluster Platform

- Control-plane/data-plane separation with one control surface for many clusters.
- DataPlane lifecycle APIs for register, observe, and retire flows.
- Shared policy, identity, and observability patterns across fleet members.

## Phase 3 — Developer Experience & Ecosystem

- Golden-path templates for service, frontend, data, and function workloads.
- Progressive delivery and CI/CD primitives with secure-by-default settings.
- Console UX improvements for marketplace, operations, and troubleshooting paths.

## Phase 4 — Data & Intelligence Platform

- First-class data services (lakehouse, databases, event streams, search/vector paths).
- AI runtime and gateway capabilities integrated with platform policy and identity.
- Safer defaults for model/runtime integrations and auditability.

## Phase 5 — Enterprise Readiness & Scale

- Availability, compliance, and operational controls for larger organizations.
- Capacity and cost controls across control plane and workload fleets.
- Stronger tenancy boundaries and governance automation.

## Cross-cutting commitments

- GitOps-first reconciliation and reviewable change history.
- Security baseline and policy enforcement with explicit exceptions only.
- Observable operations with actionable diagnostics and runbooks.
- Backward-compatible upgrade posture whenever practical.

## How to influence this roadmap

- Open or comment on [GitHub Issues](https://github.com/adhar-io/adhar/issues)
  for feature requests and bugs.
- Start design discussions in
  [GitHub Discussions](https://github.com/adhar-io/adhar/discussions).
- Propose long-lived architectural changes through
  [ADRs](./adr/README.md).
