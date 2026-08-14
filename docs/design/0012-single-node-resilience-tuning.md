# Low-Level Design — Resilience tuning for single-node local clusters

Detailed design for [ADR-0012](../adr/0012-single-node-resilience-tuning.md). This is the
as-built account of exactly which knobs Adhar overrides — on the Kind node, in the embedded
ArgoCD install, and in the Keycloak package — to keep the full platform (~100 pods on one Kind
node inside an 8-CPU Docker VM) from collapsing into the three cascades the ADR names: probe
death-spirals, the pod-capacity ceiling, and sync stampedes. Every value is quoted from the
file that ships it; the closing section records where the as-built numbers diverge from the ADR
prose.

## 0. Context recap

`adhar up` runs etcd, Cilium, ArgoCD, Gitea, Keycloak and the enabled package set on a single
Kind node whose etcd data dir sits on a virtualized (fsync-slow) filesystem. Upstream defaults
for probes, `maxPods`, leader-election windows, and ArgoCD concurrency assume dedicated
multi-node headroom; under local convergence load they interact into cluster-wide failure.
ADR-0012 makes the platform **own the tuning of everything on the bootstrap critical path**
rather than shipping upstream defaults, and sizes those values for the worst supported
environment so cloud/HA inherits them harmlessly. The tuning lives at three sites, plus an HA
gate that swaps in richer renderings.

## 1. Kind node tuning (`platform/providers/kind/resources/kind.yaml.tmpl`)

The node template carries a single `kubeadmConfigPatches` block (matched against the kubeadm
**v1beta3** API — the comment flags that a `v1beta4`/list form is silently ignored). Three
groups of overrides, each with an inline rationale so a template regeneration review preserves
them:

```yaml
kubeadmConfigPatches:
  - |
    kind: KubeletConfiguration
    maxPods: 250                 # default 110 is a hard scheduling ceiling; core stack ~100 pods
    serializeImagePulls: false   # default serial queue stalls first-up behind one slow pull
    maxParallelImagePulls: 8
  - |
    kind: ClusterConfiguration
    etcd:
      local:
        extraArgs:
          unsafe-no-fsync: "true"   # throwaway cluster trades crash-durability for stable latency
    apiServer:
      extraArgs:
        service-node-port-range: "8443-32767"   # keep Gateway 8443 pinnable (gateway.go)
        oidc-issuer-url: "{{ .OIDCIssuerURL }}"  # ADR-0008 (unrelated to 0012)
        # …oidc-* + extraVolumes for the PKI mount…
    controllerManager:
      extraArgs:
        leader-elect-lease-duration: "60s"
        leader-elect-renew-deadline: "45s"
        leader-elect-retry-period: "5s"
    scheduler:
      extraArgs:
        leader-elect-lease-duration: "60s"
        leader-elect-renew-deadline: "45s"
        leader-elect-retry-period: "5s"
```

- **`maxPods: 250`** lifts the pod-capacity ceiling (failure mode 2). At the default 110,
  platform components that restart cannot reschedule ("Too many pods" `FailedScheduling`), which
  surfaces as unrelated ArgoCD/metrics outages.
- **Leader-election windows** (renew-deadline 45s vs. kube default 10s) let the
  controller-manager and scheduler ride out etcd fsync spikes instead of losing their lease,
  crashlooping, and stalling every sync (failure mode 1, control-plane variant).
- **`unsafe-no-fsync: "true"`** removes the root cause of those spikes on Docker Desktop —
  apiserver writes no longer wait on a slow virtualized `fsync`.
- **Parallel image pulls** (`serializeImagePulls: false`, `maxParallelImagePulls: 8`) are a
  convergence-speed override, not a resilience one: the first `adhar up` fetches 60+ images.

`networking` disables the default CNI and kube-proxy (Cilium replaces both). The template is
rendered by the Kind provider (`platform/providers/kind/`) before the cluster is created.

## 2. ArgoCD tuning (embedded install manifests)

ArgoCD is applied by `ReconcileArgoCD` in
[`platform/controllers/adharplatform/argocd.go`](../../platform/controllers/adharplatform/argocd.go),
which reads one of two embedded, pre-rendered files:

```go
argocdManifestPath := "resources/argocd/install.yaml"
if resource.Spec.BuildCustomization.EnableHAMode {
    argocdManifestPath = "resources/argocd/install-ha.yaml"
}
```

Both are generated from the same chart version by `hack/argocd/generate-manifests.sh` out of
`hack/argocd/values.yaml` (non-HA, the source of truth for the tuning) and
`hack/argocd/values-ha.yaml`. Chart-image is `quay.io/argoproj/argocd:v3.4.3`.

