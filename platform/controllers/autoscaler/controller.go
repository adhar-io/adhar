/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package autoscaler implements Adhar's node autoscaler: the cluster is
// created small and grows a worker at a time when pods cannot be scheduled,
// shrinking again when the workers sit idle.
//
// Why the platform ships its own instead of the upstream cluster-autoscaler:
// Adhar provisions plain cloud VMs and bootstraps Kubernetes on them with
// kubeadm (see platform/providers/kubeadm.go). The upstream autoscaler drives
// managed node groups (ASGs, MIGs, DOKS node pools) and has no provider for a
// self-managed kubeadm cluster on raw compute — so growing the cluster is the
// same operation `adhar cluster scale` performs, and that is exactly the path
// this controller reuses.
package autoscaler

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// tickInterval is the steady-state cadence. Node capacity decisions are slow
// by nature (a kubeadm join takes minutes), so a minute of latency costs
// nothing and keeps the cloud API calls negligible.
const tickInterval = 60 * time.Second

// scaler performs the cloud-side half of a decision. It exists so the policy
// and bookkeeping can be tested without a cloud account.
type scaler interface {
	ScaleUp(ctx context.Context, spec *clusterSpec, nodeGroup string, currentWorkers int32) error
	ScaleDown(ctx context.Context, spec *clusterSpec, nodeName string) error
}

// Reconciler keeps the worker count matched to demand.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Namespace holds the AdharPlatform and the bootstrap ConfigMap/Secrets.
	Namespace string
	// PlatformName selects one AdharPlatform; empty means "the only one in the
	// namespace", which is the normal single-platform case.
	PlatformName string
	// DrainTimeout bounds the eviction phase of a scale-down (default 5m).
	DrainTimeout time.Duration

	// Scaler defaults to the real cloud-provider implementation; tests
	// substitute a fake.
	Scaler scaler
	// Now is injectable so cooldown behaviour is testable.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=platform.adhar.io,resources=adharplatforms,verbs=get;list;watch
// +kubebuilder:rbac:groups=platform.adhar.io,resources=adharplatforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch;update;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// SetupWithManager wires the controller.
//
// It watches only core types (Pods and Nodes) on purpose: a watch on a CRD
// that does not exist yet blocks the manager's cache sync and kills it at
// WaitForCacheSyncTimeout, which is why this controller can be registered
// unconditionally at bootstrap, before AdharPlatform even has a spec. The
// AdharPlatform itself is read on demand inside Reconcile.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.applyDefaults()

	// Every event maps to the same reconcile key: the decision is always about
	// the cluster as a whole, so the workqueue collapses bursts into one tick.
	toPlatform := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: r.Namespace, Name: "node-autoscaler"}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("node-autoscaler").
		// Pending pods are the scale-up signal; a pod going away frees
		// capacity and may open a scale-down.
		Watches(&corev1.Pod{}, toPlatform, builder.WithPredicates(pendingPodPredicate())).
		// Nodes are few, and their events guarantee the loop starts on an idle
		// cluster where no pod is pending (from then on the requeue keeps it
		// ticking).
		Watches(&corev1.Node{}, toPlatform).
		Complete(r)
}

