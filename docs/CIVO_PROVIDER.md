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

## 0. DNS: delegate the platform host (do this first)

`globalSettings.defaultHost` drives every URL, so it must be a subdomain you
delegate to a Civo DNS zone. The platform never creates the zone.

```bash
# 1. a zone for exactly your defaultHost
civo domain create platform.example.com

# 2. read the nameservers Civo lists for it (dashboard → DNS, or)
civo domain list
```

3. At your registrar, in the `example.com` zone, add **NS records named `platform`**
   pointing at Civo's nameservers. The apex stays where it is.

**Certificates are the exception on Civo.** external-dns publishes records through
the Civo API, but cert-manager ships **no Civo DNS-01 solver**, so the platform keeps
its self-signed certificate and browsers warn. Two ways to get a trusted wildcard:

- host the platform zone at a DNS-01-capable provider and point `dnsProvider` there
  (`cloudflare` is the lightest option — one API token), while the cluster itself
  stays on Civo; or
- accept the self-signed certificate and distribute the platform CA.

```yaml
globalSettings:
  defaultHost: platform.example.com
  dnsProvider: cloudflare      # certificates + records; the cluster is still Civo
providers:
  civo:
    token: "…"
    config:
      cloudflareApiToken: "…"  # Zone:Read + DNS:Edit on the zone
```

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

# Also delete unattached pvc-* volumes that no other cluster claims
./adhar down -f config.yaml --env dev --purge-orphaned-volumes
```

### What teardown removes, and what it deliberately leaves

In compute mode the provider creates the instances, firewall, network and SSH key
and removes all four. The controllers running **inside** the cluster create two more
things that no tracker knows about, and both are swept **before** the network is
deleted — a load balancer holds a reference to the network, so leaving one behind
makes the network deletion fail and the next run inherits it:

| Created by | Resource | Removed by teardown |
| --- | --- | --- |
| provider | instances, firewall, network, SSH key | yes |
| cloud-controller-manager | one load balancer per `LoadBalancer` Service | yes |
| CSI driver | volumes attached to this cluster's instances | yes (detached first) |
| CSI driver | unattached `pvc-*` volumes claimed by nobody | only with `--purge-orphaned-volumes` |
| anything | a volume attached elsewhere, or tagged for another cluster | never |

Load balancers are matched by **identity, not by name**: the cluster's instance tag,
one of its instance names, one of its instance IPs, or its Civo cluster id. Prefix
matching was tried and is wrong in a way that matters — a load balancer called
`adhar-mgmt-gateway` is indistinguishable from cluster `adhar` fronting a service
called `mgmt-gateway`, so tearing down `adhar` would have deleted cluster
`adhar-mgmt`'s load balancer and taken its traffic down with it.

### Quota preflight

Civo's limits are per-account and modest by default, so `adhar up` reads them
(`GetQuota`) and refuses before provisioning anything if the cluster will not fit,
naming each exhausted limit: instances, CPU cores, RAM, disk and networks. The
**load-balancer** quota is deliberately not a blocker — an exhausted one leaves the
Gateway Service `Pending` while the platform still comes up on its node ports, so
refusing to build the cluster over it would turn a recoverable inconvenience into a
hard failure. A token that cannot read the quota logs a warning and proceeds.

## 4. What to watch on a first run

- **Volume attachments per instance.** Civo's cap is the analogue of
  DigitalOcean's 7-per-droplet limit and is the most likely capacity wall for the
  full catalogue (~55 PersistentVolumes). `exceed max volume count` on Pending
  pods is the signal; the autoscaler treats it as a reason to add a worker.
- **Account quotas.** Civo accounts start with modest instance and volume
  quotas; the full profile will exceed a default account. `adhar up` now checks
  instances, CPU, RAM, disk and networks before provisioning anything (see the
  quota preflight above) and names what is short, so this surfaces as a refusal
  in the first seconds rather than a half-built cluster ten minutes in.
- **No cloud integration on `compute`, by design.** The Civo
  cloud-controller-manager resolves `CIVO_CLUSTER_ID` against the MANAGED
  Kubernetes API, which a kubeadm cluster on plain instances does not have: it
  nil-panics and never clears `node.cloudprovider.kubernetes.io/uninitialized`.
  The Civo CSI plugin then needs `.spec.providerID`, which only the CCM sets. So
  compute mode runs with no CCM and no CSI: node-local `adhar-local` is the only
  StorageClass, and block storage needs `clusterMode: managed`
  (`civo/cloud_integration.go`).
- **The platform edge comes from the NODES on `compute`.** No CCM means no
  `LoadBalancer` Service, so the Cilium Gateway runs in **host-network mode**:
  Envoy binds :80 and :443 in each node's own network namespace, the firewall
  opens both ports, and external-dns publishes the instances' public IPs as the
  platform's A records (see §4.1). `clusterMode: managed` keeps the ordinary
  Service + load-balancer path.
- **`clusterMode`.** Three values, the same on every cloud: `compute` (the
  Adhar-managed kubeadm path this documentation describes), `managed` (Civo's
  own k3s — Civo owns the control plane, and the node sizes come from a
  different family: `g4s.kube.*`, not `g3.*`) and `provided` (a cluster that
  already exists; Adhar installs onto it and never deletes it). The old
  `cluster_mode: k3s` spelling is rejected rather than mapped.

### 4.1 The edge in compute mode (host-network Gateway)

Compute mode has no load balancer, so the Gateway is reachable on the nodes
themselves. Three things inside Cilium have to agree, and the platform sets all
three (`rewriteCiliumGatewayHostNetwork`) — the first attempt set only the first
and produced a Gateway that reported `Programmed` while nothing listened on 443:

1. `gateway-api-hostnetwork-enabled: "true"` in `cilium-config`,
2. `NET_BIND_SERVICE` on the `cilium-envoy` container — a port below 1024 cannot
   be bound without it,
3. `--keep-cap-net-bind-service` on `cilium-envoy-starter`, which otherwise drops
   that capability before it execs Envoy.

And three outside it:

- the cluster firewall opens **80** and **443** (`gateway-http`/`gateway-https`
  rules; an existing firewall is reconciled, so re-running `adhar up` adds them
  to a cluster built before they existed),
- external-dns publishes the instances' **public** IPs, handed to it as
  `external-dns.alpha.kubernetes.io/target` on the Gateway. Its own status
  carries the PRIVATE addresses, and `--policy=upsert-only` means a private
  record is never retracted — 140 of them had to be deleted through the API on
  2026-10-06,
- CoreDNS resolves `*.<host>` in-cluster to a selector-less shim Service whose
  EndpointSlice lists the nodes' internal addresses. Pods must not be sent to
  the public address: it is NAT'd back to a node and hairpinning generally fails,
  and every oauth2-proxy does OIDC discovery against `https://keycloak.<host>`
  at start-up and exits if it cannot resolve.

TLS is unaffected: the certificate comes from the Civo DNS-01 webhook solver, so
`*.<host>` is a real Let's Encrypt wildcard in both modes.

Trade-off worth knowing: there is one Envoy per node and DNS round-robins
between them, so losing a node removes one A record's worth of capacity until
the next reconcile rewrites the target list. A cloud load balancer
(`clusterMode: managed`) health-checks instead.

## 5. Related

[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) (the verified reference) ·
[Production guide](PRODUCTION.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
