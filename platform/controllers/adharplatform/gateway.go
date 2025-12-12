package adharplatform

import (
	"context"
	"embed"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
)

//go:embed resources/gateway
var gatewayFS embed.FS

func (r *AdharPlatformReconciler) ReconcileGateway(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("🔵 ReconcileGateway: Starting Gateway API reconciliation")

	// Step 1: Install Gateway API CRDs first - they're required for Gateway resources
	logger.Info("🔵 ReconcileGateway: Step 1 - Installing Gateway API CRDs (if not already present)")
	if err := r.installGatewayAPICRDs(ctx); err != nil {
		logger.Error(err, "❌ ReconcileGateway: Failed to install Gateway API CRDs, will retry")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	logger.Info("✅ ReconcileGateway: Gateway API CRDs installed/verified")

	// Step 2: Apply gateway install.yaml (contains GatewayClass, Gateway, and HTTPRoutes)
	logger.Info("🔵 ReconcileGateway: Step 2 - Reading Gateway install manifest")
	gatewayManifestPath := "resources/gateway/install.yaml"
	manifestBytes, err := gatewayFS.ReadFile(gatewayManifestPath)
	if err != nil {
		logger.Error(err, "❌ ReconcileGateway: Failed to read Gateway install manifest", "path", gatewayManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading gateway manifest %s: %w", gatewayManifestPath, err)
	}
	logger.Info("✅ ReconcileGateway: Successfully read Gateway manifest", "path", gatewayManifestPath, "size", len(manifestBytes))

	logger.Info("🔵 ReconcileGateway: Step 3 - Applying Gateway API resources", "path", gatewayManifestPath)
	if err := r.applyManifest(ctx, manifestBytes, resource, "Gateway API install"); err != nil {
		logger.Error(err, "❌ ReconcileGateway: Failed to apply Gateway install manifest - will retry")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Step 4: Ensure the Gateway Service exists.
	// NOTE: Local Kind uses Cilium Gateway hostNetwork mode (Envoy binds 80/443 on the node),
	// so we do NOT force NodePort or fixed ports here.
	logger.Info("🔵 ReconcileGateway: Step 4 - Ensuring Gateway Service exposure")
	if res, err := r.ensureGatewayServiceExposure(ctx, resource.Namespace, "adhar-gateway"); err != nil {
		logger.Error(err, "❌ ReconcileGateway: Failed ensuring Gateway Service exposure - will retry")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	} else if res.RequeueAfter > 0 || res.Requeue {
		return res, nil
	}

	// Step 5: Ensure TLS secret alias exists for Cilium SDS.
	// Cilium/Envoy may request the secret by a namespaced-prefixed name (e.g. "<ns>-<secret>").
	// Without this alias, HTTPS handshakes can fail with connection resets.
	logger.Info("🔵 ReconcileGateway: Step 5 - Ensuring TLS secret alias exists for Gateway")
	if res, err := r.ensureGatewayTLSSecretAlias(ctx, resource.Namespace, "adhar-cert"); err != nil {
		logger.Error(err, "❌ ReconcileGateway: Failed ensuring TLS secret alias - will retry")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	} else if res.RequeueAfter > 0 || res.Requeue {
		return res, nil
	}

	logger.Info("✅ ReconcileGateway: Successfully reconciled Gateway API resources")
	return ctrl.Result{}, nil
}

func (r *AdharPlatformReconciler) ensureGatewayServiceExposure(ctx context.Context, namespace, gatewayName string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	svcName := "cilium-gateway-" + gatewayName
	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: namespace}, &svc); err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("Gateway Service not created yet, will retry", "service", svcName, "namespace", namespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("✅ Gateway Service exists", "service", svcName, "type", string(svc.Spec.Type))
	return ctrl.Result{}, nil
}

func (r *AdharPlatformReconciler) ensureGatewayTLSSecretAlias(ctx context.Context, namespace, secretName string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Cilium's generated Envoy config commonly references secrets as: "<namespace>/<namespace>-<secretName>"
	aliasName := namespace + "-" + secretName
	if aliasName == secretName {
		return ctrl.Result{}, nil
	}

	// If alias already exists, nothing to do.
	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: aliasName, Namespace: namespace}, &existing); err == nil {
		return ctrl.Result{}, nil
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	// Load source secret.
	var src corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &src); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("TLS source secret not found yet, will retry", "secret", secretName, "namespace", namespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	alias := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      aliasName,
			Namespace: namespace,
		},
		Type: src.Type,
		Data: map[string][]byte{},
	}
	for k, v := range src.Data {
		alias.Data[k] = v
	}

	// Create alias (don't set ownerRef; the Gateway owns generated resources, and we want this stable).
	if err := r.Create(ctx, &alias); err != nil {
		// If it already exists due to races, we can ignore.
		if errors.IsAlreadyExists(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	logger.Info("✅ Created TLS secret alias for Gateway SDS", "source", secretName, "alias", aliasName, "namespace", namespace)
	return ctrl.Result{}, nil
}

// waitForGatewayAPICRDs checks if Gateway API CRDs are available
func (r *AdharPlatformReconciler) waitForGatewayAPICRDs(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("🔵 waitForGatewayAPICRDs: Starting to wait for CRDs to be established")

	checkCRDs := func(ctx context.Context) (bool, error) {
		allReady := true
		for _, crdName := range gatewayAPICRDNames() {
			var crd apiextensionsv1.CustomResourceDefinition
			if err := r.Get(ctx, types.NamespacedName{Name: crdName}, &crd); err != nil {
				logger.V(1).Info("🔵 waitForGatewayAPICRDs: CRD not found yet", "crd", crdName, "error", err.Error())
				// If we are forbidden, fail fast so the user sees the RBAC issue
				if k8serrors.IsForbidden(err) {
					return true, fmt.Errorf("forbidden getting Gateway API CRD %s: %w", crdName, err)
				}
				allReady = false
				continue
			}

			if !crdEstablished(&crd) {
				logger.V(1).Info("🔵 waitForGatewayAPICRDs: CRD not yet established", "crd", crdName)
				allReady = false
				continue
			}
			logger.V(1).Info("✅ waitForGatewayAPICRDs: CRD is established", "crd", crdName)
		}

		if allReady {
			logger.Info("✅ waitForGatewayAPICRDs: All CRDs are ready")
		}
		return allReady, nil
	}

	logger.Info("🔵 waitForGatewayAPICRDs: Polling for CRDs (timeout: 45s, interval: 2s)")
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 45*time.Second, true, checkCRDs); err != nil {
		logger.Error(err, "❌ waitForGatewayAPICRDs: CRDs not ready within timeout")
		return fmt.Errorf("gateway API CRDs not ready: %w", err)
	}

	r.resetRESTMapper(ctx, "gateway API CRDs available")
	logger.Info("✅ waitForGatewayAPICRDs: Gateway API CRDs are available and established")
	return nil
}

