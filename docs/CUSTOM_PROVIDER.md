# Custom provider — bring your own hosts

Adhar on machines you already own: bare metal, a private cloud, VMs from a
provider Adhar has no integration for. Adhar reaches the hosts over SSH and
bootstraps Kubernetes with kubeadm — the same code path that is live-verified on
DigitalOcean.

> **Status: render-verified, not live-verified.** The kubeadm flow is shared with
> the DigitalOcean path that has run end to end, but no full run against
> bring-your-own hosts has been completed. See
> [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md#2-verification-status-what-is-proven-where).

**Adhar never creates or deletes these machines.** It configures them. That has
two consequences worth stating plainly:

- There is no autoscaling. `minWorkers`/`maxWorkers` have nothing to act on,
  because Adhar cannot buy a host. Size the cluster up front.
- `adhar down` removes the Kubernetes layer and local state; it does not
  decommission your servers.

| | |
|---|---|
| Provisioning model | kubeadm over SSH against hosts you provide |
| Control plane | Exactly one host is supported today |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |

## 1. Host requirements

- Ubuntu 24.04 (what the kubeadm bootstrap is written against).
- A user with passwordless `sudo`, reachable over SSH with a key you hold.
- Hosts able to reach each other on the cluster network, and outbound to the
  internet for image pulls.
- Swap off, and a kernel that supports the Cilium eBPF data path.

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
  custom:
    type: custom
    region: on-prem            # a label; nothing is looked up from it
    primary: true
    config:
      masterIPs:
        - 192.0.2.10           # exactly one control plane is supported
      workerIPs:
        - 192.0.2.11
        - 192.0.2.12
        - 192.0.2.13
      sshUser: ubuntu
      sshKeyPath: ~/.ssh/id_ed25519
      sshPort: 22

environmentTemplates:
  nonprod-defaults:
    clusterConfig:
      - key: autoScale
        value: "false"
    coreServices:
      cilium:
        chart:
          repoURL: https://helm.cilium.io/
          name: cilium
          version: 1.15.7

environments:
  dev:
    type: non-production
    provider: custom
    template: nonprod-defaults
    clusterConfig:
      - key: name
        value: adhar-mgmt
      - key: nodeCount
        value: "3"             # must match the number of workerIPs
```

`nodeIPs` is accepted as a legacy alias for `workerIPs`, and `username` for
`sshUser`. Prefer the current names.

## 3. Run it

```bash
./adhar up -f config.yaml --env dev --dry-run
./adhar up -f config.yaml --env dev

export KUBECONFIG=~/.adhar/clusters/dev/kubeconfig
./adhar get status
```

Because Adhar owns no infrastructure here, the create step is the SSH bootstrap
only: no VPC, no load balancer, no volumes.

## 4. Load balancing, storage and DNS are yours

The cloud providers get these from the cloud. On your own hosts you supply them:

- **Ingress.** The Cilium Gateway listens on node ports 30080/30443. Put your own
  load balancer or DNS in front of the nodes, or use MetalLB.
- **Storage.** There is no CSI driver by default, so anything wanting a
  PersistentVolume stays `Pending`. Install a storage provider that suits your
  hardware before enabling the full catalogue.
- **DNS and TLS.** Point `*.<defaultHost>` at your load balancer. ACME HTTP-01
  needs the zone to resolve publicly; otherwise supply your own certificate.

## 5. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
