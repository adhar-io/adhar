package adharplatform

import (
	"strings"
	"testing"
)

// The console authenticates to Airbyte 2.x with an application's client
// credentials (the only thing the API accepts once API_AUTHORIZATION_ENABLED
// is on). With just AIRBYTE_URL every call was 401 (2026-10-16). The console
// Deployment must carry the instance-admin application's id and secret from
// the airbyte package's `airbyte-auth-secrets`, optional so a profile without
// Airbyte still starts the console.
func TestTheConsoleCarriesAirbyteClientCredentials(t *testing.T) {
	body := readStackFile(t, "core/adhar-console/manifests/install.yaml")
	for _, env := range []struct{ name, key string }{
		{"AIRBYTE_CLIENT_ID", "instance-admin-client-id"},
		{"AIRBYTE_CLIENT_SECRET", "instance-admin-client-secret"},
	} {
		i := strings.Index(body, "- name: "+env.name+"\n")
		if i < 0 {
			t.Errorf("console Deployment has no %s; the console cannot authenticate to Airbyte", env.name)
			continue
		}
		block := body[i:]
		if j := strings.Index(block[1:], "\n        - name: "); j >= 0 {
			block = block[:j+1]
		}
		if !strings.Contains(block, "name: airbyte-auth-secrets") || !strings.Contains(block, "key: "+env.key) || !strings.Contains(block, "optional: true") {
			t.Errorf("%s must come from Secret airbyte-auth-secrets key %q and be optional; got:\n%s", env.name, env.key, block)
		}
	}
}
