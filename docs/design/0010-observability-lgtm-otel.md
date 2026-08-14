# Low-Level Design — Observability: OpenTelemetry collection, Grafana LGTM storage, hub-and-spoke

Detailed design for [ADR-0010](../adr/0010-observability-lgtm-otel.md). This documents the observability stack as actually built under `platform/stack/packages/observability/`: the Alloy collection contract, the LGTM(P) storage backends, Grafana as the single pane, SSO/gateway exposure, and the (already-wired) hub-and-spoke spoke path.

## 0. Context recap

ADR-0010 standardises on **OpenTelemetry as the collection contract** and the **Grafana stack as storage/UX**. Grafana **Alloy** is the single shipping agent (metrics/logs/traces); Prometheus + **Mimir/Loki/Tempo** (plus **Pyroscope** for profiles) are the stores; **Grafana** is the one pane; eBPF (**Beyla/Pixie/Hubble**) gives value before instrumentation. Everything ships as ordinary ApplicationSet packages (ADR-0004) so components are swap/disable-able per environment. Topology is single-cluster in-cluster (T1/T2) and **hub-and-spoke** at T3 — spokes run only Alloy, the management cluster hosts storage and query.

## 1. Signal → tool map (as built)

| Signal | Collector | Store | Grafana datasource (uid) |
|---|---|---|---|
| Metrics (cluster-local) | Prometheus Operator scrape (ServiceMonitor/PodMonitor) | Prometheus (`prometheus-operated:9090`, 10d) | `prometheus` (default) |
| Metrics (long-term / multi-cluster) | Alloy `prometheus.remote_write` | Mimir (`mimir-distributor:8080`) | `mimir` (type `prometheus`) |
| Logs | Alloy `loki.source.kubernetes` | Loki (`loki:3100`) | `loki` |
| Traces | Alloy `otelcol.receiver.otlp` (OTLP 4317/4318) | Tempo (`tempo:3200`, ingest 4317/4318) | `tempo` |
| Profiles | Pyroscope agent / SDK | Pyroscope (`pyroscope:4040`) | `pyroscope` |
| Network flows | Hubble (Cilium data path, ADR-0002) | Prometheus (PodMonitor `hubble-metrics`) | `prometheus` |
| Alerts | Prometheus rules → Alertmanager | Alertmanager (`v2`) | — |

All services live in `adhar-system`. Package directories are under `platform/stack/packages/observability/` (note: the Loki package dir is `loki-stack/`, wired in the appset as package name `loki`).

## 2. Collection — Alloy (`observability/alloy/`)

Alloy runs as a **DaemonSet** (`grafana/alloy:v1.16.1`, chart `alloy-1.8.2`) rendered into `manifests/install.yaml`. The `config.alloy` in the `alloy` ConfigMap defines the three shipping pipelines; every pipeline forwards to the **hub** and stamps `cluster` as an external label so a single Mimir/Loki/Tempo can separate spokes hub-side.

```
discovery.kubernetes {pods,nodes,services,endpoints,endpointslices,ingresses}

metrics:  prometheus.scrape "annotated_pods"  (keep prometheus.io/scrape=true)
            → prometheus.remote_write "hub"   → sys.env("HUB_MIMIR_URL")   [external_labels.cluster]
logs:     loki.source.kubernetes "pods"       (relabel ns/pod/container/node/app)
            → loki.write "hub"                → sys.env("HUB_LOKI_URL")    [external_labels.cluster]
traces:   otelcol.receiver.otlp "workloads"   (grpc 0.0.0.0:4317, http 0.0.0.0:4318)
            → otelcol.exporter.otlphttp "hub"  → sys.env("HUB_TEMPO_URL")  [tls insecure_skip_verify]
```

The pipeline endpoints come from the **`observability-hub` ConfigMap** (`alloy/manifests/hub-endpoints.yaml`), mounted onto the Alloy container via `envFrom.configMapRef: observability-hub` (kept on regeneration — see the comment in `install.yaml`). Defaults are the hub's own in-cluster services (correct when Alloy runs on the management cluster):

