# Low-Level Design — Data lakehouse on Apache Iceberg

Detailed design for [ADR-0020](../adr/0020-iceberg-data-lakehouse.md). This is the single, authoritative design document for the lakehouse: the table contract, the platform-operated Iceberg REST catalog (the integration keystone, mostly 🔜), how the shipped data packages (MinIO, Trino, Spark, lakeFS, OpenMetadata, CNPG) wire to it, the tenant self-service API, day-2 maintenance operations, milestones, risks, and tests down to file and manifest level. Status tracking lives in the [Roadmap](../ROADMAP.md) (Phase 3).

## 0. Context recap

ADR-0020 makes **Apache Iceberg the platform's table format** — the data analogue of the Gateway API routing contract and the OTel telemetry contract. Storage is the platform object store over the S3 API (MinIO local, native S3/GCS/Blob in cloud — the same seam as observability, ADR-0010). The interoperability point is **one platform-operated Iceberg REST catalog** backed by CNPG Postgres: every engine (Trino, Spark, PyIceberg, Flink) speaks the same REST protocol, so a table written by one engine is immediately, safely readable by the rest. Engines are pluggable consumers; ad-hoc per-team Hive metastores are rejected. Maintenance (compaction, snapshot expiry, orphan cleanup) is platform-owned day-2 (CronOperations, ADR-0005/0021), not per-team homework.

**What exists today vs. what this design adds** — the data machinery is shipped but *uncomposed*; there is no table contract yet:

| Component | State | Path |
|---|---|---|
| MinIO (S3 object store) | ✅ shipped, OIDC-fronted, `root-creds` secret, `adhar-backups` bucket | `platform/stack/packages/data/minio/` |
| Trino (SQL engine) | ✅ shipped, chart `trino-1.42.2` / server `480`, but catalog is only `tpch`/`tpcds` demo connectors | `platform/stack/packages/data/trino/` |
| Spark Operator (batch/ML) | ✅ shipped (operator only, no lakehouse jobs) | `platform/stack/packages/data/spark-operator/` |
| lakeFS (data branching) | ✅ shipped, but on the **local block adapter + in-memory KV**, not wired to MinIO/CNPG | `platform/stack/packages/data/lakefs/` |
| CNPG (Postgres operator) | ✅ shipped (operator + CRDs), no catalog DB cluster yet | `platform/stack/packages/data/cnpg/` |
| OpenMetadata (discovery/lineage) | ✅ shipped | `platform/stack/packages/data/open-metadata/` |
| Kafka / Airbyte / Dagster / dbt | ✅ shipped (ingest/orchestration) | `platform/stack/packages/data/{kafka,airbyte,dagster,dbt}/` |
| **Iceberg REST catalog** | 🔜 **does not exist** — the keystone this design builds | `platform/stack/packages/data/iceberg/` (new) |
| Trino/Spark Iceberg catalog wiring | 🔜 connectors point at the REST catalog | this design |
| CompositeLakehouse XR (tenant self-service) | 🔜 new XRD/composition | `platform/controlplane/configuration/` |
| Compaction / expiry / orphan CronOperations | 🔜 new | `platform/controlplane/configuration/operations/` |

Every data package is currently `enabled: "false"` in `platform/stack/adhar-appset-local.yaml` (verified: `trino`, `minio`, `lakefs`, `spark-operator`, `open-metadata`, `cnpg`, `kafka`, `airbyte`, `dagster` all gated off). This design keeps that gating and enables a curated lakehouse core (MinIO + catalog + Trino + CNPG) in small waves per ADR-0004/0012/0014.

## 1. Component topology

```
                          OIDC (Keycloak, ADR-0008)  ──┐
                                                        v
  engines ──REST──►  Iceberg REST Catalog  ──JDBC──►  CNPG cluster  (metadata brain, tier-1 HA/backup)
   Trino  Spark        (iceberg/ package)              iceberg-catalog-db
   PyIceberg  Flink          │
        │                    │ S3A (path-style, per-tenant creds via ESO)
        │  S3 read/write     v
        └──────────────►  MinIO / S3 / GCS / Blob   (buckets provisioned by CompositeStorage XR)
                              ▲
                   lakeFS ────┘  (Git-like branches over the object store; dev/test data isolation)

  OpenMetadata ──indexes──►  REST catalog (namespaces, tables, snapshots, lineage)
  CronOperations (ADR-0005) ──►  rewrite_data_files / expire_snapshots / remove_orphan_files per table
```

The **REST catalog is the only stateful new service**; everything else already runs. Its Postgres joins the tier-1 list (ADR-0021) — losing it orphans every table's pointer to its current metadata.

