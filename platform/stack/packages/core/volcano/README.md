# volcano

The batch scheduler under `BatchJob` (ADR-[0028](../../../../../docs/adr/0028-batch-scheduler.md),
design [0025](../../../../../docs/design/0025-batch-platform.md) §4): queues per team and
environment, fair share, prod reclaiming from dev and test, and all-or-nothing gangs for
partitioned jobs. It schedules only pods that name `schedulerName: volcano`; kube-scheduler
keeps every other pod on the cluster.

**Disabled in every profile, `stability: alpha`.** The production gate is Volcano v1.17, or a
maintainer's written sign-off on a v1.16 patch, with the Kind v1.37 canary green on A2.1–A2.10
(design §15). Until then batch runs on the native path, which is the Phase-1 product and not an
emergency mode. Enabling it from the Marketplace is supported, together with
`adhar-batch-volcano` (the queue policies in `core/adhar-batch/manifests/volcano/`); the
`adhar-batch-scheduler` descriptor (design D7) is what makes the `BatchJob` composition use it.

Decisions of 2026-11-14 that shape this package (design 0025 §15.1): Volcano runs use their own
PriorityClasses (D15); there is no `overcommit` (D16, withdrawn on evidence); the root queue and prod's
share are sized from the cluster at seed time (D17); one replica each, with alerts (D19); the
controllers and scheduler run on narrowed RBAC (D20).

| File | What it holds |
|---|---|
| `values.yaml` | Every decision about the chart, each with its reason. Change this, then run `./generate-manifests.sh`. |
| `generate-manifests.sh` | Renders the pinned chart (v1.16.0, checked against its sha256) into `manifests/install.yaml` and applies what the chart cannot express. Every edit asserts its anchor, so a chart bump that renames something stops the render. |
| `manifests/install.yaml` | **Generated.** Never edit it by hand. |
| `manifests/admission-certs.yaml` | cert-manager CA and serving certificate for the webhooks, replacing the chart's helm-hook Job. Ten years, because the binary reads its certificate once at start-up. |
| `manifests/root-queue.yaml.tmpl` | `Queue adhar-batch`, the parent of every `<org>-<team>-<tier>` leaf. Its `capability` and `deserved` are the cluster's batch ceiling, rendered at seed time (D17): `globalSettings.batch.capacity`, or 30% of the CPU and 20% of the memory of `maxWorkers × worker allocatable` as `adhar up` measures it (`config.ResolveBatchScheduler`). A platform whose seed data predates those fields renders the reference-cluster values (22 CPU / 24 GiB). |
| `manifests/scheduler-descriptor.yaml.tmpl` | `ConfigMap adhar-batch-scheduler` (design D7): `scheduler`, `version`, `gang`, `rootQueue`, `prodDeservedCPU`, `prodDeservedMemory`, rendered at seed time beside the root queue. Its presence is what makes the `BatchJob` composition take the Volcano path and the Team composition compose leaf queues; prod's share defaults to the larger of 4500m / 7Gi and a quarter of the ceiling (D17), 5500m / 7Gi on the reference cluster. |
| `manifests/alerts.yaml` | A component is down, or the API server cannot call a webhook. |
| `manifests/servicemonitor.yaml` | Scrapes the scheduler and the controllers. |
| `measure-footprint.sh` | The A2.8 measurement below. Read-only. |

## What is installed, and what is not

Three Deployments, one replica each, in `adhar-system`: `volcano-scheduler`,
`volcano-controllers`, `volcano-admission`. **Not installed:** JobFlow/JobTemplate (CRDs and
controllers), the colocation agent, the dashboard, Volcano Global, the agent scheduler, the
sharding and hyperNode controllers, and the chart's ValidatingAdmissionPolicies.

Scheduler configuration is design §4.2's stable core, without `overcommit` (D16): with it a
prod gang on a full cluster is never enqueued, so it can never reclaim (`values.yaml` has the
rehearsal that showed it). `values.yaml` explains, from the v1.16.0
source, how victims are chosen: `preempt` stays inside one queue and goes by priority;
`reclaim` takes only from reclaimable leaves (dev, test) that hold more than their deserved
share, and the gang plugin's reclaim vote is left ON because that is what keeps the decision
with the capacity plugin. Neither action can take a pod Volcano did not schedule (A2.7), with
one exception listed under "Open before the production gate".

