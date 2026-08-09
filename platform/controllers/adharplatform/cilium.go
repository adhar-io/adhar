package adharplatform

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"net/url"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
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

	manifestBytes = r.rewriteCiliumAPIEndpoint(ctx, manifestBytes, resource)

	if err := r.applyManifest(ctx, manifestBytes, resource, "Cilium install"); err != nil {
		logger.Error(err, "Failed to apply Cilium install manifest")
		return ctrl.Result{}, err
	}

	// Apply post-install.yaml
	ciliumPostInstallPath := "resources/cilium/post-install.yaml"
	postInstallBytes, err := ciliumFS.ReadFile(ciliumPostInstallPath)
	if err != nil {
		logger.Error(err, "Failed to read Cilium post-install manifest", "path", ciliumPostInstallPath)
		return ctrl.Result{}, fmt.Errorf("reading cilium post-install manifest %s: %w", ciliumPostInstallPath, err)
	}

	if err := r.applyManifest(ctx, postInstallBytes, resource, "Cilium post-install"); err != nil {
		logger.Error(err, "Failed to apply Cilium post-install manifest")
		return ctrl.Result{}, err
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

// rewriteCiliumAPIEndpoint substitutes the Kind-specific Kubernetes API
// endpoint baked into the generated Cilium manifest
// (KUBERNETES_SERVICE_HOST: adhar-control-plane) with the real API endpoint
// of the cluster being reconciled. Cilium agents run with kube-proxy
// replacement and host networking, so they cannot use the in-cluster service
// VIP and need a directly reachable endpoint. On Kind the baked-in node name
// is correct and is left untouched.
func (r *AdharPlatformReconciler) rewriteCiliumAPIEndpoint(ctx context.Context, manifest []byte, resource *v1alpha1.AdharPlatform) []byte {
	logger := log.FromContext(ctx)
	if resource.Spec.Provider == "" || resource.Spec.Provider == v1alpha1.ProviderKind {
		return manifest
	}

	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		logger.Error(err, "cannot determine API endpoint for Cilium; leaving manifest unchanged")
		return manifest
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Hostname() == "" {
		logger.Error(err, "cannot parse API host for Cilium", "host", cfg.Host)
		return manifest
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "6443"
	}

	manifest = bytes.ReplaceAll(manifest,
		[]byte(`value: "adhar-control-plane"`),
		[]byte(`value: "`+host+`"`))

	// Multi-NIC cloud VMs defeat Cilium's device auto-detection (it derives
	// the direct routing device from the node InternalIP, which external
	// cloud-provider nodes lack until the CCM initializes them — after
	// Cilium). Pin the private-network interface per provider to break the
	// cycle.
	if dev, ok := cloudPrivateNIC[resource.Spec.Provider]; ok {
		manifest = bytes.ReplaceAll(manifest,
			[]byte("  routing-mode: tunnel"),
			[]byte("  routing-mode: tunnel\n  direct-routing-device: "+dev+"\n  devices: "+dev))
		logger.Info("Pinned Cilium network device for cloud provider", "provider", resource.Spec.Provider, "device", dev)
	}
	logger.Info("Rewrote Cilium API endpoint for cloud cluster", "host", host, "port", port)
	return manifest
}

// cloudPrivateNIC maps providers to the VM interface carrying the private
// network (used for pod/node traffic) on their default images.
var cloudPrivateNIC = map[v1alpha1.EnvironmentProvider]string{
	v1alpha1.ProviderDO:    "eth1",
	v1alpha1.ProviderAWS:   "ens5",
	v1alpha1.ProviderGKE:   "ens4",
	v1alpha1.ProviderAzure: "eth0",
	v1alpha1.ProviderCivo:  "eth0",
}
