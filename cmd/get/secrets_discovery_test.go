package get

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func secret(name string, t corev1.SecretType, data map[string]string) corev1.Secret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	return corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "adhar-system"}, Type: t, Data: d}
}

// `adhar get secrets --all` reported a small, arbitrary subset. Two mechanisms
// fed it — a hand-written registry of thirteen providers, and the
// `adhar.io/cli-secret` label, applied by fourteen files across seventy-five
// enabled packages — so most of the platform's credentials were invisible while
// the command's own help promised "all platform secrets".
//
// These tests pin the classification that the discovery pass relies on: anything
// carrying a real credential is reported, and the namespace's considerable noise
// is not.
func TestCredentialClassificationIncludesRealCredentials(t *testing.T) {
	for _, s := range []corev1.Secret{
		secret("grafana-admin", corev1.SecretTypeOpaque, map[string]string{"admin-user": "admin", "admin-password": "p"}),
		secret("nexus-admin", corev1.SecretTypeOpaque, map[string]string{"username": "admin", "password": "p"}),
		secret("mysql-root", corev1.SecretTypeOpaque, map[string]string{"mysql-root-password": "p"}),
		secret("opensearch", corev1.SecretTypeOpaque, map[string]string{"username": "admin", "password": "p"}),
		secret("api", corev1.SecretTypeOpaque, map[string]string{"api-token": "t"}),
	} {
		if nonCredentialSecretTypes[s.Type] {
			t.Errorf("%s: type %s should not be excluded", s.Name, s.Type)
		}
		pass := firstKey(s, "password", "admin-password", "adminPassword", "instance-admin-password",
			"rootPassword", "root-password", "postgres-password", "mysql-root-password",
			"admin-token", "api-token", "access-token", "token")
		if pass == "" {
			t.Errorf("%s: a real credential was not recognised (keys: %v)", s.Name, s.Data)
		}
	}
}

// The namespace is full of things that are not credentials. Reporting them would
// make --all useless noise, which is its own kind of "not all secrets".
func TestCredentialClassificationExcludesNoise(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret corev1.Secret
	}{
		{"service account token", secret("default-token-abc", corev1.SecretTypeServiceAccountToken, map[string]string{"token": "x"})},
		{"TLS certificate", secret("adhar-tls", corev1.SecretTypeTLS, map[string]string{"tls.crt": "x", "tls.key": "y"})},
		{"image pull secret", secret("regcred", corev1.SecretTypeDockerConfigJson, map[string]string{".dockerconfigjson": "x"})},
		{"helm release state", secret("sh.helm.release.v1.argo-cd.v1", "helm.sh/release.v1", map[string]string{"release": "x"})},
		{"ssh key", secret("git-ssh", corev1.SecretTypeSSHAuth, map[string]string{"ssh-privatekey": "x"})},
	} {
		if !nonCredentialSecretTypes[tc.secret.Type] {
			t.Errorf("%s (%s) should be excluded from credential discovery", tc.name, tc.secret.Type)
		}
	}

	// A CA bundle is Opaque but carries nothing to sign in with.
	ca := secret("webhook-ca", corev1.SecretTypeOpaque, map[string]string{"ca.crt": "x"})
	if pass := firstKey(ca, "password", "token", "admin-password"); pass != "" {
		t.Error("a CA bundle must not be reported as a credential")
	}
}

// OpenBao is the platform's production secrets backend — the `vault` package is
// disabled — yet only `vault` was in the registry, so the unseal keys and root
// token an operator actually needs were not listed under any provider name.
func TestOpenBaoIsAKnownProvider(t *testing.T) {
	cfg, ok := knownProviders["openbao"]
	if !ok {
		t.Fatal("openbao must be a known provider: it is the production secrets backend")
	}
	found := false
	for _, p := range cfg.patterns {
		if p == "openbao-keys" || p == "openbao-root-token" {
			found = true
		}
	}
	if !found {
		t.Errorf("openbao patterns should cover its unseal keys and root token, got %v", cfg.patterns)
	}
}

// The default view is deliberately small — the credentials needed to sign in —
// but that must not be described as "all".
func TestEssentialProvidersAreTheSignInCredentials(t *testing.T) {
	want := map[string]bool{"argocd": true, "gitea": true, "keycloak-admin": true, "keycloak-user": true}
	if len(essentialProviders) != len(want) {
		t.Errorf("essentialProviders = %v", essentialProviders)
	}
	for _, p := range essentialProviders {
		if !want[p] {
			t.Errorf("unexpected default provider %q — the default view is for signing in", p)
		}
		if _, ok := knownProviders[p]; !ok {
			t.Errorf("default provider %q is not in knownProviders, so it resolves to nothing", p)
		}
	}
}

// Every provider offered in --provider's help must actually exist, or the flag
// advertises names that silently return nothing.
func TestAdvertisedProvidersAllResolve(t *testing.T) {
	for _, name := range []string{
		"argocd", "gitea", "keycloak", "adhar-console", "openbao", "vault",
		"postgres", "redis", "harbor", "rustfs",
	} {
		if _, ok := knownProviders[name]; !ok {
			t.Errorf("provider %q is advertised but has no configuration", name)
		}
	}
}
