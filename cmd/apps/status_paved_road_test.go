package apps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// A paved-road Application as the control plane reports it (application-
// multienv), shaped like qua-service and node-svc on the DigitalOcean cluster
// on 2026-11-03: no syncStatus/healthStatus, a phase, and the project's
// environments with the Argo CD Application each one runs as.
func pavedRoadXR(name, phase, message string) *unstructured.Unstructured {
	envs := []interface{}{}
	for _, e := range []string{"dev", "test", "prod"} {
		envs = append(envs, map[string]interface{}{
			"name": e, "namespace": e, "argoApplication": "adhar-" + name + "-" + e, "stage": e,
		})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "adhar-system",
			"labels": map[string]interface{}{"adhar.io/application": name, "adhar.io/project": "adhar"},
		},
		"spec": map[string]interface{}{"parameters": map[string]interface{}{"project": "adhar"}},
		"status": map[string]interface{}{
			"phase":        phase,
			"message":      message,
			"environments": envs,
			"conditions": []interface{}{
				// Crossplane's Ready says the composed objects exist, not that the
				// app is up: node-svc was Ready=True with dev Degraded.
				map[string]interface{}{"type": "Ready", "status": "True", "reason": "Available"},
			},
		},
	}}
}

func argoApp(name, app, env, sync, health string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "adhar-system",
			"labels": map[string]interface{}{"adhar.io/application": app, "adhar.io/environment": env},
		},
		"status": map[string]interface{}{
			"sync":   map[string]interface{}{"status": sync, "revision": "9945a54a"},
			"health": map[string]interface{}{"status": health},
		},
	}}
}

func fakeCluster(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			applicationGVR:     "ApplicationList",
			argoApplicationGVR: "ApplicationList",
			projectGVR:         "ProjectList",
		}, objs...)
}

// Every template deploy — this CLI's and the Console's — registers its
// Application in adhar-system. `status`, `list` and `delete` looked in dev
// alone, so they answered "application not found: dev/qua-service" for every
// scaffolded app.
func TestStatusListAndDeleteFindPavedRoadApplicationsInAdharSystem(t *testing.T) {
	legacy := pavedRoadXR("old-app", "", "")
	legacy.SetNamespace("dev")
	dc := fakeCluster(pavedRoadXR("qua-service", "Healthy", "1/3 environments healthy"), legacy,
		argoApp("adhar-qua-service-dev", "qua-service", "dev", "Synced", "Healthy"))
	ctx := context.Background()

	defaults := applicationNamespaces(false, "dev")
	if len(defaults) != 2 || defaults[0] != "adhar-system" || defaults[1] != "dev" {
		t.Fatalf("namespaces without --namespace = %v, want adhar-system then dev", defaults)
	}
	if got := applicationNamespaces(true, "team-a"); len(got) != 1 || got[0] != "team-a" {
		t.Fatalf("an explicit --namespace is the only one asked: %v", got)
	}

	app, err := findApplication(ctx, dc, defaults, "qua-service")
	if err != nil || app.GetNamespace() != "adhar-system" {
		t.Fatalf("status/delete of a paved-road app: %v %v", app, err)
	}
	// A --repo deploy's Application without --namespace is still found in dev.
	if app, err := findApplication(ctx, dc, defaults, "old-app"); err != nil || app.GetNamespace() != "dev" {
		t.Fatalf("status/delete of a dev Application: %v %v", app, err)
	}
	_, err = findApplication(ctx, dc, defaults, "nope")
	if !errors.Is(err, ErrApplicationNotFound) || !strings.Contains(err.Error(), "adhar-system or dev") {
		t.Fatalf("a missing app must name where it was looked for: %v", err)
	}

	views, err := listApplicationsWith(ctx, dc, defaults, false, "")
	if err != nil || len(views) != 2 {
		t.Fatalf("list without --namespace: %d views, %v", len(views), err)
	}
	if views[0].Name != "qua-service" || views[0].Namespace != "adhar-system" {
		t.Fatalf("list: first row %s/%s", views[0].Namespace, views[0].Name)
	}
}

