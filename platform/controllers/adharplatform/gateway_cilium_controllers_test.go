package adharplatform

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubeevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// The cilium-operator restarted under an API-server outage, got an RBAC
// "forbidden" back from an authorizer that had not warmed up, and ran without
// its Gateway API controllers from then on: every HTTPRoute created afterwards
// (penpot, plane, loki) had no status for 27 minutes and its hostname reset the
// TLS handshake (AWS, 2026-10-30). These guards pin the repair — restart the
// operator when routes attached to the platform Gateway go unanswered — and
// every reason it must NOT fire.

// platformGatewayRef is a parentRef naming the platform Gateway in full.
func platformGatewayRef() map[string]any {
	return map[string]any{
		"group": gatewayAPIGroup, "kind": "Gateway",
		"name": gatewayResourceName, "namespace": globals.AdharSystemNamespace,
	}
}

func platformGatewayObject(created time.Time) *unstructured.Unstructured {
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "Gateway"})
	gw.SetName(gatewayResourceName)
	gw.SetNamespace(globals.AdharSystemNamespace)
	gw.SetCreationTimestamp(metav1.NewTime(created))
	return gw
}

// testRoute is an HTTPRoute with the given parentRefs and, when status is not
// nil, the given status.parents (an empty slice is the empty status another
// implementation stamps on a new route).
func testRoute(namespace, name string, created time.Time, refs []map[string]any, status []map[string]any) *unstructured.Unstructured {
	route := &unstructured.Unstructured{Object: map[string]any{}}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "HTTPRoute"})
	route.SetName(name)
	route.SetNamespace(namespace)
	route.SetCreationTimestamp(metav1.NewTime(created))
	parentRefs := make([]any, 0, len(refs))
	for _, ref := range refs {
		parentRefs = append(parentRefs, ref)
	}
	route.Object["spec"] = map[string]any{"parentRefs": parentRefs}
	if status != nil {
		parents := make([]any, 0, len(status))
		for _, p := range status {
			parents = append(parents, p)
		}
		route.Object["status"] = map[string]any{"parents": parents}
	}
	return route
}

// ciliumAnswer is the parent status Cilium writes, Accepted or not.
func ciliumAnswer(accepted string) map[string]any {
	return map[string]any{
		"parentRef":      platformGatewayRef(),
		"controllerName": "io.cilium/gateway-controller",
		"conditions": []any{map[string]any{
			"type": "Accepted", "status": accepted, "reason": "NotAllowedByListeners",
		}},
	}
}

// ciliumOperatorFixture is the operator Deployment as the chart renders it
// (one replica, maxUnavailable 100%) and its one pod, Ready since `readySince`.
func ciliumOperatorFixture(namespace string, readySince time.Time, available int32, annotations map[string]string) (*appsv1.Deployment, *corev1.Pod) {
	labels := map[string]string{"io.cilium/app": "operator", "name": ciliumOperatorName}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: ciliumOperatorName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations}},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: available},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: ciliumOperatorName + "-67c7d78dcf-lmb4v", Namespace: namespace, Labels: labels},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(readySince),
		}}},
	}
	return deploy, pod
}

// managedFieldsEntry is one write as the API server records it on a route.
func managedFieldsEntry(manager string, op metav1.ManagedFieldsOperationType, subresource, fields string, at metav1.Time) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{
		Manager: manager, Operation: op, Subresource: subresource, Time: &at,
		APIVersion: gatewayAPIGroup + "/v1", FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(fields)},
	}
}

func wedgePlatform() *v1alpha1.AdharPlatform {
	return &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
	}}
}

// wedgeReconciler serves objs from a fake client and records Events. The fake
// client drops managedFields from what it returns unless asked not to, and
// those are how a route's last change is dated.
func wedgeReconciler(t *testing.T, objs ...client.Object) (*AdharPlatformReconciler, *kubeevents.FakeRecorder) {
	t.Helper()
	s := providedScheme(t)
	recorder := kubeevents.NewFakeRecorder(10)
	return &AdharPlatformReconciler{
		Client:   fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithReturnManagedFields().Build(),
		Scheme:   s,
		Recorder: recorder,
	}, recorder
}

// operatorRestartStamp is the restart annotation on the operator's pod
// template, empty when it was never restarted here.
func operatorRestartStamp(t *testing.T, r *AdharPlatformReconciler, namespace string) string {
	t.Helper()
	deploy := &appsv1.Deployment{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: ciliumOperatorName, Namespace: namespace}, deploy); err != nil {
		t.Fatalf("reading %s/%s: %v", namespace, ciliumOperatorName, err)
	}
	return deploy.Spec.Template.Annotations[restartedAtAnnotation]
}

