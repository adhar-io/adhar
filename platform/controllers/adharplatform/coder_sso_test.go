package adharplatform

// Coder signs people in through Keycloak natively, and a person who already
// has a Keycloak session must land inside Coder from ANY entry point with no
// click: the bare host, a deep link, /login, and — the case that kept
// regressing — a browser still holding a `coder_session_token` whose session
// has ended. Coder v2.37 keeps that cookie for a year (coderd createAPIKey,
// Max-Age 365 d) while the key behind it expires after 24 h idle, so a route
// that decides by the cookie's PRESENCE served the dashboard to a dead session
// and the SPA fell through to Coder's own password page.
//
// The fix is the coder-sso edge (application/coder/manifests/sso-edge.yaml):
// every page load goes there, it asks Coder (/api/v2/users/me) whether the
// cookie is a live session, and starts Coder's Keycloak sign-in when it is not.
// The guards below pin each part of that.

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	coderRouteFile = "application/coder/manifests/httproute.yaml.tmpl"
	coderEdgeFile  = "application/coder/manifests/sso-edge.yaml"
)

// coderRouteMatch is one HTTPRouteMatch, flattened with the rule it belongs to.
type coderRouteMatch struct {
	pathType, path string
	method         string
	headers        []struct{ typ, name, value string }
	backend        string // Service name, or "redirect:<path>"
	order          int    // position in the route, for the stable sort
}

