# ADR-0005: Crossplane v2 namespaced XRs for self-service infrastructure

**Status**: Accepted · **Date**: 2026-07

## Context

Self-service infrastructure ("give me a database/cluster/bucket") needs a Kubernetes-native API with guardrails. Crossplane v1's claim/XR split (cluster-scoped XRs + namespaced claims) doubled the object model and confused RBAC. Crossplane v2 introduces **namespaced XRs** (`apiextensions.crossplane.io/v2`, `scope: Namespaced`) that remove claims entirely, plus namespaced managed resources (`.m` API groups).

Alternative approaches — Terraform-only (no continuous reconciliation, no K8s RBAC surface) or per-cloud operators (no portable API) — don't meet the multi-cloud + self-service goals. Terraform remains available as a package for teams that need it.

## Decision

Build the control plane on **Crossplane v2.3+ with the namespaced model**:

- XRDs use `apiextensions.crossplane.io/v2`, `scope: Namespaced`, no claims — 23 platform APIs (`CompositeCluster`, `CompositeDatabase`, `CompositeApplication`, …)
- Compositions stay `apiextensions.crossplane.io/v1`, **Pipeline mode** with functions (KCL, go-templating, patch-and-transform, auto-ready, Python)
- Managed resources use namespaced `.m` API groups (`*.aws.m.upbound.io`, `kubernetes.m.crossplane.io`, `helm.m.crossplane.io`) referencing shared cluster-scoped `ClusterProviderConfig`s per cloud family
- Distributed as a Configuration package (`.xpkg`), built by `make build-control-plane`; conventions in `platform/controlplane/CONVENTIONS.md`
- Operations (`--enable-operations`): CronOperations for scheduled tasks, WatchOperations for drift response

## Consequences

- ✅ Tenant-safe by construction: a team's `CompositeDatabase` lives in *their* namespace; standard RBAC applies
- ✅ Same API, provider-appropriate implementation (CNPG locally, RDS/CloudSQL/Azure DB in cloud) via Composition selection
- ✅ Platform APIs are versioned, packaged, and upgradable as one artifact
- ⚠️ Requires Crossplane v2.3+ and the v2-style Upbound providers; legacy `ProviderConfig` remains only for DigitalOcean/Civo until their providers catch up
- ⚠️ Composition authoring (KCL/templating) is a skill the platform team must own; conventions doc mitigates