## 2. The Iceberg REST catalog package (🔜 `platform/stack/packages/data/iceberg/`)

New standard package, structured like every other data package (`generate-manifests.sh`, `values.yaml`, `manifests/{install,httproute}.yaml`). Runs the [`apache/iceberg-rest-fixture`](https://iceberg.apache.org) / production `iceberg-rest` server (JDBC catalog backend), namespaced per team, OIDC-fronted.

### 2.1 CNPG metadata cluster (`manifests/catalog-db.yaml`)

Reuses the shipped CNPG operator to stand up the catalog's Postgres — the analogue of how Keycloak/OpenMetadata get their DBs:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: iceberg-catalog-db
  namespace: adhar-system
spec:
  instances: 1                     # local; HA (3) in cloud environments (ADR-0012/0021)
  storage: { size: 2Gi, storageClass: standard }
  bootstrap:
    initdb: { database: iceberg_catalog, owner: iceberg, secret: { name: iceberg-catalog-db-app } }
  backup:                          # tier-1: WAL archiving to the platform object store
    barmanObjectStore:
      destinationPath: s3://adhar-lakehouse-meta/catalog-wal
      endpointURL: http://minio.adhar-system.svc:9000
      s3Credentials: { accessKeyId: {...}, secretAccessKey: {...} }  # via ESO from root-creds
    retentionPolicy: "30d"
```

### 2.2 Catalog Deployment (`manifests/install.yaml`)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata: { name: iceberg-rest, namespace: adhar-system }
spec:
  replicas: 1                      # stateless; scale horizontally in cloud
  template:
    spec:
      securityContext: { runAsNonRoot: true, runAsUser: 65532 }
      containers:
        - name: iceberg-rest
          image: apache/iceberg-rest-fixture:<pinned>   # or tabulario/iceberg-rest for prod
          env:
            - { name: CATALOG_CATALOG__IMPL, value: org.apache.iceberg.jdbc.JdbcCatalog }
            - { name: CATALOG_URI, value: "jdbc:postgresql://iceberg-catalog-db-rw:5432/iceberg_catalog" }
            - { name: CATALOG_JDBC_USER,     valueFrom: { secretKeyRef: { name: iceberg-catalog-db-app, key: username } } }
            - { name: CATALOG_JDBC_PASSWORD, valueFrom: { secretKeyRef: { name: iceberg-catalog-db-app, key: password } } }
            # Default warehouse root on the platform object store
            - { name: CATALOG_WAREHOUSE, value: "s3://adhar-lakehouse/warehouse" }
            - { name: CATALOG_IO__IMPL,  value: org.apache.iceberg.aws.s3.S3FileIO }
            - { name: CATALOG_S3_ENDPOINT, value: "http://minio.adhar-system.svc:9000" }
            - { name: CATALOG_S3_PATH__STYLE__ACCESS, value: "true" }        # MinIO requires path-style
            - { name: AWS_ACCESS_KEY_ID,     valueFrom: { secretKeyRef: { name: root-creds, key: rootUser } } }
            - { name: AWS_SECRET_ACCESS_KEY, valueFrom: { secretKeyRef: { name: root-creds, key: rootPassword } } }
          ports: [{ containerPort: 8181 }]
```

`root-creds` is the **existing** MinIO secret (verified in `minio/values.yaml: existingSecret: root-creds`). In cloud, `AWS_*` come from the CompositeStorage connection secret instead (§4).

### 2.3 Gateway + SSO (`manifests/httproute.yaml`, `manifests/oidc.yaml`)

An `HTTPRoute` on `adhar-gateway` exposes `catalog.<domain>` (matching the Trino/lakeFS/MinIO HTTPRoute pattern — no nginx). A JWT/OIDC validation sidecar or the catalog's `rest.auth.type=oauth2` binds tokens to the Keycloak realm (`https://keycloak.<domain>/realms/adhar`), the same issuer MinIO uses. Namespaces in the catalog map to Keycloak groups → Trino/Spark can only see their team's tables. An `ExternalSecret` (ESO, ADR-0009) delivers the `iceberg` OIDC client secret, mirroring `minio/manifests/oidc.yaml`.

## 3. Engine wiring — the pluggability payoff

### 3.1 Trino Iceberg catalog (🔜 replaces demo connectors)

Today `trino-catalog` ConfigMap ships only `tpcds.properties` / `tpch.properties` (verified). The design adds an `iceberg.properties` catalog pointing at the REST service — via Trino chart `additionalCatalogs` in `platform/stack/packages/data/trino/values.yaml`:

```properties
# rendered into the trino-catalog ConfigMap
connector.name=iceberg
iceberg.catalog.type=rest
iceberg.rest-catalog.uri=http://iceberg-rest.adhar-system.svc:8181
iceberg.rest-catalog.warehouse=s3://adhar-lakehouse/warehouse
iceberg.rest-catalog.security=OAUTH2
fs.native-s3.enabled=true
s3.endpoint=http://minio.adhar-system.svc:9000
s3.path-style-access=true
s3.region=us-east-1
```

`SELECT * FROM iceberg.<namespace>.<table>` now resolves through the shared catalog — dbt (`data/dbt`) transforms run over this Trino catalog.

### 3.2 Spark (🔜 batch/ML pipelines)

`SparkApplication` CRs submitted through the shipped Spark Operator carry the Iceberg Spark runtime + REST catalog config, so a Spark job writes tables Trino reads with zero migration:

```
spark.sql.catalog.adhar                 = org.apache.iceberg.spark.SparkCatalog
spark.sql.catalog.adhar.type            = rest
spark.sql.catalog.adhar.uri             = http://iceberg-rest.adhar-system.svc:8181
spark.sql.catalog.adhar.warehouse       = s3://adhar-lakehouse/warehouse
spark.sql.catalog.adhar.io-impl         = org.apache.iceberg.aws.s3.S3FileIO
spark.sql.extensions                    = org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions
```

### 3.3 Streaming & pipelines

Kafka → Iceberg sink connectors land streaming data into catalog tables; Airbyte (ingest) and Dagster (orchestration) compose on top — the **data golden path** (Roadmap Phase 3) scaffolds this ingest→transform→serve chain the way app golden paths scaffold CI (ADR-0018). lakeFS (§5) provides the branch/validate/merge gate for pipeline dev.

## 4. Tenant self-service — `CompositeLakehouse` XR (🔜)

"A lakehouse space" becomes tenant self-service via Crossplane (ADR-0005), building on the shipped `CompositeStorage` XRD (verified: `platform/controlplane/configuration/xrd/storage.xrd.yaml`, `scope: Namespaced`, `type: [block,file,object,database]`). A new thin XRD composes a bucket + catalog namespace + scoped credentials:

```yaml
# platform/controlplane/configuration/xrd/lakehouse.xrd.yaml (new; apiextensions v2, Namespaced)
apiVersion: platform.adhar.io/v1alpha1
kind: CompositeLakehouse
metadata: { name: growth-analytics, namespace: team-growth }
spec:
  namespace: growth            # Iceberg catalog namespace (== Keycloak group)
  storage:
    size: 100Gi
    retentionDays: 30
  maintenance:
    compaction: { schedule: "0 3 * * *", targetFileSizeMB: 512 }
    expireSnapshots: { schedule: "0 4 * * 0", olderThanDays: 7 }
```

The composition (`configuration/compositions/lakehouse/*.yaml`, Pipeline mode) fans out to: a `CompositeStorage{type: object}` for the bucket, an `ExternalSecret` delivering scoped S3 creds to the team namespace, a call to the REST catalog to `createNamespace`, and per-table `CronOperation`s (§6) seeded from `spec.maintenance`. Local/MinIO uses one shared bucket with prefixes; cloud provisions a real S3/GCS bucket per lakehouse.

## 5. Data versioning — lakeFS wired to the lakehouse (🔜)

lakeFS ships on the **local block adapter + in-memory KV** today (verified in `lakefs/values.yaml`: *"Local quickstart uses the embedded 'local' block adapter + in-memory KV. For production point this at MinIO (S3) + cnpg Postgres."*). This design executes that TODO:

```yaml
# lakefs/values.yaml (production wiring)
lakefsConfig: |
  blockstore:
    type: s3
    s3: { endpoint: http://minio.adhar-system.svc:9000, force_path_style: true }
  database:
    type: postgres
    postgres: { connection_string: "postgres://lakefs@iceberg-catalog-db-rw:5432/lakefs" }
```

lakeFS branches give the **data analogue of preview environments** (ADR-0017): branch → run pipeline on the branch's isolated view → validate → merge. Iceberg's snapshot isolation composes with lakeFS commits so a merge is atomic across many tables.

## 6. Day-2 maintenance — CronOperations (🔜)

Iceberg tables rot without compaction/expiry/orphan cleanup (small-file explosion, unbounded storage). These are **platform-owned defaults**, modeled exactly on the shipped `adhar-daily-backup` CronOperation (verified: `configuration/operations/backup-cronoperation.yaml`, `mode: Pipeline`, `function-python`, `--enable-operations`):

```yaml
# platform/controlplane/configuration/operations/iceberg-compaction-cronoperation.yaml (new)
apiVersion: ops.crossplane.io/v1alpha1
kind: CronOperation
metadata: { name: iceberg-compaction, labels: { feature: lakehouse } }
spec:
  schedule: "0 3 * * *"
  concurrencyPolicy: Forbid
  successfulHistoryLimit: 5
  operationTemplate:
    spec:
      mode: Pipeline
      pipeline:
        - step: rewrite-data-files          # SparkApplication: CALL system.rewrite_data_files
          functionRef: { name: function-python }
          input: { apiVersion: python.fn.crossplane.io/v1beta1, kind: Script, script: |
              # enumerate catalog tables, emit one SparkApplication per table into
              # rsp.desired.resources (force-applied by the operations engine) }
```

Three operations: `rewrite_data_files` (compaction, daily), `expire_snapshots` (weekly), `remove_orphan_files` (weekly). Each enumerates the catalog and emits SparkApplications. **Disabling any of these to save local resources must be a visible, documented choice** (ADR-0020 consequence) — the local profile ships them `suspend: true` with a `ROADMAP`/CONFLICTS note rather than silently omitting them.

## 7. Integration points (inherited platform properties)

| Concern | Wiring | Source of truth |
|---|---|---|
| SSO | Catalog + every engine UI validate Keycloak tokens; catalog namespace == Keycloak group | ADR-0008; `minio/manifests/oidc.yaml` pattern |
| Secrets | S3 creds + JDBC passwords via ESO `ExternalSecret` from Vault; no plaintext in Git | ADR-0009; `platform/stack/packages/security/{external-secrets,vault}/` |
| Buckets | `CompositeStorage{type: object}` / `CompositeLakehouse` XR | ADR-0005; `configuration/xrd/storage.xrd.yaml` |
| Gateway | `HTTPRoute` on `adhar-gateway` for `catalog.<domain>` (no nginx) | ADR-0002; `trino/manifests/httproute.yaml` pattern |
| GitOps | All manifests seeded to Gitea `packages` repo, synced by the local ApplicationSet | ADR-0003/0004; `globals/project.go` (`GiteaPlatformOrg=adhar`) |
| Cost | OpenCost attributes catalog/engine compute + object storage per namespace | ADR-0010 |
| Backup | CNPG WAL archiving of the catalog DB to object storage; tier-1 | ADR-0021 |

## 8. ApplicationSet gating (ADR-0004/0012/0014)

Add an `iceberg` element to `platform/stack/adhar-appset-local.yaml` (`category: data`, `manifestPath: data/iceberg/manifests`). The full stack is too heavy for the local curated core, so the **local lakehouse profile enables only MinIO + REST catalog + CNPG + Trino** (`enabled: "true"` for those four); Spark/Kafka/Airbyte/Dagster/lakeFS/OpenMetadata stay `enabled: "false"` and are turned on in small waves via `adhar apps enable <pkg>` (ADR-0014). This preserves the current all-`false` data posture while giving a working table contract on a laptop.

## 9. Ordering & idempotency

1. CNPG operator ready → `iceberg-catalog-db` Cluster healthy (Postgres up)
2. MinIO ready + `adhar-lakehouse` bucket exists (bucket job / CompositeStorage)
3. `iceberg-rest` Deployment ready (JDBC migration on first start is idempotent)
4. Trino/Spark catalogs resolve (they tolerate catalog restarts — connection is per-query)
5. CronOperations registered (no-op until tables exist)

ArgoCD sync waves order (1)–(3); engines (4) are independent Applications that self-heal once the catalog Service resolves. Every REST `createNamespace`/`createTable` is idempotent (409 → adopt). The catalog is stateless — its truth is Postgres; pod loss is recoverable, DB loss is not (hence tier-1 backup).

## 10. Failure modes

- **Catalog DB down** → all engines fail table resolution; CNPG HA (cloud) + WAL restore (local) recover it; `adhar get status` surfaces the CNPG cluster health.
- **Object store unreachable** → reads/writes fail but metadata is intact; no data loss (Iceberg metadata points at immutable files).
- **Maintenance skipped** → gradual small-file/storage rot; surfaced as an OpenMetadata/Grafana table-health metric, not a hard failure.
- **Two engines writing the same table** → Iceberg optimistic-concurrency commit via the REST catalog serializes them (one retries); this is the core correctness win over raw Parquet.

## 11. Testing

- **Package parity** (`platform/controllers/adharplatform/parity_test.go`): the new `iceberg` package appears in the appset with a resolvable `manifestPath`; local profile enables exactly {minio, iceberg, cnpg, trino}.
- **Manifest lint**: `kubeconform` on `data/iceberg/manifests/*` and the new XRD/composition/operations (repo CI already validates crossplane `configuration/`).
- **e2e (`tests/e2e`, gated)**: on a live platform — create an Iceberg table via Trino, read it via a Spark job, verify byte-identical rows (multi-engine ACID contract); create a lakeFS branch, write, merge; assert the compaction CronOperation reduces file count on a small-file table.
- **XRD round-trip**: `CompositeLakehouse` apply under envtest against the composition renders a `CompositeStorage`, an `ExternalSecret`, and N `CronOperation`s.
- **Security**: a token scoped to team A's Keycloak group cannot list team B's catalog namespace (OIDC namespace isolation).

## 12. Code & file map

| Path | State | Responsibility |
|---|---|---|
| `platform/stack/packages/data/iceberg/{generate-manifests.sh,values.yaml}` | 🔜 new | REST catalog package |
| `platform/stack/packages/data/iceberg/manifests/{install,catalog-db,httproute,oidc}.yaml` | 🔜 new | Deployment, CNPG cluster, Gateway route, OIDC ExternalSecret |
| `platform/stack/packages/data/trino/values.yaml` | 🔧 edit | add `iceberg` catalog (currently only tpch/tpcds) |
| `platform/stack/packages/data/lakefs/values.yaml` | 🔧 edit | swap local adapter → MinIO S3 + CNPG Postgres |
| `platform/stack/packages/data/spark-operator/` | 🔧 use | SparkApplication templates with Iceberg REST catalog |
| `platform/controlplane/configuration/xrd/lakehouse.xrd.yaml` | 🔜 new | `CompositeLakehouse` self-service API |
| `platform/controlplane/configuration/compositions/lakehouse/*.yaml` | 🔜 new | bucket + namespace + creds + maintenance fan-out |
| `platform/controlplane/configuration/operations/iceberg-{compaction,expire,orphan}-cronoperation.yaml` | 🔜 new | day-2 table maintenance |
| `platform/stack/adhar-appset-local.yaml` | 🔧 edit | add `iceberg` element; local lakehouse profile |
| `platform/stack/packages/data/{minio,cnpg,open-metadata,kafka,airbyte,dagster,dbt}/` | ✅ exists | storage, metadata DB operator, discovery, ingest/orchestration — unchanged |

## 13. Milestones

- **M1 — Catalog keystone**: `iceberg` package (REST server + `iceberg-catalog-db` CNPG cluster + HTTPRoute + OIDC); Trino `iceberg` catalog; create/read a table from Trino. Local profile enables {minio, iceberg, cnpg, trino}.
- **M2 — Multi-engine**: Spark Operator Iceberg jobs; prove write-Spark / read-Trino ACID interop; OpenMetadata indexes the catalog.
- **M3 — Self-service**: `CompositeLakehouse` XRD + composition; per-team bucket + namespace + scoped creds; `adhar` CLI/console scaffolds a lakehouse space.
- **M4 — Data branching**: lakeFS on MinIO+CNPG; branch→validate→merge golden path (ADR-0017 analogue); Kafka→Iceberg streaming sink; Airbyte/Dagster pipeline scaffold.
- **M5 — Day-2 hardening**: compaction/expiry/orphan CronOperations on by default; catalog DB tier-1 HA + WAL backup/restore drill; table-health dashboard; Enforce SSO namespace isolation.

## 14. Risks

- **Catalog Postgres is a single point of truth for all tables** — mitigate with CNPG HA (cloud), WAL archiving to object storage (local), and inclusion in the tier-1 backup/DR set (ADR-0021).
- **Local resource weight** — the full stack cannot run on a laptop; the curated 4-package local profile + enabled-gating (ADR-0004/0012) is the escape valve; disabling maintenance must be explicit.
- **MinIO path-style / S3 semantics drift** vs. cloud S3/GCS — pin `path-style-access` and `S3FileIO` config per environment; e2e covers both seams (same architecture, ADR-0020 pillar-4 claim).
- **REST catalog project maturity / image choice** — `iceberg-rest-fixture` is reference-grade; production may need `tabulario/iceberg-rest` or a vendor build. Pin by digest, Chainguard-base and sign (ADR-0019).
- **Maintenance not actually running** silently rots tables — surface compaction/expiry as a visible table-health SLO, not a background assumption.