func coderRoutes(t *testing.T) []coderRouteMatch {
	t.Helper()
	raw := strings.ReplaceAll(readStackFile(t, coderRouteFile), "{{ .Port }}", "443")
	var route struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Rules []struct {
				Matches []struct {
					Path struct {
						Type  string `yaml:"type"`
						Value string `yaml:"value"`
					} `yaml:"path"`
					Method  string `yaml:"method"`
					Headers []struct {
						Type  string `yaml:"type"`
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"headers"`
				} `yaml:"matches"`
				Filters []struct {
					Type            string `yaml:"type"`
					RequestRedirect struct {
						Path struct {
							ReplaceFullPath string `yaml:"replaceFullPath"`
						} `yaml:"path"`
					} `yaml:"requestRedirect"`
				} `yaml:"filters"`
				BackendRefs []struct {
					Name string `yaml:"name"`
				} `yaml:"backendRefs"`
			} `yaml:"rules"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(raw), &route); err != nil || route.Kind != "HTTPRoute" {
		t.Fatalf("%s is not an HTTPRoute: %v", coderRouteFile, err)
	}
	var out []coderRouteMatch
	for _, rule := range route.Spec.Rules {
		backend := ""
		for _, f := range rule.Filters {
			if f.Type == "RequestRedirect" {
				backend = "redirect:" + f.RequestRedirect.Path.ReplaceFullPath
			}
		}
		if backend == "" && len(rule.BackendRefs) > 0 {
			backend = rule.BackendRefs[0].Name
		}
		for _, m := range rule.Matches {
			cm := coderRouteMatch{pathType: m.Path.Type, path: m.Path.Value, method: m.Method, backend: backend, order: len(out)}
			if cm.pathType == "" {
				cm.pathType, cm.path = "PathPrefix", "/"
			}
			for _, h := range m.Headers {
				typ := h.Type
				if typ == "" {
					typ = "Exact"
				}
				cm.headers = append(cm.headers, struct{ typ, name, value string }{typ, h.Name, h.Value})
			}
			out = append(out, cm)
		}
	}
	// Cilium's route sort (operator/pkg/model/translation/envoy_virtual_host.go,
	// SortableRoute.Less, v1.20): exact-path length, then prefix length (a
	// non-root prefix loses its trailing slash), then a method before none,
	// then the number of header matchers — which counts :method too — and
	// finally list order (sort.Stable).
	prefixLen := func(m coderRouteMatch) int {
		if m.pathType != "PathPrefix" {
			return 0
		}
		if m.path == "/" {
			return 1
		}
		return len(strings.TrimSuffix(m.path, "/"))
	}
	exactLen := func(m coderRouteMatch) int {
		if m.pathType == "Exact" {
			return len(m.path)
		}
		return 0
	}
	headerCount := func(m coderRouteMatch) int {
		n := len(m.headers)
		if m.method != "" {
			n++
		}
		return n
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if exactLen(a) != exactLen(b) {
			return exactLen(a) > exactLen(b)
		}
		if prefixLen(a) != prefixLen(b) {
			return prefixLen(a) > prefixLen(b)
		}
		if (a.method == "") != (b.method == "") {
			return a.method != ""
		}
		if a.method != b.method {
			return a.method < b.method
		}
		return headerCount(a) > headerCount(b)
	})
	return out
}

// coderRouteFor returns the backend the first matching route sends a request to.
func coderRouteFor(t *testing.T, routes []coderRouteMatch, method, path string, headers map[string]string) string {
	t.Helper()
	for _, r := range routes {
		switch r.pathType {
		case "Exact":
			if path != r.path {
				continue
			}
		case "PathPrefix":
			p := strings.TrimSuffix(r.path, "/")
			if p != "" && path != p && !strings.HasPrefix(path, p+"/") {
				continue
			}
		default:
			t.Fatalf("route match type %q is not modelled by this test", r.pathType)
		}
		if r.method != "" && r.method != method {
			continue
		}
		ok := true
		for _, h := range r.headers {
			got, present := "", false
			for k, v := range headers {
				if strings.EqualFold(k, h.name) {
					got, present = v, true
				}
			}
			switch h.typ {
			case "Exact":
				ok = ok && present && got == h.value
			case "RegularExpression":
				ok = ok && present && regexp.MustCompile("^(?:"+h.value+")$").MatchString(got)
			default:
				t.Fatalf("header match type %q is not modelled by this test", h.typ)
			}
		}
		if ok {
			return r.backend
		}
	}
	return ""
}

// Every page a browser loads reaches the session check — whatever cookie it
// carries — and nothing that is not a page load ever does.
func TestCoderPageLoadsGoThroughTheSessionCheck(t *testing.T) {
	routes := coderRoutes(t)
	page := func(extra ...string) map[string]string {
		h := map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Accept": "text/html"}
		for i := 0; i+1 < len(extra); i += 2 {
			h[extra[i]] = extra[i+1]
		}
		return h
	}
	stale := "coder_session_token=AbCdEfGhIj-0123456789abcdefghijkl"
	for _, c := range []struct {
		name, method, path string
		headers            map[string]string
		want               string
	}{
		{"bare host, no cookie", "GET", "/", page(), "coder-sso"},
		// The regression: a year-long cookie over a 24 h session.
		{"bare host, stale session cookie", "GET", "/", page("Cookie", stale), "coder-sso"},
		{"deep link /workspaces", "GET", "/workspaces", page(), "coder-sso"},
		{"deep link /templates, stale cookie", "GET", "/templates", page("Cookie", stale), "coder-sso"},
		{"deep link to a workspace", "GET", "/@user1/dev", page("Cookie", stale), "coder-sso"},
		{"a load or reload of /login", "GET", "/login", page(), "coder-sso"},
		{"`coder login` opening /cli-auth", "GET", "/cli-auth", page(), "coder-sso"},
		{"a path-based workspace app", "GET", "/@user1/dev.main/apps/code-server/", page("Cookie", stale), "coder-sso"},
		// Must reach Coder whatever the edge is doing.
		{"Keycloak returning to the OIDC callback", "GET", "/api/v2/users/oidc/callback", page("Cookie", "oauth_state=x"), "coder"},
		{"the OIDC start", "GET", "/api/v2/users/oidc/callback", page(), "coder"},
		// Not page loads.
		{"dashboard XHR", "GET", "/api/v2/users/me", map[string]string{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty", "Cookie": stale}, "coder"},
		{"dashboard websocket", "GET", "/api/v2/workspaceagents/x/pty", map[string]string{"Sec-Fetch-Mode": "websocket", "Sec-Fetch-Dest": "empty"}, "coder"},
		{"coder CLI", "GET", "/api/v2/buildinfo", map[string]string{"Coder-Session-Token": "t"}, "coder"},
		{"workspace agent DERP", "GET", "/derp", map[string]string{}, "coder"},
		{"an app framed by the dashboard", "GET", "/@user1/dev.main/apps/code-server/", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}, "coder"},
		{"password sign-in POST", "POST", "/api/v2/users/login", map[string]string{"Sec-Fetch-Mode": "cors"}, "coder"},
		{"a form POST navigation", "POST", "/login", page(), "coder"},
		// A browser without Fetch Metadata keeps the old behaviour on `/`.
		{"old browser, GET / with a cookie", "GET", "/", map[string]string{"Cookie": stale}, "coder"},
		{"old browser, GET / without one", "GET", "/", map[string]string{}, "redirect:/api/v2/users/oidc/callback"},
	} {
		if got := coderRouteFor(t, routes, c.method, c.path, c.headers); got != c.want {
			t.Errorf("%s (%s %s): routed to %q, want %q", c.name, c.method, c.path, got, c.want)
		}
	}
	// And nothing reaches the edge without being a GET page load: the CLI,
	// agents and API tokens must never depend on it.
	for _, r := range routes {
		if r.backend != "coder-sso" {
			continue
		}
		need := map[string]bool{"Sec-Fetch-Mode=navigate": false, "Sec-Fetch-Dest=document": false}
		for _, h := range r.headers {
			if h.typ == "Exact" {
				if _, ok := need[h.name+"="+h.value]; ok {
					need[h.name+"="+h.value] = true
				}
			}
		}
		if r.method != "GET" || !need["Sec-Fetch-Mode=navigate"] || !need["Sec-Fetch-Dest=document"] {
			t.Errorf("a route match for %s %q sends traffic to coder-sso without requiring a GET page load "+
				"(Sec-Fetch-Mode: navigate + Sec-Fetch-Dest: document)", r.pathType, r.path)
		}
	}
}

// coderEdge returns the coder-sso ConfigMap data and Deployment.
func coderEdge(t *testing.T) (map[string]string, map[string]any) {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(readStackFile(t, coderEdgeFile)))
	var data map[string]string
	var deploy map[string]any
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		name, _ := dig(doc, "metadata", "name").(string)
		if name != "coder-sso" {
			continue
		}
		switch doc["kind"] {
		case "ConfigMap":
			data = map[string]string{}
			raw, _ := doc["data"].(map[string]any)
			for k, v := range raw {
				data[k], _ = v.(string)
			}
		case "Deployment":
			deploy = doc
		}
	}
	if data["nginx.conf"] == "" || data["coder.js"] == "" || deploy == nil {
		t.Fatalf("%s lacks the coder-sso ConfigMap (nginx.conf, coder.js) or Deployment", coderEdgeFile)
	}
	return data, deploy
}