func recordedEvents(recorder *kubeevents.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-recorder.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// The incident, replayed: an operator that has been ready for an hour, three
// routes created after it came back with no answer from Cilium, and every kind
// of route that must not be counted alongside them.
func TestAnOperatorRunningWithoutItsGatewayControllersIsRestarted(t *testing.T) {
	now := time.Now()
	operator, pod := ciliumOperatorFixture(globals.AdharSystemNamespace, now.Add(-time.Hour), 1, nil)
	ns := globals.AdharSystemNamespace
	// loki's parentRef spells nothing out: group, kind and namespace all default.
	bareRef := map[string]any{"name": gatewayResourceName}
	r, recorder := wedgeReconciler(t, operator, pod, platformGatewayObject(now.Add(-2*time.Hour)),
		// agentgateway stamps an EMPTY status on every new route; that is not an answer.
		testRoute(ns, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()}, []map[string]any{}),
		testRoute(ns, "plane", now.Add(-26*time.Minute), []map[string]any{platformGatewayRef()}, nil),
		testRoute(ns, "loki", now.Add(-25*time.Minute), []map[string]any{bareRef}, nil),
		// Answered, one of them with a refusal: neither is this condition.
		testRoute(ns, "argocd", now.Add(-time.Hour), []map[string]any{platformGatewayRef()}, []map[string]any{ciliumAnswer("True")}),
		testRoute(ns, "harbor", now.Add(-time.Hour), []map[string]any{platformGatewayRef()}, []map[string]any{ciliumAnswer("False")}),
		// Another Gateway's route, and a tenant route naming a Gateway in its OWN namespace.
		testRoute(ns, "agentgateway-llm", now.Add(-time.Hour), []map[string]any{{"name": "adhar-ai-gateway"}}, nil),
		testRoute("team-a", "web", now.Add(-time.Hour), []map[string]any{bareRef}, nil),
		// Created a moment ago: Cilium has not had its chance yet.
		testRoute(ns, "brand-new", now.Add(-30*time.Second), []map[string]any{platformGatewayRef()}, nil),
	)

	if err := r.reconcileCiliumGatewayControllers(context.Background(), wedgePlatform()); err != nil {
		t.Fatalf("reconcileCiliumGatewayControllers: %v", err)
	}

	if operatorRestartStamp(t, r, ns) == "" {
		t.Fatal("the cilium-operator was not restarted: it keeps running without its Gateway API controllers " +
			"and no route created from now on is ever programmed")
	}
	events := recordedEvents(recorder)
	if len(events) != 1 {
		t.Fatalf("recorded %d Events, want exactly one saying why the operator was restarted: %q", len(events), events)
	}
	for _, want := range []string{
		"Warning", "GatewayRoutesUnanswered", "3 HTTPRoute(s)",
		"adhar-system/loki, adhar-system/penpot, adhar-system/plane)",
	} {
		if !strings.Contains(events[0], want) {
			t.Errorf("the Event does not say %q: %s", want, events[0])
		}
	}
}

