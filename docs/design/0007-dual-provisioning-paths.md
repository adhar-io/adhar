# Low-Level Design — Dual provisioning paths (imperative provider interface + declarative Crossplane)

Detailed design for [ADR-0007](../adr/0007-dual-provisioning-paths.md). This is the authoritative
as-built design for how Adhar provisions infrastructure through **two deliberate paths**: the Go
`Provider` interface (`platform/providers/`) used to create and operate the management cluster from the
CLI, and the Crossplane control plane (ADR-0005, [design 0005](0005-crossplane-v2-namespaced.md)) used
to manage everything after the management cluster exists. The two meet at a single hand-off point in
`cmd/up/bootstrap.go`.

## 0. Context recap

Adhar provisions in two irreconcilable situations: **day-0** (no cluster exists — something outside
Kubernetes must create the management cluster; a chicken-and-egg no in-cluster controller can solve)
and **day-1+** (the management cluster exists and should manage all further infrastructure
declaratively with reconciliation/drift correction). ADR-0007 keeps **two paths with a defined
hand-off**: the imperative `Provider` interface owns bootstrap + CLI-driven day-2 cluster ops; the
declarative Crossplane control plane owns steady-state workload clusters, databases, networks. The
hand-off rule: *the imperative path's job ends when the management cluster is bootstrapped; anything
that should stay managed belongs to Crossplane.*

## 1. The imperative path — the `Provider` interface

`platform/providers/interface.go` defines a single broad interface every cloud implements. It is
deliberately wide (far beyond "create a cluster") so the CLI is useful standalone without a running
platform:

```go
type Provider interface {
    Name() string
    Region() string
    Authenticate(ctx, *types.Credentials) error
    ValidatePermissions(ctx) error
    // Cluster: Create/Delete/Update/Get/List + GetKubeconfig
    // Node groups: Add/Remove/Scale/Get/List
    // Infra: CreateVPC/CreateLoadBalancer/CreateStorage (+Delete/Get)
    // Lifecycle: UpgradeCluster/BackupCluster/RestoreCluster
    // Health/metrics, Addons, Cost, InvestigateCluster
}
```

The provider-agnostic request/response types (`ClusterSpec`, `Cluster`, `NodeGroupSpec`,
`ControlPlaneSpec`, `NetworkingSpec`, `DomainConfig`, `Credentials`, …) live in
`platform/types/cluster.go`. `ClusterSpec` carries `Provider`, `Region`, `Version`, `ControlPlane`,
`NodeGroups`, `Networking`, `Security`, `Addons`, `Domain`, `Tags`.

### Seven implementations, self-registering

`platform/providers/{aws,azure,gcp,digitalocean,civo,kind,custom}/`. Each `init()` registers a
constructor with the global `DefaultFactory` (`platform/providers/factory.go`), keyed by lowercased
type — this indirection avoids an import cycle between `provider` and the impls:

```go
// platform/providers/kind/provider.go
func init() {
    provider.DefaultFactory.RegisterProvider("kind", func(config map[string]interface{}) (provider.Provider, error) { … })
}
```

Registration only happens for packages that are blank-imported. `cmd/up/production.go` pulls in all
seven (`_ "adhar-io/adhar/platform/providers/aws"` …) so the factory is fully populated before
provisioning. `Factory.CreateProvider(type, config)` looks up the constructor;
`SupportedProviders()`/`IsSupported()`/`GetProviderInfo()` back the `adhar provider list|info` commands
(`platform/providers/provider.go`). `GetProviderInfo` is a static capability catalog (per-cloud
`Capabilities`, `RequiredCredentials`, `SupportedRegions`, `CostModel`) — the parity/capability doc
ADR-0007 asks for, in code.

### Compute mode vs managed K8s (ADR-0022)

