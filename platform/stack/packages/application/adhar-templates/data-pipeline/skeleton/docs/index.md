# Golden Path: Data Pipeline

A production-shaped starting point for a lakehouse pipeline scaffolded by the
Adhar platform, following ADR-0020 (Iceberg data lakehouse):

```
Airbyte (ingest) -> Iceberg (table format) -> dbt/Trino (transform) -> Dagster (orchestration)
```

- **Orchestration** — `dagster/pipeline.py`: a minimal Dagster asset that
  materializes the source (`${{values.sourceConnector}}`) into the Iceberg
  table `${{values.targetTable}}`, a dbt-build asset, and a daily schedule.
- **Transform** — `dbt/`: a minimal dbt project (`dbt_project.yml` +
  `models/staging/stg_example.sql`) reading the raw Iceberg table through Trino.
- **Catalog** — every engine talks to one platform **Iceberg REST catalog**;
  its endpoint is injected via `manifests/configmap.yaml`, never hard-coded.
- **Runtime** — `manifests/`: the Dagster webserver Deployment + Service, an
  HTTPRoute through the platform Gateway, and a CronJob that materializes the
  pipeline daily.
- **CI** — `jenkins-x.yml` + `.lighthouse/triggers.yaml` (ADR-0018): PRs run the
  platform `adhar-pr-verify` pipeline, merges to `main` run `adhar-release`.

Once synced by ArgoCD, the Dagster UI answers at
`https://${{values.name}}.adhar.localtest.me:8443`. See `README.md` for how to
run it locally.

### adhar

Checkout adhar website: https://adhar.io

Checkout adhar repository: https://github.com/adhar-io/adhar
