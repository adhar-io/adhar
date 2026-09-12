package adharplatform

import (
	"context"
	"embed"
	"errors"
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
	if resource.Spec.BuildCustomization.EnableHAMode {
		// Two replicas with leader election, larger caps (see install-ha.yaml).
		manifestPath = "resources/crossplane/install-ha.yaml"
	}
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

	// RBAC first — the crossplane-compose-local ClusterRole aggregates into the
	// Crossplane service account so it may create the composed resources our
	// local compositions render (CNPG Clusters, Valkey, ArgoCD Applications, …).
	// Without it every local self-service request (e.g. CompositeDatabase) fails
	// with "cannot patch resource ... forbidden". Cluster-scoped + dependency-free,
	// so it applies before anything composes.
	if err := r.applyEmbeddedManifests(ctx, fsys, "configuration/rbac", resource, "Compose RBAC", false, false); err != nil {
		return fmt.Errorf("applying compose RBAC: %w", err)
	}

	// XRDs next — they must establish before Compositions can reference them.
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
		// requireCRDs: do not accept "CRD not registered yet" as success.
		// The kubernetes/helm ClusterProviderConfigs are the whole reason this
		// loop retries — treating a NoMatch as applied silently produced a
		// live platform with providers installed and no ProviderConfig at all.
		requireCRDs bool
	}{
		{"configuration/functions", "Functions", false},
		{"configuration/providers", "Provider packages", false},
		{"configuration/providers/config", "ProviderConfigs", true},
		{"configuration/operations", "Operations", false},
	} {
		if err := r.applyEmbeddedManifestsStrict(ctx, fsys, step.dir, resource, step.label, false, false, step.requireCRDs); err != nil {
			logger.Info("Deferred control-plane step (provider/CRDs may not be ready yet); will retry", "step", step.label, "error", err)
			return err
		}
	}

	// Cloud provider packages + ProviderConfigs: heavy (hundreds of MB of
	// provider images) and useless without cloud credentials — their pods just
	// crash-loop on a local cluster. Install them only on cloud platforms.
	if isCloudProvider(resource.Spec.Provider) {
		// Only THIS cloud's provider packages and ProviderConfig. Every upjet
		// provider family registers hundreds of CRDs (AWS+Azure+GCP together
		// ~3,000); installing all of them on a DigitalOcean platform pushed a
		// single control-plane node's API server into timeouts and bought
		// nothing. Additional clouds are opted in by applying their
		// provider-packages entries and ProviderConfig from
		// platform/controlplane/configuration/providers/cloud (see README).
		// Fatal, not best-effort: without its ClusterProviderConfig the cloud
		// provider cannot reconcile a single managed resource, so a platform
		// that recorded ControlPlaneApplied without one is broken in a way
		// nothing else surfaces. The reconcile retries (ControlPlaneApplied
		// stays false); the GitOps ApplicationSet is applied earlier in the
		// reconcile, so applications are never held up by this.
		if err := r.applyCloudProviders(ctx, fsys, resource); err != nil {
			logger.Info("Cloud provider configuration incomplete; will retry", "error", err)
			return fmt.Errorf("applying cloud provider configuration: %w", err)
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
	return r.applyEmbeddedManifestsStrict(ctx, fsys, dir, resource, label, recursive, bestEffort, false)
}

// applyEmbeddedManifestsStrict is applyEmbeddedManifests with control over the
// "CRD not registered yet" escape hatch. With requireCRDs set, a NoMatch is a
// hard error so the reconcile retries instead of recording a step as applied
// when nothing was actually created (see applyManifestStrict).
func (r *AdharPlatformReconciler) applyEmbeddedManifestsStrict(ctx context.Context, fsys fs.FS, dir string, resource *v1alpha1.AdharPlatform, label string, recursive, bestEffort, requireCRDs bool) error {
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
		// *-template.yaml files are documentation (placeholder Secrets such
		// as credential-secrets-template.yaml) — applying them would create
		// Secrets holding the literal "<TOKEN>" strings and clobber the real
		// credentials the CLI materialises. Never apply them.
		if isYAML(p) && !strings.HasSuffix(p, "-template.yaml") {
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
		applyFn := r.applyManifest
		if requireCRDs {
			applyFn = r.applyManifestStrict
		}
		if err := applyFn(ctx, data, resource, label+":"+path.Base(f)); err != nil {
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

// cloudFamily maps the platform's provider to the token that names its
// Crossplane provider packages (provider-<family>-*, provider-family-<family>)
// and ProviderConfig file (<family>-providerconfig.yaml).
func cloudFamily(p v1alpha1.EnvironmentProvider) string {
	switch p {
	case v1alpha1.ProviderAWS:
		return "aws"
	case v1alpha1.ProviderAzure:
		return "azure"
	case v1alpha1.ProviderGKE:
		return "gcp"
	case v1alpha1.ProviderDO:
		return "digitalocean"
	case v1alpha1.ProviderCivo:
		return "civo"
	default:
		return ""
	}
}

// applyCloudProviders applies the Provider packages for the platform's own
// cloud (the documents of providers/cloud/provider-packages.yaml whose name
// carries the cloud family) and that cloud's ProviderConfig. Best-effort per
// document: the ProviderConfig CRD only exists once the package has installed,
// so the first pass logs and the reconcile retries.
func (r *AdharPlatformReconciler) applyCloudProviders(ctx context.Context, fsys fs.FS, resource *v1alpha1.AdharPlatform) error {
	logger := log.FromContext(ctx)
	family := cloudFamily(resource.Spec.Provider)
	if family == "" {
		return nil
	}
	pkgs, err := fs.ReadFile(fsys, "configuration/providers/cloud/provider-packages.yaml")
	if err != nil {
		return fmt.Errorf("reading provider packages: %w", err)
	}
	var selected []string
	for _, doc := range strings.Split(string(pkgs), "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		if strings.Contains(doc, "provider-"+family+"-") || strings.Contains(doc, "provider-"+family+":") ||
			strings.Contains(doc, "provider-family-"+family) || strings.Contains(doc, "name: provider-"+family) ||
			strings.Contains(doc, "provider-upjet-"+family) {
			selected = append(selected, doc)
		}
	}
	if len(selected) == 0 {
		logger.Info("No Crossplane provider packages found for cloud", "family", family)
		return nil
	}
	logger.Info("Applying Crossplane cloud providers", "family", family, "packages", len(selected))
	if err := r.applyManifest(ctx, []byte(strings.Join(selected, "\n---")), resource, "Cloud providers:"+family); err != nil {
		return fmt.Errorf("applying %s provider packages: %w", family, err)
	}
	pc := "configuration/providers/cloud/" + family + "-providerconfig.yaml"
	data, err := fs.ReadFile(fsys, pc)
	if err != nil {
		// Only a MISSING file means "no ProviderConfig shipped for this cloud".
		// Treating every read error that way hid the exact failure this package
		// has been bitten by before: a cluster that reports ControlPlaneApplied
		// while holding zero ProviderConfigs, so nothing can ever be
		// provisioned, and with no message anywhere to say why.
		if errors.Is(err, fs.ErrNotExist) {
			logger.Info("No ProviderConfig shipped for cloud", "family", family, "path", pc)
			return nil
		}
		return fmt.Errorf("reading %s: %w", pc, err)
	}
	if err := r.applyManifestStrict(ctx, data, resource, "ProviderConfig:"+family); err != nil {
		return fmt.Errorf("applying %s ProviderConfig (provider CRDs may not be registered yet): %w", family, err)
	}
	return nil
}
