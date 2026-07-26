# ADR-0020: Data lakehouse on Apache Iceberg over platform object storage

**Status**: Accepted (MinIO/Trino/Spark/Kafka/lakeFS/OpenMetadata packages shipped; Iceberg REST catalog service is the integration keystone, phased with the data golden path) · **Date**: 2026-07

## Context

The catalog ships substantial data machinery — MinIO, Trino, Spark Operator, Kafka, Airbyte, Dagster, dbt, lakeFS, OpenMetadata — but tools alone reproduce the classic failure: every team invents its own file layout on object storage, engines can't safely share tables, and "the data platform" is a pile of Parquet files with tribal-knowledge semantics. The platform needs a **table contract** the way it has a routing contract (Gateway API) and a telemetry contract (OTel). Options:

- **Raw Parquet/Hive-style layout** — no ACID, no safe concurrent writers, schema evolution by convention, O(listing) planning on object stores; this is the status quo the lakehouse movement exists to fix
- **A warehouse database (ClickHouse/OpenSearch/Postgres)** — excellent engines, but data becomes captive to one engine's storage format; conflicts with pillar 8 (open formats, replaceable engines)
- **Delta Lake** — capable format, but its ecosystem gravity is Databricks-centric; the open-governance option is weaker
- **Apache Iceberg** — open table format with ACID transactions, snapshot isolation, time travel, hidden partitioning, and schema/partition evolution; first-class support across Trino, Spark, Flink, Kafka Connect, and every major vendor — the industry's neutral table standard

## Decision

**Apache Iceberg is the platform's table format**; the lakehouse is assembled from catalog packages around it:

- **Storage**: platform object storage via the S3 API — MinIO locally and on-prem, native S3/GCS/Azure Blob in cloud (same seam as observability storage, ADR-0010). Buckets are provisioned through Crossplane XRs (ADR-0005), so "a lakehouse space" is tenant self-service.
- **Catalog**: a single platform-operated **Iceberg REST catalog** backed by CNPG PostgreSQL. The REST protocol is the interoperability point — every engine (Trino, Spark, Flink, PyIceberg) speaks to the same catalog, so tables written by one engine are immediately and safely readable by the rest. One catalog, namespaced per team, OIDC-fronted (ADR-0008); ad-hoc per-team Hive metastores are explicitly rejected.
- **Engines are pluggable consumers**: **Trino** (`data/trino`) is the interactive SQL plane; **Spark** (`data/spark-operator`) handles batch/ML pipelines; streaming ingest lands via Kafka → Iceberg connectors. Engines can be swapped or added without touching data — that's the point of the format.
- **Pipelines**: Airbyte (ingest), dbt (transform, via Trino), and Dagster (orchestration) compose on top; the data golden path (Roadmap Phase 3) scaffolds this chain the way app golden paths scaffold CI (ADR-0018).
- **Versioning & governance**: **lakeFS** provides Git-like branches over the object store for dev/test isolation of *data* (branch, run pipeline, validate, merge — the data analogue of preview environments, ADR-0017); **OpenMetadata** indexes the catalog for discovery, lineage, and ownership.
- **Maintenance is platform-owned day-2**: Iceberg tables require compaction, snapshot expiry, and orphan-file cleanup or they rot (small-file explosions, unbounded storage). These run as scheduled platform operations (CronOperations, ADR-0005) with defaults per table, not as per-team homework — the ADR-0021 principle applied to data.

## Consequences

- ✅ One table contract: ACID multi-engine access, schema evolution, and time travel replace file-layout folklore; teams choose engines per workload without data migration
- ✅ Data stays in open formats on commodity object storage — no engine or vendor holds it hostage (pillar 8); local MinIO and cloud S3 are the same architecture (pillar 4)
- ✅ Data work inherits platform properties for free: SSO on every UI, secrets via ESO, buckets via self-service XRs, cost attribution via OpenCost on storage/compute
- ⚠️ The REST catalog becomes critical data infrastructure — its Postgres is the lakehouse's metadata brain and joins the HA/backup tier-1 list (ADR-0021); losing it orphans every table
- ⚠️ The full stack is far too heavy for the local curated core; the local profile enables MinIO + Trino + catalog only, with Spark/Kafka/etc. gated per ADR-0004 and enabled in small waves (ADR-0012/0014)
- ⚠️ Iceberg's benefits are contingent on maintenance actually running — the platform owns those defaults, and skipping them (e.g. disabling the compaction CronOperation to save local resources) must be a visible, documented choice
