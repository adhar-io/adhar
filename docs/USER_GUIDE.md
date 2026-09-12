# Adhar User Guide

The task reference for a running Adhar platform. Each section answers one question — how do I deploy a service, preview a pull request, get a database, query the platform's data, grade a service, upgrade the platform — with the shortest sequence that actually works, then what it did.

New here? [Getting Started](GETTING_STARTED.md) gets you a platform in 10 minutes. Changing what the platform ships is [Customization](CUSTOMIZATION.md). How it is built is [Architecture](ARCHITECTURE.md).

## Contents

1. [How the platform works](#1-how-the-platform-works)
2. [Find your way around](#2-find-your-way-around)
3. [The CLI](#3-the-cli)
4. [Deploy an application](#4-deploy-an-application)
5. [Preview a pull request](#5-preview-a-pull-request)
6. [Request infrastructure](#6-request-infrastructure)
7. [Query the platform's data](#7-query-the-platforms-data)
8. [Use the AI layer](#8-use-the-ai-layer)
9. [Check production readiness](#9-check-production-readiness)
10. [Observe what is running](#10-observe-what-is-running)
11. [Secrets and policy](#11-secrets-and-policy)
12. [Operate the platform](#12-operate-the-platform)
13. [Troubleshooting](#13-troubleshooting)
14. [Where to go next](#14-where-to-go-next)

## 1. How the platform works

Four facts explain almost everything you will hit.

- **Git is the write path.** The platform's desired state lives in three Gitea repositories under the `adhar` org — `packages` (rendered manifests for every package), `environments` (the package set per environment) and `templates` (service scaffolding). ArgoCD reconciles from them with `selfHeal` on, so a `kubectl edit` on a managed object is reverted within a minute. Change Git instead.
- **Everything is a package behind an `enabled` flag.** 91 packages, wired as **94 ApplicationSet entries** (three packages ship more than one variant). The local profile enables a curated **32**; production enables **76**.
- **One namespace.** Every platform package installs into `adhar-system` (ADR-0011). The single exception is `buildpack` (kpack), which keeps `kpack-system` because kpack's and Cosign's webhooks both hardcode the same Secret name. Your applications get their own namespaces.
- **Self-service infrastructure is a Kubernetes API.** Databases, clusters, networks and environments are requested as namespaced Crossplane composite resources (`CompositeDatabase`, `CompositeCluster`, …) in *your* namespace, so ordinary RBAC decides who may ask for what.

## 2. Find your way around

Every UI is a subdomain of the platform host on the shared Cilium Gateway. Locally that is `<name>.adhar.localtest.me:8443`; in cloud it is `<name>.<your-domain>` on 443. The tables below use the local form.

**Always present (installed during bootstrap):**

| Service | URL | Sign in with |
| --- | --- | --- |
| ArgoCD | `https://argocd.adhar.localtest.me:8443` | `admin` / `adhar get secrets -p argocd`, or Keycloak SSO |
| Gitea | `https://gitea.adhar.localtest.me:8443` | `gitea_admin` / `r8sA8CPHD9!bt6d` (day-0 only), or Keycloak SSO |

**Enabled in the local curated core:**

| Service | URL | What it is |
| --- | --- | --- |
| Adhar Console | `https://console.adhar.localtest.me:8443` | Developer portal: catalog, golden paths, scorecards, cloud shell |
| Keycloak | `https://keycloak.adhar.localtest.me:8443` | Identity provider for every other service |
| Grafana | `https://grafana.adhar.localtest.me:8443` | Metrics, logs, traces, cost |
| Prometheus | `https://prometheus.adhar.localtest.me:8443` | Raw metric queries |
| Headlamp | `https://headlamp.adhar.localtest.me:8443` | Kubernetes UI |
| Hubble | `https://hubble.adhar.localtest.me:8443` | Live network flows |
| Harbor | `https://harbor.adhar.localtest.me:8443` | Container registry (signed + scanned) |
| Nexus | `https://nexus.adhar.localtest.me:8443` | Language artifact repository |
| Tekton | `https://tekton.adhar.localtest.me:8443` | CI pipeline runs |
| LibreDB Studio | `https://libredb.adhar.localtest.me:8443` | Browser SQL IDE — see [§7](#7-query-the-platforms-data) |
| MinIO | `https://minio.adhar.localtest.me:8443` | S3-compatible object storage |
| Kafka UI | `https://kafka-ui.adhar.localtest.me:8443` | Topics and consumer groups |
| Loki / Mimir / Tempo | `https://{loki,mimir,tempo}.adhar.localtest.me:8443` | Log, metric and trace backends (use Grafana normally) |
| Policy Reporter | `https://policy-reporter.adhar.localtest.me:8443` | Kyverno policy results |

Other packages join the same pattern when enabled — `openbao`, `kargo`, `argo-workflows`, `opencost`, `kubeflow`, `trino`, `coder`, `ai`, `mcp`, `vllm`, `agent` and more. The authoritative list for *your* platform:

```bash
kubectl get httproute -A -o custom-columns=NAME:.metadata.name,HOST:.spec.hostnames[*]
```

**Identity.** Keycloak owns the realm `adhar`. Three groups drive access everywhere: `platform-admin` (Gitea `Owners`, cluster-admin, full AI write tools), `platform-developer` (Gitea `developers`), `platform-viewer` (Gitea `viewers`). Sign in from the CLI too:

```bash
adhar auth login
adhar auth whoami      # identity + group membership
adhar auth token       # an OIDC token for scripting
```

**Credentials.**

```bash
adhar get secrets                 # everything the CLI knows about
adhar get secrets -p <service>    # argocd | gitea | keycloak | adhar-console
                                  # vault | postgres | redis | harbor | rustfs
```

## 3. The CLI

`adhar --help` lists 33 top-level commands grouped as Develop / Observe / Operate / Administer. These are the ones you will reach for.

### Lifecycle

```bash
adhar up                     # create or converge the platform (local Kind by default)
adhar up -f config.yaml      # cloud / production, from a config file
adhar upgrade                # converge foundation, diff and re-push the stack
adhar down                   # tear down the local platform
adhar down -f config.yaml --env dev   # tear down a CLOUD environment
```

### Inspection

```bash
adhar get status             # AdharPlatform conditions + per-package health
adhar get apps               # ArgoCD application sync/health (alias of `get applications`)
adhar get all                # comprehensive overview
adhar get secrets [-p svc]   # credentials
adhar get dataplanes         # workload clusters registered with the control plane
adhar health                 # platform health checks
```

### Applications

```bash
adhar apps deploy <name> --template <t> | --repo <url> [--path p] [--wait]
adhar apps list [-A] [-l <selector>]
adhar apps status <name> [--detailed]
adhar apps scale <name> --replicas 3
adhar apps restart <name>
adhar apps bind <name> <service>     # mount a backing service's connection Secret
adhar apps delete <name> [--force]
```

### From source to running, in one command

```bash
adhar push <name> --git-url <url> [--subpath dir] [--wait]
adhar service new --name <name> --git-url <url> [--wait]
```

Both drive the Tekton supply chain: build with buildpacks → scan with Trivy → sign with Cosign → push to Harbor → deploy through GitOps.

### Clusters and environments

```bash
adhar cluster create prod --provider gcp --region us-central1 --worker-replicas 3
adhar cluster list --file config.yaml        # --file is required to reach cloud providers
adhar cluster scale prod --workers 5
adhar cluster upgrade prod --version 1.37.0
adhar cluster kubeconfig prod --print-only > prod.kubeconfig
adhar cluster delete prod

adhar env create dev --provider digitalocean --region blr1 --tier dev
adhar env list
adhar env switch dev
```

**Projects** — an organisation → team → project → application hierarchy with a namespace, quota and (optionally) a Gitea repo:

```bash
adhar project create --name orders --org acme --team payments --tier dev \
  --cpu 4 --memory 8Gi --pods 30
```

### GitOps

```bash
adhar gitops status
adhar gitops sync -a <app> [--prune]
adhar gitops rollback -a <app> --revision <rev>
adhar gitops repo
```

Run `adhar <command> --help` for the full flag surface of anything above.

## 4. Deploy an application

Four paths, in increasing order of platform integration. Pick the lowest one that does what you need.

### a) Point ArgoCD at an existing repo

```bash
adhar apps deploy my-app --repo https://github.com/org/repo --path manifests/ \
  --dest-namespace my-team --wait
```

Fastest way to get something running. The cluster now depends on that repo being reachable.

### b) Instantiate a CLI template

```bash
adhar apps deploy my-app --template microservice --namespace my-app
```

Fetches `<template>.yaml` from the Gitea `templates` repo (`basic-git`, `microservice`, `frontend`), substitutes `${APP_NAME}` / `${APP_NAMESPACE}` and creates a `CompositeApplication`. Crossplane expands it into the ArgoCD Application. The Console instantiates the same templates, so CLI and portal agree.

### c) Scaffold a golden path in the Console

The Console ships four golden paths. Each one creates a **real Gitea repository** with a working, hardened skeleton, then wires it to ArgoCD:

| Golden path | Parameters | What you get |
| --- | --- | --- |
| `microservice` | `name`, `owner` | Go 1.24 service with `/healthz` + `/readyz`, graceful shutdown, JSON logs; multi-stage build to `distroless/static-debian12:nonroot`; Namespace/Deployment/Service/HTTPRoute, hardened (probes, requests+limits, non-root, drop ALL, RuntimeDefault); `preview/` overlay |
| `frontend` | `name`, `owner` | `nginx-unprivileged:1.27-alpine` static site on :8080 with `/healthz` and SPA fallback; same manifest set; `preview/` overlay |
| `data-pipeline` | `name`, `owner`, `description`, `sourceConnector` (postgres/mysql/s3/rest-api/kafka), `targetTable` | Dagster job → Iceberg REST catalog → dbt-trino models; Dagster webserver + Service + HTTPRoute, catalog ConfigMap, daily 06:00 CronJob; `preview/` overlay |
| `ml` | `name`, `owner`, `description`, `modelName`, `framework` (scikit-learn/xgboost/pytorch), `trainingSchedule` | Kubeflow Pipelines v2 training pipeline, FastAPI model server, retraining CronJob — see below; `preview/` overlay |

Every skeleton also emits a Backstage `catalog-info.yaml` (Component + System, TechDocs, ArgoCD and Kubernetes annotations) and `mkdocs.yml` + `docs/`, and every scaffolded service is reachable at `https://<name>.<host>` once its image is built.

The Console executes a scaffold through its own `POST /api/scaffold`, which creates the Gitea repo, commits the generated tree and opens the ArgoCD Application. That endpoint lives in the separate `adhar-io/adhar-console` image, so its request body is not documented here; the underlying mechanism in this repo is the Backstage scaffolder actions each template declares — `publish:gitea` (creates the repo, default branch `main`) and `adhar:create-argocd-app` (`argoInstance: in-cluster`, `projectName: default`, `path: manifests`).

**The `ml` golden path in detail.** It is the one path that does *not* use `adhar:create-argocd-app`; instead the skeleton ships `.adhar/app.yaml`, a `CompositeApplication`, and the platform's `adhar-services` ApplicationSet adopts any org repo carrying that descriptor. You get:

- `pipelines/pipeline.py` — a KFP v2 pipeline (`kfp==2.11.0`) with two components: `load_data` (a scikit-learn sample dataset → parquet, **the placeholder you replace** with a real extract, e.g. the Iceberg table from the `data-pipeline` path) and `train_model` (RandomForest, logging `accuracy` / `f1` / `roc_auc`, `joblib.dump` to `Output[Model]`). Compile with `python pipelines/pipeline.py`; submit with `kfp --endpoint "$KFP_ENDPOINT" run create --experiment-name <modelName> --package-file pipelines/pipeline.yaml`.
- **Artifacts on the platform MinIO.** KFP v2's pipeline root is the shared `data/minio`, so `Output[Model]` *is* the model registry entry — versioned per run, addressable as an `s3://` URI on the run's artifact page. No MLflow package ships; wiring one is a single `MLFLOW_TRACKING_URI` in the ConfigMap plus uncommenting the `mlflow.log_*` calls.
- `serving/main.py` — FastAPI model server on :8080 with `/healthz`, `/readyz` and `POST /predict` taking `{"features": [...]}`. It downloads the model from `MODEL_URI` via boto3 at first use. **With `MODEL_URI` unset it serves a deterministic stub and still reports ready**, so the Deployment and every PR preview are green from the first commit.
- `manifests/cronjob.yaml` — the retraining job, default `0 3 * * 0` (weekly, Sunday 03:00 UTC), `concurrencyPolicy: Forbid`. It does **not** train in-cluster: it submits the compiled pipeline to Kubeflow Pipelines so runs are tracked and artifacts land in the platform store.
- ConfigMap `<name>-model` carrying `MODEL_NAME`, `MODEL_VERSION`, `MODEL_URI`, `S3_ENDPOINT`, `KFP_ENDPOINT`, `KFP_EXPERIMENT`, `TRAINING_FRAMEWORK`.

What **you** must supply: a real `load_data`, object-store credentials as an ExternalSecret named `<name>-object-store` (keys `accesskey` / `secretkey`; the reference is `optional: true`, so the server starts without it), and a first image build. KServe is shipped as an opt-in swap at `serving/kserve-inferenceservice.yaml` — it is deliberately outside `manifests/` because the platform ships no KServe package.

### d) `CustomPackage` for team workloads

The platform-native path: your app manifest is pushed into Gitea, so the cluster never depends on an external forge. See [Customization §4](CUSTOMIZATION.md#4-deploy-team-applications-custompackage) and `examples/`.

### Paved-road CI

A repo carrying `.adhar/app.yaml` is adopted by the `adhar-services` ApplicationSet. A push then fires the supply-chain package's Tekton `app-ci` EventListener through a Gitea webhook: build (buildpacks or Dockerfile) → Cosign sign → Trivy scan → push to Harbor as **both `:latest` and `:<commit sha>`** → open a version-bump PR against the `environments` repo → report status back to the commit. A `pull_request` trigger builds the head of any PR labelled `preview`, with no writeback to `main`.

## 5. Preview a pull request

Every pull request can get its own running copy of your application at its own URL: created when the PR is labelled, updated on every push, destroyed when it closes. It is shipped by the `application/preview-environments` package (enabled in the local, production and gitops profiles) and is entirely declarative — nothing in CI creates or deletes an environment.

### One-time, per repository

Add a Kustomize overlay at `preview/kustomization.yaml` on your **default branch**. Its existence is what opts the repository in. The four golden-path skeletons already ship it.

```yaml
# preview/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../manifests          # or ../deploy — wherever your manifests live
patches:
  - patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata: {name: myapp}
      spec:
        template:
          spec:
            containers:
              - name: app
                image: harbor-core.adhar-system.svc.cluster.local/library/myapp:latest
  - target: {kind: Namespace}
    patch: |-
      - op: add
        path: /metadata/labels/adhar.io~1plane
        value: preview
      - op: add
        path: /metadata/labels/adhar.io~1preview
        value: "true"
```

Two things the overlay must do: declare the **Namespace as a managed resource** (a namespace created only by `CreateNamespace=true` would be unmanaged and survive the prune), and carry the two preview labels, which is what the guardrail policy matches on.

> A PR that *adds* `preview/` does not preview itself — the generator evaluates the default branch. The first preview starts after that merge.

### Then, per pull request

Label the PR **`preview`**.

| Step | What happens |
| --- | --- |
| 1. You label the PR | Gitea fires a `pull_request` webhook |
| 2. Tekton `app-ci` builds the PR head | image pushed to Harbor as `library/<repo>:<head sha>`, scanned and signed; **no** writeback to `main` |
| 3. ArgoCD's pull-request generator sees it (polls every **60 s**) | Application `<repo>-pr-<number>` appears |
| 4. ArgoCD syncs `preview/` at the head SHA | namespace `preview-<repo>-<number>`, image pinned to the head SHA, HTTPRoute host rewritten |
| 5. Kyverno stamps the namespace | `ResourceQuota/preview-quota` + `LimitRange/preview-limits` |
| 6. **Your URL** | `https://<repo>-pr-<number>.adhar.localtest.me:8443` (locally; your platform domain in cloud) |

The stamped guardrails, exactly:

| ResourceQuota `preview-quota` | | LimitRange `preview-limits` (Container) | cpu | memory |
| --- | --- | --- | --- | --- |
| `requests.cpu` | `2` | `default` | `250m` | `256Mi` |
| `requests.memory` | `4Gi` | `defaultRequest` | `50m` | `64Mi` |
| `limits.cpu` | `4` | `max` | `1` | `2Gi` |
| `limits.memory` | `8Gi` | | | |
| `pods` | `20` | | | |
| `persistentvolumeclaims` | `2` | | | |
| `services.nodeports` / `services.loadbalancers` | `0` | | | |

Every push moves the head SHA: CI rebuilds, ArgoCD re-syncs, the same URL serves the new commit.

**Closing or merging the PR — or removing the `preview` label — deletes the Application**, and the `resources-finalizer.argoproj.io` finalizer prunes everything it owned, namespace included. There is no reaper job and nothing to clean up by hand.

Two things to know:

- **Previews are not behind SSO.** Per-PR hostnames would need a per-PR OIDC client. Previews are unauthenticated and reachable only on the platform Gateway (locally `localhost:8443`; in cloud whatever fronts it, typically a private LB or VPN). Treat a preview as internal, and give it synthetic data only — never a production database.
- **Preview capacity is real capacity.** N open previews × the quota must fit the cluster. On a local Kind node, keep it to one or two. An abandoned-but-open PR keeps its preview indefinitely; the quota bounds it.

For a repo that needs something the shared package does not do — an ephemeral CNPG database per preview, a different base path, its own ArgoCD project — [`examples/preview-environments-appset.yaml`](../examples/preview-environments-appset.yaml) is a single-repo ApplicationSet to copy into the `environments` repo. Design rationale and rejected alternatives: [ADR-0017](adr/0017-preview-environments.md).

## 6. Request infrastructure

Ask for what you need in *your* namespace; the platform decides how to provision it. Requests are namespaced Crossplane composite resources, so RBAC governs who may ask for what, in which namespace.

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: CompositeDatabase
metadata:
  name: orders-db
  namespace: team-orders
spec:
  parameters:
    engine: postgres
    size: small
```

Locally this becomes a CNPG PostgreSQL cluster; on AWS the same request becomes RDS. The shipped APIs include `CompositeCluster`, `CompositeApplication`, `CompositeDatabase`, `CompositeNetwork`, `CompositeLogging`, `CompositeEnvironment` and `CompositePlatformConfig`, each backed by one composition per implementation. Working examples are in `examples/`; the full catalogue and conventions are in [Control Plane](CONTROL_PLANE.md).

Bind the resulting connection Secret into an application:

```bash
adhar apps bind my-app orders-db
```

Workload clusters provisioned this way register themselves with ArgoCD and appear in `adhar get dataplanes`.

## 7. Query the platform's data

**LibreDB Studio** (`https://libredb.adhar.localtest.me:8443`) is a browser SQL IDE over 16 engines, enabled in the local, production and gitops profiles. Sign in with Keycloak — no separate account. Membership of `platform-admin`, `platform-engineer` or `admin` makes you a Studio admin (audit log, fleet health, the admin-only connections); every other realm user is a normal user.

**13 platform datasources are pre-wired** and read-only in the UI — credentials are resolved server-side from the operators' own Secrets, so nobody has to copy a password anywhere:

| Connection | Engine | Visible to |
| --- | --- | --- |
| Gitea (platform Git) | postgres | admins |
| Keycloak (identity) | postgres | admins |
| Adhar Console | postgres | admins |
| LibreDB Studio (own storage) | postgres | admins |
| PostHog | postgres | everyone |
| Metabase (BI metadata) | postgres | everyone |
| OpenMetadata (catalog) | postgres | everyone |
| Adhar AI RAG (pgvector) | postgres | everyone |
| Adhar Cache (Valkey) | redis | everyone |
| Redis | redis | everyone |
| OpenSearch | opensearch | everyone |
| ClickHouse (PostHog) | clickhouse | everyone |
| Trino | trino | everyone |

A connection whose backing package is disabled simply does not appear.

**Two connections need an operator to supply a password**: OpenSearch and ClickHouse hold their credentials as literal env values inside the packages that run them, so there is no Secret for External Secrets to mirror. Create one Secret and both light up:

```bash
kubectl -n adhar-system create secret generic libredb-datasource-extras \
  --from-literal=OPENSEARCH_PASSWORD="$(kubectl -n adhar-system get statefulset opensearch-cluster-master \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="OPENSEARCH_INITIAL_ADMIN_PASSWORD")].value}')" \
  --from-literal=CLICKHOUSE_USER=admin \
  --from-literal=CLICKHOUSE_PASSWORD="$(kubectl -n adhar-system get deploy posthog-web \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CLICKHOUSE_PASSWORD")].value}')"
```

Studio re-reads its seed file on a 60-second cache, so the connections appear shortly after. Until then it logs one `Seed connection skipped` line per unresolved connection at ERROR level — expected, not a fault.

The **Adhar AI RAG** connection is listed but ships no ExternalSecret on purpose: `ai/adhar-ai` is opt-in, and an ExternalSecret whose remote key is absent fails and makes ArgoCD retry the Application forever. After enabling `ai/adhar-ai`, add a `libredb-ds-adhar-ai` ExternalSecret by copying any of the shipped Postgres blocks in the package's `manifests/datasources.yaml`.

## 8. Use the AI layer

The AI stack is **opt-in and disabled in every profile**. Nothing in the platform depends on it, and the platform runs unaffected while it is off or unkeyed. Three packages compose it:

| Package | Role | Endpoint |
| --- | --- | --- |
| `ai/agentgateway` | The AI data plane — one proxy carrying identity, authorization, guardrails, budgets and telemetry for every LLM and MCP request | `https://ai.<host>/v1`, `https://mcp.<host>/mcp` |
| `ai/adhar-ai` | The agent runtime and seven MCP tool servers | `https://agent.<host>` |
| `ai/vllm` | Optional self-hosted, OpenAI-compatible inference so prompts never leave the cluster | `http://vllm.adhar-system.svc.cluster.local:8000/v1` |

### One endpoint, model-name routing

`https://ai.<host>/v1/chat/completions` is OpenAI-compatible. **Callers name a model, never a provider** — the gateway lifts `.model` out of the request body and routes on it:

| Model name matches | Goes to |
| --- | --- |
| `claude-*`, `anthropic/*` | Anthropic |
| `gpt-*`, `o1-*`…`o9-*`, `chatgpt-*`, `openai/*` | OpenAI |
| `local/*`, `vllm/*` | the in-cluster vLLM |
| anything else | Anthropic (the platform default posture) |

Neither hosted backend pins a model, so a new Claude or GPT model works the day it ships. Provider keys are read server-side from the `adhar-ai-llm` Secret and never reach a caller or a model context. In-cluster callers use `http://adhar-ai-gateway.adhar-system.svc.cluster.local:8080/v1`.

### One MCP endpoint for external agents

`https://mcp.<host>/mcp` federates all seven MCP tool servers into a single tool list, so Claude Code, an IDE or ChatOps gets every governed tool from one URL with a standard OIDC token and no Adhar-specific client code. Tool names are permanently prefixed by server (`gitops_open_pull_request`), and a restarting server costs its own tools rather than the session.

| Tool server | Access | What it does |
| --- | --- | --- |
| `cluster` | read-only | Cluster and workload state |
| `observability` | read-only | Prometheus, Loki and Tempo queries |
| `cost` | read-only | OpenCost queries |
| `gitops` | write → PR | ArgoCD/Git state and change proposals |
| `provision` | write → PR | Infrastructure requests |
| `security` | write → PR | Policy and findings |
| `catalog` | write → PR | Service catalog entries |

### What "write" means

**Every agent write opens a Gitea pull request. Nothing is applied to a cluster.** This is architectural, not policy:

- The agent's ServiceAccount has **no write RBAC at all** — its ClusterRole is `get`/`list`/`watch` on everything it can see. The only namespaced writes it holds are two of its own ConfigMaps. It physically cannot mutate cluster state with its identity.
- Its **only** write credential is a Gitea bot token, scoped to the `packages` and `environments` repos, mounted into only the four PR-authoring servers. There is no `kubectl_apply`, `argo_sync`, `helm_install` or cloud-mutation tool anywhere.
- The gateway's CEL authorization grants LLM completions and read tools to `platform-developer`, and the PR-opening tools only to `platform-admin`. `platform-viewer` gets nothing, because an LLM call spends money.
- A Kyverno policy (`adhar-ai-guardrails`) reports direct workload mutation by the agent's ServiceAccount. It ships in **Audit** mode, so it reports rather than blocks — the RBAC is what prevents it.

**Staged autonomy** is the ladder the runtime operates on, default `suggest`:

| Stage | Behaviour |
| --- | --- |
| `read-only` | Investigate and answer; no write tools offered at all |
| `suggest` (default) | Writes yield a PR and pause for a human merge |
| `approve-to-apply` | Writes yield a PR; CI may auto-merge under policy |
| `scoped` | Narrow, policy-gated auto-merge on allowlisted paths — still a Git PR, never a direct mutation |

Shipped operators stay at or below `suggest`: `alert-triage` (suggest), `drift-explain` (read-only), `cost-advisor` (suggest), `upgrade-preflight` (suggest).

### Guardrails, budgets and telemetry

Permissive by default, and said out loud. Regex guards **Mask** credentials (AWS keys, `sk-` keys, JWTs, PEM keys, DB connection strings) in prompts and responses; PII detectors (credit card, SSN, email, phone) are **Audit** only. Per-group budgets are enforced in the proxy: `platform-admin` 120 req/min and 250 000 tokens/hour, everyone else 60 req/min and 100 000 tokens/hour. Each control documents the one field that makes it enforcing.

Two caveats worth knowing: masking is **not applied to streamed responses** (the Console chat streams), and budgets are per proxy instance — exact today because a single replica is provisioned.

Traces go to Tempo with OTel GenAI attributes (`gen_ai.request.model`, `gen_ai.usage.*_tokens`, `gen_ai.usage.cost_usd`, `mcp.tool.*`), and a Grafana dashboard *Adhar AI Gateway (agentgateway)* turns AI cost and AI latency into first-class platform signals.

### Turning it on

1. Enable `adhar-ai` **and** `agentgateway` together — see [Customization §1](CUSTOMIZATION.md#1-enable-or-disable-a-package). agentgateway hard-depends on the seven MCP Services; if any is missing, its whole MCP backend is rejected at config-translation time until they appear (LLM routes are unaffected).
2. Put the keys in the secrets backend. Every property listed must exist — an **absent** property fails the whole ExternalSecret, while an empty string is fine:

   ```bash
   bao kv put secret/adhar-ai/llm \
     PROVIDER="claude" \
     API_KEY="sk-ant-..." \
     ANTHROPIC_API_KEY="sk-ant-..." \
     OPENAI_API_KEY="" \
     MODEL="claude-opus-4-5-20251101" \
     ENDPOINT="" \
     BUDGET_PER_USER_DAILY_TOKENS="2000000" \
     BUDGET_PER_OP_MAX_TOOL_CALLS="40"
   ```

   An empty `OPENAI_API_KEY` leaves only `gpt-*` requests unserved. With no entry at all, the gateway still starts in "claude, no key" posture rather than hard-failing the sync.
3. For PR-opening tools, add the Gitea bot token:

   ```bash
   bao kv put secret/adhar-ai/bot \
     GITEA_BOT_USER="adhar-ai-bot" \
     GITEA_BOT_TOKEN="<gitea PAT, repo scope>"
   ```

Other provider layouts are documented in the package: `PROVIDER=openai` + `MODEL=gpt-4o`; `PROVIDER=azure` + `ENDPOINT` + a deployment name; `PROVIDER=openai-compatible` + `ENDPOINT`; `PROVIDER=ollama`.

### Self-hosted inference (vLLM)

`ai/vllm` ships as three ApplicationSet entries from one directory. **Enable `vllm` plus exactly one profile** — both Deployments are named `vllm` and share one Service, so enabling both makes two Applications fight over the same object.

| Entry | Image | Model | Requests |
| --- | --- | --- | --- |
| `vllm` | — | shared surface: 20Gi model-cache PVC, Service, HTTPRoute, ServiceMonitor, dashboard, optional HF-token secret | — |
| `vllm-cpu` | `vllm/vllm-openai-cpu:v0.29.0` | `Qwen/Qwen2.5-0.5B-Instruct` (`--max-model-len 4096`) | 2 CPU / 6Gi (limits 4 / 8Gi) |
| `vllm-gpu` | `vllm/vllm-openai:v0.29.0` | `Qwen/Qwen2.5-7B-Instruct` (`--max-model-len 32768`) | 4 CPU / 16Gi / 1 `nvidia.com/gpu` |

Any OpenAI SDK works against the in-cluster URL; pass any non-empty key, since the engine itself has no auth:

```python
from openai import OpenAI
client = OpenAI(base_url="http://vllm.adhar-system.svc.cluster.local:8000/v1", api_key="unused")
client.chat.completions.create(model="qwen2.5-0.5b-instruct",
                               messages=[{"role": "user", "content": "hello"}])
```

There is also a direct route at `https://vllm.<host>/v1` for curling from a laptop. **It is unauthenticated** — a debugging path. Do not expose it on an internet-facing platform: delete the package's `httproute.yaml`, or front it with the Keycloak oauth2-proxy pattern from `application/n8n`. Real access control belongs in agentgateway.

Locally, free headroom first: the CPU profile asks for 6Gi on top of the 32-app core, and cold start is image pull → ~1 GB model download → torch compile (all cached on the PVC, so the second start is fast). The GPU profile needs a GPU node pool with the NVIDIA device plugin advertising `nvidia.com/gpu` and nodes labelled `nvidia.com/gpu.present=true`; without it the pod stays `Pending` forever. Watch it in Grafana → *Platform* → **Adhar - vLLM Inference** (throughput, time-to-first-token, queue depth, KV-cache utilisation); sustained preemptions mean the KV cache is full. Gated models (Llama, Gemma) need a HuggingFace token at `secret/vllm/hf`, property `HF_TOKEN`, read through the `vault` ClusterSecretStore.

## 9. Check production readiness

The `application/scorecards` package grades every service 0–100 (A–F) from real in-cluster signals and surfaces it in the Console — a platform grade, a per-category breakdown and the full signal ledger. It is **enabled in production and gitops, off in the local core** (turn it on to see per-package readiness locally).

A CronJob runs every 30 minutes and writes ConfigMap `adhar-system/adhar-scorecards`.

| Category | Weight | Signal | Passes when |
| --- | --- | --- | --- |
| Reliability | 35 | `argocd_healthy` | the Application is `Healthy` |
| Reliability | 35 | `probes` | **every** container has both a readiness and a liveness probe |
| Reliability | 35 | `resources` | **every** container sets both requests and limits |
| Security | 25 | `image_not_latest` | no `:latest` and no untagged image (a `@sha256:` digest passes) |
| Security | 25 | `kyverno_pass_rate` | fractional — `pass / (pass + fail)` across PolicyReports |
| Observability | 15 | `argocd_synced` | the Application is `Synced` |
| Observability | 15 | `httproute_exposed` | the Application **owns** at least one HTTPRoute |
| Operations | 25 | `backup` | *stateful services only* — a Velero `Schedule` covers the namespace (or `*`), or the owned CNPG `Cluster` sets `.spec.backup` |

Grades: **A** ≥ 90, **B** ≥ 80, **C** ≥ 70, **D** ≥ 60, **F** below. Weights and thresholds are operator-tunable in ConfigMap `adhar-scorecard-config`.

A signal that cannot be evaluated is marked not-applicable and drops out of its category's denominator — missing data is neutral, never a zero. Attribution is by **what ArgoCD says the Application owns** (`Application.status.resources`), not by namespace: since every platform package shares `adhar-system`, a namespace-keyed signal would be identical for all of them. So an Application that owns no workload (an ApplicationSet, a policy bundle) is scored on what applies to it rather than borrowing its neighbours' results.

**Raising a score** is per signal: get the app Healthy and Synced; add both probes and both resource bounds to *every* container; replace `:latest` with a tag or digest; clear Kyverno failures on your own workloads (this one is fractional, so partial progress counts); ship an HTTPRoute **in your own manifests**; and for stateful services, add a Velero schedule or CNPG backup.

Run it on demand and read the ledger:

```bash
kubectl -n adhar-system create job --from=cronjob/adhar-scorecard-scorer scorecard-now
kubectl -n adhar-system get configmap adhar-scorecards -o jsonpath='{.data.summary\.json}' | jq .
```

To show a grade on a Backstage component, annotate it `adhar.io/scorecard: <argocd-application-name>`.

## 10. Observe what is running

- **Metrics** — Grafana dashboards (cluster, nodes, ArgoCD, per-app, AI, vLLM); Prometheus and Mimir behind them.
- **Logs** — Grafana → Explore → Loki. `{namespace="team-orders"}`, `{app="my-app"} |= "error"`.
- **Traces** — Tempo, ingested via Alloy over OTLP; Beyla provides eBPF auto-instrumentation when enabled.
- **Network** — the Hubble UI for live flows, invaluable when debugging connectivity or authoring network policies.
- **Cost** — OpenCost, for namespace and team attribution.
- **Policy** — Policy Reporter for Kyverno results; ClusterPolicyReports also drive the Console's policy views.

Anything composed through the control plane appears in Grafana automatically, because compositions emit an exporter plus a ServiceMonitor/PodMonitor and kube-prometheus selects all of them.

## 11. Secrets and policy

**Secrets never live in Git.** Manifests reference an `ExternalSecret`, and External Secrets fetches the value from the backend at runtime.

In production the backend is **OpenBao** (`security/openbao`) — the Linux Foundation's MPL-2.0 fork of Vault, which replaced the BUSL-licensed `security/vault` package. It is wire-compatible, and the platform deliberately kept every name you might already reference:

| What you write | Value |
| --- | --- |
| `ClusterSecretStore` name | **`vault`** (unchanged) |
| KV v2 mount | `secret/` |
| Address | `http://openbao.adhar-system.svc.cluster.local:8200` (alias `http://vault.adhar-system.svc.cluster.local:8200`) |
| UI | `https://openbao.<host>` (Keycloak SSO) |

So nothing you wrote against Vault changes. Prometheus metric names stay `vault_*` too, so existing dashboards keep working. **Exactly one of `vault` and `openbao` may be enabled** — both claim `ClusterSecretStore/vault` and `Service/vault` in the shared namespace. Locally both ship disabled: a single Kind node needs no secrets backend, and the SSO and credential chains fall back to the kubernetes-provider `keycloak` and `gitea` stores.

Write a secret and reference it:

```bash
bao kv put secret/myapp/config username=foo password=bar
```

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: myapp-config, namespace: my-team}
spec:
  secretStoreRef: {name: vault, kind: ClusterSecretStore}
  target: {name: myapp-config}
  data:
    - secretKey: password
      remoteRef: {key: myapp/config, property: password}
```

OpenBao initialises and unseals itself through an idempotent ArgoCD Sync-hook Job, storing the unseal key and root token in Secret `adhar-system/openbao-keys`. Keycloak groups map to OpenBao policies of the same name (`platform-admin`, `platform-developer`, `platform-viewer`). **Harden this before production**: where a cloud KMS exists, configure auto-unseal (`seal "awskms" {}` and friends), drop the init/unseal steps, and revoke the root token after creating scoped admin tokens. Secrets do not migrate from Vault automatically — export and re-import.

**Policy.** Kyverno admits or rejects workloads; the denial message names the policy. Supply-chain policies (signature verification, no-`:latest`, registry allowlist) ship **twice** — an Audit pack enabled everywhere and an identical Enforce pack wired off — so Audit findings predict exactly what Enforce would block. Platform namespaces are excluded from every rule twice over, by name and by label, so flipping Enforce cannot brick the platform.

**Images** should come from Harbor: Trivy scans and Cosign verification gate what runs.

## 12. Operate the platform

### Ports and URLs (local)

The Kind node maps host ports to the Gateway's pinned NodePorts; the Cilium Gateway terminates TLS and routes to the platform services. All `*.adhar.localtest.me` names resolve to `127.0.0.1`.

| Purpose | Host port | Gateway NodePort | Backend |
| --- | --- | --- | --- |
| HTTPS | `8443` | `30443` | Cilium Envoy → HTTPS listener (443) |
| HTTP | `8080` | `30080` | Cilium Envoy → HTTP listener (80) |
| HTTPS (on-node OIDC) | `8443` | `8443` | pinned so `https://keycloak.<host>:8443` resolves on-node for kube-apiserver OIDC discovery |
| Gitea SSH | `32222` | `32222` | Gitea SSH |

`--port` changes the HTTPS host port; HTTP auto-derives as `port − 363` (`--port 9443` → `9080`).

### `adhar up` flags

With no config file `adhar up` creates a **local Kind** platform and runs the controllers in-process, exiting when the platform converges. With `-f config.yaml` it provisions a **production** cluster through the provider factory and installs the in-cluster controller manager for continuous reconciliation.

| Flag | Effect |
| --- | --- |
| `-f, --file <cfg>` | Production mode: provision from a resolved config file |
| `--env <name>` | Target one environment from that config |
| `--recreate` | **Destructive.** Delete the existing Kind cluster first |
| `--port <n>` | HTTPS host port (default `8443`); HTTP derives as `n − 363` |
| `--host <name>` | Platform host name (default `adhar.localtest.me`) |
| `--kube-version <v>` | Kubernetes version for **any** provider (default `v1.37.0`) |
| `--ha` | Render foundation components in HA mode (replicas, PDBs, HA redis, CNPG for Gitea) |
| `--in-cluster` | Also install the `adhar-controller-manager` Deployment (always done in production mode) |
| `-d, --dry-run` | Preview without applying |
| `--dev-password` | Set ArgoCD and Gitea admin passwords to `developer` |
| `--kind-config <path>` | Custom Kind configuration file or URL |
| `--extra-ports <map>` | Extra host ports, e.g. `'22:32222,9090:39090'` |
| `-p, --package <path>` | Additional custom package locations (default `platform/stack`) |
| `-w, --watch` | Keep running and continuously sync directories (default on) |

**Kubernetes version precedence**, highest first: an explicitly typed `--kube-version` → the environment's `kubeVersion` / `version` in `clusterConfig` → the platform default `v1.37.0`. A worker added later by `adhar cluster scale` or the autoscaler takes its version from the **running control plane**, so a scaled cluster cannot skew.

### What a healthy bootstrap looks like

- **Foundation installed in order** — Gateway API CRDs → Cilium → Gateway → (CNPG, if HA) → ArgoCD → Gitea → Crossplane. Every manifest is embedded in the binary (`//go:embed`), so `adhar up` is offline-capable.
- **Gateway `Programmed=True`**, with the `cilium-gateway-adhar-gateway` Service a NodePort pinned to `30080` / `30443` / `8443`.
- **GitOps seeded** — the `adhar` org exists in Gitea with `packages`, `environments` and `templates` populated, and the `gitea-argocd` Service (ArgoCD → Gitea repo auth) is present.
- **2 ApplicationSets** — the platform one plus the workload one (which generates nothing until workload clusters register).
- **32 applications** converging locally. A few (CNPG-backed apps, Keycloak) sit `Progressing`/`Degraded` for the first 2–3 minutes; that is normal.
- **`AdharPlatform` conditions all True** — `ArgoCDReady`, `GatewayReady`, `GiteaReady`, `CrossplaneReady`, `GitOpsReady`, and aggregate `Ready`.

### Verify it is healthy

```bash
adhar get status                          # conditions + per-app health, in one shot
kubectl get applications -n adhar-system \
  -o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status
curl -sk -o /dev/null -w 'argocd HTTP %{http_code}\n' https://argocd.adhar.localtest.me:8443
```

[Troubleshooting](TROUBLESHOOTING.md#1-is-it-actually-healthy) carries the full copy-pasteable checklist — Gateway `Programmed`, pinned NodePorts, ApplicationSets, Gitea org seeding, end-to-end reachability — and maps every failing check to a recovery section.

### Resume an interrupted bootstrap

Re-running `adhar up` **without `--recreate`** is the designed way to resume. It is safe because the reconcile returns early on a healthy existing node, every foundation manifest re-applies with server-side apply and `ForceOwnership` (re-adopting rather than duplicating), repo **seeding** is guarded by a status flag, and the ArgoCD **ApplicationSet is re-applied on every reconcile** — it is not gated on the repos already existing.

This matters most locally, where the controller is in-process and exits on convergence: if it is interrupted (Ctrl-C, closed terminal, laptop sleep), nothing is left to retry, and re-running `adhar up` resumes from the exact gate that was pending.

| Situation | Command |
| --- | --- |
| Bootstrap interrupted or a phase stalled | `adhar up` (no `--recreate`) |
| Platform healthy, want to push stack changes | `adhar upgrade` |
| Wedged node you do not care about | `adhar up --recreate` (**destructive**) |

### Upgrade

```bash
adhar upgrade --diff-only     # show the stack diff, change nothing
adhar upgrade                 # converge foundation, re-apply ApplicationSet, re-push stack, sync
adhar upgrade -y              # non-interactive (CI)
adhar upgrade --skip-foundation   # only diff and sync the stack
```

Phase 1 re-applies this binary's embedded foundation manifests (unchanged ones are no-ops). Phase 2 diffs the local platform stack against the in-cluster Gitea repos, and on confirmation force-pushes, re-applies the ApplicationSet and requests an ArgoCD refresh. Run it from the repository root, or pass `--stack-dir`.

### Teardown

```bash
adhar down              # remove the local Kind node and Adhar resources
adhar up --recreate     # delete and recreate in one step
```

### Local vs production

The same CRDs, reconcile pipeline and embedded manifests drive both. They differ in size and in where the controller lives:

| | Local (Kind) | Production (cloud / on-prem) |
| --- | --- | --- |
| Entry | `adhar up` | `adhar up -f config.yaml` |
| Cluster | one `adhar` Kind node (CNI and kube-proxy off) | provider factory: `aws`/`azure`/`gcp`/`digitalocean`/`civo`/`custom` |
| Controller | **in-process, exits on convergence** | in-process bootstrap → `adhar-controller-manager` Deployment, continuous |
| Foundation | single replica | HA: replicas, PDBs, HA redis, CNPG for Gitea |
| Gateway edge | NodePort pinned 30080/30443/8443 | LoadBalancer + cert-manager listener cert |
| Packages enabled | 32 of 94 | 76 of 94 |
| Secrets backend | none (kubernetes-provider stores) | OpenBao |
| Recovery | **re-run `adhar up`** | the in-cluster controller self-heals every 15s |

Production HA and DR posture: [Production Guide](PRODUCTION.md).

## 13. Troubleshooting

| Symptom | Check |
| --- | --- |
| App stuck `Progressing`/`Degraded` | ArgoCD app → Events, then `kubectl -n <ns> describe pod` — often a Kyverno denial or a missing quota |
| Service URL 404 | Is the app `Healthy`? Does it ship an HTTPRoute? `kubectl get httproute -A` |
| `OutOfSync` will not heal | ArgoCD diff view — Git and cluster diverged; Git wins on sync |
| Cannot reach another service | `hubble observe --namespace <ns>` — look for `DROPPED` (network policy) |
| Pod `Pending` | `kubectl describe pod` — node resources (local) or nodepool autoscaling (cloud) |
| Platform component unhealthy | `kubectl -n adhar-system get adharplatform -o yaml` conditions; controller logs |
| Every app flips `OutOfSync` after a Gitea push | Expected. Any push to `packages` re-triggers all ~80 apps; it settles in about 10 minutes |

**A wave-stuck ArgoCD sync replays the revision it started with.** When an Application sits at `waiting for healthy state of <resource>`, pushing a fix to Gitea changes nothing and `kubectl` edits are reverted within a minute. Patching `operation: null` does *not* clear it. Terminate through the API and hard-refresh:

```bash
curl -sk -X DELETE -H 'content-type: application/json' \
  -H "Authorization: Bearer $ARGOCD_TOKEN" \
  https://argocd.adhar.localtest.me:8443/api/v1/applications/<app>/operation
```

The `content-type` header is required — without it the API returns 415.

The debugging toolbox:

```bash
adhar get status && adhar get apps
kubectl -n adhar-system get pods
kubectl -n adhar-system logs deploy/argo-cd-argocd-server
cilium status
hubble observe --since 5m --namespace <ns>
```

Failure signatures with root-cause probes and recovery steps: [Troubleshooting](TROUBLESHOOTING.md).

## 14. Where to go next

| You want to… | Read |
| --- | --- |
| Change packages, values, environments, providers | [Customization Guide](CUSTOMIZATION.md) |
| Run it for real — HA, hardening, backup/DR | [Production Guide](PRODUCTION.md) |
| Set up a specific cloud | [Provider Guide](PROVIDER_GUIDE.md) |
| Build on the Crossplane APIs | [Control Plane](CONTROL_PLANE.md) |
| Understand the design and its decisions | [Architecture](ARCHITECTURE.md) and the [ADRs](adr/README.md) |

Community: [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww) · [Discussions](https://github.com/adhar-io/adhar/discussions) · [Issues](https://github.com/adhar-io/adhar/issues)