// Each case is the incident with exactly ONE thing different, and that one
// thing must keep the operator running. Restarting it costs every route on
// the cluster a re-sync, so a false alarm is not free.
func TestTheCiliumOperatorIsLeftAloneUnlessARouteIsReallyUnanswered(t *testing.T) {
	now := time.Now()
	ns := globals.AdharSystemNamespace
	oldRoute := func() *unstructured.Unstructured {
		return testRoute(ns, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()}, nil)
	}
	for _, tc := range []struct {
		name       string
		route      func() *unstructured.Unstructured
		readySince time.Duration
		available  int32
		gateway    func() *unstructured.Unstructured
	}{
		{name: "the route was answered with Accepted=False", route: func() *unstructured.Unstructured {
			return testRoute(ns, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()},
				[]map[string]any{ciliumAnswer("False")})
		}},
		{name: "the route is attached to another Gateway", route: func() *unstructured.Unstructured {
			ref := platformGatewayRef()
			ref["name"] = "adhar-ai-gateway"
			return testRoute(ns, "agentgateway-llm", now.Add(-time.Hour), []map[string]any{ref}, nil)
		}},
		{name: "a tenant route names a Gateway in its own namespace", route: func() *unstructured.Unstructured {
			return testRoute("team-a", "web", now.Add(-time.Hour), []map[string]any{{"name": gatewayResourceName}}, nil)
		}},
		{name: "the route is younger than the grace", route: func() *unstructured.Unstructured {
			return testRoute(ns, "penpot", now.Add(-2*time.Minute), []map[string]any{platformGatewayRef()}, nil)
		}},
		{name: "the route changed a minute ago", route: func() *unstructured.Unstructured {
			route := oldRoute()
			changed := metav1.NewTime(now.Add(-time.Minute))
			route.SetManagedFields([]metav1.ManagedFieldsEntry{managedFieldsEntry("argocd-controller",
				metav1.ManagedFieldsOperationApply, "", `{"f:spec":{"f:parentRefs":{}}}`, changed)})
			return route
		}},
		{name: "the route is being deleted", route: func() *unstructured.Unstructured {
			route := oldRoute()
			deleting := metav1.NewTime(now.Add(-time.Minute))
			route.SetDeletionTimestamp(&deleting)
			route.SetFinalizers([]string{"example.com/hold"})
			return route
		}},
		{name: "the operator became ready a minute ago", route: oldRoute, readySince: time.Minute},
		{name: "the operator has no available replica", route: oldRoute, available: -1},
		{name: "the Gateway was created a minute ago", route: oldRoute, gateway: func() *unstructured.Unstructured {
			return platformGatewayObject(now.Add(-time.Minute))
		}},
		{name: "there is no platform Gateway", route: oldRoute, gateway: func() *unstructured.Unstructured { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readySince := time.Hour
			if tc.readySince != 0 {
				readySince = tc.readySince
			}
			available := int32(1)
			if tc.available < 0 {
				available = 0 // the pod still reads Ready: the replica count alone decides
			}
			operator, pod := ciliumOperatorFixture(ns, now.Add(-readySince), available, nil)
			objs := []client.Object{operator, pod, tc.route()}
			gateway := platformGatewayObject(now.Add(-2 * time.Hour))
			if tc.gateway != nil {
				gateway = tc.gateway()
			}
			if gateway != nil {
				objs = append(objs, gateway)
			}
			r, recorder := wedgeReconciler(t, objs...)

			if err := r.reconcileCiliumGatewayControllers(context.Background(), wedgePlatform()); err != nil {
				t.Fatalf("reconcileCiliumGatewayControllers: %v", err)
			}
			if stamp := operatorRestartStamp(t, r, ns); stamp != "" {
				t.Errorf("the cilium-operator was restarted (%s) although nothing was waiting on it", stamp)
			}
			if events := recordedEvents(recorder); len(events) != 0 {
				t.Errorf("recorded %q for a restart that must not happen", events)
			}
		})
	}
}

// A write to the route's STATUS by somebody else is not a change Cilium has to
// answer, so it must not restart the grace — agentgateway's empty stamp lands
// on every route, and it would hide the wedge for as long as that kept happening.
func TestAStatusWriteDoesNotRestartTheGrace(t *testing.T) {
	now := time.Now()
	ns := globals.AdharSystemNamespace
	operator, pod := ciliumOperatorFixture(ns, now.Add(-time.Hour), 1, nil)
	route := testRoute(ns, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()}, []map[string]any{})
	stamped := metav1.NewTime(now.Add(-10 * time.Second))
	route.SetManagedFields([]metav1.ManagedFieldsEntry{managedFieldsEntry("agentgateway",
		metav1.ManagedFieldsOperationUpdate, "status", `{"f:status":{}}`, stamped)})
	r, _ := wedgeReconciler(t, operator, pod, platformGatewayObject(now.Add(-2*time.Hour)), route)

	if err := r.reconcileCiliumGatewayControllers(context.Background(), wedgePlatform()); err != nil {
		t.Fatalf("reconcileCiliumGatewayControllers: %v", err)
	}
	if operatorRestartStamp(t, r, ns) == "" {
		t.Error("a status write by another controller reset the grace, so the wedged operator was never restarted")
	}
}

