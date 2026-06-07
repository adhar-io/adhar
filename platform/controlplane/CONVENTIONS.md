# Adhar Control Plane — Crossplane v2 Conventions

This document is the authoritative reference for how the Adhar control plane is
built on **Crossplane v2** (core `>=v2.3.0`). Every XRD, Composition, Function,
ProviderConfig, and Operation in `configuration/` follows these rules.

## 1. The v2 namespaced model

Crossplane v2's headline change is **namespaced composite resources**. Adhar
adopts the namespaced model everywhere:

- **XRDs** use `apiextensions.crossplane.io/v2` with `spec.scope: Namespaced`.
- **Claims are gone.** There is no `claimNames` and no separate claim kind.
  Users create the composite (XR) kind directly in their namespace.
- **`connectionSecretKeys` is removed** from XRDs — v2 XRs have no native
  connection-detail propagation. Compositions that need to surface credentials
  create a `Secret` explicitly (native object or provider-kubernetes `Object`).
- Crossplane injects a reserved `spec.crossplane` stanza into every XR
  (`compositionRef`, `compositionSelector`, `compositionRevisionRef`,
  `compositionUpdatePolicy`). **Never** declare `spec.crossplane` in an XRD
  schema. Composition selection is driven by `spec.crossplane.compositionSelector`
  or the XRD's `defaultCompositionRef`.

### XRD skeleton

```yaml
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: composite<things>.platform.adhar.io
spec:
  group: platform.adhar.io
  scope: Namespaced
  names:
    kind: Composite<Thing>
    plural: composite<things>
  defaultCompositionRef:
    name: <default-composition-name>   # optional but recommended
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema: { ... }        # spec.parameters + status only
```

## 2. Compositions

- `apiVersion: apiextensions.crossplane.io/v1` — **Composition stays v1 in
  Crossplane v2**; only the XRD moved to `/v2`. There is no `Composition` in the
  `/v2` group. `spec.mode: Pipeline` (the only mode in v2 — native patch &
  transform is removed).
- `compositeTypeRef.apiVersion: platform.adhar.io/v1alpha1`, `kind: Composite<Thing>`.
- Label every composition for selector-based dispatch:
  `feature: <domain>` and `provider: <aws|gcp|azure|kubernetes|...>`.
- A pipeline ends with `function-auto-ready` unless it computes the XR status
  (and thus readiness) itself in KCL.

### Composing resources — two idioms

1. **Native Kubernetes objects** (preferred for in-cluster resources): emit the
   object directly (e.g. `apiVersion: v1, kind: Service`). Crossplane v2 manages
   native objects directly. Omit `metadata.namespace` — Crossplane applies the
   XR's namespace automatically.
2. **Managed resources (MRs)**: cloud resources, or objects on remote clusters.
   Use the **namespaced** MR APIs (see §3) and reference a `ClusterProviderConfig`.

## 3. Namespaced managed-resource API groups

The namespaced variant of any MR group adds **`.m`** to the domain, and
namespaced APIs reset to **`v1beta1`** (except provider-kubernetes `Object`,
which is `v1alpha1`). Reference a cluster-scoped config via
`providerConfigRef: { kind: ClusterProviderConfig, name: <name> }`.

| Purpose | Cluster-scoped (v1, legacy) | Namespaced (v2, use this) |
|---|---|---|
| K8s object | `kubernetes.crossplane.io/v1alpha2` | `kubernetes.m.crossplane.io/v1alpha1` `Object` |
| Helm release | `helm.crossplane.io/v1beta1` | `helm.m.crossplane.io/v1beta1` `Release` |
| AWS (any svc) | `<svc>.aws.upbound.io/v1betaN` | `<svc>.aws.m.upbound.io/v1beta1` |
| Azure (any svc) | `<svc>.azure.upbound.io/v1betaN` | `<svc>.azure.m.upbound.io/v1beta1` |
| GCP (any svc) | `<svc>.gcp.upbound.io/v1betaN` | `<svc>.gcp.m.upbound.io/v1beta1` |

ProviderConfig names used by compositions:
- AWS / Azure / GCP families → `ClusterProviderConfig` named **`default`**.
- provider-kubernetes → `ClusterProviderConfig` named **`kubernetes-provider`**.
- provider-helm → `ClusterProviderConfig` named **`helm-provider`**.

DigitalOcean and Civo community providers (v0.x) have **no** namespaced MRs yet,
so their compositions remain legacy cluster-scoped and reference the plain
`ProviderConfig` (`digitalocean` / `civo`).

### KCL providerConfigRef pattern

```kcl
spec.providerConfigRef = {name = "default", kind = "ClusterProviderConfig"}
```

### Go-templating providerConfigRef pattern

```yaml
spec:
  providerConfigRef:
    name: default
    kind: ClusterProviderConfig
```

## 4. Functions

Installed via `configuration/functions/functions.yaml` and declared in
`crossplane.yaml`. Reference by `functionRef.name`:

| name | package | use |
|---|---|---|
| `function-kcl` | function-kcl | primary resource generation |
| `function-go-templating` | function-go-templating | template generation |
| `function-patch-and-transform` | function-patch-and-transform | declarative P&T |
| `function-auto-ready` | function-auto-ready | derive XR Ready |
| `function-python` | function-python | Operations logic |

> Use the canonical name `function-go-templating` (not the legacy `fn-go-templating`).

## 5. Operations (day-2)

Crossplane v2 adds `Operation`, `CronOperation`, and `WatchOperation`
(`ops.crossplane.io/v1alpha1`). Adhar uses them for scheduled/triggered day-2
tasks (backups, secret rotation, drift remediation) that don't fit the
"reconcile a desired-state XR" model. Operation pipelines reuse the same
functions. Operations are an alpha feature and require the core flag
`--enable-operations` (set in `hack/crossplane/values*.yaml`).

## 6. Layout

```
configuration/
  crossplane.yaml         # Configuration package meta + dependsOn
  xrd/                    # CompositeResourceDefinitions (apiextensions/v2, Namespaced)
  compositions/<domain>/  # Pipeline compositions, one file per provider/impl
  functions/functions.yaml# Function package installs
  providers/              # ClusterProviderConfigs + credential templates
  operations/             # Operation / CronOperation / WatchOperation
```
