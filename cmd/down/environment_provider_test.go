package down

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
)

// writeRegionalConfig is a file whose `dev` environment runs in its OWN region
// and mode (environments.<env>.region, documented in examples/config.yaml, and a
// clusterConfig clusterMode): `adhar up` built dev's cluster in sgp1 as a managed
// cluster, whatever the provider block says.
func writeRegionalConfig(t *testing.T) string {
	t.Helper()
	body := `globalSettings:
  adharContext: adhar-mgmt
  defaultHost: platform.example.com
  defaultHttpPort: 80
  defaultHttpsPort: 443
  email: admin@example.com
providers:
  digitalocean:
    type: digitalocean
    region: blr1
    primary: true
    useEnvironment: true
    clusterMode: compute
environmentTemplates:
  base:
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
    isolation: cluster
    type: non-production
    provider: digitalocean
    template: base
    region: sgp1
    clusterConfig:
      - key: clusterMode
        value: managed
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every provider the teardown builds for an environment — to look the cluster
// up (and delete it), to purge, and to list its volumes once it is gone — is
// the provider `adhar up` built: the block, overlaid with the environment's
// region and clusterConfig. Built from the block alone it searched blr1 for a
// cluster that runs in sgp1, called it absent, and the volume step then dropped
// the running cluster's install record because nothing in blr1 carried its id;
// --purge-orphaned-volumes swept blr1.
func TestTheTeardownBuildsEachEnvironmentsProviderTheWayAdharUpDid(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := struct {
		file, env, name string
		keep, purge     bool
		find            func(context.Context, *config.Config, string, string, map[string]interface{}, func(string)) (*helpers.ResolvedCluster, error)
		build           func(*config.Config, string, map[string]interface{}) (pfactory.Provider, error)
	}{downConfigFile, downEnv, downClusterName, keepVolumes, purgeVolumes, findCluster, buildTeardownProvider}
	t.Cleanup(func() {
		downConfigFile, downEnv, downClusterName, keepVolumes, purgeVolumes = prev.file, prev.env, prev.name, prev.keep, prev.purge
		findCluster, buildTeardownProvider = prev.find, prev.build
	})
	downConfigFile, downEnv, downClusterName, keepVolumes, purgeVolumes = writeRegionalConfig(t), "dev", "", false, true

	var lookups, built []map[string]interface{}
	findCluster = func(_ context.Context, _ *config.Config, providerName, name string, opts map[string]interface{}, _ func(string)) (*helpers.ResolvedCluster, error) {
		lookups = append(lookups, opts)
		return nil, &helpers.NotFoundError{Name: name, Searched: []string{providerName}}
	}
	buildTeardownProvider = func(_ *config.Config, _ string, opts map[string]interface{}) (pfactory.Provider, error) {
		built = append(built, opts)
		return nil, errors.New("no cloud in this test")
	}

	sub := make(chan tea.Msg, 1024)
	var details []string
	teardownFromConfig(func(m tea.Msg) { sub <- m }, func(f string, a ...interface{}) {
		details = append(details, fmt.Sprintf(f, a...))
	})
	close(sub)
	var outcome teardownOutcome
	var teardownErr error
	for m := range sub {
		switch m := m.(type) {
		case teardownOutcome:
			outcome = m
		case logger.ErrorMsg:
			teardownErr = m.Err
		}
	}
	t.Logf("teardown error: %v\n%s", teardownErr, strings.Join(details, "\n"))

	asAdharUpBuiltIt := func(what string, opts map[string]interface{}) {
		t.Helper()
		if opts["region"] != "sgp1" || opts["clusterMode"] != pfactory.ClusterModeManaged || !pfactory.KeepClusterVolumesRequested(opts) {
			t.Errorf("%s was built with %v; want the environment's region (sgp1) and clusterMode (managed), "+
				"as `adhar up` built it, with the volumes left to the volume step", what, opts)
		}
	}
	if len(lookups) == 0 {
		t.Fatal("the cluster was never looked up")
	}
	for _, o := range lookups {
		asAdharUpBuiltIt("the lookup's provider", o)
	}
	if len(built) != 1 {
		t.Fatalf("the purge built %d provider(s), want 1", len(built))
	}
	asAdharUpBuiltIt("the purge's provider", built[0])

	// The cluster was absent, so the volume step builds the provider itself —
	// the same one.
	if len(outcome.VolumeJobs) != 1 {
		t.Fatalf("volume jobs = %+v; want the absent cluster's", outcome.VolumeJobs)
	}
	built = nil
	_, _ = newVolumeStep(nil, teardownProviderOptions()).managerFor(outcome.VolumeJobs[0])
	if len(built) != 1 {
		t.Fatalf("the volume step built %d provider(s), want 1", len(built))
	}
	asAdharUpBuiltIt("the volume step's provider", built[0])
}
