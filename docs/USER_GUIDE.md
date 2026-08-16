# Adhar User Guide

The day-to-day guide for developers and platform operators using a running Adhar platform. For setup see [Getting Started](GETTING_STARTED.md); for how it works see [Architecture](ARCHITECTURE.md); for changing the platform itself see [Customization](CUSTOMIZATION.md).

---

## 1. Mental Model in 60 Seconds

- The platform is **declared in Git** (in-cluster Gitea: `adhar/packages` + `adhar/environments`) and **reconciled by ArgoCD** — after bootstrap, Git is the only write path
- Every capability is a **package** gated by an `enabled` flag; every UI is `https://<service>.<domain>:<port>`
- The **CLI** is for lifecycle and inspection (`up`, `down`, `get`); the **Git flow** is for change
- Self-service infrastructure (databases, clusters, …) is requested through **namespaced Crossplane APIs** (`CompositeDatabase`, `CompositeCluster`, …)

## 2. Accessing the Platform

| Service | URL (local defaults) | Notes |
|---------|----------------------|-------|
| Adhar Console | `https://console.adhar.localtest.me:8443` | Developer portal |
| ArgoCD | `https://argocd.adhar.localtest.me:8443` | `admin` / `adhar get secrets -p argocd` |
| Gitea | `https://gitea.adhar.localtest.me:8443` | `gitea_admin` / `r8sA8CPHD9!bt6d` (rotate in production!) |
| Grafana | `https://grafana.adhar.localtest.me:8443` | Dashboards, logs (Loki), traces (Tempo) |
| Headlamp | `https://headlamp.adhar.localtest.me:8443` | Kubernetes UI |
| Hubble | `https://hubble.adhar.localtest.me:8443` | Network flows |

`kubectl` always works too — Adhar sets your kubecontext; platform components live in `adhar-system`.

## 3. CLI Reference

### Lifecycle

```bash
adhar up                          # create/converge the platform (local Kind by default)
adhar up -f config.yaml           # cloud/production config
adhar up --port 9443              # custom HTTPS port (local)
adhar up --recreate               # rebuild from scratch
adhar up --dry-run                # preview
adhar down                        # tear down
```

### Inspection

```bash
adhar get status                  # platform component health
adhar get apps                    # ArgoCD application states
adhar get secrets [-p <service>]  # service credentials
adhar get all                     # comprehensive overview
adhar version                     # CLI version info
```

### Applications

```bash
adhar apps deploy my-app --repo https://github.com/org/repo --path manifests/ --dest-namespace my-team
adhar apps list [--namespace <ns>]
adhar apps delete my-app [--force]
```

### Clusters & environments

```bash
adhar cluster create prod --provider gcp --region us-central1 --nodes 3
adhar cluster list
adhar cluster kubeconfig prod > prod-kubeconfig.yaml
adhar cluster delete prod

adhar env create dev --provider digitalocean --template nonprod-defaults
adhar env create prod --provider aws --template prod-defaults --ha-mode
adhar env list
```

Run `adhar help` or `adhar <command> --help` for the full flag surface (27 subcommands).

## 4. Deploying Your Applications

Three supported paths, in increasing order of platform integration:

**a) Straight ArgoCD** — point an Application at your repo (via `adhar apps deploy` or the ArgoCD UI). Best for trying things out.

**b) `CustomPackage` CR** — the platform-native way; your app manifest is pushed into Gitea, so the cluster never depends on external availability. See [Customization §4](CUSTOMIZATION.md#4-deploy-team-applications-custompackage) and `examples/`.

**c) Golden path via Console** — scaffold from a template in the Adhar Console (Backstage), which creates the repo and wiring for you.

### Requesting infrastructure

Ask for what you need in *your* namespace; the platform decides how to provision it:

```yaml
apiVersion: platform.adhar.io/v1alpha1   # via a Composite API, e.g.
kind: CompositeDatabase                   # see examples/database.yaml
metadata:
  name: orders-db
  namespace: team-orders
spec:
  engine: postgres
  size: small
```

Locally this becomes a CNPG PostgreSQL; on AWS the same request becomes RDS. Quotas and policies (Kyverno) apply automatically.

### The local-first development workflow

The local platform is a scaled-down twin of production (same manifests, same
wiring — pillar 4, ADR-0015), so the whole development loop runs on your
laptop before anything reaches a remote cluster:

1. **Code** against the in-cluster Gitea (`adhar auth login`, push to your
   repo) — or mirror from your external forge
2. **CI fires on push**: the jenkins-x package (Lighthouse, ADR-0018) receives
   the webhook, triggers Tekton pipelines, and reports status and ChatOps
   (`/test`, `/lgtm`) back to the PR. Locally it is `enabled: "false"` by
   default — flip it in the ApplicationSet to run the full loop
