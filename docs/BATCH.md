# Batch jobs on Adhar

A Spring Batch job written against adhar-kit, declared as a `BatchJob` in your
repository, delivered dev → test → prod like any other application, and operated
from the Console and `adhar batch`. This page is the user guide; the design, with
every decision and its reasoning, is [design 0025](design/0025-batch-platform.md),
and the scheduler decision is [ADR-0028](adr/0028-batch-scheduler.md).

**What ships today is Phase 1: batch on plain Kubernetes.** Volcano queues, gang
scheduling and partitioned jobs are Phase 2: built, shipped disabled in every profile,
and switched on by the platform team when the production gate passes (see
[Volcano](#volcano-queues-and-gangs-phase-2)). Jobs that run inside a long-running
service on Quartz are built in the kit (see
[In-service jobs on Quartz](#in-service-jobs-on-quartz)). Calendars,
dependencies, backfill, deadlines, events and retention are Phase 3 (see
[What is not here yet](#what-is-not-here-yet)).

## The model in one paragraph

Each attempt of a job instance is one Kubernetes `Job` running one pod: your
application's own image, started by the kit's runner. Spring Batch keeps its
JobRepository in your application's own Postgres (schema `batch`), so a restart
resumes from the last committed chunk and skips completed steps; Kubernetes only
re-runs the pod. There is no batch server and no shared credential store: every
operation is a Kubernetes API call made as you, under your own RBAC.

## Start a job

1. Scaffold from the **Spring Batch Job** template (`springboot-batch-service`) in
   the Console. It writes the job, its tests, and a `deploy/` tree whose
   overlays hold a `BatchJob` per environment.
2. Or declare one by hand — [`examples/batch-job.yaml`](../examples/batch-job.yaml)
   is the annotated minimum:

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: BatchJob
metadata:
  name: importer
  namespace: dev               # an environment namespace; its labels place the job
spec:
  parameters:
    job: importJob             # the Spring Batch job bean
    image: harbor-core.adhar-system.svc.cluster.local/library/importer:1.0.0
    schedule:                  # leave it out for an on-demand job
      cron: "30 1 * * *"
      timeZone: Asia/Singapore
    parameters:
      - name: businessDate
        type: date
        source: schedule       # the scheduled time in timeZone, never now()
    database:
      secretRef: importer-db-app
```

The composition (`batchjob-kubernetes`) turns it into a CronJob (when scheduled), a
**run template** `ConfigMap <name>-run`, a ServiceAccount/Role/RoleBinding
`<name>-batch` and a `PrometheusRule <name>-batch`. Every parameter and what it
becomes is mapped in
[`compositions/batchjob/README.md`](../platform/controlplane/configuration/compositions/batchjob/README.md).
A definition the platform cannot run safely is refused with a readable message on
`Synced=False` (name over 40 characters, `priority: critical` or `scaleUp` outside
prod, `concurrencyPolicy: Replace`, a grace period not shorter than the deadline, a
missing `job`/`image`/`database.secretRef`, an `env` name the runner owns).

## Operate it

```bash
adhar batch list --env prod                       # schedule, next run, last run, hold
adhar batch describe importer --env prod          # definition, conditions, last 10 runs
adhar batch run importer -p businessDate=2026-11-03 --wait --follow
adhar batch runs importer --status failed
adhar batch logs importer-09932cb3 -f
adhar batch stop importer-09932cb3                # suspend: STOPPED at the next chunk boundary
adhar batch resume importer-09932cb3              # unsuspend: continue from the last commit
adhar batch restart importer-09932cb3             # same identity, as importer-09932cb3-r1
adhar batch hold importer --reason "feed late"    # suspend the schedule without a commit
adhar batch release importer
```

The Console's **Develop → Batch** area offers the same verbs, each behind the same
RBAC check.

| Operation | What actually happens |
|---|---|
| Run now | A `Job` created from `<name>-run`, named for its identifying parameters |
| Stop / Resume | `Job.spec.suspend` set / cleared |
| Restart | A new run with the same identifying parameters, `-r<n>` |
| Hold / Release | The annotation `adhar.io/batch-hold` set / removed on the `BatchJob`; the composition suspends the CronJob |

**Run names are the duplicate guard.** A manual run is `<job>-<hash8>`, where `hash8`
is the first 8 hex characters of the SHA-256 of the identifying parameters as
canonical JSON (naming `v1`, design §8.3). Starting the same identity twice finds the
existing run instead of running it again; Spring Batch's own instance identity is the
inner guard. The CLI, the Console and the kit compute the same name from the same
test vectors.

**The definition changes only through Git.** Kyverno enforces it:
`adhar-batch-hold-only` lets anyone but Argo CD and Crossplane change only the hold
annotation, and `adhar-batch-run-integrity` refuses a run that differs from the run
template in anything but its name, trigger label and the parameters/trigger env. A
prod operator can therefore run the deployed job, never a different image or extra
env.

**While Kyverno is down, manual runs are refused; scheduled runs are not.**
`adhar-batch-run-integrity` is `failurePolicy: Fail`, and its webhook skips the CronJob
and Volcano controllers by username, so scheduled runs never wait on Kyverno (Kyverno
v1.19 propagates that condition, verified live on 2026-11-14). Retry a manual run once
Kyverno is back. The platform's Kyverno no longer forces every policy to fail open
(design D18): the batch policies that guard run integrity (its native and Volcano
halves, `adhar-batch-run-integrity` and `adhar-batch-run-integrity-volcano`) and queue
binding fail closed, and every other policy in the stack, including the `ClusterPolicy`s
a `Policy` or `CompliancePolicy` composes, says `Ignore` itself, so nothing else
changed. A scheduled single run on Volcano does not wait either: the queue binding skips
the Job and CronJob controllers for the objects a Job or CronJob owns.
`BatchRunAdmittedUnchecked` still alerts on the API server's fail-open counter
(`core/adhar-batch/manifests/alerts.yaml`), now as a guard that a fail-open override has
not come back, and `BatchRunIntegrityUnenforced` warns while the admission controller
is gone.

## Exit codes

| Code | Meaning | Kubernetes does |
|---|---|---|
| 0 | Completed, or skipped (already complete, duplicate) | Job succeeds |
| 1 | The JVM or context failed before the run | retried (`backoffLimit`) |
| 2 | Invalid request: parameters, unknown job, business date unresolved, … | **fails the Job, no retry** |
| 3 | Failed, not retryable (skip limit, `NonRetryableBatchException`) | **fails the Job** |
| 4 | Stopped on request | **fails the Job** |
| 5 | Failed, retryable | retried (`backoffLimit`) |
| 6 / 7 | Abandoned / repository state unknown | **fails the Job** |
| 137 / 143 | Killed | an eviction or drain is not counted; otherwise retried |

The runner writes a one-line JSON **termination summary** (`"v":1`: job, instance and
execution ids, status, exit code, outcome and its `reason`, trigger, business date,
item counts, steps) to `/dev/termination-log` and stdout. That is what `adhar batch
runs` and the Console show; treat every field as optional.

## Delivery and verification

The overlays follow the Release model: dev is on demand (`schedule.suspend: true`),
test runs the real cron, prod adds `priority: critical`. Kargo pins the image per
environment. Verification passes on silence and fails on evidence: a run of this
application that **started during verification** and failed, or was OOM-killed, fails
the stage; no run is not a failure. A failed night does not mark the Argo CD
Application or the Kargo stage unhealthy — it fires an alert.

## Alerts and dashboards

Per `BatchJob` (`PrometheusRule <name>-batch`; severity `critical` in prod, `warning`
elsewhere): `BatchRunFailed`, `BatchScheduleMissed`, `BatchNoRecentSuccess`,
`BatchRunTooLong`, `BatchRunStuckPending`, `BatchRunOOMKilled`. An Application with
`type: batch` gets the `batch-jvm` dashboard; Grafana's **Delivery → Batch** shows the
fleet. Run pods push metrics over OTLP, so a run shorter than a scrape interval is
still measured (production profile).

## Capacity and priority

Batch never preempts a service: `adhar-batch` (−100) and `adhar-batch-critical` (0) are
both `preemptionPolicy: Never`, so a pending run waits for capacity rather than evicting
a service. (Runs on Volcano use `adhar-batch-volcano` and `adhar-batch-volcano-critical`
instead; see [Volcano](#volcano-queues-and-gangs-phase-2).) A run pod is `safe-to-evict: "false"` (unless `disruption: allow`), so the node
autoscaler does not drain under it, and a pending run does not buy a node unless the
job sets `scaleUp: true` (prod only). Memory request equals limit; each run is a JVM
start, so prefer cadences of 10 minutes or more.

### A batch node pool

A cluster can give batch a node group of its own, so a nightly backlog never squeezes
the platform's pods (design 0025 D14). Declare it per environment in `config.yaml`:

```yaml
environments:
  prod:
    batchPool:
      machineType: c6i.2xlarge   # empty = the provider's worker size
      minNodes: 1                # at least 1
      maxNodes: 4
    autoscaling:
      enabled: true              # the pool is autoscaled only when this is on
```

Pool nodes carry the label `adhar.io/node-pool: batch` and the taint
`adhar.io/batch=true:NoSchedule`. While one exists, `adhar-batch-node-pool` gives every
run pod the toleration and a *preferred* affinity for the pool, so existing jobs move
there with no change and still run on a general worker when the pool is full. The node
autoscaler grows the pool, never the general workers, for a waiting run that may buy
nodes (`scaleUp: true`, prod), never grows it for anything else, and retires idle pool
nodes down to `minNodes`. Its view is `status.autoscaling.batchPool` on the
`AdharPlatform`, and `adhar get status` prints it under *Node Autoscaling*, beside the
general workers. A run admitted while no pool node existed carries no toleration; that is
why the pool's floor is one node. A new pool node takes the size the pool's nodes already
run. On AWS, DigitalOcean and Civo compute clusters, a pool that has lost every node
takes `machineType` (recorded for the autoscaler in `adhar-cluster-spec`) rather than the
general workers' size.

`batchPool` is refused on `kind` and `custom` (they build no node groups) and on
`clusterMode: provided` (the nodes are not the platform's). On a provided cluster, label
and taint the nodes you want batch on yourself (`adhar.io/node-pool=batch`,
`adhar.io/batch=true:NoSchedule`): the policy places runs there all the same, but nothing
grows or shrinks those nodes.

## Storage: the root-credential risk (R5)

**A `Bucket`'s connection Secret holds the object store's ROOT credential.** Anything
that mounts it can read and write every bucket on the platform, not just its own.
Recorded as design 0025 R5 and accepted on 2026-11-13: the RustFS client can mint a
scoped user and policy, but swapping the credential under every existing `Bucket`
consumer needs a live bucket test first. Until per-bucket users land, **use a bucket
for one team only**. The decision is recorded in
[`compositions/bucket/local-rustfs.yaml`](../platform/controlplane/configuration/compositions/bucket/local-rustfs.yaml),
where `TestBucketRootCredentialRiskIsRecordedWhereItIsTaken` keeps it next to the code
that hands the credential out.

### A job's bucket (D26)

Name the `Bucket` instead of wiring its Secret yourself:

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: BatchJob
metadata: { name: importer, namespace: prod }
spec:
  parameters:
    job: importJob
    image: harbor-core.adhar-system.svc.cluster.local/library/importer:1.4.0
    database: { secretRef: importer-db-app }
    bucket: inbound          # a Bucket in this namespace
```

Every run pod (each role, scheduled or started by hand) then gets `ADHAR_S3_ENDPOINT`,
`ADHAR_S3_REGION` and `ADHAR_S3_BUCKET` from the Bucket's Secret `inbound-bucket`, and
`ADHAR_S3_ACCESS_KEY` / `ADHAR_S3_SECRET_KEY` from `importer-s3`, which the composition
projects from `root-creds` through the `platform-object-storage` store (it serves
environment namespaces only). `adhar-kit-batch` turns the five into Spring Cloud AWS's
settings, so the job's S3 readers and writers need no configuration; whatever the
application sets under `spring.cloud.aws.*` wins, and one that sets its own endpoint keeps
its own credential chain rather than the bucket's key. A Bucket that does
not exist, or publishes another Secret, suspends the schedule with
`PrerequisitesReady=False` (`BucketUnavailable`), and an `env` entry starting `ADHAR_S3_`
beside `bucket` is refused. **This is the same root credential (R5)**: the composition
records it beside the ExternalSecret, and
`TestBatchJobBucketRecordsR5WhereItTakesTheRootCredential` keeps it there. A run that
points a variable at another Secret is refused by `adhar-batch-run-integrity`
(`policy-tests/bucket-run-integrity`).

## Locally

Everything above works on `adhar up` (Kind) with Kafka, Kargo, Alloy and Volcano all
off: `core/adhar-batch` is enabled in the local profile, and the composition runs the
native path.

## Scheduler

`scheduler: auto` (the default) runs on Volcano when the cluster has it, and on plain
Kubernetes otherwise; `kubernetes` always runs on plain Kubernetes; `volcano` requires
Volcano and reports `PrerequisitesReady=False` (keeping the schedule suspended) where it
is missing, rather than running somewhere it was not asked to. "The cluster has it"
means the scheduler descriptor `ConfigMap adhar-system/adhar-batch-scheduler` exists
(design D7). Volcano is disabled in every profile today, so `auto` resolves to
`kubernetes` everywhere. Where Volcano runs, `auto` also needs your team's leaf queue for
the namespace's tier to exist and be open; without it the job stays on plain Kubernetes
and says why (`QueueFits=False`).

## Volcano: queues and gangs (Phase 2)

Volcano is the batch scheduler of [ADR-0028](adr/0028-batch-scheduler.md). Once the
platform team enables it (packages `volcano` and `adhar-batch-volcano`; the production
gate is Volcano v1.17 or a maintainer's sign-off on v1.16, with the Kubernetes 1.37
canary green), every `BatchJob` on `scheduler: auto` moves onto it at its next
reconcile, with no edit to the job.

**Your queues.** Each team gets three leaf queues under the root `adhar-batch`, one per
tier: `<organisation>-<team>-dev`, `-test` and `-prod`. A namespace runs on the leaf of
its tier (the label `adhar.io/tier`), so an environment named `staging` of tier `test`
uses the team's test leaf. Kyverno fills the queue in for you and refuses any other,
including another team's. `dev` and `test` have no guaranteed share and are
reclaimable; `prod` has a `deserved` share, so a prod run can take capacity back from
dev or test work that is over its share. That dev or test run stops at its next chunk
boundary and resumes from its last commit later. The team's `Team` object shows the
three names in `status.batchQueues` and their state in the `BatchQueuesReady`
condition.

```bash
adhar batch queues                         # the root queue and every leaf, with usage
adhar batch queues --team acme-payments    # your three leaves
```

The Console shows the same under **Develop → Batch → Queues**, a tab that appears only
on a cluster running Volcano.

**What changes for a run.**

| | Plain Kubernetes | Volcano |
|---|---|---|
| `mode: single` | a `Job` | the same `Job`, placed by Volcano in your leaf queue |
| `mode: partitioned` | an Indexed `Job`: index 0 is the manager, the rest are workers, no gang | a Volcano Job: one manager plus `workers`, started all together or not at all (a gang) |
| PriorityClass | `adhar-batch` / `adhar-batch-critical` | `adhar-batch-volcano` / `adhar-batch-volcano-critical` |
| Stop / Resume | `Job.spec.suspend` | a Volcano `AbortJob` / `ResumeJob` command |
| Restart | a new run, `-r<n>` | the same: a new run, `-r<n>` |

**Gangs wait whole.** A partitioned run on Volcano starts only when the manager and
every worker fit; no member starts before the others. While its queue is full it waits
as a PodGroup with no pods. Once the queue admits it, its pods exist and wait
`Pending` until all of them fit, and until then they count against your namespace's
quota. A dev gang never buys a node. A prod gang whose `BatchJob` sets
`scaleUp: true` may make the node autoscaler add workers, all it needs or none. `adhar
batch runs` and the Console's run drawer show the gang: queue, size, and how many
members are running.

**Batch still never evicts a service.** The Volcano classes allow preemption, but only
Volcano ever acts on that, and it chooses victims only among the batch runs it
schedules itself.

**When a queue does not fit.** The root queue's guaranteed room holds a fixed number of
prod shares, sized from the cluster. A `BatchJob` whose leaf is missing or was refused
(a team beyond that number, say) gets `QueueFits=False` with reason
`LeafQueueUnavailable`, and on `scheduler: auto` keeps running on plain Kubernetes until
it is fixed. A run larger than its leaf's whole capability in any dimension gets reason
`ExceedsCapability`: Volcano never admits it, so shrink it or raise the allowance. A
run larger than its leaf's share in every dimension gets reason
`ExceedsDeserved`: it starts only when the batch tree has idle room and can never take
capacity back from dev or test. Each message names the leaf, the numbers and the
remedy (a smaller run, a larger prod share from the platform team in
`globalSettings.batch`, or `scheduler: kubernetes`). The job is never denied silently.

**Volcano Job plugins.** Only `env` is supported. `ssh`, `svc`, `tensorflow`, `mpi`,
`pytorch` and `ray` are refused: the platform's Volcano runs without the cluster-wide
permissions they need, and batch pods talk through the JobRepository instead.

**If Volcano is removed,** the platform team deletes the descriptor first; every job on
`auto` returns to plain Kubernetes, partitioned jobs as Indexed Jobs without a gang.
See the [`core/volcano` README](../platform/stack/packages/core/volcano/README.md).

## Remote chunking (`mode: chunked`, Phase 4)

Partitioning splits the *input* between workers, and each worker reads its own share.
Some inputs cannot be split that way: one file, one stream, one query with no key to
range over. For those, `mode: chunked` keeps one reader, the manager, and spreads the
*processing and writing* over `workers` pods. The manager reads items, sends them as
chunks over a RabbitMQ queue, and the workers process and write each chunk and answer
on a second queue (design 0025 §5.11, decision D11).

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: Queue                      # one per direction, in the job's namespace
metadata: {name: importer-chunks}
# spec: as in examples/queue.yaml
---
apiVersion: platform.adhar.io/v1alpha1
kind: Queue
metadata: {name: importer-replies}
---
apiVersion: platform.adhar.io/v1alpha1
kind: BatchJob
metadata: {name: importer}
spec:
  parameters:
    mode: chunked
    workers: 2                   # besides the manager; default 2
    chunking:
      requestQueue: importer-chunks
      replyQueue: importer-replies
```

- **The shape is the partitioned one.** One manager plus `workers` pods: a gang on
  Volcano, an Indexed `Job` without it, with the same quota check at compose time.
  `gridSize` does not apply and is refused.
- **Every pod is told.** `ADHAR_BATCH_DISTRIBUTION=chunked` selects the kit's
  remote-chunking beans, and `ADHAR_BATCH_CHUNKING_TIMEOUT` bounds a chunk's round
  trip. Each `Queue`'s connection Secret reaches every pod with the prefixes
  `ADHAR_BATCH_CHUNKING_REQUESTS_` and `ADHAR_BATCH_CHUNKING_REPLIES_`, and the broker
  password arrives as `ADHAR_BATCH_CHUNKING_PASSWORD`. A chunked run never gets
  `ADHAR_BATCH_PARTITION_*`.
- **The broker credential is projected beside the job.** The broker's password lives
  in adhar-system (Secret `rabbitmq`), and a pod reads Secrets of its own namespace
  only. So the `BatchJob` composes an ExternalSecret `<job>-batch-broker` and every
  `Queue` composes `<queue>-broker`. Each one copies that single key through the
  `platform-messaging` ClusterSecretStore, which serves environment and preview
  namespaces and can read no other Secret. The run's `ADHAR_BATCH_CHUNKING_PASSWORD`
  and the Queue's own declare Job read those copies (design 0025 §18 R15, fixed
  2026-11-21).
- **The queues must exist.** A `Queue` that is missing makes the `BatchJob`
  `PrerequisitesReady=False` (reason `ChunkQueueUnavailable`, naming it) and keeps its
  schedule suspended. The two queues must be different: a worker would otherwise read
  its own replies as chunks.
- **Seeing it.** `adhar batch list` and `describe` show the mode, with the workers
  and the queues. The Console's job drawer reads "Remote chunking: 1 manager + N
  workers over RabbitMQ".
- **Restart is the kit's.** A chunk that was sent and never answered is reprocessed
  when the run restarts, from the manager's last commit. Writers must therefore be
  idempotent, as they already must be for a restart.
- **Not built yet on the kit side.** The platform renders all of the above today; the
  kit's remote-chunking beans (`adhar.batch.distribution=chunked`, KIT-4.1) come in a
  later kit change. Until they land, keep jobs on `single` or `partitioned`.
- **One team per broker credential.** A `Queue` hands its consumers the broker's
  `admin` credential, as a `Bucket` hands out RustFS's root (R5, design §18 R16), so
  treat remote chunking as single-team until per-queue users exist.
- **`envFrom` cannot reach the runner's variables.** A `prefix` that could produce an
  `ADHAR_BATCH_*` name (`ADHAR_BATCH_…`, or a shorter one such as `ADHAR_`), or
  `OPENLINEAGE_*` while lineage is on, is refused like the same name in `env`.

**Lineage (opt-in).** `spec.parameters.lineage.enabled: true` gives every run pod the
OpenLineage client's own variables, pointed at the platform's OpenMetadata by default:
`OPENLINEAGE_URL`, `OPENLINEAGE_ENDPOINT` (`/api/v1/openlineage/lineage`, OpenMetadata's
receiver, not the client's default), `OPENLINEAGE_NAMESPACE`
(`adhar/<organisation>/<team>[/<project>]/<environment>`) and, from
`lineage.credentialsSecretRef`, `OPENLINEAGE_API_KEY`. `lineage.url` points them at
another receiver. Lineage works in any mode and is advisory: a receiver that is down
never fails a run.

## Run history beyond 90 days (Phase 4)

The `adhar.batch` topic keeps 90 days. Past that, the **run-summary archive** keeps how
each run ended. When a run finishes (succeeded, failed, skipped, or lost because it was
deleted before it ended), the batch observer writes that run's final event to the
platform object store:

```text
s3://adhar-batch-archive/<namespace>/<batchJob>/<businessDate>/<run>.json
```

The object is the same CloudEvent the bus carried, with the same `id`. It holds the
outcome, exit code, counts, Spring Batch execution, business date, trigger, actor and
duration. Identifying parameters are stored as the bus carries them, with the ones
marked `sensitive` (and any whose name reads as a credential) masked, so nothing the
template hides is kept for years in the bucket. A run whose business date is not an ISO
date is filed under `undated`. A run
observed twice overwrites its own object, so the archive holds one answer per run: the
last one.

- **`adhar batch history <job>`** reads the bus and the archive together. An event both
  hold is shown once. Archived rows are marked `archive` in the `FROM` column, and with
  `-o json` they carry `"archived": true`. Without the bus, for example on a local
  cluster where Kafka is off, the history is the archive alone, and the command says so.
  The archive is read with the platform's object-store credential
  (`adhar-system/root-creds`) through a port-forward to RustFS. If your identity cannot
  read that Secret, you get the bus's history and one line saying the archive was not
  consulted. `--archive=false` skips it, and `--archive-url` reads it from a store you
  reach yourself.
- **The Console's History view** reads the archive for an open job, marks archived rows
  `archived`, and shows them only in namespaces where you may list `BatchJob`s. Choose
  **Last 12 months (archive)** to reach past the stream.
- **It is advisory.** Archiving never delays a run, an event or a reconcile. A store that
  is down or full is counted in `adhar_batch_archive_writes_total{result="failed"}` (and
  `"dropped"` if the queue overflowed), and `BatchArchiveWritesFailing` fires after 30
  minutes of that (critical for prod's runs). The run's archive entry is lost; the bus
  still has it for 90 days.
- **Only the end of each run.** Requests, starts, stops, holds, missed slots and
  deadlines stay on the bus only. The archive answers "how did each run end", not
  "what happened in between".
- **Retention is the bucket's.** Nothing expires by default. The platform team can add a
  lifecycle rule to `adhar-batch-archive` in RustFS.

## In-service jobs on Quartz

**Built in `adhar-kit-batch`** (design 0025 §5.20, decision D24; KIT-Q.1–Q.7). The
property reference is the module's README, *In-service scheduling on Quartz*.

Some jobs belong inside a long-running service rather than in a pod of their own:
they run every few seconds, follow business hours, or start when the service receives
something. The kit's embedded scheduler (`adhar.batch.scheduler.*`) gains a second
engine, `quartz`, for those.

| Use | When |
|---|---|
| A `BatchJob` (the default) | Every 10 minutes or less often; long, heavy or partitioned jobs; anything operators run, stop, restart or hold |
| `engine: simple` | A few fixed cron schedules inside a service |
| `engine: quartz` | Sub-minute or business-hours schedules, business calendars, runs started by the service's own events, or a run that must carry on on another replica when one dies |

```yaml
adhar:
  batch:
    scheduler:
      enabled: true
      engine: quartz
      time-zone: Asia/Singapore
    quartz:
      triggers:
        importJob:
          - name: nightly
            kind: cron
            expression: "0 30 1 ? * *"         # Quartz syntax, seconds first
            calendar: sg-business
          - name: office-hours
            kind: daily
            from: "09:00"
            to: "17:00"
            every: 5m
            calendar: sg-business
      calendars:
        sg-business:
          file: /etc/adhar/batch/calendars/sg-business.yaml   # the same calendar.yaml the platform uses
```

- **Exactly once across replicas.** Quartz keeps its triggers in your application's own
  Postgres, in the `batch` schema beside the JobRepository (tables `batch.QRTZ_*`,
  created by the kit). Every replica runs the scheduler; each slot fires once.
- **Same identity as the platform.** A slot's business date is its nominal time in the
  trigger's zone, exactly as for a CronJob run. A run is named
  `<job>-<hash8>` by the same rule, and a slot that already completed is skipped. A job
  polled every 5 minutes with a `businessDate` parameter therefore runs once per day,
  and a failed run is retried at the next fire.
- **Missed slots** follow the trigger's `misfire`: `latest` (the default) runs the newest
  missed slot once, `all` runs each missed slot in order (at most 24), and `skip` waits
  for the next one.
- **A replica dies mid-run:** another replica picks the run up and resumes it from its
  last commit.
- **From your own code:** `BatchTriggers.fireNow(job, params)` stores a one-shot trigger
  named after the run, so asking twice for the same parameters runs once, even after the
  first has finished. Its identity is only the parameters you pass: give a `businessDate`
  explicitly, and a job with no identifying parameter is refused, because it could only
  ever run once. Use `fireAt(job, instant, params)` for a run keyed on a time.
- **Interval triggers** keep to a fixed grid, `start` (default the Unix epoch) plus
  multiples of `every`, so after a missed slot the next fires land back on :00, :05, …
- **Calendars:** a trigger never fires on a day its calendar excludes, and must share
  the calendar's time zone or the service does not start. A mounted calendar file is
  re-read every 5 minutes; a broken edit keeps the previous version (and is counted).
- **Moving from `simple`:** your `adhar.batch.scheduler.jobs` cron schedules keep
  working, converted to Quartz and checked slot by slot at start. A schedule in the hour
  a clock change repeats (01:30 in New York or London) fails that check, because the two
  schedulers resolve that hour differently: write it as a Quartz `cron` trigger instead.
  Each JobDetail records the service's name, so two services sharing one
  `scheduler-name` on one database refuse to start rather than fire each other's jobs.
- **Seeing it:** the service writes its schedules to `ConfigMap <name>-batch-schedules`
  in its namespace. The Console shows them under **Develop → Batch → Jobs**, read-only,
  and `adhar batch schedules` lists them. The template's option **In-service batch jobs
  (Quartz)** on `springboot-web-service` sets all of this up, including the permission
  to write that ConfigMap and two alerts.
- An in-service run is not a Kubernetes object: it has no Run now, Stop, Restart or Hold
  in the Console. A job that needs those belongs in a `BatchJob`.

## What is not here yet

| Phase | Adds |
|---|---|
| 2 | Built and disabled: Volcano's queues, gangs, `mode: partitioned`, `adhar batch queues`, the Console's Queues tab. Waiting for the production gate, the canary (A2.1–A2.10), the kit's remote partitioning and the partitioned template |
| Q | Built (above): the kit engine, the `springboot-web-service` option (TPL-Q.1), the Console's read-only group and `adhar batch schedules`. Remaining: AQ.8 on a cluster |
| 3 | Events on `adhar.batch`, calendars, `dependsOn`, backfill, SLA deadlines, DLQ replay, retention, smoke-run gate |
| 4 | Platform side built: `mode: chunked`, lineage variables, batch node pools, the run-summary archive. Still to come: the kit's remote-chunking beans, Iceberg/Parquet I/O and the OpenLineage client. Deferred by decision, each waiting for a named trigger (design 0025 §15, Phase 4): Volcano `NamespaceQueue`/fair share, the Workload API and Kueue, JobFlow, Karmada placement |

The `BatchJob` XRD accepts a field only in the phase whose composition renders it, so
an older control plane rejects a definition it cannot honour instead of ignoring part
of it.

## Troubleshooting

- **The XR is `Synced=False`** — read its message; it is one of the refusals above.
- **`PrerequisitesReady=False`, reason `PriorityClassMissing`** — `core/adhar-batch`
  is not installed on this cluster; the schedule stays suspended until it is.
- **A run is denied at admission** — `adhar-batch-run-integrity` names the field that
  differs from the run template. Create runs with `adhar batch run` or the Console,
  not by editing a Job.
- **Exit 2 on a scheduled run** — the runner could not resolve the scheduled time. A
  project that adds egress policies must allow the API server.
- **`QueueFits=False`** — the run's leaf queue is missing or refused
  (`LeafQueueUnavailable`), the run is larger than its leaf's capability
  (`ExceedsCapability`), or a prod run is larger than its leaf's share
  (`ExceedsDeserved`); the message says which and what to change.
- **A partitioned `BatchJob` refused at compose time** — its shape (one manager plus N
  workers) asks for more than a key of the namespace's `ResourceQuota` (`requests.cpu`,
  `limits.memory`, `pods`, …); the message names every key that is over. A
  `cpuLimit` below a CPU request is refused the same way.
- **`BatchQueuesReady=False` on the `Team`** — `QueueRejected` means the root queue had no
  room for another prod share; `QueueNameTaken` means another team's
  `<organisation>-<team>` spells the same name.
- **A partitioned run waits with no pods** — on Volcano that is a gang that does not fit
  yet. After 30 minutes `BatchGangQueueRejected` fires while its queue has not admitted
  it, and `BatchGangPending` while it is admitted but the cluster has no room for all
  of it.
- **Upgrading** — the XRD arrives with a controller release, the package with a stack
  push; release the controller first.
