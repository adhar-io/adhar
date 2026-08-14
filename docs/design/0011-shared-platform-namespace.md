# Low-Level Design — Single shared platform namespace (`adhar-system`)

Detailed design for [ADR-0011](../adr/0011-shared-platform-namespace.md). This is the authoritative as-built description of how every platform package lands in one namespace: the constant and its creation, the per-element ApplicationSet destination that keeps an escape hatch, the two collision failure-classes that sharing a namespace turns into platform invariants, the mitigations wired into the stack (`enableServiceLinks: false`, no vendored `Namespace`, PodSecurity posture), and the parity test that pins it all.

## 0. Context recap

Upstream charts default to a namespace-per-package layout (`cert-manager`, `harbor`, `monitoring`, …). ADR-0011 rejects that for *platform* components: fragmenting ~30 namespaces spreads credentials/RBAC/NetworkPolicies/troubleshooting across `kubectl get -A` archaeology, and every cross-package reference (Grafana→Loki, ArgoCD→Gitea, oauth2-proxy→Keycloak) becomes a fully-qualified cross-namespace name that breaks whenever one side moves. The decision: **all platform packages deploy into `adhar-system`, the same namespace as the bootstrap foundation**, with a per-element opt-out for genuinely incompatible packages. Tenant/app workloads still get their own namespaces (that boundary is the concern of the control-/data-plane split, [ADR-0023](../adr/0023-control-dataplane-separation.md)).

## 1. Invariants

- **INV-1** There is exactly one platform namespace, named by one constant: `globals.AdharSystemNamespace = "adhar-system"` ([globals/project.go](../../globals/project.go)). Bootstrap foundation and GitOps packages share it.
- **INV-2** The namespace a package targets is **data**, not hardcoded — the ApplicationSet destination is `namespace: "{{ .namespace }}"`, per list element. A package can opt out of the shared namespace by carrying a different value.
- **INV-3** A shared namespace is only safe under two hygiene rules that become platform invariants: **no two enabled packages define the same `kind/name`** (object-name collisions), and **any component that reads config from env sets `enableServiceLinks: false`** (service-link collisions). Both are audited by the scan in [CONFLICTS.md](../../platform/stack/packages/CONFLICTS.md).
- **INV-4** No package ever ships a `kind: Namespace` object — pruning `Namespace/adhar-system` would delete the platform.
- **INV-5** PodSecurity on `adhar-system` is **baseline, not restricted** — eBPF/system packages (Cilium, Falco, Tetragon) need privileged pods.

## 2. The namespace constant and where it is created

`adhar-system` is a compile-time constant, referenced ~everywhere the controller talks to the cluster:

```go
// globals/project.go
AdharSystemNamespace string = "adhar-system"
```

It is created imperatively during bootstrap, before any manifest is applied, by `k8s.EnsureNamespace` ([platform/k8s/client.go](../../platform/k8s/client.go)) — a get-or-create with no PodSecurity labels, invoked from the Kind TLS setup path ([platform/providers/kind/tls.go](../../platform/providers/kind/tls.go)) so the self-signed cert and ArgoCD TLS secrets have a home. The `AdharPlatform` CR itself is created **in** `adhar-system` (`Namespace: globals.AdharSystemNamespace`, [cmd/up/bootstrap.go](../../cmd/up/bootstrap.go); the local path targets the same via `types.NamespacedName{Name: name, Namespace: globals.AdharSystemNamespace}` in [cmd/up/local.go](../../cmd/up/local.go)). Every embedded foundation manifest hardcodes `namespace: adhar-system` (e.g. `resources/gitea/install.yaml`, `resources/argocd/install.yaml`), and readiness/health probes list Deployments in it (`gitea`, `argo-cd-argocd-server`, `crossplane` — `isPlatformAlreadyDeployed`, [controller.go](../../platform/controllers/adharplatform/controller.go)).

> **Drift — the vestigial project namespace.** `ReconcileProjectNamespace` ([controller.go](../../platform/controllers/adharplatform/controller.go) L800) creates `globals.GetProjectNamespace(resource.Name)` = `adhar-<CR-name>` (i.e. `adhar-adhar` for the default CR). Nothing deploys there; the operative namespace is the constant `adhar-system`, not the per-CR-name derivation. The `GetProjectNamespace` helper is a leftover from the pre-consolidation model.

## 3. Per-element destination and the one escape hatch (`adhar-appset-local.yaml`)

The ApplicationSet is a `list` generator: one element per package, each carrying its own `namespace`. The template's destination reads that field rather than pinning a literal, which is what preserves INV-2:

```yaml
# platform/stack/adhar-appset-local.yaml (template.spec.destination)
destination:
  # Per-element so a package that cannot share adhar-system can opt out.
  # Nearly everything uses adhar-system; open-function needs its own because
  # it vendors Knative/Tekton objects whose names are fixed upstream and
  # therefore collide (see platform/stack/packages/CONFLICTS.md).
  namespace: "{{ .namespace }}"
  server: https://kubernetes.default.svc
syncOptions:
  - CreateNamespace=true      # ArgoCD creates whatever ns the element names
  - ServerSideApply=true
```

