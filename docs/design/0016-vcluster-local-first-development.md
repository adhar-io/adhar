# Low-Level Design — vCluster for local-first development and tenancy

Detailed design for [ADR-0016](../adr/0016-vcluster-local-first-development.md).

## 0. Context recap

ADR-0016 makes **vCluster the platform's virtual-cluster primitive**: a certified virtual
cluster (its own API server, controller-manager, CoreDNS, and datastore run as pods in a host
namespace) whose workloads sync down to the host and share its nodes, Cilium CNI, storage, and
images. It gives a developer a clean, disposable cluster in seconds for the things a namespace
cannot isolate — CRDs, admission webhooks, cluster-scoped RBAC, and API-server version — while
reusing the host's capacity. It is the isolation boundary for *Kubernetes itself* (namespaces
isolate workloads; vclusters isolate the cluster), the **local data plane** of ADR-0023, and the
laptop-weight backing for a `CompositeCluster` "give me a cluster" claim (ADR-0005 / ADR-0007).
This doc documents the shipped `core/vcluster` package as built and how it wires into the rest of
the platform.

## 1. Package layout

The package is a standard Adhar GitOps package under `platform/stack/packages/core/vcluster/`:

| File | Responsibility |
|---|---|
| `values.yaml` | The single Helm value override the platform sets (ingress off). |
| `generate-manifests.sh` | `helm template` the upstream chart → `manifests/install.yaml`. |
| `manifests/install.yaml` | Auto-generated, GitOps-applied render of the vcluster control plane. |
| `.gitkeep` / `manifests/.gitkeep` | Keep the (otherwise generated) tree in git. |

`generate-manifests.sh` pins the chart and renders it into the shared `adhar-system` namespace:

```bash
CHART_VERSION="0.34.1"
helm repo add loft https://charts.loft.sh --force-update
helm repo update loft
helm template --namespace adhar-system vcluster loft/vcluster \
  -f values.yaml --version ${CHART_VERSION} --include-crds >> manifests/install.yaml
```

This follows the platform's embedded/rendered-manifest convention (ADR-0006 for bootstrap
components; the GitOps packages render statically so ArgoCD applies plain YAML, not a Helm hook
chain). To bump vCluster, re-run the script and commit the regenerated `install.yaml`.

`values.yaml` is deliberately minimal — everything else is the chart default:

```yaml
# vCluster control plane. Ingress is disabled; external access (if needed) is
# routed through the Cilium Gateway API, never nginx.
controlPlane:
  ingress:
    enabled: false
```

The rendered `ingress` block confirms it (`enabled: false`, with the chart's stray nginx
`ssl-passthrough` annotations left inert). External reach, when a vcluster needs it, is an
`HTTPRoute` on the Cilium Gateway (ADR-0002) — consistent with every other platform service; the
chart's own ingress path is never used.

## 2. Rendered control plane (`manifests/install.yaml`)

`helm template` produces a self-contained control plane in `adhar-system`:

- **ServiceAccounts** `vc-vcluster` (the syncer identity) and `vc-workload-vcluster` (identity
  stamped onto synced workload pods on the host).
- **`vc-config-vcluster` Secret** — the full `vcluster.yaml` config (base64), mounted at
  `/var/lib/vcluster`; its hash is pinned into the StatefulSet pod annotation
  `vClusterConfigHash` so a config change rolls the pod.
- **RBAC** (§4).
- **Services** `vcluster` (ClusterIP: `443→8443` https, `10250→8443` kubelet) and
  `vcluster-headless` (`publishNotReadyAddresses: true`, for the StatefulSet).
- **StatefulSet `vcluster`** (§3).

### 2.1 Effective vcluster config (decoded from `vc-config-vcluster`)

