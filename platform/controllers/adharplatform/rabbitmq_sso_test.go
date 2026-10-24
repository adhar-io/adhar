package adharplatform

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// RabbitMQ signs the person in with their Keycloak token (2026-10-24). The
// oauth2-proxy in front authenticated them and the management UI then drew its
// own username/password form (`/api/auth` said oauth_enabled:false, every API
// call 401), which only the shared admin password could pass. Three pieces must
// agree: the proxy forwards the token, keeps sessions server-side (a cookie
// holding the tokens exceeds Cowboy's 4 KiB header limit -> HTTP 431), and the
// broker's RENDERED config enables the OAuth2 backend in idp_initiated mode.
func TestRabbitMQSignsInWithTheKeycloakToken(t *testing.T) {
	dir := filepath.Join("..", "..", "stack", "packages", "data", "rabbitmq")
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	sso := read("manifests/sso.yaml")
	for _, want := range []string{
		"            - --pass-authorization-header=true\n",
		"            - --session-store-type=redis\n",
		"            - --cookie-refresh=",
	} {
		if !strings.Contains(sso, want) {
			t.Errorf("rabbitmq-oauth2-proxy lacks %q", strings.TrimSpace(want))
		}
	}
	// --session-cookie-minimal drops the tokens --pass-authorization-header needs.
	if strings.Contains(sso, "            - --session-cookie-minimal=true\n") {
		t.Error("--session-cookie-minimal keeps no token to forward; RabbitMQ would see no Authorization header")
	}

	// The config ships base64-encoded inside Secret/rabbitmq-config; check what
	// the broker will actually read, not the values file.
	install := read("manifests/install.yaml")
	var conf string
	for _, b := range regexp.MustCompile(`[A-Za-z0-9+/]{400,}={0,2}`).FindAllString(install, -1) {
		if d, err := base64.StdEncoding.DecodeString(b); err == nil && strings.Contains(string(d), "cluster_formation") {
			conf = string(d)
		}
	}
	if conf == "" {
		t.Fatal("no rendered rabbitmq.conf found in install.yaml")
	}
	for _, want := range []string{
		"auth_backends.1 = rabbit_auth_backend_oauth2",
		"auth_backends.2 = rabbit_auth_backend_internal", // apps still use the admin user
		"auth_oauth2.resource_server_id = rabbitmq",      // = the client id = the ID token's aud
		"auth_oauth2.jwks_url = https://",                // http is refused at boot
		"auth_oauth2.additional_scopes_key = groups",
		"auth_oauth2.scope_aliases.platform-admin = rabbitmq.tag:administrator",
		"management.oauth_enabled = true",
		"management.oauth_initiated_logon_type = idp_initiated",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("rendered rabbitmq.conf lacks %q", want)
		}
	}
	if !strings.Contains(install, "rabbitmq_auth_backend_oauth2") {
		t.Error("the oauth2 plugin is not enabled")
	}
	if !strings.Contains(read("values.yaml"), "management.oauth_enabled = true") {
		t.Error("values.yaml no longer carries the OAuth config; regenerating would drop it")
	}
}