Of the 77 wired elements, **76 set `namespace: "adhar-system"`; exactly one — `open-function` — sets `namespace: "openfunction"`**. This is the *only* current exception, and the same in both `adhar-appset-local.yaml` and `adhar-appset-production.yaml`:

```yaml
- name: "open-function"
  # Own namespace: open-function vendors Knative and Tekton objects whose
  # hardcoded names collide with the core tekton package.
  namespace: "openfunction"
  category: "application"
  manifestPath: "application/open-function/manifests"
```

`CreateNamespace=true` means ArgoCD stamps the target namespace on first sync; because no package ships a `kind: Namespace` (INV-4), this is the only creator of package namespaces post-bootstrap. Enablement is orthogonal to placement — the generator `selector: matchLabels.enabled: "true"` filters which of the 77 elements deploy (24 in the local core); the namespace field is unaffected.

## 4. Failure class 1 — object-name collisions

Two enabled packages defining the same `kind/name` in `adhar-system` each get an ArgoCD Application claiming ownership; they flap OutOfSync↔Synced and overwrite each other every sync. The known pairs ([CONFLICTS.md §1](../../platform/stack/packages/CONFLICTS.md)):

| Object | Packages | Why unrenamable |
|---|---|---|
| `ConfigMap/config-logging` | knative, **tekton** | both read this name by fixed convention |
| `ConfigMap/config-observability` | knative, **tekton** | same |
| `ConfigMap/config-defaults` | open-function, **tekton** | open-function vendors Knative's copy |
| `ConfigMap/config-tracing` | open-function, **tekton** | same |
| `Secret/webhook-certs` | cosign, **tekton** | last writer owns the cert; the other's webhook fails TLS |
| `ServiceAccount/minio-sa` | mimir, **minio** | mimir bundles its own MinIO |

(Bold = enabled in the local core, so enabling the *other* triggers the clash.) These **cannot** be fixed by renaming — Knative and Tekton read the ConfigMaps by fixed name, which is precisely why upstream isolates them in `knative-serving`/`tekton-pipelines`. The invariant: treat the pairs as **mutually exclusive**, or split one into its own namespace via the §3 escape hatch (exactly what `open-function` does).

## 5. Failure class 2 — service-link env collisions

Kubernetes injects `<SERVICE_NAME>_PORT=tcp://…` env vars into every pod for every Service in the same namespace. With one shared namespace, a generically-named Service can hijack an unrelated component whose flag parser reads env of that shape. This is not hypothetical:

> cosign's policy-controller ships `Service/webhook`, injecting `WEBHOOK_PORT=tcp://<ip>:443` into every pod in `adhar-system`. Crossplane parses `WEBHOOK_PORT` as its `--webhook-port` flag and crashloops with `expected a valid 64 bit int`.

The fix is disabling service links on every component that reads config from env — carried directly in the bootstrap manifests:

```yaml
# platform/controllers/adharplatform/resources/crossplane/install.yaml
# Platform components share the adhar-system namespace, so kube injects a
# <SVC>_PORT env var for every Service in it. cosign's policy-controller
# ships a Service named "webhook", yielding WEBHOOK_PORT=tcp://<ip>:443 —
# which crossplane parses as its own --webhook-port flag and fails to start.
enableServiceLinks: false
```

`enableServiceLinks: false` appears across ~19 stack packages plus the embedded crossplane (`install.yaml` + `install-ha.yaml`) and gitea (`post-install.yaml`, `install-ha.yaml`) foundation manifests. Generically-named Services currently live in the namespace and are standing hazards: `webhook` (cosign, buildpack), `controller` (buildpack, open-function), `operator`/`storage` (kubescape), `proxy` (jupyterhub), `dashboard` (devtron), and jenkins-x's Lighthouse `Service/hook` (injects `HOOK_PORT`). The standing ADR-0011 rule: **any component that parses `*_PORT`-shaped env must set `enableServiceLinks: false`** — service links are almost never used, and disabling them erases the whole failure class.

## 6. Package-author rules (enforced by review + scan)

1. **Never ship a `kind: Namespace`.** An app tracking `Namespace/adhar-system` deletes the platform namespace on prune (INV-4); a vendored Namespace carrying `pod-security.kubernetes.io/enforce: restricted` would block pod creation platform-wide.
2. **Namespace references hide in env values and CLI flags**, not just `namespace:` fields. A stale `OPERATOR_NAMESPACE: trivy-system` survived the consolidation sweep and crashlooped trivy-operator. When importing a chart, grep for `value: <name>-system` and `--*namespace=`, not only `namespace:`.
3. **Run the collision scan before enabling a package** — the Python one-liner at the bottom of [CONFLICTS.md](../../platform/stack/packages/CONFLICTS.md) walks `*/*/manifests/*.yaml`, groups by `(kind, name)` for Service/Deployment/StatefulSet/DaemonSet/ConfigMap/Secret/ServiceAccount, and prints any `(kind,name)` owned by >1 package.

