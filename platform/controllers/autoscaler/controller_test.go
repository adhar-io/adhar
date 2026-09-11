package autoscaler

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// fakeScaler records the cloud-side calls a decision would have made.
type fakeScaler struct {
	upCalls    []string
	downCalls  []string
	err        error
	lastGroup  string
	lastCurren int32
}

func (f *fakeScaler) ScaleUp(_ context.Context, spec *clusterSpec, nodeGroup string, current int32) error {
	name := ""
	if spec != nil {
		name = spec.ClusterName
	}
	f.upCalls = append(f.upCalls, name)
	f.lastGroup = nodeGroup
	f.lastCurren = current
	return f.err
}

func (f *fakeScaler) ScaleDown(_ context.Context, _ *clusterSpec, nodeName string) error {
	f.downCalls = append(f.downCalls, nodeName)
	return f.err
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func platformWith(autoscaling *v1alpha1.AutoscalingSpec) *v1alpha1.AdharPlatform {
	return &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: globals.AdharSystemNamespace},
		Spec:       v1alpha1.AdharPlatformSpec{Autoscaling: autoscaling},
	}
}

func clusterSpecConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace},
		Data: map[string]string{
			"provider":    providerDigitalOcean,
			"region":      "blr1",
			"clusterName": "dev",
			"nodeGroup":   "workers",
			"size":        "s-4vcpu-8gb",
		},
	}
}

func newReconciler(t *testing.T, scaler scaler, objs ...client.Object) *Reconciler {
	t.Helper()
	s := testScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AdharPlatform{}).
		Build()
	return &Reconciler{
		Client:    c,
		Scheme:    s,
		Namespace: globals.AdharSystemNamespace,
		Scaler:    scaler,
		Now:       func() time.Time { return testNow },
	}
}

func tick(t *testing.T, r *Reconciler) {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter != tickInterval {
		t.Fatalf("expected a %s requeue, got %s", tickInterval, res.RequeueAfter)
	}
}

func loadPlatform(t *testing.T, r *Reconciler) *v1alpha1.AdharPlatform {
	t.Helper()
	p := &v1alpha1.AdharPlatform{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "adhar", Namespace: globals.AdharSystemNamespace}, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReconcileNoopWhenAutoscalingUnset(t *testing.T) {
	f := &fakeScaler{}
	n1 := worker("w1")
	pod := pending("x", "0/1 nodes are available: 1 Insufficient cpu.")
	r := newReconciler(t, f, platformWith(nil), &n1, &pod, clusterSpecConfigMap())

	tick(t, r)
	if len(f.upCalls) != 0 {
		t.Fatalf("autoscaling is not configured; no scaling must happen, got %v", f.upCalls)
	}
	if loadPlatform(t, r).Status.Autoscaling != nil {
		t.Fatal("no status should be written when autoscaling is unset")
	}
}

func TestReconcileNoopWhenDisabled(t *testing.T) {
	f := &fakeScaler{}
	n1 := worker("w1")
	pod := pending("x", "0/1 nodes are available: 1 Insufficient cpu.")
	r := newReconciler(t, f, platformWith(&v1alpha1.AutoscalingSpec{Enabled: false, MaxWorkers: 5}), &n1, &pod, clusterSpecConfigMap())

	tick(t, r)
	if len(f.upCalls) != 0 {
		t.Fatalf("disabled autoscaler must not act, got %v", f.upCalls)
	}
}

func TestReconcileScalesUpAndRecordsStatus(t *testing.T) {
	f := &fakeScaler{}
	cp := controlPlane()
	n1 := worker("w1")
	pod := pending("api", "0/2 nodes are available: 2 Insufficient memory.")
	r := newReconciler(t, f,
		platformWith(&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 4}),
		&cp, &n1, &pod, clusterSpecConfigMap())

	tick(t, r)

	if len(f.upCalls) != 1 || f.upCalls[0] != "dev" {
		t.Fatalf("expected one scale-up on cluster dev, got %v", f.upCalls)
	}
	if f.lastGroup != "workers" || f.lastCurren != 1 {
		t.Fatalf("expected node group workers at 1 current worker, got %q/%d", f.lastGroup, f.lastCurren)
	}
	st := loadPlatform(t, r).Status.Autoscaling
	if st == nil || st.LastScaleUp == nil {
		t.Fatal("expected status.autoscaling.lastScaleUp to be stamped")
	}
	if st.Workers != 1 || !st.Enabled {
		t.Fatalf("unexpected status: %+v", st)
	}
	if !strings.Contains(st.LastReason, "unschedulable") {
		t.Fatalf("status should explain the decision, got %q", st.LastReason)
	}
}

func TestReconcileScalesDownEmptiestWorker(t *testing.T) {
	f := &fakeScaler{}
	since := metav1.NewTime(testNow.Add(-30 * time.Minute))
	platform := platformWith(&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 10})
	platform.Status.Autoscaling = &v1alpha1.AutoscalingStatus{Enabled: true, UnderutilizedSince: &since}

	w1, w2 := worker("w1"), worker("w2")
	p1 := placed("apps", "a", "w1", 500, 1)
	r := newReconciler(t, f, platform, &w1, &w2, &p1, clusterSpecConfigMap())

	tick(t, r)

	if len(f.downCalls) != 1 || f.downCalls[0] != "w2" {
		t.Fatalf("expected the empty worker w2 to be removed, got %v", f.downCalls)
	}
	st := loadPlatform(t, r).Status.Autoscaling
	if st.LastScaleDown == nil {
		t.Fatal("expected status.autoscaling.lastScaleDown to be stamped")
	}
}

func TestReconcileReportsProviderFailureWithoutFailingTheTick(t *testing.T) {
	// The real scaler is used here: with no adhar-cluster-spec ConfigMap it
	// cannot reach a cloud, and that must surface as a status reason (and a
	// stamped cooldown) rather than an error that backs off onto the cloud API.
	cp := controlPlane()
	n1 := worker("w1")
	pod := pending("api", "0/2 nodes are available: 2 Insufficient cpu.")
	r := newReconciler(t, nil,
		platformWith(&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 4}),
		&cp, &n1, &pod)

	tick(t, r)

	st := loadPlatform(t, r).Status.Autoscaling
	if st == nil || !strings.Contains(st.LastReason, "ScaleUp failed") {
		t.Fatalf("expected the failure in status, got %+v", st)
	}
	if !strings.Contains(st.LastReason, globals.ClusterSpecConfigMapName) {
		t.Fatalf("the reason should name the missing ConfigMap, got %q", st.LastReason)
	}
}