// installGatewayAPICRDs installs Gateway API CRDs from the official release
// This function is idempotent - it can be called multiple times safely
func (r *AdharPlatformReconciler) installGatewayAPICRDs(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("🔵 installGatewayAPICRDs: Starting CRD installation check")

	// First check if CRDs are already installed
	logger.Info("🔵 installGatewayAPICRDs: Checking if CRDs are already installed")
	err := r.gatewayAPICRDsReady(ctx)
	if err == nil {
		logger.Info("✅ installGatewayAPICRDs: Gateway API CRDs already installed, verifying availability")
		return r.waitForGatewayAPICRDs(ctx)
	}
	logger.Info("🔵 installGatewayAPICRDs: CRDs not found, will install them", "error", err.Error())

	logger.Info("🔵 installGatewayAPICRDs: Installing Gateway API CRDs from embedded manifest")

	crdManifestPath := "resources/gateway/standard-install.yaml"
	logger.Info("🔵 installGatewayAPICRDs: Loading CRD manifest", "path", crdManifestPath)
	manifestBytes, err := gatewayFS.ReadFile(crdManifestPath)
	if err != nil {
		logger.Error(err, "❌ installGatewayAPICRDs: Failed to read embedded CRD manifest", "path", crdManifestPath)
		return fmt.Errorf("reading gateway API CRDs manifest %s: %w", crdManifestPath, err)
	}
	logger.Info("✅ installGatewayAPICRDs: Loaded Gateway API CRDs", "path", crdManifestPath, "size", len(manifestBytes))

	logger.Info("🔵 installGatewayAPICRDs: Applying Gateway API CRDs", "size", len(manifestBytes))
	// Apply the CRD manifest
	// CRDs are cluster-scoped, so we don't need a namespace-specific resource
	dummyResource := &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-api-crds",
			Namespace: "default",
		},
	}
	if err := r.applyManifest(ctx, manifestBytes, dummyResource, "Gateway API CRDs"); err != nil {
		logger.Error(err, "❌ installGatewayAPICRDs: Failed to apply CRD manifest")
		return fmt.Errorf("failed to apply Gateway API CRDs: %w", err)
	}
	logger.Info("✅ installGatewayAPICRDs: CRD manifest applied successfully")

	logger.Info("🔵 installGatewayAPICRDs: Waiting for Gateway API CRDs to be established")
	if err := r.waitForGatewayAPICRDs(ctx); err != nil {
		logger.Error(err, "❌ installGatewayAPICRDs: Failed to verify CRDs after install")
		return fmt.Errorf("failed to verify Gateway API CRDs after install: %w", err)
	}

	logger.Info("✅ installGatewayAPICRDs: Gateway API CRDs installed successfully")
	return nil
}

func (r *AdharPlatformReconciler) gatewayAPICRDsReady(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("🔵 gatewayAPICRDsReady: Checking CRD readiness")
	for _, crdName := range gatewayAPICRDNames() {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := r.Get(ctx, types.NamespacedName{Name: crdName}, &crd); err != nil {
			logger.V(1).Info("🔵 gatewayAPICRDsReady: CRD not found", "crd", crdName, "error", err.Error())
			return err
		}

		if !crdEstablished(&crd) {
			logger.V(1).Info("🔵 gatewayAPICRDsReady: CRD not yet established", "crd", crdName)
			return fmt.Errorf("gateway API CRD %s not yet established", crdName)
		}
		logger.V(1).Info("✅ gatewayAPICRDsReady: CRD is ready", "crd", crdName)
	}

	logger.V(1).Info("✅ gatewayAPICRDsReady: All CRDs are ready")
	return nil
}

func gatewayAPICRDNames() []string {
	return []string{
		"gatewayclasses.gateway.networking.k8s.io",
		"gateways.gateway.networking.k8s.io",
		"grpcroutes.gateway.networking.k8s.io",
		"httproutes.gateway.networking.k8s.io",
		"referencegrants.gateway.networking.k8s.io",
	}
}

func crdEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}

	return false
}