### 2.1 Concurrency budget — `ConfigMap/argocd-cmd-params-cm` (non-HA)

```yaml
controller.status.processors: "8"            # chart default 20
controller.operation.processors: "4"         # chart default 10
controller.kubectl.parallelism.limit: "4"    # bounds concurrent kubectl fork/execs
controller.self.heal.timeout.seconds: "5"
controller.repo.server.timeout.seconds: "60"
reposerver.parallelism.limit: "0"
```

The `values.yaml` comment states the intent: *"Throttled below the chart defaults (20/10): the
non-HA profile runs on single-node local clusters where the controller at full concurrency can
saturate kube-apiserver during initial convergence of the ~69-app ApplicationSet, starving every
other controller's leader-election lease."* This is the direct mitigation for failure mode 3
(sync stampedes) — steady progress over collapse.

### 2.2 Probes (non-HA)

Only the **argocd-server** probes are relaxed from the chart defaults (`timeout 1s / 3
failures`). From `install.yaml`, container `server`:

```yaml
readinessProbe: { path: /healthz,               timeoutSeconds: 5, failureThreshold: 3, initialDelaySeconds: 10, periodSeconds: 10 }
livenessProbe:  { path: /healthz?full=true,     timeoutSeconds: 5, failureThreshold: 6, initialDelaySeconds: 30, periodSeconds: 10 }
```

