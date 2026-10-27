package adharplatform

// Penpot (2.17.2) signs people in itself, with native OIDC against Keycloak,
// and the platform makes that click-free with a same-origin start page
// (security/keycloak/manifests/sso-start.yaml, penpot.html). Sign-in failed on
// GCP on 2026-10-26 in a way that showed nothing at all: Penpot's backend
// redeemed the authorization code at Keycloak's PUBLIC URL, which from inside
// the cluster hairpins through the Cilium Gateway's Envoy, and that Envoy
// answered the POST itself — 403 "Access denied\r\n", Cilium's L7 policy
// denial; Keycloak never saw it — and Penpot shows no message for the
// resulting `unable-to-auth`. These guards pin the fix and what it relies on.

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"adhar-io/adhar/api/v1alpha1"
)

const (
	penpotKeycloakInCluster = "http://keycloak.adhar-system.svc.cluster.local:8080/realms/adhar/protocol/openid-connect/"
	penpotKeycloakPublic    = "https://keycloak.adhar.localtest.me:8443/realms/adhar/protocol/openid-connect/"
	// The Penpot chart's published default PENPOT_SECRET_KEY (chart 1.9.0 values.yaml).
	penpotChartDefaultSecretKey = "kmZ96pAxhTgk3HZvvBkPeVTspGBneKVLEpO_3ecORs_gwACENZ77z05zCe7skvPsQ3jI3QgkULQOWCuLjmjQsg"
)

func penpotDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(stackRoot(t), "packages", "application", "penpot")
}

