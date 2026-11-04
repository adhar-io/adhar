# Low-Level Design — Adhar Batch: Spring Batch through adhar-kit, scheduled by Volcano

Detailed design for [ADR-0028](../adr/0028-batch-scheduler.md) (the scheduler decision). This is the single, authoritative design for batch on Adhar: goals and invariants, architecture and ownership, the scheduler integration, the `adhar-kit-batch` API, the self-service model (Application `type: batch` and the `BatchJob` XRD), the Console Batch area, the `adhar batch` CLI, the golden-path templates, security, observability, failure modes, capacity, a phased plan with acceptance criteria, and a per-repository work breakdown precise enough to hand to implementation agents.

| | |
|---|---|
| Status | **Proposed — forward-looking.** Nothing here is built. |
| Pairs with | [ADR-0028](../adr/0028-batch-scheduler.md): Volcano is the batch scheduler; Armada is not adopted. |
| Date | 2026-11-04 |
| Repositories | `adhar` (platform, control plane, CLI) at `6035928` · `adhar-kit` at `910ee6a` · `adhar-templates` at `9c2c404` · `adhar-console` at `9d9877c`. Line references are against these commits; the `adhar` working tree has unrelated uncommitted edits (for example in `cmd/apps/deploy.go`), so its lines may have moved. |
| Companion | [Design 0026](0026-workflows-and-gamedays.md) (Argo Workflows, written in parallel): Argo orchestrates, Volcano schedules, a `BatchJob` run is a workflow step (§12.1). |
| Live evidence | Read-only `kubectl get/describe/top/explain` on the DigitalOcean cluster `64.227.182.201` on **2026-11-03**. On 2026-11-04 that API server no longer answered, and the `adhar` kube context had been rewritten to a different cluster, which was not read. Every live figure below is therefore as of 2026-11-03. |
| Citations | `[S#]` is an external source, listed with its URL and access date in §21. Repository facts cite `path:line` against the hashes above. Statements marked *(decision)* or *(estimate)* are this design's, not a vendor's. |

---

## 0. Summary

Adhar Batch runs Spring Batch jobs as **one Kubernetes run object per job-instance attempt**: a `batch/v1` Job (one pod) or a Volcano Job (a manager plus N workers, gang-scheduled). The pod runs the application's own image with an **adhar-kit runner** that owns parameters, identity, restart, recovery, graceful stop and the exit-code contract. Spring Batch keeps its metadata in a **JDBC JobRepository in the application's own CloudNativePG database** (schema `batch`), so restart semantics belong to Spring Batch; the scheduler only re-runs pods [S67][S70].

Teams declare jobs as a **`BatchJob` XR** in their repository's `deploy/` overlays. Argo CD syncs it, Kargo promotes its image dev → test → prod, and a Crossplane composition turns it into a CronJob (or Volcano CronJob), a run template, a ServiceAccount and alert rules. The composition is the **only** component that names a scheduler: the kit and the templates do not. With no scheduler installed, the composition renders plain Kubernetes objects. That native path is the first thing that ships (Phase 1) and the permanent fallback.

**Volcano** (ADR-0028) adds per-team × per-environment queues, fair share, reclaim of dev/test capacity for prod, and all-or-nothing gangs for remote-partitioned jobs (Phase 2). Production adopts it at Volcano v1.17, the first release planned with Kubernetes 1.37 support (estimated 2027-01-30) [S7], or earlier only if Adhar's 1.37 canary suite passes on a pinned v1.16 patch.

Operations are Kubernetes API calls made **as the user**, from the Console and the CLI alike:

| Operation | Kubernetes action |
|---|---|
| Run now | Create a run from the composed run template |
| Stop / Resume | Suspend / resume the Job (Volcano: `AbortJob` / `ResumeJob`) |
| Restart | Create a new run with the same identifying parameters |
| Hold / Release | Set or remove one annotation on the `BatchJob` |

There is no batch server, no central database holding every team's credentials, and no Spring Cloud Data Flow. SCDF stopped being open source in April 2025 [S81].

Delivery is phased; each phase ships on its own (§15):

| Phase | Scope |
|---|---|
| **0** | Fix the kit's shipped defects |
| **1** | First production batch job on native Kubernetes |
| **2** | Volcano: queues, gang, partitioned template |
| **3** | Enterprise operations: events, calendars, dependencies, backfill, SLA, DLQ replay, retention |
| **4** | Scale and fleet: remote chunking, Karmada placement, batch node pools, lakehouse I/O, lineage |

---

## 1. Goals, non-goals, invariants

### 1.1 Goals

- **G1** — A Java team writes a Spring Batch job against adhar-kit and gets, with no platform tickets, everything below:
  - scheduling with time zones
  - restart from the last commit
  - run history
  - alerts
  - dashboards
  - a Console view
  - CLI operations
  - dev → test → prod promotion
- **G2** — Runs survive what clusters do to pods. OOM kills, preemption, eviction, node drain, SIGTERM and SIGKILL all end in a correct restart with no duplicate output, for writers that share the JobRepository's transaction (§5.9).
- **G3** — Batch cannot hurt the platform:
  - It never preempts a service or a platform pod.
  - It never buys nodes unless it is allowed to.
  - It runs inside queue, namespace-quota and team ceilings.
- **G4** — The definition of a job is code in Git; promotion follows the Release model of ADR-0026. Operating a job (run, stop, restart, hold) is a separate, audited right that never lets an operator run anything other than the deployed definition.
- **G5** — The same objects and verbs in the local profile (Kind; no Kafka, Kargo, Alloy or Volcano) and in production.
- **G6** — The enterprise feature set: calendars, dependencies, backfill, SLA deadlines, dead-letter handling and replay, queues and fair share, gang execution, audit, cost attribution. Each is delivered in the phase that can carry it (§15, §2.3).

### 1.2 Non-goals

- **N1.** A batch server or an SCDF-like control plane. The Console and the CLI are the management plane. SCDF and Spring Cloud Deployer are commercial-only since 2025-04-21 [S81], and the last OSS Kubernetes deployer is 2.9.5 (2024-09-13) [S82].
- **N2.** Spring Cloud Task. It is OSS and active [S83], but Spring Batch 6 and Kubernetes Jobs already provide what it would add here (task records, exit codes).
- **N3.** A workflow/DAG engine of batch's own.
  - Multi-step orchestration (branching, approvals, non-batch steps) belongs to Argo Workflows, designed in parallel in [design 0026](0026-workflows-and-gamedays.md): Argo orchestrates, Volcano schedules, and a `BatchJob` run is a step. This design owns that step (`adhar-batch-steps/run`, §12.1).
  - Data-team DAGs stay in Dagster/Prefect.
  - Batch itself keeps only a narrow `dependsOn` for "B after A for the same business date", evaluated by the batch observer (§12).
  - Volcano JobFlow is not installed (§4.1).
- **N4.** A MongoDB JobRepository, Dapr Jobs as the scheduler, Armada (ADR-0028), Volcano Global [S23], GPU batch, managed file transfer, COBOL conversion.
- **N5.** Kit SDKs in other languages. adhar-kit is Java-only, by the user's standing decision.
- **N6.** A new kit module. Every addition extends `adhar-kit-batch` or the existing module where it belongs (§5.1).

### 1.3 Invariants (each has a test, §17)

- **INV-1 One run object per attempt.** A run is one `batch/v1` Job or one Volcano Job, which executes one Spring Batch JobExecution. Pods never outlive their run object. Jobs carry `ttlSecondsAfterFinished`.
- **INV-2 Restart belongs to Spring Batch.** A retry creates a new JobExecution of the same JobInstance by re-supplying the same identifying parameters [S70]; COMPLETED steps are skipped [S67]. The scheduler only re-runs pods.
- **INV-3 Scheduler-agnostic contract.** No kit class and no template file names Volcano or kube-scheduler. Only the `BatchJob` composition decides `schedulerName`, the queue and the gang shape.
- **INV-4 One transaction for output and progress.** Business writes and JobRepository updates share one DataSource and one transaction manager by default. This is what fences zombie writers (§5.9). Non-transactional writers (S3) must be idempotent or use a staged commit.
- **INV-5 Batch never preempts.** Batch PriorityClasses carry `preemptionPolicy: Never` and a value ≤ 0 [S52]. A pending batch pod is a capacity question for queues and the autoscaler, never a priority question.
- **INV-6 Every operation is a Kubernetes API call under the caller's own RBAC.** Kyverno stamps the actor on what they create. No platform component reads a team's database.
- **INV-7 Definitions are GitOps; runs are not.** `BatchJob`s live in Git. Run objects are created by controllers or people and are never tracked by Argo CD.
- **INV-8 Local parity.** Every Phase-1 behaviour works in the local profile, where Kafka, Kargo, Alloy, Mimir and Volcano are all off (`adhar-appset-local.yaml`).

---

## 2. Starting point

### 2.1 What exists (verified 2026-11-03/04)

- **adhar-kit-batch 0.1.0** (Boot 4.1.0 parent, Spring Batch 6.0.4, Java 25): 24 main classes, 113 passing tests, 97.2% instruction coverage.
  - Classes: `BatchFacade`, `config.{BatchAutoConfiguration,BatchProperties}`, `scheduler.BatchScheduler`, `lock.{SchedulerLock,JdbcSchedulerLock}`, `operator.BatchOperator`, `metrics.{BatchMetrics,BatchJobStats}`, `listener.*` (3), `event.BatchJobFailedEvent`, `partitioner.AdharRangePartitioner`, `retry.{RetryableStepBuilder,RetryableStepBuilderFactory}`, `reader.*` (4), `writer.*` (3).
  - Paths: `adhar-kit-batch/src/main/java/com/adhar/kit/batch/**`.
- **Defects that block building on it** (detail in §19):

| Defect | What is wrong | Evidence |
|---|---|---|
| **K1** | `adhar.getBatch()` always throws. Nothing constructs `BatchFacade`. | `BatchFacade.java:62-67`, `AdharFacade.java:329-332` |
| **K2** | The auto-configuration declares no `after=`, so its `@ConditionalOnBean(DataSource\|MeterRegistry\|JobOperator)` beans are evaluated before Boot defines those beans [S74]. | `BatchAutoConfiguration.java:67-72` |
| **K3** | It is built on APIs deprecated for removal: `JobLauncher`/`TaskExecutorJobLauncher` (6.2+), `SimpleStepBuilder`/`FaultTolerantStepBuilder` (7.0), and the id-based `JobOperator` methods. | `BatchAutoConfiguration.java:185-204`; Spring Batch 6.0.4 `JobOperator.java:53-338` |
| **K4** | Nothing persists. The kit pulls only `spring-boot-starter-batch`, which is in-memory since Boot 4.0 [S71]. `table-prefix` is never read. | `adhar-kit-batch/pom.xml` |
| **K5** | Its listeners are never attached to a job or step. | |
| **K6** | The embedded cron can double-fire: the lock is released right after an *asynchronous* launch, and each fire uses `run.id = currentTimeMillis`, so it is a new JobInstance every time. | `BatchScheduler.java:116-139` |
| **K7** | Its metrics duplicate Spring Batch's own `spring.batch.*` meters. | |
| **K9** | `adhar-kit-starter` depends on `adhar-kit-batch` non-optionally, so every templated web service carries batch beans. | `adhar-kit-starter/pom.xml:151-155` |
| — | Job parameters are logged at INFO. | |

- **Spring Batch 6.0.4 already provides** what the design builds on:
  - `JobOperator.start(Job, JobParameters)`, `restart/stop/abandon(JobExecution)`, and `recover(JobExecution)` (6.0).
  - `ChunkOrientedStepBuilder` with `faultTolerant()`, `retryPolicy`, `skipPolicy` and `skipLimit`.
  - `JobExecutionShutdownHook` (defined but not registered).
  - `CommandLineJobOperator`.
  - Sources: `JobOperator.java:90,127,199,226,237`, `ChunkOrientedStepBuilder.java:280-370`, `JobExecutionShutdownHook.java`; [S61].
  - `recover` marks every running StepExecution (partitions included) FAILED, then the JobExecution FAILED, and is idempotent through `batch.recovered` (`SimpleJobOperator.java:417-457`).
- **Platform:**
  - No batch scheduler package. The README claims Armada (`README.md:218`); no Armada package exists.
  - No `batch` application type: the type enum is `[service, web, worker, data, ai, function]` (`xrd/apps.xrd.yaml:66`).
  - Kargo's verification would fail every batch app. `<app>-pods-ready` counts pods with `Ready=False`, and completed Job pods report `Ready=False/PodCompleted` (`compositions/project/local.yaml:795`; live).
  - The resilience gate requires a Rollout or Deployment (`local.yaml:828-860`).
  - The node autoscaler drains nodes running Job pods (`decision.go:667-685`) and buys nodes for any capacity-pending pod (`decision.go:477-526`).
  - kube-state-metrics exports no Kubernetes labels (no `--metric-labels-allowlist`; `observability/kube-prometheus/manifests/install.yaml:98440`) [S57].
- **Console:** no batch views. Develop → Functions is the pattern to copy:
  - `modules/develop/src/{home.tsx:12,25,69-77,95; data/functions*.ts}`
  - `packages/shell-ui/src/nav-tree.tsx:274`
- **CLI:** no batch command. `cmd/function/function.go` is the pattern to copy (registered at `cmd/main.go:124`).
- **Templates:** 17, none for batch. The CLI renderer still has no `{% for %}` (`cmd/apps/scaffold.go:404,685`).

### 2.2 The live cluster (2026-11-03)

| Item | Value |
|---|---|
| Nodes | 1 control plane + 10 workers, DigitalOcean `s-8vcpu-16gb`, Kubernetes v1.37.2, Cilium 1.20.0 |
| Worker allocatable | 75 CPU, 121.2 GiB (7.5 CPU / 12,408 MiB per node) |
| Worker requests | ≈21–22 CPU; **86.9 GiB memory (71.7%)**; per node 49–98% |
| Requestable headroom | **≈34 GiB, ≈53 CPU**; autoscaler `minWorkers 3`, `maxWorkers 10`, **at 10** |
| Control plane | memory **97%** (`kube-apiserver` 9,022 MiB); 354 CRDs (16.4 MB JSON) |
| Scheduling APIs | `scheduling.k8s.io/v1` only (the 1.37 beta Workload/PodGroup API is off) |
| PriorityClasses | `adhar-node-bound` 900000, `adhar-critical-path` 800000, `kpack-control-plane` 10000, `kpack-build-high-priority` 1000, `kpack-build-low-priority` 1 (Never). **No batch class.** |
| Environment namespaces | LimitRange default 500m/512Mi (request 100m/128Mi); ResourceQuota 16 CPU / 32 GiB / 100 pods; ingress-only default deny |
| Relevant installed | CNPG 1.30.0 (PG 18.4) · Strimzi 1.2.0 / Kafka 4.3.1 with `KafkaBridge/adhar-events` · Kyverno 1.19.0 · Argo CD v3.5.1 · Kargo v1.11.4 · KEDA 2.20.2 · Crossplane v2.4.2 · KSM v2.20.0 · Karmada hub v1.19.0 with **0 DataPlanes** · RustFS · Alloy/Mimir/Loki/Tempo (production profile) |
| Batch-relevant facts | CronJob-created Jobs carry `batch.kubernetes.io/cronjob-scheduled-timestamp`. The RustFS root credential Secret `root-creds` exists in `dev`, `test` and `prod`. |

**Memory decides everything.** Whatever the scheduler costs, and whatever batch it admits, comes out of ≈34 GiB on a cluster that cannot grow further.

### 2.3 Enterprise feature coverage (where each capability lands)

| Capability | Mechanism | Owner | Phase |
|---|---|---|---|
| Definitions as code, review, promotion | `BatchJob` XR in Git; PR; Kargo freight | platform | 1 |
| Restart from last commit, history | JDBC JobRepository; same-parameter restart; `recover` | kit | 0–1 |
| Idempotent launch | Declared identifying parameters; deterministic run names; `Forbid` | kit + platform | 1 |
| Cron with time zone, misfire window, concurrency | CronJob `timeZone`, `startingDeadlineSeconds`, `concurrencyPolicy` [S50] | platform | 1 |
| Hold / release a schedule | Annotation on the `BatchJob` | platform + console + CLI | 1 |
| Fault tolerance: retry, skip, dead-letter | Framework-7 `RetryPolicy` presets; `SkipPolicy`; JDBC DLQ | kit | 1 |
| Pod-level retry policy | `podFailurePolicy` keyed on the kit's exit codes [S49] | platform | 1 |
| Parallelism in one pod | Multi-threaded steps, local partitioning | kit | 1 |
| Distributed partitioning | Remote partitioning on a gang (Volcano) | kit + platform | 2 |
| Queues, fair share, team isolation | Volcano leaf queues `<org>-<team>-<env>` | platform | 2 |
| SLA | `maxDuration` alerts (1); `finishBy` with early warning (3) | platform | 1 / 3 |
| Calendars, cross-job dependencies, backfill | Calendar ConfigMaps; `dependsOn` in the observer; `adhar batch backfill` | platform + kit + CLI | 3 |
| DLQ replay, retention, abandon/recover | Kit housekeeping and ops jobs | kit + CLI | 3 |
| Audit | Kubernetes audit + Kyverno actor stamping; `adhar.batch` CloudEvents, 90-day retention | platform | 1 / 3 |
| Notifications | Alertmanager → Grafana OnCall; business notifications via `adhar-kit-notification` | platform + kit | 1 / 3 |
| Cost attribution | Hierarchy labels on every run pod; OpenCost aggregation [S94] | platform + console | 1 / 3 |
| Forecast ("what runs tonight") | Next schedule plus duration history | console | 3 |
| Remote chunking, lakehouse I/O, lineage, fleet placement | RabbitMQ; Iceberg; OpenLineage; Karmada | kit + platform | 4 |

Enterprise schedulers lead with calendars, SLA management, resources/locks and conditions (Control-M [S95], AutoSys [S96]), and with backfill and run versioning (Airflow 3 [S97]). Spring Batch supplies the in-job semantics. Kubernetes supplies execution, retry policy, cron and suspension [S49][S50]. Adhar adds the rest as thin, GitOps-native layers.

---

## 3. Architecture

### 3.1 Components and ownership

| Component | Repository / path | What it owns | Phase |
|---|---|---|---|
| **adhar-kit-batch** (extended) | `adhar-kit/adhar-kit-batch` | Job declaration, the Kubernetes runner, identity, recovery, graceful stop, exit codes, JobRepository schema, step and fault-tolerance API, I/O, partitioning (local and remote), telemetry, in-JVM run events, embedded scheduler | 0–4 |
| kit neighbours | `adhar-kit-metrics` (OTLP push), `adhar-kit-test-commons` (`@AdharBatchTest`), `adhar-kit-logging` (MDC), `adhar-kit-kubernetes` (run launcher, P3), `adhar-kit-starter` (stop pulling batch) | | 0–3 |
| **`core/adhar-batch` package** (new) | `platform/stack/packages/core/adhar-batch/` | PriorityClasses, aggregated ClusterRoles, Kyverno batch policies, Grafana dashboard JSON | 1 |
| **`BatchJob` XRD and composition** (new) | `platform/controlplane/configuration/{xrd/batchjob.xrd.yaml, compositions/batchjob/kubernetes.yaml}` | Turns a job definition into CronJob / Volcano CronJob, run template, RBAC, alerts | 1–2 |
| Application `type: batch` | `xrd/apps.xrd.yaml`, `compositions/apps/argocd-multienv.yaml`, `compositions/project/local.yaml` | Delivery chain, dashboard, Kargo verification for batch | 1 |
| **`core/volcano` package** (new) | `platform/stack/packages/core/volcano/` | Pinned Volcano, static webhooks, narrowed RBAC, scheduler config, root queue, metrics | 2 |
| Team composition (extended) | `compositions/team/local.yaml` | Leaf queues and queue read RBAC per team | 2 |
| Node autoscaler (extended) | `platform/controllers/autoscaler/` | Drain safety for runs; batch scale-up policy; Volcano gang demand | 1–2 |
| **Batch observer** (new controller) | `platform/controllers/batch/` | `adhar.batch` CloudEvents, lost-run detection, `dependsOn`, SLA evaluation | 3 |
| **`adhar-batch-steps`** ClusterWorkflowTemplate (new) | Shipped inside design 0026's step catalogue in the `argo-workflows` package (so it never syncs without its CRD); contract and tests owned here | The `run` step that design 0026's workflows use to start a `BatchJob` run and wait for it (§12.1) | 3 |
| Event taxonomy (extended) | `platform/events/{taxonomy.go,CONTRACT.md}`, `data/kafka-operator/manifests/events-bus.yaml` | Topic `adhar.batch` | 3 |
| Argo CD config | `hack/argocd/values*.yaml` → `platform/controllers/adharplatform/resources/argocd/install*.yaml` | Health checks for `BatchJob` and Volcano kinds; exclusions | 1–2 |
| **CLI `adhar batch`** (new) | `cmd/batch/` | list, describe, runs, run, logs, stop, resume, restart, hold, release (1); queues (2); backfill, abandon, recover, dlq, history (3) | 1–3 |
| **Console Batch area** (new) | `adhar-console/modules/develop/src/{views/batch*.tsx,data/batch*.ts}` (+ Platform queues view) | Jobs, runs, run detail, actions; queues; history and the tonight board | 1–3 |
| **Templates** (new) | `adhar-templates/templates/{springboot-batch-job,springboot-batch-partitioned}` | Golden paths | 1–2 |

### 3.2 Data flow

```
 app repo (Gitea)                         Kargo: kustomize-set-image (digest) on deploy/envs/<env>,
 deploy/base/batchjob.yaml  ───────────►  rendered to branch stage/<env>  ──► Argo CD Application
 deploy/base/database.yaml                                                   <project>-<app>-<env>
 deploy/envs/{dev,test,prod}                                                        │ SSA
                                                                                     ▼
 ┌─ environment namespace <project>-<env> ───────────────────────────────────────────────────────────┐
 │ BatchJob <name> ──Crossplane: batchjob-kubernetes──► CronJob <name>      (or Volcano CronJob, P2)    │
 │      ▲ hold annotation (operators)                   ConfigMap <name>-run (the run template)       │
 │      │                                               ServiceAccount/Role/RoleBinding <name>-batch  │
 │      │                                               PrometheusRule <name>-batch                    │
 │      │                                                                                             │
 │  CronJob controller ──┐                                                                            │
 │  Console / CLI (user) ├──► run: Job <name>-…  (or Volcano Job, P2) ──► pod(s): app image + kit runner│
 │  batch observer (P3) ─┘    Kyverno: integrity check + actor stamp            │                     │
 │                                                                              ├─► JobRepository: schema batch in <app>-db (CNPG)
 │                                                                              ├─► OTLP :4318 → Alloy → Mimir / Tempo; stdout JSON → Loki
 │                                                                              └─► /dev/termination-log: run summary (≤3.5 KB JSON)
 └────────────────────────────────────────────────────────────────────────────────────────────────────┘
   placement: kube-scheduler (Phase 1)  |  Volcano, queue <org>-<team>-<env> under adhar-batch (Phase 2)
   watchers:  kube-state-metrics → Prometheus alerts;  batch observer (P3) → adhar.batch CloudEvents, dependsOn, SLA
```

### 3.3 Lifecycle of a scheduled run

1. **The CronJob controller creates `Job <name>-<n>`** with the `batch.kubernetes.io/cronjob-scheduled-timestamp` annotation (since v1.32) [S51]. Time zone and misfire window come from the CronJob [S50].
2. **Admission.** Kyverno stamps `adhar.io/batch-actor` and admits the pod against the batch pod standards. The supply-chain policy verifies the image signature and pins the digest.
3. **Placement.** kube-scheduler places the pod. With Volcano, the pod goes through the leaf queue instead.
4. **The kit runner starts** (§5.5). It:
   - reads its owner Job with one `GET`;
   - derives the business date from the scheduled timestamp in the job's time zone;
   - resolves and validates the declared parameters;
   - looks up the JobInstance.
