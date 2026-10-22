package adharplatform

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
)

// The platform reconciler reads its status at the start of a pass and writes
// it at the end. Fields owned by the node autoscaler and the DataPlane
// controller, written in between, must survive that write: the autoscaler's
// lastScaleUp IS its cooldown, and reverting it lets the next tick buy a
// second machine for the same pending pods.
func TestPlatformStatusWriteKeepsFieldsOtherControllersOwn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stale := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: "adhar-system"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale.DeepCopy()).
		WithStatusSubresource(&v1alpha1.AdharPlatform{}).Build()

	// Another controller writes its subtree after this reconcile read the CR.
	stamp := metav1.NewTime(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	current := &v1alpha1.AdharPlatform{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(stale), current); err != nil {
		t.Fatal(err)
	}
	current.Status.Autoscaling = &v1alpha1.AutoscalingStatus{Enabled: true, LastScaleUp: &stamp}
	current.Status.Fleet = &v1alpha1.FleetStatus{DataPlanes: 2}
	if err := c.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}

	r := &AdharPlatformReconciler{Client: c, Scheme: scheme}
	stale.Status.Gitea.Available = true // this reconciler's own finding
	r.postProcessReconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stale)}, stale)

	got := &v1alpha1.AdharPlatform{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(stale), got); err != nil {
		t.Fatal(err)
	}
	if !got.Status.Gitea.Available {
		t.Fatal("the reconciler's own status was not written")
	}
	if got.Status.Autoscaling == nil || got.Status.Autoscaling.LastScaleUp == nil {
		t.Fatal("status.autoscaling (the autoscaler's cooldown) was reverted by the platform reconciler")
	}
	if got.Status.Fleet == nil || got.Status.Fleet.DataPlanes != 2 {
		t.Fatal("status.fleet (the DataPlane roll-up) was reverted by the platform reconciler")
	}
}
