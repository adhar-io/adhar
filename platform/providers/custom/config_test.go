package custom

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"adhar-io/adhar/platform/config"
)

// documentedConfig is the layout docs/CUSTOM_PROVIDER.md tells users to write:
// the hosts live under the provider's `config:` block, camel-cased.
const documentedConfig = `
globalSettings:
  defaultHost: platform.example.com
  defaultHttpPort: 80
  defaultHttpsPort: 443
  email: admin@example.com

providers:
  custom:
    type: custom
    region: on-prem
    primary: true
    config:
      masterIPs:
        - 192.0.2.10
      workerIPs:
        - 192.0.2.11
        - 192.0.2.12
        - 192.0.2.13
      sshUser: ubuntu
      sshKeyPath: ~/.ssh/id_ed25519
      sshPort: 22

environmentTemplates:
  nonprod-defaults:
    clusterConfig:
      - key: autoScale
        value: "false"
    coreServices:
      cilium:
        chart:
          repoURL: https://helm.cilium.io/
          name: cilium
          version: 1.15.7

environments:
  dev:
    type: non-production
    provider: custom
    template: nonprod-defaults
    clusterConfig:
      - key: nodeCount
        value: "3"
`

// The documented config, run through the REAL loader, must reach the provider.
// ToProviderMap nests the block under "config" and Viper lower-cases its keys,
// so a factory reading only top-level camel-cased keys rejected a correct file
// with "at least one control-plane host IP is required".
func TestConfigFromMapReadsTheDocumentedLayoutThroughTheLoader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(documentedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.ResolveEnvironments(); err != nil {
		t.Fatalf("ResolveEnvironments: %v", err)
	}
	env, ok := cfg.ResolvedEnvironments["dev"]
	if !ok || env.ProviderConfig == nil {
		t.Fatalf("dev environment or its provider config missing: %+v", env)
	}

	m := env.ProviderConfig.ToProviderMap()
	got := configFromMap(m)
	want := &Config{
		MasterIPs:  []string{"192.0.2.10"},
		WorkerIPs:  []string{"192.0.2.11", "192.0.2.12", "192.0.2.13"},
		SSHUser:    "ubuntu",
		SSHKeyPath: "~/.ssh/id_ed25519",
		SSHPort:    22,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configFromMap(loader map) =\n  %+v\nwant\n  %+v\nmap was: %#v", got, want, m)
	}
}

// Callers that pass the keys at the top level (and the legacy names) keep working.
func TestConfigFromMapAcceptsTopLevelAndLegacyKeys(t *testing.T) {
	got := configFromMap(map[string]interface{}{
		"nodeIPs":    []interface{}{"10.0.0.1", "10.0.0.2"},
		"username":   "root",
		"sshKeyPath": "/keys/id",
		"sshPort":    float64(22),
	})
	if !reflect.DeepEqual(got.NodeIPs, []string{"10.0.0.1", "10.0.0.2"}) || got.SSHUser != "root" ||
		got.SSHKeyPath != "/keys/id" || got.SSHPort != 22 {
		t.Fatalf("top-level/legacy keys not read: %+v", got)
	}
}

// ListClusters reports the cluster under the name it was created with, so
// `adhar down` / `cluster scale` find it; before the marker it invented
// "on-premises-cluster", which no lookup by name matched.
func TestClusterNameFromMarker(t *testing.T) {
	cases := map[string]string{
		"adhar\n":          "adhar",
		"payments-prod":    "payments-prod",
		"":                 "adhar", // cluster created before the marker existed
		"bad name; rm -rf": "adhar", // never trust a value that isn't a DNS label
	}
	for in, want := range cases {
		if got := clusterNameFromMarker(in); got != want {
			t.Errorf("clusterNameFromMarker(%q) = %q, want %q", in, got, want)
		}
	}
}