| Area | Setting | Effect |
|---|---|---|
| Distro | `distro.k8s` image `ghcr.io/loft-sh/kubernetes:v1.35.0` | API server / controller-manager run **embedded in the syncer** (real upstream k8s, v1.35.0). |
| Backing store | `backingStore.database.embedded`, `.etcd.deploy`, `.external` all **false** | Default embedded datastore, persisted to the `data` PVC (§3) — SQLite/embedded, no separate etcd pod. |
| CoreDNS | `coredns.enabled: true`, `embedded: false` | The vcluster runs its **own** CoreDNS deployment (`k8s-app: vcluster-kube-dns`). |
| Networking | `podCIDR: 10.244.0.0/16`, `clusterDomain: cluster.local`, `proxyKubelets.byHostname/byIP: true` | Virtual pod network; kubelet calls (logs/exec) proxied through the syncer. |
| Proxy | `controlPlane.proxy.port: 8443`, `bindAddress: 0.0.0.0` | API server exposed on 8443 inside the pod, fronted by the `vcluster` Service. |
| Service | `controlPlane.service.spec.type: ClusterIP` | No NodePort/LB; reachable in-cluster and via Gateway only. |
| Integrations | cert-manager / external-secrets / istio / kubeVirt / metrics-server all **false** | Off by default; enable per vcluster when a workload needs host↔vcluster sync. |
| Policies | `limitRange: auto`, `resourceQuota: auto`, `networkPolicy: false` | Auto LimitRange/ResourceQuota inside the vcluster; NetworkPolicy off. |
| Telemetry | `telemetry.enabled: true` | Loft anonymous telemetry (chart default). |

### 2.2 Resource-sync directions (`sync`)

The syncer projects a subset of resources between the virtual and host clusters — this is the
"shared nodes, own control plane" mechanic and the source of the ADR's synced-resource caveats:

- **`toHost` (virtual → host, enabled):** `pods`, `services`, `endpoints`/`endpointSlices`,
  `configMaps`, `secrets`, `persistentVolumeClaims`. `pods.rewriteHosts.enabled: true` (an alpine
  init container rewrites `/etc/hosts` on synced pods). **Disabled:** `ingresses`, `namespaces`,
  `networkPolicies`, `persistentVolumes`, `priorityClasses`, `serviceAccounts`, `storageClasses`.
- **`fromHost` (host → virtual):** `events: true`, `csiDrivers/csiNodes/csiStorageCapacities:
  auto`, `storageClasses: auto`. **`nodes.enabled: false`** — the vcluster sees virtual (fake)
  nodes, not the host's, keeping the node view isolated while pods still schedule onto real host
  nodes.

Because pod names are rewritten on the host and some `*.enabled: false` resources are not
projected, packages must be *tested* in a vcluster before being certified "runs in vcluster"
(ADR-0016 consequence), not assumed.

## 3. StatefulSet — the control-plane pod

`StatefulSet/vcluster` (`replicas: 1`, `serviceName: vcluster-headless`,
`podManagementPolicy: Parallel`, `serviceAccountName: vc-vcluster`):

