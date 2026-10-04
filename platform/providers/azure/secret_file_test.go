package azure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The client secret can come from a FILE, so it never passes through a shell.
//
// Passing a secret via `export AZURE_CLIENT_SECRET=…` is the most error-prone
// step in setting Azure up, and every way it fails is silent:
//
//   - double quotes let the shell expand an unescaped `$`, truncating the value;
//   - an export in one terminal is invisible in the next;
//   - a STALE export is indistinguishable from a correct one, because an Azure
//     secret value and the previous Azure secret value are both ~40 characters.
//
// On 2026-10-04 that produced four consecutive failed runs against a credential
// that was provably valid the whole time — the shell held an earlier secret, and
// no error message could tell the two apart. A path has none of those failure
// modes, which is also why a path is safe to keep in a config file.
func TestClientSecretIsReadFromAFile(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "azure-client-secret")
	const secret = "abc8Q~dEf1GhIjKlMnOpQrStUvWxYz0123456789"
	// Written with a trailing newline, which is what any editor or `>` produces.
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := parseProviderConfig(map[string]interface{}{
		"subscriptionId":   "sub",
		"clientId":         "client",
		"tenantId":         "tenant",
		"clientSecretFile": secretFile,
	})
	if err != nil {
		t.Fatalf("parseProviderConfig: %v", err)
	}
	if cfg.ClientSecret != secret {
		t.Errorf("secret not read from the file (or not trimmed): got %q (len %d), want len %d",
			cfg.ClientSecret, len(cfg.ClientSecret), len(secret))
	}
}

// The environment variant exists for CI, where a file is mounted at a path the
// config cannot know in advance.
func TestClientSecretFileFromTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("  s3cret-value  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_CLIENT_SECRET_FILE", secretFile)
	t.Setenv("AZURE_CLIENT_SECRET", "")

	cfg, err := parseProviderConfig(map[string]interface{}{"subscriptionId": "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "s3cret-value" {
		t.Errorf("got %q, want %q", cfg.ClientSecret, "s3cret-value")
	}
}

// An explicitly configured inline secret still wins, so adding the file option
// cannot change what an existing config resolves to.
func TestInlineSecretTakesPrecedenceOverTheFile(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseProviderConfig(map[string]interface{}{
		"subscriptionId":   "sub",
		"clientSecret":     "inline",
		"clientSecretFile": secretFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "inline" {
		t.Errorf("an explicit clientSecret must win; got %q", cfg.ClientSecret)
	}
}

// A path that cannot be read must not resolve to "no credential" silently: that
// sends the operator hunting for a credential problem that is really a typo in a
// path. The run still fails, but the log says which file and why.
func TestUnreadableSecretFileDoesNotMasqueradeAsNoCredential(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{
		"subscriptionId":   "sub",
		"clientSecretFile": filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "" {
		t.Errorf("an unreadable file must leave the secret empty, got %q", cfg.ClientSecret)
	}
}

// The option must be reachable from a config FILE, not just from Go — the whole
// point is that `adhar up -f config.azure.yaml` needs no environment at all.
// platform/config must therefore carry the field and pass it through.
func TestClientSecretFileIsPlumbedFromTheConfigLayer(t *testing.T) {
	for _, f := range []string{"../../config/config.go", "../../config/helpers.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		if !strings.Contains(string(b), "clientSecretFile") {
			t.Errorf("%s does not carry clientSecretFile — the key would be dropped between the "+
				"config file and the provider, and the secret would silently fall back to the "+
				"environment", f)
		}
	}
}

// A key the parser acts on must not be reported as ignored. clientId and
// tenantId were parsed but absent from the known-key list, so a working config
// was told they were "not recognised and ignored".
func TestAuthenticationKeysAreRecognised(t *testing.T) {
	b, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "known := map[string]bool{}")
	if start < 0 {
		t.Fatal("the known-key list has moved; this guard needs updating")
	}
	list := src[start:]
	if end := strings.Index(list, "\n\t\t}\n"); end > 0 {
		list = list[:end]
	}
	for _, key := range []string{"clientId", "clientSecret", "clientSecretFile", "tenantId", "useAzureCLI"} {
		if !strings.Contains(list, `"`+key+`"`) {
			t.Errorf("%q is parsed but not registered as known, so it is reported as ignored "+
				"while being acted on", key)
		}
	}
}

// An explicitly configured clientSecretFile must beat AZURE_CLIENT_SECRET.
//
// This inverts the usual "environment overrides config" rule on purpose. The
// variable is the ambient, invisible source; the config key is the deliberate
// one. A stale export silently outranking a path the operator just wrote into
// their config is the exact trap clientSecretFile exists to remove — and it is
// undetectable by inspection, because a stale secret and a correct one are both
// about 40 characters.
//
// Live on 2026-10-04: a secret exported hours earlier turned out to be valid for
// a DIFFERENT app registration in the same tenant, so every run failed with
// AADSTS7000215 against a configured secret file that was known good. Four
// rounds of debugging went into a credential that was never being used.
func TestConfiguredSecretFileBeatsAStaleEnvironmentVariable(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("the-right-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_CLIENT_SECRET", "the-stale-secret-left-in-a-shell")

	cfg, err := parseProviderConfig(map[string]interface{}{
		"subscriptionId":   "sub",
		"clientSecretFile": secretFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "the-right-secret" {
		t.Errorf("the configured file must win over an ambient AZURE_CLIENT_SECRET; got %q", cfg.ClientSecret)
	}
}

// The ambient file variant stays a FALLBACK: with no clientSecretFile
// configured, AZURE_CLIENT_SECRET still wins, so existing setups are unchanged.
func TestEnvSecretStillWinsOverTheEnvFileFallback(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("from-env-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_CLIENT_SECRET", "from-env-var")
	t.Setenv("AZURE_CLIENT_SECRET_FILE", secretFile)

	cfg, err := parseProviderConfig(map[string]interface{}{"subscriptionId": "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "from-env-var" {
		t.Errorf("without an explicit clientSecretFile, AZURE_CLIENT_SECRET must still win; got %q",
			cfg.ClientSecret)
	}
}