```yaml
# observability-hub ConfigMap (adhar-system)
HUB_MIMIR_URL:   http://mimir-distributor.adhar-system.svc.cluster.local:8080/api/v1/push
HUB_LOKI_URL:    http://loki.adhar-system.svc.cluster.local:3100/loki/api/v1/push
HUB_TEMPO_URL:   http://tempo.adhar-system.svc.cluster.local:4318
HUB_CLUSTER_NAME: management
```

On workload-cluster spokes this ConfigMap is overridden with the hub's external Gateway URLs (`https://mimir.<host>/api/v1/push`, `https://loki.<host>/loki/api/v1/push`, `https://tempo.<host>`) and the spoke's `HUB_CLUSTER_NAME`. Alloy's `ClusterRole` grants read on pods/services/endpoints/nodes plus the `monitoring.coreos.com` CRDs (podmonitors/servicemonitors/probes/scrapeconfigs) and `monitoring.grafana.com` podlogs. A `config-reloader` sidecar (`prometheus-config-reloader`) hot-reloads `config.alloy` on ConfigMap change.

## 3. eBPF & network flows

- **Hubble** (`observability/hubble/`) — network flows from the Cilium data path (ADR-0002). Enabled by default. Scraped through PodMonitors in the kube-prometheus package (`podmonitor-cilium.yaml`: `cilium-agent`, `cilium-operator`, `hubble` on port `hubble-metrics`) because Cilium exposes metrics only as pod ports; the PodMonitors live in the kube-prometheus package so they apply *after* the `monitoring.coreos.com` CRDs exist (Cilium bootstraps first). Feeds the Networking-folder dashboards.
- **Beyla** (`observability/beyla/`), **Pixie** (`observability/pixie/`) — eBPF auto-instrumentation, both wired but `enabled: "false"` in the local appset (Pixie ships `install.yaml` only, no values). Available before any code instrumentation.
- **Faro** (`observability/faro/`) — frontend/RUM collection into Alloy's Faro receiver; disabled by default.

## 4. Storage backends

| Package | Kind / mode | Object store | Retention / size | External route |
|---|---|---|---|---|
| `kube-prometheus` | Prometheus Operator, 1 replica | local TSDB | `retention: 10d` | `prometheus.adhar.localtest.me` (oauth2-proxy) |
| `mimir` | `mimir-distributed` microservices (distributor/ingester/querier/query-frontend/compactor/store-gateway + nginx gateway) | **S3** via bundled MinIO (`blocks/alertmanager/ruler_storage.backend: s3`) | policy-based (object store) | `mimir.adhar.localtest.me` → `mimir-distributor:8080` |
| `loki-stack` | Loki SingleBinary `StatefulSet`, `object_store: filesystem`, `schema: v13` | filesystem PVC (`10Gi`) | disk-bound (local) | `loki.adhar.localtest.me` → `loki:3100` |
| `tempo` | Tempo `StatefulSet`, `storage.backend: local` | local PVC | disk-bound (local) | `tempo.adhar.localtest.me` → `tempo:4318` |
| `pyroscope` | Pyroscope `2.2.1` | local | disk-bound | `pyroscope.adhar.localtest.me` → `pyroscope:4040` |

Prometheus is the local system-of-record for cluster metrics; Mimir is the long-term/multi-cluster tier that Alloy `remote_write`s into and that Grafana queries via `mimir-query-frontend:8080/prometheus`. Loki/Tempo run scaled-down single-binary locally (filesystem/local) and are meant to move to object storage in production (retention becomes policy, per the ADR). The Mimir gateway nginx (`mimir-gateway-nginx` ConfigMap) fans `/distributor`, `/prometheus`, `/compactor` paths to the component headless services.

