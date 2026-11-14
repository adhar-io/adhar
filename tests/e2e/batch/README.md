# tests/e2e/batch — batch on Kind, natively and on Volcano

The end-to-end suite of design 0025 (§17, task E2E-1.1). It scaffolds a Spring Batch job from
the platform's template, waits for `app-ci` to build it, and then runs it, interrupts it, stops
and resumes it, restarts it, refuses it, and watches the clock run it.

| Subtest of `Test_BatchOnKind` | Criterion | What it proves |
|---|---|---|
| scaffold | A1.7 | `adhar application deploy --template springboot-batch-*` registers a `type: batch` Application (CLI-1.2). The first PipelineRun succeeds, and the `BatchJob` is Ready in `dev` and `test` |
| a manual run completes | A1.7, D1, D2 | `adhar batch run --wait -o json` is named `<app>-<hash8>` by the suite's own D1 encoder, ends `Succeeded`, and has a §5.13 summary and one execution. Running it again creates nothing |
| invalid parameters | A1.9 | The CLI refuses a bad date and no Job exists. A run built from `ConfigMap <app>-run` with a bad `ADHAR_BATCH_PARAMS` is admitted by run-integrity, exits 2 and fails the Job by `PodFailurePolicy` with one pod. Its summary is `invalid`/`parameters` (D13) |
| two runs of one identity | A1.2 | Two Jobs with the same parameters (different names) give one execution: one completes and the other is `skipped` with a D13 reason. A later manual run is skipped as `already-complete` |
| deleted / evicted pod | A1.1, F2 | The pod is taken away after a commit, and the run still ends with ONE completed execution of the same instance. The committed writes over all executions equal the input. The interrupted execution ends STOPPED (graceful stop, §5.9). The Job's failure count is what the template's own `podFailurePolicy` gives: one for a delete, none for an eviction (DisruptionTarget) |
| SIGKILLed pod | A1.1, F1 | The batch container's process is SIGKILLed from its Kind node, with no SIGTERM, as the OOM killer does. Exactly once as above, but the interrupted execution ends FAILED, because the replacement had to `recover` it (§5.5). The killed container exits 137 with no summary, and the Job counts one failure |
| stop, then resume | A1.3 | `adhar batch stop` leaves the execution STOPPED within the grace period. D2 then reports `Stopped` with exit 4. `resume` completes it exactly once |
| restart | E2E-1.1 | A poisoned row fails the run (exit 5, `BackoffLimitExceeded`). After the row is healed, `adhar batch restart` runs `<run>-r1`, which completes the same instance exactly once |
| the clock | A1.7, A1.2 | In `test`, where the scaffold's `*/2` schedule is live, two firings of one business date give one execution, and a manual re-run of that date is skipped |

"No duplicate rows" is read from what Spring Batch committed (`repository.go`), because the
sample writer only logs. A chunk's `WRITE_COUNT` is committed in the same transaction as its
writes, so the counts of every execution of one instance add up to the input exactly when
nothing was written twice or lost. The interrupted runs read `public.batch_input`, which the
suite seeds in the app's own CNPG database (`source=database`).

## Running it

```bash
make build
# against a running local platform (no up, no down):
ADHAR_E2E_BATCH=1 ADHAR_E2E_SKIP_UP=1 ADHAR_E2E_KEEP=1 \
  go test -v -p 1 -timeout 180m --tags=e2e ./tests/e2e/batch/...
# iterate on one app without scaffolding again:
ADHAR_E2E_BATCH=1 ADHAR_E2E_SKIP_UP=1 ADHAR_E2E_BATCH_APP=batch-e2e-xyz ADHAR_E2E_BATCH_REUSE=1 ...
# the Volcano canary alone, against a running platform that has Volcano enabled:
ADHAR_E2E_VOLCANO=1 ADHAR_E2E_SKIP_UP=1 ADHAR_E2E_KEEP=1 \
  go test -v -p 1 -timeout 240m --tags=e2e -run Test_BatchOnVolcano ./tests/e2e/batch/...
# A2.9 from scratch (Kind v1.37 + Volcano, both suites): make e2e-batch-volcano
```

