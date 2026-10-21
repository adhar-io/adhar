package adharplatform

import (
	"strings"
	"testing"
)

// Plane's sign-in must work with no proxy session.
//
// The SPA's first request is `GET /api/instances/`; a failure there renders
// "Looks like Plane didn't start up correctly!". Behind oauth2-proxy an XHR
// without a valid session is answered 401 (never redirected), and a browser
// carrying the proxy cookie from a previous cluster — signed with that
// cluster's secret — is such a request. So was every browser on 2026-10-15
// after the rebuild, while the cached SPA was already on screen. The instance
// endpoint and Plane's own `/auth/` endpoints (CSRF token, Keycloak/Gitea
// redirects and callbacks) are public in Plane by design and must bypass the
// proxy's session check; page loads still go through Keycloak.
func TestPlaneSignInEndpointsBypassTheSSOProxySession(t *testing.T) {
	body := readStackFile(t, "application/plane/manifests/sso.yaml")
	for _, want := range []string{
		// The whole API since 2026-10-21 (a superset of the instance fetch).
		`- --skip-auth-route=^/api/`,
		`- --skip-auth-route=^/auth/`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plane's oauth2-proxy lacks %q; a browser with a stale or missing proxy cookie gets 401 on the "+
				"instance fetch and Plane shows its start-up failure page", want)
		}
	}
	// Generated manifest: the regeneration command must carry the same flags
	// or the next hack/gen-sso.sh run silently drops them.
	header := body[:strings.Index(body, "\napiVersion:")]
	if !strings.Contains(header, "--skip-auth-route=^/api/") || !strings.Contains(header, "--skip-auth-route=^/auth/") {
		t.Error("the recorded gen-sso.sh command does not carry the skip-auth routes; regenerating would revert the fix")
	}
}