3. **Preview per PR** (ADR-0017): copy
   [`examples/preview-environments-appset.yaml`](../examples/preview-environments-appset.yaml)
   for your repo — every PR labeled `preview` gets its own namespace at its
   PR head, updated on push, pruned on close
4. **Cluster-grade isolation when you need it** (ADR-0016): enable the
   `vcluster` package to test operators/CRDs/webhooks in a disposable virtual
   cluster instead of resetting the platform
5. **Promotion is Git** (ADR-0004/P2.5): merging updates the environments
   repo; Kargo promotes dev → staging → prod, ArgoCD deploys — identical
   mechanics locally and in production

Because production runs the same stack (fuller enablement, real DNS/TLS), a
change that works locally needs no translation to ship remotely.

## 5. Operating the Platform Day-to-Day

### Watching state

- **ArgoCD UI** is the single pane for "what is deployed and is it healthy" — every package and app is an Application there
- `adhar get status` summarizes component conditions from the `AdharPlatform` CR
- Drift is auto-healed: manual `kubectl edit` on managed objects will be reverted by the next sync — change Git instead

### Making platform changes

All platform changes are Git changes in Gitea (enable/disable packages, tune values, add environments) — the complete catalogue is the [Customization Guide](CUSTOMIZATION.md). Rule of thumb: if you're about to `kubectl apply` something platform-level, stop and make it a commit instead.

### Observability

- **Metrics**: Grafana dashboards (cluster, nodes, ArgoCD, per-app); Prometheus behind them
- **Logs**: Grafana → Explore → Loki. Query examples: `{namespace="team-orders"}`, `{app="my-app"} |= "error"`
- **Traces**: Tempo data source (OTel ingestion via Alloy); eBPF auto-instrumentation available via Beyla
- **Network**: Hubble UI for live flows — invaluable for debugging connectivity and authoring network policies
- **Cost**: OpenCost dashboard for namespace/team attribution

## 6. Bootstrap & Day-2 Operations

The operator manual for bringing a platform into existence with `adhar up`,
verifying it, resuming an interrupted bootstrap, upgrading, and tearing down. For
a first-time walkthrough see [Getting Started](GETTING_STARTED.md); for the
authoritative as-built design see
[design/0001 — Management-cluster-first](design/0001-management-cluster-first.md)
(decision: [ADR-0001](adr/0001-management-cluster-first.md)); for failure-signature
recovery see [Troubleshooting](TROUBLESHOOTING.md).

Everything the bootstrap installs is **embedded** in the binary (`//go:embed`) — no
manifest is fetched at runtime, so `adhar up` is offline-capable.

### Ports & URLs (local)

