# ADR-0012: Resilience tuning for single-node local clusters

**Status**: Accepted · **Date**: 2026-07

## Context

`adhar up` runs the full platform — ~100 pods including etcd, Cilium, ArgoCD, Gitea, Keycloak, and the enabled package set — on a **single Kind node inside a Docker VM**, typically 8 CPUs shared with the host OS. Upstream defaults for probes, pod capacity, and controller concurrency all assume dedicated multi-node clusters with headroom. Under local convergence load (initial sync of 20+ applications, or enabling several packages at once) those defaults interact into three distinct cluster-wide failure modes, each observed repeatedly in practice:

1. **Probe death-spirals.** Many charts ship liveness probes with `timeoutSeconds: 1`. Under CPU saturation a healthy process routinely takes 2–5s to answer `/healthz`, so kubelet kills it; the cold restart is slower still, and the component crashloops while actually healthy. When the victim is ArgoCD's repo-server, *every* application's manifest rendering stalls and the whole platform appears broken; when it is the controller-manager/scheduler (leader-election renew deadlines), the control plane itself flaps.
2. **Pod-capacity ceiling.** Kubelet's default `maxPods: 110` is below the platform's working set. Once hit, *platform components themselves* can't reschedule after a restart ("Too many pods" FailedScheduling), which presents as unrelated ArgoCD/metrics outages and deadlocked deletions.
3. **Sync stampedes.** ArgoCD's default concurrency (operation processors 10, status processors 20, unbounded kubectl) lets a reconcile storm — e.g. after a repo push touching many apps — saturate the single kube-apiserver. API latency then breaks leader elections and probes, feeding failure modes 1 and 2. Admission webhooks amplify this: an unhealthy webhook backend (or an orphaned webhook configuration left by a removed package) adds seconds of latency or hard failure to every API write.

## Decision

The platform **owns the tuning of everything on the bootstrap critical path** instead of shipping upstream defaults:

- **Kind node** (`platform/providers/kind/resources/kind.yaml.tmpl`): `maxPods: 250`; controller-manager and scheduler leader-election windows widened (`lease-duration 60s / renew-deadline 45s / retry-period 5s`) to ride out etcd fsync latency spikes on virtualized filesystems
- **ArgoCD** (embedded install manifests): repo-server and server probes widened to `timeoutSeconds: 15, failureThreshold: 5` (from `1s/3`); controller concurrency capped (`controller.operation.processors: 5`, `controller.status.processors: 10`, `controller.kubectl.parallelism.limit: 8`); server-side diff enabled
- **Keycloak** (package manifest): readiness probe `initialDelaySeconds: 60, timeoutSeconds: 10, failureThreshold: 6` — a JVM that boots in 2–3 minutes cannot answer a 1-second probe while starting
- **Operational rule**: packages must be enabled in small batches locally (the `enabled` gate, ADR-0004), and package *removal* must clean up cluster-scoped admission webhooks — an orphaned `ValidatingWebhookConfiguration` pointing at a dead service degrades every API write in the cluster

The principle generalizes: **any component whose failure is cluster-wide (CNI, GitOps engine, identity, API-server-adjacent webhooks) gets probes and concurrency budgets sized for the worst supported environment**, not the average one. Cloud/HA deployments inherit the same values harmlessly — a 15s probe timeout changes nothing on an idle cluster but prevents collapse on a loaded one.

## Consequences

- ✅ The platform converges on an 8-CPU laptop without the probe-kill / pod-cap / sync-storm cascades that previously required manual recovery
- ✅ Failure modes degrade gracefully: under overload, syncs queue longer instead of the control plane flapping
- ⚠️ Wider probes mean genuinely-hung components are detected more slowly (worst case ~2.5 min instead of ~30s) — acceptable: false-positive kills were causing far more downtime than slow detection would
- ⚠️ Capped ArgoCD concurrency makes large reconcile storms take longer wall-clock; this is the intended trade (steady progress over collapse)
- ⚠️ These values are opinions encoded in embedded manifests (ADR-0006); upgrading upstream charts requires re-applying the tuning — the values and their rationale are commented inline at each site to survive regeneration reviews
