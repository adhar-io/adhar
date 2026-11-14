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
service on Quartz are designed and not built yet (see
[In-service jobs on Quartz](#in-service-jobs-on-quartz-planned)). Calendars,
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

## In-service jobs on Quartz (planned)

**Designed (design 0025 §5.20, decision D24), not built yet**: the kit work is
KIT-Q.1–Q.7. This section is the contract it will meet.

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
| Q | In-service jobs on Quartz (above) |
| 3 | Events on `adhar.batch`, calendars, `dependsOn`, backfill, SLA deadlines, DLQ replay, retention, smoke-run gate |
| 4 | Remote chunking, batch node pools, lakehouse I/O, lineage, Karmada placement |

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
