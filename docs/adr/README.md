# Architecture Decision Records

Decisions with lasting consequences for the Adhar platform are recorded here in the lightweight [ADR format](https://adr.github.io): context, decision, consequences. ADRs are immutable once **Accepted** — a change of direction gets a new ADR that supersedes the old one.

| # | Title | Status |
|---|-------|--------|
| [0001](0001-management-cluster-first.md) | Management-cluster-first with two-phase bootstrap | Accepted |
| [0002](0002-cilium-cni-and-gateway.md) | Cilium as CNI, kube-proxy replacement, and Gateway API implementation | Accepted |
| [0003](0003-in-cluster-gitea.md) | Self-hosted in-cluster Gitea as the platform source of truth | Accepted |
| [0004](0004-applicationset-package-model.md) | Single ApplicationSet with enabled-gated package list | Accepted |
| [0005](0005-crossplane-v2-namespaced.md) | Crossplane v2 namespaced XRs for self-service infrastructure | Accepted |
| [0006](0006-embedded-bootstrap-manifests.md) | Embedded, pre-rendered manifests for bootstrap | Accepted |
| [0007](0007-dual-provisioning-paths.md) | Dual provisioning paths: imperative provider interface + declarative Crossplane | Accepted |
| [0008](0008-keycloak-platform-identity.md) | Keycloak as the platform identity provider (OIDC everywhere) | Accepted |
| [0009](0009-secrets-eso-vault.md) | Secrets: ESO as sync plane, Vault as source of truth, never Git | Accepted |
| [0010](0010-observability-lgtm-otel.md) | Observability: OTel collection, Grafana LGTM storage, hub-and-spoke | Accepted |

## Proposing an ADR

1. Copy the format of an existing ADR into `docs/adr/NNNN-short-title.md` (next free number), status **Proposed**
2. Open a PR; discussion happens on the PR
3. On merge with maintainer approval, set status to **Accepted** and add it to the table above
