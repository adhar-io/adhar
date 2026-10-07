package down

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

// `adhar down` must never delete a cluster the platform did not create.
//
// This is the whole reason `clusterMode: provided` can be offered at all. The
// teardown locates a cluster by name across every configured provider and then
// calls DeleteCluster on whatever it finds, so the mode has to be read BEFORE
// the lookup — which is what the loop in teardownFromConfig does with this
// function.
func TestTeardownReadsTheClusterModeBeforeDeletingAnything(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		ours    bool
		wantErr bool
	}{
		{"provided clusters are never ours", "provided", false, false},
		{"compute clusters are ours", "compute", true, false},
		{"managed clusters are ours", "managed", true, false},
		{"an unspecified mode is the default, compute", "", true, false},
		{"a mode nobody recognises is an error, not a default", "k3s", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := &config.ResolvedEnvironmentConfig{
				Name:             "prod",
				ResolvedProvider: "civo",
				ProviderConfig:   &config.ConfigProviderConfig{Type: "civo", ClusterMode: tc.mode},
			}
			mode, err := environmentClusterMode(env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("clusterMode %q was accepted; an unrecognised mode must stop the teardown rather than "+
						"fall back to deleting the cluster", tc.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("clusterMode %q: %v", tc.mode, err)
			}
			if got := pfactory.ClusterLifecycleIsOurs(mode); got != tc.ours {
				t.Errorf("clusterMode %q: lifecycle is ours = %v, want %v", tc.mode, got, tc.ours)
			}
		})
	}
}

// An environment with no provider block at all must not read as "provided" and
// silently skip a teardown that should have happened.
func TestTeardownWithoutAProviderBlockStillDeletes(t *testing.T) {
	mode, err := environmentClusterMode(&config.ResolvedEnvironmentConfig{Name: "prod"})
	if err != nil {
		t.Fatalf("environmentClusterMode: %v", err)
	}
	if !pfactory.ClusterLifecycleIsOurs(mode) {
		t.Error("an environment with no provider block was treated as a provided cluster, so its cluster would survive the teardown")
	}
}

// The decision has to be WIRED IN ahead of the lookup, not merely available.
// Checked against the source because the loop around it needs a config file,
// live cloud credentials and a cluster to exercise end to end.
func TestTeardownGateRunsBeforeTheClusterIsLocated(t *testing.T) {
	src, err := os.ReadFile("down.go")
	if err != nil {
		t.Fatalf("reading down.go: %v", err)
	}
	body := string(src)

	gate := strings.Index(body, "environmentClusterMode(env)")
	lookup := strings.Index(body, "helpers.FindCluster(")
	del := strings.Index(body, "found.Provider.DeleteCluster(")

	if gate < 0 {
		t.Fatal("teardownFromConfig does not consult environmentClusterMode; a provided cluster would be deleted")
	}
	if lookup < 0 || del < 0 {
		t.Fatal("down.go no longer locates and deletes a cluster the way this test assumes")
	}
	if gate > lookup || gate > del {
		t.Error("the cluster-mode gate runs AFTER the cluster is located/deleted; it has to come first")
	}
}
