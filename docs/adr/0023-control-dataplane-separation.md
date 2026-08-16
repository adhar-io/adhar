# ADR-0023: Control-plane / data-plane separation with a first-class DataPlane API

**Status**: Accepted (DataPlane API + controller, `adhar.io/plane` labels, control-plane Kyverno policy, and `adhar get dataplanes` / `adhar migrate split-planes` CLI scaffolded; live multi-cluster validation is Roadmap Phase 2) · **Date**: 2026-08

## Context

[ADR-0001](0001-management-cluster-first.md) established the management-cluster-first model: one cluster owns platform state and, in topology T3, provisions workload clusters via Crossplane. Today that split is a **soft convention** — "heavy multi-tenant services stay on the management cluster; workload clusters run a minimal agent footprint" ([ARCHITECTURE.md §8](../ARCHITECTURE.md)) — but nothing enforces it:

- In T1 (local) and T2 (single cloud cluster) the management cluster runs **both** the platform services (ArgoCD, Gitea, Keycloak, Crossplane, the observability hub) **and** the full application catalog (69+ packages) in the same `adhar-system` namespace.
- There is no first-class object representing "a data plane the control plane manages." Workload clusters are `CompositeCluster` XRs plus an ArgoCD cluster secret plus an ApplicationSet generator — three loosely-coupled pieces with no aggregate status, no lifecycle owner, and no invariant that keeps application workloads *off* the control plane.
- "Which cluster does this app run on?" has no declared answer; placement is implicit in whichever ApplicationSet happens to target which cluster label.

As the platform scales to many teams and many clusters, the fuzzy boundary becomes a liability: application workloads can be scheduled next to the GitOps engine and identity provider, blast radius is unclear, the control plane cannot be sized or secured as a small high-trust tier, and the fleet relationship (one control plane → N data planes) is invisible.

Alternatives considered:

- **Keep the soft convention** — least work, but the boundary keeps eroding under real multi-tenant load; the control plane accretes app workloads and cannot be hardened or scaled independently.
- **Physical split only in T3** — solves production but breaks the "local–production parity is sacred" commitment: the separation would be untested at T1/T2, so its code paths rot.
- **Two-role model, enforced at every topology, with a first-class DataPlane API** — one architecture that scales from one physical cluster (roles collapse, logically separated) to a fleet.

## Decision

Make the two cluster **roles** first-class and enforce a hard invariant: **application workloads run only on data planes; the control plane runs only fleet and platform services.**

**1. Two roles, one architecture.**

- **Control plane** (the management cluster): the fleet brain. Runs GitOps (ArgoCD, Gitea), IaC (Crossplane), identity (Keycloak, global OIDC), the secrets root (Vault + ESO controllers), the observability **hub** (Grafana + Mimir/Loki/Tempo storage & query), the shared registry/catalog (Harbor, later the Iceberg REST catalog), fleet controllers (`AdharPlatform` + the new `DataPlane` controller, Sveltos placement, Kargo promotion), and the agentic control layer ([ADR-0024](0024-agentic-ai-platform.md)). It hosts **no application workloads**.
- **Data plane** (a workload cluster): where applications run — team services, golden-path apps, data/ML workloads. It runs only a **thin agent profile**: Cilium (data path), metrics-server, Kyverno + policies, Alloy collectors (shipping to the hub), an ESO agent (syncing secrets from the central Vault), a SPIRE agent (workload identity), and a local Gateway for its own ingress. Its applications are delivered by the control plane's ArgoCD.

**2. A first-class `DataPlane` API** (`platform.adhar.io/v1alpha1`). A `DataPlane` custom resource, owned by the control plane, is the single object representing a managed data plane. Its controller reconciles the whole lifecycle — provision the cluster (composing a `CompositeCluster`), register it with ArgoCD, apply the thin-agent profile, attach it to the Cilium Cluster Mesh + SPIFFE trust domain, wire its telemetry to the hub — and reports aggregate status (`InfraReady`, `Registered`, `AgentsReady`, `MeshJoined`, `Ready`). The control plane's `AdharPlatform` gains a fleet view over its `DataPlane`s. This turns the implicit three-piece assembly into one observable, owned relationship: **one control plane, many data planes.**

**3. Declared placement.** "Which data plane runs this app" becomes explicit. The `environments` repo binds each environment (and/or tenant) to one or more data planes by label selector; **Sveltos** (already a platform package) and ArgoCD ApplicationSet cluster generators enforce the binding. Applications declare a target environment, not a cluster — placement is a control-plane concern, reconstructable from Git.

**4. Enforced invariant.** The control plane carries a Kyverno policy that rejects application-namespace workloads: only namespaces/packages labeled `adhar.io/plane: control` may schedule there; everything else must target a data plane. The package model ([ADR-0004](0004-applicationset-package-model.md)) splits accordingly — a **control-plane ApplicationSet** deploys only platform packages to the management cluster; **workload ApplicationSets** target data-plane clusters.

**5. Local–production parity via a local data plane.** At T1, the control plane and one data plane collapse onto a single Kind cluster, but the **logical** split is preserved by running the local data plane as a **vcluster** ([ADR-0016](0016-vcluster-local-first-development.md)): applications run inside the vcluster (data plane), platform services on the host (control plane). "Apps run on a data plane" stays true even on a laptop, and the same registration/placement/agent code paths that run at T3 are exercised at T1. T2 (single cloud cluster) runs the control plane plus a colocated vcluster data plane; scaling to T3 is adding physical data planes, not changing the architecture.

This **extends** ADR-0001 (it sharpens "management vs workload" into an enforced contract with an API) and builds on ADR-0004, ADR-0005, ADR-0010, and ADR-0016. It does not supersede any accepted ADR.

## Consequences

- ✅ **Clear blast-radius and trust boundaries** — the control plane is a small, high-trust, independently securable and HA-able tier; data planes are app-trust and scale out. A data-plane outage never touches the control plane; a control-plane outage degrades the platform to "no changes" while running apps keep running.
- ✅ **The fleet is first-class and observable** — `DataPlane` status makes "one control plane, many data planes" a real, reconcilable, `adhar get`-able relationship instead of three disconnected objects.
- ✅ **Independent scaling and sizing** — control-plane and data-plane capacity are decoupled; adding tenants means adding data planes, not fattening the management cluster.
- ✅ **Parity preserved** — the vcluster local data plane means the separation ships and is tested at T1, honoring the parity commitment; no T3-only code rot.
- ✅ **Declared, reconstructable placement** — app→cluster mapping lives in Git and is enforced by Sveltos, not implicit in generator wiring.
- ⚠️ **More moving parts and cross-plane dependencies** — data-plane apps depend on control-plane identity, registry, and secrets; those must be reachable across the plane boundary (via Cluster Mesh, HTTPRoutes, and the ESO/SPIRE agents). The connectivity contract must be explicit and tested.
- ⚠️ **Two GitOps scopes** — a control-plane appset and workload appsets; placement logic must be correct or apps land nowhere / everywhere. Parity tests must cover the split the way they cover local↔production today.
- ⚠️ **The control plane becomes a hard dependency** for provisioning, identity, and secrets — reinforcing the Phase 1 requirement that it get HA and DR first.
- ⚠️ **Migration** — existing T2 single-cluster installs must transition from dual-role to split roles (introduce a colocated vcluster data plane, move app packages onto it). This needs a documented, reversible migration path, not a flag day.

See the full detailed design (goals/invariants, Go types, controller reconcile, manifests, placement, connectivity, migration, CLI, milestones, tests, file inventory) in [docs/design/0023-control-dataplane-separation.md](../design/0023-control-dataplane-separation.md).
