# `batchjob-kubernetes` — a `BatchJob` on Kubernetes, natively or on Volcano

The default composition for `BatchJob` (design
[0025](../../../../../docs/design/0025-batch-platform.md) §6.4 and §4; user guide
[docs/BATCH.md](../../../../../docs/BATCH.md)). Pipeline mode: `function-go-templating`
renders, `function-auto-ready` reads readiness. Everything it composes lands in the
XR's own namespace, composed directly — no provider-kubernetes, because nothing here
leaves the namespace. The native path is the permanent fallback; the Volcano path is
taken only where Volcano runs (see *Scheduler*).

## What one `BatchJob` becomes

| Object | Name | Composed when | Ready |
|---|---|---|---|
| ServiceAccount | `<name>-batch` | `serviceAccountName` is unset | once observed |
| Role + RoleBinding | `<name>-batch` | always | once observed (the Role also waits for the prerequisites) |
| ConfigMap, the **run template** | `<name>-run` | always | once observed |
| CronJob (batch/v1), resource `schedule-native` | `<name>` | `schedule.cron` is set, and the run is not a gang | once observed |
| CronJob (batch.volcano.sh/v1alpha1), resource `schedule-volcano` | `<name>` | `schedule.cron` is set, and the run is a gang (`mode: partitioned` on Volcano) | once observed |
| PrometheusRule | `<name>-batch` | `alerts.enabled` (default) | once observed |

Statusless kinds publish no `Ready` condition and `function-auto-ready` would hold
the XR unready forever because of one, so each carries
`gotemplating.fn.crossplane.io/ready: "True"` once its UID has been observed: the
XR is Ready on the second reconcile, never the first.

The run is one of three shapes, written as `kind` in the run template:

| `kind` | When | The run object |
|---|---|---|
| `Job` | `mode: single` | a batch/v1 Job. On Volcano only its pod changes: `schedulerName: volcano`, the annotation `scheduling.volcano.sh/queue-name: <leaf>`, and the class `adhar-batch-volcano` / `-critical`. The CronJob stays batch/v1. |
| `IndexedJob` | `mode: partitioned`, natively | a batch/v1 Job with `completionMode: Indexed`, `completions` = `parallelism` = workers + 1, `successPolicy` `succeededIndexes: "0"` (the manager finishing ends the run and the Job controller ends the workers), the same `podFailurePolicy`; `ADHAR_BATCH_ROLE=indexed` and `JOB_COMPLETION_INDEX` from the downward API, so index 0 is the manager. One pod template serves every index, so it takes the larger of the manager's and the workers' resources per axis. |
| `VolcanoJob` | `mode: partitioned` on Volcano | a batch.volcano.sh/v1alpha1 Job (§4.5): `queue` the leaf, `minAvailable` workers + 1 (all or nothing), `maxRetry`, `ttlSecondsAfterFinished`, `plugins: {env: []}`, TerminateJob on exit codes 2, 3, 4, 6, 7 then RestartJob on `PodEvicted`/`PodFailed`; tasks `manager` (1, CompleteJob on its TaskCompleted) and `worker` (N), each pod with `schedulerName: volcano`, `restartPolicy: Never`, its own `activeDeadlineSeconds` (a Volcano Job has none of its own) and `ADHAR_BATCH_ROLE=manager|worker`. The Job's labels (workload, environment, batch-job, scale-up) are the ones Volcano copies onto its PodGroup, which the node autoscaler reads. |

The run template and the CronJob's `jobTemplate` are the SAME object built by one
loop, differing only in `adhar.io/batch-trigger` and `ADHAR_BATCH_TRIGGER`, so a run
a person starts cannot drift from a run the clock starts. The Kyverno policy
`adhar-batch-run-integrity` (package `core/adhar-batch`) refuses a run that differs
from the template in anything but the fields §6.5 lets a client set; the defaults
the API server adds to a Job on its way in (`fieldRef.apiVersion`,
`onPodConditions[].status`, `schedulerName`) are therefore written out here too.

## Extra resources it reads