// nginxBlocks returns the body of every `location <spec> { … }` in an nginx
// config, keyed by spec, with `#` comment lines removed.
func nginxBlocks(t *testing.T, conf string) map[string]string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(conf, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines = append(lines, l)
		}
	}
	src := strings.Join(lines, "\n")
	out := map[string]string{}
	re := regexp.MustCompile(`(?m)^\s*location\s+([^{]+?)\s*\{`)
	for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
		spec := src[m[2]:m[3]]
		depth, i := 1, m[1]
		for ; i < len(src) && depth > 0; i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		out[spec] = src[m[1] : i-1]
	}
	return out
}

// The edge does what its header says: Coder decides whether the session is
// live, a dead one is sent into Coder's own Keycloak sign-in for the page that
// was asked for, the API and the break-glass page are never redirected, and a
// sign-in that does not take cannot loop.
func TestCoderSSOEdgeSendsDeadSessionsIntoKeycloak(t *testing.T) {
	data, deploy := coderEdge(t)
	conf := withoutComments(data["nginx.conf"])
	for _, want := range []string{
		"load_module modules/ngx_http_js_module.so;",
		"js_path /etc/nginx/njs/;",
		"js_import coder from coder.js;",
		// Relative redirects keep the browser on its own host and port.
		"absolute_redirect off;",
		// Coder builds its CSP connect-src from Host; the dashboard's websockets
		// need the browser's.
		"proxy_set_header Host $http_host;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("coder-sso nginx.conf lacks %q", want)
		}
	}
	blocks := nginxBlocks(t, data["nginx.conf"])
	check := func(spec string, wants ...string) {
		body, ok := blocks[spec]
		if !ok {
			t.Errorf("coder-sso nginx.conf has no `location %s`", spec)
			return
		}
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("coder-sso `location %s` lacks %q", spec, w)
			}
		}
	}
	gate := []string{"auth_request /_adhar/coder/session;", "error_page 401 = @signin;", "proxy_pass http://$coder;"}
	check("/", gate...)
	check("= /login", append(gate, "error_page 418 = @coder;", "if ($arg_local)", "return 418;")...)
	check("= /_adhar/coder/session", "internal;", "proxy_pass http://$coder/api/v2/users/me;", "proxy_set_header Host $http_host;")
	check("= /_adhar/coder/authmethods", "internal;", "proxy_pass http://$coder/api/v2/users/authmethods;")
	check("@signin", "js_content coder.signin;")
	check("@coder", "proxy_pass http://$coder;")
	if body := blocks["/api/"]; !strings.Contains(body, "proxy_pass http://$coder;") || strings.Contains(body, "auth_request") {
		t.Error("coder-sso must pass /api/ (the OIDC callback included) straight to Coder, with no session check")
	}
	if !regexp.MustCompile(`map \$host \$coder \{\s*default coder\.adhar-system\.svc\.cluster\.local:80;`).MatchString(conf) {
		t.Error("coder-sso does not proxy to the coder Service (map $host $coder)")
	}

	js := data["coder.js"]
	for _, want := range []string{
		// Coder's own OIDC start, with the page to come back to.
		`'/api/v2/users/oidc/callback?redirect=' + encodeURIComponent(to)`,
		// For /login, that page is where the person was going.
		`r.uri === '/login'`,
		`r.args.redirect`,
		// Never an open redirect, never back to /login.
		`to.charAt(1) === '/'`,
		`to === '/login'`,
		// Only while Coder has Keycloak sign-in configured.
		`.oidc.enabled === true`,
		`r.internalRedirect('@coder')`,
		// At most MAX_STARTS automatic starts, then the break-glass page.
		`starts >= MAX_STARTS`,
		`'/login?local=1&redirect='`,
		`'; Path=/; Max-Age=' + WINDOW_SECONDS`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("coder-sso coder.js lacks %s", want)
		}
	}
	if !strings.Contains(js, "export default { signin }") {
		t.Error("coder.js does not export signin, which nginx.conf's js_content calls")
	}

	// The files land where nginx looks for them, in an image that has njs.
	c := containersOf(dig(deploy, "spec").(map[string]any))
	if len(c) != 1 {
		t.Fatalf("coder-sso should run one container, has %d", len(c))
	}
	image, _ := c[0]["image"].(string)
	if !strings.HasPrefix(image, "docker.io/nginxinc/nginx-unprivileged:") || strings.Contains(image, "slim") {
		t.Errorf("coder-sso image %q: needs nginx with the njs module (the -slim images have none)", image)
	}
	mounts := map[string]string{}
	for _, m := range c[0]["volumeMounts"].([]any) {
		mm := m.(map[string]any)
		if sp, _ := mm["subPath"].(string); sp != "" {
			mounts[sp], _ = mm["mountPath"].(string)
		}
	}
	if mounts["nginx.conf"] != "/etc/nginx/nginx.conf" || mounts["coder.js"] != "/etc/nginx/njs/coder.js" {
		t.Errorf("coder-sso mounts %v; nginx.conf must replace /etc/nginx/nginx.conf (load_module is only legal there) "+
			"and coder.js must sit in js_path /etc/nginx/njs/", mounts)
	}
}

