package adharplatform

import (
	"context"
	"embed"
	"fmt"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed resources/gateway-api
//go:embed resources/gateway
var gatewayFS embed.FS

const (
	// gatewayServiceName is the Service Cilium generates for the adhar-gateway
	// Gateway (named cilium-gateway-<gateway-name>).
	gatewayServiceName = "cilium-gateway-adhar-gateway"
	// Fixed node ports so the Kind static host port-mapping (8080/8443 ->
	// 30080/30443) keeps working. CiliumGatewayClassConfig can select NodePort
	// service type but cannot pin port numbers, so the controller patches them.
	gatewayHTTPNodePort  = 30080
	gatewayHTTPSNodePort = 30443
)

// RawGatewayInstallResources returns the Gateway/GatewayClass manifest so it can
// be pushed through the GitOps flow like other core packages.
func RawGatewayInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	filePath := config.FilePath
	if filePath == "" {
		filePath = "gateway.yaml"
	}
	return k8s.BuildCustomizedManifests(filePath, "resources/gateway", gatewayFS, scheme, templateData)
}

// ReconcileGatewayAPICRDs installs the upstream Gateway API CRDs. These must be
// present before Cilium starts with Gateway API support enabled.
func (r *AdharPlatformReconciler) ReconcileGatewayAPICRDs(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Gateway API CRDs")

	manifestBytes, err := gatewayFS.ReadFile("resources/gateway-api/crds.yaml")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading gateway-api crds manifest: %w", err)
	}
	if err := r.applyManifest(ctx, manifestBytes, resource, "Gateway API CRDs"); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying gateway-api crds: %w", err)
	}
	logger.Info("Gateway API CRDs installed")
	return ctrl.Result{}, nil
}

// ReconcileGateway installs the platform GatewayClass, CiliumGatewayClassConfig
// and Gateway, then pins the generated Service's node ports so Kind's host
// port-mapping continues to route to the platform.
func (r *AdharPlatformReconciler) ReconcileGateway(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Gateway (Cilium Gateway API)")

	manifestBytes, err := gatewayFS.ReadFile("resources/gateway/gateway.yaml")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading gateway manifest: %w", err)
	}
	if err := r.applyManifest(ctx, manifestBytes, resource, "Gateway"); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying gateway manifest: %w", err)
	}

	// Cilium creates the gateway Service asynchronously once the Gateway is
	// accepted. Pin its node ports so the Kind host port-mapping (8080/8443 ->
	// 30080/30443) works. This is intentionally NON-FATAL: on a cold cluster
	// Cilium may need more than one reconcile to program the Gateway and create
	// the Service. Failing here must not abort the core install (ArgoCD/Gitea/
	// Crossplane). We leave Status.Gateway.Available unset so the core-install
	// gate re-runs this reconciler until the Service is pinned, while the rest of
	// the install still proceeds this pass.
	if err := r.pinGatewayNodePorts(ctx); err != nil {
		logger.Info("Gateway Service node ports not pinned yet; will retry on the next reconcile", "error", err)
		return ctrl.Result{}, nil
	}

	resource.Status.Gateway.Available = true
	logger.Info("Gateway reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// pinGatewayNodePorts waits for the Cilium-generated gateway Service and patches
// its HTTP/HTTPS node ports to the fixed values Kind expects.
func (r *AdharPlatformReconciler) pinGatewayNodePorts(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("Waiting for Cilium gateway Service to pin node ports...", "service", gatewayServiceName)

	// Cilium owns this Service and re-reconciles it, so a plain Update can hit a
	// resource-version conflict; retry.RetryOnConflict re-reads and re-applies.
	for i := 0; i < 18; i++ { // up to ~90s
		var svc corev1.Service
		if err := r.Get(ctx, types.NamespacedName{Name: gatewayServiceName, Namespace: globals.AdharSystemNamespace}, &svc); err != nil || svc.Spec.Type != corev1.ServiceTypeNodePort {
			time.Sleep(5 * time.Second)
			continue
		}
		if gatewayNodePortsPinned(&svc) {
			logger.Info("Gateway Service node ports already pinned")
			return nil
		}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cur corev1.Service
			if getErr := r.Get(ctx, types.NamespacedName{Name: gatewayServiceName, Namespace: globals.AdharSystemNamespace}, &cur); getErr != nil {
				return getErr
			}
			setGatewayNodePorts(&cur)
			return r.Update(ctx, &cur)
		})
		if err == nil {
			logger.Info("Pinned gateway Service node ports", "http", gatewayHTTPNodePort, "https", gatewayHTTPSNodePort)
			return nil
		}
		logger.Info("Failed to pin gateway node ports (will retry)", "error", err)
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("gateway Service %s not ready as NodePort within timeout", gatewayServiceName)
}

// gatewayNodePortsPinned reports whether the Service's HTTP/HTTPS node ports
// already match the fixed values Kind maps host ports to.
func gatewayNodePortsPinned(svc *corev1.Service) bool {
	for _, p := range svc.Spec.Ports {
		if (p.Port == 80 && p.NodePort != gatewayHTTPNodePort) || (p.Port == 443 && p.NodePort != gatewayHTTPSNodePort) {
			return false
		}
	}
	return true
}

// setGatewayNodePorts pins the HTTP(80)/HTTPS(443) listener node ports in place.
func setGatewayNodePorts(svc *corev1.Service) {
	for idx := range svc.Spec.Ports {
		switch svc.Spec.Ports[idx].Port {
		case 80:
			svc.Spec.Ports[idx].NodePort = gatewayHTTPNodePort
		case 443:
			svc.Spec.Ports[idx].NodePort = gatewayHTTPSNodePort
		}
	}
}
