package adharplatform

import (
	"os"
	"strings"
	"testing"
)

// Every resource kind the stack declares and ArgoCD cannot assess must have a
// health check, or a sync wave blocks forever on something that is fine.
//
// A wave does not complete until all its resources report Healthy, and for a
// custom resource with no built-in check ArgoCD reports Unknown — which never
// becomes Healthy. Every later wave is held behind it.
//
// Measured on Azure, 2026-10-04: the keycloak Application sat at
//
//	waiting for healthy state of gateway.networking.k8s.io/HTTPRoute/keycloak
//
// while that route was Accepted=True, ResolvedRefs=True, resolved to the live
// gateway and answered HTTP 302 over a valid certificate. The wave never
// completed, so Keycloak's wave-20 config Job was never created and the
// `keycloak-clients` Secret was never written — and that Secret gates ~12
// consumer ExternalSecrets and 9 oauth2-proxy rollouts. One unassessable
// HTTPRoute held the platform's whole SSO estate Degraded. Adding the check
// below took the keycloak app from a permanently Running operation to
// "successfully synced (all tasks run)" in under a minute.
func TestArgoCDCanAssessEveryCustomKindTheStackSyncs(t *testing.T) {
	cm := argocdConfigMap(t)
	// Kinds the stack declares that ArgoCD has no built-in health check for.
	// Each must be assessable or it can stall a wave.
	for _, kind := range []string{
		"gateway.networking.k8s.io_HTTPRoute",
		"gateway.networking.k8s.io_Gateway",
		"generators.external-secrets.io_Password",
		"postgresql.cnpg.io_ScheduledBackup",
	} {
		key := "resource.customizations.health." + kind
		if !strings.Contains(cm, key) {
			t.Errorf("argocd-cm has no health check for %s. Without one ArgoCD reports Unknown, "+
				"the sync wave containing it never completes, and every later wave is blocked "+
				"on a resource that is healthy.", kind)
		}
	}
}

// The HTTPRoute check must assert what the route itself reports, not return a
// blanket Healthy — a route whose backendRefs do not resolve has to surface.
func TestHTTPRouteHealthCheckStillDetectsFailure(t *testing.T) {
	cm := argocdConfigMap(t)
	i := strings.Index(cm, "resource.customizations.health.gateway.networking.k8s.io_HTTPRoute")
	if i < 0 {
		t.Fatal("the HTTPRoute health check is missing")
	}
	// The Lua body runs until the next customization key.
	body := cm[i:]
	if j := strings.Index(body[1:], "\n  resource.customizations."); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{"Accepted", "ResolvedRefs", `"Degraded"`, `"Progressing"`, `"Healthy"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the HTTPRoute check must consider %s — a check that always returns Healthy "+
				"hides a broken route instead of stalling on a working one", want)
		}
	}
}

// Generators are declarative inputs with no status subresource, so Healthy is
// correct for them — but only because a malformed generator fails at the
// ExternalSecret that references it, and ExternalSecret IS assessed.
func TestGeneratorHealthIsPairedWithExternalSecretHealth(t *testing.T) {
	cm := argocdConfigMap(t)
	if !strings.Contains(cm, "resource.customizations.health.external-secrets.io_ExternalSecret") {
		t.Error("reporting generators Healthy is only safe while ExternalSecret itself is " +
			"assessed — that is where a malformed generator actually surfaces")
	}
}

func argocdConfigMap(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("resources/argocd/install.yaml")
	if err != nil {
		t.Fatalf("reading the embedded ArgoCD manifest: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "name: argocd-cm")
	if i < 0 {
		t.Fatal("argocd-cm has moved in the embedded manifest; this guard needs updating")
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n---"); j > 0 {
		rest = rest[:j]
	}
	return rest
}
