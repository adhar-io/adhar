package adharplatform

import (
	"context"
	"testing"
	"time"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	gitopshealth "github.com/cnoe-io/argocd-api/api/argo/gitops-engine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
)

func app(sync argov1alpha1.SyncStatusCode, health string, conds ...string) *argov1alpha1.Application {
	a := &argov1alpha1.Application{}
	a.Name = "x"
	a.Status.Sync.Status = sync
	a.Status.Health.Status = gitopshealth.HealthStatusCode(health)
	for _, c := range conds {
		a.Status.Conditions = append(a.Status.Conditions, argov1alpha1.ApplicationCondition{Type: argov1alpha1.ApplicationConditionType(c), Message: "boom"})
	}
	return a
}

func TestAppConvergedMeansSyncedAndHealthy(t *testing.T) {
	if !appConverged(app(argov1alpha1.SyncStatusCodeSynced, "Healthy")) {
		t.Error("Synced + Healthy is converged")
	}
	for _, a := range []*argov1alpha1.Application{
		app(argov1alpha1.SyncStatusCodeOutOfSync, "Healthy"), app(argov1alpha1.SyncStatusCodeSynced, "Progressing"),
		app(argov1alpha1.SyncStatusCodeUnknown, "Healthy"), app(argov1alpha1.SyncStatusCodeSynced, "Degraded"),
	} {
		if appConverged(a) {
			t.Errorf("%s/%s must not count as converged", a.Status.Sync.Status, a.Status.Health.Status)
		}
	}
}

func TestNeedsHardRefreshOnlyForComparisonFailuresAndRateLimited(t *testing.T) {
	now := time.Now()
	if !needsHardRefresh(app(argov1alpha1.SyncStatusCodeUnknown, "Healthy", argov1alpha1.ApplicationConditionComparisonError), now) {
		t.Error("a ComparisonError needs a hard refresh")
	}
	if !needsHardRefresh(app(argov1alpha1.SyncStatusCodeUnknown, "Healthy"), now) {
		t.Error("sync status Unknown needs a hard refresh even without a condition")
	}
	if needsHardRefresh(app(argov1alpha1.SyncStatusCodeOutOfSync, "Progressing"), now) {
		t.Error("an app that is merely syncing is left to ArgoCD")
	}
	if needsHardRefresh(app(argov1alpha1.SyncStatusCodeOutOfSync, "Degraded", "RepeatedResourceWarning"), now) {
		t.Error("warnings are not comparison failures")
	}
	recent := app(argov1alpha1.SyncStatusCodeUnknown, "Healthy", argov1alpha1.ApplicationConditionComparisonError)
	recent.Annotations = map[string]string{nudgeAnnotation: now.Add(-10 * time.Second).UTC().Format(time.RFC3339)}
	if needsHardRefresh(recent, now) {
		t.Error("an app nudged 10 s ago is not nudged again within the interval")
	}
	recent.Annotations[nudgeAnnotation] = now.Add(-2 * nudgeInterval).UTC().Format(time.RFC3339)
	if !needsHardRefresh(recent, now) {
		t.Error("an app nudged long ago is nudged again")
	}
	recent.Annotations[nudgeAnnotation] = "garbage"
	if !needsHardRefresh(recent, now) {
		t.Error("an unparsable nudge timestamp does not block the refresh")
	}
	_ = metav1.Now()
}

func TestConvergenceReportConverged(t *testing.T) {
	if (ConvergenceReport{}).Converged() {
		t.Error("no applications at all is not convergence (the ApplicationSet has not produced them yet)")
	}
	if !(ConvergenceReport{Total: 3, Healthy: 3}).Converged() || (ConvergenceReport{Total: 3, Healthy: 2}).Converged() {
		t.Error("converged means every application is healthy")
	}
}

