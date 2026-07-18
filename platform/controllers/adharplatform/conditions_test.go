package adharplatform

import (
	"testing"

	"adhar-io/adhar/api/v1alpha1"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSyncConditions(t *testing.T) {
	res := &v1alpha1.AdharPlatform{}

	// Nothing ready: every condition False, Ready reports components not ready.
	syncConditions(res, "", "")
	assert.Len(t, res.Status.Conditions, 6)
	for _, c := range res.Status.Conditions {
		assert.Equal(t, metav1.ConditionFalse, c.Status, "condition %s", c.Type)
	}
	ready := meta.FindStatusCondition(res.Status.Conditions, ConditionReady)
	assert.NotNil(t, ready)
	assert.Equal(t, "ComponentsNotReady", ready.Reason)

	// A reconcile failure surfaces on the Ready condition.
	syncConditions(res, "CorePackageInstallFailed", "applying ArgoCD install: boom")
	ready = meta.FindStatusCondition(res.Status.Conditions, ConditionReady)
	assert.Equal(t, "CorePackageInstallFailed", ready.Reason)
	assert.Equal(t, "applying ArgoCD install: boom", ready.Message)

	// Everything ready: all conditions True.
	res.Status.ArgoCD.Available = true
	res.Status.Gateway.Available = true
	res.Status.Gitea.Available = true
	res.Status.Gitea.RepositoriesCreated = true
	res.Status.Crossplane.Available = true
	res.Status.Crossplane.ControlPlaneApplied = true
	syncConditions(res, "", "")
	for _, c := range res.Status.Conditions {
		assert.Equal(t, metav1.ConditionTrue, c.Status, "condition %s", c.Type)
	}
	ready = meta.FindStatusCondition(res.Status.Conditions, ConditionReady)
	assert.Equal(t, "PlatformReady", ready.Reason)

	// Partial readiness: component condition goes False again, Ready follows.
	res.Status.Crossplane.ControlPlaneApplied = false
	syncConditions(res, "", "")
	xp := meta.FindStatusCondition(res.Status.Conditions, ConditionCrossplaneReady)
	assert.Equal(t, metav1.ConditionFalse, xp.Status)
	ready = meta.FindStatusCondition(res.Status.Conditions, ConditionReady)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
}
