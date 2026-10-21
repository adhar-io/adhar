package adharplatform

import (
	"strings"
	"testing"
)

// ESO does not watch the Secrets it reads, and retries a FAILED read on a
// backoff of 1s doubling to 7 minutes — refreshInterval governs only the
// period after a success. Every consumer of keycloak-clients fails until
// Keycloak's realm job writes it, so each one noticed the secret up to seven
// minutes late (2026-10-21). The realm job therefore nudges them the moment it
// has written the secret, on the full run and on the CronJob's clients run.
func TestTheRealmJobNudgesKeycloakClientConsumers(t *testing.T) {
	src := keycloakConfigSource(t)

	if n := strings.Count(src, "      reconcile_clients\n      nudge_waiting_consumers\n") +
		strings.Count(src, "    reconcile_clients\n    nudge_waiting_consumers\n"); n != 2 {
		t.Errorf("nudge_waiting_consumers follows reconcile_clients on %d path(s); want the full run AND the CronJob", n)
	}

	i := strings.Index(src, "nudge_waiting_consumers() {")
	if i < 0 {
		t.Fatal("nudge_waiting_consumers is not defined")
	}
	body := src[i : i+strings.Index(src[i:], "\n    }\n")]
	for _, want := range []string{
		`index("keycloak-clients")`,              // only the consumers of this secret
		`.type == "Ready" and .status == "True"`, // only those not yet synced
		"force-sync=",                            // the annotation change that re-enqueues immediately
		"return 0",                               // never fails the realm job
	} {
		if !strings.Contains(body, want) {
			t.Errorf("nudge_waiting_consumers lost %q", want)
		}
	}

	role := src[strings.Index(src, "kind: Role\n"):strings.Index(src, "kind: RoleBinding\n")]
	rule := `  - apiGroups: ["external-secrets.io"]
    resources: ["externalsecrets"]
    verbs: ["get", "list", "patch"]`
	if !strings.Contains(role, rule) {
		t.Error("the keycloak-config Role cannot list and patch ExternalSecrets; the nudge is denied and silently does nothing")
	}
}
