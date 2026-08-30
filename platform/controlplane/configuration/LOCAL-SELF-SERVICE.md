# Local Self-Service Provisioning

Developers request infrastructure (databases, caches, buckets, Kafka, secrets,
environments, applications, …) through the **Adhar Console or the `adhar` CLI**.
Either entry point produces the **same Crossplane composite resource (XR)** in the
developer's namespace, and the **control plane (Crossplane)** composes the real
backing resource. The identical XR maps to cloud services in cloud environments —
the request is portable.

```
Console (template)  ┐
                    ├─► CompositeX (XR, namespaced) ─► Crossplane ─► backing resource
adhar CLI (create)  ┘         provider-aware               (local / cloud composition)
                              compositionSelector
```

## One control plane, two frontends

Both the CLI and the Console converge on the **same XR + the same composition
selection contract**, so a resource request behaves identically regardless of
where it was made — and day-2 operations (status, delete, scale) act on the same
XR.

- **Selection is provider-aware.** Every composition is labelled
  `provider` + `feature` + a discriminator (`engine`/`type`). Requests select via
  `spec.crossplane.compositionSelector.matchLabels` keyed on the **active
  provider** (default `local`, override with `ADHAR_PROVIDER`). Switching the
  provider swaps the entire backing implementation with no change to the request —
  e.g. `CompositeDatabase{engine: postgresql}` resolves to CloudNativePG locally
  and AWS RDS on `ADHAR_PROVIDER=aws`.
- **CLI:** `cmd/helpers/controlplane.go` (`NewXR`, `CompositionSelector`,
  `ApplyXR`) is the single path all `adhar <resource> create` commands use.
- **Console:** Backstage scaffolder templates render the same XR (with the same
  labels) and apply it via `adhar:kubernetes:apply`, optionally committing it to
  Gitea first (`publish:gitea`) so the request is Git-tracked (GitOps).

> **Crossplane v2 note:** composition selection lives under
> `spec.crossplane.compositionRef` / `spec.crossplane.compositionSelector`
> (namespaced XRs), **not** the top-level `spec.compositionSelector` of v1.

## Implemented resources (local)

| Resource | XR kind | Local composition | Composes | Observability | Template |
|---|---|---|---|---|---|
| PostgreSQL | `CompositeDatabase` (engine: postgresql) | `compositedatabase-local-cnpg` | CloudNativePG `Cluster` | PodMonitor → CNPG dashboard | `postgres-database` |
| Redis/Valkey | `CompositeDatabase` (engine: redis) | `compositedatabase-local-redis` | Deployment + Service + Secret | redis_exporter + ServiceMonitor → Redis dashboard | `redis-cache` |
| Object bucket | `CompositeStorage` (type: object) | `compositestorage-local-minio` | MinIO bucket (Job) + Secret | platform MinIO dashboard | `object-bucket` |
| Kafka | `CompositeMessaging` | `compositemessaging-local-strimzi` | Strimzi Kafka (KRaft) + topics | JMX + kafka-exporter + PodMonitor → Kafka dashboards | `kafka-cluster` |
| Secret | `CompositeSecret` | `compositesecret-local` | Kubernetes Secret | — | `secret` |
| Environment | `CompositeEnvironment` | `compositeenvironment-local` | Namespace + ResourceQuota + LimitRange + NetworkPolicy | quota/limits visible in namespace dashboards | `environment` |
| Application | `CompositeApplication` | `compositeapplication-local-argocd` | ArgoCD `Application` (GitOps deploy) | ArgoCD dashboard | `application` |
| Project | `CompositeProject` | `compositeproject-local` | Namespace + quota/limits + ArgoCD AppProject + Gitea repo | auto (namespace in all dashboards) | `project` |

Each data composition emits a **connection Secret** (`<name>-app` /
`<name>-bucket`) the requesting app consumes.

## Ownership hierarchy

```
Organisation ─owns─► Team ─owns─► Project ─holds─► Application(s)
                                     │
                                     ├─ Environment  (namespace + quota + limits + netpol)
                                     ├─ Repository    (Gitea repo, GitOps source)
                                     ├─ AppProject    (ArgoCD deployment guardrails)
                                     └─ Monitoring     (auto — platform-common)
```

- **Organisation** and **Team** are catalog metadata (Backstage `Group`s) — the
  `organisation` / `team` templates register them.
- **Project** is the first level that provisions real infrastructure, so it is a
  Crossplane XR (`CompositeProject`). One project request fans out — via the
  control plane — into a guard-railed namespace, an ArgoCD `AppProject` scoped to
  it, and its own Gitea repository (the org is auto-created on first use).
  Create from either frontend:

  ```sh
  adhar project create --org acme --team payments --name acme-shop --tier dev
  # …or the "Project" template in the Console — same CompositeProject XR.
  ```

