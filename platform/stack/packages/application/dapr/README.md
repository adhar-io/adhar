# Dapr

Dapr 1.18.3: service invocation, pub/sub, state, locks, configuration, secrets,
bindings, actors, workflows and the Jobs API, delivered to an app as a `daprd`
sidecar. Enabled in the production profile; off in the local one.

## What runs where

| Piece | Where | Source |
| --- | --- | --- |
| Control plane: operator, sentry, sidecar injector (Deployments); placement and scheduler (StatefulSets) — one replica each | `adhar-system` | `manifests/install.yaml`, generated (below) |
| `dapr-state`: a standalone Valkey with an append-only file, the store behind every environment's state, locks, configuration and (without Kafka) pub/sub | `adhar-system` | `manifests/state-store.yaml` |
| The platform namespace's own `pubsub` and `statestore` (dapr-state, database 0) | `adhar-system` | `manifests/pubsub.yaml`, `manifests/statestore.yaml` |
| Every environment's building blocks (below) | each environment namespace (`dev`, `test`, `prod`, `<project>-<env>`) | the Environment composition, `platform/controlplane/configuration/compositions/env/local.yaml` |
| Monitors, alerts | `adhar-system` | `manifests/servicemonitor.yaml`, `manifests/rules.yaml` |
| Dashboards *Delivery / Dapr* (`adhar-dapr`) and the Dapr row of *Delivery / Developer Platform* | Grafana | `observability/kube-prometheus/dash_delivery.py` |

mTLS between sidecars is on cluster-wide (`Configuration/daprsystem`, 24h
workload certificates signed by sentry). The scheduler keeps jobs, actor
reminders and workflow timers in an embedded etcd on a 16Gi node-local volume;
it is a ONE-member etcd (`post-render.py`), which is why it can lose its leader
in a way `/healthz` does not show — see the last section.

## The environment contract

Every environment namespace gets these, from its Environment XR, on any cluster
that runs Dapr (the composition checks for the `components.dapr.io` CRD, so the
local profile composes environments exactly as before):

| Kind | Name | What |
| --- | --- | --- |
| Component | `pubsub` | `pubsub.kafka` on the platform's Kafka (`adhar-kafka-kafka-bootstrap.adhar-system.svc.cluster.local:9092`, the `plain` listener: PLAINTEXT, no auth) when the cluster runs it; otherwise `pubsub.redis` (Redis streams) on dapr-state |
| Component | `statestore` | `state.redis` on dapr-state; the actor state store, so also the Workflow store |
| Component | `lockstore` | `lock.redis` on dapr-state (`ignoreErrors`: optional) |
| Component | `configstore` | `configuration.redis` on dapr-state, a database of its own (`ignoreErrors`: optional) |
| Component | `secretstore` | `secretstores.kubernetes` over this namespace's Secrets |
| Configuration | `adhar-dapr` | Tracing to Alloy (`alloy.adhar-system.svc.cluster.local:4317`, OTLP gRPC; every trace in dev/test, one in ten in prod), low-cardinality HTTP metrics with path patterns for `/api/{version}/...`, Dapr error codes recorded |
| Resiliency | `adhar-default` | Defaults only: invocation timeout 60s and a circuit breaker after 5 consecutive failures; component timeouts and short retries for the building blocks above; ~1 minute of backoff when delivering an event |
| Role | `dapr-secret-reader` | `get`/`list` Secrets here — what an app binds before reading through `secretstore` |

The names are the ones Adhar Kit's `DaprProperties` and the Function composition
(`dapr.io/config: adhar-dapr`, `pubsubname: pubsub`) use. None is scoped to
app-ids: they belong to the environment, for every app in it. An app that wants
a private component declares its own under another name, with `scopes:` set to
its app-id.

