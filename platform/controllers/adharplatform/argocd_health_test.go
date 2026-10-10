package adharplatform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every resource kind the stack declares and ArgoCD cannot assess must have a
// health check, or a sync wave blocks forever on something that is fine.
//
// A wave does not complete until all its resources report Healthy, and for a
// custom resource with no built-in check ArgoCD reports Unknown — which never
// becomes Healthy. Every later wave is held behind it.
//
// Measured on Azure, 2026-10-04: the keycloak Application sat at
//
//	waiting for healthy state of gateway.networking.k8s.io/HTTPRoute/keycloak
//
// while that route was Accepted=True, ResolvedRefs=True, resolved to the live
// gateway and answered HTTP 302 over a valid certificate. The wave never
// completed, so Keycloak's wave-20 config Job was never created and the
// `keycloak-clients` Secret was never written — and that Secret gates ~12
// consumer ExternalSecrets and 9 oauth2-proxy rollouts. One unassessable
// HTTPRoute held the platform's whole SSO estate Degraded. Adding the check
// below took the keycloak app from a permanently Running operation to
// "successfully synced (all tasks run)" in under a minute.
func TestArgoCDCanAssessEveryCustomKindTheStackSyncs(t *testing.T) {
	cm := argocdConfigMap(t)
	// Kinds the stack declares that ArgoCD has no built-in health check for.
	// Each must be assessable or it can stall a wave.
	for _, kind := range []string{
		"gateway.networking.k8s.io_HTTPRoute",
		"gateway.networking.k8s.io_Gateway",
		"generators.external-secrets.io_Password",
		"postgresql.cnpg.io_ScheduledBackup",
	} {
		key := "resource.customizations.health." + kind
		if !strings.Contains(cm, key) {
			t.Errorf("argocd-cm has no health check for %s. Without one ArgoCD reports Unknown, "+
				"the sync wave containing it never completes, and every later wave is blocked "+
				"on a resource that is healthy.", kind)
		}
	}
}

// The HTTPRoute check must assert what the route itself reports, not return a
// blanket Healthy — a route whose backendRefs do not resolve has to surface.
func TestHTTPRouteHealthCheckStillDetectsFailure(t *testing.T) {
	cm := argocdConfigMap(t)
	i := strings.Index(cm, "resource.customizations.health.gateway.networking.k8s.io_HTTPRoute")
	if i < 0 {
		t.Fatal("the HTTPRoute health check is missing")
	}
	// The Lua body runs until the next customization key.
	body := cm[i:]
	if j := strings.Index(body[1:], "\n  resource.customizations."); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{"Accepted", "ResolvedRefs", `"Degraded"`, `"Progressing"`, `"Healthy"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the HTTPRoute check must consider %s — a check that always returns Healthy "+
				"hides a broken route instead of stalling on a working one", want)
		}
	}
}

// Generators are declarative inputs with no status subresource, so Healthy is
// correct for them — but only because a malformed generator fails at the
// ExternalSecret that references it, and ExternalSecret IS assessed.
func TestGeneratorHealthIsPairedWithExternalSecretHealth(t *testing.T) {
	cm := argocdConfigMap(t)
	if !strings.Contains(cm, "resource.customizations.health.external-secrets.io_ExternalSecret") {
		t.Error("reporting generators Healthy is only safe while ExternalSecret itself is " +
			"assessed — that is where a malformed generator actually surfaces")
	}
}

func argocdConfigMap(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("resources/argocd/install.yaml")
	if err != nil {
		t.Fatalf("reading the embedded ArgoCD manifest: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "name: argocd-cm")
	if i < 0 {
		t.Fatal("argocd-cm has moved in the embedded manifest; this guard needs updating")
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n---"); j > 0 {
		rest = rest[:j]
	}
	return rest
}

// The Argo CD SSO proxy forwards the Keycloak ID token upstream, and Argo CD
// verifies that token on every request in preference to its own session cookie.
// The proxy must therefore refresh it before Keycloak's accessTokenLifespan
// (1800 s) runs out, or every SSO session breaks 30 minutes after login while
// the proxy's own cookie is still perfectly valid (2026-10-04).
func TestArgoCDSSOProxyRefreshesTheTokenItForwards(t *testing.T) {
	b, err := argoCDFS.ReadFile("resources/argocd/sso-proxy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "--pass-authorization-header=true") {
		t.Skip("the proxy no longer forwards the token; nothing to refresh")
	}
	m := regexp.MustCompile(`--cookie-refresh=(\d+)m`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("sso-proxy.yaml forwards the ID token but never refreshes it (--cookie-refresh missing): " +
			"Argo CD rejects the dead token once it expires")
	}
	mins, _ := strconv.Atoi(m[1])

	// Checked against the realm that actually mints the token, not a number
	// typed into a comment. The previous version of this test asserted
	// "shorter than 1800 s" because the comment next to the flag said the
	// lifespan was 1800 s — but that accessTokenLifespan PUT applies to the
	// MASTER realm, and `adhar` keeps Keycloak's 300 s default. The test and
	// the bug agreed with each other for a day.
	lifespan := adharRealmTokenLifespanSeconds(t)
	if mins <= 0 || mins*60 >= lifespan {
		t.Errorf("--cookie-refresh=%dm does not fit inside the adhar realm's %d s token lifespan: "+
			"the proxy would forward an expired ID token, which Argo CD answers with 401 and "+
			"oauth2-proxy turns into a 403 for the UI's XHR calls", mins, lifespan)
	}
	// Refreshing at the very edge of expiry is a race with clock skew and a slow
	// token endpoint; keep a real margin.
	if mins*60*2 > lifespan {
		t.Errorf("--cookie-refresh=%dm leaves no margin under a %d s lifespan; refresh at most every %d s",
			mins, lifespan, lifespan/2)
	}
	if !regexp.MustCompile(`--cookie-expire=\d+h`).MatchString(s) {
		t.Error("--cookie-expire must cap the proxy session (the realm's SSO idle timeout is 8 h); the default is 168 h")
	}
	// The env form, for a cluster whose controller image predates the args: the
	// ExternalSecret is CreatedOnce, so these two keys are the one place a live
	// fix survives the controller re-applying the Deployment. Derived from the
	// flag so the two cannot drift apart.
	if want := fmt.Sprintf(`OAUTH2_PROXY_COOKIE_REFRESH: "%dm"`, mins); !strings.Contains(s, want) {
		t.Errorf("the proxy's ExternalSecret template must carry %s, matching the --cookie-refresh flag", want)
	}
	if !strings.Contains(s, `OAUTH2_PROXY_COOKIE_EXPIRE: "8h0m0s"`) {
		t.Error(`the proxy's ExternalSecret template must carry OAUTH2_PROXY_COOKIE_EXPIRE: "8h0m0s"`)
	}
}