func penpotRead(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(penpotDir(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type penpotEnvVar struct {
	Value     *string `yaml:"value"`
	ValueFrom *struct {
		SecretKeyRef *struct{ Name, Key string } `yaml:"secretKeyRef"`
	} `yaml:"valueFrom"`
}

type penpotContainer struct {
	Name    string   `yaml:"name"`
	Args    []string `yaml:"args"`
	Command []string `yaml:"command"`
	Env     []struct {
		Name         string `yaml:"name"`
		penpotEnvVar `yaml:",inline"`
	} `yaml:"env"`
}

func (c penpotContainer) env() map[string]penpotEnvVar {
	m := map[string]penpotEnvVar{}
	for _, e := range c.Env {
		m[e.Name] = e.penpotEnvVar
	}
	return m
}

// penpotDeployment returns the named Deployment's init and app containers.
func penpotDeployment(t *testing.T, manifest, name string) (inits, containers map[string]penpotContainer) {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct{ Name string }
			Spec     struct {
				Template struct {
					Spec struct {
						InitContainers []penpotContainer `yaml:"initContainers"`
						Containers     []penpotContainer `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc.Kind != "Deployment" || doc.Metadata.Name != name {
			continue
		}
		inits, containers = map[string]penpotContainer{}, map[string]penpotContainer{}
		for _, c := range doc.Spec.Template.Spec.InitContainers {
			inits[c.Name] = c
		}
		for _, c := range doc.Spec.Template.Spec.Containers {
			containers[c.Name] = c
		}
		return inits, containers
	}
	t.Fatalf("no Deployment %q in the penpot manifest", name)
	return nil, nil
}

func penpotLiteral(t *testing.T, env map[string]penpotEnvVar, name string) string {
	t.Helper()
	v, ok := env[name]
	if !ok || v.Value == nil {
		t.Errorf("penpot-backend: %s is not set to a literal value", name)
		return ""
	}
	return *v.Value
}

// The browser goes to Keycloak's public URL; Penpot's own calls (code
// redemption, userinfo, JWKS) go to the Keycloak Service. All of auth, token
// and user URIs must be set, or Penpot runs discovery against the base URI —
// the public URL — and the token endpoint it discovers is the public one again.
// The SSRF allowlist must name exactly that Service, or Penpot refuses the
// in-cluster call ("uri target is not allowed": 10/8 is on its blocklist).
func TestPenpotRedeemsTheCodeInsideTheCluster(t *testing.T) {
	check := func(where, manifest, publicPrefix string) {
		_, cs := penpotDeployment(t, manifest, "penpot-backend")
		env := cs["penpot-backend"].env()
		lit := func(name string) string { return penpotLiteral(t, env, name) }
		if got := lit("PENPOT_OIDC_AUTH_URI"); got != publicPrefix+"auth" {
			t.Errorf("%s: PENPOT_OIDC_AUTH_URI = %q, want the PUBLIC %sauth (the browser is sent there; Keycloak's session cookie lives on that host)", where, got, publicPrefix)
		}
		for name, endpoint := range map[string]string{
			"PENPOT_OIDC_TOKEN_URI": "token",
			"PENPOT_OIDC_USER_URI":  "userinfo",
			"PENPOT_OIDC_JWKS_URI":  "certs",
		} {
			if got := lit(name); got != penpotKeycloakInCluster+endpoint {
				t.Errorf("%s: %s = %q, want %s%s — through the public URL the call hairpins through the Gateway, whose Envoy refused the token POST with 403 \"Access denied\" on GCP", where, name, got, penpotKeycloakInCluster, endpoint)
			}
		}
		u, err := url.Parse(lit("PENPOT_OIDC_TOKEN_URI"))
		if err != nil {
			t.Fatalf("%s: token URI does not parse: %v", where, err)
		}
		allowed := strings.FieldsFunc(lit("PENPOT_SSRF_ALLOWED_HOSTS"), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
		if len(allowed) != 1 || allowed[0] != u.Hostname() {
			t.Errorf("%s: PENPOT_SSRF_ALLOWED_HOSTS = %q, want exactly %q: Penpot refuses the in-cluster token call otherwise, and anything more widens its SSRF exemption", where, allowed, u.Hostname())
		}
	}

	check("manifests/install.yaml", penpotRead(t, "manifests/install.yaml"), penpotKeycloakPublic)

	// As seeded for a cloud host: the public URL follows the host rewrite, the
	// in-cluster ones must come through untouched.
	spec := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "dev.adhar.io", Port: "443"}
	staged, cleanup, err := StageStack(filepath.Join(penpotDir(t), "manifests"), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(staged, "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	check("staged install.yaml (dev.adhar.io)", string(raw), "https://keycloak.dev.adhar.io/realms/adhar/protocol/openid-connect/")

	// install.yaml is generated; the source is values.yaml. A split that exists
	// only in the output is undone by the next generate-manifests.sh.
	var values struct {
		Config struct {
			Providers struct {
				OIDC struct {
					AuthURI  string `yaml:"authURI"`
					TokenURI string `yaml:"tokenURI"`
					UserURI  string `yaml:"userURI"`
				} `yaml:"oidc"`
			} `yaml:"providers"`
		} `yaml:"config"`
		Backend struct {
			ExtraEnvs []struct{ Name, Value string } `yaml:"extraEnvs"`
		} `yaml:"backend"`
	}
	if err := yaml.Unmarshal([]byte(penpotRead(t, "values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	o := values.Config.Providers.OIDC
	if o.AuthURI != penpotKeycloakPublic+"auth" || o.TokenURI != penpotKeycloakInCluster+"token" || o.UserURI != penpotKeycloakInCluster+"userinfo" {
		t.Errorf("values.yaml config.providers.oidc auth/token/user = %q / %q / %q; the generator source must carry the same split as install.yaml", o.AuthURI, o.TokenURI, o.UserURI)
	}
	extra := map[string]string{}
	for _, e := range values.Backend.ExtraEnvs {
		extra[e.Name] = e.Value
	}
	if extra["PENPOT_OIDC_JWKS_URI"] != penpotKeycloakInCluster+"certs" || extra["PENPOT_SSRF_ALLOWED_HOSTS"] != "keycloak.adhar-system.svc.cluster.local" {
		t.Errorf("values.yaml backend.extraEnvs = %v; want the in-cluster JWKS URI and the SSRF allowlist for the Keycloak Service", extra)
	}
}

// The boot-time wait must not depend on the public URL either: with auth,
// token and user URIs given Penpot skips discovery, and the one call it makes
// at boot is the JWKS fetch from the Keycloak Service.
func TestPenpotWaitsForKeycloakInsideTheCluster(t *testing.T) {
	inits, _ := penpotDeployment(t, penpotRead(t, "manifests/install.yaml"), "penpot-backend")
	w, ok := inits["wait-for-keycloak"]
	if !ok {
		t.Fatal("penpot-backend has no wait-for-keycloak init container (post-render.py)")
	}
	env := w.env()
	if v := env["KEYCLOAK_JWKS_URI"]; v.Value == nil || *v.Value != penpotKeycloakInCluster+"certs" {
		t.Errorf("wait-for-keycloak waits on %v, want %scerts", env, penpotKeycloakInCluster)
	}
	script := strings.Join(append(w.Command, w.Args...), " ")
	if strings.Contains(script, "keycloak.adhar.localtest.me") || regexp.MustCompile(`curl -[a-zA-Z]*k`).MatchString(script) {
		t.Errorf("wait-for-keycloak still reaches for the public URL or skips TLS verification:\n%s", script)
	}
	if !strings.Contains(penpotRead(t, "post-render.py"), penpotKeycloakInCluster+"certs") {
		t.Error("post-render.py (the generator of that init container) does not wait on the in-cluster JWKS URI")
	}
}

// PENPOT_SECRET_KEY keys every token Penpot issues. The chart's default is
// published, so with it anyone can mint session, registration or
// password-recovery tokens; it must come from the per-cluster Secret, which
// must never be regenerated under a running Penpot.
func TestPenpotSecretKeyIsGeneratedPerCluster(t *testing.T) {
	install := penpotRead(t, "manifests/install.yaml")
	for _, d := range []string{"penpot-backend", "penpot-exporter"} {
		_, cs := penpotDeployment(t, install, d)
		v, ok := cs[d].env()["PENPOT_SECRET_KEY"]
		switch {
		case !ok:
			t.Errorf("%s: PENPOT_SECRET_KEY is not set", d)
		case v.Value != nil:
			t.Errorf("%s: PENPOT_SECRET_KEY is a literal in the manifest; it must come from Secret penpot-secret-key", d)
		case v.ValueFrom == nil || v.ValueFrom.SecretKeyRef == nil || v.ValueFrom.SecretKeyRef.Name != "penpot-secret-key" || v.ValueFrom.SecretKeyRef.Key != "secretKey":
			t.Errorf("%s: PENPOT_SECRET_KEY must read Secret penpot-secret-key key secretKey (backend and exporter share it)", d)
		}
	}
	err := filepath.Walk(penpotDir(t), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && strings.Contains(string(b), penpotChartDefaultSecretKey) {
			t.Errorf("%s carries the Penpot chart's public default secret key", strings.TrimPrefix(p, penpotDir(t)+string(filepath.Separator)))
		}
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}

	dec := yaml.NewDecoder(strings.NewReader(penpotRead(t, "manifests/secret-key.yaml")))
	var generator, es bool
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		meta, _ := doc["metadata"].(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		if meta["name"] != "penpot-secret-key" {
			continue
		}
		switch doc["kind"] {
		case "Password":
			generator = true
			if n, _ := spec["length"].(int); n < 64 {
				t.Errorf("Password generator length %v; want at least 64", spec["length"])
			}
		case "ExternalSecret":
			es = true
			if spec["refreshPolicy"] != "CreatedOnce" {
				t.Errorf("ExternalSecret penpot-secret-key refreshPolicy = %v, want CreatedOnce: a new key signs everyone out of Penpot", spec["refreshPolicy"])
			}
			target, _ := spec["target"].(map[string]any)
			tmpl, _ := target["template"].(map[string]any)
			data, _ := tmpl["data"].(map[string]any)
			if target["name"] != "penpot-secret-key" || data["secretKey"] == nil {
				t.Errorf("ExternalSecret penpot-secret-key must write Secret penpot-secret-key with key secretKey, got target %v", target)
			}
		}
		ann, _ := meta["annotations"].(map[string]any)
		if w, _ := ann["argocd.argoproj.io/sync-wave"].(string); !strings.HasPrefix(w, "-") {
			t.Errorf("%v penpot-secret-key sync-wave %q: must precede the Deployments (wave 0) that mount it", doc["kind"], w)
		}
	}
	if !generator || !es {
		t.Errorf("manifests/secret-key.yaml must declare the Password generator (%v) and the ExternalSecret (%v) named penpot-secret-key", generator, es)
	}
}

// Every platform URL is HTTPS, so Penpot's session cookie keeps `Secure`.
func TestPenpotSessionCookieStaysSecure(t *testing.T) {
	install := penpotRead(t, "manifests/install.yaml")
	for _, d := range []string{"penpot-backend", "penpot-frontend"} {
		_, cs := penpotDeployment(t, install, d)
		if f := penpotLiteral(t, cs[d].env(), "PENPOT_FLAGS"); strings.Contains(f, "disable-secure-session-cookies") {
			t.Errorf("%s: PENPOT_FLAGS strips Secure from the session cookie: %q", d, f)
		}
		if u := penpotLiteral(t, cs[d].env(), "PENPOT_PUBLIC_URI"); !strings.HasPrefix(u, "https://") {
			t.Errorf("%s: PENPOT_PUBLIC_URI %q is not https (Penpot then drops Secure by itself)", d, u)
		}
	}
	if strings.Contains(penpotRead(t, "values.yaml"), "disable-secure-session-cookies\"") {
		t.Error("values.yaml config.flags still carries disable-secure-session-cookies")
	}
}

// Penpot's auth-token outlives the session it names, and Penpot answers such a
// cookie as the anonymous user without clearing it. A route that let `/`
// through on that cookie parked people on the login page for up to its 7 days
// after a rebuild or a new secret key. Only the start page's marker — set after
// it has asked Penpot — may let a page load of `/` past it.
func TestPenpotRouteTrustsOnlyTheStartPagesMarker(t *testing.T) {
	r := stagedRoute(t, "penpot")
	var marker, stale, bare bool
	for _, rule := range r.Spec.Rules {
		for _, m := range rule.Matches {
			if m.Path.Type != "Exact" || m.Path.Value != "/" {
				continue
			}
			if len(m.Headers) == 0 {
				bare = true
				if len(rule.BackendRefs) > 0 {
					t.Errorf("a bare load of / is routed to %q; it must go to the start page", rule.BackendRefs[0].Name)
				}
				continue
			}
			for _, h := range m.Headers {
				if !strings.EqualFold(h.Name, "Cookie") || len(rule.BackendRefs) == 0 {
					continue
				}
				re := regexp.MustCompile("^(?:" + h.Value + ")$")
				marker = marker || re.MatchString("x=1; adhar_sso_penpot=1; auth-token=abc")
				stale = stale || re.MatchString("x=1; auth-token=abc") || re.MatchString("auth-token=abc")
			}
		}
	}
	if !bare {
		t.Error("no rule sends a bare load of / to the start page")
	}
	if !marker {
		t.Error("a load of / carrying adhar_sso_penpot is not let through: the start page would loop")
	}
	if stale {
		t.Error("a load of / carrying only Penpot's auth-token is let through: a stale cookie then skips sign-in for up to 7 days")
	}
}

// The start page, run against a stubbed browser (Node vm). It is the only
// thing between a person with a Keycloak session and Penpot's login screen.
func TestPenpotStartPageSignsInWithoutAClick(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	raw, err := os.ReadFile(filepath.Join(stackRoot(t), "packages", "security", "keycloak", "manifests", "sso-start.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.NewDecoder(strings.NewReader(string(raw))).Decode(&cm); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)<script>(.*)</script>`).FindStringSubmatch(cm.Data["penpot.html"])
	if m == nil {
		t.Fatal("sso-start.yaml penpot.html has no <script>")
	}

	const anon = `{"status":200,"json":{"id":"00000000-0000-0000-0000-000000000000","fullname":"Anonymous User"}}`
	const oidc = `{"status":200,"json":{"redirectUri":"https://keycloak.example.test/realms/adhar/protocol/openid-connect/auth?client_id=penpot&state=s"}}`
	const now = 1800000000000
	recent, stale := "1799999990000", "1790000000000"
	deep := `["~#'","https://penpot.example.test/#/workspace?team-id=t&file-id=f"]`
	scenarios := map[string]any{
		"anonymous":    map[string]any{"fetch": json.RawMessage(`{"GET /api/main/methods/get-profile":` + anon + `,"POST /api/auth/oidc":` + oidc + `}`)},
		"stale-cookie": map[string]any{"cookies": "auth-token=from-a-rebuilt-cluster", "fetch": json.RawMessage(`{"GET /api/main/methods/get-profile":` + anon + `,"POST /api/auth/oidc":` + oidc + `}`)},
		"signed-in":    map[string]any{"hash": "#/dashboard/recent?team-id=t", "fetch": json.RawMessage(`{"GET /api/main/methods/get-profile":{"status":200,"json":{"id":"6f1d2c1e-0000-4000-8000-000000000001"}}}`)},
		"deep-link":    map[string]any{"hash": "#/workspace?team-id=t&file-id=f", "fetch": json.RawMessage(`{"GET /api/main/methods/get-profile":` + anon + `,"POST /api/auth/oidc":` + oidc + `}`)},
		"share-link":   map[string]any{"hash": "#/view?file-id=f&share-id=s", "fetch": json.RawMessage(`{"GET /api/main/methods/get-profile":` + anon + `}`)},
		"auth-return":  map[string]any{"hash": "#/auth/verify-token?token=abc", "session": map[string]string{"adhar_sso_penpot_started": recent}},
		"auth-error":   map[string]any{"hash": "#/auth/login?error=unable-to-auth"},
		"register-own": map[string]any{"hash": "#/auth/register/validate?token=REG&provider=oidc&fullname=A", "now": now,
			"session": map[string]string{"adhar_sso_penpot_started": recent, "penpot:/login-redirect": deep},
			"fetch":   json.RawMessage(`{"POST /api/main/methods/register-profile":{"status":200,"json":{"id":"p","isActive":true}}}`)},
		"register-foreign": map[string]any{"hash": "#/auth/register/validate?token=REG&provider=oidc", "now": now,
			"fetch": json.RawMessage(`{"POST /api/main/methods/register-profile":{"status":200,"json":{"id":"p","isActive":true}}}`)},
		"register-stale": map[string]any{"hash": "#/auth/register/validate?token=REG", "now": now,
			"session": map[string]string{"adhar_sso_penpot_started": stale},
			"fetch":   json.RawMessage(`{"POST /api/main/methods/register-profile":{"status":200,"json":{"id":"p","isActive":true}}}`)},
		"oidc-down":       map[string]any{"fetch": json.RawMessage(`{"GET /api/main/methods/get-profile":` + anon + `,"POST /api/auth/oidc":{"status":400,"json":{"code":"sso-provider-not-configured"}}}`)},
		"backend-down":    map[string]any{"fetch": json.RawMessage(`{}`)},
		"bad-redirect":    map[string]any{"fetch": json.RawMessage(`{"GET /api/main/methods/get-profile":` + anon + `,"POST /api/auth/oidc":{"status":200,"json":{"redirectUri":"javascript:alert(1)"}}}`)},
		"cookies-blocked": map[string]any{"cookiesBlocked": true, "hash": "#/auth/login?error=unable-to-auth"},
	}

	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sc, err := json.Marshal(scenarios)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, write("harness.js", penpotStartPageHarness), write("page.js", m[1]), write("scenarios.json", string(sc))).Output()
	if err != nil {
		t.Fatalf("node harness: %v", err)
	}
	type fetched struct{ Key, Body string }
	var res map[string]struct {
		Replaced   *string           `json:"replaced"`
		CookieSets []string          `json:"cookieSets"`
		Fetches    []fetched         `json:"fetches"`
		Session    map[string]string `json:"session"`
		Text       string            `json:"text"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("harness output: %v\n%s", err, out)
	}
	went := func(name string) string {
		if res[name].Replaced == nil {
			return ""
		}
		return *res[name].Replaced
	}
	called := func(name, key string) *fetched {
		for _, f := range res[name].Fetches {
			if f.Key == key {
				return &f
			}
		}
		return nil
	}
	marked := func(name string) string {
		for _, c := range res[name].CookieSets {
			if strings.HasPrefix(c, "adhar_sso_penpot=1;") {
				return regexp.MustCompile(`max-age=(\d+)`).FindStringSubmatch(c)[1]
			}
		}
		return ""
	}
	const keycloak = "https://keycloak.example.test/realms/adhar/protocol/openid-connect/auth?client_id=penpot&state=s"

	for _, name := range []string{"anonymous", "stale-cookie", "deep-link"} {
		if called(name, "GET /api/main/methods/get-profile") == nil {
			t.Errorf("%s: the page did not ask Penpot whether the session is live", name)
		}
		if f := called(name, "POST /api/auth/oidc"); f == nil || !strings.Contains(f.Body, `"provider":"oidc"`) {
			t.Errorf("%s: the page did not start Penpot's OIDC login (POST /api/auth/oidc provider=oidc)", name)
		}
		if went(name) != keycloak {
			t.Errorf("%s: went to %q, want Keycloak's URL from Penpot", name, went(name))
		}
		if marked(name) != "" {
			t.Errorf("%s: set the marker before leaving for Keycloak; the return would then skip this page", name)
		}
		if res[name].Session["adhar_sso_penpot_started"] == "" {
			t.Errorf("%s: did not record that this tab started a sign-in", name)
		}
	}
	if got := res["deep-link"].Session["penpot:/login-redirect"]; got != deep {
		t.Errorf("deep link: login-redirect = %q, want %q (Penpot follows it once signed in)", got, deep)
	}
	if went("signed-in") != "/#/dashboard/recent?team-id=t" || marked("signed-in") != "3600" || called("signed-in", "POST /api/auth/oidc") != nil {
		t.Errorf("signed in: went %q with marker %q; want straight in (1-hour marker), no new login", went("signed-in"), marked("signed-in"))
	}
	if went("share-link") != "/#/view?file-id=f&share-id=s" || called("share-link", "POST /api/auth/oidc") != nil {
		t.Errorf("share link: went %q; a share link must open for people without an account, not send them to Keycloak", went("share-link"))
	}
	for name, want := range map[string]string{"auth-return": "/#/auth/verify-token?token=abc", "auth-error": "/#/auth/login?error=unable-to-auth"} {
		if went(name) != want || marked(name) != "600" || len(res[name].Fetches) != 0 {
			t.Errorf("%s: went %q (marker %q, %d calls); the end of a sign-in must be handed to Penpot, never restart it (that looped)", name, went(name), marked(name), len(res[name].Fetches))
		}
	}
	if f := called("register-own", "POST /api/main/methods/register-profile"); f == nil || !strings.Contains(f.Body, `"token":"REG"`) {
		t.Error("first sign-in: the page did not submit Penpot's Create-account step with the token from the callback")
	}
	if went("register-own") != "/#/workspace?team-id=t&file-id=f" || res["register-own"].Session["penpot:/login-redirect"] != "" {
		t.Errorf("first sign-in: went %q; want the remembered deep link, consumed", went("register-own"))
	}
	for _, name := range []string{"register-foreign", "register-stale"} {
		if called(name, "POST /api/main/methods/register-profile") != nil {
			t.Errorf("%s: registered from a link this tab did not start: someone else's token would sign this browser in as them", name)
		}
		if !strings.HasPrefix(went(name), "/#/auth/register/validate?") {
			t.Errorf("%s: went %q; want Penpot's own Create-account page", name, went(name))
		}
	}
	for _, name := range []string{"oidc-down", "backend-down", "bad-redirect"} {
		if went(name) != "/#/auth/login" || marked(name) != "300" {
			t.Errorf("%s: went %q (marker %q); a failure must land on Penpot's login with the 5-minute marker", name, went(name), marked(name))
		}
	}
	if went("cookies-blocked") != "" || res["cookies-blocked"].Text == "" {
		t.Errorf("cookies blocked: went %q; without the marker every way back to / returns here, so the page must stop and say why", went("cookies-blocked"))
	}
}

// Runs a start-page script against a stubbed browser, once per scenario, and
// prints what it did. Node core only.
const penpotStartPageHarness = `
const vm = require('vm');
const fs = require('fs');
const [, , scriptPath, scenariosPath] = process.argv;
const code = fs.readFileSync(scriptPath, 'utf8');
const scenarios = JSON.parse(fs.readFileSync(scenariosPath, 'utf8'));
(async () => {
  const out = {};
  for (const [name, sc] of Object.entries(scenarios)) {
    const jar = {};
    for (const kv of (sc.cookies || '').split(';').map((s) => s.trim()).filter(Boolean)) {
      const i = kv.indexOf('=');
      jar[kv.slice(0, i)] = kv.slice(i + 1);
    }
    const store = Object.assign({}, sc.session || {});
    const sets = [], fetches = [];
    let replaced = null;
    const p = { textContent: '' };
    const document = {
      get cookie() { return Object.entries(jar).map(([k, v]) => k + '=' + v).join('; '); },
      set cookie(v) {
        sets.push(v);
        if (sc.cookiesBlocked) return;
        const first = v.split(';')[0], i = first.indexOf('=');
        jar[first.slice(0, i).trim()] = first.slice(i + 1);
      },
      querySelector: () => p,
    };
    const sessionStorage = {
      getItem: (k) => (Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null),
      setItem: (k, v) => { store[k] = String(v); },
      removeItem: (k) => { delete store[k]; },
    };
    const location = {
      origin: 'https://penpot.example.test', host: 'penpot.example.test',
      pathname: '/adhar-sso/penpot.html', search: '', hash: sc.hash || '',
      replace: (u) => { if (replaced === null) replaced = String(u); },
    };
    const fetch = async (path, opts = {}) => {
      const key = (opts.method || 'GET').toUpperCase() + ' ' + path;
      fetches.push({ key, body: opts.body || '' });
      const r = (sc.fetch || {})[key];
      if (!r) throw new TypeError('network error: ' + key);
      return { ok: r.status >= 200 && r.status < 300, status: r.status, json: async () => r.json };
    };
    const now = sc.now || 1800000000000;
    vm.runInContext(code, vm.createContext({
      document, sessionStorage, location, fetch, URL, URLSearchParams,
      Date: { now: () => now }, console,
    }));
    await new Promise((r) => setTimeout(r, 50));
    out[name] = { replaced, cookieSets: sets, fetches, session: store, text: p.textContent };
  }
  process.stdout.write(JSON.stringify(out));
})().catch((e) => { console.error(e); process.exit(1); });
`
