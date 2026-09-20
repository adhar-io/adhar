package adharplatform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils/files"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// decodeDocs splits an embedded multi-document manifest into generic maps,
// rendering it through the SAME template pass the controller uses. That pass is
// not incidental to the test: a manifest carrying a template action the renderer
// cannot resolve fails at apply time and the object is silently never created,
// which is exactly how the Argo CD SSO proxy went missing (ExternalSecrets' own
// placeholder syntax was read as a platform config field).
func decodeDocs(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	raw, err := argoCDFS.ReadFile(path)
	if err != nil {
		raw, err = giteaFS.ReadFile(path)
	}
	require.NoError(t, err, "reading %s", path)

	rendered, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{
		Protocol:   "https",
		Host:       "adhar.example.com",
		Port:       "8443",
		PortSuffix: ":8443",
	})
	require.NoError(t, err, "%s carries a template action the controller cannot resolve", path)

	var docs []map[string]interface{}
	for _, part := range strings.Split(string(rendered), "\n---") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		var doc map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(part), &doc), "parsing a document of %s", path)
		if len(doc) > 0 {
			docs = append(docs, doc)
		}
	}
	return docs
}

func findByKind(docs []map[string]interface{}, kind, name string) map[string]interface{} {
	for _, doc := range docs {
		if doc["kind"] != kind {
			continue
		}
		meta, _ := doc["metadata"].(map[string]interface{})
		if meta != nil && meta["name"] == name {
			return doc
		}
	}
	return nil
}

// The reconciler repoints a route it looks up BY NAME. When that name did not
// match the manifest ("argocd" against the real "argocd-server") the switch
// silently never happened and Argo CD kept serving its own login page, which is
// invisible in every test that does not compare the two.
func TestArgoCDSSOProxyTargetsTheRealRouteName(t *testing.T) {
	docs := decodeDocs(t, "resources/argocd/post-install.yaml")
	require.NotNil(t, findByKind(docs, "HTTPRoute", proxyRouteName),
		"resources/argocd/post-install.yaml has no HTTPRoute named %q, so reconcileArgoCDSSOProxy can never repoint it", proxyRouteName)
}

// The proxy is only single sign-on if it accepts the session another app
// established: that needs the SHARED cookie secret, a parent-domain cookie, and
// no provider button. It must also hand Argo CD a verifiable bearer token rather
// than a spoofable identity header.
func TestArgoCDSSOProxyUsesTheSharedPlatformSession(t *testing.T) {
	docs := decodeDocs(t, "resources/argocd/sso-proxy.yaml")

	es := findByKind(docs, "ExternalSecret", "argocd-oauth2-proxy")
	require.NotNil(t, es, "the SSO proxy has no ExternalSecret")
	assert.Contains(t, mustYAML(t, es), "adhar-sso-cookie",
		"the proxy must read the platform cookie secret, not mint its own")
	assert.NotContains(t, containerArgsJoined(t, findByKind(docs, "Deployment", "argocd-oauth2-proxy")), "--cookie-domain=",
		"a parent-domain cookie lets sibling proxies read this session")

	deploy := findByKind(docs, "Deployment", "argocd-oauth2-proxy")
	require.NotNil(t, deploy, "the SSO proxy has no Deployment")
	args := containerArgs(t, deploy)

	for _, want := range []string{
		// Its OWN cookie name, and no --cookie-domain: a shared name on the
		// parent domain made this proxy reuse another app's session and forward
		// a token minted for the wrong OIDC client.
		"--cookie-name=_adhar_sso_argocd",
		"--skip-provider-button=true",
		// --pass-authorization-header sends the ID token UPSTREAM. The
		// similarly named --set-authorization-header only sets it on the
		// response to the browser, so Argo CD received nothing and fell back to
		// its own login screen while still returning HTTP 200.
		"--pass-authorization-header=true",
	} {
		assert.Contains(t, args, want, "missing %s: without it the user still sees a login step", want)
	}

	// Fronting the UI must not silently remove CLI access.
	joined := strings.Join(args, " ")
	for _, path := range []string{"^/api/v1/session$", `^/[a-z]+\.[A-Za-z]+Service/`, "^/api/webhook"} {
		assert.Contains(t, joined, "--skip-auth-route="+path,
			"the %s path must bypass the proxy or the argocd CLI and webhooks break", path)
	}
}

