# Low-Level Design — Control-plane / Data-plane separation

Detailed design for [ADR-0023](../adr/0023-control-dataplane-separation.md). This is the single, authoritative design document: it specifies the goals/invariants, the actual Go types, CRD schema, controller reconcile logic, RBAC, manifests, placement, connectivity, migration, CLI wiring, milestones, risks, and tests down to file-and-function level. Status tracking lives in the [Roadmap](../ROADMAP.md) (Phase 2).

## 0. Goals & invariants

- **INV-1** Application workloads run only on data planes; the control plane runs only fleet/platform services.
- **INV-2** One control plane manages N data planes through a single first-class `DataPlane` API with aggregate status.
- **INV-3** App→data-plane placement is declared in Git and enforced by Sveltos/ApplicationSet, never implicit.
- **INV-4** The separation is exercised at every topology (T1 vcluster data plane → T3 physical fleet); nothing is T3-only.
- **INV-5** Migration from today's dual-role cluster is reversible and staged — no flag day.

## 1. API types (`api/v1alpha1/dataplane_types.go`)

New CRD in the existing group (`platform.adhar.io/v1alpha1`), matching the style of `adharplatform_types.go` (condition slices, `EnvironmentProvider` reuse, `FieldManager = "adhar"`).

```go
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DataPlaneInfraMode selects how the underlying cluster is realised.
type DataPlaneInfraMode string

const (
	InfraModeComposite DataPlaneInfraMode = "composite" // Crossplane CompositeCluster
	InfraModeVCluster  DataPlaneInfraMode = "vcluster"  // vcluster on the control plane (T1/T2)
	InfraModeAdopt     DataPlaneInfraMode = "adopt"     // register an existing kubeconfig
)

// DataPlaneProfile is the thin-agent bundle a data plane runs.
type DataPlaneProfile string

const (
	ProfileStandard DataPlaneProfile = "standard" // metrics-server, kyverno(+policies), alloy, eso-agent, spire-agent, gateway
	ProfileEdge     DataPlaneProfile = "edge"     // standard minus heavy collectors
	ProfileGPU      DataPlaneProfile = "gpu"      // standard + device-plugin, GPU feature discovery
	ProfileIsolated DataPlaneProfile = "isolated" // standard + stricter NetworkPolicies, dedicated node pools
)

// DataPlane condition types (metav1.Condition.Type values).
const (
	DataPlaneInfraReady        = "InfraReady"
	DataPlaneRegistered        = "Registered"
	DataPlaneAgentsReady       = "AgentsReady"
	DataPlaneMeshJoined        = "MeshJoined"
	DataPlaneObservabilityWired = "ObservabilityWired"
	DataPlaneReady             = "Ready" // aggregate
)

// Condition reasons.
const (
	ReasonProvisioning     = "Provisioning"
	ReasonInfraReady       = "InfraReady"
	ReasonRegistering      = "Registering"
	ReasonAgentsProgressing = "AgentsProgressing"
	ReasonMeshConnecting   = "MeshConnecting"
	ReasonReady            = "Ready"
	ReasonError            = "ReconcileError"
)

type NodePoolSpec struct {
	Name  string `json:"name"`
	Size  string `json:"size"`
	Count int    `json:"count"`
	// +optional
	GPU bool `json:"gpu,omitempty"`
}

type DataPlaneInfrastructure struct {
	// +kubebuilder:validation:Enum=composite;vcluster;adopt
	Mode DataPlaneInfraMode `json:"mode"`
	// +optional
	Provider EnvironmentProvider `json:"provider,omitempty"` // for mode=composite
	// +optional
	Region string `json:"region,omitempty"`
	// +optional
	NodePools []NodePoolSpec `json:"nodePools,omitempty"`
	// CompositeRef links the CompositeCluster XR the controller created (mode=composite).
	// +optional
	CompositeRef *NamedRef `json:"compositeRef,omitempty"`
	// KubeconfigSecretRef references a kubeconfig secret (mode=adopt).
	// +optional
	KubeconfigSecretRef *NamedRef `json:"kubeconfigSecretRef,omitempty"`
}

type NamedRef struct {
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

type DataPlaneMesh struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

type DataPlaneObservability struct {
	// Hub is the control-plane mesh identity that stores telemetry (default "adhar-mgmt").
	// +optional
	Hub string `json:"hub,omitempty"`
}

type DataPlanePlacement struct {
	// Labels are stamped on the ArgoCD cluster secret so ApplicationSet
	// generators and Sveltos ClusterProfiles can select this plane.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// DataPlaneSpec is the desired state.
type DataPlaneSpec struct {
	Infrastructure DataPlaneInfrastructure `json:"infrastructure"`
	// +kubebuilder:validation:Enum=standard;edge;gpu;isolated
	// +kubebuilder:default=standard
	Profile DataPlaneProfile `json:"profile,omitempty"`
	// +optional
	Mesh DataPlaneMesh `json:"mesh,omitempty"`
	// +optional
	Observability DataPlaneObservability `json:"observability,omitempty"`
	// +optional
	Placement DataPlanePlacement `json:"placement,omitempty"`
}

// DataPlaneStatus is the observed state.
type DataPlaneStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	// +optional
	ArgoCDCluster string `json:"argocdCluster,omitempty"`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// +optional
	AppCount int `json:"appCount,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=dp
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.infrastructure.mode`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.infrastructure.provider`
// +kubebuilder:printcolumn:name="Apps",type=integer,JSONPath=`.status.appCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DataPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DataPlaneSpec   `json:"spec,omitempty"`
	Status            DataPlaneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DataPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DataPlane `json:"items"`
}

