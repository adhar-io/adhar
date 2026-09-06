# DigitalOcean — production platform, end to end

The exact configuration and commands used for the live verification on
2026-09-06/07 (region `blr1`, domain `platform.adhar.io`, full production
package set, 1 control-plane + 8 workers). Everything below was executed as
written; timings are from that run.

## 0. Prerequisites

- A DigitalOcean API token with read/write scope (droplets, VPC, firewall,
  load balancers, block storage, **DNS**).
- A DNS zone for your platform domain **hosted in DigitalOcean DNS**
  (`platform.adhar.io` in the run): the platform publishes records and solves
  ACME DNS-01 challenges there. Delegate the zone to `ns1/2/3.digitalocean.com`
  at your registrar.
- `adhar` built from this repository (`make build`), `kubectl`, and the
  repository checkout (the stack is seeded from `platform/stack`).
- Account limits: DigitalOcean allows **7 attached block volumes per
  droplet**; the full production profile creates ~55–60 PersistentVolumes, so
  it needs **≥ 10 workers** (8 workers hit the ceiling during the verified run:
  a rescheduled StatefulSet pod stayed Pending with "node(s) exceed max volume
  count"). The account's droplet limit must cover master + workers (+ any
  other clusters); ask DigitalOcean to raise it if a droplet create fails with
  `422`. A curated profile (~30 packages) is comfortable on 3–4 workers.

```bash
export DIGITALOCEAN_TOKEN="dop_v1_…"     # or DIGITALOCEAN_ACCESS_TOKEN (doctl)
```

## 1. Configuration (`config.yaml`)

```yaml
globalSettings:
  adharContext: adhar
  defaultHost: platform.adhar.io          # your DigitalOcean-hosted zone
  defaultHttpPort: 80
  defaultHttpsPort: 443
  enableHAMode: false
  email: admin@platform.adhar.io          # Let's Encrypt (ACME) account
  # dnsProvider: digitalocean             # derived from the provider; set only to override

providers:
  digitalocean:
    type: digitalocean
    region: blr1
    useEnvironment: true                  # token from DIGITALOCEAN_TOKEN
    # useManagedK8s: true                 # opt-in DOKS instead of kubeadm on droplets

environmentTemplates:
  nonprod-defaults:
    clusterConfig: []

environments:
  dev:
    provider: digitalocean
    template: nonprod-defaults
    clusterConfig:
      - { key: name,      value: adhar-mgmt }
      - { key: nodeSize,  value: s-8vcpu-16gb }
      - { key: nodeCount, value: "10" }   # 8 was verified but sits at the volume ceiling
```

Notes
- `adhar up -f config.yaml` provisions **every** environment in the file —
  always pass `--env <name>`. Keep only real environments in the file (a
  sample `staging` block with GCP sizing created an orphan master droplet
  before failing with `422 invalid size`).
- `nodeCount` is the desired worker count; re-running `adhar up` adopts
  existing droplets by name (`adhar-<env>-workers-<n>`), so keep it equal to the
  live count after scaling.

## 2. Create the platform

```bash
adhar up -f config.yaml --env dev            # add --verbose to see the bootstrap controller
```

What happens (measured): VPC + firewall + SSH key, 9 droplets created and
prepared in parallel, kubeadm init/join, DO cloud-controller-manager and CSI
installed → **cluster serving in ~8 min**; then the platform bootstrap
(Gateway API CRDs → Cilium → Gateway → ArgoCD → Gitea → Crossplane → stack
seed) → **console reachable ~10 min later**; the remaining ~80 packages
converge over the following 15–25 min.

During bootstrap the CLI:
- saves the kubeconfig to `~/.adhar/clusters/dev/kubeconfig` and merges it into
  `~/.kube/config` as context `adhar-dev` (made current);
- creates the `adhar-dns-provider` Secret (DO token) for external-dns and
  cert-manager;
- renders the stack for the domain (`*.yaml.tmpl` → ClusterIssuers with the
  DigitalOcean DNS-01 solver, external-dns with
  `--provider=digitalocean --domain-filter=platform.adhar.io
  --txt-owner-id=adhar-dev`) and seeds it into Gitea;
- applies the cloud Gateway (`Service` type LoadBalancer → a DigitalOcean LB
  on 80/443, wildcard HTTPS listener annotated
  `cert-manager.io/cluster-issuer: adhar-letsencrypt-dns`).

## 3. Verify

```bash
kubectl config current-context                          # adhar-dev
kubectl get nodes                                       # 9 × Ready
kubectl -n adhar-system get gateway adhar-gateway       # ADDRESS = LB IP
dig +short console.platform.adhar.io                    # = LB IP (external-dns)
kubectl get clusterissuer                               # adhar-letsencrypt-dns True
kubectl -n adhar-system get certificate adhar-cert      # READY True (~3 min)
curl -sI https://console.platform.adhar.io | head -1    # HTTP/2 200, trusted chain
adhar get status                                        # per-package health
adhar get secrets                                       # console/Keycloak/ArgoCD/Gitea credentials
```

Log in at `https://console.platform.adhar.io` with the Keycloak `user1`
(platform admin) credentials from `adhar get secrets`. Every other UI
(`argocd.`, `gitea.`, `grafana.`, `harbor.`, `nexus.`, …`.platform.adhar.io`)
uses the same SSO.

## 4. Day-2

```bash
adhar cluster scale dev --workers 10 -p digitalocean -f config.yaml   # join / drain
adhar upgrade --yes                        # re-render foundation + push changed stack
adhar upgrade --diff-only                  # preview what would be pushed
adhar up -f config.yaml --env dev --recreate --force   # delete + fresh cluster
```

`credential-rotation` (enabled in production) rotates the Gitea admin
password; the platform reads it from the `gitea-credential` Secret everywhere
(`adhar get secrets` shows the current one).

## 5. Tear down

```bash
adhar cluster delete dev --force --file config.yaml --purge-orphaned-volumes
```

Removes droplets, the CCM LoadBalancer(s) in the cluster VPC, the block-storage
volumes tagged `adhar-cluster-dev` (CSI `--do-tag`), the firewall, the VPC, the
SSH key and the local kubeconfig/state. `--purge-orphaned-volumes` also removes
unattached `pvc-*` volumes in the region that no other cluster tag claims —
use it when the account has no other Kubernetes cluster in that region.
DNS records created by external-dns are left in place (policy `upsert-only`);
delete them in the DigitalOcean DNS panel if the zone is retired.

## 6. Known limits (verified run)

- Single control plane (HA control planes are the next step).
- The console image polls the API with HTTP/2 and logs
  `upstream error … GOAWAY` when the API server closes idle connections; the
  UI recovers on the next poll. Fix belongs to the console image (retry on
  GOAWAY / HTTP/1.1 keep-alive).
- Full profile is memory-heavy: 8 × `s-8vcpu-16gb` sits at ~55 % requested
  memory but ~130 % of limits; the eBPF agents (Beyla, Tetragon, Pixie) are the
  first SystemOOM victims when a node saturates, which flaps the node NotReady
  and takes whatever stateful pod lives there (Gitea's Valkey cache in the run)
  with it. Beyla now ships with a 1 GiB memory limit; prefer 10 workers.
