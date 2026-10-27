package adharplatform

// Signing in once must be enough. A person who already has a Keycloak session
// (from the console, or any other tool) has to be able to open every platform
// URL and land INSIDE the tool: no second password, and wherever the tool
// allows it, no "Sign in with Keycloak" button either. Keycloak's session is
// what makes that work; each guard below pins one place where a tool used to
// draw its own sign-in page in front of it (2026-10-22 sweep of every route).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every HTTPRoute backend a browser can reach is either an oauth2-proxy (the
// Keycloak edge) or an app that authenticates against Keycloak itself — the
// list below names how. A new route straight at an app's own Service, with no
// entry here, is an admin UI anyone on the network can open, or one that shows
// a login nobody was given.
func TestEveryExposedUIHasAKeycloakSignInPath(t *testing.T) {
	native := map[string]string{
		"adhar-ai-runtime":   "agent runtime API; every call carries a Keycloak bearer (no UI)",
		"adhar-sso-start":    "the same-origin page that STARTS an app's own Keycloak login (Kargo PKCE, Penpot POST); serves only /adhar-sso/",
		"adhar-ai-gateway":   "agentgateway; JWT-validated LLM/MCP API (no UI)",
		"argo-server":        "argo-workflows --auth-mode=sso (disabled in every profile)",
		"coder":              "Coder native OIDC; the route sends a session-less GET / into its OIDC start",
		"coder-sso":          "Coder's sign-in edge: page loads only; asks Coder whether the session is live, starts Coder's own Keycloak OIDC when it is not",
		"console":            "the Adhar Console's own Keycloak sign-in (the place a person signs in once)",
		"faro-alloy":         "browser telemetry collector (no UI)",
		"faro-cors":          "CORS front of the browser telemetry collector (no UI)",
		"harbor":             "Harbor oidc_auth with primary_auth_mode: the UI goes straight to /c/oidc/login",
		"headlamp":           "Headlamp OIDC with -oidc-auto-login=true",
		"kargo-api":          "Kargo OIDC (PKCE in the browser); click-free via adhar-sso-start, which bare loads of / and /login are sent to",
		"keycloak":           "the identity provider itself",
		"libredb-studio":     "LibreDB OIDC; the route redirects /login into its OIDC start",
		"minio-console":      "MinIO console OIDC (disabled in every profile)",
		"openbao-ui":         "OpenBao OIDC auth method, listed on the sign-in page",
		"penpot":             "Penpot OIDC (login-with-oidc); click-free via adhar-sso-start for a load of / with no auth-token",
		"prometheus-grafana": "Grafana generic_oauth with auto_login",
		"proxy-public":       "JupyterHub GenericOAuthenticator; the route redirects /hub/login into oauth_login",
		"tooljet":            "ToolJet's own accounts (disabled in every profile)",
		"tolgee":             "Tolgee native OAuth2 against Keycloak; one click (its sign-in page owns the OAuth2 state), native sign-up closed at the route",
		"vault-ui":           "Vault OIDC auth method (disabled in every profile)",
	}
	// Backends that are not a browser surface at all.
	internal := map[string]bool{"adhar-mcp": true, "llm-anthropic": true, "llm-openai": true, "llm-vllm": true}

	root := filepath.Join(stackRoot(t), "packages")
	tmpl := regexp.MustCompile(`\{\{[^}]*\}\}`)
	routes := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yaml.tmpl") {
			return nil
		}
		// Golden-path skeletons are app templates, not platform tools.
		if strings.Contains(path, string(filepath.Separator)+"skeleton"+string(filepath.Separator)) {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !strings.Contains(string(raw), "kind: HTTPRoute") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		dec := yaml.NewDecoder(strings.NewReader(tmpl.ReplaceAllString(string(raw), "443")))
		for {
			var doc struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Rules []struct {
						BackendRefs []struct {
							Name string `yaml:"name"`
						} `yaml:"backendRefs"`
					} `yaml:"rules"`
				} `yaml:"spec"`
			}
			if derr := dec.Decode(&doc); derr != nil {
				break
			}
			if doc.Kind != "HTTPRoute" {
				continue
			}
			routes++
			for _, rule := range doc.Spec.Rules {
				for _, b := range rule.BackendRefs {
					if strings.HasSuffix(b.Name, "-oauth2-proxy") || internal[b.Name] {
						continue
					}
					if _, ok := native[b.Name]; !ok {
						t.Errorf("%s: routes a browser straight to %q, which is neither an oauth2-proxy nor an app "+
							"with its own Keycloak sign-in — put the package's oauth2-proxy (hack/gen-sso.sh) in front", rel, b.Name)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if routes < 40 {
		t.Fatalf("expected the platform's HTTPRoutes, found %d", routes)
	}
}

// Harbor's UI sends a visitor with no Harbor session straight to its OIDC start
// only when primary_auth_mode is on; otherwise it draws its own sign-in page with
// a "Login via OIDC Provider" button (the auth guard in Harbor's served bundle:
// `auth_mode==oidc_auth && primary_auth_mode ? location.href="/c/oidc/login…"`).
func TestHarborGoesStraightToKeycloak(t *testing.T) {
	body := readStackFile(t, "application/harbor/manifests/oidc.yaml")
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	found := false
	dec := yaml.NewDecoder(strings.NewReader(body))
	for dec.Decode(&cm) == nil {
		raw, ok := cm.Data["oidc-config.json"]
		if !ok {
			cm.Data = nil
			continue
		}
		found = true
		var cfg map[string]any
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("oidc-config.json does not parse: %v", err)
		}
		if cfg["auth_mode"] != "oidc_auth" {
			t.Errorf("auth_mode = %v, want oidc_auth", cfg["auth_mode"])
		}
		if cfg["primary_auth_mode"] != true {
			t.Error("primary_auth_mode is not true: Harbor draws its sign-in page and waits for a click on " +
				"\"Login via OIDC Provider\" instead of going straight to Keycloak")
		}
		break
	}
	if !found {
		t.Fatal("no ConfigMap carrying oidc-config.json in application/harbor/manifests/oidc.yaml")
	}
}

// Gitea's default landing page is a marketing page whose only way in is a
// "Sign In" link. LANDING_PAGE=login sends an anonymous visitor to /user/login,
// which the gitea HTTPRoute already redirects into the Keycloak source. Pinned in
// the generator's values AND the generated manifests, because regenerating one
// from the other is how a hand-edit gets lost.
func TestGiteaLandingPageIsTheKeycloakLogin(t *testing.T) {
	repo := filepath.Clean(filepath.Join(stackRoot(t), "..", ".."))
	for _, f := range []struct{ path, want string }{
		{"hack/gitea/values.yaml", "LANDING_PAGE: login"},
		{"hack/gitea/values-ha.yaml", "LANDING_PAGE: login"},
		{"platform/controllers/adharplatform/resources/gitea/install.yaml", "\n    LANDING_PAGE=login\n"},
		{"platform/controllers/adharplatform/resources/gitea/install-ha.yaml", "\n    LANDING_PAGE=login\n"},
	} {
		b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f.path)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(withoutComments(string(b)), f.want) {
			t.Errorf("%s lacks %q: an anonymous visitor to Gitea's root sees the landing page and has to click "+
				"\"Sign In\" even with a live Keycloak session", f.path, strings.TrimSpace(f.want))
		}
	}
}

