# Adhar Provider Guide

How Adhar targets different infrastructure: the provider abstraction, per-cloud setup, and how to add your own provider. Architectural context in [Architecture §5](ARCHITECTURE.md#5-infrastructure--control-plane).

---

## 1. The Two Provisioning Paths

Adhar provisions infrastructure two complementary ways:

| Path | Mechanism | Used for |
|------|-----------|----------|
| **Imperative** | Go provider interface (`platform/providers/`) | Day-0 cluster creation from the CLI (`adhar up`, `adhar cluster create`), day-2 cluster ops |
| **Declarative** | Crossplane Compositions (`platform/controlplane/`) | Continuous, GitOps-managed infrastructure (`CompositeCluster`, `CompositeDatabase`, …) |

Both paths share the same provider credentials. The imperative path gets you a management cluster; the declarative path lets that cluster manage everything else.

## 2. Cluster Provisioning Model: Raw Compute by Default

On every cloud, adhar's **default** is to provision plain compute instances (Ubuntu 22.04) and install Kubernetes itself with kubeadm, driven over SSH:

1. Instances boot with a prep script: containerd (systemd cgroup driver), kubeadm/kubelet/kubectl from the pinned [pkgs.k8s.io](https://pkgs.k8s.io) minor stream, swap off, kernel prerequisites.
2. `kubeadm init` runs on the control plane with **kube-proxy skipped** — the platform bootstrap installs Cilium with `kubeProxyReplacement`, exactly like the local Kind flow. No CNI is preinstalled; nodes stay `NotReady` until Cilium arrives.
3. Workers join via `kubeadm join`; the admin kubeconfig is fetched over SSH and rewritten to the public endpoint.

A per-cluster ed25519 SSH key is generated under `~/.adhar/clusters/<name>/` and registered with the cloud; cluster resources are tagged/named `adhar-<cluster>-*` for discovery and cleanup. Day-2 operations work identically everywhere: **in-place upgrades** (`kubeadm upgrade` — control plane first, then workers, package stream switched per minor) and **worker scaling** (scale-up provisions + joins; scale-down drains, removes the node, then deletes the instance). Single control-plane per cluster today; HA control planes (LB + stacked etcd) are on the roadmap.

### Opting into managed Kubernetes

Set one flag on the provider to use the cloud's managed service instead — every other behaviour and operation is identical:

```yaml
providers:
  digitalocean:
    type: digitalocean
    useManagedK8s: true   # DOKS instead of droplets+kubeadm
```

| Provider | Default (`useManagedK8s: false`) | `useManagedK8s: true` |
|----------|----------------------------------|----------------------|
| digitalocean | Droplets + kubeadm | Managed DOKS |
| civo | Instances + kubeadm | Managed k3s |
| aws | EC2 + kubeadm | not offered (clear error) |
| azure | VMs + kubeadm | not offered (clear error) |
| gcp | GCE + kubeadm | not offered (clear error) |
| custom | Your machines + kubeadm (BYO) | not applicable (clear error) |
| kind | Local containers | not applicable |

### Bring-your-own hosts (`custom` provider)

Point adhar at machines you already own (bare metal, existing VMs):

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

`CreateCluster` preps each host over SSH and runs the same kubeadm flow; `DeleteCluster` runs `kubeadm reset` (your machines are never deleted).

### Live verification status

The DigitalOcean compute path has been verified end-to-end against a real account: cluster creation (~5 min to a serving API), SSH-fetched admin kubeconfig, node registration, Cilium install to `Ready`, worker scale-up (join) and scale-down (drain), plus cert-manager LetsEncrypt issuance via DigitalOcean DNS-01 (a real, publicly-trusted certificate; cert-manager created and cleaned the `_acme-challenge` records in the DO zone automatically). Note on external-dns with DigitalOcean DNS: releases ≥ v0.19 dropped the in-tree `digitalocean` provider (use the webhook provider), while ≤ v0.15 crashes on Kubernetes ≥ 1.33 (`failed to sync *v1.Endpoints`) — on modern clusters use external-dns ≥ v0.19 + the DigitalOcean webhook. Other clouds share the same code path but await their own live runs.

## 3. The Provider Interface

`platform/providers/interface.go` defines a single comprehensive interface every provider implements:

- **Cluster lifecycle** — create, get, list, update, delete, kubeconfig
- **Node groups** — create/scale/delete pools, autoscaling parameters
- **Networking** — VPC/subnet management, load balancers
- **Storage** — storage classes, volumes
- **Operations** — health checks, metrics, cost reporting, addon management

Providers register in `platform/providers/factory.go`; the `ProviderManager` selects and instantiates them from configuration. Each provider validates its own config against `config.schema.json` before any resources are created.

Implementations: `kind/`, `aws/`, `azure/`, `gcp/`, `digitalocean/`, `civo/`, `custom/`.

## 4. Provider Setup

### Kind (local — default)

No credentials, no cost. Adhar templates the Kind config (`platform/providers/kind/resources/kind.yaml.tmpl`) with the default CNI and kube-proxy **disabled** (Cilium replaces both) and host ports 8080/8443 mapped to the Gateway NodePorts.

```yaml
environments:
  local:
    provider: kind
    name: adhar-local
    type: development
```

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

Notable: managed node groups with autoscaling, IRSA for workload identity (use it for Crossplane credentials — see [Production §3](PRODUCTION.md#3-security-hardening-checklist)).

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

Notable: VPC-native clusters, Workload Identity, Autopilot and Standard modes.

### DigitalOcean (droplets; DOKS via useManagedK8s)

```bash
export DIGITALOCEAN_TOKEN="…"
```

```yaml
environments:
  production:
    provider: digitalocean
    name: adhar-prod
    region: nyc3
    clusterConfig:
      - { key: node_size,  value: s-2vcpu-4gb }
      - { key: node_count, value: "3" }
      - { key: auto_scale, value: "true" }
```

Sweet spot: cost-effective small/medium production.

### Civo (K3S)

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

Sweet spot: fastest cloud provisioning (< 5 min), cheap dev/staging.

### Custom (bring your own cluster)

Point Adhar at any conformant Kubernetes cluster (on-prem, another managed offering) and skip provisioning: the `custom` provider uses your kubecontext and runs the same bootstrap + GitOps flow on it. Requirements: a kernel recent enough for Cilium/eBPF, and no conflicting CNI you can't remove.

## 5. Configuration Resolution

Provider settings resolve through the four config layers (`globalSettings` → `providers` → `environmentTemplates` → `environments`); the environment block wins. Keep credentials out of `config.yaml` — use the environment variables above or workload identity. Validate any config without touching real infrastructure:

```bash
adhar up -f config.yaml --dry-run
```

Provider-specific test configurations live under `tests/`.

## 6. Troubleshooting

| Symptom | Check |
|---------|-------|
| Auth errors at create | Provider CLI works independently? (`aws sts get-caller-identity`, `az account show`, `gcloud auth list`, `doctl account get`, `civo apikey list`) |
| Schema validation failure | Compare your block against `config.schema.json`; `--dry-run` reports the exact path |
| Cluster created but bootstrap stalls | Node kernel/eBPF support for Cilium; security groups must allow node-to-node traffic |
| LB never gets an address | Cloud quota/permissions for load balancers; check provider console events |
| Kind: ports already bound | `adhar up --port 9443` |

Debug verbosely with `adhar up -f config.yaml --debug` (or `-v`), and inspect controller logs in `adhar-system`.

## 7. Adding a Provider

1. Implement the interface in `platform/providers/<name>/` (use `civo/` as the compact reference)
2. Register in `factory.go`; add config + schema entries
3. Add a test config under `tests/` and wire `--dry-run` validation
4. For declarative parity, add Crossplane Compositions implementing `CompositeCluster` for the new provider ([Customization §8](CUSTOMIZATION.md#8-extend-infrastructure-apis-crossplane))
5. Document setup in this guide

The provider interface is intentionally broad but not all-or-nothing — unimplemented capabilities should return clear "not supported" errors rather than partial behavior.

---

**Related**: [Getting Started](GETTING_STARTED.md) · [Production Guide](PRODUCTION.md) · [Customization §9](CUSTOMIZATION.md#9-add-a-provider)