// Gitea's login page is server-rendered, so the gateway can redirect it straight
// to Keycloak. Three things make that safe, and all three are easy to lose:
// the redirect is GET-only (the local form POSTs to the same path), the
// break-glass rule carries more query matches than the redirect (how Gateway API
// breaks the tie), and the catch-all still reaches Gitea for git traffic.
func TestGiteaRouteRedirectsOnlyTheLoginPage(t *testing.T) {
	docs := decodeDocs(t, "resources/gitea/post-install.yaml")
	route := findByKind(docs, "HTTPRoute", "gitea-server")
	require.NotNil(t, route, "no gitea-server HTTPRoute")

	spec := route["spec"].(map[string]interface{})
	rules, _ := spec["rules"].([]interface{})
	require.Len(t, rules, 3, "expected break-glass, redirect and catch-all rules")

	breakGlass := rules[0].(map[string]interface{})
	redirect := rules[1].(map[string]interface{})
	catchAll := rules[2].(map[string]interface{})

	bgMatch := firstMatch(t, breakGlass)
	assert.Equal(t, "GET", bgMatch["method"])
	bgQuery, _ := bgMatch["queryParams"].([]interface{})
	require.Len(t, bgQuery, 1, "the break-glass rule needs a query match to outrank the redirect")

	rdMatch := firstMatch(t, redirect)
	assert.Equal(t, "GET", rdMatch["method"],
		"a redirect that also catches POST /user/login breaks local sign-in entirely")
	assert.Nil(t, rdMatch["queryParams"],
		"the redirect must carry fewer query matches than the break-glass rule")
	assert.Contains(t, mustYAML(t, redirect), "/user/oauth2/keycloak",
		"the redirect must point at the Keycloak auth source registered by gitea-oauth-config")
	// Gateway API defaults a redirect's port to the scheme default, which sent
	// local browsers to :443 instead of the published :8443.
	filters, _ := redirect["filters"].([]interface{})
	require.NotEmpty(t, filters)
	rr, _ := filters[0].(map[string]interface{})["requestRedirect"].(map[string]interface{})
	require.NotNil(t, rr)
	assert.NotNil(t, rr["port"],
		"the redirect must state its port or the Location header drops to the scheme default")
	assert.Nil(t, redirect["backendRefs"], "a redirect rule must not also have a backend")

	caMatch := firstMatch(t, catchAll)
	path := caMatch["path"].(map[string]interface{})
	assert.Equal(t, "PathPrefix", path["type"])
	assert.Equal(t, "/", path["value"], "git clone/push and the API must still reach Gitea")
	assert.Contains(t, mustYAML(t, catchAll), "gitea-http")
}

func firstMatch(t *testing.T, rule map[string]interface{}) map[string]interface{} {
	t.Helper()
	matches, _ := rule["matches"].([]interface{})
	require.NotEmpty(t, matches)
	return matches[0].(map[string]interface{})
}

func containerArgsJoined(t *testing.T, deploy map[string]interface{}) string {
	t.Helper()
	return strings.Join(containerArgs(t, deploy), " ")
}

func containerArgs(t *testing.T, deploy map[string]interface{}) []string {
	t.Helper()
	spec := deploy["spec"].(map[string]interface{})
	tmpl := spec["template"].(map[string]interface{})
	podSpec := tmpl["spec"].(map[string]interface{})
	containers, _ := podSpec["containers"].([]interface{})
	require.NotEmpty(t, containers)
	raw, _ := containers[0].(map[string]interface{})["args"].([]interface{})
	out := make([]string, 0, len(raw))
	for _, a := range raw {
		out = append(out, a.(string))
	}
	return out
}

// mustYAML renders a decoded document back to text so a test can assert on a
// value's presence without walking every intermediate map.
func mustYAML(t *testing.T, v interface{}) string {
	t.Helper()
	return fmt.Sprintf("%v", v)
}

// Every embedded bootstrap manifest must render against the platform config.
// This is the general form of the defect that hid the Argo CD SSO proxy: a
// template action the renderer cannot resolve makes applyManifest fail, and the
// failure is easy to swallow, so the object is simply never created and the
// symptom shows up as a missing feature rather than an error. Anything that
// legitimately needs literal braces in a bootstrap manifest (ExternalSecrets or
// Argo CD notification templates, say) has to avoid this pass — usually by
// mapping keys directly instead of templating them.
func TestEveryBootstrapManifestRenders(t *testing.T) {
	cfg := v1alpha1.BuildCustomizationSpec{
		Protocol:   "https",
		Host:       "adhar.example.com",
		Port:       "8443",
		PortSuffix: ":8443",
	}
	err := filepath.Walk("resources", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if _, tErr := files.ApplyTemplate(raw, cfg); tErr != nil {
			t.Errorf("%s does not render, so the controller can never apply it: %v", path, tErr)
		}
		return nil
	})
	require.NoError(t, err, "walking the embedded resources")
}

// The SSO proxy manifest builds absolute URLs from the platform host: the
// Keycloak issuer, the OAuth callback and the cookie domain. Rendered without a
// host it yields "https://keycloak./realms/adhar" — a name ending in a bare dot —
// and the proxy crash-loops on OIDC discovery with "lookup keycloak. no such
// host". The in-cluster controller-manager reconciles the same manifest and,
// running without a configured host, REPLACED a working proxy with a broken one
// (2026-09-20).
func TestSSOProxyManifestDependsOnTheHost(t *testing.T) {
	raw, err := argoCDFS.ReadFile("resources/argocd/sso-proxy.yaml")
	require.NoError(t, err)

	// Every host-derived URL must actually interpolate the host, so an empty one
	// is detectable rather than producing a plausible-looking broken name.
	body := string(raw)
	for _, want := range []string{
		"https://keycloak.{{ .Host }}",
		"--cookie-name=",
	} {
		assert.Contains(t, body, want,
			"the manifest must build %q from the platform host", want)
	}

	// Rendering with an empty host must produce the bare-dot hostname this guard
	// exists to prevent — proving the guard is load-bearing rather than defensive
	// decoration.
	broken, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{Protocol: "https"})
	require.NoError(t, err)
	assert.Contains(t, string(broken), "https://keycloak./",
		"an empty host yields a hostname ending in a dot; reconcileArgoCDSSOProxy must refuse to apply that")
}
