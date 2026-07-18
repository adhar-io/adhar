# Adhar User Guide

**Version**: v0.1.0

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

## 6. Security Day-to-Day

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
