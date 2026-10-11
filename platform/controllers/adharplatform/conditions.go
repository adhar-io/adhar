package adharplatform

import (
	"adhar-io/adhar/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on AdharPlatform. Every reconcile pass rewrites the
// full set from the component statuses, so consumers (CLI, Console, kubectl
// wait) always see a complete, current picture.
const (
	ConditionArgoCDReady     = "ArgoCDReady"
	ConditionGatewayReady    = "GatewayReady"
	ConditionGiteaReady      = "GiteaReady"
	ConditionCrossplaneReady = "CrossplaneReady"
	ConditionGitOpsReady     = "GitOpsReady"
	// ConditionReady is the aggregate: True once every component is ready and
	// the GitOps stack is applied. It carries the most recent reconcile error
	// as its message while False.
	ConditionReady = "Ready"
)

// Transition is a condition that CHANGED on this pass.
//
// The event bus needs transitions, not state. `syncConditions` runs on every
// reconcile — every 30 seconds once the platform is quiet — so publishing the
// condition set each time would put tens of thousands of identical events a day
// onto a topic whose whole value is that reading it tells you what happened.
// `meta.SetStatusCondition` already reports whether it changed anything; this
// collects those answers so the caller emits exactly once per change.
type Transition struct {
	Type    string
	Ready   bool
	Reason  string
	Message string
}

// syncConditions derives the condition set from the component status fields and
// returns the conditions that changed.
//
// failureReason/failureMessage, when non-empty, describe the most recent
// reconcile failure and are surfaced on the aggregate Ready condition.
func syncConditions(resource *v1alpha1.AdharPlatform, failureReason, failureMessage string) []Transition {
	gen := resource.GetGeneration()
	var changed []Transition

	set := func(condType string, ok bool, notReadyMessage string) {
		c := metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionFalse,
			Reason:             "NotReady",
			Message:            notReadyMessage,
			ObservedGeneration: gen,
		}
		if ok {
			c.Status = metav1.ConditionTrue
			c.Reason = "Ready"
			c.Message = ""
		}
		if meta.SetStatusCondition(&resource.Status.Conditions, c) {
			changed = append(changed, Transition{
				Type: condType, Ready: ok, Reason: c.Reason, Message: c.Message,
			})
		}
	}

	set(ConditionArgoCDReady, resource.Status.ArgoCD.Available,
		"ArgoCD install has not completed")
	set(ConditionGatewayReady, resource.Status.Gateway.Available,
		"Cilium Gateway is not programmed yet")
	set(ConditionGiteaReady, resource.Status.Gitea.Available,
		"Gitea is not serving its API yet")
	set(ConditionCrossplaneReady,
		resource.Status.Crossplane.Available && resource.Status.Crossplane.ControlPlaneApplied,
		"Crossplane core or control-plane configuration (XRDs/Compositions/Providers) not fully applied")
	set(ConditionGitOpsReady, resource.Status.Gitea.RepositoriesCreated,
		"GitOps repositories and ApplicationSet not created yet")

	allReady := resource.Status.ArgoCD.Available &&
		resource.Status.Gateway.Available &&
		resource.Status.Gitea.Available &&
		resource.Status.Crossplane.Available &&
		resource.Status.Crossplane.ControlPlaneApplied &&
		resource.Status.Gitea.RepositoriesCreated

	ready := metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "PlatformReady",
		Message:            "All platform components are ready",
		ObservedGeneration: gen,
	}
	if !allReady {
		ready.Status = metav1.ConditionFalse
		ready.Reason = "ComponentsNotReady"
		ready.Message = "One or more platform components are not ready"
	}
	if failureMessage != "" {
		ready.Status = metav1.ConditionFalse
		ready.Reason = failureReason
		ready.Message = failureMessage
	}
	if meta.SetStatusCondition(&resource.Status.Conditions, ready) {
		changed = append(changed, Transition{
			Type:    ConditionReady,
			Ready:   ready.Status == metav1.ConditionTrue,
			Reason:  ready.Reason,
			Message: ready.Message,
		})
	}
	return changed
}
