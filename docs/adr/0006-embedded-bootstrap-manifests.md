# ADR-0006: Embedded, pre-rendered manifests for bootstrap

**Status**: Accepted · **Date**: 2026-07

## Context

Bootstrap installs Cilium, Gateway resources, ArgoCD, and Gitea before any GitOps machinery exists. Options:

- **Helm at runtime** — requires chart repos reachable at bootstrap, a Helm engine dependency, and yields non-deterministic output as upstream charts move
- **Fetch manifests from the network** — same availability problem; breaks air-gapped and flaky-network environments
- **Embed pre-rendered manifests in the binary** — render once at development time, ship in the release artifact

## Decision

Pre-render foundation manifests from pinned chart versions (values under `hack/`) and **embed them via `go:embed`** in `platform/controllers/adharplatform/resources/` (argocd/, cilium/, gitea/, gateway/, gateway-api/). The controller applies them with **Server-Side Apply + `ForceOwnership`**, with owner references for lifecycle tracking. CRDs for the platform's own API group are likewise embedded and installed by the CLI.

The same pattern extends down the stack: GitOps packages are pre-rendered too (ADR-0004), and the Crossplane control plane ships as an embedded configuration filesystem — the binary is a complete, self-contained platform installer.

## Consequences

- ✅ Bootstrap is deterministic and reproducible: a given binary always installs exactly the same foundation
- ✅ Works offline/air-gapped; no chart-repo, registry-metadata, or GitHub availability in the critical path (container images still need a registry or preload)
- ✅ SSA + ForceOwnership makes re-runs idempotent and drift attributable to a field owner
- ⚠️ Foundation upgrades are release-coupled: bumping Cilium/ArgoCD/Gitea means regenerating embedded manifests (`hack/` scripts) and cutting a release — a deliberate trade for determinism
- ⚠️ Binary size grows with embedded content (accepted; distribution is via compressed archives)
- ⚠️ Customizing foundation components is a platform-developer operation (regenerate + rebuild), not a user operation — user-level knobs must be surfaced through the `AdharPlatform` CR spec instead