The suite is **opt-in** (`ADHAR_E2E_BATCH=1`), and every knob is listed in `config.go` (the
canary's in `volcano_config.go`). Without `ADHAR_E2E_SKIP_UP` it runs `adhar up --recreate` itself, and `adhar down` afterwards unless
`ADHAR_E2E_KEEP` is set. It recreates the local `adhar` cluster. The suite's clients use the
`kind-adhar` context, but the CLI uses the current one. The suite therefore refuses to start
unless both resolve to the same cluster: run `kubectl config use-context kind-adhar` first.

The untagged files hold the contracts the suites check against: naming v1, summary v1, the
run builder, the ledger, the knobs and the local profile, and for Phase 2 (`volcano_*.go`) the
staged stack, the leaf join, Volcano's objects and scheduler configuration, the room arithmetic,
the eviction audit, the §4.5 run-template shapes and the partition ledger. They have unit tests,
run by `go test ./tests/...`, and every guard was mutation-tested.

## Wiring: `make` and CI

`make e2e` runs `./tests/e2e/...` with `-p 1`, and this package sorts before `bootstrap`, so
both suites here skip unless asked for. The package's `TestMain` (`harness_e2e_test.go`) brings
the platform up ONCE for both suites and tears it down after both.

| Target | What it runs |
|---|---|
| `make e2e-batch` | `Test_BatchOnKind` (`ADHAR_E2E_BATCH=1`), `-timeout 180m` |
| `make e2e-batch-volcano` | both suites (`ADHAR_E2E_BATCH=1 ADHAR_E2E_VOLCANO=1`) on Kind with Volcano: A2.9 |
| `make e2e-volcano-rehearsal` | no cluster: Volcano built from the tag `core/volcano` pins, on the shipped RBAC, against envtest (`TestVolcanoComponentsRunOnTheirNarrowedRBAC`) |

`.github/workflows/e2e.yaml` runs `volcano-rehearsal` and then `batch-volcano` on a push that
touches Volcano or what it drives (core/volcano, core/adhar-batch, the batchjob and team
compositions and XRDs, the autoscaler, `cmd/batch`, this directory), nightly, and on dispatch.
The bootstrap gate keeps running on every push and skips the nightly run.
`TestTheVolcanoCanaryIsWiredIntoMakeAndCI` pins the wiring: the target opts both suites in, the
job runs it after the rehearsal, and the job's budget (355m) exceeds `go test`'s (340m).

## Phase 2: the Volcano canary (`Test_BatchOnVolcano`, design §15 Phase 2, PLAT-2.8)

**Volcano stays disabled in every shipped profile.** Without `ADHAR_E2E_SKIP_UP` the harness copies
`platform/stack` to a temporary directory (or `ADHAR_E2E_VOLCANO_STACK`), switches on `volcano`,
`adhar-batch-volcano` and their contracts' required dependencies (`cert-manager`) in the COPY's
local profile, and runs `adhar up --recreate -p <copy>`. `TestVolcanoCanaryStagesACopyAndNeverTheShippedProfile`
proves the source tree is byte-identical afterwards. With `ADHAR_E2E_SKIP_UP` on a cluster that
does not serve Volcano, every subtest skips with the reason; when the harness installed Volcano
itself, its absence fails.

| Subtest | Criterion | How |
|---|---|---|
| preflight | A2.9 | Kubernetes ≥ 1.37 (`ADHAR_E2E_VOLCANO_KUBE_MINOR`), the three components available, the D7 descriptor (`scheduler`, `rootQueue`, `prodDeservedCPU/Memory`), the DEC-1 classes (`adhar-batch-volcano` −100 and `-critical` 0 PreemptLowerPriority, the native pair Never), the root queue, and dev/test/prod's leaves Open under it (prod not reclaimable with a deserved share) |
| another team's queue | A2.5 | A real second Team XR, so its leaves exist and are its own; then a Volcano Job, a PodGroup and a pod in team A's `dev` naming team B's leaf, and a dev Job naming the team's own prod leaf, are refused by `adhar-batch-queue-binding` (server dry-run; the own-leaf control is admitted). The refusal text is logged (FU-11) |
| all or nothing | A2.1, DEC-2 | Ballast fills every other node and leaves room for exactly N members (half a member of margin each way) on one; a gang of N+1 binds no member for `ADHAR_E2E_VOLCANO_HOLD`, a Pending gang has no pods (DEC-2), and with `overcommit` configured a gang beyond its enqueue room stays Pending. Then a gang of N in the same room runs, which proves the room was there. It also requires Volcano to word the shortage with one of the autoscaler's capacity markers |
| reclaim | A2.4, A2.7 | The Phase-1 job's dev run (single mode on Volcano) and a dev filler hold one node's room; a prod gang within prod's deserved share needs more than the room plus the filler. Prod runs, the dev run's execution ends STOPPED (graceful) and the run then completes exactly once. Ballast, a native sentinel at `adhar-batch` (the lowest class on the cluster) and, unless ST-2 refuses it, a default-scheduler pod with `scheduling.k8s.io/group-name`, all survive. Every Volcano eviction event names a pod Volcano scheduled |
| forged Volcano run | §10.3 | A Volcano Job labelled `adhar.io/batch-job=<the Phase-1 app>` whose pods are not the run template is refused by `adhar-batch-run-integrity` (server dry-run). Its queue is dev's own leaf, so the queue binding has nothing to refuse |
| prod leaf does not fit | DEC-3 | A prod `BatchJob` (partitioned, two workers) sized by `RunOverDeserved` from the prod leaf it finds: past its deserved share in every dimension and within its capability in every dimension, because the composition checks capability first. It reports `QueueFits` False, `ExceedsDeserved`, naming `globalSettings.batch`. It composes a run template and no pods |
| leaf capability exceeded | DEC-3 | A dev `BatchJob` sized by `RunOverCapability` past its leaf's capability (the batch ceiling or the team's allowance) reports `QueueFits` False, `ExceedsCapability`, naming the leaf and `globalSettings.batch`, instead of `Fits` and a PodGroup that waits Pending for good. `TestTheDEC3SizingLandsOnTheCompositionsVerdict` renders both sizings with `crossplane render` against the composition |
| one scale-up | A2.6 | Skips on Kind (the autoscaler cannot add a node there; `decision_test.go` is the evidence). On a cluster it can grow: a dev gang buys nothing, and an opted-in prod gang that fits one worker gets exactly one `ScalingUp` |
| partitioned scaffold | — | `springboot-batch-partitioned` (TPL-2.1); skips, naming TPL-2.1 and KIT-2.1, until it exists. Checks the dev run template against §4.5 (`CheckVolcanoGang`) and the BatchJob's `status.scheduler`/`status.queue` |
| manager ends workers | A2.3 | The last worker ends within `ADHAR_E2E_VOLCANO_COMPLETE_WITHIN` of the manager's success (CompleteJob), the Volcano Job is Completed, every partition ran once, and D2 then reports the run Succeeded without a new one |
| kill a worker | A2.2 | A worker SIGKILLed while one partition is done and another mid-flight: `retryCount` ≥ 1 (RestartJob), Completed, one COMPLETED execution, every row once, no partition ran again after it completed, and at least one unfinished partition resumed |
| stop and resume | FU-11 | `adhar batch stop` aborts the Volcano Job (AbortJob), the execution ends STOPPED, D2 says Stopped/4, and `resume` (ResumeJob) completes it exactly once |
| Volcano removed | A2.10 | The element whose Application manages the descriptor (from its tracking id) is switched off in the live ApplicationSet, with the automated sync of `adhar-platform-applicationset` (the Application that keeps that ApplicationSet equal to Gitea, with selfHeal) held for the scenario so the patch is not healed away; once Argo CD prunes it, each BatchJob reports `scheduler: kubernetes` with a native template (the partitioned one an Indexed Job the manager ends, §4.5), no BatchJob's generation moved, and a run completes natively. The element is switched back on and the owner's automated sync given back afterwards |

