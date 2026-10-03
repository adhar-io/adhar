package up

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// Each environment of a MULTI-environment run gets its own cluster.
//
// `ResolveClusterName` only ever returns --name or the default "adhar", so every
// environment in one `adhar up` targeted the SAME cluster. Provisioning
// dev/test/prod from one config built `adhar` for dev and then logged, twice:
//
//	INFO: Cluster 'adhar' already exists (gcp/adhar-cloud/adhar, status running)
//	      — reusing it; pass --recreate to build a fresh one
//
// Three environments silently sharing one cluster is worse than an error: each
// bootstrap overwrites the previous one's platform while the run reports success.
func TestEnvClusterNameSeparatesEnvironments(t *testing.T) {
	names := map[string]string{}
	for _, env := range []string{"dev", "test", "prod"} {
		got := envClusterName("", env, 3)
		if prev, clash := names[got]; clash {
			t.Fatalf("environments %q and %q both resolve to cluster %q — they would "+
				"overwrite each other's platform", prev, env, got)
		}
		names[got] = env
		if !strings.Contains(got, env) {
			t.Errorf("cluster name for %q should identify the environment, got %q", env, got)
		}
	}
	if len(names) != 3 {
		t.Errorf("expected 3 distinct cluster names, got %d: %v", len(names), names)
	}
}

// A SINGLE environment keeps the plain default. "default name keep adhar" is the
// stated preference and the overwhelmingly common case; suffixing it would
// rename every existing single-environment cluster.
func TestEnvClusterNameLeavesASingleEnvironmentAlone(t *testing.T) {
	if got := envClusterName("", "dev", 1); got != "" {
		t.Errorf("single environment must pass the override through untouched (empty => provider default), got %q", got)
	}
	if got := envClusterName("mycluster", "dev", 1); got != "mycluster" {
		t.Errorf("single environment with --name must use it verbatim, got %q", got)
	}
}

// An explicit --name stays authoritative in a multi-environment run: it becomes
// the prefix, so the operator's choice is honoured AND the names stay distinct.
func TestEnvClusterNameUsesTheOverrideAsAPrefix(t *testing.T) {
	got := envClusterName("acme", "prod", 3)
	if got != "acme-prod" {
		t.Errorf("--name must prefix in a multi-env run: got %q, want %q", got, "acme-prod")
	}
	if envClusterName("acme", "dev", 3) == got {
		t.Error("--name must still yield distinct names per environment")
	}
}

