# LibreDB Studio

A browser-based SQL IDE for the platform's own databases — one tab for
PostgreSQL, Redis/Valkey, OpenSearch, ClickHouse and Trino, with a
Monaco editor, schema-aware completion, ER diagrams and visual EXPLAIN.
MIT, self-hosted, nothing behind an enterprise wall.

- Upstream: <https://libredb.org> · <https://github.com/libredb/libredb-studio>
- Chart: `oci://ghcr.io/libredb/charts/libredb-studio` **0.1.63** (OCI only —
  there is no https Helm repo to `helm repo add`)
- Image: `ghcr.io/libredb/libredb-studio:0.15.0`
- URL: `https://libredb.<host>` (local: <https://libredb.adhar.localtest.me:8443>)

Regenerate `manifests/install.yaml` with `./generate-manifests.sh`. The other
manifests are static and are not regenerated.

## Sign-in — native Keycloak OIDC, no oauth2-proxy

The MIT build speaks OIDC itself (Authorization Code + PKCE, then its own JWT
session), so this package does **not** use the platform's oauth2-proxy front
(`hack/gen-sso.sh`). The login page offers SSO only:

| Piece | Where |
|---|---|
| Client `libredb-studio` | `manifests/keycloak-client.yaml` — a ConfigMap labelled `adhar.io/keycloak-client: "true"`, reconciled by the keycloak package (create-or-PUT every 5 minutes) |
| Client secret | exported by that reconciler into `keycloak-clients` as `LIBREDB_STUDIO_CLIENT_ID` / `LIBREDB_STUDIO_CLIENT_SECRET`, read back through the `keycloak` ClusterSecretStore into `libredb-studio-secrets` |
| Issuer | `https://keycloak.<host>/realms/adhar` |
| Roles | `OIDC_ROLE_CLAIM=groups` (the adhar realm's group-membership mapper is flat, `full.path=false`), `OIDC_ADMIN_ROLES=admin,platform-admin,platform-engineer` |

Studio knows two roles. A member of one of those three Keycloak groups signs in
as **admin** (audit log, fleet health, the admin-only connections); every other
realm user signs in as **user**.

`config.authBootstrap: "off"` turns off the app's zero-config first run. Without
it the container generates a local admin password on first start and prints it
to the pod log — a second, password-based way in that nobody asked for. Strict
mode needs a stable `JWT_SECRET`, which `libredb-studio-secrets` carries
(generated once by an External Secrets `Password` generator, so sessions
survive a restart); `ADMIN_PASSWORD` is not required under `authProvider=oidc`.

## Its own state

`STORAGE_PROVIDER=postgres` against the CNPG cluster **`libredb-db`**
(`manifests/platform-postgres.yaml`), not the chart's default browser
`localStorage` and not the chart's bundled Bitnami PostgreSQL subchart
(ADR-0011: never bundle a capability the platform already provides). Server-side
storage is what makes connections, tabs and history belong to the Keycloak
identity rather than to one browser profile — and it is the mode in which a
*managed* connection's credentials are resolved server-side and never sent to
the browser. The pod itself is stateless: no PVC, `/app/data` is an emptyDir.

The connection URL is assembled by External Secrets from the CNPG credentials,
so the database password is never written down twice.

## Pre-provisioned connections

`values.yaml` ships a `seedConnections` document (mounted at
`/app/config/seed-connections.yaml`) listing every database the platform runs.
All are `managed: true` — read-only in the UI, credentials resolved server-side,
a rotated password followed automatically.

Passwords are **never** in the seed document: it uses `${VAR}` references, and
`extraEnvFrom` fills those from per-datasource Secrets that External Secrets
mirrors from the Secret each operator already created
(`manifests/datasources.yaml`, one ExternalSecret per datasource so a missing
package costs only its own connection). A connection whose `${VAR}` is
undefined is skipped by Studio and the rest keep working — which is exactly what
happens on a local cluster that runs the curated core.

| Connection | Type | Endpoint | Credentials from | Visible to |
|---|---|---|---|---|
| Gitea | postgres | `gitea-db-rw:5432/gitea` | `gitea-db-credentials` (bootstrap phase) | admin |
| Keycloak | postgres | `keycloak-db-rw:5432/keycloak` | `keycloak-config` | admin |
| Adhar Console | postgres | `console-db-rw:5432/console` | `console-env-vars` | admin |
| LibreDB Studio | postgres | `libredb-db-rw:5432/libredb` | `libredb-db-credentials` | admin |
| PostHog | postgres | `posthog-db-rw:5432/posthog` | `posthog-db-credentials` | all |
| Metabase | postgres | `metabase-db-rw:5432/metabase` | `metabase-db-credentials` | all |
| OpenMetadata | postgres | `openmetadata-db-rw:5432/openmetadata_db` | `openmetadata-db-credentials` | all |
| Adhar AI RAG | postgres | `adhar-ai-rag-rw:5432/adhar_ai_rag` | `adhar-ai-rag-app` (CNPG-generated) | all |
| Adhar Cache (Valkey) | redis | `adhar-cache:6379` | none — `anonymousAuth` | all |
| Redis | redis | `redis:6379` | none | all |
| OpenSearch | opensearch | `opensearch:9200` (TLS, operator-generated CA) | `opensearch-admin-credentials` (generated) | all |
| ClickHouse | clickhouse | `clickhouse-posthog:8123/posthog` | `libredb-datasource-extras` (see below) | all |
| Trino | trino | `trino:8080`, catalog `tpch`, schema `tiny` | none — auth disabled | all |

CNPG note: a cluster created with `bootstrap.initdb.secret` uses **that** Secret
as its application account, so there is no `<cluster>-app` Secret to read for
most of these; `adhar-ai-rag` is the one cluster with no bootstrap secret, and
that is why it is the one reading `adhar-ai-rag-app`.

The three admin-only connections are the platform's own systems of record
(repositories, identities, console state); the rest are application data.

### MySQL and MongoDB are not listed

`data/mysql-operator` and `data/mongodb` install **operators and CRDs only** —
there is no `InnoDBCluster` or `MongoDBCommunity` instance anywhere in the
stack, so there is no server to connect to. Add a connection to
`seedConnections.config.connections` when one is provisioned. Gitea's internal
Valkey is deliberately omitted too: it is a private cache whose shape differs
between the HA and non-HA foundation renders.

### ClickHouse

ClickHouse is now the only platform datasource whose password exists solely as a
literal env value in the package that runs it — PostHog's `CLICKHOUSE_PASSWORD`.
There is no Secret for External Secrets to mirror, and copying a password into a
platform manifest is not something this package will do, so the connection is
wired to an optional Secret an operator creates once:

```bash
kubectl -n adhar-system create secret generic libredb-datasource-extras \
  --from-literal=CLICKHOUSE_USER=admin \
  --from-literal=CLICKHOUSE_PASSWORD="$(kubectl -n adhar-system get deploy posthog-web \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CLICKHOUSE_PASSWORD")].value}')"
```

Until it exists, that connection is simply not listed. Nothing else is affected.

**OpenSearch used to be here and no longer is.** The `data/opensearch` package
became operator-managed on 2026-10-03 and now generates its admin password into
`opensearch-admin-credentials` (keys `username`/`password`) instead of baking a
literal into a StatefulSet env var. `values.yaml` reads `password` from it
through `extraEnv` with `optional: true`, so the connection is wired with no
manual step and a profile that runs this package without `opensearch` still
starts — it just drops that one connection, exactly like ClickHouse above. That
is the fix this section has been asking for; PostHog is the one package left to
do it.

## Metrics

No `servicemonitor.yaml`: LibreDB Studio exposes **no Prometheus endpoint**.
`/api/db/health` (used by all three probes) answers plain JSON, and the
in-app "monitoring" tab is per-connection `INFO`/`pg_stat` reading rendered in
the browser — not a scrape target. The only `/metrics` in the upstream repo
belongs to its separate OpenShift operator, which this package does not deploy.
Its database *is* observable, though: `libredb-db` sets
`monitoring.enablePodMonitor`, so it appears in the CloudNativePG dashboards
like every other platform cluster.

## Enabled where

Enabled in both the local curated core and production. Locally it costs one
256Mi pod plus the single CNPG instance holding its own state, and it is the
only UI a developer has onto the platform's databases.

### A skipped connection logs at ERROR

Studio logs one `seed/credential-resolver … Seed connection skipped` line per
unresolved connection each time it re-reads the seed file (60s cache TTL), at
ERROR level even though skipping is the designed behaviour. On a local cluster
running the curated core that is a handful of lines naming the packages that
are not enabled — informative, not a fault.
