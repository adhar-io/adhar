package adharplatform

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"adhar-io/adhar/api/v1alpha1"
)

// Status-only writes (this reconciler's own, the autoscaler's every tick, the
// DataPlane roll-up) must not queue another full platform pass; spec and
// annotation changes (the CLI's re-trigger) must.
func TestPlatformEventsIgnoreStatusOnlyWrites(t *testing.T) {
	f := platformEventFilter()
	base := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: "adhar-system", Generation: 3, ResourceVersion: "10",
		Annotations: map[string]string{v1alpha1.CliStartTimeAnnotation: "t1"},
	}}

	statusOnly := base.DeepCopy()
	statusOnly.ResourceVersion = "11"
	statusOnly.Status.Autoscaling = &v1alpha1.AutoscalingStatus{Enabled: true, LastReason: "tick"}
	if f.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: statusOnly}) {
		t.Error("a status-only write queued a full platform reconcile")
	}

	spec := base.DeepCopy()
	spec.Generation = 4
	if !f.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: spec}) {
		t.Error("a spec change was dropped")
	}

	retrigger := base.DeepCopy()
	retrigger.Annotations = map[string]string{v1alpha1.CliStartTimeAnnotation: "t2"}
	if !f.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: retrigger}) {
		t.Error("the CLI's start-time re-trigger was dropped")
	}
	if !f.Create(event.CreateEvent{Object: base}) {
		t.Error("creation was dropped")
	}
}