The **repo-server** probes are **left at the upstream `timeoutSeconds: 1 / failureThreshold: 3`**;
what it gains instead is a CPU-weight resource *request* (`values.yaml` comment: *"Requests only
— same single-node CPU-weight rationale as the controller"*):

```yaml
# repo-server container
resources: { requests: { cpu: 300m, memory: 512Mi } }
# application-controller container
resources: { requests: { cpu: 500m, memory: 1Gi } }
```

The requests give the two components a scheduler CPU share so they are not starved under
convergence load — the resilience lever the ADR reaches for on repo-server, in place of the
probe widening the ADR text describes (see [Drift](#drift-as-built-vs-adr)).

### 2.3 HA rendering (`install-ha.yaml`)

`EnableHAMode` swaps to a rendering that:

- **Restores upstream concurrency headroom**: `controller.operation.processors: "10"`,
  `controller.status.processors: "20"` — a multi-node HA cluster has the apiserver capacity the
  cap was protecting on a laptop.
- Scales components with `replicas: 2/3` and adds `PodDisruptionBudget`s.
- Enables the bundled Redis-HA, whose probes are the ones actually carrying
  `timeoutSeconds: 15, failureThreshold: 5` (plus a `startupProbe`) — sized for a StatefulSet
  quorum that must not be probe-killed mid-election.

The `ha_test.go` invariants (§5) assert both files exist, are the same chart version, both keep
the Keycloak `oidc.config` block, and that the HA file actually carries PDBs and >1 replica.

## 3. Keycloak probe (`platform/stack/packages/security/keycloak/manifests/install.yaml`)

Keycloak (`quay.io/keycloak/keycloak:22.0.3`, `start-dev`) boots in 2–3 minutes (JVM + realm
init). A 1-second readiness probe flaps during boot and repeatedly tears down the SSO config
flow (ADR-0013's Job), so the Deployment ships a widened readiness probe:

```yaml
readinessProbe:
  httpGet: { path: /realms/master, port: 8080 }
  initialDelaySeconds: 60
  periodSeconds: 20
  timeoutSeconds: 10
  failureThreshold: 6
```

This is a package manifest (GitOps phase), not an embedded bootstrap manifest — but Keycloak is
identity, whose failure is cluster-wide, so it falls under the same "size probes for the worst
environment" principle. `/realms/master` (not `/health`) is used because this Keycloak 22 image
serves health on the main HTTP port 8080 (no separate 9000 management interface).

## 4. Package enablement gate & sync policy (`platform/stack/adhar-appset-local.yaml`)

The operational half of the ADR — "enable packages in small batches" — is mechanized by the
local ApplicationSet (`metadata.name: helm-charts-local`). Every package is wired as a `list`
generator element carrying an `enabled` string; a generator `selector` filters to the curated
core:

```yaml
generators:
  - list:
      elements:
        - { name: "external-secrets", enabled: "true",  namespace: "adhar-system", category: "security", manifestPath: "security/external-secrets/manifests" }
        - { name: "knative",          enabled: "false", ... }
        # …77 elements total…
      selector:
        matchLabels: { enabled: "true" }     # only enabled packages become Applications
goTemplate: true
goTemplateOptions: [ missingkey=error ]
```

As built, **21 of 77 elements are `enabled: "true"`** — a representative local core
(external-secrets, vault, cnpg, keycloak, metrics-server, kube-prometheus, loki/alloy/tempo/
mimir/pyroscope, headlamp, jupyterhub, tekton, adhar-console, dapr, redis, …). The comment is
explicit: *"A single Kind node cannot run all packages (the full set OOM-kills the node)."*

The template's `syncPolicy` also encodes resilience choices relevant to the sync-stampede
failure mode:

```yaml
syncPolicy:
  automated: { prune: true, selfHeal: true }
  retry:
    backoff: { duration: 5s, factor: 2, maxDuration: 1m0s }
    limit: 15                       # ~12 min of retries; covers cold-bootstrap dependency races
  syncOptions:
    - CreateNamespace=true
    - ServerSideApply=true          # server-side apply — the ADR's "server-side" intent, at sync level
```

Each Application also carries the `resources-finalizer.argoproj.io` finalizer, so flipping a
package to `enabled: "false"` prunes its resources — **including any cluster-scoped
`ValidatingWebhookConfiguration`** — which is the ADR's "package removal must clean up admission
webhooks" rule realized through ArgoCD prune rather than bespoke code (there is no webhook
cleanup in `cmd/apps/delete.go`). The generically-named webhook-Service collisions this guards
against are catalogued in [`platform/stack/packages/CONFLICTS.md`](../../platform/stack/packages/CONFLICTS.md).

`appSetFileForProvider` in
[`controller.go`](../../platform/controllers/adharplatform/controller.go) selects
`adhar-appset-local.yaml` for Kind/empty provider and `adhar-appset-production.yaml` for the
clouds.

## 5. The HA gate — one boolean, pre-rendered variants

`EnableHAMode` is a `BuildCustomizationSpec` field
([`api/v1alpha1/adharplatform_types.go`](../../api/v1alpha1/adharplatform_types.go)) — immutable
after cluster creation — sourced from `GlobalSettings.EnableHAMode`
([`platform/config/config.go`](../../platform/config/config.go)) and plumbed onto the
`AdharPlatform` CR in `cmd/up/bootstrap.go` / `cmd/up/local.go`; `cmd/config/{get,set}.go` read
and toggle it; `cmd/upgrade/upgrade.go` branches on it.

The gate never renders at runtime: it selects one of two files that `hack/*/generate-manifests.sh`
produced from the same chart version. In HA mode:

| Component | Non-HA (single node) | HA (`install-ha.yaml`) |
|---|---|---|
| ArgoCD | replicas 1, capped concurrency (4/8), server-only probe widening | replicas 2/3, PDBs, upstream concurrency (10/20), Redis-HA 15s probes |
| Gitea | replicas 1, chart-bundled PostgreSQL | replicas 3, PDBs, DB → CNPG `gitea-db-rw` (`resources/cnpg/gitea-db.yaml`, `instances: 2`) |

This is the "cloud/HA inherits harmlessly" principle inverted where it matters: the *safe local*
defaults are the base file, and HA opts **up** into headroom (more processors, more replicas)
that the single-node profile deliberately withholds.

## 6. Failure-mode → mitigation map

| ADR failure mode | Root cause on single node | Mitigation (site) |
|---|---|---|
| 1. Probe death-spiral | CPU saturation makes healthy `/healthz` take >1s; kubelet kills, cold restart is slower | argocd-server probes 5s/6 (§2.2); Keycloak readiness 10s/6 (§3); Redis-HA 15s/5 (§2.3); controller/scheduler leader-election 45s renew (§1) |
| 2. Pod-capacity ceiling | kubelet `maxPods: 110` < ~100-pod working set | `maxPods: 250` (§1) |
| 3. Sync stampede | ArgoCD full concurrency + unbounded kubectl saturates one apiserver | processors 4/8, kubectl.parallelism 4 (§2.1); batched `enabled` gate + bounded retry (§4) |
| control-plane flap | etcd fsync spikes push apiserver writes past lease timeouts | `unsafe-no-fsync` + widened leader-election windows (§1) |
| webhook amplification | orphaned `ValidatingWebhookConfiguration` on a dead service degrades every write | ArgoCD prune on package disable via `resources-finalizer` (§4) |

## Testing

- **`platform/controllers/adharplatform/ha_test.go`** — `TestArgoCDHAManifestInvariants` /
  `TestGiteaHAManifestInvariants` guard that the HA variants exist, match the base chart version,
  carry PDBs + `replicas: 2|3`, keep the `oidc.config` block on both variants, and point Gitea-HA
  at the CNPG `gitea-db-rw` service with no chart-bundled PostgreSQL. `TestCNPGBootstrapManifests`
  checks `gitea-db.yaml` (`instances: 2`). `TestAppSetFileForProvider` pins local↔production
  appset selection.
- **`platform/controllers/adharplatform/parity_test.go`** — ApplicationSet/package parity (every
  wired element resolves to a real `manifestPath`), which keeps the 77-element list honest.
- **e2e** (`make e2e`, `tests/e2e/bootstrap`) — the ultimate test of this ADR is that a full
  `adhar up` converges on a laptop without the cascades; the bootstrap suite runs that cycle.
- **Tests to add** — a lint/parity assertion on the tuned values themselves (argocd-server probe
  ≥5s, `maxPods` ≥250, leader-election renew ≥45s) so a chart regeneration cannot silently revert
  the tuning, plus a check that repo-server's `optional` params match the intended concurrency.

## Code & file map

| Path | Responsibility |
|---|---|
| `platform/providers/kind/resources/kind.yaml.tmpl` | node tuning: `maxPods`, `unsafe-no-fsync`, leader-election windows, parallel image pulls |
| `hack/argocd/values.yaml` / `values-ha.yaml` | source-of-truth ArgoCD concurrency + probe + resource overrides (non-HA / HA) |
| `hack/argocd/generate-manifests.sh` | renders `values*.yaml` → embedded `install*.yaml` |
| `platform/controllers/adharplatform/resources/argocd/install.yaml` | embedded non-HA render (concurrency 4/8, server probe 5s, repo-server requests) |
| `platform/controllers/adharplatform/resources/argocd/install-ha.yaml` | embedded HA render (replicas/PDBs, concurrency 10/20, Redis-HA 15s probes) |
| `platform/controllers/adharplatform/argocd.go` | `ReconcileArgoCD` selects install vs install-ha by `EnableHAMode` |
| `platform/controllers/adharplatform/resources/gitea/{install,install-ha}.yaml`, `resources/cnpg/gitea-db.yaml` | Gitea non-HA (bundled PG) vs HA (CNPG `gitea-db`, PDBs) |
| `platform/stack/packages/security/keycloak/manifests/install.yaml` | widened Keycloak readiness probe (60s delay / 10s timeout / 6 failures) |
| `platform/stack/adhar-appset-local.yaml` | `enabled` gate + `selector`, bounded retry, prune-on-remove, ServerSideApply |
| `platform/stack/packages/CONFLICTS.md` | catalogue of shared-namespace / webhook-Service collisions the removal rule guards |
| `api/v1alpha1/adharplatform_types.go`, `platform/config/config.go` | `EnableHAMode` field + config plumbing |
| `platform/controllers/adharplatform/ha_test.go` | HA/appset invariants |

## Drift (as-built vs. ADR)

The ADR's Decision bullet on ArgoCD is the one place the prose and the shipped manifests diverge;
the *principle* holds but the specific numbers were written against a different rendering:

1. **"repo-server and server probes widened to `timeoutSeconds: 15, failureThreshold: 5` (from
   `1s/3`)."** As built, only **argocd-server** is widened, and to **`5s`** (liveness
   `failureThreshold: 6`, readiness `3`). **repo-server probes are unchanged at `1s/3`** — it
   receives a CPU/memory *request* instead. The literal `15s/5` values live only on the Redis-HA
   probes in `install-ha.yaml`.
2. **"controller concurrency capped (`operation.processors: 5`, `status.processors: 10`,
   `kubectl.parallelism.limit: 8`)."** Non-HA actually ships **4 / 8 / 4** (tighter than the ADR
   states); the `10 / 20` figures are the *HA* rendering, which restores upstream headroom.
3. **"server-side diff enabled."** `controller.diff.server.side` is **not present** in
   `argocd-cmd-params-cm`; its env var is wired `optional: true` and therefore unset, so
   controller-level server-side diff is **not** enabled in the non-HA install. (Server-side
   *apply* is enabled, at the ApplicationSet `syncOptions` level — §4.)
4. **Package counts.** CLAUDE.md/the ADR reference "~69 wired / ~16 curated core"; the local
   appset as built wires **77 elements with 21 enabled**. Directionally the same (small curated
   core, most wired-but-disabled), numerically newer.

None of these weaken the ADR's thesis — the platform still owns critical-path tuning and still
converges on an 8-CPU laptop — but the embedded manifests are the authority, and a regeneration
review should reconcile the ADR text (or the values) so the two stop disagreeing.
