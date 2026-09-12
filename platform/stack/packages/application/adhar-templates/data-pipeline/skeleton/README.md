# ${{values.name}} — Golden Path: Data Pipeline

A production-shaped starting point for a lakehouse pipeline on the Adhar
platform, scaffolded to follow the paved road in **ADR-0020**:

```
  Airbyte  ──►  Iceberg  ──►  dbt / Trino  ──►  Dagster
 (ingest)     (table format)  (transform)    (orchestration)
```

- **Ingest** — a source (`${{values.sourceConnector}}`) lands raw records in an
  Iceberg table via the platform Iceberg REST catalog. In production this is an
  Airbyte connection; the scaffold ships a minimal Dagster asset that writes the
  same table so the chain runs end-to-end from day one.
- **Table format** — Apache Iceberg over platform object storage (MinIO
  locally, S3/GCS/Azure Blob in cloud). Every engine talks to one platform
  **Iceberg REST catalog**, so a table written here is immediately readable by
  Trino, Spark, and PyIceberg. The catalog endpoint is injected as config
  (`manifests/configmap.yaml`), not hard-coded.
- **Transform** — `dbt/` holds a minimal dbt project that reads the raw Iceberg
  table through Trino and builds a cleaned `staging` model.
- **Orchestration** — `dagster/pipeline.py` defines the ingest asset, the dbt
  build, and a daily schedule. The Dagster UI runs on-platform behind the
  Gateway.

Target Iceberg table: **`${{values.targetTable}}`**

## What gets deployed

`manifests/` (synced by ArgoCD):

- `namespace.yaml` — the pipeline's namespace.
- `configmap.yaml` — the Iceberg REST catalog endpoint + warehouse (points at
  the platform `iceberg-rest` service; override per environment).
- `deployment.yaml` + `service.yaml` — the Dagster webserver (UI + GraphQL).
- `httproute.yaml` — routes the UI through the platform Cilium Gateway.
- `cronjob.yaml` — a daily `dagster job execute` run that materializes the
  pipeline (the same schedule the Dagster daemon would trigger, run as a
  Kubernetes CronJob so the scaffold needs no long-running daemon).

Once synced, the Dagster UI answers at
`https://${{values.name}}.adhar.localtest.me:8443`.

## Run it locally

```bash
# 1. Python env
python -m venv .venv && source .venv/bin/activate
pip install -r dagster/requirements.txt

# 2. Point at the platform Iceberg REST catalog (port-forward or in-cluster DNS)
export ICEBERG_REST_URI=http://iceberg-rest.adhar-system.svc:8181
export ICEBERG_WAREHOUSE=s3://lakehouse/${{values.name}}

# 3. Materialize the pipeline (ingest asset -> Iceberg, then dbt build)
dagster asset materialize -m dagster.pipeline --select "*"

# 4. Or launch the local UI
dagster dev -m dagster.pipeline
```

dbt on its own (uses the Trino profile in `dbt/profiles`):

```bash
cd dbt && dbt build --profiles-dir profiles
```

## CI

`.adhar/app.yaml` + the platform Tekton `app-ci` pipeline (supply-chain package): PRs run the platform
`adhar-pr-verify` pipeline (lint + `dbt build` against an ephemeral schema),
merges to `main` run `adhar-release`, which opens a promotion PR against the
environments repo. Add `ci/test.sh` to customize the checks.

## ML variant

The ML paved road is its own golden path now — scaffold **Golden Path - ML
Service** (`adhar-templates/ml`): a Kubeflow Pipelines v2 training pipeline whose
model artifact lands in the platform object store, plus a hardened FastAPI model
server behind the same Gateway API route pattern used here. The Iceberg tables
this pipeline writes are the natural feature source for it: point the ML
pipeline's `load_data` component at `${{values.targetTable}}`.

Note that the platform ships **no MLflow package** today, so the ML golden path
uses the KFP artifact store (platform MinIO) as the model registry; the ML
skeleton's README documents the one-file change if you deploy MLflow.

### adhar

Checkout adhar website: https://adhar.io

Checkout adhar repository: https://github.com/adhar-io/adhar
