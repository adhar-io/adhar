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

| | |
|---|---|
| Provisioning model | kubeadm on Azure VMs (AKS is not the default path) |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Azure DNS, or any zone you point at the load balancer |

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
```

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
