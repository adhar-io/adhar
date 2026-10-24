package adharplatform

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Airbyte runs with its built-in auth OFF so Keycloak (oauth2-proxy) is the
// only login (2026-10-24; data/airbyte values.yaml). In auth mode NONE the
// application-token endpoint the console used to mint a Bearer from has no
// backing service, so the console must NOT carry the instance-admin client
// credentials any more: their presence flips the tool registry into `login`
// mode and every /api/svc/airbyte call fails on the mint. The earlier
// requirement (2026-10-16, credentials present) held only while auth was on.
func TestTheConsoleCallsAirbyteWithoutClientCredentials(t *testing.T) {
	body := readStackFile(t, "core/adhar-console/manifests/install.yaml")
	if !strings.Contains(body, "- name: AIRBYTE_URL\n") {
		t.Fatal("console Deployment has no AIRBYTE_URL; /api/svc/airbyte has nowhere to go")
	}
	for _, name := range []string{"AIRBYTE_CLIENT_ID", "AIRBYTE_CLIENT_SECRET", "AIRBYTE_TOKEN"} {
		if strings.Contains(body, "- name: "+name+"\n") {
			t.Errorf("console Deployment carries %s, but Airbyte's auth is off: the BFF would try to mint a token "+
				"from an endpoint that does not exist in auth mode NONE", name)
		}
	}
}

// With Airbyte's auth off, the airbyte package itself must keep the API
// private and the first-run wizard done. Guard the pieces together, since any
// one of them missing either reopens the second login or opens the admin API.
func TestAirbyteKeycloakIsTheOnlyLogin(t *testing.T) {
	values := readStackFile(t, "data/airbyte/values.yaml")
	var v struct {
		Global struct {
			Auth struct {
				Enabled bool `json:"enabled"`
			} `json:"auth"`
		} `json:"global"`
	}
	if err := yaml.Unmarshal([]byte(values), &v); err != nil {
		t.Fatal(err)
	}
	if v.Global.Auth.Enabled {
		t.Skip("Airbyte's built-in auth is on again; this guard covers the auth-off model only")
	}

	install := readStackFile(t, "data/airbyte/manifests/install.yaml")
	if strings.Contains(install, "API_AUTHORIZATION_ENABLED") {
		t.Error("values.yaml has auth off but install.yaml still sets API_AUTHORIZATION_ENABLED; re-run generate-manifests.sh")
	}

	route := readStackFile(t, "data/airbyte/manifests/httproute.yaml")
	if !strings.Contains(route, "name: airbyte-oauth2-proxy") || strings.Contains(route, "name: airbyte-airbyte-server-svc") {
		t.Error("the airbyte HTTPRoute must point only at airbyte-oauth2-proxy: with Airbyte's auth off it is the only login")
	}

	np := readStackFile(t, "data/airbyte/manifests/networkpolicy.yaml")
	for _, want := range []string{
		"app.kubernetes.io/name: server",
		"app.kubernetes.io/name: workload-api-server",
		"{matchLabels: {app: airbyte-oauth2-proxy}}",
		"{matchLabels: {app: console}}",
		"{matchLabels: {airbyte: job-pod}}",
	} {
		if !strings.Contains(np, want) {
			t.Errorf("networkpolicy.yaml lost %q; the unauthenticated Airbyte API must stay fenced to its known callers", want)
		}
	}

	setup := readStackFile(t, "data/airbyte/manifests/setup.yaml")
	for _, want := range []string{
		"/api/v1/instance_configuration",
		`"initialSetupComplete":true`,
		"http://airbyte-airbyte-server-svc.adhar-system.svc.cluster.local:8001",
		"app.kubernetes.io/instance: airbyte",
		"argocd.argoproj.io/hook: PostSync",
	} {
		if !strings.Contains(setup, want) {
			t.Errorf("setup.yaml lost %q; the /setup wizard would greet users again", want)
		}
	}
}
