package adharplatform

// OpenBao must sign a person with a Keycloak session straight in (2026-10-24).
//
// Live on AWS the UI opened on /ui/vault/auth?with=token with no Keycloak
// method at all: the bootstrap Sync hook waited its 20 minutes for the client
// secret, the secret landed two minutes later, and nothing ever ran the OIDC
// step again. And even with the method present, OpenBao 2.6's UI starts OIDC in
// a popup and completes it only through window.opener.postMessage, so it is
// one click at best. Each guard below pins one piece of the fix.

import (
	"strings"
	"testing"
)

const openBaoStart = "/adhar-sso/openbao.html"

// The OIDC method converges whenever its prerequisites arrive, not only during
// the one sync that happened to run the bootstrap hook.
func TestOpenBaoOIDCIsReconciledOutsideTheSyncHook(t *testing.T) {
	boot := withoutComments(readStackFile(t, "security/openbao/manifests/bootstrap.yaml"))
	for _, want := range []string{
		"kind: CronJob",
		"name: openbao-oidc-reconcile",
		`command: ["/bin/sh", "/scripts/oidc-reconcile.sh"]`,
		"exec sh /scripts/configure-oidc.sh",
		"sh /scripts/configure-oidc.sh",
		"automountServiceAccountToken: false",
	} {
		if !strings.Contains(boot, want) {
			t.Errorf("bootstrap.yaml lacks %q: the OIDC sign-in is configured only by the Sync hook, which "+
				"never runs again on a converged app", want)
		}
	}
}

// The start page is an allowed redirect URI on BOTH sides, or Keycloak answers
// `Invalid parameter: redirect_uri` / OpenBao refuses the auth_url.
func TestOpenBaoStartPageIsARegisteredRedirect(t *testing.T) {
	const uri = "https://openbao.adhar.localtest.me:8443" + openBaoStart
	boot := withoutComments(readStackFile(t, "security/openbao/manifests/bootstrap.yaml"))
	if !strings.Contains(boot, "${OPENBAO_URL}"+openBaoStart) || !strings.Contains(boot, `OPENBAO_URL="https://openbao.adhar.localtest.me:8443"`) {
		t.Error("the OpenBao oidc role does not allow the click-free start page as a redirect URI")
	}
	if !strings.Contains(readStackFile(t, "security/openbao/manifests/oidc.yaml"), `"`+uri+`"`) {
		t.Errorf("the Keycloak openbao client does not register %s", uri)
	}
}

// The route sends a bare load of the home page to the start page, lets the
// marker through (no loop), and never redirects the break-glass token page.
func TestOpenBaoRouteStartsTheKeycloakSignIn(t *testing.T) {
	route := readStackFile(t, "security/openbao/manifests/httproute.yaml.tmpl")
	for _, want := range []string{
		"path: {type: PathPrefix, value: /adhar-sso/}",
		"name: adhar-sso-start",
		"replaceFullPath: " + openBaoStart,
		"port: {{ .Port }}",
		"adhar_sso_openbao=",
		"path: {type: Exact, value: /ui/}",
	} {
		if !strings.Contains(route, want) {
			t.Errorf("the openbao route lacks %q", want)
		}
	}
	if strings.Contains(withoutComments(route), "/ui/vault") {
		t.Error("the openbao route matches a /ui/vault path: the UI's own routes, and the break-glass " +
			"/ui/vault/auth?with=token page, must reach OpenBao untouched")
	}
	// The marker rule must precede the redirect for the same paths.
	if strings.Index(route, "adhar_sso_openbao=") > strings.Index(route, "type: RequestRedirect") {
		t.Error("the openbao redirect precedes the marker pass-through")
	}
}

// The start page completes the login the only way OpenBao's UI accepts outside
// a popup: a single-use wrapping token handed to its `wrapped_token` sign-in,
// with OpenBao checking state and a browser-bound nonce.
func TestOpenBaoStartPageUsesAWrappedTokenAndANonce(t *testing.T) {
	page := readStackFile(t, "security/keycloak/manifests/sso-start.yaml")
	for _, want := range []string{
		"openbao.html: |",
		"{key: openbao.html, path: openbao.html}",
		"/oidc/auth_url",
		"client_nonce",
		"'X-Vault-Wrap-TTL': '60s'",
		"wrapped_token=",
		"/ui/vault/auth?with=",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("adhar-sso-start lacks %q for OpenBao", want)
		}
	}
	if strings.Contains(page, "client_token") {
		t.Error("the OpenBao start page handles a raw client token; it must only ever see the wrapping token")
	}
}
