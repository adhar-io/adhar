# Adhar Production Guide

How to run Adhar as production infrastructure: topology and sizing, high availability, security hardening, backup and disaster recovery, upgrades, and day-2 operations. Read the [Architecture](ARCHITECTURE.md) first — this guide assumes its terminology (topologies T1/T2/T3, bootstrap vs GitOps phases).

> ⚠️ Adhar is in active development (v0.1.x). Treat this guide as the production *blueprint* the platform is built toward; validate each control in your environment before depending on it, and see the [Roadmap](ROADMAP.md) for what is implemented vs planned.

---

## 1. Choosing a Topology

| Topology | When | Trade-off |
|----------|------|-----------|
| **T2 — single production cluster** | One team, one or two environments, getting to production fast | Platform and workloads share a failure domain and upgrade window |
| **T3 — management + workload clusters** | Multiple environments/teams, compliance boundaries, cluster-level blast-radius isolation | More clusters to pay for and operate (the management cluster automates most of it) |

Start with T2; the move to T3 is additive (provision workload clusters via `CompositeCluster`, shift apps over) because all platform state is already in Git.

## 2. Sizing and HA

### Management / platform cluster baseline (T2/T3)

| Component | Minimum production shape |
|-----------|--------------------------|
| Control plane | 3 nodes (managed control planes: rely on the provider SLA) |
| Platform node pool | 3× 4 vCPU / 16 GB across ≥ 2 zones, autoscaling enabled |
| ArgoCD | ≥ 2 replicas for server/repo-server; HA Redis |
| Gitea | ≥ 2 replicas; **external PostgreSQL via CNPG** (3 instances, streaming replication); RWX or object-backed storage |
| Gateway (Cilium Envoy) | ≥ 2 replicas behind the cloud LB; PodDisruptionBudget |
| Keycloak | ≥ 2 replicas + CNPG PostgreSQL |
| Observability | Mimir/Loki/Tempo on object storage; retention by policy, not disk size |

Enable `enableHAMode: true` in the environment config so environment templates apply replicas, PDBs, and topology-spread constraints. Anti-affinity across zones for every stateful service.

### Workload clusters (T3)

Keep them thin: Cilium, Alloy collectors, Kyverno, Falco, plus your apps. Everything multi-tenant and stateful stays on the management cluster. A workload cluster should be **fully reconstructable** in under an hour from Git + Crossplane — test that regularly (§5.4).

## 3. Security Hardening Checklist

### Identity & access

- [ ] Keycloak as OIDC provider for ArgoCD, Gitea, Grafana, Console; humans never use local admin accounts after bootstrap
- [ ] Rotate bootstrap credentials (`gitea_admin`, ArgoCD `admin`) immediately: enable the `credential-rotation` package (on by default in the production environment set) once SSO login is verified — it rotates both to random values and stores break-glass copies in Vault (`secret/adhar/bootstrap-credentials`); delete the `bootstrap-credentials-rotated` marker Secret to rotate again
- [ ] Kubernetes API via OIDC group claims; RBAC per team namespace; no cluster-admin for humans in daily work
- [ ] Cloud credentials to Crossplane via workload identity (IRSA / Workload Identity / Managed Identity) — never long-lived keys in Secrets

### Network

- [ ] Default-deny Cilium network policies in all workload namespaces; platform namespaces get scoped allow-rules (roll out namespace-by-namespace using Hubble flow data to author policies)
- [ ] WireGuard transparent encryption for node-to-node traffic
- [ ] Gateway is the only public entry; API server access restricted to VPN/allowlist; Hubble UI, ArgoCD, Gitea behind SSO
- [ ] `external-dns` scoped to the platform's DNS zone only

### Workloads & supply chain

- [ ] Kyverno policies in `Enforce`: Pod Security **restricted** baseline, no `:latest`, resource requests required, disallow privileged/hostPath
- [ ] Harbor as the only allowed registry (Kyverno image allowlist); Trivy scan gates on severity; Cosign signature verification for platform and app images
- [ ] Velero backup namespaces/PVs labeled and included (§5)

### Secrets

- [ ] Vault (HA, auto-unseal via cloud KMS) or cloud secret manager as the single source; ESO syncs into namespaces
- [ ] No secrets in Git, ever — enforce with Gitea push hooks / secret scanning
- [ ] etcd encryption at rest enabled (managed offerings: verify provider default)

## 4. The Edge: DNS, TLS, Load Balancing

On cloud/on-prem providers the platform Gateway deploys in its production
variant automatically: a LoadBalancer Service (instead of Kind's pinned
NodePorts), an HTTPS listener carrying the platform wildcard hostname, and
certificate management delegated to cert-manager via the
`cert-manager.io/cluster-issuer` annotation (default: `adhar-selfsigned`,
which needs no external configuration).

