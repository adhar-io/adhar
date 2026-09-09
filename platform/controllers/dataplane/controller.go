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

// Package dataplane implements the DataPlane controller — one control plane
// reconciles N data planes through a phase pipeline (infra → register →
// agents → mesh → observability → ready). See
// docs/design/0023-control-dataplane-separation.md.
package dataplane

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
)

const (
	// defaultRequeueTime is the steady-state poll cadence once a DataPlane is
	// Ready; errRequeueTime is the short backoff after a hard error. These
	// mirror the AdharPlatform reconciler's own (unexported) constants.
	defaultRequeueTime = time.Second * 30
	errRequeueTime     = time.Second * 5

	// controlPlaneNamespace is where the control plane hosts ArgoCD cluster
	// secrets, CompositeCluster XRs, vcluster releases and kubeconfig secrets.
	controlPlaneNamespace = "adhar-system"

	// argoClusterSecretTypeLabel marks a Secret as an ArgoCD cluster credential.
	argoClusterSecretTypeLabel = "argocd.argoproj.io/secret-type"
	argoClusterSecretTypeValue = "cluster"
)

// argoApplicationGVK identifies ArgoCD Applications without taking a typed
// dependency on the argo API — reads and watches use unstructured objects.
var argoApplicationGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: apiVersionV1alpha1, Kind: "Application"}
var argoApplicationListGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: apiVersionV1alpha1, Kind: "ApplicationList"}

// compositeClusterGVK is the Crossplane XR the controller authors for
// mode=composite data planes (owned for garbage collection).
var compositeClusterGVK = schema.GroupVersionKind{Group: "platform.adhar.io", Version: apiVersionV1alpha1, Kind: "CompositeCluster"}

// DataPlaneReconciler reconciles a DataPlane object.
type DataPlaneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.adhar.io,resources=dataplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.adhar.io,resources=dataplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.adhar.io,resources=dataplanes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch

