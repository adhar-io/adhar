package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Metabase and PostHog sell SSO in their open-source editions, so a Keycloak
// login used to end on the app's OWN login form. hack/sso-bridge turns the
// oauth2-proxy's identity into the app's session (2026-10-24). These pin the
// chain gateway -> proxy -> bridge -> app, and the trust it rests on.
func TestMetabaseAndPostHogSignInThroughTheSSOBridge(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	src, err := os.ReadFile(filepath.Join(root, "hack", "sso-bridge", "sso_bridge.py"))
	if err != nil {
		t.Fatal(err)
	}
	var embedded strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(src), "\n"), "\n") {
		if line == "" {
			embedded.WriteString("\n")
		} else {
			embedded.WriteString("    " + line + "\n")
		}
	}
	for _, c := range []struct{ dir, app, upstream string }{
		{"data/metabase", "metabase", "http://metabase.adhar-system.svc.cluster.local:80"},
		{"application/posthog", "posthog", "http://posthog-web.adhar-system.svc.cluster.local:8000"},
	} {
		dir := filepath.Join(root, "platform", "stack", "packages", c.dir, "manifests")
		read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
		bridge, sso := read("sso-bridge.yaml"), read("sso.yaml")
		if !strings.Contains(bridge, embedded.String()) {
			t.Errorf("%s: sso-bridge.yaml does not embed hack/sso-bridge/sso_bridge.py; run hack/sso-bridge/render.sh", c.app)
		}
		if !strings.Contains(sso, "- --upstream=http://"+c.app+"-sso-bridge.adhar-system.svc.cluster.local:8080\n") {
			t.Errorf("%s: the oauth2-proxy does not forward to the bridge, so users land on the app's login form", c.app)
		}
		// The bridge forwards to the APP; pointing it at the proxy would loop.
		if !strings.Contains(bridge, `{name: UPSTREAM, value: "`+c.upstream+`"}`) {
			t.Errorf("%s: bridge UPSTREAM is not %s", c.app, c.upstream)
		}
		// X-Forwarded-Email is believed only because nothing but the proxy can reach the bridge.
		if !strings.Contains(bridge, "kind: NetworkPolicy") ||
			!strings.Contains(bridge, "            matchLabels:\n              app: "+c.app+"-oauth2-proxy\n") {
			t.Errorf("%s: the bridge is reachable by pods other than its oauth2-proxy", c.app)
		}
		// Every bridged account's password derives from this key; rotating it locks them out.
		if !strings.Contains(bridge, "  refreshInterval: '0'\n  target:\n    name: "+c.app+"-sso-bridge\n") {
			t.Errorf("%s: the bridge key must be minted once", c.app)
		}
	}
}
