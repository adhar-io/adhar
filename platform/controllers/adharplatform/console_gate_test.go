package adharplatform

import (
	"context"
	"testing"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
)

func consoleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func consoleSlice(ready *bool) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "console-abc",
			Namespace: "adhar-system",
			Labels:    map[string]string{discoveryv1.LabelServiceName: consoleServiceName},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.244.1.7"},
			Conditions: discoveryv1.EndpointConditions{Ready: ready},
		}},
	}
}

// `adhar up` promises a URL a person can open, so the completion message waits
// on the console actually having a backend — not on Argo CD's opinion that the
// Application is Healthy, which is a verdict on managed resources and stays
// true from the last observation while a pod is evicted, rescheduling, or
// failing readiness.
func TestTheReadyGateWaitsForTheConsoleToHaveAReadyEndpoint(t *testing.T) {
	yes, no := true, false
	s := consoleScheme(t)

	for _, tc := range []struct {
		name    string
		objects []runtime.Object
		want    bool
	}{
		{"no endpointslice at all", nil, false},
		{"endpoint present but not ready", []runtime.Object{consoleSlice(&no)}, false},
		{"endpoint ready", []runtime.Object{consoleSlice(&yes)}, true},
		// The EndpointSlice spec says a nil Ready means "no opinion", to be
		// treated as ready.
		{"endpoint with no opinion", []runtime.Object{consoleSlice(nil)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(s)
			for _, o := range tc.objects {
				b = b.WithRuntimeObjects(o)
			}
			r := &AdharPlatformReconciler{Client: b.Build(), Scheme: s}
			if got := r.consoleIsServing(context.Background()); got != tc.want {
				t.Errorf("consoleIsServing() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The gate may only open on positive evidence. A client that cannot read must
// read as "not serving", or an unreachable API server ends the wait early —
// the exact failure the gate exists to prevent.
func TestAnUnreadableClusterNeverOpensTheReadyGate(t *testing.T) {
	// A scheme without the discovery types makes every List fail.
	broken := runtime.NewScheme()
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(broken).Build(),
		Scheme: broken,
	}
	if r.consoleIsServing(context.Background()) {
		t.Error("the gate opened although the endpoint could not be read; only positive " +
			"evidence may end the wait")
	}
}

// The gate must actually CONSULT the endpoint.
//
// Testing `consoleIsServing` in isolation proves the helper, not the gate:
// with the call removed from `nudgeApplications`, every such test still
// passes while `adhar up` goes back to announcing a console that may not be
// serving. This drives the real path.
func TestTheReadyGateConsultsTheEndpointAndNotOnlyTheApplication(t *testing.T) {
	ready := true
	healthyConsole := func() *argov1alpha1.Application {
		a := app(argov1alpha1.SyncStatusCodeSynced, "Healthy")
		a.Name = readyGateApp
		a.Namespace = globals.AdharSystemNamespace
		return a
	}

	s := consoleScheme(t)
	if err := argov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}

	// Console Application Synced + Healthy, but nothing is serving behind it.
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(healthyConsole()).Build(),
		Scheme: s,
	}
	got, err := r.nudgeApplications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.GatePresent {
		t.Fatal("the gate application was not seen at all")
	}
	if got.GateReady || got.Usable() {
		t.Error("the gate opened on a Healthy Application with no ready endpoint; " +
			"`adhar up` would announce a console that cannot answer")
	}

	// Same Application, now with a ready endpoint.
	r = &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).
			WithRuntimeObjects(healthyConsole(), consoleSlice(&ready)).Build(),
		Scheme: s,
	}
	got, err = r.nudgeApplications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.GateReady || !got.Usable() {
		t.Error("the gate stayed shut although the console is Healthy and has a ready endpoint")
	}
}
