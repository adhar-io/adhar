# Adhar Customization Guide

**Version**: v0.1.0

Every supported way to adapt the platform, from flipping a package on to adding a whole cloud provider. Each section maps to an extension point in the [Architecture](ARCHITECTURE.md#9-extensibility--customization-model). The golden rule: **customizations are Git changes** — made in the in-cluster Gitea repos (or your mirror of them), reviewed, and reconciled by ArgoCD. If you find yourself patching Go code for a routine customization, open an issue; that's an architecture gap.

---

## Table of Contents

1. [Enable or Disable a Package](#1-enable-or-disable-a-package)
2. [Change a Package's Configuration](#2-change-a-packages-configuration)
3. [Add Your Own Package](#3-add-your-own-package)
4. [Deploy Team Applications (CustomPackage)](#4-deploy-team-applications-custompackage)
5. [Create or Modify Environments](#5-create-or-modify-environments)
6. [Platform Configuration Layers](#6-platform-configuration-layers)
7. [Domain, TLS, and Ports](#7-domain-tls-and-ports)
8. [Extend Infrastructure APIs (Crossplane)](#8-extend-infrastructure-apis-crossplane)
9. [Add a Provider](#9-add-a-provider)
10. [Tune Foundation Components](#10-tune-foundation-components)

---

## 1. Enable or Disable a Package

All 69 wired packages are declared in the environment's ApplicationSet (`platform/stack/adhar-appset-local.yaml` locally — visible in Gitea under `adhar/environments`). Each entry carries an `enabled` gate:

```yaml
- name: "harbor"
  enabled: "false"        # ← flip to "true"
  namespace: "harbor"
  category: "application"
  manifestPath: "application/harbor/manifests"
```

Commit the change; ArgoCD picks it up on the next sync (or trigger one from the ArgoCD UI). Setting `enabled: "false"` removes the Application — with prune enabled, the workload is uninstalled.

> **Local resource note**: a single Kind node cannot run all 69 packages. The local default enables a curated core (~16). Enable extras selectively, and check headroom with `kubectl top nodes` first.

To see what's currently deployed: `adhar get apps` or the ArgoCD UI at `https://argocd.<domain>:8443`.

## 2. Change a Package's Configuration

Packages are **pre-rendered Helm charts**. Each package directory contains:

```text
packages/security/cert-manager/
├── values.yaml              # your configuration surface
├── generate-manifests.sh    # helm template … -f values.yaml > manifests/install.yaml
└── manifests/
    └── install.yaml         # what ArgoCD actually syncs (generated — don't hand-edit)
```

Workflow:

```bash
# 1. Edit the values
vi packages/security/cert-manager/values.yaml

# 2. Re-render (also how you pin/bump the chart version — CHART_VERSION in the script)
cd packages/security/cert-manager && ./generate-manifests.sh

# 3. Review the diff — it is exactly what will change in the cluster
git diff manifests/install.yaml

# 4. Commit & push; ArgoCD reconciles
git commit -am "cert-manager: enable Gateway API solvers" && git push
```

Because manifests are rendered ahead of time, the Git diff is the cluster diff — no in-cluster Helm surprises ([ADR-0004](adr/0004-applicationset-package-model.md)).

## 3. Add Your Own Package

Bring any chart or raw manifests into the same GitOps flow:

```bash
# 1. Create the package directory in the packages repo
mkdir -p packages/application/my-tool/manifests

# 2a. From a Helm chart — copy an existing generate-manifests.sh as a template:
#     set repo, chart, CHART_VERSION, and provide values.yaml, then run it
# 2b. From raw manifests — just place them in manifests/

# 3. Wire it into the ApplicationSet (one list entry)
```

```yaml
- name: "my-tool"
  enabled: "true"
  namespace: "my-tool"
  category: "application"
  manifestPath: "application/my-tool/manifests"
```

Conventions that keep you upgrade-safe:

- Put custom packages in their **own directories** — never inside an Adhar-shipped package dir
- All platform packages deploy into `adhar-system` — set `namespace: "adhar-system"` in the entry and render manifests with `--namespace adhar-system`
- If the tool has a UI, ship an `HTTPRoute` attaching to `adhar-gateway` so it gets `my-tool.<domain>` like everything else:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-tool
  namespace: my-tool
spec:
  parentRefs:
    - name: adhar-gateway
      namespace: adhar-system
  hostnames: ["my-tool.adhar.localtest.me"]
  rules:
    - backendRefs:
        - name: my-tool
          port: 80
```

## 4. Deploy Team Applications (CustomPackage)

Platform packages (§3) are for platform capabilities. **Team workloads** use the `CustomPackage` CRD (`platform.adhar.io/v1alpha1`), which delivers an ArgoCD Application/ApplicationSet through the platform's Gitea. See `examples/` for ready-made resources (`application.yaml`, `deployment.yaml`, `repository.yaml`, …).

Sketch:

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: CustomPackage
metadata:
  name: my-app
  namespace: adhar-system
spec:
  # Points at an ArgoCD Application manifest; local paths are pushed into
  # Gitea automatically so the cluster never depends on your laptop.
  argoCD:
    applicationFile: ./my-app/app.yaml
  replicate: true
```

Related: the `GitRepository` CRD manages repos across Gitea/GitHub/GitLab/Bitbucket (local, remote, or embedded sources) — use it when an app's source should live in, or sync from, a specific forge.

## 5. Create or Modify Environments

Environments live in the `environments` repo — one directory each (`local`, `development`, `testing`, `staging`, `production`) with a `config.yaml` declaring the package set for that environment:

```yaml
environment: staging
type: nonprod
packages:
  - name: cert-manager
    enabled: true
    namespace: cert-manager
    category: security
    manifestPath: "security/cert-manager/manifests"
    isChart: false
  # …
```

To add an environment: copy the closest existing directory, adjust `environment`, `type`, and the package list, and wire an ApplicationSet for it (copy `adhar-appset-local.yaml`, update the generator entries and destination). Per-environment package variants use per-env manifest paths — e.g. Argo Workflows ships `manifests/dev` — so one package can carry multiple rendered flavors.

Environment **promotion** is Git promotion: merge the change from `development` → `staging` → `production` configs (Kargo is available as a package to orchestrate this).

## 6. Platform Configuration Layers

Root `config.yaml` (validated by `config.schema.json`) has four layers, each overriding the previous:

```text
globalSettings          # context, base domain, ports (8443), HA mode, email
  └─ providers          # per-cloud credentials & infrastructure (kind, aws, azure, gcp, do, civo, custom)
      └─ environmentTemplates   # reusable defaults (prod-defaults, nonprod-defaults)
          └─ environments       # named instances (dev, test, staging, production) inheriting a template
```

Pattern: put organization-wide policy in `globalSettings` and templates; keep per-environment blocks to the minimal delta (`inherit: nonprod-defaults` + overrides). Never put secrets in `config.yaml` — reference them via External Secrets (§ Security in the [Architecture](ARCHITECTURE.md#7-security-architecture)).

## 7. Domain, TLS, and Ports

| What | Local default | How to change |
|------|---------------|---------------|
| Base domain | `adhar.localtest.me` (wildcard → 127.0.0.1) | `globalSettings.host` in `config.yaml`; production uses your real domain + external-dns |
| HTTPS port | `8443` | `adhar up --port 9443` (HTTP auto-derives, e.g. 9080) |
| TLS cert | Self-signed, generated at bootstrap | Production: cert-manager `Issuer`/`ClusterIssuer` (ACME or private CA) referenced by the Gateway |
| Routing | `HTTPRoute` per service on shared `adhar-gateway` | Add/modify HTTPRoutes in packages; the Gateway itself is foundation-managed |

Every service is a subdomain (`argocd.`, `gitea.`, `console.`, …) — new packages join the pattern by shipping an HTTPRoute (§3).

## 8. Extend Infrastructure APIs (Crossplane)

The control plane (`platform/controlplane/`) is designed for two kinds of extension:

**a) New implementation of an existing API** — e.g. your org's opinionated PostgreSQL behind the standard `CompositeDatabase`: write a new Composition (Pipeline mode) selecting on your label/parameters; consumers don't change.

**b) New platform API** — e.g. `CompositeQueue`:

1. XRD in `configuration/xrd/` — `apiextensions.crossplane.io/v2`, `scope: Namespaced`, no claims
2. One Composition per implementation in `configuration/compositions/` (KCL / go-templating / patch-and-transform functions)
3. Follow `platform/controlplane/CONVENTIONS.md` (namespaced `.m` managed resources, shared `ClusterProviderConfig` per cloud family)
4. `make build-control-plane` to package; ship via GitOps

Namespaced XRs mean standard RBAC governs who can request what, in which namespace ([ADR-0005](adr/0005-crossplane-v2-namespaced.md)).

## 9. Add a Provider

To target a new cloud/on-prem substrate, implement the `Provider` interface (`platform/providers/interface.go`) — cluster CRUD, node groups, networking, load balancers, storage, health/metrics/cost — and register it in `platform/providers/factory.go`. Use `platform/providers/civo/` as a compact reference implementation and add configuration + schema entries in `config.yaml` / `config.schema.json`. The `custom` provider is the low-effort alternative: point Adhar at any existing conformant cluster and skip provisioning entirely.

For declarative provisioning parity, add matching Crossplane Compositions (§8b) so `CompositeCluster` works on the new provider too.

## 10. Tune Foundation Components

Cilium, ArgoCD, Gitea, the Gateway, and Crossplane core are installed from manifests **embedded in the binary** ([ADR-0006](adr/0006-embedded-bootstrap-manifests.md)) — deterministic, offline-capable, and therefore not user-editable at runtime.

- **User-level knobs** (domain, ports, HA, component enablement) belong on the `AdharPlatform` CR spec — file an issue if a knob you need is missing
- **Platform-developer changes** (new Cilium flag, ArgoCD base config): edit values under `hack/`, regenerate the embedded manifests, rebuild — see [CONTRIBUTING.md](../CONTRIBUTING.md)

---

## Customization Decision Table

| I want to… | Do this | Section |
|------------|---------|---------|
| Turn on Harbor/Vault/Kafka/… | Flip `enabled` in the ApplicationSet / env config | §1 |
| Change a service's settings or version | Edit `values.yaml`, re-render, commit | §2 |
| Add an internal tool to the platform | New package dir + one ApplicationSet entry | §3 |
| Deploy my team's app | `CustomPackage` CR | §4 |
| Add a `qa` environment | New `environments/qa/` + ApplicationSet | §5 |
| Use my company domain + real certs | `config.yaml` host, cert-manager issuer | §7 |
| Offer databases as self-service | Crossplane XRD + Compositions | §8 |
| Run on my private cloud | `custom` provider, or implement the interface | §9 |