// The config is mounted with subPath, which never updates a running pod: an
// edited nginx.conf or coder.js only takes effect when the pod rolls, and the
// pod rolls only when its template changes.
func TestCoderSSOEdgeRollsWhenItsConfigChanges(t *testing.T) {
	data, deploy := coderEdge(t)
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(data[k]))
		h.Write([]byte{0})
	}
	want := hex.EncodeToString(h.Sum(nil))[:16]
	got, _ := dig(deploy, "spec", "template", "metadata", "annotations", "adhar.io/config-hash").(string)
	if got != want {
		t.Errorf("coder-sso pod annotation adhar.io/config-hash is %q, but the ConfigMap it mounts hashes to %q: "+
			"set it to %q so the edited config reaches the running pod", got, want, want)
	}
}

// Argo CD applies waves in order and waits for each to be healthy, so the edge
// must be serving before the route (wave 0) starts sending page loads to it —
// otherwise every upgrade opens a window of 503s on Coder's pages.
func TestCoderSSOEdgeIsServingBeforeTheRouteUsesIt(t *testing.T) {
	var route map[string]any
	if err := yaml.Unmarshal([]byte(strings.ReplaceAll(readStackFile(t, coderRouteFile), "{{ .Port }}", "443")), &route); err != nil {
		t.Fatal(err)
	}
	routeWave, _ := syncWave(route, "argocd.argoproj.io/sync-wave") // undeclared = wave 0
	dec := yaml.NewDecoder(strings.NewReader(readStackFile(t, coderEdgeFile)))
	seen := 0
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		kind, _ := doc["kind"].(string)
		if kind != "Deployment" && kind != "Service" && kind != "ConfigMap" {
			continue
		}
		seen++
		wave, ok := syncWave(doc, "argocd.argoproj.io/sync-wave")
		if !ok || wave >= routeWave {
			t.Errorf("coder-sso %s is in wave %d (declared: %v); it must come up before the route's wave %d", kind, wave, ok, routeWave)
		}
	}
	if seen != 3 {
		t.Fatalf("expected the coder-sso ConfigMap, Deployment and Service in %s, found %d", coderEdgeFile, seen)
	}
}