func init() { SchemeBuilder.Register(&DataPlane{}, &DataPlaneList{}) }
```

`make manifests generate` regenerates `config/crd/…dataplanes.yaml` and `zz_generated.deepcopy.go`. The CRD is embedded for bootstrap under `platform/controllers/adharplatform/resources/crds/` and installed by `EnsureCRDs` ([crd.go](../../platform/controllers/crd.go)) alongside the existing three.

The aggregate roll-up on `AdharPlatform`:

```go
// AdharPlatformStatus gains:
type FleetStatus struct {
	DataPlanes    int `json:"dataPlanes,omitempty"`
	ReadyDataPlanes int `json:"readyDataPlanes,omitempty"`
	PlacedApps    int `json:"placedApps,omitempty"`
}
// FleetStatus is recomputed by the AdharPlatform reconciler by listing DataPlanes.
```

## 2. Controller (`platform/controllers/dataplane/`)

Files: `controller.go` (reconcile), `infra.go` (composite/vcluster/adopt), `register.go` (ArgoCD), `agents.go` (profile appset), `mesh.go` (Cilium/SPIFFE), `observability.go` (hub wiring), `conditions.go` (helpers). Registered in `main.go`/`cmd/controller` next to the three existing controllers.

### 2.1 Reconcile as a phase pipeline

State machine — each phase gates the next; a failed phase sets its condition `False` and requeues; a satisfied phase sets it `True` and proceeds. Mirrors the `AdharPlatformReconciler` requeue idiom (`defaultRequeueTime`, `errRequeueTime`).

```
                 +-----------+   ok   +------------+  ok  +-------------+
  reconcile ---> | InfraReady| -----> | Registered | ---> | AgentsReady |
                 +-----------+        +------------+      +-------------+
                       |                                        |
                    not ready (requeue 30s)               ok    v
                                              +-----------+  +---------------------+
                                              | MeshJoined| <-| ObservabilityWired |
                                              +-----------+  +---------------------+
                                                    |                 (skip if disabled)
                                                    v
                                               +---------+
                                               |  Ready  |  -> requeue defaultRequeueTime (steady poll)
                                               +---------+
