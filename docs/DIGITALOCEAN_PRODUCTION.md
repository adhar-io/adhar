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
      - { key: nodeCount, value: "3" }     # start small; the autoscaler grows it
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10                       # 8 was verified; 10 sits at the volume ceiling
```

Notes
- `adhar up -f config.yaml` provisions **every** environment in the file —
  always pass `--env <name>`. Keep only real environments in the file (a
  sample `staging` block with GCP sizing created an orphan master droplet
  before failing with `422 invalid size`).
- `nodeCount` is the worker count the cluster is **created** with; re-running
  `adhar up` adopts existing droplets by name (`adhar-<env>-workers-<n>`), so
  keep it equal to the live count when autoscaling is off. With autoscaling on
  it is only the starting point — see [§4.1 Autoscaling](#41-autoscaling).

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

### 3.1 Verified day-2 drills (2026-09)

Everything below was run against the verified cluster; use it as the acceptance
checklist for a new environment.

```bash
# in-cluster controller + fleet
kubectl -n adhar-system get deploy adhar-controller-manager            # 1/1
adhar get dataplanes                                                   # planes + readiness
adhar get status                                                       # conditions, packages, Data Planes roll-up

# backup / restore drill (Velero -> platform MinIO, prefix velero/)
kubectl -n adhar-system get backupstoragelocation default              # Available
kubectl -n adhar-system create -f - <<'EOF'
apiVersion: velero.io/v1
kind: Backup
metadata: {name: drill, namespace: adhar-system}
spec: {includedNamespaces: [adhar-environments], storageLocation: default, ttl: 24h0m0s}
EOF
kubectl -n adhar-system create -f - <<'EOF'
apiVersion: velero.io/v1
kind: Restore
metadata: {name: drill-restore, namespace: adhar-system}
spec: {backupName: drill, namespaceMapping: {adhar-environments: adhar-restore-drill}}
EOF
kubectl -n adhar-restore-drill get project.kargo.akuity.io               # restored

# environment promotion (Kargo)
git -C environments commit -am "bump" && git push                        # anything under development/
kubectl -n adhar-environments get freight,promotion                     # staging auto-promotes; production waits

# data plane (vcluster on the control plane) + thin profile
kubectl apply -f - <<'EOF'
apiVersion: platform.adhar.io/v1alpha1
kind: DataPlane
metadata: {name: dp-local, labels: {adhar.io/plane: data}}
spec: {infrastructure: {mode: vcluster}, profile: standard, placement: {labels: {tier: local}}}
EOF
kubectl get dataplane dp-local                                          # Ready in ~3 min
kubectl -n adhar-system get applications -l adhar.io/cluster=dp-local   # kyverno, alloy, external-secrets Healthy

# reconstructability drill (Crossplane Operations)
kubectl get cronoperation adhar-reconstructability-drill -o jsonpath='{.spec.operationTemplate}' | \
  jq '{apiVersion:"ops.crossplane.io/v1alpha1",kind:"Operation",metadata:{generateName:"drill-manual-"},spec:.spec}' | kubectl create -f -
