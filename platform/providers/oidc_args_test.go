package provider

import (
	"testing"

	"adhar-io/adhar/platform/config"
)

// The apiserver flags must match the `oidc:`-prefixed Group subjects in the
// ClusterRoleBindings security/keycloak ships; a mismatch authenticates the user
// but authorizes nothing, which is the confusing failure this pins down.
func TestAPIServerOIDCArgs(t *testing.T) {
	got := apiServerOIDCArgs("platform.adhar.io")
	want := map[string]string{
		"oidc-issuer-url": "https://keycloak.platform.adhar.io/realms/adhar",
		// The token's `aud`, not its `azp`: --oidc-client-id is matched against the
		// audience, so "adhar-cli" here would reject every token with a bare
		// "Unauthorized" — which is exactly the failure this pins down.
		"oidc-client-id":       "kubernetes",
		"oidc-username-claim":  "preferred_username",
		"oidc-username-prefix": "oidc:",
		"oidc-groups-claim":    "groups",
		"oidc-groups-prefix":   "oidc:",
	}
	if len(got) != len(want) {
		t.Fatalf("arg count = %d, want %d (%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// No domain means no issuer can be formed; emitting a half-built flag would
// wedge the API server, so the builder must return nothing at all.
func TestAPIServerOIDCArgsEmptyDomain(t *testing.T) {
	if got := apiServerOIDCArgs(""); got != nil {
		t.Fatalf("expected nil for an empty domain, got %v", got)
	}
}

// The opt-in has to travel: `oidcAuth: "true"` in an environment's clusterConfig
// must land as apiserver ExtraArgs on the spec, because that map is what every
// kubeadm provider hands to KubeadmInitMaster. Nothing read APIServer.ExtraArgs
// before this change, so a silent regression here would leave the flags unset
// and the shipped oidc: RoleBindings unmatchable.
func TestBuildClusterSpecOIDCOptIn(t *testing.T) {
	base := func(kv []config.KeyValueConfig) *config.ResolvedEnvironmentConfig {
		return &config.ResolvedEnvironmentConfig{
			Name:                  "dev",
			ResolvedProvider:      "digitalocean",
			ResolvedRegion:        "blr1",
			ResolvedClusterConfig: kv,
			GlobalSettings:        &config.GlobalSettings{DefaultHost: "platform.adhar.io"},
		}
	}

	// Default: no flags at all, so an existing cluster's auth is untouched.
	spec, err := buildClusterSpec(base(nil))
	if err != nil {
		t.Fatalf("buildClusterSpec: %v", err)
	}
	if n := len(spec.ControlPlane.APIServer.ExtraArgs); n != 0 {
		t.Fatalf("OIDC must be OFF by default, got %d apiserver arg(s): %v",
			n, spec.ControlPlane.APIServer.ExtraArgs)
	}

	// Opted in.
	spec, err = buildClusterSpec(base([]config.KeyValueConfig{{Key: "oidcAuth", Value: "true"}}))
	if err != nil {
		t.Fatalf("buildClusterSpec: %v", err)
	}
	got := spec.ControlPlane.APIServer.ExtraArgs
	if got["oidc-issuer-url"] != "https://keycloak.platform.adhar.io/realms/adhar" {
		t.Errorf("issuer = %q", got["oidc-issuer-url"])
	}
	if got["oidc-groups-prefix"] != "oidc:" || got["oidc-username-prefix"] != "oidc:" {
		t.Errorf("prefixes must be oidc: to match the shipped bindings, got %v", got)
	}

	// An explicit issuer wins, for a platform not served on :443.
	spec, err = buildClusterSpec(base([]config.KeyValueConfig{
		{Key: "oidcAuth", Value: "true"},
		{Key: "oidcIssuerUrl", Value: "https://keycloak.example.com:8443/realms/adhar"},
	}))
	if err != nil {
		t.Fatalf("buildClusterSpec: %v", err)
	}
	if u := spec.ControlPlane.APIServer.ExtraArgs["oidc-issuer-url"]; u != "https://keycloak.example.com:8443/realms/adhar" {
		t.Errorf("explicit oidcIssuerUrl must win, got %q", u)
	}
}
