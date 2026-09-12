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
7. [Provider setup](#7-provider-setup)
8. [Configuration resolution](#8-configuration-resolution)
9. [Adding a provider](#9-adding-a-provider)

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
| **AWS, Azure, GCP, Civo** | ⚙️ **Render-verified only** | Provider registration, config-schema validation and `--dry-run` pass; the same kubeadm code path is shared with DigitalOcean. **No live run yet** — treat first use on these clouds as a bring-up exercise |
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
| `scaleDownUtilizationThreshold` | `"50%"` | CPU **and** memory must both stay under this |
| `scaleDownDelay` | `"10m"` | Time under the threshold before a node is drained |
| `scaleUpCooldown` | `"3m"` | Minimum gap between two additions |

The cluster is still created with `nodeCount`; autoscaling turns that into a
range. A node it adds takes its Kubernetes version from the running control
plane ([§5](#5-kubernetes-version)), so a scaled cluster cannot skew.

**Scale-up** triggers on pods unschedulable *for capacity* — `insufficient
cpu`/`memory`/`pods`/`ephemeral-storage`, `too many pods`, and
`exceed max volume count`. It is evaluated before scale-down. Taints, unsatisfiable
node affinity and `volume node affinity conflict` are deliberately not triggers:
a new worker is a clone of the existing ones.

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
other behaviour and operation is identical:

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
| aws | EC2 + kubeadm | not offered (clear error) |
| azure | VMs + kubeadm | not offered (clear error) |
| gcp | GCE + kubeadm | not offered (clear error) |
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

## 7. Provider setup

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
([PRODUCTION §5](PRODUCTION.md#5-security-hardening)). Render-verified only.

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

Render-verified only.

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

Use Workload Identity for Crossplane credentials. Render-verified only.

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

## 8. Configuration resolution

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

## 9. Adding a provider

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
