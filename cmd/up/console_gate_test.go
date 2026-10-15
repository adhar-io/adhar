package up

import (
	"context"
	"os"
	"strings"
	"testing"

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
		err := verifyConsoleReady(ctx, gateClient(t, objs...))
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
// after the in-cluster manager is installed (so a failing run still leaves
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
		t.Error("on the cloud path the console verdict must come AFTER the in-cluster manager install, so a failed run still converges on its own")
	}
}
