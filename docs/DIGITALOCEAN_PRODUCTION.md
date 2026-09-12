# DigitalOcean — production platform, end to end

**The complete DigitalOcean reference.** Every configuration field, command,
resource and limit for running Adhar on DigitalOcean, as executed on the
verified run of **2026-09-12**: region `blr1`, domain `platform.adhar.io`, HA
mode, the full production package set (76 of 91 packages), created with **3
workers** and autoscaled to **9**. Timings are from that run.

| | |
|---|---|
| Provisioning model | kubeadm on plain Ubuntu droplets (DOKS is opt-in, `useManagedK8s: true`) |
| Kubernetes | v1.37.0 (see [version precedence](#kubernetes-version)) |
| Verified result | 76/76 enabled applications Healthy; Phase 1 and Phase 2 roadmap items live-verified |
| Cost shape | 1 × control plane + 3–10 × `s-8vcpu-16gb`, 1 load balancer, ~55 block volumes |

**Contents** — [0 Prerequisites](#0-prerequisites) ·
[1 Configuration](#1-configuration-configyaml) ·
[2 Create](#2-create-the-platform) · [3 Verify](#3-verify) ·
[4 Day-2](#4-day-2) · [5 Tear down](#5-tear-down) ·
[6 Known limits](#6-known-limits-verified-run) ·
[7 DigitalOcean resources](#7-what-the-platform-creates-in-your-digitalocean-account)

## 0. Prerequisites

**API token.** A DigitalOcean token that can read/write **droplets, VPCs,
firewalls, load balancers, block storage, SSH keys, DNS** and — only if you use
`useManagedK8s` or provision workload clusters through Crossplane —
**Kubernetes**.

> A **scoped** token is fine and is what the verified run used. Scoped tokens
> return `401` on `/v2/account` and `/v2/projects` while working perfectly for
> everything above, so **never judge a token by `doctl account get`** — probe an
> endpoint the platform actually uses (`doctl compute droplet list`). Two more
> `doctl` traps: it **ignores** `DIGITALOCEAN_ACCESS_TOKEN` *and* `-t` when its
> config file has a `context:` (use `doctl --context default -t "$TOKEN"`), and
> `adhar cluster list` needs `--file <config>` or it loads the default config,
> queries no provider and reports a misleading "No clusters found".

```bash
export DIGITALOCEAN_ACCESS_TOKEN="dop_v1_…"   # DIGITALOCEAN_TOKEN also accepted
```

**DNS zone.** The platform domain must be a zone **hosted in DigitalOcean DNS**
(`platform.adhar.io` in the run). The platform publishes one A record per
hostname and solves ACME DNS-01 challenges there. Delegate the zone to
`ns1/2/3.digitalocean.com` at your registrar before you start.

**Local tooling.** `adhar` built from this repository (`make build`), `kubectl`,
and the repository checkout — the GitOps stack is seeded from `platform/stack`.

**Account limits that actually bite.**

| Limit | Value | Consequence |
|---|---|---|
| Block volumes attached per droplet | **7** | The real capacity ceiling. The full profile creates ~55 PersistentVolumes, so it needs **≥ 9 workers**; below that, StatefulSet pods sit Pending with `node(s) exceed max volume count` long before CPU or memory runs out. The autoscaler treats that message as a scale-up trigger. |
| Droplet limit | account-specific | Must cover control plane + workers + any other cluster. A create failing with `422` usually means this; ask DigitalOcean to raise it. |
| Volumes per region | account-specific | ~55–60 per full platform. |

A curated profile (~30 packages) is comfortable on 3–4 workers.

## 1. Configuration (`config.yaml`)

The complete file used for the verified run. Every field is explained below it;
nothing here is optional-but-undocumented.

```yaml
globalSettings:
  adharContext: adhar-mgmt              # kube-context name prefix and Cilium cluster name
  defaultHost: platform.adhar.io        # your DigitalOcean-hosted zone — every URL derives from it
  defaultHttpPort: 80
  defaultHttpsPort: 443                 # 443 behind a cloud LB, so URLs carry no port
  enableHAMode: true                    # ArgoCD 2x + redis-ha 3x, CNPG 2 instances, PDBs
  email: admin@platform.adhar.io        # Let's Encrypt (ACME) account
  # dnsProvider: digitalocean           # derived from the provider; set only to override

providers:
  digitalocean:
    type: digitalocean
    region: blr1
    primary: true                       # the cloud whose Crossplane provider gets installed
    useEnvironment: true                # token from DIGITALOCEAN_ACCESS_TOKEN
    # useManagedK8s: true               # opt-in DOKS instead of kubeadm on droplets
    config:
      reuse_existing_vpc: true          # reuse a VPC with the same CIDR instead of creating one
      vpc_cidr: 10.3.0.0/16             # must not overlap another cluster you intend to mesh with
      droplet_size: s-8vcpu-16gb        # default size for nodes this provider creates
      image: ubuntu-24-04-x64
      tags: [adhar, adhar-mgmt]         # applied to every droplet, on top of the per-cluster tag

environmentTemplates:
  nonprod-defaults:
    clusterConfig: []

environments:
  dev:
    type: non-production
    provider: digitalocean
    template: nonprod-defaults
    clusterConfig:
      - { key: name,      value: adhar-mgmt }     # cluster name; droplets are adhar-<env>-<role>-<n>
      - { key: nodeSize,  value: s-8vcpu-16gb }   # overrides provider droplet_size for this env
      - { key: nodeCount, value: "3" }            # workers at creation; the autoscaler grows it
      # - { key: kubeVersion,  value: v1.37.0 }   # pin the Kubernetes version for this environment
      # - { key: podCIDR,      value: 10.244.0.0/16 }   # must differ per cluster if you mesh them
      # - { key: clusterMeshId, value: "1" }      # Cilium cluster ID, unique per meshed cluster
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
      # everything below is optional; these are the defaults
      nodeGroup: workers
      scaleDownUtilizationThreshold: "50%"
      scaleDownDelay: 10m
      scaleUpCooldown: 3m
```

### Field reference

| Field | Meaning |
|---|---|
| `globalSettings.defaultHost` | The DNS zone. Every hostname is `<app>.<defaultHost>`; the wildcard certificate covers `*.<defaultHost>`. |
| `globalSettings.enableHAMode` | Selects the HA manifest variants: ArgoCD application-controller ×2 + redis-ha ×3 + HPAs/PDBs, CNPG clusters at 2 instances, Crossplane HA. |
| `globalSettings.email` | ACME registration address for Let's Encrypt. |
| `providers.digitalocean.primary` | Only the primary cloud's Crossplane provider packages are installed. Each extra upjet family registers hundreds of CRDs (AWS+Azure+GCP ≈ 3 000) and pushed a single control plane into API-server timeouts. |
| `providers.digitalocean.config.reuse_existing_vpc` | Reuse a VPC whose CIDR matches instead of creating a new one. **Required if you intend to mesh two clusters** (see §4.5). |
| `providers.digitalocean.config.vpc_cidr` | The VPC range. Clusters that will be meshed must share a VPC; clusters that will not should not overlap. |
| `clusterConfig.nodeCount` | Workers at **creation**. Re-running `adhar up` adopts existing droplets by name, so keep it equal to the live count when autoscaling is off; with autoscaling on it is only the starting point. |
| `clusterConfig.kubeVersion` (or `version`) | Pins Kubernetes for this environment. See [version precedence](#kubernetes-version). |
| `clusterConfig.podCIDR` | Pod network for this cluster. Two clusters in a Cilium mesh **must not overlap**. |
| `clusterConfig.clusterMeshId` | Cilium cluster ID (1–255), unique per meshed cluster. Paired with the cluster name it forms the mesh identity. |
| `autoscaling.*` | See [§4.1](#41-autoscaling). `minWorkers`/`maxWorkers` bound the range; the three timing knobs have sane defaults. |

### Kubernetes version

Precedence, highest first:

1. `adhar up --kube-version v1.37.0` — applies to **every** provider, not just Kind.
2. The environment's `kubeVersion` / `version` entry in `clusterConfig`.
3. `globals.DefaultKubernetesVersion` (currently **v1.37.0**).

A worker added later by `adhar cluster scale` or the node autoscaler takes its
version from the **running control plane**, not from any of the above, so a
scaled cluster can never skew.

### Gotchas

- `adhar up -f config.yaml` provisions **every** environment in the file —
  always pass `--env <name>`. Keep only real environments in the file; a sample
  `staging` block with GCP sizing once created an orphan master droplet before
  failing with `422 invalid size`.
- `adhar cluster list` and `adhar cluster delete` need `--file <config>`, or
  they load the default config and query no provider at all.

## 2. Create the platform

```bash
adhar up -f config.yaml --env dev            # add --verbose to see the bootstrap controller
```

What happens (measured on the 3-worker verified run): VPC + firewall + SSH key,
4 droplets created and prepared in parallel, kubeadm init/join, DO
cloud-controller-manager and CSI installed → **cluster serving in ~5 min**; then
the platform bootstrap (Gateway API CRDs → Cilium → Gateway → ArgoCD → Gitea →
Crossplane → stack seed) → **`Completed Environment Provisioning` at ~13 min**.
The remaining packages converge over the following 30–45 min, during which the
autoscaler adds workers as PersistentVolumes exhaust the 7-per-droplet limit
(3 → 9 on this run). Expect the app count to move around while that happens.

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
| Plane    | **Keycloak via Gitea** ("Continue with Gitea" → platform Gitea → its Keycloak auth source); CE v1.4.1 has no generic OIDC provider at all, so this is the federation path it does support. Instance admin (email + password) set up automatically as the break-glass account | `adhar get secrets -p plane` |
| Airbyte  | Airbyte's own login (`global.auth.enabled`); OIDC is Enterprise-only | `adhar get secrets -p airbyte` |
| Metabase | Metabase's own login (setup wizard completed automatically; SSO is paid) | `adhar get secrets -p metabase` |
| Penpot   | **Keycloak OIDC natively** ("Sign in with OpenID"); password login kept | your Keycloak user            |
| ArgoCD   | **Keycloak OIDC natively** ("Log in via Keycloak"; `platform-admin` group → admin) | your Keycloak user      |
| n8n      | n8n's own owner account behind the Keycloak front (host-based at `n8n.<host>`) | set on first visit       |
| LibreDB Studio | **Keycloak OIDC natively** ("Login with SSO", the only way in — the local password account is off); `admin`/`platform-admin`/`platform-engineer` groups map to Studio's admin role | your Keycloak user |

Every other UI is fronted by oauth2-proxy and needs nothing but your Keycloak
user. OIDC inside Airbyte and Metabase is a commercial feature of those
products; the Keycloak front is what enforces platform SSO there. Plane CE
v1.4.1 ships no OIDC or SAML provider either (`grep -ril oidc /code` in
`plane-backend:v1.4.1` returns nothing; the only providers in the image are
email, magic-link, Gitea, GitHub, GitLab and Google), so the platform wires its
**Gitea** provider to the in-cluster Gitea, whose own sign-in is the Keycloak
auth source — a Keycloak login inside Plane without a paid edition.

**Plane showing "No authentication methods available"** is the sign-in screen
failing to read `GET /api/instances/`. Check that payload through the gateway
first — with a Keycloak session, `curl -b <cookies>
https://plane.<host>/api/instances/` must return JSON, not the Plane HTML. If
it returns HTML, the app's oauth2-proxy is routing every path to its `/`
catch-all: oauth2-proxy treats a mount path without a trailing slash as an
**exact** match, so `--upstream=http://plane-api:8000/api` never serves
`/api/instances/`. Every non-root upstream must end in `/` (`hack/gen-sso.sh`
now appends it). If the payload is JSON but every `is_*_enabled` is false, the
`plane-instance-setup` PostSync Job did not complete — it now verifies itself
and exits non-zero rather than reporting success, so read its logs
(`kubectl -n adhar-system logs job/plane-instance-setup`). And if the page
loads but sign-in POSTs fail, check `plane-app-vars.CORS_ALLOWED_ORIGINS`:
Plane derives `CSRF_TRUSTED_ORIGINS` from it, and the chart renders it from
`ingress.appHost` even when the Ingress is disabled.

**Plane's "Continue with Gitea" ending at `error_code=5123`
(`GITEA_OAUTH_PROVIDER_ERROR`)** means the client secret Plane stores no longer
matches Gitea's. Gitea regenerates an OAuth2 application's secret on every
`PATCH /api/v1/user/applications/oauth2/{id}`, and only ever discloses it at
creation. `plane-instance-setup` probes the stored secret on each run (a token
exchange with a bogus code: `"invalid client secret"` vs `"client is not
authorized"`) and re-creates the application when it has gone stale, so a
re-sync repairs it — `kubectl -n adhar-system logs job/plane-instance-setup`
says which branch it took.

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
("Insufficient cpu/memory/pods", "Too many pods", or **"exceed max volume
count"**). That last one is the constraint that actually binds on DigitalOcean:
a droplet accepts only **7 attached block-storage volumes**, so a full
catalogue exhausts attachment slots long before CPU or memory — on a 3-worker
cluster the first thing to go Pending is every StatefulSet with a PVC. A fresh
droplet brings seven more slots, so it is a real capacity signal, unlike a
zone-pinned volume ("volume node affinity conflict"), which another identical
droplet would not fix and which is therefore ignored. Pods blocked by taints,
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

### 4.4 Workload clusters (DOKS through Crossplane)

A `CompositeCluster` XR provisions a **managed DOKS** cluster and registers it
with ArgoCD automatically — this is the self-service path for workload clusters,
distinct from the kubeadm droplets the platform itself runs on. Verified
2026-09-12: apply → `Ready` in ~11 minutes, ArgoCD cluster Secret created, the
thin workload profile (metrics-server, kyverno, kyverno-policies, alloy,
external-secrets) Healthy on it ~3 minutes later.

```bash
kubectl apply -f - <<'EOF'
apiVersion: platform.adhar.io/v1alpha1
kind: CompositeCluster
metadata: {name: wl-blr1, namespace: adhar-scratch}
spec:
  crossplane:
    compositionRef: {name: compositecluster-digitalocean-doks}
  parameters:
    provider: DigitalOcean
    region: blr1
    version: "1.36.3-do.4"          # a slug DO currently offers — they expire
    nodePools: [{name: default, size: s-2vcpu-4gb, count: 2}]
EOF

kubectl -n adhar-scratch get compositecluster wl-blr1 -w
kubectl -n adhar-system get secret cluster-wl-blr1                  # ArgoCD registration
kubectl -n adhar-system get applications -l adhar.io/cluster=wl-blr1
```

The registration Secret carries `argocd.argoproj.io/secret-type: cluster` plus
`adhar.io/cluster`, `adhar.io/dataplane` and `adhar.io/dataplane-mode: composite`,
which is what `adhar-appset-workload.yaml` selects on.

Three things that will bite you:

- **DOKS version slugs expire.** Query them rather than trusting a default:
  `doctl --context default -t "$TOKEN" kubernetes options versions`.
- **Namespaced (`.m`) managed resources have no `spec.deletionPolicy`.** Express
  retention as `managementPolicies: ["Observe","Create","Update","LateInitialize"]`.
- **Deleting the XR removes the ArgoCD registration Secret at the same instant
  as the cluster**, so the workload Applications' `resources-finalizer` can never
  reach the plane and they hang `Terminating`. Clear those finalizers by hand.

Check the control plane is actually able to provision before blaming the XR:

```bash
kubectl get clusterproviderconfigs.digitalocean.m.crossplane.io    # must not be empty
```

An empty list means the ProviderConfigs were never applied (a fresh-cluster bug,
now fixed) and nothing can be provisioned.

### 4.5 Cilium Cluster Mesh between two Adhar clusters

Verified 2026-09-12 between `adhar-mgmt` (id 1, Pod CIDR `10.244.0.0/16`) and
`adhar-test` (id 2, `10.245.0.0/16`): `clustermesh status` green on both sides
and bidirectional traffic over a global service.

Requirements, all mandatory:

| Requirement | Why |
|---|---|
| Distinct `clusterMeshId` **and** cluster name per cluster | Identity; two clusters claiming id 1 cannot mesh. |
| Non-overlapping `podCIDR` | Routing. |
| Shared `cilium-ca` | Mutual TLS between agents and the clustermesh apiserver. Adhar bakes the same CA into its embedded manifest, so this holds by default. |
| **Same VPC** (`reuse_existing_vpc: true` + matching `vpc_cidr`) | On DigitalOcean the platform's Cilium serves node ports only on the **private** NIC, so a peer outside the VPC cannot reach the clustermesh apiserver. The alternative is a LoadBalancer-typed apiserver. |
| Firewall open between peers | The platform opens the VPC CIDR for VXLAN 8472/udp; a tag-scoped rule only covers one cluster and silently drops every packet while control planes still connect. |

Enable the apiserver with `clusterConfig.clusterMeshApiServer: "true"` (and
`clusterMeshServiceType: NodePort|LoadBalancer`), then join the second cluster as
a `DataPlane` (`mode: adopt`) and the controller's mesh phase does the rest;
`MeshJoined` becomes true only after `cilium clustermesh status` converges.

**SPIFFE/SPIRE is deliberately not deployed.** Besides the platform's own reason
(identity retries starved the Cilium operator's Gateway controller), Cilium marks
that path deprecated as of **1.20** for removal in **1.21**, and the platform runs
1.20. What "mesh-ready identity" means today is exactly: a unique cluster name and
id, and one shared CA. Workload-to-workload mutual authentication is **not**
available and is not claimed.

## 5. Tear down

```bash
adhar cluster delete dev --force --file config.yaml --purge-orphaned-volumes
```

Removes, in order: the CCM LoadBalancer(s), every droplet, the block-storage
volumes tagged `adhar-cluster-dev` (the CSI `--do-tag`), the firewall, the VPC,
the SSH key, and the local kubeconfig/state entries.
`--purge-orphaned-volumes` additionally removes unattached `pvc-*` volumes in
the region that carry no other cluster's tag — use it only when the account has
no other Kubernetes cluster in that region.

**Verify it, do not assume.** A teardown that half-succeeds keeps billing:

```bash
doctl --context default -t "$TOKEN" compute droplet list --format Name,Status --no-header
doctl --context default -t "$TOKEN" compute volume list  --format Name,Region --no-header
doctl --context default -t "$TOKEN" compute load-balancer list --format Name,IP --no-header
doctl --context default -t "$TOKEN" compute firewall list --format Name --no-header
doctl --context default -t "$TOKEN" vpcs list --format Name --no-header
```

Two failure modes seen in practice:

- **An orphaned load balancer in a reused VPC.** The delete path used to match
  load balancers by the per-cluster VPC name, so a cluster created with
  `reuse_existing_vpc: true` left its CCM load balancer running (and billing).
  Matching is now by backend droplet id, but check the list anyway.
- **Shared droplet tags.** Deleting a cluster used to remove the shared
  `adhar-role-master` / `adhar-role-worker` tags, stripping them from *other*
  clusters' droplets and breaking their scale, upgrade and autoscaler paths. Only
  the per-cluster tag is removed now.

**DNS records are left in place** — external-dns runs `upsert-only` and never
deletes. Remove them in the DigitalOcean DNS panel when the zone is retired, and
in particular **never leave a manual `*.<host>` wildcard**: it shadows every
hostname without an explicit record, and after a recreate that IP may belong to
someone else, so the browser shows a stranger's certificate.

## 6. Known limits (verified run)

- **Single control plane.** `enableHAMode: true` makes the *platform* highly
  available (ArgoCD ×2, redis-ha ×3, CNPG 2 instances, PDBs) but the Kubernetes
  control plane is still one node; multi-master is the next step. One control
  plane copes with the full profile plus **one** cloud's Crossplane providers; it
  did not cope with all five clouds' providers (≈3 000 CRDs pushed the API server
  into timeouts).
- Kubernetes version, highest precedence first: an explicit
  `adhar up --kube-version v1.36.4` (it applies to every provider, not just
  Kind), then the environment's own `kubeVersion` / `version` entry in
  `clusterConfig`, then the platform default `globals.DefaultKubernetesVersion`
  (currently **v1.37.0**, the same minor Kind runs). A worker added later by
  `adhar cluster scale` or the node autoscaler takes its version from the
  running control plane rather than from any of the above, so a scaled cluster
  can never skew.
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
  Set `DEPLOYMENT=hobby` (values `env`) so `bin/migrate` skips the
  PostHog-Cloud-only `setup_tasks_oauth`/Temporal steps. Two things were still
  open when the verification cluster was torn down: the chart's
  `posthog-plugins` Deployment starts `./bin/plugin-server`, which no longer
  exists in `posthog/posthog:latest` (the plugin server needs its own
  command/image), and `posthog-web` was OOM-killed (exit 137) without a
  memory request — give it 2 GiB. Pinning the app image to a release instead
  of `:latest` would make all of this reproducible.
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

## 7. What the platform creates in your DigitalOcean account

| Resource | Name / shape | Notes |
|---|---|---|
| Droplets | `adhar-<env>-master-<n>`, `adhar-<env>-workers-<n>` | Ubuntu 24.04, size from `nodeSize`/`droplet_size`. Tagged `adhar`, the provider `tags`, a per-cluster tag `adhar-cluster-<env>` and a role tag. |
| VPC | `adhar-<env>-vpc`, or an existing one when `reuse_existing_vpc: true` | CIDR from `vpc_cidr`. Meshed clusters must share it. |
| Firewall | `adhar-<env>-fw` | Opens the Kubernetes API, node ports and — for a mesh — VXLAN 8472/udp across the VPC CIDR. |
| Load balancer | one, created by the DO cloud-controller-manager for the Cilium Gateway Service | Ports 80/443. Its IP is what every DNS record points at. |
| Block volumes | `pvc-<uuid>`, tagged `adhar-cluster-<env>` | ~55 for the full profile. **7 per droplet maximum.** |
| SSH key | `adhar-<env>` | Private half at `~/.adhar/clusters/<env>/id_ed25519`; also mirrored into the cluster as `adhar-cluster-ssh` so the node autoscaler can `kubeadm join` new workers. |
| DNS records | one `A` per platform hostname, plus ACME `TXT` during issuance | Written by external-dns with `--txt-owner-id=adhar-<env>`, policy `upsert-only`. |

Inside the cluster, `adhar up` also writes two objects the autoscaler depends on:
the `adhar-cluster-spec` ConfigMap (provider, region, cluster name, node group,
size, Kubernetes version — no credentials) and the `adhar-cluster-ssh` Secret.
