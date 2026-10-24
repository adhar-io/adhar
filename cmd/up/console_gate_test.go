package up

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/platform/k8s"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	gitopsengine "github.com/cnoe-io/argocd-api/api/argo/gitops-engine"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
)

func consoleApp(sync, health string) *argov1alpha1.Application {
	app := &argov1alpha1.Application{ObjectMeta: metav1.ObjectMeta{Name: "adhar-console", Namespace: globals.AdharSystemNamespace}}
	app.Status.Sync.Status = argov1alpha1.SyncStatusCode(sync)
	app.Status.Health.Status = gitopsengine.HealthStatusCode(health)
	return app
}

func consoleEndpoint(ready bool) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "console-abc", Namespace: globals.AdharSystemNamespace, Labels: map[string]string{discoveryv1.LabelServiceName: "console"}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.9"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}
}

func gateClient(t *testing.T, objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	if err := argov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := discoveryv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// `adhar up` completes only when adhar-console is Synced + Healthy AND
// serving. Both the spent apps budget and an unreadable platform status used
// to fall through to success (DigitalOcean + Civo, 2026-10-15).
func TestAdharUpDoesNotCompleteUntilTheConsoleIsHealthyAndServing(t *testing.T) {
	ctx := context.Background()
	if err := verifyConsoleReady(ctx, gateClient(t, consoleApp("Synced", "Healthy"), consoleEndpoint(true))); err != nil {
		t.Fatalf("a Healthy, serving console must complete the run: %v", err)
	}
	for name, objs := range map[string][]client.Object{
		"console Degraded":           {consoleApp("Synced", "Degraded"), consoleEndpoint(true)},
		"console OutOfSync":          {consoleApp("OutOfSync", "Healthy"), consoleEndpoint(true)},
		"console has no endpoint":    {consoleApp("Synced", "Healthy")},
		"console endpoint not ready": {consoleApp("Synced", "Healthy"), consoleEndpoint(false)},
		"console app missing":        {},
	} {
		err := verifyConsoleReadyWithin(ctx, gateClient(t, objs...), 0, time.Millisecond)
		if err == nil {
			t.Errorf("%s: adhar up reported completion", name)
			continue
		}
		if !strings.Contains(err.Error(), "adhar-console") || !strings.Contains(err.Error(), "re-run `adhar up`") {
			t.Errorf("%s: the failure does not name the console and the resumable re-run: %v", name, err)
		}
	}
}

// Every exit of the wait must pass through the verdict — on the cloud path
// after the adhar-controlplane manager is installed (so a failing run still leaves
// the cluster converging) and on the local path.
func TestEveryBringUpPathEndsWithTheConsoleVerdict(t *testing.T) {
	for _, f := range []string{"bootstrap.go", "local.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "verifyConsoleReady(context.Background(), kubeClient)") {
			t.Errorf("%s does not end its wait with verifyConsoleReady; a spent apps budget prints success over an unhealthy console", f)
		}
	}
	b, _ := os.ReadFile("bootstrap.go")
	src := string(b)
	install := strings.Index(src, "controllers.EnsureControllerManager(")
	verdict := strings.Index(src, "verifyConsoleReady(context.Background(), kubeClient)")
	if install < 0 || verdict < install {
		t.Error("on the cloud path the console verdict must come AFTER the adhar-controlplane manager install, so a failed run still converges on its own")
	}
}

// The CLI reads the gate with the platform's shared scheme (k8s.GetScheme). On
// 2026-10-16 that scheme had no discovery.k8s.io/v1, the EndpointSlice list
// failed with "no kind is registered", and the verdict said "not serving"
// against a console whose endpoint was ready. The gate must work with the
// scheme the bring-up actually uses, and a read failure must be reported as
// one, never as "not serving".
func TestTheVerdictReadsEndpointsWithTheBringUpScheme(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.GetScheme()).WithObjects(consoleApp("Synced", "Healthy"), consoleEndpoint(true)).Build()
	if err := verifyConsoleReady(context.Background(), c); err != nil {
		t.Fatalf("with the bring-up's own scheme a Healthy, serving console must complete the run: %v", err)
	}
	// A scheme that cannot list EndpointSlices is a caller problem and must say so.
	s := runtime.NewScheme()
	if err := argov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	blind := fake.NewClientBuilder().WithScheme(s).WithObjects(consoleApp("Synced", "Healthy")).Build()
	err := verifyConsoleReadyWithin(context.Background(), blind, 0, time.Millisecond)
	if err == nil {
		t.Fatal("a client that cannot read endpoints reported completion")
	}
	if strings.Contains(err.Error(), "not serving") || !strings.Contains(err.Error(), "could not be determined") {
		t.Errorf("a read failure was reported as the console's fault: %v", err)
	}
}

// The verdict waits for a console that is still settling instead of failing on
// the first read: the PostSync restart leaves it Progressing for a minute.
func TestTheVerdictWaitsForAConsoleThatIsStillSettling(t *testing.T) {
	app := consoleApp("Synced", "Progressing")
	c := gateClient(t, app, consoleEndpoint(true))
	go func() {
		time.Sleep(150 * time.Millisecond)
		fresh := &argov1alpha1.Application{}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(app), fresh); err != nil {
			t.Error(err)
			return
		}
		fresh.Status.Health.Status = gitopsengine.HealthStatusCode("Healthy")
		// The fake client has no status subresource registered here, so status travels with Update.
		if err := c.Update(context.Background(), fresh); err != nil {
			t.Error(err)
		}
	}()
	if err := verifyConsoleReadyWithin(context.Background(), c, 5*time.Second, 50*time.Millisecond); err != nil {
		t.Fatalf("a console that became Healthy within the grace failed the run: %v", err)
	}
	if err := verifyConsoleReadyWithin(context.Background(), gateClient(t, consoleApp("Synced", "Progressing"), consoleEndpoint(true)), 200*time.Millisecond, 50*time.Millisecond); err == nil {
		t.Fatal("a console that never settles within the grace reported completion")
	}
}

// A Healthy console whose sync operation (hooks included) is still running is
// not settled: its PostSync hook is about to restart it.
func TestTheVerdictDoesNotTrustAHealthyConsoleMidOperation(t *testing.T) {
	app := consoleApp("Synced", "Healthy")
	app.Status.OperationState = &argov1alpha1.OperationState{Phase: gitopsengine.OperationRunning}
	if err := verifyConsoleReadyWithin(context.Background(), gateClient(t, app, consoleEndpoint(true)), 0, time.Millisecond); err == nil {
		t.Fatal("a console with a sync operation in flight completed the run")
	}
}