## What the components may do

The admission ClusterRole can read Volcano's queues and pod groups and `get`/`update` its own
seven webhook configurations by name; upstream could rewrite every webhook configuration on the
cluster and approve CSRs. The controllers and the scheduler lose everything the v1.16.0 binaries
do not call with this configuration (generator, "Narrow the controllers' and scheduler's
ClusterRoles"): CRD writes, leases, Secrets, JobFlow, colocation, sharding and hyperNode writes,
PriorityClass writes, the controllers' ConfigMap, Service and NetworkPolicy writes, and the
scheduler's node and ConfigMap writes. Two consequences to know:

- **Volcano Job plugins.** `env` is supported (it is the only one the `BatchJob` composition
  emits, design §4.5). `ssh` needs Secrets, and `svc` (with the tensorflow, mpi, pytorch and
  ray plugins, which the job webhook makes pull `svc` in) needs cluster-wide ConfigMap, Service
  and NetworkPolicy writes; all of them are refused by the API server (2026-11-14). Those writes
  are the reason design §4.1 leaves the dashboard out: a ServiceAccount that may update any
  ConfigMap can rewrite `volcano-scheduler-configmap`, `cilium-config`, `coredns` or `argocd-cm`.
  `adhar-batch-queue-binding` refuses such a vcjob at admission (design D20), so it never gets
  as far as failing in the controller with `forbidden`. To support them again, delete the three
  lines in `narrow('volcano-controllers')` and regenerate.
- **One replica of each component** (D19): the platform's single-replica rule
  (`TestNoPackageWorkloadShipsMoreThanOneReplica`) holds here too. `manifests/alerts.yaml`
  pages when a component is down or the API server cannot call a Volcano webhook, which is
  when an admission outage refuses Volcano Job creation (design F13).
- **More than one scheduler or controllers replica** needs `leader_elect_enable: true` AND a
  namespaced Role granting `leases` in `adhar-system` (the lease namespace is already pinned to
  it). A second admission replica needs nothing else.

## Removing Volcano (A2.10)

Delete the `adhar-batch-scheduler` descriptor first. Every `BatchJob` on `scheduler: auto` (the
default) then renders the native path on its next reconcile, partitioned jobs as an Indexed Job
without a gang (design §4.5), with no edit to the `BatchJob`. One that names
`scheduler: volcano` explicitly reports `PrerequisitesReady=False` until it is changed. Then
disable this package. In the other order, the composition keeps emitting `schedulerName:
volcano` for a scheduler that is gone, and every new run stays Pending (design §13 F12).

## A2.8: what Volcano costs the API server

Recorded here, on the canary, before the package leaves alpha. The control plane is the
platform's tightest resource (the 2026-11-03 cluster: 97% memory, `kube-apiserver` 9 GiB, 354
CRDs), so this is measured, not assumed.

