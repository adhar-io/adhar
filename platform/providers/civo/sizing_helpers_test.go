package civo

import (
	"os"
	"path/filepath"
	"testing"

	"adhar-io/adhar/platform/config"
	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// shippedEnv loads a config file from the repository root and returns what
// `adhar up` would hand this provider for one environment: the cluster spec and
// the provider map. Credential keys are stripped so no test ever reads a
// secret (or a secret FILE) the operator keeps beside the live configs.
func shippedEnv(t *testing.T, file, env string) (*types.ClusterSpec, map[string]interface{}) {
	t.Helper()
	path := filepath.Join("..", "..", "..", file)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("%s is not present (the live configs are gitignored)", file)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("%s no longer loads: %v", file, err)
	}
	if err := cfg.ResolveEnvironments(); err != nil {
		t.Fatal(err)
	}
	e := cfg.ResolvedEnvironments[env]
	if e == nil {
		t.Fatalf("%s has no environment %s", file, env)
	}
	spec, err := provider.PreviewClusterSpec(e)
	if err != nil {
		t.Fatal(err)
	}
	m := provider.ProviderMap(e)
	for _, k := range []string{"clientSecretFile", "clientSecret", "serviceAccountKeyFile", "serviceAccountKey",
		"token", "tokenFile", "accessKeyId", "secretAccessKey", "sessionToken", "credentials_file", "credentialsFile"} {
		delete(m, k)
	}
	return spec, m
}
