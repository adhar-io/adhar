# Low-Level Design — Custom clusters on raw cloud compute (no managed Kubernetes)

Detailed design for [ADR-0022](../adr/0022-custom-clusters-no-managed-k8s.md). This is the authoritative
as-built description of how Adhar builds a conformant Kubernetes cluster from raw cloud VMs — SSH-driven
**kubeadm** on containerd, kube-proxy skipped so the platform's Cilium install replaces it — across every
cloud provider, with managed Kubernetes reduced to an explicit `useManagedK8s` opt-in that only two
providers still honour. It maps the shared `kubeadm.go` machinery, the per-provider `compute.go` flows
(DigitalOcean as the reference), cloud integration (CCM/CSI), idempotent adoption, and the day-2
scale/upgrade/delete paths.

## 0. Context recap

Managed control planes (EKS/AKS/GKE/DOKS/Civo) cost money per cluster, differ per provider, and hide the
API-server flags the platform most needs — above all the OIDC issuer trust for the platform's own Keycloak
([ADR-0008](../adr/0008-keycloak-platform-identity.md)). ADR-0022 decides Adhar clusters are **custom
clusters built from cloud primitives** (VMs + network + LB) with a lightweight, upstream-conformant
Kubernetes, so every cluster — local Kind, cloud, on-prem — runs the same distribution and the platform
owns every flag. The as-built distribution is **kubeadm** (not the ADR's proposed k3s — see Drift), which
makes cloud clusters bit-for-bit identical to the local Kind flow: containerd + systemd cgroups, no CNI,
`--skip-phases=addon/kube-proxy`, and Cilium installed later by the platform bootstrap with
`kubeProxyReplacement`.

## 1. Invariants

- **INV-1** Raw compute is the **default** on every cloud; managed Kubernetes is opt-in via `useManagedK8s`
  and is only implemented for DigitalOcean (DOKS) and Civo (k3s). AWS/Azure/GCP reject the flag.
- **INV-2** A compute cluster is byte-compatible with the Kind flow: containerd + systemd cgroup driver,
  swap off, **kube-proxy skipped**, **no CNI preinstalled**. Nodes report `NotReady` until the platform
  bootstrap installs Cilium; the cluster is "created" the moment the API server answers.
- **INV-3** Every kubeadm step is **idempotent** and every VM is **adoptable** — an interrupted `adhar up`
  re-runs without duplicating infrastructure (guards keyed on `/etc/kubernetes/*.conf`, droplet/instance
  tags, and the cloud-init marker).
- **INV-4** The per-cluster SSH key is generated once and persisted under `~/.adhar/clusters/<name>/`; all
  kubeadm orchestration is SSH from the CLI host, never an in-cluster agent.
- **INV-5** The Kubernetes minor is pinned from `spec.Version` (falling back to `KubeadmDefaultK8sMinor`,
  currently `1.36`), and the same pin drives node prep, join, and upgrade.

## 2. Mode selection — the `useManagedK8s` opt-in

`config.yaml` → `providers.<name>.useManagedK8s` (bool, default false) is the single canonical switch.
It flows through `ConfigProviderConfig.UseManagedK8s` ([platform/config/config.go](../../platform/config/config.go))
→ `ToProviderMap()` ([platform/config/helpers.go](../../platform/config/helpers.go), emits
`result["useManagedK8s"]`) → each provider's `New<Provider>Provider(configMap)` constructor, which maps it
to a provider-native `ClusterMode`:

```go
// platform/providers/digitalocean/provider.go — NewDigitalOceanProvider
if managed, ok := configMap["useManagedK8s"].(bool); ok && managed {
    doConfig.ClusterMode = "doks"          // else "" == compute (raw droplets + kubeadm)
}
// platform/providers/civo/provider.go — analogous, managed → ClusterMode "k3s"
```

`CreateCluster` then dispatches on `ClusterMode`:

```go
// digitalocean/provider.go CreateCluster
switch strings.ToLower(p.config.ClusterMode) {
case "", "compute", "droplets", "self-managed":
    return p.createComputeCluster(ctx, spec)   // DEFAULT
case "doks", "managed":                          // opt-in DOKS path continues below
default:
    return nil, fmt.Errorf("unknown cluster_mode %q ...", p.config.ClusterMode)
}
```

| Provider | Default (compute) | `useManagedK8s: true` | Cloud integration (compute) |
|---|---|---|---|
| digitalocean | droplets + kubeadm (`compute.go`) | DOKS managed | **CCM + CSI installed** (§5) |
| civo | instances + kubeadm (`compute.go`) | managed **k3s** service | none automated |
| aws | EC2 + kubeadm (`provider_cluster.go`, `provider_compute.go`) | **rejected** — "EKS integration is not offered" | none automated (CSI via addon) |
| azure | VMs + kubeadm (`provider.go`) | **rejected** — "AKS integration is not offered" | none automated |
| gcp | GCE + kubeadm (`provider.go`) | **rejected** — "GKE integration is not offered" | none automated (CSI via addon) |
| custom | BYO hosts + kubeadm (`provider.go`) | **rejected** — "always self-managed" | none (bring your own) |
| kind (local) | Kind node, CNI/kube-proxy off | n/a | local-path storage |

The AWS/Azure/GCP/custom constructors reject a truthy `useManagedK8s` with an explicit error (e.g.
`aws/provider.go`: *"useManagedK8s is not supported for the aws provider: adhar provisions Kubernetes on
raw compute here (EKS integration is not offered)…"*), so those managed paths are not merely unused — they
are absent.

## 3. Shared kubeadm machinery (`platform/providers/kubeadm.go`)

One provider-agnostic file holds the "compute mode" toolkit; every cloud `compute.go` imports it as
`provider`. The pieces:

**Version pinning.** `K8sMinorFromVersion("v1.34.2") == "1.34"` (empty/garbage → `KubeadmDefaultK8sMinor`,
`"1.36"`). Selects the `pkgs.k8s.io/core:/stable:/v<minor>` apt stream.

**Node prep (user-data).** `KubeadmNodePrepScript(k8sMinor)` returns the bash the cloud runs as VM
user-data / startup script (or, for `custom`, uploaded base64 over SSH). It: loads `overlay`/`br_netfilter`,
sets the bridge-nf + `ip_forward` sysctls, `swapoff -a`, installs **containerd** with
`SystemdCgroup = true`, installs `kubelet/kubeadm/kubectl` from the pinned minor stream and `apt-mark hold`s
them, then `touch`es the completion marker `KubeadmCloudInitMarker` (`/var/lib/adhar/cloud-init-done`).
No CNI, no kube-proxy — deliberately identical to the Kind node.

**Per-cluster SSH key.** `EnsureClusterSSHKey(name)` loads-or-generates an ed25519 keypair under
`ClusterStateDir` (`~/.adhar/clusters/<name>/id_ed25519`, `0600`) and returns an `ssh.Signer` plus the
`authorized_keys`-format public key the provider registers on the VMs. `LoadClusterSSHKey` fails loudly if
the cluster was created from another machine; `RemoveClusterState` cleans up on delete.

**SSH exec.** `SSHRun(signer, user, ip, cmd, timeout)` dials `<ip>:22`, wraps in `sudo bash -c '…'` when the
user is not root, uses `InsecureIgnoreHostKey` (hosts are freshly minted by us), and enforces the timeout via
a goroutine + `select`. `WaitForNodePrep` polls `test -f <marker>` until the prep script has finished.

**Control-plane init** — `KubeadmInitMaster(signer, user, publicIP, privateIP)`:

```
test -f /etc/kubernetes/admin.conf || kubeadm init \
  --pod-network-cidr=10.244.0.0/16 \        # KubeadmPodCIDR — matches Cilium's expectation (== Kind)
  --skip-phases=addon/kube-proxy \          # Cilium's kubeProxyReplacement takes over
  --control-plane-endpoint=<publicIP> \
  --apiserver-cert-extra-sans=<publicIP>,<privateIP>
```

The `test -f … ||` guard makes re-init a no-op (INV-3). It then idempotently patches the static-pod
`kube-apiserver.yaml` to add `--kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname` (cloud node
hostnames are not DNS-resolvable by the API server, so `kubectl logs/exec` would break without it), and
returns a fresh `kubeadm token create --print-join-command`.

**Worker join** — `KubeadmJoinWorker` runs `test -f /etc/kubernetes/kubelet.conf || <joinCmd>` (idempotent).

**Kubeconfig** — `FetchAdminKubeconfig` cats `/etc/kubernetes/admin.conf` and rewrites
`https://127.0.0.1:6443` → `https://<masterIP>:6443` so the CLI/bootstrap can reach the API server from
outside.

**Upgrade** — `KubeadmUpgradeCluster(ctx, signer, user, masterIP, workerIPs, targetVersion)` switches the apt
stream to the target minor, then control-plane-first: `apt-get install kubeadm` → `kubeadm upgrade apply -y
v<target>` → `apt-get install kubelet kubectl` + `systemctl restart kubelet`; each worker then runs
`kubeadm upgrade node` + the kubelet/kubectl swap. `LastLines(out, n)` trims SSH output for error context.

## 4. Per-provider compute flow (DigitalOcean reference — `digitalocean/compute.go`)

`createComputeCluster(ctx, spec)` is the canonical shape; AWS/Azure/GCP/Civo mirror it with their own
network/instance primitives.

1. **Guard HA** — `spec.ControlPlane.Replicas > 1 || HighAvailability` → error ("compute mode currently
   supports a single control-plane node"). True on every provider today (stacked-etcd HA + LB is not built).
2. **Prep material** — `userData := KubeadmNodePrepScript(K8sMinorFromVersion(spec.Version))`; per-cluster
   tag `adhar-cluster-<name>` (`computeClusterTagPrefix`) plus role tags `adhar-role-master` /
   `adhar-role-worker`.
3. **Cloud fabric (idempotent, adopt-or-create)**:
   - `ensureSSHKey` — `EnsureClusterSSHKey` + register the pubkey with DO (`Keys.Create`, reused by name).
   - `ensureComputeVPC` — configured VPC or a per-cluster `adhar-<name>-vpc`; on CIDR overlap it walks
     alternative `/16`s rather than failing.
   - `ensureComputeFirewall` — creates the cluster tag first (the firewall references it as source/target),
     then opens `22`, `6443`, NodePort `30000-32767` (tcp+udp) from anywhere and **all** traffic between
     tagged members (etcd, kubelet, Cilium VXLAN/health), all egress.
4. **Instances** — control-plane droplet `adhar-<name>-master-1`, then workers from `spec.NodeGroups`
   (default pool = 2 workers when none declared). Droplets already present (matched by name) are **adopted**
   with a log line, not recreated (INV-3).
5. **Bootstrap over SSH** — `WaitForNodePrep` (15-min budget) → `enableExternalCloudProvider` (§5) →
   `KubeadmInitMaster` on the master; for each worker `WaitForNodePrep` → `enableExternalCloudProvider` →
   `KubeadmJoinWorker`.
6. **Cloud integration** — `installDOCloudIntegration` (§5).
7. **Return** a `types.Cluster{Status: Running, Endpoint: https://<masterIP>:6443,
   Metadata:{mode:"compute", …}}` and cache it. The final log reminds that **nodes stay NotReady until the
   platform bootstrap installs Cilium**.

Provider parity (all drive the same `kubeadm.go` helpers, differ only in SSH user and cloud SDK):

| Provider | SSH user | Instance/network primitives | Notes |
|---|---|---|---|
| digitalocean | `root` | godo Droplets / VPC / Firewall, tag-based discovery | reference impl, full CCM/CSI |
| aws | `ubuntu` | EC2 + VPC/subnets/SGs (`provider_cluster.go`, `nodeUserData` base64) | HA rejected; no auto CCM |
| azure | `azureadmin`-style (`azureSSHUser`) | VMs via OSProfile | no auto CCM |
| gcp | `gcpSSHUser` | GCE instances + startup-script metadata | no auto CCM |
| civo | (compute user) | civogo Instances / Network / Firewall | no auto CCM |
| custom | `p.config.SSHUser` | **none — user-supplied hosts**; script uploaded base64; delete = `kubeadm reset -f` | HA rejected; machines never deleted |

## 5. Cloud integration — CCM/CSI (DigitalOcean; the replacement the ADR calls for)

Managed Kubernetes bundled a cloud-controller-manager and CSI driver; a custom cluster must install them so
that (a) `Service type=LoadBalancer` — which the platform's **cloud Gateway** uses
(`resources/gateway/gateway-cloud.yaml`, `type: LoadBalancer`) — yields a real cloud LB, and (b) stateful
platform components get a default StorageClass. DigitalOcean is the only provider that automates this today.

**Kubelet external mode (before init/join).** `enableExternalCloudProvider(signer, ip, privateIP)` appends
`KUBELET_EXTRA_ARGS=--cloud-provider=external --node-ip=<privateIP>` to `/etc/default/kubelet` and restarts
it. `--node-ip` is essential: in external mode the kubelet registers **no** InternalIP until the CCM
initialises the node, which would deadlock scheduling (Cilium can't start without a node IP; the CCM can't
schedule until Cilium clears the `uninitialized` taint) and break `kubectl logs/exec`. Cilium's DaemonSet
tolerates the uninitialized taint, so the CNI still comes up first during platform bootstrap.

**In-cluster wiring.** `installDOCloudIntegration(signer, masterIP, vpcUUID)` runs, via
`kubectl --kubeconfig /etc/kubernetes/admin.conf` on the master (no local tooling needed), a fixed idempotent
step list: the `digitalocean` token Secret in `kube-system`; CCM `v0.1.62`; CSI `v4.14.0` CRDs + driver +
snapshot-controller; `set env … DO_CLUSTER_VPC_ID=<vpc>` (else the CCM builds LBs in the wrong VPC and
droplet targeting 422s); and patching `do-block-storage` to the default StorageClass.

For AWS/Azure/GCP/Civo compute clusters this integration is **not** installed automatically — a functional
gap for the cloud Gateway's LoadBalancer Service and for default storage on those clouds (AWS/GCP expose CSI
only through the on-demand `InstallAddon` path).

## 6. Idempotency, adoption & discovery

Compute clusters carry no server-side registry beyond cloud tags, so discovery and re-entrancy are tag- and
file-driven:

- **Adoption** — `computeClusterDroplets` (list by cluster tag) builds a name→droplet map; existing
  master/workers are reused. Combined with the `test -f /etc/kubernetes/*.conf` guards in kubeadm, a
  half-finished `adhar up` resumes cleanly.
- **Discovery** — `listComputeClusters` scans all droplets for the `adhar-cluster-` tag prefix and folds
  them into `types.Cluster`s; `isComputeCluster(id)` (tag-prefix or droplet lookup) is the branch every
  lifecycle method uses to choose the compute vs. managed path (e.g. `GetKubeconfig`, `DeleteCluster`,
  `UpgradeCluster`, `ScaleNodeGroup`, `GetClusterHealth`).
- **Kubeconfig** — `computeGetKubeconfig` finds the master IP by role tag, `LoadClusterSSHKey`s, and
  `FetchAdminKubeconfig`s.

## 7. Day-2 operations on compute clusters

- **Scale** — `ScaleNodeGroup` → `scaleComputeWorkers(ctx, id, desired)`: to grow, mint droplets, `WaitForNodePrep`,
  `enableExternalCloudProvider`, and `KubeadmJoinWorker` with a fresh token; to shrink, on the control plane
  `kubectl drain --ignore-daemonsets --delete-emptydir-data` + `delete node`, then delete the droplet
  (highest-indexed first).
- **Upgrade** — `UpgradeCluster` → `upgradeComputeCluster` → `KubeadmUpgradeCluster` (§3): control plane, then
  every worker, in place.
- **Delete** — `deleteComputeCluster`: `Droplets.DeleteByTag`, wait for droplets to vanish, then firewall,
  per-cluster VPC (retrying the 409 while DO still reports membership), cluster/role tags, the registered SSH
  key, and `RemoveClusterState`. The `custom` provider instead runs `kubeadm reset -f && rm -rf /etc/cni/net.d`
  on each host and never deletes the machines.

## 8. Integration with `adhar up` and the platform bootstrap

Day-0 uses the **Go provider interface**, not Crossplane compositions (the ADR's CompositeCluster swap is
future — see Drift). `createProductionCluster` → `ProviderManager.ProvisionEnvironment`
([platform/providers/provider.go](../../platform/providers/provider.go)) builds the provider from
`buildProviderConfig(envConfig)`, `Authenticate`s, `ValidatePermissions`, and calls `CreateCluster(spec)` —
returning a `ProvisionResult{Provider, Cluster}`. `bootstrapPlatformOnCluster`
([cmd/up/bootstrap.go](../../cmd/up/bootstrap.go)) then `GetKubeconfig`s the cluster, writes it to a temp
`KUBECONFIG`, and runs the identical two-phase bootstrap as local
([ADR-0001](../adr/0001-management-cluster-first.md)): `EnsureCRDs` → self-signed TLS → start the bootstrap
manager → create the `AdharPlatform` CR (`Provider` mapped by `providerNameToEnvironmentProvider`) →
`EnsureControllerManager`. The controller's foundation installer brings up Cilium with
`kubeProxyReplacement`, which is exactly why node prep skips kube-proxy and installs no CNI — the compute
cluster is deliberately incomplete until the platform completes it, making cloud and Kind converge on one
data path.

## Testing

- **Unit** — `platform/providers/digitalocean/compute_test.go`: `TestComputeK8sMinor` locks
  `K8sMinorFromVersion` across `""/1.31/1.31.4/v1.30.2/v1.29/junk`; `TestComputeClusterNameAndTag` covers the
  tag round-trip (`computeClusterName`/`computeClusterTag` stable when a tag is fed back in); `TestLastLines`
  covers the SSH error-context trimmer. These exercise the shared `kubeadm.go` helpers through the DO package.
- **Compile-time** — `var _ provider.Provider = (*Provider)(nil)` in each provider guarantees the full
  interface (including the compute lifecycle branches) is satisfied.
- **Tests to add** — a table test asserting each cloud constructor rejects `useManagedK8s: true` where
  unsupported (AWS/Azure/GCP/custom) and sets the right `ClusterMode` where supported (DO→doks, Civo→k3s); an
  envtest/fake-SSH harness around `KubeadmInitMaster`/`KubeadmJoinWorker` idempotency; a live e2e that
  provisions a single-provider compute cluster, bootstraps the platform, and asserts nodes go Ready once
  Cilium lands (mirrors `do-live-verification`).

## Code & file map

| Path | Responsibility |
|---|---|
| `platform/providers/kubeadm.go` | shared compute toolkit: `K8sMinorFromVersion`, `KubeadmNodePrepScript`, `EnsureClusterSSHKey`/`LoadClusterSSHKey`/`ClusterStateDir`/`RemoveClusterState`, `SSHRun`/`WaitForNodePrep`, `KubeadmInitMaster`/`KubeadmJoinWorker`/`FetchAdminKubeconfig`, `KubeadmUpgradeCluster`, `LastLines`; consts `KubeadmDefaultK8sMinor`, `KubeadmPodCIDR`, `KubeadmCloudInitMarker` |
| `platform/providers/digitalocean/compute.go` | reference compute flow: SSH key/VPC/firewall/droplets, `createComputeCluster`, adopt/discover, scale/upgrade/delete, `enableExternalCloudProvider`, `installDOCloudIntegration` (CCM v0.1.62 / CSI v4.14.0) |
| `platform/providers/digitalocean/provider.go` | `useManagedK8s → ClusterMode "doks"`, `CreateCluster` dispatch, DOKS managed path, lifecycle branching on `isComputeCluster` |
| `platform/providers/digitalocean/compute_test.go` | unit tests for the shared helpers |
| `platform/providers/civo/{compute.go,provider.go}` | Civo compute (kubeadm) + `useManagedK8s → "k3s"` managed opt-in |
| `platform/providers/aws/{provider_cluster.go,provider_compute.go}` | EC2 compute via kubeadm (`awsSSHUser="ubuntu"`, `nodeUserData` base64); EKS rejected |
| `platform/providers/azure/provider.go` | Azure VM compute via kubeadm; AKS rejected |
| `platform/providers/gcp/provider.go` | GCE compute via kubeadm; GKE rejected |
| `platform/providers/custom/provider.go` | bring-your-own hosts: `prepareHost` (uploaded prep script), kubeadm init/join, `kubeadm reset` teardown |
| `platform/providers/interface.go`, `factory.go`, `provider.go` | `Provider` interface, factory registration, `ProvisionEnvironment` day-0 entry |
| `platform/config/config.go`, `helpers.go` | `ConfigProviderConfig.UseManagedK8s`, `ToProviderMap` emitting `useManagedK8s` |
| `cmd/up/{production.go,bootstrap.go}` | production `adhar up -f`: provision → `GetKubeconfig` → platform bootstrap |
| `platform/controllers/adharplatform/resources/gateway/gateway-cloud.yaml` | LoadBalancer Gateway Service that depends on a CCM in compute mode |

## Drift & notes (as-built vs. ADR)

- **Distribution is kubeadm, not k3s.** ADR-0022's Decision names **k3s (server + agents)** as the
  distribution. The entire as-built compute path uses **kubeadm + containerd + Cilium** (chosen for
  byte-parity with the Kind flow). "k3s" survives only as Civo's *managed* opt-in (`useManagedK8s → "k3s"`),
  i.e. the opposite of what the ADR intended. This is the single largest divergence and should be reconciled
  in the ADR text.
- **Provisioning is the Go provider interface, not Crossplane CompositeCluster compositions.** The ADR frames
  workload clusters as `CompositeCluster` compositions that "swap" under an unchanged API; today day-0
  provisioning is imperative Go (`ProvisionEnvironment`/`CreateCluster`). The composition path is not yet
  built.
- **Managed-K8s is not "supported until parity" — it is removed for AWS/Azure/GCP.** The ADR says managed
  paths "remain supported until each cloud's custom path reaches parity." In code, only DO (DOKS) and Civo
  (k3s) still offer a managed path; AWS/Azure/GCP/custom **error** on `useManagedK8s: true`.
- **CCM/CSI replacement exists only for DigitalOcean.** The ADR's "CCM per cloud" consequence is realised for
  DO only; AWS/Azure/GCP/Civo compute clusters get no automated cloud-controller/CSI, so the cloud Gateway's
  LoadBalancer Service and default storage are unmet there.
- **No HA control plane anywhere.** Every provider hard-errors on `Replicas > 1`/`HighAvailability`
  (single control-plane node only); stacked-etcd + API LB is the acknowledged missing piece.
- **Full API-server flag control is available but OIDC wiring is not yet applied here.** `KubeadmInitMaster`
  gives the platform ownership of the static-pod `kube-apiserver.yaml` (it already patches
  `--kubelet-preferred-address-types`), but the Keycloak OIDC issuer flags the ADR motivates are not injected
  by this path today.
