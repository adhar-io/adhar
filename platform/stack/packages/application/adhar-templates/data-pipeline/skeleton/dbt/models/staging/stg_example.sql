-- Staging model for ${{values.name}}: clean + type the raw Iceberg table into a
-- tidy view the rest of the project builds on. Reads the raw table written by
-- the Dagster ingest asset (${{values.targetTable}}) via Trino's Iceberg
-- catalog.
{{ config(materialized='view') }}

with source as (

    select * from {{ source('raw', 'example_events') }}

),

cleaned as (

    select
        cast(id as bigint)              as event_id,
        lower(trim(event))              as event_type,
        source                          as source_connector,
        ingested_at                     as ingested_at,
        date(ingested_at)               as ingested_date
    from source
    where id is not null

)

select * from cleaned