**Choosing the broker.** `spec.parameters.dapr.pubsub` on the Environment:
`auto` (default) follows the cluster — Kafka when `adhar-kafka`'s bootstrap
Service exists, Redis streams otherwise — and `kafka` / `redis` pin it.
`spec.parameters.dapr.enabled: false` composes none of the above. What an
environment resolved to is on the XR: `kubectl -n adhar-system get environment
<project>-<env> -o jsonpath='{.status.dapr}'`. Pin the broker before enabling or
removing Kafka on a running cluster: a switch leaves messages not yet delivered
on the old broker, and running sidecars keep the old component until they
restart.

**Isolation in shared stores.** Dapr prefixes some keys and not others; the
contract closes each gap:

- pub/sub: `consumerID: "{namespace}"`. Dapr expands it to `<namespace>.<app-id>`,
  so each app is its own consumer group, and because the value names
  `{namespace}` Dapr also prefixes every topic with the namespace on the wire:
  `orders` published in `dev` is the Kafka topic (or Redis stream) `devorders`.
  Environments never read each other's events. To pre-create a Kafka topic for
  Dapr with a `Topic`, set its `topicName` to `<namespace><topic>`; otherwise
  the broker creates it on first use with one partition. A new subscriber
  starts from the oldest retained message (`initialOffset: oldest`, as a Redis
  stream group does), so events published before its first start are not lost.
- state: `keyPrefix: namespace` — keys are `<namespace>.<app-id>||<key>`.
- locks: `keyPrefix: "{namespace}"` — keys are `lock||<namespace>.<app-id>||<resource>`.
- actor state, workflow state and configuration keys are NOT prefixed by Dapr,
  so dapr-state gives each tier its own database: 1/2/3 for dev/test/prod state,
  locks and streams, 9/10/11 for their configuration (0 is `adhar-system`).
  Within one tier, actor state is keyed `<app-id>||<type>||<id>`: an app-id must
  be unique among the environments of a tier. Use the Application's name, which
  is.
- The pub/sub Redis streams share dapr-state's 160 MB (`noeviction`) with state,
  so each stream is capped at ~10,000 entries (`maxLenApprox`); a subscriber
  further behind than that loses the oldest. That is the fallback; Kafka keeps
  what its topic retention says.

## How an app opts in

Pod template annotations (a Deployment's or Rollout's `spec.template.metadata`):

```yaml
annotations:
  dapr.io/enabled: "true"
  dapr.io/app-id: orders              # the Application's name (unique in the tier)
  dapr.io/app-port: "8080"            # the port daprd calls the app on
  dapr.io/config: adhar-dapr          # tracing, metrics, path patterns
  dapr.io/enable-metrics: "true"
  dapr.io/log-as-json: "true"
  # Optional: right-size the sidecar. Without these the namespace's LimitRange
  # gives daprd 100m/128Mi requested and 500m/512Mi limits, which count against
  # the environment's quota like any container.
  dapr.io/sidecar-cpu-request: "50m"
  dapr.io/sidecar-memory-request: "64Mi"
  dapr.io/sidecar-memory-limit: "256Mi"
```

A Function gets these from its composition (`spec.parameters.dapr.enabled`, or
any `topic` trigger), with `dapr.io/metrics-port: "9095"` because Knative's
queue-proxy holds 9090.

To read Secrets through `secretstore`, bind the app's ServiceAccount (the
secret store uses the pod's own identity, so this is what scopes it to its
namespace):

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: orders-dapr-secrets
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: dapr-secret-reader}
subjects:
  - {kind: ServiceAccount, name: orders}
