"""Golden-path data pipeline: ${{values.name}}.

A minimal but genuinely runnable Dagster job that materializes a source
(${{values.sourceConnector}}) into the platform Iceberg table
``${{values.targetTable}}`` via the platform Iceberg REST catalog, then triggers
the dbt staging transform. This is the paved road from ADR-0020:

    Airbyte (ingest) -> Iceberg -> dbt/Trino (transform) -> Dagster (orchestration)

In production the ingest asset is an Airbyte connection; the scaffold writes the
same table directly so the chain runs end-to-end from day one. Swap the body of
``raw_events`` for your real extract without touching anything downstream.
"""

from __future__ import annotations

import os
from datetime import datetime, timezone

import pyarrow as pa
from dagster import (
    AssetExecutionContext,
    Definitions,
    ScheduleDefinition,
    asset,
    define_asset_job,
)
from pyiceberg.catalog import load_catalog

# --- Platform Iceberg REST catalog -----------------------------------------
# Endpoint + warehouse come from manifests/configmap.yaml in-cluster; the
# defaults here let `dagster dev` run against a port-forwarded catalog locally.
CATALOG_NAME = "adhar"
ICEBERG_REST_URI = os.environ.get(
    "ICEBERG_REST_URI", "http://iceberg-rest.adhar-system.svc:8181"
)
ICEBERG_WAREHOUSE = os.environ.get(
    "ICEBERG_WAREHOUSE", "s3://lakehouse/${{values.name}}"
)
TARGET_TABLE = os.environ.get("TARGET_TABLE", "${{values.targetTable}}")
SOURCE_CONNECTOR = os.environ.get("SOURCE_CONNECTOR", "${{values.sourceConnector}}")


def _catalog():
    return load_catalog(
        CATALOG_NAME,
        **{
            "type": "rest",
            "uri": ICEBERG_REST_URI,
            "warehouse": ICEBERG_WAREHOUSE,
        },
    )


@asset(
    description="Ingest source records into the raw Iceberg table.",
    compute_kind=SOURCE_CONNECTOR,
)
def raw_events(context: AssetExecutionContext) -> None:
    """Extract from the source and append to ``${{values.targetTable}}``.

    Replace the sample batch with your real ${{values.sourceConnector}} extract
    (or wire an Airbyte connection that lands the same table).
    """
    namespace, _, table_name = TARGET_TABLE.rpartition(".")
    namespace = namespace or "raw"

    # Sample batch — deterministic shape so the dbt staging model has columns
    # to transform on the very first run.
    now = datetime.now(timezone.utc)
    batch = pa.table(
        {
            "id": pa.array([1, 2, 3], type=pa.int64()),
            "event": pa.array(["created", "updated", "deleted"]),
            "source": pa.array([SOURCE_CONNECTOR] * 3),
            "ingested_at": pa.array([now] * 3, type=pa.timestamp("us", tz="UTC")),
        }
    )

    catalog = _catalog()
    catalog.create_namespace_if_not_exists(namespace)
    table = catalog.create_table_if_not_exists(
        identifier=(namespace, table_name),
        schema=batch.schema,
    )
    table.append(batch)

    context.log.info(
        "Appended %d rows to %s.%s via %s",
        batch.num_rows,
        namespace,
        table_name,
        ICEBERG_REST_URI,
    )


@asset(
    deps=[raw_events],
    description="Run dbt to build the staging model over the raw table.",
    compute_kind="dbt",
)
def stg_transform(context: AssetExecutionContext) -> None:
    """Build the dbt staging model (``dbt/models/staging/stg_example.sql``).

    Runs the platform dbt project against Trino, which reads the raw Iceberg
    table written by ``raw_events``.
    """
    import subprocess

    dbt_dir = os.path.join(os.path.dirname(__file__), "..", "dbt")
    context.log.info("Running dbt build in %s", dbt_dir)
    subprocess.run(
        ["dbt", "build", "--profiles-dir", "profiles"],
        cwd=os.path.abspath(dbt_dir),
        check=True,
    )


# Job + daily schedule ------------------------------------------------------
pipeline_job = define_asset_job(name="${{values.name}}_pipeline", selection="*")

daily_schedule = ScheduleDefinition(
    name="${{values.name}}_daily",
    job=pipeline_job,
    # 06:00 UTC every day; the CronJob in manifests/ mirrors this cadence.
    cron_schedule="0 6 * * *",
)

defs = Definitions(
    assets=[raw_events, stg_transform],
    jobs=[pipeline_job],
    schedules=[daily_schedule],
)
