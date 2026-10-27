package adharplatform

// Tolgee must sign a person who already has a Keycloak session straight in
// (2026-10-27). Live on GCP every load of https://tolgee.<host>/ showed Tolgee's
// own sign-in page and waited for a click on "Log in with OAuth2": the route
// had no way into Keycloak at all, because Tolgee's button carries a `state`
// that its sign-in page writes to localStorage and its callback refuses any
// other, so no Gateway redirect can start the flow. A same-origin start page
// (security/keycloak/manifests/sso-start.yaml, tolgee.html) can, and these
// guards pin the three things that have to agree for it to work without ever
// looping: the route, the page, and what Tolgee itself reads.

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
)

const tolgeeStartPage = "/adhar-sso/tolgee.html"

type tolgeeMatch struct {
	Path *struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"path"`
	Method      string                              `json:"method"`
	Headers     []struct{ Type, Name, Value string } `json:"headers"`
	QueryParams []struct{ Type, Name, Value string } `json:"queryParams"`
}

type tolgeeRule struct {
	Matches []tolgeeMatch `json:"matches"`
	Filters []struct {
		Type            string `json:"type"`
		RequestRedirect *struct {
			Path struct {
				ReplaceFullPath string `json:"replaceFullPath"`
			} `json:"path"`
			Port int `json:"port"`
		} `json:"requestRedirect"`
	} `json:"filters"`
	BackendRefs []struct {
		Name string `json:"name"`
		Port int    `json:"port"`
	} `json:"backendRefs"`
}

type tolgeeHTTPRoute struct {
	Spec struct {
		Rules []tolgeeRule `json:"rules"`
	} `json:"spec"`
}

// tolgeeRenderedRoute stages the package exactly as the controller seeds it into
// Gitea (templates rendered, host rewritten) and returns its route.
func tolgeeRenderedRoute(t *testing.T) tolgeeHTTPRoute {
	t.Helper()
	spec := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "dev.adhar.io", Port: "443"}
	staged, cleanup, err := StageStack(filepath.Join(stackPackagesDir(t), "application", "tolgee", "manifests"), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(staged, "httproute.yaml"))
	if err != nil {
		t.Fatalf("the tolgee route did not render to httproute.yaml: %v", err)
	}
	if strings.Contains(string(raw), "{{") {
		t.Fatal("unrendered template left in the tolgee route")
	}
	var r tolgeeHTTPRoute
	if err := yaml.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func tolgeePrefixMatches(prefix, path string) bool {
	p := strings.TrimSuffix(prefix, "/")
	return p == "" || path == p || strings.HasPrefix(path, p+"/")
}

func tolgeeValueMatches(typ, want, got string) bool {
	if typ == "RegularExpression" {
		// Envoy's safe_regex (what Cilium programs) matches the WHOLE value.
		return regexp.MustCompile("^(?:" + want + ")$").MatchString(got)
	}
	return want == got
}

// tolgeeRuleFor returns the rule the Gateway hands a request to, by Gateway API
// precedence: an Exact path, then the longest prefix, then a METHOD match, then
// the most header matches, then the most query matches, then list order. (Cilium
// 1.20 programs Envoy routes in exactly this order; a method match outranking
// any number of headers is what defeats a marker-cookie pass-through placed
// against a `method: GET` redirect.)
func tolgeeRuleFor(r tolgeeHTTPRoute, method, target string, headers map[string]string) *tolgeeRule {
	u, err := url.Parse(target)
	if err != nil {
		panic(err)
	}
	q := u.Query()
	var best *tolgeeRule
	var bestKey [5]int
	for i := range r.Spec.Rules {
		rule := &r.Spec.Rules[i]
		matches := rule.Matches
		if len(matches) == 0 {
			matches = []tolgeeMatch{{}}
		}
	match:
		for _, m := range matches {
			typ, val := "PathPrefix", "/"
			if m.Path != nil {
				typ, val = m.Path.Type, m.Path.Value
			}
			var key [5]int
			switch typ {
			case "Exact":
				if u.Path != val {
					continue
				}
				key[0] = 1
			case "PathPrefix":
				if !tolgeePrefixMatches(val, u.Path) {
					continue
				}
				key[1] = len(val)
			default:
				continue
			}
			if m.Method != "" {
				if m.Method != method {
					continue
				}
				key[2] = 1
			}
			for _, h := range m.Headers {
				got, ok := "", false
				for k, v := range headers {
					if strings.EqualFold(k, h.Name) {
						got, ok = v, true
					}
				}
				if !ok || !tolgeeValueMatches(h.Type, h.Value, got) {
					continue match
				}
			}
			key[3] = len(m.Headers)
			for _, p := range m.QueryParams {
				if !q.Has(p.Name) || !tolgeeValueMatches(p.Type, p.Value, q.Get(p.Name)) {
					continue match
				}
			}
			key[4] = len(m.QueryParams)
			if best == nil || tolgeeKeyGreater(key, bestKey) {
				best, bestKey = rule, key
			}
		}
	}
	return best
}

func tolgeeKeyGreater(a, b [5]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false // a tie keeps the earlier rule
}

// tolgeeOutcome describes what a rule does: "→<backend>" or "302 <path>".
func tolgeeOutcome(rule *tolgeeRule) string {
	if rule == nil {
		return "no rule (404)"
	}
	for _, f := range rule.Filters {
		if f.RequestRedirect != nil {
			return "302 " + f.RequestRedirect.Path.ReplaceFullPath
		}
	}
	if len(rule.BackendRefs) > 0 {
		return "→" + rule.BackendRefs[0].Name
	}
	return "no backend"
}

// tolgeeStartPageHTML returns the tolgee.html page from the shared start-page
// manifest, and whether its Deployment mounts it.
func tolgeeStartPageHTML(t *testing.T) (string, bool) {
	t.Helper()
	docs := yamlDocs(t, filepath.Join(stackPackagesDir(t), "security", "keycloak", "manifests", "sso-start.yaml"))
	page, mounted := "", false
	for _, d := range docs {
		switch {
		case d["kind"] == "ConfigMap" && dig(d, "metadata", "name") == "adhar-sso-start":
			page, _ = dig(d, "data", "tolgee.html").(string)
		case d["kind"] == "Deployment" && dig(d, "metadata", "name") == "adhar-sso-start":
			vols, _ := dig(d, "spec", "template", "spec", "volumes").([]any)
			for _, v := range vols {
				vm, _ := v.(map[string]any)
				items, _ := dig(vm, "configMap", "items").([]any)
				for _, it := range items {
					im, _ := it.(map[string]any)
					if im["key"] == "tolgee.html" && im["path"] == "tolgee.html" {
						mounted = true
					}
				}
			}
		}
	}
	return page, mounted
}

// The route: a bare load of the home or sign-in page starts the Keycloak
// sign-in, and EVERYTHING that must reach Tolgee — the marker, the break-glass
// page, the OAuth2 return, the API — does.
func TestTolgeeRouteStartsTheKeycloakSignInAndNeverLoops(t *testing.T) {
	r := tolgeeRenderedRoute(t)
	marker := map[string]string{"Cookie": "theme=dark; adhar_sso_tolgee=1"}
	for _, c := range []struct {
		why, method, target string
		headers             map[string]string
		want                string
	}{
		{"a bare load of the home page starts the Keycloak sign-in", "GET", "/", nil, "302 " + tolgeeStartPage},
		{"a bare load of the sign-in page starts the Keycloak sign-in", "GET", "/login", nil, "302 " + tolgeeStartPage},
		{"once the start page has run, the home page must reach Tolgee or the page loops", "GET", "/", marker, "→tolgee"},
		{"once the start page has run, the sign-in page must reach Tolgee or the page loops", "GET", "/login", marker, "→tolgee"},
		{"the break-glass password sign-in (and the page's fallback) must reach Tolgee", "GET", "/login?local=1", nil, "→tolgee"},
		{"the start page itself is served on Tolgee's host", "GET", tolgeeStartPage, nil, "→adhar-sso-start"},
		{"Keycloak's return must reach Tolgee's own callback, which checks the state", "GET", "/login/auth_callback/oauth2?code=c&state=s", nil, "→tolgee"},
		{"deep links are left to Tolgee", "GET", "/projects/1/translations", nil, "→tolgee"},
		{"the CLI and SDKs (API keys) are never redirected", "GET", "/v2/projects", map[string]string{"X-API-Key": "tgpak_x"}, "→tolgee"},
		{"the public configuration the start page reads", "GET", "/api/public/configuration", nil, "→tolgee"},
		{"native password sign-up stays closed", "POST", "/api/public/sign_up", nil, "302 /login"},
		{"the sign-up page stays closed", "GET", "/sign_up", nil, "302 /login"},
	} {
		if got := tolgeeOutcome(tolgeeRuleFor(r, c.method, c.target, c.headers)); got != c.want {
			t.Errorf("%s %s (cookie %q): got %s, want %s — %s", c.method, c.target, c.headers["Cookie"], got, c.want, c.why)
		}
	}
	for _, rule := range r.Spec.Rules {
		for _, f := range rule.Filters {
			if f.RequestRedirect != nil && f.RequestRedirect.Path.ReplaceFullPath == tolgeeStartPage && f.RequestRedirect.Port != 443 {
				t.Errorf("the start-page redirect port rendered as %d, want the platform port 443", f.RequestRedirect.Port)
			}
		}
	}
}

// The page and the route must agree, or the page loops: every place the page
// sends the browser has to reach Tolgee WITHOUT the marker (a browser that
// blocks cookies never carries one), and the cookie it sets has to be the one
// the route lets through.
func TestTolgeeStartPageOnlySendsTheBrowserWhereTheRouteLetsItThrough(t *testing.T) {
	page, _ := tolgeeStartPageHTML(t)
	if page == "" {
		t.Fatal("sso-start.yaml has no tolgee.html page")
	}
	r := tolgeeRenderedRoute(t)
	targets := map[string]bool{}
	for _, m := range regexp.MustCompile(`location\.replace\('([^']+)'\)`).FindAllStringSubmatch(page, -1) {
		targets[m[1]] = true
	}
	redirect := regexp.MustCompile(`'redirect_uri', location\.origin \+ '([^']+)'`).FindStringSubmatch(page)
	if redirect == nil {
		t.Fatal("the tolgee start page does not send Keycloak back to a path on Tolgee's own origin")
	}
	targets[redirect[1]+"?code=c&state=s"] = true
	if len(targets) < 3 {
		t.Fatalf("expected the page's bounce, fallback and OAuth2 return targets, found %v", targets)
	}
	for target := range targets {
		if got := tolgeeOutcome(tolgeeRuleFor(r, "GET", target, nil)); got != "→tolgee" {
			t.Errorf("the start page sends the browser to %s, which the route answers with %s instead of Tolgee: "+
				"without a marker cookie that is a redirect loop", target, got)
		}
	}
	cookie := regexp.MustCompile(`document\.cookie = '([A-Za-z0-9_]+)=1;`).FindStringSubmatch(page)
	if cookie == nil {
		t.Fatal("the tolgee start page sets no marker cookie")
	}
	if got := tolgeeOutcome(tolgeeRuleFor(r, "GET", "/", map[string]string{"Cookie": cookie[1] + "=1"})); got != "→tolgee" {
		t.Errorf("the page sets %s but the route answers / carrying it with %s", cookie[1], got)
	}
}

