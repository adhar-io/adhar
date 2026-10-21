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

	if n := strings.Count(src, "\n      reconcile_clients_and_nudge\n") +
		strings.Count(src, "\n    reconcile_clients_and_nudge\n"); n != 2 {
		t.Errorf("reconcile_clients_and_nudge runs on %d path(s); want the full run AND the CronJob", n)
	}
	j := strings.Index(src, "reconcile_clients_and_nudge() {")
	if j < 0 {
		t.Fatal("reconcile_clients_and_nudge is not defined")
	}
	wrap := src[j : j+strings.Index(src[j:], "\n    }\n")]
	// Registering ~30 clients took 3.5 minutes on GCP and each client's keys
	// land as it is registered: nudge DURING the pass, then once at the end.
	for _, want := range []string{"while sleep 20; do nudge_waiting_consumers; done ) &", "reconcile_clients\n",
		`kill "${nudger}"`, "\n      nudge_waiting_consumers"} {
		if !strings.Contains(wrap, want) {
			t.Errorf("reconcile_clients_and_nudge lost %q", want)
		}
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
		"done || true",                           // set -e -o pipefail: a kubectl hiccup must not fail the job
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