| Resource | Why |
|---|---|
| The XR's Namespace | `adhar.io/organisation|team|project|environment` decide where the job belongs. A BatchJob outside an environment namespace, or in `adhar-system`, is refused. |
| PriorityClass `adhar-batch` or `adhar-batch-critical` | Prerequisite of the native path: absent means `core/adhar-batch` is not installed → `PrerequisitesReady=False`, schedule suspended. |
| PriorityClass `adhar-batch-volcano` or `adhar-batch-volcano-critical` | Prerequisite of the Volcano path (DEC-1): absent, `auto` runs natively. |
| ConfigMap `adhar-system/adhar-batch-scheduler` | The scheduler descriptor (D7): `scheduler`, `gang`. See *Scheduler*. |
| CRDs `jobs.batch.volcano.sh`, `cronjobs.batch.volcano.sh`, `queues.scheduling.volcano.sh` | A descriptor without them is not Volcano: composing, or requiring, a kind the cluster does not serve fails the whole reconcile. |
| Queue `<organisation>-<team>-<tier>` | Asked for only where Volcano runs and the namespace has a tier: the leaf the run is bound to, its owner labels, its state and its `deserved` share (QueueFits). |
| ResourceQuota `<namespace>-quota` (`mode: partitioned` only) | The gang-fit refusal: every key of `spec.hard` it sets among `requests.cpu`, `limits.cpu`, `requests.memory`, `limits.memory`, `cpu`, `memory` and `pods`. |

## Every parameter and what it becomes

Defaults are the XRD's. "Run" means both the CronJob's `jobTemplate` and
`template.yaml` in the run template.

