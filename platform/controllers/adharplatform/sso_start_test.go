package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
)

// Kargo (browser PKCE) and Penpot (POST-started OIDC) cannot be handed to
// Keycloak by a Gateway redirect, so a bare page load goes to a same-origin
// start page (security/keycloak/manifests/sso-start.yaml). The danger in that
// design is a redirect loop: every rule that sends a page load to the start page
// must be shadowed by a rule that lets the signed-in (or fallen-back) request
// through.

type ssoRoute struct {
	Spec struct {
		Hostnames []string `json:"hostnames"`
		Rules     []struct {
			Matches []struct {
				Path        struct{ Type, Value string }   `json:"path"`
				Headers     []struct{ Name, Value string } `json:"headers"`
				QueryParams []struct{ Name, Value string } `json:"queryParams"`
			} `json:"matches"`
			Filters []struct {
				Type            string `json:"type"`
				RequestRedirect *struct {
					Path struct {
						ReplaceFullPath string `json:"replaceFullPath"`
					} `json:"path"`
					Port int `json:"port"`
				} `json:"requestRedirect"`
			} `json:"filters"`
			BackendRefs []struct{ Name string } `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
}

func stagedRoute(t *testing.T, app string) ssoRoute {
	t.Helper()
	spec := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "dev.adhar.io", Port: "443"}
	staged, cleanup, err := StageStack(filepath.Join("..", "..", "stack", "packages", "application", app, "manifests"), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(staged, "httproute.yaml"))
	if err != nil {
		t.Fatalf("%s: the route did not render to httproute.yaml: %v", app, err)
	}
	if strings.Contains(string(raw), "{{") {
		t.Fatalf("%s: unrendered template left in the route", app)
	}
	var r ssoRoute
	if err := yaml.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSSOStartRoutesCannotLoop(t *testing.T) {
	for app, passCookie := range map[string]string{
		"kargo":  "adhar_sso_kargo=1",
		"penpot": "auth-token=abc",
	} {
		r := stagedRoute(t, app)
		page := "/adhar-sso/" + app + ".html"
		var startServed, redirectsRoot, passesRoot bool
		for _, rule := range r.Spec.Rules {
			for _, m := range rule.Matches {
				if m.Path.Value == "/adhar-sso/" && len(rule.BackendRefs) > 0 && rule.BackendRefs[0].Name == "adhar-sso-start" {
					startServed = true
				}
				if m.Path.Type != "Exact" || m.Path.Value != "/" {
					continue
				}
				for _, f := range rule.Filters {
					if f.RequestRedirect != nil && f.RequestRedirect.Path.ReplaceFullPath == page {
						redirectsRoot = len(m.Headers) == 0
						if f.RequestRedirect.Port != 443 {
							t.Errorf("%s: the redirect port rendered as %d, want the platform port", app, f.RequestRedirect.Port)
						}
					}
				}
				for _, h := range m.Headers {
					if h.Name == "Cookie" && len(rule.BackendRefs) > 0 &&
						regexp.MustCompile("^(?:"+h.Value+")$").MatchString("x=1; "+passCookie) {
						passesRoot = true
					}
				}
			}
		}
		if !startServed {
			t.Errorf("%s: /adhar-sso/ is not routed to adhar-sso-start", app)
		}
		if !redirectsRoot {
			t.Errorf("%s: a bare load of / is not sent to %s; sign-in is not click-free", app, page)
		}
		if !passesRoot {
			t.Errorf("%s: a load of / carrying %q is not let through — the start page would loop forever", app, passCookie)
		}
	}

	// Kargo's OIDC return and manual fallback land on /login and must reach the
	// UI, or the code exchange never runs and the start page loops.
	r := stagedRoute(t, "kargo")
	need := map[string]bool{"code": false, "manual": false}
	for _, rule := range r.Spec.Rules {
		if len(rule.BackendRefs) == 0 || rule.BackendRefs[0].Name != "kargo-api" {
			continue
		}
		for _, m := range rule.Matches {
			for _, q := range m.QueryParams {
				if m.Path.Value == "/login" {
					if _, ok := need[q.Name]; ok {
						need[q.Name] = true
					}
				}
			}
		}
	}
	for q, ok := range need {
		if !ok {
			t.Errorf("kargo: /login?%s=… is not routed to Kargo; the start page would loop", q)
		}
	}
}

func TestSSOStartPagesMatchTheirApps(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "security", "keycloak", "manifests", "sso-start.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		// Kargo's UI reads exactly these keys on return (Kargo 1.x login page).
		"sessionStorage.setItem('PKCE_code_verifier'",
		"sessionStorage.setItem('PKCE_state'",
		"sessionStorage.setItem('platform_redirect'",
		"location.origin + '/login'",
		// Never send Kargo back to /login after sign-in: that full load returns here.
		"to === '/login'",
		// Fallbacks set the marker the routes let through.
		"adhar_sso_kargo=1",
		"/login?manual=1",
		"adhar_sso_penpot=1",
		"body: JSON.stringify({ provider: 'oidc' })",
		// Penpot returns to the start page with its route in the fragment; restarting
		// the login there looped forever.
		"if (location.hash.length > 1) {",
		"location.replace('/' + location.hash);",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("sso-start.yaml lacks %q", want)
		}
	}
}
