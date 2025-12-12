package adharplatform

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
)

//go:embed resources/cilium
var ciliumFS embed.FS

func (r *AdharPlatformReconciler) ReconcileCilium(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Cilium core package")

	// Apply install.yaml
	ciliumManifestPath := "resources/cilium/install.yaml"
	manifestBytes, err := ciliumFS.ReadFile(ciliumManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read Cilium install manifest", "path", ciliumManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading cilium manifest %s: %w", ciliumManifestPath, err)
	}

	if err := r.applyManifest(ctx, manifestBytes, resource, "Cilium install"); err != nil {
		logger.Error(err, "Failed to apply Cilium install manifest")
		return ctrl.Result{}, err
	}

	// Apply post-install.yaml
	ciliumPostInstallPath := "resources/cilium/post-install.yaml"
	postInstallBytes, err := ciliumFS.ReadFile(ciliumPostInstallPath)
	if err != nil {
		// post-install is optional (may not exist in embedded resources)
		if errors.Is(err, fs.ErrNotExist) {
			logger.V(1).Info("Cilium post-install manifest not found, skipping", "path", ciliumPostInstallPath)
		} else {
			logger.Error(err, "Failed to read Cilium post-install manifest", "path", ciliumPostInstallPath)
			return ctrl.Result{}, fmt.Errorf("reading cilium post-install manifest %s: %w", ciliumPostInstallPath, err)
		}
	} else {
		if err := r.applyManifest(ctx, postInstallBytes, resource, "Cilium post-install"); err != nil {
			logger.Error(err, "Failed to apply Cilium post-install manifest")
			return ctrl.Result{}, err
		}
	}

	logger.Info("Successfully reconciled Cilium core package")
	return ctrl.Result{}, nil
}

// RawCiliumInstallResources returns the raw Cilium installation manifest.
// TODO: Implement templateData and config processing if needed for Cilium manifests.
func RawCiliumInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	ciliumManifestPath := "resources/cilium/install.yaml"
	manifestBytes, err := ciliumFS.ReadFile(ciliumManifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded cilium manifest %s: %w", ciliumManifestPath, err)
	}

	// For now, we return the manifest as a single item in a slice.
	// If the manifest is multi-document YAML, it should be split or handled accordingly
	// by the caller or a utility function.
	return [][]byte{manifestBytes}, nil
}
