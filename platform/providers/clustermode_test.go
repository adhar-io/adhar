package provider

import (
	"strings"
	"testing"
)

// One vocabulary for every provider: compute or managed, nothing else.
//
// It used to be five: `clusterMode: eks|aks|gke|doks|k3s`, a `useManagedK8s:
// true` boolean, and Civo spelling the key `cluster_mode` while accepting only
// "compute" or "k3s". A config could not be moved between clouds, and the
// service name in the value duplicated the provider that was already chosen.
func TestNormalizeClusterModeAcceptsExactlyTwoValues(t *testing.T) {
	for _, in := range []string{"", "compute", "COMPUTE", "  compute  "} {
		got, err := NormalizeClusterMode(in)
		if err != nil || got != ClusterModeCompute {
			t.Errorf("NormalizeClusterMode(%q) = %q, %v; want %q, nil", in, got, err, ClusterModeCompute)
		}
	}
	for _, in := range []string{"managed", "Managed", " managed "} {
		got, err := NormalizeClusterMode(in)
		if err != nil || got != ClusterModeManaged {
			t.Errorf("NormalizeClusterMode(%q) = %q, %v; want %q, nil", in, got, err, ClusterModeManaged)
		}
	}
}

// The per-cloud spellings must be REJECTED, never mapped. Mapping silently is
// the dangerous option: a stale `clusterMode: gke` that resolved to compute
// would build a kubeadm cluster on Compute Engine for someone who asked for
// GKE, and they would discover it from the bill or a missing load balancer.
func TestNormalizeClusterModeRejectsServiceNames(t *testing.T) {
	for _, in := range []string{"eks", "aks", "gke", "doks", "k3s", "EKS"} {
		got, err := NormalizeClusterMode(in)
		if err == nil {
			t.Errorf("NormalizeClusterMode(%q) = %q with no error; a service name must be refused, not mapped", in, got)
			continue
		}
		// The message has to say what to do instead.
		if !strings.Contains(err.Error(), ClusterModeManaged) {
			t.Errorf("the error for %q does not point at %q: %v", in, ClusterModeManaged, err)
		}
	}
	if _, err := NormalizeClusterMode("nonsense"); err == nil {
		t.Error("an unknown mode must be refused")
	}
}

func TestParseClusterModeReadsEitherSpellingAndNesting(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]interface{}
		want string
	}{
		{"absent means compute", map[string]interface{}{}, ClusterModeCompute},
		{"camelCase at the top level", map[string]interface{}{"clusterMode": "managed"}, ClusterModeManaged},
		{"snake_case at the top level", map[string]interface{}{"cluster_mode": "managed"}, ClusterModeManaged},
		{"nested in config:", map[string]interface{}{"config": map[string]interface{}{"cluster_mode": "managed"}}, ClusterModeManaged},
		{"explicit compute", map[string]interface{}{"clusterMode": "compute"}, ClusterModeCompute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClusterMode(tc.cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A removed key must fail the parse, not be ignored. Ignoring `useManagedK8s:
// true` would turn a config that asked for the managed service into a kubeadm
// cluster, which is exactly the silent mode-flip this vocabulary exists to
// prevent.
func TestParseClusterModeRefusesTheRemovedBoolean(t *testing.T) {
	for _, cfg := range []map[string]interface{}{
		{"useManagedK8s": true},
		{"useManagedK8s": false},
		{"config": map[string]interface{}{"useManagedK8s": true}},
	} {
		_, err := ParseClusterMode(cfg)
		if err == nil {
			t.Errorf("ParseClusterMode(%v) succeeded; the removed key must be refused so the config is fixed rather than silently reinterpreted", cfg)
			continue
		}
		if !strings.Contains(err.Error(), "clusterMode") {
			t.Errorf("the error does not name the replacement key: %v", err)
		}
	}
}

func TestClusterModeIsManagedOnlyForManaged(t *testing.T) {
	if !ClusterModeIsManaged(ClusterModeManaged) {
		t.Error("managed must select the managed service")
	}
	for _, m := range []string{"", "compute", "gke", "eks", "k3s"} {
		if ClusterModeIsManaged(m) {
			t.Errorf("%q must not select the managed service", m)
		}
	}
}

// A provided cluster already exists: the platform installs onto it and must
// never create or destroy it. That last part is the whole reason the mode needs
// its own predicate rather than being treated as "not managed" — `adhar down`
// deleting a cluster the operator already had would be the worst possible
// outcome of a teardown, and it is one boolean away.
func TestProvidedModeIsRecognisedAndNeverOurs(t *testing.T) {
	for _, in := range []string{"provided", "Provided", " provided "} {
		got, err := NormalizeClusterMode(in)
		if err != nil || got != ClusterModeProvided {
			t.Errorf("NormalizeClusterMode(%q) = %q, %v; want %q, nil", in, got, err, ClusterModeProvided)
		}
		if !ClusterModeIsProvided(in) {
			t.Errorf("ClusterModeIsProvided(%q) must be true", in)
		}
		if ClusterLifecycleIsOurs(in) {
			t.Errorf("ClusterLifecycleIsOurs(%q) must be FALSE: the platform must never create or delete a cluster it was given", in)
		}
		if ClusterModeIsManaged(in) {
			t.Errorf("%q is not the cloud's managed service", in)
		}
	}
	// The two modes the platform does own.
	for _, in := range []string{"", "compute", "managed"} {
		if !ClusterLifecycleIsOurs(in) {
			t.Errorf("ClusterLifecycleIsOurs(%q) must be true; the platform creates and deletes these", in)
		}
		if ClusterModeIsProvided(in) {
			t.Errorf("ClusterModeIsProvided(%q) must be false", in)
		}
	}
	// And the error for an unknown value has to offer all three.
	_, err := NormalizeClusterMode("nonsense")
	if err == nil {
		t.Fatal("an unknown mode must be refused")
	}
	for _, want := range []string{ClusterModeCompute, ClusterModeManaged, ClusterModeProvided} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not offer %q: %v", want, err)
		}
	}
}
