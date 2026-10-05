package adharplatform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The `adhar` realm's session and token lifespans are the clock every SSO
// integration on this platform runs on, and they reach a realm by two different
// routes that each miss half the clusters:
//
//   - realm-payload.json is POSTed ONLY when the realm does not exist, so a
//     setting added there never reaches an already-provisioned cluster;
//   - the hardening PUT can only update a realm that already exists, so on a
//     fresh cluster it 404s and anything living only there is lost.
//
// Keeping the lifespans in exactly one of the two is how a brand-new platform
// came up with Keycloak's defaults (300 s access token, 1800 s SSO idle) while
// this file said 1800 s and 28800 s — and why the Argo CD UI began answering
// "Error: Forbidden" five minutes after every sign-in. These tests pin both
// routes and the ordering that makes the second one effective.

// The settings that must be identical on both routes.
var realmLifespanKeys = []string{
	"accessTokenLifespan",
	"ssoSessionIdleTimeout",
	"ssoSessionMaxLifespan",
	"clientSessionIdleTimeout",
	"clientSessionMaxLifespan",
}

func keycloakConfigSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "security/keycloak/manifests/keycloak-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func jsonObject(t *testing.T, raw, what string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v", what, err)
	}
	return m
}

func TestRealmLifespansReachFreshAndExistingClusters(t *testing.T) {
	s := keycloakConfigSource(t)

	payloadRe := regexp.MustCompile(`(?m)^\s*realm-payload\.json: \|\n\s*(\{.*)$`)
	pm := payloadRe.FindStringSubmatch(s)
	if pm == nil {
		t.Fatal("no realm-payload.json in keycloak-config.yaml")
	}
	payload := jsonObject(t, pm[1], "realm-payload.json")

	// The hardening PUT's body, which is the only route into an existing realm.
	hardenRe := regexp.MustCompile(`harden_realm\(\) \{\n\s*kc -X PUT --data '(\{.*?\})' "\$\{REALM_URL\}"`)
	hm := hardenRe.FindStringSubmatch(s)
	if hm == nil {
		t.Fatal("no harden_realm() applying a realm representation; the realm settings reach an existing cluster nowhere")
	}
	harden := jsonObject(t, hm[1], "the harden_realm PUT body")

	for _, k := range realmLifespanKeys {
		pv, inPayload := payload[k]
		hv, inHarden := harden[k]
		switch {
		case !inHarden:
			t.Errorf("%s is missing from harden_realm: an existing cluster would never receive it", k)
		case !inPayload:
			t.Errorf("%s is missing from realm-payload.json: a FRESH cluster keeps Keycloak's default, "+
				"which is how the 300 s access token shipped", k)
		case pv != hv:
			t.Errorf("%s disagrees between the two routes: payload %v, harden_realm %v", k, pv, hv)
		}
	}
}

// The early harden_realm call cannot touch a realm that does not exist yet, so
// the fresh-cluster path must call it again AFTER the realm is created.
func TestRealmHardeningRunsAfterTheRealmIsCreated(t *testing.T) {
	s := keycloakConfigSource(t)

	create := strings.Index(s, `kc -X POST --data @/var/config/realm-payload.json`)
	if create < 0 {
		t.Fatal("keycloak-config.yaml no longer creates the realm from realm-payload.json")
	}
	if !strings.Contains(s[create:], "harden_realm") {
		t.Error("harden_realm is never called after the realm is created: on a fresh cluster the early call " +
			"404s against a realm that does not exist yet, so the hardened lifespans are silently lost")
	}
	// And the definition has to come first, or the shell call is unbound.
	if def := strings.Index(s, "harden_realm() {"); def < 0 || def > create {
		t.Error("harden_realm must be defined before the realm-creation branch that calls it")
	}
}

// The access-token lifespan is what the Argo CD SSO proxy's refresh interval is
// sized against, so a change here has to stay consistent with that proxy.
// TestArgoCDSSOProxyRefreshesTheTokenItForwards reads the same value; this one
// pins that the realm actually declares one rather than inheriting a default
// nobody wrote down.
func TestRealmDeclaresItsAccessTokenLifespan(t *testing.T) {
	if got := adharRealmTokenLifespanSeconds(t); got <= 300 {
		t.Errorf("the adhar realm's accessTokenLifespan is %d s — at or below Keycloak's own default, "+
			"which means it is not actually declared in realm-payload.json", got)
	}
}