// Keycloak is the sign-in a person is offered. Coder enables its own GitHub
// login by default on a fresh deployment, which put a "GitHub" button for any
// GitHub account beside Keycloak's; the password form stays as break-glass.
func TestCoderOffersKeycloakNotItsDefaultGitHubLogin(t *testing.T) {
	var values struct {
		Coder struct {
			Env []struct {
				Name  string `yaml:"name"`
				Value string `yaml:"value"`
			} `yaml:"env"`
		} `yaml:"coder"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, "application/coder/values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	fromValues := map[string]string{}
	for _, e := range values.Coder.Env {
		fromValues[e.Name] = e.Value
	}
	fromInstall, _ := containerEnv(t, readStackFile(t, "application/coder/manifests/install.yaml"), "Deployment", "coder")
	for src, env := range map[string]map[string]string{"values.yaml": fromValues, "manifests/install.yaml": fromInstall} {
		if env["CODER_OAUTH2_GITHUB_DEFAULT_PROVIDER_ENABLE"] != "false" {
			t.Errorf("application/coder/%s does not set CODER_OAUTH2_GITHUB_DEFAULT_PROVIDER_ENABLE=false: Coder offers "+
				"its own GitHub login beside Keycloak", src)
		}
		if env["CODER_DISABLE_PASSWORD_AUTH"] != "false" {
			t.Errorf("application/coder/%s disables the password form, the break-glass sign-in at /login?local=1", src)
		}
	}
}