The default provisioning strategy across all real clouds is **raw compute + kubeadm**, not the managed
Kubernetes service. Shared machinery lives in `platform/providers/kubeadm.go`: `KubeadmNodePrepScript`
(containerd + `pkgs.k8s.io` kubelet/kubeadm/kubectl, swap off, no CNI, kube-proxy skipped),
`EnsureClusterSSHKey`/`SSHRun`, `WaitForNodePrep`, `KubeadmInitMaster`, `KubeadmJoinWorker`,
`FetchAdminKubeconfig`, `KubeadmUpgradeCluster`. AWS's `provider_cluster.go` drives exactly this:
provision EC2 → `WaitForNodePrep` → `KubeadmInitMaster` → `KubeadmJoinWorker`, yielding a cluster whose
nodes stay `NotReady` until the platform bootstrap installs Cilium (identical to the Kind flow;
`KubeadmPodCIDR = 10.244.0.0/16` matches Cilium's expectation).

The `useManagedK8s` opt-in flips a provider into its managed service (`platform/config/config.go`
`UseManagedK8s`, surfaced through `ToProviderMap`):

| Provider | `useManagedK8s: true` | Notes |
|---|---|---|
| DigitalOcean | `ClusterMode = "doks"` | `digitalocean/provider.go` |
| Civo | `ClusterMode = "k3s"` | `civo/provider.go` |
| Azure | AKS branch | `azure/provider.go` |
| AWS | **rejected** | `init()` errors: "EKS integration is not offered … provisions Kubernetes on raw compute here" |
| custom | **rejected** | "bring-your-own hosts are always self-managed" |

## 2. The CLI entry points (day-0 and day-2)

**Day-0 / bootstrap** — two front doors, both ending at the same controller-driven bootstrap:

- **Local**: `adhar up` → `cmd/up/local.go` `LocalProvisioner` creates the Kind cluster directly
  (Cilium/kube-proxy disabled in `kind.yaml.tmpl`), installs platform CRDs, starts the controller
  manager, and creates the `AdharPlatform` CR.
- **Production/cloud**: `adhar up --config …` → `cmd/up/production.go` builds a `ProviderManager`
  (`NewProviderManager(DefaultFactory)`) and calls `ProvisionEnvironment` /
  `provisionCompletePlatformNew`.

`ProviderManager.ProvisionEnvironment` (`platform/providers/provider.go`) is the imperative
orchestration core: resolve provider type → `buildProviderConfig` → `factory.CreateProvider` →
(dry-run short-circuit) → `buildClusterSpec` → `Authenticate` → `ValidatePermissions` → `CreateCluster`,
returning a `ProvisionResult{Provider, Cluster}`.

`buildClusterSpec(envConfig)` (same file) is where environment config becomes a `ClusterSpec`:
`ControlPlane.Replicas = 1` (the kubeadm providers bootstrap a single control plane and reject >1 — HA
control planes are roadmap P1); `workerReplicas` defaults to 0 locally / 3 for production;
`Networking = {CNI: cilium, PodCIDR: 10.244.0.0/16, ServiceCIDR: 10.96.0.0/12}`; then per-key overrides
from `ResolvedClusterConfig` (`kubeVersion`/`version`, `controlPlaneReplicas`, `workerReplicas`,
`nodeInstanceType`). `buildDomainConfig` and `buildCredentials` complete the spec (credentials here are
placeholder-level — real creds flow through the provider config map / env / secret stores).

**Day-2** — `cmd/cluster/*` (`create`, `list`, `status`, `scale`, `upgrade`, `delete`, `kubeconfig`,
`debug`, `investigate`) all resolve a provider via `DefaultFactory.CreateProvider(name,
providerCfg.ToProviderMap())` and call interface methods directly (`ScaleNodeGroup`, `UpgradeCluster`,
`GetClusterHealth`, `InvestigateCluster`, …). This is the "CLI useful standalone" property: create /
inspect / scale / destroy clusters with no platform running.

## 3. The hand-off — where imperative stops and declarative begins

`cmd/up/bootstrap.go` `bootstrapPlatformOnCluster(ctx, result, envConfig, cfg)` is the single seam. It
takes the `ProvisionResult` from the imperative path and runs the **same** controller-driven bootstrap
used for local Kind:

1. `result.Provider.GetKubeconfig(ctx, result.Cluster.ID)` → write to a temp file → set `KUBECONFIG`
   (the controller's GitOps seeding shells out to `kubectl`, so the env must point at the new cluster).
2. Build a REST config + controller-runtime client (`k8s.GetScheme()`).
3. Install platform CRDs + TLS secret, then create an `AdharPlatform` CR.

Creating that CR is the hand-off: the `AdharPlatform` controller
(`platform/controllers/adharplatform/`) drives **Cilium → Gateway → ArgoCD → Gitea → Crossplane** and
seeds the GitOps repos, then the in-cluster manager keeps reconciling after the CLI exits. From this
point **the imperative path is done** — nothing it created is continuously reconciled by it; the
management cluster and everything downstream is owned declaratively.

```
adhar up --config                cmd/cluster/*  (day-2, standalone)
   │                                   │
   ▼  ProviderManager                  ▼  DefaultFactory.CreateProvider
CreateCluster (EC2+kubeadm / DOKS…)   Scale/Upgrade/Delete/Investigate
   │  ProvisionResult{Provider,Cluster}
   ▼
bootstrapPlatformOnCluster ── GetKubeconfig ──▶ create AdharPlatform CR   ◀── HAND-OFF
                                                     │
                    Cilium → Gateway → ArgoCD → Gitea → Crossplane  (in-cluster, reconciled)
                                                     │
                                            ┌────────┴─────────┐
                                    ApplicationSet stack   Crossplane control plane
                                    (69 packages)          (23 XRDs / 34 Compositions)
                                                     │
                              Developer applies CompositeCluster / CompositeDatabase …
                                    (declarative, drift-corrected — ADR-0005)
```

## 4. The declarative path (steady state)

Everything after the hand-off is the Crossplane control plane — fully covered in
[design 0005](0005-crossplane-v2-namespaced.md). What matters for ADR-0007 is the **scope split and
the parity contract**:

- Workload clusters are a namespaced **`CompositeCluster`** XR
  (`platform/controlplane/configuration/xrd/cluster.xrd.yaml`), implemented by one Composition per
  cloud in `configuration/compositions/cluster/`: `aws-eks.yaml`, `azure-aks.yaml`, `gcp-gke.yaml`,
  `digitalocean-doks.yaml`, `civo-k3s.yaml`, `kind-kubernetes.yaml`. A developer requesting a cluster
  applies a namespaced XR; Crossplane reconciles and corrects drift — exactly what the imperative path
  cannot do.
- Databases/networks/storage/etc. are likewise XRs (`CompositeDatabase`, `CompositeNetwork`, …), never
  the imperative interface. The imperative `CreateVPC`/`CreateStorage`/`CreateLoadBalancer` methods
  exist to support bootstrap and standalone CLI use, **not** as the steady-state API.

**Parity contract (ADR-0007 review checklist).** A new provider must land in *both* places: a
`Provider` implementation (interface + factory registration) **and** a `compositions/cluster/<cloud>`
Composition (plus a `ClusterProviderConfig`). The `GetProviderInfo` capability table and this design
doc are where per-provider capability gaps between the two paths get recorded.

## 5. Ordering, idempotency, failure modes

- **Ordering** is enforced by the hand-off: imperative `CreateCluster` must fully succeed (API server
  answering) before `bootstrapPlatformOnCluster` runs; the `AdharPlatform` controller must finish the
  foundation install before any `CompositeCluster` can be applied (Crossplane is the *last* foundation
  component).
- **Idempotency**: imperative cluster state is tracked per-provider (e.g. Kind persists a cluster
  registry; kubeadm providers keep SSH keys/state via `ClusterStateDir`/`RemoveClusterState`).
  Re-running the declarative path is naturally idempotent (SSA + reconciliation).
- **Failure modes**: a failed `CreateCluster` returns before the hand-off, so no half-bootstrapped
  platform. A failed foundation install leaves `AdharPlatform` un-`Ready` and the CLI does not report
  success (see design 0005 §4 — `ControlPlaneApplied` gate). Cloud provisioning failures clean up
  partial infra (AWS `cleanupPartialInfrastructure`).
- **Shared credentials**: both paths consume the same provider credential material (config map / env /
  secret store); ADR-0007's convergence target is workload identity (IRSA / GCP WI / Azure MI), already
  the production default on the Crossplane side (design 0005 §3).

## Testing

- **Unit**: `platform/providers/digitalocean/compute_test.go` covers the DO compute-mode path;
  `kubeadm.go` helpers (`K8sMinorFromVersion`, script/version derivation) are exercised via provider
  tests. Factory registration is validated implicitly by the blank-import set in `cmd/up/production.go`
  compiling and `SupportedProviders()` returning all seven.
- **e2e** (`make e2e`, `tests/e2e/bootstrap`): the local branch runs the full imperative-Kind →
  hand-off → controller bootstrap → GitOps sync cycle. Cloud provisioning + hand-off is validated
  against live clusters (see MEMORY "DO live verification").
- **Parity/declarative**: the Crossplane side is covered by design 0005's tests (`kubectl get
  compositions` shows the six `cluster/*`); `platform/controllers/adharplatform/parity_test.go` guards
  the ApplicationSet package parity.
- **Tests to add**: a lint check that every `Provider` factory key has a matching
  `compositions/cluster/<cloud>.yaml`, and that `GetProviderInfo` lists a provider iff it's registered.

## Code & file map

| Path | Responsibility |
|---|---|
| `platform/providers/interface.go` | `Provider` + `ProviderFactory` interfaces, `ProviderConfig`/`ProviderStatus` |
| `platform/providers/factory.go` | `Factory`, `DefaultFactory`, `CreateProvider`/`RegisterProvider`, static `GetProviderInfo` catalog |
| `platform/providers/provider.go` | `adhar provider` CLI; `ProviderManager.ProvisionEnvironment`, `buildProviderConfig`/`buildClusterSpec`/`buildDomainConfig`/`buildCredentials` |
| `platform/providers/kubeadm.go` | compute-mode shared machinery (node-prep script, SSH, kubeadm init/join/upgrade, state dir) |
| `platform/providers/{aws,azure,gcp,digitalocean,civo,kind,custom}/` | seven impls; each `init()` self-registers; `useManagedK8s` opt-in / rejection |
| `platform/types/cluster.go` | provider-agnostic `ClusterSpec`/`Cluster`/`NodeGroupSpec`/… |
| `cmd/up/production.go` | cloud/production `adhar up`: `ProviderManager`, `provisionCompletePlatformNew`, blank-imports all providers |
| `cmd/up/local.go` | local `adhar up`: `LocalProvisioner` (Kind) + in-process controller manager |
| `cmd/up/bootstrap.go` | `bootstrapPlatformOnCluster` — **the hand-off** (kubeconfig → CRDs/TLS → `AdharPlatform` CR) |
| `cmd/cluster/*.go` | day-2 imperative CLI (create/list/status/scale/upgrade/delete/kubeconfig/debug/investigate) |
| `platform/config/config.go` | `UseManagedK8s` + provider config → `ToProviderMap` |
| `platform/controlplane/configuration/xrd/cluster.xrd.yaml` | declarative `CompositeCluster` API |
| `platform/controlplane/configuration/compositions/cluster/*.yaml` | six per-cloud cluster Compositions (declarative path) |
| `platform/controllers/adharplatform/` | consumes the hand-off's `AdharPlatform` CR; drives foundation + installs Crossplane |

## Drift & notes (as-built vs. ADR)

- **The two paths use *different* provisioning strategies per cloud, not the same one.** The imperative
  path defaults to **raw compute + kubeadm** (ADR-0022) and treats AWS EKS as explicitly unsupported.
  The declarative `compositions/cluster/` implementations are all **managed services**
  (`aws-eks.yaml`, `azure-aks.yaml`, `gcp-gke.yaml`, `digitalocean-doks.yaml`, `civo-k3s.yaml`). So a
  cluster created by `adhar up` on AWS (EC2+kubeadm) is *not* reproducible via `CompositeCluster` on
  AWS (EKS) — the "parity" ADR-0007 asks for is API-surface parity (both paths know each cloud), not
  provisioning-strategy parity. Worth documenting per-provider as ADR-0007's "capability gaps" note
  anticipates.
- **`buildCredentials` is a placeholder.** `platform/providers/provider.go` fills AWS/GCP/Azure creds
  with literal `"placeholder"` values; real credential resolution happens in each provider's config
  parsing (env vars / config file / secret stores), not here. The ADR's "share credentials" claim is
  true at the config-map level, but the `ProviderManager` credential builder itself is a stub.
- **Local `adhar up` bypasses `ProviderManager`.** The Kind path uses a dedicated `LocalProvisioner`
  (`cmd/up/local.go`) rather than `ProviderManager.ProvisionEnvironment`; only the cloud/production
  path (`cmd/up/production.go`) goes through the factory-driven provisioning core. Both converge on the
  same `AdharPlatform`-CR hand-off.
- **`adhar provider configure`/`test` are stubs** (`provider.go` prints "will be implemented in a
  future version" / a hard-coded success), so provider onboarding is still config-file driven.
</content>
</invoke>
