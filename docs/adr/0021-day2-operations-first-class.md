# ADR-0021: Day-2 operations as a first-class product surface

**Status**: Accepted · **Date**: 2026-07

## Context

Most platforms are judged by their demo (`adhar up` in 10 minutes) but lived-in through day 2: upgrades, backup/restore, capacity, cost, incident response, and package churn. If those are left as "operator figures it out," the 50-tool catalog becomes a 50-tool liability — each with its own upgrade cadence, backup semantics, and failure modes. Fragments of the answer already exist (ADR-0014 package lifecycle, ADR-0012 resilience tuning, Velero/OpenCost/OnCall packages, `adhar get status` conditions, PRODUCTION.md runbooks) but nothing states the overall stance: **who owns day-2 mechanics — the platform or its operator?**

## Decision

**The platform owns day-2 mechanics; the operator owns day-2 decisions.** Concretely, every capability ships with its operational lifecycle designed in (pillar 7, ADR-0015), surfaced through a small set of standard mechanisms:

1. **Status is one command.** `adhar get status` is the single pane for platform health: AdharPlatform conditions (`Ready`, `ArgoCDReady`, `GatewayReady`, `GiteaReady`, `CrossplaneReady`, `GitOpsReady`), per-package ArgoCD health, and access URLs. Deep diagnosis flows to Grafana/Hubble/Headlamp (ADR-0010), but "is the platform OK and where do I look next" never requires tool archaeology.
2. **Backup/DR is shipped, not suggested.** Velero (cluster state to object storage) and CNPG scheduled backups + PITR (all platform databases: Gitea, Keycloak, Harbor, catalog metadata) ship as packages with schedules enabled in production profiles. The measure of DR is the **restore runbook, exercised** — Roadmap Phase 2's reconstructability SLO (any cluster rebuilt from Git in < 1 hour) is the acceptance test, run as scheduled drills. Because Git + secret store + object storage hold everything (pillar 2), restore is re-bootstrap + data restore, not snapshot archaeology.
3. **Upgrades are a converge-and-diff flow.** `adhar upgrade` (Roadmap Phase 1) re-converges the embedded foundation (ADR-0006) idempotently via SSA, then presents the stack diff (pre-rendered manifests, ADR-0004 — so the Git diff *is* the cluster diff) for review before sync. Package upgrades ride the same wave rules as enablement (small batches, health-gated, ADR-0014); foundation and catalog versions are released and tested *together* as one platform version — the platform's core compatibility promise.
4. **Package churn follows ADR-0014.** Enable/disable/verify/remove rules (targeted patches, health-not-sync gates, forced convergence including cluster-scoped debris) are the only sanctioned lifecycle mechanics; ad-hoc `kubectl delete` of platform packages is off the paved road.
5. **Cost and incident response are in the box.** OpenCost attributes spend per namespace/workload from day one (local included — laptop RAM is also a budget); Grafana alerting + OnCall route incidents; alert rules ship with packages, not as operator homework.
6. **Runbooks are part of definition-of-done.** PRODUCTION.md carries the operational procedures (HA, hardening, backup/DR, upgrade); a capability without its runbook section is incomplete (Roadmap cross-cutting commitment: every feature ships with docs).

The dividing line: the platform automates *mechanism* (how to back up, how to upgrade, how to remove) and the operator retains *policy* (retention windows, upgrade timing, what's enabled where) — expressed declaratively through environment configs and the `AdharPlatform` CR, never by editing platform internals.

## Consequences

- ✅ Day-2 competence stops being tribal: status, backup, upgrade, and removal have one documented, tool-supported path each — the same on a laptop and in production (pillar 4)
- ✅ Restore-from-Git as the DR model turns disasters into bounded, drilled procedures and doubles as the ultimate drift audit
- ✅ Shipping schedules/alerts/runbooks with packages keeps the catalog honest — a package that can't state its backup and upgrade story isn't done
- ⚠️ Platform-owned mechanics mean platform-owned bugs: a bad converge or backup default affects everyone; this is accepted — centralized, reviewed, tested mechanics beat 50 bespoke ones
- ⚠️ Scheduled drills and waved upgrades cost real wall-clock and discipline; they are qualification activities the platform team must actually run, or the SLO decays into fiction (same trap as ADR-0014's verification evidence)
- ⚠️ The one-platform-version promise couples release cadence to the slowest-moving component; urgent single-package CVE bumps use the package-level path (ADR-0004 regenerate + wave) with a platform patch release to follow
