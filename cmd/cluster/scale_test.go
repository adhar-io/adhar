package cluster

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

const scaleTestConfig = `globalSettings:
  adharContext: test
  defaultHost: example.com
providers:
  do:
    type: digitalocean
    region: sgp1
    primary: true
    useEnvironment: true
    config:
      droplet_size: s-4vcpu-8gb
environmentTemplates:
  defaults:
    clusterConfig:
      - {key: autoScale, value: "true"}
    coreServices:
      cilium:
        chart: {repoURL: "https://helm.cilium.io/", name: cilium, version: 1.15.7}
environments:
  prod:
    type: production
    provider: do
    template: defaults
    clusterConfig:
      - {key: nodeCount, value: "2"}
    batchPool: {machineType: c-8, minNodes: 1, maxNodes: 3}
  dev:
    type: non-production
    provider: do
    template: defaults
    clusterConfig:
      - {key: nodeCount, value: "2"}
`

func scaleTestCfg(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(scaleTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("loading the test config: %v", err)
	}
	return cfg
}

// `adhar cluster scale <name> --node-group batch -f config.yaml` builds its
// provider with the environment layer `adhar up` used, so the pool's machine type
// (provider.KeyBatchMachineType) reaches the scale path that sizes a node for an
// EMPTY pool. It built the provider from the provider block alone, so a pool that
// had lost every node came back at the worker size.
func TestClusterScaleCarriesTheBatchPoolsMachineType(t *testing.T) {
	var built map[string]interface{}
	orig := createScaleProvider
	createScaleProvider = func(name string, m map[string]interface{}) (pfactory.Provider, error) {
		built = m
		return nil, errors.New("no cloud in a test")
	}
	t.Cleanup(func() { createScaleProvider = orig })
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	for _, c := range []struct {
		what, env, cluster, want string
	}{
		{"--env names the environment", "prod", "adhar", "c-8"},
		{"the environment is found by its cluster name", "", "prod", "c-8"},
		{"an environment with no batch pool", "dev", "adhar", ""},
		{"several environments share the cluster name: --env decides", "", "adhar", ""},
	} {
		t.Run(c.what, func(t *testing.T) {
			cfg := scaleTestCfg(t)
			env, err := scaleEnvironment(cfg, c.env, "", c.cluster)
			if err != nil {
				t.Fatal(err)
			}
			built = nil
			providerFlag := "do" // the config also carries the implicit kind provider
			if env != nil && env.ResolvedProvider != providerFlag {
				t.Fatalf("environment %s resolved to provider %s", env.Name, env.ResolvedProvider)
			}
			_, _, _ = resolveClusterProvider(cmd, cfg, c.cluster, providerFlag, env)
			if built == nil {
				t.Fatal("no provider was built")
			}
			if got := pfactory.ConfigString(built, pfactory.KeyBatchMachineType); got != c.want {
				t.Errorf("the scale provider carries batchMachineType %q, want %q", got, c.want)
			}
			if got := pfactory.ConfigString(built, "droplet_size"); got != "s-4vcpu-8gb" {
				t.Errorf("the provider block was lost: droplet_size %q", got)
			}
		})
	}

	// Two environments answer to the default cluster name: neither is guessed.
	cfg := scaleTestCfg(t)
	for i := 0; i < 20; i++ { // map order: a guess would show within a few tries
		if env, _ := scaleEnvironment(cfg, "", "", "adhar"); env != nil {
			t.Fatalf("cluster adhar is both environments' and %s was picked; --env must decide", env.Name)
		}
	}
	// An --env that does not exist, or that lives on another provider, is an error.
	if _, err := scaleEnvironment(cfg, "staging", "", "adhar"); err == nil {
		t.Error("an unknown --env was accepted")
	}
	if _, err := scaleEnvironment(cfg, "prod", "aws", "adhar"); err == nil {
		t.Error("an --env on another provider was accepted")
	}
	// The layer applies only to the environment's own provider.
	if m := scaleProviderMap("aws", config.ConfigProviderConfig{Type: "aws"}, cfg.ResolvedEnvironments["prod"]); m[pfactory.KeyBatchMachineType] != nil {
		t.Errorf("another provider's map got the environment layer: %v", m)
	}
}

// The command itself wires --env through: `adhar cluster scale adhar --node-group
// batch --workers 1 --env prod -f config.yaml` builds the provider with the pool's type.
func TestClusterScaleCommandPassesTheEnvironmentThrough(t *testing.T) {
	var built map[string]interface{}
	orig := createScaleProvider
	createScaleProvider = func(name string, m map[string]interface{}) (pfactory.Provider, error) {
		if name == "do" {
			built = m
		}
		return nil, errors.New("no cloud in a test")
	}
	t.Cleanup(func() { createScaleProvider = orig })
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(scaleTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().Int("workers", 1, "")
	cmd.Flags().String("node-group", "batch", "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("file", path, "")
	cmd.Flags().String("env", "prod", "")
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	if err := scaleCluster(cmd, "adhar"); err == nil {
		t.Fatal("the scale reported success with no cloud")
	}
	if got := pfactory.ConfigString(built, pfactory.KeyBatchMachineType); got != "c-8" {
		t.Errorf("the provider `adhar cluster scale --env prod` built carries batchMachineType %q, want c-8", got)
	}
	// Without an environment the batch pool's type is unknown, and it says so.
	cmd.Flags().Set("env", "") //nolint:errcheck
	out.Reset()
	_ = scaleCluster(cmd, "adhar")
	if !bytes.Contains(out.Bytes(), []byte("pass --env")) {
		t.Errorf("scaling the batch pool with no environment matched gives no warning:\n%s", out)
	}
}
