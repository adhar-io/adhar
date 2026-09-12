# Adhar Production Guide

**What this is for:** running Adhar as production infrastructure — topology and
sizing, Kubernetes versioning, node autoscaling, security hardening, the edge,
backup and disaster recovery, upgrades and day-2 operations.

Read [Architecture](ARCHITECTURE.md) first; this guide assumes its terminology
(topologies T1/T2/T3, bootstrap vs GitOps phases). Reaching a provisioned
cluster is [PRODUCTION_ACCESS.md](PRODUCTION_ACCESS.md); per-cloud setup is
[PROVIDER_GUIDE.md](PROVIDER_GUIDE.md); when something breaks, go straight to
[TROUBLESHOOTING.md](TROUBLESHOOTING.md).

> ⚠️ Adhar is in active development (v0.1.x). Phases 1 and 2 are live-verified
> on DigitalOcean (see [PROVIDER_GUIDE §2](PROVIDER_GUIDE.md#2-verification-status-what-is-proven-where));
> validate each control in your own environment, and see the
> [Roadmap](ROADMAP.md) for implemented vs planned.

---

## Contents

1. [Choosing a topology](#1-choosing-a-topology)
2. [Sizing and HA](#2-sizing-and-ha)
3. [Kubernetes version](#3-kubernetes-version)
4. [Node autoscaling](#4-node-autoscaling)
5. [Security hardening](#5-security-hardening)
6. [The edge: DNS, TLS, load balancing](#6-the-edge-dns-tls-load-balancing)
7. [Cluster Mesh (T3)](#7-cluster-mesh-t3)
8. [Backup and disaster recovery](#8-backup-and-disaster-recovery)
9. [Upgrades](#9-upgrades)
10. [Day-2 operations](#10-day-2-operations)

---

## 1. Choosing a topology

| Topology | When | Trade-off |
| --- | --- | --- |
| **T2 — single production cluster** | One team, one or two environments, getting to production fast | Platform and workloads share a failure domain and upgrade window |
| **T3 — management + workload clusters** | Multiple environments/teams, compliance boundaries, cluster-level blast-radius isolation | More clusters to pay for and operate (the management cluster automates most of it) |

Start with T2; the move to T3 is additive (provision workload clusters via a
`CompositeCluster` XR, shift apps over) because all platform state is already in
Git. The T3 path is live-verified: a `CompositeCluster` provisioned a real DOKS
cluster in ~11 minutes, auto-registered it with ArgoCD (labels
`adhar.io/cluster`, `adhar.io/dataplane`, `adhar.io/dataplane-mode`), the thin
workload profile landed 5/5 Healthy on it, and teardown left no paid resources.

## 2. Sizing and HA

### Management / platform cluster baseline

| Component | Minimum production shape |
| --- | --- |
| Control plane | 3 nodes (managed control planes: rely on the provider SLA) |
| Platform node pool | 3× 4 vCPU / 16 GB across ≥ 2 zones, autoscaling enabled |
| ArgoCD | ≥ 2 replicas for server/repo-server; HA Redis |
| Gitea | ≥ 2 replicas; **external PostgreSQL via CNPG** (3 instances, streaming replication); RWX or object-backed storage |
| Gateway (Cilium Envoy) | ≥ 2 replicas behind the cloud LB; PodDisruptionBudget |
| Keycloak | ≥ 2 replicas + CNPG PostgreSQL |
| Observability | Mimir/Loki/Tempo on object storage; retention by policy, not disk size |

Set `globalSettings.enableHAMode: true` (or pass `--ha` to `adhar up`) so the
environment templates apply replicas, PDBs and topology-spread constraints.
Anti-affinity across zones for every stateful service.

> HA changes which foundation manifests are used. Two defects only ever
> appeared in the HA variant — see
> [TROUBLESHOOTING §3.4–3.5](TROUBLESHOOTING.md#34-ha-only-invalid-redirect-url-on-keycloak-login).
> After any HA bootstrap, log into ArgoCD **through Keycloak** and confirm you
> see the full application list.

### What actually constrains the size

The package catalogue is **91 packages** (94 ApplicationSet elements — three
packages ship more than one variant), of which **76 are enabled in the
production profile** and 32 in the curated local core. Every package installs
into **`adhar-system`**; the single exception is `buildpack` (kpack), which
keeps `kpack-system`
([ADR-0011](adr/0011-shared-platform-namespace.md); the reason is in
[TROUBLESHOOTING §6.4](TROUBLESHOOTING.md#64-kpack-webhook-secret-webhook-certs-not-found)).
Kargo project namespaces (`adhar-environments`), data-plane vclusters
(`dp-<name>`) and application environments get their own namespaces.

The full profile creates roughly **55–60 PersistentVolumes and ~300 pods**. On
most clouds the binding constraint is *not* CPU — it is per-node volume
attachment limits. On DigitalOcean that is **7 block volumes per droplet**, so
the full catalogue needs **≥ 10 workers regardless of droplet size**
([TROUBLESHOOTING §5.1](TROUBLESHOOTING.md#51-exceed-max-volume-count--the-digitalocean-7-volume-wall)).

### Workload clusters (T3)

Keep them thin: Cilium, Alloy collectors, Kyverno, Falco, plus your apps.
Everything multi-tenant and stateful stays on the management cluster. A workload
cluster should be **fully reconstructable in under an hour** from Git +
Crossplane — test it ([§8.4](#84-practice)).

## 3. Kubernetes version

The platform default is `globals.DefaultKubernetesVersion` = **v1.37.0**.
Precedence, highest first:

| Precedence | Where |
| --- | --- |
| 1. `adhar up --kube-version v1.37.0` | CLI flag — applies to **every** provider, not just Kind |
| 2. `kubeVersion` / `version` in the environment's `clusterConfig` | `config.yaml` |
| 3. `globals.DefaultKubernetesVersion` | Compiled in (v1.37.0) |

Only an *explicitly passed* flag overrides what the environment configured, so a
newer CLI cannot silently repin your clusters.

**A node added later cannot skew the cluster.** Both `adhar cluster scale` and
the node autoscaler ask the **running control plane** what it is on and prepare
the new worker against that minor stream.

Upgrading an existing cluster is a separate operation — see [§9](#9-upgrades).
Mechanics: [PROVIDER_GUIDE §5](PROVIDER_GUIDE.md#5-kubernetes-version).

## 4. Node autoscaling

Verified live in both directions on DigitalOcean: a cluster created with 3
workers and `maxWorkers: 10` grew to 9 while the production catalogue synced
(pending pods 120 → 8), then removed a node once it settled
(`cluster idle for 1h40m46s (cpu=21% memory=27%); removing adhar-dev-workers-9`).

Turn it on per environment (or on an environment template, which every
environment using it inherits). The cluster is still *created* with the
environment's `nodeCount`; autoscaling turns that into a range.

```yaml
environments:
  production:
    provider: digitalocean
    autoscaling:
      enabled: true
      minWorkers: 3
      maxWorkers: 10
```

**Production posture, in short:**

- Set `maxWorkers` as a deliberate **spend ceiling** — it is the only limit the
  autoscaler will not cross, and on DigitalOcean the catalogue needs ≥ 10.
- Set `minWorkers` to your HA floor so an idle night cannot drop you below the
  replica counts in [§2](#2-sizing-and-ha).
- Scale-up triggers on pods unschedulable *for capacity* — including
  `exceed max volume count`, which is what you will actually hit first on
  DigitalOcean.
- **Two guards refuse a scale-down**, both observed live: a recent move in
  either direction (one node per `scaleDownDelay` window, so the cluster
  settles), and a node hosting a pod with a **ReadWriteOnce** volume — that
  volume is attached to that machine and cannot follow the pod.
- Alert on `.status.autoscaling.lastReason` sitting at
  `… but maxWorkers=N reached`: that is the platform telling you it wanted
  capacity it is not allowed to buy.

Every field, its default, and the full decision order are in
[PROVIDER_GUIDE §3](PROVIDER_GUIDE.md#3-node-autoscaling); decoding a `lastReason`
is [TROUBLESHOOTING §5.3](TROUBLESHOOTING.md#53-the-autoscaler-is-not-scaling).
Manual scaling uses the same code path:

```bash
adhar cluster scale <cluster> --workers 10 --node-group workers -p digitalocean -f config.yaml
```

## 5. Security hardening

### Identity and access

- [ ] Keycloak as OIDC provider for ArgoCD, Gitea, Grafana, Console; humans never use local admin accounts after bootstrap
- [ ] Rotate the bootstrap credentials (`gitea_admin`, ArgoCD `admin`) as soon as SSO login is verified — enable the `credential-rotation` package (on by default in the production set). It rotates both to random values and stores break-glass copies in the secrets backend at `secret/adhar/bootstrap-credentials`. Delete the `bootstrap-credentials-rotated` marker Secret to rotate again
- [ ] Kubernetes API via OIDC group claims; RBAC per team namespace; no cluster-admin for humans in daily work
- [ ] ArgoCD RBAC actually maps your Keycloak groups — verify by logging in as a non-admin and confirming the application list is not empty
- [ ] Cloud credentials to Crossplane via workload identity (IRSA / Workload Identity / Managed Identity) — never long-lived keys in Secrets

### Secrets

- [ ] **OpenBao is the production secrets backend** (`security/openbao`) — the MPL-2.0, Linux Foundation fork of Vault, enabled in the production profile. The `vault` package is wired as the alternative and ships **disabled**; exactly one backend may be enabled ([CONFLICTS.md](../platform/stack/packages/CONFLICTS.md))
- [ ] Nothing downstream changes with the switch: the External Secrets `ClusterSecretStore` keeps the name **`vault`**, uses the `vault:` provider, and a compatibility `Service/vault` in `adhar-system` selects the OpenBao pods, so consumers that address `vault.adhar-system.svc.cluster.local:8200` by DNS (the Console's Secrets page, the credential-rotation Job) work untouched
- [ ] Harden with auto-unseal via cloud KMS (`seal "awskms"` / `"azurekeyvault"` / `"gcpckms"` in the package `values.yaml`) so no unseal material stays in-cluster — the shipped bootstrap Job otherwise keeps the unseal key and root token in the `openbao-keys` Secret
- [ ] No secrets in Git, ever — enforce with Gitea push hooks / secret scanning
- [ ] etcd encryption at rest enabled (managed offerings: verify the provider default)

### Network

- [ ] Default-deny Cilium network policies in all workload namespaces; platform namespaces get scoped allow-rules (author them from Hubble flow data, namespace by namespace)
- [ ] WireGuard transparent encryption for node-to-node traffic
- [ ] Gateway is the only public entry; API server access restricted to VPN/allowlist; Hubble UI, ArgoCD, Gitea behind SSO
- [ ] `external-dns` scoped to the platform's DNS zone only

### Workloads and supply chain

- [ ] Kyverno policies in `Enforce`: Pod Security **restricted** baseline, no `:latest`, resource requests required, disallow privileged/hostPath
- [ ] Harbor as the only allowed registry (Kyverno image allowlist); Trivy scan gates on severity; Cosign signature verification for platform and app images
- [ ] The three supply-chain policies ship as `security/supply-chain-policies`, split into `manifests/audit` (enabled) and `manifests/enforce` (disabled). **Audit mode can only tell you what it would have done.** Before switching to Enforce, prove the machinery works on *this* cluster with `hack/verify-supply-chain.sh` — it signs a throwaway image with a throwaway key exactly as the Tekton `cosign-sign` Task does, applies a scratch-namespace Enforce policy with `failurePolicy: Fail`, and asserts unsigned → denied / signed → admitted, then cleans up
- [ ] Velero backup namespaces/PVs labeled and included ([§8](#8-backup-and-disaster-recovery))

## 6. The edge: DNS, TLS, load balancing

On cloud and on-prem providers the platform Gateway deploys in its production
variant automatically: a LoadBalancer Service (instead of Kind's pinned
NodePorts), an HTTPS listener carrying the platform wildcard hostname, and
certificate management delegated to cert-manager via the
`cert-manager.io/cluster-issuer` annotation.

Set the base domain once and every service URL derives from it — there is no
hardcoded host or port anywhere in the foundation or the stack:

```yaml
globalSettings:
  defaultHost: platform.example.com    # your delegated DNS zone
  defaultHttpsPort: 443
  email: admin@platform.example.com    # ACME registration
  # dnsProvider: cloudflare            # only when it differs from the environment's cloud
```

| Concern | What ships | To go publicly trusted |
| --- | --- | --- |
| DNS | `external-dns` (enabled in production), `--domain-filter=<defaultHost>`, `--txt-owner-id=adhar-<cluster>`, publishing a record per platform HTTPRoute hostname against the Gateway's LoadBalancer address | Delegate `<defaultHost>` to the provider; credentials are materialised into the `adhar-dns-provider` Secret and never enter Git |
| TLS | cert-manager with `adhar-selfsigned` (default, no configuration), `adhar-letsencrypt-staging`, `adhar-letsencrypt-prod` (HTTP-01 through `adhar-gateway`) and `adhar-letsencrypt-dns` (DNS-01, rendered with the provider's solver) | The platform **wildcard** needs DNS-01 — HTTP-01 cannot issue wildcards. With a DNS-01-capable provider the wildcard `*.<defaultHost>` is issued into the `adhar-cert` Secret the HTTPS listener already references |
| Ingress | Cilium Gateway API, one cloud LoadBalancer on 80/443 | — |

The mechanics, the day-2 re-render path, and the port-forward fallback while DNS
propagates are in [PRODUCTION_ACCESS §4](PRODUCTION_ACCESS.md#4-dns-and-tls-are-automatic).

## 7. Cluster Mesh (T3)

Live-verified: `adhar-mgmt` (cluster id 1, Pod CIDR `10.244.0.0/16`) meshed with
`adhar-test` (id 2, `10.245.0.0/16`), `cilium clustermesh status` green on both
sides, bidirectional traffic over a global service.

Every Adhar cluster ships mesh-ready Cilium identity (management cluster:
`cluster.name: adhar-mgmt`, `cluster.id: 1`). Workload clusters must use unique
names and IDs 2–255 in their Cilium values.

1. All meshed clusters must share a Cilium CA — copy the management cluster's
   `cilium-ca` Secret into each workload cluster **before** Cilium starts there,
   and ensure Pod CIDRs do not overlap
2. Enable the clustermesh apiserver on each cluster
   (`clustermesh.useAPIServer: true`) and connect pairs with
   `cilium clustermesh connect --context <mgmt> --destination-context <workload>`
3. Verify with `cilium clustermesh status` and a cross-cluster service-affinity
   test

**Operational rule:** meshed clusters must share a VPC, or the clustermesh
apiserver must be LoadBalancer-typed — on DigitalOcean node ports answer only on
the private NIC.

**SPIFFE/SPIRE is deliberately not deployed.** Cilium's SPIRE-backed mutual
authentication (`authentication.enabled`, `authentication.mutual.spire.enabled`)
is **off** in the platform's Cilium values, for two reasons: unregistered
identities flood the operator with retries and starve its Gateway API
controller (the Gateway took ~4 minutes to reach `Programmed`), and Cilium marks
the feature deprecated as of v1.20 for removal in v1.21 — the platform runs
1.20. **Workload-to-workload mutual authentication is therefore not a property
this platform currently provides.** Use WireGuard/IPsec transparent encryption
(the `encryption` block in the Cilium values) plus network policy for
node-to-node and pod-to-pod confidentiality, and application-level mTLS where
you need peer identity.

## 8. Backup and disaster recovery

### 8.1 What must be backed up

| Data | Method | Frequency |
| --- | --- | --- |
| Gitea repositories (**the** platform state) | CNPG PostgreSQL backups (WAL archiving to object storage) + repo storage snapshot; optionally mirror to an external forge | Continuous (WAL) + daily |
| Databases (Keycloak, Harbor, app CNPG clusters) | CNPG scheduled backups to object storage | Continuous (WAL) + daily |
| Persistent volumes | Velero + CSI snapshots | Daily |
| Cluster API objects | Velero cluster backup | Daily |
| Crossplane state | Nothing extra — managed resources reconverge from Git-declared XRs | — |

**What ships enabled:** the velero package carries two Schedules —
`adhar-platform-daily` (02:00 UTC, `adhar-system` + cluster-scoped objects,
30-day TTL) and `adhar-cluster-weekly` (Sunday 03:00 UTC, all namespaces, 90-day
TTL) — against the `default` BackupStorageLocation. The platform CNPG databases
have WAL archiving plus a daily 01:30 UTC base backup (`ScheduledBackup`, 30-day
retention). Velero's node agent is not deployed: Velero covers *objects*, CNPG
covers *database data*; generic PVC file data needs CSI snapshots or the node
agent.

> **Repoint the BackupStorageLocation at real object storage in production.**
> The default is the in-cluster MinIO bucket `adhar-backups`, and a full MinIO
> stops WAL archiving for **every** database at once, which then fills the
> Postgres volumes and takes SSO down —
> [TROUBLESHOOTING §7.2](TROUBLESHOOTING.md#72-minio-filled--cnpg-wal-archiving-stops-platform-wide).
> Also set a CNPG `retentionPolicy` (7d) on every database.

The Crossplane CronOperations schedule daily backups and weekly secret rotation;
they require the core `--enable-operations` flag and an object store.

### 8.2 Targets

| Scenario | RPO | RTO |
| --- | --- | --- |
| Package/app misconfiguration | 0 (Git revert) | Minutes |
| Platform service data loss | ≤ 15 min (WAL) | ≤ 1 h |
| Workload cluster loss (T3) | 0 for config; app data per its own backups | ≤ 1 h (reprovision + resync) |
| Management cluster loss | ≤ 1 h | ≤ 4 h |

### 8.3 Management-cluster recovery runbook

1. `adhar up -f config.yaml --env <name>` against a fresh cluster (same config)
   → the foundation bootstraps deterministically
2. Restore the databases from object storage — a CNPG `Cluster` with a
   `bootstrap.recovery` section pointing at the barman store recovers to the
   latest WAL (or a `recoveryTarget` for PITR):

   ```yaml
   spec:
     bootstrap:
       recovery:
         source: gitea-db
     externalClusters:
       - name: gitea-db
         barmanObjectStore:
           destinationPath: s3://adhar-backups/cnpg/gitea-db
           endpointURL: <your object store>
           s3Credentials: { ... same as backup ... }
   ```

   Restore Gitea's database **before** the controller seeds repos, or let it
   seed and force-push your backed-up state
3. Restore platform objects that live outside Git (one-off Secrets, ad-hoc
   resources): `velero restore create --from-backup adhar-platform-daily-<ts>`
   — review with `--preserve-nodeports=false` and exclude anything ArgoCD owns
   (it re-syncs those from Git anyway)
4. ArgoCD reconciles the package set from restored Git state; Crossplane
   reconverges infrastructure
5. Verify: `adhar get status`, ArgoCD app health, `velero backup get` shows the
   schedules running, and smoke-test SSO plus one golden-path deploy. Also check
   `kubectl get clusterproviderconfigs.<group>` — a healthy-looking platform can
   still have zero of them
   ([TROUBLESHOOTING §4.1](TROUBLESHOOTING.md#41-zero-providerconfigs-crossplane-silently-skipped-them))

### 8.4 Practice

- **Quarterly** — full management-cluster restore into an isolated VPC.
- **Monthly** — the shipped reconstructability drill
  (`platform/controlplane/configuration/operations/reconstructability-drill.yaml`,
  requires `--enable-operations`) creates a drill `CompositeCluster` on the 1st
  of each month; its observer WatchOperation records time-to-Ready against the
  1-hour SLO in the operation output. Review the verdict, then delete the drill
  XR:

  ```bash
  kubectl -n adhar-system delete compositecluster drill-reconstructability
  ```

- **Monthly, T3** — additionally destroy and reprovision one non-prod *cloud*
  workload cluster from Git: same SLO, real provider. Prune its Applications
  **before** deleting the XR
  ([TROUBLESHOOTING §4.3](TROUBLESHOOTING.md#43-applications-hang-terminating-after-a-compositecluster-is-deleted)).

## 9. Upgrades

Three independent streams.

**Platform (an Adhar release)** — a new binary upgrades the foundation
(embedded manifests) and the stack content:

1. Read the release notes; upgrade a staging platform first
2. Take a pre-upgrade backup ([§8](#8-backup-and-disaster-recovery))
3. `adhar upgrade --diff-only` — shows what the release would change in the
   GitOps repositories without touching anything
4. `adhar upgrade` — converges the foundation to the release's embedded
   manifests (SSA-idempotent; unchanged components are no-ops), shows the stack
   diff, and on confirmation force-pushes the stack and requests an ArgoCD
   refresh (`--yes` for CI, `--skip-foundation` to touch only the stack)
5. Watch ArgoCD until all apps are `Healthy/Synced` (`adhar get status`).
   Expect a ~10-minute OutOfSync storm first — that is the monorepo behaving
   normally ([TROUBLESHOOTING §3.2](TROUBLESHOOTING.md#32-every-application-goes-outofsync-after-a-push-expected)).
   Roll back = the previous binary's `adhar upgrade` + a Git revert

**Packages (chart bumps)** — per package: bump `CHART_VERSION` in its
`generate-manifests.sh`, re-render, review the manifest diff, merge (see
[Customization §2](CUSTOMIZATION.md#2-change-a-packages-configuration)).
Automate with a CI job that re-renders and opens PRs. Two rules that bite on
re-render: keep `--include-crds` if the chart ships a `crds/` directory, and
grep the output for foreign namespaces
([TROUBLESHOOTING §6.2, §6.6](TROUBLESHOOTING.md#62-if-kind-is-a-crd-it-should-be-installed-before-calling-start)).

**Kubernetes** — in-place, control plane first, then workers:

```bash
adhar cluster upgrade <cluster> --version 1.37.2 -p digitalocean -f config.yaml
```

On `useManagedK8s` clusters follow the provider's managed-upgrade process
instead. Do the management cluster **last**, after workload clusters prove the
version.

## 10. Day-2 operations

### Golden signals to alert on

| Area | Alert on |
| --- | --- |
| ArgoCD | apps `Degraded`/`OutOfSync` > 15 min (a push storm clears in ~10); sync failures |
| Controllers | reconcile error rate, workqueue depth |
| Gitea / Keycloak / CNPG | availability, replication lag, **`lastFailedBackup` on any CNPG cluster** |
| Gateway | 5xx rate, cert expiry (< 21 days), LB health |
| Cilium | agent health, policy drop anomalies (Hubble) |
| Capacity | node memory pressure, **PVC usage > 80% (especially the MinIO claim)**, pods `Pending` for capacity, OpenCost spend anomalies |

Route via Alertmanager / Grafana OnCall; keep runbook links in alert
annotations — pointing at the matching
[TROUBLESHOOTING](TROUBLESHOOTING.md) section is usually enough.

### Routine

| Cadence | Action |
| --- | --- |
| Daily | Review ArgoCD drift and failed syncs (steady state is zero — investigate all) |
| Weekly | Trivy scan report triage; pending package updates |
| Monthly | Workload-cluster rebuild test; reconstructability drill verdict; access review (Keycloak groups) |
| Quarterly | DR restore drill; capacity/cost review (OpenCost); supply-chain enforcement drill (`hack/verify-supply-chain.sh`) |

### Everyday commands

```bash
adhar get status                                    # platform conditions + per-package health
adhar get apps                                      # ArgoCD application states
adhar get secrets -p argocd                         # platform credentials
adhar cluster list --file config.yaml               # --file is REQUIRED, see below
adhar cluster scale <cluster> --workers 10 -p <provider> -f config.yaml
kubectl -n adhar-system get adharplatform -o yaml   # component conditions + autoscaling status
cilium status && cilium connectivity test           # network layer
hubble observe --namespace <ns>                     # live flow debugging
```

> `adhar cluster list` **requires `--file <config>`**. Without it, it loads the
> default config, queries no provider and reports "No clusters found" — which
> reads like your clusters are gone.

Everything diagnostic lives in **[TROUBLESHOOTING.md](TROUBLESHOOTING.md)**,
which starts with a symptom-to-section lookup table.

---

**Related**: [Architecture](ARCHITECTURE.md) ·
[Production Access](PRODUCTION_ACCESS.md) ·
[Provider Guide](PROVIDER_GUIDE.md) · [Troubleshooting](TROUBLESHOOTING.md) ·
[Customization](CUSTOMIZATION.md) · [Roadmap](ROADMAP.md)