kubectl -n adhar-system get compositecluster drill-reconstructability   # Ready in seconds
kubectl get operations -o jsonpath='{range .items[*]}{.status.pipeline[*].output.verdict}{"\n"}{end}'   # pass
```

### 3.2 Application logins

Every platform UI sits behind Keycloak (oauth2-proxy front). A few apps have
no OIDC in their open-source edition and keep their own login behind that
gate; the platform provisions their first admin for you and `adhar get
secrets -p <app>` prints the credentials:

| App      | Inside the app                                              | Credentials                         |
|----------|-------------------------------------------------------------|-------------------------------------|
| Coder    | **Keycloak OIDC natively** ("Sign in with Keycloak"); break-glass owner `adhar-admin` | `adhar get secrets -p coder` |
| Kargo    | Keycloak OIDC natively (UI + CLI)                           | your Keycloak user                  |
| Plane    | email + password (instance set up automatically)            | `adhar get secrets -p plane`        |
| Airbyte  | Airbyte's own login (`global.auth.enabled`); OIDC is Enterprise-only | `adhar get secrets -p airbyte` |
| Metabase | Metabase's own login (setup wizard completed automatically; SSO is paid) | `adhar get secrets -p metabase` |
| Penpot   | **Keycloak OIDC natively** ("Sign in with OpenID"); password login kept | your Keycloak user            |
| ArgoCD   | **Keycloak OIDC natively** ("Log in via Keycloak"; `platform-admin` group → admin) | your Keycloak user      |
| n8n      | n8n's own owner account behind the Keycloak front (host-based at `n8n.<host>`) | set on first visit       |

Every other UI is fronted by oauth2-proxy and needs nothing but your Keycloak
user. OIDC inside Plane, Airbyte and Metabase is a commercial feature of those
products; the Keycloak front is what enforces platform SSO there.

**ArgoCD "Invalid redirect URL" on Keycloak login** meant ArgoCD's own
external URL was wrong: the HA install variant shipped
`argocd-cm.url: https://argocd.example.com`, and ArgoCD derives the OIDC
`redirect_uri` from it. Both install variants now template
`https://argocd.<host>`; on a running cluster patch `argocd-cm` and restart
`argo-cd-argocd-server`.

**If every SSO login suddenly fails with 503**, check the shared MinIO
before anything else: `kubectl -n adhar-system exec deploy/minio -- df -h
/export`. A full MinIO stops CNPG WAL archiving on every platform database
(`ContinuousArchiving=False`, `barman-cloud-wal-archive: exit status 4`,
underneath it `no space left on device`); WAL then piles up on the Postgres
volumes until CNPG reports `Not enough disk space` and Keycloak's database
stops. The MinIO volume now ships at 100 GiB with 7-day backup retention
(Keycloak alone archives ~2 GiB of WAL a day); expand it live with
`kubectl patch pvc minio -p '{"spec":{"resources":{"requests":{"storage":"200Gi"}}}}'`
and the archivers recover on their own. Then check Keycloak's database: `kubectl -n adhar-system get cluster keycloak-db` — its volume was
1 GiB and filled with retained WAL the moment archiving to MinIO hiccupped.
Platform databases now start at 5 GiB and CNPG grows them online
(`resizeInUseVolumes`).

**DNS hygiene.** external-dns creates one A record per platform hostname and
never touches records it did not create. A manually created wildcard
(`*.<host>`) pointing at an old load-balancer IP will shadow any hostname that
has no explicit record — after a `--recreate` that IP may already belong to
someone else, and the browser then shows a certificate for a stranger's domain.
Do not keep a wildcard record in the platform zone.

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

`adhar upgrade` converges the foundation (Cilium, Gateway, ArgoCD, Gitea,
CNPG in HA mode, Crossplane **and its control-plane configuration** — XRDs,
Compositions, Functions, provider packages, Operations are re-applied on every
upgrade), then diffs and pushes the stack.

**Who owns what on a running platform.** Components that are both a bootstrap
manifest and a stack package (Crossplane is one) are owned by the *stack
package* once ArgoCD runs: `selfHeal` re-applies the package's rendering over
anything else, so tune them in `platform/stack/packages/<pkg>/values.yaml`,
not in the embedded manifest. The in-cluster controller
(`adhar-controller-manager`) runs the **released** image matching the CLI
version (`ghcr.io/adhar-io/adhar:<version>`; development builds track
`:latest`) and keeps reconciling the platform with *that* release's manifests
— to exercise unreleased foundation/controller code on a cloud cluster, park
it (`kubectl -n adhar-system scale deploy adhar-controller-manager --replicas=0`)
and run `adhar controller --platform-name <env>` from the checkout, or push a
dev image to the platform Harbor and point the Deployment at it.

