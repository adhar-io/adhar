# Score cards

Per-service **production-readiness scoring** for the Adhar platform (ROADMAP
Phase 3). A CronJob computes a 0–100 score and an A–F letter grade for every
service from real, adhar-controlplane signals and publishes the results to a ConfigMap
that the Console (Backstage) surfaces as a badge on each catalog entity.

No external service, no database, no custom image — just `kubectl` + `jq` on a
schedule, reconciled through GitOps like every other package.

## How it works

```
                 every 30 min
CronJob adhar-scorecard-scorer ──► reads (read-only SA):
                                     • ArgoCD Applications      (health / sync)
                                     • Deployments / StatefulSets (probes, resources, image)
                                     • HTTPRoutes               (exposure)
                                     • Kyverno PolicyReports    (pass rate)
                                     • Velero Schedules         (backup coverage)
                                     • CNPG Clusters            (backup config)
                                          │
                                          ▼  score.sh (kubectl | jq)
                       ConfigMap adhar-system/adhar-scorecards
                         • index.json    { "<svc>": {score, grade} }
                         • summary.json  full per-signal breakdown
                                          │
                                          ▼
                       Console badge  (adhar.io/scorecard annotation)
```

## Scoring model

A service's score is the weighted mean of four category fractions, normalised
to 0–100:

```
score = round( Σ (categoryFraction × weight) / Σ weights × 100 )
```

Each category fraction is the mean of its **applicable** signals (each 0..1). A
signal that can't be evaluated — the CRD isn't installed, no workload was found —
is dropped from its category's denominator, so missing data is neutral rather
than an automatic zero.

| Category | Weight | Signals |
|---|---|---|
| **Reliability** | 35 | `argocd_healthy` (Application health = Healthy), `probes` (every container has readiness **and** liveness probe), `resources` (every container sets requests **and** limits) |
| **Security** | 25 | `image_not_latest` (no container on `:latest` or an untagged image), `kyverno_pass_rate` (PolicyReport `pass / (pass+fail)` for the namespace) |
| **Observability** | 15 | `argocd_synced` (Application sync = Synced), `httproute_exposed` (an HTTPRoute targets the namespace) |
| **Operations** | 25 | `backup` — **stateful services only**: a Velero `Schedule` covers the namespace (or `*`) **or** a CNPG `Cluster` with `.spec.backup` exists. Stateless services score neutral here. |

Weights and thresholds are **tunable** in the `adhar-scorecard-config`
ConfigMap (`scorecard-crd-and-samples.yaml`) — edit, commit, and the next run
picks them up. Weights are relative; they need not sum to 100.

### Signal details

- **Workload resolution** — for each ArgoCD Application, the scorer selects
  Deployments/StatefulSets carrying the ArgoCD tracking label
  `app.kubernetes.io/instance=<app>`; if none are found it falls back to all
  workloads in the Application's destination namespace.
- **Image supply-chain** — an image ending in `:latest`, or with no tag at all,
  fails `image_not_latest`. A `@sha256:` digest passes (pinned).
- **Stateful detection** — a service is stateful if it owns a StatefulSet or a
  CNPG `Cluster` lives in its namespace; only then is `backup` scored.
- **Graceful empties** — if a signal's source CRD isn't installed, the scorer
  substitutes an empty list, so it runs cleanly on a minimal local cluster
  (e.g. no Velero/Kyverno) and simply marks those signals not-applicable.

## Grades

| Grade | Score |
|---|---|
| **A** | ≥ 90 |
| **B** | ≥ 80 |
| **C** | ≥ 70 |
| **D** | ≥ 60 |
| **F** | < 60 |

Thresholds are the `grade_a…grade_d` keys in `adhar-scorecard-config`.

## Output

ConfigMap `adhar-system/adhar-scorecards` (created/updated by the scorer — it is
**runtime-generated and not stored in Git**, because ArgoCD selfHeal would
otherwise revert it):

- `index.json` — compact lookup the Console reads:
  ```json
  { "generatedAt": "...", "services": { "argo-workflows": { "score": 87, "grade": "B" } } }
  ```
- `summary.json` — full detail incl. per-category scores and the signal ledger
  for the Console drill-down and `adhar get status`-style views.

## Console integration