To go from self-signed to publicly trusted:

1. **DNS**: enable the `external-dns` package (enabled in the production
   environment set) and set `--provider=<your-dns>` plus credentials in its
   manifest — it publishes records for every platform HTTPRoute hostname
   against the Gateway's LoadBalancer address (`--txt-owner-id=adhar` keeps
   multi-cluster zones safe)
2. **TLS**: the cert-manager package ships `adhar-selfsigned`,
   `adhar-letsencrypt-staging` and `adhar-letsencrypt-prod` ClusterIssuers
   (ACME issuers use the HTTP-01 solver through `adhar-gateway`; set the
   registration email from `globalSettings.email`). For the platform
   *wildcard* certificate add a DNS-01 solver with your DNS credentials, then
   point the Gateway's `cert-manager.io/cluster-issuer` annotation at that
   issuer — HTTP-01 cannot issue wildcards
3. Set `globalSettings.host: platform.example.com` — every service becomes
   `argocd.platform.example.com`, `gitea.platform.example.com`, … exactly as
   in local (`*.adhar.localtest.me`), keeping runbooks identical across
   environments

### 4.1 Cluster Mesh and workload identity (T3)

Every Adhar cluster ships mesh-ready Cilium identity (management cluster:
`cluster.name: adhar-mgmt`, `cluster.id: 1`); workload clusters must use
unique names and IDs 2–255 in their Cilium values (roadmap P2.4). To connect
management and workload clusters:

1. All meshed clusters must share a Cilium CA — copy the management cluster's
   `cilium-ca` secret into each workload cluster **before** Cilium starts
   there, and ensure Pod CIDRs don't overlap
2. Enable the clustermesh-apiserver on each cluster
   (`clustermesh.useAPIServer: true` in Cilium values; expose via
   LoadBalancer) and connect pairs with `cilium clustermesh connect
   --context <mgmt> --destination-context <workload>`
3. Verify with `cilium clustermesh status` and a cross-cluster
   service-affinity test

**SPIFFE mutual authentication** ships enabled: the foundation includes a
SPIRE server (StatefulSet) and agents in `adhar-system`, trust domain
`adhar.io`, wired into Cilium's mutual auth. Workloads get SPIFFE identities
automatically; enforcement is per-policy — add `authentication.mode:
required` to a CiliumNetworkPolicy to require mutually authenticated peers.
The trust domain is shared platform-wide so identities remain valid across
Cluster Mesh members. Note that mutual auth secures the handshake; pair it
with WireGuard/IPsec (encryption block in the Cilium values) for full mTLS
semantics.

## 5. Backup and Disaster Recovery

### 5.1 What must be backed up

| Data | Method | Frequency |
|------|--------|-----------|
| Gitea repositories (**the** platform state) | CNPG PostgreSQL backups (WAL archiving to object storage) + repo storage snapshot; optionally mirror to an external forge | Continuous (WAL) + daily |
| Databases (Keycloak, Harbor, app CNPG clusters) | CNPG scheduled backups to object storage | Continuous (WAL) + daily |
| Persistent volumes | Velero + CSI snapshots | Daily |
| Cluster API objects | Velero cluster backup | Daily |
| Crossplane state | Nothing extra — managed resources reconverge from Git-declared XRs | — |

**What ships enabled (roadmap P1.5):** the velero package carries two Schedules — `adhar-platform-daily` (02:00 UTC, `adhar-system` + cluster-scoped objects, 30-day TTL) and `adhar-cluster-weekly` (Sunday 03:00 UTC, all namespaces, 90-day TTL) — against the `default` BackupStorageLocation (in-cluster MinIO bucket `adhar-backups`; repoint it at real object storage in cloud). The platform CNPG databases (`keycloak-db`, and `gitea-db` in HA mode) have WAL archiving plus a daily 01:30 UTC base backup (`ScheduledBackup`, 30-day retention) to the same bucket. Velero's node agent is not deployed: Velero covers *objects*, CNPG covers *database data* — generic PVC file data needs CSI snapshots or the node agent if you have stateful workloads outside these databases.

The Crossplane CronOperations shipped with the platform schedule daily backups and weekly secret rotation; verify they are enabled (`--enable-operations`) and pointed at your object store.

### 5.2 Targets

| Scenario | RPO | RTO |
|----------|-----|-----|
| Package/app misconfiguration | 0 (Git revert) | Minutes |
| Platform service data loss | ≤ 15 min (WAL) | ≤ 1 h |
| Workload cluster loss (T3) | 0 for config; app-data per its backups | ≤ 1 h (reprovision + resync) |
| Management cluster loss | ≤ 1 h | ≤ 4 h |