**Crossplane cloud providers.** Only the platform's own cloud gets its
provider packages and ProviderConfig (DigitalOcean →
`provider-upjet-digitalocean`); the CLI materialises the API token into the
`digitalocean-credentials` Secret the ProviderConfig reads. Every extra upjet
family registers hundreds of CRDs (AWS+Azure+GCP together ≈3 000), which on a
single control-plane node pushed the API server into timeouts for no benefit.
Opt a second cloud in by applying its entries from
`platform/controlplane/configuration/providers/cloud/provider-packages.yaml`
and its `<cloud>-providerconfig.yaml`, plus a `<cloud>-credentials` Secret in
`adhar-system`. Packages come from `xpkg.crossplane.io/crossplane-contrib`
— the Upbound-published `upbound/provider-*:v2.x` packages refuse to start on
vanilla Crossplane (UXP-only).

### 4.1 Autoscaling

There is no need to size the cluster for its peak up front. With
`environments[].autoscaling.enabled: true` the cluster is created at
`nodeCount` workers and the platform's own node autoscaler — a controller in
`adhar-controller-manager`, not the upstream cluster-autoscaler, which has no
provider for self-managed kubeadm clusters on raw droplets — moves the worker
count between `minWorkers` and `maxWorkers` on demand.

```yaml
environments:
  dev:
    clusterConfig:
      - { key: nodeCount, value: "3" }     # created with 3 workers
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
      # Everything below is optional; these are the defaults.
      nodeGroup: workers
      scaleDownUtilizationThreshold: "50%"
      scaleDownDelay: 10m
      scaleUpCooldown: 3m
```

**Scale up** — one droplet at a time, when a pod is `Pending` with
`PodScheduled=False/Unschedulable` *and* the scheduler blamed capacity
("Insufficient cpu/memory/pods", "Too many pods"). Pods blocked by taints,
node affinity that no current worker satisfies, or unbound volumes are
ignored: another identical droplet would not schedule them. The new node is
created, prepared and `kubeadm join`ed by the same code path
`adhar cluster scale` uses, so it is identical to a node from `adhar up`.
Guards: `maxWorkers`, and `scaleUpCooldown` (a join takes minutes — the
cooldown stops the queue of pending pods from buying a droplet per tick).

**Scale down** — one node at a time, when cluster-wide *requested* CPU **and**
memory (pod requests on workers ÷ worker allocatable) stay under
`scaleDownUtilizationThreshold` for `scaleDownDelay`. The emptiest worker is
picked (DaemonSet pods do not count as load), cordoned and drained through the
eviction API so PodDisruptionBudgets are honoured (5-minute timeout), then the
droplet is deleted and the Node object removed. A node is skipped when it
hosts a pod with a ReadWriteOnce volume (the block-storage volume is attached
to that droplet) or a pod no controller would recreate. Never below
`minWorkers`, and never while a node was added or removed inside the last
`scaleDownDelay`.

Watch it:

```bash
kubectl -n adhar-system get adharplatform dev -o jsonpath='{.status.autoscaling}' | jq
kubectl -n adhar-system get events --field-selector reason=ScalingUp,reason=ScalingDown
```

`status.autoscaling` carries `workers`, `lastScaleUp`, `lastScaleDown`,
`underutilizedSince` and a one-line `lastReason` for the most recent tick
(including the "did nothing because…" ones). Every action is also an Event on
the `AdharPlatform`.

Two bootstrap-time objects make this possible on a self-managed cluster, both
written by `adhar up`: the `adhar-cluster-spec` ConfigMap (provider, region,
cluster name, node group, instance size — no credentials) and the
`adhar-cluster-ssh` Secret, a copy of `~/.adhar/clusters/<env>/id_ed25519`.
Joining a worker means running `kubeadm token create` on the control plane
over SSH, so the in-cluster controller needs that key; the cloud API token is
the `digitalocean-credentials` Secret that already exists for Crossplane. A
cluster bootstrapped before those objects existed (or from another machine)
reports the reason in `status.autoscaling.lastReason` and keeps a fixed size —
re-run `adhar up` against it, or scale by hand with `adhar cluster scale`.

**Manual scaling still works** and is not fought over: `adhar cluster scale`
changes the count directly, and the autoscaler's next tick simply observes the
new count (keep it inside `min`/`max`, or the autoscaler will pull it back).

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

### 4.2 One namespace for the platform