// At most one restart per interval, counting restarts anyone made — a person's
// `kubectl rollout restart` included — so a cause a restart does not cure
// costs one restart every ten minutes rather than a restart loop.
func TestCiliumOperatorRestartsAreRateLimited(t *testing.T) {
	now := time.Now()
	ns := globals.AdharSystemNamespace
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		restart     bool
	}{
		{"restarted here two minutes ago", map[string]string{
			restartedAtAnnotation: now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)}, false},
		{"restarted by kubectl two minutes ago", map[string]string{
			kubectlRestartedAtAnnotation: now.Add(-2 * time.Minute).In(time.FixedZone("SGT", 8*3600)).Format(time.RFC3339)}, false},
		{"restarted a quarter of an hour ago", map[string]string{
			restartedAtAnnotation: now.Add(-15 * time.Minute).UTC().Format(time.RFC3339)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operator, pod := ciliumOperatorFixture(ns, now.Add(-time.Hour), 1, tc.annotations)
			r, _ := wedgeReconciler(t, operator, pod, platformGatewayObject(now.Add(-2*time.Hour)),
				testRoute(ns, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()}, nil))

			if err := r.reconcileCiliumGatewayControllers(context.Background(), wedgePlatform()); err != nil {
				t.Fatalf("reconcileCiliumGatewayControllers: %v", err)
			}
			restarted := operatorRestartStamp(t, r, ns) != tc.annotations[restartedAtAnnotation]
			if restarted != tc.restart {
				t.Errorf("restarted = %v, want %v", restarted, tc.restart)
			}
		})
	}

	// And across passes: the stamp the first restart leaves is what holds the
	// second one back, even though nothing has answered the route yet.
	operator, pod := ciliumOperatorFixture(ns, now.Add(-time.Hour), 1, nil)
	r, recorder := wedgeReconciler(t, operator, pod, platformGatewayObject(now.Add(-2*time.Hour)),
		testRoute(ns, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()}, nil))
	ctx := context.Background()
	if err := r.reconcileCiliumGatewayControllers(ctx, wedgePlatform()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := operatorRestartStamp(t, r, ns)
	if first == "" {
		t.Fatal("the first pass did not restart the operator")
	}
	r.gatewayRoutesScannedAt = time.Time{} // the scan spacing is not what is under test here
	if err := r.reconcileCiliumGatewayControllers(ctx, wedgePlatform()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if events := recordedEvents(recorder); len(events) != 1 {
		t.Errorf("two passes recorded %d restarts, want one: %q", len(events), events)
	}
	if operatorRestartStamp(t, r, ns) != first {
		t.Error("the second pass restarted the operator again inside the interval")
	}
}

// Whose Cilium it is follows ReconcileCilium: the platform's own install in
// every mode, the cluster's own on a cluster whose lifecycle is ours, and never
// the Cilium of a provided cluster — that one belongs to its operator.
func TestOnlyACiliumThePlatformOwnsIsRestarted(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		mode      string
		foreign   bool
		namespace string
		restart   bool
	}{
		{"a provided cluster's own Cilium", "provided", true, foreignCiliumNamespace, false},
		{"the cluster's own Cilium, adopted on a managed cluster", "managed", true, foreignCiliumNamespace, true},
		{"the platform's own Cilium on a provided cluster that had no CNI", "provided", false, globals.AdharSystemNamespace, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operator, pod := ciliumOperatorFixture(tc.namespace, now.Add(-time.Hour), 1, nil)
			objs := []client.Object{operator, pod, clusterSpecCM(tc.mode), platformGatewayObject(now.Add(-2 * time.Hour)),
				testRoute(globals.AdharSystemNamespace, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()}, nil)}
			if tc.foreign {
				objs = append(objs, foreignCiliumDaemonSet())
			}
			r, recorder := wedgeReconciler(t, objs...)

			if err := r.reconcileCiliumGatewayControllers(context.Background(), wedgePlatform()); err != nil {
				t.Fatalf("reconcileCiliumGatewayControllers: %v", err)
			}
			restarted := operatorRestartStamp(t, r, tc.namespace) != ""
			if restarted != tc.restart {
				t.Errorf("restarted %s/%s = %v, want %v", tc.namespace, ciliumOperatorName, restarted, tc.restart)
			}
			if events := recordedEvents(recorder); (len(events) == 1) != tc.restart {
				t.Errorf("recorded %q", events)
			}
		})
	}
}

// The route scan is a live cluster-wide LIST, so it runs at most once a minute;
// a route left unanswered in between is caught by the next scan.
func TestTheRouteScanIsSpacedOut(t *testing.T) {
	now := time.Now()
	ns := globals.AdharSystemNamespace
	ctx := context.Background()
	operator, pod := ciliumOperatorFixture(ns, now.Add(-time.Hour), 1, nil)
	r, _ := wedgeReconciler(t, operator, pod, platformGatewayObject(now.Add(-2*time.Hour)),
		testRoute(ns, "penpot", now.Add(-27*time.Minute), []map[string]any{platformGatewayRef()}, nil))

	r.gatewayRoutesScannedAt = time.Now().Add(-30 * time.Second) // the last scan, half a minute ago
	if err := r.reconcileCiliumGatewayControllers(ctx, wedgePlatform()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if operatorRestartStamp(t, r, ns) != "" {
		t.Fatal("the routes were listed again within a minute of the last scan")
	}

	r.gatewayRoutesScannedAt = time.Now().Add(-gatewayRouteScanInterval - time.Second)
	if err := r.reconcileCiliumGatewayControllers(ctx, wedgePlatform()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if operatorRestartStamp(t, r, ns) == "" {
		t.Error("the scan a minute later did not catch the unanswered route")
	}
	if time.Since(r.gatewayRoutesScannedAt) > time.Minute {
		t.Error("a scan did not record itself, so the spacing never engages")
	}
}
