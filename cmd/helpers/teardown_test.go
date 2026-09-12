package helpers

import (
	"context"
	"testing"

	"adhar-io/adhar/platform/config"
)

// The cluster name a teardown looks for must be the one `adhar up` registered.
// buildClusterSpec sets ObjectMeta.Name from the ENVIRONMENT name, so that is
// what providers list. Keying on `clusterConfig.name` instead finds nothing,
// reports "nothing to remove", and leaves the whole environment running and
// billing -- which is exactly how `adhar down` used to behave on a cloud.
func TestEnvironmentClusterNameIsTheEnvironmentName(t *testing.T) {
	tests := []struct {
		name string
		env  *config.ResolvedEnvironmentConfig
		want string
	}{
		{
			name: "plain environment",
			env:  &config.ResolvedEnvironmentConfig{Name: "dev"},
			want: "dev",
		},
		{
			// The shipped DigitalOcean config: clusterConfig.name is adhar-mgmt
			// (a tag/platform name) while the cluster is registered as "dev".
			name: "clusterConfig.name must NOT win",
			env: &config.ResolvedEnvironmentConfig{
				Name: "dev",
				ResolvedClusterConfig: []config.KeyValueConfig{
					{Key: "name", Value: "adhar-mgmt"},
					{Key: "nodeCount", Value: "3"},
				},
			},
			want: "dev",
		},
		{
			name: "nil environment yields empty, never a wrong target",
			env:  nil,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnvironmentClusterName(tc.env); got != tc.want {
				t.Fatalf("EnvironmentClusterName() = %q, want %q", got, tc.want)
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