1. On the canary cluster (Kind v1.37, the batch e2e suite's cluster) with Volcano **not**
   installed, and nothing else syncing, run `./measure-footprint.sh before` three times, five
   minutes apart. API-server RSS moves with GC; keep the median row.
2. Enable `volcano` (Marketplace, or `enabled: "true"` on its element in Gitea's copy of the
   ApplicationSet). Wait until the Application is Synced + Healthy, then 15 more minutes for the
   informers and the queue controller to settle.
3. Run `./measure-footprint.sh after` three times, five minutes apart; keep the median.
4. Paste both rows below and write the delta: RSS, CRD bytes, and the control-plane node's
   memory.
5. **It fits** when the control-plane node's memory after Volcano stays below 85% and the
   `kube-apiserver` RSS delta is under 5% of the node's memory. If it does not, the package does
   not leave alpha on that cluster size; the native path stays the product there.

The CRD count should rise by the ten Volcano CRDs this package installs (≈0.58 MB of JSON
without JobFlow, design §14), and `volcano.sh` bytes should be within a few KiB of that.

| Phase | When (UTC) | Context (server) | Kubernetes | kube-apiserver RSS | Heap in use | CRDs / bytes | Volcano CRDs / bytes | Control-plane memory |
|---|---|---|---|---|---|---|---|---|
| before | not yet measured | needs the canary cluster | | | | | | |
| after | not yet measured | needs the canary cluster | | | | | | |

**Delta:** not yet measured.

## Found before the production gate (2026-11-14)

Found by running the binaries, each with the experiment that showed it, and each now settled by a
decision in design 0025 §15.1. Kept here because the experiments are the evidence.

1. **The shipped batch PriorityClasses stop reclaim and preempt.** `adhar-batch` and
   `adhar-batch-critical` are `preemptionPolicy: Never` (design INV-5, `core/adhar-batch`), the
   Priority admission plugin copies that onto every run pod, and Volcano v1.16.0 skips a
   reclaimer with `Never` (`actions/reclaim/reclaim.go:145`) and refuses it as a preemptor
   (`actions/preempt/preempt.go:447`). So with these classes prod never reclaims from dev and a
   critical run never preempts inside its queue: A2.4 cannot pass. The rehearsal now gives its
   pods the shipped class and FAILS on exactly this; with the same classes at
   `PreemptLowerPriority` it passes in 16 s. **Settled by D15:** Volcano runs use
   `adhar-batch-volcano` (−100) and `adhar-batch-volcano-critical` (0), `PreemptLowerPriority`;
   the native classes stay `Never`. kube-scheduler never schedules, and so never preempts for, a
   `schedulerName: volcano` pod, and Volcano picks victims only among its own jobs, so INV-5
   (reworded) still holds.
2. **A prod gang larger than prod's deserved share in every dimension cannot reclaim.** The
   capacity plugin's reclaim check passes a task only while the queue's future use stays within
   `deserved` in at least one requested resource, so the gang's statement is discarded half way.
   Measured: 2 × 1 CPU/256Mi against `deserved` 1 CPU/256Mi waits forever; against 2 CPU it
   reclaims. D8's default (2 CPU / 4 GiB) was smaller than the design's own example gang.
   **Settled by D17:** prod's `deserved` is derived from the cluster's batch ceiling and is never
   smaller than the largest example gang, and a `BatchJob` whose gang still exceeds it gets
   `QueueFits=False`.
3. **No `overcommit` plugin means enqueue checks only queue capability.** A gang larger than
   the free room but within its leaf goes Inqueue at once, its pods are created and wait
   Pending, and they count against the namespace ResourceQuota, which §4.5 says does not
   happen. A2.1 still holds (allocate never binds part of a gang). **Settled by D16 the other
   way:** `overcommit` was tried and reverted (it stops prod reclaiming, A2.4), and design §4.5
   now says that an admitted gang's waiting pods hold namespace quota.
4. **A pod Volcano did not schedule can still be a victim** if it carries
   `scheduling.k8s.io/group-name`: the scheduler cache builds a job for any such pod whatever
   its `schedulerName`. Only a team's own pods are exposed (Kyverno keeps hand-written PodGroups
   in the team's leaf). **Closed in `adhar-batch-queue-binding`:** a pod in a team namespace
   with that annotation and without `schedulerName: volcano` is refused.

## Tests

- `TestVolcano*` in `platform/controllers/adharplatform/` hold the render: webhook scope, RBAC,
  namespaces, names no other package uses, one pinned version, resources, probes, alerts, the
  root queue and the scheduler configuration.
- `TestVolcanoComponentsRunOnTheirNarrowedRBAC` runs the real v1.16.0 scheduler and controllers
  against a real API server as their own ServiceAccounts: a gang is bound whole, prod does not
  take from a dev leaf inside its share, then reclaims once dev is over it, and a pod
  kube-scheduler placed is never touched. Run pods carry `adhar-batch-volcano`, the class the
  `BatchJob` composition gives a Volcano run (D15), so the A2.4 subtest exercises the shipped
  class; the authorisation checks run either way. It needs the binaries (`VOLCANO_BIN_DIR`); the comment
  at the top of the test says how to build them.
- `platform/stack/packages/CONFLICTS.md` §3 records the name scan against every other package.
