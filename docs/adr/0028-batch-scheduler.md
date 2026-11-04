# ADR-0028: Volcano is the batch scheduler; Armada is not adopted

- **Status**: Proposed
- **Date**: 2026-11-04
- **Owners**: Platform Engineering
- **Design**: [docs/design/0025-batch-platform.md](../design/0025-batch-platform.md). The design index normally numbers a design after its ADR. This design was commissioned as `0025`; see its §19.
- **Related**: [ADR-0004](0004-applicationset-package-model.md), [ADR-0005](0005-crossplane-v2-namespaced.md), [ADR-0011](0011-shared-platform-namespace.md), [ADR-0023](0023-control-dataplane-separation.md), [ADR-0026](0026-concept-hierarchy.md), [ADR-0027](0027-functions-on-knative.md)

Citations `[S#]` are listed under [Sources](#sources), with URLs. They share their numbering with design 0025. Live figures come from read-only `kubectl` reads of the DigitalOcean cluster (`64.227.182.201`) on 2026-11-03.

## Context

Adhar is getting a batch management system: Spring Batch jobs written against adhar-kit, run on the platform, operated from the Console and the CLI, and delivered dev → test → prod like any other application (design 0025). The question this ADR answers is which batch scheduler sits under it. Volcano or Armada?

**What the scheduler has to serve:**

1. **Spring Batch's execution models.**
   - **One pod:** single-step jobs, multi-threaded steps, local partitioning.
   - **Manager plus N workers:** remote partitioning and remote chunking. They must start together, end when the manager ends, and restart together.
   - **Scheduled runs.**
   - Restart belongs to Spring Batch. A failed job re-run with the same identifying parameters re-executes only the steps that did not complete [S67]. The scheduler only has to re-run pods: for a gang, the whole gang.
2. **Kubernetes-native objects.** Argo CD must sync them, Crossplane compose them, Kyverno admit them, namespace RBAC govern them, and the Console watch them as the signed-in user.
3. **Tenancy.**
   - Workloads run in `<project>-<env>` namespaces (ADR-0026).
   - Queues have to map onto team × environment.
   - Prod must be able to take capacity back from dev and test.
4. **Batch must never preempt** a service or a platform pod. Most stateful pods sit on node-local volumes and can only return to their own node.
5. **Cooperation with Adhar's node autoscaler.** It scales up only for Pending pods with a capacity message (`platform/controllers/autoscaler/decision.go:477`).
6. **A small footprint.** On 2026-11-03 the live cluster:

   | Measure | Value |
   |---|---|
   | Workers | 10 × `s-8vcpu-16gb`, already at the autoscaler's `maxWorkers` |
   | Worker memory requested | 71.7% (86.9 of 121.2 GiB) |
   | Requestable headroom | ≈34 GiB |
   | Control plane | 97% memory; `kube-apiserver` 9 GiB; 354 CRDs |
7. **Kubernetes 1.37.** The live cluster runs v1.37.2. 1.37 was released on 2026-08-26 [S46].
8. **The fleet model.** Multi-cluster placement goes through the Karmada hub Adhar already runs, in push mode. The hub reaches into members; members hold no credential for it (`platform/stack/packages/core/karmada/values.yaml`). It has 0 DataPlanes today.
9. **One platform namespace**, `adhar-system` (ADR-0011).

**Options considered:**

- **A. Volcano**: CNCF Incubating.
- **B. Armada**: CNCF Sandbox.
- **C. Kubernetes only**: kube-scheduler with `batch/v1` Job and CronJob.
  - Kubernetes 1.37's native gang (Workload and PodGroup API) is beta and **disabled by default** behind `GenericWorkload` [S47][S48].
  - Its Job integration (`WorkloadWithJob`) is alpha [S47].
  - The live cluster serves only `scheduling.k8s.io/v1`.
- **D. Kueue with JobSet**: the upstream path; v0.20.4 shipped 2026-10-29 [S60]. It is outside the two named options, and is recorded here as the long-term fallback.

## Decision

1. **Volcano is Adhar's batch scheduler** on every cluster that runs batch.
   - It ships as one GitOps stack package, `core/volcano`, installed in `adhar-system`.
   - Not installed: the JobFlow/JobTemplate subchart, the colocation agent, the dashboard and Volcano Global.
   - Webhooks are rendered statically, with cert-manager CA injection.
   - The admission ClusterRole is narrowed. Upstream it can create and update every webhook configuration and approve CSRs [S11].
   - Image tags are pinned. The chart's default tag is `latest` [S8].

2. **Version policy.**
   - Volcano **v1.16.x**, exact patch, ships disabled by default and runs Adhar's batch e2e suite on Kind v1.37 in CI.
   - **Production enables it at v1.17**, the first release planned with 1.37 support, estimated 2027-01-30 [S7]. The alternative is a maintainer's written sign-off on a v1.16 patch with that canary green.
   - Why this is needed:
     - Volcano's compatibility matrix stops at 1.36 [S5], and v1.16 builds against the 1.36.1 libraries [S98].
     - The 1.37 upgrade, PR #6129, is open; its author reports e2e passing on kind v1.37.0 [S6].

3. **We use the stable core:**

   | Kind | What |
   |---|---|
   | Plugins | `gang`, `priority`, `conformance`, `drf`, `predicates`, `capacity` (hierarchical) [S16], `nodeorder`, `binpack` |
   | Actions | `enqueue`, `allocate`, `backfill`, `reclaim`, `preempt` [S15] |
   | Policies | Volcano Job lifecycle policies [S12][S13] |
   | CRDs | The CronJob CRD (since v1.13 [S4]) |

   **Deferred** until they leave alpha or a need appears:
   - `NamespaceQueue` and `fairshare`; NamespaceQueue is alpha and off in v1.16 [S2][S18];
   - gang-aware preemption and reclaim (alpha since v1.15) [S3];
   - JobFlow; sharding; the agent scheduler.

4. **The scheduler is behind a contract.**
   - adhar-kit and the templates never name a scheduler.
   - The `BatchJob` composition is the only component that sets `schedulerName`, the queue and the gang shape.
   - With no Volcano descriptor on the cluster, it renders plain Kubernetes Jobs and CronJobs. That **native path ships first**, as design 0025's Phase 1, and is the permanent fallback.

5. **Queues follow the tenancy model.**
   - Leaf queues `<org>-<team>-<env>` sit under one `adhar-batch` parent, whose capability is the cluster's batch ceiling.
   - The Team composition emits them.
   - Only `prod` declares a `deserved` share and is not reclaimable, so prod reclaims from dev and test.
   - Volcano's job admission checks only that a queue is a valid leaf [S29], so a Kyverno policy binds each namespace to its own queue.

6. **Priorities.** Batch PriorityClasses use `preemptionPolicy: Never` and a value ≤ 0 [S52]:
   - `adhar-batch` at −100;
   - `adhar-batch-critical` at 0, prod only.

   Batch therefore never evicts a service or a platform pod. Inside batch, Volcano's `preempt` (within a queue) and `reclaim` (across queues) decide.

7. **Gangs.**
   - A remote-partitioned job is **one** Volcano Job:
     - `minAvailable: N+1`;
     - the manager's `TaskCompleted → CompleteJob` ends the workers;
     - `PodEvicted`/`PodFailed → RestartJob`, bounded by `maxRetry`;
     - non-retryable exit codes (the kit's 2, 3, 4, 6, 7) `→ TerminateJob` [S12][S13].
   - A Volcano Job's pods are not created until its PodGroup leaves Pending [S20], so a gang never half-allocates.

8. **The fleet stays Karmada's.**
   - Volcano Jobs propagate through Karmada's built-in Volcano Job interpreter (health, per-component resources, status aggregation) [S28].
   - Volcano Global is not adopted: alpha, no commit since 2026-03-06 [S23].

9. **Armada is not adopted.**
   - **Revisit only if all three hold:**
     1. batch moves to dedicated clusters;
     2. one queue must be fair across the whole fleet, at 10⁵–10⁶ jobs a day, which is Armada's design point [S33];
     3. Pulsar plus a second API and identity plane become acceptable.
   - Even then, Armada would sit beside Karmada, not replace it.

## Evidence

| Criterion (weight) | Volcano | Armada |
|---|---|---|
| **Manager plus workers lifecycle** (decisive) | One Volcano Job: `minAvailable`, task and job policies on events and exit codes, `CompleteJob` when the manager finishes, `RestartJob` for the gang, `maxRetry` [S12][S13] | A gang is N+1 separate jobs sharing gang annotations [S35]. The retry engine is **off by default**, **skips gangs of two or more**, and **never retries a preempted run** [S36]. Nothing stops the workers when the manager finishes [S35]. A missing grace period defaults to **1 s** [S35]. |
| **Kubernetes-native objects** (high) | Every object is a CRD (Job, Queue, PodGroup, CronJob) [S10][S12]. Argo CD, Crossplane, Kyverno, RBAC and Console watches work unchanged. Argo CD ships no health check for Volcano kinds, so Adhar adds Lua checks [S87]. | Jobs are submitted to Armada's gRPC/REST API and are not Kubernetes objects. Requests must equal limits [S37]. It has its own authentication and authorisation [S39], so Kyverno rules on `request.userInfo` would see the executor's identity unless impersonation is enabled [S38]. |
| **Footprint** (high, because memory binds) | Three Deployments. The chart sets no requests [S11]; the proposed total is ≈0.4 CPU / ≈1 GiB *(estimate)*, ≈3% of the headroom. | Eight services at 200m/512Mi by chart default [S41], plus Apache Pulsar (≈5 GiB of default requests and about 11 PVCs [S45]), plus Postgres and Redis [S33]. **≈10 GiB, ≈29% of the headroom.** |
| **Fleet fit** (high) | Karmada's built-in interpreter [S28]. Placement by Karmada, fairness per member cluster. | Executors on each cluster **connect to a central server** with a credential [S38][S34]. That is the inverse of the push model Adhar's Karmada design chose on purpose. True fleet-wide fair share is the one thing Armada adds. |
| **Cron and dependencies** (medium) | CronJob CRD [S4], with deterministic run names and a scheduled-timestamp annotation [S17]. JobFlow DAGs exist [S100] but are not installed. | None natively. It expects an external engine and ships an Airflow operator [S40]; Adhar does not ship Airflow. |
| **Tenancy** (medium) | Hierarchical queues on the capacity plugin; only leaves schedule; the webhook keeps children's `deserved` and `guarantee` sums within the parent [S16]. Leaf validation at admission [S29]. | Flat queues with a priority factor; fair share across active queues [S35]. |
| **Autoscaler** (medium) | A gang held at enqueue creates no pods [S20], so Adhar's autoscaler must read PodGroups. This is fixable, because the signal is a Kubernetes object. | Queued work lives outside Kubernetes [S34]. The autoscaler gets **no** signal at all. |
| **One namespace** (medium) | Fits. Webhooks exclude the release namespace and `kube-system` [S9]; team batch runs in `<project>-<env>`, so this is harmless. | Fits, but brings three stateful systems (Pulsar, Postgres, Redis) into `adhar-system`, and a second log broker beside the platform's Kafka event bus. |
| **Kubernetes 1.37** (medium) | Matrix stops at 1.36 [S5]; PR #6129 open with e2e green on kind 1.37 [S6]; v1.17 estimated 2027-01-30 [S7] | No compatibility matrix found; builds against client-go v0.32.11 [S42] |
| **Governance and activity** (medium) | CNCF **Incubating** since 2022-03-21 [S25]. 8 maintainers across NVIDIA, Huawei, Alibaba, Baidu and Hjmicro [S24]. 164 commits from 40 authors in 90 days [S31]. v1.16.0 shipped 2026-10-14 [S1]. | CNCF **Sandbox** since 2022-07-25 [S44]. 15 maintainers, **all at G-Research** [S43]. 109 commits from 22 authors in 90 days [S31]. v0.22.16 shipped 2026-10-27; v0.22.0 forced a migration that blocks scheduler writes unless staged through v0.21.x [S32]. |
| **Ecosystem** (low–medium) | The Spark operator Adhar ships supports Volcano natively [S27]. An official Headlamp plugin exists, and Adhar ships Headlamp [S26]. | `armada-spark`: Spark Connect experimental, no releases [S99] |
| **UI** (low) | The Console reads the CRDs; Headlamp plugin [S26] | Lookout, with OIDC [S33] |

**Native Kubernetes (option C)** is the fallback, not the choice. It already provides what single-pod jobs need:
- `podFailurePolicy` with `DisruptionTarget`, stable since 1.31;
- `podReplacementPolicy`;
- suspension, with SIGTERM and grace;
- success policies that terminate lingering pods [S49].

It lacks queues, fair share and reclaim. Its gang is beta and off by default [S47][S48].

**Kueue + JobSet (option D)** is where upstream Kubernetes is heading [S47][S60]. It is outside the requested comparison, and is kept as the long-term path if Volcano proves unworkable.

## Consequences

### Positive

- **The hard Spring Batch case is native.** Remote partitioning restarts as a gang, and Spring Batch re-runs only unfinished partitions [S67].
- **Nothing new to integrate.** Batch reuses Git, Argo CD, Crossplane, Kyverno, RBAC and the Console. There is no second API, identity model or broker.
- **About a tenth of Armada's memory**, on a cluster where memory is the binding constraint.
- **Team × environment queues** with prod reclaim, inside the hierarchy the platform already models.
- **The scheduler is replaceable.** Kit and templates never name it, so moving to Kueue, or to the native Workload API, later is a composition change.

### Trade-offs and risks

| Risk | Mitigation |
|---|---|
| 1.37 is not in Volcano's matrix | Disabled by default; Kind 1.37 canary; production gate at v1.17. Until then production runs the native path. |
| The autoscaler cannot see gangs held at enqueue [S20] | The autoscaler reads PodGroup demand by plain LIST behind a CRD discovery check (never an informer on a CRD that may be absent), and scales only for opted-in prod gangs below queue capability |
| Two schedulers place pods on the same nodes | Kubelet rejects the loser; `PodFailed → RestartJob` and Count with backoff absorb it; a tainted batch node group later |
| Broad admission RBAC [S11] | Narrowed to `resourceNames`, no CSR rules, and a guard test |
| API server memory at 97%, plus about 10 CRDs (≈0.58 MB without JobFlow) [S10] | Canary measurement before enabling; JobFlow not installed |
| Argo CD has no health checks for Volcano kinds [S87] | Lua checks in both values files and both generated install manifests; the existing "every custom kind is assessable" guard is extended |
| Any namespace can name any queue [S29] | Kyverno binds namespace → queue until `NamespaceQueue` is GA [S2] |
| The Volcano Job API is still `v1alpha1` [S12] | Only the `BatchJob` composition emits it; a bump is a composition change |
| No fleet-wide fair share | Accepted. Teams are bound to environments and environments to data planes, so per-cluster fairness suffices. Armada's revisit triggers are recorded above. |

## Verification

This ADR moves to **Accepted** when design 0025's Phase 2 canary passes on Kind v1.37:

| # | Criterion |
|---|---|
| A2.1 | No half-allocated gang |
| A2.2 | A killed worker leads to a gang restart that re-runs only unfinished partitions |
| A2.3 | The manager ends the workers |
| A2.4 | Prod reclaims from a dev gang, which stops gracefully |
| A2.5 | Queue binding denies a foreign queue |
| A2.6 | Exactly one scale-up for an opted-in gang; none at queue capability |
| A2.7 | Volcano never evicts pods it did not schedule |
| A2.8 | API server memory delta recorded |
| A2.9 | The batch e2e suite is green on 1.37 |
| A2.10 | Removing Volcano degrades every `BatchJob` to the native path without edits |

Guard tests pin:
- the pinned image tags and the narrowed admission ClusterRole;
- the absence of the JobFlow CRDs;
- the batch PriorityClasses' `Never` and values ≤ 0;
- the presence of Argo CD health checks for every Volcano kind the stack syncs.

## Sources

Accessed 2026-11-03 unless marked **(11-04)**, which means accessed 2026-11-04. Numbering is shared with design 0025.

| # | Source | URL |
|---|---|---|
| S1 | Volcano releases | https://github.com/volcano-sh/volcano/releases |
| S2 | Volcano v1.16.0 release notes | https://github.com/volcano-sh/volcano/releases/tag/v1.16.0 |
| S3 | Volcano v1.15.0 release notes | https://github.com/volcano-sh/volcano/releases/tag/v1.15.0 |
| S4 | Volcano v1.13.0 release notes (Cron VolcanoJob) | https://github.com/volcano-sh/volcano/releases/tag/v1.13.0 |
| S5 | Volcano Kubernetes compatibility matrix | https://github.com/volcano-sh/volcano/blob/master/README.md#kubernetes-compatibility |
| S6 | Volcano PR #6129 (Kubernetes v1.37) | https://github.com/volcano-sh/volcano/pull/6129 |
| S7 | Volcano v1.17.0 roadmap, issue #6166 | https://github.com/volcano-sh/volcano/issues/6166 |
| S8 | Volcano Helm values | https://github.com/volcano-sh/volcano/blob/master/installer/helm/chart/volcano/values.yaml |
| S9 | Volcano webhooks template | https://github.com/volcano-sh/volcano/blob/master/installer/helm/chart/volcano/templates/webhooks.yaml |
| S10 | Volcano CRDs (sizes) | https://github.com/volcano-sh/volcano/tree/master/installer/helm/chart/volcano/crd/bases |
| S11 | Volcano development manifest (admission ClusterRole; no requests) | https://github.com/volcano-sh/volcano/blob/master/installer/volcano-development.yaml |
| S12 | Volcano batch API (`JobSpec`, `TaskSpec`, `LifecyclePolicy`, `CronJobSpec`) **(also 11-04)** | https://github.com/volcano-sh/apis/blob/master/pkg/apis/batch/v1alpha1/job.go |
| S13 | Volcano bus actions and events | https://github.com/volcano-sh/apis/blob/master/pkg/apis/bus/v1alpha1/actions.go |
| S15 | Volcano docs: actions and plugins | https://volcano.sh/en/docs/plugins/ |
| S16 | Volcano hierarchical queues on the capacity plugin | https://github.com/volcano-sh/volcano/blob/master/docs/design/hierarchical-queue-on-capacity-plugin.md |
| S17 | Volcano CronJob controller | https://github.com/volcano-sh/volcano/blob/master/pkg/controllers/cronjob/cronjob_controller_util.go |
| S18 | Volcano feature gates | https://github.com/volcano-sh/volcano/blob/master/pkg/features/volcano_features.go |
| S20 | Volcano job controller (pods only after the PodGroup leaves Pending) | https://github.com/volcano-sh/volcano/blob/master/pkg/controllers/job/job_controller_actions.go |
| S23 | Volcano Global | https://github.com/volcano-sh/volcano-global |
| S24 | Volcano maintainers | https://github.com/volcano-sh/community/blob/main/MAINTAINERS.md |
| S25 | CNCF project page: Volcano | https://www.cncf.io/projects/volcano/ |
| S26 | Kubernetes blog: Headlamp Volcano plugin (2026-06-25) | https://kubernetes.io/blog/2026/06/25/visual-context-volcano-headlamp-plugin/ |
| S27 | Kubeflow Spark Operator: Volcano integration | https://spark.kubeflow.org/en/latest/user-guide/volcano-integration.html |
| S28 | Karmada built-in interpreter for Volcano Jobs | https://github.com/karmada-io/karmada/blob/master/pkg/resourceinterpreter/default/thirdparty/resourcecustomizations/batch.volcano.sh/v1alpha1/Job/customizations.yaml |
| S29 | Volcano job admission (queue validation) | https://github.com/volcano-sh/volcano/blob/master/pkg/webhooks/admission/jobs/validate/admit_job.go |
| S31 | GitHub API: repository and commit statistics, 90 days to 2026-11-03 | https://api.github.com/repos/volcano-sh/volcano ; https://api.github.com/repos/armadaproject/armada |
| S32 | Armada releases (v0.22.16; v0.22.0 migration notice) | https://github.com/armadaproject/armada/releases |
| S33 | Armada README (Redis, Postgres, Pulsar; Lookout; OIDC; scale) | https://github.com/armadaproject/armada |
| S34 | Armada system overview (executor per cluster; queued work outside Kubernetes) | https://github.com/armadaproject/armada/blob/master/docs/system_overview.md |
| S35 | Armada scheduling and preemption (queues, gang annotations, 1 s default grace) | https://github.com/armadaproject/armada/blob/master/docs/scheduling_and_preempting_jobs.md |
| S36 | Armada retry policies (off by default; gangs excluded; preemption not retried) | https://github.com/armadaproject/armada/blob/master/docs/retry_policies.md |
| S37 | Armada: creating and submitting jobs (requests must equal limits) | https://github.com/armadaproject/armada/blob/master/docs/creating_and_submitting_jobs.md |
| S38 | Armada Helm charts (`apiConnection.armadaUrl`, `impersonateUsers`) | https://github.com/armadaproject/armada/blob/master/docs/armada_helm_charts.md |
| S39 | Armada authentication | https://github.com/armadaproject/armada/blob/master/docs/auth.md |
| S40 | Armada Airflow operator | https://github.com/armadaproject/armada/blob/master/docs/armada_airflow_operator.md |
| S41 | Armada component chart values (200m/512Mi each) | https://github.com/armadaproject/armada/tree/master/deployment |
| S42 | Armada `go.mod` (client-go v0.32.11) | https://github.com/armadaproject/armada/blob/master/go.mod |
| S43 | Armada maintainers and adopters | https://github.com/armadaproject/armada/blob/master/MAINTAINERS.md |
| S44 | CNCF project page: Armada | https://www.cncf.io/projects/armada/ |
| S45 | Apache Pulsar Helm chart values (chart 4.9.0) | https://github.com/apache/pulsar-helm-chart/blob/master/charts/pulsar/values.yaml |
| S46 | Kubernetes v1.37 release (2026-08-26) | https://kubernetes.io/blog/2026/08/26/kubernetes-v1-37-release/ |
| S47 | Kubernetes v1.37: advancing workload-aware scheduling (2026-09-08) | https://kubernetes.io/blog/2026/09/08/kubernetes-v1-37-advancing-workload-aware-scheduling/ |
| S48 | Kubernetes PodGroup API (beta in 1.37, disabled by default) | https://kubernetes.io/docs/concepts/workloads/podgroup-api/ |
| S49 | Kubernetes Jobs (pod failure policy, replacement policy, suspension, success policy) **(also 11-04)** | https://kubernetes.io/docs/concepts/workloads/controllers/job/ |
| S52 | Kubernetes pod priority and preemption | https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/ |
| S60 | Kueue releases (v0.20.4, 2026-10-29) | https://github.com/kubernetes-sigs/kueue/releases |
| S67 | Spring Batch: restart (completed steps are skipped) | https://docs.spring.io/spring-batch/reference/step/chunk-oriented-processing/restart.html |
| S87 | Argo CD resource customizations (no `batch.volcano.sh` health check) | https://github.com/argoproj/argo-cd/tree/master/resource_customizations |
| S98 | Volcano `go.mod` (k8s.io 1.36.1) | https://github.com/volcano-sh/volcano/blob/master/go.mod |
| S99 | armada-spark | https://github.com/armadaproject/armada-spark |
| S100 | Volcano JobFlow design | https://github.com/volcano-sh/volcano/blob/master/docs/design/jobflow/README.md |

**Internal evidence:**
- Live cluster reads on 2026-11-03: nodes and usage, `api-versions`, CRDs, PriorityClasses, Karmada DataPlanes (none).
- `platform/stack/packages/core/karmada/values.yaml` (push-mode rationale).
- `platform/controllers/autoscaler/decision.go:477-526` (scale-up signal).
- `platform/stack/adhar-appset-{local,production}.yaml` (profiles).