// `deploy --wait` waited for status.syncStatus/healthStatus, which the paved
// road never writes, so it could only time out; the composition's phase is
// what says the app is up.
func TestAPavedRoadApplicationIsReadyWhenItsPhaseIsHealthy(t *testing.T) {
	healthy := buildApplicationStatus(pavedRoadXR("qua-service", "Healthy",
		"1/3 environments healthy; test awaits its first promotion").Object, "adhar-system")
	if !isApplicationReady(healthy) {
		t.Fatalf("phase Healthy is not ready: %+v", healthy)
	}
	if healthy.HealthStatus != "Healthy" {
		t.Errorf("health column = %q, want the phase", healthy.HealthStatus)
	}
	pending := buildApplicationStatus(pavedRoadXR("node-svc", "Pending",
		"0/3 environments healthy; dev is Synced/Degraded").Object, "adhar-system")
	if isApplicationReady(pending) {
		t.Fatal("a Pending app (dev Degraded) counts as ready, although Crossplane says Ready=True")
	}
	// application-argocd still reports Argo CD's own state.
	legacy := &ApplicationStatusView{SyncStatus: "Synced", HealthStatus: "Healthy"}
	if !isApplicationReady(legacy) {
		t.Fatal("a synced and healthy single-environment Application is not ready")
	}

	if len(healthy.EnvironmentStatuses) != 3 || healthy.EnvironmentStatuses[0].Application != "adhar-qua-service-dev" ||
		healthy.EnvironmentStatuses[2].Namespace != "prod" {
		t.Fatalf("environments: %+v", healthy.EnvironmentStatuses)
	}
}

func TestEnvironmentsCarryTheirArgoCDState(t *testing.T) {
	dc := fakeCluster(pavedRoadXR("node-svc", "Pending", "0/3 environments healthy"),
		argoApp("adhar-node-svc-dev", "node-svc", "dev", "Synced", "Degraded"),
		argoApp("adhar-node-svc-test", "node-svc", "test", "OutOfSync", "Healthy"),
		argoApp("adhar-node-svc-prod", "node-svc", "prod", "Synced", "Healthy"))
	view, err := applicationStatusIn(context.Background(), dc, applicationNamespaces(false, ""), "node-svc")
	if err != nil {
		t.Fatal(err)
	}
	dev := view.EnvironmentStatuses[0]
	if dev.SyncStatus != "Synced" || dev.Health != "Degraded" || dev.Version != "9945a54a" {
		t.Errorf("dev = %+v", dev)
	}
	// The application's sync is the one its environments share, else the
	// first that differs.
	if view.SyncStatus != "OutOfSync" {
		t.Errorf("sync = %q, want OutOfSync (test)", view.SyncStatus)
	}

	allSynced := &ApplicationStatusView{EnvironmentStatuses: []EnvironmentStatus{{Application: "a"}, {Application: "b"}}}
	applyEnvironmentStatus(allSynced, map[string]map[string]interface{}{
		"a": argoApp("a", "x", "dev", "Synced", "Healthy").Object,
		"b": argoApp("b", "x", "test", "Synced", "Healthy").Object,
	})
	if allSynced.SyncStatus != "Synced" {
		t.Errorf("all environments Synced, application sync = %q", allSynced.SyncStatus)
	}
}

func TestATemplateScaffoldRefusesAProjectThatDoesNotExist(t *testing.T) {
	ctx := context.Background()
	project := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Project",
		"metadata": map[string]interface{}{"name": "payments", "namespace": "adhar-system"},
	}}
	dc := fakeCluster(project)
	if err := checkPavedRoadProject(ctx, dc, "payments"); err != nil {
		t.Errorf("an existing project was refused: %v", err)
	}
	if err := checkPavedRoadProject(ctx, dc, "paymnets"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a misspelt project was not refused: %v", err)
	}
	// The platform's own project appears asynchronously at bootstrap.
	if err := checkPavedRoadProject(ctx, dc, "adhar"); err != nil {
		t.Errorf("the platform project was refused: %v", err)
	}
}
