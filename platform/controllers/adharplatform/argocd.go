package adharplatform

import (
	"context"
	"embed"
	"fmt"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/k8s"

	"adhar-io/adhar/globals"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// NOTE: This embeds files from the 'platform/controllers/adharplatform/resources/argocd' directory.
// This directory is populated by the 'make embedded-resources' target, which runs generation scripts.
//
//go:embed resources/argocd
var argoCDFS embed.FS

func RawArgocdInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	// config.FilePath, when set, points at a user-provided override file on
	// local disk; empty means "no overrides".
	return k8s.BuildCustomizedManifests(config.FilePath, "resources/argocd", argoCDFS, scheme, templateData)
}

func (r *AdharPlatformReconciler) ReconcileArgo(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Starting ArgoCD reconciliation")

	// ArgoCD will be installed using direct manifest application

	// Apply install.yaml, or the HA rendering (replicas, PDBs, HA redis) when
	// enableHAMode is set (roadmap P1.2). Both files are pre-rendered from the
	// same chart version by the hack/ generation scripts.
	logger.Info("Applying ArgoCD install manifest", "haMode", resource.Spec.BuildCustomization.EnableHAMode)
	argocdManifestPath := "resources/argocd/install.yaml"
	if resource.Spec.BuildCustomization.EnableHAMode {
		argocdManifestPath = "resources/argocd/install-ha.yaml"
	}
	manifestBytes, err := argoCDFS.ReadFile(argocdManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read ArgoCD install manifest", "path", argocdManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading argocd manifest %s: %w", argocdManifestPath, err)
	}
	// Render {{ .Host }}/{{ .PortSuffix }} etc. — the manifest is templated, never raw.
	if manifestBytes, err = r.renderEmbedded(manifestBytes); err != nil {
		return ctrl.Result{}, fmt.Errorf("rendering argocd manifest %s: %w", argocdManifestPath, err)
	}

	if err := r.applyManifest(ctx, manifestBytes, resource, "ArgoCD install"); err != nil {
		logger.Error(err, "Failed to apply ArgoCD install manifest")
		return ctrl.Result{}, err
	}
	logger.Info("Successfully applied ArgoCD install manifest")

	// Apply post-install.yaml for ArgoCD
	logger.Info("Applying ArgoCD post-install manifest")
	argocdPostInstallPath := "resources/argocd/post-install.yaml"
	postInstallBytes, err := argoCDFS.ReadFile(argocdPostInstallPath)
	if err != nil {
		logger.Error(err, "Failed to read ArgoCD post-install manifest", "path", argocdPostInstallPath)
		return ctrl.Result{}, fmt.Errorf("reading argocd post-install manifest %s: %w", argocdPostInstallPath, err)
	}
	if postInstallBytes, err = r.renderEmbedded(postInstallBytes); err != nil {
		return ctrl.Result{}, fmt.Errorf("rendering argocd post-install manifest %s: %w", argocdPostInstallPath, err)
	}

	if err := r.applyManifest(ctx, postInstallBytes, resource, "ArgoCD post-install"); err != nil {
		logger.Error(err, "Failed to apply ArgoCD post-install manifest")
		return ctrl.Result{}, err
	}
	logger.Info("Successfully applied ArgoCD post-install manifest")

	resource.Status.ArgoCD.Available = true
	logger.Info("ArgoCD reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// The oauth2-proxy that fronts the Argo CD UI.
const (
	proxyServiceName = "argocd-oauth2-proxy"
	proxyServicePort = 4180
	// The route the bootstrap post-install manifest creates. NOT "argocd".
	proxyRouteName = "argocd-server"
)

// reconcileArgoCDSSOProxy installs the oauth2-proxy that fronts the Argo CD UI
// and repoints the UI route at it, but only when the identity it needs exists.
func (r *AdharPlatformReconciler) reconcileArgoCDSSOProxy(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	logger := log.FromContext(ctx)

	// Prerequisites, both created by stack packages: the Argo CD Keycloak client
	// secret and the platform-wide session cookie.
	var clients corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: "keycloak-clients", Namespace: globals.AdharSystemNamespace}, &clients); err != nil {
		return fmt.Errorf("keycloak-clients not published yet: %w", err)
	}
	if len(clients.Data["ARGOCD_CLIENT_SECRET"]) == 0 {
		return fmt.Errorf("keycloak-clients has no ARGOCD_CLIENT_SECRET yet")
	}
	var cookie corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: "adhar-sso-cookie", Namespace: globals.AdharSystemNamespace}, &cookie); err != nil {
		return fmt.Errorf("shared SSO cookie not published yet: %w", err)
	}

	// The manifest builds absolute URLs from the platform host (the Keycloak
	// issuer, the OAuth callback, the cookie domain). Rendering it without one
	// produces "https://keycloak./realms/adhar" — a hostname ending in a bare dot
	// — and the proxy crash-loops on OIDC discovery with "lookup keycloak. no
	// such host". That is not hypothetical: the in-cluster controller-manager
	// reconciles this same manifest, and when it runs without the host configured
	// it REPLACED a working proxy with a broken one. Refusing to apply is the
	// safe outcome: a platform with no configured host is not serving Argo CD on
	// a real name anyway.
	if r.Config.Host == "" {
		return fmt.Errorf("no platform host configured; refusing to render the Argo CD SSO proxy with an empty hostname")
	}

	proxyBytes, err := argoCDFS.ReadFile("resources/argocd/sso-proxy.yaml")
	if err != nil {
		return fmt.Errorf("reading the Argo CD SSO proxy manifest: %w", err)
	}
	if proxyBytes, err = r.renderEmbedded(proxyBytes); err != nil {
		return fmt.Errorf("rendering the Argo CD SSO proxy manifest: %w", err)
	}
	if err := r.applyManifest(ctx, proxyBytes, resource, "ArgoCD SSO proxy"); err != nil {
		return fmt.Errorf("applying the Argo CD SSO proxy: %w", err)
	}

	// Only send traffic through the proxy once it is actually serving, so a
	// failed rollout never takes the UI with it.
	var proxy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: "argocd-oauth2-proxy", Namespace: globals.AdharSystemNamespace}, &proxy); err != nil {
		return fmt.Errorf("reading the Argo CD SSO proxy: %w", err)
	}
	if proxy.Status.ReadyReplicas == 0 {
		return fmt.Errorf("the Argo CD SSO proxy has no ready replica yet")
	}

	// post-install.yaml re-points this route at the Argo CD Service on every
	// pass, so the switch is re-applied here after it. HTTPRoute is handled as
	// unstructured, like every other Gateway API object in this controller: the
	// gateway-api Go module is deliberately not a dependency.
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"})
	if err := r.Get(ctx, types.NamespacedName{Name: proxyRouteName, Namespace: globals.AdharSystemNamespace}, route); err != nil {
		return fmt.Errorf("reading the Argo CD route: %w", err)
	}
	rules, found, err := unstructured.NestedSlice(route.Object, "spec", "rules")
	if err != nil || !found || len(rules) == 0 {
		return fmt.Errorf("the Argo CD route has no rules to repoint")
	}
	changed := false
	for i := range rules {
		rule, ok := rules[i].(map[string]interface{})
		if !ok {
			continue
		}
		refs, ok := rule["backendRefs"].([]interface{})
		if !ok {
			continue
		}
		for j := range refs {
			ref, ok := refs[j].(map[string]interface{})
			if !ok {
				continue
			}
			if ref["name"] == proxyServiceName && ref["port"] == int64(proxyServicePort) {
				continue
			}
			ref["name"] = proxyServiceName
			ref["port"] = int64(proxyServicePort)
			changed = true
		}
		rule["backendRefs"] = refs
		rules[i] = rule
	}
	if !changed {
		return nil
	}
	if err := unstructured.SetNestedSlice(route.Object, rules, "spec", "rules"); err != nil {
		return fmt.Errorf("rewriting the Argo CD route backends: %w", err)
	}
	if err := r.Update(ctx, route); err != nil {
		return fmt.Errorf("repointing the Argo CD route at its SSO proxy: %w", err)
	}
	logger.Info("Argo CD UI now serves through the platform SSO session; its own login page is bypassed")
	return nil
}