```

```go
func (r *DataPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	dp := &v1alpha1.DataPlane{}
	if err := r.Get(ctx, req.NamespacedName, dp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Finalizer for orderly teardown (deregister ArgoCD, delete CompositeCluster/vcluster).
	if !dp.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, dp)
	}
	if !controllerutil.ContainsFinalizer(dp, dataPlaneFinalizer) {
		controllerutil.AddFinalizer(dp, dataPlaneFinalizer)
		return ctrl.Result{Requeue: true}, r.Update(ctx, dp)
	}

	// Phase 1 — infra
	kube, ready, err := r.ensureInfra(ctx, dp) // returns a client to the data plane once reachable
	if err != nil { return r.fail(ctx, dp, DataPlaneInfraReady, err) }
	if !ready { r.setCond(dp, DataPlaneInfraReady, metav1.ConditionFalse, ReasonProvisioning, "waiting for infra")
		return r.progress(ctx, dp, 30*time.Second) }
	r.setCond(dp, DataPlaneInfraReady, metav1.ConditionTrue, ReasonInfraReady, "cluster reachable")

	// Phase 2 — register with ArgoCD (create/patch the cluster secret + placement labels)
	argoName, err := r.ensureArgoRegistration(ctx, dp, kube)
	if err != nil { return r.fail(ctx, dp, DataPlaneRegistered, err) }
	dp.Status.ArgoCDCluster = argoName
	r.setCond(dp, DataPlaneRegistered, metav1.ConditionTrue, ReasonReady, "registered")

	// Phase 3 — thin-agent profile healthy on the data plane
	if ok, err := r.ensureAgents(ctx, dp); err != nil {
		return r.fail(ctx, dp, DataPlaneAgentsReady, err)
	} else if !ok {
		r.setCond(dp, DataPlaneAgentsReady, metav1.ConditionFalse, ReasonAgentsProgressing, "agents converging")
		return r.progress(ctx, dp, 20*time.Second)
	}
	r.setCond(dp, DataPlaneAgentsReady, metav1.ConditionTrue, ReasonReady, "agents healthy")

	// Phase 4 — mesh (optional)
	if dp.Spec.Mesh.Enabled {
		if ok, err := r.ensureMesh(ctx, dp, kube); err != nil {
			return r.fail(ctx, dp, DataPlaneMeshJoined, err)
		} else if !ok { return r.progress(ctx, dp, 30*time.Second) }
	}
	r.setCond(dp, DataPlaneMeshJoined, condFrom(dp.Spec.Mesh.Enabled), ReasonReady, "")

	// Phase 5 — observability hub wiring
	if err := r.ensureObservability(ctx, dp, kube); err != nil {
		return r.fail(ctx, dp, DataPlaneObservabilityWired, err)
	}
	r.setCond(dp, DataPlaneObservabilityWired, metav1.ConditionTrue, ReasonReady, "")

	// Aggregate
	dp.Status.AppCount = r.countPlacedApps(ctx, argoName)
	r.setCond(dp, DataPlaneReady, metav1.ConditionTrue, ReasonReady, "data plane ready")
	dp.Status.ObservedGeneration = dp.Generation
	if err := r.Status().Update(ctx, dp); err != nil { return ctrl.Result{}, err }
	return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
}
```

### 2.2 Phase implementations

- **`ensureInfra`** — switch on `spec.infrastructure.mode`:
  - `composite`: server-side apply a `CompositeCluster` XR (see §2.3), set `status`/`spec.compositeRef`, poll its `Ready`; on ready, read the connection secret the composition publishes and build a `client.Client` to the data plane.
  - `vcluster`: `helm`-render the vcluster chart into `adhar-system` (or a `dp-<name>` namespace) on the control plane; wait for the vcluster statefulset; fetch its kubeconfig secret.
  - `adopt`: load `kubeconfigSecretRef`, build the client, verify `/healthz`.
- **`ensureArgoRegistration`** — create/patch the ArgoCD cluster `Secret` (`argocd.argoproj.io/secret-type: cluster`) with the data-plane server/CA/token, and stamp `spec.placement.labels` as secret labels. Idempotent SSA with `FieldManager`.
- **`ensureAgents`** — ensure the workload ApplicationSet (see §4) targets this cluster (label match) and query ArgoCD for the health of the profile's Applications on it; return `true` only when all are `Healthy`.
- **`ensureMesh`** — run the Cilium clustermesh connect (`cilium clustermesh connect --context mgmt --destination-context <dp>`) via a Job on the control plane using the CLI image; register the SPIFFE trust-domain entry. Idempotent (checks `clustermesh status` first).
- **`ensureObservability`** — apply the `observability-hub` ConfigMap into the data plane (Alloy reads it) with `hub` + ingest endpoints.
- **`finalize`** — deregister ArgoCD cluster secret, delete the `CompositeCluster`/vcluster (only for controller-created infra, never `adopt`), remove finalizer.

### 2.3 CompositeCluster XR authored by the controller (mode=composite)

```yaml
apiVersion: platform.adhar.io/v1alpha1
kind: CompositeCluster
metadata:
  name: {{ .dp.Name }}
  namespace: adhar-system
  ownerReferences: [ <DataPlane> ]      # GC when the DataPlane is deleted
