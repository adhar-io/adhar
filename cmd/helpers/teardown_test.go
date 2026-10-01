package helpers

import (
	"context"
	"testing"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// The names a teardown searches must include every name `adhar up` could have
// registered. Searching the wrong one finds nothing, reports "nothing to remove",
// and leaves the whole environment running and billing -- which is exactly how
// `adhar down` used to behave on a cloud.
//
// There are two schemes because the naming changed: `adhar up` now names the
// cluster after the PLATFORM (`adhar`, or `--name`), and used to name it after the
// ENVIRONMENT. Both must be searched or every pre-existing cloud cluster is
// orphaned by the upgrade.
func TestEnvironmentClusterNamesCoverBothSchemes(t *testing.T) {
	tests := []struct {
		name     string
		override string
		env      *config.ResolvedEnvironmentConfig
		want     []string
	}{
		{
			name: "default: platform name first, legacy environment name second",
			env:  &config.ResolvedEnvironmentConfig{Name: "dev"},
			want: []string{globals.DefaultClusterName, "dev"},
		},
		{
			name:     "--name wins, and the legacy name is still searched",
			override: "my-cluster",
			env:      &config.ResolvedEnvironmentConfig{Name: "dev"},
			want:     []string{"my-cluster", "dev"},
		},
		{
			// The shipped DigitalOcean config: clusterConfig.name is adhar-mgmt (a
			// tag/platform name) and must never be treated as the cluster name.
			name: "clusterConfig.name must NOT win",
			env: &config.ResolvedEnvironmentConfig{
				Name: "dev",
				ResolvedClusterConfig: []config.KeyValueConfig{
					{Key: "name", Value: "adhar-mgmt"},
					{Key: "nodeCount", Value: "3"},
				},
			},
			want: []string{globals.DefaultClusterName, "dev"},
		},
		{
			name: "no duplicate when the environment already carries the default name",
			env:  &config.ResolvedEnvironmentConfig{Name: globals.DefaultClusterName},
			want: []string{globals.DefaultClusterName},
		},
		{
			name: "nil environment still yields the platform default, never an empty target",
			env:  nil,
			want: []string{globals.DefaultClusterName},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EnvironmentClusterNames(tc.override, tc.env)
			if len(got) != len(tc.want) {
				t.Fatalf("EnvironmentClusterNames() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("EnvironmentClusterNames() = %v, want %v", got, tc.want)
				}
			}
			// The primary is what messages show, so it must be the first candidate.
			if primary := EnvironmentClusterName(tc.override, tc.env); primary != tc.want[0] {
				t.Errorf("EnvironmentClusterName() = %q, want %q", primary, tc.want[0])
			}
		})
	}
}

// A teardown must never treat "I could not ask the provider" as "there is
// nothing to delete".
func TestFindClusterRejectsUnusableInput(t *testing.T) {
	ctx := context.Background()

	if _, err := FindCluster(ctx, nil, "dev", nil, nil); err == nil {
		t.Fatal("a nil config must be an error, not a silent miss")
	}
	if _, err := FindCluster(ctx, &config.Config{}, "", nil, nil); err == nil {
		t.Fatal("an empty cluster name must be an error, not a silent miss")
	}
}

// Provider problems are surfaced to the caller rather than swallowed, so a
// credentials failure cannot masquerade as an already-deleted cluster.
func TestFindClusterReportsProviderProblems(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ConfigProviderConfig{
			"digitalocean": {Type: "digitalocean"},
		},
	}

	var warnings []string
	_, err := FindCluster(context.Background(), cfg, "dev", nil, func(w string) {
		warnings = append(warnings, w)
	})
	if err == nil {
		t.Fatal("expected a not-found error when no provider could answer")
	}
	if len(warnings) == 0 {
		t.Fatal("a provider that cannot be initialised or queried must warn, not fail silently")
	}
}

func TestIsAdharManaged(t *testing.T) {
	if (ResolvedCluster{}).IsAdharManaged() {
		t.Fatal("an empty cluster must not claim to be Adhar-managed")
	}
}
