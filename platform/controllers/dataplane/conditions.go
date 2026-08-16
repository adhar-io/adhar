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

package dataplane

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"adhar-io/adhar/api/v1alpha1"
)

// dataPlaneFinalizer guards orderly teardown (deregister ArgoCD, delete
// controller-created infra) before the DataPlane object is removed.
const dataPlaneFinalizer = "dataplane.adhar.io/finalizer"

// setCond upserts a status condition on the DataPlane, stamping the observed
// generation so consumers can tell stale conditions from current ones.
func (r *DataPlaneReconciler) setCond(dp *v1alpha1.DataPlane, condType string, status metav1.ConditionStatus, reason, msg string) {
	if reason == "" {
		reason = v1alpha1.ReasonReady
	}
	meta.SetStatusCondition(&dp.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: dp.Generation,
	})
}

// fail records a hard error on the given phase condition and requeues with the
// short error backoff, mirroring the AdharPlatform recordFailure idiom.
func (r *DataPlaneReconciler) fail(ctx context.Context, dp *v1alpha1.DataPlane, condType string, cause error) (ctrl.Result, error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	r.setCond(dp, condType, metav1.ConditionFalse, v1alpha1.ReasonError, msg)
	r.setCond(dp, v1alpha1.DataPlaneReady, metav1.ConditionFalse, v1alpha1.ReasonError, msg)
	if err := r.Status().Update(ctx, dp); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: errRequeueTime}, nil
}

// progress persists the current (in-flight) conditions and requeues after the
// supplied delay so the phase can be re-evaluated.
func (r *DataPlaneReconciler) progress(ctx context.Context, dp *v1alpha1.DataPlane, after time.Duration) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, dp); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// condFrom maps an "enabled" boolean to a condition status: enabled features
// report True, disabled ones report False (skipped, not failed).
func condFrom(enabled bool) metav1.ConditionStatus {
	if enabled {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}