The Kind node maps host ports to the Gateway's pinned NodePorts; the Cilium Gateway
(served by Cilium's Envoy) terminates and routes to the platform services. All
`*.adhar.localtest.me` names resolve to `127.0.0.1`.

| Purpose | Host port | Gateway NodePort | Backend |
|---|---|---|---|
| HTTPS | `8443` | `30443` | Cilium Envoy → HTTPS listener (443) |
| HTTP | `8080` (also `8081`) | `30080` | Cilium Envoy → HTTP listener (80) |
| HTTPS (alt, on-node OIDC) | `8443` | `8443` | pinned so `https://keycloak.<host>:8443` resolves on-node for kube-apiserver OIDC discovery |
| Gitea SSH | `32222` | `32222` | Gitea SSH |

The HTTPS port is customizable with `--port`; the HTTP port auto-derives as
`port − 363` (e.g. `--port 9443` → HTTP `9080`).

### `adhar up` and its flags

`adhar up` with no config file creates a **local Kind** platform and runs the
controllers in-process, exiting when the platform is fully converged. With
`-f config.yaml` it provisions a **production** cluster via the provider factory
and installs the in-cluster controller manager for continuous reconciliation.

| Flag | Effect |
|---|---|
| *(none)* | Local mode: create/reuse the `adhar` Kind node, bootstrap the full platform, exit on convergence. |
| `--recreate` | **Destructive.** Delete the existing Kind cluster before creating a new one. Use only when discarding local state. |
| `--port <n>` | HTTPS host port (default `8443`); HTTP auto-derives as `n − 363`. |
| `--dry-run` / `-d` | Preview what would be created without applying. |
| `--in-cluster` | After the in-process bootstrap converges, also install the `adhar-controller-manager` Deployment for continuous reconciliation (always done in production mode). |
| `-f, --file <cfg>` | Production mode: provision a cloud/on-prem cluster from a resolved config file. |
| `--ha` | Render foundation components in HA mode (replicas, PDBs, HA redis, CNPG for Gitea's DB); default for production configs with `enableHAMode`. |
| `--host <name>` | Host name for cluster resources (default `adhar.localtest.me`). |
| `-w, --watch` | Keep running to continuously sync directories (default on). |

### What a healthy bootstrap looks like

A clean `adhar up` cannot report success until the platform is genuinely *usable*:

- **Foundation installed in order** — `Gateway API CRDs → Cilium → Gateway →
  [CNPG, if HA] → ArgoCD → Gitea → Crossplane`.
- **Gateway `Programmed=True`** — the `cilium-gateway-adhar-gateway` Service is a
  **NodePort** pinned to `30080` / `30443` / `8443`.
- **GitOps seeded** — the `adhar` org exists in Gitea with the `packages` and
  `environments` repos populated; the `gitea-argocd` Service (ArgoCD→Gitea repo
  auth) is present.
- **2 ApplicationSets** — the platform ApplicationSet (`adhar-appset-local.yaml`,
  the curated single-node core) plus the workload ApplicationSet
  (`adhar-appset-workload.yaml`, which generates nothing until workload clusters
  register).
- **Roughly two dozen applications** — the curated local core, selected by the
  ApplicationSet's `enabled: "true"` filter, converging to `Healthy`. A few
  (CNPG-backed apps, Keycloak) stay `Progressing`/`Degraded` for the first 2–3
  minutes; that is normal.
- **`AdharPlatform` conditions all True** — `ArgoCDReady`, `GatewayReady`,
  `GiteaReady`, `CrossplaneReady`, `GitOpsReady`, and aggregate `Ready`.
- **Access URLs live** — `https://argocd.adhar.localtest.me:8443` and
  `https://gitea.adhar.localtest.me:8443` return HTTP 200/3xx.

### Verify-healthy checklist

Copy-pasteable; all should pass on a healthy platform. Any failing check maps to a
section in [Troubleshooting](TROUBLESHOOTING.md).

```bash
# 0. One-shot platform status (conditions + per-app health)
adhar get status

# 1. Gateway Programmed
kubectl get gateway -n adhar-system adhar-gateway \
  -o custom-columns=NAME:.metadata.name,PROGRAMMED:'.status.conditions[?(@.type=="Programmed")].status'
# want: PROGRAMMED=True

# 2. Edge Service is a pinned NodePort
kubectl get svc -n adhar-system cilium-gateway-adhar-gateway \
  -o custom-columns=NAME:.metadata.name,TYPE:.spec.type,PORTS:.spec.ports[*].nodePort
# want: TYPE=NodePort, node ports 30080 / 30443 / 8443

# 3. ApplicationSets present (expect 2) + ArgoCD->Gitea auth Service
kubectl get applicationset -n adhar-system
kubectl get svc -n adhar-system gitea-argocd

# 4. Applications converging
kubectl get applications -n adhar-system \
  -o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status

# 5. Gitea org + repos seeded
GITEA_POD=$(kubectl get pod -n adhar-system -l app=gitea -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n adhar-system "$GITEA_POD" -c gitea -- \
  curl -s -o /dev/null -w 'org=%{http_code}\n' \
  -u 'gitea_admin:r8sA8CPHD9!bt6d' http://localhost:3000/api/v1/orgs/adhar
# want: org=200

# 6. End-to-end through the Gateway
curl -sk -o /dev/null -w 'argocd HTTP %{http_code}\n' https://argocd.adhar.localtest.me:8443
curl -sk -o /dev/null -w 'gitea  HTTP %{http_code}\n' https://gitea.adhar.localtest.me:8443
```

### Safe re-run / resume

Re-running `adhar up` **without `--recreate`** is the designed way to resume an
interrupted or partial bootstrap. It is safe because:

- **Cluster reuse** — the reconcile returns early on the existing healthy Kind
  node; your data, repos, and running apps are preserved.
- **Idempotent SSA** — every foundation manifest re-applies with
  `FieldManager="adhar"` + `ForceOwnership`, re-adopting existing objects rather
  than duplicating them.
- **One-time seeding, always-applied wiring** — repo **seeding** is the one-time
  part, guarded by the `RepositoriesCreated` status flag, so it short-circuits once
  the `packages`/`environments` repos exist. The ArgoCD **ApplicationSet is
  re-applied on every reconcile** (it is *not* gated on the repos already existing),
  so re-running `adhar up` always re-applies the current platform ApplicationSet.

This matters most in **local mode**, where the controller is ephemeral (in-process,
exits on convergence): if the process is interrupted mid-bootstrap (Ctrl-C, closed
terminal, laptop sleep, timeout), there is nothing left to retry — **re-running
`adhar up` resumes from the exact gate that was pending**.

| Situation | Command |
|---|---|
| Bootstrap interrupted or a phase stalled | `adhar up` (no `--recreate`) — resume |
| Platform healthy, want to re-push stack changes | `adhar upgrade` (see below) |
| Wedged Kind node / resource-starved partial install you don't care about | `adhar up --recreate` — **destructive** rebuild |

### Upgrade

`adhar upgrade` converges the foundation to the current binary's embedded manifests
and **re-pushes the platform stack** to Gitea. It resets `RepositoriesCreated` on
the in-memory `AdharPlatform` before re-seeding, forcing a full re-push even though
the repos already exist (repo creation is 409-tolerant; population is a force push).
Without this reset an upgrade would silently push nothing on an already-bootstrapped
platform.

```bash
adhar upgrade         # converge foundation, review stack diff, re-push stack, sync
```

After it completes, ArgoCD reconciles the updated stack from Gitea. Verify with the
checklist above.

### Teardown

```bash
adhar down            # tear down the local Kind cluster and clean up Adhar resources
```

This removes the local `adhar` Kind node and its state. To rebuild afterward, run
`adhar up` again. For a rebuild-in-place, `adhar up --recreate` deletes and recreates
the node in one step.

### Local vs. production

The same CRDs, reconcile pipeline, and embedded manifests drive both topologies —
they differ only in **size** and **controller placement**:

| | Local (Kind) | Production (cloud / on-prem) |
|---|---|---|
| Entry | `adhar up` | `adhar up -f config.yaml` |
| Cluster | one `adhar` Kind node (CNI/kube-proxy off) | provider factory (`aws`/`azure`/`gcp`/`digitalocean`/`civo`/`custom`) |
| Controller placement | **in-process, exits on convergence** — ephemeral | in-process bootstrap → then **`adhar-controller-manager` Deployment**, continuous |
| Foundation size | single replica (`install.yaml`) | HA (`install-ha.yaml`): replicas, PDBs, HA redis, CNPG for Gitea |
| Gateway edge | NodePort pinned 30080/30443/8443 | LoadBalancer Service + cert-manager listener cert |
| ApplicationSet | `adhar-appset-local.yaml` (curated core) | `adhar-appset-production.yaml` (full enablement) |
| Recovery from interruption | **re-run `adhar up`** (nothing retries once the process exits) | in-cluster controller self-heals without a re-run |

The key operational consequence: in **local** mode an interrupted bootstrap is
resumed by re-running `adhar up`; in **production** the persistent
`adhar-controller-manager` self-heals the foundation and re-reconciles every 15s.
Production HA/DR posture is covered in [Production Guide](PRODUCTION.md).

## 7. Security Day-to-Day

- **Sign in with SSO** (Keycloak) everywhere; local bootstrap credentials are for day-0 only
- **Secrets** come from External Secrets — never commit them; reference an `ExternalSecret` in your app manifests
- **Policies** (Kyverno) will reject non-compliant workloads (missing resources, `:latest` tags, privileged pods) — the deny message names the violated policy
- **Images** should come from the platform registry (Harbor) once enabled; Trivy scans and Cosign verification gate what runs

## 7. Troubleshooting

| Symptom | Check |
|---------|-------|
| App stuck `Progressing`/`Degraded` | ArgoCD app → Events; then `kubectl -n <ns> describe pod …` — often a Kyverno denial or missing quota |
| Service URL 404 | Is the app `Healthy`? Does it ship an `HTTPRoute`? `kubectl get httproute -A` |
| `OutOfSync` won't heal | Diff view in ArgoCD — someone changed Git and cluster divergently; Git wins on sync |
| Can't reach another service | `hubble observe --namespace <ns>` — look for `DROPPED` (network policy) |
| Pod `Pending` | `kubectl describe pod` — node resources (local) or nodepool autoscaling (cloud) |
| Platform component unhealthy | `kubectl -n adhar-system get adharplatform -o yaml` conditions; controller logs |

```bash
# The debugging toolbox
adhar get status && adhar get apps
kubectl -n adhar-system get pods
kubectl -n adhar-system logs deploy/argo-cd-argocd-server
cilium status
hubble observe --since 5m --namespace <ns>
```

## 8. Where to Go Next

- **[Customization Guide](CUSTOMIZATION.md)** — packages, environments, your own golden paths
- **[Production Guide](PRODUCTION.md)** — HA, hardening, backup/DR, upgrades
- **[Provider Guide](PROVIDER_GUIDE.md)** — cloud-specific setup
- **[Architecture](ARCHITECTURE.md)** — the full design, with ADRs
- **Community**: [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww) · [Discussions](https://github.com/adhar-io/adhar/discussions) · [Issues](https://github.com/adhar-io/adhar/issues)
