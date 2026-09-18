package adharplatform

import (
	"context"
	"fmt"
	"sort"
	"time"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// GitOps convergence driver.
//
// Measured on a fresh local cluster: 19 of 32 platform Applications failed
// their FIRST comparison on a transient DNS miss for the Gitea Service
// ("lookup gitea-http… no such host"), and ArgoCD then left them at
// `Unknown`/ComparisonError — it does not retry a failed comparison until its
// periodic resync (timeout.reconciliation, 5 min ± jitter), and even then the
// repo-server may hand back the cached error. Because CNPG, external-secrets
// and Keycloak were among them, every app that depends on a database, a
// secret or SSO waited too. A hard refresh clears such an app in seconds.
//
// nudgeApplications is that hard refresh, applied only where it is needed
// (a ComparisonError condition, or sync status Unknown), at most once per
// nudgeInterval per Application, and it reports how far the platform has
// converged so the CLI can show "n/m apps healthy" and wait for it.

const (
	// nudgeAnnotation records when this controller last refreshed an app.
	nudgeAnnotation = "adhar.io/last-nudge"
	// nudgeInterval bounds how often one Application is refreshed.
	nudgeInterval = 30 * time.Second
	// argoCDApplicationAnnotationValueRefreshHard bypasses the repo-server's
	// manifest cache, which is what makes a cached comparison error go away.
	argoCDApplicationAnnotationValueRefreshHard = "hard"
	// DefaultAppsConvergeTimeout bounds how long `adhar up` waits for every
	// platform Application to be Synced + Healthy before exiting anyway.
	DefaultAppsConvergeTimeout = 15 * time.Minute
	// maxPendingListed caps the names carried on the status.
	maxPendingListed = 12
)

// ConvergenceReport summarises one pass over the platform Applications.
type ConvergenceReport struct {
	Total     int
	Healthy   int
	Refreshed int
	Pending   []string
}

// Converged reports whether every Application is Synced and Healthy.
func (c ConvergenceReport) Converged() bool {
	return c.Total > 0 && c.Healthy == c.Total
}

// appConverged is "Synced + Healthy" — the state the CLI waits for.
func appConverged(app *argov1alpha1.Application) bool {
	return app.Status.Sync.Status == argov1alpha1.SyncStatusCodeSynced &&
		string(app.Status.Health.Status) == "Healthy"
}

// refreshKind says whether an Application needs a nudge, and which one.
//
//   - "hard": a comparison failure (sync status Unknown, or a ComparisonError
//     condition). Only a cache-bypassing refresh clears it.
//   - "normal": the app is OutOfSync with no operation running. ArgoCD would
//     re-compare it on its own, but only after timeout.reconciliation (+ jitter)
//     — so an app that needs two sync passes (the CRDs, then the CRs that need
//     them) idles for minutes. A normal refresh re-compares now and selfHeal
//     syncs immediately.
//   - "": leave it alone (a sync is in flight, or it has converged).
//
// Either way, at most one nudge per nudgeInterval per Application.
func refreshKind(app *argov1alpha1.Application, now time.Time) string {
	kind := ""
	switch {
	case comparisonFailed(app):
		kind = argoCDApplicationAnnotationValueRefreshHard
	case app.Status.Sync.Status == argov1alpha1.SyncStatusCodeOutOfSync && !operationRunning(app):
		kind = argoCDApplicationAnnotationValueRefreshNormal
	default:
		return ""
	}
	if last, ok := app.Annotations[nudgeAnnotation]; ok {
		if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < nudgeInterval {
			return ""
		}
	}
	return kind
}

// operationRunning reports whether a sync is in flight for the Application;
// nudging one mid-sync only restarts work that is already progressing.
func operationRunning(app *argov1alpha1.Application) bool {
	if app.Operation != nil {
		return true
	}
	op := app.Status.OperationState
	return op != nil && op.Phase == "Running"
}

// comparisonFailed reports whether ArgoCD could not compare the Application
// against Git at all.
func comparisonFailed(app *argov1alpha1.Application) bool {
	if app.Status.Sync.Status == argov1alpha1.SyncStatusCodeUnknown {
		return true
	}
	for _, c := range app.Status.Conditions {
		if c.Type == argov1alpha1.ApplicationConditionComparisonError {
			return true
		}
	}
	return false
}

// needsHardRefresh reports whether an Application is stuck on a comparison
// failure that only a refresh will clear, and has not been nudged within
// nudgeInterval.
func needsHardRefresh(app *argov1alpha1.Application, now time.Time) bool {
	if !comparisonFailed(app) {
		return false
	}
	if last, ok := app.Annotations[nudgeAnnotation]; ok {
		if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < nudgeInterval {
			return false
		}
	}
	return true
}

// nudgeApplications refreshes the stuck Applications and reports convergence.
func (r *AdharPlatformReconciler) nudgeApplications(ctx context.Context) (ConvergenceReport, error) {
	logger := log.FromContext(ctx)
	apps := &argov1alpha1.ApplicationList{}
	if err := r.Client.List(ctx, apps, client.InNamespace(globals.AdharSystemNamespace)); err != nil {
		return ConvergenceReport{}, fmt.Errorf("listing ArgoCD applications: %w", err)
	}
	now := time.Now()
	report := ConvergenceReport{Total: len(apps.Items)}
	for i := range apps.Items {
		app := &apps.Items[i]
		if appConverged(app) {
			report.Healthy++
			continue
		}
		report.Pending = append(report.Pending, app.Name)
		kind := refreshKind(app, now)
		if kind == "" {
			continue
		}
		ann := app.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[argoCDApplicationAnnotationKeyRefresh] = kind
		ann[nudgeAnnotation] = now.UTC().Format(time.RFC3339)
		app.SetAnnotations(ann)
		if err := r.Client.Update(ctx, app); err != nil {
			logger.Info("Could not refresh stuck application; will retry", "app", app.Name, "error", err)
			continue
		}
		report.Refreshed++
		logger.Info("Refreshed application to move its sync along", "app", app.Name, "refresh", kind)
	}
	sort.Strings(report.Pending)
	return report, nil
}

// publishConvergence records the report on the platform status so the CLI's
// checklist can show progress; the status write is best effort.
func (r *AdharPlatformReconciler) publishConvergence(ctx context.Context, resource *v1alpha1.AdharPlatform, report ConvergenceReport) {
	pending := report.Pending
	if len(pending) > maxPendingListed {
		pending = append(append([]string{}, pending[:maxPendingListed]...), fmt.Sprintf("+%d more", len(report.Pending)-maxPendingListed))
	}
	resource.Status.GitOps = &v1alpha1.GitOpsSyncStatus{
		ApplicationsTotal:   report.Total,
		ApplicationsHealthy: report.Healthy,
		Pending:             pending,
	}
	if err := r.Status().Update(ctx, resource); err != nil {
		log.FromContext(ctx).V(1).Info("could not publish GitOps convergence status", "error", err)
	}
}

// driveConvergence is the ExitOnSync tail of the reconcile once the foundation
// is up and the ApplicationSet applied: refresh the Applications ArgoCD left
// on a comparison error, publish progress, and shut the bootstrap controller
// down only when every app is Synced + Healthy or AppsConvergeTimeout has
// passed (ArgoCD keeps converging in the background either way).
func (r *AdharPlatformReconciler) driveConvergence(ctx context.Context, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if r.convergeStart.IsZero() {
		r.convergeStart = time.Now()
		logger.Info("Driving platform applications to Synced + Healthy", "timeout", r.AppsConvergeTimeout)
	}
	expired := time.Since(r.convergeStart) >= r.AppsConvergeTimeout
	report, err := r.nudgeApplications(ctx)
	if err != nil {
		// The deadline is checked on THIS path too: when the API server itself
		// becomes unreachable (an overloaded local cluster), every convergence
		// check fails, and a bare retry here kept `adhar up` hanging long past
		// the timeout instead of finishing and reporting what it saw.
		if expired {
			logger.Info("⏳ GitOps convergence could not be evaluated before the timeout; ArgoCD continues in the background", "error", err)
			r.shouldShutdown = true
			return ctrl.Result{}, nil
		}
		logger.Info("GitOps convergence check failed; retrying", "error", err)
		return ctrl.Result{RequeueAfter: errRequeueTime}, nil
	}
	r.publishConvergence(ctx, resource, report)
	switch {
	case report.Converged():
		logger.Info("✅ Platform GitOps sync complete: every application is Synced and Healthy", "apps", report.Total)
	case expired:
		logger.Info("⏳ Platform GitOps sync still converging at the timeout; ArgoCD continues in the background",
			"healthy", report.Healthy, "total", report.Total, "pending", report.Pending)
	default:
		logger.Info("⏳ Platform GitOps sync converging", "healthy", report.Healthy, "total", report.Total, "refreshed", report.Refreshed)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	r.shouldShutdown = true
	return ctrl.Result{}, nil
}
