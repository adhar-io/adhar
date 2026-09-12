# Monitoring

This repo was scaffolded with monitoring baked in. Two objects in `manifests/`
do the work, and both are applied by the same ArgoCD Application that deploys
the app — there is nothing to enable and no ticket to file.

| File | Object | What it does |
|------|--------|--------------|
| `manifests/servicemonitor.yaml` | `ServiceMonitor` | Tells the platform Prometheus to scrape this app |
| `manifests/dashboard.yaml` | `ConfigMap` | Ships a Grafana dashboard for this app |

Both carry `argocd.argoproj.io/sync-wave: "10"`, the platform's ordering rule
for monitoring objects, so they are applied after the workload and after the
Prometheus Operator CRDs exist on a fresh cluster.

## What is scraped

The ServiceMonitor selects this app's own `Service` (`manifests/service.yaml`)
inside this app's own namespace (`manifests/namespace.yaml`, named after the
app) and scrapes:

* **port** — `http` (the Service's named port; the Dagster webserver HTTP port)
* **path** — `/metrics`
* **interval** — `60s`, with a `30s` scrape timeout
* **honorLabels** — `true`, so labels the app sets itself win

Because the ServiceMonitor sets `jobLabel: app.kubernetes.io/name`, every series
from this app lands with `job="<app name>"`. That is the label the dashboard
queries on, so nothing has to be rewired when the app is renamed or moved.

The platform Prometheus has an empty `serviceMonitorSelector`, so this object is
picked up as-is — no extra release label required.

The placeholder image this skeleton ships
(`docker.io/dagster/dagster-webserver:1.9.5`) does **not** serve `/metrics`, so
until you replace it the target shows as **DOWN** under
**Prometheus -> Status -> Targets** and the dashboard panels stay empty. That is
expected, not a broken install — the object ships from day one so that becoming
observable is a code change in this repo, never a platform change. See
[Exposing the metrics](#exposing-the-metrics) below.

## The dashboard

`manifests/dashboard.yaml` is a ConfigMap labelled `grafana_dashboard: "1"`. The
Grafana sidecar runs with `searchNamespace: ALL`, so it is found in this app's
namespace, and the `grafana_folder: "Application"` annotation files it under:

> **Grafana -> Dashboards -> Application -> `<app name>` — RED**

Platform components (ArgoCD, Cilium, Kargo, ...) live in the **Platform** folder;
app-runtime dashboards like this one belong in **Application**.

It renders the RED method from two conventional Prometheus series:

| Signal | Query |
|--------|-------|
| **Rate** | `sum(rate(http_requests_total{...}[5m]))` |
| **Errors** | 5xx rate divided by total rate |
| **Duration** | `histogram_quantile(...)` over `http_request_duration_seconds_bucket` at p50 / p95 / p99 |

A `namespace` dashboard variable defaults to the app's namespace so the same
JSON works unchanged for a preview environment — just type the preview namespace
into the variable box.

The datasource is pinned to the `prometheus` UID, matching the rest of the
platform's dashboards (`prometheus`, `loki`, `tempo`, `mimir`).

## Exposing the metrics

The placeholder `dagster-webserver` image exposes no Prometheus endpoint, so
the target is DOWN until your own image does. Two supported routes:

* **Webserver metrics** — bake `prometheus_client` into your Dagster image and
  mount its ASGI app so `http_requests_total` and
  `http_request_duration_seconds` (with a `status` label) are served on the
  `http` port's `/metrics`. Every panel then lights up unchanged.
* **Run-level metrics** — asset materializations are short-lived, so counters
  scraped from the webserver will not see them. Emit those from a Dagster
  sensor to the platform Prometheus Pushgateway, or record them as Dagster
  asset metadata and read them in the Dagster UI.


## Adding custom metrics

Anything your app exposes on the same `/metrics` endpoint is collected by the
same scrape — no new ServiceMonitor, no platform change:

1. Register the metric in your code (a counter, gauge or histogram).
2. Deploy. Within one scrape interval (60s) it is queryable in Grafana as
   `your_metric{namespace="<app>", job="<app>"}`.
3. Add a panel to `manifests/dashboard.yaml` — edit the dashboard in Grafana,
   use **Export -> JSON**, and paste the result back into the `data` block. The
   ConfigMap is the source of truth; Grafana runs with `allowUiUpdates: false`,
   so UI edits are lost on the next sync unless you commit them here.

Two conventions worth keeping:

* **Low cardinality.** Never put a user id, request id or raw URL path in a
  label. Use a route template (`/orders/:id`), not the expanded path.
* **Histograms over averages.** `histogram_quantile` on a `_bucket` series is
  what makes the p95/p99 panels work; a pre-averaged gauge cannot be aggregated
  across pods.

## Logs and traces

Metrics are only one third of the picture, and the other two need no per-repo
object at all:

* **Logs** — Alloy collects stdout from every pod cluster-wide. Query
  `{namespace="<app>"}` in Grafana's Loki datasource. Log in JSON so the
  fields are parseable.
* **Traces** — send OTLP to the platform Tempo collector; traces then correlate
  with these panels by `namespace` and `job`.
