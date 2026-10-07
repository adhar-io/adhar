package autoscaler

import (
	"context"
	"strings"
	"testing"
)

// The autoscaler must not act on a cluster the platform did not create.
//
// Scaling up means creating a cloud instance and kubeadm-joining it; scaling
// down means draining a node and deleting the instance behind it. On a provided
// cluster (clusterMode: provided) there is no such instance to create — the
// nodes belong to whoever built the cluster — so acting would bill an account
// nobody pointed this at and produce nodes the cluster's own tooling knows
// nothing about. Refused at newProvider, the single point every scaling action
// goes through, so no path can route around it.
func TestAutoscalerRefusesToActOnAProvidedCluster(t *testing.T) {
	r := &Reconciler{}
	spec := &clusterSpec{
		Provider:    "civo",
		ClusterName: "someone-elses-prod",
		ProviderConfig: map[string]interface{}{
			"clusterMode": "provided",
		},
	}

	_, err := r.newProvider(context.Background(), spec)
	if err == nil {
		t.Fatal("newProvider built a provider for a provided cluster; scaling it would create or destroy nodes the platform does not own")
	}
	if !strings.Contains(err.Error(), "provided") {
		t.Errorf("the refusal does not name the mode, so the operator cannot tell why nothing scales: %v", err)
	}
}

// …and it must still work in the modes where the cluster IS ours. A guard that
// refuses everything is not a guard.
func TestAutoscalerStillActsOnAClusterThePlatformBuilt(t *testing.T) {
	r := newReconciler(t, nil)
	spec := &clusterSpec{
		Provider:    "civo",
		ClusterName: "adhar-prod",
		ProviderConfig: map[string]interface{}{
			"clusterMode": "compute",
		},
	}

	// It fails later (no credentials Secret, no client), but it must NOT fail
	// with the provided-mode refusal.
	_, err := r.newProvider(context.Background(), spec)
	if err != nil && strings.Contains(err.Error(), "not available on a provided cluster") {
		t.Errorf("compute mode was refused as if the cluster were provided: %v", err)
	}
}
