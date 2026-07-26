# ADR-0016: vCluster as the virtual-cluster primitive for local-first development and tenancy

**Status**: Accepted (package shipped in `core/vcluster`; Composition wiring lands with Roadmap Phase 2) · **Date**: 2026-07

## Context

Developers and tenant teams need Kubernetes clusters that are cheap, fast, and disposable: a place to test operators and CRDs, run integration tests against a clean API server, try a platform-package upgrade, or host a preview environment (ADR-0017) — without waiting for cloud provisioning or endangering the shared platform. Namespaces alone are not enough: CRDs, admission webhooks, cluster-scoped RBAC, and API-server version are all cluster-global, so "test my operator in a namespace" is a fiction. Options considered:

- **Separate Kind/cloud clusters per need** — real isolation, but each Kind cluster costs ~2 GB and minutes of startup; cloud clusters cost real money and 15+ minutes; neither is self-service through the platform API, and every extra cluster needs its own GitOps/observability wiring
- **Namespace-as-environment only** — near-zero cost, but no CRD/webhook/version isolation; a tenant installing an operator affects everyone (ADR-0011 makes the platform namespace shared, which sharpens this)
- **Kamaji hosted control planes** — real control planes as pods, but each still needs *worker nodes* joined to it; right for multi-tenant production node pools, wrong for laptop-scale ephemeral clusters (package exists in `core/Kamaji` for the former case)
- **vCluster** — a certified virtual cluster: its own API server, controller manager, and datastore run as pods in a host-cluster namespace, while workloads sync to the host and share its nodes, CNI, and capacity

## Decision

**vCluster is the platform's virtual-cluster primitive.** The `core/vcluster` package ships the control-plane chart (wired in the ApplicationSet, `enabled`-gated per environment; external access goes through the Cilium Gateway — the chart's ingress stays disabled, per the package `values.yaml`).

- **Local-first development**: a vcluster on the local Kind host gives a developer a clean, disposable cluster in seconds — own API server, own CRDs, own admission chain, freely installable/deletable — while reusing the host's Cilium, storage, and images. Full `adhar up` teardown/recreate stops being the reset mechanism for everyday experiments.
- **Isolation boundary placement**: namespaces isolate *workloads* (tenant apps under RBAC/NetworkPolicy); vclusters isolate *Kubernetes itself* (CRDs, webhooks, API versions, cluster-scoped objects). Anything that needs to install cluster-scoped machinery belongs in a vcluster, not the host.
- **One API across sizes**: `CompositeCluster` (ADR-0005) gains a vcluster-backed Composition, so "give me a cluster" resolves to a vcluster locally/for ephemeral needs and to EKS/AKS/GKE for durable workload clusters — same claim shape, provider-appropriate weight (ADR-0007's declarative path).
- **GitOps applies inside**: each vcluster is registerable as an ArgoCD destination cluster; the host platform stays the management plane (ADR-0001) and vclusters are workload targets, not platform copies.
- **Version skew testing**: a vcluster can run a different Kubernetes minor version than its host, which is the platform's supported way to test tenant workloads against upcoming Kubernetes versions without a second physical cluster.

## Consequences

- ✅ Cluster-grade isolation at namespace-grade cost: seconds to create, one `helm`-sized footprint, shared nodes — practical even on the 8-CPU local profile (ADR-0012)
- ✅ Restores real self-service (pillar 3, ADR-0015): "I need a cluster" becomes an API call a tenant can make, not a ticket or a second `adhar up`
- ✅ The blast radius of experiments (operators, webhooks, CRD upgrades) is contained to the vcluster; the shared `adhar-system` host (ADR-0011) is protected
- ⚠️ Shared-kernel/shared-node reality: vclusters are **not** a hard security boundary against hostile tenants — hostile-tenant isolation needs separate node pools (Kamaji) or separate clusters; vclusters are an *operational* isolation tool
- ⚠️ Synced-resource edges exist (host-visible pod names are rewritten, some controllers assume they own the real cluster); packages certified "runs in vcluster" must be tested there, not assumed
- ⚠️ Each vcluster is another etcd-like datastore (SQLite/embedded by default) that day-2 rules apply to — ephemeral vclusters should be rebuilt, not backed up; durable ones must declare a datastore and backup story (ADR-0021)