## 7. PodSecurity posture

`adhar-system` is created with **no** `pod-security.kubernetes.io/*` labels (`EnsureNamespace`, §2), which leaves the cluster default. It must remain **baseline** and never be tightened to `restricted`: the platform runs privileged, host-networked, eBPF-based system components (Cilium, and optionally Falco/Tetragon) that a `restricted` policy rejects — enforcement on the shared namespace must be a deliberate, baseline choice (ADR-0011 consequence ⚠️). This is the trade-off of consolidation: one namespace cannot carry the strict PSA level a per-app namespace could, so isolation between platform packages drops to **label/NetworkPolicy** granularity — accepted because these are *platform* components under one operator.

## 8. What consolidation buys — short, stable cross-package names

Every cross-package reference resolves to a local Service name that survives refactors, instead of an N² web of fully-qualified cross-namespace DNS:

| Consumer → producer | Reference used |
|---|---|
| ArgoCD → Gitea (repo URL) | `http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/packages` (appset `sources[].repoURL`) |
| Grafana → Loki / Mimir / Tempo | in-namespace Service names (`loki`, `mimir`, `tempo`) |
| oauth2-proxy / apps → Keycloak | `keycloak:8080` in-cluster; external issuer via HTTPRoute for browser flows |

One RBAC boundary, one ResourceQuota target, one backup label selector, and one `kubectl get pods -n adhar-system` answers "what is the platform running." The `adhar.io/*` label set (`adhar.io/package-name`, `adhar.io/category`, plus `environment`) stamped by the ApplicationSet template gives the intra-namespace selectors that replace the lost namespace boundary.

## 9. Relation to the plane split (ADR-0023)

ADR-0011 collapses *platform* packages into one namespace on **one** cluster. ADR-0023 layers the next boundary on top: `adhar-system` becomes the **control plane** (fleet/platform services), while **application** workloads move off it onto data planes (vcluster locally, provisioned clusters in production). The two are complementary, not contradictory — 0011 says "platform components share a namespace"; 0023 says "app workloads don't run in that namespace at all." The `adhar.io/plane: control|workload|both` labeling and the Kyverno `control-plane-no-apps` policy proposed in [design/0023](0023-control-dataplane-separation.md) are the mechanism that keeps app workloads *out* of the shared `adhar-system`, which is what lets the baseline-PSA, low-isolation trade-off of §7 stay acceptable.

## Testing

- **Parity — namespace is part of the wiring identity** (`parity_test.go`, [platform/controllers/adharplatform/parity_test.go](../../platform/controllers/adharplatform/parity_test.go)): `wiring(e) = {Name, Namespace, Category, ManifestPath}`. `TestLocalProductionAppSetParity` asserts local and production ApplicationSets wire the **identical** package set including the `namespace` field — so the `open-function → openfunction` opt-out (and any future one) must appear in both, and no package can silently drift to a different namespace in one environment. `TestEnvironmentConfigsMatchAppSets` mirrors the same tuple against the per-env `config.yaml` package lists.
- **Collision scan** (manual/CI, [CONFLICTS.md](../../platform/stack/packages/CONFLICTS.md)): the Python scan enumerates `(kind,name)` owned by >1 package; a non-empty result is a merge blocker for the affected pair.
- **E2E** (`tests/e2e/bootstrap`, `make e2e`): a full `adhar up` brings the whole enabled core up in `adhar-system` and verifies foundation + GitOps health — the practical proof that 24 co-tenant packages coexist without object/service-link clashes.

## Code & file map

| Path | Responsibility |
|---|---|
| `globals/project.go` | `AdharSystemNamespace = "adhar-system"` constant; `GetProjectNamespace` (vestigial per-CR ns) |
| `platform/k8s/client.go` | `EnsureNamespace` — get-or-create of `adhar-system` at bootstrap (no PSA labels) |
| `platform/providers/kind/tls.go` | calls `EnsureNamespace(adhar-system)` before writing cert/TLS secrets |
| `cmd/up/{bootstrap,local}.go` | creates the `AdharPlatform` CR **in** `adhar-system` |
| `platform/controllers/adharplatform/controller.go` | `adhar-system`-scoped health/list checks; `ReconcileProjectNamespace` (legacy `adhar-<name>` ns) |
| `platform/controllers/adharplatform/resources/{gitea,argocd,crossplane,cnpg}/*.yaml` | embedded foundation, all pinned to `namespace: adhar-system`; crossplane/gitea set `enableServiceLinks: false` |
| `platform/stack/adhar-appset-local.yaml` / `-production.yaml` | per-element `namespace` destination + `CreateNamespace=true`; 76×`adhar-system`, 1×`openfunction` |
| `platform/stack/packages/CONFLICTS.md` | the two failure classes, the unrenamable-pair table, package-author rules, the collision scan |
| `platform/stack/packages/**/manifests/*.yaml` | ~19 packages set `enableServiceLinks: false`; none ship `kind: Namespace` |
| `platform/controllers/adharplatform/parity_test.go` | namespace is part of the parity `wiring` tuple; both appsets must agree |
