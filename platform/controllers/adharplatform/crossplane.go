package adharplatform

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/controlplane"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed resources/crossplane
var crossplaneFS embed.FS

// ReconcileCrossplane installs Crossplane core and applies the control plane configuration
func (r *AdharPlatformReconciler) ReconcileCrossplane(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Crossplane control plane")

	// Step 1: Install Crossplane core
	manifestPath := "resources/crossplane/install.yaml"
	manifestBytes, err := crossplaneFS.ReadFile(manifestPath)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading crossplane manifest: %w", err)
	}

	if err := r.applyManifest(ctx, manifestBytes, resource, "Crossplane install"); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying crossplane manifest: %w", err)
	}

	// Step 2: Wait for Crossplane deployment to be ready
	logger.Info("Waiting for Crossplane deployment to be ready...")
	for i := 0; i < 30; i++ {
		var dep appsv1.Deployment
		err := r.Get(ctx, types.NamespacedName{
			Name:      "crossplane",
			Namespace: globals.AdharSystemNamespace,
		}, &dep)
		if err == nil && dep.Status.ReadyReplicas > 0 {
			logger.Info("Crossplane deployment is ready")
			break
		}
		if i == 29 {
			logger.Info("Crossplane not fully ready yet, continuing")
		}
		time.Sleep(10 * time.Second)
	}

	resource.Status.Crossplane.Available = true

	// Step 3: Apply the control plane configuration (XRDs, Compositions, ProviderConfigs)
	if !resource.Status.Crossplane.ControlPlaneApplied {
		if err := r.applyControlPlaneConfiguration(ctx, resource); err != nil {
			logger.Info("Failed to apply control plane configuration (will retry)", "error", err)
			// Don't fail the reconciliation - Crossplane CRDs may not be ready yet
			return ctrl.Result{}, nil
		}
		resource.Status.Crossplane.ControlPlaneApplied = true
	}

	logger.Info("Crossplane reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// applyControlPlaneConfiguration installs the Crossplane v2 control plane from
// the embedded configuration (platform/controlplane/configuration) using
// server-side apply. It works identically whether the controller runs in-process
// during `adhar up` or in-cluster — no on-disk source tree or `kubectl` binary
// is required.
//
// Order matters: XRDs define the API types Compositions reference; Functions,
// ProviderConfigs and Operations depend on provider/Crossplane CRDs that may not
// be registered on a fresh cluster yet, so those steps are best-effort and the
// reconcile retries (gated on Status.Crossplane.ControlPlaneApplied) until the
// whole set applies cleanly.
func (r *AdharPlatformReconciler) applyControlPlaneConfiguration(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	logger := log.FromContext(ctx)
	fsys := controlplane.ConfigurationFS

	// XRDs first — they must establish before Compositions can reference them.
	if err := r.applyEmbeddedManifests(ctx, fsys, "configuration/xrd", resource, "XRDs", false, false); err != nil {
		return fmt.Errorf("applying XRDs: %w", err)
	}
	time.Sleep(5 * time.Second)

	// Compositions (nested per-domain directories → recursive).
	if err := r.applyEmbeddedManifests(ctx, fsys, "configuration/compositions", resource, "Compositions", true, false); err != nil {
		return fmt.Errorf("applying Compositions: %w", err)
	}

	// Ordered, fatal steps. Each is retried (gated on
	// Status.Crossplane.ControlPlaneApplied) until it applies cleanly:
	//   1. Functions          — composition functions (Function CRD ships with core).
	//   2. Provider packages   — installs provider-kubernetes/helm (Provider CRD
	//                            ships with core, so this applies immediately and
	//                            MUST run before their ClusterProviderConfigs).
	//   3. ClusterProviderConfigs — depend on the provider CRDs, which only
	//                            register a minute or two after the Provider
	//                            packages install; first-pass failures retry.
	//   4. Operations          — day-2 ops (alpha ops.crossplane.io API).
	//
	// providers/ is applied non-recursively so step 2 installs only
	// provider-packages.yaml and does NOT descend into providers/config or
	// providers/cloud (handled as their own steps).
	//
	// Note: configuration/crossplane.yaml is Crossplane *package metadata*
	// (meta.pkg.crossplane.io/v1) consumed by `crossplane xpkg build` — it is not
	// a runtime resource and is intentionally NOT applied here.
	for _, step := range []struct {
		dir, label string
	}{
		{"configuration/functions", "Functions"},
		{"configuration/providers", "Provider packages"},
		{"configuration/providers/config", "ProviderConfigs"},
		{"configuration/operations", "Operations"},
	} {
		if err := r.applyEmbeddedManifests(ctx, fsys, step.dir, resource, step.label, false, false); err != nil {
			logger.Info("Deferred control-plane step (provider/CRDs may not be ready yet); will retry", "step", step.label, "error", err)
			return err
		}
	}

	// Cloud provider packages + ProviderConfigs: heavy (hundreds of MB of
	// provider images) and useless without cloud credentials — their pods just
	// crash-loop on a local cluster. Install them only on cloud platforms.
	if isCloudProvider(resource.Spec.Provider) {
		if err := r.applyEmbeddedManifests(ctx, fsys, "configuration/providers/cloud", resource, "Cloud providers", true, true); err != nil {
			logger.Info("Cloud provider configuration deferred", "error", err)
		}
	} else {
		logger.V(1).Info("Skipping cloud Crossplane providers on local platform", "provider", resource.Spec.Provider)
	}

	logger.Info("Control plane configuration applied successfully")
	return nil
}

// applyEmbeddedManifests server-side-applies every YAML document found under dir
// in the embedded filesystem. When recursive is false only the directory's own
// files are applied (subdirectories are skipped). When bestEffort is true a
// per-file apply failure is logged and skipped instead of aborting (used for
// cloud providers, which can't apply on a cluster without the cloud provider
// CRDs); otherwise the first failure is returned so the reconcile retries.
func (r *AdharPlatformReconciler) applyEmbeddedManifests(ctx context.Context, fsys fs.FS, dir string, resource *v1alpha1.AdharPlatform, label string, recursive, bestEffort bool) error {
	logger := log.FromContext(ctx)
	logger.Info("Applying Crossplane " + label + "...")

	var files []string
	walk := func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if !recursive && p != dir {
				return fs.SkipDir
			}
			return nil
		}
		if isYAML(p) {
			files = append(files, p)
		}
		return nil
	}
	if err := fs.WalkDir(fsys, dir, walk); err != nil {
		return err
	}

	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return fmt.Errorf("reading %s: %w", f, err)
		}
		if err := r.applyManifest(ctx, data, resource, label+":"+path.Base(f)); err != nil {
			if bestEffort {
				logger.Info("Skipping manifest (best-effort)", "file", f, "error", err)
				continue
			}
			return fmt.Errorf("applying %s: %w", f, err)
		}
	}
	return nil
}

func isYAML(p string) bool {
	return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")
}

// isCloudProvider reports whether the platform targets a cloud provider whose
// Crossplane provider packages should be installed. Empty means local (kind).
func isCloudProvider(p v1alpha1.EnvironmentProvider) bool {
	switch p {
	case v1alpha1.ProviderAWS, v1alpha1.ProviderAzure, v1alpha1.ProviderGKE, v1alpha1.ProviderDO, v1alpha1.ProviderCivo:
		return true
	default:
		return false
	}
}
