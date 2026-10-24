package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ChaosCenter's native Keycloak sign-in (2026-10-24). The auth server reads
// exactly these names (chaoscenter/authentication/pkg/utils/configs.go); a near
// miss like OAUTH_CALLBACK_URLS is ignored silently and Keycloak then rejects an
// authorize request with no redirect_uri.
func TestLitmusSignsInWithKeycloak(t *testing.T) {
	dir := filepath.Join("..", "..", "stack", "packages", "application", "litmus")
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(dir, p)); return string(b) }
	oidc := read("manifests/oidc.yaml")
	for _, key := range []string{"OAUTH_ENABLED: \"true\"", "OIDC_ISSUER: ", "OAUTH_CLIENT_ID: ", "OAUTH_CLIENT_SECRET: ",
		"OAUTH_CALLBACK_URL: https://litmus.adhar.localtest.me:8443/auth/oauth/callback", "OAUTH_SECRET: "} {
		if !strings.Contains(oidc, "        "+key) {
			t.Errorf("litmus-oidc lacks %q", key)
		}
	}
	if strings.Contains(oidc, "OAUTH_CALLBACK_URLS") {
		t.Error("OAUTH_CALLBACK_URLS is not a setting; the auth server reads OAUTH_CALLBACK_URL")
	}
	if !strings.Contains(read("manifests/install.yaml"), "        - secretRef:\n            name: litmus-oidc\n") {
		t.Error("the auth server does not read litmus-oidc")
	}
	if !strings.Contains(read("manifests/sso.yaml"), `"https://litmus.adhar.localtest.me:8443/auth/oauth/callback"`) {
		t.Error("the Keycloak client does not allow the auth server's callback")
	}
	route := read("manifests/httproute.yaml.tmpl")
	for _, want := range []string{"replaceFullPath: /auth/oauth/login", "name: jwtToken", "adhar_sso_litmus=1"} {
		if !strings.Contains(route, want) {
			t.Errorf("litmus route lacks %q", want)
		}
	}
	if !strings.Contains(read("generate-manifests.sh"), "python3 post-render.py manifests/install.yaml") {
		t.Error("generate-manifests.sh no longer runs post-render.py")
	}
}
