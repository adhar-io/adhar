package utils

import (
	"testing"
)

// Kind decides which engine hosts its nodes from KIND_EXPERIMENTAL_PROVIDER,
// falling back to the first available. Adhar must follow the SAME rule: if it
// picked a different engine, a cluster could be created on one and then managed
// (torn down, preloaded into) with another that cannot see it.
func TestDetectContainerEngineHonoursTheKindOverride(t *testing.T) {
	for _, tc := range []struct {
		env      string
		wantBin  string
		wantName string
	}{
		{env: "podman", wantBin: "podman", wantName: "Podman"},
		{env: "docker", wantBin: "docker", wantName: "Docker"},
		{env: "nerdctl", wantBin: "nerdctl", wantName: "nerdctl"},
		{env: "finch", wantBin: "finch", wantName: "Finch"},
		{env: "nerdctl.lima", wantBin: "nerdctl.lima", wantName: "nerdctl (Lima)"},
		// An engine Adhar does not know is still honoured: the user asked for
		// it explicitly, and silently substituting another would be worse.
		{env: "some-future-engine", wantBin: "some-future-engine", wantName: "some-future-engine"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("KIND_EXPERIMENTAL_PROVIDER", tc.env)
			got := detectContainerEngine()
			if got.Binary != tc.wantBin {
				t.Fatalf("Binary = %q, want %q", got.Binary, tc.wantBin)
			}
			if got.Name != tc.wantName {
				t.Fatalf("Name = %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}

// Whitespace around the value must not produce a binary name that cannot exec.
func TestDetectContainerEngineTrimsTheOverride(t *testing.T) {
	t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "  podman \n")
	if got := detectContainerEngine(); got.Binary != "podman" {
		t.Fatalf("Binary = %q, want %q", got.Binary, "podman")
	}
}

// With no override, detection must fall back to probing — and whatever it
// returns must be usable as an argv[0] rather than empty.
func TestDetectContainerEngineAlwaysNamesABinary(t *testing.T) {
	t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "")
	got := detectContainerEngine()
	if got.Binary == "" {
		t.Fatal("Binary must never be empty; callers exec it directly")
	}
	if got.Name == "" {
		t.Fatal("Name must never be empty; it appears in user-facing messages")
	}
}

// The supported list is what error messages offer the user, so it must be
// non-empty and must lead with Docker — kind's own precedence.
func TestEngineNames(t *testing.T) {
	names := EngineNames()
	if len(names) == 0 {
		t.Fatal("EngineNames must not be empty")
	}
	if names[0] != "docker" {
		t.Fatalf("docker must be tried first to match kind's precedence, got %q", names[0])
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate engine %q", n)
		}
		seen[n] = true
	}
	for _, required := range []string{"docker", "podman", "nerdctl"} {
		if !seen[required] {
			t.Fatalf("engine %q must be supported", required)
		}
	}
}

// DetectContainerEngine caches, because probing spawns a subprocess and the
// engine cannot change within one CLI invocation.
func TestDetectContainerEngineIsCached(t *testing.T) {
	first := DetectContainerEngine()
	second := DetectContainerEngine()
	if first != second {
		t.Fatalf("cached result changed: %+v then %+v", first, second)
	}
}
