package provider

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Headlamp forwards a token minted for `headlamp`; `adhar auth` mints one for
// `kubernetes`. The API server must accept both, with the prefixes the
// keycloak package's RBAC bindings name (oidc:platform-admin, …) — 2026-10-21.
func TestAuthenticationConfigTrustsKeycloakForEveryClient(t *testing.T) {
	out, err := AuthenticationConfigYAML(APIServerOIDC{IssuerURL: "https://keycloak.cloud.adhar.io/realms/adhar"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		JWT        []struct {
			Issuer struct {
				URL                 string   `yaml:"url"`
				Audiences           []string `yaml:"audiences"`
				AudienceMatchPolicy string   `yaml:"audienceMatchPolicy"`
			} `yaml:"issuer"`
			ClaimMappings struct {
				Username struct{ Claim, Prefix string } `yaml:"username"`
				Groups   struct{ Claim, Prefix string } `yaml:"groups"`
			} `yaml:"claimMappings"`
		} `yaml:"jwt"`
	}
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, out)
	}
	if cfg.Kind != "AuthenticationConfiguration" || cfg.APIVersion != "apiserver.config.k8s.io/v1" || len(cfg.JWT) != 1 {
		t.Fatalf("unexpected document:\n%s", out)
	}
	j := cfg.JWT[0]
	if j.Issuer.URL != "https://keycloak.cloud.adhar.io/realms/adhar" {
		t.Errorf("issuer %q", j.Issuer.URL)
	}
	if strings.Join(j.Issuer.Audiences, ",") != "headlamp,kubernetes" || j.Issuer.AudienceMatchPolicy != "MatchAny" {
		t.Errorf("audiences %v policy %q: Headlamp's token would be rejected", j.Issuer.Audiences, j.Issuer.AudienceMatchPolicy)
	}
	m := j.ClaimMappings
	if m.Username.Claim != "preferred_username" || m.Username.Prefix != "oidc:" || m.Groups.Claim != "groups" || m.Groups.Prefix != "oidc:" {
		t.Errorf("claim mappings %+v do not match the RBAC bindings (oidc:<group>)", m)
	}
	if _, err := AuthenticationConfigYAML(APIServerOIDC{IssuerURL: "http://keycloak/realms/adhar"}); err == nil {
		t.Error("a plain-http issuer was accepted")
	}
}

// The remote script changes a control plane, so it must refuse what it cannot
// verify and undo what does not come back.
func TestTheAPIServerScriptVerifiesBeforeAndRollsBackAfter(t *testing.T) {
	s, err := APIServerOIDCScript(APIServerOIDC{IssuerURL: "https://keycloak.cloud.adhar.io/realms/adhar"})
	if err != nil {
		t.Fatal(err)
	}
	order := []string{
		`[ -f "$M" ] || { echo "ADHAR_OIDC: skipped`, // only on a kubeadm control plane
		`grep -q -- '--oidc-issuer-url' "$M"`,        // never fights the legacy flags
		`curl -fsS --max-time 15 -o /dev/null "https://keycloak.cloud.adhar.io/realms/adhar/.well-known/openid-configuration"`, // issuer answers, trusted cert
		`mv /etc/kubernetes/authn/adhar.yaml.tmp /etc/kubernetes/authn/adhar.yaml`,
		`grep -q -- '--authentication-config=' "$M"`, // already wired: file refresh only
		`cp "$M" "$B"`, // backup BEFORE editing
		`mv "$T" "$M"`, // atomic replace
		`curl -fsk --max-time 5 https://127.0.0.1:6443/livez`, // health after restart
		`cp "$B" "$M"`, // rollback
	}
	at := 0
	for _, want := range order {
		i := strings.Index(s[at:], want)
		if i < 0 {
			t.Fatalf("script lost (or reordered) %q", want)
		}
		at += i + len(want)
	}
	if strings.Contains(s, "/etc/kubernetes/manifests/kube-apiserver.yaml.") {
		t.Error("the backup is written inside the manifests directory, where the kubelet would run it as a second API server")
	}
}