- **Application** (`CompositeApplication`) is created *inside* a Project and
  deploys into its namespace under its AppProject. Monitoring needs no wiring:
  the project namespace appears in every dashboard (incl. the **Adhar Platform**
  overview) automatically.

## Observability is automatic (platform-common)

Monitoring is a **platform concern, not a per-request option**. kube-prometheus
selects **all** ServiceMonitors/PodMonitors cluster-wide (empty selectors), and the
shipped Grafana dashboards are **template-variable driven** (`$namespace` /
`$instances` / `$cluster`). So a composition only has to *emit the metrics
endpoint* and its backing resource appears in the matching dashboard automatically,
with no per-instance dashboard wiring:

- **CNPG** — `spec.monitoring.enablePodMonitor: true` on every composed `Cluster`.
- **Redis** — a `redis_exporter` sidecar + a per-instance `ServiceMonitor`.
- **Kafka** — `metricsConfig` (JMX exporter) + `kafkaExporter` + a `PodMonitor`.

Apply the same recipe to any new composition that has a dashboard: emit the
exporter + a ServiceMonitor/PodMonitor and grant `monitoring.coreos.com` in
`rbac/local-compose-rbac.yaml`.

## Control plane prerequisites (must be live for local self-service)

The local compositions lean on provider-kubernetes/helm and Crossplane RBAC. These
are part of the embedded `configuration/` tree and are installed by the controller,
but note:

1. **provider-kubernetes / provider-helm `ClusterProviderConfig`s**
   (`providers/config/`) must exist — namespaced `Object`/`Release` MRs reference
   them. A namespaced v2 XR **cannot** compose a cluster-scoped resource (e.g. a
   Namespace) directly; it wraps the target in a provider-kubernetes `Object`.
2. **Stable provider SA + RBAC** (`providers/provider-runtime-rbac.yaml`): a
   `DeploymentRuntimeConfig` pins the provider SA names (`provider-kubernetes` /
   `provider-helm`) so a committed ClusterRoleBinding can grant them the native
   resource classes compositions emit (namespaces, quotas, netpol, ArgoCD apps, …).
3. **Crossplane compose RBAC** (`rbac/local-compose-rbac.yaml`): the `crossplane`
   SA aggregates rights to compose the native/operator types directly
   (CNPG, Strimzi, argoproj, ServiceMonitors, …).

## How to add a new local composition (the pattern)

1. **Pick or add an XRD** in `configuration/xrd/` (v2, `scope: Namespaced`). Reuse
   an existing one where it fits (e.g. Redis reuses `CompositeDatabase`).
2. **Write a composition** in `configuration/compositions/<feature>/local-*.yaml`
   with `function-go-templating` → native resources (or provider-kubernetes
   `Object`s for cluster-scoped targets) → `function-auto-ready`. Label it
   `provider: local` + a discriminator. Annotate every emitted object with
   `gotemplating.fn.crossplane.io/composition-resource-name`.
3. **Grant RBAC** for the emitted type in `rbac/local-compose-rbac.yaml` (composed
   by the `crossplane` SA) or, for provider-kubernetes `Object` targets,
   `providers/provider-runtime-rbac.yaml`.
4. **Wire observability** (if a dashboard exists): emit the exporter + a
   Service/PodMonitor.
5. **Add a template** in `adhar-templates/templates/<name>/` and register it in the
   root `catalog-info.yaml`. The CLI works with no template via `adhar <res> create`.

## CLI (no Console needed)

```sh
adhar db create  --name my-db --type postgresql --version 16 --size 1Gi -n my-team
adhar db create  --name cache --type redis     --version 8         -n my-team
adhar env create dev --tier dev
# On a cloud platform, export ADHAR_PROVIDER=aws first — same commands, RDS backing.
```

Or apply the XR directly:

```sh
kubectl apply -f - <<'EOF'
apiVersion: platform.adhar.io/v1alpha1
kind: CompositeDatabase
metadata: { name: my-db, namespace: my-team }
spec:
  crossplane: { compositionSelector: { matchLabels: { provider: local, feature: database, engine: postgresql } } }
  parameters: { engine: postgresql, engineVersion: "16", storageSize: 1Gi }
EOF
# connection: secret my-db-app (uri, jdbc-uri, username, password, host, port)
```

## Cloud portability

The same XR kinds have cloud compositions (`compositedatabase-aws-rds-postgresql`,
`compositedatabase-azure-sql`, `compositedatabase-gcp-cloudsql`, …). Selection is by
the provider-keyed `compositionSelector`, so a request written for local runs
unchanged on a cloud platform by switching `ADHAR_PROVIDER`.