// Trino has no authenticator of its own here (the oauth2-proxy is the gate), and
// with none its web UI still draws a username/password form.
func TestTrinoWebUIDoesNotAskForAUser(t *testing.T) {
	for _, f := range []string{"data/trino/values.yaml", "data/trino/manifests/install.yaml"} {
		body := withoutComments(readStackFile(t, f))
		if !strings.Contains(body, "web-ui.authentication.type=fixed") || !strings.Contains(body, "web-ui.user=") {
			t.Errorf("%s: the Trino web UI is not on fixed-user authentication, so it shows its own login form "+
				"behind the Keycloak proxy", f)
		}
	}
}

// OnCall's UI is a plugin inside its own Grafana. Behind the oauth2-proxy that
// Grafana drew a password form; auth.proxy trusts the identity the proxy
// forwards instead. The bare host also has to open the plugin, not the engine
// API's "Ok".
func TestOnCallTrustsTheEdgeProxyIdentity(t *testing.T) {
	install := readStackFile(t, "observability/oncall/manifests/install.yaml")
	ini := install[strings.Index(install, "grafana.ini: |"):]
	ini = ini[:strings.Index(ini, "\n---")]
	for _, want := range []string{"[auth.proxy]", "enabled = true", "header_name = X-Forwarded-Preferred-Username", "header_property = username", "auto_sign_up = true"} {
		if !strings.Contains(ini, want) {
			t.Errorf("oncall grafana.ini lacks %q: Grafana shows its own login behind the Keycloak proxy", want)
		}
	}
	if strings.Contains(ini, "helm-testing-grafana") {
		t.Error("oncall grafana.ini still names the chart's helm-testing-grafana domain")
	}
	// auth.proxy reads headers only the proxy sets.
	if sso := readStackFile(t, "observability/oncall/manifests/sso.yaml"); !strings.Contains(sso, "--pass-user-headers=true") {
		t.Error("the oncall oauth2-proxy does not pass user headers; auth.proxy would have no identity to trust")
	}
	route := readStackFile(t, "observability/oncall/manifests/httproute.yaml.tmpl")
	for _, want := range []string{"type: RequestRedirect", "replaceFullPath: /grafana/a/grafana-oncall-app/", "port: {{ .Port }}", "method: GET"} {
		if !strings.Contains(route, want) {
			t.Errorf("the oncall route lacks %q: the bare host shows oncall-engine's \"Ok\" instead of the UI", want)
		}
	}
}