// adharRealmTokenLifespanSeconds is how long an ID token from the `adhar` realm
// is valid: the realm payload's accessTokenLifespan if it sets one, otherwise
// Keycloak's own default of 300 s. Keycloak gives the ID token the access
// token's lifespan, and that is the clock the forwarded-token refresh races.
func adharRealmTokenLifespanSeconds(t *testing.T) int {
	t.Helper()
	const keycloakDefaultTokenLifespan = 300

	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "security/keycloak/manifests/keycloak-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*realm-payload\.json: \|\n\s*(\{.*)$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("keycloak-config.yaml no longer carries a realm-payload.json; re-check this test")
	}
	var realm map[string]interface{}
	if err := json.Unmarshal(m[1], &realm); err != nil {
		t.Fatalf("realm-payload.json does not parse: %v", err)
	}
	if v, ok := realm["accessTokenLifespan"].(float64); ok && v > 0 {
		return int(v)
	}
	return keycloakDefaultTokenLifespan
}

// A PipelineRun inherits its Pipeline's annotations, tracking-id included, so
// without this exclusion every CI run is a managed resource of the Application
// that declares the Pipeline: a failed release held adhar-libraries Degraded
// until the hourly pruner, and each new run flipped it OutOfSync (2026-10-09).
// Runs are not in Git; they are watched through the console and Tekton.
func TestArgoCDDoesNotTrackTektonRuns(t *testing.T) {
	for _, file := range []string{
		"resources/argocd/install.yaml",
		"resources/argocd/install-ha.yaml",
		"../../../hack/argocd/values.yaml",
		"../../../hack/argocd/values-ha.yaml",
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		i := strings.Index(src, "resource.exclusions: |")
		if i < 0 {
			t.Errorf("%s: no resource.exclusions block", file)
			continue
		}
		body := src[i:]
		if j := strings.Index(body, "\n  resource."); j > 0 { // the next argocd-cm key
			body = body[:j]
		}
		// Strip the block's own ### comments so the guard reads config, not prose.
		var lines []string
		for _, l := range strings.Split(body, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "#") {
				lines = append(lines, l)
			}
		}
		cfg := strings.Join(lines, "\n")
		if !strings.Contains(cfg, "- tekton.dev") {
			t.Errorf("%s: resource.exclusions does not list the tekton.dev group", file)
		}
		for _, kind := range []string{"PipelineRun", "TaskRun"} {
			if !strings.Contains(cfg, "- "+kind+"\n") {
				t.Errorf("%s: resource.exclusions does not exclude tekton.dev/%s", file, kind)
			}
		}
	}
}