Every platform package installs into `adhar-system` — including the former
hold-outs (Kubeflow Pipelines, cosign, OpenFunction), whose charts and
kustomize bases are re-rendered for it at generation time. The only namespaces
the platform creates elsewhere are Kargo `Project` namespaces
(`adhar-environments`; Kargo requires one per Project), data-plane vclusters
(`dp-<name>`), and `kpack-system` for buildpack: kpack and cosign both hardcode
`Secret/webhook-certs` in their binaries, and two knative webhooks cannot share
one Secret. Application environments you create get their own
namespaces; platform components never do (ADR-0011).

### 4.3 Observability of the platform itself

The **Adhar Platform** Grafana dashboard (folder *Platform*) is the single
pane: overview tiles, capacity and nodes, GitOps and delivery, identity and
edge, data services, the observability pipeline, security and policy,
workloads, cost. Every panel is backed by a metric that exists on a verified
run. The **CloudNativePG** dashboard lists all platform databases in one table
(instances, primary, lag, WAL archiving, backup age, size, connections, TPS)
with a multi-select `cluster` variable. Every platform CNPG Cluster now sets
`monitoring.enablePodMonitor: true` — without it no `cnpg_*` series exist and
the Postgres rows stay empty.

## 6. Known limits (verified run)

- Single control plane (HA control planes are the next step). A `dev` control
  plane on `s-4vcpu-8gb` copes with the full profile + one cloud's providers;
  it did **not** cope with all five clouds' providers (see §4).
- Kubernetes: kubeadm clusters follow the platform default
  (`globals.DefaultKubernetesVersion`, v1.37 — the same minor Kind runs); pin
  `kubernetesVersion` on the environment to stay behind.
- First boot ordering: every package's ServiceMonitor/PodMonitor/PrometheusRule
  is applied in sync-wave 10 so a package's core resources and PostSync hooks
  (Keycloak client provisioning, ClusterIssuers, MinIO credentials) land before
  the Prometheus Operator CRDs exist; ArgoCD's unlimited retries pick the
  monitors up minutes later. Expect ~20 apps to show Progressing/Degraded for
  the first 10–15 minutes and then converge on their own.
- Nodes are hardened by the kubeadm prep script (static upstream resolvers,
  inotify/file-descriptor and systemd task limits); before that, a wedged
  `systemd-resolved` stub failed every image pull cluster-wide and dense nodes
  refused to create container cgroups.
- Loki ingests through MinIO (`loki` bucket) with raised ingestion limits
  (32 MB/s, 64 MB burst, 8 MB per stream); the chart default of 4 MB/s dropped
  lines from the busier namespaces ("ingestion rate limit exceeded").
- PostHog runs `posthog/posthog:latest`, which tracks PostHog master; its
  ClickHouse migrations need the ClickHouse release master runs (26.6.x — the
  chart default 23.9 stops after three migrations), Kafka *named collections*
  (`msk_cluster`, `warpstream_*`, pointed at the platform Kafka) and a
  `named_collection_control` grant for the chart's `admin` user, plus every
  named cluster the migrations address declared in `remote_servers`. All of it
  lives in `application/posthog/values.yaml`; `bin/migrate` runs the ClickHouse
  step in the background, so its error is only visible by running
  `python manage.py migrate_clickhouse` by hand in a copy of the migrate Job.
- The console image polls the API with HTTP/2 and logs
  `upstream error … GOAWAY` when the API server closes idle connections; the
  UI recovers on the next poll. Fix belongs to the console image (retry on
  GOAWAY / HTTP/1.1 keep-alive).
- Full profile is memory-heavy: 8 × `s-8vcpu-16gb` sits at ~55 % requested
  memory but ~130 % of limits; the eBPF agents (Beyla, Tetragon, Pixie) are the
  first SystemOOM victims when a node saturates, which flaps the node NotReady
  and takes whatever stateful pod lives there (Gitea's Valkey cache in the run)
  with it. Beyla now skips Go-specific uprobes and excludes the
  observability plumbing (2 GiB cap); prefer 10 workers.