// Without --name the multi-env names are built on the platform default, so they
// stay recognisable as Adhar clusters.
func TestEnvClusterNameBuildsOnThePlatformDefault(t *testing.T) {
	got := envClusterName("", "dev", 2)
	if want := globals.DefaultClusterName + "-dev"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The CLI managers must skip controller-name validation.
//
// controller-runtime's check is process-GLOBAL (it guards the global metrics
// registry), and `adhar up` creates one manager PER ENVIRONMENT in a single
// process. So the second environment failed outright:
//
//	✖ prod: starting controllers: controller with name adharplatform already
//	  exists. Controller names must be unique to avoid multiple controllers
//	  reporting the same metric.
//
// 1 of 3 environments provisioned. Both CLI managers serve no metrics
// (`BindAddress: "0"`), so the validation protects nothing here — the in-cluster
// manager in cmd/controller deliberately keeps it.
func TestCLIManagersSkipControllerNameValidation(t *testing.T) {
	for _, f := range []string{"bootstrap.go", "local.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		src := string(b)
		if !strings.Contains(src, "SkipNameValidation: ptr.To(true)") {
			t.Errorf("%s: the CLI manager must set SkipNameValidation — without it a second "+
				"environment in the same `adhar up` process cannot start its controllers", f)
		}
		// Only meaningful because these managers serve no metrics. If that ever
		// changes, skipping the name check would allow duplicate metric
		// registration, so assert the pairing holds.
		if !strings.Contains(src, `BindAddress: "0"`) {
			t.Errorf("%s: SkipNameValidation is only safe while this manager serves no metrics; "+
				"Metrics BindAddress is no longer \"0\"", f)
		}
	}
}

// Environments are NAMESPACES by default, and only a dedicated cluster when
// asked for.
//
// Declaring dev/test/prod used to mean three clusters, three platform installs
// and three bills — and on a default GCP quota (CPUS_ALL_REGIONS 64 against ~40
// for one production cluster) three did not fit at all. `isolation: namespace`
// makes the common case one cluster with three namespaces; `isolation: cluster`
// is the opt-out for an environment that needs a separate API server and a
// separate blast radius.
func TestEnvironmentsDefaultToNamespaceIsolation(t *testing.T) {
	cfg := &config.Config{Environments: map[string]config.EnvironmentConfig{
		// Deliberately blank: an omitted field must mean namespace, because that
		// is what every existing config file has.
		"dev":  {Type: "non-production", Template: "t"},
		"test": {Type: "non-production", Template: "t", Isolation: config.EnvironmentIsolationNamespace},
		"prod": {Type: "production", Template: "t", Isolation: config.EnvironmentIsolationCluster},
	}}
	cfg.ResolvedEnvironments = map[string]*config.ResolvedEnvironmentConfig{
		"dev":  {Name: "dev", ResolvedType: "non-production", ResolvedIsolation: config.EnvironmentIsolationNamespace},
		"test": {Name: "test", ResolvedType: "non-production", ResolvedIsolation: config.EnvironmentIsolationNamespace},
		"prod": {Name: "prod", ResolvedType: "production", ResolvedIsolation: config.EnvironmentIsolationCluster},
	}

	clusterEnvs, nsEnvs, err := partitionEnvironments(cfg, []string{"dev", "prod", "test"})
	if err != nil {
		t.Fatalf("partitioning: %v", err)
	}
	if got := strings.Join(clusterEnvs, ","); got != "prod" {
		t.Errorf("only `isolation: cluster` environments get their own cluster; got [%s]", got)
	}
	if got := strings.Join(nsEnvs, ","); got != "dev,test" {
		t.Errorf("namespace environments: got [%s], want [dev,test]", got)
	}
}

// The shared cluster is sized from the PRODUCTION environment when there is one.
//
// It has to carry production's load; sizing it from `dev` would under-provision
// everything that shares it.
func TestSharedClusterIsSizedFromProduction(t *testing.T) {
	cfg := &config.Config{ResolvedEnvironments: map[string]*config.ResolvedEnvironmentConfig{
		"dev":  {Name: "dev", ResolvedType: "non-production", ResolvedIsolation: config.EnvironmentIsolationNamespace},
		"prod": {Name: "prod", ResolvedType: "production", ResolvedIsolation: config.EnvironmentIsolationNamespace},
		"test": {Name: "test", ResolvedType: "non-production", ResolvedIsolation: config.EnvironmentIsolationNamespace},
	}}
	if got := sharedClusterEnvironment(cfg, []string{"dev", "prod", "test"}); got != "prod" {
		t.Errorf("shared cluster must be sized from the production environment, got %q", got)
	}
	// With no production environment the choice must still be STABLE, or two
	// runs of the same file produce differently sized clusters.
	cfg2 := &config.Config{ResolvedEnvironments: map[string]*config.ResolvedEnvironmentConfig{
		"dev":  {Name: "dev", ResolvedType: "non-production", ResolvedIsolation: config.EnvironmentIsolationNamespace},
		"test": {Name: "test", ResolvedType: "non-production", ResolvedIsolation: config.EnvironmentIsolationNamespace},
	}}
	first := sharedClusterEnvironment(cfg2, []string{"dev", "test"})
	for i := 0; i < 5; i++ {
		if got := sharedClusterEnvironment(cfg2, []string{"dev", "test"}); got != first {
			t.Fatalf("selection must be deterministic: got %q then %q", first, got)
		}
	}
}

// The shared-cluster plan is SILENT on the local provider.
//
// A Kind cluster is one cluster by definition, so telling the operator that
// environments share it states the obvious — and the advice to set
// `isolation: cluster` actively misleads, offering a second Kind cluster as if
// that were a sensible local setup.
func TestSharedClusterPlanIsSilentOnLocalProvider(t *testing.T) {
	local := &config.Config{ResolvedEnvironments: map[string]*config.ResolvedEnvironmentConfig{
		"dev": {Name: "dev", ResolvedProvider: globals.CloudProviderKind},
	}}
	if !isLocalProviderEnv(local, "dev") {
		t.Error("a kind environment must be recognised as local, so the plan line is suppressed")
	}

	cloud := &config.Config{ResolvedEnvironments: map[string]*config.ResolvedEnvironmentConfig{
		"dev": {Name: "dev", ResolvedProvider: "gcp"},
	}}
	if isLocalProviderEnv(cloud, "dev") {
		t.Error("a cloud environment must NOT be suppressed — the message is the only place the " +
			"one-cluster decision is stated before any billable work starts")
	}

	// An unresolvable environment must NOT be silenced: a missing message is
	// harder to notice than a redundant one.
	if isLocalProviderEnv(cloud, "nope") {
		t.Error("an unknown environment must default to showing the message")
	}
}
