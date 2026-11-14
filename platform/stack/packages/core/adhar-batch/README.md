# adhar-batch

What the `BatchJob` API needs on a cluster and cannot compose per job (design
[0025](../../../../../docs/design/0025-batch-platform.md), user guide
[BATCH.md](../../../../../docs/BATCH.md)). It runs no workloads.

Two ApplicationSet elements sync it. `adhar-batch` syncs `manifests/` and is enabled in
every profile. `adhar-batch-volcano` syncs `manifests/volcano/` and is enabled exactly
where `volcano` is: Kyverno refuses a policy that names a kind the cluster does not
serve, so anything naming a `batch.volcano.sh` or `scheduling.volcano.sh` kind lives in
`manifests/volcano/`, never in `manifests/` (design D22). The ApplicationSet syncs a
`manifestPath` non-recursively, so neither element sees the other's files.

| Manifest | What it holds |
|---|---|
| `priorityclasses.yaml` | `adhar-batch` (−100) and `adhar-batch-critical` (0), `preemptionPolicy: Never`, for native runs. `adhar-batch-volcano` (−100) and `adhar-batch-volcano-critical` (0), `PreemptLowerPriority`, for Volcano runs (design D15). Batch never evicts a service: kube-scheduler never preempts for a native batch pod and never schedules a Volcano one, and Volcano takes victims only among its own jobs. |
| `rbac.yaml` | `adhar-batch-view` and `adhar-batch-edit` aggregate into `view`, `edit` and `admin`. `adhar-batch-operator` is bound per prod namespace by the Environment composition. |
| `kyverno-run-integrity.yaml` | A run started by anyone but the schedule must match its `ConfigMap <batch-job>-run`. In prod, a person's Job must name a BatchJob at all. Enforce, `failurePolicy: Fail`. |
| `kyverno-actor.yaml` | Stamps `adhar.io/batch-actor` on the run and its pods. |
| `kyverno-critical-prod-only.yaml` | `adhar-batch-critical`, `adhar-batch-volcano-critical` and `adhar.io/scale-up` only in prod namespaces. Enforce. |
| `kyverno-volcano-classes.yaml` | The two Volcano classes only on pods that name `schedulerName: volcano`, the one way kube-scheduler could otherwise preempt for a batch pod. |
| `kyverno-hold-only.yaml` | Anyone but Argo CD and Crossplane may change only a BatchJob's `adhar.io/batch-hold` annotation. Enforce. |
| `kyverno-pod-standards.yaml` | Memory request = limit, `restartPolicy: Never`, a batch class, no PVC, a deadline and a TTL. Audit in Phase 1, Enforce in Phase 3. |
| `alerts.yaml` | `BatchRunAdmittedUnchecked` (the API server admitted a run without asking the integrity webhook) and `BatchRunIntegrityUnenforced` (Kyverno's admission controller is gone). |
| `dashboard-batch.yaml` | Grafana's Delivery → Batch: the fleet's runs, failures and holds. |
| `volcano/kyverno-queue.yaml` | `adhar-batch-queue-default` (mutate) puts a Volcano run on its namespace's leaf `<organisation>-<team>-<tier>`; `adhar-batch-queue-binding` (Enforce, `failurePolicy: Fail`) denies any other queue, another team's leaf, a Volcano Job plugin other than `env`, and the `scheduling.k8s.io/group-name` annotation on a pod Volcano does not schedule; `adhar-batch-volcano-pod-standards` (Audit) holds Volcano runs to the pod standards and the Volcano classes. |
| `volcano/kyverno-prod-only.yaml` | `adhar-batch-volcano-prod-only`: the Volcano half of the prod-only rule. `adhar.io/scale-up: "true"` (which Volcano copies onto the PodGroup the autoscaler trusts) and `adhar-batch-volcano-critical` only on Volcano Jobs, CronJob job templates and PodGroups in prod namespaces. Enforce. |
| `volcano/rbac.yaml` | Lets Kyverno's admission controller read Volcano Queues, by aggregation, for the binding policy's team check. |

## Order on an upgrade

`BatchJob` reaches a cluster with a controller release and this package with a stack
push. Release the controller first. If the order is reversed, nothing breaks, but
Kyverno admits `adhar-batch-hold-only` with a warning (the kind does not exist yet)
and the policy has nothing to match until the XRD arrives.

Enabling Volcano: enable `volcano` and `adhar-batch-volcano` together. `volcano` alone
leaves Volcano runs without a queue binding; `adhar-batch-volcano` alone is refused by
Kyverno, because the kinds it names are not served yet.

## A Kyverno outage

Kyverno runs without its platform-wide fail-open override (design D18,
`security/kyverno` `features.forceFailurePolicyIgnore.enabled: false`), and each policy
states its own `failurePolicy`:

- **`Fail`**: `adhar-batch-run-integrity` and `adhar-batch-queue-binding`, and nothing
  else in the stack. During an outage the API server refuses a person's run, and a
  Volcano Job, Volcano CronJob, PodGroup or Volcano pod in a team namespace that
  Volcano's controllers did not create. Scheduled runs go on: both webhooks'
  `matchConditions` exclude the CronJob controller and `volcano-controllers` by
  username.
- **`Ignore`**: every other policy here (and in the rest of the stack). A scheduled run
  during an outage just has no actor stamp, and the pod standards are not audited.

`BatchRunAdmittedUnchecked` alerts on the API server's fail-open counter. With the two
policies at `Fail` it should never fire; if it does, a fail-open override is back.

## Tests

`make policy-test` runs `policy-tests/` with the Kyverno CLI, offline. Every policy is
exercised both ways. `run-integrity/resources.yaml` is generated by
`generate-resources.sh` from the Job a client creates from the run template, as the API
server hands it to admission, so every default the policy must tolerate is in it.
`queue-binding/`, `volcano-pod-standards/` and `volcano-prod-only/` hold the Volcano policies' suites (A2.5).
`TestAdharBatchReadmeListsEveryManifest` keeps the table above in step with
`manifests/`.