Services opt in with a catalog annotation (`console-integration.yaml` documents
the full contract):

```yaml
metadata:
  annotations:
    adhar.io/scorecard: my-service   # key into adhar-scorecards/index.json
```

The Console resolves it by reading the `adhar-scorecards` ConfigMap (falling
back to `metadata.name` if the annotation value is empty). The
`adhar-scorecard-console` ConfigMap ships the Backstage proxy + TechInsights
wiring (fact retriever, a `production-readiness` check requiring ≥ C, and grade
badge colours).

## Files

| File | Purpose |
|---|---|
| `manifests/rbac.yaml` | ServiceAccount + read-only ClusterRole/binding + namespaced writer Role |
| `manifests/scorecard-crd-and-samples.yaml` | Tunable weights + grade thresholds (`adhar-scorecard-config`) |
| `manifests/scorer-cronjob.yaml` | The `score.sh` script ConfigMap + `adhar-scorecard-scorer` CronJob |
| `manifests/console-integration.yaml` | Backstage annotation contract, proxy/TechInsights config, badge colours |

## Running it by hand

```bash
kubectl -n adhar-system create job --from=cronjob/adhar-scorecard-scorer scorecard-now
kubectl -n adhar-system get configmap adhar-scorecards -o jsonpath='{.data.summary\.json}' | jq .
```

## Performance and resilience, proven automatically

Configuration alone cannot show that a service is production ready, so the
package also *tests* every delivered application (each Argo CD Application
labelled `adhar.io/application`). Nobody has to write or start anything.

The `adhar-resilience-runner` CronJob runs hourly. It tests an application
when its image changed or its evidence is more than a day old, at most three
per run, oldest first. It runs in the first healthy environment from
`runner_environments` (`test,dev`), and **never prod**.

| Step | How | Scorecard signal |
|------|-----|------------------|
| Load test | k6 Job against the app's Service; p95 and error-rate thresholds from the Service's `adhar.io/slo-latency-p95-ms` / `adhar.io/slo-error-rate` | `load_test` |
| Chaos | Litmus `pod-delete` with a continuous steady-state `httpProbe` on the Service | `chaos_pod_delete` |
| Recovery | seconds until the workload is fully ready again | `recovery_time` |
| Live SLO | Prometheus, 24 h, most-promoted environment with traffic | `latency_slo`, `error_budget` |

The scorer adds what the running workload already shows, measured on the
application's most-promoted environment: `replicas_ha`, `pod_disruption_budget`,
`autoscaling`, `spread_across_nodes`, `graceful_shutdown`, `restart_stability`
and `progressive_delivery`.

The results land in ConfigMap `adhar-resilience-results`, and the runner starts
the scorer so the console updates within a minute. An application is graded
once, under its own name, which is how the catalog entity joins it. Platform
packages keep their four original categories and show performance and
resilience as n/a.

To opt a service out of chaos, set `adhar.io/chaos: "false"` on its Service.
The scorecard records the opt-out and does not award the points. The test
point is the Service's `adhar.io/perf-path`, or its readiness probe path.
Every tunable lives in `adhar-scorecard-config`.

To run it now:

```bash
kubectl -n adhar-system create job resilience-now --from=cronjob/adhar-resilience-runner
```

### Every source of evidence, in one grade

The runner's chaos suite is configurable through `chaos_experiments`. The
default is `pod-delete`, `pod-network-latency` (300 ms) and `pod-cpu-hog`, each
under the same steady-state probe and each its own signal. The runner detects
k3s, whose container-runtime socket lives elsewhere, so the network and stress
faults work on Civo too.

Beyond what the runner proves, the scorer also reads what teams do themselves:

| Source | Join | Signal |
|--------|------|--------|
| Performance Center results (console, schedule, CI) | `adhar.io/application`, stamped from the test's target selector `app=<name>` | `perf_center_gate` (the gate verdict the Performance Center recorded) |
| Game days (console / ChaosCenter) | the workload Litmus targeted | `chaos_gameday` |

The console's Performance Center has a **Readiness** tab with one report per
application. It shows the grade, the load test against its targets, the live
SLO, every chaos fault with its probe and recovery, the team's gates and game
days, and every check with its reason. It prints to PDF and downloads as JSON.
Each application's scorecard links to its report.
