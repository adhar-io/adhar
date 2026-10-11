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

Retention is declared per topic rather than inherited. The cluster has
`auto.create.topics.enable: true`, so publishing to an undeclared topic
**succeeds** and the broker creates it with one partition and its own default
retention — no error, just a stream kept for the wrong length of time.
`TestTheEventTopicsAreDeclaredInTheStack` holds the Go taxonomy and the
`KafkaTopic` objects together.

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