## 5. Grafana — the single pane (`observability/kube-prometheus/`)

Grafana ships inside the kube-prometheus-stack chart (`prometheus-grafana` Service, port 80). Key `values.yaml` choices:

- **Datasources** (`grafana.additionalDataSources` + the datasource sidecar, label `grafana_datasource: "1"`):
  - `prometheus` — default (chart-provisioned, `prometheus-operated:9090`, `httpMethod: POST`).
  - `loki` — `loki:3100`, with a `derivedFields` TraceID regex linking to `datasourceUid: tempo` (logs → traces).
  - `tempo` — `tempo:3200`, `tracesToLogsV2 → loki`, `serviceMap → prometheus` (traces → logs, service graph).
  - `mimir` — type `prometheus`, `mimir-query-frontend:8080/prometheus` (long-term metrics).
  - `pyroscope` — `grafana-pyroscope-datasource`, `pyroscope:4040` (Explore → Profiles / flame graphs).
  - A second copy of Loki+Tempo is also provisioned via `manifests/extra-datasources.yaml` (a standalone `datasources-logs-traces` ConfigMap picked up by the same sidecar).
- **Correlation** — the derivedFields/tracesToLogs/serviceMap wiring gives one-click metrics↔logs↔traces navigation across the four datasources.
- **Dashboards** — chart defaults disabled (`defaultDashboardsEnabled: false`); a curated set of **27 in-repo dashboard ConfigMaps** (`manifests/dashboard-*.yaml`, label `grafana_dashboard: "1"`) is filed into **two folders** by the sidecar via `folderAnnotation: grafana_folder` + `foldersFromFilesStructure: true`: **Platform** (24: k8s, nodes, Cilium/Hubble, Loki, CNPG, Harbor, Keycloak, Vault, Kafka, …) and **Application** (3: Spring Boot / JVM Micrometer). Dashboard JSON is generated by `generate-dashboards.py`/`.sh` from grafana.com ids and committed — no runtime egress. `datasource` inputs are auto-pinned to the provisioned UIDs.
- **Scrape discovery** — Prometheus `serviceMonitorSelector`/`podMonitorSelector` are `{}` (empty), so any ServiceMonitor/PodMonitor in the cluster is picked up with no release label. Package-local scrape configs live in kube-prometheus: `servicemonitor-gitea.yaml`, `podmonitor-argocd.yaml`, `podmonitor-cilium.yaml`. On-kind-dead scrape jobs are disabled in `defaultRules`/scrape config (`etcd`/`kubeControllerManager`/`kubeProxy`/`kubeScheduler` false — those bind to 127.0.0.1 and kube-proxy is replaced by Cilium).

## 6. SSO & gateway exposure

- **Grafana OIDC** — `grafana.ini.auth.generic_oauth` points at Keycloak realm `adhar`; `role_attribute_path` maps realm groups (`platform-admin`→Admin, `platform-developer`→Editor, else Viewer). Client id/secret arrive out-of-band in the `grafana-oidc` Secret (an ExternalSecret in the keycloak package, ADR-0009), consumed via `envFromSecrets` with `optional: true` so Grafana boots before Keycloak exists (local `admin`/`prom-operator` is break-glass). `tls_skip_verify_insecure: true` because the Gateway serves the self-signed platform cert. Because env vars are read once at start, `manifests/grafana-oidc-reload.yaml` is a **PostSync hook Job** that restarts Grafana only if the running pod lacks the OAuth secret env — idempotent, converges login once Keycloak syncs (ADR-0013 pattern).
- **Prometheus SSO** — Prometheus has no native OIDC, so `manifests/prometheus-oauth2-proxy.yaml` puts an oauth2-proxy in front on its own host (`prometheus.adhar.localtest.me`), keeping Prometheus at root for in-cluster datasource/scrape.
- **Gateway routes** — HTTPRoutes attach to `adhar-gateway` (ADR-0002): `grafana-httproute.yaml` (`grafana.adhar.localtest.me` + `localhost` → `prometheus-grafana:80`), plus `loki/mimir/tempo/pyroscope` routes in their packages. The Mimir/Loki/Tempo routes double as **spoke ingestion endpoints** (comment: "roadmap P2.3"): Alloy on a workload cluster ships to these hostnames; in-cluster consumers keep using the Services directly.