### 5.3 Management-cluster recovery runbook

1. `adhar up` against a fresh cluster (same config.yaml) → foundation bootstraps deterministically
2. Restore the databases from object storage — a CNPG `Cluster` with a `bootstrap.recovery` section pointing at the barman store recovers to the latest WAL (or a `recoveryTarget` for PITR):

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

   Restore Gitea's database **before** the controller seeds repos, or let it seed and force-push your backed-up state
3. Restore platform objects that live outside Git (one-off Secrets, ad-hoc resources): `velero restore create --from-backup adhar-platform-daily-<ts>` — review with `--preserve-nodeports=false` and exclude anything ArgoCD owns (it re-syncs those from Git anyway)
4. ArgoCD reconciles the entire package set from restored Git state; Crossplane reconverges infrastructure
5. Verify: `adhar get status`, ArgoCD app health, `velero backup get` shows the schedules running, smoke-test SSO and one golden-path deploy

### 5.4 Practice

- Quarterly: full management-cluster restore into an isolated VPC
- Monthly: the shipped reconstructability drill (roadmap P2.6,
  `configuration/operations/reconstructability-drill.yaml`, requires
  `--enable-operations`) creates a drill CompositeCluster on the 1st of each
  month; its observer WatchOperation records time-to-Ready against the 1-hour
  SLO in the operation output. Review the verdict, then delete the drill XR
  (`kubectl -n adhar-system delete compositecluster drill-reconstructability`).
  For T3, additionally destroy and reprovision one non-prod *cloud* workload
  cluster from Git each month — same SLO, real provider

## 6. Upgrades

Two independent upgrade streams:

**Platform (Adhar release)** — new binary upgrades foundation components (embedded manifests) and stack content via `adhar upgrade` (roadmap P1.6):

1. Read release notes; upgrade a staging platform first
2. Take a pre-upgrade backup (§5)
3. `adhar upgrade --diff-only` — shows what the new release would change in the GitOps repositories without touching anything
4. `adhar upgrade` — converges the foundation to the release's embedded manifests (SSA-idempotent; unchanged components are no-ops), shows the stack diff, and on confirmation force-pushes the stack and requests an ArgoCD refresh (`--yes` for CI)
5. Watch ArgoCD until all apps are `Healthy/Synced` (`adhar get status`); roll back = previous binary's `adhar upgrade` + Git revert

**Packages (chart bumps)** — per-package: bump `CHART_VERSION` in `generate-manifests.sh`, re-render, review the manifest diff, merge (see [Customization §2](CUSTOMIZATION.md#2-change-a-packages-configuration)). Automate with a CI job that re-renders and opens PRs.

Kubernetes version upgrades follow your provider's managed-upgrade process; do the management cluster last, after workload clusters prove the version.

## 7. Day-2 Operations

### Golden signals to alert on

- ArgoCD: apps `Degraded`/`OutOfSync` > 15 min; sync failures
- Controllers: reconcile error rate, workqueue depth
- Gitea/Keycloak/CNPG: availability + replication lag
- Gateway: 5xx rate, cert expiry (< 21 days), LB health
- Cilium: agent health, policy drop anomalies (Hubble)
- Capacity: node memory pressure, PVC usage > 80%, OpenCost spend anomalies

Route via Alertmanager/Grafana OnCall; keep runbook links in alert annotations.

### Routine

| Cadence | Action |
|---------|--------|
| Daily | Review ArgoCD drift and failed syncs (should be zero — investigate all) |
| Weekly | Trivy scan report triage; pending package updates |
| Monthly | Workload-cluster rebuild test; access review (Keycloak groups) |
| Quarterly | DR restore drill; capacity/cost review (OpenCost) |

### Troubleshooting quick reference

```bash
adhar get status                     # platform-level health
adhar get apps                       # ArgoCD application states
kubectl -n adhar-system get adharplatform -o yaml   # component conditions
kubectl -n adhar-system logs deploy/argo-cd-argocd-server
cilium status && cilium connectivity test            # network layer
hubble observe --namespace <ns>      # live flow debugging
```

| Symptom | First look |
|---------|-----------|
| Service URL 404/timeout | HTTPRoute attached? Gateway `Programmed`? `kubectl get httproute -A` |
| App stuck `Progressing` | ArgoCD app events; target namespace quota/policy denials (Kyverno) |
| Package missing | `enabled` flag in ApplicationSet; generator selector |
| Bootstrap stalls | Controller logs; component order — Cilium must be `Ready` before anything schedules |

---

**Related**: [Architecture](ARCHITECTURE.md) · [Customization](CUSTOMIZATION.md) · [Provider Guide](PROVIDER_GUIDE.md) · [Roadmap](ROADMAP.md)
