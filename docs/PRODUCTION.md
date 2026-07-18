# Adhar Production Guide

**Version**: v0.1.0

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
- [ ] Rotate bootstrap credentials (`gitea_admin`, ArgoCD `admin`) immediately; store break-glass credentials in Vault
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

1. Point `*.platform.example.com` at the Gateway's LoadBalancer (external-dns automates record management)
2. cert-manager `ClusterIssuer` (ACME DNS-01 for wildcard, or your corporate CA); reference the certificate from the `adhar-gateway` TLS listener
3. Set `globalSettings.host: platform.example.com` — every service becomes `argocd.platform.example.com`, `gitea.platform.example.com`, … exactly as in local (`*.adhar.localtest.me`), keeping runbooks identical across environments

## 5. Backup and Disaster Recovery

### 5.1 What must be backed up

| Data | Method | Frequency |
|------|--------|-----------|
| Gitea repositories (**the** platform state) | CNPG PostgreSQL backups (WAL archiving to object storage) + repo storage snapshot; optionally mirror to an external forge | Continuous (WAL) + daily |
| Databases (Keycloak, Harbor, app CNPG clusters) | CNPG scheduled backups to object storage | Continuous (WAL) + daily |
| Persistent volumes | Velero + CSI snapshots | Daily |
| Cluster API objects | Velero cluster backup | Daily |
| Crossplane state | Nothing extra — managed resources reconverge from Git-declared XRs | — |

The Crossplane CronOperations shipped with the platform schedule daily backups and weekly secret rotation; verify they are enabled (`--enable-operations`) and pointed at your object store.

### 5.2 Targets

| Scenario | RPO | RTO |
|----------|-----|-----|
| Package/app misconfiguration | 0 (Git revert) | Minutes |
| Platform service data loss | ≤ 15 min (WAL) | ≤ 1 h |
| Workload cluster loss (T3) | 0 for config; app-data per its backups | ≤ 1 h (reprovision + resync) |
| Management cluster loss | ≤ 1 h | ≤ 4 h |

### 5.3 Management-cluster recovery runbook (outline)

1. `adhar up` against a fresh cluster (same config.yaml) → foundation bootstraps deterministically
2. Restore Gitea (CNPG restore + repo storage) **before** the controller seeds repos, or let it seed and force-push your backed-up state
3. Restore Velero-backed PVs for stateful platform services
4. ArgoCD reconciles the entire package set from restored Git state; Crossplane reconverges infrastructure
5. Verify: `adhar get status`, ArgoCD app health, smoke-test SSO and one golden-path deploy

### 5.4 Practice

- Quarterly: full management-cluster restore into an isolated VPC
- Monthly: destroy and reprovision one non-prod workload cluster from Git (T3) — this doubles as the reconstructability test

## 6. Upgrades

Two independent upgrade streams:

**Platform (Adhar release)** — new binary upgrades foundation components (embedded manifests) and stack content:

1. Read release notes; upgrade a staging platform first
2. Take a pre-upgrade backup (§5)
3. Run the new `adhar up` against the existing cluster — SSA idempotently converges foundation components; stack updates arrive as Git diffs you can review in Gitea before syncing
4. Watch ArgoCD until all apps are `Healthy/Synced`; roll back = previous binary + Git revert

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