spec:
  provider: {{ .dp.Spec.Infrastructure.Provider }}
  region:   {{ .dp.Spec.Infrastructure.Region }}
  nodePools: {{ toYaml .dp.Spec.Infrastructure.NodePools }}
  writeConnectionSecretToRef:
    name: {{ .dp.Name }}-kubeconfig
    namespace: adhar-system
```

### 2.4 `SetupWithManager`

```go
func (r *DataPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DataPlane{}).
		Owns(&unstructured.Unstructured{ /* CompositeCluster GVK */ }).
		Watches(&argov1alpha1.Application{}, handler.EnqueueRequestsFromMapFunc(r.appToDataPlane)). // recount on app health changes
		Complete(r)
}
```

## 3. Package `plane` label and split

Every package directory declares its plane via an annotation consumed by the appset generator and a Kyverno check:

- Add `adhar.io/plane: control | workload | both` to each package's kustomization/label set. Agents that must also run on the control plane (metrics-server, kyverno, alloy) are `both`.
- The list generator elements in the appsets carry `plane` so a single source of truth drives placement.

## 4. ApplicationSets

### 4.1 `platform/stack/adhar-appset-control.yaml`

Targets the management cluster only, selects control-plane packages.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata: { name: adhar-control, namespace: adhar-system }
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - clusters: { selector: { matchLabels: { "argocd.argoproj.io/secret-type": "local" } } }  # in-cluster only
          - list:
              elements:   # only plane in (control, both)
                - { packageName: argo-cd,   plane: control, manifestPath: "core/argo-cd/manifests" }
                - { packageName: gitea,     plane: control, manifestPath: "core/gitea/manifests" }
                - { packageName: keycloak,  plane: control, manifestPath: "security/keycloak/manifests" }
                - { packageName: adhar-ai,  plane: control, manifestPath: "ai/adhar-ai/manifests" }
                # …crossplane, vault, external-secrets, cert-manager, hub(grafana/mimir/loki/tempo), harbor, sveltos, kargo, console
  template: { … same source/syncPolicy shape as today … }
```

### 4.2 `platform/stack/adhar-appset-workload.yaml` (extend the existing thin-agent appset)

Add the `plane in (workload, both)` app packages, gated by placement labels via the cluster generator selector (already present). The current file delivers metrics-server/kyverno/alloy; extend the `list` with the app catalog and let the **cluster selector** + Sveltos decide which data plane receives which app.

## 5. Placement — environments repo + Sveltos

### 5.1 Environment→plane binding (`environments/<env>/placement.yaml`)

```yaml
placement:
  environment: production
  dataPlaneSelector:
    matchLabels: { tier: production, region: sgp1 }
```

