# Adhar Troubleshooting

**What this is for:** you have an error on screen and you want the fix. Every
section below starts with the *exact text* an operator sees, then how to
confirm it, the root cause, and the recovery. It covers the `adhar up`
bootstrap, ArgoCD/GitOps, the Crossplane control plane, cloud capacity, package
and image failures, and the storage/identity saturation modes that take the
whole platform down.

Day-0/day-1 procedures (verify-healthy checklist, safe re-run, upgrade,
teardown) live in the
[User Guide §12 — Operate the platform](USER_GUIDE.md#12-operate-the-platform).
The as-built design is
[design/0001 — Management-cluster-first](design/0001-management-cluster-first.md)
(decision record: [ADR-0001](adr/0001-management-cluster-first.md)). Production
posture is in [PRODUCTION.md](PRODUCTION.md); provider-specific setup in
[PROVIDER_GUIDE.md](PROVIDER_GUIDE.md); the full DigitalOcean runbook in
[DIGITALOCEAN_PROVIDER.md](DIGITALOCEAN_PROVIDER.md).

Unless a section says otherwise, examples assume namespace `adhar-system` and
the local Kind topology (`adhar up` with no `-f`), whose default host
`adhar.localtest.me` — and every `*.adhar.localtest.me` subdomain — resolves to
`127.0.0.1`. On cloud clusters substitute your `globalSettings.defaultHost`.

---

## Symptom lookup — what you see → where to go

| What you see | Go to |
| --- | --- |
| `curl` returns **HTTP 000**; every `*.adhar.localtest.me` URL hangs | [2.1 Gateway never Programmed](#21-gateway-never-programmed-http-000) |
| ArgoCD is up but shows **no applications at all** | [2.2 No apps in ArgoCD](#22-no-apps-in-argocd-gitops-phase-never-completed) |
| `failed installing <component>: …`, a component never `Available` | [2.3 A core installer errored](#23-a-core-installer-errored-partial-foundation) |
| `Gitea not ready` / `Gitea API not responding after 5 minutes` | [2.4 Gitea slow to serve](#24-gitea-slow-to-serve-seeding-stalls) |
| `CrossplaneReady` stays false; `adhar up` keeps requeuing | [2.5 Crossplane lags provider CRDs](#25-crossplane-lags-provider-crds) |
| Status update conflict on the last reconcile before shutdown | [2.6 Status write conflict](#26-status-write-conflict-on-the-final-local-pass) |
| App stuck at **`waiting for healthy state of <resource>`**; pushing to Gitea changes nothing; `kubectl` edits revert within a minute | [3.1 Wave-stuck sync](#31-a-wave-stuck-sync-replays-the-revision-it-started-with) |
| `415 Unsupported Media Type` when terminating an operation | [3.1 Wave-stuck sync](#31-a-wave-stuck-sync-replays-the-revision-it-started-with) |
| **All ~80 applications flip `OutOfSync`** right after a Gitea push | [3.2 Monorepo sync storm](#32-every-application-goes-outofsync-after-a-push-expected) |
| Apps `Degraded`/`Progressing` for the first few minutes after bootstrap | [3.3 First-convergence churn](#33-applications-degraded-during-first-convergence) |
| ArgoCD Keycloak login → **`Invalid redirect URL`** | [3.4 Wrong `argocd-cm.url`](#34-ha-only-invalid-redirect-url-on-keycloak-login) |
| Keycloak login succeeds but the **application list is empty** (admin sees all) | [3.5 Empty RBAC policy](#35-ha-only-empty-application-list-after-a-keycloak-login) |
| The ApplicationSet disappeared and nothing restored it | [3.6 Re-apply the ApplicationSet](#36-the-applicationset-was-deleted-and-nothing-restores-it) |
| `kubectl get clusterproviderconfigs.<group>` → **`No resources found`** on a healthy platform | [4.1 Zero ProviderConfigs](#41-zero-providerconfigs-crossplane-silently-skipped-them) |
| `field not declared in schema` on `spec.deletionPolicy` | [4.2 Namespaced managed resources](#42-deletionpolicy-field-not-declared-in-schema) |
| Applications hang `Terminating` forever after deleting a `CompositeCluster` | [4.3 Teardown finalizer trap](#43-applications-hang-terminating-after-a-compositecluster-is-deleted) |
| `0/N nodes are available: N node(s) exceed max volume count` | [5.1 The 7-volume wall](#51-exceed-max-volume-count--the-digitalocean-7-volume-wall) |
| `node(s) had volume node affinity conflict` | [5.2 Not a capacity problem](#52-volume-node-affinity-conflict--not-a-capacity-problem) |
| Autoscaler logs `holding`, or never adds a node | [5.3 The autoscaler is not scaling](#53-the-autoscaler-is-not-scaling) |
| A node flaps `NotReady`; `SystemOOM` events; containerd died | [5.4 Node OOM flaps](#54-a-node-flaps-notready-systemoom) |
| `doctl account get` → **401**, but droplets/volumes work | [5.5 Scoped DO token](#55-a-scoped-digitalocean-token-401s-on-v2account) |
| `adhar cluster list` prints **`No clusters found`** | [5.6 `cluster list` needs `--file`](#56-adhar-cluster-list-says-no-clusters-found) |
| `ImagePullBackOff` / `manifest unknown` on a `…-debian-11-rNN` tag | [6.1 Pruned Bitnami tags](#61-imagepullbackoff-on-a-bitnami--debian-11-rnn-tag) |
| `if kind is a CRD, it should be installed before calling Start` | [6.2 Chart rendered without `--include-crds`](#62-if-kind-is-a-crd-it-should-be-installed-before-calling-start) |
| `converting 'tcp://10.x.x.x:443' to type int` / `WEBHOOK_PORT` | [6.3 Service-link env injection](#63-webhook_porttcp-crashes-a-strict-env-decoder) |
| kpack webhook: `secret "webhook-certs" not found` | [6.4 kpack keeps its own namespace](#64-kpack-webhook-secret-webhook-certs-not-found) |
| `no matches for kind OCIRepository in version source.toolkit.fluxcd.io/v1beta2` | [6.5 tf-controller API skew](#65-no-matches-for-kind-ocirepository-in-version-v1beta2) |
| Webhook `certificate signed by unknown authority`; APIService discovery broken | [6.6 A chart baked its namespace](#66-a-chart-baked-its-own-namespace) |
| Scorecard job: `jq: Argument list too long` or `DeadlineExceeded` | [6.7 Scorecards](#67-the-scorecard-job-fails-or-scores-everything-the-same) |
| **Every SSO login returns 503**; Keycloak logs `Not enough disk space` | [7.1 Keycloak database full](#71-keycloak-503-on-every-login--its-database-filled) |
| `barman-cloud-wal-archive: exit status 4`, `no space left on device` | [7.2 Shared MinIO full](#72-minio-filled--cnpg-wal-archiving-stops-platform-wide) |
| `kubectl cluster-info` cannot reach the API server at all | [8 Management-cluster outage](#8-management-cluster-outage) |
| Anything else, locally | [9 When in doubt, re-run `adhar up`](#9-when-in-doubt-re-run-adhar-up) |

---

## Contents

1. [Is it actually healthy?](#1-is-it-actually-healthy)
2. [Bootstrap phase (`adhar up`)](#2-bootstrap-phase-adhar-up)
3. [ArgoCD and GitOps](#3-argocd-and-gitops)
4. [Crossplane control plane](#4-crossplane-control-plane)
5. [Capacity and cloud provider](#5-capacity-and-cloud-provider)
6. [Packages and images](#6-packages-and-images)
7. [Storage and identity saturation](#7-storage-and-identity-saturation)
8. [Management-cluster outage](#8-management-cluster-outage)
9. [When in doubt, re-run `adhar up`](#9-when-in-doubt-re-run-adhar-up)
10. [Testing a controller change without cutting a release](#10-testing-a-controller-change-without-cutting-a-release)

---

## 1. Is it actually healthy?

Run this before diagnosing anything. It checks each phase boundary in order:
Gateway programmed → ApplicationSet present → apps converging → Gitea org
seeded → reachable through the edge. If all pass, the platform is healthy and
the problem is elsewhere (DNS, browser, one app).

```bash
# 1. Gateway is Programmed (the data path is live)
kubectl get gateway -n adhar-system adhar-gateway \
  -o custom-columns=NAME:.metadata.name,PROGRAMMED:'.status.conditions[?(@.type=="Programmed")].status'
# want: PROGRAMMED=True

# 2. Local only: the Cilium-generated edge Service is a NodePort pinned to 30080/30443
kubectl get svc -n adhar-system cilium-gateway-adhar-gateway \
  -o custom-columns=NAME:.metadata.name,TYPE:.spec.type,PORTS:.spec.ports[*].nodePort
# want: TYPE=NodePort with 30080 and 30443 (cloud: TYPE=LoadBalancer with an ADDRESS)

# 3. The platform ApplicationSet(s) exist and the ArgoCD->Gitea auth landed
kubectl get applicationset -n adhar-system
kubectl get svc -n adhar-system gitea-argocd

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
# want: 200 or 3xx. HTTP 000 = TCP connects but nothing serves -> section 2.1
```

The single best summary is `adhar get status`: it renders the `AdharPlatform`
conditions (`ArgoCDReady`, `GatewayReady`, `GiteaReady`, `CrossplaneReady`,
`GitOpsReady`, aggregate `Ready`) plus per-package ArgoCD health. The raw status
carries the phase gates:

```bash
kubectl get adharplatform -n adhar-system -o yaml | less
# key gates: .status.gateway.available, .status.gitea.repositoriesCreated,
#            .status.crossplane.controlPlaneApplied, .status.autoscaling
```

> One claim `adhar get status` does **not** verify: that Crossplane can
> actually provision. Check that separately — [section 4.1](#41-zero-providerconfigs-crossplane-silently-skipped-them).

---

## 2. Bootstrap phase (`adhar up`)

The bootstrap runs in two phases (design/0001 §4):

1. **Bootstrap (imperative)** — an ordered, embedded, Server-Side-Applied
   foundation: `Gateway API CRDs → Cilium → Gateway → [CNPG, if HA] → ArgoCD →
   Gitea → Crossplane`.
2. **GitOps (declarative)** — seed the Gitea `packages`/`environments` repos,
   apply the ArgoCD repo auth, apply the platform ApplicationSet, then hand
   every further change to ArgoCD.

### 2.1 Gateway never Programmed (HTTP 000)

**Symptom:**

- `curl https://argocd.adhar.localtest.me:8443` returns **HTTP 000** — the TCP
  socket connects but no HTTP response comes back.
- Browsers hang or report a connection reset on every platform URL.

**Confirm:**

```bash
kubectl get gateway -n adhar-system adhar-gateway \
  -o custom-columns=NAME:.metadata.name,PROGRAMMED:'.status.conditions[?(@.type=="Programmed")].status'
# PROGRAMMED=False

kubectl get svc -n adhar-system cilium-gateway-adhar-gateway
# TYPE=LoadBalancer  EXTERNAL-IP=<pending>  PORT(S)=80:3xxxx/TCP,443:3yyyy/TCP

kubectl get ciliumgatewayclassconfig
# No resources found   <- the root cause
```

**Root cause** — the Gateway manifest embeds a `CiliumGatewayClassConfig` that
selects `service.type: NodePort` so Kind's host port-mapping works. Its CRD
(`ciliumgatewayclassconfigs.cilium.io`) is installed asynchronously by Cilium in
the *previous* step, and `applyManifest` deliberately skips objects whose CRD is
not yet registered (a bootstrap tolerance — design/0001 §5.1). Applied too
early, the config is dropped, Cilium defaults the Service to `LoadBalancer`,
and on Kind its external IP stays `<pending>` forever.

**Fix** — the safe path is to re-run `adhar up` (the Gateway reconciler re-runs
because `Gateway.Available` was never set). The surgical recovery:

```bash
kubectl apply -f platform/controllers/adharplatform/resources/gateway/gateway.yaml
kubectl get ciliumgatewayclassconfig            # expect one entry now

# Pin the node ports Cilium will not set itself:
# port index 0 -> 30080 (HTTP), 1 -> 30443 (HTTPS), 2 -> 8443 (alt HTTPS)
kubectl patch svc -n adhar-system cilium-gateway-adhar-gateway --type=json -p='[
  {"op":"replace","path":"/spec/ports/0/nodePort","value":30080},
  {"op":"replace","path":"/spec/ports/1/nodePort","value":30443},
  {"op":"replace","path":"/spec/ports/2/nodePort","value":8443}
]'

kubectl get gateway -n adhar-system adhar-gateway -w \
  -o custom-columns=NAME:.metadata.name,PROGRAMMED:'.status.conditions[?(@.type=="Programmed")].status'
```

If the Service is still `LoadBalancer` after the apply, the CRD had not
established — wait for `kubectl get crd ciliumgatewayclassconfigs.cilium.io` and
re-apply.

### 2.2 No apps in ArgoCD (GitOps phase never completed)

**Symptom** — ArgoCD is healthy but the UI shows no applications;
`kubectl get applicationset,applications -n adhar-system` returns nothing.

**Confirm:**

```bash
kubectl get applicationset,applications -n adhar-system   # No resources found
kubectl get svc -n adhar-system gitea-argocd              # not found = repo auth never applied

GITEA_POD=$(kubectl get pod -n adhar-system -l app=gitea -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n adhar-system "$GITEA_POD" -c gitea -- \
  curl -s -o /dev/null -w '%{http_code}\n' \
  -u 'gitea_admin:r8sA8CPHD9!bt6d' http://localhost:3000/api/v1/orgs/adhar
# 404 -> the org was never seeded

kubectl get adharplatform -n adhar-system -o jsonpath='{.items[0].status.gitea}{"\n"}'
# repositoriesCreated will be false/absent
```

**Root cause** — the bootstrap exited (or was interrupted) before the GitOps
phase completed. In local mode the controller runs in-process with
`ExitOnSync=true` and is ephemeral: once the process is gone nothing retries
(design/0001 §4.1, §9).

**Fix** — re-run `adhar up` **without** `--recreate`. It reuses the healthy
cluster and resumes from the pending gate (`RepositoriesCreated`); satisfied
gates short-circuit.

**Prevention** — `shouldShutDown` refuses to exit until `RepositoriesCreated`,
`ControlPlaneApplied` *and* `isPlatformAlreadyDeployed` (≥1 ApplicationSet plus
the `gitea-argocd` Service) all hold, so a clean run cannot report success with
an empty ArgoCD.

### 2.3 A core installer errored (partial foundation)

**Symptom** — `adhar up` logs `failed installing <component>: …` and requeues;
one of Gateway / ArgoCD / Gitea / Crossplane never reaches `Available`.

**Confirm:**

```bash
kubectl get adharplatform -n adhar-system -o jsonpath='{.items[0].status}{"\n"}'
kubectl get pods -n adhar-system                 # find the crashlooping/pending core pod
kubectl describe pod -n adhar-system <pod>
```

**Root cause** — an installer in the ordered slice returned an error (image
pull, admission webhook, resource pressure). Installs are ordered, so an early
failure (Cilium) blocks everything after it.

**Fix** — usually self-heals on requeue: every installer is idempotent
Server-Side Apply (`FieldManager="adhar"`, `ForceOwnership`), so the whole
ordered slice re-runs and re-adopts applied objects. If it does not converge,
`adhar up`; if the node is resource-starved, `adhar up --recreate`.

### 2.4 Gitea slow to serve (seeding stalls)

**Symptom** — bootstrap sits at "Setting up GitOps repositories…" and logs
`Gitea not ready` / `Gitea API not responding after 5 minutes`.

**Confirm:**

```bash
kubectl get deploy,pods -n adhar-system -l app=gitea
GITEA_POD=$(kubectl get pod -n adhar-system -l app=gitea -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n adhar-system "$GITEA_POD" -c gitea -- curl -sf http://localhost:3000/api/v1/version
kubectl logs -n adhar-system "$GITEA_POD" -c gitea --tail=50
```

**Root cause** — Gitea's deployment/pod/API took longer than the seeding step's
budget (slow disk, image pull, or in HA mode a CNPG database still
initialising). Seeding never races an unready API: `waitForGiteaReady` blocks on
deployment ready → pods Ready → an in-pod `GET /api/v1/version`, with a 10-minute
budget.

**Fix:**

```bash
kubectl rollout status deploy/gitea -n adhar-system --timeout=10m
adhar up                                   # resume seeding once Gitea serves
```

If Gitea is `CrashLoopBackOff`, check its logs and PVC; on HA confirm the CNPG
cluster is healthy (`kubectl get cluster -n adhar-system`).

### 2.5 Crossplane lags provider CRDs

**Symptom** — the foundation is up and ArgoCD has apps, but `adhar up` keeps
requeuing and `CrossplaneReady` stays false.

**Confirm:**

```bash
kubectl get adharplatform -n adhar-system -o jsonpath='{.items[0].status.crossplane}{"\n"}'
# available:true but controlPlaneApplied:false
kubectl get pkg -A                          # provider/configuration package health
```

**Root cause** — Crossplane reports `Available` as soon as its deployment is up,
but the kubernetes/helm `ClusterProviderConfig`s only apply once their provider
CRDs register, a minute or two later.

**Fix** — wait. The reconcile loop issues an explicit 15s requeue for this gate.
If it never clears, inspect `kubectl get pkg -A` and
`kubectl describe adharplatform -n adhar-system`. Once it *does* clear, still
verify [4.1](#41-zero-providerconfigs-crossplane-silently-skipped-them).

### 2.6 Status write conflict on the final local pass

**Symptom** — rare: the last reconcile before shutdown logs a status update
conflict, and you are unsure whether `Ready` / `RepositoriesCreated` /
`ControlPlaneApplied` survived.

**Confirm:**

```bash
kubectl get adharplatform -n adhar-system -o yaml | grep -A30 'conditions:'
```

**Fix** — none required. `postProcessReconcile` persists status through
`retry.RetryOnConflict`, re-reading and re-applying, so the flags survive the
controller exiting. If a field does look stale, a plain `adhar up` re-run
reconciles it back.

---

## 3. ArgoCD and GitOps

### 3.1 A wave-stuck sync replays the revision it started with

**Symptom** — the most confusing failure on the platform. An Application's
operation sits at:

```text
waiting for healthy state of <group>/<kind>/<name>
```

and then:

- pushing a fix to Gitea **changes nothing** — the app keeps failing on the old
  manifest;
- any `kubectl set image` / `kubectl patch` you make is **reverted within a
  minute**, which reads like `selfHeal` fighting you;
- `kubectl patch application <app> --type merge -p '{"operation":null}'` does
  **not** clear it;
- patching the `status` subresource fails with
  `applications.argoproj.io not found`.

**Root cause** — the in-flight operation is pinned to the revision it started
with and keeps **re-applying those stale manifests** until it is terminated. It
is not selfHeal, and it is not a Git problem: new commits are simply not part of
the running operation. Changing sync options on the ApplicationSet does not
affect an operation already in flight either.

**Fix — terminate through the API, then hard-refresh.** The
`content-type: application/json` header is **mandatory**; without it the
terminate returns `415 Unsupported Media Type`.

```bash
kubectl -n adhar-system port-forward svc/argo-cd-argocd-server 18455:80 >/dev/null &

PW=$(kubectl -n adhar-system get secret argocd-initial-admin-secret \
      -o jsonpath='{.data.password}' | base64 -d)
TOK=$(curl -s -X POST localhost:18455/api/v1/session \
      -H 'content-type: application/json' \
      --data "$(jq -nc --arg p "$PW" '{username:"admin",password:$p}')" | jq -r .token)

# Terminate the stuck operation (415 without the content-type header)
curl -s -X DELETE -H "Authorization: Bearer $TOK" -H 'content-type: application/json' \
  localhost:18455/api/v1/applications/<app>/operation

# Recompute from current main and re-sync
kubectl -n adhar-system annotate application <app> \
  argocd.argoproj.io/refresh=hard --overwrite
```

The next sync recomputes from current `main` and applies within a minute.

> If `.operation` was already nulled by hand and the app is wedged with
> `status.operationState.phase=Running`, some ArgoCD versions let you clear it
> with a merge patch on the status subresource
> (`{"status":{"operationState":null}}`); on others the subresource is not
> patchable and the API terminate above is the only route. Terminate first.

### 3.2 Every application goes OutOfSync after a push (expected)

**Symptom** — you push one small fix to the `adhar/packages` repo in Gitea and
**all ~80 Applications** immediately flip to `OutOfSync`, each logging
`Initiated automated sync to <new sha>`. It looks like the platform broke.

**Root cause** — it did not. Every platform Application tracks the *whole*
`adhar/packages` monorepo at `HEAD`, so one commit invalidates every app's
target revision. Apps with PostSync hooks (`harbor-oidc-config`, the Keycloak
config Jobs, …) stay `OutOfSync`/`Progressing` until their hook Jobs finish.

**Expected duration:** roughly **5–15 minutes**, ~10 minutes typical on a cloud
cluster.

Live-vs-target diffs shown during that window are API-server defaults
(HTTPRoute `group`/`kind`/`weight`, StatefulSet defaults) and disappear when the
sync completes. Server-side diff
(`argocd.argoproj.io/compare-options: ServerSideDiff=true`) does **not**
shortcut this — do not chase it.

**What to do** — wait for the wave to drain before judging app health. Only
investigate an app still `Degraded` after the storm settles.

### 3.3 Applications Degraded during first convergence

**Symptom** — after bootstrap several apps show `OutOfSync` or
`Degraded`/`Progressing` for the first few minutes.

**Root cause** — normal early convergence. The curated local core (32 apps
selected by the ApplicationSet's `enabled: "true"` filter) is still rolling out.
CNPG-backed databases wait on the operator and PVC binding; Keycloak takes 2–3
minutes for its first boot.

**Fix** — wait 3–5 minutes, then:

```bash
adhar get status
kubectl describe application -n adhar-system <app-name>
kubectl get events -n adhar-system --sort-by=.lastTimestamp | tail -30
```

Common *persistent* causes: a CNPG `Cluster` that force-fails sync without the
`ServerSideApply=true` sync option; an image pull error
([6.1](#61-imagepullbackoff-on-a-bitnami--debian-11-rnn-tag)); a PVC that cannot
bind ([5.1](#51-exceed-max-volume-count--the-digitalocean-7-volume-wall)).

### 3.4 HA only: `Invalid redirect URL` on Keycloak login

**Symptom** — on an HA cluster (`--ha` / `enableHAMode: true`), logging into
ArgoCD through Keycloak fails with **`Invalid redirect URL`**. The local admin
login works.

**Confirm:**

```bash
kubectl -n adhar-system get cm argocd-cm -o jsonpath='{.data.url}{"\n"}'
# a wrong value looks like: https://argocd.example.com
```

**Root cause** — ArgoCD builds the OIDC `redirect_uri` from `argocd-cm.url`. The
HA install manifest once shipped a placeholder host instead of the templated
`https://argocd.<host><portSuffix>`, so Keycloak rejected the callback. Fixed in
both installs; any cluster bootstrapped with an older binary still carries it.

**Fix (live)**

```bash
kubectl -n adhar-system patch cm argocd-cm --type merge \
  -p '{"data":{"url":"https://argocd.<your-defaultHost>"}}'
kubectl -n adhar-system rollout restart deploy/argo-cd-argocd-server
```

Then verify: `GET /api/v1/session/userinfo` should return `loggedIn: true` with
your Keycloak groups.

**Class of bug worth remembering:** *any* field that differs between the HA and
non-HA foundation manifests is a latent production-only defect. When editing
`hack/argocd/values*.yaml`, diff the `{{ .Host }}` markers between `install.yaml`
and `install-ha.yaml`.

### 3.5 HA only: empty application list after a Keycloak login

**Symptom** — a Keycloak user logs into ArgoCD successfully and sees **zero
applications**, while the local `admin` sees all of them. This reads exactly
like "the cluster was torn down".

**Confirm:**

```bash
kubectl -n adhar-system get cm argocd-rbac-cm -o jsonpath='{.data.policy\.csv}{"\n"}'
# empty output = the bug
```

**Root cause** — the HA install shipped an empty `argocd-rbac-cm.policy.csv`
(the HA values file lacked `configs.rbac.policy.csv`), so no Keycloak group
mapped to any role and every SSO user saw an empty, permission-filtered list.
Fixed in both installs.

**Fix (live)** — restore the group→role mapping and restart the server:

```bash
kubectl -n adhar-system patch cm argocd-rbac-cm --type merge -p '{"data":{"policy.csv":
"g, platform-admin, role:admin\ng, platform-developer, role:readonly\ng, platform-viewer, role:readonly\n"}}'
kubectl -n adhar-system rollout restart deploy/argo-cd-argocd-server
```

### 3.6 The ApplicationSet was deleted and nothing restores it

There is no background process that re-applies the ApplicationSet. In local mode
the controller is ephemeral and exits after the first successful convergence, so
if the ApplicationSet is deleted later on a running cluster it stays deleted.
Re-apply by hand:

```bash
kubectl apply -n adhar-system \
  -f platform/stack/adhar-appset-local.yaml \
  -f platform/stack/adhar-appset-workload.yaml
# if the repo secrets / gitea-argocd Service are also missing:
kubectl apply -n adhar-system -f platform/stack/argocd-auth.yaml
```

---

## 4. Crossplane control plane

### 4.1 Zero ProviderConfigs (Crossplane silently skipped them)

**Symptom** — the platform reports healthy,
`.status.crossplane.controlPlaneApplied` is `true`, and yet **nothing can be
provisioned**. Every XR sits unready; the providers have no configuration to
use.

**Confirm** — this is the check that matters, and `adhar get status` does not
make it for you:

```bash
kubectl get clusterproviderconfigs.kubernetes.m.crossplane.io
kubectl get clusterproviderconfigs.helm.m.crossplane.io
kubectl get clusterproviderconfigs.aws.m.upbound.io       # per cloud family in use
# "No resources found" for every group == the failure
```

**Root cause** — `applyManifestOwned` treated `meta.IsNoMatchError` ("the CRD is
not registered yet") as *skip with a warning*. On a fresh cluster the
kubernetes / helm / cloud `ClusterProviderConfig`s are applied while the
provider packages are still installing, so they all NoMatch and are dropped.
`applyControlPlaneConfiguration` still returned nil, the reconciler set
`ControlPlaneApplied: true`, and that one-shot gate then prevented the step from
ever running again. The warning scrolls past in the bootstrap log and the
platform looks fine.

**Fixed in the controller** — the providers/config step now uses
`applyManifestStrict` / `applyEmbeddedManifestsStrict` (NoMatch is a hard
error), and `applyCloudProviders` is fatal rather than best-effort. The
ApplicationSet is applied earlier in the reconcile, so failing here delays
nothing else.

**Fix on an affected cluster** — re-apply the control-plane configuration with a
binary that has the strict path, or apply the ProviderConfigs directly from
`platform/controlplane/configuration/providers/`, then confirm with the commands
above.

**Rule:** on any cluster where Crossplane is expected to work, check
`kubectl get clusterproviderconfigs.<group>` before believing
`ControlPlaneApplied`.

### 4.2 `deletionPolicy: field not declared in schema`

**Symptom** — a Composition fails to render or the managed resource is rejected
with `field not declared in schema` pointing at `spec.deletionPolicy`.

**Root cause** — Crossplane v2 **namespaced** (`.m`) managed resources expose
only `forProvider`, `initProvider`, `managementPolicies`, `providerConfigRef`
and `writeConnectionSecretToRef`. There is no `deletionPolicy`.

**Fix** — express "keep it on delete" as management policies instead:

```yaml
managementPolicies: ["Observe", "Create", "Update", "LateInitialize"]
```

Fixed in the four namespaced cluster compositions. **Still wrong** in the
namespaced `database/` and `network/` compositions (`aws-rds`, `gcp-cloudsql`,
`azure-sql`, `aws-vpc`, `azure-vnet`, `keycloak`) — expect this error if you use
them.

Related: **DOKS version slugs expire.** A hard-coded fallback such as
`1.33.1-do.3` eventually stops existing. Query
`doctl kubernetes options versions` rather than trusting a default.

### 4.3 Applications hang `Terminating` after a `CompositeCluster` is deleted

**Symptom** — you delete a `CompositeCluster`, and the ArgoCD Applications that
targeted that workload cluster hang in `Terminating` forever.

**Root cause** — deleting the XR removes the ArgoCD cluster-registration Secret
at the same instant as the cluster itself, so `resources-finalizer.argoproj.io`
on those Applications can never reach the (now gone) API server to prune.

**Fix** — clear the finalizers by hand:

```bash
kubectl -n adhar-system patch application <app> --type merge \
  -p '{"metadata":{"finalizers":[]}}'
```

**Correct order for a planned teardown:** prune the Applications first, then
deregister the cluster, then delete the XR — or drop the cascade finalizer when
the whole plane is being destroyed.

---

## 5. Capacity and cloud provider

### 5.1 `exceed max volume count` — the DigitalOcean 7-volume wall

**Symptom:**

```text
0/3 nodes are available: 3 node(s) exceed max volume count.
```

Pods stay `Pending`; new StatefulSets never start. You check CPU and memory and
they look fine — that is the point.

**Root cause** — **DigitalOcean attaches at most 7 block volumes per droplet.**
The full package catalogue needs 50+ PVCs, so the cluster runs out of *attachment
slots* long before it runs out of CPU or memory. **This is the platform's first
capacity wall on DigitalOcean**, not `Insufficient cpu`.

**Fix** — add workers. Each new droplet brings 7 more attachment slots.

```bash
adhar cluster scale <cluster> --workers 10 -p digitalocean -f config.yaml
```

Or let the node autoscaler do it: `exceed max volume count` is in the
autoscaler's `capacityShortageMarkers`, so it is treated as a scale-up trigger.

**Sizing rule:** the full production profile (~55–60 PVCs, ~300 pods against a
110-pods-per-node kubelet default) needs **at least 10 workers on DigitalOcean
regardless of droplet size**. A curated ~30-package profile runs comfortably on
3–4 × `s-8vcpu-16gb`.

### 5.2 `volume node affinity conflict` — not a capacity problem

**Symptom:**

```text
node(s) had volume node affinity conflict
… didn't find available persistent volumes to bind
```

**Root cause** — the volume is pinned to a zone/node the pod cannot use, or it
is unbound. Another identical droplet would **not** fix it, which is why the
autoscaler deliberately does *not* treat this message as a scale-up trigger.

**Fix** — look at the PV's `nodeAffinity` and the StorageClass
`volumeBindingMode`; move or recreate the claim, do not add nodes.

### 5.3 The autoscaler is not scaling

**Symptom** — pods are `Pending`, or the cluster is clearly idle, and nothing
happens. The status reason explains why:

```text
ScalingUp    1 pod(s) unschedulable for capacity (e.g. adhar-system/oncall-redis-master-0); adding a worker (4/10)
ScalingDown  cluster idle for 1h40m46s (cpu=21% memory=27%); removing adhar-dev-workers-9 (9->8 workers)
holding      cluster idle but a worker was removed recently; holding
```

**Confirm:**

```bash
kubectl get adharplatform -n adhar-system -o jsonpath='{.items[0].status.autoscaling}{"\n"}'
# workers, lastScaleUp, lastScaleDown, underutilizedSince, lastReason
```

**Reasons it legitimately does nothing:**

| `lastReason` says | Meaning |
| --- | --- |
| `… but maxWorkers=N reached` | The spend ceiling. Raise `autoscaling.maxWorkers`. |
| `… waiting out the 3m scale-up cooldown` | `scaleUpCooldown` since the last addition. |
| `utilization cpu=…% memory=…% at or above the 50% scale-down threshold` | Busy enough to keep every node. |
| `cluster idle for Xs of the required 10m` | `scaleDownDelay` has not elapsed. |
| `cluster idle … but minWorkers=N reached` | The floor. |
| `cluster idle but a worker was added/removed recently; holding` | **Guard 1** — one move per delay window, so the cluster settles. |
| `cluster idle but no worker is safe to drain: pod <ns>/<name> holds ReadWriteOnce volume <claim>` | **Guard 2** — a node hosting a ReadWriteOnce volume is never retired; the volume is attached to that machine. |

**Pending pods that are deliberately not scale-up triggers** — taints, node
affinity no current worker satisfies, and `volume node affinity conflict`. A new
worker is a clone of the existing ones, so a constraint none of them satisfies
cannot be fixed by buying another.

Scale-up is always evaluated before scale-down: a cluster that is
simultaneously "idle on average" and unable to schedule a pod has a
fragmentation problem, and removing a node makes it worse.

Configuration and defaults are in [PROVIDER_GUIDE §3](PROVIDER_GUIDE.md#3-node-autoscaling).

### 5.4 A node flaps `NotReady` (SystemOOM)

**Symptom** — a worker goes `NotReady`; `kubectl get events` shows `SystemOOM`;
whatever stateful pod lived there goes down with it. On a badly saturated node
containerd itself can be the victim.

**Root cause** — eBPF agents are the usual casualties on saturated 16 GiB nodes
(beyla first, then tempo). Once the kubelet or containerd is starved the node
drops out and takes its pods with it.

**Fix:**

```bash
# on the node, over SSH
sudo systemctl restart containerd kubelet
```

Then relieve the pressure: add workers ([5.1](#51-exceed-max-volume-count--the-digitalocean-7-volume-wall))
and make sure memory-hungry agents carry limits (beyla ships a 1 GiB limit for
exactly this reason).

### 5.5 A scoped DigitalOcean token 401s on `/v2/account`

**Symptom:**

```text
doctl account get
Error: GET https://api.digitalocean.com/v2/account: 401 Unable to authenticate you
```

…while droplets, volumes, load balancers, VPCs, DNS and Kubernetes all work
fine with the same token.

**Root cause** — a **scoped** DigitalOcean token. `/v2/account` and
`/v2/projects` are outside its scopes; the endpoints the platform actually uses
are not. **Never conclude a token is revoked from an account-endpoint 401** —
probe an endpoint the platform uses.

**Second trap:** `doctl` **ignores** both `DIGITALOCEAN_ACCESS_TOKEN` and `-t`
when its config file has a `context:` set. Force the default context:

```bash
doctl --context default -t "$TOKEN" compute droplet list
```

Adhar itself accepts `DIGITALOCEAN_TOKEN` or `DIGITALOCEAN_ACCESS_TOKEN`.

> If the CLI's own token is gone, a live cluster still holds a working one in
> the `adhar-dns-provider` Secret (`DO_TOKEN`) in `adhar-system` — enough to run
> `adhar cluster delete`.

### 5.6 `adhar cluster list` says "No clusters found"

**Symptom:**

```text
$ adhar cluster list
Listing clusters across all providers...

No clusters found
```

…for a cluster you know is running.

**Root cause** — **`adhar cluster list` requires `--file <config>`.** Without
it, it loads the default config, which on most machines declares no cloud
provider at all, queries nothing, and reports "No clusters found" — which reads
like your clusters are gone.

**Fix:**

```bash
adhar cluster list --file config.yaml
```

`adhar cluster delete`, `adhar cluster scale` and `adhar cluster upgrade` take
the same config file (`--file`, and `-f` on `scale`/`upgrade`; on
`cluster delete` `-f` is **`--force`**, so spell out `--file` there).

---

## 6. Packages and images

### 6.1 `ImagePullBackOff` on a Bitnami `-debian-11-rNN` tag

**Symptom:**

```text
Failed to pull image "docker.io/bitnami/redis:6.2.7-debian-11-r11": manifest unknown
ImagePullBackOff
```

Typically in a Bitnami **subchart** — mariadb, rabbitmq or redis under another
package (Grafana OnCall is the one in this platform).

**Root cause** — Bitnami pruned those historical `-debian-11-rNN` tags from
`docker.io/bitnami`. The chart still pins them.

**Fix** — the identical images live in **`bitnamilegacy`** under the **same
tags**. Repoint the repository, not the tag:

```text
docker.io/bitnami/redis:6.2.7-debian-11-r11
docker.io/bitnamilegacy/redis:6.2.7-debian-11-r11
```

Already applied to the oncall package's three subchart images. Any new Bitnami
subchart with a pinned `-debian-11-*` tag needs the same treatment in its
`values.yaml` and a re-render.

### 6.2 `if kind is a CRD, it should be installed before calling Start`

**Symptom:**

```text
if kind is a CRD, it should be installed before calling Start
{"kind": "StressChaos.chaos-mesh.org"}
```

…and the controller crash-loops forever. `kubectl get crd | grep <group>`
returns nothing.

**Root cause** — the package's manifests were rendered by `helm template`
**without `--include-crds`**. Helm omits a chart's `crds/` directory by default,
so the render contains zero CustomResourceDefinitions and the controller cannot
build a cache for a kind the API server has never heard of.

**Fix** — add `--include-crds` to the package's `generate-manifests.sh` and
re-render:

```bash
helm template --namespace adhar-system <name> <repo>/<chart> \
  -f values.yaml --version ${CHART_VERSION} --include-crds >> manifests/install.yaml
```

Then push to Gitea and let ArgoCD sync
([3.2](#32-every-application-goes-outofsync-after-a-push-expected) explains what
you will see next). Check any package whose chart ships a `crds/` directory.

### 6.3 `WEBHOOK_PORT=tcp://…` crashes a strict env decoder

**Symptom:**

```text
assigning WEBHOOK_PORT to WebhookPort: converting 'tcp://10.107.33.255:443' to type int
```

A pod that has nothing to do with webhooks panics at start-up.

**Root cause** — Kubernetes injects a `<NAME>_PORT` environment variable for
**every Service in the pod's namespace**. Since every platform package lives in
`adhar-system` (ADR-0011), cosign's policy-controller contributes
`Service/webhook`, which becomes `WEBHOOK_PORT=tcp://<clusterIP>:443`. Any pod
that decodes env strictly (envconfig reading `WEBHOOK_PORT` as an integer)
crashes. cosign hardcodes that Service name in its binary, so the *consumer* has
to opt out.

**Fix** — set `enableServiceLinks: false` on the affected pod spec:

```yaml
spec:
  template:
    spec:
      enableServiceLinks: false
```

Applied to the chaos-mesh workloads in its `generate-manifests.sh`. **Any new
package that parses environment variables strictly needs the same.** A component
that pins the variable itself as a container env var is safe — container env
beats service-link injection (that is why Tekton is fine next to cosign).

### 6.4 kpack webhook: `secret "webhook-certs" not found`

**Symptom** — `kpack-webhook` crash-loops (100+ restarts) with
`secret "webhook-certs" not found`, or a knative webhook presents a certificate
for the wrong SAN.

**Root cause** — kpack's `cmd/webhook/main.go` **and** sigstore
policy-controller's both hardcode `webhook.Options{SecretName: "webhook-certs"}`.
Two knative webhooks sharing one namespace fight over one Secret and one of them
ends up with a cert for the wrong SAN. Renaming kpack's Secret in the manifest
does not work either — the name is compiled into the binary.

**Fix / why it is designed this way** — **`buildpack` (kpack) is the single
package that keeps its own namespace, `kpack-system`.** Every other platform
package installs into `adhar-system`. Do not "consolidate" it.

**Before moving any knative-framework webhook** (kpack, cosign, tekton,
knative-serving) into a shared namespace, check `SecretName`/`ServiceName` in
its `cmd/webhook/main.go`, and keep
`platform/stack/packages/CONFLICTS.md` and ADR-0011 in sync.

### 6.5 `no matches for kind OCIRepository in version …/v1beta2`

**Symptom:**

```text
no matches for kind "OCIRepository" in version "source.toolkit.fluxcd.io/v1beta2"
```

…and ArgoCD retries the whole application.

**Root cause** — the tf-controller chart emits an `OCIRepository` at
`source.toolkit.fluxcd.io/v1beta2` while the bundled Flux CRDs serve only `v1`.

**Fix** — the terraform package's `generate-manifests.sh` repins those objects
from `v1beta2` to `v1` after rendering. If you re-render the chart by hand,
apply the same rewrite.

### 6.6 A chart baked its own namespace

**Symptom** — varies by chart, and all of them are confusing:

- `certificate signed by unknown authority` from a webhook, and every
  cert-manager custom resource fails (`inject-ca-from-secret: cert-manager/…`);
- APIService discovery breaks for `kubectl` *and* ArgoCD (kubescape's storage
  APIService cert SAN, from `ksNamespace`);
- CRDs annotated with a foreign namespace (`kamaji-system/…`).

**Root cause** — the chart hardcodes its default namespace into CA-injection
annotations, APIService SANs or CRD annotations, so rendering it into
`adhar-system` leaves dangling references.

**Fix** — regenerate the package with `--namespace adhar-system` and `sed` the
CRD annotations, then grep the generated manifest for foreign namespaces before
committing:

```bash
grep -nE '(cert-manager|kamaji-system|kubescape|kpack-system)/' manifests/install.yaml
```

### 6.7 The scorecard job fails, or scores everything the same

**Symptoms and causes:**

| Symptom | Cause and fix |
| --- | --- |
| `jq: Argument list too long` | Payloads passed with `--argjson`; real ones are megabytes (PolicyReports alone 3.6 MB). Write each payload to a file and bind with `--slurpfile` (yields an *array*, so unwrap with `[0]`). |
| `DeadlineExceeded` on the CronJob | The program rescanned every payload once per service. Project each payload to the fields used and index it in one linear pass, so per-service work is O(1); raise the container CPU limit above 250m — jq is CPU-bound. |
| Every service scores "stateful with backups, exposed" | Attribution was keyed on the destination namespace, and since every package lives in `adhar-system` each service inherited the whole platform's PolicyReports, HTTPRoutes and CNPG clusters. Attribute to `Application.status.resources` (ArgoCD's own inventory) instead. |
| A DaemonSet-only component is ungradable | The workload query listed only Deployments and StatefulSets. Include DaemonSets (alloy and the eBPF agents). |
| `jq: syntax error, unexpected end of file (Unix shell quoting issues?)` | The scoring program lives in a single-quoted bash string inside a ConfigMap; an apostrophe **in a comment** terminates the string. Keep the jq program apostrophe-free. |

**After changing the scorer, run it for real and inspect per-service
`signals[]`, not just the grades** — a plausible grade distribution hid the
attribution bug:

```bash
kubectl -n adhar-system create job --from=cronjob/adhar-scorecard-scorer scorecard-manual
kubectl -n adhar-system get cm adhar-scorecards -o yaml | less
```

---

## 7. Storage and identity saturation

These two failures are the platform's widest blast radius, and they are linked:
the second one causes the first.

### 7.1 Keycloak 503 on every login — its database filled

**Symptom** — **every SSO login on the platform returns 503.** ArgoCD, Grafana,
the Console, Harbor — everything that federates to Keycloak. Keycloak's own logs
show PostgreSQL errors; the CNPG cluster reports `Not enough disk space`.

**Confirm:**

```bash
kubectl -n adhar-system get cluster keycloak-db -o yaml | grep -A5 'conditions:'
kubectl -n adhar-system get pvc | grep keycloak
kubectl -n adhar-system logs keycloak-db-1 -c postgres --tail=50
```

**Root cause** — usually *not* Keycloak: WAL archiving stopped
([7.2](#72-minio-filled--cnpg-wal-archiving-stops-platform-wide)), so unarchived
WAL accumulated on the database volume until it filled. Keycloak archives
roughly **2.2 GiB of WAL per day**; a 5 GiB volume fills in days.

**Fix** — clear the archiving failure first, then expand the volume:

```bash
kubectl -n adhar-system patch pvc keycloak-db-1 --type merge \
  -p '{"spec":{"resources":{"requests":{"storage":"20Gi"}}}}'
```

The DigitalOcean CSI expands online. Note that CNPG's own `resizeInUseVolumes`
does nothing while the cluster is in the disk-space phase — patch the PVC
directly.

### 7.2 MinIO filled → CNPG WAL archiving stops platform-wide

**Symptom:**

```text
barman-cloud-wal-archive: exit status 4
… PutObject … no space left on device
```

on **every** CNPG database at once.

**Root cause** — the shared in-cluster MinIO is the default backup target for
every platform database. When its PVC fills, every `barman-cloud-wal-archive`
fails, so no database can retire WAL, so every Postgres volume starts filling —
and the first one to hit zero takes its service down
([7.1](#71-keycloak-503-on-every-login--its-database-filled)). A 10 GiB MinIO
filled in **two days** on a full platform.

**Confirm:**

```bash
kubectl -n adhar-system get pvc | grep minio
kubectl -n adhar-system get cluster -o custom-columns=\
NAME:.metadata.name,STATUS:.status.phase,LASTFAILED:.status.lastFailedBackup
```

**Fix:**

1. Expand MinIO (`persistence.size: 100Gi` in the package values, plus a live
   PVC patch so it takes effect now).
2. Set a CNPG `retentionPolicy` (7d) on **every** platform database so WAL is
   actually retired.
3. Expand any Postgres PVC that already filled ([7.1](#71-keycloak-503-on-every-login--its-database-filled)).
4. In production, point the BackupStorageLocation at real object storage rather
   than in-cluster MinIO — see [PRODUCTION §8](PRODUCTION.md#8-backup-and-disaster-recovery).

**Alert on this before it happens:** PVC usage > 80% on the MinIO claim, and
CNPG `lastFailedBackup` on any cluster.

---

## 8. Management-cluster outage

**Symptom** — `kubectl cluster-info` cannot reach the API server; `adhar`
commands fail to connect.

**Confirm:**

```bash
kubectl cluster-info
docker ps | grep adhar          # local: is the Kind node container running?
```

**Root cause** — the management cluster is a critical dependency
(ADR-0001). Its outage degrades the platform to "no changes" — ArgoCD cannot
reconcile and `adhar` cannot drive it — but **already-running workloads on other
clusters are unaffected**.

**Fix:**

- **Local** — restart Docker / the Kind node container, or `adhar up --recreate`
  to rebuild it. State is reconstructable: the platform re-bootstraps and ArgoCD
  re-syncs from Gitea.
- **Production** — an HA/DR concern; see
  [PRODUCTION §8.3](PRODUCTION.md#83-management-cluster-recovery-runbook). The
  in-cluster `adhar-controller-manager` Deployment self-heals the foundation
  once the cluster returns.

---

## 9. When in doubt, re-run `adhar up`

For nearly every local failure above, the correct first move is to run
`adhar up` again — **without `--recreate`**. This is safe by design:

- **Idempotent Server-Side Apply** — every foundation manifest is applied with
  `FieldManager="adhar"` and `ForceOwnership`. Re-applying an installed
  component re-adopts the existing objects; nothing is duplicated.
- **Status-gated, resumable phases** — each phase keys on a status flag
  (`Available`/`ControlPlaneApplied` for the foundation, `RepositoriesCreated`
  for GitOps). Satisfied gates short-circuit; the pending one runs.
- **Cluster reuse** — without `--recreate`, `Cluster.Reconcile(recreate=false)`
  returns early on the existing healthy node. Data, repos and running apps are
  preserved.
- **The exit gate verifies usability** — a clean run cannot report success while
  the Gateway is unprogrammed, the ApplicationSet is missing, or the repo auth
  is absent.

```bash
adhar up              # safe resume — reuse the cluster, resume from the pending gate
adhar up --recreate   # destructive — delete the node and rebuild
```

Use `--recreate` only to discard local state deliberately. On a cloud
environment the equivalent is
`adhar up -f config.yaml --env <name> --recreate`, which deletes that
environment's cluster first.

> Two production caveats: re-running `adhar up` against a *live, already
> deployed* cloud cluster adopts its machines but does not re-seed what is
> already there — use `adhar upgrade --yes` (or `--diff-only` to preview) to
> roll out changed edge settings or a newer stack. And `adhar up -f config.yaml`
> provisions **every** environment in the file: always pass `--env <name>`.

---

## 10. Testing a controller change without cutting a release

The in-cluster manager runs the **released** image, so a locally built
controller never runs there — and pushing a dev image needs Harbor, which needs
volumes, which needs nodes. Run the manager out-of-cluster instead:

```bash
# Stop the released one first, or leader election keeps the lease and your build idles
kubectl -n adhar-system scale deploy adhar-controller-manager --replicas=0

./adhar controller \
  --kubeconfig ~/.adhar/clusters/dev/kubeconfig \
  --platform-name dev \
  --leader-elect=false \
  --log-level debug
```

`--platform-name` must match the `AdharPlatform` CR, which is the **environment**
name (`dev`), not the default `adhar`. The manager reads everything it needs from
the cluster — the cloud token from the provider credentials Secret, the join key
from `adhar-cluster-ssh`, and provider/region/size from the
`adhar-cluster-spec` ConfigMap `adhar up` wrote — so a laptop-run manager
provisions real cloud nodes.

Restore the cluster afterwards:

```bash
kubectl -n adhar-system scale deploy adhar-controller-manager --replicas=1
```

---

**Related**: [User Guide](USER_GUIDE.md) · [Production Guide](PRODUCTION.md) ·
[Production Access](PRODUCTION_ACCESS.md) · [Provider Guide](PROVIDER_GUIDE.md) ·
[DigitalOcean runbook](DIGITALOCEAN_PROVIDER.md) ·
[design/0001 §9](design/0001-management-cluster-first.md)
