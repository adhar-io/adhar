# ADR-0014: Package lifecycle operations — toggling, verification, and clean removal

**Status**: Accepted · **Date**: 2026-07

## Context

ADR-0004 gives every package an `enabled` gate in one ApplicationSet. The promise behind that gate — *any package can be turned on at any time and works* — is only real if it is continuously verified, and if turning a package **off** actually returns the cluster to its prior state. Both halves proved non-trivial on a live platform:

- Verifying 49 optional packages by enabling them all at once collapsed the node (see ADR-0012); verification has to be batched
- ArgoCD's cascade deletion deadlocks when its repo-server is under load, leaving disabled packages half-removed for hours
- Full-file `kubectl apply` of the 73-element ApplicationSet times out on a loaded API server, and a *timed-out apply can still land later*, silently reverting narrower changes made in between
- Package health ≠ sync status: ArgoCD reports sync `Unknown` whenever repo-server is loaded, which is an ArgoCD artifact, not a package failure
- Removed packages can leave cluster-scoped debris — most dangerously admission webhook configurations pointing at deleted services, which then degrade every API write in the cluster

## Decision

Package on/off transitions and their verification follow fixed operational rules:

1. **Toggle via targeted patch, not file re-apply.** Runtime enable/disable of individual packages patches only the affected list elements (JSON patch by index). The full ApplicationSet file is the *declarative source* applied by the controller at bootstrap and on deliberate whole-set changes — never as a side effect of toggling one package, because a large SSA apply under load can time out client-side yet land server-side later with stale content.
2. **Verify in small waves against health, not sync.** The verification loop (used for release qualification of the package catalog) enables ≤4 packages at a time, requires the cluster to be calm before each wave (API healthy, pod churn settled), kicks an explicit sync per new app rather than waiting for auto-sync scan order, and passes a package when its ArgoCD **health** is `Healthy` — sync status is ignored as a pass criterion for the reason above. Failures are recorded with resource-level reasons for triage.
3. **Removal must converge, forcibly if needed.** Disable → allow graceful prune a bounded time → then strip app finalizers and delete remaining resources by instance label, **including cluster-scoped kinds**: ClusterRole(Binding)s, ValidatingWebhookConfigurations, MutatingWebhookConfigurations, and APIServices. Orphaned webhooks and aggregated APIServices from removed packages break API discovery and writes cluster-wide and must never survive a removal.
4. **The catalog carries verification state.** Each package's verified/known-broken status and its constraints (mutual exclusions from ADR-0011, resource weight, slow-start behavior) live with the package docs so "can be turned on anytime" is a checkable claim, not folklore.

## Consequences

- ✅ Enable-anytime is backed by per-package verification evidence instead of assumption
- ✅ Toggling is safe under load (small writes cannot half-land) and removal cannot poison the cluster with dead webhooks
- ✅ Verification methodology is reusable for every catalog addition and upgrade cycle
- ⚠️ Waved verification of the full catalog takes hours of wall-clock on a laptop-class node — accepted; it is a qualification activity, not a user-facing operation
- ⚠️ Force-removal by label depends on packages carrying accurate `app.kubernetes.io/instance` labels (ArgoCD applies these; hand-added resources must follow suit)
- ⚠️ Health-not-sync as the pass signal can mask genuine sync failures that leave stale-but-healthy resources; the wave loop compensates by recording sync state in the report for later inspection