// The RustFS console's own sign-in page (access-key form + a Keycloak button)
// is skipped: the bare host starts RustFS's OIDC flow, which completes silently
// against the Keycloak session.
func TestRustFSBareHostStartsTheKeycloakLogin(t *testing.T) {
	route := readStackFile(t, "data/rustfs/manifests/httproute.yaml.tmpl")
	for _, want := range []string{"type: RequestRedirect", "replaceFullPath: /rustfs/admin/v3/oidc/authorize/default", "port: {{ .Port }}", "method: GET"} {
		if !strings.Contains(route, want) {
			t.Errorf("the rustfs route lacks %q", want)
		}
	}
	// The root is ALSO the S3 API: the console lists buckets with a signed
	// `GET /` from the page. Only a browser document navigation may be
	// redirected to the OIDC start, and nothing may rewrite `/`. A redirect
	// matched by path and method alone sent that signed call to the authorize
	// URL with its Authorization header intact, and the console showed
	// "Unable to list S3 buckets — The request signature we calculated does
	// not match" (2026-10-27).
	var doc struct {
		Spec struct {
			Rules []struct {
				Matches []struct {
					Path    struct{ Type, Value string } `yaml:"path"`
					Headers []struct{ Name, Value string } `yaml:"headers"`
				} `yaml:"matches"`
				Filters []struct {
					Type string `yaml:"type"`
				} `yaml:"filters"`
			} `yaml:"rules"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(strings.ReplaceAll(route, "{{ .Port }}", "443")), &doc); err != nil {
		t.Fatalf("parsing the rustfs route: %v", err)
	}
	for _, rule := range doc.Spec.Rules {
		redirects, rewrites := false, false
		for _, f := range rule.Filters {
			redirects = redirects || f.Type == "RequestRedirect"
			rewrites = rewrites || f.Type == "URLRewrite"
		}
		for _, m := range rule.Matches {
			if m.Path.Value != "/" || m.Path.Type != "Exact" {
				continue
			}
			if rewrites {
				t.Error("the rustfs route rewrites `/`, which is also S3's ListBuckets")
			}
			if redirects {
				h := map[string]string{}
				for _, x := range m.Headers {
					h[x.Name] = x.Value
				}
				if h["Sec-Fetch-Mode"] != "navigate" || h["Sec-Fetch-Dest"] != "document" {
					t.Error("the rustfs bare-host OIDC redirect matches more than browser document navigations; " +
						"the console's signed `GET /` (ListBuckets) is redirected and fails its signature check")
				}
			}
		}
	}
	// The ESO-templated file must not grow the route back: its {{ }} would be
	// read as Go template syntax if it were ever renamed to .tmpl.
	if strings.Contains(readStackFile(t, "data/rustfs/manifests/console-sso.yaml"), "kind: HTTPRoute") {
		t.Error("console-sso.yaml declares an HTTPRoute again; the route lives in httproute.yaml.tmpl")
	}
}

// OpenBao's sign-in page offers only the methods listed for unauthenticated
// users; without it the page opens on Token and Keycloak is hidden.
func TestOpenBaoListsTheKeycloakMethodOnItsSignInPage(t *testing.T) {
	boot := withoutComments(readStackFile(t, "security/openbao/manifests/bootstrap.yaml"))
	if !strings.Contains(boot, "bao auth tune -listing-visibility=unauth oidc/") {
		t.Error("the OpenBao bootstrap does not list the oidc method on the sign-in page")
	}
}
