package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One credential must resolve the same way everywhere.
//
// `adhar up` built an Azure cluster from a secret FILE while edgeDNSSecretData —
// which knew only about AZURE_CLIENT_SECRET — wrote a stale environment value
// into the in-cluster `adhar-dns-provider` Secret. The cluster came up perfectly
// and then external-dns crash-looped on AADSTS7000215, so every platform
// hostname kept resolving to the previous cluster's dead load-balancer IP and
// every URL was unreachable (2026-10-04). Two resolvers for one credential was
// the bug.
func TestResolveSecretPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "from-config-file")
	envFile := filepath.Join(dir, "from-env-file")
	if err := os.WriteFile(cfgFile, []byte("config-file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("  env-file-value  "), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name            string
		inline, file    string
		envVal, envPath string
		want            string
	}{
		{"inline beats everything", "inline", cfgFile, "env", envFile, "inline"},
		// The inversion that matters: a path the operator configured must beat a
		// stale export they cannot see.
		{"config file beats the environment variable", "", cfgFile, "stale-export", envFile, "config-file-value"},
		{"environment variable when no file is configured", "", "", "env", envFile, "env"},
		{"environment file is the last resort", "", "", "", envFile, "env-file-value"},
		{"nothing configured", "", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SECRET_ENV", tc.envVal)
			t.Setenv("SECRET_ENV_FILE", tc.envPath)
			got, source, err := ResolveSecret(tc.inline, tc.file, "SECRET_ENV", "SECRET_ENV_FILE")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q (from %s), want %q", got, source, tc.want)
			}
			// Values read from files must be trimmed: an editor's trailing newline
			// produces the same opaque rejection as a wrong secret.
			if strings.TrimSpace(got) != got {
				t.Errorf("value was not trimmed: %q", got)
			}
		})
	}
}

// A configured path that cannot be read is an error, not a fallthrough.
// Degrading to "no credential" sends the operator looking for a credential
// problem that is really a typo in a path.
func TestResolveSecretErrorsOnAnUnreadableFile(t *testing.T) {
	_, _, err := ResolveSecret("", filepath.Join(t.TempDir(), "missing"), "", "")
	if err == nil {
		t.Fatal("a named but unreadable secret file must be an error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("the error must name the path, got: %v", err)
	}
}

// An empty file is a misconfiguration that otherwise presents as a credential
// failure much later, in-cluster.
func TestResolveSecretErrorsOnAnEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(p, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveSecret("", p, "", ""); err == nil {
		t.Fatal("an empty secret file must be an error")
	}
}

// The source must be reported: which source was used is the one fact that
// distinguishes a wrong secret from a wrong SOURCE, and it took four rounds of
// debugging precisely because nothing said it.
func TestResolveSecretNamesItsSource(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s")
	if err := os.WriteFile(p, []byte("v"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, src, _ := ResolveSecret("inline", "", "", ""); src != "configuration" {
		t.Errorf("inline source should be named, got %q", src)
	}
	if _, src, _ := ResolveSecret("", p, "", ""); src != p {
		t.Errorf("file source should be the path, got %q", src)
	}
	t.Setenv("SECRET_ENV", "v")
	if _, src, _ := ResolveSecret("", "", "SECRET_ENV", ""); src != "$SECRET_ENV" {
		t.Errorf("environment source should be named, got %q", src)
	}
}
