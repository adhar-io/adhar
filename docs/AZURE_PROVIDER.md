# Azure provider

Adhar on Azure: virtual machines bootstrapped with kubeadm, the same code path
that is live-verified on DigitalOcean.

> **Status: render-verified, not live-verified.** Provider registration, config
> validation and `adhar up --dry-run` pass, and the provisioning code is shared
> with the DigitalOcean path that has run end to end. **No full run on Azure has
> been completed.** Treat your first use as a bring-up exercise. See
> [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md#2-verification-status-what-is-proven-where)
> for the matrix, and the [DigitalOcean provider guide](DIGITALOCEAN_PROVIDER.md)
> for everything that is not cloud-specific.

> Preparing the first live run (2026-09-26) on subscription `b8e6d308` got as far
> as the quota wall, and produced three changes worth knowing about:
>
> - **`adhar up` now runs a preflight** for every cloud provider (Kind is exempt)
>   and refuses to create anything when it fails. On Azure it checks the resource
>   providers, the VM size's availability in the region, and both of the vCPU
>   limits that apply. It caught this subscription's 4-vCPU ceiling in under a second, with
>   nothing created.
> - **The autoscaling range now reaches the cluster spec.** It was dropped when the
>   spec was built, so quota checks sized against the starting worker count. The
>   Azure check reported "needs 24 vCPU" for a cluster configured to reach 56.
> - `Microsoft.Compute` and `Microsoft.Storage` needed registering first; see
>   [0.1](#01-register-the-resource-providers).
>
> The run that followed reached a working cluster on `test.adhar.io` (real Let's
> Encrypt TLS, CCM load balancer, CSI provisioning, a 1 + 2 shape that autoscaled
> to 1 + 4) and then stalled at 39 of 75 apps — **not on CPU, on the per-VM
> data-disk attach limit**. Two more changes came out of that:
>
> - **The default StorageClass is now node-local** (`adhar-local`), because ~90
>   claims cannot attach to four VMs however large they are. See
>   [Storage](#storage-why-the-default-class-is-node-local-not-azure-disk).
> - **An autoscaled worker now takes its node group's VM size.** It was reading
>   the provider-level `vmSize`, which describes the CONTROL PLANE: a cluster with
>   4-vCPU `Standard_E4bds_v5` workers grew 2-vCPU `Standard_E2bds_v5` ones, so
>   each new node arrived with half the CPU and half the disk slots the scale-up
>   had counted on.

| | |
|---|---|
| Provisioning model | kubeadm on Azure VMs (default); AKS via `useManagedK8s: true` |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Azure DNS, or any zone you point at the load balancer |

## 0. Before you start: three prerequisites, in this order

`adhar up` now checks all three itself and refuses to create anything if one
fails, naming the remedy. They are listed in the order in which a failure bites.

| # | Prerequisite | Fails as |
|---|---|---|
| 0.1 | Resource providers registered | `MissingSubscriptionRegistration`, naming a namespace rather than the VM |
| 0.2 | vCPU quota for the cluster at FULL size | A create that stops partway, or a cluster that boots and then cannot autoscale |
| 0.3 | A delegated DNS zone for `defaultHost` | Self-signed certificates; the platform still runs |

### 0.1 Register the resource providers

A fresh subscription has these **unregistered**, and nothing can be created until
they are. Measured on a real subscription (2026-09-26): `Microsoft.Compute` and
`Microsoft.Storage` were both `NotRegistered` while `Microsoft.Network` was fine.

```bash
for ns in Microsoft.Compute Microsoft.Network Microsoft.Storage; do
  az provider show -n $ns --query registrationState -o tsv
done

# Registering is idempotent and takes a few minutes.
az provider register --namespace Microsoft.Compute
az provider register --namespace Microsoft.Storage
```

This is worth checking rather than discovering, because the error Azure returns
names the namespace and not the VM you asked for — and it arrives only after the
resource group and network already exist.

### 0.2 Raise the vCPU quota for the cluster at FULL size

Azure enforces **two** limits and a create can fail on either:

- **Total Regional vCPUs** — the region-wide ceiling.
- **The VM family quota**, e.g. `standardDSv3Family` for `Standard_D8s_v3`.

```bash
az vm list-usage --location <region> -o table
```

**A new subscription defaults to 4 total regional vCPUs, in every region.** That
was the live finding on subscription `b8e6d308`: 4 in malaysiawest,
southeastasia, eastasia, centralindia, southindia, eastus, westus2, westeurope,
uksouth, australiaeast and japaneast alike. Four vCPUs cannot host this platform.

Size the request against the cluster's **maximum**, not its starting node count.
For the shipped `config.azure.yaml` — one control plane plus up to 6 workers of
`Standard_D8s_v3` at 8 vCPU each — that is **56 vCPU**. Ask for 64 to leave room.

Sizing to the floor is the subtle version of this mistake: the cluster boots on 2
workers, the catalogue needs more, the autoscaler asks, and a limit nobody checked
refuses. The preflight deliberately reports the full-size figure for that reason.

### 0.3 DNS: delegate the platform host

`globalSettings.defaultHost` drives every URL and the wildcard certificate, so it
must be a subdomain you delegate to an Azure DNS zone. The platform never creates
the zone.

```bash
# 1. a resource group for DNS (or reuse one) and a PUBLIC zone for the defaultHost
az group create --name adhar-dns --location eastus
az network dns zone create --resource-group adhar-dns --name platform.example.com

# 2. read the four nameservers Azure assigned to THIS zone
az network dns zone show --resource-group adhar-dns --name platform.example.com \
  --query nameServers --output tsv
```

3. At your registrar, in the `example.com` zone, add **four NS records named
   `platform`** with those four values. The apex stays where it is.

Azure DNS needs five values in configuration, and the zone's resource group is one
of them — it is separate from the cluster's:

```yaml
providers:
  azure:
    clientId: "…"
    clientSecret: "…"
    tenantId: "…"
    config:
      subscriptionId: "…"
      dnsResourceGroup: adhar-dns     # the group holding the DNS zone
```

The service principal needs **DNS Zone Contributor** on that zone (external-dns
writes one record per app; cert-manager writes the ACME TXT records).

**Verify before `adhar up`** (an empty CAA answer is correct; SERVFAIL is not):

```bash
dig +short NS platform.example.com      # → the nameservers from step 2
dig +short NS example.com               # → your registrar's, unchanged
dig +noall +comment CAA example.com     # → status: NOERROR
```

**Do not delegate the whole registered domain here unless you also host its apex
zone here.** Let's Encrypt reads CAA up the tree, so issuance for
`*.platform.example.com` also queries CAA for `example.com`; if that name is
delegated to nameservers holding no zone, every lookup SERVFAILs and the order fails
with a message naming CAA rather than delegation. Full explanation and the
capability table: [Provider guide §7](PROVIDER_GUIDE.md#7-dns-delegate-the-platform-host-before-adhar-up).

Skipping this is supported: the platform comes up on its self-signed certificate,
`adhar up` says so in its closing summary and `adhar get status` warns, and
cert-manager issues a trusted certificate on its own once the DNS is fixed.

## 1. Credentials

```yaml
providers:
  azure:
    type: azure
    region: eastus
    primary: true
    subscriptionId: "00000000-0000-0000-0000-000000000000"
    tenantId: "00000000-0000-0000-0000-000000000000"
    clientId: "…"
    clientSecret: "…"
    # useAzureCLI: true          # reuse an `az login` session
    # useManagedIdentity: true   # when Adhar runs on an Azure VM
    # useEnvironment: true       # AZURE_* environment variables
```

A service principal needs Contributor on the target subscription or resource
group: it creates a resource group, virtual network and subnet, network security
groups, public IPs, network interfaces, virtual machines and a load balancer.

## 2. Configuration

```yaml
globalSettings:
  adharContext: adhar-mgmt
  defaultHost: platform.example.com
  defaultHttpPort: 80
  defaultHttpsPort: 443
  enableHAMode: true
  email: admin@example.com

providers:
  azure:
    type: azure
    region: eastus
    primary: true
    useAzureCLI: true
    config:
      resource_group: adhar-platform
      location: eastus
      vnet_cidr: 10.5.0.0/16
      subnet_cidr: 10.5.1.0/24
      vm_size: Standard_D8s_v5      # 8 vCPU / 32 GiB
      disk_type: Premium_LRS

environmentTemplates:
  nonprod-defaults:
    clusterConfig:
      - key: autoScale
        value: "true"
    coreServices:
      cilium:
        chart:
          repoURL: https://helm.cilium.io/
          name: cilium
          version: 1.15.7

environments:
  dev:
    type: non-production
    provider: azure
    template: nonprod-defaults
    clusterConfig:
      - key: name
        value: adhar-mgmt
      - key: nodeSize
        value: Standard_D8s_v5
      - key: nodeCount
        value: "3"
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
```

Note that `location` and the provider-level `region` mean the same thing here;
set both to the same value to avoid surprises.


### Managed mode (AKS)

The default is kubeadm on compute. One switch on the provider hands the control
plane to AKS instead; every other operation (`adhar up`, node groups,
`adhar cluster scale`, `adhar upgrade`, `adhar down`) works the same way:

```yaml
providers:
  azure:
    type: azure
    useManagedK8s: true      # or clusterMode: aks
```

What it creates: the resource group (or the configured one), then an AKS
cluster with `networkPlugin: none` (BYO CNI — the platform bootstrap installs
Cilium exactly as on every other cluster), a system-assigned identity and one
agent pool per `nodeGroups` entry (the first is the System pool; names are
squeezed to AKS's 12 lowercase alphanumerics). The kubeconfig is the
cluster-admin credential AKS issues. Teardown deletes the cluster and then the
resource group when Adhar created it.

What compute mode installs on the control plane after the first joins
(`azure/cloud_integration.go`): `kube-system/azure-cloud-provider` (`azure.json`
from the service-principal fields), the `cloud-provider-azure` chart, the
`azuredisk-csi-driver` chart, the `adhar-block` StandardSSD StorageClass, the CSI
startup-taint toleration, and `local-path-provisioner` with the **default**
`adhar-local` StorageClass.

### Storage: why the default class is node-local, not Azure Disk

An Azure VM accepts a fixed number of attached data disks and that number scales
with the VM size — **4 on a `Standard_E2bds_v5`, 8 on a `Standard_E4bds_v5`, 16
only from 8 vCPU up**. The enabled packages ask for roughly **90
PersistentVolumeClaims**. One `adhar-block` volume costs one attach slot, so a
1 + 4 cluster of `Standard_E4bds_v5` workers offers 32 slots against ~90 claims.

This is what that looks like if block storage is the default, and it is worth
recognising because it reads exactly like a CPU shortage and is not:

```
0/5 nodes are available: 1 node(s) had untolerated taint(s),
                         4 node(s) exceed max volume count.
```

A live bring-up stalled at **39 of 75 apps** this way, with 27 claims Pending,
every worker at its attach ceiling — and the unschedulable pods between them
asking for **1.6 CPU cores**. Growing out of it is not possible inside a sane
quota either: 90 attach slots on four nodes needs 16-vCPU machines bought purely
for disk slots, ~64 vCPU for a platform that fits in 18.

So the default StorageClass is `adhar-local`: node-local directories provisioned
by `local-path-provisioner` under `/opt/adhar-local-path`. There is no attach
limit, no per-claim Azure API call, and no quota to raise. Consequences to know:

- **Worker disks are 256 GiB** (`diskSizeGb`), because platform volumes now live
  on the node filesystem alongside the containerd image cache.
- **A node-local volume does not survive losing its node.** Durability comes from
  the layer that owns the data — CNPG streaming replication plus barman base
  backups into the object store — not from the disk.
- **`adhar-block` is still installed**, just not the default. A workload that
  needs a network block device, or a volume larger than a node's filesystem, names
  it: `storageClassName: adhar-block` (RustFS's 150 GiB volume does exactly this,
  and it is the one class that supports online expansion).

## 3. Run it

```bash
az login                                   # if using useAzureCLI
./adhar up -f config.yaml --env dev --dry-run
./adhar up -f config.yaml --env dev

export KUBECONFIG=~/.adhar/clusters/dev/kubeconfig
./adhar get status
```

Teardown:

```bash
./adhar down -f config.yaml --env dev

# Also delete unattached pvc-* managed disks, including ones outside the group
./adhar down -f config.yaml --env dev --purge-orphaned-volumes
```

### What teardown removes, and what it deliberately leaves

Azure makes this simpler than the other clouds: everything a cluster owns lives in
one resource group, so deleting the group takes the VMs, the NICs, the load balancer
the cloud-controller-manager created and the disks the CSI driver created, all at
once. Three cases need more than that:

| Case | Behaviour |
| --- | --- |
| The resource group carries `managedBy=adhar-platform` | the whole group is deleted |
| The group was **not** created by Adhar | the group stays; this cluster's own VMs, NICs, load balancers, public IPs, disks, NSGs and VNet are deleted individually |
| There is no local state for the cluster | the tracker is **rebuilt from Azure** — VMs tagged `managedBy=adhar-platform` whose name matches `<cluster>-master-N` / `-worker-N` — so a cluster created on another machine is still deletable by the CLI |
| `pvc-*` disks outside the group (a CSI driver pointed elsewhere) | only with `--purge-orphaned-volumes`, and only in the cluster's own location |

An attached disk is never deleted, whatever it is called and whatever flags are
passed. `--purge-orphaned-volumes` is opt-in because an unattached CSI disk looks
identical whether its cluster is gone or is being rebuilt.

### Quota preflight

`adhar up` reads the subscription's compute usage for the target location and
refuses before creating anything if the cluster will not fit. Azure meters vCPU
**twice** — a regional total (`cores`) and a per-family limit
(`standardDSv3Family`) — and a create can fail on either, halfway through, having
already made a VNet, an NSG and some of the VMs. Both are checked, and the error
names which limit, how much is in use and how much is needed. A credential without
`Microsoft.Compute/locations/usages` read logs a warning and proceeds.

## 4. What to watch on a first run

- **Data-disk limits per VM size.** This is the Azure analogue of the attach
  limit that bounds the DigitalOcean cluster: each VM size caps how many managed
  disks can attach. Smaller sizes cap low enough to matter for a full catalogue
  (~55 PersistentVolumes). If StatefulSet pods sit `Pending` with
  `exceed max volume count`, raise the VM size or the worker count.
- **vCPU quota per region and per family.** Quota rejections during create look
  like provisioning failures.
- **Resource-group ownership.** The teardown removes what it created. If you
  pointed Adhar at a pre-existing resource group holding other resources, check
  it afterwards rather than assuming.

## 5. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
