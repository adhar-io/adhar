# Low-Level Design — Workflows (Argo Workflows) and Game Days

🔜 **Forward-looking design** (proposed, not yet built). Written 2026-11-04 (SGT).

This is the single, authoritative design for three things the user asked for on 2026-11-03:

1. **Enable Argo Workflows** as an enterprise platform service. Today the `application/argo-workflows` package is `enabled: "false"` in every profile.
2. Build an **enterprise Workflows module under Platform** in the console. It includes a catalogue, a drag-and-drop React Flow designer, live runs, history, schedules, triggers, RBAC, audit and GitOps versioning.
3. Make **Game Days** in Chaos Engineering a real, safe feature built on Argo Workflows: plan, schedule, run, observe, abort/rollback, score, report and recur.

The document covers the goals and invariants, the platform package, the control-plane objects, the console architecture and APIs, page-by-page UX, the CLI, security, failure modes, capacity, and a phased plan with a per-repo work breakdown. Status tracking will live in the [Roadmap](../ROADMAP.md) once the companion ADR is accepted.

**Inputs.** This design builds on three research notes. Each was written for this design on 2026-11-03 and records its own evidence:
- W1: the Argo Workflows survey (package, console, upstream)
- G1: the game-day research
- R1/R3: the batch lane's research on scheduling (Volcano versus Armada) and batch features

The facts they establish are restated here with their evidence, so this document stands alone. External facts carry `[S#]` citations (URL and access date, §19). Repository facts carry `path:line` references that were checked against the tree on 2026-11-04 (§18).

---

## 0. Read this first

### 0.1 The answer in twelve lines

- **One engine.** The platform's Argo Workflows (pinned **v4.1.6**, past two High advisories) is the only workflow controller on a cluster. Litmus injects faults but never brings its own Argo (Kyverno-enforced).
- **Install shape.** The package is hardened before it is switched on:
  - **minimal CRDs** (they protect a control plane already at 96% memory)
  - resources and no service links on every container
  - no preempting PriorityClass
  - SSO with no cluster-admin mapping
  - TLS verified
  - a Postgres **archive** through a `Database` XR
  - Argo CD blind to run objects
- **Tenancy.** Team workflows run in their own project and environment namespaces as the `adhar-workflow` service account. In those namespaces, Workflows may only start saved WorkflowTemplates. Reserved platform service accounts are refused. Test and production accept definitions only from GitOps.
- **Console.** It reaches Kubernetes objects through its existing user-identity gateway. It calls argo-server only through the BFF, and only for what argo-server alone serves: archive, retry, approvals, lint and artifacts. Every such call is authorised as the user and pinned to a namespace.
- **The designer** edits YAML, not a private model. The graph is a projection of a parsed YAML `Document`, so hand-written templates open, and unknown fields and comments survive.
- **Saving** commits to Git as a PR by default, into one of two homes:
  - an app's `deploy/base/workflows/`, where Kargo promotes it dev → test → prod
  - the project repository's `automation/`