// Reconcile runs the DataPlane phase pipeline. Each phase gates the next: a
// not-ready phase sets its condition False and requeues; a satisfied phase sets
// it True and proceeds. See the state machine in the design doc §2.1.
func (r *DataPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling DataPlane", "resource", req.NamespacedName)

	dp := &v1alpha1.DataPlane{}
	if err := r.Get(ctx, req.NamespacedName, dp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Finalizer for orderly teardown (deregister ArgoCD, delete
	// CompositeCluster/vcluster for controller-created infra only).
	if !dp.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, dp)
	}
	if !controllerutil.ContainsFinalizer(dp, dataPlaneFinalizer) {
		controllerutil.AddFinalizer(dp, dataPlaneFinalizer)
		return ctrl.Result{Requeue: true}, r.Update(ctx, dp)
	}

	// Phase 1 — infra: obtain a client to the data plane once reachable.
	kube, ready, err := r.ensureInfra(ctx, dp)
	if err != nil {
		return r.fail(ctx, dp, v1alpha1.DataPlaneInfraReady, err)
	}
	if !ready {
		r.setCond(dp, v1alpha1.DataPlaneInfraReady, metav1.ConditionFalse, v1alpha1.ReasonProvisioning, "waiting for infra")
		return r.progress(ctx, dp, 30*time.Second)
	}
	r.setCond(dp, v1alpha1.DataPlaneInfraReady, metav1.ConditionTrue, v1alpha1.ReasonInfraReady, "cluster reachable")

	// Phase 2 — register with ArgoCD (create/patch cluster secret + labels).
	argoName, err := r.ensureArgoRegistration(ctx, dp, kube)
	if err != nil {
		return r.fail(ctx, dp, v1alpha1.DataPlaneRegistered, err)
	}
	dp.Status.ArgoCDCluster = argoName
	r.setCond(dp, v1alpha1.DataPlaneRegistered, metav1.ConditionTrue, v1alpha1.ReasonReady, "registered")

	// Phase 3 — thin-agent profile healthy on the data plane.
	if ok, err := r.ensureAgents(ctx, dp); err != nil {
		return r.fail(ctx, dp, v1alpha1.DataPlaneAgentsReady, err)
	} else if !ok {
		r.setCond(dp, v1alpha1.DataPlaneAgentsReady, metav1.ConditionFalse, v1alpha1.ReasonAgentsProgressing, "agents converging")
		return r.progress(ctx, dp, 20*time.Second)
	}
	r.setCond(dp, v1alpha1.DataPlaneAgentsReady, metav1.ConditionTrue, v1alpha1.ReasonReady, "agents healthy")

	// Phase 4 — mesh (optional).
	if dp.Spec.Mesh.Enabled {
		if ok, err := r.ensureMesh(ctx, dp, kube); err != nil {
			return r.fail(ctx, dp, v1alpha1.DataPlaneMeshJoined, err)
		} else if !ok {
			r.setCond(dp, v1alpha1.DataPlaneMeshJoined, metav1.ConditionFalse, v1alpha1.ReasonMeshConnecting, "mesh connecting")
			return r.progress(ctx, dp, 30*time.Second)
		}
	}
	r.setCond(dp, v1alpha1.DataPlaneMeshJoined, condFrom(dp.Spec.Mesh.Enabled), v1alpha1.ReasonReady, "")

	// Phase 5 — observability hub wiring.
	if err := r.ensureObservability(ctx, dp, kube); err != nil {
		return r.fail(ctx, dp, v1alpha1.DataPlaneObservabilityWired, err)
	}
	r.setCond(dp, v1alpha1.DataPlaneObservabilityWired, metav1.ConditionTrue, v1alpha1.ReasonReady, "")

	// Aggregate.
	dp.Status.AppCount = r.countPlacedApps(ctx, argoName)
	r.setCond(dp, v1alpha1.DataPlaneReady, metav1.ConditionTrue, v1alpha1.ReasonReady, "data plane ready")
	dp.Status.ObservedGeneration = dp.Generation
	if err := r.Status().Update(ctx, dp); err != nil {
		return ctrl.Result{}, err
	}
	r.rollUpFleet(ctx)
	return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
}

// rollUpFleet writes the fleet summary (every DataPlane, its readiness and
// placed-app count) onto the control plane's AdharPlatform status so
// `adhar get status` and the console see the whole fleet in one place.
// Best-effort: a missing AdharPlatform (envtest) is not an error.
func (r *DataPlaneReconciler) rollUpFleet(ctx context.Context) {
	logger := log.FromContext(ctx)
	platforms := &v1alpha1.AdharPlatformList{}
	if err := r.List(ctx, platforms, client.InNamespace(controlPlaneNamespace)); err != nil || len(platforms.Items) == 0 {
		return
	}
	dps := &v1alpha1.DataPlaneList{}
	if err := r.List(ctx, dps); err != nil {
		return
	}
	fleet := &v1alpha1.FleetStatus{DataPlanes: len(dps.Items)}
	for i := range dps.Items {
		d := &dps.Items[i]
		ready := false
		for _, c := range d.Status.Conditions {
			if c.Type == string(v1alpha1.DataPlaneReady) && c.Status == metav1.ConditionTrue {
				ready = true
			}
		}
		if ready {
			fleet.Ready++
		}
		fleet.Planes = append(fleet.Planes, v1alpha1.FleetPlane{
			Name:              d.Name,
			Mode:              string(d.Spec.Infrastructure.Mode),
			Ready:             ready,
			Apps:              d.Status.AppCount,
			KubernetesVersion: d.Status.KubernetesVersion,
		})
	}
	platform := &platforms.Items[0]
	patch := client.MergeFrom(platform.DeepCopy())
	platform.Status.Fleet = fleet
	if err := r.Status().Patch(ctx, platform, patch); err != nil {
		logger.V(1).Info("fleet roll-up not recorded", "error", err.Error())
	}
}