```

To change a resiliency default for one app, add a Resiliency of your own with
an explicit target (`targets.apps.<app-id>` or `targets.components.<name>`):
explicit targets win over the `Default*Policy` names in `adhar-default`.

Check a sidecar: `kubectl -n <ns> get pods -l dapr.io/sidecar-injected=true`, and
its view of the world, `kubectl get --raw
/api/v1/namespaces/<ns>/pods/<pod>:3500/proxy/v1.0/metadata` (components loaded,
subscriptions, actors). A pod annotated `dapr.io/enabled` without a `daprd`
container missed injection — the webhook fails open (DaprSidecarInjectionFailed).

## Observability

- **Dashboards.** *Delivery / Dapr*: health at a glance, the control plane
  (scheduler leader, connected sidecars, jobs; placement; sentry; injector;
  operator), then per `$namespace` / `$app_id`: the Dapr API (rate, p50/p95,
  error ratio), calls into the app, error codes, sidecar CPU and memory, service
  invocation edges, pub/sub, state/configuration/secrets/locks/bindings, actors,
  workflows, resiliency and circuit breakers, mTLS, and dapr-state.
  *Delivery / Developer Platform* keeps a Dapr summary row with a scheduler
  leader tile.
- **Metrics.** The PodMonitor `dapr-sidecars` scrapes every injected sidecar and
  copies its `dapr.io/app-id` label onto every series as `app_id`. Daprd exports
  through OpenCensus: counters keep their names without `_total`
  (`dapr_http_server_request_count`), histograms are in milliseconds, and most
  series appear only after the first event. With `increasedCardinality: false`
  only `dapr_http_server_request_count` / `_latency` carry the HTTP status and
  path, and the path is the matching pattern, or empty.
- **Recording rules.** `dapr:http_requests:rate5m`, `dapr:http_latency_ms:p95`,
  `dapr:invocation_sent:rate5m`, `dapr:pubsub_ingress:rate5m`,
  `dapr:pubsub_egress:rate5m`, `dapr:state_ops:rate5m` — per namespace and
  app-id.
- **Alerts** (`manifests/rules.yaml`): DaprSchedulerNoLeader,
  DaprPlacementNoLeader, DaprControlPlaneDown, DaprIssuerCertExpiring,
  DaprCertSignFailures, DaprSidecarInjectionFailed, DaprComponentInitFailed,
  DaprCircuitBreakerOpen, DaprPubsubDrops, DaprStateStoreDown,
  DaprStateStoreMemoryHigh.

## Regenerating the manifests

`./generate-manifests.sh` renders the chart (`values.yaml`) into
`manifests/install.yaml` and runs `post-render.py`, which makes the scheduler a
one-member etcd (replica count AND peer list, together) and gives it the
node-bound priority its volume needs. Never hand-edit `install.yaml`: the next
render reverts it, and on 2026-10-16 half of such an edit survived and left the
scheduler leaderless. `TestDaprSchedulerIsASingleMemberEtcd` pins the result.

## Recovering a leaderless scheduler

Symptom: `etcd_server_has_leader{job="scheduler"}` is 0 (DaprSchedulerNoLeader;
the *Scheduler leader* tile is red) while the pod reads Ready, and the log loops
on `starting a new election` with `MsgPreVote` sent to members that do not
exist. The scheduler's data directory still records an older, larger cluster:
etcd reads its membership from the data directory and ignores
`--etcd-initial-cluster` once one exists, so the one-member flag never takes
effect until the directory is empty.

A scheduler that never had a leader holds no data (its raft log stops at index
3), so the fix is to delete its volume. On a scheduler that HAD a leader, the
same steps also delete every pending job and reminder.

```sh
# 1. Confirm: no leader, raft log at index 3, PreVotes to absent members.
kubectl -n adhar-system logs dapr-scheduler-server-0 --tail=20 | grep -E 'index: 3|MsgPreVote|new election'

# 2. Delete the claim (held until its pod goes), then the pod.
kubectl -n adhar-system delete pvc dapr-scheduler-data-dir-dapr-scheduler-server-0 --wait=false
kubectl -n adhar-system delete pod dapr-scheduler-server-0

# 3. The StatefulSet recreates the pod with a fresh claim. If the new pod stays
#    Pending on the old, terminating claim, delete the pod once more once the
#    claim is gone.
kubectl -n adhar-system get pvc,pod | grep dapr-scheduler

# 4. Verify: a leader, and sidecars reconnect.
kubectl -n adhar-system logs dapr-scheduler-server-0 | grep -m1 'became leader'
```

Then `etcd_server_has_leader{job="scheduler"}` reads 1, and
`dapr_scheduler_sidecars_connected` counts the running sidecars.
