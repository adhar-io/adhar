# ADR-0022: Custom Kubernetes clusters on raw cloud infrastructure — no managed Kubernetes services

**Status**: Accepted (direction; compositions land incrementally, EKS/AKS/GKE paths remain until parity) · **Date**: 2026-07

## Context

Today `CompositeCluster` provisions managed Kubernetes (EKS/AKS/GKE/DOKS/Civo), and the management cluster on cloud is likewise managed. Managed control planes cost real money per cluster, differ per provider (version cadence, admission defaults, CNI constraints, API server flags you cannot set — e.g. OIDC issuer trust for the platform's own Keycloak, ADR-0008), and put the platform's most identity-critical component behind a vendor abstraction. The platform already replaces most managed add-ons (CNI, ingress, observability) anyway — the managed control plane is the last vendor-shaped piece. Alternatives considered: keep managed K8s (status quo — least effort, most drift between providers), Cluster API (a second large management-plane framework alongside Crossplane), or **build clusters from raw infrastructure with Crossplane** (VMs, networks, load balancers via the existing providers) with a lightweight distribution.

## Decision

**Adhar clusters are custom clusters built from cloud primitives; managed Kubernetes services are not used.**

- **Distribution**: k3s (server + agents) — single-binary, embedded etcd HA, upstream-conformant, air-gap friendly; installed via instance user-data with the join token passed through provider secret machinery
- **Workload clusters**: `CompositeCluster` compositions provision network + instances + LB per cloud family (compute/VPC/LB APIs of the existing Upbound providers) and bootstrap k3s; ArgoCD auto-registration (P2.1) reuses the generated kubeconfig connection details — the CompositeCluster API is unchanged, only Compositions swap, so tenants notice nothing (ADR-0005's portability promise)
- **Management cluster**: `adhar up -f config.yaml` provisions the same shape imperatively through the provider interface (ADR-0007's day-0 path targets VMs instead of managed-K8s APIs), then bootstraps the platform on it as today
- **Full API-server control**: OIDC flags trusting the platform Keycloak realm (completing the k8s-rbac.yaml story), audit policy, admission configuration — all become platform-ownable configuration
- **Kamaji stays the special case**: hosted control planes for hostile-tenant isolation (Phase 5) — complementary, not competing

## Consequences

- ✅ Identical Kubernetes everywhere (local Kind, cloud, on-prem run the same conformant distribution) — the strongest possible form of pillar 4 parity, and no per-provider control-plane quirks
- ✅ Control-plane cost drops to instance cost; API server flags (OIDC!) finally belong to the platform
- ✅ Reconstructability (P2.6) covers the control plane itself — it is just instances built from Git-declared infrastructure
- ⚠️ The platform inherits control-plane operations: etcd health/backup, version upgrades, node lifecycle — mitigated by k3s's operational simplicity, the existing backup machinery (ADR-0021), and upgrade automation that must land with the compositions
- ⚠️ Large migration surface: five cloud composition families to rewrite plus provider-interface cluster CRUD; managed-K8s paths remain supported until each cloud's custom path reaches parity (tracked per cloud on the Roadmap)
- ⚠️ Managed-K8s conveniences (cloud-controller integrations, IAM-for-pods) need explicit replacements (CCM per cloud, workload identity via SPIFFE — ADR-0024 territory)
