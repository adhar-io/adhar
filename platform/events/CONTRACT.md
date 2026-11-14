# The Adhar platform event bus

Every notable thing the platform does is published once, as a
[CloudEvent 1.0](https://github.com/cloudevents/spec), onto Kafka.

This file is the contract. It is what a consumer in any language can be written
against, and what a new producer must satisfy.

## Why it exists

`kubectl get events` is namespaced, lives about an hour, and says nothing about
a node the cloud refused to create. `adhar get status` is a snapshot. Neither
answers *what happened here, in order* — and nothing could **react** to a change
without polling for it. The bus is that record and that trigger.

## Reaching it

Events go in over HTTP, through the Strimzi `KafkaBridge/adhar-events` that the
`kafka-operator` package declares:

```
POST http://adhar-events-bridge-service.adhar-system.svc.cluster.local:8080/topics/<topic>
Content-Type: application/vnd.kafka.json.v2+json

{"records":[{"key":"<subject>","value":{ …the CloudEvent… }}]}
```

A bridge rather than a Kafka client, because the platform is not one language:
the controllers are Go, the Console is Deno, the agent runtime is Python, and the
paved road is shell inside Tekton. Every one of them can POST JSON; none of them
should have to carry broker credentials.

The CloudEvent is the record **value** — the *structured* content mode of the
CloudEvents Kafka binding — so a consumer reads one JSON document and needs no
header support. The record **key** is the event's `subject`, so every event about
one object lands on one partition and is therefore ordered with respect to the
others about it.

Read it with `adhar events tail` / `adhar events list`, which go through the same
bridge via the API server's service proxy (no port-forward, no second credential).

## The envelope

| Attribute | Notes |
|---|---|
| `specversion` | always `1.0` |
| `id` | random; `(source, id)` is what makes an event unique |
| `source` | `/adhar/<host>/<component>` — identifies the emitter, so two platforms in one aggregated stream cannot collide |
| `type` | `io.adhar.<domain>.<object>.<verb>`, verb in the **past tense** |
| `subject` | the object, e.g. `prod/argocd`, `adhar/adhar-worker-3` |
| `time` | RFC 3339 |
| `datacontenttype` | `application/json` whenever `data` is present |
| `data` | domain payload |

Adhar extensions, lower-case alphanumeric because the spec restricts extension
names to exactly that (`adhar-team` would be an invalid attribute name and a
validating consumer would reject the whole event):

`adharorganisation`, `adharteam`, `adharproject`, `adharenvironment`,
`adharplane`, `adharcorrelationid`.

`adharcorrelationid` is what makes the stream answerable: every event from one
reconcile, request or agent run carries the same one, so "what happened during
the reconcile that failed at 10:04" is a filter rather than a guess from
timestamps across five components.

## Topics

One per **domain**, not one per type: a consumer interested in cluster shape
wants every node event without enumerating them, and partitions are the unit of
ordering — splitting a domain across topics throws away the order between
`node.added` and `node.removed`.

| Topic | Carries | Retention |
|---|---|---|
| `adhar.platform` | platform and component lifecycle | 14 days |
| `adhar.cluster` | nodes, scaling, data planes | 14 days |
| `adhar.tenancy` | organisations, teams, projects | 90 days |
| `adhar.delivery` | GitOps, packages, pipelines | 14 days |
| `adhar.ai` | agent runs | 3 days |
| `adhar.batch` | the runs of BatchJobs: requested, started, ended, lost; holds, missed slots, deadlines | 90 days |

Retention is declared per topic rather than inherited. The cluster has
`auto.create.topics.enable: true`, so publishing to an undeclared topic
**succeeds** and the broker creates it with one partition and its own default
retention — no error, just a stream kept for the wrong length of time.
`TestTheEventTopicsAreDeclaredInTheStack` holds the Go taxonomy and the
`KafkaTopic` objects together.

## The `adhar.batch` domain

Published by the **batch observer** (`platform/controllers/batch`, design 0025
§12), which reads the run objects (Jobs and Volcano Jobs labelled
`adhar.io/workload=batch`), their pods' termination summaries and the
`BatchJob`s. The kit never publishes to the bus itself: a tenant pod holds no
write path to the bridge and cannot forge an event, and a run that dies without
a word (OOM, SIGKILL, a lost node) still produces one.

`subject` is `<namespace>/<batchjob>` — the record key, so one job's events stay
in order on one partition. `adharcorrelationid` is the **run's UID** (the
BatchJob's UID for a hold or a missed slot with no run). The hierarchy
extensions come from the **namespace's** labels, never from the run.

| Type | When |
|---|---|
| `io.adhar.batch.run.requested` | A run appeared with a trigger other than `schedule` (manual, restart, backfill, dependency, workflow, ops, smoke); `data.run.actor` is who |
| `io.adhar.batch.run.started` | The run's first attempt is Running (published before its end even when nobody saw it run) |
| `io.adhar.batch.run.succeeded` | Job Complete / Volcano Completed, with the summary's counts |
| `io.adhar.batch.run.failed` | Job Failed / Volcano Failed or Terminated; `data.outcome` has the exit code and the reason (`BackoffLimitExceeded`, `DeadlineExceeded`, the kit's reason) |
| `io.adhar.batch.run.skipped` | Exit 0 with the kit's `outcome: skipped` (`already-complete`, `duplicate`, `calendar`) — **instead of** succeeded |
| `io.adhar.batch.run.stopped` / `run.resumed` | Each suspend / unsuspend (Volcano: Aborted / back to Pending or Running) |
| `io.adhar.batch.run.lost` | One **attempt** ended with no summary — `data.pod` and `data.outcome` (`OOMKilled` + 137, `NodeLost`, `PodVanished`) — or the run was deleted before it ended (`Vanished`), or it was deleted while no observer was running, so how it ended is not known (`Unobserved`; it satisfies no `dependsOn`). Per attempt, so an OOM kill followed by a successful retry is `lost` then `succeeded` |
| `io.adhar.batch.schedule.held` / `schedule.released` | The `adhar.io/batch-hold` annotation appeared / went |
| `io.adhar.batch.schedule.missed` | A cron slot passed ten minutes ago (`BatchScheduleMissed`'s grace) with no run |
| `io.adhar.batch.deadline.endangered` | The expected end (start, or the slot, + p90 of the last 20 durations) passes `sla.finishBy` — **before** the deadline |
| `io.adhar.batch.deadline.missed` | `sla.finishBy` passed without a success |

`data` (every field optional for a consumer): `batchJob`, `namespace`,
`application`; `run{name,kind,uid,trigger,actor,scheduler,queue}`;
`springBatch{job,instanceId,executionId,status,businessDate,identifying}` with
`sensitive` parameters masked as `••••••`; `counts{read,written,skipped,deadLettered}`;
`start`, `end`, `durationSeconds`; `outcome{reason,exitCode,message}`; and, by
type, `pod`, `schedule{cron,timeZone,slot,holdReason,heldSince}`,
`deadline{finishBy,timeZone,deadline,slot,expectedEnd,p90Seconds,slackSeconds}`,
`dependency{businessDate,after}` (on the `run.requested` of a run `dependsOn` started).

**Exactly once per transition, across restarts.** What has been said about each
job is kept in `ConfigMap <batchjob>-observer` beside it (owned by the
BatchJob), written **before** the events are handed to the publisher: a crash
between the two loses an event, which this contract allows, and never sends one
twice. The first look at a job (no record yet) records its history without
publishing it. Nothing is written on the run objects.

**`dependsOn` does not use the bus.** It is decided from Kubernetes objects and
that record, so it works where Kafka is off (the local profile) and through a
bus outage. A dependency run a full ResourceQuota refuses is kept in the record
of the job whose success called for it and retried (30 s doubling to 10 min, for
up to 26 h) until it starts; a refusal that cannot pass by itself (run
integrity, a LimitRange) is a Warning Event on the dependent BatchJob.

**The SLA is also two gauges**, served by the manager and read by the Mimir
ruler (`observability/mimir/manifests/batch-app-alerts.yaml`):
`adhar_batch_deadline_seconds_remaining` (finishBy less now, per business date
not yet succeeded; negative once passed) and
`adhar_batch_deadline_projected_slack_seconds` (finishBy less the expected end,
for the deadline being forecast). Both are labelled `k8s_namespace_name`,
`adhar_batch_job`, `adhar_application`, `adhar_environment`, `adhar_team` and
`business_date`, the hierarchy again from the namespace.

## The rule that matters most

**Publishing is advisory and must never change what the platform does.** An emit
cannot fail a reconcile, cannot block it, and cannot slow it down. The publisher
hands the event to a bounded queue and returns; `Publish` has no error return,
deliberately. If the bridge is down, the queue is full, or no bus is configured,
the event is dropped and counted and the caller never learns the difference.

A platform that will not converge because its audit log is unavailable is worse
than a platform with no audit log.

Three consequences worth knowing:

- The queue **drops the oldest** event when full. Newer events describe the
  current state, which is what a consumer catching up wants.
- There is **one worker**, so events from one component keep their order.
- Events are emitted on **transitions only**. The reconcile runs every 30
  seconds; republishing the condition set each pass would bury the changes in
  tens of thousands of identical events a day.

## Turning it on

Opt-in, via `ADHAR_EVENTS_BRIDGE_URL` on the emitting component (the manager
Deployment sets it). Deriving the address automatically would make every local
`adhar up` — where Kafka is not enabled — retry HTTP against a Service that does
not exist, once per reconcile, forever. A platform with no bus should be silent
about not having one.

## Adding an event type

1. Add the constant to `platform/events/taxonomy.go`, `io.adhar.<domain>.<object>.<verb>`.
2. If the domain is new, add its topic to `Topics` **and** a `KafkaTopic` to
   `data/kafka-operator/manifests/events-bus.yaml` — the test fails otherwise.
3. Publish it from the transition that makes it true, not from a polling loop.
4. Put the outcome **in** the event. A scale-up that was decided and then refused
   is the case an operator most needs; a bus carrying only successes is quietest
   exactly when something is wrong.