- **initContainer `kubernetes`** (`ghcr.io/loft-sh/kubernetes:v1.35.0`) copies the k8s binaries
  into the shared `binaries` emptyDir — this is where the **virtual API-server version** comes
  from, independent of the host Kind node version (ADR-0016's version-skew testing story).
- **container `syncer`** (`ghcr.io/loft-sh/vcluster-pro:0.34.1`): serves the API on `:8443`
  (`/healthz` liveness, `/readyz` readiness + startup probe, HTTPS). Env includes `VCLUSTER_NAME`,
  `POD_NAME/IP`, `NODE_NAME/IP` (downward API). `securityContext: runAsUser/Group 0`,
  `allowPrivilegeEscalation: false`.
- **Storage:** `volumeClaimTemplates` `data` (`5Gi`, RWO) mounted at `/data`,
  `persistentVolumeClaimRetentionPolicy.whenDeleted: Retain` — the embedded datastore lives here;
  on the local Kind host this binds to `local-path`. Plus emptyDirs `binaries`, `certs`, `tmp`,
  `helm-cache`, and the `vcluster-config` secret mount.
- **Footprint (fits the 8-CPU local profile, ADR-0012):** `requests cpu:200m mem:256Mi`,
  `limits mem:4Gi ephemeral-storage:10Gi`. One helm-sized control plane, seconds to start —
  namespace-grade cost for cluster-grade isolation.

## 4. RBAC

Two tiers, because the syncer manages workloads in its own namespace but reads a few
cluster-scoped objects:

- **Namespaced `Role/RoleBinding vc-vcluster`** (in `adhar-system`): full CRUD on the resources
  it syncs to the host — `pods` (+`attach`/`exec`/`portforward`/`log`/`status`/
  `ephemeralcontainers`), `services`, `configmaps`, `secrets`, `persistentvolumeclaims`,
  `endpoints`, `endpointslices`, `events`, and `apps` (`deployments`/`replicasets`/
  `statefulsets`). This is the syncer's blast radius: **its own namespace only**.
- **`ClusterRole/ClusterRoleBinding vc-vcluster-v-vcluster`**: read `persistentvolumes`
  (get/list) and manage `snapshot.storage.k8s.io` `volumesnapshotclasses`/`volumesnapshotcontents`
  — the minimum cluster scope for CSI/PV awareness. No broad cluster write; the vcluster is an
  *operational* isolation tool, **not** a hostile-tenant security boundary (ADR-0016 ⚠️).

## 5. ApplicationSet wiring & enablement

vcluster is a **wired but `enabled`-gated** package in the local/production generators and
**enabled** in development — matching the ADR ("shipped in `core/vcluster`; Composition wiring
lands with Roadmap Phase 2"):

| Generator element source | `enabled` | `manifestPath` |
|---|---|---|
| `platform/stack/adhar-appset-local.yaml` (L212) | `"false"` | `core/vcluster/manifests` |
| `platform/stack/environments/local/config.yaml` (L198) | `"false"` | `core/vcluster/manifests` |
| `platform/stack/environments/production/config.yaml` (L198) | `"false"` | `core/vcluster/manifests` |
| `platform/stack/environments/development/config.yaml` (L14) | `true` | `core/vcluster` |

All target `namespace: adhar-system`, `category: core`. Flipping `enabled: "true"` on the element
(a Gitea commit; kubectl edits are reverted by ArgoCD selfHeal) is what turns the package on — the
standard package-lifecycle path (ADR-0014). The ApplicationSet `selector` on `enabled: "true"`
keeps it out of the curated local core until opted in.

## 6. Relationship to ADR-0023 (control/data-plane separation)

vcluster is the **T1 (local) data plane** in the fleet design. In [ADR-0023](../adr/0023-control-dataplane-separation.md)
/ [design 0023](0023-control-dataplane-separation.md):

- The `DataPlaneReconciler`'s `ensureInfra` phase, for `spec.infrastructure.mode: vcluster`
  (`InfraModeVCluster`), helm-renders this same vcluster chart into a data-plane namespace on the
  control plane, waits for the StatefulSet, and reads its kubeconfig secret to build a client
  (design 0023 §2.2, §7).
- The host stays the **control plane** (platform services in `adhar-system`); the vcluster is the
  **data plane** where application packages actually run — so "apps run on a data plane" holds
  even on a laptop, and the T3 registration/placement/agent code paths are exercised at T1
  (ADR-0023 decision point 5, parity). `adhar up --single` collapses the roles for constrained
  machines (design 0023 §7.5).
- The vcluster is registered as an **ArgoCD destination cluster** (`ensureArgoRegistration`) with
  placement labels; the workload ApplicationSet places app packages onto it while the control
  appset keeps platform services on the host.

## 7. Relationship to ADR-0005 / ADR-0007 (one API across sizes)

The vcluster is the ephemeral/local realization of the durable `CompositeCluster` abstraction.
`platform/controlplane/configuration/xrd/cluster.xrd.yaml` defines
`compositeclusters.platform.adhar.io` (`kind: CompositeCluster`, `scope: Namespaced`, `v1alpha1`).
Per ADR-0016, `CompositeCluster` gains a vcluster-backed Composition so the same claim shape
resolves to a **vcluster** locally / for ephemeral needs and to **EKS/AKS/GKE** for durable
workload clusters — provider-appropriate weight on ADR-0007's declarative path. That Composition
is the "Composition wiring" the ADR defers to Roadmap Phase 2; the *package* (this control-plane
chart) is what ships today.

## 8. Adjacent primitives (why vcluster, not the others)

- **`core/Kamaji`** (package present) — hosted control planes as pods, but each still needs
  *worker nodes* joined; right for multi-tenant production node pools, wrong for laptop-scale
  ephemeral clusters. vcluster shares the host's nodes, so it has none to join.
- **Namespaces** (ADR-0011 shared `adhar-system`) — isolate workloads, not CRDs/webhooks/API
  version. Anything installing cluster-scoped machinery belongs in a vcluster.

## 9. Failure modes & day-2

- **Datastore is state.** The embedded datastore lives on the `5Gi data` PVC (`Retain` on delete).
  Ephemeral vclusters should be **rebuilt, not backed up**; a durable one must declare a real
  datastore/backup story (ADR-0021). Deleting the StatefulSet keeps the PVC (retention policy) —
  a stale PVC can shadow a fresh install; delete it explicitly to truly reset.
- **Version skew:** the virtual API server is v1.35.0 (initContainer image) regardless of the host
  Kind version — intended, and the supported way to test workloads against an upcoming k8s minor.
- **Sync edges:** disabled `toHost` resources (ingresses, namespaces, networkPolicies, PVs) and
  host-visible pod-name rewriting are the known fidelity gaps; certify packages *in* a vcluster.
- **Not a hard security boundary:** shared kernel/nodes — hostile-tenant isolation needs Kamaji or
  separate clusters/node pools.

## 10. Testing

- **Parity (`platform/controllers/adharplatform/parity_test.go`):** enforces appset ↔ environment
  generator parity, so the `vcluster` element (path/namespace/category/`enabled`) must stay
  identical across `adhar-appset-local.yaml` and `environments/*/config.yaml` — the drift that
  test guards is exactly the four-row table in §5.
- **Regeneration check (manual/CI):** re-running `generate-manifests.sh` with the pinned
  `CHART_VERSION=0.34.1` must reproduce `manifests/install.yaml` byte-for-byte (modulo the config
  hash) — the guard against hand-edited renders.
- **To add (with ADR-0023 M3):** an e2e assertion in `tests/e2e/bootstrap` that a sample app's
  pods run inside the vcluster (data plane) and **not** directly in `adhar-system`, and that the
  vcluster StatefulSet reaches Ready — see design 0023 §10 (e2e T1).

## 11. Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/core/vcluster/values.yaml` | Sole override: `controlPlane.ingress.enabled: false`. |
| `platform/stack/packages/core/vcluster/generate-manifests.sh` | Render `loft/vcluster` `0.34.1` → `manifests/install.yaml`. |
| `platform/stack/packages/core/vcluster/manifests/install.yaml` | Rendered control plane (SA/RBAC/Services/StatefulSet/config secret). |
| `platform/stack/adhar-appset-local.yaml` (≈L212) | ApplicationSet generator element (`enabled: "false"`). |
| `platform/stack/environments/{local,production}/config.yaml` (≈L198) | Env generator elements (`enabled: "false"`). |
| `platform/stack/environments/development/config.yaml` (≈L14) | Dev generator element (`enabled: true`). |
| `platform/controlplane/configuration/xrd/cluster.xrd.yaml` | `CompositeCluster` XRD the vcluster Composition will back (ADR-0005). |
| `platform/controllers/dataplane/infra.go` (planned, design 0023) | `ensureInfra` `mode: vcluster` — renders this chart as the T1 data plane. |
| `platform/stack/packages/core/Kamaji/` | Hosted-control-plane alternative for node-pool multi-tenancy. |
</content>
</invoke>
