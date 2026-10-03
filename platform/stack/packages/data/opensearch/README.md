# OpenSearch

The platform's shared search and analytics cluster, managed by the
[OpenSearch Kubernetes operator](https://github.com/opensearch-project/opensearch-k8s-operator).

| | |
|---|---|
| Operator | `opensearch-operator` chart **3.0.14** (app 3.0.0) |
| Cluster | `opensearch-cluster` chart **3.3.5**, OpenSearch **3.8.0** |
| API group | `opensearch.org/v1` (the legacy `opensearch.opster.io` CRDs are **not** installed) |
| Namespace | `adhar-system` |
| Enabled | production only (`localSafe: false`) |

## What connects to what

The operator derives every Service name from the `OpenSearchCluster` CR, which is
named `opensearch`. **Renaming it renames the Services and breaks the routes and
both consumers.**

| Service | Port | Who talks to it |
|---|---|---|
| `opensearch` | 9200 (HTTPS) | `data/open-metadata` (through its nginx TLS bridge), `data/libredb-studio` |
| `opensearch-nodes` | 9300 | node discovery (headless, internal) |
| `opensearch-dashboards` | 5601 (HTTP) | `opensearch-oauth2-proxy` → the Cilium Gateway |

`opensearch-dashboards` is the same name the old plain chart produced, which is
why `manifests/httproute.yaml` and `manifests/sso.yaml` needed no change. The
REST Service did change: it was `opensearch-cluster-master`.

## Credentials

Both passwords are **generated** — nothing is committed.

```bash
adhar get secrets -p opensearch
```

| Secret | Keys | Used by |
|---|---|---|
| `opensearch-admin-credentials` | `username`, `password` | the operator (hashes it into the security config), OpenMetadata, LibreDB Studio |
| `opensearch-monitoring-credentials` | `username`, `password` | the `adhar-monitoring` OpensearchUser and the generated ServiceMonitor's scrape |

Rotation is deliberate, not automatic: both ExternalSecrets are
`refreshPolicy: CreatedOnce` because the admin password's **hash** is written
into the cluster's security config at bootstrap, and silently reissuing the
plaintext would leave a Secret that no longer opens anything. To rotate, delete
the Secret and let ESO regenerate it, then let the operator reconcile the hash.

## Day 2

Everything below is a CR. None of it is a REST call you have to remember.

**Scale out.** Edit `cluster.nodePools` in `values-cluster.yaml`, re-run
`generate-manifests.sh`, commit, push to Gitea. `confMgmt.smartScaler: true`
drains shards and excludes a departing node from the voting configuration first,
so a scale-down cannot lose quorum. To split masters from data nodes, replace the
single `nodes` pool with `masters` (roles `[master]`, 3 replicas) and `data`
(roles `[data, ingest]`) — the operator relocates shards as it rolls.

**Snapshots.** `manifests/snapshot-policy.yaml` is an `OpensearchSnapshotPolicy`:
daily at 01:30 UTC into the `adhar-rustfs` repository (the `adhar-backups` bucket
in RustFS, under `base_path: opensearch`), 14 days of history with a floor of 5
snapshots. This is the cluster's **only** durability story — the default
StorageClass is node-local `adhar-local`, so the PVC does not survive its node,
and the cluster runs one replica.

Restore has no CRD; it is an explicit operator action:

```bash
PASS=$(kubectl -n adhar-system get secret opensearch-admin-credentials -o jsonpath='{.data.password}' | base64 -d)
kubectl -n adhar-system exec opensearch-nodes-0 -- \
  curl -sk -u "admin:$PASS" "https://localhost:9200/_snapshot/adhar-rustfs/_all?pretty"
kubectl -n adhar-system exec opensearch-nodes-0 -- \
  curl -sk -u "admin:$PASS" -X POST "https://localhost:9200/_snapshot/adhar-rustfs/<snapshot>/_restore"
```

**Index lifecycle.** `values-cluster.yaml` ships one `OpenSearchISMPolicy`,
`adhar-timeseries`: roll over at 20 GB or 7 days, drop the replica at 7 days,
delete at 30. Its `ismTemplate` claims `logs-*`, `events-*`, `traces-*` and
`adhar-*` — deliberately **not** `*`, because OpenMetadata's catalogue indices are
not dated and must not be rolled over or deleted underneath it.

**Users and roles.** `roles`, `users` and `usersRoleBinding` in
`values-cluster.yaml` render `OpensearchRole` / `OpensearchUser` /
`OpensearchUserRoleBinding` CRs. An `OpensearchUser`'s name **is** its OpenSearch
username (the CRD has no `username` field) and its password comes from a Secret.
Reserved accounts (`admin`, `kibanaserver`) cannot be managed this way.

## Metrics

Two separate scrapes, and it is worth keeping them apart:

- **the cluster** (`opensearch_*`) — the operator installs the Prometheus
  exporter plugin and generates the ServiceMonitor itself, because
  `general.monitoring.enable: true`. It wires the credentials
  (`opensearch-monitoring-credentials`) and TLS to match the cluster it built.
  `manifests/dashboard.yaml` renders these; they are the first data those panels
  have ever had.
- **the operator** (`controller_runtime_*`, `workqueue_*`) —
  `manifests/operator-metrics.yaml`, scraped over the authenticated
  controller-runtime endpoint with the Prometheus ServiceAccount token.

## Version coupling — read before bumping

`general.version` in `values-cluster.yaml` is pinned to **3.8.0** and must not be
raised on its own. With monitoring enabled the operator downloads a Prometheus
exporter plugin whose release is version-matched to OpenSearch
(`prometheus-exporter-<version>.0.zip`), and
[Aiven-Open/prometheus-exporter-plugin-for-opensearch](https://github.com/Aiven-Open/prometheus-exporter-plugin-for-opensearch/releases)
tops out at 3.8.0.0. On OpenSearch 3.9.0 that fetch 404s and **every node fails
to start**. Move the engine and the plugin together, or set
`general.monitoring.pluginUrl` explicitly.

## Known sharp edges

- The validating webhook is `failurePolicy: Ignore`, not the chart's `Fail`. Its
  serving certificate comes from cert-manager, which is a separate Argo CD
  Application with no ordering relationship to this one — with `Fail`, a
  cert-manager that is still starting rejects the wave-5 `OpenSearchCluster` and
  the package deadlocks on a certificate waiting for a controller in another app.
- `opensearch-cluster` 3.3.5 ships `secret:` followed by an unclosed comment, so
  its default for `dashboards.tls.secret` is `null`, and the chart renders that
  into the CR. The CRD declares the field `type: object` without `nullable`, so
  the API server rejects the whole cluster. `values-cluster.yaml` overrides it
  with `{}`.
- OpenMetadata still reaches OpenSearch through an nginx bridge
  (`openmetadata-opensearch`) because its search client offers no
  verification-skip switch and the operator's CA is not in the JVM trust store.
  The *hostname* half of that problem is gone — the operator's certificate covers
  the real Service DNS name, unlike the old demo certificate — so the bridge is
  now a choice between one nginx and building a JKS truststore, not a necessity.