// The convergence wait must honour its deadline even when every check fails —
// an unreachable API server used to leave `adhar up` retrying every 5 s for an
// hour instead of finishing and reporting what it last saw.
func TestDriveConvergenceStopsAtTheTimeoutWhenChecksKeepFailing(t *testing.T) {
	r := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(), AppsConvergeTimeout: time.Minute}
	r.convergeStart = time.Now().Add(-2 * time.Minute) // deadline already passed
	res, err := r.driveConvergence(context.Background(), &v1alpha1.AdharPlatform{})
	if err != nil {
		t.Fatalf("a failing check past the deadline must not error: %v", err)
	}
	if res.RequeueAfter != 0 || !r.shouldShutdown {
		t.Errorf("expected shutdown with no requeue, got requeue=%v shutdown=%v", res.RequeueAfter, r.shouldShutdown)
	}
}

func TestDriveConvergenceKeepsRetryingBeforeTheTimeout(t *testing.T) {
	r := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(), AppsConvergeTimeout: time.Hour}
	r.convergeStart = time.Now()
	res, err := r.driveConvergence(context.Background(), &v1alpha1.AdharPlatform{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 || r.shouldShutdown {
		t.Errorf("before the deadline a failing check retries and does not shut down: requeue=%v shutdown=%v", res.RequeueAfter, r.shouldShutdown)
	}
}

func TestRefreshKindPicksHardNormalOrNothing(t *testing.T) {
	now := time.Now()
	hard := argoCDApplicationAnnotationValueRefreshHard
	normal := argoCDApplicationAnnotationValueRefreshNormal

	cases := []struct {
		name string
		app  *argov1alpha1.Application
		want string
	}{
		{"comparison error → hard", app(argov1alpha1.SyncStatusCodeOutOfSync, "Healthy", argov1alpha1.ApplicationConditionComparisonError), hard},
		{"sync unknown → hard", app(argov1alpha1.SyncStatusCodeUnknown, "Healthy"), hard},
		{"idle out of sync → normal", app(argov1alpha1.SyncStatusCodeOutOfSync, "Healthy"), normal},
		{"idle out of sync but degraded → normal", app(argov1alpha1.SyncStatusCodeOutOfSync, "Degraded"), normal},
		{"converged → nothing", app(argov1alpha1.SyncStatusCodeSynced, "Healthy"), ""},
		{"synced but progressing → nothing", app(argov1alpha1.SyncStatusCodeSynced, "Progressing"), ""},
	}
	for _, c := range cases {
		if got := refreshKind(c.app, now); got != c.want {
			t.Errorf("%s: refreshKind = %q, want %q", c.name, got, c.want)
		}
	}

	// A sync already in flight is left alone — nudging restarts work in progress.
	running := app(argov1alpha1.SyncStatusCodeOutOfSync, "Progressing")
	running.Status.OperationState = &argov1alpha1.OperationState{Phase: "Running"}
	if got := refreshKind(running, now); got != "" {
		t.Errorf("an app mid-sync must not be nudged, got %q", got)
	}
	// A comparison failure is nudged even mid-sync: the comparison, not the
	// sync, is what is broken.
	brokenMidSync := app(argov1alpha1.SyncStatusCodeUnknown, "Healthy", argov1alpha1.ApplicationConditionComparisonError)
	brokenMidSync.Status.OperationState = &argov1alpha1.OperationState{Phase: "Running"}
	if got := refreshKind(brokenMidSync, now); got != hard {
		t.Errorf("a comparison failure is still hard-refreshed, got %q", got)
	}
	// Rate limit applies to both kinds.
	for _, a := range []*argov1alpha1.Application{
		app(argov1alpha1.SyncStatusCodeUnknown, "Healthy"),
		app(argov1alpha1.SyncStatusCodeOutOfSync, "Healthy"),
	} {
		a.Annotations = map[string]string{nudgeAnnotation: now.Add(-5 * time.Second).UTC().Format(time.RFC3339)}
		if got := refreshKind(a, now); got != "" {
			t.Errorf("a just-nudged app must be skipped, got %q", got)
		}
	}
}
