# Adhar Roadmap

**Version**: v0.1.0 · Direction, not commitment — sequencing follows community needs. Discuss on [GitHub Discussions](https://github.com/adhar-io/adhar/discussions).

The destination is fixed: the best open-source Internal Developer Platform — one command to a complete, production-grade, customizable platform on any infrastructure. The [Architecture](ARCHITECTURE.md) describes the target design (topologies T1→T3); **this roadmap is the main tracker** — every capability is listed under its phase with its current status.

**Legend**: ✅ implemented and exercised · 🟡 implemented, needs hardening/verification · 🔜 designed, not yet built

---

## Phase 0 — Local Excellence (v0.1.x) ✅ COMPLETE

The foundation everyone evaluates first: `adhar up` on a laptop must be flawless.

- ✅ Deterministic bootstrap: Gateway API → Cilium → Gateway → ArgoCD → Gitea
- ✅ GitOps package model: 69 packages wired, curated local core enabled
- ✅ Crossplane v2 control plane (23 XRDs, 34 Compositions) packaged and installable
- ✅ Automated releases (GoReleaser + GitHub Actions: binaries, GHCR images, Homebrew)
- ✅ Standard status conditions on `AdharPlatform` (ArgoCDReady, GatewayReady, GiteaReady, CrossplaneReady, GitOpsReady, aggregate Ready) with reconcile-failure messages, surfaced by `adhar get status`
- ✅ Unit test suite fully green (was ~15 known-failing tests; fixes included restoring the `adhar://` source-replication feature, source-deletion mirroring to Git, tag/branch checkout, secret patching, and scheme gaps)
- ✅ Package health dashboard: `adhar get status` shows per-package ArgoCD health/sync with a healthy/progressing/degraded summary
- ✅ E2E test covering the full bootstrap sequence (`make e2e`): `adhar up` → Ready condition → foundation components (deployments, pinned Gateway node ports, CRDs) → seeded Gitea repos via the external route → ArgoCD API auth → ApplicationSet package health → CLI status → `adhar down`

## Phase 1 — Single-Cluster Production (T2)

Make one managed cluster a defensible production platform.

- **In-cluster controllers**: run the manager as a Deployment for continuous reconciliation (today it exits after local deployment)
- **HA mode end-to-end**: replicas/PDBs/topology-spread applied from `enableHAMode`; Gitea + Keycloak on CNPG PostgreSQL
- **Production edge**: cert-manager ClusterIssuer + external-dns wired to the Gateway out of the box
- **SSO by default**: Keycloak OIDC pre-wired into ArgoCD, Gitea, Grafana, Console; bootstrap-credential rotation flow
- **Backup/DR**: Velero + CNPG backup schedules shipped enabled; documented, tested restore runbook ([Production §5](PRODUCTION.md#5-backup-and-disaster-recovery))
- **Upgrade story**: `adhar upgrade` — converge foundation + present stack diff for review before sync

## Phase 2 — Multi-Cluster Platform (T3)

The management cluster earns its name.

- **Workload clusters via GitOps**: `CompositeCluster` provisions EKS/AKS/GKE/DOKS/Civo clusters that auto-register with ArgoCD
- **Thin workload-cluster profile**: minimal agent set (Cilium, Alloy, policy) with the management cluster as hub
- **Observability hub**: Mimir/Loki/Tempo centralized; workload clusters ship, hub stores and queries
- **Cilium Cluster Mesh** between management and workload clusters; SPIFFE-based workload identity
- **Environment promotion**: dev → staging → prod as first-class Git promotion, Kargo-orchestrated
- **Cluster reconstructability SLO**: any workload cluster rebuilt from Git in < 1 hour, verified by scheduled drills

## Phase 3 — Developer Experience & Ecosystem

From platform to product.

- **Golden paths**: production-quality templates (microservice, frontend, data pipeline, ML) scaffolded from the Console with repo, CI, deployment, and observability pre-wired
- **Score cards**: per-service production-readiness scoring surfaced in the Console
- **Package marketplace**: community-contributed packages with a compatibility contract and provenance (signed, scanned)
- **Policy packs**: opt-in compliance profiles (CIS, SOC2-oriented) as Kyverno bundles
- **AI-assisted operations**: platform-aware assistant for debugging and change suggestions

## Cross-Cutting Commitments

- **No breaking changes without an ADR** and a documented migration
- **Local–production parity is sacred** — nothing lands in T2/T3 that can't be exercised (scaled down) in T1
- **Every feature ships with docs** — the [docs set](README.md) is part of the definition of done
- **APIs graduate deliberately**: `v1alpha1` → `v1beta1` once controllers are in-cluster and covered by e2e

## How to Influence This

- 👍 existing issues, or open one with the `roadmap` label
- Propose designs as ADRs ([docs/adr](adr/README.md))
- Bring a use case to [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww) — real adoption stories set priorities