## 7. Package model & enabled-gating (ADR-0004)

Every component is a list element in `platform/stack/adhar-appset-local.yaml`, gated by a `selector` on `enabled: "true"`. The local curated core enables the **full LGTM+profiles path plus agent**:

| enabled `"true"` (local core) | enabled `"false"` (wired, off) |
|---|---|
| metrics-server, kube-prometheus, loki, alloy, tempo, pyroscope, mimir, headlamp, hubble | opencost, oncall, beyla, faro, pixie, victoria-metrics |

`victoria-metrics` is the pluggable metrics alternative (ADR-0010's "swap components" clause) — wired but not integration-tested to the default path's depth. `headlamp` (OIDC-gated K8s UI) and `metrics-server` round out the default operator experience.

## 8. Hub-and-spoke topology

The spoke path is already built (ahead of the ADR's "hub lands in Phase 2" status):

- `platform/stack/adhar-appset-workload.yaml` (`adhar-workload-clusters`) targets every ArgoCD-registered workload cluster (`clusters` generator, selector `adhar.io/cluster Exists`) and ships a **thin agent set**: `metrics-server`, `kyverno(+policies)`, and **`alloy`** only. The management/local cluster is not matched (its in-cluster destination has no `adhar.io/cluster` label), so storage stays on the hub.
- Each spoke's `observability-hub` ConfigMap is overridden with the hub's external Gateway URLs; Alloy `remote_write`/`loki.write`/`otlphttp` to `mimir/loki/tempo.<host>` through the platform Gateway, tagging every sample/stream with `cluster = HUB_CLUSTER_NAME`.
- Hub-side, a single Mimir/Loki/Tempo separates clusters by the `cluster` label; Grafana queries all clusters from one pane. This is the observability leg of the control/data-plane split (see [design 0023 §8, §2.2 `ensureObservability`](0023-control-dataplane-separation.md)).

## 9. Alerting & cost

- **Alertmanager** ships with kube-prometheus (`apiVersion: v2`, 1 replica, 120h retention, default inhibit/route with a `null` receiver + Watchdog). `defaultRules.create: true` with the kind-incompatible groups pruned.
- **OnCall** (`observability/oncall/`) — Grafana OnCall for escalation/routing; `enabled: false` locally.
- **OpenCost** (`observability/opencost/`) — spend attribution against Prometheus metrics; `enabled: false` locally.

## Testing

- **Parity** — `platform/controllers/adharplatform/parity_test.go` asserts every appset list element resolves to a real `platform/stack/packages/<category>/<name>/manifests` path (covers all observability packages incl. `loki` → `loki-stack/`) and that enabled/namespace fields are well-formed.
- **e2e bootstrap** — `tests/e2e/bootstrap` runs a full `adhar up` and verifies the enabled core (kube-prometheus/loki/alloy/tempo/mimir/pyroscope) syncs Healthy in ArgoCD; `adhar get status` surfaces per-package health.
- **Gaps to add** — a smoke check that (a) Alloy `remote_write` reaches Mimir (`mimir_ingester_ingested_samples_total > 0`), (b) Loki receives pod logs with `namespace`/`pod` labels, (c) an OTLP trace lands in Tempo, and (d) Grafana's five datasources return `200` on `/health`. A spoke-path test (override `observability-hub`, assert `cluster` label appears hub-side) will land with the T3 hub work.

## Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/observability/alloy/manifests/install.yaml` | Alloy DaemonSet + `config.alloy` (metrics/logs/traces → hub), RBAC |
| `platform/stack/packages/observability/alloy/manifests/hub-endpoints.yaml` | `observability-hub` ConfigMap (HUB_{MIMIR,LOKI,TEMPO}_URL, HUB_CLUSTER_NAME) |
| `platform/stack/packages/observability/kube-prometheus/values.yaml` | Prometheus (10d, `{}` selectors, pruned kind rules), Grafana OIDC + datasources + folders |
| `…/kube-prometheus/manifests/extra-datasources.yaml` | Standalone Loki+Tempo datasource ConfigMap (sidecar) |
| `…/kube-prometheus/manifests/grafana-httproute.yaml` | Grafana Gateway route (`grafana.adhar.localtest.me` → `prometheus-grafana:80`) |
| `…/kube-prometheus/manifests/grafana-oidc-reload.yaml` | PostSync hook restarting Grafana once `grafana-oidc` secret exists |
| `…/kube-prometheus/manifests/prometheus-oauth2-proxy.yaml` | oauth2-proxy fronting the Prometheus UI |
| `…/kube-prometheus/manifests/{servicemonitor-gitea,podmonitor-argocd,podmonitor-cilium}.yaml` | Package-local scrape configs (incl. Hubble) |
| `…/kube-prometheus/manifests/dashboard-*.yaml` (27) | Curated Grafana dashboards, foldered Platform/Application |
| `…/kube-prometheus/generate-dashboards.{py,sh}` | Dashboard JSON generation from grafana.com ids |
| `…/mimir/manifests/{install,httproute}.yaml` | Mimir microservices + MinIO/S3 + spoke ingest route |
| `…/loki-stack/manifests/{install,httproute}.yaml` | Loki SingleBinary (filesystem, v13) + spoke ingest route |
| `…/tempo/manifests/{install,httproute}.yaml` | Tempo (OTLP 4317/4318) + spoke ingest route |
| `…/pyroscope/manifests/{install,httproute}.yaml` | Pyroscope profiles store + UI route |
| `…/{hubble,beyla,pixie,faro,headlamp,metrics-server,opencost,oncall,victoria-metrics}/` | Flows, eBPF, RUM, K8s UI, cost, alerting, metrics alternative |
| `platform/stack/adhar-appset-local.yaml` | Enabled-gated wiring of the local curated observability core |
| `platform/stack/adhar-appset-workload.yaml` | Thin spoke profile (alloy-only telemetry shipper) |

## Notes / drift from the ADR

- **Local core is heavier than the ADR text.** ADR-0010 says the local curated core enables "a subset (metrics-server, Hubble, Headlamp by default)" and that "enabling Mimir/Tempo locally needs resources." In practice `adhar-appset-local.yaml` enables the **full LGTM+Pyroscope+Alloy** path by default (`kube-prometheus`, `loki`, `alloy`, `tempo`, `mimir`, `pyroscope` all `"true"`). The ADR consequence should be updated to match.
- **Pyroscope (profiles) is a first-class, default-on backend** with its own datasource and Gateway route, though the ADR only mentions "profiles" in passing and doesn't list Pyroscope in the storage decision.
- **Beyla/Pixie ship disabled** — the ADR's "eBPF options mean value before any instrumentation" is wired but off by default (`enabled: "false"`); only Hubble flows are on locally.
- **Hub-and-spoke is already built, not just planned.** The `observability-hub` ConfigMap, the Mimir/Loki/Tempo ingestion HTTPRoutes, and the `adhar-workload-clusters` appset exist today (marked "roadmap P2.3" in-code), ahead of the ADR status line ("hub topology lands in Roadmap Phase 2").
- **Vestigial Alloy client.** `alloy/values.yaml` still carries a legacy `alloy.config.clients: [http://loki-gateway/...]` block; the rendered `install.yaml` does not use it — logs ship via the `loki.write "hub"` pipeline to `HUB_LOKI_URL` (`loki:3100`). Harmless but stale.