### 5.2 Sveltos ClusterProfile (packaged; one per environment)

```yaml
apiVersion: config.projectsveltos.io/v1beta1
kind: ClusterProfile
metadata: { name: apps-production }
spec:
  clusterSelector:
    matchLabels: { tier: production }
  helmCharts: []            # apps come via ArgoCD; Sveltos enforces membership/add-ons
  policyRefs: []            # e.g. baseline NetworkPolicies per plane
```

## 6. Enforcement (`platform/stack/packages/security/policy-packs/manifests/plane-isolation.yaml`)

Kyverno ClusterPolicy on the control plane — reject app workloads in app namespaces:

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: control-plane-no-apps
  annotations: { adhar.io/plane: control }
spec:
  validationFailureAction: Audit   # -> Enforce after migration (M5)
  background: true
  rules:
    - name: only-control-plane-workloads
      match:
        any:
          - resources:
              kinds: [Deployment, StatefulSet, DaemonSet, Rollout]
      exclude:
        any:
          - resources:
              namespaceSelector:
                matchLabels: { "adhar.io/plane": "control" }
      validate:
        message: "This is the control plane; application workloads must target a data plane (ADR-0023)."
        deny: {}
```

Control-plane namespaces (`adhar-system`, the hub, harbor) carry `adhar.io/plane: control`; anything else is denied. The vcluster syncer namespaces are labeled data-plane.

## 7. Local data plane (T1) — vcluster

`adhar up` (local) after the foundation:

1. Create namespace `dp-local` labeled `adhar.io/plane: data`.
2. Helm-template vcluster (values pin: sync ingresses off, expose via the host Gateway, storage = local-path) into `dp-local`.
3. Register the vcluster with the local ArgoCD as cluster `local` with labels `{ tier: local }`.
4. The workload appset places app packages into cluster `local` (the vcluster); the control appset keeps platform services on the host.
5. `adhar up --single` bypasses steps 1-4 (collapsed dual role) for constrained laptops.

## 8. Cross-plane connectivity (implementation)

| Dependency | Concrete wiring |
|---|---|
| OIDC | Keycloak `HTTPRoute` on the control plane; data-plane apps use the external issuer URL + `validate_server_cert:false` for self-signed (the proven console/JupyterHub pattern) |
| Secrets | Each data plane runs an ESO instance with a `ClusterSecretStore` pointing at the control-plane Vault (`https://vault.<domain>`), auth via a SPIFFE-scoped Kubernetes auth role |
| Registry | Harbor robot account per data plane, delivered as a pull secret through ESO |
| East-west | Cilium Cluster Mesh; `CiliumNetworkPolicy` gates cross-plane calls by SPIFFE identity |
| Telemetry | Alloy `remote_write`/OTLP to Mimir/Loki/Tempo ingest `HTTPRoute`s, cluster label = DataPlane name |

`adhar dataplane check <name>` runs one probe per row and prints a pass/fail table.

## 9. CLI (`cmd/dataplane/`, `cmd/migrate/`)

- `adhar get dataplanes` — table from `DataPlaneList` (Mode/Provider/Apps/Ready/Age via printcolumns).
- `adhar dataplane check <name>` — connectivity contract probes (§8).
- `adhar dataplane logs/describe <name>` — proxied through the control plane.
- `adhar migrate split-planes [--dry-run]` — drives §7 of the plan: create local/colocated vcluster data plane, re-home app packages by editing placement bindings in Git, wait for ArgoCD, flip Kyverno to Enforce. Each step is a Git commit; `--dry-run` prints the diff.

## 10. Tests

