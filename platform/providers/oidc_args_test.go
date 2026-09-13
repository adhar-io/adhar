package provider

import (
	"strings"
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

	// Asking for it at creation time must FAIL LOUDLY, not silently no-op and not
	// brick the cluster. Setting --oidc-issuer-url during `kubeadm init` points
	// the apiserver at a Keycloak that does not exist yet; on a real DigitalOcean
	// build the apiserver never started and provisioning died at
	// `kubeadm token create`. Removing the flags recovered it in seconds.
	_, err = buildClusterSpec(base([]config.KeyValueConfig{{Key: "oidcAuth", Value: "true"}}))
	if err == nil {
		t.Fatal("oidcAuth at creation time must be rejected; it prevents the apiserver from starting")
	}
	for _, want := range []string{"oidcAuth", "after the platform is up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must explain the remedy (missing %q): %v", want, err)
		}
	}

	// A value that is not "true" is simply ignored, so an explicit opt-out is fine.
	if _, err := buildClusterSpec(base([]config.KeyValueConfig{{Key: "oidcAuth", Value: "false"}})); err != nil {
		t.Errorf("oidcAuth=false must be accepted: %v", err)
	}
}

// The sed script that inserts an apiserver flag must reach the node intact.
//
// This exists because a `\n` written in a double-quoted Go literal compiles to a
// REAL newline, which splits the sed script across two lines; the node then fails
// with `sed: -e expression #1, char 43: unterminated 's' command` and aborts
// `adhar up` AFTER the droplets are created. sed needs the two characters
// backslash + n, and nothing in the type system enforces that — only this test.
func TestAPIServerFlagCommand(t *testing.T) {
	cmd := apiServerFlagCommand("oidc-client-id", "kubernetes")

	if strings.Contains(cmd, "\n") {
		t.Fatalf("command contains a real newline; sed will see an unterminated 's' command:\n%q", cmd)
	}
	if !strings.Contains(cmd, `\n`) {
		t.Errorf("command must pass a literal backslash-n to sed, got:\n%q", cmd)
	}
	// Idempotence: it must test for the flag NAME before inserting, so a re-run
	// cannot add a second copy (duplicate apiserver flags are a start-up error).
	if !strings.Contains(cmd, "grep -q -- '--oidc-client-id='") {
		t.Errorf("command must be guarded by a grep on the flag name, got:\n%q", cmd)
	}
	if !strings.Contains(cmd, "--oidc-client-id=kubernetes") {
		t.Errorf("command must insert the flag with its value, got:\n%q", cmd)
	}
	if !strings.Contains(cmd, "/etc/kubernetes/manifests/kube-apiserver.yaml") {
		t.Errorf("command must target the static pod manifest, got:\n%q", cmd)
	}
}
