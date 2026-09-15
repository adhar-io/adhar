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

# No `from __future__ import annotations` here: Dagster 1.9 resolves the
# `context: AssetExecutionContext` annotation at decoration time and rejects
# the postponed (string) form with "Cannot annotate `context` parameter…" —
# the asset never loads, and the CronJob fails before touching the catalog
# (found running this skeleton on the platform, 2026-09-15).

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
    "ICEBERG_REST_URI", "http://rustfs.adhar-system.svc.cluster.local:9000/iceberg"
)
ICEBERG_WAREHOUSE = os.environ.get("ICEBERG_WAREHOUSE", "lakehouse")
ICEBERG_NAMESPACE = os.environ.get("ICEBERG_NAMESPACE", "${{values.name}}")
S3_ENDPOINT = os.environ.get("S3_ENDPOINT", "http://rustfs.adhar-system.svc.cluster.local:9000")
S3_REGION = os.environ.get("S3_REGION", "us-east-1")
# RustFS S3 Tables authenticates the REST catalog with the S3 keys over SigV4
# (signing name `s3`) and the data plane is the same endpoint.
S3_ACCESS_KEY_ID = os.environ.get("S3_ACCESS_KEY_ID", "")
S3_SECRET_ACCESS_KEY = os.environ.get("S3_SECRET_ACCESS_KEY", "")
# The catalog's SigV4 signer is boto3's default session, which only reads the
# AWS_* names; the ExternalSecret publishes both spellings, and this fallback
# keeps a local run with only the S3_* pair working.
os.environ.setdefault("AWS_ACCESS_KEY_ID", S3_ACCESS_KEY_ID)
os.environ.setdefault("AWS_SECRET_ACCESS_KEY", S3_SECRET_ACCESS_KEY)
os.environ.setdefault("AWS_DEFAULT_REGION", S3_REGION)
TARGET_TABLE = os.environ.get("TARGET_TABLE", "${{values.targetTable}}")
SOURCE_CONNECTOR = os.environ.get("SOURCE_CONNECTOR", "${{values.sourceConnector}}")


def _catalog():
    # RustFS S3 Tables: the REST catalog and the S3 data plane are the same
    # endpoint, and both are authenticated with the S3 key pair — the catalog
    # over SigV4 (`rest.sigv4-enabled`, signing name `s3`), the data files as
    # ordinary S3 objects.
    return load_catalog(
        CATALOG_NAME,
        **{
            "type": "rest",
            "uri": ICEBERG_REST_URI,
            "warehouse": ICEBERG_WAREHOUSE,
            "rest.sigv4-enabled": "true",
            "rest.signing-region": S3_REGION,
            "rest.signing-name": "s3",
            "s3.endpoint": S3_ENDPOINT,
            "s3.region": S3_REGION,
            "s3.access-key-id": S3_ACCESS_KEY_ID,
            "s3.secret-access-key": S3_SECRET_ACCESS_KEY,
            "s3.path-style-access": "true",
            # PyArrow's FileIO needs no extra S3 driver; the s3fs path pulls
            # an aiobotocore that pins boto3 and breaks dependency resolution.
            "py-io-impl": "pyiceberg.io.pyarrow.PyArrowFileIO",
            # RustFS's S3 kernel requires the SigV4 payload-hash header on
            # every signed call and PyIceberg's generic signer omits it —
            # without this every catalog call is a 400 "missing header:
            # x-amz-content-sha256" (verified on RustFS 1.0.0-rc.6).
            "header.x-amz-content-sha256": "UNSIGNED-PAYLOAD",
        },
    )


def _bootstrap_table(namespace: str, table_name: str, schema: pa.Schema) -> None:
    """Create the raw table through Trino if it does not exist yet.

    Verified on the platform's RustFS S3 Tables catalog (1.0.0-rc.6): a table's
    FIRST data commit from PyIceberg is rejected (``assert-ref-snapshot-id
    requires snapshot-id`` — the client omits the null snapshot-id the spec
    allows), while a table created by Trino takes PyIceberg appends fine and
    both engines read it. So the paved road creates the table with Trino's
    ``iceberg`` catalog — the same catalog dbt uses downstream — and appends
    with PyIceberg from then on. Idempotent (``IF NOT EXISTS``).
    """
    import trino

    type_map = {
        pa.int64(): "bigint",
        pa.int32(): "integer",
        pa.float64(): "double",
        pa.string(): "varchar",
        pa.bool_(): "boolean",
    }
    cols = []
    for field in schema:
        if pa.types.is_timestamp(field.type):
            trino_type = "timestamp(6) with time zone"
        else:
            trino_type = type_map.get(field.type, "varchar")
        cols.append(f'"{field.name}" {trino_type}')
    conn = trino.dbapi.connect(
        host=os.environ.get("TRINO_HOST", "trino.adhar-system.svc"),
        port=int(os.environ.get("TRINO_PORT", "8080")),
        user=os.environ.get("TRINO_USER", "${{values.name}}"),
        catalog="iceberg",
        schema=namespace,
    )
    cur = conn.cursor()
    cur.execute(f'CREATE SCHEMA IF NOT EXISTS iceberg."{namespace}"')
    cur.execute(f'CREATE TABLE IF NOT EXISTS iceberg."{namespace}"."{table_name}" ({", ".join(cols)})')
    cur.fetchall()


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
    _bootstrap_table(namespace, table_name, batch.schema)
    table = catalog.load_table((namespace, table_name))
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
