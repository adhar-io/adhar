package get

import (
	"bytes"
	"os"
	"strings"
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

// A credential is never shortened, checked at the CALL SITE.
//
// `cmd/helpers` has a guard that an Unbounded table does not truncate, but the
// bug that shipped was a caller that did not ask for one: the credentials table
// computed its own budget, and when the shared table began charging the gutter
// to that budget every password lost its last characters to an ellipsis
// (reported 2026-10-11). The guard has to be here, where the table is built.
func TestCredentialsAreNeverTruncated(t *testing.T) {
	// 36 characters, the length the platform's generators actually produce.
	const secret = "Hc-5NuJsYBlkgafY8h-LFbyI-7/7Nr1jpJu-"
	// The rows must be WIDER than the default budget, or the test passes
	// whether or not the table is Unbounded and proves nothing. Under `go test`
	// stdout is a pipe, so TerminalBudget cannot read a width and falls back to
	// 110 — these rows come to ~117 cells, which a bounded table would shave.
	entries := []SecretEntry{
		{Service: "ArgoCD", Username: "admin", Password: secret},
		{
			Service:  "Keycloak user1 (administrator of this platform and its tenants)",
			Username: "user1@noreply.com",
			Password: secret,
		},
	}

	out := captureStdout(t, func() {
		if err := displaySecretEntries(entries, "platform"); err != nil {
			t.Fatalf("displaySecretEntries: %v", err)
		}
	})
	if strings.Count(out, secret) != 2 {
		t.Errorf("a password was shortened; the table must be Unbounded().\n%s", out)
	}
	if strings.Contains(out, "...") {
		t.Errorf("an ellipsis appears in the credentials table:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = saved
	return <-done
}
