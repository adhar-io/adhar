# Adhar Provider Guide

**What this is for:** how Adhar targets different infrastructure — the two
provisioning paths, what is proven on real hardware versus render-verified, node
autoscaling, the Kubernetes version rules, per-cloud setup, and how to add your
own provider.

Architectural context: [Architecture §5](ARCHITECTURE.md#5-infrastructure--control-plane).
Production posture: [PRODUCTION.md](PRODUCTION.md). Reaching a provisioned
cluster: [PRODUCTION_ACCESS.md](PRODUCTION_ACCESS.md). Failures:
[TROUBLESHOOTING.md](TROUBLESHOOTING.md).

---

## Contents

1. [The two provisioning paths](#1-the-two-provisioning-paths)
2. [Verification status: what is proven where](#2-verification-status-what-is-proven-where)
3. [Node autoscaling](#3-node-autoscaling)
4. [Cluster provisioning model: raw compute by default](#4-cluster-provisioning-model-raw-compute-by-default)
5. [Kubernetes version](#5-kubernetes-version)
6. [The provider interface](#6-the-provider-interface)
7. [DNS: delegate the platform host BEFORE `adhar up`](#7-dns-delegate-the-platform-host-before-adhar-up)
8. [Provider setup](#8-provider-setup)
9. [Configuration resolution](#9-configuration-resolution)
10. [Adding a provider](#10-adding-a-provider)

---

## 1. The two provisioning paths

| Path | Mechanism | Used for |
| --- | --- | --- |
| **Imperative** | Go provider interface (`platform/providers/`) | Day-0 cluster creation from the CLI (`adhar up`, `adhar cluster create`) and day-2 cluster ops (`scale`, `upgrade`, `delete`) |
| **Declarative** | Crossplane Compositions (`platform/controlplane/`) | Continuous, GitOps-managed infrastructure (`CompositeCluster`, `CompositeDatabase`, …) |

Both share the same provider credentials. The imperative path gets you a
management cluster; the declarative path lets that cluster manage everything
else.

## 2. Verification status: what is proven where

Be explicit about this when planning: the provider code is shared, but only some
paths have run against real hardware.

| Path | Status | Evidence |
| --- | --- | --- |
| **DigitalOcean — droplets + kubeadm** | ✅ **Live-verified end to end** | Cluster creation (~5 min to a serving API), SSH-fetched admin kubeconfig, node registration, Cilium to `Ready`, worker scale-up (join) and scale-down (drain), node autoscaling in both directions, full 76-package production profile, HA mode, `adhar upgrade`, Velero backup+restore, publicly trusted wildcard TLS via DO DNS-01, clean teardown leaving no paid resources |
| **DigitalOcean — DOKS via `CompositeCluster`** | ✅ **Live-verified** | A `CompositeCluster` XR provisioned a real DOKS cluster in ~11 min, auto-registered it with ArgoCD (`cluster-wl-blr1`, labels `adhar.io/cluster`, `adhar.io/dataplane`, `adhar.io/dataplane-mode`), the thin workload profile landed 5/5 Healthy, teardown left nothing behind |
| **Cilium Cluster Mesh** | ✅ **Live-verified** | `adhar-mgmt` (id 1, `10.244.0.0/16`) meshed with `adhar-test` (id 2, `10.245.0.0/16`); `clustermesh status` green both sides, bidirectional traffic over a global service. See [PRODUCTION §7](PRODUCTION.md#7-cluster-mesh-t3) for the VPC rule and why SPIFFE/SPIRE is deliberately not deployed |
| **GCP — GCE instances + kubeadm** | ✅ **Live-verified** | Cluster creation on `adhar-cloud` (kubeadm on e2-standard-8, external CCM, PD CSI), a real load balancer for the Gateway, node autoscaling, the 80-package production profile, and a clean `adhar down` that removed every instance, the forwarding rule, the target pool, the `k8s-*` firewall rules and 41 `pvc-*` disks with no manual intervention. See [GCP_PROVIDER.md](GCP_PROVIDER.md) |
| **AWS, Azure, Civo** | ⚙️ **Built, render-verified only** | Provider registration, config-schema validation and `--dry-run` pass; the kubeadm code path — node prep, join, drain, scale plan, cloud-controller-manager, CSI + default StorageClass, upgrade — is shared with DigitalOcean (see §2.1 for the per-row status), and the managed opt-in (EKS / AKS / GKE / Civo k3s via `useManagedK8s: true`) is implemented end to end (create, kubeconfig, node groups, scale, upgrade, health, delete). **No live run yet** — treat first use on these clouds as a bring-up exercise |
| **`custom` (bring your own hosts)** | ⚙️ Render-verified | Same kubeadm flow over SSH against machines you own |
| **Kind (local)** | ✅ Exercised continuously | `make e2e` runs a full `adhar up` → verify → `adhar down` cycle |

**Per-provider setup pages** — credentials, a complete configuration, the exact
commands and the limits that bite:
[DigitalOcean](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Kind](KIND_PROVIDER.md) · [AWS](AWS_PROVIDER.md) · [Azure](AZURE_PROVIDER.md) ·
[GCP](GCP_PROVIDER.md) · [Civo](CIVO_PROVIDER.md) ·
[Your own hosts](CUSTOM_PROVIDER.md)

Phase 3 (preview environments, the ML golden path, the AI stack) is built but
awaits a live run; scorecards and the package marketplace contracts are
live-verified.

### 2.1 Self-managed lifecycle parity (kubeadm on raw compute)

DigitalOcean was the reference implementation; on 2026-09-15 its worker
lifecycle moved into shared helpers in `platform/providers` (`kubeadm.go`:
`EnableExternalCloudProvider`, `JoinCommand`, `RetireWorker`,
`WorkerScalePlan`) and the other providers were rebuilt on them. What each
provider actually does today — **built** means the code path exists and is
unit-tested where it is pure; **live** means it ran on that cloud:

| Capability | DigitalOcean | AWS | Azure | GCP | Civo |
| --- | --- | --- | --- | --- | --- |
| Create control plane + workers (shared node-prep, `kubeadm init/join`) | ✅ live | ✅ built | ✅ built | ✅ built | ✅ built |
| Scale up: fresh join token, prep, join (`ScaleNodeGroup`) | ✅ live | ✅ built (was a stub returning `nil`) | ✅ built (was a stub that only logged) | ✅ built (was a stub) | ✅ built (reached the managed node-pool API) |
| Scale down: drain + delete Node, then delete the instance | ✅ live | ✅ built (terminated without draining before) | ✅ built | ✅ built | ✅ built |
| `GetNodeGroup` / `ListNodeGroups` report real members | ✅ | ✅ (by tag) | ✅ (by VM prefix) | ✅ (by instance prefix) | managed pools only |
| Cloud-controller-manager (`--cloud-provider=external`) | ✅ DO CCM | ✅ built (`aws-cloud-controller-manager` chart) | ✅ built (`cloud-provider-azure` chart) | ✅ built (`cloud-provider-gcp` manifest) | ✅ built (Civo CCM manifest) |
| CSI driver + default StorageClass | ✅ DO CSI | ✅ built (EBS CSI chart, `adhar-block` gp3) | ✅ built (Azure Disk CSI chart, `adhar-block` StandardSSD) | ✅ built (PD CSI kustomize, `adhar-block` pd-balanced) | ✅ built (Civo CSI kustomize, `civo-volume` marked default) |
| CSI startup taint (`node.adhar.io/csi-not-ready`, lifted by the autoscaler; CSI node DaemonSet tolerates it) | ✅ live | ✅ built | ✅ built | ✅ built | ✅ built |
| Node autoscaler (needs the two rows above) | ✅ live | built, unverified | built, unverified | built, unverified | built, unverified |
| Nodes pull kpack-built images from the in-cluster Harbor (containerd certs.d) | ✅ live | ✅ shared node-prep | ✅ shared node-prep | ✅ shared node-prep | ✅ shared node-prep |
| Upgrade (`adhar upgrade`, control plane then workers — shared `KubeadmUpgradeCluster`) | ✅ live | ✅ built | ✅ built | ✅ built | ✅ built |
| Managed-service mode (`useManagedK8s: true`), same operations | ✅ DOKS live (via `CompositeCluster`) | ✅ EKS built | ✅ AKS built (BYO CNI) | ✅ GKE built | ✅ k3s built |
| Cleanup leaves no billed resources | ✅ live | ✅ built | ✅ built | ✅ **live** | ✅ built |
| Teardown sweeps what the IN-CLUSTER controllers created (CCM load balancers, CSI volumes) — invisible to the resource tracker | ✅ live | ✅ built (classic ELB **and** NLB, target groups, `k8s-elb-*` SGs, EBS volumes) | ✅ built (resource-group delete; per-resource when the group is not ours) | ✅ **live** (forwarding rule, target pool, `k8s-*` firewall rules, PD disks) | ✅ built (load balancers by instance identity, volumes detached first) |
| `--purge-orphaned-volumes` for untagged CSI leftovers | ✅ live | ✅ built | ✅ built (incl. disks outside the resource group) | ✅ **live** (41 disks swept on a real teardown) | ✅ built |
| Teardown works with NO local state (rediscovers the cluster from the cloud) | ✅ live | ✅ built (tag-based discovery) | ✅ built (VM tag + name) | ✅ **live** | ✅ built (instance tag) |
| Quota preflight: refuse before provisioning anything, naming the limit | — | ✅ built (Service Quotas `L-1216C47A` vCPU vs running usage) | ✅ built (regional `cores` **and** the size family) | ✅ **live** (`CPUS_ALL_REGIONS` + `SSD_TOTAL_GB`/`DISKS_TOTAL_GB`) | ✅ built (instances, CPU, RAM, disk, networks) |

The `custom` (bring-your-own hosts) provider shares the same rows where they
apply: create/join, scale by moving the boundary within `workerIPs` (join the
next host, or drain + `kubeadm reset` the last one), upgrade, and — since there
is no cloud storage — the local-path provisioner as the default StorageClass.

The integration itself is one shared runner (`platform/providers/cloudintegration.go`):
each provider lists idempotent steps (`helm upgrade --install` with pinned chart
versions, `kubectl apply` of pinned manifests/kustomizations, create-once
Secrets for cloud credentials, the default StorageClass, the CSI DaemonSet's
startup-taint toleration) and they run over SSH on the control plane right
after the first joins; a failing step names itself. The step lists are
unit-tested (`*/cloud_integration_test.go`), the cloud calls are not.

**What "fully in sync" still needs, per cloud** is now the same thing
everywhere: **one live bring-up** (credentials + spend approval) that runs
`adhar up`, scale-up/down, the autoscaler, `adhar upgrade` and `adhar down`,
and then flips the column's rows from *built* to *live*. Known specifics to
watch on that run:

- **AWS**: the CCM chart values (`args[]`) and the EBS CSI `awsAccessSecret`
  keys follow the upstream chart pins in `aws/cloud_integration.go`; with an
  instance profile no `aws-secret` is written. EKS mode needs the `aws` CLI on
  PATH wherever the kubeconfig is used (`aws eks get-token`) and IAM rights to
  create the two roles (`adhar-<cluster>-eks-cluster`, `-eks-node`).
- **Azure**: `azure.json` (kube-system/`azure-cloud-provider`) is built from
  the service-principal fields; managed identity is passed through
  `useManagedIdentityExtension`. AKS mode is created with `networkPlugin: none`
  so the bootstrap's Cilium is the CNI, as on every other cluster.
- **GCP**: the PD CSI driver takes the service-account key as `cloud-sa` (or
  application-default credentials when none is configured). GKE mode needs
  `gke-gcloud-auth-plugin` on PATH. GKE and EKS ship their own CNI/kube-proxy;
  the platform bootstrap installing Cilium on those managed planes is the
  least-verified part of managed mode.
- **Civo**: CCM + CSI read the API key from kube-system/`civo-api-access`; the
  CSI manifest ships `civo-volume`, which is marked default.

The teardown and quota rows above came out of the GCP bring-up, where each was a
real bill or a real half-built cluster before it was code: a load balancer and 41
CSI disks survived the first `adhar down`, and an exhausted regional vCPU quota
failed a create ten minutes in. AWS, Azure and Civo now carry the same three
protections — an in-cluster-resource sweep ordered before the network teardown, an
opt-in orphan-volume purge, and a preflight that refuses a create the account
cannot hold. The selection rules are unit-tested (`*/teardown_test.go`); the cloud
calls still await a live run.

The autoscaler's own logic is provider-agnostic and unit-tested; on a cloud
without a CSI driver it simply never sees `exceed max volume count`.

### 2.2 Startup and GitOps-sync parity

Every startup fix measured on the local Kind flow applies to the cloud and
on-prem providers too, because it lives in the shared layers rather than in a
provider:

| Fix | Where it lives | Providers it covers |
| --- | --- | --- |
| Probe timeouts raised off the 1-second chart defaults (62 probes, 20 packages) | stack packages | all |
| ExternalSecrets that read `keycloak-clients` refresh at 30s, so a lost race against Keycloak's client provisioning costs seconds | stack packages | all |
| oauth2-proxy Deployments cap `progressDeadlineSeconds` at 120s instead of 600s | stack packages | all |
| `ignoreDifferences` for API- and webhook-defaulted fields, plus ArgoCD server-side diff | ApplicationSets + ArgoCD bootstrap | all |
| ArgoCD reconciliation 120s + 15s jitter (was 300s + 60s, which idled an app needing two passes for up to 12 minutes) | ArgoCD bootstrap | all |
| GitOps repos seeded from host-packed git bundles; Crossplane core started before seeding | AdharPlatform controller | all |
| `adhar up` drives Applications to Synced + Healthy and reports `n/m` progress (`--apps-timeout`) | controller + CLI | all (cloud/on-prem logs it, Kind renders it on the checklist) |
| Cilium data-path images pre-pulled in the background during node prep | `provider.CriticalPathImages` + `KubeadmNodePrepScript` | every kubeadm provider (AWS, Azure, GCP, DigitalOcean, Civo, custom) |
| Per-registry pull-through image cache | `platform/providers/kind` | Kind only — a cloud node has no host cache to pull from; the node-prep pre-pull is its equivalent |

The one deliberate asymmetry is the last row: on Kind the node shares the
developer's machine, so a local registry cache serves every run. A cloud node
is created once and thrown away, so the equivalent win is overlapping the CNI
image download with the kubeadm join, which is what node prep now does.

## 3. Node autoscaling

Live-verified in both directions on DigitalOcean. Enable it per environment (or
on an environment template, which every environment using it inherits):

```yaml
environments:
  production:
    provider: digitalocean
    name: adhar-prod
    region: nyc3
    clusterConfig:
      - { key: nodeSize,  value: s-8vcpu-16gb }
      - { key: nodeCount, value: "3" }
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
```

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Turn the autoscaler on |
| `minWorkers` | `1` | Floor |
| `maxWorkers` | `5` | Ceiling — the spend limit |
| `nodeGroup` | `workers` | Provider node group to scale |
| `scaleUpUtilizationThreshold` | `"90%"` | CPU **or** memory at or above this adds a worker, before anything is Pending |
| `scaleDownUtilizationThreshold` | `"50%"` | CPU **and** memory must both stay under this |
| `scaleDownDelay` | `"10m"` | Time under the threshold before a node is drained |
| `scaleUpCooldown` | `"3m"` | Minimum gap between two additions |

A cloud platform is created with **1 control plane and 2 workers** by default, and
`nodeCount` overrides that. Two is the smallest shape that is really a cluster: one
worker leaves evictions, drains and node replacements nowhere to put the pods, and a
single worker reaches the kubelet's 110-pod ceiling well before it runs out of CPU.
Autoscaling then turns that starting size into a range. A node it adds takes its Kubernetes version from the running control
plane ([§5](#5-kubernetes-version)), so a scaled cluster cannot skew.

**Scale-up has two triggers, and the first is the one that matters.**

1. **Utilisation**, at or above `scaleUpUtilizationThreshold` (90% by default) on
   CPU **or** memory. Either dimension counts: a cluster at 95% CPU and 40% memory
   cannot place another CPU-bound pod, and the platform's catalogue is CPU-bound.
   This adds a worker while the cluster is merely *busy*.
2. **Pods unschedulable for capacity** — `insufficient cpu`/`memory`/`pods`/
   `ephemeral-storage`, `too many pods`, `exceed max volume count`. This is the
   backstop for a burst too large to anticipate, and it can add several workers at
   once.

Waiting only for trigger 2 meant the cluster grew after work had already stopped:
an unschedulable pod has to wait out a node create, which is minutes on every
cloud. Trigger 1 exists so that does not happen in the ordinary case.

Both are evaluated before scale-down, and a `scaleUpUtilizationThreshold` below
`scaleDownUtilizationThreshold` is clamped up to it — otherwise the autoscaler
would add a node and immediately qualify to remove it.

Taints, unsatisfiable node affinity and `volume node affinity conflict` are
deliberately not triggers: a new worker is a clone of the existing ones.

**Two guards refuse a scale-down:** a recent move in either direction (one node
per `scaleDownDelay` window, so the cluster settles), and a node hosting a pod
with a **ReadWriteOnce** volume — that volume is attached to that machine.

Inspect decisions at `.status.autoscaling` on the `AdharPlatform` (`workers`,
`lastScaleUp`, `lastScaleDown`, `underutilizedSince`, `lastReason`) and via
`adhar get status`. Decoding the reasons:
[TROUBLESHOOTING §5.3](TROUBLESHOOTING.md#53-the-autoscaler-is-not-scaling).

Manual scaling uses the same code path:

```bash
adhar cluster scale <cluster> --workers 8 --node-group workers -p digitalocean -f config.yaml
```

## 4. Cluster provisioning model: raw compute by default

On every cloud, Adhar's **default** is to provision plain compute instances
(Ubuntu 22.04) and install Kubernetes itself with kubeadm, driven over SSH:

1. Instances boot with a prep script: containerd (systemd cgroup driver),
   kubeadm/kubelet/kubectl from the pinned [pkgs.k8s.io](https://pkgs.k8s.io)
   minor stream, swap off, kernel prerequisites.
2. `kubeadm init` runs on the control plane with **kube-proxy skipped** — the
   platform bootstrap installs Cilium with `kubeProxyReplacement`, exactly like
   the local Kind flow. No CNI is preinstalled; nodes stay `NotReady` until
   Cilium arrives.
3. Workers join via `kubeadm join`; the admin kubeconfig is fetched over SSH and
   rewritten to the public endpoint.

A per-cluster ed25519 SSH key is generated under `~/.adhar/clusters/<name>/` and
registered with the cloud; resources are tagged/named `adhar-<cluster>-*` for
discovery and cleanup. Day-2 operations work identically everywhere: **in-place
upgrades** (`kubeadm upgrade` — control plane first, then workers, package
stream switched per minor) and **worker scaling** (scale-up provisions + joins;
scale-down drains, removes the node, then deletes the instance). Single
control-plane per cluster today; HA control planes (LB + stacked etcd) are on
the roadmap.

### Opting into managed Kubernetes

Set one flag on the provider to use the cloud's managed service instead — every
other behaviour and operation is identical (`clusterMode: <service>` is the
explicit spelling of the same switch; `compute` is the default). Creating,
fetching the kubeconfig, adding/removing/scaling node groups, upgrading,
health and teardown all go through the managed API in that mode, and the
network the cluster sits in is created and cleaned up by the same code as the
compute mode:

```yaml
providers:
  digitalocean:
    type: digitalocean
    useManagedK8s: true   # DOKS instead of droplets+kubeadm
```

| Provider | Default (`useManagedK8s: false`) | `useManagedK8s: true` |
| --- | --- | --- |
| digitalocean | Droplets + kubeadm | Managed DOKS |
| civo | Instances + kubeadm | Managed k3s |
| aws | EC2 + kubeadm | Managed EKS (`clusterMode: eks`; needs the `aws` CLI for `eks get-token`) |
| azure | VMs + kubeadm | Managed AKS (`clusterMode: aks`; BYO CNI, Cilium from the bootstrap) |
| gcp | GCE + kubeadm | Managed GKE (`clusterMode: gke`; needs `gke-gcloud-auth-plugin`) |
| custom | Your machines + kubeadm (BYO) | not applicable (clear error) |
| kind | Local containers | not applicable |

### Bring-your-own hosts (`custom` provider)

Point Adhar at machines you already own (bare metal, existing VMs):

```yaml
providers:
  custom:
    type: custom
    config:
      masterIPs: ["203.0.113.10"]        # exactly one control-plane host
      workerIPs: ["203.0.113.11", "203.0.113.12"]
      sshUser: "root"                     # or a passwordless-sudo user
      sshKeyPath: "~/.ssh/id_ed25519"     # your key (passphrase-less)
```

`CreateCluster` preps each host over SSH and runs the same kubeadm flow;
`DeleteCluster` runs `kubeadm reset` (your machines are never deleted).
Requirements: a kernel recent enough for Cilium/eBPF, and no conflicting CNI you
cannot remove.

## 5. Kubernetes version

Default: `globals.DefaultKubernetesVersion` = **v1.37.0**.

| Precedence (highest first) | Where |
| --- | --- |
| 1. `adhar up --kube-version v1.37.0` | CLI flag — applies to **every** provider, not just Kind |
| 2. `kubeVersion` or `version` in the environment's `clusterConfig` | `config.yaml` |
| 3. `globals.DefaultKubernetesVersion` | Compiled in |

The flag carries the platform default as its *default value*, so only an
explicitly passed `--kube-version` overrides the environment — otherwise every
cluster would be silently pinned to the CLI's compiled-in version.

**Nodes added later take the running control plane's version.** Both
`adhar cluster scale` and the node autoscaler ask the control plane
(`kubeadm version -o short`) and prepare the new worker against that minor
stream, so a newer CLI cannot introduce a version skew kubeadm would reject.

To move an existing cluster:

```bash
adhar cluster upgrade <cluster> --version 1.37.2 -p digitalocean -f config.yaml
```

## 6. The provider interface

`platform/providers/interface.go` defines a single interface every provider
implements:

- **Cluster lifecycle** — create, get, list, update, delete, kubeconfig
- **Node groups** — create/scale/delete pools, autoscaling parameters
- **Networking** — VPC/subnet management, load balancers
- **Storage** — storage classes, volumes
- **Operations** — health checks, metrics, cost reporting, addon management

Providers register in `platform/providers/factory.go`; the `ProviderManager`
selects and instantiates them from configuration. Each provider validates its
own config against `config.schema.json` before any resources are created.

Implementations: `kind/`, `aws/`, `azure/`, `gcp/`, `digitalocean/`, `civo/`,
`custom/`.

## 7. DNS: delegate the platform host BEFORE `adhar up`

This is manual, it is required on every cloud, and the platform deliberately does
not do it for you: creating a DNS zone means taking over a name you own, and
repointing a registrar is not something a provisioning tool should do behind your
back. Ten minutes here is the difference between a platform on a publicly trusted
wildcard certificate and one that every browser warns about.

### The model

One value drives everything: `globalSettings.defaultHost`. Every app is published at
`<app>.<defaultHost>`, the wildcard certificate is `*.<defaultHost>`, and
external-dns writes one record per app into the zone for that name.

So make `defaultHost` a **subdomain you delegate**, not the domain you registered:

```
example.com              ← stays wherever it is registered (GoDaddy, Namecheap, …)
└── platform.example.com ← a PUBLIC zone in your cloud's DNS  ← defaultHost
    ├── argocd.platform.example.com   ← written by external-dns
    ├── gitea.platform.example.com
    └── …
```

Three steps, in this order:

1. **Create a public zone for exactly `defaultHost`** in your cloud's DNS service.
   The platform never creates it — `adhar up` fails at `configuring edge DNS` if it
   is missing, and external-dns has nowhere to write.
2. **Read that zone's nameservers** (the provider assigns them; they differ per
   zone on AWS, Azure and GCP).
3. **At your registrar, add NS records for the label only.** For
   `platform.example.com`, that is four (or two) NS records named `platform` in the
   `example.com` zone, pointing at the nameservers from step 2. The apex stays
   exactly where it is.

Verify before running `adhar up`:

```bash
HOST=platform.example.com        # your defaultHost
APEX=example.com                 # the domain you registered

dig +short NS "$HOST"            # → your cloud's nameservers for the zone
dig +short NS "$APEX"            # → your registrar's nameservers (unchanged)
dig +noall +comment CAA "$APEX"  # → status: NOERROR  (an EMPTY answer is correct)
```

### The trap that costs a day

**Do not point the whole registered domain at your cloud unless you also host its
apex zone there.** It looks equivalent and it is not: Let's Encrypt walks *up* the
tree reading CAA records, so issuing for `*.platform.example.com` also queries CAA
for `platform.example.com` **and for `example.com`**. If the apex is delegated to
nameservers that hold no zone for it, every one of those lookups returns SERVFAIL
and the order fails with

```
DNS problem: SERVFAIL looking up CAA for example.com
  - the domain's nameservers may be malfunctioning
```

which names CAA, not delegation — so it reads as a certificate problem when it is a
delegation problem. This happened on the first live GCP bring-up: the platform
subdomain resolved perfectly, every app was reachable, and issuance could never
succeed.

A CAA record is **not** required. No CAA means any CA may issue, which is what you
want. Add one only to pin issuance: `0 issue "letsencrypt.org"` plus
`0 issuewild "letsencrypt.org"` at the apex.

**If you are moving the apex back to your registrar**, add the delegation NS records
in the registrar's zone *first*, then change the nameservers. Do it the other way
round and the platform subdomain stops resolving for the propagation window.

### What each provider needs

| `dnsProvider` | Zone service | Trusted wildcard cert (ACME DNS-01) | Credentials the platform needs |
| --- | --- | --- | --- |
| `digitalocean` | DigitalOcean DNS | ✅ | `providers.digitalocean.token` (or `DIGITALOCEAN_TOKEN`) |
| `aws` | Route 53 | ✅ | **static** `accessKeyId` + `secretAccessKey` (+ region). An instance profile is not enough — the solver reads keys from a Secret |
| `gcp` | Cloud DNS | ✅ | `serviceAccountKey`/`serviceAccountKeyFile` **and** `projectId`. ADC alone provisions fine but fails here |
| `azure` | Azure DNS | ✅ | `clientId`, `clientSecret`, `tenantId`, `config.subscriptionId`, `config.dnsResourceGroup` |
| `cloudflare` | Cloudflare | ✅ | `config.cloudflareApiToken` (or `CLOUDFLARE_API_TOKEN`) |
| `civo` | Civo DNS | ❌ records only | `providers.civo.token`. cert-manager has no Civo solver, so the platform keeps its self-signed certificate — use `dnsProvider: cloudflare` for the zone if you need a trusted one |
| `none` / omitted | — | ❌ | none. Self-signed certificate, no DNS automation |

### Skipping it is supported

A platform with no delegated zone still comes up and works — it serves the
self-signed platform certificate, so browsers warn and strict clients need the
platform CA. `adhar up` says so in its closing summary, and `adhar get status`
raises it as a warning quoting cert-manager's own reason, so this is never a silent
failure. Fix the DNS later and cert-manager issues a trusted certificate on its own
within minutes.

## 8. Provider setup

### Kind (local — default)

No credentials, no cost. Adhar templates the Kind config
(`platform/providers/kind/resources/kind.yaml.tmpl`) with the default CNI and
kube-proxy **disabled** (Cilium replaces both) and host ports 8080/8443 mapped
to the Gateway NodePorts.

```yaml
environments:
  local:
    provider: kind
    name: adhar-local
    type: development
```

Port conflict? `adhar up --port 9443` (HTTP auto-derives as 9080).

### DigitalOcean (droplets; DOKS via `useManagedK8s`)

The most exercised path — see [§2](#2-verification-status-what-is-proven-where).

```bash
export DIGITALOCEAN_TOKEN="…"        # DIGITALOCEAN_ACCESS_TOKEN (doctl's) is accepted too
```

```yaml
globalSettings:
  defaultHost: platform.example.io   # a DigitalOcean DNS zone → records + wildcard TLS are automatic
  email: admin@example.io
environments:
  production:
    provider: digitalocean
    name: adhar-prod
    region: nyc3
    clusterConfig:
      - { key: nodeSize,  value: s-8vcpu-16gb }
      - { key: nodeCount, value: "10" }
```

**Sizing — volumes, not CPU.** DigitalOcean attaches at most **7 block volumes
per droplet**, and the kubelet defaults to 110 pods per node. The full
production profile creates ~55–60 PersistentVolumes and ~300 pods, so it needs
**at least 10 workers regardless of droplet size** — pods otherwise stay
`Pending` with `node(s) exceed max volume count` (8 workers hit the ceiling in
the verified run). A curated ~30-package profile runs comfortably on 3–4 ×
`s-8vcpu-16gb`. Details:
[TROUBLESHOOTING §5.1](TROUBLESHOOTING.md#51-exceed-max-volume-count--the-digitalocean-7-volume-wall).

**Token gotchas.** A *scoped* DO token returns 401 on `/v2/account` and
`/v2/projects` while working fine for droplets, volumes, LBs, VPCs, DNS and
Kubernetes — never judge a token by `doctl account get`. And `doctl` ignores
both `-t` and `DIGITALOCEAN_ACCESS_TOKEN` when its config has a `context:`; use
`doctl --context default -t "$TOKEN"`.
([TROUBLESHOOTING §5.5](TROUBLESHOOTING.md#55-a-scoped-digitalocean-token-401s-on-v2account).)

**external-dns.** Releases ≥ v0.19 dropped the in-tree `digitalocean` provider
(use the webhook provider), while ≤ v0.15 crashes on Kubernetes ≥ 1.33
(`failed to sync *v1.Endpoints`). On modern clusters use external-dns ≥ v0.19
plus the DigitalOcean webhook.

**Teardown** (`adhar cluster delete <name> --file config.yaml`, or
`adhar up … --recreate`) removes everything the cluster created in the account:
droplets, the CCM-provisioned LoadBalancer(s) in the cluster VPC, the block
volumes behind its PersistentVolumes (the CSI driver tags them
`adhar-cluster-<name>`), the firewall, the VPC, and the SSH key. Add
`--purge-orphaned-volumes` to sweep pre-tagging `pvc-*` leftovers.

The complete verified run (exact config, commands, timings, verification,
teardown) is in [DIGITALOCEAN_PROVIDER.md](DIGITALOCEAN_PROVIDER.md).

### AWS (EC2 compute)

```bash
aws configure   # or:
export AWS_ACCESS_KEY_ID="…" AWS_SECRET_ACCESS_KEY="…"
```

```yaml
environments:
  production:
    provider: aws
    name: adhar-aws-prod
    region: us-west-2
    clusterConfig:
      - { key: instance_type,    value: m5.large }
      - { key: desired_capacity, value: "3" }
      - { key: min_size,         value: "1" }
      - { key: max_size,         value: "10" }
```

Use IRSA for workload identity, including Crossplane's credentials
([PRODUCTION §5](PRODUCTION.md#5-security-hardening)). `useManagedK8s: true`
on the provider switches to EKS (control plane + managed node groups + EBS CSI
addon). Render-verified only.

### Azure (VM compute)

```bash
az login   # or:
export AZURE_CLIENT_ID="…" AZURE_CLIENT_SECRET="…" AZURE_TENANT_ID="…"
```

```yaml
environments:
  production:
    provider: azure
    name: adhar-azure-prod
    region: East US
    clusterConfig:
      - { key: node_vm_size,        value: Standard_D2s_v3 }
      - { key: node_count,          value: "3" }
      - { key: enable_auto_scaling, value: "true" }
```

`useManagedK8s: true` on the provider switches to AKS (BYO CNI, one agent
pool per node group). Render-verified only.

### GCP (GCE compute)

```bash
gcloud auth application-default login   # or:
export GOOGLE_APPLICATION_CREDENTIALS="path-to-service-account.json"
```

```yaml
environments:
  production:
    provider: gcp
    name: adhar-gcp-prod
    region: us-central1
    clusterConfig:
      - { key: machine_type, value: e2-standard-4 }
      - { key: disk_size,    value: "50" }
      - { key: node_count,   value: "3" }
```

Use Workload Identity for Crossplane credentials. `useManagedK8s: true` on
the provider switches to GKE (zonal, one node pool per node group).
Render-verified only.

### Civo (instances; k3s via `useManagedK8s`)

```bash
export CIVO_API_KEY="…"
```

```yaml
environments:
  staging:
    provider: civo
    name: adhar-civo-staging
    region: LON1
    clusterConfig:
      - { key: node_size,  value: g4s.kube.medium }
      - { key: node_count, value: "3" }
```

Sweet spot: fast, cheap dev/staging. Render-verified only.

## 9. Configuration resolution

Provider settings resolve through the four config layers
(`globalSettings` → `providers` → `environmentTemplates` → `environments`); the
environment block wins. Keep credentials out of `config.yaml` — use the
environment variables above or workload identity.

Validate any config without touching real infrastructure:

```bash
adhar up -f config.yaml --dry-run
```

Provider-specific test configurations live under `tests/`.

Two flags worth internalising:

- `adhar up -f config.yaml` provisions **every** environment in the file.
  Always pass `--env <name>`.
- `adhar cluster list` **requires `--file <config>`**, or it queries no provider
  and reports "No clusters found"
  ([TROUBLESHOOTING §5.6](TROUBLESHOOTING.md#56-adhar-cluster-list-says-no-clusters-found)).
  `scale`, `upgrade` and `delete` take the same file; on `cluster delete`, `-f`
  means `--force`, so spell out `--file` there.

Debug verbosely with `adhar up -f config.yaml --debug` (or `-v`) and inspect the
controller logs in `adhar-system`. Provider-level symptoms:

| Symptom | Check |
| --- | --- |
| Auth errors at create | Does the provider CLI work independently? (`aws sts get-caller-identity`, `az account show`, `gcloud auth list`, `civo apikey list`) — for DigitalOcean, probe droplets rather than `doctl account get` |
| Schema validation failure | Compare your block against `config.schema.json`; `--dry-run` reports the exact path |
| Cluster created but bootstrap stalls | Node kernel/eBPF support for Cilium; security groups must allow node-to-node traffic |
| LB never gets an address | Cloud quota/permissions for load balancers; provider console events |
| Kind: ports already bound | `adhar up --port 9443` |

Everything else: [TROUBLESHOOTING.md](TROUBLESHOOTING.md), which opens with a
symptom-to-section lookup table.

## 10. Adding a provider

1. Implement the interface in `platform/providers/<name>/` (use `civo/` as the
   compact reference)
2. Register in `factory.go`; add config + schema entries
3. Add a test config under `tests/` and wire `--dry-run` validation
4. For declarative parity, add Crossplane Compositions implementing
   `CompositeCluster` for the new provider
   ([Customization §9](CUSTOMIZATION.md#9-extend-the-infrastructure-apis-crossplane))
5. Document setup in this guide, and record its verification status in
   [§2](#2-verification-status-what-is-proven-where) — "render-verified" until
   it has actually run

The provider interface is intentionally broad but not all-or-nothing:
unimplemented capabilities should return clear "not supported" errors rather
than partial behaviour.

---

**Related**: [Getting Started](GETTING_STARTED.md) ·
[Production Guide](PRODUCTION.md) · [Production Access](PRODUCTION_ACCESS.md) ·
[Troubleshooting](TROUBLESHOOTING.md) ·
[DigitalOcean runbook](DIGITALOCEAN_PROVIDER.md) ·
[Customization §10](CUSTOMIZATION.md#10-add-a-provider)