- **Unit**: `dataplane_types_test.go` (enum validation via envtest apply), deepcopy round-trip.
- **envtest** (`platform/controllers/dataplane/controller_test.go`): fake CompositeCluster (unstructured) + fake ArgoCD Application/Secret; assert condition progression InfraReady→…→Ready, finalizer teardown, app recount on Application health change.
- **parity** (`parity_test.go` additions): assert every package's `plane` label is set; no `plane: workload` element appears in `adhar-appset-control.yaml`; control and workload appsets together cover the same package set as today (superset invariant).
- **policy** (`chainsaw`/kyverno-test): app Deployment denied in a non-control namespace on the control plane; admitted in a data-plane namespace.
- **e2e T1** (`tests/e2e/bootstrap`): after `adhar up`, assert a `DataPlane` named `local` is `Ready`, a sample app's pods run in the vcluster (not `adhar-system`), and `adhar-system` has zero `plane: workload` workloads.
- **e2e T3 (live)**: a `DataPlane` provisions a cloud plane, registers, gets agents, joins mesh, receives a placed app, reports `Ready`; delete-and-recreate drill (control plane unaffected).

## 11. File inventory

```
api/v1alpha1/dataplane_types.go                         (new)
api/v1alpha1/zz_generated.deepcopy.go                   (regen)
platform/controllers/dataplane/{controller,infra,register,agents,mesh,observability,conditions}.go (new)
platform/controllers/adharplatform/resources/crds/dataplanes.yaml (new, embedded)
platform/controllers/crd.go                             (register DataPlane CRD)
cmd/controller/main.go                                  (wire DataPlaneReconciler)
cmd/dataplane/{get,check,describe}.go                   (new)
cmd/migrate/split_planes.go                             (new)
platform/stack/adhar-appset-control.yaml                (new)
platform/stack/adhar-appset-workload.yaml               (extend)
platform/stack/packages/**/ (add adhar.io/plane labels)  (edit)
platform/stack/packages/security/policy-packs/manifests/plane-isolation.yaml (new)
platform/stack/packages/core/sveltos/manifests/clusterprofiles/*.yaml (new)
platform/stack/environments/*/placement.yaml            (new)
docs/PRODUCTION.md                                       (split-plane runbook)
tests/e2e/bootstrap/bootstrap_test.go                   (T1 assertions)
```

## 12. Observability & failure modes

- Controller exposes `dataplane_reconcile_duration_seconds`, `dataplane_phase{phase,result}`, `dataplane_apps_placed` — scraped by the hub.
- Requeue matrix: infra 30s, agents 20s, mesh 30s, steady poll `defaultRequeueTime`; hard errors use `errRequeueTime` and set the phase condition `False` with the error message (mirrors `recordFailure`).
- Idempotency: every apply is SSA with `FieldManager="adhar"`; re-running a partially-failed reconcile adopts existing infra (compositeRef/argo secret checked before create).

## 13. Milestones

- **M1 — API + controller skeleton**: `DataPlane` CRD, deepcopy, controller with `mode: adopt` (register an existing kubeconfig), `adhar get dataplanes`. No provisioning yet.
- **M2 — Package split + placement**: `plane` labels, `adhar-appset-control.yaml`, extended workload appset, Sveltos placement, parity tests. Control plane app-free in CI.
- **M3 — Local vcluster data plane**: T1 provisions a vcluster data plane by default; e2e asserts app placement off the control plane.
- **M4 — Composite + vcluster infra modes**: controller drives `CompositeCluster`/vcluster; mesh + observability wiring automated; live T3 `DataPlane` reaches `Ready`.
- **M5 — Migration + enforcement**: `adhar migrate split-planes`, Kyverno Enforce, `adhar dataplane check`; docs + PRODUCTION runbook.

## 14. Risks

- Cross-plane latency/availability for identity/secrets/registry — mitigate with the connectivity contract (§8) + `dataplane check` gating.
- vcluster fidelity at T1 (some CRDs/host-networking differ) — keep the `adhar up --single` escape hatch; document known vcluster caveats.
- Placement mistakes stranding apps — parity tests + `--dry-run` migration + ArgoCD's own health gating.
- Control plane becomes a hard dependency for provisioning/identity/secrets — reinforces the Phase 1 HA/DR requirement.