- **Game days** are `GameDay` plans (a new XRD) that a trusted composition compiles into a WorkflowTemplate from a vetted, Go-implemented step library: preflight, guard, fault, load, recover, checkpoint, revert, collect. Five independent abort layers protect each run, and a platform **safety lever** stops all of them.
- **The safety boundary** is admission on `ChaosEngine` (catalogue faults only, no overrides, the requester's own non-production environments), not the UI.
- **Batch.** Volcano schedules capacity and Argo orchestrates. A batch job is a *step* that starts a 0025 `BatchJob` run. Workflow control pods never wait in a batch queue.
- **Phasing.** Ten shippable phases, each with acceptance criteria and mutation-tested guards:
  - W0–W5 for the engine and the Workflows module
  - G0–G3 for game days

  **W0 + G0** (enable safely) is the first release.

### 0.2 Numbering, companions and provenance

- **Numbering.** `docs/design/README.md` pairs each design document with the ADR of the same number. The request fixed this file's name, but **ADR-0026 is the concept hierarchy**. The parallel batch design takes ADR-0028.
  - **Recommendation:** accept this design as **ADR-0029** ("Argo Workflows is the platform's orchestration engine; game days are plans compiled to WorkflowTemplates"), then either rename this file to `0029-…` or add an explicit row to the design README.
  - Not done here: this lane edits no file except this one.
- **Batch alignment.** At 06:18 SGT on 2026-11-04, `docs/design/0025-batch-platform.md` and `docs/adr/0028-batch-scheduler.md` **did not exist**. §10 states the contract this design needs from them. It follows the batch lane's research:
  - Volcano recommended, Armada rejected
  - a `BatchJob` XRD
  - runs as `batch/v1` Jobs
  - an `adhar.batch` event topic
  - "do not build a DAG engine; use the opt-in Argo Workflows package"

  Re-check §10 when 0025 lands.
- **Live cluster.**
  - The DigitalOcean cluster read for this design (`https://64.227.182.201:6443`) and its successor (`139.59.45.180`, which the `adhar` context now names) were **unreachable** on 2026-11-04.
  - Every live figure below is from read-only reads on 2026-11-03 (~23:15–23:45 SGT).
  - **Enabling anything here needs a Ready cluster.**

---

## 1. Goals, non-goals and invariants

### 1.1 Goals

| # | Goal |
|---|---|
| G-1 | Argo Workflows runs as a supported production service: patched, sized, observable, backed up, SSO-protected, multi-tenant and safe on memory-constrained control planes. |
| G-2 | Teams author, review, version, run, schedule, trigger and audit workflows **with their own identity**, inside their own namespaces, from the console, the CLI or Git, with the same result from each. |
| G-3 | The console gives a first-class, visually consistent experience: catalogue, visual designer, live run graph, logs, history, schedules, triggers and approvals. A designer never corrupts a template it does not understand. |
| G-4 | Game days follow industry practice (§8.1): charter, steady state, staged escalation, human checkpoints, abort conditions separate from hypotheses, a big red button, observation, a scored and blameless report, tracked action items, and a recurring cadence. |
| G-5 | Game days cannot harm what they were not aimed at. A team breaks only its own non-production environments, and every fault is bounded and reverted. |
| G-6 | One orchestration story with the batch platform: Argo orchestrates, Volcano schedules, and Spring Batch jobs are steps. |
| G-7 | Every change ships in phases. Each phase has acceptance criteria and guards that are mutation-tested: revert the fix and the guard fails. |

### 1.2 Non-goals

- A second workflow engine: no Temporal, no Volcano JobFlow in the designer, no Litmus ChaosCenter execution plane, and no per-team controllers via `instanceID`.
- Embedding the Argo UI in the console. It stays a power-user link for platform staff.
- Free-form inline `Workflow` specs in team namespaces. Teams start saved templates; drafts are templates too.
- S3 artifact repositories for team namespaces before scoped object-store credentials exist (§5.5, W5). Logs after pod clean-up come from Loki.
- Game days against production by default. This is an opt-in, approval-gated feature (D-14).
- CI builds (Tekton), deployment (Argo CD), promotion (Kargo) and data-team analytics DAGs (Dagster) stay where they are (§4.2).

### 1.3 Invariants

| ID | Invariant | Enforced by |
|---|---|---|
| **INV-1** | **One workflow controller per cluster.** Nothing but the `argo-workflows` Application creates a `workflow-controller`, its ConfigMap or the Argo workflow CRDs. | Kyverno `adhar-argo-single-controller` (§5.8); CONFLICTS.md row |
| **INV-2** | **What runs in test and prod is what was merged.** In workload namespaces a `Workflow` must reference a WorkflowTemplate or ClusterWorkflowTemplate. In `tier: test` and `tier: prod` namespaces, definitions are admitted only from Argo CD or Crossplane. | Kyverno `adhar-workflows-reference-templates`, `adhar-workflow-definitions-gitops-only` |
| **INV-3** | **Authors do not choose powerful identities.** Workflow pods run as `adhar-workflow` or a team-owned service account. Reserved platform service accounts are refused. argo-server never acts beyond the caller's RBAC. | workflowDefaults; Kyverno `adhar-workflow-reserved-serviceaccounts`; BFF SSAR plus namespace pinning (§6.4) |
| **INV-4** | **Faults only through vetted plans.** A ChaosEngine from a non-platform identity is admitted only if it uses a catalogue fault, carries no image, secret, sidecar or command-probe override, and targets the requester's own non-production environment. | Kyverno `adhar-chaosengine-guardrails` (Enforce, `failurePolicy: Fail`) |
| **INV-5** | **Every run is bounded.** Every run has a TTL, pod clean-up, a deadline and retention. Parallelism is capped per controller and per namespace. | controller configuration (§5.2) |
| **INV-6** | **The canvas never loses what it does not understand.** YAML is the source of truth; the graph is derived from it. A round trip preserves meaning (JSON-equal), comments and unknown fields. | `model/roundtrip_test.ts` corpus (§6.5) |
| **INV-7** | **The platform's control plane comes first.** Argo run objects are invisible to Argo CD. There is no preempting PriorityClass and every container sets resources. API server memory is measured before and after every phase, against a stop-the-line budget. | Argo CD exclusions; `TestNoStackPriorityClassPreemptsNodePinnedWorkloads`; measurement gate (§13) |
| **INV-8** | **Abort always reverts.** For game days, abort means `shutdown: Stop` (exit handlers run), never Terminate. Every fault is time-bounded by Litmus whether or not Argo is alive. A platform sweeper stops orphaned faults. | step library; console offers Stop only; sweeper (§8.5) |
| **INV-9** | **Gateway first.** Kubernetes objects are read and written through the console's user-identity gateway. argo-server is used only for archive, retry, approvals, lint and artifacts. | console data layer (§6.3) |
| **INV-10** | **Events are advisory and sent on transitions only.** Automation built on events uses at-least-once paths with deterministic event ids, never Kubernetes `Event` objects ("do not use Kubernetes events for automation" [S12]). | console recorder (§7.4) |
| **INV-11** | **Disabling the package never destroys data.** CRDs, the archive database and buckets survive Application deletion. | `Delete=false,Prune=false` sync options (§5.13) |

---

## 2. Where we start (verified 2026-11-03/04)

### 2.1 Inventory

| Piece | State | Evidence |
|---|---|---|
| Package `application/argo-workflows` | Vendored upstream **v4.1.2**, cluster-wide install, rewritten to `adhar-system`. Disabled in both ApplicationSets and every environment configuration. Contract shows `verified / production / 2026-09-23`. | `adhar-package.yaml`; `generate-manifests.sh` (`APP_VERSION="v4.1.2"`); `adhar-appset-production.yaml:498-503`; `adhar-appset-local.yaml:506-511` |
| Overlay | `manifests/dev` is used by **both** profiles. It holds SSO (`insecureSkipVerify: true`), the SSO service accounts, an ExternalSecret and the HTTPRoute. | `manifests/dev/kustomization.yaml`; `patches/cm-argo-workflows.yaml` |
| SSO RBAC | `platform-admin` maps to SA `admin`, which is bound to **cluster-admin** through a long-lived token Secret. `platform-engineer` and every team group match no rule. | `manifests/dev/sa-admin.yaml:13-39` |
| PriorityClass | Upstream `workflow-controller` has value **1,000,000** with `PreemptLowerPriority`. That is above `adhar-node-bound` (900,000) and `adhar-critical-path` (800,000). | `manifests/base/install.yaml` (W1 #6); `data/cnpg/manifests/priorityclass.yaml`; `security/keycloak/manifests/priorityclass.yaml` |
| Resources | None on the controller or argo-server. No `enableServiceLinks: false`, although adhar-system has 290 Services. | `install.yaml`; W1 B.2 |
| Metrics | The PodMonitor scrapes plain HTTP, but the controller's metrics default to TLS (`secure` defaults to true). The two Argo dashboards were removed on 2026-10-23. | `manifests/base/podmonitor.yaml`; [S9]; `dashboards_test.go:223-229` |
| Artifacts and history | The default repository points at Kubeflow's `mlpipeline` bucket and a Secret only Kubeflow creates. `archiveLogs: false`. No archive. | `patches/cm-argo-workflows.yaml:28-48` |
| Keycloak client | `argo-workflows` client has `directAccessGrantsEnabled: true` (the password grant, which nothing needs). | `security/keycloak/manifests/keycloak-config.yaml:158-182` |
| Console | No argo-server client: it was removed 2026-10-03 (`cbc4c8d`). Game days exist as a tab that builds an Argo `Workflow` and shows "Game days need Argo Workflows" while the CRD is absent. The React Flow designer and workbench were deleted on purpose and can be recovered from `git show cbc4c8d^:modules/develop/src/…`. | `modules/platform/src/views/chaos-gameday.tsx:70-74`; `data/chaos-scenarios.ts:317-454`; `data/chaos.ts:387-510` |
| Stale consumers | <ul><li>`adhar pipeline` defaults to namespace `argo`.</li><li>Composition `pipeline-argo-workflows` uses `alpine:latest`.</li><li>The legacy skeleton runs as SA `admin`.</li><li>The console has a dead `?section=workflows` XR page ("Temporal namespaces").</li><li>The console service account has a cluster-wide `create workflows` grant and an unused `ARGO_WORKFLOWS_URL`.</li><li>The builder exports `spec.schedule`, which v4 removed.</li></ul> | `cmd/pipeline/helpers.go:44-50`; `compositions/pipeline/argo-workflows.yaml`; `application/adhar-templates/argo-workflows/skeleton/manifests/deployment.yaml`; `modules/platform/src/home.tsx:313-318`; `core/adhar-console/manifests/install.yaml:10,63-67,863`; `packages/builder/src/lib/argo-export.ts:133` |
| Litmus | <ul><li>ChaosCenter 3.33.0 plus the 3.31.0 execution plane and 29 faults.</li><li>**No chaos infrastructure**, so ChaosCenter cannot run experiments.</li><li>`adhar-chaos-gameday` is bound **cluster-wide** to `k8s-chaos-admin` (secrets, pods/exec, nodes patch).</li></ul> | `application/litmus/manifests/gameday-rbac.yaml`; G1 §2 |
| Scorer | The `chaos_gameday` signal excludes `adhar-chaos-*` engines but **counts the promotion gate's** `adhar-gate-chaos-*` runs as game days. Live, 7 of 8 apps read "latest game day pod-delete: Pass", but no game day has ever run. | `adhar-scorecards/manifests/scorer-cronjob.yaml:530-542`; G1 F1 |
| Control plane (live, 2026-11-03) | Master at **96% memory** (12,022 Mi). `kube-apiserver` uses 8.4–9.0 GiB. 354 CRDs (16.4 MB JSON), 1,554 Events, 1,234 PolicyReports. Autoscaler at `maxWorkers` (10). | W1 B.2; R1 §2 |

### 2.2 Findings that shape the design

Findings F-1 to F-10 are from the research notes. F-11 to F-16 are new in this design.

| # | Finding | Consequence here |
|---|---|---|
| F-1 | v4.1.2 is affected by **GHSA-vm9v-p987-xm32** (artifact path traversal, CVSS 8.8) and **GHSA-q65w-j2vp-47c4** (cross-namespace archive IDOR). Both are fixed in 4.1.4. Latest is **v4.1.6** (2026-10-22) [S1][S4][S5]. | Pin v4.1.6; guard the floor (§15). |
| F-2 | v4 removed CronWorkflow `spec.schedule` (now `schedules[]`), singular `mutex`/`semaphore` and `podPriority`. Outputs read from skipped steps now fail unless a default is declared [S3]. | The designer emits v4 shapes only. The builder's exporter is fixed in W0. |
| F-3 | argo-server has **no per-user impersonation**. `client` mode uses the caller's bearer; `sso` maps users to fixed service accounts [S6][S7]. | The gateway handles Kubernetes objects. argo-server is reached through a BFF that authorises first (§4.4). |
| F-4 | Argo places "no restriction on which service account in a namespace may be used" [S15]. Kubernetes `edit` already allows "running Pods as any ServiceAccount in the namespace" [S35]. | Reserved service accounts are denied by name in workload namespaces (§5.8). |
| F-5 | A ChaosEngine can override images, secrets, sidecars and command-probe sources that then run as `k8s-chaos-admin` (G1 §2.5). | The admission policy is the fault boundary (§8.5). |
| F-6 | Litmus 3.33.0's chaos-infrastructure manifest installs `Deployment/workflow-controller` and `ConfigMap/workflow-controller-configmap` (Argo v3.3.1) plus its own CRDs [S30]. | The single-controller policy (INV-1). |
| F-7 | Full v4 CRDs add **4.84 MiB of compact JSON schema** (8 CRDs). That is about +30% on the 16.4 MB already served, on an API server at 96% node memory. "Existing installations using the minimal CRDs will continue to work", losing only the features that rely on validation information (Validating Admission Policy, `kubectl explain`) [S3]. | **D-3: minimal CRDs.** Validation comes from argo-server lint (§6.7). |
| F-8 | `Workflow` has no status subresource. Large status is gzip-compressed into `status.compressedNodes`. Offloaded status is only visible through argo-server [S25]. | `nodeStatusOffLoad: false`. The console inflates `compressedNodes` in the browser (§6.3). |
| F-9 | Kubernetes Events for workflows (`WorkflowRunning`/`Succeeded`/`Failed`/`TimedOut`) exist, but upstream warns not to use them for automation because they can be lost or rolled up [S12]. | Lifecycle CloudEvents come from a metadata-only watch on Argo's own labels (§7.4). |
| F-10 | Argo stamps `workflows.argoproj.io/phase`, `/completed`, `/workflow-template`, `/cluster-workflow-template`, `/cron-workflow`, `/workflow-event-binding`, `/resubmitted-from-workflow` and `/creator*` labels [S10]. | Cheap filtering and lifecycle detection without full-object watches. |
| **F-11** | The vendored aggregate roles grant workflows `get, list` (view) and `create … update` (edit, admin), but **never `watch`**. | A team user's console live view would fall back to 60 s polling. W1 adds an aggregated `watch` role (§5.6). |
| **F-12** | `platform-developer` is bound to `edit` **cluster-wide** (`security/keycloak/manifests/k8s-rbac.yaml:135-145`). `argo-aggregate-to-edit` includes **create/delete on `clusterworkflowtemplates`**, so platform developers could rewrite the platform's step catalogue. | ClusterWorkflowTemplate writes are admitted only from Argo CD (§5.8). |
| **F-13** | The `Bucket` composition reads `root-creds` from the XR's own namespace and publishes a Secret that *references* it (`compositions/bucket/local-rustfs.yaml:76-128`). A Bucket therefore works only in `adhar-system`, and there is no per-team scoped S3 credential. | Team artifact repositories are deferred (D-12). Game-day reports do not need S3 (§8.7). |
| **F-14** | **Pre-existing escalation (S-1).** Each project namespace holds SA `adhar-resilience-gate`. Its RoleBinding in adhar-system allows `create jobs` with no constraint on the pod template (`adhar-scorecards/manifests/resilience-runner.yaml:845-861`; `compositions/project/local.yaml:452-468`). Team admins can run a pod as that SA in their own namespace, which is the `admin` role [S35]. | Not caused by Argo. Enabling the package as it stands adds a cluster-admin SA in adhar-system one hop away. The reserved-SA policy (§5.8) closes the Argo path and proposes the Job and Pod path (§11.2). |
| **F-15** | ApplicationSet Applications carry `resources-finalizer.argocd.argoproj.io` and `prune: true` (`adhar-appset-production.yaml:772-773,879`). Switching the package off would cascade-delete its CRDs (with every team's templates and runs) and its archive database. | INV-11: `Delete=false,Prune=false` on CRDs and data XRs. |
| **F-16** | The console's own replica of truth for platform events is the API server (`apps/console/app/server/events/platform-events.ts:14-45`). Every resource it watches must be granted by `console-event-watcher`, which a test pins (`console_event_watch_rbac_test.go:147`). The manager must not watch optional CRDs, because a missing CRD fails cache sync and kills it (memory `bootstrap-2min-cache-sync-death`). | Workflow and game-day recorders live in the console BFF. The controller manager only **polls** for the chaos sweeper (§8.5). |

---

## 3. Decisions

| ID | Decision | Choice | Why | Owner |
|---|---|---|---|---|
| D-1 | Workflow engine | Platform Argo Workflows **v4.1.6**, the single controller per cluster; Litmus is the fault engine only | F-1, F-6; one engine, one upgrade path | Design |
| D-2 | Profiles | **Production: on** (W0). **Local: off by default**, `localSafe: true`, opt-in through Marketplace. Flip local on only after the bootstrap-time check in §14 W2 passes. | The local core is CPU-bound during bootstrap (CLAUDE.md). Game days also need Litmus (`localSafe: false`). | **User** may override |
| D-3 | CRDs | **Minimal CRDs** from upstream `manifests/base/crds/minimal` at v4.1.6. Full CRDs are opt-in when API server headroom allows. | F-7; validation comes from argo-server lint and Kyverno, not VAP | Design |
| D-4 | Console to engine path | Gateway first (user identity, live watches). argo-server through the BFF only for archive, retry, memoized resubmit, `set`/`resume`, lint, artifacts and archived logs. | F-3, INV-9; works on clusters whose API server rejects user tokens | Design |
| D-5 | BFF identity toward argo-server | Forward the **user's token** when the gateway's effective mode is `user`. Otherwise use a short-lived **TokenRequest** for SA `adhar-console-argo`, after a SubjectAccessReview as the user. The namespace is always pinned and field selectors are never forwarded. | The GHSA-q65w IDOR class; no long-lived secret | Design |
| D-6 | Tenancy | User workflows run in the **project home namespace** and **environment namespaces**, as `adhar-workflow`. `adhar-system` is for platform staff (platform tasks, platform game days). | Team RBAC is already `admin` there; quotas apply | Design (G1 D4, joint) |
| D-7 | What may run | Templates only in workload namespaces (Kyverno, not controller-wide `templateReferencing: Strict`, which would break Kubeflow and platform ad-hoc runs). Drafts are dev-tier WorkflowTemplates. | INV-2; G1 D14 resolved here | Design |
| D-8 | Authoring | **GitOps-first.** Save means a Gitea PR (or a direct commit where the user may push) into the app repository (`deploy/base/workflows/`, Kargo-promoted) or the project repository (`automation/`, a composed Argo CD app). | Matches `adhar-cli-direction`; review comes from Gitea | Design |
| D-9 | History | Postgres **archive on** (`Database` XR, 60 days). `nodeStatusOffLoad` **off**. Live objects: TTL 1 day on success, 3 days on failure. | Keeps etcd small and history durable | Design |
| D-10 | Argo CD | Exclude `Workflow`, `WorkflowTaskResult`, `WorkflowTaskSet` and `WorkflowArtifactGCTask` from tracking. Consequence: no package may ship a `Workflow` through Git. | Same reasoning as the Tekton exclusion (CLAUDE.md, 2026-10-09) | Design |
| D-11 | Native Argo UI | Platform staff only (`platform-admin`/`platform-engineer`: Argo-scoped operator; `platform-viewer`/`platform-developer`: viewer). Teams use the console. Namespace delegation is a W5 option. | Less surface; no per-namespace token Secrets | Design |
| D-12 | Artifacts | `adhar-system` gets an `artifact-repositories` ConfigMap on bucket `argo-artifacts`, with no controller-wide default. Team namespaces get none until scoped credentials exist (W5). `archiveLogs: false` everywhere; logs come from Loki after pod clean-up. | F-13; Loki already holds every pod's logs | Design |
| D-13 | Game-day plans | `GameDay` XR in the project home namespace. A trusted composition renders a WorkflowTemplate from the vetted step library. Every run carries an immutable plan snapshot as a parameter. | G1 D3; CLI parity; one renderer | Design |
| D-14 | Production game days | Off by default. Opt-in per platform through `adhar-chaos-settings`, requiring attended mode, a platform approver and a checkpoint before every stage. | G1 D11 | **User** |
| D-15 | Paging during game days | Real alerts, announced in advance. A per-plan silence of paging receivers is an option. Detection is measured from `ALERTS` either way. | G1 D12 | **User** |
| D-16 | Action items | Gitea issues by default; Plane work items when the plane package is enabled | G1 D13 | **User** |
| D-17 | ChaosCenter | Keep it as an optional hub UI. Its "connect infrastructure" path is blocked by policy. Review retirement at G3. | G1 D2 | **User** |
| D-18 | Triggers | **Tenant triggers are `WorkflowEventBinding`s** (namespaced, team-owned, no pods). Webhooks hit argo-server with a scoped bearer. Platform CloudEvents are relayed by a platform-owned Argo Events Sensor. **Argo Events Sensors are platform-owned only** (one Kafka EventBus in adhar-system, shared with 0025). | The bus is not tenant-isolated; per-team EventSources and Sensors cost pods | Design (joint with 0025) |
| D-19 | Step image | Game-day and platform steps run `ghcr.io/adhar-io/adhar:<platform version>` (distroless, non-root 65532), rendered into the stack at seed time | Safety logic in tested Go; CLI parity | Design |
| D-20 | YAML library | Add `yaml@2.9.1` (eemeli/yaml, published 2026-09-11 [S42]) to `modules/platform` and the BFF, for parse, edit and print with comments. The hand-written emitter stays for its current users. | Round trip needs a real parser (`manifest-yaml.ts:13-16` refuses to hand-roll one) | Design |
| D-21 | CLI naming | New `adhar workflow` (alias `wf`) and `adhar gameday`. `adhar pipeline template …` is deprecated toward `adhar workflow template …` for one release. | "Pipelines" are Tekton (`platform-tasks.yaml` header) | Design |
| D-22 | MVP game-day steps (G0) | Every console game-day step runs `docker.io/litmuschaos/k8s:3.33.0` as a sized, non-root (65532) container. `litmus-checker` is **not** used. A scenario step is a **kubectl verdict gate**: it refuses an existing engine of the same name, clears a stale `<engine>-<fault>` ChaosResult, creates the engine, polls the ChaosResult, and exits 0 **only on `Pass`**. Its reason goes to the termination log, which Argo makes the step's message. `Stopped` with a `Failed` probe is reported as a failed hypothesis, because a failing `stopOnFailure` steady-state probe stops the fault. The `revert` exit handler tries every engine, bounds each delete (`--timeout=60s`) and has its own `activeDeadlineSeconds`. Engines carry `adhar.io/application` when the target selector names an application. | `litmus-checker` (identical at 3.31.0 and 3.33.0) exits 0 when `engineStatus` reads `completed` or `stopped` and never reads the verdict, so the MVP never stopped at a failed hypothesis. Argo exempts exit handlers from the workflow deadline (v4.1.6 `workflow/controller/workflowpod.go`). Kyverno #8 requires `adhar.io/application`. The G1 Go step library replaces these scripts. | Design (G0 build, 2026-11-05) |
| D-23 | Game-day retention (G0) | `buildGameDay` sets **all three** `ttlStrategy` fields (`secondsAfterCompletion`, `secondsAfterSuccess`, `secondsAfterFailure`) to 604800 (7 days) and keeps `podGC: OnWorkflowSuccess`. | The controller merges `workflowDefaults` into each Workflow field by field (`util.MergeTo`), and its GC reads `secondsAfterFailure`/`secondsAfterSuccess` before `secondsAfterCompletion`. Under §5.2's defaults, a completion-only TTL resolves to 1 day after success and 3 days after failure, never 7. Checked with the v4.1.6 validator and merge code. | Design (G0 build, 2026-11-05) |

---

## 4. Architecture

### 4.1 Component view

```
                      ┌─────────────────────────── adhar-console (BFF + React) ───────────────────────────┐
 People ──► browser ──►  Platform → Workflows  │  Deliver → Chaos → Game days  │  Notification Center      │
                      │      │ (React Flow designer / run graph shared in modules/platform)                │
                      │      ├─ /api/k8s/* gateway (user token | impersonation) ─────────────┐             │
                      │      ├─ /api/workflows/* ─ SSAR as user ─► argo-server (client mode) │             │
                      │      ├─ /api/gamedays/*  ─ reports, notes, checkpoints, safety lever │             │
                      │      └─ recorders: metadata watch (Workflows) → notifications + CloudEvents        │
                      └───────────────┬──────────────────────────────────────┬───────────────┬────────────┘
                                      │                                      │               │ POST /topics/adhar.{workflow,resilience}
 CLI: adhar workflow / gameday ───────┤ kubeconfig (k8s) + bearer (argo-srv) │               ▼
 Git (Gitea): app repo deploy/base/workflows/, project repo automation/ ──► Argo CD ──► WorkflowTemplates, CronWorkflows,
                                                                                         WorkflowEventBindings, GameDay XRs
                                                                                              │
 ┌──────────────────────── adhar-system (platform) ─────────────────────────────────────────┐  │  Crossplane: GameDay ──► WorkflowTemplate gameday-<plan>
 │ workflow-controller (1 replica, leader election)  argo-server (1)  archive DB (CNPG)      │  │                         (+ CronWorkflow if unattended)
 │ ClusterWorkflowTemplates: adhar-steps, adhar-gameday-steps, adhar-batch-steps (0025)     │  │
 │ Litmus chaos-operator + k8s-chaos-admin runner  ◄── ChaosEngines (policy-gated)          │  │
 │ Argo Events: EventBus(Kafka) + adhar-workflow-relay Sensor (W4)   Kafka bridge (bus)     │  │
 │ adhar controller manager: chaos sweeper (poll), safety-lever enforcement                 │  │
 └──────────────────────────────────────────────────────────────────────────────────────────┘  │
 ┌──────── <project> home ns ────────┐   ┌──── <project>-dev / -test / -prod ────┐ ◄──────────┘
 │ adhar-workflow SA (executor)      │   │ adhar-workflow SA, team admin|view    │
 │ adhar-gameday SA (game days)      │   │ targets of faults; k6 via Gateway     │
 │ Workflows: automation, game days  │   │ Workflows: app workflows (promoted)   │
 └───────────────────────────────────┘   └───────────────────────────────────────┘
```

### 4.2 One tool per concern

| Concern | Tool | Not this |
|---|---|---|
| CI builds | Tekton (`app-ci`) | Argo Workflows (no build pipelines here) |
| Deploy and drift | Argo CD | — |
| Promotion between environments | Kargo | — |
| **Orchestrating containers, Kubernetes resources, HTTP calls and approvals; ops automation; batch orchestration; game days** | **Argo Workflows** | — |
| Triggers | WorkflowEventBinding (tenants); Argo Events (platform plumbing) | per-team Sensors on the shared bus |
| In-application durable code workflows | Dapr Workflows (already integrated) | Argo (pod-per-step granularity is too coarse) |
| Data-team analytics DAGs | Dagster (installed; batch R3 G4) | — |
| Batch capacity, gangs, queues, fair share | Volcano (pending ADR-0028) | Argo `parallelism` alone |
| Fault injection | Litmus chaos-operator and faults | ChaosCenter execution plane |
| Load | k6 | — |

### 4.3 Tenancy and placement

| Namespace | Who authors | What runs | Executor SA | Notes |
|---|---|---|---|---|
| `adhar-system` | Platform staff (GitOps: stack packages) | Platform tasks (7 templates, 6 crons today), platform game days, Kubeflow (if enabled) | Named per template (`platform-tasks`, `adhar-chaos-gameday`). No default SA exists, so a template that names none **fails closed**. | Workloads here are not tenant-scoped. Policies exclude the Argo CD and controller identities. |
| `<project>` (home, Kargo project namespace) | The team (`oidc:ws-team-<org>.<team>` = `admin`) | Project automation (`automation/workflows/`), game days (GameDay XRs), drafts in the dev tier, event-triggered runs | `adhar-workflow` (default), `adhar-gameday` (reserved, only from composed game-day templates) | `tier` defaults to `dev` (`compositions/project/local.yaml:166`) |
| `<project>-<env>` | The team: `admin` in dev and test, `view` in prod by default (`compositions/project/local.yaml:116`) | App workflows from `deploy/base/workflows/`, promoted per environment; schedules that need in-namespace access (databases) | `adhar-workflow` | Default-deny ingress except same namespace and adhar-system (`compositions/env/local.yaml:171-232`) |

**Consequences that the UI and docs must state:**
- A run in the home namespace cannot reach an environment's Services directly; it is denied by the environment NetworkPolicy. Use the environment's public route, or run the workflow in that environment.
- Parallelism per namespace is `namespaceParallelism` (5), overridable per namespace with the label `workflows.argoproj.io/parallelism-limit` [S11]. The Environment composition sets it from a parameter (§5.6).

### 4.4 Identity and request paths

| Operation | Console path | Identity at the API server or argo-server | Notes |
|---|---|---|---|
| List, watch, get Workflows, templates, crons, bindings | `/api/k8s/…` gateway | user (token or impersonation) | label-scoped, `limit` 200 |
| Submit from a template | gateway `POST` a `Workflow` with `workflowTemplateRef` | user | Kyverno stamps provenance (§5.8 #6) |
| Stop, terminate, suspend, resume (workflow-level) | gateway `PATCH` `spec.shutdown` / `spec.suspend` | user | Stop is the default; Terminate is gated (§6.9) |
| Approve or reject a suspend node (with values) | `/api/workflows/runs/:ns/:name/approve` | BFF: SSAR (`update workflows` in `:ns`), approver policy, then argo-server `PUT …/set` + `PUT …/resume` with `nodeFieldSelector` | message and approver are server-derived |
| Retry (failed nodes, optional restart-successful) | `/api/workflows/runs/:ns/:name/retry` | BFF: SSAR `update workflows`, then argo-server `PUT …/retry` | — |
| Resubmit | gateway (new Workflow, same template ref and arguments, label `workflows.argoproj.io/resubmitted-from-workflow`); memoized: `/api/workflows/runs/…/resubmit` | user, or BFF plus a provenance patch | — |
| Archive list and detail, archived retry and resubmit | `/api/workflows/archive/:ns…` | BFF: SSAR `list workflows` in `:ns`, then argo-server with `namespace=:ns` pinned | never forward `fieldSelector` (GHSA-q65w) |
| Lint | `/api/workflows/lint` | BFF: SSAR `create workflowtemplates` in `:ns`, then argo-server `…/lint` | — |
| Artifacts | `/api/workflows/runs/:ns/:name/artifacts/:node/:artifact` | BFF: SSAR `get workflows`, then argo-server `/artifact-files/…` (streamed) | adhar-system only until W5 |
| Logs | `/api/workflows/runs/:ns/:name/logs` | live pod: gateway `pods/log` (user). After clean-up: Loki through the console's Loki tool (BFF checks `get pods/log` in `:ns` as the user). | `archiveLogs` is off |
| Publish (save to Git) | `/api/workflows/publish` | Gitea as the user (`Sudo`, `gitea-sudo.ts`); dry-run and lint first | PR by default |

**argo-server identity selection** (`apps/console/app/server/workflows/argo-server.ts`):

```ts
// One function decides; every route calls it. Never cached across users.
async function argoIdentity(req: Request, id: K8sIdentity): Promise<{ bearer: string; mode: 'user' | 'service' }> {
  if (k8sAuthState().effective === 'user') return { bearer: id.token, mode: 'user' }      // argo-server client mode = the user
  const tok = await tokenRequest('adhar-system', 'adhar-console-argo', { expirationSeconds: 600 }) // cached ≤ 8 min
  return { bearer: tok, mode: 'service' }  // only after authorize(req, check) has said yes AS THE USER
}
```

`adhar-console-argo` is a new service account. The console service account gets `create` on `serviceaccounts/token` with `resourceNames: [adhar-console-argo]`. Its ClusterRole `adhar-console-argo`:
- `argoproj.io` `workflows`: get, list, watch, update, patch, create
- `workflowtemplates`, `clusterworkflowtemplates`, `cronworkflows`, `workfloweventbindings`: get, list
- `pods/log`: get

It holds nothing outside Argo.

### 4.5 GitOps model

| Home | Path | Synced by | Reaches | Promotion |
|---|---|---|---|---|
| **App** | `<app repo>/deploy/base/workflows/*.yaml`, listed by `deploy/base/workflows/kustomization.yaml`, which `deploy/base/kustomization.yaml` includes once | The app's per-environment Argo CD Applications (multi-environment composition) | each `<project>-<env>` namespace | Kargo. A merge to `main` triggers `app-ci` and so new image freight, and each environment receives the templates when that freight is promoted (`compositions/apps/argocd-multienv.yaml:750-760`). *Verify in W3 that `app-ci` has no path filter that skips `deploy/**`.* |
| **Project** | `<org>/<project>` repository, `automation/workflows/` and `automation/gamedays/` | New Argo CD Application `<project>-automation` composed by the Project composition (§5.6) | `<project>` home namespace | none: merge is deploy |
| **Platform** | `adhar/packages` → `application/argo-workflows/manifests/platform-tasks/`, ClusterWorkflowTemplates | the `argo-workflows` Application | `adhar-system` and cluster-scoped | platform release (`adhar upgrade`) |
| **Draft** | not in Git; a WorkflowTemplate labelled `adhar.io/workflow-draft: "true"` applied by the console as the user | — | dev-tier namespaces only (Kyverno) | expires after 7 days (janitor, §6.4) |

**Drafts** are the only things that bypass Git, and they live only in dev tiers. A draft template is named `<name>-draft-<user-slug>` so it never collides with the GitOps copy.

### 4.6 Where state lives

| State | Store | Lifetime | Writer → readers |
|---|---|---|---|
| Template definitions | Git, then WorkflowTemplate/CronWorkflow/WorkflowEventBinding objects | until deleted | publish/Argo CD → controller, console, CLI |
| Draft templates | WorkflowTemplate (dev tier) plus console DB `workflows.draft` (unsaved canvas state) | 7 days / until published | designer → designer |
| Live runs | `Workflow` objects (`status.nodes` or `compressedNodes`) | TTL 1 day (success) / 3 days (failure) | controller → console, CLI, Argo UI |
| Run history | Argo archive (Postgres, `argo-workflows-db`) | 60 days (`archiveTTL`) | controller → argo-server → BFF |
| Step logs | the pod while it exists, then Loki | Loki retention (30 days) | Alloy → console BFF |
| Platform artifacts | RustFS `argo-artifacts` | 30 days (Bucket lifecycle) | executor (adhar-system) → argo-server |
| Approvals | suspend node plus intermediate outputs, node `message` | with the run and archive | BFF → console, report |
| Audit | console `workspace.audit` documents (`writeAudit`), API server audit (user identity), Gitea commit and PR history | long | BFF, API server, Gitea |
| Game-day plan | `GameDay` XR (+ Git) | until deleted | team → composition |
| Game-day run evidence | `report` output parameter of the `collect` step → console DB `resilience.gameday-report` (frozen), archive | long | collect/recorder → console |
| Game-day human input | console DB `resilience.gameday-note`, `resilience.gameday-retro`, `resilience.gameday-action` | long | participants → report |
| Scorecard feed | ConfigMap `gameday-results` (label `adhar.io/gameday-results`) in the project namespace | rolling | collect → scorer |
| Safety lever and chaos settings | ConfigMap `adhar-system/adhar-chaos-settings` | until changed | platform staff / on-call → guards, preflight, sweeper, runner, console, Kyverno |
| Chaos lock | Lease `adhar-chaos-lock` in the target environment namespace plus Argo mutex `chaos-<env-ns>` (in adhar-system) | run | preflight/guard → runner, gate, console |

`resilience.*` document kinds are already server-only in the console store (`api-handlers.ts:37`). Game-day evidence uses that prefix so no client can forge it.

### 4.7 Labels and annotations introduced

| Key | Kind | On | Written by | Read by |
|---|---|---|---|---|
| `adhar.io/designer-positions` | ann | WT, CWT, CronWF | designer | designer (positions JSON `{task: [x, y]}`) |
| `adhar.io/designer-version` | ann | same | designer | designer (`"2"`; `"1"` was the deleted designer) |
| `adhar.io/source` | ann | WT, CronWF, WEB | publish route | catalogue ("Managed in Git: repo@path"), history |
| `adhar.io/spec-hash` | ann | WT | publish route | run provenance |
| `adhar.io/template-spec-hash` | label | Workflow | console/CLI submit | run view ("ran template version …") |
| `adhar.io/workflow-draft` | label | WT | test-run route | Kyverno (dev only), janitor |
| `adhar.io/draft-expires` | ann | WT | test-run route | janitor |
| `adhar.io/submitted-by` | ann | Workflow | **Kyverno mutate** from `request.userInfo.username` | approvals (separation of duties), UI |
| `adhar.io/submitter` | label | Workflow | Kyverno mutate (DNS-safe slug) | "My runs" filter |
| `adhar.io/has-approvals` | label | Workflow (`workflowMetadata`) | designer | recorder approvals watch |
| `adhar.io/catalog`, `adhar.io/catalog-tags` | label | CWT, WT | stack, designer | palette, catalogue |
| `adhar.io/node-kind` | ann | CWT template | stack | palette (`approval`, `http-check`, `k6`, `gameday`, `batch-job`) |
| `workflows.argoproj.io/title`, `/description` | ann | WT, CWT, Workflow | designer | catalogue and run header (Argo's own keys) |
| `adhar.io/chaos-gameday` | label | Workflow, ChaosEngine (→ ChaosResult) | composition, fault step | scorer, console, sweeper |
| `adhar.io/gameday-plan`, `adhar.io/gameday-run` | label | WT, Workflow, ChaosEngine, Lease, Job | composition, steps | console, policies, sweeper, collect |
| `adhar.io/abort-reason` | ann | Workflow | guard, console | run room, report |
| `adhar.io/gameday-results` | label | ConfigMap | collect | scorer |
| `workflows.argoproj.io/parallelism-limit` | label | Namespace | Environment/Project composition | controller [S11] |

---

## 5. The platform package: Argo Workflows as an enterprise service (adhar repo)

### 5.1 Package layout and generator

```
platform/stack/packages/application/argo-workflows/
├── adhar-package.yaml            version "v4.1.6"; localSafe: true; deps + cnpg, crossplane, kyverno; keywords + approvals, gameday
├── README.md                     (new) operator notes: knobs, CRD choice, purge runbook
├── generate-manifests.sh         (rewritten, see below)
└── manifests/
    ├── base/                     GENERATED ONLY
    │   ├── install.yaml          upstream cluster install minus CRDs and PriorityClass, namespace → adhar-system
    │   ├── crds-minimal.yaml     upstream manifests/base/crds/minimal/* at the pinned tag
    │   └── kustomization.yaml
    ├── platform/                 the Argo CD element's manifestPath (replaces manifests/dev)
    │   ├── kustomization.yaml    resources: ../base, ../platform-tasks, ../catalog, local files; patches below
    │   ├── workflow-controller-configmap.yaml.tmpl   §5.2 (template only for SSO rootCA)
    │   ├── controller-patch.yaml / server-patch.yaml §5.3
    │   ├── sso.yaml              ExternalSecret argo-workflows-sso + SSO RBAC service accounts (§5.4)
    │   ├── httproute.yaml        + the /login → /oauth2/redirect rule (click-free)
    │   ├── archive-db.yaml       Password generator + ExternalSecret + Database XR (§5.5)
    │   ├── artifacts.yaml        Bucket XR argo-artifacts + adhar-system artifact-repositories ConfigMap
    │   ├── rbac.yaml             adhar-workflow-executor, adhar-workflow-launcher, watch aggregation, catalogue reader, adhar-console-argo
    │   ├── metrics.yaml          PodMonitor (controller, plain HTTP); dashboards ride kube-prometheus
    │   ├── rules.yaml            PrometheusRule (§5.10)
    │   ├── networkpolicy.yaml    CiliumNetworkPolicy for argo-server and controller metrics
    │   └── policies.yaml         Kyverno ClusterPolicies (§5.8)
    ├── platform-tasks/           today's platform-tasks.yaml, moved, image bumped, podGC/securityContext added
    ├── catalog/                  ClusterWorkflowTemplates adhar-steps(.yaml.tmpl) [W2]; adhar-gameday-steps [G1]
    └── triggers/                 EventSource/Sensor relay [W4], its own ApplicationSet element
```

**Why `manifests/platform` and not `manifests/dev`.** Both profiles have always used `dev`, which misleads the next reader. The element is disabled everywhere today, so changing its `manifestPath` costs nothing live.

**`generate-manifests.sh`** keeps the vendoring approach (the overlay patches upstream object names). It changes as follows:

1. Set `APP_VERSION="v4.1.6"`. Fix the header, which wrongly calls the file a namespace install.
2. `curl` `releases/download/${APP_VERSION}/install.yaml`, then:
   - `yq` removes every `CustomResourceDefinition` and `PriorityClass` document;
   - removes `priorityClassName` from `Deployment/workflow-controller`;
   - applies the existing namespace `sed`.
3. Fetch the 8 minimal CRDs from `https://raw.githubusercontent.com/argoproj/argo-workflows/${APP_VERSION}/manifests/base/crds/minimal/` (D-3; the upstream location is cited in [S3]; confirm the file names when you regenerate). Annotate each with `argocd.argoproj.io/sync-wave: "-10"` and `argocd.argoproj.io/sync-options: Delete=false,Prune=false,ServerSideApply=true` (INV-11).
4. Print a checksum line and the upstream tag into each file's header. `TestArgoWorkflowsIsPinnedToAPatchedRelease` reads it (§15).

**Switching to full CRDs later.** Replace step 3 with `manifests/base/crds/full`. Do it only after the §13 measurement shows the API server can afford it.

### 5.2 Controller configuration

The full content of `workflow-controller-configmap`. Every key is verified against v4.1.6 `config/config.go` [S9] or the docs cited.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: workflow-controller-configmap
  namespace: adhar-system
  annotations: {argocd.argoproj.io/sync-wave: "0"}
data:
  # ── Admission before pods exist: what Argo queues, the API server never sees ──
  parallelism: "25"                    # cluster-wide running workflows (10-worker DO profile)
  namespaceParallelism: "5"            # per namespace; override with the namespace label
                                       # workflows.argoproj.io/parallelism-limit [S11]
  resourceRateLimit: |                 # pod creations per second
    limit: 5
    burst: 10
  # ── Events: node events off (Events are the #1 object kind in this API server) ──
  nodeEvents: |
    enabled: false
  workflowEvents: |
    enabled: true                      # transition-only, cheap; NOT used for automation [S12]
  # ── Executor (init/wait) sizing: the team LimitRange would otherwise default each to 100m/128Mi
  #    requests and 500m/512Mi limits, against the team's limits quota on every step ──
  executor: |
    resources:
      requests: {cpu: 10m, memory: 64Mi}
      limits:   {cpu: 250m, memory: 256Mi}
    securityContext:
      runAsNonRoot: true
      allowPrivilegeEscalation: false
      capabilities: {drop: [ALL]}
      seccompProfile: {type: RuntimeDefault}
  # ── Defaults merged into every Workflow (a value the author sets explicitly wins) ──
  workflowDefaults: |
    spec:
      serviceAccountName: adhar-workflow      # exists in tenant namespaces only (§5.6);
                                              # adhar-system fails closed
      activeDeadlineSeconds: 21600            # 6 h; Kyverno caps an explicit value at 24 h in workload namespaces
      parallelism: 10                         # concurrent pods per workflow
      ttlStrategy:
        secondsAfterSuccess: 86400            # history lives in the archive (D-9)
        secondsAfterFailure: 259200
      podGC:
        strategy: OnPodSuccess                # failed pods stay for inspection until the TTL
        deleteDelayDuration: 60s
      podSpecPatch: '{"enableServiceLinks": false}'   # 290 Services in adhar-system
      securityContext:
        seccompProfile: {type: RuntimeDefault}
  retentionPolicy: |                   # backstop on top of TTL
    completed: 300
    failed: 100
    errored: 100
  failedPodRestart: |                  # restart pods evicted before they started (memory-pressured nodes)
    enabled: true
    maxRestarts: 3
  # ── History: Postgres archive; node status stays on the object (D-9) ──
  persistence: |
    archive: true
    archiveTTL: 60d
    nodeStatusOffLoad: false
    connectionPool: {maxIdleConns: 2, maxOpenConns: 8, connMaxLifetime: 300s}
    postgresql:
      host: argo-workflows-db-rw.adhar-system.svc.cluster.local
      port: 5432
      database: argo
      tableName: argo_workflows
      userNameSecret: {name: argo-workflows-db-credentials, key: username}
      passwordSecret: {name: argo-workflows-db-credentials, key: password}
      ssl: true
      sslMode: require                 # honoured since 4.1.5 [S2]; CNPG serves TLS with its own CA
  # ── Metrics: plain HTTP, reachable only from Prometheus (networkpolicy.yaml) ──
  metricsConfig: |
    enabled: true
    path: /metrics
    port: 9090
    secure: false
  # ── UI deep links (host rewritten at seed time) ──
  links: |
    - name: Open in Adhar console
      scope: workflow
      url: https://console.adhar.localtest.me:8443/platform?section=workflow-runs&run=${metadata.namespace}/${metadata.name}
  # ── SSO for the native UI (platform staff only, D-11) ──
  sso: |
    issuer: https://keycloak.adhar.localtest.me:8443/realms/adhar
    clientId:     {name: argo-workflows-sso, key: client-id}
    clientSecret: {name: argo-workflows-sso, key: client-secret}
    redirectUrl: https://argo-workflows.adhar.localtest.me:8443/oauth2/callback
    scopes: [openid, profile, email, groups]
    sessionExpiry: 8h
    filterGroupsRegex: ["^platform-.*$"]     # only mapped groups ride in the cookie (4 KB limit) [S7]
    rbac:
      enabled: true
    # rootCA: rendered ONLY when the platform serves its own self-signed edge certificate.
    # A pinned CA replaces the trust set (memory oidc-trust-roots-and-one-shot-hooks), so a
    # platform on a public ACME certificate must not carry one. insecureSkipVerify is never set.
```

There is deliberately **no `artifactRepository`** at controller level (D-12), so a team namespace never inherits a repository whose Secret does not exist there.

### 5.3 Controller and argo-server deployments

| | workflow-controller | argo-server |
|---|---|---|
| Replicas | 1, with leader election (upstream default). `TestNoPackageWorkloadShipsMoreThanOneReplica` holds. | 1 |
| Requests / limits | `100m / 256Mi`, limit `1Gi` (≥256 Mi floor: `TestInformerHoldingContainersHaveMemoryHeadroom`). Calibrate from `container_memory_working_set_bytes` after a week. | `50m / 128Mi`, limit `512Mi` |
| `enableServiceLinks` | `false` | `false` |
| Flags | Append `--workflow-workers=8 --workflow-ttl-workers=2 --pod-cleanup-workers=2 --cron-workflow-workers=2 --qps=15 --burst=25`. Upstream warns that raising qps/burst "can increase the load on the Kubernetes API server" [S9]. | `server --auth-mode=client --auth-mode=sso --secure=false --api-rate-limit=200`. TLS terminates at the Gateway. |
| Env | `OTEL_EXPORTER_OTLP_ENDPOINT=http://alloy.adhar-system.svc.cluster.local:4317`, `OTEL_SERVICE_NAME=argo-workflow-controller` | `ARGO_ARTIFACT_SERVER=true` (adhar-system artifacts only) |
| Priority | none (default 0); upstream class removed (§5.1) | none |
| Sync wave | 10 (after the archive Database is Healthy, wave −5) | 20 |

**Upgrades.** A v4.1.2 → v4.1.x controller upgrade changes the SSO cookie format: every user signs in once more [S3]. Record this in the release notes.

### 5.4 SSO and the native UI

The native UI is a platform-staff power tool (D-11). The SSO service accounts replace `manifests/dev/sa-admin.yaml`. Nothing maps to cluster-admin.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: argo-sso-platform
  namespace: adhar-system
  annotations:
    workflows.argoproj.io/rbac-rule: "'platform-admin' in groups || 'platform-engineer' in groups"
    workflows.argoproj.io/rbac-rule-precedence: "10"
---
apiVersion: v1
kind: Secret                       # argo-server SSO RBAC needs a token Secret since k8s 1.24 [S7];
metadata:                          # scope is Argo-only, so the blast radius is Argo-only
  name: argo-sso-platform.service-account-token
  namespace: adhar-system
  annotations: {kubernetes.io/service-account.name: argo-sso-platform}
type: kubernetes.io/service-account-token
---
# ClusterRole adhar-argo-platform-operator (bound to argo-sso-platform):
#   argoproj.io: workflows, workflowtemplates, cronworkflows, workfloweventbindings,
#                workflowtaskresults, clusterworkflowtemplates (get list watch create update patch delete)
#   "": pods, pods/log, events (get list watch)
# ServiceAccount argo-sso-viewer: rule "'platform-viewer' in groups || 'platform-developer' in groups",
#   precedence 5, ClusterRole adhar-argo-viewer (get list watch on the argoproj.io kinds above, pods/log get)
# No rule for ws-team-* or org-*: "if no rule matches, we deny the user access" [S7]; teams use the console.
```

**Other changes:**
- **Keycloak client `argo-workflows`:** `directAccessGrantsEnabled: false` (`keycloak-config.yaml:165`). Rename the ExternalSecret target to `argo-workflows-sso`; the generic `keycloak-oidc` name is one collision away in a shared namespace (ADR-0011).
- **Click-free sign-in.** On a 401 the Argo UI sets `document.location.href = /login?redirect=<path>` (`ui/src/shared/services/requests.ts:11-18`), and `/oauth2/redirect` reads `redirect` (`server/auth/sso/sso.go:242-246`) (W1 [S31]). Add an HTTPRoute rule `GET /login → RequestRedirect path /oauth2/redirect` that keeps the query and **states the port**: a redirect without a port drops to 443 (CLAUDE.md, Gitea note). Update the `argo-server` entry in `sso_click_free_test.go:31` from "(disabled in every profile)" to the click-free description.
- **Delete** `admin.service-account-token` and `developer.service-account-token`, and the `argo-admin` ClusterRoleBinding to cluster-admin.

### 5.5 Archive and artifacts

**Archive database.** This uses the `Database` XR pattern of `data/mlflow/manifests/platform-postgres.yaml`:

```yaml
# Password generator argo-workflows-db + ExternalSecret argo-workflows-db-credentials (basic-auth, user "argo",
# rewrite transform PASSWORD — memory generator-key-case), waves -6.
apiVersion: platform.adhar.io/v1alpha1
kind: Database
metadata:
  name: argo-workflows-db
  namespace: adhar-system
  labels: {adhar.io/component: argo-workflows}
  annotations:
    argocd.argoproj.io/sync-wave: "-5"
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true,Delete=false,Prune=false   # INV-11
spec:
  parameters:
    databaseName: argo
    credentials: {masterUsername: argo}
    credentialsSecret: argo-workflows-db-credentials
    storageSize: 10Gi
    component: argo-workflows
    backupSchedule: 0 40 3 * * *
    resources: {cpu: 50m, memory: 192Mi, memoryLimit: 512Mi}
```

- **Table growth.** The archive stores the full Workflow JSON per run, typically 10–100 KiB. At 60 days and ~500 runs/day this is ≤ 3 GiB. An alert fires at 70% of the volume (CNPG metrics).
- **Controller start dependency.** "If migration fails, the controller does not start" [S13], so the controller sits in a later wave than the Database (Argo CD health for `Database` already reads composite readiness, `hack/argocd/values.yaml:352`).

**Artifacts.** Platform namespace only (D-12):

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: Bucket
metadata:
  name: argo-artifacts
  namespace: adhar-system
  annotations:
    argocd.argoproj.io/sync-wave: "-5"
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true,Delete=false,Prune=false
spec:
  parameters: {quotaGB: 50, expireAfterDays: 30, versioning: false, team: platform}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: artifact-repositories
  namespace: adhar-system
  annotations: {workflows.argoproj.io/default-artifact-repository: argo-artifacts}
data:
  argo-artifacts: |
    archiveLogs: false
    s3:
      endpoint: rustfs.adhar-system.svc.cluster.local:9000
      bucket: argo-artifacts
      insecure: true
      addressingStyle: path            # safer than auto-detect on a non-AWS endpoint [S14]
      accessKeySecret: {name: root-creds, key: rootUser}
      secretKeySecret: {name: root-creds, key: rootPassword}
      keyFormat: "{{workflow.namespace}}/{{workflow.name}}/{{pod.name}}"
```

**Argo CD health.** This package declares the stack's **first `Bucket` XR**. Add `resource.customizations.health.platform.adhar.io_Bucket` (copy of the `Database` check) to both `hack/argocd/values*.yaml` files and both generated install manifests. Otherwise Argo CD calls it Healthy at creation (`hack/argocd/values.yaml:355-358`).

Kubeflow keeps `mlpipeline` through its own `artifactRepositoryRef` when it is enabled. Its section of today's ConfigMap moves into the kubeflow package.

### 5.6 Execution identity in tenant namespaces (control plane)

These are delivered by **controller releases**, because the XRDs and compositions are embedded (CLAUDE.md).

| Composition | Adds (only when `ExtraResources` shows the `workflows.argoproj.io` CRD) | Phase |
|---|---|---|
| `compositions/env/local.yaml` | <ul><li>SA `adhar-workflow`</li><li>RoleBinding to ClusterRole `adhar-workflow-executor`</li><li>RoleBinding to `view` (own namespace; no Secrets)</li><li>RoleBinding to `adhar-workflow-launcher`</li><li>Namespace label `workflows.argoproj.io/parallelism-limit` from the new parameter `workflowParallelism` (default: dev 3, test 3, prod 5)</li></ul> | W1 |
| `compositions/project/local.yaml` (home namespace) | <ul><li>The same four objects for `<project>`</li><li>**G1:** SA `adhar-gameday` and RoleBindings:<ul><li>`<project>`: `adhar-workflow-executor` and patch on its own Workflows</li><li>each non-prod environment namespace: `view`, Leases, Jobs for in-cluster load</li><li>`adhar-system`: Role `adhar-gameday-engines` (provider-kubernetes `Object`, like `adhar-resilience-gate-<project>`)</li></ul></li><li>**W3:** Argo CD Application `<project>-automation`</li></ul> | W1 / G1 / W3 |
| `rbac/local-compose-rbac.yaml` | Crossplane may manage `argoproj.io` `workflowtemplates`, `cronworkflows`, `workfloweventbindings` (the GameDay composition emits them directly in the XR's namespace) | G1 |

**ClusterRoles shipped by the package (`platform/rbac.yaml`):**

```yaml
# adhar-workflow-executor — the documented minimum [S16] + the HTTP/plugin agent [S20]
- apiGroups: [argoproj.io]
  resources: [workflowtaskresults]
  verbs: [create, patch]
- apiGroups: [argoproj.io]
  resources: [workflowtasksets]
  verbs: [list, watch]
- apiGroups: [argoproj.io]
  resources: [workflowtasksets/status]
  verbs: [patch]
---
# adhar-workflow-launcher — steps that start other things in their OWN namespace
- apiGroups: [argoproj.io]
  resources: [workflows]
  verbs: [create, get, watch]                        # run-gameday step (Kyverno decides which templates)
- apiGroups: [batch]
  resources: [jobs]
  verbs: [create, get, list, watch, delete]          # batch-job step (0025)
- apiGroups: [platform.adhar.io]
  resources: [gamedays, batchjobs]
  verbs: [get]
---
# adhar-argo-aggregate-watch — F-11: the upstream aggregates have no `watch`
metadata:
  labels:
    rbac.authorization.k8s.io/aggregate-to-view: "true"
    rbac.authorization.k8s.io/aggregate-to-edit: "true"
    rbac.authorization.k8s.io/aggregate-to-admin: "true"
rules:
- apiGroups: [argoproj.io]
  resources: [workflows, workflowtemplates, cronworkflows, workfloweventbindings]
  verbs: [watch]
---
# adhar-workflow-catalog-reader — ClusterWorkflowTemplate RBAC is checked cluster-wide since v4.0-rc2 [S2],
# and a RoleBinding cannot grant a cluster-scoped read. Bound by ClusterRoleBinding to system:authenticated:
# the catalogue holds no secrets.
- apiGroups: [argoproj.io]
  resources: [clusterworkflowtemplates]
  verbs: [get, list, watch]
```

### 5.7 The platform step catalogue (ClusterWorkflowTemplates)

These are shipped in `manifests/catalog/` and labelled `adhar.io/catalog: "true"`. Each template carries `adhar.io/node-kind` and Argo's `workflows.argoproj.io/title` and `/description` annotations. The designer's palette builds its "Platform steps" section from them, so a new step needs no console release. Images are pinned by tag in `.yaml.tmpl`. `{{ .PlatformImage }}` is a new `stackTemplateData` method that returns the image the controller manager itself runs (`platform/controllers/adharplatform/controller.go:1290`), so the step code and the control plane are always the same release (D-19).

| CWT / template | Kind | What it does | Phase |
|---|---|---|---|
| `adhar-steps/approval` | suspend + intermediate parameters | Inputs: `question`, `approvers` (comma-separated Keycloak groups or users), `timeout` (`suspend.duration`), `forbidSelfApproval`. Output parameters `decision` (enum `approve,reject`, **default `reject`**), `comment` and `approver`, all `valueFrom.supplied` [S18]. **Verify in W2** that an expired duration resumes with the defaults (that is, rejects). | W2 |
| `adhar-steps/http-check` | http | Wait until a URL answers `successCondition`, with a retry strategy. Runs in the per-workflow agent pod and must be idempotent [S20]. | W2 |
| `adhar-steps/wait-healthy` | container (`{{ .PlatformImage }}`) | `adhar workflow wait --kind Deployment\|Rollout\|StatefulSet\|Job --name … --for Healthy --timeout …` in its own namespace (uses the `view` binding) | W2 |
| `adhar-steps/k6-load` | container `grafana/k6` (pinned) | The runner's generic load (`handleSummary` prints `ADHAR_K6_SUMMARY {json}`; exit 99 = thresholds failed, a result and not an error) | W2 |
| `adhar-steps/run-gameday` | container (`{{ .PlatformImage }}`) | `adhar gameday run --plan <ns>/<plan> --wait --parent {{workflow.name}}`: starts the plan's composed template as a child Workflow and returns its verdict (§8) | G2 |
| `adhar-gameday-steps/*` | container / suspend | preflight, guard, fault, load, recover, checkpoint, revert, collect (§8.4) | G1 |
| `adhar-batch-steps/run` | owned by 0025 | Starts a `BatchJob` run and waits for its exit status (§10) | after 0025 |

**`platform-tasks/`.** The existing 7 templates and 6 crons are kept, with these changes:
- The image moves off `docker.io/alpine/k8s:1.31.0`, which is outside the kubectl version-skew window against v1.37. Use the platform image's `adhar` subcommands where one exists, otherwise a pinned `alpine/k8s` that matches the server minor.
- Add `podGC` and `securityContext`.
- Cron objects keep `schedules:` (already v4-shaped).

### 5.8 Kyverno policies

All of these use `background: false`. Admission-only policies add no PolicyReports, of which the API server already stores 1,234. Each is proven with `kyverno test` fixtures under `policy-tests/` (new `make policy-test` target, §15) and mutation-tested.

| # | Policy | Mode | Rule (summary) | Phase | Ships in |
|---|---|---|---|---|---|
| 1 | `adhar-argo-single-controller` | Enforce, `failurePolicy: Fail` | <ul><li>Deny pods and controllers whose image matches `*litmuschaos/workflow-controller*`, `*litmuschaos/argoexec*`, `*litmusportal-subscriber*` or `*litmusportal-event-tracker*`.</li><li>Deny `Deployment/workflow-controller` and `ConfigMap/workflow-controller-configmap` writes not from `system:serviceaccount:adhar-system:argocd-application-controller`.</li><li>Deny writes to the 8 workflow CRDs not from Argo CD.</li></ul> | W0 | argo-workflows |
| 2 | `adhar-cwt-platform-only` | Enforce | ClusterWorkflowTemplate create, update or delete only from Argo CD (F-12) | W0 | argo-workflows |
| 3 | `adhar-workflow-reserved-serviceaccounts` | Enforce, Fail | In `adhar.io/plane: workload` namespaces, deny `adhar-resilience-gate`, `adhar-release` and `adhar-gameday` in `spec.serviceAccountName`, `spec.templates[].serviceAccountName`, `spec.executor.serviceAccountName` and `spec.workflowSpec.*` (Workflow, WorkflowTemplate, CronWorkflow), **except** WorkflowTemplates written by `system:serviceaccount:adhar-system:crossplane` | W1 | argo-workflows |
| 4 | `adhar-workflow-provenance` | mutate | On Workflow CREATE, stamp annotation `adhar.io/submitted-by` = `request.userInfo.username` and label `adhar.io/submitter` = a DNS-safe slug. Client values are overwritten, except from `adhar-console-argo` (service mode, §4.4), whose value the BFF set after authenticating the user. | W1 | argo-workflows |
| 5 | `adhar-workflows-reference-templates` | Audit (W1) → Enforce (W3) | In workload namespaces, Workflow CREATE must set `spec.workflowTemplateRef`, and top-level `spec` keys must be within `{workflowTemplateRef, arguments, entrypoint, podMetadata, workflowMetadata, priority, shutdown, suspend}`. CronWorkflow `spec.workflowSpec` must hold `workflowTemplateRef`. `activeDeadlineSeconds` ≤ 86400 where set. | W1/W3 | argo-workflows |
| 6 | `adhar-workflow-definitions-gitops-only` | Enforce | In namespaces labelled `adhar.io/tier` ∈ {test, prod}, WorkflowTemplate, CronWorkflow and WorkflowEventBinding writes only from Argo CD or Crossplane | W3 | argo-workflows |
| 7 | `adhar-workflow-drafts-dev-only` | Enforce | `adhar.io/workflow-draft: "true"` templates only in `adhar.io/tier: dev` namespaces, carrying `adhar.io/draft-expires` ≤ 7 days | W2 | argo-workflows |
| 8 | `adhar-chaosengine-guardrails` | Enforce, Fail | §8.5 table | G0 (catalogue/overrides) / G1 (tenancy) | litmus |
| 9 | `adhar-gameday-templates-crossplane-only` | Enforce | WorkflowTemplates labelled `adhar.io/gameday-plan` are written only by Crossplane | G1 | litmus |

**Exceptions to CLAUDE.md's "validation policies stay Audit in adhar-system".** Policies 1, 2 and 8 deliberately Enforce in `adhar-system`. They match only kinds and identities no platform package uses legitimately, which is the same argument G1 made for #8. `TestKyvernoAdmitsThePlatformNamespace` stays green.

Sketch of #3, the shape the others follow:

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: adhar-workflow-reserved-serviceaccounts
  annotations: {policies.kyverno.io/title: "Workflows never run as reserved platform identities (INV-3)"}
spec:
  background: false
  failurePolicy: Fail
  rules:
    - name: reserved-serviceaccounts
      match:
        any:
          - resources:
              kinds: [argoproj.io/v1alpha1/Workflow, argoproj.io/v1alpha1/WorkflowTemplate, argoproj.io/v1alpha1/CronWorkflow]
              operations: [CREATE, UPDATE]
              namespaceSelector: {matchLabels: {adhar.io/plane: workload}}
      exclude:
        any:
          - subjects: [{kind: ServiceAccount, name: crossplane, namespace: adhar-system}]
      validate:
        failureAction: Enforce
        message: >-
          Workflows run as adhar-workflow or a ServiceAccount your team owns; adhar-resilience-gate,
          adhar-release and adhar-gameday are reserved for the platform (docs/WORKFLOWS.md#identity).
        deny:
          conditions:
            any:
              - key: "{{ [request.object.spec.serviceAccountName, request.object.spec.executor.serviceAccountName, request.object.spec.workflowSpec.serviceAccountName][] }}"
                operator: AnyIn
                value: [adhar-resilience-gate, adhar-release, adhar-gameday]
              - key: "{{ request.object.spec.templates[].serviceAccountName || `[]` }}"
                operator: AnyIn
                value: [adhar-resilience-gate, adhar-release, adhar-gameday]
```

### 5.9 Argo CD integration

Edit **both** `hack/argocd/values.yaml` and `values-ha.yaml`, **and** both generated manifests (`platform/controllers/adharplatform/resources/argocd/install.yaml` and `install-ha.yaml`). `TestArgoCDReconciliationMatchesItsGeneratorValues` and the regenerate-reverts-hand-edits lesson both require this.

```yaml
resource.exclusions: |
  # … existing entries (tekton.dev PipelineRun/TaskRun/CustomRun etc.) …
  ### Argo Workflows run objects are runtime state, not desired state (D-10). Without this Argo CD
  ### caches every Workflow (status rewritten on each node transition) and every per-pod
  ### WorkflowTaskResult — cluster cache memory and API server watch load for nothing.
  - apiGroups: [argoproj.io]
    kinds: [Workflow, WorkflowTaskResult, WorkflowTaskSet, WorkflowArtifactGCTask]
resource.customizations.health.argoproj.io_WorkflowTemplate: |
  hs = {status = "Healthy", message = "definition"}; return hs
resource.customizations.health.argoproj.io_ClusterWorkflowTemplate: |   # same
resource.customizations.health.argoproj.io_WorkflowEventBinding: |      # same
resource.customizations.health.platform.adhar.io_Bucket: |              # copy of _Database
resource.customizations.health.platform.adhar.io_GameDay: |             # copy of _Database (G1)
```

- **Guard.** `TestArgoCDDoesNotTrackArgoWorkflowRuns` (sibling of `TestArgoCDDoesNotTrackTektonRuns`, `argocd_health_test.go:187`). Extend the kind list in `TestArgoCDCanAssessEveryCustomKindTheStackSyncs` (`argocd_health_test.go:33`) with the definitions above.
- **CronWorkflow.** It keeps Argo CD's built-in health check, which goes Degraded on `SpecError`/`SubmissionError` [S29]: a broken platform cron marks the `argo-workflows` Application Degraded, which is the signal we want.

### 5.10 Observability

| Signal | How |
|---|---|
| Controller metrics | PodMonitor on port `metrics` (9090), **scheme http** with `metricsConfig.secure: false`. CiliumNetworkPolicy admits only Prometheus. Fixes the scheme mismatch. Non-leader replicas serve empty metrics [S22]. |
| argo-server metrics | **Not scraped in W0.** `/metrics` is served on the same port as the public UI route, and `ARGO_SERVER_METRICS_AUTH=false` would publish it through the Gateway. W5 adds an authenticated scrape. Health comes from readiness and a blackbox `GET /api/v1/version`. |
| Dashboards | <ul><li>`dash_delivery.py`: collapsible row **Argo Workflows** in `delivery-ci`:<ul><li>leader and up</li><li>workflows by phase (`argo_workflows_gauge`)</li><li>throughput (`argo_workflows_total_count`)</li><li>queue depth and latency (`argo_workflows_queue_*`)</li><li>workers busy</li><li>pods pending (`argo_workflows_pod_pending_count`)</li><li>cron triggers (`argo_workflows_cronworkflows_triggered_total`)</li><li>k8s request rate and latency</li><li>`argo_workflows_error_count`</li><li>controller memory</li></ul></li><li>`dash_resilience.py`: row **Game days** from the per-workflow custom metrics the composed template declares (§8.4).</li><li>Register `argo-workflows` in `dashboardCoverage["delivery-ci"]` (`dashboards_test.go:229`). Folder caps (`TestEachFolderHoldsAFewDashboards`) are untouched because these are rows, not dashboards.</li></ul> |
| Alerts (`rules.yaml`) | <ul><li>`ArgoWorkflowsControllerDown` (no leader for 5 min, warning)</li><li>`ArgoWorkflowsQueueBacklog` (workflow queue > 50 for 15 min)</li><li>`ArgoWorkflowsErrors` (`increase(argo_workflows_error_count[15m]) > 0`)</li><li>`ArgoWorkflowsPodsPendingLong` (> 20 for 15 min; the first question is capacity, memory `capacity-symptoms-that-arent-capacity`)</li><li>`ArgoWorkflowsArchiveDatabaseDown` (CNPG)</li><li>`ArgoWorkflowsArchiveVolumeFilling` (> 70%)</li></ul>Metric names are from [S22]; verify against `/metrics` in W0. |
| Tracing | OTLP to Alloy → Tempo. v4.1.6 injects `TRACEPARENT` into child containers [S2]. |
| Logs | Loki (Alloy collects every pod). The console's run view queries Loki after pod clean-up (§6.4). |

### 5.11 Coexistence with Litmus

- **CONFLICTS.md.** Add a row: `Deployment/workflow-controller`, `ConfigMap/workflow-controller-configmap`, `Service/workflow-controller-metrics`, Argo workflow CRDs | litmus (chaos infrastructure 3.33.0, Argo v3.3.1) vs **argo-workflows** | **REFUSED by policy.** Never enrol a ChaosCenter chaos infrastructure. Platform Argo runs Litmus faults (game days) through `litmus-checker`/`adhar gameday fault`.
- **CRD ownership.** It stays a two-writer fight even though both sides would ship minimal CRDs, so policy #1 also guards the CRDs.
- **Naming.** Our metrics Service is named `argo-workflows-controller-metrics`, so even a blocked collision attempt cannot claim our name.
- **Litmus docs** (`application/litmus/README` / `docs/GAMEDAYS.md`): ChaosCenter is a catalogue browser and designer only (D-17).

### 5.12 Stale consumers

| Consumer | Action | Phase |
|---|---|---|
| `compositions/pipeline/argo-workflows.yaml` (`pipeline-argo-workflows`: `alpine:latest`, missing SA and ConfigMap, Sensors with no EventBus). Nothing selects it. | **Delete.** The `Pipeline` XR stays Tekton and local. Update the console XR description ("on Tekton, Argo Workflows or the local runner", `xr-sections.ts:40`). | W0 (controller release) |
| `application/adhar-templates/argo-workflows/` (legacy skeleton running as SA `admin`) | **Delete.** Golden paths live in the Gitea mirror of `adhar-io/adhar-templates`. | W0 |
| `core/adhar-console/manifests/install.yaml`: ClusterRole/Binding `console-argo-workflows` (cluster-wide `create workflows`) and env `ARGO_WORKFLOWS_URL` | **Delete both.** The BFF's argo-server URL becomes `ARGO_SERVER_URL=http://argo-server.adhar-system.svc.cluster.local:2746` (in-cluster). | W0 / W1 |
| `cmd/pipeline/*` (`adhar pipeline template list\|run`, default namespace `argo`) | Deprecate toward `adhar workflow template …` for one release, then remove (D-21) | W1 |
| Console `?section=workflows` XR page (`home.tsx:313-318`, `xr-kinds.tsx:403-405`, `compositeworkflows`) | Reclaimed by the Workflows module | W1 |
| Builder exporter `packages/builder/src/lib/argo-export.ts:133` (`spec.schedule`) | Emit `schedules: [ … ]` (v4) | W0 |
| `CONFLICTS.md:28` kubeflow row | Still true. Note that kubeflow's artifact repository now comes through `artifactRepositoryRef`. | W0 |

### 5.13 Enablement, lifecycle and rollback

| File / test | Change |
|---|---|
| `platform/stack/adhar-appset-production.yaml` | `argo-workflows` → `enabled: "true"`, `manifestPath: application/argo-workflows/manifests/platform`; comment updated ("the platform's orchestration engine; see docs/design/0026") |
| `platform/stack/adhar-appset-local.yaml` | Same `manifestPath`, `enabled: "false"` (D-2). `TestLocalProductionAppSetParity` holds (local ⊆ production). |
| `platform/stack/environments/{prod,local}/config.yaml` | Mirror exactly (`TestEnvironmentConfigsMatchAppSets`, `parity_test.go:126`) |
| `platform/stack/environments/{dev,test}/config.yaml` | Same `manifestPath` and `enabled` as prod, for consistency even though these files are untested |
| `adhar-package.yaml` | `version: "v4.1.6"`, `localSafe: true` (footprint: 2 small pods and a 10 Gi CNPG), `stability: beta` until W2 is live-verified. **`verification` is rewritten only by `adhar stack verify --write`** (ADR-0014). |
| CLAUDE.md | Remove `argo-workflows` from "Off by default in EVERY profile". Production count 76 → **77**; in W4 add the `argo-workflows-triggers` element (elements 104 → **105**). Add a "Workflows and game days" implementation note: minimal CRDs, exclusions, tenancy rules, reserved service accounts, single controller. |
| Rollback | Switch the element off (Marketplace or Git). INV-11 keeps the CRDs, the Database and the Bucket, so templates (in Git) and history (archive) survive. A run in flight is orphaned until re-enable. **Purge** is a documented, explicit runbook (`docs/WORKFLOWS.md#purge`): delete the CRDs, then the Database XR (CNPG then deletes its PVCs; barman backups stay in `s3://adhar-backups/cnpg/argo-workflows-db`). |

---

## 6. The console Workflows module (adhar-console)

### 6.1 Information architecture

The **sidebar** (`packages/shell-ui/src/nav-tree.tsx`, at most 2 levels) gets a new level-1 item in the Platform section, between *Kubernetes* and *Adhar Resources*:

```ts
{
  id: 'platform-workflows', label: 'Workflows', to: '/platform', search: 'workflows',
  icon: <IconWorkflow />, description: 'Automate, approve and orchestrate', roles: LIFECYCLE_ROLES,
  children: [
    { id: 'platform.workflows',          label: 'Overview',  to: '/platform', search: 'workflows' },
    { id: 'platform.workflow-catalog',   label: 'Catalog',   to: '/platform', search: 'workflow-catalog' },
    { id: 'platform.workflow-designer',  label: 'Designer',  to: '/platform', search: 'workflow-designer' },
    { id: 'platform.workflow-runs',      label: 'Runs',      to: '/platform', search: 'workflow-runs' },
    { id: 'platform.workflow-schedules', label: 'Schedules', to: '/platform', search: 'workflow-schedules' },
    { id: 'platform.workflow-triggers',  label: 'Triggers',  to: '/platform', search: 'workflow-triggers' },
    { id: 'platform.workflow-history',   label: 'History',   to: '/platform', search: 'workflow-history' },
    { id: 'platform.workflow-engine',    label: 'Engine',    to: '/platform', search: 'workflow-engine', roles: PLATFORM_OPS_ROLES },
  ],
}
```

**Visibility.** The row is visible to everyone who builds software (`LIFECYCLE_ROLES`), because teams own their workflows. Whether Argo is installed comes from the capability probe, not the nav. When the CRDs are absent, every Workflows page shows one honest `EmptyState`: *"Argo Workflows is not enabled on this cluster"*. Platform staff also get *"Enable it in Marketplace"* (`CONSOLE_PERMISSIONS.packagesToggle`). This is the pattern game days already use (`chaos-gameday.tsx:70-74`).

**Deep links:**

| Page | Link |
|---|---|
| Run | `?section=workflow-runs&run=<ns>/<name>[&node=<nodeId>][&tab=graph\|timeline\|logs\|io\|events\|yaml]` |
| Designer | `?section=workflow-designer&template=<wt\|cwt\|cron>/<ns>/<name>`, `&draft=<id>` or `&starter=<id>` |
| History | `?section=workflow-history&uid=<uid>&ns=<ns>` |
| Catalog | `?section=workflow-catalog&q=<text>` |
| Game days (unchanged home) | `/deliver?section=chaos&tab=gamedays` with `&plan=`, `&run=`, `&report=`, `&designer=`, `&calendar=1` |

**Command palette (⌘K):**
- "New workflow…"
- "Run a template…"
- "Open run…" (searches active runs)
- "Approvals waiting for me"

**Scope.** The top-bar organisation, project, environment and cluster scope filters every list (`useActiveNamespace`, `useTeamProjects`, `useActiveCluster`).

**Reclaiming `?section=workflows`.** In `modules/platform/src/home.tsx`:
1. Delete the dead `workflows` XR entry (`home.tsx:313-318`) and `WorkflowsView` (`views/xr-kinds.tsx:403-405`).
2. Dispatch every `workflow*` section to `lazy(() => import('./workflows/area.tsx'))`.

That keeps @xyflow, `yaml` and Monaco out of the Home chunk, as the knowledge graph already does (`home.tsx:53`).

### 6.2 Code layout

```
modules/platform/src/
├── components/flow-css.ts          ensureFlowCss() moved here from views/graph-canvas.tsx (one stylesheet injection,
│                                   two consumers: the knowledge graph and the workflow canvas)
├── workflows/
│   ├── area.tsx                    section router, capability gate, scope bar
│   ├── data/                       pure modules have *_test.ts beside them (Deno test, no React)
│   │   ├── gvr.ts                  WORKFLOWS / WORKFLOW_TEMPLATES / CLUSTER_WORKFLOW_TEMPLATES / CRON_WORKFLOWS /
│   │   │                           WORKFLOW_EVENT_BINDINGS / GAMEDAYS GVRs + Argo label constants (F-10)
│   │   ├── capabilities.ts         GET /api/workflows/capabilities, derived flags
│   │   ├── runs.ts                 list/watch hooks; gateway actions: submit, stop, terminate, suspend, resume, delete,
│   │   │                           resubmit (non-memoized); BFF actions: retry, memoized resubmit, approve
│   │   ├── nodes.ts                inflate compressedNodes; status.nodes → run graph; phase → StatusKind; durations
│   │   ├── catalog.ts              templates / cluster templates / crons → catalogue entries; parameter schema
│   │   ├── archive.ts              BFF archive client
│   │   ├── schedules.ts, cron.ts   describe + next N fire times (5-field, IANA zone), recovered from the deleted wf-model
│   │   ├── triggers.ts             bindings, webhook bundle, test-fire
│   │   ├── approvals.ts            pending approval extraction; client-side eligibility (server re-checks)
│   │   └── publish.ts              home resolution (app/project), PublishRequest builder, PR state
│   ├── model/                      the designer's brain — pure, exhaustively tested
│   │   ├── doc.ts                  yaml@2.9.1 Document wrapper: parse, print (stable options), getIn/setIn/deleteIn,
│   │   │                           ranges for diagnostics, clone for undo
│   │   ├── project.ts              Document → DesignerGraph (§6.5)
│   │   ├── ops.ts                  edit operations → Document mutations (+ inverse for undo)
│   │   ├── kinds.ts                node-kind registry: palette metadata, defaults, inspector schema, compile rules
│   │   ├── depends.ts              parse/print Argo `depends` expressions; dependencies → depends migration
│   │   ├── validate.ts             client rules (§6.7)
│   │   ├── layout.ts               deterministic layered layout
│   │   ├── positions.ts            adhar.io/designer-positions codec
│   │   └── corpus/ + roundtrip_test.ts   golden corpus: upstream examples @v4.1.6, every stack template, game-day renders
│   ├── canvas/
│   │   ├── workflow-canvas.tsx     ReactFlow wrapper: mode 'edit' | 'run' | 'staged' (game days)
│   │   ├── nodes/                  step-node, group-node, approval-node, fanout-node, exit-lane, stage-column
│   │   ├── edges/depends-edge.tsx  condition-aware edge (label, dash, run-mode colouring)
│   │   ├── palette.tsx, inspector/*.tsx, toolbar.tsx, diagnostics-bar.tsx, yaml-panel.tsx (Monaco)
│   └── views/                      overview, catalog, designer, runs, run-view, schedules, triggers, history, engine,
│                                   run-form, publish-dialog, test-run-dialog
└── gamedays/                       §8.8 (data/plan.ts, data/catalogue.ts, data/runroom.ts, data/report.ts, views/*)

apps/console/app/server/
├── workflows/
│   ├── handler.ts                  /api/workflows/* dispatch
│   ├── argo-server.ts (+_test)     identity selection (D-5), namespace pinning, query allow-list, timeouts
│   ├── authz.ts (+_test)           operation → SSAR mapping (one table, unit-tested)
│   ├── publish.ts (+_test)         Gitea branch/commit/PR as the user (Sudo), kustomization wiring, CI-path refusal,
│   │                               optimistic concurrency (blob sha), semantic no-op detection
│   ├── logs.ts (+_test)            live pod → Loki fallback; window from node timestamps
│   ├── recorder.ts (+_test)        metadata watch → notifications + CloudEvents; approvals watch (§7.4)
│   └── model.ts (+_test)           request validators (pure)
├── gamedays/                       handler, recorder, report-model, actions (Gitea issues / Plane), safety
├── bus.ts (+_test)                 CloudEvents publisher to the Kafka bridge (bounded queue, drop-oldest, deterministic ids)
└── server.ts                       + `/api/workflows/*` and `/api/gamedays/*` dispatch lines next to /api/resilience
```

**Dependencies.**
- `yaml@2.9.1` goes in `modules/platform/deno.json` and the console app's imports (D-20).
- `@xyflow/react` stays on the locked **12.12.0** (2026-09-24). 12.12.1 is two days old as of writing [S41]; bump in a routine dependency pass.
- **No layout library.** The deterministic in-house layering matches `graph-canvas.tsx` and the deleted `layoutGraph`.

**Prior art to recover** (rename `@adhar-console/*` imports to `@adhar/*`):
- `git show cbc4c8d^:modules/develop/src/data/wf-model.ts` (cron helpers, `findCycle`, `stepDepths`, positions codec)
- `wf-starters.ts` (starter catalogue)
- `views/wf-designer.tsx` (interaction patterns only)

The old graph-first model is **replaced** by the YAML-first model (§6.5). It kept only container steps and could not open arbitrary templates.

### 6.3 Data layer

**Kubernetes objects** go through `@adhar/api-clients/k8s` `kube.{list,get,apply,patch,delete}` (gateway) and live updates through `useLiveRefetch(watchOf(GVR, {namespace, labelSelector, cluster}))`. This is exactly the chaos module's pattern (`data/chaos.ts:422-447`).

- **Lists are namespace-scoped and label-scoped.**
  - *Active* runs use `workflows.argoproj.io/completed=false`, with `limit: 200`.
  - *Recent* runs (inside the TTL window) are a second query.
  - Older runs come from the archive.
  - Platform staff with cluster-wide list rights (`useCan`) get an "All namespaces" toggle.
- **Run detail is one watched object.** `nodes.ts` inflates `status.compressedNodes` (base64 gzip, the v4.1 default [S25]) with `DecompressionStream('gzip')`. If inflation fails (another algorithm, or offloaded), the detail falls back to `GET /api/workflows/runs/:ns/:name` (the hydrated workflow from argo-server).
- **Run graph mapping.**
  - DAG task nodes map by `displayName` (task name) and `boundaryID`.
  - `Retry` nodes group their attempt children.
  - `withItems`/`withParam` fan-outs (`TaskGroup`) collapse into one node "×N" with a progress bar and a paged child list.
  - Phases map to `StatusKind`: Pending → `progressing` (dimmed), Running → `progressing`, Succeeded → `healthy`, Failed/Error → `failed`, Skipped/Omitted → `unknown`, a suspended node waiting for approval → `paused`.
- **Capabilities.** `GET /api/workflows/capabilities` returns the following. It is cached for 60 s per cluster and is the only thing the module asks before rendering.

  ```ts
  { installed: boolean;            // workflows.argoproj.io served
    version?: string;               // argo-server /api/v1/version
    argoServer: { reachable: boolean; identity: 'user' | 'service' | 'none' };
    archive: boolean; artifactNamespaces: string[];          // ['adhar-system'] until W5
    triggers: { bindings: boolean; platformEvents: boolean }; // relay installed (W4)
    gamedays: boolean;               // gamedays.platform.adhar.io + litmus CRDs
    volcano: boolean; batch: boolean // queues.scheduling.volcano.sh, batchjobs.platform.adhar.io (0025)
  }
  ```
- **Writes the gateway performs as the user:**
  - submit: a `Workflow` with `workflowTemplateRef`, `arguments`, and labels `workflows.argoproj.io/workflow-template` (or `cluster-workflow-template`) and `adhar.io/template-spec-hash`. **The client never sets a service account**: the run form has no such field and the BFF strips `serviceAccount` from argo-server `SubmitOpts`.
  - `spec.shutdown: Stop | Terminate`
  - `spec.suspend: true | false`
  - delete
  - non-memoized resubmit: a new Workflow with label `workflows.argoproj.io/resubmitted-from-workflow`

### 6.4 Console server routes (BFF)

All routes:
- validate `:ns` and `:name` as DNS-1123;
- run `getRequestUser`, then `authorize(req, check)` **as the user** (`apps/console/app/server/k8s/access.ts:355`);
- choose the argo-server identity per D-5;
- record `writeAudit(store, actor, action, target, req, metadata)` for every mutation (`workspace/store.ts:240`) plus the structured `audit()` log line (`k8s/gateway.ts:103`);
- return `{error, detail}` JSON with 400/401/403/404/409/502/503.

The argo-server base URL is `ARGO_SERVER_URL` (in-cluster `http://argo-server.adhar-system.svc.cluster.local:2746`).

| Method & path | Body / query | Check (SSAR as the user, unless stated) | Upstream call |
|---|---|---|---|
| `GET /api/workflows/capabilities` | `cluster?` | signed in | discovery; argo-server `GET /api/v1/version` |
| `GET /api/workflows/runs/:ns/:name` | — | `get workflows` in `:ns` | argo-server `GET /api/v1/workflows/{ns}/{name}` (hydrated; used only when the browser cannot inflate) |
| `POST /api/workflows/runs/:ns/:name/retry` | `{restartSuccessful?, nodeFieldSelector?, parameters?}` | `update workflows` | `PUT /api/v1/workflows/{ns}/{name}/retry` [S8] |
| `POST /api/workflows/runs/:ns/:name/resubmit` | `{memoized: true, parameters?}` | `create workflows` | `PUT …/resubmit`, then BFF patches `adhar.io/submitted-by` on the new Workflow (service mode) |
| `POST /api/workflows/runs/:ns/:name/approve` | `{nodeId, decision: 'approve'\|'reject', comment?}` | `update workflows`, **plus** the approver policy: the node's inputs `approvers` (groups/users; empty = anyone with update) and `forbidSelfApproval` (≠ `adhar.io/submitted-by`) | `PUT …/set` `{nodeFieldSelector: "id=<nodeId>", outputParameters: "{\"decision\":…,\"comment\":…,\"approver\":<server-derived user>}", message: "<decision> by <user> via Adhar console"}`, then `PUT …/resume` `{nodeFieldSelector}` [S8][S18] |
| `GET /api/workflows/runs/:ns/:name/logs` | `node`, `container=main\|wait\|init`, `follow?`, `tail?` | `get pods/log` | pod exists: gateway `pods/log`. Else Loki `{namespace="<ns>", pod="<pod>"}` over `[startedAt−1m, finishedAt+1m]` (console Loki tool). Response says `source: live\|loki`. |
| `GET /api/workflows/runs/:ns/:name/artifacts/:nodeId/:artifact` | — | `get workflows`; `:ns` ∈ `artifactNamespaces` | argo-server `GET /artifact-files/{ns}/workflows/{name}/{nodeId}/outputs/{artifact}` (streamed, `content-disposition` preserved) |
| `GET /api/workflows/archive/:ns` | `limit≤100, continue, phase, template, submitter, since` | `list workflows` | `GET /api/v1/archived-workflows?namespace=<ns>&listOptions.labelSelector=<built server-side from allow-listed filters>&listOptions.limit&listOptions.continue`. **Never** forwards `fieldSelector` (F-1/GHSA-q65w). |
| `GET /api/workflows/archive/:ns/:uid` | — | `get workflows` | `GET /api/v1/archived-workflows/{uid}`; **404 unless `metadata.namespace == :ns`** |
| `POST /api/workflows/archive/:ns/:uid/{retry,resubmit}` | as above | `create workflows` | `PUT /api/v1/archived-workflows/{uid}/{retry,resubmit}` with body `namespace: :ns` |
| `DELETE /api/workflows/archive/:ns/:uid` | — | console permission `workflowsAdmin` | `DELETE /api/v1/archived-workflows/{uid}` |
| `POST /api/workflows/lint` | `{namespace, kind, manifest}` | `create <kind>` in `namespace` (ClusterWorkflowTemplate: platform roles) | `POST /api/v1/{workflow-templates/{ns}\|cron-workflows/{ns}\|cluster-workflow-templates\|workflows/{ns}}/lint` |
| `POST /api/workflows/publish` | `PublishRequest` (below) | Gitea permissions of the user (the write runs with `Sudo`, `gitea-sudo.ts`), and `create workflowtemplates` in the home's dev namespace | Gitea contents, branches and pulls API; server-side lint + `dryRun` apply first |
| `GET /api/workflows/history` | `repo, path` | the user can read the repository (Gitea `GET /repos/{o}/{r}` with `Sudo`) | `GET /repos/{o}/{r}/commits?path=` and open PRs touching `path` |
| `POST /api/workflows/drafts/:ns/test-run` | `{manifest: WorkflowTemplate, arguments}` | `create workflowtemplates` + `create workflows`; `:ns` has `adhar.io/tier: dev` | gateway apply (as the user) of `<name>-draft-<user-slug>` (labels `adhar.io/workflow-draft`, `adhar.io/draft-expires` = now+7 d), then submit |
| `POST /api/workflows/triggers/:ns/webhooks` | `{name, template, selector, parameters[]}` | `create` on workfloweventbindings, serviceaccounts, roles, rolebindings, secrets in `:ns` (dev/test; prod is publish-only) | gateway: the bundle in §7.2 |
| `GET /api/workflows/triggers/:ns/webhooks/:name/token` | — | `get secrets` in `:ns` | gateway; shown once, never logged |
| `POST /api/workflows/triggers/:ns/webhooks/:name/test` | `{payload}` | `create workflows` | argo-server `POST /api/v1/events/{ns}/{discriminator}` |
| `GET /api/workflows/engine` | — | `workflowsAdmin` or platform roles | Prometheus (controller metrics), archive DB size, per-namespace parallelism labels |
| `POST /api/workflows/engine/schedules/suspend-all` | `{namespaces?, reason}` | `workflowsAdmin` | gateway, as the user (platform staff hold the rights), patch `spec.suspend` on CronWorkflows |

```ts
interface PublishRequest {
  home: { kind: 'app'; repo: string /* org/name */ } | { kind: 'project'; project: string };
  files: Array<{ path: string; content: string; baseSha: string | null }>; // WT/Cron/WEB YAML only
  mode: 'pr' | 'commit';         // 'commit' only if Gitea allows the user to push to the base branch
  branch?: string;               // default main
  title: string; description?: string;
}
// Server rules: paths must be under deploy/base/workflows/ (app) or automation/{workflows,gamedays}/ (project);
// CI paths are refused exactly like scorecard fixes (scorecard-fix.ts CI_PATH); a file whose parsed content is
// JSON-equal to the base is dropped (no-op saves create no commits); the first publish into an app repo also adds
// `workflows` to deploy/base/kustomization.yaml resources (parsed and re-printed with yaml@2.9.1).
```

**Draft clean-up** needs no console privilege. A Kyverno `ClusterCleanupPolicy` (`adhar-workflow-draft-expiry`, every 30 min) deletes WorkflowTemplates labelled `adhar.io/workflow-draft: "true"` whose `adhar.io/draft-expires` has passed. Its cleanup controller is granted `delete workflowtemplates` through an aggregated ClusterRole (`rbac.kyverno.io/aggregate-to-cleanup-controller: "true"`) shipped in the package.

**New console permissions** (`packages/auth/src/console-permissions.ts`; grants in the platform's `core/adhar-console/manifests/console-rbac.yaml`; documented in console `docs/rbac.md`):

| Permission | Virtual resource / verb | Granted to |
|---|---|---|
| `workflowsAdmin` | `workflows-admin` / `update` | platform-admin, platform-engineer (cluster-admin already covers `console.adhar.io`) |
| `gamedaysRunProduction` | `gamedays` / `run-production` | platform staff, plus an explicit binding per production-approver group (D-14) |
| `gamedaysApprovePlan` | `gamedays` / `approve` | platform staff and named approvers |
| `chaosSafetyLever` | `chaos-safety` / `update` | platform staff and on-call (Keycloak group `oncall`, bound in console-rbac.yaml) |

### 6.5 The designer model (YAML-first round trip)

**Principle.** The parsed YAML **Document** is the state; the canvas is a projection (INV-6). This is the deleted model's own rule ("the graph is DERIVED from the spec, never stored alongside it", `wf-model.ts` header) taken one step further: the graph is derived from the *document*, not from a typed model of it. Fields the designer does not model are never read into memory as model fields, so they cannot be dropped.

```ts
// model/project.ts — the projection (pure)
interface DesignerGraph {
  resource: { kind: 'WorkflowTemplate' | 'ClusterWorkflowTemplate' | 'CronWorkflow' | 'Workflow'; name: string; namespace?: string };
  entry: string;                    // spec.entrypoint (CronWorkflow: spec.workflowSpec.entrypoint)
  editable: boolean;                // false when entry is `steps`, or the doc has errors; see below
  params: Array<{ name: string; value?: string; enum?: string[]; description?: string; secretRef?: { name: string; key: string } }>;
  nodes: DesignerNode[];            // DAG tasks of the entry template (+ group nodes for nested DAGs)
  edges: DesignerEdge[];            // derived from depends / dependencies
  exitLane?: { template: string; nodes: DesignerNode[]; edges: DesignerEdge[] };   // spec.onExit
  diagnostics: Diagnostic[];        // §6.7, each with a Document path → node id + YAML range
}
interface DesignerNode {
  id: string;                       // task name
  kind: NodeKind;                   // §6.6, inferred from the referenced template (never stored)
  template: { local?: string; ref?: { name: string; template: string; clusterScope?: boolean } };
  sharedBy: number;                 // other tasks using the same local template (inspector warns on edit)
  badges: { when?: string; retry?: number; timeout?: string; fanOut?: 'items' | 'param' | 'sequence'; hooks?: number; continueOn?: boolean };
  position: { x: number; y: number; fromAnnotation: boolean };
  path: (string | number)[];        // Document path of the task (for ops and diagnostics)
}
interface DesignerEdge {
  source: string; target: string;
  condition: 'Succeeded' | 'Failed' | 'Errored' | 'Skipped' | 'Daemoned' | 'AnySucceeded' | 'AllFailed' | 'Expression';
  expression?: string;              // the full `depends` when it is not a pure conjunction of single terms
}
```

**Projection rules:**

| Case | Behaviour |
|---|---|
| Entry is a `dag` template | Full editing |
| Entry is a `steps` template | Read-only columns, one per parallel group. The action "Convert to DAG" rewrites it explicitly and shows the diff before applying. |
| A task references another local `dag` template | A **group node**, collapsed by default, which expands inline (editable from W3) |
| Workflow-level `onExit` | Drawn in a separate **exit lane** under the main graph |
| `when` | A node badge |
| `withItems`/`withParam`/`withSequence` | A fan-out badge (×N) |
| `hooks` | A badge |
| `continueOn` | A badge; the inspector explains it cannot be combined with `depends` (CEL rule, W1 A.2) |
| `depends` expressions | Parsed by `depends.ts`. A pure `&&` of single terms becomes one edge per term, labelled by condition. Anything else (`(A \|\| B) && C`) draws dashed edges to each referenced task, puts an "expression" badge on the target and is edited as text in the inspector. Legacy `dependencies` lists draw success edges. |
| Positions | Read from `adhar.io/designer-positions`. Tasks without one are laid out by `layout.ts`, and only a drag writes positions back to the annotation (on drag *end*, as `packages/builder/src/components/flow-canvas.tsx:192-199` does). |
| A document that does not parse | The canvas freezes on the last good projection under a banner "YAML has errors". It never auto-fixes. |

**Edit operations** (`model/ops.ts`). Each is one Document mutation plus its inverse; undo/redo keeps ≤ 100 `Document.clone()` snapshots.

| Op | Mutation |
|---|---|
| `addNode(kind, at)` | Append a template named from the kind registry (unique), append a DAG task `{name, template}`, write its position |
| `addRef(ref, at)` | Append a task with `templateRef` (WorkflowTemplate or ClusterWorkflowTemplate) and default `arguments` from the referenced inputs |
| `connect(a, b, cond)` | Add the term `a.<Cond>` (or bare `a` for Succeeded) to `b.depends` with `&&`. If `b` uses `dependencies`, a Succeeded edge appends to it; any other condition **migrates** `b` to `depends` (you cannot use both, per the CEL rule) and records it in the change summary. |
| `disconnect(a, b)` | Remove the term; drop `depends` when empty |
| `removeNode(id)` | Remove the task and its terms in other tasks. Delete its local template only if no task, exit handler or hook still references it; otherwise ask. |
| `rename(id, to)` | Rename the task, every `depends`/`dependencies` term, every `{{tasks.<id>.…}}` token in the DAG's arguments, `when` and outputs (token-boundary match), and the positions key |
| `set(path, value)` / `unset(path)` | Inspector edits on the task or its template |
| `autoLayout()` | Rewrite every position (one undo step) |

**Printing.**
- `doc.toString({ indent: 2, lineWidth: 0, minContentWidth: 0 })`. Comments and key order survive, and so do untouched nodes. Byte-identity is **not** promised for re-flowed scalars; JSON-equality and comment retention are.
- Publish drops a file whose parsed content is JSON-equal to its base, so opening and saving never creates a commit.

**Two-way editing.**
- The YAML panel (Monaco, `components/code-editor.tsx`) and the canvas share the one Document.
- Typing in YAML re-parses after a 400 ms debounce and re-projects. Canvas ops update the Document and the panel keeps its cursor by path.
- Diagnostics map to both (§6.7).

**Corpus test** (`model/roundtrip_test.ts`). For every file in the corpus:
- `print(apply(noop, parse(f)))` is JSON-equal to `f`, and every comment line survives;
- `project` is deterministic;
- `rename`/`connect`/`disconnect` followed by their inverses restores JSON-equality.

The corpus holds:
- all upstream `examples/*.yaml` at the pinned tag (DAG, steps, exit handlers, hooks, fan-out, intermediate parameters);
- every WorkflowTemplate and ClusterWorkflowTemplate in the stack;
- the GameDay composition's golden renders.

### 6.6 Node kinds (palette → compiled template)

| Palette node | Compiles to | Inspector fields | Guardrails shown |
|---|---|---|---|
| **Container** | local `container` template | image (registry allow-list hint, `restrict-image-registries-enforce`), command/args, env, **secret env** (`valueFrom.secretKeyRef`, picker over the namespace's Secrets by name only), resources (**required**), working dir, `emptyDir` volumes, run-as-non-root toggle (default on for new nodes) | warns when a parameter is interpolated into `args` (prefer env) |
| **Script** | local `script` template | presets: bash, python, node, deno; source (Monaco); env | parameters are passed as env `PARAM_<NAME>`, never spliced into `source` (shell injection) |
| **Kubernetes resource** | `resource` template | action (create, apply, patch, delete, get), manifest (YAML), `successCondition`/`failureCondition`, `setOwnerReference` | "needs RBAC: `<verb> <kind>` for adhar-workflow" (computed from the manifest) |
| **HTTP call** | `http` template | url, method, headers (secret-backed), body, `successCondition`, `timeoutSeconds` | "runs in the workflow's agent pod; must be idempotent" [S20] |
| **Approval** | `templateRef: {name: adhar-steps, template: approval, clusterScope: true}` | question, approvers (Keycloak groups/users picker), timeout, decision on timeout (fixed: **reject**), forbid self-approval | adds `adhar.io/has-approvals: "true"` to `workflowMetadata.labels` |
| **Timer** | `suspend: {duration}` | duration | — |
| **Template** | `templateRef` to a WorkflowTemplate in scope or a catalogue ClusterWorkflowTemplate | picker, template-within, arguments form generated from the referenced `inputs.parameters` (`enum` becomes a select) | — |
| **Platform step** | `templateRef` to `adhar-steps/*` (`http-check`, `wait-healthy`, `k6-load`) | schema from the ClusterWorkflowTemplate's inputs | — |
| **Sub-flow** | a nested local `dag` template | name; children edited in place (W3) | — |
| **Batch job** | `templateRef: adhar-batch-steps/run` (0025) | BatchJob picker (`batchjobs.platform.adhar.io` in scope), parameters from its declared schema, queue (read-only, from the BatchJob) | hidden unless `capabilities.batch` |
| **Chaos experiment** | `templateRef: adhar-steps/run-gameday` (starts a **GameDay plan** as a child run) | plan picker (the project's GameDays); "New plan…" opens the game-day designer | faults run only through vetted plans (INV-4); hidden unless `capabilities.gamedays` |
| **Exit handler** | `spec.onExit` → exit lane | nodes inside the lane | — |

**Starters gallery** (recovered `wf-starters.ts`, extended):
- Blank DAG
- Nightly maintenance (cron)
- Approval-gated release check
- Fan-out over items
- ETL with data-quality gate
- Smoke test after promotion (event-triggered)
- Game-day launcher
- Batch pipeline (0025, hidden until available)

### 6.7 Validation pipeline

Diagnostics come from three levels, merged, and each is mapped to a node badge and a Monaco marker. Level C uses `yaml` node ranges.

| Level | When | What |
|---|---|---|
| **A: client** (`validate.ts`, pure) | every edit | <ul><li>**Argo's CEL rules mirrored** (minimal CRDs mean the API server no longer enforces them, D-3): ≤ 200 DAG tasks; a task name cannot begin with a digit when `depends`/`dependencies` is used; never both `depends` and `dependencies`; no `continueOn` with `depends`; at most one template type per template; no `timeout` on steps or DAG templates; `activeDeadlineSeconds` only on leaf templates; at most one artifact location (W1 A.2).</li><li>**v4 semantics:** outputs read from a task that can be skipped (it has `when`, or is downstream of a conditional) must declare `valueFrom.default`, or v4 fails the node [S3]; `schedules` (not `schedule`); `synchronization.mutexes[]`/`semaphores[]` (lists).</li><li>**Graph:** cycles (`findCycle`); unreachable tasks; dangling `{{tasks.x…}}` references.</li><li>**Platform hints:** missing resources; non-allow-listed image registries; reserved service accounts; secrets in parameters (names matching `(?i)pass|token|secret|key`); parameter splicing into scripts; a missing `activeDeadlineSeconds` is defaulted (info).</li></ul> |
| **B: admission dry-run** | 2 s after idle, and before publish or test-run | `kube.apply(manifest, { dryRun: true })` as the user. This runs RBAC and **Kyverno** (reserved service accounts, templates-only, drafts dev-only) and the minimal-CRD structural pass. |
| **C: argo-server lint** | before publish or test-run; on demand ("Validate") | `POST /api/workflows/lint` gives full semantic validation (template resolution, expression syntax, `templateRef` existence) |

Publish and test-run are disabled while any error remains. Warnings need an explicit "Publish with 2 warnings".

### 6.8 Canvas implementation and visual design

**React Flow configuration** (`canvas/workflow-canvas.tsx`):

| Area | Setting |
|---|---|
| Mode | Controlled `nodes`/`edges` derived from the projection, memoised by Document version. `nodesDraggable` in `edit` mode only. `nodesConnectable` in `edit` mode (handles on node edges). `onlyRenderVisibleElements` above 150 nodes. `snapToGrid` (8 px). |
| Drag from the palette | `dataTransfer` MIME `application/x-adhar-step` + `screenToFlowPosition` (the builder canvas pattern, `flow-canvas.tsx:213-222`) |
| Connect | `onConnect` → `connect(a, b, 'Succeeded')`. Shift-drag opens the condition picker. |
| Chrome | `Background` dots (`gap 22`, `var(--color-edge-strong)`), `Controls` (bottom-left), `MiniMap` coloured by kind (edit) or status (run), `Panel` toolbar (top). Styles come from the shared `ensureFlowCss()` with the `.adhar-flow` scope. The federated remote does not carry React Flow's stylesheet (`graph-canvas.tsx:52-68`). |
| Run mode | Positions from the template's annotation if the run came from a template (stable layout between design and run), else `layout.ts`. Status updates are throttled to 2 Hz from the live watch. Only node `data` changes; the arrays are not rebuilt. |

**Layout** (`layout.ts`, deterministic):
1. Longest-path layering, so a task's column is one more than its deepest dependency.
2. Two passes of barycentric ordering within each column.
3. Ties broken by task name.
4. The exit lane is laid out separately below.

The same template always draws the same way. That is the property the knowledge graph relies on ("the red pod on the left means something", `graph-canvas.tsx:33-38`).

**Visual language.** Shell-ui primitives and theme tokens only. The canvas lives in `modules/platform`, never in shell-ui, which is bundled into every remote.

- **Node card.** 248 × 72 in edit mode; 248 × 88 in run mode, with an elapsed and progress footer. Layout:
  - left: a 40 × 40 icon tile in the kind's hue;
  - middle: the task name (13 px, semibold) and a subtitle (image short name or `template@ref`, 11 px, muted, monospace);
  - right: chips for retry ×N, ⏱ timeout, `when`, ×N fan-out and hooks.

  Kind hues are token-based and tested in both themes: container sky, script violet, resource teal, HTTP indigo, approval amber, timer slate, template cyan, platform step emerald, batch orange, chaos rose.
- **Status (run mode).** The same mapping and hues as the Tekton run canvas (`views/tekton.tsx:2026-2037`: healthy `#10b981`, failed `#f43f5e`, progressing `#6366f1`, paused `#f59e0b`, else `#94a3b8`), drawn as the card's ring with a soft pulse while running. Edges are **green behind, indigo animated on the active path, grey ahead, red where it broke** (the Tekton canvas's stated language, `tekton.tsx:2043-2050`).
- **Edges.**
  - Smoothstep, 1.5 px, `ArrowClosed`.
  - Failure-condition edges are rose and dashed, with the label "on failure".
  - `when`-guarded targets get an amber dashed incoming edge, labelled with the condition truncated to 24 characters.
  - Expression edges are slate and dashed, with an `ƒ` badge.
- **Layout of the designer.** Toolbar across the top:
  - undo / redo
  - auto-layout
  - fit
  - zoom
  - validate (with a count)
  - YAML toggle
  - test run (dev only)
  - publish (with branch/PR state)

  Palette on the left: search, then sections for Steps, Flow control, Platform steps, Batch and Chaos. Inspector on the right, with tabs General · Inputs & outputs · Retry & timeout · Conditions · Resources · Advanced. Diagnostics bar at the bottom. On narrow screens the palette and inspector collapse into sheets.
- **Empty states.**
  - Empty canvas: "Drag a step from the palette, or start from a template", with starter cards.
  - Empty catalogue: "No templates yet in <project>", with links to the designer and to docs.
  - Not installed: §6.1.
- **Motion.** `prefers-reduced-motion` turns off pulses and edge animation, and marks running edges with a static pattern instead.
- **Keyboard.**

  | Key | Action |
  |---|---|
  | `f` | fit |
  | `/` | palette search |
  | `Enter` | open the inspector |
  | `Delete` | remove |
  | arrows (⇧ ×10) | nudge |
  | ⌘Z / ⇧⌘Z | undo / redo |
  | ⌘C / ⌘V | copy / paste tasks as YAML |
  | ⌘S | save draft |
  | ⌘↵ | test run |
  | `l` | auto-layout |

  Nodes are focusable (`nodesFocusable`) and every action has a button. Validation and run-status changes are announced through an `aria-live` region.
- **Performance budgets.** The Workflows overview loads **without** @xyflow (lazy designer and run graph chunks). The canvas holds 60 fps panning at 200 nodes. A run graph with 1,000 fan-out children renders collapsed groups by default.

### 6.9 Pages

**Overview** (`?section=workflows`)
- KPI row (shell-ui `Card`s):
  - running now
  - queued, i.e. Pending because of parallelism (`workflows.argoproj.io/phase=Pending` count)
  - succeeded and failed in the last 24 h (live + archive)
  - approvals waiting for me
  - failing schedules
- "Needs your attention":
  - my pending approvals (with Approve/Reject in place)
  - failed runs of templates my team owns
  - suspended or erroring schedules
- Recent runs (`DataTable`).
- Quick actions: New workflow · Run a template · Browse catalog.
- Platform staff also see an engine health strip: leader, queue depth, workers busy, archive state.

**Catalog** (`?section=workflow-catalog`)
- Cards or table (toggle) of WorkflowTemplates in scope, catalogue ClusterWorkflowTemplates (`adhar.io/catalog`) and CronWorkflows.
- Each card shows:
  - title and description (Argo's `workflows.argoproj.io/title`/`description`)
  - kind icon
  - home badge (App · Project · Platform · Draft) from `adhar.io/source`
  - environments where the same template name is deployed
  - parameter count
  - last 10 run outcomes as a sparkline, with success rate and p50 duration over 7 days (live + archive)
  - owner team (`adhar.io/team`)
- Filters (`FilterBar`): home, kind, environment, tags (`adhar.io/catalog-tags`), owner, "has approvals".
- Actions: **Run** (form), **Open in designer**, **Duplicate**, **History** (Git), **Schedule** (prefilled cron).

**Run form** (modal). Generated from `spec.arguments.parameters`: `enum` becomes a select, `description` becomes help text, secret parameters are refused. Advanced: labels (allow-listed `adhar.io/*` keys), priority, `dryRun` (server dry-run). It shows the resolved namespace and **"Runs as adhar-workflow"**, read-only.

**Designer** (`?section=workflow-designer`). §6.5–§6.8, plus:
- **Test run.** Applies a dev-tier draft and submits it; the run graph opens in a split view under the canvas.
- **Publish dialog**:
  - picks the home (app/project), branch and mode (PR default)
  - shows a **semantic diff** (YAML diff of the changed documents) and the diagnostics summary
  - opens the PR as the user and links it
  - subsequent saves update the same branch and PR
- **History panel.** Git commits for the file, open PRs, and "Open version" (read-only projection of an old blob).
- **Drafts.** Unsaved canvas state autosaves (debounced) to the console DB kind `workflows.draft` (client-owned), keyed by user and template.

**Runs** (`?section=workflow-runs`)
- Tabs: Active / Recent / Mine (`adhar.io/submitter`).
- Columns: status, name, template, trigger (manual, cron `workflows.argoproj.io/cron-workflow`, event `…/workflow-event-binding`, resubmit), submitter, started, duration, progress (`status.progress`), namespace.
- Row actions: stop, suspend/resume, retry, resubmit, delete. Bulk stop and delete with confirmation.

**Run view** (drawer, widenable with `DrawerWidenButton`, or full page)
- **Header:** name; phase badge; template link with spec-hash ("ran template version 3f2a…"; "template changed since" when it differs); trigger; submitter; elapsed / duration; progress; lineage (resubmitted from, retries).
- **Header actions:** **Stop** (default; runs exit handlers), **Suspend/Resume**, **Retry** (failed nodes; option "restart successful"), **Resubmit** (option "memoized"), **Terminate** (behind a "More" menu, typed confirmation, a warning that exit handlers do **not** run [S23], never offered for game days), **Delete**, **Open in Argo UI** (platform staff).
- **Tabs:**
  - **Graph** (run mode; node click opens the node panel)
  - **Timeline** (Gantt from node start and finish, grouped by task, with retries stacked)
  - **Logs** (node picker, container tabs, follow, search, severity colouring through `LogConsole`; source chip `live` / `Loki`)
  - **Inputs & outputs** (parameters, artifacts with download where available)
  - **Events** (Kubernetes events for the workflow's pods)
  - **YAML** (read-only live object; "Open template in designer")
- **Node panel:** phase, message, times, attempts, resources requested vs used (Prometheus container metrics by pod, sparkline), logs, outputs, **Retry from here** (`nodeFieldSelector`), and the **approval card** for a waiting suspend node: question, approvers, countdown, Approve/Reject with comment, "you submitted this run" when separation of duties applies.

**Schedules** (`?section=workflow-schedules`)
- Table: schedule(s) in words (`cron.ts` `describeCron`) with the next 3 fire times in the CronWorkflow's `timezone`, concurrency policy, last run (status and time), 14-day success sparkline, suspended toggle (`spec.suspend`, through the gateway), owner, home.
- Drawer: history (runs labelled with this cron), YAML, stop strategy (`stopStrategy.expression`), "Run now" (submit from the cron's `workflowSpec`).
- New and edit go through the designer's schedule tab, which edits a CronWorkflow wrapping a `workflowTemplateRef`.

**Triggers** (`?section=workflow-triggers`)
- Tabs:
  - **Webhooks** (WorkflowEventBindings with discriminator ≠ `adhar-bus`)
  - **Platform events** (discriminator `adhar-bus`, §7.3)
  - **Schedules** (link)
  - **Recent firings** (Workflows labelled `workflows.argoproj.io/workflow-event-binding`)
- Create flows: §7.2 and §7.3. Each binding shows the selector, the parameter mapping, the target template, last fired, and **Send test event**.

**History** (`?section=workflow-history`)
- Archive browser per namespace. Filters: template, phase, date range, submitter, labels.
- The run view opens on archived runs with the same tabs (logs come from Loki, within its retention).
- **Compare two runs**: parameter diff, per-node duration delta, outcome.
- CSV export.

**Engine** (`?section=workflow-engine`, platform staff)
- Controller: leader, version, queue depth and latency, workers busy, k8s request rate.
- Archive: database size, oldest row, TTL.
- Parallelism: global, namespace default, per-namespace overrides (edit = patch the namespace label, platform staff).
- Policy denials for Argo kinds (Kyverno events).
- "Suspend all schedules" (reason, audited).
- Links: Argo UI, Grafana dashboard row, docs.

### 6.10 RBAC, audit and notifications in the UI

- **RBAC.**
  - Every action button is wrapped in `<Can check={canResource(GVR, verb, {namespace})}>` (`@adhar/shell-ui` `can.tsx`; batched SSAR via `/api/k8s/-/access-batch`). **Blocked actions are hidden, not disabled.** That is the console convention; game-day participants are the one exception (§8.8).
  - BFF routes re-check, so the UI and the server cannot disagree.
- **Audit.**
  - BFF mutations call `writeAudit` with action keys `workflow.approve`, `workflow.retry`, `workflow.resubmit`, `workflow.publish`, `workflow.trigger.create`, `workflow.engine.suspend-all`, `gameday.*`.
  - Gateway mutations are in the API server's audit as the user.
  - Gitea records publishes as the user.
  - The run view shows "who did what" from Argo's actor labels, node messages and `adhar.io/submitted-by`.
- **Notifications.** Delivered by the recorder (§7.4) to the Notification Center, email, and chat webhooks configured in Workspace settings:
  - approval requested → each eligible approver
  - run failed → submitter, and the owner team for scheduled or triggered runs
  - schedule erroring → owner team
  - stranded draft expiring → author

---

## 7. Triggers and events

### 7.1 Schedules

The designer's **Schedule** tab writes a `CronWorkflow` that wraps the template:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: CronWorkflow
metadata: {name: nightly-ledger-check, namespace: shop-prod}
spec:
  schedules: ["0 2 * * *"]               # v4: a list; `schedule` was removed [S3]
  timezone: Asia/Singapore
  concurrencyPolicy: Forbid               # default in the form; Allow/Replace selectable
  startingDeadlineSeconds: 300            # no storm of missed runs after controller downtime
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  stopStrategy: {expression: "cronworkflow.failed >= 3"}   # optional: stop a schedule that keeps failing [S24]
  workflowSpec:
    workflowTemplateRef: {name: ledger-check}
    arguments: {parameters: [{name: mode, value: full}]}
```

- **"Run once at T"** is a CronWorkflow for that minute with `stopStrategy: {expression: "cronworkflow.succeeded >= 1 || cronworkflow.failed >= 1"}` (the deleted designer's approach, `wf-model.ts` Schedule `once`).
- **Publishing.** Schedules publish through GitOps like templates (§4.5). Suspend and resume from the Schedules page patch the live object. In GitOps-managed namespaces that change drifts until the next sync reverts it, so in test and prod tiers the toggle commits instead of patching. The UI says which it will do.

### 7.2 Webhooks (`WorkflowEventBinding`)

Created by the console as the user in dev and test, or committed through GitOps in prod. One bundle per trigger:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: WorkflowEventBinding
metadata: {name: on-gitea-push, namespace: shop-dev, labels: {adhar.io/trigger: webhook}}
spec:
  event:
    selector: discriminator == "on-gitea-push" && payload.ref == "refs/heads/main"
  submit:
    workflowTemplateRef: {name: integration-suite}
    arguments:
      parameters:
        - {name: commit, valueFrom: {event: payload.after}}
---
# ServiceAccount on-gitea-push-webhook; Role: workflowtemplates get (resourceNames: [integration-suite]),
# workfloweventbindings get/list, workflows create; RoleBinding; and
# Secret on-gitea-push-webhook-token (type kubernetes.io/service-account-token — no secret material in Git)
```

- **URL.** `POST https://argo-workflows.<host>/api/v1/events/<ns>/<discriminator>` with `Authorization: Bearer <token>` [S19]. The UI shows it once, through the token route.
- **Senders.**
  - **Gitea ≥ 1.19** can send an `Authorization` header [S43].
  - GitHub-style HMAC senders need a relay: a Gitea mirror today, an Argo Events adapter at W5.
- **Delivery.** Processing is asynchronous and a full queue answers 503, so senders must retry [S19].
- **Rotate** = delete the token Secret and recreate it (one click).
- **Scope.** `create workflows` cannot be narrowed by `resourceNames`, so a token can start any template in its namespace. Kyverno limits what it can start (templates only, no reserved service accounts). The UI says so.

### 7.3 Platform events → workflows (W4)

Teams subscribe to platform CloudEvents **through WorkflowEventBindings with discriminator `adhar-bus`**. One platform-owned relay forwards each event to the namespace it concerns. A tenant therefore only ever sees events about its own project. The bus itself is not tenant-isolated, which is why tenants get no Sensors of their own (D-18).

```
adhar-kafka topics adhar.{delivery,platform,tenancy,workflow,resilience,batch}
   └─► Argo Events EventSource adhar-platform-bus (Kafka, consumer group adhar-workflow-relay)
         └─► EventBus default (Kafka on adhar-kafka, topic adhar.argo-events; declared KafkaTopic, retention 1 d) [S26]
               └─► Sensor adhar-workflow-relay
                     trigger: http POST http://argo-server.adhar-system.svc:2746/api/v1/events/<ns>/adhar-bus
                       <ns> = the CloudEvent extension `adharproject` (home namespace); absent → adhar-system
                       (trigger-level parameterization of the URL, `dest: http.url` [S27])
                     body: the CloudEvent; headers: x-adhar-event-id = CloudEvent id; bearer: SA adhar-workflow-relay
```

**An example binding:** "when a release reaches prod, run the smoke test":

```yaml
spec:
  event:
    selector: >-
      discriminator == "adhar-bus" && payload.type == "io.adhar.delivery.package.synced" &&
      payload.subject == "shop/checkout-prod"
  submit:
    workflowTemplateRef: {name: smoke-test}
    arguments: {parameters: [{name: event-id, valueFrom: {event: 'metadata["x-adhar-event-id"][0]'}}]}
```

- **Semantics.** At-least-once (consumer group plus Sensor `retryStrategy`). Templates that must not repeat use `event-id` as their idempotency key.
- **Placement.** The relay ships as its own ApplicationSet element `argo-workflows-triggers` (`manifestPath: application/argo-workflows/manifests/triggers`). It is enabled in production only, because it needs argo-events and kafka-operator, which are production-only.
- **EventBus ownership.** The EventBus belongs to the **argo-events** package (`application/argo-events/manifests/eventbus.yaml`), because 0025's batch `dependsOn` Sensors need the same bus. One bus, platform-owned.
- **Spike gate (W4, first task).** Prove on Argo Events v1.9.11 that the Kafka EventBus works on `adhar-kafka` and that the URL parameterization holds. The Kafka EventBus creates per-Sensor topics; declare them as `KafkaTopic`s next to the Sensor. **Fallback:** `adhar events relay`, a small Deployment of the platform image using the bridge consumer API that `adhar events tail` already speaks (`cmd/events/reach.go`).

### 7.4 Lifecycle signals: notifications and CloudEvents

A recorder in the console BFF (`apps/console/app/server/workflows/recorder.ts`) runs as the console service account, like `events/platform-events.ts`. It holds three watches:

| Watch | Scope | Payload | Emits |
|---|---|---|---|
| Lifecycle | all Workflows, cluster-wide | **metadata only** (`Accept: application/json;as=PartialObjectMetadata;g=meta.k8s.io;v=v1`). Phase comes from the label `workflows.argoproj.io/phase` [S10], so no status bytes are streamed. | `io.adhar.workflow.run.{started,succeeded,failed,errored,stopped}`; failure notifications |
| Approvals | `adhar.io/has-approvals=true,workflows.argoproj.io/completed=false` | full object | `io.adhar.workflow.approval.{requested,decided}`; actionable notifications to eligible approvers |
| Game days | `adhar.io/chaos-gameday` | full object | §8.9 |

- **Why not Kubernetes Events.** Upstream says they "can be lost or rolled up" and must not drive automation [S12]. Labels on a resumable watch can drive it.
- **Event ids.** Deterministic: `sha256(uid + ":" + phase)[0:32]`. Consumers deduplicate across recorder restarts (at-least-once).
- **Bus transport.** `bus.ts` mirrors the Go publisher's contract (`platform/events/publisher.go`): a bounded queue that drops the oldest on overflow, one worker, never blocks a request. It is opt-in through `ADHAR_EVENTS_BRIDGE_URL` on the console Deployment, exactly as on the manager.
- **RBAC.** The `console-event-watcher` ClusterRole (`core/adhar-console/manifests/install.yaml:233+`) gains `argoproj.io/workflows` (get, list, watch) and `platform.adhar.io/gamedays` (get, list, watch). `TestConsoleWatchedResourcesAreGranted` reads the console sources and pins the grant.
- **Replicas.** The console runs one replica (`install.yaml:414`). Before it ever runs more, the recorders must take a Lease (`adhar-console-recorder`) or every event doubles (§12 R-7).

### 7.5 Event taxonomy additions

| Type | Topic | Subject | `data` |
|---|---|---|---|
| `io.adhar.workflow.run.started` / `.succeeded` / `.failed` / `.errored` / `.stopped` | `adhar.workflow` | `<ns>/<workflow>` | `{template, trigger: manual\|cron\|event\|resubmit, submitter, durationSeconds?}` |
| `io.adhar.workflow.approval.requested` / `.decided` | `adhar.workflow` | `<ns>/<workflow>/<nodeId>` | `{question, approvers}` / `{decision, approver}` |
| `io.adhar.workflow.template.published` | `adhar.workflow` | `<repo>:<path>` | `{home, pr?, commit?, publisher}` |
| `io.adhar.resilience.gameday.scheduled` / `.started` / `.aborted` / `.completed` | `adhar.resilience` | `<ns>/<plan>` | `{run, environment, verdict?, reason?}` |
| `io.adhar.resilience.checkpoint.requested` / `.decided` | `adhar.resilience` | `<ns>/<run>/<checkpoint>` | `{question}` / `{decision, by}` |
| `io.adhar.resilience.fault.injected` / `.reverted` | `adhar.resilience` | `<env-ns>/<engine>` | `{scenario, app, by: step\|sweeper\|lever}` |
| `io.adhar.resilience.report.finalized` | `adhar.resilience` | `<ns>/<run>` | `{verdict, score}` |
| `io.adhar.resilience.safetylever.engaged` / `.released` | `adhar.resilience` | `platform` | `{reason, by}` |

- **Declarations.** Add `KafkaTopic`s `adhar.workflow` (3 partitions, `retention.ms: "1209600000"`, 14 days) and `adhar.resilience` (3 partitions, `retention.ms: "7776000000"`, 90 days, because these are evidence) to `data/kafka-operator/manifests/events-bus.yaml`.
- **Go taxonomy.** Append `TopicWorkflow` and `TopicResilience` to `Topics` and add the type constants in `platform/events/taxonomy.go`, even though the console produces them: the taxonomy is the contract. `TestTheEventTopicsAreDeclaredInTheStack` and `TestEveryEventTypeRoutesToADeclaredTopic` then cover them.
- **Docs.** Document both domains in `platform/events/CONTRACT.md`.

---

## 8. Game days

### 8.1 What a game day is, in practice

Current practice agrees across the following sources:
- AWS Well-Architected REL12-BP05 [S36] and the AWS FIS team's own game days [S37]
- AWS FIS stop conditions and safety lever [S38]
- Gremlin [S39]
- Google DiRT and SRE ("big red button") [S40]
- Harness [S45]

The shape they share:
- a **charter**: type, objective, hypotheses, scope, roles, comms
- a **steady state proven before injection**
- **staged escalation**, from low to high blast radius
- **human checkpoints**
- **abort conditions kept separate from hypotheses** (a broken hypothesis is the result; an abort condition is harm starting)
- a **big red button** plus a **platform safety lever**
- **observation of people as well as systems**: expected alarms, time to detect
- a **blameless, scored report** with **tracked action items**
- a **recurring cadence**

The console MVP has the fault sequence and a revert, and none of the rest (G1 §3.3).

### 8.2 Model

```
 Team (own identity) ── console designer | adhar gameday apply | Git automation/gamedays/ ──►  GameDay XR (<project> ns)
        │                                                                                         │ Crossplane: gameday-argo
        │                                     validates targets ∈ project envs, catalogue, rails ▼
        │                                             WorkflowTemplate gameday-<plan>  (+ CronWorkflow if unattended+cron)
        │   start (Owner/Coordinator, or the CronWorkflow) ──────────────────────────────────────► Workflow (run) as adhar-gameday
        │                                          ┌─ guard (daemon): SLO burn, ALERTS, checkpoint timeouts, safety lever, Lease
        │                                          ├─ preflight: lever off, no blackout, chaos lock, steady state for holdFor, targets
        │                                          ├─ stage N: load ∥ fault(s) ─► ChaosEngine (adhar-system, policy-gated) ─► Litmus runner
        │                                          ├─ recover: readiness + steady state restored (seconds)
        │                                          ├─ checkpoint (suspend + intermediate params) ◄── console run room (approve/abort)
        │                                          └─ onExit: revert ─► collect (report JSON output, gameday-results ConfigMap)
 Console recorder ◄── watch (adhar.io/chaos-gameday) ── notifications, Grafana annotations, CloudEvents, frozen report (console DB)
 Manager sweeper (poll 60 s) ── stops stranded faults; enforces the safety lever cluster-wide
```

### 8.3 The `GameDay` XRD (`platform/controlplane/configuration/xrd/gameday.xrd.yaml`)

```yaml
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata: {name: gamedays.platform.adhar.io}
spec:
  group: platform.adhar.io
  scope: Namespaced
  names: {kind: GameDay, plural: gamedays}
  defaultCompositionRef: {name: gameday-argo}
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
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
                  required: [environment, objective, stages]
                  properties:
                    displayName: {type: string, maxLength: 80}
                    type: {type: string, enum: [train-teams, tune-observability, validate-past-outage, exploratory], default: exploratory}
                    objective: {type: string, maxLength: 500}
                    environment: {type: string, enum: [dev, test, prod]}      # resolved via the Project's status.environments
                    mode: {type: string, enum: [attended, unattended], default: attended}
                    dryRun: {type: boolean, default: false}                  # rehearsal: everything but injection
                    participants:
                      type: array
                      maxItems: 50
                      items:
                        type: object
                        required: [role]
                        properties:
                          role: {type: string, enum: [owner, coordinator, scribe, observer, responder, approver]}
                          user: {type: string}
                          group: {type: string}
                          briefed: {type: boolean, default: true}             # false = "operator" not told the plan [S37]
                    comms:
                      type: object
                      properties:
                        channels: {type: array, items: {type: string, enum: [inapp, email, chat]}}
                        announce: {type: array, items: {type: string, pattern: '^[0-9]+[mh]$'}}   # e.g. [24h, 1h]
                        silencePaging: {type: boolean, default: false}       # D-15
                    schedule:
                      type: object
                      properties:
                        at: {type: string, format: date-time}                 # attended: reminder + pre-armed run room
                        window: {type: string, pattern: '^[0-9]+[mh]$'}
                        cron: {type: array, maxItems: 4, items: {type: string}}   # unattended only, non-prod only
                        timezone: {type: string}
                    steadyState:
                      type: object
                      properties:
                        holdFor: {type: string, pattern: '^([0-9]+[smh])+$', default: 5m}
                        slis:
                          type: array
                          maxItems: 10
                          items:
                            type: object
                            required: [name, kind, objective]
                            properties:
                              name: {type: string}
                              app: {type: string}
                              kind: {type: string, enum: [http-availability, http-latency-p95, ready-replicas, promql]}
                              query: {type: string, maxLength: 1000}           # kind: promql; $NS substituted
                              objective: {type: string, pattern: '^(>=|<=|>|<)\s*[0-9.]+(ms|s|%)?$'}
                    abortWhen:
                      type: array
                      maxItems: 10
                      items:
                        type: object
                        properties:
                          sli: {type: string}
                          below: {type: number}
                          above: {type: number}
                          for: {type: string, pattern: '^([0-9]+[smh])+$'}
                          alerts: {type: object, properties: {severity: {type: string, enum: [critical, warning]}}}
                          checkpointTimeout: {type: string, pattern: '^([0-9]+[smh])+$'}
                          errorBudgetConsumed: {type: string, pattern: '^[0-9.]+%$'}
                          deploy: {type: boolean}                               # abort if the target is redeployed
                    load:
                      type: array
                      maxItems: 3
                      items:
                        type: object
                        required: [app, rate]
                        properties:
                          app: {type: string}
                          via: {type: string, enum: [gateway, in-cluster], default: gateway}
                          test: {type: string}                                 # Performance Center test (perf/test.json)
                          rate: {type: string, pattern: '^[0-9]+/s$'}
                          path: {type: string}
                    stages:
                      type: array
                      minItems: 1
                      maxItems: 10
                      items:
                        type: object
                        required: [name, faults]
                        properties:
                          name: {type: string, pattern: '^[a-z0-9-]{1,30}$'}
                          checkpoint:
                            type: object
                            properties:
                              question: {type: string, maxLength: 200}
                              timeout: {type: string, pattern: '^([0-9]+[smh])+$', default: 15m}
                          onHypothesisFailure: {type: string, enum: [stop, ask, continue], default: ask}
                          faults:
                            type: array
                            minItems: 1
                            maxItems: 5                                         # parallel = a compound failure
                            items:
                              type: object
                              required: [scenario, app]
                              properties:
                                scenario: {type: string, pattern: '^[a-z0-9-]{1,63}$'}   # ∈ adhar-gameday-catalogue
                                app: {type: string}                            # adhar.io/application in the environment
                                mode: {type: string, enum: [one, percent, all], default: one}
                                percent: {type: integer, minimum: 1, maximum: 100}
                                duration: {type: string, pattern: '^([0-9]+[sm])+$', default: 60s}  # ≤ 10m (composition)
                                params: {type: object, additionalProperties: {type: string}}
                                probe: {type: object, properties: {path: {type: string}, status: {type: integer}}}
                                expect: {type: object, properties: {recoveryWithin: {type: string}}}
                          expect:
                            type: object
                            properties:
                              alerts:
                                type: array
                                items: {type: object, properties: {name: {type: string}, within: {type: string}}}
                    recovery:
                      type: object
                      properties:
                        between: {type: string, default: 60s}
                        steadyStateFor: {type: string, default: 2m}
                    runbooks: {type: array, items: {type: string}}
                    dashboards: {type: array, items: {type: string}}
                    actionItems:
                      type: object
                      properties:
                        tracker: {type: string, enum: [gitea, plane], default: gitea}
                        repo: {type: string}
            status:
              type: object
              properties:
                template: {type: string}
                cronWorkflow: {type: string}
                targets: {type: array, items: {type: object, x-kubernetes-preserve-unknown-fields: true}}
                validation:
                  type: array
                  items: {type: object, properties: {level: {type: string}, message: {type: string}, path: {type: string}}}
                estimatedDuration: {type: string}
                productionApproved: {type: object, properties: {by: {type: string}, at: {type: string}}}
                lastRun: {type: object, x-kubernetes-preserve-unknown-fields: true}
                ready: {type: boolean}
```

Ships with `examples/gameday.yaml` (indexed in `examples/README.md`). `TestEveryPlatformObjectHasAnExample` and `TestExamplesMatchTheirSchema` require it. The kind name obeys `TestNoXRDKindCarriesTheCompositePrefix`. CLAUDE.md XRD count: 37 → **38**.

### 8.4 Composition, catalogue and the step library

**Composition** `compositions/gameday/argo.yaml` (`gameday-argo`). It uses `function-go-templating` with `ExtraResources`:
- the owning `Project` XR (environment → namespace and tier)
- `Application` XRs labelled `adhar.io/project` (targets)
- ConfigMap `adhar-system/adhar-gameday-catalogue`
- ConfigMap `adhar-system/adhar-chaos-settings`

It emits:

1. **Status**: resolved `targets` (app → namespace, workload kind, selector, Service, route host), `validation[]` (block / confirm / warn, the `chaos-safety.ts` rails moved server-side), `estimatedDuration`, `template`, `ready`.
2. **`WorkflowTemplate gameday-<plan>`**, *only when nothing blocks*. Blocking conditions: unknown scenario, target outside the project's environments, prod without approval or with prod disabled, a fault longer than 10 minutes, a plan longer than `maxRunMinutes`.
3. **`CronWorkflow gameday-<plan>`** when `mode: unattended` and `schedule.cron` is set (non-prod only). It uses `concurrencyPolicy: Forbid`, `startingDeadlineSeconds: 600` and `stopStrategy: {expression: "cronworkflow.failed >= 3"}` [S24].

The `adhar-gameday` service account and its bindings are **per project** (Project composition, §5.6), not per plan. Two plans therefore never manage the same object.

**Catalogue as data.** `application/litmus/manifests/gameday-catalogue.yaml` holds ConfigMap `adhar-gameday-catalogue`. It is one source for four readers:
- the composition (validation)
- `adhar gameday fault` (engine rendering)
- the console palette (hypotheses, knobs, blast)
- the scorer (blast weights)

It is generated from the console's `SCENARIOS`/`CHAOS_FAULTS` as they stand today (16 scenarios; 27 explained faults of the 29 installed). The console then reads the ConfigMap and keeps its constants only as a fallback for older platforms. Each entry looks like this:

```json
{"id": "pod-delete-one", "fault": "pod-delete", "family": "availability", "blast": "low",
 "hypothesis": "Losing one replica causes no user-visible errors; a replacement is Ready within 60 s.",
 "env": {"TOTAL_CHAOS_DURATION": "60", "PODS_AFFECTED_PERC": "0", "FORCE": "false"},
 "minReplicas": 2, "nodeLevel": false, "probe": "http"}
```

**The rendered template.** A sketch; the composition's golden renders pin the exact output.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: gameday-checkout-dependency-failures
  namespace: shop
  labels: {adhar.io/gameday-plan: checkout-dependency-failures, adhar.io/organisation: acme, adhar.io/team: payments,
           adhar.io/project: shop, adhar.io/environment: test, adhar.io/plane: workload}
  annotations: {adhar.io/plan-generation: "7", workflows.argoproj.io/title: "Checkout dependency failures"}
spec:
  serviceAccountName: adhar-gameday
  entrypoint: gameday
  onExit: finalize                          # runs on Stop and on failure; NOT on Terminate [S23]
  activeDeadlineSeconds: 9000               # estimated + margin, ≤ maxRunMinutes
  arguments:
    parameters:
      - {name: plan, value: '<the plan spec as JSON — every run carries its own immutable snapshot>'}
      - {name: dry-run, value: "false"}
  synchronization:
    mutexes: [{name: chaos-shop-test, namespace: adhar-system}]   # one game day per environment, project or platform [S21]
  ttlStrategy: {secondsAfterCompletion: 604800}                    # 7 d live; the archive keeps the rest
  podGC: {strategy: OnPodSuccess, deleteDelayDuration: 60s}
  workflowMetadata:
    labels: {adhar.io/chaos-gameday: checkout-dependency-failures, adhar.io/gameday-plan: checkout-dependency-failures,
             adhar.io/has-approvals: "true"}
  metrics:
    prometheus:                               # custom metrics: names [a-zA-Z0-9_] only (W1 A.8); low cardinality
      - name: gameday_runs
        help: Game day runs by final status
        labels: [{key: plan, value: checkout-dependency-failures}, {key: environment, value: test}, {key: status, value: "{{status}}"}]
        counter: {value: "1"}
        when: "{{status}} != Running"
      - name: gameday_duration_seconds
        help: Game day wall-clock duration
        labels: [{key: plan, value: checkout-dependency-failures}]
        histogram: {value: "{{workflow.duration}}", buckets: [600, 1800, 3600, 7200, 14400]}
  templates:
    - name: gameday
      dag:
        tasks:
          - {name: guard, templateRef: {name: adhar-gameday-steps, template: guard, clusterScope: true}}
          - {name: preflight, depends: guard.Daemoned, templateRef: {name: adhar-gameday-steps, template: preflight, clusterScope: true}}
          - {name: s1-load, depends: preflight.Succeeded, templateRef: {name: adhar-gameday-steps, template: load, clusterScope: true},
             arguments: {parameters: [{name: stage, value: s1}]}}
          - {name: s1-f0, depends: preflight.Succeeded, templateRef: {name: adhar-gameday-steps, template: fault, clusterScope: true},
             arguments: {parameters: [{name: stage, value: s1}, {name: index, value: "0"}]}}
          - {name: s1-recover, depends: s1-f0.Succeeded && s1-load.Succeeded,
             templateRef: {name: adhar-gameday-steps, template: recover, clusterScope: true}}
          - {name: cp2, depends: s1-recover.Succeeded, templateRef: {name: adhar-gameday-steps, template: checkpoint, clusterScope: true},
             arguments: {parameters: [{name: question, value: "Stage 1 held. Proceed to Postgres latency?"}]}}
          - {name: s2-f0, depends: cp2.Succeeded, when: "'{{tasks.cp2.outputs.parameters.decision}}' == 'proceed'",
             templateRef: {name: adhar-gameday-steps, template: fault, clusterScope: true},
             arguments: {parameters: [{name: stage, value: s2}, {name: index, value: "0"}]}}
          # … one task per fault; stage gates compile `onHypothesisFailure` into `when` over the previous
          #   stage's verdict outputs, each read with valueFrom.default so a skipped stage cannot fail v4 [S3]
    - name: finalize
      steps:
        - [{name: revert, templateRef: {name: adhar-gameday-steps, template: revert, clusterScope: true}}]
        - [{name: collect, templateRef: {name: adhar-gameday-steps, template: collect, clusterScope: true}}]
```

**Why fault steps exit 0 on a broken hypothesis.**
- A broken hypothesis is a *result*, not an error. Fault steps succeed with `outputs.parameters.verdict: Fail` and fail only on infrastructure errors. Routing uses `when` on verdicts.
- This avoids `continueOn`, which the CRD forbids alongside `depends` (W1 A.2), and the v4 rule that failed outputs of skipped tasks fail the node [S3].

**Step library** (`adhar-gameday-steps`, image `{{ .PlatformImage }}`, `args: [gameday, <step>, …]`). Code lives in `platform/gameday/` and `cmd/gameday/` (§9). Every step reads the plan snapshot from the workflow parameter, so a plan edited mid-run cannot change the run.

| Step | Does | Outputs | Exit | Resources (req / limit) |
|---|---|---|---|---|
| `preflight` | Refuses before any injection unless all of these hold: safety lever released; no blackout; Argo mutex held; Lease `adhar-chaos-lock` acquired in the target namespace; **no critical alert firing** there (`ALERTS{alertstate="firing",severity="critical",namespace=…}`); each SLI within its objective for `holdFor`; error budget above threshold; targets resolve with ≥ `minReplicas`; faults installed. Records target image digests (to detect deploys). | `preflight` (JSON), `targets` | 0 pass · 1 refused (reason) · 2 error | 10m/48Mi · 128Mi |
| `guard` (daemon) | Every 5 s evaluates `abortWhen`, the safety lever, checkpoint timeouts, deploys to targets (when `deploy: true`) and its own heartbeat Lease `gameday-<run>-guard`. On breach: annotates `adhar.io/abort-reason`, sets `engineState: stop` on the run's engines, then patches `spec.shutdown: Stop`. | — | runs until the DAG ends | 10m/48Mi · 128Mi |
| `fault` | **Refuses unless the guard Lease is fresh** (< 15 s). Renders the ChaosEngine from the catalogue: unique name `<run>-<stage>-<i>-<scenario>`, labels `adhar.io/chaos-gameday`, `gameday-run`, `application`, `project`, `environment`, and a Continuous `httpProbe` or `promProbe` (`sloProbe` needs Harness SRM and is unusable here, G1 §2.5). Creates it in `adhar-system`, waits for the ChaosResult (`Completed*` phases, `Completed_With_Probe_Failure` included), and stops its engine on SIGTERM. | `verdict` Pass/Fail/Error, `probeSuccess`, `engine`, `startedAt`, `endedAt` | 0 on any verdict · 2 on infrastructure error | 10m/48Mi · 128Mi |
| `load` | k6 through the **Gateway** by default (the users' real path; environment NetworkPolicies admit `fromEntities: [ingress]`). `via: in-cluster` creates a k6 Job in the target namespace. Scripts come from the Performance Center test or the runner's generic `load.js`. | `summary` (k6 `ADHAR_K6_SUMMARY`) | 0 · 99 = thresholds failed (a result) | 100m/128Mi · 512Mi |
| `recover` | Measures seconds until readiness **and** steady state hold for `recovery.steadyStateFor`; compares with `expect.recoveryWithin`. | `recoverySeconds`, `restored` | 0 | 10m/48Mi · 128Mi |
| `checkpoint` | `suspend: {}` with intermediate parameters `decision` (enum `proceed,abort`, **default `abort`**) and `note`. The guard enforces the timeout as an abort [S18]. | `decision`, `note`, `approver` | — | none |
| `revert` (exit) | Stops, waits `terminationGracePeriodSeconds`, then deletes the run's engines. Reports leftovers (NetworkPolicies, taints, cordons) Litmus should have removed. Releases the Lease. | `leftovers` | always 0 | 10m/48Mi · 128Mi |
| `collect` (exit) | Gathers ChaosResults (then deletes them, G1 F6), k6 summaries, checkpoint decisions, guard events and timings into a ≤ 64 KiB report. Computes the automatic parts of the score (§8.7). Writes `gameday-results` in the project namespace for the scorer. | `report`, `verdict`, `score` | always 0 | 10m/48Mi · 128Mi |

**Batch.** Game-day control pods use the default scheduler and **never** a batch queue (§10).

### 8.5 Safety architecture

**Identities:**

| Identity | Where | Rights | Used by |
|---|---|---|---|
| `adhar-gameday` (per project, from the Project composition) | `<project>` | `<project>`: executor + get/patch own Workflows, Leases. Each non-prod environment namespace: `view`, Leases, Jobs (in-cluster load). `adhar-system`: Role `adhar-gameday-engines`: chaosengines create/get/list/watch/patch/delete; chaosresults get/list/watch/delete; chaosexperiments get/list; configmaps get `adhar-chaos-settings`, `adhar-gameday-catalogue`. | game-day step pods only (Kyverno #3, #9) |
| `k8s-chaos-admin` | `adhar-system` | the fault runner's cluster rights (unchanged) | the Litmus runner pod only, from **admitted** engines |
| `adhar-chaos-gameday` (existing, MVP, platform game days) | `adhar-system` | **G0: the cluster-wide binding to `k8s-chaos-admin` is removed** (G1 F2), replaced by the `adhar-gameday-engines` Role in adhar-system | console MVP game days, then platform `GameDay`s in adhar-system |

**`adhar-chaosengine-guardrails`** (Kyverno, Enforce, `failurePolicy: Fail`, `background: false`; G1 §4.5). "Platform requesters" means `oidc:platform-admin`, `oidc:platform-engineer`, Argo CD's controller and `system:serviceaccount:adhar-system:adhar-resilience-runner`.

| Rule | Applies to | Why |
|---|---|---|
| exactly one `experiments[]`, and its name is an installed `ChaosExperiment` (`context.apiCall`) | non-platform requesters | catalogue only |
| deny `experiments[].spec.components.{experimentImage, secrets, configMaps}`, `spec.components.runner.image`, `spec.components.sidecar` | non-platform | image or secret injection as `k8s-chaos-admin` |
| deny `cmdProbe/inputs.source` | non-platform | arbitrary pods with arbitrary privileges |
| `chaosServiceAccount == k8s-chaos-admin` | everyone | a fixed runner identity |
| requester `system:serviceaccount:<P>:adhar-gameday`: `appinfo.appns` carries `adhar.io/project` equal to `<P>`'s project label, is not protected, and has `adhar.io/tier != prod` unless `allowProductionGameDays == "true"` **and** the plan's `status.productionApproved` is set | team runs | a team breaks only its own project |
| node-level faults (`node-*`, `kubelet-service-kill`, `docker-service-kill`, `disk-loss`) | non-platform → deny | nodes carry other teams' pods |
| labels `adhar.io/chaos-gameday` or `adhar.io/resilience-test`, plus `adhar.io/application` | everyone | provenance and the scorer join |
| safety lever engaged → deny | non-platform | the lever also closes admission |

**Abort layers.** Each is independent of the others.

| # | Trigger | Mechanism | Latency |
|---|---|---|---|
| 1 Probe | hypothesis probe fails (`stopOnFailure: true` in attended plans) | Litmus stops and reverts that fault; verdict Fail | seconds |
| 2 Guard | `abortWhen` breached, critical alert, budget burn, checkpoint timeout, deploy detected | stop the run's engines, then `shutdown: Stop`, then `revert` → `collect` | ≤ 5 s + revert |
| 3 Human | anyone in the run room with `patch workflows` ("Abort game day", reason required) | same path as the guard (BFF `POST /api/gamedays/runs/:ns/:name/abort`) | seconds |
| 4 Safety lever | platform or on-call engages it (`adhar-chaos-settings.safetyLever=engaged`, reason) | every guard aborts its run; preflight and fault refuse; Kyverno denies new engines; **the manager sweeper stops all active game-day engines and Stops all running game-day Workflows** even if a guard is dead; the runner skips chaos; the console disables Start and shows a banner | ≤ 60 s worst case (sweeper), ≤ 5 s (guards) |
| 5 Deadline + sweeper | `activeDeadlineSeconds`; sweeper every 60 s | Workflow fails into its exit handler (*verify in G0 that `onExit` runs after a deadline on v4.1.6*); sweeper stops any engine labelled `adhar.io/chaos-gameday` whose run is finished or missing, or older than its fault duration + 5 min | minutes |

**Invariants.**
- **No guard, no injection.**
- Every fault is bounded by `TOTAL_CHAOS_DURATION`, which Litmus enforces without Argo.
- **Never Terminate** a game day (the console does not offer it; the CLI refuses it).
- Control pods are tiny and run on the default scheduler.

**Sweeper** (`platform/controllers/chaos/sweeper.go`, a leader-elected `manager.Runnable`):
- It **polls** with a dynamic client and starts no informer, so an absent Litmus CRD cannot kill the manager's cache sync (F-16). Each tick it checks discovery for `chaosengines.litmuschaos.io`; if the CRD is absent it does nothing.
- It emits `io.adhar.resilience.fault.reverted` (`by: sweeper|lever`) through the existing Go publisher, and the metric `adhar_chaos_sweeper_stopped_total`.
- Alert `GameDayStrandedFaultStopped`: `increase(adhar_chaos_sweeper_stopped_total[10m]) > 0`, warning.

**Chaos settings** (`application/litmus/manifests/chaos-settings.yaml`, ConfigMap `adhar-chaos-settings`, adhar-system):

```yaml
data:
  safetyLever: released            # engaged | released
  reason: ""                       # required when engaged
  by: ""                           # set by the console/CLI, never typed
  at: ""
  allowProductionGameDays: "false" # D-14
  maxRunMinutes: "240"
  maxConcurrentGameDays: "3"       # per cluster (preflight counts running game-day Workflows)
  blackouts: "[]"                  # [{"from":"2026-12-20T00:00:00+08:00","to":"2027-01-03T00:00:00+08:00","reason":"freeze"}]
```

**Writes and drift.**
- The data is written through `PUT /api/gamedays/safety` (permission `chaosSafetyLever`; the console service account holds `update` on that one ConfigMap by `resourceNames`) or `adhar gameday safety engage --reason`.
- The ConfigMap is listed under the litmus Application's `ignoreDifferences` (`/data`), so Argo CD does not revert an engaged lever.
- `TestChaosSettingsAreNotSelfHealed` pins that.

**Production game days (D-14).** Off unless `allowProductionGameDays: "true"`. When on, a production plan:
- needs `mode: attended`;
- needs a checkpoint before every stage;
- needs non-empty `abortWhen` SLI floors;
- needs a plan approval: a platform approver with `gamedaysApprovePlan` uses the console approval queue (`workspace.approval`), which sets `status.productionApproved` through the XR's status writer, not the user.

**Paging (D-15).** Real alerts, announced. `comms.silencePaging` creates an Alertmanager silence for **paging receivers only**, for the run window. Detection is still measured from `ALERTS`, which silences do not affect.

### 8.6 Coexisting with the resilience runner and the promotion gate

There is one chaos actor per environment at a time.

| Actor | Behaviour while an environment holds `adhar-chaos-lock` |
|---|---|
| Hourly runner (`resilience-runner.yaml`) | **Skips** the app and records `skipped: game day <run>`. The scorer ignores skipped. |
| Promotion gate (runner in gate mode, Kargo verification) | Waits up to 20 min, then **fails closed** with the reason `environment under game day <run>`. The freight stays unverified until re-verification. It never silently passes and never injects into a game day. |
| Another game day on the same environment | Queued by the Argo mutex `chaos-<env-ns>` |
| A promotion *into* the environment during a game day | Not blocked (Kargo has no lock). Preflight records target digests; the guard records a "deploy during game day" timeline entry and aborts if `abortWhen.deploy: true`. The run room suggests pausing auto-promotion for the stage. |

**Runner RBAC.** The runner needs `get leases` cluster-wide (ClusterRole addition in `adhar-scorecards/manifests/rbac.yaml`).

**Scorer fix (F1).** `scorer-cronjob.yaml:532` selects results whose engine label `adhar.io/chaos-gameday` is present, instead of excluding names prefixed `adhar-chaos-`. From G1 it reads the `gameday-results` ConfigMaps (list by label, which the scorer already may: `rbac.yaml:49-51`). `TestScorerCountsOnlyGameDays` feeds gate results to `score.jq` offline.

### 8.7 State, scoring, report and action items

**State** follows §4.6. Game-day evidence never needs S3:
- `collect` writes the report into its output parameter.
- The console recorder freezes it into `resilience.gameday-report`, adding:
  - Prometheus range metrics for the run window ± 20 min, as AWS FIS does [S37]
  - the `ALERTS` timeline (detection)
  - scribe notes, checkpoint decisions and the timeline
- Barman backs up the console database.

**Verdicts:**
- **Passed**: every hypothesis held and there was no abort.
- **Failed**: a hypothesis broke. This is the most valuable result; the report says so (blameless) [S36].
- **Aborted**: guard, human or lever, with the reason recorded. Completed stages are still scored.
- **Error**: an infrastructure problem; no verdict.

**Score (0–100).** Explainable, with weights re-normalised when a part does not apply (G1 §4.7):

| Part | Weight | Computed by | From |
|---|---|---|---|
| Hypotheses | 40 | collect | Σ blast-weight × held / Σ blast-weight (low 1, medium 2, high 3) |
| Detection | 20 | collect | expected alerts fired within their window (from `ALERTS`); −0.1 per unexpected critical page; N/A when no expectations are declared |
| Recovery | 20 | collect | share of faults recovered within the objective |
| SLO impact | 10 | collect | 1 if the budget consumed ≤ allowance, linear to 0 at 2× |
| Process | 10 | console at finalize | preflight passed; checkpoints decided before timeout; scribe notes present; each failed hypothesis has an action item; finalized within 5 working days |

**Report.** Sections:
1. header (plan@generation, run, environment, targets, window, participants, platform, Litmus and Argo versions)
2. executive summary (auto-drafted, editable)
3. hypotheses × results
4. detection and response
5. steady state and SLO charts (reusing the `chaos-report.ts` charts)
6. timeline
7. checkpoint decisions and aborts
8. retrospective (went well, went poorly, surprises, runbook effectiveness, learnings)
9. action items (synced state)
10. comparison with previous runs
11. appendix (plan snapshot, raw results, SHA-256 of the report JSON)

**Finalize** (Owner) makes the report immutable (later edits are addenda), writes the PDF (`chaos-report-pdf.ts`) and emits `report.finalized`.

**Action items.**
- Created as **Gitea issues** in the owning app's repository (as the user via Sudo; label `gameday`, linking back), or as Plane work items when Plane is enabled (D-16).
- Their state syncs back into the report on a 10-minute timer.
- Overdue items notify their owner.

### 8.8 Console UX for game days

Game days stay at **Deliver → Chaos Engineering → Game days** (`chaos.tsx` tab `gamedays`). The tab becomes four surfaces built from `modules/platform/src/gamedays/`, sharing the Workflows canvas in `staged` mode.

**1. Home**
- **KPIs:** next scheduled, live now, last verdict and score, open action items, **coverage** (apps with a passed game day in the evidence window / all apps).
- **Live now:** cards with a pulsing stage, a "waiting for checkpoint" badge and "Open run room".
- **Upcoming:** a list or month/week calendar, with blackouts shaded and the lever state shown.
- **Plans:** type, environment, last 5 verdicts (sparkline), next run, owner. Actions: Run, Edit, Duplicate, Schedule.
- **History:** live + archive, with filters and "compare any two".
- **Templates** (G3): "Pod resilience basics", "Dependency degradation", "Validate last incident", "Data service failover" (CNPG switchover in the project environment), "Batch restartability" (0025).

**2. Plan designer** (staged canvas)
- **Charter tabs** across the top:
  - Overview
  - Scope & blast radius, with a computed sentence per fault ("1 of 2 payments pods in shop-test")
  - Steady state & SLOs (SLI pickers with **live current values** from Prometheus)
  - Abort conditions
  - Participants & roles
  - Comms
  - Schedule (cron builder, timezone, next five runs, blackouts)
  - Runbooks & dashboards
- **Canvas:** columns are **stages** (time runs left to right), and nodes stacked in a column run in parallel. Edges are generated by order; free-form edges are disabled, so the compile stays deterministic.
- **Palette:** the catalogue, grouped by family with blast badges and hypotheses, plus building blocks: Load, Compound group, Escalation ladder (expands to 1 pod → 50% → majority stages), Checkpoint (diamond), Expectation (alert X within T), Note.
- **Inspector:** target app picker from the project's environments, mode, duration, knobs from the catalogue, probe overrides, `expect`, `onHypothesisFailure`.
- **Validation strip:** client rails now, **server truth** after save (`status.validation`).
- **Preview targets** and read-only YAML of the XR and of the rendered WorkflowTemplate.
- **Save:** applies the XR as the user (home is dev tier), or **Publish** to `automation/gamedays/<name>.yaml` (GitOps). Production plans open an approval request.

**3. Preflight and run room**
- **Preflight checklist:**
  - automated checks, live (lever, blackout, lock, alerts, SLIs holding for mm:ss, budget, targets, faults installed)
  - manual ticks by the Coordinator (participants present, comms sent, runbooks open, rollback understood)
  - Start is enabled when everything passes; an Owner override needs a reason and is audited
- **Run room:**
  - header: `RunHero` (exists), plan, environment, elapsed / planned, current stage, verdict so far, and a large red **Abort game day** button (reason required, Stop semantics)
  - centre: the plan graph in run mode, with probe %, recovery seconds and k6 p95 on each node; clicking a fault opens the existing `ProbePanel`/`TargetPodsPanel`/`LiveMetricsPanel` (`chaos-run-canvas.tsx`)
  - right rail:
    - **checkpoint card**: question, evidence summary, Proceed/Abort, countdown to auto-abort
    - **guard gauges**: each abort condition against its threshold
    - **alerts and detection feed**: "fired 2m14s after injection ✓ expected"
    - participants with presence
  - bottom: the **scribe timeline**: automatic entries plus notes (text, pasted screenshots, tags `surprise`/`runbook-gap`/`action`), with links to Grafana, Loki and Tempo for the window
- **Blind responder mode** (train-teams): responders see the normal console plus a banner, not the plan.

**4. Report and retro.** §8.7, plus one-click "create action item" from any timeline note or failed hypothesis, Finalize, PDF, and Compare.

**Permissions** (as presented; enforced as noted). Participants see actions they cannot take as **disabled with an explanation**, an exception to the hide-don't-disable convention, because a game day is a team exercise and people must know who can act.

| Action | Who | Enforced by |
|---|---|---|
| View plans and runs | project members | Kubernetes RBAC |
| Author or edit a plan | project `admin` (team, org admins), platform tiers | Kubernetes RBAC (aggregate-to-admin for `gamedays`) |
| Start | Owner and Coordinator (UI); `create workflows` in `<project>` (technically) | UI + Kubernetes RBAC |
| Decide a checkpoint | plan approvers / owner (UI + BFF approver policy); `update workflows` | BFF + RBAC; recorded in the node message |
| Abort | any participant | `patch workflows` |
| Node faults, production, safety lever | platform tiers; `chaosSafetyLever` | Kyverno #8 + console permissions |

### 8.9 Notifications, annotations and events

| Transition | Audience | Channels |
|---|---|---|
| Plan published / schedule set | participants | in-app, email (with an `.ics` invite; *verify `mail.ts` attachments*) |
| T−24 h, T−1 h (`comms.announce`) | participants, on-call for the target environment | in-app, email, chat |
| Started | participants, **everyone viewing that environment** (console-wide banner "Game day in progress on shop/test"), on-call | in-app, chat |
| Checkpoint waiting | approvers (actionable card) | in-app, chat |
| Aborted (with reason) / lever engaged | all of the above + the platform channel | all |
| Completed; report draft ready / finalized | participants / project | in-app, email |
| Action item overdue | its owner | in-app, email |

- **Grafana annotations.** One per stage and fault window, tagged `gameday`, `<plan>`, `<run>`, `<app>`, `<env>`, through the console's Grafana tool. Per-app dashboards and the All-in-One dashboard get an annotation query on tag `gameday`.
- **CloudEvents.** As listed in §7.5.

---

## 9. CLI (adhar repo)

The two command trees follow the repository's conventions:
- one package per command (`cmd/workflow/`, `cmd/gameday/`)
- tables through `cmd/helpers/table.go`, icons through `cmd/helpers/icons.go`, the single gutter (`helpers.BannerGutter`)
- `-o json|yaml` for scripting
- no direct writes to the terminal while a live checklist owns it

**Identity and transport:**

| Operations | Transport | Identity |
|---|---|---|
| Kubernetes-object operations | the kubeconfig (`helpers.GetKubeConfig()`) | the kubeconfig's user |
| argo-server-only operations: `retry`, memoized `resubmit`, `approve`/`reject`, `lint`, `archive` | `https://argo-workflows.<host>` | `Authorization: Bearer $(adhar auth token)` (`cmd/auth/token.go`), client mode |

Where the API server rejects that token, the commands exit 3 with "needs a token the API server accepts; use the console for this action".

```
adhar workflow (alias: wf)
  list      [-n NS | -A] [--template T] [--phase P] [--mine] [--archived]
  get       NAME [-n NS] [-o tree|yaml|json]           # tree: the DAG with per-node status (lipgloss)
  watch     NAME [-n NS]                                # live tree (Bubbletea); exits with the run's status code
  submit    --from template/NAME|cluster-template/NAME|cron/NAME [-p key=value]... [--wait|--watch] [-n NS]
  logs      NAME [--node ID|NAME] [-c main|wait|init] [-f]   # pod logs; "pod gone: logs are in Loki (adhar logs …)"
  stop | suspend | resume  NAME
  terminate NAME --yes                                  # refused for game-day runs (INV-8)
  retry     NAME [--restart-successful] [--node-field-selector S]
  resubmit  NAME [--memoized] [-p key=value]...
  approve | reject  NAME --node ID|NAME [--comment C]
  delete    NAME
  lint      -f FILE
  template  list | get NAME | lint -f FILE | publish -f FILE --home app:ORG/REPO|project:NAME [--commit] | history NAME | diff NAME
  cron      list | get NAME | suspend NAME | resume NAME | run-now NAME
  trigger   list | create-webhook NAME --template T [--selector EXPR] [--param name=payload.path]... | token NAME | rotate NAME | test NAME -d @payload.json
  archive   list [-n NS] [--since 7d] | get UID | retry UID | resubmit UID
  wait      --kind Deployment|Rollout|StatefulSet|Job --name N [--for Healthy] [--timeout 10m]   # also the adhar-steps/wait-healthy body

adhar gameday
  plan      list | get NAME | apply -f FILE | validate -f FILE | preview NAME | delete NAME
  run       NAME [--wait]        status RUN        approve RUN --checkpoint ID [--note N] [--abort]
  abort     RUN --reason R       report RUN [-o json|pdf]
  safety    status | engage --reason R | release
  # step commands, hidden from help, run inside workflow pods (docs/GAMEDAYS.md#steps):
  preflight | guard | fault | recover | revert | collect | sweep --once
```

**Shared Go packages.** Each has unit tests; the safety logic is mutation-tested.

| Package | Contents |
|---|---|
| `platform/workflows/` | `gvr.go`, `labels.go` (Argo keys, F-10), `nodes.go` (inflate gzip `compressedNodes`, DAG reconstruction), `tree.go`, `argoserver.go` (bearer client, namespace pinning, no field selectors), `submit.go` (never sets a service account) |
| `platform/gameday/` | `plan.go` (types mirroring the XRD), `catalogue.go`, `engine.go` (ChaosEngine from the catalogue; byte-identical to console fixtures, see §15), `targets.go`, `prom.go` (Prometheus client with timeouts; `ALERTS`, SLI queries), `lock.go` (Lease and mutex naming), `settings.go` (lever, caps, blackouts), `preflight.go`, `guard.go`, `fault.go`, `recover.go`, `revert.go`, `collect.go`, `score.go`, `sweep.go` |

**Deprecations and docs.** `adhar pipeline template list|run` prints a one-line deprecation pointing at `adhar workflow template …` and delegates to it for one release (D-21). `docs/design/cli-command-landscape.md` gains both trees.

---

## 10. Batch alignment: the contract with 0025 / ADR-0028

*Written before 0025 exists, from the batch lane's research (Volcano recommended; `BatchJob` XRD; runs as `batch/v1` Jobs; `adhar.batch` topic). Whoever lands second reconciles this table.*

| Topic | This design provides | 0025 provides or decides |
|---|---|---|
| Orchestration | Argo Workflows DAGs. **A batch job is a step**: palette node "Batch job" → `templateRef: adhar-batch-steps/run` | The `adhar-batch-steps` ClusterWorkflowTemplate body: `adhar batch run <app>/<job> --env E -p … --wait`, mapping Spring Batch exit status to the step's outcome and outputs (`executionId`, `exitStatus`) |
| One DAG engine | Volcano JobFlow is **not** offered; Dagster stays for data-team analytics DAGs | Confirm (batch R3 G4: "do not build a DAG engine; adopt … the opt-in Argo Workflows package") |
| Scheduling | Workflow pods use the default scheduler. A template opts in with `schedulerName: volcano` + `podMetadata.annotations["scheduling.volcano.sh/queue-name"]` (designer toggle when `capabilities.volcano`). Argo-level admission (`parallelism`, `namespaceParallelism`, semaphores) keeps Pending pods out of etcd. | Queue naming per team and environment, Queue capacity, non-preempting batch PriorityClasses, and the Volcano version gate (Kubernetes 1.37 support expected with Volcano v1.17, estimated 2027-01-30; batch R1) |
| Gangs | Never inside Argo. A gang is a BatchJob run (Volcano `Job`, `minAvailable`), launched by the step and watched to completion. This is Volcano's own Argo integration pattern [S32]. | the Volcano Job template, retry policies |
| Schedules | `CronWorkflow` for multi-step flows | single-job schedules on the BatchJob |
| Events | `adhar.workflow` and `adhar.resilience`; the relay forwards `adhar.batch` events to bindings (§7.3); one platform EventBus in the argo-events package | the `adhar.batch` topic and types; BatchJob `dependsOn` Sensors on the shared EventBus |
| Game days | Scenario "kill a worker of a partitioned job mid-run" (hypothesis: restart from the last committed chunk, no duplicates) once targets resolve by `adhar.io/batch-job`. Control pods **never** in a batch queue. | `adhar.io/batch-job` labels on Jobs and Pods; Spring Batch Micrometer metrics usable in `promProbe` |
| Console | The Workflows palette's Batch node; runs that launched a batch run carry `adhar.io/launched-by-workflow: <ns>/<name>` | the Batch area, linking back to the launching run |

---

## 11. Security

### 11.1 Threat model

| # | Threat | Control | Proven by |
|---|---|---|---|
| T1 | Escalation through a workflow's service account (a team names `adhar-resilience-gate`, `adhar-release` or `adhar-gameday`) | Kyverno #3; workflowDefaults → `adhar-workflow`; adhar-system has no default service account (fails closed) | `kyverno test` fixtures; mutation test |
| T2 | Fault injection beyond scope (ChaosEngine overrides, foreign targets, node faults, production) | Kyverno #8; GameDay composition validation; lever | fixtures; G1 live acceptance |
| T3 | argo-server as a confused deputy / archive IDOR (GHSA-q65w-j2vp-47c4 class) | v4.1.6; BFF SSAR as the user; namespace pinned in path and query; `fieldSelector` never forwarded; archived item 404 unless its namespace matches | BFF unit tests; W1 live test (user A cannot read B's archive) |
| T4 | Over-privileged SSO mapping (cluster-admin today) | Argo-scoped `adhar-argo-platform-operator` / `adhar-argo-viewer`; teams unmapped | `TestArgoSSOMapsNoGroupToClusterAdmin` |
| T5 | TLS bypass to Keycloak (`insecureSkipVerify: true` today) | removed; `rootCA` only for self-signed platforms | `TestArgoSSONeverSkipsTLSVerification` |
| T6 | Long-lived tokens | BFF TokenRequest (10 min); SSO token Secrets only for Argo-scoped accounts; webhook tokens namespace-scoped and rotatable | review + tests |
| T7 | A second controller or CRD downgrade (Litmus infrastructure) | Kyverno #1; CONFLICTS.md | fixtures |
| T8 | Tampering with the platform step catalogue (`platform-developer` has `edit` cluster-wide → ClusterWorkflowTemplate create/delete through the aggregates, F-12) | Kyverno #2 (Argo CD only) | fixtures |
| T9 | Bypassing review in test/prod (`kubectl apply` of a template) | Kyverno #6; drafts dev-only (#7) | fixtures; W3 live test |
| T10 | Secrets in parameters or logs (Argo does not mask logs) | Designer: secrets only as `secretKeyRef` env; lint warns on secret-like parameter names; log access is RBAC'd (`get pods/log` before the Loki fallback) | `validate_test.ts`; BFF tests |
| T11 | Shell injection through parameters spliced into `source`/`args` | env passing by default; lint warning | `validate_test.ts` |
| T12 | Supply chain | images pinned by tag; `restrict-image-registries-enforce` applies to workload namespaces (quay.io, ghcr.io and docker.io are allowed; the scarf host the MVP uses is **not**, so G0 moves to `docker.io/litmuschaos/…`); the platform image is cosign-signed (ADR-0019) | registry policy fixtures |
| T13 | Cross-tenant event leakage | the relay routes by `adharproject`; tenants own no Sensors | relay test (W4) |
| T14 | Webhook token theft | namespace-scoped, templates-only, rotate in one click; optional Gateway IP allow-list per route (W5) | — |
| T15 | Resource exhaustion (fan-out bombs, cron storms) | `parallelism` / `namespaceParallelism` / `resourceRateLimit`, quotas, deadline cap (Kyverno #5), 200-task mirror, `startingDeadlineSeconds`, `Forbid` default | config guard test |
| T16 | Forged or self-approved approvals | approver and message derived on the server; separation of duties (`forbidSelfApproval` vs the Kyverno-stamped `adhar.io/submitted-by`); audit | BFF tests |
| T17 | Provenance spoofing (`submitted-by` set by the client) | Kyverno #4 overwrites from `request.userInfo` (trusts only `adhar-console-argo`) | fixtures |

### 11.2 Pre-existing issues found while designing (not caused by this design)

| ID | Issue | Recommendation | Phase |
|---|---|---|---|
| **S-1** | Team admins can run pods as `adhar-resilience-gate` in their project namespace (Kubernetes `admin` allows any service account in the namespace [S35]). That account may `create jobs` in adhar-system with **any** pod template and therefore any adhar-system service account (`resilience-runner.yaml:845-861`, `project/local.yaml:452-468`). Today this is effectively cluster-admin for every team admin. | Kyverno `adhar-reserved-serviceaccount-workloads`: <ol><li>in workload namespaces, Pods and pod-template controllers naming a reserved service account are admitted only from their sanctioned creator (`adhar-resilience-gate`: Jobs from `system:serviceaccount:adhar-system:kargo-controller`; `adhar-gameday`: Pods owned by a Workflow, created by the Argo controller `system:serviceaccount:adhar-system:argo`; `adhar-release`: Jobs from Crossplane);</li><li>in adhar-system, Jobs created by any `adhar-resilience-gate` must use pod service account `adhar-resilience-runner` and the runner image.</li></ol>Audit in W1, Enforce in W3 after a soak (it guards the promotion gate). | W1 → W3 |
| S-2 | `platform-developer` is `edit` cluster-wide, which reads Secrets and runs pods as any service account in adhar-system | Out of scope here; decide in an ADR (narrow to `view` plus per-namespace `edit`). This design does not widen it. | — |
| S-3 | The Kafka bridge is unauthenticated in-cluster: any pod can publish a forged platform event | Consumers must not treat bus events as authority. The relay only starts templates the target namespace already owns. Recommend a bridge NetworkPolicy admitting known producers. | — |
| S-4 | Prometheus is readable from every pod (no tenancy) | Note only. Game-day guard and preflight queries are scoped by namespace label by convention, not enforcement. | — |

---

## 12. Failure modes

| # | Failure | Detection | Behaviour / mitigation |
|---|---|---|---|
| FM-1 | Controller down or crashlooping | `ArgoWorkflowsControllerDown`; capability probe; Engine page | Running pods continue. No new nodes, no `shutdown` processing. **Game days: guards stop engines directly** and Litmus bounds every fault without Argo. The sweeper stops leftovers. The console shows "engine not processing". |
| FM-2 | Archive DB unreachable at controller start | crashloop; `ArgoWorkflowsArchiveDatabaseDown` | Upstream: if migration fails the controller does not start [S13]. Runbook: restore CNPG, or break-glass `persistence.archive: false` through Git. Waves order DB → controller. `reconnectionConfig` covers transient loss while running [S9]. |
| FM-3 | argo-server down | readiness; probe; BFF 502/503 | Gateway paths keep working (list, watch, submit, stop, suspend, resume, live logs). Archive, retry, approvals, lint and artifacts show "argo-server unavailable". Approvals wait; their timeout defaults to reject. |
| FM-4 | Control-plane memory pressure (the live master was at 96–108%) | node memory alert; §13 measurement | Minimal CRDs; Argo CD exclusions; TTL, retention, parallelism; metadata-only console watch. Stop-the-line thresholds in §13; rollback = disable the element (INV-11 keeps data). |
| FM-5 | Kyverno unavailable | Kyverno health | #1, #3 and #8 are `failurePolicy: Fail`: Argo and ChaosEngine creates are refused (fail closed). Others `Ignore`. The console explains a webhook-unavailable error. |
| FM-6 | Gitea down | publish errors | Drafts persist in the console DB. Already-synced templates still run. |
| FM-7 | Argo CD reverts a console patch (a suspend toggle on a GitOps-managed CronWorkflow) | drift | In test/prod tiers the toggle commits instead of patching (§7.1); the UI says which. |
| FM-8 | Workflow object exceeds etcd's request limit | controller error | gzip node compression [S25]; designer warns above 100 tasks or wide fan-outs; offload stays off by design (D-9). |
| FM-9 | Cron storm after downtime | queue metrics | `startingDeadlineSeconds: 300`, `concurrencyPolicy: Forbid` by default |
| FM-10 | Webhook queue full | argo-server 503 [S19] | senders retry; the Engine page shows rejected events |
| FM-11 | Relay duplicates or lag | Sensor metrics | at-least-once by design; `x-adhar-event-id` idempotency key (§7.3) |
| FM-12 | Console restarts mid-run | — | Recorders resume from the API server; CloudEvent ids are deterministic. A notification may repeat once. |
| FM-13 | Guard pod evicted | the fault step sees a stale Lease | no new injection; running faults are bounded; the sweeper stops leftovers; the run room shows "guard lost" |
| FM-14 | Litmus operator down mid-run | operator health | the experiment pod reverts at the end of `TOTAL_CHAOS_DURATION` by itself; `revert` and the sweeper retry the stop |
| FM-15 | Project deleted while plans or runs exist | — | Composite deletion removes templates; labelled engines are swept; reports remain in the console DB |
| FM-16 | Clock skew | — | Lease freshness uses 3× the interval; the API server's `renewTime` is authoritative |
| FM-17 | An invalid template admitted (minimal CRDs) | `SpecError` at submit; the CronWorkflow goes Degraded in Argo CD [S29] | lint before publish (§6.7); the error shows in the run view |
| FM-18 | Package switched off with live data | Application deleted | INV-11 keeps CRDs, the DB and the Bucket; purge is a separate, explicit runbook |
| FM-19 | Console scaled above 1 replica | duplicate events and notifications | R-7: recorders must take a Lease first |

---

## 13. Capacity

### 13.1 Footprint (requests / limits)

| Component | CPU req | Mem req | Mem limit | Where | Phase |
|---|---|---|---|---|---|
| workflow-controller | 100m | 256Mi | 1Gi | worker | W0 |
| argo-server | 50m | 128Mi | 512Mi | worker | W0 |
| Archive DB (CNPG, 1 instance, `adhar-node-bound`) | 50m | 192Mi | 512Mi | worker (10 Gi PVC) | W0 |
| Per step pod: `wait` (+ short-lived `init`) | 10m | 64Mi | 256Mi | worker | — |
| HTTP/plugin agent pod (per workflow using them) | 10m | 48Mi | 128Mi | worker | W2 |
| Relay EventSource + Sensor | 20m | 96Mi | 256Mi | worker | W4 |
| Game-day control pods (guard, preflight, fault, recover, revert, collect) | 10m each | 48Mi each | 128Mi | worker | G1 |
| k6 load step | 100m | 128Mi | 512Mi | worker | G1 |

**W0 adds about 0.58 GiB of requests on workers.** The live cluster had about 34 GiB of requestable memory left (R1 §2).

### 13.2 Control plane, the binding constraint

**Additions:**
- 8 **minimal** CRDs (a few KiB; the full ones were 4.84 MiB of JSON, F-7)
- Workflow objects (TTL 1 day/3 days; retention 300/100/100)
- one WorkflowTaskResult per step pod (deleted with its Workflow)
- step pods (until pod clean-up)
- a few Events per run (node events off)

**Watches:**
- the controller
- the console's **metadata-only** lifecycle watch plus two label-scoped watches
- Argo CD sees none of the run kinds (D-10)

**Measurement (W0 gate, repeated each phase).** Take a baseline, then measure 1 h idle after enable, then under a synthetic load of 50 concurrent workflows × 5 steps for 30 min:
- `max_over_time(container_memory_working_set_bytes{namespace="kube-system",container="kube-apiserver"}[1h])`
- `apiserver_storage_objects{resource=~"workflows.argoproj.io|workflowtaskresults.argoproj.io|pods|events"}`
- etcd DB size
- `rate(apiserver_request_total{group="argoproj.io"}[5m])`
- control-plane node memory

**Stop-the-line:**

| Condition | Threshold | Action |
|---|---|---|
| Precondition for W0 | master memory ≤ 85% sustained for 24 h | the CPM lane's fixes land, or the control plane is resized to ≥ 16 GiB; until then do not enable |
| API server RSS delta | > 150 MiB idle, or > 300 MiB under load | lower TTL, retention or parallelism, or roll back |
| Live Workflow objects | > 500 | — |
| Events | grow > 10% | — |

### 13.3 Scale ceilings and knobs

| Need | Knob | Note |
|---|---|---|
| More concurrent runs | `parallelism`, `namespaceParallelism`, namespace label | raise with the measurement above |
| Faster scheduling | controller `--workflow-workers`, `--qps/--burst` | "can increase the load on the Kubernetes API server" [S9] |
| Pod creation bursts | `resourceRateLimit` | protects the API server and scheduler |
| Long history | `archiveTTL`, DB `storageSize` | alert at 70% |
| Big DAGs | stay under 200 tasks (CEL; mirrored client-side); split into sub-flows | the designer warns at 100 |

### 13.4 Local profile (if opted in, D-2)

About 0.6 GiB of requests (controller, server, a small CNPG), minimal CRDs, no relay, no game days (Litmus is not `localSafe`). W2 measures `adhar up` wall time three times with and three times without; **flip local on only if Δ ≤ 5%** and the 15-app local core still converges inside `--apps-timeout`.

---

## 14. Phased implementation plan

Each phase ships on its own, is gated by the console's capability probe, and is accepted on a live cluster. Phases run in this order:

```
W0 ─┬─► W1 ─► W2 ─► W3 ─► W4 ─► W5
    └─► G0 ───► G1 (needs W1) ─► G2 (needs W2) ─► G3 (needs W4, 0025)
```

**W0 + G0 together are the first release.**

**Rules for the implementing agents:**
- Never commit or push. The user releases, and **lists every path you touched** (memory `agent-edits-swept-into-user-releases`).
- Mutation-test every new guard: revert the fix and watch the test fail.
- Read `git status -- platform/stack/` before any `adhar upgrade` (memory `a-push-carries-the-whole-tree`).
- Live checks are read-only unless the phase's acceptance step says otherwise, and need a Ready cluster.

### W0: Enable the engine safely (adhar, plus one console fix)

**Preconditions**
- A Ready cluster.
- Control-plane memory ≤ 85% for 24 h (§13.2).
- A baseline measurement captured.

**Work packages**

| WP | Repo | Files | Change |
|---|---|---|---|
| W0-1 | adhar | `platform/stack/packages/application/argo-workflows/{generate-manifests.sh, adhar-package.yaml, README.md}`, `manifests/{base,platform,platform-tasks}/**`; delete `manifests/dev/**` | §5.1–§5.5, §5.7 (`platform-tasks` only), §5.10; policies #1 and #2 (§5.8); `localSafe: true`; v4.1.6; minimal CRDs; `Delete=false,Prune=false` on CRDs, Database and Bucket |
| W0-2 | adhar | `hack/argocd/values.yaml`, `hack/argocd/values-ha.yaml`, `platform/controllers/adharplatform/resources/argocd/{install,install-ha}.yaml` | exclusions and health checks (§5.9), regenerated with `hack/argocd/generate-manifests.sh`, not hand-edited |
| W0-3 | adhar | `security/keycloak/manifests/keycloak-config.yaml` | `argo-workflows` client: `directAccessGrantsEnabled: false` |
| W0-4 | adhar | `observability/kube-prometheus/dash_delivery.py` → regenerate `manifests/dashboard-delivery-ci.yaml`; `platform/controllers/adharplatform/dashboards_test.go` (`dashboardCoverage["delivery-ci"]` += `argo-workflows`) | the Argo Workflows row |
| W0-5 | adhar | `platform/stack/adhar-appset-{production,local}.yaml`, `platform/stack/environments/{prod,local,dev,test}/config.yaml`, `CLAUDE.md`, `platform/stack/packages/CONFLICTS.md`, `platform/controllers/adharplatform/sso_click_free_test.go` | §5.13 enablement (production on, local off), counts 76→77, the Litmus row, click-free entry |
| W0-6 | adhar | delete `platform/controlplane/configuration/compositions/pipeline/argo-workflows.yaml`, delete `platform/stack/packages/application/adhar-templates/argo-workflows/`; in `core/adhar-console/manifests/install.yaml` delete `console-argo-workflows` (ClusterRole and Binding) and `ARGO_WORKFLOWS_URL` | §5.12. The composition removal is a controller release. |
| W0-7 | adhar | `Makefile` (`policy-test`: pinned kyverno CLI), `platform/stack/packages/application/argo-workflows/policy-tests/{single-controller,cwt-platform-only}/` | fixtures that allow and deny |
| W0-8 | adhar | new guard tests (§15.1, W0 rows) | — |
| W0-C1 | adhar-console | `packages/builder/src/lib/argo-export.ts` (+ test) | emit `schedules: [ … ]` (v4) |

**Acceptance (live, production profile)**
1. The `argo-workflows` Application is Synced + Healthy. The 8 `argoproj.io` workflow CRDs are the minimal ones (`x-kubernetes-preserve-unknown-fields`). No `PriorityClass/workflow-controller` exists.
2. Every platform CronWorkflow runs once (manually created from its `workflowSpec`) and succeeds. Finished runs appear in `GET /api/v1/archived-workflows?namespace=adhar-system`.
3. From an existing Keycloak session, a `platform-admin` opens `https://argo-workflows.<host>` and lands in the UI with **no click**. A `platform-viewer` is read-only. A `ws-team-*` user is denied.
4. `kubectl auth can-i '*' '*' --as=system:serviceaccount:adhar-system:argo-sso-platform` → **no**.
5. Kyverno denies a `Deployment` with image `litmuschaos/workflow-controller:v3.3.1`, and denies a ClusterWorkflowTemplate created by a `platform-developer` (dry-run requests).
6. Prometheus scrapes the controller (`up == 1`). The dashboard row has data. The rules are loaded.
7. §13.2 measurements are within budget, recorded in the PR description.
8. `make test` and `make policy-test` are green, and every new guard is mutation-tested.

**Rollback.** Disable the element. CRDs, the DB and the bucket remain (INV-11).

### G0: Game days, the existing MVP made safe (adhar + console)

| WP | Repo | Files | Change |
|---|---|---|---|
| G0-1 | adhar | `application/litmus/manifests/gameday-rbac.yaml` | Remove the `adhar-chaos-gameday` → `k8s-chaos-admin` **ClusterRoleBinding**. Add Role/RoleBinding `adhar-gameday-engines` in adhar-system (§8.5). |
| G0-2 | adhar | `application/litmus/manifests/{policies.yaml, chaos-settings.yaml}`, `policy-tests/chaosengine/`; ApplicationSet `ignoreDifferences` for `ConfigMap/adhar-chaos-settings` `.data` in both appsets | Kyverno #8 (catalogue, overrides, labels, lever, node faults; the tenancy rule waits for G1), the settings ConfigMap |
| G0-3 | adhar | `application/litmus/{values.yaml, generate-manifests.sh}`, `manifests/{core,experiments}.yaml` | Execution plane 3.31.0 → **3.33.0** and hub branch `v3.33.x`, **after** confirming the Docker Hub tags and branch exist (G1 F5) and re-running the runner suite and gate live |
| G0-4 | adhar | `application/adhar-scorecards/manifests/scorer-cronjob.yaml` (~l.532) + `scorecard_resilience_test.go` | F1: select by the `adhar.io/chaos-gameday` label (`TestScorerCountsOnlyGameDays`) |
| G0-C1 | adhar-console | `modules/platform/src/data/chaos-scenarios.ts` (+ test) | Every step on `docker.io/litmuschaos/k8s:3.33.0`. The scenario step is a kubectl verdict gate instead of `litmus-checker`, and the revert is bounded (D-22). `buildGameDay` sets all three `ttlStrategy` fields to 604800 (D-23) and keeps `podGC`. |
| G0-C2 | adhar-console | `modules/platform/src/data/chaos.ts:378-381` | Correct the comment claiming ChaosCenter can read console game days (unverified, W1 D.5) |

**Acceptance (live)**
- A two-scenario MVP game day on a test app completes, the revert runs, and no engine remains.
- `kubectl auth can-i create pods/exec -A --as=system:serviceaccount:adhar-system:adhar-chaos-gameday` → **no**.
- Kyverno denies a ChaosEngine with `experimentImage`, a `secrets` mount or a `cmdProbe` source from a non-platform identity.
- The scorer's `chaos_gameday` no longer counts `adhar-gate-chaos-*` (live summary).
- **Verified and documented:** whether `onExit` runs after `activeDeadlineSeconds` on v4.1.6 (§17 Q1).

### W1: Runs, catalogue and history in the console; tenant identity (adhar + console)

| WP | Repo | Files | Change |
|---|---|---|---|
| W1-1 | adhar | `controlplane/configuration/compositions/env/local.yaml`, `xrd/env.xrd.yaml` (param `workflowParallelism`), `compositions/project/local.yaml`; render tests (`renderGoTemplate`) | Executor SA and bindings, parallelism label, CRD-gated through `ExtraResources` (§5.6). **Controller release.** |
| W1-2 | adhar | `argo-workflows/manifests/platform/{rbac.yaml, policies.yaml}`, `policy-tests/*` | executor, launcher, watch aggregation, catalogue reader; Kyverno #3 (Enforce), #4 (mutate), #5 (Audit); S-1 policy (Audit) |
| W1-3 | adhar | `core/adhar-console/manifests/{install.yaml, console-rbac.yaml}` | `console-event-watcher` += `argoproj.io/workflows`; SA `adhar-console-argo` + ClusterRole; console SA `create serviceaccounts/token` (resourceNames); env `ARGO_SERVER_URL`; virtual `workflows-admin` |
| W1-4 | adhar | `cmd/workflow/**`, `platform/workflows/**`, `cmd/pipeline/template.go` (deprecation), `cmd/root.go` | `adhar workflow` (list, get, watch, submit, logs, stop, suspend, resume, terminate, retry, resubmit, approve, reject, delete, archive, cron list/suspend/resume) |
| W1-C1 | adhar-console | `packages/shell-ui/src/nav-tree.tsx` (+ `icons.tsx` `IconWorkflow`), `modules/platform/src/home.tsx`, delete `views/xr-kinds.tsx` `WorkflowsView`, `modules/platform/src/workflows/{area.tsx, data/*}` | nav, section router, capability gate |
| W1-C2 | adhar-console | `workflows/views/{overview,catalog,run-form,runs,run-view,history,engine}.tsx`, `canvas/workflow-canvas.tsx` (run mode), `components/flow-css.ts` | §6.9 pages except designer, schedules and triggers |
| W1-C3 | adhar-console | `apps/console/app/server/workflows/{handler,argo-server,authz,logs,model}.ts`, `apps/console/server.ts` | §6.4 routes: capabilities, runs (retry, resubmit, approve, logs, artifacts, hydrated), archive, lint, engine |
| W1-C4 | adhar-console | `apps/console/app/server/workflows/recorder.ts` | lifecycle (metadata) and approvals watches → notifications (CloudEvents arrive in W4) |
| W1-C5 | adhar-console | `packages/auth/src/console-permissions.ts`, `docs/rbac.md` | `workflowsAdmin` |

**Acceptance (live)**
1. A team member (`ws-team-<org>.<team>`) sees their namespaces' runs **live** (watch, not 60 s polling). They submit a template with parameters, stop it, retry it and resubmit it. A pending approval in a hand-written suspend template is approved through the console with its message and approver recorded; self-approval is refused when `forbidSelfApproval`.
2. Archive: user A cannot read B's namespace (BFF 403/404). A crafted `fieldSelector` is dropped (unit and live).
3. Logs of a cleaned-up pod come from Loki, labelled as such.
4. A Workflow naming `adhar-resilience-gate` is **denied**. Every run has `adhar.io/submitted-by`.
5. The Overview chunk does not contain `@xyflow` (bundle analysis in CI).
6. CLI parity: the same actions through `adhar workflow`.

### W2: The designer and test runs (console + adhar)

| WP | Repo | Files | Change |
|---|---|---|---|
| W2-1 | adhar | `platform/controllers/adharplatform/controller.go` (`stackTemplateData.PlatformImage()` + plumbing at the `stageStack` call sites), `argo-workflows/manifests/catalog/adhar-steps.yaml.tmpl`, `cmd/workflow/wait.go` | the platform step catalogue (§5.7) |
| W2-2 | adhar | `argo-workflows/manifests/platform/{policies.yaml, cleanup.yaml}` | Kyverno #7 + `ClusterCleanupPolicy adhar-workflow-draft-expiry` + cleanup-controller aggregate role |
| W2-3 | adhar | — (measurement) | the D-2 local gate (§13.4); flipping local on is a separate, user-approved change |
| W2-C1 | adhar-console | `modules/platform/deno.json` (+ app imports): `yaml@2.9.1`; `workflows/model/**` + `corpus/` | §6.5–§6.7 model, ops, validation, layout; corpus round trip |
| W2-C2 | adhar-console | `workflows/canvas/**`, `workflows/views/{designer,test-run-dialog}.tsx` | §6.8 editing UI: palette, inspector, YAML panel, keyboard, accessibility |
| W2-C3 | adhar-console | `apps/console/app/server/workflows/handler.ts` (`drafts/:ns/test-run`), `workflows/data/drafts.ts` | test runs; `workflows.draft` autosave |
| W2-C4 | adhar-console | `views/graph-canvas.tsx` | use the shared `ensureFlowCss()`; no visual change (screenshot check) |

**Acceptance**
1. **Round trip:** 100% of the corpus opens and re-saves JSON-equal with comments kept, and no-op saves produce no diff.
2. A 6-node DAG (container, script, HTTP, approval, template-ref, fan-out) is built **by drag and drop only**, validates, test-runs in dev and appears in the run view.
3. Every mirrored CEL rule has a failing fixture.
4. An approval that times out takes **reject** on v4.1.6, or the fallback is implemented and documented (§17 Q2).
5. An expired draft is cleaned by Kyverno.
6. A 3-node flow can be created keyboard-only.
7. A 200-node synthetic DAG pans at ≥ 50 fps on the reference laptop.

### W3: GitOps publishing, versions and review (adhar + console + templates)

| WP | Repo | Files | Change |
|---|---|---|---|
| W3-1 | adhar | `compositions/project/local.yaml` (Argo CD Application `<project>-automation`, path `automation/`, recurse, `*.yaml`; repo-creation Job seeds `automation/README.md`); render tests; `examples/project.yaml` note | the project home (§4.5). **Controller release.** |
| W3-2 | adhar | `argo-workflows/manifests/platform/policies.yaml` | #5 → Enforce; #6 Enforce; S-1 → Enforce after a two-week Audit soak with zero violations |
| W3-3 | adhar | `cmd/workflow/template_publish.go`, `template_history.go`, `template_diff.go` | CLI publish, history and diff |
| W3-C1 | adhar-console | `apps/console/app/server/workflows/publish.ts` (+ test), `workflows/views/publish-dialog.tsx`, history panel | §6.4 publish and history |
| W3-T1 | adhar-templates | `templates/springboot-web-service/skeleton/deploy/base/workflows/{kustomization.yaml, db-migration-check.yaml}` and the same for `python-web-service`; template READMEs | a golden-path example of app workflows (kept in the template repo, memory `templates-stay-in-template-repo`) |

**Acceptance (live)**
1. Publish opens a PR **authored by the user** in the app repository. On merge, Argo CD syncs the template into dev, and the next promotion brings it to test (verify that `app-ci` builds on `deploy/**` changes, §17 Q5).
2. A team admin's `kubectl apply` of a WorkflowTemplate into a test namespace is **denied**.
3. A project publish lands in `<project>` through `<project>-automation`.
4. Re-saving unchanged creates no commit.
5. Reverting through a PR restores the old template.

### W4: Schedules, triggers and events (adhar + console)

| WP | Repo | Files | Change |
|---|---|---|---|
| W4-1 | adhar | spike notes in the PR; `application/argo-events/manifests/eventbus.yaml` (+ KafkaTopics); `argo-workflows/manifests/triggers/{eventsource,sensor,rbac,kafkatopics}.yaml`; appsets and environment configs gain element `argo-workflows-triggers` (production on, local off); CLAUDE.md (105 elements) | §7.3. If the spike fails: `cmd/events/relay.go` plus a `Deployment` instead of the Sensor. |
| W4-2 | adhar | `platform/events/{taxonomy.go, CONTRACT.md}`, `data/kafka-operator/manifests/events-bus.yaml` | §7.5 topics and types |
| W4-3 | adhar | `cmd/workflow/{cron,trigger}.go` | CLI |
| W4-C1 | adhar-console | `workflows/views/schedules.tsx`, the designer's schedule tab, `data/{schedules,cron}.ts` | §7.1 |
| W4-C2 | adhar-console | `workflows/views/triggers.tsx`, webhook routes in `handler.ts` | §7.2 |
| W4-C3 | adhar-console | `apps/console/app/server/bus.ts` (+ test), `recorder.ts` CloudEvents | §7.4 |

**Acceptance (live)**
1. A Gitea push webhook (with an `Authorization` header) starts the bound template in dev, and a forced 503 is retried.
2. A platform `package.synced` event for project X starts X's binding and never Y's.
3. `adhar events tail` shows `io.adhar.workflow.run.failed` once, with the same id after a console restart.
4. A cron with `stopStrategy` stops after 3 failures.

### W5: Enterprise scale and batch (each item is its own small PR)

| Item | Repo | Acceptance |
|---|---|---|
| Volcano scheduling toggle + queue picker; Batch-job node with 0025's ClusterWorkflowTemplate | console + adhar (+ 0025) | a 3-step pipeline with a gang BatchJob step completes; control pods stay on the default scheduler |
| DB-backed semaphores and mutexes (`synchronization.enableAPI`) with a UI under Engine | adhar + console | a semaphore limits two templates across namespaces |
| Memoization (per-namespace cache ConfigMaps + Role) | adhar + console | a memoized step is reused on resubmit |
| Team artifact repositories once **scoped RustFS credentials** exist (extend the `Bucket` composition: a scoped user and policy per bucket; the Environment composition emits `artifact-repositories`) | adhar | a team step uploads an artifact that another team cannot read |
| Authenticated argo-server metrics scrape | adhar | the API latency panel has data |
| Optional: native UI for teams through SSO namespace delegation (`SSO_DELEGATE_RBAC_TO_NAMESPACE`) | adhar | a team user sees only their namespaces in the Argo UI |
| Workflow SLOs per template (alerts from `argo_workflows_workflowtemplate_runtime` / `_triggered_total`) | adhar + console | a failing template pages its owners |
| Evaluate init-less pods (`initlessPod.enabled`, beta in 4.1) and full CRDs against the §13 budget | adhar | measured decision recorded |

### G1: Game-day plans and runs (adhar + console; needs W1)

| WP | Repo | Files | Change |
|---|---|---|---|
| G1-1 | adhar | `controlplane/configuration/xrd/gameday.xrd.yaml`, `compositions/gameday/argo.yaml`, `rbac/local-compose-rbac.yaml` (+ `argoproj.io` kinds), `examples/gameday.yaml` + `examples/README.md`, Argo CD health `platform.adhar.io_GameDay`; golden render tests | §8.3–§8.4. **Controller release.** |
| G1-2 | adhar | `compositions/project/local.yaml` | `adhar-gameday` SA and bindings (§5.6, §8.5) |
| G1-3 | adhar | `platform/gameday/**`, `cmd/gameday/**` | step commands + user commands (plan, run, status, approve, abort, safety) |
| G1-4 | adhar | `argo-workflows/manifests/catalog/adhar-gameday-steps.yaml.tmpl`, `application/litmus/manifests/gameday-catalogue.yaml` | step library and catalogue (one-time port of the console's `SCENARIOS`/`CHAOS_FAULTS`; this file becomes the source of truth) |
| G1-5 | adhar | `application/litmus/manifests/policies.yaml` | #8 tenancy rule, #9 |
| G1-6 | adhar | `platform/controllers/chaos/sweeper.go` (+ test), manager wiring, RBAC, `rules.yaml` | the sweeper and lever enforcement (§8.5) |
| G1-7 | adhar | `application/adhar-scorecards/manifests/{resilience-runner.yaml, scorer-cronjob.yaml, rbac.yaml}` | runner and gate honour the chaos lock; the scorer reads `gameday-results` |
| G1-C1 | adhar-console | `modules/platform/src/gamedays/{data,views}/**`, `views/chaos-gameday.tsx` (becomes a router) | home, plans, "quick game day" = a one-stage GameDay XR (`buildGameDay` retired for runs) |
| G1-C2 | adhar-console | `gamedays/views/run-room.tsx`; `apps/console/app/server/gamedays/{handler,safety}.ts` | run room v1: graph, checkpoint card, abort, lever banner and `PUT /api/gamedays/safety` |
| G1-C3 | adhar-console | `apps/console/app/server/gamedays/{recorder,report-model}.ts` | the frozen report (`resilience.gameday-report`) and notifications |

**Acceptance (live)**
1. A 2-stage plan with a checkpoint completes, and the report is frozen.
2. **A forced SLO breach aborts within the guard interval plus revert.** Engines are stopped and the report states the reason.
3. Engaging the lever mid-run aborts every run, blocks Start, and makes Kyverno deny new engines.
4. Deleting the guard pod mid-run means no new fault is injected, and the sweeper stops leftovers within 60 s of the run's end.
5. A runner tick skips the locked app, and the gate fails closed with the reason.
6. A cross-project target is rejected by the composition **and** a direct ChaosEngine as `adhar-gameday` is rejected by Kyverno.
7. A production plan without approval renders no template.

### G2: Designer, collaboration and the report (console + adhar; needs W2)

**Console:**
- the staged canvas mode, charter tabs and preflight checklist
- run room v2: guard gauges, alerts and detection feed, scribe timeline, participants, blind responder mode
- report and retro, action items (Gitea issues / Plane), Finalize, PDF, Compare, Grafana annotations
- the Workflows palette's **Chaos experiment** node

**adhar:**
- `adhar-steps/run-gameday` and `adhar gameday run --wait --parent`
- `adhar.resilience` events from the recorder (the topic exists from W4; if G2 lands first, W4-2's topic work moves here)

**Acceptance**
- The plan → staged graph → plan round trip is lossless (unit).
- An action item created from a note links both ways.
- A finalized report is immutable.
- The PDF renders.
- Grafana shows annotations on the app dashboard for the run window.

### G3: The game-day program (needs W4, 0025)

Scope:
- calendar and recurring programs
- a coverage dashboard and trends
- the templates library
- **AI-drafted plans**: adhar-ai drafts a GameDay from an incident or postmortem through agentgateway, and **never runs it**
- dependency failover scenarios (CNPG switchover in the project's environment)
- batch scenarios (0025)
- production opt-in (D-14)
- full CLI parity tests
- the ChaosCenter retirement decision (D-17)

**Acceptance**
- A monthly unattended program runs for a month without intervention.
- Coverage is visible per team.
- The console and CLI produce the same rendered template and report for one plan (parity test).

### 14.1 Release order across repositories

| Step | Release | Depends on | Degrades gracefully when absent |
|---|---|---|---|
| 1 | **adhar** W0 + G0 (platform release; controller release for the composition deletion) | Ready cluster, §13 precondition | the console needs nothing new |
| 2 | **adhar** W1 (controller release: Environment/Project compositions; stack: RBAC, policies, console grants) | 1 | — |
| 3 | **adhar-console** W1 | 2 (watch aggregation, `console-event-watcher`, `adhar-console-argo`, `ARGO_SERVER_URL`) | missing grants → "platform ≥ vX.Y needed" banners; polling fallback |
| 4 | adhar W2 → console W2 | the stack step catalogue before the palette uses it | palette hides "Platform steps" |
| 5 | adhar W3 (controller release) → console W3 → templates W3 | — | publish to a project home hidden until `<project>-automation` exists |
| 6 | adhar W4 → console W4 | Kafka + argo-events in the profile | Triggers → "Platform events" tab hidden |
| 7 | adhar G1 (controller release) → console G1; G2 after W2 | — | game days fall back to the G0 MVP |

---

## 15. Test strategy

### 15.1 New guards in the adhar repo

Each guard is a Go test unless stated, and each is **mutation-tested**.

| Test | Asserts | Phase |
|---|---|---|
| `TestArgoWorkflowsIsPinnedToAPatchedRelease` | generator, contract and file headers agree, and the version is ≥ v4.1.4 (F-1) | W0 |
| `TestArgoWorkflowsShipsMinimalCRDsThatSurviveDisable` | the 8 CRDs are minimal and carry `Delete=false,Prune=false` (D-3, INV-11) | W0 |
| `TestNoStackPriorityClassPreemptsNodePinnedWorkloads` | no stack PriorityClass above 0 with `PreemptLowerPriority` except the allow-listed `adhar-node-bound` and `adhar-critical-path`; generalises `TestPrometheusNeverPreemptsNodePinnedWorkloads` | W0 |
| `TestArgoWorkflowsContainersAreSizedAndHaveNoServiceLinks` | controller and server requests/limits; `enableServiceLinks: false`; `podSpecPatch` in workflowDefaults | W0 |
| `TestArgoWorkflowDefaultsBoundEveryRun` | the ConfigMap holds ttlStrategy, podGC, activeDeadlineSeconds, default SA, retentionPolicy, parallelism and namespaceParallelism | W0 |
| `TestArgoMetricsScrapeMatchesTheControllerScheme` | PodMonitor scheme ↔ `metricsConfig.secure` | W0 |
| `TestArgoSSOMapsNoGroupToClusterAdmin`, `TestArgoSSONeverSkipsTLSVerification` | §5.4 | W0 |
| `TestArgoCDDoesNotTrackArgoWorkflowRuns` | exclusions in both values files and both generated manifests | W0 |
| `TestArgoWorkflowsHasNoControllerWideArtifactRepository` | D-12 | W0 |
| `TestOnlyOneWorkflowControllerCanExist` | policy #1 matches the litmus images and the CRD names | W0 |
| `TestArgoPoliciesAddNoPolicyReports` | every Argo and game-day ClusterPolicy has `background: false` | W0 |
| `TestTheGameDayServiceAccountIsNotClusterWide` | no ClusterRoleBinding gives `k8s-chaos-admin` to anything but the runner path | G0 |
| `TestScorerCountsOnlyGameDays` | `score.jq` offline fixtures: gate results are not game days | G0 |
| `TestTenantNamespacesGetAnExecutorOnlyWhenArgoIsInstalled` | env/project render with and without the CRD in `ExtraResources` | W1 |
| `TestConsoleWatchedResourcesAreGranted` (extended) | the console's new watches are in `console-event-watcher` | W1 |
| `TestGameDayCompositionRendersOnlyWholeObjects`, `TestGameDayRefusesForeignTargets`, `TestGameDayNeverRendersProductionWithoutApproval`, `TestGameDayTemplatesUseOnlyTheStepLibrary` | golden renders (`renderGoTemplate`) | G1 |
| `TestChaosSettingsAreNotSelfHealed` | `ignoreDifferences` covers `adhar-chaos-settings` `.data` in both appsets | G0 |
| `TestTheEventTopicsAreDeclaredInTheStack` (extended automatically) | `adhar.workflow` and `adhar.resilience` | W4 |
| Go unit tests in `platform/gameday` | the guard aborts on each breach type; fault refuses without a fresh guard Lease; preflight refuses (lever, blackout, firing critical alert, steady state broken, too few replicas); revert is idempotent; score maths; sweeper stops orphans and enforces the lever | G1 |
| `make policy-test` (kyverno CLI) | allow and deny fixtures for #1–#9 and S-1 | W0+ |

### 15.2 Console tests

All are Deno `*_test.ts` beside pure modules, following the existing convention.

| Area | Tests |
|---|---|
| `model/` | corpus round trip (JSON-equal + comments); projection determinism; every op and its inverse; `depends` parser (precedence, parentheses, `.Succeeded` etc.); `dependencies` → `depends` migration; rename rewrites `{{tasks.x}}` tokens only on token boundaries; layout determinism (same input → same coordinates); positions codec |
| `validate.ts` | one failing fixture per mirrored CEL rule and v4 rule; platform hints |
| `data/` | `inflateNodes` (gzip fixture from a real v4.1.6 Workflow); phase → StatusKind; fan-out grouping; cron next-fire times across DST in IANA zones; approval eligibility |
| BFF | the authz table (every route → exactly one SSAR); argo-server query allow-list (`fieldSelector` dropped, `namespace` forced); archived-item namespace mismatch → 404; `SubmitOpts.serviceAccount` stripped; approver policy and separation of duties; publish (CI-path refusal, home path rules, no-op detection, stale `baseSha` → 409); recorder (deterministic ids, transition-only); `bus.ts` (bounded queue, drop-oldest) |
| Game days | plan validation mirrors the composition; the ChaosEngine builder produces **byte-identical JSON to `platform/gameday/engine.go`** for shared fixtures (`testdata/gameday-engines/*.json` vendored in both repos; CI in each repo compares against its copy, and a release check diffs the two copies) |

### 15.3 End-to-end and live

- **`tests/e2e/workflows`** (opt-in `ADHAR_E2E_WORKFLOWS=1`, against a cluster with the package enabled): submit a platform task, find it in the archive, retry a failing fixture template, and assert the policies' denials.
- **Live checklist** per phase: the acceptance lists above. They go into the closure lane's `LIVE-PENDING.md` style checklist while no cluster is Ready.
- **Performance:** the 200-node designer frame-rate harness; recorder watch load (API server `apiserver_request_total` for the console service account before and after W1).

---

## 16. Documentation deliverables

| Repo | Document | Content | Phase |
|---|---|---|---|
| adhar | `docs/WORKFLOWS.md` (new) | concepts; the three homes; identity (`adhar-workflow`, reserved accounts); drafts; triggers; limits; troubleshooting; **purge runbook** | W1–W4 |
| adhar | `docs/GAMEDAYS.md` (new) | practice; plan schema; safety layers and the lever; steps (`#steps`); scoring; runbook for a stranded fault and an engaged lever | G1–G3 |
| adhar | `docs/TROUBLESHOOTING.md`, `docs/PRODUCTION.md`, `docs/USER_GUIDE.md` | controller and archive failures; sizing and backup/restore of the archive; CLI | W0–W4 |
| adhar | `platform/stack/packages/application/argo-workflows/README.md`, `CONFLICTS.md`, `platform/events/CONTRACT.md`, `docs/design/cli-command-landscape.md`, `examples/gameday.yaml` + `examples/README.md`, CLAUDE.md | operator notes; the Litmus row; event domains; CLI rows; example; counts and notes | per phase |
| adhar | **ADR-0029** + a `docs/design/README.md` row (§0.2) | the decision record for this design | before W1 |
| adhar-console | `docs/phases/platform.md`, `docs/phases/deliver.md`, `docs/rbac.md`, `docs/guides/workflows-designer.md` (new), `CHANGELOG.md` | module pages; game days; permissions; designer guide | W1–G2 |
| adhar-templates | template READMEs | `deploy/base/workflows/` | W3 |

---

## 17. Open questions and things to verify

| # | Question | How / when | Default if unresolved |
|---|---|---|---|
| Q1 | Does `onExit` run after a workflow's `activeDeadlineSeconds` on v4.1.6? (Docs confirm Stop yes, Terminate no [S23].) | G0 live test | the sweeper covers it; the guard enforces its own deadline earlier |
| Q2 | Does an expired `suspend.duration` resume intermediate parameters with their **defaults** (approval → reject, checkpoint → abort)? | W2 live test | the guard (checkpoints) and a sibling timer task that Stops (approvals) |
| Q3 | Kafka EventBus on `adhar-kafka` and `dest: http.url` parameterization on Argo Events v1.9.11 | W4 spike | the Go relay `adhar events relay` |
| Q4 | Exact minimal-CRD file names at `v4.1.6/manifests/base/crds/minimal` | W0 regeneration | — |
| Q5 | Does `app-ci` build on `deploy/**`-only commits, so workflow changes ride freight? | W3 | document "a workflow-only change needs a CI run" |
| Q6 | Is `Delete=false` honoured for CRDs on the platform's Argo CD version when an Application is deleted? | W0 canary (disable and re-enable) | `adhar.io/keep` plus documentation |
| Q7 | Can team members open PRs on project repositories through `Sudo` (repository and team permissions)? | W3 | publish to app repos only until fixed |
| Q8 | RustFS per-user policies through `rc admin` (scoped credentials) | W5 | no team artifact repositories |
| Q9 | Can `mail.ts` attach an `.ics` invite? | G2 | a link to the calendar view |
| Q10 | Do the Litmus 3.33.0 execution-plane images and hub branch exist, and does the runner suite pass? | G0 | stay on 3.31.0 (images are still pinned) |
| Q11 | Kyverno admission latency for large Workflow specs with list JMESPath | W1 | simplify the rule to top-level fields plus Argo's own `templateReferencing` for game days |
| Q12 | Does the controller set `workflows.argoproj.io/workflow-template` on API-created Workflows? | W1 | the console and CLI stamp it (they do anyway) |
| **User** | D-2 (local profile), D-14 (production game days), D-15 (paging), D-16 (action items), D-17 (ChaosCenter) | before G1/G3 | as recommended in §3 |

---

## 18. Drift and verification log

These were checked in the tree on 2026-11-04. Repository HEADs:
- adhar `6035928`, clean
- adhar-console `9d9877c`, with an unrelated untracked Hubble `flows/` service-map lane; **do not name anything "flows"** in this module
- adhar-templates and adhar-ui read-only

Verified:
- The package and overlay content (§2.1); `APP_VERSION="v4.1.2"`; the header miscalls it a namespace install.
- Both ApplicationSets and all four environment configurations disable `argo-workflows` with the same `manifests/dev` path.
- `CONFLICTS.md:28` kubeflow row.
- Keycloak client lines 158–182.
- `core/adhar-console/manifests/install.yaml` lines 10, 63–67 and 863; replicas 1 at line 414.
- `console-rbac.yaml` virtual-verb model.
- Project composition RBAC, quota and LimitRange (lines 140–260) and the gate SA (lines 411–468).
- Environment composition NetworkPolicies (lines 171–232).
- `Database`/`Bucket` XRD fields; the `Bucket` composition's `root-creds` reference (F-13).
- `local-compose-rbac.yaml` (no `argoproj.io` yet).
- The event taxonomy and topic guard (`platform/events/{taxonomy.go,topics_declared_test.go}`).
- Argo CD values and generated manifests (Tekton exclusion; `resources-finalizer`; `prune: true`).
- The guard tests named in §15.1 that already exist (`parity_test.go:70,126`, `argocd_health_test.go:33,187`, `dashboards_test.go:200-312`, `resource_footprint_test.go:20-69`, `stack_invariants_test.go:526,674`, `node_bound_priority_test.go`, `sso_click_free_test.go:31`, `console_event_watch_rbac_test.go:147`).
- Console:
  - `home.tsx` sections (lines 53, 313–318, 409–506)
  - `nav-tree.tsx` (2-level rule, role sets at lines 93–101)
  - `chaos.ts:387-510`
  - `chaos-scenarios.ts:317-454`
  - `graph-canvas.tsx:52-68`
  - `flow-canvas.tsx:192-222`
  - `tekton.tsx:2026-2050`
  - `api-handlers.ts:37` (`resilience.*` reserved)
  - `access.ts:355`, `store.ts:240`, `gateway.ts:103,198-301`
  - `scorecard-fix.ts`, `gitea-sudo.ts`
  - `manifest-yaml.ts` (no YAML parser by policy)
  - `@xyflow/react ^12.12.0` in `modules/platform/deno.json`
- The deleted designer at `cbc4c8d^` (wf-model 569 lines, wf-designer 1,072, workflow-list 1,530).

**Drift and contradictions recorded:**
1. CLAUDE.md says Argo CD calls a kind with no health check **Healthy** on creation. `argocd_health_test.go:14-31` says such kinds read **Unknown** and block waves. This design adds explicit health checks for every kind it introduces, so it is correct under either behaviour. The contradiction should be settled in CLAUDE.md separately.
2. The argo-workflows contract shows `verified / v0.1.23 / 2026-09-23` for v4.1.2. It must be re-verified by `adhar stack verify --write` after W0, never by hand.
3. `chaos.ts:378-381` claims console game days are "also readable" in ChaosCenter. That is unverified, and no chaos infrastructure exists (fixed in G0-C2).
4. The design README numbering convention (design N ↔ ADR N) is broken by this file's requested number (§0.2).
5. The live cluster is unreachable as of this writing. Every live number is from 2026-11-03.
6. **G0 build (2026-11-05):** `litmus-checker` never reads the verdict. Its `main.go` and `pkg/chaos-checker/check.go` are identical at test-tools 3.31.0 and 3.33.0, and both exit 0 on `engineStatus` `completed` or `stopped`. The MVP's claim that a game day "stops at the first failed hypothesis" was therefore false. Fixed by D-22.
7. **G0 build:** G0-C1's `ttlStrategy: {secondsAfterCompletion: 604800}` would give 1 day / 3 days under §5.2's defaults, not 7 days. Fixed by D-23.
8. **§17 Q1, from source:** v4.1.6 `workflow/controller/workflowpod.go` builds an exit-handler pod without the workflow deadline, "so they still run if the deadline has passed". So `onExit` should run after `activeDeadlineSeconds`. The G0 live check still has to confirm it. Because of this exemption, the MVP `revert` carries its own deadline.
9. **W0-C1:** the console's `workflowToArgo` has had no caller since `123babe` (2026-10-17) deleted `packages/builder/src/lib/project-export.ts`. The fix keeps the exported library correct (v4 `schedules`, task names that do not start with a digit, names that do not end with a hyphen), but nothing in the UI exports Argo YAML today.
10. **Finding for W0-1 (§5.2), not yet acted on:** the `workflowDefaults` TTL sets only `secondsAfterSuccess` and `secondsAfterFailure`. In v4.1.6, `WorkflowStatus.Failed()` is true only for phase `Failed`, so a Workflow that ends in **`Error`** matches neither field. With no `secondsAfterCompletion`, it is never TTL-deleted, and only `retentionPolicy.errored` bounds it. That contradicts INV-5 ("every run has a TTL"). Adding `secondsAfterCompletion` (for example 259200) to the defaults closes the gap. Game days set all three fields (D-23).

---

## 19. Sources

External sources. "Accessed" is the date the fact was read, by this design or by its research notes.

- [S1] Argo Workflows releases (v4.1.6 "Latest", 2026-10-22; v4.1.2 2026-08-21). https://github.com/argoproj/argo-workflows/releases (accessed 2026-11-03)
- [S2] Argo Workflows CHANGELOG (v4.1.4 security fixes; v4.1.5 `sslMode`; v4.1.6 TRACEPARENT; v4.0-rc2 cluster-wide ClusterWorkflowTemplate RBAC check). https://github.com/argoproj/argo-workflows/blob/main/CHANGELOG.md (accessed 2026-11-03)
- [S3] Argo Workflows, Upgrading guide: v4.0 full CRDs by default, "Existing installations using the minimal CRDs will continue to work…"; removed `schedule`/`mutex`/`semaphore`/`podPriority`; v4.0.7 skipped-output rule; v4.1.2 SSO cookie. https://argo-workflows.readthedocs.io/en/latest/upgrading/ (accessed 2026-11-04)
- [S4] GHSA-vm9v-p987-xm32 (artifact path traversal, High 8.8, fixed 4.1.4). https://github.com/argoproj/argo-workflows/security/advisories/GHSA-vm9v-p987-xm32 (accessed 2026-11-03)
- [S5] GHSA-q65w-j2vp-47c4 (cross-namespace IDOR in archived workflows, fixed 4.1.4). https://github.com/argoproj/argo-workflows/security/advisories/GHSA-q65w-j2vp-47c4 (accessed 2026-11-03)
- [S6] Argo Server auth modes. https://argo-workflows.readthedocs.io/en/latest/argo-server-auth-mode/ (accessed 2026-11-03)
- [S7] Argo Server SSO and SSO RBAC (rules, precedence, "if no rule matches, we deny", token Secrets, `filterGroupsRegex`). https://argo-workflows.readthedocs.io/en/latest/argo-server-sso/ (accessed 2026-11-03)
- [S8] argo-server OpenAPI at v4.1.6 (paths and request types for retry, set, resume, submit, archived workflows, events, lint, artifact-files). https://github.com/argoproj/argo-workflows/blob/v4.1.6/api/openapi-spec/swagger.json (accessed 2026-11-03; local copy read 2026-11-04)
- [S9] Argo Workflows `config/config.go` and `config/sso.go` at v4.1.6 (all controller keys used in §5.2; `rootCA`; `reconnectionConfig`); `argo server` flags; scaling (qps/burst warning). https://github.com/argoproj/argo-workflows/tree/v4.1.6/config · https://argo-workflows.readthedocs.io/en/latest/scaling/ (accessed 2026-11-03)
- [S10] Argo Workflows `workflow/common/common.go` at v4.1.6 (labels `phase`, `completed`, `workflow-template`, `cluster-workflow-template`, `cron-workflow`, `workflow-event-binding`, `resubmitted-from-workflow`, `creator*`, `actor*`). https://github.com/argoproj/argo-workflows/blob/v4.1.6/workflow/common/common.go (accessed 2026-11-04)
- [S11] Argo Workflows, Parallelism (`parallelism`, `namespaceParallelism`, namespace label `workflows.argoproj.io/parallelism-limit`). https://argo-workflows.readthedocs.io/en/latest/parallelism/ (accessed 2026-11-04)
- [S12] Argo Workflows, Workflow events (reasons; do not use Kubernetes events for automation: they can be lost or rolled up). https://argo-workflows.readthedocs.io/en/latest/workflow-events/ (accessed 2026-11-04)
- [S13] Argo Workflows, Workflow archive (Postgres; `archiveTTL`; controller does not start if migration fails; access only through argo-server). https://argo-workflows.readthedocs.io/en/latest/workflow-archive/ (accessed 2026-11-03)
- [S14] Argo Workflows, Configuring an artifact repository (`addressingStyle`; `artifact-repositories` ConfigMap; resolution order). https://argo-workflows.readthedocs.io/en/latest/configure-artifact-repository/ (accessed 2026-11-03)
- [S15] Argo Workflows, Security ("no restriction on which service account in a namespace may be used"; pods run as root by default). https://argo-workflows.readthedocs.io/en/latest/security/ (accessed 2026-11-03)
- [S16] Argo Workflows, Workflow RBAC (executor minimum: `workflowtaskresults` create/patch). https://argo-workflows.readthedocs.io/en/latest/workflow-rbac/ (accessed 2026-11-03)
- [S17] Argo Workflows, Workflow restrictions (`templateReferencing: Strict|Secure`). https://argo-workflows.readthedocs.io/en/latest/workflow-restrictions/ (accessed 2026-11-03)
- [S18] Argo Workflows, Intermediate parameters (suspend inputs with enum/default; `valueFrom.supplied`). https://argo-workflows.readthedocs.io/en/latest/intermediate-inputs/ (accessed 2026-11-03)
- [S19] Argo Workflows, Events (`WorkflowEventBinding`; `POST /api/v1/events/{ns}/{discriminator}`; asynchronous, 503 when the queue is full). https://argo-workflows.readthedocs.io/en/latest/events/ (accessed 2026-11-03)
- [S20] Argo Workflows, HTTP template (agent pod; idempotency; agent RBAC). https://argo-workflows.readthedocs.io/en/latest/http-template/ (accessed 2026-11-03)
- [S21] Argo Workflows, Synchronization (lists of mutexes and semaphores, database locks); `workflow/sync/lock_name.go` at v4.1.6 (namespaced mutex names). https://argo-workflows.readthedocs.io/en/latest/synchronization/ · https://github.com/argoproj/argo-workflows/blob/v4.1.6/workflow/sync/lock_name.go (accessed 2026-11-03)
- [S22] Argo Workflows, Metrics and telemetry configuration (`metricsConfig.secure` defaults to true; metric names; non-leaders serve empty metrics; custom metric naming). https://argo-workflows.readthedocs.io/en/latest/metrics/ · https://argo-workflows.readthedocs.io/en/latest/telemetry-configuration/ (accessed 2026-11-03)
- [S23] Argo CLI `argo stop` ("still run exit handlers") and `argo terminate` ("do not run any exit handlers"). https://argo-workflows.readthedocs.io/en/latest/cli/argo_stop/ · https://argo-workflows.readthedocs.io/en/latest/cli/argo_terminate/ (accessed 2026-11-03)
- [S24] Argo Workflows, CronWorkflows (`schedules`, `timezone`, `concurrencyPolicy`, `startingDeadlineSeconds`, `stopStrategy`). https://argo-workflows.readthedocs.io/en/latest/cron-workflows/ (accessed 2026-11-03)
- [S25] Argo Workflows, Offloading large workflows (node status compression; offload through argo-server only). https://argo-workflows.readthedocs.io/en/latest/offloading-large-workflows/ (accessed 2026-11-03)
- [S26] Argo Events, Kafka EventBus. https://argoproj.github.io/argo-events/eventbus/kafka/ (accessed 2026-11-03)
- [S27] Argo Events, Trigger parameterization (template-level parameters that modify the trigger template). https://argoproj.github.io/argo-events/tutorials/02-parameterization/ (accessed 2026-11-04)
- [S28] Argo Events, Argo Workflow trigger. https://argoproj.github.io/argo-events/sensors/triggers/argo-workflow/ (accessed 2026-11-03)
- [S29] Argo CD built-in CronWorkflow health check. https://github.com/argoproj/argo-cd/blob/master/resource_customizations/argoproj.io/CronWorkflow/health.lua (accessed 2026-11-03)
- [S30] Litmus chaos-infrastructure manifests at 3.33.0 (`cluster/1a_argo_crds.yaml`, `1c_argo_deployment.yaml`: `workflow-controller`, `workflow-controller-configmap`, Argo v3.3.1). https://github.com/litmuschaos/litmus/tree/3.33.0/chaoscenter/graphql/server/manifests (accessed 2026-11-03)
- [S31] Docker Hub tags for `litmuschaos/{litmus-checker,k8s,chaos-operator,chaos-runner,go-runner}` (3.33.0 pushed 2026-10-27) and `workflow-controller`/`argoexec` (newest v3.3.1, 2023-07-21). https://hub.docker.com/v2/repositories/litmuschaos/litmus-checker/tags (and siblings) (accessed 2026-11-03)
- [S32] Volcano, VolcanoJob concept, Argo integration example. https://volcano.sh/docs/Concepts/VolcanoJob (accessed 2026-11-03)
- [S33] Volcano v1.16.0 release notes (2026-10-14). https://github.com/volcano-sh/volcano/releases/tag/v1.16.0 (accessed 2026-11-03)
- [S34] Kubernetes blog, "Kubernetes v1.37: Advancing Workload-Aware Scheduling" (2026-09-08). https://kubernetes.io/blog/2026/09/08/kubernetes-v1-37-advancing-workload-aware-scheduling/ (accessed 2026-11-03)
- [S35] Kubernetes RBAC, user-facing roles (`edit`/`admin` allow "running Pods as any ServiceAccount in the namespace"). https://kubernetes.io/docs/reference/access-authn-authz/rbac/#user-facing-roles (accessed 2026-11-03)
- [S36] AWS Well-Architected, REL12-BP05 "Conduct game days regularly". https://docs.aws.amazon.com/wellarchitected/latest/reliability-pillar/rel_testing_resiliency_game_days_resiliency.html (accessed 2026-11-03)
- [S37] AWS Cloud Operations Blog, "Learn from AWS Fault Injection Service team's approach to Game Days" (2025-07-31); AWS FIS experiment report configuration. https://aws.amazon.com/blogs/mt/learn-from-aws-fault-injection-service-team-approach-to-game-days/ · https://docs.aws.amazon.com/fis/latest/userguide/experiment-report-configuration.html (accessed 2026-11-03)
- [S38] AWS FIS stop conditions and safety levers. https://docs.aws.amazon.com/fis/latest/userguide/stop-conditions.html · https://docs.aws.amazon.com/fis/latest/userguide/safety-lever.html (accessed 2026-11-03)
- [S39] Gremlin, GameDays (roles, cadence) and Health Checks. https://www.gremlin.com/docs/fault-injection-gamedays · https://www.gremlin.com/docs/platform-health-checks (accessed 2026-11-03)
- [S40] Google SRE, "Lessons Learned from Twenty Years of Site Reliability Engineering" (big red button); Google Cloud blog on DiRT (2020-06-11). https://sre.google/resources/practices-and-processes/twenty-years-of-sre-lessons-learned/ · https://cloud.google.com/blog/products/management-tools/sre-principles-in-practice-for-business-continuity (accessed 2026-11-03)
- [S41] npm registry, `@xyflow/react` (12.12.0 2026-09-24; 12.12.1 2026-11-02; next 13.0.0-next.0). https://registry.npmjs.org/@xyflow/react (accessed 2026-11-04)
- [S42] npm registry, `yaml` (2.9.1 published 2026-09-11). https://registry.npmjs.org/yaml (accessed 2026-11-04)
- [S43] Gitea webhook `Authorization` header (Gitea 1.19, go-gitea/gitea PR #20926). https://github.com/go-gitea/gitea/pull/20926 (accessed 2026-11-04)
- [S44] LitmusChaos, Resilience probes (`stopOnFailure`, `promProbe`, `sloProbe` requirements). https://docs.litmuschaos.io/docs/concepts/probes (accessed 2026-11-03)
- [S45] Harness Chaos Engineering, GameDay introduction (objectives, approvals, prerequisites). https://developer.harness.io/docs/chaos-engineering/features/gameday/introduction-to-gameday/ (accessed 2026-11-03)
