# Civo provider

Adhar on Civo: instances bootstrapped with kubeadm, the same code path that is
live-verified on DigitalOcean. Civo is the closest analogue to DigitalOcean in
this repository — small, flat API, cheap instances.

> **Status: render-verified, not live-verified.** Provider registration, config
> validation and `adhar up --dry-run` pass, and the provisioning code is shared
> with the DigitalOcean path that has run end to end. **No full run on Civo has
> been completed.** See
> [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md#2-verification-status-what-is-proven-where)
> for the matrix, and the [DigitalOcean provider guide](DIGITALOCEAN_PROVIDER.md)
> for everything that is not cloud-specific.

| | |
|---|---|
| Provisioning model | kubeadm on Civo instances (`cluster_mode: compute`), or Civo's managed K3s |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| DNS | Civo DNS, or any zone you point at the load balancer |

## 1. Credentials

```bash
export CIVO_TOKEN="…"
```

```yaml
providers:
  civo:
    type: civo
    region: LON1
    primary: true
    useEnvironment: true        # read CIVO_TOKEN
    # token: "…"                # or inline (avoid in a committed file)
    # tokenFile: /path/to/token
```

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
  civo:
    type: civo
    region: LON1
    primary: true
    useEnvironment: true
    config:
      cluster_mode: compute            # kubeadm on instances; "k3s" uses Civo's managed offering
      network_label: adhar-network
      reuse_existing_network: true
      cidr: 10.7.0.0/16
      size: g3.xlarge                  # check `civo instance size` for your region
      disk_image: ubuntu-noble
      default_node_count: 3
      tags:
        - adhar
        - adhar-mgmt

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
    provider: civo
    template: nonprod-defaults
    clusterConfig:
      - key: name
        value: adhar-mgmt
      - key: nodeSize
        value: g3.xlarge
      - key: nodeCount
        value: "3"
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
```

Instance sizes and image names differ per region. Confirm them with the Civo CLI
before the first run rather than copying these values verbatim.

## 3. Run it

```bash
export CIVO_TOKEN="…"
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

- **Volume attachments per instance.** Civo's cap is the analogue of
  DigitalOcean's 7-per-droplet limit and is the most likely capacity wall for the
  full catalogue (~55 PersistentVolumes). `exceed max volume count` on Pending
  pods is the signal; the autoscaler treats it as a reason to add a worker.
- **Account quotas.** Civo accounts start with modest instance and volume
  quotas; the full profile will exceed a default account.
- **`cluster_mode`.** `compute` is the Adhar-managed kubeadm path this
  documentation describes. `k3s` hands cluster lifecycle to Civo and behaves
  differently; do not mix expectations between the two.

## 5. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
