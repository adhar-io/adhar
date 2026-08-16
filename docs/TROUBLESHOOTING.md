# Bootstrap Troubleshooting

A per-phase diagnosis and recovery runbook for `adhar up`. When a bootstrap does
not converge, this document maps the **observable symptom** to the phase that
stalled, the exact commands to confirm it, the root cause, the fix, and the code
gate that now prevents a recurrence.

It is the operator-facing companion to the authoritative as-built design in
[design/0001 — Management-cluster-first](design/0001-management-cluster-first.md)
(decision record: [ADR-0001](adr/0001-management-cluster-first.md)). Day-0/day-1
procedures (verify-healthy checklist, safe re-run, upgrade, teardown) live in the
[User Guide — Bootstrap & Day-2 Operations](USER_GUIDE.md#6-bootstrap--day-2-operations).

The bootstrap runs in two phases (design/0001 §4):

1. **Bootstrap phase (imperative)** — an ordered, embedded, Server-Side-Applied
   foundation: `Gateway API CRDs → Cilium → Gateway → [CNPG, if HA] → ArgoCD →
   Gitea → Crossplane`.
2. **GitOps phase (declarative)** — seed the Gitea `packages`/`environments`
   repos, apply the ArgoCD repo auth, apply the platform ApplicationSet, then
   hand every further change to ArgoCD.

Everything below assumes the local Kind topology (`adhar up` with no `-f`), the
`adhar-system` namespace, and the default host `adhar.localtest.me` (which, with
every `*.adhar.localtest.me` subdomain, resolves to `127.0.0.1`).

---

## First — is it actually healthy?

Run this block before diagnosing anything. It checks each phase boundary in
order: Gateway programmed → ApplicationSet(s) present → apps converging → Gitea
org seeded. If all four pass, the platform is healthy and any problem is
elsewhere (DNS, browser, a single app).

```bash
# 1. Gateway is Programmed (data path is live)
kubectl get gateway -n adhar-system adhar-gateway \
  -o custom-columns=NAME:.metadata.name,PROGRAMMED:'.status.conditions[?(@.type=="Programmed")].status'
# want: PROGRAMMED=True

# 2. The Cilium-generated edge Service is a NodePort pinned to 30080/30443
kubectl get svc -n adhar-system cilium-gateway-adhar-gateway \
  -o custom-columns=NAME:.metadata.name,TYPE:.spec.type,PORTS:.spec.ports[*].nodePort
# want: TYPE=NodePort, node ports include 30080 and 30443 (and 8443)

# 3. The platform ApplicationSet(s) exist and the ArgoCD->Gitea auth landed
kubectl get applicationset -n adhar-system
kubectl get svc -n adhar-system gitea-argocd
# want: >=1 ApplicationSet; the gitea-argocd Service present

# 4. Applications are syncing/healthy
kubectl get applications -n adhar-system \
  -o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status

# 5. The 'adhar' Gitea org and its repos were seeded
GITEA_POD=$(kubectl get pod -n adhar-system -l app=gitea -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n adhar-system "$GITEA_POD" -c gitea -- \
  curl -s -o /dev/null -w '%{http_code}\n' \
  -u 'gitea_admin:r8sA8CPHD9!bt6d' http://localhost:3000/api/v1/orgs/adhar
# want: 200

# 6. End-to-end reachability through the Gateway
curl -sk -o /dev/null -w 'HTTP %{http_code}\n' https://argocd.adhar.localtest.me:8443
# want: HTTP 200 (or 3xx). HTTP 000 = TCP connects but no HTTP -> Gateway not Programmed (see Failure 1)
```

The single best summary command is `adhar get status`, which renders the
`AdharPlatform` conditions (`ArgoCDReady`, `GatewayReady`, `GiteaReady`,
`CrossplaneReady`, `GitOpsReady`, aggregate `Ready`) plus per-package ArgoCD
health. Inspect the raw status any time with:

```bash
kubectl get adharplatform -n adhar-system -o yaml | less
# key gates: .status.gateway.available, .status.gitea.repositoriesCreated,
#            .status.crossplane.controlPlaneApplied
```

---

## Failure 1 — Gateway never Programmed (services unreachable, HTTP 000)

### Symptom

- `curl https://argocd.adhar.localtest.me:8443` returns **HTTP 000** — the TCP
  socket connects but no HTTP response comes back (nothing is bound behind the
  listener).
- Browsers hang or report a connection reset on every `*.adhar.localtest.me`
  URL.

### How to confirm

```bash
# Gateway is not Programmed
kubectl get gateway -n adhar-system adhar-gateway \
  -o custom-columns=NAME:.metadata.name,PROGRAMMED:'.status.conditions[?(@.type=="Programmed")].status'
# PROGRAMMED=False

# The generated Service came up as a LoadBalancer with a pending IP and random node ports
kubectl get svc -n adhar-system cilium-gateway-adhar-gateway
# TYPE=LoadBalancer   EXTERNAL-IP=<pending>   PORT(S)=80:3xxxx/TCP,443:3yyyy/TCP

# Root-cause probe: the GatewayClass parameters object is missing
kubectl get ciliumgatewayclassconfig
# No resources found
```

### Root cause

The Gateway manifest (`platform/controllers/adharplatform/resources/gateway/gateway.yaml`)
embeds a `CiliumGatewayClassConfig` that selects `service.type: NodePort` so
Kind's host port-mapping works. That object's CRD
(`ciliumgatewayclassconfigs.cilium.io`) is installed asynchronously by **Cilium**
in the *previous* bootstrap step. `applyManifest` deliberately **skips** objects
whose CRD is not yet installed (a bootstrap tolerance — design/0001 §5.1). If the
Gateway is applied before that CRD is `Established`, the config is silently
dropped, Cilium defaults the generated Service to `LoadBalancer`, its external IP
stays `<pending>` on Kind, and the Gateway never reaches `Programmed=True`.

### Fix (manual recovery)

Re-apply the missing config and pin the node ports so Cilium reprograms the
Gateway. (Prefer the safe path — just re-run `adhar up`, see below — but this is
the surgical recovery.)

```bash
# 1. Re-apply the GatewayClass + CiliumGatewayClassConfig + Gateway (service type NodePort)
kubectl apply -f platform/controllers/adharplatform/resources/gateway/gateway.yaml

# 2. Confirm the config object now exists
kubectl get ciliumgatewayclassconfig
# expect: one entry (the adhar gateway class config)

# 3. Pin the generated Service's node ports (Cilium won't set fixed ones itself).
#    Port index 0 -> 30080 (HTTP 80), 1 -> 30443 (HTTPS 443), 2 -> 8443 (alt HTTPS).
kubectl patch svc -n adhar-system cilium-gateway-adhar-gateway --type=json -p='[
  {"op":"replace","path":"/spec/ports/0/nodePort","value":30080},
  {"op":"replace","path":"/spec/ports/1/nodePort","value":30443},
  {"op":"replace","path":"/spec/ports/2/nodePort","value":8443}
]'

# 4. Watch the Gateway flip to Programmed=True
kubectl get gateway -n adhar-system adhar-gateway -w \
  -o custom-columns=NAME:.metadata.name,PROGRAMMED:'.status.conditions[?(@.type=="Programmed")].status'

# 5. Verify reachability
curl -sk -o /dev/null -w 'HTTP %{http_code}\n' https://argocd.adhar.localtest.me:8443
```

If the Service is still `LoadBalancer` after step 1, the CRD had not established;
give Cilium a moment (`kubectl get crd ciliumgatewayclassconfigs.cilium.io`) and
re-apply.

### Prevention (why re-running clears it)

The foundation installs in a fixed order (Gateway API CRDs → Cilium → Gateway),
and `applyManifest` skips objects whose CRD is not yet installed (design/0001
§5.1). If Cilium's `ciliumgatewayclassconfigs.cilium.io` CRD has not `Established`
when the Gateway is first applied, the `CiliumGatewayClassConfig` is dropped and
`pinGatewayNodePorts` cannot pin the ports — so `Gateway.Available` is left unset
and the core-install gate **re-runs the Gateway reconciler on the next pass**. By
then Cilium has registered the CRD, the config applies, and the ports are pinned
to 30080/30443/8443 (design/0001 §5.2). In local mode the controller is
ephemeral, so if it has already exited with the Service stuck as a
`LoadBalancer`, apply the manual fix above.

---

## Failure 2 — No apps in ArgoCD (GitOps phase never completed)

### Symptom

- ArgoCD is up and healthy, but the UI shows **no applications**.
- `kubectl get applicationset,applications -n adhar-system` returns nothing.

### How to confirm

```bash
# No ApplicationSet, no Applications
kubectl get applicationset,applications -n adhar-system
# No resources found

# The ArgoCD->Gitea repo-auth Service is missing (the auth-applied signal)
kubectl get svc -n adhar-system gitea-argocd
# Error: services "gitea-argocd" not found

# The platform org was never created in Gitea
GITEA_POD=$(kubectl get pod -n adhar-system -l app=gitea -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n adhar-system "$GITEA_POD" -c gitea -- \
  curl -s -o /dev/null -w '%{http_code}\n' \
  -u 'gitea_admin:r8sA8CPHD9!bt6d' http://localhost:3000/api/v1/orgs/adhar
# 404 -> org not seeded

# Confirm from the CR status which gate is pending
kubectl get adharplatform -n adhar-system \
  -o jsonpath='{.items[0].status.gitea}{"\n"}'
# repositoriesCreated will be false/absent
```

### Root cause

The bootstrap process exited (or was interrupted) **before the GitOps phase
completed**. In local mode the controller runs in-process with `ExitOnSync=true`
and is ephemeral — once the process is gone there is nothing left to retry
(design/0001 §4.1, §9). The GitOps phase seeds the org + repos, applies
`argocd-auth.yaml` (which creates the `gitea-argocd` Service), and applies the
ApplicationSet; if it stopped partway, ArgoCD comes up with nothing to sync.

### Fix (recovery)

Re-run `adhar up` **without** `--recreate`. It reuses the healthy Kind cluster
(`Cluster.Reconcile(recreate=false)` returns early) and resumes the reconcile
pipeline from the pending gate (`RepositoriesCreated`): already-satisfied gates
short-circuit, the GitOps phase re-runs to completion, and the ApplicationSet
lands.

```bash
adhar up            # no --recreate: reuse the cluster, resume from the pending gate

# then verify the phase completed
kubectl get applicationset -n adhar-system
kubectl get svc -n adhar-system gitea-argocd
kubectl get applications -n adhar-system \
  -o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status
```

### Prevention (the exit gate) and manual re-apply

`shouldShutDown` refuses to let the bootstrap exit until `RepositoriesCreated`,
`ControlPlaneApplied`, **and** `isPlatformAlreadyDeployed` (which verifies ≥1
ApplicationSet *and* the `gitea-argocd` Service exist) all hold — so a clean
`adhar up` cannot report success with an empty ArgoCD (design/0001 §4.1, §6.1).

There is no background process that re-applies the ApplicationSet. In local mode
the controller is ephemeral and exits after the first successful convergence, so
if the ApplicationSet is later deleted on an already-running cluster nothing
restores it automatically. Re-apply it by hand:

```bash
kubectl apply -n adhar-system \
  -f platform/stack/adhar-appset-local.yaml \
  -f platform/stack/adhar-appset-workload.yaml
# if the repo secrets / gitea-argocd Service are also missing:
kubectl apply -n adhar-system -f platform/stack/argocd-auth.yaml
```

---

## Failure 3 — Applications Degraded during convergence

### Symptom

- The ApplicationSet exists and applications appear, but several show
  `SYNC=OutOfSync` or `HEALTH=Degraded/Progressing` for the first few minutes.

### How to confirm

```bash
kubectl get applications -n adhar-system \
  -o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status
```

### Root cause

Normal early convergence. The curated local core (~two dozen apps selected by the
ApplicationSet's `enabled: "true"` filter) is still rolling out. Slow starters are
expected: CNPG-backed databases wait on the operator and PVC binding, and Keycloak
typically takes 2–3 minutes to finish its first boot. Transient `Degraded`/
`Progressing` during this window is not a failure.

### Fix

Wait 3–5 minutes and re-check. Only a **persistently** Degraded app needs
attention:

```bash
# summary of platform + per-app health
adhar get status

# drill into a specific stuck app
kubectl describe application -n adhar-system <app-name>
kubectl get events -n adhar-system --sort-by=.lastTimestamp | tail -30
```

Common persistent causes: a CNPG `Cluster` that force-fails sync without the
`ServerSideApply=true` sync option; an image pull error; or a PVC that cannot bind
on a single Kind node. Address the specific app — the platform itself is healthy.

### Prevention

Convergence timing is inherent, not a bug. The bootstrap does not gate exit on
every app being Healthy — only on the platform being *usable* (foundation ready +
ApplicationSet + repo auth present). ArgoCD self-heals the rest.

---

## Failure 4 — Partial foundation (a core installer errored)

### Symptom

- `adhar up` logs `failed installing <component>: ...` and requeues; one of
  Gateway / ArgoCD / Gitea / Crossplane never reaches `Available`.

### How to confirm

```bash
kubectl get adharplatform -n adhar-system -o yaml | grep -A20 'status:'
# look for which of gateway/argoCD/gitea/crossplane .available is false

kubectl get pods -n adhar-system
# find the crashlooping / pending core pod
kubectl describe pod -n adhar-system <pod>
```

### Root cause

Any installer in the ordered slice returned an error (image pull, admission
webhook, resource pressure). The pass aborts with `%s: %w` context and requeues at
`errRequeueTime` (5s). Because installs are ordered, a failure early in the slice
(e.g. Cilium) blocks everything after it.

### Fix

Usually self-heals via requeue — every installer is idempotent Server-Side Apply,
so the guard re-runs the whole ordered slice and re-adopts already-applied
objects. If it does not converge, re-run `adhar up` (no `--recreate`); if the node
is resource-starved, `adhar up --recreate` for a clean node.

```bash
kubectl get adharplatform -n adhar-system -o jsonpath='{.items[0].status}{"\n"}'
adhar up                 # resume; idempotent SSA re-adopts existing objects
# last resort, clean node:
adhar up --recreate
```

### Prevention

INV-1: every foundation apply is idempotent SSA with `FieldManager="adhar"` and
`ForceOwnership`, so re-application is always safe and the ordered slice re-runs
until every component is `Available` (design/0001 §5, §9 row 1).

---

## Failure 5 — Gitea slow to serve (seeding stalls)

### Symptom

- Bootstrap sits at "Setting up GitOps repositories…"; eventually logs
  `Gitea not ready` / `Gitea API not responding after 5 minutes`.

### How to confirm

```bash
kubectl get deploy -n adhar-system gitea
kubectl get pods -n adhar-system -l app=gitea
GITEA_POD=$(kubectl get pod -n adhar-system -l app=gitea -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n adhar-system "$GITEA_POD" -c gitea -- \
  curl -sf http://localhost:3000/api/v1/version
# should print a version JSON once the API is live
kubectl logs -n adhar-system "$GITEA_POD" -c gitea --tail=50
```

### Root cause

Gitea's deployment/pod/API took longer than the seeding step's budget (slow disk,
image pull, or — in HA mode — a CNPG database still initializing). The seeding
never races an unready API: `waitForGiteaReady` blocks on deployment ready → pods
Running/Ready → an in-pod `GET /api/v1/version` probe, with a 10-minute budget
(design/0001 §6 step 1, §9 row 4).

### Fix

Give Gitea time, then resume:

```bash
kubectl rollout status deploy/gitea -n adhar-system --timeout=10m
adhar up            # resume seeding once Gitea serves
```

If Gitea is `CrashLoopBackOff`, inspect its logs and PVC; on HA, confirm the CNPG
cluster for Gitea is healthy (`kubectl get cluster -n adhar-system`).

### Prevention

`waitForGiteaReady` gates seeding behind a comprehensive readiness probe, so
repository creation never fires against an unready API; a genuine timeout fails
loudly with a clear error and requeues rather than corrupting a half-seeded repo.

---

## Failure 6 — Crossplane control plane lags provider CRDs

### Symptom

- Foundation is up and ArgoCD has apps, but `adhar up` keeps requeuing and does
  not exit; `CrossplaneReady` stays false.

### How to confirm

```bash
kubectl get adharplatform -n adhar-system \
  -o jsonpath='{.items[0].status.crossplane}{"\n"}'
# available:true but controlPlaneApplied:false

kubectl get crds | grep -E 'crossplane|upbound'   # provider CRDs still registering
kubectl get providers.pkg.crossplane.io 2>/dev/null
```

### Root cause

Crossplane reports `Available` as soon as its deployment is up, but the
kubernetes/helm `ClusterProviderConfig`s only apply once their **provider CRDs
register** — a minute or two after the provider packages install. Until then
`ControlPlaneApplied` is false (design/0001 §4, §9 row 6).

### Fix

Wait — this converges on its own. The reconcile loop issues an explicit 15s
requeue specifically for this gate (so it converges even in watch mode where the
`ExitOnSync` loop is skipped). No action needed unless it never clears, in which
case inspect the provider/package status:

```bash
kubectl get pkg -A                       # provider/configuration package health
kubectl describe adharplatform -n adhar-system
```

### Prevention

The `!ControlPlaneApplied` branch in `Reconcile` returns an explicit
`RequeueAfter: 15s`, guaranteeing convergence regardless of topology. Shutdown is
gated on `ControlPlaneApplied` so local mode never exits leaving XRDs/Compositions
unapplied. See design/0001 §4.

---

## Failure 7 — Status write conflict on the final local pass

### Symptom

- Rare: the very last reconcile before shutdown logs a status update conflict;
  concern that `Ready`/`RepositoriesCreated`/`ControlPlaneApplied` might be dropped.

### How to confirm

```bash
kubectl get adharplatform -n adhar-system -o yaml | grep -A30 'conditions:'
# Ready should be True; RepositoriesCreated/ControlPlaneApplied should be true
```

### Root cause

On the final pass the in-process controller both writes status and cancels its own
context; a naive write could lose the update as the manager exits, permanently
dropping the flag in local mode (no controller remains to re-set it).

### Fix

None required — this is handled. If a status field does look stale, a plain
`adhar up` re-run reconciles it back (idempotent).

### Prevention

`postProcessReconcile` persists status through `retry.RetryOnConflict`, re-reading
the latest object and re-applying, so `Ready=True` (and the phase flags) survive
the controller exiting (design/0001 §4, §9 row 7).

---

## Failure 8 — Management-cluster outage

### Symptom

- The Kind node (or, in production, the management cluster) is down; `kubectl`
  cannot reach the API server; `adhar` commands fail to connect.

### How to confirm

```bash
kubectl cluster-info
docker ps | grep adhar          # local: is the Kind node container running?
```

### Root cause

The management cluster is a critical dependency (ADR-0001 ⚠️). Its outage degrades
the platform to "no changes" — ArgoCD cannot reconcile and `adhar` cannot drive
it — but **already-running workloads on other clusters are unaffected**.

### Fix

- **Local**: restart Docker / the Kind node container, or `adhar up --recreate`
  to rebuild the node (state is reconstructable — the platform re-bootstraps and
  ArgoCD re-syncs from Gitea).
- **Production**: this is an HA/DR concern; see [PRODUCTION.md](PRODUCTION.md). The
  in-cluster `adhar-controller-manager` Deployment self-heals the foundation once
  the cluster returns.

### Prevention

Architectural, not a bootstrap bug: Git is the source of truth, so a rebuilt
management cluster reconstructs the platform from the seeded Gitea repos. HA/DR
posture for production management clusters is documented in the Production Guide.

---

## When in doubt: re-run `adhar up`

For nearly every local failure above, the correct first move is simply to **run
`adhar up` again — without `--recreate`**. This is safe by design, not by luck:

- **Idempotent Server-Side Apply** — every foundation manifest is applied with
  `FieldManager="adhar"` and `ForceOwnership` (INV-1). Re-applying an
  already-installed component re-adopts the existing objects; nothing is
  duplicated or churned.
- **Status-gated, resumable phases** — each phase keys on a status flag
  (`Available`/`ControlPlaneApplied` for the foundation, `RepositoriesCreated` for
  GitOps). Already-satisfied gates short-circuit instantly; the one pending gate
  runs (design/0001 §4, §6.1).
- **Cluster reuse** — without `--recreate`, `Cluster.Reconcile(recreate=false)`
  returns early on the existing healthy Kind node. Your data, repos, and running
  apps are preserved; the bootstrap resumes from exactly where it stopped
  (design/0001 §9, last row).
- **The exit gate verifies usability** — a clean run cannot report success while
  the Gateway is unprogrammed, the ApplicationSet is missing, or the repo auth is
  absent. So "re-run until it reports done" is a valid, verified strategy.

Use `--recreate` **only** when you want to discard local state and rebuild the
node from scratch (e.g. a wedged Kind node or a resource-starved partial install
you no longer care about). It destroys the existing `adhar` cluster.

```bash
adhar up              # safe resume — reuse cluster, resume from pending gate
adhar up --recreate   # destructive — delete the Kind node and rebuild
```

See the [User Guide — Bootstrap & Day-2 Operations](USER_GUIDE.md#6-bootstrap--day-2-operations)
for the full verify-healthy checklist, upgrade, and teardown procedures, and
[design/0001 §9](design/0001-management-cluster-first.md) for the complete failure
& idempotency table.
</content>
</invoke>