| Parameter | Becomes |
|---|---|
| `displayName` | Nothing on the cluster; the Console and `adhar batch list` show it. |
| `application` | Label `adhar.io/application` on every object and run pod (default: the XR's name), `OTEL_RESOURCE_ATTRIBUTES` `adhar.application`, alert label `adhar_application`. |
| `job` | `ADHAR_BATCH_JOB` on the run container. Required. |
| `image` | The run container's image. Kargo pins it per environment. Required. |
| `imagePullSecrets` | `spec.template.spec.imagePullSecrets` of the run (default `[harbor-dockercfg]`; `[]` names none). |
| `mode` | The run's shape: `single` (a Job) or `partitioned` (an Indexed Job natively, a Volcano gang on Volcano). |
| `workers` | `partitioned` only: the workers besides the manager — `completions`/`parallelism` − 1 of the Indexed Job, the `worker` task's `replicas` and `minAvailable` − 1 of the gang. Counted in the quota check. |
| `gridSize` | `ADHAR_BATCH_PARTITION_GRIDSIZE` (`adhar.batch.partition.grid-size`) on every pod of a partitioned run; default 4 × `workers`. With `ADHAR_BATCH_PARTITION_TRANSPORT=jdbc` and `ADHAR_BATCH_PARTITION_TIMEOUT` (the deadline less five minutes, or half the deadline when that is shorter). |
| `scheduler` | `status.scheduler` (and `status.queue` on Volcano). `kubernetes` is native; `auto` is Volcano where the descriptor names it, its CRDs are served, the leaf queue is the team's and open and the Volcano class exists (and, for a gang, the descriptor does not say `gang: "false"`), native otherwise; `volcano` without all of that is `PrerequisitesReady=False` (reason `SchedulerUnavailable`, `QueueUnavailable` or `PriorityClassMissing`), schedule suspended. |
| `schedule` | A CronJob exists at all. Absent: on-demand only, `ScheduleReady` reason `OnDemand`. |
| `schedule.cron` | `CronJob.spec.schedule`. Firing more often than every 10 minutes is accepted and reported on `Placement` (a JVM start per run). |
| `schedule.timeZone` | `CronJob.spec.timeZone` and `ADHAR_BATCH_TIME_ZONE`. |
| `schedule.startingDeadlineSeconds` | `CronJob.spec.startingDeadlineSeconds` (the misfire window). |
| `schedule.concurrencyPolicy` | `CronJob.spec.concurrencyPolicy`. `Replace` is refused. |
| `schedule.suspend` | `CronJob.spec.suspend` (OR'd with the hold and with missing prerequisites); `ScheduleReady` reason `SuspendedInGit`. |
| `parameters` | `parameters.json` in the run template, every default written out so a client needs no schema. `source: schedule` and `identifying` are what the runner and the run name (§8.3, `naming: v1`) read. |
| `resources` | The run container's `resources`. |
| `resources.cpu` | `requests.cpu`. |
| `resources.memory` | `requests.memory` AND `limits.memory`: a run overcommitted on memory is the one the kernel kills. |
| `resources.cpuLimit` | `limits.cpu` on every pod. Unset, `limits.cpu` is the pod's CPU request: an environment's LimitRange defaults a missing CPU limit to 500m and its ResourceQuota counts `limits.cpu`, so a pod with no limit and a 1-CPU request is rejected when the Job controller creates it. A `cpuLimit` below a CPU request is refused. |
| `resources.workerCpu` | A partitioned run's worker `requests.cpu`; default `resources.cpu`, which then sizes the manager alone. |
| `resources.workerMemory` | A partitioned run's worker `requests.memory` and `limits.memory`; default `resources.memory`. |
| `resources.tmpSize` | `sizeLimit` of the `tmp` emptyDir mounted at `/tmp`. |
| `jvm` | What the Paketo memory calculator reserves before any heap. |
| `jvm.threads` | `BPL_JVM_THREAD_COUNT`. |
| `jvm.codeCache` | `-XX:ReservedCodeCacheSize=<codeCache>` in `JAVA_TOOL_OPTIONS`. |
| `jvm.options` | Appended to `JAVA_TOOL_OPTIONS`. |
| `retry` | How Kubernetes retries a run. |
| `retry.maxRetry` | The gang's `maxRetry`: how often Volcano restarts the whole gang (RestartJob) before the run is Failed. |
| `retry.backoffLimit` | `Job.spec.backoffLimit` (the native shapes). Counts only exit codes the failure policy does not fail outright (1, 5, a kill); 2, 3, 4, 6 and 7 are `FailJob` (§5.7). |
| `timeouts` | The two clocks of a run; grace must be shorter than the deadline (refused otherwise). |
| `timeouts.activeDeadlineSeconds` | `Job.spec.activeDeadlineSeconds`, the hard stop; also the default of `sla.maxDuration`. |
| `timeouts.terminationGracePeriodSeconds` | `terminationGracePeriodSeconds` of the run pod; `ADHAR_BATCH_SHUTDOWN_TIMEOUT` is 30 s less (never under 10 s), so the runner records STOPPED before the kubelet kills it. |
| `history` | What stays behind after a run. |
| `history.successfulJobsHistoryLimit` | `CronJob.spec.successfulJobsHistoryLimit`. |
| `history.failedJobsHistoryLimit` | `CronJob.spec.failedJobsHistoryLimit`. |
| `history.ttlSecondsAfterFinished` | `Job.spec.ttlSecondsAfterFinished` on every run, however it started. |
| `priority` | `priorityClassName`: `standard` → `adhar-batch`, `critical` → `adhar-batch-critical`, neither of which preempts anything; on Volcano `adhar-batch-volcano` / `adhar-batch-volcano-critical`, with which Volcano reclaims and preempts among batch only (DEC-1; kube-scheduler never schedules those pods). `critical` outside a `prod` namespace is refused. |
| `disruption` | `avoid` (default) annotates the run pod `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, which the node autoscaler honours; `allow` leaves the annotation off. |
| `scaleUp` | Label `adhar.io/scale-up: "true"` on the run pod, which lets a Pending run buy a node. Outside `prod` it is refused. |
| `database` | Where Spring Batch keeps its JobRepository. Required. |
| `database.secretRef` | `SPRING_DATASOURCE_URL|USERNAME|PASSWORD` from that Secret's `jdbc-uri`, `username`, `password`. |
| `database.schema` | `ADHAR_BATCH_REPOSITORY_SCHEMA`. |
| `env` | Extra env on the run container. The caller wins over the platform's defaults, except `OTEL_RESOURCE_ATTRIBUTES`, which is merged (the platform's attributes come last). Names the runner owns (`ADHAR_BATCH_*`, `POD_*`, `NODE_NAME`, `ADHAR_ENVIRONMENT`, the datasource) are refused. |
| `envFrom` | The run container's `envFrom`, verbatim (a `Bucket` Secret, for example). |
| `serviceAccountName` | The run pod's ServiceAccount instead of the composed `<name>-batch`, which is then not composed. The RoleBinding still grants it the runner's Role. |
| `sla` | Alert thresholds, not a hard stop. |
| `sla.maxDuration` | Threshold of `BatchRunTooLong`, in seconds; defaults to `timeouts.activeDeadlineSeconds`. |
| `alerts` | The PrometheusRule. |
| `alerts.enabled` | `false` composes no PrometheusRule. |
| `alerts.staleAfter` | Threshold of `BatchNoRecentSuccess`. |
| `alerts.severity` | Label `severity` on every alert; `auto` is `critical` in `prod`, `warning` elsewhere. |
| `organisation` | Never needed: the namespace's label decides. A value that disagrees is ignored and reported on `Placement` (reason `ParameterOverridden`). |
| `team` | As `organisation`. |
| `project` | As `organisation`. |
| `environment` | As `organisation`. |

Always set, with no parameter: the labels `adhar.io/workload: batch`,
`adhar.io/batch-job: <name>`, `adhar.io/batch-role: single`, `adhar.io/component: app`
(Alloy derives the log stream's component from it) and `adhar.io/plane: workload`;
`restartPolicy: Never`, `enableServiceLinks: false`, `runAsNonRoot`, `RuntimeDefault`
seccomp, all capabilities dropped; `parallelism: 1`, `completions: 1`,
`podReplacementPolicy: Failed` and the `podFailurePolicy` of §5.7 (a
`DisruptionTarget` is ignored, exit codes 2, 3, 4, 6 and 7 fail the Job); the env
`ADHAR_BATCH_NAME`, `ADHAR_BATCH_ROLE`, `ADHAR_BATCH_TRIGGER`, `ADHAR_BATCH_PARAMS`
(`"{}"` until a client sets it), `ADHAR_BATCH_K8S_JOB` and `ADHAR_BATCH_ACTOR` (field
refs), `POD_NAME`, `POD_UID`, `NODE_NAME`, `ADHAR_ENVIRONMENT`, and OTLP to Alloy
(`OTEL_EXPORTER_OTLP_ENDPOINT`, `http/protobuf`, `OTEL_METRICS_EXPORTER=otlp`).

## Refusals

Each is `fail` with a readable message on `Synced=False`, instead of a definition
that is accepted and breaks at run time in the middle of a night:

- the name is not a DNS-1035 label of at most 40 characters (CronJob names are at
  most 52 and every run name adds a suffix);
- the namespace is `adhar-system` or carries no `adhar.io/environment` label;
- `priority: critical` or `scaleUp: true` outside `prod`;
- `schedule.concurrencyPolicy: Replace`;
- `timeouts.terminationGracePeriodSeconds` not shorter than `activeDeadlineSeconds`;
- `job`, `image` or `database.secretRef` missing;
- an `env` name the runner owns (including `JOB_COMPLETION_INDEX`);
- `resources.cpuLimit` below a CPU request (`cpu`, or `workerCpu` when partitioned):
  the API server rejects such a pod, and only once the Job controller creates it;
- `mode: partitioned` whose pods together need more than the namespace's
  ResourceQuota `<namespace>-quota` allows on ANY key it sets (`requests.cpu`,
  `limits.cpu`, `requests.memory`, `limits.memory`, bare `cpu`/`memory`, `pods`): a
  gang that could never start (its leaf admits it, its pods are created and the last
  are forbidden, holding the rest of the quota), or an Indexed Job that would start
  only some workers.

`TestBatchJobRefusals` pins every message.

## Conditions and status

| Condition | Meaning |
|---|---|
| `PrerequisitesReady` | `Met`, or `PriorityClassMissing` / `SchedulerUnavailable` / `QueueUnavailable` with the schedule suspended. Argo CD reports `False` as Degraded. |
| `QueueFits` | DEC-3, a warning (Argo CD does not read it): `NativeScheduler`; `Fits` (the leaf); `LeafQueueUnavailable` — the leaf is missing (Volcano refused a prod leaf the batch ceiling cannot admit), another team's, closed, or the namespace has no tier, so `auto` runs natively instead of being denied; `ExceedsCapability` — a run larger than the leaf's `capability` in any dimension, which Volcano never admits (its PodGroup stays `Pending` and `BatchGangQueueRejected` fires); checked first, on every tier; `ExceedsDeserved` — a prod run larger than the leaf's `deserved` in every dimension it declares, which starts only on idle room and can never reclaim. Also appended to `status.message`. |
| `SchedulerSwitch` | `Settled`, or `WaitingForRuns` while a schedule's kind switch waits for the run in flight (see *Scheduler*). |
| `TemplateReady` | `Published` once `<name>-run` is observed. |
| `ScheduleReady` | `OnDemand`, `Scheduled`, `Held`, `SuspendedInGit`, `Creating`, `SwitchPending` or `PrerequisitesMissing`. |
| `Held` | `True`/`OnHold` while the XR carries `adhar.io/batch-hold`. |
| `Placement` | Where the job runs and why; `ParameterOverridden` when a placement parameter disagreed with the namespace; carries the cadence note. |

`status.scheduler`, `queue` (on Volcano), `runTemplate`, `cronJob`, `lastScheduleTime`,
`lastSuccessfulTime` and `activeRuns` are copied from the resolved values and the
observed CronJob; `status.held.{reason,since}` from the annotation; `status.message`
is the one-line summary the CLI prints.

## The hold

Operators set `adhar.io/batch-hold: "<reason>"` on the BatchJob (`adhar batch hold`,
the Console, or `kubectl annotate`); the composition sets `CronJob.spec.suspend`. Argo
CD does not revert it, because the annotation is not in Git and Argo CD compares only
the fields it applies under server-side apply. Kyverno (`adhar-batch-hold-only`) lets
anyone but Argo CD and Crossplane change ONLY that annotation.

## Scheduler

`kubernetes` is native. `auto` runs on Volcano when ALL of these hold, and natively
otherwise — so removing Volcano (its descriptor) leaves every BatchJob running with no
edit, a partitioned job degrading to the Indexed Job (A2.10):

1. the descriptor `ConfigMap adhar-system/adhar-batch-scheduler` exists and names
   `scheduler: volcano` (D7);
2. Volcano's Job, CronJob and Queue CRDs are served;
3. the namespace's leaf `<adhar.io/organisation>-<adhar.io/team>-<adhar.io/tier>` exists,
   is labelled as this team's and is not Closed/Closing — the same leaf
   `adhar-batch-queue-binding` computes, so a run is never composed onto a queue Kyverno
   would deny (DEC-8: the tier, not the environment's name);
4. the class `adhar-batch-volcano(-critical)` exists;
5. for a gang, the descriptor does not say `gang: "false"`.

`volcano` insists: if any fails, `PrerequisitesReady=False` names it and the schedule is
suspended. When the queue is the reason, `QueueFits=False` says so either way (DEC-3).

**Switching kind.** In single mode a switch only changes the pod template. In
partitioned mode it changes the schedule's KIND (`schedule-native` ↔ `schedule-volcano`),
and Crossplane deleting a CronJob cascades to its runs. So while the old CronJob reports
a run in flight (`status.active`) the switch waits: the old object is composed as
observed with `suspend: true`, the new one is not composed, `SchedulerSwitch=False`
(`WaitingForRuns`) and `ScheduleReady=False` (`SwitchPending`) name the run. Once it has
finished the switch happens. Holding the job first (`adhar batch hold`) keeps the clock
from starting another run in between — hold → switch → release (§6.4). A Volcano
CronJob is kept only while its CRD is served.

**Alerts on the gang path.** A Volcano Job is invisible to kube-state-metrics' Job
series, so a gang's `BatchRunFailed`, `BatchNoRecentSuccess` (composed only while the
schedule is not suspended — Volcano publishes no suspension series) and the per-job
P2 alerts `BatchGangPending` (PodGroup `Inqueue` for 30 m: admitted, no room) and
`BatchGangQueueRejected` (PodGroup `Pending` for 30 m: the queue has not admitted it)
read the `kube_volcano_*` custom-resource-state series of `observability/kube-prometheus`
(`custom-resource-state.yaml`). `BatchRunTooLong` reads the pods; `BatchRunStuckPending`
and `BatchRunOOMKilled` are pod-based on every path. There is no `BatchScheduleMissed`
for a Volcano CronJob, which publishes no next-schedule time.

**The kit's side of the contract** (KIT-2.x): `ADHAR_BATCH_ROLE` is `single`, `indexed`,
`manager` or `worker`; a gang pod's `ADHAR_BATCH_K8S_JOB` is its `volcano.sh/job-name`
label (a Volcano Job, whose owner lookup is `batch.volcano.sh/v1alpha1`); the partition
channel is `ADHAR_BATCH_PARTITION_{TRANSPORT,GRIDSIZE,TIMEOUT}`. There is deliberately
no `ADHAR_BATCH_MODE`: Spring would bind it to `adhar.batch.mode`
(`auto|kubernetes|embedded`).

## Tests

`batchjob_composition_test.go`: the refusals, the ready annotations, the hold, the
alerts, template ↔ CronJob equality, the exit-code failure policy, and a real
`crossplane render` of the fixtures in `testdata/batchjob/`.
`volcano_composition_test.go`: the scheduler resolution, each shape, the switch, QueueFits,
the quota check, the gang alerts against the stack's series, Crossplane's grants, a golden
render per branch (`testdata/batchjob-volcano/golden-*.yaml`; `ADHAR_UPDATE_GOLDEN=1`
rewrites them) and a real `crossplane render` of every branch.
`TestBatchJobCompositionReadmeMapsEveryParameter` keeps this file and the XRD in step
both ways.
