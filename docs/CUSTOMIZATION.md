# Adhar Customization Guide

How to change what the platform ships: turn a package on, retune one, add your own, add a golden path, add an environment, change the domain, extend the infrastructure APIs, or add a cloud.

The golden rule: **customizations are declarative changes to the platform stack, applied with `adhar upgrade`** — never `kubectl apply` against a managed object, which ArgoCD reverts within a minute. If you find yourself patching Go code for a routine customization, open an issue; that is an architecture gap.

Using the platform rather than changing it? [User Guide](USER_GUIDE.md). Starting from zero? [Getting Started](GETTING_STARTED.md).

## Contents

1. [Enable or disable a package](#1-enable-or-disable-a-package)
2. [Change a package's configuration](#2-change-a-packages-configuration)
3. [Add your own package](#3-add-your-own-package)
4. [Deploy team applications (CustomPackage)](#4-deploy-team-applications-custompackage)
5. [Add a golden path](#5-add-a-golden-path)
6. [Create or modify environments](#6-create-or-modify-environments)
7. [Platform configuration layers](#7-platform-configuration-layers)
8. [Domain, TLS, ports and Kubernetes version](#8-domain-tls-ports-and-kubernetes-version)
9. [Extend the infrastructure APIs (Crossplane)](#9-extend-the-infrastructure-apis-crossplane)
10. [Add a provider](#10-add-a-provider)
11. [Tune foundation components](#11-tune-foundation-components)
12. [Decision table](#12-decision-table)

## 1. Enable or disable a package

The catalogue is **91 packages**, wired as **94 ApplicationSet entries** (three packages ship more than one variant: `supply-chain-policies` as audit/enforce, `vllm` as shared surface/cpu/gpu). The local profile enables a curated **32**; production enables **76**.

Each entry carries an `enabled` gate:

```yaml
- name: "harbor"
  enabled: "false"        # ← flip to "true"
  namespace: "adhar-system"
  category: "application"
  manifestPath: "application/harbor/manifests"
```

**Where to edit.** The ApplicationSet is a cluster object the controller applies from the platform stack — it is *not* read from Gitea, so editing it in Gitea has no effect. Edit the two files that define it, in your Adhar checkout, and keep them in step (a parity test enforces the match):

| Profile | ApplicationSet | Mirrored environment config |
| --- | --- | --- |
| Local (Kind) | `platform/stack/adhar-appset-local.yaml` | `platform/stack/environments/local/config.yaml` |
| Production (any cloud / on-prem) | `platform/stack/adhar-appset-production.yaml` | `platform/stack/environments/production/config.yaml` |
| Full-enablement reference | `platform/stack/adhar-appset-gitops.yaml` | — |

Then apply:

```bash
adhar upgrade --diff-only     # exactly what will change
adhar upgrade                 # converge, re-apply the ApplicationSet, re-push the stack
```

ArgoCD reconciles. Setting `enabled: "false"` removes the Application, and with prune on, the workload is uninstalled. Check the result with `adhar get apps` or the ArgoCD UI.

**Two rules that will bite you.**

- *Local capacity.* A single Kind node cannot run all 94 — the full set OOM-kills the node. Check `kubectl top nodes` before enabling anything heavy.
- *Mutual exclusion.* Some packages claim the same cluster-scoped objects in the shared `adhar-system` namespace and must not both be on. `platform/stack/packages/CONFLICTS.md` is the list; the ones you are most likely to hit:

| Group | Rule |
| --- | --- |
| `vault` / `openbao` | Exactly one. Both claim `ClusterSecretStore/vault` and `Service/vault`. Production ships `openbao`; switching means flipping **both** flags |
| `vllm` + `vllm-cpu` / `vllm-gpu` | Enable `vllm` plus exactly one profile. Both Deployments are named `vllm` and share one Service |
| `supply-chain-policies` / `-enforce` | The audit and enforce packs are identical rules differing only in `failureAction`. Audit findings predict exactly what enforce would block |
| `agentgateway` | Hard-depends on `adhar-ai` — enable both together ([User Guide §8](USER_GUIDE.md#8-use-the-ai-layer)) |

## 2. Change a package's configuration

Packages are **pre-rendered Helm charts**, so the Git diff is the cluster diff — no in-cluster Helm surprises ([ADR-0004](adr/0004-applicationset-package-model.md)). Each directory looks like this:

```text
platform/stack/packages/security/cert-manager/
├── adhar-package.yaml        # marketplace contract (category, compatibility, provenance)
├── values.yaml               # your configuration surface
├── generate-manifests.sh     # helm template … -f values.yaml > manifests/install.yaml
└── manifests/
    └── install.yaml          # what ArgoCD syncs (generated — never hand-edit)
```

```bash
# 1. Edit the values
vi platform/stack/packages/security/cert-manager/values.yaml

# 2. Re-render (CHART_VERSION in the script is also how you pin or bump the chart)
cd platform/stack/packages/security/cert-manager && ./generate-manifests.sh

# 3. Review the diff — it is exactly what will change in the cluster
git diff manifests/install.yaml

# 4. Push it to the platform
adhar upgrade
```

Two generator gotchas that have cost real time: pass `--include-crds` when the chart ships CRDs (without it the render has none and the controller crash-loops on an unknown kind), and render with `--namespace adhar-system` so nothing bakes in a different namespace.

## 3. Add your own package

Bring any chart or raw manifests into the same GitOps flow:

```bash
# 1. Create the package directory
mkdir -p platform/stack/packages/application/my-tool/manifests

# 2a. From a Helm chart — copy a neighbouring generate-manifests.sh, set repo/chart/
#     CHART_VERSION, add values.yaml, run it
# 2b. From raw manifests — just place them in manifests/

# 3. Wire it into BOTH the ApplicationSet and the mirrored environment config (§1)
```

```yaml
- name: "my-tool"
  enabled: "true"
  namespace: "adhar-system"
  category: "application"
  manifestPath: "application/my-tool/manifests"
```

Conventions that keep you upgrade-safe:

- Put custom packages in their **own directories** — never inside an Adhar-shipped package dir.
- **Target `adhar-system`** ([ADR-0011](adr/0011-shared-platform-namespace.md)). A shared namespace means Kubernetes injects a `<NAME>_PORT` env var for every Service; if your pod decodes env strictly, set `enableServiceLinks: false`.
- Ship an `adhar-package.yaml` contract — CI fails a PR that adds a package without one. Validate with `hack/validate-packages.sh`; the schema is `platform/stack/packages/marketplace.schema.json` and the submission flow is `platform/stack/packages/MARKETPLACE.md`.
- If the tool has a UI, ship an `HTTPRoute` attaching to `adhar-gateway` so it gets `my-tool.<domain>` like everything else:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-tool
  namespace: adhar-system
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

Write the hostname with the literal local domain. When the stack is seeded, the controller rewrites `adhar.localtest.me:8443` → `<your-host><port-suffix>` and then bare `adhar.localtest.me` → `<your-host>`, so the same manifest serves every platform.

## 4. Deploy team applications (CustomPackage)

Platform packages (§3) are for platform capabilities. **Team workloads** use the `CustomPackage` CRD, which delivers an ArgoCD Application or ApplicationSet through the platform's Gitea — so the cluster never depends on your laptop or an external forge.

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: CustomPackage
metadata:
  name: my-app
  namespace: adhar-system
spec:
  argoCD:
    applicationFile: ./my-app/app.yaml
  replicate: true
```

Ready-made resources are in `examples/`. Related: the `GitRepository` CRD manages repos across Gitea, GitHub, GitLab and Bitbucket (local, remote or embedded sources) — use it when an app's source should live in, or sync from, a specific forge.

For day-to-day deployment (CLI, golden paths, preview environments), see [User Guide §4](USER_GUIDE.md#4-deploy-an-application).

## 5. Add a golden path

There are two template systems, and they serve different surfaces.

**CLI / control-plane templates** — `platform/stack/templates/*.yaml`, pushed to the Gitea `templates` repo at bootstrap. Each is a `CompositeApplication` with `${APP_NAME}` / `${APP_NAMESPACE}` placeholders. These are what `adhar apps deploy <name> --template <t>` instantiates. Shipped: `basic-git`, `microservice`, `frontend`. To add one, drop a new `<name>.yaml` in that directory and run `adhar upgrade`.

**Console golden paths** — `platform/stack/packages/application/adhar-templates/`, Backstage `scaffolder.backstage.io/v1beta3` templates. Shipped: `microservice`, `frontend`, `data-pipeline`, `ml` (plus `basic`, `argo-workflows`, `app-with-bucket`). Adding one:

1. Create `<name>/template.yaml` (parameters, then the steps) and `<name>/skeleton/` (the files to generate).
2. Steps: `fetch:template` from `./skeleton` → `publish:gitea` (creates the repo, `defaultBranch: main`) → `adhar:create-argocd-app` → `catalog:register`. **Omit `adhar:create-argocd-app`** if your skeleton ships `.adhar/app.yaml` — the `adhar-services` ApplicationSet adopts any org repo carrying that descriptor, and a second Application managing the same resources is exactly the drift the platform forbids. The `ml` path is the worked example.
3. Register it by adding `./<name>/template.yaml` to the `targets` list in `adhar-templates/catalog-info.yaml`.
4. Ship a `preview/kustomization.yaml` in the skeleton so every scaffolded repo has per-PR previews from day one ([User Guide §5](USER_GUIDE.md#5-preview-a-pull-request)).
5. Ship `catalog-info.yaml`, `mkdocs.yml` and `docs/` so the service appears in the catalog with TechDocs.

## 6. Create or modify environments

Environments live in `platform/stack/environments/` — one directory each (`local`, `development`, `testing`, `staging`, `production`) with a `config.yaml` declaring that environment's package set. The directory is pushed to the Gitea `environments` repo at bootstrap.

```yaml
environment: staging
type: nonprod
packages:
  - name: cert-manager
    enabled: "true"
    namespace: "adhar-system"
    category: "security"
    manifestPath: "security/cert-manager/manifests"
```

To add an environment: copy the closest existing directory, adjust `environment`, `type` and the package list, and wire an ApplicationSet for it (copy `adhar-appset-local.yaml`, update the generator entries, the `environment` label in the template and the destination). Per-environment package variants use per-env manifest paths — Argo Workflows ships `manifests/dev`, for instance — so one package can carry several rendered flavours.

Promotion between environments is Git promotion: merge the change from `development` → `staging` → `production`. Kargo is available as a package to orchestrate it.

## 7. Platform configuration layers

Root `config.yaml` (validated by `config.schema.json`, JSON Schema draft-07) has four layers, each overriding the previous:

```text
globalSettings          # context, default host, ports (8443), HA mode, email
  └─ providers          # per-cloud credentials & infrastructure (kind, aws, azure, gcp, do, civo, custom)
      └─ environmentTemplates   # reusable defaults (prod-defaults, nonprod-defaults)
          └─ environments       # named instances (dev, test, staging, production) inheriting a template
```

Put organization-wide policy in `globalSettings` and templates; keep each environment block to the minimal delta (`template: nonprod-defaults` plus overrides). **Never put secrets in `config.yaml`** — reference them through External Secrets ([User Guide §11](USER_GUIDE.md#11-secrets-and-policy)).

Select one environment at bootstrap with `adhar up -f config.yaml --env production`.

## 8. Domain, TLS, ports and Kubernetes version

| What | Local default | How to change |
| --- | --- | --- |
| Base domain | `adhar.localtest.me` (wildcard → 127.0.0.1) | `globalSettings.defaultHost` in `config.yaml`, or `adhar up --host`; production adds external-dns |
| HTTPS port | `8443` | `adhar up --port 9443` (HTTP derives as port − 363, here `9080`) |
| Extra host ports | `32222` (Gitea SSH) | `adhar up --extra-ports '22:32222,9090:39090'` |
| TLS certificate | Self-signed, generated at bootstrap | Production: a cert-manager `Issuer`/`ClusterIssuer` (ACME or private CA) referenced by the Gateway |
| Routing | One `HTTPRoute` per service on the shared `adhar-gateway` | Add or modify HTTPRoutes in packages (§3); the Gateway itself is foundation-managed |
| Kubernetes version | `v1.37.0` | See below |

**Kubernetes version precedence**, highest first:

1. an explicitly typed `adhar up --kube-version v1.36.0` — this applies to **every** provider, not just Kind;
2. the environment's `kubeVersion` (or `version`) entry in its `clusterConfig`;
3. the platform default, `globals.DefaultKubernetesVersion` = `v1.37.0`.

The flag carries the platform default as its *default value*, so only an explicitly typed flag overrides what an environment configured — otherwise every cluster would silently be pinned to the CLI's compiled-in version. A worker added later by `adhar cluster scale` or the autoscaler takes its version from the **running control plane**, so a scaled cluster cannot skew.

Domain and port values are never hardcoded in package manifests: the stack is written with the literal local domain and rewritten at seed time, and `.yaml.tmpl` files in the stack are rendered against the platform spec.

## 9. Extend the infrastructure APIs (Crossplane)

The control plane (`platform/controlplane/`) ships the composite APIs (XRDs) and their compositions on Crossplane v2, and is designed for two kinds of extension.

**a) A new implementation of an existing API** — for example your organization's opinionated PostgreSQL behind the standard `CompositeDatabase`. Write a new Composition (Pipeline mode) selecting on your label or parameters; consumers do not change.

**b) A new platform API** — for example `CompositeQueue`:

1. XRD in `configuration/xrd/` — `apiextensions.crossplane.io/v2`, `scope: Namespaced`, no claims.
2. One Composition per implementation in `configuration/compositions/` (function-kcl, function-go-templating, function-patch-and-transform, function-auto-ready or function-python).
3. Follow `platform/controlplane/CONVENTIONS.md`: namespaced `.m` managed resources, a shared `ClusterProviderConfig` per cloud family. Namespaced managed resources have **no `spec.deletionPolicy`** — express retention as `managementPolicies: ["Observe","Create","Update","LateInitialize"]`.
4. `make build-control-plane` to package, then ship it through GitOps.

Namespaced composite resources mean standard RBAC governs who may request what, in which namespace ([ADR-0005](adr/0005-crossplane-v2-namespaced.md)). The full catalogue is in [Control Plane](CONTROL_PLANE.md).

## 10. Add a provider

To target a new cloud or on-prem substrate, implement the `Provider` interface (`platform/providers/interface.go`) — cluster CRUD, node groups, networking, load balancers, storage, health, metrics and cost — and register it in `platform/providers/factory.go`. `platform/providers/civo/` is a compact reference implementation. Add matching configuration and schema entries to `config.yaml` and `config.schema.json`.

The **`custom` provider** is the low-effort alternative: point Adhar at any existing conformant cluster and skip provisioning entirely.

For declarative provisioning parity, add matching Crossplane compositions (§9b) so `CompositeCluster` works on the new provider too.

## 11. Tune foundation components

Cilium, the Gateway, ArgoCD, Gitea and Crossplane core are installed from manifests **embedded in the binary** ([ADR-0006](adr/0006-embedded-bootstrap-manifests.md)) — deterministic and offline-capable, and therefore not editable at runtime.

- **User-level knobs** (domain, ports, HA, component enablement) belong on the `AdharPlatform` CR spec. File an issue if one you need is missing.
- **Platform-developer changes** (a new Cilium flag, ArgoCD base config): edit the values under `hack/`, regenerate the embedded manifests, rebuild. See [CONTRIBUTING.md](../CONTRIBUTING.md).
- `adhar upgrade` is what converges a running platform onto a new binary's foundation.

## 12. Decision table

| I want to… | Do this | Section |
| --- | --- | --- |
| Turn on Harbor / OpenBao / Kafka / … | Flip `enabled` in both the appset and the env config, then `adhar upgrade` | §1 |
| Switch the secrets backend to OpenBao | Flip `vault` off and `openbao` on — never both | §1 |
| Turn on the AI layer | Enable `adhar-ai` **and** `agentgateway`, then add the provider key | §1, [UG §8](USER_GUIDE.md#8-use-the-ai-layer) |
| Change a service's settings or version | Edit `values.yaml`, re-render, `adhar upgrade` | §2 |
| Add an internal tool to the platform | New package dir + contract + two ApplicationSet entries | §3 |
| Deploy my team's app | `CustomPackage` CR | §4 |
| Add a golden path for my org | New Backstage template + skeleton + catalog registration | §5 |
| Add a `qa` environment | New `environments/qa/` + its ApplicationSet | §6 |
| Use my company domain and real certs | `config.yaml` host + a cert-manager issuer | §8 |
| Pin a different Kubernetes version | `adhar up --kube-version`, or the environment's `kubeVersion` | §8 |
| Offer databases as self-service | Crossplane XRD + Compositions | §9 |
| Run on my private cloud | The `custom` provider, or implement the interface | §10 |