// What the page writes is what Tolgee 3.x reads. The keys are Tolgee webapp
// internals (useAuthService.tsx `oauth2State`, TokenService.ts `jwtToken`,
// OAuthService.tsx's parameters, OAuthRedirectionHandler's callback route),
// verified against the served v3.236.1 bundle, so the major version is pinned
// here too: a Tolgee 4 must be re-read before this page is trusted with it.
func TestTolgeeStartPageWritesWhatTolgeeReads(t *testing.T) {
	page, mounted := tolgeeStartPageHTML(t)
	if !mounted {
		t.Error("the adhar-sso-start Deployment does not mount tolgee.html (ConfigMap volume `items`): " +
			"the route would send every visitor to a 404")
	}
	js := withoutComments(page)
	for _, want := range []string{
		"STATE = 'oauth2State'",
		"localStorage.setItem(STATE, state)",
		"TOKEN = 'jwtToken'",
		"fetch('/v2/user', { headers: { authorization: 'Bearer ' + t }",
		"fetch('/api/public/configuration'",
		"new URL(o.authorizationUrl)",
		"u.searchParams.set('client_id', o.clientId)",
		"u.searchParams.set('redirect_uri', location.origin + '/login/auth_callback/oauth2')",
		"u.searchParams.set('response_type', 'code')",
		"u.searchParams.set('scope', o.scopes.join(' '))",
		"u.searchParams.set('state', state)",
		"location.replace('/login?local=1')",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the tolgee start page lacks %q", want)
		}
	}
	// The callback the page names is the one the Keycloak client registers.
	var client struct {
		RedirectURIs []string `json:"redirectUris"`
	}
	cm := docNamed(tolgeeDocs(t, "keycloak-client.yaml"), "ConfigMap", "tolgee-keycloak-client")
	if err := json.Unmarshal([]byte(toStr(dig(cm, "data", "client.json"))), &client); err != nil {
		t.Fatalf("client.json: %v", err)
	}
	registered := false
	for _, u := range client.RedirectURIs {
		registered = registered || u == "https://tolgee.adhar.localtest.me:8443/login/auth_callback/oauth2"
	}
	if !registered {
		t.Errorf("the Keycloak tolgee client registers %v, not the callback the start page sends Keycloak back to", client.RedirectURIs)
	}
	img := toStr(firstContainer(t, docNamed(tolgeeDocs(t, "install.yaml"), "Deployment", "tolgee"))["image"])
	if !strings.HasPrefix(img, "docker.io/tolgee/tolgee:v3.") {
		t.Errorf("tolgee runs %s: the start page writes Tolgee 3.x webapp internals (localStorage oauth2State/jwtToken, "+
			"/login/auth_callback/oauth2); re-read the new webapp, then move this pin", img)
	}
}
