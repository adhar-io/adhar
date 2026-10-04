package adharplatform

import (
	"os"
	"strings"
	"testing"
)

// Every key a consumer declares as required must always exist.
//
// External Secrets fails the WHOLE ExternalSecret when a single listed key is
// missing, so a "best-effort" value on the producing side becomes a hard,
// permanent failure on the consuming side.
//
// ARGOCD_SESSION_TOKEN is written by Keycloak's config Job, which treats it as
// best effort because ArgoCD may be briefly unreachable. The creation path
// deliberately writes the key even when the token is empty — its comment says
// why: "otherwise consumers of it fail with 'could not get secret data'". The
// already-exists path did NOT: it wrote nothing unless the token was non-empty.
// And the 5-minute reconcile CronJob runs MODE=clients, which skips that block
// entirely, so the key was never backfilled.
//
// Live on 2026-10-04, right after an argocd-application-controller restart: the
// full-mode Job fetched an empty token, the key stayed absent, `console-oidc`
// reported `could not get secret data from provider` forever, the console
// Deployment was never created and https://console.<host> returned 503.
func TestKeycloakAlwaysCreatesTheArgoCDTokenKey(t *testing.T) {
	src := keycloakConfigScript(t)

	// The already-exists branch must create the key when the token is empty.
	i := strings.Index(src, "ensure_clients_secret()")
	if i < 0 {
		t.Fatal("ensure_clients_secret has moved; this guard needs updating")
	}
	fn := src[i:]
	if j := strings.Index(fn, "\n    }"); j > 0 {
		fn = fn[:j]
	}
	if !strings.Contains(fn, `"ARGOCD_SESSION_TOKEN":""`) {
		t.Error("when the token cannot be fetched and the key is absent, ensure_clients_secret must " +
			"still create it empty — External Secrets fails the entire ExternalSecret on one " +
			"missing key, so a best-effort value becomes a permanent hard failure")
	}
	// It must only do that when the key is genuinely absent, never clobbering a
	// good token with an empty string.
	if !strings.Contains(fn, "jsonpath='{.data.ARGOCD_SESSION_TOKEN}'") {
		t.Error("the empty-token backfill must check whether the key already exists, or a transient " +
			"ArgoCD outage would overwrite a working token with an empty one")
	}
}

// The console declares the key as required, which is what makes the above
// mandatory. If that ever changes to a tolerant form, this guard should be
// revisited rather than silently kept.
func TestConsoleStillRequiresTheArgoCDTokenKey(t *testing.T) {
	b, err := os.ReadFile("../../stack/packages/core/adhar-console/manifests/install.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ARGOCD_SESSION_TOKEN") {
		t.Skip("the console no longer references ARGOCD_SESSION_TOKEN")
	}
	if !strings.Contains(string(b), "secretKey: ARGOCD_SESSION_TOKEN") {
		t.Log("the console references the token but not as an explicit required secretKey; " +
			"the producer-side guarantee may no longer be necessary")
	}
}

func keycloakConfigScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../stack/packages/security/keycloak/manifests/keycloak-config.yaml")
	if err != nil {
		t.Fatalf("reading the keycloak config manifest: %v", err)
	}
	return string(b)
}