Each scenario sizes itself from the room it finds, so it runs on a 4-vCPU runner and a large
laptop alike, and fails with the numbers when the cluster cannot hold it.

**Written to the contract, not to today's code.** The composition's Volcano branch (PLAT-2.5),
the descriptor (FU-13), the kit's role switch and partition channel (§5.10, D11, KIT-2.1) and
the partitioned template (TPL-2.1) are built beside this suite. `testdata/volcano/` holds the
§4.5 run templates (VolcanoJob, IndexedJob) written from the design; replace them with
`crossplane render` output once PLAT-2.5 lands, as `testdata/run-template.yaml` is for the native
Job, and `CheckVolcanoGang`/`CheckIndexedFallback` then hold the composition to the contract.

**Live formats it records** (FU-11): the PodGroup's name for a Volcano Job, whether
`volcano.sh/task-index` is a label or an annotation, the PodGroup phases an over-size gang goes
through, the Unschedulable and PodScheduled wording, the queue-binding refusal text, the Queue
status keys, Volcano's eviction event wording, and whether its eviction carries DisruptionTarget.

## What it will catch on a first run (2026-11-14)

The suite is written to the design, not to today's template. Until the kit's runner lands
(KIT-1.2, KIT-1.3), the template's stand-in launcher (`PlatformRunLauncher`) is expected to
fail these assertions:

- **D13 summary.** It writes `outcome: "skipped(duplicate)"` with no `reason`. That fails the
  double-fire and clock subtests.
- **Invalid path.** It writes no summary on the invalid path. That fails the D13 check of A1.9;
  the exit code and Job assertions still hold.
- **Graceful stop.** It has no graceful stop (§5.9). The stopped execution stays STARTED, so
  stop/resume fails at "STOPPED within the grace period". The deleted and evicted pods fail
  the same way, on "execution N after a delete/evict".
- **Recovery.** It does not recover a STARTED execution, so the SIGKILL subtest fails. That
  subtest will also fail against a runner written to the letter of §5.5. §5.5 recovers when
  the owner pod is "gone (GET → 404)", but a Job keeps its failed pods. The SIGKILLed pod is
  still there, Failed, when the replacement starts about 10 s later. Its lease is younger than
  `stale-after`, so the runner exits 0 as `duplicate`. The run then completes with the instance
  left STARTED. KIT-1.3 must also treat a terminal owner pod as gone.

These failures are the point of the suite.

## Not covered here

- **A real OOM.** The SIGKILL subtest stands in for it. Run-integrity pins the memory limit to
  the template, so a run cannot be given a smaller one. To the JVM and the runner, an OOM kill
  is the same SIGKILL; only the kubelet's `OOMKilled` reason differs, and `BatchRunOOMKilled`
  (A1.4) is live-only. The kill goes through `docker exec <node> crictl` (or
  `KIND_EXPERIMENTAL_PROVIDER`), so it needs the Kind node on the machine that runs the suite.
- **Console halves.** The Console halves of A1.3 and A1.9 are tested in adhar-console.
- **Live-only criteria.** A1.4, A1.5, A1.6 (impersonation/chainsaw), A1.8 (live) and A1.10 are
  live-only.