5. **The runner decides:**

   | JobInstance state | Action |
   |---|---|
   | None | Start |
   | FAILED / STOPPED | Restart |
   | STARTED, owner dead | Recover, then restart |
   | STARTED, owner alive | Exit 0 "duplicate" |
   | COMPLETED | Exit 0 "already complete" |
6. **During the run**, the runner holds a lease row and heartbeats it every 30 s. Each chunk commits its business writes and its step context in one transaction.
7. **At the end**, the runner maps the outcome to an exit code (§5.7), writes the run summary to `/dev/termination-log` (the kubelet keeps ≤4096 bytes) [S53], and the JVM exits.
8. **Afterwards:**
   - The Job controller applies `podFailurePolicy` [S49].
   - kube-state-metrics publishes the Job's state; the composed PrometheusRule alerts on failure or staleness.
   - In Phase 3, the observer emits `io.adhar.batch.run.*`.

### 3.4 Lifecycle of manual operations

| Action | Mechanism |
|---|---|
| **Run now** | The client reads `ConfigMap <name>-run`, builds `ADHAR_BATCH_PARAMS` from the declared schema, names the run `<name>-<hash8>` (§8.3) and creates it as the user. `AlreadyExists` means a run with these parameters already exists, and the client shows it. Kyverno admits the run only if it matches the template (§10.3). |
| **Stop** | Native: patch `spec.suspend: true`. Running pods get SIGTERM with their grace period, and do not count towards completions [S49]. Volcano: create a `bus.volcano.sh` Command with action `AbortJob`, which is what `vcctl job suspend` issues [S30]. Either way, the kit's graceful-stop lifecycle stops the step at the next chunk boundary and records STOPPED. |
| **Resume** | Native: `spec.suspend: false` (the Job's `startTime` and active-deadline timer reset [S49]). Volcano: Command `ResumeJob` [S13]. The new pod finds STOPPED and restarts. |
| **Restart a failed run** | Create `<name>-<hash8>-r<n>` with the same identifying parameters and `ADHAR_BATCH_TRIGGER=restart`. Spring Batch re-executes only the failed steps [S67]. |
| **Hold / Release** | Set or remove the annotation `adhar.io/batch-hold: "<reason>"` on the `BatchJob`. The composition sets the CronJob's `suspend`. Argo CD does not revert it, because the annotation is not in Git and Argo CD compares only declared fields under server-side apply (verified in test PLAT-1.6c). |

### 3.5 Who writes what (no field has two writers)

| Object / field | Writer | Readers |
|---|---|---|
| `BatchJob.spec` | Git → Argo CD (SSA) | Crossplane, Console, CLI |
| `BatchJob` annotation `adhar.io/batch-hold` | Operators (Console/CLI) | Composition. Kyverno allows operators to change only this. |
| `BatchJob.status` | Composition | Console, CLI, Argo CD health |
| CronJob, Volcano CronJob, `<name>-run` ConfigMap, SA/Role/RoleBinding, PrometheusRule | Crossplane | Controllers, Kyverno, Console, CLI |
| Run Jobs / Volcano Jobs | CronJob controllers; users; observer (`dependsOn`); Kargo analysis (smoke run) | Job controller, Volcano, KSM, Console, CLI, observer |
| `/dev/termination-log` | Kit runner | Console, CLI, observer |
| JobRepository (schema `batch`, application database) | Kit runner, kit ops jobs | Kit only. **No platform component reads a team's database.** |
| Root queue `adhar-batch` | Git (`core/volcano`) | Volcano |
| Leaf queues `<org>-<team>-<env>` | Team composition | Volcano, Console (`get` by name) |
| PodGroups | Volcano | Autoscaler, Console |
| `adhar.batch` events | Batch observer | Console history, CLI, consumers |

---

## 4. Scheduler integration (Volcano)

ADR-0028 records the decision and its evidence. This section is the engineering.

### 4.1 Package `core/volcano` (Phase 2)

| Item | Decision |
|---|---|
| Version | Helm chart `volcano` **v1.16.x**, exact patch and image tags pinned (the chart defaults to `latest` [S8]). Production gate: **v1.17** [S7] or the 1.37 canary suite passing (§15, Phase 2). |
| Namespace | `adhar-system` (ADR-0011), via the chart's per-component namespace overrides [S8]. No `kind: Namespace`. |
| Components | `volcano-scheduler`, `volcano-controllers`, `volcano-admission`. **Not installed:** the JobFlow/JobTemplate subchart, `volcano-agent` (colocation; needs BPF/SETFCAP capabilities and reads the CNI config path, beside Cilium's datapath [S8]), the dashboard (its ServiceAccount can patch the scheduler ConfigMap [S101]) and Volcano Global [S23]. |
| Resources *(estimate; the chart sets none [S11])* | Scheduler 200m/512Mi (limit 1Gi); controllers 100m/256Mi (limit 512Mi); admission 2 × 50m/128Mi (limit 256Mi). **≈0.4 CPU / ≈1 GiB in total.** Measure in the canary and record. |
| CRDs | Job, CronJob, Queue, PodGroup, Command, HyperNode and the rest of the core set. JobFlow (289 KB) and JobTemplate (225 KB) are excluded, which saves ≈0.5 MB of the ≈1.09 MB set [S10]. |
| Webhooks | Rendered **statically** in `generate-manifests.sh`. A cert-manager `Certificate` plus CA injection replaces the admission-init Job. The upstream `namespaceSelector` excludes the release namespace and `kube-system`, and Volcano kinds are `failurePolicy: Fail` [S9]. Keep both: batch runs in workload namespaces. |
| RBAC | Narrow the admission ClusterRole. Upstream it can create/update every webhook configuration and approve CSRs [S11]. Replace with `get` plus `update` restricted by `resourceNames` to Volcano's own configurations, and no CSR rules. A guard test asserts the narrowed rules. |
| One-namespace hygiene | `enableServiceLinks: false` on every Deployment; `CONFLICTS.md` name scan; no stale `volcano-system` references. |
| Scheduler config | ConfigMap `volcano-scheduler-configmap` (§4.2). |
| Root queue | `Queue adhar-batch` (cluster-scoped) from `root-queue.yaml.tmpl`, with capability rendered at seed time from `globalSettings.batch.capacity`. The default is 30% of the CPU and 20% of the memory of `maxWorkers × worker allocatable`, as computed by `platform/providers/sizing.go` *(decision; §14)*. |
| Descriptor | `ConfigMap adhar-batch-scheduler` in `adhar-system` (`scheduler: volcano`, `version`, `gang: "true"`). The `BatchJob` composition reads it as an extra resource. Absent means the native path. |
| Metrics | ServiceMonitors for the scheduler and controllers. Useful series [S22]: `volcano_e2e_job_scheduling_latency_milliseconds`, `volcano_queue_{allocated,deserved,capability,inqueue}_*`, `volcano_unschedule_job_counts`, `volcano_eviction_transactions_total`. |
| Contract | `adhar-package.yaml`: category `core`, `planeAffinity: data-plane` (it schedules workload pods; ADR-0023), `stability: alpha` → `beta` after the canary, `dependencies: [cert-manager]`. |
| Profiles | Wired in the local, production and gitops ApplicationSets plus `environments/{local,prod}/config.yaml`. `enabled: "false"` everywhere until the production gate; then production `true`. **Local stays opt-in** (memory), and the native path covers it. |
| Optional | The Headlamp Volcano plugin [S26] in the `headlamp` package (P2-nice). |

### 4.2 Scheduler configuration

```yaml
actions: "enqueue, allocate, backfill, reclaim, preempt"
tiers:
  - plugins:
      - name: priority
      - name: gang
      - name: conformance
  - plugins:
      - name: drf
      - name: predicates
      - name: capacity          # hierarchical queues: only leaves schedule [S16]
        enableHierarchy: true
      - name: nodeorder
      - name: binpack
```

These are the stable core from ADR-0028. **Deferred:** `NamespaceQueue` and `fairshare` (new in v1.16; NamespaceQueue is alpha and off [S2][S18]), `gangPreempt`/`gangReclaim` (alpha since v1.15 [S3]), JobFlow, sharding and the agent. Exact plugin argument names are verified in the canary before the package leaves alpha.

### 4.3 Queues per team and environment

```
root
└── adhar-batch                      capability = batch ceiling (seed-rendered); deserved = capability
    ├── acme-payments-dev            capability ≤ min(team allowance, ceiling); deserved 0; reclaimable: true;  priority 100
    ├── acme-payments-test           same as dev;                                                              priority 200
    ├── acme-payments-prod           deserved = batch.prodDeserved (default 2 CPU / 4 GiB); reclaimable: false; priority 300
    └── <org>-<team>-<env> …
```

The fields are Volcano's own Queue fields: `capability`, `deserved`, `guarantee`, `reclaimable`, `priority`, `weight` and `parent` [S14].

- **Leaves are team × tier**, named `<org>-<team>-<env>`, where `<org>-<team>` is the Team XR's name (ADR-0026).
  - The three tiers are fixed (`dev | test | prod`).
  - All projects of a team share the team's leaf for a tier.
  - A project-level leaf would make two Project XRs compose the same cluster-scoped object.
- **No team level in the hierarchy** *(decision)*. The team ceiling is already enforced at admission by `adhar-tenant-quotas` (`security/adhar-tenant-quotas/manifests/allowances.yaml`). A team node would require every Team XR to coordinate `deserved` sums, which the queue webhook enforces against the parent [S16].
- **The Team composition emits the three leaves** as provider-kubernetes `Object`s (a namespaced XR cannot compose cluster-scoped objects directly):
  - Capability is the smaller of the team's `adhar-tenant-quotas` allowance (read as an extra resource) and the batch ceiling.
  - Only `prod` declares a `deserved`. That is what lets prod **reclaim** from reclaimable dev/test leaves [S15].
  - It also emits a ClusterRole/ClusterRoleBinding granting the team group `get` on its three queues by `resourceNames`.
  - Status condition `BatchQueuesReady`. If the ceiling cannot admit another prod `deserved` share, the webhook's refusal is reported as `QueueRejected`, with the message.
- **Binding namespace → queue.** Kyverno, not Volcano: job admission only checks that the queue is a valid leaf [S29], and `NamespaceQueue` is alpha and off [S2]. Policy `adhar-batch-queue-binding` (§10.3):
  - **mutates** the `scheduling.volcano.sh/queue-name` pod annotation [S19] and `spec.queue` to `<org>-<team>-<env>` from the namespace labels;
  - **denies** any other value.
- **`default` queue.** Volcano's own; untouched. The binding policy keeps workload namespaces off it.

### 4.4 Priorities and preemption

| PriorityClass | Value | `preemptionPolicy` | Used by | Can be preempted by |
|---|---|---|---|---|
| `adhar-batch` (new) | −100 | Never | Every batch run by default | Any default-priority (0) service and every platform class |
| `adhar-batch-critical` (new) | 0 | Never | Prod runs with `priority: critical` (Kyverno: prod namespaces only) | Only platform classes (`adhar-critical-path` 800000, `adhar-node-bound` 900000) |

- **Negative values and `Never` are both supported** [S52]. Batch therefore never evicts a service or a node-pinned stateful pod, which matters because most stateful pods sit on node-local volumes and a preempted one can only return to its own node.
- **Between batch jobs**, Volcano decides:
  - `preempt` works within a queue by priority;
  - `reclaim` returns over-share capacity from reclaimable dev/test leaves to prod [S15].
  - A preempted or reclaimed run receives SIGTERM, stops gracefully as STOPPED, and is restarted (§4.5, §5.9).
- **Canary test (A2.7):** Volcano never evicts a pod it did not schedule.

### 4.5 Gang scheduling for partitioned jobs

A `BatchJob` with `mode: partitioned` and `workers: N` renders a **Volcano Job** (vcjob) [S12]. The template below is what a Volcano CronJob (a CRD since v1.13 [S4]) stamps out:

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  generateName: settlement-            # the CronJob or the client names it (§8.3)
  labels: { adhar.io/batch-job: settlement, adhar.io/workload: batch, adhar.io/batch-trigger: schedule }
spec:
  schedulerName: volcano
  queue: acme-payments-prod            # mutated/validated by adhar-batch-queue-binding
  priorityClassName: adhar-batch-critical
  minAvailable: 5                      # 1 manager + 4 workers, all-or-nothing [S12]
  maxRetry: 3
  ttlSecondsAfterFinished: 604800
  plugins: { env: [] }                 # VC_TASK_INDEX for logs [S21]; no svc plugin: pods talk via the JobRepository
  policies:                            # exit-code rules first; first matching policy applies (verified in canary)
    - { exitCode: 2, action: TerminateJob }
    - { exitCode: 3, action: TerminateJob }
    - { exitCode: 4, action: TerminateJob }
    - { exitCode: 6, action: TerminateJob }
    - { exitCode: 7, action: TerminateJob }
    - { events: [PodEvicted, PodFailed], action: RestartJob }   # whole gang; Spring Batch restarts only unfinished partitions
  tasks:
    - name: manager
      replicas: 1
      policies: [ { event: TaskCompleted, action: CompleteJob } ]  # manager done → workers ended
      template: { … pod spec as §6.4, env ADHAR_BATCH_ROLE=manager … }
    - name: worker
      replicas: 4
      template: { … ADHAR_BATCH_ROLE=worker, resources from workerCpu/workerMemory … }
```

- **All-or-nothing.** The vcjob's pods are not created until its PodGroup leaves Pending [S20]. The gang therefore never half-allocates, and never holds quota while its workers wait.
- **Restart is Spring Batch's** (INV-2). `RestartJob` replaces every pod. The new manager finds the JobExecution STOPPED, or STARTED with a dead lease, recovers it if needed, and restarts. Only partitions that did not complete run again [S67]. `recover` marks the old partitions FAILED (`SimpleJobOperator.java:441-449`).
- **Workers are interchangeable consumers** of partition requests (§5.10). The gang size and the partition count are independent: `gridSize` defaults to `4 × workers`.
- **Native fallback** (no Volcano, or `scheduler: kubernetes`) renders one **Indexed Job**:
  - `completions` and `parallelism` = N+1.
  - Index 0 is the manager, resolved from the downward-API `batch.kubernetes.io/job-completion-index` annotation.
  - `successPolicy.rules: [{succeededIndexes: "0"}]`. After the success policy is met, the Job controller terminates the lingering worker pods [S49].
  - The same `podFailurePolicy` as single runs.
  - This keeps the manager-ends-workers lifecycle **without** the gang. A worker index that fails is replaced and keeps consuming. A manager that fails is retried, within the backoff limit, and re-dispatches unfinished partitions.

### 4.6 Version policy and the Kubernetes 1.37 gap

- **Where things stand:**
  - v1.16.0 shipped on 2026-10-14 [S1]. Volcano's compatibility matrix stops at 1.36 [S5], and v1.16 builds against the 1.36.1 libraries [S98].
  - PR #6129 (1.37) has been open since 2026-10-20; its author reports e2e passing on kind v1.37.0 [S6].
  - v1.17 is estimated for 2027-01-30 [S7].
  - The live cluster runs 1.37.2.
- **Phase 2 plan:**
  - Ship v1.16.x **disabled by default**.
  - Run the batch e2e suite (§17) on Kind v1.37 with Volcano enabled, in CI, on every change to the package.
  - Avoid scheduling features that depend on 1.37-only pod fields.
- **Production gate:** enable in the production profile **only** when v1.17 is out and green in the canary, or when a maintainer explicitly signs off on a v1.16 patch with the canary green.
- **Until then**, production batch runs on the native path. The fallback is the Phase-1 product, not an emergency mode.

### 4.7 The node autoscaler

| Phase | Rule (in `platform/controllers/autoscaler/decision.go`) |
|---|---|
| 1 | `unsafeToDrain` (667) also refuses a node holding a pod annotated `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`. That annotation is upstream's convention, which the autoscaler does not yet honour. The composition sets it on every run pod unless `disruption: allow`. |
| 1 | `pendingForCapacity` (477) skips pods labelled `adhar.io/workload: batch` unless they carry `adhar.io/scale-up: "true"`. The composition sets that only for `scaleUp: true`, which Kyverno permits only in prod. A dev/test backlog never buys a node. |
| 2 | `pendingForCapacity` also skips pods with `schedulerName: volcano`; their demand is read from PodGroups instead. Each tick, the autoscaler **LISTs** `podgroups.scheduling.volcano.sh` with a plain client, after a discovery check. It never uses an informer on a CRD that may be absent: an informer cache-sync timeout has killed the manager before. |
| 2 | Scale up for a PodGroup in Pending/Inqueue, with an unschedulable condition, whose owner vcjob is opted in, and whose queue is below `capability`. Request `ceil(minResources / worker allocatable)` nodes, bounded by `maxWorkers`. A queue at `capability` never scales. |

---

## 5. `adhar-kit-batch` — the Java API (extending the existing module)

### 5.1 Module decision

**No new module.** Every gap maps onto an existing module:

| Concern | Module |
|---|---|
| Runner, repository, steps, fault tolerance, I/O, partitioning, telemetry wiring, events | `adhar-kit-batch` |
| Generic OTLP push | `adhar-kit-metrics` |
| Test support | `adhar-kit-test-commons` |
| MDC and BATCH log events | `adhar-kit-logging` (`com.adhar.adharkit.logging.batch.BatchJobLogger`) |
| Launching a run from a Spring service (P3) | `adhar-kit-kubernetes` (already carries fabric8) |

Heavy integrations stay **optional** dependencies of `adhar-kit-batch`, which is how other kit modules carry optional integrations.

`adhar-kit-batch` dependencies:

| Scope | Dependencies |
|---|---|
| **Compile** | `adhar-kit-commons`, `spring-boot-starter-batch-jdbc` (JDBC store; exists since Boot 4.0.0 [S76][S69]), `flyway-core`, `flyway-database-postgresql` |
| **Optional** | `spring-batch-integration` + `spring-integration-jdbc` (remote partitioning), `spring-cloud-aws-starter-s3` 4.x (S3/RustFS I/O; spike first — 4.2.1 targets Boot 4 [S85]), `micrometer-core`, `micrometer-tracing`, `jackson-databind` (Jackson 3), `jakarta.validation-api`, `jakarta.persistence-api` |

`adhar-kit-starter` changes `adhar-kit-batch` to `<optional>true</optional>`. Batch templates depend on it explicitly (K9).

**Versions:**

| Kit release | Phase | Content |
|---|---|---|
| 0.2.0 | 0 | Defect fixes |
| 0.3.0 | 1 | Runner |
| 0.4.0 | 2 | Remote partitioning |

A released kit version is immutable in Nexus, so every change bumps the version and the templates' pins.

### 5.2 Package layout

| Package (`com.adhar.kit.batch…`) | Status | Contents |
|---|---|---|
| (root) | changed / new | `BatchFacade` (self-initialising, K1); **new** `AdharBatchApplication` |
| `annotation` | new | `@AdharBatchJob`, `@JobParam`, `ParamType`, `ParamSource` |
| `params` | new | `JobParameterSpec`, `JobParameterSpecRegistry`, `JobParametersResolver`, `ParameterValidationException`, `SecretParameterGuard`, `RunIdentity` |
| `runtime` | new | `AdharBatchRunner` (`ApplicationRunner`), `RunContext` (record), `RunContextResolver`, `KubernetesOwnerLookup`, `RunTrigger`, `BatchRole`, `ExitCodes`, `BatchExitCodeGenerator`, `ExecutionLease`, `JdbcExecutionLease`, `RecoveryPolicy`, `GracefulStopLifecycle`, `TerminationSummaryWriter`, `RunOutcome` |
| `repository` | new | `AdharBatchRepositoryConfiguration`, `BatchSchemaMigrator`, `ResourcelessRepositoryGuard` |
| `step` | new | `AdharSteps`, `AdharChunkStepBuilder` (over `ChunkOrientedStepBuilder`) |
| `fault` | new | `FaultTolerance`, `RetryPresets`, `SkipPresets`, `NonRetryableBatchException`, `DeadLetterSink`, `JdbcDeadLetterSink`, `DeadLetterSkipListener` |
| `reader` / `writer` | extended | **new:** `S3ItemReaderBuilder`, `KeysetPagingItemReaderBuilder`, `JsonLinesItemReaderBuilder`, `S3ItemWriterBuilder`, `PostgresUpsertItemWriterBuilder`, `JsonLinesItemWriterBuilder`; page/chunk defaults from properties |
| `partitioner` | extended | `AdharRangePartitioner` + **new** `SqlKeyRangePartitioner`, `ObjectPrefixPartitioner` |
| `partition` | new | `LocalPartitioning` (P1); `RemotePartitioningAutoConfiguration`, `JdbcPartitionTransport`, `StaleRequestGuard`, `WorkerStopHandler` (P2) |
| `telemetry` | new | `BatchTelemetryAutoConfiguration`, `ItemObservationTracingFilter`, `BatchProgressMetrics`, `BatchMdc` |
| `event` | extended | `BatchJobFailedEvent` kept; **new** sealed `BatchRunEvent` (`Started`, `Completed`, `Failed`, `Stopped`, `Skipped`, `Recovered`) |
| `operator` | migrated | `BatchOperator`: `JobExecution`-based `restart/stop/abandon`, **`recover`**, `findExecutions(job, params)`; the long-id methods are deprecated and delegate |
| `scheduler` / `lock` | fixed | `BatchScheduler` (embedded mode, §5.16); `SchedulerLock.renew`; `JdbcSchedulerLock` without runtime DDL |
| `retry` | deprecated | `RetryableStepBuilder*` delegate to `AdharSteps`; removed in 0.4.0 |
| `ops` (P3) | new | `BatchCommandRunner`: start, restart, stop, abandon, recover, status, housekeeping, dlq-replay |
| `config` | changed | `BatchAutoConfiguration` (ordered, gated); `BatchProperties` (extended); **new** `BatchRuntimeEnvironmentPostProcessor` |

### 5.3 Declaring a job

```java
@Configuration
class ImportJobConfiguration {

    @Bean
    @AdharBatchJob(
        description = "Imports inbound records into Postgres",
        parameters = {
            @JobParam(name = "businessDate", type = ParamType.DATE, source = ParamSource.SCHEDULE),
            @JobParam(name = "source", type = ParamType.STRING, defaultValue = "inbound/records.csv",
                      pattern = "^[a-zA-Z0-9/._-]+$"),
            @JobParam(name = "dryRun", type = ParamType.BOOLEAN, identifying = false, defaultValue = "false")
        })
    Job importJob(JobRepository jobRepository, Step importStep) {
        return new JobBuilder("importJob", jobRepository).start(importStep).build();   // no incrementer (§5.6)
    }

    @Bean
    Step importStep(AdharSteps steps, ItemReader<InputRecord> reader,
                    RecordProcessor processor, ItemWriter<Record> writer) {
        return steps.<InputRecord, Record>chunk("importStep")       // size from adhar.batch.chunk.size
            .reader(reader).processor(processor).writer(writer)
            .faultTolerance(FaultTolerance.transientInfrastructure()          // retry presets
                .skip(ValidationException.class, 100)                         // SkipPolicy + limit
                .deadLetter())                                                // JdbcDeadLetterSink
            .build();
    }
}
```

```java
@Target({ElementType.METHOD, ElementType.TYPE}) @Retention(RetentionPolicy.RUNTIME)
public @interface AdharBatchJob {
    String name() default "";              // default: the bean name
    String description() default "";
    JobParam[] parameters() default {};
    boolean allowIncrementer() default false;   // housekeeping jobs only
}
public @interface JobParam {
    String name(); ParamType type() default ParamType.STRING;
    boolean identifying() default true; boolean required() default false;
    String defaultValue() default ""; String pattern() default ""; String[] allowed() default {};
    ParamSource source() default ParamSource.INPUT;   // INPUT | SCHEDULE (business date from the nominal time)
    boolean sensitive() default false;    // never logged or emitted; secret-like NAMES are refused anyway
    String description() default "";
}
public enum ParamType { STRING, LONG, DOUBLE, BOOLEAN, DATE, DATETIME }
```

- **One registry, two consumers.** `JobParameterSpecRegistry` collects the declarations from bean-method metadata. The runner validates against it at run time (it is authoritative). `BatchContractAssertions` (test-commons) fails the build when `deploy/base/batchjob.yaml` declares different parameters (§9.1).
- **A programmatic `JobParameterSpec` bean** is accepted for jobs whose parameters are computed.
- **When Spring Batch 6.1 reaches GA** (RC1 shipped with Boot 4.2.0-RC1 on 2026-10-22 [S64]), the runner also feeds 6.1's `@JobParameter` injection [S63].

### 5.4 Steps and fault tolerance

- **`AdharSteps.chunk(name)`** returns `AdharChunkStepBuilder`. It wraps Spring Batch 6's `new StepBuilder(name, repo).<I,O>chunk(n)` → `ChunkOrientedStepBuilder` (`StepBuilder.java:113`), sets the platform transaction manager, attaches the kit listeners (K5), and applies presets:
  - **Retry presets** use Spring Framework 7 core retry (`ChunkOrientedStepBuilder.retryPolicy/retry/retryLimit`, `:292-324`):

    | Preset | Retries on |
    |---|---|
    | `transientInfrastructure()` | `TransientDataAccessException`, `CannotGetJdbcConnectionException`, `SocketTimeoutException`, S3 5xx/throttling |
    | `transientHttp()` | HTTP failures (the same retry semantics) |

    All presets back off exponentially (1 s × 2, capped at 30 s, 3 attempts by default).
  - **Skip presets:** `SkipPolicy` with a limit (`:338-370`). `NonRetryableBatchException` and its subclasses are never retried.
  - **Dead letter:** `DeadLetterSkipListener` writes the item JSON, the exception class and message, step, execution id and business date to `batch.ADHAR_BATCH_DEAD_LETTER`. `JdbcDeadLetterSink` is the default; S3, Kafka and RabbitMQ sinks come in P3.
- **Classification for exit codes** (§5.7):

  | Outcome | Exit code |
  |---|---|
  | A step fails with `NonRetryableBatchException`, `SkipLimitExceededException` or a control-totals mismatch (P3) | **3** |
  | Any other failure | **5** |

### 5.5 The Kubernetes runner

**Activation.** `adhar.batch.mode=auto` (the default) becomes `kubernetes` when `ADHAR_BATCH_JOB` is set. The composition always sets it. `BatchRuntimeEnvironmentPostProcessor` then sets:

| Property | Value | Why |
|---|---|---|
| `spring.batch.job.enabled` | `false` | Replaces Boot's `JobLauncherApplicationRunner` [S69] |
| `spring.main.web-application-type` | `none` | |
| `spring.lifecycle.timeout-per-shutdown-phase` | `adhar.batch.shutdown.timeout` | [S75] |
| `spring.main.cloud-platform` | `none` | No kit-kubernetes informers in a run-to-completion pod |

An application's `main` calls `AdharBatchApplication.run(App.class, args)`, which does `System.exit(SpringApplication.exit(ctx))`. `BatchExitCodeGenerator` (`@Order(HIGHEST_PRECEDENCE)`) decides the code [S84].

```text
AdharBatchRunner.run():
  guard: JobRepository is JDBC, else exit 2 ("resourceless repository in a Kubernetes run")
  ctx  = RunContextResolver.resolve()     # env + downward API; ONE GET of the owner Job (scheduled timestamp)
                                          # and of the own pod (image digest); role; trigger; actor
  job  = registry.job(ctx.jobName)        # unknown → exit 2;  incrementer && !allowIncrementer → exit 2
  params = resolver.resolve(spec, ctx)    # explicit ADHAR_BATCH_PARAMS > schedule-derived > defaults
                                          # type/required/pattern/allowed; SecretParameterGuard; invalid → exit 2
  calendar/guards (P3)                    # not a business day → exit 0, outcome SKIPPED(calendar)
  instance = repo.getJobInstance(job, identifying(params))
  last     = repo.getLastJobExecution(instance)
  switch (last?.status):
    null, FAILED, STOPPED   → execution = jobOperator.start(job, params)        # restart when instance exists
    COMPLETED               → exit 0, SKIPPED(already-complete)                 # configurable: adhar.batch.exit.already-complete
    ABANDONED               → exit 6
    STARTING/STARTED/STOPPING:
        owner = lease.owner(last)
        if owner pod is gone (GET → 404) or lease stale (> recovery.stale-after) → jobOperator.recover(last); start
        elif owner pod is terminating → wait (poll 5 s, ≤ recovery.max-wait) then re-evaluate
        else → exit 0, SKIPPED(duplicate)
  during: lease heartbeat every recovery.heartbeat-interval; GracefulStopLifecycle armed
  after:  outcome → ExitCodes.of(execution); TerminationSummaryWriter.write(outcome)
          (SIGTERM path: STOPPED is recorded; the JVM's own exit status stands, see §5.7)
```

**`RunContext`:**

```java
public record RunContext(
    String jobName, String batchJobName, BatchRole role, RunTrigger trigger, String actor,
    String k8sJobName, String k8sJobUid, String podName, String podUid, String nodeName,
    Optional<OffsetDateTime> scheduledTime, ZoneId zone, Optional<LocalDate> businessDate,
    String imageDigest, int attempt, OptionalInt partitionIndex, String correlationId) {}
public enum RunTrigger { SCHEDULE, MANUAL, RESTART, BACKFILL, DEPENDENCY, WORKFLOW, SMOKE, OPS, EMBEDDED }
public enum BatchRole  { SINGLE, MANAGER, WORKER, INDEXED }      // INDEXED: index 0 → MANAGER, else WORKER
```

**What the runner reads, and where it comes from:**

| Input | Source |
|---|---|
| `ADHAR_BATCH_JOB`, `_NAME`, `_ROLE`, `_TRIGGER`, `_PARAMS` (JSON) | Pod env set by the composition or the client |
| `ADHAR_BATCH_K8S_JOB` | Downward API `metadata.labels['batch.kubernetes.io/job-name']` |
| `ADHAR_BATCH_ACTOR` | Downward API `metadata.annotations['adhar.io/batch-actor']`, stamped by Kyverno |
| `POD_*`, `NODE_NAME` | Downward API |
| Scheduled time | `GET jobs/<owner>` → annotation `batch.kubernetes.io/cronjob-scheduled-timestamp`. For a Volcano Job, the CronJob controller's scheduled-timestamp annotation [S17]. Uses the JDK `HttpClient` with the mounted ServiceAccount token, against the composed Role `get jobs, pods` (§6.4). |
| Business date | The scheduled time in the job's `timeZone`, truncated to a date. **Never `now()` for a scheduled run.** If it cannot be resolved, the runner exits 2 rather than invent an identity. |

### 5.6 Identity, parameters and business date

- **The identifying parameters are the declared ones with `identifying = true`.** For scheduled jobs that is normally `businessDate` (`source = SCHEDULE`), plus whatever selects the data set (region, source path).
- **Non-identifying provenance** is added to every execution:
  - `adhar.trigger`
  - `adhar.actor`
  - `adhar.k8s.job`
  - `adhar.app.version` (image digest)
  - `adhar.attempt`
- **No incrementers in business jobs.** In Spring Batch 6, `start(job, params)` on a job with an incrementer ignores the supplied parameters and starts the next instance (`SimpleJobOperator.java:189-201`). The kit's former `run.id = currentTimeMillis` is exactly what made scheduled runs unrestartable (K6). `@AdharBatchJob(allowIncrementer = true)` exists for housekeeping only.
- **Duplicates.** Spring Batch's instance identity is the duplicate guard: `JobInstanceAlreadyCompleteException` and `JobExecutionAlreadyRunningException`. It is backed by the repository's `SERIALIZABLE` isolation for instance creation (`AbstractJobRepositoryFactoryBean`). Deterministic run names (§8.3) and `concurrencyPolicy: Forbid` are the outer layers, because a CronJob fires "approximately once" [S50].
- **Secrets never travel as parameters.**
  - Parameters are persisted in clear text (`PARAMETER_VALUE VARCHAR(2500)`, Batch 6 `schema-postgresql.sql`).
  - `SecretParameterGuard` refuses names matching `(?i)pass(word)?|secret|token|credential|api[-_]?key|private[-_]?key`.
  - `sensitive = true` values are masked in logs, the termination summary and events.
  - Credentials come only from `secretKeyRef`/`envFrom`.

### 5.7 Exit-code contract

Codes 0, 4, 5, 6 and 7 equal Boot's `BatchStatus` ordinals (`JobExecutionExitCodeGenerator`), so they mean the same thing with or without the kit. Ordinals 1–3 are non-terminal statuses, which a finished run never exits with. The kit gives 2 and 3 their own meanings, and 1 stays the JVM's generic failure.

| Code | Meaning | Runner condition | `podFailurePolicy` (native) | Volcano policy |
|---|---|---|---|---|
| 0 | Completed, or a no-op | COMPLETED; or SKIPPED (already complete, duplicate, calendar) | success | manager `TaskCompleted → CompleteJob` |
| 1 | JVM/context failure before the run | bean creation, DataSource unreachable at start | Count | `PodFailed → RestartJob` |
| 2 | Invalid request | parameters, unknown job, incrementer, resourceless repository, Flyway migration failure, unresolved business date | **FailJob** | `TerminateJob` |
| 3 | Failed, not retryable | `NonRetryableBatchException`, skip limit exceeded, control totals (P3) | **FailJob** | `TerminateJob` |
| 4 | Stopped on request (not by a signal) | Another process marked the execution STOPPING (ops `stop`) | **FailJob** | `TerminateJob` |
| 5 | Failed, retryable | Any other FAILED | Count (backoffLimit) | `PodFailed → RestartJob` |
| 6 | Abandoned | Last execution ABANDONED: a new identity is needed | **FailJob** | `TerminateJob` |
| 7 | Repository state unknown | `BatchStatus.UNKNOWN` | **FailJob** | `TerminateJob` |
| 137 / 143 | Killed (OOM / SIGTERM) | — | `DisruptionTarget` → **Ignore**; otherwise Count | `PodEvicted`/`PodFailed → RestartJob` |

```yaml
podFailurePolicy:                 # rules are evaluated in order; first match wins [S49]
  rules:
    - action: Ignore              # preemption, API eviction (drain), taint eviction: replacement, not counted
      onPodConditions: [ { type: DisruptionTarget } ]
    - action: FailJob
      onExitCodes: { containerName: batch, operator: In, values: [2, 3, 4, 6, 7] }
backoffLimit: 2                   # Count for everything else
podReplacementPolicy: Failed      # the only value permitted with a failure policy [S49]
```

**The SIGTERM path.** On suspend, preemption, eviction or drain, `GracefulStopLifecycle` records STOPPED. The JVM then exits with its signal status. It does not call `System.exit` mid-shutdown, which would block. Policy treats that termination by the pod's `DisruptionTarget` condition, or by suspension, whose pods do not count [S49]. The status code itself is never relied on.

### 5.8 JobRepository on the platform Postgres

- **Where:** the application's own `Database` XR (CNPG). The template names it `<app>-db`; CNPG publishes `<app>-db-app` with `jdbc-uri`, `username`, `password`, and the database defaults to `app` (`xrd/database.xrd.yaml:94-107`).
  - Not a shared central database: a failure stays inside one application, tenancy stays the namespace, and there is no cross-team credential *(decision)*.
  - Barman backups to RustFS already come with the composition.
- **Schema `batch`.** Spring Batch's tables use `spring.batch.jdbc.table-prefix=batch.BATCH_` (wired from `adhar.batch.repository.{schema,table-prefix}`, K8). Business tables stay in `public` (or the application's schema) and never share a migration history with the kit.
- **Migrations by the kit, not by Boot.** `spring.batch.jdbc.initialize-schema` defaults to `embedded`, which never runs on Postgres. The kit runs **programmatic Flyway** in `BatchSchemaMigrator`:
  - schema `batch`, history table `batch.adhar_batch_schema_history`;
  - locations `classpath:com/adhar/kit/batch/db/postgresql`.
  - It is deliberately not a `Flyway` bean: Boot's Flyway auto-configuration backs off when one exists, and the application's own migrations would stop running.
  - It is ordered before the JobRepository and JPA through Boot's database-initializer dependency mechanism.

  ```text
  V6_0_0__spring_batch_6_0.sql     Batch 6.0 schema-postgresql.sql (6 tables, 3 sequences) + indexes
                                   (BATCH_JOB_EXECUTION(JOB_INSTANCE_ID), (STATUS, START_TIME);
                                    BATCH_STEP_EXECUTION(JOB_EXECUTION_ID); BATCH_JOB_INSTANCE(JOB_NAME))
  V6_0_1__adhar_tables.sql         ADHAR_SCHEDULER_LOCK (embedded mode; replaces runtime DDL, K6)
                                   ADHAR_BATCH_EXECUTION_LEASE(JOB_EXECUTION_ID PK, OWNER_POD, OWNER_UID,
                                     NODE, ACQUIRED_AT, HEARTBEAT_AT, LAST_COMMIT_AT)
                                   ADHAR_BATCH_DEAD_LETTER(ID, JOB_EXECUTION_ID, STEP_NAME, BUSINESS_DATE,
                                     ITEM JSONB, ERROR_CLASS, ERROR_MESSAGE, CREATED_AT, REPLAYED_AT)
  V6_0_2__partition_channel.sql    INT_CHANNEL_MESSAGE (Spring Integration; P2, only when transport=jdbc) [S80]
  ```

  Future framework schema changes become versioned migrations; 6.0's `BATCH_JOB_SEQ` → `BATCH_JOB_INSTANCE_SEQ` rename is the precedent [S62].
- **Fail fast.** A Kubernetes-mode run with a resourceless repository exits 2. Tests opt into in-memory with `adhar.batch.repository.allow-in-memory=true`.
- **Must be proven by test (KIT-0.4):**
  - The schema-qualified prefix works on PG 18.
  - Two concurrent launches of the same identity on PG 18 yield exactly one execution.

### 5.9 Graceful stop, recovery, and why restarts do not duplicate

- **`GracefulStopLifecycle`** is a `SmartLifecycle` in the earliest stop phase [S75]. On context close (SIGTERM):

  | Role | Action |
  |---|---|
  | Single / manager | `jobOperator.stop(execution)`, then wait up to `adhar.batch.shutdown.timeout` (default 90 s, below the default `terminationGracePeriodSeconds: 120`) for a terminal status |
  | Worker | Sets `terminateOnly` on its own partition's StepExecution only (`WorkerStopHandler`), so the other partitions finish |

  Batch 6's `JobExecutionShutdownHook` only signals the stop and returns, racing the DataSource close (`JobExecutionShutdownHook.java`). The kit does not use it.
- **The lease.** The single/manager process writes its `ADHAR_BATCH_EXECUTION_LEASE` row at start and heartbeats every 30 s. A STARTED execution is recovered only when its owner pod is gone or the heartbeat is older than `recovery.stale-after` (default 2 min).
  - For native Jobs, `podReplacementPolicy: Failed` already guarantees that the old pod is terminal before a replacement exists [S49].
  - The lease protects the cases with no such guarantee: Volcano `RestartJob`, Indexed-Job managers, and humans creating a restart.
- **Fencing (INV-4).** If a zombie writer survives a `recover` (a manager restarted while a worker still runs an old partition):
  - its next chunk commit updates its StepExecution with an outdated `VERSION`;
  - the update fails with an optimistic-locking error;
  - because the business writes are in the **same transaction**, they roll back with it.
  - The restarted partition resumes from the last committed execution context.

  This is the reason the kit defaults to one DataSource. For S3 writers, write to a staging prefix and publish with a `_SUCCESS` marker after the step (P3 `S3ItemWriterBuilder.staged()`), or use idempotent object keys.

### 5.10 Partitioning

- **Local (P1).** `LocalPartitioning.of(gridSize, threads)` gives a `TaskExecutorPartitionHandler` preset. `SqlKeyRangePartitioner` splits by primary-key ranges (`MIN/MAX` or `NTILE`); `ObjectPrefixPartitioner` splits by S3 prefixes. CPU is sized to `threads`.
- **Remote (P2): standard Spring Batch Integration**, through `@EnableBatchIntegration`'s `RemotePartitioningManagerStepBuilderFactory` / `RemotePartitioningWorkerStepBuilderFactory`. The manager is in **job-repository polling** mode: it has no reply channel, only a poll interval and a timeout [S66].
- **Transport `jdbc`** (the default, and the only one in P2): a `QueueChannel` backed by Spring Integration's `JdbcChannelMessageStore`, with `PostgresChannelMessageStoreQueryProvider`, whose poll is a single `DELETE … RETURNING` [S80]. It lives on the application's own database:
  - no broker;
  - works in the local profile;
  - partition requests "do not need to be durable"; the repository guarantees each partition runs once [S65].
  - `kafka` and `amqp` are reserved names. RabbitMQ arrives with remote chunking in P4 (durable, single consumer [S65]).
- **Role switch.** `ADHAR_BATCH_ROLE` is `manager`, `worker` or `indexed`. One image, one `@Configuration`. Beans are conditional on the role, so a worker never defines the manager step and vice versa.
- **Stale-request guard.**
  - The message group id is `adhar-batch:<job>:<jobKey>`, where `jobKey` is Spring Batch's key of the identifying parameters, which the manager and every worker compute alike.
  - The manager clears the group when it starts or restarts an execution.
  - A worker discards any request whose JobExecution is not STARTED.
  - A request left from a failed attempt therefore never executes.

### 5.11 Remote chunking (P4)

`RemoteChunkingManagerStepBuilderFactory` / `RemoteChunkingWorkerBuilder` [S66] over a RabbitMQ `Queue` XR (with dead-letter queue). The middleware must be durable, with guaranteed delivery and a single consumer per message [S65]. That rules out the JDBC channel for chunking. The local profile uses Batch 6 **local chunking** instead [S61].

### 5.12 I/O

| Builder | Phase | Notes |
|---|---|---|
| `JdbcItemReaderBuilder` / `JpaItemReaderBuilder` (existing) | 0 | Page size from `adhar.batch.page.size`, no longer hard-coded 50 (K8) |
| `KeysetPagingItemReaderBuilder` | 1 | Stable order on a unique key, which is what restart needs |
| `PostgresUpsertItemWriterBuilder` | 1 | `INSERT … ON CONFLICT (<keys>) DO UPDATE` — exactly-once output with a unique key |
| `S3ItemReaderBuilder` / `S3ItemWriterBuilder` | 1 (after spike) | Spring Cloud AWS 4.x `Resource` against the RustFS endpoint, path-style. The spike (KIT-1.5a) must pass before it ships. Multi-object reads by prefix. `staged()` commit in P3. |
| `JsonLinesItemReaderBuilder` / `JsonLinesItemWriterBuilder` | 1 | Until 6.1's `JsonLineMapper`/`JsonLineAggregator` [S63] |
| Kafka, fixed-width, XML presets | 3 | Classes already in `spring-batch-infrastructure` 6.0.4 |
| Iceberg / Parquet | 4 | Reads via Trino JDBC; bulk writes through the Iceberg API to the RustFS catalogue, never row by row through Trino |

### 5.13 Observability (kit side)

- **Metrics.**
  - Spring Batch's own meters (`spring.batch.job`, `spring.batch.job.active`, `spring.batch.step`, `spring.batch.item.read`, `spring.batch.item.process`, `spring.batch.chunk.write`) are wired by Boot's `BatchObservationAutoConfiguration`.
  - The kit **removes** its duplicates (`adhar.batch.job.*`, `adhar.batch.step.*`), behind `adhar.batch.metrics.legacy=false` (K7).
  - It adds only what is missing:

    | Meter | Type | Tags |
    |---|---|---|
    | `adhar.batch.last.commit` | gauge, epoch seconds | job, step |
    | `adhar.batch.items.dead.lettered` | counter | job, step |
    | `adhar.batch.run` | gauge, 1 while running | job, trigger |

    No business date or execution id goes in a tag (cardinality).
- **Delivery.** `adhar-kit-metrics` gains an OTLP push mode: optional `micrometer-registry-otlp`, plus an EnvironmentPostProcessor mapping `OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_METRICS_EXPORTER=otlp` to `management.otlp.metrics.export.*` [S73], modelled on the tracing module's `OtelEnvironmentPostProcessor`.
  - A run pod pushes to Alloy (`:4318`), which forwards OTLP metrics to the hub's remote write (`observability/alloy/values.yaml:247-268`).
  - The registry flushes on context close.
  - Scrape-only would lose every pod shorter than the 30 s scrape interval.
- **Traces.** Job, step and chunk spans only. Per-item observations stay metrics-only (`ItemObservationTracingFilter`); otherwise a million-item run is a million spans. Contract test: a 10,000-item run produces fewer than 50 spans.
- **Logs.** JSON to stdout. MDC carries `batch.job`, `batch.instance`, `batch.execution`, `batch.step`, `batch.partition`, `batch.businessDate`, `k8s.job`. `adhar-kit-logging`'s `BatchJobLogger` events are attached by the kit listeners.
- **Termination summary.** `/dev/termination-log`; the kubelet keeps ≤4096 bytes per container [S53]. The kit caps the summary at 3.5 KB and drops step detail first:

  ```json
  {"v":1,"job":"importJob","batchJob":"importer","instanceId":42,"executionId":97,"status":"COMPLETED",
   "exitCode":0,"exitDescription":"","outcome":"completed","trigger":"schedule","businessDate":"2026-11-03",
   "read":120000,"written":119990,"skipped":10,"deadLettered":10,"filtered":0,
   "start":"2026-11-03T17:30:04Z","end":"2026-11-03T17:43:36Z","image":"sha256:…",
   "steps":[{"name":"importStep","status":"COMPLETED","read":120000,"written":119990,"skipped":10}]}
  ```

### 5.14 Events (kit side)

The kit publishes **in-JVM** `BatchRunEvent`s (`Started`, `Completed`, `Failed`, `Stopped`, `Skipped`, `Recovered`) as Spring `ApplicationEvent`s. `BatchJobFailedEvent` is kept for compatibility. Applications bridge them to business notifications (`adhar-kit-notification`, P3).

**The kit does not publish to the platform event bus** *(decision, revising the research proposal)*. Platform `adhar.batch` events come from the batch observer (§12), which reads the run object and the termination summary:

- Tenant pods hold no write path to the shared bridge, so they cannot forge events.
- The organisation, team and project on each event come from the object's namespace, not from claims the pod makes.
- A run that dies without writing a summary (OOM, SIGKILL, lost node) still produces an event.

### 5.15 Security (kit side)

- No secrets in parameters (§5.6). Masked logs (the kit's own listener no longer logs parameters at INFO).
- The ServiceAccount token is used for two `GET`s only.
- The runner refuses to start with a resourceless repository.
- The P1 batch image runs as non-root (Paketo default), drops all capabilities and uses the RuntimeDefault seccomp profile (§6.4).

### 5.16 Embedded mode (services that also run a small job) and `BatchFacade`

Embedded mode is kept, fixed and documented as **not** the platform path:

- The launch is **synchronous** on the scheduler thread through `JobOperator.start`. The async `TaskExecutorJobLauncher` is removed (K3).
- The lock is held **and renewed** for the run's duration (`SchedulerLock.renew`). Optionally it is a Kubernetes Lease through `adhar-kit-kubernetes`'s `LeaderElectionService`.
- Identity is `adhar.scheduled-time` = the trigger's nominal fire time, truncated to the minute. A double fire is therefore the same JobInstance (K6).
- `CronTrigger` gets a time zone; fires later than `adhar.batch.scheduler.misfire-threshold` are skipped and logged.
- The lock table comes from Flyway.
- `BatchFacade.getInstance()` self-initialises from the context, following `NotificationFacade` (K1).

### 5.17 Properties

| Property | Default | Meaning |
|---|---|---|
| `adhar.batch.enabled` | `true` | Module switch; the auto-configuration also requires at least one `Job` bean |
| `adhar.batch.mode` | `auto` | `auto` \| `kubernetes` \| `embedded` |
| `adhar.batch.job` | env `ADHAR_BATCH_JOB` | Job to run in Kubernetes mode |
| `adhar.batch.role` | env `ADHAR_BATCH_ROLE` (`single`) | `single` \| `manager` \| `worker` \| `indexed` |
| `adhar.batch.repository.schema` / `.table-prefix` | `batch` / `BATCH_` | Schema-qualified prefix |
| `adhar.batch.repository.migrate` | `true` | Run the kit's Flyway |
| `adhar.batch.repository.allow-in-memory` | `false` | Tests only |
| `adhar.batch.recovery.heartbeat-interval` / `.stale-after` / `.max-wait` | `30s` / `2m` / `10m` | §5.9 |
| `adhar.batch.shutdown.timeout` | `90s` | Must be below the pod's grace period |
| `adhar.batch.exit.already-complete` | `success` | `success` (exit 0) \| `fail` (exit 3) |
| `adhar.batch.chunk.size` / `adhar.batch.page.size` | `100` / `500` | Builder defaults (K8) |
| `adhar.batch.retry.max-attempts` / `.initial-backoff` / `.max-backoff` | `3` / `1s` / `30s` | Retry presets |
| `adhar.batch.dead-letter.enabled` | `true` | JDBC DLQ |
| `adhar.batch.partition.transport` / `.grid-size` / `.poll-interval` / `.timeout` | `jdbc` / `4×workers` / `5s` / activeDeadline − 5m | §5.10 |
| `adhar.batch.telemetry.trace-items` | `false` | Per-item spans |
| `adhar.batch.metrics.legacy` | `false` | Old `adhar.batch.*` meters (removed in 0.4.0) |
| `adhar.batch.scheduler.enabled` / `.time-zone` / `.misfire-threshold` / `.lock` | `false` / `UTC` / `10m` / `jdbc` | Embedded mode |

Removed or renamed: `max-concurrent-jobs` (async launcher gone), `retry-on-failure` (it was a *step* default and is now `retry.*`), `default-chunk-size`/`default-page-size` (renamed).

### 5.18 Auto-configuration

- `@AutoConfiguration(afterName = {BatchAutoConfiguration (Boot), BatchJdbcAutoConfiguration, DataSourceAutoConfiguration, the Micrometer metrics/observation auto-configurations})` — the kit's own convention, as in event sourcing and cache [S74].
- `@ConditionalOnBean(Job.class)`: a service with no jobs gets nothing.
- Imports file unchanged (`META-INF/spring/…AutoConfiguration.imports`).
- KIT-0.2's guard test uses **`@SpringBootTest` with real auto-configuration** against Testcontainers Postgres, not an `ApplicationContextRunner` with user beans. The latter is what hid K2.

### 5.19 Test support (`adhar-kit-test-commons`)

- **`@AdharBatchTest`** composes:
  - `@SpringBatchTest` (`JobOperatorTestUtils`, `JobRepositoryTestUtils`; JUnit 5 only [S68]);
  - Testcontainers Postgres (`PostgresTestContainer`, `postgres:18.4`);
  - the kit's Flyway schema;
  - optional LocalStack S3 (`LocalStackTestContainer`).
- **`RestartScenario`**: fail at item N through a `FailureInjector` bean, restart with the same parameters, then assert the row count, no duplicates and the STEP status history.
- **`BatchContractAssertions`**: compares `@AdharBatchJob` parameters with `deploy/base/batchjob.yaml`; checks memory ≥ the JVM floor and that grace exceeds the shutdown timeout.

Everything runs under app-ci's kubedock: public images, explicit tags, no bind mounts.

---

## 6. Self-service model

### 6.1 Application `type: batch`

| Change | Where |
|---|---|
| Enum `type: [service, web, worker, data, ai, function, batch]`; `dashboard.type` adds `batch-jvm` | `xrd/apps.xrd.yaml:66,86` |
| `$isBatch` = `type == batch` or label `adhar.io/workload: batch`, mirroring `$isFunction` (`:141`). The overlay holds `BatchJob` XRs. The dashboard selects `batch-jvm`. | `compositions/apps/argocd-multienv.yaml:134-148` |
| `$batches` set beside `$functions` (`:87-97`); batch verification branch (§6.6); resilience gate skipped for batch, as for functions (`:828-833`) | `compositions/project/local.yaml` |
| `TYPES += "batch-jvm"` with batch panels (§11.2); regenerate; `TestGeneratedDashboardsAreUpToDate` | `hack/dashboards/build-app-dashboard.py:70` |

An application may own several `BatchJob`s (`settlement`, `settlement-housekeeping`). A service application can also run batch: the same image with `ADHAR_BATCH_JOB` set is a runner (§5.5). The application then stays `type: service` and simply adds `BatchJob`s to its overlays.

### 6.2 `BatchJob` XRD (`xrd/batchjob.xrd.yaml`)

Namespaced Crossplane v2 XRD in `platform.adhar.io`, kind `BatchJob`, plural `batchjobs`, `defaultCompositionRef: batchjob-kubernetes`. Written in the style of `function.xrd.yaml`. The schema below is abridged to types and defaults; every field gets a description in the real file.

```yaml
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata: { name: batchjobs.platform.adhar.io }
spec:
  group: platform.adhar.io
  scope: Namespaced
  names: { kind: BatchJob, plural: batchjobs }
  defaultCompositionRef: { name: batchjob-kubernetes }
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      additionalPrinterColumns:
        - { name: Schedule,  type: string, jsonPath: .spec.parameters.schedule.cron }
        - { name: Scheduler, type: string, jsonPath: .status.scheduler }
        - { name: Last-Success, type: date, jsonPath: .status.lastSuccessfulTime }
        - { name: Held, type: string, jsonPath: .status.held.reason }
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              required: [parameters]
              properties:
                parameters:
                  type: object
                  required: [job, image, database]
                  x-kubernetes-validations:
                    - { rule: "self.mode != 'partitioned' || (has(self.workers) && self.workers >= 1)", message: "partitioned needs workers >= 1" }
                    - { rule: "self.timeouts.terminationGracePeriodSeconds < self.timeouts.activeDeadlineSeconds", message: "grace must be shorter than the deadline" }
                  properties:
                    displayName: { type: string }
                    application: { type: string }                       # adhar.io/application; default: the XR name
                    job: { type: string, pattern: "^[A-Za-z][A-Za-z0-9_.-]*$" }   # Spring Batch job bean name
                    image: { type: string }                             # Kargo pins it (kustomizeconfig)
                    imagePullSecrets: { type: array, items: { type: string } }    # default [harbor-dockercfg]
                    mode: { type: string, enum: [single, partitioned], default: single }
                    workers: { type: integer, minimum: 1, maximum: 64, default: 4 }
                    gridSize: { type: integer, minimum: 1, maximum: 1024 }        # default 4 × workers
                    scheduler: { type: string, enum: [auto, volcano, kubernetes], default: auto }
                    schedule:
                      type: object
                      required: [cron]
                      properties:
                        cron: { type: string }                          # 5-field; omit `schedule` for on-demand only
                        timeZone: { type: string, default: UTC }        # IANA [S50]
                        startingDeadlineSeconds: { type: integer, minimum: 60, default: 3600 }   # misfire window
                        concurrencyPolicy: { type: string, enum: [Forbid, Allow], default: Forbid }  # Replace refused
                        suspend: { type: boolean, default: false }     # declared in Git; operators use the hold annotation
                        calendar: { type: string }                      # P3
                    parameters:                                         # mirrors @AdharBatchJob; drives forms and CLI checks
                      type: array
                      maxItems: 32
                      items:
                        type: object
                        required: [name]
                        properties:
                          name: { type: string, pattern: "^[A-Za-z][A-Za-z0-9_.]*$", maxLength: 64 }
                          type: { type: string, enum: [string, long, double, boolean, date, datetime], default: string }
                          identifying: { type: boolean, default: true }
                          required: { type: boolean, default: false }
                          default: { type: string }
                          pattern: { type: string }
                          allowed: { type: array, items: { type: string } }
                          source: { type: string, enum: [input, schedule], default: input }
                          sensitive: { type: boolean, default: false }
                          description: { type: string }
                    resources:
                      type: object
                      properties:
                        cpu: { type: string, default: "1" }
                        memory: { type: string, default: "1536Mi" }    # request = limit
                        cpuLimit: { type: string }                      # unset: no CPU limit
                        workerCpu: { type: string }
                        workerMemory: { type: string }
                        tmpSize: { type: string, default: "1Gi" }       # emptyDir /tmp sizeLimit
                    jvm:
                      type: object
                      properties:
                        threads: { type: integer, default: 50 }         # BPL_JVM_THREAD_COUNT
                        codeCache: { type: string, default: "64M" }
                        options: { type: string }
                    retry:
                      type: object
                      properties:
                        backoffLimit: { type: integer, minimum: 0, maximum: 10, default: 2 }
                        maxRetry: { type: integer, minimum: 0, maximum: 10, default: 3 }
                    timeouts:
                      type: object
                      default: {}
                      properties:
                        activeDeadlineSeconds: { type: integer, minimum: 300, default: 21600 }
                        terminationGracePeriodSeconds: { type: integer, minimum: 30, maximum: 900, default: 120 }
                    history:
                      type: object
                      properties:
                        successfulJobsHistoryLimit: { type: integer, default: 3 }
                        failedJobsHistoryLimit: { type: integer, default: 3 }
                        ttlSecondsAfterFinished: { type: integer, default: 604800 }
                    priority: { type: string, enum: [standard, critical], default: standard }
                    disruption: { type: string, enum: [avoid, allow], default: avoid }
                    scaleUp: { type: boolean, default: false }
                    database:
                      type: object
                      required: [secretRef]
                      properties:
                        secretRef: { type: string }                    # CNPG app Secret: jdbc-uri, username, password
                        schema: { type: string, default: batch }
                    env: { type: object, additionalProperties: { type: string } }
                    envFrom: { type: array, items: { type: object, x-kubernetes-preserve-unknown-fields: true } }
                    serviceAccountName: { type: string }               # default: composed <name>-batch
                    sla:
                      type: object
                      properties:
                        maxDuration: { type: string }                  # e.g. 3h; default = activeDeadlineSeconds
                        finishBy: { type: string, pattern: "^([01][0-9]|2[0-3]):[0-5][0-9]$" }   # P3
                        timeZone: { type: string }
                    alerts:
                      type: object
                      properties:
                        enabled: { type: boolean, default: true }
                        staleAfter: { type: string, default: "26h" }
                        severity: { type: string, enum: [auto, warning, critical], default: auto }
                    dependsOn:                                          # P3
                      type: array
                      items:
                        type: object
                        required: [batchJob]
                        properties:
                          batchJob: { type: string }
                          namespace: { type: string }
                          sameBusinessDate: { type: boolean, default: true }
                    retention:                                          # P3 housekeeping
                      type: object
                      properties:
                        days: { type: integer, default: 90 }
                        keepLast: { type: integer, default: 30 }
                    smoke:                                              # P3 verification run in test
                      type: object
                      properties:
                        parameters: { type: object, additionalProperties: { type: string } }
                    organisation: { type: string }                      # outside an environment namespace only;
                    team: { type: string }                              #   namespace labels win (as Function)
                    project: { type: string }
                    environment: { type: string, enum: [dev, test, prod] }
            status:
              type: object
              properties:
                scheduler: { type: string }                             # volcano | kubernetes
                queue: { type: string }
                runTemplate: { type: string }                           # ConfigMap name
                cronJob: { type: string }
                lastScheduleTime: { type: string, format: date-time }
                lastSuccessfulTime: { type: string, format: date-time }
                activeRuns: { type: array, items: { type: string } }
                held: { type: object, properties: { reason: { type: string }, since: { type: string } } }
                message: { type: string }
                conditions: { type: array, items: { type: object, x-kubernetes-preserve-unknown-fields: true } }
```

**Conditions:** `Synced`, `Ready` (Crossplane); `ScheduleReady`, `TemplateReady`, `PrerequisitesReady`, `Placement` (as in Function), `Held`.

### 6.3 Examples

**Scheduled, single pod (the template's prod overlay output):**

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: BatchJob
metadata:
  name: importer
  namespace: prod                       # an environment namespace (ADR-0026); labels supply the hierarchy
spec:
  parameters:
    job: importJob
    image: harbor-core.adhar-system.svc.cluster.local/library/importer@sha256:…   # pinned by Kargo
    schedule: { cron: "30 1 * * *", timeZone: Asia/Singapore }
    parameters:
      - { name: businessDate, type: date, source: schedule }
      - { name: source, type: string, default: inbound/records.csv, pattern: "^[a-zA-Z0-9/._-]+$" }
      - { name: dryRun, type: boolean, identifying: false, default: "false" }
    resources: { cpu: "1", memory: 1536Mi }
    priority: critical
    database: { secretRef: importer-db-app }
    envFrom: [ { secretRef: { name: importer-inbound-bucket } } ]
    sla: { maxDuration: 3h }
```

**Partitioned, on demand:**

```yaml
spec:
  parameters:
    job: reconcileJob
    image: harbor-core.adhar-system.svc.cluster.local/library/reconciler@sha256:…
    mode: partitioned
    workers: 4
    gridSize: 32
    resources: { cpu: "500m", memory: 1Gi, workerCpu: "1", workerMemory: 1536Mi }
    parameters:
      - { name: businessDate, type: date, required: true }
    database: { secretRef: reconciler-db-app }
```

### 6.4 Composition `batchjob-kubernetes` (`compositions/batchjob/kubernetes.yaml` + `README.md`)

Built like `function-local-knative`: Pipeline mode, `function-go-templating` then `function-auto-ready`.

**Placement.** Hierarchy from the namespace's `adhar.io/*` labels; parameters apply only outside an environment namespace.

**Extra resources read:**

| Resource | Purpose |
|---|---|
| The namespace | Hierarchy labels |
| `ConfigMap adhar-batch-scheduler` (`adhar-system`) | Scheduler descriptor |
| PriorityClasses `adhar-batch*` | Prerequisite check |
| The namespace's `ResourceQuota` | Gang-fit check |
| Calendar ConfigMaps (P3) | Business-day evaluation |

**Composed objects** (all in the XR's namespace; composed directly, no provider-kubernetes):

| Object | Name | Ready rule |
|---|---|---|
| ServiceAccount, Role (`get` on `jobs` in `batch` and `batch.volcano.sh`, and on `pods`), RoleBinding | `<name>-batch` | Ready annotation once observed (statusless kinds) |
| ConfigMap — the **run template** (`template.yaml`: the full Job or Volcano Job, unnamed; `parameters.json`; `naming: v1`) | `<name>-run` | once observed |
| CronJob (`schedule` set; native; single mode, or partitioned → Indexed Job template) | `<name>` | once observed |
| Volcano CronJob (P2; partitioned with Volcano) | `<name>` | once observed |
| PrometheusRule (§11.3) | `<name>-batch` | annotation |
| ConfigMap (P3) | `<name>-calendar` | annotation |

Statusless kinds publish no `Ready` condition, and `function-auto-ready` would otherwise hold the XR unready forever. Each one therefore carries `gotemplating.fn.crossplane.io/ready: "True"` once its UID has been observed.

**Scheduler choice:**

| `scheduler` | Behaviour |
|---|---|
| `kubernetes` | Native objects |
| `volcano` | Requires the descriptor; `PrerequisitesReady=False` without it |
| `auto` | Volcano when the descriptor exists, otherwise native |

- In single mode, Volcano only changes the pod template: `schedulerName: volcano` plus the queue annotation [S19]. The CronJob stays native.
- In partitioned mode, Volcano switches the composed kind (CronJob → Volcano CronJob). The resource names differ (`schedule-native` / `schedule-volcano`), so a switch deletes one and creates the other. **Enabling Volcano on a cluster with partitioned jobs therefore follows hold → switch → release.** The package README documents it, and the CLI warns.

**Validation (`fail` with a readable message, as Function does):**
- The name is a DNS-1035 label of at most 40 characters. CronJob names are at most 52 characters [S50], and run names add a suffix.
- `priority: critical` outside a `prod` namespace.
- A gang whose `(workers × workerMemory + memory)` exceeds the namespace `ResourceQuota`'s `limits.memory`.
- An XR in `adhar-system` or in a namespace without `adhar.io/environment`.
- `schedule.concurrencyPolicy: Replace`, which would SIGTERM a running run.
- A cron that fires more often than every 10 minutes is accepted, with `Placement` reporting the JVM-per-run cost (§14).

**Status.**
- `lastScheduleTime`, `lastSuccessfulTime` and `activeRuns` are copied from the observed CronJob's status.
- `Held=True` and `status.held` come from the hold annotation.
- `scheduler` and `queue` are resolved values.

**Pod template (native, single):**

```yaml
template:
  metadata:
    labels:  { adhar.io/batch-job: importer, adhar.io/workload: batch, adhar.io/batch-role: single,
               adhar.io/organisation: …, adhar.io/team: …, adhar.io/project: …, adhar.io/application: importer,
               adhar.io/environment: prod }
    annotations:
      cluster-autoscaler.kubernetes.io/safe-to-evict: "false"   # disruption: avoid
      adhar.io/batch-actor: ""                                   # overwritten by Kyverno at admission
  spec:
    restartPolicy: Never
    priorityClassName: adhar-batch-critical
    serviceAccountName: importer-batch
    terminationGracePeriodSeconds: 120
    enableServiceLinks: false
    imagePullSecrets: [ { name: harbor-dockercfg } ]
    securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
    containers:
      - name: batch
        image: harbor-core.adhar-system.svc.cluster.local/library/importer@sha256:…
        env:
          - { name: ADHAR_BATCH_JOB, value: importJob }
          - { name: ADHAR_BATCH_NAME, value: importer }
          - { name: ADHAR_BATCH_ROLE, value: single }
          - { name: ADHAR_BATCH_TRIGGER, value: schedule }
          - { name: ADHAR_BATCH_PARAMS, value: "{}" }
          - { name: ADHAR_BATCH_TIME_ZONE, value: Asia/Singapore }
          - { name: ADHAR_BATCH_K8S_JOB, valueFrom: { fieldRef: { fieldPath: "metadata.labels['batch.kubernetes.io/job-name']" } } }
          - { name: ADHAR_BATCH_ACTOR,   valueFrom: { fieldRef: { fieldPath: "metadata.annotations['adhar.io/batch-actor']" } } }
          - { name: POD_NAME,  valueFrom: { fieldRef: { fieldPath: metadata.name } } }
          - { name: POD_UID,   valueFrom: { fieldRef: { fieldPath: metadata.uid } } }
          - { name: NODE_NAME, valueFrom: { fieldRef: { fieldPath: spec.nodeName } } }
          - { name: ADHAR_ENVIRONMENT, value: prod }
          - { name: SPRING_DATASOURCE_URL,      valueFrom: { secretKeyRef: { name: importer-db-app, key: jdbc-uri } } }
          - { name: SPRING_DATASOURCE_USERNAME, valueFrom: { secretKeyRef: { name: importer-db-app, key: username } } }
          - { name: SPRING_DATASOURCE_PASSWORD, valueFrom: { secretKeyRef: { name: importer-db-app, key: password } } }
          - { name: OTEL_SERVICE_NAME, value: importer }
          - { name: OTEL_EXPORTER_OTLP_ENDPOINT, value: "http://alloy.adhar-system.svc.cluster.local:4318" }
          - { name: OTEL_EXPORTER_OTLP_PROTOCOL, value: http/protobuf }
          - { name: OTEL_METRICS_EXPORTER, value: otlp }
          - { name: BPL_JVM_THREAD_COUNT, value: "50" }
          - { name: JAVA_TOOL_OPTIONS, value: "-XX:ReservedCodeCacheSize=64M" }
        envFrom: [ { secretRef: { name: importer-inbound-bucket } } ]
        resources: { requests: { cpu: "1", memory: 1536Mi }, limits: { memory: 1536Mi } }
        securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: [ALL] } }
        volumeMounts: [ { name: tmp, mountPath: /tmp } ]
    volumes: [ { name: tmp, emptyDir: { sizeLimit: 1Gi } } ]
```

The Job spec around it is `backoffLimit`, `activeDeadlineSeconds`, `ttlSecondsAfterFinished`, `podReplacementPolicy: Failed` and the `podFailurePolicy` of §5.7.

The JVM settings follow the Function composition's fix for the 512Mi LimitRange (`BPL_JVM_THREAD_COUNT=50` and a 64M code cache), because the Paketo calculator needs ≈567M of fixed regions at its default thread count. The composition never relies on the LimitRange default.

### 6.5 The run-template contract (`ConfigMap <name>-run`)

| Key | Content |
|---|---|
| `template.yaml` | The run object exactly as the CronJob would create it: `metadata.name` absent, `adhar.io/batch-trigger: manual`, `ADHAR_BATCH_PARAMS: "{}"` |
| `parameters.json` | The declared parameters (form and CLI validation) |
| `kind` | `Job` \| `IndexedJob` \| `VolcanoJob` |
| `naming` | `v1` (§8.3) |

**Clients may change only** the name, the `adhar.io/batch-trigger` label and the `ADHAR_BATCH_PARAMS`, `ADHAR_BATCH_TRIGGER`, `ADHAR_BATCH_RESTART_OF`, `ADHAR_BATCH_COMMAND` and `ADHAR_BATCH_EXECUTION_ID` environment variables. Kyverno enforces this (§10.3).

### 6.6 Promotion dev → test → prod (Kargo)

- **Overlays.** `deploy/envs/<env>/kustomization.yaml` sets the namespace and the Kargo-owned `images:` block, and patches the `BatchJob`.
  - dev: `schedule.suspend: true` (on demand).
  - test: the real cron.
  - prod: the real cron plus `priority: critical`.
- **Image pinning.** `deploy/base/kustomizeconfig.yaml` declares `group: platform.adhar.io, kind: BatchJob, path: spec/parameters/image`, exactly as the Function template does. Without it, kustomize leaves `:latest` in place. Kargo renders the overlay to `stage/<env>` and Argo CD syncs it.
- **Verification** (Project composition, batch branch). Checks pass on silence and fail on evidence: no run in the window is not a failure, as for scale-to-zero functions.

  | Metric | Query (shape) | Fails when |
  |---|---|---|
  | `<app>-batchjob-healthy` | `argocd_app_info{name="<project>-<app>-<env>",health_status="Degraded"}`, plus not-Healthy over the settle window | the `BatchJob` composition is broken |
  | `<app>-runs-not-failing` | `count(kube_job_failed{namespace=NS,condition="true"} == 1 and on(namespace,job_name) kube_job_labels{label_adhar_io_application="<app>"} and on(namespace,job_name) (time() - kube_job_created) < WINDOW) or vector(0)` | a run of this app **started during verification** failed |
  | `<app>-no-oom` | `kube_pod_container_status_last_terminated_reason{reason="OOMKilled"}` joined to `kube_pod_labels{label_adhar_io_application="<app>"}` within the window | a run pod was OOM-killed |
  | `<app>-smoke-run` (P3, test only) | A Job metric that runs `<name>-smoke-<sha7>` from the run template with `smoke.parameters` | the smoke run fails |

  These need kube-state-metrics to export the labels (PLAT-1.7); labels are not exported by default [S57]. The metric names are stable or experimental as noted in [S54][S56].
- **Resilience gate.** Skipped for batch (it needs a Rollout or Deployment). The batch resilience check is the chaos restart test (P3).
- **Why a failed night does not block promotion.** Argo CD derives an application's health from its **immediate** resources and does not inherit child health [S86]. The CronJob is composed, not in the application source, so its built-in "Degraded when the last job failed" check [S86] never reaches the Application or Kargo's stage health [S88]. Run failures surface through alerts and the Console (§11) and gate only promotions in which they happen.

### 6.7 Argo CD health checks

Added to `hack/argocd/values.yaml` **and** `values-ha.yaml`, then regenerated into `platform/controllers/adharplatform/resources/argocd/install*.yaml`. `TestArgoCDReconciliationMatchesItsGeneratorValues` holds them together. `TestArgoCDCanAssessEveryCustomKindTheStackSyncs` (`argocd_health_test.go:33`) gains these kinds:

| Kind | Healthy | Progressing | Degraded |
|---|---|---|---|
| `platform.adhar.io_BatchJob` (P1) | `Ready=True` (`Held` still Healthy, message "held by …") | waiting for `Ready` | `Synced=False/ReconcileError`; `PrerequisitesReady=False` |
| `batch.volcano.sh_Job` (P2) | `Completed` | `Pending`, `Running`, `Restarting`, `Completing` | `Failed`, `Terminated`, `Aborted` |
| `batch.volcano.sh_CronJob` (P2) | exists (run failures are alerts, not health) | — | — |
| `scheduling.volcano.sh_Queue` (P2) | `Open` | `Unknown` | `Closed` / `Closing` |
| `scheduling.volcano.sh_PodGroup` (P2) | `Running`, `Completed` | `Pending`, `Inqueue` | — |

`resource.exclusions` (P2) adds `scheduling.volcano.sh/PodGroup` and `bus.volcano.sh/Command`. Volcano creates these per run; they are never in Git, and excluding them keeps Argo CD's cluster cache from watching them. Run Jobs need no exclusion: they carry no tracking annotation, because Crossplane, not Argo CD, writes the CronJob, and Volcano copies labels from the job template only [S17].

---

## 7. Console — the Batch area

### 7.1 Placement

**Develop → Batch** (`develop.batch`, after `develop.functions` in `packages/shell-ui/src/nav-tree.tsx:274`; `modules/develop/src/home.tsx` and `data/nav.ts`), with `roles: BUILDER_ROLES`.

**Queues** (P2) is owned by the Platform module and rendered under Develop → Batch → Queues, the way Builds is. Name everything `batch-*` / `Batch*`, and keep clear of the existing `prom-batch.ts`/`can-batcher.ts`, which are request batching.

### 7.2 Pages

| Page | Content | Phase |
|---|---|---|
| **Jobs** | One row per `BatchJob` the user can list, grouped by application then environment: schedule (human-readable, with TZ), next run (`kube_cronjob_next_schedule_time` [S55]), last run (status, duration, time), last success, SLA state, held badge, queue (P2). Filters: project, environment, status. | 1 |
| **Job drawer** | Definition (from the XR and the run template); parameter schema; recent runs as duration bars against `maxDuration`; firing alerts; delivery (Kargo stage/freight, as Functions); links to the Grafana `batch-jvm` dashboard, Loki and Tempo. Actions: **Run now**, **Hold / Release**. | 1 |
| **Runs** | Jobs and Volcano Jobs labelled `adhar.io/batch-job`: trigger, actor, business date, status, attempts, duration, exit code (from the summary). | 1 |
| **Run detail** | Phase and conditions; pods (manager and workers, with their index); the **termination summary** (execution id, step counts, exit description); live logs (`pods/log?follow`); Kubernetes events; metrics. Actions: **Stop**, **Resume**, **Restart**. | 1 |
| **Queues** | The `adhar-batch` hierarchy: per leaf `capability`, `deserved`, allocated and in-queue (`volcano_queue_*` [S22]); pending PodGroups with gang progress. | 2 |
| **History** | Runs beyond Job TTL, from `adhar.batch` events (90-day retention), served by the BFF `/api/batch/history`, which reads the bridge with the Console's identity and filters by namespaces the user may list. | 3 |
| **Tonight** | Forecast: every schedule in the next 24 h, with expected end = start + p90 duration and `finishBy` SLAs. Plus dependency chains, the DLQ (counts per job, replay action) and calendars. | 3 |

### 7.3 Data

- **Read as the signed-in user** through the Kubernetes gateway (`apps/console/app/server/k8s/gateway.ts`), live by `useWatchSignal(watchOf(GVR, …))`, as in `modules/develop/src/data/functions.ts:42-58`.
- **`data/batch-model.ts`** (pure, unit-tested):
  - GVRs `batchjobs.platform.adhar.io`, `jobs.batch`, `cronjobs.batch`, `jobs.batch.volcano.sh`, `cronjobs.batch.volcano.sh`, `podgroups.scheduling.volcano.sh`, `queues.scheduling.volcano.sh`, `commands.bus.volcano.sh`, pods, events, configmaps;
  - `runPhaseOf()`, `summaryOf(pod)` (parses the v1 summary and tolerates truncation), `runName(batchJob, params)` (§8.3), `paramFormOf(parameters.json)`, `slaStateOf()`.
  - Every field is optional: a page that throws on a missing status shows nothing.
- **PromQL** goes through `/api/prom/batch`.

### 7.4 Actions

| Action | Kubernetes call (as the user) | Gate (`useCan` / `<Can>`) |
|---|---|---|
| Run now | Read `<name>-run`, validate the form, `POST` the run named `runName()` | `create jobs` (or `jobs.batch.volcano.sh`) in the namespace |
| Stop | `PATCH spec.suspend=true` (native) / create Command `AbortJob` [S30] | `patch jobs` / `create commands.bus.volcano.sh` |
| Resume | `PATCH spec.suspend=false` / Command `ResumeJob` [S13] | same |
| Restart | `POST` `<name>-<hash8>-r<n>` with `ADHAR_BATCH_TRIGGER=restart`, `ADHAR_BATCH_RESTART_OF=<run>` | `create jobs` |
| Hold / Release | `PATCH` annotation `adhar.io/batch-hold` on the `BatchJob` | `patch batchjobs` |
| Change schedule or definition | Pull request against `deploy/envs/<env>` (Argo CD self-heal would revert a live edit) | existing PR flow |
| Delete | Through the owning application (`/api/apps/delete`) | `applicationsDelete` |

No BFF endpoint is needed in Phases 1–2. Blocked actions are hidden, not disabled (`packages/shell-ui/src/can.tsx`). No new `console.adhar.io` permission is needed: every action maps to a Kubernetes verb on a namespaced object.

### 7.5 Template-family plumbing (P1)

| File | Change |
|---|---|
| `apps/console/app/server/templates.ts:72-95` | Add `isBatchTemplate(name, tags, labels)`: label `adhar.io/workload: batch`, name `springboot-batch-*`, or tag `batch`. **It must run before** the `data` regex (`data\|ml\|pipeline\|etl\|…`), which would otherwise claim a template tagged `etl`. |
| `control-plane.ts:115,144-153` | `Workload = 'function' \| 'batch'`; `DASHBOARD_TYPES` += `batch-jvm`; `applicationTypeOf` returns `batch` |

The Console must also start honouring `adhar.io/workload`, which the CLI already does.

---

## 8. CLI — `adhar batch`

### 8.1 Commands

| Command | Behaviour | Phase |
|---|---|---|
| `adhar batch list [-n NS \| --env E [--project P] \| -A] [-o table\|json\|yaml]` | `BatchJob`s with schedule, next run, last run, last success, held | 1 |
| `adhar batch describe <job>` | Definition, parameters, schedule, scheduler/queue, conditions, last 10 runs | 1 |
| `adhar batch runs <job> [--status failed] [--limit 20]` | Runs (Jobs/Volcano Jobs by label) with trigger, actor, business date, exit code from the summary | 1 |
| `adhar batch run <job> [-p k=v …] [--wait] [--follow]` | Validate against `parameters.json`; create `<job>-<hash8>`; on `AlreadyExists`, show that run | 1 |
| `adhar batch logs <run> [--role manager\|worker] [--index N] [-f]` | Pod logs of the run | 1 |
| `adhar batch stop <run>` / `resume <run>` | Suspend/unsuspend, or Volcano `AbortJob`/`ResumeJob` | 1 |
| `adhar batch restart <run>` | Same identifying parameters, `-r<n>` | 1 |
| `adhar batch hold <job> --reason "…"` / `release <job>` | Annotation on the `BatchJob` | 1 |
| `adhar batch queues [--team T]` | Leaf queues and usage | 2 |
| `adhar batch backfill <job> --from D --to D [--parallel N] [--wait]` | One run per business date. Runs are created suspended and released N at a time while the CLI waits. With Volcano they are all created and the queue throttles. | 3 |
| `adhar batch abandon \| recover <run> --execution ID` | An ops Job from the run template with `ADHAR_BATCH_COMMAND` | 3 |
| `adhar batch dlq <job> [--replay]` | DLQ counts (from the summary and events); replay through an ops Job | 3 |
| `adhar batch history <job> [--since 30d]` | From `adhar.batch` through the bridge, as `adhar events` does | 3 |

### 8.2 Implementation

- **Layout:** `cmd/batch/{batch.go,list.go,describe.go,runs.go,run.go,logs.go,suspend.go,restart.go,hold.go,naming.go,naming_test.go,…}`, registered in `cmd/main.go` with `GroupID = GroupDevelop` (beside `function`, `:124`).
- **Clients:** `helpers.DynamicClient()` plus a typed clientset for logs. The shared table, icons and gutter come from `cmd/helpers`. `--env/--project` resolve the namespace from `Project.status.environments`.
- **Template plumbing:** `cmd/apps/deploy.go` gains `isBatchTemplate` and `type: batch`, plus `batch-jvm` in the dashboard switch (`:325-347`), alongside `isFunctionTemplate` (`:369`). Covered by `paved_road_test.go`.

### 8.3 Run naming (shared by the CLI, Console and kit P3 launcher; version `v1`)

```
canonical = JSON object of the IDENTIFYING parameters only, keys sorted by code point,
            every value as a string, no whitespace, UTF-8
hash8     = first 8 lowercase hex characters of SHA-256(canonical)
manual / backfill / dependency / workflow run   <batchjob>-<hash8>   (≤ 40 + 9 = 49 characters)
restart                              <batchjob>-<hash8>-r<n>     (n = 1..99)
smoke (P3)                           <batchjob>-smoke-<sha7>
ops (P3)                             <batchjob>-ops-<verb>-<executionId>, trimmed to 63
scheduled                            named by the CronJob controller
```

Test vectors live in `cmd/batch/naming_test.go`, `batch-model_test.ts` and the kit's `RunIdentityTest`. All three must agree byte for byte.

**Canonical encoding (normative; decision D1, §15.1).**
- The object is exactly what ECMAScript `JSON.stringify` writes for the identifying parameters, with keys sorted by Unicode code point.
- The parameters are taken **after** the declared defaults are applied. An optional parameter with no default and no value is absent, not `""`.
- Escapes are minimal:
  - `"` → `\"` and `\` → `\\`;
  - U+0008, U+000C, U+000A, U+000D and U+0009 → `\b`, `\f`, `\n`, `\r`, `\t`;
  - every other U+0000–U+001F → `\u00xx`, in lowercase hex.
- Nothing else is escaped. `/`, `<`, `>`, `&`, U+007F, U+2028, U+2029 and all non-ASCII characters are written as raw UTF-8.
- A value containing a lone surrogate is an invalid parameter and is never hashed.
- Go's `encoding/json` escapes U+2028/U+2029 even with `SetEscapeHTML(false)`, and Jackson's default escaping differs too. The Go and Java implementations therefore hand-roll the encoder.

| Vector | Identifying parameters | `hash8` |
|---|---|---|
| `v1-empty` | `{}` | `44136fa3` |
| `v1-business-date` | `businessDate=2026-11-03` | `09932cb3` |
| `v1-key-order` | `source=inbound/records.csv`, `businessDate=2026-11-03`, `region=sg` | `97cfb853` |
| `v1-codepoint-order` | `alpha=1`, `a_b=2`, `a.b=3`, `Zeta=4`, `A=5` (canonical order `A`, `Zeta`, `a.b`, `a_b`, `alpha`) | `ebe73916` |
| `v1-escapes` | `note` = `q"b\s/<>&` + LF + TAB + U+0001 + U+007F + U+2028 | `1d17cd61` |
| `v1-unicode` | `city=café`, `name=日本`, `tag=🚀` | `044f08a7` |
| `v1-empty-value` | `businessDate=2026-11-03`, `suffix=""` | `71c1df51` |

So `importer` with `businessDate=2026-11-03` is the run `importer-09932cb3`, and its first restart is `importer-09932cb3-r1`. The vectors were cross-checked in Python, ECMAScript, Go and Java. Each repository vendors the same vector file, carrying the canonical text and its UTF-8 bytes per vector.

---

## 9. Templates (`adhar-templates`)

### 9.1 `springboot-batch-job` (Phase 1)

**`template.yaml`:**
- Labels: `adhar.io/workload: batch`, `adhar.io/dashboard-type: batch-jvm`.
- Tags: `batch`, `java`, `springboot`, `spring-batch`, `adhar-kit`. No `etl` tag (§7.5).
- `spec.type: batch`. Steps as in `springboot-web-service`: `fetch:template` with an explicit `values` map using `or` defaults, `publish:gitea`, `adhar:create-argocd-app`, `catalog:register`.
- Registered in `catalog-info.yaml` `targets`.

| Page | Parameter | Type / default |
|---|---|---|
| Batch job | `name` | `^[a-z]([-a-z0-9]*[a-z0-9])?$`, max **40** |
| | `description`, `owner` | string, OwnerPicker |
| Job | `input` | enum `database`, `bucket`; default `database` (`bucket` adds `deploy/base/bucket.yaml` and the S3 reader) |
| | `schedule` | string, default `0 2 * * *`; empty means on demand |
| | `timeZone` | string, default `UTC` |
| | `parallelism` | enum `none`, `threads`, `partitions`; default `none` |
| | `chunkSize` | integer, default 500 |
| | `withDeadLetter` | boolean, default true |
| Resources | `cpu` / `memory` | `"1"` / `"1536Mi"` |
| | `maxDurationMinutes` | integer, default 180 |
| Operations | `lifecycle`, `tier`, `runbook`, `onCall` | as the web template |

Neither wizard shows `dependencies`/`oneOf` fields, so every follow-up value has an `or` default in `values`. Options use `{% if %}` blocks; there are no loops.

**Skeleton:**

```
skeleton/
  pom.xml                     parent com.adhar.kit:adhar-kit-parent:0.3.x; adhar-kit-batch, postgresql,
                              [spring-cloud-aws-starter-s3 when bucket]; test: adhar-kit-test-commons, spring-batch-test,
                              testcontainers-postgresql [+ localstack]; CVE floors and JaCoCo exclusion as the web template
  project.toml  settings.xml  BP_JVM_VERSION=25; Nexus maven-public mirror (unchanged)
  catalog-info.yaml           Component, spec.type: batch, adhar.io/* annotations, argocd/app-name
  README.md  mkdocs.yml  docs/{index,architecture,operations}.md   runbook: run, stop/resume, restart, hold,
                              OOM, DLQ, recover, backfill (P3)
  compose.yaml                local Postgres (+ an S3 emulator when bucket)
  src/main/java/io/adhar/batch/
    Application.java          AdharBatchApplication.run(Application.class, args)
    job/ImportJobConfiguration.java   @AdharBatchJob + Job + Step (AdharSteps, FaultTolerance, DLQ)
    job/{InputRecord,Record,RecordProcessor}.java   Bean Validation on InputRecord
    {% if input == 'bucket' %}job/InboundReaderConfiguration.java (S3ItemReaderBuilder){% endif %}
  src/main/resources/
    application.yaml          spring.datasource.* from env; adhar.batch.chunk.size; Flyway for the business schema
    db/migration/V1__records.sql      business table with a UNIQUE key (upsert target)
    sample/records.csv        fixture; must contain no `{%` or `${{`
  src/test/java/io/adhar/batch/
    RecordProcessorTest.java  unit
    ImportJobIT.java          @AdharBatchTest end to end: seed → run → assert rows and DLQ entries
    RestartIT.java            RestartScenario: fail at item N, restart, assert exactly-once
    RunnerExitCodeIT.java     runs the runner as the pod would (env vars): exit 0 / 2 / 5
    BatchJobContractTest.java @AdharBatchJob params == deploy/base/batchjob.yaml; memory floor; grace > shutdown timeout
  deploy/base/{kustomization.yaml, kustomizeconfig.yaml, batchjob.yaml, database.yaml, bucket.yaml?}
  deploy/envs/{dev,test,prod}/kustomization.yaml   namespace, Kargo-owned images:, schedule/priority patches
```

**CI expectations** (`application/adhar-supply-chain/manifests/70-app-ci.yaml`, unchanged):
- Buildpacks image (Java 25).
- `mvn -B -q verify` runs unit tests and the failsafe ITs on kubedock (public `postgres:18.4`), with the JaCoCo gate at **≥ 80%** (`Application` excluded).
- gitleaks, trivy (source and image), SBOM, cosign signing.
- `deploy/envs` exists, so app-ci pins nothing and Kargo promotes.
- A green first pipeline is part of acceptance (A1.10).

### 9.2 `springboot-batch-partitioned` (Phase 2)

- Same anatomy. `BatchJob` has `mode: partitioned`, `workers` (default 4) and `gridSize`.
- One `@Configuration` with manager and worker beans conditional on `ADHAR_BATCH_ROLE` (§5.10).
- `SqlKeyRangePartitioner` over the business table.
- **`PartitionedJobIT`** runs one manager and two worker contexts against one Testcontainers Postgres. It kills a worker mid-partition, restarts, and asserts that only unfinished partitions re-ran and the row count is exact.

### 9.3 Rules

- **Render every option combination with both renderers before pushing**: the Console's `template-render.ts` and the CLI's `renderScaffold` (`cmd/apps/scaffold.go`). Write the output to a scratch directory, never into a product repository. Run `mvn verify` on each rendered tree.
- **Templates reach clusters only through the GitHub → Gitea pull mirror.**
- **Kit pins are exact.**

---

## 10. Security and multi-tenancy

### 10.1 Tenancy mapping

- **Runs** execute in the application's environment namespace `<project>-<env>` (ADR-0026), under its ResourceQuota and LimitRange, and inside the team's `adhar-tenant-quotas` allowance.
- **Queues (P2)** add a team × tier throttle and the share policy (§4.3).
- **Hierarchy labels.** Every run pod carries all `adhar.io/*` hierarchy labels, plus `adhar.io/batch-job` and `adhar.io/workload: batch`, for policy, cost (OpenCost aggregation by label [S94]) and observability.

### 10.2 RBAC (package `core/adhar-batch`)

| ClusterRole | Aggregation / binding | Rules |
|---|---|---|
| `adhar-batch-view` | `aggregate-to-view` (hence edit and admin) | get/list/watch `batchjobs.platform.adhar.io`, `jobs` and `cronjobs` in `batch.volcano.sh`, `podgroups.scheduling.volcano.sh` |
| `adhar-batch-edit` | `aggregate-to-edit`, `aggregate-to-admin` | create/delete/patch `jobs.batch.volcano.sh`; create `commands.bus.volcano.sh`; patch `batchjobs.platform.adhar.io` |
| `adhar-batch-operator` | Bound by the Environment composition (`compositions/env/local.yaml:233-254`) to the team group **in prod**, where the team's access is `view`. Controlled by `Project.spec.parameters.batchOperations: team \| none` (default `team`). | view rules, plus create/delete/patch `jobs.batch`, create `jobs.batch.volcano.sh`, create `commands.bus.volcano.sh`, patch `batchjobs`, get `pods/log`, get `configmaps` |
| Per-team queue reader (P2) | Composed by the Team composition | `get` on `queues.scheduling.volcano.sh` with `resourceNames` = the team's three leaves |
| Per-job runner Role | Composed per `BatchJob` | `get` on `jobs` (batch, batch.volcano.sh) and on `pods` in the namespace |

Built-in `admin`/`edit`/`view` already cover `batch/v1` Jobs and CronJobs. Operating a job is not changing it: production *changes* still arrive through a Release (ADR-0026); operating is bounded by §10.3.

### 10.3 Run integrity (Kyverno, `core/adhar-batch`)

| Policy | Mode | Rule |
|---|---|---|
| `adhar-batch-run-integrity` | **Enforce** | On CREATE of `jobs.batch` / `jobs.batch.volcano.sh` labelled `adhar.io/batch-job` by any principal **other than** the Kubernetes CronJob controller and Volcano's controller manager (which runs its CronJob controller): load `ConfigMap <batch-job>-run` (an `apiCall` context) and deny unless image(s), command, args, `serviceAccountName`, volumes, `envFrom`, security contexts and every env entry other than the client-settable `ADHAR_BATCH_*` set (§6.5) equal the template. Users, the CLI, the Console, the batch observer and workflow steps are all checked. The webhook's `matchConditions` exclude those two controllers **by `request.userInfo.username`, never by label**: a label such as `adhar.io/batch-trigger: schedule` is client-settable and must not skip the check. A Kyverno outage therefore blocks manual runs (fail closed) and never scheduled ones. Verify `matchConditions` propagation on Kyverno 1.19 (PLAT-1.1 test). |
| `adhar-batch-actor` | Mutate | Stamp `adhar.io/batch-actor: {{request.userInfo.username}}` on the run and on every pod template (`foreach` over `spec.tasks` for Volcano) |
| `adhar-batch-critical-prod-only` | Enforce | `priorityClassName: adhar-batch-critical` and `adhar.io/scale-up: "true"` only in namespaces labelled `adhar.io/environment: prod` |
| `adhar-batch-hold-only` | Enforce | An UPDATE of a `BatchJob` by a user, rather than by Argo CD's application controller or Crossplane's service account, may change only the `adhar.io/batch-hold` annotation (`request.object.spec == request.oldObject.spec`) |
| `adhar-batch-pod-standards` | Audit (P1) → Enforce (P3) | Batch pods: memory request = limit; `restartPolicy: Never`; batch PriorityClass; no `persistentVolumeClaim` volumes (a node-local volume pins a preemptible pod to one node); Jobs have `activeDeadlineSeconds` and `ttlSecondsAfterFinished`. Written explicitly for `spec.tasks[*].template` (Kyverno autogen covers built-in controllers [S89]; Volcano coverage is unconfirmed). |
| `adhar-batch-queue-binding` (P2) | Mutate + Enforce | Queue annotation and `spec.queue` = `<org>-<team>-<env>` from namespace labels (§4.3) |
| soc2 probe exclusion | Edit `security/adhar-policy-packs/manifests/soc2.yaml` | Run-to-completion pods need no probes |

### 10.4 Secrets, storage and network

- **Database credentials** come from the CNPG application Secret (`secretKeyRef`). Third-party credentials come from ExternalSecrets (ADR-0009). None travel as parameters (§5.6).
- **Object storage (prerequisite).** `root-creds` exists in `dev`, `test` and `prod`, and the `Bucket` XR's connection Secret references it (`compositions/bucket/local-rustfs.yaml:108-128`), so every bucket user holds the store's root key.
  - PLAT-1.9 is a spike: per-bucket RustFS users and policies, minted by the Bucket composition's `mc` Job.
  - If RustFS cannot, the risk is recorded in this document's §18 with an owner. **Until one of the two lands, the template's `input: bucket` option is documented as single-team only.**
- **Supply chain.** Unchanged and inherited: the Kyverno signature policy (`verify-supply-chain-images`, Enforce) applies to every run pod.
- **Network.**
  - Environment namespaces deny ingress by default and allow egress, which is what runs need: the database in the same namespace, and RustFS, Alloy and the API server for the two `GET`s.
  - A project that adds egress policies must allow the API server, or the runner falls back to an explicit `businessDate` parameter and exits 2 for scheduled runs.
  - No in-cluster transit encryption is configured today; that is P4 for regulated workloads.
- **Volcano** (P2): webhook and RBAC narrowing (§4.1); pinned images; no dashboard; no agent.

---

## 11. Observability

### 11.1 Signals

| Signal | Source | Where it lands |
|---|---|---|
| Run state | kube-state-metrics Job/CronJob series (STABLE except where noted [S54][S55][S56]); labels via the allowlist (PLAT-1.7) | Prometheus (alerts), Mimir |
| In-job metrics | Spring Batch meters + kit gauges, OTLP push | Alloy → Mimir (production profile) |
| Traces | Job/step/chunk spans | Alloy → Tempo |
| Logs | JSON stdout with MDC | Loki |
| Run summary | Termination message | Console, CLI, observer |
| Volcano (P2) | `volcano_*` [S22]; Volcano Job/PodGroup phases via KSM custom-resource state (feature-frozen in favour of resource-state-metrics [S58]; isolated in one config file so the move is one change) | Prometheus, Mimir |
| Events (P3) | `adhar.batch` CloudEvents | Kafka (90 days) |

**Verify at the reader.** Prove every series with a query at Mimir/Prometheus before a dashboard or alert uses it. OTLP names are translated (`spring.batch.job` → a `spring_batch_job_*` family), so never write them from Micrometer names.

### 11.2 Dashboard type `batch-jvm`

Panels:
- Runs per day by outcome.
- Duration vs `maxDuration`/SLA.
- Items read/written/skipped per run; throughput (items/s).
- Dead-lettered items.
- Last commit age (progress).
- JVM heap/GC.
- Pod restarts and OOM kills.
- Queue wait (P2: `volcano_e2e_job_scheduling_latency_milliseconds`).
- Cost per run (P3, OpenCost).

Generated by `hack/dashboards/build-app-dashboard.py` and stamped per application by the multienv composition.

### 11.3 Alerts (composed per `BatchJob` as `PrometheusRule <name>-batch`)

Severity `auto`: critical in prod, warning elsewhere. Routed by the team label through Alertmanager → Grafana OnCall.

| Alert | Expression (shape) | For | Phase |
|---|---|---|---|
| `BatchRunFailed` | The latest run failed: `kube_job_failed{namespace=NS,condition="true"} == 1 and on(namespace,job_name) topk by (namespace,label_adhar_io_batch_job) (1, kube_job_created{namespace=NS} * on(namespace,job_name) group_left(label_adhar_io_batch_job) kube_job_labels{label_adhar_io_batch_job="NAME"})`. It resolves when a newer run exists. | 1m | 1 |
| `BatchScheduleMissed` | `time() - kube_cronjob_next_schedule_time{namespace=NS,cronjob="NAME"} > 600 and on(namespace,cronjob) kube_cronjob_spec_suspend == 0`. The time zone comes from `spec.timeZone` [S55]. | 0m | 1 |
| `BatchNoRecentSuccess` | `time() - kube_cronjob_status_last_successful_time{…} > staleAfter` | 0m | 1 |
| `BatchRunTooLong` | `time() - kube_job_status_start_time{…} > maxDuration and on(namespace,job_name) kube_job_status_active > 0` (joined to the batch-job label) | 0m | 1 |
| `BatchRunStuckPending` | A run pod in `kube_pod_status_phase{phase="Pending"}` for 15 min | 15m | 1 |
| `BatchRunOOMKilled` | `kube_pod_container_status_last_terminated_reason{reason="OOMKilled"}` joined to `kube_pod_labels{label_adhar_io_batch_job="NAME"}` | 0m | 1 |
| `BatchGangPending` / `BatchQueueSaturated` | PodGroup Pending/Inqueue > 30m; leaf allocated ≥ capability with in-queue demand | 30m | 2 |
| `BatchNoProgress`, `BatchDeadLetterGrowing`, `BatchDeadlineEndangered` / `Missed` | Kit gauges and observer SLA metrics. These series live in Mimir, while the in-cluster Prometheus evaluates PrometheusRules and the Mimir ruler is off (`observability/mimir/values.yaml:120-121`). P3 therefore enables one ruler replica and Alloy's `mimir.rules.kubernetes` loader [S90] for rules labelled `adhar.io/rules-target: mimir` (production profile only). | — | 3 |

### 11.4 SLAs

- **P1** `maxDuration`: an alert, plus `activeDeadlineSeconds` as the hard stop.
- **P3** `finishBy HH:MM TZ`. The observer computes expected end = start (or the next schedule) + p90 of the last 20 durations, and emits:
  - `io.adhar.batch.deadline.endangered` when the expected end passes the deadline;
  - `io.adhar.batch.deadline.missed` when the deadline passes without success.

  It also publishes `adhar_batch_deadline_seconds_remaining`. Daylight-saving time is handled in Go with the IANA zone, not in PromQL.

---

## 12. Events: the `adhar.batch` domain (Phase 3)

- **Taxonomy.** `TopicBatch = "adhar.batch"` is added to `Topics` in `platform/events/taxonomy.go:70-83`. `TopicFor` derives the domain, so new types route the moment they are named. The `KafkaTopic adhar.batch` (3 partitions, `retention.ms: 7776000000` = 90 days, audit-grade like `adhar.cluster`) goes in `data/kafka-operator/manifests/events-bus.yaml`. `CONTRACT.md` gets a batch section, and `TestTheEventTopicsAreDeclaredInTheStack` guards it: undeclared topics are auto-created with one partition and the broker's retention.
- **Types** (past tense; subject `<namespace>/<batchjob>`, the record key, so one job's events stay ordered):

  | Type | When |
  |---|---|
  | `io.adhar.batch.run.requested` | A run appeared with trigger manual / restart / backfill / dependency / workflow / ops (carries the actor) |
  | `io.adhar.batch.run.started` | The run's first pod is Running |
  | `io.adhar.batch.run.succeeded` | Job Complete / Volcano Job Completed (summary inline) |
  | `io.adhar.batch.run.failed` | Job Failed (`BackoffLimitExceeded` / `PodFailurePolicy` / `DeadlineExceeded`) or Volcano Failed/Terminated (exit code + summary) |
  | `io.adhar.batch.run.stopped` / `run.resumed` | Suspend/unsuspend, `AbortJob`/`ResumeJob` |
  | `io.adhar.batch.run.skipped` | The summary says already-complete, duplicate or calendar |
  | `io.adhar.batch.run.lost` | A pod ended with **no** summary (OOMKilled, SIGKILL, node lost) — the outcome is in the event |
  | `io.adhar.batch.schedule.held` / `schedule.released` / `schedule.missed` | Hold annotation transitions; next schedule passed with no run |
  | `io.adhar.batch.deadline.endangered` / `deadline.missed` | §11.4 |
- **Data:**
  - `batchJob`, `namespace`, `application`;
  - `run{name,kind,uid,trigger,actor,scheduler,queue}`;
  - `springBatch{job,instanceId,executionId,status,businessDate,identifying (sensitive masked)}`;
  - `counts{read,written,skipped,deadLettered}`;
  - `start`, `end`, `durationSeconds`;
  - `outcome{reason,exitCode,message}`.

  Extensions `adharorganisation|team|project|environment|plane|correlationid` are taken from the **namespace labels**; the correlation id is the run UID.
- **Publisher: the batch observer** (`platform/controllers/batch/`) in the existing manager.
  - Label-selected informers on Jobs and pods (`adhar.io/workload=batch`) and on `BatchJob`s.
  - Volcano informers are started **only after discovery shows the CRDs**. A watch on an absent CRD hits the cache-sync timeout that has killed the manager before.
  - Publishing follows the contract: advisory, a bounded drop-oldest queue, transitions only, outcome inside the event, opt-in through `ADHAR_EVENTS_BRIDGE_URL`.
- **`dependsOn` is evaluated by the observer from Kubernetes objects, not from the bus**, so it works in the local profile and through bus outages:
  - When every listed job has a succeeded run for business date D, it creates `<name>-<hash8>` with `businessDate=D` and trigger `dependency`, as a run admitted by the integrity policy.
  - The obvious alternative is not used. Argo Events Sensors would need an EventBus, and the `argo-events` package ships the CRD and controller but **no EventBus** (`application/argo-events/manifests/install.yaml`). A NATS JetStream bus per environment namespace is memory the clusters do not have.

### 12.1 The workflow step `adhar-batch-steps/run` (the contract design 0026 needs)

Design 0026 makes Argo Workflows the orchestration engine and lists `adhar-batch-steps/run` as "owned by 0025". This is that contract.

| Aspect | Contract |
|---|---|
| Object | `ClusterWorkflowTemplate adhar-batch-steps`, template `run`, shipped in 0026's step catalogue in the `argo-workflows` package (Phase 3, once both packages exist) |
| Inputs | `batchJob` (required), `namespace` (default `{{workflow.namespace}}`), `parameters` (JSON object, default `{}`), `wait` (default `true`), `timeout` (default: the `BatchJob`'s `activeDeadlineSeconds` + 10 min) |
| Behaviour | Exactly `adhar batch run <batchJob> -n <ns> --wait -o json`: read `<batchJob>-run`, validate against `parameters.json`, name `<batchJob>-<hash8>` (naming v1), label `adhar.io/batch-trigger: workflow`, annotate `adhar.io/batch-workflow: {{workflow.name}}` |
| Idempotency | An existing run with the same name that completed → succeed without re-running (Spring Batch would no-op anyway). One that failed → create `-r<n>`. One still running → wait for it. |
| Outputs | `run`, `status` (`Succeeded` \| `Failed` \| `Stopped`), `exitCode`, `executionId`, `summary` (the termination summary JSON) |
| Result | The step succeeds when the run ends with exit 0, including SKIPPED. Otherwise it fails, carrying the exit code and summary message. |
| Implementation | One container step running the pinned `adhar` CLI image, as the workflow's executor ServiceAccount (`adhar-workflow` in 0026). 0026 grants it create/get/list/watch/delete on `jobs` and get on `batchjobs`; this step also needs `get` on `configmaps` and `pods` (for the summary). |
| Admission | A non-schedule trigger: `adhar-batch-run-integrity` applies (the run must equal the template). Kyverno stamps the executor ServiceAccount as the actor. |
| Queues | The run waits in the `BatchJob`'s queue; the workflow's own pods never wait in a batch queue (0026 §0.1) |
| Tests | Render test of the template; Phase-3 e2e with `argo-workflows` enabled: a two-step workflow (batch run → HTTP check) succeeds, and a run that exits 3 fails its step with `exitCode: 3` in the outputs (A3.8) |

---

## 13. Failure modes

| # | Failure | What happens | Recovery | Test |
|---|---|---|---|---|
| F1 | OOM kill mid-run | Execution left STARTED; exit 137 → Count → replacement pod (after the old one is terminal [S49]) | Runner: owner pod gone → `recover` → restart from last commit; `BatchRunOOMKilled` fires | A1.1 |
| F2 | Preemption, eviction, drain | SIGTERM → STOPPED at chunk boundary; `DisruptionTarget` → Ignore | Replacement restarts the STOPPED instance | A1.1, A2.4 |
| F3 | Chunk longer than grace → SIGKILL | STARTED left | Lease stale → `recover` → restart; the kit logs "stop not reached within grace" | KIT-1.3 test |
| F4 | Node lost | Pod Unknown; replacement waits for a terminal pod | Lease stale → `recover` | e2e (cordon + delete node, Phase 2 canary) |
| F5 | JobRepository unreachable at start | exit 1 → Count with backoff | Retries; then Failed → alert | KIT-1.2 |
| F6 | JobRepository unreachable mid-run | Transient retry preset; then FAILED → exit 5 | Count; restart resumes | KIT-1.4 |
| F7 | Double fire / duplicate manual submit | `AlreadyExists` (same name) or AlreadyRunning/AlreadyComplete | Second run exits 0 `skipped` | A1.2 |
| F8 | Invalid parameters | exit 2 → FailJob immediately | Fix the parameters; the Console shows the summary message | A1.9 |
| F9 | Data error beyond skip limit | exit 3 → FailJob; rejects in the DLQ | Fix the data → Restart (P3 DLQ replay) | KIT-1.4 |
| F10 | Image pull denied (unsigned or wrong registry) | Pod Pending/ErrImagePull | `BatchRunStuckPending`; `activeDeadlineSeconds` ends the run | A1.6 |
| F11 | kube-controller-manager unhealthy (leader-election collapse on a small master) | No Jobs created | `BatchScheduleMissed`. **Read `kube-system` pods first**; this is one fault with many symptoms. | alert test |
| F12 | Volcano scheduler down | Runs with `schedulerName: volcano` stay Pending | `BatchRunStuckPending`; hold, then `scheduler: kubernetes` per job, or remove the descriptor cluster-wide | A2.10 |
| F13 | Volcano admission down | Volcano Job creation refused (`failurePolicy: Fail` [S9]) | Alert on admission availability; 2 replicas; manual runs retry | canary |
| F14 | Gang cannot fit | PodGroup Pending; no partial allocation [S20] | Prod opt-in → autoscaler; otherwise waits; `BatchGangPending` | A2.1, A2.6 |
| F15 | Kyverno down | Manual runs refused (fail closed); scheduled runs unaffected (match conditions on the CronJob controllers' identities) | Restore Kyverno | PLAT-1.1 test, incl. a forged `batch-trigger: schedule` label being denied |
| F16 | Kargo promotes while a run is in flight | The running Job keeps its spec; the next run uses the new image | P3 version-safe restart guard warns when a FAILED instance restarts on a different digest with changed step names | KIT-3.5 |
| F17 | Crossplane down | Definition changes wait; existing CronJobs keep firing | — | — |
| F18 | Flyway migration fails at start | exit 2 → FailJob (no retry storm) | Fix the migration | KIT-0.4 |
| F19 | Zombie writer after `recover` | Its commit fails on the StepExecution version; its business writes roll back with it (INV-4) | Restarted partition resumes | KIT-2.2 |
| F20 | Stale partition request from a failed attempt | Group cleared by the manager; worker guard discards it | — | KIT-2.1 |
| F21 | Summary exceeds 4096 bytes | Kubelet truncates [S53] | Kit caps at 3.5 KB; parsers tolerate truncation | KIT-1.2, CON-1.2 |
| F22 | Event bus down | Events dropped (counter); runs and `dependsOn` unaffected | — | PLAT-3.1 |
| F23 | API server memory pressure from Volcano CRDs/watchers | Control-plane instability | Canary measurement gate; JobFlow off | A2.8 |
| F24 | DST / time zones | CronJob `timeZone` handles DST [S50]; business date taken in the job's zone | — | KIT-1.2 |
| F25 | Hold annotation reverted by Argo CD | Not expected (field not in Git) | — | PLAT-1.6c |

---

## 14. Capacity on memory-constrained clusters

- **Scheduler footprint.**
  - Volcano: ≈0.4 CPU / ≈1 GiB of requests *(estimate)*, ≈3% of the 34 GiB headroom.
  - Armada's stack: ≈10 GiB, ≈29% (ADR-0028).
  - The observer adds label-filtered informers to the existing manager.
  - `core/adhar-batch` runs no workloads.
- **Batch ceiling.** Root queue capability defaults to 30% of the CPU and 20% of the memory of `maxWorkers × allocatable` *(decision)*.
  - On the reference cluster that is ≈22 CPU / ≈24 GiB. That matches the research's proposal and leaves ≈10 GiB of today's ≈34 GiB headroom for services.
  - Memory gets the smaller fraction because memory, not CPU, is what runs out.
  - The platform team sets the real value in config.
  - Without Volcano (P1), the ceiling is the namespace ResourceQuota (16 CPU / 32 GiB per environment namespace) and the team allowance.
- **JVM sizing.**
  - The Paketo memory calculator sizes heap = limit − fixed regions. Fixed regions at the default 250 threads come to ≈567 MB.
  - With 50 threads, a 64 MB code cache and a Spring Batch metaspace of ≈150 MB *(estimate)*, the fixed regions are ≈280 MB. A 1536Mi limit leaves ≈1.2 GiB of heap.
  - **1 GiB is the practical floor**: `BatchContractAssertions` fails below it, and the LimitRange default of 512Mi would not start.
- **Concurrency on today's cluster.** Within the ≈24 GiB default ceiling:
  - ≈16 single-pod runs at 1.5 GiB;
  - or one 1+8 gang at 1.5 GiB per pod (13.5 GiB) plus about six single runs.
  - Batch never grows the cluster unless prod opts in, and the autoscaler is already at `maxWorkers=10` there.
- **JVM per run.** A kit-starter JVM measured 154 s to start on a busy node (template comment, 2026-10-14).
  - Jobs that run more often than every 15 minutes belong in embedded mode in a service, or in a Function.
  - The composition reports cadences under 10 minutes on `Placement`.
  - JDK 25 AOT cache ergonomics (JEP 514/515 [S91]) are evaluated in P3 to cut start time.
- **Control plane.**
  - Volcano adds ≈10 CRDs (≈0.58 MB without JobFlow) to 354; the `BatchJob` XRD adds one.
  - On a master at 97% memory this is measured, not assumed: canary step A2.8 records `kube-apiserver` RSS before and after.
- **Local profile.**
  - Volcano is off.
  - The template's dev overlay suspends the schedule and runs on demand at 1 GiB.
  - Kafka, Alloy, Mimir, Kargo and the observer bus publishing are all absent, and nothing in Phase 1 needs them.

---

## 15. Phased implementation plan

Each phase is independently shippable: it leaves the platform working with the previous phases alone. A phase is done when every acceptance criterion passes on the named evidence.

### Phase 0 — Kit foundation (adhar-kit 0.2.0; no platform change)

**Scope:** K1–K9 and parameter masking.
- Batch auto-configuration ordered and gated on `Job` beans.
- The starter stops pulling batch.
- JDBC repository with the Flyway schema.
- `BatchOperator` and the step API migrated off removal-scheduled APIs.
- Listeners attached.
- Embedded scheduler fixed.
- Metrics de-duplicated.

| # | Acceptance criterion | Evidence |
|---|---|---|
| A0.1 | With real auto-configuration (`@SpringBootTest`) on Testcontainers Postgres: the JobRepository is JDBC, the `batch` schema exists, and `BatchOperator`, `SchedulerLock` and the kit gauges are beans | KIT-0.10 test |
| A0.2 | A service from `springboot-web-service` on kit 0.2.0 has no batch beans and no `adhar_scheduler_lock` table | IT in the kit with a web context |
| A0.3 | Two embedded-mode contexts sharing one database, firing the same cron slot, produce **exactly one** JobExecution | KIT-0.7 test |
| A0.4 | The module compiles with `-Xlint:removal -Werror` against Spring Batch 6.0.x | build |
| A0.5 | Restart: fail at item N, restart, no duplicates (JDBC repository) | `RestartScenario` test |
| A0.6 | JaCoCo ≥ 80% (BUNDLE/INSTRUCTION) | build |

**Ship:** kit 0.2.0 to Nexus. Templates move their pins when they next change.

### Phase 1 — First production batch job on native Kubernetes

**Scope:**

| Area | Scope |
|---|---|
| Kit 0.3.0 | Runner, identity, recovery and lease, graceful stop, exit codes, termination summary, `@AdharBatchJob`, `AdharSteps` + fault tolerance + JDBC DLQ, keyset/upsert/JSONL, S3 after the spike, local partitioning, OTLP metrics, tracing filter, MDC, `@AdharBatchTest` |
| Platform | `core/adhar-batch`; `BatchJob` XRD and composition (native); Application `type: batch` with verification and dashboard; Argo CD health; KSM label allowlist; autoscaler drain and scale-up rules; Environment operator binding; Bucket credential spike/decision; soc2 exclusion |
| CLI | list, describe, runs, run, logs, stop, resume, restart, hold, release |
| Console | Jobs, Job drawer, Runs, Run detail and actions |
| Template | `springboot-batch-job` |

| # | Acceptance criterion | Evidence |
|---|---|---|
| A1.1 | Deleting the run pod mid-chunk, or OOM-killing it, ends in one COMPLETED execution of the same JobInstance, resumed from the last commit, with **no duplicate rows** | e2e on Kind and on the DO cluster |
| A1.2 | Two runs for the same business date (double fire, or a manual duplicate) → exactly one execution; the other exits 0 `skipped` | e2e |
| A1.3 | Stop from the Console → STOPPED within one chunk plus grace; Resume → COMPLETED | e2e + Console test |
| A1.4 | A failed run fires `BatchRunFailed` within 5 minutes; the Argo CD Application and the Kargo stage stay Healthy; the next promotion is gated only by failures inside its own window | live |
| A1.5 | The Console shows the run's step counts from the summary. In the production profile, metrics of a run pod that lived **under 30 s** are queryable in Mimir. | Mimir query (verify at the reader) |
| A1.6 | A user without the team's RBAC can neither see nor operate the job. A prod operator can run the deployed template, but Kyverno denies the same run with a different image or extra env. | impersonation test + chainsaw |
| A1.7 | Local profile: `adhar up` → scaffold `springboot-batch-job` → a manual run and a scheduled run complete on Kind with no Kafka, Kargo, Alloy or Volcano | `tests/e2e/batch` |
| A1.8 | The autoscaler never drains a node with a running `safe-to-evict: "false"` batch pod; a dev batch backlog causes no scale-up | `decision_test.go` + live |
| A1.9 | Invalid parameters → exit 2 → Job Failed with zero retries; the Console shows the validation message | e2e |
| A1.10 | The scaffolded repository's first app-ci run is green (ITs on kubedock, JaCoCo ≥ 80%, scans, signing), and Kargo promotes dev → test → prod with the batch verification | live |

**Ship gate:**
- Controller release carrying the XRD and compositions.
- Stack push carrying `core/adhar-batch`; read `git status -- platform/stack/` before `adhar upgrade`.
- Templates pushed to GitHub.
- Console release with a pinned tag.

### Phase 2 — Volcano: queues, fair share, gang

**Scope:**

| Area | Scope |
|---|---|
| Platform | `core/volcano` (pinned v1.16.x, alpha, disabled); Argo CD health and exclusions for Volcano kinds; Team-composed leaf queues + queue RBAC; Kyverno queue binding + Volcano pod standards; composition scheduler branch (single → `schedulerName`; partitioned → Volcano CronJob / Indexed fallback); autoscaler PodGroup demand; KSM custom-resource state; Volcano alerts; **1.37 canary CI job** |
| Kit 0.4.0 | Remote partitioning on the JDBC transport, role switch, stale guard, worker-only stop |
| CLI | `queues`; Volcano-aware runs/logs/stop |
| Console | Queues view; gang status |
| Template | `springboot-batch-partitioned` |

| # | Acceptance criterion | Evidence |
|---|---|---|
| A2.1 | With room for N pods but not N+1, no pod of an N+1 gang starts and nothing is half-allocated | canary e2e |
| A2.2 | Killing a worker mid-partition → `RestartJob` → the new execution re-runs only unfinished partitions; exact row count | canary e2e + KIT-2.2 |
| A2.3 | The manager completing ends the workers within seconds (`CompleteJob`), and the Volcano Job is Completed | canary e2e |
| A2.4 | Prod demand reclaims capacity from a running dev gang over its share; the dev run stops gracefully as STOPPED and later completes | canary e2e |
| A2.5 | A Volcano Job in team A's namespace naming team B's queue is denied | chainsaw |
| A2.6 | A pending opted-in prod gang that fits one new worker triggers exactly one scale-up; a dev gang, or any gang at queue capability, triggers none | `decision_test.go` + canary |
| A2.7 | Volcano never evicts a pod it did not schedule | canary e2e |
| A2.8 | `kube-apiserver` RSS and CRD bytes before/after Volcano are recorded in the package README; the delta fits the master | canary measurement |
| A2.9 | The batch e2e suite passes on Kind v1.37 with Volcano, in CI | CI |
| A2.10 | Removing Volcano (descriptor gone) leaves every `BatchJob` running on the native path with no manual edits; partitioned jobs degrade to the Indexed Job without a gang | canary e2e |

**Production gate:** Volcano v1.17 [S7] green on A2.1–A2.10, or a maintainer's written sign-off on a v1.16 patch with the canary green. Until then production runs Phase 1's native path.

### Phase 3 — Enterprise operations

**Scope:**
- Batch observer: events, `run.lost`, `dependsOn`, SLA.
- `adhar.batch` topic.
- Calendars.
- Smoke-run gate (spike: Kargo's analysis supports a Job metric).
- Mimir ruler + Alloy rule loader; app-metric alerts.
- Kyverno batch pod standards → Enforce.
- Scorecard batch rules: restart test present, alerts present, JDBC repository, deadline set, 7-day success rate.
- Housekeeping (retention), ops jobs (abandon/recover/stop/status), DLQ replay, backfill, control totals, version-safe restart guard, staged S3 writes, Kafka/fixed-width/XML presets.
- Business notifications; OpenCost view.
- Launching a run from a Spring service (`adhar-kit-kubernetes` `BatchRunLauncher`, same naming contract).
- The `adhar-batch-steps/run` workflow step for design 0026's Argo Workflows (§12.1).
- Console History and Tonight; CLI backfill, abandon, recover, dlq, history.

| # | Acceptance criterion |
|---|---|
| A3.1 | Every run transition yields exactly one event of the right type, with hierarchy extensions from the namespace; an OOM-killed run yields `run.lost` |
| A3.2 | `dependsOn`: B starts once for date D only after A and C succeeded for D; it works in the local profile with the bus off |
| A3.3 | Backfill of 7 dates with `--parallel 2` never has more than 2 running and is idempotent on re-run |
| A3.4 | A job whose expected end crosses `finishBy` emits `deadline.endangered` before the deadline |
| A3.5 | Retention keeps the newest restartable FAILED/STOPPED instance |
| A3.6 | DLQ replay re-processes only rejected items |
| A3.7 | The smoke-run gate blocks a test → prod promotion whose smoke run fails |
| A3.8 | With `argo-workflows` enabled, a workflow step `adhar-batch-steps/run` starts a run, waits, and returns its summary; re-running the workflow with the same parameters does not re-execute a completed run |

### Phase 4 — Scale and fleet

Remote chunking (RabbitMQ `Queue` XR); a tainted batch node group with autoscaler support; Iceberg/Parquet I/O; OpenLineage → OpenMetadata (opt-in); archive of run summaries to the object store; JobFlow only on demand; adopting Volcano `NamespaceQueue`/`fairshare` when they leave alpha; evaluating the native Workload API [S47][S48] and Kueue [S60] at Kubernetes 1.38+.

**Karmada placement** of batch onto data planes has its own design addendum, written when the first DataPlane registers. The hub has 0 today. The constraint: `BatchJob`s compose on the control plane, so the composed run objects must be created in Karmada's API server, with Karmada's built-in Volcano Job interpreter reflecting status [S28]. Volcano Global stays out [S23].

### 15.1 Decisions fixed by the implementation plan (2026-11-04)

The implementation plan runs several tasks in parallel, in different repositories. These decisions fix the contracts between those tasks where this design left a choice open. Each one supersedes any looser wording elsewhere in this document.

| # | Decision | Why |
|---|---|---|
| D1 | **Naming v1 canonical encoding** and the shared vectors in §8.3 | The Go CLI, the TypeScript Console and the Java kit compute run names independently. Without fixed escaping, they would disagree on any value with a control character, U+2028 or non-ASCII. |
| D2 | **`adhar batch run --wait` carries §12.1's idempotency.** An existing run of the same name that completed is a success with no new run. One still running is waited for. One that failed is restarted as the next `-r<n>`. One that is suspended is reported as `Stopped`, with a non-zero exit and a hint to `resume`. `-o json` prints `{run, namespace, batchJob, kind, status, exitCode, executionId, summary}`, where `status` is `Succeeded`, `Failed`, `Stopped` or `Running`. The CLI's process exit code is the run's: 0 includes SKIPPED. | §12.1 defines the workflow step as exactly this command, and the Phase-3 smoke gate (D6) reuses it. Implementing the semantics once in the CLI (Phase 1) keeps both thin. |
| D3 | **XRD fields arrive with the phase whose composition renders them.** Phase 1 accepts §6.2 except `mode: partitioned`, `workers`, `gridSize`, `resources.workerCpu`, `resources.workerMemory`, `retry.maxRetry` and the partitioned validation rule. Those come in Phase 2. Phase 1 also leaves out every field marked P3 (`schedule.calendar`, `sla.finishBy`, `sla.timeZone`, `dependsOn`, `retention`, `smoke`), which Phase 3 adds. | The XRD is structural and strict. An earlier control plane therefore rejects a field it cannot honour, instead of accepting a definition and silently ignoring part of it. |
| D4 | **Retention runs after the business run.** The composition passes `retention.days` and `retention.keepLast` to every run as `ADHAR_BATCH_RETENTION_DAYS` and `ADHAR_BATCH_RETENTION_KEEP_LAST`. These are composition-owned and not client-settable, so `adhar-batch-run-integrity` compares them. After a COMPLETED run, the runner deletes JobRepository metadata older than `days`. It keeps the newest `keepLast` instances, and always the newest restartable FAILED or STOPPED instance (A3.5). A housekeeping failure is logged and counted, and never changes the run's exit code. The ops verb `housekeeping` runs the same code on demand. No housekeeping CronJob is composed. | A JVM per job per day only for housekeeping would cost memory (§14) and another start-up (R9). |
| D5 | **Calendars** (this resolves open question 3). A calendar is a ConfigMap labelled `adhar.io/batch-calendar: "true"`. It holds the key `calendar.yaml`, with `name`, `timeZone` (optional; defaults to the job's), `weekendDays` (default `[SATURDAY, SUNDAY]`), `holidays` (ISO dates) and `businessDays` (ISO dates that override weekends). `schedule.calendar` names one; the namespace is checked first, then `adhar-system`. Teams author calendars in their overlays. The platform team authors platform calendars in `core/adhar-batch`, which ships `weekdays`. The composition copies the resolved calendar into `ConfigMap <name>-calendar`, mounts it read-only at `/etc/adhar/batch/calendar/calendar.yaml` and sets `ADHAR_BATCH_CALENDAR` to that path. An unresolvable calendar is `PrerequisitesReady=False`. When the business date is not a business day, the runner exits 0 with `outcome: skipped` and `reason: calendar` for the triggers `schedule`, `dependency` and `backfill`. A missing or invalid calendar file is exit 2. | Teams need their own holidays, and a platform default must exist. The copy keeps the run pod's inputs inside its own namespace. |
| D6 | **Smoke runs** (Phase 3) are `adhar batch run <job> -n <ns> --smoke --wait -o json`. The parameters come from `spec.parameters.smoke.parameters` and the trigger is `smoke`. The run is named `<batchjob>-smoke-<sha7>`, where `sha7` is the first 7 hex characters of the image digest in the run template. A re-verified freight therefore reuses its run. | §8.3 named the run without saying what `sha7` hashes. |
| D7 | **Scheduler descriptor keys.** `ConfigMap adhar-batch-scheduler` carries `scheduler`, `version` and `gang`, plus `rootQueue` (`adhar-batch`), `prodDeservedCPU` and `prodDeservedMemory`. All are rendered at seed time from `globalSettings.batch`. | The Team composition reads the prod `deserved` share from it (§4.3). Nothing else on the control plane carries that config value. |
| D8 | **Open questions 1, 2 and 4 are resolved as proposed.** `batchOperations` defaults to `team`. The batch ceiling is 30% of the CPU and 20% of the memory of `maxWorkers × worker allocatable`; prod `deserved` defaults to 2 CPU and 4 GiB. `adhar.batch` events carry identifying parameter values, with `sensitive` ones masked. | Operators can run only the deployed template (§10.3). Without the values, history, backfill and `dependsOn` cannot name a business date. |
| D9 | **Kit versions.** 0.2.0 (Phase 0), 0.3.0 (1), 0.4.0 (2), 0.5.0 (3) and 0.6.0 (4). Templates pin exact versions. | §5.1 numbered only 0.2.0–0.4.0. |
| D10 | **`adhar-batch-steps`** ships as `platform/stack/packages/application/argo-workflows/manifests/catalog/adhar-batch-steps.yaml.tmpl`, with the image `{{ .PlatformImage }}`, following design 0026 §5.1 and §5.7. This supersedes the `manifests/steps/` path in §16.2 PLAT-3.10. | The package belongs to design 0026, and its catalogue directory is where the designer's palette discovers steps. |
| D11 | **Remote chunking** (Phase 4) is `mode: chunked`. It renders the partitioned gang shape and sets `ADHAR_BATCH_MODE=chunked`, and the kit selects its remote-chunking beans from that. The AMQP connection arrives through the app's `Queue` XR Secret via `envFrom`. Lineage is opt-in through `spec.parameters.lineage.enabled`, which sets the standard `OPENLINEAGE_URL` and `OPENLINEAGE_NAMESPACE`. | Remote chunking reuses the manager-plus-workers lifecycle (§4.5). The OpenLineage client's own variables need no kit-specific contract. |
| D12 | **The Console reads `adhar.batch` history** through the API server's service proxy to the events bridge, exactly as `adhar events` does. The Console's ServiceAccount is granted `get` on `services/proxy` for that bridge Service only. | This adds no second credential and no new network path (§12). |
| D13 | **Termination summary v1** adds two optional fields to §5.13. `reason` is present when `outcome` is `skipped` (`already-complete`, `duplicate`, `calendar`) or `invalid` (`parameters`, `unknown-job`, `incrementer`, `resourceless-repository`, `migration`, `business-date`). `stepsTruncated: true` is set when step detail was dropped to stay under 3,584 bytes. `instanceId`, `executionId` and `status` are absent when no execution exists. Parsers treat every field as optional and tolerate truncation. | Exit 0 covers four different outcomes, and the Console and CLI must tell them apart (§7.2, §8.1). |
| D14 | **Batch node pools** (Phase 4) are placed by policy, not by the composition. Nodes carry the label `adhar.io/node-pool: batch` and the taint `adhar.io/batch=true:NoSchedule`. While a batch pool exists, a Kyverno mutate rule in `core/adhar-batch` adds the toleration and a preferred node affinity to pods labelled `adhar.io/workload: batch`. | Every existing `BatchJob` moves onto the pool without a definition change, and a cluster without a pool is unaffected. |

---

## 16. Work breakdown per repository

Conventions for every task:
- One task, one reviewable change, with its tests in the same change.
- **Mutation-test each guard**: revert the fix and watch the guard fail.
- Generated manifests change only through their generators and values, never by hand.
- XRD/composition changes reach clusters with a controller release; stack changes with a Gitea push.
- Kit versions are immutable in Nexus: bump the version and the template pins.
- Template renders go to scratch directories only.
- Nothing is committed or pushed by agents; the user commits.

### 16.1 adhar-kit (`/Users/tapas-pc/Documents/Work/Adhar/AdharWS/adhar-kit`)

| ID | Phase | Files | Change | Tests | Depends |
|---|---|---|---|---|---|
| KIT-0.1 | 0 | `adhar-kit-batch/.../BatchFacade.java`; `adhar-kit-starter/.../AdharFacade.java:329-332` | Self-initialising facade (as `NotificationFacade.getInstance()`); `getBatch()` works | facade test without mocks | — |
| KIT-0.2 | 0 | `config/BatchAutoConfiguration.java` | `@AutoConfiguration(afterName=…)` (§5.18); `@ConditionalOnBean(Job.class)` | `@SpringBootTest` real auto-configuration (A0.1) | — |
| KIT-0.3 | 0 | `adhar-kit-starter/pom.xml:151-155` | `adhar-kit-batch` optional | web-context IT (A0.2) | KIT-0.2 |
| KIT-0.4 | 0 | `adhar-kit-batch/pom.xml`; `repository/*`; `src/main/resources/com/adhar/kit/batch/db/postgresql/V6_0_0__…`, `V6_0_1__…` | starter-batch-jdbc, Flyway; `BatchSchemaMigrator`; prefix wiring; resourceless guard | PG 18 schema test; concurrent-launch test; prefix test; Flyway-failure exit 2 | — |
| KIT-0.5 | 0 | `operator/BatchOperator.java`; `step/*`; `fault/*`; `retry/*` (deprecated delegates) | `JobExecution`-based operator + `recover`; `AdharSteps` over `ChunkOrientedStepBuilder`; Framework-7 retry and skip presets | operator tests on a real repository; A0.4 | KIT-0.4 |
| KIT-0.6 | 0 | `listener/*`; new post-processor | Attach the kit listeners to every `AbstractJob`/`AbstractStep`; wire `BatchJobLogger`; stop logging parameters | listener-attached test; masked-log test | KIT-0.2 |
| KIT-0.7 | 0 | `scheduler/BatchScheduler.java`, `lock/*` | Synchronous launch, renewed lock, nominal-time identity, time zone, misfire threshold, Flyway lock table | two-context race test (A0.3) | KIT-0.4, 0.5 |
| KIT-0.8 | 0 | `metrics/*` | Remove duplicates behind `metrics.legacy`; new gauges | meter-registry test | KIT-0.6 |
| KIT-0.9 | 0 | `config/BatchProperties.java`, README, `CHANGELOG.md` | Property reference (§5.17); README rewrite | — | all |
| KIT-0.10 | 0 | `adhar-kit-batch/src/test/**` | Guard suite for A0.1–A0.6 | — | all |
| KIT-1.1 | 1 | `annotation/*`, `params/*` | `@AdharBatchJob`, `@JobParam`, registry, resolver, validator, `SecretParameterGuard`, `RunIdentity` (naming v1 vectors) | resolver/validator tests; vector test | KIT-0.x |
| KIT-1.2 | 1 | `runtime/*`, `AdharBatchApplication` | Runner (§5.5), `RunContextResolver` + `KubernetesOwnerLookup` (JDK HttpClient), exit codes, `BatchExitCodeGenerator`, termination summary | runner tests with a fake API server (WireMock); `RunnerExitCodeIT`; summary-cap test | KIT-1.1 |
| KIT-1.3 | 1 | `runtime/{ExecutionLease,JdbcExecutionLease,RecoveryPolicy,GracefulStopLifecycle}` | Lease, heartbeat, stale recovery, owner-pod check, graceful stop | SIGTERM-in-process test; stale-lease test; terminating-owner wait test | KIT-1.2 |
| KIT-1.4 | 1 | `fault/*` | DLQ table sink and listener; classification for exit codes 3/5 | skip → DLQ test; skip-limit → exit 3 | KIT-0.5 |
| KIT-1.5a | 1 | spike under `adhar-kit-batch/src/test` | Spring Cloud AWS 4.x `Resource` against a RustFS container, path-style | spike report in the PR | — |
| KIT-1.5 | 1 | `reader/*`, `writer/*` | keyset, upsert, JSONL; S3 builders if KIT-1.5a passed | Testcontainers tests | KIT-1.5a |
| KIT-1.6 | 1 | `partition/LocalPartitioning`, `partitioner/*` | Local partitioning presets, SQL/prefix partitioners | partitioned restart test | KIT-0.5 |
| KIT-1.7 | 1 | `adhar-kit-metrics` (OTLP), `telemetry/*`, `adhar-kit-logging` MDC | OTLP push mode; tracing filter; MDC | span-count test (<50 for 10k items); env-mapping test | KIT-0.8 |
| KIT-1.8 | 1 | `event/BatchRunEvent` | In-JVM run events | listener test | KIT-1.2 |
| KIT-1.9 | 1 | `adhar-kit-test-commons` | `@AdharBatchTest`, `RestartScenario`, `BatchContractAssertions` | self-tests | KIT-1.1 |
| KIT-1.10 | 1 | root `pom.xml`, BOM | Release 0.3.0 | full build | all P1 |
| KIT-2.1 | 2 | `partition/*` | Remote partitioning (§5.10), JDBC transport, `V6_0_2__partition_channel.sql`, stale guard, worker stop | multi-context ITs | KIT-1.x |
| KIT-2.2 | 2 | tests | Kill worker mid-partition → restart re-runs only unfinished; zombie fenced (F19) | — | KIT-2.1 |
| KIT-2.3 | 2 | poms | Release 0.4.0; remove `retry/*` and legacy meters | — | — |
| KIT-3.x | 3 | `ops/*`, `fault/*`, `writer/*`, `adhar-kit-kubernetes` | Housekeeping, ops verbs, calendar evaluation, control totals, version-safe restart, DLQ replay, staged S3, Kafka/fixed-width/XML presets, `BatchRunLauncher`, notification bridge | per item | — |
| KIT-4.x | 4 | `partition/*`, `reader/*`, `writer/*` | Remote chunking (RabbitMQ), Iceberg/Parquet, OpenLineage | per item | — |

### 16.2 adhar (`/Users/tapas-pc/Documents/Work/Adhar/adhar`)

| ID | Phase | Files | Change | Tests | Depends |
|---|---|---|---|---|---|
| PLAT-1.1 | 1 | `platform/stack/packages/core/adhar-batch/{adhar-package.yaml, manifests/{priorityclasses,rbac,kyverno-*}.yaml}`; three appsets; `environments/{local,prod}/config.yaml` | Package (§4.4, §10.2, §10.3); enabled in local + production | `parity_test.go`; `hack/validate-packages.sh`; chainsaw for every policy (integrity allow/deny, a forged `batch-trigger: schedule` label denied, actor stamp, prod-only, hold-only with Argo CD/Crossplane exempt, match conditions); guard that batch classes are `Never` and ≤ 0 | — |
| PLAT-1.2 | 1 | `controlplane/configuration/xrd/batchjob.xrd.yaml`; `compositions/batchjob/{kubernetes.yaml,README.md}`; `rbac/local-compose-rbac.yaml` (cronjobs, configmaps, serviceaccounts, roles, rolebindings, prometheusrules) | XRD + native composition (§6.2–6.5) | `batchjob_composition_test.go` (render golden files for single/on-demand/held/invalid; ready annotations; validation messages); `crossplane render` of the template's XR; `TestNoXRDKindCarriesTheCompositePrefix`, `TestCompositionsAndXRDsAgreeOnKinds` | — |
| PLAT-1.3 | 1 | `xrd/apps.xrd.yaml:66,86`; `compositions/apps/argocd-multienv.yaml`; `hack/dashboards/build-app-dashboard.py:70` | `type: batch`, `batch-jvm`, `$isBatch` | `TestGeneratedDashboardsAreUpToDate`; multienv render test | — |
| PLAT-1.4 | 1 | `compositions/project/local.yaml:82-97,716-860` | `$batches`; batch verification (§6.6); resilience skip | `batch_delivery_test.go` (queries, skip, no `pods-ready` for batch) | PLAT-1.7 |
| PLAT-1.5 | 1 | `compositions/env/local.yaml`, `xrd/project.xrd.yaml` | `batchOperations` + prod operator RoleBinding | render test | PLAT-1.1 |
| PLAT-1.6 | 1 | `hack/argocd/values.yaml`, `values-ha.yaml` → `generate-manifests.sh` → `resources/argocd/install*.yaml` | `BatchJob` health | `argocd_health_test.go` additions; (c) a live test that the hold annotation survives a sync | — |
| PLAT-1.7 | 1 | `observability/kube-prometheus/values.yaml` → regenerated `manifests/install.yaml` | `kube-state-metrics.metricLabelsAllowlist`: `jobs=[adhar.io/batch-job,adhar.io/application,adhar.io/environment]`, `pods=[…same]`, `cronjobs=[adhar.io/batch-job]` | guard test on the rendered `--metric-labels-allowlist` | — |
| PLAT-1.8 | 1 | `platform/controllers/autoscaler/decision.go:477-526,667-685` (+ `decision_test.go`) | §4.7 Phase-1 rules | table tests; mutation-tested | — |
| PLAT-1.9 | 1 | `compositions/bucket/local-rustfs.yaml` | Spike: per-bucket RustFS user + policy; else record the risk (§18) | render + live bucket test | — |
| PLAT-1.10 | 1 | `security/adhar-policy-packs/manifests/soc2.yaml` | Exclude batch pods from the probe rule | chainsaw | — |
| PLAT-1.11 | 1 | `README.md:218`; `CLAUDE.md`; `docs/design/README.md`, `docs/adr/README.md`; `docs/BATCH.md` (user guide) | Remove the Armada claim; document batch; index ADR-0028 and this design | — | — |
| CLI-1.1 | 1 | `cmd/batch/*`; `cmd/main.go:124` (register) | §8.1 Phase-1 commands | fake dynamic client tests; `naming_test.go` vectors | PLAT-1.2 |
| CLI-1.2 | 1 | `cmd/apps/deploy.go:313-375`, `paved_road_test.go` | `isBatchTemplate`, `type: batch`, `batch-jvm` | paved-road tests | — |
| E2E-1.1 | 1 | `tests/e2e/batch/` | Kind: scaffold, run, kill, restart, stop/resume, invalid params (A1.1–A1.3, A1.7, A1.9) | — | all P1 |
| PLAT-2.1 | 2 | `platform/stack/packages/core/volcano/{generate-manifests.sh, values.yaml, manifests/*, adhar-package.yaml, README.md}`; appsets; env configs; `CONFLICTS.md` scan | §4.1–4.2 | guard tests: pinned tags, narrowed ClusterRole, `enableServiceLinks: false`, no JobFlow CRDs, webhook `namespaceSelector` | — |
| PLAT-2.2 | 2 | `hack/argocd/values*.yaml` → install manifests | Volcano health + exclusions (§6.7) | health tests | PLAT-2.1 |
| PLAT-2.3 | 2 | `compositions/team/local.yaml`; `rbac/local-compose-rbac.yaml` (provider-kubernetes Objects already) | Leaf queues, queue RBAC, `BatchQueuesReady` | render tests incl. `QueueRejected` | PLAT-2.1 |
| PLAT-2.4 | 2 | `core/adhar-batch/manifests/kyverno-queue.yaml` | Queue binding + Volcano pod standards | chainsaw (A2.5) | PLAT-2.3 |
| PLAT-2.5 | 2 | `compositions/batchjob/kubernetes.yaml` | Scheduler branch, Volcano CronJob, Indexed fallback | golden renders for each branch; switch warning | PLAT-2.1 |
| PLAT-2.6 | 2 | `autoscaler/decision.go`, `controller.go` | PodGroup demand (LIST, discovery-guarded) | table tests (A2.6) | PLAT-2.1 |
| PLAT-2.7 | 2 | kube-prometheus values (custom-resource state), composition alerts | Volcano Job/PodGroup phase metrics, P2 alerts | rendered-rule tests | PLAT-2.1 |
| PLAT-2.8 | 2 | CI workflow + `tests/e2e/batch` (`ADHAR_E2E_VOLCANO=1`), measurement script | 1.37 canary (A2.1–A2.10) | — | all P2 |
| CLI-2.1 | 2 | `cmd/batch/{queues.go,…}` | Queues; Volcano-aware runs/logs/stop | tests | PLAT-2.x |
| PLAT-3.1 | 3 | `platform/controllers/batch/*`; `cmd/controller` wiring; manager RBAC | Observer (§12) | envtest: transitions → events; `run.lost`; `dependsOn`; CRD-absent startup | — |
| PLAT-3.2 | 3 | `platform/events/{taxonomy.go,CONTRACT.md}`; `data/kafka-operator/manifests/events-bus.yaml` | Topic + types | `TestTheEventTopicsAreDeclaredInTheStack` | — |
| PLAT-3.3…3.9 | 3 | composition, Project composition, mimir/alloy values, policy packs, scorecards, OpenCost | Calendars; smoke gate; Mimir ruler; Enforce; scorecards; cost | per item | — |
| PLAT-3.10 | 3 | `platform/stack/packages/application/argo-workflows/manifests/steps/adhar-batch-steps.yaml` (coordinate with design 0026's lane) | `adhar-batch-steps/run` (§12.1) | render test; A3.8 e2e | CLI-1.1, 0026's package |
| CLI-3.1 | 3 | `cmd/batch/*` | backfill, abandon, recover, dlq, history | tests (A3.3) | PLAT-3.1 |

### 16.3 adhar-templates (`/Users/tapas-pc/Documents/Work/Adhar/AdharWS/adhar-templates`)

| ID | Phase | Files | Change | Tests | Depends |
|---|---|---|---|---|---|
| TPL-1.1 | 1 | `templates/springboot-batch-job/**`; `catalog-info.yaml` targets | §9.1 | Skeleton ITs green against kit 0.3.0; `BatchJobContractTest` | KIT-1.10, PLAT-1.2 |
| TPL-1.2 | 1 | scratch only | Render every option combination with both renderers; `mvn verify` each | render report in the PR | TPL-1.1 |
| TPL-2.1 | 2 | `templates/springboot-batch-partitioned/**` | §9.2 | `PartitionedJobIT` | KIT-2.3 |
| TPL-3.1 | 3 | both templates' `docs/operations.md` | Backfill, DLQ replay, calendars, dependsOn | render check | P3 |

### 16.4 adhar-console (`/Users/tapas-pc/Documents/Work/Anication/AdharWS/adhar-console`)

| ID | Phase | Files | Change | Tests | Depends |
|---|---|---|---|---|---|
| CON-1.1 | 1 | `apps/console/app/server/{templates.ts,control-plane.ts}` | §7.5 | unit tests | — |
| CON-1.2 | 1 | `modules/develop/src/data/batch-model.ts` (+ `_test.ts`) | GVRs, phases, summary parser, naming v1, form schema | deno tests incl. naming vectors | PLAT-1.2 |
| CON-1.3 | 1 | `modules/develop/src/data/batch.ts` | Queries and watches as the user; writes (§7.4) | hook tests with a fake gateway | CON-1.2 |
| CON-1.4 | 1 | `modules/develop/src/views/batch.tsx`, `components/batch/*` | Jobs, drawer, Runs, Run detail, Run-now dialog, actions behind `<Can>` | component tests | CON-1.3 |
| CON-1.5 | 1 | `packages/shell-ui/src/nav-tree.tsx:274`; `modules/develop/src/{home.tsx,data/nav.ts}` | `develop.batch` | nav-access test | CON-1.4 |
| CON-2.1 | 2 | `modules/platform/src/views/batch-queues.tsx` (rendered under Develop) | Queues; gang status | tests | PLAT-2.3 |
| CON-3.1 | 3 | `apps/console/server.ts` (`/api/batch/history`), `app/server/batch-history.ts`; views | History, Tonight, DLQ, backfill dialog, calendars | tests | PLAT-3.1/3.2 |

**Cross-repository order:**

```
Phase 1:  KIT-0.x → KIT-1.x ┐
          PLAT-1.x          ├→ TPL-1.x → E2E-1.1 + live acceptance
          CLI-1.x, CON-1.x  ┘
Phase 2:  PLAT-2.1 → (PLAT-2.x, KIT-2.x) → TPL-2.1 → PLAT-2.8 canary → production gate
```

---

## 17. Test strategy

| Layer | What | Where |
|---|---|---|
| Kit unit/integration | Resolver, runner, lease, exit codes, restart, partitioning, telemetry; Testcontainers Postgres (PG 18) | `adhar-kit-batch`, kit CI |
| Contract | Naming v1 vectors (Go, TS, Java); template ↔ XR parameters (`BatchJobContractTest`); termination-summary schema v1 | three repositories |
| Composition | Golden `crossplane render` per branch; readiness on second reconcile with an observed copy | `platform/controllers/adharplatform/*_composition_test.go` |
| Policy | Chainsaw/kyverno-test per rule, allow and deny | `core/adhar-batch` |
| Argo CD | Health Lua per kind; generator ↔ manifest parity | `argocd_health_test.go`, existing parity tests |
| Autoscaler | Table tests for drain and scale-up rules | `decision_test.go` |
| CLI / Console | Fake-client command tests; model tests; gated actions | `cmd/batch`, `modules/develop` |
| e2e (Kind) | A1.1–A1.3, A1.7, A1.9 | `tests/e2e/batch` |
| Canary (Kind 1.37 + Volcano) | A2.1–A2.10 | CI |
| Live (DO, production profile) | A1.4–A1.6, A1.8, A1.10; Mimir queries | manual with recorded evidence |
| Chaos (P3) | Kill mid-run in `test`, assert restart completes; feeds the scorecards | resilience suite |

---

## 18. Risks and open questions

| # | Risk | Likelihood / impact | Mitigation | Owner |
|---|---|---|---|---|
| R1 | Volcano not validated on Kubernetes 1.37 [S5][S6] | Medium / Medium | Disabled by default; canary on 1.37; production gate on v1.17 [S7]; native path is the product until then | platform |
| R2 | API server memory (97%) and +CRDs | Medium / High | Measure (A2.8); JobFlow off; native path needs no CRDs beyond the XRD | platform |
| R3 | Autoscaler blind to gangs held at enqueue [S20] | High / High (P2) | PodGroup demand rule (PLAT-2.6) | platform |
| R4 | Two schedulers bind to the same nodes | Medium / Low–Medium | `PodFailed → RestartJob`; Count with backoff; P4 batch node group | platform |
| R5 | RustFS root credential handed to apps | Certain / High for multi-team buckets | PLAT-1.9 scoped users, else a recorded risk acceptance; template documents single-team use | platform (open) |
| R6 | Kyverno `matchConditions` propagation on 1.19 unverified (§10.3) | Medium / Medium | PLAT-1.1 test; fallback: `failurePolicy: Ignore` for the integrity policy with an audit alert | platform |
| R7 | Spring Cloud AWS 4.x against RustFS unverified | Medium / Low | Spike KIT-1.5a; fallback: AWS SDK v2 S3 client wrapped in the kit | kit |
| R8 | Volcano policy precedence (exit code vs `PodFailed`) unverified | Medium / Medium | Canary test; reorder or split policies | platform |
| R9 | JVM-per-run start-up (≈154 s measured on a busy node) | Certain / Low–Medium | Cadence guidance; P3 AOT cache evaluation [S91] | kit |
| R10 | Spring Batch 6.1 changes (JDBC DAOs on `JdbcClient` [S63]) | Low / Low | The kit never instantiates DAOs directly | kit |
| R11 | Single-vendor or stalled subprojects (Volcano Global) [S23] | — | Not adopted | — |

**Open questions** (decide before the phase that needs them):
1. Default `batchOperations` in prod: `team` (as proposed) or `none`? **Decided: `team` (D8, §15.1).**
2. The batch ceiling default: fractions of max allocatable (30% CPU / 20% memory), or a fixed per-profile value? **Decided: the fractions (D8).**
3. Is an organisation-wide calendar authored by the platform team or by each organisation's admins? (P3) **Decided: both. Platform calendars live in `core/adhar-batch`, team calendars in the team's overlays, and the namespace's calendar wins (D5).**
4. Should `adhar.batch` events include identifying parameter *values*, given masking? **Decided: yes, with `sensitive` values masked (D8).**
5. When Spring Batch 6.1 reaches GA with Boot 4.2, move the kit in the next minor release? Still open; not needed by any phase.

---

## 19. Drift (current tree vs this design and ADR-0028)

| Where | Today | Design | Fixed by |
|---|---|---|---|
| `README.md:218` | Lists "Armada – multi-cluster batch job scheduling"; no package exists | Volcano (ADR-0028) | PLAT-1.11 |
| `adhar-kit-batch` `BatchScheduler.java:116-139` | `run.id = currentTimeMillis`; lock released after an async launch | Nominal-time identity; lock held | KIT-0.7 |
| `BatchAutoConfiguration.java:67-72,185-204` | No ordering; `TaskExecutorJobLauncher` `@Primary` | Ordered; `JobOperator` only | KIT-0.2, 0.5 |
| `adhar-kit-batch/pom.xml` | Resourceless repository | JDBC + Flyway schema `batch` | KIT-0.4 |
| `adhar-kit-starter/pom.xml:151-155` | Batch pulled into every service | Optional | KIT-0.3 |
| `AdharJobExecutionListener` | Logs all parameters at INFO | Masked | KIT-0.6 |
| `xrd/apps.xrd.yaml:66` | No `batch` | `batch` | PLAT-1.3 |
| `compositions/project/local.yaml:743-830` | `pods-ready` would fail every batch app | Batch branch | PLAT-1.4 |
| `autoscaler/decision.go:667-685` | Drains nodes running Job pods | Honours safe-to-evict | PLAT-1.8 |
| `kube-prometheus` KSM args | No label allowlist | Allowlist for batch labels | PLAT-1.7 |
| `docs/design/README.md` | Numbers design docs to match ADRs | This file was commissioned as `0025`; it pairs with ADR-0028. The index entry (and possibly a rename to `0028-batch-platform.md`) is left to the maintainer, since this change writes only two files. | PLAT-1.11 |

---

## 20. File inventory

```
adhar-kit
  adhar-kit-batch/pom.xml                                                     (edit)
  adhar-kit-batch/src/main/java/com/adhar/kit/batch/{BatchFacade,AdharBatchApplication}.java
  adhar-kit-batch/src/main/java/com/adhar/kit/batch/{annotation,params,runtime,repository,step,fault,partition,telemetry}/**   (new)
  adhar-kit-batch/src/main/java/com/adhar/kit/batch/{config,operator,scheduler,lock,listener,metrics,event,reader,writer,partitioner,retry}/**   (edit)
  adhar-kit-batch/src/main/resources/com/adhar/kit/batch/db/postgresql/V6_0_*.sql      (new)
  adhar-kit-metrics/**  (OTLP push)   adhar-kit-test-commons/**  (batch test support)   adhar-kit-starter/pom.xml
adhar
  platform/controlplane/configuration/xrd/batchjob.xrd.yaml                    (new)
  platform/controlplane/configuration/compositions/batchjob/{kubernetes.yaml,README.md}  (new)
  platform/controlplane/configuration/{xrd/apps.xrd.yaml, xrd/project.xrd.yaml}          (edit)
  platform/controlplane/configuration/compositions/{apps/argocd-multienv.yaml, project/local.yaml, env/local.yaml, team/local.yaml, bucket/local-rustfs.yaml}  (edit)
  platform/controlplane/configuration/rbac/local-compose-rbac.yaml             (edit)
  platform/stack/packages/core/{adhar-batch,volcano}/**                        (new)
  platform/stack/{adhar-appset-local,adhar-appset-production,adhar-appset-gitops}.yaml, environments/{local,prod}/config.yaml  (edit)
  platform/stack/packages/observability/kube-prometheus/{values.yaml,manifests/install.yaml}   (edit, generated)
  platform/stack/packages/security/adhar-policy-packs/manifests/soc2.yaml      (edit)
  platform/stack/packages/data/kafka-operator/manifests/events-bus.yaml       (edit, P3)
  platform/events/{taxonomy.go,CONTRACT.md}                                    (edit, P3)
  platform/controllers/autoscaler/{decision.go,decision_test.go,controller.go} (edit)
  platform/controllers/batch/**                                                (new, P3)
  platform/controllers/adharplatform/{batchjob_composition_test.go,batch_delivery_test.go,argocd_health_test.go}  (new/edit)
  hack/argocd/{values.yaml,values-ha.yaml} → platform/controllers/adharplatform/resources/argocd/install*.yaml  (edit, generated)
  hack/dashboards/build-app-dashboard.py                                       (edit)
  cmd/batch/**  (new)   cmd/main.go, cmd/apps/deploy.go  (edit)
  tests/e2e/batch/**  (new)   README.md, CLAUDE.md, docs/BATCH.md, docs/{design,adr}/README.md  (edit/new)
adhar-templates
  templates/springboot-batch-job/**  templates/springboot-batch-partitioned/**  (new)   catalog-info.yaml (edit)
adhar-console
  apps/console/app/server/{templates.ts,control-plane.ts}                      (edit)
  modules/develop/src/{data/batch-model.ts,data/batch.ts,views/batch.tsx,components/batch/**}   (new)
  modules/develop/src/{home.tsx,data/nav.ts}, packages/shell-ui/src/nav-tree.tsx (edit)
  modules/platform/src/views/batch-queues.tsx (P2), apps/console/{server.ts,app/server/batch-history.ts} (P3)
```

---

## 21. Sources

### External

Accessed 2026-11-03 unless marked **(11-04)**, which means accessed 2026-11-04. "(excerpt)" marks a claim read from a search-result summary rather than a fetched page; it is lower confidence.

| # | Source | URL |
|---|---|---|
| S1 | Volcano releases | https://github.com/volcano-sh/volcano/releases |
| S2 | Volcano v1.16.0 notes (NamespaceQueue alpha, fairshare, K8s 1.36) | https://github.com/volcano-sh/volcano/releases/tag/v1.16.0 |
| S3 | Volcano v1.15.0 notes (gang-aware preemption/reclaim alpha) | https://github.com/volcano-sh/volcano/releases/tag/v1.15.0 |
| S4 | Volcano v1.13.0 notes (Cron VolcanoJob) | https://github.com/volcano-sh/volcano/releases/tag/v1.13.0 |
| S5 | Volcano Kubernetes compatibility matrix | https://github.com/volcano-sh/volcano/blob/master/README.md#kubernetes-compatibility |
| S6 | Volcano PR #6129 "Upgrade Volcano to Kubernetes v1.37" | https://github.com/volcano-sh/volcano/pull/6129 |
| S7 | Volcano v1.17.0 roadmap, issue #6166 | https://github.com/volcano-sh/volcano/issues/6166 |
| S8 | Volcano Helm values | https://github.com/volcano-sh/volcano/blob/master/installer/helm/chart/volcano/values.yaml |
| S9 | Volcano webhooks template | https://github.com/volcano-sh/volcano/blob/master/installer/helm/chart/volcano/templates/webhooks.yaml |
| S10 | Volcano CRDs (sizes) | https://github.com/volcano-sh/volcano/tree/master/installer/helm/chart/volcano/crd/bases |
| S11 | Volcano development manifest (admission ClusterRole; no requests) | https://github.com/volcano-sh/volcano/blob/master/installer/volcano-development.yaml |
| S12 | Volcano batch API: JobSpec, TaskSpec, LifecyclePolicy, CronJobSpec **(also 11-04)** | https://github.com/volcano-sh/apis/blob/master/pkg/apis/batch/v1alpha1/job.go |
| S13 | Volcano bus actions and events | https://github.com/volcano-sh/apis/blob/master/pkg/apis/bus/v1alpha1/actions.go |
| S14 | Volcano docs: Queue | https://volcano.sh/en/docs/queue/ |
| S15 | Volcano docs: actions and plugins | https://volcano.sh/en/docs/plugins/ |
| S16 | Hierarchical queues on the capacity plugin | https://github.com/volcano-sh/volcano/blob/master/docs/design/hierarchical-queue-on-capacity-plugin.md |
| S17 | Volcano CronJob controller util | https://github.com/volcano-sh/volcano/blob/master/pkg/controllers/cronjob/cronjob_controller_util.go |
| S18 | Volcano feature gates | https://github.com/volcano-sh/volcano/blob/master/pkg/features/volcano_features.go |
| S19 | Volcano PodGroup controller; scheduling annotation keys | https://github.com/volcano-sh/volcano/blob/master/pkg/controllers/podgroup/pg_controller_handler.go ; https://github.com/volcano-sh/apis/blob/master/pkg/apis/scheduling/v1beta1/labels.go |
| S20 | Volcano job controller (pods after PodGroup leaves Pending) | https://github.com/volcano-sh/volcano/blob/master/pkg/controllers/job/job_controller_actions.go |
| S21 | Volcano `svc` and `env` plugins | https://github.com/volcano-sh/volcano/blob/master/pkg/controllers/job/plugins/env/env.go |
| S22 | Volcano scheduler metrics | https://github.com/volcano-sh/volcano/blob/master/docs/design/metrics.md |
| S23 | Volcano Global (alpha; last commit 2026-03-06) | https://github.com/volcano-sh/volcano-global |
| S26 | Kubernetes blog: Headlamp Volcano plugin (2026-06-25) | https://kubernetes.io/blog/2026/06/25/visual-context-volcano-headlamp-plugin/ |
| S28 | Karmada built-in Volcano Job interpreter | https://github.com/karmada-io/karmada/blob/master/pkg/resourceinterpreter/default/thirdparty/resourcecustomizations/batch.volcano.sh/v1alpha1/Job/customizations.yaml |
| S29 | Volcano job admission (queue must be a leaf) | https://github.com/volcano-sh/volcano/blob/master/pkg/webhooks/admission/jobs/validate/admit_job.go |
| S30 | `vcctl job suspend` issues `AbortJobAction` **(11-04)** | https://github.com/volcano-sh/volcano/blob/master/pkg/cli/job/suspend.go |
| S47 | Kubernetes v1.37 workload-aware scheduling (2026-09-08) | https://kubernetes.io/blog/2026/09/08/kubernetes-v1-37-advancing-workload-aware-scheduling/ |
| S48 | Kubernetes PodGroup API (beta 1.37, off by default) | https://kubernetes.io/docs/concepts/workloads/podgroup-api/ |
| S49 | Kubernetes Jobs: pod failure policy (stable 1.31; ordered rules; `restartPolicy: Never`), `podReplacementPolicy` (`Failed` with a failure policy), backoffLimitPerIndex (stable 1.33), managedBy (stable 1.35), suspend (SIGTERM with grace; not counted toward completions; `startTime` reset), success policy (lingering pods terminated) **(also 11-04)** | https://kubernetes.io/docs/concepts/workloads/controllers/job/ |
| S50 | Kubernetes CronJob (timeZone, startingDeadlineSeconds, concurrencyPolicy, "approximately once", 52-character names) | https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/ |
| S51 | KEP-4026, CronJob scheduled-timestamp annotation (v1.32+) (excerpt) | https://www.kubernetes.dev/resources/keps/4026/ |
| S52 | Pod priority and preemption (negative values; `preemptionPolicy: Never`) | https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/ |
| S53 | Termination messages (4096 bytes per container; 12 KiB total) **(11-04)** | https://kubernetes.io/docs/tasks/debug/debug-application/determine-reason-pod-failure/ |
| S54 | kube-state-metrics Job metrics **(11-04)** | https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/workload/job-metrics.md |
| S55 | kube-state-metrics CronJob metrics **(11-04)** | https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/workload/cronjob-metrics.md |
| S56 | kube-state-metrics Pod metrics **(11-04)** | https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/workload/pod-metrics.md |
| S57 | kube-state-metrics `--metric-labels-allowlist` (labels off by default) **(11-04)** | https://github.com/kubernetes/kube-state-metrics/blob/main/docs/developer/cli-arguments.md |
| S58 | kube-state-metrics custom-resource state (feature-frozen) **(11-04)** | https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/extend/customresourcestate-metrics.md |
| S60 | Kueue releases (v0.20.4, 2026-10-29) | https://github.com/kubernetes-sigs/kueue/releases |
| S61 | Spring Batch 6: what's new (`recover`, graceful shutdown, local chunking, JFR) | https://docs.spring.io/spring-batch/reference/whatsnew.html |
| S62 | Spring Batch 6.0 migration guide | https://github.com/spring-projects/spring-batch/wiki/Spring-Batch-6.0-Migration-Guide |
| S63 | Spring Batch 6.1 what's new (RC1) | https://docs.spring.io/spring-batch/reference/6.1/whatsnew.html |
| S64 | Spring blog: Batch 6.1.0-RC1 and 6.0.6 (2026-10-22) | https://spring.io/blog/2026/10/22/spring-batch-6 |
| S65 | Spring Batch scaling (remote chunking middleware; partitioning once-only) | https://docs.spring.io/spring-batch/reference/scalability.html |
| S66 | Spring Batch Integration, externalizing execution (6.0.6) **(also 11-04)** | https://docs.spring.io/spring-batch/reference/spring-batch-integration/externalizing-execution.html |
| S67 | Spring Batch restart (COMPLETED steps skipped) | https://docs.spring.io/spring-batch/reference/step/chunk-oriented-processing/restart.html |
| S68 | Spring Batch testing (JUnit 5) | https://docs.spring.io/spring-batch/reference/testing.html |
| S69 | Spring Boot reference: Spring Batch (stores, `spring.batch.*`) | https://docs.spring.io/spring-boot/reference/io/spring-batch.html |
| S70 | Spring Boot how-to: batch (restart needs the same parameters) | https://docs.spring.io/spring-boot/how-to/batch.html |
| S71 | Spring Boot 4.0 migration guide (in-memory batch by default) | https://github.com/spring-projects/spring-boot/wiki/Spring-Boot-4.0-Migration-Guide |
| S73 | Spring Boot metrics (OTLP export) | https://docs.spring.io/spring-boot/reference/actuator/metrics.html |
| S74 | Spring Boot: developing auto-configuration (conditions and ordering) | https://docs.spring.io/spring-boot/reference/features/developing-auto-configuration.html |
| S75 | Spring Boot graceful shutdown (`spring.lifecycle.timeout-per-shutdown-phase`; SmartLifecycle phases; 4.1.2) **(11-04)** | https://docs.spring.io/spring-boot/reference/web/graceful-shutdown.html |
| S76 | Maven Central `spring-boot-starter-batch-jdbc` | https://repo.maven.apache.org/maven2/org/springframework/boot/spring-boot-starter-batch-jdbc/ |
| S80 | Spring Integration JDBC message store (`JdbcChannelMessageStore`, `PostgresChannelMessageStoreQueryProvider`, `DELETE…RETURNING`; 7.1.1) **(11-04)** | https://docs.spring.io/spring-integration/reference/jdbc/message-store.html |
| S81 | Spring Cloud Data Flow end of open source (2025-04-21) | https://spring.io/blog/2025/04/21/spring-cloud-data-flow-commercial |
| S82 | Maven Central `spring-cloud-deployer-kubernetes` (2.9.5) | https://repo.maven.apache.org/maven2/org/springframework/cloud/spring-cloud-deployer-kubernetes/ |
| S83 | Maven Central `spring-cloud-task-core` | https://repo.maven.apache.org/maven2/org/springframework/cloud/spring-cloud-task-core/ |
| S84 | Spring blog: Spring Batch on Kubernetes (2021-01-27) | https://spring.io/blog/2021/01/27/spring-batch-on-kubernetes-efficient-batch-processing-at-scale |
| S85 | Spring Cloud AWS S3 starter (4.2.1) (compatibility: excerpt) | https://repo.maven.apache.org/maven2/io/awspring/cloud/spring-cloud-aws-starter-s3/ |
| S86 | Argo CD health (app health from immediate resources; not inherited; CronJob Degraded on failed last job) **(11-04)** | https://argo-cd.readthedocs.io/en/stable/operator-manual/health/ |
| S88 | Kargo promotion steps `argocd-update` / `argocd-wait` (excerpt) | https://docs.kargo.io/user-guide/reference-docs/promotion-steps/argocd-update |
| S89 | Kyverno autogen | https://kyverno.io/docs/policy-types/cluster-policy/autogen |
| S90 | Grafana Alloy `mimir.rules.kubernetes` **(11-04)** | https://grafana.com/docs/alloy/latest/reference/components/mimir/mimir.rules.kubernetes/ |
| S91 | OpenJDK 25 (JEP 514/515) | https://openjdk.org/projects/jdk/25/ |
| S94 | OpenCost API (label aggregation) (excerpt) | https://www.opencost.io/docs/integrations/api |
| S95 | BMC Control-M Automation API, what's new (SLA management, calendars, resources) | https://documents.bmc.com/supportu/controlm-saas/en-US/Documentation/API_What_s_New.htm |
| S96 | Broadcom AutoSys courses (box jobs, calendars, file watchers) (excerpt) | https://academy.broadcom.com/learning-paths/getting-started-with-autosys-job-creation |
| S97 | Apache Airflow 3.0 (backfill, DAG versioning) | https://airflow.apache.org/blog/airflow-three-point-oh-is-here/ |
| S98 | Volcano `go.mod` (k8s.io 1.36.1) | https://github.com/volcano-sh/volcano/blob/master/go.mod |
| S101 | Volcano dashboard README (port-forward access; ServiceAccount can patch the scheduler ConfigMap) | https://github.com/volcano-sh/dashboard |

The sources used only for the Volcano-versus-Armada comparison (S24, S25, S27, S31–S46, S99 and S100; the numbering is shared) are listed in [ADR-0028](../adr/0028-batch-scheduler.md#sources).

### Internal evidence

- **Live cluster**: read-only reads on 2026-11-03 of the DigitalOcean cluster at `64.227.182.201`:
  - `kubectl version`, `get nodes`, `top nodes`/`pods`, `describe nodes`;
  - `api-versions`/`api-resources`, `get crd`, `get priorityclass`, `get clusterpolicies`;
  - namespaces with quotas and LimitRanges, NetworkPolicies, CronJob-created Jobs, completed Job pod conditions, Secret existence (not values);
  - `explain job.spec`.

  The server did not answer on 2026-11-04.
- **adhar** at `6035928`: paths cited inline.
- **adhar-kit** at `910ee6a`: `adhar-kit-batch/**`, `adhar-kit-starter/{pom.xml,…/AdharFacade.java}`, `adhar-kit-metrics/pom.xml`, `adhar-kit-tracing/…/OtelEnvironmentPostProcessor.java`, `adhar-kit-test-commons/**`, `adhar-kit-logging/…/BatchJobLogger.java`.
- **Spring Batch 6.0.4 sources** (`~/.m2`): `JobOperator.java`, `SimpleJobOperator.java:189-201,417-457`, `ChunkOrientedStepBuilder.java`, `StepBuilder.java:99-113`, `JobExecutionShutdownHook.java`, `schema-postgresql.sql`, `migration/6.0/migration-postgresql.sql`.
- **adhar-templates** at `9c2c404`: `templates/{springboot-web-service,function-java-service}/**`, `catalog-info.yaml`.
- **adhar-console** at `9d9877c`: paths cited inline.