// finalize deregisters the ArgoCD cluster secret and (for controller-created
// infra only, never adopt) deletes the CompositeCluster/vcluster, then removes
// the finalizer.
func (r *DataPlaneReconciler) finalize(ctx context.Context, dp *v1alpha1.DataPlane) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Retire the plane's Applications while ArgoCD can still reach the
	// cluster: once the cluster secret is gone their resources-finalizer can
	// never complete and every app hangs in Terminating.
	if pending, err := r.deletePlacedApps(ctx, dp); err != nil {
		logger.Error(err, "deleting the plane's Applications during finalize")
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	} else if pending > 0 {
		logger.Info("waiting for the plane's Applications to finish deleting", "pending", pending)
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	}

	// Deregister ArgoCD cluster secret (best-effort; ignore if already gone).
	if err := r.deleteArgoRegistration(ctx, dp); err != nil {
		logger.Error(err, "deregistering ArgoCD cluster secret during finalize")
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	}

	// Delete controller-created infra only — an adopted cluster is never torn
	// down by us.
	if dp.Spec.Infrastructure.Mode != v1alpha1.InfraModeAdopt {
		if err := r.deleteManagedInfra(ctx, dp); err != nil {
			logger.Error(err, "deleting managed infra during finalize")
			return ctrl.Result{RequeueAfter: errRequeueTime}, nil
		}
	}

	controllerutil.RemoveFinalizer(dp, dataPlaneFinalizer)
	if err := r.Update(ctx, dp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	r.rollUpFleet(ctx)
	return ctrl.Result{}, nil
}

// countPlacedApps counts ArgoCD Applications whose destination targets the
// given cluster. Tolerant of the Application CRD being absent (envtest).
func (r *DataPlaneReconciler) countPlacedApps(ctx context.Context, argoName string) int {
	if argoName == "" {
		return 0
	}
	apps := &unstructured.UnstructuredList{}
	apps.SetGroupVersionKind(argoApplicationListGVK)
	if err := r.List(ctx, apps, client.InNamespace(controlPlaneNamespace)); err != nil {
		return 0
	}
	count := 0
	for i := range apps.Items {
		if applicationTargetsCluster(&apps.Items[i], argoName) {
			count++
		}
	}
	return count
}

// SetupWithManager wires the controller: reconcile DataPlanes, own the
// CompositeCluster XRs it authors, and recount placed apps when Application
// health changes.
// SetupWithManager wires the controller. It deliberately watches only core
// types: the CompositeCluster XRD and the ArgoCD Application CRD are installed
// late in (or after) bootstrap, and a watch on a not-yet-registered kind blocks
// the manager's cache sync and kills it at WaitForCacheSyncTimeout — which is
// how an early version left ArgoCD empty on fresh `adhar up`. Progress on the
// managed infra is picked up by the reconcile's own requeue cadence instead.
func (r *DataPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DataPlane{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// deletePlacedApps deletes every ArgoCD Application generated for this plane
// (label adhar.io/cluster=<name>) and returns how many still exist. Tolerant
// of the Application CRD being absent (envtest).
func (r *DataPlaneReconciler) deletePlacedApps(ctx context.Context, dp *v1alpha1.DataPlane) (int, error) {
	apps := &unstructured.UnstructuredList{}
	apps.SetGroupVersionKind(argoApplicationListGVK)
	if err := r.List(ctx, apps, client.InNamespace(controlPlaneNamespace), client.MatchingLabels{clusterLabelKey: dp.Name}); err != nil {
		if isMissingKind(err) {
			return 0, nil
		}
		return 0, err
	}
	pending := 0
	for i := range apps.Items {
		app := &apps.Items[i]
		pending++
		if !app.GetDeletionTimestamp().IsZero() {
			continue
		}
		if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
			return pending, err
		}
	}
	return pending, nil
}
