package config

import (
	"fmt"
	"os"
	"strings"
)

// ResolveSecret returns a secret from the first source that supplies one, and
// names that source so a caller can say which one it used.
//
// Precedence, strongest first:
//
//  1. inline      — a value written directly in configuration
//  2. file        — a path named in configuration
//  3. envVar      — an environment variable
//  4. envFileVar  — a path named in an environment variable
//
// Only 2-over-3 is unusual. The environment is the ambient, invisible source and
// a path in the config file is the deliberate one, so a stale `export` must not
// silently beat a path the operator just configured — and it cannot be caught by
// inspection, because a stale secret and a correct one look alike.
//
// This exists because the same credential was resolved in two places with two
// different sets of rules. `adhar up` built an Azure cluster from the secret
// FILE while edgeDNSSecretData, which knew only about AZURE_CLIENT_SECRET, wrote
// a stale environment value into the in-cluster `adhar-dns-provider` Secret. The
// cluster came up perfectly and then external-dns crash-looped on
// AADSTS7000215, so every platform hostname kept resolving to the previous
// cluster's dead load-balancer IP and every URL was unreachable (2026-10-04).
// One resolver, used by every caller, is the fix; divergence was the bug.
//
// A path that is named but unreadable is an error rather than a fallthrough:
// degrading to "no credential" sends the operator looking for a credential
// problem that is really a typo in a path.
func ResolveSecret(inline, file, envVar, envFileVar string) (value, source string, err error) {
	if v := strings.TrimSpace(inline); v != "" {
		return v, "configuration", nil
	}
	if f := strings.TrimSpace(file); f != "" {
		return readSecretFile(f)
	}
	if envVar != "" {
		if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
			return v, "$" + envVar, nil
		}
	}
	if envFileVar != "" {
		if f := strings.TrimSpace(os.Getenv(envFileVar)); f != "" {
			return readSecretFile(f)
		}
	}
	return "", "", nil
}

func readSecretFile(path string) (string, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("reading secret from %s: %w", path, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", "", fmt.Errorf("the secret file %s is empty", path)
	}
	return v, path, nil
}