// pendingPodPredicate lets through only what can change the answer: a pod
// that is (or became) Pending, and pod deletions.
func pendingPodPredicate() predicate.Predicate {
	isPending := func(o client.Object) bool {
		p, ok := o.(*corev1.Pod)
		return ok && p.Status.Phase == corev1.PodPending
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isPending(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isPending(e.ObjectNew) || isPending(e.ObjectOld) },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// applyDefaults fills in the optional wiring. Called from both SetupWithManager
// and Reconcile so a directly constructed reconciler (tests, embedding) behaves
// identically to a managed one.
func (r *Reconciler) applyDefaults() {
	if r.Namespace == "" {
		r.Namespace = globals.AdharSystemNamespace
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Scaler == nil {
		r.Scaler = &providerScaler{r: r}
	}
}

// Reconcile runs one autoscaling tick.
func (r *Reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	r.applyDefaults()

	platform, err := r.findPlatform(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if platform == nil {
		// Nothing bootstrapped yet — keep ticking quietly.
		return ctrl.Result{RequeueAfter: tickInterval}, nil
	}

	spec := platform.Spec.Autoscaling.WithDefaults()
	if platform.Spec.Autoscaling == nil || !platform.Spec.Autoscaling.Enabled {
		if platform.Status.Autoscaling != nil && platform.Status.Autoscaling.Enabled {
			// Reflect the switch-off once, then stop writing status.
			_ = r.patchStatus(ctx, platform, func(st *v1alpha1.AutoscalingStatus) {
				st.Enabled = false
				st.LastReason = "autoscaling disabled"
				st.UnderutilizedSince = nil
			})
		}
		return ctrl.Result{RequeueAfter: tickInterval}, nil
	}

	snapshot, err := r.snapshot(ctx, platform, spec)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision := Decide(snapshot.Snapshot)
	logger.V(1).Info("autoscaling tick", "action", decision.Action, "workers", decision.Workers, "reason", decision.Reason)

	now := r.Now()
	var actionErr error
	switch decision.Action {
	case ActionScaleUp:
		r.event(platform, corev1.EventTypeNormal, "ScalingUp", decision.Reason)
		// The clock starts when the attempt starts, not when it finishes:
		// provisioning takes minutes and a crash mid-flight must not let the
		// next tick buy a second machine for the same pending pods.
		if err := r.patchStatus(ctx, platform, func(st *v1alpha1.AutoscalingStatus) {
			st.LastScaleUp = &metav1.Time{Time: now}
		}); err != nil {
			return ctrl.Result{}, err
		}
		actionErr = r.Scaler.ScaleUp(ctx, snapshot.cluster, spec.NodeGroup, decision.Workers)
	case ActionScaleDown:
		r.event(platform, corev1.EventTypeNormal, "ScalingDown", decision.Reason)
		if err := r.patchStatus(ctx, platform, func(st *v1alpha1.AutoscalingStatus) {
			st.LastScaleDown = &metav1.Time{Time: now}
		}); err != nil {
			return ctrl.Result{}, err
		}
		actionErr = r.Scaler.ScaleDown(ctx, snapshot.cluster, decision.Node)
	}

	reason := decision.Reason
	if actionErr != nil {
		reason = string(decision.Action) + " failed: " + actionErr.Error()
		r.event(platform, corev1.EventTypeWarning, string(decision.Action)+"Failed", reason)
		logger.Error(actionErr, "autoscaling action failed", "action", decision.Action, "node", decision.Node)
	}

	if err := r.patchStatus(ctx, platform, func(st *v1alpha1.AutoscalingStatus) {
		st.Enabled = true
		st.Workers = decision.Workers
		st.LastReason = reason
		if decision.UnderutilizedSince != nil {
			st.UnderutilizedSince = &metav1.Time{Time: *decision.UnderutilizedSince}
		} else {
			st.UnderutilizedSince = nil
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	// A failed action is reported and retried on the next tick rather than
	// returned as an error: controller-runtime's exponential backoff would
	// otherwise hammer the cloud API with a broken credential.
	return ctrl.Result{RequeueAfter: tickInterval}, nil
}

// snapshotWithCluster bundles the decision input with the cloud facts the
// action needs.
type snapshotWithCluster struct {
	Snapshot
	cluster *clusterSpec
}

// snapshot reads the cluster state the decision is made from.
func (r *Reconciler) snapshot(ctx context.Context, platform *v1alpha1.AdharPlatform, spec v1alpha1.AutoscalingSpec) (*snapshotWithCluster, error) {
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return nil, err
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods); err != nil {
		return nil, err
	}
	rwo, err := r.readWriteOnceClaims(ctx)
	if err != nil {
		return nil, err
	}

	// A missing cluster spec is not fatal for the read-only half of the tick:
	// status still reports utilization, and the action (which needs it) fails
	// with a clear reason.
	cluster, err := loadClusterSpec(ctx, r.Client, r.Namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	var status v1alpha1.AutoscalingStatus
	if platform.Status.Autoscaling != nil {
		status = *platform.Status.Autoscaling
	}

	return &snapshotWithCluster{
		Snapshot: Snapshot{
			Spec:      spec,
			Status:    status,
			Nodes:     nodes.Items,
			Pods:      pods.Items,
			RWOClaims: rwo,
			Now:       r.Now(),
		},
		cluster: cluster,
	}, nil
}

// readWriteOnceClaims indexes the PVCs whose access mode ties them to one
// node, so a node hosting such a pod is never chosen for removal.
func (r *Reconciler) readWriteOnceClaims(ctx context.Context) (map[string]bool, error) {
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := r.List(ctx, pvcs); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, pvc := range pvcs.Items {
		for _, mode := range pvc.Spec.AccessModes {
			if mode == corev1.ReadWriteOnce || mode == corev1.ReadWriteOncePod {
				out[pvc.Namespace+"/"+pvc.Name] = true
			}
		}
	}
	return out, nil
}

// findPlatform returns the AdharPlatform this cluster is, or nil when the
// platform has not been created yet.
func (r *Reconciler) findPlatform(ctx context.Context) (*v1alpha1.AdharPlatform, error) {
	list := &v1alpha1.AdharPlatformList{}
	if err := r.List(ctx, list, client.InNamespace(r.Namespace)); err != nil {
		// The controller is registered before the platform CRDs are installed
		// at bootstrap; an unknown kind means "not yet", not a failure.
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	for i := range list.Items {
		if r.PlatformName == "" || list.Items[i].Name == r.PlatformName {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

// patchStatus applies a mutation to status.autoscaling with a merge patch, so
// concurrent status writers (the platform reconciler owns other subfields) do
// not overwrite each other.
func (r *Reconciler) patchStatus(ctx context.Context, platform *v1alpha1.AdharPlatform, mutate func(*v1alpha1.AutoscalingStatus)) error {
	base := platform.DeepCopy()
	if platform.Status.Autoscaling == nil {
		platform.Status.Autoscaling = &v1alpha1.AutoscalingStatus{}
	}
	mutate(platform.Status.Autoscaling)
	return r.Status().Patch(ctx, platform, client.MergeFrom(base))
}

func (r *Reconciler) event(platform *v1alpha1.AdharPlatform, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(platform, eventType, reason, message)
}

// providerScaler is the real implementation: it drives the cloud provider the
// cluster was created with.
type providerScaler struct{ r *Reconciler }

func (p *providerScaler) ScaleUp(ctx context.Context, spec *clusterSpec, nodeGroup string, current int32) error {
	if spec == nil {
		return errNoClusterSpec
	}
	if nodeGroup == "" {
		nodeGroup = spec.NodeGroup
	}
	return p.r.scaleUp(ctx, spec, nodeGroup, current)
}

func (p *providerScaler) ScaleDown(ctx context.Context, spec *clusterSpec, nodeName string) error {
	if spec == nil {
		return errNoClusterSpec
	}
	return p.r.scaleDown(ctx, spec, nodeName)
}
